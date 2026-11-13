package service

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"sync"

	"github.com/bengearig/ralph/internal/store"
)

// StoreIDSource says where the effective store identifier comes from.
type StoreIDSource string

const (
	StoreIDConfig StoreIDSource = "config"
	StoreIDStore  StoreIDSource = "store"
	StoreIDNone   StoreIDSource = "none"
)

const (
	storeIDMetaKey  = "store_id"
	storeIDAlphabet = "abcdefghjkmnpqrstuvwxyz23456789"
	storeIDLength   = 3
)

type storeIDCache struct {
	mu sync.Mutex
	id string
}

// StoreID returns the effective identifier without creating one.
func (s *Service) StoreID(ctx context.Context) (string, StoreIDSource, error) {
	if s.storeID != "" {
		return s.storeID, StoreIDConfig, nil
	}
	id, err := s.store.GetMeta(ctx, storeIDMetaKey)
	if errors.Is(err, store.ErrNotFound) {
		return "", StoreIDNone, nil
	}
	if err != nil {
		return "", StoreIDNone, err
	}
	return id, StoreIDStore, nil
}

// EnsureStoreID returns the configured identifier, else the stored one, generating and keeping one when neither exists.
func (s *Service) EnsureStoreID(ctx context.Context) (string, error) {
	if s.storeID != "" {
		return s.storeID, nil
	}
	c := s.storeIDCache
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.id != "" {
		return c.id, nil
	}
	id, err := s.store.GetMeta(ctx, storeIDMetaKey)
	if errors.Is(err, store.ErrNotFound) {
		var gen string
		if gen, err = generateStoreID(); err != nil {
			return "", err
		}
		id, err = s.store.SetMetaIfAbsent(ctx, storeIDMetaKey, gen)
	}
	if err != nil {
		return "", err
	}
	// A transaction may roll back, which would orphan the cached value.
	if !s.inTx {
		c.id = id
	}
	return id, nil
}

func generateStoreID() (string, error) {
	n := len(storeIDAlphabet)
	limit := 256 - 256%n
	out := make([]byte, 0, storeIDLength)
	buf := make([]byte, 16)
	for len(out) < storeIDLength {
		if _, err := rand.Read(buf); err != nil {
			return "", fmt.Errorf("generate store identifier: %w", err)
		}
		for _, b := range buf {
			if int(b) >= limit {
				continue
			}
			out = append(out, storeIDAlphabet[int(b)%n])
			if len(out) == storeIDLength {
				break
			}
		}
	}
	return string(out), nil
}
