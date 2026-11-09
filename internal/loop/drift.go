package loop

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"text/template"

	"github.com/bengearig/ralph/internal/config"
	"github.com/bengearig/ralph/internal/domain"
	"github.com/bengearig/ralph/internal/drift"
	"github.com/bengearig/ralph/internal/git"
	"github.com/bengearig/ralph/internal/project"
	"github.com/bengearig/ralph/internal/service"
)

// ResolveRepo returns the git repository containing the project at root,
// with the project's .ralph directory excluded from working-tree operations.
func ResolveRepo(ctx context.Context, root string) (git.Repo, error) {
	if _, err := exec.LookPath("git"); err != nil {
		return git.Repo{}, errors.New("git was not found on PATH")
	}
	gitRoot, err := project.GitRoot(ctx, root)
	if err != nil {
		return git.Repo{}, err
	}
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		root = resolved
	}
	if resolved, err := filepath.EvalSymlinks(gitRoot); err == nil {
		gitRoot = resolved
	}
	exclude, err := filepath.Rel(gitRoot, filepath.Join(root, project.DirName))
	if err != nil {
		return git.Repo{}, err
	}
	return git.Repo{Dir: gitRoot, Exclude: filepath.ToSlash(exclude)}, nil
}

func parseCommitTemplate(tmpl string) (*template.Template, error) {
	t, err := template.New("commit").Option("missingkey=error").Parse(tmpl)
	if err != nil {
		return nil, fmt.Errorf("loop.commit_template: %w", err)
	}
	return t, nil
}

// commitMessage renders the message the loop commits a task with.
func commitMessage(t *template.Template, data commitData) (string, error) {
	var b strings.Builder
	if err := t.Execute(&b, data); err != nil {
		return "", fmt.Errorf("loop.commit_template: %w", err)
	}
	msg := strings.TrimSpace(b.String())
	if msg == "" {
		msg = data.Task.ID + ": " + data.Task.Title
	}
	return msg, nil
}

// CommitSubject returns a function giving the subject git reports for the
// commit the loop makes for a task of the milestone: the first paragraph of
// the rendered loop.commit_template, its lines joined by spaces. The function
// returns "" for a task the template fails on.
func CommitSubject(tmpl string, initiative, milestone Ref) (func(service.TaskView) string, error) {
	t, err := parseCommitTemplate(tmpl)
	if err != nil {
		return nil, err
	}
	return func(task service.TaskView) string {
		msg, err := commitMessage(t, commitData{Task: task, Initiative: initiative, Milestone: milestone})
		if err != nil {
			return ""
		}
		var lines []string
		for _, l := range strings.Split(msg, "\n") {
			l = strings.TrimRight(l, " \t\r")
			if l == "" {
				break
			}
			lines = append(lines, l)
		}
		return strings.Join(lines, " ")
	}, nil
}

// DriftInput builds the drift check input for a milestone as the loop sees
// it: the project's git repository, pr.base_branch, the milestone's branch
// and the loop's commit subjects.
func DriftInput(ctx context.Context, svc *service.Service, cfg *config.Config, root string, milestoneID int64) (drift.Input, error) {
	m, err := svc.GetMilestone(ctx, milestoneID)
	if err != nil {
		return drift.Input{}, err
	}
	iID, err := domain.ParseID(domain.KindInitiative, m.InitiativeID)
	if err != nil {
		return drift.Input{}, err
	}
	i, err := svc.GetInitiative(ctx, iID)
	if err != nil {
		return drift.Input{}, err
	}
	initiative := Ref{ID: i.ID, Slug: i.Slug, Title: i.Title}
	milestone := Ref{ID: m.ID, Slug: m.Slug, Title: m.Title}
	branch, err := MilestoneBranch(cfg.Loop.BranchTemplate, initiative, milestone, m.Branch)
	if err != nil {
		return drift.Input{}, err
	}
	subject, err := CommitSubject(cfg.Loop.CommitTemplate, initiative, milestone)
	if err != nil {
		return drift.Input{}, err
	}
	repo, err := ResolveRepo(ctx, root)
	if err != nil {
		return drift.Input{}, fmt.Errorf("checking %s's branch needs a git repository: %w", m.ID, err)
	}
	return drift.Input{
		Service:        svc,
		Repo:           repo,
		MilestoneID:    milestoneID,
		BaseBranch:     cfg.PR.BaseBranch,
		TemplateBranch: branch,
		Subject:        subject,
	}, nil
}
