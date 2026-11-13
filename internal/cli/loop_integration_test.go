package cli

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bengearig/ralph/internal/errs"
	"github.com/bengearig/ralph/internal/looplock"
	"github.com/bengearig/ralph/internal/project"
)

// loopIntegrationFixture is a committed git project with the harness
// integration installed, one TODO milestone, and a stale integration file in
// the working tree. It uses the given init.update mode.
func loopIntegrationFixture(t *testing.T, mode string) (*testEnv, func(args ...string) string, *chainHarness) {
	t.Helper()
	h, git, _ := newGitTestEnv(t)
	addBareRemote(t, git, "origin")
	if err := os.WriteFile(filepath.Join(h.dir, ".gitignore"), []byte("xdg/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	h.ok("initiative", "create", "--title", "SSO")
	h.ok("milestone", "create", "--initiative", "I-1", "--title", "One")
	h.ok("task", "create", "--milestone", "M-1", "--title", "Work", "--description", "d", "--criteria", "c")
	h.ok("task", "status", "T-1", "TODO")
	t.Setenv("RALPH_INIT__UPDATE", "manual")
	h.installHarness()
	git("add", "-A")
	git("commit", "--quiet", "-m", "integration")
	earlier, err := os.ReadFile("../setup/testdata/import-earlier.md")
	if err != nil {
		t.Fatal(err)
	}
	h.write(staleCmd, string(earlier))
	git("add", "-A")
	git("commit", "--quiet", "-m", "stale integration")
	t.Setenv("RALPH_INIT__UPDATE", mode)
	c := &chainHarness{h: h}
	useChainHarness(t, c)
	return h, git, c
}

func assertLoopStopped(t *testing.T, h *testEnv, git func(args ...string) string, c *chainHarness, errOut string, err error, rerun string) {
	t.Helper()
	if !errors.Is(err, errs.ErrNeedsHuman) {
		t.Fatalf("err = %v, want ErrNeedsHuman", err)
	}
	for _, want := range []string{
		"updated its integration files: " + staleCmd,
		"they must be committed before the loop can run; commit them, then rerun: " + rerun,
	} {
		if !strings.Contains(errOut, want) {
			t.Errorf("stderr missing %q:\n%s", want, errOut)
		}
	}
	if len(c.calls) != 0 {
		t.Errorf("harness calls = %v", c.calls)
	}
	if b := git("branch", "--list", "ralph/*"); b != "" {
		t.Errorf("milestone branch created: %q", b)
	}
	lock, lerr := looplock.Acquire(filepath.Join(h.dir, project.DirName, "loop.lock"), "test")
	if lerr != nil {
		t.Errorf("loop lock was held: %v", lerr)
	} else {
		lock.Release()
	}
}

func TestLoopStopsAfterAutoUpdate(t *testing.T) {
	for _, tc := range []struct {
		name  string
		args  []string
		rerun string
	}{
		{"milestone", []string{"loop", "--milestone", "M-1"}, "ralph loop --milestone M-1"},
		{"initiative merge", []string{"loop", "--initiative", "I-1", "--merge"}, "ralph loop --initiative I-1 --merge"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, git, c := loopIntegrationFixture(t, "auto")
			stale := h.read(staleCmd)
			_, errOut, err := h.run("", tc.args...)
			assertLoopStopped(t, h, git, c, errOut, err, tc.rerun)
			if h.read(staleCmd) == stale {
				t.Error("integration file was not updated on disk")
			}
		})
	}
}

func TestLoopProceedsAfterIntegrationCommitted(t *testing.T) {
	h, git, c := loopIntegrationFixture(t, "auto")
	_, errOut, err := h.run("", "loop", "--milestone", "M-1")
	assertLoopStopped(t, h, git, c, errOut, err, "ralph loop --milestone M-1")
	git("add", "-A")
	git("commit", "--quiet", "-m", "update integration files")
	_, errOut, err = h.run("", "loop", "--milestone", "M-1")
	if err != nil || strings.Contains(errOut, "integration") {
		t.Fatalf("err=%v stderr=%q", err, errOut)
	}
	if strings.Join(c.calls, ",") != "T-1" {
		t.Errorf("harness calls = %v", c.calls)
	}
}

func TestLoopDryRunIgnoresStaleIntegration(t *testing.T) {
	h, _, c := loopIntegrationFixture(t, "auto")
	before := h.snapshot()
	out, errOut, err := h.run("", "loop", "--milestone", "M-1", "--dry-run")
	if err != nil || !strings.Contains(out, "milestone:  M-1") {
		t.Fatalf("err=%v out=%q", err, out)
	}
	if !strings.Contains(errOut, outOfDateWarn+staleCmd) || strings.Contains(errOut, "they must be committed") {
		t.Errorf("stderr = %q", errOut)
	}
	if len(c.calls) != 0 {
		t.Errorf("harness calls = %v", c.calls)
	}
	after := h.snapshot()
	for k, v := range before {
		if after[k] != v {
			t.Errorf("%s changed", k)
		}
	}
}

func TestLoopManualModeOnlyWarns(t *testing.T) {
	h, _, c := loopIntegrationFixture(t, "manual")
	_, errOut, err := h.run("", "loop", "--milestone", "M-1")
	if err != nil {
		t.Fatalf("err=%v stderr=%q", err, errOut)
	}
	if !strings.Contains(errOut, outOfDateWarn+staleCmd) || strings.Contains(errOut, "they must be committed") {
		t.Errorf("stderr = %q", errOut)
	}
	if strings.Join(c.calls, ",") != "T-1" {
		t.Errorf("harness calls = %v", c.calls)
	}
}
