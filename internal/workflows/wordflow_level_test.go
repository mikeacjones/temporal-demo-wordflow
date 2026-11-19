package workflows

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/mjones/temporal-word-game/internal/campaign"
	"github.com/mjones/temporal-word-game/internal/game"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/testsuite"
)

// newLevelTestEnv is a test environment whose ClaimLevel Activity hands out
// puzzle and whose other Activities succeed, with the start-level Update sent
// as soon as the Workflow starts. Tests that pass a State skip the claim.
func newLevelTestEnv(t *testing.T, puzzle game.Puzzle) *testsuite.TestWorkflowEnvironment {
	t.Helper()
	env := (&testsuite.WorkflowTestSuite{}).NewTestWorkflowEnvironment()
	env.RegisterActivityWithOptions(func(context.Context, ClaimLevelActivityInput) (ClaimLevelResult, error) {
		return ClaimLevelResult{Player: game.PlayerView{DisplayName: "Test"}, Puzzle: puzzle, TotalLevels: 2}, nil
	}, activity.RegisterOptions{Name: ActivityClaimLevel})
	env.RegisterActivityWithOptions(func(context.Context, SpendPointsActivityInput) (SpendPointsResult, error) {
		return SpendPointsResult{}, nil
	}, activity.RegisterOptions{Name: ActivitySpendPoints})
	env.RegisterActivityWithOptions(func(context.Context, RecordLevelResultActivityInput) error {
		return nil
	}, activity.RegisterOptions{Name: ActivityRecordLevelResult})
	env.RegisterDelayedCallback(func() {
		env.UpdateWorkflow(UpdateStartLevel, "start", &testsuite.TestUpdateCallback{
			OnReject:   func(err error) { require.NoError(t, err) },
			OnComplete: func(_ any, err error) { require.NoError(t, err) },
		}, StartLevelInput{})
	}, 0)
	return env
}

func testPuzzle(t *testing.T, level int) game.Puzzle {
	t.Helper()
	definitions := []game.LevelDefinition{
		{Title: "Hello, Workflow", Letters: "WORKFLOW", Words: []string{"WORKFLOW", "WORK", "FLOW", "WOLF", "FOOL", "ROOF", "ROW", "OWL"}},
		{Title: "Activity Time", Letters: "ACTIVE", Words: []string{"ACTIVE", "EVICT", "CAVE", "VICE", "VET", "CAT"}},
	}
	puzzle, err := game.BuildPuzzle(level, definitions[level-1])
	require.NoError(t, err)
	return puzzle
}

func TestWordflowLevelWorkflowCompletesThroughUpdates(t *testing.T) {
	puzzle := testPuzzle(t, 1)

	env := newLevelTestEnv(t, puzzle)

	for index, word := range puzzle.Words {
		index, answer := index, word.Answer
		env.RegisterDelayedCallback(func() {
			env.UpdateWorkflow(UpdateSubmitGuess, fmt.Sprintf("guess-%d", index), &testsuite.TestUpdateCallback{
				OnComplete: func(_ any, err error) { require.NoError(t, err) },
			}, SubmitGuessInput{Word: answer})
		}, time.Duration(index+1)*time.Millisecond)
	}

	env.ExecuteWorkflow(WordflowLevelWorkflow, WordflowLevelWorkflowInput{
		PlayerID: "test-player", CampaignID: "test-campaign", Level: puzzle.Level,
	})

	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())
	var result campaign.LevelResult
	require.NoError(t, env.GetWorkflowResult(&result))
	require.Equal(t, "test-campaign", result.CampaignID)
	require.Equal(t, puzzle.Level, result.Level)
	require.Equal(t, len(puzzle.Words), result.Attempts)
	require.Equal(t, []campaign.PointAward{{Description: "Wordflow score", Points: 40}}, result.Awards)
}

func TestRejectedWordsAreDurableAndNewestFirst(t *testing.T) {
	puzzle := testPuzzle(t, 1)

	env := newLevelTestEnv(t, puzzle)
	var latest game.GameView

	for index, word := range []string{"LOW", "FOR", "ZZZ"} {
		index, word := index, word
		env.RegisterDelayedCallback(func() {
			env.UpdateWorkflow(UpdateSubmitGuess, fmt.Sprintf("miss-%d", index), &testsuite.TestUpdateCallback{
				OnComplete: func(result any, err error) {
					require.NoError(t, err)
					latest = result.(game.GuessResult).Game
				},
			}, SubmitGuessInput{Word: word})
		}, time.Duration(index+1)*time.Millisecond)
	}
	for index, word := range puzzle.Words {
		index, answer := index, word.Answer
		env.RegisterDelayedCallback(func() {
			env.UpdateWorkflow(UpdateSubmitGuess, fmt.Sprintf("guess-%d", index), &testsuite.TestUpdateCallback{
				OnComplete: func(_ any, err error) { require.NoError(t, err) },
			}, SubmitGuessInput{Word: answer})
		}, time.Duration(index+4)*time.Millisecond)
	}

	env.ExecuteWorkflow(WordflowLevelWorkflow, WordflowLevelWorkflowInput{
		PlayerID: "test-player", CampaignID: "test-campaign", Level: puzzle.Level,
	})

	require.NoError(t, env.GetWorkflowError())
	// ZZZ is an invalid letter combination, not a word that was absent from the puzzle.
	require.Equal(t, []string{"FOR", "LOW"}, latest.RejectedWords)
}

func TestGameScoreUsesTimeMistakesAndHints(t *testing.T) {
	startedAt := time.Date(2026, time.September, 20, 12, 0, 0, 0, time.UTC)
	state := &WordflowLevelState{
		StartedAt:        startedAt,
		CompletedAt:      startedAt.Add(150 * time.Second),
		IncorrectGuesses: 3,
		LetterHintsUsed:  1,
		BrushHintsUsed:   1,
		WordHintsUsed:    1,
	}

	score := calculateGameScore(state)
	require.Equal(t, game.GameScore{
		Points: 18, BasePoints: 10, SpeedBonus: 4, AccuracyBonus: 4, HintBonus: 0,
		DurationSeconds: 150, IncorrectGuesses: 3, HintsUsed: 3,
	}, score)
}

func TestLevelGameplaySettingsOverrideDefaults(t *testing.T) {
	startedAt := time.Date(2026, time.September, 20, 12, 0, 0, 0, time.UTC)
	state := &WordflowLevelState{
		Puzzle: game.Puzzle{
			TimeLimitSeconds: 180,
			BasePoints:       20,
			HintPrices:       game.HintPrices{Letter: 15, Brush: 30, Word: 45},
		},
		StartedAt:   startedAt,
		CompletedAt: startedAt.Add(30 * time.Second),
	}

	score := calculateGameScore(state)
	view := wordflowLevelView(state)
	require.Equal(t, 20, score.BasePoints)
	require.Equal(t, 50, score.Points)
	require.Equal(t, game.HintPrices{Letter: 15, Brush: 30, Word: 45}, view.HintPrices)
	require.NotNil(t, view.ExpiresAt)
	require.Equal(t, startedAt.Add(180*time.Second), *view.ExpiresAt)
	require.Equal(t, 15, mustHintPointCost(t, state.Puzzle, game.HintLetter))
	require.Equal(t, 30, mustHintPointCost(t, state.Puzzle, game.HintBrush))
	require.Equal(t, 45, mustHintPointCost(t, state.Puzzle, game.HintWord))
}

func TestWordflowLevelWorkflowEndsWhenHardLimitExpires(t *testing.T) {
	puzzle := testPuzzle(t, 1)
	puzzle.TimeLimitSeconds = 60

	env := newLevelTestEnv(t, puzzle)
	env.ExecuteWorkflow(WordflowLevelWorkflow, WordflowLevelWorkflowInput{
		PlayerID: "test-player", CampaignID: "daily", Level: puzzle.Level,
	})

	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())
	var result campaign.LevelResult
	require.NoError(t, env.GetWorkflowResult(&result))
	require.True(t, result.TimedOut)
	require.Empty(t, result.Awards)
	require.False(t, result.CompletedAt.IsZero())
}

func TestTimedOutViewRevealsSolutionAndDistinguishesMissedWords(t *testing.T) {
	puzzle := testPuzzle(t, 1)
	state := &WordflowLevelState{
		Puzzle:       puzzle,
		FoundAnswers: []string{puzzle.Words[0].Answer},
		TimedOut:     true,
	}

	view := wordflowLevelView(state)
	require.Len(t, view.SolutionWords, len(puzzle.Words))
	require.True(t, view.SolutionWords[0].Found)
	require.False(t, view.SolutionWords[1].Found)
	require.NotEmpty(t, view.Cells)
	require.True(t, hasMissedSolutionCell(view.Cells))
	for _, cell := range view.Cells {
		require.True(t, cell.Revealed)
		require.NotEmpty(t, cell.Letter)
	}
}

func hasMissedSolutionCell(cells []game.CellView) bool {
	for _, cell := range cells {
		if cell.Missed {
			return true
		}
	}
	return false
}

func mustHintPointCost(t *testing.T, puzzle game.Puzzle, hint game.HintType) int {
	t.Helper()
	value, err := hintPointCost(puzzle, hint)
	require.NoError(t, err)
	return value
}

func TestSpeedBonusTiersUseTheWorkflowStartTime(t *testing.T) {
	startedAt := time.Date(2026, time.September, 20, 12, 0, 0, 0, time.UTC)
	require.Equal(t, []game.SpeedBonusTier{
		{Points: 10, EndsAt: startedAt.Add(time.Minute)},
		{Points: 7, EndsAt: startedAt.Add(2 * time.Minute)},
		{Points: 4, EndsAt: startedAt.Add(3 * time.Minute)},
	}, speedBonusTiers(startedAt, 4))

	require.Equal(t, 10, speedBonusFor(time.Minute, 4))
	require.Equal(t, 7, speedBonusFor(time.Minute+time.Nanosecond, 4))
	require.Equal(t, 4, speedBonusFor(2*time.Minute+time.Nanosecond, 4))
	require.Zero(t, speedBonusFor(3*time.Minute+time.Nanosecond, 4))
	require.Equal(t, 90*time.Second, speedBonusTierDuration(6))
	require.Equal(t, 2*time.Minute, speedBonusTierDuration(8))
	require.Equal(t, 150*time.Second, speedBonusTierDuration(10))
}

func TestLevelViewShowsCurrentHintAndAccuracyBonuses(t *testing.T) {
	state := &WordflowLevelState{
		IncorrectGuesses: 3,
		LetterHintsUsed:  1,
		BrushHintsUsed:   1,
	}

	view := wordflowLevelView(state)
	require.Equal(t, 4, view.AccuracyBonus)
	require.Equal(t, 4, view.HintBonus)
}

func TestHintsAreDurableState(t *testing.T) {
	puzzle := testPuzzle(t, 2)
	state := &WordflowLevelState{
		Puzzle: puzzle,
		Hints:  game.HintInventory{Letters: 1, Brushes: 1, Words: 1},
	}

	outcome, err := applyHint(state, game.HintLetter, false)
	require.NoError(t, err)
	require.Equal(t, "letter_revealed", outcome)
	require.Len(t, state.RevealedCells, 1)
	require.Zero(t, state.Hints.Letters)

	outcome, err = applyHint(state, game.HintWord, false)
	require.NoError(t, err)
	require.Equal(t, "word_revealed", outcome)
	require.Len(t, state.FoundAnswers, 1)
}

func TestHintsSkipLettersVisibleFromFoundWords(t *testing.T) {
	state := &WordflowLevelState{
		Puzzle: game.Puzzle{Words: []game.PlacedWord{
			{Answer: "WORDS", Direction: game.Across},
			{Answer: "SOW", Col: 4, Direction: game.Down},
		}},
		FoundAnswers: []string{"WORDS"},
		Hints:        game.HintInventory{Letters: 1, Brushes: 1},
	}

	outcome, err := applyHint(state, game.HintLetter, false)
	require.NoError(t, err)
	require.Equal(t, "letter_revealed", outcome)
	require.Equal(t, []game.Position{{Row: 1, Col: 4}}, state.RevealedCells)

	view := wordflowLevelView(state)
	cells := map[game.Position]game.CellView{}
	for _, cell := range view.Cells {
		cells[game.Position{Row: cell.Row, Col: cell.Col}] = cell
	}
	require.True(t, cells[game.Position{Col: 4}].Revealed)
	require.False(t, cells[game.Position{Col: 4}].Hinted)
	require.True(t, cells[game.Position{Row: 1, Col: 4}].Hinted)

	outcome, err = applyHint(state, game.HintBrush, false)
	require.NoError(t, err)
	require.Equal(t, "letters_revealed", outcome)
	require.Equal(t, []game.Position{{Row: 1, Col: 4}, {Row: 2, Col: 4}}, state.RevealedCells)
	require.Equal(t, []string{"WORDS"}, state.FoundAnswers)

	state.Hints.Letters = 1
	_, err = applyHint(state, game.HintLetter, false)
	require.Error(t, err)
	require.Equal(t, 1, state.Hints.Letters)
}

func TestPaidHintSpendsPointsThroughActivity(t *testing.T) {
	puzzle := testPuzzle(t, 1)
	puzzle.HintPrices.Letter = 15
	gameID := WordflowLevelWorkflowID("test-player", "test-campaign", puzzle.Level)

	env := newLevelTestEnv(t, puzzle)
	env.OnActivity(ActivitySpendPoints, mock.Anything, SpendPointsActivityInput{
		PlayerID: "test-player", LevelWorkflowID: gameID, Amount: 15,
	}).Return(SpendPointsResult{Spent: 15, Remaining: 40}, nil).Once()

	var hintResult game.HintResult
	env.RegisterDelayedCallback(func() {
		env.UpdateWorkflow(UpdateUseHint, "buy-letter", &testsuite.TestUpdateCallback{
			OnComplete: func(result any, err error) {
				require.NoError(t, err)
				hintResult = result.(game.HintResult)
			},
		}, UseHintInput{Hint: game.HintLetter})
	}, time.Millisecond)
	for index, word := range puzzle.Words {
		index, answer := index, word.Answer
		env.RegisterDelayedCallback(func() {
			env.UpdateWorkflow(UpdateSubmitGuess, fmt.Sprintf("guess-%d", index), &testsuite.TestUpdateCallback{
				OnComplete: func(_ any, err error) { require.NoError(t, err) },
			}, SubmitGuessInput{Word: answer})
		}, time.Duration(index+2)*time.Millisecond)
	}

	env.ExecuteWorkflow(WordflowLevelWorkflow, WordflowLevelWorkflowInput{State: &WordflowLevelState{
		WorkflowID: gameID,
		PlayerID:   "test-player",
		CampaignID: "test-campaign",
		Puzzle:     puzzle,
	}})

	require.NoError(t, env.GetWorkflowError())
	require.Equal(t, 15, hintResult.PointsSpent)
	require.NotNil(t, hintResult.PointsRemaining)
	require.Equal(t, 40, *hintResult.PointsRemaining)
	env.AssertExpectations(t)
}

func TestRefusedClaimEndsTheLevelWithoutARecordedResult(t *testing.T) {
	env := (&testsuite.WorkflowTestSuite{}).NewTestWorkflowEnvironment()
	env.RegisterActivityWithOptions(func(context.Context, ClaimLevelActivityInput) (ClaimLevelResult, error) {
		return ClaimLevelResult{}, temporal.NewNonRetryableApplicationError("level 2 is not next", "level_not_next", nil)
	}, activity.RegisterOptions{Name: ActivityClaimLevel})
	var refusal error
	env.RegisterDelayedCallback(func() {
		env.UpdateWorkflow(UpdateStartLevel, "start", &testsuite.TestUpdateCallback{
			OnReject:   func(err error) { require.NoError(t, err) },
			OnComplete: func(_ any, err error) { refusal = err },
		}, StartLevelInput{})
	}, 0)

	env.ExecuteWorkflow(WordflowLevelWorkflow, WordflowLevelWorkflowInput{PlayerID: "p", CampaignID: "c", Level: 2})

	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())
	require.ErrorContains(t, refusal, "level 2 is not next")
}

func TestUntimedLevelIsAbandonedAfterADayWithoutAMove(t *testing.T) {
	puzzle := testPuzzle(t, 1)
	env := newLevelTestEnv(t, puzzle)
	var recorded RecordLevelResultActivityInput
	env.OnActivity(ActivityRecordLevelResult, mock.Anything, mock.Anything).
		Run(func(arguments mock.Arguments) { recorded = arguments.Get(1).(RecordLevelResultActivityInput) }).
		Return(nil).Once()
	env.SetStartWorkflowOptions(client.StartWorkflowOptions{ID: WordflowLevelWorkflowID("p", "c", 1)})
	startedAt := env.Now()
	// A guess 20 hours in pushes the expiry to 44 hours.
	env.RegisterDelayedCallback(func() {
		env.UpdateWorkflow(UpdateSubmitGuess, "guess", &testsuite.TestUpdateCallback{
			OnComplete: func(_ any, err error) { require.NoError(t, err) },
		}, SubmitGuessInput{Word: puzzle.Words[0].Answer})
	}, 20*time.Hour)

	env.ExecuteWorkflow(WordflowLevelWorkflow, WordflowLevelWorkflowInput{PlayerID: "p", CampaignID: "c", Level: 1})

	require.NoError(t, env.GetWorkflowError())
	var result campaign.LevelResult
	require.NoError(t, env.GetWorkflowResult(&result))
	require.True(t, result.Abandoned)
	require.Empty(t, result.Awards)
	require.False(t, result.CompletedAt.Before(startedAt.Add(44*time.Hour)), "the idle limit counts from the last move")
	require.Equal(t, result, recorded.Result)
	require.Equal(t, "p", recorded.PlayerID)
	require.Equal(t, WordflowLevelWorkflowID("p", "c", 1), recorded.LevelWorkflowID)
}

func TestTimedLevelIsNotAbandonedWhileItsClockRuns(t *testing.T) {
	puzzle := testPuzzle(t, 1)
	puzzle.TimeLimitSeconds = 180
	env := newLevelTestEnv(t, puzzle)

	env.ExecuteWorkflow(WordflowLevelWorkflow, WordflowLevelWorkflowInput{PlayerID: "p", CampaignID: "c", Level: 1})

	var result campaign.LevelResult
	require.NoError(t, env.GetWorkflowResult(&result))
	require.True(t, result.TimedOut)
	require.False(t, result.Abandoned)
}
