package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"time"

	msqlite "modernc.org/sqlite"
	sqlitelib "modernc.org/sqlite/lib"
)

// migrations[i] moves the schema from user_version i to i+1.
var migrations = [][]string{
	{
		`CREATE TABLE workspaces (
			id TEXT PRIMARY KEY,
			name TEXT NOT NULL UNIQUE,
			created_at TEXT NOT NULL
		)`,
		`CREATE TABLE connections (
			id TEXT PRIMARY KEY,
			workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
			name TEXT NOT NULL,
			engine TEXT NOT NULL,
			secret BLOB NOT NULL,
			secret_key_id INTEGER NOT NULL,
			host TEXT NOT NULL DEFAULT '',
			port INTEGER NOT NULL DEFAULT 0,
			"database" TEXT NOT NULL DEFAULT '',
			username TEXT NOT NULL DEFAULT '',
			description TEXT NOT NULL DEFAULT '',
			mode TEXT NOT NULL CHECK (mode IN ('ro', 'rw')),
			max_rows INTEGER NOT NULL,
			timeout_ms INTEGER NOT NULL,
			pool_size INTEGER NOT NULL,
			allowed_namespaces TEXT NOT NULL DEFAULT '[]',
			tls TEXT NOT NULL DEFAULT '{}',
			version INTEGER NOT NULL DEFAULT 1,
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL,
			UNIQUE (workspace_id, name)
		)`,
		`INSERT INTO workspaces (id, name, created_at)
		 VALUES ('default', 'default', strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))`,
	},
}

// SQLite is a Store backed by a SQLite file.
type SQLite struct {
	db  *sql.DB
	now func() time.Time
}

// OpenSQLite opens (creating if needed) the store at path and migrates it.
func OpenSQLite(path string) (*SQLite, error) {
	msqlite.StrictPragmas(true)
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o700); err != nil {
		return nil, err
	}
	u := url.URL{Scheme: "file", Path: abs, RawQuery: "_journal_mode=WAL&_busy_timeout=5000&_foreign_keys=1"}
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return nil, err
	}
	s := &SQLite{db: db, now: func() time.Time { return time.Now().UTC() }}
	if err := s.migrate(context.Background()); err != nil {
		db.Close()
		return nil, fmt.Errorf("store: migrate %s: %w", abs, err)
	}
	return s, nil
}

func (s *SQLite) migrate(ctx context.Context) error {
	var v int
	if err := s.db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&v); err != nil {
		return err
	}
	for i := v; i < len(migrations); i++ {
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		for _, stmt := range migrations[i] {
			if _, err := tx.ExecContext(ctx, stmt); err != nil {
				tx.Rollback()
				return err
			}
		}
		if _, err := tx.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", i+1)); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

// Close closes the database.
func (s *SQLite) Close() error { return s.db.Close() }

const connColumns = `id, workspace_id, name, engine, secret, secret_key_id, host, port, "database",
	username, description, mode, max_rows, timeout_ms, pool_size, allowed_namespaces, version,
	created_at, updated_at`

func isUnique(err error) bool {
	var se *msqlite.Error
	return errors.As(err, &se) && se.Code() == sqlitelib.SQLITE_CONSTRAINT_UNIQUE
}

func (s *SQLite) CreateConnection(ctx context.Context, c *Connection) error {
	ns, err := json.Marshal(nonNil(c.AllowedNamespaces))
	if err != nil {
		return err
	}
	now := s.now()
	_, err = s.db.ExecContext(ctx, `INSERT INTO connections (`+connColumns+`)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 1, ?, ?)`,
		c.ID, c.WorkspaceID, c.Name, c.Engine, c.Secret, c.SecretKeyID, c.Host, c.Port, c.Database,
		c.Username, c.Description, c.Mode, c.MaxRows, c.TimeoutMS, c.PoolSize, string(ns),
		now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano))
	if isUnique(err) {
		return ErrExists
	}
	if err != nil {
		return err
	}
	c.Version, c.CreatedAt, c.UpdatedAt = 1, now, now
	return nil
}

func (s *SQLite) UpdateConnection(ctx context.Context, c *Connection) error {
	ns, err := json.Marshal(nonNil(c.AllowedNamespaces))
	if err != nil {
		return err
	}
	now := s.now()
	res, err := s.db.ExecContext(ctx, `UPDATE connections SET engine = ?, secret = ?, secret_key_id = ?,
		host = ?, port = ?, "database" = ?, username = ?, description = ?, mode = ?, max_rows = ?,
		timeout_ms = ?, pool_size = ?, allowed_namespaces = ?, version = version + 1, updated_at = ?
		WHERE workspace_id = ? AND name = ? AND version = ?`,
		c.Engine, c.Secret, c.SecretKeyID, c.Host, c.Port, c.Database, c.Username, c.Description,
		c.Mode, c.MaxRows, c.TimeoutMS, c.PoolSize, string(ns), now.Format(time.RFC3339Nano),
		c.WorkspaceID, c.Name, c.Version)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		if _, err := s.GetConnection(ctx, c.WorkspaceID, c.Name); err != nil {
			return err
		}
		return ErrConflict
	}
	c.Version++
	c.UpdatedAt = now
	return nil
}

func (s *SQLite) DeleteConnection(ctx context.Context, workspaceID, name string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM connections WHERE workspace_id = ? AND name = ?`, workspaceID, name)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *SQLite) GetConnection(ctx context.Context, workspaceID, name string) (*Connection, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+connColumns+` FROM connections WHERE workspace_id = ? AND name = ?`, workspaceID, name)
	c, err := scanConnection(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return c, err
}

func (s *SQLite) ListConnections(ctx context.Context, workspaceID string) ([]*Connection, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+connColumns+` FROM connections WHERE workspace_id = ? ORDER BY name`, workspaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Connection
	for rows.Next() {
		c, err := scanConnection(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

type scanner interface{ Scan(dest ...any) error }

func scanConnection(r scanner) (*Connection, error) {
	var c Connection
	var ns, created, updated string
	err := r.Scan(&c.ID, &c.WorkspaceID, &c.Name, &c.Engine, &c.Secret, &c.SecretKeyID, &c.Host, &c.Port,
		&c.Database, &c.Username, &c.Description, &c.Mode, &c.MaxRows, &c.TimeoutMS, &c.PoolSize, &ns,
		&c.Version, &created, &updated)
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(ns), &c.AllowedNamespaces); err != nil {
		return nil, err
	}
	if len(c.AllowedNamespaces) == 0 {
		c.AllowedNamespaces = nil
	}
	if c.CreatedAt, err = time.Parse(time.RFC3339Nano, created); err != nil {
		return nil, err
	}
	if c.UpdatedAt, err = time.Parse(time.RFC3339Nano, updated); err != nil {
		return nil, err
	}
	return &c, nil
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
