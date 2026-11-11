package jira

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/bengearig/ralph/internal/taskmanager"
)

type linkRequest struct {
	method, uri, auth string
	body              map[string]any
}

type linkJira struct {
	*httptest.Server
	mu     sync.Mutex
	reqs   []linkRequest
	status int
}

func newLinkJira(t *testing.T, status int) *linkJira {
	t.Helper()
	f := &linkJira{status: status}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var m map[string]any
		_ = json.Unmarshal(b, &m)
		f.mu.Lock()
		f.reqs = append(f.reqs, linkRequest{r.Method, r.RequestURI, r.Header.Get("Authorization"), m})
		status := f.status
		f.mu.Unlock()
		w.WriteHeader(status)
		_, _ = w.Write([]byte(`{"errorMessages":["nope"]}`))
	}))
	t.Cleanup(f.Close)
	return f
}

func (f *linkJira) setStatus(s int) {
	f.mu.Lock()
	f.status = s
	f.mu.Unlock()
}

func (f *linkJira) requests() []linkRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]linkRequest(nil), f.reqs...)
}

var testLink = taskmanager.PullRequestLink{
	ID:    "ralph-pr:github:o/r#1",
	Title: "AIN-3604: Pull request",
	URL:   "https://github.com/o/r/pull/1",
}

func TestLinkPullRequest(t *testing.T) {
	f := newLinkJira(t, http.StatusCreated)
	p := newTestProvider(f.URL)
	if err := p.LinkPullRequest(context.Background(), "AIN-3604", testLink); err != nil {
		t.Fatal(err)
	}
	reqs := f.requests()
	if len(reqs) != 1 {
		t.Fatalf("got %d requests, want 1", len(reqs))
	}
	r := reqs[0]
	if r.method != http.MethodPost || r.uri != "/rest/api/3/issue/AIN-3604/remotelink" {
		t.Errorf("request = %s %s", r.method, r.uri)
	}
	if r.auth != authValue() {
		t.Errorf("Authorization = %q", r.auth)
	}
	if r.body["globalId"] != testLink.ID || r.body["relationship"] != "pull request" {
		t.Errorf("body = %v", r.body)
	}
	if app, _ := r.body["application"].(map[string]any); app["name"] != "ralph" {
		t.Errorf("application = %v", r.body["application"])
	}
	obj, _ := r.body["object"].(map[string]any)
	if obj["url"] != testLink.URL || obj["title"] != testLink.Title {
		t.Errorf("object = %v", obj)
	}
}

func TestLinkPullRequestTwice(t *testing.T) {
	f := newLinkJira(t, http.StatusCreated)
	p := newTestProvider(f.URL)
	ctx := context.Background()
	if err := p.LinkPullRequest(ctx, "AIN-3604", testLink); err != nil {
		t.Fatal(err)
	}
	f.setStatus(http.StatusOK)
	if err := p.LinkPullRequest(ctx, "AIN-3604", testLink); err != nil {
		t.Fatal(err)
	}
	reqs := f.requests()
	if len(reqs) != 2 {
		t.Fatalf("got %d requests, want 2", len(reqs))
	}
	if reqs[0].body["globalId"] != testLink.ID || reqs[1].body["globalId"] != testLink.ID {
		t.Errorf("globalIds differ: %v, %v", reqs[0].body["globalId"], reqs[1].body["globalId"])
	}
}

func TestLinkPullRequestErrors(t *testing.T) {
	for _, status := range []int{http.StatusNotFound, http.StatusUnauthorized, http.StatusForbidden, http.StatusInternalServerError} {
		f := newLinkJira(t, status)
		err := newTestProvider(f.URL).LinkPullRequest(context.Background(), "AIN-3604", testLink)
		if err == nil {
			t.Errorf("status %d: no error", status)
			continue
		}
		for _, secret := range []string{testToken, testEmail} {
			if strings.Contains(err.Error(), secret) {
				t.Errorf("status %d: error leaks %q: %v", status, secret, err)
			}
		}
	}
}

func TestLinkPullRequestEscapesKey(t *testing.T) {
	f := newLinkJira(t, http.StatusCreated)
	if err := newTestProvider(f.URL).LinkPullRequest(context.Background(), "A/B ?#1", testLink); err != nil {
		t.Fatal(err)
	}
	if got, want := f.requests()[0].uri, "/rest/api/3/issue/A%2FB%20%3F%231/remotelink"; got != want {
		t.Errorf("uri = %q, want %q", got, want)
	}
}

func TestLinkPullRequestRejectsEmpty(t *testing.T) {
	f := newLinkJira(t, http.StatusCreated)
	p := newTestProvider(f.URL)
	cases := map[string]struct {
		key string
		l   taskmanager.PullRequestLink
	}{
		"key": {"", testLink},
		"id":  {"AIN-1", taskmanager.PullRequestLink{Title: "t", URL: testLink.URL}},
		"url": {"AIN-1", taskmanager.PullRequestLink{ID: testLink.ID, Title: "t"}},
	}
	for name, c := range cases {
		if err := p.LinkPullRequest(context.Background(), c.key, c.l); err == nil {
			t.Errorf("empty %s: no error", name)
		}
	}
	if n := len(f.requests()); n != 0 {
		t.Errorf("%d requests sent, want none", n)
	}
}

func TestProviderIsPullRequestLinker(t *testing.T) {
	var p taskmanager.Provider = newTestProvider("http://example.invalid")
	if _, ok := p.(taskmanager.PullRequestLinker); !ok {
		t.Error("jira provider does not implement PullRequestLinker")
	}
}
