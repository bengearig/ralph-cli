package service

import (
	"context"
	"strings"
	"testing"

	"github.com/bengearig/ralph/internal/domain"
	tmfake "github.com/bengearig/ralph/internal/taskmanager/fake"
)

var kindsWithoutTask = []domain.Kind{domain.KindInitiative, domain.KindMilestone}

// keptFixture creates cards for every kind, then switches the task kind off.
func keptFixture(t *testing.T) (env, *tmfake.Provider, int64, int64, int64) {
	t.Helper()
	e, p, mID, tID := statusFixture(t)
	m, err := e.svc.store.GetMilestone(context.Background(), mID)
	if err != nil {
		t.Fatal(err)
	}
	e.svc.cardKinds = kindsWithoutTask
	return e, p, m.InitiativeID, mID, tID
}

func TestKeptTaskCardFollowsEdits(t *testing.T) {
	ctx := context.Background()
	e, p, _, _, tID := keptFixture(t)
	n := len(p.Calls())
	title, desc, ac := "New title", "new desc", "new criteria"
	v, err := e.svc.UpdateTask(ctx, tID, TaskPatch{Title: &title, Description: &desc, AcceptanceCriteria: &ac})
	if err != nil || v.CardWarning != "" {
		t.Fatalf("%v %q", err, v.CardWarning)
	}
	calls := p.Calls()[n:]
	if got := methods(calls); !eq(got, []string{"Update:FAKE-3", "SetStatus:FAKE-3:PROPOSED"}) {
		t.Fatalf("calls = %v", got)
	}
	if r := calls[0].Request; r.Title != title || !strings.Contains(r.Description, desc) || !strings.Contains(r.Description, ac) {
		t.Errorf("request = %+v", r)
	}
	row, _ := e.svc.store.GetCard(ctx, domain.KindTask, tID, "fake")
	req, _ := e.svc.cardRequest(ctx, domain.KindTask, tID, "FAKE-2", true)
	if row.Key != "FAKE-3" || row.ContentHash != req.Hash() {
		t.Errorf("row = %+v", row)
	}
}

func TestKeptTaskCardFollowsStatusAndMilestoneFollowsComputed(t *testing.T) {
	ctx := context.Background()
	e, p, _, _, tID := keptFixture(t)
	if _, err := e.svc.SetTaskStatus(ctx, tID, domain.StatusTodo, "ready"); err != nil {
		t.Fatal(err)
	}
	steps := []struct {
		to   domain.Status
		want []string
	}{
		{domain.StatusInProgress, []string{"Update:FAKE-3", "SetStatus:FAKE-3:IN_PROGRESS", "Update:FAKE-2", "SetStatus:FAKE-2:IN_PROGRESS"}},
		{domain.StatusDone, []string{"Update:FAKE-3", "SetStatus:FAKE-3:DONE", "Update:FAKE-2", "SetStatus:FAKE-2:DONE"}},
	}
	for _, s := range steps {
		n := len(p.Calls())
		if _, err := e.svc.SetTaskStatus(ctx, tID, s.to, ""); err != nil {
			t.Fatal(err)
		}
		if got := methods(p.Calls()[n:]); !eq(got, s.want) {
			t.Errorf("%s: calls = %v, want %v", s.to, got, s.want)
		}
	}
	n := len(p.Calls())
	if _, err := e.svc.ReopenTask(ctx, tID, "lost"); err != nil {
		t.Fatal(err)
	}
	if got := methods(p.Calls()[n:]); len(got) < 2 || got[1] != "SetStatus:FAKE-3:TODO" {
		t.Errorf("reopen calls = %v", got)
	}
}

func TestKeptTaskCardCancelStatus(t *testing.T) {
	ctx := context.Background()
	e, p, _, _, tID := keptFixture(t)
	n := len(p.Calls())
	if _, err := e.svc.SetTaskStatus(ctx, tID, domain.StatusCancelled, "dropped"); err != nil {
		t.Fatal(err)
	}
	if got := methods(p.Calls()[n:]); len(got) < 2 || got[1] != "SetStatus:FAKE-3:CANCELLED" {
		t.Errorf("calls = %v", got)
	}
}

func TestKeptTaskCardMovedToCanceledOnDelete(t *testing.T) {
	ctx := context.Background()
	for _, target := range []domain.Kind{domain.KindTask, domain.KindMilestone, domain.KindInitiative} {
		t.Run(string(target), func(t *testing.T) {
			e, p, iID, mID, tID := keptFixture(t)
			e.svc.cardCanceledStatus = "Canceled"
			ids := map[domain.Kind]int64{domain.KindTask: tID, domain.KindMilestone: mID, domain.KindInitiative: iID}

			n := len(p.Calls())
			pv, err := e.svc.PreviewDelete(ctx, target, ids[target], true)
			if err != nil || !hasCard(pv.Cards, "FAKE-3", false) {
				t.Fatalf("preview %v %+v", err, pv.Cards)
			}
			if len(p.Calls()) != n {
				t.Fatalf("preview made calls: %v", methods(p.Calls()[n:]))
			}

			var cards []DeletedCard
			switch target {
			case domain.KindTask:
				v, err := e.svc.DeleteTask(ctx, tID, true)
				if err != nil {
					t.Fatal(err)
				}
				cards = v.Cards
			case domain.KindMilestone:
				v, err := e.svc.DeleteMilestone(ctx, mID, true)
				if err != nil {
					t.Fatal(err)
				}
				cards = v.Cards
			default:
				v, err := e.svc.DeleteInitiative(ctx, iID, true)
				if err != nil {
					t.Fatal(err)
				}
				cards = v.Cards
			}
			if !hasCard(cards, "FAKE-3", true) {
				t.Errorf("cards = %+v", cards)
			}
			found := false
			for _, m := range methods(p.Calls()[n:]) {
				if m == "SetStatus:FAKE-3:CANCELLED" {
					found = true
				}
			}
			if !found {
				t.Errorf("calls = %v", methods(p.Calls()[n:]))
			}
		})
	}
}

func hasCard(cards []DeletedCard, key string, moved bool) bool {
	for _, c := range cards {
		if c.Key == key {
			return c.Moved == moved
		}
	}
	return false
}

func TestKeptTaskCardViewShowsKeyURLAndPending(t *testing.T) {
	ctx := context.Background()
	e, p, _, _, tID := keptFixture(t)
	v, err := e.svc.GetTask(ctx, tID)
	if err != nil || v.Card == nil || v.Card.Key != "FAKE-3" || v.Card.URL == "" || v.Card.Pending != "" {
		t.Fatalf("%v %+v", err, v.Card)
	}
	title := "Renamed"
	e.svc.taskManager = nil
	if _, err := e.svc.UpdateTask(ctx, tID, TaskPatch{Title: &title}); err != nil {
		t.Fatal(err)
	}
	e.svc.taskManager = p
	v, _ = e.svc.GetTask(ctx, tID)
	if v.Card == nil || v.Card.Key != "FAKE-3" || v.Card.URL == "" || v.Card.Pending != "update" {
		t.Errorf("get card = %+v", v.Card)
	}
	list, err := e.svc.ListTasks(ctx, TaskFilter{})
	if err != nil || len(list) != 1 || list[0].Card == nil || list[0].Card.Key != "FAKE-3" || list[0].Card.URL == "" || list[0].Card.Pending != "update" {
		t.Errorf("list = %+v, %v", list, err)
	}
}

func TestSwitchingKindOffMakesNoProviderCalls(t *testing.T) {
	ctx := context.Background()
	e, p, _, tID := statusFixture(t)
	n := len(p.Calls())
	svc := New(e.svc.store, e.svc.index, Options{TaskManager: p, CardKinds: kindsWithoutTask, CardCanceledStatus: "Canceled"})
	if _, err := svc.GetTask(ctx, tID); err != nil {
		t.Fatal(err)
	}
	if got := len(p.Calls()) - n; got != 0 {
		t.Errorf("provider calls = %v", methods(p.Calls()[n:]))
	}
}
