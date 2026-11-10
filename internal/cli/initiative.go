package cli

import (
	"github.com/spf13/cobra"

	"github.com/bengearig/ralph/internal/domain"
	"github.com/bengearig/ralph/internal/service"
)

func (a *app) newInitiativeCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "initiative",
		Short: "Manage initiatives (one received set of instructions each)",
	}

	var title, slug, brief, briefFile string
	create := &cobra.Command{
		Use:   "create",
		Short: "Record newly received instructions as an initiative",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			b, err := a.textFlag(cmd, "brief", brief, "brief-file", briefFile)
			if err != nil {
				return err
			}
			in := service.InitiativeInput{Title: title, Slug: slug}
			if b != nil {
				in.Brief = *b
			}
			svc, err := a.service(cmd.Context())
			if err != nil {
				return err
			}
			v, err := svc.CreateInitiative(cmd.Context(), in)
			if err != nil {
				return err
			}
			a.warn(v.IndexWarning)
			if a.jsonOut {
				return a.writeJSON(v)
			}
			return a.printf("created %s %s\n", v.ID, v.Slug)
		},
	}
	create.Flags().StringVar(&title, "title", "", "short title (required)")
	create.Flags().StringVar(&slug, "slug", "", "slug (default: derived from title)")
	create.Flags().StringVar(&brief, "brief", "", "the instructions as received")
	create.Flags().StringVar(&briefFile, "brief-file", "", "read the brief from a file, or - for stdin")
	_ = create.MarkFlagRequired("title")

	var listArchived bool
	list := &cobra.Command{
		Use:   "list",
		Short: "List initiatives",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			svc, err := a.service(cmd.Context())
			if err != nil {
				return err
			}
			all, err := svc.ListInitiatives(cmd.Context(), service.ListOptions{IncludeArchived: listArchived})
			if err != nil {
				return err
			}
			if a.jsonOut {
				return a.writeJSON(all)
			}
			rows := make([][]string, 0, len(all))
			for _, i := range all {
				rows = append(rows, []string{i.ID, i.Slug, i.Title})
			}
			return a.writeTable([]string{"ID", "SLUG", "TITLE"}, rows)
		},
	}

	list.Flags().BoolVar(&listArchived, "archived", false, "include archived initiatives")

	show := &cobra.Command{
		Use:   "show <id>",
		Short: "Show an initiative and its progress",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := parseIDArg(domain.KindInitiative, args[0])
			if err != nil {
				return err
			}
			svc, err := a.service(cmd.Context())
			if err != nil {
				return err
			}
			v, err := svc.GetInitiative(cmd.Context(), id)
			if err != nil {
				return err
			}
			st, err := svc.Status(cmd.Context(), id, service.ListOptions{IncludeArchived: true})
			if err != nil {
				return err
			}
			if a.jsonOut {
				return a.writeJSON(struct {
					service.InitiativeView
					Status service.InitiativeStatus `json:"status"`
				}{v, st.Initiatives[0]})
			}
			if err := a.printf("%s  %s\nslug: %s\ntasks: %s\n", v.ID, v.Title, v.Slug, service.FormatCounts(st.Initiatives[0].TaskCounts)); err != nil {
				return err
			}
			if err := a.printf("%s", archivedLine(v.ArchivedAt, false, "")); err != nil {
				return err
			}
			if err := a.section("Brief", v.Brief); err != nil {
				return err
			}
			return a.printMilestoneTable(st.Initiatives[0].Milestones)
		},
	}

	var upTitle, upBrief, upBriefFile string
	update := &cobra.Command{
		Use:   "update <id>",
		Short: "Edit an initiative's title or brief",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := parseIDArg(domain.KindInitiative, args[0])
			if err != nil {
				return err
			}
			b, err := a.textFlag(cmd, "brief", upBrief, "brief-file", upBriefFile)
			if err != nil {
				return err
			}
			svc, err := a.service(cmd.Context())
			if err != nil {
				return err
			}
			v, err := svc.UpdateInitiative(cmd.Context(), id, service.InitiativePatch{Title: optString(cmd, "title", upTitle), Brief: b})
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
	update.Flags().StringVar(&upBrief, "brief", "", "new brief")
	update.Flags().StringVar(&upBriefFile, "brief-file", "", "read the new brief from a file, or - for stdin")

	cmd.AddCommand(create, list, show, update, a.newInitiativeArchiveCommand(true), a.newInitiativeArchiveCommand(false), a.newDeleteCommand(domain.KindInitiative, true, "Delete an initiative. Without --force it must have no milestones or documents; with --force they are deleted too."))
	return cmd
}

func (a *app) printMilestoneTable(ms []service.MilestoneView) error {
	if len(ms) == 0 {
		return a.printf("\nno milestones\n")
	}
	if err := a.printf("\n"); err != nil {
		return err
	}
	rows := make([][]string, 0, len(ms))
	for _, m := range ms {
		rows = append(rows, []string{m.ID, m.Status, m.Title, service.FormatCounts(m.TaskCounts)})
	}
	return a.writeTable([]string{"MILESTONE", "STATUS", "TITLE", "TASKS"}, rows)
}

func (a *app) newInitiativeArchiveCommand(archive bool) *cobra.Command {
	use, short, verb := "unarchive <id>", "Make an archived initiative and its contents editable again", "unarchived"
	if archive {
		use, short, verb = "archive <id>", "Archive an initiative, hiding it and making it and its contents read-only", "archived"
	}
	return &cobra.Command{
		Use:   use,
		Short: short,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := parseIDArg(domain.KindInitiative, args[0])
			if err != nil {
				return err
			}
			svc, err := a.service(cmd.Context())
			if err != nil {
				return err
			}
			var v service.InitiativeView
			if archive {
				v, err = svc.ArchiveInitiative(cmd.Context(), id)
			} else {
				v, err = svc.UnarchiveInitiative(cmd.Context(), id)
			}
			if err != nil {
				return err
			}
			if a.jsonOut {
				return a.writeJSON(v)
			}
			return a.printf("%s %s\n", verb, v.ID)
		},
	}
}
