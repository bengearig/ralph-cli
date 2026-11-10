package cli

import (
	"errors"
	"strings"

	"github.com/spf13/cobra"

	"github.com/bengearig/ralph/internal/config"
	"github.com/bengearig/ralph/internal/domain"
	"github.com/bengearig/ralph/internal/harness"
	"github.com/bengearig/ralph/internal/loop"
)

func (a *app) newLoopCommand() *cobra.Command {
	var milestone string
	var dryRun, showPrompt bool
	cmd := &cobra.Command{
		Use:   "loop",
		Short: "Run the RALPH loop over a milestone's TODO tasks",
		Long: `Runs one fresh, headless harness session per task on the milestone's branch
(loop.branch_template), committing each task that reaches DONE. BLOCKED and
PROPOSED tasks are skipped. The loop stops when nothing loopable remains or
loop.max_iterations is reached.

Exit codes: 0 when the milestone is done, 3 when tasks need a person
(BLOCKED, PROPOSED, or the iteration limit), 1 on errors. An interrupted run
leaves its task IN_PROGRESS; rerun to resume it. Progress goes to stderr and
full transcripts to .ralph/runs/.`,
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
			if dryRun {
				if a.jsonOut {
					return a.writeJSON(plan)
				}
				return a.printPlan(plan, showPrompt)
			}
			for _, w := range plan.Warnings {
				a.warn(w)
			}
			h, res, err := harness.ForStep(l.Config, config.StepLoop)
			if err != nil {
				return err
			}
			if res.Harness == config.HarnessClaudeCode && l.Config.Harness.ClaudeCode.PermissionMode == "bypassPermissions" {
				a.warn("harness.claude_code.permission_mode is bypassPermissions: the agent can run anything unprompted; use only in a sandbox")
			}
			out, runErr := loop.Runner{Service: svc, Config: l.Config, Root: p.Root, Harness: h, Out: a.streams.Err}.Run(cmd.Context(), plan)
			if a.jsonOut {
				if err := a.writeJSON(out); err != nil {
					return errors.Join(runErr, err)
				}
				return runErr
			}
			if out.Iterations > 0 {
				if err := a.printf("%s: %s after %d iteration(s); %d done, %d commit(s), %d blocked, %d proposed, %s\n",
					out.Milestone, out.StopReason, out.Iterations, len(out.Done), len(out.Commits),
					len(out.Blocked), len(out.Proposed), loop.FormatCost(out.CostUSD)); err != nil {
					return errors.Join(runErr, err)
				}
			}
			return runErr
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
	if err := a.printf("milestone:  %s  %s\ninitiative: %s  %s\nbranch:     %s (from %s)\nharness:    %s, model %s\ncommand:    %s  < prompt on stdin\npolicy:     max %d iterations, stall after %d failures, commit per task: %t, commit trailer: %t, commit card trailer: %t\n",
		plan.Milestone.ID, plan.Milestone.Title,
		plan.Initiative.ID, plan.Initiative.Title,
		plan.Branch, plan.BaseBranch,
		plan.Harness, model,
		strings.Join(plan.Command, " "),
		plan.MaxIterations, plan.StallThreshold, plan.CommitPerTask, plan.CommitTrailer, plan.CommitCardTrailer); err != nil {
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
