package service

import (
	"context"
	"testing"

	"github.com/bengearig/ralph/internal/domain"
)

func TestSprintCarryRequests(t *testing.T) {
	ctx := context.Background()
	e, p := withProvider(newEnv(t))
	with, err := e.svc.CreateInitiative(ctx, InitiativeInput{Title: "With", Sprint: "Sprint 42"})
	if err != nil {
		t.Fatal(err)
	}
	without, err := e.svc.CreateInitiative(ctx, InitiativeInput{Title: "Without"})
	if err != nil {
		t.Fatal(err)
	}
	m1, err := e.svc.CreateMilestone(ctx, MilestoneInput{InitiativeID: iid(t, with.ID), Title: "M1"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.CreateMilestone(ctx, MilestoneInput{InitiativeID: iid(t, without.ID), Title: "M2"}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.CreateTask(ctx, TaskInput{MilestoneID: mid(t, m1.ID), Title: "T"}); err != nil {
		t.Fatal(err)
	}

	calls := p.SprintCalls()
	if len(calls) != 2 || calls[0].Request.Sprint != "Sprint 42" || calls[1].Request.Sprint != "" {
		t.Fatalf("sprint calls = %+v", calls)
	}
	for _, c := range creates(p.Calls()) {
		if c.Request.Kind != domain.KindMilestone && c.Request.Sprint != "" {
			t.Errorf("%s request carries sprint %q", c.Request.ID, c.Request.Sprint)
		}
	}
}

func TestSprintCarryChangeMarksNothingPending(t *testing.T) {
	ctx := context.Background()
	e, p := withProvider(newEnv(t))
	i, err := e.svc.CreateInitiative(ctx, InitiativeInput{Title: "I", Sprint: "Sprint 42"})
	if err != nil {
		t.Fatal(err)
	}
	m, err := e.svc.CreateMilestone(ctx, MilestoneInput{InitiativeID: iid(t, i.ID), Title: "M"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.CreateTask(ctx, TaskInput{MilestoneID: mid(t, m.ID), Title: "T"}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.SyncCards(ctx, SyncOptions{}); err != nil {
		t.Fatal(err)
	}
	before := len(updates(p.Calls()))
	next := "Sprint 43"
	if _, err := e.svc.UpdateInitiative(ctx, iid(t, i.ID), InitiativePatch{Sprint: &next}); err != nil {
		t.Fatal(err)
	}
	ix, err := e.svc.loadCards(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		kind   domain.Kind
		id     int64
		parent cardID
	}{
		{domain.KindInitiative, iid(t, i.ID), cardID{}},
		{domain.KindMilestone, mid(t, m.ID), cardID{domain.KindInitiative, iid(t, i.ID)}},
	} {
		if v := ix.view(ctx, c.kind, c.id, c.parent); v == nil || v.Pending != "" {
			t.Errorf("%s view = %+v", c.kind, v)
		}
	}
	if _, err := e.svc.SyncCards(ctx, SyncOptions{}); err != nil {
		t.Fatal(err)
	}
	if n := len(updates(p.Calls())) - before; n != 0 {
		t.Fatalf("sync made %d updates", n)
	}
}
