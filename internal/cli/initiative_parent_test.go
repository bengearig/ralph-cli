package cli

import (
	"strings"
	"testing"

	"github.com/bengearig/ralph/internal/taskmanager"
)

func parentState(t *testing.T, h *testEnv, id string) (string, bool) {
	t.Helper()
	var raw map[string]any
	h.json(&raw, "initiative", "show", id)
	v, ok := raw["parent_card"]
	line := strings.Contains(h.ok("initiative", "show", id), "parent card:")
	if ok != line {
		t.Fatalf("json key present = %v, text line present = %v", ok, line)
	}
	s, _ := v.(string)
	return s, ok
}

func TestInitiativeParentCardLifecycle(t *testing.T) {
	h := newTestEnv(t, true)
	h.ok("initiative", "create", "--title", "Plain")
	if _, ok := parentState(t, h, "I-1"); ok {
		t.Fatal("parent card shown without the flag")
	}
	h.ok("initiative", "create", "--title", "X", "--parent-card", "PROJ-12")
	if out := h.ok("initiative", "show", "I-2"); !strings.Contains(out, "parent card: PROJ-12\n") {
		t.Errorf("show = %q", out)
	}
	if got, _ := parentState(t, h, "I-2"); got != "PROJ-12" {
		t.Errorf("parent_card = %q", got)
	}
	h.ok("initiative", "update", "I-2", "--parent-card", "ABC-7")
	if got, _ := parentState(t, h, "I-2"); got != "ABC-7" {
		t.Errorf("after update = %q", got)
	}
	h.ok("initiative", "update", "I-2", "--title", "New")
	if got, _ := parentState(t, h, "I-2"); got != "ABC-7" {
		t.Errorf("after title update = %q", got)
	}
	h.ok("initiative", "update", "I-2", "--parent-card", "")
	if _, ok := parentState(t, h, "I-2"); ok {
		t.Error("parent card not cleared")
	}
}

func TestInitiativeParentCardInvalid(t *testing.T) {
	h := newTestEnv(t, true)
	for _, bad := range []string{"proj-12", "PROJ 12"} {
		_, errOut, err := h.run("", "initiative", "create", "--title", "X", "--parent-card", bad)
		if err == nil || !strings.Contains(err.Error()+errOut, "parent_card") {
			t.Errorf("create %q: err = %v", bad, err)
		}
		if h.exists("initiative", "show", "I-1") {
			t.Errorf("create %q left an initiative", bad)
		}
	}
	h.ok("initiative", "create", "--title", "X", "--parent-card", "PROJ-12")
	for _, bad := range []string{"proj-12", "PROJ 12"} {
		_, errOut, err := h.run("", "initiative", "update", "I-1", "--parent-card", bad)
		if err == nil || !strings.Contains(err.Error()+errOut, "parent_card") {
			t.Errorf("update %q: err = %v", bad, err)
		}
		if got, _ := parentState(t, h, "I-1"); got != "PROJ-12" {
			t.Errorf("update %q changed parent to %q", bad, got)
		}
	}
}

func TestInitiativeParentCardReachesProvider(t *testing.T) {
	h, p := syncEnv(t)
	h.ok("initiative", "create", "--title", "Child", "--parent-card", "PROJ-12")
	h.ok("initiative", "update", "I-2", "--parent-card", "ABC-7")
	var create, update *taskmanager.CardRequest
	for _, c := range p.Calls() {
		r := c.Request
		if r.ID != "I-2" {
			continue
		}
		switch c.Method {
		case "Create":
			create = &r
		case "Update":
			update = &r
		}
	}
	if create == nil || create.ParentKey != "PROJ-12" {
		t.Errorf("create = %+v", create)
	}
	if update == nil || update.ParentKey != "ABC-7" || update.PreviousParentKey != "PROJ-12" {
		t.Errorf("update = %+v", update)
	}
}
