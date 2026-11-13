package sqlite

import (
	"context"
	"fmt"
)

func (s *Store) GetMeta(ctx context.Context, key string) (string, error) {
	var v string
	err := s.q.QueryRowContext(ctx, `SELECT value FROM store_meta WHERE key = ?`, key).Scan(&v)
	return v, mapErr(err, "metadata "+key)
}

func (s *Store) SetMetaIfAbsent(ctx context.Context, key, value string) (string, error) {
	what := fmt.Sprintf("keeping metadata %s", key)
	var got string
	err := s.inTx(ctx, func(tx *Store) error {
		if _, err := tx.q.ExecContext(ctx,
			`INSERT INTO store_meta (key, value) VALUES (?, ?) ON CONFLICT (key) DO NOTHING`, key, value); err != nil {
			return mapErr(err, what)
		}
		var err error
		got, err = tx.GetMeta(ctx, key)
		return err
	})
	return got, err
}
