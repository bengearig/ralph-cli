package cli

import (
	"fmt"
	"io"
	"os"
	"strings"
	"time"
)

const (
	highlightFor = 3 * time.Second

	escHideCursor = "\x1b[?25l"
	escShowCursor = "\x1b[?25h"
	escHome       = "\x1b[H"
	escClearLine  = "\x1b[K"
	escClearBelow = "\x1b[J"
	escReset      = "\x1b[0m"
	escReverse    = "\x1b[7m"
	escBold       = "\x1b[1m"
	escDim        = "\x1b[2m"
)

type termSize struct {
	width, height int
	ok            bool
}

// terminalRenderer redraws a watch in place and highlights changed lines.
type terminalRenderer struct {
	out       io.Writer
	size      func() (int, int, bool)
	interval  time.Duration
	now       func() time.Time
	highlight string

	lines     []string
	expiry    []time.Time
	updatedAt time.Time
	notice    string
	drawn     bool
	lastSize  termSize
	stopped   bool
}

func (a *app) newTerminalRenderer(interval time.Duration, now func() time.Time) *terminalRenderer {
	hl := escReverse
	if os.Getenv("NO_COLOR") != "" {
		hl = escBold
	}
	return &terminalRenderer{
		out:       a.streams.Out,
		size:      a.streams.OutSize,
		interval:  interval,
		now:       now,
		highlight: hl,
	}
}

func (r *terminalRenderer) Start() error {
	_, err := io.WriteString(r.out, escHideCursor)
	return err
}

func (r *terminalRenderer) Stop() error {
	if r.stopped {
		return nil
	}
	r.stopped = true
	_, err := io.WriteString(r.out, escShowCursor+"\n")
	return err
}

func (r *terminalRenderer) Update(prev, cur *snapshot) error {
	now := r.now()
	lines := splitViewLines(cur.Text)
	expiry := make([]time.Time, len(lines))

	if prev != nil {
		seen := map[string]int{}
		for _, l := range splitViewLines(prev.Text) {
			seen[l]++
		}
		for i, l := range lines {
			if seen[l] > 0 {
				seen[l]--
				continue
			}
			if strings.TrimSpace(l) != "" {
				expiry[i] = now.Add(highlightFor)
			}
		}
		for i, l := range r.lines {
			if !r.expiry[i].After(now) {
				continue
			}
			for j, c := range lines {
				if c == l && expiry[j].IsZero() && strings.TrimSpace(c) != "" {
					expiry[j] = r.expiry[i]
					break
				}
			}
		}
	}

	r.lines, r.expiry = lines, expiry
	r.updatedAt = now
	return r.draw(now)
}

func (r *terminalRenderer) Tick(now time.Time) error {
	if !r.drawn {
		return nil
	}
	redraw := r.currentSize() != r.lastSize
	for i, e := range r.expiry {
		if !e.IsZero() && !e.After(now) {
			r.expiry[i] = time.Time{}
			redraw = true
		}
	}
	if !redraw {
		return nil
	}
	return r.draw(now)
}

func (r *terminalRenderer) Notice(message string) error {
	r.notice = message
	if !r.drawn {
		return nil
	}
	return r.draw(r.now())
}

func (r *terminalRenderer) currentSize() termSize {
	if r.size == nil {
		return termSize{}
	}
	w, h, ok := r.size()
	if !ok {
		return termSize{}
	}
	return termSize{w, h, true}
}

func (r *terminalRenderer) draw(now time.Time) error {
	sz := r.currentSize()
	r.lastSize = sz

	shown := len(r.lines)
	hidden := 0
	if sz.ok && shown > sz.height-1 {
		shown = max(sz.height-2, 0)
		hidden = len(r.lines) - shown
	}

	var b strings.Builder
	b.WriteString(escHome)
	for i := 0; i < shown; i++ {
		line := r.lines[i]
		if sz.ok {
			line = clipRunes(line, max(sz.width-1, 0))
		}
		if r.expiry[i].After(now) {
			line = r.highlight + line + escReset
		}
		b.WriteString(line)
		b.WriteString(escClearLine + "\n")
	}

	var footer string
	switch {
	case hidden > 0:
		footer = fmt.Sprintf("... %d more lines - every %s - Ctrl-C to exit", hidden, r.interval)
	case r.notice != "":
		footer = fmt.Sprintf("watching - every %s - %s - Ctrl-C to exit", r.interval, r.notice)
	default:
		footer = fmt.Sprintf("watching - every %s - updated %s - Ctrl-C to exit", r.interval, r.updatedAt.Format("15:04:05"))
	}
	b.WriteString(escDim + footer + escReset)
	b.WriteString(escClearBelow)

	r.drawn = true
	_, err := io.WriteString(r.out, b.String())
	return err
}

func splitViewLines(text string) []string {
	if text == "" {
		return nil
	}
	return strings.Split(strings.TrimSuffix(text, "\n"), "\n")
}

func clipRunes(s string, n int) string {
	i := 0
	for pos := range s {
		if i == n {
			return s[:pos]
		}
		i++
	}
	return s
}
