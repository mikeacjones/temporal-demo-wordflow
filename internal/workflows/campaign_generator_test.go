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
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/testsuite"
	"go.temporal.io/sdk/workflow"
)

func testGenerateWordflowCampaign(context.Context, GenerateWordflowCampaignActivityInput) (WordflowCampaignWorkflowInput, error) {
	return WordflowCampaignWorkflowInput{}, nil
}

func generatedCampaignFixture(id string, answer string) WordflowCampaignWorkflowInput {
	return WordflowCampaignWorkflowInput{
		Definition: campaign.Definition{
			ID:   id,
			Game: campaign.GameSummary{ID: campaign.GameWordflow, Title: "Wordflow"},
		},
		Levels: []game.Puzzle{{Level: 1, Words: []game.PlacedWord{{Answer: answer}}}},
	}
}

func previousGeneratedCampaigns(count int) CampaignGeneratorResult {
	var result CampaignGeneratorResult
	for index := range count {
		result.Recent = append(result.Recent, GeneratedCampaign{
			CampaignID: fmt.Sprintf("old-%d", index),
			Answers:    []string{fmt.Sprintf("OLDWORD%d", index)},
		})
	}
	return result
}

type generatorRun struct {
	env             *testsuite.TestWorkflowEnvironment
	activityInput   GenerateWordflowCampaignActivityInput
	startedCampaign WordflowCampaignWorkflowInput
}

func runGenerator(t *testing.T, now time.Time, kind campaign.Kind, previous CampaignGeneratorResult, generated WordflowCampaignWorkflowInput) *generatorRun {
	t.Helper()
	run := &generatorRun{env: (&testsuite.WorkflowTestSuite{}).NewTestWorkflowEnvironment()}
	run.env.SetStartTime(now)
	if len(previous.Recent) > 0 {
		run.env.SetLastCompletionResult(previous)
	}
	run.env.RegisterActivityWithOptions(testGenerateWordflowCampaign, activity.RegisterOptions{Name: ActivityGenerateWordflowCampaign})
	run.env.OnActivity(ActivityGenerateWordflowCampaign, mock.Anything, mock.Anything).
		Run(func(arguments mock.Arguments) {
			run.activityInput = arguments.Get(1).(GenerateWordflowCampaignActivityInput)
		}).Return(generated, nil).Once()
	run.env.RegisterWorkflowWithOptions(func(_ workflow.Context, input WordflowCampaignWorkflowInput) error {
		run.startedCampaign = input
		return nil
	}, workflow.RegisterOptions{Name: WordflowCampaignWorkflowName})

	run.env.ExecuteWorkflow(WordflowCampaignGeneratorWorkflow, CampaignGeneratorWorkflowInput{Kind: kind})
	return run
}

func TestDailyGeneratorStartsTodaysChallengeAndHandsOnTenDaysOfAnswers(t *testing.T) {
	generated := generatedCampaignFixture("daily-challenge-2026-09-22", "NEWWORD")
	run := runGenerator(t, time.Date(2026, 9, 22, 0, 0, 5, 0, torontoLocation),
		campaign.KindDailyChallenge, previousGeneratedCampaigns(dailyChallengeHistory), generated)

	require.NoError(t, run.env.GetWorkflowError())
	startsAt := time.Date(2026, 9, 22, 0, 0, 0, 0, torontoLocation)
	endsAt := startsAt.AddDate(0, 0, 1)
	require.Equal(t, campaign.KindDailyChallenge, run.activityInput.Kind)
	require.Equal(t, "2026-09-22", run.activityInput.Date)
	require.True(t, startsAt.Equal(run.activityInput.StartsAt))
	require.True(t, endsAt.Equal(*run.activityInput.EndsAt))
	require.Len(t, run.activityInput.ExcludedWords, dailyChallengeHistory)
	require.Equal(t, "OLDWORD0", run.activityInput.ExcludedWords[0])
	require.Equal(t, generated, run.startedCampaign)

	var result CampaignGeneratorResult
	require.NoError(t, run.env.GetWorkflowResult(&result))
	require.Len(t, result.Recent, dailyChallengeHistory)
	require.Equal(t, "old-1", result.Recent[0].CampaignID)
	require.Equal(t, GeneratedCampaign{CampaignID: "daily-challenge-2026-09-22", Answers: []string{"NEWWORD"}}, result.Recent[9])
}

func TestMonthlyGeneratorStartsAPermanentPackForTheCurrentMonth(t *testing.T) {
	generated := generatedCampaignFixture("monthly-pack-2026-12", "NEWWORD")
	run := runGenerator(t, time.Date(2026, 12, 14, 9, 0, 0, 0, torontoLocation),
		campaign.KindMonthlyPack, previousGeneratedCampaigns(monthlyPackHistory), generated)

	require.NoError(t, run.env.GetWorkflowError())
	require.Equal(t, "2026-12-01", run.activityInput.Date)
	require.True(t, time.Date(2026, 12, 1, 0, 0, 0, 0, torontoLocation).Equal(run.activityInput.StartsAt))
	require.Nil(t, run.activityInput.EndsAt)
	require.Equal(t, generated, run.startedCampaign)

	var result CampaignGeneratorResult
	require.NoError(t, run.env.GetWorkflowResult(&result))
	require.Len(t, result.Recent, monthlyPackHistory)
	require.Equal(t, "old-1", result.Recent[0].CampaignID)
	require.Equal(t, "monthly-pack-2026-12", result.Recent[monthlyPackHistory-1].CampaignID)
}

func TestGeneratorWithoutAPreviousRunExcludesNothing(t *testing.T) {
	run := runGenerator(t, time.Date(2026, 9, 22, 12, 0, 0, 0, torontoLocation),
		campaign.KindDailyChallenge, CampaignGeneratorResult{}, generatedCampaignFixture("daily-challenge-2026-09-22", "NEWWORD"))

	require.NoError(t, run.env.GetWorkflowError())
	require.Empty(t, run.activityInput.ExcludedWords)
}

func TestGeneratorRejectsACampaignForTheWrongPeriod(t *testing.T) {
	run := runGenerator(t, time.Date(2026, 9, 22, 12, 0, 0, 0, torontoLocation),
		campaign.KindDailyChallenge, CampaignGeneratorResult{}, generatedCampaignFixture("daily-challenge-2026-09-21", "NEWWORD"))

	var applicationError *temporal.ApplicationError
	require.ErrorAs(t, run.env.GetWorkflowError(), &applicationError)
	require.Equal(t, "invalid_generated_campaign", applicationError.Type())
	require.True(t, applicationError.NonRetryable())
	require.Empty(t, run.startedCampaign.Definition.ID)
}

func TestGeneratorRejectsAnUnknownKindWithoutGenerating(t *testing.T) {
	env := (&testsuite.WorkflowTestSuite{}).NewTestWorkflowEnvironment()

	env.ExecuteWorkflow(WordflowCampaignGeneratorWorkflow, CampaignGeneratorWorkflowInput{Kind: "weekly"})

	var applicationError *temporal.ApplicationError
	require.ErrorAs(t, env.GetWorkflowError(), &applicationError)
	require.Equal(t, "invalid_campaign_kind", applicationError.Type())
	require.True(t, applicationError.NonRetryable())
}

func TestGeneratedCampaignPeriodUsesTorontoCalendarDays(t *testing.T) {
	// 02:00 UTC on September 22 is still the evening of September 21 in Toronto.
	date, startsAt, endsAt, _, err := generatedCampaignPeriod(campaign.KindDailyChallenge, time.Date(2026, 9, 22, 2, 0, 0, 0, time.UTC))
	require.NoError(t, err)
	require.Equal(t, "2026-09-21", date)
	require.True(t, time.Date(2026, 9, 21, 0, 0, 0, 0, torontoLocation).Equal(startsAt))
	require.True(t, startsAt.AddDate(0, 0, 1).Equal(*endsAt))

	date, _, _, _, err = generatedCampaignPeriod(campaign.KindMonthlyPack, time.Date(2026, 12, 1, 3, 0, 0, 0, time.UTC))
	require.NoError(t, err)
	require.Equal(t, "2026-11-01", date, "03:00 UTC on December 1 is still November 30 in Toronto")
}

func TestGeneratedCampaignID(t *testing.T) {
	require.Equal(t, "daily-challenge-2026-09-22", GeneratedCampaignID(campaign.KindDailyChallenge, "2026-09-22"))
	require.Equal(t, "monthly-pack-2026-12", GeneratedCampaignID(campaign.KindMonthlyPack, "2026-12-01"))
}

func TestRecentAnswersPreservesOrderAndDeduplicates(t *testing.T) {
	recent := []GeneratedCampaign{
		{Answers: []string{"FIRST", "SHARED"}},
		{Answers: []string{"SHARED", "LAST"}},
	}
	require.Equal(t, []string{"FIRST", "SHARED", "LAST"}, recentAnswers(recent))
}
