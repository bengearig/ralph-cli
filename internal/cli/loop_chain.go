package cli

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/bengearig/ralph/internal/config"
	"github.com/bengearig/ralph/internal/domain"
	"github.com/bengearig/ralph/internal/errs"
	"github.com/bengearig/ralph/internal/loop"
	"github.com/bengearig/ralph/internal/service"
)

// chainOverview computes the chain's state each step; tests replace it.
var chainOverview = loop.Overview

func (a *app) chainPrintf(format string, args ...any) error {
	var w io.Writer = a.streams.Out
	if a.jsonOut {
		w = a.streams.Err
	}
	_, err := fmt.Fprintf(w, format, args...)
	return err
}

func (a *app) chainStop(ref string, err error) error {
	if perr := a.chainPrintf("stopped at %s: %v\n", ref, err); perr != nil {
		return errors.Join(err, perr)
	}
	return err
}

func (a *app) chainInterrupted(err error) error {
	if perr := a.chainPrintf("interrupted\n"); perr != nil {
		return errors.Join(err, perr)
	}
	return err
}

// loopChain runs and merges the initiative's milestones in order until it is
// complete or something stops it.
func (a *app) loopChain(ctx context.Context, svc *service.Service, cfg *config.Config, root string, iID int64, entries []loop.ChainEntry, opts loopOptions) error {
	iRef := domain.FormatID(domain.KindInitiative, iID)
	maxSteps := 2*len(entries) + 1
	for step := 0; ; step++ {
		if err := ctx.Err(); err != nil {
			return a.chainInterrupted(fmt.Errorf("loop interrupted: %w", err))
		}
		if step >= maxSteps {
			return fmt.Errorf("the chain made no progress after %d steps; check milestone state with ralph loop --initiative %s --dry-run", step, iRef)
		}
		if step > 0 {
			var err error
			if entries, err = chainOverview(ctx, svc, cfg, root, iID, opts.force); err != nil {
				return err
			}
		}
		next := loop.NextStep(entries, true)
		switch next.Action {
		case loop.ActionComplete:
			return a.chainPrintf("initiative %s is complete: every milestone is merged or cancelled\n", iRef)
		case loop.ActionStop:
			n := newChainNext(next)
			msg := fmt.Sprintf("stopped at %s: %s; %s", n.Milestone, n.Reason, n.Fix)
			if err := a.chainPrintf("%s\n", msg); err != nil {
				return err
			}
			return fmt.Errorf("%w: %s", errs.ErrNeedsHuman, msg)
		}
		ref := next.Entry.Milestone
		mID, err := domain.ParseID(domain.KindMilestone, ref)
		if err != nil {
			return err
		}
		if next.Action == loop.ActionMerge {
			if err := a.chainPrintf("merging %s\n", ref); err != nil {
				return err
			}
			res, err := loop.Merge(ctx, svc, cfg, root, mID, loop.MergeOptions{})
			if err != nil {
				return a.chainStop(ref, err)
			}
			if res.AlreadyMerged {
				if err := a.chainPrintf("%s is already merged\n", ref); err != nil {
					return err
				}
			}
			continue
		}
		if err := a.chainPrintf("next: %s\n", ref); err != nil {
			return err
		}
		out, err := a.loopMilestone(ctx, mID, opts)
		if err != nil {
			if ctx.Err() != nil {
				return a.chainInterrupted(err)
			}
			return err
		}
		m, err := svc.GetMilestone(ctx, mID)
		if err != nil {
			return err
		}
		if m.Status != service.MilestoneDone {
			msg := fmt.Sprintf("stopped at %s: the run ended %s and the milestone is %s, not DONE", ref, out.StopReason, m.Status)
			if err := a.chainPrintf("%s\n", msg); err != nil {
				return err
			}
			return fmt.Errorf("%w: %s", errs.ErrNeedsHuman, msg)
		}
	}
}
