package workflows

import (
	"fmt"
	"time"

	"github.com/mjones/temporal-word-game/internal/campaign"

	"go.temporal.io/api/enums/v1"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"
)

const (
	dateLayout  = "2006-01-02"
	monthLayout = "2006-01"

	dailyChallengeHistory = 10
	monthlyPackHistory    = 3

	// A run has to finish inside its Schedule's 30-minute execution timeout.
	dailyChallengeMaxTurns = 30
	monthlyPackMaxTurns    = 40
)

// CampaignGeneratorWorkflowInput selects which kind of campaign one run creates.
// A Schedule starts one run per period.
type CampaignGeneratorWorkflowInput struct {
	Kind campaign.Kind `json:"kind"`
}

// GeneratedCampaign is what a later run needs to know about an earlier one.
type GeneratedCampaign struct {
	CampaignID string   `json:"campaignId"`
	Answers    []string `json:"answers"`
}

// CampaignGeneratorResult is handed to the Schedule's next run as its last
// completion result, so each run can avoid the previous runs' answers.
type CampaignGeneratorResult struct {
	Recent []GeneratedCampaign `json:"recent"`
}

// CampaignRequest describes the campaign one generator run is asked to create.
type CampaignRequest struct {
	Kind          campaign.Kind `json:"kind"`
	Date          string        `json:"date"`
	StartsAt      time.Time     `json:"startsAt"`
	EndsAt        *time.Time    `json:"endsAt,omitempty"`
	ExcludedWords []string      `json:"excludedWords,omitempty"`
}

// The tools the generating agent can call. Submitting is the only way to finish.
const (
	AgentToolCheckWords     = "check_words"
	AgentToolCheckLevel     = "check_level"
	AgentToolSubmitCampaign = "submit_campaign"
)

type AgentItemType string

const (
	AgentItemMessage    AgentItemType = "message"
	AgentItemToolCall   AgentItemType = "tool_call"
	AgentItemToolResult AgentItemType = "tool_result"
)

// AgentItem is one entry of the conversation after the opening prompt. The
// Workflow owns the conversation, so every turn and tool call is in its history.
type AgentItem struct {
	Type      AgentItemType `json:"type"`
	Text      string        `json:"text,omitempty"`      // message text or tool output
	CallID    string        `json:"callId,omitempty"`    // tool call and result
	Name      string        `json:"name,omitempty"`      // tool call
	Arguments string        `json:"arguments,omitempty"` // tool call
}

type AgentToolCall struct {
	CallID    string `json:"callId"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type RunCampaignAgentTurnInput struct {
	Request CampaignRequest `json:"request"`
	Items   []AgentItem     `json:"items,omitempty"`
}

type RunCampaignAgentTurnResult struct {
	Calls []AgentToolCall `json:"calls,omitempty"`
}

type ExecuteCampaignToolInput struct {
	Request CampaignRequest `json:"request"`
	Call    AgentToolCall   `json:"call"`
}

// ExecuteCampaignToolResult always carries an Output for the agent, including
// when a check or submission is rejected. Campaign is set only when a
// submission passed full validation.
type ExecuteCampaignToolResult struct {
	Output   string                         `json:"output"`
	Campaign *WordflowCampaignWorkflowInput `json:"campaign,omitempty"`
}

// GeneratedCampaignID names the campaign a run of this kind creates for the
// period that contains date (YYYY-MM-DD).
func GeneratedCampaignID(kind campaign.Kind, date string) string {
	if kind == campaign.KindMonthlyPack {
		return string(kind) + "-" + date[:len(monthLayout)]
	}
	return string(kind) + "-" + date
}

// WordflowCampaignGeneratorWorkflow runs an agent loop that designs one
// campaign, then starts it. Each model turn and each tool call is an Activity.
// A rejected check or submission goes back to the agent as a tool result, not as
// a failure; the run fails only when the agent uses up its turns. Daily
// challenges are single-day campaigns; monthly packs are permanent campaigns
// that add new levels to the catalog.
func WordflowCampaignGeneratorWorkflow(ctx workflow.Context, input CampaignGeneratorWorkflowInput) (CampaignGeneratorResult, error) {
	period, err := generatedCampaignPeriod(input.Kind, workflow.Now(ctx))
	if err != nil {
		return CampaignGeneratorResult{}, temporal.NewNonRetryableApplicationError(err.Error(), "invalid_campaign_kind", err)
	}

	var previous CampaignGeneratorResult
	if workflow.HasLastCompletionResult(ctx) {
		if err := workflow.GetLastCompletionResult(ctx, &previous); err != nil {
			return CampaignGeneratorResult{}, fmt.Errorf("read previous generator result: %w", err)
		}
	}

	request := CampaignRequest{
		Kind: input.Kind, Date: period.date, StartsAt: period.startsAt, EndsAt: period.endsAt,
		ExcludedWords: recentAnswers(previous.Recent),
	}
	campaignInput, err := runCampaignAgent(ctx, request, period.maxTurns)
	if err != nil {
		return CampaignGeneratorResult{}, err
	}

	expectedCampaignID := GeneratedCampaignID(input.Kind, period.date)
	if campaignInput.Definition.ID != expectedCampaignID {
		return CampaignGeneratorResult{}, temporal.NewNonRetryableApplicationError(
			fmt.Sprintf("generated campaign ID %q does not match %q", campaignInput.Definition.ID, expectedCampaignID),
			"invalid_generated_campaign",
			nil,
		)
	}

	childCtx := workflow.WithChildOptions(ctx, workflow.ChildWorkflowOptions{
		WorkflowID:            WordflowCampaignWorkflowID(campaignInput.Definition.ID),
		WorkflowIDReusePolicy: enums.WORKFLOW_ID_REUSE_POLICY_ALLOW_DUPLICATE_FAILED_ONLY,
		ParentClosePolicy:     enums.PARENT_CLOSE_POLICY_ABANDON,
	})
	child := workflow.ExecuteChildWorkflow(childCtx, WordflowCampaignWorkflowName, campaignInput)
	if err := child.GetChildWorkflowExecution().Get(ctx, nil); err != nil {
		return CampaignGeneratorResult{}, fmt.Errorf("start %s campaign: %w", input.Kind, err)
	}

	return CampaignGeneratorResult{Recent: appendGenerated(previous.Recent, campaignInput, period.history)}, nil
}

// runCampaignAgent loops model turns and tool calls until the agent submits a
// campaign that passes full validation, or maxTurns turns have been used.
func runCampaignAgent(ctx workflow.Context, request CampaignRequest, maxTurns int) (WordflowCampaignWorkflowInput, error) {
	modelCtx := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		StartToCloseTimeout: 2 * time.Minute,
		RetryPolicy: &temporal.RetryPolicy{
			InitialInterval:    5 * time.Second,
			BackoffCoefficient: 2,
			MaximumInterval:    time.Minute,
			MaximumAttempts:    5,
		},
	})
	toolCtx := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		StartToCloseTimeout: time.Minute,
		RetryPolicy:         &temporal.RetryPolicy{MaximumAttempts: 3},
	})
	logger := workflow.GetLogger(ctx)

	var items []AgentItem
	lastRejection := "the agent never submitted a campaign"
	for turn := 1; turn <= maxTurns; turn++ {
		var turnResult RunCampaignAgentTurnResult
		if err := workflow.ExecuteActivity(modelCtx, ActivityRunCampaignAgentTurn, RunCampaignAgentTurnInput{
			Request: request, Items: items,
		}).Get(ctx, &turnResult); err != nil {
			return WordflowCampaignWorkflowInput{}, fmt.Errorf("%s agent turn %d: %w", request.Kind, turn, err)
		}
		if len(turnResult.Calls) == 0 {
			items = append(items, AgentItem{Type: AgentItemMessage, Text: "Respond only by calling a tool. Finish by calling " + AgentToolSubmitCampaign + "."})
			continue
		}

		results := make([]workflow.Future, len(turnResult.Calls))
		for index, call := range turnResult.Calls {
			results[index] = workflow.ExecuteActivity(toolCtx, ActivityExecuteCampaignTool, ExecuteCampaignToolInput{Request: request, Call: call})
		}
		var accepted *WordflowCampaignWorkflowInput
		for index, call := range turnResult.Calls {
			var result ExecuteCampaignToolResult
			if err := results[index].Get(ctx, &result); err != nil {
				return WordflowCampaignWorkflowInput{}, fmt.Errorf("%s tool %s: %w", request.Kind, call.Name, err)
			}
			items = append(items,
				AgentItem{Type: AgentItemToolCall, CallID: call.CallID, Name: call.Name, Arguments: call.Arguments},
				AgentItem{Type: AgentItemToolResult, CallID: call.CallID, Text: result.Output},
			)
			switch {
			case result.Campaign != nil && accepted == nil:
				accepted = result.Campaign
			case call.Name == AgentToolSubmitCampaign && result.Campaign == nil:
				lastRejection = result.Output
				logger.Info("Campaign submission rejected", "kind", request.Kind, "turn", turn, "reason", result.Output)
			}
		}
		if accepted != nil {
			return *accepted, nil
		}
	}
	return WordflowCampaignWorkflowInput{}, temporal.NewNonRetryableApplicationError(
		fmt.Sprintf("the agent did not submit a valid %s campaign in %d turns; last rejection: %s", request.Kind, maxTurns, lastRejection),
		"campaign_not_generated",
		nil,
	)
}

type campaignPeriod struct {
	date     string
	startsAt time.Time
	endsAt   *time.Time // nil: the campaign never expires
	history  int        // past campaigns of this kind that later runs avoid repeating
	maxTurns int        // model turns the agent gets
}

// generatedCampaignPeriod returns the Toronto-time period that now falls in.
func generatedCampaignPeriod(kind campaign.Kind, now time.Time) (campaignPeriod, error) {
	local := now.In(torontoLocation)
	switch kind {
	case campaign.KindDailyChallenge:
		startsAt := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, torontoLocation)
		endsAt := startsAt.AddDate(0, 0, 1)
		return campaignPeriod{date: startsAt.Format(dateLayout), startsAt: startsAt, endsAt: &endsAt, history: dailyChallengeHistory, maxTurns: dailyChallengeMaxTurns}, nil
	case campaign.KindMonthlyPack:
		startsAt := time.Date(local.Year(), local.Month(), 1, 0, 0, 0, 0, torontoLocation)
		return campaignPeriod{date: startsAt.Format(dateLayout), startsAt: startsAt, history: monthlyPackHistory, maxTurns: monthlyPackMaxTurns}, nil
	default:
		return campaignPeriod{}, fmt.Errorf("unsupported campaign kind %q", kind)
	}
}

func appendGenerated(recent []GeneratedCampaign, generated WordflowCampaignWorkflowInput, history int) []GeneratedCampaign {
	entry := GeneratedCampaign{CampaignID: generated.Definition.ID}
	for _, level := range generated.Levels {
		for _, word := range level.Words {
			entry.Answers = append(entry.Answers, word.Answer)
		}
	}
	recent = append(append([]GeneratedCampaign(nil), recent...), entry)
	if len(recent) > history {
		recent = recent[len(recent)-history:]
	}
	return recent
}

func recentAnswers(recent []GeneratedCampaign) []string {
	seen := map[string]bool{}
	var answers []string
	for _, generated := range recent {
		for _, answer := range generated.Answers {
			if !seen[answer] {
				seen[answer] = true
				answers = append(answers, answer)
			}
		}
	}
	return answers
}
