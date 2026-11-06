package cli

import (
	"encoding/json"
	"fmt"
	"strings"
	"text/tabwriter"
)

func (a *app) writeJSON(v any) error {
	enc := json.NewEncoder(a.streams.Out)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// writeTable renders rows as aligned columns. Cell text is flattened to a
// single line so multi-line fields cannot break the layout.
func (a *app) writeTable(header []string, rows [][]string) error {
	tw := tabwriter.NewWriter(a.streams.Out, 0, 0, 2, ' ', 0)
	if _, err := fmt.Fprintln(tw, strings.Join(header, "\t")); err != nil {
		return err
	}
	for _, r := range rows {
		cells := make([]string, len(r))
		for i, c := range r {
			cells[i] = oneLine(c)
		}
		if _, err := fmt.Fprintln(tw, strings.Join(cells, "\t")); err != nil {
			return err
		}
	}
	return tw.Flush()
}

func (a *app) printf(format string, args ...any) error {
	_, err := fmt.Fprintf(a.streams.Out, format, args...)
	return err
}

func oneLine(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	const maxWidth = 60
	if r := []rune(s); len(r) > maxWidth {
		return string(r[:maxWidth-1]) + "…"
	}
	return s
}
