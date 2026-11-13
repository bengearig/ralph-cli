package cli

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/bengearig/ralph/internal/errs"
)

func TestStatusWatchFlags(t *testing.T) {
	h := newTestEnv(t, true)
	if _, _, err := h.run("", "status", "--interval", "250ms"); err == nil || err.Error() != "--interval requires --watch" {
		t.Errorf("interval without watch: %v", err)
	}
	if _, _, err := h.run("", "status", "-w", "--interval", "50ms"); err == nil || err.Error() != "--interval must be at least 100ms" {
		t.Errorf("short interval: %v", err)
	}
	if _, _, err := h.run("", "status", "--until-done"); err == nil || !strings.Contains(err.Error(), "unknown flag") {
		t.Errorf("until-done: %v", err)
	}
	w := h.startWatch(false, "status", "-w", "--interval", "250ms")
	w.waitFor(t, time.Second, func() bool { return len(w.out.lines()) >= 1 })
	if err := w.stop(t); !errors.Is(err, errs.ErrInterrupted) {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(h.ok("status", "--help"), "--watch") {
		t.Error("help does not mention --watch")
	}
}

func TestStatusWatchJSONLines(t *testing.T) {
	h := newTestEnv(t, true)
	seedWatch(h)
	w := h.startWatch(false, "status", "--watch", "--json", "--interval", "100ms")
	w.waitFor(t, time.Second, func() bool { return len(w.out.lines()) >= 1 })
	var first, shown map[string]any
	if err := json.Unmarshal([]byte(w.out.lines()[0]), &first); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(h.ok("--json", "status")), &shown); err != nil {
		t.Fatal(err)
	}
	if !jsonEqual(first, shown) {
		t.Fatalf("first line %v != status %v", first, shown)
	}
	h.ok("initiative", "create", "--title", "Second one", "--brief", "b")
	w.waitFor(t, time.Second, func() bool { return len(w.out.lines()) >= 2 })
	if !strings.Contains(w.out.lines()[1], "Second one") {
		t.Fatalf("line = %s", w.out.lines()[1])
	}
	h.ok("task", "status", "T-1", "in_progress")
	w.waitFor(t, time.Second, func() bool { return len(w.out.lines()) >= 3 })
	time.Sleep(400 * time.Millisecond)
	if n := len(w.out.lines()); n != 3 {
		t.Fatalf("%d lines, want 3", n)
	}
	if err := w.stop(t); !errors.Is(err, errs.ErrInterrupted) {
		t.Fatalf("err = %v", err)
	}
}

func TestStatusWatchTerminal(t *testing.T) {
	h := newTestEnv(t, true)
	seedWatch(h)
	w := h.startWatch(true, "status", "-w", "--interval", "100ms")
	w.waitFor(t, time.Second, func() bool { return strings.Contains(w.out.String(), "M-1") })
	h.ok("task", "status", "T-1", "in_progress")
	w.waitFor(t, time.Second, func() bool { return strings.Contains(w.out.String(), "IN_PROGRESS") })
	if !strings.Contains(w.out.String(), "\x1b[7m") {
		t.Errorf("changed line not highlighted: %q", w.out.String())
	}
	n := strings.Count(w.out.String(), "\x1b[7m")
	h.ok("initiative", "create", "--title", "Brand new", "--brief", "b")
	w.waitFor(t, time.Second, func() bool { return strings.Contains(w.out.String(), "Brand new") })
	if strings.Count(w.out.String(), "\x1b[7m") <= n {
		t.Errorf("added initiative not highlighted: %q", w.out.String())
	}
	_ = w.stop(t)
}

func TestStatusWatchInitiativeScope(t *testing.T) {
	h := newTestEnv(t, true)
	seedWatch(h)
	h.ok("initiative", "create", "--title", "Other", "--brief", "b")
	w := h.startWatch(false, "status", "--watch", "--initiative", "I-1", "--json", "--interval", "100ms")
	w.waitFor(t, time.Second, func() bool { return len(w.out.lines()) >= 1 })
	h.ok("initiative", "create", "--title", "Third", "--brief", "b")
	h.ok("milestone", "create", "--initiative", "I-2", "--title", "Elsewhere")
	time.Sleep(500 * time.Millisecond)
	if n := len(w.out.lines()); n != 1 {
		t.Fatalf("%d lines, want 1", n)
	}
	h.ok("initiative", "archive", "I-1", "--promote", "none")
	w.waitFor(t, time.Second, func() bool { return len(w.out.lines()) >= 2 })
	if l := w.out.lines()[1]; l != `{"initiatives":[]}` {
		t.Fatalf("line = %s", l)
	}
	select {
	case <-w.finished:
		t.Fatalf("ended: %v", w.err)
	case <-time.After(300 * time.Millisecond):
	}
	_ = w.stop(t)
}

func TestStatusWatchMissingAndDeleted(t *testing.T) {
	h := newTestEnv(t, true)
	_, _, statusErr := h.run("", "status", "--initiative", "I-99")
	_, _, watchErr := h.run("", "status", "--watch", "--initiative", "I-99")
	if statusErr == nil || watchErr == nil || statusErr.Error() != watchErr.Error() {
		t.Fatalf("status %v, watch %v", statusErr, watchErr)
	}
	h.ok("initiative", "create", "--title", "Doomed")
	w := h.startWatch(false, "status", "-w", "--initiative", "I-1", "--interval", "100ms")
	w.waitFor(t, time.Second, func() bool { return len(w.out.lines()) >= 1 })
	h.ok("initiative", "delete", "I-1", "--yes")
	if err := w.finish(t); err == nil || err.Error() != "I-1 no longer exists" {
		t.Fatalf("err = %v", err)
	}
}
