package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/bengearig/ralph/internal/domain"
	"github.com/bengearig/ralph/internal/store"
)

type scanner interface{ Scan(dest ...any) error }

// ---- initiatives ----

const archivedInitiativeIDs = `SELECT id FROM initiatives WHERE archived_at <> ''`

const initiativeCols = `id, slug, title, brief, created_at, updated_at, archived_at, parent_card`

func scanInitiative(sc scanner) (domain.Initiative, error) {
	var i domain.Initiative
	var created, updated, archived string
	if err := sc.Scan(&i.ID, &i.Slug, &i.Title, &i.Brief, &created, &updated, &archived, &i.ParentCard); err != nil {
		return i, err
	}
	var err error
	if i.CreatedAt, err = parseTime(created); err != nil {
		return i, err
	}
	if i.UpdatedAt, err = parseTime(updated); err != nil {
		return i, err
	}
	i.ArchivedAt, err = parseArchivedAt(archived)
	return i, err
}

func parseArchivedAt(s string) (time.Time, error) {
	if s == "" {
		return time.Time{}, nil
	}
	return parseTime(s)
}

func formatArchivedAt(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return formatTime(t)
}

// SetInitiativeArchived sets or, for a zero time, clears archived_at without touching updated_at.
func (s *Store) SetInitiativeArchived(ctx context.Context, id int64, at time.Time) error {
	return s.setArchived(ctx, "initiatives", domain.KindInitiative, id, at)
}

// SetDocumentArchived sets or, for a zero time, clears archived_at without touching updated_at.
func (s *Store) SetDocumentArchived(ctx context.Context, id int64, at time.Time) error {
	return s.setArchived(ctx, "documents", domain.KindDocument, id, at)
}

func (s *Store) setArchived(ctx context.Context, table string, kind domain.Kind, id int64, at time.Time) error {
	what := domain.FormatID(kind, id)
	res, err := s.q.ExecContext(ctx, `UPDATE `+table+` SET archived_at = ? WHERE id = ?`, formatArchivedAt(at), id)
	if err != nil {
		return mapErr(err, "archiving "+what)
	}
	return checkAffected(res, what)
}

func (s *Store) CreateInitiative(ctx context.Context, i *domain.Initiative) error {
	now := s.now()
	res, err := s.q.ExecContext(ctx,
		`INSERT INTO initiatives (slug, title, brief, parent_card, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)`,
		i.Slug, i.Title, i.Brief, i.ParentCard, formatTime(now), formatTime(now))
	if err != nil {
		return mapErr(err, "creating initiative "+i.Slug)
	}
	if i.ID, err = res.LastInsertId(); err != nil {
		return err
	}
	i.CreatedAt, i.UpdatedAt = now, now
	return nil
}

func (s *Store) GetInitiative(ctx context.Context, id int64) (domain.Initiative, error) {
	i, err := scanInitiative(s.q.QueryRowContext(ctx, `SELECT `+initiativeCols+` FROM initiatives WHERE id = ?`, id))
	return i, mapErr(err, domain.FormatID(domain.KindInitiative, id))
}

func (s *Store) ListInitiatives(ctx context.Context, excludeArchived bool) ([]domain.Initiative, error) {
	q := `SELECT ` + initiativeCols + ` FROM initiatives`
	if excludeArchived {
		q += ` WHERE archived_at = ''`
	}
	rows, err := s.q.QueryContext(ctx, q+` ORDER BY id`)
	if err != nil {
		return nil, mapErr(err, "listing initiatives")
	}
	return collect(rows, scanInitiative)
}

func (s *Store) UpdateInitiative(ctx context.Context, i *domain.Initiative) error {
	now := s.now()
	res, err := s.q.ExecContext(ctx,
		`UPDATE initiatives SET slug = ?, title = ?, brief = ?, parent_card = ?, updated_at = ? WHERE id = ?`,
		i.Slug, i.Title, i.Brief, i.ParentCard, formatTime(now), i.ID)
	what := domain.FormatID(domain.KindInitiative, i.ID)
	if err != nil {
		return mapErr(err, "updating "+what)
	}
	if err := checkAffected(res, what); err != nil {
		return err
	}
	i.UpdatedAt = now
	return nil
}

func (s *Store) DeleteInitiative(ctx context.Context, id int64) error {
	return s.deleteWithCards(ctx, "initiatives", domain.KindInitiative, id,
		cardsOf(domain.KindTask, `SELECT t.id FROM tasks t JOIN milestones m ON m.id = t.milestone_id WHERE m.initiative_id = ?`),
		cardsOf(domain.KindMilestone, `SELECT id FROM milestones WHERE initiative_id = ?`),
		cardsOf(domain.KindInitiative, `SELECT ?`))
}

// ---- milestones ----

const milestoneCols = `id, initiative_id, slug, title, description, position, branch, base_commit, head_commit,
	pr_provider, pr_namespace, pr_repository, pr_number, created_at, updated_at`

func scanMilestone(sc scanner) (domain.Milestone, error) {
	var m domain.Milestone
	var created, updated string
	if err := sc.Scan(&m.ID, &m.InitiativeID, &m.Slug, &m.Title, &m.Description, &m.Position,
		&m.Git.Branch, &m.Git.BaseCommit, &m.Git.HeadCommit,
		&m.PullRequest.Provider, &m.PullRequest.Namespace, &m.PullRequest.Repository, &m.PullRequest.Number, &created, &updated); err != nil {
		return m, err
	}
	var err error
	if m.CreatedAt, err = parseTime(created); err != nil {
		return m, err
	}
	m.UpdatedAt, err = parseTime(updated)
	return m, err
}

func (s *Store) CreateMilestone(ctx context.Context, m *domain.Milestone) error {
	return s.inTx(ctx, func(tx *Store) error {
		if m.Position <= 0 {
			if err := tx.q.QueryRowContext(ctx,
				`SELECT COALESCE(MAX(position), 0) + 1 FROM milestones WHERE initiative_id = ?`, m.InitiativeID,
			).Scan(&m.Position); err != nil {
				return err
			}
		}
		now := tx.now()
		res, err := tx.q.ExecContext(ctx,
			`INSERT INTO milestones (initiative_id, slug, title, description, position, created_at, updated_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?)`,
			m.InitiativeID, m.Slug, m.Title, m.Description, m.Position, formatTime(now), formatTime(now))
		if err != nil {
			return mapErr(err, "creating milestone "+m.Slug)
		}
		if m.ID, err = res.LastInsertId(); err != nil {
			return err
		}
		m.CreatedAt, m.UpdatedAt = now, now
		return nil
	})
}

func (s *Store) GetMilestone(ctx context.Context, id int64) (domain.Milestone, error) {
	m, err := scanMilestone(s.q.QueryRowContext(ctx, `SELECT `+milestoneCols+` FROM milestones WHERE id = ?`, id))
	return m, mapErr(err, domain.FormatID(domain.KindMilestone, id))
}

func (s *Store) ListMilestones(ctx context.Context, initiativeID int64, excludeArchived bool) ([]domain.Milestone, error) {
	q := `SELECT ` + milestoneCols + ` FROM milestones`
	var where []string
	var args []any
	if initiativeID != 0 {
		where = append(where, `initiative_id = ?`)
		args = append(args, initiativeID)
	}
	if excludeArchived {
		where = append(where, `initiative_id NOT IN (`+archivedInitiativeIDs+`)`)
	}
	if len(where) > 0 {
		q += ` WHERE ` + strings.Join(where, ` AND `)
	}
	q += ` ORDER BY initiative_id, position, id`
	rows, err := s.q.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, mapErr(err, "listing milestones")
	}
	return collect(rows, scanMilestone)
}

func (s *Store) UpdateMilestone(ctx context.Context, m *domain.Milestone) error {
	now := s.now()
	res, err := s.q.ExecContext(ctx,
		`UPDATE milestones SET slug = ?, title = ?, description = ?, position = ?, updated_at = ? WHERE id = ?`,
		m.Slug, m.Title, m.Description, m.Position, formatTime(now), m.ID)
	what := domain.FormatID(domain.KindMilestone, m.ID)
	if err != nil {
		return mapErr(err, "updating "+what)
	}
	if err := checkAffected(res, what); err != nil {
		return err
	}
	m.UpdatedAt = now
	return nil
}

func (s *Store) SetMilestoneGit(ctx context.Context, id int64, g domain.MilestoneGit) error {
	res, err := s.q.ExecContext(ctx,
		`UPDATE milestones SET branch = ?, base_commit = ?, head_commit = ? WHERE id = ?`,
		g.Branch, g.BaseCommit, g.HeadCommit, id)
	what := domain.FormatID(domain.KindMilestone, id)
	if err != nil {
		return mapErr(err, "recording git state of "+what)
	}
	return checkAffected(res, what)
}

func (s *Store) SetMilestonePullRequest(ctx context.Context, id int64, rec domain.PullRequestRecord) error {
	res, err := s.q.ExecContext(ctx,
		`UPDATE milestones SET pr_provider = ?, pr_namespace = ?, pr_repository = ?, pr_number = ? WHERE id = ?`,
		rec.Provider, rec.Namespace, rec.Repository, rec.Number, id)
	what := domain.FormatID(domain.KindMilestone, id)
	if err != nil {
		return mapErr(err, "recording pull request of "+what)
	}
	return checkAffected(res, what)
}

func (s *Store) SetMilestoneDependencies(ctx context.Context, milestoneID int64, dependsOn []int64) error {
	what := "setting dependencies of " + domain.FormatID(domain.KindMilestone, milestoneID)
	return s.inTx(ctx, func(tx *Store) error {
		if _, err := tx.q.ExecContext(ctx, `DELETE FROM milestone_dependencies WHERE milestone_id = ?`, milestoneID); err != nil {
			return mapErr(err, what)
		}
		now := formatTime(tx.now())
		for _, dep := range dependsOn {
			if _, err := tx.q.ExecContext(ctx,
				`INSERT INTO milestone_dependencies (milestone_id, depends_on_id, created_at) VALUES (?, ?, ?)`,
				milestoneID, dep, now); err != nil {
				return mapErr(err, what)
			}
		}
		return nil
	})
}

func (s *Store) ListMilestoneDependencies(ctx context.Context, initiativeID int64) (map[int64][]int64, error) {
	q := `SELECT d.milestone_id, d.depends_on_id FROM milestone_dependencies d`
	var args []any
	if initiativeID != 0 {
		q += ` JOIN milestones m ON m.id = d.milestone_id WHERE m.initiative_id = ?`
		args = append(args, initiativeID)
	}
	q += ` ORDER BY d.milestone_id, d.depends_on_id`
	rows, err := s.q.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, mapErr(err, "listing milestone dependencies")
	}
	defer rows.Close()
	out := map[int64][]int64{}
	for rows.Next() {
		var from, to int64
		if err := rows.Scan(&from, &to); err != nil {
			return nil, fmt.Errorf("scanning row: %w", err)
		}
		out[from] = append(out[from], to)
	}
	return out, rows.Err()
}

func (s *Store) ListDependents(ctx context.Context, milestoneID int64) ([]int64, error) {
	rows, err := s.q.QueryContext(ctx,
		`SELECT milestone_id FROM milestone_dependencies WHERE depends_on_id = ? ORDER BY milestone_id`, milestoneID)
	if err != nil {
		return nil, mapErr(err, "listing dependents of "+domain.FormatID(domain.KindMilestone, milestoneID))
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scanning row: %w", err)
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

func (s *Store) DeleteMilestone(ctx context.Context, id int64) error {
	return s.deleteWithCards(ctx, "milestones", domain.KindMilestone, id,
		cardsOf(domain.KindTask, `SELECT id FROM tasks WHERE milestone_id = ?`),
		cardsOf(domain.KindMilestone, `SELECT ?`))
}

// ---- tasks ----

const taskCols = `t.id, t.milestone_id, t.title, t.description, t.acceptance_criteria, t.status, t.position,
	t.failure_count, t.blocked_reason, t.commit_hash, t.created_at, t.updated_at`

func scanTask(sc scanner) (domain.Task, error) {
	var t domain.Task
	var status, created, updated string
	if err := sc.Scan(&t.ID, &t.MilestoneID, &t.Title, &t.Description, &t.AcceptanceCriteria, &status, &t.Position,
		&t.FailureCount, &t.BlockedReason, &t.Commit, &created, &updated); err != nil {
		return t, err
	}
	t.Status = domain.Status(status)
	var err error
	if t.CreatedAt, err = parseTime(created); err != nil {
		return t, err
	}
	t.UpdatedAt, err = parseTime(updated)
	return t, err
}

func (s *Store) CreateTask(ctx context.Context, t *domain.Task) error {
	return s.inTx(ctx, func(tx *Store) error {
		if t.Position <= 0 {
			if err := tx.q.QueryRowContext(ctx,
				`SELECT COALESCE(MAX(position), 0) + 1 FROM tasks WHERE milestone_id = ?`, t.MilestoneID,
			).Scan(&t.Position); err != nil {
				return err
			}
		}
		now := tx.now()
		res, err := tx.q.ExecContext(ctx,
			`INSERT INTO tasks (milestone_id, title, description, acceptance_criteria, status, position,
			                    failure_count, blocked_reason, created_at, updated_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			t.MilestoneID, t.Title, t.Description, t.AcceptanceCriteria, string(t.Status), t.Position,
			t.FailureCount, t.BlockedReason, formatTime(now), formatTime(now))
		if err != nil {
			return mapErr(err, "creating task")
		}
		if t.ID, err = res.LastInsertId(); err != nil {
			return err
		}
		t.CreatedAt, t.UpdatedAt = now, now
		return nil
	})
}

func (s *Store) GetTask(ctx context.Context, id int64) (domain.Task, error) {
	t, err := scanTask(s.q.QueryRowContext(ctx, `SELECT `+taskCols+` FROM tasks t WHERE t.id = ?`, id))
	return t, mapErr(err, domain.FormatID(domain.KindTask, id))
}

func (s *Store) ListTasks(ctx context.Context, f store.TaskFilter) ([]domain.Task, error) {
	var where []string
	var args []any
	if f.MilestoneID != 0 {
		where = append(where, `t.milestone_id = ?`)
		args = append(args, f.MilestoneID)
	}
	if f.InitiativeID != 0 {
		where = append(where, `m.initiative_id = ?`)
		args = append(args, f.InitiativeID)
	}
	if len(f.Statuses) > 0 {
		where = append(where, `t.status IN (`+placeholders(len(f.Statuses))+`)`)
		for _, st := range f.Statuses {
			args = append(args, string(st))
		}
	}
	if f.ExcludeArchived {
		where = append(where, `m.initiative_id NOT IN (`+archivedInitiativeIDs+`)`)
	}
	q := `SELECT ` + taskCols + ` FROM tasks t JOIN milestones m ON m.id = t.milestone_id`
	if len(where) > 0 {
		q += ` WHERE ` + strings.Join(where, ` AND `)
	}
	q += ` ORDER BY m.initiative_id, m.position, m.id, t.position, t.id`
	rows, err := s.q.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, mapErr(err, "listing tasks")
	}
	return collect(rows, scanTask)
}

func (s *Store) UpdateTask(ctx context.Context, t *domain.Task) error {
	now := s.now()
	res, err := s.q.ExecContext(ctx,
		`UPDATE tasks SET title = ?, description = ?, acceptance_criteria = ?, status = ?, position = ?,
		                  failure_count = ?, blocked_reason = ?, updated_at = ?
		 WHERE id = ?`,
		t.Title, t.Description, t.AcceptanceCriteria, string(t.Status), t.Position,
		t.FailureCount, t.BlockedReason, formatTime(now), t.ID)
	what := domain.FormatID(domain.KindTask, t.ID)
	if err != nil {
		return mapErr(err, "updating "+what)
	}
	if err := checkAffected(res, what); err != nil {
		return err
	}
	t.UpdatedAt = now
	return nil
}

func (s *Store) SetTaskCommit(ctx context.Context, id int64, hash string) error {
	res, err := s.q.ExecContext(ctx, `UPDATE tasks SET commit_hash = ? WHERE id = ?`, hash, id)
	what := domain.FormatID(domain.KindTask, id)
	if err != nil {
		return mapErr(err, "recording commit of "+what)
	}
	return checkAffected(res, what)
}

func (s *Store) AddTaskNote(ctx context.Context, n *domain.TaskNote) error {
	now := s.now()
	res, err := s.q.ExecContext(ctx,
		`INSERT INTO task_notes (task_id, kind, body, created_at) VALUES (?, ?, ?, ?)`,
		n.TaskID, string(n.Kind), n.Body, formatTime(now))
	if err != nil {
		return mapErr(err, "adding note to "+domain.FormatID(domain.KindTask, n.TaskID))
	}
	if n.ID, err = res.LastInsertId(); err != nil {
		return err
	}
	n.CreatedAt = now
	return nil
}

func (s *Store) ListTaskNotes(ctx context.Context, taskID int64) ([]domain.TaskNote, error) {
	rows, err := s.q.QueryContext(ctx,
		`SELECT id, task_id, kind, body, created_at FROM task_notes WHERE task_id = ? ORDER BY id`, taskID)
	if err != nil {
		return nil, mapErr(err, "listing notes")
	}
	return collect(rows, func(sc scanner) (domain.TaskNote, error) {
		var n domain.TaskNote
		var kind, created string
		if err := sc.Scan(&n.ID, &n.TaskID, &kind, &n.Body, &created); err != nil {
			return n, err
		}
		n.Kind = domain.NoteKind(kind)
		var err error
		n.CreatedAt, err = parseTime(created)
		return n, err
	})
}

func (s *Store) GetTaskNote(ctx context.Context, id int64) (domain.TaskNote, error) {
	var n domain.TaskNote
	var kind, created string
	err := s.q.QueryRowContext(ctx,
		`SELECT id, task_id, kind, body, created_at FROM task_notes WHERE id = ?`, id).
		Scan(&n.ID, &n.TaskID, &kind, &n.Body, &created)
	if err != nil {
		return n, mapErr(err, domain.FormatID(domain.KindNote, id))
	}
	n.Kind = domain.NoteKind(kind)
	n.CreatedAt, err = parseTime(created)
	return n, err
}

func (s *Store) DeleteTaskNote(ctx context.Context, id int64) error {
	return s.deleteByID(ctx, "task_notes", domain.KindNote, id)
}

// ---- documents ----

const documentCols = `id, initiative_id, kind, slug, title, body, created_at, updated_at, archived_at, origin_initiative_id`

func scanDocument(sc scanner) (domain.Document, error) {
	var d domain.Document
	var initiative, origin sql.NullInt64
	var kind, created, updated, archived string
	if err := sc.Scan(&d.ID, &initiative, &kind, &d.Slug, &d.Title, &d.Body, &created, &updated, &archived, &origin); err != nil {
		return d, err
	}
	if initiative.Valid {
		v := initiative.Int64
		d.InitiativeID = &v
	}
	if origin.Valid {
		v := origin.Int64
		d.OriginInitiativeID = &v
	}
	d.Kind = domain.DocKind(kind)
	var err error
	if d.CreatedAt, err = parseTime(created); err != nil {
		return d, err
	}
	if d.UpdatedAt, err = parseTime(updated); err != nil {
		return d, err
	}
	d.ArchivedAt, err = parseArchivedAt(archived)
	return d, err
}

func nullID(p *int64) sql.NullInt64 {
	if p == nil {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: *p, Valid: true}
}

func (s *Store) CreateDocument(ctx context.Context, d *domain.Document) error {
	now := s.now()
	res, err := s.q.ExecContext(ctx,
		`INSERT INTO documents (initiative_id, kind, slug, title, body, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		nullID(d.InitiativeID), string(d.Kind), d.Slug, d.Title, d.Body, formatTime(now), formatTime(now))
	if err != nil {
		return mapErr(err, "creating document "+d.Slug)
	}
	if d.ID, err = res.LastInsertId(); err != nil {
		return err
	}
	d.CreatedAt, d.UpdatedAt = now, now
	return nil
}

func (s *Store) GetDocument(ctx context.Context, id int64) (domain.Document, error) {
	d, err := scanDocument(s.q.QueryRowContext(ctx, `SELECT `+documentCols+` FROM documents WHERE id = ?`, id))
	return d, mapErr(err, domain.FormatID(domain.KindDocument, id))
}

func (s *Store) ListDocuments(ctx context.Context, f store.DocumentFilter) ([]domain.Document, error) {
	var where []string
	var args []any
	switch {
	case f.ProjectOnly:
		where = append(where, `initiative_id IS NULL`)
	case f.InitiativeID != 0:
		where = append(where, `initiative_id = ?`)
		args = append(args, f.InitiativeID)
	}
	if f.Kind != "" {
		where = append(where, `kind = ?`)
		args = append(args, string(f.Kind))
	}
	if f.ExcludeArchived {
		where = append(where, `archived_at = ''`, `(initiative_id IS NULL OR initiative_id NOT IN (`+archivedInitiativeIDs+`))`)
	}
	q := `SELECT ` + documentCols + ` FROM documents`
	if len(where) > 0 {
		q += ` WHERE ` + strings.Join(where, ` AND `)
	}
	q += ` ORDER BY IFNULL(initiative_id, 0), id`
	rows, err := s.q.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, mapErr(err, "listing documents")
	}
	return collect(rows, scanDocument)
}

func (s *Store) UpdateDocument(ctx context.Context, d *domain.Document) error {
	now := s.now()
	res, err := s.q.ExecContext(ctx,
		`UPDATE documents SET kind = ?, slug = ?, title = ?, body = ?, updated_at = ? WHERE id = ?`,
		string(d.Kind), d.Slug, d.Title, d.Body, formatTime(now), d.ID)
	what := domain.FormatID(domain.KindDocument, d.ID)
	if err != nil {
		return mapErr(err, "updating "+what)
	}
	if err := checkAffected(res, what); err != nil {
		return err
	}
	d.UpdatedAt = now
	return nil
}

// SetDocumentScope moves a document between scopes in one statement; an empty slug keeps the current slug.
func (s *Store) SetDocumentScope(ctx context.Context, id int64, initiativeID, originInitiativeID *int64, slug string) error {
	what := domain.FormatID(domain.KindDocument, id)
	res, err := s.q.ExecContext(ctx,
		`UPDATE documents SET initiative_id = ?, origin_initiative_id = ?, slug = CASE WHEN ? = '' THEN slug ELSE ? END, updated_at = ? WHERE id = ?`,
		nullID(initiativeID), nullID(originInitiativeID), slug, slug, formatTime(s.now()), id)
	if err != nil {
		return mapErr(err, "moving "+what)
	}
	return checkAffected(res, what)
}

func collect[T any](rows *sql.Rows, scan func(scanner) (T, error)) ([]T, error) {
	defer rows.Close()
	var out []T
	for rows.Next() {
		v, err := scan(rows)
		if err != nil {
			return nil, fmt.Errorf("scanning row: %w", err)
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func (s *Store) DeleteTask(ctx context.Context, id int64) error {
	return s.deleteWithCards(ctx, "tasks", domain.KindTask, id, cardsOf(domain.KindTask, `SELECT ?`))
}

func (s *Store) DeleteDocument(ctx context.Context, id int64) error {
	return s.deleteByID(ctx, "documents", domain.KindDocument, id)
}

// cardScope selects card rows of one kind whose entity IDs come from a subquery taking the deleted ID.
type cardScope struct {
	kind domain.Kind
	ids  string
}

func cardsOf(kind domain.Kind, ids string) cardScope { return cardScope{kind, ids} }

// deleteWithCards removes the cards in scope, then the row, in one transaction. A missing row rolls it back.
func (s *Store) deleteWithCards(ctx context.Context, table string, kind domain.Kind, id int64, scopes ...cardScope) error {
	return s.inTx(ctx, func(tx *Store) error {
		for _, sc := range scopes {
			if _, err := tx.q.ExecContext(ctx,
				`DELETE FROM cards WHERE kind = ? AND entity_id IN (`+sc.ids+`)`, string(sc.kind), id); err != nil {
				return mapErr(err, "deleting cards of "+domain.FormatID(kind, id))
			}
		}
		return tx.deleteByID(ctx, table, kind, id)
	})
}

func (s *Store) deleteByID(ctx context.Context, table string, kind domain.Kind, id int64) error {
	res, err := s.q.ExecContext(ctx, `DELETE FROM `+table+` WHERE id = ?`, id)
	what := domain.FormatID(kind, id)
	if err != nil {
		return mapErr(err, "deleting "+what)
	}
	return checkAffected(res, what)
}

func (s *Store) CountCascade(ctx context.Context, kind domain.Kind, id int64) (store.Cascade, error) {
	what := domain.FormatID(kind, id)
	var (
		c     store.Cascade
		query string
		dest  []any
	)
	switch kind {
	case domain.KindInitiative:
		c.Initiatives = 1
		query = `SELECT
			(SELECT COUNT(*) FROM milestones WHERE initiative_id = ?1),
			(SELECT COUNT(*) FROM tasks t JOIN milestones m ON m.id = t.milestone_id WHERE m.initiative_id = ?1),
			(SELECT COUNT(*) FROM task_notes n JOIN tasks t ON t.id = n.task_id
				JOIN milestones m ON m.id = t.milestone_id WHERE m.initiative_id = ?1),
			(SELECT COUNT(*) FROM documents WHERE initiative_id = ?1)
			WHERE EXISTS (SELECT 1 FROM initiatives WHERE id = ?1)`
		dest = []any{&c.Milestones, &c.Tasks, &c.Notes, &c.Documents}
	case domain.KindMilestone:
		c.Milestones = 1
		query = `SELECT
			(SELECT COUNT(*) FROM tasks WHERE milestone_id = ?1),
			(SELECT COUNT(*) FROM task_notes n JOIN tasks t ON t.id = n.task_id WHERE t.milestone_id = ?1)
			WHERE EXISTS (SELECT 1 FROM milestones WHERE id = ?1)`
		dest = []any{&c.Tasks, &c.Notes}
	case domain.KindTask:
		c.Tasks = 1
		query = `SELECT (SELECT COUNT(*) FROM task_notes WHERE task_id = ?1)
			WHERE EXISTS (SELECT 1 FROM tasks WHERE id = ?1)`
		dest = []any{&c.Notes}
	default:
		return c, fmt.Errorf("counting cascade of %s: unsupported kind", kind)
	}
	err := s.q.QueryRowContext(ctx, query, id).Scan(dest...)
	if errors.Is(err, sql.ErrNoRows) {
		return store.Cascade{}, fmt.Errorf("%s: %w", what, store.ErrNotFound)
	}
	if err != nil {
		return store.Cascade{}, mapErr(err, "counting what deleting "+what+" removes")
	}
	return c, nil
}
