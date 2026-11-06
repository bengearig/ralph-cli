// Package claudecode drives Claude Code (the `claude` CLI) as a ralph
// harness and wires ralph into Claude Code projects.
package claudecode

import (
	"context"
	"fmt"

	"github.com/bengearig/ralph/internal/config"
	"github.com/bengearig/ralph/internal/errs"
	"github.com/bengearig/ralph/internal/harness"
)

// Name is the harness's config identifier.
const Name = config.HarnessClaudeCode

func init() {
	harness.Register(Name, func(cfg *config.Config) (harness.Harness, error) {
		return New(cfg.Harness.ClaudeCode), nil
	})
}

// Harness runs `claude -p` headlessly.
type Harness struct {
	cfg config.ClaudeCodeConfig
}

var (
	_ harness.Harness   = (*Harness)(nil)
	_ harness.Installer = (*Harness)(nil)
)

// New returns a Claude Code harness.
func New(cfg config.ClaudeCodeConfig) *Harness { return &Harness{cfg: cfg} }

func (h *Harness) Name() string { return Name }

// Command builds the headless invocation. The MCP server comes from the
// project's .mcp.json, which `ralph init` maintains.
func (h *Harness) Command(req harness.Request) []string {
	argv := []string{h.cfg.Binary, "--print", "--output-format", "stream-json", "--verbose"}
	model := req.Model
	if model == "" {
		model = h.cfg.Model
	}
	if model != "" {
		argv = append(argv, "--model", model)
	}
	if h.cfg.PermissionMode != "" {
		argv = append(argv, "--permission-mode", h.cfg.PermissionMode)
	}
	return append(argv, h.cfg.ExtraArgs...)
}

// Run is not implemented yet; the loop's dry run uses Command instead.
func (h *Harness) Run(context.Context, harness.Request) (harness.Result, error) {
	return harness.Result{}, fmt.Errorf("claude_code harness execution: %w", errs.ErrNotImplemented)
}
