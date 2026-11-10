// Package loop plans and runs the RALPH loop: one fresh harness session per
// task until nothing loopable remains or the iteration budget is spent.
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

// BranchRef is a Ref with the entity's card key, for the branch template.
type BranchRef struct {
	Ref
	CardKey       string
	CardKeyPrefix string
}

// BranchKeys holds the raw card keys the branch template can use; empty means no card.
type BranchKeys struct {
	Initiative, Milestone string
}

// Plan is everything a loop run would do, resolved from config and the store.
type Plan struct {
	Project           string             `json:"project"`
	Initiative        Ref                `json:"initiative"`
	Milestone         Ref                `json:"milestone"`
	Branch            string             `json:"branch"`
	BaseBranch        string             `json:"base_branch"`
	Harness           string             `json:"harness"`
	Model             string             `json:"model,omitempty"`
	Command           []string           `json:"command"`
	Tasks             []service.TaskView `json:"tasks"`
	MaxIterations     int                `json:"max_iterations"`
	StallThreshold    int                `json:"stall_threshold"`
	CommitPerTask     bool               `json:"commit_per_task"`
	CommitTrailer     bool               `json:"commit_trailer"`
	CommitCardTrailer bool               `json:"commit_card_trailer"`
	FirstPrompt       string             `json:"first_prompt,omitempty"`
	Warnings          []string           `json:"warnings,omitempty"`
}

// Planner builds loop plans.
type Planner struct {
	Service *service.Service
	Config  *config.Config
	Root    string
	// Force skips the dependency check; WarnOnly turns its refusal into warnings.
	Force, WarnOnly bool
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
	if err := p.Service.CheckMilestoneWritable(ctx, milestoneID); err != nil {
		return Plan{}, err
	}
	var warnings []string
	if !p.Force {
		unmet, err := UnmetDependencies(ctx, p.Service, p.Config, p.Root, milestoneID)
		if err != nil {
			return Plan{}, err
		}
		if len(unmet) > 0 {
			if !p.WarnOnly {
				return Plan{}, DependencyRefusal(unmet)
			}
			warnings = unmet
		}
	}
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
		Project:           p.Config.ProjectName(p.Root),
		Initiative:        Ref{ID: i.ID, Slug: i.Slug, Title: i.Title},
		Milestone:         Ref{ID: m.ID, Slug: m.Slug, Title: m.Title},
		BaseBranch:        p.Config.PR.BaseBranch,
		MaxIterations:     p.Config.Loop.MaxIterations,
		StallThreshold:    p.Config.Loop.StallThreshold,
		CommitPerTask:     p.Config.Loop.CommitPerTask,
		CommitTrailer:     p.Config.Loop.CommitTrailer,
		CommitCardTrailer: p.Config.Loop.CommitCardTrailer,
		Warnings:          warnings,
	}
	if plan.Branch, err = ResolveBranch(ctx, p.Service, p.Config, i, m); err != nil {
		return Plan{}, err
	}

	h, res, err := harness.ForStep(p.Config, config.StepLoop)
	if err != nil {
		return Plan{}, err
	}
	plan.Harness, plan.Model = res.Harness, res.Model
	plan.Command = h.Command(harness.Request{Step: config.StepLoop, Model: res.Model, WorkDir: p.Root})

	all, err := p.Service.ListTasks(ctx, service.TaskFilter{MilestoneID: milestoneID, IncludeArchived: true})
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
		tmpl, err := promptTemplate(p.Config, p.Root)
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

func promptTemplate(cfg *config.Config, root string) (string, error) {
	path := cfg.Loop.PromptTemplate
	if path == "" {
		return defaultPrompt, nil
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(root, path)
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

// MilestoneBranch returns the milestone's recorded branch, or the one the
// branch template derives when none is recorded.
func MilestoneBranch(tmpl string, initiative, milestone Ref, keys BranchKeys, recorded string) (string, error) {
	if recorded != "" {
		return recorded, nil
	}
	return BranchName(tmpl, initiative, milestone, keys)
}

// BranchName renders the branch template and checks the result is a usable
// git branch name. A template that uses card keys is cleaned up when a key is
// empty, and falls back to empty keys when the real keys give an invalid name.
func BranchName(tmpl string, initiative, milestone Ref, keys BranchKeys) (string, error) {
	t, err := template.New("branch").Option("missingkey=error").Parse(tmpl)
	if err != nil {
		return "", fmt.Errorf("loop.branch_template: %w", err)
	}
	usesKeys := strings.Contains(tmpl, "CardKey")
	name, err := renderBranch(t, initiative, milestone, keys, usesKeys)
	if err != nil {
		return "", err
	}
	verr := validateBranch(name)
	if verr == nil {
		return name, nil
	}
	if usesKeys && keys != (BranchKeys{}) {
		if plain, err := renderBranch(t, initiative, milestone, BranchKeys{}, true); err == nil && validateBranch(plain) == nil {
			return plain, nil
		}
	}
	return "", fmt.Errorf("loop.branch_template produced %q: %w", name, verr)
}

func renderBranch(t *template.Template, initiative, milestone Ref, keys BranchKeys, usesKeys bool) (string, error) {
	var b strings.Builder
	if err := t.Execute(&b, struct{ Initiative, Milestone BranchRef }{branchRef(initiative, keys.Initiative), branchRef(milestone, keys.Milestone)}); err != nil {
		return "", fmt.Errorf("loop.branch_template: %w", err)
	}
	name := strings.TrimSpace(b.String())
	if usesKeys && (keys.Initiative == "" || keys.Milestone == "") {
		name = cleanBranch(name)
	}
	return name, nil
}

// cleanBranch removes the separators an empty card key leaves behind.
func cleanBranch(name string) string {
	var parts []string
	for _, part := range strings.Split(name, "/") {
		part = strings.TrimLeft(part, "-_.")
		for strings.Contains(part, "--") {
			part = strings.ReplaceAll(part, "--", "-")
		}
		if part != "" {
			parts = append(parts, part)
		}
	}
	return strings.Join(parts, "/")
}

func branchRef(r Ref, key string) BranchRef {
	b := BranchRef{Ref: r, CardKey: key}
	if key != "" {
		b.CardKeyPrefix = key + "-"
	}
	return b
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
