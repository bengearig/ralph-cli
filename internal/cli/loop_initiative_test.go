package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/bengearig/ralph/internal/config"
	"github.com/bengearig/ralph/internal/domain"
	"github.com/bengearig/ralph/internal/errs"
	"github.com/bengearig/ralph/internal/harness"
	"github.com/bengearig/ralph/internal/service"
)

var taskInPrompt = regexp.MustCompile(`Your task: (T-\d+)`)

// chainHarness completes each task it is asked to work, or blocks the tasks listed in block.
type chainHarness struct {
	h     *testEnv
	calls []string
	block map[string]bool
}

func (c *chainHarness) Name() string                     { return "fake" }
func (c *chainHarness) Command(harness.Request) []string { return []string{"fake"} }
func (c *chainHarness) Run(_ context.Context, req harness.Request) (harness.Result, error) {
	m := taskInPrompt.FindStringSubmatch(req.Prompt)
	if m == nil {
		return harness.Result{}, errors.New("prompt names no task")
	}
	id := m[1]
	c.calls = append(c.calls, id)
	fmt.Fprintln(req.Transcript, `{"type":"result","subtype":"success"}`)
	if c.block[id] {
		c.h.ok("task", "status", id, "BLOCKED", "--reason", "stuck")
		return harness.Result{Completed: true, Subtype: "success"}, nil
	}
	if err := os.WriteFile(filepath.Join(c.h.dir, strings.ToLower(id)+".txt"), []byte("done\n"), 0o644); err != nil {
		return harness.Result{}, err
	}
	c.h.ok("task", "status", id, "DONE", "--reason", "verified")
	return harness.Result{Completed: true, Subtype: "success"}, nil
}

func useChainHarness(t *testing.T, c *chainHarness) {
	t.Helper()
	newLoopHarness = func(cfg *config.Config, step config.Step) (harness.Harness, config.StepResolution, error) {
		return c, config.StepResolution{Harness: "fake"}, nil
	}
	t.Cleanup(func() { newLoopHarness = harness.ForStep })
}

// chainFixture: M-1 DONE and merged, M-2 READY (depends on M-1), M-3 READY.
func chainFixture(t *testing.T) (*testEnv, func(args ...string) string) {
	t.Helper()
	h, git, commit := newGitTestEnv(t)
	if err := os.WriteFile(filepath.Join(h.dir, ".gitignore"), []byte("xdg/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git("add", ".gitignore")
	git("commit", "--quiet", "-m", "ignore")
	h.ok("initiative", "create", "--title", "SSO")
	for _, title := range []string{"One", "Two", "Three"} {
		h.ok("milestone", "create", "--initiative", "I-1", "--title", title)
	}
	h.ok("milestone", "update", "M-2", "--depends-on", "M-1")
	for i, m := range []string{"M-1", "M-2", "M-3"} {
		h.ok("task", "create", "--milestone", m, "--title", "Work "+m, "--description", "d", "--criteria", "c")
		task := fmt.Sprintf("T-%d", i+1)
		h.ok("task", "status", task, "TODO")
		if m == "M-1" {
			h.ok("task", "status", task, "IN_PROGRESS")
			h.ok("task", "status", task, "DONE", "--reason", "done")
		}
	}
	base := git("rev-parse", "HEAD")
	git("switch", "--quiet", "-c", "ralph/one")
	head := commit("one.txt", "one work")
	git("switch", "--quiet", "main")
	git("merge", "--quiet", "--no-ff", "-m", "merge one", "ralph/one")
	cfg, err := config.Defaults()
	if err != nil {
		t.Fatal(err)
	}
	cfg.Embeddings.Provider = config.EmbeddingsNone
	svc, closeFn, err := service.Open(context.Background(), cfg, h.dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer closeFn()
	if err := svc.RecordMilestoneGit(context.Background(), 1, domain.MilestoneGit{Branch: "ralph/one", BaseCommit: base, HeadCommit: head}); err != nil {
		t.Fatal(err)
	}
	return h, git
}

func TestLoopRequiresExactlyOneOfMilestoneAndInitiative(t *testing.T) {
	h := newTestEnv(t, true)
	for _, args := range [][]string{
		{"loop"},
		{"loop", "--milestone", "M-1", "--initiative", "I-1"},
	} {
		_, _, err := h.run("", args...)
		if err == nil || errors.Is(err, errs.ErrNeedsHuman) || !strings.Contains(err.Error(), "milestone") || !strings.Contains(err.Error(), "initiative") {
			t.Errorf("%v: err = %v", args, err)
		}
	}
	if _, _, err := h.run("", "loop", "--milestone", "M-1", "--merge"); err == nil || err.Error() != "--merge requires --initiative" || errors.Is(err, errs.ErrNeedsHuman) {
		t.Errorf("--merge err = %v", err)
	}
}

func TestLoopInitiativeDryRun(t *testing.T) {
	h, git := chainFixture(t)
	c := &chainHarness{h: h}
	useChainHarness(t, c)
	status := git("status", "--porcelain")
	branch := git("branch", "--show-current")

	out := h.ok("loop", "--initiative", "I-1", "--dry-run")
	for _, want := range []string{"ID", "DISPOSITION", "M-1", "merged", "M-2", "ready", "M-3", "next: run M-2\n"} {
		if !strings.Contains(out, want) {
			t.Errorf("dry run output missing %q:\n%s", want, out)
		}
	}
	var res struct {
		Initiative string `json:"initiative"`
		Entries    []struct {
			Milestone   string `json:"milestone"`
			Disposition string `json:"disposition"`
		} `json:"entries"`
		Next struct {
			Action    string `json:"action"`
			Milestone string `json:"milestone"`
		} `json:"next"`
	}
	h.json(&res, "loop", "--initiative", "I-1", "--dry-run")
	if res.Initiative != "I-1" || len(res.Entries) != 3 || res.Entries[0].Disposition != "merged" || res.Next.Action != "run" || res.Next.Milestone != "M-2" {
		t.Errorf("json = %+v", res)
	}
	if len(c.calls) != 0 || git("status", "--porcelain") != status || git("branch", "--show-current") != branch || git("branch", "--list", "ralph/two") != "" {
		t.Errorf("dry run changed things: calls %v", c.calls)
	}
	if got := h.ok("task", "get", "T-2"); !strings.Contains(got, "TODO") {
		t.Errorf("T-2 changed: %s", got)
	}

	h.ok("task", "status", "T-2", "IN_PROGRESS")
	h.ok("task", "status", "T-2", "BLOCKED", "--reason", "stuck")
	out, _, err := h.run("", "loop", "--initiative", "I-1", "--dry-run")
	if err != nil || !strings.Contains(out, "next: stop at M-2: has BLOCKED tasks; unblock them, then rerun") {
		t.Errorf("stop dry run: err = %v, out = %q", err, out)
	}
}

func TestLoopInitiativeRunsOneMilestoneAtATime(t *testing.T) {
	h, git := chainFixture(t)
	c := &chainHarness{h: h}
	useChainHarness(t, c)

	out, _, err := h.run("", "loop", "--initiative", "I-1")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(c.calls, ",") != "T-2" {
		t.Errorf("harness calls = %v", c.calls)
	}
	want := "M-2 is DONE; review it, then merge: ralph milestone merge M-2; run ralph loop --initiative I-1 again for the next one"
	if !strings.Contains(out, want) {
		t.Errorf("output missing %q:\n%s", want, out)
	}
	if b := git("branch", "--show-current"); b != "main" {
		t.Errorf("branch = %s, want main", b)
	}

	out, _, err = h.run("", "loop", "--initiative", "I-1")
	if !errors.Is(err, errs.ErrNeedsHuman) || !strings.Contains(out, "stopped at M-2: needs a merge; ralph milestone merge M-2") {
		t.Errorf("second run: err = %v, out = %q", err, out)
	}
	if len(c.calls) != 1 {
		t.Errorf("harness calls = %v", c.calls)
	}
}

func TestLoopInitiativeStops(t *testing.T) {
	t.Run("proposed", func(t *testing.T) {
		h, _ := chainFixture(t)
		h.ok("task", "status", "T-2", "PROPOSED")
		out, _, err := h.run("", "loop", "--initiative", "I-1")
		if !errors.Is(err, errs.ErrNeedsHuman) || !strings.Contains(out, "stopped at M-2: has PROPOSED tasks; /ralph:articulate M-2") {
			t.Errorf("err = %v, out = %q", err, out)
		}
	})
	t.Run("blocked", func(t *testing.T) {
		h, _ := chainFixture(t)
		h.ok("task", "status", "T-2", "IN_PROGRESS")
		h.ok("task", "status", "T-2", "BLOCKED", "--reason", "stuck")
		_, _, err := h.run("", "loop", "--initiative", "I-1")
		if !errors.Is(err, errs.ErrNeedsHuman) || !strings.Contains(err.Error(), "M-2") {
			t.Errorf("err = %v", err)
		}
	})
	t.Run("waiting and force", func(t *testing.T) {
		h, _ := chainFixture(t)
		c := &chainHarness{h: h}
		useChainHarness(t, c)
		h.ok("milestone", "update", "M-2", "--depends-on", "M-3")
		out, _, err := h.run("", "loop", "--initiative", "I-1")
		if !errors.Is(err, errs.ErrNeedsHuman) || !strings.Contains(out, "stopped at M-2: M-2 depends on M-3, which is READY, not DONE; finish it first: ralph loop --milestone M-3") {
			t.Errorf("err = %v, out = %q", err, out)
		}
		if len(c.calls) != 0 {
			t.Errorf("harness calls = %v", c.calls)
		}
		out, _, err = h.run("", "loop", "--initiative", "I-1", "--force")
		if err != nil || strings.Join(c.calls, ",") != "T-2" || !strings.Contains(out, "M-2 is DONE; review it") {
			t.Errorf("forced: err = %v, calls %v, out = %q", err, c.calls, out)
		}
	})
}

func TestLoopInitiativeComplete(t *testing.T) {
	h, _ := chainFixture(t)
	h.ok("task", "status", "T-2", "CANCELLED")
	h.ok("task", "status", "T-3", "CANCELLED")
	out, _, err := h.run("", "loop", "--initiative", "I-1")
	if err != nil || out != "initiative I-1 is complete: every milestone is merged or cancelled\n" {
		t.Errorf("err = %v, out = %q", err, out)
	}
	h.ok("initiative", "create", "--title", "Empty")
	out, _, err = h.run("", "loop", "--initiative", "I-2")
	if err != nil || !strings.Contains(out, "initiative I-2 is complete") {
		t.Errorf("empty: err = %v, out = %q", err, out)
	}
}

func TestLoopInitiativeNeedsPersonMidRun(t *testing.T) {
	h, _ := chainFixture(t)
	c := &chainHarness{h: h, block: map[string]bool{"T-2": true}}
	useChainHarness(t, c)
	_, _, err := h.run("", "loop", "--initiative", "I-1")
	if !errors.Is(err, errs.ErrNeedsHuman) {
		t.Fatalf("err = %v", err)
	}
	if strings.Join(c.calls, ",") != "T-2" {
		t.Errorf("harness calls = %v", c.calls)
	}
}
