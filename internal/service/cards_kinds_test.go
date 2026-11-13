package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/bengearig/ralph/internal/config"
	"github.com/bengearig/ralph/internal/domain"
	tmfake "github.com/bengearig/ralph/internal/taskmanager/fake"
)

func kindsEnv(t *testing.T, kinds []domain.Kind) (env, *tmfake.Provider) {
	e, p := withProvider(newEnv(t))
	e.svc.cardKinds = kinds
	return e, p
}

func TestCreateCardKinds(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name  string
		kinds []domain.Kind
		want  [3]bool
	}{
		{"nil means all", nil, [3]bool{true, true, true}},
		{"initiative and milestone", []domain.Kind{domain.KindInitiative, domain.KindMilestone}, [3]bool{true, true, false}},
		{"initiative only", []domain.Kind{domain.KindInitiative}, [3]bool{true, false, false}},
		{"empty", []domain.Kind{}, [3]bool{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e, p := kindsEnv(t, tc.kinds)
			name := p.Name()
			i, err := e.svc.CreateInitiative(ctx, InitiativeInput{Title: "I", Brief: "b"})
			if err != nil || i.CardWarning != "" {
				t.Fatalf("%v %q", err, i.CardWarning)
			}
			m, err := e.svc.CreateMilestone(ctx, MilestoneInput{InitiativeID: iid(t, i.ID), Title: "M"})
			if err != nil || m.CardWarning != "" {
				t.Fatalf("%v %q", err, m.CardWarning)
			}
			tk, err := e.svc.CreateTask(ctx, TaskInput{MilestoneID: mid(t, m.ID), Title: "T"})
			if err != nil || tk.CardWarning != "" {
				t.Fatalf("%v %q", err, tk.CardWarning)
			}
			ids := [3]struct {
				k  domain.Kind
				id int64
			}{{domain.KindInitiative, iid(t, i.ID)}, {domain.KindMilestone, mid(t, m.ID)}, {domain.KindTask, tid(t, tk.ID)}}
			n := 0
			for j, x := range ids {
				c, err := e.svc.Card(ctx, x.k, x.id, name)
				if err != nil {
					t.Fatal(err)
				}
				if (c != nil) != tc.want[j] {
					t.Errorf("%s card present = %v, want %v", x.k, c != nil, tc.want[j])
				}
				if tc.want[j] {
					n++
				}
			}
			if got := len(creates(p.Calls())); got != n {
				t.Errorf("provider creates = %d, want %d", got, n)
			}
		})
	}
}

func TestCreateTaskUnderMilestoneWithoutCard(t *testing.T) {
	ctx := context.Background()
	e, p := kindsEnv(t, []domain.Kind{})
	i, _ := e.svc.CreateInitiative(ctx, InitiativeInput{Title: "I", Brief: "b"})
	m, _ := e.svc.CreateMilestone(ctx, MilestoneInput{InitiativeID: iid(t, i.ID), Title: "M"})
	e.svc.cardKinds = []domain.Kind{domain.KindInitiative, domain.KindMilestone}
	tk, err := e.svc.CreateTask(ctx, TaskInput{MilestoneID: mid(t, m.ID), Title: "T"})
	if err != nil || tk.CardWarning != "" {
		t.Fatalf("%v %q", err, tk.CardWarning)
	}
	cards, err := e.svc.ListCards(ctx, p.Name())
	if err != nil || len(cards) != 0 {
		t.Errorf("cards = %v, %v", cards, err)
	}
	if len(p.Calls()) != 0 {
		t.Errorf("provider calls = %v", p.Calls())
	}
}

func TestEditTaskWithFailedCardAndKindOff(t *testing.T) {
	ctx := context.Background()
	for _, enabled := range []bool{false, true} {
		e, p := kindsEnv(t, nil)
		i, _ := e.svc.CreateInitiative(ctx, InitiativeInput{Title: "I", Brief: "b"})
		m, _ := e.svc.CreateMilestone(ctx, MilestoneInput{InitiativeID: iid(t, i.ID), Title: "M"})
		e.svc.cardKinds = []domain.Kind{}
		tk, _ := e.svc.CreateTask(ctx, TaskInput{MilestoneID: mid(t, m.ID), Title: "T"})
		task := tid(t, tk.ID)
		if err := e.svc.RecordCardFailure(ctx, domain.KindTask, task, p.Name(), errors.New("down")); err != nil {
			t.Fatal(err)
		}
		e.svc.cardKinds = []domain.Kind{domain.KindInitiative, domain.KindMilestone}
		if enabled {
			e.svc.cardKinds = append(e.svc.cardKinds, domain.KindTask)
		}
		e.svc.cardClaimStale = time.Nanosecond
		before := len(p.Calls())
		title := "T2"
		if _, err := e.svc.UpdateTask(ctx, task, TaskPatch{Title: &title}); err != nil {
			t.Fatal(err)
		}
		c, err := e.svc.Card(ctx, domain.KindTask, task, p.Name())
		if err != nil || c == nil {
			t.Fatalf("card = %v, %v", c, err)
		}
		cards, _ := e.svc.loadCards(ctx)
		v := cards.view(ctx, domain.KindTask, task, cardID{domain.KindMilestone, mid(t, m.ID)})
		if enabled {
			if len(creates(p.Calls())) != 3 || c.Key == "" {
				t.Errorf("enabled: want retry, calls = %v card = %+v", p.Calls(), c)
			}
			continue
		}
		if len(p.Calls()) != before || c.Key != "" {
			t.Errorf("disabled: calls = %v card = %+v", p.Calls(), c)
		}
		if v.Pending != "" || v.LastError != "down" {
			t.Errorf("disabled view = %+v", v)
		}
	}
}

func TestOpenWithTaskManagerCardKinds(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name  string
		kinds []string
		want  int
	}{{"default", nil, 2}, {"empty", []string{}, 0}} {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := config.Defaults()
			if err != nil {
				t.Fatal(err)
			}
			cfg.Embeddings.Provider = config.EmbeddingsNone
			if tc.kinds != nil {
				cfg.TaskManager.CardKinds = tc.kinds
			}
			p := tmfake.New()
			svc, closeFn, err := OpenWithTaskManager(ctx, cfg, t.TempDir(), nil, p)
			if err != nil {
				t.Fatal(err)
			}
			defer closeFn()
			i, _ := svc.CreateInitiative(ctx, InitiativeInput{Title: "I", Brief: "b"})
			m, _ := svc.CreateMilestone(ctx, MilestoneInput{InitiativeID: iid(t, i.ID), Title: "M"})
			if _, err := svc.CreateTask(ctx, TaskInput{MilestoneID: mid(t, m.ID), Title: "T"}); err != nil {
				t.Fatal(err)
			}
			if got := len(creates(p.Calls())); got != tc.want {
				t.Errorf("creates = %d, want %d", got, tc.want)
			}
		})
	}
}
