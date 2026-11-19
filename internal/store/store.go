// Package store keeps Wordflow's long-term data in DynamoDB: player records
// in one table, with a leaderboard index, and registered campaigns in another.
// Workflows keep what a level or campaign window needs while it runs; this is
// what outlives them.
package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"time"

	"github.com/mjones/temporal-word-game/internal/catalog"
	"github.com/mjones/temporal-word-game/internal/game"
	"github.com/mjones/temporal-word-game/internal/player"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

// The environment that names the tables. On the demo platform, spec.aws in the
// DemoProject creates the tables and sets the first two; DynamoDBEndpointEnv
// points at DynamoDB Local in development.
const (
	PlayersTableEnv     = "WORDFLOW_PLAYERS_TABLE"
	CampaignsTableEnv   = "WORDFLOW_CAMPAIGNS_TABLE"
	DynamoDBEndpointEnv = "DYNAMODB_ENDPOINT"
)

// The tables' keys and index. They match the DemoProject's spec.aws, and
// DynamoDB can't change them once a table exists.
const (
	playerIDKey        = "playerId"
	campaignIDKey      = "campaignId"
	leaderboardIndex   = "leaderboard"
	boardKey           = "board"
	lifetimePointsKey  = "lifetimePoints"
	leaderboardBoard   = "global"
	versionAttribute   = "version"
	stateAttribute     = "state"
	workflowIDKey      = "workflowId"
	registeredAtKey    = "registeredAt"
	playerWriteRetries = 5
)

var (
	ErrNotFound = errors.New("not found")
	// ErrExists is a player ID someone has already signed up with.
	ErrExists = errors.New("already exists")
	// ErrCampaignClaimed is a campaign ID another Campaign Workflow registered.
	ErrCampaignClaimed = errors.New("campaign ID is registered by another workflow")
)

type Config struct {
	PlayersTable   string
	CampaignsTable string
	// Endpoint overrides the DynamoDB endpoint, for DynamoDB Local.
	Endpoint string
}

// ConfigFromEnv reads the table names, and fails naming every one that is missing.
func ConfigFromEnv(getenv func(string) string) (Config, error) {
	config := Config{
		PlayersTable:   getenv(PlayersTableEnv),
		CampaignsTable: getenv(CampaignsTableEnv),
		Endpoint:       getenv(DynamoDBEndpointEnv),
	}
	var missing []string
	if config.PlayersTable == "" {
		missing = append(missing, PlayersTableEnv)
	}
	if config.CampaignsTable == "" {
		missing = append(missing, CampaignsTableEnv)
	}
	if len(missing) > 0 {
		return Config{}, fmt.Errorf("%v must be set to the DynamoDB table names", missing)
	}
	return config, nil
}

type Store struct {
	db        *dynamodb.Client
	players   string
	campaigns string
}

// Open connects with the AWS SDK's default credential chain, which picks up
// EKS Pod Identity on the platform.
func Open(ctx context.Context, config Config) (*Store, error) {
	awsConfig, err := awsconfig.LoadDefaultConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("load AWS config: %w", err)
	}
	db := dynamodb.NewFromConfig(awsConfig, func(options *dynamodb.Options) {
		if config.Endpoint != "" {
			options.BaseEndpoint = aws.String(config.Endpoint)
		}
	})
	return &Store{db: db, players: config.PlayersTable, campaigns: config.CampaignsTable}, nil
}

// CreatePlayer saves a new player, failing with ErrExists if the ID is taken.
func (s *Store) CreatePlayer(ctx context.Context, state player.State) error {
	item, err := playerItem(state, 1)
	if err != nil {
		return err
	}
	_, err = s.db.PutItem(ctx, &dynamodb.PutItemInput{
		TableName:           aws.String(s.players),
		Item:                item,
		ConditionExpression: aws.String("attribute_not_exists(#id)"),
		ExpressionAttributeNames: map[string]string{
			"#id": playerIDKey,
		},
	})
	if isConditionFailed(err) {
		return ErrExists
	}
	if err != nil {
		return fmt.Errorf("create player %s: %w", state.PlayerID, err)
	}
	return nil
}

// Player reads a player, failing with ErrNotFound if they never signed up.
func (s *Store) Player(ctx context.Context, playerID string) (player.State, error) {
	state, _, err := s.readPlayer(ctx, playerID)
	return state, err
}

// UpdatePlayer applies change to the latest copy of a player and saves it,
// unless another write got there first, in which case it reads and applies
// change again. change's error is returned as is, and nothing is saved.
func (s *Store) UpdatePlayer(ctx context.Context, playerID string, change func(*player.State) error) (player.State, error) {
	for range playerWriteRetries {
		state, version, err := s.readPlayer(ctx, playerID)
		if err != nil {
			return player.State{}, err
		}
		if err := change(&state); err != nil {
			return player.State{}, err
		}
		item, err := playerItem(state, version+1)
		if err != nil {
			return player.State{}, err
		}
		_, err = s.db.PutItem(ctx, &dynamodb.PutItemInput{
			TableName:                aws.String(s.players),
			Item:                     item,
			ConditionExpression:      aws.String("#version = :version"),
			ExpressionAttributeNames: map[string]string{"#version": versionAttribute},
			ExpressionAttributeValues: map[string]types.AttributeValue{
				":version": number(version),
			},
		})
		if isConditionFailed(err) {
			continue
		}
		if err != nil {
			return player.State{}, fmt.Errorf("save player %s: %w", playerID, err)
		}
		return state, nil
	}
	return player.State{}, fmt.Errorf("save player %s: still changing after %d attempts", playerID, playerWriteRetries)
}

// Leaderboard returns the players with the most lifetime points, best first.
func (s *Store) Leaderboard(ctx context.Context, limit int) ([]game.LeaderboardRank, error) {
	output, err := s.db.Query(ctx, &dynamodb.QueryInput{
		TableName:                aws.String(s.players),
		IndexName:                aws.String(leaderboardIndex),
		KeyConditionExpression:   aws.String("#board = :board"),
		ExpressionAttributeNames: map[string]string{"#board": boardKey},
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":board": &types.AttributeValueMemberS{Value: leaderboardBoard},
		},
		ScanIndexForward: aws.Bool(false),
		Limit:            aws.Int32(int32(limit)),
	})
	if err != nil {
		return nil, fmt.Errorf("query leaderboard: %w", err)
	}
	ranks := make([]game.LeaderboardRank, 0, len(output.Items))
	for index, item := range output.Items {
		state, _, err := decodePlayer(item)
		if err != nil {
			return nil, err
		}
		ranks = append(ranks, game.LeaderboardRank{
			DisplayName:          state.DisplayName,
			LifetimePointsEarned: state.LifetimePointsEarned,
			CompletedLevels:      len(state.CompletedLevels),
			Rank:                 index + 1,
		})
	}
	return ranks, nil
}

// PutCampaign registers a campaign. Registering the same campaign from the same
// Workflow again is a no-op; ErrCampaignClaimed means another Workflow owns the ID.
func (s *Store) PutCampaign(ctx context.Context, registration catalog.Registration, registeredAt time.Time) error {
	encoded, err := json.Marshal(registration)
	if err != nil {
		return fmt.Errorf("encode campaign %s: %w", registration.Definition.ID, err)
	}
	_, err = s.db.PutItem(ctx, &dynamodb.PutItemInput{
		TableName: aws.String(s.campaigns),
		Item: map[string]types.AttributeValue{
			campaignIDKey:   &types.AttributeValueMemberS{Value: registration.Definition.ID},
			workflowIDKey:   &types.AttributeValueMemberS{Value: registration.WorkflowID},
			registeredAtKey: &types.AttributeValueMemberS{Value: registeredAt.UTC().Format(time.RFC3339Nano)},
			stateAttribute:  &types.AttributeValueMemberS{Value: string(encoded)},
		},
		ConditionExpression:      aws.String("attribute_not_exists(#id) OR #workflow = :workflow"),
		ExpressionAttributeNames: map[string]string{"#id": campaignIDKey, "#workflow": workflowIDKey},
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":workflow": &types.AttributeValueMemberS{Value: registration.WorkflowID},
		},
	})
	if isConditionFailed(err) {
		return ErrCampaignClaimed
	}
	if err != nil {
		return fmt.Errorf("register campaign %s: %w", registration.Definition.ID, err)
	}
	return nil
}

// DeleteCampaign removes a campaign that workflowID registered. A campaign
// that is already gone, or owned by another Workflow, is left as it is.
func (s *Store) DeleteCampaign(ctx context.Context, campaignID, workflowID string) error {
	_, err := s.db.DeleteItem(ctx, &dynamodb.DeleteItemInput{
		TableName:                aws.String(s.campaigns),
		Key:                      map[string]types.AttributeValue{campaignIDKey: &types.AttributeValueMemberS{Value: campaignID}},
		ConditionExpression:      aws.String("#workflow = :workflow"),
		ExpressionAttributeNames: map[string]string{"#workflow": workflowIDKey},
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":workflow": &types.AttributeValueMemberS{Value: workflowID},
		},
	})
	if err != nil && !isConditionFailed(err) {
		return fmt.Errorf("unregister campaign %s: %w", campaignID, err)
	}
	return nil
}

// Campaign reads one registered campaign, failing with ErrNotFound.
func (s *Store) Campaign(ctx context.Context, campaignID string) (catalog.Registration, error) {
	output, err := s.db.GetItem(ctx, &dynamodb.GetItemInput{
		TableName:      aws.String(s.campaigns),
		Key:            map[string]types.AttributeValue{campaignIDKey: &types.AttributeValueMemberS{Value: campaignID}},
		ConsistentRead: aws.Bool(true),
	})
	if err != nil {
		return catalog.Registration{}, fmt.Errorf("read campaign %s: %w", campaignID, err)
	}
	if output.Item == nil {
		return catalog.Registration{}, ErrNotFound
	}
	registration, _, err := decodeCampaign(output.Item)
	return registration, err
}

// Campaigns lists every registered campaign, oldest registration first. The
// table holds tens of campaigns, so a Scan is the simple and cheap read.
func (s *Store) Campaigns(ctx context.Context) ([]catalog.Registration, error) {
	type registered struct {
		registration catalog.Registration
		at           string
	}
	var all []registered
	paginator := dynamodb.NewScanPaginator(s.db, &dynamodb.ScanInput{TableName: aws.String(s.campaigns)})
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("list campaigns: %w", err)
		}
		for _, item := range page.Items {
			registration, at, err := decodeCampaign(item)
			if err != nil {
				return nil, err
			}
			all = append(all, registered{registration: registration, at: at})
		}
	}
	sort.SliceStable(all, func(i, j int) bool {
		if all[i].at != all[j].at {
			return all[i].at < all[j].at
		}
		return all[i].registration.Definition.ID < all[j].registration.Definition.ID
	})
	registrations := make([]catalog.Registration, len(all))
	for index, entry := range all {
		registrations[index] = entry.registration
	}
	return registrations, nil
}

func (s *Store) readPlayer(ctx context.Context, playerID string) (player.State, int64, error) {
	output, err := s.db.GetItem(ctx, &dynamodb.GetItemInput{
		TableName:      aws.String(s.players),
		Key:            map[string]types.AttributeValue{playerIDKey: &types.AttributeValueMemberS{Value: playerID}},
		ConsistentRead: aws.Bool(true),
	})
	if err != nil {
		return player.State{}, 0, fmt.Errorf("read player %s: %w", playerID, err)
	}
	if output.Item == nil {
		return player.State{}, 0, ErrNotFound
	}
	return decodePlayer(output.Item)
}

// playerItem stores the whole record as JSON, with the key, the version and
// the leaderboard index's attributes alongside it. Only players who have
// earned points get the index attributes, so the index lists only them.
func playerItem(state player.State, version int64) (map[string]types.AttributeValue, error) {
	encoded, err := json.Marshal(state)
	if err != nil {
		return nil, fmt.Errorf("encode player %s: %w", state.PlayerID, err)
	}
	item := map[string]types.AttributeValue{
		playerIDKey:      &types.AttributeValueMemberS{Value: state.PlayerID},
		versionAttribute: number(version),
		stateAttribute:   &types.AttributeValueMemberS{Value: string(encoded)},
	}
	if state.LifetimePointsEarned > 0 {
		item[boardKey] = &types.AttributeValueMemberS{Value: leaderboardBoard}
		item[lifetimePointsKey] = number(int64(state.LifetimePointsEarned))
	}
	return item, nil
}

func decodePlayer(item map[string]types.AttributeValue) (player.State, int64, error) {
	encoded, ok := item[stateAttribute].(*types.AttributeValueMemberS)
	if !ok {
		return player.State{}, 0, errors.New("player item has no state")
	}
	var state player.State
	if err := json.Unmarshal([]byte(encoded.Value), &state); err != nil {
		return player.State{}, 0, fmt.Errorf("decode player: %w", err)
	}
	version, ok := item[versionAttribute].(*types.AttributeValueMemberN)
	if !ok {
		return player.State{}, 0, fmt.Errorf("player item %s has no version", state.PlayerID)
	}
	parsed, err := strconv.ParseInt(version.Value, 10, 64)
	if err != nil {
		return player.State{}, 0, fmt.Errorf("decode player %s version: %w", state.PlayerID, err)
	}
	return state, parsed, nil
}

func decodeCampaign(item map[string]types.AttributeValue) (catalog.Registration, string, error) {
	encoded, ok := item[stateAttribute].(*types.AttributeValueMemberS)
	if !ok {
		return catalog.Registration{}, "", errors.New("campaign item has no registration")
	}
	var registration catalog.Registration
	if err := json.Unmarshal([]byte(encoded.Value), &registration); err != nil {
		return catalog.Registration{}, "", fmt.Errorf("decode campaign: %w", err)
	}
	registeredAt := ""
	if value, ok := item[registeredAtKey].(*types.AttributeValueMemberS); ok {
		registeredAt = value.Value
	}
	return registration, registeredAt, nil
}

func number(value int64) *types.AttributeValueMemberN {
	return &types.AttributeValueMemberN{Value: strconv.FormatInt(value, 10)}
}

func isConditionFailed(err error) bool {
	var conditionFailed *types.ConditionalCheckFailedException
	return errors.As(err, &conditionFailed)
}
