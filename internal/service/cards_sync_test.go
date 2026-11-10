package service

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/bengearig/ralph/internal/domain"
	"github.com/bengearig/ralph/internal/store"
	"github.com/bengearig/ralph/internal/taskmanager"
	tmfake "github.com/bengearig/ralph/internal/taskmanager/fake"
)

func syncFixture(t *testing.T) (env, InitiativeView, MilestoneView, TaskView, func() int) {
	t.Helper()
	ctx := context.Background()
	e, p := withProvider(newEnv(t))
	i, err := e.svc.CreateInitiative(ctx, InitiativeInput{Title: "Init", Brief: "brief"})
	if err != nil {
		t.Fatal(err)
	}
	m, err := e.svc.CreateMilestone(ctx, MilestoneInput{InitiativeID: iid(t, i.ID), Title: "Mile", Description: "mdesc"})
	if err != nil {
		t.Fatal(err)
	}
	tk, err := e.svc.CreateTask(ctx, TaskInput{MilestoneID: mid(t, m.ID), Title: "Task", Description: "tdesc", AcceptanceCriteria: "crit"})
	if err != nil {
		t.Fatal(err)
	}
	return e, i, m, tk, func() int { return len(p.Calls()) }
}

func TestUpdateTaskPushesEdit(t *testing.T) {
	ctx := context.Background()
	e, _, _, tk, calls := syncFixture(t)
	id := tid(t, tk.ID)
	before, _ := e.svc.store.GetCard(ctx, domain.KindTask, id, "fake")
	n := calls()
	title := "New title"
	v, err := e.svc.UpdateTask(ctx, id, TaskPatch{Title: &title})
	if err != nil || v.CardWarning != "" {
		t.Fatalf("%v %q", err, v.CardWarning)
	}
	if calls() != n+1 {
		t.Fatalf("calls = %d, want %d", calls(), n+1)
	}
	after, _ := e.svc.store.GetCard(ctx, domain.KindTask, id, "fake")
	if after.ContentHash == before.ContentHash || after.Key != before.Key || after.SyncedAt.Before(before.SyncedAt) {
		t.Errorf("row %+v -> %+v", before, after)
	}
	req, _ := buildCardRequest(ctx, e.svc.store, domain.KindTask, id, "FAKE-2")
	if req.Title != title || after.ContentHash != req.Hash() {
		t.Errorf("hash mismatch")
	}
	if _, err := e.svc.UpdateTask(ctx, id, TaskPatch{Title: &title}); err != nil || calls() != n+1 {
		t.Errorf("repeat edit called provider: %d", calls())
	}
	pos := 5
	if _, err := e.svc.UpdateTask(ctx, id, TaskPatch{Position: &pos}); err != nil || calls() != n+1 {
		t.Errorf("position edit called provider: %d", calls())
	}
}

func TestUpdatePushesEachKind(t *testing.T) {
	ctx := context.Background()
	e, i, m, tk, calls := syncFixture(t)
	n := calls()
	brief, desc, ac := "new brief", "new mdesc", "new crit"
	if _, err := e.svc.UpdateInitiative(ctx, iid(t, i.ID), InitiativePatch{Brief: &brief}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.UpdateMilestone(ctx, mid(t, m.ID), MilestonePatch{Description: &desc}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.UpdateTask(ctx, tid(t, tk.ID), TaskPatch{AcceptanceCriteria: &ac}); err != nil {
		t.Fatal(err)
	}
	if calls() != n+3 {
		t.Fatalf("calls = %d", calls())
	}
	var got [3]taskmanager.CardRequest
	for k, c := range e.svc.taskManager.(*tmfake.Provider).Calls()[n:] {
		if c.Method != "Update" {
			t.Errorf("method %s", c.Method)
		}
		got[k] = c.Request
	}
	if got[0].Description != brief || got[1].Description != desc || !strings.Contains(got[2].Description, ac) {
		t.Errorf("requests %+v", got)
	}
}

func TestUpdateFailureNeverBlocks(t *testing.T) {
	ctx := context.Background()
	e, _, _, tk, _ := syncFixture(t)
	fp := e.svc.taskManager.(*tmfake.Provider)
	id := tid(t, tk.ID)
	before, _ := e.svc.store.GetCard(ctx, domain.KindTask, id, "fake")
	fp.SetError(errors.New("boom"))
	title := "T2"
	v, err := e.svc.UpdateTask(ctx, id, TaskPatch{Title: &title})
	if err != nil || v.Title != title {
		t.Fatalf("%v %+v", err, v)
	}
	for _, s := range []string{tk.ID, before.Key, "boom"} {
		if !strings.Contains(v.CardWarning, s) {
			t.Errorf("warning %q lacks %q", v.CardWarning, s)
		}
	}
	row, _ := e.svc.store.GetCard(ctx, domain.KindTask, id, "fake")
	if row.Key != before.Key || row.URL != before.URL || row.ContentHash != before.ContentHash || !strings.Contains(row.LastError, "boom") {
		t.Errorf("row %+v", row)
	}
	fp.SetError(nil)
	title = "T3"
	v, err = e.svc.UpdateTask(ctx, id, TaskPatch{Title: &title})
	if err != nil || v.CardWarning != "" {
		t.Fatalf("%v %q", err, v.CardWarning)
	}
	row, _ = e.svc.store.GetCard(ctx, domain.KindTask, id, "fake")
	if row.LastError != "" || row.ContentHash == before.ContentHash {
		t.Errorf("row %+v", row)
	}
}

func TestSyncCardModes(t *testing.T) {
	ctx := context.Background()
	e, p := withProvider(newEnv(t))
	i, _ := e.svc.CreateInitiative(ctx, InitiativeInput{Title: "I"})
	e.svc.taskManager = nil
	m, _ := e.svc.CreateMilestone(ctx, MilestoneInput{InitiativeID: iid(t, i.ID), Title: "M"})
	tk, _ := e.svc.CreateTask(ctx, TaskInput{MilestoneID: mid(t, m.ID), Title: "T"})
	e.svc.taskManager = p
	n := len(p.Calls())
	tID := tid(t, tk.ID)

	if r, w := e.svc.syncCard(ctx, domain.KindTask, tID, modeExisting); r != skipped || w != "" {
		t.Errorf("existing: %v %q", r, w)
	}
	if _, err := e.svc.store.GetCard(ctx, domain.KindTask, tID, "fake"); !errors.Is(err, store.ErrNotFound) || len(p.Calls()) != n {
		t.Errorf("row written or call made: %v", err)
	}
	if r, w := e.svc.syncCard(ctx, domain.KindTask, tID, modeCreateMissing); r != created || w != "" {
		t.Errorf("create: %v %q", r, w)
	}
	if len(p.Calls()) != n+2 {
		t.Errorf("calls = %d, want milestone and task created", len(p.Calls())-n)
	}
	n = len(p.Calls())
	if r, _ := e.svc.syncCard(ctx, domain.KindTask, tID, modeCreateMissing); r != inSync || len(p.Calls()) != n {
		t.Errorf("fresh card: %v", r)
	}
}

func TestSyncCardEmptyKeyRetried(t *testing.T) {
	ctx := context.Background()
	e, p := withProvider(newEnv(t))
	p.SetError(errors.New("down"))
	i, _ := e.svc.CreateInitiative(ctx, InitiativeInput{Title: "I"})
	p.SetError(nil)
	e.svc.cardClaimStale = time.Millisecond
	time.Sleep(5 * time.Millisecond)
	id := iid(t, i.ID)
	row, err := e.svc.store.GetCard(ctx, domain.KindInitiative, id, "fake")
	if err != nil || row.Key != "" {
		t.Fatalf("%+v %v", row, err)
	}
	b := "x"
	if v, err := e.svc.UpdateInitiative(ctx, id, InitiativePatch{Brief: &b}); err != nil || v.CardWarning != "" {
		t.Fatalf("%v %q", err, v.CardWarning)
	}
	row, _ = e.svc.store.GetCard(ctx, domain.KindInitiative, id, "fake")
	if row.Key == "" || row.ContentHash == "" {
		t.Errorf("row %+v", row)
	}
	if c := p.Calls(); len(c) != 1 || c[0].Method != "Create" {
		t.Errorf("calls %+v", c)
	}
}

func TestSyncCardGuards(t *testing.T) {
	ctx := context.Background()
	e, _, _, tk, calls := syncFixture(t)
	id := tid(t, tk.ID)
	n := calls()
	title := "Changed"
	err := e.svc.InTx(ctx, func(txs *Service) error {
		if _, err := txs.UpdateTask(ctx, id, TaskPatch{Title: &title}); err != nil {
			return err
		}
		return errors.New("rollback")
	})
	if err == nil || calls() != n {
		t.Errorf("err=%v calls=%d", err, calls()-n)
	}
	tm := e.svc.taskManager
	e.svc.taskManager = nil
	other := "Other"
	if _, err := e.svc.UpdateTask(ctx, id, TaskPatch{Title: &other}); err != nil || calls() != n {
		t.Errorf("nil provider: %v", err)
	}
	e.svc.taskManager = tm
}

func TestBuildCardRequestStatus(t *testing.T) {
	ctx := context.Background()
	e, i, m, tk, _ := syncFixture(t)
	st := e.svc.store
	ir, err := buildCardRequest(ctx, st, domain.KindInitiative, iid(t, i.ID), "")
	if err != nil || ir.Status != "" {
		t.Errorf("initiative status = %q, %v", ir.Status, err)
	}
	tr, err := buildCardRequest(ctx, st, domain.KindTask, tid(t, tk.ID), "FAKE-2")
	if err != nil || tr.Status != string(domain.StatusProposed) {
		t.Errorf("task status = %q, %v", tr.Status, err)
	}
	mr, err := buildCardRequest(ctx, st, domain.KindMilestone, mid(t, m.ID), "FAKE-1")
	if err != nil {
		t.Fatal(err)
	}
	mv, err := e.svc.GetMilestone(ctx, mid(t, m.ID))
	if err != nil || mr.Status == "" || mr.Status != mv.Status {
		t.Errorf("milestone status = %q, view %q, %v", mr.Status, mv.Status, err)
	}
}
