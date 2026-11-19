package bootstrap

import (
	"context"
	"errors"
	"testing"

	"github.com/mjones/temporal-word-game/internal/campaign"
	"github.com/mjones/temporal-word-game/internal/workflows"

	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/mocks"
	"go.temporal.io/sdk/temporal"
)

func newScheduleClient(t *testing.T) (*mocks.Client, *mocks.ScheduleClient) {
	t.Helper()
	scheduleClient := &mocks.ScheduleClient{}
	temporalClient := &mocks.Client{}
	temporalClient.On("ScheduleClient").Return(scheduleClient)
	t.Cleanup(func() {
		scheduleClient.AssertExpectations(t)
		temporalClient.AssertExpectations(t)
	})
	return temporalClient, scheduleClient
}

func TestEnsureCampaignSchedulesCreatesBothSchedules(t *testing.T) {
	temporalClient, scheduleClient := newScheduleClient(t)
	var created []client.ScheduleOptions
	scheduleClient.On("Create", mock.Anything, mock.Anything).
		Run(func(arguments mock.Arguments) { created = append(created, arguments.Get(1).(client.ScheduleOptions)) }).
		Return(nil, nil).Twice()

	require.NoError(t, EnsureCampaignSchedules(context.Background(), temporalClient, "test-queue"))

	require.Len(t, created, 2)
	daily, monthly := created[0], created[1]
	require.Equal(t, workflows.DailyChallengeScheduleID, daily.ID)
	require.Equal(t, workflows.MonthlyPackScheduleID, monthly.ID)
	for _, options := range created {
		require.Equal(t, "America/Toronto", options.Spec.TimeZoneName)
		action := options.Action.(*client.ScheduleWorkflowAction)
		require.Equal(t, workflows.WordflowCampaignGeneratorWorkflowName, action.Workflow)
		require.Equal(t, "test-queue", action.TaskQueue)
		require.Contains(t, action.ID, workflows.CampaignGeneratorWorkflowIDPrefix)
	}
	require.Equal(t, []any{workflows.CampaignGeneratorWorkflowInput{Kind: campaign.KindDailyChallenge}},
		daily.Action.(*client.ScheduleWorkflowAction).Args)
	require.Equal(t, []any{workflows.CampaignGeneratorWorkflowInput{Kind: campaign.KindMonthlyPack}},
		monthly.Action.(*client.ScheduleWorkflowAction).Args)
	require.Empty(t, daily.Spec.Calendars[0].DayOfMonth, "the daily schedule matches every day")
	require.Equal(t, []client.ScheduleRange{{Start: 1}}, monthly.Spec.Calendars[0].DayOfMonth)
}

func TestEnsureCampaignSchedulesLeavesExistingSchedulesAlone(t *testing.T) {
	temporalClient, scheduleClient := newScheduleClient(t)
	scheduleClient.On("Create", mock.Anything, mock.Anything).Return(nil, temporal.ErrScheduleAlreadyRunning).Twice()

	require.NoError(t, EnsureCampaignSchedules(context.Background(), temporalClient, "test-queue"))
}

func TestEnsureCampaignSchedulesReportsOtherFailures(t *testing.T) {
	temporalClient, scheduleClient := newScheduleClient(t)
	scheduleClient.On("Create", mock.Anything, mock.Anything).Return(nil, errors.New("namespace unavailable")).Once()

	err := EnsureCampaignSchedules(context.Background(), temporalClient, "test-queue")

	require.ErrorContains(t, err, workflows.DailyChallengeScheduleID)
	require.ErrorContains(t, err, "namespace unavailable")
}
