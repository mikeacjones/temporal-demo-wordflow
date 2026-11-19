package workflows

import (
	"testing"
	"time"

	"github.com/mjones/temporal-word-game/internal/campaign"
	"github.com/mjones/temporal-word-game/internal/game"

	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/testsuite"
)

func newCampaignTestEnv() *testsuite.TestWorkflowEnvironment {
	env := (&testsuite.WorkflowTestSuite{}).NewTestWorkflowEnvironment()
	env.RegisterActivityWithOptions(func(RegisterCampaignActivityInput) error { return nil },
		activity.RegisterOptions{Name: ActivityRegisterCampaign})
	env.RegisterActivityWithOptions(func(UnregisterCampaignActivityInput) error { return nil },
		activity.RegisterOptions{Name: ActivityUnregisterCampaign})
	return env
}

func testCampaignInput(endsAt *time.Time) WordflowCampaignWorkflowInput {
	return WordflowCampaignWorkflowInput{
		Definition: campaign.Definition{
			ID: "test", EndsAt: endsAt,
			Game: campaign.GameSummary{ID: campaign.GameWordflow, Title: "Wordflow"},
		},
		Levels: []game.Puzzle{{Level: 1, Title: "One", Letters: "ONE"}},
	}
}

func TestPermanentCampaignCompletesOncePublished(t *testing.T) {
	env := newCampaignTestEnv()
	var published RegisterCampaignActivityInput
	env.OnActivity(ActivityRegisterCampaign, mock.Anything).
		Run(func(arguments mock.Arguments) { published = arguments.Get(0).(RegisterCampaignActivityInput) }).
		Return(nil).Once()

	env.ExecuteWorkflow(WordflowCampaignWorkflow, testCampaignInput(nil))

	require.NoError(t, env.GetWorkflowError())
	require.Equal(t, "test", published.Registration.Definition.ID)
	require.NotEmpty(t, published.Registration.WorkflowID)
	require.Len(t, published.Registration.Levels, 1)
	env.AssertExpectations(t)
}

func TestCampaignUnregistersAndCompletesAtItsEndTime(t *testing.T) {
	env := newCampaignTestEnv()
	endsAt := env.Now().Add(time.Hour)
	env.OnActivity(ActivityRegisterCampaign, mock.Anything).Return(nil).Once()
	var removal UnregisterCampaignActivityInput
	var removedAt time.Time
	env.OnActivity(ActivityUnregisterCampaign, mock.Anything).
		Run(func(arguments mock.Arguments) {
			removal = arguments.Get(0).(UnregisterCampaignActivityInput)
			removedAt = env.Now()
		}).Return(nil).Once()

	env.ExecuteWorkflow(WordflowCampaignWorkflow, testCampaignInput(&endsAt))

	require.NoError(t, env.GetWorkflowError())
	require.Equal(t, "test", removal.CampaignID)
	require.NotEmpty(t, removal.WorkflowID)
	require.False(t, removedAt.Before(endsAt), "the campaign comes down at its end time, not before")
	env.AssertExpectations(t)
}

func TestCampaignRefusesLevelsTheGameCannotRun(t *testing.T) {
	env := newCampaignTestEnv()
	input := testCampaignInput(nil)
	input.Levels[0].Letters = "ABCDEFGHI"

	env.ExecuteWorkflow(WordflowCampaignWorkflow, input)

	var applicationError *temporal.ApplicationError
	require.ErrorAs(t, env.GetWorkflowError(), &applicationError)
	require.Equal(t, "invalid_campaign", applicationError.Type())
	require.True(t, applicationError.NonRetryable())
}
