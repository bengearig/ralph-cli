// Package adf converts Markdown to Atlassian Document Format, the JSON that
// Jira Cloud's REST API v3 takes for rich text fields.
package adf

import (
	"encoding/json"
	"net/url"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Document is an ADF document.
type Document struct {
	Version int    `json:"version"`
	Type    string `json:"type"`
	Content []Node `json:"content"`
}

// Node is a block or inline ADF node.
type Node struct {
	Type    string         `json:"type"`
	Attrs   map[string]any `json:"attrs,omitempty"`
	Content []Node         `json:"content,omitempty"`
	Text    string         `json:"text,omitempty"`
	Marks   []Mark         `json:"marks,omitempty"`
}

// Mark is an inline mark on a text node.
type Mark struct {
	Type  string         `json:"type"`
	Attrs map[string]any `json:"attrs,omitempty"`
}

// Empty reports whether the document has no content.
func (d Document) Empty() bool { return len(d.Content) == 0 }

// JSON returns the document as JSON.
func (d Document) JSON() json.RawMessage {
	if d.Content == nil {
		d.Content = []Node{}
	}
	b, err := json.Marshal(d)
	if err != nil {
		return json.RawMessage(`{"version":1,"type":"doc","content":[]}`)
	}
	return b
}

// FromMarkdown converts common Markdown to ADF. Unsupported syntax is kept as
// plain text; the conversion never fails and never drops text.
func FromMarkdown(src string) Document {
	src = strings.ReplaceAll(src, "\r\n", "\n")
	src = strings.ReplaceAll(src, "\r", "\n")
	content := parseBlocks(strings.Split(src, "\n"))
	if content == nil {
		content = []Node{}
	}
	return Document{Version: 1, Type: "doc", Content: content}
}

func parseBlocks(lines []string) []Node {
	var out []Node
	for i := 0; i < len(lines); {
		line := lines[i]
		if isBlank(line) {
			i++
			continue
		}
		if f, ok := parseFence(line); ok {
			var node Node
			node, i = buildFence(lines, i, f)
			out = append(out, node)
			continue
		}
		if level, text, ok := parseHeading(line); ok {
			out = append(out, Node{
				Type:    "heading",
				Attrs:   map[string]any{"level": level},
				Content: parseInline(text, nil),
			})
			i++
			continue
		}
		if isRule(line) {
			out = append(out, Node{Type: "rule"})
			i++
			continue
		}
		if isQuote(line) {
			var inner []string
			for i < len(lines) && isQuote(lines[i]) {
				inner = append(inner, stripQuote(lines[i]))
				i++
			}
			if content := parseBlocks(inner); len(content) > 0 {
				out = append(out, Node{Type: "blockquote", Content: content})
			}
			continue
		}
		if _, ok := parseMarker(line); ok {
			var node Node
			node, i = parseList(lines, i)
			out = append(out, node)
			continue
		}
		var para []string
		for i < len(lines) && !isBlank(lines[i]) && (len(para) == 0 || !interrupts(lines[i])) {
			para = append(para, strings.TrimSpace(lines[i]))
			i++
		}
		out = append(out, paragraph(para))
	}
	return out
}

func paragraph(lines []string) Node {
	var content []Node
	for n, l := range lines {
		if n > 0 {
			content = append(content, Node{Type: "hardBreak"})
		}
		content = append(content, parseInline(l, nil)...)
	}
	return Node{Type: "paragraph", Content: content}
}

func interrupts(line string) bool {
	if _, ok := parseFence(line); ok {
		return true
	}
	if _, _, ok := parseHeading(line); ok {
		return true
	}
	if isRule(line) || isQuote(line) {
		return true
	}
	_, ok := parseMarker(line)
	return ok
}

func isBlank(line string) bool { return strings.TrimSpace(line) == "" }

type fence struct {
	char   byte
	length int
	lang   string
	indent int
}

func parseFence(line string) (fence, bool) {
	t := strings.TrimLeft(line, " \t")
	if len(t) < 3 || (t[0] != '`' && t[0] != '~') {
		return fence{}, false
	}
	n := runLen(t, 0, t[0])
	if n < 3 {
		return fence{}, false
	}
	info := strings.TrimSpace(t[n:])
	if t[0] == '`' && strings.Contains(info, "`") {
		return fence{}, false
	}
	f := fence{char: t[0], length: n, indent: indentOf(line)}
	if fields := strings.Fields(info); len(fields) > 0 {
		f.lang = fields[0]
	}
	return f, true
}

func buildFence(lines []string, i int, f fence) (Node, int) {
	i++
	var body []string
	for i < len(lines) {
		t := strings.TrimSpace(lines[i])
		if len(t) >= f.length && runLen(t, 0, f.char) == len(t) {
			i++
			break
		}
		body = append(body, dedent(lines[i], f.indent))
		i++
	}
	node := Node{Type: "codeBlock"}
	if f.lang != "" {
		node.Attrs = map[string]any{"language": f.lang}
	}
	if text := strings.Join(body, "\n"); text != "" {
		node.Content = []Node{{Type: "text", Text: text}}
	}
	return node, i
}

func parseHeading(line string) (int, string, bool) {
	if indentOf(line) > 3 {
		return 0, "", false
	}
	t := strings.TrimLeft(line, " \t")
	n := runLen(t, 0, '#')
	if n < 1 || n > 6 || n >= len(t) || (t[n] != ' ' && t[n] != '\t') {
		return 0, "", false
	}
	text := strings.TrimSpace(t[n:])
	if text == "" {
		return 0, "", false
	}
	return n, text, true
}

func isRule(line string) bool {
	t := strings.TrimSpace(line)
	if t == "" || (t[0] != '-' && t[0] != '*' && t[0] != '_') {
		return false
	}
	count := 0
	for i := 0; i < len(t); i++ {
		switch t[i] {
		case t[0]:
			count++
		case ' ', '\t':
		default:
			return false
		}
	}
	return count >= 3
}

func isQuote(line string) bool {
	return strings.HasPrefix(strings.TrimLeft(line, " \t"), ">")
}

func stripQuote(line string) string {
	t := strings.TrimLeft(line, " \t")[1:]
	if t != "" && (t[0] == ' ' || t[0] == '\t') {
		t = t[1:]
	}
	return t
}

type marker struct {
	indent  int
	ordered bool
	num     int
	rest    string
}

func parseMarker(line string) (marker, bool) {
	t := strings.TrimLeft(line, " \t")
	if t == "" {
		return marker{}, false
	}
	m := marker{indent: indentOf(line)}
	var after int
	switch c := t[0]; {
	case c == '-' || c == '*' || c == '+':
		after = 1
	case c >= '0' && c <= '9':
		n := 0
		for n < len(t) && t[n] >= '0' && t[n] <= '9' {
			n++
		}
		if n > 9 || n >= len(t) || (t[n] != '.' && t[n] != ')') {
			return marker{}, false
		}
		m.ordered = true
		m.num, _ = strconv.Atoi(t[:n])
		after = n + 1
	default:
		return marker{}, false
	}
	if after < len(t) && t[after] != ' ' && t[after] != '\t' {
		return marker{}, false
	}
	m.rest = strings.TrimLeft(t[after:], " \t")
	if isCheckbox(m.rest) || isRule(line) {
		return marker{}, false
	}
	return m, true
}

func isCheckbox(s string) bool {
	if len(s) < 3 || s[0] != '[' || s[2] != ']' || (s[1] != ' ' && s[1] != 'x' && s[1] != 'X') {
		return false
	}
	return len(s) == 3 || s[3] == ' ' || s[3] == '\t'
}

func parseList(lines []string, i int) (Node, int) {
	first, _ := parseMarker(lines[i])
	base := first.indent
	list := Node{Type: "bulletList"}
	if first.ordered {
		list.Type = "orderedList"
		if first.num != 1 {
			list.Attrs = map[string]any{"order": first.num}
		}
	}
	for i < len(lines) {
		m, ok := parseMarker(lines[i])
		if !ok || m.ordered != first.ordered || m.indent >= base+2 {
			break
		}
		item := []string{m.rest}
		j := i + 1
		for j < len(lines) {
			if isBlank(lines[j]) {
				k := j
				for k < len(lines) && isBlank(lines[k]) {
					k++
				}
				if k >= len(lines) || indentOf(lines[k]) < base+2 {
					break
				}
				for ; j < k; j++ {
					item = append(item, "")
				}
				continue
			}
			if indentOf(lines[j]) < base+2 {
				break
			}
			item = append(item, dedent(lines[j], base+2))
			j++
		}
		content := parseBlocks(item)
		if len(content) == 0 {
			content = []Node{{Type: "paragraph"}}
		}
		list.Content = append(list.Content, Node{Type: "listItem", Content: content})
		i = j
		for i < len(lines) && isBlank(lines[i]) {
			i++
		}
	}
	return list, i
}

func indentOf(line string) int {
	col := 0
	for i := 0; i < len(line); i++ {
		switch line[i] {
		case ' ':
			col++
		case '\t':
			col += 4
		default:
			return col
		}
	}
	return col
}

// dedent removes up to n columns of leading whitespace.
func dedent(line string, n int) string {
	col := 0
	for i := 0; i < len(line); i++ {
		if col >= n {
			return line[i:]
		}
		switch line[i] {
		case ' ':
			col++
		case '\t':
			if col+4 > n {
				return strings.Repeat(" ", col+4-n) + line[i+1:]
			}
			col += 4
		default:
			return line[i:]
		}
	}
	return ""
}

const punctuation = "!\"#$%&'()*+,-./:;<=>?@[\\]^_`{|}~"

func isPunct(c byte) bool { return strings.IndexByte(punctuation, c) >= 0 }

func runLen(s string, i int, c byte) int {
	n := 0
	for i+n < len(s) && s[i+n] == c {
		n++
	}
	return n
}

func isAlnum(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }

func parseInline(s string, marks []Mark) []Node {
	var nodes []Node
	var buf strings.Builder
	flush := func() {
		if buf.Len() > 0 {
			nodes = append(nodes, Node{Type: "text", Text: buf.String(), Marks: withMarks(marks)})
			buf.Reset()
		}
	}
	for i := 0; i < len(s); {
		c := s[i]
		switch c {
		case '\\':
			if i+1 < len(s) && isPunct(s[i+1]) {
				buf.WriteByte(s[i+1])
				i += 2
				continue
			}
		case '`':
			n := runLen(s, i, '`')
			end := findCodeClose(s, i+n, n)
			if end < 0 {
				buf.WriteString(s[i : i+n])
				i += n
				continue
			}
			code := s[i+n : end]
			if len(code) >= 2 && code[0] == ' ' && code[len(code)-1] == ' ' && strings.Trim(code, " ") != "" {
				code = code[1 : len(code)-1]
			}
			flush()
			nodes = append(nodes, Node{Type: "text", Text: code, Marks: withMarks(codeContext(marks), Mark{Type: "code"})})
			i = end + n
			continue
		case '!':
			if i+1 < len(s) && s[i+1] == '[' {
				if _, _, end, ok := matchLink(s, i+1); ok {
					buf.WriteString(s[i:end])
					i = end
					continue
				}
			}
		case '[':
			if label, dest, end, ok := matchLink(s, i); ok && !hasMark(marks, "link") {
				flush()
				inner := marks
				if href, safe := safeHref(dest); safe {
					inner = withMarks(marks, Mark{Type: "link", Attrs: map[string]any{"href": href}})
				}
				nodes = append(nodes, parseInline(label, inner)...)
				i = end
				continue
			}
		case '*', '_':
			r := runLen(s, i, c)
			if inner, k, end, ok := findEmphasis(s, i, r, c); ok {
				flush()
				mark := Mark{Type: "em"}
				if k == 2 {
					mark.Type = "strong"
				}
				nodes = append(nodes, parseInline(inner, withMarks(marks, mark))...)
				i = end
				continue
			}
			buf.WriteString(s[i : i+r])
			i += r
			continue
		}
		buf.WriteByte(c)
		i++
	}
	flush()
	return nodes
}

// withMarks copies marks and appends extra ones, skipping types already present.
func withMarks(marks []Mark, extra ...Mark) []Mark {
	var out []Mark
	out = append(out, marks...)
	for _, e := range extra {
		if !hasMark(out, e.Type) {
			out = append(out, e)
		}
	}
	return out
}

func hasMark(marks []Mark, typ string) bool {
	for _, m := range marks {
		if m.Type == typ {
			return true
		}
	}
	return false
}

// codeContext keeps only the marks ADF allows alongside code.
func codeContext(marks []Mark) []Mark {
	var out []Mark
	for _, m := range marks {
		if m.Type == "link" {
			out = append(out, m)
		}
	}
	return out
}

func findCodeClose(s string, from, n int) int {
	for j := from; j < len(s); {
		if s[j] != '`' {
			j++
			continue
		}
		m := runLen(s, j, '`')
		if m == n {
			return j
		}
		j += m
	}
	return -1
}

func matchLink(s string, i int) (label, dest string, end int, ok bool) {
	depth := 0
	closeAt := -1
	for j := i; j < len(s) && closeAt < 0; j++ {
		switch s[j] {
		case '\\':
			j++
		case '[':
			depth++
		case ']':
			depth--
			if depth == 0 {
				closeAt = j
			}
		}
	}
	if closeAt < 0 || closeAt == i+1 || closeAt+1 >= len(s) || s[closeAt+1] != '(' {
		return "", "", 0, false
	}
	depth = 1
	for k := closeAt + 2; k < len(s); k++ {
		switch s[k] {
		case '\\':
			k++
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				if k == closeAt+2 {
					return "", "", 0, false
				}
				return s[i+1 : closeAt], s[closeAt+2 : k], k + 1, true
			}
		case ' ', '\t':
			return "", "", 0, false
		}
	}
	return "", "", 0, false
}

func safeHref(dest string) (string, bool) {
	var b strings.Builder
	for i := 0; i < len(dest); i++ {
		if dest[i] == '\\' && i+1 < len(dest) && isPunct(dest[i+1]) {
			i++
		}
		b.WriteByte(dest[i])
	}
	href := b.String()
	u, err := url.Parse(href)
	if err != nil {
		return "", false
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https":
		return href, u.Host != ""
	case "mailto":
		return href, u.Opaque != "" || u.Path != ""
	}
	return "", false
}

func findEmphasis(s string, i, r int, c byte) (inner string, k, end int, ok bool) {
	after := i + r
	if after >= len(s) {
		return "", 0, 0, false
	}
	if next, _ := utf8.DecodeRuneInString(s[after:]); unicode.IsSpace(next) {
		return "", 0, 0, false
	}
	if c == '_' && i > 0 {
		if prev, _ := utf8.DecodeLastRuneInString(s[:i]); isAlnum(prev) {
			return "", 0, 0, false
		}
	}
	candidates := []int{1}
	if r >= 2 {
		candidates = []int{2}
		if r >= 3 {
			candidates = []int{2, 1}
		}
	}
	for _, k := range candidates {
		if p, m, found := findCloser(s, i+k, c, k); found {
			return s[i+k : p+m-k], k, p + m, true
		}
	}
	return "", 0, 0, false
}

func findCloser(s string, start int, c byte, k int) (pos, length int, ok bool) {
	for j := start; j < len(s); {
		switch s[j] {
		case '\\':
			j += 2
		case '`':
			n := runLen(s, j, '`')
			if e := findCodeClose(s, j+n, n); e >= 0 {
				j = e + n
			} else {
				j += n
			}
		case c:
			m := runLen(s, j, c)
			if (m >= k && k == 2 || m == k) && j+m-k > start && closes(s, j, m, c) {
				return j, m, true
			}
			j += m
		default:
			j++
		}
	}
	return 0, 0, false
}

func closes(s string, j, m int, c byte) bool {
	prev, _ := utf8.DecodeLastRuneInString(s[:j])
	if unicode.IsSpace(prev) {
		return false
	}
	if c == '_' && j+m < len(s) {
		if next, _ := utf8.DecodeRuneInString(s[j+m:]); isAlnum(next) {
			return false
		}
	}
	return true
}
