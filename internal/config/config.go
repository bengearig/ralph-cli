// Package config defines ralph's configuration and loads it from layered
// sources: built-in defaults, the global file, the project file, an optional
// extra file, RALPH_* environment variables, and command-line flags.
package config

import (
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"text/template"
	"time"

	"github.com/bengearig/ralph/internal/domain"
)

// Identifiers referenced from config values.
const (
	HarnessClaudeCode = "claude_code"

	EmbeddingsOllama = "ollama"
	EmbeddingsNone   = "none"

	PRBitbucketCloud = "bitbucket_cloud"
	PRGitHub         = "github"

	StaleBaseRefuse = "refuse"
	StaleBaseSync   = "sync"

	BitbucketAuthBasic  = "basic"
	BitbucketAuthBearer = "bearer"

	TaskManagerNone = "none"
	TaskManagerJira = "jira"
)

var jiraDurationRE = regexp.MustCompile(`^[0-9]+[wdhm]( [0-9]+[wdhm])*$`)

var (
	knownHarnesses      = []string{HarnessClaudeCode}
	knownEmbeddings     = []string{EmbeddingsOllama, EmbeddingsNone}
	knownPRProviders    = []string{PRBitbucketCloud, PRGitHub}
	knownStaleBases     = []string{StaleBaseRefuse, StaleBaseSync}
	knownBitbucketAuths = []string{BitbucketAuthBasic, BitbucketAuthBearer}
	knownTaskManagers   = []string{TaskManagerNone, TaskManagerJira}
	knownLogLevels      = []string{"debug", "info", "warn", "error"}
	knownLogFormats     = []string{"text", "json"}
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
	Import     ImportConfig     `koanf:"import"`
	Loop       LoopConfig       `koanf:"loop"`
	PR         PRConfig         `koanf:"pr"`

	TaskManager TaskManagerConfig `koanf:"task_manager"`
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

type ImportConfig struct {
	MaxFileBytes int `koanf:"max_file_bytes"`
}

type LoopConfig struct {
	MaxIterations     int    `koanf:"max_iterations"`
	StallThreshold    int    `koanf:"stall_threshold"`
	BranchTemplate    string `koanf:"branch_template"`
	CommitPerTask     bool   `koanf:"commit_per_task"`
	CommitTemplate    string `koanf:"commit_template"`
	CommitTrailer     bool   `koanf:"commit_trailer"`
	CommitCardTrailer bool   `koanf:"commit_card_trailer"`
	PromptTemplate    string `koanf:"prompt_template"`
}

type PRConfig struct {
	Provider       string               `koanf:"provider"`
	BaseBranch     string               `koanf:"base_branch"`
	Remote         string               `koanf:"remote"`
	StaleBase      string               `koanf:"stale_base"`
	Draft          bool                 `koanf:"draft"`
	Timeout        time.Duration        `koanf:"timeout"`
	BitbucketCloud BitbucketCloudConfig `koanf:"bitbucket_cloud"`
	GitHub         GitHubConfig         `koanf:"github"`
}

type GitHubConfig struct {
	Owner    string `koanf:"owner"`
	Repo     string `koanf:"repo"`
	TokenEnv string `koanf:"token_env"`
}

type BitbucketCloudConfig struct {
	Workspace string `koanf:"workspace"`
	RepoSlug  string `koanf:"repo_slug"`
	Auth      string `koanf:"auth"`
	EmailEnv  string `koanf:"email_env"`
	TokenEnv  string `koanf:"token_env"`
}

type TaskManagerConfig struct {
	Provider string        `koanf:"provider"`
	Timeout  time.Duration `koanf:"timeout"`
	Jira     JiraConfig    `koanf:"jira"`
}

type JiraConfig struct {
	BaseURL          string              `koanf:"base_url"`
	ProjectKey       string              `koanf:"project_key"`
	EmailEnv         string              `koanf:"email_env"`
	TokenEnv         string              `koanf:"token_env"`
	IssueTypes       JiraIssueTypes      `koanf:"issue_types"`
	CanceledStatus   string              `koanf:"canceled_status"`
	StatusMap        map[string][]string `koanf:"status_map"`
	Labels           []string            `koanf:"labels"`
	OriginalEstimate JiraEstimates       `koanf:"original_estimate"`
}

// JiraEstimates are the original estimates given to cards that have none before they move; empty means none.
type JiraEstimates struct {
	Initiative string `koanf:"initiative"`
	Milestone  string `koanf:"milestone"`
	Task       string `koanf:"task"`
}

type JiraIssueTypes struct {
	Initiative string `koanf:"initiative"`
	Milestone  string `koanf:"milestone"`
	Task       string `koanf:"task"`
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

// AllowTaskManager makes name a valid task_manager.provider. Tests use it to
// select a fake provider; it is not safe to call concurrently with Validate.
func AllowTaskManager(name string) {
	if !slices.Contains(knownTaskManagers, name) {
		knownTaskManagers = append(knownTaskManagers, name)
	}
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

	if c.Import.MaxFileBytes < 1 {
		bad("import.max_file_bytes", "must be at least 1")
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
	if strings.TrimSpace(c.PR.Remote) == "" {
		bad("pr.remote", "must not be empty")
	}
	oneOf("pr.stale_base", c.PR.StaleBase, knownStaleBases)
	if c.PR.Timeout <= 0 {
		bad("pr.timeout", "must be positive")
	}
	if c.PR.Provider == PRBitbucketCloud {
		b := c.PR.BitbucketCloud
		oneOf("pr.bitbucket_cloud.auth", b.Auth, knownBitbucketAuths)
		if b.Auth == BitbucketAuthBasic && strings.TrimSpace(b.EmailEnv) == "" {
			bad("pr.bitbucket_cloud.email_env", "must name an environment variable")
		}
		if strings.TrimSpace(b.TokenEnv) == "" {
			bad("pr.bitbucket_cloud.token_env", "must name an environment variable")
		}
	}
	if c.PR.Provider == PRGitHub {
		g := c.PR.GitHub
		if strings.TrimSpace(g.Owner) == "" {
			bad("pr.github.owner", "must not be empty")
		}
		if strings.TrimSpace(g.Repo) == "" {
			bad("pr.github.repo", "must not be empty")
		}
		if strings.TrimSpace(g.TokenEnv) == "" {
			bad("pr.github.token_env", "must name an environment variable")
		}
	}

	oneOf("task_manager.provider", c.TaskManager.Provider, knownTaskManagers)
	if c.TaskManager.Timeout <= 0 {
		bad("task_manager.timeout", "must be positive")
	}
	if c.TaskManager.Provider == TaskManagerJira {
		j := c.TaskManager.Jira
		if u, err := url.Parse(j.BaseURL); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			bad("task_manager.jira.base_url", "%q is not an http(s) URL", j.BaseURL)
		}
		for _, f := range []struct{ key, val string }{
			{"project_key", j.ProjectKey},
			{"email_env", j.EmailEnv},
			{"token_env", j.TokenEnv},
			{"issue_types.initiative", j.IssueTypes.Initiative},
			{"issue_types.milestone", j.IssueTypes.Milestone},
			{"issue_types.task", j.IssueTypes.Task},
		} {
			if strings.TrimSpace(f.val) == "" {
				bad("task_manager.jira."+f.key, "must not be empty")
			}
		}
		for _, f := range []struct{ key, val string }{
			{"initiative", j.OriginalEstimate.Initiative},
			{"milestone", j.OriginalEstimate.Milestone},
			{"task", j.OriginalEstimate.Task},
		} {
			if f.val != "" && !jiraDurationRE.MatchString(f.val) {
				bad("task_manager.jira.original_estimate."+f.key, "%q is not a Jira duration such as 5m, 2h, or 1d 4h", f.val)
			}
		}
		for _, l := range j.Labels {
			if strings.TrimSpace(l) == "" || strings.ContainsAny(l, " \t\r\n") {
				bad("task_manager.jira.labels", "%q must not be empty or contain whitespace", l)
			}
		}
		keys := make([]string, 0, len(j.StatusMap))
		for k := range j.StatusMap {
			keys = append(keys, k)
		}
		slices.Sort(keys)
		for _, k := range keys {
			switch {
			case k == string(domain.StatusCancelled):
				bad("task_manager.jira.status_map", "key %q is not allowed; use task_manager.jira.canceled_status", k)
			case !knownMappedStatus(k):
				bad("task_manager.jira.status_map", "unknown status %q (expected one of %s)", k, strings.Join(mappableStatuses(), ", "))
			case len(j.StatusMap[k]) > 1 && slices.ContainsFunc(j.StatusMap[k], func(s string) bool { return strings.TrimSpace(s) == "" }):
				bad("task_manager.jira.status_map", "%s has a blank status in its path", k)
			}
		}
	}

	return errors.Join(errs...)
}

// mappableStatuses lists the task and milestone statuses, sorted and unique.
func mappableStatuses() []string {
	var out []string
	for _, s := range domain.Statuses() {
		out = append(out, string(s))
	}
	out = append(out, domain.MilestoneStatuses()...)
	slices.Sort(out)
	return slices.Compact(out)
}

func knownMappedStatus(s string) bool { return slices.Contains(mappableStatuses(), s) }
