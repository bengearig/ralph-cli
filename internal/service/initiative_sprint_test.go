package service

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/bengearig/ralph/internal/domain"
)

func TestInitiativeSprintCreate(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	v, err := e.svc.CreateInitiative(ctx, InitiativeInput{Title: "With sprint", Sprint: "  Sprint 42 "})
	if err != nil {
		t.Fatal(err)
	}
	if v.Sprint != "Sprint 42" {
		t.Fatalf("view sprint = %q", v.Sprint)
	}
	b, _ := json.Marshal(v)
	if !strings.Contains(string(b), `"sprint":"Sprint 42"`) {
		t.Fatalf("json = %s", b)
	}
	got, err := e.svc.GetInitiative(ctx, mustID(t, domain.KindInitiative, v.ID))
	if err != nil || got.Sprint != "Sprint 42" {
		t.Fatalf("get = %+v, %v", got, err)
	}

	plain, err := e.svc.CreateInitiative(ctx, InitiativeInput{Title: "Without sprint"})
	if err != nil {
		t.Fatal(err)
	}
	b, _ = json.Marshal(plain)
	if plain.Sprint != "" || strings.Contains(string(b), `"sprint"`) {
		t.Fatalf("sprint = %q, json = %s", plain.Sprint, b)
	}
}

func TestInitiativeSprintUpdate(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	v, err := e.svc.CreateInitiative(ctx, InitiativeInput{Title: "Init", Sprint: "Sprint 42"})
	if err != nil {
		t.Fatal(err)
	}
	id := mustID(t, domain.KindInitiative, v.ID)
	title := "Renamed"
	s43, empty := "Sprint 43", "  "
	for _, c := range []struct {
		name  string
		patch InitiativePatch
		want  string
	}{
		{"nil leaves", InitiativePatch{Title: &title}, "Sprint 42"},
		{"changes", InitiativePatch{Sprint: &s43}, "Sprint 43"},
		{"empty clears", InitiativePatch{Sprint: &empty}, ""},
	} {
		got, err := e.svc.UpdateInitiative(ctx, id, c.patch)
		if err != nil || got.Sprint != c.want {
			t.Fatalf("%s: view = %q, %v", c.name, got.Sprint, err)
		}
		if again, err := e.svc.GetInitiative(ctx, id); err != nil || again.Sprint != c.want {
			t.Fatalf("%s: stored = %q, %v", c.name, again.Sprint, err)
		}
	}
}

func TestInitiativeSprintRejectsInvalid(t *testing.T) {
	for name, bad := range map[string]string{
		"too long": strings.Repeat("é", 256),
		"newline":  "Sprint\n42",
		"tab":      "Sprint\t42",
	} {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			e := newEnv(t)
			_, err := e.svc.CreateInitiative(ctx, InitiativeInput{Title: "Bad", Sprint: bad})
			if !errors.Is(err, domain.ErrInvalid) || !strings.Contains(err.Error(), "sprint") {
				t.Fatalf("create err = %v", err)
			}
			list, err := e.svc.ListInitiatives(ctx, ListOptions{IncludeArchived: true})
			if err != nil || len(list) != 0 {
				t.Fatalf("list = %+v, %v", list, err)
			}

			v, err := e.svc.CreateInitiative(ctx, InitiativeInput{Title: "Good", Sprint: "Sprint 42"})
			if err != nil {
				t.Fatal(err)
			}
			id := mustID(t, domain.KindInitiative, v.ID)
			_, err = e.svc.UpdateInitiative(ctx, id, InitiativePatch{Sprint: &bad})
			if !errors.Is(err, domain.ErrInvalid) || !strings.Contains(err.Error(), "sprint") {
				t.Fatalf("update err = %v", err)
			}
			if got, err := e.svc.GetInitiative(ctx, id); err != nil || got.Sprint != "Sprint 42" {
				t.Fatalf("after refused update = %q, %v", got.Sprint, err)
			}
		})
	}
}

func TestInitiativeSprintAcceptsMaxLength(t *testing.T) {
	e := newEnv(t)
	name := strings.Repeat("é", 255)
	v, err := e.svc.CreateInitiative(context.Background(), InitiativeInput{Title: "Max", Sprint: name})
	if err != nil || v.Sprint != name {
		t.Fatalf("sprint = %q, %v", v.Sprint, err)
	}
}
