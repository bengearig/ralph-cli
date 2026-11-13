package mcpserver

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bengearig/ralph/internal/domain"
	"github.com/bengearig/ralph/internal/embed/fake"
	"github.com/bengearig/ralph/internal/search"
	"github.com/bengearig/ralph/internal/service"
	"github.com/bengearig/ralph/internal/store/sqlite"
	tmfake "github.com/bengearig/ralph/internal/taskmanager/fake"
)

func TestGetToolsIncludeCard(t *testing.T) {
	st, err := sqlite.Open(context.Background(), filepath.Join(t.TempDir(), "r.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	ix := &search.Index{Store: st, Embedder: fake.New(), Chunker: search.Chunker{MaxChars: 1000, OverlapChars: 100}}
	cs := connectService(t, service.New(st, ix, service.Options{TaskManager: tmfake.New()}), Options{})

	_, task := seedDeletable(t, cs)
	var tv service.TaskView
	mustOK(t, cs, "task_get", map[string]any{"id": task.ID}, &tv)
	if tv.Card == nil || tv.Card.Key != "FAKE-3" || tv.Card.Pending != "" {
		t.Errorf("task_get card = %+v", tv.Card)
	}
	var iv service.InitiativeView
	mustOK(t, cs, "initiative_get", map[string]any{"id": "I-1"}, &iv)
	if iv.Card == nil || iv.Card.Key != "FAKE-1" {
		t.Errorf("initiative_get card = %+v", iv.Card)
	}
	var sv service.StatusView
	mustOK(t, cs, "status", map[string]any{}, &sv)
	if len(sv.Initiatives) != 1 || sv.Initiatives[0].Milestones[0].Card == nil {
		t.Errorf("status = %+v", sv)
	}
}

func TestCardsSyncTool(t *testing.T) {
	st, err := sqlite.Open(context.Background(), filepath.Join(t.TempDir(), "r.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	ix := &search.Index{Store: st, Embedder: fake.New(), Chunker: search.Chunker{MaxChars: 1000, OverlapChars: 100}}
	seed := connectService(t, service.New(st, ix, service.Options{}), Options{})
	seedDeletable(t, seed)
	mustOK(t, seed, "initiative_create", map[string]any{"title": "Second"}, nil)

	cs := connectService(t, service.New(st, ix, service.Options{TaskManager: tmfake.New()}), Options{})
	tools, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, tl := range tools.Tools {
		if tl.Name == "cards_sync" {
			found = strings.Contains(tl.Description, "never runs automatically") && strings.Contains(tl.Description, "cancelled")
		}
	}
	if !found {
		t.Error("cards_sync missing or description incomplete")
	}

	var r service.SyncReport
	mustOK(t, cs, "cards_sync", map[string]any{"initiative_id": "I-2"}, &r)
	if r.Created != 1 || len(r.Items) != 1 || r.Items[0].ID != "I-2" {
		t.Errorf("scoped report = %+v", r)
	}
	r = service.SyncReport{}
	mustOK(t, cs, "cards_sync", map[string]any{}, &r)
	if r.Created != 3 || r.InSync != 1 || r.Failed != 0 {
		t.Errorf("report = %+v", r)
	}
}

func TestCardsSyncToolNotConfigured(t *testing.T) {
	cs := connect(t)
	var out any
	res := call(t, cs, "cards_sync", map[string]any{}, &out)
	if !res.IsError || !strings.Contains(text(res), "task_manager.provider") {
		t.Errorf("res = %+v %s", res.IsError, text(res))
	}
}

func TestCardsSyncToolKindOff(t *testing.T) {
	st, err := sqlite.Open(context.Background(), filepath.Join(t.TempDir(), "r.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	ix := &search.Index{Store: st, Embedder: fake.New(), Chunker: search.Chunker{MaxChars: 1000, OverlapChars: 100}}
	seed := connectService(t, service.New(st, ix, service.Options{}), Options{})
	seedDeletable(t, seed)

	kinds := []domain.Kind{domain.KindInitiative, domain.KindMilestone}
	cs := connectService(t, service.New(st, ix, service.Options{TaskManager: tmfake.New(), CardKinds: kinds}), Options{})
	var r service.SyncReport
	mustOK(t, cs, "cards_sync", map[string]any{}, &r)
	if r.Skipped == 0 || r.Failed != 0 || r.Skipped+r.Created != len(r.Items) {
		t.Fatalf("report = %+v", r)
	}
	for _, it := range r.Items {
		if it.Outcome == "skipped" && it.Error != "kind not in task_manager.card_kinds" {
			t.Errorf("%+v", it)
		}
		if strings.HasPrefix(it.ID, "T-") && it.Outcome != "skipped" {
			t.Errorf("%+v", it)
		}
	}
}

func TestInitiativeCreateSendsParentCard(t *testing.T) {
	st, err := sqlite.Open(context.Background(), filepath.Join(t.TempDir(), "r.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	ix := &search.Index{Store: st, Embedder: fake.New(), Chunker: search.Chunker{MaxChars: 1000, OverlapChars: 100}}
	prov := tmfake.New()
	cs := connectService(t, service.New(st, ix, service.Options{TaskManager: prov}), Options{})

	mustOK(t, cs, "initiative_create", map[string]any{"title": "Child", "parent_card": "PROJ-12"}, nil)
	for _, c := range prov.Calls() {
		if c.Method == "Create" && c.Request.Kind == domain.KindInitiative {
			if c.Request.ParentKey != "PROJ-12" {
				t.Errorf("parent key = %q", c.Request.ParentKey)
			}
			return
		}
	}
	t.Errorf("no initiative card created: %+v", prov.Calls())
}
