package sqlite

import (
	"context"
	"database/sql"
	"encoding/binary"
	"fmt"
	"math"
	"strings"
	"unicode"

	"github.com/bengearig/ralph/internal/domain"
	"github.com/bengearig/ralph/internal/store"
)

const chunkCols = `c.id, c.owner_kind, c.owner_id, c.initiative_id, c.seq, c.text, c.content_hash, c.model, c.embedding`

func scanChunk(sc scanner) (store.Chunk, error) {
	var c store.Chunk
	var kind string
	var initiative sql.NullInt64
	var blob []byte
	if err := sc.Scan(&c.ID, &kind, &c.Owner.ID, &initiative, &c.Seq, &c.Text, &c.ContentHash, &c.Model, &blob); err != nil {
		return c, err
	}
	c.Owner.Kind = domain.Kind(kind)
	c.InitiativeID = initiative.Int64
	if blob != nil {
		vec, err := decodeVector(blob)
		if err != nil {
			return c, fmt.Errorf("chunk %d: %w", c.ID, err)
		}
		c.Embedding = vec
	}
	return c, nil
}

func encodeVector(v []float32) []byte {
	b := make([]byte, 4*len(v))
	for i, f := range v {
		binary.LittleEndian.PutUint32(b[4*i:], math.Float32bits(f))
	}
	return b
}

func decodeVector(b []byte) ([]float32, error) {
	if len(b)%4 != 0 {
		return nil, fmt.Errorf("embedding blob length %d is not a multiple of 4", len(b))
	}
	v := make([]float32, len(b)/4)
	for i := range v {
		v[i] = math.Float32frombits(binary.LittleEndian.Uint32(b[4*i:]))
	}
	return v, nil
}

func nullInitiative(id int64) sql.NullInt64 {
	return sql.NullInt64{Int64: id, Valid: id != 0}
}

func (s *Store) ReplaceChunks(ctx context.Context, owner store.Owner, chunks []store.Chunk) error {
	return s.inTx(ctx, func(tx *Store) error {
		rows, err := tx.q.QueryContext(ctx,
			`SELECT `+chunkCols+` FROM chunks c WHERE c.owner_kind = ? AND c.owner_id = ? AND c.embedding IS NOT NULL`,
			string(owner.Kind), owner.ID)
		if err != nil {
			return mapErr(err, "loading existing chunks")
		}
		existing, err := collect(rows, scanChunk)
		if err != nil {
			return err
		}
		reuse := make(map[string]store.Chunk, len(existing))
		for _, c := range existing {
			reuse[c.ContentHash] = c
		}

		if _, err := tx.q.ExecContext(ctx, `DELETE FROM chunks WHERE owner_kind = ? AND owner_id = ?`,
			string(owner.Kind), owner.ID); err != nil {
			return mapErr(err, "deleting chunks")
		}
		for i := range chunks {
			c := &chunks[i]
			c.Owner = owner
			if prev, ok := reuse[c.ContentHash]; ok && c.Embedding == nil {
				c.Model, c.Embedding = prev.Model, prev.Embedding
			}
			var blob []byte
			if c.Embedding != nil {
				blob = encodeVector(c.Embedding)
			}
			res, err := tx.q.ExecContext(ctx,
				`INSERT INTO chunks (owner_kind, owner_id, initiative_id, seq, text, content_hash, model, dims, embedding)
				 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				string(owner.Kind), owner.ID, nullInitiative(c.InitiativeID), c.Seq, c.Text, c.ContentHash,
				c.Model, len(c.Embedding), blob)
			if err != nil {
				return mapErr(err, "inserting chunk")
			}
			if c.ID, err = res.LastInsertId(); err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *Store) StaleChunks(ctx context.Context, model string, limit int) ([]store.Chunk, error) {
	if limit <= 0 {
		limit = -1
	}
	rows, err := s.q.QueryContext(ctx,
		`SELECT `+chunkCols+` FROM chunks c WHERE c.embedding IS NULL OR c.model <> ? ORDER BY c.id LIMIT ?`,
		model, limit)
	if err != nil {
		return nil, mapErr(err, "listing stale chunks")
	}
	return collect(rows, scanChunk)
}

func (s *Store) CountStaleChunks(ctx context.Context, model string) (int, error) {
	var n int
	err := s.q.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM chunks WHERE embedding IS NULL OR model <> ?`, model).Scan(&n)
	return n, mapErr(err, "counting stale chunks")
}

func (s *Store) SetEmbedding(ctx context.Context, chunkID int64, model string, vec []float32) error {
	if len(vec) == 0 {
		return fmt.Errorf("chunk %d: embedding must not be empty", chunkID)
	}
	res, err := s.q.ExecContext(ctx,
		`UPDATE chunks SET model = ?, dims = ?, embedding = ? WHERE id = ?`,
		model, len(vec), encodeVector(vec), chunkID)
	if err != nil {
		return mapErr(err, "storing embedding")
	}
	return checkAffected(res, fmt.Sprintf("chunk %d", chunkID))
}

func chunkWhere(f store.ChunkFilter) ([]string, []any) {
	var where []string
	var args []any
	if len(f.Kinds) > 0 {
		where = append(where, `c.owner_kind IN (`+placeholders(len(f.Kinds))+`)`)
		for _, k := range f.Kinds {
			args = append(args, string(k))
		}
	}
	if f.InitiativeID != 0 {
		where = append(where, `c.initiative_id = ?`)
		args = append(args, f.InitiativeID)
	}
	return where, args
}

func (s *Store) EmbeddedChunks(ctx context.Context, model string, f store.ChunkFilter) ([]store.Chunk, error) {
	where, args := chunkWhere(f)
	where = append([]string{`c.embedding IS NOT NULL`, `c.model = ?`}, where...)
	args = append([]any{model}, args...)
	rows, err := s.q.QueryContext(ctx,
		`SELECT `+chunkCols+` FROM chunks c WHERE `+strings.Join(where, ` AND `)+` ORDER BY c.id`, args...)
	if err != nil {
		return nil, mapErr(err, "loading embedded chunks")
	}
	return collect(rows, scanChunk)
}

// ftsQuery turns free text into an FTS5 query matching any of its words.
// Each word is quoted so user input cannot inject FTS5 syntax.
func ftsQuery(text string) string {
	words := strings.FieldsFunc(text, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
	quoted := make([]string, 0, len(words))
	for _, w := range words {
		quoted = append(quoted, `"`+w+`"`)
	}
	return strings.Join(quoted, " OR ")
}

func (s *Store) KeywordSearch(ctx context.Context, query string, f store.ChunkFilter, limit int) ([]store.KeywordHit, error) {
	match := ftsQuery(query)
	if match == "" {
		return nil, nil
	}
	if limit <= 0 {
		limit = -1
	}
	where, args := chunkWhere(f)
	where = append([]string{`chunks_fts MATCH ?`}, where...)
	args = append([]any{match}, args...)
	args = append(args, limit)
	rows, err := s.q.QueryContext(ctx,
		`SELECT `+chunkCols+`, bm25(chunks_fts) AS rank
		 FROM chunks_fts JOIN chunks c ON c.id = chunks_fts.rowid
		 WHERE `+strings.Join(where, ` AND `)+`
		 ORDER BY rank LIMIT ?`, args...)
	if err != nil {
		return nil, mapErr(err, "keyword search")
	}
	return collect(rows, func(sc scanner) (store.KeywordHit, error) {
		var h store.KeywordHit
		var kind string
		var initiative sql.NullInt64
		var blob []byte
		var rank float64
		if err := sc.Scan(&h.Chunk.ID, &kind, &h.Chunk.Owner.ID, &initiative, &h.Chunk.Seq, &h.Chunk.Text,
			&h.Chunk.ContentHash, &h.Chunk.Model, &blob, &rank); err != nil {
			return h, err
		}
		h.Chunk.Owner.Kind = domain.Kind(kind)
		h.Chunk.InitiativeID = initiative.Int64
		// bm25 is lower-is-better; negate so higher is better like cosine.
		h.Score = -rank
		return h, nil
	})
}
