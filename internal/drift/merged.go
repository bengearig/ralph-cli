package drift

import (
	"context"
	"fmt"

	"github.com/bengearig/ralph/internal/git"
	"github.com/bengearig/ralph/internal/service"
)

// tasksInBase finds each DONE task's work among the commits of lower..baseRef
// by Ralph-Task trailer, then by the patch-id of its recorded commit. Subjects
// never count. The findings follow tasks' order, and all is true only when
// there are tasks and none is missing.
func tasksInBase(ctx context.Context, repo git.Repo, lower, baseRef string, tasks []service.TaskView) ([]TaskFinding, bool, error) {
	return findInBase(ctx, repo, repo.PatchID, lower, baseRef, tasks)
}

func findInBase(ctx context.Context, repo git.Repo, patchIDFn func(context.Context, string) (string, error),
	lower, baseRef string, tasks []service.TaskView) ([]TaskFinding, bool, error) {
	candidates, err := repo.Log(ctx, lower+".."+baseRef, TrailerKey)
	if err != nil {
		return nil, false, err
	}
	patchID := cachedPatchID(ctx, patchIDFn)
	found := make([]TaskFinding, 0, len(tasks))
	all := len(tasks) > 0
	for _, t := range tasks {
		f := TaskFinding{ID: t.ID, Title: t.Title, RecordedCommit: t.Commit, Match: MatchMissing}
		if len(candidates) > 0 {
			if err := findInRange(ctx, repo, patchID, t, candidates, &f); err != nil {
				return nil, false, fmt.Errorf("matching %s: %w", t.ID, err)
			}
		}
		if f.Match == MatchMissing {
			all = false
		}
		found = append(found, f)
	}
	return found, all, nil
}

func findInRange(ctx context.Context, repo git.Repo, patchID func(string) (string, error),
	t service.TaskView, candidates []git.LogCommit, f *TaskFinding) error {
	hash, err := trailerCommit(t.ID, candidates)
	if err != nil {
		return err
	}
	if hash != "" {
		f.Match, f.Commit = MatchTrailer, hash
		return nil
	}
	if t.Commit == "" {
		return nil
	}
	ok, err := repo.CommitExists(ctx, t.Commit)
	if err != nil || !ok {
		return err
	}
	want, err := patchID(t.Commit)
	if err != nil || want == "" {
		return err
	}
	for _, c := range candidates {
		got, err := patchID(c.Hash)
		if err != nil {
			return err
		}
		if got == want {
			f.Match, f.Commit = MatchPatchID, c.Hash
			return nil
		}
	}
	return nil
}
