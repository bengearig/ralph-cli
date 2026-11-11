package pr

import (
	"testing"

	"github.com/bengearig/ralph/internal/config"
	"github.com/bengearig/ralph/internal/service"
)

func TestLinkUnregistered(t *testing.T) {
	if got, ok := Link("no_such_provider", "a", "b", 1); ok || got != "" {
		t.Errorf("Link = %q, %v", got, ok)
	}
}

func TestLinkNilFunc(t *testing.T) {
	RegisterLink("test_nil_link", nil)
	if got, ok := Link("test_nil_link", "a", "b", 1); ok || got != "" {
		t.Errorf("Link = %q, %v", got, ok)
	}
}

func TestRegisterLinkDuplicatePanics(t *testing.T) {
	RegisterLink("test_dup_link", func(string, string, int) string { return "x" })
	defer func() {
		if recover() == nil {
			t.Error("expected panic on duplicate registration")
		}
	}()
	RegisterLink("test_dup_link", func(string, string, int) string { return "y" })
}

func TestLinkRegistered(t *testing.T) {
	RegisterLink("test_ok_link", func(ns, repo string, n int) string { return ns + "/" + repo })
	if got, ok := Link("test_ok_link", "a", "b", 3); !ok || got != "a/b" {
		t.Errorf("Link = %q, %v", got, ok)
	}
}

func TestMilestoneRequestTitle(t *testing.T) {
	tests := []struct {
		name     string
		provider string
		card     *service.CardView
		want     string
	}{
		{"card key", config.TaskManagerJira, &service.CardView{Key: "AIN-3604"}, "[AIN-3604] M-30: Push"},
		{"task manager none", config.TaskManagerNone, &service.CardView{Key: "AIN-3604"}, "M-30: Push"},
		{"no card", config.TaskManagerJira, nil, "M-30: Push"},
		{"pending card", config.TaskManagerJira, &service.CardView{}, "M-30: Push"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := config.Defaults()
			if err != nil {
				t.Fatal(err)
			}
			cfg.TaskManager.Provider = tt.provider
			m := service.MilestoneView{ID: "M-30", Title: "Push", Card: tt.card}
			if got := MilestoneRequest(cfg, service.InitiativeView{ID: "I-1"}, m, nil, "b").Title; got != tt.want {
				t.Errorf("title = %q, want %q", got, tt.want)
			}
		})
	}
}
