package config

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/knadh/koanf/parsers/yaml"
	"github.com/spf13/pflag"
)

func writeFile(t *testing.T, dir, name, body string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestDefaults(t *testing.T) {
	cfg, err := Defaults()
	if err != nil {
		t.Fatalf("Defaults: %v", err)
	}
	if cfg.Storage.Path != ".ralph/ralph.db" {
		t.Errorf("storage.path = %q", cfg.Storage.Path)
	}
	if cfg.Embeddings.Timeout != 30*time.Second {
		t.Errorf("embeddings.timeout = %v", cfg.Embeddings.Timeout)
	}
	if cfg.Harness.Default != HarnessClaudeCode || cfg.PR.Provider != PRBitbucketCloud {
		t.Errorf("unexpected harness/provider: %q %q", cfg.Harness.Default, cfg.PR.Provider)
	}
	if cfg.Embeddings.Ollama.QueryPrefix != "auto" || cfg.Embeddings.Ollama.DocumentPrefix != "auto" {
		t.Errorf("prefix defaults = %q, %q", cfg.Embeddings.Ollama.QueryPrefix, cfg.Embeddings.Ollama.DocumentPrefix)
	}
	if cfg.Harness.ClaudeCode.PermissionMode != "auto" || cfg.Harness.ClaudeCode.MaxBudgetUSD != 0 {
		t.Errorf("claude_code defaults = %+v", cfg.Harness.ClaudeCode)
	}
	if cfg.Loop.CommitTemplate != "{{.Task.ID}}: {{.Task.Title}}" {
		t.Errorf("commit_template = %q", cfg.Loop.CommitTemplate)
	}
	if cfg.Loop.StallThreshold != 3 || !cfg.Loop.CommitPerTask || !cfg.Loop.CommitTrailer || cfg.Loop.CommitCardTrailer || !cfg.PR.Draft {
		t.Errorf("unexpected loop/pr defaults: %+v %+v", cfg.Loop, cfg.PR)
	}
	if cfg.Receive.ConfirmOutline {
		t.Error("receive.confirm_outline should default to false")
	}
	tm := cfg.TaskManager
	if tm.Provider != TaskManagerNone || tm.Timeout != 10*time.Second {
		t.Errorf("task_manager defaults = %+v", tm)
	}
	if j := tm.Jira; j.EmailEnv != "RALPH_JIRA_EMAIL" || j.TokenEnv != "RALPH_JIRA_TOKEN" || j.CanceledStatus != "" || len(j.Labels) != 0 || j.OriginalEstimate != (JiraEstimates{}) || j.AssignOnStart ||
		j.IssueTypes != (JiraIssueTypes{Initiative: "Epic", Milestone: "Task", Task: "Sub-task"}) {
		t.Errorf("jira defaults = %+v", j)
	}
	if b := cfg.PR.BitbucketCloud; cfg.PR.Timeout != 30*time.Second || b.Auth != "basic" || b.EmailEnv != "RALPH_BITBUCKET_EMAIL" || b.TokenEnv != "RALPH_BITBUCKET_TOKEN" {
		t.Errorf("pr defaults = %+v", cfg.PR)
	}
	if cfg.Import.MaxFileBytes != 1048576 {
		t.Errorf("import.max_file_bytes = %d", cfg.Import.MaxFileBytes)
	}
	if cfg.UI.Progress != UIProgressAuto {
		t.Errorf("ui.progress = %q", cfg.UI.Progress)
	}
	if cfg.Init.Update != InitUpdateAuto || len(cfg.Init.Ignore) != 0 {
		t.Errorf("init defaults = %+v", cfg.Init)
	}
	if err := cfg.Validate(); err != nil {
		t.Errorf("default config should validate: %v", err)
	}
}

func TestValidateTaskManager(t *testing.T) {
	complete := func(c *Config) {
		c.TaskManager.Provider = TaskManagerJira
		c.TaskManager.Jira.BaseURL = "https://example.atlassian.net"
		c.TaskManager.Jira.ProjectKey = "RAL"
	}
	cases := []struct {
		name   string
		mutate func(*Config)
		want   []string
	}{
		{"bad provider", func(c *Config) { c.TaskManager.Provider = "bogus" }, []string{"task_manager.provider:"}},
		{"zero timeout", func(c *Config) { c.TaskManager.Timeout = 0 }, []string{"task_manager.timeout:"}},
		{"jira defaults", func(c *Config) { c.TaskManager.Provider = TaskManagerJira },
			[]string{"task_manager.jira.base_url:", "task_manager.jira.project_key:"}},
		{"bad url and blank type", func(c *Config) {
			complete(c)
			c.TaskManager.Jira.BaseURL = "example.atlassian.net"
			c.TaskManager.Jira.IssueTypes.Task = " "
		}, []string{"task_manager.jira.base_url:", "task_manager.jira.issue_types.task:"}},
		{"blank env names", func(c *Config) {
			complete(c)
			c.TaskManager.Jira.EmailEnv = ""
			c.TaskManager.Jira.TokenEnv = ""
			c.TaskManager.Jira.IssueTypes.Initiative = ""
			c.TaskManager.Jira.IssueTypes.Milestone = ""
		}, []string{"email_env:", "token_env:", "issue_types.initiative:", "issue_types.milestone:"}},
		{"complete jira", complete, nil},
		{"status_map extra entry", func(c *Config) {
			complete(c)
			c.TaskManager.Jira.StatusMap["BLOCKED"] = []string{"Blocked"}
		}, nil},
		{"status_map blank value", func(c *Config) {
			complete(c)
			c.TaskManager.Jira.StatusMap["DONE"] = []string{""}
		}, nil},
		{"status_map path", func(c *Config) {
			complete(c)
			c.TaskManager.Jira.StatusMap["DONE"] = []string{"To Do", "In Progress", "Done"}
		}, nil},
		{"original_estimate", func(c *Config) {
			complete(c)
			c.TaskManager.Jira.OriginalEstimate = JiraEstimates{Task: "5m", Milestone: "1h 30m", Initiative: "2d"}
		}, nil},
		{"assign_on_start true", func(c *Config) {
			complete(c)
			c.TaskManager.Jira.AssignOnStart = true
		}, nil},
		{"assign_on_start false", func(c *Config) {
			complete(c)
			c.TaskManager.Jira.AssignOnStart = false
		}, nil},
		{"original_estimate invalid", func(c *Config) {
			complete(c)
			c.TaskManager.Jira.OriginalEstimate = JiraEstimates{Task: "five minutes", Milestone: "1.5h"}
		}, []string{"task_manager.jira.original_estimate.task:", "task_manager.jira.original_estimate.milestone:"}},
		{"status_map blank step in path", func(c *Config) {
			complete(c)
			c.TaskManager.Jira.StatusMap["DONE"] = []string{"To Do", " ", "Done"}
		}, []string{"task_manager.jira.status_map:", "DONE has a blank status"}},
		{"status_map milestone status", func(c *Config) {
			complete(c)
			c.TaskManager.Jira.StatusMap["ARTICULATING"] = []string{"Backlog"}
		}, nil},
		{"status_map unknown key", func(c *Config) {
			complete(c)
			c.TaskManager.Jira.StatusMap["WAITING"] = []string{"Waiting"}
		}, []string{"task_manager.jira.status_map:", "WAITING"}},
		{"status_map cancelled key", func(c *Config) {
			complete(c)
			c.TaskManager.Jira.StatusMap["CANCELLED"] = []string{"Cancelled"}
		}, []string{"task_manager.jira.status_map:", "canceled_status"}},
		{"labels", func(c *Config) {
			complete(c)
			c.TaskManager.Jira.Labels = []string{"workflow-2.0", "team_a"}
		}, nil},
		{"label with whitespace", func(c *Config) {
			complete(c)
			c.TaskManager.Jira.Labels = []string{"two words"}
		}, []string{"task_manager.jira.labels:", "two words"}},
		{"blank label", func(c *Config) {
			complete(c)
			c.TaskManager.Jira.Labels = []string{" "}
		}, []string{"task_manager.jira.labels:"}},
		{"none ignores invalid status_map", func(c *Config) {
			c.TaskManager.Jira.StatusMap = map[string][]string{"WAITING": {"x"}, "CANCELLED": {"y"}}
		}, nil},
		{"none ignores jira block", func(c *Config) {
			c.TaskManager.Jira = JiraConfig{BaseURL: "garbage"}
		}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg, _ := Defaults()
			tc.mutate(cfg)
			err := cfg.Validate()
			if tc.want == nil {
				if err != nil {
					t.Errorf("unexpected error: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("expected error")
			}
			for _, key := range tc.want {
				if !strings.Contains(err.Error(), key) {
					t.Errorf("missing %s in:\n%v", key, err)
				}
			}
		})
	}
}

func TestTaskManagerLayers(t *testing.T) {
	project := writeFile(t, t.TempDir(), "p.yaml", "task_manager:\n  provider: jira\n  jira:\n    base_url: https://example.atlassian.net\n    project_key: RAL\n")
	l, err := Load(Sources{ProjectFile: project, Environ: []string{"RALPH_TASK_MANAGER__JIRA__ISSUE_TYPES__TASK=Story"}})
	if err != nil {
		t.Fatal(err)
	}
	j := l.Config.TaskManager.Jira
	if l.Config.TaskManager.Provider != TaskManagerJira || j.ProjectKey != "RAL" || j.IssueTypes.Task != "Story" || j.IssueTypes.Milestone != "Task" {
		t.Errorf("task_manager = %+v", l.Config.TaskManager)
	}
	if err := l.Config.Validate(); err != nil {
		t.Errorf("validate: %v", err)
	}

	typo := writeFile(t, t.TempDir(), "typo.yaml", "task_manager:\n  jira:\n    issue_type: {}\n")
	if _, err := Load(Sources{ProjectFile: typo}); err == nil || !strings.Contains(err.Error(), "issue_type") {
		t.Errorf("err = %v, want an unknown-key error naming issue_type", err)
	}
}

func TestJiraAssignOnStartLayers(t *testing.T) {
	project := writeFile(t, t.TempDir(), "p.yaml", "task_manager:\n  jira:\n    assign_on_start: true\n")
	l, err := Load(Sources{ProjectFile: project})
	if err != nil {
		t.Fatal(err)
	}
	if !l.Config.TaskManager.Jira.AssignOnStart {
		t.Error("assign_on_start in a file should load as true")
	}
	l, err = Load(Sources{ProjectFile: project, Environ: []string{"RALPH_TASK_MANAGER__JIRA__ASSIGN_ON_START=false"}})
	if err != nil {
		t.Fatal(err)
	}
	if l.Config.TaskManager.Jira.AssignOnStart {
		t.Error("env false should override the file's true")
	}

	off := writeFile(t, t.TempDir(), "off.yaml", "task_manager:\n  jira:\n    assign_on_start: false\n")
	l, err = Load(Sources{ProjectFile: off, Environ: []string{"RALPH_TASK_MANAGER__JIRA__ASSIGN_ON_START=true"}})
	if err != nil {
		t.Fatal(err)
	}
	if !l.Config.TaskManager.Jira.AssignOnStart {
		t.Error("RALPH_TASK_MANAGER__JIRA__ASSIGN_ON_START=true should override the file")
	}
}

func TestLayerPrecedence(t *testing.T) {
	dir := t.TempDir()
	global := writeFile(t, dir, "global.yaml", "log:\n  level: debug\nsearch:\n  limit: 3\nloop:\n  max_iterations: 7\n")
	project := writeFile(t, dir, "project.yaml", "search:\n  limit: 4\nloop:\n  stall_threshold: 9\n")
	extra := writeFile(t, dir, "extra.yaml", "search:\n  limit: 5\n")

	fs := pflag.NewFlagSet("t", pflag.ContinueOnError)
	fs.String("log-level", "", "")
	fs.String("unrelated", "x", "")
	if err := fs.Parse([]string{"--log-level=error", "--unrelated=y"}); err != nil {
		t.Fatal(err)
	}

	l, err := Load(Sources{
		GlobalFile:  global,
		ProjectFile: project,
		ExtraFile:   extra,
		Environ: []string{
			"RALPH_LOOP__STALL_THRESHOLD=11",
			"RALPH_EMBEDDINGS__OLLAMA__MODEL=mxbai-embed-large",
			"RALPH_HARNESS__CLAUDE_CODE__EXTRA_ARGS=--verbose,--debug",
			"RALPH_PR__DRAFT=false",
			"RALPH_LOOP__COMMIT_TRAILER=false",
			"RALPH_LOOP__COMMIT_CARD_TRAILER=true",
			"RALPH_BITBUCKET_TOKEN=secret",
			"RALPH_BITBUCKET_EMAIL=me@example.com",
			"RALPH_PR__BITBUCKET_CLOUD__AUTH=bearer",
			"RALPH_PR__TIMEOUT=5s",
			"OTHER=1",
		},
		Flags: fs,
	})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	c := l.Config
	if c.Log.Level != "error" {
		t.Errorf("flag should win: log.level = %q", c.Log.Level)
	}
	if c.Loop.MaxIterations != 7 {
		t.Errorf("global should apply: loop.max_iterations = %d", c.Loop.MaxIterations)
	}
	if c.Search.Limit != 5 {
		t.Errorf("extra should beat project: search.limit = %d", c.Search.Limit)
	}
	if c.Loop.StallThreshold != 11 {
		t.Errorf("env should beat project: loop.stall_threshold = %d", c.Loop.StallThreshold)
	}
	if c.Embeddings.Ollama.Model != "mxbai-embed-large" {
		t.Errorf("nested env: model = %q", c.Embeddings.Ollama.Model)
	}
	if got := strings.Join(c.Harness.ClaudeCode.ExtraArgs, " "); got != "--verbose --debug" {
		t.Errorf("env slice: extra_args = %q", got)
	}
	if c.PR.BitbucketCloud.Auth != "bearer" || c.PR.Timeout != 5*time.Second {
		t.Errorf("env overrides: pr = %+v", c.PR)
	}
	if c.PR.BitbucketCloud.EmailEnv != "RALPH_BITBUCKET_EMAIL" || c.PR.BitbucketCloud.TokenEnv != "RALPH_BITBUCKET_TOKEN" {
		t.Errorf("named env vars must not enter the config: %+v", c.PR.BitbucketCloud)
	}
	if c.PR.Draft {
		t.Error("env bool: pr.draft should be false")
	}
	if c.Loop.CommitTrailer {
		t.Error("env bool: loop.commit_trailer should be false")
	}
	if !c.Loop.CommitCardTrailer {
		t.Error("env bool: loop.commit_card_trailer should be true")
	}

	out, err := l.YAML()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), "secret") || strings.Contains(string(out), "bitbucket_token") {
		t.Errorf("unrelated RALPH_ variable leaked into config:\n%s", out)
	}

	present := map[string]bool{}
	for _, layer := range l.Layers {
		present[layer.Name] = layer.Present
	}
	for _, name := range []string{"defaults", "global", "project", "extra", "environment", "flags"} {
		if !present[name] {
			t.Errorf("layer %s should be present: %+v", name, l.Layers)
		}
	}
}

func TestUnchangedFlagDoesNotOverride(t *testing.T) {
	dir := t.TempDir()
	project := writeFile(t, dir, "p.yaml", "log:\n  level: warn\n")
	fs := pflag.NewFlagSet("t", pflag.ContinueOnError)
	fs.String("log-level", "", "")
	l, err := Load(Sources{ProjectFile: project, Flags: fs})
	if err != nil {
		t.Fatal(err)
	}
	if l.Config.Log.Level != "warn" {
		t.Errorf("log.level = %q", l.Config.Log.Level)
	}
}

func TestMissingOptionalFilesAreSkipped(t *testing.T) {
	dir := t.TempDir()
	l, err := Load(Sources{GlobalFile: filepath.Join(dir, "nope.yaml"), ProjectFile: filepath.Join(dir, "nope2.yaml")})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	for _, layer := range l.Layers[1:] {
		if layer.Present {
			t.Errorf("layer %s should be absent", layer.Name)
		}
	}
}

func TestLoadErrors(t *testing.T) {
	dir := t.TempDir()
	cases := map[string]Sources{
		"missing extra":  {ExtraFile: filepath.Join(dir, "missing.yaml")},
		"malformed yaml": {ProjectFile: writeFile(t, dir, "bad.yaml", "log: [unterminated\n")},
		"unknown key":    {ProjectFile: writeFile(t, dir, "typo.yaml", "loop:\n  max_iteration: 3\n")},
		"bad duration":   {ProjectFile: writeFile(t, dir, "dur.yaml", "embeddings:\n  timeout: soon\n")},
		"directory":      {ProjectFile: dir},
	}
	for name, src := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := Load(src); err == nil {
				t.Error("expected error")
			}
		})
	}
}

func TestValidateReportsAllProblems(t *testing.T) {
	cfg, err := Defaults()
	if err != nil {
		t.Fatal(err)
	}
	cfg.Log.Level = "loud"
	cfg.Embeddings.Provider = "openai"
	cfg.Embeddings.Timeout = 0
	cfg.Embeddings.Chunk.OverlapChars = cfg.Embeddings.Chunk.MaxChars
	cfg.Search.Limit = 0
	cfg.Import.MaxFileBytes = 0
	cfg.Steps.Loop.Harness = "codex"
	cfg.Loop.StallThreshold = 0
	cfg.Loop.BranchTemplate = "ralph/{{.Milestone"
	cfg.PR.Provider = "gitlab"
	cfg.PR.BaseBranch = " "
	cfg.PR.Remote = " "
	cfg.PR.StaleBase = "rebase"
	cfg.Harness.ClaudeCode.MaxBudgetUSD = -1
	cfg.Loop.CommitTemplate = "{{.Task"
	cfg.Init.Update = "sometimes"
	cfg.Init.Ignore = []string{"../x"}
	cfg.UI.Progress = "always"

	err = cfg.Validate()
	if err == nil {
		t.Fatal("expected validation error")
	}
	for _, key := range []string{
		"log.level", "embeddings.provider", "embeddings.timeout", "embeddings.chunk.overlap_chars",
		"search.limit", "import.max_file_bytes", "steps.loop.harness", "loop.stall_threshold", "loop.branch_template",
		"pr.provider", "pr.base_branch", "pr.remote", "pr.stale_base", "harness.claude_code.max_budget_usd", "loop.commit_template",
		"init.update", "init.ignore", "ui.progress",
	} {
		if !strings.Contains(err.Error(), key+":") {
			t.Errorf("missing problem for %s in:\n%v", key, err)
		}
	}
}

func TestBlankCommitTemplate(t *testing.T) {
	cfg, _ := Defaults()
	cfg.Loop.CommitTemplate = "  "
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "loop.commit_template: must not be empty") {
		t.Errorf("err = %v", err)
	}
}

func TestBudgetFromEnv(t *testing.T) {
	l, err := Load(Sources{Environ: []string{"RALPH_HARNESS__CLAUDE_CODE__MAX_BUDGET_USD=1.5"}})
	if err != nil {
		t.Fatal(err)
	}
	if l.Config.Harness.ClaudeCode.MaxBudgetUSD != 1.5 {
		t.Errorf("max_budget_usd = %v", l.Config.Harness.ClaudeCode.MaxBudgetUSD)
	}
}

func TestConfirmOutlineFromProjectFile(t *testing.T) {
	project := writeFile(t, t.TempDir(), "p.yaml", "receive:\n  confirm_outline: true\n")
	l, err := Load(Sources{ProjectFile: project})
	if err != nil {
		t.Fatal(err)
	}
	if !l.Config.Receive.ConfirmOutline {
		t.Error("receive.confirm_outline should be true")
	}
}

func TestConfirmOutlineFromEnv(t *testing.T) {
	l, err := Load(Sources{Environ: []string{"RALPH_RECEIVE__CONFIRM_OUTLINE=true"}})
	if err != nil {
		t.Fatal(err)
	}
	if !l.Config.Receive.ConfirmOutline {
		t.Error("receive.confirm_outline should be true")
	}
}

func TestConfirmOutlineUnderStepsIsUnknown(t *testing.T) {
	project := writeFile(t, t.TempDir(), "p.yaml", "steps:\n  receive:\n    confirm_outline: true\n")
	_, err := Load(Sources{ProjectFile: project})
	if err == nil || !strings.Contains(err.Error(), "confirm_outline") {
		t.Errorf("err = %v, want an unknown-key error naming confirm_outline", err)
	}
}

func TestValidateOllamaSettings(t *testing.T) {
	cfg, _ := Defaults()
	cfg.Embeddings.Ollama.BaseURL = "localhost:11434"
	cfg.Embeddings.Ollama.Model = ""
	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "embeddings.ollama.base_url") || !strings.Contains(err.Error(), "embeddings.ollama.model") {
		t.Fatalf("got %v", err)
	}
	cfg.Embeddings.Provider = EmbeddingsNone
	if err := cfg.Validate(); err != nil {
		t.Errorf("ollama settings should be ignored when provider is none: %v", err)
	}
}

func TestResolveStep(t *testing.T) {
	cfg, _ := Defaults()
	cfg.Harness.ClaudeCode.Model = "sonnet"
	cfg.Steps.Loop.Model = "opus"

	r, err := cfg.ResolveStep(StepLoop)
	if err != nil {
		t.Fatal(err)
	}
	if r.Harness != HarnessClaudeCode || r.Model != "opus" {
		t.Errorf("loop resolution = %+v", r)
	}
	r, err = cfg.ResolveStep(StepArticulate)
	if err != nil {
		t.Fatal(err)
	}
	if r.Model != "sonnet" {
		t.Errorf("articulate should inherit harness model: %+v", r)
	}
	if _, err := cfg.ResolveStep("deploy"); err == nil {
		t.Error("unknown step should error")
	}
	for _, s := range Steps() {
		if _, err := cfg.ResolveStep(s); err != nil {
			t.Errorf("step %s: %v", s, err)
		}
	}
}

func TestPaths(t *testing.T) {
	cfg, _ := Defaults()
	if got := cfg.StoragePath("/repo"); got != filepath.Join("/repo", ".ralph", "ralph.db") {
		t.Errorf("relative storage = %q", got)
	}
	cfg.Storage.Path = "/var/ralph.db"
	if got := cfg.StoragePath("/repo"); got != "/var/ralph.db" {
		t.Errorf("absolute storage = %q", got)
	}
	if got := cfg.ProjectName("/src/widgets"); got != "widgets" {
		t.Errorf("project name = %q", got)
	}
}

func TestValidatePR(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*Config)
		want   string
	}{
		{"valid basic", func(c *Config) {}, ""},
		{"bearer without email_env", func(c *Config) {
			c.PR.BitbucketCloud.Auth = "bearer"
			c.PR.BitbucketCloud.EmailEnv = ""
		}, ""},
		{"empty remote", func(c *Config) { c.PR.Remote = "" }, "pr.remote:"},
		{"blank remote", func(c *Config) { c.PR.Remote = " \t" }, "pr.remote:"},
		{"zero timeout", func(c *Config) { c.PR.Timeout = 0 }, "pr.timeout:"},
		{"negative timeout", func(c *Config) { c.PR.Timeout = -time.Second }, "pr.timeout:"},
		{"unknown auth", func(c *Config) { c.PR.BitbucketCloud.Auth = "oauth" }, "pr.bitbucket_cloud.auth:"},
		{"empty auth", func(c *Config) { c.PR.BitbucketCloud.Auth = "" }, "pr.bitbucket_cloud.auth:"},
		{"empty token_env", func(c *Config) { c.PR.BitbucketCloud.TokenEnv = " " }, "pr.bitbucket_cloud.token_env:"},
		{"bearer empty token_env", func(c *Config) {
			c.PR.BitbucketCloud.Auth = "bearer"
			c.PR.BitbucketCloud.TokenEnv = ""
		}, "pr.bitbucket_cloud.token_env:"},
		{"basic empty email_env", func(c *Config) { c.PR.BitbucketCloud.EmailEnv = "" }, "pr.bitbucket_cloud.email_env:"},
		{"github valid", func(c *Config) {
			c.PR.Provider = PRGitHub
			c.PR.GitHub.Owner, c.PR.GitHub.Repo = "acme", "widgets"
		}, ""},
		{"github skips bitbucket section", func(c *Config) {
			c.PR.Provider = PRGitHub
			c.PR.GitHub.Owner, c.PR.GitHub.Repo = "acme", "widgets"
			c.PR.BitbucketCloud = BitbucketCloudConfig{}
		}, ""},
		{"github empty owner", func(c *Config) {
			c.PR.Provider = PRGitHub
			c.PR.GitHub.Repo = "widgets"
		}, "pr.github.owner:"},
		{"github blank repo", func(c *Config) {
			c.PR.Provider = PRGitHub
			c.PR.GitHub.Owner, c.PR.GitHub.Repo = "acme", " "
		}, "pr.github.repo:"},
		{"github empty token_env", func(c *Config) {
			c.PR.Provider = PRGitHub
			c.PR.GitHub.Owner, c.PR.GitHub.Repo = "acme", "widgets"
			c.PR.GitHub.TokenEnv = " "
		}, "pr.github.token_env:"},
		{"bitbucket skips github section", func(c *Config) {
			c.PR.GitHub = GitHubConfig{}
		}, ""},
		{"unknown provider lists github", func(c *Config) { c.PR.Provider = "gitlab" }, "github"},
		{"other provider skips section", func(c *Config) {
			c.PR.Provider = "gitlab"
			c.PR.BitbucketCloud = BitbucketCloudConfig{}
		}, "pr.provider:"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := Defaults()
			if err != nil {
				t.Fatal(err)
			}
			tc.mutate(cfg)
			err = cfg.Validate()
			if tc.want == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want %q in %v", tc.want, err)
			}
			if tc.name == "other provider skips section" && strings.Contains(err.Error(), "pr.bitbucket_cloud") {
				t.Errorf("bitbucket settings validated for another provider: %v", err)
			}
		})
	}
}

func TestGitHubDefaults(t *testing.T) {
	cfg, err := Defaults()
	if err != nil {
		t.Fatal(err)
	}
	g := cfg.PR.GitHub
	if g.TokenEnv != "RALPH_GITHUB_TOKEN" || g.Owner != "" || g.Repo != "" {
		t.Errorf("github defaults = %+v", g)
	}
	if cfg.PR.Provider != PRBitbucketCloud {
		t.Errorf("pr.provider = %q", cfg.PR.Provider)
	}
}

func TestGitHubLayers(t *testing.T) {
	file := writeFile(t, t.TempDir(), "p.yaml", "pr:\n  provider: github\n  github:\n    owner: file-owner\n    repo: widgets\n    token_env: MY_GH_TOKEN\n")
	l, err := Load(Sources{
		ProjectFile: file,
		Environ: []string{
			"RALPH_PR__GITHUB__OWNER=acme",
			"RALPH_GITHUB_TOKEN=secret",
		},
	})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	g := l.Config.PR.GitHub
	if g.Owner != "acme" || g.Repo != "widgets" || g.TokenEnv != "MY_GH_TOKEN" {
		t.Errorf("github = %+v", g)
	}
	if err := l.Config.Validate(); err != nil {
		t.Errorf("Validate: %v", err)
	}
	out, err := l.YAML()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), "secret") {
		t.Errorf("token leaked into config:\n%s", out)
	}

	l, err = Load(Sources{Environ: []string{"RALPH_GITHUB_TOKEN=secret"}})
	if err != nil {
		t.Fatal(err)
	}
	if got := l.Config.PR.GitHub.TokenEnv; got != "RALPH_GITHUB_TOKEN" {
		t.Errorf("token_env = %q", got)
	}
}

func TestEnvVar(t *testing.T) {
	if got := EnvVar("embeddings.ollama.base_url"); got != "RALPH_EMBEDDINGS__OLLAMA__BASE_URL" {
		t.Errorf("EnvVar = %q", got)
	}
	if got := envKey(EnvVar("pr.bitbucket_cloud.token_env")); got != "pr.bitbucket_cloud.token_env" {
		t.Errorf("round trip = %q", got)
	}
}

func TestProjectTemplateOverridesNothing(t *testing.T) {
	tmpl := ProjectTemplate()
	m, err := yaml.Parser().Unmarshal(tmpl)
	if err != nil {
		t.Fatalf("template is not valid YAML: %v", err)
	}
	if len(m) != 0 {
		t.Errorf("template should contain only comments, got keys %v", m)
	}
	for _, line := range strings.Split(string(defaultsYAML), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if !strings.Contains(string(tmpl), "\n# "+line+"\n") {
			t.Errorf("template is missing commented setting %q", line)
		}
	}
	for _, line := range strings.Split(string(tmpl), "\n") {
		if strings.Contains(line, "confirm_outline: false") && !strings.HasPrefix(line, "# ") {
			t.Errorf("confirm_outline should only appear commented out, got %q", line)
		}
	}
	if !strings.Contains(string(tmpl), "\n#   confirm_outline: false\n") {
		t.Error("template is missing the commented receive.confirm_outline setting")
	}
	if !strings.Contains(string(tmpl), "\n# task_manager:\n") {
		t.Error("template is missing the commented task_manager section")
	}
}

func TestStatusMapDefaultsAndLayers(t *testing.T) {
	cfg, err := Defaults()
	if err != nil {
		t.Fatal(err)
	}
	want := map[string][]string{"TODO": {"To Do"}, "IN_PROGRESS": {"In Progress"}, "DONE": {"Done"}}
	got := cfg.TaskManager.Jira.StatusMap
	if len(got) != len(want) {
		t.Errorf("status_map defaults = %v", got)
	}
	for k, v := range want {
		if !slices.Equal(got[k], v) {
			t.Errorf("status_map defaults = %v", got)
		}
	}

	add := writeFile(t, t.TempDir(), "add.yaml", "task_manager:\n  jira:\n    status_map:\n      BLOCKED: Blocked\n")
	l, err := Load(Sources{ProjectFile: add})
	if err != nil {
		t.Fatal(err)
	}
	m := l.Config.TaskManager.Jira.StatusMap
	if len(m) != 4 || !slices.Equal(m["BLOCKED"], []string{"Blocked"}) || !slices.Equal(m["TODO"], []string{"To Do"}) || !slices.Equal(m["DONE"], []string{"Done"}) {
		t.Errorf("entry did not merge with defaults: %v", m)
	}

	blank := writeFile(t, t.TempDir(), "blank.yaml", "task_manager:\n  jira:\n    status_map:\n      DONE: \"\"\n")
	l, err = Load(Sources{ProjectFile: blank})
	if err != nil {
		t.Fatal(err)
	}
	m = l.Config.TaskManager.Jira.StatusMap
	if v, ok := m["DONE"]; !ok || len(v) > 1 || (len(v) == 1 && v[0] != "") || !slices.Equal(m["TODO"], []string{"To Do"}) {
		t.Errorf("blanking a default: %v", m)
	}

	path := writeFile(t, t.TempDir(), "path.yaml", "task_manager:\n  jira:\n    status_map:\n      DONE: [To Do, In Progress, Done]\n      IN_PROGRESS: [To Do, In Progress]\n")
	l, err = Load(Sources{ProjectFile: path})
	if err != nil {
		t.Fatal(err)
	}
	m = l.Config.TaskManager.Jira.StatusMap
	if !slices.Equal(m["DONE"], []string{"To Do", "In Progress", "Done"}) || !slices.Equal(m["IN_PROGRESS"], []string{"To Do", "In Progress"}) || !slices.Equal(m["TODO"], []string{"To Do"}) {
		t.Errorf("path did not replace the default: %v", m)
	}
}

func TestImportMaxFileBytes(t *testing.T) {
	project := writeFile(t, t.TempDir(), "p.yaml", "import:\n  max_file_bytes: 2048\n")
	l, err := Load(Sources{ProjectFile: project})
	if err != nil {
		t.Fatal(err)
	}
	if l.Config.Import.MaxFileBytes != 2048 {
		t.Errorf("file: import.max_file_bytes = %d", l.Config.Import.MaxFileBytes)
	}
	l, err = Load(Sources{ProjectFile: project, Environ: []string{"RALPH_IMPORT__MAX_FILE_BYTES=4096"}})
	if err != nil {
		t.Fatal(err)
	}
	if l.Config.Import.MaxFileBytes != 4096 {
		t.Errorf("env: import.max_file_bytes = %d", l.Config.Import.MaxFileBytes)
	}
}

func TestUIProgress(t *testing.T) {
	project := writeFile(t, t.TempDir(), "p.yaml", "ui:\n  progress: never\n")
	l, err := Load(Sources{ProjectFile: project})
	if err != nil {
		t.Fatal(err)
	}
	if l.Config.UI.Progress != UIProgressNever {
		t.Errorf("file: ui.progress = %q", l.Config.UI.Progress)
	}
	other := writeFile(t, t.TempDir(), "q.yaml", "ui:\n  progress: auto\n")
	l, err = Load(Sources{ProjectFile: other, Environ: []string{"RALPH_UI__PROGRESS=never"}})
	if err != nil {
		t.Fatal(err)
	}
	if l.Config.UI.Progress != UIProgressNever {
		t.Errorf("env: ui.progress = %q", l.Config.UI.Progress)
	}
}

func TestValidateUIProgress(t *testing.T) {
	for _, v := range []string{UIProgressAuto, UIProgressNever} {
		cfg, _ := Defaults()
		cfg.UI.Progress = v
		if err := cfg.Validate(); err != nil {
			t.Errorf("progress %q: %v", v, err)
		}
	}
	cfg, _ := Defaults()
	cfg.UI.Progress = "always"
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "ui.progress:") || !strings.Contains(err.Error(), "auto, never") {
		t.Errorf("progress err = %v", err)
	}
}

func TestInitSettings(t *testing.T) {
	project := writeFile(t, t.TempDir(), "p.yaml", "init:\n  update: manual\n  ignore: [.claude/commands/ralph/receive.md, docs/x.md]\n")
	l, err := Load(Sources{ProjectFile: project})
	if err != nil {
		t.Fatal(err)
	}
	if l.Config.Init.Update != InitUpdateManual || !slices.Equal(l.Config.Init.Ignore, []string{".claude/commands/ralph/receive.md", "docs/x.md"}) {
		t.Errorf("file: init = %+v", l.Config.Init)
	}
	file := writeFile(t, t.TempDir(), "q.yaml", "init:\n  update: auto\n")
	l, err = Load(Sources{ProjectFile: file, Environ: []string{"RALPH_INIT__UPDATE=manual"}})
	if err != nil {
		t.Fatal(err)
	}
	if l.Config.Init.Update != InitUpdateManual {
		t.Errorf("env: init.update = %q", l.Config.Init.Update)
	}
}

func TestValidateInit(t *testing.T) {
	for _, u := range []string{InitUpdateAuto, InitUpdateManual} {
		cfg, _ := Defaults()
		cfg.Init.Update = u
		if err := cfg.Validate(); err != nil {
			t.Errorf("update %q: %v", u, err)
		}
	}
	cfg, _ := Defaults()
	cfg.Init.Update = "sometimes"
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "init.update:") || !strings.Contains(err.Error(), "auto, manual") {
		t.Errorf("update err = %v", err)
	}
	for _, p := range []string{"", "/x", `a\b`, "../x", "a/../b", "a/.."} {
		cfg, _ := Defaults()
		cfg.Init.Ignore = []string{p}
		err := cfg.Validate()
		if err == nil || !strings.Contains(err.Error(), "init.ignore:") || !strings.Contains(err.Error(), fmt.Sprintf("%q", p)) {
			t.Errorf("ignore %q: err = %v", p, err)
		}
	}
	cfg, _ = Defaults()
	cfg.Init.Ignore = []string{".claude/commands/ralph/receive.md"}
	if err := cfg.Validate(); err != nil {
		t.Errorf("valid ignore: %v", err)
	}
}

func TestPRRemote(t *testing.T) {
	cfg, err := Defaults()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.PR.Remote != "origin" {
		t.Errorf("default pr.remote = %q, want origin", cfg.PR.Remote)
	}

	project := writeFile(t, t.TempDir(), "p.yaml", "pr:\n  remote: fork\n")
	l, err := Load(Sources{ProjectFile: project})
	if err != nil {
		t.Fatal(err)
	}
	if l.Config.PR.Remote != "fork" {
		t.Errorf("project pr.remote = %q, want fork", l.Config.PR.Remote)
	}

	l, err = Load(Sources{ProjectFile: project, Environ: []string{"RALPH_PR__REMOTE=upstream"}})
	if err != nil {
		t.Fatal(err)
	}
	if l.Config.PR.Remote != "upstream" {
		t.Errorf("env pr.remote = %q, want upstream", l.Config.PR.Remote)
	}
	if got := EnvVar("pr.remote"); got != "RALPH_PR__REMOTE" {
		t.Errorf("EnvVar = %q", got)
	}
}

func TestPRStaleBase(t *testing.T) {
	cfg, err := Defaults()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.PR.StaleBase != StaleBaseRefuse {
		t.Errorf("default pr.stale_base = %q, want refuse", cfg.PR.StaleBase)
	}

	for _, v := range []string{StaleBaseRefuse, StaleBaseSync} {
		cfg.PR.StaleBase = v
		if err := cfg.Validate(); err != nil {
			t.Errorf("%q should validate: %v", v, err)
		}
	}
	for _, v := range []string{"rebase", ""} {
		cfg.PR.StaleBase = v
		cfg.PR.Provider = "gitlab"
		err := cfg.Validate()
		if err == nil {
			t.Fatalf("%q should fail validation", v)
		}
		for _, want := range []string{"pr.stale_base:", "refuse, sync", "pr.provider:"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("%q: missing %q in:\n%v", v, want, err)
			}
		}
	}

	project := writeFile(t, t.TempDir(), "p.yaml", "pr:\n  stale_base: sync\n")
	l, err := Load(Sources{ProjectFile: project})
	if err != nil {
		t.Fatal(err)
	}
	if l.Config.PR.StaleBase != StaleBaseSync {
		t.Errorf("project pr.stale_base = %q, want sync", l.Config.PR.StaleBase)
	}

	typo := writeFile(t, t.TempDir(), "typo.yaml", "pr:\n  stale_bse: sync\n")
	if _, err := Load(Sources{ProjectFile: typo}); err == nil {
		t.Error("unknown key under pr should fail strict decoding")
	}
}
