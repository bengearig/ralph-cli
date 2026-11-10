package loop

import (
	"context"

	"github.com/bengearig/ralph/internal/config"
	"github.com/bengearig/ralph/internal/domain"
	"github.com/bengearig/ralph/internal/drift"
	"github.com/bengearig/ralph/internal/service"
)

// Dispositions of a milestone within an initiative's chain.
const (
	DispositionMerged       = "merged"
	DispositionCancelled    = "cancelled"
	DispositionDoneUnmerged = "done_unmerged"
	DispositionReady        = "ready"
	DispositionNotReady     = "not_ready"
	DispositionBlocked      = "blocked"
	DispositionWaiting      = "waiting"
)

// Actions NextStep can choose.
const (
	ActionRun      = "run"
	ActionMerge    = "merge"
	ActionStop     = "stop"
	ActionComplete = "complete"
)

// ChainEntry is one milestone of an initiative and what the chain can do with it.
type ChainEntry struct {
	Milestone   string `json:"milestone"`
	Title       string `json:"title"`
	Status      string `json:"status"`
	Disposition string `json:"disposition"`
	Reason      string `json:"reason,omitempty"`
	Fix         string `json:"fix,omitempty"`
}

// ChainStep is the next thing to do for an initiative. Entry is nil for complete.
type ChainStep struct {
	Action string      `json:"action"`
	Entry  *ChainEntry `json:"entry,omitempty"`
}

// Overview classifies the initiative's milestones in position order. With
// force, unmerged dependencies do not hold a milestone back.
func Overview(ctx context.Context, svc *service.Service, cfg *config.Config, root string, initiativeID int64, force bool) ([]ChainEntry, error) {
	if err := svc.CheckInitiativeWritable(ctx, initiativeID); err != nil {
		return nil, err
	}
	ms, err := svc.ListMilestones(ctx, initiativeID, service.ListOptions{})
	if err != nil {
		return nil, err
	}
	entries := make([]ChainEntry, 0, len(ms))
	for _, m := range ms {
		e := ChainEntry{Milestone: m.ID, Title: m.Title, Status: m.Status}
		id, err := domain.ParseID(domain.KindMilestone, m.ID)
		if err != nil {
			return nil, err
		}
		switch m.Status {
		case service.MilestoneCancelled:
			e.Disposition = DispositionCancelled
		case service.MilestoneDone:
			in, err := DriftInput(ctx, svc, cfg, root, id)
			if err != nil {
				return nil, err
			}
			r, err := drift.Check(ctx, in)
			if err != nil {
				return nil, err
			}
			if r.State == drift.StateMerged {
				e.Disposition = DispositionMerged
				break
			}
			e.Disposition = DispositionDoneUnmerged
			e.Reason = r.Summary()
			e.Fix = "ralph milestone merge " + m.ID
		case service.MilestoneReady, service.MilestoneInProgress:
			e.Disposition = DispositionReady
			if force {
				break
			}
			fs, err := DependencyCheck(ctx, svc, cfg, root, id)
			if err != nil {
				return nil, err
			}
			if un := Unsatisfied(fs); len(un) > 0 {
				e.Disposition = DispositionWaiting
				e.Reason = un[0].Message(m.ID)
				e.Fix = un[0].Fix
			}
		case service.MilestoneBlocked:
			e.Disposition = DispositionBlocked
			e.Reason = "has BLOCKED tasks"
			e.Fix = "unblock them, then rerun"
		default:
			e.Disposition = DispositionNotReady
			e.Reason = "has no TODO tasks"
			if m.Status == service.MilestoneArticulating {
				e.Reason = "has PROPOSED tasks"
			}
			e.Fix = "/ralph:articulate " + m.ID
		}
		entries = append(entries, e)
	}
	return entries, nil
}

// NextStep picks what to do next: the first entry that is neither merged nor
// cancelled decides.
func NextStep(entries []ChainEntry, merge bool) ChainStep {
	for i := range entries {
		e := &entries[i]
		switch e.Disposition {
		case DispositionMerged, DispositionCancelled:
			continue
		case DispositionReady:
			return ChainStep{Action: ActionRun, Entry: e}
		case DispositionDoneUnmerged:
			if merge {
				return ChainStep{Action: ActionMerge, Entry: e}
			}
			stop := *e
			stop.Reason = "needs a merge"
			return ChainStep{Action: ActionStop, Entry: &stop}
		}
		return ChainStep{Action: ActionStop, Entry: e}
	}
	return ChainStep{Action: ActionComplete}
}
