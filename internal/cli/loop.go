package cli

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/bengearig/ralph/internal/config"
	"github.com/bengearig/ralph/internal/domain"
	"github.com/bengearig/ralph/internal/errs"
	"github.com/bengearig/ralph/internal/harness"
	"github.com/bengearig/ralph/internal/loop"
)

// newLoopHarness builds the loop's harness; tests replace it.
var newLoopHarness = harness.ForStep

func (a *app) newLoopCommand() *cobra.Command {
	var milestone, initiative string
	var dryRun, showPrompt, force, merge bool
	cmd := &cobra.Command{
		Use:   "loop (--milestone M-n | --initiative I-n)",
		Short: "Run the RALPH loop over a milestone's TODO tasks",
		Long: `Runs one fresh, headless harness session per task on the milestone's branch
(loop.branch_template), committing each task that reaches DONE. BLOCKED and
PROPOSED tasks are skipped. The loop stops when nothing loopable remains or
loop.max_iterations is reached.

Exit codes: 0 when the milestone is done, 3 when tasks need a person
(BLOCKED, PROPOSED, or the iteration limit), 1 on errors. An interrupted run
leaves its task IN_PROGRESS; rerun to resume it. Progress goes to stderr and
full transcripts to .ralph/runs/.

With --initiative I-n the loop runs the initiative's next ready milestone, one
per invocation, after checking that earlier milestones are merged or cancelled;
--dry-run shows the ordered milestones and what would happen next. It stops
(exit code 3) at the first milestone that needs a person, including a DONE one
that still needs ralph milestone merge.

The loop refuses (exit code 3) while a milestone this one depends on is not
merged; --force skips that check, and --dry-run reports it as warnings.

The base branch is fetched first, and the loop refuses (exit code 3) when the
local base is behind or diverged; --force skips that.

With --merge (only with --initiative) the loop carries on through the
initiative: it merges each finished milestone into the base branch
(fast-forward only) and runs the next, until the initiative is complete or
something needs a person. A milestone that is not DONE is never merged, and
--force never overrides a merge refusal.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := a.stopForIntegrationUpdate(cmd, dryRun); err != nil {
				return err
			}
			opts := loopOptions{dryRun: dryRun, showPrompt: showPrompt, force: force, merge: merge}
			if merge && initiative == "" {
				return errors.New("--merge requires --initiative")
			}
			if initiative != "" {
				return a.loopInitiative(cmd.Context(), initiative, opts)
			}
			mID, err := parseIDArg(domain.KindMilestone, milestone)
			if err != nil {
				return err
			}
			_, err = a.loopMilestone(cmd.Context(), mID, opts)
			return err
		},
	}
	cmd.Flags().StringVar(&milestone, "milestone", "", "milestone ID, e.g. M-1")
	cmd.Flags().StringVar(&initiative, "initiative", "", "initiative ID, e.g. I-1: run its next ready milestone")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "print the plan without running anything")
	cmd.Flags().BoolVar(&force, "force", false, "ignore unmerged milestone dependencies and the stale-base check")
	cmd.Flags().BoolVar(&merge, "merge", false, "merge each finished milestone into the base branch and continue")
	cmd.Flags().BoolVar(&showPrompt, "show-prompt", false, "with --dry-run, include the first iteration's prompt")
	cmd.MarkFlagsOneRequired("milestone", "initiative")
	cmd.MarkFlagsMutuallyExclusive("milestone", "initiative")
	return cmd
}

// stopForIntegrationUpdate refuses to run the loop when this process just
// rewrote the integration files, which would dirty the loop's working tree.
func (a *app) stopForIntegrationUpdate(cmd *cobra.Command, dryRun bool) error {
	if dryRun || a.integration == nil || len(a.integration.Updated) == 0 {
		return nil
	}
	parts := make([]string, len(a.integration.Updated))
	for i, c := range a.integration.Updated {
		parts[i] = c.Path
	}
	paths := strings.Join(parts, ", ")
	rerun := loopRerunCommand(cmd)
	a.warn("updated its integration files: " + paths + integrationIndent +
		"they must be committed before the loop can run; commit them, then rerun: " + rerun)
	return fmt.Errorf("%w: updated its integration files (%s); commit them, then rerun", errs.ErrNeedsHuman, paths)
}

// loopRerunCommand reproduces the command as typed from the flags that were set.
func loopRerunCommand(cmd *cobra.Command) string {
	parts := []string{cmd.CommandPath()}
	cmd.Flags().Visit(func(f *pflag.Flag) {
		if f.Value.Type() == "bool" {
			parts = append(parts, "--"+f.Name)
			return
		}
		parts = append(parts, "--"+f.Name, f.Value.String())
	})
	return strings.Join(parts, " ")
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
	if err := a.renderTaskList(plan.Tasks); err != nil {
		return err
	}
	if showPrompt {
		return a.printf("\n--- first iteration prompt ---\n%s", plan.FirstPrompt)
	}
	return nil
}

type loopOptions struct {
	dryRun, showPrompt, force, merge bool
	baseChecked                      bool
}

// guardBase runs the stale-base guard and prints its notices. In a dry run the
// would-refuse warnings are returned for the caller to report.
func (a *app) guardBase(ctx context.Context, cfg *config.Config, root string, force, dryRun bool) ([]string, error) {
	res, err := loop.EnsureFreshBase(ctx, cfg, root, loop.BaseGuardOptions{Force: force, DryRun: dryRun})
	if err != nil {
		return nil, err
	}
	for _, n := range res.Notices {
		a.warn(n)
	}
	return res.Warnings, nil
}

func (a *app) loopMilestone(ctx context.Context, mID int64, opts loopOptions) (loop.Outcome, error) {
	p, err := a.findProject()
	if err != nil {
		return loop.Outcome{}, err
	}
	l, err := a.loadConfig()
	if err != nil {
		return loop.Outcome{}, err
	}
	svc, err := a.service(ctx)
	if err != nil {
		return loop.Outcome{}, err
	}
	var baseWarnings []string
	if !opts.baseChecked {
		if baseWarnings, err = a.guardBase(ctx, l.Config, p.Root, opts.force, opts.dryRun); err != nil {
			return loop.Outcome{}, err
		}
	}
	plan, err := loop.Planner{Service: svc, Config: l.Config, Root: p.Root, Force: opts.force, WarnOnly: opts.dryRun}.Plan(ctx, mID)
	if err != nil {
		return loop.Outcome{}, err
	}
	plan.Warnings = append(baseWarnings, plan.Warnings...)
	if opts.dryRun {
		if a.jsonOut {
			return loop.Outcome{}, a.writeJSON(plan)
		}
		return loop.Outcome{}, a.printPlan(plan, opts.showPrompt)
	}
	for _, w := range plan.Warnings {
		a.warn(w)
	}
	h, res, err := newLoopHarness(l.Config, config.StepLoop)
	if err != nil {
		return loop.Outcome{}, err
	}
	if res.Harness == config.HarnessClaudeCode && l.Config.Harness.ClaudeCode.PermissionMode == "bypassPermissions" {
		a.warn("harness.claude_code.permission_mode is bypassPermissions: the agent can run anything unprompted; use only in a sandbox")
	}
	out, runErr := loop.Runner{Service: svc, Config: l.Config, Root: p.Root, Harness: h, Out: a.streams.Err}.Run(ctx, plan)
	if a.jsonOut {
		if err := a.writeJSON(out); err != nil {
			return out, errors.Join(runErr, err)
		}
		return out, runErr
	}
	if out.Iterations > 0 {
		if err := a.printf("%s: %s after %d iteration(s); %d done, %d commit(s), %d blocked, %d proposed, %s\n",
			out.Milestone, out.StopReason, out.Iterations, len(out.Done), len(out.Commits),
			len(out.Blocked), len(out.Proposed), loop.FormatCost(out.CostUSD)); err != nil {
			return out, errors.Join(runErr, err)
		}
	}
	return out, runErr
}

type chainNext struct {
	Action    string `json:"action"`
	Milestone string `json:"milestone,omitempty"`
	Reason    string `json:"reason,omitempty"`
	Fix       string `json:"fix,omitempty"`
}

func newChainNext(step loop.ChainStep) chainNext {
	n := chainNext{Action: step.Action}
	if step.Entry != nil {
		n.Milestone, n.Reason, n.Fix = step.Entry.Milestone, step.Entry.Reason, step.Entry.Fix
	}
	return n
}

func (n chainNext) stopMessage() string {
	return fmt.Sprintf("%s at %s: %s; %s", n.Action, n.Milestone, n.Reason, n.Fix)
}

func (a *app) loopInitiative(ctx context.Context, initiative string, opts loopOptions) error {
	iID, err := parseIDArg(domain.KindInitiative, initiative)
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
	svc, err := a.service(ctx)
	if err != nil {
		return err
	}
	iRef := domain.FormatID(domain.KindInitiative, iID)
	baseWarnings, err := a.guardBase(ctx, l.Config, p.Root, opts.force, opts.dryRun)
	if err != nil {
		return err
	}
	entries, err := chainOverview(ctx, svc, l.Config, p.Root, iID, opts.force)
	if err != nil {
		return err
	}
	if opts.merge && !opts.dryRun {
		return a.loopChain(ctx, svc, l.Config, p.Root, iID, entries, opts)
	}
	step := loop.NextStep(entries, opts.merge)
	next := newChainNext(step)
	if opts.dryRun {
		for _, w := range baseWarnings {
			a.warn(w)
		}
		if a.jsonOut {
			return a.writeJSON(struct {
				Initiative string            `json:"initiative"`
				Entries    []loop.ChainEntry `json:"entries"`
				Next       chainNext         `json:"next"`
			}{iRef, entries, next})
		}
		rows := make([][]string, 0, len(entries))
		for _, e := range entries {
			rows = append(rows, []string{e.Milestone, e.Status, e.Disposition, e.Reason})
		}
		if len(rows) > 0 {
			if err := a.writeTable([]string{"ID", "STATUS", "DISPOSITION", "REASON"}, rows); err != nil {
				return err
			}
		}
		switch step.Action {
		case loop.ActionRun, loop.ActionMerge:
			if err := a.printf("next: %s %s\n", step.Action, next.Milestone); err != nil {
				return err
			}
			if opts.merge {
				return a.printf("later steps depend on earlier ones succeeding; nothing was changed\n")
			}
			return nil
		case loop.ActionStop:
			return a.printf("next: %s\n", next.stopMessage())
		}
		return a.printf("next: complete\n")
	}
	switch step.Action {
	case loop.ActionComplete:
		return a.printf("initiative %s is complete: every milestone is merged or cancelled\n", iRef)
	case loop.ActionStop:
		msg := fmt.Sprintf("stopped at %s: %s; %s", next.Milestone, next.Reason, next.Fix)
		if err := a.printf("%s\n", msg); err != nil {
			return err
		}
		return fmt.Errorf("%w: %s", errs.ErrNeedsHuman, msg)
	}
	mID, err := parseIDArg(domain.KindMilestone, next.Milestone)
	if err != nil {
		return err
	}
	opts.baseChecked = true
	out, err := a.loopMilestone(ctx, mID, opts)
	if err == nil && out.StopReason == loop.StopCompleted && !a.jsonOut {
		return a.printf("%s is DONE; review it, then merge: ralph milestone merge %s; run ralph loop --initiative %s again for the next one\n", next.Milestone, next.Milestone, iRef)
	}
	return err
}
