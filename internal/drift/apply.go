package drift

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/bengearig/ralph/internal/domain"
	"github.com/bengearig/ralph/internal/service"
)

// Actions are the reconcile actions Apply performs, in field order.
type Actions struct {
	// Branch points the record at this existing local branch, keeping the
	// recorded base and head.
	Branch string
	// Reopen lists DONE tasks of the milestone to send back to TODO.
	Reopen []int64
	// Reason explains the reopen; required with Reopen.
	Reason string
	// Accept records the branch tip, the base and each DONE task's matched
	// commit as the milestone's new record.
	Accept bool
}

// Empty reports whether there is nothing to apply.
func (a Actions) Empty() bool {
	return a.Branch == "" && len(a.Reopen) == 0 && a.Reason == "" && !a.Accept
}

// Apply performs reconcile actions and returns the reports before and after
// them. Every action is validated before anything is written, and the writes
// share one service transaction, so a failure leaves the store unchanged.
// With no actions, after equals before.
func Apply(ctx context.Context, in Input, a Actions) (before, after Report, err error) {
	if before, err = Check(ctx, in); err != nil {
		return Report{}, Report{}, err
	}
	if a.Empty() {
		return before, before, nil
	}

	if err = in.Service.CheckMilestoneWritable(ctx, in.MilestoneID); err != nil {
		return before, Report{}, err
	}

	mid := before
	if a.Branch != "" {
		exists, err := in.Repo.BranchExists(ctx, a.Branch)
		if err != nil {
			return before, Report{}, err
		}
		if !exists {
			return before, Report{}, fmt.Errorf("%w: branch %q does not exist locally", domain.ErrInvalid, a.Branch)
		}
		if mid, err = check(ctx, in, a.Branch); err != nil {
			return before, Report{}, err
		}
	}
	reopen, err := reopenable(ctx, in, before.Milestone, a)
	if err != nil {
		return before, Report{}, err
	}
	if a.Accept {
		switch mid.State {
		case StateMerged:
			return before, Report{}, fmt.Errorf("%w: cannot accept: %s", domain.ErrInvalid, mid.Summary())
		case StateBranchMissing:
			return before, Report{}, fmt.Errorf("%w: cannot accept: %s; point the record at an existing branch with --branch first",
				domain.ErrInvalid, mid.Summary())
		}
	}

	err = in.Service.InTx(ctx, func(tx *service.Service) error {
		if a.Branch != "" {
			g := domain.MilestoneGit{Branch: a.Branch, BaseCommit: before.Recorded.BaseCommit, HeadCommit: before.Recorded.HeadCommit}
			if err := tx.RecordMilestoneGit(ctx, in.MilestoneID, g); err != nil {
				return fmt.Errorf("recording branch %s: %w", a.Branch, err)
			}
		}
		for _, id := range reopen {
			if _, err := tx.ReopenTask(ctx, id, a.Reason); err != nil {
				return fmt.Errorf("reopening %s: %w", domain.FormatID(domain.KindTask, id), err)
			}
		}
		if a.Accept {
			return accept(ctx, tx, in.MilestoneID, mid, reopen)
		}
		return nil
	})
	if err != nil {
		return before, Report{}, err
	}
	if after, err = Check(ctx, in); err != nil {
		return before, Report{}, fmt.Errorf("reconcile applied, but checking again failed: %w", err)
	}
	return before, after, nil
}

// reopenable validates the reopen request and returns its task IDs without
// duplicates.
func reopenable(ctx context.Context, in Input, milestone string, a Actions) ([]int64, error) {
	if len(a.Reopen) == 0 {
		if strings.TrimSpace(a.Reason) != "" {
			return nil, fmt.Errorf("%w: a reason is only used when reopening tasks", domain.ErrInvalid)
		}
		return nil, nil
	}
	if err := domain.RequireText("reason", a.Reason); err != nil {
		return nil, err
	}
	var ids []int64
	for _, id := range a.Reopen {
		if slices.Contains(ids, id) {
			continue
		}
		t, err := in.Service.GetTask(ctx, id)
		if err != nil {
			return nil, fmt.Errorf("reopening %s: %w", domain.FormatID(domain.KindTask, id), err)
		}
		if t.MilestoneID != milestone {
			return nil, fmt.Errorf("%w: %s belongs to %s, not %s", domain.ErrInvalid, t.ID, t.MilestoneID, milestone)
		}
		if t.Status != string(domain.StatusDone) {
			return nil, fmt.Errorf("%w: only DONE tasks can be reopened; %s is %s", domain.ErrInvalid, t.ID, t.Status)
		}
		ids = append(ids, id)
	}
	return ids, nil
}

// accept records the report's tip, branch and proposed base, and each DONE
// task's matched commit, except for the tasks being reopened.
func accept(ctx context.Context, tx *service.Service, milestoneID int64, r Report, reopened []int64) error {
	g := domain.MilestoneGit{Branch: r.Branch, BaseCommit: r.ProposedBase, HeadCommit: r.Tip}
	if err := tx.RecordMilestoneGit(ctx, milestoneID, g); err != nil {
		return fmt.Errorf("recording branch %s: %w", r.Branch, err)
	}
	for _, f := range r.Tasks {
		id, err := domain.ParseID(domain.KindTask, f.ID)
		if err != nil {
			return err
		}
		if slices.Contains(reopened, id) {
			continue
		}
		if err := tx.RecordTaskCommit(ctx, id, f.Commit); err != nil {
			return fmt.Errorf("recording %s's commit: %w", f.ID, err)
		}
		if f.Match == MatchMissing {
			note := fmt.Sprintf("reconcile: no commit found on %s at %s; kept DONE", r.Branch, short(r.Tip))
			if _, err := tx.AddTaskNote(ctx, id, domain.NoteGeneral, note); err != nil {
				return fmt.Errorf("noting %s: %w", f.ID, err)
			}
		}
	}
	return nil
}
