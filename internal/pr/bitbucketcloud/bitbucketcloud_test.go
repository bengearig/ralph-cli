package bitbucketcloud

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/bengearig/ralph/internal/config"
	"github.com/bengearig/ralph/internal/errs"
	"github.com/bengearig/ralph/internal/pr"
	"github.com/bengearig/ralph/internal/service"
)

func env(vars map[string]string) func(string) (string, bool) {
	return func(k string) (string, bool) {
		v, ok := vars[k]
		return v, ok
	}
}

func TestValidate(t *testing.T) {
	p := New(config.BitbucketCloudConfig{TokenEnv: "TOK"}, env(nil))
	err := p.Validate()
	if err == nil {
		t.Fatal("expected error")
	}
	for _, want := range []string{"workspace", "repo_slug", "TOK"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("missing %q in %v", want, err)
		}
	}
	ready := New(config.BitbucketCloudConfig{Workspace: "acme", RepoSlug: "web", TokenEnv: "TOK"}, env(map[string]string{"TOK": "s3cret"}))
	if err := ready.Validate(); err != nil {
		t.Errorf("Validate = %v", err)
	}
	if _, err := ready.Create(context.Background(), pr.Request{}); !errors.Is(err, errs.ErrNotImplemented) {
		t.Errorf("Create = %v", err)
	}
	if _, err := p.Create(context.Background(), pr.Request{}); errors.Is(err, errs.ErrNotImplemented) {
		t.Error("Create should report configuration problems first")
	}
}

func TestRegisteredAndRequest(t *testing.T) {
	cfg, err := config.Defaults()
	if err != nil {
		t.Fatal(err)
	}
	p, err := pr.New(cfg)
	if err != nil || p.Name() != Name {
		t.Fatalf("pr.New = %v, %v", p, err)
	}
	cfg.PR.Provider = "github"
	if _, err := pr.New(cfg); err == nil {
		t.Error("unknown provider should fail")
	}

	cfg.PR.Provider = Name
	req := pr.MilestoneRequest(cfg,
		service.InitiativeView{ID: "I-1", Title: "SSO"},
		service.MilestoneView{ID: "M-1", Title: "OIDC", Description: "Login via OIDC"},
		[]service.TaskView{{ID: "T-1", Title: "a", Status: "DONE"}, {ID: "T-2", Title: "b", Status: "CANCELLED"}},
		"ralph/oidc")
	if req.Title != "M-1: OIDC" || req.SourceBranch != "ralph/oidc" || req.DestinationBranch != "main" || !req.Draft {
		t.Errorf("req = %+v", req)
	}
	for _, want := range []string{"- [x] T-1 a (DONE)", "- [ ] T-2 b (CANCELLED)", "Login via OIDC", "I-1 (SSO)"} {
		if !strings.Contains(req.Body, want) {
			t.Errorf("body lacks %q:\n%s", want, req.Body)
		}
	}
}
