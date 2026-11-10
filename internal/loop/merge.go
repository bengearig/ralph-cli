package loop

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/bengearig/ralph/internal/config"
	"github.com/bengearig/ralph/internal/drift"
	"github.com/bengearig/ralph/internal/errs"
	"github.com/bengearig/ralph/internal/looplock"
	"github.com/bengearig/ralph/internal/project"
	"github.com/bengearig/ralph/internal/service"
)

// MergeOptions selects optional parts of Merge.
type MergeOptions struct {
	// DeleteBranch removes the milestone's branch after the merge, with the
	// safe delete only.
	DeleteBranch bool
	// DryRun makes every check and reports the plan without changing anything.
	DryRun bool
}

// MergeResult is what Merge did, or with DryRun would do.
type MergeResult struct {
	Milestone  string `json:"milestone"`
	Branch     string `json:"branch"`
	BaseBranch string `json:"base_branch"`
	// From is the base branch's tip before the merge, To its tip after.
	From          string   `json:"from"`
	To            string   `json:"to"`
	DryRun        bool     `json:"dry_run"`
	AlreadyMerged bool     `json:"already_merged"`
	Merged        bool     `json:"merged"`
	BranchDeleted bool     `json:"branch_deleted"`
	Warnings      []string `json:"warnings"`
}

const maxDirtyListed = 5

// Merge fast-forwards the base branch to a finished milestone's branch. Every
// check runs before anything changes, and no merge commit is ever created.
func Merge(ctx context.Context, svc *service.Service, cfg *config.Config, root string, milestoneID int64, opts MergeOptions) (MergeResult, error) {
	res := MergeResult{BaseBranch: cfg.PR.BaseBranch, DryRun: opts.DryRun, Warnings: []string{}}
	if err := svc.CheckMilestoneWritable(ctx, milestoneID); err != nil {
		return res, err
	}
	m, err := svc.GetMilestone(ctx, milestoneID)
	if err != nil {
		return res, err
	}
	res.Milestone = m.ID
	if m.Status != service.MilestoneDone {
		return res, fmt.Errorf("%w: %s is %s, not DONE; only a finished milestone can be merged", errs.ErrNeedsHuman, m.ID, m.Status)
	}

	lockPath := filepath.Join(root, project.DirName, "loop.lock")
	holder, held, err := looplock.Probe(lockPath)
	if err != nil {
		return res, err
	}
	if held {
		who := "another process"
		if holder.PID != 0 {
			who = fmt.Sprintf("pid %d (%s)", holder.PID, holder.Milestone)
		}
		return res, fmt.Errorf("a ralph loop holds %s: %s; merging switches branches, so wait for it to finish", lockPath, who)
	}

	in, err := DriftInput(ctx, svc, cfg, root, milestoneID)
	if err != nil {
		return res, err
	}
	report, err := drift.Check(ctx, in)
	if err != nil {
		return res, err
	}
	res.Branch = report.Branch
	if report.State == drift.StateMerged {
		res.AlreadyMerged = true
		return res, nil
	}
	if !report.Clean() {
		return res, fmt.Errorf("%w: %s; run: ralph milestone reconcile %s", errs.ErrNeedsHuman, report.Summary(), m.ID)
	}

	repo := in.Repo
	dirty, err := repo.Dirty(ctx)
	if err != nil {
		return res, err
	}
	if len(dirty) > 0 {
		list := dirty
		more := ""
		if len(list) > maxDirtyListed {
			more = fmt.Sprintf(" and %d more", len(list)-maxDirtyListed)
			list = list[:maxDirtyListed]
		}
		return res, fmt.Errorf("%w: the working tree has uncommitted changes (%s%s); commit or stash them first", errs.ErrNeedsHuman, strings.Join(list, ", "), more)
	}

	base, branch := cfg.PR.BaseBranch, report.Branch
	baseExists, err := repo.BranchExists(ctx, base)
	if err != nil {
		return res, err
	}
	if !baseExists {
		return res, fmt.Errorf("base branch %s does not exist locally; merging needs a local %s branch", base, base)
	}
	branchExists, err := repo.BranchExists(ctx, branch)
	if err != nil {
		return res, err
	}
	if !branchExists {
		return res, fmt.Errorf("branch %s does not exist locally", branch)
	}
	if res.From, err = repo.RevParse(ctx, "refs/heads/"+base); err != nil {
		return res, err
	}
	if res.To, err = repo.RevParse(ctx, "refs/heads/"+branch); err != nil {
		return res, err
	}
	ff, err := repo.IsAncestor(ctx, res.From, res.To)
	if err != nil {
		return res, err
	}
	if !ff {
		return res, fmt.Errorf("%w: %s has commits that %s lacks; rebase the branch onto %s, then merge", errs.ErrNeedsHuman, base, branch, base)
	}
	if opts.DryRun {
		return res, nil
	}

	startBranch, startCommit, err := repo.HeadState(ctx)
	if err != nil {
		return res, err
	}
	restore := func() error {
		if startBranch != "" {
			return repo.Switch(ctx, startBranch)
		}
		return repo.SwitchDetached(ctx, startCommit)
	}
	if err := repo.Switch(ctx, base); err != nil {
		return res, err
	}
	if err := repo.MergeFFOnly(ctx, "refs/heads/"+branch); err != nil {
		// A fast-forward that fails while moving the ref can leave the
		// branch's files checked out; the tree was clean, so drop them.
		if head, herr := repo.RevParse(ctx, "HEAD"); herr == nil && head == res.From {
			_ = repo.ResetHard(ctx, res.From)
		}
		if rerr := restore(); rerr != nil {
			return res, fmt.Errorf("%w (and returning to the starting point failed: %v)", err, rerr)
		}
		return res, err
	}
	res.Merged = true

	if opts.DeleteBranch {
		if err := repo.DeleteBranch(ctx, branch); err != nil {
			res.Warnings = append(res.Warnings, fmt.Sprintf("branch %s was kept: %v", branch, err))
		} else {
			res.BranchDeleted = true
		}
	}
	return res, nil
}
