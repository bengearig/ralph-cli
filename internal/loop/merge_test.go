package loop

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/bengearig/ralph/internal/domain"
	"github.com/bengearig/ralph/internal/errs"
	"github.com/bengearig/ralph/internal/harness"
	"github.com/bengearig/ralph/internal/looplock"
	"github.com/bengearig/ralph/internal/service"
)

const mergeBranch = "ralph/oidc-login"

// newMergeEnv loops a one-task milestone to DONE on its branch, then checks
// out main so the merge starts from the base branch.
func newMergeEnv(t *testing.T) *runEnv {
	t.Helper()
	useGitEnv(t)
	files, mID := mergeRepo(t)
	root := t.TempDir()
	restoreTree(t, root, files)
	svc, cfg, st := openStore(t, root, nil, nil)
	return &runEnv{root: root, svc: svc, cfg: cfg, st: st, mID: mID}
}

var (
	mergeOnce  sync.Once
	mergeFiles tree
	mergeMID   int64
)

// mergeRepo returns the repository and database newMergeEnv hands out, built
// once per test process by looping a one-task milestone to DONE. Neither
// holds an absolute path, so any directory can host them.
func mergeRepo(t *testing.T) (tree, int64) {
	t.Helper()
	mergeOnce.Do(func() {
		root, err := os.MkdirTemp("", "ralph-merge-")
		if err != nil {
			t.Fatal(err)
		}
		defer os.RemoveAll(root)
		restoreTree(t, root, initialRepo(t))
		e := openRunEnv(t, root, nil, nil, "a")
		h := &fakeHarness{script: func(_ int, id int64, _ harness.Request) (harness.Result, error) { return e.complete(id) }}
		if _, err := e.run(t, context.Background(), h); err != nil {
			t.Fatalf("setup run: %v\n%s", err, e.out.String())
		}
		if got := e.milestone(t).Status; got != "DONE" {
			t.Fatalf("milestone is %s", got)
		}
		gitCmd(t, root, "switch", "--quiet", "main")
		if err := e.st.Close(); err != nil {
			t.Fatal(err)
		}
		mergeFiles, mergeMID = readTree(t, root), e.mID
	})
	return mergeFiles, mergeMID
}

func (e *runEnv) merge(opts MergeOptions) (MergeResult, error) {
	return Merge(context.Background(), e.svc, e.cfg, e.root, e.mID, opts)
}

type snapshot struct{ branch, head, base, tree string }

func (e *runEnv) snapshot(t *testing.T) snapshot {
	t.Helper()
	heads := strings.Fields(gitCmd(t, e.root, "rev-parse", "HEAD", "main"))
	if len(heads) != 2 {
		t.Fatalf("rev-parse HEAD main: %q", heads)
	}
	return snapshot{
		branch: gitCmd(t, e.root, "branch", "--show-current"),
		head:   heads[0],
		base:   heads[1],
		tree:   gitCmd(t, e.root, "status", "--porcelain"),
	}
}

func (e *runEnv) assertRefusal(t *testing.T, opts MergeOptions, want error, contains ...string) {
	t.Helper()
	before := e.snapshot(t)
	res, err := e.merge(opts)
	if err == nil {
		t.Fatalf("merge succeeded: %+v", res)
	}
	if want != nil && !errors.Is(err, want) {
		t.Errorf("err = %v, want it to wrap %v", err, want)
	}
	if want == nil && errors.Is(err, errs.ErrNeedsHuman) {
		t.Errorf("err = %v, should not be ErrNeedsHuman", err)
	}
	for _, c := range contains {
		if !strings.Contains(err.Error(), c) {
			t.Errorf("err = %q, want it to contain %q", err, c)
		}
	}
	if res.Merged || res.BranchDeleted {
		t.Errorf("result = %+v", res)
	}
	if after := e.snapshot(t); after != before {
		t.Errorf("state changed from %+v to %+v", before, after)
	}
}

func TestMergeFastForwards(t *testing.T) {
	t.Parallel()
	e := newMergeEnv(t)
	oldBase := gitCmd(t, e.root, "rev-parse", "main")
	tip := gitCmd(t, e.root, "rev-parse", mergeBranch)
	res, err := e.merge(MergeOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Merged || res.AlreadyMerged || res.DryRun || res.BranchDeleted || len(res.Warnings) != 0 {
		t.Errorf("result = %+v", res)
	}
	if res.Milestone != "M-1" || res.Branch != mergeBranch || res.BaseBranch != "main" || res.From != oldBase || res.To != tip {
		t.Errorf("result = %+v, want from %s to %s", res, oldBase, tip)
	}
	if b := gitCmd(t, e.root, "branch", "--show-current"); b != "main" {
		t.Errorf("on %q, want main", b)
	}
	if got := gitCmd(t, e.root, "rev-parse", "main"); got != tip {
		t.Errorf("main = %s, want %s", got, tip)
	}
	if gitCmd(t, e.root, "branch", "--list", mergeBranch) == "" {
		t.Error("branch should remain")
	}
	if merges := gitCmd(t, e.root, "rev-list", "--merges", "main"); merges != "" {
		t.Errorf("merge commits: %s", merges)
	}

	before := e.snapshot(t)
	res, err = e.merge(MergeOptions{DeleteBranch: true})
	if err != nil {
		t.Fatal(err)
	}
	if !res.AlreadyMerged || res.Merged || res.BranchDeleted {
		t.Errorf("second result = %+v", res)
	}
	if after := e.snapshot(t); after != before {
		t.Errorf("second merge changed %+v to %+v", before, after)
	}
	if gitCmd(t, e.root, "branch", "--list", mergeBranch) == "" {
		t.Error("an already merged milestone's branch should not be deleted")
	}
}

func TestMergeDeleteBranch(t *testing.T) {
	t.Parallel()
	e := newMergeEnv(t)
	res, err := e.merge(MergeOptions{DeleteBranch: true})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Merged || !res.BranchDeleted || len(res.Warnings) != 0 {
		t.Errorf("result = %+v", res)
	}
	if gitCmd(t, e.root, "branch", "--list", mergeBranch) != "" {
		t.Error("branch should be gone")
	}
}

func TestMergeDeleteRefusedWarns(t *testing.T) {
	t.Parallel()
	e := newMergeEnv(t)
	// The safe delete refuses a branch checked out in another worktree.
	other := filepath.Join(t.TempDir(), "wt")
	gitCmd(t, e.root, "worktree", "add", "--quiet", other, mergeBranch)
	res, err := e.merge(MergeOptions{DeleteBranch: true})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Merged || res.BranchDeleted || len(res.Warnings) != 1 || !strings.Contains(res.Warnings[0], mergeBranch) {
		t.Errorf("result = %+v", res)
	}
	if gitCmd(t, e.root, "branch", "--list", mergeBranch) == "" {
		t.Error("branch should remain")
	}
}

func TestMergeDryRun(t *testing.T) {
	t.Parallel()
	e := newMergeEnv(t)
	oldBase := gitCmd(t, e.root, "rev-parse", "main")
	tip := gitCmd(t, e.root, "rev-parse", mergeBranch)
	before := e.snapshot(t)
	res, err := e.merge(MergeOptions{DryRun: true, DeleteBranch: true})
	if err != nil {
		t.Fatal(err)
	}
	if !res.DryRun || res.Merged || res.BranchDeleted || res.From != oldBase || res.To != tip || res.Branch != mergeBranch {
		t.Errorf("result = %+v", res)
	}
	if after := e.snapshot(t); after != before {
		t.Errorf("dry run changed %+v to %+v", before, after)
	}
	if gitCmd(t, e.root, "branch", "--list", mergeBranch) == "" {
		t.Error("branch should remain")
	}
}

func TestMergeRefusesUnfinishedMilestone(t *testing.T) {
	t.Parallel()
	e := newMergeEnv(t)
	ctx := context.Background()
	tv, err := e.svc.CreateTask(ctx, service.TaskInput{MilestoneID: e.mID, Title: "more", Description: "d", AcceptanceCriteria: "c"})
	if err != nil {
		t.Fatal(err)
	}
	id, _ := domain.ParseID(domain.KindTask, tv.ID)
	if _, err := e.svc.SetTaskStatus(ctx, id, domain.StatusTodo, ""); err != nil {
		t.Fatal(err)
	}
	e.assertRefusal(t, MergeOptions{}, errs.ErrNeedsHuman, "IN_PROGRESS")
}

func TestMergeRefusesArchivedInitiative(t *testing.T) {
	t.Parallel()
	e := newMergeEnv(t)
	if _, err := e.svc.ArchiveInitiative(context.Background(), 1, service.DocumentChoice{}); err != nil {
		t.Fatal(err)
	}
	e.assertRefusal(t, MergeOptions{}, nil, "archived")
}

func TestMergeRefusesHeldLock(t *testing.T) {
	t.Parallel()
	e := newMergeEnv(t)
	lock, err := looplock.Acquire(filepath.Join(e.root, ".ralph", "loop.lock"), "milestone M-9")
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Release()
	e.assertRefusal(t, MergeOptions{}, nil, "M-9", "pid")
}

func TestMergeRefusesDrift(t *testing.T) {
	t.Parallel()
	e := newMergeEnv(t)
	gitCmd(t, e.root, "switch", "--quiet", mergeBranch)
	e.commitFile(t, "extra.txt")
	gitCmd(t, e.root, "switch", "--quiet", "main")
	e.assertRefusal(t, MergeOptions{}, errs.ErrNeedsHuman, "ahead", "ralph milestone reconcile M-1")
}

func TestMergeRefusesDirtyTree(t *testing.T) {
	t.Parallel()
	e := newMergeEnv(t)
	if err := os.WriteFile(filepath.Join(e.root, "scratch.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	e.assertRefusal(t, MergeOptions{}, errs.ErrNeedsHuman, "scratch.txt")
}

func TestMergeRefusesMissingBranches(t *testing.T) {
	t.Parallel()
	t.Run("base", func(t *testing.T) {
		e := newMergeEnv(t)
		e.cfg.PR.BaseBranch = "trunk"
		e.assertRefusal(t, MergeOptions{}, nil, "trunk", "local")
	})
	t.Run("milestone branch", func(t *testing.T) {
		e := newMergeEnv(t)
		gitCmd(t, e.root, "branch", "-m", mergeBranch, "renamed")
		e.assertRefusal(t, MergeOptions{}, errs.ErrNeedsHuman, mergeBranch)
	})
}

func TestMergeRefusesAdvancedBase(t *testing.T) {
	t.Parallel()
	e := newMergeEnv(t)
	e.commitFile(t, "mainline.txt")
	e.assertRefusal(t, MergeOptions{}, errs.ErrNeedsHuman, "rebase")
}

func TestMergeFromAnotherBranch(t *testing.T) {
	t.Parallel()
	e := newMergeEnv(t)
	gitCmd(t, e.root, "switch", "--quiet", "-c", "scratch")
	res, err := e.merge(MergeOptions{})
	if err != nil || !res.Merged {
		t.Fatalf("merge: %+v, %v", res, err)
	}
	if b := gitCmd(t, e.root, "branch", "--show-current"); b != "main" {
		t.Errorf("on %q, want main", b)
	}
}

func TestMergeFromDetachedHead(t *testing.T) {
	t.Parallel()
	e := newMergeEnv(t)
	gitCmd(t, e.root, "switch", "--quiet", "--detach")
	res, err := e.merge(MergeOptions{})
	if err != nil || !res.Merged {
		t.Fatalf("merge: %+v, %v", res, err)
	}
	if b := gitCmd(t, e.root, "branch", "--show-current"); b != "main" {
		t.Errorf("on %q, want main", b)
	}
}

func TestMergeFailureRestoresStart(t *testing.T) {
	t.Parallel()
	// A lock on main's ref passes every check and fails the fast-forward.
	for _, start := range []string{"scratch", "detached"} {
		t.Run(start, func(t *testing.T) {
			e := newMergeEnv(t)
			args := []string{"switch", "--quiet", "-c", "scratch"}
			if start == "detached" {
				args = []string{"switch", "--quiet", "--detach"}
			}
			gitCmd(t, e.root, args...)
			before := e.snapshot(t)
			lock := filepath.Join(e.root, ".git", "refs", "heads", "main.lock")
			if err := os.WriteFile(lock, nil, 0o644); err != nil {
				t.Fatal(err)
			}
			res, err := e.merge(MergeOptions{DeleteBranch: true})
			if err == nil {
				t.Fatalf("merge succeeded: %+v", res)
			}
			if res.Merged || res.BranchDeleted {
				t.Errorf("result = %+v", res)
			}
			os.Remove(lock)
			if after := e.snapshot(t); after != before {
				t.Errorf("state changed from %+v to %+v", before, after)
			}
		})
	}
}
