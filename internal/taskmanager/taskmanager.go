// Package taskmanager abstracts external task trackers. Providers register
// themselves by config name.
package taskmanager

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/bengearig/ralph/internal/config"
	"github.com/bengearig/ralph/internal/domain"
)

// Card is a record in the external task manager.
type Card struct {
	Key string `json:"key"`
	URL string `json:"url"`
}

// CardRequest describes a card to create or update. ID is the display ID such
// as T-12; ParentKey is empty for an initiative.
type CardRequest struct {
	Kind        domain.Kind
	ID          string
	Title       string
	Description string
	ParentKey   string
	// Status is the ralph status of a task or milestone; empty for an initiative.
	Status string
}

// Hash returns a deterministic SHA-256 hex digest of the request's content.
// Adding Status changed every existing hash once, so existing cards show as
// pending an update until next synced.
func (r CardRequest) Hash() string {
	b, _ := json.Marshal(struct {
		Kind, ID, Title, Description, ParentKey, Status string
	}{string(r.Kind), r.ID, r.Title, r.Description, r.ParentKey, r.Status})
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// Provider creates and updates cards in a task manager.
type Provider interface {
	Name() string
	// Validate checks configuration and credentials without network calls.
	Validate() error
	Create(ctx context.Context, req CardRequest) (Card, error)
	Update(ctx context.Context, key string, req CardRequest) error
	// SetStatus moves the card to the state matching a ralph status. It does
	// nothing and succeeds when the provider has no mapping for status.
	SetStatus(ctx context.Context, key, status string) error
}

// Preflighter is implemented by providers that can check their configuration
// against the task manager itself, which takes network calls. Sync runs it first.
type Preflighter interface {
	Preflight(ctx context.Context) error
}

// Factory builds a provider from configuration.
type Factory func(cfg *config.Config) (Provider, error)

var (
	mu        sync.RWMutex
	factories = map[string]Factory{}
)

// Register makes a provider available under name.
func Register(name string, f Factory) {
	mu.Lock()
	defer mu.Unlock()
	if _, dup := factories[name]; dup {
		panic("taskmanager: duplicate registration of " + name)
	}
	factories[name] = f
}

// New builds the configured provider. It returns a nil provider and nil error
// when the task manager is disabled; callers treat a nil provider as disabled.
func New(cfg *config.Config) (Provider, error) {
	name := cfg.TaskManager.Provider
	if name == config.TaskManagerNone {
		return nil, nil
	}
	mu.RLock()
	f, ok := factories[name]
	names := make([]string, 0, len(factories))
	for n := range factories {
		names = append(names, n)
	}
	mu.RUnlock()
	if !ok {
		sort.Strings(names)
		list := "(none)"
		if len(names) > 0 {
			list = strings.Join(names, ", ")
		}
		return nil, fmt.Errorf("unknown task manager provider %q (registered: %s)", name, list)
	}
	return f(cfg)
}
