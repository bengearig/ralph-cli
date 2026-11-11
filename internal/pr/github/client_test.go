package github

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bengearig/ralph/internal/config"
)

const testToken = "s3cr3t-token"

var goodEnv = map[string]string{"RALPH_GITHUB_TOKEN": testToken}

func env(vars map[string]string) func(string) (string, bool) {
	return func(k string) (string, bool) {
		v, ok := vars[k]
		return v, ok
	}
}

func testCfg() config.GitHubConfig {
	return config.GitHubConfig{Owner: "acme", Repo: "widgets", TokenEnv: "RALPH_GITHUB_TOKEN"}
}

func newTestClient(t *testing.T, baseURL string, timeout time.Duration) *Client {
	t.Helper()
	c, err := NewClient(testCfg(), timeout, env(goodEnv), nil, baseURL)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func respond(status int, body string, headers map[string]string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for k, v := range headers {
			w.Header().Set(k, v)
		}
		w.WriteHeader(status)
		io.WriteString(w, body)
	}))
}

func assertNoSecrets(t *testing.T, err error) {
	t.Helper()
	for _, s := range []string{testToken, "Authorization", "Bearer "} {
		if strings.Contains(err.Error(), s) {
			t.Errorf("error %q contains %q", err, s)
		}
	}
}

func TestRequestHeaders(t *testing.T) {
	for _, withBody := range []bool{false, true} {
		var got http.Header
		var path, rawQuery string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			got, path, rawQuery = r.Header.Clone(), r.URL.Path, r.URL.RawQuery
			io.WriteString(w, `{}`)
		}))
		c, err := NewClient(testCfg(), time.Second, env(goodEnv), nil, srv.URL+"/")
		if err != nil {
			t.Fatal(err)
		}
		var body []byte
		if withBody {
			body = []byte(`{"a":1}`)
		}
		if _, err := c.do(context.Background(), "POST", "/x", url.Values{"state": {"open"}}, body); err != nil {
			t.Fatal(err)
		}
		srv.Close()
		if path != "/x" || rawQuery != "state=open" {
			t.Errorf("path %q query %q", path, rawQuery)
		}
		for k, want := range map[string]string{
			"Authorization":        "Bearer " + testToken,
			"Accept":               "application/vnd.github+json",
			"X-GitHub-Api-Version": "2022-11-28",
		} {
			if got.Get(k) != want {
				t.Errorf("%s = %q, want %q", k, got.Get(k), want)
			}
		}
		if !strings.HasPrefix(got.Get("User-Agent"), "ralph/") {
			t.Errorf("User-Agent = %q", got.Get("User-Agent"))
		}
		ct := got.Get("Content-Type")
		if withBody && ct != "application/json" {
			t.Errorf("Content-Type = %q", ct)
		}
		if !withBody && ct != "" {
			t.Errorf("Content-Type = %q without body", ct)
		}
	}
}

func TestDefaultBaseURL(t *testing.T) {
	c, err := NewClient(testCfg(), time.Second, env(goodEnv), nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if c.baseURL != "https://api.github.com" {
		t.Errorf("baseURL = %q", c.baseURL)
	}
}

func TestTokenEnvFromConfig(t *testing.T) {
	cfg := testCfg()
	cfg.TokenEnv = "MY_GH"
	if _, err := NewClient(cfg, time.Second, env(map[string]string{"MY_GH": testToken}), nil, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := NewClient(cfg, time.Second, env(goodEnv), nil, ""); err == nil {
		t.Fatal("default variable must not be read when token_env is overridden")
	}
}

func TestMissingToken(t *testing.T) {
	for name, vars := range map[string]map[string]string{
		"missing": {},
		"blank":   {"RALPH_GITHUB_TOKEN": "  "},
		"empty":   {"RALPH_GITHUB_TOKEN": ""},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := NewClient(testCfg(), time.Second, env(vars), nil, "")
			if err == nil || !strings.Contains(err.Error(), "pr.github.token_env") {
				t.Fatalf("err = %v", err)
			}
			if strings.Contains(err.Error(), "  ") {
				t.Errorf("error leaks value: %v", err)
			}
		})
	}
}

func TestStatusMapping(t *testing.T) {
	reset := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	long := strings.Repeat("x", 500)
	for _, tc := range []struct {
		name    string
		status  int
		body    string
		headers map[string]string
		want    []string
		notWant []string
	}{
		{"401", 401, `{"message":"Bad credentials"}`, nil, []string{"401", "pr.github.token_env"}, nil},
		{"403 permission", 403, `{"message":"Resource not accessible"}`, nil, []string{"permission denied (403)"}, []string{"rate limited"}},
		{"403 rate limit", 403, `{"message":"API rate limit exceeded"}`,
			map[string]string{"X-RateLimit-Remaining": "0", "X-RateLimit-Reset": strconv.FormatInt(reset.Unix(), 10)},
			[]string{"rate limited (403)", "2026-11-11T12:00:00Z"}, []string{"permission denied"}},
		{"404", 404, `{"message":"Not Found"}`, nil, []string{"acme/widgets", "404"}, nil},
		{"422", 422, `{"message":"Validation Failed","errors":[{"resource":"PullRequest","code":"custom","message":"No commits between main and feat"}]}`, nil,
			[]string{"422", "Validation Failed", "No commits between main and feat"}, []string{"pr.draft"}},
		{"422 field error", 422, `{"message":"Validation Failed","errors":[{"resource":"PullRequest","field":"head","code":"invalid"}]}`, nil,
			[]string{"PullRequest head invalid"}, nil},
		{"422 draft", 422, `{"message":"Validation Failed","errors":[{"resource":"PullRequest","code":"custom","message":"Draft pull requests are not supported in this repository."}]}`, nil,
			[]string{"pr.draft: false"}, nil},
		{"429", 429, `{"message":"slow down"}`, map[string]string{"Retry-After": "30"}, []string{"rate limited (429)", "Retry-After: 30"}, nil},
		{"500", 500, `{"message":"boom"}`, nil, []string{"500 Internal Server Error", "boom"}, nil},
		{"long body", 500, long, nil, []string{strings.Repeat("x", 200) + "…"}, []string{strings.Repeat("x", 201)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := respond(tc.status, tc.body, tc.headers)
			defer srv.Close()
			_, err := newTestClient(t, srv.URL, time.Second).do(context.Background(), "GET", "/x", nil, nil)
			if err == nil {
				t.Fatal("want error")
			}
			var apiErr *APIError
			if !errors.As(err, &apiErr) || apiErr.Status != tc.status {
				t.Fatalf("err = %v", err)
			}
			for _, w := range tc.want {
				if !strings.Contains(err.Error(), w) {
					t.Errorf("error %q missing %q", err, w)
				}
			}
			for _, w := range tc.notWant {
				if strings.Contains(err.Error(), w) {
					t.Errorf("error %q contains %q", err, w)
				}
			}
			assertNoSecrets(t, err)
		})
	}
}

func TestOversizeResponseRejected(t *testing.T) {
	srv := respond(200, strings.Repeat("a", maxResponseBody+10), nil)
	defer srv.Close()
	_, err := newTestClient(t, srv.URL, 5*time.Second).do(context.Background(), "GET", "/x", nil, nil)
	if err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("err = %v", err)
	}
}

func TestTimeout(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()
	defer close(release)
	_, err := newTestClient(t, srv.URL, 50*time.Millisecond).do(context.Background(), "GET", "/x", nil, nil)
	if err == nil || !(errors.Is(err, context.DeadlineExceeded) || strings.Contains(err.Error(), "deadline exceeded") || strings.Contains(err.Error(), "timeout")) {
		t.Fatalf("err = %v", err)
	}
	assertNoSecrets(t, err)
}

func TestSingleAttempt(t *testing.T) {
	for _, status := range []int{500, 429} {
		var n atomic.Int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			n.Add(1)
			w.WriteHeader(status)
		}))
		_, err := newTestClient(t, srv.URL, time.Second).do(context.Background(), "GET", "/x", nil, nil)
		srv.Close()
		if err == nil {
			t.Fatalf("status %d: want error", status)
		}
		if n.Load() != 1 {
			t.Errorf("status %d: %d requests", status, n.Load())
		}
	}
}
