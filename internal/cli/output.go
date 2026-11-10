package cli

import (
	"encoding/json"
	"fmt"
	"strings"
	"text/tabwriter"

	"github.com/bengearig/ralph/internal/service"
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

func (a *app) cardsEnabled() bool { return a.svc != nil && a.svc.CardsEnabled() }

func pendingSuffix(c *service.CardView) string {
	if c.Pending == "" {
		return ""
	}
	return " (pending " + c.Pending + ")"
}

// cardLine renders a resource's card for a show command; it is empty without one.
func cardLine(c *service.CardView) string {
	if c == nil {
		return ""
	}
	line := "card:"
	if c.Key != "" {
		line += " " + c.Key
		if c.URL != "" {
			line += " " + c.URL
		}
	}
	line += pendingSuffix(c) + "\n"
	if c.LastError != "" {
		line += "card error: " + c.LastError + "\n"
	}
	return line
}

// cardCell renders a resource's card for a table column.
func cardCell(c *service.CardView) string {
	if c == nil {
		return ""
	}
	if c.Key == "" {
		return strings.TrimSpace(pendingSuffix(c))
	}
	return c.Key + pendingSuffix(c)
}

// withCard adds the CARD column to a table when a provider is configured.
func (a *app) withCard(header []string, rows [][]string, cards []*service.CardView) ([]string, [][]string) {
	if !a.cardsEnabled() {
		return header, rows
	}
	for i := range rows {
		rows[i] = append(rows[i], cardCell(cards[i]))
	}
	return append(header, "CARD"), rows
}
