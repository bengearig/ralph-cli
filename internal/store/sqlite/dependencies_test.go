package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/bengearig/ralph/internal/domain"
	"github.com/bengearig/ralph/internal/store"
)

func depMilestones(t *testing.T, s *Store, initiativeID int64, n int) []int64 {
	t.Helper()
	ids := make([]int64, n)
	for i := range ids {
		m := domain.Milestone{InitiativeID: initiativeID, Slug: fmt.Sprintf("dep-%d-%d", initiativeID, i+1), Title: "M"}
		if err := s.CreateMilestone(context.Background(), &m); err != nil {
			t.Fatal(err)
		}
		ids[i] = m.ID
	}
	return ids
}

func TestDependenciesMigrationFreshAndUpgraded(t *testing.T) {
	ctx := context.Background()
	fresh := openTest(t)
	assertDependencySchema(t, fresh)

	path := filepath.Join(t.TempDir(), "old.db")
	raw, err := sql.Open("sqlite", "file:"+path+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	raw.SetMaxOpenConns(1)
	if _, err := raw.Exec(`CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY, applied_at TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	ms, err := loadMigrations()
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range ms {
		if m.name == "0007_milestone_dependencies.sql" {
			break
		}
		if _, err := raw.Exec(m.sql); err != nil {
			t.Fatalf("applying %s: %v", m.name, err)
		}
		if _, err := raw.Exec(`INSERT INTO schema_migrations (version, applied_at) VALUES (?, '2026-01-01T00:00:00Z')`, m.version); err != nil {
			t.Fatal(err)
		}
	}
	for _, q := range []string{
		`INSERT INTO initiatives (slug, title, brief, created_at, updated_at) VALUES ('a', 'A', 'brief', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`,
		`INSERT INTO milestones (initiative_id, slug, title, description, position, created_at, updated_at) VALUES (1, 'm1', 'M1', 'd', 1, '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`,
	} {
		if _, err := raw.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	raw.Close()

	s, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()
	assertDependencySchema(t, s)
	if m, err := s.GetMilestone(ctx, 1); err != nil || m.Title != "M1" || m.Description != "d" {
		t.Errorf("existing milestone = %+v, %v", m, err)
	}
}

func assertDependencySchema(t *testing.T, s *Store) {
	t.Helper()
	want := map[string]string{
		"milestone_id":  "INTEGER notnull=1 default= pk=1",
		"depends_on_id": "INTEGER notnull=1 default= pk=2",
		"created_at":    "TEXT notnull=1 default= pk=0",
	}
	if got := tableCols(t, s.db, "milestone_dependencies"); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("columns = %v; want %v", got, want)
	}
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = 'milestone_dependencies_depends_on'`).Scan(&n); err != nil || n != 1 {
		t.Errorf("depends_on index count = %d, %v", n, err)
	}
	i, m := seed(t, s)
	_ = i
	if _, err := s.db.Exec(`INSERT INTO milestone_dependencies (milestone_id, depends_on_id, created_at) VALUES (?, ?, 't')`, m.ID, m.ID); err == nil {
		t.Error("CHECK should reject a self edge")
	}
}

func TestSetMilestoneDependencies(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	i, _ := seed(t, s)
	ids := depMilestones(t, s, i.ID, 3)
	a, b, c := ids[0], ids[1], ids[2]

	if err := s.SetMilestoneDependencies(ctx, a, []int64{c, b}); err != nil {
		t.Fatal(err)
	}
	got, err := s.ListMilestoneDependencies(ctx, 0)
	if err != nil || !reflect.DeepEqual(got, map[int64][]int64{a: {b, c}}) {
		t.Fatalf("after set = %v, %v", got, err)
	}
	var created string
	if err := s.db.QueryRow(`SELECT created_at FROM milestone_dependencies LIMIT 1`).Scan(&created); err != nil || created == "" {
		t.Errorf("created_at = %q, %v", created, err)
	}

	if err := s.SetMilestoneDependencies(ctx, a, []int64{c}); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.ListMilestoneDependencies(ctx, 0); !reflect.DeepEqual(got, map[int64][]int64{a: {c}}) {
		t.Errorf("after replace = %v", got)
	}

	if err := s.SetMilestoneDependencies(ctx, a, nil); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.ListMilestoneDependencies(ctx, 0); len(got) != 0 {
		t.Errorf("after clear = %v", got)
	}
}

func TestSetMilestoneDependenciesMissingIDs(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	i, _ := seed(t, s)
	ids := depMilestones(t, s, i.ID, 2)
	if err := s.SetMilestoneDependencies(ctx, ids[0], []int64{ids[1]}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetMilestoneDependencies(ctx, ids[0], []int64{ids[1], 9999}); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("missing dependency error = %v; want ErrNotFound", err)
	}
	if got, _ := s.ListMilestoneDependencies(ctx, 0); !reflect.DeepEqual(got, map[int64][]int64{ids[0]: {ids[1]}}) {
		t.Errorf("failed set changed the stored set: %v", got)
	}
	if err := s.SetMilestoneDependencies(ctx, 9999, []int64{ids[1]}); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("missing milestone error = %v; want ErrNotFound", err)
	}
}

func TestListMilestoneDependenciesByInitiative(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	i1, _ := seed(t, s)
	i2 := domain.Initiative{Slug: "other", Title: "Other", Brief: "b"}
	if err := s.CreateInitiative(ctx, &i2); err != nil {
		t.Fatal(err)
	}
	in1 := depMilestones(t, s, i1.ID, 2)
	in2 := depMilestones(t, s, i2.ID, 2)
	if err := s.SetMilestoneDependencies(ctx, in1[1], []int64{in2[1], in1[0]}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetMilestoneDependencies(ctx, in2[0], []int64{in1[1]}); err != nil {
		t.Fatal(err)
	}

	got, err := s.ListMilestoneDependencies(ctx, i1.ID)
	want := map[int64][]int64{in1[1]: {in1[0], in2[1]}}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Errorf("initiative 1 = %v, %v; want %v", got, err, want)
	}
	got, err = s.ListMilestoneDependencies(ctx, i2.ID)
	want = map[int64][]int64{in2[0]: {in1[1]}}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Errorf("initiative 2 = %v, %v; want %v", got, err, want)
	}
	got, err = s.ListMilestoneDependencies(ctx, 0)
	want = map[int64][]int64{in1[1]: {in1[0], in2[1]}, in2[0]: {in1[1]}}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Errorf("all = %v, %v; want %v", got, err, want)
	}

	deps, err := s.ListDependents(ctx, in1[1])
	if err != nil || !reflect.DeepEqual(deps, []int64{in2[0]}) {
		t.Errorf("dependents = %v, %v", deps, err)
	}
	if err := s.SetMilestoneDependencies(ctx, in1[0], []int64{in2[1]}); err != nil {
		t.Fatal(err)
	}
	deps, err = s.ListDependents(ctx, in2[1])
	if err != nil || !reflect.DeepEqual(deps, []int64{in1[0], in1[1]}) {
		t.Errorf("dependents ascending = %v, %v", deps, err)
	}
	if deps, err := s.ListDependents(ctx, in2[0]); err != nil || len(deps) != 0 {
		t.Errorf("no dependents = %v, %v", deps, err)
	}
}

func TestDeleteMilestoneRemovesEdges(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	i, _ := seed(t, s)
	ids := depMilestones(t, s, i.ID, 3)
	a, b, c := ids[0], ids[1], ids[2]
	if err := s.SetMilestoneDependencies(ctx, a, []int64{b}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetMilestoneDependencies(ctx, b, []int64{c}); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteMilestone(ctx, b); err != nil {
		t.Fatal(err)
	}
	if got, err := s.ListMilestoneDependencies(ctx, 0); err != nil || len(got) != 0 {
		t.Errorf("edges after delete = %v, %v", got, err)
	}
}
