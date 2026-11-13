package cli

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/bengearig/ralph/internal/errs"
)

func TestMilestoneAndInitiativeListWatchFlags(t *testing.T) {
	h := newTestEnv(t, true)
	seedWatch(h)
	for _, kind := range []string{"milestone", "initiative"} {
		if _, _, err := h.run("", kind, "list", "--interval", "250ms"); err == nil || err.Error() != "--interval requires --watch" {
			t.Errorf("%s interval without watch: %v", kind, err)
		}
		if _, _, err := h.run("", kind, "list", "-w", "--interval", "50ms"); err == nil || err.Error() != "--interval must be at least 100ms" {
			t.Errorf("%s short interval: %v", kind, err)
		}
		if _, _, err := h.run("", kind, "list", "--until-done"); err == nil || !strings.Contains(err.Error(), "unknown flag") {
			t.Errorf("%s until-done: %v", kind, err)
		}
		w := h.startWatch(false, kind, "list", "-w", "--interval", "250ms")
		w.waitFor(t, time.Second, func() bool { return len(w.out.lines()) >= 1 })
		if err := w.stop(t); !errors.Is(err, errs.ErrInterrupted) {
			t.Fatalf("%s err = %v", kind, err)
		}
		if !strings.Contains(h.ok(kind, "list", "--help"), "--watch") {
			t.Errorf("%s help does not mention --watch", kind)
		}
	}
}

func TestMilestoneListWatchJSONLines(t *testing.T) {
	h := newTestEnv(t, true)
	seedWatch(h)
	w := h.startWatch(false, "milestone", "list", "--initiative", "I-1", "--watch", "--json", "--interval", "100ms")
	w.waitFor(t, time.Second, func() bool { return len(w.out.lines()) >= 1 })
	if l := w.out.lines()[0]; !strings.HasPrefix(l, "[") || !strings.Contains(l, `"M-1"`) {
		t.Fatalf("first line = %s", l)
	}
	h.ok("task", "status", "T-1", "in_progress")
	w.waitFor(t, time.Second, func() bool { return len(w.out.lines()) >= 2 })
	time.Sleep(400 * time.Millisecond)
	if n := len(w.out.lines()); n != 2 {
		t.Fatalf("%d lines, want 2", n)
	}
	h.ok("milestone", "create", "--initiative", "I-1", "--title", "Second")
	w.waitFor(t, time.Second, func() bool { return len(w.out.lines()) >= 3 })
	if l := w.out.lines()[2]; !strings.Contains(l, `"M-2"`) {
		t.Fatalf("line = %s", l)
	}
	if err := w.stop(t); !errors.Is(err, errs.ErrInterrupted) {
		t.Fatalf("err = %v", err)
	}
}

func TestMilestoneListWatchTerminal(t *testing.T) {
	h := newTestEnv(t, true)
	seedWatch(h)
	h.ok("milestone", "create", "--initiative", "I-1", "--title", "Second")
	h.ok("task", "create", "--milestone", "M-2", "--title", "B", "--description", "d", "--criteria", "c")
	h.ok("task", "status", "T-2", "todo", "--reason", "r")
	h.ok("task", "status", "T-2", "in_progress")
	w := h.startWatch(true, "milestone", "list", "-w", "--interval", "100ms")
	w.waitFor(t, time.Second, func() bool { return strings.Contains(w.out.String(), "Second") })
	n := strings.Count(w.out.String(), "\x1b[7m")
	h.ok("task", "status", "T-1", "in_progress")
	w.waitFor(t, time.Second, func() bool { return strings.Count(w.out.String(), "\x1b[7m") > n })
	if got := strings.Count(w.out.String(), "\x1b[7m") - n; got != 1 {
		t.Errorf("want 1 new highlight, got %d in %q", got, w.out.String())
	}
	_ = w.stop(t)
}

func TestMilestoneListWatchInitiativeDeleted(t *testing.T) {
	h := newTestEnv(t, true)
	if _, _, err := h.run("", "milestone", "list", "-w", "--initiative", "I-9"); err == nil {
		t.Fatal("missing initiative accepted")
	}
	seedWatch(h)
	w := h.startWatch(false, "milestone", "list", "-w", "--initiative", "I-1", "--interval", "100ms")
	w.waitFor(t, time.Second, func() bool { return len(w.out.lines()) >= 1 })
	h.ok("initiative", "delete", "I-1", "--yes", "--force")
	if err := w.finish(t); err == nil || err.Error() != "I-1 no longer exists" {
		t.Fatalf("err = %v", err)
	}
}

func TestInitiativeListWatchEmpty(t *testing.T) {
	h := newTestEnv(t, true)
	w := h.startWatch(false, "initiative", "list", "--watch", "--json", "--interval", "100ms")
	w.waitFor(t, time.Second, func() bool { return len(w.out.lines()) >= 1 })
	if l := w.out.lines()[0]; l != "[]" {
		t.Fatalf("first line = %s", l)
	}
	if err := w.stop(t); !errors.Is(err, errs.ErrInterrupted) {
		t.Fatalf("err = %v", err)
	}
}

func TestInitiativeListWatchJSONLines(t *testing.T) {
	h := newTestEnv(t, true)
	seedWatch(h)
	w := h.startWatch(false, "initiative", "list", "--watch", "--json", "--interval", "100ms")
	wa := h.startWatch(false, "initiative", "list", "--watch", "--archived", "--json", "--interval", "100ms")
	w.waitFor(t, time.Second, func() bool { return len(w.out.lines()) >= 1 })
	wa.waitFor(t, time.Second, func() bool { return len(wa.out.lines()) >= 1 })
	h.ok("initiative", "create", "--title", "Other", "--brief", "b")
	w.waitFor(t, time.Second, func() bool { return len(w.out.lines()) >= 2 })
	if l := w.out.lines()[1]; !strings.Contains(l, `"I-2"`) {
		t.Fatalf("line = %s", l)
	}
	h.ok("initiative", "archive", "I-2", "--promote", "none")
	w.waitFor(t, time.Second, func() bool { return len(w.out.lines()) >= 3 })
	if l := w.out.lines()[2]; strings.Contains(l, `"I-2"`) {
		t.Fatalf("archived initiative still listed: %s", l)
	}
	if l := wa.out.lines(); !strings.Contains(l[len(l)-1], `"I-2"`) {
		t.Fatalf("--archived lost I-2: %s", l[len(l)-1])
	}
	time.Sleep(400 * time.Millisecond)
	if n := len(w.out.lines()); n != 3 {
		t.Fatalf("%d lines, want 3", n)
	}
	_ = wa.stop(t)
	if err := w.stop(t); !errors.Is(err, errs.ErrInterrupted) {
		t.Fatalf("err = %v", err)
	}
}

func TestInitiativeListWatchTerminal(t *testing.T) {
	h := newTestEnv(t, true)
	seedWatch(h)
	w := h.startWatch(true, "initiative", "list", "-w", "--interval", "100ms")
	w.waitFor(t, time.Second, func() bool { return strings.Contains(w.out.String(), "Watched") })
	n := strings.Count(w.out.String(), "\x1b[7m")
	h.ok("initiative", "create", "--title", "Other", "--brief", "b")
	w.waitFor(t, time.Second, func() bool { return strings.Contains(w.out.String(), "Other") })
	if got := strings.Count(w.out.String(), "\x1b[7m") - n; got != 1 {
		t.Errorf("want 1 new highlight, got %d in %q", got, w.out.String())
	}
	_ = w.stop(t)
}
