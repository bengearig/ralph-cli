package drift

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/bengearig/ralph/internal/domain"
	"github.com/bengearig/ralph/internal/git"
	"github.com/bengearig/ralph/internal/search"
	"github.com/bengearig/ralph/internal/service"
	"github.com/bengearig/ralph/internal/store/sqlite"
)

const branch = "ralph/oidc-login"

type env struct {
	t     *testing.T
	root  string
	svc   *service.Service
	mID   int64
	tasks []int64
	base  string
}

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

// newEnv creates a repository on main and a milestone with a TODO task per
// title, with the milestone branch checked out from main.
func newEnv(t *testing.T, titles ...string) *env {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}

	root := t.TempDir()
	restoreTree(t, root, initialRepo(t))
	e := &env{t: t, root: root}
	e.base = e.rev("HEAD")
	gitCmd(t, root, "switch", "--quiet", "-c", branch)

	ctx := context.Background()
	st, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "ralph.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	e.svc = service.New(st, &search.Index{Store: st, Chunker: search.Chunker{MaxChars: 500}}, service.Options{})

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
	for _, title := range titles {
		tv, err := e.svc.CreateTask(ctx, service.TaskInput{MilestoneID: e.mID, Title: title, Description: "do " + title, AcceptanceCriteria: title + " works"})
		if err != nil {
			t.Fatal(err)
		}
		id, _ := domain.ParseID(domain.KindTask, tv.ID)
		if _, err := e.svc.SetTaskStatus(ctx, id, domain.StatusTodo, ""); err != nil {
			t.Fatal(err)
		}
		e.tasks = append(e.tasks, id)
	}
	return e
}

func (e *env) rev(ref string) string {
	e.t.Helper()
	return gitCmd(e.t, e.root, "rev-parse", ref)
}

// commit writes file and commits it with message, returning the hash.
func (e *env) commit(file, content, message string) string {
	e.t.Helper()
	if err := os.WriteFile(filepath.Join(e.root, file), []byte(content), 0o644); err != nil {
		e.t.Fatal(err)
	}
	gitCmd(e.t, e.root, "add", "--", file)
	gitCmd(e.t, e.root, "commit", "--quiet", "-m", message)
	return e.rev("HEAD")
}

func subject(t service.TaskView) string { return t.ID + ": " + t.Title }

func taskID(id int64) string { return domain.FormatID(domain.KindTask, id) }

// do commits work for the i-th task as the loop would, marks it DONE and
// records the commit.
func (e *env) do(i int, trailer bool) string {
	e.t.Helper()
	ctx := context.Background()
	id := e.tasks[i]
	tv, err := e.svc.GetTask(ctx, id)
	if err != nil {
		e.t.Fatal(err)
	}
	msg := subject(tv)
	if trailer {
		msg += "\n\nRalph-Task: " + tv.ID
	}
	hash := e.commit(strings.ReplaceAll(tv.Title, " ", "-")+".txt", "work for "+tv.Title+"\n", msg)
	e.done(i)
	if err := e.svc.RecordTaskCommit(ctx, id, hash); err != nil {
		e.t.Fatal(err)
	}
	return hash
}

func (e *env) done(i int) {
	e.t.Helper()
	ctx := context.Background()
	if _, err := e.svc.SetTaskStatus(ctx, e.tasks[i], domain.StatusInProgress, ""); err != nil {
		e.t.Fatal(err)
	}
	if _, err := e.svc.SetTaskStatus(ctx, e.tasks[i], domain.StatusDone, "done"); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) record(g domain.MilestoneGit) {
	e.t.Helper()
	if err := e.svc.RecordMilestoneGit(context.Background(), e.mID, g); err != nil {
		e.t.Fatal(err)
	}
}

// recordHere records the branch, the base and the current tip as head.
func (e *env) recordHere() {
	e.t.Helper()
	e.record(domain.MilestoneGit{Branch: branch, BaseCommit: e.base, HeadCommit: e.rev("HEAD")})
}

func (e *env) input() Input {
	return Input{
		Service:        e.svc,
		Repo:           git.Repo{Dir: e.root},
		MilestoneID:    e.mID,
		BaseBranch:     "main",
		TemplateBranch: branch,
		Subjects:       func(t service.TaskView) []string { return []string{subject(t)} },
	}
}

type snapshot struct {
	Milestone service.MilestoneView
	Tasks     []service.TaskView
}

func (e *env) snapshot() snapshot {
	e.t.Helper()
	ctx := context.Background()
	m, err := e.svc.GetMilestone(ctx, e.mID)
	if err != nil {
		e.t.Fatal(err)
	}
	var tasks []service.TaskView
	for _, id := range e.tasks {
		tv, err := e.svc.GetTask(ctx, id)
		if err != nil {
			e.t.Fatal(err)
		}
		tasks = append(tasks, tv)
	}
	return snapshot{Milestone: m, Tasks: tasks}
}

// check runs Check and asserts it changed no record.
func (e *env) check(in Input) Report {
	e.t.Helper()
	before := e.snapshot()
	r, err := Check(context.Background(), in)
	if err != nil {
		e.t.Fatalf("Check: %v", err)
	}
	if after := e.snapshot(); !reflect.DeepEqual(before, after) {
		e.t.Fatalf("Check changed records:\nbefore %+v\nafter  %+v", before, after)
	}
	return r
}

func wantMergedBy(t *testing.T, r Report, by string) {
	t.Helper()
	if r.MergedBy != by {
		t.Errorf("merged_by = %q, want %q", r.MergedBy, by)
	}
}

func wantState(t *testing.T, r Report, s State) {
	t.Helper()
	if r.State != s {
		t.Fatalf("state = %s, want %s; report %+v", r.State, s, r)
	}
}

type want struct {
	match  Match
	commit string
}

func wantTasks(t *testing.T, r Report, ws ...want) {
	t.Helper()
	if len(r.Tasks) != len(ws) {
		t.Fatalf("tasks = %+v, want %d findings", r.Tasks, len(ws))
	}
	for i, w := range ws {
		if f := r.Tasks[i]; f.Match != w.match || f.Commit != w.commit {
			t.Errorf("task %s: match %s commit %q, want %s %q", f.ID, f.Match, f.Commit, w.match, w.commit)
		}
	}
}

func hashes(cs []Commit) []string {
	out := []string{}
	for _, c := range cs {
		out = append(out, c.Hash)
	}
	return out
}

func wantCommits(t *testing.T, what string, got []Commit, want ...string) {
	t.Helper()
	if want == nil {
		want = []string{}
	}
	if h := hashes(got); !reflect.DeepEqual(h, want) {
		t.Errorf("%s = %v, want %v", what, h, want)
	}
}

func TestUnchanged(t *testing.T) {
	t.Parallel()
	e := newEnv(t, "login", "logout")
	c1, c2 := e.do(0, true), e.do(1, true)
	e.recordHere()

	r := e.check(e.input())
	wantState(t, r, StateUnchanged)
	if !r.Clean() {
		t.Error("Clean() = false for unchanged")
	}
	if !r.BranchExists {
		t.Error("branch_exists = false for an existing branch")
	}
	wantTasks(t, r, want{MatchPresent, c1}, want{MatchPresent, c2})
	wantCommits(t, "unattributed", r.UnattributedCommits)
	if r.Milestone != domain.FormatID(domain.KindMilestone, e.mID) || r.Branch != branch || r.BaseRef != "refs/heads/main" {
		t.Errorf("report header = %q %q %q", r.Milestone, r.Branch, r.BaseRef)
	}
	if r.Tip != c2 || r.Recorded != (Recorded{Branch: branch, BaseCommit: e.base, HeadCommit: c2}) {
		t.Errorf("tip %s, recorded %+v", r.Tip, r.Recorded)
	}
	if r.ProposedBase != e.base {
		t.Errorf("proposed base = %s, want recorded base %s", r.ProposedBase, e.base)
	}
	if r.Tasks[0].ID != taskID(e.tasks[0]) || r.Tasks[0].Title != "login" || r.Tasks[0].RecordedCommit != c1 {
		t.Errorf("finding = %+v", r.Tasks[0])
	}
	if s := r.Summary(); !strings.Contains(s, "recorded head "+c2[:12]) {
		t.Errorf("summary = %q", s)
	}
}

func TestReportJSON(t *testing.T) {
	t.Parallel()
	e := newEnv(t, "login")
	e.do(0, true)
	e.recordHere()
	data, err := json.Marshal(e.check(e.input()))
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"milestone", "branch", "base_ref", "recorded", "tip", "branch_exists", "state", "remote_branch_exists", "tasks", "unattributed_commits", "proposed_base", "unstarted"} {
		if _, ok := m[k]; !ok {
			t.Errorf("JSON lacks %q: %s", k, data)
		}
	}
	if m["branch_exists"] != true {
		t.Errorf("branch_exists = %v, want true", m["branch_exists"])
	}
	rec := m["recorded"].(map[string]any)
	for _, k := range []string{"branch", "base_commit", "head_commit"} {
		if _, ok := rec[k]; !ok {
			t.Errorf("recorded lacks %q: %s", k, data)
		}
	}
	task := m["tasks"].([]any)[0].(map[string]any)
	for _, k := range []string{"id", "title", "recorded_commit", "match", "commit"} {
		if _, ok := task[k]; !ok {
			t.Errorf("task finding lacks %q: %s", k, data)
		}
	}
	if _, ok := m["merged_by"]; ok {
		t.Errorf("JSON has merged_by for a non-merged state: %s", data)
	}

	gitCmd(t, e.root, "switch", "--quiet", "main")
	gitCmd(t, e.root, "branch", "--quiet", "-D", branch)
	if data, err = json.Marshal(e.check(e.input())); err != nil {
		t.Fatal(err)
	}
	m = nil
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	if v, ok := m["branch_exists"]; !ok || v != false {
		t.Errorf("branch_exists = %v (present %v), want false: %s", v, ok, data)
	}
}

func TestAhead(t *testing.T) {
	t.Parallel()
	e := newEnv(t, "login", "logout")
	c1, c2 := e.do(0, true), e.do(1, true)
	e.recordHere()
	extra := e.commit("manual.txt", "by hand\n", "Manual tweak")

	r := e.check(e.input())
	wantState(t, r, StateAhead)
	if r.Clean() {
		t.Error("Clean() = true for ahead")
	}
	wantCommits(t, "extra", r.ExtraCommits, extra)
	if r.ExtraCommits[0].Subject != "Manual tweak" {
		t.Errorf("extra subject = %q", r.ExtraCommits[0].Subject)
	}
	wantTasks(t, r, want{MatchPresent, c1}, want{MatchPresent, c2})
	wantCommits(t, "unattributed", r.UnattributedCommits, extra)
	if s := r.Summary(); !strings.Contains(s, "1 commit ahead") {
		t.Errorf("summary = %q", s)
	}
}

func TestBehind(t *testing.T) {
	t.Parallel()
	e := newEnv(t, "login", "logout")
	c1, c2 := e.do(0, true), e.do(1, true)
	e.recordHere()
	gitCmd(t, e.root, "reset", "--quiet", "--hard", "HEAD~1")

	r := e.check(e.input())
	wantState(t, r, StateBehind)
	wantCommits(t, "lost", r.LostCommits, c2)
	if r.LostCommits[0].Subject != subject(e.snapshot().Tasks[1]) {
		t.Errorf("lost subject = %q", r.LostCommits[0].Subject)
	}
	wantTasks(t, r, want{MatchPresent, c1}, want{MatchMissing, ""})
	if s := r.Summary(); !strings.Contains(s, "behind") {
		t.Errorf("summary = %q", s)
	}
}

func TestDivergedByRebase(t *testing.T) {
	t.Parallel()
	for _, trailer := range []bool{true, false} {
		name := map[bool]string{true: "trailer", false: "patch_id"}[trailer]
		t.Run(name, func(t *testing.T) {
			e := newEnv(t, "login", "logout")
			e.do(0, trailer)
			e.do(1, trailer)
			e.recordHere()
			head := e.rev("HEAD")
			gitCmd(t, e.root, "switch", "--quiet", "main")
			e.commit("upstream.txt", "upstream\n", "Upstream change")
			gitCmd(t, e.root, "switch", "--quiet", branch)
			gitCmd(t, e.root, "rebase", "--quiet", "main")
			n1, n2 := e.rev("HEAD~1"), e.rev("HEAD")

			r := e.check(e.input())
			wantState(t, r, StateDiverged)
			m := MatchPatchID
			if trailer {
				m = MatchTrailer
			}
			wantTasks(t, r, want{m, n1}, want{m, n2})
			wantCommits(t, "unattributed", r.UnattributedCommits)
			if s, w := r.Summary(), "has diverged from its recorded head "+head[:12]; !strings.Contains(s, w) {
				t.Errorf("summary = %q, want it to contain %q", s, w)
			}
			// The recorded base is still an ancestor of the rebased tip.
			if r.ProposedBase != e.base {
				t.Errorf("proposed base = %s, want recorded base %s", r.ProposedBase, e.base)
			}
		})
	}
}

func TestDivergedBySquash(t *testing.T) {
	t.Parallel()
	e := newEnv(t, "login", "logout")
	e.do(0, true)
	e.do(1, true)
	e.recordHere()
	t1, t2 := taskID(e.tasks[0]), taskID(e.tasks[1])
	gitCmd(t, e.root, "reset", "--quiet", "--soft", e.base)
	gitCmd(t, e.root, "commit", "--quiet", "-m", "Login and logout\n\nRalph-Task: "+t1+"\nRalph-Task: "+t2)
	sq := e.rev("HEAD")

	r := e.check(e.input())
	wantState(t, r, StateDiverged)
	wantTasks(t, r, want{MatchTrailer, sq}, want{MatchTrailer, sq})
	wantCommits(t, "unattributed", r.UnattributedCommits)
}

func TestTrailerNewestWins(t *testing.T) {
	t.Parallel()
	e := newEnv(t, "login")
	e.do(0, true)
	fix := e.commit("fix.txt", "fix\n", "Fix login\n\nRalph-Task: "+taskID(e.tasks[0]))
	e.record(domain.MilestoneGit{Branch: branch, BaseCommit: e.base, HeadCommit: fix})
	if err := e.svc.RecordTaskCommit(context.Background(), e.tasks[0], ""); err != nil {
		t.Fatal(err)
	}

	r := e.check(e.input())
	wantTasks(t, r, want{MatchTrailer, fix})
	wantCommits(t, "unattributed", r.UnattributedCommits, e.rev("HEAD~1"))
}

func TestCardTrailerDoesNotHideOrImitateTaskTrailer(t *testing.T) {
	t.Parallel()
	e := newEnv(t, "login", "logout")
	e.do(0, true)
	e.do(1, false)
	e.recordHere()
	t0, t1 := taskID(e.tasks[0]), taskID(e.tasks[1])
	gitCmd(t, e.root, "reset", "--quiet", "--hard", e.base)
	both := e.commit("a.txt", "a\n", "Reworded\n\nRalph-Task: "+t0+"\nCard: ABC-1235")
	e.commit("b.txt", "b\n", "Other work\n\nCard: "+t1)

	r := e.check(e.input())
	wantTasks(t, r, want{MatchTrailer, both}, want{MatchMissing, ""})
}

func TestSubjectMatch(t *testing.T) {
	t.Parallel()
	e := newEnv(t, "login", "logout")
	e.do(0, false)
	e.do(1, false)
	e.recordHere()
	gitCmd(t, e.root, "reset", "--quiet", "--hard", e.base)
	redone := e.commit("other.txt", "different work\n", subject(e.snapshot().Tasks[0]))

	r := e.check(e.input())
	wantState(t, r, StateDiverged)
	wantTasks(t, r, want{MatchSubject, redone}, want{MatchMissing, ""})
}

func TestMissing(t *testing.T) {
	t.Parallel()
	e := newEnv(t, "login", "logout")
	e.do(0, false)
	e.do(1, false)
	e.recordHere()
	gitCmd(t, e.root, "reset", "--quiet", "--hard", e.base)
	unrelated := e.commit("unrelated.txt", "something else\n", "Unrelated work")

	r := e.check(e.input())
	wantState(t, r, StateDiverged)
	wantTasks(t, r, want{MatchMissing, ""}, want{MatchMissing, ""})
	wantCommits(t, "unattributed", r.UnattributedCommits, unrelated)
	if r.UnattributedCommits[0].Subject != "Unrelated work" {
		t.Errorf("unattributed subject = %q", r.UnattributedCommits[0].Subject)
	}
}

func TestMerged(t *testing.T) {
	t.Parallel()
	e := newEnv(t, "login")
	c1 := e.do(0, true)
	e.recordHere()
	gitCmd(t, e.root, "switch", "--quiet", "main")
	gitCmd(t, e.root, "merge", "--quiet", "--no-ff", "-m", "Merge", branch)

	r := e.check(e.input())
	wantState(t, r, StateMerged)
	wantMergedBy(t, r, MergedByAncestry)
	wantTasks(t, r, want{MatchPresent, c1})
	if !r.BranchExists {
		t.Error("branch_exists = false for an existing branch")
	}
	if s := r.Summary(); !strings.Contains(s, "already merged into main") || strings.Contains(s, "branch deleted") {
		t.Errorf("summary = %q", s)
	}
}

func TestMergedBranchDeleted(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"fast-forward", "no-ff"} {
		t.Run(mode, func(t *testing.T) {
			e := newEnv(t, "login", "logout")
			c1, c2 := e.do(0, true), e.do(1, true)
			e.recordHere()
			gitCmd(t, e.root, "switch", "--quiet", "main")
			if mode == "no-ff" {
				gitCmd(t, e.root, "merge", "--quiet", "--no-ff", "-m", "Merge", branch)
			} else {
				gitCmd(t, e.root, "merge", "--quiet", "--ff-only", branch)
			}
			gitCmd(t, e.root, "branch", "--quiet", "-D", branch)

			r := e.check(e.input())
			wantState(t, r, StateMerged)
			if r.BranchExists || r.Tip != "" || r.ProposedBase != "" {
				t.Errorf("branch_exists %v, tip %q, proposed base %q", r.BranchExists, r.Tip, r.ProposedBase)
			}
			wantTasks(t, r, want{MatchPresent, c1}, want{MatchPresent, c2})
			if s := r.Summary(); !strings.Contains(s, "already merged into main (branch deleted)") {
				t.Errorf("summary = %q", s)
			}
		})
	}
}

func TestMergedBranchDeletedWithOrigin(t *testing.T) {
	t.Parallel()
	e := newEnv(t, "login")
	e.do(0, true)
	e.recordHere()
	remote := t.TempDir()
	gitCmd(t, remote, "init", "--quiet", "--bare")
	gitCmd(t, e.root, "remote", "add", "origin", remote)
	gitCmd(t, e.root, "push", "--quiet", "origin", branch)
	gitCmd(t, e.root, "fetch", "--quiet", "origin")
	gitCmd(t, e.root, "switch", "--quiet", "main")
	gitCmd(t, e.root, "merge", "--quiet", "--no-ff", "-m", "Merge", branch)
	gitCmd(t, e.root, "branch", "--quiet", "-D", branch)

	r := e.check(e.input())
	wantState(t, r, StateMerged)
	wantMergedBy(t, r, MergedByAncestry)
	if !r.RemoteBranchExists {
		t.Error("remote_branch_exists = false with origin/" + branch)
	}
}

func TestBranchDeletedNotMerged(t *testing.T) {
	t.Parallel()
	t.Run("head equals base", func(t *testing.T) {
		e := newEnv(t)
		e.recordHere()
		gitCmd(t, e.root, "switch", "--quiet", "main")
		gitCmd(t, e.root, "branch", "--quiet", "-D", branch)
		r := e.check(e.input())
		wantState(t, r, StateBranchMissing)
		if r.BranchExists {
			t.Error("branch_exists = true")
		}
	})
	t.Run("head object missing", func(t *testing.T) {
		e := newEnv(t, "login")
		e.do(0, true)
		e.record(domain.MilestoneGit{Branch: branch, BaseCommit: e.base, HeadCommit: strings.Repeat("a", 40)})
		gitCmd(t, e.root, "switch", "--quiet", "main")
		gitCmd(t, e.root, "branch", "--quiet", "-D", branch)
		r := e.check(e.input())
		wantState(t, r, StateBranchMissing)
		wantTasks(t, r, want{MatchMissing, ""})
	})
	t.Run("empty head", func(t *testing.T) {
		e := newEnv(t, "login")
		e.do(0, true)
		gitCmd(t, e.root, "switch", "--quiet", "main")
		gitCmd(t, e.root, "branch", "--quiet", "-D", branch)
		r := e.check(e.input())
		wantState(t, r, StateBranchMissing)
		wantTasks(t, r, want{MatchMissing, ""})
	})
}

func TestFreshBranchNotMerged(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.recordHere()

	r := e.check(e.input())
	wantState(t, r, StateUnchanged)
	wantTasks(t, r)

	// Without a recorded base, the proposed base stands in for it.
	e.record(domain.MilestoneGit{Branch: branch, HeadCommit: e.base})
	wantState(t, e.check(e.input()), StateUnchanged)
}

func TestBranchMissing(t *testing.T) {
	t.Parallel()
	e := newEnv(t, "login")
	e.do(0, true)
	e.recordHere()
	gitCmd(t, e.root, "switch", "--quiet", "main")
	gitCmd(t, e.root, "branch", "--quiet", "-D", branch)

	r := e.check(e.input())
	wantState(t, r, StateBranchMissing)
	if r.BranchExists {
		t.Error("branch_exists = true for a deleted branch")
	}
	wantTasks(t, r, want{MatchMissing, ""})
	if r.RemoteBranchExists {
		t.Error("remote_branch_exists = true without a remote")
	}
	if r.Tip != "" || r.ProposedBase != "" {
		t.Errorf("tip %q, proposed base %q, want both empty", r.Tip, r.ProposedBase)
	}
	if s := r.Summary(); !strings.Contains(s, "does not exist") || strings.Contains(s, "origin/") {
		t.Errorf("summary = %q", s)
	}

	remote := t.TempDir()
	gitCmd(t, remote, "init", "--quiet", "--bare")
	gitCmd(t, e.root, "remote", "add", "origin", remote)
	gitCmd(t, e.root, "push", "--quiet", "origin", e.rev("HEAD")+":refs/heads/"+branch)
	gitCmd(t, e.root, "fetch", "--quiet", "origin")

	r = e.check(e.input())
	wantState(t, r, StateBranchMissing)
	if !r.RemoteBranchExists {
		t.Error("remote_branch_exists = false with origin/" + branch)
	}
	if s := r.Summary(); !strings.Contains(s, "origin/"+branch+" exists") {
		t.Errorf("summary = %q", s)
	}
}

func TestBaseRefFallsBackToOrigin(t *testing.T) {
	t.Parallel()
	e := newEnv(t, "login")
	e.do(0, true)
	e.recordHere()
	remote := t.TempDir()
	gitCmd(t, remote, "init", "--quiet", "--bare")
	gitCmd(t, e.root, "remote", "add", "origin", remote)
	gitCmd(t, e.root, "push", "--quiet", "origin", "main")
	gitCmd(t, e.root, "fetch", "--quiet", "origin")
	gitCmd(t, e.root, "branch", "--quiet", "-D", "main")

	r := e.check(e.input())
	if r.BaseRef != "refs/remotes/origin/main" {
		t.Errorf("base ref = %q", r.BaseRef)
	}
	wantState(t, r, StateUnchanged)
}

func TestUnrecorded(t *testing.T) {
	t.Parallel()
	e := newEnv(t, "login", "logout")
	tv := e.snapshot().Tasks
	c1 := e.commit("login.txt", "login\n", "Login\n\nRalph-Task: "+tv[0].ID)
	c2 := e.commit("logout.txt", "logout\n", subject(tv[1]))
	e.done(0)
	e.done(1)
	gitCmd(t, e.root, "switch", "--quiet", "main")
	e.commit("upstream.txt", "upstream\n", "Upstream change")
	gitCmd(t, e.root, "switch", "--quiet", branch)

	r := e.check(e.input())
	wantState(t, r, StateUnrecorded)
	if r.Branch != branch || r.Recorded != (Recorded{}) {
		t.Errorf("branch %q, recorded %+v", r.Branch, r.Recorded)
	}
	mb := gitCmd(t, e.root, "merge-base", c2, "main")
	if mb != e.base || r.ProposedBase != mb {
		t.Errorf("proposed base = %s, want merge-base %s", r.ProposedBase, mb)
	}
	wantTasks(t, r, want{MatchTrailer, c1}, want{MatchSubject, c2})
	wantCommits(t, "unattributed", r.UnattributedCommits)
}

func TestGarbageCollectedHead(t *testing.T) {
	t.Parallel()
	e := newEnv(t, "login")
	c1 := e.do(0, true)
	e.record(domain.MilestoneGit{Branch: branch, BaseCommit: e.base, HeadCommit: strings.Repeat("0123456789", 4)})

	r := e.check(e.input())
	wantState(t, r, StateDiverged)
	wantTasks(t, r, want{MatchPresent, c1})
}

func TestMissingRecordedCommitObject(t *testing.T) {
	t.Parallel()
	e := newEnv(t, "login")
	e.do(0, false)
	e.recordHere()
	gone := strings.Repeat("abcdef0123", 4)
	if err := e.svc.RecordTaskCommit(context.Background(), e.tasks[0], gone); err != nil {
		t.Fatal(err)
	}

	r := e.check(e.input())
	wantTasks(t, r, want{MatchSubject, e.rev("HEAD")})
}

func TestMissingBaseBranch(t *testing.T) {
	t.Parallel()
	e := newEnv(t, "login")
	e.recordHere()
	for _, base := range []string{"nope", ""} {
		in := e.input()
		in.BaseBranch = base
		before := e.snapshot()
		_, err := Check(context.Background(), in)
		if err == nil || !strings.Contains(err.Error(), "pr.base_branch") {
			t.Errorf("base %q: err = %v, want it to mention pr.base_branch", base, err)
		}
		if after := e.snapshot(); !reflect.DeepEqual(before, after) {
			t.Error("Check changed records")
		}
	}
}

func TestOnlyDoneTasks(t *testing.T) {
	t.Parallel()
	e := newEnv(t, "login", "logout")
	c1 := e.do(0, true)
	e.recordHere()

	r := e.check(e.input())
	wantTasks(t, r, want{MatchPresent, c1})
}

func (e *env) apply(a Actions) (Report, Report) {
	e.t.Helper()
	before, after, err := Apply(context.Background(), e.input(), a)
	if err != nil {
		e.t.Fatalf("Apply(%+v): %v", a, err)
	}
	return before, after
}

// wantRefused asserts Apply fails with domain.ErrInvalid and changes nothing.
func (e *env) wantRefused(a Actions, contains string) {
	e.t.Helper()
	before := e.snapshot()
	_, _, err := Apply(context.Background(), e.input(), a)
	if err == nil || !strings.Contains(err.Error(), contains) {
		e.t.Errorf("Apply(%+v): err = %v, want it to contain %q", a, err, contains)
	}
	if after := e.snapshot(); !reflect.DeepEqual(before, after) {
		e.t.Errorf("Apply(%+v) changed records:\nbefore %+v\nafter  %+v", a, before, after)
	}
}

func (e *env) wantRecord(g domain.MilestoneGit) {
	e.t.Helper()
	m := e.snapshot().Milestone
	if got := (domain.MilestoneGit{Branch: m.Branch, BaseCommit: m.BaseCommit, HeadCommit: m.HeadCommit}); got != g {
		e.t.Errorf("record = %+v, want %+v", got, g)
	}
}

func (e *env) wantTask(i int, status, commit string) service.TaskView {
	e.t.Helper()
	tv := e.snapshot().Tasks[i]
	if tv.Status != status || tv.Commit != commit {
		e.t.Errorf("%s: status %s commit %q, want %s %q", tv.ID, tv.Status, tv.Commit, status, commit)
	}
	return tv
}

func noteBodies(tv service.TaskView) []string {
	out := []string{}
	for _, n := range tv.Notes {
		out = append(out, n.Kind+": "+n.Body)
	}
	return out
}

func TestApplyNoActions(t *testing.T) {
	t.Parallel()
	e := newEnv(t, "login")
	e.do(0, true)
	e.recordHere()
	e.commit("manual.txt", "by hand\n", "Manual tweak")
	snap := e.snapshot()

	before, after := e.apply(Actions{})
	wantState(t, before, StateAhead)
	if !reflect.DeepEqual(before, after) {
		t.Errorf("after = %+v, want before %+v", after, before)
	}
	if got := e.snapshot(); !reflect.DeepEqual(snap, got) {
		t.Error("Apply without actions changed records")
	}
}

func TestApplyAcceptDiverged(t *testing.T) {
	t.Parallel()
	e := newEnv(t, "login", "logout")
	e.do(0, true)
	e.do(1, true)
	e.recordHere()
	gitCmd(t, e.root, "switch", "--quiet", "main")
	e.commit("upstream.txt", "upstream\n", "Upstream change")
	gitCmd(t, e.root, "switch", "--quiet", branch)
	gitCmd(t, e.root, "rebase", "--quiet", "main")
	n1, n2 := e.rev("HEAD~1"), e.rev("HEAD")

	before, after := e.apply(Actions{Accept: true})
	wantState(t, before, StateDiverged)
	wantState(t, after, StateUnchanged)
	e.wantRecord(domain.MilestoneGit{Branch: branch, BaseCommit: e.base, HeadCommit: n2})
	e.wantTask(0, "DONE", n1)
	e.wantTask(1, "DONE", n2)
	wantTasks(t, after, want{MatchPresent, n1}, want{MatchPresent, n2})
}

func TestApplyAcceptKeepsMissingTaskDone(t *testing.T) {
	t.Parallel()
	e := newEnv(t, "login", "logout")
	c1 := e.do(0, true)
	e.do(1, true)
	e.recordHere()
	gitCmd(t, e.root, "reset", "--quiet", "--hard", "HEAD~1")

	before, after := e.apply(Actions{Accept: true})
	wantState(t, before, StateBehind)
	wantState(t, after, StateUnchanged)
	e.wantRecord(domain.MilestoneGit{Branch: branch, BaseCommit: e.base, HeadCommit: c1})
	e.wantTask(0, "DONE", c1)
	tv := e.wantTask(1, "DONE", "")
	note := "NOTE: reconcile: no commit found on " + branch + " at " + c1[:12] + "; kept DONE"
	if notes := noteBodies(tv); notes[len(notes)-1] != note {
		t.Errorf("notes = %q, want last %q", notes, note)
	}
	wantTasks(t, after, want{MatchPresent, c1}, want{MatchMissing, ""})
}

func TestApplyReopenAndAccept(t *testing.T) {
	t.Parallel()
	e := newEnv(t, "login", "logout")
	c1 := e.do(0, true)
	e.do(1, true)
	e.recordHere()
	gitCmd(t, e.root, "reset", "--quiet", "--hard", "HEAD~1")

	_, after := e.apply(Actions{Reopen: []int64{e.tasks[1]}, Reason: "lost in a reset", Accept: true})
	wantState(t, after, StateUnchanged)
	e.wantRecord(domain.MilestoneGit{Branch: branch, BaseCommit: e.base, HeadCommit: c1})
	e.wantTask(0, "DONE", c1)
	tv := e.wantTask(1, "TODO", "")
	notes := noteBodies(tv)
	if last := notes[len(notes)-1]; last != "NOTE: Reopened by reconcile: lost in a reset" {
		t.Errorf("notes = %q", notes)
	}
	for _, n := range notes {
		if strings.Contains(n, "kept DONE") {
			t.Errorf("reopened task got the kept-DONE note: %q", notes)
		}
	}
	wantTasks(t, after, want{MatchPresent, c1})
}

func TestApplyRejectsInvalidActions(t *testing.T) {
	t.Parallel()
	e := newEnv(t, "login", "logout", "logs")
	e.do(0, true)
	e.do(1, true)
	e.recordHere()
	gitCmd(t, e.root, "reset", "--quiet", "--hard", "HEAD~1")

	ctx := context.Background()
	m := e.snapshot().Milestone
	iID, _ := domain.ParseID(domain.KindInitiative, m.InitiativeID)
	other, err := e.svc.CreateMilestone(ctx, service.MilestoneInput{InitiativeID: iID, Title: "Other"})
	if err != nil {
		t.Fatal(err)
	}
	otherID, _ := domain.ParseID(domain.KindMilestone, other.ID)
	ot, err := e.svc.CreateTask(ctx, service.TaskInput{MilestoneID: otherID, Title: "elsewhere", Description: "d", AcceptanceCriteria: "c"})
	if err != nil {
		t.Fatal(err)
	}
	otID, _ := domain.ParseID(domain.KindTask, ot.ID)
	for _, st := range []domain.Status{domain.StatusTodo, domain.StatusInProgress, domain.StatusDone} {
		if _, err := e.svc.SetTaskStatus(ctx, otID, st, "r"); err != nil {
			t.Fatal(err)
		}
	}
	otBefore, _ := e.svc.GetTask(ctx, otID)

	lost := e.tasks[1]
	for _, accept := range []bool{false, true} {
		e.wantRefused(Actions{Reopen: []int64{lost}, Accept: accept}, "reason")
		e.wantRefused(Actions{Reopen: []int64{lost}, Reason: "  ", Accept: accept}, "reason")
		e.wantRefused(Actions{Reopen: []int64{lost, e.tasks[2]}, Reason: "r", Accept: accept}, "is TODO")
		e.wantRefused(Actions{Reopen: []int64{lost, otID}, Reason: "r", Accept: accept}, "belongs to "+other.ID)
		e.wantRefused(Actions{Reopen: []int64{lost, 999}, Reason: "r", Accept: accept}, "T-999")
		e.wantRefused(Actions{Reason: "r", Accept: accept}, "only used when reopening")
		e.wantRefused(Actions{Branch: "nope", Reopen: []int64{lost}, Reason: "r", Accept: accept}, `branch "nope" does not exist`)
	}
	if otAfter, _ := e.svc.GetTask(ctx, otID); !reflect.DeepEqual(otBefore, otAfter) {
		t.Errorf("other milestone's task changed: %+v", otAfter)
	}
	_, _, err = Apply(ctx, e.input(), Actions{Reopen: []int64{lost}})
	if !errors.Is(err, domain.ErrInvalid) {
		t.Errorf("err = %v, want domain.ErrInvalid", err)
	}
}

func TestApplyRefusesAcceptOnMergedAndBranchMissing(t *testing.T) {
	t.Parallel()
	t.Run("merged", func(t *testing.T) {
		e := newEnv(t, "login")
		e.do(0, true)
		e.recordHere()
		gitCmd(t, e.root, "switch", "--quiet", "main")
		gitCmd(t, e.root, "merge", "--quiet", "--no-ff", "-m", "Merge", branch)
		e.wantRefused(Actions{Accept: true}, "already merged")
		e.wantRefused(Actions{Reopen: []int64{e.tasks[0]}, Reason: "r", Accept: true}, "already merged")
	})
	t.Run("merged and deleted", func(t *testing.T) {
		e := newEnv(t, "login")
		e.do(0, true)
		e.recordHere()
		gitCmd(t, e.root, "switch", "--quiet", "main")
		gitCmd(t, e.root, "merge", "--quiet", "--no-ff", "-m", "Merge", branch)
		gitCmd(t, e.root, "branch", "--quiet", "-D", branch)
		e.wantRefused(Actions{Accept: true}, "branch deleted")
	})
	t.Run("branch_missing", func(t *testing.T) {
		e := newEnv(t, "login")
		e.do(0, true)
		e.recordHere()
		gitCmd(t, e.root, "switch", "--quiet", "main")
		gitCmd(t, e.root, "branch", "--quiet", "-D", branch)
		e.wantRefused(Actions{Accept: true}, "--branch first")
		e.wantRefused(Actions{Reopen: []int64{e.tasks[0]}, Reason: "r", Accept: true}, "--branch first")
	})
}

func TestApplyBranchThenAccept(t *testing.T) {
	t.Parallel()
	e := newEnv(t, "login")
	c1 := e.do(0, true)
	e.recordHere()
	gitCmd(t, e.root, "branch", "--quiet", "-m", "other")
	extra := e.commit("manual.txt", "by hand\n", "Manual tweak")

	before, after := e.apply(Actions{Branch: "other"})
	wantState(t, before, StateBranchMissing)
	wantState(t, after, StateAhead)
	e.wantRecord(domain.MilestoneGit{Branch: "other", BaseCommit: e.base, HeadCommit: c1})
	if after.Branch != "other" {
		t.Errorf("after branch = %q", after.Branch)
	}

	_, after = e.apply(Actions{Accept: true})
	wantState(t, after, StateUnchanged)
	e.wantRecord(domain.MilestoneGit{Branch: "other", BaseCommit: e.base, HeadCommit: extra})
	e.wantTask(0, "DONE", c1)
}

func TestApplyBranchAndAcceptTogether(t *testing.T) {
	t.Parallel()
	e := newEnv(t, "login")
	c1 := e.do(0, true)
	e.recordHere()
	gitCmd(t, e.root, "branch", "--quiet", "-m", "other")
	extra := e.commit("manual.txt", "by hand\n", "Manual tweak")

	before, after := e.apply(Actions{Branch: "other", Accept: true})
	wantState(t, before, StateBranchMissing)
	wantState(t, after, StateUnchanged)
	e.wantRecord(domain.MilestoneGit{Branch: "other", BaseCommit: e.base, HeadCommit: extra})
	e.wantTask(0, "DONE", c1)
}

func TestApplyBackfill(t *testing.T) {
	t.Parallel()
	e := newEnv(t, "login", "logout")
	tv := e.snapshot().Tasks
	c1 := e.commit("login.txt", "login\n", "Login\n\nRalph-Task: "+tv[0].ID)
	c2 := e.commit("logout.txt", "logout\n", subject(tv[1]))
	e.done(0)
	e.done(1)
	gitCmd(t, e.root, "switch", "--quiet", "main")
	e.commit("upstream.txt", "upstream\n", "Upstream change")
	gitCmd(t, e.root, "switch", "--quiet", branch)

	before, after := e.apply(Actions{Accept: true})
	wantState(t, before, StateUnrecorded)
	wantState(t, after, StateUnchanged)
	e.wantRecord(domain.MilestoneGit{Branch: branch, BaseCommit: e.base, HeadCommit: c2})
	e.wantTask(0, "DONE", c1)
	e.wantTask(1, "DONE", c2)
	wantTasks(t, after, want{MatchPresent, c1}, want{MatchPresent, c2})
}

func TestApplyRefusedWhenArchived(t *testing.T) {
	t.Parallel()
	e := newEnv(t, "login")
	e.do(0, true)
	e.recordHere()
	ctx := context.Background()
	m, err := e.svc.GetMilestone(ctx, e.mID)
	if err != nil {
		t.Fatal(err)
	}
	iID, _ := domain.ParseID(domain.KindInitiative, m.InitiativeID)
	if _, err := e.svc.ArchiveInitiative(ctx, iID, service.DocumentChoice{}); err != nil {
		t.Fatal(err)
	}
	before := e.snapshot()
	for _, a := range []Actions{{Accept: true}, {Branch: branch}, {Reopen: []int64{e.tasks[0]}, Reason: "lost"}} {
		_, _, err := Apply(ctx, e.input(), a)
		if !errors.Is(err, service.ErrArchived) {
			t.Errorf("Apply(%+v): err = %v, want ErrArchived", a, err)
		}
	}
	if after := e.snapshot(); !reflect.DeepEqual(before, after) {
		t.Errorf("Apply changed records:\nbefore %+v\nafter  %+v", before, after)
	}
}

func TestSubjectCandidates(t *testing.T) {
	t.Parallel()
	e := newEnv(t, "login", "logout")
	e.do(0, false)
	e.do(1, false)
	e.recordHere()
	keyed := func(tv service.TaskView) string { return "[ABC-1] " + subject(tv) }
	in := e.input()
	in.Subjects = func(tv service.TaskView) []string { return []string{keyed(tv), "", subject(tv)} }

	r := e.check(in)
	wantState(t, r, StateUnchanged)
	wantTasks(t, r, want{MatchPresent, e.rev("HEAD~1")}, want{MatchPresent, e.rev("HEAD")})

	gitCmd(t, e.root, "reset", "--quiet", "--hard", e.base)
	first := e.commit("a.txt", "a\n", keyed(e.snapshot().Tasks[0]))
	second := e.commit("b.txt", "b\n", subject(e.snapshot().Tasks[1]))
	e.commit("c.txt", "c\n", "Unrelated work")
	r = e.check(in)
	wantTasks(t, r, want{MatchSubject, first}, want{MatchSubject, second})

	gitCmd(t, e.root, "reset", "--quiet", "--hard", second)
	gitCmd(t, e.root, "commit", "--quiet", "--amend", "-m", "Unrelated\n\nRalph-Task: "+e.snapshot().Tasks[1].ID)
	r = e.check(in)
	wantTasks(t, r, want{MatchSubject, first}, want{MatchTrailer, e.rev("HEAD")})

	in.Subjects = func(service.TaskView) []string { return []string{"", "nothing like it"} }
	r = e.check(in)
	wantTasks(t, r, want{MatchMissing, ""}, want{MatchTrailer, e.rev("HEAD")})
}

func (e *env) doneViews() []service.TaskView {
	e.t.Helper()
	var out []service.TaskView
	for _, id := range e.tasks {
		tv, err := e.svc.GetTask(context.Background(), id)
		if err != nil {
			e.t.Fatal(err)
		}
		if tv.Status == string(domain.StatusDone) {
			out = append(out, tv)
		}
	}
	return out
}

func (e *env) inBase(tasks []service.TaskView) ([]TaskFinding, bool) {
	e.t.Helper()
	f, all, err := tasksInBase(context.Background(), git.Repo{Dir: e.root}, e.base, "refs/heads/main", tasks)
	if err != nil {
		e.t.Fatalf("tasksInBase: %v", err)
	}
	return f, all
}

func wantFindings(t *testing.T, got []TaskFinding, ws ...want) {
	t.Helper()
	wantTasks(t, Report{Tasks: got}, ws...)
}

func TestTasksInBaseSquashKeepsTrailers(t *testing.T) {
	t.Parallel()
	e := newEnv(t, "login", "logout")
	e.do(0, true)
	e.do(1, true)
	t1, t2 := taskID(e.tasks[0]), taskID(e.tasks[1])
	gitCmd(t, e.root, "switch", "--quiet", "main")
	gitCmd(t, e.root, "merge", "--squash", "--quiet", branch)
	gitCmd(t, e.root, "commit", "--quiet", "-m", "Login and logout\n\nRalph-Task: "+t1+"\nRalph-Task: "+t2)
	sq := e.rev("HEAD")

	found, all := e.inBase(e.doneViews())
	wantFindings(t, found, want{MatchTrailer, sq}, want{MatchTrailer, sq})
	if !all {
		t.Error("all = false, want true")
	}
	if found[0].ID != t1 || found[0].Title != "login" || found[0].RecordedCommit == "" {
		t.Errorf("finding fields = %+v", found[0])
	}
}

func TestTasksInBaseRebaseWithoutTrailers(t *testing.T) {
	t.Parallel()
	e := newEnv(t, "login", "logout")
	c1, c2 := e.do(0, false), e.do(1, false)
	gitCmd(t, e.root, "switch", "--quiet", "main")
	gitCmd(t, e.root, "cherry-pick", c1)
	p1 := e.rev("HEAD")
	gitCmd(t, e.root, "cherry-pick", c2)
	p2 := e.rev("HEAD")

	found, all := e.inBase(e.doneViews())
	wantFindings(t, found, want{MatchPatchID, p1}, want{MatchPatchID, p2})
	if !all {
		t.Error("all = false, want true")
	}
}

func TestTasksInBasePartial(t *testing.T) {
	t.Parallel()
	e := newEnv(t, "login", "logout")
	c1 := e.do(0, false)
	e.do(1, false)
	gitCmd(t, e.root, "switch", "--quiet", "main")
	gitCmd(t, e.root, "cherry-pick", c1)
	p1 := e.rev("HEAD")

	found, all := e.inBase(e.doneViews())
	wantFindings(t, found, want{MatchPatchID, p1}, want{MatchMissing, ""})
	if all {
		t.Error("all = true, want false")
	}
}

func TestTasksInBaseSubjectNeverCounts(t *testing.T) {
	t.Parallel()
	e := newEnv(t, "login")
	e.do(0, true)
	tv := e.doneViews()[0]
	gitCmd(t, e.root, "switch", "--quiet", "main")
	e.commit("other.txt", "something else\n", subject(tv))

	found, all := e.inBase(e.doneViews())
	wantFindings(t, found, want{MatchMissing, ""})
	if all {
		t.Error("all = true, want false")
	}
}

func TestTasksInBaseNoTasks(t *testing.T) {
	t.Parallel()
	e := newEnv(t, "login")
	gitCmd(t, e.root, "switch", "--quiet", "main")
	e.commit("other.txt", "x\n", "other")
	found, all := e.inBase(nil)
	if len(found) != 0 || all {
		t.Errorf("found %+v all %v, want none and false", found, all)
	}
}

func TestTasksInBaseEmptyRange(t *testing.T) {
	t.Parallel()
	e := newEnv(t, "login", "logout")
	e.do(0, true)
	e.do(1, true)
	f, all, err := tasksInBase(context.Background(), git.Repo{Dir: e.root}, e.base, e.base, e.doneViews())
	if err != nil {
		t.Fatal(err)
	}
	wantFindings(t, f, want{MatchMissing, ""}, want{MatchMissing, ""})
	if all {
		t.Error("all = true, want false")
	}
}

func TestTasksInBasePatchIDsAreLazy(t *testing.T) {
	t.Parallel()
	e := newEnv(t, "login", "logout")
	e.do(0, true)
	e.do(1, true)
	t1, t2 := taskID(e.tasks[0]), taskID(e.tasks[1])
	gitCmd(t, e.root, "switch", "--quiet", "main")
	e.commit("a.txt", "a\n", "A\n\nRalph-Task: "+t1)
	e.commit("b.txt", "b\n", "B\n\nRalph-Task: "+t2)

	repo := git.Repo{Dir: e.root}
	calls := 0
	count := func(ctx context.Context, h string) (string, error) {
		calls++
		return repo.PatchID(ctx, h)
	}
	found, all, err := findInBase(context.Background(), repo, count, e.base, "refs/heads/main", e.doneViews())
	if err != nil || !all {
		t.Fatalf("found %+v all %v err %v", found, all, err)
	}
	if calls != 0 {
		t.Errorf("PatchID called %d times, want 0 when every task matches by trailer", calls)
	}
}

// squashed records the branch at its tip and squash-merges it into main with
// the given trailers kept, returning the squash commit.
func (e *env) squashed(trailers ...string) string {
	e.t.Helper()
	e.recordHere()
	gitCmd(e.t, e.root, "switch", "--quiet", "main")
	gitCmd(e.t, e.root, "merge", "--squash", "--quiet", branch)
	msg := "Squashed"
	if len(trailers) > 0 {
		msg += "\n\n" + strings.Join(trailers, "\n")
	}
	gitCmd(e.t, e.root, "commit", "--quiet", "-m", msg)
	return e.rev("HEAD")
}

func TestMergedBySquashTrailers(t *testing.T) {
	t.Parallel()
	e := newEnv(t, "login", "logout")
	e.do(0, true)
	e.do(1, true)
	sq := e.squashed("Ralph-Task: "+taskID(e.tasks[0]), "Ralph-Task: "+taskID(e.tasks[1]))

	r := e.check(e.input())
	wantState(t, r, StateMerged)
	wantMergedBy(t, r, MergedByTasks)
	wantTasks(t, r, want{MatchTrailer, sq}, want{MatchTrailer, sq})
	wantCommits(t, "unattributed", r.UnattributedCommits)
	if r.ExtraCommits != nil || r.LostCommits != nil || !r.BranchExists {
		t.Errorf("extra %v, lost %v, branch_exists %v", r.ExtraCommits, r.LostCommits, r.BranchExists)
	}
	if s := r.Summary(); !strings.Contains(s, "already merged into main (squash or rebase merge)") || strings.Contains(s, "branch deleted") {
		t.Errorf("summary = %q", s)
	}

	gitCmd(t, e.root, "branch", "--quiet", "-D", branch)
	r = e.check(e.input())
	wantState(t, r, StateMerged)
	wantMergedBy(t, r, MergedByTasks)
	wantTasks(t, r, want{MatchTrailer, sq}, want{MatchTrailer, sq})
	if r.BranchExists {
		t.Error("branch_exists = true after deleting the branch")
	}
	if s := r.Summary(); !strings.Contains(s, "(squash or rebase merge)") || !strings.Contains(s, "(branch deleted)") {
		t.Errorf("summary = %q", s)
	}
}

func TestMergedByRebasePatchID(t *testing.T) {
	t.Parallel()
	e := newEnv(t, "login", "logout")
	c1, c2 := e.do(0, false), e.do(1, false)
	e.recordHere()
	gitCmd(t, e.root, "switch", "--quiet", "main")
	e.commit("other.txt", "x\n", "unrelated")
	gitCmd(t, e.root, "cherry-pick", c1)
	p1 := e.rev("HEAD")
	gitCmd(t, e.root, "cherry-pick", c2)
	p2 := e.rev("HEAD")

	r := e.check(e.input())
	wantState(t, r, StateMerged)
	wantMergedBy(t, r, MergedByTasks)
	wantTasks(t, r, want{MatchPatchID, p1}, want{MatchPatchID, p2})
}

func TestPartialSquashNotMerged(t *testing.T) {
	t.Parallel()
	e := newEnv(t, "login", "logout")
	e.do(0, true)
	e.do(1, true)
	e.squashed("Ralph-Task: " + taskID(e.tasks[0]))

	r := e.check(e.input())
	wantState(t, r, StateUnchanged)
	wantMergedBy(t, r, "")

	gitCmd(t, e.root, "branch", "--quiet", "-D", branch)
	r = e.check(e.input())
	wantState(t, r, StateBranchMissing)
	wantMergedBy(t, r, "")
}

func TestSubjectOnlyOnMainNotMerged(t *testing.T) {
	t.Parallel()
	e := newEnv(t, "login")
	e.do(0, true)
	tv := e.doneViews()[0]
	e.recordHere()
	gitCmd(t, e.root, "switch", "--quiet", "main")
	e.commit("other.txt", "something else\n", subject(tv))

	r := e.check(e.input())
	wantState(t, r, StateUnchanged)
	wantMergedBy(t, r, "")
}

func TestNoDoneTasksNotMergedByTasks(t *testing.T) {
	t.Parallel()
	e := newEnv(t, "login")
	e.commit("a.txt", "a\n", "work")
	e.recordHere()
	gitCmd(t, e.root, "switch", "--quiet", "main")
	e.commit("b.txt", "b\n", "Ralph-Task: "+taskID(e.tasks[0]))

	r := e.check(e.input())
	wantState(t, r, StateUnchanged)
	wantMergedBy(t, r, "")
}

// unstartedMoved records an unstarted milestone, gives main two new commits
// and moves the branch to main's tip.
func unstartedMoved(t *testing.T) (*env, []string) {
	t.Helper()
	e := newEnv(t)
	e.recordHere()
	gitCmd(t, e.root, "switch", "--quiet", "main")
	c1 := e.commit("a.txt", "a\n", "Upstream a")
	c2 := e.commit("b.txt", "b\n", "Upstream b")
	gitCmd(t, e.root, "branch", "--quiet", "-f", branch, "main")
	return e, []string{c2, c1}
}

func TestUnstartedMovedOntoBase(t *testing.T) {
	t.Parallel()
	e, extra := unstartedMoved(t)
	r := e.check(e.input())
	wantState(t, r, StateAhead)
	wantMergedBy(t, r, "")
	if !r.Unstarted {
		t.Error("unstarted = false")
	}
	if r.ProposedBase != r.Tip || r.Tip != e.rev("main") {
		t.Errorf("proposed base %q, tip %q, want both main's tip", r.ProposedBase, r.Tip)
	}
	wantCommits(t, "extra commits", r.ExtraCommits, extra...)
}

func TestUnstartedWithOwnCommit(t *testing.T) {
	t.Parallel()
	e, _ := unstartedMoved(t)
	gitCmd(t, e.root, "switch", "--quiet", branch)
	own := e.commit("own.txt", "mine\n", "Mine")
	r := e.check(e.input())
	wantState(t, r, StateAhead)
	if !r.Unstarted || r.ProposedBase != e.base {
		t.Errorf("unstarted %v, proposed base %q, want true and %q", r.Unstarted, r.ProposedBase, e.base)
	}
	if len(r.ExtraCommits) == 0 || r.ExtraCommits[0].Hash != own {
		t.Errorf("extra commits = %v, want %s first", hashes(r.ExtraCommits), own)
	}
}

func TestUnstartedAtHead(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.recordHere()
	r := e.check(e.input())
	wantState(t, r, StateUnchanged)
	if !r.Unstarted {
		t.Error("unstarted = false")
	}
}

func TestStartedFastForwardMergedNotUnstarted(t *testing.T) {
	t.Parallel()
	e := newEnv(t, "login")
	e.do(0, true)
	e.recordHere()
	gitCmd(t, e.root, "switch", "--quiet", "main")
	gitCmd(t, e.root, "merge", "--quiet", "--ff-only", branch)
	r := e.check(e.input())
	wantState(t, r, StateMerged)
	wantMergedBy(t, r, MergedByAncestry)
	if r.Unstarted {
		t.Error("unstarted = true for a started milestone")
	}
}

func TestDeletedBranchNotUnstarted(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.recordHere()
	gitCmd(t, e.root, "switch", "--quiet", "main")
	gitCmd(t, e.root, "branch", "--quiet", "-D", branch)
	r := e.check(e.input())
	wantState(t, r, StateBranchMissing)
	if r.Unstarted {
		t.Error("unstarted = true for a deleted branch")
	}
}

func TestUnstartedAcceptMovedBranch(t *testing.T) {
	t.Parallel()
	e, _ := unstartedMoved(t)
	tip := e.rev("main")
	before, after := e.apply(Actions{Accept: true})
	wantState(t, before, StateAhead)
	wantState(t, after, StateUnchanged)
	e.wantRecord(domain.MilestoneGit{Branch: branch, BaseCommit: tip, HeadCommit: tip})
	wantState(t, e.check(e.input()), StateUnchanged)
}

func TestBaseRef(t *testing.T) {
	t.Parallel()
	e := newEnv(t, "login")
	e.do(0, true)
	e.recordHere()
	gitCmd(t, e.root, "switch", "--quiet", "main")
	gitCmd(t, e.root, "merge", "--quiet", "--no-ff", "-m", "Merge", branch)
	gitCmd(t, e.root, "update-ref", "refs/remotes/origin/main", "HEAD")
	gitCmd(t, e.root, "reset", "--quiet", "--hard", e.base)

	wantState(t, e.check(e.input()), StateUnchanged)
	in := e.input()
	in.BaseRef = "refs/remotes/origin/main"
	r := e.check(in)
	wantState(t, r, StateMerged)
	if r.BaseRef != in.BaseRef {
		t.Errorf("base_ref = %q", r.BaseRef)
	}

	in.BaseRef = "refs/remotes/origin/nope"
	if _, err := Check(context.Background(), in); !errors.Is(err, domain.ErrInvalid) {
		t.Errorf("unresolvable BaseRef err = %v", err)
	}
}
