package cli

import (
	"github.com/spf13/cobra"

	"github.com/bengearig/ralph/internal/git"
	"github.com/bengearig/ralph/internal/loop"
)

func (a *app) newSyncCommand() *cobra.Command {
	var dryRun bool
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
			res, err := loop.SyncBase(cmd.Context(), l.Config, p.Root, loop.SyncOptions{DryRun: dryRun})
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
				return a.printf("fast-forwarded %s to %s: %s..%s (%d commit(s))\n", res.BaseBranch, upstream, shortCommit(res.From), shortCommit(res.To), res.Behind)
			case res.DryRun && res.State == git.BaseBehind:
				return a.printf("would fast-forward %s to %s: %s..%s (%d commit(s))\n", res.BaseBranch, upstream, shortCommit(res.From), shortCommit(res.RemoteTip), res.Behind)
			case res.State == git.BaseCurrent:
				return a.printf("%s is already up to date with %s\n", res.BaseBranch, upstream)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "fetch and check, then print what would happen without moving the branch")
	return cmd
}
