package player

import (
	"testing"
	"time"

	"github.com/mjones/temporal-word-game/internal/campaign"
	"github.com/mjones/temporal-word-game/internal/game"

	"github.com/stretchr/testify/require"
)

const levelID = "wordflow-level/player/campaign/5"

func TestCompletionAdvancesCampaignAndAwardsPointsOnce(t *testing.T) {
	completedAt := time.Date(2026, time.September, 18, 12, 0, 0, 0, time.UTC)
	state := State{
		PlayerID: "player",
		Campaigns: []campaign.PlayerCampaignProgress{{
			CampaignID: "campaign", NextLevel: 5, TotalLevels: 5, CompletedLevels: 4,
		}},
		ActiveGame:    &game.ActiveGame{WorkflowID: levelID, CampaignID: "campaign", Level: 5, TotalLevels: 5},
		CurrentStreak: 2, BestStreak: 2,
		LastCompletionDay: dayKey(completedAt.AddDate(0, 0, -1)),
	}
	result := campaign.LevelResult{
		GameID: campaign.GameWordflow, CampaignID: "campaign", Level: 5,
		Attempts: 8, CompletedAt: completedAt,
		Awards: []campaign.PointAward{
			{Description: "Wordflow score", Points: 32},
			{Description: "Level milestone", Points: 50},
			{Description: "Test Event", Points: 25},
		},
	}

	require.True(t, state.FinishLevel(levelID, result))
	require.Nil(t, state.ActiveGame)
	require.Equal(t, 6, state.Campaigns[0].NextLevel)
	require.True(t, state.Campaigns[0].Completed)
	require.Equal(t, 3, state.CurrentStreak)
	require.Equal(t, 107, state.Points)
	require.Equal(t, 107, state.LifetimePointsEarned)
	require.Equal(t, 107, state.CompletedLevels[0].Points)
	require.Len(t, state.Rewards, 3)

	require.False(t, state.FinishLevel(levelID, result), "a repeated result is ignored")
	require.Equal(t, 107, state.Points)
	require.Len(t, state.CompletedLevels, 1)
}

func TestTimeoutEndsAttemptWithoutAdvancingOrAwardingPoints(t *testing.T) {
	timedOutAt := time.Date(2026, time.September, 21, 12, 0, 0, 0, time.UTC)
	state := State{
		PlayerID: "player",
		Campaigns: []campaign.PlayerCampaignProgress{{
			CampaignID: "daily", NextLevel: 2, TotalLevels: 3, CompletedLevels: 1,
		}},
		ActiveGame: &game.ActiveGame{WorkflowID: levelID, CampaignID: "daily", Level: 2, TotalLevels: 3},
		Points:     50,
	}

	require.True(t, state.FinishLevel(levelID, campaign.LevelResult{
		GameID: campaign.GameWordflow, CampaignID: "daily", Level: 2,
		Attempts: 4, CompletedAt: timedOutAt, TimedOut: true,
	}))

	require.Nil(t, state.ActiveGame)
	require.True(t, state.Campaigns[0].Failed)
	require.Equal(t, 2, state.Campaigns[0].NextLevel)
	require.Equal(t, 1, state.Campaigns[0].CompletedLevels)
	require.Equal(t, 50, state.Points)
	require.Empty(t, state.CompletedLevels)
	require.Empty(t, state.Rewards)
}

func TestAbandonedLevelCanBeStartedAgain(t *testing.T) {
	now := time.Date(2026, time.September, 21, 12, 0, 0, 0, time.UTC)
	state := New("player", "Player", "hash", now)
	active := game.ActiveGame{WorkflowID: levelID, CampaignID: "campaign", Level: 1, TotalLevels: 3}
	require.NoError(t, state.StartLevel(active, 3, now))

	require.True(t, state.FinishLevel(levelID, campaign.LevelResult{
		CampaignID: "campaign", Level: 1, CompletedAt: now.Add(25 * time.Hour), Abandoned: true,
	}))

	require.Nil(t, state.ActiveGame)
	require.False(t, state.Campaigns[0].Failed)
	require.Equal(t, 1, state.Campaigns[0].NextLevel)
	require.NoError(t, state.StartLevel(active, 3, now.Add(26*time.Hour)))
}

func TestOnlyOneLevelOpenAtATime(t *testing.T) {
	now := time.Date(2026, time.September, 21, 12, 0, 0, 0, time.UTC)
	state := New("player", "Player", "hash", now)
	require.NoError(t, state.StartLevel(game.ActiveGame{WorkflowID: levelID, CampaignID: "campaign", Level: 1}, 3, now))
	require.Equal(t, []campaign.PlayerCampaignProgress{{CampaignID: "campaign", JoinedAt: now, NextLevel: 1, TotalLevels: 3}}, state.Campaigns)

	err := state.StartLevel(game.ActiveGame{WorkflowID: "other", CampaignID: "other", Level: 1}, 2, now)
	require.ErrorContains(t, err, "finish the active level")
	require.True(t, IsRule(err))
}

func TestSpendPointsOnlyInTheOpenLevelAndWithinTheBalance(t *testing.T) {
	state := State{Points: 25, ActiveGame: &game.ActiveGame{WorkflowID: levelID}}
	now := time.Now()

	require.NoError(t, state.SpendPoints(levelID, 20, now))
	require.Equal(t, 5, state.Points)
	require.ErrorContains(t, state.SpendPoints(levelID, 10, now), "not enough points")
	require.ErrorContains(t, state.SpendPoints("wordflow-level/another/campaign/1", 1, now), "not active")
	require.Equal(t, 5, state.Points)
}

func TestStreakFreezeProtectsOneMissedDay(t *testing.T) {
	now := time.Date(2026, time.September, 20, 12, 0, 0, 0, time.UTC)
	state := State{
		CurrentStreak:     4,
		LastCompletionDay: dayKey(now.In(Location).AddDate(0, 0, -2)),
		StreakFreeze:      true,
	}

	state.SignIn(now)
	require.Equal(t, 4, state.CurrentStreak)
	require.False(t, state.StreakFreeze)
	require.Equal(t, dayKey(now.In(Location).AddDate(0, 0, -1)), state.LastCompletionDay)

	state.SignIn(now.AddDate(0, 0, 2))
	require.Zero(t, state.CurrentStreak, "a second missed day ends the streak")
}

func TestBuyStreakFreezeDeductsPoints(t *testing.T) {
	state := State{Points: StreakFreezePointCost}

	require.NoError(t, state.BuyStreakFreeze(time.Now()))
	require.True(t, state.StreakFreeze)
	require.Zero(t, state.Points)
	require.ErrorContains(t, state.BuyStreakFreeze(time.Now()), "already ready")
}

func TestCheckPassword(t *testing.T) {
	state := New("player", "Player", "hash", time.Now())
	require.NoError(t, state.CheckPassword("hash"))
	require.True(t, IsRule(state.CheckPassword("other")))
	require.True(t, IsRule(state.CheckPassword("")))
}
