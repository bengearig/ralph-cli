package cli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bengearig/ralph/internal/config"
	"github.com/bengearig/ralph/internal/domain"
	"github.com/bengearig/ralph/internal/errs"
	"github.com/bengearig/ralph/internal/pr"
	"github.com/bengearig/ralph/internal/service"
)

func depsFixture(t *testing.T) *testEnv {
	t.Helper()
	h := newTestEnv(t, true)
	h.ok("initiative", "create", "--title", "Init", "--brief", "brief")
	h.ok("milestone", "create", "--initiative", "I-1", "--title", "One")
	h.ok("milestone", "create", "--initiative", "I-1", "--title", "Two")
	return h
}

func TestMilestoneDependsOnFlags(t *testing.T) {
	h := depsFixture(t)
	h.ok("milestone", "create", "--initiative", "I-1", "--title", "Three", "--depends-on", "M-1,M-2")
	if out := h.ok("milestone", "show", "M-3"); !strings.Contains(out, "depends on: M-1, M-2\n") {
		t.Fatalf("show = %s", out)
	}
	h.ok("milestone", "create", "--initiative", "I-1", "--title", "Four", "--depends-on", "M-2", "--depends-on", "M-1")
	if out := h.ok("milestone", "show", "M-4"); !strings.Contains(out, "depends on: M-1, M-2\n") {
		t.Fatalf("repeated flags show = %s", out)
	}

	h.ok("milestone", "update", "M-3", "--depends-on", "M-1")
	if out := h.ok("milestone", "show", "M-3"); !strings.Contains(out, "depends on: M-1\n") {
		t.Fatalf("replace show = %s", out)
	}
	h.ok("milestone", "update", "M-3", "--title", "Three!")
	if out := h.ok("milestone", "show", "M-3"); !strings.Contains(out, "depends on: M-1\n") {
		t.Fatalf("untouched show = %s", out)
	}
	h.ok("milestone", "update", "M-3", "--no-depends-on")
	if out := h.ok("milestone", "show", "M-3"); strings.Contains(out, "depends on") {
		t.Fatalf("cleared show = %s", out)
	}

	for name, args := range map[string][]string{
		"both": {"milestone", "update", "M-3", "--depends-on", "M-1", "--no-depends-on"},
		"bad":  {"milestone", "update", "M-3", "--depends-on", "X-1"},
		"self": {"milestone", "update", "M-3", "--depends-on", "M-3"},
	} {
		if _, _, err := h.run("", args...); err == nil {
			t.Errorf("%s: want error", name)
		}
	}
	if _, _, err := h.run("", "milestone", "update", "M-1", "--depends-on", "M-4"); err == nil || !strings.Contains(err.Error(), "cycle") {
		t.Errorf("cycle err = %v", err)
	}
}

func TestMilestoneDependsOnJSON(t *testing.T) {
	h := depsFixture(t)
	h.ok("milestone", "create", "--initiative", "I-1", "--title", "Three", "--depends-on", "M-2,M-1")
	var shown struct {
		DependsOn []string `json:"depends_on"`
	}
	h.json(&shown, "milestone", "show", "M-3")
	if strings.Join(shown.DependsOn, ",") != "M-1,M-2" {
		t.Fatalf("show depends_on = %v", shown.DependsOn)
	}
	out := h.ok("--json", "milestone", "list")
	if strings.Count(out, "depends_on") != 1 {
		t.Fatalf("list should carry depends_on once: %s", out)
	}
	if out := h.ok("--json", "milestone", "show", "M-1"); strings.Contains(out, "depends_on") {
		t.Fatalf("depends_on not omitted: %s", out)
	}
}

func TestMilestoneDeleteListsDependents(t *testing.T) {
	h := depsFixture(t)
	h.ok("milestone", "update", "M-2", "--depends-on", "M-1")
	_, _, err := h.run("", "milestone", "delete", "M-1", "--yes")
	if !errors.Is(err, service.ErrNeedsForce) || !strings.Contains(err.Error(), "M-2 depends on M-1") {
		t.Fatalf("delete err = %v", err)
	}
	out := h.ok("milestone", "delete", "M-1", "--dry-run", "--force")
	if !strings.Contains(out, "M-2 depends on M-1") {
		t.Fatalf("dry-run = %s", out)
	}
	var v service.DeleteView
	h.json(&v, "milestone", "delete", "M-1", "--dry-run", "--force")
	if len(v.Dependents) != 1 {
		t.Fatalf("dependents = %v", v.Dependents)
	}
	h.ok("milestone", "delete", "M-1", "--force", "--yes")
	if out := h.ok("milestone", "show", "M-2"); strings.Contains(out, "depends on") {
		t.Fatalf("dependency survived delete: %s", out)
	}
}

func TestMilestoneHelpMentionsDependsOn(t *testing.T) {
	h := depsFixture(t)
	for _, c := range []struct {
		cmd  string
		want []string
	}{{"create", []string{"--depends-on"}}, {"update", []string{"--depends-on", "--no-depends-on"}}} {
		out := h.ok("milestone", c.cmd, "--help")
		for _, w := range c.want {
			if !strings.Contains(out, w) {
				t.Errorf("%s --help lacks %s", c.cmd, w)
			}
		}
	}
}

func TestLoopAndPRCreateEnforceDependencies(t *testing.T) {
	h, git, commit := newGitTestEnv(t)
	bin := filepath.Join(t.TempDir(), "claude")
	script := "#!/bin/sh\ncat >/dev/null\n" +
		"printf '%s\\n' '{\"type\":\"system\",\"subtype\":\"init\",\"mcp_servers\":[{\"name\":\"ralph\",\"status\":\"connected\"}]}'\n" +
		"printf '%s\\n' '{\"type\":\"result\",\"subtype\":\"success\",\"result\":\"looked around\",\"num_turns\":2}'\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RALPH_HARNESS__CLAUDE_CODE__BINARY", bin)
	t.Setenv("RALPH_LOOP__STALL_THRESHOLD", "1")

	h.ok("initiative", "create", "--title", "SSO")
	h.ok("milestone", "create", "--initiative", "I-1", "--title", "Dep")
	h.ok("milestone", "create", "--initiative", "I-1", "--title", "Looping", "--depends-on", "M-1")
	h.ok("milestone", "create", "--initiative", "I-1", "--title", "Shipping", "--depends-on", "M-1")
	finish := func(task string) {
		h.ok("task", "status", task, "TODO")
		h.ok("task", "status", task, "IN_PROGRESS")
		h.ok("task", "status", task, "DONE", "--reason", "done")
	}
	h.ok("task", "create", "--milestone", "M-1", "--title", "Dep work", "--description", "d", "--criteria", "c")
	h.ok("task", "create", "--milestone", "M-2", "--title", "Loop work", "--description", "d", "--criteria", "c")
	h.ok("task", "status", "T-2", "TODO")
	h.ok("task", "create", "--milestone", "M-3", "--title", "Ship work", "--description", "d", "--criteria", "c")
	finish("T-1")
	finish("T-3")

	base := git("rev-parse", "HEAD")
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
	for id, branch := range map[int64]string{1: "ralph/dep", 3: "ralph/shipping"} {
		git("switch", "--quiet", "-c", branch)
		head := commit(branch[6:]+".txt", branch+" work")
		git("switch", "--quiet", "main")
		if err := svc.RecordMilestoneGit(context.Background(), id, domain.MilestoneGit{Branch: branch, BaseCommit: base, HeadCommit: head}); err != nil {
			t.Fatal(err)
		}
	}
	fake := &fakeProvider{}
	newPRProvider = func(*config.Config) (pr.Provider, error) { return fake, nil }
	t.Cleanup(func() { newPRProvider = pr.New })

	want := "M-2 depends on M-1, which branch ralph/dep is not merged into main; ralph milestone merge M-1"
	_, _, err = h.run("", "loop", "--milestone", "M-2")
	if !errors.Is(err, errs.ErrNeedsHuman) || !strings.Contains(err.Error(), want) {
		t.Fatalf("loop err = %v", err)
	}
	if git("branch", "--list", "ralph/looping") != "" {
		t.Error("refused loop created the branch")
	}
	_, errOut, err := h.run("", "loop", "--milestone", "M-2", "--dry-run")
	if err != nil || !strings.Contains(errOut, "warning: "+want) {
		t.Errorf("dry run: err = %v, stderr = %q", err, errOut)
	}
	if git("branch", "--list", "ralph/looping") != "" {
		t.Error("dry run created the branch")
	}

	_, _, err = h.run("", "pr", "create", "--milestone", "M-3")
	wantPR := strings.Replace(want, "M-2", "M-3", 1)
	if !errors.Is(err, errs.ErrNeedsHuman) || !strings.Contains(err.Error(), wantPR) || fake.created != 0 {
		t.Fatalf("pr err = %v (created %d)", err, fake.created)
	}
	out, errOut, err := h.run("", "pr", "create", "--milestone", "M-3", "--dry-run")
	if err != nil || !strings.Contains(errOut, "warning: "+wantPR) || !strings.Contains(out, "provider: fake") {
		t.Errorf("pr dry run: err = %v, out = %q, stderr = %q", err, out, errOut)
	}
	var plan struct {
		Warnings []string `json:"dependency_warnings"`
	}
	h.json(&plan, "pr", "create", "--milestone", "M-3", "--dry-run")
	if len(plan.Warnings) != 1 || plan.Warnings[0] != wantPR {
		t.Errorf("dependency_warnings = %v", plan.Warnings)
	}
	if out, _, err := h.run("", "pr", "create", "--milestone", "M-3", "--force"); err != nil || fake.created != 1 {
		t.Errorf("pr --force: %q %v (created %d)", out, err, fake.created)
	}

	_, errOut, err = h.run("", "loop", "--milestone", "M-2", "--force")
	if err == nil || strings.Contains(err.Error(), "depends on") || !strings.Contains(errOut, "creating ralph/looping from main") {
		t.Errorf("loop --force: err = %v, stderr = %q", err, errOut)
	}

	git("switch", "--quiet", "main")
	git("merge", "--quiet", "--ff-only", "ralph/dep")
	if _, errOut, err = h.run("", "loop", "--milestone", "M-2", "--dry-run"); err != nil || strings.Contains(errOut, "depends on") {
		t.Errorf("loop after merge: err = %v, stderr = %q", err, errOut)
	}
	h.ok("pr", "unlink", "--milestone", "M-3")
	if _, _, err := h.run("", "pr", "create", "--milestone", "M-3"); err != nil || fake.created != 2 {
		t.Errorf("pr after merge: %v (created %d)", err, fake.created)
	}
}
