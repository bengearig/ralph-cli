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
	"unicode/utf8"

	"github.com/bengearig/ralph/internal/config"
	"github.com/bengearig/ralph/internal/domain"
	"github.com/bengearig/ralph/internal/taskmanager"
)

type recorded struct {
	method, path string
	body         map[string]any
}

type fakeJira struct {
	*httptest.Server
	mu   sync.Mutex
	reqs []recorded
}

func newFakeJira(t *testing.T) *fakeJira {
	t.Helper()
	f := &fakeJira{}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var m map[string]any
		_ = json.Unmarshal(b, &m)
		f.mu.Lock()
		f.reqs = append(f.reqs, recorded{r.Method, r.URL.Path, m})
		f.mu.Unlock()
		if r.Method == http.MethodPost {
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"key":"PROJ-7"}`))
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(f.Close)
	return f
}

func (f *fakeJira) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.reqs)
}

func (f *fakeJira) last(t *testing.T) recorded {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.reqs) == 0 {
		t.Fatal("no request recorded")
	}
	return f.reqs[len(f.reqs)-1]
}

func (r recorded) fields() map[string]any { return r.body["fields"].(map[string]any) }

func (r recorded) description() string {
	b, _ := json.Marshal(r.fields()["description"])
	return string(b)
}

func testCfg(base string) config.JiraConfig {
	return config.JiraConfig{
		BaseURL:    base,
		ProjectKey: "PROJ",
		EmailEnv:   "RALPH_JIRA_EMAIL",
		TokenEnv:   "RALPH_JIRA_TOKEN",
		IssueTypes: config.JiraIssueTypes{Initiative: "Epic", Milestone: "Task", Task: "Sub-task"},
	}
}

func newTestProvider(base string) *Provider {
	return NewProvider(testCfg(base), 0, env(goodEnv), nil)
}

func TestIssueTypes(t *testing.T) {
	f := newFakeJira(t)
	ctx := context.Background()
	cases := []struct {
		kind domain.Kind
		want string
	}{{domain.KindInitiative, "Epic"}, {domain.KindMilestone, "Task"}, {domain.KindTask, "Sub-task"}}
	for _, c := range cases {
		p := newTestProvider(f.URL)
		if _, err := p.Create(ctx, taskmanager.CardRequest{Kind: c.kind, ID: "X-1", Title: "t", ParentKey: "PROJ-1"}); err != nil {
			t.Fatal(err)
		}
		if got := f.last(t).fields()["issuetype"].(map[string]any)["name"]; got != c.want {
			t.Errorf("%s: issue type %v, want %s", c.kind, got, c.want)
		}
	}
	cfg := testCfg(f.URL)
	cfg.IssueTypes = config.JiraIssueTypes{Initiative: "Feature", Milestone: "Story", Task: "Subtask"}
	p := NewProvider(cfg, 0, env(goodEnv), nil)
	for i, c := range cases {
		want := []string{"Feature", "Story", "Subtask"}[i]
		if _, err := p.Create(ctx, taskmanager.CardRequest{Kind: c.kind, ID: "X-1", Title: "t", ParentKey: "PROJ-1"}); err != nil {
			t.Fatal(err)
		}
		if got := f.last(t).fields()["issuetype"].(map[string]any)["name"]; got != want {
			t.Errorf("custom %s: issue type %v, want %s", c.kind, got, want)
		}
	}
}

func TestSummaryLabelsParent(t *testing.T) {
	f := newFakeJira(t)
	p := newTestProvider(f.URL)
	ctx := context.Background()
	if _, err := p.Create(ctx, taskmanager.CardRequest{Kind: domain.KindTask, ID: "T-12", Title: "Add X", ParentKey: "PROJ-5"}); err != nil {
		t.Fatal(err)
	}
	fl := f.last(t).fields()
	if fl["summary"] != "T-12: Add X" {
		t.Errorf("summary %v", fl["summary"])
	}
	if l := fl["labels"].([]any); len(l) != 2 || l[0] != "ralph" || l[1] != "T-12" {
		t.Errorf("labels %v", l)
	}
	if fl["parent"].(map[string]any)["key"] != "PROJ-5" {
		t.Errorf("parent %v", fl["parent"])
	}
	if fl["project"].(map[string]any)["key"] != "PROJ" {
		t.Errorf("project %v", fl["project"])
	}

	if _, err := p.Create(ctx, taskmanager.CardRequest{Kind: domain.KindInitiative, ID: "I-1", Title: "x", ParentKey: "PROJ-5"}); err != nil {
		t.Fatal(err)
	}
	if _, ok := f.last(t).fields()["parent"]; ok {
		t.Error("initiative sent a parent")
	}
}

func TestMissingParentFailsBeforeHTTP(t *testing.T) {
	f := newFakeJira(t)
	p := newTestProvider(f.URL)
	for _, k := range []domain.Kind{domain.KindMilestone, domain.KindTask} {
		req := taskmanager.CardRequest{Kind: k, ID: "Z-9", Title: "x"}
		if _, err := p.Create(context.Background(), req); err == nil || !strings.Contains(err.Error(), "Z-9") {
			t.Errorf("%s create: err %v", k, err)
		}
		if err := p.Update(context.Background(), "PROJ-1", req); err == nil || !strings.Contains(err.Error(), "Z-9") {
			t.Errorf("%s update: err %v", k, err)
		}
	}
	if n := f.count(); n != 0 {
		t.Errorf("server saw %d requests", n)
	}
}

func TestUnsupportedKind(t *testing.T) {
	f := newFakeJira(t)
	p := newTestProvider(f.URL)
	if _, err := p.Create(context.Background(), taskmanager.CardRequest{Kind: "doc", ID: "D-1"}); err == nil {
		t.Fatal("expected error")
	}
	if f.count() != 0 {
		t.Error("request made")
	}
}

func TestSummaryTruncation(t *testing.T) {
	for _, ch := range []string{"a", "é", "日"} {
		s := summary(taskmanager.CardRequest{ID: "T-1", Title: strings.Repeat(ch, 400)})
		if n := utf8.RuneCountInString(s); n != 255 {
			t.Errorf("%q: %d runes", ch, n)
		}
		if !strings.HasSuffix(s, "…") || !utf8.ValidString(s) {
			t.Errorf("%q: bad suffix or utf8", ch)
		}
	}
	exact := "T-1: " + strings.Repeat("x", 250)
	if got := summary(taskmanager.CardRequest{ID: "T-1", Title: strings.Repeat("x", 250)}); got != exact {
		t.Error("255-rune summary was altered")
	}
}

func TestDescription(t *testing.T) {
	f := newFakeJira(t)
	p := newTestProvider(f.URL)
	ctx := context.Background()
	req := func(desc string) taskmanager.CardRequest {
		return taskmanager.CardRequest{Kind: domain.KindInitiative, ID: "I-3", Title: "t", Description: desc}
	}

	if _, err := p.Create(ctx, req("Hello body\n\n- one\n- two")); err != nil {
		t.Fatal(err)
	}
	d := f.last(t).description()
	for _, want := range []string{`"type":"doc"`, "Hello body", `"bulletList"`, "Managed by ralph (I-3)", "edit them in ralph", `"rule"`} {
		if !strings.Contains(d, want) {
			t.Errorf("description lacks %q: %s", want, d)
		}
	}
	if strings.Contains(d, "truncated, see ralph") {
		t.Error("short body marked truncated")
	}

	if _, err := p.Create(ctx, req("")); err != nil {
		t.Fatal(err)
	}
	doc := f.last(t).fields()["description"].(map[string]any)
	content := doc["content"].([]any)
	if len(content) != 2 || content[0].(map[string]any)["type"] != "rule" {
		t.Errorf("empty body description: %v", content)
	}

	if _, err := p.Create(ctx, req(strings.Repeat("日", 30001))); err != nil {
		t.Fatal(err)
	}
	d = f.last(t).description()
	if !strings.Contains(d, "truncated, see ralph") || !strings.Contains(d, "Managed by ralph (I-3)") {
		t.Error("long body not truncated with footer")
	}
	if strings.Contains(d, strings.Repeat("日", 30001)) {
		t.Error("body not cut")
	}
	if _, err := p.Create(ctx, req(strings.Repeat("日", 30000))); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(f.last(t).description(), "truncated, see ralph") {
		t.Error("body at the limit was truncated")
	}
}

func TestCreateResultURL(t *testing.T) {
	f := newFakeJira(t)
	for _, base := range []string{f.URL, f.URL + "/"} {
		p := newTestProvider(base)
		card, err := p.Create(context.Background(), taskmanager.CardRequest{Kind: domain.KindInitiative, ID: "I-1", Title: "t"})
		if err != nil {
			t.Fatal(err)
		}
		if card.Key != "PROJ-7" || card.URL != f.URL+"/browse/PROJ-7" {
			t.Errorf("card %+v", card)
		}
	}
}

func TestUpdate(t *testing.T) {
	f := newFakeJira(t)
	p := newTestProvider(f.URL)
	long := strings.Repeat("y", 400)
	err := p.Update(context.Background(), "PROJ-9", taskmanager.CardRequest{Kind: domain.KindTask, ID: "T-4", Title: long, Description: "body", ParentKey: "PROJ-1"})
	if err != nil {
		t.Fatal(err)
	}
	r := f.last(t)
	if r.method != http.MethodPut || r.path != "/rest/api/3/issue/PROJ-9" {
		t.Errorf("%s %s", r.method, r.path)
	}
	fl := r.fields()
	if len(fl) != 2 {
		t.Errorf("fields %v", fl)
	}
	if s := fl["summary"].(string); utf8.RuneCountInString(s) != 255 || !strings.HasSuffix(s, "…") {
		t.Errorf("summary %q", s)
	}
	if !strings.Contains(r.description(), "body") || !strings.Contains(r.description(), "T-4") {
		t.Errorf("description %s", r.description())
	}
}

func TestValidate(t *testing.T) {
	f := newFakeJira(t)
	if err := newTestProvider(f.URL).Validate(); err != nil {
		t.Fatalf("valid config: %v", err)
	}

	cfg := testCfg("not a url")
	cfg.ProjectKey = " "
	cfg.IssueTypes.Milestone = ""
	p := NewProvider(cfg, 0, env(map[string]string{"RALPH_JIRA_EMAIL": testEmail}), nil)
	err := p.Validate()
	if err == nil {
		t.Fatal("expected error")
	}
	for _, want := range []string{"base_url", "project_key", "issue_types.milestone", "RALPH_JIRA_TOKEN"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error lacks %q: %v", want, err)
		}
	}
	if strings.Contains(err.Error(), testEmail) {
		t.Error("error leaks a credential value")
	}
	if f.count() != 0 {
		t.Error("Validate made a request")
	}
}

func TestConstructionWithoutCredentials(t *testing.T) {
	f := newFakeJira(t)
	p := NewProvider(testCfg(f.URL), 0, env(nil), nil)
	if p.Name() != config.TaskManagerJira {
		t.Errorf("name %q", p.Name())
	}
	if f.count() != 0 {
		t.Error("constructor contacted server")
	}
	_, err := p.Create(context.Background(), taskmanager.CardRequest{Kind: domain.KindInitiative, ID: "I-1", Title: "t"})
	if err == nil || !strings.Contains(err.Error(), "RALPH_JIRA_EMAIL") {
		t.Errorf("err %v", err)
	}
	if f.count() != 0 {
		t.Error("request made without credentials")
	}
}

func TestRegistry(t *testing.T) {
	cfg, err := config.Defaults()
	if err != nil {
		t.Fatal(err)
	}
	cfg.TaskManager.Provider = config.TaskManagerJira
	p, err := taskmanager.New(cfg)
	if err != nil || p == nil || p.Name() != "jira" {
		t.Fatalf("New: %v, %v", p, err)
	}
	cfg.TaskManager.Provider = config.TaskManagerNone
	if p, err := taskmanager.New(cfg); p != nil || err != nil {
		t.Errorf("none: %v, %v", p, err)
	}
}

type statusJira struct {
	*httptest.Server
	mu   sync.Mutex
	reqs []string
}

func newStatusJira(t *testing.T) *statusJira {
	t.Helper()
	f := &statusJira{}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.reqs = append(f.reqs, r.Method+" "+r.URL.Path)
		f.mu.Unlock()
		switch {
		case r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/transitions"):
			_, _ = w.Write([]byte(`{"transitions":[{"id":"21","to":{"name":"In Progress"}},{"id":"41","to":{"name":"Cancelled"}}]}`))
		case r.Method == "GET":
			_, _ = w.Write([]byte(`{"fields":{"status":{"name":"To Do"}}}`))
		default:
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	t.Cleanup(f.Close)
	return f
}

func (f *statusJira) requests() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.reqs...)
}

func TestSetStatus(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name     string
		mutate   func(*config.JiraConfig)
		status   string
		wantReqs int
	}{
		{"mapped", func(c *config.JiraConfig) {}, "IN_PROGRESS", 3},
		{"unmapped", func(c *config.JiraConfig) {}, "BLOCKED", 0},
		{"blank mapping", func(c *config.JiraConfig) { c.StatusMap["IN_PROGRESS"] = " " }, "IN_PROGRESS", 0},
		{"cancelled uses canceled_status", func(c *config.JiraConfig) { c.CanceledStatus = "Cancelled" }, "CANCELLED", 3},
		{"cancelled blank", func(c *config.JiraConfig) {}, "CANCELLED", 0},
		{"cancelled ignores map", func(c *config.JiraConfig) { c.StatusMap["CANCELLED"] = "Cancelled" }, "CANCELLED", 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newStatusJira(t)
			cfg := testCfg(f.URL)
			cfg.StatusMap = map[string]string{"TODO": "To Do", "IN_PROGRESS": "In Progress", "DONE": "Done"}
			tc.mutate(&cfg)
			p := NewProvider(cfg, 0, env(goodEnv), nil)
			if err := p.SetStatus(ctx, "PROJ-1", tc.status); err != nil {
				t.Fatal(err)
			}
			if got := f.requests(); len(got) != tc.wantReqs {
				t.Errorf("requests = %v, want %d", got, tc.wantReqs)
			}
		})
	}
}

func TestSetStatusTransitionsToMappedName(t *testing.T) {
	f := newStatusJira(t)
	cfg := testCfg(f.URL)
	cfg.StatusMap = map[string]string{"IN_PROGRESS": "In Progress"}
	if err := NewProvider(cfg, 0, env(goodEnv), nil).SetStatus(context.Background(), "PROJ-1", "IN_PROGRESS"); err != nil {
		t.Fatal(err)
	}
	got := f.requests()
	if len(got) != 3 || got[2] != "POST /rest/api/3/issue/PROJ-1/transitions" {
		t.Errorf("requests = %v", got)
	}
}
