package sqlite

import (
	"context"

	"github.com/bengearig/ralph/internal/domain"
)

const importCols = `id, path, status, content, content_hash, size, last_done_hash, note, queued_at, processed_at`

func scanImport(sc scanner) (domain.Import, error) {
	var im domain.Import
	var status, queued, processed string
	if err := sc.Scan(&im.ID, &im.Path, &status, &im.Content, &im.ContentHash, &im.Size,
		&im.LastDoneHash, &im.Note, &queued, &processed); err != nil {
		return im, err
	}
	im.Status = domain.ImportStatus(status)
	var err error
	if im.QueuedAt, err = parseTime(queued); err != nil {
		return im, err
	}
	im.ProcessedAt, err = parseArchivedAt(processed)
	return im, err
}

func (s *Store) CreateImport(ctx context.Context, im *domain.Import) error {
	res, err := s.q.ExecContext(ctx,
		`INSERT INTO imports (path, status, content, content_hash, size, last_done_hash, note, queued_at, processed_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		im.Path, string(im.Status), im.Content, im.ContentHash, im.Size, im.LastDoneHash, im.Note,
		formatTime(im.QueuedAt), formatArchivedAt(im.ProcessedAt))
	if err != nil {
		return mapErr(err, "creating import "+im.Path)
	}
	im.ID, err = res.LastInsertId()
	return err
}

func (s *Store) GetImportByPath(ctx context.Context, path string) (domain.Import, error) {
	im, err := scanImport(s.q.QueryRowContext(ctx, `SELECT `+importCols+` FROM imports WHERE path = ?`, path))
	return im, mapErr(err, "import "+path)
}

// UpdateImport writes every column except id and path, addressing the row by path.
func (s *Store) UpdateImport(ctx context.Context, im *domain.Import) error {
	res, err := s.q.ExecContext(ctx,
		`UPDATE imports SET status = ?, content = ?, content_hash = ?, size = ?, last_done_hash = ?,
		note = ?, queued_at = ?, processed_at = ? WHERE path = ?`,
		string(im.Status), im.Content, im.ContentHash, im.Size, im.LastDoneHash, im.Note,
		formatTime(im.QueuedAt), formatArchivedAt(im.ProcessedAt), im.Path)
	what := "import " + im.Path
	if err != nil {
		return mapErr(err, "updating "+what)
	}
	return checkAffected(res, what)
}

func (s *Store) ListImports(ctx context.Context, statuses []domain.ImportStatus) ([]domain.Import, error) {
	q := `SELECT ` + importCols + ` FROM imports`
	var args []any
	if len(statuses) > 0 {
		q += ` WHERE status IN (` + placeholders(len(statuses)) + `)`
		for _, st := range statuses {
			args = append(args, string(st))
		}
	}
	q += ` ORDER BY path`
	rows, err := s.q.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, mapErr(err, "listing imports")
	}
	return collect(rows, scanImport)
}

// AddImportLink is a no-op when the link already exists.
func (s *Store) AddImportLink(ctx context.Context, importID, documentID int64, rel domain.ImportRelation) error {
	_, err := s.q.ExecContext(ctx,
		`INSERT INTO import_documents (import_id, document_id, relation, created_at) VALUES (?, ?, ?, ?)
		ON CONFLICT (import_id, document_id, relation) DO NOTHING`,
		importID, documentID, string(rel), formatTime(s.now()))
	return mapErr(err, "linking import to "+domain.FormatID(domain.KindDocument, documentID))
}

func (s *Store) ListImportLinks(ctx context.Context, importID int64) ([]domain.ImportLink, error) {
	rows, err := s.q.QueryContext(ctx,
		`SELECT document_id, relation, created_at FROM import_documents WHERE import_id = ?
		ORDER BY created_at, document_id, relation`, importID)
	if err != nil {
		return nil, mapErr(err, "listing import links")
	}
	return collect(rows, func(sc scanner) (domain.ImportLink, error) {
		var l domain.ImportLink
		var rel, created string
		if err := sc.Scan(&l.DocumentID, &rel, &created); err != nil {
			return l, err
		}
		l.Relation = domain.ImportRelation(rel)
		var err error
		l.CreatedAt, err = parseTime(created)
		return l, err
	})
}
