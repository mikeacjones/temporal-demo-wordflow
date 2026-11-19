// Package player holds a player's long-term record and the rules that change
// it: points, streaks, campaign progress and the one level they have open. The
// store saves State; the API and Activities apply these rules to it.
package player

import (
	"errors"
	"fmt"
	"time"
	_ "time/tzdata"

	"github.com/mjones/temporal-word-game/internal/campaign"
	"github.com/mjones/temporal-word-game/internal/catalog"
	"github.com/mjones/temporal-word-game/internal/game"
)

const (
	timeZone              = "America/Toronto"
	StreakFreezePointCost = 50
)

// Location is the time zone that decides which day a level was completed on.
var Location = mustLoadLocation(timeZone)

// State is everything the game keeps about one player.
type State struct {
	PlayerID             string                            `json:"playerId"`
	DisplayName          string                            `json:"displayName"`
	PasswordHash         string                            `json:"passwordHash"`
	CreatedAt            time.Time                         `json:"createdAt"`
	LastSeenAt           time.Time                         `json:"lastSeenAt"`
	LastCompletionDay    string                            `json:"lastCompletionDay"`
	CurrentStreak        int                               `json:"currentStreak"`
	BestStreak           int                               `json:"bestStreak"`
	Points               int                               `json:"points"`
	LifetimePointsEarned int                               `json:"lifetimePointsEarned"`
	StreakFreeze         bool                              `json:"streakFreeze"`
	Campaigns            []campaign.PlayerCampaignProgress `json:"campaigns"`
	CompletedLevels      []game.LevelCompletion            `json:"completedLevels"`
	Rewards              []game.Reward                     `json:"rewards"`
	ActiveGame           *game.ActiveGame                  `json:"activeGame,omitempty"`
}

// Error is a rule the player broke, with a stable type the API maps to a status.
type Error struct {
	Type    string
	Message string
}

func (e *Error) Error() string { return e.Message }

func ruleError(errorType, format string, args ...any) error {
	return &Error{Type: errorType, Message: fmt.Sprintf(format, args...)}
}

// IsRule reports whether err is a broken game rule rather than a failure.
func IsRule(err error) bool {
	var ruleErr *Error
	return errors.As(err, &ruleErr)
}

// New is a player who has just signed up.
func New(playerID, displayName, passwordHash string, now time.Time) State {
	return State{
		PlayerID: playerID, DisplayName: displayName, PasswordHash: passwordHash,
		CreatedAt: now, LastSeenAt: now,
	}
}

// View is what the browser shows about the player.
func (s *State) View() game.PlayerView {
	var active *game.ActiveGame
	if s.ActiveGame != nil {
		copy := *s.ActiveGame
		active = &copy
	}
	return game.PlayerView{
		DisplayName:          s.DisplayName,
		CurrentStreak:        s.CurrentStreak,
		BestStreak:           s.BestStreak,
		Points:               s.Points,
		LifetimePointsEarned: s.LifetimePointsEarned,
		StreakFreeze:         s.StreakFreeze,
		StreakFreezeCost:     StreakFreezePointCost,
		Campaigns:            append([]campaign.PlayerCampaignProgress(nil), s.Campaigns...),
		CompletedLevelCount:  len(s.CompletedLevels),
		ActiveGame:           active,
	}
}

// Progress is the player as the catalog sees them.
func (s *State) Progress() campaign.PlayerProgress {
	progress := campaign.PlayerProgress{
		Campaigns: append([]campaign.PlayerCampaignProgress(nil), s.Campaigns...),
	}
	if s.ActiveGame != nil {
		progress.ActiveCampaignID = s.ActiveGame.CampaignID
		progress.ActiveLevel = s.ActiveGame.Level
	}
	return progress
}

// CheckPassword refuses a sign-in whose password hash doesn't match.
func (s *State) CheckPassword(passwordHash string) error {
	if passwordHash == "" || s.PasswordHash != passwordHash {
		return ruleError("invalid_credentials", "invalid username or password")
	}
	return nil
}

// SignIn records a visit and ends a streak the player has let lapse.
func (s *State) SignIn(now time.Time) {
	s.LastSeenAt = now
	s.expireStreakIfNeeded(now)
}

// StartLevel opens a level for the player. The catalog has already allowed it.
func (s *State) StartLevel(active game.ActiveGame, totalLevels int, now time.Time) error {
	if s.ActiveGame != nil {
		return ruleError("game_active", "finish the active level before starting another")
	}
	if progress := catalog.FindProgress(s.Campaigns, active.CampaignID); progress != nil {
		progress.TotalLevels = totalLevels
	} else {
		s.Campaigns = append(s.Campaigns, campaign.PlayerCampaignProgress{
			CampaignID: active.CampaignID, JoinedAt: now, NextLevel: 1, TotalLevels: totalLevels,
		})
	}
	s.ActiveGame = &active
	s.LastSeenAt = now
	return nil
}

// ProgressForStart is the player as the catalog sees them when they open
// campaignID, counting a campaign they haven't joined yet as joined now.
func (s *State) ProgressForStart(campaignID string, now time.Time) campaign.PlayerProgress {
	progress := s.Progress()
	if catalog.FindProgress(progress.Campaigns, campaignID) == nil {
		progress.Campaigns = append(progress.Campaigns, campaign.PlayerCampaignProgress{
			CampaignID: campaignID, JoinedAt: now, NextLevel: 1,
		})
	}
	return progress
}

// SpendPoints pays for a hint in the level the player has open.
func (s *State) SpendPoints(levelWorkflowID string, amount int, now time.Time) error {
	if amount <= 0 {
		return ruleError("invalid_point_spend", "point spend must be positive")
	}
	if s.ActiveGame == nil || s.ActiveGame.WorkflowID != levelWorkflowID {
		return ruleError("level_not_active", "the level is not active for this player")
	}
	if s.Points < amount {
		return ruleError("insufficient_points", "not enough points; need %d, have %d", amount, s.Points)
	}
	s.Points -= amount
	s.LastSeenAt = now
	return nil
}

// BuyStreakFreeze trades points for one missed day that won't end the streak.
func (s *State) BuyStreakFreeze(now time.Time) error {
	if s.StreakFreeze {
		return ruleError("streak_freeze_owned", "a streak freeze is already ready")
	}
	if s.Points < StreakFreezePointCost {
		return ruleError("insufficient_points", "not enough points; need %d, have %d", StreakFreezePointCost, s.Points)
	}
	s.Points -= StreakFreezePointCost
	s.StreakFreeze = true
	s.LastSeenAt = now
	return nil
}

// FinishLevel applies a level's result and closes it. It reports false when
// levelWorkflowID is not the player's open level, which is how a repeated
// result is ignored.
func (s *State) FinishLevel(levelWorkflowID string, result campaign.LevelResult) bool {
	if s.ActiveGame == nil || s.ActiveGame.WorkflowID != levelWorkflowID {
		return false
	}
	s.ActiveGame = nil
	s.LastSeenAt = result.CompletedAt
	progress := catalog.FindProgress(s.Campaigns, result.CampaignID)
	if progress == nil || progress.NextLevel != result.Level {
		return true
	}
	if result.Abandoned {
		return true
	}
	if result.TimedOut {
		progress.Failed = true
		return true
	}

	s.updateStreak(result.CompletedAt)
	points := 0
	for _, award := range result.Awards {
		points += award.Points
		s.Rewards = append(s.Rewards, game.Reward{
			CampaignID: result.CampaignID, Level: result.Level,
			Description: award.Description, Points: award.Points, AwardedAt: result.CompletedAt,
		})
	}
	s.CompletedLevels = append(s.CompletedLevels, game.LevelCompletion{
		CampaignID: result.CampaignID, Level: result.Level,
		Attempts: result.Attempts, CompletedAt: result.CompletedAt, Points: points,
	})
	progress.NextLevel++
	progress.CompletedLevels++
	progress.Completed = progress.CompletedLevels >= progress.TotalLevels
	s.Points += points
	s.LifetimePointsEarned += points
	return true
}

func (s *State) updateStreak(completedAt time.Time) {
	today := dayKey(completedAt)
	if s.LastCompletionDay == today {
		return
	}

	yesterday := dayKey(completedAt.In(Location).AddDate(0, 0, -1))
	if s.LastCompletionDay == yesterday {
		s.CurrentStreak++
	} else {
		s.CurrentStreak = 1
	}
	s.BestStreak = max(s.BestStreak, s.CurrentStreak)
	s.LastCompletionDay = today
}

func (s *State) expireStreakIfNeeded(now time.Time) {
	if s.LastCompletionDay == "" {
		return
	}
	today := dayKey(now)
	yesterday := dayKey(now.In(Location).AddDate(0, 0, -1))
	if s.LastCompletionDay == today || s.LastCompletionDay == yesterday {
		return
	}

	dayBeforeYesterday := dayKey(now.In(Location).AddDate(0, 0, -2))
	if s.StreakFreeze && s.LastCompletionDay == dayBeforeYesterday {
		s.StreakFreeze = false
		s.LastCompletionDay = yesterday
		return
	}
	s.CurrentStreak = 0
}

func dayKey(value time.Time) string {
	return value.In(Location).Format("2006-01-02")
}

func mustLoadLocation(name string) *time.Location {
	location, err := time.LoadLocation(name)
	if err != nil {
		panic(err)
	}
	return location
}
