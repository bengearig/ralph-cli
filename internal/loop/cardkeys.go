package loop

import (
	"context"

	"github.com/bengearig/ralph/internal/config"
	"github.com/bengearig/ralph/internal/domain"
	"github.com/bengearig/ralph/internal/service"
)

// CardKeys reads the recorded card keys of a milestone and its initiative for
// the branch template. It never calls the provider or writes; a missing card
// or any error leaves that key empty.
func CardKeys(ctx context.Context, svc *service.Service, cfg *config.Config, initiativeID, milestoneID int64) BranchKeys {
	provider := cfg.TaskManager.Provider
	if provider == config.TaskManagerNone {
		return BranchKeys{}
	}
	key := func(kind domain.Kind, id int64) string {
		c, err := svc.Card(ctx, kind, id, provider)
		if err != nil || c == nil {
			return ""
		}
		return c.Key
	}
	return BranchKeys{
		Initiative: key(domain.KindInitiative, initiativeID),
		Milestone:  key(domain.KindMilestone, milestoneID),
	}
}

// ResolveBranch returns the branch name of a milestone: its recorded branch,
// or the branch template rendered with the recorded card keys.
func ResolveBranch(ctx context.Context, svc *service.Service, cfg *config.Config, initiative service.InitiativeView, milestone service.MilestoneView) (string, error) {
	iID, err := domain.ParseID(domain.KindInitiative, initiative.ID)
	if err != nil {
		return "", err
	}
	mID, err := domain.ParseID(domain.KindMilestone, milestone.ID)
	if err != nil {
		return "", err
	}
	keys := CardKeys(ctx, svc, cfg, iID, mID)
	return MilestoneBranch(cfg.Loop.BranchTemplate,
		Ref{ID: initiative.ID, Slug: initiative.Slug, Title: initiative.Title},
		Ref{ID: milestone.ID, Slug: milestone.Slug, Title: milestone.Title},
		keys, milestone.Branch)
}
