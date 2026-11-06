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

// Embedder turns texts into vectors. Implementations must return exactly one
// vector per input, in order.
type Embedder interface {
	Embed(ctx context.Context, texts []string) ([][]float32, error)
	// Model identifies the vector space; vectors from different models are
	// never compared.
	Model() string
}
