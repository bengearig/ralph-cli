package cli

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bengearig/ralph/internal/errs"
)

// advanceOrigin pushes a new commit to main on the bare remote.
func advanceOrigin(t *testing.T, bare string) string {
	t.Helper()
	clone := t.TempDir()
	for _, args := range [][]string{
		{"clone", "--quiet", bare, clone},
		{"-C", clone, "switch", "--quiet", "main"},
	} {
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	if err := os.WriteFile(filepath.Join(clone, "upstream.txt"), []byte("u\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"-C", clone, "add", "upstream.txt"},
		{"-C", clone, "commit", "--quiet", "-m", "upstream"},
		{"-C", clone, "push", "--quiet", "origin", "main"},
	} {
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	out, err := exec.Command("git", "-C", bare, "rev-parse", "main").Output()
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(out))
}

// traceFetches counts the git fetch commands the test process runs.
func traceFetches(t *testing.T) func() int {
	t.Helper()
	path := filepath.Join(t.TempDir(), "trace.json")
	t.Setenv("GIT_TRACE2_EVENT", path)
	return func() int {
		b, _ := os.ReadFile(path)
		n := 0
		for _, line := range strings.Split(string(b), "\n") {
			if strings.Contains(line, `"event":"cmd_name"`) && strings.Contains(line, `"name":"fetch"`) {
				n++
			}
		}
		return n
	}
}

func TestLoopMilestoneRefusesStaleBase(t *testing.T) {
	h, git, _ := newGitTestEnv(t)
	bare := addBareRemote(t, git, "origin")
	h.ok("initiative", "create", "--title", "SSO")
	h.ok("milestone", "create", "--initiative", "I-1", "--title", "Login")
	h.ok("task", "create", "--milestone", "M-1", "--title", "Route", "--description", "d", "--criteria", "c")
	h.ok("task", "status", "T-1", "TODO")
	useChainHarness(t, &chainHarness{h: h})
	advanceOrigin(t, bare)
	mainBefore := git("rev-parse", "main")

	_, _, err := h.run("", "loop", "--milestone", "M-1")
	if !errors.Is(err, errs.ErrNeedsHuman) || !strings.Contains(err.Error(), "local main is 1 commit(s) behind origin/main") {
		t.Fatalf("err = %v", err)
	}
	if git("branch", "--list", "ralph/login") != "" {
		t.Error("refused loop created the branch")
	}
	var task struct{ Status string }
	h.json(&task, "task", "show", "T-1")
	if task.Status != "TODO" {
		t.Errorf("task status = %s", task.Status)
	}
	if git("rev-parse", "main") != mainBefore {
		t.Error("main moved")
	}

	_, errOut, err := h.run("", "loop", "--milestone", "M-1", "--dry-run")
	if err != nil || !strings.Contains(errOut, "warning: would refuse: local main is 1 commit(s) behind origin/main") {
		t.Errorf("dry run: err = %v, stderr = %q", err, errOut)
	}
	var plan struct {
		Warnings []string `json:"warnings"`
	}
	h.json(&plan, "loop", "--milestone", "M-1", "--dry-run")
	if len(plan.Warnings) == 0 || !strings.HasPrefix(plan.Warnings[0], "would refuse: local main is 1 commit(s) behind") {
		t.Errorf("plan warnings = %v", plan.Warnings)
	}
	if git("branch", "--list", "ralph/login") != "" || git("rev-parse", "main") != mainBefore {
		t.Error("dry run changed git")
	}

	if _, _, err := h.run("", "loop", "--milestone", "M-1", "--force"); err != nil {
		t.Fatalf("--force: %v", err)
	}
	if git("branch", "--list", "ralph/login") == "" {
		t.Error("--force did not run the loop")
	}
}

func TestLoopMilestoneSyncsWhenConfigured(t *testing.T) {
	h, git, _ := newGitTestEnv(t)
	bare := addBareRemote(t, git, "origin")
	h.ok("initiative", "create", "--title", "SSO")
	h.ok("milestone", "create", "--initiative", "I-1", "--title", "Login")
	h.ok("task", "create", "--milestone", "M-1", "--title", "Route", "--description", "d", "--criteria", "c")
	h.ok("task", "status", "T-1", "TODO")
	useChainHarness(t, &chainHarness{h: h})
	t.Setenv("RALPH_PR__STALE_BASE", "sync")
	tip := advanceOrigin(t, bare)

	_, errOut, err := h.run("", "loop", "--milestone", "M-1")
	if err != nil || !strings.Contains(errOut, "warning: fast-forwarded main to origin/main (1 commit(s))") {
		t.Fatalf("err = %v, stderr = %q", err, errOut)
	}
	if got := git("rev-parse", "main"); got != tip {
		t.Errorf("main = %s, want %s", got, tip)
	}
}

func TestPRCreateRefusesStaleBase(t *testing.T) {
	f := newPRCreateFixture(t)
	advanceOrigin(t, f.remote)

	_, _, err := f.h.run("", "pr", "create", "--milestone", "M-1")
	if !errors.Is(err, errs.ErrNeedsHuman) || !strings.Contains(err.Error(), "local main is 1 commit(s) behind origin/main") {
		t.Fatalf("err = %v", err)
	}
	if remoteHasBranch(t, f.remote, "ralph/login") || f.fake.created != 0 || f.fake.finds != 0 || !f.recorded(t).IsZero() {
		t.Error("refused pr create changed something")
	}

	out, errOut, err := f.h.run("", "pr", "create", "--milestone", "M-1", "--dry-run")
	if err != nil || !strings.Contains(errOut, "warning: would refuse: local main is 1 commit(s) behind") || !strings.Contains(out, "provider: fake") {
		t.Errorf("dry run: err = %v, out = %q, stderr = %q", err, out, errOut)
	}
	var plan struct {
		BaseWarnings []string `json:"base_warnings"`
	}
	f.h.json(&plan, "pr", "create", "--milestone", "M-1", "--dry-run")
	if len(plan.BaseWarnings) != 1 || !strings.HasPrefix(plan.BaseWarnings[0], "would refuse: local main is 1 commit(s) behind") {
		t.Errorf("base_warnings = %v", plan.BaseWarnings)
	}
	if remoteHasBranch(t, f.remote, "ralph/login") {
		t.Error("dry run pushed")
	}

	if _, _, err := f.h.run("", "pr", "create", "--milestone", "M-1", "--force"); err != nil {
		t.Fatalf("--force: %v", err)
	}
	if !remoteHasBranch(t, f.remote, "ralph/login") || f.fake.created != 1 {
		t.Error("--force did not open the pull request")
	}
}

func TestPRCreateDryRunOmitsBaseWarningsWhenFresh(t *testing.T) {
	f := newPRCreateFixture(t)
	var plan map[string]any
	f.h.json(&plan, "pr", "create", "--milestone", "M-1", "--dry-run")
	if _, ok := plan["base_warnings"]; ok {
		t.Errorf("base_warnings present: %v", plan["base_warnings"])
	}
}

func TestLoopInitiativeFetchesOnce(t *testing.T) {
	h, git := chainFixture(t)
	useChainHarness(t, &chainHarness{h: h})
	fetches := traceFetches(t)
	if _, _, err := h.run("", "loop", "--initiative", "I-1"); err != nil {
		t.Fatal(err)
	}
	if n := fetches(); n != 1 {
		t.Errorf("fetches = %d, want 1", n)
	}
	if git("branch", "--list", "ralph/two") == "" {
		t.Error("the milestone did not run")
	}
}

func TestMilestoneMergeDoesNotFetch(t *testing.T) {
	h, git, _ := newMergeTestEnv(t)
	bare := addBareRemote(t, git, "origin")
	before := git("rev-parse", "refs/remotes/origin/main")
	advanceOrigin(t, bare)
	fetches := traceFetches(t)
	h.ok("milestone", "merge", "M-1")
	if n := fetches(); n != 0 {
		t.Errorf("fetches = %d, want 0", n)
	}
	if got := git("rev-parse", "refs/remotes/origin/main"); got != before {
		t.Errorf("origin/main moved to %s", got)
	}
}
