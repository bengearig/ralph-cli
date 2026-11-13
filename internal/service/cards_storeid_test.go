package service

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/bengearig/ralph/internal/domain"
	"github.com/bengearig/ralph/internal/store"
)

func TestCardRequestStoreIDConfigured(t *testing.T) {
	ctx := context.Background()
	e, p := withProvider(newEnv(t))
	e.svc.storeID = "ab"
	if i, err := e.svc.CreateInitiative(ctx, InitiativeInput{Title: "I"}); err != nil || i.CardWarning != "" {
		t.Fatalf("%v %q", err, i.CardWarning)
	}
	cs := creates(p.Calls())
	if len(cs) != 1 || cs[0].Request.StoreID != "ab" {
		t.Fatalf("creates = %+v", cs)
	}
	if _, err := e.svc.store.GetMeta(ctx, storeIDMetaKey); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("configured identifier was stored: %v", err)
	}
}

func TestCardRequestStoreIDGenerated(t *testing.T) {
	ctx := context.Background()
	e, p := withProvider(newEnv(t))
	if i, err := e.svc.CreateInitiative(ctx, InitiativeInput{Title: "I"}); err != nil || i.CardWarning != "" {
		t.Fatalf("%v %q", err, i.CardWarning)
	}
	kept, err := e.svc.store.GetMeta(ctx, storeIDMetaKey)
	if err != nil || kept == "" {
		t.Fatalf("kept = %q, %v", kept, err)
	}
	cs := creates(p.Calls())
	if len(cs) != 1 || cs[0].Request.StoreID != kept {
		t.Fatalf("creates = %+v, kept %q", cs, kept)
	}
}

func TestCardRequestStoreIDNoTaskManager(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	if i, err := e.svc.CreateInitiative(ctx, InitiativeInput{Title: "I"}); err != nil || i.CardWarning != "" {
		t.Fatalf("%v %q", err, i.CardWarning)
	}
	if _, err := e.svc.store.GetMeta(ctx, storeIDMetaKey); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("identifier generated without a task manager: %v", err)
	}
}

func TestCardPendingStoreID(t *testing.T) {
	ctx := context.Background()
	e, p := withProvider(newEnv(t))
	i, err := e.svc.CreateInitiative(ctx, InitiativeInput{Title: "I", Brief: "b"})
	if err != nil {
		t.Fatal(err)
	}
	id := iid(t, i.ID)
	pending := func() string {
		t.Helper()
		ix, err := e.svc.loadCards(ctx)
		if err != nil {
			t.Fatal(err)
		}
		v := ix.view(ctx, domain.KindInitiative, id, cardID{})
		if v == nil {
			t.Fatal("no card view")
		}
		return v.Pending
	}

	// A hash recorded before the identifier existed.
	e.svc.storeID = "ab"
	c, _ := e.svc.store.GetCard(ctx, domain.KindInitiative, id, "fake")
	old, err := buildCardRequest(ctx, e.svc.store, domain.KindInitiative, id, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := e.svc.RecordCard(ctx, domain.KindInitiative, id, "fake", c.Key, c.URL, old.Hash()); err != nil {
		t.Fatal(err)
	}
	if got := pending(); got != "update" {
		t.Fatalf("pending = %q, want update", got)
	}
	before := len(updates(p.Calls()))
	r, err := e.svc.SyncCards(ctx, SyncOptions{})
	if err != nil || r.Updated != 1 {
		t.Fatalf("report = %+v, %v", r, err)
	}
	us := updates(p.Calls())
	if len(us) != before+1 || us[len(us)-1].Request.StoreID != "ab" {
		t.Fatalf("updates = %+v", us)
	}
	if got := pending(); got != "" {
		t.Fatalf("pending after sync = %q", got)
	}

	e.svc.storeID = "cd"
	if got := pending(); got != "update" {
		t.Fatalf("pending after change = %q, want update", got)
	}
}

func TestCardStoreIDFailureWarns(t *testing.T) {
	ctx := context.Background()
	e, p := withProvider(newEnv(t))
	e.svc.store = failingMetaStore{e.svc.store}
	i, err := e.svc.CreateInitiative(ctx, InitiativeInput{Title: "I"})
	if err != nil {
		t.Fatalf("resource operation failed: %v", err)
	}
	if !strings.Contains(i.CardWarning, "store read") {
		t.Fatalf("warning = %q", i.CardWarning)
	}
	if len(creates(p.Calls())) != 0 {
		t.Fatalf("calls = %+v", p.Calls())
	}
}
