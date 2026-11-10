package mcpserver

import (
	"context"
	"path/filepath"
	"testing"

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
