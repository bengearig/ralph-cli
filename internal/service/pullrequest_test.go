package service

import (
	"context"
	"errors"
	"testing"

	"github.com/bengearig/ralph/internal/domain"
	"github.com/bengearig/ralph/internal/store"
)

func fakeLink(provider, namespace, repository string, number int) (string, bool) {
	if provider != "fake" {
		return "", false
	}
	return "https://example.test/" + namespace + "/" + repository + "/" + string(rune('0'+number)), true
}

func TestRecordMilestonePullRequestViews(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	iid, mid := e.scaffold(t)
	rec := domain.PullRequestRecord{Provider: " fake ", Namespace: "acme", Repository: "app", Number: 7}

	before, err := e.svc.GetMilestone(ctx, mid)
	if err != nil {
		t.Fatal(err)
	}
	if before.PullRequest != nil {
		t.Fatalf("pull_request = %+v before recording, want nil", before.PullRequest)
	}
	if err := e.svc.RecordMilestonePullRequest(ctx, mid, rec); err != nil {
		t.Fatal(err)
	}
	want := PullRequestView{Provider: "fake", Namespace: "acme", Repository: "app", Number: 7}

	check := func(label string, got *PullRequestView, wantView PullRequestView) {
		t.Helper()
		if got == nil || *got != wantView {
			t.Errorf("%s pull_request = %+v, want %+v", label, got, wantView)
		}
	}
	views := func(wantView PullRequestView) {
		t.Helper()
		m, err := e.svc.GetMilestone(ctx, mid)
		if err != nil {
			t.Fatal(err)
		}
		check("get", m.PullRequest, wantView)
		if m.UpdatedAt != before.UpdatedAt {
			t.Errorf("updated_at changed from %s to %s", before.UpdatedAt, m.UpdatedAt)
		}
		list, err := e.svc.ListMilestones(ctx, iid, ListOptions{})
		if err != nil || len(list) != 1 {
			t.Fatalf("ListMilestones = %v, %v", list, err)
		}
		check("list", list[0].PullRequest, wantView)
		st, err := e.svc.Status(ctx, 0, ListOptions{})
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, in := range st.Initiatives {
			for _, ms := range in.Milestones {
				found = true
				check("status", ms.PullRequest, wantView)
			}
		}
		if !found {
			t.Error("status has no milestones")
		}
	}
	views(want)

	e.svc.SetPullRequestLink(fakeLink)
	want.URL = "https://example.test/acme/app/7"
	views(want)

	title := "Renamed"
	u, err := e.svc.UpdateMilestone(ctx, mid, MilestonePatch{Title: &title})
	if err != nil {
		t.Fatal(err)
	}
	check("update", u.PullRequest, want)

	e.svc.SetPullRequestLink(func(string, string, string, int) (string, bool) { return "ignored", false })
	m, _ := e.svc.GetMilestone(ctx, mid)
	if m.PullRequest == nil || m.PullRequest.URL != "" {
		t.Errorf("url with a link function returning false = %+v, want empty", m.PullRequest)
	}

	c, err := e.svc.CreateMilestone(ctx, MilestoneInput{InitiativeID: iid, Title: "Other"})
	if err != nil || c.PullRequest != nil {
		t.Errorf("created milestone pull_request = %+v, %v; want nil", c.PullRequest, err)
	}
}

func TestRecordMilestonePullRequestValidation(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	_, mid := e.scaffold(t)
	good := domain.PullRequestRecord{Provider: "p", Namespace: "n", Repository: "r", Number: 1}

	for name, rec := range map[string]domain.PullRequestRecord{
		"empty provider":   {Namespace: "n", Repository: "r", Number: 1},
		"blank namespace":  {Provider: "p", Namespace: "  ", Repository: "r", Number: 1},
		"empty repository": {Provider: "p", Namespace: "n", Number: 1},
		"zero number":      {Provider: "p", Namespace: "n", Repository: "r"},
		"negative number":  {Provider: "p", Namespace: "n", Repository: "r", Number: -3},
	} {
		if err := e.svc.RecordMilestonePullRequest(ctx, mid, rec); !errors.Is(err, domain.ErrInvalid) {
			t.Errorf("%s: err = %v, want ErrInvalid", name, err)
		}
		if m, _ := e.svc.GetMilestone(ctx, mid); m.PullRequest != nil {
			t.Errorf("%s: record changed to %+v", name, m.PullRequest)
		}
	}
	if err := e.svc.RecordMilestonePullRequest(ctx, mid, good); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.RecordMilestonePullRequest(ctx, mid, domain.PullRequestRecord{Provider: "p", Namespace: "n", Repository: "r"}); !errors.Is(err, domain.ErrInvalid) {
		t.Errorf("invalid over valid: err = %v, want ErrInvalid", err)
	}
	if m, _ := e.svc.GetMilestone(ctx, mid); m.PullRequest == nil || m.PullRequest.Number != 1 {
		t.Errorf("invalid record replaced the valid one: %+v", m.PullRequest)
	}
	second := domain.PullRequestRecord{Provider: "q", Namespace: "n2", Repository: "r2", Number: 2}
	if err := e.svc.RecordMilestonePullRequest(ctx, mid, second); err != nil {
		t.Fatal(err)
	}
	if m, _ := e.svc.GetMilestone(ctx, mid); m.PullRequest == nil || *m.PullRequest != (PullRequestView{Provider: "q", Namespace: "n2", Repository: "r2", Number: 2}) {
		t.Errorf("second record did not replace the first: %+v", m.PullRequest)
	}
	if err := e.svc.RecordMilestonePullRequest(ctx, 999, good); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("unknown milestone: err = %v, want ErrNotFound", err)
	}
}

func TestClearMilestonePullRequest(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	_, mid := e.scaffold(t)
	rec := domain.PullRequestRecord{Provider: "p", Namespace: "n", Repository: "r", Number: 4}

	if got, err := e.svc.ClearMilestonePullRequest(ctx, mid); err != nil || !got.IsZero() {
		t.Errorf("clear with none = %+v, %v; want zero, nil", got, err)
	}
	if err := e.svc.RecordMilestonePullRequest(ctx, mid, rec); err != nil {
		t.Fatal(err)
	}
	if got, err := e.svc.ClearMilestonePullRequest(ctx, mid); err != nil || got != rec {
		t.Errorf("clear = %+v, %v; want %+v", got, err, rec)
	}
	if m, _ := e.svc.GetMilestone(ctx, mid); m.PullRequest != nil {
		t.Errorf("view after clear = %+v, want nil", m.PullRequest)
	}
	if got, err := e.svc.ClearMilestonePullRequest(ctx, mid); err != nil || !got.IsZero() {
		t.Errorf("second clear = %+v, %v; want zero, nil", got, err)
	}
	if _, err := e.svc.ClearMilestonePullRequest(ctx, 999); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("unknown milestone: err = %v, want ErrNotFound", err)
	}
}

func TestMilestonePullRequestArchivedAndTx(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	iid, mid := e.scaffold(t)
	rec := domain.PullRequestRecord{Provider: "p", Namespace: "n", Repository: "r", Number: 4}
	if err := e.svc.RecordMilestonePullRequest(ctx, mid, rec); err != nil {
		t.Fatal(err)
	}

	boom := errors.New("boom")
	err := e.svc.InTx(ctx, func(tx *Service) error {
		if _, err := tx.ClearMilestonePullRequest(ctx, mid); err != nil {
			return err
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("InTx = %v, want boom", err)
	}
	if m, _ := e.svc.GetMilestone(ctx, mid); m.PullRequest == nil || m.PullRequest.Number != 4 {
		t.Errorf("rolled-back clear persisted: %+v", m.PullRequest)
	}
	err = e.svc.InTx(ctx, func(tx *Service) error {
		if err := tx.RecordMilestonePullRequest(ctx, mid, domain.PullRequestRecord{Provider: "p", Namespace: "n", Repository: "r", Number: 9}); err != nil {
			return err
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("InTx = %v, want boom", err)
	}
	if m, _ := e.svc.GetMilestone(ctx, mid); m.PullRequest == nil || m.PullRequest.Number != 4 {
		t.Errorf("rolled-back record persisted: %+v", m.PullRequest)
	}

	if _, err := e.svc.ArchiveInitiative(ctx, iid, DocumentChoice{None: true}); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.RecordMilestonePullRequest(ctx, mid, domain.PullRequestRecord{Provider: "p", Namespace: "n", Repository: "r", Number: 9}); !errors.Is(err, ErrArchived) {
		t.Errorf("record on archived = %v, want ErrArchived", err)
	}
	if _, err := e.svc.ClearMilestonePullRequest(ctx, mid); !errors.Is(err, ErrArchived) {
		t.Errorf("clear on archived = %v, want ErrArchived", err)
	}
}
