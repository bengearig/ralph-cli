package domain

import (
	"fmt"
	"slices"
	"strings"
)

// Status is a task's position in the RALPH lifecycle.
type Status string

const (
	// StatusProposed tasks are still being articulated.
	StatusProposed Status = "PROPOSED"
	// StatusTodo tasks are fully articulated and ready for the loop.
	StatusTodo       Status = "TODO"
	StatusInProgress Status = "IN_PROGRESS"
	// StatusBlocked tasks need help; see the Kickback Clause.
	StatusBlocked   Status = "BLOCKED"
	StatusCancelled Status = "CANCELLED"
	StatusDone      Status = "DONE"
)

// Statuses returns every status in lifecycle order.
func Statuses() []Status {
	return []Status{StatusProposed, StatusTodo, StatusInProgress, StatusBlocked, StatusCancelled, StatusDone}
}

var transitions = map[Status][]Status{
	StatusProposed:   {StatusTodo, StatusCancelled},
	StatusTodo:       {StatusInProgress, StatusProposed, StatusCancelled},
	StatusInProgress: {StatusDone, StatusTodo, StatusBlocked, StatusProposed},
	StatusBlocked:    {StatusTodo, StatusProposed, StatusCancelled},
	StatusCancelled:  nil,
	StatusDone:       nil,
}

// ParseStatus accepts a status case-insensitively, with dashes or spaces in
// place of underscores.
func ParseStatus(s string) (Status, error) {
	norm := strings.ToUpper(strings.TrimSpace(s))
	norm = strings.NewReplacer("-", "_", " ", "_").Replace(norm)
	st := Status(norm)
	if _, ok := transitions[st]; !ok {
		names := make([]string, 0, len(transitions))
		for _, x := range Statuses() {
			names = append(names, string(x))
		}
		return "", fmt.Errorf("%w: unknown status %q (expected one of %s)", ErrInvalid, s, strings.Join(names, ", "))
	}
	return st, nil
}

// Terminal reports whether no further transitions are possible.
func (s Status) Terminal() bool { return len(transitions[s]) == 0 }

// AllowedNext lists the statuses reachable from s.
func (s Status) AllowedNext() []Status { return slices.Clone(transitions[s]) }

// CanTransition reports whether the state machine permits from → to.
func CanTransition(from, to Status) bool {
	return slices.Contains(transitions[from], to)
}

// CheckTransition validates moving t to status `to`, including the guards
// that make TODO mean "ready to loop" and BLOCKED mean "explained".
func (t Task) CheckTransition(to Status, reason string) error {
	if !CanTransition(t.Status, to) {
		allowed := make([]string, 0, len(transitions[t.Status]))
		for _, s := range transitions[t.Status] {
			allowed = append(allowed, string(s))
		}
		next := "none; it is terminal"
		if len(allowed) > 0 {
			next = strings.Join(allowed, ", ")
		}
		return fmt.Errorf("%w: %s cannot move from %s to %s (allowed: %s)",
			ErrInvalidTransition, FormatID(KindTask, t.ID), t.Status, to, next)
	}
	switch to {
	case StatusTodo:
		var missing []string
		if strings.TrimSpace(t.Description) == "" {
			missing = append(missing, "description")
		}
		if strings.TrimSpace(t.AcceptanceCriteria) == "" {
			missing = append(missing, "acceptance criteria")
		}
		if len(missing) > 0 {
			return fmt.Errorf("%w: %s is not articulated enough for TODO; add %s first",
				ErrInvalidTransition, FormatID(KindTask, t.ID), strings.Join(missing, " and "))
		}
	case StatusBlocked:
		if strings.TrimSpace(reason) == "" {
			return fmt.Errorf("%w: moving %s to BLOCKED requires a reason", ErrInvalidTransition, FormatID(KindTask, t.ID))
		}
	}
	return nil
}

// ApplyTransition mutates t for a validated move to `to`. A failed
// iteration (IN_PROGRESS → TODO) counts toward the Stall Statute; success
// resets the count.
func (t *Task) ApplyTransition(to Status, reason string) {
	from := t.Status
	t.Status = to
	switch {
	case to == StatusBlocked:
		t.BlockedReason = strings.TrimSpace(reason)
	case from == StatusBlocked:
		t.BlockedReason = ""
	}
	switch {
	case from == StatusInProgress && to == StatusTodo:
		t.FailureCount++
	case to == StatusDone:
		t.FailureCount = 0
	}
}

// TransitionNoteKind classifies the note recorded alongside a transition.
func TransitionNoteKind(from, to Status) NoteKind {
	switch {
	case to == StatusBlocked, to == StatusProposed && from != StatusTodo:
		return NoteKickback
	case from == StatusInProgress:
		return NoteIteration
	}
	return NoteGeneral
}
