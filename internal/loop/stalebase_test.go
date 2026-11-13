package loop

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bengearig/ralph/internal/config"
	"github.com/bengearig/ralph/internal/errs"
	"github.com/bengearig/ralph/internal/git"
)

// addRemote gives the environment a bare origin that has main pushed.
func (e *runEnv) addRemote(t *testing.T) string {
	t.Helper()
	bare := t.TempDir()
	restoreTree(t, bare, bareRepo(t))
	gitCmd(t, e.root, "remote", "add", "origin", bare)
	gitCmd(t, e.root, "push", "--quiet", "origin", "main")
	return bare
}

// advanceRemote adds n commits to main on the bare remote.
func advanceRemote(t *testing.T, bare string, n int) {
	t.Helper()
	clone := t.TempDir()
	gitCmd(t, clone, "clone", "--quiet", bare, ".")
	for i := 0; i < n; i++ {
		name := "remote-" + filepath.Base(clone) + string(rune('a'+i)) + ".txt"
		if err := os.WriteFile(filepath.Join(clone, name), []byte("r\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		gitCmd(t, clone, "add", name)
		gitCmd(t, clone, "commit", "--quiet", "-m", "remote "+name)
	}
	gitCmd(t, clone, "push", "--quiet", "origin", "main")
}

func guard(t *testing.T, e *runEnv, mode string, opts BaseGuardOptions) (BaseGuardResult, error) {
	t.Helper()
	e.cfg.PR.StaleBase = mode
	return EnsureFreshBase(context.Background(), e.cfg, e.root, opts)
}

func TestEnsureFreshBaseCurrentAndAhead(t *testing.T) {
	t.Parallel()
	e := newRunEnv(t, "a")
	e.addRemote(t)
	res, err := guard(t, e, config.StaleBaseRefuse, BaseGuardOptions{})
	if err != nil || res.Skipped || res.Synced || len(res.Warnings) != 0 || len(res.Notices) != 0 || res.Status.State != git.BaseCurrent {
		t.Fatalf("current: %+v, %v", res, err)
	}
	e.commitFile(t, "ahead.txt")
	res, err = guard(t, e, config.StaleBaseRefuse, BaseGuardOptions{})
	if err != nil || len(res.Warnings) != 0 || len(res.Notices) != 0 || res.Status.State != git.BaseAhead {
		t.Fatalf("ahead: %+v, %v", res, err)
	}
}

func TestEnsureFreshBaseMissingLocalBase(t *testing.T) {
	t.Parallel()
	e := newRunEnv(t, "a")
	e.addRemote(t)
	gitCmd(t, e.root, "switch", "--quiet", "-c", "work")
	gitCmd(t, e.root, "branch", "-D", "main")
	res, err := guard(t, e, config.StaleBaseRefuse, BaseGuardOptions{})
	if err != nil || len(res.Warnings) != 0 || len(res.Notices) != 0 {
		t.Fatalf("%+v, %v", res, err)
	}
}

func TestEnsureFreshBaseBehindRefuses(t *testing.T) {
	t.Parallel()
	e := newRunEnv(t, "a")
	bare := e.addRemote(t)
	advanceRemote(t, bare, 2)
	before := gitCmd(t, e.root, "rev-parse", "main")
	_, err := guard(t, e, config.StaleBaseRefuse, BaseGuardOptions{})
	if !errors.Is(err, errs.ErrNeedsHuman) {
		t.Fatalf("err = %v, want ErrNeedsHuman", err)
	}
	for _, want := range []string{"2 commit(s)", "ralph sync (or git pull --ff-only origin main)"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("err %q lacks %q", err, want)
		}
	}
	if got := gitCmd(t, e.root, "rev-parse", "main"); got != before {
		t.Errorf("main moved to %s", got)
	}
}

func TestEnsureFreshBaseBehindSyncs(t *testing.T) {
	t.Parallel()
	e := newRunEnv(t, "a")
	bare := e.addRemote(t)
	advanceRemote(t, bare, 2)
	res, err := guard(t, e, config.StaleBaseSync, BaseGuardOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Synced || len(res.Notices) != 1 || res.Notices[0] != "fast-forwarded main to origin/main (2 commit(s))" {
		t.Fatalf("result = %+v", res)
	}
	if tips := strings.Fields(gitCmd(t, e.root, "rev-parse", "main", "origin/main")); len(tips) != 2 || tips[0] != tips[1] {
		t.Errorf("main and origin/main = %v, want the same commit", tips)
	}
}

func TestEnsureFreshBaseSyncDirtyRefuses(t *testing.T) {
	t.Parallel()
	e := newRunEnv(t, "a")
	bare := e.addRemote(t)
	advanceRemote(t, bare, 1)
	if err := os.WriteFile(filepath.Join(e.root, "wip.txt"), []byte("w\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	before := gitCmd(t, e.root, "rev-parse", "main")
	_, err := guard(t, e, config.StaleBaseSync, BaseGuardOptions{})
	if !errors.Is(err, errs.ErrNeedsHuman) || !strings.Contains(err.Error(), "could not fast-forward main") || !strings.Contains(err.Error(), "wip.txt") {
		t.Fatalf("err = %v", err)
	}
	if got := gitCmd(t, e.root, "rev-parse", "main"); got != before {
		t.Errorf("main moved to %s", got)
	}
}

func TestEnsureFreshBaseDivergedRefusesInBothModes(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{config.StaleBaseRefuse, config.StaleBaseSync} {
		e := newRunEnv(t, "a")
		bare := e.addRemote(t)
		advanceRemote(t, bare, 2)
		e.commitFile(t, "local.txt")
		before := gitCmd(t, e.root, "rev-parse", "main")
		_, err := guard(t, e, mode, BaseGuardOptions{})
		if !errors.Is(err, errs.ErrNeedsHuman) || !strings.Contains(err.Error(), "have diverged (1 ahead, 2 behind)") {
			t.Fatalf("%s: err = %v", mode, err)
		}
		if got := gitCmd(t, e.root, "rev-parse", "main"); got != before {
			t.Errorf("%s: main moved to %s", mode, got)
		}
	}
}

func TestEnsureFreshBaseFetchFailures(t *testing.T) {
	t.Parallel()
	e := newRunEnv(t, "a")
	e.addRemote(t)
	e.cfg.PR.Remote = "nowhere"
	_, err := guard(t, e, config.StaleBaseRefuse, BaseGuardOptions{})
	if !errors.Is(err, errs.ErrNeedsHuman) || !strings.Contains(err.Error(), "could not fetch nowhere/main") {
		t.Errorf("unknown remote: %v", err)
	}
	e.cfg.PR.Remote = "origin"
	e.cfg.PR.BaseBranch = "absent"
	_, err = guard(t, e, config.StaleBaseRefuse, BaseGuardOptions{})
	if !errors.Is(err, errs.ErrNeedsHuman) || !strings.Contains(err.Error(), "could not fetch origin/absent") {
		t.Errorf("missing branch: %v", err)
	}
}

func TestEnsureFreshBaseForceSkipsFetch(t *testing.T) {
	t.Parallel()
	e := newRunEnv(t, "a")
	bare := e.addRemote(t)
	remoteBefore := gitCmd(t, e.root, "rev-parse", "origin/main")
	advanceRemote(t, bare, 1)
	res, err := guard(t, e, config.StaleBaseRefuse, BaseGuardOptions{Force: true})
	if err != nil || !res.Skipped {
		t.Fatalf("%+v, %v", res, err)
	}
	if got := gitCmd(t, e.root, "rev-parse", "origin/main"); got != remoteBefore {
		t.Errorf("origin/main moved to %s: Force fetched", got)
	}
}

func TestEnsureFreshBaseDryRun(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{config.StaleBaseRefuse, config.StaleBaseSync} {
		e := newRunEnv(t, "a")
		bare := e.addRemote(t)
		advanceRemote(t, bare, 2)
		localBefore := gitCmd(t, e.root, "rev-parse", "main")
		remoteTip := gitCmd(t, bare, "rev-parse", "main")

		res, err := guard(t, e, mode, BaseGuardOptions{DryRun: true})
		if err != nil || res.Synced || len(res.Warnings) != 1 {
			t.Fatalf("%s behind: %+v, %v", mode, res, err)
		}
		if mode == config.StaleBaseSync {
			if res.Warnings[0] != "would fast-forward main to origin/main (2 commit(s))" {
				t.Errorf("warning = %q", res.Warnings[0])
			}
		} else if !strings.HasPrefix(res.Warnings[0], "would refuse: local main is 2 commit(s) behind origin/main") {
			t.Errorf("warning = %q", res.Warnings[0])
		}
		if got := gitCmd(t, e.root, "rev-parse", "main"); got != localBefore {
			t.Errorf("%s: main moved", mode)
		}
		if got := gitCmd(t, e.root, "rev-parse", "origin/main"); got != remoteTip {
			t.Errorf("%s: dry run did not fetch", mode)
		}

		e.commitFile(t, "local.txt")
		res, err = guard(t, e, mode, BaseGuardOptions{DryRun: true})
		if err != nil || len(res.Warnings) != 1 || !strings.HasPrefix(res.Warnings[0], "would refuse: local main and origin/main have diverged") {
			t.Fatalf("%s diverged: %+v, %v", mode, res, err)
		}

		e.cfg.PR.Remote = "nowhere"
		res, err = guard(t, e, mode, BaseGuardOptions{DryRun: true})
		if err != nil || len(res.Warnings) != 1 || !strings.HasPrefix(res.Warnings[0], "would refuse: could not fetch nowhere/main") {
			t.Fatalf("%s fetch failure: %+v, %v", mode, res, err)
		}
	}
}
