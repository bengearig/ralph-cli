package setup

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/bengearig/ralph/internal/config"
	"github.com/bengearig/ralph/internal/harness"
	"github.com/bengearig/ralph/internal/harness/claudecode"
)

// CheckResult reports which integration files a newer ralph would change.
type CheckResult struct {
	// Installed is false when the project has no harness integration, in which
	// case there is nothing to check.
	Installed bool
	// Stale entries would be created or updated by ralph init.
	Stale []harness.Change
	// Customized entries differ locally and need ralph init --force.
	Customized []harness.Change
}

// Check dry-runs the integration steps of ralph init. It never opens the
// database, never touches the network, and never writes.
func Check(ctx context.Context, root string, cfg *config.Config) (CheckResult, error) {
	var res CheckResult
	h, err := harness.New(cfg.Harness.Default, cfg)
	if err != nil {
		return res, err
	}
	inst, ok := h.(harness.Installer)
	if !ok || !integrationInstalled(root) {
		return res, nil
	}
	res.Installed = true

	o := Options{Root: root, DryRun: true, KeepMCPCommand: true, Ignore: cfg.Init.Ignore}
	changes, err := ensureGitignore(o, cfg.StoragePath(root))
	if err != nil {
		return res, err
	}
	hc, err := inst.Install(root, harness.InstallOptions{DryRun: true, KeepMCPCommand: true, Ignore: cfg.Init.Ignore})
	if err != nil {
		return res, err
	}
	for _, c := range append([]harness.Change{changes}, hc...) {
		switch c.Action {
		case harness.Created, harness.Updated:
			res.Stale = append(res.Stale, c)
		case harness.Skipped:
			res.Customized = append(res.Customized, c)
		}
	}
	return res, nil
}

// integrationInstalled reports whether any managed harness file exists.
// Unreadable or invalid files do not count.
func integrationInstalled(root string) bool {
	if data, err := os.ReadFile(filepath.Join(root, ".mcp.json")); err == nil {
		var cfg struct {
			MCPServers map[string]json.RawMessage `json:"mcpServers"`
		}
		if json.Unmarshal(data, &cfg) == nil {
			if _, ok := cfg.MCPServers[claudecode.ServerName]; ok {
				return true
			}
		}
	}
	if info, err := os.Stat(filepath.Join(root, ".claude", "commands", "ralph")); err == nil && info.IsDir() {
		return true
	}
	begin, _, _ := strings.Cut(claudecode.ClaudeMDSection(), "\n")
	data, err := os.ReadFile(filepath.Join(root, "CLAUDE.md"))
	return err == nil && strings.Contains(string(data), begin)
}
