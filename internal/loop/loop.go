// Package loop plans and (eventually) runs the RALPH loop: one fresh harness
// invocation per task until a milestone's tasks are done, a task stalls, or
// the iteration budget is spent.
package loop

import (
	"bytes"
	"context"
	_ "embed"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/template"

	"github.com/bengearig/ralph/internal/config"
	"github.com/bengearig/ralph/internal/domain"
	"github.com/bengearig/ralph/internal/errs"
	"github.com/bengearig/ralph/internal/harness"
	"github.com/bengearig/ralph/internal/service"
)

//go:embed prompt.md
var defaultPrompt string

// ErrNothingToLoop means the milestone has no IN_PROGRESS or TODO tasks.
var ErrNothingToLoop = errors.New("nothing to loop")

// Ref names an entity in templates.
type Ref struct {
	ID    string `json:"id"`
	Slug  string `json:"slug"`
	Title string `json:"title"`
}

// Plan is everything a loop run would do, resolved from config and the store.
type Plan struct {
	Project        string             `json:"project"`
	Initiative     Ref                `json:"initiative"`
	Milestone      Ref                `json:"milestone"`
	Branch         string             `json:"branch"`
	BaseBranch     string             `json:"base_branch"`
	Harness        string             `json:"harness"`
	Model          string             `json:"model,omitempty"`
	Command        []string           `json:"command"`
	Tasks          []service.TaskView `json:"tasks"`
	MaxIterations  int                `json:"max_iterations"`
	StallThreshold int                `json:"stall_threshold"`
	CommitPerTask  bool               `json:"commit_per_task"`
	FirstPrompt    string             `json:"first_prompt,omitempty"`
	Warnings       []string           `json:"warnings,omitempty"`
}

// Planner builds loop plans.
type Planner struct {
	Service *service.Service
	Config  *config.Config
	Root    string
}

// PromptData is the data available to the iteration prompt template.
type PromptData struct {
	Project        string
	Initiative     Ref
	Milestone      Ref
	Branch         string
	Task           service.TaskView
	Iteration      int
	MaxIterations  int
	StallThreshold int
	CommitPerTask  bool
}

// Plan resolves the loop for a milestone without changing anything.
func (p Planner) Plan(ctx context.Context, milestoneID int64) (Plan, error) {
	m, err := p.Service.GetMilestone(ctx, milestoneID)
	if err != nil {
		return Plan{}, err
	}
	iID, err := domain.ParseID(domain.KindInitiative, m.InitiativeID)
	if err != nil {
		return Plan{}, err
	}
	i, err := p.Service.GetInitiative(ctx, iID)
	if err != nil {
		return Plan{}, err
	}
	plan := Plan{
		Project:        p.Config.ProjectName(p.Root),
		Initiative:     Ref{ID: i.ID, Slug: i.Slug, Title: i.Title},
		Milestone:      Ref{ID: m.ID, Slug: m.Slug, Title: m.Title},
		BaseBranch:     p.Config.PR.BaseBranch,
		MaxIterations:  p.Config.Loop.MaxIterations,
		StallThreshold: p.Config.Loop.StallThreshold,
		CommitPerTask:  p.Config.Loop.CommitPerTask,
	}
	if plan.Branch, err = BranchName(p.Config.Loop.BranchTemplate, plan.Initiative, plan.Milestone); err != nil {
		return Plan{}, err
	}

	h, res, err := harness.ForStep(p.Config, config.StepLoop)
	if err != nil {
		return Plan{}, err
	}
	plan.Harness, plan.Model = res.Harness, res.Model
	plan.Command = h.Command(harness.Request{Step: config.StepLoop, Model: res.Model, WorkDir: p.Root})

	all, err := p.Service.ListTasks(ctx, service.TaskFilter{MilestoneID: milestoneID})
	if err != nil {
		return Plan{}, err
	}
	counts := map[string]int{}
	for _, t := range all {
		counts[t.Status]++
		if t.Status == string(domain.StatusInProgress) {
			plan.Tasks = append(plan.Tasks, t)
		}
	}
	for _, t := range all {
		if t.Status == string(domain.StatusTodo) {
			plan.Tasks = append(plan.Tasks, t)
		}
	}
	if n := counts[string(domain.StatusProposed)]; n > 0 {
		plan.Warnings = append(plan.Warnings, fmt.Sprintf("%d PROPOSED task(s) will not be looped until articulated and promoted to TODO", n))
	}
	if n := counts[string(domain.StatusBlocked)]; n > 0 {
		plan.Warnings = append(plan.Warnings, fmt.Sprintf("%d BLOCKED task(s) need a human before the milestone can finish", n))
	}
	if len(plan.Tasks) > plan.MaxIterations {
		plan.Warnings = append(plan.Warnings, fmt.Sprintf("%d loopable tasks exceed loop.max_iterations (%d)", len(plan.Tasks), plan.MaxIterations))
	}

	if len(plan.Tasks) > 0 {
		firstID, err := domain.ParseID(domain.KindTask, plan.Tasks[0].ID)
		if err != nil {
			return Plan{}, err
		}
		first, err := p.Service.GetTask(ctx, firstID)
		if err != nil {
			return Plan{}, err
		}
		tmpl, err := p.promptTemplate()
		if err != nil {
			return Plan{}, err
		}
		plan.FirstPrompt, err = RenderPrompt(tmpl, PromptData{
			Project:        plan.Project,
			Initiative:     plan.Initiative,
			Milestone:      plan.Milestone,
			Branch:         plan.Branch,
			Task:           first,
			Iteration:      1,
			MaxIterations:  plan.MaxIterations,
			StallThreshold: plan.StallThreshold,
			CommitPerTask:  plan.CommitPerTask,
		})
		if err != nil {
			return Plan{}, err
		}
	}
	return plan, nil
}

func (p Planner) promptTemplate() (string, error) {
	path := p.Config.Loop.PromptTemplate
	if path == "" {
		return defaultPrompt, nil
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(p.Root, path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("loop.prompt_template: %w", err)
	}
	return string(data), nil
}

// DefaultPrompt returns the built-in iteration prompt template.
func DefaultPrompt() string { return defaultPrompt }

// RenderPrompt executes an iteration prompt template.
func RenderPrompt(tmpl string, data PromptData) (string, error) {
	t, err := template.New("prompt").Option("missingkey=error").Parse(tmpl)
	if err != nil {
		return "", fmt.Errorf("parsing prompt template: %w", err)
	}
	var b bytes.Buffer
	if err := t.Execute(&b, data); err != nil {
		return "", fmt.Errorf("rendering prompt template: %w", err)
	}
	return b.String(), nil
}

// BranchName renders the branch template and checks the result is a usable
// git branch name.
func BranchName(tmpl string, initiative, milestone Ref) (string, error) {
	t, err := template.New("branch").Option("missingkey=error").Parse(tmpl)
	if err != nil {
		return "", fmt.Errorf("loop.branch_template: %w", err)
	}
	var b strings.Builder
	if err := t.Execute(&b, struct{ Initiative, Milestone Ref }{initiative, milestone}); err != nil {
		return "", fmt.Errorf("loop.branch_template: %w", err)
	}
	name := strings.TrimSpace(b.String())
	if err := validateBranch(name); err != nil {
		return "", fmt.Errorf("loop.branch_template produced %q: %w", name, err)
	}
	return name, nil
}

// validateBranch applies the git check-ref-format rules that templates can
// plausibly violate.
func validateBranch(name string) error {
	switch {
	case name == "":
		return errors.New("branch name is empty")
	case strings.HasPrefix(name, "-"), strings.HasPrefix(name, "/"), strings.HasSuffix(name, "/"):
		return errors.New("branch name must not start with '-' or '/', or end with '/'")
	case strings.HasSuffix(name, ".lock"), strings.HasSuffix(name, "."):
		return errors.New("branch name must not end with '.lock' or '.'")
	case strings.Contains(name, ".."), strings.Contains(name, "//"), strings.Contains(name, "@{"):
		return errors.New("branch name must not contain '..', '//', or '@{'")
	}
	for _, r := range name {
		if r <= ' ' || r == 0x7f || strings.ContainsRune("~^:?*[\\", r) {
			return fmt.Errorf("branch name contains invalid character %q", r)
		}
	}
	for _, part := range strings.Split(name, "/") {
		if strings.HasPrefix(part, ".") {
			return errors.New("branch name components must not start with '.'")
		}
	}
	return nil
}

// Run executes a plan. Execution is not built yet; see docs/loop.md.
func Run(_ context.Context, plan Plan) error {
	if len(plan.Tasks) == 0 {
		return fmt.Errorf("%w: %s has no IN_PROGRESS or TODO tasks", ErrNothingToLoop, plan.Milestone.ID)
	}
	return fmt.Errorf("loop execution: %w (use --dry-run to inspect the plan)", errs.ErrNotImplemented)
}
