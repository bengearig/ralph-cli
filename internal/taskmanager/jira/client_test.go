package jira

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
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

func env(m map[string]string) func(string) (string, bool) {
	return func(k string) (string, bool) { v, ok := m[k]; return v, ok }
}

var goodEnv = map[string]string{"RALPH_JIRA_EMAIL": testEmail, "RALPH_JIRA_TOKEN": testToken}

func newTestClient(t *testing.T, baseURL string, timeout time.Duration) *Client {
	t.Helper()
	c, err := NewClient(config.JiraConfig{BaseURL: baseURL, EmailEnv: "RALPH_JIRA_EMAIL", TokenEnv: "RALPH_JIRA_TOKEN"}, timeout, env(goodEnv), nil)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func authValue() string {
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(testEmail+":"+testToken))
}

func TestCreateIssue(t *testing.T) {
	var got map[string]map[string]json.RawMessage
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/rest/api/3/issue" {
			t.Errorf("got %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("Authorization") != authValue() {
			t.Errorf("auth header = %q", r.Header.Get("Authorization"))
		}
		if r.Header.Get("Content-Type") != "application/json" || r.Header.Get("Accept") != "application/json" {
			t.Errorf("headers = %v", r.Header)
		}
		b, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(b, &got); err != nil {
			t.Error(err)
		}
		w.WriteHeader(201)
		io.WriteString(w, `{"id":"10001","key":"PROJ-7"}`)
	}))
	defer srv.Close()
	c := newTestClient(t, srv.URL, time.Second)
	adf := `{"type":"doc","version":1,"content":[]}`
	key, err := c.CreateIssue(context.Background(), IssueFields{
		Project: "PROJ", IssueType: "Task", Summary: "T-1: x", Description: json.RawMessage(adf),
		Labels: []string{"ralph", "T-1"}, ParentKey: "PROJ-1",
	})
	if err != nil || key != "PROJ-7" {
		t.Fatalf("key=%q err=%v", key, err)
	}
	f := got["fields"]
	want := map[string]string{
		"project": `{"key":"PROJ"}`, "issuetype": `{"name":"Task"}`, "summary": `"T-1: x"`,
		"description": adf, "labels": `["ralph","T-1"]`, "parent": `{"key":"PROJ-1"}`,
	}
	for k, v := range want {
		if string(f[k]) != v {
			t.Errorf("fields.%s = %s, want %s", k, f[k], v)
		}
	}
}

func TestCreateIssueOmitsEmpty(t *testing.T) {
	var fields map[string]json.RawMessage
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct{ Fields map[string]json.RawMessage }
		b, _ := io.ReadAll(r.Body)
		json.Unmarshal(b, &body)
		fields = body.Fields
		w.WriteHeader(201)
		io.WriteString(w, `{"key":"PROJ-8"}`)
	}))
	defer srv.Close()
	c := newTestClient(t, srv.URL, time.Second)
	if _, err := c.CreateIssue(context.Background(), IssueFields{Project: "P", IssueType: "Task", Summary: "s"}); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"parent", "labels", "description"} {
		if _, ok := fields[k]; ok {
			t.Errorf("fields.%s present", k)
		}
	}
}

func TestCreateIssueEmptyKey(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(201)
		io.WriteString(w, `{"id":"1","key":""}`)
	}))
	defer srv.Close()
	if _, err := newTestClient(t, srv.URL, time.Second).CreateIssue(context.Background(), IssueFields{}); err == nil {
		t.Fatal("expected error")
	}
}

func TestUpdateIssue(t *testing.T) {
	type rec struct{ method, path, raw, body string }
	var last rec
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		last = rec{r.Method, r.URL.Path, r.URL.EscapedPath(), string(b)}
		w.WriteHeader(204)
	}))
	defer srv.Close()
	c := newTestClient(t, srv.URL, time.Second)
	ctx := context.Background()

	adf := `{"type":"doc","version":1,"content":[]}`
	if err := c.UpdateIssue(ctx, "PROJ-7", IssueFields{Project: "X", Summary: "s", Description: json.RawMessage(adf), Labels: []string{"a"}, ParentKey: "P-1"}); err != nil {
		t.Fatal(err)
	}
	if last.method != "PUT" || last.path != "/rest/api/3/issue/PROJ-7" {
		t.Errorf("got %+v", last)
	}
	if last.body != `{"fields":{"summary":"s","description":`+adf+`}}` {
		t.Errorf("body = %s", last.body)
	}

	if err := c.UpdateIssue(ctx, "PROJ-7", IssueFields{Summary: "s"}); err != nil {
		t.Fatal(err)
	}
	if last.body != `{"fields":{"summary":"s","description":null}}` {
		t.Errorf("body = %s", last.body)
	}

	if err := c.UpdateIssue(ctx, "A/B C", IssueFields{Summary: "s"}); err != nil {
		t.Fatal(err)
	}
	if last.raw != "/rest/api/3/issue/A%2FB%20C" || last.path != "/rest/api/3/issue/A/B C" {
		t.Errorf("path = %q raw = %q", last.path, last.raw)
	}
}

func TestErrors(t *testing.T) {
	tests := []struct {
		name    string
		status  int
		header  map[string]string
		body    string
		contain []string
	}{
		{"401", 401, nil, `{"errorMessages":["Unauthorized"]}`, []string{"401 Unauthorized", "check the email and API token"}},
		{"403", 403, nil, `{"errorMessages":["Forbidden"]}`, []string{"403 Forbidden", "Forbidden", "can create issues"}},
		{"404", 404, nil, `{"errorMessages":["No project"]}`, []string{"404 Not Found", "No project", "task_manager.jira.base_url and project_key"}},
		{"400", 400, nil, `{"errorMessages":["bad"],"errors":{"summary":"too long","issuetype":"Specify an issue type"}}`,
			[]string{"400 Bad Request", "bad; issuetype: Specify an issue type; summary: too long", "task_manager.jira.issue_types"}},
		{"429", 429, map[string]string{"Retry-After": "30"}, `{}`, []string{"429 Too Many Requests", "30"}},
		{"500", 500, nil, "  upstream exploded  ", []string{"500 Internal Server Error", "upstream exploded"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var n atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				n.Add(1)
				for k, v := range tc.header {
					w.Header().Set(k, v)
				}
				w.WriteHeader(tc.status)
				io.WriteString(w, tc.body)
			}))
			defer srv.Close()
			c := newTestClient(t, srv.URL, time.Second)
			_, err := c.CreateIssue(context.Background(), IssueFields{})
			var ae *APIError
			if !errors.As(err, &ae) || ae.Status != tc.status {
				t.Fatalf("err = %v", err)
			}
			for _, s := range tc.contain {
				if !strings.Contains(err.Error(), s) {
					t.Errorf("%q missing %q", err, s)
				}
			}
			if tc.status == 429 && ae.RetryAfter != "30" {
				t.Errorf("RetryAfter = %q", ae.RetryAfter)
			}
			if err := c.UpdateIssue(context.Background(), "P-1", IssueFields{}); !errors.As(err, &ae) {
				t.Errorf("update err = %v", err)
			}
			if n.Load() != 2 {
				t.Errorf("requests = %d, want 2 (one per call)", n.Load())
			}
			if strings.Contains(err.Error(), testToken) {
				t.Error("token leaked")
			}
		})
	}
}

func TestInvalidJSONOnSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(201)
		io.WriteString(w, "not json")
	}))
	defer srv.Close()
	_, err := newTestClient(t, srv.URL, time.Second).CreateIssue(context.Background(), IssueFields{})
	if err == nil || !strings.Contains(err.Error(), "not valid JSON") {
		t.Fatalf("err = %v", err)
	}
}

func TestTimeoutAndCancel(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()
	defer close(release)

	c := newTestClient(t, srv.URL, 50*time.Millisecond)
	_, err := c.CreateIssue(context.Background(), IssueFields{})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v", err)
	}

	c = newTestClient(t, srv.URL, 10*time.Second)
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(30*time.Millisecond, cancel)
	start := time.Now()
	err = c.UpdateIssue(ctx, "P-1", IssueFields{})
	if !errors.Is(err, context.Canceled) || time.Since(start) > 2*time.Second {
		t.Fatalf("err = %v after %v", err, time.Since(start))
	}
}

func TestSecretsNotLeaked(t *testing.T) {
	var logs bytes.Buffer
	log.SetOutput(&logs)
	defer log.SetOutput(io.Discard)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
		io.WriteString(w, `{"errorMessages":["nope"]}`)
	}))
	c := newTestClient(t, srv.URL, time.Second)
	_, err401 := c.CreateIssue(context.Background(), IssueFields{})
	srv.Close()
	_, errNet := c.CreateIssue(context.Background(), IssueFields{})
	if err401 == nil || errNet == nil {
		t.Fatal("expected errors")
	}
	b64 := base64.StdEncoding.EncodeToString([]byte(testEmail + ":" + testToken))
	for _, s := range []string{err401.Error(), errNet.Error(), logs.String()} {
		if strings.Contains(s, testToken) || strings.Contains(s, b64) {
			t.Errorf("secret leaked in %q", s)
		}
	}
}

func TestMissingEnv(t *testing.T) {
	cfg := config.JiraConfig{BaseURL: "https://x", EmailEnv: "RALPH_JIRA_EMAIL", TokenEnv: "RALPH_JIRA_TOKEN"}
	_, err := NewClient(cfg, time.Second, env(map[string]string{"RALPH_JIRA_EMAIL": testEmail}), nil)
	if err == nil || !strings.Contains(err.Error(), "RALPH_JIRA_TOKEN") || !strings.Contains(err.Error(), "is not set") || strings.Contains(err.Error(), testEmail) {
		t.Fatalf("err = %v", err)
	}
	_, err = NewClient(cfg, time.Second, env(map[string]string{"RALPH_JIRA_EMAIL": "  ", "RALPH_JIRA_TOKEN": testToken}), nil)
	if err == nil || !strings.Contains(err.Error(), "RALPH_JIRA_EMAIL") || strings.Contains(err.Error(), testToken) {
		t.Fatalf("err = %v", err)
	}
}

func TestBaseURLTrailingSlash(t *testing.T) {
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		w.WriteHeader(201)
		io.WriteString(w, `{"key":"P-1"}`)
	}))
	defer srv.Close()
	for _, base := range []string{srv.URL, srv.URL + "/", srv.URL + "//"} {
		if _, err := newTestClient(t, base, time.Second).CreateIssue(context.Background(), IssueFields{}); err != nil {
			t.Fatal(err)
		}
	}
	for _, p := range paths {
		if p != "/rest/api/3/issue" {
			t.Errorf("path = %q", p)
		}
	}
	if len(paths) != 3 {
		t.Errorf("paths = %v", paths)
	}
}

type transitionServer struct {
	*httptest.Server
	reqs []string
	post string
}

func newTransitionServer(t *testing.T, current string, transitions string, status int) *transitionServer {
	t.Helper()
	ts := &transitionServer{}
	ts.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ts.reqs = append(ts.reqs, r.Method+" "+r.URL.RequestURI())
		if status != 0 {
			w.WriteHeader(status)
			io.WriteString(w, `{"errorMessages":["nope"]}`)
			return
		}
		switch {
		case r.Method == "GET" && r.URL.Path == "/rest/api/3/issue/PROJ-1":
			io.WriteString(w, `{"fields":{"status":{"name":"`+current+`"}}}`)
		case r.Method == "GET" && r.URL.Path == "/rest/api/3/issue/PROJ-1/transitions":
			io.WriteString(w, transitions)
		case r.Method == "POST" && r.URL.Path == "/rest/api/3/issue/PROJ-1/transitions":
			b, _ := io.ReadAll(r.Body)
			ts.post = string(b)
			w.WriteHeader(204)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL)
			w.WriteHeader(500)
		}
	}))
	t.Cleanup(ts.Close)
	return ts
}

const sampleTransitions = `{"transitions":[{"id":"21","to":{"name":"In Progress"}},{"id":"31","to":{"name":"Done"}}]}`

func TestTransitionIssueAlreadyThere(t *testing.T) {
	ts := newTransitionServer(t, "DONE", sampleTransitions, 0)
	c := newTestClient(t, ts.URL, time.Second)
	if err := c.TransitionIssue(context.Background(), "PROJ-1", "Done"); err != nil {
		t.Fatal(err)
	}
	if len(ts.reqs) != 1 || ts.reqs[0] != "GET /rest/api/3/issue/PROJ-1?fields=status" {
		t.Errorf("requests = %v", ts.reqs)
	}
}

func TestTransitionIssueTransitions(t *testing.T) {
	ts := newTransitionServer(t, "To Do", sampleTransitions, 0)
	c := newTestClient(t, ts.URL, time.Second)
	if err := c.TransitionIssue(context.Background(), "PROJ-1", "done"); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"GET /rest/api/3/issue/PROJ-1?fields=status",
		"GET /rest/api/3/issue/PROJ-1/transitions",
		"POST /rest/api/3/issue/PROJ-1/transitions",
	}
	if strings.Join(ts.reqs, "|") != strings.Join(want, "|") {
		t.Errorf("requests = %v", ts.reqs)
	}
	if ts.post != `{"transition":{"id":"31"}}` {
		t.Errorf("post body = %s", ts.post)
	}
}

func TestTransitionIssueNoMatch(t *testing.T) {
	ts := newTransitionServer(t, "To Do", `{"transitions":[{"id":"21","to":{"name":"In Progress"}},{"id":"11","to":{"name":"Blocked"}}]}`, 0)
	c := newTestClient(t, ts.URL, time.Second)
	err := c.TransitionIssue(context.Background(), "PROJ-1", "Done")
	if err == nil || !strings.Contains(err.Error(), `no transition to status "Done"`) ||
		!strings.Contains(err.Error(), "reachable statuses: Blocked, In Progress") {
		t.Errorf("err = %v", err)
	}
	if len(ts.reqs) != 2 {
		t.Errorf("requests = %v", ts.reqs)
	}

	ts = newTransitionServer(t, "To Do", `{"transitions":[]}`, 0)
	c = newTestClient(t, ts.URL, time.Second)
	err = c.TransitionIssue(context.Background(), "PROJ-1", "Done")
	if err == nil || !strings.HasSuffix(err.Error(), "reachable statuses: none") {
		t.Errorf("err = %v", err)
	}
}

func TestTransitionIssueAPIErrorNoRetry(t *testing.T) {
	ts := newTransitionServer(t, "", "", 401)
	c := newTestClient(t, ts.URL, time.Second)
	err := c.TransitionIssue(context.Background(), "PROJ-1", "Done")
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != 401 || !strings.Contains(err.Error(), "check the email and API token") {
		t.Fatalf("err = %v", err)
	}
	if len(ts.reqs) != 1 {
		t.Errorf("requests = %v", ts.reqs)
	}

	// 401 on a later call
	var n int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n++
		if n == 1 {
			io.WriteString(w, `{"fields":{"status":{"name":"To Do"}}}`)
			return
		}
		w.WriteHeader(401)
	}))
	defer srv.Close()
	c = newTestClient(t, srv.URL, time.Second)
	if err := c.TransitionIssue(context.Background(), "PROJ-1", "Done"); !errors.As(err, &apiErr) || n != 2 {
		t.Errorf("err = %v after %d requests", err, n)
	}
}
