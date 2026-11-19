package workflows

import (
	"fmt"

	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"
)

const (
	TaskQueue            = "temporal-word-game"
	WorkerDeploymentName = "temporal-word-game"

	CatalogWorkflowName                   = "CatalogWorkflow"
	WordflowCampaignGeneratorWorkflowName = "WordflowCampaignGeneratorWorkflow"
	WordflowCampaignWorkflowName          = "WordflowCampaignWorkflow"
	PlayerWorkflowName                    = "PlayerWorkflow"
	WordflowLevelWorkflowName             = "WordflowLevelWorkflow"
	LeaderboardWorkflowName               = "LeaderboardWorkflow"

	QueryCampaignSummary      = "campaign-summary"
	QueryCampaignView         = "campaign-view"
	QueryCatalog              = "catalog"
	QueryCatalogWordflowLevel = "catalog-wordflow-level"
	QueryWordflowLevel        = "wordflow-level"
	QueryPlayerState          = "player-state"
	QueryWordflowLevelState   = "wordflow-level-state"
	QueryLeaderboard          = "leaderboard"

	UpdateOpenCatalog        = "open-catalog"
	UpdateRegisterCampaign   = "register-campaign"
	UpdateUnregisterCampaign = "unregister-campaign"
	// Keep the existing wire name so accounts pinned to older Workers can log in.
	UpdateAuthenticatePlayer    = "open-session"
	UpdateStartLevel            = "start-level"
	UpdateSpendPoints           = "spend-points"
	UpdateBuyStreakFreeze       = "buy-streak-freeze"
	UpdateSubmitGuess           = "submit-guess"
	UpdateUseHint               = "use-hint"
	SignalRequestVersionUpgrade = "request-version-upgrade"
	SignalLeaderboardScore      = "record-score"

	ActivityRegisterCampaign     = "RegisterCampaign"
	ActivityUnregisterCampaign   = "UnregisterCampaign"
	ActivityRunCampaignAgentTurn = "RunCampaignAgentTurn"
	ActivityExecuteCampaignTool  = "ExecuteCampaignTool"
	ActivityResolveWordflowLevel = "ResolveWordflowLevel"
	ActivitySpendPoints          = "SpendPoints"
	ActivityPublishLeaderboard   = "PublishLeaderboard"
)

const (
	CatalogWorkflowID     = "catalog/global"
	LeaderboardWorkflowID = "leaderboard/global"

	// A Schedule appends its fire time to the generator's Workflow ID prefix.
	CampaignGeneratorWorkflowIDPrefix = "wordflow-generator/"

	DailyChallengeScheduleID = "wordflow-daily-challenge"
	MonthlyPackScheduleID    = "wordflow-monthly-pack"
)

func PlayerWorkflowID(playerID string) string {
	return "player/" + playerID
}

func WordflowCampaignWorkflowID(campaignID string) string {
	return "wordflow-campaign/" + campaignID
}

func WordflowLevelWorkflowID(playerID, campaignID string, level int) string {
	return fmt.Sprintf("wordflow-level/%s/%s/%d", playerID, campaignID, level)
}

// Register uses pinned registrations when Worker Versioning is enabled.
func Register(registry worker.Registry, versioned bool) {
	if !versioned {
		registry.RegisterWorkflowWithOptions(CatalogWorkflow, workflow.RegisterOptions{Name: CatalogWorkflowName})
		registry.RegisterWorkflowWithOptions(WordflowCampaignGeneratorWorkflow, workflow.RegisterOptions{Name: WordflowCampaignGeneratorWorkflowName})
		registry.RegisterWorkflowWithOptions(WordflowCampaignWorkflow, workflow.RegisterOptions{Name: WordflowCampaignWorkflowName})
		registry.RegisterWorkflowWithOptions(PlayerWorkflow, workflow.RegisterOptions{Name: PlayerWorkflowName})
		registry.RegisterWorkflowWithOptions(WordflowLevelWorkflow, workflow.RegisterOptions{Name: WordflowLevelWorkflowName})
		registry.RegisterWorkflowWithOptions(LeaderboardWorkflow, workflow.RegisterOptions{Name: LeaderboardWorkflowName})
		return
	}

	registry.RegisterWorkflowWithOptions(CatalogWorkflow, workflow.RegisterOptions{
		Name:               CatalogWorkflowName,
		VersioningBehavior: workflow.VersioningBehaviorPinned,
	})
	registry.RegisterWorkflowWithOptions(WordflowCampaignGeneratorWorkflow, workflow.RegisterOptions{
		Name:               WordflowCampaignGeneratorWorkflowName,
		VersioningBehavior: workflow.VersioningBehaviorPinned,
	})
	registry.RegisterWorkflowWithOptions(WordflowCampaignWorkflow, workflow.RegisterOptions{
		Name:               WordflowCampaignWorkflowName,
		VersioningBehavior: workflow.VersioningBehaviorPinned,
	})
	registry.RegisterWorkflowWithOptions(PlayerWorkflow, workflow.RegisterOptions{
		Name:               PlayerWorkflowName,
		VersioningBehavior: workflow.VersioningBehaviorPinned,
	})
	registry.RegisterWorkflowWithOptions(WordflowLevelWorkflow, workflow.RegisterOptions{
		Name:               WordflowLevelWorkflowName,
		VersioningBehavior: workflow.VersioningBehaviorPinned,
	})
	registry.RegisterWorkflowWithOptions(LeaderboardWorkflow, workflow.RegisterOptions{
		Name:               LeaderboardWorkflowName,
		VersioningBehavior: workflow.VersioningBehaviorPinned,
	})
}
