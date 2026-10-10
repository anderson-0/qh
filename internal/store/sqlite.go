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
	"strings"
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
	{
		`CREATE TABLE api_keys (
			id TEXT PRIMARY KEY,
			workspace_id TEXT REFERENCES workspaces(id) ON DELETE CASCADE,
			prefix TEXT NOT NULL UNIQUE,
			hash BLOB NOT NULL,
			scopes TEXT NOT NULL,
			name TEXT NOT NULL DEFAULT '',
			created_at TEXT NOT NULL,
			expires_at TEXT,
			last_used_at TEXT
		)`,
		`CREATE INDEX api_keys_workspace ON api_keys (workspace_id)`,
	},
}

// SQLite is a Store backed by a SQLite file.
type SQLite struct {
	db  *sql.DB
	now func() time.Time
}

// openRetryFor bounds how long OpenSQLite retries after SQLITE_BUSY. The driver
// runs PRAGMA journal_mode=WAL on every new connection, and SQLite does not
// apply busy_timeout to that switch, so a connection opened while another
// process is creating or converting the file can fail at once.
const openRetryFor = 10 * time.Second

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
	deadline := time.Now().Add(openRetryFor)
	backoff := 5 * time.Millisecond
	for {
		s, err := openSQLiteOnce(abs)
		if err == nil || !isBusy(err) || time.Now().After(deadline) {
			return s, err
		}
		time.Sleep(backoff)
		if backoff < 100*time.Millisecond {
			backoff *= 2
		}
	}
}

func openSQLiteOnce(abs string) (*SQLite, error) {
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

// migrate applies pending migrations in one BEGIN IMMEDIATE transaction. The
// write lock is taken before user_version is read, so concurrent first opens
// wait on busy_timeout and then see the finished schema instead of racing to
// create the same tables.
func (s *SQLite) migrate(ctx context.Context) error {
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return err
	}
	if err := applyMigrations(ctx, conn); err != nil {
		conn.ExecContext(context.Background(), "ROLLBACK")
		return err
	}
	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		conn.ExecContext(context.Background(), "ROLLBACK")
		return err
	}
	return nil
}

func applyMigrations(ctx context.Context, conn *sql.Conn) error {
	var v int
	if err := conn.QueryRowContext(ctx, "PRAGMA user_version").Scan(&v); err != nil {
		return err
	}
	for i := v; i < len(migrations); i++ {
		for _, stmt := range migrations[i] {
			if _, err := conn.ExecContext(ctx, stmt); err != nil {
				return err
			}
		}
		if _, err := conn.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", i+1)); err != nil {
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

func isBusy(err error) bool {
	var se *msqlite.Error
	return errors.As(err, &se) && se.Code()&0xff == sqlitelib.SQLITE_BUSY
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
		WHERE workspace_id = ? AND name = ? AND id = ? AND version = ?`,
		c.Engine, c.Secret, c.SecretKeyID, c.Host, c.Port, c.Database, c.Username, c.Description,
		c.Mode, c.MaxRows, c.TimeoutMS, c.PoolSize, string(ns), now.Format(time.RFC3339Nano),
		c.WorkspaceID, c.Name, c.ID, c.Version)
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

func isForeignKey(err error) bool {
	var se *msqlite.Error
	return errors.As(err, &se) && se.Code() == sqlitelib.SQLITE_CONSTRAINT_FOREIGNKEY
}

func formatTime(t *time.Time) any {
	if t == nil {
		return nil
	}
	return t.UTC().Format(time.RFC3339Nano)
}

func parseTime(s sql.NullString) (*time.Time, error) {
	if !s.Valid {
		return nil, nil
	}
	t, err := time.Parse(time.RFC3339Nano, s.String)
	if err != nil {
		return nil, err
	}
	return &t, nil
}

func (s *SQLite) Ping(ctx context.Context) error { return s.db.PingContext(ctx) }

func (s *SQLite) CreateWorkspace(ctx context.Context, id string) (*Workspace, error) {
	now := s.now()
	_, err := s.db.ExecContext(ctx, `INSERT INTO workspaces (id, name, created_at) VALUES (?, ?, ?)`,
		id, id, now.Format(time.RFC3339Nano))
	if isUnique(err) {
		return nil, ErrExists
	}
	if err != nil {
		return nil, err
	}
	return &Workspace{ID: id, CreatedAt: now}, nil
}

func (s *SQLite) ListWorkspaces(ctx context.Context) ([]*Workspace, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, created_at FROM workspaces ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Workspace
	for rows.Next() {
		var w Workspace
		var created string
		if err := rows.Scan(&w.ID, &created); err != nil {
			return nil, err
		}
		if w.CreatedAt, err = time.Parse(time.RFC3339Nano, created); err != nil {
			return nil, err
		}
		out = append(out, &w)
	}
	return out, rows.Err()
}

func (s *SQLite) DeleteWorkspace(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM workspaces WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil {
		return err
	} else if n == 0 {
		return ErrNotFound
	}
	return nil
}

const keyColumns = `id, workspace_id, prefix, hash, scopes, name, created_at, expires_at, last_used_at`

func (s *SQLite) CreateKey(ctx context.Context, k *APIKey) error {
	now := s.now()
	var ws any
	if k.WorkspaceID != "" {
		ws = k.WorkspaceID
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO api_keys (`+keyColumns+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, NULL)`,
		k.ID, ws, k.Prefix, k.Hash, strings.Join(k.Scopes, ","), k.Name, now.Format(time.RFC3339Nano), formatTime(k.ExpiresAt))
	switch {
	case isUnique(err):
		return ErrExists
	case isForeignKey(err):
		return ErrNotFound
	case err != nil:
		return err
	}
	k.CreatedAt = now
	return nil
}

func scanKey(r scanner) (*APIKey, error) {
	var k APIKey
	var ws, expires, used sql.NullString
	var scopes, created string
	if err := r.Scan(&k.ID, &ws, &k.Prefix, &k.Hash, &scopes, &k.Name, &created, &expires, &used); err != nil {
		return nil, err
	}
	k.WorkspaceID = ws.String
	if scopes != "" {
		k.Scopes = strings.Split(scopes, ",")
	}
	var err error
	if k.CreatedAt, err = time.Parse(time.RFC3339Nano, created); err != nil {
		return nil, err
	}
	if k.ExpiresAt, err = parseTime(expires); err != nil {
		return nil, err
	}
	if k.LastUsedAt, err = parseTime(used); err != nil {
		return nil, err
	}
	return &k, nil
}

func (s *SQLite) GetKeyByPrefix(ctx context.Context, prefix string) (*APIKey, error) {
	k, err := scanKey(s.db.QueryRowContext(ctx, `SELECT `+keyColumns+` FROM api_keys WHERE prefix = ?`, prefix))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return k, err
}

func (s *SQLite) listKeys(ctx context.Context, where string, args ...any) ([]*APIKey, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+keyColumns+` FROM api_keys `+where+` ORDER BY created_at, id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*APIKey
	for rows.Next() {
		k, err := scanKey(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

func (s *SQLite) ListKeys(ctx context.Context, workspaceID string) ([]*APIKey, error) {
	if workspaceID == "" {
		return s.listKeys(ctx, `WHERE workspace_id IS NULL`)
	}
	return s.listKeys(ctx, `WHERE workspace_id = ?`, workspaceID)
}

func (s *SQLite) ListAllKeys(ctx context.Context) ([]*APIKey, error) { return s.listKeys(ctx, ``) }

func (s *SQLite) DeleteKey(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM api_keys WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil {
		return err
	} else if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *SQLite) TouchKey(ctx context.Context, id string, at time.Time) error {
	_, err := s.db.ExecContext(ctx, `UPDATE api_keys SET last_used_at = ? WHERE id = ?`, at.UTC().Format(time.RFC3339Nano), id)
	return err
}

func (s *SQLite) CountKeys(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM api_keys`).Scan(&n)
	return n, err
}
