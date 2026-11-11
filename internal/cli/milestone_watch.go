package cli

import (
	"context"
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/bengearig/ralph/internal/domain"
	"github.com/bengearig/ralph/internal/store"
)

func (a *app) newMilestoneWatchCommand() *cobra.Command {
	var opts *watchOptions
	cmd := &cobra.Command{
		Use:   "watch <id>",
		Short: "Watch a milestone and its tasks live",
		Long: `Watch a milestone and its tasks live.

This is the live form of "ralph milestone show": it polls the milestone and
shows it again whenever it changes. It is read-only. On a terminal it redraws in
place and highlights what changed; otherwise (or with --json) it writes one line
of JSON for the first snapshot and for every change.

With --until-done the watch ends when the milestone is done.

Exit codes:
  0    finished (--until-done), or the output was closed
  3    stuck: the milestone or a task is blocked (--until-done)
  130  interrupted (Ctrl-C or SIGTERM)
  1    error, including the milestone no longer existing`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := parseIDArg(domain.KindMilestone, args[0])
			if err != nil {
				return err
			}
			if err := opts.validate(); err != nil {
				return err
			}
			svc, err := a.service(cmd.Context())
			if err != nil {
				return err
			}
			seen := ""
			src := watchSource{Fetch: func(ctx context.Context) (snapshot, error) {
				v, err := a.fetchMilestoneShowWaiting(ctx, svc, id)
				if err != nil {
					if seen != "" && errors.Is(err, store.ErrNotFound) {
						return snapshot{}, fmt.Errorf("%s no longer exists", seen)
					}
					return snapshot{}, err
				}
				seen = v.ID
				text, err := a.capture(func() error { return a.renderMilestoneShow(v) })
				if err != nil {
					return snapshot{}, err
				}
				js, err := snapshotJSON(v)
				if err != nil {
					return snapshot{}, err
				}
				return snapshot{Text: text, JSON: js, State: milestoneState(v)}, nil
			}}
			return a.runWatch(cmd.Context(), src, *opts, a.newWatchRenderer(opts.Interval))
		},
	}
	opts = addWatchFlags(cmd, true)
	return cmd
}

func milestoneState(v milestoneShow) doneState {
	if v.Status == "BLOCKED" {
		return stuck
	}
	for _, t := range v.Tasks {
		if t.Status == "BLOCKED" {
			return stuck
		}
	}
	if v.Status == "DONE" {
		return done
	}
	return notDone
}
