package claudecode

import (
	"bytes"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"

	"github.com/bengearig/ralph/internal/harness"
	"github.com/bengearig/ralph/internal/workflow"
)

//go:embed templates
var templates embed.FS

const (
	// ServerName is the key under mcpServers; tools appear as mcp__ralph__*.
	ServerName = "ralph"
	// PermissionRule allows every tool from the ralph MCP server.
	PermissionRule = "mcp__" + ServerName

	sectionBegin = "<!-- ralph:begin -->"
	sectionEnd   = "<!-- ralph:end -->"

	ignoredNote = "ignored by init.ignore"
)

// Install registers the MCP server, allows its tools, installs the RALPH
// slash commands, and adds a CLAUDE.md section. It is idempotent: unchanged
// files are not rewritten. With opts.DryRun it returns the same changes and
// writes nothing.
func (h *Harness) Install(root string, opts harness.InstallOptions) ([]harness.Change, error) {
	if opts.RalphCommand == "" {
		opts.RalphCommand = "ralph"
	}
	var changes []harness.Change
	steps := []func() (harness.Change, error){
		func() (harness.Change, error) { return installMCP(root, opts) },
		func() (harness.Change, error) { return installPermission(root, opts) },
		func() (harness.Change, error) { return syncClaudeMD(root, opts) },
	}
	for _, step := range steps {
		c, err := step()
		if err != nil {
			return changes, err
		}
		changes = append(changes, c)
	}
	cmds, err := installCommands(root, opts)
	changes = append(changes, cmds...)
	return changes, err
}

func ignored(opts harness.InstallOptions, relPath string) bool {
	return slices.Contains(opts.Ignore, filepath.ToSlash(relPath))
}

func ignoredChange(c harness.Change) harness.Change {
	c.Action = harness.Unchanged
	c.Note = ignoredNote
	return c
}

func rel(root, p string) string {
	if r, err := filepath.Rel(root, p); err == nil {
		return r
	}
	return p
}

// readJSONObject reads a JSON object file; a missing file yields an empty
// object. Invalid JSON is an error so user files are never clobbered.
func readJSONObject(p string) (map[string]any, bool, error) {
	data, err := os.ReadFile(p)
	if errors.Is(err, fs.ErrNotExist) {
		return map[string]any{}, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return map[string]any{}, true, nil
	}
	var obj map[string]any
	if err := json.Unmarshal(data, &obj); err != nil {
		return nil, true, fmt.Errorf("%s is not a JSON object; fix or remove it and rerun: %w", p, err)
	}
	if obj == nil {
		obj = map[string]any{}
	}
	return obj, true, nil
}

func writeJSON(p string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	return os.WriteFile(p, append(data, '\n'), 0o644)
}

func childObject(parent map[string]any, key, file string) (map[string]any, error) {
	v, ok := parent[key]
	if !ok || v == nil {
		m := map[string]any{}
		parent[key] = m
		return m, nil
	}
	m, ok := v.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("%s: %q must be a JSON object", file, key)
	}
	return m, nil
}

// MCPServerEntry is the .mcp.json entry for ralph.
func MCPServerEntry(command string) map[string]any {
	return map[string]any{"type": "stdio", "command": command, "args": []any{"mcp"}}
}

func installMCP(root string, opts harness.InstallOptions) (harness.Change, error) {
	p := filepath.Join(root, ".mcp.json")
	c := harness.Change{Path: rel(root, p)}
	if ignored(opts, c.Path) {
		return ignoredChange(c), nil
	}
	obj, existed, err := readJSONObject(p)
	if err != nil {
		return c, err
	}
	servers, err := childObject(obj, "mcpServers", c.Path)
	if err != nil {
		return c, err
	}
	want := MCPServerEntry(opts.RalphCommand)
	if cur, ok := servers[ServerName]; ok {
		if reflect.DeepEqual(cur, want) || (opts.KeepMCPCommand && isStdioMCP(cur)) {
			c.Action = harness.Unchanged
			return c, nil
		}
		if !opts.Force {
			c.Action = harness.Skipped
			c.Note = "a different " + ServerName + " server entry exists; use --force to replace it"
			return c, nil
		}
	}
	servers[ServerName] = want
	c.Action = harness.Created
	if existed {
		c.Action = harness.Updated
	}
	if opts.DryRun {
		return c, nil
	}
	return c, writeJSON(p, obj)
}

// isStdioMCP reports whether an entry launches `<command> mcp` over stdio,
// whatever the command is.
func isStdioMCP(entry any) bool {
	m, ok := entry.(map[string]any)
	return ok && m["type"] == "stdio" && reflect.DeepEqual(m["args"], []any{"mcp"})
}

func installPermission(root string, opts harness.InstallOptions) (harness.Change, error) {
	p := filepath.Join(root, ".claude", "settings.json")
	c := harness.Change{Path: rel(root, p)}
	if ignored(opts, c.Path) {
		return ignoredChange(c), nil
	}
	obj, existed, err := readJSONObject(p)
	if err != nil {
		return c, err
	}
	perms, err := childObject(obj, "permissions", c.Path)
	if err != nil {
		return c, err
	}
	var allow []any
	switch v := perms["allow"].(type) {
	case nil:
	case []any:
		allow = v
	default:
		return c, fmt.Errorf("%s: permissions.allow must be an array", c.Path)
	}
	if slices.Contains(allow, any(PermissionRule)) {
		c.Action = harness.Unchanged
		return c, nil
	}
	perms["allow"] = append(allow, PermissionRule)
	c.Action = harness.Created
	if existed {
		c.Action = harness.Updated
	}
	if opts.DryRun {
		return c, nil
	}
	return c, writeJSON(p, obj)
}

// ClaudeMDSection returns the managed CLAUDE.md block, markers included.
func ClaudeMDSection() string {
	body, err := templates.ReadFile("templates/claude-md-section.md")
	if err != nil {
		panic(err)
	}
	return sectionBegin + "\n" + strings.TrimRight(string(body), "\n") + "\n" + sectionEnd + "\n"
}

func installClaudeMD(root string) (harness.Change, error) {
	return syncClaudeMD(root, harness.InstallOptions{})
}

func syncClaudeMD(root string, opts harness.InstallOptions) (harness.Change, error) {
	p := filepath.Join(root, "CLAUDE.md")
	c := harness.Change{Path: rel(root, p)}
	if ignored(opts, c.Path) {
		return ignoredChange(c), nil
	}
	section := ClaudeMDSection()
	data, err := os.ReadFile(p)
	if errors.Is(err, fs.ErrNotExist) {
		c.Action = harness.Created
		if opts.DryRun {
			return c, nil
		}
		return c, os.WriteFile(p, []byte(section), 0o644)
	}
	if err != nil {
		return c, err
	}
	content := string(data)
	begin := strings.Index(content, sectionBegin)
	end := strings.Index(content, sectionEnd)
	var updated string
	switch {
	case begin < 0 && end < 0:
		sep := "\n"
		if !strings.HasSuffix(content, "\n") {
			sep = "\n\n"
		}
		if content == "" {
			sep = ""
		}
		updated = content + sep + section
	case begin >= 0 && end > begin:
		tail := content[end+len(sectionEnd):]
		tail = strings.TrimPrefix(tail, "\n")
		updated = content[:begin] + section + tail
	default:
		return c, fmt.Errorf("%s has unbalanced %s / %s markers; fix them and rerun", c.Path, sectionBegin, sectionEnd)
	}
	if updated == content {
		c.Action = harness.Unchanged
		return c, nil
	}
	c.Action = harness.Updated
	if opts.DryRun {
		return c, nil
	}
	return c, os.WriteFile(p, []byte(updated), 0o644)
}

func installCommands(root string, opts harness.InstallOptions) ([]harness.Change, error) {
	dir := filepath.Join(root, ".claude", "commands", "ralph")
	if !opts.DryRun {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, err
		}
	}
	var changes []harness.Change
	for _, w := range workflow.List() {
		file := w.Name + ".md"
		want, err := workflow.RenderCommand(w.Name)
		if err != nil {
			return changes, err
		}
		p := filepath.Join(dir, file)
		c := harness.Change{Path: rel(root, p)}
		if ignored(opts, c.Path) {
			changes = append(changes, ignoredChange(c))
			continue
		}
		cur, err := os.ReadFile(p)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			c.Action = harness.Created
		case err != nil:
			return changes, err
		case bytes.Equal(cur, want):
			c.Action = harness.Unchanged
		case shippedEarlier(file, cur):
			c.Action = harness.Updated
			c.Note = "updated from an earlier ralph version"
		case opts.Force:
			c.Action = harness.Updated
		default:
			c.Action = harness.Skipped
			c.Note = "locally modified; use --force to overwrite"
		}
		if !opts.DryRun && (c.Action == harness.Created || c.Action == harness.Updated) {
			if err := os.WriteFile(p, want, 0o644); err != nil {
				return changes, err
			}
		}
		changes = append(changes, c)
	}
	return changes, nil
}
