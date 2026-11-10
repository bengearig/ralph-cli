package config

import (
	"os"
	"path/filepath"
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
	if cfg.Loop.StallThreshold != 3 || !cfg.Loop.CommitPerTask || !cfg.Loop.CommitTrailer || !cfg.PR.Draft {
		t.Errorf("unexpected loop/pr defaults: %+v %+v", cfg.Loop, cfg.PR)
	}
	if cfg.Receive.ConfirmOutline {
		t.Error("receive.confirm_outline should default to false")
	}
	tm := cfg.TaskManager
	if tm.Provider != TaskManagerNone || tm.Timeout != 10*time.Second {
		t.Errorf("task_manager defaults = %+v", tm)
	}
	if j := tm.Jira; j.EmailEnv != "RALPH_JIRA_EMAIL" || j.TokenEnv != "RALPH_JIRA_TOKEN" || j.CanceledStatus != "" ||
		j.IssueTypes != (JiraIssueTypes{Initiative: "Epic", Milestone: "Task", Task: "Sub-task"}) {
		t.Errorf("jira defaults = %+v", j)
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
			c.TaskManager.Jira.StatusMap["BLOCKED"] = "Blocked"
		}, nil},
		{"status_map blank value", func(c *Config) {
			complete(c)
			c.TaskManager.Jira.StatusMap["DONE"] = ""
		}, nil},
		{"status_map milestone status", func(c *Config) {
			complete(c)
			c.TaskManager.Jira.StatusMap["ARTICULATING"] = "Backlog"
		}, nil},
		{"status_map unknown key", func(c *Config) {
			complete(c)
			c.TaskManager.Jira.StatusMap["WAITING"] = "Waiting"
		}, []string{"task_manager.jira.status_map:", "WAITING"}},
		{"status_map cancelled key", func(c *Config) {
			complete(c)
			c.TaskManager.Jira.StatusMap["CANCELLED"] = "Cancelled"
		}, []string{"task_manager.jira.status_map:", "canceled_status"}},
		{"none ignores invalid status_map", func(c *Config) {
			c.TaskManager.Jira.StatusMap = map[string]string{"WAITING": "x", "CANCELLED": "y"}
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
			"RALPH_BITBUCKET_TOKEN=secret",
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
	if c.PR.Draft {
		t.Error("env bool: pr.draft should be false")
	}
	if c.Loop.CommitTrailer {
		t.Error("env bool: loop.commit_trailer should be false")
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
	cfg.Steps.Loop.Harness = "codex"
	cfg.Loop.StallThreshold = 0
	cfg.Loop.BranchTemplate = "ralph/{{.Milestone"
	cfg.PR.Provider = "github"
	cfg.PR.BaseBranch = " "
	cfg.Harness.ClaudeCode.MaxBudgetUSD = -1
	cfg.Loop.CommitTemplate = "{{.Task"

	err = cfg.Validate()
	if err == nil {
		t.Fatal("expected validation error")
	}
	for _, key := range []string{
		"log.level", "embeddings.provider", "embeddings.timeout", "embeddings.chunk.overlap_chars",
		"search.limit", "steps.loop.harness", "loop.stall_threshold", "loop.branch_template",
		"pr.provider", "pr.base_branch", "harness.claude_code.max_budget_usd", "loop.commit_template",
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
	want := map[string]string{"TODO": "To Do", "IN_PROGRESS": "In Progress", "DONE": "Done"}
	if got := cfg.TaskManager.Jira.StatusMap; len(got) != len(want) || got["TODO"] != want["TODO"] || got["IN_PROGRESS"] != want["IN_PROGRESS"] || got["DONE"] != want["DONE"] {
		t.Errorf("status_map defaults = %v", got)
	}

	add := writeFile(t, t.TempDir(), "add.yaml", "task_manager:\n  jira:\n    status_map:\n      BLOCKED: Blocked\n")
	l, err := Load(Sources{ProjectFile: add})
	if err != nil {
		t.Fatal(err)
	}
	m := l.Config.TaskManager.Jira.StatusMap
	if len(m) != 4 || m["BLOCKED"] != "Blocked" || m["TODO"] != "To Do" || m["DONE"] != "Done" {
		t.Errorf("entry did not merge with defaults: %v", m)
	}

	blank := writeFile(t, t.TempDir(), "blank.yaml", "task_manager:\n  jira:\n    status_map:\n      DONE: \"\"\n")
	l, err = Load(Sources{ProjectFile: blank})
	if err != nil {
		t.Fatal(err)
	}
	m = l.Config.TaskManager.Jira.StatusMap
	if v, ok := m["DONE"]; !ok || v != "" || m["TODO"] != "To Do" {
		t.Errorf("blanking a default: %v", m)
	}
}
