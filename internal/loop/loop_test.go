package loop

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/bengearig/ralph/internal/config"
	"github.com/bengearig/ralph/internal/domain"
	"github.com/bengearig/ralph/internal/search"
	"github.com/bengearig/ralph/internal/service"
	"github.com/bengearig/ralph/internal/store/sqlite"

	_ "github.com/bengearig/ralph/internal/harness/claudecode"
)

func setup(t *testing.T) (Planner, int64) {
	t.Helper()
	ctx := context.Background()
	root := t.TempDir()
	st, err := sqlite.Open(ctx, filepath.Join(root, "r.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	svc := service.New(st, &search.Index{Store: st, Chunker: search.Chunker{MaxChars: 500}}, service.Options{StallThreshold: 3})
	cfg, err := config.Defaults()
	if err != nil {
		t.Fatal(err)
	}
	cfg.Steps.Loop.Model = "opus"

	i, _ := svc.CreateInitiative(ctx, service.InitiativeInput{Title: "SSO"})
	iID, _ := domain.ParseID(domain.KindInitiative, i.ID)
	m, _ := svc.CreateMilestone(ctx, service.MilestoneInput{InitiativeID: iID, Title: "OIDC login"})
	mID, _ := domain.ParseID(domain.KindMilestone, m.ID)

	mk := func(title string, to ...domain.Status) {
		tv, err := svc.CreateTask(ctx, service.TaskInput{MilestoneID: mID, Title: title, Description: "do " + title, AcceptanceCriteria: title + " works"})
		if err != nil {
			t.Fatal(err)
		}
		id, _ := domain.ParseID(domain.KindTask, tv.ID)
		for _, s := range to {
			if _, err := svc.SetTaskStatus(ctx, id, s, "reason"); err != nil {
				t.Fatal(err)
			}
		}
	}
	mk("first", domain.StatusTodo)
	mk("resume me", domain.StatusTodo, domain.StatusInProgress)
	mk("draft")
	mk("stuck", domain.StatusTodo, domain.StatusInProgress, domain.StatusBlocked)
	mk("finished", domain.StatusTodo, domain.StatusInProgress, domain.StatusDone)
	return Planner{Service: svc, Config: cfg, Root: root}, mID
}

func TestPlan(t *testing.T) {
	p, mID := setup(t)
	plan, err := p.Plan(context.Background(), mID)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Branch != "ralph/oidc-login" || plan.BaseBranch != "main" {
		t.Errorf("branch = %q from %q", plan.Branch, plan.BaseBranch)
	}
	if plan.Harness != config.HarnessClaudeCode || plan.Model != "opus" {
		t.Errorf("harness = %s %s", plan.Harness, plan.Model)
	}
	if !strings.Contains(strings.Join(plan.Command, " "), "--model opus") {
		t.Errorf("command = %v", plan.Command)
	}
	var titles []string
	for _, task := range plan.Tasks {
		titles = append(titles, task.Title)
	}
	if strings.Join(titles, ",") != "resume me,first" {
		t.Errorf("loop order = %v; interrupted work must come first", titles)
	}
	if len(plan.Warnings) != 2 {
		t.Errorf("warnings = %v", plan.Warnings)
	}
	for _, want := range []string{"Your task: T-2 — resume me", "do resume me", "resume me works", "Do not commit"} {
		if !strings.Contains(plan.FirstPrompt, want) {
			t.Errorf("prompt lacks %q:\n%s", want, plan.FirstPrompt)
		}
	}
}

func TestPlanCustomPromptTemplate(t *testing.T) {
	p, mID := setup(t)
	path := filepath.Join(p.Root, "prompt.tmpl")
	os.WriteFile(path, []byte("Do {{.Task.ID}} on {{.Branch}}"), 0o644)
	p.Config.Loop.PromptTemplate = "prompt.tmpl"
	plan, err := p.Plan(context.Background(), mID)
	if err != nil {
		t.Fatal(err)
	}
	if plan.FirstPrompt != "Do T-2 on ralph/oidc-login" {
		t.Errorf("prompt = %q", plan.FirstPrompt)
	}
	os.WriteFile(path, []byte("{{.Nope}}"), 0o644)
	if _, err := p.Plan(context.Background(), mID); err == nil {
		t.Error("unknown template field should fail")
	}
	p.Config.Loop.PromptTemplate = "missing.tmpl"
	if _, err := p.Plan(context.Background(), mID); err == nil {
		t.Error("missing template file should fail")
	}
}

func TestNothingToLoop(t *testing.T) {
	p, _ := setup(t)
	ctx := context.Background()
	m, _ := p.Service.CreateMilestone(ctx, service.MilestoneInput{InitiativeID: 1, Title: "Empty"})
	mID, _ := domain.ParseID(domain.KindMilestone, m.ID)
	plan, err := p.Plan(ctx, mID)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Tasks) != 0 || plan.FirstPrompt != "" {
		t.Errorf("plan = %+v", plan)
	}
	if _, err := (Runner{Service: p.Service, Config: p.Config, Root: p.Root}).Run(ctx, plan); !errors.Is(err, ErrNothingToLoop) {
		t.Errorf("Run = %v", err)
	}
}

func TestBranchName(t *testing.T) {
	i, m := Ref{ID: "I-1", Slug: "sso"}, Ref{ID: "M-2", Slug: "oidc-login", Title: "OIDC Login"}
	good := map[string]string{
		"ralph/{{.Milestone.Slug}}":                      "ralph/oidc-login",
		"feature/{{.Initiative.Slug}}/{{.Milestone.ID}}": "feature/sso/M-2",
	}
	for tmpl, want := range good {
		if got, err := BranchName(tmpl, i, m, BranchKeys{}); err != nil || got != want {
			t.Errorf("BranchName(%q) = %q, %v", tmpl, got, err)
		}
	}
	for _, tmpl := range []string{
		"ralph/{{.Milestone.Title}}",
		"{{.Milestone.Nope}}",
		"ralph/..{{.Milestone.Slug}}",
		"ralph/{{.Milestone.Slug}}.lock",
		"-x",
		"ralph//x",
		"a/.hidden",
		"x~1",
		"{{",
		"  ",
	} {
		if got, err := BranchName(tmpl, i, m, BranchKeys{}); err == nil {
			t.Errorf("BranchName(%q) = %q, want error", tmpl, got)
		}
	}
}

func TestDefaultPromptRendersWithoutNotes(t *testing.T) {
	out, err := RenderPrompt(DefaultPrompt(), PromptData{
		Project: "p", Task: service.TaskView{ID: "T-1", Title: "x"}, Iteration: 1, MaxIterations: 5, StallThreshold: 3,
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "Notes from earlier work") || strings.Contains(out, "Do not commit") {
		t.Errorf("optional sections rendered:\n%s", out)
	}
}

func TestCommitSubjects(t *testing.T) {
	i, m := Ref{ID: "I-1", Slug: "sso", Title: "SSO"}, Ref{ID: "M-2", Slug: "login", Title: "Login"}
	task := service.TaskView{ID: "T-3", Title: "Add route"}
	for _, tc := range []struct{ tmpl, want string }{
		{"{{.Task.ID}}: {{.Task.Title}}", "T-3: Add route"},
		{"{{.Milestone.ID}} {{.Task.ID}}: {{.Task.Title}}\n\nBody for {{.Initiative.Slug}}", "M-2 T-3: Add route"},
		{"  {{.Task.ID}}  \n{{.Task.Title}}\n\nbody", "T-3 Add route"},
		{"{{if false}}x{{end}}", "T-3: Add route"},
	} {
		subjects, err := CommitSubjects(tc.tmpl, "", i, m)
		if err != nil {
			t.Fatalf("%q: %v", tc.tmpl, err)
		}
		if got := subjects(task); !slices.Equal(got, []string{tc.want}) {
			t.Errorf("%q: subjects = %q, want %q", tc.tmpl, got, tc.want)
		}
	}
	if _, err := CommitSubjects("{{.Task", "", i, m); err == nil || !strings.Contains(err.Error(), "loop.commit_template") {
		t.Errorf("bad template: err = %v", err)
	}
	subjects, err := CommitSubjects("{{.Task.Nope}}", "", i, m)
	if err != nil {
		t.Fatal(err)
	}
	if got := subjects(task); len(got) != 0 {
		t.Errorf("failing template: subjects = %q, want none", got)
	}
}

func TestCommitSubjectsCardKey(t *testing.T) {
	i, m := Ref{ID: "I-1", Slug: "sso", Title: "SSO"}, Ref{ID: "M-2", Slug: "login", Title: "Login"}
	base := service.TaskView{ID: "T-12", Title: "Add X"}
	keyed := base
	keyed.Card = &service.CardView{Key: "ABC-1235"}
	emptyKey := base
	emptyKey.Card = &service.CardView{}
	for _, tc := range []struct {
		name, tmpl string
		task       service.TaskView
		want       []string
	}{
		{"prefix keyed", "{{.Task.CardKeyPrefix}}{{.Task.ID}}: {{.Task.Title}}", keyed, []string{"[ABC-1235] T-12: Add X", "T-12: Add X"}},
		{"key keyed", "{{.Task.CardKey}} {{.Task.ID}}", keyed, []string{"ABC-1235 T-12", "T-12"}},
		{"prefix no card", "{{.Task.CardKeyPrefix}}{{.Task.ID}}: {{.Task.Title}}", base, []string{"T-12: Add X"}},
		{"prefix empty key", "{{.Task.CardKeyPrefix}}{{.Task.ID}}: {{.Task.Title}}", emptyKey, []string{"T-12: Add X"}},
		{"no key fields keyed", "{{.Task.ID}}: {{.Task.Title}}", keyed, []string{"T-12: Add X"}},
		{"no key fields no card", "{{.Task.ID}}: {{.Task.Title}}", base, []string{"T-12: Add X"}},
		{"key not in subject", "{{.Task.ID}}: {{.Task.Title}}\n\nCard {{.Task.CardKey}}", keyed, []string{"T-12: Add X"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			subjects, err := CommitSubjects(tc.tmpl, "", i, m)
			if err != nil {
				t.Fatal(err)
			}
			if got := subjects(tc.task); !slices.Equal(got, tc.want) {
				t.Errorf("subjects = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestPlanRefusesArchivedInitiative(t *testing.T) {
	ctx := context.Background()
	p, mID := setup(t)
	if _, err := p.Service.ArchiveInitiative(ctx, 1, service.DocumentChoice{}); err != nil {
		t.Fatal(err)
	}
	_, err := p.Plan(ctx, mID)
	if !errors.Is(err, service.ErrArchived) || !strings.Contains(err.Error(), "ralph initiative unarchive I-1") {
		t.Fatalf("Plan on archived initiative: err = %v", err)
	}
	if _, err := p.Service.UnarchiveInitiative(ctx, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Plan(ctx, mID); err != nil {
		t.Fatalf("Plan after unarchive: %v", err)
	}
}

func TestBranchNameCardKeys(t *testing.T) {
	i, m := Ref{ID: "I-1", Slug: "sso"}, Ref{ID: "M-2", Slug: "oidc-login", Title: "OIDC Login"}
	keys := BranchKeys{Initiative: "ABC-1", Milestone: "ABC-1234"}
	for tmpl, want := range map[string]string{
		"{{.Milestone.CardKeyPrefix}}{{.Milestone.Slug}}":                    "ABC-1234-oidc-login",
		"{{.Milestone.CardKey}}/{{.Milestone.Slug}}":                         "ABC-1234/oidc-login",
		"{{.Initiative.CardKey}}":                                            "ABC-1",
		"{{.Initiative.CardKeyPrefix}}{{.Milestone.ID}}-{{.Milestone.Slug}}": "ABC-1-M-2-oidc-login",
		"x/{{.Milestone.Slug}}-{{.Milestone.ID}}":                            "x/oidc-login-M-2",
	} {
		if got, err := BranchName(tmpl, i, m, keys); err != nil || got != want {
			t.Errorf("BranchName(%q) = %q, %v; want %q", tmpl, got, err, want)
		}
	}
	if got, err := BranchName("{{.Milestone.CardKey}}/{{.Milestone.Title}}", i, Ref{ID: "M-2", Slug: "s", Title: "Login"}, keys); err != nil || got != "ABC-1234/Login" {
		t.Errorf("title = %q, %v", got, err)
	}

	empty := BranchKeys{}
	if got, err := BranchName("{{.Milestone.CardKeyPrefix}}x", i, m, empty); err != nil || got != "x" {
		t.Errorf("empty prefix = %q, %v", got, err)
	}
	if got, err := BranchName("{{.Milestone.CardKey}}", i, m, empty); err == nil {
		t.Errorf("empty key = %q, want validation error", got)
	}
	if got, err := BranchName("ralph/{{.Milestone.Slug}}", i, m, empty); err != nil || got != "ralph/oidc-login" {
		t.Errorf("default = %q, %v", got, err)
	}
}

func TestMilestoneBranchRecordedWins(t *testing.T) {
	i, m := Ref{ID: "I-1", Slug: "sso"}, Ref{ID: "M-2", Slug: "oidc-login"}
	got, err := MilestoneBranch("{{.Milestone.CardKeyPrefix}}{{.Milestone.Slug}}", i, m, BranchKeys{Milestone: "ABC-1"}, "kept/branch")
	if err != nil || got != "kept/branch" {
		t.Errorf("recorded = %q, %v", got, err)
	}
	got, err = MilestoneBranch("{{.Milestone.CardKeyPrefix}}{{.Milestone.Slug}}", i, m, BranchKeys{Milestone: "ABC-1"}, "")
	if err != nil || got != "ABC-1-oidc-login" {
		t.Errorf("derived = %q, %v", got, err)
	}
}

func TestCommitTemplateRejectsCardKey(t *testing.T) {
	tmpl, err := parseCommitTemplate("{{.Milestone.CardKey}}")
	if err != nil {
		t.Fatal(err)
	}
	_, err = commitMessage(tmpl, commitData{Milestone: Ref{ID: "M-1"}})
	if err == nil || !strings.Contains(err.Error(), "CardKey") {
		t.Errorf("err = %v, want unknown field", err)
	}
}

func TestBranchNameEmptyKeyCleanup(t *testing.T) {
	i := Ref{ID: "I-1", Slug: "sso"}
	m := Ref{ID: "M-2", Slug: "implement-cool-feature"}
	none := BranchKeys{}
	both := BranchKeys{Initiative: "ABC-1", Milestone: "ABC-1234"}
	cases := []struct {
		name, tmpl string
		keys       BranchKeys
		want       string
		wantErr    string
	}{
		{"dash empty", "{{.Milestone.CardKey}}-{{.Milestone.Slug}}", none, "implement-cool-feature", ""},
		{"dash key", "{{.Milestone.CardKey}}-{{.Milestone.Slug}}", both, "ABC-1234-implement-cool-feature", ""},
		{"prefix empty", "{{.Milestone.CardKeyPrefix}}{{.Milestone.Slug}}", none, "implement-cool-feature", ""},
		{"prefix key", "{{.Milestone.CardKeyPrefix}}{{.Milestone.Slug}}", both, "ABC-1234-implement-cool-feature", ""},
		{"path empty", "feature/{{.Milestone.CardKey}}-x", none, "feature/x", ""},
		{"path key", "feature/{{.Milestone.CardKey}}-x", BranchKeys{Milestone: "ABC-1"}, "feature/ABC-1-x", ""},
		{"both prefixes empty", "{{.Initiative.CardKeyPrefix}}{{.Milestone.CardKeyPrefix}}{{.Milestone.Slug}}", none, "implement-cool-feature", ""},
		{"nested both empty", "{{.Initiative.CardKey}}/{{.Milestone.CardKey}}-{{.Milestone.Slug}}", none, "implement-cool-feature", ""},
		{"nested initiative empty", "{{.Initiative.CardKey}}/{{.Milestone.CardKey}}-{{.Milestone.Slug}}", BranchKeys{Milestone: "ABC-1234"}, "ABC-1234-implement-cool-feature", ""},
		{"underscore", "{{.Milestone.CardKey}}_{{.Milestone.Slug}}", none, "implement-cool-feature", ""},
		{"dot", "{{.Milestone.CardKey}}.{{.Milestone.Slug}}", none, "implement-cool-feature", ""},
		{"no key fields untouched", "a--b", none, "a--b", ""},
		{"no key fields untouched x--y", "x--{{.Milestone.ID}}", none, "x--M-2", ""},
		{"key present slug dashes kept", "{{.Milestone.CardKey}}-{{.Milestone.Slug}}", BranchKeys{Initiative: "ABC-1", Milestone: "ABC-1"}, "ABC-1-implement-cool-feature", ""},
		{"nothing left", "{{.Milestone.CardKey}}", none, "", "branch name is empty"},
		{"space still fails", "{{.Milestone.CardKey}} x y", none, "", "invalid character"},
		{"invalid key space", "{{.Milestone.CardKeyPrefix}}{{.Milestone.Slug}}", BranchKeys{Milestone: "AB C-1"}, "implement-cool-feature", ""},
		{"invalid key colon", "{{.Milestone.CardKeyPrefix}}{{.Milestone.Slug}}", BranchKeys{Milestone: "AB:1"}, "implement-cool-feature", ""},
		{"invalid template regardless", "{{.Milestone.CardKey}}{{.Milestone.Slug}}..x", both, "", "must not contain"},
		{"invalid template empty keys", "{{.Milestone.CardKey}}{{.Milestone.Slug}}..x", none, "", "must not contain"},
	}
	for _, c := range cases {
		got, err := BranchName(c.tmpl, i, m, c.keys)
		if c.wantErr != "" {
			if err == nil || !strings.Contains(err.Error(), c.wantErr) {
				t.Errorf("%s: BranchName = %q, %v; want error containing %q", c.name, got, err, c.wantErr)
			}
			continue
		}
		if err != nil || got != c.want {
			t.Errorf("%s: BranchName = %q, %v; want %q", c.name, got, err, c.want)
		}
	}
}
