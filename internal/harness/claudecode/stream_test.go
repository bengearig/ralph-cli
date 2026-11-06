package claudecode

import (
	"strings"
	"testing"

	"github.com/bengearig/ralph/internal/harness"
)

func TestToolSummary(t *testing.T) {
	cases := []struct {
		name  string
		input map[string]any
		want  string
	}{
		{"Bash", map[string]any{"command": "go test ./..."}, "Bash go test ./..."},
		{"Edit", map[string]any{"file_path": "/x/a.go"}, "Edit /x/a.go"},
		{"NotebookEdit", map[string]any{"notebook_path": "n.ipynb"}, "NotebookEdit n.ipynb"},
		{"Grep", map[string]any{"pattern": "TODO"}, "Grep TODO"},
		{"mcp__ralph__task_get", map[string]any{"id": "T-3"}, "ralph task_get T-3"},
		{"mcp__ralph__search", map[string]any{"query": "auth"}, "ralph search auth"},
		{"mcp__ralph__status", nil, "ralph status"},
		{"WebFetch", map[string]any{"url": "x"}, "WebFetch"},
		{"Bash", map[string]any{"command": 42}, "Bash"},
	}
	for _, c := range cases {
		if got := toolSummary(c.name, c.input); got != c.want {
			t.Errorf("toolSummary(%s) = %q, want %q", c.name, got, c.want)
		}
	}
	long := toolSummary("Bash", map[string]any{"command": strings.Repeat("x", 500)})
	if n := len([]rune(long)); n != eventTextRunes {
		t.Errorf("long summary has %d runes", n)
	}
}

func TestTailBuffer(t *testing.T) {
	b := &tailBuffer{max: 5}
	b.Write([]byte("abc"))
	b.Write([]byte("defgh"))
	if b.String() != "defgh" {
		t.Errorf("tail = %q", b.String())
	}
}

func TestParserIgnoresJunk(t *testing.T) {
	p := &streamParser{res: &harness.Result{}}
	for _, line := range []string{"", "{", `{"type":"assistant","message":{"content":"plain string"}}`, `{"type":"unknown"}`} {
		if p.handle([]byte(line)) {
			t.Errorf("line %q stopped the session", line)
		}
	}
}
