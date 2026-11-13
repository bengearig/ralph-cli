package fake

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/bengearig/ralph/internal/domain"
	"github.com/bengearig/ralph/internal/taskmanager"
)

var ctx = context.Background()

func req(id string) taskmanager.CardRequest {
	return taskmanager.CardRequest{Kind: domain.KindTask, ID: id, Title: "t"}
}

func TestCreateAndUpdateRecorded(t *testing.T) {
	p := New()
	if p.Name() != "fake" || p.Validate() != nil {
		t.Fatal("name or validate wrong")
	}
	c1, err1 := p.Create(ctx, req("T-1"))
	c2, err2 := p.Create(ctx, req("T-2"))
	if err1 != nil || err2 != nil {
		t.Fatal(err1, err2)
	}
	if c1 != (taskmanager.Card{Key: "FAKE-1", URL: "https://fake.example/browse/FAKE-1"}) ||
		c2 != (taskmanager.Card{Key: "FAKE-2", URL: "https://fake.example/browse/FAKE-2"}) {
		t.Errorf("cards = %+v %+v", c1, c2)
	}
	if err := p.Update(ctx, "FAKE-1", req("T-1")); err != nil {
		t.Fatal(err)
	}
	calls := p.Calls()
	want := []Call{
		{"Create", "FAKE-1", req("T-1")},
		{"Create", "FAKE-2", req("T-2")},
		{"Update", "FAKE-1", req("T-1")},
	}
	if len(calls) != len(want) {
		t.Fatalf("calls = %+v", calls)
	}
	for i := range want {
		if calls[i] != want[i] {
			t.Errorf("call %d = %+v, want %+v", i, calls[i], want[i])
		}
	}
}

func TestConfiguredError(t *testing.T) {
	p := New()
	boom := errors.New("boom")
	p.SetError(boom)
	if _, err := p.Create(ctx, req("T-1")); err != boom {
		t.Errorf("Create err = %v", err)
	}
	if err := p.Update(ctx, "K-1", req("T-1")); err != boom {
		t.Errorf("Update err = %v", err)
	}
	if len(p.Calls()) != 0 {
		t.Error("failed calls recorded")
	}
	p.SetError(nil)
	if c, err := p.Create(ctx, req("T-1")); err != nil || c.Key != "FAKE-1" {
		t.Errorf("after clear: %+v, %v", c, err)
	}
}

func TestHookFailsOnlyNthCall(t *testing.T) {
	p := New()
	boom := errors.New("boom")
	p.SetHook(func(n int, _ string, _ taskmanager.CardRequest) error {
		if n == 2 {
			return boom
		}
		return nil
	})
	_, e1 := p.Create(ctx, req("T-1"))
	_, e2 := p.Create(ctx, req("T-2"))
	c3, e3 := p.Create(ctx, req("T-3"))
	if e1 != nil || e2 != boom || e3 != nil {
		t.Fatalf("errs = %v %v %v", e1, e2, e3)
	}
	if c3.Key != "FAKE-2" || len(p.Calls()) != 2 {
		t.Errorf("c3 = %+v, calls = %d", c3, len(p.Calls()))
	}
}

func TestHookFailsByKind(t *testing.T) {
	p := New()
	boom := errors.New("boom")
	p.SetHook(func(_ int, _ string, r taskmanager.CardRequest) error {
		if r.Kind == domain.KindMilestone {
			return boom
		}
		return nil
	})
	if _, err := p.Create(ctx, taskmanager.CardRequest{Kind: domain.KindMilestone}); err != boom {
		t.Errorf("err = %v", err)
	}
	if _, err := p.Create(ctx, req("T-1")); err != nil {
		t.Errorf("err = %v", err)
	}
}

func TestValidateError(t *testing.T) {
	p := New()
	boom := errors.New("boom")
	p.SetValidateError(boom)
	if p.Validate() != boom {
		t.Error("validate error not returned")
	}
}

func TestConcurrent(t *testing.T) {
	p := New()
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = p.Create(ctx, req("T-1"))
			_ = p.Update(ctx, "FAKE-1", req("T-1"))
			_ = p.Calls()
		}()
	}
	wg.Wait()
	if len(p.Calls()) != 100 {
		t.Errorf("calls = %d", len(p.Calls()))
	}
}

func TestSetStatusRecordedInOrder(t *testing.T) {
	p := New()
	if _, err := p.Create(ctx, req("T-1")); err != nil {
		t.Fatal(err)
	}
	if err := p.SetStatus(ctx, "FAKE-1", "IN_PROGRESS"); err != nil {
		t.Fatal(err)
	}
	if err := p.Update(ctx, "FAKE-1", req("T-1")); err != nil {
		t.Fatal(err)
	}
	calls := p.Calls()
	if len(calls) != 3 || calls[0].Method != "Create" || calls[1].Method != "SetStatus" || calls[2].Method != "Update" {
		t.Fatalf("calls = %+v", calls)
	}
	if calls[1].Key != "FAKE-1" || calls[1].Request.Status != "IN_PROGRESS" {
		t.Errorf("SetStatus call = %+v", calls[1])
	}
}

func TestSetStatusFailureInjection(t *testing.T) {
	p := New()
	boom := errors.New("boom")
	p.SetError(boom)
	if err := p.SetStatus(ctx, "K-1", "DONE"); err != boom {
		t.Errorf("err = %v", err)
	}
	p.SetError(nil)
	p.SetHook(func(n int, method string, _ taskmanager.CardRequest) error {
		if method == "SetStatus" && n == 2 {
			return boom
		}
		return nil
	})
	if err := p.SetStatus(ctx, "K-1", "DONE"); err != boom {
		t.Errorf("hook err = %v", err)
	}
	if len(p.Calls()) != 0 {
		t.Errorf("failed calls recorded: %+v", p.Calls())
	}
}

func TestLinkPullRequestRecordedInOrder(t *testing.T) {
	p := New()
	a := taskmanager.PullRequestLink{ID: "ralph-pr:github:o/r#1", Title: "Pull request r#1", URL: "https://example.test/1"}
	b := taskmanager.PullRequestLink{ID: "ralph-pr:github:o/r#2", Title: "Pull request r#2", URL: "https://example.test/2"}
	if err := p.LinkPullRequest(ctx, "K-1", a); err != nil {
		t.Fatal(err)
	}
	if err := p.LinkPullRequest(ctx, "K-2", b); err != nil {
		t.Fatal(err)
	}
	links := p.Links()
	if len(links) != 2 || links[0] != (LinkCall{Key: "K-1", Link: a}) || links[1] != (LinkCall{Key: "K-2", Link: b}) {
		t.Fatalf("links = %+v", links)
	}
	if len(p.Calls()) != 0 {
		t.Errorf("link recorded as card call: %+v", p.Calls())
	}
}

func TestLinkPullRequestFailureInjection(t *testing.T) {
	p := New()
	boom := errors.New("boom")
	p.SetError(boom)
	if err := p.LinkPullRequest(ctx, "K-1", taskmanager.PullRequestLink{}); err != boom {
		t.Errorf("err = %v", err)
	}
	p.SetError(nil)
	p.SetHook(func(n int, method string, _ taskmanager.CardRequest) error {
		if method == "LinkPullRequest" && n == 2 {
			return boom
		}
		return nil
	})
	if err := p.LinkPullRequest(ctx, "K-1", taskmanager.PullRequestLink{}); err != boom {
		t.Errorf("hook err = %v", err)
	}
	if len(p.Links()) != 0 {
		t.Errorf("failed calls recorded: %+v", p.Links())
	}
	if err := p.LinkPullRequest(ctx, "K-1", taskmanager.PullRequestLink{ID: "x"}); err != nil {
		t.Fatal(err)
	}
	if len(p.Links()) != 1 {
		t.Errorf("links = %+v", p.Links())
	}
}

func TestLinkPullRequestConcurrent(t *testing.T) {
	p := New()
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = p.LinkPullRequest(ctx, "K-1", taskmanager.PullRequestLink{})
			_ = p.Links()
		}()
	}
	wg.Wait()
	if len(p.Links()) != 50 {
		t.Errorf("links = %d", len(p.Links()))
	}
}

func TestFakeIsPullRequestLinker(t *testing.T) {
	var p taskmanager.Provider = New()
	if _, ok := p.(taskmanager.PullRequestLinker); !ok {
		t.Error("fake does not implement PullRequestLinker")
	}
}

func TestFindOpenCardsReturnsCopyOfCandidates(t *testing.T) {
	p := New()
	in := []taskmanager.CardCandidate{{Key: "A-1", Summary: "T-1: x", Created: time.Unix(10, 0)}, {Key: "A-2"}}
	p.SetCandidates(in)
	in[0].Key = "changed"

	got, err := p.FindOpenCards(context.Background(), taskmanager.CardSearch{})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Key != "A-1" || got[1].Key != "A-2" {
		t.Fatalf("got %+v", got)
	}
	got[0].Key = "mutated"
	again, _ := p.FindOpenCards(context.Background(), taskmanager.CardSearch{})
	if again[0].Key != "A-1" {
		t.Errorf("caller changed fake state: %+v", again)
	}
}

func TestFindOpenCardsRecordsSearchesInOrder(t *testing.T) {
	p := New()
	first := taskmanager.CardSearch{}
	second := taskmanager.CardSearch{CreatedAfter: time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)}
	for _, s := range []taskmanager.CardSearch{first, second} {
		if _, err := p.FindOpenCards(context.Background(), s); err != nil {
			t.Fatal(err)
		}
	}
	got := p.Searches()
	if len(got) != 2 || !got[0].CreatedAfter.IsZero() || !got[1].CreatedAfter.Equal(second.CreatedAfter) {
		t.Fatalf("searches = %+v", got)
	}
	got[0].CreatedAfter = time.Now()
	if !p.Searches()[0].CreatedAfter.IsZero() {
		t.Error("Searches did not return a copy")
	}
}

func TestFindOpenCardsError(t *testing.T) {
	p := New()
	p.SetCandidates([]taskmanager.CardCandidate{{Key: "A-1"}})
	boom := errors.New("boom")
	p.SetError(boom)
	got, err := p.FindOpenCards(context.Background(), taskmanager.CardSearch{})
	if !errors.Is(err, boom) || got != nil {
		t.Fatalf("got %v, %v", got, err)
	}
	p.SetError(nil)
	if got, err := p.FindOpenCards(context.Background(), taskmanager.CardSearch{}); err != nil || len(got) != 1 {
		t.Fatalf("after clearing: %v, %v", got, err)
	}
}

func TestFindOpenCardsSkipsHook(t *testing.T) {
	p := New()
	var ns []int
	p.SetHook(func(n int, method string, _ taskmanager.CardRequest) error {
		ns = append(ns, n)
		if method == "FindOpenCards" {
			t.Error("hook ran for FindOpenCards")
		}
		return nil
	})
	_, _ = p.FindOpenCards(context.Background(), taskmanager.CardSearch{})
	if _, err := p.Create(context.Background(), taskmanager.CardRequest{}); err != nil {
		t.Fatal(err)
	}
	if len(ns) != 1 || ns[0] != 1 {
		t.Errorf("hook counts = %v, want [1]", ns)
	}
}

func TestFakeIsCardFinder(t *testing.T) {
	var p taskmanager.Provider = New()
	if _, ok := p.(taskmanager.CardFinder); !ok {
		t.Error("fake does not implement CardFinder")
	}
}

func TestSprintAdderRecordsCallsInOrder(t *testing.T) {
	var _ taskmanager.SprintAdder = New()
	p := New()
	a := taskmanager.SprintRequest{Kind: domain.KindMilestone, Moment: taskmanager.SprintMomentCreated}
	b := taskmanager.SprintRequest{Kind: domain.KindMilestone, Moment: taskmanager.SprintMomentStarted, Sprint: "S"}
	if _, err := p.AddToSprint(ctx, "K-1", a); err != nil {
		t.Fatal(err)
	}
	if _, err := p.AddToSprint(ctx, "K-2", b); err != nil {
		t.Fatal(err)
	}
	got := p.SprintCalls()
	if len(got) != 2 || got[0] != (SprintCall{"K-1", a}) || got[1] != (SprintCall{"K-2", b}) {
		t.Fatalf("calls = %+v", got)
	}
}

func TestSprintAdderResultAndHookCounting(t *testing.T) {
	p := New()
	var ns []int
	p.SetHook(func(n int, _ string, _ taskmanager.CardRequest) error { ns = append(ns, n); return nil })
	boom := errors.New("boom")
	p.SetSprintResult("fell back", boom)
	note, err := p.AddToSprint(ctx, "K-1", taskmanager.SprintRequest{})
	if note != "fell back" || err != boom {
		t.Fatalf("got %q, %v", note, err)
	}
	p.SetSprintResult("", nil)
	if note, err := p.AddToSprint(ctx, "K-1", taskmanager.SprintRequest{}); note != "" || err != nil {
		t.Fatalf("got %q, %v", note, err)
	}
	if _, err := p.Create(ctx, req("T-1")); err != nil {
		t.Fatal(err)
	}
	if len(ns) != 1 || ns[0] != 1 {
		t.Fatalf("hook counts = %v, want [1]", ns)
	}
	if len(p.Calls()) != 1 {
		t.Fatalf("calls = %v", p.Calls())
	}
}
