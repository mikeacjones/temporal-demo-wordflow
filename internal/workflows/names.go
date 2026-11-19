package workflows

import (
	"fmt"

	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"
)

const (
	TaskQueue            = "temporal-word-game"
	WorkerDeploymentName = "temporal-word-game"

	WordflowCampaignGeneratorWorkflowName = "WordflowCampaignGeneratorWorkflow"
	WordflowCampaignWorkflowName          = "WordflowCampaignWorkflow"
	WordflowLevelWorkflowName             = "WordflowLevelWorkflow"

	QueryWordflowLevelState = "wordflow-level-state"

	UpdateStartLevel            = "start-level"
	UpdateSubmitGuess           = "submit-guess"
	UpdateUseHint               = "use-hint"
	SignalRequestVersionUpgrade = "request-version-upgrade"

	ActivityRegisterCampaign     = "RegisterCampaign"
	ActivityUnregisterCampaign   = "UnregisterCampaign"
	ActivityRunCampaignAgentTurn = "RunCampaignAgentTurn"
	ActivityExecuteCampaignTool  = "ExecuteCampaignTool"
	ActivityClaimLevel           = "ClaimLevel"
	ActivitySpendPoints          = "SpendPoints"
	ActivityRecordLevelResult    = "RecordLevelResult"
)

const (
	// A Schedule appends its fire time to the generator's Workflow ID prefix.
	CampaignGeneratorWorkflowIDPrefix = "wordflow-generator/"

	DailyChallengeScheduleID = "wordflow-daily-challenge"
	MonthlyPackScheduleID    = "wordflow-monthly-pack"
)

func WordflowCampaignWorkflowID(campaignID string) string {
	return "wordflow-campaign/" + campaignID
}

func WordflowLevelWorkflowID(playerID, campaignID string, level int) string {
	return fmt.Sprintf("wordflow-level/%s/%s/%d", playerID, campaignID, level)
}

// Register registers the Workflows, Pinned when Worker Versioning is on: a
// level, campaign window or generation run finishes on the version it started on.
func Register(registry worker.WorkflowRegistry, versioned bool) {
	behavior := workflow.VersioningBehaviorUnspecified
	if versioned {
		behavior = workflow.VersioningBehaviorPinned
	}
	for name, definition := range map[string]any{
		WordflowCampaignGeneratorWorkflowName: WordflowCampaignGeneratorWorkflow,
		WordflowCampaignWorkflowName:          WordflowCampaignWorkflow,
		WordflowLevelWorkflowName:             WordflowLevelWorkflow,
	} {
		registry.RegisterWorkflowWithOptions(definition, workflow.RegisterOptions{Name: name, VersioningBehavior: behavior})
	}
}
