package cli

import (
	"strconv"

	"github.com/spf13/cobra"

	"github.com/bengearig/ralph/internal/domain"
	"github.com/bengearig/ralph/internal/service"
)

func (a *app) newMilestoneCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "milestone",
		Short: "Manage milestones (each becomes one branch and pull request)",
	}

	var initiative, title, slug, description string
	var position int
	create := &cobra.Command{
		Use:   "create",
		Short: "Add a milestone to an initiative",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			iID, err := parseIDArg(domain.KindInitiative, initiative)
			if err != nil {
				return err
			}
			svc, err := a.service(cmd.Context())
			if err != nil {
				return err
			}
			v, err := svc.CreateMilestone(cmd.Context(), service.MilestoneInput{
				InitiativeID: iID, Title: title, Slug: slug, Description: description, Position: position,
			})
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
	create.Flags().StringVar(&initiative, "initiative", "", "initiative ID, e.g. I-1 (required)")
	create.Flags().StringVar(&title, "title", "", "title (required)")
	create.Flags().StringVar(&slug, "slug", "", "slug (default: derived from title)")
	create.Flags().StringVar(&description, "description", "", "what this milestone delivers")
	create.Flags().IntVar(&position, "position", 0, "order within the initiative (default: last)")
	_ = create.MarkFlagRequired("initiative")
	_ = create.MarkFlagRequired("title")

	var listInitiative string
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
			ms, err := svc.ListMilestones(cmd.Context(), iID)
			if err != nil {
				return err
			}
			if a.jsonOut {
				return a.writeJSON(ms)
			}
			rows := make([][]string, 0, len(ms))
			for _, m := range ms {
				rows = append(rows, []string{m.ID, m.InitiativeID, strconv.Itoa(m.Position), m.Status, m.Title, service.FormatCounts(m.TaskCounts)})
			}
			return a.writeTable([]string{"ID", "INITIATIVE", "POS", "STATUS", "TITLE", "TASKS"}, rows)
		},
	}
	list.Flags().StringVar(&listInitiative, "initiative", "", "only this initiative")

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
			m, err := svc.GetMilestone(cmd.Context(), id)
			if err != nil {
				return err
			}
			tasks, err := svc.ListTasks(cmd.Context(), service.TaskFilter{MilestoneID: id})
			if err != nil {
				return err
			}
			if a.jsonOut {
				return a.writeJSON(struct {
					service.MilestoneView
					Tasks []service.TaskView `json:"tasks"`
				}{m, tasks})
			}
			if err := a.printf("%s  %s\ninitiative: %s\nslug: %s\nstatus: %s\n", m.ID, m.Title, m.InitiativeID, m.Slug, m.Status); err != nil {
				return err
			}
			if err := a.section("Description", m.Description); err != nil {
				return err
			}
			return a.printTaskTable(tasks)
		},
	}

	var upTitle, upDescription string
	var upPosition int
	update := &cobra.Command{
		Use:   "update <id>",
		Short: "Edit a milestone",
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
			v, err := svc.UpdateMilestone(cmd.Context(), id, service.MilestonePatch{
				Title:       optString(cmd, "title", upTitle),
				Description: optString(cmd, "description", upDescription),
				Position:    optInt(cmd, "position", upPosition),
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

	cmd.AddCommand(create, list, show, update)
	return cmd
}
