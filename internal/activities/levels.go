package activities

import (
	"context"
	"errors"
	"time"

	"github.com/mjones/temporal-word-game/internal/catalog"
	"github.com/mjones/temporal-word-game/internal/game"
	"github.com/mjones/temporal-word-game/internal/player"
	"github.com/mjones/temporal-word-game/internal/store"
	"github.com/mjones/temporal-word-game/internal/workflows"

	"go.temporal.io/sdk/temporal"
)

// LevelStore is the part of the store the level Activities use.
type LevelStore interface {
	Campaign(ctx context.Context, campaignID string) (catalog.Registration, error)
	UpdatePlayer(ctx context.Context, playerID string, change func(*player.State) error) (player.State, error)
}

// Levels changes a player's record as their level starts, spends points and ends.
type Levels struct {
	Store LevelStore
}

// ClaimLevel opens a level in the player's record, if the catalog allows it
// now, and returns the puzzle. Repeating a claim that already landed returns
// the same puzzle.
func (a *Levels) ClaimLevel(ctx context.Context, input workflows.ClaimLevelActivityInput) (workflows.ClaimLevelResult, error) {
	registration, err := a.Store.Campaign(ctx, input.CampaignID)
	if errors.Is(err, store.ErrNotFound) {
		return workflows.ClaimLevelResult{}, temporal.NewNonRetryableApplicationError(
			"campaign "+input.CampaignID+" is not available", "campaign_not_found", err)
	}
	if err != nil {
		return workflows.ClaimLevelResult{}, err
	}

	var claimed catalog.Resolution
	state, err := a.Store.UpdatePlayer(ctx, input.PlayerID, func(state *player.State) error {
		if state.ActiveGame != nil && state.ActiveGame.WorkflowID == input.LevelWorkflowID &&
			input.Level >= 1 && input.Level <= len(registration.Levels) {
			claimed = catalog.Resolution{Allowed: true, Puzzle: registration.Levels[input.Level-1], TotalLevels: len(registration.Levels)}
			return nil
		}
		claimed = catalog.Resolve(registration, state.ProgressForStart(input.CampaignID, input.StartedAt), input.Level, input.StartedAt)
		if !claimed.Allowed {
			return &player.Error{Type: claimed.ErrorType, Message: claimed.Reason}
		}
		return state.StartLevel(game.ActiveGame{
			WorkflowID: input.LevelWorkflowID, CampaignID: input.CampaignID, Level: input.Level,
			Title: claimed.Puzzle.Title, TotalLevels: claimed.TotalLevels,
		}, claimed.TotalLevels, input.StartedAt)
	})
	if err != nil {
		return workflows.ClaimLevelResult{}, playerError(err)
	}
	return workflows.ClaimLevelResult{Player: state.View(), Puzzle: claimed.Puzzle, TotalLevels: claimed.TotalLevels}, nil
}

// SpendPoints pays for a hint in the player's open level.
func (a *Levels) SpendPoints(ctx context.Context, input workflows.SpendPointsActivityInput) (workflows.SpendPointsResult, error) {
	state, err := a.Store.UpdatePlayer(ctx, input.PlayerID, func(state *player.State) error {
		return state.SpendPoints(input.LevelWorkflowID, input.Amount, time.Now())
	})
	if err != nil {
		return workflows.SpendPointsResult{}, playerError(err)
	}
	return workflows.SpendPointsResult{Spent: input.Amount, Remaining: state.Points}, nil
}

// RecordLevelResult applies a finished level to the player's record: points,
// streak and progress, and closes the level so the player can start another.
// A result recorded before is ignored, so a retry changes nothing.
func (a *Levels) RecordLevelResult(ctx context.Context, input workflows.RecordLevelResultActivityInput) error {
	_, err := a.Store.UpdatePlayer(ctx, input.PlayerID, func(state *player.State) error {
		state.FinishLevel(input.LevelWorkflowID, input.Result)
		return nil
	})
	return playerError(err)
}

// playerError makes a broken game rule, or a player who doesn't exist, a
// non-retryable failure; anything else stays retryable.
func playerError(err error) error {
	var rule *player.Error
	switch {
	case err == nil:
		return nil
	case errors.As(err, &rule):
		return temporal.NewNonRetryableApplicationError(rule.Message, rule.Type, err)
	case errors.Is(err, store.ErrNotFound):
		return temporal.NewNonRetryableApplicationError("player not found", "player_not_found", err)
	default:
		return err
	}
}
