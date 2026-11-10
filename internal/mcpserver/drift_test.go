package mcpserver

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bengearig/ralph/internal/domain"
	"github.com/bengearig/ralph/internal/drift"
	"github.com/bengearig/ralph/internal/git"
	"github.com/bengearig/ralph/internal/service"
)

const driftBranch = "ralph/oidc-login"

func gitCmd(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func gitCommit(t *testing.T, dir, file, message string) string {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, file), []byte(message+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitCmd(t, dir, "add", "--", file)
	gitCmd(t, dir, "commit", "--quiet", "-m", message)
	return gitCmd(t, dir, "rev-parse", "HEAD")
}

type driftEnv struct {
	root  string
	svc   *service.Service
	mID   int64
	tasks []string
	head  string
}

// newRebasedMilestone records a milestone with two DONE tasks committed on
// its branch, then rebases the branch onto a new commit on main.
func newRebasedMilestone(t *testing.T) *driftEnv {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", home)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	for _, k := range []string{"GIT_AUTHOR_NAME", "GIT_COMMITTER_NAME"} {
		t.Setenv(k, "Ralph Test")
	}
	for _, k := range []string{"GIT_AUTHOR_EMAIL", "GIT_COMMITTER_EMAIL"} {
		t.Setenv(k, "ralph@example.com")
	}

	ctx := context.Background()
	e := &driftEnv{root: t.TempDir(), svc: newService(t)}
	gitCmd(t, e.root, "init", "--quiet", "--initial-branch=main")
	base := gitCommit(t, e.root, "README.md", "initial")
	gitCmd(t, e.root, "switch", "--quiet", "-c", driftBranch)

	i, err := e.svc.CreateInitiative(ctx, service.InitiativeInput{Title: "SSO"})
	if err != nil {
		t.Fatal(err)
	}
	iID, _ := domain.ParseID(domain.KindInitiative, i.ID)
	m, err := e.svc.CreateMilestone(ctx, service.MilestoneInput{InitiativeID: iID, Title: "OIDC login"})
	if err != nil {
		t.Fatal(err)
	}
	e.mID, _ = domain.ParseID(domain.KindMilestone, m.ID)
	for _, title := range []string{"login", "logout"} {
		tv, err := e.svc.CreateTask(ctx, service.TaskInput{MilestoneID: e.mID, Title: title, Description: "do " + title, AcceptanceCriteria: title + " works"})
		if err != nil {
			t.Fatal(err)
		}
		id, _ := domain.ParseID(domain.KindTask, tv.ID)
		for _, st := range []domain.Status{domain.StatusTodo, domain.StatusInProgress, domain.StatusDone} {
			if _, err := e.svc.SetTaskStatus(ctx, id, st, "r"); err != nil {
				t.Fatal(err)
			}
		}
		hash := gitCommit(t, e.root, title+".txt", tv.ID+": "+title+"\n\n"+drift.TrailerKey+": "+tv.ID)
		if err := e.svc.RecordTaskCommit(ctx, id, hash); err != nil {
			t.Fatal(err)
		}
		e.tasks = append(e.tasks, tv.ID)
	}
	e.head = gitCmd(t, e.root, "rev-parse", "HEAD")
	if err := e.svc.RecordMilestoneGit(ctx, e.mID, domain.MilestoneGit{Branch: driftBranch, BaseCommit: base, HeadCommit: e.head}); err != nil {
		t.Fatal(err)
	}

	gitCmd(t, e.root, "switch", "--quiet", "main")
	gitCommit(t, e.root, "upstream.txt", "Upstream change")
	gitCmd(t, e.root, "switch", "--quiet", driftBranch)
	gitCmd(t, e.root, "rebase", "--quiet", "main")
	return e
}

func (e *driftEnv) options() Options {
	return Options{DriftInput: func(_ context.Context, milestoneID int64) (drift.Input, error) {
		return drift.Input{
			Service:        e.svc,
			Repo:           git.Repo{Dir: e.root},
			MilestoneID:    milestoneID,
			BaseBranch:     "main",
			TemplateBranch: driftBranch,
			Subjects:       func(tv service.TaskView) []string { return []string{tv.ID + ": " + tv.Title} },
		}, nil
	}}
}

type milestoneRecord struct {
	HeadCommit string             `json:"head_commit"`
	Tasks      []service.TaskView `json:"tasks"`
}

func TestMilestoneDriftAndReconcile(t *testing.T) {
	e := newRebasedMilestone(t)
	cs := connectService(t, e.svc, e.options())
	tip := gitCmd(t, e.root, "rev-parse", "HEAD")

	var r drift.Report
	mustOK(t, cs, "milestone_drift", map[string]any{"milestone_id": "M-1"}, &r)
	if r.State != drift.StateDiverged || r.Tip != tip || r.Recorded.HeadCommit != e.head {
		t.Fatalf("milestone_drift = %+v, want diverged at %s from %s", r, tip, e.head)
	}
	if len(r.Tasks) != len(e.tasks) {
		t.Fatalf("task findings = %+v, want %v", r.Tasks, e.tasks)
	}
	for i, f := range r.Tasks {
		if f.ID != e.tasks[i] || f.Match != drift.MatchTrailer || f.Commit == "" || f.Commit == f.RecordedCommit {
			t.Errorf("finding %d = %+v, want %s matched by trailer at a new commit", i, f, e.tasks[i])
		}
	}

	var before milestoneRecord
	mustOK(t, cs, "milestone_get", map[string]any{"id": "M-1"}, &before)
	for _, args := range []map[string]any{
		{"milestone_id": "M-1", "reopen": []string{e.tasks[1]}},
		{"milestone_id": "M-1", "reopen": []string{e.tasks[1]}, "accept": true},
	} {
		res := call(t, cs, "milestone_reconcile", args, nil)
		if !res.IsError || !strings.Contains(text(res), "reason") || !strings.Contains(text(res), "nothing was changed") {
			t.Errorf("milestone_reconcile(%v): isError=%v text=%q; want a reason error", args, res.IsError, text(res))
		}
	}
	var unchanged milestoneRecord
	mustOK(t, cs, "milestone_get", map[string]any{"id": "M-1"}, &unchanged)
	if unchanged.HeadCommit != e.head || len(unchanged.Tasks) != len(before.Tasks) {
		t.Fatalf("refused reconcile changed the milestone: %+v", unchanged)
	}
	for i, tv := range unchanged.Tasks {
		if tv.Status != "DONE" || tv.Commit != before.Tasks[i].Commit {
			t.Errorf("refused reconcile changed %s: %+v", tv.ID, tv)
		}
	}

	var out struct {
		Before drift.Report `json:"before"`
		After  drift.Report `json:"after"`
	}
	mustOK(t, cs, "milestone_reconcile", map[string]any{"milestone_id": "M-1", "accept": true}, &out)
	if out.Before.State != drift.StateDiverged || out.After.State != drift.StateUnchanged {
		t.Fatalf("reconcile states = %s → %s, want diverged → unchanged", out.Before.State, out.After.State)
	}
	var after milestoneRecord
	mustOK(t, cs, "milestone_get", map[string]any{"id": "M-1"}, &after)
	if after.HeadCommit != tip {
		t.Errorf("head_commit = %s, want the branch tip %s", after.HeadCommit, tip)
	}
	for i, tv := range after.Tasks {
		if tv.Status != "DONE" || tv.Commit != r.Tasks[i].Commit {
			t.Errorf("%s = %s at %s, want DONE at %s", tv.ID, tv.Status, tv.Commit, r.Tasks[i].Commit)
		}
	}
}

func TestMilestoneReconcileNotCleanIsNotAnError(t *testing.T) {
	e := newRebasedMilestone(t)
	cs := connectService(t, e.svc, e.options())
	var out struct {
		Before drift.Report `json:"before"`
		After  drift.Report `json:"after"`
	}
	mustOK(t, cs, "milestone_reconcile", map[string]any{"milestone_id": "M-1"}, &out)
	if out.Before.State != drift.StateDiverged || out.After.State != drift.StateDiverged {
		t.Errorf("no-action reconcile = %s → %s, want diverged → diverged", out.Before.State, out.After.State)
	}
}

func TestMilestoneReconcileRejectsBadInput(t *testing.T) {
	e := newRebasedMilestone(t)
	cs := connectService(t, e.svc, e.options())
	cases := []struct {
		args     map[string]any
		contains string
	}{
		{map[string]any{"milestone_id": "T-1"}, "not a milestone ID"},
		{map[string]any{"milestone_id": "M-9"}, "milestone_list"},
		{map[string]any{"milestone_id": "M-1", "branch": "  "}, "branch must not be empty"},
		{map[string]any{"milestone_id": "M-1", "branch": "nope", "accept": true}, `branch "nope" does not exist`},
		{map[string]any{"milestone_id": "M-1", "reopen": []string{"M-1"}, "reason": "r"}, "reopen:"},
		{map[string]any{"milestone_id": "M-1", "reopen": []string{"T-99"}, "reason": "r"}, "task_list"},
		{map[string]any{"milestone_id": "M-1", "reason": "r"}, "only used when reopening"},
	}
	for _, c := range cases {
		res := call(t, cs, "milestone_reconcile", c.args, nil)
		if !res.IsError || !strings.Contains(text(res), c.contains) {
			t.Errorf("milestone_reconcile(%v): isError=%v text=%q; want %q", c.args, res.IsError, text(res), c.contains)
		}
	}
	var m milestoneRecord
	mustOK(t, cs, "milestone_get", map[string]any{"id": "M-1"}, &m)
	if m.HeadCommit != e.head {
		t.Errorf("rejected input changed head_commit to %s", m.HeadCommit)
	}
}

func TestDriftToolsUnavailableWithoutDriftInput(t *testing.T) {
	cs := connect(t)
	mustOK(t, cs, "initiative_create", map[string]any{"title": "SSO"}, nil)
	mustOK(t, cs, "milestone_create", map[string]any{"initiative_id": "I-1", "title": "OIDC login"}, nil)
	for _, c := range []struct {
		name string
		args map[string]any
	}{
		{"milestone_drift", map[string]any{"milestone_id": "M-1"}},
		{"milestone_reconcile", map[string]any{"milestone_id": "M-1", "accept": true}},
	} {
		res := call(t, cs, c.name, c.args, nil)
		if !res.IsError || !strings.Contains(text(res), "drift checking is unavailable") {
			t.Errorf("%s: isError=%v text=%q; want unavailable", c.name, res.IsError, text(res))
		}
	}
}

func TestMilestoneGetWaitingOn(t *testing.T) {
	e := newRebasedMilestone(t)
	ctx := context.Background()
	two, err := e.svc.CreateMilestone(ctx, service.MilestoneInput{InitiativeID: 1, Title: "After login", DependsOn: []int64{e.mID}})
	if err != nil {
		t.Fatal(err)
	}
	cs := connectService(t, e.svc, e.options())

	type got struct {
		DependsOn []string `json:"depends_on"`
		WaitingOn []struct {
			ID     string `json:"id"`
			State  string `json:"state"`
			Reason string `json:"reason"`
		} `json:"waiting_on"`
	}
	var g got
	mustOK(t, cs, "milestone_get", map[string]any{"id": two.ID}, &g)
	if len(g.WaitingOn) != 1 || g.WaitingOn[0].ID != "M-1" || g.WaitingOn[0].State != "not_merged" || !strings.Contains(g.WaitingOn[0].Reason, "is not merged into main") {
		t.Fatalf("waiting_on = %+v", g.WaitingOn)
	}

	for name, args := range map[string][2]any{"milestone_list": {"milestone_list", map[string]any{}}, "status": {"status", map[string]any{}}} {
		res := call(t, cs, args[0].(string), args[1].(map[string]any), nil)
		out := text(res)
		if res.IsError || !strings.Contains(out, `"depends_on"`) || strings.Contains(out, "waiting_on") {
			t.Errorf("%s: %s", name, out)
		}
	}

	var bare got
	res := call(t, connectService(t, e.svc, Options{}), "milestone_get", map[string]any{"id": two.ID}, &bare)
	if res.IsError || len(bare.DependsOn) != 1 || strings.Contains(text(res), "waiting_on") {
		t.Fatalf("without drift: %s", text(res))
	}

	gitCmd(t, e.root, "switch", "--quiet", "main")
	gitCmd(t, e.root, "merge", "--quiet", "--ff-only", driftBranch)
	g = got{}
	mustOK(t, cs, "milestone_get", map[string]any{"id": two.ID}, &g)
	if len(g.WaitingOn) != 0 || len(g.DependsOn) != 1 {
		t.Fatalf("after merge: %+v", g)
	}

}
