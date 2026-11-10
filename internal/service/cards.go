package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/bengearig/ralph/internal/domain"
	"github.com/bengearig/ralph/internal/store"
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
