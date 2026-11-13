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
	"time"

	"github.com/bengearig/ralph/internal/config"
	"github.com/bengearig/ralph/internal/domain"
)

// Card is a record in the external task manager.
type Card struct {
	Key string `json:"key"`
	URL string `json:"url"`
}

// AssigneeConflictError reports that a card is held by someone other than the
// user ralph acts as. Assignee is the holder's display name, or the account ID
// when there is none.
type AssigneeConflictError struct {
	Key, Assignee string
}

func (e *AssigneeConflictError) Error() string {
	return fmt.Sprintf("card %s is already assigned to %s", e.Key, e.Assignee)
}

// CardRequest describes a card to create or update. ID is the display ID such
// as T-12; ParentKey is the parent card an initiative was attached to, empty for none.
type CardRequest struct {
	Kind        domain.Kind
	ID          string
	Title       string
	Description string
	ParentKey   string
	// PreviousParentKey is the parent card an initiative's card was last sent, empty for none.
	// Update compares it with ParentKey to decide whether the parent changed; Hash ignores it.
	PreviousParentKey string
	// Status is the ralph status of a task or milestone; empty for an initiative.
	Status string
	// StoreID is the identifier of the ralph store the card belongs to.
	StoreID string
	// Sprint is the initiative's named sprint, set only for a milestone card, empty for none.
	Sprint string
}

// Hash returns a deterministic SHA-256 hex digest of the request's content.
// PreviousParentKey and Sprint are ignored.
// Adding Status or StoreID changed every existing hash once, so existing cards
// show as pending an update until next synced.
func (r CardRequest) Hash() string {
	b, _ := json.Marshal(struct {
		Kind, ID, Title, Description, ParentKey, Status, StoreID string
	}{string(r.Kind), r.ID, r.Title, r.Description, r.ParentKey, r.Status, r.StoreID})
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

// PullRequestLink is a pull request to show on a card. ID is stable for one
// pull request, so linking it again updates the link instead of adding another.
type PullRequestLink struct {
	ID, Title, URL string
}

// PullRequestLinker is implemented by providers that can attach a pull request
// link to a card. Callers skip a provider that does not implement it.
type PullRequestLinker interface {
	LinkPullRequest(ctx context.Context, key string, l PullRequestLink) error
}

// The moments a card can be added to a sprint: when it is created and when its
// work starts.
const (
	SprintMomentCreated = "created"
	SprintMomentStarted = "started"
)

// SprintRequest says which card kind and moment triggered a sprint add. Sprint
// is the initiative's named sprint, empty for none.
type SprintRequest struct {
	Kind   domain.Kind
	Moment string
	Sprint string
}

// SprintAdder is implemented by providers that can put a card in a sprint.
// Callers skip a provider that does not implement it. A non-empty note means
// the card was added with a caveat; an error means it was not added.
type SprintAdder interface {
	AddToSprint(ctx context.Context, key string, r SprintRequest) (note string, err error)
}

// CardCandidate is an open card found by a search.
type CardCandidate struct {
	Key, URL, Summary, Status string
	Created                   time.Time
}

// CardSearch narrows a search for open cards. The zero CreatedAfter means no
// date scope.
type CardSearch struct {
	CreatedAfter time.Time
}

// CardFinder is implemented by providers that can find open cards carrying
// ralph's labels. Callers skip a provider that does not implement it.
type CardFinder interface {
	FindOpenCards(ctx context.Context, s CardSearch) ([]CardCandidate, error)
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
