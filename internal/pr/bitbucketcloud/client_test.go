package bitbucketcloud

import (
	"context"
	"encoding/base64"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bengearig/ralph/internal/config"
)

const (
	testEmail = "me@example.com"
	testToken = "s3cr3t-token"
)

var goodEnv = map[string]string{"RALPH_BITBUCKET_EMAIL": testEmail, "RALPH_BITBUCKET_TOKEN": testToken}

func testCfg(auth string) config.BitbucketCloudConfig {
	return config.BitbucketCloudConfig{
		Workspace: "acme", RepoSlug: "widgets", Auth: auth,
		EmailEnv: "RALPH_BITBUCKET_EMAIL", TokenEnv: "RALPH_BITBUCKET_TOKEN",
	}
}

func newTestClient(t *testing.T, baseURL string, timeout time.Duration) *Client {
	t.Helper()
	c, err := NewClient(testCfg(config.BitbucketAuthBasic), timeout, env(goodEnv), nil, baseURL)
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

func TestRequestHeaders(t *testing.T) {
	basic := "Basic " + base64.StdEncoding.EncodeToString([]byte(testEmail+":"+testToken))
	for _, tc := range []struct {
		name, auth, want string
	}{
		{"basic", config.BitbucketAuthBasic, basic},
		{"bearer", config.BitbucketAuthBearer, "Bearer " + testToken},
	} {
		for _, withBody := range []bool{false, true} {
			t.Run(tc.name, func(t *testing.T) {
				var got http.Header
				var path, rawQuery string
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					got, path, rawQuery = r.Header.Clone(), r.URL.Path, r.URL.RawQuery
					io.WriteString(w, `{}`)
				}))
				defer srv.Close()
				c, err := NewClient(testCfg(tc.auth), time.Second, env(goodEnv), nil, srv.URL+"/")
				if err != nil {
					t.Fatal(err)
				}
				var body []byte
				if withBody {
					body = []byte(`{"a":1}`)
				}
				if _, err := c.do(context.Background(), "POST", "/x", url.Values{"state": {"OPEN"}}, body); err != nil {
					t.Fatal(err)
				}
				if got.Get("Authorization") != tc.want {
					t.Errorf("Authorization = %q", got.Get("Authorization"))
				}
				if got.Get("Accept") != "application/json" {
					t.Errorf("Accept = %q", got.Get("Accept"))
				}
				if !strings.HasPrefix(got.Get("User-Agent"), "ralph/") {
					t.Errorf("User-Agent = %q", got.Get("User-Agent"))
				}
				if ct := got.Get("Content-Type"); withBody != (ct == "application/json") || (!withBody && ct != "") {
					t.Errorf("withBody=%v Content-Type = %q", withBody, ct)
				}
				if path != "/x" || rawQuery != "state=OPEN" {
					t.Errorf("path=%q query=%q", path, rawQuery)
				}
			})
		}
	}
}

func TestDefaultBaseURL(t *testing.T) {
	c, err := NewClient(testCfg(config.BitbucketAuthBasic), time.Second, env(goodEnv), nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if c.baseURL != "https://api.bitbucket.org/2.0" {
		t.Errorf("baseURL = %q", c.baseURL)
	}
}

func TestMissingEnv(t *testing.T) {
	for _, tc := range []struct {
		name    string
		auth    string
		env     map[string]string
		setting string
		secret  string
	}{
		{"email missing", config.BitbucketAuthBasic, map[string]string{"RALPH_BITBUCKET_TOKEN": testToken}, "pr.bitbucket_cloud.email_env", testToken},
		{"email blank", config.BitbucketAuthBasic, map[string]string{"RALPH_BITBUCKET_EMAIL": "  ", "RALPH_BITBUCKET_TOKEN": testToken}, "pr.bitbucket_cloud.email_env", testToken},
		{"token missing", config.BitbucketAuthBasic, map[string]string{"RALPH_BITBUCKET_EMAIL": testEmail}, "pr.bitbucket_cloud.token_env", testEmail},
		{"token blank", config.BitbucketAuthBasic, map[string]string{"RALPH_BITBUCKET_EMAIL": testEmail, "RALPH_BITBUCKET_TOKEN": ""}, "pr.bitbucket_cloud.token_env", testEmail},
		{"bearer token missing", config.BitbucketAuthBearer, map[string]string{"RALPH_BITBUCKET_EMAIL": testEmail}, "pr.bitbucket_cloud.token_env", testEmail},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := NewClient(testCfg(tc.auth), time.Second, env(tc.env), nil, "")
			if err == nil || !strings.Contains(err.Error(), tc.setting) {
				t.Fatalf("err = %v", err)
			}
			if strings.Contains(err.Error(), tc.secret) {
				t.Errorf("error leaks value: %v", err)
			}
		})
	}
}

func TestBearerDoesNotRequireEmail(t *testing.T) {
	_, err := NewClient(testCfg(config.BitbucketAuthBearer), time.Second, env(map[string]string{"RALPH_BITBUCKET_TOKEN": testToken}), nil, "")
	if err != nil {
		t.Fatal(err)
	}
}

func TestStatusMapping(t *testing.T) {
	long := strings.Repeat("x", 500)
	for _, tc := range []struct {
		name    string
		status  int
		body    string
		headers map[string]string
		want    []string
	}{
		{"401", 401, `{"type":"error","error":{"message":"bad creds"}}`, nil,
			[]string{"authentication failed", "pr.bitbucket_cloud.auth", "email_env", "token_env"}},
		{"403", 403, ``, nil, []string{"permission denied", "pull request write access"}},
		{"404", 404, ``, nil, []string{"acme/widgets", "not found or not accessible"}},
		{"400", 400, `{"type":"error","error":{"message":"Bad branch","fields":{"source.branch.name":["branch not found"]}}}`, nil,
			[]string{"400", "Bad branch", "source.branch.name: branch not found"}},
		{"429", 429, ``, map[string]string{"Retry-After": "42"}, []string{"rate limited", "Retry-After: 42"}},
		{"429 no header", 429, ``, nil, []string{"rate limited"}},
		{"500", 500, `oops`, nil, []string{"500", "Internal Server Error", "oops"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := respond(tc.status, tc.body, tc.headers)
			defer srv.Close()
			_, err := newTestClient(t, srv.URL, time.Second).do(context.Background(), "GET", "/repositories/acme/widgets/pullrequests", nil, nil)
			if err == nil {
				t.Fatal("want error")
			}
			var apiErr *APIError
			if !errors.As(err, &apiErr) || apiErr.Status != tc.status {
				t.Fatalf("err = %#v", err)
			}
			for _, w := range tc.want {
				if !strings.Contains(err.Error(), w) {
					t.Errorf("error %q lacks %q", err, w)
				}
			}
			assertNoSecrets(t, err)
		})
	}
	t.Run("long body truncated", func(t *testing.T) {
		srv := respond(500, long, nil)
		defer srv.Close()
		_, err := newTestClient(t, srv.URL, time.Second).do(context.Background(), "GET", "/x", nil, nil)
		if err == nil || strings.Contains(err.Error(), strings.Repeat("x", 201)) || !strings.Contains(err.Error(), strings.Repeat("x", 200)) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("long error message truncated", func(t *testing.T) {
		srv := respond(400, `{"error":{"message":"`+long+`"}}`, nil)
		defer srv.Close()
		_, err := newTestClient(t, srv.URL, time.Second).do(context.Background(), "GET", "/x", nil, nil)
		if err == nil || strings.Contains(err.Error(), strings.Repeat("x", 201)) {
			t.Fatalf("err = %v", err)
		}
	})
}

func assertNoSecrets(t *testing.T, err error) {
	t.Helper()
	for _, s := range []string{testToken, testEmail, "Authorization", "Basic ", "Bearer "} {
		if strings.Contains(err.Error(), s) {
			t.Errorf("error %q contains %q", err, s)
		}
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
