package claudecode

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
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
		".claude/commands/ralph/search.md", ".claude/commands/ralph/reconcile.md", ".claude/commands/ralph/import.md"} {
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

func snapshot(t *testing.T, root string) map[string]string {
	t.Helper()
	snap := map[string]string{}
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		r, _ := filepath.Rel(root, p)
		if d.IsDir() {
			snap[r+"/"] = ""
			return nil
		}
		data, err := os.ReadFile(p)
		snap[r] = string(data)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return snap
}

func writeFile(t *testing.T, root, name, content string) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func installedProject(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if _, err := New(config.ClaudeCodeConfig{Binary: "claude"}).Install(root, harness.InstallOptions{}); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestInstallDryRunMatchesRealRun(t *testing.T) {
	const receive = ".claude/commands/ralph/receive.md"
	old := []byte("an earlier receive\n")
	saved := shippedCommands
	t.Cleanup(func() { shippedCommands = saved })
	shippedCommands = map[string][]string{"receive.md": {commandDigest(old)}}

	fixtures := map[string]func(t *testing.T, root string){
		"empty":     func(t *testing.T, root string) {},
		"installed": func(t *testing.T, root string) { copyTree(t, installedProject(t), root) },
		"earlier shipped command": func(t *testing.T, root string) {
			copyTree(t, installedProject(t), root)
			writeFile(t, root, receive, string(old))
		},
		"modified command": func(t *testing.T, root string) {
			copyTree(t, installedProject(t), root)
			writeFile(t, root, receive, "my version")
		},
		"missing command": func(t *testing.T, root string) {
			copyTree(t, installedProject(t), root)
			if err := os.Remove(filepath.Join(root, filepath.FromSlash(receive))); err != nil {
				t.Fatal(err)
			}
		},
		"different mcp entry": func(t *testing.T, root string) {
			writeFile(t, root, ".mcp.json", `{"mcpServers":{"ralph":{"command":"other"}}}`)
		},
		"missing permission": func(t *testing.T, root string) {
			writeFile(t, root, ".claude/settings.json", `{"permissions":{"allow":["Bash"]}}`)
		},
		"outdated claude block": func(t *testing.T, root string) {
			writeFile(t, root, "CLAUDE.md", "# P\n\n"+sectionBegin+"\nold\n"+sectionEnd+"\ntail\n")
		},
		"claude without block": func(t *testing.T, root string) {
			writeFile(t, root, "CLAUDE.md", "# P\n")
		},
	}
	h := New(config.ClaudeCodeConfig{Binary: "claude"})
	for name, setup := range fixtures {
		t.Run(name, func(t *testing.T) {
			dry, real := t.TempDir(), t.TempDir()
			setup(t, dry)
			setup(t, real)
			before := snapshot(t, dry)

			dryChanges, err := h.Install(dry, harness.InstallOptions{DryRun: true})
			if err != nil {
				t.Fatal(err)
			}
			if after := snapshot(t, dry); !reflect.DeepEqual(before, after) {
				t.Errorf("dry run modified the project:\nbefore %v\nafter  %v", before, after)
			}
			realChanges, err := h.Install(real, harness.InstallOptions{})
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(dryChanges, realChanges) {
				t.Errorf("dry %v\nreal %v", dryChanges, realChanges)
			}
		})
	}
}

func copyTree(t *testing.T, from, to string) {
	t.Helper()
	for r, content := range snapshot(t, from) {
		if strings.HasSuffix(r, "/") {
			continue
		}
		writeFile(t, to, filepath.ToSlash(r), content)
	}
}

func TestInstallDryRunCreatesNoDirectories(t *testing.T) {
	root := t.TempDir()
	if _, err := New(config.ClaudeCodeConfig{Binary: "claude"}).Install(root, harness.InstallOptions{DryRun: true}); err != nil {
		t.Fatal(err)
	}
	if entries, _ := os.ReadDir(root); len(entries) != 0 {
		t.Errorf("dry run created %v", entries)
	}
}

func TestInstallDryRunReturnsSameErrors(t *testing.T) {
	h := New(config.ClaudeCodeConfig{Binary: "claude"})
	for name, setup := range map[string]func(root string){
		"invalid mcp json": func(root string) { writeFile(t, root, ".mcp.json", "{nope") },
		"unbalanced markers": func(root string) {
			writeFile(t, root, "CLAUDE.md", sectionBegin+"\nno end\n")
		},
	} {
		t.Run(name, func(t *testing.T) {
			dry, real := t.TempDir(), t.TempDir()
			setup(dry)
			setup(real)
			_, dryErr := h.Install(dry, harness.InstallOptions{DryRun: true})
			_, realErr := h.Install(real, harness.InstallOptions{})
			if dryErr == nil || realErr == nil {
				t.Fatalf("errors: dry %v, real %v", dryErr, realErr)
			}
			norm := func(err error, root string) string { return strings.ReplaceAll(err.Error(), root, "ROOT") }
			if norm(dryErr, dry) != norm(realErr, real) {
				t.Errorf("dry %q, real %q", dryErr, realErr)
			}
		})
	}
}

func TestInstallIgnore(t *testing.T) {
	const receive = ".claude/commands/ralph/receive.md"
	root := installedProject(t)
	writeFile(t, root, receive, "stale")
	writeFile(t, root, ".claude/commands/ralph/status.md", "also stale")
	writeFile(t, root, "CLAUDE.md", "# P\n")
	h := New(config.ClaudeCodeConfig{Binary: "claude"})

	changes, err := h.Install(root, harness.InstallOptions{Force: true, Ignore: []string{receive, "CLAUDE.md"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range changes {
		switch filepath.ToSlash(c.Path) {
		case receive, "CLAUDE.md":
			if c.Action != harness.Unchanged || c.Note != "ignored by init.ignore" {
				t.Errorf("%s = %+v", c.Path, c)
			}
		case ".claude/commands/ralph/status.md":
			if c.Action != harness.Updated {
				t.Errorf("status.md = %+v", c)
			}
		}
	}
	if data, _ := os.ReadFile(filepath.Join(root, filepath.FromSlash(receive))); string(data) != "stale" {
		t.Error("ignored command modified")
	}
	if data, _ := os.ReadFile(filepath.Join(root, "CLAUDE.md")); string(data) != "# P\n" {
		t.Error("ignored CLAUDE.md modified")
	}
	if data, _ := os.ReadFile(filepath.Join(root, ".claude", "commands", "ralph", "status.md")); string(data) == "also stale" {
		t.Error("non-ignored command not updated")
	}
}

func TestInstallKeepMCPCommand(t *testing.T) {
	h := New(config.ClaudeCodeConfig{Binary: "claude"})
	mcpAction := func(t *testing.T, content string, opts harness.InstallOptions) (string, string) {
		root := t.TempDir()
		if content != "" {
			writeFile(t, root, ".mcp.json", content)
		}
		changes, err := h.Install(root, opts)
		if err != nil {
			t.Fatal(err)
		}
		data, _ := os.ReadFile(filepath.Join(root, ".mcp.json"))
		return actions(changes)[".mcp.json"], string(data)
	}
	abs := `{"mcpServers":{"ralph":{"type":"stdio","command":"/abs/ralph","args":["mcp"]}}}`
	if a, _ := mcpAction(t, abs, harness.InstallOptions{KeepMCPCommand: true}); a != harness.Unchanged {
		t.Errorf("kept entry = %q", a)
	}
	if a, _ := mcpAction(t, abs, harness.InstallOptions{}); a != harness.Skipped {
		t.Errorf("entry without option = %q", a)
	}
	otherArgs := `{"mcpServers":{"ralph":{"type":"stdio","command":"/abs/ralph","args":["serve"]}}}`
	if a, _ := mcpAction(t, otherArgs, harness.InstallOptions{KeepMCPCommand: true}); a != harness.Skipped {
		t.Errorf("other args = %q", a)
	}
	otherType := `{"mcpServers":{"ralph":{"type":"http","command":"/abs/ralph","args":["mcp"]}}}`
	if a, _ := mcpAction(t, otherType, harness.InstallOptions{KeepMCPCommand: true}); a != harness.Skipped {
		t.Errorf("other type = %q", a)
	}
	a, data := mcpAction(t, "", harness.InstallOptions{KeepMCPCommand: true})
	if a != harness.Created || !strings.Contains(data, `"command": "ralph"`) {
		t.Errorf("missing entry = %q %s", a, data)
	}
}
