package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/bengearig/ralph/internal/domain"
	"github.com/bengearig/ralph/internal/service"
)

func (a *app) newSearchCommand() *cobra.Command {
	var kinds []string
	var initiative string
	var limit int
	cmd := &cobra.Command{
		Use:   "search <query>",
		Short: "Search initiatives, milestones, tasks, and documents",
		Long:  "Ranks by embedding similarity, falling back to keyword search when embeddings are unavailable.",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ks, err := parseKinds(kinds)
			if err != nil {
				return err
			}
			iID, err := parseIDFlag(domain.KindInitiative, initiative)
			if err != nil {
				return err
			}
			if cmd.Flags().Changed("limit") && limit < 1 {
				return fmt.Errorf("--limit must be at least 1")
			}
			svc, err := a.service(cmd.Context())
			if err != nil {
				return err
			}
			res, err := svc.Search(cmd.Context(), service.SearchInput{
				Query: strings.Join(args, " "), Kinds: ks, InitiativeID: iID, Limit: limit,
			})
			if err != nil {
				return err
			}
			for _, w := range res.Warnings {
				a.warn(w)
			}
			if a.jsonOut {
				return a.writeJSON(res)
			}
			if len(res.Hits) == 0 {
				return a.printf("no matches (%s search)\n", res.Mode)
			}
			rows := make([][]string, 0, len(res.Hits))
			for _, h := range res.Hits {
				rows = append(rows, []string{h.ID, fmt.Sprintf("%.3f", h.Score), h.Title, h.Snippet})
			}
			if err := a.writeTable([]string{"ID", "SCORE", "TITLE", "SNIPPET"}, rows); err != nil {
				return err
			}
			return a.printf("(%s search)\n", res.Mode)
		},
	}
	cmd.Flags().StringSliceVar(&kinds, "kind", nil, "only these kinds: initiative, milestone, task, document")
	cmd.Flags().StringVar(&initiative, "initiative", "", "only this initiative")
	cmd.Flags().IntVar(&limit, "limit", 0, "maximum results (default: search.limit)")
	return cmd
}

func (a *app) newIndexCommand() *cobra.Command {
	var rebuild bool
	cmd := &cobra.Command{
		Use:   "index",
		Short: "Embed content that is missing embeddings",
		Long: `Embeds every chunk that lacks an embedding for the configured model, such as
content written while the embedding provider was unreachable or after the
model changed. --rebuild first re-chunks everything, which applies changed
chunking settings.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			svc, err := a.service(cmd.Context())
			if err != nil {
				return err
			}
			progress := func(done, total int) {
				if !a.jsonOut {
					fmt.Fprintf(a.streams.Err, "embedded %d/%d\n", done, total)
				}
			}
			rep, err := svc.Reindex(cmd.Context(), rebuild, progress)
			if err != nil {
				return err
			}
			if a.jsonOut {
				return a.writeJSON(rep)
			}
			if rep.Mode != "vector" {
				return a.printf("rechunked %d item(s); embeddings are disabled, so search is keyword-only\n", rep.Rechunked)
			}
			return a.printf("rechunked %d item(s), embedded %d chunk(s), %d remaining\n", rep.Rechunked, rep.Embedded, rep.Remaining)
		},
	}
	cmd.Flags().BoolVar(&rebuild, "rebuild", false, "re-chunk all content before embedding")
	return cmd
}

func (a *app) newStatusCommand() *cobra.Command {
	var initiative string
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Summarize initiatives, milestones, and task progress",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			iID, err := parseIDFlag(domain.KindInitiative, initiative)
			if err != nil {
				return err
			}
			svc, err := a.service(cmd.Context())
			if err != nil {
				return err
			}
			st, err := svc.Status(cmd.Context(), iID, service.ListOptions{})
			if err != nil {
				return err
			}
			if a.jsonOut {
				return a.writeJSON(st)
			}
			if len(st.Initiatives) == 0 {
				return a.printf("no initiatives yet; start with `ralph initiative create` or /ralph:receive in Claude Code\n")
			}
			for n, i := range st.Initiatives {
				if n > 0 {
					if err := a.printf("\n"); err != nil {
						return err
					}
				}
				if err := a.printf("%s  %s  (%d document(s); %s)\n", i.ID, i.Title, i.Documents, service.FormatCounts(i.TaskCounts)); err != nil {
					return err
				}
				if err := a.printMilestoneTable(i.Milestones); err != nil {
					return err
				}
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&initiative, "initiative", "", "only this initiative")
	return cmd
}
