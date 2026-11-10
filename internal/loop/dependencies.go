package loop

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/bengearig/ralph/internal/config"
	"github.com/bengearig/ralph/internal/domain"
	"github.com/bengearig/ralph/internal/drift"
	"github.com/bengearig/ralph/internal/errs"
	"github.com/bengearig/ralph/internal/project"
	"github.com/bengearig/ralph/internal/service"
)

// Dependency states reported by DependencyCheck.
const (
	DependencySatisfied = "satisfied"
	DependencyCancelled = "cancelled"
	DependencyNotDone   = "not_done"
	DependencyNotMerged = "not_merged"
	DependencyUnknown   = "unknown"
)

// DependencyFinding is the outcome of checking one milestone dependency.
type DependencyFinding struct {
	ID        string `json:"id"`
	Satisfied bool   `json:"satisfied"`
	State     string `json:"state"`
	Reason    string `json:"reason,omitempty"`
	Fix       string `json:"fix,omitempty"`
}

// Message describes an unsatisfied dependency of the named milestone.
func (f DependencyFinding) Message(dependent string) string {
	return fmt.Sprintf("%s depends on %s, which %s; %s", dependent, f.ID, f.Reason, f.Fix)
}

// Unsatisfied returns the findings that are not satisfied.
func Unsatisfied(fs []DependencyFinding) []DependencyFinding {
	var out []DependencyFinding
	for _, f := range fs {
		if !f.Satisfied {
			out = append(out, f)
		}
	}
	return out
}

// DependencyMessages renders a message per unsatisfied finding.
func DependencyMessages(dependent string, fs []DependencyFinding) []string {
	var out []string
	for _, f := range Unsatisfied(fs) {
		out = append(out, f.Message(dependent))
	}
	return out
}

// DependencyCheck reports whether each of a milestone's dependencies is
// satisfied, in ascending ID order. Git is touched only for DONE dependencies.
func DependencyCheck(ctx context.Context, svc *service.Service, cfg *config.Config, root string, milestoneID int64) ([]DependencyFinding, error) {
	m, err := svc.GetMilestone(ctx, milestoneID)
	if err != nil {
		return nil, err
	}
	type dep struct {
		id  int64
		ref string
	}
	deps := make([]dep, 0, len(m.DependsOn))
	for _, ref := range m.DependsOn {
		id, err := domain.ParseID(domain.KindMilestone, ref)
		if err != nil {
			return nil, err
		}
		deps = append(deps, dep{id, ref})
	}
	slices.SortFunc(deps, func(a, b dep) int { return int(a.id - b.id) })

	findings := []DependencyFinding{}
	for _, d := range deps {
		f, err := CheckDependency(ctx, svc, cfg, root, d.id)
		if err != nil {
			return nil, err
		}
		findings = append(findings, f)
	}
	return findings, nil
}

// CheckDependency reports whether the milestone with the given ID is
// satisfied as a dependency. Git is touched only when it is DONE.
func CheckDependency(ctx context.Context, svc *service.Service, cfg *config.Config, root string, id int64) (DependencyFinding, error) {
	dm, err := svc.GetMilestone(ctx, id)
	if err != nil {
		return DependencyFinding{}, err
	}
	return ClassifyDependency(ctx, dm, cfg.PR.BaseBranch, func(ctx context.Context) (drift.Report, error) {
		in, err := DriftInput(ctx, svc, cfg, root, id)
		if err != nil {
			if errors.Is(err, project.ErrNotGitRepo) {
				return drift.Report{}, errors.New("checking dependencies needs a git repository")
			}
			return drift.Report{}, err
		}
		return drift.Check(ctx, in)
	})
}

// ClassifyDependency turns a dependency milestone into a finding. report runs
// the drift check and is called only when the milestone is DONE.
func ClassifyDependency(ctx context.Context, dm service.MilestoneView, baseBranch string, report func(context.Context) (drift.Report, error)) (DependencyFinding, error) {
	f := DependencyFinding{ID: dm.ID}
	switch {
	case dm.Status == service.MilestoneCancelled:
		f.Satisfied, f.State = true, DependencyCancelled
	case dm.Status != service.MilestoneDone:
		f.State = DependencyNotDone
		f.Reason = fmt.Sprintf("is %s, not DONE", dm.Status)
		f.Fix = "finish it first: ralph loop --milestone " + dm.ID
	default:
		r, err := report(ctx)
		if err != nil {
			return f, err
		}
		switch r.State {
		case drift.StateMerged:
			f.Satisfied, f.State = true, DependencySatisfied
		case drift.StateUnrecorded, drift.StateBranchMissing:
			f.State = DependencyUnknown
			f.Reason = "its merge cannot be confirmed: no git record, or its branch is missing"
			f.Fix = fmt.Sprintf("ralph milestone reconcile %s, or use --force", dm.ID)
		default:
			f.State = DependencyNotMerged
			f.Reason = fmt.Sprintf("branch %s is not merged into %s", r.Branch, baseBranch)
			f.Fix = "ralph milestone merge " + dm.ID
		}
	}
	return f, nil
}

// DependencyRefusal is the ErrNeedsHuman error listing unsatisfied messages.
func DependencyRefusal(messages []string) error {
	return fmt.Errorf("%w: %s", errs.ErrNeedsHuman, strings.Join(messages, "\n"))
}

// UnmetDependencies returns the messages for a milestone's unsatisfied
// dependencies.
func UnmetDependencies(ctx context.Context, svc *service.Service, cfg *config.Config, root string, milestoneID int64) ([]string, error) {
	m, err := svc.GetMilestone(ctx, milestoneID)
	if err != nil {
		return nil, err
	}
	fs, err := DependencyCheck(ctx, svc, cfg, root, milestoneID)
	if err != nil {
		return nil, err
	}
	return DependencyMessages(m.ID, fs), nil
}
