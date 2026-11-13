package service

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/bengearig/ralph/internal/domain"
	"github.com/bengearig/ralph/internal/taskmanager"
	"github.com/bengearig/ralph/internal/taskmanager/fake"
)

func updates(calls []fake.Call) []fake.Call {
	var out []fake.Call
	for _, c := range calls {
		if c.Method == "Update" {
			out = append(out, c)
		}
	}
	return out
}

func TestInitiativeParentMoveSequence(t *testing.T) {
	ctx := context.Background()
	e, p := withProvider(newEnv(t))
	i, err := e.svc.CreateInitiative(ctx, InitiativeInput{Title: "Init"})
	if err != nil {
		t.Fatal(err)
	}
	id := iid(t, i.ID)
	str := func(s string) *string { return &s }
	steps := []struct {
		name        string
		patch       InitiativePatch
		parent, old string
	}{
		{"set", InitiativePatch{ParentCard: str("ABC-1")}, "ABC-1", ""},
		{"title only", InitiativePatch{Title: str("Renamed")}, "ABC-1", "ABC-1"},
		{"change", InitiativePatch{ParentCard: str("ABC-2")}, "ABC-2", "ABC-1"},
		{"clear", InitiativePatch{ParentCard: str("")}, "", "ABC-2"},
		{"edit again", InitiativePatch{Title: str("Again")}, "", ""},
	}
	for n, st := range steps {
		v, err := e.svc.UpdateInitiative(ctx, id, st.patch)
		if err != nil || v.CardWarning != "" {
			t.Fatalf("%s: %v %q", st.name, err, v.CardWarning)
		}
		us := updates(p.Calls())
		if len(us) != n+1 {
			t.Fatalf("%s: %d updates", st.name, len(us))
		}
		r := us[n].Request
		if r.ParentKey != st.parent || r.PreviousParentKey != st.old {
			t.Errorf("%s: request parent %q previous %q", st.name, r.ParentKey, r.PreviousParentKey)
		}
		if got := recordedParent(t, e, domain.KindInitiative, id); got != r.ParentKey {
			t.Errorf("%s: recorded parent %q", st.name, got)
		}
	}
}

func TestInitiativeParentMoveFailureRetries(t *testing.T) {
	ctx := context.Background()
	e, p := withProvider(newEnv(t))
	i, err := e.svc.CreateInitiative(ctx, InitiativeInput{Title: "Init", ParentCard: "ABC-1"})
	if err != nil {
		t.Fatal(err)
	}
	id := iid(t, i.ID)
	p.SetError(errors.New("parent type rejected"))
	next := "ABC-2"
	v, err := e.svc.UpdateInitiative(ctx, id, InitiativePatch{ParentCard: &next})
	if err != nil || !strings.Contains(v.CardWarning, "parent type rejected") {
		t.Fatalf("%v %q", err, v.CardWarning)
	}
	if got := recordedParent(t, e, domain.KindInitiative, id); got != "ABC-1" {
		t.Errorf("recorded parent %q after failure", got)
	}
	gi, err := e.svc.GetInitiative(ctx, id)
	if err != nil || gi.Card == nil || gi.Card.Pending == "" {
		t.Fatalf("card = %+v, %v", gi.Card, err)
	}
	p.SetError(nil)
	r, err := e.svc.SyncCards(ctx, SyncOptions{})
	if err != nil || r.Updated != 1 || r.Failed != 0 {
		t.Fatalf("report = %+v, %v", r, err)
	}
	us := updates(p.Calls())
	if len(us) != 1 || us[0].Request.ParentKey != "ABC-2" || us[0].Request.PreviousParentKey != "ABC-1" {
		t.Fatalf("updates = %+v", us)
	}
	if got := recordedParent(t, e, domain.KindInitiative, id); got != "ABC-2" {
		t.Errorf("recorded parent %q after retry", got)
	}
}

func TestLegacyInitiativeCardUpdateHasEmptyParents(t *testing.T) {
	ctx := context.Background()
	e, p := withProvider(newEnv(t))
	i, err := e.svc.CreateInitiative(ctx, InitiativeInput{Title: "Init"})
	if err != nil {
		t.Fatal(err)
	}
	title := "Edited"
	if v, err := e.svc.UpdateInitiative(ctx, iid(t, i.ID), InitiativePatch{Title: &title}); err != nil || v.CardWarning != "" {
		t.Fatalf("%v %q", err, v.CardWarning)
	}
	us := updates(p.Calls())
	if len(us) != 1 || us[0].Request.ParentKey != "" || us[0].Request.PreviousParentKey != "" {
		t.Fatalf("updates = %+v", us)
	}
}

func TestHashIgnoresPreviousParentKey(t *testing.T) {
	a := taskmanager.CardRequest{Kind: domain.KindInitiative, ID: "I-1", Title: "t", Description: "d"}
	b := a
	b.PreviousParentKey = "ABC-1"
	if a.Hash() != b.Hash() {
		t.Error("hash depends on PreviousParentKey")
	}
	const stored = "6007ef794e6af8ed03be1f8b3c597d297138dbc0c72e4bb47603ed4fe64a8e71"
	if got := a.Hash(); got != stored {
		t.Errorf("hash = %s, want %s", got, stored)
	}
}
