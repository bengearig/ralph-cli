package service

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/bengearig/ralph/internal/domain"
	"github.com/bengearig/ralph/internal/taskmanager"
	tmfake "github.com/bengearig/ralph/internal/taskmanager/fake"
)

// noFinder hides the fake's CardFinder.
type noFinder struct{ taskmanager.Provider }

func cand(key, summary string) taskmanager.CardCandidate {
	return taskmanager.CardCandidate{Key: key, URL: "https://x/" + key, Summary: summary, Status: "To Do"}
}

func reconcileFixture(t *testing.T) (env, *tmfake.Provider, InitiativeView, MilestoneView, TaskView) {
	t.Helper()
	ctx := context.Background()
	e, p := withProvider(newEnv(t))
	i, err := e.svc.CreateInitiative(ctx, InitiativeInput{Title: "Init", Brief: "b"})
	if err != nil {
		t.Fatal(err)
	}
	m, err := e.svc.CreateMilestone(ctx, MilestoneInput{InitiativeID: iid(t, i.ID), Title: "Mile"})
	if err != nil {
		t.Fatal(err)
	}
	tk, err := e.svc.CreateTask(ctx, TaskInput{MilestoneID: mid(t, m.ID), Title: "Task"})
	if err != nil {
		t.Fatal(err)
	}
	return e, p, i, m, tk
}

func TestReconcileOrphan(t *testing.T) {
	e, p, _, _, _ := reconcileFixture(t)
	p.SetCandidates([]taskmanager.CardCandidate{cand("ZZ-1", "T-99: Gone")})
	r, err := e.svc.ReconcileCards(context.Background(), ReconcileOptions{})
	if err != nil {
		t.Fatal(err)
	}
	want := ReconcileItem{Key: "ZZ-1", URL: "https://x/ZZ-1", ID: "T-99", Summary: "T-99: Gone", Status: "To Do", Outcome: "orphan"}
	if r.Orphans != 1 || r.Skipped != 0 || len(r.Items) != 1 || r.Items[0] != want {
		t.Errorf("%+v", r)
	}
}

func TestReconcileRecordedKeyWins(t *testing.T) {
	e, p, _, _, tk := reconcileFixture(t)
	key := "FAKE-3"
	row, err := e.svc.store.GetCard(context.Background(), domain.KindTask, tid(t, tk.ID), "fake")
	if err != nil || row.Key == "" {
		t.Fatalf("%+v %v", row, err)
	}
	key = row.Key
	p.SetCandidates([]taskmanager.CardCandidate{cand(key, "T-99: other id"), cand(key, "no id at all")})
	r, err := e.svc.ReconcileCards(context.Background(), ReconcileOptions{})
	if err != nil {
		t.Fatal(err)
	}
	reason := "key is recorded for " + tk.ID
	if r.Skipped != 2 || r.Items[0].Reason != reason || r.Items[1].Reason != reason || r.Items[0].Outcome != "skipped" {
		t.Errorf("%+v", r)
	}
}

func TestReconcileNoID(t *testing.T) {
	e, p, _, _, _ := reconcileFixture(t)
	p.SetCandidates([]taskmanager.CardCandidate{cand("A-1", "Fix the thing"), cand("A-2", "M-2 missing colon")})
	r, err := e.svc.ReconcileCards(context.Background(), ReconcileOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range r.Items {
		if it.Outcome != "skipped" || it.Reason != "summary has no ralph ID" || it.ID != "" {
			t.Errorf("%+v", it)
		}
	}
	if r.Skipped != 2 || r.Orphans != 0 {
		t.Errorf("%+v", r)
	}
}

func TestReconcileExistingIDs(t *testing.T) {
	ctx := context.Background()
	e, p, i, m, tk := reconcileFixture(t)
	if _, err := e.svc.SetTaskStatus(ctx, tid(t, tk.ID), domain.StatusCancelled, ""); err != nil {
		t.Fatal(err)
	}
	i2, err := e.svc.CreateInitiative(ctx, InitiativeInput{Title: "Other", Brief: "b"})
	if err != nil {
		t.Fatal(err)
	}
	m2, err := e.svc.CreateMilestone(ctx, MilestoneInput{InitiativeID: iid(t, i2.ID), Title: "M2"})
	if err != nil {
		t.Fatal(err)
	}
	t2, err := e.svc.CreateTask(ctx, TaskInput{MilestoneID: mid(t, m2.ID), Title: "T2"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.ArchiveInitiative(ctx, iid(t, i2.ID), DocumentChoice{None: true}); err != nil {
		t.Fatal(err)
	}
	var cs []taskmanager.CardCandidate
	ids := []string{i.ID, m.ID, tk.ID, i2.ID, m2.ID, t2.ID}
	for n, id := range ids {
		cs = append(cs, cand("X-"+string(rune('a'+n)), id+": title"))
	}
	p.SetCandidates(cs)
	r, err := e.svc.ReconcileCards(ctx, ReconcileOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if r.Skipped != len(ids) || r.Orphans != 0 {
		t.Fatalf("%+v", r)
	}
	for n, it := range r.Items {
		if it.Reason != ids[n]+" exists in ralph with a different or no recorded card" {
			t.Errorf("%+v", it)
		}
	}
}

func TestReconcileNormalizesID(t *testing.T) {
	e, p, _, m, _ := reconcileFixture(t)
	p.SetCandidates([]taskmanager.CardCandidate{cand("A-1", "M-0"+m.ID[2:]+": padded"), cand("A-2", "M-098: absent")})
	r, err := e.svc.ReconcileCards(context.Background(), ReconcileOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if r.Items[0].ID != m.ID || r.Items[0].Outcome != "skipped" || r.Items[1].ID != "M-98" || r.Items[1].Outcome != "orphan" {
		t.Errorf("%+v", r.Items)
	}
}

func TestReconcileOrderAndEmpty(t *testing.T) {
	e, p, _, _, _ := reconcileFixture(t)
	r, err := e.svc.ReconcileCards(context.Background(), ReconcileOptions{})
	if err != nil || r.Items == nil || len(r.Items) != 0 {
		t.Fatalf("%+v %v", r, err)
	}
	p.SetCandidates([]taskmanager.CardCandidate{cand("K-3", "T-90: c"), cand("K-1", "plain"), cand("K-2", "T-91: b")})
	r, err = e.svc.ReconcileCards(context.Background(), ReconcileOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var keys []string
	for _, it := range r.Items {
		keys = append(keys, it.Key)
	}
	if !reflect.DeepEqual(keys, []string{"K-3", "K-1", "K-2"}) || r.Orphans != 2 || r.Skipped != 1 {
		t.Errorf("%v %+v", keys, r)
	}
}

func TestReconcileSearchAndErrors(t *testing.T) {
	ctx := context.Background()
	e, p, _, _, _ := reconcileFixture(t)
	after := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
	if _, err := e.svc.ReconcileCards(ctx, ReconcileOptions{CreatedAfter: after}); err != nil {
		t.Fatal(err)
	}
	if s := p.Searches(); len(s) != 1 || !s[0].CreatedAfter.Equal(after) {
		t.Errorf("searches %+v", s)
	}
	boom := errors.New("boom")
	p.SetError(boom)
	if _, err := e.svc.ReconcileCards(ctx, ReconcileOptions{}); !errors.Is(err, boom) {
		t.Errorf("err = %v", err)
	}

	bare := newEnv(t)
	if _, err := bare.svc.ReconcileCards(ctx, ReconcileOptions{}); !errors.Is(err, ErrNoTaskManager) {
		t.Errorf("err = %v", err)
	}
	e.svc.taskManager = noFinder{tmfake.New()}
	if _, err := e.svc.ReconcileCards(ctx, ReconcileOptions{}); !errors.Is(err, domain.ErrInvalid) {
		t.Errorf("err = %v", err)
	}
}

func TestReconcileInTransaction(t *testing.T) {
	e, _, _, _, _ := reconcileFixture(t)
	inner := *e.svc
	inner.inTx = true
	if _, err := inner.ReconcileCards(context.Background(), ReconcileOptions{}); err == nil {
		t.Error("expected an error inside a transaction")
	}
}

func TestReconcileIsReadOnly(t *testing.T) {
	ctx := context.Background()
	e, p, _, _, _ := reconcileFixture(t)
	p.SetCandidates([]taskmanager.CardCandidate{cand("O-1", "T-90: orphan"), cand("O-2", "none")})
	before, err := e.svc.ListCards(ctx, "fake")
	if err != nil {
		t.Fatal(err)
	}
	calls := len(p.Calls())
	if _, err := e.svc.ReconcileCards(ctx, ReconcileOptions{}); err != nil {
		t.Fatal(err)
	}
	if got := p.Calls(); len(got) != calls {
		t.Errorf("provider calls %+v", got[calls:])
	}
	after, _ := e.svc.ListCards(ctx, "fake")
	if !reflect.DeepEqual(before, after) {
		t.Errorf("cards changed: %+v -> %+v", before, after)
	}
}
