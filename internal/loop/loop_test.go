package loop

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bengearig/ralph/internal/config"
	"github.com/bengearig/ralph/internal/domain"
	"github.com/bengearig/ralph/internal/errs"
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
	if err := Run(context.Background(), plan); !errors.Is(err, errs.ErrNotImplemented) {
		t.Errorf("Run = %v", err)
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
	if err := Run(ctx, plan); !errors.Is(err, ErrNothingToLoop) {
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
		if got, err := BranchName(tmpl, i, m); err != nil || got != want {
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
		if got, err := BranchName(tmpl, i, m); err == nil {
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
