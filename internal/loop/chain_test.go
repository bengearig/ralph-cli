package loop

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/bengearig/ralph/internal/domain"
	"github.com/bengearig/ralph/internal/service"
	"github.com/bengearig/ralph/internal/store"
)

func overview(t *testing.T, e *runEnv, force bool) []ChainEntry {
	t.Helper()
	es, err := Overview(context.Background(), e.svc, e.cfg, e.root, 1, force)
	if err != nil {
		t.Fatal(err)
	}
	return es
}

func TestOverviewDispositions(t *testing.T) {
	e := newRunEnv(t)
	// M-1 is EMPTY from newRunEnv.
	cancelled := e.depMilestone(t, "cancelled", domain.StatusCancelled)
	merged := e.depMilestone(t, "merged", doneSteps...)
	e.depBranch(t, merged, "ralph/merged")
	gitCmd(t, e.root, "merge", "--quiet", "--no-ff", "-m", "merge", "ralph/merged")
	unmerged := e.depMilestone(t, "unmerged", doneSteps...)
	e.depBranch(t, unmerged, "ralph/unmerged")
	ready := e.depMilestone(t, "ready", domain.StatusTodo)
	inProgress := e.depMilestone(t, "in progress", domain.StatusTodo, domain.StatusInProgress)
	blocked := e.depMilestone(t, "blocked", domain.StatusTodo, domain.StatusInProgress, domain.StatusBlocked)
	proposed := e.depMilestone(t, "proposed")
	waiting := e.depMilestone(t, "waiting", domain.StatusTodo)
	if _, err := e.svc.UpdateMilestone(context.Background(), waiting, service.MilestonePatch{DependsOn: &[]int64{unmerged}}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.UpdateMilestone(context.Background(), ready, service.MilestonePatch{DependsOn: &[]int64{merged, cancelled}}); err != nil {
		t.Fatal(err)
	}
	_ = []int64{inProgress, blocked, proposed}

	byID := func(es []ChainEntry) map[string]ChainEntry {
		m := map[string]ChainEntry{}
		for i, x := range es {
			if want := "M-" + string(rune('0'+i+1)); x.Milestone != want {
				t.Errorf("entry %d is %s, want %s (position order)", i, x.Milestone, want)
			}
			m[x.Milestone] = x
		}
		return m
	}
	got := byID(overview(t, e, false))
	want := map[string]string{
		"M-1": DispositionNotReady, "M-2": DispositionCancelled, "M-3": DispositionMerged,
		"M-4": DispositionDoneUnmerged, "M-5": DispositionReady, "M-6": DispositionReady,
		"M-7": DispositionBlocked, "M-8": DispositionNotReady, "M-9": DispositionWaiting,
	}
	for id, d := range want {
		if got[id].Disposition != d {
			t.Errorf("%s disposition = %s, want %s (%+v)", id, got[id].Disposition, d, got[id])
		}
	}
	if x := got["M-1"]; x.Reason != "has no TODO tasks" || x.Fix != "/ralph:articulate M-1" {
		t.Errorf("M-1 = %+v", x)
	}
	if x := got["M-8"]; x.Reason != "has PROPOSED tasks" || x.Fix != "/ralph:articulate M-8" {
		t.Errorf("M-8 = %+v", x)
	}
	if x := got["M-4"]; x.Fix != "ralph milestone merge M-4" || x.Reason == "" {
		t.Errorf("M-4 = %+v", x)
	}
	if x := got["M-7"]; x.Reason != "has BLOCKED tasks" || x.Fix != "unblock them, then rerun" {
		t.Errorf("M-7 = %+v", x)
	}
	if x := got["M-9"]; !strings.HasPrefix(x.Reason, "M-9 depends on M-4, which ") || x.Fix != "ralph milestone merge M-4" {
		t.Errorf("M-9 = %+v", x)
	}
	if got["M-5"].Reason != "" || got["M-3"].Status != "DONE" {
		t.Errorf("M-5 = %+v, M-3 = %+v", got["M-5"], got["M-3"])
	}
	if d := byID(overview(t, e, true))["M-9"].Disposition; d != DispositionReady {
		t.Errorf("forced M-9 = %s, want ready", d)
	}
}

func TestOverviewInitiativeErrors(t *testing.T) {
	e := newRunEnv(t)
	ctx := context.Background()
	if _, err := Overview(ctx, e.svc, e.cfg, e.root, 99, false); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("unknown initiative err = %v", err)
	}
	if _, err := e.svc.ArchiveInitiative(ctx, 1, service.DocumentChoice{}); err != nil {
		t.Fatal(err)
	}
	if _, err := Overview(ctx, e.svc, e.cfg, e.root, 1, false); !errors.Is(err, service.ErrArchived) {
		t.Errorf("archived initiative err = %v", err)
	}
}

func TestNextStep(t *testing.T) {
	entry := func(id, d string) ChainEntry {
		return ChainEntry{Milestone: id, Disposition: d, Reason: id + " reason", Fix: id + " fix"}
	}
	tests := []struct {
		name    string
		entries []ChainEntry
		merge   bool
		action  string
		id      string
		reason  string
	}{
		{"empty", nil, false, ActionComplete, "", ""},
		{"all merged or cancelled", []ChainEntry{entry("M-1", "merged"), entry("M-2", "cancelled")}, false, ActionComplete, "", ""},
		{"skips merged and cancelled", []ChainEntry{entry("M-1", "merged"), entry("M-2", "cancelled"), entry("M-3", "ready"), entry("M-4", "ready")}, false, ActionRun, "M-3", ""},
		{"unmerged stops", []ChainEntry{entry("M-1", "done_unmerged"), entry("M-2", "ready")}, false, ActionStop, "M-1", "needs a merge"},
		{"unmerged merges", []ChainEntry{entry("M-1", "done_unmerged"), entry("M-2", "ready")}, true, ActionMerge, "M-1", "M-1 reason"},
		{"waiting", []ChainEntry{entry("M-1", "waiting"), entry("M-2", "ready")}, false, ActionStop, "M-1", "M-1 reason"},
		{"blocked", []ChainEntry{entry("M-1", "merged"), entry("M-2", "blocked")}, true, ActionStop, "M-2", "M-2 reason"},
		{"not ready", []ChainEntry{entry("M-1", "not_ready"), entry("M-2", "ready")}, false, ActionStop, "M-1", "M-1 reason"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := NextStep(tt.entries, tt.merge)
			if s.Action != tt.action {
				t.Fatalf("action = %s, want %s", s.Action, tt.action)
			}
			if tt.id == "" {
				if s.Entry != nil {
					t.Errorf("entry = %+v, want nil", s.Entry)
				}
				return
			}
			if s.Entry.Milestone != tt.id || (tt.reason != "" && s.Entry.Reason != tt.reason) {
				t.Errorf("entry = %+v", s.Entry)
			}
			if tt.reason == "needs a merge" && s.Entry.Fix != "M-1 fix" {
				t.Errorf("fix = %q", s.Entry.Fix)
			}
		})
	}
}
