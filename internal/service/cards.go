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
	case domain.KindTask:
		t, err := st.GetTask(ctx, id)
		if err != nil {
			return req, err
		}
		req.Title, req.Description = t.Title, t.Description
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
func (s *Service) createCard(ctx context.Context, kind domain.Kind, id int64) string {
	if s.taskManager == nil || s.inTx {
		return ""
	}
	if _, err := s.ensureCard(ctx, kind, id); err != nil {
		if errors.Is(err, errCardClaimed) {
			return ""
		}
		return fmt.Sprintf("%s card for %s not created: %v", s.taskManager.Name(), domain.FormatID(kind, id), err)
	}
	return ""
}

func (s *Service) ensureCard(ctx context.Context, kind domain.Kind, id int64) (string, error) {
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
		parentKey, err = s.ensureCard(ctx, pk, pid)
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
	claimed, err := s.store.ClaimCard(ctx, kind, id, p.Name(), s.cardClaimStale)
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
	if err := s.RecordCard(ctx, kind, id, p.Name(), card.Key, card.URL, req.Hash()); err != nil {
		s.log.Error("recording card", "id", req.ID, "key", card.Key, "error", err)
		return card.Key, fmt.Errorf("created %s but recording it failed: %w", card.Key, err)
	}
	return card.Key, nil
}

// failCard records cause on the card row and returns it, adding any error from recording.
func (s *Service) failCard(ctx context.Context, kind domain.Kind, id int64, cause error) error {
	if err := s.RecordCardFailure(ctx, kind, id, s.taskManager.Name(), cause); err != nil {
		s.log.Error("recording card failure", "id", domain.FormatID(kind, id), "error", err)
		return fmt.Errorf("%w (recording the failure also failed: %v)", cause, err)
	}
	return cause
}

// CardsEnabled reports whether a task manager provider is configured.
func (s *Service) CardsEnabled() bool { return s.taskManager != nil }

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
