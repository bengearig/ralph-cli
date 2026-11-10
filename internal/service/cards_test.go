package service

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/bengearig/ralph/internal/domain"
)

func TestRecordCardAndFailure(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	_, mid := e.scaffold(t)

	if v, err := e.svc.Card(ctx, domain.KindMilestone, mid, "jira"); err != nil || v != nil {
		t.Fatalf("Card with no row = %v, %v; want nil", v, err)
	}

	if err := e.svc.RecordCardFailure(ctx, domain.KindMilestone, mid, "jira", errors.New("down")); err != nil {
		t.Fatal(err)
	}
	v, err := e.svc.Card(ctx, domain.KindMilestone, mid, "jira")
	if err != nil || v == nil || v.Key != "" || v.LastError != "down" || v.AttemptedAt == "" || v.SyncedAt != "" {
		t.Fatalf("failure with no row = %+v, %v", v, err)
	}
	if v.ID != domain.FormatID(domain.KindMilestone, mid) || v.Kind != "milestone" {
		t.Errorf("view identity = %+v", v)
	}

	if err := e.svc.RecordCard(ctx, domain.KindMilestone, mid, "jira", "P-1", "https://j/P-1", "h1"); err != nil {
		t.Fatal(err)
	}
	ok, _ := e.svc.Card(ctx, domain.KindMilestone, mid, "jira")
	if ok.Key != "P-1" || ok.URL != "https://j/P-1" || ok.LastError != "" || ok.SyncedAt == "" || ok.AttemptedAt == "" {
		t.Fatalf("RecordCard = %+v", ok)
	}
	stored, err := e.svc.store.GetCard(ctx, domain.KindMilestone, mid, "jira")
	if err != nil || stored.ContentHash != "h1" {
		t.Fatalf("stored = %+v, %v", stored, err)
	}

	if err := e.svc.RecordCardFailure(ctx, domain.KindMilestone, mid, "jira", errors.New("timeout")); err != nil {
		t.Fatal(err)
	}
	after, _ := e.svc.store.GetCard(ctx, domain.KindMilestone, mid, "jira")
	if after.Key != "P-1" || after.URL != "https://j/P-1" || after.ContentHash != "h1" ||
		!after.SyncedAt.Equal(stored.SyncedAt) || after.LastError != "timeout" || after.AttemptedAt.Before(stored.AttemptedAt) {
		t.Errorf("failure on synced card = %+v, was %+v", after, stored)
	}

	if err := e.svc.RecordCard(ctx, domain.KindMilestone, mid, "jira", "P-1", "https://j/P-1", "h2"); err != nil {
		t.Fatal(err)
	}
	if cleared, _ := e.svc.Card(ctx, domain.KindMilestone, mid, "jira"); cleared.LastError != "" {
		t.Errorf("error not cleared: %+v", cleared)
	}

	list, err := e.svc.ListCards(ctx, "jira")
	if err != nil || len(list) != 1 {
		t.Errorf("ListCards = %v, %v", list, err)
	}
}

func TestCardValidationAndJSON(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	if err := e.svc.RecordCard(ctx, domain.KindDocument, 1, "jira", "k", "u", "h"); !errors.Is(err, domain.ErrInvalid) {
		t.Errorf("document card err = %v, want ErrInvalid", err)
	}
	if err := e.svc.RecordCard(ctx, domain.KindTask, 1, " ", "k", "u", "h"); !errors.Is(err, domain.ErrInvalid) {
		t.Errorf("blank provider err = %v, want ErrInvalid", err)
	}
	if err := e.svc.RecordCardFailure(ctx, domain.KindTask, 1, "jira", nil); !errors.Is(err, domain.ErrInvalid) {
		t.Errorf("nil cause err = %v, want ErrInvalid", err)
	}

	b, err := json.Marshal(CardView{Kind: "task", ID: "T-1", Provider: "jira", AttemptedAt: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if s := string(b); strings.Contains(s, "last_error") || strings.Contains(s, "synced_at") {
		t.Errorf("JSON %s should omit empty last_error and synced_at", s)
	}
}
