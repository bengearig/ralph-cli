package service

import (
	"context"
	"testing"

	"github.com/bengearig/ralph/internal/domain"
	"github.com/bengearig/ralph/internal/taskmanager"
	tmfake "github.com/bengearig/ralph/internal/taskmanager/fake"
)

const fallbackMilestoneKey = "FAKE-2"

func fallbackFixture(t *testing.T, kinds []domain.Kind, titles ...string) (env, *tmfake.Provider, []int64) {
	t.Helper()
	ctx := context.Background()
	e, p := kindsEnv(t, kinds)
	i, _ := e.svc.CreateInitiative(ctx, InitiativeInput{Title: "I"})
	m, _ := e.svc.CreateMilestone(ctx, MilestoneInput{InitiativeID: iid(t, i.ID), Title: "M"})
	var ids []int64
	for _, title := range titles {
		tk, err := e.svc.CreateTask(ctx, TaskInput{MilestoneID: mid(t, m.ID), Title: title, Description: "d", AcceptanceCriteria: "c"})
		if err != nil {
			t.Fatal(err)
		}
		id := tid(t, tk.ID)
		if _, err := e.svc.SetTaskStatus(ctx, id, domain.StatusTodo, "ready"); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	return e, p, ids
}

func TestMilestoneCardMovedOnceWhenTasksHaveNoCard(t *testing.T) {
	ctx := context.Background()
	e, p, ids := fallbackFixture(t, kindsWithoutTask, "T1", "T2")

	n := len(p.Calls())
	v, err := e.svc.SetTaskStatus(ctx, ids[0], domain.StatusInProgress, "")
	if err != nil || v.CardWarning != "" || v.CardConflict != nil {
		t.Fatalf("%v %+v", err, v)
	}
	want := []string{"Update:" + fallbackMilestoneKey, "SetStatus:" + fallbackMilestoneKey + ":IN_PROGRESS"}
	if got := methods(p.Calls()[n:]); !eq(got, want) {
		t.Fatalf("calls = %v, want %v", got, want)
	}

	n = len(p.Calls())
	if _, err := e.svc.SetTaskStatus(ctx, ids[1], domain.StatusInProgress, ""); err != nil {
		t.Fatal(err)
	}
	if got := methods(p.Calls()[n:]); len(got) != 0 {
		t.Errorf("second task made calls %v", got)
	}
}

func TestMilestoneCardHeldBySomeoneElseYieldsConflict(t *testing.T) {
	ctx := context.Background()
	e, p, ids := fallbackFixture(t, kindsWithoutTask, "T1")
	conflict := &taskmanager.AssigneeConflictError{Key: fallbackMilestoneKey, Assignee: "Pat"}
	p.SetHook(func(_ int, method string, req taskmanager.CardRequest) error {
		if method == "SetStatus" && req.ID == fallbackMilestoneKey {
			return conflict
		}
		return nil
	})
	n := len(p.Calls())
	v, err := e.svc.SetTaskStatus(ctx, ids[0], domain.StatusInProgress, "")
	if err != nil {
		t.Fatal(err)
	}
	want := &CardConflictView{Card: fallbackMilestoneKey, Resource: domain.FormatID(domain.KindMilestone, 1), Assignee: "Pat"}
	if v.CardConflict == nil || *v.CardConflict != *want {
		t.Fatalf("conflict = %+v, want %+v", v.CardConflict, want)
	}
	for _, c := range p.Calls()[n:] {
		if c.Method == "SetStatus" {
			t.Errorf("card moved: %v", methods(p.Calls()[n:]))
		}
	}
	row, _ := e.svc.store.GetCard(ctx, domain.KindMilestone, 1, "fake")
	req, _ := buildCardRequest(ctx, e.svc.store, domain.KindMilestone, 1, "FAKE-1")
	if row.ContentHash == req.Hash() || row.LastError != conflict.Error() {
		t.Errorf("row = %+v", row)
	}
}

func TestTaskWithOwnCardStillMovedWithMilestoneCard(t *testing.T) {
	ctx := context.Background()
	e, p, ids := fallbackFixture(t, nil, "T1")
	n := len(p.Calls())
	v, err := e.svc.SetTaskStatus(ctx, ids[0], domain.StatusInProgress, "")
	if err != nil || v.CardWarning != "" || v.CardConflict != nil {
		t.Fatalf("%v %+v", err, v)
	}
	want := []string{"Update:FAKE-3", "SetStatus:FAKE-3:IN_PROGRESS", "Update:" + fallbackMilestoneKey, "SetStatus:" + fallbackMilestoneKey + ":IN_PROGRESS"}
	if got := methods(p.Calls()[n:]); !eq(got, want) {
		t.Fatalf("calls = %v, want %v", got, want)
	}
}
