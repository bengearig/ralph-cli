package service

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/bengearig/ralph/internal/domain"
	"github.com/bengearig/ralph/internal/taskmanager"
	tmfake "github.com/bengearig/ralph/internal/taskmanager/fake"
)

func methods(calls []tmfake.Call) []string {
	var out []string
	for _, c := range calls {
		s := c.Method + ":" + c.Key
		if c.Method == "SetStatus" {
			s += ":" + c.Request.Status
		}
		out = append(out, s)
	}
	return out
}

func eq(a, b []string) bool { return strings.Join(a, ",") == strings.Join(b, ",") }

func statusFixture(t *testing.T) (env, *tmfake.Provider, int64, int64) {
	t.Helper()
	ctx := context.Background()
	e, p := withProvider(newEnv(t))
	i, _ := e.svc.CreateInitiative(ctx, InitiativeInput{Title: "I"})
	m, _ := e.svc.CreateMilestone(ctx, MilestoneInput{InitiativeID: iid(t, i.ID), Title: "M"})
	tk, err := e.svc.CreateTask(ctx, TaskInput{MilestoneID: mid(t, m.ID), Title: "T", Description: "d", AcceptanceCriteria: "c"})
	if err != nil {
		t.Fatal(err)
	}
	return e, p, mid(t, m.ID), tid(t, tk.ID)
}

func TestSetTaskStatusPushesTaskThenMilestone(t *testing.T) {
	ctx := context.Background()
	e, p, mID, tID := statusFixture(t)
	if _, err := e.svc.SetTaskStatus(ctx, tID, domain.StatusTodo, "ready"); err != nil {
		t.Fatal(err)
	}
	n := len(p.Calls())
	v, err := e.svc.SetTaskStatus(ctx, tID, domain.StatusInProgress, "")
	if err != nil || v.CardWarning != "" {
		t.Fatalf("%v %q", err, v.CardWarning)
	}
	got := methods(p.Calls()[n:])
	want := []string{"Update:FAKE-3", "SetStatus:FAKE-3:IN_PROGRESS", "Update:FAKE-2", "SetStatus:FAKE-2:IN_PROGRESS"}
	if !eq(got, want) {
		t.Fatalf("calls = %v, want %v", got, want)
	}
	row, _ := e.svc.store.GetCard(ctx, domain.KindTask, tID, "fake")
	req, _ := e.svc.cardRequest(ctx, domain.KindTask, tID, "FAKE-2", true)
	if req.Status != "IN_PROGRESS" || row.ContentHash != req.Hash() {
		t.Errorf("hash %q, request %+v", row.ContentHash, req)
	}

	n = len(p.Calls())
	if _, err := e.svc.SetTaskStatus(ctx, tID, domain.StatusDone, ""); err != nil {
		t.Fatal(err)
	}
	got = methods(p.Calls()[n:])
	want = []string{"Update:FAKE-3", "SetStatus:FAKE-3:DONE", "Update:FAKE-2", "SetStatus:FAKE-2:DONE"}
	if !eq(got, want) {
		t.Fatalf("calls = %v, want %v", got, want)
	}
	_ = mID
}

func TestSetTaskStatusMilestoneUnchangedMakesNoCall(t *testing.T) {
	ctx := context.Background()
	e, p, _, t1 := statusFixture(t)
	t2v, _ := e.svc.CreateTask(ctx, TaskInput{MilestoneID: 1, Title: "T2", Description: "d", AcceptanceCriteria: "c"})
	t2 := tid(t, t2v.ID)
	for _, id := range []int64{t1, t2} {
		if _, err := e.svc.SetTaskStatus(ctx, id, domain.StatusTodo, "ready"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := e.svc.SetTaskStatus(ctx, t1, domain.StatusInProgress, ""); err != nil {
		t.Fatal(err)
	}
	n := len(p.Calls())
	if _, err := e.svc.SetTaskStatus(ctx, t2, domain.StatusInProgress, ""); err != nil {
		t.Fatal(err)
	}
	got := methods(p.Calls()[n:])
	if want := []string{"Update:FAKE-4", "SetStatus:FAKE-4:IN_PROGRESS"}; !eq(got, want) {
		t.Fatalf("calls = %v, want %v", got, want)
	}
}

func TestCancelAndReopenPushStatus(t *testing.T) {
	ctx := context.Background()
	e, p, _, tID := statusFixture(t)
	n := len(p.Calls())
	if _, err := e.svc.SetTaskStatus(ctx, tID, domain.StatusCancelled, "dropped"); err != nil {
		t.Fatal(err)
	}
	if got := methods(p.Calls()[n:]); len(got) < 2 || got[1] != "SetStatus:FAKE-3:CANCELLED" {
		t.Fatalf("calls = %v", got)
	}

	e, p, _, tID = statusFixture(t)
	for _, to := range []domain.Status{domain.StatusTodo, domain.StatusInProgress, domain.StatusDone} {
		if _, err := e.svc.SetTaskStatus(ctx, tID, to, "r"); err != nil {
			t.Fatal(err)
		}
	}
	n = len(p.Calls())
	v, err := e.svc.ReopenTask(ctx, tID, "lost")
	if err != nil || v.CardWarning != "" {
		t.Fatalf("%v %q", err, v.CardWarning)
	}
	if got := methods(p.Calls()[n:]); len(got) < 2 || got[1] != "SetStatus:FAKE-3:TODO" {
		t.Fatalf("calls = %v", got)
	}
}

func TestCreateWithStatusOrderAndPartialFailure(t *testing.T) {
	ctx := context.Background()
	e, p := withProvider(newEnv(t))
	i, _ := e.svc.CreateInitiative(ctx, InitiativeInput{Title: "I"})
	m, _ := e.svc.CreateMilestone(ctx, MilestoneInput{InitiativeID: iid(t, i.ID), Title: "M"})
	boom := errors.New("status boom")
	p.SetHook(func(_ int, method string, _ taskmanager.CardRequest) error {
		if method == "SetStatus" {
			return boom
		}
		return nil
	})
	v, err := e.svc.CreateTask(ctx, TaskInput{MilestoneID: mid(t, m.ID), Title: "T"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(v.CardWarning, "FAKE-3") || !strings.Contains(v.CardWarning, "status not set: status boom") {
		t.Errorf("warning = %q", v.CardWarning)
	}
	id := tid(t, v.ID)
	row, _ := e.svc.store.GetCard(ctx, domain.KindTask, id, "fake")
	if row.Key != "FAKE-3" || row.URL == "" || row.ContentHash != "" || !strings.Contains(row.LastError, "status boom") {
		t.Fatalf("row = %+v", row)
	}
	p.SetHook(nil)
	n := len(p.Calls())
	if r, w, _ := e.svc.syncCard(ctx, domain.KindTask, id, modeExisting, e.svc.cardClaimStale); r != updated || w != "" {
		t.Fatalf("sync = %v %q", r, w)
	}
	if got := methods(p.Calls()[n:]); !eq(got, []string{"Update:FAKE-3", "SetStatus:FAKE-3:PROPOSED"}) {
		t.Errorf("calls = %v", got)
	}
	row, _ = e.svc.store.GetCard(ctx, domain.KindTask, id, "fake")
	if row.ContentHash == "" || row.LastError != "" {
		t.Errorf("row = %+v", row)
	}
}

func TestStatusFailureNeverBlocks(t *testing.T) {
	ctx := context.Background()
	e, p, _, tID := statusFixture(t)
	if _, err := e.svc.SetTaskStatus(ctx, tID, domain.StatusTodo, "ready"); err != nil {
		t.Fatal(err)
	}
	p.SetHook(func(_ int, method string, _ taskmanager.CardRequest) error {
		if method == "SetStatus" {
			return errors.New("nope")
		}
		return nil
	})
	v, err := e.svc.SetTaskStatus(ctx, tID, domain.StatusInProgress, "")
	if err != nil || v.Status != "IN_PROGRESS" || !strings.Contains(v.CardWarning, "status not set: nope") {
		t.Fatalf("%v %+v", err, v)
	}
	row, _ := e.svc.store.GetCard(ctx, domain.KindTask, tID, "fake")
	if row.Key != "FAKE-3" || !strings.Contains(row.LastError, "nope") {
		t.Errorf("row = %+v", row)
	}
	p.SetError(errors.New("down"))
	for _, to := range []domain.Status{domain.StatusTodo, domain.StatusInProgress, domain.StatusTodo} {
		v, err = e.svc.SetTaskStatus(ctx, tID, to, "failed")
		if err != nil {
			t.Fatal(err)
		}
	}
	if v.Status != "BLOCKED" {
		t.Errorf("status = %s, want BLOCKED", v.Status)
	}
}

func TestSetTaskStatusNoRowOrInTxMakesNoCalls(t *testing.T) {
	ctx := context.Background()
	e, p, _, tID := statusFixture(t)
	n := len(p.Calls())
	err := e.svc.InTx(ctx, func(tx *Service) error {
		_, err := tx.SetTaskStatus(ctx, tID, domain.StatusTodo, "ready")
		return err
	})
	if err != nil || len(p.Calls()) != n {
		t.Fatalf("%v, calls %d", err, len(p.Calls())-n)
	}

	if err := e.svc.store.DeleteCards(ctx, domain.KindTask, tID); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.store.DeleteCards(ctx, domain.KindMilestone, 1); err != nil {
		t.Fatal(err)
	}
	n = len(p.Calls())
	if _, err := e.svc.SetTaskStatus(ctx, tID, domain.StatusInProgress, ""); err != nil {
		t.Fatal(err)
	}
	if len(p.Calls()) != n {
		t.Errorf("calls = %v", methods(p.Calls()[n:]))
	}

	e.svc.taskManager = nil
	if _, err := e.svc.SetTaskStatus(ctx, tID, domain.StatusDone, ""); err != nil {
		t.Fatal(err)
	}
}

func TestSetTaskStatusReportsAssigneeConflict(t *testing.T) {
	const taskKey, milestoneKey = "FAKE-3", "FAKE-2"
	cases := []struct {
		name     string
		failKeys map[string]error
		warn     bool
		want     func(tID int64) *CardConflictView
	}{
		{"task card", map[string]error{taskKey: &taskmanager.AssigneeConflictError{Key: taskKey, Assignee: "Pat"}}, true,
			func(tID int64) *CardConflictView {
				return &CardConflictView{Card: taskKey, Resource: domain.FormatID(domain.KindTask, tID), Assignee: "Pat"}
			}},
		{"milestone card", map[string]error{milestoneKey: &taskmanager.AssigneeConflictError{Key: milestoneKey, Assignee: "Pat"}}, true,
			func(int64) *CardConflictView {
				return &CardConflictView{Card: milestoneKey, Resource: domain.FormatID(domain.KindMilestone, 1), Assignee: "Pat"}
			}},
		{"both", map[string]error{
			taskKey:      &taskmanager.AssigneeConflictError{Key: taskKey, Assignee: "Pat"},
			milestoneKey: &taskmanager.AssigneeConflictError{Key: milestoneKey, Assignee: "Sam"},
		}, true, func(tID int64) *CardConflictView {
			return &CardConflictView{Card: taskKey, Resource: domain.FormatID(domain.KindTask, tID), Assignee: "Pat"}
		}},
		{"other error", map[string]error{taskKey: errors.New("nope")}, true, func(int64) *CardConflictView { return nil }},
		{"no error", nil, false, func(int64) *CardConflictView { return nil }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			e, p, _, tID := statusFixture(t)
			if _, err := e.svc.SetTaskStatus(ctx, tID, domain.StatusTodo, "ready"); err != nil {
				t.Fatal(err)
			}
			p.SetHook(func(_ int, method string, req taskmanager.CardRequest) error {
				if method == "SetStatus" {
					return tc.failKeys[req.ID]
				}
				return nil
			})
			v, err := e.svc.SetTaskStatus(ctx, tID, domain.StatusInProgress, "")
			if err != nil {
				t.Fatal(err)
			}
			if v.Status != "IN_PROGRESS" {
				t.Errorf("status = %s", v.Status)
			}
			stored, err := e.svc.store.GetTask(ctx, tID)
			if err != nil || stored.Status != domain.StatusInProgress {
				t.Errorf("stored = %v, %v", stored.Status, err)
			}
			if (v.CardWarning != "") != tc.warn {
				t.Errorf("warning = %q", v.CardWarning)
			}
			want := tc.want(tID)
			if (v.CardConflict == nil) != (want == nil) || (want != nil && *v.CardConflict != *want) {
				t.Errorf("conflict = %+v, want %+v", v.CardConflict, want)
			}
		})
	}
}

func TestAssigneeConflictLeavesCardPending(t *testing.T) {
	ctx := context.Background()
	e, p, _, tID := statusFixture(t)
	if _, err := e.svc.SetTaskStatus(ctx, tID, domain.StatusTodo, "ready"); err != nil {
		t.Fatal(err)
	}
	conflict := &taskmanager.AssigneeConflictError{Key: "FAKE-3", Assignee: "Pat"}
	p.SetHook(func(_ int, method string, req taskmanager.CardRequest) error {
		if method == "SetStatus" && req.ID == "FAKE-3" {
			return conflict
		}
		return nil
	})
	v, err := e.svc.SetTaskStatus(ctx, tID, domain.StatusInProgress, "")
	if err != nil {
		t.Fatal(err)
	}
	if want := "fake card FAKE-3 for T-1 status not set: " + conflict.Error(); v.CardWarning != want {
		t.Errorf("warning = %q, want %q", v.CardWarning, want)
	}
	row, _ := e.svc.store.GetCard(ctx, domain.KindTask, tID, "fake")
	req, _ := e.svc.cardRequest(ctx, domain.KindTask, tID, "FAKE-2", true)
	if row.ContentHash == req.Hash() || row.LastError != conflict.Error() {
		t.Errorf("row = %+v", row)
	}
	ix, err := e.svc.loadCards(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if cv := ix.view(ctx, domain.KindTask, tID, cardID{domain.KindMilestone, 1}); cv == nil || cv.Pending != "update" {
		t.Errorf("card = %+v", cv)
	}
}
