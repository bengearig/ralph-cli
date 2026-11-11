// Package store defines persistence for ralph's entities and search index.
package store

import (
	"context"
	"errors"
	"time"

	"github.com/bengearig/ralph/internal/domain"
)

var (
	ErrNotFound = errors.New("not found")
	// ErrConflict marks a uniqueness violation, such as a duplicate slug.
	ErrConflict = errors.New("conflict")
)

// TaskFilter narrows ListTasks. Zero values do not filter.
type TaskFilter struct {
	InitiativeID int64
	MilestoneID  int64
	Statuses     []domain.Status
	// ExcludeArchived drops tasks of archived initiatives.
	ExcludeArchived bool
}

// DocumentFilter narrows ListDocuments. ProjectOnly selects documents with no
// initiative; otherwise a non-zero InitiativeID selects that initiative's.
type DocumentFilter struct {
	InitiativeID int64
	ProjectOnly  bool
	Kind         domain.DocKind
	// ExcludeArchived drops documents that are archived or belong to an archived initiative.
	ExcludeArchived bool
}

// Owner identifies the entity a chunk was derived from.
type Owner struct {
	Kind domain.Kind
	ID   int64
}

// Chunk is a searchable slice of an entity's text. A nil Embedding, or a
// Model different from the active one, means the chunk needs (re)embedding.
type Chunk struct {
	ID           int64
	Owner        Owner
	InitiativeID int64
	Seq          int
	Text         string
	ContentHash  string
	Model        string
	Embedding    []float32
}

// ChunkFilter narrows chunk queries. Zero values do not filter.
type ChunkFilter struct {
	Kinds        []domain.Kind
	InitiativeID int64
	// ExcludeArchived drops chunks of archived initiatives and of archived documents.
	ExcludeArchived bool
}

// KeywordHit is a full-text match; higher Score is better.
type KeywordHit struct {
	Chunk Chunk
	Score float64
}

// Cascade counts the rows a delete removes, the target included.
type Cascade struct {
	Initiatives int
	Milestones  int
	Tasks       int
	Notes       int
	Documents   int
}

// Store persists entities and the search index. Implementations must be safe
// for concurrent use by multiple processes against the same database.
type Store interface {
	CreateInitiative(ctx context.Context, i *domain.Initiative) error
	GetInitiative(ctx context.Context, id int64) (domain.Initiative, error)
	ListInitiatives(ctx context.Context, excludeArchived bool) ([]domain.Initiative, error)
	UpdateInitiative(ctx context.Context, i *domain.Initiative) error
	// SetInitiativeArchived sets archived_at, or clears it when at is zero. It leaves updated_at alone.
	SetInitiativeArchived(ctx context.Context, id int64, at time.Time) error
	// DeleteInitiative removes an initiative with its milestones, tasks, notes, and documents.
	DeleteInitiative(ctx context.Context, id int64) error

	CreateMilestone(ctx context.Context, m *domain.Milestone) error
	GetMilestone(ctx context.Context, id int64) (domain.Milestone, error)
	// ListMilestones lists all milestones, or one initiative's when initiativeID is non-zero.
	ListMilestones(ctx context.Context, initiativeID int64, excludeArchived bool) ([]domain.Milestone, error)
	// UpdateMilestone never writes the git record; use SetMilestoneGit.
	UpdateMilestone(ctx context.Context, m *domain.Milestone) error
	// SetMilestoneGit replaces a milestone's git record without changing updated_at.
	SetMilestoneGit(ctx context.Context, id int64, g domain.MilestoneGit) error
	// SetMilestonePullRequest replaces a milestone's pull request record without
	// changing updated_at; a zero record clears it.
	SetMilestonePullRequest(ctx context.Context, id int64, rec domain.PullRequestRecord) error
	// DeleteMilestone removes a milestone with its tasks and their notes, and the dependency edges to and from it.
	DeleteMilestone(ctx context.Context, id int64) error
	// SetMilestoneDependencies replaces the milestone's whole dependency set; an empty slice clears it.
	// A missing milestone, on either side, gives ErrNotFound.
	SetMilestoneDependencies(ctx context.Context, milestoneID int64, dependsOn []int64) error
	// ListMilestoneDependencies maps each dependent milestone to the milestones it depends on, ascending, for
	// the dependents of one initiative, or of every initiative when initiativeID is 0.
	ListMilestoneDependencies(ctx context.Context, initiativeID int64) (map[int64][]int64, error)
	// ListDependents returns the milestones that depend on the milestone, ascending.
	ListDependents(ctx context.Context, milestoneID int64) ([]int64, error)

	CreateTask(ctx context.Context, t *domain.Task) error
	GetTask(ctx context.Context, id int64) (domain.Task, error)
	// ListTasks orders by milestone position, then task position.
	ListTasks(ctx context.Context, f TaskFilter) ([]domain.Task, error)
	// UpdateTask never writes the commit; use SetTaskCommit.
	UpdateTask(ctx context.Context, t *domain.Task) error
	// SetTaskCommit records a task's commit without changing updated_at.
	SetTaskCommit(ctx context.Context, id int64, hash string) error
	AddTaskNote(ctx context.Context, n *domain.TaskNote) error
	ListTaskNotes(ctx context.Context, taskID int64) ([]domain.TaskNote, error)
	GetTaskNote(ctx context.Context, id int64) (domain.TaskNote, error)
	DeleteTaskNote(ctx context.Context, id int64) error
	// DeleteTask removes a task with its notes.
	DeleteTask(ctx context.Context, id int64) error

	CreateDocument(ctx context.Context, d *domain.Document) error
	GetDocument(ctx context.Context, id int64) (domain.Document, error)
	ListDocuments(ctx context.Context, f DocumentFilter) ([]domain.Document, error)
	UpdateDocument(ctx context.Context, d *domain.Document) error
	// SetDocumentArchived sets archived_at, or clears it when at is zero. It leaves updated_at alone.
	SetDocumentArchived(ctx context.Context, id int64, at time.Time) error
	// SetDocumentScope sets the initiative and origin initiative (nil for project scope or no origin) and, when
	// slug is not empty, the slug. It bumps updated_at and returns ErrConflict if (initiative, slug) is taken.
	SetDocumentScope(ctx context.Context, id int64, initiativeID, originInitiativeID *int64, slug string) error
	DeleteDocument(ctx context.Context, id int64) error

	// GetCard returns ErrNotFound when the entity has no card row for the provider.
	GetCard(ctx context.Context, kind domain.Kind, entityID int64, provider string) (domain.Card, error)
	// SetCard inserts the card or replaces the row with the same kind, entity, and provider.
	SetCard(ctx context.Context, c *domain.Card) error
	// ClaimCard atomically reserves creation of the entity's card: it inserts an empty-key row stamped now when there
	// is none, or refreshes the stamp of an empty-key row last attempted more than staleAfter ago, and reports true.
	// A row with a key, or an empty-key row attempted more recently, is left alone and reports false.
	ClaimCard(ctx context.Context, kind domain.Kind, entityID int64, provider string, staleAfter time.Duration) (bool, error)
	// ListCards lists a provider's cards ordered by kind, then entity ID.
	ListCards(ctx context.Context, provider string) ([]domain.Card, error)
	// DeleteCards removes the entity's cards under every provider; it is a no-op when there are none.
	// DeleteInitiative, DeleteMilestone, and DeleteTask already remove the cards they cascade to.
	DeleteCards(ctx context.Context, kind domain.Kind, entityID int64) error
	CreateImport(ctx context.Context, im *domain.Import) error
	GetImportByPath(ctx context.Context, path string) (domain.Import, error)
	// UpdateImport writes every column except id and path, locating the row by path.
	UpdateImport(ctx context.Context, im *domain.Import) error
	// ListImports orders by path; no statuses means all.
	ListImports(ctx context.Context, statuses []domain.ImportStatus) ([]domain.Import, error)
	// AddImportLink is idempotent. A missing document gives ErrNotFound.
	AddImportLink(ctx context.Context, importID, documentID int64, rel domain.ImportRelation) error
	// ListImportLinks orders by creation time, then document ID.
	ListImportLinks(ctx context.Context, importID int64) ([]domain.ImportLink, error)

	// CountCascade counts what deleting the initiative, milestone, or task
	// with the given ID would remove. It returns ErrNotFound for a missing ID.
	CountCascade(ctx context.Context, kind domain.Kind, id int64) (Cascade, error)

	// ReplaceChunks swaps an owner's chunks for a new set, keeping the
	// embedding of any chunk whose content hash and model are unchanged.
	ReplaceChunks(ctx context.Context, owner Owner, chunks []Chunk) error
	// StaleChunks returns chunks lacking an embedding for model.
	StaleChunks(ctx context.Context, model string, limit int) ([]Chunk, error)
	CountStaleChunks(ctx context.Context, model string) (int, error)
	SetEmbedding(ctx context.Context, chunkID int64, model string, vec []float32) error
	// EmbeddedChunks returns chunks embedded with model, for vector ranking.
	EmbeddedChunks(ctx context.Context, model string, f ChunkFilter) ([]Chunk, error)
	KeywordSearch(ctx context.Context, query string, f ChunkFilter, limit int) ([]KeywordHit, error)

	// InTx runs fn atomically; the Store passed to fn must be used for all
	// operations inside it.
	InTx(ctx context.Context, fn func(Store) error) error
	Close() error
}
