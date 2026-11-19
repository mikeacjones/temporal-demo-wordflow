package workflows

import (
	"testing"
	"time"

	"github.com/mjones/temporal-word-game/internal/game"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/converter"
	"go.temporal.io/sdk/testsuite"
	"go.temporal.io/sdk/workflow"
)

func requestVersionUpgrade(env *testsuite.TestWorkflowEnvironment) {
	env.SignalWorkflow(SignalRequestVersionUpgrade, struct{}{})
}

func requireVersionUpgrade(t *testing.T, env *testsuite.TestWorkflowEnvironment) *workflow.ContinueAsNewError {
	t.Helper()
	var continueAsNew *workflow.ContinueAsNewError
	require.ErrorAs(t, env.GetWorkflowError(), &continueAsNew)
	require.EqualValues(t, workflow.ContinueAsNewVersioningBehaviorAutoUpgrade, continueAsNew.InitialVersioningBehavior)
	return continueAsNew
}

func TestLevelUpgradeSignalContinuesAsNew(t *testing.T) {
	state := &WordflowLevelState{
		WorkflowID: "wordflow-level/player/campaign/1",
		PlayerID:   "player",
		CampaignID: "campaign",
		Puzzle: game.Puzzle{
			Level: 1, Title: "One", Letters: "ONE",
			Words: []game.PlacedWord{{Answer: "ONE"}},
		},
		StartedAt: time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC),
	}
	env := newLevelTestEnv(t, state.Puzzle)
	env.RegisterDelayedCallback(func() { requestVersionUpgrade(env) }, time.Millisecond)

	env.ExecuteWorkflow(WordflowLevelWorkflow, WordflowLevelWorkflowInput{State: state})

	var nextRun WordflowLevelWorkflowInput
	require.NoError(t, converter.GetDefaultDataConverter().FromPayloads(requireVersionUpgrade(t, env).Input, &nextRun))
	require.Equal(t, state, nextRun.State)
}
