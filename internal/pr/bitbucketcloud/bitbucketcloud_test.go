package bitbucketcloud

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/bengearig/ralph/internal/config"
	"github.com/bengearig/ralph/internal/pr"
	"github.com/bengearig/ralph/internal/service"
)

func env(vars map[string]string) func(string) (string, bool) {
	return func(k string) (string, bool) {
		v, ok := vars[k]
		return v, ok
	}
}

func cfgFor(auth string) config.BitbucketCloudConfig {
	return config.BitbucketCloudConfig{Workspace: "acme", RepoSlug: "widgets", Auth: auth, EmailEnv: "EML", TokenEnv: "TOK"}
}

func TestValidate(t *testing.T) {
	for _, tt := range []struct {
		auth string
		want []string
		not  []string
	}{
		{config.BitbucketAuthBasic, []string{"workspace", "repo_slug", "TOK", "EML"}, nil},
		{config.BitbucketAuthBearer, []string{"workspace", "repo_slug", "TOK"}, []string{"EML"}},
	} {
		p := New(config.BitbucketCloudConfig{Auth: tt.auth, EmailEnv: "EML", TokenEnv: "TOK"}, time.Second, env(nil))
		err := p.Validate()
		if err == nil {
			t.Fatalf("%s: expected error", tt.auth)
		}
		for _, w := range tt.want {
			if !strings.Contains(err.Error(), w) {
				t.Errorf("%s: missing %q in %v", tt.auth, w, err)
			}
		}
		for _, w := range tt.not {
			if strings.Contains(err.Error(), w) {
				t.Errorf("%s: unexpected %q in %v", tt.auth, w, err)
			}
		}
	}
	ready := New(cfgFor(config.BitbucketAuthBasic), time.Second, env(map[string]string{"TOK": "s3cret", "EML": "me@example.com"}))
	if err := ready.Validate(); err != nil {
		t.Errorf("Validate = %v", err)
	}
}

type captured struct {
	method, path, auth, rawQuery string
	query                        url.Values
	body                         map[string]any
}

func fakeServer(t *testing.T, status int, response string) (*httptest.Server, *captured, *int) {
	t.Helper()
	got := &captured{}
	calls := new(int)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*calls++
		got.method, got.path, got.auth = r.Method, r.URL.EscapedPath(), r.Header.Get("Authorization")
		got.rawQuery, got.query = r.URL.RawQuery, r.URL.Query()
		_ = json.NewDecoder(r.Body).Decode(&got.body)
		w.WriteHeader(status)
		_, _ = w.Write([]byte(response))
	}))
	t.Cleanup(srv.Close)
	return srv, got, calls
}

func provider(srv *httptest.Server, cfg config.BitbucketCloudConfig) *Provider {
	return NewWithClient(cfg, time.Second, env(map[string]string{"TOK": "s3cret", "EML": "me@example.com"}), srv.Client(), srv.URL)
}

func nested(m map[string]any, keys ...string) any {
	var v any = m
	for _, k := range keys {
		v = v.(map[string]any)[k]
	}
	return v
}

func TestCreate(t *testing.T) {
	for _, draft := range []bool{true, false} {
		srv, got, _ := fakeServer(t, http.StatusCreated, `{"id": 42, "links": {"html": {"href": "https://bitbucket.org/acme/widgets/pull-requests/42"}}}`)
		pull, err := provider(srv, cfgFor(config.BitbucketAuthBearer)).Create(context.Background(), pr.Request{
			Title: "M-1: Login", Body: "the body", SourceBranch: "ralph/login", DestinationBranch: "main", Draft: draft,
		})
		if err != nil {
			t.Fatal(err)
		}
		want := pr.PullRequest{ID: "42", Number: 42, Namespace: "acme", Repository: "widgets", URL: "https://bitbucket.org/acme/widgets/pull-requests/42"}
		if pull != want {
			t.Errorf("pull = %+v, want %+v", pull, want)
		}
		if got.method != http.MethodPost || got.path != "/repositories/acme/widgets/pullrequests" || got.auth != "Bearer s3cret" {
			t.Errorf("request = %+v", got)
		}
		for key, w := range map[string]any{
			"title": "M-1: Login", "description": "the body", "draft": draft,
		} {
			if got.body[key] != w {
				t.Errorf("body[%s] = %v, want %v", key, got.body[key], w)
			}
		}
		if nested(got.body, "source", "branch", "name") != "ralph/login" || nested(got.body, "destination", "branch", "name") != "main" {
			t.Errorf("branches = %v", got.body)
		}
	}
}

func TestCreateBasicAuth(t *testing.T) {
	srv, got, _ := fakeServer(t, http.StatusCreated, `{"id": 1}`)
	if _, err := provider(srv, cfgFor(config.BitbucketAuthBasic)).Create(context.Background(), pr.Request{}); err != nil {
		t.Fatal(err)
	}
	if want := "Basic " + base64.StdEncoding.EncodeToString([]byte("me@example.com:s3cret")); got.auth != want {
		t.Errorf("auth = %q, want %q", got.auth, want)
	}
}

func TestCreateLinkFallbackAndMissingID(t *testing.T) {
	srv, _, _ := fakeServer(t, http.StatusCreated, `{"id": 7}`)
	pull, err := provider(srv, cfgFor(config.BitbucketAuthBearer)).Create(context.Background(), pr.Request{})
	if err != nil {
		t.Fatal(err)
	}
	if want, _ := pr.Link(Name, "acme", "widgets", 7); pull.URL != want {
		t.Errorf("URL = %q, want %q", pull.URL, want)
	}
	srv, _, _ = fakeServer(t, http.StatusCreated, `{"links": {"html": {"href": "https://x"}}}`)
	if _, err := provider(srv, cfgFor(config.BitbucketAuthBearer)).Create(context.Background(), pr.Request{}); err == nil {
		t.Error("a response without id should fail")
	}
}

func TestCreateEscapesPath(t *testing.T) {
	srv, got, _ := fakeServer(t, http.StatusCreated, `{"id": 3}`)
	cfg := cfgFor(config.BitbucketAuthBearer)
	cfg.Workspace, cfg.RepoSlug = "ac me", "wid/gets"
	if _, err := provider(srv, cfg).Create(context.Background(), pr.Request{}); err != nil {
		t.Fatal(err)
	}
	if want := "/repositories/ac%20me/wid%2Fgets/pullrequests"; got.path != want {
		t.Errorf("path = %q, want %q", got.path, want)
	}
}

func TestCreateInvalidConfigMakesNoRequest(t *testing.T) {
	srv, _, calls := fakeServer(t, http.StatusCreated, `{"id": 1}`)
	cfg := cfgFor(config.BitbucketAuthBasic)
	cfg.Workspace = ""
	if _, err := provider(srv, cfg).Create(context.Background(), pr.Request{}); err == nil || !strings.Contains(err.Error(), "workspace") {
		t.Errorf("err = %v", err)
	}
	if *calls != 0 {
		t.Errorf("%d requests made", *calls)
	}
}

func TestCreateErrorsHideCredentials(t *testing.T) {
	for _, tt := range []struct {
		status int
		want   string
	}{
		{http.StatusUnauthorized, "authentication failed (401)"},
		{http.StatusForbidden, "permission denied (403)"},
		{http.StatusNotFound, "repository acme/widgets not found"},
		{http.StatusBadRequest, "bad request (400)"},
	} {
		srv, _, _ := fakeServer(t, tt.status, `{"error": {"message": "nope"}}`)
		_, err := provider(srv, cfgFor(config.BitbucketAuthBasic)).Create(context.Background(), pr.Request{})
		if err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("%d: err = %v, want %q", tt.status, err, tt.want)
			continue
		}
		for _, secret := range []string{"s3cret", "me@example.com"} {
			if strings.Contains(err.Error(), secret) {
				t.Errorf("%d: error leaks %q: %v", tt.status, secret, err)
			}
		}
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

func TestLink(t *testing.T) {
	tests := []struct {
		namespace, repository string
		number                int
		want                  string
	}{
		{"acme", "widgets", 7, "https://bitbucket.org/acme/widgets/pull-requests/7"},
		{"ac me", "wid/gets", 12, "https://bitbucket.org/ac%20me/wid%2Fgets/pull-requests/12"},
		{"a?b", "c#d", 1, "https://bitbucket.org/a%3Fb/c%23d/pull-requests/1"},
	}
	for _, tt := range tests {
		got, ok := pr.Link(Name, tt.namespace, tt.repository, tt.number)
		if !ok || got != tt.want {
			t.Errorf("Link(%q, %q, %d) = %q, %v; want %q", tt.namespace, tt.repository, tt.number, got, ok, tt.want)
		}
	}
}

func TestFindOpen(t *testing.T) {
	srv, got, _ := fakeServer(t, http.StatusOK, `{"values": [{"id": 9, "links": {"html": {"href": "https://bitbucket.org/acme/widgets/pull-requests/9"}}}]}`)
	pull, ok, err := provider(srv, cfgFor(config.BitbucketAuthBearer)).FindOpen(context.Background(), "ralph/x", "main")
	if err != nil || !ok {
		t.Fatalf("ok = %v, err = %v", ok, err)
	}
	want := pr.PullRequest{ID: "9", Number: 9, Namespace: "acme", Repository: "widgets", URL: "https://bitbucket.org/acme/widgets/pull-requests/9"}
	if pull != want {
		t.Errorf("pull = %+v, want %+v", pull, want)
	}
	if got.method != http.MethodGet || got.path != "/repositories/acme/widgets/pullrequests" || got.auth != "Bearer s3cret" {
		t.Errorf("request = %+v", got)
	}
	for k, w := range map[string]string{
		"state": "OPEN", "sort": "-updated_on", "pagelen": "1",
		"q": `source.branch.name="ralph/x" AND destination.branch.name="main"`,
	} {
		if got.query.Get(k) != w {
			t.Errorf("query[%s] = %q, want %q", k, got.query.Get(k), w)
		}
	}
}

func TestFindOpenEscapesBranchNames(t *testing.T) {
	srv, got, _ := fakeServer(t, http.StatusOK, `{"values": []}`)
	if _, _, err := provider(srv, cfgFor(config.BitbucketAuthBearer)).FindOpen(context.Background(), `a"b`, `c\d`); err != nil {
		t.Fatal(err)
	}
	if want := `source.branch.name="a\"b" AND destination.branch.name="c\\d"`; got.query.Get("q") != want {
		t.Errorf("q = %q, want %q", got.query.Get("q"), want)
	}
}

func TestFindOpenNone(t *testing.T) {
	for _, body := range []string{`{"values": []}`, `{}`} {
		srv, _, _ := fakeServer(t, http.StatusOK, body)
		pull, ok, err := provider(srv, cfgFor(config.BitbucketAuthBearer)).FindOpen(context.Background(), "ralph/x", "main")
		if err != nil || ok || pull != (pr.PullRequest{}) {
			t.Errorf("%s: pull = %+v, ok = %v, err = %v", body, pull, ok, err)
		}
	}
}

func TestFindOpenLinkFallback(t *testing.T) {
	srv, _, _ := fakeServer(t, http.StatusOK, `{"values": [{"id": 5}]}`)
	pull, ok, err := provider(srv, cfgFor(config.BitbucketAuthBearer)).FindOpen(context.Background(), "a", "b")
	if err != nil || !ok {
		t.Fatalf("ok = %v, err = %v", ok, err)
	}
	if want, _ := pr.Link(Name, "acme", "widgets", 5); pull.URL != want {
		t.Errorf("URL = %q, want %q", pull.URL, want)
	}
}

func TestFindOpenMalformed(t *testing.T) {
	for _, body := range []string{`not json`, `{"values": [{"links": {}}]}`} {
		srv, _, _ := fakeServer(t, http.StatusOK, body)
		if _, ok, err := provider(srv, cfgFor(config.BitbucketAuthBearer)).FindOpen(context.Background(), "a", "b"); err == nil || ok {
			t.Errorf("%s: ok = %v, err = %v", body, ok, err)
		}
	}
}

func TestFindOpenErrorsHideCredentials(t *testing.T) {
	for _, tt := range []struct {
		status int
		want   string
	}{
		{http.StatusUnauthorized, "authentication failed (401)"},
		{http.StatusForbidden, "permission denied (403)"},
		{http.StatusNotFound, "repository acme/widgets not found"},
		{http.StatusTooManyRequests, "rate limited (429)"},
		{http.StatusInternalServerError, "500 Internal Server Error"},
	} {
		srv, _, _ := fakeServer(t, tt.status, `{"error": {"message": "nope"}}`)
		_, ok, err := provider(srv, cfgFor(config.BitbucketAuthBasic)).FindOpen(context.Background(), "a", "b")
		if err == nil || ok || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("%d: ok = %v, err = %v, want %q", tt.status, ok, err, tt.want)
			continue
		}
		for _, secret := range []string{"s3cret", "me@example.com"} {
			if strings.Contains(err.Error(), secret) {
				t.Errorf("%d: error leaks %q: %v", tt.status, secret, err)
			}
		}
	}
}

func TestFindOpenInvalidConfigMakesNoRequest(t *testing.T) {
	srv, _, calls := fakeServer(t, http.StatusOK, `{"values": []}`)
	cfg := cfgFor(config.BitbucketAuthBasic)
	cfg.Workspace = ""
	if _, _, err := provider(srv, cfg).FindOpen(context.Background(), "a", "b"); err == nil || !strings.Contains(err.Error(), "workspace") {
		t.Errorf("err = %v", err)
	}
	if *calls != 0 {
		t.Errorf("%d requests made", *calls)
	}
}
