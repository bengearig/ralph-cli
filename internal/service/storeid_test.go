package service

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bengearig/ralph/internal/search"
	"github.com/bengearig/ralph/internal/store"
	"github.com/bengearig/ralph/internal/store/sqlite"
)

func storeIDService(t *testing.T, st store.Store, configured string) *Service {
	t.Helper()
	return New(st, &search.Index{Store: st}, Options{StoreID: configured})
}

func openStoreIDStore(t *testing.T) store.Store {
	t.Helper()
	st, err := sqlite.Open(context.Background(), filepath.Join(t.TempDir(), "r.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func TestStoreIDResolution(t *testing.T) {
	ctx := context.Background()
	st := openStoreIDStore(t)

	id, src, err := storeIDService(t, st, "").StoreID(ctx)
	if err != nil || id != "" || src != StoreIDNone {
		t.Fatalf("none = %q, %q, %v", id, src, err)
	}
	if _, err := st.GetMeta(ctx, "store_id"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("StoreID wrote: %v", err)
	}

	if _, err := st.SetMetaIfAbsent(ctx, "store_id", "kept"); err != nil {
		t.Fatal(err)
	}
	id, src, err = storeIDService(t, st, "").StoreID(ctx)
	if err != nil || id != "kept" || src != StoreIDStore {
		t.Fatalf("store = %q, %q, %v", id, src, err)
	}
	id, src, err = storeIDService(t, st, "cfg").StoreID(ctx)
	if err != nil || id != "cfg" || src != StoreIDConfig {
		t.Fatalf("config = %q, %q, %v", id, src, err)
	}
	if got, err := storeIDService(t, st, "cfg").EnsureStoreID(ctx); err != nil || got != "cfg" {
		t.Fatalf("ensure config = %q, %v", got, err)
	}
}

func TestEnsureStoreIDGeneratesAndKeeps(t *testing.T) {
	ctx := context.Background()
	st := openStoreIDStore(t)
	a := storeIDService(t, st, "")
	id, err := a.EnsureStoreID(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(id) != 3 || strings.Trim(id, storeIDAlphabet) != "" {
		t.Fatalf("id = %q", id)
	}
	kept, err := st.GetMeta(ctx, "store_id")
	if err != nil || kept != id {
		t.Fatalf("kept = %q, %v", kept, err)
	}
	if again, err := a.EnsureStoreID(ctx); err != nil || again != id {
		t.Fatalf("again = %q, %v", again, err)
	}
	if other, err := storeIDService(t, st, "").EnsureStoreID(ctx); err != nil || other != id {
		t.Fatalf("other service = %q, %v", other, err)
	}
	got, src, err := a.StoreID(ctx)
	if err != nil || got != id || src != StoreIDStore {
		t.Fatalf("StoreID = %q, %q, %v", got, src, err)
	}
}

func TestEnsureStoreIDCachesWithoutReadingStore(t *testing.T) {
	ctx := context.Background()
	st := openStoreIDStore(t)
	a := storeIDService(t, st, "")
	id, err := a.EnsureStoreID(ctx)
	if err != nil {
		t.Fatal(err)
	}
	a.store = failingMetaStore{Store: st}
	if again, err := a.EnsureStoreID(ctx); err != nil || again != id {
		t.Fatalf("again = %q, %v", again, err)
	}
}

type failingMetaStore struct{ store.Store }

func (failingMetaStore) GetMeta(context.Context, string) (string, error) {
	return "", errors.New("store read")
}

func TestGenerateStoreIDAlphabet(t *testing.T) {
	seen := map[byte]bool{}
	for range 1000 {
		id, err := generateStoreID()
		if err != nil {
			t.Fatal(err)
		}
		if len(id) != 3 || strings.ContainsAny(id, "ilo01") || strings.Trim(id, storeIDAlphabet) != "" {
			t.Fatalf("id = %q", id)
		}
		for i := range len(id) {
			seen[id[i]] = true
		}
	}
	if len(seen) < 2 {
		t.Fatalf("characters not varied: %v", seen)
	}
}
