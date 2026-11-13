package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bengearig/ralph/internal/config"
	"github.com/bengearig/ralph/internal/domain"
	"github.com/bengearig/ralph/internal/errs"
	"github.com/bengearig/ralph/internal/pr"
	"github.com/bengearig/ralph/internal/pr/bitbucketcloud"
	"github.com/bengearig/ralph/internal/project"
	"github.com/bengearig/ralph/internal/service"
	"github.com/bengearig/ralph/internal/store"
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
	bare := filepath.Join(t.TempDir(), "origin.git")
	run("init", "--quiet", "--bare", bare)
	run("remote", "add", "origin", bare)
	run("push", "--quiet", "origin", "main")

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
		policy := fmt.Sprintf("policy:     max 50 iterations, stall after 3 failures, commit per task: true, commit trailer: %t, commit card trailer: false\n", tc.want)
		if !strings.Contains(out, policy) {
			t.Errorf("RALPH_LOOP__COMMIT_TRAILER=%q: dry-run lacks %q:\n%s", tc.env, policy, out)
		}
		var plan map[string]any
		h.json(&plan, "loop", "--milestone", "M-1", "--dry-run")
		if got, ok := plan["commit_trailer"].(bool); !ok || got != tc.want {
			t.Errorf("RALPH_LOOP__COMMIT_TRAILER=%q: plan commit_trailer = %v", tc.env, plan["commit_trailer"])
		}
		if got, ok := plan["commit_card_trailer"].(bool); !ok || got {
			t.Errorf("plan commit_card_trailer = %v, want false", plan["commit_card_trailer"])
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

func TestMilestoneReconcileMergedDeletedBranch(t *testing.T) {
	h, git, commit := newGitTestEnv(t)
	base := git("rev-parse", "HEAD")

	h.ok("initiative", "create", "--title", "SSO")
	h.ok("milestone", "create", "--initiative", "I-1", "--title", "Login")
	h.ok("task", "create", "--milestone", "M-1", "--title", "Route", "--description", "d", "--criteria", "c")
	git("switch", "--quiet", "-c", "ralph/login")
	h.ok("task", "status", "T-1", "TODO")
	h.ok("task", "status", "T-1", "IN_PROGRESS")
	h.ok("task", "status", "T-1", "DONE", "--reason", "done")
	c1 := commit("T-1.txt", "T-1: work\n\nRalph-Task: T-1")

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
	if err := svc.RecordMilestoneGit(ctx, 1, domain.MilestoneGit{Branch: "ralph/login", BaseCommit: base, HeadCommit: c1}); err != nil {
		t.Fatal(err)
	}
	if err := svc.RecordTaskCommit(ctx, 1, c1); err != nil {
		t.Fatal(err)
	}

	git("switch", "--quiet", "main")
	git("merge", "--quiet", "--no-ff", "-m", "Merge", "ralph/login")
	git("branch", "--quiet", "-D", "ralph/login")

	out, _, err := h.run("", "milestone", "reconcile", "M-1")
	if !errors.Is(err, errs.ErrNeedsHuman) {
		t.Fatalf("err = %v, want ErrNeedsHuman", err)
	}
	for _, want := range []string{
		"M-1 branch ralph/login is already merged into main (branch deleted)",
		"state:    merged",
		"merged by: ancestry",
		"tip:      (branch does not exist locally)",
		"T-1   present  " + c1[:12] + "  Route",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("report lacks %q:\n%s", want, out)
		}
	}
}

func TestMilestoneReconcileSquashMerged(t *testing.T) {
	h, git, commit := newGitTestEnv(t)
	base := git("rev-parse", "HEAD")

	h.ok("initiative", "create", "--title", "SSO")
	h.ok("milestone", "create", "--initiative", "I-1", "--title", "Login")
	h.ok("task", "create", "--milestone", "M-1", "--title", "Route", "--description", "d", "--criteria", "c")
	git("switch", "--quiet", "-c", "ralph/login")
	h.ok("task", "status", "T-1", "TODO")
	h.ok("task", "status", "T-1", "IN_PROGRESS")
	h.ok("task", "status", "T-1", "DONE", "--reason", "done")
	c1 := commit("T-1.txt", "T-1: work\n\nRalph-Task: T-1")

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
	if err := svc.RecordMilestoneGit(context.Background(), 1, domain.MilestoneGit{Branch: "ralph/login", BaseCommit: base, HeadCommit: c1}); err != nil {
		t.Fatal(err)
	}

	git("switch", "--quiet", "main")
	git("merge", "--squash", "--quiet", "ralph/login")
	git("commit", "--quiet", "-m", "Login\n\nRalph-Task: T-1")

	out, _, err := h.run("", "milestone", "reconcile", "M-1")
	if !errors.Is(err, errs.ErrNeedsHuman) {
		t.Fatalf("err = %v, want ErrNeedsHuman", err)
	}
	for _, want := range []string{
		"M-1 branch ralph/login is already merged into main (squash or rebase merge)",
		"state:    merged",
		"merged by: tasks",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("report lacks %q:\n%s", want, out)
		}
	}
}

// fakeProvider is a pull request provider that counts and records calls.
type fakeProvider struct {
	created     int
	found       pr.PullRequest
	foundOK     bool
	findError   error
	createError error
	onCreate    func()
	finds       int
	onFind      func()
	findArgs    [2]string
	createReq   pr.Request
}

func (f *fakeProvider) Name() string    { return "fake" }
func (f *fakeProvider) Validate() error { return nil }
func (f *fakeProvider) Create(_ context.Context, req pr.Request) (pr.PullRequest, error) {
	f.created++
	f.createReq = req
	if f.onCreate != nil {
		f.onCreate()
	}
	if f.createError != nil {
		return pr.PullRequest{}, f.createError
	}
	return pr.PullRequest{ID: "1", Namespace: "acme", Repository: "sso", Number: 1, URL: "https://example.com/pr/1"}, nil
}

func (f *fakeProvider) FindOpen(_ context.Context, source, destination string) (pr.PullRequest, bool, error) {
	f.finds++
	if f.onFind != nil {
		f.onFind()
	}
	f.findArgs = [2]string{source, destination}
	return f.found, f.foundOK, f.findError
}

func TestPRCreateDriftGate(t *testing.T) {
	h, git, commit := newGitTestEnv(t)
	addBareRemote(t, git, "origin")
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

	// The real provider is reached on a clean milestone, against a fake server.
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			_, _ = w.Write([]byte(`{"values": []}`))
			return
		}
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id": 9, "links": {"html": {"href": "https://bitbucket.example/acme/sso/pull-requests/9"}}}`))
	}))
	defer srv.Close()
	newPRProvider = func(c *config.Config) (pr.Provider, error) {
		return bitbucketcloud.NewWithClient(c.PR.BitbucketCloud, c.PR.Timeout, os.LookupEnv, srv.Client(), srv.URL), nil
	}
	t.Setenv("RALPH_PR__BITBUCKET_CLOUD__WORKSPACE", "acme")
	t.Setenv("RALPH_PR__BITBUCKET_CLOUD__REPO_SLUG", "sso")
	t.Setenv(cfg.PR.BitbucketCloud.EmailEnv, "me@example.com")
	t.Setenv(cfg.PR.BitbucketCloud.TokenEnv, "secret")
	h.ok("pr", "unlink", "--milestone", "M-1")
	if help := h.ok("pr", "create", "--help"); strings.Contains(help, "not implemented") {
		t.Errorf("help still says creation is not implemented:\n%s", help)
	}
	if out := h.ok("pr", "create", "--milestone", "M-1"); out != "opened https://bitbucket.org/acme/sso/pull-requests/9\n" || gotPath != "/repositories/acme/sso/pullrequests" {
		t.Errorf("clean milestone with bitbucket_cloud: %q, path %q", out, gotPath)
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
	_, _, err = h.run("", "pr", "create", "--milestone", "M-1", "--force")
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
			_, err = svc.ArchiveInitiative(context.Background(), 1, service.DocumentChoice{})
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
	h.ok("doc", "archive", "D-1")

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

func importFixture(t *testing.T) *testEnv {
	t.Helper()
	h := newTestEnv(t, true)
	for name, body := range map[string]string{
		"docs/a.md":     "# A\n",
		"docs/b.md":     "# B\n",
		"docs/blob.bin": "x\x00y",
	} {
		p := filepath.Join(h.dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Chdir(h.dir)
	return h
}

func TestImportDir(t *testing.T) {
	h := importFixture(t)

	out := h.ok("import", "--dir", "docs")
	for _, want := range []string{
		"new (2):\n  docs/a.md\n  docs/b.md\n",
		"skipped (1):\n  docs/blob.bin: not text (contains a NUL byte)\n",
		"next: run /ralph:import (Claude Code) or the ralph MCP import prompt to integrate 2 queued file(s)\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}

	out = h.ok("import", "--dir", "docs")
	if !strings.Contains(out, "already queued (2):") || strings.Contains(out, "new (") {
		t.Errorf("second run:\n%s", out)
	}

	if err := os.WriteFile(filepath.Join(h.dir, "docs", "a.md"), []byte("# A2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out = h.ok("import", "--file", "docs/a.md")
	if !strings.Contains(out, "changed (1):\n  docs/a.md\n") {
		t.Errorf("after edit:\n%s", out)
	}
}

func TestImportJSON(t *testing.T) {
	h := importFixture(t)
	var res struct {
		Queued   []service.ImportOutcome `json:"queued"`
		Skipped  []struct{ Path, Reason string }
		Warnings []string `json:"warnings"`
	}
	h.json(&res, "import", "--dir", "docs", "--file", "missing.md")
	if len(res.Queued) != 2 || res.Queued[0] != (service.ImportOutcome{Path: "docs/a.md", Outcome: "new"}) {
		t.Errorf("queued = %+v", res.Queued)
	}
	if len(res.Skipped) != 2 || res.Skipped[0].Path != "docs/blob.bin" || res.Skipped[1].Reason != "not found" {
		t.Errorf("skipped = %+v", res.Skipped)
	}
	if len(res.Warnings) != 1 {
		t.Errorf("warnings = %v", res.Warnings)
	}
	raw := h.ok("--json", "import", "--file", "docs/a.md")
	if strings.Contains(raw, "null") {
		t.Errorf("null in JSON:\n%s", raw)
	}
}

func TestImportNeedsInput(t *testing.T) {
	h := importFixture(t)
	if _, _, err := h.run("", "import"); err == nil {
		t.Error("expected an error with no --dir or --file")
	}
}

func TestImportAsNew(t *testing.T) {
	h := importFixture(t)
	out := h.ok("import", "--as-new", "--file", "docs/a.md", "--file", "docs/b.md")
	if !strings.Contains(out, "created (2):\n  docs/a.md -> D-1\n  docs/b.md -> D-2\n") {
		t.Errorf("out:\n%s", out)
	}
	var doc struct{ Title, Kind string }
	h.json(&doc, "doc", "show", "D-1")
	if doc.Title != "A" || doc.Kind != "reference" {
		t.Errorf("doc = %+v", doc)
	}

	out = h.ok("import", "--as-new", "--file", "docs/a.md", "--dir", "docs")
	if !strings.Contains(out, "skipped (2):\n  docs/a.md: already imported\n") || !strings.Contains(out, "not imported (1):\n  docs/blob.bin: not text") {
		t.Errorf("rerun:\n%s", out)
	}

	if err := os.WriteFile(filepath.Join(h.dir, "docs", "a.md"), []byte("# A2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	stdout, _, err := h.run("", "import", "--as-new", "--file", "docs/a.md")
	if !errors.Is(err, errs.ErrNeedsHuman) || !strings.Contains(stdout, "refused (1):\n  docs/a.md: changed since its last import") {
		t.Errorf("refused: err=%v\n%s", err, stdout)
	}

	var res struct {
		Imported []service.AsNewOutcome `json:"imported"`
	}
	h.json(&res, "import", "--as-new", "--again", "--kind", "design", "--file", "docs/a.md")
	if len(res.Imported) != 1 || res.Imported[0].Outcome != "created" || res.Imported[0].Document != "D-3" {
		t.Errorf("again = %+v", res.Imported)
	}
}

func TestImportAsNewOptions(t *testing.T) {
	h := importFixture(t)
	h.ok("initiative", "create", "--title", "Docs")
	h.ok("import", "--as-new", "--kind", "design", "--initiative", "I-1", "--file", "docs/a.md")
	var doc struct {
		Kind         string
		InitiativeID string `json:"initiative_id"`
	}
	h.json(&doc, "doc", "show", "D-1")
	if doc.Kind != "design" || doc.InitiativeID != "I-1" {
		t.Errorf("doc = %+v", doc)
	}
	for _, args := range [][]string{
		{"--kind", "bogus"}, {"--initiative", "I-9"}, {"--initiative", "nope"},
	} {
		all := append([]string{"import", "--as-new", "--file", "docs/b.md"}, args...)
		if _, _, err := h.run("", all...); err == nil {
			t.Errorf("%v accepted", args)
		}
	}
	if h.exists("doc", "show", "D-2") {
		t.Error("a document was created despite invalid options")
	}
	if _, _, err := h.run("", "import", "--file", "docs/b.md", "--again"); err == nil {
		t.Error("--again without --as-new accepted")
	}
}

func TestDocPromoteAndReturnCommands(t *testing.T) {
	h := newTestEnv(t, true)
	h.ok("initiative", "create", "--title", "SSO")
	h.ok("initiative", "create", "--title", "Billing")
	h.ok("doc", "create", "--initiative", "I-1", "--kind", "design", "--title", "Layout")
	h.ok("doc", "create", "--initiative", "I-2", "--kind", "design", "--title", "Layout")

	if got := h.ok("doc", "promote", "D-1"); got != "promoted D-1 to project-wide (from I-1)\n" {
		t.Errorf("doc promote = %q", got)
	}
	if got := h.ok("doc", "show", "D-1"); !strings.Contains(got, "from I-1") {
		t.Errorf("doc show lacks origin: %q", got)
	}
	if got := h.ok("doc", "list", "--project"); !strings.Contains(got, "from I-1") {
		t.Errorf("doc list lacks origin: %q", got)
	}

	_, _, err := h.run("", "doc", "promote", "D-2")
	if err == nil || !strings.Contains(err.Error(), "D-1") {
		t.Errorf("promote clash error = %v, want it to name D-1", err)
	}
	_, _, err = h.run("", "doc", "promote", "D-2", "--slug", "Not A Slug")
	if err == nil {
		t.Error("invalid --slug accepted")
	}
	if got := h.ok("doc", "promote", "D-2", "--slug", "billing-layout"); got != "promoted D-2 to project-wide (from I-2)\n" {
		t.Errorf("doc promote --slug = %q", got)
	}

	_, _, err = h.run("", "doc", "promote", "D-1")
	if err == nil || !strings.Contains(err.Error(), "already project-wide") {
		t.Errorf("promote of project-wide document error = %v", err)
	}

	h.ok("doc", "create", "--initiative", "I-1", "--kind", "design", "--title", "Layout")
	_, _, err = h.run("", "doc", "return", "D-1")
	if err == nil || !strings.Contains(err.Error(), "D-3") {
		t.Errorf("return clash error = %v, want it to name D-3", err)
	}
	if got := h.ok("doc", "return", "D-1", "--slug", "sso-layout"); got != "returned D-1 to I-1\n" {
		t.Errorf("doc return --slug = %q", got)
	}
	var v service.DocumentView
	h.json(&v, "doc", "return", "D-2")
	if v.InitiativeID != "I-2" || v.OriginInitiativeID != "" {
		t.Errorf("doc return --json = %+v", v)
	}
	v = service.DocumentView{}
	h.json(&v, "doc", "promote", "D-2")
	if v.InitiativeID != "" || v.OriginInitiativeID != "I-2" {
		t.Errorf("doc promote --json = %+v", v)
	}

	h.ok("initiative", "archive", "I-2")
	_, _, err = h.run("", "doc", "return", "D-2")
	if err == nil || !strings.Contains(err.Error(), "I-2") || !strings.Contains(err.Error(), "unarchive") {
		t.Errorf("return to archived origin error = %v", err)
	}
}

func TestInitiativeArchivePromoteChoice(t *testing.T) {
	seed := func(t *testing.T) *testEnv {
		h := newTestEnv(t, true)
		h.ok("initiative", "create", "--title", "Customer SSO")
		h.ok("doc", "create", "--initiative", "I-1", "--kind", "design", "--title", "Flow", "--body", "x")
		h.ok("doc", "create", "--initiative", "I-1", "--kind", "reference", "--title", "Notes", "--body", "y")
		return h
	}

	h := seed(t)
	_, _, err := h.run("", "initiative", "archive", "I-1")
	if !errors.Is(err, errs.ErrNeedsHuman) {
		t.Fatalf("err = %v, want ErrNeedsHuman", err)
	}
	for _, want := range []string{`D-1  design  "Flow"`, `D-2  reference  "Notes"`, "ralph initiative archive I-1 --promote D-1\n", "--promote all", "--promote none"} {
		if !strings.Contains(err.Error()+"\n", want) {
			t.Errorf("refusal lacks %q:\n%v", want, err)
		}
	}
	var inits []service.InitiativeView
	h.json(&inits, "initiative", "list")
	if len(inits) != 1 {
		t.Errorf("initiative archived despite refusal: %+v", inits)
	}
	if _, _, err := h.run("", "initiative", "archive", "I-1", "--json"); !errors.Is(err, errs.ErrNeedsHuman) {
		t.Errorf("--json refusal err = %v", err)
	}
	if _, _, err := h.run("", "initiative", "archive", "I-1", "--promote", "all,D-1"); !errors.Is(err, domain.ErrInvalid) {
		t.Errorf("mixed promote err = %v, want ErrInvalid", err)
	}

	for _, tc := range []struct{ flag, want string }{
		{"all", "archived I-1; promoted D-1, D-2\n"},
		{"none", "archived I-1\n"},
		{"D-1,D-2", "archived I-1; promoted D-1, D-2\n"},
	} {
		h := seed(t)
		if got := h.ok("initiative", "archive", "I-1", "--promote", tc.flag); got != tc.want {
			t.Errorf("--promote %s = %q, want %q", tc.flag, got, tc.want)
		}
	}

	h = seed(t)
	var v service.InitiativeView
	h.json(&v, "initiative", "archive", "I-1", "--promote", "D-2")
	if !slices.Equal(v.PromotedDocuments, []string{"D-2"}) {
		t.Errorf("json promoted = %v", v.PromotedDocuments)
	}
	if got := h.ok("initiative", "archive", "I-1"); got != "archived I-1\n" {
		t.Errorf("already archived = %q", got)
	}

	h = newTestEnv(t, true)
	h.ok("initiative", "create", "--title", "Bare")
	if got := h.ok("initiative", "archive", "I-1"); got != "archived I-1\n" {
		t.Errorf("no documents = %q", got)
	}
}

func TestPRUnlink(t *testing.T) {
	h := newTestEnv(t, true)
	t.Setenv("RALPH_PR__PROVIDER", "bitbucket_cloud")
	h.ok("initiative", "create", "--title", "Customer SSO")
	h.ok("milestone", "create", "--initiative", "I-1", "--title", "Callback")
	withService := func(f func(svc *service.Service)) {
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
		svc.SetPullRequestLink(pr.Link)
		f(svc)
	}
	record := func(rec domain.PullRequestRecord) {
		withService(func(svc *service.Service) {
			if err := svc.RecordMilestonePullRequest(context.Background(), 1, rec); err != nil {
				t.Fatal(err)
			}
		})
	}
	bb := domain.PullRequestRecord{Provider: "bitbucket_cloud", Namespace: "acme", Repository: "app", Number: 7}
	const url = "https://bitbucket.org/acme/app/pull-requests/7"

	record(bb)
	if out := h.ok("pr", "unlink", "--milestone", "M-1"); out != "unlinked "+url+"\n" {
		t.Errorf("unlink = %q", out)
	}
	var shown struct {
		PullRequest *service.PullRequestView `json:"pull_request"`
	}
	h.json(&shown, "milestone", "show", "M-1")
	if shown.PullRequest != nil {
		t.Errorf("pull request still recorded: %+v", shown.PullRequest)
	}
	if out := h.ok("milestone", "show", "M-1"); strings.Contains(out, "pull request:") {
		t.Errorf("milestone show still has a pull request line:\n%s", out)
	}
	if out := h.ok("pr", "unlink", "--milestone", "M-1"); out != "nothing to unlink\n" {
		t.Errorf("second unlink = %q", out)
	}

	record(bb)
	var res struct {
		Milestone string                   `json:"milestone"`
		Unlinked  *service.PullRequestView `json:"unlinked"`
	}
	h.json(&res, "pr", "unlink", "--milestone", "M-1")
	want := service.PullRequestView{Provider: "bitbucket_cloud", Namespace: "acme", Repository: "app", Number: 7, URL: url}
	if res.Milestone != "M-1" || res.Unlinked == nil || *res.Unlinked != want {
		t.Errorf("json = %+v", res)
	}
	if out := h.ok("--json", "pr", "unlink", "--milestone", "M-1"); !strings.Contains(out, `"unlinked": null`) {
		t.Errorf("second json = %q", out)
	}

	record(domain.PullRequestRecord{Provider: "other", Namespace: "ns", Repository: "repo", Number: 3})
	if out := h.ok("pr", "unlink", "--milestone", "M-1"); out != "unlinked other ns/repo#3\n" {
		t.Errorf("no-url unlink = %q", out)
	}

	if _, _, err := h.run("", "pr", "unlink"); err == nil {
		t.Error("missing --milestone: want error")
	}
	if _, _, err := h.run("", "pr", "unlink", "--milestone", "M-9"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("unknown milestone err = %v; want not found", err)
	}

	record(bb)
	withService(func(svc *service.Service) {
		if _, err := svc.ArchiveInitiative(context.Background(), 1, service.DocumentChoice{}); err != nil {
			t.Fatal(err)
		}
	})
	if _, _, err := h.run("", "pr", "unlink", "--milestone", "M-1"); !errors.Is(err, service.ErrArchived) {
		t.Errorf("archived err = %v; want ErrArchived", err)
	}
}

func TestPullRequestDisplay(t *testing.T) {
	h := newTestEnv(t, true)
	h.ok("initiative", "create", "--title", "Customer SSO")
	h.ok("milestone", "create", "--initiative", "I-1", "--title", "Callback")
	h.ok("milestone", "create", "--initiative", "I-1", "--title", "Logout")
	record := func(id int64, rec domain.PullRequestRecord) {
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
		if err := svc.RecordMilestonePullRequest(context.Background(), id, rec); err != nil {
			t.Fatal(err)
		}
	}

	if out := h.ok("milestone", "show", "M-1"); strings.Contains(out, "pull request:") {
		t.Errorf("show without record = %q", out)
	}
	if out := h.ok("status"); strings.Contains(out, "PR") {
		t.Errorf("status without record has PR column: %q", out)
	}

	record(1, domain.PullRequestRecord{Provider: "bitbucket_cloud", Namespace: "acme", Repository: "app", Number: 7})
	if out := h.ok("milestone", "show", "M-1"); !strings.Contains(out, "pull request: https://bitbucket.org/acme/app/pull-requests/7\n") {
		t.Errorf("show = %q", out)
	}
	if out := h.ok("milestone", "show", "M-2"); strings.Contains(out, "pull request:") {
		t.Errorf("show M-2 = %q", out)
	}
	out := h.ok("status")
	if !strings.Contains(out, "PR") || !strings.Contains(out, "https://bitbucket.org/acme/app/pull-requests/7") {
		t.Errorf("status = %q", out)
	}
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "M-2") && !strings.HasSuffix(strings.TrimSpace(line), "-") {
			t.Errorf("M-2 row lacks '-': %q", line)
		}
	}
	if out := h.ok("milestone", "list"); strings.Contains(out, "PR") {
		t.Errorf("milestone list changed: %q", out)
	}
	if out := h.ok("--json", "status"); !strings.Contains(out, `"pull_request"`) {
		t.Errorf("status json = %q", out)
	}

	record(1, domain.PullRequestRecord{Provider: "other", Namespace: "ns", Repository: "repo", Number: 3})
	if out := h.ok("milestone", "show", "M-1"); !strings.Contains(out, "pull request: other ns/repo#3\n") {
		t.Errorf("no-url show = %q", out)
	}
}

func dirFiles(t *testing.T, dir string) []string {
	t.Helper()
	var files []string
	err := filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			rel, _ := filepath.Rel(dir, p)
			files = append(files, rel)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return files
}

type initResult struct {
	Root    string
	DryRun  bool `json:"dry_run"`
	Changes []struct{ Path, Action, Note string }
}

func (r initResult) actions() map[string]string {
	m := map[string]string{}
	for _, c := range r.Changes {
		m[c.Path] = c.Action
	}
	return m
}

func TestInitDryRun(t *testing.T) {
	t.Run("empty repository", func(t *testing.T) {
		h := newTestEnv(t, false)
		if err := os.Mkdir(filepath.Join(h.dir, ".git"), 0o755); err != nil {
			t.Fatal(err)
		}
		before := dirFiles(t, h.dir)

		out := h.ok("init", "--dry-run")
		if !strings.HasPrefix(out, "dry run: nothing was written\n\n") || strings.Contains(out, "initialized ralph") {
			t.Errorf("output:\n%s", out)
		}
		for _, col := range []string{"ACTION", "PATH", "NOTE"} {
			if !strings.Contains(out, col) {
				t.Errorf("table lacks %s:\n%s", col, out)
			}
		}
		if after := dirFiles(t, h.dir); !slices.Equal(before, after) {
			t.Errorf("dry run wrote files: before %v, after %v", before, after)
		}
		for _, p := range []string{".ralph", ".mcp.json", ".claude", "CLAUDE.md", ".gitignore"} {
			if _, err := os.Stat(filepath.Join(h.dir, p)); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("%s exists after a dry run: %v", p, err)
			}
		}

		var dry, real initResult
		h.json(&dry, "init", "--dry-run")
		h.json(&real, "init")
		if !dry.DryRun || real.DryRun {
			t.Errorf("dry_run: dry %v, real %v", dry.DryRun, real.DryRun)
		}
		if len(dry.Changes) == 0 || !reflect.DeepEqual(dry.Changes, real.Changes) {
			t.Errorf("dry run changes differ from the real run:\n dry  %+v\n real %+v", dry.Changes, real.Changes)
		}
		for _, c := range real.Changes {
			if c.Action == "unchanged" {
				t.Errorf("fresh repository reported %s unchanged", c.Path)
			}
		}
	})

	t.Run("initialized project", func(t *testing.T) {
		h := newTestEnv(t, false)
		h.ok("init", "--ralph-command", "ralph")
		receive := filepath.Join(h.dir, ".claude", "commands", "ralph", "receive.md")
		status := filepath.Join(h.dir, ".claude", "commands", "ralph", "status.md")
		earlier, err := os.ReadFile(filepath.Join("testdata", "receive.earlier.md"))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(receive, earlier, 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(status, []byte("my own status\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		snapshot := func() map[string]string {
			m := map[string]string{}
			for _, f := range dirFiles(t, h.dir) {
				if strings.HasPrefix(f, "xdg") || strings.HasPrefix(f, ".ralph/ralph.db") {
					continue
				}
				data, _ := os.ReadFile(filepath.Join(h.dir, f))
				m[f] = string(data)
			}
			return m
		}
		before := snapshot()
		rel := func(name string) string { return filepath.Join(".claude", "commands", "ralph", name) }

		var plain, forced, noHarness initResult
		h.json(&plain, "init", "--dry-run")
		if a := plain.actions(); a[rel("receive.md")] != "updated" || a[rel("status.md")] != "skipped" {
			t.Errorf("plain dry run actions: %v", a)
		}
		h.json(&forced, "init", "--dry-run", "--force")
		if a := forced.actions(); a[rel("status.md")] != "updated" {
			t.Errorf("forced dry run actions: %v", a)
		}
		h.json(&noHarness, "init", "--dry-run", "--no-harness")
		for p := range noHarness.actions() {
			if strings.HasPrefix(p, ".claude") || p == ".mcp.json" || p == "CLAUDE.md" {
				t.Errorf("--no-harness reported %s", p)
			}
		}
		if !reflect.DeepEqual(before, snapshot()) {
			t.Error("dry runs changed the project")
		}

		var real initResult
		h.json(&real, "init")
		if a := real.actions(); a[rel("receive.md")] != "updated" || a[rel("status.md")] != "skipped" {
			t.Errorf("real run after dry runs: %v", a)
		}
	})

	t.Run("no ollama request", func(t *testing.T) {
		h := newTestEnv(t, false)
		var requests atomic.Int32
		srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
			requests.Add(1)
		}))
		defer srv.Close()
		t.Setenv("RALPH_EMBEDDINGS__PROVIDER", "ollama")
		t.Setenv("RALPH_EMBEDDINGS__OLLAMA__BASE_URL", srv.URL)

		var res initResult
		h.json(&res, "init", "--dry-run", "--no-harness")
		if !res.DryRun || len(res.Changes) == 0 {
			t.Errorf("result = %+v", res)
		}
		if n := requests.Load(); n != 0 {
			t.Errorf("dry run made %d Ollama requests", n)
		}
		h.ok("init", "--no-harness")
		if requests.Load() == 0 {
			t.Error("the fake Ollama server is not reached by a real init, so the check above proves nothing")
		}
	})
}

// installHarness runs ralph init so the integration files exist.
func (h *testEnv) installHarness() {
	h.t.Helper()
	h.ok("init")
}

func (h *testEnv) write(rel, content string) {
	h.t.Helper()
	p := filepath.Join(h.dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		h.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		h.t.Fatal(err)
	}
}

func (h *testEnv) snapshot() map[string]string {
	h.t.Helper()
	snap := map[string]string{}
	err := filepath.WalkDir(h.dir, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || strings.HasSuffix(p, ".db") || strings.Contains(p, ".db-") {
			return err
		}
		data, err := os.ReadFile(p)
		snap[p] = string(data)
		return err
	})
	if err != nil {
		h.t.Fatal(err)
	}
	return snap
}
