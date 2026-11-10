package claudecode

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/bengearig/ralph/internal/config"
	"github.com/bengearig/ralph/internal/harness"
	"github.com/bengearig/ralph/internal/workflow"
)

func actions(changes []harness.Change) map[string]string {
	m := map[string]string{}
	for _, c := range changes {
		m[c.Path] = c.Action
	}
	return m
}

func readJSON(t *testing.T, p string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestInstallFreshAndIdempotent(t *testing.T) {
	root := t.TempDir()
	h := New(config.ClaudeCodeConfig{Binary: "claude"})
	changes, err := h.Install(root, harness.InstallOptions{})
	if err != nil {
		t.Fatal(err)
	}
	got := actions(changes)
	for _, p := range []string{".mcp.json", ".claude/settings.json", "CLAUDE.md",
		".claude/commands/ralph/receive.md", ".claude/commands/ralph/articulate.md", ".claude/commands/ralph/status.md",
		".claude/commands/ralph/search.md", ".claude/commands/ralph/reconcile.md"} {
		if got[filepath.FromSlash(p)] != harness.Created {
			t.Errorf("%s: %q", p, got[filepath.FromSlash(p)])
		}
	}

	mcp := readJSON(t, filepath.Join(root, ".mcp.json"))
	entry := mcp["mcpServers"].(map[string]any)["ralph"].(map[string]any)
	if entry["command"] != "ralph" || entry["args"].([]any)[0] != "mcp" {
		t.Errorf("mcp entry = %v", entry)
	}

	again, err := h.Install(root, harness.InstallOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range again {
		if c.Action != harness.Unchanged {
			t.Errorf("second install changed %s: %s", c.Path, c.Action)
		}
	}
}

func TestInstallPreservesExistingConfig(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, ".claude"), 0o755)
	os.WriteFile(filepath.Join(root, ".mcp.json"), []byte(`{"mcpServers":{"other":{"command":"x"}},"extra":1}`), 0o644)
	os.WriteFile(filepath.Join(root, ".claude", "settings.json"), []byte(`{"permissions":{"allow":["Bash(go test:*)"],"deny":["Read(.env)"]},"model":"opus"}`), 0o644)
	os.WriteFile(filepath.Join(root, "CLAUDE.md"), []byte("# Project\n\nExisting guidance."), 0o644)

	h := New(config.ClaudeCodeConfig{Binary: "claude"})
	changes, err := h.Install(root, harness.InstallOptions{RalphCommand: "/opt/bin/ralph"})
	if err != nil {
		t.Fatal(err)
	}
	got := actions(changes)
	if got[".mcp.json"] != harness.Updated || got[filepath.FromSlash(".claude/settings.json")] != harness.Updated || got["CLAUDE.md"] != harness.Updated {
		t.Errorf("actions = %v", got)
	}

	mcp := readJSON(t, filepath.Join(root, ".mcp.json"))
	servers := mcp["mcpServers"].(map[string]any)
	if servers["other"] == nil || mcp["extra"] == nil {
		t.Errorf("existing keys lost: %v", mcp)
	}
	if servers["ralph"].(map[string]any)["command"] != "/opt/bin/ralph" {
		t.Errorf("custom command ignored: %v", servers["ralph"])
	}

	settings := readJSON(t, filepath.Join(root, ".claude", "settings.json"))
	perms := settings["permissions"].(map[string]any)
	allow := perms["allow"].([]any)
	if !slices.Contains(allow, any("Bash(go test:*)")) || !slices.Contains(allow, any(PermissionRule)) || perms["deny"] == nil || settings["model"] != "opus" {
		t.Errorf("settings = %v", settings)
	}

	md, _ := os.ReadFile(filepath.Join(root, "CLAUDE.md"))
	if !strings.HasPrefix(string(md), "# Project\n\nExisting guidance.\n\n"+sectionBegin) {
		t.Errorf("CLAUDE.md = %q", md)
	}
}

func TestInstallRespectsCustomizations(t *testing.T) {
	root := t.TempDir()
	h := New(config.ClaudeCodeConfig{Binary: "claude"})
	if _, err := h.Install(root, harness.InstallOptions{}); err != nil {
		t.Fatal(err)
	}
	receive := filepath.Join(root, ".claude", "commands", "ralph", "receive.md")
	os.WriteFile(receive, []byte("my version"), 0o644)
	if _, err := h.Install(root, harness.InstallOptions{RalphCommand: "other-ralph"}); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(receive); string(data) != "my version" {
		t.Error("customized command overwritten without --force")
	}
	mcp := readJSON(t, filepath.Join(root, ".mcp.json"))
	if mcp["mcpServers"].(map[string]any)["ralph"].(map[string]any)["command"] != "ralph" {
		t.Error("differing MCP entry replaced without --force")
	}

	changes, err := h.Install(root, harness.InstallOptions{RalphCommand: "other-ralph", Force: true})
	if err != nil {
		t.Fatal(err)
	}
	got := actions(changes)
	if got[".mcp.json"] != harness.Updated || got[filepath.FromSlash(".claude/commands/ralph/receive.md")] != harness.Updated {
		t.Errorf("force actions = %v", got)
	}
}

func TestClaudeMDSectionReplacedInPlace(t *testing.T) {
	root := t.TempDir()
	p := filepath.Join(root, "CLAUDE.md")
	os.WriteFile(p, []byte("before\n"+sectionBegin+"\nold content\n"+sectionEnd+"\nafter\n"), 0o644)
	c, err := installClaudeMD(root)
	if err != nil || c.Action != harness.Updated {
		t.Fatalf("change = %+v, %v", c, err)
	}
	data, _ := os.ReadFile(p)
	s := string(data)
	if !strings.HasPrefix(s, "before\n"+sectionBegin) || !strings.HasSuffix(s, sectionEnd+"\nafter\n") || strings.Contains(s, "old content") {
		t.Errorf("CLAUDE.md = %q", s)
	}
	if c, _ := installClaudeMD(root); c.Action != harness.Unchanged {
		t.Errorf("rerun = %s", c.Action)
	}

	os.WriteFile(p, []byte(sectionBegin+"\nno end"), 0o644)
	if _, err := installClaudeMD(root); err == nil {
		t.Error("unbalanced markers should fail")
	}
}

func TestInstallRejectsMalformedJSON(t *testing.T) {
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, ".mcp.json"), []byte(`{"mcpServers": [}`), 0o644)
	if _, err := New(config.ClaudeCodeConfig{}).Install(root, harness.InstallOptions{}); err == nil {
		t.Error("malformed .mcp.json should fail")
	}
	data, _ := os.ReadFile(filepath.Join(root, ".mcp.json"))
	if string(data) != `{"mcpServers": [}` {
		t.Error("malformed file was modified")
	}
	os.WriteFile(filepath.Join(root, ".mcp.json"), []byte(`{"mcpServers": []}`), 0o644)
	if _, err := installMCP(root, harness.InstallOptions{RalphCommand: "ralph"}); err == nil {
		t.Error("non-object mcpServers should fail")
	}
}

func TestCommand(t *testing.T) {
	h := New(config.ClaudeCodeConfig{Binary: "/usr/bin/claude", Model: "sonnet", PermissionMode: "auto", MaxBudgetUSD: 1.25, ExtraArgs: []string{"--max-turns", "40"}})
	got := strings.Join(h.Command(harness.Request{}), " ")
	want := "/usr/bin/claude --print --output-format stream-json --verbose --permission-prompts none --model sonnet --permission-mode auto --max-budget-usd 1.25 --max-turns 40"
	if got != want {
		t.Errorf("argv = %q", got)
	}
	got = strings.Join(h.Command(harness.Request{Model: "opus"}), " ")
	if !strings.Contains(got, "--model opus") || strings.Contains(got, "sonnet") {
		t.Errorf("request model should win: %q", got)
	}
	bare := strings.Join(New(config.ClaudeCodeConfig{Binary: "claude"}).Command(harness.Request{}), " ")
	if bare != "claude --print --output-format stream-json --verbose --permission-prompts none" {
		t.Errorf("bare argv = %q", bare)
	}
}

func TestRegistered(t *testing.T) {
	cfg, err := config.Defaults()
	if err != nil {
		t.Fatal(err)
	}
	h, res, err := harness.ForStep(cfg, config.StepLoop)
	if err != nil || h.Name() != Name || res.Harness != Name {
		t.Errorf("ForStep = %v, %+v, %v", h, res, err)
	}
	if _, err := harness.New("codex", cfg); err == nil {
		t.Error("unknown harness should fail")
	}
}

func TestInstallUpdatesEarlierShippedCommands(t *testing.T) {
	old := []byte("an earlier receive\n")
	saved := shippedCommands
	t.Cleanup(func() { shippedCommands = saved })
	shippedCommands = map[string][]string{"receive.md": {commandDigest(old)}}

	h := New(config.ClaudeCodeConfig{Binary: "claude"})
	for name, content := range map[string][]byte{
		"lf":   old,
		"crlf": []byte(strings.ReplaceAll(string(old), "\n", "\r\n")),
	} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			dir := filepath.Join(root, ".claude", "commands", "ralph")
			os.MkdirAll(dir, 0o755)
			os.WriteFile(filepath.Join(dir, "receive.md"), content, 0o644)
			os.WriteFile(filepath.Join(dir, "status.md"), []byte("my status\n"), 0o644)

			changes, err := h.Install(root, harness.InstallOptions{})
			if err != nil {
				t.Fatal(err)
			}
			got := actions(changes)
			if a := got[filepath.FromSlash(".claude/commands/ralph/receive.md")]; a != harness.Updated {
				t.Errorf("receive.md action = %q", a)
			}
			if a := got[filepath.FromSlash(".claude/commands/ralph/status.md")]; a != harness.Skipped {
				t.Errorf("edited status.md action = %q", a)
			}
			want, _ := workflow.RenderCommand("receive")
			if data, _ := os.ReadFile(filepath.Join(dir, "receive.md")); string(data) != string(want) {
				t.Error("receive.md not replaced with the current template")
			}
		})
	}
}

func TestInstalledCommandsMatchGoldens(t *testing.T) {
	root := t.TempDir()
	if _, err := New(config.ClaudeCodeConfig{Binary: "claude"}).Install(root, harness.InstallOptions{}); err != nil {
		t.Fatal(err)
	}
	goldens, err := filepath.Glob(filepath.Join("..", "..", "workflow", "testdata", "*.md"))
	if err != nil || len(goldens) == 0 {
		t.Fatalf("goldens: %v, %v", goldens, err)
	}
	goldens = slices.DeleteFunc(goldens, func(g string) bool { return strings.HasSuffix(g, ".neutral.md") })
	for _, g := range goldens {
		want, err := os.ReadFile(g)
		if err != nil {
			t.Fatal(err)
		}
		got, err := os.ReadFile(filepath.Join(root, ".claude", "commands", "ralph", filepath.Base(g)))
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != string(want) {
			t.Errorf("%s: installed command differs from golden", filepath.Base(g))
		}
	}
	entries, _ := os.ReadDir(filepath.Join(root, ".claude", "commands", "ralph"))
	if len(entries) != len(goldens) {
		t.Errorf("%d installed commands for %d goldens", len(entries), len(goldens))
	}
}

func TestShippedCommandDigestsAreEarlierVersions(t *testing.T) {
	for name, digests := range shippedCommands {
		cur, err := workflow.RenderCommand(strings.TrimSuffix(name, ".md"))
		if err != nil {
			t.Errorf("%s: no such workflow", name)
			continue
		}
		if slices.Contains(digests, commandDigest(cur)) {
			t.Errorf("%s: the current template is listed as an earlier version", name)
		}
	}
}
