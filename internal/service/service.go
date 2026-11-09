// Package service is ralph's application layer. The CLI and the MCP server
// both call it, so validation, status rules, and indexing behave identically
// regardless of who is driving.
package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/bengearig/ralph/internal/domain"
	"github.com/bengearig/ralph/internal/search"
	"github.com/bengearig/ralph/internal/store"
)

// Service implements ralph's use cases over a store and search index.
type Service struct {
	store store.Store
	index *search.Index
	log   *slog.Logger
	// stallThreshold is the Stall Statute: after this many consecutive
	// failed iterations a task is blocked instead of returned to TODO.
	// Zero disables it.
	stallThreshold int
	searchLimit    int
}

// Options configures a Service.
type Options struct {
	StallThreshold int
	// SearchLimit is the result count when a search does not specify one.
	SearchLimit int
	Log         *slog.Logger
}

// New returns a Service. The index must share the store.
func New(st store.Store, ix *search.Index, opts Options) *Service {
	log := opts.Log
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	limit := opts.SearchLimit
	if limit < 1 {
		limit = 10
	}
	return &Service{store: st, index: ix, log: log, stallThreshold: opts.StallThreshold, searchLimit: limit}
}

// Store exposes the underlying store for read-only orchestration such as
// loop planning.
func (s *Service) Store() store.Store { return s.store }

const maxSlugAttempts = 100

// createWithSlug calls create with the requested slug, or with slugs derived
// from title (suffixed -2, -3, … on conflict) when none was requested.
func createWithSlug(requested, title, fallback string, create func(slug string) error) error {
	if requested != "" {
		if err := domain.ValidateSlug(requested); err != nil {
			return err
		}
		return create(requested)
	}
	base := domain.Slugify(title)
	if base == "" {
		base = fallback
	}
	for n := 1; n <= maxSlugAttempts; n++ {
		slug := base
		if n > 1 {
			slug = fmt.Sprintf("%s-%d", base, n)
		}
		err := create(slug)
		if !errors.Is(err, store.ErrConflict) {
			return err
		}
	}
	return fmt.Errorf("%w: could not find a free slug for %q", store.ErrConflict, title)
}

func trimmed(p *string) *string {
	if p == nil {
		return nil
	}
	v := strings.TrimSpace(*p)
	return &v
}

// ---- initiatives ----

type InitiativeInput struct {
	Title string
	Slug  string
	Brief string
}

type InitiativePatch struct {
	Title *string
	Brief *string
}

func (s *Service) CreateInitiative(ctx context.Context, in InitiativeInput) (InitiativeView, error) {
	if err := domain.RequireText("title", in.Title); err != nil {
		return InitiativeView{}, err
	}
	i := domain.Initiative{Title: strings.TrimSpace(in.Title), Brief: strings.TrimSpace(in.Brief)}
	err := createWithSlug(in.Slug, i.Title, "initiative", func(slug string) error {
		i.Slug = slug
		return s.store.CreateInitiative(ctx, &i)
	})
	if err != nil {
		return InitiativeView{}, err
	}
	v := initiativeView(i)
	v.IndexWarning = s.indexInitiative(ctx, i)
	return v, nil
}

func (s *Service) GetInitiative(ctx context.Context, id int64) (InitiativeView, error) {
	i, err := s.store.GetInitiative(ctx, id)
	if err != nil {
		return InitiativeView{}, err
	}
	return initiativeView(i), nil
}

func (s *Service) ListInitiatives(ctx context.Context) ([]InitiativeView, error) {
	all, err := s.store.ListInitiatives(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]InitiativeView, 0, len(all))
	for _, i := range all {
		out = append(out, initiativeView(i))
	}
	return out, nil
}

func (s *Service) UpdateInitiative(ctx context.Context, id int64, p InitiativePatch) (InitiativeView, error) {
	i, err := s.store.GetInitiative(ctx, id)
	if err != nil {
		return InitiativeView{}, err
	}
	if t := trimmed(p.Title); t != nil {
		if err := domain.RequireText("title", *t); err != nil {
			return InitiativeView{}, err
		}
		i.Title = *t
	}
	if b := trimmed(p.Brief); b != nil {
		i.Brief = *b
	}
	if err := s.store.UpdateInitiative(ctx, &i); err != nil {
		return InitiativeView{}, err
	}
	v := initiativeView(i)
	v.IndexWarning = s.indexInitiative(ctx, i)
	return v, nil
}

// ---- milestones ----

type MilestoneInput struct {
	InitiativeID int64
	Title        string
	Slug         string
	Description  string
	Position     int
}

type MilestonePatch struct {
	Title       *string
	Description *string
	Position    *int
}

func (s *Service) CreateMilestone(ctx context.Context, in MilestoneInput) (MilestoneView, error) {
	if err := domain.RequireText("title", in.Title); err != nil {
		return MilestoneView{}, err
	}
	if in.Position < 0 {
		return MilestoneView{}, fmt.Errorf("%w: position must not be negative", domain.ErrInvalid)
	}
	if _, err := s.store.GetInitiative(ctx, in.InitiativeID); err != nil {
		return MilestoneView{}, err
	}
	m := domain.Milestone{
		InitiativeID: in.InitiativeID,
		Title:        strings.TrimSpace(in.Title),
		Description:  strings.TrimSpace(in.Description),
		Position:     in.Position,
	}
	err := createWithSlug(in.Slug, m.Title, "milestone", func(slug string) error {
		m.Slug = slug
		m.Position = in.Position
		return s.store.CreateMilestone(ctx, &m)
	})
	if err != nil {
		return MilestoneView{}, err
	}
	v := milestoneView(m, nil)
	v.IndexWarning = s.indexMilestone(ctx, m)
	return v, nil
}

func (s *Service) GetMilestone(ctx context.Context, id int64) (MilestoneView, error) {
	m, err := s.store.GetMilestone(ctx, id)
	if err != nil {
		return MilestoneView{}, err
	}
	tasks, err := s.store.ListTasks(ctx, store.TaskFilter{MilestoneID: id})
	if err != nil {
		return MilestoneView{}, err
	}
	return milestoneView(m, tasks), nil
}

func (s *Service) ListMilestones(ctx context.Context, initiativeID int64) ([]MilestoneView, error) {
	if initiativeID != 0 {
		if _, err := s.store.GetInitiative(ctx, initiativeID); err != nil {
			return nil, err
		}
	}
	ms, err := s.store.ListMilestones(ctx, initiativeID)
	if err != nil {
		return nil, err
	}
	tasks, err := s.store.ListTasks(ctx, store.TaskFilter{InitiativeID: initiativeID})
	if err != nil {
		return nil, err
	}
	byMilestone := map[int64][]domain.Task{}
	for _, t := range tasks {
		byMilestone[t.MilestoneID] = append(byMilestone[t.MilestoneID], t)
	}
	out := make([]MilestoneView, 0, len(ms))
	for _, m := range ms {
		out = append(out, milestoneView(m, byMilestone[m.ID]))
	}
	return out, nil
}

func (s *Service) UpdateMilestone(ctx context.Context, id int64, p MilestonePatch) (MilestoneView, error) {
	m, err := s.store.GetMilestone(ctx, id)
	if err != nil {
		return MilestoneView{}, err
	}
	if t := trimmed(p.Title); t != nil {
		if err := domain.RequireText("title", *t); err != nil {
			return MilestoneView{}, err
		}
		m.Title = *t
	}
	if d := trimmed(p.Description); d != nil {
		m.Description = *d
	}
	if p.Position != nil {
		if *p.Position < 1 {
			return MilestoneView{}, fmt.Errorf("%w: position must be at least 1", domain.ErrInvalid)
		}
		m.Position = *p.Position
	}
	if err := s.store.UpdateMilestone(ctx, &m); err != nil {
		return MilestoneView{}, err
	}
	tasks, err := s.store.ListTasks(ctx, store.TaskFilter{MilestoneID: id})
	if err != nil {
		return MilestoneView{}, err
	}
	v := milestoneView(m, tasks)
	v.IndexWarning = s.indexMilestone(ctx, m)
	return v, nil
}

// ---- git records ----

// RecordMilestoneGit replaces a milestone's recorded git state. Only the loop
// and reconcile call it; it neither reindexes nor changes updated_at.
func (s *Service) RecordMilestoneGit(ctx context.Context, milestoneID int64, g domain.MilestoneGit) error {
	if err := domain.ValidCommit(g.BaseCommit); err != nil {
		return err
	}
	if err := domain.ValidCommit(g.HeadCommit); err != nil {
		return err
	}
	if strings.TrimSpace(g.Branch) == "" && (g.BaseCommit != "" || g.HeadCommit != "") {
		return fmt.Errorf("%w: recorded commits need a branch", domain.ErrInvalid)
	}
	return s.store.SetMilestoneGit(ctx, milestoneID, g)
}

// RecordMilestoneHead updates only the recorded head of a milestone's branch.
func (s *Service) RecordMilestoneHead(ctx context.Context, milestoneID int64, head string) error {
	if err := domain.ValidCommit(head); err != nil {
		return err
	}
	return s.store.InTx(ctx, func(tx store.Store) error {
		m, err := tx.GetMilestone(ctx, milestoneID)
		if err != nil {
			return err
		}
		g := m.Git
		g.HeadCommit = head
		if strings.TrimSpace(g.Branch) == "" && head != "" {
			return fmt.Errorf("%w: %s has no recorded branch to set a head on",
				domain.ErrInvalid, domain.FormatID(domain.KindMilestone, milestoneID))
		}
		return tx.SetMilestoneGit(ctx, milestoneID, g)
	})
}

// RecordTaskCommit records the loop's commit for a task.
func (s *Service) RecordTaskCommit(ctx context.Context, taskID int64, hash string) error {
	if err := domain.ValidCommit(hash); err != nil {
		return err
	}
	return s.store.SetTaskCommit(ctx, taskID, hash)
}

// ---- tasks ----

type TaskInput struct {
	MilestoneID        int64
	Title              string
	Description        string
	AcceptanceCriteria string
	Position           int
}

type TaskPatch struct {
	Title              *string
	Description        *string
	AcceptanceCriteria *string
	Position           *int
}

type TaskFilter struct {
	InitiativeID int64
	MilestoneID  int64
	Statuses     []domain.Status
}

// CreateTask adds a PROPOSED task; it reaches TODO only through SetTaskStatus
// once fully articulated.
func (s *Service) CreateTask(ctx context.Context, in TaskInput) (TaskView, error) {
	if err := domain.RequireText("title", in.Title); err != nil {
		return TaskView{}, err
	}
	if in.Position < 0 {
		return TaskView{}, fmt.Errorf("%w: position must not be negative", domain.ErrInvalid)
	}
	m, err := s.store.GetMilestone(ctx, in.MilestoneID)
	if err != nil {
		return TaskView{}, err
	}
	t := domain.Task{
		MilestoneID:        m.ID,
		Title:              strings.TrimSpace(in.Title),
		Description:        strings.TrimSpace(in.Description),
		AcceptanceCriteria: strings.TrimSpace(in.AcceptanceCriteria),
		Status:             domain.StatusProposed,
		Position:           in.Position,
	}
	if err := s.store.CreateTask(ctx, &t); err != nil {
		return TaskView{}, err
	}
	v := taskView(t, nil)
	v.IndexWarning = s.indexTask(ctx, t, m.InitiativeID)
	return v, nil
}

// GetTask returns a task with its notes.
func (s *Service) GetTask(ctx context.Context, id int64) (TaskView, error) {
	t, err := s.store.GetTask(ctx, id)
	if err != nil {
		return TaskView{}, err
	}
	notes, err := s.store.ListTaskNotes(ctx, id)
	if err != nil {
		return TaskView{}, err
	}
	return taskView(t, notes), nil
}

func (s *Service) ListTasks(ctx context.Context, f TaskFilter) ([]TaskView, error) {
	if f.MilestoneID != 0 {
		if _, err := s.store.GetMilestone(ctx, f.MilestoneID); err != nil {
			return nil, err
		}
	}
	if f.InitiativeID != 0 {
		if _, err := s.store.GetInitiative(ctx, f.InitiativeID); err != nil {
			return nil, err
		}
	}
	tasks, err := s.store.ListTasks(ctx, store.TaskFilter(f))
	if err != nil {
		return nil, err
	}
	out := make([]TaskView, 0, len(tasks))
	for _, t := range tasks {
		out = append(out, taskView(t, nil))
	}
	return out, nil
}

// UpdateTask edits a task's text. Terminal tasks are immutable, and tasks
// already past articulation must stay loopable.
func (s *Service) UpdateTask(ctx context.Context, id int64, p TaskPatch) (TaskView, error) {
	t, err := s.store.GetTask(ctx, id)
	if err != nil {
		return TaskView{}, err
	}
	if t.Status.Terminal() {
		return TaskView{}, fmt.Errorf("%w: %s is %s and can no longer be edited",
			domain.ErrInvalid, domain.FormatID(domain.KindTask, id), t.Status)
	}
	if v := trimmed(p.Title); v != nil {
		if err := domain.RequireText("title", *v); err != nil {
			return TaskView{}, err
		}
		t.Title = *v
	}
	if v := trimmed(p.Description); v != nil {
		t.Description = *v
	}
	if v := trimmed(p.AcceptanceCriteria); v != nil {
		t.AcceptanceCriteria = *v
	}
	if p.Position != nil {
		if *p.Position < 1 {
			return TaskView{}, fmt.Errorf("%w: position must be at least 1", domain.ErrInvalid)
		}
		t.Position = *p.Position
	}
	if t.Status != domain.StatusProposed && (t.Description == "" || t.AcceptanceCriteria == "") {
		return TaskView{}, fmt.Errorf("%w: a %s task must keep its description and acceptance criteria; move it back to PROPOSED first",
			domain.ErrInvalid, t.Status)
	}
	if err := s.store.UpdateTask(ctx, &t); err != nil {
		return TaskView{}, err
	}
	m, err := s.store.GetMilestone(ctx, t.MilestoneID)
	if err != nil {
		return TaskView{}, err
	}
	v := taskView(t, nil)
	v.IndexWarning = s.indexTask(ctx, t, m.InitiativeID)
	return v, nil
}

// SetTaskStatus moves a task through the state machine, recording the
// reason as a note. A failed iteration that reaches the stall threshold
// blocks the task instead (the Stall Statute).
func (s *Service) SetTaskStatus(ctx context.Context, id int64, to domain.Status, reason string) (TaskView, error) {
	reason = strings.TrimSpace(reason)
	var out TaskView
	err := s.store.InTx(ctx, func(tx store.Store) error {
		t, err := tx.GetTask(ctx, id)
		if err != nil {
			return err
		}
		if err := t.CheckTransition(to, reason); err != nil {
			return err
		}
		from := t.Status
		t.ApplyTransition(to, reason)

		stalled := from == domain.StatusInProgress && to == domain.StatusTodo &&
			s.stallThreshold > 0 && t.FailureCount >= s.stallThreshold
		if stalled {
			why := fmt.Sprintf("Stall Statute: %d consecutive failed iterations", t.FailureCount)
			if reason != "" {
				why += "; last: " + reason
			}
			t.Status = from
			t.ApplyTransition(domain.StatusBlocked, why)
			reason = why
			to = domain.StatusBlocked
		}
		if err := tx.UpdateTask(ctx, &t); err != nil {
			return err
		}
		if reason != "" {
			note := domain.TaskNote{
				TaskID: id,
				Kind:   domain.TransitionNoteKind(from, to),
				Body:   fmt.Sprintf("%s → %s: %s", from, to, reason),
			}
			if err := tx.AddTaskNote(ctx, &note); err != nil {
				return err
			}
		}
		notes, err := tx.ListTaskNotes(ctx, id)
		if err != nil {
			return err
		}
		out = taskView(t, notes)
		return nil
	})
	return out, err
}

// AddTaskNote appends a note to a task.
func (s *Service) AddTaskNote(ctx context.Context, id int64, kind domain.NoteKind, body string) (TaskView, error) {
	if err := domain.RequireText("note", body); err != nil {
		return TaskView{}, err
	}
	if _, err := s.store.GetTask(ctx, id); err != nil {
		return TaskView{}, err
	}
	if err := s.store.AddTaskNote(ctx, &domain.TaskNote{TaskID: id, Kind: kind, Body: strings.TrimSpace(body)}); err != nil {
		return TaskView{}, err
	}
	return s.GetTask(ctx, id)
}

// NextTask returns the task the loop should work on in a milestone: an
// interrupted IN_PROGRESS task first, otherwise the first TODO by position.
// It returns nil when nothing is loopable.
func (s *Service) NextTask(ctx context.Context, milestoneID int64) (*TaskView, error) {
	if _, err := s.store.GetMilestone(ctx, milestoneID); err != nil {
		return nil, err
	}
	for _, st := range []domain.Status{domain.StatusInProgress, domain.StatusTodo} {
		tasks, err := s.store.ListTasks(ctx, store.TaskFilter{MilestoneID: milestoneID, Statuses: []domain.Status{st}})
		if err != nil {
			return nil, err
		}
		if len(tasks) > 0 {
			v, err := s.GetTask(ctx, tasks[0].ID)
			if err != nil {
				return nil, err
			}
			return &v, nil
		}
	}
	return nil, nil
}

// ---- documents ----

type DocumentInput struct {
	// InitiativeID zero means a project-wide document.
	InitiativeID int64
	Kind         domain.DocKind
	Title        string
	Slug         string
	Body         string
}

type DocumentPatch struct {
	Kind  *domain.DocKind
	Title *string
	Body  *string
}

type DocumentFilter struct {
	InitiativeID int64
	ProjectOnly  bool
	Kind         domain.DocKind
}

func (s *Service) CreateDocument(ctx context.Context, in DocumentInput) (DocumentView, error) {
	if err := domain.RequireText("title", in.Title); err != nil {
		return DocumentView{}, err
	}
	if _, err := domain.ParseDocKind(string(in.Kind)); err != nil {
		return DocumentView{}, err
	}
	d := domain.Document{Kind: in.Kind, Title: strings.TrimSpace(in.Title), Body: strings.TrimSpace(in.Body)}
	if in.InitiativeID != 0 {
		if _, err := s.store.GetInitiative(ctx, in.InitiativeID); err != nil {
			return DocumentView{}, err
		}
		id := in.InitiativeID
		d.InitiativeID = &id
	}
	err := createWithSlug(in.Slug, d.Title, string(d.Kind), func(slug string) error {
		d.Slug = slug
		return s.store.CreateDocument(ctx, &d)
	})
	if err != nil {
		return DocumentView{}, err
	}
	v := documentView(d, true)
	v.IndexWarning = s.indexDocument(ctx, d)
	return v, nil
}

func (s *Service) GetDocument(ctx context.Context, id int64) (DocumentView, error) {
	d, err := s.store.GetDocument(ctx, id)
	if err != nil {
		return DocumentView{}, err
	}
	return documentView(d, true), nil
}

// ListDocuments omits bodies; fetch a document to read it.
func (s *Service) ListDocuments(ctx context.Context, f DocumentFilter) ([]DocumentView, error) {
	if f.Kind != "" {
		if _, err := domain.ParseDocKind(string(f.Kind)); err != nil {
			return nil, err
		}
	}
	if f.InitiativeID != 0 {
		if _, err := s.store.GetInitiative(ctx, f.InitiativeID); err != nil {
			return nil, err
		}
	}
	docs, err := s.store.ListDocuments(ctx, store.DocumentFilter(f))
	if err != nil {
		return nil, err
	}
	out := make([]DocumentView, 0, len(docs))
	for _, d := range docs {
		out = append(out, documentView(d, false))
	}
	return out, nil
}

func (s *Service) UpdateDocument(ctx context.Context, id int64, p DocumentPatch) (DocumentView, error) {
	d, err := s.store.GetDocument(ctx, id)
	if err != nil {
		return DocumentView{}, err
	}
	if p.Kind != nil {
		k, err := domain.ParseDocKind(string(*p.Kind))
		if err != nil {
			return DocumentView{}, err
		}
		d.Kind = k
	}
	if t := trimmed(p.Title); t != nil {
		if err := domain.RequireText("title", *t); err != nil {
			return DocumentView{}, err
		}
		d.Title = *t
	}
	if b := trimmed(p.Body); b != nil {
		d.Body = *b
	}
	if err := s.store.UpdateDocument(ctx, &d); err != nil {
		return DocumentView{}, err
	}
	v := documentView(d, true)
	v.IndexWarning = s.indexDocument(ctx, d)
	return v, nil
}
