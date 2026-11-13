package sqlite

import (
	"context"
	"fmt"
	"time"

	"github.com/bengearig/ralph/internal/domain"
)

const cardCols = `kind, entity_id, provider, card_key, url, content_hash, last_error, attempted_at, synced_at, parent_key`

const cardWriteCols = `kind, entity_id, provider, card_key, url, content_hash, last_error, attempted_at, synced_at`

func scanCard(sc scanner) (domain.Card, error) {
	var c domain.Card
	var kind, attempted, synced string
	if err := sc.Scan(&kind, &c.EntityID, &c.Provider, &c.Key, &c.URL, &c.ContentHash, &c.LastError,
		&attempted, &synced, &c.ParentKey); err != nil {
		return c, err
	}
	c.Kind = domain.Kind(kind)
	var err error
	if c.AttemptedAt, err = parseArchivedAt(attempted); err != nil {
		return c, err
	}
	c.SyncedAt, err = parseArchivedAt(synced)
	return c, err
}

func (s *Store) GetCard(ctx context.Context, kind domain.Kind, entityID int64, provider string) (domain.Card, error) {
	c, err := scanCard(s.q.QueryRowContext(ctx,
		`SELECT `+cardCols+` FROM cards WHERE kind = ? AND entity_id = ? AND provider = ?`,
		string(kind), entityID, provider))
	return c, mapErr(err, fmt.Sprintf("%s card of %s", provider, domain.FormatID(kind, entityID)))
}

func (s *Store) SetCard(ctx context.Context, c *domain.Card) error {
	_, err := s.q.ExecContext(ctx,
		`INSERT INTO cards (`+cardWriteCols+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (kind, entity_id, provider) DO UPDATE SET card_key = excluded.card_key, url = excluded.url,
			content_hash = excluded.content_hash, last_error = excluded.last_error,
			attempted_at = excluded.attempted_at, synced_at = excluded.synced_at`,
		string(c.Kind), c.EntityID, c.Provider, c.Key, c.URL, c.ContentHash, c.LastError,
		formatArchivedAt(c.AttemptedAt), formatArchivedAt(c.SyncedAt))
	return mapErr(err, fmt.Sprintf("saving %s card of %s", c.Provider, domain.FormatID(c.Kind, c.EntityID)))
}

func (s *Store) SetCardParentKey(ctx context.Context, kind domain.Kind, entityID int64, provider, parentKey string) error {
	what := fmt.Sprintf("setting parent of %s card of %s", provider, domain.FormatID(kind, entityID))
	res, err := s.q.ExecContext(ctx,
		`UPDATE cards SET parent_key = ? WHERE kind = ? AND entity_id = ? AND provider = ?`,
		parentKey, string(kind), entityID, provider)
	if err != nil {
		return mapErr(err, what)
	}
	return checkAffected(res, what)
}

func (s *Store) ClaimCard(ctx context.Context, kind domain.Kind, entityID int64, provider string, staleAfter time.Duration) (bool, error) {
	now := time.Now().UTC()
	res, err := s.q.ExecContext(ctx,
		`INSERT INTO cards (kind, entity_id, provider, attempted_at) VALUES (?, ?, ?, ?)
		ON CONFLICT (kind, entity_id, provider) DO UPDATE SET attempted_at = excluded.attempted_at
		WHERE card_key = '' AND (attempted_at = '' OR julianday(attempted_at) < julianday(?))`,
		string(kind), entityID, provider, formatTime(now), formatTime(now.Add(-staleAfter)))
	if err != nil {
		return false, mapErr(err, fmt.Sprintf("claiming %s card of %s", provider, domain.FormatID(kind, entityID)))
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, mapErr(err, "claiming card")
	}
	return n > 0, nil
}

func (s *Store) ListCards(ctx context.Context, provider string) ([]domain.Card, error) {
	rows, err := s.q.QueryContext(ctx,
		`SELECT `+cardCols+` FROM cards WHERE provider = ? ORDER BY kind, entity_id`, provider)
	if err != nil {
		return nil, mapErr(err, "listing "+provider+" cards")
	}
	return collect(rows, scanCard)
}

func (s *Store) DeleteCards(ctx context.Context, kind domain.Kind, entityID int64) error {
	_, err := s.q.ExecContext(ctx, `DELETE FROM cards WHERE kind = ? AND entity_id = ?`, string(kind), entityID)
	return mapErr(err, "deleting cards of "+domain.FormatID(kind, entityID))
}
