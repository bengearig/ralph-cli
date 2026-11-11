package cli

import (
	"github.com/spf13/cobra"

	"github.com/bengearig/ralph/internal/git"
	"github.com/bengearig/ralph/internal/loop"
)

func (a *app) newSyncCommand() *cobra.Command {
	var dryRun, deleteBranches bool
	cmd := &cobra.Command{
		Use:   "sync",
		Short: "Fast-forward the base branch to its remote",
		Long: `Brings the local base branch (pr.base_branch) up to date with its remote
(pr.remote) by fast-forward only.

Sync fetches only that one branch from that one remote, then fast-forwards the
local branch to it. It never pushes, never creates a merge commit, never
rewrites history, never deletes anything, and never switches branches. A local
base branch that is ahead of the remote is left alone with a warning. If the
branches have diverged, or the base branch is checked out with uncommitted
changes, sync says why and changes nothing. It takes no action while a loop is
running.

--dry-run makes every check, the fetch included (which updates only the
remote-tracking branch), and prints what would happen without moving the base
branch.

--delete-branches removes the local branches of the milestones this sync
merged. Only those branches are considered, with git's safe delete, locally
only; a branch git refuses to delete (for example a squash-merged branch git
does not see as merged) is kept with a warning.

Syncing is a person's action, so there is no MCP tool for it.

Exit codes: 0 when synced or already current, 3 when it needs a person, 1 on
errors.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := a.findProject()
			if err != nil {
				return err
			}
			l, err := a.loadConfig()
			if err != nil {
				return err
			}
			svc, err := a.service(cmd.Context())
			if err != nil {
				return err
			}
			res, err := loop.SyncBase(cmd.Context(), svc, l.Config, p.Root, loop.SyncOptions{DryRun: dryRun, DeleteBranches: deleteBranches})
			if err != nil {
				return err
			}
			if a.jsonOut {
				return a.writeJSON(res)
			}
			for _, w := range res.Warnings {
				a.warn(w)
			}
			upstream := res.Remote + "/" + res.BaseBranch
			switch {
			case res.Synced:
				if err := a.printf("fast-forwarded %s to %s: %s..%s (%d commit(s))\n", res.BaseBranch, upstream, shortCommit(res.From), shortCommit(res.To), res.Behind); err != nil {
					return err
				}
				return a.printMerged(res, "merged:", deleteBranches)
			case res.DryRun && res.State == git.BaseBehind:
				if err := a.printf("would fast-forward %s to %s: %s..%s (%d commit(s))\n", res.BaseBranch, upstream, shortCommit(res.From), shortCommit(res.RemoteTip), res.Behind); err != nil {
					return err
				}
				return a.printMerged(res, "would be merged:", deleteBranches)
			case res.State == git.BaseCurrent:
				return a.printf("%s is already up to date with %s\n", res.BaseBranch, upstream)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "fetch and check, then print what would happen without moving the branch")
	cmd.Flags().BoolVar(&deleteBranches, "delete-branches", false, "remove the local branches of the milestones this sync brought in, with git's safe delete")
	return cmd
}

func (a *app) printMerged(res loop.SyncResult, verb string, deleteBranches bool) error {
	for _, m := range res.NewlyMerged {
		kept := "already deleted"
		if m.BranchExists {
			kept = "kept locally"
		}
		if err := a.printf("%s %s (branch %s, %s)\n", verb, m.ID, m.Branch, kept); err != nil {
			return err
		}
		if !deleteBranches || !m.BranchExists {
			continue
		}
		var line string
		switch {
		case res.DryRun && m.Branch == res.BaseBranch:
			line = "would keep branch " + m.Branch
		case res.DryRun:
			line = "would delete branch " + m.Branch
		case m.BranchDeleted:
			line = "deleted branch " + m.Branch
		default:
			line = "kept branch " + m.Branch
		}
		if err := a.printf("  %s\n", line); err != nil {
			return err
		}
	}
	return nil
}
