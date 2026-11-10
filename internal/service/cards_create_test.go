package service

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/bengearig/ralph/internal/config"
	"github.com/bengearig/ralph/internal/domain"
	"github.com/bengearig/ralph/internal/store"
	"github.com/bengearig/ralph/internal/taskmanager"
	tmfake "github.com/bengearig/ralph/internal/taskmanager/fake"
)

func withProvider(e env) (env, *tmfake.Provider) {
	p := tmfake.New()
	e.svc.taskManager = p
	return e, p
}

func iid(t *testing.T, s string) int64 { t.Helper(); return mustID(t, domain.KindInitiative, s) }
func mid(t *testing.T, s string) int64 { t.Helper(); return mustID(t, domain.KindMilestone, s) }
func tid(t *testing.T, s string) int64 { t.Helper(); return mustID(t, domain.KindTask, s) }

func TestCreateCardDisabled(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	i, err := e.svc.CreateInitiative(ctx, InitiativeInput{Title: "I", Brief: "b"})
	if err != nil {
		t.Fatal(err)
	}
	m, _ := e.svc.CreateMilestone(ctx, MilestoneInput{InitiativeID: iid(t, i.ID), Title: "M"})
	tk, _ := e.svc.CreateTask(ctx, TaskInput{MilestoneID: mid(t, m.ID), Title: "T"})
	for _, v := range []any{i, m, tk} {
		b, _ := json.Marshal(v)
		if strings.Contains(string(b), "card_warning") {
			t.Errorf("view has card_warning: %s", b)
		}
	}
	cards, err := e.svc.ListCards(ctx, "fake")
	if err != nil || len(cards) != 0 {
		t.Errorf("cards = %v, %v", cards, err)
	}
}

func TestCreateCardChain(t *testing.T) {
	ctx := context.Background()
	e, p := withProvider(newEnv(t))
	i, err := e.svc.CreateInitiative(ctx, InitiativeInput{Title: "Init", Brief: "the brief"})
	if err != nil || i.CardWarning != "" {
		t.Fatalf("%v %q", err, i.CardWarning)
	}
	m, err := e.svc.CreateMilestone(ctx, MilestoneInput{InitiativeID: iid(t, i.ID), Title: "Mile", Description: "mdesc"})
	if err != nil || m.CardWarning != "" {
		t.Fatalf("%v %q", err, m.CardWarning)
	}
	withAC, err := e.svc.CreateTask(ctx, TaskInput{MilestoneID: mid(t, m.ID), Title: "Task", Description: "tdesc", AcceptanceCriteria: "do it"})
	if err != nil || withAC.CardWarning != "" {
		t.Fatalf("%v %q", err, withAC.CardWarning)
	}
	noAC, err := e.svc.CreateTask(ctx, TaskInput{MilestoneID: mid(t, m.ID), Title: "Bare", Description: "only"})
	if err != nil {
		t.Fatal(err)
	}

	calls := p.Calls()
	if len(calls) != 4 {
		t.Fatalf("calls = %+v", calls)
	}
	want := []taskmanager.CardRequest{
		{Kind: domain.KindInitiative, ID: i.ID, Title: "Init", Description: "the brief"},
		{Kind: domain.KindMilestone, ID: m.ID, Title: "Mile", Description: "mdesc", ParentKey: "FAKE-1"},
		{Kind: domain.KindTask, ID: withAC.ID, Title: "Task", Description: "tdesc\n\n## Acceptance criteria\n\ndo it", ParentKey: "FAKE-2"},
		{Kind: domain.KindTask, ID: noAC.ID, Title: "Bare", Description: "only", ParentKey: "FAKE-2"},
	}
	for n, w := range want {
		if calls[n].Method != "Create" || calls[n].Request != w {
			t.Errorf("call %d = %+v, want %+v", n, calls[n], w)
		}
	}
	c, err := e.svc.Card(ctx, domain.KindInitiative, iid(t, i.ID), "fake")
	if err != nil || c == nil || c.Key != "FAKE-1" || c.URL == "" {
		t.Fatalf("card = %+v, %v", c, err)
	}
	stored, _ := e.svc.store.GetCard(ctx, domain.KindInitiative, iid(t, i.ID), "fake")
	if stored.ContentHash != want[0].Hash() {
		t.Errorf("hash = %q", stored.ContentHash)
	}
}

func TestCreateCardMissingParents(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	i, _ := e.svc.CreateInitiative(ctx, InitiativeInput{Title: "I"})
	m, _ := e.svc.CreateMilestone(ctx, MilestoneInput{InitiativeID: iid(t, i.ID), Title: "M"})
	if cards, _ := e.svc.ListCards(ctx, "fake"); len(cards) != 0 {
		t.Fatalf("cards = %v", cards)
	}
	e, p := withProvider(e)
	tk, err := e.svc.CreateTask(ctx, TaskInput{MilestoneID: mid(t, m.ID), Title: "T"})
	if err != nil || tk.CardWarning != "" {
		t.Fatalf("%v %q", err, tk.CardWarning)
	}
	calls := p.Calls()
	if len(calls) != 3 {
		t.Fatalf("calls = %+v", calls)
	}
	kinds := []domain.Kind{domain.KindInitiative, domain.KindMilestone, domain.KindTask}
	parents := []string{"", "FAKE-1", "FAKE-2"}
	for n := range calls {
		if calls[n].Request.Kind != kinds[n] || calls[n].Request.ParentKey != parents[n] {
			t.Errorf("call %d = %+v", n, calls[n])
		}
	}
	if cards, _ := e.svc.ListCards(ctx, "fake"); len(cards) != 3 {
		t.Errorf("cards = %v", cards)
	}
}

func TestCreateCardIdempotent(t *testing.T) {
	ctx := context.Background()
	e, p := withProvider(newEnv(t))
	i, _ := e.svc.CreateInitiative(ctx, InitiativeInput{Title: "I"})
	if w := e.svc.createCard(ctx, domain.KindInitiative, iid(t, i.ID)); w != "" {
		t.Fatal(w)
	}
	if n := len(p.Calls()); n != 1 {
		t.Errorf("calls = %d", n)
	}
}

func TestCreateCardFailureNeverBlocks(t *testing.T) {
	ctx := context.Background()
	e, p := withProvider(newEnv(t))
	p.SetError(errors.New("jira down"))
	i, err := e.svc.CreateInitiative(ctx, InitiativeInput{Title: "I"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(i.CardWarning, i.ID) || !strings.Contains(i.CardWarning, "jira down") || !strings.HasPrefix(i.CardWarning, "fake card for") {
		t.Errorf("warning = %q", i.CardWarning)
	}
	c, _ := e.svc.Card(ctx, domain.KindInitiative, iid(t, i.ID), "fake")
	if c == nil || c.Key != "" || c.LastError != "jira down" {
		t.Errorf("card = %+v", c)
	}

	m, err := e.svc.CreateMilestone(ctx, MilestoneInput{InitiativeID: iid(t, i.ID), Title: "M"})
	if err != nil || !strings.Contains(m.CardWarning, m.ID) {
		t.Fatalf("%v %q", err, m.CardWarning)
	}
	mc, _ := e.svc.Card(ctx, domain.KindMilestone, mid(t, m.ID), "fake")
	if mc == nil || mc.Key != "" || !strings.Contains(mc.LastError, "parent "+i.ID+" has no card: jira down") {
		t.Errorf("milestone card = %+v", mc)
	}

	tk, err := e.svc.CreateTask(ctx, TaskInput{MilestoneID: mid(t, m.ID), Title: "T"})
	if err != nil || !strings.Contains(tk.CardWarning, "jira down") || !strings.Contains(tk.CardWarning, "parent "+m.ID) {
		t.Fatalf("%v %q", err, tk.CardWarning)
	}
	if len(p.Calls()) != 0 {
		t.Errorf("calls = %+v", p.Calls())
	}
}

func TestCreateCardInTx(t *testing.T) {
	ctx := context.Background()
	e, p := withProvider(newEnv(t))
	var iv InitiativeView
	err := e.svc.InTx(ctx, func(tx *Service) error {
		var err error
		iv, err = tx.CreateInitiative(ctx, InitiativeInput{Title: "I"})
		return err
	})
	if err != nil || iv.CardWarning != "" {
		t.Fatalf("%v %q", err, iv.CardWarning)
	}
	if len(p.Calls()) != 0 {
		t.Fatalf("calls = %+v", p.Calls())
	}
	if c, _ := e.svc.Card(ctx, domain.KindInitiative, iid(t, iv.ID), "fake"); c != nil {
		t.Errorf("card = %+v", c)
	}
	if w := e.svc.createCard(ctx, domain.KindInitiative, iid(t, iv.ID)); w != "" {
		t.Fatal(w)
	}
	if len(p.Calls()) != 1 {
		t.Errorf("calls = %+v", p.Calls())
	}

	boom := errors.New("boom")
	err = e.svc.InTx(ctx, func(tx *Service) error {
		if _, err := tx.CreateInitiative(ctx, InitiativeInput{Title: "Gone"}); err != nil {
			return err
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatal(err)
	}
	if cards, _ := e.svc.ListCards(ctx, "fake"); len(cards) != 1 {
		t.Errorf("cards = %v", cards)
	}
}

func TestCreateCardOutsideTransaction(t *testing.T) {
	ctx := context.Background()
	e, p := withProvider(newEnv(t))
	p.SetHook(func(int, string, taskmanager.CardRequest) error {
		cctx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		return e.svc.store.InTx(cctx, func(st store.Store) error {
			_, err := st.ListInitiatives(cctx, false)
			return err
		})
	})
	i, err := e.svc.CreateInitiative(ctx, InitiativeInput{Title: "I"})
	if err != nil || i.CardWarning != "" {
		t.Fatalf("%v %q", err, i.CardWarning)
	}
	if len(p.Calls()) != 1 {
		t.Errorf("calls = %+v", p.Calls())
	}
}

func TestNewTaskManager(t *testing.T) {
	cfg := &config.Config{}
	cfg.TaskManager.Provider = config.TaskManagerNone
	if p := newTaskManager(cfg, slog.Default()); p != nil {
		t.Errorf("none gave %v", p)
	}
	cfg.TaskManager.Provider = "test-wiring"
	taskmanager.Register("test-wiring", func(*config.Config) (taskmanager.Provider, error) { return tmfake.New(), nil })
	if p := newTaskManager(cfg, nil); p == nil {
		t.Error("registered provider gave nil")
	}
	cfg.TaskManager.Provider = "unregistered-xyz"
	if p := newTaskManager(cfg, nil); p != nil {
		t.Errorf("unknown gave %v", p)
	}
}
