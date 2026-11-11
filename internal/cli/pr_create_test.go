package cli

import (
	"errors"
	"strings"
	"testing"

	"github.com/bengearig/ralph/internal/config"
	"github.com/bengearig/ralph/internal/domain"
	"github.com/bengearig/ralph/internal/pr"
	"github.com/bengearig/ralph/internal/service"
)

type prCreateFixture struct {
	h    *testEnv
	fake *fakeProvider
	svc  *service.Service
}

func newPRCreateFixture(t *testing.T) *prCreateFixture {
	t.Helper()
	h, git, commit := newGitTestEnv(t)
	base := git("rev-parse", "HEAD")
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
	return &prCreateFixture{h, fake, svc}
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
