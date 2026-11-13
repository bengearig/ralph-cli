package service

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/bengearig/ralph/internal/domain"
	"github.com/bengearig/ralph/internal/taskmanager"
)

func TestSprintHookCreatedRecordsOneCallForMilestoneOnly(t *testing.T) {
	ctx := context.Background()
	e, p := withProvider(newEnv(t))
	i, err := e.svc.CreateInitiative(ctx, InitiativeInput{Title: "I"})
	if err != nil || i.CardWarning != "" {
		t.Fatalf("%v %q", err, i.CardWarning)
	}
	if n := len(p.SprintCalls()); n != 0 {
		t.Fatalf("initiative recorded %d sprint calls", n)
	}
	m, err := e.svc.CreateMilestone(ctx, MilestoneInput{InitiativeID: iid(t, i.ID), Title: "M"})
	if err != nil || m.CardWarning != "" {
		t.Fatalf("%v %q", err, m.CardWarning)
	}
	calls := p.SprintCalls()
	if len(calls) != 1 || calls[0].Request.Kind != domain.KindMilestone || calls[0].Request.Moment != taskmanager.SprintMomentCreated {
		t.Fatalf("calls = %+v", calls)
	}
	if _, err := e.svc.CreateTask(ctx, TaskInput{MilestoneID: mid(t, m.ID), Title: "T"}); err != nil {
		t.Fatal(err)
	}
	if n := len(p.SprintCalls()); n != 1 {
		t.Fatalf("task recorded sprint calls: %d", n)
	}
}

func TestSprintHookErrorWarnsAndRecordsCard(t *testing.T) {
	ctx := context.Background()
	e, p := withProvider(newEnv(t))
	i, _ := e.svc.CreateInitiative(ctx, InitiativeInput{Title: "I"})
	p.SetSprintResult("", errors.New("no active sprint"))
	m, err := e.svc.CreateMilestone(ctx, MilestoneInput{InitiativeID: iid(t, i.ID), Title: "M"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(m.CardWarning, "not added to a sprint") || !strings.Contains(m.CardWarning, "no active sprint") {
		t.Fatalf("warning = %q", m.CardWarning)
	}
	row, err := e.svc.store.GetCard(ctx, domain.KindMilestone, mid(t, m.ID), "fake")
	if err != nil || row.Key == "" || row.ContentHash == "" || row.LastError != "" {
		t.Fatalf("row %+v, %v", row, err)
	}
	ix, err := e.svc.loadCards(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if v := ix.view(ctx, domain.KindMilestone, mid(t, m.ID), cardID{domain.KindInitiative, iid(t, i.ID)}); v == nil || v.Pending != "" {
		t.Fatalf("view = %+v", v)
	}
}

func TestSprintHookNoteWarns(t *testing.T) {
	ctx := context.Background()
	e, p := withProvider(newEnv(t))
	i, _ := e.svc.CreateInitiative(ctx, InitiativeInput{Title: "I"})
	p.SetSprintResult("used the newest sprint", nil)
	m, err := e.svc.CreateMilestone(ctx, MilestoneInput{InitiativeID: iid(t, i.ID), Title: "M"})
	if err != nil || !strings.Contains(m.CardWarning, "added to a sprint: used the newest sprint") {
		t.Fatalf("%v %q", err, m.CardWarning)
	}
}

func TestSprintHookStartedOnFirstTaskOnly(t *testing.T) {
	ctx := context.Background()
	e, p, _, t1 := statusFixture(t)
	t2v, _ := e.svc.CreateTask(ctx, TaskInput{MilestoneID: 1, Title: "T2", Description: "d", AcceptanceCriteria: "c"})
	t2 := tid(t, t2v.ID)
	for _, id := range []int64{t1, t2} {
		if _, err := e.svc.SetTaskStatus(ctx, id, domain.StatusTodo, "ready"); err != nil {
			t.Fatal(err)
		}
	}
	before := len(p.SprintCalls())
	if _, err := e.svc.SetTaskStatus(ctx, t1, domain.StatusInProgress, ""); err != nil {
		t.Fatal(err)
	}
	calls := p.SprintCalls()[before:]
	if len(calls) != 1 || calls[0].Request.Moment != taskmanager.SprintMomentStarted || calls[0].Request.Kind != domain.KindMilestone {
		t.Fatalf("calls = %+v", calls)
	}
	if _, err := e.svc.SetTaskStatus(ctx, t2, domain.StatusInProgress, ""); err != nil {
		t.Fatal(err)
	}
	if n := len(p.SprintCalls()) - before; n != 1 {
		t.Fatalf("sprint calls after second task = %d", n)
	}
}

func TestSprintHookRetriedCreationAddsBackfillDoesNot(t *testing.T) {
	ctx := context.Background()
	e, p := withProvider(newEnv(t))
	i, _ := e.svc.CreateInitiative(ctx, InitiativeInput{Title: "I"})
	p.SetError(errors.New("down"))
	m, _ := e.svc.CreateMilestone(ctx, MilestoneInput{InitiativeID: iid(t, i.ID), Title: "M"})
	p.SetError(nil)
	e.svc.cardTimeout = time.Millisecond
	time.Sleep(10 * time.Millisecond)
	if row, err := e.svc.store.GetCard(ctx, domain.KindMilestone, mid(t, m.ID), "fake"); err != nil || row.Key != "" {
		t.Fatalf("row %+v, %v", row, err)
	}
	if _, err := e.svc.SyncCards(ctx, SyncOptions{}); err != nil {
		t.Fatal(err)
	}
	calls := p.SprintCalls()
	if len(calls) != 1 || calls[0].Request.Moment != taskmanager.SprintMomentCreated {
		t.Fatalf("retry calls = %+v", calls)
	}

	e2, p2 := withProvider(newEnv(t))
	i2, _ := e2.svc.CreateInitiative(ctx, InitiativeInput{Title: "I"})
	e2.svc.taskManager = nil
	if _, err := e2.svc.CreateMilestone(ctx, MilestoneInput{InitiativeID: iid(t, i2.ID), Title: "M"}); err != nil {
		t.Fatal(err)
	}
	e2.svc.taskManager = p2
	if _, err := e2.svc.SyncCards(ctx, SyncOptions{}); err != nil {
		t.Fatal(err)
	}
	if n := len(p2.SprintCalls()); n != 0 {
		t.Fatalf("backfill recorded %d sprint calls", n)
	}
}

func TestSprintHookSyncItemCarriesWarning(t *testing.T) {
	ctx := context.Background()
	e, p := withProvider(newEnv(t))
	i, _ := e.svc.CreateInitiative(ctx, InitiativeInput{Title: "I"})
	p.SetError(errors.New("down"))
	e.svc.CreateMilestone(ctx, MilestoneInput{InitiativeID: iid(t, i.ID), Title: "M"})
	p.SetError(nil)
	e.svc.cardTimeout = time.Millisecond
	time.Sleep(10 * time.Millisecond)
	p.SetSprintResult("", errors.New("no board"))
	r, err := e.svc.SyncCards(ctx, SyncOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, it := range r.Items {
		if it.Outcome == "created" && strings.Contains(it.Error, "not added to a sprint") {
			found = true
		}
	}
	if !found {
		t.Fatalf("items = %+v", r.Items)
	}
}
