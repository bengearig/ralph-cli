// Package progress reports the progress of a long-running command on a
// writer: a line per item, a single redrawn line, or nothing.
package progress

import (
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
	"unicode"
)

// Mode selects how a Reporter writes.
type Mode int

const (
	// Off writes nothing.
	Off Mode = iota
	// Plain writes one line per item.
	Plain
	// Live redraws a single line with a bar.
	Live
)

const (
	defaultWidth = 80
	barCells     = 16
	clearLine    = "\r\x1b[2K"
)

// Reporter writes progress to one writer. It is safe for concurrent use.
type Reporter struct {
	mu    sync.Mutex
	w     io.Writer
	mode  Mode
	width int
	total int
	n     int
	drawn bool
	done  bool
}

// New returns a Reporter writing to w. A non-positive width means 80.
func New(w io.Writer, mode Mode, width int) *Reporter {
	if width <= 0 {
		width = defaultWidth
	}
	return &Reporter{w: w, mode: mode, width: width}
}

// Start begins a run of total items under label.
func (r *Reporter) Start(label string, total int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.total, r.n, r.done = total, 0, false
	if total <= 0 {
		r.total = 0
		return
	}
	switch r.mode {
	case Plain:
		noun := "items"
		if total == 1 {
			noun = "item"
		}
		fmt.Fprintf(r.w, "%s: %d %s\n", clean(label), total, noun)
	case Live:
		r.draw("")
	}
}

// Step records one finished item, never counting past the total.
func (r *Reporter) Step(item string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.done || r.total == 0 || r.mode == Off {
		return
	}
	if r.n < r.total {
		r.n++
	}
	item = clean(item)
	switch r.mode {
	case Plain:
		fmt.Fprintf(r.w, "[%d/%d] %s\n", r.n, r.total, item)
	case Live:
		r.draw(item)
	}
}

// Finish ends the run and clears a live line. It is idempotent.
func (r *Reporter) Finish() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.done {
		return
	}
	r.done = true
	if r.mode == Live && r.drawn {
		io.WriteString(r.w, clearLine)
		r.drawn = false
	}
}

func (r *Reporter) draw(item string) {
	filled := (barCells*r.n*2 + r.total) / (r.total * 2)
	prefix := "[" + strings.Repeat("█", filled) + strings.Repeat("░", barCells-filled) +
		"] " + strconv.Itoa(r.n) + "/" + strconv.Itoa(r.total) + "  "
	avail := r.width - len([]rune(prefix))
	runes := []rune(item)
	switch {
	case avail <= 0:
		runes = nil
	case len(runes) > avail:
		runes = append(runes[:avail-1], '…')
	}
	io.WriteString(r.w, clearLine+prefix+string(runes))
	r.drawn = true
}

func clean(s string) string {
	return strings.Map(func(c rune) rune {
		if unicode.IsControl(c) {
			return ' '
		}
		return c
	}, s)
}

// WidthFromEnv returns the positive integer in COLUMNS, else 80.
func WidthFromEnv(getenv func(string) string) int {
	if n, err := strconv.Atoi(strings.TrimSpace(getenv("COLUMNS"))); err == nil && n > 0 {
		return n
	}
	return defaultWidth
}
