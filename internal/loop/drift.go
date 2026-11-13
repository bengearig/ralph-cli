package loop

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
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

// CommitTask is the task as the commit template sees it. CardKey is the key of
// the task's own card, else the milestone's card, empty when neither has one;
// CardKeyPrefix is "[KEY] " or empty.
type CommitTask struct {
	service.TaskView
	CardKey       string
	CardKeyPrefix string
}

func commitTask(t service.TaskView, milestoneKey string) CommitTask {
	ct := CommitTask{TaskView: t}
	key := milestoneKey
	if t.Card != nil && t.Card.Key != "" {
		key = t.Card.Key
	}
	if key != "" {
		ct.CardKey = key
		ct.CardKeyPrefix = "[" + key + "] "
	}
	return ct
}

type commitTemplate struct {
	*template.Template
	usesKey bool
}

func parseCommitTemplate(tmpl string) (*commitTemplate, error) {
	t, err := template.New("commit").Option("missingkey=error").Parse(tmpl)
	if err != nil {
		return nil, fmt.Errorf("loop.commit_template: %w", err)
	}
	return &commitTemplate{Template: t, usesKey: strings.Contains(tmpl, "CardKey")}, nil
}

var emptyBrackets = regexp.MustCompile(`\[\s*\]|\(\s*\)`)
var blankRun = regexp.MustCompile(`[ \t]+`)

// cleanSubject tidies the first line of a message rendered without a card key.
func cleanSubject(msg string) string {
	first, rest, multi := strings.Cut(msg, "\n")
	first = emptyBrackets.ReplaceAllString(first, "")
	first = blankRun.ReplaceAllString(first, " ")
	first = strings.TrimLeft(first, " :-,;")
	first = strings.TrimRight(first, " ")
	if !multi {
		return first
	}
	return first + "\n" + rest
}

// commitMessage renders the message the loop commits a task with.
func commitMessage(t *commitTemplate, data commitData) (string, error) {
	var b strings.Builder
	if err := t.Execute(&b, data); err != nil {
		return "", fmt.Errorf("loop.commit_template: %w", err)
	}
	msg := strings.TrimSpace(b.String())
	if t.usesKey && data.Task.CardKey == "" {
		msg = strings.TrimSpace(cleanSubject(msg))
	}
	if msg == "" {
		msg = data.Task.ID + ": " + data.Task.Title
	}
	return msg, nil
}

// commitSubject renders the subject git reports for a task's commit: the first
// paragraph of the rendered template, its lines joined by spaces. It returns
// "" for a task the template fails on.
func commitSubject(t *commitTemplate, task service.TaskView, milestoneKey string, initiative, milestone Ref) string {
	msg, err := commitMessage(t, commitData{Task: commitTask(task, milestoneKey), Initiative: initiative, Milestone: milestone})
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
}

// CommitSubjects returns a function giving the subjects the loop's commit for a
// task of the milestone may carry, without duplicates and skipping any the
// template fails on. milestoneKey is the milestone's recorded card key, empty
// without one. The first is the subject a commit made now would get: the
// task's own card key, else the milestone's. When the template uses the card
// key, the subject with the milestone's key, the one with the task's own key,
// and the keyless one follow, so a commit made before the task or milestone had
// a card, or before the template used the key, still matches. A commit
// carrying a key that has since changed or disappeared cannot be reconstructed
// from ralph's data; the Ralph-Task trailer remains the durable matcher for it.
func CommitSubjects(tmpl string, milestoneKey string, initiative, milestone Ref) (func(service.TaskView) []string, error) {
	t, err := parseCommitTemplate(tmpl)
	if err != nil {
		return nil, err
	}
	return func(task service.TaskView) []string {
		var out []string
		add := func(s string) {
			if s != "" && !slices.Contains(out, s) {
				out = append(out, s)
			}
		}
		add(commitSubject(t, task, milestoneKey, initiative, milestone))
		if t.usesKey {
			keyless := task
			keyless.Card = nil
			if milestoneKey != "" {
				add(commitSubject(t, keyless, milestoneKey, initiative, milestone))
			}
			if task.Card != nil && task.Card.Key != "" {
				add(commitSubject(t, task, "", initiative, milestone))
			}
			add(commitSubject(t, keyless, "", initiative, milestone))
		}
		return out
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
	branch, err := ResolveBranch(ctx, svc, cfg, i, m)
	if err != nil {
		return drift.Input{}, err
	}
	keys := CardKeys(ctx, svc, cfg, iID, milestoneID)
	subjects, err := CommitSubjects(cfg.Loop.CommitTemplate, keys.Milestone, initiative, milestone)
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
		BaseRemote:     cfg.PR.Remote,
		TemplateBranch: branch,
		Subjects:       subjects,
	}, nil
}
