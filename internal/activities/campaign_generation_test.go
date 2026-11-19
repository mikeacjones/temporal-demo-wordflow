package activities

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/mjones/temporal-word-game/internal/campaign"
	"github.com/mjones/temporal-word-game/internal/game"
	"github.com/mjones/temporal-word-game/internal/workflows"

	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/responses"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/temporal"
)

type fakeResponseCreator struct {
	params    []responses.ResponseNewParams
	responses []*responses.Response
}

func (f *fakeResponseCreator) New(_ context.Context, params responses.ResponseNewParams, _ ...option.RequestOption) (*responses.Response, error) {
	f.params = append(f.params, params)
	return f.responses[len(f.params)-1], nil
}

func TestGenerateDailyCampaignRequiresTheTerminalToolCall(t *testing.T) {
	generated := validGeneratedDailyCampaign()
	arguments, err := json.Marshal(generated)
	require.NoError(t, err)
	responseJSON := fmt.Sprintf(`{"output":[{"type":"reasoning"},{"type":"function_call","name":%q,"call_id":"call-1","arguments":%q}]}`,
		campaignToolName, string(arguments))
	var response responses.Response
	require.NoError(t, json.Unmarshal([]byte(responseJSON), &response))

	fake := &fakeResponseCreator{responses: []*responses.Response{&response}}
	activity := &CampaignGenerator{Responses: fake, Model: "test-model"}
	startsAt := time.Date(2026, 9, 22, 0, 0, 0, 0, time.FixedZone("EDT", -4*60*60))
	configured, err := activity.GenerateWordflowCampaign(context.Background(), workflows.GenerateWordflowCampaignActivityInput{
		Kind: campaign.KindDailyChallenge, Date: "2026-09-22", StartsAt: startsAt, EndsAt: endOf(startsAt),
		ExcludedWords: []string{"REPLAY"},
	})
	require.NoError(t, err)

	require.Len(t, fake.params, 1)
	params := fake.params[0]
	require.Equal(t, "test-model", params.Model)
	require.Equal(t, campaignToolName, params.ToolChoice.OfFunctionTool.Name)
	require.Len(t, params.Tools, 1)
	require.Equal(t, campaignToolName, params.Tools[0].OfFunction.Name)
	require.True(t, params.Tools[0].OfFunction.Strict.Value)
	require.Contains(t, params.Input.OfString.Value, "exactly three especially difficult")
	require.Contains(t, params.Input.OfString.Value, "hard 180-second limit")
	require.Contains(t, params.Input.OfString.Value, "15, 30, and 45 points")
	require.Contains(t, params.Input.OfString.Value, "REPLAY")

	require.Equal(t, "daily-challenge-2026-09-22", configured.Definition.ID)
	require.Equal(t, campaign.KindDailyChallenge, configured.Definition.Kind)
	require.True(t, configured.Definition.SingleAttempt)
	require.Equal(t, 3, configured.Definition.Unlock.InitialLevels)
	require.Equal(t, startsAt, *configured.Definition.StartsAt)
	require.Len(t, configured.Levels, 3)
	for _, level := range configured.Levels {
		require.Len(t, level.Words, 10)
		require.Equal(t, 180, level.TimeLimitSeconds)
		require.Equal(t, 20, level.BasePoints)
		require.Equal(t, game.HintPrices{Letter: 15, Brush: 30, Word: 45}, level.HintPrices)
	}
}

func TestExtractCampaignToolCallRejectsProse(t *testing.T) {
	var response responses.Response
	require.NoError(t, json.Unmarshal([]byte(`{"output":[{"type":"message"}]}`), &response))

	_, _, err := extractCampaignToolCall(&response)
	require.ErrorContains(t, err, "instead of ending with")
}

func TestGenerateDailyCampaignReturnsValidationErrorToTheTool(t *testing.T) {
	invalid := validGeneratedDailyCampaign()
	invalid.Levels[0].Words[9] = "ACTIVITY"
	invalidArguments, err := json.Marshal(invalid)
	require.NoError(t, err)
	validArguments, err := json.Marshal(validGeneratedDailyCampaign())
	require.NoError(t, err)

	response := func(id, callID string, arguments []byte) *responses.Response {
		responseJSON := fmt.Sprintf(`{"id":%q,"output":[{"type":"function_call","name":%q,"call_id":%q,"arguments":%q}]}`,
			id, campaignToolName, callID, string(arguments))
		var decoded responses.Response
		require.NoError(t, json.Unmarshal([]byte(responseJSON), &decoded))
		return &decoded
	}
	fake := &fakeResponseCreator{responses: []*responses.Response{
		response("response-1", "call-1", invalidArguments),
		response("response-2", "call-2", validArguments),
	}}
	startsAt := time.Date(2026, 9, 22, 0, 0, 0, 0, time.UTC)
	_, err = (&CampaignGenerator{Responses: fake, Model: "test-model"}).GenerateWordflowCampaign(
		context.Background(),
		workflows.GenerateWordflowCampaignActivityInput{
			Kind: campaign.KindDailyChallenge, Date: "2026-09-22", StartsAt: startsAt, EndsAt: endOf(startsAt),
		},
	)
	require.NoError(t, err)
	require.Len(t, fake.params, 2)
	require.Len(t, fake.params[1].Input.OfInputItemList, 3)
	feedback := fake.params[1].Input.OfInputItemList[2].OfFunctionCallOutput
	require.Equal(t, "call-1", feedback.CallID.Value)
	require.Contains(t, feedback.Output.OfString.Value, "repeats answer")
}

func TestBuildDailyCampaignRejectsInvalidGeneratedWords(t *testing.T) {
	generated := validGeneratedDailyCampaign()
	generated.Levels[0].Words[9] = "ACTIVITY"
	startsAt := time.Date(2026, 9, 22, 0, 0, 0, 0, time.UTC)

	_, err := buildCampaign(dailyChallengeSpec, workflows.GenerateWordflowCampaignActivityInput{
		Kind: campaign.KindDailyChallenge, Date: "2026-09-22", StartsAt: startsAt, EndsAt: endOf(startsAt),
	}, generated)
	require.ErrorContains(t, err, "repeats answer")
}

func TestBuildDailyCampaignRejectsARecentAnswer(t *testing.T) {
	generated := validGeneratedDailyCampaign()
	startsAt := time.Date(2026, 9, 22, 0, 0, 0, 0, time.UTC)

	_, err := buildCampaign(dailyChallengeSpec, workflows.GenerateWordflowCampaignActivityInput{
		Kind: campaign.KindDailyChallenge, Date: "2026-09-22", StartsAt: startsAt, EndsAt: endOf(startsAt),
		ExcludedWords: []string{"activity"},
	}, generated)
	require.ErrorContains(t, err, `reuses answer "ACTIVITY"`)
}

func TestBuildDailyCampaignRejectsPastTensePadding(t *testing.T) {
	generated := validGeneratedDailyCampaign()
	generated.Levels[1].Words[9] = "CURE"
	startsAt := time.Date(2026, 9, 22, 0, 0, 0, 0, time.UTC)

	_, err := buildCampaign(dailyChallengeSpec, workflows.GenerateWordflowCampaignActivityInput{
		Kind: campaign.KindDailyChallenge, Date: "2026-09-22", StartsAt: startsAt, EndsAt: endOf(startsAt),
	}, generated)
	require.ErrorContains(t, err, "past-tense form")
}

func endOf(startsAt time.Time) *time.Time {
	endsAt := startsAt.Add(24 * time.Hour)
	return &endsAt
}

func validGeneratedDailyCampaign() generatedCampaign {
	return generatedCampaign{
		Description: "Three expert Wordflows with time pressure, richer rewards, premium hints, and only one attempt.",
		Levels: []generatedWordflowLevel{
			{
				Title: "Activity Pressure", Letters: "ACIITTVY",
				Words: []string{"ACTIVITY", "CITY", "TACT", "IVY", "ACT", "CAT", "VAT", "VIA", "TIC", "ICY"},
			},
			{
				Title: "Cloud Scramble", Letters: "CDEILORU",
				Words: []string{"CLOUDIER", "CLOUD", "COULD", "CRUDE", "CURED", "LURED", "IDLE", "RIDE", "RULE", "LOUD"},
			},
			{
				Title: "Rollback Rush", Letters: "ABCKLLOR",
				Words: []string{"ROLLBACK", "ROLL", "BACK", "BALL", "CALL", "ROCK", "LACK", "LOCK", "ORCA", "CAB"},
			},
		},
	}
}

func validGeneratedMonthlyCampaign() generatedCampaign {
	generated := validGeneratedDailyCampaign()
	generated.Description = "Nine themed Wordflows that start gently and finish with real vocabulary, at your own pace."
	generated.Levels = append(generated.Levels,
		generatedWordflowLevel{
			Title: "Plant Plan", Letters: "AGILNNPT",
			Words: []string{"PLANTING", "TAPING", "PLAIN", "PLANT", "PAINT", "ALIGN", "GIANT", "PLAN", "PANG", "TANG"},
		},
		generatedWordflowLevel{
			Title: "After Dark", Letters: "DGHIIMNT",
			Words: []string{"MIDNIGHT", "MIGHT", "NIGHT", "TIMING", "TIDING", "THING", "MIND", "MINT", "HINT", "DING"},
		},
		generatedWordflowLevel{
			Title: "Gather Round", Letters: "ADEGHRTU",
			Words: []string{"DAUGHTER", "GATHER", "THREAD", "TRUDGE", "GRATED", "EARTH", "HEARD", "TRADE", "DRAG", "RUDE"},
		},
		generatedWordflowLevel{
			Title: "Stranger Danger", Letters: "AEGNRRST",
			Words: []string{"STRANGER", "RANGER", "GARNET", "ANGER", "GRANT", "STAGE", "RENT", "STAR", "EARN", "SNAG"},
		},
		generatedWordflowLevel{
			Title: "Pocket Champion", Letters: "ACHIMNOP",
			Words: []string{"CHAMPION", "PINCH", "PIANO", "NACHO", "POACH", "CHIN", "CHOP", "CAMP", "CHIP", "PAIN"},
		},
		generatedWordflowLevel{
			Title: "Hold Steady", Letters: "DEHLORSU",
			Words: []string{"SHOULDER", "SHOULD", "HOLDER", "HURDLE", "SOLDER", "LOUDER", "HOUSE", "RULED", "HOLD", "SHED"},
		},
	)
	return generated
}

func TestBuildMonthlyCampaignIsPermanentAndUsesStandardRules(t *testing.T) {
	startsAt := time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)

	configured, err := buildCampaign(monthlyPackSpec, workflows.GenerateWordflowCampaignActivityInput{
		Kind: campaign.KindMonthlyPack, Date: "2026-12-01", StartsAt: startsAt,
	}, validGeneratedMonthlyCampaign())
	require.NoError(t, err)

	definition := configured.Definition
	require.Equal(t, "monthly-pack-2026-12", definition.ID)
	require.Equal(t, campaign.KindMonthlyPack, definition.Kind)
	require.Equal(t, "Monthly Pack · December 2026", definition.Title)
	require.False(t, definition.SingleAttempt)
	require.Nil(t, definition.EndsAt)
	require.Equal(t, startsAt, *definition.StartsAt)
	require.Equal(t, campaign.UnlockPolicy{InitialLevels: 3, LevelsPerInterval: 3, Interval: 24 * time.Hour}, definition.Unlock)
	require.Len(t, configured.Levels, 9)
	for _, level := range configured.Levels {
		require.Len(t, level.Words, 10)
		require.Zero(t, level.TimeLimitSeconds)
		require.Zero(t, level.BasePoints)
		require.Zero(t, level.HintPrices)
	}
}

func TestBuildMonthlyCampaignRequiresNineLevels(t *testing.T) {
	_, err := buildCampaign(monthlyPackSpec, workflows.GenerateWordflowCampaignActivityInput{
		Kind: campaign.KindMonthlyPack, Date: "2026-12-01", StartsAt: time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC),
	}, validGeneratedDailyCampaign())
	require.ErrorContains(t, err, "exactly 9 levels")
}

func TestMonthlyGenerationAsksForNineLevelsAndExcludesRecentPacks(t *testing.T) {
	arguments, err := json.Marshal(validGeneratedMonthlyCampaign())
	require.NoError(t, err)
	var response responses.Response
	require.NoError(t, json.Unmarshal([]byte(fmt.Sprintf(`{"output":[{"type":"function_call","name":%q,"call_id":"call-1","arguments":%q}]}`,
		campaignToolName, string(arguments))), &response))
	fake := &fakeResponseCreator{responses: []*responses.Response{&response}}

	configured, err := (&CampaignGenerator{Responses: fake, Model: "test-model"}).GenerateWordflowCampaign(
		context.Background(),
		workflows.GenerateWordflowCampaignActivityInput{
			Kind: campaign.KindMonthlyPack, Date: "2026-12-01",
			StartsAt: time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC), ExcludedWords: []string{"REPLAY"},
		},
	)
	require.NoError(t, err)
	require.Len(t, configured.Levels, 9)

	prompt := fake.params[0].Input.OfString.Value
	require.Contains(t, prompt, "Monthly Pack for December 2026")
	require.Contains(t, prompt, "exactly nine levels")
	require.Contains(t, prompt, "previous monthly packs: REPLAY")
	require.NotContains(t, prompt, "180-second")
	schema := fake.params[0].Tools[0].OfFunction.Parameters["properties"].(map[string]any)["levels"].(map[string]any)
	require.Equal(t, 9, schema["minItems"])
	require.Equal(t, 9, schema["maxItems"])
}

func TestGenerateRejectsAnUnknownKindWithoutCallingOpenAI(t *testing.T) {
	fake := &fakeResponseCreator{}

	_, err := (&CampaignGenerator{Responses: fake}).GenerateWordflowCampaign(
		context.Background(), workflows.GenerateWordflowCampaignActivityInput{Kind: "weekly"},
	)

	var applicationError *temporal.ApplicationError
	require.ErrorAs(t, err, &applicationError)
	require.True(t, applicationError.NonRetryable())
	require.Empty(t, fake.params)
}

func TestGenerateFailsWithoutAnOpenAIKey(t *testing.T) {
	t.Setenv(OpenAIKeyEnvironment, "")

	_, err := (&CampaignGenerator{}).GenerateWordflowCampaign(
		context.Background(), workflows.GenerateWordflowCampaignActivityInput{Kind: campaign.KindDailyChallenge},
	)

	var applicationError *temporal.ApplicationError
	require.ErrorAs(t, err, &applicationError)
	require.Equal(t, "openai_not_configured", applicationError.Type())
	require.True(t, applicationError.NonRetryable())
}
