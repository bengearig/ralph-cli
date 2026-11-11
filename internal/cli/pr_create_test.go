package cli

import (
	"errors"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bengearig/ralph/internal/config"
	"github.com/bengearig/ralph/internal/domain"
	"github.com/bengearig/ralph/internal/errs"
	"github.com/bengearig/ralph/internal/pr"
	"github.com/bengearig/ralph/internal/service"
)

type prCreateFixture struct {
	h      *testEnv
	fake   *fakeProvider
	svc    *service.Service
	git    func(args ...string) string
	remote string
}

func addBareRemote(t *testing.T, git func(args ...string) string, name string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), name+".git")
	if out, err := exec.Command("git", "init", "--quiet", "--bare", dir).CombinedOutput(); err != nil {
		t.Fatalf("git init --bare: %v\n%s", err, out)
	}
	git("remote", "add", name, dir)
	return dir
}

func remoteHasBranch(t *testing.T, bare, branch string) bool {
	t.Helper()
	out, err := exec.Command("git", "-C", bare, "branch", "--list", branch).CombinedOutput()
	if err != nil {
		t.Fatalf("git branch --list: %v\n%s", err, out)
	}
	return strings.TrimSpace(string(out)) != ""
}

func newPRCreateFixture(t *testing.T) *prCreateFixture {
	t.Helper()
	h, git, commit := newGitTestEnv(t)
	base := git("rev-parse", "HEAD")
	remote := addBareRemote(t, git, "origin")
	h.ok("initiative", "create", "--title", "SSO")
	h.ok("milestone", "create", "--initiative", "I-1", "--title", "Login")
	h.ok("task", "create", "--milestone", "M-1", "--title", "Route", "--description", "d", "--criteria", "c")
	git("switch", "--quiet", "-c", "ralph/login")
	h.ok("task", "status", "T-1", "TODO")
	h.ok("task", "status", "T-1", "IN_PROGRESS")
	h.ok("task", "status", "T-1", "DONE", "--reason", "done")
	head := commit("T-1.txt", "T-1: work\n\nRalph-Task: T-1")
	cfg, err := config.Defaults()
	if err != nil {
		t.Fatal(err)
	}
	cfg.Embeddings.Provider = config.EmbeddingsNone
	svc, closeFn, err := service.Open(t.Context(), cfg, h.dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = closeFn() })
	if err := svc.RecordMilestoneGit(t.Context(), 1, domain.MilestoneGit{Branch: "ralph/login", BaseCommit: base, HeadCommit: head}); err != nil {
		t.Fatal(err)
	}
	if err := svc.RecordTaskCommit(t.Context(), 1, head); err != nil {
		t.Fatal(err)
	}
	fake := &fakeProvider{}
	newPRProvider = func(*config.Config) (pr.Provider, error) { return fake, nil }
	t.Cleanup(func() { newPRProvider = pr.New })
	return &prCreateFixture{h, fake, svc, git, remote}
}

func (f *prCreateFixture) recorded(t *testing.T) domain.PullRequestRecord {
	t.Helper()
	m, err := f.svc.GetMilestone(t.Context(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if m.PullRequest == nil {
		return domain.PullRequestRecord{}
	}
	return domain.PullRequestRecord{Provider: m.PullRequest.Provider, Namespace: m.PullRequest.Namespace, Repository: m.PullRequest.Repository, Number: m.PullRequest.Number}
}

func TestPRCreateOpensAndRecords(t *testing.T) {
	f := newPRCreateFixture(t)
	out := f.h.ok("pr", "create", "--milestone", "M-1")
	if out != "opened https://example.com/pr/1\n" || f.fake.created != 1 {
		t.Fatalf("create: %q, created %d", out, f.fake.created)
	}
	if f.fake.finds != 1 || f.fake.findArgs != [2]string{"ralph/login", "main"} {
		t.Errorf("FindOpen calls = %d, args = %v", f.fake.finds, f.fake.findArgs)
	}
	if f.fake.createReq.SourceBranch != "ralph/login" || f.fake.createReq.DestinationBranch != "main" {
		t.Errorf("Create request = %+v", f.fake.createReq)
	}
	want := domain.PullRequestRecord{Provider: "fake", Namespace: "acme", Repository: "sso", Number: 1}
	if got := f.recorded(t); got != want {
		t.Errorf("recorded = %+v, want %+v", got, want)
	}
	if show := f.h.ok("milestone", "show", "M-1"); !strings.Contains(show, "fake acme/sso#1") {
		t.Errorf("milestone show lacks the pull request:\n%s", show)
	}

	out = f.h.ok("pr", "create", "--milestone", "M-1")
	if out != "exists fake acme/sso#1\n" || f.fake.created != 1 || f.fake.finds != 1 {
		t.Errorf("second run: %q, created %d, finds %d", out, f.fake.created, f.fake.finds)
	}
	var res struct {
		Outcome     string `json:"outcome"`
		Pushed      bool   `json:"pushed"`
		Remote      string `json:"remote"`
		PullRequest struct {
			Provider, Namespace, Repository string
			Number                          int
		} `json:"pull_request"`
	}
	f.h.json(&res, "pr", "create", "--milestone", "M-1")
	if res.Outcome != "exists" || res.Pushed || res.Remote != "" || res.PullRequest.Number != 1 || res.PullRequest.Provider != "fake" {
		t.Errorf("exists JSON = %+v", res)
	}
	if f.fake.created != 1 || f.fake.finds != 1 {
		t.Errorf("exists made provider calls: created %d, finds %d", f.fake.created, f.fake.finds)
	}
}

func TestPRCreateJSONOpened(t *testing.T) {
	f := newPRCreateFixture(t)
	var res struct {
		Outcome     string `json:"outcome"`
		PullRequest struct {
			URL    string `json:"url"`
			Number int    `json:"number"`
		} `json:"pull_request"`
	}
	f.h.json(&res, "pr", "create", "--milestone", "M-1")
	if res.Outcome != "opened" || res.PullRequest.URL != "https://example.com/pr/1" || res.PullRequest.Number != 1 {
		t.Errorf("opened JSON = %+v", res)
	}
}

func TestPRCreateAdoptsOpenPullRequest(t *testing.T) {
	f := newPRCreateFixture(t)
	f.fake.found = pr.PullRequest{Namespace: "acme", Repository: "web", Number: 7, URL: "https://example.com/pr/7"}
	f.fake.foundOK = true
	out := f.h.ok("pr", "create", "--milestone", "M-1")
	if out != "adopted https://example.com/pr/7\n" || f.fake.created != 0 {
		t.Fatalf("adopt: %q, created %d", out, f.fake.created)
	}
	want := domain.PullRequestRecord{Provider: "fake", Namespace: "acme", Repository: "web", Number: 7}
	if got := f.recorded(t); got != want {
		t.Errorf("recorded = %+v, want %+v", got, want)
	}
	if show := f.h.ok("milestone", "show", "M-1"); !strings.Contains(show, "fake acme/web#7") {
		t.Errorf("milestone show lacks the pull request:\n%s", show)
	}
}

func TestPRCreateProviderFailuresRecordNothing(t *testing.T) {
	f := newPRCreateFixture(t)
	f.fake.createError = errors.New("create exploded")
	if _, _, err := f.h.run("", "pr", "create", "--milestone", "M-1"); err == nil || !strings.Contains(err.Error(), "create exploded") {
		t.Errorf("create failure: err = %v", err)
	}
	if !f.recorded(t).IsZero() {
		t.Error("a failed Create recorded a pull request")
	}
	f.fake.createError = nil
	f.fake.findError = errors.New("find exploded")
	created := f.fake.created
	if _, _, err := f.h.run("", "pr", "create", "--milestone", "M-1"); err == nil || !strings.Contains(err.Error(), "find exploded") {
		t.Errorf("find failure: err = %v", err)
	}
	if !f.recorded(t).IsZero() || f.fake.created != created {
		t.Error("a failed FindOpen recorded a pull request or created one")
	}
}

func TestPRCreateRecordingFailureReportsLink(t *testing.T) {
	f := newPRCreateFixture(t)
	f.fake.onCreate = func() { f.h.ok("initiative", "archive", "I-1") }
	out, _, err := f.h.run("", "pr", "create", "--milestone", "M-1")
	if out != "opened https://example.com/pr/1\n" {
		t.Errorf("output = %q", out)
	}
	if err == nil || !strings.Contains(err.Error(), "could not be recorded") || !strings.Contains(err.Error(), "rerunning pr create adopts it") {
		t.Errorf("err = %v", err)
	}
}

func TestPRCreateDryRunRecorded(t *testing.T) {
	f := newPRCreateFixture(t)
	out := f.h.ok("pr", "create", "--milestone", "M-1", "--dry-run")
	if strings.Contains(out, "pull request:") {
		t.Errorf("dry run without a record mentions one:\n%s", out)
	}
	f.h.ok("pr", "create", "--milestone", "M-1")
	finds, created := f.fake.finds, f.fake.created
	out = f.h.ok("pr", "create", "--milestone", "M-1", "--dry-run")
	if want := "pull request: https://example.com/pr/1 (already recorded; nothing would be pushed or opened)\n"; !strings.Contains(out, want) && !strings.Contains(out, strings.Replace(want, "https://example.com/pr/1", "fake acme/sso#1", 1)) {
		t.Errorf("dry run lacks the recorded line:\n%s", out)
	}
	var plan struct {
		PullRequest *struct {
			Number int `json:"number"`
		} `json:"pull_request"`
	}
	f.h.json(&plan, "pr", "create", "--milestone", "M-1", "--dry-run")
	if plan.PullRequest == nil || plan.PullRequest.Number != 1 {
		t.Errorf("dry run JSON pull_request = %+v", plan.PullRequest)
	}
	if f.fake.finds != finds || f.fake.created != created {
		t.Error("dry run called the provider API")
	}
}

func TestPRCreatePushesBeforeFindingAndCreating(t *testing.T) {
	f := newPRCreateFixture(t)
	f.fake.onFind = func() {
		if !remoteHasBranch(t, f.remote, "ralph/login") {
			t.Error("FindOpen ran before the branch was pushed")
		}
	}
	f.fake.onCreate = func() {
		if !remoteHasBranch(t, f.remote, "ralph/login") {
			t.Error("Create ran before the branch was pushed")
		}
	}
	var res struct {
		Outcome string `json:"outcome"`
		Pushed  bool   `json:"pushed"`
		Remote  string `json:"remote"`
	}
	f.h.json(&res, "pr", "create", "--milestone", "M-1")
	if res.Outcome != "opened" || !res.Pushed || res.Remote != "origin" {
		t.Errorf("JSON = %+v", res)
	}
	if !remoteHasBranch(t, f.remote, "ralph/login") || f.fake.finds != 1 || f.fake.created != 1 {
		t.Errorf("remote has branch %t, finds %d, created %d", remoteHasBranch(t, f.remote, "ralph/login"), f.fake.finds, f.fake.created)
	}
}

func TestPRCreateSecondRunPushesNothing(t *testing.T) {
	f := newPRCreateFixture(t)
	f.fake.createError = errors.New("create exploded")
	if _, _, err := f.h.run("", "pr", "create", "--milestone", "M-1"); err == nil {
		t.Fatal("expected the create failure")
	}
	f.fake.createError = nil
	var res struct {
		Outcome string `json:"outcome"`
		Pushed  bool   `json:"pushed"`
		Remote  string `json:"remote"`
	}
	f.h.json(&res, "pr", "create", "--milestone", "M-1")
	if res.Outcome != "opened" || res.Pushed || res.Remote != "origin" {
		t.Errorf("JSON = %+v", res)
	}
	if f.fake.finds != 2 || f.fake.created != 2 {
		t.Errorf("finds %d, created %d", f.fake.finds, f.fake.created)
	}
}

func TestPRCreateFailedPushExitsThreeAndCallsNoProvider(t *testing.T) {
	f := newPRCreateFixture(t)
	f.git("push", "--quiet", "origin", "ralph/login")
	other := filepath.Join(t.TempDir(), "other")
	if out, err := exec.Command("git", "clone", "--quiet", f.remote, other).CombinedOutput(); err != nil {
		t.Fatalf("clone: %v\n%s", err, out)
	}
	for _, args := range [][]string{
		{"-C", other, "switch", "--quiet", "ralph/login"},
		{"-C", other, "commit", "--quiet", "--allow-empty", "-m", "elsewhere"},
		{"-C", other, "push", "--quiet", "origin", "ralph/login"},
	} {
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	f.git("remote", "add", "unreachable", filepath.Join(t.TempDir(), "nowhere.git"))

	for _, c := range []struct {
		name, remote, want string
	}{
		{"rejected", "origin", "rejected"},
		{"missing remote", "nonesuch", "nonesuch"},
		{"unreachable", "unreachable", "does not appear to be a git repository"},
	} {
		t.Setenv("RALPH_PR__REMOTE", c.remote)
		_, _, err := f.h.run("", "pr", "create", "--milestone", "M-1")
		if !errors.Is(err, errs.ErrNeedsHuman) || !strings.Contains(err.Error(), c.want) ||
			!strings.Contains(err.Error(), "fix the push, then rerun: ralph pr create --milestone M-1") {
			t.Errorf("%s: err = %v", c.name, err)
		}
		if f.fake.finds != 0 || f.fake.created != 0 || !f.recorded(t).IsZero() {
			t.Errorf("%s: finds %d, created %d, recorded %+v", c.name, f.fake.finds, f.fake.created, f.recorded(t))
		}
	}
}

func TestPRCreatePushesToConfiguredRemote(t *testing.T) {
	f := newPRCreateFixture(t)
	upstream := addBareRemote(t, f.git, "upstream")
	t.Setenv("RALPH_PR__REMOTE", "upstream")
	var res struct {
		Pushed bool   `json:"pushed"`
		Remote string `json:"remote"`
	}
	f.h.json(&res, "pr", "create", "--milestone", "M-1")
	if !res.Pushed || res.Remote != "upstream" || !remoteHasBranch(t, upstream, "ralph/login") || remoteHasBranch(t, f.remote, "ralph/login") {
		t.Errorf("JSON = %+v; upstream has branch %t, origin has branch %t", res, remoteHasBranch(t, upstream, "ralph/login"), remoteHasBranch(t, f.remote, "ralph/login"))
	}
}

func TestPRCreateDryRunPushesNothing(t *testing.T) {
	f := newPRCreateFixture(t)
	out := f.h.ok("pr", "create", "--milestone", "M-1", "--dry-run")
	if !strings.Contains(out, "push:     origin ralph/login (not pushed in a dry run)\n") {
		t.Errorf("dry run lacks the push line:\n%s", out)
	}
	var plan struct {
		Push struct {
			Remote string `json:"remote"`
			Branch string `json:"branch"`
		} `json:"push"`
	}
	f.h.json(&plan, "pr", "create", "--milestone", "M-1", "--dry-run")
	if plan.Push.Remote != "origin" || plan.Push.Branch != "ralph/login" {
		t.Errorf("dry run JSON push = %+v", plan.Push)
	}
	if remoteHasBranch(t, f.remote, "ralph/login") {
		t.Error("dry run pushed the branch")
	}
	if f.fake.finds != 0 || f.fake.created != 0 {
		t.Error("dry run called the provider API")
	}
}

func TestPRCreateRecordedPushesNothing(t *testing.T) {
	f := newPRCreateFixture(t)
	f.h.ok("pr", "create", "--milestone", "M-1")
	f.git("push", "--quiet", "origin", "--delete", "ralph/login")
	var res struct {
		Outcome string `json:"outcome"`
		Pushed  bool   `json:"pushed"`
	}
	f.h.json(&res, "pr", "create", "--milestone", "M-1")
	if res.Outcome != "exists" || res.Pushed || remoteHasBranch(t, f.remote, "ralph/login") {
		t.Errorf("JSON = %+v", res)
	}
}
