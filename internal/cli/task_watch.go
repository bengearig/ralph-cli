package cli

import (
	"context"
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/bengearig/ralph/internal/domain"
	"github.com/bengearig/ralph/internal/store"
)

func (a *app) newTaskWatchCommand() *cobra.Command {
	var opts *watchOptions
	cmd := &cobra.Command{
		Use:   "watch <id>",
		Short: "Watch a task and its notes live",
		Long: `Watch a task and its notes live.

This is the live form of "ralph task show": it polls the task and shows it again
whenever it changes. It is read-only. On a terminal it redraws in place and
highlights what changed; otherwise (or with --json) it writes one line of JSON
for the first snapshot and for every change.

With --until-done the watch ends when the task is done or cancelled.

Exit codes:
  0    finished (--until-done), or the output was closed
  3    stuck: the task is blocked (--until-done)
  130  interrupted (Ctrl-C or SIGTERM)
  1    error, including the task no longer existing`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := parseIDArg(domain.KindTask, args[0])
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
				v, err := fetchTaskShow(ctx, svc, id)
				if err != nil {
					if seen != "" && errors.Is(err, store.ErrNotFound) {
						return snapshot{}, fmt.Errorf("%s no longer exists", seen)
					}
					return snapshot{}, err
				}
				seen = v.View.ID
				text, err := a.capture(func() error { return a.renderTaskShow(v) })
				if err != nil {
					return snapshot{}, err
				}
				js, err := snapshotJSON(v.View)
				if err != nil {
					return snapshot{}, err
				}
				return snapshot{Text: text, JSON: js, State: taskState(v.View.Status)}, nil
			}}
			return a.runWatch(cmd.Context(), src, *opts, a.newWatchRenderer(opts.Interval))
		},
	}
	opts = addWatchFlags(cmd, true)
	return cmd
}

func taskState(status string) doneState {
	switch status {
	case "BLOCKED":
		return stuck
	case "DONE", "CANCELLED":
		return done
	}
	return notDone
}
