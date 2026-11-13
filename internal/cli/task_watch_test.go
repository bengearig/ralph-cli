package cli

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/bengearig/ralph/internal/errs"
)

const goldenTaskText = "T-1  A\nmilestone: M-1\nstatus: TODO (next: IN_PROGRESS, PROPOSED, CANCELLED)\nposition: 1\nfailures: 0\n\nDescription:\n  d\n\nAcceptance criteria:\n  c\n\nNotes:\n  TS N-1 NOTE: PROPOSED → TODO: r\n  TS N-2 NOTE: a note\n"

const goldenTaskJSON = "{\n  \"id\": \"T-1\",\n  \"milestone_id\": \"M-1\",\n  \"title\": \"A\",\n  \"description\": \"d\",\n  \"acceptance_criteria\": \"c\",\n  \"status\": \"TODO\",\n  \"allowed_next\": [\n    \"IN_PROGRESS\",\n    \"PROPOSED\",\n    \"CANCELLED\"\n  ],\n  \"position\": 1,\n  \"failure_count\": 0,\n  \"notes\": [\n    {\n      \"id\": \"N-1\",\n      \"kind\": \"NOTE\",\n      \"body\": \"PROPOSED → TODO: r\",\n      \"created_at\": \"TS\"\n    },\n    {\n      \"id\": \"N-2\",\n      \"kind\": \"NOTE\",\n      \"body\": \"a note\",\n      \"created_at\": \"TS\"\n    }\n  ],\n  \"created_at\": \"TS\",\n  \"updated_at\": \"TS\"\n}\n"

func TestTaskShowOutputUnchanged(t *testing.T) {
	h := newTestEnv(t, true)
	seedWatch(h)
	h.ok("task", "note", "T-1", "a note")
	if got := timestampRE.ReplaceAllString(h.ok("task", "show", "T-1"), "TS"); got != goldenTaskText {
		t.Errorf("show text = %q", got)
	}
	if got := timestampRE.ReplaceAllString(h.ok("--json", "task", "show", "T-1"), "TS"); got != goldenTaskJSON {
		t.Errorf("show json = %q", got)
	}
}

func TestTaskWatchJSONLines(t *testing.T) {
	h := newTestEnv(t, true)
	seedWatch(h)
	w := h.startWatch(false, "task", "watch", "T-1", "--interval", "100ms")
	w.waitFor(t, time.Second, func() bool { return len(w.out.lines()) >= 1 })

	var first, shown map[string]any
	if err := json.Unmarshal([]byte(w.out.lines()[0]), &first); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(h.ok("--json", "task", "show", "T-1")), &shown); err != nil {
		t.Fatal(err)
	}
	if !jsonEqual(first, shown) {
		t.Fatalf("first line %v != show %v", first, shown)
	}

	h.ok("task", "status", "T-1", "in_progress")
	w.waitFor(t, time.Second, func() bool { return len(w.out.lines()) >= 2 })
	if l := w.out.lines()[1]; !strings.Contains(l, `"status":"IN_PROGRESS"`) {
		t.Fatalf("second line = %s", l)
	}
	h.ok("task", "note", "T-1", "fresh note")
	w.waitFor(t, time.Second, func() bool { return len(w.out.lines()) >= 3 })
	if l := w.out.lines()[2]; !strings.Contains(l, "fresh note") {
		t.Fatalf("third line = %s", l)
	}
	time.Sleep(400 * time.Millisecond)
	if n := len(w.out.lines()); n != 3 {
		t.Fatalf("%d lines, want 3", n)
	}
	if err := w.stop(t); !errors.Is(err, errs.ErrInterrupted) {
		t.Fatalf("err = %v", err)
	}
	if w.errOut.String() != "" {
		t.Fatalf("stderr = %q", w.errOut.String())
	}
}

func TestTaskWatchTerminalHighlightExpires(t *testing.T) {
	h := newTermHarness(t)
	before := "T-1  A\nNotes:\n  N-1 one\n"
	after := before + "  N-2 two\n"
	_ = h.r.Update(nil, termSnap(before))
	h.w.reset()
	_ = h.r.Update(termSnap(before), termSnap(after))
	if got := h.w.all(); !strings.Contains(got, "\x1b[7m  N-2 two\x1b[0m") || strings.Count(got, "\x1b[7m") != 1 {
		t.Fatalf("new note not highlighted: %q", got)
	}
	h.w.reset()
	h.clock = h.clock.Add(highlightFor + time.Second)
	_ = h.r.Tick(h.clock)
	if got := h.w.all(); strings.Contains(got, "\x1b[7m") || !strings.Contains(got, "N-2 two") {
		t.Fatalf("highlight not cleared: %q", got)
	}
}

func TestTaskWatchTerminalNewNote(t *testing.T) {
	h := newTestEnv(t, true)
	seedWatch(h)
	w := h.startWatch(true, "task", "watch", "T-1", "--interval", "100ms")
	w.waitFor(t, time.Second, func() bool { return strings.Contains(w.out.String(), "T-1") })
	h.ok("task", "note", "T-1", "fresh note")
	w.waitFor(t, time.Second, func() bool { return strings.Contains(w.out.String(), "\x1b[7m") })
	if !strings.Contains(w.out.String(), "fresh note") {
		t.Errorf("note missing: %q", w.out.String())
	}
	_ = w.stop(t)
}

func TestTaskWatchUntilDoneFinished(t *testing.T) {
	for _, target := range []string{"done", "cancelled"} {
		t.Run(target, func(t *testing.T) {
			h := newTestEnv(t, true)
			seedWatch(h)
			if target == "done" {
				completeTask(h, "T-1")
			} else {
				h.ok("task", "status", "T-1", "cancelled", "--reason", "no")
			}
			w := h.startWatch(false, "task", "watch", "T-1", "--until-done", "--interval", "100ms")
			if err := w.finish(t); err != nil {
				t.Fatal(err)
			}
			if n := len(w.out.lines()); n != 1 {
				t.Fatalf("%d lines, want 1", n)
			}
		})
	}
}

func TestTaskWatchUntilDoneStuck(t *testing.T) {
	h := newTestEnv(t, true)
	seedWatch(h)
	h.ok("task", "status", "T-1", "in_progress")
	h.ok("task", "status", "T-1", "blocked", "--reason", "need help")
	w := h.startWatch(false, "task", "watch", "T-1", "--until-done", "--interval", "100ms")
	if err := w.finish(t); !errors.Is(err, errs.ErrNeedsHuman) {
		t.Fatalf("err = %v", err)
	}
}

func TestTaskWatchUntilDoneWaits(t *testing.T) {
	h := newTestEnv(t, true)
	seedWatch(h)
	w := h.startWatch(false, "task", "watch", "T-1", "--until-done", "--interval", "100ms")
	w.waitFor(t, time.Second, func() bool { return len(w.out.lines()) >= 1 })
	select {
	case <-w.finished:
		t.Fatalf("ended: %v", w.err)
	case <-time.After(400 * time.Millisecond):
	}
	completeTask(h, "T-1")
	if err := w.finish(t); err != nil {
		t.Fatal(err)
	}
}

func TestTaskWatchMissing(t *testing.T) {
	h := newTestEnv(t, true)
	_, _, showErr := h.run("", "task", "show", "T-99")
	_, _, watchErr := h.run("", "task", "watch", "T-99")
	if showErr == nil || watchErr == nil || showErr.Error() != watchErr.Error() {
		t.Fatalf("show %v, watch %v", showErr, watchErr)
	}
}

func TestTaskWatchDeleted(t *testing.T) {
	h := newTestEnv(t, true)
	h.ok("initiative", "create", "--title", "I")
	h.ok("milestone", "create", "--initiative", "I-1", "--title", "M")
	h.ok("task", "create", "--milestone", "M-1", "--title", "Doomed")
	w := h.startWatch(false, "task", "watch", "T-1", "--interval", "100ms")
	w.waitFor(t, time.Second, func() bool { return len(w.out.lines()) >= 1 })
	h.ok("task", "delete", "T-1", "--yes")
	err := w.finish(t)
	if err == nil || err.Error() != "T-1 no longer exists" {
		t.Fatalf("err = %v", err)
	}
}

func TestTaskWatchReadOnly(t *testing.T) {
	h := newTestEnv(t, true)
	seedWatch(h)
	before := h.ok("--json", "task", "show", "T-1")
	w := h.startWatch(false, "task", "watch", "T-1", "--interval", "100ms")
	w.waitFor(t, time.Second, func() bool { return len(w.out.lines()) >= 1 })
	time.Sleep(300 * time.Millisecond)
	_ = w.stop(t)
	if after := h.ok("--json", "task", "show", "T-1"); after != before {
		t.Fatalf("task changed by watch:\n%s\n%s", before, after)
	}
}

func TestTaskWatchFlags(t *testing.T) {
	h := newTestEnv(t, true)
	seedWatch(h)
	_, _, err := h.run("", "task", "watch", "T-1", "--interval", "50ms")
	if err == nil || err.Error() != "--interval must be at least 100ms" {
		t.Fatalf("err = %v", err)
	}
	help := h.ok("task", "watch", "--help")
	for _, f := range []string{"--interval", "--until-done"} {
		if !strings.Contains(help, f) {
			t.Errorf("help lacks %s", f)
		}
	}
	if _, _, err := h.run("", "task", "watch"); err == nil {
		t.Error("no argument accepted")
	}
}
