package search

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"math"
	"sort"
	"time"

	"github.com/bengearig/ralph/internal/domain"
	"github.com/bengearig/ralph/internal/embed"
	"github.com/bengearig/ralph/internal/store"
)

// Search modes reported with results.
const (
	ModeVector  = "vector"
	ModeKeyword = "keyword"
)

const snippetRunes = 280

// Index maintains chunk embeddings and answers queries. A nil Embedder means
// embeddings are disabled and every search is keyword-only.
type Index struct {
	Store    store.Store
	Embedder embed.Embedder
	Chunker  Chunker
	Timeout  time.Duration
	Log      *slog.Logger
}

// Item is an entity's searchable text.
type Item struct {
	Owner        store.Owner
	InitiativeID int64
	Title        string
	Body         string
}

// IndexResult reports what indexing an item did. Warning is set when the
// write succeeded but embeddings could not be computed.
type IndexResult struct {
	Chunks  int    `json:"chunks"`
	Stale   int    `json:"stale"`
	Warning string `json:"warning,omitempty"`
}

func hash(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}

func (ix *Index) model() string {
	if ix.Embedder == nil {
		return ""
	}
	return ix.Embedder.Model()
}

func (ix *Index) logger() *slog.Logger {
	if ix.Log == nil {
		return slog.New(slog.DiscardHandler)
	}
	return ix.Log
}

func (ix *Index) embedCtx(ctx context.Context) (context.Context, context.CancelFunc) {
	if ix.Timeout <= 0 {
		return context.WithCancel(ctx)
	}
	return context.WithTimeout(ctx, ix.Timeout)
}

// Index rechunks an item, reusing embeddings for unchanged chunks and
// embedding the rest. Embedding failures leave chunks stale and are reported
// as a warning, never as an error, so writes are not lost.
func (ix *Index) Index(ctx context.Context, it Item) (IndexResult, error) {
	texts := ix.Chunker.Split(it.Title, it.Body)
	chunks := make([]store.Chunk, len(texts))
	for i, t := range texts {
		chunks[i] = store.Chunk{InitiativeID: it.InitiativeID, Seq: i, Text: t, ContentHash: hash(t)}
	}
	if err := ix.Store.ReplaceChunks(ctx, it.Owner, chunks); err != nil {
		return IndexResult{}, err
	}
	res := IndexResult{Chunks: len(chunks)}
	if ix.Embedder == nil {
		return res, nil
	}
	model := ix.model()
	var pending []store.Chunk
	for _, c := range chunks {
		if c.Embedding == nil || c.Model != model {
			pending = append(pending, c)
		}
	}
	if len(pending) == 0 {
		return res, nil
	}
	if err := ix.embedChunks(ctx, pending); err != nil {
		res.Stale = len(pending)
		res.Warning = fmt.Sprintf("saved, but %d chunk(s) could not be embedded and will use keyword search until `ralph index` succeeds: %v", len(pending), err)
		ix.logger().Debug("embedding failed; chunks left stale",
			"owner", domain.FormatID(it.Owner.Kind, it.Owner.ID), "chunks", len(pending), "err", err)
	}
	return res, nil
}

func (ix *Index) embedChunks(ctx context.Context, chunks []store.Chunk) error {
	texts := make([]string, len(chunks))
	for i, c := range chunks {
		texts[i] = c.Text
	}
	ectx, cancel := ix.embedCtx(ctx)
	defer cancel()
	vecs, err := ix.Embedder.Embed(ectx, embed.Document, texts)
	if err != nil {
		return err
	}
	if len(vecs) != len(chunks) {
		return fmt.Errorf("embedder returned %d vectors for %d chunks", len(vecs), len(chunks))
	}
	model := ix.model()
	return ix.Store.InTx(ctx, func(tx store.Store) error {
		for i, c := range chunks {
			if err := tx.SetEmbedding(ctx, c.ID, model, vecs[i]); err != nil {
				return err
			}
		}
		return nil
	})
}

// BackfillResult reports a backfill run.
type BackfillResult struct {
	Embedded  int `json:"embedded"`
	Remaining int `json:"remaining"`
}

// Backfill embeds stale chunks in batches until none remain or embedding
// fails. progress, if non-nil, is called after each batch.
func (ix *Index) Backfill(ctx context.Context, batch int, progress func(done, total int)) (BackfillResult, error) {
	var res BackfillResult
	if ix.Embedder == nil {
		return res, fmt.Errorf("%w: embeddings.provider is %q", embed.ErrUnavailable, "none")
	}
	if batch < 1 {
		batch = 32
	}
	model := ix.model()
	total, err := ix.Store.CountStaleChunks(ctx, model)
	if err != nil {
		return res, err
	}
	res.Remaining = total
	for res.Remaining > 0 {
		if err := ctx.Err(); err != nil {
			return res, err
		}
		stale, err := ix.Store.StaleChunks(ctx, model, batch)
		if err != nil {
			return res, err
		}
		if len(stale) == 0 {
			break
		}
		if err := ix.embedChunks(ctx, stale); err != nil {
			return res, err
		}
		res.Embedded += len(stale)
		res.Remaining = max(total-res.Embedded, 0)
		if progress != nil {
			progress(res.Embedded, total)
		}
	}
	res.Remaining, err = ix.Store.CountStaleChunks(ctx, model)
	return res, err
}

// Query describes a search.
type Query struct {
	Text         string
	Kinds        []domain.Kind
	InitiativeID int64
	Limit        int
}

// Hit is the best-matching chunk of one entity.
type Hit struct {
	Owner        store.Owner
	InitiativeID int64
	Score        float64
	Snippet      string
}

// Result is a ranked hit list plus how it was produced.
type Result struct {
	Mode     string
	Hits     []Hit
	Warnings []string
}

// Search ranks entities by cosine similarity of their best chunk, falling
// back to full-text keyword search when vectors are unavailable.
func (ix *Index) Search(ctx context.Context, q Query) (Result, error) {
	if q.Limit < 1 {
		q.Limit = 10
	}
	filter := store.ChunkFilter{Kinds: q.Kinds, InitiativeID: q.InitiativeID}
	var warnings []string

	if ix.Embedder != nil {
		res, warn, err := ix.vectorSearch(ctx, q, filter)
		if err != nil {
			return Result{}, err
		}
		if warn == "" {
			return res, nil
		}
		warnings = append(warnings, warn)
	}

	hits, err := ix.Store.KeywordSearch(ctx, q.Text, filter, q.Limit*5)
	if err != nil {
		return Result{}, err
	}
	scored := make([]scoredChunk, len(hits))
	for i, h := range hits {
		scored[i] = scoredChunk{h.Chunk, h.Score}
	}
	return Result{Mode: ModeKeyword, Hits: bestPerOwner(scored, q.Limit), Warnings: warnings}, nil
}

// vectorSearch returns a non-empty warning instead of results when the
// caller should fall back to keyword search.
func (ix *Index) vectorSearch(ctx context.Context, q Query, filter store.ChunkFilter) (Result, string, error) {
	model := ix.model()
	candidates, err := ix.Store.EmbeddedChunks(ctx, model, filter)
	if err != nil {
		return Result{}, "", err
	}
	if len(candidates) == 0 {
		return Result{}, "no embedded content for model " + model + " yet; run `ralph index`; using keyword search", nil
	}
	ectx, cancel := ix.embedCtx(ctx)
	defer cancel()
	vecs, err := ix.Embedder.Embed(ectx, embed.Query, []string{q.Text})
	if err != nil {
		return Result{}, fmt.Sprintf("could not embed query (%v); using keyword search", err), nil
	}
	if len(vecs) != 1 {
		return Result{}, "embedder returned no query vector; using keyword search", nil
	}
	qv := vecs[0]
	scored := make([]scoredChunk, 0, len(candidates))
	for _, c := range candidates {
		if len(c.Embedding) != len(qv) {
			continue
		}
		scored = append(scored, scoredChunk{c, Cosine(qv, c.Embedding)})
	}
	res := Result{Mode: ModeVector, Hits: bestPerOwner(scored, q.Limit)}
	if n, err := ix.Store.CountStaleChunks(ctx, model); err == nil && n > 0 {
		res.Warnings = append(res.Warnings, fmt.Sprintf("%d chunk(s) are not embedded yet and were not searched; run `ralph index`", n))
	}
	return res, "", nil
}

type scoredChunk struct {
	chunk store.Chunk
	score float64
}

func bestPerOwner(scored []scoredChunk, limit int) []Hit {
	best := map[store.Owner]scoredChunk{}
	for _, s := range scored {
		if cur, ok := best[s.chunk.Owner]; !ok || s.score > cur.score {
			best[s.chunk.Owner] = s
		}
	}
	hits := make([]Hit, 0, len(best))
	for _, s := range best {
		hits = append(hits, Hit{
			Owner:        s.chunk.Owner,
			InitiativeID: s.chunk.InitiativeID,
			Score:        s.score,
			Snippet:      snippet(s.chunk.Text),
		})
	}
	sort.Slice(hits, func(i, j int) bool {
		if hits[i].Score != hits[j].Score {
			return hits[i].Score > hits[j].Score
		}
		if hits[i].Owner.Kind != hits[j].Owner.Kind {
			return hits[i].Owner.Kind < hits[j].Owner.Kind
		}
		return hits[i].Owner.ID < hits[j].Owner.ID
	})
	if len(hits) > limit {
		hits = hits[:limit]
	}
	return hits
}

func snippet(s string) string {
	r := []rune(s)
	if len(r) <= snippetRunes {
		return s
	}
	return string(r[:snippetRunes-1]) + "…"
}

// Cosine returns the cosine similarity of equal-length vectors, or 0 when
// either has zero magnitude.
func Cosine(a, b []float32) float64 {
	var dot, na, nb float64
	for i := range a {
		x, y := float64(a[i]), float64(b[i])
		dot += x * y
		na += x * x
		nb += y * y
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / (math.Sqrt(na) * math.Sqrt(nb))
}
