package service

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bengearig/ralph/internal/domain"
	"github.com/bengearig/ralph/internal/embed/fake"
	"github.com/bengearig/ralph/internal/search"
	"github.com/bengearig/ralph/internal/store"
	"github.com/bengearig/ralph/internal/store/sqlite"
	tmfake "github.com/bengearig/ralph/internal/taskmanager/fake"
)

type countingStore struct {
	store.Store
	listCards int
}

func (c *countingStore) ListCards(ctx context.Context, provider string) ([]domain.Card, error) {
	c.listCards++
	return c.Store.ListCards(ctx, provider)
}

func newCountingEnv(t *testing.T) (env, *countingStore) {
	t.Helper()
	st, err := sqlite.Open(context.Background(), filepath.Join(t.TempDir(), "r.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	cs := &countingStore{Store: st}
	ix := &search.Index{Store: st, Embedder: fake.New(), Chunker: search.Chunker{MaxChars: 1000, OverlapChars: 100}}
	return env{svc: New(cs, ix, Options{StallThreshold: 2})}, cs
}

func TestViewsShowCardAndDerivedPending(t *testing.T) {
	ctx := context.Background()
	e, p := withProvider(newEnv(t))
	i, _ := e.svc.CreateInitiative(ctx, InitiativeInput{Title: "I", Brief: "b"})
	m, _ := e.svc.CreateMilestone(ctx, MilestoneInput{InitiativeID: iid(t, i.ID), Title: "M"})
	tk, err := e.svc.CreateTask(ctx, TaskInput{MilestoneID: mid(t, m.ID), Title: "T", Description: "d"})
	if err != nil {
		t.Fatal(err)
	}

	got, err := e.svc.GetTask(ctx, tid(t, tk.ID))
	if err != nil {
		t.Fatal(err)
	}
	if got.Card == nil || got.Card.Key != "FAKE-3" || got.Card.Provider != "fake" || got.Card.Pending != "" {
		t.Fatalf("card = %+v", got.Card)
	}
	gi, _ := e.svc.GetInitiative(ctx, iid(t, i.ID))
	gm, _ := e.svc.GetMilestone(ctx, mid(t, m.ID))
	if gi.Card == nil || gi.Card.Key != "FAKE-1" || gm.Card == nil || gm.Card.Key != "FAKE-2" || gi.Card.Pending != "" || gm.Card.Pending != "update" {
		t.Fatalf("initiative card %+v, milestone card %+v", gi.Card, gm.Card)
	}

	// The milestone card was created while the milestone was empty; adding a
	// task changed its computed status.
	title := "Renamed"
	e.svc.taskManager = nil
	if _, err := e.svc.UpdateTask(ctx, tid(t, tk.ID), TaskPatch{Title: &title}); err != nil {
		t.Fatal(err)
	}
	e.svc.taskManager = p
	got, _ = e.svc.GetTask(ctx, tid(t, tk.ID))
	if got.Card == nil || got.Card.Pending != "update" {
		t.Errorf("after edit card = %+v", got.Card)
	}
	list, _ := e.svc.ListTasks(ctx, TaskFilter{})
	if len(list) != 1 || list[0].Card == nil || list[0].Card.Pending != "update" {
		t.Errorf("list = %+v", list)
	}
	ms, _ := e.svc.ListMilestones(ctx, 0, ListOptions{})
	if len(ms) != 1 || ms[0].Card == nil || ms[0].Card.Key != "FAKE-2" {
		t.Errorf("milestones = %+v", ms)
	}
	is, _ := e.svc.ListInitiatives(ctx, ListOptions{})
	if len(is) != 1 || is[0].Card == nil || is[0].Card.Key != "FAKE-1" {
		t.Errorf("initiatives = %+v", is)
	}
	st, err := e.svc.Status(ctx, 0, ListOptions{})
	if err != nil || len(st.Initiatives) != 1 || st.Initiatives[0].Card == nil || st.Initiatives[0].Milestones[0].Card == nil {
		t.Errorf("status = %+v, %v", st, err)
	}
}

func TestViewCardPendingCreateAndMissing(t *testing.T) {
	ctx := context.Background()
	e, p := withProvider(newEnv(t))
	p.SetError(errors.New("jira is down"))
	i, _ := e.svc.CreateInitiative(ctx, InitiativeInput{Title: "I"})
	m, _ := e.svc.CreateMilestone(ctx, MilestoneInput{InitiativeID: iid(t, i.ID), Title: "M"})
	tk, err := e.svc.CreateTask(ctx, TaskInput{MilestoneID: mid(t, m.ID), Title: "T"})
	if err != nil {
		t.Fatal(err)
	}
	got, err := e.svc.GetTask(ctx, tid(t, tk.ID))
	if err != nil {
		t.Fatal(err)
	}
	if got.Card == nil || got.Card.Pending != "create" || got.Card.Key != "" || !strings.Contains(got.Card.LastError, "parent") {
		t.Errorf("task card = %+v", got.Card)
	}
	gi, _ := e.svc.GetInitiative(ctx, iid(t, i.ID))
	if gi.Card == nil || gi.Card.Pending != "create" || !strings.Contains(gi.Card.LastError, "jira is down") {
		t.Errorf("initiative card = %+v", gi.Card)
	}

	// A resource with no row has no card, and its keyed child never errors.
	if err := e.svc.RecordCard(ctx, domain.KindTask, tid(t, tk.ID), "fake", "FAKE-9", "u", "stale"); err != nil {
		t.Fatal(err)
	}
	got, err = e.svc.GetTask(ctx, tid(t, tk.ID))
	if err != nil || got.Card == nil || got.Card.Key != "FAKE-9" || got.Card.Pending != "" {
		t.Errorf("unknown parent: card = %+v, err = %v", got.Card, err)
	}
	if err := e.svc.Store().DeleteCards(ctx, domain.KindInitiative, iid(t, i.ID)); err != nil {
		t.Fatal(err)
	}
	gi, _ = e.svc.GetInitiative(ctx, iid(t, i.ID))
	if gi.Card != nil {
		t.Errorf("initiative without row has card %+v", gi.Card)
	}
}

func TestViewsWithoutProviderHaveNoCard(t *testing.T) {
	ctx := context.Background()
	e, cs := newCountingEnv(t)
	i, _ := e.svc.CreateInitiative(ctx, InitiativeInput{Title: "I"})
	m, _ := e.svc.CreateMilestone(ctx, MilestoneInput{InitiativeID: iid(t, i.ID), Title: "M"})
	tk, _ := e.svc.CreateTask(ctx, TaskInput{MilestoneID: mid(t, m.ID), Title: "T"})

	var views []any
	gi, _ := e.svc.GetInitiative(ctx, iid(t, i.ID))
	li, _ := e.svc.ListInitiatives(ctx, ListOptions{})
	gm, _ := e.svc.GetMilestone(ctx, mid(t, m.ID))
	lm, _ := e.svc.ListMilestones(ctx, 0, ListOptions{})
	gt, _ := e.svc.GetTask(ctx, tid(t, tk.ID))
	lt, _ := e.svc.ListTasks(ctx, TaskFilter{})
	st, err := e.svc.Status(ctx, 0, ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	views = append(views, gi, li, gm, lm, gt, lt, st)
	for _, v := range views {
		b, _ := json.Marshal(v)
		if strings.Contains(string(b), `"card"`) {
			t.Errorf("view has card: %s", b)
		}
	}
	if cs.listCards != 0 {
		t.Errorf("ListCards called %d times", cs.listCards)
	}
}

func TestListTasksLoadsCardsOnce(t *testing.T) {
	ctx := context.Background()
	e, cs := newCountingEnv(t)
	e.svc.taskManager = tmfake.New()
	i, _ := e.svc.CreateInitiative(ctx, InitiativeInput{Title: "I"})
	m, _ := e.svc.CreateMilestone(ctx, MilestoneInput{InitiativeID: iid(t, i.ID), Title: "M"})
	for n := 0; n < 50; n++ {
		if _, err := e.svc.CreateTask(ctx, TaskInput{MilestoneID: mid(t, m.ID), Title: "T"}); err != nil {
			t.Fatal(err)
		}
	}
	cs.listCards = 0
	list, err := e.svc.ListTasks(ctx, TaskFilter{})
	if err != nil || len(list) != 50 {
		t.Fatalf("%d tasks, %v", len(list), err)
	}
	for _, v := range list {
		if v.Card == nil || v.Card.Key == "" {
			t.Fatalf("task %s has no card", v.ID)
		}
	}
	if cs.listCards != 1 {
		t.Errorf("ListCards called %d times", cs.listCards)
	}
	cs.listCards = 0
	if _, err := e.svc.Status(ctx, 0, ListOptions{}); err != nil || cs.listCards != 1 {
		t.Errorf("Status: %d ListCards calls, %v", cs.listCards, err)
	}
}
