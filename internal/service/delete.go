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
	v := deleteView(kind, id, c)
	if s.taskManager != nil && s.cardCanceledStatus != "" {
		cards, err := s.deleteCards(ctx, kind, id)
		if err != nil {
			return DeleteView{}, err
		}
		for _, c := range cards {
			v.Cards = append(v.Cards, DeletedCard{ID: domain.FormatID(c.Kind, c.EntityID), Key: c.Key, URL: c.URL})
		}
	}
	return v, nil
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
// the delete see the same state. When cards are enabled, the deleted subtree's
// cards are first moved to the canceled status, outside the transaction and
// after a guard check on its own; a failed move never blocks the delete.
func (s *Service) runDelete(ctx context.Context, kind domain.Kind, id int64, force bool) (DeleteView, error) {
	var cards []DeletedCard
	if s.taskManager != nil && !s.inTx {
		if _, err := s.checkDelete(ctx, kind, id, force); err != nil {
			return DeleteView{}, err
		}
		rows, err := s.deleteCards(ctx, kind, id)
		if err != nil {
			return DeleteView{}, err
		}
		for _, r := range rows {
			dc := DeletedCard{ID: domain.FormatID(r.Kind, r.EntityID), Key: r.Key, URL: r.URL}
			if err := s.taskManager.SetStatus(ctx, r.Key, string(domain.StatusCancelled)); err != nil {
				dc.Error = err.Error()
				s.log.Warn("moving card to canceled", "id", dc.ID, "key", r.Key, "error", err)
			} else {
				dc.Moved = true
			}
			cards = append(cards, dc)
		}
	}
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
		v.Cards = cards
		return nil
	})
	return v, err
}

// deleteCards returns the keyed card rows of the target and its subtree, tasks
// first, then milestones, then the initiative.
func (s *Service) deleteCards(ctx context.Context, kind domain.Kind, id int64) ([]domain.Card, error) {
	var ents []cardID
	switch kind {
	case domain.KindTask:
		ents = []cardID{{domain.KindTask, id}}
	case domain.KindMilestone:
		ts, err := s.store.ListTasks(ctx, store.TaskFilter{MilestoneID: id})
		if err != nil {
			return nil, err
		}
		for _, t := range ts {
			ents = append(ents, cardID{domain.KindTask, t.ID})
		}
		ents = append(ents, cardID{domain.KindMilestone, id})
	case domain.KindInitiative:
		ts, err := s.store.ListTasks(ctx, store.TaskFilter{InitiativeID: id})
		if err != nil {
			return nil, err
		}
		for _, t := range ts {
			ents = append(ents, cardID{domain.KindTask, t.ID})
		}
		ms, err := s.store.ListMilestones(ctx, id, false)
		if err != nil {
			return nil, err
		}
		for _, m := range ms {
			ents = append(ents, cardID{domain.KindMilestone, m.ID})
		}
		ents = append(ents, cardID{domain.KindInitiative, id})
	default:
		return nil, nil
	}
	var out []domain.Card
	for _, e := range ents {
		c, err := s.store.GetCard(ctx, e.kind, e.id, s.taskManager.Name())
		if errors.Is(err, store.ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if c.Key != "" {
			out = append(out, c)
		}
	}
	return out, nil
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
		ms, err := s.store.ListMilestones(ctx, id, false)
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
