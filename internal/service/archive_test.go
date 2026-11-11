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
	"github.com/bengearig/ralph/internal/store"
	"github.com/bengearig/ralph/internal/store/sqlite"
)

func TestArchiveRoundTrip(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	ini, _ := e.scaffold(t)
	d := e.doc(t, ini, "body")

	for range 2 {
		v, err := e.svc.ArchiveInitiative(ctx, ini, DocumentChoice{None: true})
		if err != nil || v.ArchivedAt == "" {
			t.Fatalf("archive initiative: %+v, %v", v, err)
		}
	}
	first, _ := e.svc.GetInitiative(ctx, ini)
	again, _ := e.svc.ArchiveInitiative(ctx, ini, DocumentChoice{})
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
		"initiative": get(e.svc.ArchiveInitiative(ctx, 99, DocumentChoice{})),
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

	if _, err := e.svc.ArchiveInitiative(ctx, ini, DocumentChoice{}); !errors.Is(err, looplock.ErrLoopRunning) {
		t.Errorf("archive with lock held: %v", err)
	}
	if v, _ := e.svc.GetInitiative(ctx, ini); v.ArchivedAt != "" {
		t.Error("refused archive still archived")
	}
	if _, err := e.svc.ArchiveInitiative(ctx, otherID, DocumentChoice{}); err != nil {
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

	if _, err := e.svc.ArchiveInitiative(ctx, ini, DocumentChoice{None: true}); err != nil {
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
	if _, err := e.svc.ArchiveInitiative(ctx, ini, DocumentChoice{None: true}); err != nil {
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
	if _, err := e.svc.ArchiveInitiative(ctx, ini, DocumentChoice{None: true}); err != nil {
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

func TestNextTaskRefusesArchivedInitiative(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	ini, m := e.scaffold(t)
	if _, err := e.svc.ArchiveInitiative(ctx, ini, DocumentChoice{None: true}); err != nil {
		t.Fatal(err)
	}
	v, err := e.svc.NextTask(ctx, m)
	if !errors.Is(err, ErrArchived) || v != nil || !strings.Contains(err.Error(), "ralph initiative unarchive I-1") {
		t.Fatalf("NextTask = %+v, %v; want ErrArchived with unarchive hint", v, err)
	}
	if _, err := e.svc.UnarchiveInitiative(ctx, ini); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.NextTask(ctx, m); err != nil {
		t.Errorf("NextTask after unarchive: %v", err)
	}
}

func (e env) kindDoc(t *testing.T, ini int64, kind domain.DocKind, title string) int64 {
	t.Helper()
	v, err := e.svc.CreateDocument(context.Background(), DocumentInput{InitiativeID: ini, Kind: kind, Title: title, Body: "body"})
	if err != nil {
		t.Fatal(err)
	}
	return mustID(t, domain.KindDocument, v.ID)
}

func (e env) docScope(t *testing.T, id int64) (inInitiative bool, promoted bool) {
	t.Helper()
	d, err := e.svc.Store().GetDocument(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return d.InitiativeID != nil, d.InitiativeID == nil && d.OriginInitiativeID != nil
}

func TestArchiveDocumentChoiceRequired(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	ini, _ := e.scaffold(t)
	design := e.kindDoc(t, ini, domain.DocDesign, "The design")
	note := e.kindDoc(t, ini, domain.DocNote, "A note")
	decision := e.kindDoc(t, ini, domain.DocDecision, "A decision")
	hidden := e.kindDoc(t, ini, domain.DocReference, "Hidden")
	if _, err := e.svc.ArchiveDocument(ctx, hidden); err != nil {
		t.Fatal(err)
	}

	_, err := e.svc.ArchiveInitiative(ctx, ini, DocumentChoice{})
	if !errors.Is(err, ErrDocumentChoiceRequired) {
		t.Fatalf("err = %v", err)
	}
	var ce *DocumentChoiceError
	if !errors.As(err, &ce) {
		t.Fatalf("not a DocumentChoiceError: %v", err)
	}
	ref := func(id int64) string { return domain.FormatID(domain.KindDocument, id) }
	if len(ce.Documents) != 3 || ce.Documents[0].ID != ref(design) || ce.Documents[1].ID != ref(note) || ce.Documents[2].ID != ref(decision) {
		t.Errorf("documents = %+v", ce.Documents)
	}
	if len(ce.Suggested) != 2 || ce.Suggested[0].ID != ref(design) || ce.Suggested[1].ID != ref(decision) {
		t.Errorf("suggested = %+v", ce.Suggested)
	}
	for _, want := range []string{ref(design) + "  design  \"The design\"", ref(note) + "  note  \"A note\"", ref(design) + ", " + ref(decision)} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("message lacks %q:\n%s", want, err)
		}
	}
	if strings.Contains(err.Error(), ref(hidden)) {
		t.Errorf("archived document listed:\n%s", err)
	}
	if v, _ := e.svc.GetInitiative(ctx, ini); v.ArchivedAt != "" {
		t.Error("refused archive still archived")
	}
	for _, id := range []int64{design, note, decision} {
		if in, _ := e.docScope(t, id); !in {
			t.Errorf("%s changed", ref(id))
		}
	}
}

func TestArchiveWithoutCandidates(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	ini, _ := e.scaffold(t)
	if _, err := e.svc.CreateDocument(ctx, DocumentInput{Kind: domain.DocNote, Title: "Project", Body: "b"}); err != nil {
		t.Fatal(err)
	}
	promoted := e.kindDoc(t, ini, domain.DocDesign, "Promoted")
	if _, err := e.svc.PromoteDocument(ctx, promoted, ""); err != nil {
		t.Fatal(err)
	}
	if v, err := e.svc.ArchiveInitiative(ctx, ini, DocumentChoice{}); err != nil || v.ArchivedAt == "" || len(v.PromotedDocuments) != 0 {
		t.Fatalf("archive: %+v, %v", v, err)
	}
	other, _ := e.svc.CreateInitiative(ctx, InitiativeInput{Title: "Other"})
	oid := mustID(t, domain.KindInitiative, other.ID)
	if _, err := e.svc.ArchiveInitiative(ctx, oid, DocumentChoice{All: true}); err != nil {
		t.Errorf("All with no candidates: %v", err)
	}
	third, _ := e.svc.CreateInitiative(ctx, InitiativeInput{Title: "Third"})
	if _, err := e.svc.ArchiveInitiative(ctx, mustID(t, domain.KindInitiative, third.ID), DocumentChoice{IDs: []int64{promoted}}); !errors.Is(err, domain.ErrInvalid) {
		t.Errorf("ID list with no candidates: %v", err)
	}
}

func TestArchiveDocumentChoices(t *testing.T) {
	setup := func(t *testing.T) (env, int64, [3]int64) {
		e := newEnv(t)
		ini, _ := e.scaffold(t)
		return e, ini, [3]int64{
			e.kindDoc(t, ini, domain.DocDesign, "One"),
			e.kindDoc(t, ini, domain.DocNote, "Two"),
			e.kindDoc(t, ini, domain.DocDecision, "Three"),
		}
	}
	ctx := context.Background()
	ref := func(id int64) string { return domain.FormatID(domain.KindDocument, id) }

	t.Run("all", func(t *testing.T) {
		e, ini, ds := setup(t)
		v, err := e.svc.ArchiveInitiative(ctx, ini, DocumentChoice{All: true})
		if err != nil || v.ArchivedAt == "" || len(v.PromotedDocuments) != 3 || v.PromotedDocuments[0] != ref(ds[0]) {
			t.Fatalf("%+v, %v", v, err)
		}
		for _, id := range ds {
			if _, p := e.docScope(t, id); !p {
				t.Errorf("%s not promoted", ref(id))
			}
		}
	})
	t.Run("none", func(t *testing.T) {
		e, ini, ds := setup(t)
		v, err := e.svc.ArchiveInitiative(ctx, ini, DocumentChoice{None: true})
		if err != nil || v.ArchivedAt == "" || len(v.PromotedDocuments) != 0 {
			t.Fatalf("%+v, %v", v, err)
		}
		for _, id := range ds {
			if in, _ := e.docScope(t, id); !in {
				t.Errorf("%s moved", ref(id))
			}
		}
	})
	t.Run("ids", func(t *testing.T) {
		e, ini, ds := setup(t)
		v, err := e.svc.ArchiveInitiative(ctx, ini, DocumentChoice{IDs: []int64{ds[2], ds[0], ds[2]}})
		if err != nil || len(v.PromotedDocuments) != 2 || v.PromotedDocuments[0] != ref(ds[2]) || v.PromotedDocuments[1] != ref(ds[0]) {
			t.Fatalf("%+v, %v", v, err)
		}
		if _, p := e.docScope(t, ds[0]); !p {
			t.Error("first not promoted")
		}
		if in, _ := e.docScope(t, ds[1]); !in {
			t.Error("second moved")
		}
		if _, p := e.docScope(t, ds[2]); !p {
			t.Error("third not promoted")
		}
	})
	t.Run("invalid", func(t *testing.T) {
		e, ini, ds := setup(t)
		otherIni, _ := e.svc.CreateInitiative(ctx, InitiativeInput{Title: "Other"})
		foreign := e.kindDoc(t, mustID(t, domain.KindInitiative, otherIni.ID), domain.DocDesign, "Foreign")
		own := e.kindDoc(t, ini, domain.DocNote, "Own hidden")
		if _, err := e.svc.ArchiveDocument(ctx, own); err != nil {
			t.Fatal(err)
		}
		for _, bad := range []int64{foreign, own, 9999} {
			_, err := e.svc.ArchiveInitiative(ctx, ini, DocumentChoice{IDs: []int64{ds[0], bad}})
			if !errors.Is(err, domain.ErrInvalid) || !strings.Contains(err.Error(), ref(bad)) {
				t.Errorf("%s: %v", ref(bad), err)
			}
		}
		if v, _ := e.svc.GetInitiative(ctx, ini); v.ArchivedAt != "" {
			t.Error("archived")
		}
		if in, _ := e.docScope(t, ds[0]); !in {
			t.Error("promoted despite invalid list")
		}
	})
}

func TestArchiveSlugClashRollsBack(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	ini, _ := e.scaffold(t)
	first := e.kindDoc(t, ini, domain.DocDesign, "Alpha")
	second := e.kindDoc(t, ini, domain.DocDesign, "Beta")
	if _, err := e.svc.CreateDocument(ctx, DocumentInput{Kind: domain.DocNote, Title: "Beta", Body: "b"}); err != nil {
		t.Fatal(err)
	}
	secondRef := domain.FormatID(domain.KindDocument, second)

	_, err := e.svc.ArchiveInitiative(ctx, ini, DocumentChoice{All: true})
	if !errors.Is(err, store.ErrConflict) {
		t.Fatalf("err = %v", err)
	}
	for _, want := range []string{secondRef, `"beta"`, "on its own with a new slug"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("message lacks %q: %v", want, err)
		}
	}
	if v, _ := e.svc.GetInitiative(ctx, ini); v.ArchivedAt != "" {
		t.Error("archived despite clash")
	}
	for _, id := range []int64{first, second} {
		if in, _ := e.docScope(t, id); !in {
			t.Errorf("D-%d promoted despite rollback", id)
		}
	}

	if _, err := e.svc.PromoteDocument(ctx, second, "beta-2"); err != nil {
		t.Fatal(err)
	}
	v, err := e.svc.ArchiveInitiative(ctx, ini, DocumentChoice{All: true})
	if err != nil || v.ArchivedAt == "" || len(v.PromotedDocuments) != 1 {
		t.Fatalf("retry: %+v, %v", v, err)
	}
}

func TestArchiveIdempotentWithDocuments(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	ini, _ := e.scaffold(t)
	e.kindDoc(t, ini, domain.DocDesign, "Kept")
	if _, err := e.svc.ArchiveInitiative(ctx, ini, DocumentChoice{None: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.ArchiveInitiative(ctx, ini, DocumentChoice{}); err != nil {
		t.Errorf("repeat archive refused: %v", err)
	}
}
