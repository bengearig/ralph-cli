package service

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/bengearig/ralph/internal/domain"
)

func TestCreateInitiativeParentCard(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	v, err := e.svc.CreateInitiative(ctx, InitiativeInput{Title: "With parent", ParentCard: " PROJ-12 "})
	if err != nil {
		t.Fatal(err)
	}
	if v.ParentCard != "PROJ-12" {
		t.Fatalf("view parent = %q", v.ParentCard)
	}
	got, err := e.svc.GetInitiative(ctx, mustID(t, domain.KindInitiative, v.ID))
	if err != nil || got.ParentCard != "PROJ-12" {
		t.Fatalf("get = %+v, %v", got, err)
	}
	list, err := e.svc.ListInitiatives(ctx, ListOptions{})
	if err != nil || len(list) != 1 || list[0].ParentCard != "PROJ-12" {
		t.Fatalf("list = %+v, %v", list, err)
	}
	if _, err := e.svc.CreateInitiative(ctx, InitiativeInput{Title: "Underscore", ParentCard: "ABC_D-3"}); err != nil {
		t.Fatal(err)
	}

	plain, err := e.svc.CreateInitiative(ctx, InitiativeInput{Title: "Without parent"})
	if err != nil {
		t.Fatal(err)
	}
	if plain.ParentCard != "" {
		t.Fatalf("parent = %q", plain.ParentCard)
	}
	b, _ := json.Marshal(plain)
	if strings.Contains(string(b), "parent_card") {
		t.Fatalf("json has parent_card: %s", b)
	}
}

func TestCreateInitiativeRejectsBadParentCard(t *testing.T) {
	for _, bad := range []string{"proj-12", "PROJ12", "PROJ-", "-12", "PROJ-1 2", "http://x/browse/PROJ-12"} {
		t.Run(bad, func(t *testing.T) {
			ctx := context.Background()
			e := newEnv(t)
			_, err := e.svc.CreateInitiative(ctx, InitiativeInput{Title: "Bad", ParentCard: bad})
			if !errors.Is(err, domain.ErrInvalid) {
				t.Fatalf("err = %v", err)
			}
			list, err := e.svc.ListInitiatives(ctx, ListOptions{IncludeArchived: true})
			if err != nil || len(list) != 0 {
				t.Fatalf("list = %+v, %v", list, err)
			}
		})
	}
}

func TestUpdateInitiativeParentCard(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	c, err := e.svc.CreateInitiative(ctx, InitiativeInput{Title: "Old"})
	if err != nil {
		t.Fatal(err)
	}
	id := mustID(t, domain.KindInitiative, c.ID)
	str := func(s string) *string { return &s }

	v, err := e.svc.UpdateInitiative(ctx, id, InitiativePatch{ParentCard: str("ABC-7")})
	if err != nil || v.ParentCard != "ABC-7" {
		t.Fatalf("set = %+v, %v", v, err)
	}
	v, err = e.svc.UpdateInitiative(ctx, id, InitiativePatch{Title: str("New")})
	if err != nil || v.ParentCard != "ABC-7" || v.Title != "New" {
		t.Fatalf("nil patch = %+v, %v", v, err)
	}

	_, err = e.svc.UpdateInitiative(ctx, id, InitiativePatch{Title: str("Nope"), ParentCard: str("abc 7")})
	if !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("err = %v", err)
	}
	got, _ := e.svc.GetInitiative(ctx, id)
	if got.Title != "New" || got.ParentCard != "ABC-7" {
		t.Fatalf("changed on invalid: %+v", got)
	}

	for _, clear := range []string{"", "   "} {
		if _, err := e.svc.UpdateInitiative(ctx, id, InitiativePatch{ParentCard: str("ABC-7")}); err != nil {
			t.Fatal(err)
		}
		v, err = e.svc.UpdateInitiative(ctx, id, InitiativePatch{ParentCard: str(clear)})
		if err != nil || v.ParentCard != "" {
			t.Fatalf("clear %q = %+v, %v", clear, v, err)
		}
		got, _ := e.svc.GetInitiative(ctx, id)
		if got.ParentCard != "" {
			t.Fatalf("stored parent = %q", got.ParentCard)
		}
	}
}

func TestUpdateArchivedInitiativeParentCardRefused(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	c, err := e.svc.CreateInitiative(ctx, InitiativeInput{Title: "Arch"})
	if err != nil {
		t.Fatal(err)
	}
	id := mustID(t, domain.KindInitiative, c.ID)
	if _, err := e.svc.ArchiveInitiative(ctx, id, DocumentChoice{}); err != nil {
		t.Fatal(err)
	}
	p := "ABC-7"
	if _, err := e.svc.UpdateInitiative(ctx, id, InitiativePatch{ParentCard: &p}); !errors.Is(err, ErrArchived) {
		t.Fatalf("err = %v", err)
	}
}
