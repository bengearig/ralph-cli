// Package config defines ralph's configuration and loads it from layered
// sources: built-in defaults, the global file, the project file, an optional
// extra file, RALPH_* environment variables, and command-line flags.
package config

import (
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"slices"
	"strings"
	"text/template"
	"time"
)

// Identifiers referenced from config values.
const (
	HarnessClaudeCode = "claude_code"

	EmbeddingsOllama = "ollama"
	EmbeddingsNone   = "none"

	PRBitbucketCloud = "bitbucket_cloud"
)

var (
	knownHarnesses   = []string{HarnessClaudeCode}
	knownEmbeddings  = []string{EmbeddingsOllama, EmbeddingsNone}
	knownPRProviders = []string{PRBitbucketCloud}
	knownLogLevels   = []string{"debug", "info", "warn", "error"}
	knownLogFormats  = []string{"text", "json"}
)

// Step is a phase of the RALPH method that can run on its own harness.
type Step string

const (
	StepReceive    Step = "receive"
	StepArticulate Step = "articulate"
	StepLoop       Step = "loop"
	StepPR         Step = "pr"
)

// Steps returns every configurable step.
func Steps() []Step { return []Step{StepReceive, StepArticulate, StepLoop, StepPR} }

type Config struct {
	Project    ProjectConfig    `koanf:"project"`
	Storage    StorageConfig    `koanf:"storage"`
	Log        LogConfig        `koanf:"log"`
	Embeddings EmbeddingsConfig `koanf:"embeddings"`
	Search     SearchConfig     `koanf:"search"`
	Harness    HarnessConfig    `koanf:"harness"`
	Steps      StepsConfig      `koanf:"steps"`
	Receive    ReceiveConfig    `koanf:"receive"`
	Loop       LoopConfig       `koanf:"loop"`
	PR         PRConfig         `koanf:"pr"`
}

type ProjectConfig struct {
	Name string `koanf:"name"`
}

type StorageConfig struct {
	Path string `koanf:"path"`
}

type LogConfig struct {
	Level  string `koanf:"level"`
	Format string `koanf:"format"`
}

type EmbeddingsConfig struct {
	Provider string        `koanf:"provider"`
	Timeout  time.Duration `koanf:"timeout"`
	Chunk    ChunkConfig   `koanf:"chunk"`
	Ollama   OllamaConfig  `koanf:"ollama"`
}

type ChunkConfig struct {
	MaxChars     int `koanf:"max_chars"`
	OverlapChars int `koanf:"overlap_chars"`
}

type OllamaConfig struct {
	BaseURL        string `koanf:"base_url"`
	Model          string `koanf:"model"`
	QueryPrefix    string `koanf:"query_prefix"`
	DocumentPrefix string `koanf:"document_prefix"`
}

type SearchConfig struct {
	Limit int `koanf:"limit"`
}

type HarnessConfig struct {
	Default    string           `koanf:"default"`
	ClaudeCode ClaudeCodeConfig `koanf:"claude_code"`
}

type ClaudeCodeConfig struct {
	Binary         string   `koanf:"binary"`
	Model          string   `koanf:"model"`
	PermissionMode string   `koanf:"permission_mode"`
	MaxBudgetUSD   float64  `koanf:"max_budget_usd"`
	ExtraArgs      []string `koanf:"extra_args"`
}

type StepsConfig struct {
	Receive    StepConfig `koanf:"receive"`
	Articulate StepConfig `koanf:"articulate"`
	Loop       StepConfig `koanf:"loop"`
	PR         StepConfig `koanf:"pr"`
}

type StepConfig struct {
	Harness string `koanf:"harness"`
	Model   string `koanf:"model"`
}

type ReceiveConfig struct {
	ConfirmOutline bool `koanf:"confirm_outline"`
}

type LoopConfig struct {
	MaxIterations  int    `koanf:"max_iterations"`
	StallThreshold int    `koanf:"stall_threshold"`
	BranchTemplate string `koanf:"branch_template"`
	CommitPerTask  bool   `koanf:"commit_per_task"`
	CommitTemplate string `koanf:"commit_template"`
	CommitTrailer  bool   `koanf:"commit_trailer"`
	PromptTemplate string `koanf:"prompt_template"`
}

type PRConfig struct {
	Provider       string               `koanf:"provider"`
	BaseBranch     string               `koanf:"base_branch"`
	Draft          bool                 `koanf:"draft"`
	BitbucketCloud BitbucketCloudConfig `koanf:"bitbucket_cloud"`
}

type BitbucketCloudConfig struct {
	Workspace string `koanf:"workspace"`
	RepoSlug  string `koanf:"repo_slug"`
	TokenEnv  string `koanf:"token_env"`
}

// Step returns the raw override block for a step.
func (s StepsConfig) Step(step Step) (StepConfig, error) {
	switch step {
	case StepReceive:
		return s.Receive, nil
	case StepArticulate:
		return s.Articulate, nil
	case StepLoop:
		return s.Loop, nil
	case StepPR:
		return s.PR, nil
	}
	return StepConfig{}, fmt.Errorf("unknown step %q", step)
}

// StepResolution is the effective harness and model for a step.
type StepResolution struct {
	Step    Step   `json:"step"`
	Harness string `json:"harness"`
	Model   string `json:"model,omitempty"`
}

// ResolveStep applies the step's overrides on top of the harness defaults.
func (c *Config) ResolveStep(step Step) (StepResolution, error) {
	sc, err := c.Steps.Step(step)
	if err != nil {
		return StepResolution{}, err
	}
	r := StepResolution{Step: step, Harness: sc.Harness, Model: sc.Model}
	if r.Harness == "" {
		r.Harness = c.Harness.Default
	}
	if r.Model == "" {
		model, err := c.harnessModel(r.Harness)
		if err != nil {
			return StepResolution{}, err
		}
		r.Model = model
	}
	return r, nil
}

func (c *Config) harnessModel(name string) (string, error) {
	switch name {
	case HarnessClaudeCode:
		return c.Harness.ClaudeCode.Model, nil
	}
	return "", fmt.Errorf("unknown harness %q", name)
}

// ProjectName returns the configured name, or the root directory's name.
func (c *Config) ProjectName(root string) string {
	if c.Project.Name != "" {
		return c.Project.Name
	}
	return filepath.Base(root)
}

// StoragePath returns the database path, resolved against root when relative.
func (c *Config) StoragePath(root string) string {
	if filepath.IsAbs(c.Storage.Path) {
		return c.Storage.Path
	}
	return filepath.Join(root, c.Storage.Path)
}

// Validate reports every invalid setting at once.
func (c *Config) Validate() error {
	var errs []error
	bad := func(key, format string, args ...any) {
		errs = append(errs, fmt.Errorf("%s: %s", key, fmt.Sprintf(format, args...)))
	}
	oneOf := func(key, val string, allowed []string) {
		if !slices.Contains(allowed, val) {
			bad(key, "%q is not one of %s", val, strings.Join(allowed, ", "))
		}
	}

	if strings.TrimSpace(c.Storage.Path) == "" {
		bad("storage.path", "must not be empty")
	}
	oneOf("log.level", c.Log.Level, knownLogLevels)
	oneOf("log.format", c.Log.Format, knownLogFormats)

	oneOf("embeddings.provider", c.Embeddings.Provider, knownEmbeddings)
	if c.Embeddings.Timeout <= 0 {
		bad("embeddings.timeout", "must be positive")
	}
	if c.Embeddings.Chunk.MaxChars < 1 {
		bad("embeddings.chunk.max_chars", "must be at least 1")
	}
	if o := c.Embeddings.Chunk.OverlapChars; o < 0 || o >= c.Embeddings.Chunk.MaxChars {
		bad("embeddings.chunk.overlap_chars", "must be at least 0 and less than max_chars")
	}
	if c.Embeddings.Provider == EmbeddingsOllama {
		if u, err := url.Parse(c.Embeddings.Ollama.BaseURL); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			bad("embeddings.ollama.base_url", "%q is not an http(s) URL", c.Embeddings.Ollama.BaseURL)
		}
		if strings.TrimSpace(c.Embeddings.Ollama.Model) == "" {
			bad("embeddings.ollama.model", "must not be empty")
		}
	}

	if c.Search.Limit < 1 {
		bad("search.limit", "must be at least 1")
	}

	oneOf("harness.default", c.Harness.Default, knownHarnesses)
	if strings.TrimSpace(c.Harness.ClaudeCode.Binary) == "" {
		bad("harness.claude_code.binary", "must not be empty")
	}
	if c.Harness.ClaudeCode.MaxBudgetUSD < 0 {
		bad("harness.claude_code.max_budget_usd", "must not be negative")
	}
	for _, step := range Steps() {
		sc, _ := c.Steps.Step(step)
		if sc.Harness != "" {
			oneOf("steps."+string(step)+".harness", sc.Harness, knownHarnesses)
		}
	}

	if c.Loop.MaxIterations < 1 {
		bad("loop.max_iterations", "must be at least 1")
	}
	if c.Loop.StallThreshold < 1 {
		bad("loop.stall_threshold", "must be at least 1")
	}
	if strings.TrimSpace(c.Loop.BranchTemplate) == "" {
		bad("loop.branch_template", "must not be empty")
	} else if _, err := template.New("branch").Option("missingkey=error").Parse(c.Loop.BranchTemplate); err != nil {
		bad("loop.branch_template", "%v", err)
	}

	if strings.TrimSpace(c.Loop.CommitTemplate) == "" {
		bad("loop.commit_template", "must not be empty")
	} else if _, err := template.New("commit").Option("missingkey=error").Parse(c.Loop.CommitTemplate); err != nil {
		bad("loop.commit_template", "%v", err)
	}

	oneOf("pr.provider", c.PR.Provider, knownPRProviders)
	if strings.TrimSpace(c.PR.BaseBranch) == "" {
		bad("pr.base_branch", "must not be empty")
	}
	if c.PR.Provider == PRBitbucketCloud && strings.TrimSpace(c.PR.BitbucketCloud.TokenEnv) == "" {
		bad("pr.bitbucket_cloud.token_env", "must name an environment variable")
	}

	return errors.Join(errs...)
}
