package service

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/bengearig/ralph/internal/domain"
	"github.com/bengearig/ralph/internal/looplock"
	"github.com/bengearig/ralph/internal/search"
	"github.com/bengearig/ralph/internal/store"
	"github.com/bengearig/ralph/internal/store/sqlite"
)

func (e env) task(t *testing.T, milestone int64, status domain.Status) int64 {
	t.Helper()
	ctx := context.Background()
	v, err := e.svc.CreateTask(ctx, TaskInput{MilestoneID: milestone, Title: "work", Description: "d", AcceptanceCriteria: "c"})
	if err != nil {
		t.Fatal(err)
	}
	id := mustID(t, domain.KindTask, v.ID)
	steps := map[domain.Status][]domain.Status{
		domain.StatusProposed:   nil,
		domain.StatusInProgress: {domain.StatusTodo, domain.StatusInProgress},
		domain.StatusDone:       {domain.StatusTodo, domain.StatusInProgress, domain.StatusDone},
	}[status]
	for _, to := range steps {
		if _, err := e.svc.SetTaskStatus(ctx, id, to, ""); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := e.svc.AddTaskNote(ctx, id, domain.NoteGeneral, "a note"); err != nil {
		t.Fatal(err)
	}
	return id
}

func (e env) doc(t *testing.T, initiative int64, body string) int64 {
	t.Helper()
	v, err := e.svc.CreateDocument(context.Background(), DocumentInput{InitiativeID: initiative, Kind: domain.DocDesign, Title: "Doc", Body: body})
	if err != nil {
		t.Fatal(err)
	}
	return mustID(t, domain.KindDocument, v.ID)
}

func (e env) chunkHits(t *testing.T, word string) int {
	t.Helper()
	hits, err := e.svc.Store().KeywordSearch(context.Background(), word, store.ChunkFilter{}, 50)
	if err != nil {
		t.Fatal(err)
	}
	return len(hits)
}

func wantDeleted(t *testing.T, got DeleteView, id, kind string, deleted map[string]int) {
	t.Helper()
	want := DeleteView{ID: id, Kind: kind, Deleted: deleted}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("view = %+v, want %+v", got, want)
	}
}

func TestDeleteSimpleTargets(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	ini, ms := e.scaffold(t)
	m2, err := e.svc.CreateMilestone(ctx, MilestoneInput{InitiativeID: ini, Title: "Second"})
	if err != nil {
		t.Fatal(err)
	}
	m2id := mustID(t, domain.KindMilestone, m2.ID)
	tk := e.task(t, ms, domain.StatusProposed)
	d := e.doc(t, 0, "quokkaword body")
	if e.chunkHits(t, "quokkaword") == 0 {
		t.Fatal("document was not indexed")
	}

	v, err := e.svc.DeleteMilestone(ctx, m2id, false)
	if err != nil {
		t.Fatal(err)
	}
	wantDeleted(t, v, m2.ID, "milestone", map[string]int{"milestones": 1})
	if _, err := e.svc.Store().GetMilestone(ctx, m2id); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("milestone still there: %v", err)
	}

	v, err = e.svc.DeleteTask(ctx, tk, false)
	if err != nil {
		t.Fatal(err)
	}
	wantDeleted(t, v, domain.FormatID(domain.KindTask, tk), "task", map[string]int{"tasks": 1, "notes": 1})
	if _, err := e.svc.Store().GetTask(ctx, tk); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("task still there: %v", err)
	}

	v, err = e.svc.DeleteDocument(ctx, d)
	if err != nil {
		t.Fatal(err)
	}
	wantDeleted(t, v, domain.FormatID(domain.KindDocument, d), "document", map[string]int{"documents": 1})
	if _, err := e.svc.Store().GetDocument(ctx, d); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("document still there: %v", err)
	}
	if n := e.chunkHits(t, "quokkaword"); n != 0 {
		t.Errorf("%d document chunks remain", n)
	}

	if _, err := e.svc.DeleteMilestone(ctx, ms, false); err != nil {
		t.Fatal(err)
	}
	v, err = e.svc.DeleteInitiative(ctx, ini, false)
	if err != nil {
		t.Fatal(err)
	}
	wantDeleted(t, v, domain.FormatID(domain.KindInitiative, ini), "initiative", map[string]int{"initiatives": 1})
	if _, err := e.svc.Store().GetInitiative(ctx, ini); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("initiative still there: %v", err)
	}
	if n := e.chunkHits(t, "SSO"); n != 0 {
		t.Errorf("%d initiative chunks remain", n)
	}
}

func TestDeleteRefusalsNeedForce(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	ini, ms := e.scaffold(t)
	docOnly, err := e.svc.CreateInitiative(ctx, InitiativeInput{Title: "Docs only"})
	if err != nil {
		t.Fatal(err)
	}
	docOnlyID := mustID(t, domain.KindInitiative, docOnly.ID)
	e.doc(t, docOnlyID, "body")
	for i := 0; i < 2; i++ {
		e.task(t, ms, domain.StatusProposed)
	}
	running := e.task(t, ms, domain.StatusInProgress)
	done := e.task(t, ms, domain.StatusDone)

	cases := []struct {
		name string
		del  func() error
		want string
	}{
		{"initiative with milestones", func() error { _, err := e.svc.DeleteInitiative(ctx, ini, false); return err }, "1 milestone"},
		{"initiative with documents", func() error { _, err := e.svc.DeleteInitiative(ctx, docOnlyID, false); return err }, "1 document"},
		{"milestone with tasks", func() error { _, err := e.svc.DeleteMilestone(ctx, ms, false); return err }, "4 tasks"},
		{"in-progress task", func() error { _, err := e.svc.DeleteTask(ctx, running, false); return err }, "IN_PROGRESS"},
		{"done task", func() error { _, err := e.svc.DeleteTask(ctx, done, false); return err }, "DONE"},
	}
	for _, c := range cases {
		err := c.del()
		if !errors.Is(err, ErrNeedsForce) || !strings.Contains(err.Error(), c.want) || !strings.Contains(err.Error(), "pass force") {
			t.Errorf("%s: err = %v", c.name, err)
		}
	}
	if _, err := e.svc.Store().GetInitiative(ctx, ini); err != nil {
		t.Error(err)
	}
	if ts, _ := e.svc.Store().ListTasks(ctx, store.TaskFilter{MilestoneID: ms}); len(ts) != 4 {
		t.Errorf("%d tasks remain, want 4", len(ts))
	}
	if _, err := e.svc.DeleteTask(ctx, running, true); err != nil {
		t.Errorf("forced task delete: %v", err)
	}
}

func TestDeleteInitiativeCascade(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	ini, ms := e.scaffold(t)
	m2, err := e.svc.CreateMilestone(ctx, MilestoneInput{InitiativeID: ini, Title: "Second"})
	if err != nil {
		t.Fatal(err)
	}
	t1 := e.task(t, ms, domain.StatusDone)
	t2 := e.task(t, mustID(t, domain.KindMilestone, m2.ID), domain.StatusProposed)
	d := e.doc(t, ini, "okapiword body")
	keep := e.doc(t, 0, "project wide")

	v, err := e.svc.DeleteInitiative(ctx, ini, true)
	if err != nil {
		t.Fatal(err)
	}
	wantDeleted(t, v, domain.FormatID(domain.KindInitiative, ini), "initiative",
		map[string]int{"initiatives": 1, "milestones": 2, "tasks": 2, "notes": 2, "documents": 1})
	st := e.svc.Store()
	for _, err := range []error{
		get(st.GetMilestone(ctx, ms)), get(st.GetTask(ctx, t1)), get(st.GetTask(ctx, t2)), get(st.GetDocument(ctx, d)),
	} {
		if !errors.Is(err, store.ErrNotFound) {
			t.Errorf("not deleted: %v", err)
		}
	}
	if _, err := st.GetDocument(ctx, keep); err != nil {
		t.Errorf("project document lost: %v", err)
	}
	if n := e.chunkHits(t, "okapiword"); n != 0 {
		t.Errorf("%d chunks remain", n)
	}
	if n := e.chunkHits(t, "wide"); n == 0 {
		t.Error("project document chunks were removed")
	}
}

func get[T any](_ T, err error) error { return err }

func TestDeleteUnknownIDs(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	for name, err := range map[string]error{
		"initiative": get(e.svc.DeleteInitiative(ctx, 99, true)),
		"milestone":  get(e.svc.DeleteMilestone(ctx, 99, true)),
		"task":       get(e.svc.DeleteTask(ctx, 99, true)),
		"document":   get(e.svc.DeleteDocument(ctx, 99)),
	} {
		if !errors.Is(err, store.ErrNotFound) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}

func TestDeleteNeverReusesIDs(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	ini, ms := e.scaffold(t)
	tk := e.task(t, ms, domain.StatusProposed)
	d := e.doc(t, ini, "x")
	if _, err := e.svc.DeleteTask(ctx, tk, false); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.DeleteDocument(ctx, d); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.DeleteMilestone(ctx, ms, false); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.DeleteInitiative(ctx, ini, false); err != nil {
		t.Fatal(err)
	}
	i2, err := e.svc.CreateInitiative(ctx, InitiativeInput{Title: "Again"})
	if err != nil {
		t.Fatal(err)
	}
	i2id := mustID(t, domain.KindInitiative, i2.ID)
	m2, err := e.svc.CreateMilestone(ctx, MilestoneInput{InitiativeID: i2id, Title: "Again"})
	if err != nil {
		t.Fatal(err)
	}
	m2id := mustID(t, domain.KindMilestone, m2.ID)
	if i2id <= ini || m2id <= ms {
		t.Errorf("reused IDs: initiative %d (old %d), milestone %d (old %d)", i2id, ini, m2id, ms)
	}
	if id := e.task(t, m2id, domain.StatusProposed); id <= tk {
		t.Errorf("task ID %d reused (old %d)", id, tk)
	}
	if id := e.doc(t, i2id, "y"); id <= d {
		t.Errorf("document ID %d reused (old %d)", id, d)
	}
}

func TestDeleteLoopLockGuard(t *testing.T) {
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
	other, err := e.svc.CreateMilestone(ctx, MilestoneInput{InitiativeID: ini, Title: "Other"})
	if err != nil {
		t.Fatal(err)
	}
	inMs := e.task(t, ms, domain.StatusProposed)
	inOther := e.task(t, mustID(t, domain.KindMilestone, other.ID), domain.StatusProposed)

	lock, err := looplock.Acquire(lockPath, "milestone "+domain.FormatID(domain.KindMilestone, ms))
	if err != nil {
		t.Fatal(err)
	}
	if _, held, _ := looplock.Probe(lockPath); !held {
		lock.Release()
		t.Skip("locking is a no-op on this platform")
	}
	defer lock.Release()

	for name, err := range map[string]error{
		"milestone":  get(e.svc.DeleteMilestone(ctx, ms, true)),
		"task":       get(e.svc.DeleteTask(ctx, inMs, true)),
		"initiative": get(e.svc.DeleteInitiative(ctx, ini, true)),
	} {
		if !errors.Is(err, looplock.ErrLoopRunning) || !strings.Contains(err.Error(), "M-1") {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	if _, err := e.svc.DeleteTask(ctx, inOther, false); err != nil {
		t.Errorf("task in another milestone: %v", err)
	}
	if _, err := e.svc.DeleteMilestone(ctx, mustID(t, domain.KindMilestone, other.ID), false); err != nil {
		t.Errorf("other milestone: %v", err)
	}
	if _, err := e.svc.Store().GetTask(ctx, inMs); err != nil {
		t.Errorf("guarded task was deleted: %v", err)
	}
}
