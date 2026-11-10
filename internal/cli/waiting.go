package cli

import (
	"context"
	"fmt"
	"strings"

	"github.com/bengearig/ralph/internal/config"
	"github.com/bengearig/ralph/internal/domain"
	"github.com/bengearig/ralph/internal/loop"
	"github.com/bengearig/ralph/internal/service"
)

// repoKey holds the git-repository check in a findings cache: present with a
// nil value means the project is not a repository.
const repoKey = ""

// waitingOn returns the unsatisfied dependency findings of m, or nil. It is
// informational: anything that prevents the check yields nil.
func (a *app) waitingOn(ctx context.Context, svc *service.Service, cfg *config.Config, root string, m service.MilestoneView, cache map[string][]loop.DependencyFinding) []loop.DependencyFinding {
	return loop.Unsatisfied(a.dependencyFindings(ctx, svc, cfg, root, m, cache))
}

// dependencyFindings returns a finding for every dependency of m, or nil when
// m has none, is DONE or CANCELLED, or the check cannot be made. Each
// dependency is checked once per cache.
func (a *app) dependencyFindings(ctx context.Context, svc *service.Service, cfg *config.Config, root string, m service.MilestoneView, cache map[string][]loop.DependencyFinding) []loop.DependencyFinding {
	if len(m.DependsOn) == 0 || m.Status == service.MilestoneDone || m.Status == service.MilestoneCancelled {
		return nil
	}
	if v, ok := cache[repoKey]; ok {
		if v == nil {
			return nil
		}
	} else {
		_, err := loop.ResolveRepo(ctx, root)
		if err != nil {
			a.logger().Debug("dependency check skipped", "milestone", m.ID, "error", err)
			cache[repoKey] = nil
			return nil
		}
		cache[repoKey] = []loop.DependencyFinding{}
	}
	out := make([]loop.DependencyFinding, 0, len(m.DependsOn))
	for _, ref := range m.DependsOn {
		if cached, ok := cache[ref]; ok {
			out = append(out, cached[0])
			continue
		}
		id, err := domain.ParseID(domain.KindMilestone, ref)
		if err == nil {
			var f loop.DependencyFinding
			if f, err = loop.CheckDependency(ctx, svc, cfg, root, id); err == nil {
				cache[ref] = []loop.DependencyFinding{f}
				out = append(out, f)
				continue
			}
		}
		a.logger().Debug("dependency check failed", "milestone", m.ID, "dependency", ref, "error", err)
		return nil
	}
	return out
}

// waitingContext returns the config and project root, or false when they
// cannot be resolved.
func (a *app) waitingContext() (*config.Config, string, bool) {
	l, err := a.loadConfig()
	if err != nil {
		a.logger().Debug("dependency check skipped", "error", err)
		return nil, "", false
	}
	p, err := a.findProject()
	if err != nil {
		a.logger().Debug("dependency check skipped", "error", err)
		return nil, "", false
	}
	return l.Config, p.Root, true
}

// dependsOnLine renders the dependencies with their state; without findings
// it is the plain list.
func dependsOnLine(deps []string, findings []loop.DependencyFinding) string {
	if len(findings) != len(deps) {
		return strings.Join(deps, ", ")
	}
	parts := make([]string, len(deps))
	for i, f := range findings {
		switch {
		case f.State == loop.DependencyCancelled:
			parts[i] = fmt.Sprintf("%s (cancelled)", deps[i])
		case f.Satisfied:
			parts[i] = fmt.Sprintf("%s (merged)", deps[i])
		default:
			parts[i] = fmt.Sprintf("%s (waiting: %s)", deps[i], f.Reason)
		}
	}
	return strings.Join(parts, ", ")
}

func findingIDs(fs []loop.DependencyFinding) []string {
	ids := make([]string, len(fs))
	for i, f := range fs {
		ids[i] = f.ID
	}
	return ids
}
