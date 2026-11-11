package cli

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/bengearig/ralph/internal/domain"
	"github.com/bengearig/ralph/internal/errs"
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
			a.warn(v.CardWarning)
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
			cards := make([]*service.CardView, 0, len(all))
			for _, i := range all {
				rows = append(rows, []string{i.ID, i.Slug, i.Title})
				cards = append(cards, i.Card)
			}
			header, rows := a.withCard([]string{"ID", "SLUG", "TITLE"}, rows, cards)
			return a.writeTable(header, rows)
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
			v, err := fetchInitiativeShow(cmd.Context(), svc, id)
			if err != nil {
				return err
			}
			if a.jsonOut {
				return a.writeJSON(v)
			}
			return a.renderInitiativeShow(v)
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

	cmd.AddCommand(create, list, show, a.newInitiativeWatchCommand(), update, a.newInitiativeArchiveCommand(true), a.newInitiativeArchiveCommand(false), a.newDeleteCommand(domain.KindInitiative, true, "Delete an initiative. Without --force it must have no milestones or documents; with --force they are deleted too."))
	return cmd
}

type initiativeShow struct {
	service.InitiativeView
	Status service.InitiativeStatus `json:"status"`
}

func fetchInitiativeShow(ctx context.Context, svc *service.Service, id int64) (initiativeShow, error) {
	v, err := svc.GetInitiative(ctx, id)
	if err != nil {
		return initiativeShow{}, err
	}
	st, err := svc.Status(ctx, id, service.ListOptions{IncludeArchived: true})
	if err != nil {
		return initiativeShow{}, err
	}
	return initiativeShow{v, st.Initiatives[0]}, nil
}

func (a *app) renderInitiativeShow(v initiativeShow) error {
	if err := a.printf("%s  %s\nslug: %s\ntasks: %s\n", v.ID, v.Title, v.Slug, service.FormatCounts(v.Status.TaskCounts)); err != nil {
		return err
	}
	if err := a.printf("%s%s", cardLine(v.Card), archivedLine(v.ArchivedAt, false, "")); err != nil {
		return err
	}
	if err := a.section("Brief", v.Brief); err != nil {
		return err
	}
	return a.printMilestoneTable(v.Status.Milestones, nil)
}

func (a *app) printMilestoneTable(ms []service.MilestoneView, waiting map[string][]string) error {
	if len(ms) == 0 {
		return a.printf("\nno milestones\n")
	}
	if err := a.printf("\n"); err != nil {
		return err
	}
	rows := make([][]string, 0, len(ms))
	cards := make([]*service.CardView, 0, len(ms))
	withPR := false
	for _, m := range ms {
		withPR = withPR || m.PullRequest != nil
	}
	for _, m := range ms {
		status := m.Status
		if w := waiting[m.ID]; len(w) > 0 {
			status += "  waiting on " + strings.Join(w, ", ")
		}
		row := []string{m.ID, status, m.Title, service.FormatCounts(m.TaskCounts)}
		if withPR {
			link := "-"
			if m.PullRequest != nil {
				link = pullRequestLine(*m.PullRequest)
			}
			row = append(row, link)
		}
		rows = append(rows, row)
		cards = append(cards, m.Card)
	}
	columns := []string{"MILESTONE", "STATUS", "TITLE", "TASKS"}
	if withPR {
		columns = append(columns, "PR")
	}
	header, rows := a.withCard(columns, rows, cards)
	return a.writeTable(header, rows)
}

func (a *app) newInitiativeArchiveCommand(archive bool) *cobra.Command {
	use, short, verb := "unarchive <id>", "Make an archived initiative and its contents editable again", "unarchived"
	if archive {
		use, short, verb = "archive <id>", "Archive an initiative, hiding it and making it and its contents read-only", "archived"
	}
	var promote string
	cmd := &cobra.Command{
		Use:   use,
		Short: short,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := parseIDArg(domain.KindInitiative, args[0])
			if err != nil {
				return err
			}
			var choice service.DocumentChoice
			if archive && cmd.Flags().Changed("promote") {
				if choice, err = service.ParseDocumentChoice(strings.Split(promote, ",")); err != nil {
					return err
				}
			}
			svc, err := a.service(cmd.Context())
			if err != nil {
				return err
			}
			var v service.InitiativeView
			if archive {
				v, err = svc.ArchiveInitiative(cmd.Context(), id, choice)
			} else {
				v, err = svc.UnarchiveInitiative(cmd.Context(), id)
			}
			var need *service.DocumentChoiceError
			if errors.As(err, &need) {
				return fmt.Errorf("%w: %s", errs.ErrNeedsHuman, documentChoiceMessage(need))
			}
			if err != nil {
				return err
			}
			if a.jsonOut {
				return a.writeJSON(v)
			}
			if len(v.PromotedDocuments) > 0 {
				return a.printf("%s %s; promoted %s\n", verb, v.ID, strings.Join(v.PromotedDocuments, ", "))
			}
			return a.printf("%s %s\n", verb, v.ID)
		},
	}
	if archive {
		cmd.Flags().StringVar(&promote, "promote", "", "documents to promote to project-wide: comma-separated IDs (D-1,D-2), all, or none; required when the initiative has documents")
	}
	return cmd
}

func documentChoiceMessage(e *service.DocumentChoiceError) string {
	var b strings.Builder
	b.WriteString(e.Error())
	b.WriteString("\nRerun with one of:")
	if ids := e.SuggestedIDs(); ids != "" {
		fmt.Fprintf(&b, "\n  ralph initiative archive %s --promote %s", e.Initiative, ids)
	}
	fmt.Fprintf(&b, "\n  ralph initiative archive %s --promote all", e.Initiative)
	fmt.Fprintf(&b, "\n  ralph initiative archive %s --promote none", e.Initiative)
	return b.String()
}
