package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bengearig/ralph/internal/domain"
	"github.com/bengearig/ralph/internal/project"
)

// harness runs ralph commands against a temporary project with embeddings
// disabled, isolated from the user's global config.
type harness struct {
	t   *testing.T
	dir string
}

func newHarness(t *testing.T, initProject bool) *harness {
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
	return &harness{t: t, dir: dir}
}

func (h *harness) run(stdin string, args ...string) (string, string, error) {
	h.t.Helper()
	var out, errOut bytes.Buffer
	full := append([]string{"--project-dir", h.dir}, args...)
	err := Execute(context.Background(), Streams{In: strings.NewReader(stdin), Out: &out, Err: &errOut}, full)
	return out.String(), errOut.String(), err
}

func (h *harness) ok(args ...string) string {
	h.t.Helper()
	out, errOut, err := h.run("", args...)
	if err != nil {
		h.t.Fatalf("ralph %s: %v\nstderr: %s", strings.Join(args, " "), err, errOut)
	}
	return out
}

func (h *harness) json(v any, args ...string) {
	h.t.Helper()
	out := h.ok(append([]string{"--json"}, args...)...)
	if err := json.Unmarshal([]byte(out), v); err != nil {
		h.t.Fatalf("decoding %q: %v", out, err)
	}
}

func TestWorkflowThroughCLI(t *testing.T) {
	h := newHarness(t, true)
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
	h := newHarness(t, true)
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
	h := newHarness(t, false)
	_, _, err := h.run("", "status")
	if !errors.Is(err, project.ErrNotFound) {
		t.Errorf("err = %v", err)
	}
	if out := h.ok("config", "validate"); !strings.Contains(out, "valid") {
		t.Errorf("config validate outside a project: %s", out)
	}
}

func TestConfigLayersVisible(t *testing.T) {
	h := newHarness(t, true)
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
