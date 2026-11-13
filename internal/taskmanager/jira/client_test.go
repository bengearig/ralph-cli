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
	"sync"
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

func TestUpdateIssueParent(t *testing.T) {
	var body string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		body = string(b)
		w.WriteHeader(204)
	}))
	defer srv.Close()
	c := newTestClient(t, srv.URL, time.Second)
	tests := []struct {
		name string
		f    IssueFields
		want string
	}{
		{"leave", IssueFields{Summary: "s", ParentKey: "P-1"}, `{"fields":{"summary":"s","description":null}}`},
		{"set", IssueFields{Summary: "s", ParentKey: "P-1", ParentChange: ParentSet}, `{"fields":{"summary":"s","description":null,"parent":{"key":"P-1"}}}`},
		{"clear", IssueFields{Summary: "s", ParentChange: ParentClear}, `{"fields":{"summary":"s","description":null,"parent":null}}`},
	}
	for _, tt := range tests {
		if err := c.UpdateIssue(context.Background(), "PROJ-7", tt.f); err != nil {
			t.Fatalf("%s: %v", tt.name, err)
		}
		if body != tt.want {
			t.Errorf("%s: body = %s, want %s", tt.name, body, tt.want)
		}
	}
}

func TestUpdateIssueAddLabel(t *testing.T) {
	var body string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		body = string(b)
		w.WriteHeader(204)
	}))
	defer srv.Close()
	c := newTestClient(t, srv.URL, time.Second)
	adf := `{"type":"doc","version":1,"content":[]}`
	tests := []struct {
		name string
		f    IssueFields
		want string
	}{
		{"add", IssueFields{Summary: "s", Description: json.RawMessage(adf), AddLabels: []string{"ralph-store-ab"}, Labels: []string{"x"}},
			`{"fields":{"summary":"s","description":` + adf + `},"update":{"labels":[{"add":"ralph-store-ab"}]}}`},
		{"with parent", IssueFields{Summary: "s", ParentKey: "P-1", ParentChange: ParentSet, AddLabels: []string{"ralph-store-ab"}},
			`{"fields":{"summary":"s","description":null,"parent":{"key":"P-1"}},"update":{"labels":[{"add":"ralph-store-ab"}]}}`},
		{"empty", IssueFields{Summary: "s"}, `{"fields":{"summary":"s","description":null}}`},
	}
	for _, tt := range tests {
		if err := c.UpdateIssue(context.Background(), "PROJ-7", tt.f); err != nil {
			t.Fatalf("%s: %v", tt.name, err)
		}
		if body != tt.want {
			t.Errorf("%s: body = %s, want %s", tt.name, body, tt.want)
		}
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
		{"400 parent", 400, nil, `{"errors":{"parent":"Please select valid parent issue."}}`,
			[]string{"parent: Please select valid parent issue.", "one hierarchy level below its parent's type", "task_manager.jira.issue_types"}},
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

type workflowServer struct {
	*httptest.Server
	status string
	edges  map[string][]string
	posts  []string
}

func newWorkflowServer(t *testing.T, start string, edges map[string][]string) *workflowServer {
	t.Helper()
	ws := &workflowServer{status: start, edges: edges}
	ws.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "GET" && r.URL.Path == "/rest/api/3/issue/PROJ-1":
			io.WriteString(w, `{"fields":{"status":{"name":"`+ws.status+`"}}}`)
		case r.Method == "GET" && r.URL.Path == "/rest/api/3/issue/PROJ-1/transitions":
			var ts []string
			for _, to := range ws.edges[ws.status] {
				ts = append(ts, `{"id":"`+to+`","to":{"name":"`+to+`"}}`)
			}
			io.WriteString(w, `{"transitions":[`+strings.Join(ts, ",")+`]}`)
		case r.Method == "POST" && r.URL.Path == "/rest/api/3/issue/PROJ-1/transitions":
			var body struct {
				Transition struct{ ID string } `json:"transition"`
			}
			b, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(b, &body)
			ws.posts = append(ws.posts, body.Transition.ID)
			ws.status = body.Transition.ID
			w.WriteHeader(204)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL)
			w.WriteHeader(500)
		}
	}))
	t.Cleanup(ws.Close)
	return ws
}

var gatedWorkflow = map[string][]string{
	"To Be Sized": {"To Do", "Canceled"},
	"To Do":       {"In Progress", "Canceled"},
	"In Progress": {"Done", "To Do"},
}

func TestMoveIssueThroughPath(t *testing.T) {
	path := []string{"To Do", "In Progress", "Done"}
	for _, tc := range []struct {
		start string
		posts []string
	}{
		{"To Be Sized", []string{"To Do", "In Progress", "Done"}},
		{"To Do", []string{"In Progress", "Done"}},
		{"In Progress", []string{"Done"}},
		{"Done", nil},
	} {
		t.Run(tc.start, func(t *testing.T) {
			ws := newWorkflowServer(t, tc.start, gatedWorkflow)
			c := newTestClient(t, ws.URL, time.Second)
			if err := c.MoveIssue(context.Background(), "PROJ-1", path, nil); err != nil {
				t.Fatal(err)
			}
			if strings.Join(ws.posts, ",") != strings.Join(tc.posts, ",") || !strings.EqualFold(ws.status, "Done") {
				t.Errorf("posts %v, status %q", ws.posts, ws.status)
			}
		})
	}
}

func TestMoveIssueStopsAtFailingStep(t *testing.T) {
	ws := newWorkflowServer(t, "To Be Sized", map[string][]string{"To Be Sized": {"To Do"}, "To Do": {"Canceled"}})
	c := newTestClient(t, ws.URL, time.Second)
	err := c.MoveIssue(context.Background(), "PROJ-1", []string{"To Do", "In Progress", "Done"}, nil)
	if err == nil || !strings.Contains(err.Error(), `no transition to status "In Progress"`) ||
		!strings.Contains(err.Error(), "reachable statuses: Canceled") || !strings.Contains(err.Error(), "moving through To Do > In Progress > Done") {
		t.Fatalf("err = %v", err)
	}
	if ws.status != "To Do" {
		t.Errorf("status = %q, want the card left at To Do", ws.status)
	}
}

func TestMoveIssueSingleStepIsTransition(t *testing.T) {
	ts := newTransitionServer(t, "To Do", sampleTransitions, 0)
	c := newTestClient(t, ts.URL, time.Second)
	if err := c.MoveIssue(context.Background(), "PROJ-1", []string{"Done"}, nil); err != nil {
		t.Fatal(err)
	}
	if len(ts.reqs) != 3 || ts.post != `{"transition":{"id":"31"}}` {
		t.Errorf("requests = %v, post %s", ts.reqs, ts.post)
	}
}

func TestMoveIssuePrepareRunsOnlyWhenMoving(t *testing.T) {
	for _, tc := range []struct {
		start string
		runs  int
	}{{"To Be Sized", 1}, {"Done", 0}} {
		ws := newWorkflowServer(t, tc.start, gatedWorkflow)
		c := newTestClient(t, ws.URL, time.Second)
		runs := 0
		prepare := func(context.Context) error { runs++; return nil }
		if err := c.MoveIssue(context.Background(), "PROJ-1", []string{"To Do", "In Progress", "Done"}, prepare); err != nil {
			t.Fatal(err)
		}
		if runs != tc.runs {
			t.Errorf("from %s: prepare ran %d times, want %d", tc.start, runs, tc.runs)
		}
	}

	ws := newWorkflowServer(t, "To Be Sized", gatedWorkflow)
	c := newTestClient(t, ws.URL, time.Second)
	boom := errors.New("boom")
	if err := c.MoveIssue(context.Background(), "PROJ-1", []string{"To Do"}, func(context.Context) error { return boom }); !errors.Is(err, boom) || len(ws.posts) != 0 {
		t.Errorf("err = %v, posts %v", err, ws.posts)
	}
}

func TestEnsureOriginalEstimate(t *testing.T) {
	pick := func(typ string) string {
		return map[string]string{"Task": "5m", "Feature": "1m"}[typ]
	}
	for _, tc := range []struct {
		name, issue string
		put         string
	}{
		{"empty task", `{"fields":{"issuetype":{"name":"Task"},"timetracking":{}}}`, `{"fields":{"timetracking":{"originalEstimate":"5m"}}}`},
		{"empty feature", `{"fields":{"issuetype":{"name":"Feature"}}}`, `{"fields":{"timetracking":{"originalEstimate":"1m"}}}`},
		{"already set", `{"fields":{"issuetype":{"name":"Task"},"timetracking":{"originalEstimate":"2h","originalEstimateSeconds":7200}}}`, ""},
		{"seconds only", `{"fields":{"issuetype":{"name":"Task"},"timetracking":{"originalEstimateSeconds":60}}}`, ""},
		{"no estimate for type", `{"fields":{"issuetype":{"name":"Bug"},"timetracking":{}}}`, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var put string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodPut {
					b, _ := io.ReadAll(r.Body)
					put = string(b)
					w.WriteHeader(204)
					return
				}
				io.WriteString(w, tc.issue)
			}))
			defer srv.Close()
			c := newTestClient(t, srv.URL, time.Second)
			if err := c.EnsureOriginalEstimate(context.Background(), "PROJ-1", pick); err != nil {
				t.Fatal(err)
			}
			if put != tc.put {
				t.Errorf("put = %q, want %q", put, tc.put)
			}
		})
	}
}

type clientReq struct {
	method, uri, auth, body string
}

func recordingServer(t *testing.T, status int, resp string) (*httptest.Server, *[]clientReq) {
	t.Helper()
	var mu sync.Mutex
	var reqs []clientReq
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		reqs = append(reqs, clientReq{r.Method, r.RequestURI, r.Header.Get("Authorization"), string(b)})
		mu.Unlock()
		w.WriteHeader(status)
		io.WriteString(w, resp)
	}))
	t.Cleanup(srv.Close)
	return srv, &reqs
}

func TestCurrentUser(t *testing.T) {
	srv, reqs := recordingServer(t, 200, `{"accountId":"abc","displayName":"Me"}`)
	u, err := newTestClient(t, srv.URL, time.Second).CurrentUser(context.Background())
	if err != nil || u != (User{AccountID: "abc", DisplayName: "Me"}) {
		t.Fatalf("u=%+v err=%v", u, err)
	}
	if len(*reqs) != 1 || (*reqs)[0].method != "GET" || (*reqs)[0].uri != "/rest/api/3/myself" || (*reqs)[0].auth != authValue() {
		t.Errorf("requests = %+v", *reqs)
	}
}

func TestCurrentUserErrors(t *testing.T) {
	for name, body := range map[string]string{"no account id": `{"displayName":"Me"}`, "empty account id": `{"accountId":""}`, "not json": `<html>`} {
		t.Run(name, func(t *testing.T) {
			srv, reqs := recordingServer(t, 200, body)
			_, err := newTestClient(t, srv.URL, time.Second).CurrentUser(context.Background())
			if err == nil {
				t.Fatal("expected error")
			}
			if len(*reqs) != 1 {
				t.Errorf("requests = %d", len(*reqs))
			}
			if strings.Contains(err.Error(), testToken) {
				t.Errorf("credential in error: %v", err)
			}
		})
	}
}

func TestAssignee(t *testing.T) {
	tests := []struct {
		name string
		key  string
		body string
		uri  string
		want *User
	}{
		{"null", "AIN-1", `{"fields":{"assignee":null}}`, "/rest/api/3/issue/AIN-1?fields=assignee", nil},
		{"missing", "AIN-1", `{"fields":{}}`, "/rest/api/3/issue/AIN-1?fields=assignee", nil},
		{"present", "AIN-1", `{"fields":{"assignee":{"accountId":"abc","displayName":"Me"}}}`, "/rest/api/3/issue/AIN-1?fields=assignee", &User{"abc", "Me"}},
		{"escaped", "A/B 1", `{"fields":{}}`, "/rest/api/3/issue/A%2FB%201?fields=assignee", nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv, reqs := recordingServer(t, 200, tc.body)
			got, err := newTestClient(t, srv.URL, time.Second).Assignee(context.Background(), tc.key)
			if err != nil {
				t.Fatal(err)
			}
			if (got == nil) != (tc.want == nil) || (got != nil && *got != *tc.want) {
				t.Errorf("got %+v, want %+v", got, tc.want)
			}
			if len(*reqs) != 1 || (*reqs)[0].method != "GET" || (*reqs)[0].uri != tc.uri || (*reqs)[0].auth != authValue() {
				t.Errorf("requests = %+v", *reqs)
			}
		})
	}
}

func TestAssigneeInvalidJSON(t *testing.T) {
	srv, reqs := recordingServer(t, 200, `nope`)
	_, err := newTestClient(t, srv.URL, time.Second).Assignee(context.Background(), "AIN-1")
	if err == nil || !strings.Contains(err.Error(), "response is not valid JSON") || len(*reqs) != 1 {
		t.Fatalf("err=%v requests=%d", err, len(*reqs))
	}
}

func TestAssignIssue(t *testing.T) {
	srv, reqs := recordingServer(t, 204, "")
	if err := newTestClient(t, srv.URL, time.Second).AssignIssue(context.Background(), "AIN-1", "abc"); err != nil {
		t.Fatal(err)
	}
	want := clientReq{"PUT", "/rest/api/3/issue/AIN-1/assignee", authValue(), `{"accountId":"abc"}`}
	if len(*reqs) != 1 || (*reqs)[0] != want {
		t.Errorf("requests = %+v", *reqs)
	}
}

func TestAssignIssueEmptyAccountID(t *testing.T) {
	srv, reqs := recordingServer(t, 204, "")
	if err := newTestClient(t, srv.URL, time.Second).AssignIssue(context.Background(), "AIN-1", ""); err == nil {
		t.Fatal("expected error")
	}
	if len(*reqs) != 0 {
		t.Errorf("requests = %+v", *reqs)
	}
}

func TestAssignIssueForbiddenHint(t *testing.T) {
	srv, reqs := recordingServer(t, 403, `{"errorMessages":["Forbidden"]}`)
	err := newTestClient(t, srv.URL, time.Second).AssignIssue(context.Background(), "AIN-1", "abc")
	var ae *APIError
	if !errors.As(err, &ae) || ae.Status != 403 {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(err.Error(), "Assign Issues permission") || strings.Contains(err.Error(), "can create issues") {
		t.Errorf("err = %v", err)
	}
	if strings.Contains(err.Error(), testToken) || len(*reqs) != 1 {
		t.Errorf("err=%v requests=%d", err, len(*reqs))
	}
}

func TestForbiddenOnCreateKeepsHint(t *testing.T) {
	srv, _ := recordingServer(t, 403, `{}`)
	_, err := newTestClient(t, srv.URL, time.Second).CreateIssue(context.Background(), IssueFields{})
	if err == nil || !strings.Contains(err.Error(), "can create issues") || strings.Contains(err.Error(), "Assign Issues") {
		t.Errorf("err = %v", err)
	}
}

type searchReq struct {
	JQL           string   `json:"jql"`
	Fields        []string `json:"fields"`
	MaxResults    int      `json:"maxResults"`
	NextPageToken string   `json:"nextPageToken"`
}

func searchServer(t *testing.T, reqs *[]searchReq, respond func(n int, r searchReq) (int, string)) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/rest/api/3/search/jql" {
			t.Errorf("got %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("Authorization") != authValue() {
			t.Errorf("auth header = %q", r.Header.Get("Authorization"))
		}
		var sr searchReq
		if err := json.NewDecoder(r.Body).Decode(&sr); err != nil {
			t.Errorf("decode body: %v", err)
		}
		*reqs = append(*reqs, sr)
		status, body := respond(len(*reqs), sr)
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

const searchJQL = `project = "ACGL" AND labels = "ralph" AND labels = "automation-platform" AND statusCategory != Done ORDER BY created ASC`

var searchLabels = []string{"ralph", "automation-platform"}

func TestSearchOpenCardsOnePage(t *testing.T) {
	var reqs []searchReq
	srv := searchServer(t, &reqs, func(int, searchReq) (int, string) {
		return 200, `{"issues":[{"key":"ACGL-1","fields":{"summary":"T-1: a","status":{"name":"To Do"},"created":"2026-11-10T14:03:22.123+0000"}},{"key":"ACGL-2","fields":{"summary":"T-2: b","status":{"name":"In Progress"},"created":"2026-11-11T09:00:00.000-0500"}}]}`
	})
	got, err := newTestClient(t, srv.URL, time.Second).SearchOpenCards(context.Background(), "ACGL", searchLabels, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(reqs) != 1 {
		t.Fatalf("requests = %d", len(reqs))
	}
	r := reqs[0]
	if r.JQL != searchJQL || r.MaxResults != 100 || r.NextPageToken != "" || strings.Join(r.Fields, ",") != "summary,status,created" {
		t.Errorf("request = %+v", r)
	}
	want := []FoundIssue{
		{"ACGL-1", "T-1: a", "To Do", time.Date(2026, 11, 10, 14, 3, 22, 123e6, time.UTC)},
		{"ACGL-2", "T-2: b", "In Progress", time.Date(2026, 11, 11, 14, 0, 0, 0, time.UTC)},
	}
	if len(got) != len(want) {
		t.Fatalf("got %+v", got)
	}
	for i := range want {
		g := got[i]
		if g.Key != want[i].Key || g.Summary != want[i].Summary || g.Status != want[i].Status || !g.Created.Equal(want[i].Created) {
			t.Errorf("issue %d = %+v, want %+v", i, g, want[i])
		}
	}
}

func TestSearchOpenCardsCreatedAfter(t *testing.T) {
	var reqs []searchReq
	srv := searchServer(t, &reqs, func(int, searchReq) (int, string) { return 200, `{"issues":[]}` })
	c := newTestClient(t, srv.URL, time.Second)
	if _, err := c.SearchOpenCards(context.Background(), "ACGL", searchLabels, time.Date(2026, 1, 15, 0, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	want := `project = "ACGL" AND labels = "ralph" AND labels = "automation-platform" AND statusCategory != Done AND created >= "2026-01-15" ORDER BY created ASC`
	if reqs[0].JQL != want {
		t.Errorf("jql = %s", reqs[0].JQL)
	}
}

func TestSearchOpenCardsTwoPages(t *testing.T) {
	var reqs []searchReq
	srv := searchServer(t, &reqs, func(n int, _ searchReq) (int, string) {
		if n == 1 {
			return 200, `{"issues":[{"key":"ACGL-1","fields":{}}],"nextPageToken":"tok1"}`
		}
		return 200, `{"issues":[{"key":"ACGL-2","fields":{}}],"nextPageToken":""}`
	})
	got, err := newTestClient(t, srv.URL, time.Second).SearchOpenCards(context.Background(), "ACGL", searchLabels, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(reqs) != 2 || reqs[0].NextPageToken != "" || reqs[1].NextPageToken != "tok1" || reqs[1].JQL != searchJQL {
		t.Errorf("requests = %+v", reqs)
	}
	if len(got) != 2 || got[0].Key != "ACGL-1" || got[1].Key != "ACGL-2" {
		t.Errorf("got %+v", got)
	}
}

func TestSearchOpenCardsRepeatedToken(t *testing.T) {
	var reqs []searchReq
	srv := searchServer(t, &reqs, func(n int, _ searchReq) (int, string) {
		return 200, `{"issues":[],"nextPageToken":"same"}`
	})
	_, err := newTestClient(t, srv.URL, time.Second).SearchOpenCards(context.Background(), "ACGL", searchLabels, time.Time{})
	if err == nil || !strings.Contains(err.Error(), "repeated") {
		t.Fatalf("err = %v", err)
	}
	if len(reqs) != 2 {
		t.Errorf("requests = %d", len(reqs))
	}
}

func TestSearchOpenCardsPageLimit(t *testing.T) {
	var reqs []searchReq
	srv := searchServer(t, &reqs, func(n int, _ searchReq) (int, string) {
		return 200, `{"issues":[],"nextPageToken":"t` + strings.Repeat("x", n%7) + string(rune('a'+n%26)) + strings.Repeat("y", n/26) + `"}`
	})
	_, err := newTestClient(t, srv.URL, time.Second).SearchOpenCards(context.Background(), "ACGL", searchLabels, time.Time{})
	if err == nil || !strings.Contains(err.Error(), "result pages") {
		t.Fatalf("err = %v", err)
	}
	if len(reqs) != maxSearchPages {
		t.Errorf("requests = %d, want %d", len(reqs), maxSearchPages)
	}
}

func TestSearchOpenCardsEscapesLabels(t *testing.T) {
	var reqs []searchReq
	srv := searchServer(t, &reqs, func(int, searchReq) (int, string) { return 200, `{"issues":[]}` })
	c := newTestClient(t, srv.URL, time.Second)
	if _, err := c.SearchOpenCards(context.Background(), `P"Q`, []string{`a"b`, `c\d`}, time.Time{}); err != nil {
		t.Fatal(err)
	}
	want := `project = "P\"Q" AND labels = "a\"b" AND labels = "c\\d" AND statusCategory != Done ORDER BY created ASC`
	if reqs[0].JQL != want {
		t.Errorf("jql = %s", reqs[0].JQL)
	}
}

func TestSearchOpenCardsRejectsEmptyInput(t *testing.T) {
	var reqs []searchReq
	srv := searchServer(t, &reqs, func(int, searchReq) (int, string) { return 200, `{"issues":[]}` })
	c := newTestClient(t, srv.URL, time.Second)
	if _, err := c.SearchOpenCards(context.Background(), "ACGL", nil, time.Time{}); err == nil {
		t.Error("no labels: want error")
	}
	if _, err := c.SearchOpenCards(context.Background(), "", searchLabels, time.Time{}); err == nil {
		t.Error("empty project: want error")
	}
	if len(reqs) != 0 {
		t.Errorf("requests = %d", len(reqs))
	}
}

func TestSearchOpenCardsErrors(t *testing.T) {
	var reqs []searchReq
	srv := searchServer(t, &reqs, func(int, searchReq) (int, string) {
		return 400, `{"errorMessages":["bad jql"]}`
	})
	_, err := newTestClient(t, srv.URL, time.Second).SearchOpenCards(context.Background(), "ACGL", searchLabels, time.Time{})
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != 400 {
		t.Fatalf("err = %v", err)
	}
	if strings.Contains(err.Error(), testToken) || strings.Contains(err.Error(), authValue()) {
		t.Errorf("error leaks credentials: %v", err)
	}

	reqs = nil
	srv2 := searchServer(t, &reqs, func(int, searchReq) (int, string) { return 200, `not json` })
	_, err = newTestClient(t, srv2.URL, time.Second).SearchOpenCards(context.Background(), "ACGL", searchLabels, time.Time{})
	if err == nil || !strings.Contains(err.Error(), "response is not valid JSON") || strings.Contains(err.Error(), testToken) {
		t.Errorf("err = %v", err)
	}
}

func TestSearchOpenCardsBadCreated(t *testing.T) {
	var reqs []searchReq
	srv := searchServer(t, &reqs, func(int, searchReq) (int, string) {
		return 200, `{"issues":[{"key":"ACGL-1","fields":{"summary":"s","status":{"name":"To Do"},"created":"yesterday"}}]}`
	})
	got, err := newTestClient(t, srv.URL, time.Second).SearchOpenCards(context.Background(), "ACGL", searchLabels, time.Time{})
	if err != nil || len(got) != 1 || !got[0].Created.IsZero() || got[0].Key != "ACGL-1" {
		t.Fatalf("got %+v err=%v", got, err)
	}
}

func TestAgileBoardSprints(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		if r.Method != "GET" || r.URL.Path != "/rest/agile/1.0/board/12/sprint" {
			t.Errorf("got %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("Authorization") != authValue() {
			t.Errorf("auth header = %q", r.Header.Get("Authorization"))
		}
		q := r.URL.Query()
		if q.Get("state") != "active,future" || q.Get("maxResults") != "50" {
			t.Errorf("query = %v", q)
		}
		switch q.Get("startAt") {
		case "0":
			_, _ = w.Write([]byte(`{"isLast":false,"values":[{"id":7,"name":"Sprint 7","state":"active","startDate":"2026-11-02T09:00:00.000+10:00"},{"id":8,"name":"Sprint 8","state":"future"}]}`))
		case "2":
			_, _ = w.Write([]byte(`{"isLast":true,"values":[{"id":9,"name":"Sprint 9","state":"future","startDate":"not a date"}]}`))
		default:
			t.Errorf("startAt = %q", q.Get("startAt"))
		}
	}))
	defer srv.Close()
	got, err := newTestClient(t, srv.URL, time.Second).BoardSprints(context.Background(), 12, "active,future")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || atomic.LoadInt32(&calls) != 2 {
		t.Fatalf("got %+v after %d calls", got, calls)
	}
	want := time.Date(2026, 11, 1, 23, 0, 0, 0, time.UTC)
	if got[0].ID != 7 || got[0].Name != "Sprint 7" || got[0].State != "active" || !got[0].StartDate.Equal(want) {
		t.Errorf("first = %+v", got[0])
	}
	if got[1].ID != 8 || !got[1].StartDate.IsZero() {
		t.Errorf("absent startDate = %+v", got[1])
	}
	if got[2].ID != 9 || !got[2].StartDate.IsZero() {
		t.Errorf("unparseable startDate = %+v", got[2])
	}
}

func TestAgileBoardSprintsStopsOnEmptyPage(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		_, _ = w.Write([]byte(`{"isLast":false,"values":[]}`))
	}))
	defer srv.Close()
	got, err := newTestClient(t, srv.URL, time.Second).BoardSprints(context.Background(), 12, "active")
	if err != nil || len(got) != 0 || atomic.LoadInt32(&calls) != 1 {
		t.Fatalf("got %+v, %v after %d calls", got, err, calls)
	}
}

func TestAgileBoardSprintsInvalidJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`<html>`))
	}))
	defer srv.Close()
	_, err := newTestClient(t, srv.URL, time.Second).BoardSprints(context.Background(), 12, "active")
	if err == nil || !strings.Contains(err.Error(), "jira: GET /rest/agile/1.0/board/12/sprint: response is not valid JSON") {
		t.Fatalf("err = %v", err)
	}
}

func TestAgileCardSprint(t *testing.T) {
	cases := []struct {
		name, body string
		want       *Sprint
	}{
		{"object", `{"fields":{"sprint":{"id":7,"name":"Sprint 7","state":"active","startDate":"2026-11-02T09:00:00.000Z"}}}`,
			&Sprint{ID: 7, Name: "Sprint 7", State: "active", StartDate: time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)}},
		{"null", `{"fields":{"sprint":null}}`, nil},
		{"missing", `{"fields":{}}`, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var calls int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				atomic.AddInt32(&calls, 1)
				if r.Method != "GET" || r.URL.EscapedPath() != "/rest/agile/1.0/issue/AIN%2F1" || r.URL.Query().Get("fields") != "sprint" {
					t.Errorf("got %s %s", r.Method, r.URL.String())
				}
				if r.Header.Get("Authorization") != authValue() {
					t.Errorf("auth header = %q", r.Header.Get("Authorization"))
				}
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()
			got, err := newTestClient(t, srv.URL, time.Second).CardSprint(context.Background(), "AIN/1")
			if err != nil {
				t.Fatal(err)
			}
			if atomic.LoadInt32(&calls) != 1 {
				t.Errorf("calls = %d", calls)
			}
			switch {
			case tc.want == nil && got != nil:
				t.Errorf("got %+v, want nil", got)
			case tc.want != nil && (got == nil || got.ID != tc.want.ID || got.Name != tc.want.Name || got.State != tc.want.State || !got.StartDate.Equal(tc.want.StartDate)):
				t.Errorf("got %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestAgileAddToSprint(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		if r.Method != "POST" || r.URL.Path != "/rest/agile/1.0/sprint/5/issue" {
			t.Errorf("got %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("Authorization") != authValue() {
			t.Errorf("auth header = %q", r.Header.Get("Authorization"))
		}
		b, _ := io.ReadAll(r.Body)
		if string(b) != `{"issues":["AIN-1"]}` {
			t.Errorf("body = %s", b)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	if err := newTestClient(t, srv.URL, time.Second).AddToSprint(context.Background(), 5, "AIN-1"); err != nil {
		t.Fatal(err)
	}
	if atomic.LoadInt32(&calls) != 1 {
		t.Errorf("calls = %d", calls)
	}
}

func TestAgileErrorHints(t *testing.T) {
	cases := []struct {
		name   string
		status int
		call   func(c *Client) error
		want   string
		not    string
	}{
		{"board 404", 404, func(c *Client) error { _, err := c.BoardSprints(context.Background(), 12, "active"); return err },
			"task_manager.jira.sprint.board", "project_key"},
		{"board 401", 401, func(c *Client) error { _, err := c.BoardSprints(context.Background(), 12, "active"); return err },
			"task_manager.jira.sprint.board", "create issues"},
		{"board 403", 403, func(c *Client) error { _, err := c.BoardSprints(context.Background(), 12, "active"); return err },
			"Jira Software project", "create issues"},
		{"add 403", 403, func(c *Client) error { return c.AddToSprint(context.Background(), 5, "AIN-1") },
			"permission to schedule issues", "create issues"},
		{"add 404 unchanged", 404, func(c *Client) error { return c.AddToSprint(context.Background(), 5, "AIN-1") },
			"task_manager.jira.base_url", "schedule"},
		{"card 403 unchanged", 403, func(c *Client) error { _, err := c.CardSprint(context.Background(), "AIN-1"); return err },
			"create issues in the project", "schedule"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var calls int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				atomic.AddInt32(&calls, 1)
				w.WriteHeader(tc.status)
			}))
			defer srv.Close()
			err := tc.call(newTestClient(t, srv.URL, time.Second))
			var apiErr *APIError
			if !errors.As(err, &apiErr) || apiErr.Status != tc.status {
				t.Fatalf("err = %v", err)
			}
			msg := err.Error()
			if !strings.Contains(msg, tc.want) || strings.Contains(msg, tc.not) {
				t.Errorf("message = %q, want %q and not %q", msg, tc.want, tc.not)
			}
			if strings.Contains(msg, testToken) || strings.Contains(msg, testEmail) || strings.Contains(msg, authValue()) {
				t.Errorf("credential in error: %q", msg)
			}
			if atomic.LoadInt32(&calls) != 1 {
				t.Errorf("calls = %d, want one attempt", calls)
			}
		})
	}
}
