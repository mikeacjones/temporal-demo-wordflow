package catalog

import (
	"testing"
	"time"

	"github.com/mjones/temporal-word-game/internal/campaign"
	"github.com/mjones/temporal-word-game/internal/game"

	"github.com/stretchr/testify/require"
)

func testRegistration(levels int, unlock campaign.UnlockPolicy) Registration {
	registration := Registration{
		WorkflowID: "wordflow-campaign/test",
		Definition: campaign.Definition{
			ID: "test", Title: "Test", Unlock: unlock,
			Game: campaign.GameSummary{ID: campaign.GameWordflow, Title: "Wordflow"},
		},
	}
	for level := 1; level <= levels; level++ {
		registration.Levels = append(registration.Levels, game.Puzzle{Level: level, Title: "Level", Letters: "ONE"})
	}
	return registration
}

func TestRequirementsAndEnrollmentUnlockSchedule(t *testing.T) {
	now := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	definition := campaign.Definition{
		Requirements: campaign.Requirements{
			Campaigns: []string{"first"},
			Levels:    []campaign.RequiredLevel{{CampaignID: "second", Level: 3}},
		},
	}
	player := campaign.PlayerProgress{Campaigns: []campaign.PlayerCampaignProgress{
		{CampaignID: "first", Completed: true},
		{CampaignID: "second", CompletedLevels: 3},
	}}
	eligible, reason := eligibility(definition, player, campaign.StatusActive)
	require.True(t, eligible)
	require.Empty(t, reason)

	policy := campaign.UnlockPolicy{InitialLevels: 3, LevelsPerInterval: 3, Interval: 24 * time.Hour}
	require.Equal(t, 3, unlockedLevels(policy, now, now.Add(23*time.Hour), 30))
	require.Equal(t, 6, unlockedLevels(policy, now, now.Add(24*time.Hour), 30))
	require.Equal(t, 30, unlockedLevels(policy, now, now.Add(30*24*time.Hour), 30))
}

func TestSingleAttemptCampaignLocksAfterTimeout(t *testing.T) {
	definition := campaign.Definition{ID: "daily", SingleAttempt: true}
	player := campaign.PlayerProgress{Campaigns: []campaign.PlayerCampaignProgress{{
		CampaignID: "daily", NextLevel: 2, CompletedLevels: 1, Failed: true,
	}}}

	eligible, reason := eligibility(definition, player, campaign.StatusActive)
	require.False(t, eligible)
	require.Equal(t, "Today's daily challenge attempt is over.", reason)
}

func TestStatusUsesConfiguredWindow(t *testing.T) {
	startsAt := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	endsAt := startsAt.Add(48 * time.Hour)
	definition := campaign.Definition{StartsAt: &startsAt, EndsAt: &endsAt}

	require.Equal(t, campaign.StatusUpcoming, Status(definition, startsAt.Add(-time.Second)))
	require.Equal(t, campaign.StatusActive, Status(definition, startsAt))
	require.Equal(t, campaign.StatusEnded, Status(definition, endsAt))
}

func TestResolveAllowsOnlyTheNextUnlockedLevel(t *testing.T) {
	now := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	registration := testRegistration(3, campaign.UnlockPolicy{InitialLevels: 1, LevelsPerInterval: 1, Interval: 24 * time.Hour})
	joined := campaign.PlayerProgress{Campaigns: []campaign.PlayerCampaignProgress{{
		CampaignID: "test", JoinedAt: now, NextLevel: 2, CompletedLevels: 1,
	}}}

	resolution := Resolve(registration, campaign.PlayerProgress{}, 1, now)
	require.True(t, resolution.Allowed)
	require.Equal(t, registration.Levels[0], resolution.Puzzle)
	require.Equal(t, 3, resolution.TotalLevels)

	require.Equal(t, "level_not_next", Resolve(registration, joined, 3, now).ErrorType)
	require.Equal(t, "level_locked", Resolve(registration, joined, 2, now).ErrorType, "level 2 unlocks a day after joining")
	require.True(t, Resolve(registration, joined, 2, now.Add(24*time.Hour)).Allowed)

	elsewhere := campaign.PlayerProgress{ActiveCampaignID: "other", ActiveLevel: 1}
	require.Equal(t, "campaign_locked", Resolve(registration, elsewhere, 1, now).ErrorType)
}

func TestValidateRefusesWhatTheGameCannotRun(t *testing.T) {
	require.NoError(t, Validate(testRegistration(1, campaign.UnlockPolicy{InitialLevels: 1})))
	require.ErrorContains(t, ValidateLevels([]game.Puzzle{{Level: 1, Letters: "ABCDEFGHI"}}), "more than 8 letters")
	require.ErrorContains(t, ValidateLevels([]game.Puzzle{{Level: 2, Letters: "ONE", TimeLimitSeconds: -1}}), "invalid gameplay settings")
	incomplete := testRegistration(1, campaign.UnlockPolicy{})
	incomplete.WorkflowID = ""
	require.ErrorContains(t, Validate(incomplete), "incomplete")
}
