package activities

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"
	"unicode"

	"github.com/mjones/temporal-word-game/internal/campaign"
	"github.com/mjones/temporal-word-game/internal/game"
	"github.com/mjones/temporal-word-game/internal/workflows"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/responses"
	"go.temporal.io/sdk/temporal"
)

const (
	campaignToolName     = "create_wordflow_campaign"
	defaultCampaignModel = "gpt-5.4-mini"
	campaignToolAttempts = 5
	// OpenAIKeyEnvironment is required by GenerateWordflowCampaign.
	OpenAIKeyEnvironment   = "OPENAI_API_KEY"
	openAIModelEnvironment = "OPENAI_MODEL"
)

type responseCreator interface {
	New(context.Context, responses.ResponseNewParams, ...option.RequestOption) (*responses.Response, error)
}

type CampaignGenerator struct {
	Responses responseCreator
	Model     string
}

type generatedCampaign struct {
	Description string                   `json:"description"`
	Levels      []generatedWordflowLevel `json:"levels"`
}

type generatedWordflowLevel struct {
	Title   string   `json:"title"`
	Letters string   `json:"letters"`
	Words   []string `json:"words"`
}

func (a *CampaignGenerator) GenerateWordflowCampaign(ctx context.Context, input workflows.GenerateWordflowCampaignActivityInput) (workflows.WordflowCampaignWorkflowInput, error) {
	spec, err := campaignSpecFor(input.Kind)
	if err != nil {
		return workflows.WordflowCampaignWorkflowInput{}, temporal.NewNonRetryableApplicationError(err.Error(), "invalid_campaign_kind", nil)
	}

	creator := a.Responses
	if creator == nil {
		if strings.TrimSpace(os.Getenv(OpenAIKeyEnvironment)) == "" {
			return workflows.WordflowCampaignWorkflowInput{}, temporal.NewNonRetryableApplicationError(
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

	prompt := spec.prompt(input)
	params := responses.ResponseNewParams{
		Model:        model,
		Instructions: openai.String(campaignAgentInstructions),
		Input: responses.ResponseNewParamsInputUnion{
			OfString: openai.String(prompt),
		},
		MaxOutputTokens:   openai.Int(5000),
		ParallelToolCalls: openai.Bool(false),
		Store:             openai.Bool(false),
		Tools: []responses.ToolUnionParam{{
			OfFunction: &responses.FunctionToolParam{
				Name:        campaignToolName,
				Description: openai.String("Submit the complete Wordflow campaign and end the generation turn."),
				Parameters:  campaignToolSchema(spec.levelCount),
				Strict:      openai.Bool(true),
			},
		}},
		ToolChoice: responses.ResponseNewParamsToolChoiceUnion{
			OfFunctionTool: &responses.ToolChoiceFunctionParam{Name: campaignToolName},
		},
	}

	var history responses.ResponseInputParam
	var lastErr error
	for attempt := 1; attempt <= campaignToolAttempts; attempt++ {
		response, err := creator.New(ctx, params)
		if err != nil {
			return workflows.WordflowCampaignWorkflowInput{}, fmt.Errorf("generate %s campaign with OpenAI: %w", input.Kind, err)
		}

		generated, call, err := extractCampaignToolCall(response)
		if err == nil {
			if configured, buildErr := buildCampaign(spec, input, generated); buildErr == nil {
				return configured, nil
			} else {
				err = buildErr
			}
		}
		lastErr = err
		if call.CallID == "" || attempt == campaignToolAttempts {
			break
		}

		if len(history) == 0 {
			history = append(history, responses.ResponseInputItemParamOfMessage(prompt, responses.EasyInputMessageRoleUser))
		}
		history = append(history, responses.ResponseInputItemParamOfFunctionCall(call.Arguments, call.CallID, campaignToolName))
		toolOutput := responses.ResponseInputItemParamOfFunctionCallOutput(fmt.Sprintf(
			`{"accepted":false,"error":%q,"instruction":"Correct the rejected campaign and call create_wordflow_campaign again."}`,
			err.Error(),
		))
		toolOutput.OfFunctionCallOutput.CallID = openai.String(call.CallID)
		history = append(history, toolOutput)
		params.Input = responses.ResponseNewParamsInputUnion{OfInputItemList: history}
	}
	return workflows.WordflowCampaignWorkflowInput{}, fmt.Errorf("OpenAI did not produce a valid %s campaign after %d tool calls: %w", input.Kind, campaignToolAttempts, lastErr)
}

const campaignAgentInstructions = `You design Wordflow puzzles. The only way to finish your turn is to call the create_wordflow_campaign tool exactly once. Do not return prose or any other final response.`

// campaignSpec is everything that differs between the kinds of campaign the
// generator can produce. The tool schema and level validation are shared.
type campaignSpec struct {
	levelCount int
	prompt     func(workflows.GenerateWordflowCampaignActivityInput) string
	// level applies the kind's timing, scoring, and hint rules to a validated level.
	level      func(game.LevelDefinition) game.LevelDefinition
	definition func(input workflows.GenerateWordflowCampaignActivityInput, description string) campaign.Definition
}

func campaignSpecFor(kind campaign.Kind) (campaignSpec, error) {
	switch kind {
	case campaign.KindDailyChallenge:
		return dailyChallengeSpec, nil
	case campaign.KindMonthlyPack:
		return monthlyPackSpec, nil
	default:
		return campaignSpec{}, fmt.Errorf("unsupported campaign kind %q", kind)
	}
}

var dailyChallengeSpec = campaignSpec{
	levelCount: 3,
	prompt:     dailyChallengePrompt,
	level: func(level game.LevelDefinition) game.LevelDefinition {
		level.TimeLimitSeconds = 180
		level.BasePoints = 20
		level.HintPrices = game.HintPrices{Letter: 15, Brush: 30, Word: 45}
		return level
	},
	definition: func(input workflows.GenerateWordflowCampaignActivityInput, description string) campaign.Definition {
		return campaign.Definition{
			ID:            workflows.GeneratedCampaignID(campaign.KindDailyChallenge, input.Date),
			Kind:          campaign.KindDailyChallenge,
			SingleAttempt: true,
			Game:          wordflowGameSummary,
			Title:         "Daily Challenge · " + input.StartsAt.Format("January 2"),
			Description:   description,
			StartsAt:      &input.StartsAt,
			EndsAt:        input.EndsAt,
			Unlock:        campaign.UnlockPolicy{InitialLevels: 3},
		}
	},
}

// A monthly pack is a permanent campaign. It keeps the default scoring and
// has no time limit, so it reads as a relaxed counterpart to the daily challenge.
var monthlyPackSpec = campaignSpec{
	levelCount: 9,
	prompt:     monthlyPackPrompt,
	level:      func(level game.LevelDefinition) game.LevelDefinition { return level },
	definition: func(input workflows.GenerateWordflowCampaignActivityInput, description string) campaign.Definition {
		return campaign.Definition{
			ID:          workflows.GeneratedCampaignID(campaign.KindMonthlyPack, input.Date),
			Kind:        campaign.KindMonthlyPack,
			Game:        wordflowGameSummary,
			Title:       "Monthly Pack · " + input.StartsAt.Format("January 2006"),
			Description: description,
			StartsAt:    &input.StartsAt,
			Unlock:      campaign.UnlockPolicy{InitialLevels: 3, LevelsPerInterval: 3, Interval: 24 * time.Hour},
		}
	},
}

var wordflowGameSummary = campaign.GameSummary{
	ID: campaign.GameWordflow, Title: "Wordflow",
	Description: "Resolve connected words from a shared letter queue.",
}

const levelRules = `For every level:
- provide exactly eight uppercase letters and exactly ten unique English answers
- every answer must be at least three letters and spellable from the letter multiset without using a letter too many times
- choose a familiar eight-letter anchor word first, use exactly its letters for the letters field in any order, and include that anchor in the answers; the anchor must be a common standalone word, not a plural, conjugation, obscure derivative, or invented compound
- the letters field is only a letter bank and need not itself spell a word; every entry in words, including the eight-letter anchor, must be a real familiar word
- make the words interconnected enough to form one connected crossword
- use familiar modern dictionary words; never use proper nouns, abbreviations, offensive terms, archaic words, or dubious comparative forms
- do not pad the answer list with both a base word and its simple plural or past tense, and do not reuse an answer in another level
- give the level a short, energetic title

Before calling the tool, silently audit every answer character by character against its eight-letter multiset and replace anything questionable. The tool arguments are rejected automatically if even one answer overuses a letter or the crossword cannot be connected.`

func exclusionRule(excluded []string, scope string) string {
	if len(excluded) == 0 {
		return "There are no recent answers to exclude."
	}
	return "Do not use any of these answers from " + scope + ": " + strings.Join(excluded, ", ") + "."
}

func dailyChallengePrompt(input workflows.GenerateWordflowCampaignActivityInput) string {
	return fmt.Sprintf(`Create the Daily Wordflow Challenge for %s.

The tool call must define exactly three especially difficult but fair levels. %s

%s

The campaign is one attempt only. All three levels unlock immediately but must be played sequentially. Each level has a hard 180-second limit, awards 20 base points, and prices letter, brush, and word hints at 15, 30, and 45 points. Its active window is %s through %s. Mention the high difficulty, time pressure, richer rewards, premium hints, and single attempt in one concise campaign description.

End your turn by calling create_wordflow_campaign exactly once.`,
		input.Date,
		levelRules,
		exclusionRule(input.ExcludedWords, "the previous ten daily challenges"),
		input.StartsAt.Format("2006-01-02 15:04 MST"),
		input.EndsAt.Format("2006-01-02 15:04 MST"),
	)
}

func monthlyPackPrompt(input workflows.GenerateWordflowCampaignActivityInput) string {
	return fmt.Sprintf(`Create the Wordflow Monthly Pack for %s.

The tool call must define exactly nine levels that share one fun, family-friendly theme you choose for the month. Order them from approachable to challenging, so the first three suit a newcomer and the last three need real vocabulary. %s

%s

The pack is permanent: players can work through it at their own pace with no time limit and the standard scoring. Mention the theme and the progression in one concise campaign description.

End your turn by calling create_wordflow_campaign exactly once.`,
		input.StartsAt.Format("January 2006"),
		levelRules,
		exclusionRule(input.ExcludedWords, "the previous monthly packs"),
	)
}

func campaignToolSchema(levelCount int) map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"description": map[string]any{
				"type":      "string",
				"minLength": 40,
				"maxLength": 220,
			},
			"levels": map[string]any{
				"type":     "array",
				"minItems": levelCount,
				"maxItems": levelCount,
				"items": map[string]any{
					"type":                 "object",
					"additionalProperties": false,
					"properties": map[string]any{
						"title": map[string]any{
							"type":      "string",
							"minLength": 3,
							"maxLength": 40,
						},
						"letters": map[string]any{
							"type":      "string",
							"pattern":   "^[A-Z]{8}$",
							"minLength": 8,
							"maxLength": 8,
						},
						"words": map[string]any{
							"type":     "array",
							"minItems": 10,
							"maxItems": 10,
							"items": map[string]any{
								"type":      "string",
								"pattern":   "^[A-Z]{3,8}$",
								"minLength": 3,
								"maxLength": 8,
							},
						},
					},
					"required": []string{"title", "letters", "words"},
				},
			},
		},
		"required": []string{"description", "levels"},
	}
}

type campaignToolCall struct {
	CallID    string
	Arguments string
}

func extractCampaignToolCall(response *responses.Response) (generatedCampaign, campaignToolCall, error) {
	if response == nil {
		return generatedCampaign{}, campaignToolCall{}, fmt.Errorf("OpenAI returned no response")
	}

	var call campaignToolCall
	for _, item := range response.Output {
		switch item.Type {
		case "function_call":
			functionCall := item.AsFunctionCall()
			if functionCall.Name != campaignToolName {
				return generatedCampaign{}, call, fmt.Errorf("OpenAI called unexpected tool %q", functionCall.Name)
			}
			if call.Arguments != "" {
				return generatedCampaign{}, call, fmt.Errorf("OpenAI called %s more than once", campaignToolName)
			}
			call = campaignToolCall{CallID: functionCall.CallID, Arguments: functionCall.Arguments}
		case "message":
			return generatedCampaign{}, call, fmt.Errorf("OpenAI returned a message instead of ending with %s", campaignToolName)
		}
	}
	if call.Arguments == "" {
		return generatedCampaign{}, call, fmt.Errorf("OpenAI did not call %s", campaignToolName)
	}

	var generated generatedCampaign
	if err := json.Unmarshal([]byte(call.Arguments), &generated); err != nil {
		return generatedCampaign{}, call, fmt.Errorf("decode %s arguments: %w", campaignToolName, err)
	}
	return generated, call, nil
}

func buildCampaign(spec campaignSpec, input workflows.GenerateWordflowCampaignActivityInput, generated generatedCampaign) (workflows.WordflowCampaignWorkflowInput, error) {
	if input.Date == "" || input.StartsAt.IsZero() || (input.EndsAt != nil && !input.EndsAt.After(input.StartsAt)) {
		return workflows.WordflowCampaignWorkflowInput{}, temporal.NewNonRetryableApplicationError(
			"campaign date and a valid active window are required", "invalid_campaign_input", nil,
		)
	}
	generated.Description = strings.TrimSpace(generated.Description)
	if generated.Description == "" {
		return workflows.WordflowCampaignWorkflowInput{}, fmt.Errorf("campaign description is required")
	}
	if len(generated.Levels) != spec.levelCount {
		return workflows.WordflowCampaignWorkflowInput{}, fmt.Errorf("campaign must contain exactly %d levels", spec.levelCount)
	}

	levels := make([]game.Puzzle, 0, len(generated.Levels))
	allAnswers := make(map[string]bool, len(input.ExcludedWords)+10*spec.levelCount)
	for _, answer := range input.ExcludedWords {
		allAnswers[strings.ToUpper(strings.TrimSpace(answer))] = true
	}
	for index, level := range generated.Levels {
		definition, err := validateGeneratedWordflowLevel(index+1, level, allAnswers)
		if err != nil {
			return workflows.WordflowCampaignWorkflowInput{}, err
		}
		puzzle, err := game.BuildPuzzle(index+1, spec.level(definition))
		if err != nil {
			return workflows.WordflowCampaignWorkflowInput{}, fmt.Errorf("build generated campaign: %w", err)
		}
		levels = append(levels, puzzle)
	}

	return workflows.WordflowCampaignWorkflowInput{
		Definition: spec.definition(input, generated.Description),
		Levels:     levels,
	}, nil
}

func validateGeneratedWordflowLevel(levelNumber int, generated generatedWordflowLevel, allAnswers map[string]bool) (game.LevelDefinition, error) {
	title := strings.TrimSpace(generated.Title)
	letters := strings.ToUpper(strings.TrimSpace(generated.Letters))
	if title == "" {
		return game.LevelDefinition{}, fmt.Errorf("level %d title is required", levelNumber)
	}
	if len([]rune(letters)) != game.MaxWordflowLetters || !asciiLettersOnly(letters) {
		return game.LevelDefinition{}, fmt.Errorf("level %d must have exactly %d uppercase letters", levelNumber, game.MaxWordflowLetters)
	}
	if len(generated.Words) != 10 {
		return game.LevelDefinition{}, fmt.Errorf("level %d must have exactly 10 words", levelNumber)
	}

	words := make([]string, 0, len(generated.Words))
	levelAnswers := map[string]bool{}
	usesFullQueue := false
	for _, value := range generated.Words {
		answer := strings.ToUpper(strings.TrimSpace(value))
		length := len([]rune(answer))
		switch {
		case length < 3 || length > game.MaxWordflowLetters || !asciiLettersOnly(answer):
			return game.LevelDefinition{}, fmt.Errorf("level %d has invalid answer %q", levelNumber, value)
		case levelAnswers[answer]:
			return game.LevelDefinition{}, fmt.Errorf("level %d repeats answer %q", levelNumber, answer)
		case allAnswers[answer]:
			return game.LevelDefinition{}, fmt.Errorf("campaign reuses answer %q", answer)
		case !spellsFromLetters(answer, letters):
			return game.LevelDefinition{}, fmt.Errorf("level %d answer %q cannot be spelled from %q", levelNumber, answer, letters)
		}
		levelAnswers[answer] = true
		allAnswers[answer] = true
		usesFullQueue = usesFullQueue || length == game.MaxWordflowLetters
		words = append(words, answer)
	}
	if !usesFullQueue {
		return game.LevelDefinition{}, fmt.Errorf("level %d has no answer using all %d letters", levelNumber, game.MaxWordflowLetters)
	}
	for answer := range levelAnswers {
		if levelAnswers[answer+"S"] || levelAnswers[answer+"ES"] {
			return game.LevelDefinition{}, fmt.Errorf("level %d pads its answers with a plural form of %q", levelNumber, answer)
		}
		pastTense := answer + "ED"
		if strings.HasSuffix(answer, "E") {
			pastTense = answer + "D"
		}
		if levelAnswers[pastTense] {
			return game.LevelDefinition{}, fmt.Errorf("level %d pads its answers with a past-tense form of %q", levelNumber, answer)
		}
	}

	return game.LevelDefinition{Title: title, Letters: letters, Words: words}, nil
}

func asciiLettersOnly(value string) bool {
	for _, letter := range value {
		if letter > unicode.MaxASCII || letter < 'A' || letter > 'Z' {
			return false
		}
	}
	return value != ""
}

func spellsFromLetters(word, letters string) bool {
	available := map[rune]int{}
	for _, letter := range letters {
		available[letter]++
	}
	for _, letter := range word {
		available[letter]--
		if available[letter] < 0 {
			return false
		}
	}
	return true
}
