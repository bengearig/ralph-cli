package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/bengearig/ralph/internal/domain"
	"github.com/bengearig/ralph/internal/store"
)

func TestDocumentOriginMigrationFreshAndUpgraded(t *testing.T) {
	ctx := context.Background()
	assertOriginColumn(t, openTest(t))

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
		if m.name == "0008_document_origin.sql" {
			break
		}
		if _, err := raw.Exec(m.sql); err != nil {
			t.Fatalf("applying %s: %v", m.name, err)
		}
		if _, err := raw.Exec(`INSERT INTO schema_migrations (version, applied_at) VALUES (?, '2026-01-01T00:00:00Z')`, m.version); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := raw.Exec(`INSERT INTO documents (kind, slug, title, body, created_at, updated_at) VALUES ('note', 'n', 'N', 'b', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	raw.Close()

	s, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()
	assertOriginColumn(t, s)
	if d, err := s.GetDocument(ctx, 1); err != nil || d.Title != "N" || d.OriginInitiativeID != nil {
		t.Errorf("existing document = %+v, %v", d, err)
	}
}

func assertOriginColumn(t *testing.T, s *Store) {
	t.Helper()
	if got := tableCols(t, s.db, "documents")["origin_initiative_id"]; got != "INTEGER notnull=0 default= pk=0" {
		t.Errorf("origin_initiative_id = %q", got)
	}
}

func TestDocumentOriginRoundTrip(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	i, _ := seed(t, s)
	d := domain.Document{Kind: domain.DocNote, Slug: "n", Title: "N"}
	if err := s.CreateDocument(ctx, &d); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetDocument(ctx, d.ID); got.OriginInitiativeID != nil {
		t.Errorf("new document origin = %v, want nil", *got.OriginInitiativeID)
	}
	if err := s.SetDocumentScope(ctx, d.ID, nil, &i.ID, ""); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetDocument(ctx, d.ID)
	if err != nil || got.OriginInitiativeID == nil || *got.OriginInitiativeID != i.ID {
		t.Fatalf("Get = %+v, %v", got, err)
	}
	list, _ := s.ListDocuments(ctx, store.DocumentFilter{})
	if len(list) != 1 || list[0].OriginInitiativeID == nil || *list[0].OriginInitiativeID != i.ID {
		t.Errorf("List = %+v", list)
	}
	got.Title = "Renamed"
	if err := s.UpdateDocument(ctx, &got); err != nil {
		t.Fatal(err)
	}
	if after, _ := s.GetDocument(ctx, d.ID); after.Title != "Renamed" || after.OriginInitiativeID == nil || *after.OriginInitiativeID != i.ID {
		t.Errorf("after update = %+v", after)
	}

	if err := s.DeleteInitiative(ctx, i.ID); err != nil {
		t.Fatal(err)
	}
	after, err := s.GetDocument(ctx, d.ID)
	if err != nil || after.OriginInitiativeID != nil {
		t.Errorf("after deleting origin = %+v, %v", after, err)
	}
}

func TestSetDocumentScope(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	i, _ := seed(t, s)
	d := domain.Document{InitiativeID: &i.ID, Kind: domain.DocDesign, Slug: "arch", Title: "Arch"}
	if err := s.CreateDocument(ctx, &d); err != nil {
		t.Fatal(err)
	}
	s.now = func() time.Time { return d.UpdatedAt.Add(time.Hour) }

	if err := s.SetDocumentScope(ctx, d.ID, nil, &i.ID, ""); err != nil {
		t.Fatal(err)
	}
	got, _ := s.GetDocument(ctx, d.ID)
	if got.InitiativeID != nil || got.OriginInitiativeID == nil || *got.OriginInitiativeID != i.ID || got.Slug != "arch" {
		t.Errorf("promoted = %+v", got)
	}
	if !got.UpdatedAt.After(d.UpdatedAt) {
		t.Errorf("updated_at not bumped: %v -> %v", d.UpdatedAt, got.UpdatedAt)
	}

	if err := s.SetDocumentScope(ctx, d.ID, &i.ID, nil, "arch-2"); err != nil {
		t.Fatal(err)
	}
	got, _ = s.GetDocument(ctx, d.ID)
	if got.InitiativeID == nil || *got.InitiativeID != i.ID || got.OriginInitiativeID != nil || got.Slug != "arch-2" {
		t.Errorf("returned = %+v", got)
	}

	if err := s.SetDocumentScope(ctx, 999, nil, nil, ""); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("unknown ID: %v", err)
	}
}

func TestSetDocumentScopeConflict(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	i, _ := seed(t, s)
	project := domain.Document{Kind: domain.DocNote, Slug: "arch", Title: "Project"}
	scoped := domain.Document{InitiativeID: &i.ID, Kind: domain.DocNote, Slug: "arch", Title: "Scoped"}
	for _, d := range []*domain.Document{&project, &scoped} {
		if err := s.CreateDocument(ctx, d); err != nil {
			t.Fatal(err)
		}
	}
	before, _ := s.GetDocument(ctx, scoped.ID)

	if err := s.SetDocumentScope(ctx, scoped.ID, nil, &i.ID, ""); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("clashing move: %v", err)
	}
	if after, _ := s.GetDocument(ctx, scoped.ID); after.InitiativeID == nil || after.OriginInitiativeID != nil || after.Slug != "arch" || !after.UpdatedAt.Equal(before.UpdatedAt) {
		t.Errorf("failed move changed the document: %+v", after)
	}
	if err := s.SetDocumentScope(ctx, scoped.ID, nil, &i.ID, "arch-scoped"); err != nil {
		t.Fatalf("move with a free slug: %v", err)
	}
	if got, _ := s.GetDocument(ctx, scoped.ID); got.InitiativeID != nil || got.Slug != "arch-scoped" {
		t.Errorf("moved = %+v", got)
	}
}
