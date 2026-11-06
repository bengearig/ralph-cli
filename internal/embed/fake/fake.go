// Package fake provides a deterministic embedder for tests. Texts sharing
// words get similar vectors, so ranking behaves plausibly without a model.
package fake

import (
	"context"
	"hash/fnv"
	"math"
	"strings"
	"sync"
	"unicode"

	"github.com/bengearig/ralph/internal/embed"
)

// Embedder hashes words into a fixed-size bag-of-words vector.
type Embedder struct {
	Dims int
	Name string

	mu    sync.Mutex
	err   error
	calls int
}

var _ embed.Embedder = (*Embedder)(nil)

// New returns a fake embedder with 64 dimensions.
func New() *Embedder { return &Embedder{Dims: 64, Name: "fake/bow"} }

// Model returns the configured model name.
func (e *Embedder) Model() string { return e.Name }

// FailWith makes subsequent calls return err; nil restores success.
func (e *Embedder) FailWith(err error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.err = err
}

// Calls returns how many Embed calls have been made.
func (e *Embedder) Calls() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.calls
}

// Embed returns one normalized vector per text.
func (e *Embedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	e.mu.Lock()
	e.calls++
	err := e.err
	e.mu.Unlock()
	if err != nil {
		return nil, err
	}
	out := make([][]float32, len(texts))
	for i, t := range texts {
		out[i] = e.vector(t)
	}
	return out, nil
}

func (e *Embedder) vector(text string) []float32 {
	v := make([]float32, e.Dims)
	words := strings.FieldsFunc(strings.ToLower(text), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
	for _, w := range words {
		h := fnv.New32a()
		h.Write([]byte(w))
		v[int(h.Sum32())%e.Dims]++
	}
	var norm float64
	for _, x := range v {
		norm += float64(x * x)
	}
	if norm == 0 {
		v[0] = 1
		return v
	}
	n := float32(math.Sqrt(norm))
	for i := range v {
		v[i] /= n
	}
	return v
}
