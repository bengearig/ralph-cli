package cli

import (
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/bengearig/ralph/internal/domain"
	"github.com/bengearig/ralph/internal/errs"
	"github.com/bengearig/ralph/internal/service"
)

func (a *app) newCardsCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "cards",
		Short: "Work with task manager cards",
	}
	cmd.AddCommand(a.newCardsSyncCommand())
	cmd.AddCommand(a.newCardsReconcileCommand())
	return cmd
}

func (a *app) newCardsSyncCommand() *cobra.Command {
	var initiative string
	var includeArchived, verbose, noProgress bool
	cmd := &cobra.Command{
		Use:   "sync",
		Short: "Create cards for existing work and retry pending ones",
		Long: `Creates task manager cards (for example Jira issues) for work that has none and
pushes changes that are still pending, parents first.

Sync is explicit: it is the only way work that existed before the task manager
was configured gets cards, and ralph never runs it automatically. It is safe to
rerun; work already in sync is left alone. Cancelled work with no card is
skipped. Archived initiatives are skipped unless --include-archived is set.

Progress is shown on stderr while it runs: a live bar on a terminal, otherwise
one line per item. --no-progress, --json and the ui.progress setting "never"
turn it off.

Exit code 3 means at least one item failed and a person needs to look.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			iID, err := parseIDFlag(domain.KindInitiative, initiative)
			if err != nil {
				return err
			}
			svc, err := a.service(cmd.Context())
			if err != nil {
				return err
			}
			opts := service.SyncOptions{InitiativeID: iID, IncludeArchived: includeArchived}
			rep := a.newProgress(noProgress)
			defer rep.Finish()
			opts.Progress = func(p service.SyncProgress) {
				if p.Item == nil {
					rep.Start("syncing cards", p.Total)
					return
				}
				rep.Step(p.Item.ID)
			}
			report, err := svc.SyncCards(cmd.Context(), opts)
			rep.Finish()
			if errors.Is(err, service.ErrNoTaskManager) {
				if a.tmErr != nil {
					return fmt.Errorf("task manager disabled: %w", a.tmErr)
				}
				return fmt.Errorf("%w; see ralph config validate", err)
			}
			if err != nil {
				return err
			}
			if a.jsonOut {
				if err := a.writeJSON(report); err != nil {
					return err
				}
			} else if err := a.printSyncReport(report, verbose); err != nil {
				return err
			}
			if report.Failed > 0 {
				return fmt.Errorf("%w: %d item(s) failed to sync", errs.ErrNeedsHuman, report.Failed)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&initiative, "initiative", "", "only this initiative, for example I-8")
	cmd.Flags().BoolVar(&includeArchived, "include-archived", false, "also sync archived initiatives")
	cmd.Flags().BoolVar(&verbose, "verbose", false, "also list items that are already in sync")
	addNoProgressFlag(cmd, &noProgress)
	return cmd
}

func (a *app) printSyncReport(r service.SyncReport, verbose bool) error {
	for _, it := range r.Items {
		switch it.Outcome {
		case "created":
			if err := a.printf("%-8s %s  %s  %s\n", "created", it.ID, it.Key, it.URL); err != nil {
				return err
			}
		case "updated":
			line := fmt.Sprintf("%-8s %s  %s  %s", "updated", it.ID, it.Key, it.URL)
			if it.Error != "" {
				line += "  (" + it.Error + ")"
			}
			if err := a.printf("%s\n", line); err != nil {
				return err
			}
		case "failed":
			if err := a.printf("%-8s %s  %s\n", "failed", it.ID, it.Error); err != nil {
				return err
			}
		case "skipped":
			if err := a.printf("%-8s %s  %s\n", "skipped", it.ID, it.Error); err != nil {
				return err
			}
		case "in_sync":
			if !verbose {
				continue
			}
			if err := a.printf("%-8s %s  %s\n", "in sync", it.ID, it.Key); err != nil {
				return err
			}
		}
	}
	return a.printf("created %d, updated %d, in sync %d, skipped %d, failed %d\n",
		r.Created, r.Updated, r.InSync, r.Skipped, r.Failed)
}
