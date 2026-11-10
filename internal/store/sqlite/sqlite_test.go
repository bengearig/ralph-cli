package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

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

var gitRecordColumns = map[string][]string{
	"milestones": {"branch", "base_commit", "head_commit"},
	"tasks":      {"commit_hash"},
}

func TestGitRecordColumns(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	for table, want := range gitRecordColumns {
		rows, err := s.db.QueryContext(ctx, `SELECT name, type, "notnull", dflt_value FROM pragma_table_info(?)`, table)
		if err != nil {
			t.Fatal(err)
		}
		type col struct {
			typ     string
			notNull bool
			dflt    sql.NullString
		}
		cols := map[string]col{}
		for rows.Next() {
			var name string
			var c col
			if err := rows.Scan(&name, &c.typ, &c.notNull, &c.dflt); err != nil {
				t.Fatal(err)
			}
			cols[name] = c
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		rows.Close()
		for _, name := range want {
			c, ok := cols[name]
			switch {
			case !ok:
				t.Errorf("%s.%s missing", table, name)
			case c.typ != "TEXT" || !c.notNull || !c.dflt.Valid || c.dflt.String != "''":
				t.Errorf("%s.%s = type %q, notnull %v, default %v; want TEXT NOT NULL DEFAULT ''", table, name, c.typ, c.notNull, c.dflt)
			}
		}
	}
}

// Builds a database with migration 0001 only, as ralph left it before the git
// record columns existed, then checks Open upgrades it without losing rows.
func TestGitRecordMigrationPreservesRows(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "ralph.db")
	ms, err := loadMigrations()
	if err != nil {
		t.Fatal(err)
	}
	if ms[0].version != 1 {
		t.Fatalf("first migration = %s, want version 1", ms[0].name)
	}

	raw, err := sql.Open("sqlite", "file:"+path+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	const now = "2026-01-01T00:00:00Z"
	for _, stmt := range []string{
		`CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY, applied_at TEXT NOT NULL)`,
		ms[0].sql,
		`INSERT INTO schema_migrations (version, applied_at) VALUES (1, '` + now + `')`,
		`INSERT INTO initiatives (id, slug, title, created_at, updated_at) VALUES (1, 'old', 'Old', '` + now + `', '` + now + `')`,
		`INSERT INTO milestones (id, initiative_id, slug, title, position, created_at, updated_at) VALUES (1, 1, 'm1', 'M1', 1, '` + now + `', '` + now + `')`,
		`INSERT INTO tasks (id, milestone_id, title, status, position, created_at, updated_at) VALUES (1, 1, 'T1', 'TODO', 1, '` + now + `', '` + now + `')`,
	} {
		if _, err := raw.ExecContext(ctx, stmt); err != nil {
			raw.Close()
			t.Fatalf("building pre-migration database: %v", err)
		}
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}

	s, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()
	if v, err := s.SchemaVersion(ctx); err != nil || v != ms[len(ms)-1].version {
		t.Errorf("SchemaVersion = %d, %v; want %d", v, err, ms[len(ms)-1].version)
	}

	var title, branch, base, head string
	if err := s.db.QueryRowContext(ctx, `SELECT title, branch, base_commit, head_commit FROM milestones WHERE id = 1`).Scan(&title, &branch, &base, &head); err != nil {
		t.Fatalf("milestone: %v", err)
	}
	if title != "M1" || branch != "" || base != "" || head != "" {
		t.Errorf("milestone = %q, branch %q, base %q, head %q", title, branch, base, head)
	}
	var commit string
	if err := s.db.QueryRowContext(ctx, `SELECT title, commit_hash FROM tasks WHERE id = 1`).Scan(&title, &commit); err != nil {
		t.Fatalf("task: %v", err)
	}
	if title != "T1" || commit != "" {
		t.Errorf("task = %q, commit_hash %q", title, commit)
	}
}

func TestGitRecordSetters(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	i, m := seed(t, s)
	task := domain.Task{MilestoneID: m.ID, Title: "t", Status: domain.StatusTodo}
	if err := s.CreateTask(ctx, &task); err != nil {
		t.Fatal(err)
	}
	// Any write that stamps updated_at would now produce a different time.
	later := m.UpdatedAt.Add(time.Hour)
	s.now = func() time.Time { return later }

	g := domain.MilestoneGit{
		Branch:     "ralph/m1",
		BaseCommit: strings.Repeat("a", 40),
		HeadCommit: strings.Repeat("b", 40),
	}
	if err := s.SetMilestoneGit(ctx, m.ID, g); err != nil {
		t.Fatal(err)
	}
	hash := strings.Repeat("c", 64)
	if err := s.SetTaskCommit(ctx, task.ID, hash); err != nil {
		t.Fatal(err)
	}

	gotM, err := s.GetMilestone(ctx, m.ID)
	if err != nil {
		t.Fatal(err)
	}
	if gotM.Git != g {
		t.Errorf("GetMilestone git = %+v, want %+v", gotM.Git, g)
	}
	if !gotM.UpdatedAt.Equal(m.UpdatedAt) {
		t.Errorf("SetMilestoneGit changed updated_at: %v -> %v", m.UpdatedAt, gotM.UpdatedAt)
	}
	ms, err := s.ListMilestones(ctx, i.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(ms) != 1 || ms[0].Git != g {
		t.Errorf("ListMilestones = %+v", ms)
	}
	gotT, err := s.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if gotT.Commit != hash {
		t.Errorf("GetTask commit = %q, want %q", gotT.Commit, hash)
	}
	if !gotT.UpdatedAt.Equal(task.UpdatedAt) {
		t.Errorf("SetTaskCommit changed updated_at: %v -> %v", task.UpdatedAt, gotT.UpdatedAt)
	}
	ts, err := s.ListTasks(ctx, store.TaskFilter{InitiativeID: i.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(ts) != 1 || ts[0].Commit != hash {
		t.Errorf("ListTasks = %+v", ts)
	}

	if err := s.SetMilestoneGit(ctx, 999, g); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("SetMilestoneGit(999) = %v, want ErrNotFound", err)
	}
	if err := s.SetTaskCommit(ctx, 999, hash); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("SetTaskCommit(999) = %v, want ErrNotFound", err)
	}

	// Edits through Update* must not overwrite recorded values, even from a
	// struct whose git fields are stale or empty.
	gotM.Title = "Renamed"
	gotM.Git = domain.MilestoneGit{}
	if err := s.UpdateMilestone(ctx, &gotM); err != nil {
		t.Fatal(err)
	}
	gotT.Status = domain.StatusInProgress
	gotT.Commit = ""
	if err := s.UpdateTask(ctx, &gotT); err != nil {
		t.Fatal(err)
	}
	if after, err := s.GetMilestone(ctx, m.ID); err != nil || after.Title != "Renamed" || after.Git != g {
		t.Errorf("after UpdateMilestone = %+v, %v; want title Renamed and git %+v", after, err, g)
	}
	if after, err := s.GetTask(ctx, task.ID); err != nil || after.Status != domain.StatusInProgress || after.Commit != hash {
		t.Errorf("after UpdateTask = %+v, %v; want IN_PROGRESS and commit %q", after, err, hash)
	}
}

func TestCreateIgnoresGitRecord(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	i, _ := seed(t, s)
	m := domain.Milestone{InitiativeID: i.ID, Slug: "m2", Title: "M2",
		Git: domain.MilestoneGit{Branch: "b", BaseCommit: strings.Repeat("a", 40), HeadCommit: strings.Repeat("a", 40)}}
	if err := s.CreateMilestone(ctx, &m); err != nil {
		t.Fatal(err)
	}
	task := domain.Task{MilestoneID: m.ID, Title: "t", Status: domain.StatusTodo, Commit: strings.Repeat("a", 40)}
	if err := s.CreateTask(ctx, &task); err != nil {
		t.Fatal(err)
	}
	if got, err := s.GetMilestone(ctx, m.ID); err != nil || got.Git != (domain.MilestoneGit{}) {
		t.Errorf("created milestone git = %+v, %v; want empty", got.Git, err)
	}
	if got, err := s.GetTask(ctx, task.ID); err != nil || got.Commit != "" {
		t.Errorf("created task commit = %q, %v; want empty", got.Commit, err)
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

const rebuildMilestonesSQL = `-- ralph:rebuild
CREATE TABLE milestones_new (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    initiative_id INTEGER NOT NULL REFERENCES initiatives (id) ON DELETE CASCADE,
    slug          TEXT NOT NULL,
    title         TEXT NOT NULL,
    description   TEXT NOT NULL DEFAULT '',
    position      INTEGER NOT NULL,
    created_at    TEXT NOT NULL,
    updated_at    TEXT NOT NULL,
    branch        TEXT NOT NULL DEFAULT '',
    base_commit   TEXT NOT NULL DEFAULT '',
    head_commit   TEXT NOT NULL DEFAULT '',
    UNIQUE (initiative_id, slug)
);
INSERT INTO milestones_new (id, initiative_id, slug, title, description, position, created_at, updated_at, branch, base_commit, head_commit)
    SELECT id, initiative_id, slug, title, description, position, created_at, updated_at, branch, base_commit, head_commit FROM milestones%s;
DROP TABLE milestones;
ALTER TABLE milestones_new RENAME TO milestones;
%s`

const milestonesTriggerSQL = `CREATE TRIGGER milestones_ad AFTER DELETE ON milestones BEGIN
    DELETE FROM chunks WHERE owner_kind = 'milestone' AND owner_id = old.id;
END;`

const rebuildVersion = 99

func rebuildFS(t *testing.T, body string) fstest.MapFS {
	t.Helper()
	fsys := fstest.MapFS{}
	entries, err := fs.ReadDir(migrationFS, "migrations")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		b, err := fs.ReadFile(migrationFS, "migrations/"+e.Name())
		if err != nil {
			t.Fatal(err)
		}
		fsys["migrations/"+e.Name()] = &fstest.MapFile{Data: b}
	}
	fsys[fmt.Sprintf("migrations/%04d_rebuild.sql", rebuildVersion)] = &fstest.MapFile{Data: []byte(body)}
	return fsys
}

// openPopulated returns a migrated store holding an initiative, milestone,
// task with notes, documents and chunks (some embedded).
func openPopulated(t *testing.T) (*Store, string) {
	t.Helper()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "ralph.db")
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	const now = "2026-01-01T00:00:00Z"
	for _, stmt := range []string{
		`INSERT INTO initiatives (id, slug, title, created_at, updated_at) VALUES (1, 'i', 'I', '` + now + `', '` + now + `')`,
		`INSERT INTO milestones (id, initiative_id, slug, title, position, created_at, updated_at, branch) VALUES (1, 1, 'm1', 'M1', 1, '` + now + `', '` + now + `', 'ralph/m1')`,
		`INSERT INTO milestones (id, initiative_id, slug, title, position, created_at, updated_at) VALUES (2, 1, 'm2', 'M2', 2, '` + now + `', '` + now + `')`,
		`INSERT INTO tasks (id, milestone_id, title, status, position, created_at, updated_at) VALUES (1, 1, 'T1', 'TODO', 1, '` + now + `', '` + now + `')`,
		`INSERT INTO task_notes (id, task_id, kind, body, created_at) VALUES (1, 1, 'NOTE', 'a', '` + now + `'), (2, 1, 'NOTE', 'b', '` + now + `')`,
		`INSERT INTO documents (id, initiative_id, kind, slug, title, created_at, updated_at) VALUES (1, 1, 'design', 'd', 'D', '` + now + `', '` + now + `'), (2, NULL, 'note', 'p', 'P', '` + now + `', '` + now + `')`,
		`INSERT INTO chunks (owner_kind, owner_id, initiative_id, seq, text, content_hash, model, dims, embedding) VALUES
			('milestone', 1, 1, 0, 'alpha', 'h1', 'm', 1, x'00000000'),
			('milestone', 2, 1, 0, 'beta', 'h2', '', 0, NULL),
			('task', 1, 1, 0, 'gamma', 'h3', 'm', 1, x'00000000'),
			('document', 1, 1, 0, 'delta', 'h4', '', 0, NULL)`,
	} {
		if _, err := s.db.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("populating: %v", err)
		}
	}
	return s, path
}

// dbState captures every schema definition and the rows of every ralph table.
func dbState(t *testing.T, db *sql.DB) string {
	t.Helper()
	ctx := context.Background()
	var b strings.Builder
	rows, err := db.QueryContext(ctx, `SELECT type, name, IFNULL(sql, '') FROM sqlite_master ORDER BY type, name`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var typ, name, def string
		if err := rows.Scan(&typ, &name, &def); err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(&b, "%s %s %s\n", typ, name, def)
	}
	rows.Close()
	for _, tbl := range []string{"schema_migrations", "initiatives", "milestones", "tasks", "task_notes", "documents", "chunks"} {
		rows, err := db.QueryContext(ctx, `SELECT * FROM `+tbl+` ORDER BY 1`)
		if err != nil {
			t.Fatal(err)
		}
		cols, _ := rows.Columns()
		for rows.Next() {
			vals := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for i := range vals {
				ptrs[i] = &vals[i]
			}
			if err := rows.Scan(ptrs...); err != nil {
				t.Fatal(err)
			}
			fmt.Fprintf(&b, "%s %v\n", tbl, vals)
		}
		rows.Close()
	}
	return b.String()
}

func count(t *testing.T, db *sql.DB, query string) int {
	t.Helper()
	var n int
	if err := db.QueryRowContext(context.Background(), query).Scan(&n); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	return n
}

func requireForeignKeysOn(t *testing.T, s *Store) {
	t.Helper()
	if n := count(t, s.db, `SELECT foreign_keys FROM pragma_foreign_keys`); n != 1 {
		t.Errorf("foreign_keys = %d, want 1", n)
	}
}

func backups(t *testing.T, path string) []string {
	t.Helper()
	m, err := filepath.Glob(fmt.Sprintf("%s.pre-%d-*.bak", path, rebuildVersion))
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestRebuildMigrationPreservesEverything(t *testing.T) {
	ctx := context.Background()
	s, path := openPopulated(t)
	tables := []string{"initiatives", "milestones", "tasks", "task_notes", "documents", "chunks", "chunks_fts"}
	before := map[string]int{}
	for _, tbl := range tables {
		before[tbl] = count(t, s.db, `SELECT COUNT(*) FROM `+tbl)
	}
	embedded := count(t, s.db, `SELECT COUNT(*) FROM chunks WHERE embedding IS NOT NULL`)
	originalState := dbState(t, s.db)

	body := fmt.Sprintf(rebuildMilestonesSQL, "", milestonesTriggerSQL)
	if err := s.migrateFS(ctx, rebuildFS(t, body)); err != nil {
		t.Fatalf("migrateFS: %v", err)
	}
	for _, tbl := range tables {
		if got := count(t, s.db, `SELECT COUNT(*) FROM `+tbl); got != before[tbl] {
			t.Errorf("%s rows = %d, want %d", tbl, got, before[tbl])
		}
	}
	if got := count(t, s.db, `SELECT COUNT(*) FROM chunks WHERE embedding IS NOT NULL`); got != embedded {
		t.Errorf("embedded chunks = %d, want %d", got, embedded)
	}
	if got := count(t, s.db, fmt.Sprintf(`SELECT COUNT(*) FROM schema_migrations WHERE version = %d`, rebuildVersion)); got != 1 {
		t.Errorf("schema_migrations rows for rebuild = %d, want 1", got)
	}
	if got := count(t, s.db, `SELECT COUNT(*) FROM sqlite_master WHERE sql LIKE '%AUTOINCREMENT%'`); got != 1 {
		t.Errorf("AUTOINCREMENT tables = %d, want 1", got)
	}
	requireForeignKeysOn(t, s)

	bs := backups(t, path)
	if len(bs) != 1 {
		t.Fatalf("backups = %v, want exactly one", bs)
	}
	raw, err := sql.Open("sqlite", "file:"+bs[0]+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	if got := dbState(t, raw); got != originalState {
		t.Errorf("backup does not hold the original database:\n%s", got)
	}
	var qc string
	if err := raw.QueryRow(`PRAGMA quick_check`).Scan(&qc); err != nil || qc != "ok" {
		t.Errorf("backup quick_check = %q, %v", qc, err)
	}
}

func TestRebuildMigrationFailuresRollBack(t *testing.T) {
	tests := []struct {
		name      string
		body      string
		wantCheck string
	}{
		{"missing trigger", fmt.Sprintf(rebuildMilestonesSQL, "", ""), `"snapshot"`},
		{"dangling foreign key", fmt.Sprintf(rebuildMilestonesSQL, " WHERE id <> 1", milestonesTriggerSQL), `"foreign_key_check"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			s, path := openPopulated(t)
			before := dbState(t, s.db)

			err := s.migrateFS(ctx, rebuildFS(t, tt.body))
			if err == nil {
				t.Fatal("migration succeeded, want failure")
			}
			bs := backups(t, path)
			if len(bs) != 1 {
				t.Fatalf("backups = %v, want exactly one", bs)
			}
			if !strings.Contains(err.Error(), tt.wantCheck) || !strings.Contains(err.Error(), bs[0]) {
				t.Errorf("error %q should name check %s and backup %s", err, tt.wantCheck, bs[0])
			}
			if after := dbState(t, s.db); after != before {
				t.Errorf("database changed after failed migration:\nbefore:\n%s\nafter:\n%s", before, after)
			}
			requireForeignKeysOn(t, s)
		})
	}
}

// Documents why rebuild mode exists: run as a normal migration, DROP TABLE
// cascades through the foreign keys and destroys child rows.
func TestRebuildSQLAsNormalMigrationCascades(t *testing.T) {
	ctx := context.Background()
	s, _ := openPopulated(t)
	body := strings.TrimPrefix(fmt.Sprintf(rebuildMilestonesSQL, "", milestonesTriggerSQL), rebuildMarker+"\n")
	if err := s.migrateFS(ctx, rebuildFS(t, body)); err != nil {
		t.Fatalf("migrateFS: %v", err)
	}
	if n := count(t, s.db, `SELECT COUNT(*) FROM tasks`); n != 0 {
		t.Errorf("tasks = %d; expected the cascade to delete them", n)
	}
	if n := count(t, s.db, `SELECT COUNT(*) FROM task_notes`); n != 0 {
		t.Errorf("task_notes = %d; expected the cascade to delete them", n)
	}
}

func TestRebuildMigrationNotRepeated(t *testing.T) {
	ctx := context.Background()
	s, path := openPopulated(t)
	fsys := rebuildFS(t, fmt.Sprintf(rebuildMilestonesSQL, "", milestonesTriggerSQL))
	if err := s.migrateFS(ctx, fsys); err != nil {
		t.Fatal(err)
	}
	state := dbState(t, s.db)
	if err := s.migrateFS(ctx, fsys); err != nil {
		t.Fatalf("second run: %v", err)
	}
	if got := backups(t, path); len(got) != 1 {
		t.Errorf("backups after second run = %v, want one", got)
	}
	if dbState(t, s.db) != state {
		t.Error("second run changed the database")
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s2, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer s2.Close()
	if got := backups(t, path); len(got) != 1 {
		t.Errorf("backups after reopen = %v, want one", got)
	}
	requireForeignKeysOn(t, s2)
}

func TestRebuildMarkerMustBeFirstLine(t *testing.T) {
	fsys := rebuildFS(t, "-- note\n"+rebuildMarker+"\nSELECT 1;")
	ms, err := loadMigrationsFS(fsys)
	if err != nil {
		t.Fatal(err)
	}
	if last := ms[len(ms)-1]; last.rebuild {
		t.Errorf("%s detected as rebuild; marker is not on the first line", last.name)
	}
}
