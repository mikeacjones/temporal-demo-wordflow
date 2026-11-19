package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/mjones/temporal-word-game/internal/campaign"
	"github.com/mjones/temporal-word-game/internal/workflows"

	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/temporal"
)

const scheduleTimeZone = "America/Toronto"

// campaignSchedules returns the Schedules that generate new campaigns: a daily
// challenge at midnight Toronto time, and a monthly pack at midnight on the
// first of each month.
func campaignSchedules(taskQueue string) []client.ScheduleOptions {
	return []client.ScheduleOptions{
		{
			ID: workflows.DailyChallengeScheduleID,
			Spec: client.ScheduleSpec{
				Calendars:    []client.ScheduleCalendarSpec{{Comment: "every day at midnight"}},
				TimeZoneName: scheduleTimeZone,
			},
			Action:        generatorAction(taskQueue, campaign.KindDailyChallenge),
			CatchupWindow: 12 * time.Hour,
			Note:          "Creates the dated Daily Challenge campaign.",
		},
		{
			ID: workflows.MonthlyPackScheduleID,
			Spec: client.ScheduleSpec{
				Calendars: []client.ScheduleCalendarSpec{{
					DayOfMonth: []client.ScheduleRange{{Start: 1}},
					Comment:    "midnight on the first of the month",
				}},
				TimeZoneName: scheduleTimeZone,
			},
			Action:        generatorAction(taskQueue, campaign.KindMonthlyPack),
			CatchupWindow: 7 * 24 * time.Hour,
			Note:          "Creates the permanent Monthly Pack campaign with new levels.",
		},
	}
}

func generatorAction(taskQueue string, kind campaign.Kind) *client.ScheduleWorkflowAction {
	return &client.ScheduleWorkflowAction{
		ID:                       workflows.CampaignGeneratorWorkflowIDPrefix + string(kind),
		Workflow:                 workflows.WordflowCampaignGeneratorWorkflowName,
		Args:                     []any{workflows.CampaignGeneratorWorkflowInput{Kind: kind}},
		TaskQueue:                taskQueue,
		WorkflowExecutionTimeout: 30 * time.Minute,
	}
}

// EnsureCampaignSchedules creates any campaign-generation Schedule that does
// not exist yet. Existing Schedules are left exactly as they are, so a worker
// restart never resets an operator's pause or edit.
func EnsureCampaignSchedules(ctx context.Context, temporalClient client.Client, taskQueue string) error {
	for _, options := range campaignSchedules(taskQueue) {
		_, err := temporalClient.ScheduleClient().Create(ctx, options)
		if err != nil && !errors.Is(err, temporal.ErrScheduleAlreadyRunning) {
			return fmt.Errorf("create schedule %s: %w", options.ID, err)
		}
	}
	return nil
}
