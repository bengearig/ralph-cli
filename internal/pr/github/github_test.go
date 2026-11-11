package github

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/bengearig/ralph/internal/config"
	"github.com/bengearig/ralph/internal/pr"
)

type captured struct {
	method, path, auth string
	query              url.Values
	body               map[string]any
}

func fakeServer(t *testing.T, status int, response string) (*httptest.Server, *captured, *int) {
	t.Helper()
	got := &captured{}
	calls := new(int)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*calls++
		got.method, got.path, got.auth = r.Method, r.URL.EscapedPath(), r.Header.Get("Authorization")
		got.query = r.URL.Query()
		_ = json.NewDecoder(r.Body).Decode(&got.body)
		w.WriteHeader(status)
		_, _ = w.Write([]byte(response))
	}))
	t.Cleanup(srv.Close)
	return srv, got, calls
}

func provider(srv *httptest.Server, cfg config.GitHubConfig) *Provider {
	return NewWithClient(cfg, time.Second, env(goodEnv), srv.Client(), srv.URL)
}

func TestValidate(t *testing.T) {
	err := New(config.GitHubConfig{TokenEnv: "TOK"}, time.Second, env(nil)).Validate()
	if err == nil {
		t.Fatal("expected error")
	}
	for _, w := range []string{"pr.github.owner", "pr.github.repo", "pr.github.token_env", "TOK"} {
		if !strings.Contains(err.Error(), w) {
			t.Errorf("missing %q in %v", w, err)
		}
	}
	err = New(config.GitHubConfig{}, time.Second, env(nil)).Validate()
	if err == nil || !strings.Contains(err.Error(), "pr.github.token_env is not set") {
		t.Errorf("empty token_env: %v", err)
	}
	if err := New(testCfg(), time.Second, env(goodEnv)).Validate(); err != nil {
		t.Errorf("Validate = %v", err)
	}
}

func TestCreate(t *testing.T) {
	for _, draft := range []bool{true, false} {
		srv, got, _ := fakeServer(t, http.StatusCreated, `{"number": 42, "html_url": "https://github.com/acme/widgets/pull/42"}`)
		pull, err := provider(srv, testCfg()).Create(context.Background(), pr.Request{
			Title: "M-1: Login", Body: "the body", SourceBranch: "ralph/login", DestinationBranch: "main", Draft: draft,
		})
		if err != nil {
			t.Fatal(err)
		}
		want := pr.PullRequest{ID: "42", Number: 42, Namespace: "acme", Repository: "widgets", URL: "https://github.com/acme/widgets/pull/42"}
		if pull != want {
			t.Errorf("pull = %+v, want %+v", pull, want)
		}
		if got.method != http.MethodPost || got.path != "/repos/acme/widgets/pulls" || got.auth != "Bearer "+testToken {
			t.Errorf("request = %+v", got)
		}
		for key, w := range map[string]any{
			"title": "M-1: Login", "body": "the body", "head": "ralph/login", "base": "main", "draft": draft,
		} {
			if got.body[key] != w {
				t.Errorf("body[%s] = %v, want %v", key, got.body[key], w)
			}
		}
	}
}

func TestCreateEscapesPath(t *testing.T) {
	srv, got, _ := fakeServer(t, http.StatusCreated, `{"number": 1}`)
	cfg := testCfg()
	cfg.Owner, cfg.Repo = "a b", "c/d"
	if _, err := provider(srv, cfg).Create(context.Background(), pr.Request{}); err != nil {
		t.Fatal(err)
	}
	if want := "/repos/a%20b/c%2Fd/pulls"; got.path != want {
		t.Errorf("path = %q, want %q", got.path, want)
	}
}

func TestCreateLinkFallbackAndMissingNumber(t *testing.T) {
	srv, _, _ := fakeServer(t, http.StatusCreated, `{"number": 7}`)
	pull, err := provider(srv, testCfg()).Create(context.Background(), pr.Request{})
	if err != nil {
		t.Fatal(err)
	}
	if want, _ := pr.Link(Name, "acme", "widgets", 7); pull.URL != want {
		t.Errorf("URL = %q, want %q", pull.URL, want)
	}
	for _, body := range []string{`{"html_url": "https://github.com/x"}`, `not json`} {
		srv, _, _ := fakeServer(t, http.StatusCreated, body)
		if _, err := provider(srv, testCfg()).Create(context.Background(), pr.Request{}); err == nil {
			t.Errorf("%s: expected error", body)
		}
	}
}

func TestInvalidConfigMakesNoRequest(t *testing.T) {
	srv, _, calls := fakeServer(t, http.StatusCreated, `{"number": 1}`)
	cfg := testCfg()
	cfg.Owner = ""
	if _, err := provider(srv, cfg).Create(context.Background(), pr.Request{}); err == nil || !strings.Contains(err.Error(), "owner") {
		t.Errorf("Create err = %v", err)
	}
	if _, _, err := provider(srv, cfg).FindOpen(context.Background(), "a", "b"); err == nil || !strings.Contains(err.Error(), "owner") {
		t.Errorf("FindOpen err = %v", err)
	}
	if *calls != 0 {
		t.Errorf("%d requests made", *calls)
	}
}

func TestErrorsHideToken(t *testing.T) {
	for _, tt := range []struct {
		status int
		want   string
	}{
		{http.StatusUnauthorized, "bad credentials (401)"},
		{http.StatusForbidden, "permission denied (403)"},
		{http.StatusNotFound, "repository acme/widgets not found"},
		{http.StatusUnprocessableEntity, "validation failed (422)"},
		{http.StatusInternalServerError, "500 Internal Server Error"},
	} {
		srv, _, _ := fakeServer(t, tt.status, `{"message": "nope"}`)
		_, err := provider(srv, testCfg()).Create(context.Background(), pr.Request{})
		if err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("Create %d: err = %v, want %q", tt.status, err, tt.want)
		} else {
			assertNoSecrets(t, err)
		}
		_, ok, err := provider(srv, testCfg()).FindOpen(context.Background(), "a", "b")
		if err == nil || ok || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("FindOpen %d: ok = %v, err = %v, want %q", tt.status, ok, err, tt.want)
		} else {
			assertNoSecrets(t, err)
		}
	}
}

func TestRegistered(t *testing.T) {
	cfg, err := config.Defaults()
	if err != nil {
		t.Fatal(err)
	}
	cfg.PR.Provider = Name
	p, err := pr.New(cfg)
	if err != nil || p.Name() != Name {
		t.Fatalf("pr.New = %v, %v", p, err)
	}
	if _, ok := p.(*Provider); !ok {
		t.Errorf("provider is %T", p)
	}
}

func TestLink(t *testing.T) {
	for _, tt := range []struct {
		namespace, repository string
		number                int
		want                  string
	}{
		{"acme", "widgets", 7, "https://github.com/acme/widgets/pull/7"},
		{"ac me", "wid/gets", 12, "https://github.com/ac%20me/wid%2Fgets/pull/12"},
		{"a?b", "c#d", 1, "https://github.com/a%3Fb/c%23d/pull/1"},
	} {
		got, ok := pr.Link(Name, tt.namespace, tt.repository, tt.number)
		if !ok || got != tt.want {
			t.Errorf("Link(%q, %q, %d) = %q, %v; want %q", tt.namespace, tt.repository, tt.number, got, ok, tt.want)
		}
	}
}

func TestFindOpen(t *testing.T) {
	srv, got, _ := fakeServer(t, http.StatusOK, `[{"number": 9, "html_url": "https://github.com/acme/widgets/pull/9"}]`)
	pull, ok, err := provider(srv, testCfg()).FindOpen(context.Background(), "ralph/x", "main")
	if err != nil || !ok {
		t.Fatalf("ok = %v, err = %v", ok, err)
	}
	want := pr.PullRequest{ID: "9", Number: 9, Namespace: "acme", Repository: "widgets", URL: "https://github.com/acme/widgets/pull/9"}
	if pull != want {
		t.Errorf("pull = %+v, want %+v", pull, want)
	}
	if got.method != http.MethodGet || got.path != "/repos/acme/widgets/pulls" || got.auth != "Bearer "+testToken {
		t.Errorf("request = %+v", got)
	}
	for k, w := range map[string]string{
		"state": "open", "head": "acme:ralph/x", "base": "main",
		"sort": "updated", "direction": "desc", "per_page": "1",
	} {
		if got.query.Get(k) != w {
			t.Errorf("query[%s] = %q, want %q", k, got.query.Get(k), w)
		}
	}
}

func TestFindOpenEncodesBranch(t *testing.T) {
	srv, got, _ := fakeServer(t, http.StatusOK, `[]`)
	if _, _, err := provider(srv, testCfg()).FindOpen(context.Background(), "a&b=c d#e", "main"); err != nil {
		t.Fatal(err)
	}
	if want := "acme:a&b=c d#e"; got.query.Get("head") != want {
		t.Errorf("head = %q, want %q", got.query.Get("head"), want)
	}
}

func TestFindOpenNoneAndMalformed(t *testing.T) {
	srv, _, _ := fakeServer(t, http.StatusOK, `[]`)
	pull, ok, err := provider(srv, testCfg()).FindOpen(context.Background(), "a", "b")
	if err != nil || ok || pull != (pr.PullRequest{}) {
		t.Errorf("pull = %+v, ok = %v, err = %v", pull, ok, err)
	}
	for _, body := range []string{`not json`, `{"number": 1}`, `[{"html_url": "x"}]`} {
		srv, _, _ := fakeServer(t, http.StatusOK, body)
		if _, ok, err := provider(srv, testCfg()).FindOpen(context.Background(), "a", "b"); err == nil || ok {
			t.Errorf("%s: ok = %v, err = %v", body, ok, err)
		}
	}
}

func TestFindOpenLinkFallback(t *testing.T) {
	srv, _, _ := fakeServer(t, http.StatusOK, `[{"number": 5}]`)
	pull, ok, err := provider(srv, testCfg()).FindOpen(context.Background(), "a", "b")
	if err != nil || !ok {
		t.Fatalf("ok = %v, err = %v", ok, err)
	}
	if want, _ := pr.Link(Name, "acme", "widgets", 5); pull.URL != want {
		t.Errorf("URL = %q, want %q", pull.URL, want)
	}
}
