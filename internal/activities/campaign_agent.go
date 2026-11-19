package activities

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

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

	switch input.Call.Name {
	case workflows.AgentToolCheckWords, workflows.AgentToolCheckLevel:
		var level generatedWordflowLevel
		if err := json.Unmarshal([]byte(input.Call.Arguments), &level); err != nil {
			return toolRejected(fmt.Errorf("invalid arguments: %w", err)), nil
		}
		if err := checkLevel(input.Request, level, input.Call.Name == workflows.AgentToolCheckLevel); err != nil {
			return toolRejected(err), nil
		}
		return toolOutput(map[string]any{"ok": true}), nil

	case workflows.AgentToolSubmitCampaign:
		var generated generatedCampaign
		if err := json.Unmarshal([]byte(input.Call.Arguments), &generated); err != nil {
			return toolRejected(fmt.Errorf("invalid arguments: %w", err)), nil
		}
		configured, err := buildCampaign(spec, input.Request, generated)
		if err != nil {
			return toolRejected(err), nil
		}
		result := toolOutput(map[string]any{"accepted": true})
		result.Campaign = &configured
		return result, nil

	default:
		return toolRejected(fmt.Errorf("unknown tool %q", input.Call.Name)), nil
	}
}

// checkLevel applies the per-level rules, optionally including whether the
// crossword layout connects, without the cross-level rules a submission adds.
func checkLevel(request workflows.CampaignRequest, level generatedWordflowLevel, includeLayout bool) error {
	definition, err := validateGeneratedWordflowLevel(1, level, excludedAnswers(request))
	if err != nil {
		return err
	}
	if includeLayout {
		if _, err := game.BuildPuzzle(1, definition); err != nil {
			return err
		}
	}
	return nil
}

func toolRejected(err error) workflows.ExecuteCampaignToolResult {
	return toolOutput(map[string]any{"ok": false, "accepted": false, "error": err.Error()})
}

func toolOutput(output map[string]any) workflows.ExecuteCampaignToolResult {
	encoded, _ := json.Marshal(output) // a map of strings and booleans always encodes
	return workflows.ExecuteCampaignToolResult{Output: string(encoded)}
}

func campaignTools(levelCount int) []responses.ToolUnionParam {
	return []responses.ToolUnionParam{
		functionTool(workflows.AgentToolCheckWords,
			"Quickly check one level's letter bank and ten answers: letters, spelling from the bank, repeats, padding, and answers used recently. Does not test the crossword layout.",
			levelSchema()),
		functionTool(workflows.AgentToolCheckLevel,
			"Fully check one level, including whether its answers form one connected crossword.",
			levelSchema()),
		functionTool(workflows.AgentToolSubmitCampaign,
			"Submit the complete campaign for final validation. Accepted campaigns end the task; rejected ones return the reason.",
			campaignSchema(levelCount)),
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

func levelSchema() map[string]any {
	return map[string]any{
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
	}
}

func campaignSchema(levelCount int) map[string]any {
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
				"items":    levelSchema(),
			},
		},
		"required": []string{"description", "levels"},
	}
}
