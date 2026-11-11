package cli

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/bengearig/ralph/internal/config"
	"github.com/bengearig/ralph/internal/pr"
	"github.com/bengearig/ralph/internal/pr/github"
)

type githubServer struct {
	mu       sync.Mutex
	requests []string
	existing string
	created  map[string]any
}

func (g *githubServer) count(method string) int {
	g.mu.Lock()
	defer g.mu.Unlock()
	n := 0
	for _, r := range g.requests {
		if strings.HasPrefix(r, method+" ") {
			n++
		}
	}
	return n
}

func newGitHubFixture(t *testing.T, existing string) (*prCreateFixture, *githubServer) {
	t.Helper()
	f := newPRCreateFixture(t)
	gs := &githubServer{existing: existing}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gs.mu.Lock()
		gs.requests = append(gs.requests, r.Method+" "+r.URL.RequestURI())
		gs.mu.Unlock()
		if r.Header.Get("Authorization") != "Bearer ghs-secret" {
			http.Error(w, `{"message":"Bad credentials"}`, http.StatusUnauthorized)
			return
		}
		if r.Method == http.MethodGet {
			if gs.existing == "" {
				_, _ = w.Write([]byte(`[]`))
				return
			}
			_, _ = w.Write([]byte(gs.existing))
			return
		}
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &gs.created)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"number": 7, "html_url": "https://github.com/acme/sso/pull/7"}`))
	}))
	t.Cleanup(srv.Close)
	newPRProvider = func(c *config.Config) (pr.Provider, error) {
		return github.NewWithClient(c.PR.GitHub, c.PR.Timeout, os.LookupEnv, srv.Client(), srv.URL), nil
	}
	t.Setenv("RALPH_PR__PROVIDER", "github")
	t.Setenv("RALPH_PR__GITHUB__OWNER", "acme")
	t.Setenv("RALPH_PR__GITHUB__REPO", "sso")
	t.Setenv("RALPH_GITHUB_TOKEN", "ghs-secret")
	return f, gs
}

func TestPRCreateGitHubOpensAndRecords(t *testing.T) {
	f, gs := newGitHubFixture(t, "")
	out := f.h.ok("pr", "create", "--milestone", "M-1")
	if want := "opened https://github.com/acme/sso/pull/7\n"; out != want {
		t.Fatalf("create = %q, want %q", out, want)
	}
	if !remoteHasBranch(t, f.remote, "ralph/login") {
		t.Error("branch was not pushed")
	}
	if gs.count("GET") != 1 || gs.count("POST") != 1 {
		t.Errorf("requests = %v", gs.requests)
	}
	if gs.created["head"] != "ralph/login" || gs.created["base"] != "main" {
		t.Errorf("create body = %v", gs.created)
	}
	if got := f.recorded(t); got.Provider != "github" || got.Namespace != "acme" || got.Repository != "sso" || got.Number != 7 {
		t.Errorf("recorded = %+v", got)
	}

	out = f.h.ok("pr", "create", "--milestone", "M-1")
	if want := "exists https://github.com/acme/sso/pull/7\n"; out != want {
		t.Errorf("second run = %q, want %q", out, want)
	}
	if len(gs.requests) != 2 {
		t.Errorf("second run made requests: %v", gs.requests)
	}

	t.Setenv("RALPH_PR__PROVIDER", "bitbucket_cloud")
	if show := f.h.ok("milestone", "show", "M-1"); !strings.Contains(show, "https://github.com/acme/sso/pull/7") {
		t.Errorf("milestone show lacks the link:\n%s", show)
	}
	var st struct {
		Initiatives []struct {
			Milestones []struct {
				ID          string `json:"id"`
				PullRequest *struct {
					Provider string `json:"provider"`
					Number   int    `json:"number"`
					URL      string `json:"url"`
				} `json:"pull_request"`
			} `json:"milestones"`
		} `json:"initiatives"`
	}
	f.h.json(&st, "status", "--json")
	found := false
	for _, i := range st.Initiatives {
		for _, m := range i.Milestones {
			if m.PullRequest != nil {
				found = true
				if m.PullRequest.Provider != "github" || m.PullRequest.Number != 7 || m.PullRequest.URL != "https://github.com/acme/sso/pull/7" {
					t.Errorf("status pull request = %+v", *m.PullRequest)
				}
			}
		}
	}
	if !found {
		t.Error("status --json shows no pull request")
	}
}

func TestPRCreateGitHubAdoptsOpenPullRequest(t *testing.T) {
	f, gs := newGitHubFixture(t, `[{"number": 4, "html_url": "https://github.com/acme/sso/pull/4"}]`)
	out := f.h.ok("pr", "create", "--milestone", "M-1")
	if want := "adopted https://github.com/acme/sso/pull/4\n"; out != want {
		t.Fatalf("adopt = %q, want %q", out, want)
	}
	if gs.count("POST") != 0 {
		t.Errorf("adopt created a pull request: %v", gs.requests)
	}
	if len(gs.requests) != 1 || !strings.Contains(gs.requests[0], "head=acme%3Aralph%2Flogin") || !strings.Contains(gs.requests[0], "base=main") {
		t.Errorf("find request = %v", gs.requests)
	}
	if got := f.recorded(t); got.Provider != "github" || got.Number != 4 {
		t.Errorf("recorded = %+v", got)
	}
}

func TestPRCreateGitHubDryRunMissingToken(t *testing.T) {
	f, gs := newGitHubFixture(t, "")
	t.Setenv("RALPH_GITHUB_TOKEN", "")
	_, errOut, err := f.h.run("", "pr", "create", "--milestone", "M-1", "--dry-run")
	if err != nil {
		t.Fatalf("dry run: %v", err)
	}
	if !strings.Contains(errOut, "provider is not ready") || !strings.Contains(errOut, "pr.github.token_env") {
		t.Errorf("stderr = %q", errOut)
	}
	if len(gs.requests) != 0 {
		t.Errorf("dry run made requests: %v", gs.requests)
	}
	if remoteHasBranch(t, f.remote, "ralph/login") {
		t.Error("dry run pushed the branch")
	}
}
