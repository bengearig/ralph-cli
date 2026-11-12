package cli

import (
	"errors"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/bengearig/ralph/internal/domain"
	"github.com/bengearig/ralph/internal/errs"
	"github.com/bengearig/ralph/internal/service"
)

func (a *app) newCardsReconcileCommand() *cobra.Command {
	var createdAfter string
	var apply bool
	cmd := &cobra.Command{
		Use:   "reconcile",
		Short: "Find, and optionally cancel, open cards whose ralph work no longer exists",
		Long: `Finds open Jira cards carrying all of the configured labels whose ralph resource
no longer exists, for example cards of work deleted before
task_manager.jira.canceled_status was set.

Without --apply this command changes nothing; it only reports. A card is an
orphan only when its ID and key are both unknown to ralph. Cards that cannot
be matched are listed as skipped for a person to review, and are never touched.

--apply moves each orphan to task_manager.jira.canceled_status. It requires
--created-after, so that only cards created since this repository started using
ralph are considered, and asks no confirmation. A card with no available
transition to that status is reported as failed rather than forced, and the
command then exits with code 3.

The labels identify the Jira project, not this ralph store, so other people's
or other repos' cards with the same labels can look like orphans. Review the
list, and scope the search with --created-after.

--created-after takes a date (YYYY-MM-DD). Only the date is sent to Jira, which
reads it in the Jira user's time zone.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			var opts service.ReconcileOptions
			if createdAfter != "" {
				d, err := time.Parse("2006-01-02", createdAfter)
				if err != nil {
					return fmt.Errorf("%w: --created-after: %q is not a date (expected YYYY-MM-DD)", domain.ErrInvalid, createdAfter)
				}
				opts.CreatedAfter = d
			}
			opts.Apply = apply
			if apply && !a.jsonOut {
				opts.Progress = func(it service.ReconcileItem) {
					fmt.Fprintf(a.streams.Err, "%s %s %s\n", it.Outcome, it.ID, it.Key)
				}
			}
			svc, err := a.service(cmd.Context())
			if err != nil {
				return err
			}
			report, err := svc.ReconcileCards(cmd.Context(), opts)
			if errors.Is(err, service.ErrReconcileNeedsScope) {
				return fmt.Errorf("%w: --apply needs --created-after YYYY-MM-DD, so that only cards created since this repository started using ralph are considered", domain.ErrInvalid)
			}
			if errors.Is(err, service.ErrNoCanceledStatus) {
				return fmt.Errorf("%w: --apply needs task_manager.jira.canceled_status to be set; cards would not be moved", domain.ErrInvalid)
			}
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
			} else if err := a.printReconcileReport(report, apply); err != nil {
				return err
			}
			if apply && report.Failed > 0 {
				return fmt.Errorf("%w: %d card(s) could not be cancelled", errs.ErrNeedsHuman, report.Failed)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&apply, "apply", false, "cancel the orphans; requires --created-after")
	cmd.Flags().StringVar(&createdAfter, "created-after", "", "only cards created on or after this date, YYYY-MM-DD")
	return cmd
}

func (a *app) printReconcileReport(r service.ReconcileReport, apply bool) error {
	for _, it := range r.Items {
		switch it.Outcome {
		case "orphan":
			if err := a.printf("%-8s %s  %s  [%s]  %s  %s\n", "orphan", it.ID, it.Key, it.Status, it.Summary, it.URL); err != nil {
				return err
			}
		case "cancelled":
			if err := a.printf("%-8s %s  %s  %s\n", "cancelled", it.ID, it.Key, it.Summary); err != nil {
				return err
			}
		case "failed":
			if err := a.printf("%-8s %s  %s\n", "failed", it.Key, it.Reason); err != nil {
				return err
			}
		case "skipped":
			if err := a.printf("%-8s %s  %s  (%s)\n", "skipped", it.Key, it.Reason, it.Summary); err != nil {
				return err
			}
		}
	}
	if apply {
		return a.printf("orphans %d, cancelled %d, skipped %d, failed %d\n", r.Orphans, r.Cancelled, r.Skipped, r.Failed)
	}
	return a.printf("orphans %d, skipped %d\n", r.Orphans, r.Skipped)
}
