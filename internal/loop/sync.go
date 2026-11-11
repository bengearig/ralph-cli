package loop

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"

	"github.com/bengearig/ralph/internal/config"
	"github.com/bengearig/ralph/internal/errs"
	"github.com/bengearig/ralph/internal/git"
	"github.com/bengearig/ralph/internal/looplock"
	"github.com/bengearig/ralph/internal/project"
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
	// RemoteTip is the fetched remote tip; a dry run reports it as the target.
	RemoteTip string `json:"-"`
}

// SyncBase fetches pr.base_branch from pr.remote and fast-forwards the local
// base branch to it. It never pushes, merges, rewrites, deletes, or switches
// branches.
func SyncBase(ctx context.Context, cfg *config.Config, root string, opts SyncOptions) (SyncResult, error) {
	remote, branch := cfg.PR.Remote, cfg.PR.BaseBranch
	res := SyncResult{Remote: remote, BaseBranch: branch, DryRun: opts.DryRun, Warnings: []string{}}

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
			return res, nil
		}
		if err := repo.FastForwardBranch(ctx, remote, branch); err != nil {
			return res, fmt.Errorf("%w: could not fast-forward %s: %s", errs.ErrNeedsHuman, branch, gitMessage(err))
		}
		if res.To, err = repo.RevParse(ctx, "refs/heads/"+branch); err != nil {
			return res, err
		}
		res.Synced = true
	}
	return res, nil
}
