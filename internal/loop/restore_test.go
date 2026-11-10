package loop

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bengearig/ralph/internal/domain"
	"github.com/bengearig/ralph/internal/errs"
	"github.com/bengearig/ralph/internal/harness"
)

const milestoneBranch = "ralph/oidc-login"

func (e *runEnv) currentBranch(t *testing.T) string {
	t.Helper()
	return gitCmd(t, e.root, "branch", "--show-current")
}

func (e *runEnv) blockWith(extra func()) *fakeHarness {
	return &fakeHarness{script: func(_ int, id int64, _ harness.Request) (harness.Result, error) {
		if extra != nil {
			extra()
		}
		_, err := e.svc.SetTaskStatus(context.Background(), id, domain.StatusBlocked, "stuck")
		return harness.Result{Completed: true}, err
	}}
}

func TestRunReturnsToStartOnSuccess(t *testing.T) {
	e := newRunEnv(t, "a")
	h := &fakeHarness{script: func(_ int, id int64, _ harness.Request) (harness.Result, error) { return e.complete(id) }}
	out, err := e.runRaw(t, context.Background(), h)
	if err != nil || out.StopReason != StopCompleted {
		t.Fatalf("out = %+v, err = %v", out, err)
	}
	if b := e.currentBranch(t); b != "main" {
		t.Errorf("branch = %q", b)
	}
	if log := gitCmd(t, e.root, "log", "--format=%s", "main.."+milestoneBranch); log != "T-1: a" {
		t.Errorf("milestone commits = %q", log)
	}
	if !strings.Contains(e.out.String(), "returned to main\n") {
		t.Errorf("feed:\n%s", e.out.String())
	}
}

func TestRunReturnsToStartOnEveryExit(t *testing.T) {
	tests := map[string]struct {
		setup func(e *runEnv) harness.Harness
		check func(t *testing.T, err error)
	}{
		"needs a person": {
			setup: func(e *runEnv) harness.Harness { return e.blockWith(nil) },
			check: func(t *testing.T, err error) {
				if !errors.Is(err, errs.ErrNeedsHuman) {
					t.Errorf("err = %v", err)
				}
			},
		},
		"iteration limit": {
			setup: func(e *runEnv) harness.Harness {
				e.cfg.Loop.MaxIterations = 1
				return &fakeHarness{script: func(_ int, id int64, _ harness.Request) (harness.Result, error) { return e.complete(id) }}
			},
			check: func(t *testing.T, err error) {
				if !errors.Is(err, errs.ErrNeedsHuman) || !strings.Contains(err.Error(), "max_iterations") {
					t.Errorf("err = %v", err)
				}
			},
		},
		"harness failure": {
			setup: func(e *runEnv) harness.Harness {
				return &fakeHarness{script: func(int, int64, harness.Request) (harness.Result, error) {
					return harness.Result{}, errors.New("agent crashed")
				}}
			},
			check: func(t *testing.T, err error) {
				if err == nil || !strings.Contains(err.Error(), "agent crashed") {
					t.Errorf("err = %v", err)
				}
			},
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			e := newRunEnv(t, "a", "b")
			_, err := e.runRaw(t, context.Background(), tc.setup(e))
			tc.check(t, err)
			if b := e.currentBranch(t); b != "main" {
				t.Errorf("branch = %q\n%s", b, e.out.String())
			}
			if !strings.Contains(e.out.String(), "returned to main") {
				t.Errorf("feed:\n%s", e.out.String())
			}
		})
	}

	t.Run("cancelled", func(t *testing.T) {
		e := newRunEnv(t, "a")
		ctx, cancel := context.WithCancel(context.Background())
		h := &fakeHarness{script: func(int, int64, harness.Request) (harness.Result, error) {
			cancel()
			return harness.Result{}, ctx.Err()
		}}
		out, err := e.runRaw(t, ctx, h)
		if !errors.Is(err, context.Canceled) || out.StopReason != StopInterrupted {
			t.Fatalf("out = %+v, err = %v", out, err)
		}
		if b := e.currentBranch(t); b != "main" {
			t.Errorf("branch = %q", b)
		}
	})
}

func TestRunReturnsToOtherStart(t *testing.T) {
	t.Run("third branch", func(t *testing.T) {
		e := newRunEnv(t, "a")
		gitCmd(t, e.root, "switch", "--quiet", "-c", "work")
		if _, err := e.runRaw(t, context.Background(), e.blockWith(nil)); !errors.Is(err, errs.ErrNeedsHuman) {
			t.Fatal(err)
		}
		if b := e.currentBranch(t); b != "work" {
			t.Errorf("branch = %q", b)
		}
	})

	t.Run("detached head", func(t *testing.T) {
		e := newRunEnv(t, "a")
		gitCmd(t, e.root, "switch", "--quiet", "--detach")
		start := gitCmd(t, e.root, "rev-parse", "HEAD")
		if _, err := e.runRaw(t, context.Background(), e.blockWith(nil)); !errors.Is(err, errs.ErrNeedsHuman) {
			t.Fatal(err)
		}
		if b := e.currentBranch(t); b != "" {
			t.Errorf("branch = %q, want detached", b)
		}
		if head := gitCmd(t, e.root, "rev-parse", "HEAD"); head != start {
			t.Errorf("HEAD = %s, want %s", head, start)
		}
		if !strings.Contains(e.out.String(), "returned to "+start[:7]) {
			t.Errorf("feed:\n%s", e.out.String())
		}
	})
}

func TestRunOnMilestoneBranchSwitchesNothing(t *testing.T) {
	e := newRunEnv(t, "a")
	gitCmd(t, e.root, "switch", "--quiet", "-c", milestoneBranch)
	h := &fakeHarness{script: func(_ int, id int64, _ harness.Request) (harness.Result, error) { return e.complete(id) }}
	if _, err := e.runRaw(t, context.Background(), h); err != nil {
		t.Fatal(err)
	}
	if b := e.currentBranch(t); b != milestoneBranch {
		t.Errorf("branch = %q", b)
	}
	if strings.Contains(e.out.String(), "returned to") {
		t.Errorf("feed:\n%s", e.out.String())
	}
}

func TestRunLeavesDirtyTreeOnMilestoneBranch(t *testing.T) {
	e := newRunEnv(t, "a")
	h := e.blockWith(func() {
		os.WriteFile(filepath.Join(e.root, "partial.txt"), []byte("x\n"), 0o644)
	})
	out, err := e.runRaw(t, context.Background(), h)
	if !errors.Is(err, errs.ErrNeedsHuman) || out.StopReason != StopNeedsHuman {
		t.Fatalf("out = %+v, err = %v", out, err)
	}
	if b := e.currentBranch(t); b != milestoneBranch {
		t.Errorf("branch = %q", b)
	}
	want := "! left on " + milestoneBranch + ": uncommitted changes remain (switch back to main yourself)\n"
	if !strings.Contains(e.out.String(), want) {
		t.Errorf("feed lacks %q:\n%s", want, e.out.String())
	}
}

func TestRunStartBranchDeletedDuringRun(t *testing.T) {
	e := newRunEnv(t, "a")
	gitCmd(t, e.root, "switch", "--quiet", "-c", "work")
	start := gitCmd(t, e.root, "rev-parse", "HEAD")
	h := e.blockWith(func() { gitCmd(t, e.root, "branch", "-D", "work") })
	out, err := e.runRaw(t, context.Background(), h)
	if !errors.Is(err, errs.ErrNeedsHuman) || out.StopReason != StopNeedsHuman {
		t.Fatalf("out = %+v, err = %v", out, err)
	}
	if b := e.currentBranch(t); b != "" {
		t.Errorf("branch = %q, want detached", b)
	}
	if head := gitCmd(t, e.root, "rev-parse", "HEAD"); head != start {
		t.Errorf("HEAD = %s, want %s", head, start)
	}
}

func TestRestoreStartMissingBranchAndCommit(t *testing.T) {
	e := newRunEnv(t)
	ctx := context.Background()
	repo, start, ok := readStart(ctx, e.root)
	if !ok {
		t.Fatal("readStart failed")
	}
	gitCmd(t, e.root, "switch", "--quiet", "-c", milestoneBranch)
	gitCmd(t, e.root, "branch", "-D", "main")
	start.commit = strings.Repeat("0", 40)
	restoreStart(ctx, repo, start, milestoneBranch, feed{w: &e.out})
	if b := e.currentBranch(t); b != milestoneBranch {
		t.Errorf("branch = %q", b)
	}
	if !strings.Contains(e.out.String(), "! could not return to main: it no longer exists\n") {
		t.Errorf("feed:\n%s", e.out.String())
	}
}

func TestRunFailedSwitchKeepsOutcome(t *testing.T) {
	e := newRunEnv(t, "a")
	h := e.blockWith(func() {
		e.commitFile(t, "agent.txt")
		os.WriteFile(filepath.Join(e.root, ".git", "index.lock"), nil, 0o644)
	})
	out, err := e.runRaw(t, context.Background(), h)
	if !errors.Is(err, errs.ErrNeedsHuman) || out.StopReason != StopNeedsHuman {
		t.Fatalf("out = %+v, err = %v", out, err)
	}
	if !strings.Contains(e.out.String(), "! could not return to main: ") {
		t.Errorf("feed:\n%s", e.out.String())
	}
	if b := e.currentBranch(t); b != milestoneBranch {
		t.Errorf("branch = %q", b)
	}
}
