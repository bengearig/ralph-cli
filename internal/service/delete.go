package service

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/bengearig/ralph/internal/domain"
	"github.com/bengearig/ralph/internal/looplock"
	"github.com/bengearig/ralph/internal/store"
)

// ErrNeedsForce marks a delete refused because it would remove more than the
// target, or a task that has progressed; the caller may retry with force.
var ErrNeedsForce = errors.New("needs force")

// PreviewDelete reports what the matching delete would remove, or the error it
// would refuse with, without deleting anything.
func (s *Service) PreviewDelete(ctx context.Context, kind domain.Kind, id int64, force bool) (DeleteView, error) {
	c, err := s.checkDelete(ctx, kind, id, force)
	if err != nil {
		return DeleteView{}, err
	}
	return deleteView(kind, id, c), nil
}

func (s *Service) DeleteInitiative(ctx context.Context, id int64, force bool) (DeleteView, error) {
	return s.runDelete(ctx, domain.KindInitiative, id, force)
}

func (s *Service) DeleteMilestone(ctx context.Context, id int64, force bool) (DeleteView, error) {
	return s.runDelete(ctx, domain.KindMilestone, id, force)
}

func (s *Service) DeleteTask(ctx context.Context, id int64, force bool) (DeleteView, error) {
	return s.runDelete(ctx, domain.KindTask, id, force)
}

func (s *Service) DeleteDocument(ctx context.Context, id int64) (DeleteView, error) {
	return s.runDelete(ctx, domain.KindDocument, id, false)
}

// DeleteNote removes one task note. Notes are history, so any task status
// allows it, but a running loop's milestone does not. Notes are not indexed.
func (s *Service) DeleteNote(ctx context.Context, id int64) (DeleteView, error) {
	return s.runDelete(ctx, domain.KindNote, id, false)
}

// runDelete checks the guards and deletes in one transaction, so the check and
// the delete see the same state.
func (s *Service) runDelete(ctx context.Context, kind domain.Kind, id int64, force bool) (DeleteView, error) {
	var v DeleteView
	err := s.InTx(ctx, func(tx *Service) error {
		c, err := tx.checkDelete(ctx, kind, id, force)
		if err != nil {
			return err
		}
		if err := tx.deleteRow(ctx, kind, id); err != nil {
			return err
		}
		v = deleteView(kind, id, c)
		return nil
	})
	return v, err
}

func (s *Service) deleteRow(ctx context.Context, kind domain.Kind, id int64) error {
	switch kind {
	case domain.KindInitiative:
		return s.store.DeleteInitiative(ctx, id)
	case domain.KindMilestone:
		return s.store.DeleteMilestone(ctx, id)
	case domain.KindTask:
		return s.store.DeleteTask(ctx, id)
	case domain.KindDocument:
		return s.store.DeleteDocument(ctx, id)
	case domain.KindNote:
		return s.store.DeleteTaskNote(ctx, id)
	}
	return fmt.Errorf("%w: cannot delete a %s", domain.ErrInvalid, kind)
}

// checkDelete applies every refusal a delete of the target is subject to and
// returns the rows the delete would remove.
func (s *Service) checkDelete(ctx context.Context, kind domain.Kind, id int64, force bool) (store.Cascade, error) {
	what := domain.FormatID(kind, id)
	switch kind {
	case domain.KindInitiative:
		c, err := s.store.CountCascade(ctx, kind, id)
		if err != nil {
			return c, err
		}
		ms, err := s.store.ListMilestones(ctx, id)
		if err != nil {
			return c, err
		}
		affected := make([]int64, len(ms))
		for i, m := range ms {
			affected[i] = m.ID
		}
		if err := s.guardLoop("delete", what, affected); err != nil {
			return c, err
		}
		if !force && (c.Milestones > 0 || c.Documents > 0) {
			var parts []string
			if c.Milestones > 0 {
				parts = append(parts, plural(c.Milestones, "milestone"))
			}
			if c.Documents > 0 {
				parts = append(parts, plural(c.Documents, "document"))
			}
			return c, fmt.Errorf("%w: %s has %s; pass force to delete them too",
				ErrNeedsForce, what, strings.Join(parts, " and "))
		}
		return c, nil
	case domain.KindMilestone:
		c, err := s.store.CountCascade(ctx, kind, id)
		if err != nil {
			return c, err
		}
		if err := s.guardLoop("delete", what, []int64{id}); err != nil {
			return c, err
		}
		if !force && c.Tasks > 0 {
			return c, fmt.Errorf("%w: %s has %s; pass force to delete them too",
				ErrNeedsForce, what, plural(c.Tasks, "task"))
		}
		return c, nil
	case domain.KindTask:
		c, err := s.store.CountCascade(ctx, kind, id)
		if err != nil {
			return c, err
		}
		t, err := s.store.GetTask(ctx, id)
		if err != nil {
			return c, err
		}
		if err := s.guardLoop("delete", what, []int64{t.MilestoneID}); err != nil {
			return c, err
		}
		if !force && (t.Status == domain.StatusInProgress || t.Status == domain.StatusDone) {
			return c, fmt.Errorf("%w: %s is %s; pass force to delete it",
				ErrNeedsForce, what, t.Status)
		}
		return c, nil
	case domain.KindDocument:
		if _, err := s.store.GetDocument(ctx, id); err != nil {
			return store.Cascade{}, err
		}
		return store.Cascade{Documents: 1}, nil
	case domain.KindNote:
		n, err := s.store.GetTaskNote(ctx, id)
		if err != nil {
			return store.Cascade{}, err
		}
		t, err := s.store.GetTask(ctx, n.TaskID)
		if err != nil {
			return store.Cascade{}, err
		}
		if err := s.guardLoop("delete", what, []int64{t.MilestoneID}); err != nil {
			return store.Cascade{}, err
		}
		return store.Cascade{Notes: 1}, nil
	}
	return store.Cascade{}, fmt.Errorf("%w: cannot delete a %s", domain.ErrInvalid, kind)
}

// guardLoop refuses an action (such as delete or archive) that touches the milestone a running loop holds,
// whatever force says. An unparsable holder is assumed to touch everything.
func (s *Service) guardLoop(action, what string, milestoneIDs []int64) error {
	if s.loopLockPath == "" {
		return nil
	}
	h, held, err := looplock.Probe(s.loopLockPath)
	if err != nil {
		return err
	}
	if !held {
		return nil
	}
	affected := h.Milestone == ""
	for _, id := range milestoneIDs {
		if domain.FormatID(domain.KindMilestone, id) == h.Milestone {
			affected = true
		}
	}
	if !affected {
		return nil
	}
	if h.Milestone == "" {
		return fmt.Errorf("%w: cannot %s %s while a loop holds the lock (holder unknown)",
			looplock.ErrLoopRunning, action, what)
	}
	return fmt.Errorf("%w: cannot %s %s while the loop (pid %d) is working on %s",
		looplock.ErrLoopRunning, action, what, h.PID, h.Milestone)
}

func deleteView(kind domain.Kind, id int64, c store.Cascade) DeleteView {
	deleted := map[string]int{}
	for k, n := range map[string]int{
		"initiatives": c.Initiatives,
		"milestones":  c.Milestones,
		"tasks":       c.Tasks,
		"notes":       c.Notes,
		"documents":   c.Documents,
	} {
		if n > 0 {
			deleted[k] = n
		}
	}
	return DeleteView{ID: domain.FormatID(kind, id), Kind: string(kind), Deleted: deleted}
}

func plural(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return fmt.Sprintf("%d %ss", n, noun)
}
