package cli

import (
	"github.com/spf13/cobra"

	"github.com/bengearig/ralph/internal/domain"
	"github.com/bengearig/ralph/internal/loop"
	"github.com/bengearig/ralph/internal/pr"
	"github.com/bengearig/ralph/internal/service"

	// Register pull request providers.
	_ "github.com/bengearig/ralph/internal/pr/bitbucketcloud"
)

func (a *app) newPRCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "pr",
		Short: "Open pull requests for milestones",
	}
	var milestone string
	var dryRun bool
	create := &cobra.Command{
		Use:   "create",
		Short: "Open the pull request for a milestone's branch",
		Long:  "Pull request creation is not implemented yet; --dry-run shows the request that would be sent.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			mID, err := parseIDArg(domain.KindMilestone, milestone)
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
			tasks, err := svc.ListTasks(cmd.Context(), service.TaskFilter{MilestoneID: mID})
			if err != nil {
				return err
			}
			branch, err := loop.BranchName(l.Config.Loop.BranchTemplate,
				loop.Ref{ID: i.ID, Slug: i.Slug, Title: i.Title}, loop.Ref{ID: m.ID, Slug: m.Slug, Title: m.Title})
			if err != nil {
				return err
			}
			req := pr.MilestoneRequest(l.Config, i, m, tasks, branch)
			provider, err := pr.New(l.Config)
			if err != nil {
				return err
			}
			if dryRun {
				validation := ""
				if err := provider.Validate(); err != nil {
					validation = err.Error()
				}
				if a.jsonOut {
					return a.writeJSON(struct {
						Provider   string     `json:"provider"`
						Request    pr.Request `json:"request"`
						Validation string     `json:"validation_errors,omitempty"`
					}{provider.Name(), req, validation})
				}
				if validation != "" {
					a.warn("provider is not ready:\n" + validation)
				}
				return a.printf("provider: %s\ntitle:    %s\nbranches: %s → %s\ndraft:    %t\n\n%s",
					provider.Name(), req.Title, req.SourceBranch, req.DestinationBranch, req.Draft, req.Body)
			}
			if m.Status != service.MilestoneDone {
				a.warn("milestone " + m.ID + " is " + m.Status + ", not DONE")
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
	create.Flags().BoolVar(&dryRun, "dry-run", false, "print the request without sending it")
	_ = create.MarkFlagRequired("milestone")
	cmd.AddCommand(create)
	return cmd
}
