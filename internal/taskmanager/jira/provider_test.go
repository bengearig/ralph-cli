package jira

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
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

	projectStatus int
	projectBody   string
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
		if r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/rest/api/3/project/") {
			f.mu.Lock()
			status, body := f.projectStatus, f.projectBody
			f.mu.Unlock()
			w.WriteHeader(status)
			_, _ = w.Write([]byte(body))
			return
		}
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

func (f *fakeJira) setProject(status int, body string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.projectStatus, f.projectBody = status, body
}

const ainTypes = `{"issueTypes":[{"name":"Feature","hierarchyLevel":1},{"name":"Task","hierarchyLevel":0},{"name":"Bug","hierarchyLevel":0},{"name":"Sub-task","hierarchyLevel":-1},{"name":"Epic","hierarchyLevel":2}]}`

func TestPreflight(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		types  config.JiraIssueTypes
		want   []string
	}{
		{"adjacent levels", 200, ainTypes, config.JiraIssueTypes{Initiative: "Feature", Milestone: "Task", Task: "Sub-task"}, nil},
		{"case-insensitive names", 200, ainTypes, config.JiraIssueTypes{Initiative: "feature", Milestone: "TASK", Task: "sub-task"}, nil},
		{"epic above task", 200, ainTypes, config.JiraIssueTypes{Initiative: "Epic", Milestone: "Task", Task: "Sub-task"},
			[]string{`initiative type "Epic" is level 2`, `milestone type "Task" is level 0`, "must be level 1 (the project has: Feature)"}},
		{"no type at the level", 200, ainTypes, config.JiraIssueTypes{Initiative: "Feature", Milestone: "Feature", Task: "Sub-task"},
			[]string{`milestone type "Feature" is level 1`, "must be level 0"}},
		{"unknown type", 200, ainTypes, config.JiraIssueTypes{Initiative: "Theme", Milestone: "Task", Task: "Sub-task"},
			[]string{`issue_types.initiative "Theme" is not an issue type of project PROJ`, "Bug, Epic, Feature, Sub-task, Task"}},
		{"levels absent", 200, `{"issueTypes":[{"name":"Epic"},{"name":"Task"},{"name":"Sub-task"}]}`, config.JiraIssueTypes{Initiative: "Epic", Milestone: "Task", Task: "Sub-task"}, nil},
		{"levels absent still checks names", 200, `{"issueTypes":[{"name":"Task"}]}`, config.JiraIssueTypes{Initiative: "Epic", Milestone: "Task", Task: "Sub-task"},
			[]string{`issue_types.initiative "Epic"`, `issue_types.task "Sub-task"`}},
		{"forbidden", 403, `{"errorMessages":["Forbidden"]}`, config.JiraIssueTypes{Initiative: "Epic", Milestone: "Task", Task: "Sub-task"},
			[]string{"403 Forbidden"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeJira(t)
			f.setProject(tc.status, tc.body)
			cfg := testCfg(f.URL)
			cfg.IssueTypes = tc.types
			err := NewProvider(cfg, 0, env(goodEnv), nil).Preflight(context.Background())
			if tc.want == nil {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("expected error")
			}
			for _, w := range tc.want {
				if !strings.Contains(err.Error(), w) {
					t.Errorf("%q missing %q", err, w)
				}
			}
		})
	}
}

func TestPreflightMissingCredentials(t *testing.T) {
	f := newFakeJira(t)
	err := NewProvider(testCfg(f.URL), 0, env(nil), nil).Preflight(context.Background())
	if err == nil || !strings.Contains(err.Error(), "RALPH_JIRA_EMAIL") || f.count() != 0 {
		t.Errorf("err = %v, requests = %d", err, f.count())
	}
}

func TestConfiguredLabels(t *testing.T) {
	f := newFakeJira(t)
	cfg := testCfg(f.URL)
	cfg.Labels = []string{"workflow-2.0", " ralph ", "workflow-2.0", ""}
	p := NewProvider(cfg, 0, env(goodEnv), nil)
	if _, err := p.Create(context.Background(), taskmanager.CardRequest{Kind: domain.KindTask, ID: "T-12", Title: "t", ParentKey: "PROJ-5"}); err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, l := range f.last(t).fields()["labels"].([]any) {
		got = append(got, l.(string))
	}
	if want := []string{"workflow-2.0", "ralph"}; !slices.Equal(got, want) {
		t.Errorf("labels %v, want %v", got, want)
	}

	if _, err := newTestProvider(f.URL).Create(context.Background(), taskmanager.CardRequest{Kind: domain.KindTask, ID: "T-12", Title: "t", ParentKey: "PROJ-5"}); err != nil {
		t.Fatal(err)
	}
	if l, ok := f.last(t).fields()["labels"]; ok {
		t.Errorf("labels %v sent with none configured", l)
	}

	cfg.Labels = []string{"two words"}
	if err := NewProvider(cfg, 0, env(goodEnv), nil).Validate(); err == nil || !strings.Contains(err.Error(), "task_manager.jira.labels") {
		t.Errorf("Validate = %v, want a labels error", err)
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
	if fl["parent"].(map[string]any)["key"] != "PROJ-5" {
		t.Errorf("parent %v", fl["parent"])
	}
	if fl["project"].(map[string]any)["key"] != "PROJ" {
		t.Errorf("project %v", fl["project"])
	}

	if _, err := p.Create(ctx, taskmanager.CardRequest{Kind: domain.KindInitiative, ID: "I-1", Title: "x", ParentKey: "PROJ-5"}); err != nil {
		t.Fatal(err)
	}
	if got := f.last(t).fields()["parent"].(map[string]any)["key"]; got != "PROJ-5" {
		t.Errorf("initiative parent %v", got)
	}

	if _, err := p.Create(ctx, taskmanager.CardRequest{Kind: domain.KindInitiative, ID: "I-2", Title: "x"}); err != nil {
		t.Fatal(err)
	}
	if _, ok := f.last(t).fields()["parent"]; ok {
		t.Error("initiative without a parent sent one")
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

func TestUpdateParent(t *testing.T) {
	tests := []struct {
		name         string
		kind         domain.Kind
		parent, prev string
		wantKey      string
		wantNull     bool
	}{
		{"set", domain.KindInitiative, "PROJ-5", "", "PROJ-5", false},
		{"unchanged", domain.KindInitiative, "PROJ-5", "PROJ-5", "", false},
		{"changed", domain.KindInitiative, "PROJ-6", "PROJ-5", "PROJ-6", false},
		{"clear", domain.KindInitiative, "", "PROJ-5", "", true},
		{"never had one", domain.KindInitiative, "", "", "", false},
		{"milestone ignores", domain.KindMilestone, "PROJ-5", "", "", false},
		{"milestone ignores change", domain.KindMilestone, "PROJ-6", "PROJ-5", "", false},
		{"task ignores", domain.KindTask, "PROJ-5", "PROJ-4", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFakeJira(t)
			p := newTestProvider(f.URL)
			req := taskmanager.CardRequest{Kind: tt.kind, ID: "X-1", Title: "t", ParentKey: tt.parent, PreviousParentKey: tt.prev}
			if err := p.Update(context.Background(), "PROJ-9", req); err != nil {
				t.Fatal(err)
			}
			r := f.last(t)
			fl := r.fields()
			if _, ok := fl["summary"]; !ok {
				t.Errorf("no summary: %v", fl)
			}
			if _, ok := fl["description"]; !ok {
				t.Errorf("no description: %v", fl)
			}
			par, has := fl["parent"]
			switch {
			case tt.wantKey != "":
				if m, _ := par.(map[string]any); !has || m["key"] != tt.wantKey {
					t.Errorf("parent = %v", par)
				}
			case tt.wantNull:
				if !has || par != nil {
					t.Errorf("parent = %v (present %v), want null", par, has)
				}
			default:
				if has {
					t.Errorf("unexpected parent %v", par)
				}
			}
		})
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
		{"blank mapping", func(c *config.JiraConfig) { c.StatusMap["IN_PROGRESS"] = []string{" "} }, "IN_PROGRESS", 0},
		{"cancelled uses canceled_status", func(c *config.JiraConfig) { c.CanceledStatus = "Cancelled" }, "CANCELLED", 3},
		{"cancelled blank", func(c *config.JiraConfig) {}, "CANCELLED", 0},
		{"cancelled ignores map", func(c *config.JiraConfig) { c.StatusMap["CANCELLED"] = []string{"Cancelled"} }, "CANCELLED", 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newStatusJira(t)
			cfg := testCfg(f.URL)
			cfg.StatusMap = map[string][]string{"TODO": {"To Do"}, "IN_PROGRESS": {"In Progress"}, "DONE": {"Done"}}
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
	cfg.StatusMap = map[string][]string{"IN_PROGRESS": {"In Progress"}}
	if err := NewProvider(cfg, 0, env(goodEnv), nil).SetStatus(context.Background(), "PROJ-1", "IN_PROGRESS"); err != nil {
		t.Fatal(err)
	}
	got := f.requests()
	if len(got) != 3 || got[2] != "POST /rest/api/3/issue/PROJ-1/transitions" {
		t.Errorf("requests = %v", got)
	}
}

func TestSetStatusFillsEstimateBeforeMoving(t *testing.T) {
	var mu sync.Mutex
	var reqs []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		reqs = append(reqs, r.Method+" "+r.URL.Path)
		mu.Unlock()
		switch {
		case r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/transitions"):
			_, _ = w.Write([]byte(`{"transitions":[{"id":"21","to":{"name":"To Do"}}]}`))
		case r.Method == "GET" && r.URL.RawQuery == "fields=status":
			_, _ = w.Write([]byte(`{"fields":{"status":{"name":"To Be Sized"}}}`))
		case r.Method == "GET":
			_, _ = w.Write([]byte(`{"fields":{"issuetype":{"name":"Sub-task"},"timetracking":{}}}`))
		default:
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	defer srv.Close()
	cfg := testCfg(srv.URL)
	cfg.StatusMap = map[string][]string{"TODO": {"To Do"}}
	cfg.OriginalEstimate = config.JiraEstimates{Task: "5m", Milestone: "1m"}
	if err := NewProvider(cfg, 0, env(goodEnv), nil).SetStatus(context.Background(), "PROJ-1", "TODO"); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	want := []string{"GET /rest/api/3/issue/PROJ-1", "GET /rest/api/3/issue/PROJ-1", "PUT /rest/api/3/issue/PROJ-1", "GET /rest/api/3/issue/PROJ-1/transitions", "POST /rest/api/3/issue/PROJ-1/transitions"}
	if strings.Join(reqs, "|") != strings.Join(want, "|") {
		t.Errorf("requests = %v", reqs)
	}

	reqs = nil
	cfg.OriginalEstimate = config.JiraEstimates{}
	mu.Unlock()
	if err := NewProvider(cfg, 0, env(goodEnv), nil).SetStatus(context.Background(), "PROJ-1", "TODO"); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	for _, r := range reqs {
		if strings.HasPrefix(r, "PUT") {
			t.Errorf("estimate sent with none configured: %v", reqs)
		}
	}
}

type assignJira struct {
	*httptest.Server
	mu         sync.Mutex
	reqs       []string
	putBody    string
	assignee   string
	myselfCode int
	putCode    int
}

func newAssignJira(t *testing.T, assignee string) *assignJira {
	t.Helper()
	f := &assignJira{assignee: assignee, myselfCode: http.StatusOK, putCode: http.StatusNoContent}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.reqs = append(f.reqs, r.Method+" "+r.URL.Path)
		switch {
		case r.URL.Path == "/rest/api/3/myself":
			if f.myselfCode != http.StatusOK {
				w.WriteHeader(f.myselfCode)
				return
			}
			_, _ = w.Write([]byte(`{"accountId":"me-1","displayName":"Me"}`))
		case r.Method == "PUT" && strings.HasSuffix(r.URL.Path, "/assignee"):
			b, _ := io.ReadAll(r.Body)
			f.putBody = string(b)
			w.WriteHeader(f.putCode)
		case r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/transitions"):
			_, _ = w.Write([]byte(`{"transitions":[{"id":"21","to":{"name":"In Progress"}},{"id":"31","to":{"name":"Done"}},{"id":"41","to":{"name":"Cancelled"}}]}`))
		case r.Method == "GET" && r.URL.RawQuery == "fields=assignee":
			_, _ = w.Write([]byte(`{"fields":{"assignee":` + f.assignee + `}}`))
		case r.Method == "GET":
			_, _ = w.Write([]byte(`{"fields":{"status":{"name":"To Do"}}}`))
		default:
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	t.Cleanup(f.Close)
	return f
}

func (f *assignJira) requests() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.reqs...)
}

func (f *assignJira) has(prefix string) bool {
	for _, r := range f.requests() {
		if strings.HasPrefix(r, prefix) {
			return true
		}
	}
	return false
}

func assignProvider(f *assignJira, mutate func(*config.JiraConfig)) *Provider {
	cfg := testCfg(f.URL)
	cfg.AssignOnStart = true
	cfg.StatusMap = map[string][]string{"TODO": {"To Do"}, "IN_PROGRESS": {"In Progress"}, "DONE": {"Done"}}
	if mutate != nil {
		mutate(&cfg)
	}
	return NewProvider(cfg, 0, env(goodEnv), nil)
}

func TestSetStatusAssignsUnassignedCard(t *testing.T) {
	f := newAssignJira(t, "null")
	if err := assignProvider(f, nil).SetStatus(context.Background(), "PROJ-1", "IN_PROGRESS"); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"GET /rest/api/3/myself",
		"GET /rest/api/3/issue/PROJ-1",
		"PUT /rest/api/3/issue/PROJ-1/assignee",
		"GET /rest/api/3/issue/PROJ-1",
		"GET /rest/api/3/issue/PROJ-1/transitions",
		"POST /rest/api/3/issue/PROJ-1/transitions",
	}
	if got := f.requests(); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("requests = %v, want %v", got, want)
	}
	if f.putBody != `{"accountId":"me-1"}` {
		t.Errorf("assignee body = %s", f.putBody)
	}
}

func TestSetStatusKeepsOwnAssignment(t *testing.T) {
	f := newAssignJira(t, `{"accountId":"me-1","displayName":"Me"}`)
	if err := assignProvider(f, nil).SetStatus(context.Background(), "PROJ-1", "IN_PROGRESS"); err != nil {
		t.Fatal(err)
	}
	if f.has("PUT") {
		t.Errorf("unexpected assignment: %v", f.requests())
	}
	if !f.has("POST /rest/api/3/issue/PROJ-1/transitions") {
		t.Errorf("card not moved: %v", f.requests())
	}
}

func TestSetStatusAssigneeConflict(t *testing.T) {
	for _, tc := range []struct{ name, assignee, want string }{
		{"display name", `{"accountId":"pat-1","displayName":"Pat Lee"}`, "Pat Lee"},
		{"account ID fallback", `{"accountId":"pat-1"}`, "pat-1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newAssignJira(t, tc.assignee)
			err := assignProvider(f, nil).SetStatus(context.Background(), "PROJ-1", "IN_PROGRESS")
			var conflict *taskmanager.AssigneeConflictError
			if !errors.As(err, &conflict) {
				t.Fatalf("err = %v, want AssigneeConflictError", err)
			}
			if conflict.Key != "PROJ-1" || conflict.Assignee != tc.want {
				t.Errorf("conflict = %+v", conflict)
			}
			if f.has("PUT") || f.has("POST") {
				t.Errorf("card was changed: %v", f.requests())
			}
		})
	}
}

func TestAssigneeConflictErrorMessage(t *testing.T) {
	err := &taskmanager.AssigneeConflictError{Key: "AIN-12", Assignee: "Pat Lee"}
	if got := err.Error(); got != "card AIN-12 is already assigned to Pat Lee" {
		t.Errorf("Error() = %q", got)
	}
}

func TestSetStatusAssignsWithBlankInProgressMapping(t *testing.T) {
	f := newAssignJira(t, "null")
	p := assignProvider(f, func(c *config.JiraConfig) { c.StatusMap["IN_PROGRESS"] = []string{" "} })
	if err := p.SetStatus(context.Background(), "PROJ-1", "IN_PROGRESS"); err != nil {
		t.Fatal(err)
	}
	if !f.has("PUT /rest/api/3/issue/PROJ-1/assignee") || f.has("POST") {
		t.Errorf("requests = %v", f.requests())
	}
}

func TestSetStatusResolvesTokenUserOnce(t *testing.T) {
	f := newAssignJira(t, `{"accountId":"me-1"}`)
	p := assignProvider(f, nil)
	for range 2 {
		if err := p.SetStatus(context.Background(), "PROJ-1", "IN_PROGRESS"); err != nil {
			t.Fatal(err)
		}
	}
	n := 0
	for _, r := range f.requests() {
		if r == "GET /rest/api/3/myself" {
			n++
		}
	}
	if n != 1 {
		t.Errorf("myself requests = %d, want 1", n)
	}
}

func TestSetStatusRetriesTokenUserAfterFailure(t *testing.T) {
	f := newAssignJira(t, `{"accountId":"me-1"}`)
	f.myselfCode = http.StatusInternalServerError
	p := assignProvider(f, nil)
	err := p.SetStatus(context.Background(), "PROJ-1", "IN_PROGRESS")
	var conflict *taskmanager.AssigneeConflictError
	if err == nil || errors.As(err, &conflict) {
		t.Fatalf("err = %v, want ordinary failure", err)
	}
	f.mu.Lock()
	f.myselfCode = http.StatusOK
	f.mu.Unlock()
	if err := p.SetStatus(context.Background(), "PROJ-1", "IN_PROGRESS"); err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, r := range f.requests() {
		if r == "GET /rest/api/3/myself" {
			n++
		}
	}
	if n != 2 {
		t.Errorf("myself requests = %d, want 2", n)
	}
}

func TestSetStatusAssignForbiddenIsNotConflict(t *testing.T) {
	f := newAssignJira(t, "null")
	f.putCode = http.StatusForbidden
	err := assignProvider(f, nil).SetStatus(context.Background(), "PROJ-1", "IN_PROGRESS")
	var conflict *taskmanager.AssigneeConflictError
	if err == nil || errors.As(err, &conflict) {
		t.Fatalf("err = %v, want ordinary failure", err)
	}
	if f.has("POST") {
		t.Errorf("card moved after failed assignment: %v", f.requests())
	}
}

func TestSetStatusAssignsOnlyForInProgress(t *testing.T) {
	for _, status := range []string{"TODO", "DONE", "CANCELLED"} {
		t.Run(status, func(t *testing.T) {
			f := newAssignJira(t, "null")
			p := assignProvider(f, func(c *config.JiraConfig) { c.CanceledStatus = "Cancelled" })
			if err := p.SetStatus(context.Background(), "PROJ-1", status); err != nil {
				t.Fatal(err)
			}
			for _, r := range f.requests() {
				if strings.Contains(r, "myself") || strings.HasSuffix(r, "/assignee") {
					t.Errorf("unexpected request %s", r)
				}
			}
		})
	}
}

type findJira struct {
	*httptest.Server
	mu     sync.Mutex
	bodies []map[string]any
	status int
	resp   string
}

func newFindJira(t *testing.T, status int, resp string) *findJira {
	t.Helper()
	f := &findJira{status: status, resp: resp}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var m map[string]any
		_ = json.Unmarshal(b, &m)
		f.mu.Lock()
		f.bodies = append(f.bodies, m)
		f.mu.Unlock()
		if r.URL.Path != "/rest/api/3/search/jql" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.WriteHeader(f.status)
		_, _ = w.Write([]byte(f.resp))
	}))
	t.Cleanup(f.Close)
	return f
}

func findProvider(base string, labels ...string) *Provider {
	cfg := testCfg(base + "/")
	cfg.Labels = labels
	return NewProvider(cfg, 0, env(goodEnv), nil)
}

func TestFindOpenCards(t *testing.T) {
	f := newFindJira(t, 200, `{"issues":[{"key":"PROJ-1","fields":{"summary":"T-1: a","status":{"name":"To Do"},"created":"2026-11-10T14:03:22.123+0000"}},{"key":"PROJ-2","fields":{"summary":"T-2: b","status":{"name":"In Progress"},"created":"2026-11-11T09:00:00.000+0000"}}]}`)
	p := findProvider(f.URL, "ralph", " ", "automation-platform", "ralph")
	got, err := p.FindOpenCards(context.Background(), taskmanager.CardSearch{CreatedAfter: time.Date(2026, 1, 15, 0, 0, 0, 0, time.UTC)})
	if err != nil {
		t.Fatal(err)
	}
	if len(f.bodies) != 1 {
		t.Fatalf("requests = %d", len(f.bodies))
	}
	wantJQL := `project = "PROJ" AND labels = "ralph" AND labels = "automation-platform" AND statusCategory != Done AND created >= "2026-01-15" ORDER BY created ASC`
	if jql := f.bodies[0]["jql"]; jql != wantJQL {
		t.Errorf("jql = %v", jql)
	}
	want := []taskmanager.CardCandidate{
		{Key: "PROJ-1", URL: f.URL + "/browse/PROJ-1", Summary: "T-1: a", Status: "To Do", Created: time.Date(2026, 11, 10, 14, 3, 22, 123e6, time.UTC)},
		{Key: "PROJ-2", URL: f.URL + "/browse/PROJ-2", Summary: "T-2: b", Status: "In Progress", Created: time.Date(2026, 11, 11, 9, 0, 0, 0, time.UTC)},
	}
	if len(got) != len(want) {
		t.Fatalf("got %+v", got)
	}
	for i, w := range want {
		g := got[i]
		if g.Key != w.Key || g.URL != w.URL || g.Summary != w.Summary || g.Status != w.Status || !g.Created.Equal(w.Created) {
			t.Errorf("candidate %d = %+v, want %+v", i, g, w)
		}
	}
}

func TestFindOpenCardsWithoutLabels(t *testing.T) {
	for name, labels := range map[string][]string{"none": nil, "blanks": {"", "  "}} {
		t.Run(name, func(t *testing.T) {
			f := newFindJira(t, 200, `{"issues":[]}`)
			_, err := findProvider(f.URL, labels...).FindOpenCards(context.Background(), taskmanager.CardSearch{})
			if err == nil || !strings.Contains(err.Error(), "task_manager.jira.labels must name at least one label: without labels the search would match every open card in the project") {
				t.Errorf("err = %v", err)
			}
			if len(f.bodies) != 0 {
				t.Errorf("requests = %d", len(f.bodies))
			}
		})
	}
}

func TestFindOpenCardsAPIError(t *testing.T) {
	f := newFindJira(t, 400, `{"errorMessages":["bad jql"]}`)
	_, err := findProvider(f.URL, "ralph").FindOpenCards(context.Background(), taskmanager.CardSearch{})
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != 400 {
		t.Errorf("err = %v", err)
	}
}

func TestFindOpenCardsMissingCredentials(t *testing.T) {
	f := newFindJira(t, 200, `{"issues":[]}`)
	cfg := testCfg(f.URL)
	cfg.Labels = []string{"ralph"}
	_, err := NewProvider(cfg, 0, env(nil), nil).FindOpenCards(context.Background(), taskmanager.CardSearch{})
	if err == nil || !strings.Contains(err.Error(), "RALPH_JIRA_EMAIL") || len(f.bodies) != 0 {
		t.Errorf("err = %v, requests = %d", err, len(f.bodies))
	}
}

func TestRegisteredProviderIsCardFinder(t *testing.T) {
	cfg, err := config.Defaults()
	if err != nil {
		t.Fatal(err)
	}
	cfg.TaskManager.Provider = config.TaskManagerJira
	p, err := taskmanager.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := p.(taskmanager.CardFinder); !ok {
		t.Errorf("%T does not implement CardFinder", p)
	}
}

func TestSummaryStoreID(t *testing.T) {
	f := newFakeJira(t)
	p := newTestProvider(f.URL)
	for _, tc := range []struct{ storeID, want string }{{"ab", "[ab] T-12: Title"}, {"", "T-12: Title"}} {
		req := taskmanager.CardRequest{Kind: domain.KindTask, ID: "T-12", Title: "Title", StoreID: tc.storeID, ParentKey: "PROJ-1"}
		if _, err := p.Create(context.Background(), req); err != nil {
			t.Fatal(err)
		}
		if got := f.last(t).fields()["summary"]; got != tc.want {
			t.Errorf("create %q: summary %q", tc.storeID, got)
		}
		if err := p.Update(context.Background(), "PROJ-9", req); err != nil {
			t.Fatal(err)
		}
		if got := f.last(t).fields()["summary"]; got != tc.want {
			t.Errorf("update %q: summary %q", tc.storeID, got)
		}
	}
}

func TestSummaryStoreIDTruncation(t *testing.T) {
	s := summary(taskmanager.CardRequest{ID: "T-12", StoreID: "ab", Title: strings.Repeat("x", 400)})
	if utf8.RuneCountInString(s) != 255 || !strings.HasSuffix(s, "…") || !strings.HasPrefix(s, "[ab] T-12: ") {
		t.Errorf("summary %q", s)
	}
}

func storeLabelsOf(t *testing.T, r recorded) []string {
	t.Helper()
	raw, ok := r.fields()["labels"].([]any)
	if !ok {
		return nil
	}
	var got []string
	for _, l := range raw {
		got = append(got, l.(string))
	}
	return got
}

func TestStoreLabelOnCreate(t *testing.T) {
	tests := []struct {
		name    string
		storeID string
		labels  []string
		want    []string
	}{
		{"appended after configured", "ab", []string{"ralph", "team"}, []string{"ralph", "team", "ralph-store-ab"}},
		{"empty store id", "", []string{"ralph", "team"}, []string{"ralph", "team"}},
		{"no configured labels", "ab", nil, []string{"ralph-store-ab"}},
		{"already configured", "ab", []string{"ralph-store-ab", "team"}, []string{"ralph-store-ab", "team"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeJira(t)
			cfg := testCfg(f.URL)
			cfg.Labels = tc.labels
			p := NewProvider(cfg, 0, env(goodEnv), nil)
			req := taskmanager.CardRequest{Kind: domain.KindTask, ID: "T-12", Title: "t", StoreID: tc.storeID, ParentKey: "PROJ-5"}
			if _, err := p.Create(context.Background(), req); err != nil {
				t.Fatal(err)
			}
			if got := storeLabelsOf(t, f.last(t)); !slices.Equal(got, tc.want) {
				t.Errorf("labels %v, want %v", got, tc.want)
			}
		})
	}
}

func TestStoreLabelNotSentOnUpdate(t *testing.T) {
	f := newFakeJira(t)
	cfg := testCfg(f.URL)
	cfg.Labels = []string{"ralph"}
	p := NewProvider(cfg, 0, env(goodEnv), nil)
	req := taskmanager.CardRequest{Kind: domain.KindTask, ID: "T-12", Title: "t", StoreID: "ab", ParentKey: "PROJ-5"}
	if err := p.Update(context.Background(), "PROJ-9", req); err != nil {
		t.Fatal(err)
	}
	r := f.last(t)
	if r.method != http.MethodPut {
		t.Fatalf("method %s", r.method)
	}
	if l, ok := r.fields()["labels"]; ok {
		t.Errorf("update sent labels %v", l)
	}
}

func TestUpdateAddLabel(t *testing.T) {
	for _, tc := range []struct {
		name, storeID string
		want          []any
	}{
		{"with store id", "ab", []any{map[string]any{"add": "ralph-store-ab"}}},
		{"without store id", "", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeJira(t)
			cfg := testCfg(f.URL)
			cfg.Labels = []string{"ralph"}
			p := NewProvider(cfg, 0, env(goodEnv), nil)
			req := taskmanager.CardRequest{Kind: domain.KindTask, ID: "T-12", Title: "t", StoreID: tc.storeID, ParentKey: "PROJ-5"}
			if err := p.Update(context.Background(), "PROJ-9", req); err != nil {
				t.Fatal(err)
			}
			r := f.last(t)
			if _, ok := r.fields()["labels"]; ok {
				t.Errorf("update sent fields.labels")
			}
			update, ok := r.body["update"].(map[string]any)
			if tc.want == nil {
				if ok {
					t.Errorf("unexpected update %v", update)
				}
				return
			}
			if !ok || !reflect.DeepEqual(update["labels"], tc.want) {
				t.Errorf("update = %v, want labels %v", update, tc.want)
			}
		})
	}
}

type sprintJira struct {
	*httptest.Server
	mu         sync.Mutex
	reqs       []string
	cardSprint string
	active     string
	status     int
}

func newSprintJira(t *testing.T, cardSprint, active string) *sprintJira {
	t.Helper()
	f := &sprintJira{cardSprint: cardSprint, active: active}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.reqs = append(f.reqs, r.Method+" "+r.URL.Path)
		status := f.status
		f.mu.Unlock()
		if status != 0 {
			w.WriteHeader(status)
			_, _ = w.Write([]byte(`{"errorMessages":["nope"]}`))
			return
		}
		switch {
		case r.Method == "GET" && strings.HasPrefix(r.URL.Path, "/rest/agile/1.0/issue/"):
			_, _ = w.Write([]byte(`{"fields":{"sprint":` + f.cardSprint + `}}`))
		case r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/sprint"):
			_, _ = w.Write([]byte(`{"isLast":true,"values":` + f.active + `}`))
		default:
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	t.Cleanup(f.Close)
	return f
}

func (f *sprintJira) requests() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.reqs...)
}

func sprintProvider(f *sprintJira, board int, when string) *Provider {
	cfg := testCfg(f.URL)
	cfg.Sprint = config.JiraSprintConfig{Board: board, When: when}
	return NewProvider(cfg, 0, env(goodEnv), nil)
}

var milestoneCreated = taskmanager.SprintRequest{Kind: domain.KindMilestone, Moment: taskmanager.SprintMomentCreated}

func TestAddToSprintAddsToActiveSprint(t *testing.T) {
	f := newSprintJira(t, `null`, `[{"id":7,"name":"S7","state":"active"}]`)
	note, err := sprintProvider(f, 12, "created").AddToSprint(context.Background(), "PROJ-1", milestoneCreated)
	if note != "" || err != nil {
		t.Fatalf("got %q, %v", note, err)
	}
	want := []string{
		"GET /rest/agile/1.0/issue/PROJ-1",
		"GET /rest/agile/1.0/board/12/sprint",
		"POST /rest/agile/1.0/sprint/7/issue",
	}
	if got := f.requests(); !slices.Equal(got, want) {
		t.Fatalf("requests = %v, want %v", got, want)
	}
}

func TestAddToSprintLeavesCardInOpenSprint(t *testing.T) {
	for _, state := range []string{"active", "future"} {
		f := newSprintJira(t, `{"id":3,"name":"S3","state":"`+state+`"}`, `[]`)
		note, err := sprintProvider(f, 12, "created").AddToSprint(context.Background(), "PROJ-1", milestoneCreated)
		if note != "" || err != nil {
			t.Fatalf("%s: got %q, %v", state, note, err)
		}
		if got := f.requests(); len(got) != 1 || got[0] != "GET /rest/agile/1.0/issue/PROJ-1" {
			t.Fatalf("%s: requests = %v", state, got)
		}
	}
}

func TestAddToSprintAddsCardInClosedSprintOnly(t *testing.T) {
	f := newSprintJira(t, `{"id":3,"name":"S3","state":"closed"}`, `[{"id":7,"state":"active"}]`)
	if _, err := sprintProvider(f, 12, "created").AddToSprint(context.Background(), "PROJ-1", milestoneCreated); err != nil {
		t.Fatal(err)
	}
	if got := f.requests(); len(got) != 3 || got[2] != "POST /rest/agile/1.0/sprint/7/issue" {
		t.Fatalf("requests = %v", got)
	}
}

func TestAddToSprintNoActiveSprint(t *testing.T) {
	f := newSprintJira(t, `null`, `[]`)
	_, err := sprintProvider(f, 12, "created").AddToSprint(context.Background(), "PROJ-1", milestoneCreated)
	if err == nil || !strings.Contains(err.Error(), "board 12") || !strings.Contains(err.Error(), "backlog") {
		t.Fatalf("err = %v", err)
	}
	for _, r := range f.requests() {
		if strings.HasPrefix(r, "POST") {
			t.Fatalf("unexpected add: %v", f.requests())
		}
	}
}

func TestAddToSprintChoosesLatestStart(t *testing.T) {
	tests := []struct {
		name, active string
		want         string
	}{
		{"later start", `[{"id":9,"state":"active","startDate":"2026-01-01T00:00:00.000Z"},{"id":4,"state":"active","startDate":"2026-02-01T00:00:00.000Z"}]`, "4"},
		{"equal dates", `[{"id":4,"state":"active","startDate":"2026-01-01T00:00:00.000Z"},{"id":9,"state":"active","startDate":"2026-01-01T00:00:00.000Z"}]`, "9"},
		{"missing dates", `[{"id":9,"state":"active"},{"id":4,"state":"active"}]`, "9"},
		{"one missing", `[{"id":9,"state":"active"},{"id":4,"state":"active","startDate":"2026-02-01T00:00:00.000Z"}]`, "4"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newSprintJira(t, `null`, tt.active)
			if _, err := sprintProvider(f, 12, "created").AddToSprint(context.Background(), "PROJ-1", milestoneCreated); err != nil {
				t.Fatal(err)
			}
			got := f.requests()
			if want := "POST /rest/agile/1.0/sprint/" + tt.want + "/issue"; got[len(got)-1] != want {
				t.Fatalf("requests = %v, want last %s", got, want)
			}
		})
	}
}

func TestAddToSprintSkipsWithoutRequest(t *testing.T) {
	tests := []struct {
		name  string
		board int
		when  string
		req   taskmanager.SprintRequest
	}{
		{"task", 12, "created", taskmanager.SprintRequest{Kind: domain.KindTask, Moment: "created"}},
		{"initiative", 12, "created", taskmanager.SprintRequest{Kind: domain.KindInitiative, Moment: "created"}},
		{"other moment", 12, "created", taskmanager.SprintRequest{Kind: domain.KindMilestone, Moment: "started"}},
		{"board off", 0, "created", milestoneCreated},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newSprintJira(t, `null`, `[{"id":7,"state":"active"}]`)
			note, err := sprintProvider(f, tt.board, tt.when).AddToSprint(context.Background(), "PROJ-1", tt.req)
			if note != "" || err != nil {
				t.Fatalf("got %q, %v", note, err)
			}
			if got := f.requests(); len(got) != 0 {
				t.Fatalf("requests = %v", got)
			}
		})
	}
}

func TestAddToSprintAPIError(t *testing.T) {
	f := newSprintJira(t, `null`, `[]`)
	f.status = http.StatusForbidden
	_, err := sprintProvider(f, 12, "created").AddToSprint(context.Background(), "PROJ-1", milestoneCreated)
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusForbidden {
		t.Fatalf("err = %v", err)
	}
}
