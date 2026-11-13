package service

import (
	"context"
	"errors"
	"testing"

	"github.com/bengearig/ralph/internal/domain"
	"github.com/bengearig/ralph/internal/store"
)

type failingParentStore struct{ store.Store }

func (failingParentStore) SetCardParentKey(context.Context, domain.Kind, int64, string, string) error {
	return errors.New("disk full")
}

func recordedParent(t *testing.T, e env, kind domain.Kind, id int64) string {
	t.Helper()
	c, err := e.svc.store.GetCard(context.Background(), kind, id, "fake")
	if err != nil {
		t.Fatal(err)
	}
	return c.ParentKey
}

func TestCardRecordsParentKey(t *testing.T) {
	ctx := context.Background()
	e, _ := withProvider(newEnv(t))
	with, err := e.svc.CreateInitiative(ctx, InitiativeInput{Title: "With", ParentCard: "PROJ-12"})
	if err != nil || with.CardWarning != "" {
		t.Fatalf("%v %q", err, with.CardWarning)
	}
	without, err := e.svc.CreateInitiative(ctx, InitiativeInput{Title: "Without"})
	if err != nil || without.CardWarning != "" {
		t.Fatalf("%v %q", err, without.CardWarning)
	}
	wid, oid := iid(t, with.ID), iid(t, without.ID)
	if got := recordedParent(t, e, domain.KindInitiative, wid); got != "PROJ-12" {
		t.Errorf("parent key = %q", got)
	}
	if got := recordedParent(t, e, domain.KindInitiative, oid); got != "" {
		t.Errorf("parent key = %q", got)
	}

	title := "Renamed"
	if v, err := e.svc.UpdateInitiative(ctx, wid, InitiativePatch{Title: &title}); err != nil || v.CardWarning != "" {
		t.Fatalf("%v %q", err, v.CardWarning)
	}
	if got := recordedParent(t, e, domain.KindInitiative, wid); got != "PROJ-12" {
		t.Errorf("after update parent key = %q", got)
	}
	next := "PROJ-99"
	if v, err := e.svc.UpdateInitiative(ctx, wid, InitiativePatch{ParentCard: &next}); err != nil || v.CardWarning != "" {
		t.Fatalf("%v %q", err, v.CardWarning)
	}
	if got := recordedParent(t, e, domain.KindInitiative, wid); got != "PROJ-99" {
		t.Errorf("after parent change parent key = %q", got)
	}

	m, err := e.svc.CreateMilestone(ctx, MilestoneInput{InitiativeID: wid, Title: "M"})
	if err != nil {
		t.Fatal(err)
	}
	tk, err := e.svc.CreateTask(ctx, TaskInput{MilestoneID: mid(t, m.ID), Title: "T"})
	if err != nil {
		t.Fatal(err)
	}
	if got := recordedParent(t, e, domain.KindMilestone, mid(t, m.ID)); got != "" {
		t.Errorf("milestone parent key = %q", got)
	}
	if got := recordedParent(t, e, domain.KindTask, tid(t, tk.ID)); got != "" {
		t.Errorf("task parent key = %q", got)
	}
}

func TestCardParentKeyFailureWarns(t *testing.T) {
	ctx := context.Background()
	e, _ := withProvider(newEnv(t))
	e.svc.store = failingParentStore{e.svc.store}
	v, err := e.svc.CreateInitiative(ctx, InitiativeInput{Title: "I", ParentCard: "PROJ-12"})
	if err != nil {
		t.Fatalf("create failed: %v", err)
	}
	if v.CardWarning == "" {
		t.Error("no warning on create")
	}
	c, err := e.svc.store.GetCard(ctx, domain.KindInitiative, iid(t, v.ID), "fake")
	if err != nil || c.Key == "" {
		t.Fatalf("card = %+v, %v", c, err)
	}
	title := "Renamed"
	u, err := e.svc.UpdateInitiative(ctx, iid(t, v.ID), InitiativePatch{Title: &title})
	if err != nil || u.Title != "Renamed" {
		t.Fatalf("update = %+v, %v", u, err)
	}
	if u.CardWarning == "" {
		t.Error("no warning on update")
	}
}
