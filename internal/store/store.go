// Package store defines persistence for ralph's entities and search index.
package store

import (
	"context"
	"errors"

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
}

// DocumentFilter narrows ListDocuments. ProjectOnly selects documents with no
// initiative; otherwise a non-zero InitiativeID selects that initiative's.
type DocumentFilter struct {
	InitiativeID int64
	ProjectOnly  bool
	Kind         domain.DocKind
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
}

// KeywordHit is a full-text match; higher Score is better.
type KeywordHit struct {
	Chunk Chunk
	Score float64
}

// Store persists entities and the search index. Implementations must be safe
// for concurrent use by multiple processes against the same database.
type Store interface {
	CreateInitiative(ctx context.Context, i *domain.Initiative) error
	GetInitiative(ctx context.Context, id int64) (domain.Initiative, error)
	ListInitiatives(ctx context.Context) ([]domain.Initiative, error)
	UpdateInitiative(ctx context.Context, i *domain.Initiative) error

	CreateMilestone(ctx context.Context, m *domain.Milestone) error
	GetMilestone(ctx context.Context, id int64) (domain.Milestone, error)
	// ListMilestones lists all milestones, or one initiative's when initiativeID is non-zero.
	ListMilestones(ctx context.Context, initiativeID int64) ([]domain.Milestone, error)
	// UpdateMilestone never writes the git record; use SetMilestoneGit.
	UpdateMilestone(ctx context.Context, m *domain.Milestone) error
	// SetMilestoneGit replaces a milestone's git record without changing updated_at.
	SetMilestoneGit(ctx context.Context, id int64, g domain.MilestoneGit) error

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

	CreateDocument(ctx context.Context, d *domain.Document) error
	GetDocument(ctx context.Context, id int64) (domain.Document, error)
	ListDocuments(ctx context.Context, f DocumentFilter) ([]domain.Document, error)
	UpdateDocument(ctx context.Context, d *domain.Document) error

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
