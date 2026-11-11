package loop

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bengearig/ralph/internal/errs"
	"github.com/bengearig/ralph/internal/git"
	"github.com/bengearig/ralph/internal/looplock"
)

func (e *runEnv) sync(opts SyncOptions) (SyncResult, error) {
	return SyncBase(context.Background(), e.cfg, e.root, opts)
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
