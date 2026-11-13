package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"sort"
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
	ms, err := s.ListMilestones(ctx, i.ID, false)
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
	all, err := s.ListInitiatives(ctx, false)
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
	if all, _ := s.ListInitiatives(ctx, false); len(all) != 0 {
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
	all, err := a.ListInitiatives(ctx, false)
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
	autoincBefore := count(t, s.db, `SELECT COUNT(*) FROM sqlite_master WHERE sql LIKE '%AUTOINCREMENT%'`)
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
	if got := count(t, s.db, `SELECT COUNT(*) FROM sqlite_master WHERE sql LIKE '%AUTOINCREMENT%'`); got != autoincBefore {
		t.Errorf("AUTOINCREMENT tables = %d, want %d", got, autoincBefore)
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

var idTables = []string{"initiatives", "milestones", "tasks", "task_notes", "documents"}

func TestPermanentIDsAutoincrement(t *testing.T) {
	s := openTest(t)
	for _, tbl := range idTables {
		var def string
		if err := s.db.QueryRow(`SELECT sql FROM sqlite_master WHERE type = 'table' AND name = ?`, tbl).Scan(&def); err != nil {
			t.Fatalf("%s: %v", tbl, err)
		}
		if !strings.Contains(def, "AUTOINCREMENT") {
			t.Errorf("%s definition lacks AUTOINCREMENT: %s", tbl, def)
		}
	}
}

// openAtPreviousVersion creates a database migrated through the migration
// before the permanent-ID rebuild, using the raw driver so Open does not
// upgrade it.
func openAtPreviousVersion(t *testing.T, path string) *sql.DB {
	t.Helper()
	ms, err := loadMigrations()
	if err != nil {
		t.Fatal(err)
	}
	var prev []migration
	for _, m := range ms {
		if m.name == "0003_permanent_ids.sql" {
			break
		}
		prev = append(prev, m)
	}
	if len(prev) == 0 || len(prev) == len(ms) {
		t.Fatalf("migration 0003_permanent_ids.sql not found among %d migrations", len(ms))
	}
	raw, err := sql.Open("sqlite", "file:"+path+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	raw.SetMaxOpenConns(1)
	t.Cleanup(func() { raw.Close() })
	if _, err := raw.Exec(`CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY, applied_at TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	for _, m := range prev {
		if _, err := raw.Exec(m.sql); err != nil {
			t.Fatalf("applying %s: %v", m.name, err)
		}
		if _, err := raw.Exec(`INSERT INTO schema_migrations (version, applied_at) VALUES (?, '2026-01-01T00:00:00Z')`, m.version); err != nil {
			t.Fatal(err)
		}
	}
	return raw
}

// applyLaterMigrations applies the normal migrations after 0003 to a database
// left at the previous version, so its schema can be compared with a fresh one.
func applyLaterMigrations(t *testing.T, db *sql.DB) {
	t.Helper()
	ms, err := loadMigrations()
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range ms {
		if m.version > 3 {
			if _, err := db.Exec(m.sql); err != nil {
				t.Fatalf("applying %s: %v", m.name, err)
			}
		}
	}
}

func pragmaRows(t *testing.T, db *sql.DB, pragma, table string, drop ...string) []string {
	t.Helper()
	rows, err := db.Query(fmt.Sprintf(`SELECT * FROM pragma_%s('%s')`, pragma, table))
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	cols, _ := rows.Columns()
	var out []string
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			t.Fatal(err)
		}
		var parts []string
		for i, c := range cols {
			skip := false
			for _, d := range drop {
				skip = skip || c == d
			}
			if !skip {
				parts = append(parts, fmt.Sprintf("%s=%v", c, vals[i]))
			}
		}
		out = append(out, strings.Join(parts, ","))
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestPermanentIDsSchemaEquivalence(t *testing.T) {
	old := openAtPreviousVersion(t, filepath.Join(t.TempDir(), "old.db"))
	applyLaterMigrations(t, old)
	fresh := openTest(t)
	for _, tbl := range idTables {
		for _, p := range []struct {
			name string
			drop []string
			sort bool
		}{
			{"table_info", nil, false},
			{"foreign_key_list", nil, true},
			{"index_list", []string{"seq"}, true},
		} {
			want := pragmaRows(t, old, p.name, tbl, p.drop...)
			got := pragmaRows(t, fresh.db, p.name, tbl, p.drop...)
			if p.sort {
				sort.Strings(want)
				sort.Strings(got)
			}
			if strings.Join(got, "\n") != strings.Join(want, "\n") {
				t.Errorf("%s %s differs:\n got %q\nwant %q", tbl, p.name, got, want)
			}
		}
	}
}

func dataRows(t *testing.T, db *sql.DB) []string {
	t.Helper()
	var out []string
	for _, line := range strings.Split(dbState(t, db), "\n") {
		for _, tbl := range append(append([]string{}, idTables...), "chunks") {
			if strings.HasPrefix(line, tbl+" [") {
				out = append(out, line)
			}
		}
	}
	return out
}

func TestPermanentIDsUpgradePreservesData(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "ralph.db")
	old := openAtPreviousVersion(t, path)
	const now = "2026-01-01T00:00:00Z"
	for _, stmt := range []string{
		`INSERT INTO initiatives (id, slug, title, brief, created_at, updated_at) VALUES (1, 'i', 'I', 'brief', '` + now + `', '` + now + `'), (4, 'j', 'J', '', '` + now + `', '` + now + `')`,
		`INSERT INTO milestones (id, initiative_id, slug, title, description, position, created_at, updated_at, branch, base_commit, head_commit) VALUES (1, 1, 'm1', 'M1', 'desc', 1, '` + now + `', '` + now + `', 'ralph/m1', 'aaa', 'bbb'), (7, 4, 'm2', 'M2', '', 1, '` + now + `', '` + now + `', '', '', '')`,
		`INSERT INTO tasks (id, milestone_id, title, description, acceptance_criteria, status, position, failure_count, blocked_reason, created_at, updated_at, commit_hash) VALUES (1, 1, 'T1', 'd', 'ac', 'DONE', 1, 2, 'r', '` + now + `', '` + now + `', 'abc123'), (9, 7, 'T2', '', '', 'TODO', 1, 0, '', '` + now + `', '` + now + `', '')`,
		`INSERT INTO task_notes (id, task_id, kind, body, created_at) VALUES (1, 1, 'NOTE', 'a', '` + now + `'), (5, 9, 'ITERATION', 'b', '` + now + `')`,
		`INSERT INTO documents (id, initiative_id, kind, slug, title, body, created_at, updated_at) VALUES (1, 1, 'design', 'd', 'D', 'text', '` + now + `', '` + now + `'), (3, NULL, 'note', 'p', 'P', '', '` + now + `', '` + now + `')`,
		`INSERT INTO chunks (owner_kind, owner_id, initiative_id, seq, text, content_hash, model, dims, embedding) VALUES
			('initiative', 1, 1, 0, 'quokka brief', 'h0', '', 0, NULL),
			('milestone', 1, 1, 0, 'quokka alpha', 'h1', 'm', 1, x'00000000'),
			('task', 1, 1, 0, 'gamma', 'h3', 'm', 1, x'00000000'),
			('document', 1, 1, 0, 'delta quokka', 'h4', '', 0, NULL)`,
	} {
		if _, err := old.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("populating: %v", err)
		}
	}
	hits := func(db *sql.DB) string {
		rows, err := db.Query(`SELECT c.owner_kind, c.owner_id, c.seq FROM chunks_fts JOIN chunks c ON c.id = chunks_fts.rowid WHERE chunks_fts MATCH 'quokka' ORDER BY c.id`)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var b strings.Builder
		for rows.Next() {
			var k string
			var id, seq int
			if err := rows.Scan(&k, &id, &seq); err != nil {
				t.Fatal(err)
			}
			fmt.Fprintf(&b, "%s/%d/%d ", k, id, seq)
		}
		return b.String()
	}
	wantRows := dataRows(t, old)
	// Migration 0004 appends an empty archived_at to these two tables.
	for n, line := range wantRows {
		if strings.HasPrefix(line, "initiatives [") || strings.HasPrefix(line, "documents [") {
			wantRows[n] = strings.TrimSuffix(line, "]") + " ]"
		}
		// Migration 0008 appends a NULL origin_initiative_id to documents.
		if strings.HasPrefix(line, "documents [") {
			wantRows[n] = strings.TrimSuffix(wantRows[n], "]") + " <nil>]"
		}
		// Migration 0010 appends an empty parent_card to initiatives.
		if strings.HasPrefix(line, "initiatives [") {
			wantRows[n] = strings.TrimSuffix(wantRows[n], "]") + " ]"
		}
		// Migration 0009 appends the empty pull request record to milestones.
		if strings.HasPrefix(line, "milestones [") {
			wantRows[n] = strings.TrimSuffix(line, "]") + "    0]"
		}
	}
	wantHits := hits(old)
	chunks := count(t, old, `SELECT COUNT(*) FROM chunks`)
	embedded := count(t, old, `SELECT COUNT(*) FROM chunks WHERE embedding IS NOT NULL`)
	if wantHits == "" {
		t.Fatal("known query has no hits before upgrade")
	}
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}

	s, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()
	if got := dataRows(t, s.db); strings.Join(got, "\n") != strings.Join(wantRows, "\n") {
		t.Errorf("rows changed:\n got %q\nwant %q", got, wantRows)
	}
	if got := count(t, s.db, `SELECT COUNT(*) FROM chunks`); got != chunks {
		t.Errorf("chunks = %d, want %d", got, chunks)
	}
	if got := count(t, s.db, `SELECT COUNT(*) FROM chunks WHERE embedding IS NOT NULL`); got != embedded {
		t.Errorf("embedded chunks = %d, want %d", got, embedded)
	}
	if got := hits(s.db); got != wantHits {
		t.Errorf("search hits = %q, want %q", got, wantHits)
	}
	bs, err := filepath.Glob(path + ".pre-*.bak")
	if err != nil || len(bs) != 1 {
		t.Errorf("backups = %v, %v; want exactly one", bs, err)
	}
	requireForeignKeysOn(t, s)

	if err := s.CreateInitiative(ctx, &domain.Initiative{Slug: "k", Title: "K"}); err != nil {
		t.Fatal(err)
	}
	var seq int
	if err := s.db.QueryRow(`SELECT seq FROM sqlite_sequence WHERE name = 'initiatives'`).Scan(&seq); err != nil || seq != 5 {
		t.Errorf("initiatives sequence = %d, %v; want 5", seq, err)
	}
}

func TestPermanentIDsNotReused(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	i, m := seed(t, s)
	task := domain.Task{MilestoneID: m.ID, Title: "T", Status: domain.StatusTodo}
	if err := s.CreateTask(ctx, &task); err != nil {
		t.Fatal(err)
	}
	if err := s.AddTaskNote(ctx, &domain.TaskNote{TaskID: task.ID, Kind: domain.NoteGeneral, Body: "n"}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateDocument(ctx, &domain.Document{InitiativeID: &i.ID, Kind: domain.DocNote, Slug: "d", Title: "D"}); err != nil {
		t.Fatal(err)
	}
	create := map[string]func() (int64, error){
		"task_notes": func() (int64, error) {
			n := domain.TaskNote{TaskID: task.ID, Kind: domain.NoteGeneral, Body: "again"}
			err := s.AddTaskNote(ctx, &n)
			return n.ID, err
		},
		"documents": func() (int64, error) {
			d := domain.Document{InitiativeID: &i.ID, Kind: domain.DocNote, Slug: "d2", Title: "D2"}
			err := s.CreateDocument(ctx, &d)
			return d.ID, err
		},
		"tasks": func() (int64, error) {
			tk := domain.Task{MilestoneID: m.ID, Title: "T2", Status: domain.StatusTodo}
			err := s.CreateTask(ctx, &tk)
			return tk.ID, err
		},
		"milestones": func() (int64, error) {
			mm := domain.Milestone{InitiativeID: i.ID, Slug: "m2", Title: "M2"}
			err := s.CreateMilestone(ctx, &mm)
			return mm.ID, err
		},
		"initiatives": func() (int64, error) {
			in := domain.Initiative{Slug: "other", Title: "Other"}
			err := s.CreateInitiative(ctx, &in)
			return in.ID, err
		},
	}
	// Children first, so each parent still exists when its children are recreated.
	for _, tbl := range []string{"task_notes", "documents", "tasks", "milestones", "initiatives"} {
		var maxID int64
		if err := s.db.QueryRow(`SELECT MAX(id) FROM ` + tbl).Scan(&maxID); err != nil {
			t.Fatal(err)
		}
		if _, err := s.db.Exec(`DELETE FROM `+tbl+` WHERE id = ?`, maxID); err != nil {
			t.Fatal(err)
		}
		id, err := create[tbl]()
		if err != nil {
			t.Fatalf("%s: %v", tbl, err)
		}
		if id <= maxID {
			t.Errorf("%s: new ID %d not greater than deleted ID %d", tbl, id, maxID)
		}
	}
}

func TestPermanentIDsCascadesStillWork(t *testing.T) {
	s, _ := openPopulated(t)
	if _, err := s.db.Exec(`DELETE FROM initiatives WHERE id = 1`); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`SELECT COUNT(*) FROM milestones`,
		`SELECT COUNT(*) FROM tasks`,
		`SELECT COUNT(*) FROM task_notes`,
		`SELECT COUNT(*) FROM documents WHERE initiative_id IS NOT NULL`,
		`SELECT COUNT(*) FROM chunks`,
		`SELECT COUNT(*) FROM chunks_fts WHERE chunks_fts MATCH 'alpha OR beta OR gamma OR delta'`,
	} {
		if n := count(t, s.db, q); n != 0 {
			t.Errorf("%s = %d, want 0", q, n)
		}
	}
	if n := count(t, s.db, `SELECT COUNT(*) FROM documents`); n != 1 {
		t.Errorf("project-scoped documents = %d, want 1", n)
	}
}

func archivedColumn(t *testing.T, db *sql.DB, table string) (typ string, notNull int, dflt string) {
	t.Helper()
	err := db.QueryRow(`SELECT type, "notnull", IFNULL(dflt_value, '') FROM pragma_table_info(?) WHERE name = 'archived_at'`, table).Scan(&typ, &notNull, &dflt)
	if err != nil {
		t.Fatalf("archived_at on %s: %v", table, err)
	}
	return typ, notNull, dflt
}

func TestArchiveMigrationAddsColumns(t *testing.T) {
	check := func(t *testing.T, db *sql.DB) {
		t.Helper()
		for _, table := range []string{"initiatives", "documents"} {
			if typ, nn, dflt := archivedColumn(t, db, table); typ != "TEXT" || nn != 1 || dflt != "''" {
				t.Errorf("%s.archived_at = %s notnull=%d default=%s; want TEXT NOT NULL DEFAULT ''", table, typ, nn, dflt)
			}
		}
	}
	t.Run("fresh", func(t *testing.T) {
		check(t, openTest(t).db)
	})
	t.Run("upgraded", func(t *testing.T) {
		ctx := context.Background()
		path := filepath.Join(t.TempDir(), "ralph.db")
		ms, err := loadMigrations()
		if err != nil {
			t.Fatal(err)
		}
		raw, err := sql.Open("sqlite", "file:"+path+"?_pragma=foreign_keys(1)")
		if err != nil {
			t.Fatal(err)
		}
		raw.SetMaxOpenConns(1)
		if _, err := raw.Exec(`CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY, applied_at TEXT NOT NULL)`); err != nil {
			t.Fatal(err)
		}
		for _, m := range ms {
			if m.name == "0004_archive.sql" {
				break
			}
			if _, err := raw.Exec(m.sql); err != nil {
				t.Fatalf("applying %s: %v", m.name, err)
			}
			if _, err := raw.Exec(`INSERT INTO schema_migrations (version, applied_at) VALUES (?, '2026-01-01T00:00:00Z')`, m.version); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := raw.Exec(`INSERT INTO initiatives (slug, title, brief, created_at, updated_at) VALUES ('a', 'A', '', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`); err != nil {
			t.Fatal(err)
		}
		raw.Close()
		s, err := Open(ctx, path)
		if err != nil {
			t.Fatalf("upgrade: %v", err)
		}
		defer s.Close()
		check(t, s.db)
		i, err := s.GetInitiative(ctx, 1)
		if err != nil || !i.ArchivedAt.IsZero() {
			t.Errorf("upgraded initiative = %+v, %v; want not archived", i, err)
		}
	})
}

func TestSetArchived(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	i, _ := seed(t, s)
	d := domain.Document{InitiativeID: &i.ID, Kind: domain.DocKind("design"), Slug: "d", Title: "D", Body: "b"}
	if err := s.CreateDocument(ctx, &d); err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)

	t.Run("initiative", func(t *testing.T) {
		before, _ := s.GetInitiative(ctx, i.ID)
		if !before.ArchivedAt.IsZero() {
			t.Fatalf("new initiative archived: %v", before.ArchivedAt)
		}
		if err := s.SetInitiativeArchived(ctx, i.ID, at); err != nil {
			t.Fatal(err)
		}
		got, _ := s.GetInitiative(ctx, i.ID)
		if !got.ArchivedAt.Equal(at) || !got.UpdatedAt.Equal(before.UpdatedAt) {
			t.Errorf("archived_at=%v updated_at=%v; want %v and %v", got.ArchivedAt, got.UpdatedAt, at, before.UpdatedAt)
		}
		got.Title = "Renamed"
		if err := s.UpdateInitiative(ctx, &got); err != nil {
			t.Fatal(err)
		}
		stale := before
		stale.Title = "Stale"
		if err := s.UpdateInitiative(ctx, &stale); err != nil {
			t.Fatal(err)
		}
		if after, _ := s.GetInitiative(ctx, i.ID); !after.ArchivedAt.Equal(at) {
			t.Errorf("UpdateInitiative changed archived_at to %v", after.ArchivedAt)
		}
		all, err := s.ListInitiatives(ctx, false)
		if err != nil || len(all) != 1 || !all[0].ArchivedAt.Equal(at) {
			t.Errorf("ListInitiatives = %+v, %v", all, err)
		}
		cur, _ := s.GetInitiative(ctx, i.ID)
		if err := s.SetInitiativeArchived(ctx, i.ID, time.Time{}); err != nil {
			t.Fatal(err)
		}
		cleared, _ := s.GetInitiative(ctx, i.ID)
		if !cleared.ArchivedAt.IsZero() || !cleared.UpdatedAt.Equal(cur.UpdatedAt) {
			t.Errorf("after clear archived_at=%v updated_at=%v; want zero and %v", cleared.ArchivedAt, cleared.UpdatedAt, cur.UpdatedAt)
		}
		if err := s.SetInitiativeArchived(ctx, 999, at); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("unknown initiative: %v, want ErrNotFound", err)
		}
	})

	t.Run("document", func(t *testing.T) {
		before, _ := s.GetDocument(ctx, d.ID)
		if err := s.SetDocumentArchived(ctx, d.ID, at); err != nil {
			t.Fatal(err)
		}
		got, _ := s.GetDocument(ctx, d.ID)
		if !got.ArchivedAt.Equal(at) || !got.UpdatedAt.Equal(before.UpdatedAt) {
			t.Errorf("archived_at=%v updated_at=%v; want %v and %v", got.ArchivedAt, got.UpdatedAt, at, before.UpdatedAt)
		}
		stale := before
		stale.Title = "Stale"
		if err := s.UpdateDocument(ctx, &stale); err != nil {
			t.Fatal(err)
		}
		if after, _ := s.GetDocument(ctx, d.ID); !after.ArchivedAt.Equal(at) {
			t.Errorf("UpdateDocument changed archived_at to %v", after.ArchivedAt)
		}
		docs, err := s.ListDocuments(ctx, store.DocumentFilter{})
		if err != nil || len(docs) != 1 || !docs[0].ArchivedAt.Equal(at) {
			t.Errorf("ListDocuments = %+v, %v", docs, err)
		}
		cur, _ := s.GetDocument(ctx, d.ID)
		if err := s.SetDocumentArchived(ctx, d.ID, time.Time{}); err != nil {
			t.Fatal(err)
		}
		cleared, _ := s.GetDocument(ctx, d.ID)
		if !cleared.ArchivedAt.IsZero() || !cleared.UpdatedAt.Equal(cur.UpdatedAt) {
			t.Errorf("after clear archived_at=%v updated_at=%v; want zero and %v", cleared.ArchivedAt, cleared.UpdatedAt, cur.UpdatedAt)
		}
		if err := s.SetDocumentArchived(ctx, 999, at); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("unknown document: %v, want ErrNotFound", err)
		}
	})
}

func tableCols(t *testing.T, db *sql.DB, table string) map[string]string {
	t.Helper()
	rows, err := db.Query(`SELECT name, type, "notnull", IFNULL(dflt_value, ''), pk FROM pragma_table_info(?)`, table)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var name, typ, dflt string
		var nn, pk int
		if err := rows.Scan(&name, &typ, &nn, &dflt, &pk); err != nil {
			t.Fatal(err)
		}
		out[name] = fmt.Sprintf("%s notnull=%d default=%s pk=%d", typ, nn, dflt, pk)
	}
	return out
}

func TestImportSchema(t *testing.T) {
	s := openTest(t)
	wantImports := map[string]string{
		"id":             "INTEGER notnull=0 default= pk=1",
		"path":           "TEXT notnull=1 default= pk=0",
		"status":         "TEXT notnull=1 default= pk=0",
		"content":        "TEXT notnull=1 default= pk=0",
		"content_hash":   "TEXT notnull=1 default= pk=0",
		"size":           "INTEGER notnull=1 default= pk=0",
		"last_done_hash": "TEXT notnull=1 default='' pk=0",
		"note":           "TEXT notnull=1 default='' pk=0",
		"queued_at":      "TEXT notnull=1 default= pk=0",
		"processed_at":   "TEXT notnull=1 default='' pk=0",
	}
	wantLinks := map[string]string{
		"import_id":   "INTEGER notnull=1 default= pk=1",
		"document_id": "INTEGER notnull=1 default= pk=2",
		"relation":    "TEXT notnull=1 default= pk=3",
		"created_at":  "TEXT notnull=1 default= pk=0",
	}
	for table, want := range map[string]map[string]string{"imports": wantImports, "import_documents": wantLinks} {
		got := tableCols(t, s.db, table)
		if fmt.Sprint(got) != fmt.Sprint(want) {
			t.Errorf("%s columns = %v; want %v", table, got, want)
		}
	}

	rows, err := s.db.Query(`SELECT "table", "from", "to", on_delete FROM pragma_foreign_key_list('import_documents') ORDER BY "from"`)
	if err != nil {
		t.Fatal(err)
	}
	var fks []string
	for rows.Next() {
		var tbl, from, to, onDelete string
		if err := rows.Scan(&tbl, &from, &to, &onDelete); err != nil {
			t.Fatal(err)
		}
		fks = append(fks, fmt.Sprintf("%s->%s.%s %s", from, tbl, to, onDelete))
	}
	rows.Close()
	if want := "[document_id->documents.id CASCADE import_id->imports.id CASCADE]"; fmt.Sprint(fks) != want {
		t.Errorf("foreign keys = %v; want %s", fks, want)
	}

	var autoinc string
	if err := s.db.QueryRow(`SELECT sql FROM sqlite_master WHERE name = 'imports'`).Scan(&autoinc); err != nil || !strings.Contains(autoinc, "AUTOINCREMENT") {
		t.Errorf("imports lacks AUTOINCREMENT: %v", err)
	}

	const ins = `INSERT INTO imports (path, status, content, content_hash, size, queued_at) VALUES (?, ?, '', '', 0, 't')`
	if _, err := s.db.Exec(ins, "a", "bogus"); err == nil {
		t.Error("CHECK should reject an invalid status")
	}
	if _, err := s.db.Exec(ins, "a", "pending"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(ins, "a", "done"); err == nil {
		t.Error("UNIQUE should reject a duplicate path")
	}
	if _, err := s.db.Exec(`INSERT INTO import_documents (import_id, document_id, relation, created_at) VALUES (1, 1, 'bogus', 't')`); err == nil {
		t.Error("CHECK should reject an invalid relation")
	}
}

func TestParseImportStatus(t *testing.T) {
	for _, st := range domain.ImportStatuses() {
		if got, err := domain.ParseImportStatus(" " + strings.ToUpper(string(st)) + " "); err != nil || got != st {
			t.Errorf("ParseImportStatus(%q) = %q, %v", st, got, err)
		}
	}
	if _, err := domain.ParseImportStatus("nope"); !errors.Is(err, domain.ErrInvalid) {
		t.Errorf("unknown status: %v", err)
	}
}

func TestImportCRUD(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	q0 := time.Date(2026, 1, 2, 3, 4, 5, 6, time.UTC)
	mk := func(path string, st domain.ImportStatus) *domain.Import {
		return &domain.Import{Path: path, Status: st, Content: "body of " + path, ContentHash: "h-" + path, Size: 42, QueuedAt: q0}
	}
	b, a, c := mk("docs/b.md", domain.ImportPending), mk("a.md", domain.ImportDone), mk("docs/c.md", domain.ImportDeferred)
	a.LastDoneHash, a.ProcessedAt = "h-a.md", q0.Add(time.Hour)
	c.Note = "open items"
	for _, im := range []*domain.Import{b, a, c} {
		if err := s.CreateImport(ctx, im); err != nil || im.ID == 0 {
			t.Fatalf("CreateImport(%s): id=%d, %v", im.Path, im.ID, err)
		}
	}
	if err := s.CreateImport(ctx, mk("a.md", domain.ImportPending)); !errors.Is(err, store.ErrConflict) {
		t.Errorf("duplicate path: %v; want ErrConflict", err)
	}

	for _, want := range []*domain.Import{a, b, c} {
		got, err := s.GetImportByPath(ctx, want.Path)
		if err != nil || got != *want {
			t.Errorf("GetImportByPath(%s) = %+v, %v; want %+v", want.Path, got, err, *want)
		}
	}
	if got, _ := s.GetImportByPath(ctx, "a.md"); got.ProcessedAt.IsZero() {
		t.Error("processed_at did not round-trip")
	}
	if got, _ := s.GetImportByPath(ctx, "docs/b.md"); !got.ProcessedAt.IsZero() || got.LastDoneHash != "" {
		t.Errorf("zero values should round-trip as empty: %+v", got)
	}
	if _, err := s.GetImportByPath(ctx, "missing.md"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("unknown path: %v; want ErrNotFound", err)
	}

	paths := func(ims []domain.Import) string {
		var ps []string
		for _, im := range ims {
			ps = append(ps, im.Path)
		}
		return strings.Join(ps, ",")
	}
	all, err := s.ListImports(ctx, nil)
	if err != nil || paths(all) != "a.md,docs/b.md,docs/c.md" {
		t.Errorf("list all = %s, %v", paths(all), err)
	}
	some, err := s.ListImports(ctx, []domain.ImportStatus{domain.ImportPending, domain.ImportDeferred})
	if err != nil || paths(some) != "docs/b.md,docs/c.md" {
		t.Errorf("list filtered = %s, %v", paths(some), err)
	}
	if none, err := s.ListImports(ctx, []domain.ImportStatus{domain.ImportSkipped}); err != nil || len(none) != 0 {
		t.Errorf("list skipped = %v, %v", none, err)
	}

	id := b.ID
	upd := domain.Import{
		ID: 999, Path: "docs/b.md", Status: domain.ImportDone, Content: "new", ContentHash: "h2", Size: 3,
		LastDoneHash: "h2", Note: "n", QueuedAt: q0.Add(2 * time.Hour), ProcessedAt: q0.Add(3 * time.Hour),
	}
	if err := s.UpdateImport(ctx, &upd); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetImportByPath(ctx, "docs/b.md")
	upd.ID = id
	if err != nil || got != upd {
		t.Errorf("after update = %+v, %v; want %+v", got, err, upd)
	}
	if err := s.UpdateImport(ctx, &domain.Import{Path: "missing.md", Status: domain.ImportPending}); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("update unknown path: %v; want ErrNotFound", err)
	}
	if err := s.CreateImport(ctx, &domain.Import{Path: "x.md", Status: "bogus"}); err == nil {
		t.Error("CHECK should reject an invalid status")
	}
}

func TestImportLinks(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	tick := 0
	s.now = func() time.Time { tick++; return base.Add(time.Duration(tick) * time.Second) }

	im := domain.Import{Path: "a.md", Status: domain.ImportPending, QueuedAt: base}
	if err := s.CreateImport(ctx, &im); err != nil {
		t.Fatal(err)
	}
	var docs []domain.Document
	for _, slug := range []string{"one", "two", "three"} {
		d := domain.Document{Kind: domain.DocReference, Slug: slug, Title: slug}
		if err := s.CreateDocument(ctx, &d); err != nil {
			t.Fatal(err)
		}
		docs = append(docs, d)
	}

	if err := s.AddImportLink(ctx, im.ID, docs[1].ID, domain.ImportCreated); err != nil {
		t.Fatal(err)
	}
	if err := s.AddImportLink(ctx, im.ID, docs[0].ID, domain.ImportUpdated); err != nil {
		t.Fatal(err)
	}
	before, _ := s.ListImportLinks(ctx, im.ID)
	if err := s.AddImportLink(ctx, im.ID, docs[1].ID, domain.ImportCreated); err != nil {
		t.Errorf("repeated link: %v; want no-op", err)
	}
	if err := s.AddImportLink(ctx, im.ID, docs[1].ID, domain.ImportDecision); err != nil {
		t.Fatal(err)
	}
	links, err := s.ListImportLinks(ctx, im.ID)
	if err != nil || len(links) != 3 {
		t.Fatalf("links = %+v, %v; want 3", links, err)
	}
	if links[0] != before[0] || links[1] != before[1] {
		t.Errorf("repeat changed existing links: %+v vs %+v", links[:2], before)
	}
	want := []struct {
		doc int64
		rel domain.ImportRelation
	}{{docs[1].ID, domain.ImportCreated}, {docs[0].ID, domain.ImportUpdated}, {docs[1].ID, domain.ImportDecision}}
	for i, w := range want {
		if links[i].DocumentID != w.doc || links[i].Relation != w.rel {
			t.Errorf("link %d = %+v; want doc %d %s", i, links[i], w.doc, w.rel)
		}
	}

	// Equal timestamps order by document ID.
	s.now = func() time.Time { return base.Add(time.Hour) }
	for _, d := range []domain.Document{docs[2], docs[0]} {
		if err := s.AddImportLink(ctx, im.ID, d.ID, domain.ImportDecision); err != nil {
			t.Fatal(err)
		}
	}
	links, _ = s.ListImportLinks(ctx, im.ID)
	if n := len(links); n != 5 || links[3].DocumentID != docs[0].ID || links[4].DocumentID != docs[2].ID {
		t.Errorf("tie order = %+v", links)
	}

	if err := s.AddImportLink(ctx, im.ID, 9999, domain.ImportCreated); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("missing document: %v; want ErrNotFound", err)
	}
	if links, err := s.ListImportLinks(ctx, 9999); err != nil || len(links) != 0 {
		t.Errorf("links of unknown import = %v, %v", links, err)
	}
}

func TestImportLinkCascades(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	mkImport := func(path string) domain.Import {
		im := domain.Import{Path: path, Status: domain.ImportPending, QueuedAt: time.Now().UTC()}
		if err := s.CreateImport(ctx, &im); err != nil {
			t.Fatal(err)
		}
		return im
	}
	mkDoc := func(slug string) domain.Document {
		d := domain.Document{Kind: domain.DocReference, Slug: slug, Title: slug}
		if err := s.CreateDocument(ctx, &d); err != nil {
			t.Fatal(err)
		}
		return d
	}
	i1, i2 := mkImport("a.md"), mkImport("b.md")
	d1, d2 := mkDoc("one"), mkDoc("two")
	for _, im := range []domain.Import{i1, i2} {
		for _, d := range []domain.Document{d1, d2} {
			if err := s.AddImportLink(ctx, im.ID, d.ID, domain.ImportCreated); err != nil {
				t.Fatal(err)
			}
		}
	}
	linkCount := func() int { return count(t, s.db, `SELECT COUNT(*) FROM import_documents`) }
	if linkCount() != 4 {
		t.Fatalf("links = %d; want 4", linkCount())
	}

	if err := s.DeleteDocument(ctx, d1.ID); err != nil {
		t.Fatal(err)
	}
	if n := count(t, s.db, fmt.Sprintf(`SELECT COUNT(*) FROM import_documents WHERE document_id = %d`, d1.ID)); n != 0 || linkCount() != 2 {
		t.Errorf("after deleting a document: its links = %d, total = %d; want 0 and 2", n, linkCount())
	}
	if _, err := s.db.Exec(`DELETE FROM imports WHERE id = ?`, i1.ID); err != nil {
		t.Fatal(err)
	}
	if links, _ := s.ListImportLinks(ctx, i1.ID); len(links) != 0 || linkCount() != 1 {
		t.Errorf("after deleting an import: its links = %v, total = %d; want none and 1", links, linkCount())
	}
	if _, err := s.GetImportByPath(ctx, "b.md"); err != nil {
		t.Errorf("deleting a document removed an import: %v", err)
	}
}
