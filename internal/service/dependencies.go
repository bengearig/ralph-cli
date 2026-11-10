package service

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/bengearig/ralph/internal/domain"
	"github.com/bengearig/ralph/internal/store"
)

func milestoneRef(id int64) string { return domain.FormatID(domain.KindMilestone, id) }

// formatDependsOn formats dependency IDs in ascending order; nil when there are none.
func formatDependsOn(ids []int64) []string {
	if len(ids) == 0 {
		return nil
	}
	sorted := slices.Sorted(slices.Values(ids))
	out := make([]string, len(sorted))
	for i, id := range sorted {
		out[i] = milestoneRef(id)
	}
	return out
}

// setDependencies validates and stores the whole dependency set of an existing milestone. The caller owns the
// transaction and the initiative's writability check.
func (s *Service) setDependencies(ctx context.Context, milestoneID int64, dependsOn []int64) error {
	if err := s.validateDependencies(ctx, milestoneID, dependsOn); err != nil {
		return err
	}
	return s.store.SetMilestoneDependencies(ctx, milestoneID, slices.Sorted(slices.Values(dependsOn)))
}

func (s *Service) validateDependencies(ctx context.Context, milestoneID int64, dependsOn []int64) error {
	seen := map[int64]bool{}
	for _, dep := range dependsOn {
		switch {
		case dep == milestoneID:
			return fmt.Errorf("%w: a milestone cannot depend on itself", domain.ErrInvalid)
		case seen[dep]:
			return fmt.Errorf("%w: %s is listed more than once", domain.ErrInvalid, milestoneRef(dep))
		}
		seen[dep] = true
	}
	for _, dep := range dependsOn {
		if _, err := s.store.GetMilestone(ctx, dep); err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return fmt.Errorf("%w: %s does not exist", domain.ErrInvalid, milestoneRef(dep))
			}
			return err
		}
	}
	if len(dependsOn) == 0 {
		return nil
	}
	edges, err := s.store.ListMilestoneDependencies(ctx, 0)
	if err != nil {
		return err
	}
	edges[milestoneID] = dependsOn
	for _, dep := range slices.Sorted(slices.Values(dependsOn)) {
		if path := findPath(edges, dep, milestoneID, nil, map[int64]bool{}); path != nil {
			ids := append([]int64{milestoneID}, path...)
			refs := make([]string, len(ids))
			for i, id := range ids {
				refs[i] = milestoneRef(id)
			}
			return fmt.Errorf("%w: dependency cycle %s", domain.ErrInvalid, strings.Join(refs, " -> "))
		}
	}
	return nil
}

// findPath searches depth-first along dependencies from node for target and returns the nodes walked, ending at
// target; nil when target is unreachable.
func findPath(edges map[int64][]int64, node, target int64, path []int64, visited map[int64]bool) []int64 {
	path = append(path, node)
	if node == target {
		return path
	}
	if visited[node] {
		return nil
	}
	visited[node] = true
	for _, next := range edges[node] {
		if p := findPath(edges, next, target, path, visited); p != nil {
			return p
		}
	}
	return nil
}

// deleteDependents lists "M-5 depends on M-2" for each milestone outside the milestones being deleted that depends
// on one inside them.
func (s *Service) deleteDependents(ctx context.Context, deleted []int64) ([]string, error) {
	inside := map[int64]bool{}
	for _, id := range deleted {
		inside[id] = true
	}
	var out []string
	for _, id := range slices.Sorted(slices.Values(deleted)) {
		deps, err := s.store.ListDependents(ctx, id)
		if err != nil {
			return nil, err
		}
		for _, d := range deps {
			if !inside[d] {
				out = append(out, fmt.Sprintf("%s depends on %s", milestoneRef(d), milestoneRef(id)))
			}
		}
	}
	return out, nil
}
