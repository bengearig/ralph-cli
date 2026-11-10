package service

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/bengearig/ralph/internal/domain"
	"github.com/bengearig/ralph/internal/embed/fake"
	"github.com/bengearig/ralph/internal/search"
	"github.com/bengearig/ralph/internal/store"
	"github.com/bengearig/ralph/internal/store/sqlite"
)

type depCountingStore struct {
	store.Store
	listDeps int
}

func (c *depCountingStore) ListMilestoneDependencies(ctx context.Context, initiativeID int64) (map[int64][]int64, error) {
	c.listDeps++
	return c.Store.ListMilestoneDependencies(ctx, initiativeID)
}

func newDepCountingEnv(t *testing.T) (env, *depCountingStore) {
	t.Helper()
	st, err := sqlite.Open(context.Background(), filepath.Join(t.TempDir(), "r.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	cs := &depCountingStore{Store: st}
	ix := &search.Index{Store: st, Embedder: fake.New(), Chunker: search.Chunker{MaxChars: 1000, OverlapChars: 100}}
	return env{svc: New(cs, ix, Options{StallThreshold: 2})}, cs
}

func (e env) depInitiative(t *testing.T, title string) int64 {
	t.Helper()
	i, err := e.svc.CreateInitiative(context.Background(), InitiativeInput{Title: title, Brief: "b"})
	if err != nil {
		t.Fatal(err)
	}
	return mustID(t, domain.KindInitiative, i.ID)
}

func (e env) depMilestone(t *testing.T, initiative int64, title string, deps ...int64) int64 {
	t.Helper()
	m, err := e.svc.CreateMilestone(context.Background(), MilestoneInput{InitiativeID: initiative, Title: title, DependsOn: deps})
	if err != nil {
		t.Fatal(err)
	}
	return mustID(t, domain.KindMilestone, m.ID)
}

func (e env) dependsOn(t *testing.T, id int64) []string {
	t.Helper()
	v, err := e.svc.GetMilestone(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return v.DependsOn
}

func setDeps(e env, id int64, deps ...int64) error {
	_, err := e.svc.UpdateMilestone(context.Background(), id, MilestonePatch{DependsOn: &deps})
	return err
}

func TestCreateMilestoneWithDependencies(t *testing.T) {
	e := newEnv(t)
	i1, i2 := e.depInitiative(t, "One"), e.depInitiative(t, "Two")
	a, b := e.depMilestone(t, i1, "A"), e.depMilestone(t, i2, "B")
	v, err := e.svc.CreateMilestone(context.Background(), MilestoneInput{InitiativeID: i1, Title: "C", DependsOn: []int64{b, a}})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"M-" + itoa(a), "M-" + itoa(b)}
	if !reflect.DeepEqual(v.DependsOn, want) {
		t.Errorf("created DependsOn = %v; want %v", v.DependsOn, want)
	}
	if got := e.dependsOn(t, mustID(t, domain.KindMilestone, v.ID)); !reflect.DeepEqual(got, want) {
		t.Errorf("stored DependsOn = %v; want %v", got, want)
	}
}

func itoa(n int64) string {
	b, _ := json.Marshal(n)
	return string(b)
}

func TestCreateMilestoneInvalidDependenciesLeavesNothing(t *testing.T) {
	e := newEnv(t)
	i := e.depInitiative(t, "One")
	a := e.depMilestone(t, i, "A")
	before, _ := e.svc.ListMilestones(context.Background(), i, ListOptions{})
	for name, deps := range map[string][]int64{"missing": {9999}, "duplicate": {a, a}} {
		_, err := e.svc.CreateMilestone(context.Background(), MilestoneInput{InitiativeID: i, Title: "New", DependsOn: deps})
		if !errors.Is(err, domain.ErrInvalid) {
			t.Errorf("%s: err = %v; want ErrInvalid", name, err)
		}
	}
	after, _ := e.svc.ListMilestones(context.Background(), i, ListOptions{})
	if len(after) != len(before) {
		t.Errorf("milestones = %d after failed creates; want %d", len(after), len(before))
	}
}

func TestDependencyValidation(t *testing.T) {
	e := newEnv(t)
	i1, i2, i3 := e.depInitiative(t, "One"), e.depInitiative(t, "Two"), e.depInitiative(t, "Three")
	a, b, c := e.depMilestone(t, i1, "A"), e.depMilestone(t, i2, "B"), e.depMilestone(t, i3, "C")
	if err := setDeps(e, a, b); err != nil {
		t.Fatal(err)
	}
	if err := setDeps(e, b, c); err != nil {
		t.Fatal(err)
	}
	ref := func(id int64) string { return "M-" + itoa(id) }

	cases := []struct {
		name string
		id   int64
		deps []int64
		want string
	}{
		{"missing", c, []int64{9999}, "M-9999 does not exist"},
		{"self", c, []int64{c}, "a milestone cannot depend on itself"},
		{"duplicate", c, []int64{a, a}, "more than once"},
		{"direct cycle", b, []int64{a}, ref(b) + " -> " + ref(a) + " -> " + ref(b)},
		{"transitive cycle", c, []int64{a}, ref(c) + " -> " + ref(a) + " -> " + ref(b) + " -> " + ref(c)},
	}
	for _, tc := range cases {
		before := e.dependsOn(t, tc.id)
		err := setDeps(e, tc.id, tc.deps...)
		if !errors.Is(err, domain.ErrInvalid) || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v; want ErrInvalid containing %q", tc.name, err, tc.want)
		}
		if got := e.dependsOn(t, tc.id); !reflect.DeepEqual(got, before) {
			t.Errorf("%s: stored set changed from %v to %v", tc.name, before, got)
		}
	}
	if got := e.dependsOn(t, a); !reflect.DeepEqual(got, []string{ref(b)}) {
		t.Errorf("a depends on %v", got)
	}

	if err := setDeps(e, a, b, c); err != nil {
		t.Errorf("diamond (A to B and C): %v", err)
	}
}

func TestDependencyOnArchivedInitiativeAllowedButNotFrom(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	live, old := e.depInitiative(t, "Live"), e.depInitiative(t, "Old")
	a, b := e.depMilestone(t, live, "A"), e.depMilestone(t, old, "B")
	if _, err := e.svc.ArchiveInitiative(ctx, old); err != nil {
		t.Fatal(err)
	}
	if err := setDeps(e, a, b); err != nil {
		t.Fatalf("dependency on archived initiative's milestone: %v", err)
	}
	if err := setDeps(e, b, a); !errors.Is(err, ErrArchived) {
		t.Errorf("set on archived milestone = %v; want ErrArchived", err)
	}
	if _, err := e.svc.CreateMilestone(ctx, MilestoneInput{InitiativeID: old, Title: "X", DependsOn: []int64{a}}); !errors.Is(err, ErrArchived) {
		t.Errorf("create in archived initiative = %v; want ErrArchived", err)
	}
}

func TestUpdateMilestoneDependsOn(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	i := e.depInitiative(t, "One")
	a, b, c := e.depMilestone(t, i, "A"), e.depMilestone(t, i, "B"), e.depMilestone(t, i, "C")
	ref := func(id int64) string { return "M-" + itoa(id) }

	if err := setDeps(e, a, c, b); err != nil {
		t.Fatal(err)
	}
	title := "Renamed"
	v, err := e.svc.UpdateMilestone(ctx, a, MilestonePatch{Title: &title})
	if err != nil || !reflect.DeepEqual(v.DependsOn, []string{ref(b), ref(c)}) {
		t.Errorf("nil DependsOn = %v, %v; want unchanged", v.DependsOn, err)
	}
	v, err = e.svc.UpdateMilestone(ctx, a, MilestonePatch{DependsOn: &[]int64{c}})
	if err != nil || !reflect.DeepEqual(v.DependsOn, []string{ref(c)}) {
		t.Errorf("replace = %v, %v", v.DependsOn, err)
	}
	v, err = e.svc.UpdateMilestone(ctx, a, MilestonePatch{DependsOn: &[]int64{}})
	if err != nil || len(v.DependsOn) != 0 {
		t.Errorf("clear = %v, %v", v.DependsOn, err)
	}
	if got := e.dependsOn(t, a); len(got) != 0 {
		t.Errorf("stored after clear = %v", got)
	}

	if err := setDeps(e, a, b); err != nil {
		t.Fatal(err)
	}
	bad := "Should not stick"
	_, err = e.svc.UpdateMilestone(ctx, a, MilestonePatch{Title: &bad, DependsOn: &[]int64{a}})
	if !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("self dependency = %v", err)
	}
	if got, _ := e.svc.GetMilestone(ctx, a); got.Title == bad {
		t.Error("failed dependency validation still applied the title")
	}
}

func TestMilestoneViewsCarryDependsOn(t *testing.T) {
	ctx := context.Background()
	e, cs := newDepCountingEnv(t)
	i := e.depInitiative(t, "One")
	a := e.depMilestone(t, i, "A")
	b := e.depMilestone(t, i, "B", a)
	c := e.depMilestone(t, i, "C", a, b)
	d := e.depMilestone(t, i, "D")
	ref := func(id int64) string { return "M-" + itoa(id) }

	cs.listDeps = 0
	list, err := e.svc.ListMilestones(ctx, i, ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if cs.listDeps != 1 {
		t.Errorf("ListMilestones made %d ListMilestoneDependencies calls; want 1", cs.listDeps)
	}
	got := map[string][]string{}
	for _, v := range list {
		got[v.ID] = v.DependsOn
	}
	want := map[string][]string{ref(a): nil, ref(b): {ref(a)}, ref(c): {ref(a), ref(b)}, ref(d): nil}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("list depends_on = %v; want %v", got, want)
	}

	cs.listDeps = 0
	if _, err := e.svc.ListMilestones(ctx, 0, ListOptions{}); err != nil || cs.listDeps != 1 {
		t.Errorf("unscoped list: calls = %d, err = %v", cs.listDeps, err)
	}

	st, err := e.svc.Status(ctx, i, ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range st.Initiatives[0].Milestones {
		if v.ID == ref(c) && !reflect.DeepEqual(v.DependsOn, []string{ref(a), ref(b)}) {
			t.Errorf("status depends_on = %v", v.DependsOn)
		}
	}

	raw, _ := json.Marshal(mustView(t, e, d))
	if strings.Contains(string(raw), "depends_on") {
		t.Errorf("milestone without dependencies marshals depends_on: %s", raw)
	}
	raw, _ = json.Marshal(mustView(t, e, c))
	if !strings.Contains(string(raw), `"depends_on":["`+ref(a)+`","`+ref(b)+`"]`) {
		t.Errorf("milestone with dependencies lacks depends_on: %s", raw)
	}
}

func mustView(t *testing.T, e env, id int64) MilestoneView {
	t.Helper()
	v, err := e.svc.GetMilestone(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestDeleteGuardDependents(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	i1, i2 := e.depInitiative(t, "One"), e.depInitiative(t, "Two")
	a := e.depMilestone(t, i1, "A")
	b := e.depMilestone(t, i1, "B", a)
	c := e.depMilestone(t, i2, "C", a)
	ref := func(id int64) string { return "M-" + itoa(id) }

	_, err := e.svc.DeleteMilestone(ctx, a, false)
	if !errors.Is(err, ErrNeedsForce) || !strings.Contains(err.Error(), ref(b)+" depends on "+ref(a)) ||
		!strings.Contains(err.Error(), ref(c)+" depends on "+ref(a)) {
		t.Fatalf("delete without force = %v", err)
	}
	pv, err := e.svc.PreviewDelete(ctx, domain.KindMilestone, a, true)
	wantDeps := []string{ref(b) + " depends on " + ref(a), ref(c) + " depends on " + ref(a)}
	if err != nil || !reflect.DeepEqual(pv.Dependents, wantDeps) {
		t.Errorf("preview = %+v, %v; want dependents %v", pv, err, wantDeps)
	}
	if _, err := e.svc.PreviewDelete(ctx, domain.KindMilestone, a, false); !errors.Is(err, ErrNeedsForce) {
		t.Errorf("preview without force = %v", err)
	}

	if _, err := e.svc.DeleteInitiative(ctx, i1, true); err != nil {
		t.Fatalf("forced initiative delete: %v", err)
	}
	if got := e.dependsOn(t, c); len(got) != 0 {
		t.Errorf("c depends_on after forced delete = %v", got)
	}
}

func TestDeleteGuardInitiativeAndInsideSet(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	i1, i2 := e.depInitiative(t, "One"), e.depInitiative(t, "Two")
	a := e.depMilestone(t, i1, "A")
	e.depMilestone(t, i1, "B", a)
	c := e.depMilestone(t, i2, "C", a)

	_, err := e.svc.DeleteInitiative(ctx, i1, true)
	if err != nil {
		t.Fatalf("forced delete: %v", err)
	}
	_ = c

	i3, i4 := e.depInitiative(t, "Three"), e.depInitiative(t, "Four")
	x := e.depMilestone(t, i3, "X")
	e.depMilestone(t, i3, "Y", x)
	z := e.depMilestone(t, i4, "Z", x)
	if _, err := e.svc.DeleteInitiative(ctx, i3, true); err != nil {
		t.Fatal(err)
	}
	_ = z

	i5, i6 := e.depInitiative(t, "Five"), e.depInitiative(t, "Six")
	p := e.depMilestone(t, i5, "P")
	q := e.depMilestone(t, i6, "Q", p)
	_, err = e.svc.DeleteInitiative(ctx, i5, false)
	if !errors.Is(err, ErrNeedsForce) || !strings.Contains(err.Error(), "M-"+itoa(q)+" depends on M-"+itoa(p)) {
		t.Errorf("initiative with outside dependent = %v", err)
	}

	i7 := e.depInitiative(t, "Seven")
	r := e.depMilestone(t, i7, "R")
	s := e.depMilestone(t, i7, "S", r)
	_ = s
	pv, err := e.svc.PreviewDelete(ctx, domain.KindInitiative, i7, true)
	if err != nil || len(pv.Dependents) != 0 {
		t.Errorf("dependent inside the deleted set: %+v, %v", pv, err)
	}
	if _, err := e.svc.DeleteMilestone(ctx, s, false); err != nil {
		t.Errorf("deleting the dependent itself: %v", err)
	}
	if _, err := e.svc.DeleteMilestone(ctx, r, false); err != nil {
		t.Errorf("deleting a milestone nothing depends on after its dependent is gone: %v", err)
	}
}
