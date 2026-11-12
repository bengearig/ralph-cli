package loop

import (
	"context"
	"fmt"
	"strings"

	"github.com/bengearig/ralph/internal/config"
	"github.com/bengearig/ralph/internal/domain"
	"github.com/bengearig/ralph/internal/git"
	"github.com/bengearig/ralph/internal/looplock"
	"github.com/bengearig/ralph/internal/service"
)

// Readiness returns one reason for each way the milestone is not ready for a
// pull request, in a fixed order, and none when it is ready. The error is for
// a failed check, never for a reason. Nothing is changed.
func Readiness(ctx context.Context, repo git.Repo, lockPath string, cfg *config.Config, m service.MilestoneView, tasks []service.TaskView, branch string) ([]string, error) {
	reasons := []string{}

	holder, held, err := looplock.Probe(lockPath)
	if err != nil {
		return nil, err
	}
	if held {
		who := "a loop"
		if holder.PID != 0 {
			who = fmt.Sprintf("a loop (pid %d, milestone %s)", holder.PID, holder.Milestone)
		}
		reasons = append(reasons, who+" is running in this project; its last commit may not have landed yet")
	}

	if branch != "" {
		current, err := repo.CurrentBranch(ctx)
		if err != nil {
			return nil, err
		}
		if current == branch {
			dirty, err := repo.Dirty(ctx)
			if err != nil {
				return nil, err
			}
			if len(dirty) > 0 {
				list, more := dirty, ""
				if len(list) > maxDirtyListed {
					more = fmt.Sprintf(" and %d more", len(list)-maxDirtyListed)
					list = list[:maxDirtyListed]
				}
				reasons = append(reasons, fmt.Sprintf("branch %s has uncommitted changes (%s%s)", branch, strings.Join(list, ", "), more))
			}
		}
	}

	if cfg.Loop.CommitPerTask {
		var uncommitted []string
		for _, t := range tasks {
			if t.Status == string(domain.StatusDone) && t.Commit == "" {
				uncommitted = append(uncommitted, t.ID)
			}
		}
		if len(uncommitted) > 0 {
			reasons = append(reasons, fmt.Sprintf("%s DONE with no recorded commit (%s); this is a false positive if a task changed no files, and --force skips the check",
				pluralTasks(len(uncommitted)), strings.Join(uncommitted, ", ")))
		}
	}

	if m.Status != service.MilestoneDone {
		reasons = append(reasons, fmt.Sprintf("%s is %s, not DONE", m.ID, m.Status))
	}
	return reasons, nil
}

func pluralTasks(n int) string {
	if n == 1 {
		return "a task is"
	}
	return "tasks are"
}
