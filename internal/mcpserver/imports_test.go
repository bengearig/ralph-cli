package mcpserver

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/bengearig/ralph/internal/service"
)

func importFixture(t *testing.T) (*mcp.ClientSession, string) {
	t.Helper()
	root := t.TempDir()
	files := map[string]string{
		"docs/a.md":   "# Alpha\n\nfirst\n",
		"docs/b.txt":  "second\n",
		"docs/bin.db": "x\x00y",
	}
	for name, body := range files {
		full := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return connectWith(t, Options{ProjectRoot: root, ImportMaxFileBytes: 1024}), root
}

func queueDocs(t *testing.T, cs *mcp.ClientSession) importQueueOut {
	t.Helper()
	var out importQueueOut
	mustOK(t, cs, "import_queue", map[string]any{"paths": []string{"docs"}}, &out)
	return out
}

func TestImportQueueListGet(t *testing.T) {
	cs, _ := importFixture(t)
	out := queueDocs(t, cs)
	if len(out.Queued) != 2 || out.Queued[0].Path != "docs/a.md" || out.Queued[0].Outcome != service.OutcomeNew {
		t.Fatalf("queued = %+v", out.Queued)
	}
	if len(out.Skipped) != 1 || out.Skipped[0].Path != "docs/bin.db" || !strings.Contains(out.Skipped[0].Reason, "not text") {
		t.Fatalf("skipped = %+v", out.Skipped)
	}

	var list importList
	mustOK(t, cs, "import_list", map[string]any{"statuses": []string{"pending"}}, &list)
	if len(list.Imports) != 2 {
		t.Fatalf("list = %+v", list.Imports)
	}
	for _, im := range list.Imports {
		if im.Content != "" || im.Status != "pending" {
			t.Errorf("list entry %+v has content or wrong status", im)
		}
	}
	mustOK(t, cs, "import_list", map[string]any{"statuses": []string{"done"}}, &list)
	if len(list.Imports) != 0 {
		t.Errorf("done list = %+v", list.Imports)
	}

	var got service.ImportView
	mustOK(t, cs, "import_get", map[string]any{"path": "docs/a.md"}, &got)
	if got.Content != "# Alpha\n\nfirst\n" {
		t.Errorf("content = %q", got.Content)
	}
	if res := call(t, cs, "import_get", map[string]any{"path": "docs/none.md"}, nil); !res.IsError {
		t.Error("import_get on a missing record did not fail")
	}
}

func TestImportResolve(t *testing.T) {
	cs, _ := importFixture(t)
	queueDocs(t, cs)
	var doc service.DocumentView
	mustOK(t, cs, "doc_create", map[string]any{"kind": "reference", "title": "Alpha"}, &doc)

	var got service.ImportView
	mustOK(t, cs, "import_resolve", map[string]any{
		"path": "docs/a.md", "status": "done",
		"links": []map[string]any{{"document_id": doc.ID, "relation": "created"}},
	}, &got)
	if got.Status != "done" {
		t.Errorf("status = %q", got.Status)
	}
	got = service.ImportView{}
	mustOK(t, cs, "import_get", map[string]any{"path": "docs/a.md"}, &got)
	if len(got.Links) != 1 || got.Links[0].Document != doc.ID || got.Links[0].Relation != "created" {
		t.Errorf("links = %+v", got.Links)
	}

	res := call(t, cs, "import_resolve", map[string]any{"path": "docs/b.txt", "status": "deferred"}, nil)
	if !res.IsError || !strings.Contains(text(res), "note") {
		t.Errorf("deferred without note: IsError=%v %q", res.IsError, text(res))
	}
	mustOK(t, cs, "import_resolve", map[string]any{"path": "docs/b.txt", "status": "deferred", "note": "contradicts D-1"}, nil)
}

func TestImportQueueAsNew(t *testing.T) {
	cs, _ := importFixture(t)
	var out importQueueOut
	mustOK(t, cs, "import_queue", map[string]any{"paths": []string{"docs"}, "as_new": true}, &out)
	if len(out.Imported) != 2 || len(out.Queued) != 0 {
		t.Fatalf("out = %+v", out)
	}
	for _, o := range out.Imported {
		if o.Outcome != service.AsNewCreated || o.Document == "" {
			t.Errorf("outcome %+v", o)
		}
		var doc service.DocumentView
		mustOK(t, cs, "doc_get", map[string]any{"id": o.Document}, &doc)
		if doc.Kind != "reference" {
			t.Errorf("kind = %q", doc.Kind)
		}
	}
	if res := call(t, cs, "import_queue", map[string]any{"paths": []string{"docs"}, "kind": "design"}, nil); !res.IsError {
		t.Error("kind without as_new did not fail")
	}
}

func TestImportQueueSkipsBadPaths(t *testing.T) {
	cs, root := importFixture(t)
	outside := filepath.Join(filepath.Dir(root), "outside-"+filepath.Base(root)+".md")
	if err := os.WriteFile(outside, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Remove(outside) })

	var out importQueueOut
	mustOK(t, cs, "import_queue", map[string]any{"paths": []string{"../" + filepath.Base(outside), "docs/missing.md", "docs/b.txt"}}, &out)
	if len(out.Queued) != 1 || out.Queued[0].Path != "docs/b.txt" {
		t.Errorf("queued = %+v", out.Queued)
	}
	want := map[string]string{
		"../" + filepath.Base(outside): "outside the project",
		"docs/missing.md":              "not found",
	}
	if len(out.Skipped) != len(want) {
		t.Fatalf("skipped = %+v", out.Skipped)
	}
	for _, s := range out.Skipped {
		if want[s.Path] != s.Reason {
			t.Errorf("skipped %+v, want reason %q", s, want[s.Path])
		}
	}
}

func TestImportToolDescriptionsStateWorkflow(t *testing.T) {
	cs, _ := importFixture(t)
	res, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	extra := map[string][]string{
		"import_queue":   {"as_new", "fast path", "no integration"},
		"import_resolve": {"deferred", "open contradictions"},
	}
	seen := 0
	for _, tool := range res.Tools {
		if !strings.HasPrefix(tool.Name, "import_") {
			continue
		}
		seen++
		for _, sub := range append([]string{"import_queue", "import_get", "integrate", "import_resolve", "source of truth"}, extra[tool.Name]...) {
			if !strings.Contains(tool.Description, sub) {
				t.Errorf("%s description lacks %q", tool.Name, sub)
			}
		}
	}
	if seen != 4 {
		t.Errorf("saw %d import tools", seen)
	}
}
