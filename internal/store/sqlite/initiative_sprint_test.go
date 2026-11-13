package sqlite

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/bengearig/ralph/internal/domain"
)

func assertSprintColumn(t *testing.T, s *Store) {
	t.Helper()
	const want = "TEXT notnull=1 default='' pk=0"
	if got := tableCols(t, s.db, "initiatives")["sprint"]; got != want {
		t.Errorf("sprint = %q, want %q", got, want)
	}
}

func TestInitiativeSprintMigrationFreshAndUpgraded(t *testing.T) {
	ctx := context.Background()
	assertSprintColumn(t, openTest(t))

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
		if m.name == "0013_initiative_sprint.sql" {
			break
		}
		if _, err := raw.Exec(m.sql); err != nil {
			t.Fatalf("applying %s: %v", m.name, err)
		}
		if _, err := raw.Exec(`INSERT INTO schema_migrations (version, applied_at) VALUES (?, '2026-01-01T00:00:00Z')`, m.version); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := raw.Exec(`INSERT INTO initiatives (slug, title, brief, created_at, updated_at) VALUES ('old', 'Old', 'Brief', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	raw.Close()

	s, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()
	assertSprintColumn(t, s)
	is, err := s.ListInitiatives(ctx, false)
	if err != nil || len(is) != 1 || is[0].Slug != "old" || is[0].Sprint != "" {
		t.Fatalf("ListInitiatives = %+v, %v", is, err)
	}
}

func TestInitiativeSprint(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)

	plain := domain.Initiative{Slug: "plain", Title: "Plain"}
	if err := s.CreateInitiative(ctx, &plain); err != nil {
		t.Fatal(err)
	}
	if got, err := s.GetInitiative(ctx, plain.ID); err != nil || got.Sprint != "" {
		t.Fatalf("without sprint = %q, %v", got.Sprint, err)
	}

	i := domain.Initiative{Slug: "auth", Title: "Auth", Sprint: "Sprint 42"}
	if err := s.CreateInitiative(ctx, &i); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetInitiative(ctx, i.ID)
	if err != nil || got.Sprint != "Sprint 42" {
		t.Fatalf("GetInitiative = %q, %v", got.Sprint, err)
	}
	list, err := s.ListInitiatives(ctx, false)
	if err != nil || len(list) != 2 || list[0].Sprint != "" || list[1].Sprint != "Sprint 42" {
		t.Fatalf("ListInitiatives = %+v, %v", list, err)
	}

	for _, want := range []string{"Sprint 43", ""} {
		got.Sprint = want
		if err := s.UpdateInitiative(ctx, &got); err != nil {
			t.Fatal(err)
		}
		if again, err := s.GetInitiative(ctx, i.ID); err != nil || again.Sprint != want {
			t.Fatalf("after setting %q = %q, %v", want, again.Sprint, err)
		}
	}
}
