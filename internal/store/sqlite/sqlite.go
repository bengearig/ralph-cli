// Package sqlite implements store.Store on SQLite via the pure-Go
// modernc.org/sqlite driver.
package sqlite

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"

	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"

	"github.com/bengearig/ralph/internal/store"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

type querier interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// Store is a SQLite-backed store.Store.
type Store struct {
	db   *sql.DB
	path string
	q    querier
	tx   *sql.Tx
	now  func() time.Time
}

var _ store.Store = (*Store)(nil)

// Open opens (creating if needed) the database at path and applies pending
// migrations.
func Open(ctx context.Context, path string) (*Store, error) {
	if strings.ContainsAny(path, "?#") {
		return nil, fmt.Errorf("database path %q must not contain '?' or '#'", path)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("creating database directory: %w", err)
	}
	dsn := "file:" + path +
		"?_pragma=foreign_keys(1)" +
		"&_pragma=busy_timeout(5000)" +
		"&_pragma=journal_mode(WAL)" +
		"&_pragma=synchronous(NORMAL)" +
		"&_txlock=immediate"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("opening database: %w", err)
	}
	// One connection per process serializes writers in-process; WAL and
	// busy_timeout handle concurrency with other ralph processes.
	db.SetMaxOpenConns(1)
	s := &Store{db: db, path: path, q: db, now: func() time.Time { return time.Now().UTC() }}
	if err := s.migrate(ctx); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

// Close releases the database.
func (s *Store) Close() error {
	if s.tx != nil {
		return errors.New("Close called inside a transaction")
	}
	return s.db.Close()
}

// InTx runs fn in a transaction. Nested calls reuse the outer transaction.
func (s *Store) InTx(ctx context.Context, fn func(store.Store) error) error {
	if s.tx != nil {
		return fn(s)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("beginning transaction: %w", err)
	}
	inner := &Store{db: s.db, path: s.path, q: tx, tx: tx, now: s.now}
	if err := fn(inner); err != nil {
		if rbErr := tx.Rollback(); rbErr != nil {
			return errors.Join(err, fmt.Errorf("rolling back: %w", rbErr))
		}
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("committing transaction: %w", err)
	}
	return nil
}

func (s *Store) inTx(ctx context.Context, fn func(*Store) error) error {
	return s.InTx(ctx, func(st store.Store) error { return fn(st.(*Store)) })
}

const rebuildMarker = "-- ralph:rebuild"

type migration struct {
	version int
	name    string
	sql     string
	rebuild bool
}

func loadMigrations() ([]migration, error) { return loadMigrationsFS(migrationFS) }

func loadMigrationsFS(fsys fs.FS) ([]migration, error) {
	entries, err := fs.ReadDir(fsys, "migrations")
	if err != nil {
		return nil, err
	}
	var ms []migration
	seen := map[int]string{}
	for _, e := range entries {
		name := e.Name()
		prefix, _, ok := strings.Cut(name, "_")
		v, err := strconv.Atoi(prefix)
		if !ok || err != nil || v < 1 {
			return nil, fmt.Errorf("migration %q must be named NNNN_description.sql", name)
		}
		if prev, dup := seen[v]; dup {
			return nil, fmt.Errorf("migrations %q and %q share version %d", prev, name, v)
		}
		seen[v] = name
		body, err := fs.ReadFile(fsys, "migrations/"+name)
		if err != nil {
			return nil, err
		}
		first, _, _ := strings.Cut(string(body), "\n")
		ms = append(ms, migration{version: v, name: name, sql: string(body), rebuild: first == rebuildMarker})
	}
	sort.Slice(ms, func(i, j int) bool { return ms[i].version < ms[j].version })
	return ms, nil
}

func (s *Store) migrate(ctx context.Context) error { return s.migrateFS(ctx, migrationFS) }

func (s *Store) migrateFS(ctx context.Context, fsys fs.FS) error {
	ms, err := loadMigrationsFS(fsys)
	if err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version    INTEGER PRIMARY KEY,
		applied_at TEXT NOT NULL
	)`); err != nil {
		return fmt.Errorf("creating schema_migrations: %w", err)
	}
	for _, m := range ms {
		var err error
		if m.rebuild {
			err = s.applyRebuild(ctx, m)
		} else {
			err = s.applyNormal(ctx, m)
		}
		if err != nil {
			return fmt.Errorf("applying migration %s: %w", m.name, err)
		}
	}
	return nil
}

func (s *Store) applyNormal(ctx context.Context, m migration) error {
	return s.inTx(ctx, func(tx *Store) error {
		var n int
		if err := tx.q.QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_migrations WHERE version = ?`, m.version).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			return nil
		}
		if _, err := tx.q.ExecContext(ctx, m.sql); err != nil {
			return err
		}
		_, err := tx.q.ExecContext(ctx, `INSERT INTO schema_migrations (version, applied_at) VALUES (?, ?)`, m.version, formatTime(tx.now()))
		return err
	})
}

type snapshot struct {
	Counts   map[string]int
	Embedded int
	FTS      int
	Names    []string
}

func takeSnapshot(ctx context.Context, conn *sql.Conn) (snapshot, error) {
	snap := snapshot{Counts: map[string]int{}}
	for _, t := range []string{"initiatives", "milestones", "tasks", "task_notes", "documents", "chunks"} {
		var n int
		if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+t).Scan(&n); err != nil {
			return snap, fmt.Errorf("counting %s: %w", t, err)
		}
		snap.Counts[t] = n
	}
	if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM chunks WHERE embedding IS NOT NULL`).Scan(&snap.Embedded); err != nil {
		return snap, fmt.Errorf("counting embedded chunks: %w", err)
	}
	if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM chunks_fts`).Scan(&snap.FTS); err != nil {
		return snap, fmt.Errorf("counting chunks_fts: %w", err)
	}
	rows, err := conn.QueryContext(ctx, `SELECT name FROM sqlite_master WHERE type IN ('trigger', 'index') ORDER BY name`)
	if err != nil {
		return snap, fmt.Errorf("listing triggers and indexes: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return snap, err
		}
		snap.Names = append(snap.Names, name)
	}
	return snap, rows.Err()
}

func foreignKeys(ctx context.Context, conn *sql.Conn) (int, error) {
	var v int
	err := conn.QueryRowContext(ctx, `PRAGMA foreign_keys`).Scan(&v)
	return v, err
}

func setForeignKeys(ctx context.Context, conn *sql.Conn, on bool) error {
	want := 0
	if on {
		want = 1
	}
	if _, err := conn.ExecContext(ctx, fmt.Sprintf(`PRAGMA foreign_keys=%d`, want)); err != nil {
		return err
	}
	got, err := foreignKeys(ctx, conn)
	if err != nil {
		return err
	}
	if got != want {
		return fmt.Errorf("PRAGMA foreign_keys reads %d, want %d", got, want)
	}
	return nil
}

// applyRebuild runs a migration that redefines tables with foreign keys off,
// guarded by a backup, a before/after snapshot and a foreign key check.
func (s *Store) applyRebuild(ctx context.Context, m migration) error {
	var applied int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_migrations WHERE version = ?`, m.version).Scan(&applied); err != nil {
		return err
	}
	if applied > 0 {
		return nil
	}

	backup := fmt.Sprintf("%s.pre-%d-%s.bak", s.path, m.version, s.now().UTC().Format("20060102T150405Z"))
	if _, err := s.db.ExecContext(ctx, `VACUUM INTO '`+strings.ReplaceAll(backup, "'", "''")+`'`); err != nil {
		return fmt.Errorf("backing up database to %s: %w", backup, err)
	}
	fail := func(check string, cause error) error {
		return fmt.Errorf("rebuild check %q failed: %w (backup: %s)", check, cause, backup)
	}
	if err := s.rebuildOnConn(ctx, m, fail); err != nil {
		return err
	}
	var res string
	if err := s.db.QueryRowContext(ctx, `PRAGMA quick_check`).Scan(&res); err != nil {
		return fail("quick_check", err)
	}
	if res != "ok" {
		return fail("quick_check", errors.New(res))
	}
	return nil
}

func (s *Store) rebuildOnConn(ctx context.Context, m migration, fail func(string, error) error) (err error) {
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return fail("connection", err)
	}
	defer conn.Close()
	if err := setForeignKeys(ctx, conn, false); err != nil {
		// Never return a connection with foreign keys in an unknown state to the pool.
		return errors.Join(fail("foreign_keys off", err), discard(conn))
	}

	inTx := false
	defer func() {
		cleanup := context.WithoutCancel(ctx)
		if inTx {
			if _, rbErr := conn.ExecContext(cleanup, `ROLLBACK`); rbErr != nil {
				err = errors.Join(err, fail("rollback", rbErr), discard(conn))
			}
		}
		if fkErr := setForeignKeys(cleanup, conn, true); fkErr != nil {
			err = errors.Join(err, fail("foreign_keys on", fkErr), discard(conn))
		}
	}()

	before, err := takeSnapshot(ctx, conn)
	if err != nil {
		return fail("snapshot", err)
	}
	if _, err := conn.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		return fail("begin", err)
	}
	inTx = true
	if _, err := conn.ExecContext(ctx, m.sql); err != nil {
		return fail("migration", err)
	}
	if err := foreignKeyCheck(ctx, conn); err != nil {
		return fail("foreign_key_check", err)
	}
	after, err := takeSnapshot(ctx, conn)
	if err != nil {
		return fail("snapshot", err)
	}
	if !reflect.DeepEqual(before, after) {
		return fail("snapshot", fmt.Errorf("changed from %+v to %+v", before, after))
	}
	if _, err := conn.ExecContext(ctx, `INSERT INTO schema_migrations (version, applied_at) VALUES (?, ?)`, m.version, formatTime(s.now())); err != nil {
		return fail("record migration", err)
	}
	if _, err := conn.ExecContext(ctx, `COMMIT`); err != nil {
		return fail("commit", err)
	}
	inTx = false

	return nil
}

func foreignKeyCheck(ctx context.Context, conn *sql.Conn) error {
	rows, err := conn.QueryContext(ctx, `PRAGMA foreign_key_check`)
	if err != nil {
		return err
	}
	defer rows.Close()
	if rows.Next() {
		var table, parent string
		var rowid, fkid sql.NullInt64
		if err := rows.Scan(&table, &rowid, &parent, &fkid); err != nil {
			return err
		}
		return fmt.Errorf("table %s row %d references missing %s", table, rowid.Int64, parent)
	}
	return rows.Err()
}

func discard(conn *sql.Conn) error {
	return conn.Raw(func(any) error { return driver.ErrBadConn })
}

// SchemaVersion returns the highest applied migration.
func (s *Store) SchemaVersion(ctx context.Context) (int, error) {
	var v sql.NullInt64
	err := s.q.QueryRowContext(ctx, `SELECT MAX(version) FROM schema_migrations`).Scan(&v)
	return int(v.Int64), err
}

func formatTime(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

func parseTime(s string) (time.Time, error) { return time.Parse(time.RFC3339Nano, s) }

// mapErr translates driver errors into store sentinels.
func mapErr(err error, what string) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%s: %w", what, store.ErrNotFound)
	}
	var se *sqlite.Error
	if errors.As(err, &se) {
		switch se.Code() {
		case sqlite3.SQLITE_CONSTRAINT_UNIQUE, sqlite3.SQLITE_CONSTRAINT_PRIMARYKEY:
			return fmt.Errorf("%s: %w", what, store.ErrConflict)
		case sqlite3.SQLITE_CONSTRAINT_FOREIGNKEY:
			return fmt.Errorf("%s: referenced parent: %w", what, store.ErrNotFound)
		}
	}
	return fmt.Errorf("%s: %w", what, err)
}

func checkAffected(res sql.Result, what string) error {
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("%s: %w", what, store.ErrNotFound)
	}
	return nil
}

func placeholders(n int) string {
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}
