package cli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bengearig/ralph/internal/config"
	"github.com/bengearig/ralph/internal/errs"
	"github.com/bengearig/ralph/internal/harness"
	"github.com/bengearig/ralph/internal/loop"
	"github.com/bengearig/ralph/internal/looplock"
	"github.com/bengearig/ralph/internal/service"
)

// hookHarness runs inner, then after.
type hookHarness struct {
	inner  *chainHarness
	hook   func()
	before bool
}

func (k hookHarness) Name() string                       { return "fake" }
func (k hookHarness) Command(r harness.Request) []string { return k.inner.Command(r) }
func (k hookHarness) Run(ctx context.Context, r harness.Request) (harness.Result, error) {
	if k.before {
		k.hook()
		return harness.Result{}, ctx.Err()
	}
	res, err := k.inner.Run(ctx, r)
	k.hook()
	return res, err
}

func useHook(t *testing.T, k hookHarness) {
	t.Helper()
	newLoopHarness = func(*config.Config, config.Step) (harness.Harness, config.StepResolution, error) {
		return k, config.StepResolution{Harness: "fake"}, nil
	}
	t.Cleanup(func() { newLoopHarness = harness.ForStep })
}

func indexOrder(t *testing.T, out string, wants ...string) {
	t.Helper()
	at := 0
	for _, w := range wants {
		i := strings.Index(out[at:], w)
		if i < 0 {
			t.Fatalf("output missing %q in order:\n%s", w, out)
		}
		at += i + len(w)
	}
}

func TestLoopChainFull(t *testing.T) {
	h, git := chainFixture(t)
	c := &chainHarness{h: h}
	useChainHarness(t, c)

	fixtureTip := git("rev-parse", "main")
	out, _, err := h.run("", "loop", "--initiative", "I-1", "--merge")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(c.calls, ",") != "T-2,T-3" {
		t.Errorf("calls = %v", c.calls)
	}
	indexOrder(t, out, "next: M-2", "merging M-2", "next: M-3", "merging M-3",
		"initiative I-1 is complete: every milestone is merged or cancelled\n")
	if b := git("branch", "--show-current"); b != "main" {
		t.Errorf("branch = %s", b)
	}
	if n := git("rev-list", "--merges", "--count", fixtureTip+"..main"); n != "0" {
		t.Errorf("merge commits after M-1 = %s", n)
	}
	log := git("log", "--format=%s", "-n", "2")
	if !strings.Contains(log, "T-2") || !strings.Contains(log, "T-3") {
		t.Errorf("base log:\n%s", log)
	}
	if strings.Index(log, "T-3") > strings.Index(log, "T-2") {
		t.Errorf("commits out of order (newest first):\n%s", log)
	}
	if st := git("status", "--porcelain", "--untracked-files=no"); st != "" {
		t.Errorf("working tree dirty: %s", st)
	}
}

func TestLoopChainLeadingUnmerged(t *testing.T) {
	h, git := chainFixture(t)
	c := &chainHarness{h: h}
	useChainHarness(t, c)
	h.ok("loop", "--initiative", "I-1")
	if git("branch", "--show-current") != "main" || strings.Join(c.calls, ",") != "T-2" {
		t.Fatalf("setup: calls %v", c.calls)
	}
	out, _, err := h.run("", "loop", "--initiative", "I-1", "--merge")
	if err != nil {
		t.Fatal(err)
	}
	indexOrder(t, out, "merging M-2", "next: M-3", "merging M-3", "is complete")
	if strings.Join(c.calls, ",") != "T-2,T-3" {
		t.Errorf("calls = %v", c.calls)
	}
}

func TestLoopChainNeverMergesUnfinished(t *testing.T) {
	h, git := chainFixture(t)
	c := &chainHarness{h: h, block: map[string]bool{"T-2": true}}
	useChainHarness(t, c)
	before := git("rev-parse", "main")
	out, _, err := h.run("", "loop", "--initiative", "I-1", "--merge")
	if !errors.Is(err, errs.ErrNeedsHuman) {
		t.Fatalf("err = %v, out = %q", err, out)
	}
	if strings.Contains(out, "merging") || strings.Join(c.calls, ",") != "T-2" {
		t.Errorf("calls %v, out %q", c.calls, out)
	}
	if git("rev-parse", "main") != before {
		t.Error("main moved")
	}
}

func TestLoopChainMergeRefusal(t *testing.T) {
	t.Run("dirty tree", func(t *testing.T) {
		h, _ := chainFixture(t)
		c := &chainHarness{h: h}
		useChainHarness(t, c)
		h.ok("loop", "--initiative", "I-1")
		if err := os.WriteFile(filepath.Join(h.dir, "scratch.txt"), []byte("x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		out, _, err := h.run("", "loop", "--initiative", "I-1", "--merge")
		if !errors.Is(err, errs.ErrNeedsHuman) || !strings.Contains(out, "stopped at M-2:") || !strings.Contains(out, "uncommitted") {
			t.Fatalf("err = %v, out = %q", err, out)
		}
		if strings.Join(c.calls, ",") != "T-2" || !strings.Contains(out, "merging M-2") {
			t.Errorf("calls = %v", c.calls)
		}
	})
	t.Run("base moved", func(t *testing.T) {
		h, git := chainFixture(t)
		c := &chainHarness{h: h}
		useHook(t, hookHarness{inner: c, hook: func() {
			git("switch", "--quiet", "main")
			_ = os.WriteFile(filepath.Join(h.dir, "extra.txt"), []byte("x\n"), 0o644)
			git("add", "extra.txt")
			git("commit", "--quiet", "-m", "extra")
		}})
		out, _, err := h.run("", "loop", "--initiative", "I-1", "--merge")
		if !errors.Is(err, errs.ErrNeedsHuman) || !strings.Contains(out, "stopped at M-2:") {
			t.Fatalf("err = %v, out = %q", err, out)
		}
		if strings.Join(c.calls, ",") != "T-2" {
			t.Errorf("calls = %v", c.calls)
		}
	})
	t.Run("loop lock held is a plain error", func(t *testing.T) {
		h, _ := chainFixture(t)
		c := &chainHarness{h: h}
		useChainHarness(t, c)
		h.ok("loop", "--initiative", "I-1")
		lock, err := looplock.Acquire(filepath.Join(h.dir, ".ralph", "loop.lock"), "test")
		if err != nil {
			t.Fatal(err)
		}
		defer lock.Release()
		_, _, err = h.run("", "loop", "--initiative", "I-1", "--merge")
		if err == nil || errors.Is(err, errs.ErrNeedsHuman) {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestLoopChainInterrupted(t *testing.T) {
	h, git := chainFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c := &chainHarness{h: h}
	useHook(t, hookHarness{inner: c, hook: cancel, before: true})
	var out, errOut strings.Builder
	err := Execute(ctx, Streams{In: strings.NewReader(""), Out: &out, Err: &errOut}, []string{"--project-dir", h.dir, "loop", "--initiative", "I-1", "--merge"})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(out.String(), "interrupted") || strings.Contains(out.String(), "merging") {
		t.Errorf("out = %q", out.String())
	}
	if b := git("branch", "--show-current"); b != "main" {
		t.Errorf("branch = %s", b)
	}
}

func TestLoopChainDryRun(t *testing.T) {
	h, git := chainFixture(t)
	c := &chainHarness{h: h}
	useChainHarness(t, c)
	status := git("status", "--porcelain")
	out := h.ok("loop", "--initiative", "I-1", "--merge", "--dry-run")
	for _, want := range []string{"DISPOSITION", "next: run M-2\n", "later steps depend"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q:\n%s", want, out)
		}
	}
	if len(c.calls) != 0 || git("status", "--porcelain") != status {
		t.Error("dry run changed things")
	}
}

func TestLoopChainForceDoesNotOverrideMergeRefusal(t *testing.T) {
	h, git := chainFixture(t)
	h.ok("milestone", "update", "M-2", "--depends-on", "M-3")
	c := &chainHarness{h: h}
	useHook(t, hookHarness{inner: c, hook: func() {
		git("switch", "--quiet", "main")
		_ = os.WriteFile(filepath.Join(h.dir, "extra.txt"), []byte("x\n"), 0o644)
		git("add", "extra.txt")
		git("commit", "--quiet", "-m", "extra")
	}})
	out, _, err := h.run("", "loop", "--initiative", "I-1", "--merge", "--force")
	if !errors.Is(err, errs.ErrNeedsHuman) || strings.Join(c.calls, ",") != "T-2" || !strings.Contains(out, "stopped at M-2:") {
		t.Fatalf("err = %v, calls %v, out %q", err, c.calls, out)
	}
}

func TestLoopChainNoProgressGuard(t *testing.T) {
	h, _ := chainFixture(t)
	orig := chainOverview
	chainOverview = func(ctx context.Context, svc *service.Service, cfg *config.Config, root string, id int64, force bool) ([]loop.ChainEntry, error) {
		return []loop.ChainEntry{{Milestone: "M-1", Status: "DONE", Disposition: loop.DispositionDoneUnmerged}}, nil
	}
	t.Cleanup(func() { chainOverview = orig })
	_, _, err := h.run("", "loop", "--initiative", "I-1", "--merge")
	if err == nil || !strings.Contains(err.Error(), "no progress") || errors.Is(err, errs.ErrNeedsHuman) {
		t.Fatalf("err = %v", err)
	}
}
