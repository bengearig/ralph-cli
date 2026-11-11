package mcpserver

import (
	"context"
	"testing"

	"github.com/bengearig/ralph/internal/domain"
	"github.com/bengearig/ralph/internal/service"
)

func TestMilestoneGetIncludesPullRequest(t *testing.T) {
	ctx := context.Background()
	svc := newService(t)
	svc.SetPullRequestLink(func(provider, namespace, repository string, number int) (string, bool) {
		return "https://example.test/" + provider + "/" + namespace + "/" + repository, true
	})
	i, err := svc.CreateInitiative(ctx, service.InitiativeInput{Title: "Login", Brief: "b"})
	if err != nil {
		t.Fatal(err)
	}
	iid, _ := domain.ParseID(domain.KindInitiative, i.ID)
	m, err := svc.CreateMilestone(ctx, service.MilestoneInput{InitiativeID: iid, Title: "Core"})
	if err != nil {
		t.Fatal(err)
	}
	mid, _ := domain.ParseID(domain.KindMilestone, m.ID)
	cs := connectService(t, svc, Options{})

	var got service.MilestoneView
	mustOK(t, cs, "milestone_get", map[string]any{"id": m.ID}, &got)
	if got.PullRequest != nil {
		t.Fatalf("pull_request = %+v before recording, want nil", got.PullRequest)
	}
	if err := svc.RecordMilestonePullRequest(ctx, mid, domain.PullRequestRecord{Provider: "bb", Namespace: "acme", Repository: "app", Number: 12}); err != nil {
		t.Fatal(err)
	}
	got = service.MilestoneView{}
	mustOK(t, cs, "milestone_get", map[string]any{"id": m.ID}, &got)
	want := service.PullRequestView{Provider: "bb", Namespace: "acme", Repository: "app", Number: 12, URL: "https://example.test/bb/acme/app"}
	if got.PullRequest == nil || *got.PullRequest != want {
		t.Errorf("pull_request = %+v, want %+v", got.PullRequest, want)
	}
}
