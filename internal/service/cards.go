package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/bengearig/ralph/internal/domain"
	"github.com/bengearig/ralph/internal/store"
	"github.com/bengearig/ralph/internal/taskmanager"
)

// CardView is a card record on the wire. Key is empty while creation is failing.
type CardView struct {
	Kind        string `json:"kind"`
	ID          string `json:"id"`
	Provider    string `json:"provider"`
	Key         string `json:"key"`
	URL         string `json:"url"`
	LastError   string `json:"last_error,omitempty"`
	AttemptedAt string `json:"attempted_at"`
	SyncedAt    string `json:"synced_at,omitempty"`
	// Pending is "create" or "update" while the card is behind the resource, derived rather than stored.
	Pending string `json:"pending,omitempty"`
}

func cardView(c domain.Card) CardView {
	v := CardView{Kind: string(c.Kind), ID: domain.FormatID(c.Kind, c.EntityID), Provider: c.Provider, Key: c.Key,
		URL: c.URL, LastError: c.LastError}
	if !c.AttemptedAt.IsZero() {
		v.AttemptedAt = ts(c.AttemptedAt)
	}
	if !c.SyncedAt.IsZero() {
		v.SyncedAt = ts(c.SyncedAt)
	}
	return v
}

func checkCardTarget(kind domain.Kind, provider string) error {
	switch kind {
	case domain.KindInitiative, domain.KindMilestone, domain.KindTask:
	default:
		return fmt.Errorf("%w: %s has no card", domain.ErrInvalid, kind)
	}
	return domain.RequireText("provider", provider)
}

// Card returns the entity's card record, or nil when there is none.
func (s *Service) Card(ctx context.Context, kind domain.Kind, entityID int64, provider string) (*CardView, error) {
	if err := checkCardTarget(kind, provider); err != nil {
		return nil, err
	}
	c, err := s.store.GetCard(ctx, kind, entityID, provider)
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	v := cardView(c)
	return &v, nil
}

// ListCards lists a provider's card records ordered by kind, then ID.
func (s *Service) ListCards(ctx context.Context, provider string) ([]CardView, error) {
	if err := domain.RequireText("provider", provider); err != nil {
		return nil, err
	}
	cards, err := s.store.ListCards(ctx, provider)
	if err != nil {
		return nil, err
	}
	out := make([]CardView, 0, len(cards))
	for _, c := range cards {
		out = append(out, cardView(c))
	}
	return out, nil
}

// RecordCard stores a successful push: the key, URL, and hash, with any earlier error cleared.
func (s *Service) RecordCard(ctx context.Context, kind domain.Kind, entityID int64, provider, key, url, hash string) error {
	if err := checkCardTarget(kind, provider); err != nil {
		return err
	}
	now := time.Now().UTC()
	return s.store.SetCard(ctx, &domain.Card{Kind: kind, EntityID: entityID, Provider: provider, Key: key, URL: url,
		ContentHash: hash, AttemptedAt: now, SyncedAt: now})
}

// RecordCardFailure stores the error and attempt time, keeping any existing key, URL, hash, and sync time.
func (s *Service) RecordCardFailure(ctx context.Context, kind domain.Kind, entityID int64, provider string, cause error) error {
	if err := checkCardTarget(kind, provider); err != nil {
		return err
	}
	if cause == nil {
		return fmt.Errorf("%w: a card failure needs a cause", domain.ErrInvalid)
	}
	return s.store.InTx(ctx, func(st store.Store) error {
		c, err := st.GetCard(ctx, kind, entityID, provider)
		if errors.Is(err, store.ErrNotFound) {
			c = domain.Card{Kind: kind, EntityID: entityID, Provider: provider}
		} else if err != nil {
			return err
		}
		c.LastError = cause.Error()
		c.AttemptedAt = time.Now().UTC()
		return st.SetCard(ctx, &c)
	})
}

// buildCardRequest composes the request for a resource from its stored state.
func buildCardRequest(ctx context.Context, st store.Store, kind domain.Kind, id int64, parentKey string) (taskmanager.CardRequest, error) {
	req := taskmanager.CardRequest{Kind: kind, ID: domain.FormatID(kind, id), ParentKey: parentKey}
	switch kind {
	case domain.KindInitiative:
		i, err := st.GetInitiative(ctx, id)
		if err != nil {
			return req, err
		}
		req.Title, req.Description = i.Title, i.Brief
	case domain.KindMilestone:
		m, err := st.GetMilestone(ctx, id)
		if err != nil {
			return req, err
		}
		req.Title, req.Description = m.Title, m.Description
		tasks, err := st.ListTasks(ctx, store.TaskFilter{MilestoneID: id})
		if err != nil {
			return req, err
		}
		req.Status = MilestoneStatus(tasks)
	case domain.KindTask:
		t, err := st.GetTask(ctx, id)
		if err != nil {
			return req, err
		}
		req.Title, req.Description = t.Title, t.Description
		req.Status = string(t.Status)
		if t.AcceptanceCriteria != "" {
			req.Description += "\n\n## Acceptance criteria\n\n" + t.AcceptanceCriteria
		}
	default:
		return req, fmt.Errorf("%w: %s has no card", domain.ErrInvalid, kind)
	}
	return req, nil
}

// cardParent returns the resource whose card is the parent of this one.
func (s *Service) cardParent(ctx context.Context, kind domain.Kind, id int64) (domain.Kind, int64, error) {
	switch kind {
	case domain.KindMilestone:
		m, err := s.store.GetMilestone(ctx, id)
		return domain.KindInitiative, m.InitiativeID, err
	case domain.KindTask:
		t, err := s.store.GetTask(ctx, id)
		return domain.KindMilestone, t.MilestoneID, err
	}
	return "", 0, nil
}

// errCardClaimed means another process holds the card's creation, or the card exists.
var errCardClaimed = errors.New("card claimed elsewhere")

// createCard makes sure the resource, and any parent missing a card, has one.
// It returns a warning when the resource's card could not be created; it is
// empty on success and when cards are disabled. The provider is called only
// outside transactions.
func (s *Service) createCard(ctx context.Context, kind domain.Kind, id int64, stale time.Duration) string {
	if s.taskManager == nil || s.inTx {
		return ""
	}
	if _, err := s.ensureCard(ctx, kind, id, stale); err != nil {
		if errors.Is(err, errCardClaimed) {
			return ""
		}
		var sw statusWarning
		if errors.As(err, &sw) {
			return sw.msg
		}
		return fmt.Sprintf("%s card for %s not created: %v", s.taskManager.Name(), domain.FormatID(kind, id), err)
	}
	return ""
}

// CardConflictView names a card held by someone else, which blocks moving it.
type CardConflictView struct {
	Card     string `json:"card"`
	Resource string `json:"resource"`
	Assignee string `json:"assignee"`
}

// statusWarning is a created card whose status could not be set.
type statusWarning struct {
	msg      string
	conflict *CardConflictView
}

func (e statusWarning) Error() string { return e.msg }

// statusNotSet records a failed status push and returns its warning, plus the
// conflict when another person holds the card.
func (s *Service) statusNotSet(ctx context.Context, kind domain.Kind, id int64, key string, cause error) (string, *CardConflictView) {
	name := domain.FormatID(kind, id)
	warn := fmt.Sprintf("%s card %s for %s status not set: %v", s.taskManager.Name(), key, name, cause)
	if rerr := s.RecordCardFailure(ctx, kind, id, s.taskManager.Name(), cause); rerr != nil {
		s.log.Error("recording card failure", "id", name, "error", rerr)
		warn += fmt.Sprintf(" (recording the failure also failed: %v)", rerr)
	}
	var conflict *taskmanager.AssigneeConflictError
	if errors.As(cause, &conflict) {
		return warn, &CardConflictView{Card: conflict.Key, Resource: name, Assignee: conflict.Assignee}
	}
	return warn, nil
}

func (s *Service) ensureCard(ctx context.Context, kind domain.Kind, id int64, stale time.Duration) (string, error) {
	p := s.taskManager
	if c, err := s.store.GetCard(ctx, kind, id, p.Name()); err == nil && c.Key != "" {
		return c.Key, nil
	} else if err != nil && !errors.Is(err, store.ErrNotFound) {
		return "", err
	}

	var parentKey string
	if kind != domain.KindInitiative {
		pk, pid, err := s.cardParent(ctx, kind, id)
		if err != nil {
			return "", err
		}
		parentKey, err = s.ensureCard(ctx, pk, pid, stale)
		if parentKey == "" {
			if err == nil || errors.Is(err, errCardClaimed) {
				err = errors.New("its creation is in progress or recently failed")
			}
			err = fmt.Errorf("parent %s has no card: %w", domain.FormatID(pk, pid), err)
			return "", s.failCard(ctx, kind, id, err)
		}
	}

	req, err := buildCardRequest(ctx, s.store, kind, id, parentKey)
	if err != nil {
		return "", err
	}
	claimed, err := s.store.ClaimCard(ctx, kind, id, p.Name(), stale)
	if err != nil {
		return "", err
	}
	if !claimed {
		return "", errCardClaimed
	}
	card, err := p.Create(ctx, req)
	if err != nil {
		return "", s.failCard(ctx, kind, id, err)
	}
	hash := req.Hash()
	if req.Status != "" {
		if err := s.RecordCard(ctx, kind, id, p.Name(), card.Key, card.URL, ""); err != nil {
			s.log.Error("recording card", "id", req.ID, "key", card.Key, "error", err)
			return card.Key, fmt.Errorf("created %s but recording it failed: %w", card.Key, err)
		}
		if err := p.SetStatus(ctx, card.Key, req.Status); err != nil {
			warn, conflict := s.statusNotSet(ctx, kind, id, card.Key, err)
			return card.Key, statusWarning{msg: warn, conflict: conflict}
		}
	}
	if err := s.RecordCard(ctx, kind, id, p.Name(), card.Key, card.URL, hash); err != nil {
		s.log.Error("recording card", "id", req.ID, "key", card.Key, "error", err)
		return card.Key, fmt.Errorf("created %s but recording it failed: %w", card.Key, err)
	}
	return card.Key, nil
}

type cardMode int

const (
	// modeExisting leaves resources without a card row alone.
	modeExisting cardMode = iota
	// modeCreateMissing creates cards for resources without a row.
	modeCreateMissing
)

type cardResult int

const (
	created cardResult = iota
	updated
	inSync
	skipped
	failed
)

// syncCard brings the resource's card up to date and reports what it did,
// with a warning when it failed and the conflict when another person holds the
// card. The provider is called only outside
// transactions; errors recording the outcome are logged and folded into the warning.
func (s *Service) syncCard(ctx context.Context, kind domain.Kind, id int64, mode cardMode, stale time.Duration) (cardResult, string, *CardConflictView) {
	if s.taskManager == nil || s.inTx {
		return skipped, "", nil
	}
	p := s.taskManager
	name := domain.FormatID(kind, id)
	row, err := s.store.GetCard(ctx, kind, id, p.Name())
	missing := errors.Is(err, store.ErrNotFound)
	if err != nil && !missing {
		s.log.Error("loading card", "id", name, "error", err)
		return failed, fmt.Sprintf("%s card for %s not synced: %v", p.Name(), name, err), nil
	}
	if missing && mode == modeExisting {
		return skipped, "", nil
	}
	if missing || row.Key == "" {
		if _, err := s.ensureCard(ctx, kind, id, stale); err != nil {
			if errors.Is(err, errCardClaimed) {
				return skipped, "", nil
			}
			var sw statusWarning
			if errors.As(err, &sw) {
				return failed, sw.msg, sw.conflict
			}
			return failed, fmt.Sprintf("%s card for %s not created: %v", p.Name(), name, err), nil
		}
		return created, "", nil
	}

	parentKey := ""
	if kind != domain.KindInitiative {
		pk, pid, err := s.cardParent(ctx, kind, id)
		if err != nil {
			return failed, fmt.Sprintf("%s card %s for %s not updated: %v", p.Name(), row.Key, name, err), nil
		}
		if pr, err := s.store.GetCard(ctx, pk, pid, p.Name()); err == nil {
			parentKey = pr.Key
		} else if !errors.Is(err, store.ErrNotFound) {
			return failed, fmt.Sprintf("%s card %s for %s not updated: %v", p.Name(), row.Key, name, err), nil
		}
	}
	req, err := buildCardRequest(ctx, s.store, kind, id, parentKey)
	if err != nil {
		return failed, fmt.Sprintf("%s card %s for %s not updated: %v", p.Name(), row.Key, name, err), nil
	}
	hash := req.Hash()
	if row.ContentHash == hash {
		return inSync, "", nil
	}
	if err := p.Update(ctx, row.Key, req); err != nil {
		warn := fmt.Sprintf("%s card %s for %s not updated: %v", p.Name(), row.Key, name, err)
		if rerr := s.RecordCardFailure(ctx, kind, id, p.Name(), err); rerr != nil {
			s.log.Error("recording card failure", "id", name, "error", rerr)
			warn += fmt.Sprintf(" (recording the failure also failed: %v)", rerr)
		}
		return failed, warn, nil
	}
	if req.Status != "" {
		if err := p.SetStatus(ctx, row.Key, req.Status); err != nil {
			warn, conflict := s.statusNotSet(ctx, kind, id, row.Key, err)
			return failed, warn, conflict
		}
	}
	if err := s.RecordCard(ctx, kind, id, p.Name(), row.Key, row.URL, hash); err != nil {
		s.log.Error("recording card", "id", name, "key", row.Key, "error", err)
		return updated, fmt.Sprintf("%s card %s for %s updated but recording it failed: %v", p.Name(), row.Key, name, err), nil
	}
	return updated, "", nil
}

// failCard records cause on the card row and returns it, adding any error from recording.
func (s *Service) failCard(ctx context.Context, kind domain.Kind, id int64, cause error) error {
	if err := s.RecordCardFailure(ctx, kind, id, s.taskManager.Name(), cause); err != nil {
		s.log.Error("recording card failure", "id", domain.FormatID(kind, id), "error", err)
		return fmt.Errorf("%w (recording the failure also failed: %v)", cause, err)
	}
	return cause
}

// LinkMilestonePullRequest shows the milestone's recorded pull request on its
// card. It returns a warning on failure, and "" on success or when there is
// nothing to do. It never writes to the store. The provider is called only
// outside transactions.
func (s *Service) LinkMilestonePullRequest(ctx context.Context, milestoneID int64) string {
	if s.taskManager == nil || s.inTx {
		return ""
	}
	linker, ok := s.taskManager.(taskmanager.PullRequestLinker)
	if !ok {
		return ""
	}
	name := domain.FormatID(domain.KindMilestone, milestoneID)
	provider := s.taskManager.Name()
	m, err := s.store.GetMilestone(ctx, milestoneID)
	if err != nil {
		s.log.Error("loading milestone", "id", name, "error", err)
		return fmt.Sprintf("%s card for %s pull request link not added: %v", provider, name, err)
	}
	pr := m.PullRequest
	if pr.IsZero() {
		return ""
	}
	card, err := s.store.GetCard(ctx, domain.KindMilestone, milestoneID, provider)
	if errors.Is(err, store.ErrNotFound) || (err == nil && card.Key == "") {
		return ""
	}
	if err != nil {
		s.log.Error("loading card", "id", name, "error", err)
		return fmt.Sprintf("%s card for %s pull request link not added: %v", provider, name, err)
	}
	var url string
	if s.pullRequestLink != nil {
		url, _ = s.pullRequestLink(pr.Provider, pr.Namespace, pr.Repository, pr.Number)
	}
	if url == "" {
		return "no link could be built for the pull request"
	}
	ctx, cancel := context.WithTimeout(ctx, s.cardTimeout)
	defer cancel()
	link := taskmanager.PullRequestLink{
		ID:    fmt.Sprintf("ralph-pr:%s:%s/%s#%d", pr.Provider, pr.Namespace, pr.Repository, pr.Number),
		Title: fmt.Sprintf("Pull request %s#%d", pr.Repository, pr.Number),
		URL:   url,
	}
	if err := linker.LinkPullRequest(ctx, card.Key, link); err != nil {
		return fmt.Sprintf("%s card %s: pull request link not added: %v", provider, card.Key, err)
	}
	return ""
}

// CardsEnabled reports whether a task manager provider is configured.
func (s *Service) CardsEnabled() bool { return s.taskManager != nil }

// CardCanceledStatus is the status deleted resources' cards move to; empty means none.
func (s *Service) CardCanceledStatus() string { return s.cardCanceledStatus }

type cardID struct {
	kind domain.Kind
	id   int64
}

// cardIndex holds one provider's card rows, loaded with a single query.
type cardIndex struct {
	s     *Service
	cards map[cardID]domain.Card
}

// loadCards returns nil when no provider is configured.
func (s *Service) loadCards(ctx context.Context) (*cardIndex, error) {
	if s.taskManager == nil {
		return nil, nil
	}
	rows, err := s.store.ListCards(ctx, s.taskManager.Name())
	if err != nil {
		return nil, err
	}
	ix := &cardIndex{s: s, cards: make(map[cardID]domain.Card, len(rows))}
	for _, c := range rows {
		ix.cards[cardID{c.Kind, c.EntityID}] = c
	}
	return ix, nil
}

// view returns the resource's card with its derived pending state, or nil when
// there is no index or no row. parent is the resource whose card is the parent
// of this one (zero for an initiative). It never fails: a request that cannot
// be built leaves pending unknown.
func (ix *cardIndex) view(ctx context.Context, kind domain.Kind, id int64, parent cardID) *CardView {
	if ix == nil {
		return nil
	}
	c, ok := ix.cards[cardID{kind, id}]
	if !ok {
		return nil
	}
	v := cardView(c)
	switch {
	case c.Key == "":
		v.Pending = "create"
	case c.ContentHash != "":
		parentKey := ""
		if parent.kind != "" {
			parentKey = ix.cards[parent].Key
			if parentKey == "" {
				return &v
			}
		}
		req, err := buildCardRequest(ctx, ix.s.store, kind, id, parentKey)
		if err == nil && req.Hash() != c.ContentHash {
			v.Pending = "update"
		}
	default:
		v.Pending = "update"
	}
	return &v
}

// ErrNoTaskManager means a card operation was asked for while no provider is configured.
var ErrNoTaskManager = errors.New("no task manager is configured; set task_manager.provider")

// SyncOptions scopes SyncCards. A zero InitiativeID means every initiative.
type SyncOptions struct {
	InitiativeID    int64
	IncludeArchived bool
	// Progress, when set, is called with each item as soon as its card is created.
	Progress func(SyncItem)
}

// SyncItem is the outcome for one resource. For a skipped item, Error holds the reason.
type SyncItem struct {
	ID      string `json:"id"`
	Outcome string `json:"outcome"`
	Key     string `json:"key,omitempty"`
	URL     string `json:"url,omitempty"`
	Error   string `json:"error,omitempty"`
}

// SyncReport counts and lists what SyncCards did, in processing order.
type SyncReport struct {
	Created int        `json:"created"`
	Updated int        `json:"updated"`
	InSync  int        `json:"in_sync"`
	Skipped int        `json:"skipped"`
	Failed  int        `json:"failed"`
	Items   []SyncItem `json:"items"`

	progress func(SyncItem)
}

const (
	syncReasonCancelled = "cancelled"
	syncReasonInFlight  = "creation in progress"
)

func (r *SyncReport) add(it SyncItem) {
	switch it.Outcome {
	case "created":
		r.Created++
	case "updated":
		r.Updated++
	case "in_sync":
		r.InSync++
	case "skipped":
		r.Skipped++
	case "failed":
		r.Failed++
	}
	r.Items = append(r.Items, it)
	if it.Outcome == "created" && r.progress != nil {
		r.progress(it)
	}
}

// SyncCards creates missing cards and pushes pending changes for every
// resource, parents first. A failure never stops the sync. It must not run
// inside a transaction.
func (s *Service) SyncCards(ctx context.Context, opts SyncOptions) (SyncReport, error) {
	report := SyncReport{Items: []SyncItem{}, progress: opts.Progress}
	if s.taskManager == nil {
		return report, ErrNoTaskManager
	}
	if s.inTx {
		return report, errors.New("cards cannot be synced inside a transaction")
	}
	if pf, ok := s.taskManager.(taskmanager.Preflighter); ok {
		if err := pf.Preflight(ctx); err != nil {
			return report, fmt.Errorf("task manager check failed, nothing was synced: %w", err)
		}
	}
	var initiatives []domain.Initiative
	if opts.InitiativeID != 0 {
		i, err := s.store.GetInitiative(ctx, opts.InitiativeID)
		if err != nil {
			return report, err
		}
		if i.ArchivedAt.IsZero() || opts.IncludeArchived {
			initiatives = append(initiatives, i)
		}
	} else {
		all, err := s.store.ListInitiatives(ctx, !opts.IncludeArchived)
		if err != nil {
			return report, err
		}
		initiatives = all
	}
	for _, i := range initiatives {
		if err := s.syncInitiative(ctx, i.ID, &report); err != nil {
			return report, err
		}
	}
	return report, nil
}

func (s *Service) syncInitiative(ctx context.Context, id int64, report *SyncReport) error {
	ms, err := s.store.ListMilestones(ctx, id, false)
	if err != nil {
		return err
	}
	tasks, err := s.store.ListTasks(ctx, store.TaskFilter{InitiativeID: id})
	if err != nil {
		return err
	}
	byMilestone := map[int64][]domain.Task{}
	for _, t := range tasks {
		byMilestone[t.MilestoneID] = append(byMilestone[t.MilestoneID], t)
	}
	parent, err := s.syncOne(ctx, domain.KindInitiative, id, false, nil, report)
	if err != nil {
		return err
	}
	for _, m := range ms {
		mTasks := byMilestone[m.ID]
		mParent, err := s.syncOne(ctx, domain.KindMilestone, m.ID, MilestoneStatus(mTasks) == MilestoneCancelled, parent, report)
		if err != nil {
			return err
		}
		for _, t := range mTasks {
			if _, err := s.syncOne(ctx, domain.KindTask, t.ID, t.Status == domain.StatusCancelled, mParent, report); err != nil {
				return err
			}
		}
	}
	return nil
}

// noCard is a resource that could not be given a card, which its children inherit.
type noCard struct {
	id    string
	cause string
}

// syncOne syncs one resource and returns non-nil when it has no card afterwards because of a failure
// or an in-flight creation. A non-nil parent means the resource is not attempted.
func (s *Service) syncOne(ctx context.Context, kind domain.Kind, id int64, cancelled bool, parent *noCard, report *SyncReport) (*noCard, error) {
	name := domain.FormatID(kind, id)
	p := s.taskManager.Name()
	if parent != nil {
		cause := fmt.Sprintf("parent %s has no card: %s", parent.id, parent.cause)
		report.add(SyncItem{ID: name, Outcome: "failed", Error: cause})
		return &noCard{id: name, cause: cause}, nil
	}
	row, err := s.store.GetCard(ctx, kind, id, p)
	hasRow := err == nil
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		cause := fmt.Sprintf("%s card for %s not synced: %v", p, name, err)
		report.add(SyncItem{ID: name, Outcome: "failed", Error: cause})
		return &noCard{id: name, cause: cause}, nil
	}
	if cancelled && !hasRow {
		report.add(SyncItem{ID: name, Outcome: "skipped", Error: syncReasonCancelled})
		return nil, nil
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	res, warn, _ := s.syncCard(ctx, kind, id, modeCreateMissing, s.cardTimeout)
	row, err = s.store.GetCard(ctx, kind, id, p)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		s.log.Error("loading card", "id", name, "error", err)
	}
	it := SyncItem{ID: name, Key: row.Key, URL: row.URL}
	switch res {
	case created:
		it.Outcome = "created"
	case updated:
		it.Outcome = "updated"
	case inSync:
		it.Outcome = "in_sync"
	case skipped:
		it.Outcome, it.Error = "skipped", syncReasonInFlight
	default:
		it.Outcome = "failed"
	}
	if res == failed || res == updated {
		it.Error = warn
	}
	report.add(it)
	switch {
	case res == failed && row.Key == "":
		return &noCard{id: name, cause: warn}, nil
	case res == skipped && row.Key == "":
		return &noCard{id: name, cause: syncReasonInFlight}, nil
	}
	return nil, nil
}
