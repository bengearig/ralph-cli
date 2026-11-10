package cli

import (
	"errors"
	"strings"
	"testing"

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
