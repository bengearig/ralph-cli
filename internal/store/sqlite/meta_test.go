package sqlite

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"

	"github.com/bengearig/ralph/internal/store"
)

func TestMetaGetMissing(t *testing.T) {
	s := openTest(t)
	if _, err := s.GetMeta(context.Background(), "store_id"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("GetMeta = %v, want ErrNotFound", err)
	}
}

func TestMetaSetIfAbsentKeepsFirst(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	got, err := s.SetMetaIfAbsent(ctx, "store_id", "abc")
	if err != nil || got != "abc" {
		t.Fatalf("first = %q, %v", got, err)
	}
	got, err = s.SetMetaIfAbsent(ctx, "store_id", "xyz")
	if err != nil || got != "abc" {
		t.Fatalf("second = %q, %v; want abc", got, err)
	}
	if got, err = s.GetMeta(ctx, "store_id"); err != nil || got != "abc" {
		t.Fatalf("GetMeta = %q, %v; want abc", got, err)
	}
}

func raceMeta(t *testing.T, stores []*Store, n int) {
	t.Helper()
	ctx := context.Background()
	results := make([]string, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i], errs[i] = stores[i%len(stores)].SetMetaIfAbsent(ctx, "store_id", string(rune('a'+i)))
		}()
	}
	wg.Wait()
	for i := range n {
		if errs[i] != nil {
			t.Fatalf("call %d: %v", i, errs[i])
		}
		if results[i] != results[0] {
			t.Fatalf("results differ: %v", results)
		}
	}
	if got, err := stores[0].GetMeta(ctx, "store_id"); err != nil || got != results[0] {
		t.Fatalf("GetMeta = %q, %v; want %q", got, err, results[0])
	}
}

func TestMetaSetIfAbsentGoroutines(t *testing.T) {
	raceMeta(t, []*Store{openTest(t)}, 16)
}

func TestMetaSetIfAbsentTwoOpens(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "ralph.db")
	var stores []*Store
	for range 2 {
		s, err := Open(ctx, path)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { s.Close() })
		stores = append(stores, s)
	}
	raceMeta(t, stores, 16)
}

func TestMetaMigrationAddsTable(t *testing.T) {
	s := openTest(t)
	if got := count(t, s.db, `SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'store_meta'`); got != 1 {
		t.Fatalf("store_meta tables = %d, want 1", got)
	}
	if got := count(t, s.db, `SELECT COUNT(*) FROM schema_migrations WHERE version = 12`); got != 1 {
		t.Fatalf("migration 12 applied %d times, want 1", got)
	}
}
