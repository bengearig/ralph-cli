package loop

import (
	"fmt"
	"io"

	"github.com/bengearig/ralph/internal/harness"
)

// feed writes the human-readable progress of a loop run.
type feed struct {
	w io.Writer
}

func (f feed) printf(format string, args ...any) {
	if f.w != nil {
		fmt.Fprintf(f.w, format, args...)
	}
}

func (f feed) event(e harness.Event) {
	switch e.Kind {
	case harness.EventTool:
		f.printf("    · %s\n", e.Text)
	case harness.EventText:
		f.printf("    » %s\n", e.Text)
	case harness.EventInfo:
		f.printf("    ! %s\n", e.Text)
	}
}

// FormatCost renders a session or run cost; harnesses that report none show n/a.
func FormatCost(usd float64) string {
	if usd == 0 {
		return "cost n/a"
	}
	return fmt.Sprintf("$%.2f", usd)
}
