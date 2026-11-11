package cli

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/bengearig/ralph/internal/domain"
	"github.com/bengearig/ralph/internal/drift"
	"github.com/bengearig/ralph/internal/errs"
	"github.com/bengearig/ralph/internal/git"
	"github.com/bengearig/ralph/internal/loop"
	"github.com/bengearig/ralph/internal/pr"
	"github.com/bengearig/ralph/internal/service"

	// Register pull request providers.
	_ "github.com/bengearig/ralph/internal/pr/bitbucketcloud"
	_ "github.com/bengearig/ralph/internal/pr/github"
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
		Long: `Pushes the milestone's branch to pr.remote (never forced), opens the pull
request for it, and records it on the milestone.

If the milestone already records a pull request, pr create prints "exists" and
the link and does nothing else; use ralph pr unlink to open another. Otherwise
it pushes the branch (a failed push exits with code 3 and opens nothing), then
asks the provider for an open pull request from the branch into
pr.base_branch: if there is one it is recorded and printed as "adopted", and if
not a new one is created, recorded, and printed as "opened". Rerunning after a
failure is safe.

--dry-run pushes nothing, calls no provider API, and shows the request that
would be sent, plus the recorded pull request when there is one.

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
			remote := l.Config.PR.Remote
			var recorded *service.PullRequestView
			if m.PullRequest != nil {
				v := *m.PullRequest
				if v.URL == "" {
					if u, ok := pr.Link(v.Provider, v.Namespace, v.Repository, v.Number); ok {
						v.URL = u
					}
				}
				recorded = &v
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
						Provider   string                   `json:"provider"`
						Request    pr.Request               `json:"request"`
						Validation string                   `json:"validation_errors,omitempty"`
						Drift      *drift.Report            `json:"drift"`
						DriftError string                   `json:"drift_error,omitempty"`
						DepWarns   []string                 `json:"dependency_warnings,omitempty"`
						Push       pushPlan                 `json:"push"`
						PR         *service.PullRequestView `json:"pull_request,omitempty"`
					}{provider.Name(), req, validation, driftReport, driftError, depWarnings, pushPlan{remote, branch}, recorded})
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
				if err := a.printf("provider: %s\ntitle:    %s\nbranches: %s → %s\ndrift:    %s\ndraft:    %t\npush:     %s %s (not pushed in a dry run)\n",
					provider.Name(), req.Title, req.SourceBranch, req.DestinationBranch, driftLine, req.Draft, remote, branch); err != nil {
					return err
				}
				if recorded != nil {
					if err := a.printf("pull request: %s (already recorded; nothing would be pushed or opened)\n", pullRequestLine(*recorded)); err != nil {
						return err
					}
				}
				if driftErr == nil && !report.Clean() {
					if err := a.printDriftDetails(report); err != nil {
						return err
					}
				}
				return a.printf("\n%s", req.Body)
			}
			if recorded != nil {
				return a.reportAndLinkPullRequest(cmd.Context(), svc, mID, "exists", recorded, git.PushResult{})
			}
			repo, err := loop.ResolveRepo(cmd.Context(), p.Root)
			if err != nil {
				return err
			}
			pushed, err := repo.Push(cmd.Context(), remote, branch)
			if err != nil {
				return fmt.Errorf("%w: pushing %s to %s failed: %s; fix the push, then rerun: ralph pr create --milestone %s", errs.ErrNeedsHuman, branch, remote, err, m.ID)
			}
			found, ok, err := provider.FindOpen(cmd.Context(), branch, l.Config.PR.BaseBranch)
			if err != nil {
				return err
			}
			outcome := "adopted"
			if !ok {
				outcome = "opened"
				if found, err = provider.Create(cmd.Context(), req); err != nil {
					return err
				}
			}
			view := &service.PullRequestView{Provider: provider.Name(), Namespace: found.Namespace, Repository: found.Repository, Number: found.Number, URL: found.URL}
			if u, ok := pr.Link(view.Provider, view.Namespace, view.Repository, view.Number); ok {
				view.URL = u
			}
			rec := domain.PullRequestRecord{Provider: view.Provider, Namespace: view.Namespace, Repository: view.Repository, Number: view.Number}
			if err := svc.RecordMilestonePullRequest(cmd.Context(), mID, rec); err != nil {
				if !a.jsonOut {
					_ = a.printf("opened %s\n", pullRequestLine(*view))
				}
				return fmt.Errorf("the pull request exists (%s) but could not be recorded; rerunning pr create adopts it: %w", pullRequestLine(*view), err)
			}
			return a.reportAndLinkPullRequest(cmd.Context(), svc, mID, outcome, view, pushed)
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
			return a.printf("unlinked %s\n", pullRequestLine(*view))
		},
	}
	unlink.Flags().StringVar(&unlinkMilestone, "milestone", "", "milestone ID, e.g. M-1 (required)")
	_ = unlink.MarkFlagRequired("milestone")
	cmd.AddCommand(unlink)
	return cmd
}

type pushPlan struct {
	Remote string `json:"remote"`
	Branch string `json:"branch"`
}

// reportAndLinkPullRequest prints the outcome, then links the recorded pull
// request on the milestone's card. A link failure is only a warning.
func (a *app) reportAndLinkPullRequest(ctx context.Context, svc *service.Service, mID int64, outcome string, view *service.PullRequestView, pushed git.PushResult) error {
	if a.jsonOut {
		warning := svc.LinkMilestonePullRequest(ctx, mID)
		if warning != "" {
			a.warn(warning)
		}
		return a.reportPullRequest(outcome, view, pushed, warning)
	}
	if err := a.reportPullRequest(outcome, view, pushed, ""); err != nil {
		return err
	}
	if warning := svc.LinkMilestonePullRequest(ctx, mID); warning != "" {
		a.warn(warning)
	}
	return nil
}

// reportPullRequest prints a pull request command's outcome.
func (a *app) reportPullRequest(outcome string, view *service.PullRequestView, pushed git.PushResult, cardWarning string) error {
	if a.jsonOut {
		return a.writeJSON(struct {
			Outcome     string                   `json:"outcome"`
			Pushed      bool                     `json:"pushed"`
			Remote      string                   `json:"remote"`
			PullRequest *service.PullRequestView `json:"pull_request"`
			CardWarning string                   `json:"card_warning,omitempty"`
		}{outcome, pushed.Pushed, pushed.Remote, view, cardWarning})
	}
	return a.printf("%s %s\n", outcome, pullRequestLine(*view))
}
