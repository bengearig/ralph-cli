package sqlite

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/bengearig/ralph/internal/domain"
)

func assertParentCardColumn(t *testing.T, s *Store) {
	t.Helper()
	const want = "TEXT notnull=1 default='' pk=0"
	if got := tableCols(t, s.db, "initiatives")["parent_card"]; got != want {
		t.Errorf("parent_card = %q, want %q", got, want)
	}
}

func TestParentCardMigrationFreshAndUpgraded(t *testing.T) {
	ctx := context.Background()
	assertParentCardColumn(t, openTest(t))

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
		if m.name == "0010_initiative_parent_card.sql" {
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
	assertParentCardColumn(t, s)
	is, err := s.ListInitiatives(ctx, false)
	if err != nil || len(is) != 1 {
		t.Fatalf("ListInitiatives = %v, %v", is, err)
	}
	if is[0].Slug != "old" || is[0].Title != "Old" || is[0].Brief != "Brief" || is[0].ParentCard != "" {
		t.Errorf("upgraded initiative = %+v", is[0])
	}
}

func TestInitiativeParentCard(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)

	plain := domain.Initiative{Slug: "plain", Title: "Plain"}
	if err := s.CreateInitiative(ctx, &plain); err != nil {
		t.Fatal(err)
	}
	if got, err := s.GetInitiative(ctx, plain.ID); err != nil || got.ParentCard != "" {
		t.Fatalf("GetInitiative without parent = %q, %v", got.ParentCard, err)
	}

	i := domain.Initiative{Slug: "auth", Title: "Auth", ParentCard: "PROJ-12"}
	if err := s.CreateInitiative(ctx, &i); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetInitiative(ctx, i.ID)
	if err != nil || got.ParentCard != "PROJ-12" {
		t.Fatalf("GetInitiative = %q, %v", got.ParentCard, err)
	}
	list, err := s.ListInitiatives(ctx, false)
	if err != nil || len(list) != 2 || list[0].ParentCard != "" || list[1].ParentCard != "PROJ-12" {
		t.Fatalf("ListInitiatives = %+v, %v", list, err)
	}

	got.Title = "Auth v2"
	if err := s.UpdateInitiative(ctx, &got); err != nil {
		t.Fatal(err)
	}
	if again, err := s.GetInitiative(ctx, i.ID); err != nil || again.Title != "Auth v2" || again.ParentCard != "PROJ-12" {
		t.Fatalf("after title update = %+v, %v", again, err)
	}

	for _, want := range []string{"PROJ-99", ""} {
		got.ParentCard = want
		if err := s.UpdateInitiative(ctx, &got); err != nil {
			t.Fatal(err)
		}
		if again, err := s.GetInitiative(ctx, i.ID); err != nil || again.ParentCard != want {
			t.Fatalf("after setting %q = %q, %v", want, again.ParentCard, err)
		}
	}
}
