// Package catalog decides what a player sees and may play: each registered
// campaign's card, the status of its levels, and whether a level may start.
// It holds no state; the registrations live in the store.
package catalog

import (
	"errors"
	"fmt"
	"time"

	"github.com/mjones/temporal-word-game/internal/campaign"
	"github.com/mjones/temporal-word-game/internal/game"
)

// Registration is the immutable snapshot of a campaign that its Campaign
// Workflow publishes, so the catalog can render cards and authorize level
// starts without asking the Workflow.
type Registration struct {
	WorkflowID string              `json:"workflowId"`
	Definition campaign.Definition `json:"definition"`
	Levels     []game.Puzzle       `json:"levels"`
}

// Validate reports why a registration cannot be published.
func Validate(registration Registration) error {
	definition := registration.Definition
	if definition.ID == "" || registration.WorkflowID == "" || definition.Game.ID == "" || definition.Game.Title == "" {
		return errors.New("campaign registration is incomplete")
	}
	return ValidateLevels(registration.Levels)
}

// ValidateLevels reports the first level whose settings the game cannot run.
func ValidateLevels(levels []game.Puzzle) error {
	for _, puzzle := range levels {
		if len([]rune(puzzle.Letters)) > game.MaxWordflowLetters {
			return fmt.Errorf("level %d has more than %d letters", puzzle.Level, game.MaxWordflowLetters)
		}
		if puzzle.TimeLimitSeconds < 0 || puzzle.BasePoints < 0 ||
			puzzle.HintPrices.Letter < 0 || puzzle.HintPrices.Brush < 0 || puzzle.HintPrices.Word < 0 {
			return fmt.Errorf("level %d has invalid gameplay settings", puzzle.Level)
		}
	}
	return nil
}

// Resolution says whether a player may start a level, and if so which puzzle.
type Resolution struct {
	Allowed     bool        `json:"allowed"`
	Reason      string      `json:"reason,omitempty"`
	ErrorType   string      `json:"errorType,omitempty"`
	TotalLevels int         `json:"totalLevels"`
	Puzzle      game.Puzzle `json:"puzzle"`
}

// Resolve decides whether the player may start level now.
func Resolve(registration Registration, player campaign.PlayerProgress, level int, now time.Time) Resolution {
	view := View(registration, player, now)
	resolution := Resolution{TotalLevels: len(registration.Levels)}
	if !view.Eligible {
		resolution.Reason = view.LockedReason
		resolution.ErrorType = "campaign_locked"
		return resolution
	}
	if level != view.NextLevel {
		resolution.Reason = fmt.Sprintf("level %d is not next; expected level %d", level, view.NextLevel)
		resolution.ErrorType = "level_not_next"
		return resolution
	}
	if level < 1 || level > len(view.Levels) || view.Levels[level-1].Status != campaign.LevelAvailable {
		resolution.Reason = "the next level has not unlocked yet"
		resolution.ErrorType = "level_locked"
		return resolution
	}
	resolution.Allowed = true
	resolution.Puzzle = registration.Levels[level-1]
	return resolution
}

// View is one campaign's card as this player sees it now.
func View(registration Registration, player campaign.PlayerProgress, now time.Time) campaign.View {
	definition := registration.Definition
	status := Status(definition, now)
	progress := FindProgress(player.Campaigns, definition.ID)
	joinedAt := now
	nextLevel := 1
	completedLevels := 0
	failed := false
	if progress != nil {
		joinedAt = progress.JoinedAt
		nextLevel = progress.NextLevel
		completedLevels = progress.CompletedLevels
		failed = progress.Failed
	}

	eligible, reason := eligibility(definition, player, status)
	if player.ActiveCampaignID != "" && player.ActiveCampaignID != definition.ID {
		eligible = false
		reason = "Finish the active level before starting another campaign."
	}
	unlocked := unlockedLevels(definition.Unlock, joinedAt, now, len(registration.Levels))
	levels := make([]campaign.LevelView, 0, len(registration.Levels))
	for _, puzzle := range registration.Levels {
		levelStatus := campaign.LevelLocked
		switch {
		case puzzle.Level < nextLevel:
			levelStatus = campaign.LevelComplete
		case player.ActiveCampaignID == definition.ID && player.ActiveLevel == puzzle.Level:
			levelStatus = campaign.LevelActive
		case eligible && puzzle.Level == nextLevel && puzzle.Level <= unlocked:
			levelStatus = campaign.LevelAvailable
		case eligible && puzzle.Level <= unlocked:
			levelStatus = campaign.LevelUnlocked
		}
		levels = append(levels, campaign.LevelView{
			Level: puzzle.Level, Title: puzzle.Title, Status: levelStatus,
			UnlockedAt: levelUnlockTime(definition.Unlock, joinedAt, puzzle.Level),
		})
	}

	return campaign.View{
		CampaignID: definition.ID, Kind: definition.Kind, WorkflowID: registration.WorkflowID,
		Game: definition.Game, Title: definition.Title, Description: definition.Description,
		Status: status, StartsAt: definition.StartsAt, EndsAt: definition.EndsAt,
		Eligible: eligible, Failed: failed, LockedReason: reason, NextLevel: nextLevel,
		CompletedLevels: completedLevels, TotalLevels: len(registration.Levels), Levels: levels,
	}
}

// Status is whether the campaign's window has opened and closed at now.
func Status(definition campaign.Definition, now time.Time) campaign.Status {
	if definition.StartsAt != nil && now.Before(*definition.StartsAt) {
		return campaign.StatusUpcoming
	}
	if definition.EndsAt != nil && !now.Before(*definition.EndsAt) {
		return campaign.StatusEnded
	}
	return campaign.StatusActive
}

// FindProgress returns the player's progress in campaignID, or nil before they join it.
func FindProgress(progress []campaign.PlayerCampaignProgress, campaignID string) *campaign.PlayerCampaignProgress {
	for index := range progress {
		if progress[index].CampaignID == campaignID {
			return &progress[index]
		}
	}
	return nil
}

func eligibility(definition campaign.Definition, player campaign.PlayerProgress, status campaign.Status) (bool, string) {
	switch status {
	case campaign.StatusUpcoming:
		return false, "This campaign has not started yet."
	case campaign.StatusEnded:
		return false, "This campaign has ended."
	}
	if progress := FindProgress(player.Campaigns, definition.ID); definition.SingleAttempt && progress != nil && progress.Failed {
		return false, "Today's daily challenge attempt is over."
	}
	for _, requiredCampaign := range definition.Requirements.Campaigns {
		progress := FindProgress(player.Campaigns, requiredCampaign)
		if progress == nil || !progress.Completed {
			return false, "Complete campaign " + requiredCampaign + " first."
		}
	}
	for _, requiredLevel := range definition.Requirements.Levels {
		progress := FindProgress(player.Campaigns, requiredLevel.CampaignID)
		if progress == nil || progress.CompletedLevels < requiredLevel.Level {
			return false, fmt.Sprintf("Complete level %d of %s first.", requiredLevel.Level, requiredLevel.CampaignID)
		}
	}
	return true, ""
}

func unlockedLevels(policy campaign.UnlockPolicy, joinedAt, now time.Time, total int) int {
	unlocked := policy.InitialLevels
	if policy.LevelsPerInterval > 0 && policy.Interval > 0 && now.After(joinedAt) {
		unlocked += int(now.Sub(joinedAt)/policy.Interval) * policy.LevelsPerInterval
	}
	return min(max(unlocked, 0), total)
}

func levelUnlockTime(policy campaign.UnlockPolicy, joinedAt time.Time, level int) *time.Time {
	if level <= policy.InitialLevels || policy.LevelsPerInterval <= 0 || policy.Interval <= 0 {
		value := joinedAt
		return &value
	}
	intervals := (level - policy.InitialLevels + policy.LevelsPerInterval - 1) / policy.LevelsPerInterval
	value := joinedAt.Add(time.Duration(intervals) * policy.Interval)
	return &value
}
