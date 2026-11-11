package loop

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/bengearig/ralph/internal/domain"
	"github.com/bengearig/ralph/internal/drift"
	"github.com/bengearig/ralph/internal/errs"
	"github.com/bengearig/ralph/internal/git"
	"github.com/bengearig/ralph/internal/looplock"
	"github.com/bengearig/ralph/internal/service"
)

func (e *runEnv) sync(opts SyncOptions) (SyncResult, error) {
	return SyncBase(context.Background(), e.svc, e.cfg, e.root, opts)
}

func (e *runEnv) rev(t *testing.T, ref string) string {
	t.Helper()
	return gitCmd(t, e.root, "rev-parse", ref)
}

func TestSyncBaseBehindNotCheckedOut(t *testing.T) {
	e := newRunEnv(t, "a")
	bare := e.addRemote(t)
	gitCmd(t, e.root, "switch", "--quiet", "-c", "work")
	advanceRemote(t, bare, 2)
	from := e.rev(t, "main")
	res, err := e.sync(SyncOptions{})
	if err != nil {
		t.Fatal(err)
	}
	remote := e.rev(t, "origin/main")
	if res.State != git.BaseBehind || res.Behind != 2 || res.Ahead != 0 || !res.Synced || res.DryRun || res.From != from || res.To != remote || res.Remote != "origin" || res.BaseBranch != "main" || res.Warnings == nil {
		t.Fatalf("result = %+v", res)
	}
	if got := e.rev(t, "main"); got != remote {
		t.Errorf("main = %s, want %s", got, remote)
	}
	if got := gitCmd(t, e.root, "branch", "--show-current"); got != "work" {
		t.Errorf("on %q, want work", got)
	}
	if got := gitCmd(t, e.root, "rev-list", "--merges", from+".."+remote); got != "" {
		t.Errorf("merge commits: %s", got)
	}
}

func TestSyncBaseBehindCheckedOut(t *testing.T) {
	e := newRunEnv(t, "a")
	bare := e.addRemote(t)
	advanceRemote(t, bare, 1)
	tree := gitCmd(t, e.root, "status", "--porcelain")
	res, err := e.sync(SyncOptions{})
	if err != nil || !res.Synced || res.Behind != 1 || res.From == res.To {
		t.Fatalf("%+v, %v", res, err)
	}
	if got, want := e.rev(t, "HEAD"), e.rev(t, "origin/main"); got != want {
		t.Errorf("HEAD = %s, want %s", got, want)
	}
	if got := gitCmd(t, e.root, "status", "--porcelain"); got != tree {
		t.Errorf("tree: %s", got)
	}
	if got := gitCmd(t, e.root, "rev-list", "--merges", res.From+".."+res.To); got != "" {
		t.Errorf("merge commits: %s", got)
	}
}

func TestSyncBaseCurrent(t *testing.T) {
	e := newRunEnv(t, "a")
	e.addRemote(t)
	before := e.rev(t, "main")
	res, err := e.sync(SyncOptions{})
	if err != nil || res.State != git.BaseCurrent || res.Synced || res.From != before || res.To != before || len(res.Warnings) != 0 {
		t.Fatalf("%+v, %v", res, err)
	}
}

func TestSyncBaseAhead(t *testing.T) {
	e := newRunEnv(t, "a")
	e.addRemote(t)
	e.commitFile(t, "ahead.txt")
	before, remoteBefore := e.rev(t, "main"), e.rev(t, "origin/main")
	res, err := e.sync(SyncOptions{})
	if err != nil || res.State != git.BaseAhead || res.Ahead != 1 || res.Synced || res.To != before {
		t.Fatalf("%+v, %v", res, err)
	}
	if len(res.Warnings) != 1 || res.Warnings[0] != "main is 1 commit(s) ahead of origin/main; nothing to sync" {
		t.Errorf("warnings = %v", res.Warnings)
	}
	if got := e.rev(t, "main"); got != before {
		t.Errorf("main moved to %s", got)
	}
	if got := gitCmd(t, e.root, "-C", e.root, "ls-remote", "origin", "refs/heads/main"); !strings.HasPrefix(got, remoteBefore) {
		t.Errorf("remote changed: %s", got)
	}
}

func TestSyncBaseRefusals(t *testing.T) {
	t.Run("diverged", func(t *testing.T) {
		for _, dry := range []bool{false, true} {
			e := newRunEnv(t, "a")
			bare := e.addRemote(t)
			advanceRemote(t, bare, 2)
			e.commitFile(t, "local.txt")
			before := e.rev(t, "main")
			_, err := e.sync(SyncOptions{DryRun: dry})
			want := "local main and origin/main have diverged (1 ahead, 2 behind); sync only fast-forwards, so reconcile them by hand, for example git pull --rebase origin main"
			if !errors.Is(err, errs.ErrNeedsHuman) || !strings.Contains(err.Error(), want) {
				t.Fatalf("dry=%v: err = %v", dry, err)
			}
			if got := e.rev(t, "main"); got != before {
				t.Errorf("main moved to %s", got)
			}
		}
	})
	t.Run("dirty checked-out base", func(t *testing.T) {
		for _, dry := range []bool{false, true} {
			e := newRunEnv(t, "a")
			bare := e.addRemote(t)
			advanceRemote(t, bare, 1)
			if err := os.WriteFile(filepath.Join(e.root, "wip.txt"), []byte("w\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(e.root, "README.md"), []byte("changed\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			gitCmd(t, e.root, "add", "README.md")
			before, tree := e.rev(t, "main"), gitCmd(t, e.root, "status", "--porcelain")
			res, err := e.sync(SyncOptions{DryRun: dry})
			if dry {
				if err != nil || res.Synced {
					t.Fatalf("dry run: %+v, %v", res, err)
				}
			} else if !errors.Is(err, errs.ErrNeedsHuman) || !strings.Contains(err.Error(), "could not fast-forward main") {
				t.Fatalf("err = %v", err)
			}
			if got := e.rev(t, "main"); got != before {
				t.Errorf("main moved to %s", got)
			}
			if got := gitCmd(t, e.root, "status", "--porcelain"); got != tree {
				t.Errorf("tree changed: %s", got)
			}
		}
	})
	t.Run("fetch failure", func(t *testing.T) {
		for _, dry := range []bool{false, true} {
			e := newRunEnv(t, "a")
			e.addRemote(t)
			e.cfg.PR.Remote = "nowhere"
			if _, err := e.sync(SyncOptions{DryRun: dry}); !errors.Is(err, errs.ErrNeedsHuman) || !strings.Contains(err.Error(), "could not fetch nowhere/main") {
				t.Errorf("unknown remote: %v", err)
			}
			e.cfg.PR.Remote, e.cfg.PR.BaseBranch = "origin", "absent"
			if _, err := e.sync(SyncOptions{DryRun: dry}); !errors.Is(err, errs.ErrNeedsHuman) || !strings.Contains(err.Error(), "could not fetch origin/absent") {
				t.Errorf("missing branch: %v", err)
			}
		}
	})
	t.Run("missing local base", func(t *testing.T) {
		e := newRunEnv(t, "a")
		e.addRemote(t)
		gitCmd(t, e.root, "switch", "--quiet", "-c", "work")
		gitCmd(t, e.root, "branch", "-D", "main")
		_, err := e.sync(SyncOptions{})
		if err == nil || errors.Is(err, errs.ErrNeedsHuman) || !strings.Contains(err.Error(), "base branch main does not exist locally; create it with git before syncing") {
			t.Fatalf("err = %v", err)
		}
		if gitCmd(t, e.root, "branch", "--list", "main") != "" {
			t.Error("sync created the branch")
		}
	})
}

func TestSyncBaseRefusesHeldLock(t *testing.T) {
	e := newRunEnv(t, "a")
	bare := e.addRemote(t)
	advanceRemote(t, bare, 1)
	trackingBefore := e.rev(t, "origin/main")
	lock, err := looplock.Acquire(filepath.Join(e.root, ".ralph", "loop.lock"), "milestone M-9")
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Release()
	_, err = e.sync(SyncOptions{})
	if err == nil || errors.Is(err, errs.ErrNeedsHuman) || !strings.Contains(err.Error(), "M-9") || !strings.Contains(err.Error(), "pid") {
		t.Fatalf("err = %v", err)
	}
	if got := e.rev(t, "origin/main"); got != trackingBefore {
		t.Errorf("remote-tracking ref moved to %s", got)
	}
}

func TestSyncBaseDryRunBehind(t *testing.T) {
	e := newRunEnv(t, "a")
	bare := e.addRemote(t)
	advanceRemote(t, bare, 2)
	before, trackingBefore := e.rev(t, "main"), e.rev(t, "origin/main")
	tree := gitCmd(t, e.root, "status", "--porcelain")
	res, err := e.sync(SyncOptions{DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	tracking := e.rev(t, "origin/main")
	if tracking == trackingBefore {
		t.Error("the fetch should update the remote-tracking ref")
	}
	if !res.DryRun || res.Synced || res.State != git.BaseBehind || res.Behind != 2 || res.From != before || res.To != before || res.RemoteTip != tracking {
		t.Fatalf("result = %+v", res)
	}
	if got := e.rev(t, "main"); got != before {
		t.Errorf("main moved to %s", got)
	}
	if got := gitCmd(t, e.root, "status", "--porcelain"); got != tree {
		t.Errorf("tree: %s", got)
	}
}

// mergeOnRemote publishes the local branches to the bare remote and, through a
// clone, merges each into main there: ordinary merge, or a squash commit that
// carries the given trailer lines.
func (e *runEnv) mergeOnRemote(t *testing.T, bare string, merges map[string]string) {
	t.Helper()
	clone := t.TempDir()
	gitCmd(t, clone, "clone", "--quiet", bare, ".")
	branches := make([]string, 0, len(merges))
	for b := range merges {
		branches = append(branches, b)
	}
	slices.Sort(branches)
	for _, b := range branches {
		gitCmd(t, e.root, "push", "--quiet", "origin", "refs/heads/"+b+":refs/heads/"+b)
		gitCmd(t, clone, "fetch", "--quiet", "origin", b)
		if trailer := merges[b]; trailer != "" {
			gitCmd(t, clone, "merge", "--squash", "FETCH_HEAD")
			gitCmd(t, clone, "commit", "--quiet", "-m", "squash "+b+"\n\n"+trailer)
		} else {
			gitCmd(t, clone, "merge", "--quiet", "--no-ff", "-m", "merge "+b, "FETCH_HEAD")
		}
	}
	gitCmd(t, clone, "push", "--quiet", "origin", "main")
}

func (e *runEnv) doneMilestone(t *testing.T, title, branch string) (int64, string) {
	t.Helper()
	id := e.depMilestone(t, title, doneSteps...)
	e.depBranch(t, id, branch)
	ts, err := e.svc.ListTasks(context.Background(), service.TaskFilter{MilestoneID: id})
	if err != nil || len(ts) != 1 {
		t.Fatalf("tasks = %v, %v", ts, err)
	}
	return id, ts[0].ID
}

func (e *runEnv) mergeLocally(t *testing.T, branch string) {
	t.Helper()
	gitCmd(t, e.root, "merge", "--quiet", "--no-ff", "-m", "merge "+branch, branch)
}

// newMergedEnv sets up milestones around a sync: M-1 merged on the remote by
// an ordinary merge, M-2 by squash, M-3 merged before the sync, and M-4 DONE
// but unmerged; M-5 is not DONE, M-6 is cancelled, and M-7 is in an archived
// initiative, all three merged on the remote.
func newMergedEnv(t *testing.T) (e *runEnv, bare string, merges map[string]string) {
	t.Helper()
	e = newMergeEnv(t)
	ctx := context.Background()
	_, squashTask := e.doneMilestone(t, "squashed", "ralph/squashed")
	e.doneMilestone(t, "premerged", "ralph/premerged")
	e.mergeLocally(t, "ralph/premerged")
	e.doneMilestone(t, "unmerged", "ralph/unmerged")

	open := e.depMilestone(t, "open", domain.StatusTodo)
	e.depBranch(t, open, "ralph/open")
	cancelled := e.depMilestone(t, "cancelled", domain.StatusCancelled)
	e.depBranch(t, cancelled, "ralph/cancelled")

	i, err := e.svc.CreateInitiative(ctx, service.InitiativeInput{Title: "Old"})
	if err != nil {
		t.Fatal(err)
	}
	iID, _ := domain.ParseID(domain.KindInitiative, i.ID)
	m, err := e.svc.CreateMilestone(ctx, service.MilestoneInput{InitiativeID: iID, Title: "archived"})
	if err != nil {
		t.Fatal(err)
	}
	aID, _ := domain.ParseID(domain.KindMilestone, m.ID)
	tv, err := e.svc.CreateTask(ctx, service.TaskInput{MilestoneID: aID, Title: "t", Description: "d", AcceptanceCriteria: "c"})
	if err != nil {
		t.Fatal(err)
	}
	tid, _ := domain.ParseID(domain.KindTask, tv.ID)
	for _, s := range doneSteps {
		if _, err := e.svc.SetTaskStatus(ctx, tid, s, "r"); err != nil {
			t.Fatal(err)
		}
	}
	e.depBranch(t, aID, "ralph/archived")
	if _, err := e.svc.ArchiveInitiative(ctx, iID, service.DocumentChoice{}); err != nil {
		t.Fatal(err)
	}

	bare = e.addRemote(t)
	merges = map[string]string{
		mergeBranch: "", "ralph/squashed": "Ralph-Task: " + squashTask,
		"ralph/open": "", "ralph/cancelled": "", "ralph/archived": "",
	}
	e.mergeOnRemote(t, bare, merges)
	return e, bare, merges
}

func wantMerged() []MergedMilestone {
	return []MergedMilestone{
		{ID: "M-1", Branch: mergeBranch, BranchExists: true, MergedBy: drift.MergedByAncestry},
		{ID: "M-2", Branch: "ralph/squashed", BranchExists: true, MergedBy: drift.MergedByTasks},
	}
}

func TestSyncBaseReportsNewlyMerged(t *testing.T) {
	e, _, _ := newMergedEnv(t)
	res, err := e.sync(SyncOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Synced || len(res.Warnings) != 0 || !reflect.DeepEqual(res.NewlyMerged, wantMerged()) {
		t.Fatalf("result = %+v", res)
	}
}

func TestSyncBaseDryRunReportsNewlyMerged(t *testing.T) {
	e, _, _ := newMergedEnv(t)
	before := e.rev(t, "main")
	res, err := e.sync(SyncOptions{DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if res.Synced || len(res.Warnings) != 0 || !reflect.DeepEqual(res.NewlyMerged, wantMerged()) {
		t.Fatalf("result = %+v", res)
	}
	if got := e.rev(t, "main"); got != before {
		t.Errorf("main moved to %s", got)
	}
}

func TestSyncBaseNewlyMergedDeletedBranch(t *testing.T) {
	e, _, _ := newMergedEnv(t)
	gitCmd(t, e.root, "branch", "-D", mergeBranch)
	res, err := e.sync(SyncOptions{})
	if err != nil {
		t.Fatal(err)
	}
	want := wantMerged()
	want[0].BranchExists = false
	if len(res.Warnings) != 0 || !reflect.DeepEqual(res.NewlyMerged, want) {
		t.Fatalf("result = %+v", res)
	}
}

func TestSyncBaseNewlyMergedIndependentOfCheckout(t *testing.T) {
	e, _, _ := newMergedEnv(t)
	gitCmd(t, e.root, "switch", "--quiet", "ralph/unmerged")
	res, err := e.sync(SyncOptions{})
	if err != nil || !reflect.DeepEqual(res.NewlyMerged, wantMerged()) {
		t.Fatalf("%+v, %v", res, err)
	}
}

func TestSyncBaseNoReportWithoutSync(t *testing.T) {
	t.Run("current", func(t *testing.T) {
		e := newMergeEnv(t)
		e.addRemote(t)
		res, err := e.sync(SyncOptions{})
		if err != nil || res.NewlyMerged == nil || len(res.NewlyMerged) != 0 {
			t.Fatalf("%+v, %v", res, err)
		}
	})
	t.Run("ahead", func(t *testing.T) {
		e := newMergeEnv(t)
		e.addRemote(t)
		e.commitFile(t, "ahead.txt")
		res, err := e.sync(SyncOptions{})
		if err != nil || res.NewlyMerged == nil || len(res.NewlyMerged) != 0 {
			t.Fatalf("%+v, %v", res, err)
		}
	})
	t.Run("refusal", func(t *testing.T) {
		e, _, _ := newMergedEnv(t)
		e.commitFile(t, "local.txt")
		res, err := e.sync(SyncOptions{})
		if !errors.Is(err, errs.ErrNeedsHuman) || res.NewlyMerged == nil || len(res.NewlyMerged) != 0 {
			t.Fatalf("%+v, %v", res, err)
		}
	})
}

func TestNewlyMergedSkipsFailingMilestone(t *testing.T) {
	e, _, _ := newMergedEnv(t)
	from := e.rev(t, "main")
	gitCmd(t, e.root, "fetch", "--quiet", "origin", "main")
	to := e.rev(t, "origin/main")
	e.doneMilestone(t, "broken", "ralph/broken")
	tip := e.rev(t, "ralph/broken")
	object := filepath.Join(e.root, ".git", "objects", tip[:2], tip[2:])
	if err := os.Chmod(object, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(object, []byte("corrupt"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, warnings, err := NewlyMerged(context.Background(), e.svc, e.cfg, e.root, from, to)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, wantMerged()) {
		t.Errorf("merged = %+v", got)
	}
	if len(warnings) != 1 || !strings.HasPrefix(warnings[0], "could not check M-8: ") {
		t.Errorf("warnings = %v", warnings)
	}
}
