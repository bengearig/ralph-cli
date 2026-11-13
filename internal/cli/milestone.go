package cli

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/bengearig/ralph/internal/domain"
	"github.com/bengearig/ralph/internal/drift"
	"github.com/bengearig/ralph/internal/errs"
	"github.com/bengearig/ralph/internal/loop"
	"github.com/bengearig/ralph/internal/service"
)

func (a *app) newMilestoneCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "milestone",
		Short: "Manage milestones (each becomes one branch and pull request)",
	}

	var initiative, title, slug, description string
	var position int
	var dependsOn []string
	create := &cobra.Command{
		Use:   "create",
		Short: "Add a milestone to an initiative",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			iID, err := parseIDArg(domain.KindInitiative, initiative)
			if err != nil {
				return err
			}
			deps, err := parseDependsOn(dependsOn)
			if err != nil {
				return err
			}
			svc, err := a.service(cmd.Context())
			if err != nil {
				return err
			}
			v, err := svc.CreateMilestone(cmd.Context(), service.MilestoneInput{
				InitiativeID: iID, Title: title, Slug: slug, Description: description, Position: position, DependsOn: deps,
			})
			if err != nil {
				return err
			}
			a.warn(v.IndexWarning)
			a.warn(v.CardWarning)
			if a.jsonOut {
				return a.writeJSON(v)
			}
			return a.printf("created %s %s\n", v.ID, v.Slug)
		},
	}
	create.Flags().StringVar(&initiative, "initiative", "", "initiative ID, e.g. I-1 (required)")
	create.Flags().StringVar(&title, "title", "", "title (required)")
	create.Flags().StringVar(&slug, "slug", "", "slug (default: derived from title)")
	create.Flags().StringVar(&description, "description", "", "what this milestone delivers")
	create.Flags().IntVar(&position, "position", 0, "order within the initiative (default: last)")
	create.Flags().StringSliceVar(&dependsOn, "depends-on", nil, "milestones this one depends on, e.g. M-15,M-16 (repeatable)")
	_ = create.MarkFlagRequired("initiative")
	_ = create.MarkFlagRequired("title")

	var listInitiative string
	var listArchived bool
	list := &cobra.Command{
		Use:   "list",
		Short: "List milestones with derived status",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			iID, err := parseIDFlag(domain.KindInitiative, listInitiative)
			if err != nil {
				return err
			}
			svc, err := a.service(cmd.Context())
			if err != nil {
				return err
			}
			ms, err := svc.ListMilestones(cmd.Context(), iID, service.ListOptions{IncludeArchived: listArchived})
			if err != nil {
				return err
			}
			if a.jsonOut {
				return a.writeJSON(ms)
			}
			rows := make([][]string, 0, len(ms))
			cards := make([]*service.CardView, 0, len(ms))
			for _, m := range ms {
				rows = append(rows, []string{m.ID, m.InitiativeID, strconv.Itoa(m.Position), m.Status, m.Title, service.FormatCounts(m.TaskCounts)})
				cards = append(cards, m.Card)
			}
			header, rows := a.withCard([]string{"ID", "INITIATIVE", "POS", "STATUS", "TITLE", "TASKS"}, rows, cards)
			return a.writeTable(header, rows)
		},
	}
	list.Flags().StringVar(&listInitiative, "initiative", "", "only this initiative")
	list.Flags().BoolVar(&listArchived, "archived", false, "include archived initiatives and documents, and their contents")

	show := &cobra.Command{
		Use:   "show <id>",
		Short: "Show a milestone and its tasks",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := parseIDArg(domain.KindMilestone, args[0])
			if err != nil {
				return err
			}
			svc, err := a.service(cmd.Context())
			if err != nil {
				return err
			}
			v, err := a.fetchMilestoneShowWaiting(cmd.Context(), svc, id)
			if err != nil {
				return err
			}
			if a.jsonOut {
				return a.writeJSON(v)
			}
			return a.renderMilestoneShow(v)
		},
	}

	var upTitle, upDescription string
	var upPosition int
	var upDependsOn []string
	var upNoDependsOn bool
	update := &cobra.Command{
		Use:   "update <id>",
		Short: "Edit a milestone",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := parseIDArg(domain.KindMilestone, args[0])
			if err != nil {
				return err
			}
			if cmd.Flags().Changed("depends-on") && upNoDependsOn {
				return fmt.Errorf("%w: use either --depends-on or --no-depends-on, not both", domain.ErrInvalid)
			}
			var deps *[]int64
			if cmd.Flags().Changed("depends-on") {
				ids, err := parseDependsOn(upDependsOn)
				if err != nil {
					return err
				}
				deps = &ids
			} else if upNoDependsOn {
				deps = &[]int64{}
			}
			svc, err := a.service(cmd.Context())
			if err != nil {
				return err
			}
			v, err := svc.UpdateMilestone(cmd.Context(), id, service.MilestonePatch{
				Title:       optString(cmd, "title", upTitle),
				Description: optString(cmd, "description", upDescription),
				Position:    optInt(cmd, "position", upPosition),
				DependsOn:   deps,
			})
			if err != nil {
				return err
			}
			a.warn(v.IndexWarning)
			if a.jsonOut {
				return a.writeJSON(v)
			}
			return a.printf("updated %s\n", v.ID)
		},
	}
	update.Flags().StringVar(&upTitle, "title", "", "new title")
	update.Flags().StringVar(&upDescription, "description", "", "new description")
	update.Flags().IntVar(&upPosition, "position", 0, "new position")
	update.Flags().StringSliceVar(&upDependsOn, "depends-on", nil, "replace the milestones this one depends on, e.g. M-15,M-16 (repeatable)")
	update.Flags().BoolVar(&upNoDependsOn, "no-depends-on", false, "remove all dependencies")

	cmd.AddCommand(create, list, show, a.newMilestoneWatchCommand(), update, a.newMilestoneReconcileCommand(), a.newMilestoneMergeCommand(), a.newDeleteCommand(domain.KindMilestone, true, "Delete a milestone. Without --force it must have no tasks; with --force its tasks and their notes are deleted too."))
	return cmd
}

// parseDependsOn parses --depends-on values; blank entries are skipped.
func parseDependsOn(raw []string) ([]int64, error) {
	ids := []int64{}
	for _, r := range raw {
		if strings.TrimSpace(r) == "" {
			continue
		}
		id, err := domain.ParseID(domain.KindMilestone, r)
		if err != nil {
			return nil, fmt.Errorf("--depends-on: %w", err)
		}
		ids = append(ids, id)
	}
	return ids, nil
}

func (a *app) newMilestoneReconcileCommand() *cobra.Command {
	var branch, reason string
	var reopen []string
	var accept bool
	cmd := &cobra.Command{
		Use:   "reconcile <id>",
		Short: "Compare a milestone's branch with its record and resolve drift",
		Long: `Checks how the milestone's branch differs from its recorded git state and
where each DONE task's work is now, and prints the report. Without flags it
changes nothing.

Flags apply in this order: --branch points the record at an existing local
branch; --reopen sends DONE tasks whose work was lost back to TODO (--reason
is required); --accept records the branch tip, the base and each DONE task's
matched commit. A task with no matching commit stays DONE without a commit.
Everything is validated first; if anything fails, nothing is changed.

Exit codes: 0 when the branch matches its record (state unchanged), 3 when
it still needs a person, 1 on errors.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := parseIDArg(domain.KindMilestone, args[0])
			if err != nil {
				return err
			}
			if cmd.Flags().Changed("branch") && strings.TrimSpace(branch) == "" {
				return fmt.Errorf("%w: --branch must not be empty", domain.ErrInvalid)
			}
			actions := drift.Actions{Branch: strings.TrimSpace(branch), Reason: reason, Accept: accept}
			for _, r := range reopen {
				if strings.TrimSpace(r) == "" {
					continue
				}
				tID, err := domain.ParseID(domain.KindTask, r)
				if err != nil {
					return fmt.Errorf("--reopen: %w", err)
				}
				if !slices.Contains(actions.Reopen, tID) {
					actions.Reopen = append(actions.Reopen, tID)
				}
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
			in, err := loop.DriftInput(cmd.Context(), svc, l.Config, p.Root, id)
			if err != nil {
				return err
			}
			before, after, err := drift.Apply(cmd.Context(), in, actions)
			if err != nil {
				return err
			}
			applied := !actions.Empty()
			final := before
			if applied {
				final = after
			}

			if a.jsonOut {
				out := struct {
					Before drift.Report  `json:"before"`
					After  *drift.Report `json:"after"`
				}{Before: before}
				if applied {
					out.After = &after
				}
				if err := a.writeJSON(out); err != nil {
					return err
				}
			} else {
				if err := a.printDriftReport(before); err != nil {
					return err
				}
				if applied {
					if err := a.printReconcileChanges(actions, after); err != nil {
						return err
					}
					if err := a.printf("\nafter:\n"); err != nil {
						return err
					}
					if err := a.printDriftReport(after); err != nil {
						return err
					}
				}
			}
			if final.Clean() {
				return nil
			}
			return fmt.Errorf("%w: %s; %s", errs.ErrNeedsHuman, final.Summary(), reconcileNext(final))
		},
	}
	cmd.Flags().StringVar(&branch, "branch", "", "point the record at this existing local branch, e.g. after a rename")
	cmd.Flags().StringSliceVar(&reopen, "reopen", nil, "DONE tasks to send back to TODO, e.g. T-3,T-4 (repeatable)")
	cmd.Flags().StringVar(&reason, "reason", "", "why the tasks are reopened (required with --reopen)")
	cmd.Flags().BoolVar(&accept, "accept", false, "record the branch tip, base and matched task commits")
	return cmd
}

// reconcileNext says what to do about a report that is not clean.
func reconcileNext(r drift.Report) string {
	cmd := "ralph milestone reconcile " + r.Milestone
	switch r.State {
	case drift.StateBranchMissing:
		if r.RemoteBranchExists {
			return fmt.Sprintf("restore it with `git branch %s origin/%s`, or run: %s --branch <name>", r.Branch, r.Branch, cmd)
		}
		return "run: " + cmd + " --branch <name>"
	case drift.StateMerged:
		return "its work is already in the base branch; there is nothing to reconcile or open a pull request for"
	}
	var missing []string
	for _, f := range r.Tasks {
		if f.Match == drift.MatchMissing {
			missing = append(missing, f.ID)
		}
	}
	next := "review the report, then run: " + cmd + " --accept"
	if len(missing) > 0 {
		next += fmt.Sprintf(" (add --reopen %s --reason \"<why>\" to send tasks without a commit back to the loop)", strings.Join(missing, ","))
	}
	return next
}

// printDriftReport renders a drift report for people.
func (a *app) printDriftReport(r drift.Report) error {
	recorded := "nothing"
	if r.Recorded != (drift.Recorded{}) {
		recorded = branchLine(service.MilestoneView{Branch: r.Recorded.Branch, BaseCommit: r.Recorded.BaseCommit, HeadCommit: r.Recorded.HeadCommit})
	}
	tip := shortCommit(r.Tip)
	if !r.BranchExists {
		tip = "(branch does not exist locally)"
		if r.RemoteBranchExists {
			tip = fmt.Sprintf("(branch does not exist locally; origin/%s does)", r.Branch)
		}
	}
	mergedBy := ""
	if r.State == drift.StateMerged {
		mergedBy = fmt.Sprintf("merged by: %s\n", r.MergedBy)
	}
	if err := a.printf("%s\nstate:    %s\n%sbranch:   %s (base %s)\nrecorded: %s\ntip:      %s\n",
		r.Summary(), r.State, mergedBy, r.Branch, r.BaseRef, recorded, tip); err != nil {
		return err
	}
	if r.ProposedBase != "" && r.ProposedBase != r.Recorded.BaseCommit {
		if err := a.printf("proposed base: %s\n", shortCommit(r.ProposedBase)); err != nil {
			return err
		}
	}
	return a.printDriftDetails(r)
}

// printDriftDetails renders a drift report's commit lists and task table.
func (a *app) printDriftDetails(r drift.Report) error {
	if err := a.printCommits("commits beyond the recorded head", r.ExtraCommits); err != nil {
		return err
	}
	if err := a.printCommits("recorded commits no longer on the branch", r.LostCommits); err != nil {
		return err
	}
	if len(r.Tasks) == 0 {
		if err := a.printf("\nno DONE tasks\n"); err != nil {
			return err
		}
	} else {
		if err := a.printf("\n"); err != nil {
			return err
		}
		rows := make([][]string, 0, len(r.Tasks))
		for _, f := range r.Tasks {
			commit := shortCommit(f.Commit)
			if commit == "" {
				commit = "-"
			}
			rows = append(rows, []string{f.ID, string(f.Match), commit, f.Title})
		}
		if err := a.writeTable([]string{"TASK", "MATCH", "COMMIT", "TITLE"}, rows); err != nil {
			return err
		}
	}
	return a.printCommits("unattributed commits", r.UnattributedCommits)
}

func (a *app) printCommits(label string, cs []drift.Commit) error {
	if len(cs) == 0 {
		return nil
	}
	if err := a.printf("\n%s:\n", label); err != nil {
		return err
	}
	for _, c := range cs {
		if err := a.printf("  %s  %s\n", shortCommit(c.Hash), oneLine(c.Subject)); err != nil {
			return err
		}
	}
	return nil
}

// printReconcileChanges lists what Apply changed, read from the report after.
func (a *app) printReconcileChanges(actions drift.Actions, after drift.Report) error {
	lines := []string{}
	if actions.Branch != "" {
		lines = append(lines, "recorded branch "+actions.Branch)
	}
	for _, id := range actions.Reopen {
		lines = append(lines, "reopened "+domain.FormatID(domain.KindTask, id)+" (now TODO)")
	}
	if actions.Accept {
		rec := after.Recorded
		line := fmt.Sprintf("accepted %s at %s", rec.Branch, shortCommit(rec.HeadCommit))
		if rec.BaseCommit != "" {
			line += ", base " + shortCommit(rec.BaseCommit)
		}
		lines = append(lines, line)
		for _, f := range after.Tasks {
			if f.RecordedCommit == "" {
				lines = append(lines, f.ID+" kept DONE without a commit")
			} else {
				lines = append(lines, f.ID+" commit "+shortCommit(f.RecordedCommit))
			}
		}
	}
	if err := a.printf("\napplied:\n"); err != nil {
		return err
	}
	for _, l := range lines {
		if err := a.printf("  %s\n", l); err != nil {
			return err
		}
	}
	return nil
}

type milestoneShow struct {
	service.MilestoneView
	WaitingOn []loop.DependencyFinding `json:"waiting_on,omitempty"`
	Tasks     []service.TaskView       `json:"tasks"`

	findings []loop.DependencyFinding
}

func fetchMilestoneShow(ctx context.Context, svc *service.Service, id int64) (milestoneShow, error) {
	m, err := svc.GetMilestone(ctx, id)
	if err != nil {
		return milestoneShow{}, err
	}
	tasks, err := svc.ListTasks(ctx, service.TaskFilter{MilestoneID: id, IncludeArchived: true})
	if err != nil {
		return milestoneShow{}, err
	}
	return milestoneShow{MilestoneView: m, Tasks: tasks}, nil
}

// fetchMilestoneShowWaiting adds the dependency findings, which need the project's config and git.
func (a *app) fetchMilestoneShowWaiting(ctx context.Context, svc *service.Service, id int64) (milestoneShow, error) {
	v, err := fetchMilestoneShow(ctx, svc, id)
	if err != nil {
		return v, err
	}
	if len(v.DependsOn) > 0 {
		if cfg, root, ok := a.waitingContext(); ok {
			v.findings = a.dependencyFindings(ctx, svc, cfg, root, v.MilestoneView, map[string][]loop.DependencyFinding{})
			v.WaitingOn = loop.Unsatisfied(v.findings)
		}
	}
	return v, nil
}

func (a *app) renderMilestoneShow(v milestoneShow) error {
	m := v.MilestoneView
	if err := a.printf("%s  %s\ninitiative: %s\nslug: %s\nstatus: %s\n", m.ID, m.Title, m.InitiativeID, m.Slug, m.Status); err != nil {
		return err
	}
	if err := a.printf("%s%s", cardLine(m.Card), archivedLine("", m.Archived, m.InitiativeID)); err != nil {
		return err
	}
	if m.Branch != "" {
		if err := a.printf("branch: %s\n", branchLine(m)); err != nil {
			return err
		}
	}
	if m.PullRequest != nil {
		if err := a.printf("pull request: %s\n", pullRequestLine(*m.PullRequest)); err != nil {
			return err
		}
	}
	if len(m.DependsOn) > 0 {
		if err := a.printf("depends on: %s\n", dependsOnLine(m.DependsOn, v.findings)); err != nil {
			return err
		}
	}
	if err := a.section("Description", m.Description); err != nil {
		return err
	}
	return a.renderTaskList(v.Tasks)
}
