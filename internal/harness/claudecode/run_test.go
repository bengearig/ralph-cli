//go:build unix

package claudecode

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bengearig/ralph/internal/config"
	"github.com/bengearig/ralph/internal/harness"
)

const (
	initOK     = `{"type":"system","subtype":"init","session_id":"s1","mcp_servers":[{"name":"other","status":"failed"},{"name":"ralph","status":"connected"}]}`
	initFailed = `{"type":"system","subtype":"init","session_id":"s1","mcp_servers":[{"name":"ralph","status":"failed"}]}`
)

// fakeClaude writes an executable script that records its stdin, args, and
// working directory into dir, then runs body.
func fakeClaude(t *testing.T, body string) (bin, dir string) {
	t.Helper()
	dir = t.TempDir()
	bin = filepath.Join(dir, "claude")
	script := "#!/bin/sh\n" +
		"cat > '" + dir + "/stdin.txt'\n" +
		"printf '%s\\n' \"$@\" > '" + dir + "/args.txt'\n" +
		"pwd > '" + dir + "/pwd.txt'\n" +
		body + "\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin, dir
}

func read(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestRunSuccess(t *testing.T) {
	bin, dir := fakeClaude(t, `
printf '%s\n' '`+initOK+`'
printf '%s\n' '{"type":"assistant","message":{"content":[{"type":"text","text":"Working on it\nsecond line"},{"type":"tool_use","name":"Bash","input":{"command":"go test ./..."}},{"type":"tool_use","name":"mcp__ralph__task_set_status","input":{"id":"T-1","status":"DONE"}}]}}'
printf '%s\n' 'not json'
printf '%s' '{"type":"result","subtype":"success","is_error":false,"result":"all done","total_cost_usd":0.25,"num_turns":4,"duration_ms":1500,"session_id":"s1"}'
`)
	work := t.TempDir()
	var transcript bytes.Buffer
	var events []harness.Event
	h := New(config.ClaudeCodeConfig{Binary: bin, PermissionMode: "auto"})
	res, err := h.Run(context.Background(), harness.Request{
		Prompt: "do T-1", WorkDir: work, Transcript: &transcript,
		OnEvent: func(e harness.Event) { events = append(events, e) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Completed || res.IsError || res.Subtype != "success" || res.Summary != "all done" ||
		res.CostUSD != 0.25 || res.Turns != 4 || res.Duration != 1500*time.Millisecond || res.SessionID != "s1" || res.ExitCode != 0 {
		t.Errorf("result = %+v", res)
	}
	if read(t, filepath.Join(dir, "stdin.txt")) != "do T-1" {
		t.Error("prompt not delivered on stdin")
	}
	if !strings.Contains(read(t, filepath.Join(dir, "args.txt")), "--permission-mode\nauto\n") {
		t.Error("args not passed")
	}
	gotWD, _ := filepath.EvalSymlinks(strings.TrimSpace(read(t, filepath.Join(dir, "pwd.txt"))))
	wantWD, _ := filepath.EvalSymlinks(work)
	if gotWD != wantWD {
		t.Errorf("working dir = %q, want %q", gotWD, wantWD)
	}
	lines := strings.Split(strings.TrimSuffix(transcript.String(), "\n"), "\n")
	if len(lines) != 4 || lines[2] != "not json" || !strings.HasPrefix(lines[3], `{"type":"result"`) {
		t.Errorf("transcript = %q", transcript.String())
	}
	want := []harness.Event{
		{Kind: harness.EventText, Text: "Working on it …"},
		{Kind: harness.EventTool, Text: "Bash go test ./..."},
		{Kind: harness.EventTool, Text: "ralph task_set_status T-1 DONE"},
	}
	if len(events) != len(want) {
		t.Fatalf("events = %+v", events)
	}
	for i := range want {
		if events[i] != want[i] {
			t.Errorf("event %d = %+v, want %+v", i, events[i], want[i])
		}
	}
}

func TestRunErrorResultAndExitCode(t *testing.T) {
	bin, _ := fakeClaude(t, `
printf '%s\n' '`+initOK+`'
printf '%s\n' '{"type":"result","subtype":"error_max_budget_usd","is_error":true,"result":"budget exceeded","total_cost_usd":1}'
exit 3`)
	res, err := New(config.ClaudeCodeConfig{Binary: bin}).Run(context.Background(), harness.Request{WorkDir: t.TempDir()})
	if err != nil {
		t.Fatalf("session failures belong in the result: %v", err)
	}
	if res.ExitCode != 3 || !res.IsError || res.Subtype != "error_max_budget_usd" || res.Duration <= 0 {
		t.Errorf("result = %+v", res)
	}
}

func TestRunWithoutResultUsesStderr(t *testing.T) {
	bin, _ := fakeClaude(t, `printf '%s\n' 'authentication failed' >&2; exit 1`)
	res, err := New(config.ClaudeCodeConfig{Binary: bin}).Run(context.Background(), harness.Request{WorkDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if res.Completed || res.ExitCode != 1 || res.Summary != "authentication failed" {
		t.Errorf("result = %+v", res)
	}
}

func shortGrace(t *testing.T) {
	t.Helper()
	old := stopGrace
	stopGrace = 300 * time.Millisecond
	t.Cleanup(func() { stopGrace = old })
}

func TestRunStopsWhenRalphToolsUnavailable(t *testing.T) {
	shortGrace(t)
	for name, init := range map[string]string{
		"failed":  initFailed,
		"missing": `{"type":"system","subtype":"init","mcp_servers":[{"name":"other","status":"connected"}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			bin, _ := fakeClaude(t, "printf '%s\n' '"+init+"'\nsleep 30")
			start := time.Now()
			_, err := New(config.ClaudeCodeConfig{Binary: bin}).Run(context.Background(), harness.Request{WorkDir: t.TempDir()})
			if !errors.Is(err, harness.ErrToolsUnavailable) {
				t.Fatalf("err = %v", err)
			}
			if !strings.Contains(err.Error(), "approve") {
				t.Errorf("error lacks guidance: %v", err)
			}
			if time.Since(start) > 5*time.Second {
				t.Error("session was not stopped promptly")
			}
		})
	}
}

func TestRunWithoutMCPStatusContinues(t *testing.T) {
	bin, _ := fakeClaude(t, `printf '%s\n' '{"type":"system","subtype":"init"}'
printf '%s\n' '{"type":"result","subtype":"success"}'`)
	var events []harness.Event
	res, err := New(config.ClaudeCodeConfig{Binary: bin}).Run(context.Background(), harness.Request{
		WorkDir: t.TempDir(), OnEvent: func(e harness.Event) { events = append(events, e) },
	})
	if err != nil || !res.Completed {
		t.Fatalf("res = %+v, err = %v", res, err)
	}
	if len(events) != 1 || events[0].Kind != harness.EventInfo {
		t.Errorf("events = %+v", events)
	}
}

func TestRunCancellation(t *testing.T) {
	shortGrace(t)

	for name, body := range map[string]string{
		"cooperative":    "printf '%s\n' '" + initOK + "'\nsleep 30",
		"ignores SIGINT": "trap '' INT\nprintf '%s\n' '" + initOK + "'\nsleep 30",
	} {
		t.Run(name, func(t *testing.T) {
			bin, _ := fakeClaude(t, body)
			ctx, cancel := context.WithCancel(context.Background())
			time.AfterFunc(300*time.Millisecond, cancel)
			start := time.Now()
			_, err := New(config.ClaudeCodeConfig{Binary: bin}).Run(ctx, harness.Request{WorkDir: t.TempDir()})
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("err = %v", err)
			}
			if d := time.Since(start); d > 5*time.Second {
				t.Errorf("took %v to stop", d)
			}
		})
	}
}

func TestRunReturnsWhenLeftoverProcessHoldsStdout(t *testing.T) {
	bin, _ := fakeClaude(t, `sleep 30 &
printf '%s\n' '`+initOK+`'
printf '%s\n' '{"type":"result","subtype":"success"}'`)
	start := time.Now()
	res, err := New(config.ClaudeCodeConfig{Binary: bin}).Run(context.Background(), harness.Request{WorkDir: t.TempDir()})
	if err != nil || !res.Completed {
		t.Fatalf("res = %+v, err = %v", res, err)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("took %v; a background process kept the session open", d)
	}
}

func TestRunMissingBinary(t *testing.T) {
	_, err := New(config.ClaudeCodeConfig{Binary: filepath.Join(t.TempDir(), "nope")}).Run(context.Background(), harness.Request{WorkDir: t.TempDir()})
	if err == nil || !strings.Contains(err.Error(), "starting") {
		t.Errorf("err = %v", err)
	}
}

func TestRunUnreadLargePromptDoesNotHang(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "claude")
	script := "#!/bin/sh\nprintf '%s\\n' '" + `{"type":"result","subtype":"success"}` + "'\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	res, err := New(config.ClaudeCodeConfig{Binary: bin}).Run(context.Background(), harness.Request{
		WorkDir: dir, Prompt: strings.Repeat("x", 1<<20),
	})
	if err != nil || !res.Completed {
		t.Fatalf("res = %+v, err = %v", res, err)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("took %v", d)
	}
}
