// Package search chunks entity text, keeps chunk embeddings current, and
// answers queries by vector similarity with a keyword fallback.
package search

import (
	"strings"
	"unicode/utf8"
)

// Chunker splits text into retrieval-sized pieces.
type Chunker struct {
	MaxChars     int
	OverlapChars int
}

// Split chunks a titled markdown body. Sections start at ATX headings outside
// fenced code; adjacent small sections are packed together, and oversized
// sections are windowed with overlap. Each chunk is prefixed with the title
// so it stays meaningful on its own.
func (c Chunker) Split(title, body string) []string {
	title = strings.TrimSpace(title)
	body = strings.TrimSpace(body)
	if body == "" {
		if title == "" {
			return nil
		}
		return []string{title}
	}
	prefix := ""
	if title != "" {
		prefix = title + "\n\n"
	}
	budget := c.MaxChars - utf8.RuneCountInString(prefix)
	if budget < 1 {
		// A title longer than the budget still needs room for content.
		budget = c.MaxChars
	}
	overlap := min(c.OverlapChars, budget-1)

	var pieces []string
	for _, sec := range sections(body) {
		if utf8.RuneCountInString(sec) <= budget {
			pieces = append(pieces, sec)
			continue
		}
		pieces = append(pieces, window(sec, budget, overlap)...)
	}

	var chunks []string
	var cur strings.Builder
	curLen := 0
	flush := func() {
		if cur.Len() > 0 {
			chunks = append(chunks, prefix+cur.String())
			cur.Reset()
			curLen = 0
		}
	}
	for _, p := range pieces {
		n := utf8.RuneCountInString(p)
		if curLen > 0 && curLen+2+n > budget {
			flush()
		}
		if curLen > 0 {
			cur.WriteString("\n\n")
			curLen += 2
		}
		cur.WriteString(p)
		curLen += n
	}
	flush()
	return chunks
}

func sections(body string) []string {
	var out []string
	var cur []string
	fence := ""
	push := func() {
		if s := strings.TrimSpace(strings.Join(cur, "\n")); s != "" {
			out = append(out, s)
		}
		cur = cur[:0]
	}
	for _, line := range strings.Split(body, "\n") {
		trimmed := strings.TrimSpace(line)
		switch {
		case fence != "":
			if strings.HasPrefix(trimmed, fence) {
				fence = ""
			}
		case strings.HasPrefix(trimmed, "```"):
			fence = "```"
		case strings.HasPrefix(trimmed, "~~~"):
			fence = "~~~"
		case isHeading(trimmed):
			push()
		}
		cur = append(cur, line)
	}
	push()
	return out
}

func isHeading(line string) bool {
	n := 0
	for n < len(line) && line[n] == '#' {
		n++
	}
	return n >= 1 && n <= 6 && (n == len(line) || line[n] == ' ' || line[n] == '\t')
}

// window splits s into pieces of at most size runes, each starting overlap
// runes before the previous one ended, preferring whitespace boundaries.
func window(s string, size, overlap int) []string {
	r := []rune(s)
	var out []string
	start := 0
	for start < len(r) {
		end := min(start+size, len(r))
		if end < len(r) {
			// Break at the last whitespace in the back half of the window.
			for i := end; i > start+size/2; i-- {
				if r[i-1] == ' ' || r[i-1] == '\n' || r[i-1] == '\t' {
					end = i
					break
				}
			}
		}
		if piece := strings.TrimSpace(string(r[start:end])); piece != "" {
			out = append(out, piece)
		}
		if end == len(r) {
			break
		}
		next := end - overlap
		if next <= start {
			next = end
		}
		start = next
	}
	return out
}
