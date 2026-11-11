package cli

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/bengearig/ralph/internal/domain"
	"github.com/bengearig/ralph/internal/loop"
	"github.com/bengearig/ralph/internal/service"
	"github.com/bengearig/ralph/internal/store"
)

func (a *app) newSearchCommand() *cobra.Command {
	var kinds []string
	var initiative string
	var limit int
	var archived bool
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
				Query: strings.Join(args, " "), Kinds: ks, InitiativeID: iID, Limit: limit, IncludeArchived: archived,
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
	cmd.Flags().BoolVar(&archived, "archived", false, "include archived initiatives and documents, and their contents")
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
	var archived bool
	var lw *listWatch
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Summarize initiatives, milestones, and task progress",
		Long: `Summarize initiatives, milestones, and task progress.

With --watch it polls and shows the summary again whenever it changes. It is
read-only. On a terminal it redraws in place and highlights what changed;
otherwise (or with --json) it writes one line of JSON for the first snapshot and
for every change. The overview never finishes, so the watch runs until it is
interrupted (exit 130), an error occurs, or, with --initiative, that initiative
no longer exists.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := lw.check(cmd); err != nil {
				return err
			}
			iID, err := parseIDFlag(domain.KindInitiative, initiative)
			if err != nil {
				return err
			}
			svc, err := a.service(cmd.Context())
			if err != nil {
				return err
			}
			if lw.Watch {
				return a.watchStatus(cmd.Context(), svc, iID, archived, lw.Interval)
			}
			doc, err := a.fetchStatus(cmd.Context(), svc, iID, archived)
			if err != nil {
				return err
			}
			if a.jsonOut {
				return a.writeJSON(doc)
			}
			return a.renderStatus(doc)
		},
	}
	cmd.Flags().StringVar(&initiative, "initiative", "", "only this initiative")
	cmd.Flags().BoolVar(&archived, "archived", false, "include archived initiatives and documents, and their contents")
	lw = addListWatchFlags(cmd)
	return cmd
}

func (a *app) fetchStatus(ctx context.Context, svc *service.Service, initiativeID int64, archived bool) (statusDoc, error) {
	st, err := svc.Status(ctx, initiativeID, service.ListOptions{IncludeArchived: archived})
	if err != nil {
		return statusDoc{}, err
	}
	waiting := map[string][]string{}
	if cfg, root, ok := a.waitingContext(); ok {
		cache := map[string][]loop.DependencyFinding{}
		for _, i := range st.Initiatives {
			for _, m := range i.Milestones {
				if w := a.waitingOn(ctx, svc, cfg, root, m, cache); len(w) > 0 {
					waiting[m.ID] = findingIDs(w)
				}
			}
		}
	}
	return statusJSON(st, waiting), nil
}

func (a *app) renderStatus(doc statusDoc) error {
	if len(doc.Initiatives) == 0 {
		return a.printf("no initiatives yet; start with `ralph initiative create` or /ralph:receive in Claude Code\n")
	}
	for n, i := range doc.Initiatives {
		if n > 0 {
			if err := a.printf("\n"); err != nil {
				return err
			}
		}
		if err := a.printf("%s  %s  (%d document(s); %s)\n", i.ID, i.Title, i.Documents, service.FormatCounts(i.TaskCounts)); err != nil {
			return err
		}
		ms := make([]service.MilestoneView, 0, len(i.Milestones))
		waiting := map[string][]string{}
		for _, m := range i.Milestones {
			ms = append(ms, m.MilestoneView)
			if len(m.WaitingOn) > 0 {
				waiting[m.ID] = m.WaitingOn
			}
		}
		if err := a.printMilestoneTable(ms, waiting); err != nil {
			return err
		}
	}
	return nil
}

func (a *app) watchStatus(ctx context.Context, svc *service.Service, initiativeID int64, archived bool, interval time.Duration) error {
	seen := ""
	src := watchSource{Fetch: func(ctx context.Context) (snapshot, error) {
		doc, err := a.fetchStatus(ctx, svc, initiativeID, archived)
		if err != nil {
			if seen != "" && errors.Is(err, store.ErrNotFound) {
				return snapshot{}, fmt.Errorf("%s no longer exists", seen)
			}
			return snapshot{}, err
		}
		if initiativeID != 0 {
			seen = domain.FormatID(domain.KindInitiative, initiativeID)
		}
		text, err := a.capture(func() error { return a.renderStatus(doc) })
		if err != nil {
			return snapshot{}, err
		}
		js, err := snapshotJSON(doc)
		if err != nil {
			return snapshot{}, err
		}
		return snapshot{Text: text, JSON: js, State: notDone}, nil
	}}
	return a.runWatch(ctx, src, watchOptions{Interval: interval}, a.newWatchRenderer(interval))
}

type statusMilestone struct {
	service.MilestoneView
	WaitingOn []string `json:"waiting_on,omitempty"`
}

type statusInitiative struct {
	service.InitiativeStatus
	Milestones []statusMilestone `json:"milestones"`
}

type statusDoc struct {
	Initiatives []statusInitiative `json:"initiatives"`
}

func statusJSON(st service.StatusView, waiting map[string][]string) statusDoc {
	out := statusDoc{Initiatives: make([]statusInitiative, 0, len(st.Initiatives))}
	for _, i := range st.Initiatives {
		si := statusInitiative{InitiativeStatus: i, Milestones: make([]statusMilestone, 0, len(i.Milestones))}
		for _, m := range i.Milestones {
			si.Milestones = append(si.Milestones, statusMilestone{MilestoneView: m, WaitingOn: waiting[m.ID]})
		}
		out.Initiatives = append(out.Initiatives, si)
	}
	return out
}
