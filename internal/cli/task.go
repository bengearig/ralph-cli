package cli

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/bengearig/ralph/internal/domain"
	"github.com/bengearig/ralph/internal/service"
)

func (a *app) newTaskCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "task",
		Short: "Manage tasks (PROPOSED → TODO → IN_PROGRESS → DONE)",
	}

	var milestone, title, description, criteria string
	var position int
	create := &cobra.Command{
		Use:   "create",
		Short: "Propose a task in a milestone",
		Long:  "Tasks start PROPOSED. Promote one to TODO with `ralph task status <id> TODO` once it has a description and acceptance criteria.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			mID, err := parseIDArg(domain.KindMilestone, milestone)
			if err != nil {
				return err
			}
			svc, err := a.service(cmd.Context())
			if err != nil {
				return err
			}
			v, err := svc.CreateTask(cmd.Context(), service.TaskInput{
				MilestoneID: mID, Title: title, Description: description, AcceptanceCriteria: criteria, Position: position,
			})
			if err != nil {
				return err
			}
			a.warn(v.IndexWarning)
			if a.jsonOut {
				return a.writeJSON(v)
			}
			return a.printf("created %s (%s)\n", v.ID, v.Status)
		},
	}
	create.Flags().StringVar(&milestone, "milestone", "", "milestone ID, e.g. M-1 (required)")
	create.Flags().StringVar(&title, "title", "", "title (required)")
	create.Flags().StringVar(&description, "description", "", "what to do and how")
	create.Flags().StringVar(&criteria, "criteria", "", "acceptance criteria: how to tell it is done")
	create.Flags().IntVar(&position, "position", 0, "order within the milestone (default: last)")
	_ = create.MarkFlagRequired("milestone")
	_ = create.MarkFlagRequired("title")

	var listMilestone, listInitiative string
	var listStatus []string
	list := &cobra.Command{
		Use:   "list",
		Short: "List tasks in loop order",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			mID, err := parseIDFlag(domain.KindMilestone, listMilestone)
			if err != nil {
				return err
			}
			iID, err := parseIDFlag(domain.KindInitiative, listInitiative)
			if err != nil {
				return err
			}
			statuses, err := parseStatuses(listStatus)
			if err != nil {
				return err
			}
			svc, err := a.service(cmd.Context())
			if err != nil {
				return err
			}
			tasks, err := svc.ListTasks(cmd.Context(), service.TaskFilter{MilestoneID: mID, InitiativeID: iID, Statuses: statuses})
			if err != nil {
				return err
			}
			if a.jsonOut {
				return a.writeJSON(tasks)
			}
			return a.printTaskTable(tasks)
		},
	}
	list.Flags().StringVar(&listMilestone, "milestone", "", "only this milestone")
	list.Flags().StringVar(&listInitiative, "initiative", "", "only this initiative")
	list.Flags().StringSliceVar(&listStatus, "status", nil, "only these statuses (comma-separated or repeated)")

	show := &cobra.Command{
		Use:   "show <id>",
		Short: "Show a task with its notes",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := parseIDArg(domain.KindTask, args[0])
			if err != nil {
				return err
			}
			svc, err := a.service(cmd.Context())
			if err != nil {
				return err
			}
			v, err := svc.GetTask(cmd.Context(), id)
			if err != nil {
				return err
			}
			if a.jsonOut {
				return a.writeJSON(v)
			}
			initiative, err := taskInitiative(cmd.Context(), svc, v)
			if err != nil {
				return err
			}
			return a.printTask(v, initiative)
		},
	}

	var upTitle, upDescription, upCriteria string
	var upPosition int
	update := &cobra.Command{
		Use:   "update <id>",
		Short: "Edit a task's text or position",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := parseIDArg(domain.KindTask, args[0])
			if err != nil {
				return err
			}
			svc, err := a.service(cmd.Context())
			if err != nil {
				return err
			}
			v, err := svc.UpdateTask(cmd.Context(), id, service.TaskPatch{
				Title:              optString(cmd, "title", upTitle),
				Description:        optString(cmd, "description", upDescription),
				AcceptanceCriteria: optString(cmd, "criteria", upCriteria),
				Position:           optInt(cmd, "position", upPosition),
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
	update.Flags().StringVar(&upCriteria, "criteria", "", "new acceptance criteria")
	update.Flags().IntVar(&upPosition, "position", 0, "new position")

	var reason string
	status := &cobra.Command{
		Use:   "status <id> <status>",
		Short: "Move a task to a new status",
		Long:  transitionHelp(),
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := parseIDArg(domain.KindTask, args[0])
			if err != nil {
				return err
			}
			to, err := domain.ParseStatus(args[1])
			if err != nil {
				return err
			}
			svc, err := a.service(cmd.Context())
			if err != nil {
				return err
			}
			v, err := svc.SetTaskStatus(cmd.Context(), id, to, reason)
			if err != nil {
				return err
			}
			if a.jsonOut {
				return a.writeJSON(v)
			}
			if v.Status != string(to) {
				a.warn(v.BlockedReason)
			}
			return a.printf("%s is now %s\n", v.ID, v.Status)
		},
	}
	status.Flags().StringVar(&reason, "reason", "", "why; recorded as a note")

	var noteKind string
	note := &cobra.Command{
		Use:   "note <id> <text>",
		Short: "Add a note to a task",
		Args:  cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := parseIDArg(domain.KindTask, args[0])
			if err != nil {
				return err
			}
			kind, err := domain.ParseNoteKind(noteKind)
			if err != nil {
				return err
			}
			svc, err := a.service(cmd.Context())
			if err != nil {
				return err
			}
			v, err := svc.AddTaskNote(cmd.Context(), id, kind, strings.Join(args[1:], " "))
			if err != nil {
				return err
			}
			if a.jsonOut {
				return a.writeJSON(v)
			}
			return a.printf("noted on %s\n", v.ID)
		},
	}
	note.Flags().StringVar(&noteKind, "kind", string(domain.NoteGeneral), "NOTE, KICKBACK, or ITERATION")

	var nextMilestone string
	next := &cobra.Command{
		Use:   "next",
		Short: "Show the task the loop would pick next in a milestone",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			mID, err := parseIDArg(domain.KindMilestone, nextMilestone)
			if err != nil {
				return err
			}
			svc, err := a.service(cmd.Context())
			if err != nil {
				return err
			}
			v, err := svc.NextTask(cmd.Context(), mID)
			if err != nil {
				return err
			}
			if a.jsonOut {
				return a.writeJSON(v)
			}
			if v == nil {
				return a.printf("no IN_PROGRESS or TODO tasks in %s\n", domain.FormatID(domain.KindMilestone, mID))
			}
			initiative, err := taskInitiative(cmd.Context(), svc, *v)
			if err != nil {
				return err
			}
			return a.printTask(*v, initiative)
		},
	}
	next.Flags().StringVar(&nextMilestone, "milestone", "", "milestone ID (required)")
	_ = next.MarkFlagRequired("milestone")

	cmd.AddCommand(create, list, show, update, status, note, next, a.newDeleteCommand(domain.KindTask, true, "Delete a task and its notes. Without --force the task must not be IN_PROGRESS or DONE."))
	return cmd
}

func (a *app) printTaskTable(tasks []service.TaskView) error {
	if len(tasks) == 0 {
		return a.printf("\nno tasks\n")
	}
	rows := make([][]string, 0, len(tasks))
	for _, t := range tasks {
		fails := ""
		if t.FailureCount > 0 {
			fails = strconv.Itoa(t.FailureCount)
		}
		rows = append(rows, []string{t.ID, t.MilestoneID, strconv.Itoa(t.Position), t.Status, fails, t.Title})
	}
	if err := a.printf("\n"); err != nil {
		return err
	}
	return a.writeTable([]string{"ID", "MILESTONE", "POS", "STATUS", "FAILS", "TITLE"}, rows)
}

// taskInitiative names a task's initiative, looked up only when it is archived.
func taskInitiative(ctx context.Context, svc *service.Service, v service.TaskView) (string, error) {
	if !v.Archived {
		return "", nil
	}
	mID, err := parseIDArg(domain.KindMilestone, v.MilestoneID)
	if err != nil {
		return "", err
	}
	m, err := svc.GetMilestone(ctx, mID)
	if err != nil {
		return "", err
	}
	return m.InitiativeID, nil
}

func (a *app) printTask(v service.TaskView, initiative string) error {
	if err := a.printf("%s  %s\nmilestone: %s\nstatus: %s (next: %s)\nposition: %d\nfailures: %d\n",
		v.ID, v.Title, v.MilestoneID, v.Status, orNone(strings.Join(v.AllowedNext, ", ")), v.Position, v.FailureCount); err != nil {
		return err
	}
	if err := a.printf("%s", archivedLine("", v.Archived, initiative)); err != nil {
		return err
	}
	if v.Commit != "" {
		if err := a.printf("commit: %s\n", shortCommit(v.Commit)); err != nil {
			return err
		}
	}
	if v.BlockedReason != "" {
		if err := a.printf("blocked: %s\n", v.BlockedReason); err != nil {
			return err
		}
	}
	if err := a.section("Description", v.Description); err != nil {
		return err
	}
	if err := a.section("Acceptance criteria", v.AcceptanceCriteria); err != nil {
		return err
	}
	if len(v.Notes) > 0 {
		var b strings.Builder
		for _, n := range v.Notes {
			b.WriteString(n.CreatedAt + " " + n.ID + " " + n.Kind + ": " + n.Body + "\n")
		}
		return a.section("Notes", b.String())
	}
	return nil
}

func orNone(s string) string {
	if s == "" {
		return "none"
	}
	return s
}

func transitionHelp() string {
	var b strings.Builder
	b.WriteString("Move a task through the lifecycle. Allowed moves:\n")
	for _, st := range domain.Statuses() {
		next := st.AllowedNext()
		names := make([]string, len(next))
		for i, n := range next {
			names[i] = string(n)
		}
		fmt.Fprintf(&b, "  %-12s → %s\n", st, orNone(strings.Join(names, ", ")))
	}
	b.WriteString("TODO requires a description and acceptance criteria; BLOCKED requires --reason.")
	return b.String()
}
