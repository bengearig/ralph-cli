package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/bengearig/ralph/internal/domain"
	"github.com/bengearig/ralph/internal/embed/fake"
	"github.com/bengearig/ralph/internal/looplock"
	"github.com/bengearig/ralph/internal/search"
	"github.com/bengearig/ralph/internal/service"
	"github.com/bengearig/ralph/internal/store/sqlite"
	tmfake "github.com/bengearig/ralph/internal/taskmanager/fake"
	"github.com/bengearig/ralph/internal/workflow"
)

func connect(t *testing.T) *mcp.ClientSession {
	t.Helper()
	return connectWith(t, Options{})
}

func connectWith(t *testing.T, opts Options) *mcp.ClientSession {
	t.Helper()
	return connectService(t, newService(t), opts)
}

func newService(t *testing.T) *service.Service {
	t.Helper()
	st, err := sqlite.Open(context.Background(), filepath.Join(t.TempDir(), "r.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	ix := &search.Index{Store: st, Embedder: fake.New(), Chunker: search.Chunker{MaxChars: 1000, OverlapChars: 100}}
	return service.New(st, ix, service.Options{StallThreshold: 3})
}

func connectService(t *testing.T, svc *service.Service, opts Options) *mcp.ClientSession {
	t.Helper()
	ctx := context.Background()
	srvT, cliT := mcp.NewInMemoryTransports()
	ss, err := New(svc, "test", nil, opts).Connect(ctx, srvT, nil)
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
			continue
		}
		switch tool.Name {
		case "import_list", "import_get":
			if !tool.Annotations.ReadOnlyHint {
				t.Errorf("%s is not read-only: %+v", tool.Name, tool.Annotations)
			}
		case "import_queue", "import_resolve", "task_import":
			a := tool.Annotations
			if a.ReadOnlyHint || a.DestructiveHint == nil || *a.DestructiveHint {
				t.Errorf("%s annotations = %+v, want non-destructive write", tool.Name, a)
			}
		case "milestone_drift":
			if !tool.Annotations.ReadOnlyHint {
				t.Errorf("milestone_drift is not read-only: %+v", tool.Annotations)
			}
		case "milestone_reconcile":
			a := tool.Annotations
			if a.ReadOnlyHint || a.DestructiveHint == nil || !*a.DestructiveHint || a.OpenWorldHint == nil || *a.OpenWorldHint {
				t.Errorf("milestone_reconcile annotations = %+v, want destructive and closed-world", a)
			}
		}
	}
	for _, want := range []string{
		"initiative_create", "initiative_list", "initiative_get", "initiative_update",
		"milestone_create", "milestone_list", "milestone_get", "milestone_update", "milestone_drift", "milestone_reconcile",
		"task_create", "task_import", "task_list", "task_get", "task_update", "task_set_status", "task_add_note", "task_next",
		"doc_create", "doc_list", "doc_get", "doc_update", "search", "status",
		"initiative_delete", "milestone_delete", "task_delete", "doc_delete", "note_delete",
		"initiative_archive", "initiative_unarchive", "doc_archive", "doc_unarchive",
		"doc_promote", "doc_return",
		"import_queue", "import_list", "import_get", "import_resolve",
	} {
		if !slices.Contains(names, want) {
			t.Errorf("missing tool %s", want)
		}
	}
	for _, n := range names {
		if strings.Contains(n, "merge") {
			t.Errorf("tool %s: merging is a person's action and has no MCP tool", n)
		}
	}
	if init := cs.InitializeResult(); init == nil || !strings.Contains(init.Instructions, "RALPH") {
		t.Error("server instructions missing")
	} else if !strings.Contains(init.Instructions, "include_archived") {
		t.Error("server instructions do not mention archived items")
	}
	for _, tool := range res.Tools {
		if !slices.Contains(deleteTools, tool.Name) {
			continue
		}
		a := tool.Annotations
		if a.ReadOnlyHint || a.DestructiveHint == nil || !*a.DestructiveHint || a.OpenWorldHint == nil || *a.OpenWorldHint {
			t.Errorf("%s annotations = %+v, want destructive and closed-world", tool.Name, a)
		}
	}
}

var deleteTools = []string{"initiative_delete", "milestone_delete", "task_delete", "doc_delete", "note_delete"}

func TestDeleteToolDescriptionsStatePolicy(t *testing.T) {
	cs := connect(t)
	res, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	seen := 0
	for _, tool := range res.Tools {
		if !slices.Contains(deleteTools, tool.Name) {
			continue
		}
		seen++
		wants := []string{"permanent", "CANCELLED", "user has agreed", "Cascades", "Refused"}
		if tool.Name != "doc_delete" && tool.Name != "note_delete" {
			wants = append(wants, "force without the user's explicit approval")
		}
		if tool.Name == "doc_delete" {
			wants = []string{"permanent", "CANCELLED", "user has agreed", "nothing blocks it"}
		}
		if tool.Name == "note_delete" {
			wants = []string{"permanent", "CANCELLED", "user has agreed", "Refused"}
		}
		for _, w := range wants {
			if !strings.Contains(tool.Description, w) {
				t.Errorf("%s description lacks %q: %s", tool.Name, w, tool.Description)
			}
		}
	}
	if seen != len(deleteTools) {
		t.Errorf("found %d delete tools, want %d", seen, len(deleteTools))
	}
	ins := cs.InitializeResult().Instructions
	for _, w := range []string{"mistakes", "agreement", "CANCELLED"} {
		if !strings.Contains(ins, w) {
			t.Errorf("instructions lack %q", w)
		}
	}
}

func seedDeletable(t *testing.T, cs *mcp.ClientSession) (ms service.MilestoneView, task service.TaskView) {
	t.Helper()
	mustOK(t, cs, "initiative_create", map[string]any{"title": "Init"}, nil)
	mustOK(t, cs, "milestone_create", map[string]any{"initiative_id": "I-1", "title": "Mile"}, &ms)
	mustOK(t, cs, "task_create", map[string]any{"milestone_id": ms.ID, "title": "Task"}, &task)
	return ms, task
}

func TestDeleteProposedTask(t *testing.T) {
	cs := connect(t)
	_, task := seedDeletable(t, cs)
	var v service.DeleteView
	mustOK(t, cs, "task_delete", map[string]any{"id": task.ID}, &v)
	if v.ID != task.ID || v.Kind != "task" || len(v.Deleted) != 1 || v.Deleted["tasks"] != 1 {
		t.Fatalf("delete view = %+v", v)
	}
	res := call(t, cs, "task_get", map[string]any{"id": task.ID}, nil)
	if !res.IsError || !strings.Contains(text(res), "not found") {
		t.Fatalf("task_get after delete = %q, want not found", text(res))
	}
}

func TestMilestoneDeleteNeedsForce(t *testing.T) {
	cs := connect(t)
	ms, task := seedDeletable(t, cs)
	mustOK(t, cs, "task_add_note", map[string]any{"id": task.ID, "body": "hello"}, nil)

	res := call(t, cs, "milestone_delete", map[string]any{"id": ms.ID}, nil)
	msg := text(res)
	if !res.IsError || !strings.Contains(msg, "force: true") || !strings.Contains(msg, "ask the user") {
		t.Fatalf("delete without force = %v %q", res.IsError, msg)
	}
	mustOK(t, cs, "task_get", map[string]any{"id": task.ID}, nil)

	var v service.DeleteView
	mustOK(t, cs, "milestone_delete", map[string]any{"id": ms.ID, "force": true}, &v)
	if v.ID != ms.ID || v.Kind != "milestone" || v.Deleted["milestones"] != 1 || v.Deleted["tasks"] != 1 || v.Deleted["notes"] < 1 {
		t.Fatalf("delete view = %+v", v)
	}
	if res := call(t, cs, "task_get", map[string]any{"id": task.ID}, nil); !res.IsError {
		t.Fatal("task survived the cascade")
	}
	if res := call(t, cs, "milestone_get", map[string]any{"id": ms.ID}, nil); !res.IsError {
		t.Fatal("milestone survived its delete")
	}
}

func TestInitiativeAndDocDelete(t *testing.T) {
	cs := connect(t)
	seedDeletable(t, cs)
	var doc service.DocumentView
	mustOK(t, cs, "doc_create", map[string]any{"kind": "design", "title": "Doc", "initiative_id": "I-1"}, &doc)

	res := call(t, cs, "initiative_delete", map[string]any{"id": "I-1"}, nil)
	if !res.IsError || !strings.Contains(text(res), "force: true") {
		t.Fatalf("initiative_delete without force = %q", text(res))
	}
	var v service.DeleteView
	mustOK(t, cs, "doc_delete", map[string]any{"id": doc.ID}, &v)
	if v.Kind != "document" || v.Deleted["documents"] != 1 {
		t.Fatalf("doc delete view = %+v", v)
	}
	mustOK(t, cs, "initiative_delete", map[string]any{"id": "I-1", "force": true}, &v)
	if v.Kind != "initiative" || v.Deleted["initiatives"] != 1 || v.Deleted["milestones"] != 1 || v.Deleted["tasks"] != 1 {
		t.Fatalf("initiative delete view = %+v", v)
	}
}

func TestNoteDelete(t *testing.T) {
	cs := connect(t)
	_, task := seedDeletable(t, cs)
	var tv service.TaskView
	mustOK(t, cs, "task_add_note", map[string]any{"id": task.ID, "body": "keep"}, &tv)
	mustOK(t, cs, "task_add_note", map[string]any{"id": task.ID, "body": "drop"}, &tv)
	var drop service.NoteView
	for _, n := range tv.Notes {
		if n.Body == "drop" {
			drop = n
		}
	}
	if !strings.HasPrefix(drop.ID, "N-") {
		t.Fatalf("notes = %+v", tv.Notes)
	}
	var v service.DeleteView
	mustOK(t, cs, "note_delete", map[string]any{"id": drop.ID}, &v)
	if v.ID != drop.ID || v.Kind != "note" || v.Deleted["notes"] != 1 {
		t.Fatalf("note delete view = %+v", v)
	}
	mustOK(t, cs, "task_get", map[string]any{"id": task.ID}, &tv)
	for _, n := range tv.Notes {
		if n.ID == drop.ID || n.Body == "drop" {
			t.Fatalf("note survived: %+v", tv.Notes)
		}
	}
}

func TestDeleteWrongKindID(t *testing.T) {
	cs := connect(t)
	seedDeletable(t, cs)
	for tool, id := range map[string]string{
		"doc_delete": "T-1", "task_delete": "D-1", "milestone_delete": "T-1", "initiative_delete": "M-1", "note_delete": "T-1",
	} {
		res := call(t, cs, tool, map[string]any{"id": id}, nil)
		if !res.IsError || !strings.Contains(text(res), "is not a") {
			t.Errorf("%s(%s) = %v %q, want a wrong-kind tool error", tool, id, res.IsError, text(res))
		}
	}
	mustOK(t, cs, "task_get", map[string]any{"id": "T-1"}, nil)
}

func TestToolErrorHints(t *testing.T) {
	got := toolError(fmt.Errorf("%w: busy", looplock.ErrLoopRunning))
	if !strings.Contains(got.Error(), "wait for the loop to finish or stop it") {
		t.Errorf("loop error = %q", got)
	}
	got = toolError(fmt.Errorf("%w: x", service.ErrNeedsForce))
	if !strings.Contains(got.Error(), "(ask the user before retrying with force: true)") {
		t.Errorf("force error = %q", got)
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
	for _, n := range task.Notes {
		if !strings.HasPrefix(n.ID, "N-") {
			t.Errorf("note ID = %q, want N-<n>", n.ID)
		}
	}
	var fetched service.TaskView
	mustOK(t, cs, "task_get", map[string]any{"id": task.ID}, &fetched)
	if len(fetched.Notes) != 3 || fetched.Notes[0].ID != task.Notes[0].ID || fetched.Notes[0].ID == "" {
		t.Errorf("task_get notes = %+v", fetched.Notes)
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

func TestInitiativeCreateReportsConfirmOutline(t *testing.T) {
	for _, want := range []bool{true, false} {
		cs := connectWith(t, Options{ConfirmOutline: want})
		var got map[string]any
		mustOK(t, cs, "initiative_create", map[string]any{"title": "Customer SSO", "brief": "Business wants Okta SSO"}, &got)
		co, ok := got["confirm_outline"]
		if !ok || co != want {
			t.Errorf("ConfirmOutline=%v: confirm_outline = %v (present %v) in %v", want, co, ok, got)
		}
		if got["id"] != "I-1" || got["slug"] != "customer-sso" || got["title"] != "Customer SSO" || got["brief"] != "Business wants Okta SSO" {
			t.Errorf("ConfirmOutline=%v: initiative fields not at top level: %v", want, got)
		}
	}
}

func TestConfirmOutlineOnlyOnInitiativeCreate(t *testing.T) {
	cs := connectWith(t, Options{ConfirmOutline: true})
	mustOK(t, cs, "initiative_create", map[string]any{"title": "Customer SSO"}, nil)
	for _, c := range []struct {
		name string
		args map[string]any
	}{
		{"initiative_get", map[string]any{"id": "I-1"}},
		{"initiative_list", map[string]any{}},
		{"initiative_update", map[string]any{"id": "I-1", "brief": "updated"}},
	} {
		var raw json.RawMessage
		mustOK(t, cs, c.name, c.args, &raw)
		if strings.Contains(string(raw), "confirm_outline") {
			t.Errorf("%s result contains confirm_outline: %s", c.name, raw)
		}
	}
}

func TestInitiativeCreateOutputSchema(t *testing.T) {
	cs := connect(t)
	res, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range res.Tools {
		if tool.Name != "initiative_create" {
			continue
		}
		raw, err := json.Marshal(tool.OutputSchema)
		if err != nil {
			t.Fatal(err)
		}
		var schema struct {
			Properties map[string]json.RawMessage `json:"properties"`
		}
		if err := json.Unmarshal(raw, &schema); err != nil {
			t.Fatal(err)
		}
		for _, key := range []string{"confirm_outline", "id", "slug", "title", "brief"} {
			if _, ok := schema.Properties[key]; !ok {
				t.Errorf("initiative_create output schema lacks %s: %s", key, raw)
			}
		}
		return
	}
	t.Fatal("initiative_create not listed")
}

func TestTaskNextRefusesArchivedInitiative(t *testing.T) {
	ctx := context.Background()
	svc := newService(t)
	cs := connectService(t, svc, Options{})
	var ms service.MilestoneView
	mustOK(t, cs, "initiative_create", map[string]any{"title": "Customer SSO"}, nil)
	mustOK(t, cs, "milestone_create", map[string]any{"initiative_id": "I-1", "title": "OIDC login"}, &ms)
	if _, err := svc.ArchiveInitiative(ctx, 1, service.DocumentChoice{}); err != nil {
		t.Fatal(err)
	}
	res := call(t, cs, "task_next", map[string]any{"milestone_id": ms.ID}, nil)
	if !res.IsError || !strings.Contains(text(res), "I-1 is archived") {
		t.Fatalf("task_next on archived initiative: error=%v %q", res.IsError, text(res))
	}
}

func TestArchiveToolsAndIncludeArchived(t *testing.T) {
	cs := connect(t)
	_, task := seedDeletable(t, cs)
	var list struct {
		Initiatives []service.InitiativeView `json:"initiatives"`
	}
	var arch service.InitiativeView
	mustOK(t, cs, "initiative_archive", map[string]any{"id": "I-1"}, &arch)
	if arch.ArchivedAt == "" {
		t.Errorf("initiative_archive view lacks archived_at: %+v", arch)
	}
	mustOK(t, cs, "initiative_list", map[string]any{}, &list)
	if len(list.Initiatives) != 0 {
		t.Errorf("archived initiative listed by default: %+v", list.Initiatives)
	}
	mustOK(t, cs, "initiative_list", map[string]any{"include_archived": true}, &list)
	if len(list.Initiatives) != 1 {
		t.Errorf("include_archived lists %d initiatives, want 1", len(list.Initiatives))
	}

	var hits service.SearchView
	mustOK(t, cs, "search", map[string]any{"query": "Task"}, &hits)
	if len(hits.Hits) != 0 {
		t.Errorf("search found archived items by default: %+v", hits.Hits)
	}
	mustOK(t, cs, "search", map[string]any{"query": "Task", "include_archived": true}, &hits)
	if len(hits.Hits) == 0 {
		t.Error("search with include_archived found nothing")
	}

	res := call(t, cs, "task_set_status", map[string]any{"id": task.ID, "status": "CANCELLED"}, nil)
	if !res.IsError || !strings.Contains(text(res), "unarchive it first if the user wants to change it") {
		t.Fatalf("mutation on archived task: error=%v %q", res.IsError, text(res))
	}

	var unarch service.InitiativeView
	mustOK(t, cs, "initiative_unarchive", map[string]any{"id": "I-1"}, &unarch)
	if unarch.ArchivedAt != "" {
		t.Errorf("initiative_unarchive left archived_at = %q", unarch.ArchivedAt)
	}
	mustOK(t, cs, "task_set_status", map[string]any{"id": task.ID, "status": "CANCELLED"}, nil)
}

func TestDocArchiveTools(t *testing.T) {
	cs := connect(t)
	var d service.DocumentView
	mustOK(t, cs, "doc_create", map[string]any{"kind": "architecture", "title": "Layout"}, &d)
	mustOK(t, cs, "doc_archive", map[string]any{"id": d.ID}, &d)
	if d.ArchivedAt == "" {
		t.Errorf("doc_archive view lacks archived_at: %+v", d)
	}
	var list struct {
		Documents []service.DocumentView `json:"documents"`
	}
	mustOK(t, cs, "doc_list", map[string]any{}, &list)
	if len(list.Documents) != 0 {
		t.Errorf("archived document listed by default")
	}
	mustOK(t, cs, "doc_list", map[string]any{"include_archived": true}, &list)
	if len(list.Documents) != 1 {
		t.Errorf("include_archived lists %d documents, want 1", len(list.Documents))
	}
	var restored service.DocumentView
	mustOK(t, cs, "doc_unarchive", map[string]any{"id": d.ID}, &restored)
	if restored.ArchivedAt != "" {
		t.Errorf("doc_unarchive left archived_at = %q", restored.ArchivedAt)
	}
}

func TestTaskDeleteResultCarriesCards(t *testing.T) {
	ctx := context.Background()
	st, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "r.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	ix := &search.Index{Store: st, Embedder: fake.New(), Chunker: search.Chunker{MaxChars: 1000, OverlapChars: 100}}
	p := tmfake.New()
	svc := service.New(st, ix, service.Options{StallThreshold: 3, TaskManager: p, CardCanceledStatus: "Canceled"})
	cs := connectService(t, svc, Options{})
	_, task := seedDeletable(t, cs)
	if err := svc.RecordCard(ctx, domain.KindTask, mustTaskID(t, task.ID), "fake", "FAKE-9", "http://x/FAKE-9", ""); err != nil {
		t.Fatal(err)
	}
	var v service.DeleteView
	mustOK(t, cs, "task_delete", map[string]any{"id": task.ID}, &v)
	if len(v.Cards) != 1 || v.Cards[0].Key != "FAKE-9" || !v.Cards[0].Moved {
		t.Fatalf("cards = %+v", v.Cards)
	}
	calls := p.Calls()
	if last := calls[len(calls)-1]; last.Method != "SetStatus" || last.Key != "FAKE-9" || last.Request.Status != "CANCELLED" {
		t.Errorf("last call = %+v", last)
	}
}

func mustTaskID(t *testing.T, s string) int64 {
	t.Helper()
	id, err := domain.ParseID(domain.KindTask, s)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestWorkflowsAreServedAsPrompts(t *testing.T) {
	cs := connect(t)
	ctx := context.Background()
	res, err := cs.ListPrompts(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := workflow.List()
	if len(res.Prompts) != len(want) {
		t.Fatalf("got %d prompts, want %d", len(res.Prompts), len(want))
	}
	for _, w := range want {
		i := slices.IndexFunc(res.Prompts, func(p *mcp.Prompt) bool { return p.Name == w.Name })
		if i < 0 {
			t.Fatalf("missing prompt %s", w.Name)
		}
		p := res.Prompts[i]
		if p.Description != w.Description {
			t.Errorf("%s description = %q, want %q", w.Name, p.Description, w.Description)
		}
		if len(p.Arguments) != 1 || p.Arguments[0].Name != w.Argument || p.Arguments[0].Required || p.Arguments[0].Description != w.ArgumentDescription {
			t.Errorf("%s arguments = %+v", w.Name, p.Arguments)
		}
	}
	if init := cs.InitializeResult(); init == nil || !strings.Contains(init.Instructions, "MCP prompts") {
		t.Error("server instructions do not mention the prompts")
	}
}

func TestGetPromptRendersNeutralWorkflow(t *testing.T) {
	cs := connect(t)
	ctx := context.Background()
	text := func(res *mcp.GetPromptResult) string {
		t.Helper()
		if len(res.Messages) != 1 || res.Messages[0].Role != "user" {
			t.Fatalf("messages = %+v", res.Messages)
		}
		c, ok := res.Messages[0].Content.(*mcp.TextContent)
		if !ok {
			t.Fatalf("content = %T", res.Messages[0].Content)
		}
		return c.Text
	}

	res, err := cs.GetPrompt(ctx, &mcp.GetPromptParams{Name: "receive", Arguments: map[string]string{"instructions": "x", "bogus": "y"}})
	if err != nil {
		t.Fatal(err)
	}
	want, err := workflow.Render("receive", workflow.Data{Target: workflow.Neutral, Args: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if got := text(res); got != want || !strings.Contains(got, "x") {
		t.Errorf("rendered prompt differs from workflow.Render")
	}

	res, err = cs.GetPrompt(ctx, &mcp.GetPromptParams{Name: "receive"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text(res), "(none given)") {
		t.Error("prompt without arguments lacks (none given)")
	}

	if _, err := cs.GetPrompt(ctx, &mcp.GetPromptParams{Name: "nope"}); err == nil {
		t.Error("unknown prompt should error")
	}
}

func TestImportPromptAndToolPointers(t *testing.T) {
	cs := connect(t)
	ctx := context.Background()
	res, err := cs.GetPrompt(ctx, &mcp.GetPromptParams{Name: "import", Arguments: map[string]string{"paths": "docs"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Messages) != 1 || !strings.Contains(res.Messages[0].Content.(*mcp.TextContent).Text, "docs") {
		t.Errorf("import prompt does not contain its argument: %+v", res.Messages)
	}
	tools, err := cs.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	seen := 0
	for _, tl := range tools.Tools {
		switch tl.Name {
		case "import_queue", "import_list", "import_get", "import_resolve":
			seen++
			if !strings.Contains(tl.Description, "import MCP prompt") {
				t.Errorf("%s description does not point at the import prompt", tl.Name)
			}
		}
	}
	if seen != 4 {
		t.Errorf("saw %d import tools, want 4", seen)
	}
}

func TestTaskImport(t *testing.T) {
	cs := connect(t)
	ms, _ := seedDeletable(t, cs)

	res, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range res.Tools {
		if tool.Name == "task_import" {
			for _, w := range []string{"task_create", "PROPOSED"} {
				if !strings.Contains(tool.Description, w) {
					t.Errorf("task_import description lacks %q: %s", w, tool.Description)
				}
			}
		}
	}

	var v service.TaskView
	mustOK(t, cs, "task_import", map[string]any{
		"milestone_id": ms.ID, "title": "Old login fix", "status": "DONE",
		"reason": "closed in the tracker", "source": "TRACKER.md",
	}, &v)
	if v.Status != "DONE" || len(v.AllowedNext) != 0 {
		t.Fatalf("imported task = %+v", v)
	}
	if len(v.Notes) != 1 || v.Notes[0].Body != "Imported as DONE: closed in the tracker (source: TRACKER.md)" {
		t.Errorf("notes = %+v", v.Notes)
	}

	if res := call(t, cs, "task_import", map[string]any{
		"milestone_id": ms.ID, "title": "Running", "status": "IN_PROGRESS", "reason": "x",
	}, nil); !res.IsError {
		t.Error("IN_PROGRESS import should fail")
	}
}

func TestDocPromoteAndReturnTools(t *testing.T) {
	cs := connect(t)
	mustOK(t, cs, "initiative_create", map[string]any{"title": "SSO"}, nil)
	var a, b service.DocumentView
	mustOK(t, cs, "doc_create", map[string]any{"initiative_id": "I-1", "kind": "design", "title": "Layout"}, &a)
	mustOK(t, cs, "doc_create", map[string]any{"kind": "design", "title": "Layout"}, &b)

	res := call(t, cs, "doc_promote", map[string]any{"id": a.ID}, nil)
	if !res.IsError || !strings.Contains(text(res), b.ID) || !strings.Contains(text(res), "slug") {
		t.Fatalf("clashing promote: error=%v %q", res.IsError, text(res))
	}
	var v service.DocumentView
	mustOK(t, cs, "doc_promote", map[string]any{"id": a.ID, "slug": "sso-layout"}, &v)
	if v.InitiativeID != "" || v.OriginInitiativeID != "I-1" || v.Slug != "sso-layout" {
		t.Errorf("doc_promote view = %+v", v)
	}
	if res := call(t, cs, "doc_promote", map[string]any{"id": a.ID}, nil); !res.IsError {
		t.Error("promoting a project-wide document succeeded")
	}
	v = service.DocumentView{}
	mustOK(t, cs, "doc_return", map[string]any{"id": a.ID}, &v)
	if v.InitiativeID != "I-1" || v.OriginInitiativeID != "" {
		t.Errorf("doc_return view = %+v", v)
	}
	if res := call(t, cs, "doc_return", map[string]any{"id": a.ID}, nil); !res.IsError {
		t.Error("returning a document with no origin succeeded")
	}

	tools, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	seen := 0
	for _, tool := range tools.Tools {
		if tool.Name != "doc_promote" && tool.Name != "doc_return" {
			continue
		}
		seen++
		an := tool.Annotations
		if an == nil || an.ReadOnlyHint || an.DestructiveHint == nil || *an.DestructiveHint {
			t.Errorf("%s annotations = %+v, want non-destructive write", tool.Name, an)
		}
		schema, _ := json.Marshal(tool.InputSchema)
		var s struct {
			Properties map[string]any `json:"properties"`
			Required   []string       `json:"required"`
		}
		if err := json.Unmarshal(schema, &s); err != nil {
			t.Fatal(err)
		}
		if _, ok := s.Properties["slug"]; !ok || slices.Contains(s.Required, "slug") {
			t.Errorf("%s slug input missing or required: %s", tool.Name, schema)
		}
	}
	if seen != 2 {
		t.Errorf("found %d promote/return tools, want both", seen)
	}
}

func seedArchiveDocs(t *testing.T, cs *mcp.ClientSession) {
	t.Helper()
	mustOK(t, cs, "initiative_create", map[string]any{"title": "Customer SSO"}, nil)
	mustOK(t, cs, "doc_create", map[string]any{"initiative_id": "I-1", "kind": "design", "title": "Flow", "body": "x"}, nil)
	mustOK(t, cs, "doc_create", map[string]any{"initiative_id": "I-1", "kind": "reference", "title": "Notes", "body": "y"}, nil)
}

func TestInitiativeArchiveRequiresPromoteChoice(t *testing.T) {
	cs := connect(t)
	seedArchiveDocs(t, cs)
	res := call(t, cs, "initiative_archive", map[string]any{"id": "I-1"}, nil)
	msg := text(res)
	if !res.IsError {
		t.Fatalf("archive without promote succeeded: %s", msg)
	}
	for _, want := range []string{`D-1  design  "Flow"`, `D-2  reference  "Notes"`, "Suggested (design and decision documents): D-1", `promote: ["D-1"]`, `promote: "all"`, `promote: "none"`} {
		if !strings.Contains(msg, want) {
			t.Errorf("refusal lacks %q:\n%s", want, msg)
		}
	}
	var got service.InitiativeView
	mustOK(t, cs, "initiative_get", map[string]any{"id": "I-1"}, &got)
	if got.ArchivedAt != "" {
		t.Errorf("initiative archived despite refusal: %+v", got)
	}
}

func TestInitiativeArchivePromoteForms(t *testing.T) {
	for _, tc := range []struct {
		name    string
		promote any
		want    []string
	}{
		{"all", "all", []string{"D-1", "D-2"}},
		{"none", "none", nil},
		{"list", []string{"D-2"}, []string{"D-2"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cs := connect(t)
			seedArchiveDocs(t, cs)
			var v service.InitiativeView
			mustOK(t, cs, "initiative_archive", map[string]any{"id": "I-1", "promote": tc.promote}, &v)
			if v.ArchivedAt == "" || !slices.Equal(v.PromotedDocuments, tc.want) {
				t.Errorf("view = %+v, want promoted %v", v, tc.want)
			}
		})
	}
}

func TestInitiativeArchivePromoteInvalid(t *testing.T) {
	for name, promote := range map[string]any{
		"mixed":     []string{"all", "D-1"},
		"malformed": []string{"X-1"},
		"number":    5,
	} {
		t.Run(name, func(t *testing.T) {
			cs := connect(t)
			seedArchiveDocs(t, cs)
			res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "initiative_archive", Arguments: map[string]any{"id": "I-1", "promote": promote}})
			if err == nil && !res.IsError {
				t.Fatalf("invalid promote %v accepted", promote)
			}
			var v service.InitiativeView
			mustOK(t, cs, "initiative_get", map[string]any{"id": "I-1"}, &v)
			if v.ArchivedAt != "" {
				t.Errorf("initiative archived despite invalid promote")
			}
		})
	}
}

func TestInitiativeArchiveSchemaAllowsArrayOrKeywords(t *testing.T) {
	cs := connect(t)
	res, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range res.Tools {
		if tool.Name != "initiative_archive" {
			continue
		}
		raw, _ := json.Marshal(tool.InputSchema)
		var s struct {
			Properties map[string]struct {
				OneOf []struct {
					Type string   `json:"type"`
					Enum []string `json:"enum"`
				} `json:"oneOf"`
			} `json:"properties"`
			Required []string `json:"required"`
		}
		if err := json.Unmarshal(raw, &s); err != nil {
			t.Fatal(err)
		}
		p := s.Properties["promote"]
		if len(p.OneOf) != 2 || p.OneOf[0].Type != "array" || p.OneOf[1].Type != "string" || !slices.Equal(p.OneOf[1].Enum, []string{"all", "none"}) || slices.Contains(s.Required, "promote") {
			t.Errorf("promote schema = %s", raw)
		}
		return
	}
	t.Fatal("initiative_archive not listed")
}

func TestNoticeAppendedToInstructions(t *testing.T) {
	base := connect(t).InitializeResult().Instructions
	got := connectWith(t, Options{Notice: "N"}).InitializeResult().Instructions
	if got != base+"\n\nN" {
		t.Errorf("instructions = %q", got)
	}
	if !strings.HasSuffix(got, "\n\nN") {
		t.Errorf("notice missing: %q", got)
	}
}

func TestInitiativeParentCardTools(t *testing.T) {
	cs := connect(t)

	var created map[string]any
	mustOK(t, cs, "initiative_create", map[string]any{"title": "With parent", "parent_card": "PROJ-12"}, &created)
	if created["parent_card"] != "PROJ-12" {
		t.Errorf("create parent_card = %v", created["parent_card"])
	}
	if _, ok := created["confirm_outline"]; !ok {
		t.Errorf("create result lacks confirm_outline: %v", created)
	}
	var got service.InitiativeView
	mustOK(t, cs, "initiative_get", map[string]any{"id": "I-1"}, &got)
	if got.ParentCard != "PROJ-12" {
		t.Errorf("get parent_card = %q", got.ParentCard)
	}

	var bare map[string]any
	mustOK(t, cs, "initiative_create", map[string]any{"title": "No parent"}, &bare)
	if _, ok := bare["parent_card"]; ok {
		t.Errorf("create without parent_card returned one: %v", bare)
	}

	var list struct {
		Initiatives []service.InitiativeView `json:"initiatives"`
	}
	mustOK(t, cs, "initiative_list", map[string]any{}, &list)
	if len(list.Initiatives) != 2 || list.Initiatives[0].ParentCard != "PROJ-12" || list.Initiatives[1].ParentCard != "" {
		t.Errorf("list = %+v", list.Initiatives)
	}

	var upd service.InitiativeView
	mustOK(t, cs, "initiative_update", map[string]any{"id": "I-1", "parent_card": "ABC-7"}, &upd)
	if upd.ParentCard != "ABC-7" {
		t.Errorf("update parent_card = %q", upd.ParentCard)
	}
	mustOK(t, cs, "initiative_update", map[string]any{"id": "I-1", "title": "Renamed"}, &upd)
	if upd.ParentCard != "ABC-7" || upd.Title != "Renamed" {
		t.Errorf("title-only update = %+v", upd)
	}
	var cleared, after service.InitiativeView
	mustOK(t, cs, "initiative_update", map[string]any{"id": "I-1", "parent_card": ""}, &cleared)
	if cleared.ParentCard != "" {
		t.Errorf("clearing parent_card left %q", cleared.ParentCard)
	}
	mustOK(t, cs, "initiative_get", map[string]any{"id": "I-1"}, &after)
	if after.ParentCard != "" {
		t.Errorf("get after clear = %q", after.ParentCard)
	}
}

func TestInitiativeParentCardInvalid(t *testing.T) {
	cs := connect(t)
	res := call(t, cs, "initiative_create", map[string]any{"title": "Bad", "parent_card": "proj-12"}, nil)
	if !res.IsError || !strings.Contains(text(res), "parent_card") {
		t.Errorf("create = error:%v %q", res.IsError, text(res))
	}
	var list struct {
		Initiatives []service.InitiativeView `json:"initiatives"`
	}
	mustOK(t, cs, "initiative_list", map[string]any{}, &list)
	if len(list.Initiatives) != 0 {
		t.Errorf("refused create stored %+v", list.Initiatives)
	}

	mustOK(t, cs, "initiative_create", map[string]any{"title": "Good", "parent_card": "PROJ-1"}, nil)
	res = call(t, cs, "initiative_update", map[string]any{"id": "I-1", "parent_card": "nope"}, nil)
	if !res.IsError || !strings.Contains(text(res), "parent_card") {
		t.Errorf("update = error:%v %q", res.IsError, text(res))
	}
	var got service.InitiativeView
	mustOK(t, cs, "initiative_get", map[string]any{"id": "I-1"}, &got)
	if got.ParentCard != "PROJ-1" {
		t.Errorf("refused update changed parent to %q", got.ParentCard)
	}
}

func TestInitiativeParentCardSchema(t *testing.T) {
	cs := connect(t)
	res, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	seen := 0
	for _, tool := range res.Tools {
		if tool.Name != "initiative_create" && tool.Name != "initiative_update" {
			continue
		}
		seen++
		raw, err := json.Marshal(tool.InputSchema)
		if err != nil {
			t.Fatal(err)
		}
		var schema struct {
			Properties map[string]struct {
				Description string `json:"description"`
			} `json:"properties"`
			Required []string `json:"required"`
		}
		if err := json.Unmarshal(raw, &schema); err != nil {
			t.Fatal(err)
		}
		p, ok := schema.Properties["parent_card"]
		if !ok || p.Description == "" {
			t.Errorf("%s input schema lacks a described parent_card: %s", tool.Name, raw)
		}
		for _, r := range schema.Required {
			if r == "parent_card" {
				t.Errorf("%s requires parent_card", tool.Name)
			}
		}
		if !strings.Contains(tool.Description, "parent") {
			t.Errorf("%s description omits the parent card: %q", tool.Name, tool.Description)
		}
	}
	if seen != 2 {
		t.Errorf("found %d initiative tools", seen)
	}
}
