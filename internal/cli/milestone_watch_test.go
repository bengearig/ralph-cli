package cli

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/bengearig/ralph/internal/config"
	"github.com/bengearig/ralph/internal/domain"
	"github.com/bengearig/ralph/internal/errs"
	"github.com/bengearig/ralph/internal/service"
)

const goldenMilestoneText = "M-1  First\ninitiative: I-1\nslug: first\nstatus: READY\n\nDescription:\n  Desc here\n\nID   MILESTONE  POS  STATUS  FAILS  TITLE\nT-1  M-1        1    TODO           A\n"

const goldenMilestoneJSON = "{\n  \"id\": \"M-1\",\n  \"initiative_id\": \"I-1\",\n  \"slug\": \"first\",\n  \"title\": \"First\",\n  \"description\": \"Desc here\",\n  \"position\": 1,\n  \"status\": \"READY\",\n  \"task_counts\": {\n    \"TODO\": 1\n  },\n  \"created_at\": \"TS\",\n  \"updated_at\": \"TS\",\n  \"tasks\": [\n    {\n      \"id\": \"T-1\",\n      \"milestone_id\": \"M-1\",\n      \"title\": \"A\",\n      \"description\": \"d\",\n      \"acceptance_criteria\": \"c\",\n      \"status\": \"TODO\",\n      \"allowed_next\": [\n        \"IN_PROGRESS\",\n        \"PROPOSED\",\n        \"CANCELLED\"\n      ],\n      \"position\": 1,\n      \"failure_count\": 0,\n      \"created_at\": \"TS\",\n      \"updated_at\": \"TS\"\n    }\n  ]\n}\n"

func TestMilestoneShowOutputUnchanged(t *testing.T) {
	h := newTestEnv(t, true)
	h.ok("initiative", "create", "--title", "Golden", "--brief", "b")
	h.ok("milestone", "create", "--initiative", "I-1", "--title", "First", "--description", "Desc here")
	h.ok("task", "create", "--milestone", "M-1", "--title", "A", "--description", "d", "--criteria", "c")
	h.ok("task", "status", "T-1", "todo", "--reason", "r")
	if got := h.ok("milestone", "show", "M-1"); got != goldenMilestoneText {
		t.Errorf("show text = %q", got)
	}
	if got := timestampRE.ReplaceAllString(h.ok("--json", "milestone", "show", "M-1"), "TS"); got != goldenMilestoneJSON {
		t.Errorf("show json = %q", got)
	}
}

func TestMilestoneWatchJSONLines(t *testing.T) {
	h := newTestEnv(t, true)
	seedWatch(h)
	w := h.startWatch(false, "milestone", "watch", "M-1", "--interval", "100ms")
	w.waitFor(t, time.Second, func() bool { return len(w.out.lines()) >= 1 })

	var first, shown map[string]any
	if err := json.Unmarshal([]byte(w.out.lines()[0]), &first); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(h.ok("--json", "milestone", "show", "M-1")), &shown); err != nil {
		t.Fatal(err)
	}
	if !jsonEqual(first, shown) {
		t.Fatalf("first line %v != show %v", first, shown)
	}

	h.ok("task", "status", "T-1", "in_progress")
	w.waitFor(t, time.Second, func() bool { return len(w.out.lines()) >= 2 })
	if l := w.out.lines()[1]; !strings.Contains(l, `"IN_PROGRESS":1`) || !strings.Contains(l, `"status":"IN_PROGRESS"`) {
		t.Fatalf("second line = %s", l)
	}
	time.Sleep(400 * time.Millisecond)
	if n := len(w.out.lines()); n != 2 {
		t.Fatalf("%d lines, want 2", n)
	}
	if err := w.stop(t); !errors.Is(err, errs.ErrInterrupted) {
		t.Fatalf("err = %v", err)
	}
	if w.errOut.String() != "" {
		t.Fatalf("stderr = %q", w.errOut.String())
	}
}

func TestMilestoneWatchTerminal(t *testing.T) {
	h := newTestEnv(t, true)
	seedWatch(h)
	w := h.startWatch(true, "milestone", "watch", "M-1", "--interval", "100ms")
	w.waitFor(t, time.Second, func() bool { return strings.Contains(w.out.String(), "T-1") })
	if !strings.HasPrefix(w.out.String(), escHideCursor) {
		t.Fatalf("output does not start with hide cursor: %q", w.out.String())
	}
	h.ok("task", "status", "T-1", "in_progress")
	w.waitFor(t, time.Second, func() bool { return strings.Contains(w.out.String(), "IN_PROGRESS") })
	if !strings.Contains(w.out.String(), "\x1b[7m") {
		t.Errorf("changed row not highlighted: %q", w.out.String())
	}
	if err := w.stop(t); !errors.Is(err, errs.ErrInterrupted) {
		t.Fatalf("err = %v", err)
	}
	if !strings.HasSuffix(strings.TrimRight(w.out.String(), "\n"), escShowCursor) {
		t.Errorf("output does not end with show cursor: %q", w.out.String())
	}
}

func TestMilestoneWatchUntilDoneFinished(t *testing.T) {
	h := newTestEnv(t, true)
	seedWatch(h)
	completeTask(h, "T-1")
	w := h.startWatch(false, "milestone", "watch", "M-1", "--until-done", "--interval", "100ms")
	if err := w.finish(t); err != nil {
		t.Fatal(err)
	}
	if n := len(w.out.lines()); n != 1 {
		t.Fatalf("%d lines, want 1", n)
	}
}

func TestMilestoneWatchUntilDoneStuck(t *testing.T) {
	h := newTestEnv(t, true)
	seedWatch(h)
	h.ok("task", "status", "T-1", "in_progress")
	h.ok("task", "status", "T-1", "blocked", "--reason", "need help")
	w := h.startWatch(false, "milestone", "watch", "M-1", "--until-done", "--interval", "100ms")
	if err := w.finish(t); !errors.Is(err, errs.ErrNeedsHuman) {
		t.Fatalf("err = %v", err)
	}
}

func TestMilestoneWatchUntilDoneStuckBesideDoneTask(t *testing.T) {
	h := newTestEnv(t, true)
	seedWatch(h)
	h.ok("task", "create", "--milestone", "M-1", "--title", "B", "--description", "d", "--criteria", "c")
	h.ok("task", "status", "T-2", "todo", "--reason", "r")
	completeTask(h, "T-1")
	h.ok("task", "status", "T-2", "in_progress")
	h.ok("task", "status", "T-2", "blocked", "--reason", "need help")
	w := h.startWatch(false, "milestone", "watch", "M-1", "--until-done", "--interval", "100ms")
	if err := w.finish(t); !errors.Is(err, errs.ErrNeedsHuman) {
		t.Fatalf("err = %v", err)
	}
}

func TestMilestoneWatchUntilDoneInProgress(t *testing.T) {
	h := newTestEnv(t, true)
	seedWatch(h)
	h.ok("task", "status", "T-1", "in_progress")
	w := h.startWatch(false, "milestone", "watch", "M-1", "--until-done", "--interval", "100ms")
	w.waitFor(t, time.Second, func() bool { return len(w.out.lines()) >= 1 })
	select {
	case err := <-w.done:
		t.Fatalf("ended: %v", err)
	case <-time.After(400 * time.Millisecond):
	}
	h.ok("task", "status", "T-1", "done", "--reason", "ok")
	if err := w.finish(t); err != nil {
		t.Fatal(err)
	}
	if n := len(w.out.lines()); n != 2 {
		t.Fatalf("%d lines, want 2", n)
	}
}

func TestMilestoneWatchMissing(t *testing.T) {
	h := newTestEnv(t, true)
	_, _, showErr := h.run("", "milestone", "show", "M-99")
	_, _, watchErr := h.run("", "milestone", "watch", "M-99")
	if showErr == nil || watchErr == nil || showErr.Error() != watchErr.Error() {
		t.Fatalf("show %v, watch %v", showErr, watchErr)
	}
}

func TestMilestoneWatchDeleted(t *testing.T) {
	h := newTestEnv(t, true)
	h.ok("initiative", "create", "--title", "I")
	h.ok("milestone", "create", "--initiative", "I-1", "--title", "Doomed")
	w := h.startWatch(false, "milestone", "watch", "M-1", "--interval", "100ms")
	w.waitFor(t, time.Second, func() bool { return len(w.out.lines()) >= 1 })
	h.ok("milestone", "delete", "M-1", "--yes")
	err := w.finish(t)
	if err == nil || err.Error() != "M-1 no longer exists" {
		t.Fatalf("err = %v", err)
	}
}

func TestMilestoneWatchArchivedInitiative(t *testing.T) {
	h := newTestEnv(t, true)
	h.ok("initiative", "create", "--title", "I")
	h.ok("milestone", "create", "--initiative", "I-1", "--title", "M")
	w := h.startWatch(true, "milestone", "watch", "M-1", "--interval", "100ms")
	w.waitFor(t, time.Second, func() bool { return strings.Contains(w.out.String(), "M-1") })
	h.ok("initiative", "archive", "I-1", "--promote", "none")
	w.waitFor(t, 2*time.Second, func() bool { return strings.Contains(w.out.String(), "archived") })
	select {
	case err := <-w.done:
		t.Fatalf("ended: %v", err)
	default:
	}
	_ = w.stop(t)
}

func TestMilestoneWatchBranchAndCardLines(t *testing.T) {
	h := jiraEnv(t, "secret")
	seedWatch(h)
	recordJiraCard(t, h, domain.KindMilestone, 1, "RAL-2", "https://x.atlassian.net/browse/RAL-2")
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
	if err := svc.RecordMilestoneGit(context.Background(), 1, domain.MilestoneGit{Branch: "ralph/first"}); err != nil {
		t.Fatal(err)
	}
	shown := h.ok("milestone", "show", "M-1")
	if !strings.Contains(shown, "card: RAL-2") || !strings.Contains(shown, "branch: ralph/first\n") {
		t.Fatalf("show lacks lines: %q", shown)
	}
	w := h.startWatch(true, "milestone", "watch", "M-1", "--interval", "100ms")
	w.waitFor(t, time.Second, func() bool { return strings.Contains(w.out.String(), "T-1") })
	for _, line := range strings.Split(strings.TrimSuffix(shown, "\n"), "\n") {
		if line != "" && !strings.Contains(w.out.String(), line) {
			t.Errorf("missing line %q", line)
		}
	}
	_ = w.stop(t)
}

func TestMilestoneWatchFlags(t *testing.T) {
	h := newTestEnv(t, true)
	h.ok("initiative", "create", "--title", "X")
	h.ok("milestone", "create", "--initiative", "I-1", "--title", "M")
	_, _, err := h.run("", "milestone", "watch", "M-1", "--interval", "50ms")
	if err == nil || err.Error() != "--interval must be at least 100ms" {
		t.Fatalf("err = %v", err)
	}
	help := h.ok("milestone", "watch", "--help")
	for _, f := range []string{"--interval", "--until-done"} {
		if !strings.Contains(help, f) {
			t.Errorf("help lacks %s", f)
		}
	}
	if _, _, err := h.run("", "milestone", "watch"); err == nil {
		t.Error("no argument accepted")
	}
}
