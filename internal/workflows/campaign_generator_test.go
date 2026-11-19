package workflows

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/mjones/temporal-word-game/internal/campaign"
	"github.com/mjones/temporal-word-game/internal/game"

	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/testsuite"
	"go.temporal.io/sdk/workflow"
)

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

func toolCall(id, name string) AgentToolCall {
	return AgentToolCall{CallID: id, Name: name, Arguments: `{"call":"` + id + `"}`}
}

// agentRun scripts the model's turns and the tools' answers, and records what
// the Workflow sent to each.
type agentRun struct {
	env *testsuite.TestWorkflowEnvironment
	// turns[n] is what the model does on turn n+1. A turn past the script repeats the last one.
	turns []RunCampaignAgentTurnResult
	// toolResult answers a call; the default accepts a submission with accepted.
	toolResult func(AgentToolCall) ExecuteCampaignToolResult

	requests        []CampaignRequest
	drafts          []CampaignDraft // the draft each tool call received
	turnInputs      []RunCampaignAgentTurnInput
	executed        []AgentToolCall
	startedCampaign WordflowCampaignWorkflowInput
}

func newAgentRun(now time.Time, previous CampaignGeneratorResult) *agentRun {
	run := &agentRun{env: (&testsuite.WorkflowTestSuite{}).NewTestWorkflowEnvironment()}
	run.env.SetStartTime(now)
	if len(previous.Recent) > 0 {
		run.env.SetLastCompletionResult(previous)
	}
	run.env.RegisterActivityWithOptions(func(_ context.Context, input RunCampaignAgentTurnInput) (RunCampaignAgentTurnResult, error) {
		run.turnInputs = append(run.turnInputs, input)
		turn := min(len(run.turnInputs), len(run.turns)) - 1
		return run.turns[turn], nil
	}, activity.RegisterOptions{Name: ActivityRunCampaignAgentTurn})
	run.env.RegisterActivityWithOptions(func(_ context.Context, input ExecuteCampaignToolInput) (ExecuteCampaignToolResult, error) {
		run.requests = append(run.requests, input.Request)
		run.drafts = append(run.drafts, input.Draft)
		run.executed = append(run.executed, input.Call)
		return run.toolResult(input.Call), nil
	}, activity.RegisterOptions{Name: ActivityExecuteCampaignTool})
	run.env.RegisterWorkflowWithOptions(func(_ workflow.Context, input WordflowCampaignWorkflowInput) error {
		run.startedCampaign = input
		return nil
	}, workflow.RegisterOptions{Name: WordflowCampaignWorkflowName})
	return run
}

func (run *agentRun) execute(kind campaign.Kind) {
	run.env.ExecuteWorkflow(WordflowCampaignGeneratorWorkflow, CampaignGeneratorWorkflowInput{Kind: kind})
}

// acceptSubmission makes every submit_campaign call succeed with generated.
func acceptSubmission(generated WordflowCampaignWorkflowInput) func(AgentToolCall) ExecuteCampaignToolResult {
	return func(call AgentToolCall) ExecuteCampaignToolResult {
		if call.Name == AgentToolSubmitCampaign {
			return ExecuteCampaignToolResult{Output: `{"accepted":true}`, Campaign: &generated}
		}
		return ExecuteCampaignToolResult{Output: `{"ok":true}`}
	}
}

func TestDailyGeneratorStartsTodaysChallengeAndHandsOnTenDaysOfAnswers(t *testing.T) {
	generated := generatedCampaignFixture("daily-challenge-2026-09-22", "NEWWORD")
	run := newAgentRun(time.Date(2026, 9, 22, 0, 0, 5, 0, torontoLocation), previousGeneratedCampaigns(dailyChallengeHistory))
	run.turns = []RunCampaignAgentTurnResult{{Calls: []AgentToolCall{toolCall("submit-1", AgentToolSubmitCampaign)}}}
	run.toolResult = acceptSubmission(generated)

	run.execute(campaign.KindDailyChallenge)

	require.NoError(t, run.env.GetWorkflowError())
	startsAt := time.Date(2026, 9, 22, 0, 0, 0, 0, torontoLocation)
	endsAt := startsAt.AddDate(0, 0, 1)
	request := run.turnInputs[0].Request
	require.Equal(t, campaign.KindDailyChallenge, request.Kind)
	require.Equal(t, "2026-09-22", request.Date)
	require.True(t, startsAt.Equal(request.StartsAt))
	require.True(t, endsAt.Equal(*request.EndsAt))
	require.Len(t, request.ExcludedWords, dailyChallengeHistory)
	require.Equal(t, "OLDWORD0", request.ExcludedWords[0])
	require.Equal(t, generated, run.startedCampaign)

	var result CampaignGeneratorResult
	require.NoError(t, run.env.GetWorkflowResult(&result))
	require.Len(t, result.Recent, dailyChallengeHistory)
	require.Equal(t, "old-1", result.Recent[0].CampaignID)
	require.Equal(t, GeneratedCampaign{CampaignID: "daily-challenge-2026-09-22", Answers: []string{"NEWWORD"}}, result.Recent[9])
}

func TestAgentResubmitsAfterARejectionWithoutFailing(t *testing.T) {
	generated := generatedCampaignFixture("daily-challenge-2026-09-22", "NEWWORD")
	run := newAgentRun(time.Date(2026, 9, 22, 12, 0, 0, 0, torontoLocation), CampaignGeneratorResult{})
	run.turns = []RunCampaignAgentTurnResult{
		{Calls: []AgentToolCall{toolCall("words-1", AgentToolFindWords), toolCall("submit-1", AgentToolSubmitCampaign)}},
		{Calls: []AgentToolCall{toolCall("submit-2", AgentToolSubmitCampaign)}},
	}
	run.toolResult = func(call AgentToolCall) ExecuteCampaignToolResult {
		if call.CallID == "submit-1" {
			return ExecuteCampaignToolResult{Output: `{"accepted":false,"problems":["levels [3] are not saved yet"]}`}
		}
		return acceptSubmission(generated)(call)
	}

	run.execute(campaign.KindDailyChallenge)

	require.NoError(t, run.env.GetWorkflowError())
	require.Len(t, run.turnInputs, 2)
	require.Equal(t, generated, run.startedCampaign)
	require.Len(t, run.executed, 3)

	require.Empty(t, run.turnInputs[0].Items)
	require.Equal(t, []AgentItem{
		{Type: AgentItemToolCall, CallID: "words-1", Name: AgentToolFindWords, Arguments: `{"call":"words-1"}`},
		{Type: AgentItemToolResult, CallID: "words-1", Text: `{"ok":true}`},
		{Type: AgentItemToolCall, CallID: "submit-1", Name: AgentToolSubmitCampaign, Arguments: `{"call":"submit-1"}`},
		{Type: AgentItemToolResult, CallID: "submit-1", Text: `{"accepted":false,"problems":["levels [3] are not saved yet"]}`},
	}, run.turnInputs[1].Items, "the agent sees why its submission was rejected")
}

func TestSavedLevelsReachLaterToolCallsInTheSameTurnAndAfter(t *testing.T) {
	first := CampaignLevel{Title: "One", Letters: "ACEINORT", Words: []string{"REACTION"}}
	replaced := CampaignLevel{Title: "One again", Letters: "ACEINORT", Words: []string{"CREATION"}}
	second := CampaignLevel{Title: "Two", Letters: "ACELPRST", Words: []string{"SPECTRAL"}}
	saves := map[string]SavedCampaignLevel{
		"set-1": {Number: 1, Level: first},
		"set-2": {Number: 2, Level: second},
		"set-3": {Number: 1, Level: replaced},
	}
	generated := generatedCampaignFixture("daily-challenge-2026-09-22", "NEWWORD")
	run := newAgentRun(time.Date(2026, 9, 22, 12, 0, 0, 0, torontoLocation), CampaignGeneratorResult{})
	run.turns = []RunCampaignAgentTurnResult{
		{Calls: []AgentToolCall{toolCall("set-1", AgentToolSetLevel), toolCall("set-2", AgentToolSetLevel), toolCall("rejected", AgentToolSetLevel)}},
		{Calls: []AgentToolCall{toolCall("set-3", AgentToolSetLevel), toolCall("submit", AgentToolSubmitCampaign)}},
	}
	run.toolResult = func(call AgentToolCall) ExecuteCampaignToolResult {
		if saved, ok := saves[call.CallID]; ok {
			return ExecuteCampaignToolResult{Output: `{"ok":true}`, SavedLevel: &saved}
		}
		if call.CallID == "rejected" {
			return ExecuteCampaignToolResult{Output: `{"ok":false}`}
		}
		return acceptSubmission(generated)(call)
	}

	run.execute(campaign.KindDailyChallenge)

	require.NoError(t, run.env.GetWorkflowError())
	require.Equal(t, generated, run.startedCampaign)
	require.Equal(t, []string{"set-1", "set-2", "rejected", "set-3", "submit"}, callIDs(run.executed), "calls run in the order the model made them")
	require.Empty(t, run.drafts[0])
	require.Equal(t, CampaignDraft{1: first}, run.drafts[1], "a level saved earlier in the turn is in the draft")
	require.Equal(t, CampaignDraft{1: first, 2: second}, run.drafts[2])
	require.Equal(t, CampaignDraft{1: first, 2: second}, run.drafts[3], "a rejected level changes nothing")
	require.Equal(t, CampaignDraft{1: replaced, 2: second}, run.drafts[4], "setting a level again replaces it")
}

func callIDs(calls []AgentToolCall) []string {
	ids := make([]string, len(calls))
	for index, call := range calls {
		ids[index] = call.CallID
	}
	return ids
}

func TestAgentThatNeverSubmitsAValidCampaignFailsAfterItsTurnBudget(t *testing.T) {
	for _, test := range []struct {
		kind     campaign.Kind
		maxTurns int
	}{
		{campaign.KindDailyChallenge, dailyChallengeMaxTurns},
		{campaign.KindMonthlyPack, monthlyPackMaxTurns},
	} {
		t.Run(string(test.kind), func(t *testing.T) {
			run := newAgentRun(time.Date(2026, 9, 22, 12, 0, 0, 0, torontoLocation), CampaignGeneratorResult{})
			run.turns = []RunCampaignAgentTurnResult{{Calls: []AgentToolCall{toolCall("submit", AgentToolSubmitCampaign)}}}
			run.toolResult = func(AgentToolCall) ExecuteCampaignToolResult {
				return ExecuteCampaignToolResult{Output: `{"accepted":false,"problems":["level 1: no answer uses all 8 letters"]}`}
			}

			run.execute(test.kind)

			var applicationError *temporal.ApplicationError
			require.ErrorAs(t, run.env.GetWorkflowError(), &applicationError)
			require.Equal(t, "campaign_not_generated", applicationError.Type())
			require.True(t, applicationError.NonRetryable())
			require.ErrorContains(t, applicationError, "no answer uses all 8 letters")
			require.Len(t, run.turnInputs, test.maxTurns)
			require.Empty(t, run.startedCampaign.Definition.ID)
		})
	}
}

func TestAgentTurnWithoutToolCallsIsToldToCallATool(t *testing.T) {
	generated := generatedCampaignFixture("daily-challenge-2026-09-22", "NEWWORD")
	run := newAgentRun(time.Date(2026, 9, 22, 12, 0, 0, 0, torontoLocation), CampaignGeneratorResult{})
	run.turns = []RunCampaignAgentTurnResult{{}, {Calls: []AgentToolCall{toolCall("submit-1", AgentToolSubmitCampaign)}}}
	run.toolResult = acceptSubmission(generated)

	run.execute(campaign.KindDailyChallenge)

	require.NoError(t, run.env.GetWorkflowError())
	require.Len(t, run.turnInputs[1].Items, 1)
	nudge := run.turnInputs[1].Items[0]
	require.Equal(t, AgentItemMessage, nudge.Type)
	require.Contains(t, nudge.Text, AgentToolSubmitCampaign)
}

func TestMonthlyGeneratorStartsAPermanentPackForTheCurrentMonth(t *testing.T) {
	generated := generatedCampaignFixture("monthly-pack-2026-12", "NEWWORD")
	run := newAgentRun(time.Date(2026, 12, 14, 9, 0, 0, 0, torontoLocation), previousGeneratedCampaigns(monthlyPackHistory))
	run.turns = []RunCampaignAgentTurnResult{{Calls: []AgentToolCall{toolCall("submit-1", AgentToolSubmitCampaign)}}}
	run.toolResult = acceptSubmission(generated)

	run.execute(campaign.KindMonthlyPack)

	require.NoError(t, run.env.GetWorkflowError())
	request := run.turnInputs[0].Request
	require.Equal(t, "2026-12-01", request.Date)
	require.True(t, time.Date(2026, 12, 1, 0, 0, 0, 0, torontoLocation).Equal(request.StartsAt))
	require.Nil(t, request.EndsAt)
	require.Equal(t, generated, run.startedCampaign)

	var result CampaignGeneratorResult
	require.NoError(t, run.env.GetWorkflowResult(&result))
	require.Len(t, result.Recent, monthlyPackHistory)
	require.Equal(t, "old-1", result.Recent[0].CampaignID)
	require.Equal(t, "monthly-pack-2026-12", result.Recent[monthlyPackHistory-1].CampaignID)
}

func TestGeneratorWithoutAPreviousRunExcludesNothing(t *testing.T) {
	run := newAgentRun(time.Date(2026, 9, 22, 12, 0, 0, 0, torontoLocation), CampaignGeneratorResult{})
	run.turns = []RunCampaignAgentTurnResult{{Calls: []AgentToolCall{toolCall("submit-1", AgentToolSubmitCampaign)}}}
	run.toolResult = acceptSubmission(generatedCampaignFixture("daily-challenge-2026-09-22", "NEWWORD"))

	run.execute(campaign.KindDailyChallenge)

	require.NoError(t, run.env.GetWorkflowError())
	require.Empty(t, run.turnInputs[0].Request.ExcludedWords)
}

func TestGeneratorRejectsACampaignForTheWrongPeriod(t *testing.T) {
	run := newAgentRun(time.Date(2026, 9, 22, 12, 0, 0, 0, torontoLocation), CampaignGeneratorResult{})
	run.turns = []RunCampaignAgentTurnResult{{Calls: []AgentToolCall{toolCall("submit-1", AgentToolSubmitCampaign)}}}
	run.toolResult = acceptSubmission(generatedCampaignFixture("daily-challenge-2026-09-21", "NEWWORD"))

	run.execute(campaign.KindDailyChallenge)

	var applicationError *temporal.ApplicationError
	require.ErrorAs(t, run.env.GetWorkflowError(), &applicationError)
	require.Equal(t, "invalid_generated_campaign", applicationError.Type())
	require.True(t, applicationError.NonRetryable())
	require.Empty(t, run.startedCampaign.Definition.ID)
}

func TestGeneratorRejectsAnUnknownKindWithoutAskingTheModel(t *testing.T) {
	run := newAgentRun(time.Date(2026, 9, 22, 12, 0, 0, 0, torontoLocation), CampaignGeneratorResult{})

	run.execute("weekly")

	var applicationError *temporal.ApplicationError
	require.ErrorAs(t, run.env.GetWorkflowError(), &applicationError)
	require.Equal(t, "invalid_campaign_kind", applicationError.Type())
	require.True(t, applicationError.NonRetryable())
	require.Empty(t, run.turnInputs)
}

func TestGeneratedCampaignPeriodUsesTorontoCalendarDays(t *testing.T) {
	// 02:00 UTC on September 22 is still the evening of September 21 in Toronto.
	period, err := generatedCampaignPeriod(campaign.KindDailyChallenge, time.Date(2026, 9, 22, 2, 0, 0, 0, time.UTC))
	require.NoError(t, err)
	require.Equal(t, "2026-09-21", period.date)
	require.True(t, time.Date(2026, 9, 21, 0, 0, 0, 0, torontoLocation).Equal(period.startsAt))
	require.True(t, period.startsAt.AddDate(0, 0, 1).Equal(*period.endsAt))
	require.Equal(t, dailyChallengeMaxTurns, period.maxTurns)

	period, err = generatedCampaignPeriod(campaign.KindMonthlyPack, time.Date(2026, 12, 1, 3, 0, 0, 0, time.UTC))
	require.NoError(t, err)
	require.Equal(t, "2026-11-01", period.date, "03:00 UTC on December 1 is still November 30 in Toronto")
	require.Nil(t, period.endsAt)
	require.Equal(t, monthlyPackMaxTurns, period.maxTurns)
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
