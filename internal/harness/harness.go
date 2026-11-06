// Package harness abstracts the agent harness (such as Claude Code) that
// ralph drives for each step of the workflow. Implementations register
// themselves by config name.
package harness

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"sync"
	"time"

	"github.com/bengearig/ralph/internal/config"
)

// ErrToolsUnavailable means the session could not reach ralph's MCP server,
// so the agent could neither read its task nor report a status.
var ErrToolsUnavailable = errors.New("ralph tools unavailable in the harness session")

// Request is one headless harness invocation.
type Request struct {
	Step    config.Step
	Model   string
	Prompt  string
	WorkDir string
	// Transcript receives the harness's raw output; nil discards it.
	Transcript io.Writer
	// OnEvent receives progress as it happens; nil ignores it.
	OnEvent func(Event)
}

// EventKind classifies progress events.
type EventKind int

const (
	// EventText is something the agent said.
	EventText EventKind = iota
	// EventTool is a tool the agent called.
	EventTool
	// EventInfo is a note from the harness adapter itself.
	EventInfo
)

// Event is one line of progress.
type Event struct {
	Kind EventKind
	Text string
}

// Result is the outcome of a session that ran.
type Result struct {
	ExitCode  int
	SessionID string
	// Completed reports whether the harness emitted a final result.
	Completed bool
	IsError   bool
	// Subtype is the harness's result classification, such as "success" or
	// "error_max_budget_usd".
	Subtype  string
	Summary  string
	CostUSD  float64
	Turns    int
	Duration time.Duration
}

// Harness runs an agent non-interactively.
type Harness interface {
	Name() string
	// Command returns the argv that Run would execute. The prompt is
	// delivered on stdin, so it is not part of the argv.
	Command(req Request) []string
	// Run executes one session. It returns an error only when the session
	// could not do any work: it failed to start, the context was cancelled,
	// or ErrToolsUnavailable. Failures inside a session are in the Result.
	Run(ctx context.Context, req Request) (Result, error)
}

// Change records what an Installer did to one file.
type Change struct {
	Path   string `json:"path"`
	Action string `json:"action"`
	Note   string `json:"note,omitempty"`
}

// Change actions.
const (
	Created   = "created"
	Updated   = "updated"
	Unchanged = "unchanged"
	Skipped   = "skipped"
)

// InstallOptions controls project wiring.
type InstallOptions struct {
	// RalphCommand is how the harness should launch ralph, e.g. "ralph" or an
	// absolute path.
	RalphCommand string
	// Force overwrites files the user may have customized.
	Force bool
}

// Installer wires a harness into a project so interactive sessions can use
// ralph (MCP registration, commands, instructions).
type Installer interface {
	Install(root string, opts InstallOptions) ([]Change, error)
}

// Factory builds a harness from configuration.
type Factory func(cfg *config.Config) (Harness, error)

var (
	mu        sync.RWMutex
	factories = map[string]Factory{}
)

// Register makes a harness available under name. It panics on duplicates,
// which indicate a programming error.
func Register(name string, f Factory) {
	mu.Lock()
	defer mu.Unlock()
	if _, dup := factories[name]; dup {
		panic("harness: duplicate registration of " + name)
	}
	factories[name] = f
}

// New builds the named harness.
func New(name string, cfg *config.Config) (Harness, error) {
	mu.RLock()
	f, ok := factories[name]
	mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("unknown harness %q (registered: %v)", name, Names())
	}
	return f(cfg)
}

// ForStep builds the harness configured for a step.
func ForStep(cfg *config.Config, step config.Step) (Harness, config.StepResolution, error) {
	res, err := cfg.ResolveStep(step)
	if err != nil {
		return nil, res, err
	}
	h, err := New(res.Harness, cfg)
	return h, res, err
}

// Names lists registered harnesses.
func Names() []string {
	mu.RLock()
	defer mu.RUnlock()
	names := make([]string, 0, len(factories))
	for n := range factories {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}
