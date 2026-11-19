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
	"github.com/mjones/temporal-word-game/internal/dictionary"
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

func dailyRequest() workflows.CampaignRequest {
	startsAt := time.Date(2026, 9, 22, 0, 0, 0, 0, time.FixedZone("EDT", -4*60*60))
	return workflows.CampaignRequest{
		Kind: campaign.KindDailyChallenge, Date: "2026-09-22", StartsAt: startsAt, EndsAt: endOf(startsAt),
	}
}

func TestRunCampaignAgentTurnOffersFourToolsAndReturnsItsCalls(t *testing.T) {
	calls := []workflows.AgentToolCall{
		{CallID: "call-1", Name: workflows.AgentToolFindWords, Arguments: `{"letters":"ACIITTVY"}`},
		{CallID: "call-2", Name: workflows.AgentToolSetLevel, Arguments: `{"letters":"CDEILORU"}`},
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
	require.Equal(t, []string{
		workflows.AgentToolSuggestAnchors, workflows.AgentToolFindWords, workflows.AgentToolSetLevel, workflows.AgentToolSubmitCampaign,
	}, names)

	instructions := params.Instructions.Value
	require.Contains(t, instructions, "bank of eight letters and an empty crossword", "the agent is told how the game plays")
	require.Contains(t, instructions, "must cross an answer already placed", "and how the crossword is laid out")

	require.Len(t, params.Input.OfInputItemList, 1)
	prompt := params.Input.OfInputItemList[0].OfMessage.Content.OfString.Value
	require.Contains(t, prompt, "exactly three especially difficult")
	require.Contains(t, prompt, "hard 180-second limit")
	require.Contains(t, prompt, "15, 30, and 45 points")
	require.Contains(t, prompt, "find_words already leaves out the answers from the previous ten daily challenges")
	require.NotContains(t, prompt, "REPLAY", "the tools apply the exclusions, so the prompt doesn't list them")
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
	require.Contains(t, prompt, "the answers from the previous monthly packs")
	require.NotContains(t, prompt, "180-second")
	setLevel := fake.params[0].Tools[2].OfFunction
	require.Equal(t, workflows.AgentToolSetLevel, setLevel.Name)
	level := setLevel.Parameters["properties"].(map[string]any)["level"].(map[string]any)
	require.Equal(t, 1, level["minimum"])
	require.Equal(t, 9, level["maximum"])
	require.Equal(t, []string{"letters", "level", "title", "words"}, setLevel.Parameters["required"])
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

func executeTool(t *testing.T, request workflows.CampaignRequest, draft workflows.CampaignDraft, name, arguments string) workflows.ExecuteCampaignToolResult {
	t.Helper()
	result, err := (&CampaignGenerator{}).ExecuteCampaignTool(context.Background(), workflows.ExecuteCampaignToolInput{
		Request: request, Draft: draft, Call: workflows.AgentToolCall{CallID: "call", Name: name, Arguments: arguments},
	})
	require.NoError(t, err, "a rejection is tool output, never an Activity failure")
	return result
}

func jsonArguments(t *testing.T, value any) string {
	t.Helper()
	encoded, err := json.Marshal(value)
	require.NoError(t, err)
	return string(encoded)
}

func setLevelArguments(t *testing.T, number int, level workflows.CampaignLevel) string {
	t.Helper()
	return jsonArguments(t, map[string]any{"level": number, "title": level.Title, "letters": level.Letters, "words": level.Words})
}

func decodeOutput(t *testing.T, result workflows.ExecuteCampaignToolResult) map[string]any {
	t.Helper()
	var output map[string]any
	require.NoError(t, json.Unmarshal([]byte(result.Output), &output))
	return output
}

func draftOf(levels []workflows.CampaignLevel) workflows.CampaignDraft {
	draft := workflows.CampaignDraft{}
	for index, level := range levels {
		draft[index+1] = level
	}
	return draft
}

func TestSuggestAnchorsReturnsUsableBanksThatDifferFromSavedLevels(t *testing.T) {
	words, err := dictionary.Load()
	require.NoError(t, err)
	request := dailyRequest()
	draft := draftOf(validDailyLevels()[:1])

	result := executeTool(t, request, draft, workflows.AgentToolSuggestAnchors, `{}`)

	var output struct {
		Anchors []struct {
			Word        string `json:"word"`
			CommonWords int    `json:"common_words"`
		} `json:"anchors"`
	}
	require.NoError(t, json.Unmarshal([]byte(result.Output), &output))
	require.Len(t, output.Anchors, anchorSuggestions)
	previousShared := 0
	for _, anchor := range output.Anchors {
		require.Len(t, anchor.Word, 8)
		require.True(t, words.IsCommon(anchor.Word), anchor.Word)
		require.False(t, isInflection(words, anchor.Word), anchor.Word)
		require.GreaterOrEqual(t, anchor.CommonWords, minAnchorWords, anchor.Word)
		shared := mostSharedLetters(anchor.Word, draft)
		require.GreaterOrEqual(t, shared, previousShared, "anchors sharing fewer letters with saved levels come first")
		previousShared = shared
	}
	require.Nil(t, result.SavedLevel)
	require.Nil(t, result.Campaign)
}

func TestIsInflection(t *testing.T) {
	words, err := dictionary.Load()
	require.NoError(t, err)
	for _, word := range []string{"TEARINGS", "STOPPING", "STOPPED", "BAKING", "HOUSES", "MOUNTAINS"} {
		require.True(t, isInflection(words, word), word)
	}
	for _, word := range []string{"REACTION", "SPECTRAL", "ANCESTOR", "PROGRESS", "STRANGER"} {
		require.False(t, isInflection(words, word), word)
	}
}

func TestFindWordsListsCommonWordsTheBankCanSpell(t *testing.T) {
	request := dailyRequest()
	request.ExcludedWords = []string{"creation"}
	draft := workflows.CampaignDraft{2: {Title: "Saved", Letters: "AEGINRST", Words: []string{"RATIO"}}}

	result := executeTool(t, request, draft, workflows.AgentToolFindWords, `{"letters":"ACEINORT"}`)

	var output struct {
		Letters string         `json:"letters"`
		Words   []string       `json:"words"`
		Used    map[string]int `json:"used_by_saved_levels"`
	}
	require.NoError(t, json.Unmarshal([]byte(result.Output), &output))
	require.Equal(t, "ACEINORT", output.Letters)
	require.Equal(t, "REACTION", output.Words[0], "longest first")
	require.Contains(t, output.Words, "RATIO")
	require.NotContains(t, output.Words, "CREATION", "recent answers are left out")
	require.NotContains(t, output.Words, "AIRT", "obscure words are left out")
	require.Equal(t, map[string]int{"RATIO": 2}, output.Used)
	for index := 1; index < len(output.Words); index++ {
		require.GreaterOrEqual(t, len(output.Words[index-1]), len(output.Words[index]))
	}
}

func TestFindWordsRejectsABadLetterBank(t *testing.T) {
	for _, letters := range []string{"ABC", "ACEINOR1", "ACEINORTS"} {
		result := executeTool(t, dailyRequest(), nil, workflows.AgentToolFindWords, jsonArguments(t, map[string]string{"letters": letters}))
		require.Contains(t, result.Output, "letters must be exactly 8 letters", letters)
	}
}

func TestSetLevelSavesAValidLevel(t *testing.T) {
	level := validDailyLevels()[1]
	level.Words[0] = "cloudier "
	draft := draftOf(validDailyLevels()[:1])

	result := executeTool(t, dailyRequest(), draft, workflows.AgentToolSetLevel, setLevelArguments(t, 2, level))

	require.JSONEq(t, `{"ok":true,"level":2,"saved_levels":[1,2],"levels_still_needed":[3]}`, result.Output)
	require.NotNil(t, result.SavedLevel)
	require.Equal(t, 2, result.SavedLevel.Number)
	require.Equal(t, "CLOUDIER", result.SavedLevel.Level.Words[0], "the saved level is normalized")
	require.Nil(t, result.Campaign)
}

func TestSetLevelListsEveryProblemAtOnce(t *testing.T) {
	request := dailyRequest()
	request.ExcludedWords = []string{"CITY"}
	draft := workflows.CampaignDraft{2: {Title: "Saved", Letters: "ACIITTVY", Words: []string{"TACT"}}}
	level := workflows.CampaignLevel{
		Title: "Broken", Letters: "ACIITTVY",
		Words: []string{"ACTIVITY", "CITY", "TACT", "TAIT", "VITA", "ITCHY", "ACT", "CAT", "CATS", "ACT"},
	}

	result := executeTool(t, request, draft, workflows.AgentToolSetLevel, setLevelArguments(t, 1, level))

	output := decodeOutput(t, result)
	require.Equal(t, false, output["ok"])
	require.Equal(t, []any{
		"CITY is already used in a recent campaign",
		"TACT is already used in level 2",
		"TAIT is not an English word",
		"VITA is too obscure for players to find; use a common word from find_words",
		"ITCHY cannot be spelled from ACIITTVY",
		"CATS cannot be spelled from ACIITTVY",
		"ACT appears twice",
		"CAT and its plural are both answers; drop one",
	}, output["problems"])
	require.Nil(t, result.SavedLevel)
}

func TestSetLevelRejectsABankWithoutCheckingItsWords(t *testing.T) {
	level := validDailyLevels()[0]
	level.Letters = "ACIITTV"

	result := executeTool(t, dailyRequest(), nil, workflows.AgentToolSetLevel, setLevelArguments(t, 1, level))

	require.JSONEq(t, `{"ok":false,"level":1,"problems":["letters must be exactly 8 letters A-Z"]}`, result.Output)
}

func TestSetLevelRequiresTheAnchor(t *testing.T) {
	level := validDailyLevels()[0]
	level.Words[0] = "TIC" // replaces ACTIVITY
	level.Words[8] = "TAT"

	result := executeTool(t, dailyRequest(), nil, workflows.AgentToolSetLevel, setLevelArguments(t, 1, level))

	require.Contains(t, result.Output, "no answer uses all 8 letters")
}

func TestSetLevelCanReplaceASavedLevelWithItsOwnAnswers(t *testing.T) {
	level := validDailyLevels()[0]
	draft := draftOf(validDailyLevels())
	level.Title = "Renamed"

	result := executeTool(t, dailyRequest(), draft, workflows.AgentToolSetLevel, setLevelArguments(t, 1, level))

	require.JSONEq(t, `{"ok":true,"level":1,"saved_levels":[1,2,3],"levels_still_needed":[]}`, result.Output)
	require.Equal(t, "Renamed", result.SavedLevel.Level.Title)
}

func TestSetLevelRejectsALevelNumberOutsideTheCampaign(t *testing.T) {
	for _, number := range []int{0, 4} {
		result := executeTool(t, dailyRequest(), nil, workflows.AgentToolSetLevel, setLevelArguments(t, number, validDailyLevels()[0]))
		require.Contains(t, result.Output, "level must be 1 to 3")
		require.Nil(t, result.SavedLevel)
	}
}

func TestToolsReturnBadArgumentsAndUnknownToolsAsOutput(t *testing.T) {
	for _, call := range []struct{ name, arguments, want string }{
		{workflows.AgentToolFindWords, `{"letters":`, "invalid arguments"},
		{workflows.AgentToolSetLevel, `{"level":"one"}`, "invalid arguments"},
		{workflows.AgentToolSubmitCampaign, `not json`, "invalid arguments"},
		{"rm_rf", `{}`, `unknown tool`},
	} {
		result := executeTool(t, dailyRequest(), nil, call.name, call.arguments)
		require.Contains(t, result.Output, call.want, call.name)
		require.Nil(t, result.Campaign)
		require.Nil(t, result.SavedLevel)
	}
}

const testDescription = "Three expert Wordflows with time pressure, richer rewards, premium hints, and only one attempt."

func TestSubmitAcceptsASavedDailyCampaign(t *testing.T) {
	result := executeTool(t, dailyRequest(), draftOf(validDailyLevels()), workflows.AgentToolSubmitCampaign,
		jsonArguments(t, map[string]string{"description": testDescription}))

	require.JSONEq(t, `{"accepted":true}`, result.Output)
	require.NotNil(t, result.Campaign)
	require.Equal(t, "daily-challenge-2026-09-22", result.Campaign.Definition.ID)
	require.Equal(t, campaign.KindDailyChallenge, result.Campaign.Definition.Kind)
	require.Equal(t, testDescription, result.Campaign.Definition.Description)
	require.True(t, result.Campaign.Definition.SingleAttempt)
	require.Len(t, result.Campaign.Levels, 3)
	for index, level := range result.Campaign.Levels {
		require.Equal(t, index+1, level.Level)
		require.Len(t, level.Words, 10)
		require.Equal(t, 180, level.TimeLimitSeconds)
		require.Equal(t, 20, level.BasePoints)
	}
}

func TestSubmitListsTheLevelsStillMissing(t *testing.T) {
	result := executeTool(t, dailyRequest(), draftOf(validDailyLevels()[:1]), workflows.AgentToolSubmitCampaign,
		jsonArguments(t, map[string]string{"description": testDescription}))

	require.JSONEq(t, `{"accepted":false,"problems":["levels [2 3] are not saved yet; save them with set_level"]}`, result.Output)
	require.Nil(t, result.Campaign)
}

func TestSubmitChecksTheWholeDraftAgain(t *testing.T) {
	// set_level keeps the draft consistent, so this is a safety net. Each word
	// gets its most basic problem, and a repeat is reported on its later level.
	levels := validDailyLevels()
	levels[2] = validDailyLevels()[0]
	levels[1].Words[1] = "TACT"
	request := dailyRequest()
	request.ExcludedWords = []string{"LOUD"}

	result := executeTool(t, request, draftOf(levels), workflows.AgentToolSubmitCampaign,
		jsonArguments(t, map[string]string{"description": testDescription}))

	problems := decodeOutput(t, result)["problems"].([]any)
	require.Equal(t, []any{
		"level 2: TACT cannot be spelled from CDEILORU",
		"level 2: LOUD is already used in a recent campaign",
		"level 3: ACTIVITY is already used in level 1",
	}, problems[:3])
	require.Len(t, problems, 2+10, "every answer of the copied level is a repeat")
	require.Nil(t, result.Campaign)
}

func TestSubmitAcceptsASavedMonthlyPack(t *testing.T) {
	request := workflows.CampaignRequest{
		Kind: campaign.KindMonthlyPack, Date: "2026-12-01", StartsAt: time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC),
	}

	result := executeTool(t, request, draftOf(validMonthlyLevels()), workflows.AgentToolSubmitCampaign,
		jsonArguments(t, map[string]string{"description": "Nine themed Wordflows that start gently and finish with real vocabulary."}))

	require.JSONEq(t, `{"accepted":true}`, result.Output)
	definition := result.Campaign.Definition
	require.Equal(t, "monthly-pack-2026-12", definition.ID)
	require.Equal(t, campaign.KindMonthlyPack, definition.Kind)
	require.Equal(t, "Monthly Pack · December 2026", definition.Title)
	require.False(t, definition.SingleAttempt)
	require.Nil(t, definition.EndsAt)
	require.Equal(t, campaign.UnlockPolicy{InitialLevels: 3, LevelsPerInterval: 3, Interval: 24 * time.Hour}, definition.Unlock)
	require.Len(t, result.Campaign.Levels, 9)
	for _, level := range result.Campaign.Levels {
		require.Len(t, level.Words, 10)
		require.Zero(t, level.TimeLimitSeconds)
		require.Zero(t, level.BasePoints)
		require.Zero(t, level.HintPrices)
	}
}

func TestSubmitFailsForARequestWithoutAValidWindow(t *testing.T) {
	request := dailyRequest()
	request.EndsAt = &request.StartsAt

	_, err := (&CampaignGenerator{}).ExecuteCampaignTool(context.Background(), workflows.ExecuteCampaignToolInput{
		Request: request, Draft: draftOf(validDailyLevels()),
		Call: workflows.AgentToolCall{CallID: "call", Name: workflows.AgentToolSubmitCampaign, Arguments: `{"description":"x"}`},
	})

	var applicationError *temporal.ApplicationError
	require.ErrorAs(t, err, &applicationError)
	require.Equal(t, "invalid_campaign_input", applicationError.Type())
	require.True(t, applicationError.NonRetryable())
}

func TestCheckLevelRejectsPastTensePadding(t *testing.T) {
	words, err := dictionary.Load()
	require.NoError(t, err)
	level := validDailyLevels()[1]
	level.Words[9] = "CURE"

	_, problems := checkLevel(words, level, nil)

	require.Equal(t, []string{"CURE and its past tense are both answers; drop one"}, problems)
}

func endOf(startsAt time.Time) *time.Time {
	endsAt := startsAt.Add(24 * time.Hour)
	return &endsAt
}

func validDailyLevels() []workflows.CampaignLevel {
	return []workflows.CampaignLevel{
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
			Words: []string{"ROLLBACK", "ROLL", "BACK", "BALL", "CALL", "ROCK", "LACK", "LOCK", "CORAL", "CAB"},
		},
	}
}

func validMonthlyLevels() []workflows.CampaignLevel {
	return append(validDailyLevels(),
		workflows.CampaignLevel{
			Title: "Plant Plan", Letters: "AGILNNPT",
			Words: []string{"PLANTING", "TAPING", "PLAIN", "PLANT", "PAINT", "ALIGN", "GIANT", "PLAN", "PANG", "TANG"},
		},
		workflows.CampaignLevel{
			Title: "After Dark", Letters: "DGHIIMNT",
			Words: []string{"MIDNIGHT", "MIGHT", "NIGHT", "TIMING", "TIDING", "THING", "MIND", "MINT", "HINT", "DING"},
		},
		workflows.CampaignLevel{
			Title: "Gather Round", Letters: "ADEGHRTU",
			Words: []string{"DAUGHTER", "GATHER", "THREAD", "TRUDGE", "GRATED", "EARTH", "HEARD", "TRADE", "DRAG", "RUDE"},
		},
		workflows.CampaignLevel{
			Title: "Stranger Danger", Letters: "AEGNRRST",
			Words: []string{"STRANGER", "RANGER", "GARNET", "ANGER", "GRANT", "STAGE", "RENT", "STAR", "EARN", "SNAG"},
		},
		workflows.CampaignLevel{
			Title: "Pocket Champion", Letters: "ACHIMNOP",
			Words: []string{"CHAMPION", "PINCH", "PIANO", "CHAIN", "POACH", "CHIN", "CHOP", "CAMP", "CHIP", "PAIN"},
		},
		workflows.CampaignLevel{
			Title: "Hold Steady", Letters: "DEHLORSU",
			Words: []string{"SHOULDER", "SHOULD", "HOLDER", "HURDLE", "SOLDER", "LOUDER", "HOUSE", "RULED", "HOLD", "SHED"},
		},
	)
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
			{"type":"function_call","id":"fc1","call_id":"call-9","name":"set_level","arguments":"{\"title\":\"T\"}","status":"completed"},
			{"type":"function_call","id":"fc2","call_id":"call-10","name":"suggest_anchors","arguments":"{}","status":"completed"}]}`)
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
		{CallID: "call-9", Name: "set_level", Arguments: `{"title":"T"}`},
		{CallID: "call-10", Name: "suggest_anchors", Arguments: `{}`},
	}, result.Calls)

	require.Equal(t, "required", body["tool_choice"])
	require.Equal(t, true, body["parallel_tool_calls"])
	require.Equal(t, false, body["store"])
	require.Len(t, body["tools"], 4)
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
