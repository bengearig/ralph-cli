package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bengearig/ralph/internal/domain"
	"github.com/bengearig/ralph/internal/errs"
	"github.com/bengearig/ralph/internal/project"
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
	h.json(&i, "initiative", "create", "--title", "Customer SSO", "--brief", "Okta login for customers")
	if i.ID != "I-1" {
		t.Fatalf("initiative id = %q", i.ID)
	}
	h.ok("milestone", "create", "--initiative", "I-1", "--title", "Callback")
	h.ok("task", "create", "--milestone", "M-1", "--title", "Route", "--description", "Add route", "--criteria", "Tests pass")
	h.ok("task", "status", "T-1", "todo", "--reason", "articulated")

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
	var cfg struct{ Search struct{ Limit int } }
	h.json(&cfg, "config", "show")
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
