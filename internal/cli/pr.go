package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/bengearig/ralph/internal/domain"
	"github.com/bengearig/ralph/internal/drift"
	"github.com/bengearig/ralph/internal/errs"
	"github.com/bengearig/ralph/internal/loop"
	"github.com/bengearig/ralph/internal/pr"
	"github.com/bengearig/ralph/internal/service"

	// Register pull request providers.
	_ "github.com/bengearig/ralph/internal/pr/bitbucketcloud"
)

// newPRProvider builds the configured pull request provider; tests replace it.
var newPRProvider = pr.New

func (a *app) newPRCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "pr",
		Short: "Open pull requests for milestones",
	}
	var milestone string
	var dryRun, force bool
	create := &cobra.Command{
		Use:   "create",
		Short: "Open the pull request for a milestone's branch",
		Long: `Pull request creation is not implemented yet; --dry-run shows the request that
would be sent.

The milestone's branch must match its recorded git state: otherwise pr create
refuses (exit code 3) and points at ralph milestone reconcile. --dry-run
reports the drift instead of refusing.

It also refuses (exit code 3) while a milestone this one depends on is not
merged; --force skips that check, and --dry-run reports it as warnings.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			mID, err := parseIDArg(domain.KindMilestone, milestone)
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
			svc, err := a.service(cmd.Context())
			if err != nil {
				return err
			}
			if err := svc.CheckMilestoneWritable(cmd.Context(), mID); err != nil {
				return err
			}
			m, err := svc.GetMilestone(cmd.Context(), mID)
			if err != nil {
				return err
			}
			iID, err := domain.ParseID(domain.KindInitiative, m.InitiativeID)
			if err != nil {
				return err
			}
			i, err := svc.GetInitiative(cmd.Context(), iID)
			if err != nil {
				return err
			}
			tasks, err := svc.ListTasks(cmd.Context(), service.TaskFilter{MilestoneID: mID, IncludeArchived: true})
			if err != nil {
				return err
			}
			branch, err := loop.ResolveBranch(cmd.Context(), svc, l.Config, i, m)
			if err != nil {
				return err
			}
			req := pr.MilestoneRequest(l.Config, i, m, tasks, branch)
			if !dryRun && m.Status != service.MilestoneDone {
				a.warn("milestone " + m.ID + " is " + m.Status + ", not DONE")
			}
			var report drift.Report
			in, driftErr := loop.DriftInput(cmd.Context(), svc, l.Config, p.Root, mID)
			if driftErr == nil {
				report, driftErr = drift.Check(cmd.Context(), in)
			}
			if !dryRun {
				if driftErr != nil {
					return driftErr
				}
				if !report.Clean() {
					return fmt.Errorf("%w: %s; run: ralph milestone reconcile %s", errs.ErrNeedsHuman, report.Summary(), m.ID)
				}
			}
			var depWarnings []string
			if !force {
				depWarnings, err = loop.UnmetDependencies(cmd.Context(), svc, l.Config, p.Root, mID)
				if err != nil {
					if !dryRun {
						return err
					}
					depWarnings = []string{"dependency check failed: " + err.Error()}
				} else if len(depWarnings) > 0 && !dryRun {
					return loop.DependencyRefusal(depWarnings)
				}
			}
			provider, err := newPRProvider(l.Config)
			if err != nil {
				return err
			}
			if dryRun {
				validation := ""
				if err := provider.Validate(); err != nil {
					validation = err.Error()
				}
				var driftReport *drift.Report
				driftError := ""
				if driftErr != nil {
					driftError = driftErr.Error()
				} else {
					driftReport = &report
				}
				if a.jsonOut {
					return a.writeJSON(struct {
						Provider   string        `json:"provider"`
						Request    pr.Request    `json:"request"`
						Validation string        `json:"validation_errors,omitempty"`
						Drift      *drift.Report `json:"drift"`
						DriftError string        `json:"drift_error,omitempty"`
						DepWarns   []string      `json:"dependency_warnings,omitempty"`
					}{provider.Name(), req, validation, driftReport, driftError, depWarnings})
				}
				if validation != "" {
					a.warn("provider is not ready:\n" + validation)
				}
				for _, w := range depWarnings {
					a.warn(w)
				}
				driftLine := "not checked"
				if driftErr != nil {
					a.warn("drift check failed: " + driftError)
				} else {
					driftLine = fmt.Sprintf("%s (%s)", report.State, report.Summary())
				}
				if err := a.printf("provider: %s\ntitle:    %s\nbranches: %s → %s\ndrift:    %s\ndraft:    %t\n",
					provider.Name(), req.Title, req.SourceBranch, req.DestinationBranch, driftLine, req.Draft); err != nil {
					return err
				}
				if driftErr == nil && !report.Clean() {
					if err := a.printDriftDetails(report); err != nil {
						return err
					}
				}
				return a.printf("\n%s", req.Body)
			}
			created, err := provider.Create(cmd.Context(), req)
			if err != nil {
				return err
			}
			if a.jsonOut {
				return a.writeJSON(created)
			}
			return a.printf("opened %s\n", created.URL)
		},
	}
	create.Flags().StringVar(&milestone, "milestone", "", "milestone ID, e.g. M-1 (required)")
	create.Flags().BoolVar(&force, "force", false, "ignore unmerged milestone dependencies")
	create.Flags().BoolVar(&dryRun, "dry-run", false, "print the request without sending it")
	_ = create.MarkFlagRequired("milestone")
	cmd.AddCommand(create)

	var unlinkMilestone string
	unlink := &cobra.Command{
		Use:   "unlink",
		Short: "Forget a milestone's recorded pull request",
		Long: `Clears the pull request recorded for a milestone, so pr create can open a new
one, for example after the old one was declined or closed. It changes nothing
on the hosting service and needs no provider, token, or network.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			mID, err := parseIDArg(domain.KindMilestone, unlinkMilestone)
			if err != nil {
				return err
			}
			svc, err := a.service(cmd.Context())
			if err != nil {
				return err
			}
			removed, err := svc.ClearMilestonePullRequest(cmd.Context(), mID)
			if err != nil {
				return err
			}
			var view *service.PullRequestView
			if !removed.IsZero() {
				view = &service.PullRequestView{Provider: removed.Provider, Namespace: removed.Namespace, Repository: removed.Repository, Number: removed.Number}
				if u, ok := pr.Link(removed.Provider, removed.Namespace, removed.Repository, removed.Number); ok {
					view.URL = u
				}
			}
			if a.jsonOut {
				return a.writeJSON(struct {
					Milestone string                   `json:"milestone"`
					Unlinked  *service.PullRequestView `json:"unlinked"`
				}{domain.FormatID(domain.KindMilestone, mID), view})
			}
			if view == nil {
				return a.printf("nothing to unlink\n")
			}
			if view.URL != "" {
				return a.printf("unlinked %s\n", view.URL)
			}
			return a.printf("unlinked %s %s/%s#%d\n", view.Provider, view.Namespace, view.Repository, view.Number)
		},
	}
	unlink.Flags().StringVar(&unlinkMilestone, "milestone", "", "milestone ID, e.g. M-1 (required)")
	_ = unlink.MarkFlagRequired("milestone")
	cmd.AddCommand(unlink)
	return cmd
}
