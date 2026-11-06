package mcpserver

import (
	"context"
	"encoding/json"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/bengearig/ralph/internal/embed/fake"
	"github.com/bengearig/ralph/internal/search"
	"github.com/bengearig/ralph/internal/service"
	"github.com/bengearig/ralph/internal/store/sqlite"
)

func connect(t *testing.T) *mcp.ClientSession {
	t.Helper()
	ctx := context.Background()
	st, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "r.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	ix := &search.Index{Store: st, Embedder: fake.New(), Chunker: search.Chunker{MaxChars: 1000, OverlapChars: 100}}
	svc := service.New(st, ix, service.Options{StallThreshold: 3})

	srvT, cliT := mcp.NewInMemoryTransports()
	ss, err := New(svc, "test", nil).Connect(ctx, srvT, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ss.Close() })
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil).Connect(ctx, cliT, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cs.Close() })
	return cs
}

// call invokes a tool and decodes its structured result into out. It fails
// the test on protocol errors but returns tool errors for inspection.
func call(t *testing.T, cs *mcp.ClientSession, name string, args map[string]any, out any) *mcp.CallToolResult {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s: protocol error: %v", name, err)
	}
	if !res.IsError && out != nil {
		raw, err := json.Marshal(res.StructuredContent)
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(raw, out); err != nil {
			t.Fatalf("%s: decoding %s: %v", name, raw, err)
		}
	}
	return res
}

func mustOK(t *testing.T, cs *mcp.ClientSession, name string, args map[string]any, out any) {
	t.Helper()
	if res := call(t, cs, name, args, out); res.IsError {
		t.Fatalf("%s(%v) failed: %s", name, args, text(res))
	}
}

func text(res *mcp.CallToolResult) string {
	var parts []string
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			parts = append(parts, tc.Text)
		}
	}
	return strings.Join(parts, "\n")
}

func TestToolsAreListedWithSchemas(t *testing.T) {
	cs := connect(t)
	res, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tool := range res.Tools {
		names = append(names, tool.Name)
		if tool.Description == "" || tool.InputSchema == nil || tool.OutputSchema == nil {
			t.Errorf("tool %s lacks description or schemas", tool.Name)
		}
		if tool.Annotations == nil || tool.Annotations.Title == "" {
			t.Errorf("tool %s lacks annotations", tool.Name)
		}
	}
	for _, want := range []string{
		"initiative_create", "initiative_list", "initiative_get", "initiative_update",
		"milestone_create", "milestone_list", "milestone_get", "milestone_update",
		"task_create", "task_list", "task_get", "task_update", "task_set_status", "task_add_note", "task_next",
		"doc_create", "doc_list", "doc_get", "doc_update", "search", "status",
	} {
		if !slices.Contains(names, want) {
			t.Errorf("missing tool %s", want)
		}
	}
	if init := cs.InitializeResult(); init == nil || !strings.Contains(init.Instructions, "RALPH") {
		t.Error("server instructions missing")
	}
}

func TestArticulateThenLoopFlow(t *testing.T) {
	cs := connect(t)
	var initiative service.InitiativeView
	mustOK(t, cs, "initiative_create", map[string]any{"title": "Customer SSO", "brief": "Business wants Okta SSO"}, &initiative)
	if initiative.ID != "I-1" {
		t.Fatalf("initiative = %+v", initiative)
	}
	var doc service.DocumentView
	mustOK(t, cs, "doc_create", map[string]any{
		"kind": "design", "title": "SSO design", "initiative_id": "I-1",
		"body": "Use OIDC authorization code flow with PKCE against Okta.",
	}, &doc)
	var ms service.MilestoneView
	mustOK(t, cs, "milestone_create", map[string]any{"initiative_id": "I-1", "title": "OIDC login"}, &ms)

	var task service.TaskView
	mustOK(t, cs, "task_create", map[string]any{"milestone_id": ms.ID, "title": "Add callback route"}, &task)
	if task.Status != "PROPOSED" {
		t.Fatalf("task = %+v", task)
	}

	res := call(t, cs, "task_set_status", map[string]any{"id": task.ID, "status": "TODO"}, nil)
	if !res.IsError || !strings.Contains(text(res), "acceptance criteria") {
		t.Fatalf("promotion without articulation should be a tool error, got %q", text(res))
	}

	mustOK(t, cs, "task_update", map[string]any{
		"id": task.ID, "description": "Add /oauth/callback handler", "acceptance_criteria": "Handler test passes",
	}, &task)
	mustOK(t, cs, "task_set_status", map[string]any{"id": task.ID, "status": "todo", "reason": "articulated"}, &task)

	var next struct {
		Task    *service.TaskView `json:"task"`
		Message string            `json:"message"`
	}
	mustOK(t, cs, "task_next", map[string]any{"milestone_id": ms.ID}, &next)
	if next.Task == nil || next.Task.ID != task.ID {
		t.Fatalf("task_next = %+v", next)
	}
	mustOK(t, cs, "task_set_status", map[string]any{"id": task.ID, "status": "IN_PROGRESS"}, nil)
	mustOK(t, cs, "task_add_note", map[string]any{"id": task.ID, "body": "Handler stubbed", "kind": "iteration"}, nil)
	mustOK(t, cs, "task_set_status", map[string]any{"id": task.ID, "status": "DONE", "reason": "tests pass"}, &task)
	if len(task.Notes) != 3 {
		t.Errorf("notes = %+v", task.Notes)
	}
	var none struct {
		Task    *service.TaskView `json:"task"`
		Message string            `json:"message"`
	}
	mustOK(t, cs, "task_next", map[string]any{"milestone_id": ms.ID}, &none)
	if none.Task != nil || none.Message == "" {
		t.Errorf("expected no next task: %+v", none)
	}

	var found service.SearchView
	mustOK(t, cs, "search", map[string]any{"query": "PKCE authorization code", "kinds": []string{"document"}}, &found)
	if found.Mode != "vector" || len(found.Hits) != 1 || found.Hits[0].ID != doc.ID {
		t.Errorf("search = %+v", found)
	}

	var status service.StatusView
	mustOK(t, cs, "status", map[string]any{}, &status)
	if len(status.Initiatives) != 1 || status.Initiatives[0].Milestones[0].Status != "DONE" {
		t.Errorf("status = %+v", status)
	}

	var detail struct {
		ID    string             `json:"id"`
		Tasks []service.TaskView `json:"tasks"`
	}
	mustOK(t, cs, "milestone_get", map[string]any{"id": ms.ID}, &detail)
	if detail.ID != ms.ID || len(detail.Tasks) != 1 {
		t.Errorf("milestone_get = %+v", detail)
	}

	var lists struct {
		Initiatives []service.InitiativeView `json:"initiatives"`
	}
	mustOK(t, cs, "initiative_list", map[string]any{}, &lists)
	if len(lists.Initiatives) != 1 {
		t.Errorf("initiative_list = %+v", lists)
	}
}

func TestToolErrorsAreActionable(t *testing.T) {
	cs := connect(t)
	cases := []struct {
		name     string
		args     map[string]any
		contains string
	}{
		{"task_get", map[string]any{"id": "T-99"}, "task_list"},
		{"task_get", map[string]any{"id": "M-1"}, "not a task ID"},
		{"doc_create", map[string]any{"kind": "memo", "title": "x"}, "unknown document kind"},
		{"task_list", map[string]any{"statuses": []string{"WAITING"}}, "unknown status"},
		{"search", map[string]any{"query": "x", "kinds": []string{"widget"}}, "unknown kind"},
		{"milestone_create", map[string]any{"initiative_id": "I-5", "title": "m"}, "not found"},
		{"doc_update", map[string]any{"id": "D-1", "kind": "memo"}, "not found"},
	}
	for _, c := range cases {
		res := call(t, cs, c.name, c.args, nil)
		if !res.IsError || !strings.Contains(text(res), c.contains) {
			t.Errorf("%s(%v): isError=%v text=%q; want %q", c.name, c.args, res.IsError, text(res), c.contains)
		}
	}

	mustOK(t, cs, "initiative_create", map[string]any{"title": "A", "slug": "a"}, nil)
	res := call(t, cs, "initiative_create", map[string]any{"title": "B", "slug": "a"}, nil)
	if !res.IsError || !strings.Contains(text(res), "different slug") {
		t.Errorf("conflict: %q", text(res))
	}

	_, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "task_get", Arguments: map[string]any{}})
	if err == nil {
		// Missing required arguments are rejected by schema validation, either
		// as a protocol error or a tool error; both are acceptable.
		res := call(t, cs, "task_get", map[string]any{}, nil)
		if !res.IsError {
			t.Error("missing required id should fail")
		}
	}
}
