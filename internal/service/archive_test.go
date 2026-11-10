package service

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bengearig/ralph/internal/domain"
	"github.com/bengearig/ralph/internal/looplock"
	"github.com/bengearig/ralph/internal/search"
	"github.com/bengearig/ralph/internal/store/sqlite"
)

func TestArchiveRoundTrip(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	ini, _ := e.scaffold(t)
	d := e.doc(t, ini, "body")

	for range 2 {
		v, err := e.svc.ArchiveInitiative(ctx, ini)
		if err != nil || v.ArchivedAt == "" {
			t.Fatalf("archive initiative: %+v, %v", v, err)
		}
	}
	first, _ := e.svc.GetInitiative(ctx, ini)
	again, _ := e.svc.ArchiveInitiative(ctx, ini)
	if first.ArchivedAt != again.ArchivedAt {
		t.Errorf("repeat archive changed archived_at: %q -> %q", first.ArchivedAt, again.ArchivedAt)
	}
	for range 2 {
		v, err := e.svc.UnarchiveInitiative(ctx, ini)
		if err != nil || v.ArchivedAt != "" {
			t.Fatalf("unarchive initiative: %+v, %v", v, err)
		}
	}

	for range 2 {
		v, err := e.svc.ArchiveDocument(ctx, d)
		if err != nil || v.ArchivedAt == "" || !v.Archived {
			t.Fatalf("archive document: %+v, %v", v, err)
		}
	}
	for range 2 {
		v, err := e.svc.UnarchiveDocument(ctx, d)
		if err != nil || v.ArchivedAt != "" || v.Archived {
			t.Fatalf("unarchive document: %+v, %v", v, err)
		}
	}
	for name, err := range map[string]error{
		"initiative": get(e.svc.ArchiveInitiative(ctx, 99)),
		"unarchive":  get(e.svc.UnarchiveInitiative(ctx, 99)),
		"document":   get(e.svc.ArchiveDocument(ctx, 99)),
		"undocument": get(e.svc.UnarchiveDocument(ctx, 99)),
	} {
		if err == nil {
			t.Errorf("%s: unknown ID accepted", name)
		}
	}
}

func TestArchiveLoopLock(t *testing.T) {
	ctx := context.Background()
	st, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "r.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	lockPath := filepath.Join(t.TempDir(), "loop.lock")
	ix := &search.Index{Store: st, Chunker: search.Chunker{MaxChars: 1000, OverlapChars: 100}}
	e := env{svc: New(st, ix, Options{LoopLockPath: lockPath})}
	ini, ms := e.scaffold(t)
	otherIni, err := e.svc.CreateInitiative(ctx, InitiativeInput{Title: "Other"})
	if err != nil {
		t.Fatal(err)
	}
	otherID := mustID(t, domain.KindInitiative, otherIni.ID)

	lock, err := looplock.Acquire(lockPath, "milestone "+domain.FormatID(domain.KindMilestone, ms))
	if err != nil {
		t.Fatal(err)
	}
	if _, held, _ := looplock.Probe(lockPath); !held {
		lock.Release()
		t.Skip("locking is a no-op on this platform")
	}
	defer lock.Release()

	if _, err := e.svc.ArchiveInitiative(ctx, ini); !errors.Is(err, looplock.ErrLoopRunning) {
		t.Errorf("archive with lock held: %v", err)
	}
	if v, _ := e.svc.GetInitiative(ctx, ini); v.ArchivedAt != "" {
		t.Error("refused archive still archived")
	}
	if _, err := e.svc.ArchiveInitiative(ctx, otherID); err != nil {
		t.Errorf("other initiative: %v", err)
	}
}

func TestArchivedInitiativeIsReadOnly(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	ini, ms := e.scaffold(t)
	task := e.task(t, ms, domain.StatusDone)
	todo := e.task(t, ms, domain.StatusProposed)
	doc := e.doc(t, ini, "body")
	title := "changed"

	if _, err := e.svc.ArchiveInitiative(ctx, ini); err != nil {
		t.Fatal(err)
	}

	calls := map[string]func() error{
		"UpdateInitiative": func() error { return get(e.svc.UpdateInitiative(ctx, ini, InitiativePatch{Title: &title})) },
		"CreateMilestone":  func() error { return get(e.svc.CreateMilestone(ctx, MilestoneInput{InitiativeID: ini, Title: "new"})) },
		"UpdateMilestone":  func() error { return get(e.svc.UpdateMilestone(ctx, ms, MilestonePatch{Title: &title})) },
		"CreateTask":       func() error { return get(e.svc.CreateTask(ctx, TaskInput{MilestoneID: ms, Title: "new"})) },
		"UpdateTask":       func() error { return get(e.svc.UpdateTask(ctx, todo, TaskPatch{Title: &title})) },
		"SetTaskStatus":    func() error { return get(e.svc.SetTaskStatus(ctx, todo, domain.StatusTodo, "")) },
		"AddTaskNote":      func() error { return get(e.svc.AddTaskNote(ctx, todo, domain.NoteGeneral, "x")) },
		"ReopenTask":       func() error { return get(e.svc.ReopenTask(ctx, task, "lost")) },
		"CreateDocument": func() error {
			return get(e.svc.CreateDocument(ctx, DocumentInput{InitiativeID: ini, Kind: domain.DocDesign, Title: "new"}))
		},
		"UpdateDocument":      func() error { return get(e.svc.UpdateDocument(ctx, doc, DocumentPatch{Title: &title})) },
		"RecordMilestoneGit":  func() error { return e.svc.RecordMilestoneGit(ctx, ms, domain.MilestoneGit{Branch: "b"}) },
		"RecordMilestoneHead": func() error { return e.svc.RecordMilestoneHead(ctx, ms, "") },
		"RecordTaskCommit":    func() error { return e.svc.RecordTaskCommit(ctx, task, "") },
	}
	snap := func() string {
		i, _ := e.svc.GetInitiative(ctx, ini)
		m, _ := e.svc.GetMilestone(ctx, ms)
		tk, _ := e.svc.GetTask(ctx, task)
		td, _ := e.svc.GetTask(ctx, todo)
		d, _ := e.svc.GetDocument(ctx, doc)
		ts, _ := e.svc.ListTasks(ctx, TaskFilter{InitiativeID: ini})
		ds, _ := e.svc.ListDocuments(ctx, DocumentFilter{InitiativeID: ini})
		ms, _ := e.svc.ListMilestones(ctx, ini, ListOptions{})
		return strings.Join([]string{
			toJSON(t, i), toJSON(t, m), toJSON(t, tk), toJSON(t, td), toJSON(t, d), toJSON(t, ts), toJSON(t, ds), toJSON(t, ms),
		}, "\n")
	}
	before := snap()
	for name, call := range calls {
		err := call()
		if !errors.Is(err, ErrArchived) {
			t.Errorf("%s: err = %v, want ErrArchived", name, err)
			continue
		}
		if !strings.Contains(err.Error(), "I-1 is archived; unarchive it first (ralph initiative unarchive I-1)") {
			t.Errorf("%s: message = %q", name, err)
		}
	}
	if after := snap(); after != before {
		t.Errorf("refused calls changed state:\n%s\n---\n%s", before, after)
	}

	if _, err := e.svc.UnarchiveInitiative(ctx, ini); err != nil {
		t.Fatal(err)
	}
	for name, call := range calls {
		if name == "RecordMilestoneHead" || name == "RecordTaskCommit" {
			continue
		}
		if err := call(); errors.Is(err, ErrArchived) {
			t.Errorf("%s after unarchive: %v", name, err)
		}
	}
	if err := e.svc.RecordMilestoneHead(ctx, ms, ""); err != nil {
		t.Errorf("RecordMilestoneHead after unarchive: %v", err)
	}
	if err := e.svc.RecordTaskCommit(ctx, task, ""); err != nil {
		t.Errorf("RecordTaskCommit after unarchive: %v", err)
	}
}

func TestArchivedDocumentOnly(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	ini, ms := e.scaffold(t)
	project := e.doc(t, 0, "project")
	scoped := e.doc(t, ini, "scoped")
	title := "changed"

	if _, err := e.svc.ArchiveDocument(ctx, project); err != nil {
		t.Fatal(err)
	}
	err := get(e.svc.UpdateDocument(ctx, project, DocumentPatch{Title: &title}))
	if !errors.Is(err, ErrArchived) || !strings.Contains(err.Error(), "ralph doc unarchive D-1") {
		t.Errorf("update archived document: %v", err)
	}
	if err := get(e.svc.UpdateDocument(ctx, scoped, DocumentPatch{Title: &title})); err != nil {
		t.Errorf("other document: %v", err)
	}
	if err := get(e.svc.UpdateInitiative(ctx, ini, InitiativePatch{Title: &title})); err != nil {
		t.Errorf("initiative: %v", err)
	}
	if err := get(e.svc.CreateTask(ctx, TaskInput{MilestoneID: ms, Title: "t"})); err != nil {
		t.Errorf("task: %v", err)
	}
	if _, err := e.svc.DeleteDocument(ctx, project); err != nil {
		t.Errorf("delete archived document: %v", err)
	}
}

func TestUnarchiveDocumentInArchivedInitiative(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	ini, _ := e.scaffold(t)
	doc := e.doc(t, ini, "body")
	title := "changed"
	if _, err := e.svc.ArchiveDocument(ctx, doc); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.ArchiveInitiative(ctx, ini); err != nil {
		t.Fatal(err)
	}
	v, err := e.svc.UnarchiveDocument(ctx, doc)
	if err != nil || !v.Archived || v.ArchivedAt != "" {
		t.Fatalf("view = %+v, err = %v", v, err)
	}
	if err := get(e.svc.UpdateDocument(ctx, doc, DocumentPatch{Title: &title})); !errors.Is(err, ErrArchived) {
		t.Errorf("update: %v", err)
	}
}

func TestDeleteArchived(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	ini, ms := e.scaffold(t)
	e.task(t, ms, domain.StatusDone)
	doc := e.doc(t, ini, "body")
	if _, err := e.svc.ArchiveInitiative(ctx, ini); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.DeleteDocument(ctx, doc); err != nil {
		t.Errorf("delete document in archived initiative: %v", err)
	}
	if _, err := e.svc.DeleteInitiative(ctx, ini, true); err != nil {
		t.Errorf("force delete archived initiative: %v", err)
	}
}

func toJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
