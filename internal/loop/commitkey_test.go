package loop

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bengearig/ralph/internal/config"
	"github.com/bengearig/ralph/internal/domain"
	"github.com/bengearig/ralph/internal/harness"
	"github.com/bengearig/ralph/internal/project"
	"github.com/bengearig/ralph/internal/search"
	"github.com/bengearig/ralph/internal/service"
	"github.com/bengearig/ralph/internal/store/sqlite"
	"github.com/bengearig/ralph/internal/taskmanager/fake"
)

func renderCommit(t *testing.T, tmpl string, task service.TaskView) string {
	t.Helper()
	i, m := Ref{ID: "I-1", Slug: "sso", Title: "SSO"}, Ref{ID: "M-2", Slug: "login", Title: "Login"}
	subject, err := CommitSubject(tmpl, i, m)
	if err != nil {
		t.Fatal(err)
	}
	ct, err := parseCommitTemplate(tmpl)
	if err != nil {
		t.Fatal(err)
	}
	msg, err := commitMessage(ct, commitData{Task: commitTask(task), Initiative: i, Milestone: m})
	if err != nil {
		t.Fatalf("%q: %v", tmpl, err)
	}
	if first, _, _ := strings.Cut(msg, "\n"); !strings.Contains(tmpl, "\n") && subject(task) != first {
		t.Errorf("%q: subject %q differs from message %q", tmpl, subject(task), first)
	}
	return msg
}

func TestCommitTaskCardKey(t *testing.T) {
	base := service.TaskView{ID: "T-12", Title: "Add X", Description: "desc"}
	keyed := base
	keyed.Card = &service.CardView{Key: "ABC-1235"}
	emptyKey := base
	emptyKey.Card = &service.CardView{}

	for _, tc := range []struct {
		name, tmpl string
		task       service.TaskView
		want       string
	}{
		{"prefix", "{{.Task.CardKeyPrefix}}{{.Task.ID}}: {{.Task.Title}}", keyed, "[ABC-1235] T-12: Add X"},
		{"key", "{{.Task.CardKey}} {{.Task.ID}}", keyed, "ABC-1235 T-12"},
		{"existing fields", "{{.Task.CardKeyPrefix}}{{.Task.ID}} {{.Task.Title}} {{.Task.Description}}", keyed, "[ABC-1235] T-12 Add X desc"},
		{"prefix no card", "{{.Task.CardKeyPrefix}}{{.Task.ID}}: {{.Task.Title}}", base, "T-12: Add X"},
		{"prefix empty key", "{{.Task.CardKeyPrefix}}{{.Task.ID}}: {{.Task.Title}}", emptyKey, "T-12: Add X"},
		{"brackets", "[{{.Task.CardKey}}] {{.Task.ID}}: {{.Task.Title}}", base, "T-12: Add X"},
		{"brackets empty key", "[{{.Task.CardKey}}] {{.Task.ID}}: {{.Task.Title}}", emptyKey, "T-12: Add X"},
		{"leading separator", "{{.Task.CardKey}}: {{.Task.ID}}", base, "T-12"},
		{"parens and spaces", "{{.Task.ID}}  ({{.Task.CardKey}})\t{{.Task.Title}}", base, "T-12 Add X"},
		{"trailing space", "{{.Task.ID}} {{.Task.CardKey}}", base, "T-12"},
		{"body untouched", "[{{.Task.CardKey}}]  {{.Task.ID}}\n\nbody  [] ( )  x", base, "T-12\n\nbody  [] ( )  x"},
		{"no key fields, no clean", "a  b []", base, "a  b []"},
		{"keyed, no clean", "[{{.Task.CardKey}}]  x", keyed, "[ABC-1235]  x"},
		{"empty falls back", "{{.Task.CardKey}}", base, "T-12: Add X"},
	} {
		if got := renderCommit(t, tc.tmpl, tc.task); got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestCommitTemplateRejectsMilestoneAndInitiativeKeys(t *testing.T) {
	for _, f := range []string{"{{.Milestone.CardKey}}", "{{.Initiative.CardKey}}", "{{.Milestone.CardKeyPrefix}}"} {
		tmpl, err := parseCommitTemplate(f)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := commitMessage(tmpl, commitData{Task: commitTask(service.TaskView{ID: "T-1"})}); err == nil {
			t.Errorf("%s rendered, want an error", f)
		}
	}
}

func TestCommitDefaultTemplateUnchanged(t *testing.T) {
	cfg, err := config.Defaults()
	if err != nil {
		t.Fatal(err)
	}
	for _, task := range []service.TaskView{
		{ID: "T-1", Title: "Do  it"},
		{ID: "T-1", Title: "Do  it", Card: &service.CardView{Key: "ABC-1"}},
	} {
		if got := renderCommit(t, cfg.Loop.CommitTemplate, task); got != "T-1: Do  it" {
			t.Errorf("got %q", got)
		}
	}
}

func TestRunCommitSubjectCardKey(t *testing.T) {
	for _, withCard := range []bool{true, false} {
		e := newRunEnv(t, "a")
		ctx := context.Background()
		st, err := sqlite.Open(ctx, filepath.Join(e.root, project.DirName, "ralph.db"))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { st.Close() })
		prov := fake.New()
		e.svc = service.New(st, &search.Index{Store: st, Chunker: search.Chunker{MaxChars: 500}}, service.Options{StallThreshold: 2, TaskManager: prov})
		if withCard {
			if err := e.svc.RecordCard(ctx, domain.KindTask, 1, "fake", "ABC-1235", "", ""); err != nil {
				t.Fatal(err)
			}
		}
		e.cfg.Loop.CommitTemplate = "[{{.Task.CardKey}}] {{.Task.ID}}: {{.Task.Title}}"
		h := &fakeHarness{script: func(_ int, id int64, _ harness.Request) (harness.Result, error) { return e.complete(id) }}
		if _, err := e.run(t, ctx, h); err != nil {
			t.Fatal(err)
		}
		want := "T-1: a"
		if withCard {
			want = "[ABC-1235] T-1: a"
		}
		if got := gitCmd(t, e.root, "log", "-1", "--format=%s"); got != want {
			t.Errorf("card=%v: subject = %q, want %q", withCard, got, want)
		}
		if body := gitCmd(t, e.root, "log", "-1", "--format=%B"); !strings.Contains(body, "Ralph-Task: T-1") {
			t.Errorf("card=%v: trailer missing in %q", withCard, body)
		}
	}
}
