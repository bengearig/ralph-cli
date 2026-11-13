package cli

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/bengearig/ralph/internal/errs"
)

func TestTaskListWatchFlags(t *testing.T) {
	h := newTestEnv(t, true)
	seedWatch(h)
	if _, _, err := h.run("", "task", "list", "--interval", "250ms"); err == nil || err.Error() != "--interval requires --watch" {
		t.Errorf("interval without watch: %v", err)
	}
	if _, _, err := h.run("", "task", "list", "-w", "--interval", "50ms"); err == nil || err.Error() != "--interval must be at least 100ms" {
		t.Errorf("short interval: %v", err)
	}
	if _, _, err := h.run("", "task", "list", "--until-done"); err == nil || !strings.Contains(err.Error(), "unknown flag") {
		t.Errorf("until-done: %v", err)
	}
	w := h.startWatch(false, "task", "list", "-w", "--status", "TODO", "--initiative", "I-1", "--interval", "250ms")
	w.waitFor(t, time.Second, func() bool { return len(w.out.lines()) >= 1 })
	if err := w.stop(t); !errors.Is(err, errs.ErrInterrupted) {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(h.ok("task", "list", "--help"), "--watch") {
		t.Error("help does not mention --watch")
	}
}

func TestTaskListWatchJSONLines(t *testing.T) {
	h := newTestEnv(t, true)
	seedWatch(h)
	w := h.startWatch(false, "task", "list", "--status", "BLOCKED", "--watch", "--json", "--interval", "100ms")
	w.waitFor(t, time.Second, func() bool { return len(w.out.lines()) >= 1 })
	if l := w.out.lines()[0]; l != "[]" {
		t.Fatalf("first line = %s", l)
	}
	h.ok("task", "status", "T-1", "in_progress")
	h.ok("task", "status", "T-1", "blocked", "--reason", "stuck")
	w.waitFor(t, time.Second, func() bool { return len(w.out.lines()) >= 2 })
	if l := w.out.lines()[1]; !strings.Contains(l, `"T-1"`) {
		t.Fatalf("line = %s", l)
	}
	time.Sleep(400 * time.Millisecond)
	if n := len(w.out.lines()); n != 2 {
		t.Fatalf("%d lines, want 2", n)
	}
	h.ok("task", "status", "T-1", "todo", "--reason", "back")
	w.waitFor(t, time.Second, func() bool { return len(w.out.lines()) >= 3 })
	if l := w.out.lines()[2]; l != "[]" {
		t.Fatalf("line = %s", l)
	}
	if err := w.stop(t); !errors.Is(err, errs.ErrInterrupted) {
		t.Fatalf("err = %v", err)
	}
}

func TestTaskListWatchTerminal(t *testing.T) {
	h := newTestEnv(t, true)
	seedWatch(h)
	h.ok("milestone", "create", "--initiative", "I-1", "--title", "Second")
	h.ok("task", "create", "--milestone", "M-2", "--title", "Tail", "--description", "d", "--criteria", "c")
	h.ok("task", "create", "--milestone", "M-2", "--title", "Tail two", "--description", "d", "--criteria", "c")
	h.ok("task", "status", "T-2", "todo", "--reason", "r")
	h.ok("task", "status", "T-3", "todo", "--reason", "r")
	w := h.startWatch(true, "task", "list", "-w", "--status", "TODO", "--interval", "100ms")
	w.waitFor(t, time.Second, func() bool { return strings.Contains(w.out.String(), "T-1") })
	n := strings.Count(w.out.String(), "\x1b[7m")
	h.ok("task", "create", "--milestone", "M-1", "--title", "Front", "--description", "d", "--criteria", "c")
	h.ok("task", "status", "T-4", "todo", "--reason", "r")
	w.waitFor(t, time.Second, func() bool { return strings.Contains(w.out.String(), "Front") })
	if got := strings.Count(w.out.String(), "\x1b[7m") - n; got != 1 {
		t.Errorf("want 1 new highlight, got %d in %q", got, w.out.String())
	}
	_ = w.stop(t)
}

func TestTaskListWatchEmptyText(t *testing.T) {
	h := newTestEnv(t, true)
	seedWatch(h)
	w := h.startWatch(true, "task", "list", "-w", "--status", "BLOCKED", "--interval", "100ms")
	w.waitFor(t, time.Second, func() bool { return strings.Contains(w.out.String(), "no tasks") })
	_ = w.stop(t)
}

func TestTaskListWatchMissingAndDeleted(t *testing.T) {
	h := newTestEnv(t, true)
	for _, args := range [][]string{{"--status", "NOPE"}, {"--milestone", "M-99"}} {
		_, _, listErr := h.run("", append([]string{"task", "list"}, args...)...)
		_, _, watchErr := h.run("", append([]string{"task", "list", "-w"}, args...)...)
		if listErr == nil || watchErr == nil || listErr.Error() != watchErr.Error() {
			t.Fatalf("%v: list %v, watch %v", args, listErr, watchErr)
		}
	}
	seedWatch(h)
	w := h.startWatch(false, "task", "list", "-w", "--milestone", "M-1", "--interval", "100ms")
	w.waitFor(t, time.Second, func() bool { return len(w.out.lines()) >= 1 })
	h.ok("milestone", "delete", "M-1", "--yes", "--force")
	if err := w.finish(t); err == nil || err.Error() != "M-1 no longer exists" {
		t.Fatalf("err = %v", err)
	}
}
