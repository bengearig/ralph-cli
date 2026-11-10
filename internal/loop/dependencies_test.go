package loop

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/bengearig/ralph/internal/domain"
	"github.com/bengearig/ralph/internal/errs"
	"github.com/bengearig/ralph/internal/service"
)

// depMilestone adds a milestone with one task driven to the given statuses.
func (e *runEnv) depMilestone(t *testing.T, title string, to ...domain.Status) int64 {
	t.Helper()
	ctx := context.Background()
	m, err := e.svc.CreateMilestone(ctx, service.MilestoneInput{InitiativeID: 1, Title: title})
	if err != nil {
		t.Fatal(err)
	}
	id, _ := domain.ParseID(domain.KindMilestone, m.ID)
	tv, err := e.svc.CreateTask(ctx, service.TaskInput{MilestoneID: id, Title: title + " task", Description: "d", AcceptanceCriteria: "c"})
	if err != nil {
		t.Fatal(err)
	}
	tid, _ := domain.ParseID(domain.KindTask, tv.ID)
	for _, s := range to {
		if _, err := e.svc.SetTaskStatus(ctx, tid, s, "r"); err != nil {
			t.Fatal(err)
		}
	}
	return id
}

var doneSteps = []domain.Status{domain.StatusTodo, domain.StatusInProgress, domain.StatusDone}

// depBranch gives a DONE milestone a recorded branch with one commit beyond main.
func (e *runEnv) depBranch(t *testing.T, id int64, branch string) {
	t.Helper()
	base := gitCmd(t, e.root, "rev-parse", "main")
	gitCmd(t, e.root, "switch", "--quiet", "-c", branch)
	e.commitFile(t, branch[strings.LastIndex(branch, "/")+1:]+".txt")
	head := gitCmd(t, e.root, "rev-parse", "HEAD")
	gitCmd(t, e.root, "switch", "--quiet", "main")
	if err := e.svc.RecordMilestoneGit(context.Background(), id, domain.MilestoneGit{Branch: branch, BaseCommit: base, HeadCommit: head}); err != nil {
		t.Fatal(err)
	}
}

func (e *runEnv) dependOn(t *testing.T, ids ...int64) {
	t.Helper()
	if _, err := e.svc.UpdateMilestone(context.Background(), e.mID, service.MilestonePatch{DependsOn: &ids}); err != nil {
		t.Fatal(err)
	}
}

func (e *runEnv) check(t *testing.T) []DependencyFinding {
	t.Helper()
	fs, err := DependencyCheck(context.Background(), e.svc, e.cfg, e.root, e.mID)
	if err != nil {
		t.Fatal(err)
	}
	return fs
}

func TestDependencyCheckMatrix(t *testing.T) {
	e := newRunEnv(t, "a")
	cancelled := e.depMilestone(t, "Cancelled", domain.StatusCancelled)
	running := e.depMilestone(t, "Running", domain.StatusTodo, domain.StatusInProgress)
	merged := e.depMilestone(t, "Merged", doneSteps...)
	e.depBranch(t, merged, "ralph/merged")
	gitCmd(t, e.root, "merge", "--quiet", "--ff-only", "ralph/merged")
	ahead := e.depMilestone(t, "Ahead", doneSteps...)
	e.depBranch(t, ahead, "ralph/ahead")
	unrecorded := e.depMilestone(t, "Unrecorded", doneSteps...)
	gitCmd(t, e.root, "branch", "ralph/unrecorded")
	gone := e.depMilestone(t, "Gone", doneSteps...)
	e.depBranch(t, gone, "ralph/gone")
	gitCmd(t, e.root, "branch", "-D", "ralph/gone")
	e.dependOn(t, gone, unrecorded, ahead, merged, running, cancelled)

	fs := e.check(t)
	want := []struct {
		id, state, fix string
		ok             bool
	}{
		{"M-2", DependencyCancelled, "", true},
		{"M-3", DependencyNotDone, "finish it first: ralph loop --milestone M-3", false},
		{"M-4", DependencySatisfied, "", true},
		{"M-5", DependencyNotMerged, "ralph milestone merge M-5", false},
		{"M-6", DependencyUnknown, "reconcile", false},
		{"M-7", DependencyUnknown, "reconcile", false},
	}
	if len(fs) != len(want) {
		t.Fatalf("findings = %+v", fs)
	}
	for i, w := range want {
		f := fs[i]
		if f.ID != w.id || f.State != w.state || f.Satisfied != w.ok || !strings.Contains(f.Fix, w.fix) {
			t.Errorf("finding %d = %+v; want %+v", i, f, w)
		}
	}
	if !strings.Contains(fs[1].Reason, "is IN_PROGRESS, not DONE") || !strings.Contains(fs[4].Fix, "--force") {
		t.Errorf("reason/fix: %+v %+v", fs[1], fs[4])
	}
	if got := fs[3].Message("M-1"); got != "M-1 depends on M-5, which branch ralph/ahead is not merged into main; ralph milestone merge M-5" {
		t.Errorf("message = %q", got)
	}
	if got := Unsatisfied(fs); len(got) != 4 {
		t.Errorf("unsatisfied = %+v", got)
	}
}

func TestDependencyCheckNoDependenciesSkipsGit(t *testing.T) {
	p, mID := setup(t)
	fs, err := DependencyCheck(context.Background(), p.Service, p.Config, p.Root, mID)
	if err != nil || fs == nil || len(fs) != 0 {
		t.Fatalf("findings = %#v, err = %v", fs, err)
	}
	if _, err := p.Plan(context.Background(), mID); err != nil {
		t.Fatalf("Plan without git: %v", err)
	}
}

func TestDependencyCheckNeedsRepository(t *testing.T) {
	p, mID := setup(t)
	t.Setenv("GIT_CEILING_DIRECTORIES", p.Root)
	ctx := context.Background()
	dep, _ := p.Service.CreateMilestone(ctx, service.MilestoneInput{InitiativeID: 1, Title: "Dep"})
	dID, _ := domain.ParseID(domain.KindMilestone, dep.ID)
	tv, _ := p.Service.CreateTask(ctx, service.TaskInput{MilestoneID: dID, Title: "t", Description: "d", AcceptanceCriteria: "c"})
	tid, _ := domain.ParseID(domain.KindTask, tv.ID)
	for _, s := range doneSteps {
		if _, err := p.Service.SetTaskStatus(ctx, tid, s, "r"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := p.Service.UpdateMilestone(ctx, mID, service.MilestonePatch{DependsOn: &[]int64{dID}}); err != nil {
		t.Fatal(err)
	}
	_, err := DependencyCheck(ctx, p.Service, p.Config, p.Root, mID)
	if err == nil || err.Error() != "checking dependencies needs a git repository" {
		t.Fatalf("err = %v", err)
	}
}

func TestPlanDependencies(t *testing.T) {
	e := newRunEnv(t, "a")
	ctx := context.Background()
	ahead := e.depMilestone(t, "Ahead", doneSteps...)
	e.depBranch(t, ahead, "ralph/ahead")
	running := e.depMilestone(t, "Running", domain.StatusTodo, domain.StatusInProgress)
	e.dependOn(t, ahead, running)
	branchBefore := gitCmd(t, e.root, "branch", "--show-current")
	branchesBefore := gitCmd(t, e.root, "branch", "--list")

	planner := Planner{Service: e.svc, Config: e.cfg, Root: e.root}
	_, err := planner.Plan(ctx, e.mID)
	if !errors.Is(err, errs.ErrNeedsHuman) {
		t.Fatalf("err = %v", err)
	}
	for _, want := range []string{"M-1 depends on M-2, which branch ralph/ahead is not merged into main; ralph milestone merge M-2", "M-1 depends on M-3, which is IN_PROGRESS, not DONE"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error lacks %q: %v", want, err)
		}
	}
	if strings.Count(err.Error(), "\n") != 1 {
		t.Errorf("want one message per line: %q", err)
	}
	if got := gitCmd(t, e.root, "branch", "--show-current"); got != branchBefore || gitCmd(t, e.root, "branch", "--list") != branchesBefore {
		t.Errorf("git changed: on %q", got)
	}

	planner.WarnOnly = true
	plan, err := planner.Plan(ctx, e.mID)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Warnings) != 2 || !strings.Contains(plan.Warnings[0], "ralph milestone merge M-2") {
		t.Errorf("warnings = %v", plan.Warnings)
	}

	planner = Planner{Service: e.svc, Config: e.cfg, Root: e.root, Force: true}
	if plan, err = planner.Plan(ctx, e.mID); err != nil || len(plan.Warnings) != 0 {
		t.Errorf("force: %v %v", plan.Warnings, err)
	}

	gitCmd(t, e.root, "merge", "--quiet", "--ff-only", "ralph/ahead")
	e.dependOn(t, ahead)
	if plan, err = (Planner{Service: e.svc, Config: e.cfg, Root: e.root}).Plan(ctx, e.mID); err != nil || len(plan.Warnings) != 0 {
		t.Errorf("satisfied: %v %v", plan.Warnings, err)
	}
}
