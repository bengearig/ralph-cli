package service

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/bengearig/ralph/internal/domain"
	"github.com/bengearig/ralph/internal/looplock"
	"github.com/bengearig/ralph/internal/taskmanager"
	tmfake "github.com/bengearig/ralph/internal/taskmanager/fake"
)

func deleteFixture(t *testing.T) (env, func() []string, int64, int64, int64, int64) {
	t.Helper()
	ctx := context.Background()
	e, p, mID, t1 := statusFixture(t)
	e.svc.cardCanceledStatus = "Canceled"
	m, err := e.svc.store.GetMilestone(ctx, mID)
	if err != nil {
		t.Fatal(err)
	}
	tk, err := e.svc.CreateTask(ctx, TaskInput{MilestoneID: mID, Title: "T2", Description: "d", AcceptanceCriteria: "c"})
	if err != nil {
		t.Fatal(err)
	}
	n := len(p.Calls())
	return e, func() []string { return methods(p.Calls()[n:]) }, m.InitiativeID, mID, t1, tid(t, tk.ID)
}

func TestDeleteMovesCardsToCanceled(t *testing.T) {
	ctx := context.Background()
	t.Run("task", func(t *testing.T) {
		e, calls, _, _, t1, _ := deleteFixture(t)
		row, _ := e.svc.store.GetCard(ctx, domain.KindTask, t1, "fake")
		v, err := e.svc.DeleteTask(ctx, t1, true)
		if err != nil {
			t.Fatal(err)
		}
		if got := calls(); !eq(got, []string{"SetStatus:" + row.Key + ":CANCELLED"}) {
			t.Errorf("calls = %v", got)
		}
		if len(v.Cards) != 1 || !v.Cards[0].Moved || v.Cards[0].Key != row.Key {
			t.Errorf("cards = %+v", v.Cards)
		}
		if _, err := e.svc.store.GetCard(ctx, domain.KindTask, t1, "fake"); err == nil {
			t.Error("card row remains")
		}
	})
	t.Run("milestone", func(t *testing.T) {
		e, calls, _, mID, t1, t2 := deleteFixture(t)
		keys := map[domain.Kind][]string{}
		for _, c := range []cardID{{domain.KindTask, t1}, {domain.KindTask, t2}, {domain.KindMilestone, mID}} {
			r, _ := e.svc.store.GetCard(ctx, c.kind, c.id, "fake")
			keys[c.kind] = append(keys[c.kind], "SetStatus:"+r.Key+":CANCELLED")
		}
		if _, err := e.svc.DeleteMilestone(ctx, mID, true); err != nil {
			t.Fatal(err)
		}
		want := append(keys[domain.KindTask], keys[domain.KindMilestone]...)
		if got := calls(); !eq(got, want) {
			t.Errorf("calls = %v, want %v", got, want)
		}
	})
	t.Run("initiative", func(t *testing.T) {
		e, calls, iID, mID, t1, t2 := deleteFixture(t)
		var want []string
		for _, c := range []cardID{{domain.KindTask, t1}, {domain.KindTask, t2}, {domain.KindMilestone, mID}, {domain.KindInitiative, iID}} {
			r, _ := e.svc.store.GetCard(ctx, c.kind, c.id, "fake")
			want = append(want, "SetStatus:"+r.Key+":CANCELLED")
		}
		v, err := e.svc.DeleteInitiative(ctx, iID, true)
		if err != nil {
			t.Fatal(err)
		}
		if got := calls(); !eq(got, want) {
			t.Errorf("calls = %v, want %v", got, want)
		}
		if len(v.Cards) != 4 {
			t.Errorf("cards = %+v", v.Cards)
		}
		cs, _ := e.svc.store.ListCards(ctx, "fake")
		if len(cs) != 0 {
			t.Errorf("rows remain: %+v", cs)
		}
	})
}

func TestDeleteSkipsEmptyKeysAndMissingRows(t *testing.T) {
	ctx := context.Background()
	e, calls, _, _, t1, t2 := deleteFixture(t)
	if err := e.svc.RecordCard(ctx, domain.KindTask, t1, "fake", "", "", ""); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.store.DeleteCards(ctx, domain.KindTask, t2); err != nil {
		t.Fatal(err)
	}
	for _, id := range []int64{t1, t2} {
		if _, err := e.svc.DeleteTask(ctx, id, true); err != nil {
			t.Fatal(err)
		}
	}
	if got := calls(); len(got) != 0 {
		t.Errorf("calls = %v", got)
	}
}

func TestDeleteRefusedMovesNothing(t *testing.T) {
	ctx := context.Background()
	e, _, _, mID, t1, _ := deleteFixture(t)
	if _, err := e.svc.SetTaskStatus(ctx, t1, domain.StatusTodo, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.SetTaskStatus(ctx, t1, domain.StatusInProgress, ""); err != nil {
		t.Fatal(err)
	}
	p := e.svc.taskManager.(*tmfake.Provider)
	n := len(p.Calls())
	if _, err := e.svc.DeleteTask(ctx, t1, false); !errors.Is(err, ErrNeedsForce) {
		t.Fatalf("err = %v", err)
	}
	if _, err := e.svc.DeleteMilestone(ctx, mID, false); !errors.Is(err, ErrNeedsForce) {
		t.Fatalf("err = %v", err)
	}

	lockPath := filepath.Join(t.TempDir(), "loop.lock")
	e.svc.loopLockPath = lockPath
	lock, err := looplock.Acquire(lockPath, "milestone "+domain.FormatID(domain.KindMilestone, mID))
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Release()
	if _, held, _ := looplock.Probe(lockPath); held {
		if _, err := e.svc.DeleteTask(ctx, t1, true); !errors.Is(err, looplock.ErrLoopRunning) {
			t.Fatalf("err = %v", err)
		}
	}
	if got := len(p.Calls()) - n; got != 0 {
		t.Errorf("refused deletes made %d calls", got)
	}
}

func TestPreviewDeleteListsCardsOnlyWithCanceledStatus(t *testing.T) {
	ctx := context.Background()
	e, calls, _, mID, _, _ := deleteFixture(t)
	v, err := e.svc.PreviewDelete(ctx, domain.KindMilestone, mID, true)
	if err != nil || len(v.Cards) != 3 || v.Cards[0].Moved {
		t.Fatalf("%v %+v", err, v.Cards)
	}
	e.svc.cardCanceledStatus = ""
	v, err = e.svc.PreviewDelete(ctx, domain.KindMilestone, mID, true)
	if err != nil || len(v.Cards) != 0 {
		t.Fatalf("%v %+v", err, v.Cards)
	}
	if got := calls(); len(got) != 0 {
		t.Errorf("calls = %v", got)
	}
}

func TestDeleteCardFailureNeverBlocks(t *testing.T) {
	ctx := context.Background()
	e, calls, _, mID, t1, _ := deleteFixture(t)
	bad, _ := e.svc.store.GetCard(ctx, domain.KindTask, t1, "fake")
	var seen int
	e.svc.taskManager.(*tmfake.Provider).SetHook(func(_ int, method string, _ taskmanager.CardRequest) error {
		if method != "SetStatus" {
			return nil
		}
		seen++
		if seen == 1 {
			return errors.New("nope")
		}
		return nil
	})
	v, err := e.svc.DeleteMilestone(ctx, mID, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(calls()) != 2 || len(v.Cards) != 3 {
		t.Fatalf("calls %v cards %+v", calls(), v.Cards)
	}
	for _, c := range v.Cards {
		failed := c.Key == bad.Key
		if c.Moved == failed || (failed && c.Error != "nope") || (!failed && c.Error != "") {
			t.Errorf("card %+v (failing key %s)", c, bad.Key)
		}
	}
	if _, err := e.svc.Store().GetMilestone(ctx, mID); err == nil {
		t.Error("milestone not deleted")
	}
}

func TestDeleteWithoutProviderOrInTxMakesNoCalls(t *testing.T) {
	ctx := context.Background()
	e, calls, _, _, t1, t2 := deleteFixture(t)
	err := e.svc.InTx(ctx, func(tx *Service) error {
		_, err := tx.DeleteTask(ctx, t1, true)
		return err
	})
	if err != nil || len(calls()) != 0 {
		t.Fatalf("%v %v", err, calls())
	}
	e.svc.taskManager = nil
	if v, err := e.svc.DeleteTask(ctx, t2, true); err != nil || len(v.Cards) != 0 {
		t.Fatalf("%v %+v", err, v)
	}
	if got := calls(); len(got) != 0 {
		t.Errorf("calls = %v", got)
	}
}

func TestDeleteLeavesOtherInitiativesCards(t *testing.T) {
	ctx := context.Background()
	e, _, iID, _, _, _ := deleteFixture(t)
	other, err := e.svc.CreateInitiative(ctx, InitiativeInput{Title: "Other"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.DeleteInitiative(ctx, iID, true); err != nil {
		t.Fatal(err)
	}
	rows, _ := e.svc.store.ListCards(ctx, "fake")
	if len(rows) != 1 || rows[0].EntityID != iid(t, other.ID) {
		t.Errorf("rows = %+v", rows)
	}
}
