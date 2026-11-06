package cli

import (
	"strings"

	"github.com/spf13/cobra"

	"github.com/bengearig/ralph/internal/domain"
	"github.com/bengearig/ralph/internal/loop"
)

func (a *app) newLoopCommand() *cobra.Command {
	var milestone string
	var dryRun, showPrompt bool
	cmd := &cobra.Command{
		Use:   "loop",
		Short: "Run the RALPH loop over a milestone's TODO tasks",
		Long: `Runs one fresh, headless harness session per task until the milestone's tasks
are done, a task stalls, or loop.max_iterations is reached. Work happens on the
milestone's branch (loop.branch_template).

Execution is not implemented yet; --dry-run shows exactly what would run.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			mID, err := parseIDArg(domain.KindMilestone, milestone)
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
			plan, err := loop.Planner{Service: svc, Config: l.Config, Root: p.Root}.Plan(cmd.Context(), mID)
			if err != nil {
				return err
			}
			if !dryRun {
				for _, w := range plan.Warnings {
					a.warn(w)
				}
				return loop.Run(cmd.Context(), plan)
			}
			if a.jsonOut {
				return a.writeJSON(plan)
			}
			return a.printPlan(plan, showPrompt)
		},
	}
	cmd.Flags().StringVar(&milestone, "milestone", "", "milestone ID, e.g. M-1 (required)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "print the plan without running anything")
	cmd.Flags().BoolVar(&showPrompt, "show-prompt", false, "with --dry-run, include the first iteration's prompt")
	_ = cmd.MarkFlagRequired("milestone")
	return cmd
}

func (a *app) printPlan(plan loop.Plan, showPrompt bool) error {
	model := plan.Model
	if model == "" {
		model = "(harness default)"
	}
	if err := a.printf("milestone:  %s  %s\ninitiative: %s  %s\nbranch:     %s (from %s)\nharness:    %s, model %s\ncommand:    %s  < prompt on stdin\npolicy:     max %d iterations, stall after %d failures, commit per task: %t\n",
		plan.Milestone.ID, plan.Milestone.Title,
		plan.Initiative.ID, plan.Initiative.Title,
		plan.Branch, plan.BaseBranch,
		plan.Harness, model,
		strings.Join(plan.Command, " "),
		plan.MaxIterations, plan.StallThreshold, plan.CommitPerTask); err != nil {
		return err
	}
	for _, w := range plan.Warnings {
		a.warn(w)
	}
	if len(plan.Tasks) == 0 {
		return a.printf("\nnothing to loop: no IN_PROGRESS or TODO tasks\n")
	}
	if err := a.printTaskTable(plan.Tasks); err != nil {
		return err
	}
	if showPrompt {
		return a.printf("\n--- first iteration prompt ---\n%s", plan.FirstPrompt)
	}
	return nil
}
