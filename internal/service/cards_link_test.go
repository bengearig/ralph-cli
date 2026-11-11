package service

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/bengearig/ralph/internal/domain"
	"github.com/bengearig/ralph/internal/taskmanager"
	tmfake "github.com/bengearig/ralph/internal/taskmanager/fake"
)

func linkURL(provider, namespace, repository string, number int) (string, bool) {
	return "https://bitbucket.test/" + namespace + "/" + repository + "/pull-requests/7", true
}

var linkRecord = domain.PullRequestRecord{Provider: "bitbucket_cloud", Namespace: "acme", Repository: "widgets", Number: 7}

func linkEnv(t *testing.T, key string) (env, *tmfake.Provider, int64) {
	t.Helper()
	e := newEnv(t)
	_, m := e.scaffold(t)
	e, p := withProvider(e)
	e.svc.SetPullRequestLink(linkURL)
	if err := e.svc.RecordMilestonePullRequest(context.Background(), m, linkRecord); err != nil {
		t.Fatal(err)
	}
	if key != "" {
		if err := e.svc.RecordCard(context.Background(), domain.KindMilestone, m, "fake", key, "https://fake.example/"+key, "h"); err != nil {
			t.Fatal(err)
		}
	}
	return e, p, m
}

func TestLinkMilestonePullRequest(t *testing.T) {
	e, p, m := linkEnv(t, "AIN-1")
	if w := e.svc.LinkMilestonePullRequest(context.Background(), m); w != "" {
		t.Fatal(w)
	}
	want := []tmfake.LinkCall{{Key: "AIN-1", Link: taskmanager.PullRequestLink{
		ID: "ralph-pr:bitbucket_cloud:acme/widgets#7", Title: "Pull request widgets#7",
		URL: "https://bitbucket.test/acme/widgets/pull-requests/7"}}}
	got := p.Links()
	if len(got) != 1 || got[0] != want[0] {
		t.Errorf("links = %+v, want %+v", got, want)
	}
}

type plainProvider struct{ taskmanager.Provider }

func TestLinkMilestonePullRequestNothingToDo(t *testing.T) {
	ctx := context.Background()

	e, p, m := linkEnv(t, "AIN-1")
	e.svc.taskManager = nil
	if w := e.svc.LinkMilestonePullRequest(ctx, m); w != "" || len(p.Links()) != 0 {
		t.Errorf("no task manager: %q", w)
	}

	e, p, m = linkEnv(t, "AIN-1")
	e.svc.taskManager = plainProvider{p}
	if w := e.svc.LinkMilestonePullRequest(ctx, m); w != "" || len(p.Links()) != 0 {
		t.Errorf("no capability: %q", w)
	}

	e, p, m = linkEnv(t, "AIN-1")
	if _, err := e.svc.ClearMilestonePullRequest(ctx, m); err != nil {
		t.Fatal(err)
	}
	if w := e.svc.LinkMilestonePullRequest(ctx, m); w != "" || len(p.Links()) != 0 {
		t.Errorf("no pull request: %q", w)
	}

	e, p, m = linkEnv(t, "")
	if w := e.svc.LinkMilestonePullRequest(ctx, m); w != "" || len(p.Links()) != 0 {
		t.Errorf("no card: %q", w)
	}

	e, p, m = linkEnv(t, "")
	if err := e.svc.RecordCardFailure(ctx, domain.KindMilestone, m, "fake", errors.New("down")); err != nil {
		t.Fatal(err)
	}
	if w := e.svc.LinkMilestonePullRequest(ctx, m); w != "" || len(p.Links()) != 0 {
		t.Errorf("empty key: %q", w)
	}
}

func TestLinkMilestonePullRequestFailureWritesNothing(t *testing.T) {
	ctx := context.Background()
	e, p, m := linkEnv(t, "AIN-1")
	beforeM, err := e.svc.GetMilestone(ctx, m)
	if err != nil {
		t.Fatal(err)
	}
	beforeC, _ := e.svc.ListCards(ctx, "fake")
	p.SetError(errors.New("boom"))
	w := e.svc.LinkMilestonePullRequest(ctx, m)
	if !strings.Contains(w, "AIN-1") || !strings.Contains(w, "boom") {
		t.Fatalf("warning = %q", w)
	}
	afterM, _ := e.svc.GetMilestone(ctx, m)
	afterC, _ := e.svc.ListCards(ctx, "fake")
	if afterM.UpdatedAt != beforeM.UpdatedAt {
		t.Errorf("updated_at changed")
	}
	if len(afterC) != len(beforeC) || afterC[0] != beforeC[0] {
		t.Errorf("cards changed: %+v -> %+v", beforeC, afterC)
	}
}

func TestLinkMilestonePullRequestNoURL(t *testing.T) {
	ctx := context.Background()
	for name, fn := range map[string]func(string, string, string, int) (string, bool){
		"nil":   nil,
		"empty": func(string, string, string, int) (string, bool) { return "", false },
	} {
		e, p, m := linkEnv(t, "AIN-1")
		e.svc.SetPullRequestLink(fn)
		w := e.svc.LinkMilestonePullRequest(ctx, m)
		if !strings.Contains(w, "no link could be built") || len(p.Links()) != 0 {
			t.Errorf("%s: warning %q, links %d", name, w, len(p.Links()))
		}
	}
}

func TestLinkMilestonePullRequestInTx(t *testing.T) {
	ctx := context.Background()
	e, p, m := linkEnv(t, "AIN-1")
	err := e.svc.InTx(ctx, func(tx *Service) error {
		if w := tx.LinkMilestonePullRequest(ctx, m); w != "" {
			t.Errorf("warning %q", w)
		}
		return nil
	})
	if err != nil || len(p.Links()) != 0 {
		t.Fatalf("%v, links %d", err, len(p.Links()))
	}
}

type blockingLinker struct{ taskmanager.Provider }

func (blockingLinker) LinkPullRequest(ctx context.Context, _ string, _ taskmanager.PullRequestLink) error {
	<-ctx.Done()
	return ctx.Err()
}

func TestLinkMilestonePullRequestTimeout(t *testing.T) {
	e, p, m := linkEnv(t, "AIN-1")
	e.svc.taskManager = blockingLinker{p}
	e.svc.cardTimeout = 50 * time.Millisecond
	start := time.Now()
	w := e.svc.LinkMilestonePullRequest(context.Background(), m)
	if !strings.Contains(w, "AIN-1") || !strings.Contains(w, "deadline") {
		t.Errorf("warning = %q", w)
	}
	if time.Since(start) > 5*time.Second {
		t.Error("not cut off")
	}
}
