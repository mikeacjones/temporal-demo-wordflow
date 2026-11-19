package activities

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/mjones/temporal-word-game/internal/campaign"
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

func functionCallsResponse(t *testing.T, calls ...workflows.AgentToolCall) *responses.Response {
	t.Helper()
	output := `{"type":"reasoning"}`
	for _, call := range calls {
		output += fmt.Sprintf(`,{"type":"function_call","name":%q,"call_id":%q,"arguments":%q}`, call.Name, call.CallID, call.Arguments)
	}
	var response responses.Response
	require.NoError(t, json.Unmarshal([]byte(`{"output":[`+output+`]}`), &response))
	return &response
}

func levelArguments(t *testing.T, level generatedWordflowLevel) string {
	t.Helper()
	encoded, err := json.Marshal(level)
	require.NoError(t, err)
	return string(encoded)
}

func dailyRequest() workflows.CampaignRequest {
	startsAt := time.Date(2026, 9, 22, 0, 0, 0, 0, time.FixedZone("EDT", -4*60*60))
	return workflows.CampaignRequest{
		Kind: campaign.KindDailyChallenge, Date: "2026-09-22", StartsAt: startsAt, EndsAt: endOf(startsAt),
	}
}

func TestRunCampaignAgentTurnOffersThreeToolsAndReturnsItsCalls(t *testing.T) {
	calls := []workflows.AgentToolCall{
		{CallID: "call-1", Name: workflows.AgentToolCheckWords, Arguments: `{"letters":"ACIITTVY"}`},
		{CallID: "call-2", Name: workflows.AgentToolCheckLevel, Arguments: `{"letters":"CDEILORU"}`},
	}
	fake := &fakeResponseCreator{responses: []*responses.Response{functionCallsResponse(t, calls...)}}
	request := dailyRequest()
	request.ExcludedWords = []string{"REPLAY"}

	result, err := (&CampaignGenerator{Responses: fake, Model: "test-model"}).RunCampaignAgentTurn(
		context.Background(), workflows.RunCampaignAgentTurnInput{Request: request},
	)
	require.NoError(t, err)
	require.Equal(t, calls, result.Calls)

	params := fake.params[0]
	require.Equal(t, "test-model", params.Model)
	require.Equal(t, responses.ToolChoiceOptionsRequired, params.ToolChoice.OfToolChoiceMode.Value)
	require.True(t, params.ParallelToolCalls.Value)
	require.False(t, params.Store.Value)
	var names []string
	for _, tool := range params.Tools {
		names = append(names, tool.OfFunction.Name)
		require.True(t, tool.OfFunction.Strict.Value)
	}
	require.Equal(t, []string{workflows.AgentToolCheckWords, workflows.AgentToolCheckLevel, workflows.AgentToolSubmitCampaign}, names)

	require.Len(t, params.Input.OfInputItemList, 1)
	prompt := params.Input.OfInputItemList[0].OfMessage.Content.OfString.Value
	require.Contains(t, prompt, "exactly three especially difficult")
	require.Contains(t, prompt, "hard 180-second limit")
	require.Contains(t, prompt, "15, 30, and 45 points")
	require.Contains(t, prompt, "REPLAY")
	require.Contains(t, prompt, "check_level")
}

func TestRunCampaignAgentTurnSendsTheConversationSoFar(t *testing.T) {
	fake := &fakeResponseCreator{responses: []*responses.Response{functionCallsResponse(t)}}
	items := []workflows.AgentItem{
		{Type: workflows.AgentItemToolCall, CallID: "call-1", Name: workflows.AgentToolSubmitCampaign, Arguments: `{"levels":[]}`},
		{Type: workflows.AgentItemToolResult, CallID: "call-1", Text: `{"accepted":false,"error":"too few levels"}`},
		{Type: workflows.AgentItemMessage, Text: "Call a tool."},
	}

	result, err := (&CampaignGenerator{Responses: fake}).RunCampaignAgentTurn(
		context.Background(), workflows.RunCampaignAgentTurnInput{Request: dailyRequest(), Items: items},
	)
	require.NoError(t, err)
	require.Empty(t, result.Calls)

	input := fake.params[0].Input.OfInputItemList
	require.Len(t, input, 4, "the opening prompt, then each item in order")
	require.Equal(t, "call-1", input[1].OfFunctionCall.CallID)
	require.Equal(t, workflows.AgentToolSubmitCampaign, input[1].OfFunctionCall.Name)
	require.Equal(t, "call-1", input[2].OfFunctionCallOutput.CallID.Value)
	require.Contains(t, input[2].OfFunctionCallOutput.Output.OfString.Value, "too few levels")
	require.Equal(t, "Call a tool.", input[3].OfMessage.Content.OfString.Value)
}

func TestMonthlyTurnAsksForNineLevelsAndExcludesRecentPacks(t *testing.T) {
	fake := &fakeResponseCreator{responses: []*responses.Response{functionCallsResponse(t)}}
	request := workflows.CampaignRequest{
		Kind: campaign.KindMonthlyPack, Date: "2026-12-01",
		StartsAt: time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC), ExcludedWords: []string{"REPLAY"},
	}

	_, err := (&CampaignGenerator{Responses: fake}).RunCampaignAgentTurn(
		context.Background(), workflows.RunCampaignAgentTurnInput{Request: request},
	)
	require.NoError(t, err)

	prompt := fake.params[0].Input.OfInputItemList[0].OfMessage.Content.OfString.Value
	require.Contains(t, prompt, "Monthly Pack for December 2026")
	require.Contains(t, prompt, "exactly nine levels")
	require.Contains(t, prompt, "previous monthly packs: REPLAY")
	require.NotContains(t, prompt, "180-second")
	submit := fake.params[0].Tools[2].OfFunction
	require.Equal(t, workflows.AgentToolSubmitCampaign, submit.Name)
	levels := submit.Parameters["properties"].(map[string]any)["levels"].(map[string]any)
	require.Equal(t, 9, levels["minItems"])
	require.Equal(t, 9, levels["maxItems"])
}

func TestRunCampaignAgentTurnRejectsAnUnknownKindWithoutCallingOpenAI(t *testing.T) {
	fake := &fakeResponseCreator{}

	_, err := (&CampaignGenerator{Responses: fake}).RunCampaignAgentTurn(
		context.Background(), workflows.RunCampaignAgentTurnInput{Request: workflows.CampaignRequest{Kind: "weekly"}},
	)

	var applicationError *temporal.ApplicationError
	require.ErrorAs(t, err, &applicationError)
	require.True(t, applicationError.NonRetryable())
	require.Empty(t, fake.params)
}

func TestRunCampaignAgentTurnFailsWithoutAnOpenAIKey(t *testing.T) {
	t.Setenv(OpenAIKeyEnvironment, "")

	_, err := (&CampaignGenerator{}).RunCampaignAgentTurn(
		context.Background(), workflows.RunCampaignAgentTurnInput{Request: dailyRequest()},
	)

	var applicationError *temporal.ApplicationError
	require.ErrorAs(t, err, &applicationError)
	require.Equal(t, "openai_not_configured", applicationError.Type())
	require.True(t, applicationError.NonRetryable())
}

func executeTool(t *testing.T, request workflows.CampaignRequest, name, arguments string) workflows.ExecuteCampaignToolResult {
	t.Helper()
	result, err := (&CampaignGenerator{}).ExecuteCampaignTool(context.Background(), workflows.ExecuteCampaignToolInput{
		Request: request, Call: workflows.AgentToolCall{CallID: "call", Name: name, Arguments: arguments},
	})
	require.NoError(t, err, "a rejection is tool output, never an Activity failure")
	return result
}

func TestChecksAcceptAValidLevel(t *testing.T) {
	arguments := levelArguments(t, validGeneratedDailyCampaign().Levels[0])

	for _, tool := range []string{workflows.AgentToolCheckWords, workflows.AgentToolCheckLevel} {
		result := executeTool(t, dailyRequest(), tool, arguments)
		require.JSONEq(t, `{"ok":true}`, result.Output, tool)
		require.Nil(t, result.Campaign)
	}
}

func TestChecksReturnWhatIsWrongWithALevel(t *testing.T) {
	level := validGeneratedDailyCampaign().Levels[0]
	level.Words[9] = "ACTIVITY"
	request := dailyRequest()

	repeated := executeTool(t, request, workflows.AgentToolCheckWords, levelArguments(t, level))
	require.Contains(t, repeated.Output, `"ok":false`)
	require.Contains(t, repeated.Output, "repeats answer")

	level = validGeneratedDailyCampaign().Levels[0]
	request.ExcludedWords = []string{"icy"}
	recent := executeTool(t, request, workflows.AgentToolCheckLevel, levelArguments(t, level))
	require.Contains(t, recent.Output, `"ok":false`)
	require.Contains(t, recent.Output, `reuses answer \"ICY\"`)
}

func TestToolsReturnBadArgumentsAndUnknownToolsAsOutput(t *testing.T) {
	for _, call := range []struct{ name, arguments, want string }{
		{workflows.AgentToolCheckWords, `{"letters":`, "invalid arguments"},
		{workflows.AgentToolSubmitCampaign, `not json`, "invalid arguments"},
		{"rm_rf", `{}`, `unknown tool`},
	} {
		result := executeTool(t, dailyRequest(), call.name, call.arguments)
		require.Contains(t, result.Output, call.want, call.name)
		require.Nil(t, result.Campaign)
	}
}

func TestSubmitAcceptsAValidCampaignAndReturnsIt(t *testing.T) {
	arguments, err := json.Marshal(validGeneratedDailyCampaign())
	require.NoError(t, err)

	result := executeTool(t, dailyRequest(), workflows.AgentToolSubmitCampaign, string(arguments))

	require.JSONEq(t, `{"accepted":true}`, result.Output)
	require.NotNil(t, result.Campaign)
	require.Equal(t, "daily-challenge-2026-09-22", result.Campaign.Definition.ID)
	require.Equal(t, campaign.KindDailyChallenge, result.Campaign.Definition.Kind)
	require.True(t, result.Campaign.Definition.SingleAttempt)
	require.Len(t, result.Campaign.Levels, 3)
	for _, level := range result.Campaign.Levels {
		require.Equal(t, 180, level.TimeLimitSeconds)
		require.Equal(t, 20, level.BasePoints)
	}
}

func TestSubmitRejectionCarriesTheReasonAndNoCampaign(t *testing.T) {
	invalid := validGeneratedDailyCampaign()
	invalid.Levels[0].Words[9] = "ACTIVITY"
	arguments, err := json.Marshal(invalid)
	require.NoError(t, err)

	result := executeTool(t, dailyRequest(), workflows.AgentToolSubmitCampaign, string(arguments))

	require.Contains(t, result.Output, `"accepted":false`)
	require.Contains(t, result.Output, "repeats answer")
	require.Nil(t, result.Campaign)
}

func TestSubmitAcceptsAValidMonthlyPack(t *testing.T) {
	arguments, err := json.Marshal(validGeneratedMonthlyCampaign())
	require.NoError(t, err)
	request := workflows.CampaignRequest{
		Kind: campaign.KindMonthlyPack, Date: "2026-12-01", StartsAt: time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC),
	}

	result := executeTool(t, request, workflows.AgentToolSubmitCampaign, string(arguments))

	require.JSONEq(t, `{"accepted":true}`, result.Output)
	require.Equal(t, "monthly-pack-2026-12", result.Campaign.Definition.ID)
	require.Len(t, result.Campaign.Levels, 9)
}

func TestBuildDailyCampaignRejectsInvalidGeneratedWords(t *testing.T) {
	generated := validGeneratedDailyCampaign()
	generated.Levels[0].Words[9] = "ACTIVITY"
	startsAt := time.Date(2026, 9, 22, 0, 0, 0, 0, time.UTC)

	_, err := buildCampaign(dailyChallengeSpec, workflows.CampaignRequest{
		Kind: campaign.KindDailyChallenge, Date: "2026-09-22", StartsAt: startsAt, EndsAt: endOf(startsAt),
	}, generated)
	require.ErrorContains(t, err, "repeats answer")
}

func TestBuildDailyCampaignRejectsARecentAnswer(t *testing.T) {
	generated := validGeneratedDailyCampaign()
	startsAt := time.Date(2026, 9, 22, 0, 0, 0, 0, time.UTC)

	_, err := buildCampaign(dailyChallengeSpec, workflows.CampaignRequest{
		Kind: campaign.KindDailyChallenge, Date: "2026-09-22", StartsAt: startsAt, EndsAt: endOf(startsAt),
		ExcludedWords: []string{"activity"},
	}, generated)
	require.ErrorContains(t, err, `reuses answer "ACTIVITY"`)
}

func TestBuildDailyCampaignRejectsPastTensePadding(t *testing.T) {
	generated := validGeneratedDailyCampaign()
	generated.Levels[1].Words[9] = "CURE"
	startsAt := time.Date(2026, 9, 22, 0, 0, 0, 0, time.UTC)

	_, err := buildCampaign(dailyChallengeSpec, workflows.CampaignRequest{
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

	configured, err := buildCampaign(monthlyPackSpec, workflows.CampaignRequest{
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
	_, err := buildCampaign(monthlyPackSpec, workflows.CampaignRequest{
		Kind: campaign.KindMonthlyPack, Date: "2026-12-01", StartsAt: time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC),
	}, validGeneratedDailyCampaign())
	require.ErrorContains(t, err, "exactly 9 levels")
}

// This test goes through the real OpenAI client, pointed at a local server, to
// pin the request the model actually receives and the response shape it returns.
func TestRunCampaignAgentTurnWireFormat(t *testing.T) {
	var body map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		require.Equal(t, "/responses", request.URL.Path)
		require.NoError(t, json.NewDecoder(request.Body).Decode(&body))
		writer.Header().Set("Content-Type", "application/json")
		fmt.Fprint(writer, `{"id":"resp","object":"response","status":"completed","output":[
			{"type":"reasoning","id":"rs","summary":[]},
			{"type":"function_call","id":"fc1","call_id":"call-9","name":"check_level","arguments":"{\"title\":\"T\"}","status":"completed"},
			{"type":"function_call","id":"fc2","call_id":"call-10","name":"check_words","arguments":"{}","status":"completed"}]}`)
	}))
	defer server.Close()
	t.Setenv("OPENAI_BASE_URL", server.URL)
	t.Setenv(OpenAIKeyEnvironment, "test-key")

	result, err := (&CampaignGenerator{Model: "test-model"}).RunCampaignAgentTurn(context.Background(), workflows.RunCampaignAgentTurnInput{
		Request: dailyRequest(),
		Items: []workflows.AgentItem{
			{Type: workflows.AgentItemToolCall, CallID: "call-1", Name: workflows.AgentToolSubmitCampaign, Arguments: `{"levels":[]}`},
			{Type: workflows.AgentItemToolResult, CallID: "call-1", Text: `{"accepted":false}`},
		},
	})
	require.NoError(t, err)
	require.Equal(t, []workflows.AgentToolCall{
		{CallID: "call-9", Name: "check_level", Arguments: `{"title":"T"}`},
		{CallID: "call-10", Name: "check_words", Arguments: `{}`},
	}, result.Calls)

	require.Equal(t, "required", body["tool_choice"])
	require.Equal(t, true, body["parallel_tool_calls"])
	require.Equal(t, false, body["store"])
	require.Len(t, body["tools"], 3)
	for _, tool := range body["tools"].([]any) {
		require.Equal(t, "function", tool.(map[string]any)["type"])
		require.Equal(t, true, tool.(map[string]any)["strict"])
	}
	input := body["input"].([]any)
	require.Len(t, input, 3)
	require.Equal(t, "user", input[0].(map[string]any)["role"])
	require.Equal(t, map[string]any{
		"type": "function_call", "call_id": "call-1", "name": "submit_campaign", "arguments": `{"levels":[]}`,
	}, input[1])
	require.Equal(t, map[string]any{
		"type": "function_call_output", "call_id": "call-1", "output": `{"accepted":false}`,
	}, input[2])
}
