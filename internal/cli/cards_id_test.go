package cli

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/bengearig/ralph/internal/config"
	"github.com/bengearig/ralph/internal/service"
	"github.com/bengearig/ralph/internal/store"
	"github.com/bengearig/ralph/internal/store/sqlite"
)

func openStoreForID(t *testing.T, h *testEnv) (*service.Service, func() error) {
	t.Helper()
	cfg, err := config.Defaults()
	if err != nil {
		t.Fatal(err)
	}
	cfg.Embeddings.Provider = config.EmbeddingsNone
	svc, closeFn, err := service.Open(context.Background(), cfg, h.dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	return svc, closeFn
}

func keepStoredID(t *testing.T, h *testEnv) string {
	t.Helper()
	svc, closeFn := openStoreForID(t, h)
	defer closeFn()
	id, err := svc.EnsureStoreID(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestCardsIDConfigured(t *testing.T) {
	h := newTestEnv(t, true)
	t.Setenv("RALPH_TASK_MANAGER__STORE_ID", "AB")
	if out := h.ok("cards", "id"); out != "ab (configured)\n" {
		t.Errorf("output = %q", out)
	}
	var raw map[string]string
	h.json(&raw, "cards", "id")
	if raw["store_id"] != "ab" || raw["source"] != "config" {
		t.Errorf("json = %v", raw)
	}
}

func TestCardsIDStored(t *testing.T) {
	h := newTestEnv(t, true)
	id := keepStoredID(t, h)
	if out := h.ok("cards", "id"); out != id+" (generated, kept in the store)\n" {
		t.Errorf("output = %q", out)
	}
	var raw map[string]string
	h.json(&raw, "cards", "id")
	if raw["store_id"] != id || raw["source"] != "store" {
		t.Errorf("json = %v", raw)
	}
}

func TestCardsIDNoneWritesNothing(t *testing.T) {
	h := newTestEnv(t, true)
	if out := h.ok("cards", "id"); out != "none yet; one is generated when the first card is created\n" {
		t.Errorf("output = %q", out)
	}
	var raw map[string]string
	h.json(&raw, "cards", "id")
	if v, ok := raw["store_id"]; !ok || v != "" || raw["source"] != "none" {
		t.Errorf("json = %v", raw)
	}
	svc, closeFn := openStoreForID(t, h)
	defer closeFn()
	if _, source, err := svc.StoreID(context.Background()); err != nil || source != service.StoreIDNone {
		t.Errorf("StoreID = %v, %v; want none", source, err)
	}
	cfg, err := config.Defaults()
	if err != nil {
		t.Fatal(err)
	}
	st, err := sqlite.Open(context.Background(), cfg.StoragePath(h.dir))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if _, err := st.GetMeta(context.Background(), "store_id"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("GetMeta = %v, want ErrNotFound", err)
	}
}

func TestCardsIDListedInHelp(t *testing.T) {
	h := newTestEnv(t, true)
	if out := h.ok("cards", "--help"); !strings.Contains(out, "\n  id ") {
		t.Errorf("help = %q", out)
	}
}
