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
			last_used_at TEXT,
			created_by TEXT
		)`,
		`CREATE INDEX api_keys_workspace ON api_keys (workspace_id)`,
	},
	{
		`CREATE TABLE query_log (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
			connection_id TEXT NOT NULL,
			connection_name TEXT NOT NULL,
			key_id TEXT NOT NULL DEFAULT '',
			tool TEXT NOT NULL,
			query TEXT NOT NULL,
			row_count INTEGER NOT NULL,
			duration_ms INTEGER NOT NULL,
			error_category TEXT NOT NULL DEFAULT '',
			at INTEGER NOT NULL
		)`,
		`CREATE INDEX query_log_workspace ON query_log (workspace_id, id)`,
		`CREATE INDEX query_log_at ON query_log (at)`,
	},
	{
		`ALTER TABLE connections ADD COLUMN ssh TEXT NOT NULL DEFAULT '{}'`,
		`ALTER TABLE connections ADD COLUMN refs TEXT NOT NULL DEFAULT '{}'`,
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
	created_at, updated_at, tls, ssh, refs`

// jsonColumn stores an empty JSON summary (tls, ssh, refs) as '{}', the column's default.
func jsonColumn(s string) string {
	if s == "" {
		return "{}"
	}
	return s
}

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
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 1, ?, ?, ?, ?, ?)`,
		c.ID, c.WorkspaceID, c.Name, c.Engine, c.Secret, c.SecretKeyID, c.Host, c.Port, c.Database,
		c.Username, c.Description, c.Mode, c.MaxRows, c.TimeoutMS, c.PoolSize, string(ns),
		now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano), jsonColumn(c.TLS), jsonColumn(c.SSH), jsonColumn(c.Refs))
	if isUnique(err) {
		return ErrExists
	}
	if isForeignKey(err) {
		return ErrNotFound // the workspace does not exist
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
		timeout_ms = ?, pool_size = ?, allowed_namespaces = ?, tls = ?, ssh = ?, refs = ?, version = version + 1, updated_at = ?
		WHERE workspace_id = ? AND name = ? AND id = ? AND version = ?`,
		c.Engine, c.Secret, c.SecretKeyID, c.Host, c.Port, c.Database, c.Username, c.Description,
		c.Mode, c.MaxRows, c.TimeoutMS, c.PoolSize, string(ns), jsonColumn(c.TLS), jsonColumn(c.SSH), jsonColumn(c.Refs), now.Format(time.RFC3339Nano),
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
	var ns, created, updated, tls, ssh, refs string
	err := r.Scan(&c.ID, &c.WorkspaceID, &c.Name, &c.Engine, &c.Secret, &c.SecretKeyID, &c.Host, &c.Port,
		&c.Database, &c.Username, &c.Description, &c.Mode, &c.MaxRows, &c.TimeoutMS, &c.PoolSize, &ns,
		&c.Version, &created, &updated, &tls, &ssh, &refs)
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(ns), &c.AllowedNamespaces); err != nil {
		return nil, err
	}
	if len(c.AllowedNamespaces) == 0 {
		c.AllowedNamespaces = nil
	}
	if tls != "{}" {
		c.TLS = tls
	}
	if ssh != "{}" {
		c.SSH = ssh
	}
	if refs != "{}" {
		c.Refs = refs
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

func (s *SQLite) AppendQueryLog(ctx context.Context, e *QueryLogEntry) error {
	if e.At.IsZero() {
		e.At = s.now()
	}
	res, err := s.db.ExecContext(ctx, `INSERT INTO query_log (workspace_id, connection_id, connection_name, key_id,
		tool, query, row_count, duration_ms, error_category, at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		e.WorkspaceID, e.ConnectionID, e.ConnectionName, e.KeyID, e.Tool, e.Query, e.RowCount, e.DurationMS,
		e.ErrorCategory, e.At.UnixMilli())
	if err != nil {
		return err
	}
	e.ID, err = res.LastInsertId()
	return err
}

func (s *SQLite) ListQueryLog(ctx context.Context, f QueryLogFilter) ([]*QueryLogEntry, error) {
	q := `SELECT id, workspace_id, connection_id, connection_name, key_id, tool, query, row_count, duration_ms,
		error_category, at FROM query_log WHERE workspace_id = ?`
	args := []any{f.WorkspaceID}
	if f.ConnectionName != "" {
		q += ` AND connection_name = ?`
		args = append(args, f.ConnectionName)
	}
	if !f.Since.IsZero() {
		q += ` AND at >= ?`
		args = append(args, f.Since.UnixMilli())
	}
	limit := f.Limit
	if limit <= 0 {
		limit = -1 // SQLite: no limit
	}
	q += ` ORDER BY id DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*QueryLogEntry
	for rows.Next() {
		var e QueryLogEntry
		var at int64
		if err := rows.Scan(&e.ID, &e.WorkspaceID, &e.ConnectionID, &e.ConnectionName, &e.KeyID, &e.Tool, &e.Query,
			&e.RowCount, &e.DurationMS, &e.ErrorCategory, &at); err != nil {
			return nil, err
		}
		e.At = time.UnixMilli(at).UTC()
		out = append(out, &e)
	}
	return out, rows.Err()
}

func (s *SQLite) PruneQueryLog(ctx context.Context, before time.Time) (int64, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM query_log WHERE at < ?`, before.UnixMilli())
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// ResealConnections calls fn for every connection in every workspace inside
// one write transaction, and stores the secret fn returns. A nil secret
// leaves the connection unchanged. If fn fails, nothing is changed. Versions
// stay the same: the connection string itself does not change.
func (s *SQLite) ResealConnections(ctx context.Context, fn func(c *Connection) ([]byte, uint32, error)) (int, error) {
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return 0, err
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return 0, err
	}
	n, err := reseal(ctx, conn, fn)
	if err == nil {
		_, err = conn.ExecContext(ctx, "COMMIT")
	}
	if err != nil {
		conn.ExecContext(context.Background(), "ROLLBACK")
		return 0, err
	}
	return n, nil
}

func reseal(ctx context.Context, conn *sql.Conn, fn func(c *Connection) ([]byte, uint32, error)) (int, error) {
	rows, err := conn.QueryContext(ctx, `SELECT `+connColumns+` FROM connections ORDER BY workspace_id, name`)
	if err != nil {
		return 0, err
	}
	var all []*Connection
	for rows.Next() {
		c, err := scanConnection(rows)
		if err != nil {
			rows.Close()
			return 0, err
		}
		all = append(all, c)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, err
	}
	rows.Close()
	n := 0
	for _, c := range all {
		secret, keyID, err := fn(c)
		if err != nil {
			return 0, err
		}
		if secret == nil {
			continue
		}
		if _, err := conn.ExecContext(ctx, `UPDATE connections SET secret = ?, secret_key_id = ? WHERE id = ?`, secret, keyID, c.ID); err != nil {
			return 0, err
		}
		n++
	}
	return n, nil
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

const keyColumns = `id, workspace_id, prefix, hash, scopes, name, created_at, expires_at, last_used_at, created_by`

func (s *SQLite) CreateKey(ctx context.Context, k *APIKey) error {
	now := s.now()
	var ws, by any
	if k.WorkspaceID != "" {
		ws = k.WorkspaceID
	}
	if k.CreatedBy != "" {
		by = k.CreatedBy
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO api_keys (`+keyColumns+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, NULL, ?)`,
		k.ID, ws, k.Prefix, k.Hash, strings.Join(k.Scopes, ","), k.Name, now.Format(time.RFC3339Nano), formatTime(k.ExpiresAt), by)
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

func (s *SQLite) CreateKeyIfNone(ctx context.Context, k *APIKey) (bool, error) {
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return false, err
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return false, err
	}
	var n int
	if err := conn.QueryRowContext(ctx, `SELECT count(*) FROM api_keys`).Scan(&n); err != nil {
		conn.ExecContext(context.Background(), "ROLLBACK")
		return false, err
	}
	if n > 0 {
		_, err := conn.ExecContext(context.Background(), "ROLLBACK")
		return false, err
	}
	now := s.now()
	var ws, by any
	if k.WorkspaceID != "" {
		ws = k.WorkspaceID
	}
	if k.CreatedBy != "" {
		by = k.CreatedBy
	}
	if _, err := conn.ExecContext(ctx, `INSERT INTO api_keys (`+keyColumns+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, NULL, ?)`,
		k.ID, ws, k.Prefix, k.Hash, strings.Join(k.Scopes, ","), k.Name, now.Format(time.RFC3339Nano), formatTime(k.ExpiresAt), by); err != nil {
		conn.ExecContext(context.Background(), "ROLLBACK")
		switch {
		case isUnique(err):
			return false, ErrExists
		case isForeignKey(err):
			return false, ErrNotFound
		}
		return false, err
	}
	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		conn.ExecContext(context.Background(), "ROLLBACK")
		return false, err
	}
	k.CreatedAt = now
	return true, nil
}

func (s *SQLite) ImportConnection(ctx context.Context, c *Connection) error {
	ns, err := json.Marshal(nonNil(c.AllowedNamespaces))
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO connections (`+connColumns+`)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		c.ID, c.WorkspaceID, c.Name, c.Engine, c.Secret, c.SecretKeyID, c.Host, c.Port, c.Database,
		c.Username, c.Description, c.Mode, c.MaxRows, c.TimeoutMS, c.PoolSize, string(ns), c.Version,
		c.CreatedAt.UTC().Format(time.RFC3339Nano), c.UpdatedAt.UTC().Format(time.RFC3339Nano),
		jsonColumn(c.TLS), jsonColumn(c.SSH), jsonColumn(c.Refs))
	switch {
	case isUnique(err):
		return ErrExists
	case isForeignKey(err):
		return ErrNotFound
	}
	return err
}

func (s *SQLite) ImportKey(ctx context.Context, k *APIKey) error {
	var ws, by any
	if k.WorkspaceID != "" {
		ws = k.WorkspaceID
	}
	if k.CreatedBy != "" {
		by = k.CreatedBy
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO api_keys (`+keyColumns+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		k.ID, ws, k.Prefix, k.Hash, strings.Join(k.Scopes, ","), k.Name, k.CreatedAt.UTC().Format(time.RFC3339Nano),
		formatTime(k.ExpiresAt), formatTime(k.LastUsedAt), by)
	switch {
	case isUnique(err):
		return ErrExists
	case isForeignKey(err):
		return ErrNotFound
	}
	return err
}

func scanKey(r scanner) (*APIKey, error) {
	var k APIKey
	var ws, expires, used, by sql.NullString
	var scopes, created string
	if err := r.Scan(&k.ID, &ws, &k.Prefix, &k.Hash, &scopes, &k.Name, &created, &expires, &used, &by); err != nil {
		return nil, err
	}
	k.WorkspaceID, k.CreatedBy = ws.String, by.String
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
