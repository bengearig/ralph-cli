package loop

import (
	"context"
	"errors"
	"fmt"

	"github.com/bengearig/ralph/internal/config"
	"github.com/bengearig/ralph/internal/errs"
	"github.com/bengearig/ralph/internal/git"
)

// BaseGuardOptions selects how EnsureFreshBase behaves.
type BaseGuardOptions struct {
	// Force skips the guard, fetch included.
	Force bool
	// DryRun fetches but turns every refusal into a warning and never syncs.
	DryRun bool
}

// BaseGuardResult reports what EnsureFreshBase found and did.
type BaseGuardResult struct {
	Skipped  bool
	Status   git.BaseStatus
	Synced   bool
	Warnings []string
	Notices  []string
}

// EnsureFreshBase fetches pr.base_branch from pr.remote and checks that the
// local base branch is not behind or diverged, refusing with errs.ErrNeedsHuman
// otherwise. With pr.stale_base set to sync, a behind base is fast-forwarded.
func EnsureFreshBase(ctx context.Context, cfg *config.Config, root string, opts BaseGuardOptions) (BaseGuardResult, error) {
	var res BaseGuardResult
	if opts.Force {
		res.Skipped = true
		return res, nil
	}
	remote, branch := cfg.PR.Remote, cfg.PR.BaseBranch
	refuse := func(format string, args ...any) (BaseGuardResult, error) {
		msg := fmt.Sprintf(format, args...)
		if opts.DryRun {
			res.Warnings = append(res.Warnings, "would refuse: "+msg)
			return res, nil
		}
		return res, fmt.Errorf("%w: %s", errs.ErrNeedsHuman, msg)
	}
	repo, err := ResolveRepo(ctx, root)
	if err != nil {
		if opts.DryRun {
			return refuse("%s", err)
		}
		return res, err
	}
	if err := repo.FetchBranch(ctx, remote, branch); err != nil {
		return refuse("could not fetch %s/%s: %s; fix the remote, or use --force to skip this check", remote, branch, gitMessage(err))
	}
	st, err := repo.CompareBase(ctx, remote, branch)
	if errors.Is(err, git.ErrNoLocalBase) {
		return res, nil
	}
	if err != nil {
		return res, err
	}
	res.Status = st
	switch st.State {
	case git.BaseDiverged:
		return refuse("local %s and %s/%s have diverged (%d ahead, %d behind); reconcile them by hand (for example git pull --rebase %s %s), or use --force",
			branch, remote, branch, st.Ahead, st.Behind, remote, branch)
	case git.BaseBehind:
		if cfg.PR.StaleBase != config.StaleBaseSync {
			return refuse("local %s is %d commit(s) behind %s/%s; fast-forward it with: git pull --ff-only %s %s, or set pr.stale_base to sync, or use --force",
				branch, st.Behind, remote, branch, remote, branch)
		}
		if opts.DryRun {
			res.Warnings = append(res.Warnings, fmt.Sprintf("would fast-forward %s to %s/%s (%d commit(s))", branch, remote, branch, st.Behind))
			return res, nil
		}
		if err := repo.FastForwardBranch(ctx, remote, branch); err != nil {
			return refuse("could not fast-forward %s: %s; fix it by hand: git pull --ff-only %s %s, or use --force", branch, gitMessage(err), remote, branch)
		}
		res.Synced = true
		res.Notices = append(res.Notices, fmt.Sprintf("fast-forwarded %s to %s/%s (%d commit(s))", branch, remote, branch, st.Behind))
	}
	return res, nil
}

func gitMessage(err error) string {
	var ge *git.Error
	if errors.As(err, &ge) {
		return ge.Msg
	}
	return err.Error()
}
