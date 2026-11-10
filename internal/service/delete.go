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

func (s *Service) DeleteInitiative(ctx context.Context, id int64, force bool) (DeleteView, error) {
	var v DeleteView
	err := s.InTx(ctx, func(tx *Service) error {
		c, err := tx.store.CountCascade(ctx, domain.KindInitiative, id)
		if err != nil {
			return err
		}
		what := domain.FormatID(domain.KindInitiative, id)
		ms, err := tx.store.ListMilestones(ctx, id)
		if err != nil {
			return err
		}
		affected := make([]int64, len(ms))
		for i, m := range ms {
			affected[i] = m.ID
		}
		if err := tx.guardLoop(what, affected); err != nil {
			return err
		}
		if !force && (c.Milestones > 0 || c.Documents > 0) {
			var parts []string
			if c.Milestones > 0 {
				parts = append(parts, plural(c.Milestones, "milestone"))
			}
			if c.Documents > 0 {
				parts = append(parts, plural(c.Documents, "document"))
			}
			return fmt.Errorf("%w: %s has %s; pass force to delete them too",
				ErrNeedsForce, what, strings.Join(parts, " and "))
		}
		if err := tx.store.DeleteInitiative(ctx, id); err != nil {
			return err
		}
		v = deleteView(domain.KindInitiative, id, c)
		return nil
	})
	return v, err
}

func (s *Service) DeleteMilestone(ctx context.Context, id int64, force bool) (DeleteView, error) {
	var v DeleteView
	err := s.InTx(ctx, func(tx *Service) error {
		c, err := tx.store.CountCascade(ctx, domain.KindMilestone, id)
		if err != nil {
			return err
		}
		what := domain.FormatID(domain.KindMilestone, id)
		if err := tx.guardLoop(what, []int64{id}); err != nil {
			return err
		}
		if !force && c.Tasks > 0 {
			return fmt.Errorf("%w: %s has %s; pass force to delete them too",
				ErrNeedsForce, what, plural(c.Tasks, "task"))
		}
		if err := tx.store.DeleteMilestone(ctx, id); err != nil {
			return err
		}
		v = deleteView(domain.KindMilestone, id, c)
		return nil
	})
	return v, err
}

func (s *Service) DeleteTask(ctx context.Context, id int64, force bool) (DeleteView, error) {
	var v DeleteView
	err := s.InTx(ctx, func(tx *Service) error {
		c, err := tx.store.CountCascade(ctx, domain.KindTask, id)
		if err != nil {
			return err
		}
		t, err := tx.store.GetTask(ctx, id)
		if err != nil {
			return err
		}
		what := domain.FormatID(domain.KindTask, id)
		if err := tx.guardLoop(what, []int64{t.MilestoneID}); err != nil {
			return err
		}
		if !force && (t.Status == domain.StatusInProgress || t.Status == domain.StatusDone) {
			return fmt.Errorf("%w: %s is %s; pass force to delete it",
				ErrNeedsForce, what, t.Status)
		}
		if err := tx.store.DeleteTask(ctx, id); err != nil {
			return err
		}
		v = deleteView(domain.KindTask, id, c)
		return nil
	})
	return v, err
}

func (s *Service) DeleteDocument(ctx context.Context, id int64) (DeleteView, error) {
	var v DeleteView
	err := s.InTx(ctx, func(tx *Service) error {
		if err := tx.store.DeleteDocument(ctx, id); err != nil {
			return err
		}
		v = deleteView(domain.KindDocument, id, store.Cascade{Documents: 1})
		return nil
	})
	return v, err
}

// DeleteNote removes one task note. Notes are history, so any task status
// allows it, but a running loop's milestone does not. Notes are not indexed.
func (s *Service) DeleteNote(ctx context.Context, id int64) (DeleteView, error) {
	var v DeleteView
	err := s.InTx(ctx, func(tx *Service) error {
		n, err := tx.store.GetTaskNote(ctx, id)
		if err != nil {
			return err
		}
		t, err := tx.store.GetTask(ctx, n.TaskID)
		if err != nil {
			return err
		}
		if err := tx.guardLoop(domain.FormatID(domain.KindNote, id), []int64{t.MilestoneID}); err != nil {
			return err
		}
		if err := tx.store.DeleteTaskNote(ctx, id); err != nil {
			return err
		}
		v = deleteView(domain.KindNote, id, store.Cascade{Notes: 1})
		return nil
	})
	return v, err
}

// guardLoop refuses a delete that touches the milestone a running loop holds,
// whatever force says. An unparsable holder is assumed to touch everything.
func (s *Service) guardLoop(what string, milestoneIDs []int64) error {
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
		return fmt.Errorf("%w: cannot delete %s while a loop holds the lock (holder unknown)",
			looplock.ErrLoopRunning, what)
	}
	return fmt.Errorf("%w: cannot delete %s while the loop (pid %d) is working on %s",
		looplock.ErrLoopRunning, what, h.PID, h.Milestone)
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
