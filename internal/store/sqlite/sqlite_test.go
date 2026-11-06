package sqlite

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"

	"github.com/bengearig/ralph/internal/domain"
	"github.com/bengearig/ralph/internal/store"
)

func openTest(t *testing.T) *Store {
	t.Helper()
	s, err := Open(context.Background(), filepath.Join(t.TempDir(), "nested", "ralph.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func seed(t *testing.T, s *Store) (domain.Initiative, domain.Milestone) {
	t.Helper()
	ctx := context.Background()
	i := domain.Initiative{Slug: "auth", Title: "Auth", Brief: "Add login"}
	if err := s.CreateInitiative(ctx, &i); err != nil {
		t.Fatal(err)
	}
	m := domain.Milestone{InitiativeID: i.ID, Slug: "m1", Title: "M1"}
	if err := s.CreateMilestone(ctx, &m); err != nil {
		t.Fatal(err)
	}
	return i, m
}

func TestMigrationsIdempotent(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "ralph.db")
	s1, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	v1, err := s1.SchemaVersion(ctx)
	if err != nil || v1 < 1 {
		t.Fatalf("SchemaVersion = %d, %v", v1, err)
	}
	s1.Close()
	s2, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer s2.Close()
	if v2, _ := s2.SchemaVersion(ctx); v2 != v1 {
		t.Errorf("version changed on reopen: %d → %d", v1, v2)
	}
}

func TestOpenRejectsURIChars(t *testing.T) {
	if _, err := Open(context.Background(), filepath.Join(t.TempDir(), "a?b.db")); err == nil {
		t.Error("expected error")
	}
}

func TestInitiativeCRUD(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	i := domain.Initiative{Slug: "x", Title: "X", Brief: "b"}
	if err := s.CreateInitiative(ctx, &i); err != nil {
		t.Fatal(err)
	}
	if i.ID == 0 || i.CreatedAt.IsZero() {
		t.Fatalf("not populated: %+v", i)
	}
	dup := domain.Initiative{Slug: "x", Title: "X2"}
	if err := s.CreateInitiative(ctx, &dup); !errors.Is(err, store.ErrConflict) {
		t.Errorf("duplicate slug: %v", err)
	}
	i.Title = "X prime"
	if err := s.UpdateInitiative(ctx, &i); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetInitiative(ctx, i.ID)
	if err != nil || got.Title != "X prime" || got.Brief != "b" {
		t.Errorf("Get = %+v, %v", got, err)
	}
	if _, err := s.GetInitiative(ctx, 999); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("missing: %v", err)
	}
	missing := domain.Initiative{ID: 999, Slug: "y", Title: "Y"}
	if err := s.UpdateInitiative(ctx, &missing); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("update missing: %v", err)
	}
	all, err := s.ListInitiatives(ctx)
	if err != nil || len(all) != 1 {
		t.Errorf("List = %v, %v", all, err)
	}
}

func TestMilestoneAndTaskOrdering(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	i, m1 := seed(t, s)
	m2 := domain.Milestone{InitiativeID: i.ID, Slug: "m2", Title: "M2"}
	if err := s.CreateMilestone(ctx, &m2); err != nil {
		t.Fatal(err)
	}
	if m1.Position != 1 || m2.Position != 2 {
		t.Errorf("positions = %d, %d", m1.Position, m2.Position)
	}
	if err := s.CreateMilestone(ctx, &domain.Milestone{InitiativeID: i.ID, Slug: "m1", Title: "dup"}); !errors.Is(err, store.ErrConflict) {
		t.Errorf("duplicate milestone slug: %v", err)
	}
	if err := s.CreateMilestone(ctx, &domain.Milestone{InitiativeID: 999, Slug: "z", Title: "z"}); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("missing initiative: %v", err)
	}

	mk := func(m domain.Milestone, title string, st domain.Status) domain.Task {
		task := domain.Task{MilestoneID: m.ID, Title: title, Status: st}
		if err := s.CreateTask(ctx, &task); err != nil {
			t.Fatal(err)
		}
		return task
	}
	b := mk(m2, "b", domain.StatusTodo)
	a1 := mk(m1, "a1", domain.StatusProposed)
	a2 := mk(m1, "a2", domain.StatusTodo)
	if a1.Position != 1 || a2.Position != 2 || b.Position != 1 {
		t.Errorf("task positions = %d %d %d", a1.Position, a2.Position, b.Position)
	}

	all, err := s.ListTasks(ctx, store.TaskFilter{InitiativeID: i.ID})
	if err != nil {
		t.Fatal(err)
	}
	var titles []string
	for _, x := range all {
		titles = append(titles, x.Title)
	}
	if got := len(titles); got != 3 || titles[0] != "a1" || titles[1] != "a2" || titles[2] != "b" {
		t.Errorf("order = %v", titles)
	}
	todo, _ := s.ListTasks(ctx, store.TaskFilter{Statuses: []domain.Status{domain.StatusTodo}, MilestoneID: m1.ID})
	if len(todo) != 1 || todo[0].ID != a2.ID {
		t.Errorf("filtered = %+v", todo)
	}

	a1.Status = domain.Status("WAITING")
	if err := s.UpdateTask(ctx, &a1); err == nil {
		t.Error("CHECK constraint should reject unknown status")
	}
}

func TestTaskNotesAndCascade(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	i, m := seed(t, s)
	task := domain.Task{MilestoneID: m.ID, Title: "t", Status: domain.StatusProposed}
	if err := s.CreateTask(ctx, &task); err != nil {
		t.Fatal(err)
	}
	for _, k := range []domain.NoteKind{domain.NoteGeneral, domain.NoteKickback} {
		if err := s.AddTaskNote(ctx, &domain.TaskNote{TaskID: task.ID, Kind: k, Body: string(k)}); err != nil {
			t.Fatal(err)
		}
	}
	notes, err := s.ListTaskNotes(ctx, task.ID)
	if err != nil || len(notes) != 2 || notes[1].Kind != domain.NoteKickback {
		t.Fatalf("notes = %+v, %v", notes, err)
	}
	if err := s.AddTaskNote(ctx, &domain.TaskNote{TaskID: 999, Kind: domain.NoteGeneral, Body: "x"}); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("note on missing task: %v", err)
	}
	if err := s.ReplaceChunks(ctx, store.Owner{Kind: domain.KindTask, ID: task.ID}, []store.Chunk{{Text: "t", ContentHash: "h"}}); err != nil {
		t.Fatal(err)
	}
	doc := domain.Document{InitiativeID: &i.ID, Kind: domain.DocBrief, Slug: "b", Title: "B"}
	if err := s.CreateDocument(ctx, &doc); err != nil {
		t.Fatal(err)
	}

	if _, err := s.db.ExecContext(ctx, `DELETE FROM initiatives WHERE id = ?`, i.ID); err != nil {
		t.Fatal(err)
	}
	for table, want := range map[string]int{"milestones": 0, "tasks": 0, "task_notes": 0, "documents": 0, "chunks": 0} {
		var n int
		if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+table).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != want {
			t.Errorf("%s has %d rows after cascade", table, n)
		}
	}
	var fts int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM chunks_fts WHERE chunks_fts MATCH '"t"'`).Scan(&fts); err != nil {
		t.Fatal(err)
	}
	if fts != 0 {
		t.Error("FTS index not cleaned up")
	}
}

func TestDocuments(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	i, _ := seed(t, s)
	proj := domain.Document{Kind: domain.DocArchitecture, Slug: "arch", Title: "Arch", Body: "body"}
	if err := s.CreateDocument(ctx, &proj); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateDocument(ctx, &domain.Document{Kind: domain.DocNote, Slug: "arch", Title: "dup"}); !errors.Is(err, store.ErrConflict) {
		t.Errorf("project-scoped duplicate slug: %v", err)
	}
	scoped := domain.Document{InitiativeID: &i.ID, Kind: domain.DocDesign, Slug: "arch", Title: "Scoped"}
	if err := s.CreateDocument(ctx, &scoped); err != nil {
		t.Errorf("same slug in another scope should be allowed: %v", err)
	}
	if err := s.CreateDocument(ctx, &domain.Document{Kind: "memo", Slug: "m", Title: "m"}); err == nil {
		t.Error("CHECK should reject unknown kind")
	}

	got, err := s.GetDocument(ctx, scoped.ID)
	if err != nil || got.InitiativeID == nil || *got.InitiativeID != i.ID {
		t.Errorf("Get scoped = %+v, %v", got, err)
	}
	projOnly, _ := s.ListDocuments(ctx, store.DocumentFilter{ProjectOnly: true})
	if len(projOnly) != 1 || projOnly[0].ID != proj.ID {
		t.Errorf("project-only = %+v", projOnly)
	}
	byKind, _ := s.ListDocuments(ctx, store.DocumentFilter{Kind: domain.DocDesign})
	if len(byKind) != 1 || byKind[0].ID != scoped.ID {
		t.Errorf("by kind = %+v", byKind)
	}
	all, _ := s.ListDocuments(ctx, store.DocumentFilter{})
	if len(all) != 2 {
		t.Errorf("all = %d", len(all))
	}
	proj.Body = "new body"
	if err := s.UpdateDocument(ctx, &proj); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetDocument(ctx, proj.ID); got.Body != "new body" {
		t.Errorf("updated body = %q", got.Body)
	}
}

func TestChunksEmbeddingLifecycle(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	i, m := seed(t, s)
	owner := store.Owner{Kind: domain.KindMilestone, ID: m.ID}
	chunks := []store.Chunk{
		{InitiativeID: i.ID, Seq: 0, Text: "database migrations", ContentHash: "h0"},
		{InitiativeID: i.ID, Seq: 1, Text: "oauth login flow", ContentHash: "h1"},
	}
	if err := s.ReplaceChunks(ctx, owner, chunks); err != nil {
		t.Fatal(err)
	}
	if n, _ := s.CountStaleChunks(ctx, "m"); n != 2 {
		t.Fatalf("stale = %d", n)
	}
	stale, err := s.StaleChunks(ctx, "m", 1)
	if err != nil || len(stale) != 1 {
		t.Fatalf("StaleChunks limit = %d, %v", len(stale), err)
	}
	vec := []float32{0.25, -1, 3.5}
	if err := s.SetEmbedding(ctx, chunks[0].ID, "m", vec); err != nil {
		t.Fatal(err)
	}
	if err := s.SetEmbedding(ctx, 9999, "m", vec); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("missing chunk: %v", err)
	}
	if err := s.SetEmbedding(ctx, chunks[1].ID, "m", nil); err == nil {
		t.Error("empty embedding should be rejected")
	}
	emb, err := s.EmbeddedChunks(ctx, "m", store.ChunkFilter{InitiativeID: i.ID, Kinds: []domain.Kind{domain.KindMilestone}})
	if err != nil || len(emb) != 1 {
		t.Fatalf("EmbeddedChunks = %v, %v", emb, err)
	}
	for j, f := range vec {
		if emb[0].Embedding[j] != f {
			t.Errorf("vector round trip: %v", emb[0].Embedding)
		}
	}
	if n, _ := s.CountStaleChunks(ctx, "other-model"); n != 2 {
		t.Errorf("model change should make all stale, got %d", n)
	}

	// Unchanged content keeps its embedding; changed content becomes stale.
	if err := s.ReplaceChunks(ctx, owner, []store.Chunk{
		{InitiativeID: i.ID, Seq: 0, Text: "database migrations", ContentHash: "h0"},
		{InitiativeID: i.ID, Seq: 1, Text: "saml login flow", ContentHash: "h2"},
	}); err != nil {
		t.Fatal(err)
	}
	if n, _ := s.CountStaleChunks(ctx, "m"); n != 1 {
		t.Errorf("after replace stale = %d, want 1", n)
	}

	hits, err := s.KeywordSearch(ctx, "SAML login?", store.ChunkFilter{}, 10)
	if err != nil || len(hits) != 1 || hits[0].Chunk.Text != "saml login flow" {
		t.Fatalf("keyword = %+v, %v", hits, err)
	}
	if hits[0].Chunk.Owner != owner {
		t.Errorf("owner = %+v", hits[0].Chunk.Owner)
	}
	if hits, _ := s.KeywordSearch(ctx, "oauth", store.ChunkFilter{}, 10); len(hits) != 0 {
		t.Error("replaced text should no longer match")
	}
	if hits, err := s.KeywordSearch(ctx, `" OR NEAR(`, store.ChunkFilter{}, 10); err != nil || len(hits) != 0 {
		t.Errorf("FTS syntax must be neutralized: %v %v", hits, err)
	}
	if hits, _ := s.KeywordSearch(ctx, "migrations", store.ChunkFilter{Kinds: []domain.Kind{domain.KindTask}}, 10); len(hits) != 0 {
		t.Error("kind filter ignored")
	}
}

func TestInTxRollback(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	boom := errors.New("boom")
	err := s.InTx(ctx, func(tx store.Store) error {
		if err := tx.CreateInitiative(ctx, &domain.Initiative{Slug: "r", Title: "R"}); err != nil {
			return err
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
	if all, _ := s.ListInitiatives(ctx); len(all) != 0 {
		t.Error("rollback did not discard insert")
	}
}

func TestConcurrentProcessesShareDatabase(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "ralph.db")
	a, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	b, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()

	var wg sync.WaitGroup
	errs := make(chan error, 40)
	for n := range 20 {
		for idx, st := range []*Store{a, b} {
			wg.Add(1)
			go func() {
				defer wg.Done()
				errs <- st.CreateInitiative(ctx, &domain.Initiative{Slug: fmt.Sprintf("s-%d-%d", n, idx), Title: "x"})
			}()
		}
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Errorf("concurrent write failed: %v", err)
		}
	}
	all, err := a.ListInitiatives(ctx)
	if err != nil || len(all) != 40 {
		t.Errorf("initiatives = %d, %v", len(all), err)
	}
}
