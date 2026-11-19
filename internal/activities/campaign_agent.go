package activities

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"os"
	"sort"
	"strings"

	"github.com/mjones/temporal-word-game/internal/dictionary"
	"github.com/mjones/temporal-word-game/internal/game"
	"github.com/mjones/temporal-word-game/internal/workflows"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/packages/param"
	"github.com/openai/openai-go/v3/responses"
	"go.temporal.io/sdk/temporal"
)

const (
	defaultCampaignModel = "gpt-5.4-mini"
	// OpenAIKeyEnvironment is required by RunCampaignAgentTurn.
	OpenAIKeyEnvironment   = "OPENAI_API_KEY"
	openAIModelEnvironment = "OPENAI_MODEL"
)

type responseCreator interface {
	New(context.Context, responses.ResponseNewParams, ...option.RequestOption) (*responses.Response, error)
}

// CampaignGenerator holds the Activities of the campaign-generating agent: one
// that asks the model for its next tool calls, and one that runs a tool call.
type CampaignGenerator struct {
	Responses responseCreator
	Model     string
}

// RunCampaignAgentTurn sends the conversation so far to the model and returns
// the tool calls it makes. It runs no tools; the Workflow does that, so each
// call appears in the Workflow's history.
func (a *CampaignGenerator) RunCampaignAgentTurn(ctx context.Context, input workflows.RunCampaignAgentTurnInput) (workflows.RunCampaignAgentTurnResult, error) {
	spec, err := campaignSpecFor(input.Request.Kind)
	if err != nil {
		return workflows.RunCampaignAgentTurnResult{}, temporal.NewNonRetryableApplicationError(err.Error(), "invalid_campaign_kind", nil)
	}

	creator := a.Responses
	if creator == nil {
		if strings.TrimSpace(os.Getenv(OpenAIKeyEnvironment)) == "" {
			return workflows.RunCampaignAgentTurnResult{}, temporal.NewNonRetryableApplicationError(
				OpenAIKeyEnvironment+" is required to generate a campaign",
				"openai_not_configured",
				nil,
			)
		}
		client := openai.NewClient()
		creator = &client.Responses
	}

	model := strings.TrimSpace(a.Model)
	if model == "" {
		model = strings.TrimSpace(os.Getenv(openAIModelEnvironment))
	}
	if model == "" {
		model = defaultCampaignModel
	}

	response, err := creator.New(ctx, responses.ResponseNewParams{
		Model:        model,
		Instructions: openai.String(campaignAgentInstructions),
		Input: responses.ResponseNewParamsInputUnion{
			OfInputItemList: conversation(spec.prompt(input.Request), input.Items),
		},
		MaxOutputTokens:   openai.Int(5000),
		ParallelToolCalls: openai.Bool(true),
		Store:             openai.Bool(false),
		Tools:             campaignTools(spec.levelCount),
		ToolChoice: responses.ResponseNewParamsToolChoiceUnion{
			OfToolChoiceMode: param.NewOpt(responses.ToolChoiceOptionsRequired),
		},
	})
	if err != nil {
		return workflows.RunCampaignAgentTurnResult{}, fmt.Errorf("ask OpenAI for the next %s campaign step: %w", input.Request.Kind, err)
	}
	return workflows.RunCampaignAgentTurnResult{Calls: toolCalls(response)}, nil
}

func conversation(prompt string, items []workflows.AgentItem) responses.ResponseInputParam {
	input := responses.ResponseInputParam{responses.ResponseInputItemParamOfMessage(prompt, responses.EasyInputMessageRoleUser)}
	for _, item := range items {
		switch item.Type {
		case workflows.AgentItemMessage:
			input = append(input, responses.ResponseInputItemParamOfMessage(item.Text, responses.EasyInputMessageRoleUser))
		case workflows.AgentItemToolCall:
			input = append(input, responses.ResponseInputItemParamOfFunctionCall(item.Arguments, item.CallID, item.Name))
		case workflows.AgentItemToolResult:
			output := responses.ResponseInputItemParamOfFunctionCallOutput(item.Text)
			output.OfFunctionCallOutput.CallID = openai.String(item.CallID)
			input = append(input, output)
		}
	}
	return input
}

func toolCalls(response *responses.Response) []workflows.AgentToolCall {
	var calls []workflows.AgentToolCall
	for _, item := range response.Output {
		if item.Type != "function_call" {
			continue
		}
		call := item.AsFunctionCall()
		calls = append(calls, workflows.AgentToolCall{CallID: call.CallID, Name: call.Name, Arguments: call.Arguments})
	}
	return calls
}

// ExecuteCampaignTool runs one tool call. A bad argument or a rejected level or
// campaign is the agent's mistake, so it comes back as the tool's output for the
// agent to correct. The Activity fails only for a problem the agent cannot fix.
func (a *CampaignGenerator) ExecuteCampaignTool(_ context.Context, input workflows.ExecuteCampaignToolInput) (workflows.ExecuteCampaignToolResult, error) {
	spec, err := campaignSpecFor(input.Request.Kind)
	if err != nil {
		return workflows.ExecuteCampaignToolResult{}, temporal.NewNonRetryableApplicationError(err.Error(), "invalid_campaign_kind", nil)
	}
	words, err := dictionary.Load()
	if err != nil {
		return workflows.ExecuteCampaignToolResult{}, temporal.NewNonRetryableApplicationError(err.Error(), "dictionary_unavailable", err)
	}

	switch input.Call.Name {
	case workflows.AgentToolSuggestAnchors:
		return toolOutput(map[string]any{
			"anchors": suggestAnchors(words, takenAnswers(input.Request, input.Draft, 0), input.Draft),
		}), nil

	case workflows.AgentToolFindWords:
		var arguments struct {
			Letters string `json:"letters"`
		}
		if err := json.Unmarshal([]byte(input.Call.Arguments), &arguments); err != nil {
			return toolRejected(fmt.Errorf("invalid arguments: %w", err)), nil
		}
		letters := strings.ToUpper(strings.TrimSpace(arguments.Letters))
		if len(letters) != game.MaxWordflowLetters || !isUppercaseLetters(letters) {
			return toolRejected(fmt.Errorf("letters must be exactly %d letters A-Z", game.MaxWordflowLetters)), nil
		}
		return toolOutput(findWords(words, letters, input.Request, input.Draft)), nil

	case workflows.AgentToolSetLevel:
		var arguments struct {
			Level int `json:"level"`
			workflows.CampaignLevel
		}
		if err := json.Unmarshal([]byte(input.Call.Arguments), &arguments); err != nil {
			return toolRejected(fmt.Errorf("invalid arguments: %w", err)), nil
		}
		if arguments.Level < 1 || arguments.Level > spec.levelCount {
			return toolRejected(fmt.Errorf("level must be 1 to %d", spec.levelCount)), nil
		}
		level, problems := checkLevel(words, arguments.CampaignLevel, takenAnswers(input.Request, input.Draft, arguments.Level))
		if len(problems) > 0 {
			return toolOutput(map[string]any{"ok": false, "level": arguments.Level, "problems": problems}), nil
		}
		draft := workflows.CampaignDraft{arguments.Level: level}
		for number, saved := range input.Draft {
			if number != arguments.Level {
				draft[number] = saved
			}
		}
		result := toolOutput(map[string]any{
			"ok": true, "level": arguments.Level,
			"saved_levels": savedLevels(draft), "levels_still_needed": missingLevels(spec, draft),
		})
		result.SavedLevel = &workflows.SavedCampaignLevel{Number: arguments.Level, Level: level}
		return result, nil

	case workflows.AgentToolSubmitCampaign:
		var arguments struct {
			Description string `json:"description"`
		}
		if err := json.Unmarshal([]byte(input.Call.Arguments), &arguments); err != nil {
			return toolRejected(fmt.Errorf("invalid arguments: %w", err)), nil
		}
		configured, problems, err := buildCampaign(words, spec, input.Request, input.Draft, arguments.Description)
		if err != nil {
			return workflows.ExecuteCampaignToolResult{}, err
		}
		if len(problems) > 0 {
			return toolOutput(map[string]any{"accepted": false, "problems": problems}), nil
		}
		result := toolOutput(map[string]any{"accepted": true})
		result.Campaign = &configured
		return result, nil

	default:
		return toolRejected(fmt.Errorf("unknown tool %q", input.Call.Name)), nil
	}
}

// findWords lists the common words a letter bank can spell, longest first.
// Answers from recent campaigns are left out; answers a saved level already
// uses stay in the list and are named, because the agent may be replacing that level.
func findWords(words *dictionary.Dictionary, letters string, request workflows.CampaignRequest, draft workflows.CampaignDraft) map[string]any {
	recent := takenAnswers(request, nil, 0)
	usedBySaved := map[string]int{}
	for number, level := range draft {
		for _, answer := range level.Words {
			usedBySaved[answer] = number
		}
	}

	found := []string{}
	usedByLevel := map[string]int{}
	for _, word := range words.CommonWordsFrom(letters, 3) {
		if recent[word] != "" {
			continue
		}
		found = append(found, word)
		if number, ok := usedBySaved[word]; ok {
			usedByLevel[word] = number
		}
	}
	sortLongestFirst(found)
	return map[string]any{"letters": letters, "words": found, "used_by_saved_levels": usedByLevel}
}

func sortLongestFirst(words []string) {
	sort.Slice(words, func(i, j int) bool {
		if len(words[i]) != len(words[j]) {
			return len(words[i]) > len(words[j])
		}
		return words[i] < words[j]
	})
}

const (
	anchorSuggestions = 10
	// anchorCandidates is how many qualifying anchors are drawn before ranking.
	// Drawing at random keeps suggestions varied from run to run.
	anchorCandidates = 40
	// minAnchorWords leaves room to choose ten answers that also connect.
	minAnchorWords = 20
)

type anchorSuggestion struct {
	Word            string `json:"word"`
	CommonWords     int    `json:"common_words"`
	sharedWithSaved int
}

// suggestAnchors returns common eight-letter words that make good letter banks:
// not a plural or verb form, not already used, with plenty of common words to
// choose from. Anchors that share the fewest letters with saved levels come
// first, so the campaign's levels don't keep producing the same short words.
func suggestAnchors(words *dictionary.Dictionary, taken map[string]string, draft workflows.CampaignDraft) []anchorSuggestion {
	anchors := words.CommonWordsOfLength(game.MaxWordflowLetters)
	rand.Shuffle(len(anchors), func(i, j int) { anchors[i], anchors[j] = anchors[j], anchors[i] })

	var candidates []anchorSuggestion
	for _, anchor := range anchors {
		if len(candidates) == anchorCandidates {
			break
		}
		if taken[anchor] != "" || isInflection(words, anchor) {
			continue
		}
		available := 0
		for _, word := range words.CommonWordsFrom(anchor, 3) {
			if taken[word] == "" {
				available++
			}
		}
		if available < minAnchorWords {
			continue
		}
		candidates = append(candidates, anchorSuggestion{Word: anchor, CommonWords: available, sharedWithSaved: mostSharedLetters(anchor, draft)})
	}

	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].sharedWithSaved != candidates[j].sharedWithSaved {
			return candidates[i].sharedWithSaved < candidates[j].sharedWithSaved
		}
		return candidates[i].CommonWords > candidates[j].CommonWords
	})
	return candidates[:min(anchorSuggestions, len(candidates))]
}

// isInflection reports whether word looks like a plural or verb form of a
// common word: CATS, BOXES, BAKED, STOPPED, BAKING, STOPPING.
func isInflection(words *dictionary.Dictionary, word string) bool {
	for _, suffix := range []string{"S", "ES", "ED", "ING"} {
		stem, ok := strings.CutSuffix(word, suffix)
		if !ok || stem == "" {
			continue
		}
		if words.IsCommon(stem) || words.IsCommon(stem+"E") {
			return true
		}
		doubled := len(stem) > 1 && stem[len(stem)-1] == stem[len(stem)-2]
		if doubled && words.IsCommon(stem[:len(stem)-1]) {
			return true
		}
	}
	return false
}

// mostSharedLetters returns how many of anchor's letters, counted with
// repeats, it shares with the saved level it overlaps most.
func mostSharedLetters(anchor string, draft workflows.CampaignDraft) int {
	most := 0
	for _, level := range draft {
		var available [26]int
		for _, letter := range level.Letters {
			available[letter-'A']++
		}
		shared := 0
		for _, letter := range anchor {
			if available[letter-'A'] > 0 {
				available[letter-'A']--
				shared++
			}
		}
		most = max(most, shared)
	}
	return most
}

func toolRejected(err error) workflows.ExecuteCampaignToolResult {
	return toolOutput(map[string]any{"ok": false, "error": err.Error()})
}

func toolOutput(output map[string]any) workflows.ExecuteCampaignToolResult {
	encoded, err := json.Marshal(output)
	if err != nil {
		// Every output is built from strings, numbers, and slices of them.
		panic(fmt.Sprintf("encode tool output: %v", err))
	}
	return workflows.ExecuteCampaignToolResult{Output: string(encoded)}
}

func campaignTools(levelCount int) []responses.ToolUnionParam {
	return []responses.ToolUnionParam{
		functionTool(workflows.AgentToolSuggestAnchors,
			"List common eight-letter anchor words that make good letter banks, with how many common words each can spell. Anchors that share the fewest letters with your saved levels come first. Each call returns a fresh selection.",
			objectSchema(map[string]any{})),
		functionTool(workflows.AgentToolFindWords,
			"List every common word of three or more letters that a letter bank can spell, longest first. Leaves out answers from recent campaigns and names the saved level that already uses a word.",
			objectSchema(map[string]any{"letters": lettersSchema()})),
		functionTool(workflows.AgentToolSetLevel,
			"Check one level and save it to the campaign draft if it passes. Lists every problem when it does not. Setting a level number again replaces that level.",
			objectSchema(map[string]any{
				"level":   map[string]any{"type": "integer", "minimum": 1, "maximum": levelCount},
				"title":   map[string]any{"type": "string", "minLength": 3, "maxLength": 40},
				"letters": lettersSchema(),
				"words": map[string]any{
					"type":     "array",
					"minItems": levelAnswerCount,
					"maxItems": levelAnswerCount,
					"items":    map[string]any{"type": "string", "pattern": "^[A-Z]{3,8}$", "minLength": 3, "maxLength": 8},
				},
			})),
		functionTool(workflows.AgentToolSubmitCampaign,
			fmt.Sprintf("Submit the campaign once all %d levels are saved. An accepted campaign ends the task; a rejected one lists every problem.", levelCount),
			objectSchema(map[string]any{
				"description": map[string]any{"type": "string", "minLength": 40, "maxLength": 220},
			})),
	}
}

func functionTool(name, description string, parameters map[string]any) responses.ToolUnionParam {
	return responses.ToolUnionParam{OfFunction: &responses.FunctionToolParam{
		Name:        name,
		Description: openai.String(description),
		Parameters:  parameters,
		Strict:      openai.Bool(true),
	}}
}

// objectSchema is a strict-mode object: every property required, no others allowed.
func objectSchema(properties map[string]any) map[string]any {
	required := make([]string, 0, len(properties))
	for name := range properties {
		required = append(required, name)
	}
	sort.Strings(required)
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties":           properties,
		"required":             required,
	}
}

func lettersSchema() map[string]any {
	return map[string]any{"type": "string", "pattern": "^[A-Z]{8}$", "minLength": 8, "maxLength": 8}
}
