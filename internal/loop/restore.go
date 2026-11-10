package loop

import (
	"context"
	"errors"

	"github.com/bengearig/ralph/internal/git"
)

// startState is where the work tree stood before the run touched it.
type startState struct {
	branch string // empty when HEAD was detached
	commit string
}

func (s startState) label() string {
	if s.branch != "" {
		return s.branch
	}
	return shortHash(s.commit)
}

// readStart records the checked-out branch and commit, or reports false when
// they cannot be read.
func readStart(ctx context.Context, root string) (git.Repo, startState, bool) {
	repo, err := ResolveRepo(ctx, root)
	if err != nil {
		return git.Repo{}, startState{}, false
	}
	branch, commit, err := repo.HeadState(ctx)
	if err != nil {
		return git.Repo{}, startState{}, false
	}
	return repo, startState{branch: branch, commit: commit}, true
}

// restoreStart returns the work tree to where the run began. It never fails
// the run: problems are reported through the feed.
func restoreStart(ctx context.Context, repo git.Repo, start startState, milestoneBranch string, f feed) {
	ctx = context.WithoutCancel(ctx)
	if start.branch == milestoneBranch {
		return
	}
	branch, commit, err := repo.HeadState(ctx)
	if err != nil {
		f.printf("! could not return to %s: %v\n", start.label(), err)
		return
	}
	if start.branch != "" && branch == start.branch || start.branch == "" && branch == "" && commit == start.commit {
		return
	}
	dirty, err := repo.Dirty(ctx)
	if err != nil {
		f.printf("! could not return to %s: %v\n", start.label(), err)
		return
	}
	if len(dirty) > 0 {
		f.printf("! left on %s: uncommitted changes remain (switch back to %s yourself)\n", currentLabel(branch, commit), start.label())
		return
	}
	if err := switchToStart(ctx, repo, start); err != nil {
		f.printf("! could not return to %s: %v\n", start.label(), err)
		return
	}
	f.printf("returned to %s\n", start.label())
}

func currentLabel(branch, commit string) string {
	if branch != "" {
		return branch
	}
	return shortHash(commit)
}

var errStartGone = errors.New("it no longer exists")

// switchToStart checks out the start branch, or its commit detached when the
// branch is gone.
func switchToStart(ctx context.Context, repo git.Repo, start startState) error {
	if start.branch != "" {
		exists, err := repo.BranchExists(ctx, start.branch)
		if err != nil {
			return err
		}
		if exists {
			return repo.Switch(ctx, start.branch)
		}
	}
	exists, err := repo.CommitExists(ctx, start.commit)
	if err != nil {
		return err
	}
	if !exists {
		return errStartGone
	}
	return repo.SwitchDetached(ctx, start.commit)
}
