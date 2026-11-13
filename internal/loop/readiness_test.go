package loop

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bengearig/ralph/internal/git"
	"github.com/bengearig/ralph/internal/looplock"
	"github.com/bengearig/ralph/internal/service"
)

type readinessEnv struct {
	*runEnv
	repo     git.Repo
	lockPath string
	m        service.MilestoneView
	tasks    []service.TaskView
}

func newReadinessEnv(t *testing.T) *readinessEnv {
	t.Helper()
	e := newMergeEnv(t)
	gitCmd(t, e.root, "switch", "--quiet", mergeBranch)
	re := &readinessEnv{
		runEnv:   e,
		repo:     git.Repo{Dir: e.root, Exclude: ".ralph"},
		lockPath: filepath.Join(t.TempDir(), "loop.lock"),
	}
	re.reload(t)
	return re
}

func (e *readinessEnv) reload(t *testing.T) {
	t.Helper()
	e.m = e.milestone(t)
	ts, err := e.svc.ListTasks(context.Background(), service.TaskFilter{MilestoneID: e.mID})
	if err != nil {
		t.Fatal(err)
	}
	e.tasks = ts
}

func (e *readinessEnv) check(t *testing.T) []string {
	t.Helper()
	rs, err := Readiness(context.Background(), e.repo, e.lockPath, e.cfg, e.m, e.tasks, mergeBranch)
	if err != nil {
		t.Fatal(err)
	}
	if rs == nil {
		t.Fatal("reasons is nil, want an empty slice")
	}
	return rs
}

func (e *readinessEnv) dirty(t *testing.T, names ...string) {
	t.Helper()
	for _, n := range names {
		if err := os.WriteFile(filepath.Join(e.root, n), []byte("x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func wantReasons(t *testing.T, got []string, contains ...string) {
	t.Helper()
	if len(got) != len(contains) {
		t.Fatalf("reasons = %q, want %d", got, len(contains))
	}
	for i, c := range contains {
		if !strings.Contains(got[i], c) {
			t.Errorf("reason %d = %q, want it to contain %q", i, got[i], c)
		}
	}
}

func TestReadinessReady(t *testing.T) {
	t.Parallel()
	e := newReadinessEnv(t)
	wantReasons(t, e.check(t))
}

func TestReadinessLock(t *testing.T) {
	t.Parallel()
	t.Run("held", func(t *testing.T) {
		e := newReadinessEnv(t)
		lock, err := looplock.Acquire(e.lockPath, "milestone M-9")
		if err != nil {
			t.Fatal(err)
		}
		defer lock.Release()
		got := e.check(t)
		wantReasons(t, got, "loop")
		for _, w := range []string{fmt.Sprintf("pid %d", os.Getpid()), "M-9", "may not have landed"} {
			if !strings.Contains(got[0], w) {
				t.Errorf("reason %q lacks %q", got[0], w)
			}
		}
	})
	t.Run("unparseable holder", func(t *testing.T) {
		e := newReadinessEnv(t)
		lock, err := looplock.Acquire(e.lockPath, "milestone M-9")
		if err != nil {
			t.Fatal(err)
		}
		defer lock.Release()
		if err := os.WriteFile(e.lockPath, []byte("garbage"), 0o644); err != nil {
			t.Fatal(err)
		}
		got := e.check(t)
		wantReasons(t, got, "a loop is running")
		if strings.Contains(got[0], "pid") {
			t.Errorf("generic reason names a pid: %q", got[0])
		}
	})
	t.Run("released", func(t *testing.T) {
		e := newReadinessEnv(t)
		lock, err := looplock.Acquire(e.lockPath, "milestone M-9")
		if err != nil {
			t.Fatal(err)
		}
		if err := lock.Release(); err != nil {
			t.Fatal(err)
		}
		wantReasons(t, e.check(t))
	})
	t.Run("missing file", func(t *testing.T) {
		e := newReadinessEnv(t)
		wantReasons(t, e.check(t))
	})
}

func TestReadinessDirtyTree(t *testing.T) {
	t.Parallel()
	t.Run("on the branch", func(t *testing.T) {
		e := newReadinessEnv(t)
		e.dirty(t, "a.txt", "b.txt")
		got := e.check(t)
		wantReasons(t, got, "uncommitted changes")
		if !strings.Contains(got[0], "a.txt") || !strings.Contains(got[0], "b.txt") || strings.Contains(got[0], "more") {
			t.Errorf("reason = %q", got[0])
		}
	})
	t.Run("long list is capped", func(t *testing.T) {
		e := newReadinessEnv(t)
		var names []string
		for i := 0; i < maxDirtyListed+3; i++ {
			names = append(names, fmt.Sprintf("f%02d.txt", i))
		}
		e.dirty(t, names...)
		wantReasons(t, e.check(t), "and 3 more")
	})
	t.Run("another branch", func(t *testing.T) {
		e := newReadinessEnv(t)
		gitCmd(t, e.root, "switch", "--quiet", "main")
		e.dirty(t, "a.txt")
		wantReasons(t, e.check(t))
	})
	t.Run("detached head", func(t *testing.T) {
		e := newReadinessEnv(t)
		gitCmd(t, e.root, "switch", "--quiet", "--detach")
		e.dirty(t, "a.txt")
		wantReasons(t, e.check(t))
	})
	t.Run("missing branch", func(t *testing.T) {
		e := newReadinessEnv(t)
		e.dirty(t, "a.txt")
		rs, err := Readiness(context.Background(), e.repo, e.lockPath, e.cfg, e.m, e.tasks, "no-such-branch")
		if err != nil || len(rs) != 0 {
			t.Errorf("reasons = %q, err = %v", rs, err)
		}
		rs, err = Readiness(context.Background(), e.repo, e.lockPath, e.cfg, e.m, e.tasks, "")
		if err != nil || len(rs) != 0 {
			t.Errorf("empty branch: reasons = %q, err = %v", rs, err)
		}
	})
}

func TestReadinessUncommittedTasks(t *testing.T) {
	t.Parallel()
	t.Run("done without commit", func(t *testing.T) {
		e := newReadinessEnv(t)
		e.tasks[0].Commit = ""
		got := e.check(t)
		wantReasons(t, got, e.tasks[0].ID)
		for _, w := range []string{"false positive", "--force"} {
			if !strings.Contains(got[0], w) {
				t.Errorf("reason %q lacks %q", got[0], w)
			}
		}
	})
	t.Run("done with commit", func(t *testing.T) {
		e := newReadinessEnv(t)
		if e.tasks[0].Commit == "" {
			t.Fatal("setup task has no commit")
		}
		wantReasons(t, e.check(t))
	})
	t.Run("cancelled without commit", func(t *testing.T) {
		e := newReadinessEnv(t)
		e.tasks = append(e.tasks, service.TaskView{ID: "T-99", Status: "CANCELLED"})
		wantReasons(t, e.check(t))
	})
	t.Run("commit_per_task off", func(t *testing.T) {
		e := newReadinessEnv(t)
		e.tasks[0].Commit = ""
		e.cfg.Loop.CommitPerTask = false
		wantReasons(t, e.check(t))
	})
}

func TestReadinessMilestoneNotDone(t *testing.T) {
	t.Parallel()
	for _, status := range []string{"IN_PROGRESS", "READY", "ARTICULATING"} {
		t.Run(status, func(t *testing.T) {
			e := newReadinessEnv(t)
			e.m.Status = status
			wantReasons(t, e.check(t), fmt.Sprintf("%s is %s, not DONE", e.m.ID, status))
		})
	}
}

func TestReadinessAllReasonsInOrder(t *testing.T) {
	t.Parallel()
	e := newReadinessEnv(t)
	lock, err := looplock.Acquire(e.lockPath, "milestone M-9")
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Release()
	e.dirty(t, "a.txt")
	e.tasks[0].Commit = ""
	e.m.Status = "IN_PROGRESS"
	wantReasons(t, e.check(t), "loop", "uncommitted changes", e.tasks[0].ID, "not DONE")
}

func TestReadinessChangesNothing(t *testing.T) {
	t.Parallel()
	e := newReadinessEnv(t)
	e.dirty(t, "a.txt")
	lock, err := looplock.Acquire(e.lockPath, "milestone M-9")
	if err != nil {
		t.Fatal(err)
	}
	e.check(t)
	before := e.snapshot(t)
	e.check(t)
	if after := e.snapshot(t); after != before {
		t.Errorf("repository changed: %+v -> %+v", before, after)
	}
	if after := e.milestone(t); after.Status != e.m.Status || after.HeadCommit != e.m.HeadCommit {
		t.Errorf("milestone changed: %+v", after)
	}
	if err := lock.Release(); err != nil {
		t.Fatal(err)
	}
	again, err := looplock.Acquire(e.lockPath, "milestone M-9")
	if err != nil {
		t.Fatalf("lock not acquirable after a call: %v", err)
	}
	again.Release()
}
