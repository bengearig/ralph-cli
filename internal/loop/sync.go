package loop

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"

	"github.com/bengearig/ralph/internal/config"
	"github.com/bengearig/ralph/internal/domain"
	"github.com/bengearig/ralph/internal/drift"
	"github.com/bengearig/ralph/internal/errs"
	"github.com/bengearig/ralph/internal/git"
	"github.com/bengearig/ralph/internal/looplock"
	"github.com/bengearig/ralph/internal/project"
	"github.com/bengearig/ralph/internal/service"
)

// SyncOptions selects optional parts of SyncBase.
type SyncOptions struct {
	// DryRun makes every check, the fetch included, but never moves the branch.
	DryRun bool
}

// SyncResult is what SyncBase did, or with DryRun would do.
type SyncResult struct {
	Remote     string        `json:"remote"`
	BaseBranch string        `json:"base_branch"`
	State      git.BaseState `json:"state"`
	Behind     int           `json:"behind"`
	Ahead      int           `json:"ahead"`
	// From is the local base branch's tip before the sync, To its tip after.
	From     string   `json:"from"`
	To       string   `json:"to"`
	DryRun   bool     `json:"dry_run"`
	Synced   bool     `json:"synced"`
	Warnings []string `json:"warnings"`
	// NewlyMerged lists the milestones the sync brought into the base branch.
	NewlyMerged []MergedMilestone `json:"newly_merged"`
	// RemoteTip is the fetched remote tip; a dry run reports it as the target.
	RemoteTip string `json:"-"`
}

// MergedMilestone is a milestone a sync merged into the base branch.
type MergedMilestone struct {
	ID           string `json:"id"`
	Branch       string `json:"branch"`
	BranchExists bool   `json:"branch_exists"`
	MergedBy     string `json:"merged_by"`
}

// SyncBase fetches pr.base_branch from pr.remote and fast-forwards the local
// base branch to it. It never pushes, merges, rewrites, deletes, or switches
// branches.
func SyncBase(ctx context.Context, svc *service.Service, cfg *config.Config, root string, opts SyncOptions) (SyncResult, error) {
	remote, branch := cfg.PR.Remote, cfg.PR.BaseBranch
	res := SyncResult{Remote: remote, BaseBranch: branch, DryRun: opts.DryRun, Warnings: []string{}, NewlyMerged: []MergedMilestone{}}

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
		return res, fmt.Errorf("a ralph loop holds %s: %s; syncing can move the checked-out branch, so wait for it to finish", lockPath, who)
	}

	repo, err := ResolveRepo(ctx, root)
	if err != nil {
		return res, err
	}
	st, err := fetchAndCompare(ctx, repo, remote, branch)
	var fe *fetchError
	if errors.As(err, &fe) {
		return res, fmt.Errorf("%w: could not fetch %s/%s: %s", errs.ErrNeedsHuman, remote, branch, gitMessage(fe.err))
	}
	if errors.Is(err, git.ErrNoLocalBase) {
		return res, fmt.Errorf("base branch %s does not exist locally; create it with git before syncing", branch)
	}
	if err != nil {
		return res, err
	}
	res.State, res.Behind, res.Ahead, res.From, res.To, res.RemoteTip = st.State, st.Behind, st.Ahead, st.Local, st.Local, st.Remote

	switch st.State {
	case git.BaseAhead:
		res.Warnings = append(res.Warnings, fmt.Sprintf("%s is %d commit(s) ahead of %s/%s; nothing to sync", branch, st.Ahead, remote, branch))
	case git.BaseDiverged:
		return res, fmt.Errorf("%w: local %s and %s/%s have diverged (%d ahead, %d behind); sync only fast-forwards, so reconcile them by hand, for example git pull --rebase %s %s",
			errs.ErrNeedsHuman, branch, remote, branch, st.Ahead, st.Behind, remote, branch)
	case git.BaseBehind:
		if opts.DryRun {
			return res, res.reportMerged(ctx, svc, cfg, root, res.From, res.RemoteTip)
		}
		if err := repo.FastForwardBranch(ctx, remote, branch); err != nil {
			return res, fmt.Errorf("%w: could not fast-forward %s: %s", errs.ErrNeedsHuman, branch, gitMessage(err))
		}
		if res.To, err = repo.RevParse(ctx, "refs/heads/"+branch); err != nil {
			return res, err
		}
		res.Synced = true
		return res, res.reportMerged(ctx, svc, cfg, root, res.From, res.To)
	}
	return res, nil
}

func (r *SyncResult) reportMerged(ctx context.Context, svc *service.Service, cfg *config.Config, root, from, to string) error {
	merged, warnings, err := NewlyMerged(ctx, svc, cfg, root, from, to)
	if err != nil {
		return err
	}
	r.NewlyMerged = merged
	r.Warnings = append(r.Warnings, warnings...)
	return nil
}

// NewlyMerged lists the DONE milestones, in initiatives that are not archived,
// merged into the commit to but not into the commit from. A milestone whose
// check fails is skipped with a warning.
func NewlyMerged(ctx context.Context, svc *service.Service, cfg *config.Config, root, from, to string) ([]MergedMilestone, []string, error) {
	out, warnings := []MergedMilestone{}, []string{}
	ms, err := svc.ListMilestones(ctx, 0, service.ListOptions{})
	if err != nil {
		return nil, nil, err
	}
	type candidate struct {
		n  int64
		id string
	}
	var cands []candidate
	for _, m := range ms {
		if m.Status != service.MilestoneDone {
			continue
		}
		n, err := domain.ParseID(domain.KindMilestone, m.ID)
		if err != nil {
			return nil, nil, err
		}
		cands = append(cands, candidate{n, m.ID})
	}
	slices.SortFunc(cands, func(a, b candidate) int { return cmp.Compare(a.n, b.n) })
	for _, c := range cands {
		in, err := DriftInput(ctx, svc, cfg, root, c.n)
		if err == nil {
			var after, before drift.Report
			in.BaseRef = to
			after, err = drift.Check(ctx, in)
			if err == nil && after.State == drift.StateMerged {
				in.BaseRef = from
				before, err = drift.Check(ctx, in)
				if err == nil && before.State != drift.StateMerged {
					out = append(out, MergedMilestone{ID: c.id, Branch: after.Branch, BranchExists: after.BranchExists, MergedBy: after.MergedBy})
				}
			}
		}
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("could not check %s: %v", c.id, err))
		}
	}
	return out, warnings, nil
}
