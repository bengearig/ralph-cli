package cli

import (
	"errors"
	"strings"
	"testing"
	"time"
)

type recWriter struct {
	writes []string
	err    error
}

func (w *recWriter) Write(p []byte) (int, error) {
	if w.err != nil {
		return 0, w.err
	}
	w.writes = append(w.writes, string(p))
	return len(p), nil
}

func (w *recWriter) all() string { return strings.Join(w.writes, "") }
func (w *recWriter) reset()      { w.writes = nil }

type termHarness struct {
	w     *recWriter
	clock time.Time
	size  termSize
	r     *terminalRenderer
}

func newTermHarness(t *testing.T) *termHarness {
	t.Helper()
	h := &termHarness{
		w:     &recWriter{},
		clock: time.Date(2026, 1, 2, 14, 3, 7, 0, time.UTC),
	}
	a := &app{streams: Streams{Out: h.w, OutSize: func() (int, int, bool) {
		return h.size.width, h.size.height, h.size.ok
	}}}
	h.r = a.newTerminalRenderer(time.Second, func() time.Time { return h.clock })
	return h
}

func termSnap(text string) *snapshot { return &snapshot{Text: text} }

func TestTerminalStartStop(t *testing.T) {
	h := newTermHarness(t)
	if err := h.r.Start(); err != nil {
		t.Fatal(err)
	}
	if got := h.w.all(); got != "\x1b[?25l" {
		t.Fatalf("start wrote %q", got)
	}
	h.w.reset()
	if err := h.r.Stop(); err != nil {
		t.Fatal(err)
	}
	got := h.w.all()
	if !strings.Contains(got, "\x1b[?25h") || !strings.HasSuffix(got, "\n") {
		t.Fatalf("stop wrote %q", got)
	}
	h.w.reset()
	if err := h.r.Stop(); err != nil || len(h.w.writes) != 0 {
		t.Fatalf("second stop wrote %q, err %v", h.w.all(), err)
	}
}

func TestTerminalFrameShape(t *testing.T) {
	h := newTermHarness(t)
	if err := h.r.Update(nil, termSnap("A\nB\n")); err != nil {
		t.Fatal(err)
	}
	if len(h.w.writes) != 1 {
		t.Fatalf("want one write, got %d", len(h.w.writes))
	}
	got := h.w.writes[0]
	prefix := "\x1b[H" + "A\x1b[K\n" + "B\x1b[K\n"
	if !strings.HasPrefix(got, prefix) || !strings.HasSuffix(got, "\x1b[J") {
		t.Fatalf("frame %q", got)
	}
	footer := strings.TrimSuffix(strings.TrimPrefix(got, prefix), "\x1b[J")
	if footer != "\x1b[2mwatching - every 1s - updated 14:03:07 - Ctrl-C to exit\x1b[0m" {
		t.Fatalf("footer %q", footer)
	}
	if strings.Contains(got, "\x1b[2J") || strings.Contains(got, "\x1b[7m") || strings.Contains(got, "\x1b[1m") {
		t.Fatalf("unexpected escape in %q", got)
	}
}

func TestTerminalHighlightChange(t *testing.T) {
	h := newTermHarness(t)
	_ = h.r.Update(nil, termSnap("T-1  TODO\nT-2  TODO\n"))
	h.w.reset()
	_ = h.r.Update(termSnap("T-1  TODO\nT-2  TODO\n"), termSnap("T-1  DONE\nT-2  TODO\n"))
	got := h.w.all()
	if !strings.Contains(got, "\x1b[7mT-1  DONE\x1b[0m\x1b[K") {
		t.Fatalf("changed line not highlighted: %q", got)
	}
	if strings.Contains(got, "\x1b[7mT-2") || strings.Count(got, "\x1b[7m") != 1 {
		t.Fatalf("unchanged line highlighted: %q", got)
	}
}

func TestTerminalMovedRowNotHighlighted(t *testing.T) {
	h := newTermHarness(t)
	_ = h.r.Update(nil, termSnap("A\nB\n"))
	h.w.reset()
	_ = h.r.Update(termSnap("A\nB\n"), termSnap("A\nNEW\nB\n"))
	got := h.w.all()
	if strings.Count(got, "\x1b[7m") != 1 || !strings.Contains(got, "\x1b[7mNEW\x1b[0m") {
		t.Fatalf("got %q", got)
	}
}

func TestTerminalDuplicateHighlightedOnce(t *testing.T) {
	h := newTermHarness(t)
	_ = h.r.Update(nil, termSnap("X\nY\n"))
	h.w.reset()
	_ = h.r.Update(termSnap("X\nY\n"), termSnap("X\nX\nY\n"))
	if n := strings.Count(h.w.all(), "\x1b[7m"); n != 1 {
		t.Fatalf("want 1 highlight, got %d in %q", n, h.w.all())
	}
}

func TestTerminalBlankLineNotHighlighted(t *testing.T) {
	h := newTermHarness(t)
	_ = h.r.Update(nil, termSnap("A\n"))
	h.w.reset()
	_ = h.r.Update(termSnap("A\n"), termSnap("A\n\n\n"))
	if strings.Contains(h.w.all(), "\x1b[7m") {
		t.Fatalf("blank highlighted: %q", h.w.all())
	}
}

func TestTerminalExpiry(t *testing.T) {
	h := newTermHarness(t)
	_ = h.r.Update(nil, termSnap("A\n"))
	_ = h.r.Update(termSnap("A\n"), termSnap("B\n"))
	h.w.reset()

	if err := h.r.Tick(h.clock); err != nil || len(h.w.writes) != 0 {
		t.Fatalf("idle tick wrote %q", h.w.all())
	}
	h.clock = h.clock.Add(2 * time.Second)
	_ = h.r.Tick(h.clock)
	if len(h.w.writes) != 0 {
		t.Fatalf("tick at 2s wrote %q", h.w.all())
	}
	h.clock = h.clock.Add(time.Second)
	_ = h.r.Tick(h.clock)
	if len(h.w.writes) != 1 || strings.Contains(h.w.all(), "\x1b[7m") || !strings.Contains(h.w.all(), "B\x1b[K") {
		t.Fatalf("tick at 3s wrote %q", h.w.all())
	}
	h.w.reset()
	_ = h.r.Tick(h.clock.Add(time.Second))
	if len(h.w.writes) != 0 {
		t.Fatalf("tick after expiry wrote %q", h.w.all())
	}
}

func TestTerminalLaterUpdateKeepsUnexpired(t *testing.T) {
	h := newTermHarness(t)
	_ = h.r.Update(nil, termSnap("A\nB\n"))
	_ = h.r.Update(termSnap("A\nB\n"), termSnap("A\nC\n"))
	h.clock = h.clock.Add(time.Second)
	h.w.reset()
	_ = h.r.Update(termSnap("A\nC\n"), termSnap("A\nC\nD\n"))
	got := h.w.all()
	if !strings.Contains(got, "\x1b[7mC\x1b[0m") || !strings.Contains(got, "\x1b[7mD\x1b[0m") || strings.Contains(got, "\x1b[7mA") {
		t.Fatalf("got %q", got)
	}
}

func TestTerminalNoColor(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	h := newTermHarness(t)
	_ = h.r.Update(nil, termSnap("A\n"))
	h.w.reset()
	_ = h.r.Update(termSnap("A\n"), termSnap("B\n"))
	got := h.w.all()
	if !strings.Contains(got, "\x1b[1mB\x1b[0m") || strings.Contains(got, "\x1b[7m") {
		t.Fatalf("got %q", got)
	}
}

func TestTerminalFooterAndNotice(t *testing.T) {
	h := newTermHarness(t)
	_ = h.r.Update(nil, termSnap("A\n"))
	h.w.reset()
	_ = h.r.Notice("waiting: database is locked")
	got := h.w.all()
	if !strings.Contains(got, "waiting: database is locked") || strings.Contains(got, "updated") || !strings.Contains(got, "Ctrl-C to exit") {
		t.Fatalf("got %q", got)
	}
	h.w.reset()
	_ = h.r.Notice("")
	got = h.w.all()
	if !strings.Contains(got, "updated 14:03:07") || strings.Contains(got, "waiting") {
		t.Fatalf("got %q", got)
	}
}

func TestTerminalSizeClipsHeight(t *testing.T) {
	h := newTermHarness(t)
	h.size = termSize{20, 5, true}
	var lines []string
	for i := 1; i <= 10; i++ {
		lines = append(lines, "line"+string(rune('0'+i%10)))
	}
	_ = h.r.Update(nil, termSnap(strings.Join(lines, "\n")+"\n"))
	got := h.w.all()
	if strings.Count(got, "\x1b[K\n") != 3 || !strings.Contains(got, "7 more lines") || !strings.Contains(got, "every 1s") {
		t.Fatalf("got %q", got)
	}
}

func TestTerminalSizeClipsWidth(t *testing.T) {
	h := newTermHarness(t)
	h.size = termSize{20, 50, true}
	_ = h.r.Update(nil, termSnap(strings.Repeat("x", 30)+"\n"+strings.Repeat("é", 30)+"\n"))
	got := h.w.all()
	if !strings.Contains(got, "\x1b[H"+strings.Repeat("x", 19)+"\x1b[K\n"+strings.Repeat("é", 19)+"\x1b[K\n") {
		t.Fatalf("got %q", got)
	}
}

func TestTerminalClipBeforeHighlight(t *testing.T) {
	h := newTermHarness(t)
	h.size = termSize{5, 50, true}
	_ = h.r.Update(nil, termSnap("a\n"))
	h.w.reset()
	_ = h.r.Update(termSnap("a\n"), termSnap("abcdefgh\n"))
	if !strings.Contains(h.w.all(), "\x1b[7mabcd\x1b[0m\x1b[K") {
		t.Fatalf("got %q", h.w.all())
	}
}

func TestTerminalUnknownSizeNoClip(t *testing.T) {
	h := newTermHarness(t)
	h.size = termSize{5, 2, false}
	_ = h.r.Update(nil, termSnap(strings.Repeat("x", 30)+"\nb\nc\nd\n"))
	got := h.w.all()
	if !strings.Contains(got, strings.Repeat("x", 30)) || strings.Contains(got, "more lines") || strings.Count(got, "\x1b[K\n") != 4 {
		t.Fatalf("got %q", got)
	}
}

func TestTerminalResizeTriggersRedraw(t *testing.T) {
	h := newTermHarness(t)
	h.size = termSize{80, 24, true}
	_ = h.r.Update(nil, termSnap("A\n"))
	h.w.reset()
	_ = h.r.Tick(h.clock)
	if len(h.w.writes) != 0 {
		t.Fatalf("wrote %q", h.w.all())
	}
	h.size = termSize{40, 24, true}
	_ = h.r.Tick(h.clock)
	if len(h.w.writes) != 1 {
		t.Fatalf("want redraw, got %q", h.w.all())
	}
}

func TestTerminalWriteError(t *testing.T) {
	h := newTermHarness(t)
	boom := errors.New("boom")
	h.w.err = boom
	if err := h.r.Update(nil, termSnap("A\n")); !errors.Is(err, boom) {
		t.Fatalf("got %v", err)
	}
}
