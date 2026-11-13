package cli

import (
	"strings"
	"testing"
)

func sprintState(t *testing.T, h *testEnv, id string) (string, bool) {
	t.Helper()
	var raw map[string]any
	h.json(&raw, "initiative", "show", id)
	v, ok := raw["sprint"]
	line := strings.Contains(h.ok("initiative", "show", id), "sprint:")
	if ok != line {
		t.Fatalf("json key present = %v, text line present = %v", ok, line)
	}
	s, _ := v.(string)
	return s, ok
}

func TestInitiativeSprintLifecycle(t *testing.T) {
	h := newTestEnv(t, true)
	h.ok("initiative", "create", "--title", "Plain")
	if _, ok := sprintState(t, h, "I-1"); ok {
		t.Fatal("sprint shown without the flag")
	}
	h.ok("initiative", "create", "--title", "X", "--sprint", "Sprint 42")
	if out := h.ok("initiative", "show", "I-2"); !strings.Contains(out, "sprint: Sprint 42\n") {
		t.Errorf("show = %q", out)
	}
	if got, _ := sprintState(t, h, "I-2"); got != "Sprint 42" {
		t.Errorf("sprint = %q", got)
	}
	h.ok("initiative", "update", "I-2", "--sprint", "Sprint 43")
	if got, _ := sprintState(t, h, "I-2"); got != "Sprint 43" {
		t.Errorf("after update = %q", got)
	}
	h.ok("initiative", "update", "I-2", "--title", "New")
	if got, _ := sprintState(t, h, "I-2"); got != "Sprint 43" {
		t.Errorf("after title update = %q", got)
	}
	h.ok("initiative", "update", "I-2", "--sprint", "")
	if _, ok := sprintState(t, h, "I-2"); ok {
		t.Error("sprint not cleared")
	}
}

func TestInitiativeSprintInvalid(t *testing.T) {
	h := newTestEnv(t, true)
	const bad = "Sprint\n42"
	_, errOut, err := h.run("", "initiative", "create", "--title", "X", "--sprint", bad)
	if err == nil || !strings.Contains(err.Error()+errOut, "sprint") {
		t.Errorf("create: err = %v", err)
	}
	if h.exists("initiative", "show", "I-1") {
		t.Error("create left an initiative")
	}
	h.ok("initiative", "create", "--title", "X", "--sprint", "Sprint 42")
	_, errOut, err = h.run("", "initiative", "update", "I-1", "--sprint", bad)
	if err == nil || !strings.Contains(err.Error()+errOut, "sprint") {
		t.Errorf("update: err = %v", err)
	}
	if got, _ := sprintState(t, h, "I-1"); got != "Sprint 42" {
		t.Errorf("update changed sprint to %q", got)
	}
}
