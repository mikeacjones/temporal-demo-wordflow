package store

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/mjones/temporal-word-game/internal/campaign"
	"github.com/mjones/temporal-word-game/internal/catalog"
	"github.com/mjones/temporal-word-game/internal/game"
	"github.com/mjones/temporal-word-game/internal/player"

	"github.com/stretchr/testify/require"
)

// testStoreEndpointEnv points the store tests at DynamoDB Local; `make test`
// runs them in Compose. Without it they skip.
const testStoreEndpointEnv = "WORDFLOW_TEST_DYNAMODB_ENDPOINT"

// newTestStore gives each test its own pair of tables.
func newTestStore(t *testing.T) *Store {
	t.Helper()
	endpoint := os.Getenv(testStoreEndpointEnv)
	if endpoint == "" {
		t.Skip(testStoreEndpointEnv + " is not set")
	}
	suffix := make([]byte, 4)
	_, _ = rand.Read(suffix)
	name := "test-" + hex.EncodeToString(suffix)
	store, err := Open(context.Background(), Config{
		PlayersTable: name + "-players", CampaignsTable: name + "-campaigns", Endpoint: endpoint,
	})
	require.NoError(t, err)
	require.NoError(t, store.CreateTables(context.Background()))
	require.NoError(t, store.CreateTables(context.Background()), "creating existing tables is a no-op")
	return store
}

func TestPlayerLifecycle(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	alice := player.New("alice", "Alice#1", "hash", time.Now().UTC())

	_, err := store.Player(ctx, "alice")
	require.ErrorIs(t, err, ErrNotFound)
	require.NoError(t, store.CreatePlayer(ctx, alice))
	require.ErrorIs(t, store.CreatePlayer(ctx, alice), ErrExists)

	read, err := store.Player(ctx, "alice")
	require.NoError(t, err)
	require.Equal(t, "Alice#1", read.DisplayName)

	refusal := errors.New("refused")
	_, err = store.UpdatePlayer(ctx, "alice", func(*player.State) error { return refusal })
	require.ErrorIs(t, err, refusal)

	updated, err := store.UpdatePlayer(ctx, "alice", func(state *player.State) error {
		state.Points = 40
		return nil
	})
	require.NoError(t, err)
	require.Equal(t, 40, updated.Points)
	_, err = store.UpdatePlayer(ctx, "bob", func(*player.State) error { return nil })
	require.ErrorIs(t, err, ErrNotFound)
}

func TestConcurrentUpdatesAreNotLost(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	require.NoError(t, store.CreatePlayer(ctx, player.New("alice", "Alice#1", "hash", time.Now().UTC())))

	// Two writers at a time stay within the retry budget; each must land once.
	var wait sync.WaitGroup
	errs := make(chan error, 8)
	for range 4 {
		wait.Add(2)
		for range 2 {
			go func() {
				defer wait.Done()
				_, err := store.UpdatePlayer(ctx, "alice", func(state *player.State) error {
					state.Points++
					return nil
				})
				errs <- err
			}()
		}
		wait.Wait()
	}
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	read, err := store.Player(ctx, "alice")
	require.NoError(t, err)
	require.Equal(t, 8, read.Points)
}

func TestLeaderboardListsOnlyPlayersWithPointsBestFirst(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	for name, points := range map[string]int{"alice": 40, "bob": 90, "carol": 0} {
		account := player.New(name, name, "hash", time.Now().UTC())
		account.LifetimePointsEarned = points
		account.CompletedLevels = make([]game.LevelCompletion, points/10)
		require.NoError(t, store.CreatePlayer(ctx, account))
	}

	ranks, err := store.Leaderboard(ctx, 10)
	require.NoError(t, err)
	require.Equal(t, []game.LeaderboardRank{
		{DisplayName: "bob", LifetimePointsEarned: 90, CompletedLevels: 9, Rank: 1},
		{DisplayName: "alice", LifetimePointsEarned: 40, CompletedLevels: 4, Rank: 2},
	}, ranks)

	ranks, err = store.Leaderboard(ctx, 1)
	require.NoError(t, err)
	require.Len(t, ranks, 1)
}

func TestCampaignsRegisterOncePerWorkflowAndListInOrder(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	registration := func(id, workflowID string) catalog.Registration {
		return catalog.Registration{
			WorkflowID: workflowID,
			Definition: campaign.Definition{ID: id, Title: id, Game: campaign.GameSummary{ID: campaign.GameWordflow, Title: "Wordflow"}},
			Levels:     []game.Puzzle{{Level: 1, Title: "One", Letters: "ONE"}},
		}
	}
	now := time.Now().UTC()

	require.NoError(t, store.PutCampaign(ctx, registration("second", "wordflow-campaign/second"), now.Add(time.Second)))
	require.NoError(t, store.PutCampaign(ctx, registration("first", "wordflow-campaign/first"), now))
	require.NoError(t, store.PutCampaign(ctx, registration("first", "wordflow-campaign/first"), now), "the same Workflow registering again is a no-op")
	require.ErrorIs(t, store.PutCampaign(ctx, registration("first", "someone-else"), now), ErrCampaignClaimed)

	listed, err := store.Campaigns(ctx)
	require.NoError(t, err)
	require.Len(t, listed, 2)
	require.Equal(t, "first", listed[0].Definition.ID)
	require.Equal(t, "second", listed[1].Definition.ID)

	read, err := store.Campaign(ctx, "first")
	require.NoError(t, err)
	require.Equal(t, registration("first", "wordflow-campaign/first"), read)

	require.NoError(t, store.DeleteCampaign(ctx, "first", "someone-else"), "another Workflow can't take it down")
	_, err = store.Campaign(ctx, "first")
	require.NoError(t, err)
	require.NoError(t, store.DeleteCampaign(ctx, "first", "wordflow-campaign/first"))
	_, err = store.Campaign(ctx, "first")
	require.ErrorIs(t, err, ErrNotFound)
	require.NoError(t, store.DeleteCampaign(ctx, "first", "wordflow-campaign/first"), "already gone is fine")
}
