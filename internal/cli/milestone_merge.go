package cli

import (
	"github.com/spf13/cobra"

	"github.com/bengearig/ralph/internal/domain"
	"github.com/bengearig/ralph/internal/loop"
)

func (a *app) newMilestoneMergeCommand() *cobra.Command {
	var dryRun, deleteBranch bool
	cmd := &cobra.Command{
		Use:   "merge <id>",
		Short: "Fast-forward a finished milestone into the base branch",
		Long: `Fast-forwards the base branch to the milestone's branch, for a milestone whose
pull request was not merged on the hosting service.

The milestone must be DONE with no drift (see ralph milestone reconcile), and
the working tree must be clean. Merging never creates a merge commit, never
pushes, and never force-deletes. If a fast-forward is not possible, merge says
why; rebase the branch onto the base branch first. The base branch is left
checked out. It takes no action while a loop is running.

--delete-branch removes the milestone's branch after a successful merge, with
git's safe delete only; if git refuses, the branch is kept and a warning says
so. --dry-run runs every check and prints what would happen, changing nothing.

Merging is a person's action, so there is no MCP tool for it.

Exit codes: 0 on success or when the milestone is already merged, 3 when it
needs a person, 1 on errors.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := parseIDArg(domain.KindMilestone, args[0])
			if err != nil {
				return err
			}
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
			res, err := loop.Merge(cmd.Context(), svc, l.Config, p.Root, id, loop.MergeOptions{DeleteBranch: deleteBranch, DryRun: dryRun})
			if err != nil {
				return err
			}
			if a.jsonOut {
				return a.writeJSON(res)
			}
			for _, w := range res.Warnings {
				a.warn(w)
			}
			if res.AlreadyMerged {
				return a.printf("%s is already merged into %s\n", res.Milestone, res.BaseBranch)
			}
			verb := "merged"
			if res.DryRun {
				verb = "would merge"
			}
			if err := a.printf("%s %s into %s: %s..%s\n", verb, res.Milestone, res.BaseBranch, shortCommit(res.From), shortCommit(res.To)); err != nil {
				return err
			}
			var branchLine string
			switch {
			case res.DryRun && deleteBranch:
				branchLine = "would delete branch " + res.Branch
			case res.DryRun:
				branchLine = "would keep branch " + res.Branch
			case res.BranchDeleted:
				branchLine = "deleted branch " + res.Branch
			default:
				branchLine = "branch " + res.Branch + " kept"
			}
			if err := a.printf("%s\n", branchLine); err != nil {
				return err
			}
			if res.DryRun {
				return a.printf("dry run: nothing changed\n")
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "print what would happen and change nothing")
	cmd.Flags().BoolVar(&deleteBranch, "delete-branch", false, "after merging, delete the milestone's branch (safe delete only)")
	return cmd
}
