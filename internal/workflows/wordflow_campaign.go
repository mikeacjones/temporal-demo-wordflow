package workflows

import (
	"time"

	"github.com/mjones/temporal-word-game/internal/campaign"
	"github.com/mjones/temporal-word-game/internal/catalog"
	"github.com/mjones/temporal-word-game/internal/game"

	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"
)

type WordflowCampaignWorkflowInput struct {
	Definition campaign.Definition `json:"definition"`
	Levels     []game.Puzzle       `json:"levels"`
}

type RegisterCampaignActivityInput struct {
	Registration catalog.Registration `json:"registration"`
}

type UnregisterCampaignActivityInput struct {
	CampaignID string `json:"campaignId"`
	WorkflowID string `json:"workflowId"`
}

// WordflowCampaignWorkflow publishes a campaign to the catalog. A campaign
// with an end time stays open until then and takes itself down; a permanent
// one is done once it is published.
func WordflowCampaignWorkflow(ctx workflow.Context, input WordflowCampaignWorkflowInput) error {
	if err := catalog.ValidateLevels(input.Levels); err != nil {
		return temporal.NewNonRetryableApplicationError(err.Error(), "invalid_campaign", nil)
	}
	workflowID := workflow.GetInfo(ctx).WorkflowExecution.ID
	activityCtx := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{StartToCloseTimeout: 30 * time.Second})

	if err := workflow.ExecuteActivity(activityCtx, ActivityRegisterCampaign, RegisterCampaignActivityInput{
		Registration: catalog.Registration{WorkflowID: workflowID, Definition: input.Definition, Levels: input.Levels},
	}).Get(ctx, nil); err != nil {
		return err
	}
	if input.Definition.EndsAt == nil {
		return nil
	}

	if remaining := input.Definition.EndsAt.Sub(workflow.Now(ctx)); remaining > 0 {
		if err := workflow.Sleep(ctx, remaining); err != nil {
			return err
		}
	}
	return workflow.ExecuteActivity(activityCtx, ActivityUnregisterCampaign, UnregisterCampaignActivityInput{
		CampaignID: input.Definition.ID, WorkflowID: workflowID,
	}).Get(ctx, nil)
}
