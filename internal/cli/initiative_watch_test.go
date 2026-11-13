package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bengearig/ralph/internal/errs"
)

const goldenShowText = "I-1  Golden\nslug: golden\ntasks: TODO 1\n\nBrief:\n  The brief\n  line two\n\nMILESTONE  STATUS  TITLE   TASKS\nM-1        READY   First   TODO 1\nM-2        EMPTY   Second  no tasks\n"

const goldenShowJSON = "{\n  \"id\": \"I-1\",\n  \"slug\": \"golden\",\n  \"title\": \"Golden\",\n  \"brief\": \"The brief\\nline two\",\n  \"created_at\": \"TS\",\n  \"updated_at\": \"TS\",\n  \"status\": {\n    \"id\": \"I-1\",\n    \"title\": \"Golden\",\n    \"task_counts\": {\n      \"TODO\": 1\n    },\n    \"milestones\": [\n      {\n        \"id\": \"M-1\",\n        \"initiative_id\": \"I-1\",\n        \"slug\": \"first\",\n        \"title\": \"First\",\n        \"description\": \"\",\n        \"position\": 1,\n        \"status\": \"READY\",\n        \"task_counts\": {\n          \"TODO\": 1\n        },\n        \"created_at\": \"TS\",\n        \"updated_at\": \"TS\"\n      },\n      {\n        \"id\": \"M-2\",\n        \"initiative_id\": \"I-1\",\n        \"slug\": \"second\",\n        \"title\": \"Second\",\n        \"description\": \"\",\n        \"position\": 2,\n        \"status\": \"EMPTY\",\n        \"task_counts\": {},\n        \"created_at\": \"TS\",\n        \"updated_at\": \"TS\"\n      }\n    ],\n    \"documents\": 0\n  }\n}\n"

var timestampRE = regexp.MustCompile(`\d{4}-\d\d-\d\dT\d\d:\d\d:\d\dZ`)

func TestInitiativeShowOutputUnchanged(t *testing.T) {
	h := newTestEnv(t, true)
	h.ok("initiative", "create", "--title", "Golden", "--brief", "The brief\nline two")
	h.ok("milestone", "create", "--initiative", "I-1", "--title", "First")
	h.ok("milestone", "create", "--initiative", "I-1", "--title", "Second")
	h.ok("task", "create", "--milestone", "M-1", "--title", "A", "--description", "d", "--criteria", "c")
	h.ok("task", "status", "T-1", "todo", "--reason", "r")
	if got := h.ok("initiative", "show", "I-1"); got != goldenShowText {
		t.Errorf("show text = %q", got)
	}
	got := timestampRE.ReplaceAllString(h.ok("--json", "initiative", "show", "I-1"), "TS")
	if got != goldenShowJSON {
		t.Errorf("show json = %q", got)
	}
}

func TestCapture(t *testing.T) {
	var orig bytes.Buffer
	a := &app{streams: Streams{Out: &orig}}
	got, err := a.capture(func() error { return a.printf("hello %d\n", 1) })
	if err != nil || got != "hello 1\n" {
		t.Fatalf("capture = %q, %v", got, err)
	}
	if a.streams.Out != &orig || orig.Len() != 0 {
		t.Fatal("Out not restored or written")
	}
	wantErr := errors.New("boom")
	if _, err := a.capture(func() error { return wantErr }); err != wantErr {
		t.Fatalf("err = %v", err)
	}
	func() {
		defer func() {
			if recover() == nil {
				t.Error("panic not propagated")
			}
		}()
		_, _ = a.capture(func() error { panic("x") })
	}()
	if a.streams.Out != &orig {
		t.Fatal("Out not restored after panic")
	}
}

func TestCaptureMatchesRender(t *testing.T) {
	h := newTestEnv(t, true)
	h.ok("initiative", "create", "--title", "Golden", "--brief", "The brief\nline two")
	h.ok("milestone", "create", "--initiative", "I-1", "--title", "First")
	var v initiativeShow
	h.json(&v, "initiative", "show", "I-1")
	a := &app{streams: Streams{Out: &bytes.Buffer{}}}
	got, err := a.capture(func() error { return a.renderInitiativeShow(v) })
	if err != nil || got != h.ok("initiative", "show", "I-1") {
		t.Fatalf("capture = %q, %v", got, err)
	}
}

type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func (s *syncBuf) lines() []string {
	str := s.String()
	if str == "" {
		return nil
	}
	return strings.Split(strings.TrimSuffix(str, "\n"), "\n")
}

type runningWatch struct {
	out, errOut *syncBuf
	cancel      context.CancelFunc
	done        chan error
	exited      chan struct{}
}

func (h *testEnv) startWatch(terminal bool, args ...string) *runningWatch {
	h.t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	w := &runningWatch{out: &syncBuf{}, errOut: &syncBuf{}, cancel: cancel, done: make(chan error, 1), exited: make(chan struct{})}
	streams := Streams{Out: w.out, Err: w.errOut, OutIsTerminal: terminal}
	if terminal {
		streams.OutSize = func() (int, int, bool) { return 100, 40, true }
	}
	full := append([]string{"--project-dir", h.dir}, args...)
	go func() {
		w.done <- Execute(ctx, streams, full)
		close(w.exited)
	}()
	h.t.Cleanup(func() {
		cancel()
		select {
		case <-w.exited:
		case <-time.After(5 * time.Second):
		}
	})
	return w
}

func (w *runningWatch) waitFor(t *testing.T, d time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out; output so far: %q", w.out.String())
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func (w *runningWatch) finish(t *testing.T) error {
	t.Helper()
	select {
	case err := <-w.done:
		return err
	case <-time.After(5 * time.Second):
		t.Fatal("watch did not end")
		return nil
	}
}

func (w *runningWatch) stop(t *testing.T) error {
	w.cancel()
	return w.finish(t)
}

func seedWatch(h *testEnv) {
	h.ok("initiative", "create", "--title", "Watched", "--brief", "b")
	h.ok("milestone", "create", "--initiative", "I-1", "--title", "First")
	h.ok("task", "create", "--milestone", "M-1", "--title", "A", "--description", "d", "--criteria", "c")
	h.ok("task", "status", "T-1", "todo", "--reason", "r")
}

func completeTask(h *testEnv, id string) {
	h.ok("task", "status", id, "in_progress")
	h.ok("task", "status", id, "done", "--reason", "ok")
}

func TestInitiativeWatchJSONLines(t *testing.T) {
	h := newTestEnv(t, true)
	seedWatch(h)
	w := h.startWatch(false, "initiative", "watch", "I-1", "--interval", "100ms")
	w.waitFor(t, time.Second, func() bool { return len(w.out.lines()) >= 1 })

	var first, shown map[string]any
	if err := json.Unmarshal([]byte(w.out.lines()[0]), &first); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(h.ok("--json", "initiative", "show", "I-1")), &shown); err != nil {
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

func jsonEqual(a, b any) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return bytes.Equal(x, y)
}

func TestInitiativeWatchTerminal(t *testing.T) {
	h := newTestEnv(t, true)
	seedWatch(h)
	w := h.startWatch(true, "initiative", "watch", "I-1", "--interval", "100ms")
	w.waitFor(t, time.Second, func() bool { return strings.Contains(w.out.String(), "M-1") })
	if !strings.HasPrefix(w.out.String(), escHideCursor) {
		t.Fatalf("output does not start with hide cursor: %q", w.out.String())
	}
	for _, line := range strings.Split(strings.TrimSuffix(h.ok("initiative", "show", "I-1"), "\n"), "\n") {
		if line != "" && !strings.Contains(w.out.String(), line) {
			t.Errorf("missing line %q", line)
		}
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

func TestInitiativeWatchJSONFlagOnTerminal(t *testing.T) {
	h := newTestEnv(t, true)
	seedWatch(h)
	w := h.startWatch(true, "--json", "initiative", "watch", "I-1", "--interval", "100ms")
	w.waitFor(t, time.Second, func() bool { return len(w.out.lines()) >= 1 })
	_ = w.stop(t)
	if strings.Contains(w.out.String(), "\x1b") || !json.Valid([]byte(w.out.lines()[0])) {
		t.Fatalf("output = %q", w.out.String())
	}
}

func TestInitiativeWatchUntilDoneFinished(t *testing.T) {
	h := newTestEnv(t, true)
	seedWatch(h)
	completeTask(h, "T-1")
	w := h.startWatch(false, "initiative", "watch", "I-1", "--until-done", "--interval", "100ms")
	if err := w.finish(t); err != nil {
		t.Fatal(err)
	}
	if n := len(w.out.lines()); n != 1 {
		t.Fatalf("%d lines, want 1", n)
	}
}

func TestInitiativeWatchUntilDoneStuck(t *testing.T) {
	h := newTestEnv(t, true)
	seedWatch(h)
	h.ok("task", "status", "T-1", "in_progress")
	h.ok("task", "status", "T-1", "blocked", "--reason", "need help")
	w := h.startWatch(false, "initiative", "watch", "I-1", "--until-done", "--interval", "100ms")
	if err := w.finish(t); !errors.Is(err, errs.ErrNeedsHuman) {
		t.Fatalf("err = %v", err)
	}
}

func TestInitiativeWatchUntilDoneNoMilestones(t *testing.T) {
	h := newTestEnv(t, true)
	h.ok("initiative", "create", "--title", "Empty")
	w := h.startWatch(false, "initiative", "watch", "I-1", "--until-done", "--interval", "100ms")
	w.waitFor(t, time.Second, func() bool { return len(w.out.lines()) >= 1 })
	select {
	case err := <-w.done:
		t.Fatalf("ended: %v", err)
	case <-time.After(400 * time.Millisecond):
	}
	if err := w.stop(t); !errors.Is(err, errs.ErrInterrupted) {
		t.Fatalf("err = %v", err)
	}
}

func TestInitiativeWatchUntilDoneDoneAndCancelled(t *testing.T) {
	h := newTestEnv(t, true)
	seedWatch(h)
	completeTask(h, "T-1")
	h.ok("milestone", "create", "--initiative", "I-1", "--title", "Second")
	h.ok("task", "create", "--milestone", "M-2", "--title", "B", "--description", "d", "--criteria", "c")
	h.ok("task", "status", "T-2", "cancelled", "--reason", "no")
	w := h.startWatch(false, "initiative", "watch", "I-1", "--until-done", "--interval", "100ms")
	if err := w.finish(t); err != nil {
		t.Fatal(err)
	}
}

func TestInitiativeWatchUntilDoneCancelledOnly(t *testing.T) {
	h := newTestEnv(t, true)
	seedWatch(h)
	h.ok("task", "status", "T-1", "cancelled", "--reason", "no")
	w := h.startWatch(false, "initiative", "watch", "I-1", "--until-done", "--interval", "100ms")
	w.waitFor(t, time.Second, func() bool { return len(w.out.lines()) >= 1 })
	select {
	case err := <-w.done:
		t.Fatalf("ended: %v", err)
	case <-time.After(300 * time.Millisecond):
	}
	_ = w.stop(t)
}

func TestInitiativeWatchMissing(t *testing.T) {
	h := newTestEnv(t, true)
	_, _, showErr := h.run("", "initiative", "show", "I-99")
	_, _, watchErr := h.run("", "initiative", "watch", "I-99")
	if showErr == nil || watchErr == nil || showErr.Error() != watchErr.Error() {
		t.Fatalf("show %v, watch %v", showErr, watchErr)
	}
}

func TestInitiativeWatchDeleted(t *testing.T) {
	h := newTestEnv(t, true)
	h.ok("initiative", "create", "--title", "Doomed")
	w := h.startWatch(false, "initiative", "watch", "I-1", "--interval", "100ms")
	w.waitFor(t, time.Second, func() bool { return len(w.out.lines()) >= 1 })
	h.ok("initiative", "delete", "I-1", "--yes")
	err := w.finish(t)
	if err == nil || err.Error() != "I-1 no longer exists" {
		t.Fatalf("err = %v", err)
	}
}

func TestInitiativeWatchArchived(t *testing.T) {
	h := newTestEnv(t, true)
	h.ok("initiative", "create", "--title", "Soon archived")
	w := h.startWatch(true, "initiative", "watch", "I-1", "--interval", "100ms")
	w.waitFor(t, time.Second, func() bool { return strings.Contains(w.out.String(), "I-1") })
	h.ok("initiative", "archive", "I-1", "--promote", "none")
	w.waitFor(t, 2*time.Second, func() bool { return strings.Contains(w.out.String(), "archived") })
	select {
	case err := <-w.done:
		t.Fatalf("ended: %v", err)
	default:
	}
	_ = w.stop(t)
}

func TestInitiativeWatchFlags(t *testing.T) {
	h := newTestEnv(t, true)
	h.ok("initiative", "create", "--title", "X")
	_, _, err := h.run("", "initiative", "watch", "I-1", "--interval", "50ms")
	if err == nil || err.Error() != "--interval must be at least 100ms" {
		t.Fatalf("err = %v", err)
	}
	help := h.ok("initiative", "watch", "--help")
	for _, f := range []string{"--interval", "--until-done"} {
		if !strings.Contains(help, f) {
			t.Errorf("help lacks %s", f)
		}
	}
	if _, _, err := h.run("", "initiative", "watch"); err == nil {
		t.Error("no argument accepted")
	}
	if _, _, err := h.run("", "initiative", "watch", "I-1", "I-2"); err == nil {
		t.Error("two arguments accepted")
	}
}
