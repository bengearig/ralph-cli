// Package service is ralph's application layer. The CLI and the MCP server
// both call it, so validation, status rules, and indexing behave identically
// regardless of who is driving.
package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/bengearig/ralph/internal/domain"
	"github.com/bengearig/ralph/internal/search"
	"github.com/bengearig/ralph/internal/store"
	"github.com/bengearig/ralph/internal/taskmanager"
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
	loopLockPath   string
	// taskManager is nil when no provider is configured.
	taskManager    taskmanager.Provider
	cardClaimStale time.Duration
	// cardTimeout is the provider call timeout; a deliberate sync claims with it as the stale window.
	cardTimeout time.Duration
	// cardCanceledStatus is the status a deleted resource's card moves to; empty leaves cards alone.
	cardCanceledStatus string
	// cardKinds lists the kinds that get new cards; nil means every kind.
	cardKinds []domain.Kind
	// inTx marks a service bound to a transaction, which never calls the provider.
	inTx bool
	// pullRequestLink builds a pull request's web URL for views; nil leaves URLs empty.
	pullRequestLink func(provider, namespace, repository string, number int) (string, bool)
	// storeID is the normalized configured identifier; empty means none is set.
	storeID string
	// storeIDCache is shared with services bound to a transaction.
	storeIDCache *storeIDCache
}

// Options configures a Service.
type Options struct {
	StallThreshold int
	// SearchLimit is the result count when a search does not specify one.
	SearchLimit int
	Log         *slog.Logger
	// LoopLockPath is the loop's lock file; deletes touching the held
	// milestone are refused. Empty disables the guard.
	LoopLockPath string
	// TaskManager creates cards for new resources; nil disables it.
	TaskManager taskmanager.Provider
	// CardClaimStale is how long an unfinished card claim holds off other processes; zero means 30 seconds.
	CardClaimStale time.Duration
	// CardCanceledStatus is the status deleted resources' cards move to; empty means they are left as they are.
	CardCanceledStatus string
	// CardTimeout is the configured provider call timeout; zero means 10 seconds.
	CardTimeout time.Duration
	// CardKinds are the kinds that get new cards. Nil means every kind; a non-nil slice, even an empty one, is exactly those kinds.
	CardKinds []domain.Kind
	// StoreID is the configured store identifier, already normalized; empty means none is configured.
	StoreID string
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
	claimStale := opts.CardClaimStale
	if claimStale <= 0 {
		claimStale = 30 * time.Second
	}
	cardTimeout := opts.CardTimeout
	if cardTimeout <= 0 {
		cardTimeout = 10 * time.Second
	}
	return &Service{store: st, index: ix, log: log, stallThreshold: opts.StallThreshold, searchLimit: limit,
		loopLockPath: opts.LoopLockPath, taskManager: opts.TaskManager, cardClaimStale: claimStale, cardTimeout: cardTimeout, cardCanceledStatus: opts.CardCanceledStatus,
		cardKinds: opts.CardKinds, storeID: opts.StoreID, storeIDCache: &storeIDCache{}}
}

// SetPullRequestLink sets how views build a pull request's web URL. It must
// not be called while the service is in use.
func (s *Service) SetPullRequestLink(fn func(provider, namespace, repository string, number int) (string, bool)) {
	s.pullRequestLink = fn
}

// Store exposes the underlying store for read-only orchestration such as
// loop planning.
func (s *Service) Store() store.Store { return s.store }

// InTx runs fn with a Service bound to one store transaction: if fn returns
// an error, none of its writes persist. fn must use only the Service it is
// given; the store may allow a single connection, so calls through s would
// wait on the transaction.
func (s *Service) InTx(ctx context.Context, fn func(tx *Service) error) error {
	return s.store.InTx(ctx, func(st store.Store) error {
		inner := *s
		inner.store = st
		inner.inTx = true
		if s.index != nil {
			ix := *s.index
			ix.Store = st
			inner.index = &ix
		}
		return fn(&inner)
	})
}

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

// initiativeArchived reports whether an initiative is archived.
func initiativeArchived(ctx context.Context, st store.Store, id int64) (bool, error) {
	i, err := st.GetInitiative(ctx, id)
	if err != nil {
		return false, err
	}
	return !i.ArchivedAt.IsZero(), nil
}

// milestoneArchived reports whether a milestone's initiative is archived.
func milestoneArchived(ctx context.Context, st store.Store, m domain.Milestone) (bool, error) {
	return initiativeArchived(ctx, st, m.InitiativeID)
}

// taskArchived reports whether a task's initiative is archived.
func taskArchived(ctx context.Context, st store.Store, t domain.Task) (bool, error) {
	m, err := st.GetMilestone(ctx, t.MilestoneID)
	if err != nil {
		return false, err
	}
	return milestoneArchived(ctx, st, m)
}

// archivedInitiatives maps every archived initiative's ID to true, in one query.
func archivedInitiatives(ctx context.Context, st store.Store) (map[int64]bool, error) {
	all, err := st.ListInitiatives(ctx, false)
	if err != nil {
		return nil, err
	}
	out := map[int64]bool{}
	for _, i := range all {
		if !i.ArchivedAt.IsZero() {
			out[i.ID] = true
		}
	}
	return out, nil
}

// ErrArchived marks a write refused because the target is effectively
// archived; unarchive it first.
var ErrArchived = errors.New("archived")

type archivedError struct{ msg string }

func (e *archivedError) Error() string        { return e.msg }
func (e *archivedError) Is(target error) bool { return target == ErrArchived }

// requireWritable refuses with ErrArchived when the initiative (KindInitiative)
// or document (KindDocument) with this ID is effectively archived. A document
// is archived by its own flag or by its initiative's.
func requireWritable(ctx context.Context, st store.Store, kind domain.Kind, id int64) error {
	switch kind {
	case domain.KindInitiative:
		i, err := st.GetInitiative(ctx, id)
		if err != nil {
			return err
		}
		if !i.ArchivedAt.IsZero() {
			return initiativeArchivedError(id)
		}
		return nil
	case domain.KindDocument:
		d, err := st.GetDocument(ctx, id)
		if err != nil {
			return err
		}
		if !d.ArchivedAt.IsZero() {
			return documentArchivedError(d)
		}
		if d.InitiativeID != nil {
			return requireWritable(ctx, st, domain.KindInitiative, *d.InitiativeID)
		}
		return nil
	}
	return fmt.Errorf("%w: %s cannot be archived", domain.ErrInvalid, kind)
}

func initiativeArchivedError(id int64) error {
	what := domain.FormatID(domain.KindInitiative, id)
	return &archivedError{fmt.Sprintf("%s is archived; unarchive it first (ralph initiative unarchive %s)", what, what)}
}

// requireTaskWritable refuses with ErrArchived when the task's initiative is archived.
func requireTaskWritable(ctx context.Context, st store.Store, t domain.Task) error {
	m, err := st.GetMilestone(ctx, t.MilestoneID)
	if err != nil {
		return err
	}
	return requireWritable(ctx, st, domain.KindInitiative, m.InitiativeID)
}

// CheckMilestoneWritable returns an error wrapping ErrArchived when the
// milestone's initiative is archived, so callers can fail before a multi-step write.
func (s *Service) CheckMilestoneWritable(ctx context.Context, id int64) error {
	m, err := s.store.GetMilestone(ctx, id)
	if err != nil {
		return err
	}
	return requireWritable(ctx, s.store, domain.KindInitiative, m.InitiativeID)
}

// CheckInitiativeWritable returns an error wrapping ErrArchived when the
// initiative is archived, and not found when it does not exist.
func (s *Service) CheckInitiativeWritable(ctx context.Context, id int64) error {
	return requireWritable(ctx, s.store, domain.KindInitiative, id)
}

// ---- initiatives ----

type InitiativeInput struct {
	Title string
	Slug  string
	Brief string
	// ParentCard is a Jira issue key; empty means none.
	ParentCard string
	// Sprint is the name of the sprint milestone cards are added to; empty means none.
	Sprint string
}

type InitiativePatch struct {
	Title *string
	Brief *string
	// ParentCard nil leaves the parent unchanged; a pointer to an empty string clears it.
	ParentCard *string
	// Sprint nil leaves the sprint unchanged; a pointer to an empty string clears it.
	Sprint *string
}

var parentCardKey = regexp.MustCompile(`^[A-Z][A-Z0-9_]*-[0-9]+$`)

func normalizeParentCard(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s != "" && !parentCardKey.MatchString(s) {
		return "", fmt.Errorf("%w: parent_card %q is not a Jira key such as PROJ-123", domain.ErrInvalid, s)
	}
	return s, nil
}

const maxSprintRunes = 255

func normalizeSprint(s string) (string, error) {
	s = strings.TrimSpace(s)
	if utf8.RuneCountInString(s) > maxSprintRunes {
		return "", fmt.Errorf("%w: sprint is longer than %d characters", domain.ErrInvalid, maxSprintRunes)
	}
	for _, r := range s {
		if unicode.IsControl(r) {
			return "", fmt.Errorf("%w: sprint must not contain control characters", domain.ErrInvalid)
		}
	}
	return s, nil
}

func (s *Service) CreateInitiative(ctx context.Context, in InitiativeInput) (InitiativeView, error) {
	if err := domain.RequireText("title", in.Title); err != nil {
		return InitiativeView{}, err
	}
	parent, err := normalizeParentCard(in.ParentCard)
	if err != nil {
		return InitiativeView{}, err
	}
	sprint, err := normalizeSprint(in.Sprint)
	if err != nil {
		return InitiativeView{}, err
	}
	i := domain.Initiative{Title: strings.TrimSpace(in.Title), Brief: strings.TrimSpace(in.Brief), ParentCard: parent, Sprint: sprint}
	err = createWithSlug(in.Slug, i.Title, "initiative", func(slug string) error {
		i.Slug = slug
		return s.store.CreateInitiative(ctx, &i)
	})
	if err != nil {
		return InitiativeView{}, err
	}
	v := initiativeView(i)
	v.IndexWarning = s.indexInitiative(ctx, i)
	v.CardWarning = s.createCard(ctx, domain.KindInitiative, i.ID, s.cardClaimStale)
	return v, nil
}

func (s *Service) GetInitiative(ctx context.Context, id int64) (InitiativeView, error) {
	i, err := s.store.GetInitiative(ctx, id)
	if err != nil {
		return InitiativeView{}, err
	}
	cards, err := s.loadCards(ctx)
	if err != nil {
		return InitiativeView{}, err
	}
	v := initiativeView(i)
	v.Card = cards.view(ctx, domain.KindInitiative, i.ID, cardID{})
	return v, nil
}

// ListOptions widens list and query operations. Archived items are hidden
// unless IncludeArchived is set.
type ListOptions struct {
	IncludeArchived bool
}

func (s *Service) ListInitiatives(ctx context.Context, opts ListOptions) ([]InitiativeView, error) {
	all, err := s.store.ListInitiatives(ctx, !opts.IncludeArchived)
	if err != nil {
		return nil, err
	}
	cards, err := s.loadCards(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]InitiativeView, 0, len(all))
	for _, i := range all {
		v := initiativeView(i)
		v.Card = cards.view(ctx, domain.KindInitiative, i.ID, cardID{})
		out = append(out, v)
	}
	return out, nil
}

func (s *Service) UpdateInitiative(ctx context.Context, id int64, p InitiativePatch) (InitiativeView, error) {
	i, err := s.store.GetInitiative(ctx, id)
	if err != nil {
		return InitiativeView{}, err
	}
	if err := requireWritable(ctx, s.store, domain.KindInitiative, id); err != nil {
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
	if p.ParentCard != nil {
		parent, err := normalizeParentCard(*p.ParentCard)
		if err != nil {
			return InitiativeView{}, err
		}
		i.ParentCard = parent
	}
	if p.Sprint != nil {
		sprint, err := normalizeSprint(*p.Sprint)
		if err != nil {
			return InitiativeView{}, err
		}
		i.Sprint = sprint
	}
	if err := s.store.UpdateInitiative(ctx, &i); err != nil {
		return InitiativeView{}, err
	}
	v := initiativeView(i)
	v.IndexWarning = s.indexInitiative(ctx, i)
	_, v.CardWarning, _ = s.syncCard(ctx, domain.KindInitiative, i.ID, modeExisting, s.cardClaimStale)
	return v, nil
}

// ---- milestones ----

type MilestoneInput struct {
	InitiativeID int64
	Title        string
	Slug         string
	Description  string
	Position     int
	DependsOn    []int64
}

// MilestonePatch changes the fields that are set. A nil DependsOn leaves the dependencies unchanged; an empty
// non-nil one clears them.
type MilestonePatch struct {
	Title       *string
	Description *string
	Position    *int
	DependsOn   *[]int64
}

func (s *Service) CreateMilestone(ctx context.Context, in MilestoneInput) (MilestoneView, error) {
	if err := domain.RequireText("title", in.Title); err != nil {
		return MilestoneView{}, err
	}
	if in.Position < 0 {
		return MilestoneView{}, fmt.Errorf("%w: position must not be negative", domain.ErrInvalid)
	}
	if err := requireWritable(ctx, s.store, domain.KindInitiative, in.InitiativeID); err != nil {
		return MilestoneView{}, err
	}
	m := domain.Milestone{
		InitiativeID: in.InitiativeID,
		Title:        strings.TrimSpace(in.Title),
		Description:  strings.TrimSpace(in.Description),
		Position:     in.Position,
	}
	err := s.InTx(ctx, func(tx *Service) error {
		err := createWithSlug(in.Slug, m.Title, "milestone", func(slug string) error {
			m.Slug = slug
			m.Position = in.Position
			return tx.store.CreateMilestone(ctx, &m)
		})
		if err != nil {
			return err
		}
		return tx.setDependencies(ctx, m.ID, in.DependsOn)
	})
	if err != nil {
		return MilestoneView{}, err
	}
	v := s.milestoneView(m, nil, false)
	v.DependsOn = formatDependsOn(in.DependsOn)
	v.IndexWarning = s.indexMilestone(ctx, m)
	v.CardWarning = s.createCard(ctx, domain.KindMilestone, m.ID, s.cardClaimStale)
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
	archived, err := milestoneArchived(ctx, s.store, m)
	if err != nil {
		return MilestoneView{}, err
	}
	cards, err := s.loadCards(ctx)
	if err != nil {
		return MilestoneView{}, err
	}
	deps, err := s.store.ListMilestoneDependencies(ctx, m.InitiativeID)
	if err != nil {
		return MilestoneView{}, err
	}
	v := s.milestoneView(m, tasks, archived)
	v.DependsOn = formatDependsOn(deps[m.ID])
	v.Card = cards.view(ctx, domain.KindMilestone, m.ID, cardID{domain.KindInitiative, m.InitiativeID})
	return v, nil
}

func (s *Service) ListMilestones(ctx context.Context, initiativeID int64, opts ListOptions) ([]MilestoneView, error) {
	cards, err := s.loadCards(ctx)
	if err != nil {
		return nil, err
	}
	return s.listMilestones(ctx, initiativeID, opts, cards)
}

func (s *Service) listMilestones(ctx context.Context, initiativeID int64, opts ListOptions, cards *cardIndex) ([]MilestoneView, error) {
	if initiativeID != 0 {
		if _, err := s.store.GetInitiative(ctx, initiativeID); err != nil {
			return nil, err
		}
	}
	ms, err := s.store.ListMilestones(ctx, initiativeID, !opts.IncludeArchived)
	if err != nil {
		return nil, err
	}
	tasks, err := s.store.ListTasks(ctx, store.TaskFilter{InitiativeID: initiativeID, ExcludeArchived: !opts.IncludeArchived})
	if err != nil {
		return nil, err
	}
	byMilestone := map[int64][]domain.Task{}
	for _, t := range tasks {
		byMilestone[t.MilestoneID] = append(byMilestone[t.MilestoneID], t)
	}
	archived, err := archivedInitiatives(ctx, s.store)
	if err != nil {
		return nil, err
	}
	deps, err := s.store.ListMilestoneDependencies(ctx, initiativeID)
	if err != nil {
		return nil, err
	}
	out := make([]MilestoneView, 0, len(ms))
	for _, m := range ms {
		v := s.milestoneView(m, byMilestone[m.ID], archived[m.InitiativeID])
		v.DependsOn = formatDependsOn(deps[m.ID])
		v.Card = cards.view(ctx, domain.KindMilestone, m.ID, cardID{domain.KindInitiative, m.InitiativeID})
		out = append(out, v)
	}
	return out, nil
}

func (s *Service) UpdateMilestone(ctx context.Context, id int64, p MilestonePatch) (MilestoneView, error) {
	m, err := s.store.GetMilestone(ctx, id)
	if err != nil {
		return MilestoneView{}, err
	}
	if err := requireWritable(ctx, s.store, domain.KindInitiative, m.InitiativeID); err != nil {
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
	err = s.InTx(ctx, func(tx *Service) error {
		if p.DependsOn != nil {
			if err := tx.setDependencies(ctx, id, *p.DependsOn); err != nil {
				return err
			}
		}
		return tx.store.UpdateMilestone(ctx, &m)
	})
	if err != nil {
		return MilestoneView{}, err
	}
	tasks, err := s.store.ListTasks(ctx, store.TaskFilter{MilestoneID: id})
	if err != nil {
		return MilestoneView{}, err
	}
	archived, err := milestoneArchived(ctx, s.store, m)
	if err != nil {
		return MilestoneView{}, err
	}
	deps, err := s.store.ListMilestoneDependencies(ctx, m.InitiativeID)
	if err != nil {
		return MilestoneView{}, err
	}
	v := s.milestoneView(m, tasks, archived)
	v.DependsOn = formatDependsOn(deps[m.ID])
	v.IndexWarning = s.indexMilestone(ctx, m)
	_, v.CardWarning, _ = s.syncCard(ctx, domain.KindMilestone, m.ID, modeExisting, s.cardClaimStale)
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
	return s.store.InTx(ctx, func(tx store.Store) error {
		m, err := tx.GetMilestone(ctx, milestoneID)
		if err != nil {
			return err
		}
		if err := requireWritable(ctx, tx, domain.KindInitiative, m.InitiativeID); err != nil {
			return err
		}
		return tx.SetMilestoneGit(ctx, milestoneID, g)
	})
}

// RecordMilestonePullRequest replaces a milestone's recorded pull request. Like
// RecordMilestoneGit it neither reindexes nor changes updated_at.
func (s *Service) RecordMilestonePullRequest(ctx context.Context, milestoneID int64, rec domain.PullRequestRecord) error {
	rec.Provider = strings.TrimSpace(rec.Provider)
	rec.Namespace = strings.TrimSpace(rec.Namespace)
	rec.Repository = strings.TrimSpace(rec.Repository)
	for name, v := range map[string]string{"provider": rec.Provider, "namespace": rec.Namespace, "repository": rec.Repository} {
		if v == "" {
			return fmt.Errorf("%w: pull request %s must not be empty", domain.ErrInvalid, name)
		}
	}
	if rec.Number < 1 {
		return fmt.Errorf("%w: pull request number must be at least 1", domain.ErrInvalid)
	}
	return s.store.InTx(ctx, func(tx store.Store) error {
		m, err := tx.GetMilestone(ctx, milestoneID)
		if err != nil {
			return err
		}
		if err := requireWritable(ctx, tx, domain.KindInitiative, m.InitiativeID); err != nil {
			return err
		}
		return tx.SetMilestonePullRequest(ctx, milestoneID, rec)
	})
}

// ClearMilestonePullRequest removes a milestone's recorded pull request and
// returns it; the record is zero when none was recorded.
func (s *Service) ClearMilestonePullRequest(ctx context.Context, milestoneID int64) (domain.PullRequestRecord, error) {
	var removed domain.PullRequestRecord
	err := s.store.InTx(ctx, func(tx store.Store) error {
		m, err := tx.GetMilestone(ctx, milestoneID)
		if err != nil {
			return err
		}
		if err := requireWritable(ctx, tx, domain.KindInitiative, m.InitiativeID); err != nil {
			return err
		}
		removed = m.PullRequest
		if removed.IsZero() {
			return nil
		}
		return tx.SetMilestonePullRequest(ctx, milestoneID, domain.PullRequestRecord{})
	})
	if err != nil {
		return domain.PullRequestRecord{}, err
	}
	return removed, nil
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
		if err := requireWritable(ctx, tx, domain.KindInitiative, m.InitiativeID); err != nil {
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
	return s.store.InTx(ctx, func(tx store.Store) error {
		t, err := tx.GetTask(ctx, taskID)
		if err != nil {
			return err
		}
		if err := requireTaskWritable(ctx, tx, t); err != nil {
			return err
		}
		return tx.SetTaskCommit(ctx, taskID, hash)
	})
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
	// IncludeArchived adds tasks of archived initiatives.
	IncludeArchived bool
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
	if err := requireWritable(ctx, s.store, domain.KindInitiative, m.InitiativeID); err != nil {
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
	v := taskView(t, nil, false)
	v.IndexWarning = s.indexTask(ctx, t, m.InitiativeID)
	v.CardWarning = s.createCard(ctx, domain.KindTask, t.ID, s.cardClaimStale)
	return v, nil
}

// ImportTaskInput describes an existing task to record in a given status.
type ImportTaskInput struct {
	MilestoneID        int64
	Title              string
	Description        string
	AcceptanceCriteria string
	Status             domain.Status
	Reason             string
	Source             string
	Position           int
}

// ImportTask creates a task directly in an import-allowed status, bypassing
// the PROPOSED start of CreateTask. IN_PROGRESS is refused because the loop
// resumes those tasks first.
func (s *Service) ImportTask(ctx context.Context, in ImportTaskInput) (TaskView, error) {
	title := strings.TrimSpace(in.Title)
	desc := strings.TrimSpace(in.Description)
	criteria := strings.TrimSpace(in.AcceptanceCriteria)
	reason := strings.TrimSpace(in.Reason)
	source := strings.TrimSpace(in.Source)
	if err := domain.RequireText("title", title); err != nil {
		return TaskView{}, err
	}
	if err := domain.RequireText("reason", reason); err != nil {
		return TaskView{}, err
	}
	switch in.Status {
	case domain.StatusProposed, domain.StatusTodo, domain.StatusBlocked, domain.StatusDone, domain.StatusCancelled:
	case domain.StatusInProgress:
		return TaskView{}, fmt.Errorf("%w: an imported task cannot be IN_PROGRESS because the loop resumes those first; import it as TODO or PROPOSED", domain.ErrInvalid)
	default:
		return TaskView{}, fmt.Errorf("%w: cannot import a task as %q (expected PROPOSED, TODO, BLOCKED, DONE, or CANCELLED)", domain.ErrInvalid, in.Status)
	}
	if in.Status == domain.StatusTodo {
		var missing []string
		if desc == "" {
			missing = append(missing, "description")
		}
		if criteria == "" {
			missing = append(missing, "acceptance criteria")
		}
		if len(missing) > 0 {
			return TaskView{}, fmt.Errorf("%w: a task imported as TODO needs %s; import it as PROPOSED otherwise",
				domain.ErrInvalid, strings.Join(missing, " and "))
		}
	}
	if in.Position < 0 {
		return TaskView{}, fmt.Errorf("%w: position must not be negative", domain.ErrInvalid)
	}

	body := fmt.Sprintf("Imported as %s: %s", in.Status, reason)
	if source != "" {
		body += fmt.Sprintf(" (source: %s)", source)
	}
	var (
		out          TaskView
		task         domain.Task
		initiativeID int64
	)
	err := s.store.InTx(ctx, func(tx store.Store) error {
		m, err := tx.GetMilestone(ctx, in.MilestoneID)
		if err != nil {
			return err
		}
		if err := requireWritable(ctx, tx, domain.KindInitiative, m.InitiativeID); err != nil {
			return err
		}
		task = domain.Task{
			MilestoneID:        m.ID,
			Title:              title,
			Description:        desc,
			AcceptanceCriteria: criteria,
			Status:             in.Status,
			Position:           in.Position,
		}
		if in.Status == domain.StatusBlocked {
			task.BlockedReason = reason
		}
		if err := tx.CreateTask(ctx, &task); err != nil {
			return err
		}
		note := domain.TaskNote{TaskID: task.ID, Kind: domain.NoteGeneral, Body: body}
		if err := tx.AddTaskNote(ctx, &note); err != nil {
			return err
		}
		notes, err := tx.ListTaskNotes(ctx, task.ID)
		if err != nil {
			return err
		}
		initiativeID = m.InitiativeID
		out = taskView(task, notes, false)
		return nil
	})
	if err != nil {
		return TaskView{}, err
	}
	out.IndexWarning = s.indexTask(ctx, task, initiativeID)
	out.CardWarning = s.createCard(ctx, domain.KindTask, task.ID, s.cardClaimStale)
	return out, nil
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
	archived, err := taskArchived(ctx, s.store, t)
	if err != nil {
		return TaskView{}, err
	}
	cards, err := s.loadCards(ctx)
	if err != nil {
		return TaskView{}, err
	}
	v := taskView(t, notes, archived)
	v.Card = cards.view(ctx, domain.KindTask, t.ID, cardID{domain.KindMilestone, t.MilestoneID})
	return v, nil
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
	tasks, err := s.store.ListTasks(ctx, store.TaskFilter{
		InitiativeID: f.InitiativeID, MilestoneID: f.MilestoneID, Statuses: f.Statuses, ExcludeArchived: !f.IncludeArchived,
	})
	if err != nil {
		return nil, err
	}
	ms, err := s.store.ListMilestones(ctx, 0, false)
	if err != nil {
		return nil, err
	}
	archivedInit, err := archivedInitiatives(ctx, s.store)
	if err != nil {
		return nil, err
	}
	archivedMilestone := make(map[int64]bool, len(ms))
	for _, m := range ms {
		archivedMilestone[m.ID] = archivedInit[m.InitiativeID]
	}
	cards, err := s.loadCards(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]TaskView, 0, len(tasks))
	for _, t := range tasks {
		v := taskView(t, nil, archivedMilestone[t.MilestoneID])
		v.Card = cards.view(ctx, domain.KindTask, t.ID, cardID{domain.KindMilestone, t.MilestoneID})
		out = append(out, v)
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
	if err := requireTaskWritable(ctx, s.store, t); err != nil {
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
	archived, err := milestoneArchived(ctx, s.store, m)
	if err != nil {
		return TaskView{}, err
	}
	v := taskView(t, nil, archived)
	v.IndexWarning = s.indexTask(ctx, t, m.InitiativeID)
	_, v.CardWarning, _ = s.syncCard(ctx, domain.KindTask, t.ID, modeExisting, s.cardClaimStale)
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
		if err := requireTaskWritable(ctx, tx, t); err != nil {
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
		archived, err := taskArchived(ctx, tx, t)
		if err != nil {
			return err
		}
		out = taskView(t, notes, archived)
		return nil
	})
	if err != nil {
		return out, err
	}
	out.CardWarning, out.CardConflict = s.syncTaskCards(ctx, id)
	return out, nil
}

// syncTaskCards pushes a task's card and then its milestone's, returning the
// first warning and the first assignee conflict, the task's card first.
func (s *Service) syncTaskCards(ctx context.Context, id int64) (string, *CardConflictView) {
	if s.taskManager == nil || s.inTx {
		return "", nil
	}
	_, warn, conflict := s.syncCard(ctx, domain.KindTask, id, modeExisting, s.cardClaimStale)
	t, err := s.store.GetTask(ctx, id)
	if err != nil {
		s.log.Error("loading task for milestone card", "id", domain.FormatID(domain.KindTask, id), "error", err)
		return warn, conflict
	}
	_, w, c := s.syncCard(ctx, domain.KindMilestone, t.MilestoneID, modeExisting, s.cardClaimStale)
	if warn == "" {
		warn = w
	}
	if conflict == nil {
		conflict = c
	}
	return warn, conflict
}

// ReopenTask sends a DONE task whose work was lost back to TODO. It is
// reconcile-only: the transition table keeps DONE terminal.
func (s *Service) ReopenTask(ctx context.Context, id int64, reason string) (TaskView, error) {
	if err := domain.RequireText("reason", reason); err != nil {
		return TaskView{}, err
	}
	reason = strings.TrimSpace(reason)
	var out TaskView
	err := s.store.InTx(ctx, func(tx store.Store) error {
		t, err := tx.GetTask(ctx, id)
		if err != nil {
			return err
		}
		if err := requireTaskWritable(ctx, tx, t); err != nil {
			return err
		}
		if t.Status != domain.StatusDone {
			return fmt.Errorf("%w: only a DONE task can be reopened; %s is %s",
				domain.ErrInvalid, domain.FormatID(domain.KindTask, id), t.Status)
		}
		t.Status = domain.StatusTodo
		t.FailureCount = 0
		t.BlockedReason = ""
		if err := tx.UpdateTask(ctx, &t); err != nil {
			return err
		}
		if err := tx.SetTaskCommit(ctx, id, ""); err != nil {
			return err
		}
		t.Commit = ""
		note := domain.TaskNote{TaskID: id, Kind: domain.NoteGeneral, Body: "Reopened by reconcile: " + reason}
		if err := tx.AddTaskNote(ctx, &note); err != nil {
			return err
		}
		notes, err := tx.ListTaskNotes(ctx, id)
		if err != nil {
			return err
		}
		archived, err := taskArchived(ctx, tx, t)
		if err != nil {
			return err
		}
		out = taskView(t, notes, archived)
		return nil
	})
	if err != nil {
		return out, err
	}
	out.CardWarning, _ = s.syncTaskCards(ctx, id)
	return out, nil
}

// AddTaskNote appends a note to a task.
func (s *Service) AddTaskNote(ctx context.Context, id int64, kind domain.NoteKind, body string) (TaskView, error) {
	if err := domain.RequireText("note", body); err != nil {
		return TaskView{}, err
	}
	err := s.store.InTx(ctx, func(tx store.Store) error {
		t, err := tx.GetTask(ctx, id)
		if err != nil {
			return err
		}
		if err := requireTaskWritable(ctx, tx, t); err != nil {
			return err
		}
		return tx.AddTaskNote(ctx, &domain.TaskNote{TaskID: id, Kind: kind, Body: strings.TrimSpace(body)})
	})
	if err != nil {
		return TaskView{}, err
	}
	return s.GetTask(ctx, id)
}

// NextTask returns the task the loop should work on in a milestone: an
// interrupted IN_PROGRESS task first, otherwise the first TODO by position.
// It returns nil when nothing is loopable, and an ErrArchived error when the
// milestone's initiative is archived.
func (s *Service) NextTask(ctx context.Context, milestoneID int64) (*TaskView, error) {
	if err := s.CheckMilestoneWritable(ctx, milestoneID); err != nil {
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
	// IncludeArchived adds archived documents and those of archived initiatives.
	IncludeArchived bool
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
		if err := requireWritable(ctx, s.store, domain.KindInitiative, in.InitiativeID); err != nil {
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
	v := documentView(d, true, false)
	v.IndexWarning = s.indexDocument(ctx, d)
	return v, nil
}

// documentInitiativeArchived reports whether the document's initiative, if any, is archived.
func documentInitiativeArchived(ctx context.Context, st store.Store, d domain.Document) (bool, error) {
	if d.InitiativeID == nil {
		return false, nil
	}
	return initiativeArchived(ctx, st, *d.InitiativeID)
}

func (s *Service) GetDocument(ctx context.Context, id int64) (DocumentView, error) {
	d, err := s.store.GetDocument(ctx, id)
	if err != nil {
		return DocumentView{}, err
	}
	archived, err := documentInitiativeArchived(ctx, s.store, d)
	if err != nil {
		return DocumentView{}, err
	}
	return documentView(d, true, archived), nil
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
	docs, err := s.store.ListDocuments(ctx, store.DocumentFilter{
		InitiativeID: f.InitiativeID, ProjectOnly: f.ProjectOnly, Kind: f.Kind, ExcludeArchived: !f.IncludeArchived,
	})
	if err != nil {
		return nil, err
	}
	archived, err := archivedInitiatives(ctx, s.store)
	if err != nil {
		return nil, err
	}
	out := make([]DocumentView, 0, len(docs))
	for _, d := range docs {
		out = append(out, documentView(d, false, d.InitiativeID != nil && archived[*d.InitiativeID]))
	}
	return out, nil
}

func (s *Service) UpdateDocument(ctx context.Context, id int64, p DocumentPatch) (DocumentView, error) {
	d, err := s.store.GetDocument(ctx, id)
	if err != nil {
		return DocumentView{}, err
	}
	if err := requireWritable(ctx, s.store, domain.KindDocument, id); err != nil {
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
	archived, err := documentInitiativeArchived(ctx, s.store, d)
	if err != nil {
		return DocumentView{}, err
	}
	v := documentView(d, true, archived)
	v.IndexWarning = s.indexDocument(ctx, d)
	return v, nil
}

// documentArchivedError refuses a document that is itself archived.
func documentArchivedError(d domain.Document) error {
	what := domain.FormatID(domain.KindDocument, d.ID)
	return &archivedError{fmt.Sprintf("%s is archived; unarchive it first (ralph doc unarchive %s)", what, what)}
}

// requireFreeDocumentSlug refuses with ErrConflict when a document in the
// destination scope (initiative, or project-wide when nil) already has the slug.
func requireFreeDocumentSlug(ctx context.Context, st store.Store, d domain.Document, initiativeID *int64, slug string) error {
	f := store.DocumentFilter{ProjectOnly: initiativeID == nil}
	if initiativeID != nil {
		f.InitiativeID = *initiativeID
	}
	docs, err := st.ListDocuments(ctx, f)
	if err != nil {
		return err
	}
	for _, other := range docs {
		if other.Slug == slug && other.ID != d.ID {
			return fmt.Errorf("%w: %s already uses the slug %q there; retry with a new slug (doc_promote / doc_return with slug, or --slug)",
				store.ErrConflict, domain.FormatID(domain.KindDocument, other.ID), slug)
		}
	}
	return nil
}

// PromoteDocument makes an initiative's document project-wide, remembering the
// initiative as its origin. A non-empty slug replaces the document's slug.
func (s *Service) PromoteDocument(ctx context.Context, id int64, slug string) (DocumentView, error) {
	return s.moveDocument(ctx, id, slug, true)
}

// ReturnDocument moves a promoted document back to its origin initiative. A
// non-empty slug replaces the document's slug.
func (s *Service) ReturnDocument(ctx context.Context, id int64, slug string) (DocumentView, error) {
	return s.moveDocument(ctx, id, slug, false)
}

func (s *Service) moveDocument(ctx context.Context, id int64, slug string, promote bool) (DocumentView, error) {
	what := domain.FormatID(domain.KindDocument, id)
	var out DocumentView
	var moved domain.Document
	err := s.InTx(ctx, func(tx *Service) error {
		d, err := tx.store.GetDocument(ctx, id)
		if err != nil {
			return err
		}
		if slug != "" {
			if err := domain.ValidateSlug(slug); err != nil {
				return err
			}
		}
		var dest, origin *int64
		if promote {
			if d.InitiativeID == nil {
				return fmt.Errorf("%w: %s is already project-wide", domain.ErrInvalid, what)
			}
			origin = d.InitiativeID
		} else {
			if d.OriginInitiativeID == nil {
				return fmt.Errorf("%w: %s has no origin initiative", domain.ErrInvalid, what)
			}
			if err := requireWritable(ctx, tx.store, domain.KindInitiative, *d.OriginInitiativeID); err != nil {
				return err
			}
			dest = d.OriginInitiativeID
		}
		if !d.ArchivedAt.IsZero() {
			return documentArchivedError(d)
		}
		use := slug
		if use == "" {
			use = d.Slug
		}
		if err := requireFreeDocumentSlug(ctx, tx.store, d, dest, use); err != nil {
			return err
		}
		if err := tx.store.SetDocumentScope(ctx, id, dest, origin, slug); err != nil {
			return err
		}
		if moved, err = tx.store.GetDocument(ctx, id); err != nil {
			return err
		}
		archived, err := documentInitiativeArchived(ctx, tx.store, moved)
		if err != nil {
			return err
		}
		out = documentView(moved, true, archived)
		return nil
	})
	if err != nil {
		return DocumentView{}, err
	}
	out.IndexWarning = s.indexDocument(ctx, moved)
	return out, nil
}

// ---- archive ----

// ArchiveInitiative archives an initiative, making it and everything under it
// read-only. It is idempotent and refused while a loop holds one of its milestones.
func (s *Service) ArchiveInitiative(ctx context.Context, id int64, choice DocumentChoice) (InitiativeView, error) {
	return s.setInitiativeArchived(ctx, id, true, choice)
}

// UnarchiveInitiative reverses ArchiveInitiative, under the same loop guard.
func (s *Service) UnarchiveInitiative(ctx context.Context, id int64) (InitiativeView, error) {
	return s.setInitiativeArchived(ctx, id, false, DocumentChoice{})
}

func (s *Service) setInitiativeArchived(ctx context.Context, id int64, archive bool, choice DocumentChoice) (InitiativeView, error) {
	var out InitiativeView
	err := s.InTx(ctx, func(tx *Service) error {
		i, err := tx.store.GetInitiative(ctx, id)
		if err != nil {
			return err
		}
		if archive != i.ArchivedAt.IsZero() {
			out = initiativeView(i)
			return nil
		}
		ms, err := tx.store.ListMilestones(ctx, id, false)
		if err != nil {
			return err
		}
		ids := make([]int64, len(ms))
		for n, m := range ms {
			ids[n] = m.ID
		}
		action := "unarchive"
		var at time.Time
		if archive {
			action, at = "archive", time.Now().UTC()
		}
		if err := tx.guardLoop(action, domain.FormatID(domain.KindInitiative, id), ids); err != nil {
			return err
		}
		var promoted []string
		if archive {
			if promoted, err = tx.promoteForArchive(ctx, i, choice); err != nil {
				return err
			}
		}
		if err := tx.store.SetInitiativeArchived(ctx, id, at); err != nil {
			return err
		}
		if i, err = tx.store.GetInitiative(ctx, id); err != nil {
			return err
		}
		out = initiativeView(i)
		out.PromotedDocuments = promoted
		return nil
	})
	return out, err
}

// promoteForArchive validates the choice against the initiative's candidate
// documents and promotes the chosen ones, returning their IDs.
func (s *Service) promoteForArchive(ctx context.Context, i domain.Initiative, choice DocumentChoice) ([]string, error) {
	docs, err := s.store.ListDocuments(ctx, store.DocumentFilter{InitiativeID: i.ID, ExcludeArchived: true})
	if err != nil {
		return nil, err
	}
	var candidates []domain.Document
	for _, d := range docs {
		if d.InitiativeID != nil && *d.InitiativeID == i.ID && d.ArchivedAt.IsZero() {
			candidates = append(candidates, d)
		}
	}
	if len(candidates) > 0 && !choice.chosen() {
		return nil, newDocumentChoiceError(i, candidates)
	}
	byID := make(map[int64]domain.Document, len(candidates))
	for _, d := range candidates {
		byID[d.ID] = d
	}
	var pick []domain.Document
	switch {
	case choice.All:
		pick = candidates
	case len(choice.IDs) > 0:
		seen := map[int64]bool{}
		for _, id := range choice.IDs {
			d, ok := byID[id]
			if !ok {
				return nil, fmt.Errorf("%w: %s is not an initiative-scoped, unarchived document of %s",
					domain.ErrInvalid, domain.FormatID(domain.KindDocument, id), domain.FormatID(domain.KindInitiative, i.ID))
			}
			if !seen[id] {
				seen[id] = true
				pick = append(pick, d)
			}
		}
	}
	var promoted []string
	for _, d := range pick {
		what := domain.FormatID(domain.KindDocument, d.ID)
		if err := requireFreeDocumentSlug(ctx, s.store, d, nil, d.Slug); err != nil {
			if !errors.Is(err, store.ErrConflict) {
				return nil, err
			}
			return nil, fmt.Errorf("%w: cannot promote %s: another project-wide document already uses the slug %q; promote %s on its own with a new slug, then archive again",
				store.ErrConflict, what, d.Slug, what)
		}
		if _, err := s.PromoteDocument(ctx, d.ID, ""); err != nil {
			return nil, err
		}
		promoted = append(promoted, what)
	}
	return promoted, nil
}

// ArchiveDocument archives a document. It is idempotent.
func (s *Service) ArchiveDocument(ctx context.Context, id int64) (DocumentView, error) {
	return s.setDocumentArchived(ctx, id, true)
}

// UnarchiveDocument clears a document's own archive flag. A document in an
// archived initiative stays effectively archived, and its view says so.
func (s *Service) UnarchiveDocument(ctx context.Context, id int64) (DocumentView, error) {
	return s.setDocumentArchived(ctx, id, false)
}

func (s *Service) setDocumentArchived(ctx context.Context, id int64, archive bool) (DocumentView, error) {
	var out DocumentView
	err := s.InTx(ctx, func(tx *Service) error {
		d, err := tx.store.GetDocument(ctx, id)
		if err != nil {
			return err
		}
		if archive == d.ArchivedAt.IsZero() {
			var at time.Time
			if archive {
				at = time.Now().UTC()
			}
			if err := tx.store.SetDocumentArchived(ctx, id, at); err != nil {
				return err
			}
		}
		out, err = tx.GetDocument(ctx, id)
		return err
	})
	return out, err
}
