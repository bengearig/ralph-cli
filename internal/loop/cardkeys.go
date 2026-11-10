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
