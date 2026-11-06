// Package embed defines text-embedding providers.
package embed

import (
	"context"
	"errors"
)

// ErrUnavailable means the provider could not be reached or is not
// configured, so callers should degrade (mark chunks stale, fall back to
// keyword search) rather than fail.
var ErrUnavailable = errors.New("embedding provider unavailable")

// Purpose says how embedded text will be used. Asymmetric models embed
// stored content and search queries differently.
type Purpose int

const (
	// Document is content stored in the index.
	Document Purpose = iota
	// Query is a search query compared against stored documents.
	Query
)

// Embedder turns texts into vectors. Implementations must return exactly one
// vector per input, in order.
type Embedder interface {
	Embed(ctx context.Context, purpose Purpose, texts []string) ([][]float32, error)
	// Model identifies the vector space of Document embeddings; vectors from
	// different identities are never compared.
	Model() string
}
