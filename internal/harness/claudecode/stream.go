package claudecode

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/bengearig/ralph/internal/harness"
)

// streamEvent is the subset of Claude Code's stream-json output that ralph
// reads. Unknown fields and event types are ignored.
type streamEvent struct {
	Type      string `json:"type"`
	Subtype   string `json:"subtype"`
	SessionID string `json:"session_id"`
	// MCPServers is a pointer so an absent field can be told apart from an
	// empty list.
	MCPServers *[]mcpServerStatus `json:"mcp_servers"`
	Message    *struct {
		Content json.RawMessage `json:"content"`
	} `json:"message"`
	IsError      bool    `json:"is_error"`
	Result       string  `json:"result"`
	TotalCostUSD float64 `json:"total_cost_usd"`
	NumTurns     int     `json:"num_turns"`
	DurationMS   int64   `json:"duration_ms"`
}

type mcpServerStatus struct {
	Name   string `json:"name"`
	Status string `json:"status"`
}

type contentBlock struct {
	Type  string         `json:"type"`
	Text  string         `json:"text"`
	Name  string         `json:"name"`
	Input map[string]any `json:"input"`
}

const eventTextRunes = 160

// streamParser folds stream-json lines into a Result and progress events.
type streamParser struct {
	res  *harness.Result
	emit func(harness.Event)
	// toolsProblem is set when ralph's MCP server did not connect.
	toolsProblem string
}

func (p *streamParser) send(kind harness.EventKind, text string) {
	if p.emit != nil && text != "" {
		p.emit(harness.Event{Kind: kind, Text: text})
	}
}

// handle processes one line and reports whether the session must be stopped.
func (p *streamParser) handle(line []byte) (stop bool) {
	if len(line) == 0 {
		return false
	}
	var ev streamEvent
	if err := json.Unmarshal(line, &ev); err != nil {
		return false
	}
	if ev.SessionID != "" {
		p.res.SessionID = ev.SessionID
	}
	switch ev.Type {
	case "system":
		if ev.Subtype == "init" {
			return p.checkTools(ev.MCPServers)
		}
	case "assistant":
		if ev.Message != nil {
			p.assistant(ev.Message.Content)
		}
	case "result":
		p.res.Completed = true
		p.res.Subtype = ev.Subtype
		p.res.IsError = ev.IsError
		p.res.Summary = ev.Result
		p.res.CostUSD = ev.TotalCostUSD
		p.res.Turns = ev.NumTurns
		p.res.Duration = time.Duration(ev.DurationMS) * time.Millisecond
	}
	return false
}

func (p *streamParser) checkTools(servers *[]mcpServerStatus) bool {
	if servers == nil {
		p.send(harness.EventInfo, "session did not report MCP server status; assuming ralph tools are available")
		return false
	}
	for _, s := range *servers {
		if s.Name != ServerName {
			continue
		}
		if s.Status == "connected" {
			return false
		}
		p.toolsProblem = fmt.Sprintf("MCP server %q is %q", ServerName, s.Status)
		return true
	}
	p.toolsProblem = fmt.Sprintf("MCP server %q is not configured or not approved", ServerName)
	return true
}

func (p *streamParser) assistant(raw json.RawMessage) {
	var blocks []contentBlock
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return
	}
	for _, b := range blocks {
		switch b.Type {
		case "text":
			p.send(harness.EventText, firstLine(b.Text, eventTextRunes))
		case "tool_use":
			p.send(harness.EventTool, toolSummary(b.Name, b.Input))
		}
	}
}

func firstLine(s string, maxRunes int) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = strings.TrimSpace(s[:i]) + " …"
	}
	if r := []rune(s); len(r) > maxRunes {
		s = string(r[:maxRunes-1]) + "…"
	}
	return s
}

func stringArg(input map[string]any, key string) string {
	if v, ok := input[key].(string); ok {
		return v
	}
	return ""
}

// toolSummary renders a tool call as a short human-readable line.
func toolSummary(name string, input map[string]any) string {
	var detail string
	switch name {
	case "Bash":
		detail = stringArg(input, "command")
	case "Read", "Edit", "Write", "MultiEdit":
		detail = stringArg(input, "file_path")
	case "NotebookEdit":
		detail = stringArg(input, "notebook_path")
	case "Grep", "Glob":
		detail = stringArg(input, "pattern")
	default:
		if tool, ok := strings.CutPrefix(name, "mcp__"+ServerName+"__"); ok {
			name = "ralph " + tool
			var parts []string
			for _, key := range []string{"id", "milestone_id", "status", "query"} {
				if v := stringArg(input, key); v != "" {
					parts = append(parts, v)
				}
			}
			detail = strings.Join(parts, " ")
		}
	}
	if detail == "" {
		return name
	}
	return firstLine(name+" "+detail, eventTextRunes)
}

// tailBuffer keeps the last max bytes written to it.
type tailBuffer struct {
	max int
	buf []byte
}

func (t *tailBuffer) Write(b []byte) (int, error) {
	t.buf = append(t.buf, b...)
	if over := len(t.buf) - t.max; over > 0 {
		t.buf = append(t.buf[:0], t.buf[over:]...)
	}
	return len(b), nil
}

func (t *tailBuffer) String() string { return strings.TrimSpace(string(t.buf)) }
