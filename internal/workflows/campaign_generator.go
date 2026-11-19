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

type GenerateWordflowCampaignActivityInput struct {
	Kind          campaign.Kind `json:"kind"`
	Date          string        `json:"date"`
	StartsAt      time.Time     `json:"startsAt"`
	EndsAt        *time.Time    `json:"endsAt,omitempty"`
	ExcludedWords []string      `json:"excludedWords,omitempty"`
}

// GeneratedCampaignID names the campaign a run of this kind creates for the
// period that contains date (YYYY-MM-DD).
func GeneratedCampaignID(kind campaign.Kind, date string) string {
	if kind == campaign.KindMonthlyPack {
		return string(kind) + "-" + date[:len(monthLayout)]
	}
	return string(kind) + "-" + date
}

// WordflowCampaignGeneratorWorkflow generates one campaign with an Activity
// and starts it. Daily challenges are single-day campaigns; monthly packs are
// permanent campaigns that add new levels to the catalog.
func WordflowCampaignGeneratorWorkflow(ctx workflow.Context, input CampaignGeneratorWorkflowInput) (CampaignGeneratorResult, error) {
	date, startsAt, endsAt, history, err := generatedCampaignPeriod(input.Kind, workflow.Now(ctx))
	if err != nil {
		return CampaignGeneratorResult{}, temporal.NewNonRetryableApplicationError(err.Error(), "invalid_campaign_kind", err)
	}

	var previous CampaignGeneratorResult
	if workflow.HasLastCompletionResult(ctx) {
		if err := workflow.GetLastCompletionResult(ctx, &previous); err != nil {
			return CampaignGeneratorResult{}, fmt.Errorf("read previous generator result: %w", err)
		}
	}

	activityCtx := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		StartToCloseTimeout:    90 * time.Second,
		ScheduleToCloseTimeout: 10 * time.Minute,
		RetryPolicy: &temporal.RetryPolicy{
			InitialInterval:    5 * time.Second,
			BackoffCoefficient: 2,
			MaximumInterval:    time.Minute,
			MaximumAttempts:    5,
		},
	})
	var campaignInput WordflowCampaignWorkflowInput
	if err := workflow.ExecuteActivity(activityCtx, ActivityGenerateWordflowCampaign, GenerateWordflowCampaignActivityInput{
		Kind: input.Kind, Date: date, StartsAt: startsAt, EndsAt: endsAt,
		ExcludedWords: recentAnswers(previous.Recent),
	}).Get(ctx, &campaignInput); err != nil {
		return CampaignGeneratorResult{}, fmt.Errorf("generate %s campaign: %w", input.Kind, err)
	}

	expectedCampaignID := GeneratedCampaignID(input.Kind, date)
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

	return CampaignGeneratorResult{Recent: appendGenerated(previous.Recent, campaignInput, history)}, nil
}

// generatedCampaignPeriod returns the Toronto-time period that now falls in,
// and how many past campaigns of this kind later runs avoid repeating.
// A nil end means the campaign never expires.
func generatedCampaignPeriod(kind campaign.Kind, now time.Time) (date string, startsAt time.Time, endsAt *time.Time, history int, err error) {
	local := now.In(torontoLocation)
	switch kind {
	case campaign.KindDailyChallenge:
		startsAt = time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, torontoLocation)
		end := startsAt.AddDate(0, 0, 1)
		return startsAt.Format(dateLayout), startsAt, &end, dailyChallengeHistory, nil
	case campaign.KindMonthlyPack:
		startsAt = time.Date(local.Year(), local.Month(), 1, 0, 0, 0, 0, torontoLocation)
		return startsAt.Format(dateLayout), startsAt, nil, monthlyPackHistory, nil
	default:
		return "", time.Time{}, nil, 0, fmt.Errorf("unsupported campaign kind %q", kind)
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
