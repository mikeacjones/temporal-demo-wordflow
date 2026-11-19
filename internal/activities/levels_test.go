package activities

import (
	"context"
	"testing"
	"time"

	"github.com/mjones/temporal-word-game/internal/campaign"
	"github.com/mjones/temporal-word-game/internal/catalog"
	"github.com/mjones/temporal-word-game/internal/game"
	"github.com/mjones/temporal-word-game/internal/player"
	"github.com/mjones/temporal-word-game/internal/store"
	"github.com/mjones/temporal-word-game/internal/workflows"

	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/temporal"
)

// fakeLevelStore keeps one player and the campaigns in memory.
type fakeLevelStore struct {
	campaigns map[string]catalog.Registration
	players   map[string]player.State
	writes    int
}

func (f *fakeLevelStore) Campaign(_ context.Context, campaignID string) (catalog.Registration, error) {
	registration, ok := f.campaigns[campaignID]
	if !ok {
		return catalog.Registration{}, store.ErrNotFound
	}
	return registration, nil
}

func (f *fakeLevelStore) UpdatePlayer(_ context.Context, playerID string, change func(*player.State) error) (player.State, error) {
	state, ok := f.players[playerID]
	if !ok {
		return player.State{}, store.ErrNotFound
	}
	if err := change(&state); err != nil {
		return player.State{}, err
	}
	f.players[playerID] = state
	f.writes++
	return state, nil
}

func newFakeLevelStore() *fakeLevelStore {
	return &fakeLevelStore{
		campaigns: map[string]catalog.Registration{"campaign": {
			WorkflowID: "wordflow-campaign/campaign",
			Definition: campaign.Definition{
				ID: "campaign", Unlock: campaign.UnlockPolicy{InitialLevels: 3},
				Game: campaign.GameSummary{ID: campaign.GameWordflow, Title: "Wordflow"},
			},
			Levels: []game.Puzzle{{Level: 1, Title: "One", Letters: "ONE"}, {Level: 2, Title: "Two", Letters: "TWO"}},
		}},
		players: map[string]player.State{"alice": player.New("alice", "Alice#1", "hash", time.Now())},
	}
}

func claim(level int, workflowID string) workflows.ClaimLevelActivityInput {
	return workflows.ClaimLevelActivityInput{
		PlayerID: "alice", CampaignID: "campaign", Level: level,
		LevelWorkflowID: workflowID, StartedAt: time.Now(),
	}
}

func requireApplicationError(t *testing.T, err error, errorType string) {
	t.Helper()
	var applicationError *temporal.ApplicationError
	require.ErrorAs(t, err, &applicationError)
	require.Equal(t, errorType, applicationError.Type())
	require.True(t, applicationError.NonRetryable())
}

func TestClaimLevelOpensTheNextLevelAndIsSafeToRepeat(t *testing.T) {
	data := newFakeLevelStore()
	levels := &Levels{Store: data}
	levelID := workflows.WordflowLevelWorkflowID("alice", "campaign", 1)

	first, err := levels.ClaimLevel(context.Background(), claim(1, levelID))
	require.NoError(t, err)
	require.Equal(t, "One", first.Puzzle.Title)
	require.Equal(t, 2, first.TotalLevels)
	require.Equal(t, levelID, data.players["alice"].ActiveGame.WorkflowID)
	require.Equal(t, 1, first.Player.ActiveGame.Level)

	again, err := levels.ClaimLevel(context.Background(), claim(1, levelID))
	require.NoError(t, err, "an Activity retry after the claim landed")
	require.Equal(t, first.Puzzle, again.Puzzle)
}

func TestClaimLevelRefusesWhatTheCatalogDoesNotAllow(t *testing.T) {
	data := newFakeLevelStore()
	levels := &Levels{Store: data}

	_, err := levels.ClaimLevel(context.Background(), claim(2, "wordflow-level/alice/campaign/2"))
	requireApplicationError(t, err, "level_not_next")

	_, err = levels.ClaimLevel(context.Background(), workflows.ClaimLevelActivityInput{
		PlayerID: "alice", CampaignID: "missing", Level: 1, LevelWorkflowID: "x", StartedAt: time.Now(),
	})
	requireApplicationError(t, err, "campaign_not_found")

	_, err = levels.ClaimLevel(context.Background(), workflows.ClaimLevelActivityInput{
		PlayerID: "bob", CampaignID: "campaign", Level: 1, LevelWorkflowID: "x", StartedAt: time.Now(),
	})
	requireApplicationError(t, err, "player_not_found")

	_, err = levels.ClaimLevel(context.Background(), claim(1, "wordflow-level/alice/campaign/1"))
	require.NoError(t, err)
	_, err = levels.ClaimLevel(context.Background(), claim(1, "another-run-of-level-1"))
	requireApplicationError(t, err, "level_locked") // the catalog shows level 1 as already open
	require.Nil(t, data.players["alice"].Rewards)
}

func TestSpendPointsAndRecordLevelResult(t *testing.T) {
	data := newFakeLevelStore()
	levels := &Levels{Store: data}
	levelID := workflows.WordflowLevelWorkflowID("alice", "campaign", 1)
	_, err := levels.ClaimLevel(context.Background(), claim(1, levelID))
	require.NoError(t, err)
	alice := data.players["alice"]
	alice.Points = 20
	data.players["alice"] = alice

	spent, err := levels.SpendPoints(context.Background(), workflows.SpendPointsActivityInput{
		PlayerID: "alice", LevelWorkflowID: levelID, Amount: 15,
	})
	require.NoError(t, err)
	require.Equal(t, workflows.SpendPointsResult{Spent: 15, Remaining: 5}, spent)
	_, err = levels.SpendPoints(context.Background(), workflows.SpendPointsActivityInput{
		PlayerID: "alice", LevelWorkflowID: levelID, Amount: 15,
	})
	requireApplicationError(t, err, "insufficient_points")

	result := workflows.RecordLevelResultActivityInput{PlayerID: "alice", LevelWorkflowID: levelID, Result: campaign.LevelResult{
		CampaignID: "campaign", Level: 1, CompletedAt: time.Now(),
		Awards: []campaign.PointAward{{Description: "Wordflow score", Points: 30}},
	}}
	require.NoError(t, levels.RecordLevelResult(context.Background(), result))
	require.NoError(t, levels.RecordLevelResult(context.Background(), result), "a retry is ignored")
	alice = data.players["alice"]
	require.Nil(t, alice.ActiveGame)
	require.Equal(t, 35, alice.Points)
	require.Equal(t, 30, alice.LifetimePointsEarned)
	require.Equal(t, 2, alice.Campaigns[0].NextLevel)
}
