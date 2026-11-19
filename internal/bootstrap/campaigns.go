package bootstrap

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"time"

	"github.com/mjones/temporal-word-game/internal/campaign"
	"github.com/mjones/temporal-word-game/internal/game"
	"github.com/mjones/temporal-word-game/internal/workflows"

	"go.temporal.io/api/enums/v1"
	"go.temporal.io/sdk/client"
)

//go:embed temporal-foundations.json
var campaignFiles embed.FS

type campaignFile struct {
	ID            string                 `json:"id"`
	Kind          campaign.Kind          `json:"kind,omitempty"`
	SingleAttempt bool                   `json:"singleAttempt,omitempty"`
	Title         string                 `json:"title"`
	Description   string                 `json:"description"`
	Game          campaign.GameSummary   `json:"game"`
	StartsAt      *time.Time             `json:"startsAt,omitempty"`
	EndsAt        *time.Time             `json:"endsAt,omitempty"`
	Requirements  campaign.Requirements  `json:"requirements"`
	Unlock        campaignUnlockFile     `json:"unlock"`
	Levels        []game.LevelDefinition `json:"levels"`
}

type campaignUnlockFile struct {
	InitialLevels     int `json:"initialLevels"`
	LevelsPerInterval int `json:"levelsPerInterval"`
	IntervalHours     int `json:"intervalHours"`
}

// StartDefaultCampaign publishes the built-in Temporal Foundations campaign.
func StartDefaultCampaign(ctx context.Context, temporalClient client.Client, taskQueue string) error {
	input, err := DefaultWordflowCampaign()
	if err != nil {
		return err
	}
	return StartWordflowCampaign(ctx, temporalClient, taskQueue, input)
}

// StartWordflowCampaign starts the campaign's Workflow, which publishes it to
// the catalog. A permanent campaign's Workflow completes once it has published,
// so starting it again (every deploy starts the default one) publishes it again
// in a new run; the write is idempotent. A run still open is left alone.
func StartWordflowCampaign(ctx context.Context, temporalClient client.Client, taskQueue string, input workflows.WordflowCampaignWorkflowInput) error {
	if input.Definition.ID == "" {
		return fmt.Errorf("campaign ID is required")
	}
	_, err := temporalClient.ExecuteWorkflow(ctx, client.StartWorkflowOptions{
		ID:                       workflows.WordflowCampaignWorkflowID(input.Definition.ID),
		TaskQueue:                taskQueue,
		WorkflowIDConflictPolicy: enums.WORKFLOW_ID_CONFLICT_POLICY_USE_EXISTING,
		WorkflowIDReusePolicy:    enums.WORKFLOW_ID_REUSE_POLICY_ALLOW_DUPLICATE,
	}, workflows.WordflowCampaignWorkflowName, input)
	return err
}

func DefaultWordflowCampaign() (workflows.WordflowCampaignWorkflowInput, error) {
	contents, err := campaignFiles.ReadFile("temporal-foundations.json")
	if err != nil {
		return workflows.WordflowCampaignWorkflowInput{}, err
	}
	return ParseWordflowCampaign(contents)
}

func ParseWordflowCampaign(contents []byte) (workflows.WordflowCampaignWorkflowInput, error) {
	var configured campaignFile
	if err := json.Unmarshal(contents, &configured); err != nil {
		return workflows.WordflowCampaignWorkflowInput{}, fmt.Errorf("read wordflow campaign: %w", err)
	}

	levels := make([]game.Puzzle, 0, len(configured.Levels))
	for index, definition := range configured.Levels {
		puzzle, err := game.BuildPuzzle(index+1, definition)
		if err != nil {
			return workflows.WordflowCampaignWorkflowInput{}, fmt.Errorf("build wordflow campaign: %w", err)
		}
		levels = append(levels, puzzle)
	}
	return workflows.WordflowCampaignWorkflowInput{
		Definition: campaign.Definition{
			ID: configured.ID, Kind: configured.Kind, SingleAttempt: configured.SingleAttempt,
			Game:  configured.Game,
			Title: configured.Title, Description: configured.Description,
			StartsAt: configured.StartsAt, EndsAt: configured.EndsAt,
			Requirements: configured.Requirements,
			Unlock: campaign.UnlockPolicy{
				InitialLevels:     configured.Unlock.InitialLevels,
				LevelsPerInterval: configured.Unlock.LevelsPerInterval,
				Interval:          time.Duration(configured.Unlock.IntervalHours) * time.Hour,
			},
		},
		Levels: levels,
	}, nil
}
