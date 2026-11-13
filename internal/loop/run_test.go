package loop

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/bengearig/ralph/internal/config"
	"github.com/bengearig/ralph/internal/domain"
	"github.com/bengearig/ralph/internal/errs"
	"github.com/bengearig/ralph/internal/harness"
	"github.com/bengearig/ralph/internal/looplock"
	"github.com/bengearig/ralph/internal/project"
	"github.com/bengearig/ralph/internal/search"
	"github.com/bengearig/ralph/internal/service"
	"github.com/bengearig/ralph/internal/store/sqlite"
	"github.com/bengearig/ralph/internal/taskmanager"
	tmfake "github.com/bengearig/ralph/internal/taskmanager/fake"

	_ "github.com/bengearig/ralph/internal/harness/claudecode"
)

// fakeHarness runs a scripted function per session instead of an agent.
type fakeHarness struct {
	calls  int
	script func(call int, task int64, req harness.Request) (harness.Result, error)
}

var taskInPrompt = regexp.MustCompile(`Your task: T-(\d+)`)

func (h *fakeHarness) Name() string                     { return "fake" }
func (h *fakeHarness) Command(harness.Request) []string { return []string{"fake"} }
func (h *fakeHarness) Run(ctx context.Context, req harness.Request) (harness.Result, error) {
	h.calls++
	m := taskInPrompt.FindStringSubmatch(req.Prompt)
	if m == nil {
		return harness.Result{}, fmt.Errorf("prompt names no task")
	}
	var id int64
	fmt.Sscan(m[1], &id)
	fmt.Fprintln(req.Transcript, `{"type":"result","subtype":"success"}`)
	req.OnEvent(harness.Event{Kind: harness.EventTool, Text: "Edit work.txt"})
	return h.script(h.calls, id, req)
}

type runEnv struct {
	root string
	svc  *service.Service
	cfg  *config.Config
	st   *sqlite.Store
	mID  int64
	out  bytes.Buffer
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

// newRunEnv creates a git repository with a ralph project, one milestone,
// and a TODO task per title.
func newRunEnv(t *testing.T, titles ...string) *runEnv {
	t.Helper()
	return newRunEnvWith(t, nil, titles...)
}

// newRunEnvWith is newRunEnv with a task manager provider, which may be nil.
func newRunEnvWith(t *testing.T, tm taskmanager.Provider, titles ...string) *runEnv {
	t.Helper()
	return newRunEnvKinds(t, tm, nil, titles...)
}

// useGitEnv skips the test when git is missing. TestMain sets HOME and the
// git identity.
func useGitEnv(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
}

func newRunEnvKinds(t *testing.T, tm taskmanager.Provider, kinds []domain.Kind, titles ...string) *runEnv {
	t.Helper()
	useGitEnv(t)
	root := t.TempDir()
	restoreTree(t, root, initialRepo(t))
	return openRunEnv(t, root, tm, kinds, titles...)
}

// openRunEnv opens the ralph store under root and creates the initiative,
// milestone, and a TODO task per title.
func openRunEnv(t *testing.T, root string, tm taskmanager.Provider, kinds []domain.Kind, titles ...string) *runEnv {
	t.Helper()
	svc, cfg, st := openStore(t, root, tm, kinds)
	ctx := context.Background()
	i, _ := svc.CreateInitiative(ctx, service.InitiativeInput{Title: "SSO"})
	iID, _ := domain.ParseID(domain.KindInitiative, i.ID)
	m, _ := svc.CreateMilestone(ctx, service.MilestoneInput{InitiativeID: iID, Title: "OIDC login"})
	mID, _ := domain.ParseID(domain.KindMilestone, m.ID)
	for _, title := range titles {
		tv, err := svc.CreateTask(ctx, service.TaskInput{MilestoneID: mID, Title: title, Description: "do " + title, AcceptanceCriteria: title + " works"})
		if err != nil {
			t.Fatal(err)
		}
		id, _ := domain.ParseID(domain.KindTask, tv.ID)
		if _, err := svc.SetTaskStatus(ctx, id, domain.StatusTodo, ""); err != nil {
			t.Fatal(err)
		}
	}
	return &runEnv{root: root, svc: svc, cfg: cfg, st: st, mID: mID}
}

func openStore(t *testing.T, root string, tm taskmanager.Provider, kinds []domain.Kind) (*service.Service, *config.Config, *sqlite.Store) {
	t.Helper()
	ctx := context.Background()
	st, err := sqlite.Open(ctx, filepath.Join(root, project.DirName, "ralph.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	cfg, err := config.Defaults()
	if err != nil {
		t.Fatal(err)
	}
	cfg.Loop.StallThreshold = 2
	cfg.Loop.MaxIterations = 10
	svc := service.New(st, &search.Index{Store: st, Chunker: search.Chunker{MaxChars: 500}}, service.Options{StallThreshold: cfg.Loop.StallThreshold, TaskManager: tm, CardKinds: kinds})
	return svc, cfg, st
}

// run runs the loop and then checks the milestone branch back out, so tests
// can inspect its work tree; runRaw leaves the tree where Run put it.
func (e *runEnv) run(t *testing.T, ctx context.Context, h harness.Harness) (Outcome, error) {
	t.Helper()
	out, err := e.runRaw(t, ctx, h)
	if out.Iterations > 0 {
		gitCmd(t, e.root, "switch", "--quiet", out.Branch)
	}
	return out, err
}

func (e *runEnv) runRaw(t *testing.T, ctx context.Context, h harness.Harness) (Outcome, error) {
	t.Helper()
	plan, err := Planner{Service: e.svc, Config: e.cfg, Root: e.root}.Plan(ctx, e.mID)
	if err != nil {
		t.Fatal(err)
	}
	return Runner{Service: e.svc, Config: e.cfg, Root: e.root, Harness: h, Out: &e.out}.Run(ctx, plan)
}

func (e *runEnv) status(t *testing.T, id int64) service.TaskView {
	t.Helper()
	v, err := e.svc.GetTask(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// complete simulates an agent finishing task id: it edits a file and marks
// the task DONE through the service, as the MCP tool would.
func (e *runEnv) complete(id int64) (harness.Result, error) {
	name := fmt.Sprintf("task-%d.txt", id)
	if err := os.WriteFile(filepath.Join(e.root, name), []byte("done\n"), 0o644); err != nil {
		return harness.Result{}, err
	}
	_, err := e.svc.SetTaskStatus(context.Background(), id, domain.StatusDone, "criteria verified")
	return harness.Result{Completed: true, Subtype: "success", Turns: 3, CostUSD: 0.5}, err
}

func TestRunCompletesMilestone(t *testing.T) {
	t.Parallel()
	e := newRunEnv(t, "first", "second")
	h := &fakeHarness{script: func(_ int, id int64, req harness.Request) (harness.Result, error) {
		if e.status(t, id).Status != "IN_PROGRESS" {
			t.Errorf("T-%d should be IN_PROGRESS during its session", id)
		}
		return e.complete(id)
	}}
	out, err := e.run(t, context.Background(), h)
	if err != nil {
		t.Fatalf("Run: %v\n%s", err, e.out.String())
	}
	if out.StopReason != StopCompleted || out.Iterations != 2 || len(out.Done) != 2 || len(out.Commits) != 2 || out.CostUSD != 1 {
		t.Errorf("outcome = %+v", out)
	}
	if b := gitCmd(t, e.root, "branch", "--show-current"); b != "ralph/oidc-login" {
		t.Errorf("branch = %q", b)
	}
	if log := gitCmd(t, e.root, "log", "--format=%s", "main..HEAD"); log != "T-2: second\nT-1: first" {
		t.Errorf("commits = %q", log)
	}
	for i, id := range []int64{1, 2} {
		v := e.status(t, id)
		want := e.commitWithSubject(t, v.ID+": "+v.Title)
		if len(want) != 40 || v.Commit != want {
			t.Errorf("%s commit = %q, want %q", v.ID, v.Commit, want)
		}
		if c := out.Commits[i]; c.Task != v.ID || c.Hash != want {
			t.Errorf("outcome commit %d = %+v, want %s %s", i, c, v.ID, want)
		}
		if tr := gitCmd(t, e.root, "log", "-1", "--format=%(trailers:key=Ralph-Task,valueonly)", want); tr != v.ID {
			t.Errorf("%s Ralph-Task trailer = %q", v.ID, tr)
		}
	}
	e.assertHead(t)
	if short := out.Commits[0].Hash[:7]; !strings.Contains(e.out.String(), "  committed "+short+` "T-1: first"`+"\n") {
		t.Errorf("feed should show %s as %s:\n%s", out.Commits[0].Hash, short, e.out.String())
	}
	if files := gitCmd(t, e.root, "show", "--name-only", "--format=", "HEAD"); files != "task-2.txt" {
		t.Errorf("last commit files = %q", files)
	}
	if st := gitCmd(t, e.root, "status", "--porcelain", "--", ".", ":(exclude).ralph"); st != "" {
		t.Errorf("tree not clean outside .ralph: %q", st)
	}
	if tracked := gitCmd(t, e.root, "ls-files", ".ralph"); tracked != "" {
		t.Errorf(".ralph files were committed: %q", tracked)
	}
	logs, _ := filepath.Glob(filepath.Join(e.root, ".ralph", "runs", "M-1", "*"))
	if len(logs) != 4 {
		t.Errorf("run logs = %v", logs)
	}
	prompts, _ := filepath.Glob(filepath.Join(e.root, ".ralph", "runs", "M-1", "*-001-T-1.prompt.md"))
	if len(prompts) != 1 {
		t.Errorf("prompt log missing: %v", logs)
	}
	feed := e.out.String()
	for _, want := range []string{"creating ralph/oidc-login from main", "▶ [1/10] T-1 first", "· Edit work.txt", "✓ T-1 DONE", "committed", "next: ralph pr create --milestone M-1"} {
		if !strings.Contains(feed, want) {
			t.Errorf("feed lacks %q:\n%s", want, feed)
		}
	}
}

func TestRunSkipsBlockedAndStopsForHuman(t *testing.T) {
	t.Parallel()
	e := newRunEnv(t, "flaky", "fine")
	h := &fakeHarness{script: func(_ int, id int64, _ harness.Request) (harness.Result, error) {
		if id == 1 {
			return harness.Result{Completed: true, IsError: true, Subtype: "error_max_turns", Summary: "ran out of turns"}, nil
		}
		return e.complete(id)
	}}
	out, err := e.run(t, context.Background(), h)
	if !errors.Is(err, errs.ErrNeedsHuman) {
		t.Fatalf("err = %v\n%s", err, e.out.String())
	}
	if h.calls != 3 {
		t.Errorf("sessions = %d; want two failed attempts at T-1 then T-2", h.calls)
	}
	t1 := e.status(t, 1)
	if t1.Status != "BLOCKED" || !strings.Contains(t1.BlockedReason, "Stall Statute") || !strings.Contains(t1.BlockedReason, "error_max_turns: ran out of turns") {
		t.Errorf("T-1 = %+v", t1)
	}
	if e.status(t, 2).Status != "DONE" {
		t.Error("T-2 should have been worked after T-1 was blocked")
	}
	if out.StopReason != StopNeedsHuman || len(out.Blocked) != 1 || out.Blocked[0].ID != "T-1" || len(out.Commits) != 1 {
		t.Errorf("outcome = %+v", out)
	}
	if !strings.Contains(e.out.String(), "↺ T-1 not finished (failure 1 of 2)") || !strings.Contains(e.out.String(), "✗ T-1 BLOCKED") {
		t.Errorf("feed:\n%s", e.out.String())
	}
}

func TestRunAgentKickbacks(t *testing.T) {
	t.Parallel()
	e := newRunEnv(t, "unclear", "blocked")
	h := &fakeHarness{script: func(_ int, id int64, _ harness.Request) (harness.Result, error) {
		to, reason := domain.StatusProposed, "acceptance criteria contradict the design"
		if id == 2 {
			to, reason = domain.StatusBlocked, "needs production credentials"
		}
		_, err := e.svc.SetTaskStatus(context.Background(), id, to, reason)
		return harness.Result{Completed: true}, err
	}}
	out, err := e.run(t, context.Background(), h)
	if !errors.Is(err, errs.ErrNeedsHuman) {
		t.Fatalf("err = %v", err)
	}
	if len(out.Proposed) != 1 || len(out.Blocked) != 1 || out.Blocked[0].Reason != "needs production credentials" || len(out.Commits) != 0 {
		t.Errorf("outcome = %+v", out)
	}
}

func TestRunIterationLimit(t *testing.T) {
	t.Parallel()
	e := newRunEnv(t, "a", "b", "c")
	e.cfg.Loop.MaxIterations = 2
	h := &fakeHarness{script: func(_ int, id int64, _ harness.Request) (harness.Result, error) { return e.complete(id) }}
	out, err := e.run(t, context.Background(), h)
	if !errors.Is(err, errs.ErrNeedsHuman) || !strings.Contains(err.Error(), "T-3 still loopable") {
		t.Fatalf("err = %v", err)
	}
	if out.StopReason != StopIterationLimit || out.Iterations != 2 {
		t.Errorf("outcome = %+v", out)
	}
	// Rerunning continues where it stopped.
	out, err = e.run(t, context.Background(), h)
	if err != nil || out.StopReason != StopCompleted || len(out.Done) != 1 {
		t.Errorf("rerun = %+v, %v", out, err)
	}
}

func TestRunDirtyTree(t *testing.T) {
	t.Parallel()
	e := newRunEnv(t, "a")
	os.WriteFile(filepath.Join(e.root, "README.md"), []byte("local edit\n"), 0o644)
	h := &fakeHarness{script: func(_ int, id int64, _ harness.Request) (harness.Result, error) { return e.complete(id) }}
	_, err := e.run(t, context.Background(), h)
	if err == nil || !strings.Contains(err.Error(), "uncommitted changes") || !strings.Contains(err.Error(), "README.md") {
		t.Fatalf("err = %v", err)
	}
	if h.calls != 0 || e.status(t, 1).Status != "TODO" {
		t.Error("nothing should run on a dirty tree that needs a branch switch")
	}

	gitCmd(t, e.root, "stash", "--quiet")
	gitCmd(t, e.root, "switch", "--quiet", "-c", "ralph/oidc-login")
	gitCmd(t, e.root, "stash", "pop", "--quiet")
	out, err := e.run(t, context.Background(), h)
	if err != nil || len(out.Commits) != 1 {
		t.Fatalf("on the milestone branch a dirty tree is resumed work: %+v, %v", out, err)
	}
	if files := gitCmd(t, e.root, "show", "--name-only", "--format=", "HEAD"); !strings.Contains(files, "README.md") {
		t.Errorf("leftover work should be committed with the task: %q", files)
	}
}

func TestRunResumesInProgressTask(t *testing.T) {
	t.Parallel()
	e := newRunEnv(t, "a")
	e.svc.SetTaskStatus(context.Background(), 1, domain.StatusInProgress, "")
	h := &fakeHarness{script: func(_ int, id int64, _ harness.Request) (harness.Result, error) { return e.complete(id) }}
	if _, err := e.run(t, context.Background(), h); err != nil {
		t.Fatal(err)
	}
	if notes := e.status(t, 1).Notes; len(notes) != 1 {
		t.Errorf("resumed task should not be re-transitioned; notes = %+v", notes)
	}
}

func TestRunInterruptedLeavesTaskInProgress(t *testing.T) {
	t.Parallel()
	e := newRunEnv(t, "a")
	ctx, cancel := context.WithCancel(context.Background())
	h := &fakeHarness{script: func(int, int64, harness.Request) (harness.Result, error) {
		e.commitFile(t, "agent.txt")
		cancel()
		return harness.Result{}, ctx.Err()
	}}
	out, err := e.run(t, ctx, h)
	if !errors.Is(err, context.Canceled) || out.StopReason != StopInterrupted {
		t.Fatalf("out = %+v, err = %v", out, err)
	}
	if v := e.status(t, 1); v.Status != "IN_PROGRESS" || v.FailureCount != 0 || v.Commit != "" {
		t.Errorf("task = %+v", v)
	}
	if e.assertHead(t) != e.commitWithSubject(t, "add agent.txt") {
		t.Error("HEAD should be the agent's commit")
	}
	if !strings.Contains(e.out.String(), "T-1 left IN_PROGRESS, rerun to resume") {
		t.Errorf("feed:\n%s", e.out.String())
	}
}

func TestRunToolsUnavailableCountsNoFailure(t *testing.T) {
	t.Parallel()
	e := newRunEnv(t, "a")
	h := &fakeHarness{script: func(int, int64, harness.Request) (harness.Result, error) {
		return harness.Result{}, fmt.Errorf("%w: not approved", harness.ErrToolsUnavailable)
	}}
	_, err := e.run(t, context.Background(), h)
	if !errors.Is(err, harness.ErrToolsUnavailable) {
		t.Fatalf("err = %v", err)
	}
	if v := e.status(t, 1); v.Status != "IN_PROGRESS" || v.FailureCount != 0 {
		t.Errorf("task = %+v", v)
	}
}

func TestRunLockContention(t *testing.T) {
	t.Parallel()
	e := newRunEnv(t, "a")
	lock, err := looplock.Acquire(filepath.Join(e.root, ".ralph", "loop.lock"), "milestone M-9")
	if err != nil {
		t.Fatal(err)
	}
	h := &fakeHarness{script: func(_ int, id int64, _ harness.Request) (harness.Result, error) { return e.complete(id) }}
	_, err = e.run(t, context.Background(), h)
	if !errors.Is(err, looplock.ErrLoopRunning) || !strings.Contains(err.Error(), "milestone M-9") {
		t.Fatalf("err = %v", err)
	}
	if err := lock.Release(); err != nil {
		t.Fatal(err)
	}
	if _, err := e.run(t, context.Background(), h); err != nil {
		t.Errorf("after release: %v", err)
	}
}

func TestRunWithoutCommits(t *testing.T) {
	t.Parallel()
	e := newRunEnv(t, "a")
	e.cfg.Loop.CommitPerTask = false
	h := &fakeHarness{script: func(_ int, id int64, _ harness.Request) (harness.Result, error) {
		e.commitFile(t, "agent.txt")
		return e.complete(id)
	}}
	out, err := e.run(t, context.Background(), h)
	if err != nil || len(out.Commits) != 0 {
		t.Fatalf("out = %+v, err = %v", out, err)
	}
	if v := e.status(t, 1); v.Commit != "" {
		t.Errorf("no loop commit should be recorded: %q", v.Commit)
	}
	if e.assertHead(t) != e.commitWithSubject(t, "add agent.txt") {
		t.Error("HEAD should be the agent's commit")
	}
	if st := gitCmd(t, e.root, "status", "--porcelain"); !strings.Contains(st, "task-1.txt") {
		t.Errorf("work should be left uncommitted: %q", st)
	}
}

func TestRunWithoutCommitTrailer(t *testing.T) {
	t.Parallel()
	e := newRunEnv(t, "a", "b")
	e.cfg.Loop.CommitTrailer = false
	h := &fakeHarness{script: func(_ int, id int64, _ harness.Request) (harness.Result, error) { return e.complete(id) }}
	out, err := e.run(t, context.Background(), h)
	if err != nil || len(out.Commits) != 2 {
		t.Fatalf("out = %+v, err = %v", out, err)
	}
	for _, c := range out.Commits {
		if tr := gitCmd(t, e.root, "log", "-1", "--format=%(trailers:key=Ralph-Task)", c.Hash); tr != "" {
			t.Errorf("%s has a Ralph-Task trailer: %q", c.Task, tr)
		}
		if msg := gitCmd(t, e.root, "log", "-1", "--format=%B", c.Hash); strings.Contains(msg, "Ralph-Task") {
			t.Errorf("%s message = %q", c.Task, msg)
		}
	}
}

func TestRunRecordsHeadForUnfinishedIterations(t *testing.T) {
	t.Parallel()
	e := newRunEnv(t, "flaky", "unclear")
	var heads []string
	h := &fakeHarness{script: func(call int, id int64, _ harness.Request) (harness.Result, error) {
		if len(heads) > 0 {
			if got := e.milestone(t).HeadCommit; got != heads[len(heads)-1] {
				t.Errorf("session %d sees head %q, want the previous session's HEAD %q", call, got, heads[len(heads)-1])
			}
		}
		e.commitFile(t, fmt.Sprintf("agent-%d.txt", call))
		heads = append(heads, gitCmd(t, e.root, "rev-parse", "HEAD"))
		var err error
		switch call {
		case 1:
		case 2:
			_, err = e.svc.SetTaskStatus(context.Background(), id, domain.StatusBlocked, "stuck")
		default:
			_, err = e.svc.SetTaskStatus(context.Background(), id, domain.StatusProposed, "unclear")
		}
		return harness.Result{Completed: true}, err
	}}
	_, err := e.run(t, context.Background(), h)
	if !errors.Is(err, errs.ErrNeedsHuman) || h.calls != 3 {
		t.Fatalf("calls = %d, err = %v\n%s", h.calls, err, e.out.String())
	}
	feed := e.out.String()
	for _, want := range []string{"↺ T-1 not finished", "✗ T-1 BLOCKED", "⤺ T-2 sent back to PROPOSED"} {
		if !strings.Contains(feed, want) {
			t.Errorf("feed lacks %q:\n%s", want, feed)
		}
	}
	if head := e.assertHead(t); head != heads[2] {
		t.Errorf("HEAD = %q, want the last session's commit %q", head, heads[2])
	}
	for _, id := range []int64{1, 2} {
		if v := e.status(t, id); v.Commit != "" {
			t.Errorf("%s commit = %q, want none", v.ID, v.Commit)
		}
	}
}

func TestRunCommitHookFailureRecordsNoTaskCommit(t *testing.T) {
	t.Parallel()
	e := newRunEnv(t, "a")
	hook := filepath.Join(e.root, ".git", "hooks", "pre-commit")
	if err := os.WriteFile(hook, []byte("#!/bin/sh\necho 'lint failed' >&2\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	h := &fakeHarness{script: func(_ int, id int64, _ harness.Request) (harness.Result, error) { return e.complete(id) }}
	out, err := e.run(t, context.Background(), h)
	if err == nil || !strings.Contains(err.Error(), "T-1 is DONE but the commit failed") || !strings.Contains(err.Error(), "lint failed") {
		t.Fatalf("err = %v\n%s", err, e.out.String())
	}
	if v := e.status(t, 1); v.Status != "DONE" || v.Commit != "" {
		t.Errorf("task = %+v", v)
	}
	if len(out.Commits) != 0 || strings.Contains(e.out.String(), "could not record head") {
		t.Errorf("out = %+v\n%s", out, e.out.String())
	}
	if e.assertHead(t) != gitCmd(t, e.root, "rev-parse", "main") {
		t.Error("HEAD should still be the base commit")
	}
}

func TestRunCommitTemplateAndBaseFallback(t *testing.T) {
	t.Parallel()
	e := newRunEnv(t, "a")
	e.cfg.Loop.CommitTemplate = "feat({{.Milestone.Slug}}): {{.Task.Title}} [{{.Task.ID}}]"
	e.cfg.PR.BaseBranch = "develop"
	h := &fakeHarness{script: func(_ int, id int64, _ harness.Request) (harness.Result, error) { return e.complete(id) }}
	if _, err := e.run(t, context.Background(), h); err == nil || !strings.Contains(err.Error(), `base branch "develop" not found`) {
		t.Fatalf("missing base: %v", err)
	}
	gitCmd(t, e.root, "branch", "develop")
	if _, err := e.run(t, context.Background(), h); err != nil {
		t.Fatal(err)
	}
	if msg := gitCmd(t, e.root, "log", "-1", "--format=%s"); msg != "feat(oidc-login): a [T-1]" {
		t.Errorf("message = %q", msg)
	}
}

func TestSessionReason(t *testing.T) {
	t.Parallel()
	cases := []struct {
		res  harness.Result
		want string
	}{
		{harness.Result{ExitCode: 1, Summary: "auth\nfailed"}, "session ended without a final result (exit code 1): auth failed"},
		{harness.Result{Completed: true, IsError: true, Subtype: "error_max_budget_usd"}, "session ended with error_max_budget_usd"},
		{harness.Result{Completed: true, Summary: "I think it works"}, "session finished without setting a task status: I think it works"},
	}
	for _, c := range cases {
		if got := sessionReason(c.res); got != c.want {
			t.Errorf("sessionReason(%+v) = %q, want %q", c.res, got, c.want)
		}
	}
	long := sessionReason(harness.Result{Completed: true, Summary: strings.Repeat("x", 1000)})
	if n := len([]rune(long)); n > 400 {
		t.Errorf("reason too long: %d runes", n)
	}
}

func TestRunStashesUnfinishedWorkBeforeNextTask(t *testing.T) {
	t.Parallel()
	e := newRunEnv(t, "blocked", "fine")
	h := &fakeHarness{script: func(_ int, id int64, _ harness.Request) (harness.Result, error) {
		if id == 1 {
			os.WriteFile(filepath.Join(e.root, "partial.txt"), []byte("half done\n"), 0o644)
			_, err := e.svc.SetTaskStatus(context.Background(), id, domain.StatusBlocked, "needs a decision")
			return harness.Result{Completed: true}, err
		}
		if _, err := os.Stat(filepath.Join(e.root, "partial.txt")); err == nil {
			t.Error("T-1's partial work was still in the tree when T-2 started")
		}
		return e.complete(id)
	}}
	out, err := e.run(t, context.Background(), h)
	if !errors.Is(err, errs.ErrNeedsHuman) {
		t.Fatalf("err = %v", err)
	}
	if len(out.Stashes) != 1 || out.Stashes[0] != "ralph: T-1 BLOCKED, unfinished work" {
		t.Errorf("stashes = %v", out.Stashes)
	}
	if files := gitCmd(t, e.root, "show", "--name-only", "--format=", "HEAD"); files != "task-2.txt" {
		t.Errorf("T-2's commit should contain only its own work: %q", files)
	}
	if list := gitCmd(t, e.root, "stash", "list"); !strings.Contains(list, "ralph: T-1 BLOCKED, unfinished work") {
		t.Errorf("stash list = %q", list)
	}
}

func TestRunKeepsWorkForRetryOfSameTask(t *testing.T) {
	t.Parallel()
	e := newRunEnv(t, "a")
	h := &fakeHarness{script: func(call int, id int64, _ harness.Request) (harness.Result, error) {
		if call == 1 {
			os.WriteFile(filepath.Join(e.root, "progress.txt"), []byte("step 1\n"), 0o644)
			return harness.Result{Completed: true}, nil
		}
		if _, err := os.Stat(filepath.Join(e.root, "progress.txt")); err != nil {
			t.Error("a retry of the same task should see its earlier work")
		}
		return e.complete(id)
	}}
	out, err := e.run(t, context.Background(), h)
	if err != nil || len(out.Stashes) != 0 {
		t.Fatalf("out = %+v, err = %v", out, err)
	}
	if files := gitCmd(t, e.root, "show", "--name-only", "--format=", "HEAD"); !strings.Contains(files, "progress.txt") {
		t.Errorf("commit files = %q", files)
	}
}

func TestRunReportsLeftoverAtEnd(t *testing.T) {
	t.Parallel()
	e := newRunEnv(t, "a")
	h := &fakeHarness{script: func(_ int, id int64, _ harness.Request) (harness.Result, error) {
		os.WriteFile(filepath.Join(e.root, "partial.txt"), []byte("x\n"), 0o644)
		_, err := e.svc.SetTaskStatus(context.Background(), id, domain.StatusBlocked, "stuck")
		return harness.Result{Completed: true}, err
	}}
	if _, err := e.run(t, context.Background(), h); !errors.Is(err, errs.ErrNeedsHuman) {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(e.out.String(), "uncommitted changes from T-1 (BLOCKED) are left in the working tree") {
		t.Errorf("feed:\n%s", e.out.String())
	}
}

// blocker is a harness that blocks every task without touching the tree,
// recording the prompts it was given.
func (e *runEnv) blocker(prompts *[]string) *fakeHarness {
	return &fakeHarness{script: func(_ int, id int64, req harness.Request) (harness.Result, error) {
		*prompts = append(*prompts, req.Prompt)
		_, err := e.svc.SetTaskStatus(context.Background(), id, domain.StatusBlocked, "stop here")
		return harness.Result{Completed: true}, err
	}}
}

func (e *runEnv) milestone(t *testing.T) service.MilestoneView {
	t.Helper()
	m, err := e.svc.GetMilestone(context.Background(), e.mID)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func (e *runEnv) commitFile(t *testing.T, name string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(e.root, name), []byte(name+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitCmd(t, e.root, "add", name)
	gitCmd(t, e.root, "commit", "--quiet", "-m", "add "+name)
}

// commitWithSubject returns the full hash of the one commit on HEAD whose
// subject is subject.
func (e *runEnv) commitWithSubject(t *testing.T, subject string) string {
	t.Helper()
	var found []string
	for _, line := range strings.Split(gitCmd(t, e.root, "log", "--format=%H %s", "HEAD"), "\n") {
		if hash, s, _ := strings.Cut(line, " "); s == subject {
			found = append(found, hash)
		}
	}
	if len(found) != 1 {
		t.Fatalf("commits with subject %q: %v", subject, found)
	}
	return found[0]
}

func (e *runEnv) assertHead(t *testing.T) string {
	t.Helper()
	head := gitCmd(t, e.root, "rev-parse", "HEAD")
	if got := e.milestone(t).HeadCommit; got != head {
		t.Errorf("recorded head = %q, want HEAD %q", got, head)
	}
	return head
}

func TestRunRecordsCreatedBranch(t *testing.T) {
	t.Parallel()
	e := newRunEnv(t, "a")
	mainHash := gitCmd(t, e.root, "rev-parse", "main")
	plan, err := Planner{Service: e.svc, Config: e.cfg, Root: e.root}.Plan(context.Background(), e.mID)
	if err != nil {
		t.Fatal(err)
	}
	var prompts []string
	_, err = Runner{Service: e.svc, Config: e.cfg, Root: e.root, Harness: e.blocker(&prompts), Out: &e.out}.Run(context.Background(), plan)
	if !errors.Is(err, errs.ErrNeedsHuman) {
		t.Fatalf("err = %v\n%s", err, e.out.String())
	}
	if !strings.Contains(e.out.String(), "creating ralph/oidc-login from main") {
		t.Errorf("feed:\n%s", e.out.String())
	}
	m := e.milestone(t)
	if len(mainHash) != 40 || m.Branch != plan.Branch || m.BaseCommit != mainHash || m.HeadCommit != mainHash {
		t.Errorf("record = %q base %q head %q; want %q base and head %q", m.Branch, m.BaseCommit, m.HeadCommit, plan.Branch, mainHash)
	}
}

func TestRunAdoptsExistingUnrecordedBranch(t *testing.T) {
	t.Parallel()
	e := newRunEnv(t, "a")
	gitCmd(t, e.root, "switch", "--quiet", "-c", "ralph/oidc-login")
	e.commitFile(t, "earlier.txt")
	gitCmd(t, e.root, "switch", "--quiet", "main")
	e.commitFile(t, "main-moves-on.txt")
	tip := gitCmd(t, e.root, "rev-parse", "ralph/oidc-login")
	base := gitCmd(t, e.root, "merge-base", "ralph/oidc-login", "main")

	var prompts []string
	if _, err := e.run(t, context.Background(), e.blocker(&prompts)); !errors.Is(err, errs.ErrNeedsHuman) {
		t.Fatalf("err = %v\n%s", err, e.out.String())
	}
	if !strings.Contains(e.out.String(), "switching to ralph/oidc-login") {
		t.Errorf("feed:\n%s", e.out.String())
	}
	m := e.milestone(t)
	if m.Branch != "ralph/oidc-login" || m.BaseCommit != base || m.HeadCommit != tip {
		t.Errorf("record = %q base %q head %q; want base %q head %q", m.Branch, m.BaseCommit, m.HeadCommit, base, tip)
	}
}

func TestRunAdoptsUnrelatedBranchWithoutBase(t *testing.T) {
	t.Parallel()
	e := newRunEnv(t, "a")
	gitCmd(t, e.root, "switch", "--quiet", "--orphan", "ralph/oidc-login")
	e.commitFile(t, "unrelated.txt")
	tip := gitCmd(t, e.root, "rev-parse", "HEAD")

	var prompts []string
	if _, err := e.run(t, context.Background(), e.blocker(&prompts)); !errors.Is(err, errs.ErrNeedsHuman) {
		t.Fatalf("err = %v\n%s", err, e.out.String())
	}
	if !strings.Contains(e.out.String(), "! ralph/oidc-login shares no history with main; recording it without a base commit") {
		t.Errorf("feed:\n%s", e.out.String())
	}
	if m := e.milestone(t); m.Branch != "ralph/oidc-login" || m.BaseCommit != "" || m.HeadCommit != tip {
		t.Errorf("record = %q base %q head %q; want head %q", m.Branch, m.BaseCommit, m.HeadCommit, tip)
	}
}

func TestRunUsesRecordedBranch(t *testing.T) {
	t.Parallel()
	e := newRunEnv(t, "a")
	gitCmd(t, e.root, "branch", "feature/custom")
	g := domain.MilestoneGit{Branch: "feature/custom"}
	if err := e.svc.RecordMilestoneGit(context.Background(), e.mID, g); err != nil {
		t.Fatal(err)
	}
	plan, err := Planner{Service: e.svc, Config: e.cfg, Root: e.root}.Plan(context.Background(), e.mID)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Branch != "feature/custom" || !strings.Contains(plan.FirstPrompt, "Branch: feature/custom") {
		t.Fatalf("plan branch = %q", plan.Branch)
	}
	var prompts []string
	out, err := Runner{Service: e.svc, Config: e.cfg, Root: e.root, Harness: e.blocker(&prompts), Out: &e.out}.Run(context.Background(), plan)
	if !errors.Is(err, errs.ErrNeedsHuman) {
		t.Fatalf("err = %v\n%s", err, e.out.String())
	}
	if out.Branch != "feature/custom" || gitCmd(t, e.root, "branch", "--list", "feature/custom") == "" {
		t.Errorf("outcome branch %q", out.Branch)
	}
	if list := gitCmd(t, e.root, "branch", "--list", "ralph/oidc-login"); list != "" {
		t.Errorf("template branch was created: %q", list)
	}
	if m := e.milestone(t); m.Branch != g.Branch || m.BaseCommit != "" {
		t.Errorf("record changed to %q base %q", m.Branch, m.BaseCommit)
	}
	e.assertHead(t)
}

// newRecordedEnv loops T-1 to DONE and T-2 to BLOCKED, then returns T-2 to
// TODO, leaving the milestone recorded on ralph/oidc-login at T-1's commit.
func newRecordedEnv(t *testing.T) *runEnv {
	t.Helper()
	e := newRunEnv(t, "a", "b")
	ctx := context.Background()
	h := &fakeHarness{script: func(_ int, id int64, _ harness.Request) (harness.Result, error) {
		if id == 1 {
			return e.complete(id)
		}
		_, err := e.svc.SetTaskStatus(ctx, id, domain.StatusBlocked, "stop here")
		return harness.Result{Completed: true}, err
	}}
	if _, err := e.run(t, ctx, h); !errors.Is(err, errs.ErrNeedsHuman) {
		t.Fatalf("setup run: %v\n%s", err, e.out.String())
	}
	if _, err := e.svc.SetTaskStatus(ctx, 2, domain.StatusTodo, ""); err != nil {
		t.Fatal(err)
	}
	if head := e.assertHead(t); head != e.status(t, 1).Commit {
		t.Fatalf("setup head %q is not T-1's commit %q", head, e.status(t, 1).Commit)
	}
	e.out.Reset()
	return e
}

func (e *runEnv) tasks(t *testing.T) []service.TaskView {
	t.Helper()
	ts, err := e.svc.ListTasks(context.Background(), service.TaskFilter{MilestoneID: e.mID})
	if err != nil {
		t.Fatal(err)
	}
	return ts
}

// assertDriftRefusal runs the loop and checks that it refuses with a message
// containing want, before switching branches, starting a session, or
// changing any task or the milestone record.
func (e *runEnv) assertDriftRefusal(t *testing.T, want string) {
	t.Helper()
	branch := gitCmd(t, e.root, "branch", "--show-current")
	head := gitCmd(t, e.root, "rev-parse", "HEAD")
	tasks, record := e.tasks(t), e.milestone(t)
	h := &fakeHarness{script: func(int, int64, harness.Request) (harness.Result, error) {
		t.Error("no session should start on a drifted branch")
		return harness.Result{}, nil
	}}
	out, err := e.run(t, context.Background(), h)
	if !errors.Is(err, errs.ErrNeedsHuman) {
		t.Fatalf("err = %v, want ErrNeedsHuman\n%s", err, e.out.String())
	}
	msg := "; run: ralph milestone reconcile M-1"
	if !strings.Contains(err.Error(), want) || !strings.HasSuffix(err.Error(), msg) {
		t.Errorf("err = %q, want it to contain %q and end with %q", err, want, msg)
	}
	if line := strings.TrimPrefix(err.Error(), errs.ErrNeedsHuman.Error()+": "); !strings.Contains(e.out.String(), "■ "+line+"\n") {
		t.Errorf("feed lacks the refusal %q:\n%s", line, e.out.String())
	}
	if out.StopReason != StopNeedsHuman || out.Iterations != 0 || h.calls != 0 {
		t.Errorf("outcome = %+v, sessions = %d", out, h.calls)
	}
	if b, hd := gitCmd(t, e.root, "branch", "--show-current"), gitCmd(t, e.root, "rev-parse", "HEAD"); b != branch || hd != head {
		t.Errorf("on %q at %s, want %q at %s", b, hd, branch, head)
	}
	if strings.Contains(e.out.String(), "switching to") || strings.Contains(e.out.String(), "creating ") {
		t.Errorf("feed shows a branch change:\n%s", e.out.String())
	}
	after := e.tasks(t)
	if len(after) != len(tasks) {
		t.Fatalf("tasks = %d, want %d", len(after), len(tasks))
	}
	for i, v := range after {
		if v.Status != tasks[i].Status || v.Commit != tasks[i].Commit || v.FailureCount != tasks[i].FailureCount {
			t.Errorf("%s changed from %s %q to %s %q", v.ID, tasks[i].Status, tasks[i].Commit, v.Status, v.Commit)
		}
	}
	if m := e.milestone(t); m.Branch != record.Branch || m.BaseCommit != record.BaseCommit || m.HeadCommit != record.HeadCommit {
		t.Errorf("record changed from %+v to %+v", record, m)
	}
}

func TestRunRefusesDivergedBranch(t *testing.T) {
	t.Parallel()
	e := newRecordedEnv(t)
	recorded := e.milestone(t).HeadCommit
	gitCmd(t, e.root, "switch", "--quiet", "main")
	e.commitFile(t, "main-moves-on.txt")
	gitCmd(t, e.root, "switch", "--quiet", "ralph/oidc-login")
	gitCmd(t, e.root, "rebase", "--quiet", "main")
	gitCmd(t, e.root, "switch", "--quiet", "main")
	if err := os.WriteFile(filepath.Join(e.root, "README.md"), []byte("local edit\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	e.assertDriftRefusal(t, "M-1 has diverged from its recorded head "+recorded[:12])
	if strings.Contains(e.out.String(), "uncommitted changes") {
		t.Errorf("the drift refusal should come before the dirty-tree one:\n%s", e.out.String())
	}
}

func TestRunRefusesBranchBehind(t *testing.T) {
	t.Parallel()
	e := newRecordedEnv(t)
	gitCmd(t, e.root, "reset", "--quiet", "--hard", "HEAD~1")
	e.assertDriftRefusal(t, "M-1 branch ralph/oidc-login is 1 commit behind its recorded head")
}

func TestRunRefusesMergedBranch(t *testing.T) {
	t.Parallel()
	e := newRecordedEnv(t)
	gitCmd(t, e.root, "switch", "--quiet", "main")
	gitCmd(t, e.root, "merge", "--quiet", "--ff-only", "ralph/oidc-login")
	e.assertDriftRefusal(t, "M-1 branch ralph/oidc-login is already merged into main")
}

func TestRunRefusesMissingRecordedBranch(t *testing.T) {
	t.Parallel()
	e := newRunEnv(t, "a")
	stale := strings.Repeat("ab", 20)
	if err := e.svc.RecordMilestoneGit(context.Background(), e.mID, domain.MilestoneGit{Branch: "gone/branch", BaseCommit: stale, HeadCommit: stale}); err != nil {
		t.Fatal(err)
	}
	e.assertDriftRefusal(t, "M-1 branch gone/branch does not exist locally")
	if list := gitCmd(t, e.root, "branch", "--list", "ralph/oidc-login"); list != "" {
		t.Errorf("template branch was created: %q", list)
	}
}

func TestRunAdoptsCommitsAheadOfRecordedHead(t *testing.T) {
	t.Parallel()
	e := newRecordedEnv(t)
	record := e.milestone(t)
	e.commitFile(t, "manual.txt")
	tip := gitCmd(t, e.root, "rev-parse", "HEAD")
	h := &fakeHarness{script: func(_ int, id int64, _ harness.Request) (harness.Result, error) {
		if got := e.milestone(t).HeadCommit; got != tip {
			t.Errorf("recorded head during the session = %q, want the tip %q", got, tip)
		}
		return e.complete(id)
	}}
	out, err := e.run(t, context.Background(), h)
	if err != nil || out.StopReason != StopCompleted || h.calls != 1 {
		t.Fatalf("out = %+v, err = %v\n%s", out, err, e.out.String())
	}
	want := "! M-1 branch has 1 commit(s) beyond the recorded head:\n  " + tip[:12] + " add manual.txt\n"
	if !strings.Contains(e.out.String(), want) {
		t.Errorf("feed lacks %q:\n%s", want, e.out.String())
	}
	if head := e.assertHead(t); head == tip || gitCmd(t, e.root, "rev-parse", "HEAD~1") != tip {
		t.Errorf("head %s should be T-2's commit on top of %s", head, tip)
	}
	if m := e.milestone(t); m.Branch != record.Branch || m.BaseCommit != record.BaseCommit {
		t.Errorf("record = %q base %q, want %q base %q", m.Branch, m.BaseCommit, record.Branch, record.BaseCommit)
	}
}

func newUnstartedEnv(t *testing.T) *runEnv {
	t.Helper()
	e := newRunEnv(t, "a")
	base := gitCmd(t, e.root, "rev-parse", "HEAD")
	gitCmd(t, e.root, "branch", "ralph/oidc-login")
	if err := e.svc.RecordMilestoneGit(context.Background(), e.mID, domain.MilestoneGit{Branch: "ralph/oidc-login", BaseCommit: base, HeadCommit: base}); err != nil {
		t.Fatal(err)
	}
	return e
}

func TestRunRerecordsUnstartedBranchMovedToBase(t *testing.T) {
	t.Parallel()
	e := newUnstartedEnv(t)
	e.commitFile(t, "main-moves-on.txt")
	tip := gitCmd(t, e.root, "rev-parse", "HEAD")
	gitCmd(t, e.root, "branch", "--force", "ralph/oidc-login", "main")
	h := &fakeHarness{script: func(_ int, id int64, _ harness.Request) (harness.Result, error) { return e.complete(id) }}
	out, err := e.run(t, context.Background(), h)
	if err != nil || out.StopReason != StopCompleted || h.calls != 1 {
		t.Fatalf("out = %+v, err = %v\n%s", out, err, e.out.String())
	}
	want := "! M-1 branch ralph/oidc-login was moved to " + tip[:10] + ", which is in main; recording it as the base\n"
	if !strings.Contains(e.out.String(), want) {
		t.Errorf("feed lacks %q:\n%s", want, e.out.String())
	}
	if strings.Contains(e.out.String(), "beyond the recorded head") {
		t.Errorf("feed has the ahead warning:\n%s", e.out.String())
	}
	if m := e.milestone(t); m.Branch != "ralph/oidc-login" || m.BaseCommit != tip {
		t.Errorf("record = %q base %q, want ralph/oidc-login base %q", m.Branch, m.BaseCommit, tip)
	}
}

func TestRunRerecordsMovedBranchBeforeAnySession(t *testing.T) {
	t.Parallel()
	e := newUnstartedEnv(t)
	e.commitFile(t, "main-moves-on.txt")
	tip := gitCmd(t, e.root, "rev-parse", "HEAD")
	gitCmd(t, e.root, "branch", "--force", "ralph/oidc-login", "main")
	h := &fakeHarness{script: func(_ int, id int64, _ harness.Request) (harness.Result, error) {
		if m := e.milestone(t); m.BaseCommit != tip || m.HeadCommit != tip {
			t.Errorf("during the session base %q head %q, want %q", m.BaseCommit, m.HeadCommit, tip)
		}
		return e.complete(id)
	}}
	if _, err := e.run(t, context.Background(), h); err != nil {
		t.Fatalf("%v\n%s", err, e.out.String())
	}
}

func TestRunUnstartedBranchWithOwnCommitRecordsHeadOnly(t *testing.T) {
	t.Parallel()
	e := newUnstartedEnv(t)
	record := e.milestone(t)
	gitCmd(t, e.root, "switch", "--quiet", "ralph/oidc-login")
	e.commitFile(t, "manual.txt")
	tip := gitCmd(t, e.root, "rev-parse", "HEAD")
	h := &fakeHarness{script: func(_ int, id int64, _ harness.Request) (harness.Result, error) {
		if got := e.milestone(t).HeadCommit; got != tip {
			t.Errorf("recorded head during the session = %q, want %q", got, tip)
		}
		return e.complete(id)
	}}
	if _, err := e.run(t, context.Background(), h); err != nil {
		t.Fatalf("%v\n%s", err, e.out.String())
	}
	if !strings.Contains(e.out.String(), "beyond the recorded head") || strings.Contains(e.out.String(), "recording it as the base") {
		t.Errorf("feed:\n%s", e.out.String())
	}
	if m := e.milestone(t); m.BaseCommit != record.BaseCommit {
		t.Errorf("base = %q, want unchanged %q", m.BaseCommit, record.BaseCommit)
	}
}

func TestRunResumesUnchangedBranch(t *testing.T) {
	t.Parallel()
	e := newRecordedEnv(t)
	record := e.milestone(t)
	gitCmd(t, e.root, "switch", "--quiet", "main")
	h := &fakeHarness{script: func(_ int, id int64, _ harness.Request) (harness.Result, error) { return e.complete(id) }}
	out, err := e.run(t, context.Background(), h)
	if err != nil || out.StopReason != StopCompleted || len(out.Commits) != 1 {
		t.Fatalf("out = %+v, err = %v\n%s", out, err, e.out.String())
	}
	feed := e.out.String()
	if !strings.HasPrefix(feed, "switching to ralph/oidc-login\n▶ [1/10] T-2 b\n") || strings.Contains(feed, "!") || strings.Contains(feed, "reconcile") {
		t.Errorf("feed:\n%s", feed)
	}
	if b := gitCmd(t, e.root, "branch", "--show-current"); b != "ralph/oidc-login" {
		t.Errorf("branch = %q", b)
	}
	if head := e.assertHead(t); gitCmd(t, e.root, "rev-parse", "HEAD~1") != record.HeadCommit || head != e.status(t, 2).Commit {
		t.Errorf("head %s should be T-2's commit on top of %s", head, record.HeadCommit)
	}
	if m := e.milestone(t); m.Branch != record.Branch || m.BaseCommit != record.BaseCommit {
		t.Errorf("record = %q base %q, want %q base %q", m.Branch, m.BaseCommit, record.Branch, record.BaseCommit)
	}
}

func TestPlanRecordsNothing(t *testing.T) {
	t.Parallel()
	e := newRunEnv(t, "a")
	gitCmd(t, e.root, "branch", "ralph/oidc-login")
	plan, err := Planner{Service: e.svc, Config: e.cfg, Root: e.root}.Plan(context.Background(), e.mID)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Branch != "ralph/oidc-login" {
		t.Errorf("plan branch = %q", plan.Branch)
	}
	if m := e.milestone(t); m.Branch != "" || m.BaseCommit != "" || m.HeadCommit != "" {
		t.Errorf("planning recorded %q base %q head %q", m.Branch, m.BaseCommit, m.HeadCommit)
	}
	if b := gitCmd(t, e.root, "branch", "--show-current"); b != "main" {
		t.Errorf("planning switched to %q", b)
	}
}

func TestRunNamesBranchAfterCardAndKeepsIt(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	e := newRunEnv(t, "a")
	e.cfg.TaskManager.Provider = config.TaskManagerJira
	e.cfg.Loop.BranchTemplate = "{{.Milestone.CardKeyPrefix}}{{.Milestone.Slug}}"
	if err := e.svc.RecordCard(ctx, domain.KindMilestone, e.mID, "jira", "ABC-1234", "", ""); err != nil {
		t.Fatal(err)
	}
	planner := Planner{Service: e.svc, Config: e.cfg, Root: e.root}
	plan, err := planner.Plan(ctx, e.mID)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Branch != "ABC-1234-oidc-login" {
		t.Fatalf("plan branch = %q", plan.Branch)
	}
	in, err := DriftInput(ctx, e.svc, e.cfg, e.root, e.mID)
	if err != nil {
		t.Fatal(err)
	}
	if in.TemplateBranch != plan.Branch {
		t.Errorf("drift branch = %q, plan branch = %q", in.TemplateBranch, plan.Branch)
	}
	for _, line := range strings.Split(plan.FirstPrompt, "\n") {
		if strings.Contains(line, "ABC-1234") && !strings.HasPrefix(line, "Branch: ") {
			t.Errorf("card key leaked into prompt line %q", line)
		}
	}

	var prompts []string
	if _, err := (Runner{Service: e.svc, Config: e.cfg, Root: e.root, Harness: e.blocker(&prompts), Out: &e.out}).Run(ctx, plan); !errors.Is(err, errs.ErrNeedsHuman) {
		t.Fatalf("err = %v\n%s", err, e.out.String())
	}
	if m := e.milestone(t); m.Branch != "ABC-1234-oidc-login" {
		t.Fatalf("recorded branch = %q", m.Branch)
	}
	if gitCmd(t, e.root, "branch", "--list", "ABC-1234-oidc-login") == "" {
		t.Error("the card-named branch should exist")
	}

	if err := e.svc.RecordCard(ctx, domain.KindMilestone, e.mID, "jira", "ABC-9999", "", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.SetTaskStatus(ctx, 1, domain.StatusTodo, ""); err != nil {
		t.Fatal(err)
	}
	plan, err = planner.Plan(ctx, e.mID)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Branch != "ABC-1234-oidc-login" {
		t.Errorf("plan branch after key change = %q", plan.Branch)
	}
	e.out.Reset()
	if _, err := (Runner{Service: e.svc, Config: e.cfg, Root: e.root, Harness: e.blocker(&prompts), Out: &e.out}).Run(ctx, plan); !errors.Is(err, errs.ErrNeedsHuman) {
		t.Fatalf("second run err = %v\n%s", err, e.out.String())
	}
	if list := gitCmd(t, e.root, "branch", "--list", "ABC-9999*"); list != "" {
		t.Errorf("second branch created: %q", list)
	}
	if list := gitCmd(t, e.root, "branch", "--list", "ABC-*"); strings.Count(list, "\n") != 0 {
		t.Errorf("branches: %q", list)
	}
}

func newStaleUnstartedEnv(t *testing.T) (e *runEnv, base, tip string) {
	t.Helper()
	e = newUnstartedEnv(t)
	base = e.milestone(t).BaseCommit
	e.commitFile(t, "main-moves-on.txt")
	tip = gitCmd(t, e.root, "rev-parse", "HEAD")
	return e, base, tip
}

func (e *runEnv) assertFastForwarded(t *testing.T, tip string) {
	t.Helper()
	if got := gitCmd(t, e.root, "rev-parse", "ralph/oidc-login~1"); got != tip {
		t.Errorf("branch parent = %s, want the base tip %s", got, tip)
	}
	if merges := gitCmd(t, e.root, "rev-list", "--merges", "--count", "main..ralph/oidc-login"); merges != "0" {
		t.Errorf("merge commits = %s", merges)
	}
	want := "! M-1 branch ralph/oidc-login had no work of its own and was behind main; fast-forwarded to " + tip[:10] + "\n"
	if !strings.Contains(e.out.String(), want) {
		t.Errorf("feed lacks %q:\n%s", want, e.out.String())
	}
}

func TestRunFastForwardsStaleUnstartedBranch(t *testing.T) {
	t.Parallel()
	for _, onBranch := range []bool{false, true} {
		e, _, tip := newStaleUnstartedEnv(t)
		if onBranch {
			gitCmd(t, e.root, "switch", "--quiet", "ralph/oidc-login")
		}
		h := &fakeHarness{script: func(_ int, id int64, _ harness.Request) (harness.Result, error) {
			if m := e.milestone(t); m.BaseCommit != tip || m.HeadCommit != tip {
				t.Errorf("during the session base %q head %q, want %q", m.BaseCommit, m.HeadCommit, tip)
			}
			if got := gitCmd(t, e.root, "rev-parse", "HEAD"); got != tip {
				t.Errorf("HEAD during the session = %s, want %s", got, tip)
			}
			return e.complete(id)
		}}
		if _, err := e.run(t, context.Background(), h); err != nil {
			t.Fatalf("onBranch=%v: %v\n%s", onBranch, err, e.out.String())
		}
		e.assertFastForwarded(t, tip)
	}
}

func TestRunDoesNotFastForwardInDirtyTree(t *testing.T) {
	t.Parallel()
	e, base, tip := newStaleUnstartedEnv(t)
	gitCmd(t, e.root, "switch", "--quiet", "ralph/oidc-login")
	if err := os.WriteFile(filepath.Join(e.root, "scratch.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	h := &fakeHarness{script: func(_ int, id int64, _ harness.Request) (harness.Result, error) {
		if got := gitCmd(t, e.root, "rev-parse", "HEAD"); got != base {
			t.Errorf("HEAD during the session = %s, want unmoved %s", got, base)
		}
		return e.complete(id)
	}}
	if _, err := e.run(t, context.Background(), h); err != nil {
		t.Fatalf("%v\n%s", err, e.out.String())
	}
	if !strings.Contains(e.out.String(), "uncommitted changes; not fast-forwarding") || strings.Contains(e.out.String(), "fast-forwarded to") {
		t.Errorf("feed:\n%s", e.out.String())
	}
	if m := e.milestone(t); m.BaseCommit != base || m.BaseCommit == tip {
		t.Errorf("base = %q, want unchanged %q", m.BaseCommit, base)
	}
}

func TestRunDoesNotFastForwardBranchWithOwnCommit(t *testing.T) {
	t.Parallel()
	e, base, _ := newStaleUnstartedEnv(t)
	gitCmd(t, e.root, "switch", "--quiet", "ralph/oidc-login")
	e.commitFile(t, "manual.txt")
	h := &fakeHarness{script: func(_ int, id int64, _ harness.Request) (harness.Result, error) { return e.complete(id) }}
	if _, err := e.run(t, context.Background(), h); err != nil {
		t.Fatalf("%v\n%s", err, e.out.String())
	}
	if strings.Contains(e.out.String(), "fast-forward") {
		t.Errorf("feed:\n%s", e.out.String())
	}
	if m := e.milestone(t); m.BaseCommit != base {
		t.Errorf("base = %q, want unchanged %q", m.BaseCommit, base)
	}
}

func TestRunDoesNotFastForwardStartedMilestone(t *testing.T) {
	t.Parallel()
	e := newRecordedEnv(t)
	record := e.milestone(t)
	gitCmd(t, e.root, "switch", "--quiet", "main")
	e.commitFile(t, "main-moves-on.txt")
	h := &fakeHarness{script: func(_ int, id int64, _ harness.Request) (harness.Result, error) { return e.complete(id) }}
	if _, err := e.run(t, context.Background(), h); err != nil {
		t.Fatalf("%v\n%s", err, e.out.String())
	}
	if strings.Contains(e.out.String(), "fast-forward") {
		t.Errorf("feed:\n%s", e.out.String())
	}
	if m := e.milestone(t); m.BaseCommit != record.BaseCommit {
		t.Errorf("base = %q, want unchanged %q", m.BaseCommit, record.BaseCommit)
	}
}

func TestRunUnstartedBranchAtBaseTipIsLeftAlone(t *testing.T) {
	t.Parallel()
	e := newUnstartedEnv(t)
	record := e.milestone(t)
	h := &fakeHarness{script: func(_ int, id int64, _ harness.Request) (harness.Result, error) { return e.complete(id) }}
	if _, err := e.run(t, context.Background(), h); err != nil {
		t.Fatalf("%v\n%s", err, e.out.String())
	}
	if strings.Contains(e.out.String(), "fast-forward") || strings.Contains(e.out.String(), "!") {
		t.Errorf("feed:\n%s", e.out.String())
	}
	if m := e.milestone(t); m.BaseCommit != record.BaseCommit {
		t.Errorf("base = %q, want unchanged %q", m.BaseCommit, record.BaseCommit)
	}
}

// failInProgress makes the nth SetStatus to IN_PROGRESS return err. The task's
// card is pushed before its milestone's, so n picks which one fails.
func failInProgress(p *tmfake.Provider, n int, err error) {
	seen := 0
	p.SetHook(func(_ int, method string, req taskmanager.CardRequest) error {
		if method != "SetStatus" || req.Status != "IN_PROGRESS" {
			return nil
		}
		seen++
		if seen == n {
			return err
		}
		return nil
	})
}

func TestRunBlocksAndStopsOnAssigneeConflict(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name     string
		nth      int
		resource string
	}{
		{"task card", 1, "T-1"},
		{"milestone card", 2, "M-1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := tmfake.New()
			e := newRunEnvWith(t, p, "first", "second")
			failInProgress(p, tc.nth, &taskmanager.AssigneeConflictError{Key: "AIN-12", Assignee: "Pat Lee"})
			h := &fakeHarness{script: func(_ int, id int64, _ harness.Request) (harness.Result, error) { return e.complete(id) }}
			out, err := e.run(t, context.Background(), h)
			if !errors.Is(err, errs.ErrNeedsHuman) || out.StopReason != StopNeedsHuman {
				t.Fatalf("err = %v, stop = %s\n%s", err, out.StopReason, e.out.String())
			}
			t1 := e.status(t, 1)
			if t1.Status != "BLOCKED" {
				t.Fatalf("T-1 = %+v", t1)
			}
			for _, want := range []string{"AIN-12", "Pat Lee", "(" + tc.resource + ")"} {
				if !strings.Contains(t1.BlockedReason, want) {
					t.Errorf("BlockedReason %q lacks %q", t1.BlockedReason, want)
				}
			}
			noted := false
			for _, n := range t1.Notes {
				noted = noted || (strings.Contains(n.Body, "AIN-12") && strings.Contains(n.Body, "Pat Lee"))
			}
			if !noted {
				t.Errorf("no note names the card and holder: %+v", t1.Notes)
			}
			if len(out.Blocked) != 1 || out.Blocked[0].ID != "T-1" || out.Blocked[0].Reason != t1.BlockedReason {
				t.Errorf("blocked = %+v", out.Blocked)
			}
			if h.calls != 0 || out.Iterations != 0 || t1.FailureCount != 0 {
				t.Errorf("sessions = %d, iterations = %d, failures = %d", h.calls, out.Iterations, t1.FailureCount)
			}
			if got := e.status(t, 2).Status; got != "TODO" {
				t.Errorf("T-2 = %s", got)
			}
			if want := "✗ T-1 BLOCKED: " + t1.BlockedReason; !strings.Contains(e.out.String(), want) {
				t.Errorf("feed lacks %q:\n%s", want, e.out.String())
			}
		})
	}
}

func TestRunBlocksOnMilestoneCardConflictWhenTaskHasNoCard(t *testing.T) {
	t.Parallel()
	p := tmfake.New()
	e := newRunEnvKinds(t, p, []domain.Kind{domain.KindInitiative, domain.KindMilestone}, "first", "second")
	failInProgress(p, 1, &taskmanager.AssigneeConflictError{Key: "AIN-12", Assignee: "Pat Lee"})
	h := &fakeHarness{script: func(_ int, id int64, _ harness.Request) (harness.Result, error) { return e.complete(id) }}
	out, err := e.run(t, context.Background(), h)
	if !errors.Is(err, errs.ErrNeedsHuman) || out.StopReason != StopNeedsHuman {
		t.Fatalf("err = %v, stop = %s\n%s", err, out.StopReason, e.out.String())
	}
	t1 := e.status(t, 1)
	if t1.Status != "BLOCKED" {
		t.Fatalf("T-1 = %+v", t1)
	}
	for _, want := range []string{"AIN-12", "Pat Lee", "(M-1)"} {
		if !strings.Contains(t1.BlockedReason, want) {
			t.Errorf("BlockedReason %q lacks %q", t1.BlockedReason, want)
		}
	}
	if h.calls != 0 || out.Iterations != 0 || t1.FailureCount != 0 {
		t.Errorf("sessions = %d, iterations = %d, failures = %d", h.calls, out.Iterations, t1.FailureCount)
	}
	if got := e.status(t, 2).Status; got != "TODO" {
		t.Errorf("T-2 = %s", got)
	}
}

func TestRunCardFailureWithoutConflictDoesNotStop(t *testing.T) {
	t.Parallel()
	p := tmfake.New()
	e := newRunEnvWith(t, p, "only")
	p.SetError(errors.New("jira is down"))
	h := &fakeHarness{script: func(_ int, id int64, _ harness.Request) (harness.Result, error) { return e.complete(id) }}
	out, err := e.run(t, context.Background(), h)
	if err != nil || out.StopReason != StopCompleted {
		t.Fatalf("err = %v, stop = %s\n%s", err, out.StopReason, e.out.String())
	}
	if h.calls != 1 || e.status(t, 1).Status != "DONE" || len(out.Blocked) != 0 {
		t.Errorf("sessions = %d, blocked = %+v", h.calls, out.Blocked)
	}
}
