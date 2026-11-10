package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bengearig/ralph/internal/config"
	"github.com/bengearig/ralph/internal/domain"
	"github.com/bengearig/ralph/internal/errs"
	"github.com/bengearig/ralph/internal/pr"
	"github.com/bengearig/ralph/internal/project"
	"github.com/bengearig/ralph/internal/service"
)

// testEnv runs ralph commands against a temporary project with embeddings
// disabled, isolated from the user's global config.
type testEnv struct {
	t   *testing.T
	dir string
}

func newTestEnv(t *testing.T, initProject bool) *testEnv {
	t.Helper()
	dir := t.TempDir()
	if initProject {
		if err := os.Mkdir(filepath.Join(dir, project.DirName), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "xdg"))
	t.Setenv("HOME", dir)
	t.Setenv("RALPH_EMBEDDINGS__PROVIDER", "none")
	return &testEnv{t: t, dir: dir}
}

func (h *testEnv) run(stdin string, args ...string) (string, string, error) {
	h.t.Helper()
	var out, errOut bytes.Buffer
	full := append([]string{"--project-dir", h.dir}, args...)
	err := Execute(context.Background(), Streams{In: strings.NewReader(stdin), Out: &out, Err: &errOut}, full)
	return out.String(), errOut.String(), err
}

func (h *testEnv) ok(args ...string) string {
	h.t.Helper()
	out, errOut, err := h.run("", args...)
	if err != nil {
		h.t.Fatalf("ralph %s: %v\nstderr: %s", strings.Join(args, " "), err, errOut)
	}
	return out
}

func (h *testEnv) json(v any, args ...string) {
	h.t.Helper()
	out := h.ok(append([]string{"--json"}, args...)...)
	if err := json.Unmarshal([]byte(out), v); err != nil {
		h.t.Fatalf("decoding %q: %v", out, err)
	}
}

func TestWorkflowThroughCLI(t *testing.T) {
	h := newTestEnv(t, true)
	var i struct{ ID string }
	out := h.ok("--json", "initiative", "create", "--title", "Customer SSO", "--brief", "Okta login for customers")
	if err := json.Unmarshal([]byte(out), &i); err != nil {
		t.Fatalf("decoding %q: %v", out, err)
	}
	if i.ID != "I-1" {
		t.Fatalf("initiative id = %q", i.ID)
	}
	var fields map[string]any
	if err := json.Unmarshal([]byte(out), &fields); err != nil {
		t.Fatal(err)
	}
	if _, ok := fields["confirm_outline"]; ok {
		t.Errorf("CLI initiative create JSON has MCP-only confirm_outline: %s", out)
	}
	h.ok("milestone", "create", "--initiative", "I-1", "--title", "Callback")
	h.ok("task", "create", "--milestone", "M-1", "--title", "Route", "--description", "Add route", "--criteria", "Tests pass")
	h.ok("task", "status", "T-1", "todo", "--reason", "articulated")

	h.ok("task", "note", "T-1", "first note")
	h.ok("task", "note", "T-1", "second note")
	if out := h.ok("task", "show", "T-1"); !strings.Contains(out, " N-2 NOTE: first note") || !strings.Contains(out, " N-3 NOTE: second note") {
		t.Errorf("task show lacks note IDs:\n%s", out)
	}
	var shown struct {
		Notes []struct{ ID, Body string }
	}
	h.json(&shown, "task", "show", "T-1")
	if len(shown.Notes) != 3 || shown.Notes[0].ID != "N-1" || shown.Notes[2].ID != "N-3" {
		t.Errorf("task show --json notes = %+v", shown.Notes)
	}

	var next struct{ ID, Status string }
	h.json(&next, "task", "next", "--milestone", "M-1")
	if next.ID != "T-1" || next.Status != "TODO" {
		t.Errorf("next = %+v", next)
	}

	_, _, err := h.run("", "task", "status", "T-1", "DONE")
	if !errors.Is(err, domain.ErrInvalidTransition) {
		t.Errorf("TODO → DONE should be rejected: %v", err)
	}

	_, _, err = h.run("# Notes\nOkta tenant is acme", "doc", "create", "--kind", "reference", "--title", "Okta", "--file", "-")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct{ Body string }
	h.json(&doc, "doc", "show", "D-1")
	if !strings.Contains(doc.Body, "acme") {
		t.Errorf("stdin body not stored: %q", doc.Body)
	}

	var res struct {
		Mode string
		Hits []struct{ ID string }
	}
	h.json(&res, "search", "okta", "tenant", "--kind", "document")
	if res.Mode != "keyword" || len(res.Hits) != 1 || res.Hits[0].ID != "D-1" {
		t.Errorf("search = %+v", res)
	}

	if out := h.ok("status"); !strings.Contains(out, "READY") || !strings.Contains(out, "TODO 1") {
		t.Errorf("status output:\n%s", out)
	}
	if out := h.ok("index"); !strings.Contains(out, "keyword-only") {
		t.Errorf("index output: %s", out)
	}
}

func TestInputErrors(t *testing.T) {
	h := newTestEnv(t, true)
	cases := [][]string{
		{"task", "show", "M-1"},
		{"task", "status", "T-1", "WAITING"},
		{"doc", "create", "--kind", "memo", "--title", "x"},
		{"doc", "create", "--kind", "note", "--title", "x", "--body", "a", "--file", "b"},
		{"doc", "list", "--project", "--initiative", "I-1"},
		{"milestone", "create", "--initiative", "I-9", "--title", "x"},
		{"search", "x", "--kind", "widget"},
		{"initiative", "create"},
	}
	for _, args := range cases {
		if _, _, err := h.run("", args...); err == nil {
			t.Errorf("ralph %s should fail", strings.Join(args, " "))
		}
	}
}

func TestOutsideProject(t *testing.T) {
	h := newTestEnv(t, false)
	_, _, err := h.run("", "status")
	if !errors.Is(err, project.ErrNotFound) {
		t.Errorf("err = %v", err)
	}
	if out := h.ok("config", "validate"); !strings.Contains(out, "valid") {
		t.Errorf("config validate outside a project: %s", out)
	}
}

func TestConfigLayersVisible(t *testing.T) {
	h := newTestEnv(t, true)
	if err := os.WriteFile(filepath.Join(h.dir, ".ralph", "config.yaml"), []byte("search:\n  limit: 3\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		Search      struct{ Limit int }
		TaskManager struct{ Provider string }
	}
	h.json(&cfg, "config", "show")
	if cfg.TaskManager.Provider != "none" {
		t.Errorf("config show lacks task_manager: %+v", cfg)
	}
	if cfg.Search.Limit != 3 {
		t.Errorf("project config not applied: %+v", cfg)
	}
	var layers []struct {
		Name    string
		Present bool
	}
	h.json(&layers, "--log-level", "debug", "config", "path")
	got := map[string]bool{}
	for _, l := range layers {
		got[l.Name] = l.Present
	}
	if !got["project"] || !got["environment"] || !got["flags"] || got["global"] {
		t.Errorf("layers = %+v", layers)
	}
}

func TestInit(t *testing.T) {
	h := newTestEnv(t, false)
	var res struct {
		Root    string
		Changes []struct{ Path, Action string }
	}
	h.json(&res, "init", "--ralph-command", "/bin/ralph")
	if res.Root != h.dir {
		t.Errorf("root = %q", res.Root)
	}
	for _, p := range []string{".ralph/config.yaml", ".ralph/ralph.db", ".gitignore", ".mcp.json", "CLAUDE.md", ".claude/commands/ralph/receive.md"} {
		if _, err := os.Stat(filepath.Join(h.dir, p)); err != nil {
			t.Errorf("%s: %v", p, err)
		}
	}
	gi, _ := os.ReadFile(filepath.Join(h.dir, ".gitignore"))
	for _, pat := range []string{"/.ralph/ralph.db*\n", "/.ralph/runs/\n", "/.ralph/loop.lock\n"} {
		if !strings.Contains(string(gi), pat) {
			t.Errorf(".gitignore lacks %q: %q", pat, gi)
		}
	}

	h.json(&res, "init")
	for _, c := range res.Changes {
		if c.Action != "unchanged" && c.Path != ".mcp.json" {
			t.Errorf("rerun changed %s: %s", c.Path, c.Action)
		}
	}
	h.ok("initiative", "create", "--title", "works after init")

	if _, _, err := h.run("", "init", "--no-harness", "--force"); err != nil {
		t.Errorf("forced rerun: %v", err)
	}
}

func TestInitAddsMissingGitignorePatterns(t *testing.T) {
	h := newTestEnv(t, false)
	gi := filepath.Join(h.dir, ".gitignore")
	if err := os.WriteFile(gi, []byte("bin/\n# ralph database (local to each clone)\n/.ralph/ralph.db*"), 0o644); err != nil {
		t.Fatal(err)
	}
	h.ok("init", "--no-harness")
	data, _ := os.ReadFile(gi)
	got := string(data)
	if strings.Count(got, "/.ralph/ralph.db*") != 1 || !strings.Contains(got, "ralph.db*\n# ralph local files") ||
		!strings.HasSuffix(got, "/.ralph/runs/\n/.ralph/loop.lock\n") {
		t.Errorf(".gitignore = %q", got)
	}
}

func TestLoopCommand(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	h := newTestEnv(t, false)
	for k, v := range map[string]string{
		"GIT_CONFIG_NOSYSTEM": "1",
		"GIT_AUTHOR_NAME":     "Ralph Test", "GIT_AUTHOR_EMAIL": "ralph@example.com",
		"GIT_COMMITTER_NAME": "Ralph Test", "GIT_COMMITTER_EMAIL": "ralph@example.com",
	} {
		t.Setenv(k, v)
	}
	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = h.dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "--quiet", "--initial-branch=main")
	h.ok("init", "--no-harness")
	run("add", ".")
	run("commit", "--quiet", "-m", "initial")

	// A claude stand-in that connects to ralph but never sets a status, so
	// every session counts as a failed iteration.
	bin := filepath.Join(t.TempDir(), "claude")
	script := "#!/bin/sh\ncat >/dev/null\n" +
		"printf '%s\\n' '{\"type\":\"system\",\"subtype\":\"init\",\"mcp_servers\":[{\"name\":\"ralph\",\"status\":\"connected\"}]}'\n" +
		"printf '%s\\n' '{\"type\":\"result\",\"subtype\":\"success\",\"result\":\"looked around\",\"num_turns\":2}'\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RALPH_HARNESS__CLAUDE_CODE__BINARY", bin)
	t.Setenv("RALPH_LOOP__STALL_THRESHOLD", "2")

	h.ok("initiative", "create", "--title", "SSO")
	h.ok("milestone", "create", "--initiative", "I-1", "--title", "Login")
	h.ok("task", "create", "--milestone", "M-1", "--title", "Route", "--description", "d", "--criteria", "c")
	h.ok("task", "status", "T-1", "TODO")

	out, errOut, err := h.run("", "loop", "--milestone", "M-1")
	if !errors.Is(err, errs.ErrNeedsHuman) {
		t.Fatalf("err = %v\nstderr: %s", err, errOut)
	}
	if !strings.Contains(out, "M-1: needs_human after 2 iteration(s)") {
		t.Errorf("stdout = %q", out)
	}
	for _, want := range []string{"creating ralph/login from main", "▶ [1/50] T-1 Route", "✗ T-1 BLOCKED: Stall Statute"} {
		if !strings.Contains(errOut, want) {
			t.Errorf("stderr lacks %q:\n%s", want, errOut)
		}
	}
	var task struct {
		Status        string
		BlockedReason string `json:"blocked_reason"`
	}
	h.json(&task, "task", "show", "T-1")
	if task.Status != "BLOCKED" || !strings.Contains(task.BlockedReason, "looked around") {
		t.Errorf("task = %+v", task)
	}
	logs, _ := filepath.Glob(filepath.Join(h.dir, ".ralph", "runs", "M-1", "*.jsonl"))
	if len(logs) != 2 {
		t.Errorf("transcripts = %v", logs)
	}
	status := exec.Command("git", "status", "--porcelain")
	status.Dir = h.dir
	if st, _ := status.Output(); len(st) != 0 {
		t.Errorf("ralph's own files should be git-ignored: %q", st)
	}
}

func TestShowRecordedGitState(t *testing.T) {
	h := newTestEnv(t, true)
	h.ok("initiative", "create", "--title", "Customer SSO")
	h.ok("milestone", "create", "--initiative", "I-1", "--title", "Callback")
	h.ok("task", "create", "--milestone", "M-1", "--title", "Route", "--description", "Add route", "--criteria", "Tests pass")
	plainMilestone := h.ok("milestone", "show", "M-1")
	plainTask := h.ok("task", "show", "T-1")

	cfg, err := config.Defaults()
	if err != nil {
		t.Fatal(err)
	}
	cfg.Embeddings.Provider = config.EmbeddingsNone
	svc, closeFn, err := service.Open(context.Background(), cfg, h.dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer closeFn()
	record := func(g domain.MilestoneGit) {
		t.Helper()
		if err := svc.RecordMilestoneGit(context.Background(), 1, g); err != nil {
			t.Fatal(err)
		}
	}

	if got := h.ok("milestone", "show", "M-1"); got != plainMilestone {
		t.Errorf("milestone show changed with nothing recorded:\n%s\nwant:\n%s", got, plainMilestone)
	}

	base := strings.Repeat("0123456789abcdef", 3)[:40]
	head := strings.Repeat("fedcba9876543210", 4)
	commit := strings.Repeat("abcdef0123456789", 3)[:40]
	statusEnd := strings.Index(plainMilestone, "\nstatus: ")
	statusEnd += strings.Index(plainMilestone[statusEnd+1:], "\n") + 2
	withBranch := func(line string) string {
		return plainMilestone[:statusEnd] + "branch: " + line + "\n" + plainMilestone[statusEnd:]
	}

	record(domain.MilestoneGit{Branch: "ralph/callback"})
	if got, want := h.ok("milestone", "show", "M-1"), withBranch("ralph/callback"); got != want {
		t.Errorf("branch only:\n%s\nwant:\n%s", got, want)
	}
	record(domain.MilestoneGit{Branch: "ralph/callback", BaseCommit: base})
	if got, want := h.ok("milestone", "show", "M-1"), withBranch("ralph/callback (base 0123456789ab)"); got != want {
		t.Errorf("branch and base:\n%s\nwant:\n%s", got, want)
	}
	record(domain.MilestoneGit{Branch: "ralph/callback", BaseCommit: base, HeadCommit: head})
	if got, want := h.ok("milestone", "show", "M-1"), withBranch("ralph/callback (base 0123456789ab, head fedcba987654)"); got != want {
		t.Errorf("branch, base and head:\n%s\nwant:\n%s", got, want)
	}

	if err := svc.RecordTaskCommit(context.Background(), 1, commit); err != nil {
		t.Fatal(err)
	}
	wantTask := strings.Replace(plainTask, "failures: 0\n", "failures: 0\ncommit: abcdef012345\n", 1)
	if got := h.ok("task", "show", "T-1"); got != wantTask {
		t.Errorf("task show:\n%s\nwant:\n%s", got, wantTask)
	}

	var mj struct {
		Branch     string `json:"branch"`
		BaseCommit string `json:"base_commit"`
		HeadCommit string `json:"head_commit"`
		Tasks      []struct {
			Commit string `json:"commit"`
		} `json:"tasks"`
	}
	h.json(&mj, "milestone", "show", "M-1")
	if mj.Branch != "ralph/callback" || mj.BaseCommit != base || mj.HeadCommit != head || len(mj.Tasks) != 1 || mj.Tasks[0].Commit != commit {
		t.Errorf("milestone JSON = %+v", mj)
	}
	var tj struct {
		Commit string `json:"commit"`
	}
	h.json(&tj, "task", "show", "T-1")
	if tj.Commit != commit {
		t.Errorf("task JSON commit = %q, want %q", tj.Commit, commit)
	}
}

func TestLoopDryRunShowsCommitTrailer(t *testing.T) {
	h := newTestEnv(t, true)
	h.ok("initiative", "create", "--title", "Customer SSO")
	h.ok("milestone", "create", "--initiative", "I-1", "--title", "Callback")
	h.ok("task", "create", "--milestone", "M-1", "--title", "Route", "--description", "Add route", "--criteria", "Tests pass")
	h.ok("task", "status", "T-1", "TODO")

	for _, tc := range []struct {
		env  string
		want bool
	}{{"", true}, {"false", false}} {
		if tc.env != "" {
			t.Setenv("RALPH_LOOP__COMMIT_TRAILER", tc.env)
		}
		out := h.ok("loop", "--milestone", "M-1", "--dry-run")
		policy := fmt.Sprintf("policy:     max 50 iterations, stall after 3 failures, commit per task: true, commit trailer: %t\n", tc.want)
		if !strings.Contains(out, policy) {
			t.Errorf("RALPH_LOOP__COMMIT_TRAILER=%q: dry-run lacks %q:\n%s", tc.env, policy, out)
		}
		var plan map[string]any
		h.json(&plan, "loop", "--milestone", "M-1", "--dry-run")
		if got, ok := plan["commit_trailer"].(bool); !ok || got != tc.want {
			t.Errorf("RALPH_LOOP__COMMIT_TRAILER=%q: plan commit_trailer = %v", tc.env, plan["commit_trailer"])
		}
	}
}

func TestPRDryRunUsesRecordedBranch(t *testing.T) {
	h := newTestEnv(t, true)
	h.ok("initiative", "create", "--title", "Customer SSO")
	h.ok("milestone", "create", "--initiative", "I-1", "--title", "Callback")
	h.ok("task", "create", "--milestone", "M-1", "--title", "Route", "--description", "Add route", "--criteria", "Tests pass")
	h.ok("task", "status", "T-1", "TODO")

	if out := h.ok("pr", "create", "--milestone", "M-1", "--dry-run"); !strings.Contains(out, "branches: ralph/callback → main\n") {
		t.Errorf("template branch:\n%s", out)
	}
	var plan struct {
		Branch string `json:"branch"`
	}
	h.json(&plan, "loop", "--milestone", "M-1", "--dry-run")
	if plan.Branch != "ralph/callback" {
		t.Errorf("loop dry-run branch = %q", plan.Branch)
	}
	var m struct {
		Branch string `json:"branch"`
	}
	h.json(&m, "milestone", "show", "M-1")
	if m.Branch != "" {
		t.Errorf("loop --dry-run recorded branch %q", m.Branch)
	}

	cfg, err := config.Defaults()
	if err != nil {
		t.Fatal(err)
	}
	cfg.Embeddings.Provider = config.EmbeddingsNone
	svc, closeFn, err := service.Open(context.Background(), cfg, h.dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer closeFn()
	if err := svc.RecordMilestoneGit(context.Background(), 1, domain.MilestoneGit{Branch: "feature/sso-callback"}); err != nil {
		t.Fatal(err)
	}

	if out := h.ok("pr", "create", "--milestone", "M-1", "--dry-run"); !strings.Contains(out, "branches: feature/sso-callback → main\n") {
		t.Errorf("recorded branch:\n%s", out)
	}
	var req struct {
		Request struct {
			SourceBranch string `json:"source_branch"`
		} `json:"request"`
	}
	h.json(&req, "pr", "create", "--milestone", "M-1", "--dry-run")
	if req.Request.SourceBranch != "feature/sso-callback" {
		t.Errorf("JSON source branch = %q", req.Request.SourceBranch)
	}
	h.json(&plan, "loop", "--milestone", "M-1", "--dry-run")
	if plan.Branch != "feature/sso-callback" {
		t.Errorf("loop dry-run branch = %q", plan.Branch)
	}
}

// newGitTestEnv is newTestEnv in a new git repository whose main branch
// holds an initialized ralph project. git runs a git command in it and
// returns its trimmed output; commit commits a file and returns the hash.
func newGitTestEnv(t *testing.T) (h *testEnv, git func(args ...string) string, commit func(file, msg string) string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	h = newTestEnv(t, false)
	for k, v := range map[string]string{
		"GIT_CONFIG_NOSYSTEM": "1",
		"GIT_AUTHOR_NAME":     "Ralph Test", "GIT_AUTHOR_EMAIL": "ralph@example.com",
		"GIT_COMMITTER_NAME": "Ralph Test", "GIT_COMMITTER_EMAIL": "ralph@example.com",
	} {
		t.Setenv(k, v)
	}
	git = func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = h.dir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	commit = func(file, msg string) string {
		t.Helper()
		if err := os.WriteFile(filepath.Join(h.dir, file), []byte(msg+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		git("add", "--", file)
		git("commit", "--quiet", "-m", msg)
		return git("rev-parse", "HEAD")
	}
	git("init", "--quiet", "--initial-branch=main")
	h.ok("init", "--no-harness")
	git("add", ".")
	git("commit", "--quiet", "-m", "initial")
	return h, git, commit
}

func TestMilestoneReconcile(t *testing.T) {
	h, git, commit := newGitTestEnv(t)
	base := git("rev-parse", "HEAD")

	h.ok("initiative", "create", "--title", "SSO")
	h.ok("milestone", "create", "--initiative", "I-1", "--title", "Login")
	for _, title := range []string{"Route", "Logout"} {
		h.ok("task", "create", "--milestone", "M-1", "--title", title, "--description", "d", "--criteria", "c")
	}
	git("switch", "--quiet", "-c", "ralph/login")
	var commits []string
	for _, id := range []string{"T-1", "T-2"} {
		h.ok("task", "status", id, "TODO")
		h.ok("task", "status", id, "IN_PROGRESS")
		h.ok("task", "status", id, "DONE", "--reason", "done")
		commits = append(commits, commit(id+".txt", id+": work\n\nRalph-Task: "+id))
	}

	cfg, err := config.Defaults()
	if err != nil {
		t.Fatal(err)
	}
	cfg.Embeddings.Provider = config.EmbeddingsNone
	svc, closeFn, err := service.Open(context.Background(), cfg, h.dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer closeFn()
	ctx := context.Background()
	if err := svc.RecordMilestoneGit(ctx, 1, domain.MilestoneGit{Branch: "ralph/login", BaseCommit: base, HeadCommit: commits[1]}); err != nil {
		t.Fatal(err)
	}
	for i, c := range commits {
		if err := svc.RecordTaskCommit(ctx, int64(i+1), c); err != nil {
			t.Fatal(err)
		}
	}
	state := func() string {
		t.Helper()
		return h.ok("--json", "milestone", "show", "M-1")
	}

	// Unchanged: the report alone succeeds.
	out := h.ok("milestone", "reconcile", "M-1")
	for _, want := range []string{"state:    unchanged", "tip:      " + commits[1][:12], "T-1   present  " + commits[0][:12] + "  Route"} {
		if !strings.Contains(out, want) {
			t.Errorf("unchanged report lacks %q:\n%s", want, out)
		}
	}

	// Diverged by a rebase: the report changes nothing and needs a human.
	git("switch", "--quiet", "main")
	commit("upstream.txt", "Upstream change")
	git("switch", "--quiet", "ralph/login")
	git("rebase", "--quiet", "main")
	tip := git("rev-parse", "HEAD")
	rebased := []string{git("rev-parse", "HEAD~1"), tip}
	recorded := state()
	out, _, err = h.run("", "milestone", "reconcile", "M-1")
	if !errors.Is(err, errs.ErrNeedsHuman) {
		t.Fatalf("diverged report: err = %v, want ErrNeedsHuman", err)
	}
	if !strings.HasSuffix(err.Error(), "run: ralph milestone reconcile M-1 --accept") {
		t.Errorf("error should end with the next step: %v", err)
	}
	for _, want := range []string{
		"M-1 has diverged from its recorded head " + commits[1][:12],
		"state:    diverged",
		"recorded: ralph/login (base " + base[:12] + ", head " + commits[1][:12] + ")",
		"tip:      " + tip[:12],
		"T-2   trailer  " + rebased[1][:12] + "  Logout",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("diverged report lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "applied:") {
		t.Errorf("report without flags claims changes:\n%s", out)
	}
	if got := state(); got != recorded {
		t.Errorf("reconcile without flags changed records:\n%s\nwant:\n%s", got, recorded)
	}

	// --json without flags: after is null.
	out, _, err = h.run("", "--json", "milestone", "reconcile", "M-1")
	if !errors.Is(err, errs.ErrNeedsHuman) {
		t.Fatalf("--json report: err = %v", err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(out), &raw); err != nil {
		t.Fatalf("decoding %q: %v", out, err)
	}
	if string(raw["after"]) != "null" {
		t.Errorf("after = %s, want null", raw["after"])
	}

	// --accept resolves it.
	out = h.ok("milestone", "reconcile", "M-1", "--accept")
	for _, want := range []string{
		"applied:\n  accepted ralph/login at " + tip[:12] + ", base " + base[:12] + "\n  T-1 commit " + rebased[0][:12] + "\n  T-2 commit " + rebased[1][:12] + "\n",
		"after:\nM-1 branch ralph/login is at its recorded head " + tip[:12],
	} {
		if !strings.Contains(out, want) {
			t.Errorf("accept output lacks %q:\n%s", want, out)
		}
	}
	var m struct {
		HeadCommit string `json:"head_commit"`
		Tasks      []struct {
			Commit string `json:"commit"`
		} `json:"tasks"`
	}
	h.json(&m, "milestone", "show", "M-1")
	if m.HeadCommit != tip || m.Tasks[0].Commit != rebased[0] || m.Tasks[1].Commit != rebased[1] {
		t.Errorf("after accept: %+v", m)
	}

	// --json with --accept on a branch that is ahead.
	extra := commit("manual.txt", "Manual tweak")
	var res struct {
		Before map[string]any `json:"before"`
		After  map[string]any `json:"after"`
	}
	h.json(&res, "milestone", "reconcile", "M-1", "--accept")
	if res.Before["state"] != "ahead" || res.After["state"] != "unchanged" {
		t.Errorf("states = %v → %v", res.Before["state"], res.After["state"])
	}
	for _, r := range []map[string]any{res.Before, res.After} {
		for _, k := range []string{"milestone", "branch", "base_ref", "recorded", "tip", "state", "remote_branch_exists", "tasks", "unattributed_commits", "proposed_base"} {
			if _, ok := r[k]; !ok {
				t.Errorf("report lacks %q: %v", k, r)
			}
		}
	}
	if extras, _ := res.Before["extra_commits"].([]any); len(extras) != 1 || extras[0].(map[string]any)["hash"] != extra {
		t.Errorf("before extra_commits = %v", res.Before["extra_commits"])
	}

	// A reset drops T-2's work: invalid reopen requests change nothing, a
	// valid one reopens it.
	git("reset", "--quiet", "--hard", rebased[0])
	recorded = state()
	for _, args := range [][]string{
		{"--reopen", "T-2", "--accept"},
		{"--reopen", "T-2,T-9", "--reason", "lost"},
		{"--reopen", "M-1", "--reason", "lost"},
		{"--branch", "", "--accept"},
		{"--branch", "nope", "--accept"},
	} {
		_, _, err := h.run("", append([]string{"milestone", "reconcile", "M-1"}, args...)...)
		if err == nil || errors.Is(err, errs.ErrNeedsHuman) {
			t.Errorf("%v: err = %v, want a usage error", args, err)
		}
		if got := state(); got != recorded {
			t.Errorf("%v changed records", args)
		}
	}
	out = h.ok("milestone", "reconcile", "M-1", "--reopen", "T-2", "--reopen", "T-2", "--reason", "lost in a reset", "--accept")
	if !strings.Contains(out, "applied:\n  reopened T-2 (now TODO)\n  accepted ralph/login at "+rebased[0][:12]) {
		t.Errorf("reopen output:\n%s", out)
	}
	var task struct {
		Status string `json:"status"`
		Commit string `json:"commit"`
	}
	h.json(&task, "task", "show", "T-2")
	if task.Status != "TODO" || task.Commit != "" {
		t.Errorf("T-2 = %+v", task)
	}
}

// fakeProvider is a pull request provider that counts Create calls.
type fakeProvider struct{ created int }

func (f *fakeProvider) Name() string    { return "fake" }
func (f *fakeProvider) Validate() error { return nil }
func (f *fakeProvider) Create(context.Context, pr.Request) (pr.PullRequest, error) {
	f.created++
	return pr.PullRequest{ID: "1", URL: "https://example.com/pr/1"}, nil
}

func TestPRCreateDriftGate(t *testing.T) {
	h, git, commit := newGitTestEnv(t)
	base := git("rev-parse", "HEAD")
	h.ok("initiative", "create", "--title", "SSO")
	h.ok("milestone", "create", "--initiative", "I-1", "--title", "Login")
	for _, title := range []string{"Route", "Logout"} {
		h.ok("task", "create", "--milestone", "M-1", "--title", title, "--description", "d", "--criteria", "c")
	}
	git("switch", "--quiet", "-c", "ralph/login")
	var commits []string
	for _, id := range []string{"T-1", "T-2"} {
		h.ok("task", "status", id, "TODO")
		h.ok("task", "status", id, "IN_PROGRESS")
		h.ok("task", "status", id, "DONE", "--reason", "done")
		commits = append(commits, commit(id+".txt", id+": work\n\nRalph-Task: "+id))
	}
	cfg, err := config.Defaults()
	if err != nil {
		t.Fatal(err)
	}
	cfg.Embeddings.Provider = config.EmbeddingsNone
	svc, closeFn, err := service.Open(context.Background(), cfg, h.dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer closeFn()
	ctx := context.Background()
	record := func(g domain.MilestoneGit) {
		t.Helper()
		if err := svc.RecordMilestoneGit(ctx, 1, g); err != nil {
			t.Fatal(err)
		}
	}
	recorded := domain.MilestoneGit{Branch: "ralph/login", BaseCommit: base, HeadCommit: commits[1]}
	record(recorded)
	for i, c := range commits {
		if err := svc.RecordTaskCommit(ctx, int64(i+1), c); err != nil {
			t.Fatal(err)
		}
	}

	fake := &fakeProvider{}
	newPRProvider = func(*config.Config) (pr.Provider, error) { return fake, nil }
	t.Cleanup(func() { newPRProvider = pr.New })

	type dryRunJSON struct {
		Provider string         `json:"provider"`
		Drift    map[string]any `json:"drift"`
	}
	refused := func(name, want string) {
		t.Helper()
		before := fake.created
		_, _, err := h.run("", "pr", "create", "--milestone", "M-1")
		if !errors.Is(err, errs.ErrNeedsHuman) {
			t.Fatalf("%s: err = %v, want ErrNeedsHuman", name, err)
		}
		for _, w := range []string{want, "run: ralph milestone reconcile M-1"} {
			if !strings.Contains(err.Error(), w) {
				t.Errorf("%s: error lacks %q: %v", name, w, err)
			}
		}
		if fake.created != before {
			t.Errorf("%s: the provider was called", name)
		}
	}

	// Unchanged: the dry run reports it without a task table, and the
	// request reaches the provider.
	out := h.ok("pr", "create", "--milestone", "M-1", "--dry-run")
	if want := "branches: ralph/login → main\ndrift:    unchanged (M-1 branch ralph/login is at its recorded head " + commits[1][:12] + ")\ndraft:"; !strings.Contains(out, want) {
		t.Errorf("unchanged dry run lacks %q:\n%s", want, out)
	}
	if strings.Contains(out, "MATCH") {
		t.Errorf("unchanged dry run prints the task table:\n%s", out)
	}
	var plan dryRunJSON
	h.json(&plan, "pr", "create", "--milestone", "M-1", "--dry-run")
	if plan.Drift["state"] != "unchanged" || plan.Provider != "fake" {
		t.Errorf("unchanged JSON dry run = %+v", plan)
	}
	if fake.created != 0 {
		t.Fatalf("dry run called the provider")
	}
	if out := h.ok("pr", "create", "--milestone", "M-1"); out != "opened https://example.com/pr/1\n" || fake.created != 1 {
		t.Errorf("unchanged create: %q, created %d", out, fake.created)
	}

	// The real provider is reached on a clean milestone, so the error is
	// its own and not a refusal.
	newPRProvider = pr.New
	t.Setenv("RALPH_PR__BITBUCKET_CLOUD__WORKSPACE", "acme")
	t.Setenv("RALPH_PR__BITBUCKET_CLOUD__REPO_SLUG", "sso")
	t.Setenv(cfg.PR.BitbucketCloud.TokenEnv, "secret")
	if _, _, err := h.run("", "pr", "create", "--milestone", "M-1"); !errors.Is(err, errs.ErrNotImplemented) {
		t.Errorf("clean milestone with bitbucket_cloud: err = %v, want ErrNotImplemented", err)
	}
	newPRProvider = func(*config.Config) (pr.Provider, error) { return fake, nil }

	// Ahead by a manual commit.
	commit("manual.txt", "Manual tweak")
	refused("ahead", "M-1 branch ralph/login is 1 commit ahead of its recorded head "+commits[1][:12])

	// Diverged by a rebase onto updated main: refused, but the dry run
	// reports it with the task table.
	git("reset", "--quiet", "--hard", commits[1])
	git("switch", "--quiet", "main")
	commit("upstream.txt", "Upstream change")
	git("switch", "--quiet", "ralph/login")
	git("rebase", "--quiet", "main")
	rebased := git("rev-parse", "HEAD")
	refused("diverged", "M-1 has diverged from its recorded head "+commits[1][:12])
	out = h.ok("pr", "create", "--milestone", "M-1", "--dry-run")
	for _, want := range []string{
		"drift:    diverged (M-1 has diverged from its recorded head " + commits[1][:12] + ")\n",
		"TASK  MATCH    COMMIT        TITLE",
		"T-2   trailer  " + rebased[:12] + "  Logout",
		"Milestone M-1 of initiative I-1",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("diverged dry run lacks %q:\n%s", want, out)
		}
	}
	plan = dryRunJSON{}
	h.json(&plan, "pr", "create", "--milestone", "M-1", "--dry-run")
	if plan.Drift["state"] != "diverged" {
		t.Errorf("diverged JSON dry run drift = %v", plan.Drift)
	}

	// Unrecorded: refused until reconcile --accept.
	record(domain.MilestoneGit{})
	refused("unrecorded", "M-1 has no recorded head for branch ralph/login")
	record(recorded)

	// Merged into main.
	git("switch", "--quiet", "main")
	git("merge", "--quiet", "--no-ff", "-m", "Merge ralph/login", "ralph/login")
	refused("merged", "M-1 branch ralph/login is already merged into main")

	// A failing drift check fails pr create but only warns in a dry run.
	t.Setenv("RALPH_PR__BASE_BRANCH", "nope")
	_, _, err = h.run("", "pr", "create", "--milestone", "M-1")
	if err == nil || errors.Is(err, errs.ErrNeedsHuman) || !strings.Contains(err.Error(), "pr.base_branch") {
		t.Errorf("missing base branch: err = %v, want the drift check's error", err)
	}
	out, errOut, err := h.run("", "pr", "create", "--milestone", "M-1", "--dry-run")
	if err != nil {
		t.Fatalf("dry run with a failing drift check: %v", err)
	}
	if !strings.Contains(errOut, "warning: drift check failed: ") || !strings.Contains(errOut, "pr.base_branch") {
		t.Errorf("dry run warning:\n%s", errOut)
	}
	if !strings.Contains(out, "drift:    not checked\n") {
		t.Errorf("dry run output:\n%s", out)
	}
	var raw map[string]any
	h.json(&raw, "pr", "create", "--milestone", "M-1", "--dry-run")
	if raw["drift"] != nil || !strings.Contains(fmt.Sprint(raw["drift_error"]), "pr.base_branch") {
		t.Errorf("JSON dry run with a failing drift check: drift = %v, drift_error = %v", raw["drift"], raw["drift_error"])
	}
	if fake.created != 1 {
		t.Errorf("provider called %d times, want 1", fake.created)
	}
}

func (h *testEnv) runStreams(stdin string, terminal bool, args ...string) (string, string, error) {
	h.t.Helper()
	var out, errOut bytes.Buffer
	full := append([]string{"--project-dir", h.dir}, args...)
	err := Execute(context.Background(), Streams{In: strings.NewReader(stdin), Out: &out, Err: &errOut, IsTerminal: terminal}, full)
	return out.String(), errOut.String(), err
}

func (h *testEnv) exists(args ...string) bool {
	h.t.Helper()
	_, _, err := h.run("", args...)
	return err == nil
}

func deleteFixture(t *testing.T) *testEnv {
	t.Helper()
	h := newTestEnv(t, true)
	h.ok("initiative", "create", "--title", "Init", "--brief", "brief")
	h.ok("milestone", "create", "--initiative", "I-1", "--title", "Mile")
	h.ok("task", "create", "--milestone", "M-1", "--title", "One", "--description", "d", "--criteria", "c")
	h.ok("task", "create", "--milestone", "M-1", "--title", "Two", "--description", "d", "--criteria", "c")
	h.ok("task", "note", "T-1", "a note")
	return h
}

func TestDeleteTaskYes(t *testing.T) {
	h := deleteFixture(t)
	out := h.ok("task", "delete", "T-2", "--yes")
	if out != "deleted T-2\n" {
		t.Errorf("output = %q", out)
	}
	if h.exists("task", "show", "T-2") {
		t.Error("T-2 still exists")
	}
}

func TestDeleteConfirmation(t *testing.T) {
	h := deleteFixture(t)
	for _, in := range []string{"n\n", "", "maybe\n"} {
		out, errOut, err := h.runStreams(in, true, "task", "delete", "T-1")
		if err != nil || out != "cancelled\n" {
			t.Errorf("stdin %q: out=%q err=%v", in, out, err)
		}
		if !strings.Contains(errOut, "Delete T-1 with 1 note?") || !strings.Contains(errOut, "[y/N]") {
			t.Errorf("prompt = %q", errOut)
		}
		if !h.exists("task", "show", "T-1") {
			t.Fatalf("stdin %q deleted the task", in)
		}
	}
	out, _, err := h.runStreams("YES\n", true, "task", "delete", "T-1")
	if err != nil || out != "deleted T-1 (1 note)\n" {
		t.Errorf("out=%q err=%v", out, err)
	}
	if h.exists("task", "show", "T-1") {
		t.Error("T-1 still exists")
	}
}

func TestDeleteRefusesWithoutTerminal(t *testing.T) {
	h := deleteFixture(t)
	_, _, err := h.runStreams("y\n", false, "task", "delete", "T-2")
	if err == nil || !strings.Contains(err.Error(), "--yes") {
		t.Fatalf("err = %v", err)
	}
	if !h.exists("task", "show", "T-2") {
		t.Error("task deleted without confirmation")
	}
}

func TestDeleteMilestoneForce(t *testing.T) {
	h := deleteFixture(t)
	_, _, err := h.run("", "milestone", "delete", "M-1", "--yes")
	if !errors.Is(err, service.ErrNeedsForce) || !strings.Contains(err.Error(), "--force") {
		t.Fatalf("err = %v", err)
	}
	out := h.ok("milestone", "delete", "M-1", "--force", "--yes")
	if out != "deleted M-1 (2 tasks, 1 note)\n" {
		t.Errorf("output = %q", out)
	}
	if h.exists("milestone", "show", "M-1") {
		t.Error("M-1 still exists")
	}
}

func TestDeleteNoteAndDoc(t *testing.T) {
	h := deleteFixture(t)
	h.ok("doc", "create", "--kind", "reference", "--title", "Ref", "--body", "text")
	if out := h.ok("note", "delete", "N-1", "--yes"); out != "deleted N-1\n" {
		t.Errorf("output = %q", out)
	}
	if out := h.ok("doc", "delete", "D-1", "--yes"); out != "deleted D-1\n" {
		t.Errorf("output = %q", out)
	}
	if h.exists("doc", "show", "D-1") {
		t.Error("D-1 still exists")
	}
	if _, _, err := h.run("", "note", "delete", "T-1", "--yes"); err == nil {
		t.Error("note delete accepted a task ID")
	}
}

func TestDeleteJSON(t *testing.T) {
	h := deleteFixture(t)
	var v struct {
		ID      string
		Kind    string
		Deleted map[string]int
	}
	h.json(&v, "task", "delete", "T-2", "--yes")
	if v.ID != "T-2" || v.Kind != "task" || v.Deleted["tasks"] != 1 {
		t.Errorf("view = %+v", v)
	}
	out, errOut, err := h.runStreams("y\n", true, "--json", "task", "delete", "T-1")
	if err != nil || !strings.Contains(errOut, "[y/N]") || !strings.Contains(out, `"id": "T-1"`) {
		t.Errorf("out=%q errOut=%q err=%v", out, errOut, err)
	}
}

func TestShowArchivedState(t *testing.T) {
	h := newTestEnv(t, true)
	h.ok("initiative", "create", "--title", "Customer SSO")
	h.ok("milestone", "create", "--initiative", "I-1", "--title", "Callback")
	h.ok("task", "create", "--milestone", "M-1", "--title", "Route", "--description", "Add route", "--criteria", "Tests pass")
	h.ok("doc", "create", "--initiative", "I-1", "--kind", "design", "--title", "Scoped", "--body", "x")
	h.ok("doc", "create", "--kind", "design", "--title", "Wide", "--body", "y")
	shows := [][]string{
		{"initiative", "show", "I-1"}, {"milestone", "show", "M-1"}, {"task", "show", "T-1"},
		{"doc", "show", "D-1"}, {"doc", "show", "D-2"},
	}
	plain := make([]string, len(shows))
	for n, args := range shows {
		plain[n] = h.ok(args...)
		if strings.Contains(plain[n], "archived") {
			t.Errorf("ralph %v mentions archived with nothing archived:\n%s", args, plain[n])
		}
	}

	cfg, err := config.Defaults()
	if err != nil {
		t.Fatal(err)
	}
	cfg.Embeddings.Provider = config.EmbeddingsNone
	svc, closeFn, err := service.Open(context.Background(), cfg, h.dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer closeFn()
	at := time.Date(2026, 5, 6, 7, 8, 9, 0, time.UTC)
	if err := svc.Store().SetInitiativeArchived(context.Background(), 1, at); err != nil {
		t.Fatal(err)
	}
	if err := svc.Store().SetDocumentArchived(context.Background(), 2, at); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"archived: 2026-05-06T07:08:09Z\n",
		"archived: yes (initiative I-1)\n",
		"archived: yes (initiative I-1)\n",
		"archived: yes (initiative I-1)\n",
		"archived: 2026-05-06T07:08:09Z\n",
	}
	for n, args := range shows {
		got := h.ok(args...)
		if !strings.Contains(got, want[n]) {
			t.Errorf("ralph %v lacks %q:\n%s", args, want[n], got)
		}
		if strings.Replace(got, want[n], "", 1) != plain[n] {
			t.Errorf("ralph %v differs beyond the archived line:\n%s\nvs\n%s", args, got, plain[n])
		}
	}
}

func TestLoopAndPRRefuseArchivedInitiative(t *testing.T) {
	h := newTestEnv(t, true)
	h.ok("initiative", "create", "--title", "Customer SSO")
	h.ok("milestone", "create", "--initiative", "I-1", "--title", "Callback")
	h.ok("task", "create", "--milestone", "M-1", "--title", "Route", "--description", "Add route", "--criteria", "Tests pass")
	h.ok("task", "status", "T-1", "TODO")
	setArchived := func(on bool) {
		cfg, err := config.Defaults()
		if err != nil {
			t.Fatal(err)
		}
		cfg.Embeddings.Provider = config.EmbeddingsNone
		svc, closeFn, err := service.Open(context.Background(), cfg, h.dir, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer closeFn()
		if on {
			_, err = svc.ArchiveInitiative(context.Background(), 1)
		} else {
			_, err = svc.UnarchiveInitiative(context.Background(), 1)
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	setArchived(true)

	for _, args := range [][]string{
		{"loop", "--milestone", "M-1", "--dry-run"},
		{"pr", "create", "--milestone", "M-1", "--dry-run"},
	} {
		_, _, err := h.run("", args...)
		if err == nil || !strings.Contains(err.Error(), "I-1 is archived") || !strings.Contains(err.Error(), "ralph initiative unarchive I-1") {
			t.Errorf("ralph %v: err = %v; want archived refusal", args, err)
		}
	}
	setArchived(false)
	h.ok("loop", "--milestone", "M-1", "--dry-run")
}

func TestArchiveCommandsRoundTrip(t *testing.T) {
	h := newTestEnv(t, true)
	h.ok("initiative", "create", "--title", "Customer SSO")
	h.ok("milestone", "create", "--initiative", "I-1", "--title", "Callback")
	h.ok("task", "create", "--milestone", "M-1", "--title", "Route", "--description", "Add route", "--criteria", "Tests pass")
	h.ok("doc", "create", "--initiative", "I-1", "--kind", "design", "--title", "Scoped", "--body", "scoped")
	h.ok("doc", "create", "--kind", "design", "--title", "Wide", "--body", "wide")

	if got := h.ok("initiative", "archive", "I-1"); got != "archived I-1\n" {
		t.Errorf("initiative archive = %q", got)
	}
	var inits []service.InitiativeView
	h.json(&inits, "initiative", "list")
	if len(inits) != 0 {
		t.Errorf("initiative list shows archived: %+v", inits)
	}
	h.json(&inits, "initiative", "list", "--archived")
	if len(inits) != 1 || inits[0].ArchivedAt == "" {
		t.Errorf("initiative list --archived = %+v", inits)
	}
	var tasks []service.TaskView
	h.json(&tasks, "task", "list")
	if len(tasks) != 0 {
		t.Errorf("task list shows archived: %+v", tasks)
	}
	h.json(&tasks, "task", "list", "--archived")
	if len(tasks) != 1 || !tasks[0].Archived {
		t.Errorf("task list --archived = %+v", tasks)
	}
	if got := h.ok("milestone", "list"); strings.Contains(got, "M-1") {
		t.Errorf("milestone list shows archived:\n%s", got)
	}
	if got := h.ok("milestone", "list", "--archived"); !strings.Contains(got, "M-1") {
		t.Errorf("milestone list --archived lacks M-1:\n%s", got)
	}
	if got := h.ok("status"); strings.Contains(got, "I-1") {
		t.Errorf("status shows archived:\n%s", got)
	}
	if got := h.ok("status", "--archived"); !strings.Contains(got, "I-1") {
		t.Errorf("status --archived lacks I-1:\n%s", got)
	}
	if got := h.ok("search", "Route"); !strings.Contains(got, "no matches") {
		t.Errorf("search shows archived:\n%s", got)
	}
	if got := h.ok("search", "--archived", "Route"); !strings.Contains(got, "T-1") {
		t.Errorf("search --archived lacks T-1:\n%s", got)
	}
	var docs []service.DocumentView
	h.json(&docs, "doc", "list")
	if len(docs) != 1 || docs[0].ID != "D-2" {
		t.Errorf("doc list = %+v, want only the project-wide document", docs)
	}
	h.json(&docs, "doc", "list", "--archived")
	if len(docs) != 2 {
		t.Errorf("doc list --archived = %+v", docs)
	}

	// Unarchiving a document inside an archived initiative warns.
	out, errOut, err := h.run("", "doc", "unarchive", "D-1")
	if err != nil || out != "unarchived D-1\n" || !strings.Contains(errOut, "D-1 stays hidden until I-1 is unarchived") {
		t.Errorf("doc unarchive in archived initiative: out=%q err=%q %v", out, errOut, err)
	}

	var archived service.InitiativeView
	if got := h.ok("initiative", "unarchive", "I-1"); got != "unarchived I-1\n" {
		t.Errorf("initiative unarchive = %q", got)
	}
	h.json(&archived, "initiative", "show", "I-1")
	if archived.ArchivedAt != "" {
		t.Errorf("initiative still archived: %+v", archived)
	}
	h.json(&inits, "initiative", "list")
	if len(inits) != 1 {
		t.Errorf("initiative list after unarchive = %+v", inits)
	}

	var doc service.DocumentView
	h.json(&doc, "doc", "archive", "D-2")
	if doc.ArchivedAt == "" {
		t.Errorf("doc archive --json lacks archived_at: %+v", doc)
	}
	h.json(&docs, "doc", "list", "--project")
	if len(docs) != 0 {
		t.Errorf("archived document listed: %+v", docs)
	}
	out, errOut, err = h.run("", "doc", "unarchive", "D-2")
	if err != nil || out != "unarchived D-2\n" || errOut != "" {
		t.Errorf("doc unarchive: out=%q err=%q %v", out, errOut, err)
	}
	h.json(&docs, "doc", "list", "--project")
	if len(docs) != 1 {
		t.Errorf("document missing after unarchive: %+v", docs)
	}
}

func TestBranchFromCardAgreesAcrossCommands(t *testing.T) {
	h, _, _ := newGitTestEnv(t)
	h.ok("init")
	h.ok("initiative", "create", "--title", "SSO")
	h.ok("milestone", "create", "--initiative", "I-1", "--title", "Login")
	h.ok("task", "create", "--milestone", "M-1", "--title", "Route", "--description", "d", "--criteria", "c")
	cfgYAML := `task_manager:
  provider: jira
  jira:
    base_url: https://example.atlassian.net
    project_key: ABC
    email_env: JIRA_EMAIL
    token_env: JIRA_TOKEN
    issue_types: {initiative: Epic, milestone: Story, task: Task}
loop:
  branch_template: "{{.Milestone.CardKeyPrefix}}{{.Milestone.Slug}}"
`
	if err := os.WriteFile(filepath.Join(h.dir, ".ralph", "config.yaml"), []byte(cfgYAML), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Defaults()
	if err != nil {
		t.Fatal(err)
	}
	cfg.Embeddings.Provider = config.EmbeddingsNone
	svc, closeFn, err := service.Open(context.Background(), cfg, h.dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.RecordCard(context.Background(), domain.KindMilestone, 1, "jira", "ABC-1234", "", ""); err != nil {
		t.Fatal(err)
	}
	closeFn()

	newPRProvider = func(*config.Config) (pr.Provider, error) { return &fakeProvider{}, nil }
	t.Cleanup(func() { newPRProvider = pr.New })

	if out := h.ok("loop", "--milestone", "M-1", "--dry-run"); !strings.Contains(out, "branch:     ABC-1234-login (from ") {
		t.Errorf("loop dry run:\n%s", out)
	}
	if out := h.ok("pr", "create", "--milestone", "M-1", "--dry-run"); !strings.Contains(out, "branches: ABC-1234-login → ") {
		t.Errorf("pr dry run:\n%s", out)
	}
}
