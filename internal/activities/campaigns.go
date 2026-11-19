package activities

import (
	"context"
	"errors"
	"time"

	"github.com/mjones/temporal-word-game/internal/catalog"
	"github.com/mjones/temporal-word-game/internal/store"
	"github.com/mjones/temporal-word-game/internal/workflows"

	"go.temporal.io/sdk/temporal"
)

// CampaignStore is the part of the store the campaign Activities use.
type CampaignStore interface {
	PutCampaign(ctx context.Context, registration catalog.Registration, registeredAt time.Time) error
	DeleteCampaign(ctx context.Context, campaignID, workflowID string) error
}

// Campaigns publishes campaigns to the catalog and takes them down.
type Campaigns struct {
	Store CampaignStore
}

func (a *Campaigns) RegisterCampaign(ctx context.Context, input workflows.RegisterCampaignActivityInput) error {
	if err := catalog.Validate(input.Registration); err != nil {
		return temporal.NewNonRetryableApplicationError(err.Error(), "invalid_campaign_registration", err)
	}
	err := a.Store.PutCampaign(ctx, input.Registration, time.Now())
	if errors.Is(err, store.ErrCampaignClaimed) {
		return temporal.NewNonRetryableApplicationError(
			"campaign "+input.Registration.Definition.ID+" is registered by another workflow",
			"campaign_already_registered", err,
		)
	}
	return err
}

func (a *Campaigns) UnregisterCampaign(ctx context.Context, input workflows.UnregisterCampaignActivityInput) error {
	return a.Store.DeleteCampaign(ctx, input.CampaignID, input.WorkflowID)
}
