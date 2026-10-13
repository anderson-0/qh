package store

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"strings"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/stdlib"
)

// pgOpenRetry bounds how long OpenPostgres waits for an unreachable
// database, so instances can start before it.
var pgOpenRetry = 30 * time.Second

// pgMaxConns bounds the store's connection pool.
const pgMaxConns = 10

// migrateLock is the advisory-lock key that serialises schema migrations.
const migrateLock = 7_212_011_970

// bootstrapLock is the advisory-lock key that serialises first-key creation.
const bootstrapLock = 7_212_011_971

// pgMigrations[i] moves the qh schema from version i to i+1.
//
// Migrations must be additive (new tables, nullable or defaulted columns, new
// indexes; never a drop, rename or type change), so an older binary keeps
// working against a newer schema during a rolling upgrade. A store whose
// recorded version is higher than this binary knows is accepted as it is.
var pgMigrations = [][]string{
	{
		`CREATE TABLE qh.workspaces (
			id text PRIMARY KEY,
			name text NOT NULL UNIQUE,
			created_at timestamptz NOT NULL
		)`,
		`INSERT INTO qh.workspaces (id, name, created_at) VALUES ('default', 'default', now())`,
		`CREATE TABLE qh.connections (
			id text PRIMARY KEY,
			workspace_id text NOT NULL REFERENCES qh.workspaces(id) ON DELETE CASCADE,
			name text NOT NULL,
			engine text NOT NULL,
			secret bytea NOT NULL,
			secret_key_id bigint NOT NULL,
			host text NOT NULL DEFAULT '',
			port integer NOT NULL DEFAULT 0,
			"database" text NOT NULL DEFAULT '',
			username text NOT NULL DEFAULT '',
			description text NOT NULL DEFAULT '',
			mode text NOT NULL CHECK (mode IN ('ro', 'rw')),
			max_rows integer NOT NULL,
			timeout_ms integer NOT NULL,
			pool_size integer NOT NULL,
			allowed_namespaces text NOT NULL DEFAULT '[]',
			tls text NOT NULL DEFAULT '{}',
			ssh text NOT NULL DEFAULT '{}',
			refs text NOT NULL DEFAULT '{}',
			version bigint NOT NULL DEFAULT 1,
			created_at timestamptz NOT NULL,
			updated_at timestamptz NOT NULL,
			UNIQUE (workspace_id, name)
		)`,
		`CREATE TABLE qh.api_keys (
			id text PRIMARY KEY,
			workspace_id text REFERENCES qh.workspaces(id) ON DELETE CASCADE,
			prefix text NOT NULL UNIQUE,
			hash bytea NOT NULL,
			scopes text NOT NULL,
			name text NOT NULL DEFAULT '',
			created_at timestamptz NOT NULL,
			expires_at timestamptz,
			last_used_at timestamptz,
			created_by text
		)`,
		`CREATE INDEX api_keys_workspace ON qh.api_keys (workspace_id)`,
		`CREATE TABLE qh.query_log (
			id bigserial PRIMARY KEY,
			workspace_id text NOT NULL REFERENCES qh.workspaces(id) ON DELETE CASCADE,
			connection_id text NOT NULL,
			connection_name text NOT NULL,
			key_id text NOT NULL DEFAULT '',
			tool text NOT NULL,
			query text NOT NULL,
			row_count bigint NOT NULL,
			duration_ms bigint NOT NULL,
			error_category text NOT NULL DEFAULT '',
			at timestamptz NOT NULL
		)`,
		`CREATE INDEX query_log_workspace ON qh.query_log (workspace_id, id)`,
		`CREATE INDEX query_log_at ON qh.query_log (at)`,
	},
}

// Postgres is a Store backed by a Postgres database; its tables live in the
// qh schema.
type Postgres struct {
	db  *sql.DB
	cfg *pgx.ConnConfig // kept for the LISTEN connection
	now func() time.Time
	// origin tags this process's invalidation notices so its own listener
	// skips them (NOTIFY is delivered to the sender too).
	origin string
	logger atomic.Pointer[slog.Logger] // nil: the listener logs nothing
}

// SetLogger makes the invalidation listener report losing and regaining its
// connection to l. Call it before Invalidations.
func (p *Postgres) SetLogger(l *slog.Logger) { p.logger.Store(l) }

// OpenPostgres connects to dsn, waiting up to pgOpenRetry for the database,
// and migrates the qh schema. QH_STORE_PASSWORD_FILE, when set, supplies the
// password. Errors never contain the password.
func OpenPostgres(ctx context.Context, dsn string) (*Postgres, error) {
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		return nil, errors.New("store: invalid postgres connection string")
	}
	if f := os.Getenv("QH_STORE_PASSWORD_FILE"); f != "" {
		b, err := os.ReadFile(f)
		if err != nil {
			// The *PathError names the path and the cause, never the contents.
			return nil, fmt.Errorf("store: cannot read QH_STORE_PASSWORD_FILE: %w", err)
		}
		cfg.Password = strings.TrimRight(string(b), "\r\n")
	}
	db := stdlib.OpenDB(*cfg)
	db.SetMaxOpenConns(pgMaxConns)
	db.SetMaxIdleConns(pgMaxConns)
	db.SetConnMaxLifetime(30 * time.Minute)
	if err := pingUntil(ctx, db, time.Now().Add(pgOpenRetry)); err != nil {
		db.Close()
		return nil, fmt.Errorf("store: cannot reach the postgres store at %s:%d: %s", cfg.Host, cfg.Port, redactErr(err, cfg.Password))
	}
	p := &Postgres{db: db, cfg: cfg, now: func() time.Time { return time.Now().UTC().Truncate(time.Microsecond) }, origin: NewID()}
	if err := p.migrate(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("store: migrate: %s", redactErr(err, cfg.Password))
	}
	return p, nil
}

// pingUntil pings db until it answers, the deadline passes or ctx ends. Each
// attempt is bounded by the deadline, since the default dialer has none. A
// permanent error (a wrong password, a missing database) is returned at once;
// a server that is starting up or full is retried.
func pingUntil(ctx context.Context, db *sql.DB, deadline time.Time) error {
	for {
		pctx, cancel := context.WithDeadline(ctx, deadline)
		err := db.PingContext(pctx)
		cancel()
		if err == nil || permanent(err) || ctx.Err() != nil {
			return err
		}
		wait := time.Until(deadline)
		if wait <= 0 {
			return err
		}
		select {
		case <-ctx.Done():
			return err
		case <-time.After(min(wait, 500*time.Millisecond)):
		}
	}
}

// transientPG lists the SQLSTATEs a server sends while it cannot take
// connections for now: starting up, out of connection slots, shutting down.
var transientPG = map[string]bool{
	"57P03": true, // cannot_connect_now
	"53300": true, // too_many_connections
	"57P01": true, // admin_shutdown
	"57P02": true, // crash_shutdown
}

// permanent reports whether err came from the server and retrying cannot fix
// it (a wrong password, a missing database). Network errors are not permanent.
func permanent(err error) bool {
	var pe *pgconn.PgError
	return errors.As(err, &pe) && !transientPG[pe.Code]
}

// redactErr returns err's text with the password replaced.
func redactErr(err error, password string) string {
	s := err.Error()
	if len(password) >= 4 {
		s = strings.ReplaceAll(s, password, "***")
	}
	return s
}

func (p *Postgres) migrate(ctx context.Context) error {
	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1)`, int64(migrateLock)); err != nil {
		return err
	}
	// CREATE SCHEMA IF NOT EXISTS needs CREATE on the database even when the
	// schema exists, so a role that owns a pre-created qh schema would fail.
	var schemaExists bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM pg_namespace WHERE nspname = 'qh')`).Scan(&schemaExists); err != nil {
		return err
	}
	if !schemaExists {
		if _, err := tx.ExecContext(ctx, `CREATE SCHEMA qh`); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS qh.schema_migrations (
		version integer PRIMARY KEY, applied_at timestamptz NOT NULL DEFAULT now())`); err != nil {
		return err
	}
	var v int
	if err := tx.QueryRowContext(ctx, `SELECT coalesce(max(version), 0) FROM qh.schema_migrations`).Scan(&v); err != nil {
		return err
	}
	for i := v; i < len(pgMigrations); i++ {
		for _, stmt := range pgMigrations[i] {
			if _, err := tx.ExecContext(ctx, stmt); err != nil {
				return err
			}
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO qh.schema_migrations (version) VALUES ($1)`, i+1); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func pgCode(err error, code string) bool {
	var pe *pgconn.PgError
	return errors.As(err, &pe) && pe.Code == code
}

func isPGUnique(err error) bool     { return pgCode(err, "23505") }
func isPGForeignKey(err error) bool { return pgCode(err, "23503") }

// read runs the read fn, and runs it again while it fails because the server
// ended the connection (a restart, a failover, an administrator). After a
// restart every pooled connection may be dead; each failed attempt discards
// one, so pgMaxConns+1 attempts always reach a fresh one. Writes never go
// through read, since their first attempt may have committed. fn must reset
// anything it fills.
func (p *Postgres) read(ctx context.Context, fn func() error) error {
	err := fn()
	for range pgMaxConns {
		if err == nil || ctx.Err() != nil || !connDied(err) {
			break
		}
		err = fn()
	}
	return err
}

// connDied reports whether err means the connection itself is gone, rather
// than that the statement failed.
func connDied(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	var ce *pgconn.ConnectError
	if errors.As(err, &ce) {
		return false // a new connection could not be made; trying again at once will not help
	}
	var pe *pgconn.PgError
	if errors.As(err, &pe) {
		// The server ends the session after any FATAL error (57P05
		// idle_session_timeout, 25P03 idle_in_transaction_session_timeout, ...).
		if pe.SeverityUnlocalized == "FATAL" || pe.Severity == "FATAL" {
			return true
		}
		switch pe.Code {
		case "57P01", "57P02", "57P03": // admin_shutdown, crash_shutdown, cannot_connect_now
			return true
		}
		return strings.HasPrefix(pe.Code, "08") // connection_exception
	}
	if errors.Is(err, driver.ErrBadConn) || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}
	var ne net.Error
	return errors.As(err, &ne)
}

func (p *Postgres) Close() error { return p.db.Close() }
func (p *Postgres) Ping(ctx context.Context) error {
	return p.read(ctx, func() error { return p.db.PingContext(ctx) })
}

func scanPGConnection(r scanner) (*Connection, error) {
	var c Connection
	var ns, tls, ssh, refs string
	var keyID int64
	err := r.Scan(&c.ID, &c.WorkspaceID, &c.Name, &c.Engine, &c.Secret, &keyID, &c.Host, &c.Port,
		&c.Database, &c.Username, &c.Description, &c.Mode, &c.MaxRows, &c.TimeoutMS, &c.PoolSize, &ns,
		&c.Version, &c.CreatedAt, &c.UpdatedAt, &tls, &ssh, &refs)
	if err != nil {
		return nil, err
	}
	c.SecretKeyID = uint32(keyID)
	c.CreatedAt, c.UpdatedAt = c.CreatedAt.UTC(), c.UpdatedAt.UTC()
	if err := json.Unmarshal([]byte(ns), &c.AllowedNamespaces); err != nil {
		return nil, err
	}
	if len(c.AllowedNamespaces) == 0 {
		c.AllowedNamespaces = nil
	}
	for _, f := range []struct {
		col string
		dst *string
	}{{tls, &c.TLS}, {ssh, &c.SSH}, {refs, &c.Refs}} {
		if f.col != "{}" {
			*f.dst = f.col
		}
	}
	return &c, nil
}

const pgInsertConnection = `INSERT INTO qh.connections (` + connColumns + `)
	VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21, $22)`

func connArgs(c *Connection, ns []byte) []any {
	return []any{c.ID, c.WorkspaceID, c.Name, c.Engine, c.Secret, int64(c.SecretKeyID), c.Host, c.Port, c.Database,
		c.Username, c.Description, c.Mode, c.MaxRows, c.TimeoutMS, c.PoolSize, string(ns), c.Version,
		c.CreatedAt, c.UpdatedAt, jsonColumn(c.TLS), jsonColumn(c.SSH), jsonColumn(c.Refs)}
}

func (p *Postgres) CreateConnection(ctx context.Context, c *Connection) error {
	ns, err := json.Marshal(nonNil(c.AllowedNamespaces))
	if err != nil {
		return err
	}
	now := p.now()
	row := *c
	row.Version, row.CreatedAt, row.UpdatedAt = 1, now, now
	_, err = p.db.ExecContext(ctx, pgInsertConnection, connArgs(&row, ns)...)
	switch {
	case isPGUnique(err):
		return ErrExists
	case isPGForeignKey(err):
		return ErrNotFound
	case err != nil:
		return err
	}
	c.Version, c.CreatedAt, c.UpdatedAt = 1, now, now
	return nil
}

func (p *Postgres) UpdateConnection(ctx context.Context, c *Connection) error {
	ns, err := json.Marshal(nonNil(c.AllowedNamespaces))
	if err != nil {
		return err
	}
	now := p.now()
	res, err := p.db.ExecContext(ctx, `UPDATE qh.connections SET engine = $1, secret = $2, secret_key_id = $3,
		host = $4, port = $5, "database" = $6, username = $7, description = $8, mode = $9, max_rows = $10,
		timeout_ms = $11, pool_size = $12, allowed_namespaces = $13, tls = $14, ssh = $15, refs = $16,
		version = version + 1, updated_at = $17
		WHERE workspace_id = $18 AND name = $19 AND id = $20 AND version = $21`,
		c.Engine, c.Secret, int64(c.SecretKeyID), c.Host, c.Port, c.Database, c.Username, c.Description,
		c.Mode, c.MaxRows, c.TimeoutMS, c.PoolSize, string(ns), jsonColumn(c.TLS), jsonColumn(c.SSH), jsonColumn(c.Refs),
		now, c.WorkspaceID, c.Name, c.ID, c.Version)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		if _, err := p.GetConnection(ctx, c.WorkspaceID, c.Name); err != nil {
			return err
		}
		return ErrConflict
	}
	c.Version++
	c.UpdatedAt = now
	return nil
}

func (p *Postgres) DeleteConnection(ctx context.Context, workspaceID, name string) error {
	res, err := p.db.ExecContext(ctx, `DELETE FROM qh.connections WHERE workspace_id = $1 AND name = $2`, workspaceID, name)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func (p *Postgres) GetConnection(ctx context.Context, workspaceID, name string) (*Connection, error) {
	var c *Connection
	err := p.read(ctx, func() (err error) {
		c, err = scanPGConnection(p.db.QueryRowContext(ctx, `SELECT `+connColumns+` FROM qh.connections
			WHERE workspace_id = $1 AND name = $2`, workspaceID, name))
		return err
	})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return c, err
}

func (p *Postgres) listConnections(ctx context.Context, q *sql.Rows, err error) ([]*Connection, error) {
	if err != nil {
		return nil, err
	}
	defer q.Close()
	var out []*Connection
	for q.Next() {
		c, err := scanPGConnection(q)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, q.Err()
}

func (p *Postgres) ListConnections(ctx context.Context, workspaceID string) ([]*Connection, error) {
	var out []*Connection
	err := p.read(ctx, func() error {
		rows, err := p.db.QueryContext(ctx, `SELECT `+connColumns+` FROM qh.connections WHERE workspace_id = $1 ORDER BY name`, workspaceID)
		out, err = p.listConnections(ctx, rows, err)
		return err
	})
	return out, err
}

func (p *Postgres) CreateWorkspace(ctx context.Context, id string) (*Workspace, error) {
	now := p.now()
	_, err := p.db.ExecContext(ctx, `INSERT INTO qh.workspaces (id, name, created_at) VALUES ($1, $1, $2)`, id, now)
	if isPGUnique(err) {
		return nil, ErrExists
	}
	if err != nil {
		return nil, err
	}
	return &Workspace{ID: id, CreatedAt: now}, nil
}

func (p *Postgres) ImportWorkspace(ctx context.Context, w *Workspace) error {
	_, err := p.db.ExecContext(ctx, `INSERT INTO qh.workspaces (id, name, created_at) VALUES ($1, $1, $2)
		ON CONFLICT (id) DO UPDATE SET created_at = excluded.created_at`, w.ID, w.CreatedAt.UTC().Truncate(time.Microsecond))
	return err
}

func (p *Postgres) ListWorkspaces(ctx context.Context) ([]*Workspace, error) {
	var out []*Workspace
	err := p.read(ctx, func() (err error) {
		out, err = p.listWorkspaces(ctx)
		return err
	})
	return out, err
}

func (p *Postgres) listWorkspaces(ctx context.Context) ([]*Workspace, error) {
	rows, err := p.db.QueryContext(ctx, `SELECT id, created_at FROM qh.workspaces ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Workspace
	for rows.Next() {
		var w Workspace
		if err := rows.Scan(&w.ID, &w.CreatedAt); err != nil {
			return nil, err
		}
		w.CreatedAt = w.CreatedAt.UTC()
		out = append(out, &w)
	}
	return out, rows.Err()
}

func (p *Postgres) DeleteWorkspace(ctx context.Context, id string) error {
	res, err := p.db.ExecContext(ctx, `DELETE FROM qh.workspaces WHERE id = $1`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func nullTime(t *time.Time) any {
	if t == nil {
		return nil
	}
	return t.UTC().Truncate(time.Microsecond)
}

func nullString(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func (p *Postgres) CreateKey(ctx context.Context, k *APIKey) error {
	now := p.now()
	_, err := p.db.ExecContext(ctx, `INSERT INTO qh.api_keys (`+keyColumns+`) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, NULL, $9)`,
		k.ID, nullString(k.WorkspaceID), k.Prefix, k.Hash, strings.Join(k.Scopes, ","), k.Name, now, nullTime(k.ExpiresAt), nullString(k.CreatedBy))
	switch {
	case isPGUnique(err):
		return ErrExists
	case isPGForeignKey(err):
		return ErrNotFound
	case err != nil:
		return err
	}
	k.CreatedAt = now
	return nil
}

func (p *Postgres) CreateKeyIfNone(ctx context.Context, k *APIKey) (bool, error) {
	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1)`, int64(bootstrapLock)); err != nil {
		return false, err
	}
	var n int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM qh.api_keys`).Scan(&n); err != nil {
		return false, err
	}
	if n > 0 {
		return false, nil
	}
	now := p.now()
	if _, err := tx.ExecContext(ctx, `INSERT INTO qh.api_keys (`+keyColumns+`) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, NULL, $9)`,
		k.ID, nullString(k.WorkspaceID), k.Prefix, k.Hash, strings.Join(k.Scopes, ","), k.Name, now, nullTime(k.ExpiresAt), nullString(k.CreatedBy)); err != nil {
		switch {
		case isPGUnique(err):
			return false, ErrExists
		case isPGForeignKey(err):
			return false, ErrNotFound
		}
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	k.CreatedAt = now
	return true, nil
}

func (p *Postgres) ImportConnection(ctx context.Context, c *Connection) error {
	ns, err := json.Marshal(nonNil(c.AllowedNamespaces))
	if err != nil {
		return err
	}
	row := *c
	row.CreatedAt, row.UpdatedAt = c.CreatedAt.UTC().Truncate(time.Microsecond), c.UpdatedAt.UTC().Truncate(time.Microsecond)
	_, err = p.db.ExecContext(ctx, pgInsertConnection, connArgs(&row, ns)...)
	switch {
	case isPGUnique(err):
		return ErrExists
	case isPGForeignKey(err):
		return ErrNotFound
	}
	return err
}

func (p *Postgres) ImportKey(ctx context.Context, k *APIKey) error {
	_, err := p.db.ExecContext(ctx, `INSERT INTO qh.api_keys (`+keyColumns+`) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`,
		k.ID, nullString(k.WorkspaceID), k.Prefix, k.Hash, strings.Join(k.Scopes, ","), k.Name,
		k.CreatedAt.UTC().Truncate(time.Microsecond), nullTime(k.ExpiresAt), nullTime(k.LastUsedAt), nullString(k.CreatedBy))
	switch {
	case isPGUnique(err):
		return ErrExists
	case isPGForeignKey(err):
		return ErrNotFound
	}
	return err
}

func scanPGKey(r scanner) (*APIKey, error) {
	var k APIKey
	var ws, by sql.NullString
	var expires, used sql.NullTime
	var scopes string
	if err := r.Scan(&k.ID, &ws, &k.Prefix, &k.Hash, &scopes, &k.Name, &k.CreatedAt, &expires, &used, &by); err != nil {
		return nil, err
	}
	k.WorkspaceID, k.CreatedBy, k.CreatedAt = ws.String, by.String, k.CreatedAt.UTC()
	if scopes != "" {
		k.Scopes = strings.Split(scopes, ",")
	}
	if expires.Valid {
		t := expires.Time.UTC()
		k.ExpiresAt = &t
	}
	if used.Valid {
		t := used.Time.UTC()
		k.LastUsedAt = &t
	}
	return &k, nil
}

func (p *Postgres) GetKeyByPrefix(ctx context.Context, prefix string) (*APIKey, error) {
	var k *APIKey
	err := p.read(ctx, func() (err error) {
		k, err = scanPGKey(p.db.QueryRowContext(ctx, `SELECT `+keyColumns+` FROM qh.api_keys WHERE prefix = $1`, prefix))
		return err
	})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return k, err
}

func (p *Postgres) listKeys(ctx context.Context, where string, args ...any) ([]*APIKey, error) {
	var out []*APIKey
	err := p.read(ctx, func() (err error) {
		out, err = p.queryKeys(ctx, where, args...)
		return err
	})
	return out, err
}

func (p *Postgres) queryKeys(ctx context.Context, where string, args ...any) ([]*APIKey, error) {
	rows, err := p.db.QueryContext(ctx, `SELECT `+keyColumns+` FROM qh.api_keys `+where+` ORDER BY created_at, id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*APIKey
	for rows.Next() {
		k, err := scanPGKey(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

func (p *Postgres) ListKeys(ctx context.Context, workspaceID string) ([]*APIKey, error) {
	if workspaceID == "" {
		return p.listKeys(ctx, `WHERE workspace_id IS NULL`)
	}
	return p.listKeys(ctx, `WHERE workspace_id = $1`, workspaceID)
}

func (p *Postgres) ListAllKeys(ctx context.Context) ([]*APIKey, error) { return p.listKeys(ctx, ``) }

func (p *Postgres) DeleteKey(ctx context.Context, id string) error {
	res, err := p.db.ExecContext(ctx, `DELETE FROM qh.api_keys WHERE id = $1`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func (p *Postgres) TouchKey(ctx context.Context, id string, at time.Time) error {
	_, err := p.db.ExecContext(ctx, `UPDATE qh.api_keys SET last_used_at = $1 WHERE id = $2`, at.UTC().Truncate(time.Microsecond), id)
	return err
}

func (p *Postgres) CountKeys(ctx context.Context) (int, error) {
	var n int
	err := p.read(ctx, func() error {
		return p.db.QueryRowContext(ctx, `SELECT count(*) FROM qh.api_keys`).Scan(&n)
	})
	return n, err
}

func (p *Postgres) AppendQueryLog(ctx context.Context, e *QueryLogEntry) error {
	if e.At.IsZero() {
		e.At = p.now()
	}
	e.At = e.At.UTC().Truncate(time.Microsecond)
	return p.db.QueryRowContext(ctx, `INSERT INTO qh.query_log (workspace_id, connection_id, connection_name, key_id,
		tool, query, row_count, duration_ms, error_category, at) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10) RETURNING id`,
		e.WorkspaceID, e.ConnectionID, e.ConnectionName, e.KeyID, e.Tool, e.Query, e.RowCount, e.DurationMS,
		e.ErrorCategory, e.At).Scan(&e.ID)
}

func (p *Postgres) ListQueryLog(ctx context.Context, f QueryLogFilter) ([]*QueryLogEntry, error) {
	q := `SELECT id, workspace_id, connection_id, connection_name, key_id, tool, query, row_count, duration_ms,
		error_category, at FROM qh.query_log WHERE workspace_id = $1`
	args := []any{f.WorkspaceID}
	if f.ConnectionName != "" {
		args = append(args, f.ConnectionName)
		q += fmt.Sprintf(` AND connection_name = $%d`, len(args))
	}
	if !f.Since.IsZero() {
		args = append(args, f.Since.UTC())
		q += fmt.Sprintf(` AND at >= $%d`, len(args))
	}
	q += ` ORDER BY id DESC`
	if f.Limit > 0 {
		args = append(args, f.Limit)
		q += fmt.Sprintf(` LIMIT $%d`, len(args))
	}
	var out []*QueryLogEntry
	err := p.read(ctx, func() (err error) {
		out, err = p.queryLog(ctx, q, args)
		return err
	})
	return out, err
}

func (p *Postgres) queryLog(ctx context.Context, q string, args []any) ([]*QueryLogEntry, error) {
	rows, err := p.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*QueryLogEntry
	for rows.Next() {
		var e QueryLogEntry
		if err := rows.Scan(&e.ID, &e.WorkspaceID, &e.ConnectionID, &e.ConnectionName, &e.KeyID, &e.Tool, &e.Query,
			&e.RowCount, &e.DurationMS, &e.ErrorCategory, &e.At); err != nil {
			return nil, err
		}
		e.At = e.At.UTC()
		out = append(out, &e)
	}
	return out, rows.Err()
}

func (p *Postgres) PruneQueryLog(ctx context.Context, before time.Time) (int64, error) {
	res, err := p.db.ExecContext(ctx, `DELETE FROM qh.query_log WHERE at < $1`, before.UTC())
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// ResealConnections calls fn for every connection inside one transaction that
// locks them all (SELECT ... FOR UPDATE), and stores the secret fn returns. A
// nil secret leaves the connection unchanged; an error rolls everything back.
// Versions stay the same.
func (p *Postgres) ResealConnections(ctx context.Context, fn func(c *Connection) ([]byte, uint32, error)) (int, error) {
	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT `+connColumns+` FROM qh.connections ORDER BY workspace_id, name FOR UPDATE`)
	all, err := p.listConnections(ctx, rows, err)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, c := range all {
		secret, keyID, err := fn(c)
		if err != nil {
			return 0, err
		}
		if secret == nil {
			continue
		}
		if _, err := tx.ExecContext(ctx, `UPDATE qh.connections SET secret = $1, secret_key_id = $2 WHERE id = $3`, secret, int64(keyID), c.ID); err != nil {
			return 0, err
		}
		n++
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return n, nil
}

const (
	listenTimeout = 10 * time.Second // bounds each LISTEN connection attempt
	closeTimeout  = 2 * time.Second  // bounds closing the LISTEN connection
)

// The LISTEN connection mostly sits idle, so a link that dies silently (a
// half-open TCP connection after a failover or a NAT timeout) could leave it
// waiting until the OS gives up on the socket. It therefore pings the server
// after listenIdle without a notice, giving the ping listenPingTimeout; that
// ping is what detects a dead link. TCP keepalive (listenKeepAlive, the same
// as Go's default) is kept on as a second, OS-level probe. Variables so tests
// can shorten them.
//
// A server idle_session_timeout shorter than listenIdle ends the quiet
// listener's session every cycle; it then reconnects (and drops the cached
// schemas) each time. listenLog keeps that from flooding the log.
var (
	listenIdle        = 30 * time.Second
	listenPingTimeout = 5 * time.Second
	listenKeepAlive   = 15 * time.Second
	// listenLogEvery limits the listener's outage logs.
	listenLogEvery = time.Minute
)

// Invalidate tells every other instance listening on the store that id's
// schema is stale.
func (p *Postgres) Invalidate(ctx context.Context, id string) error {
	_, err := p.db.ExecContext(ctx, `SELECT pg_notify('qh_catalog', $1)`, p.origin+":"+id)
	return err
}

// Invalidations listens on qh_catalog with its own connection, reconnecting
// with backoff; after each reconnect it sends "" because notices may have
// been missed. It skips this process's own notices.
func (p *Postgres) Invalidations(ctx context.Context) (<-chan string, error) {
	conn, err := p.listen(ctx)
	if err != nil {
		return nil, fmt.Errorf("store: listen: %s", redactErr(err, p.cfg.Password))
	}
	out := make(chan string, 64)
	logs := &listenLog{log: p.logger.Load, password: p.cfg.Password, every: listenLogEvery, now: time.Now}
	go func() {
		defer close(out)
		for {
			n, err := p.waitForNotice(ctx, conn)
			if err == nil {
				id, own := p.fromNotice(n.Payload)
				if own {
					continue
				}
				select {
				case out <- id:
				case <-ctx.Done():
				}
				continue
			}
			closeConn(conn)
			if ctx.Err() != nil {
				return
			}
			logs.lost(err)
			if conn = p.relisten(ctx, logs); conn == nil {
				return
			}
			select {
			case out <- "":
			case <-ctx.Done():
				closeConn(conn)
				return
			}
		}
	}()
	return out, nil
}

// waitForNotice waits for the next notice on conn. When none comes within
// listenIdle it pings the server and, if that works, keeps waiting; an error
// means the connection is lost.
//
// A context deadline does not break a pgx connection: pgconn's default
// context watcher sets a read deadline on the socket, the read fails with a
// timeout, which pgconn treats as non-fatal (it closes the connection on any
// other read error), and the deadline is cleared again. A message cut off by
// the deadline stays buffered and is completed by the next read. So a quiet
// interval costs one round trip, not a reconnect and a cache drop.
func (p *Postgres) waitForNotice(ctx context.Context, conn *pgx.Conn) (*pgconn.Notification, error) {
	for {
		wctx, cancel := context.WithTimeout(ctx, listenIdle)
		n, err := conn.WaitForNotification(wctx)
		cancel()
		if err == nil || ctx.Err() != nil || !pgconn.Timeout(err) || conn.IsClosed() {
			return n, err
		}
		pctx, cancel := context.WithTimeout(ctx, listenPingTimeout)
		err = conn.Ping(pctx)
		cancel()
		if err != nil {
			return nil, err
		}
		// A notice that arrived during the ping is queued on conn; the next
		// WaitForNotification returns it at once.
	}
}

// relisten reconnects with backoff until it succeeds or ctx ends (then it
// returns nil).
func (p *Postgres) relisten(ctx context.Context, logs *listenLog) *pgx.Conn {
	backoff := 500 * time.Millisecond
	for attempt := 1; ; attempt++ {
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(backoff):
		}
		conn, err := p.listen(ctx)
		if err == nil {
			logs.reconnected(attempt)
			return conn
		}
		if ctx.Err() != nil {
			return nil
		}
		logs.failed(attempt, err)
		backoff = min(backoff*2, 30*time.Second)
	}
}

// listenLog reports the listener's outages without flooding the log when the
// link flaps. An outage is reported (its loss and its recovery) unless another
// was reported within every; skipped outages are counted in the next report.
// The first failed reconnect of a reported outage is logged at once; other
// failures at most once per every. Errors are logged without the store
// password. A nil logger logs nothing.
type listenLog struct {
	log      func() *slog.Logger
	password string
	every    time.Duration
	now      func() time.Time

	lastOutage time.Time // when an outage was last reported
	lastFail   time.Time // when a failed reconnect was last logged
	skipped    int       // outages not reported since lastOutage
	reported   bool      // the current outage's loss was logged
	failLogged bool      // a failure was logged during the current outage
}

func (l *listenLog) lost(err error) {
	now := l.now()
	l.failLogged = false
	if !l.lastOutage.IsZero() && now.Sub(l.lastOutage) < l.every {
		l.reported = false
		l.skipped++
		return
	}
	l.lastOutage, l.reported = now, true
	args := []any{"error", redactErr(err, l.password)}
	if l.skipped > 0 {
		args = append(args, "unreported_outages", l.skipped)
		l.skipped = 0
	}
	if lg := l.log(); lg != nil {
		lg.Warn("store: lost the connection that listens for schema invalidations; reconnecting", args...)
	}
}

func (l *listenLog) failed(attempt int, err error) {
	now := l.now()
	first := l.reported && !l.failLogged
	if !first && !l.lastFail.IsZero() && now.Sub(l.lastFail) < l.every {
		return
	}
	l.lastFail, l.failLogged = now, true
	if lg := l.log(); lg != nil {
		lg.Warn("store: cannot reconnect the schema invalidation listener; cached schemas may be stale until it does",
			"attempt", attempt, "error", redactErr(err, l.password))
	}
}

// reconnected logs the recovery when the outage's loss or a failure was logged.
func (l *listenLog) reconnected(attempts int) {
	if !l.reported && !l.failLogged {
		return
	}
	if lg := l.log(); lg != nil {
		lg.Info("store: reconnected the schema invalidation listener; dropping cached schemas", "attempts", attempts)
	}
}

// fromNotice splits a notice payload into the connection ID and whether this
// process sent it. A payload without an origin prefix is forwarded as is.
func (p *Postgres) fromNotice(payload string) (id string, own bool) {
	origin, id, ok := strings.Cut(payload, ":")
	if !ok {
		return payload, false
	}
	return id, origin == p.origin
}

// listen opens a connection that listens on qh_catalog, with TCP keepalive
// set to listenKeepAlive; each attempt is bounded by listenTimeout.
func (p *Postgres) listen(ctx context.Context) (*pgx.Conn, error) {
	ctx, cancel := context.WithTimeout(ctx, listenTimeout)
	defer cancel()
	cfg := p.cfg.Copy()
	cfg.DialFunc = keepAliveDial(cfg.DialFunc, listenKeepAlive)
	conn, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		return nil, err
	}
	if _, err := conn.Exec(ctx, `LISTEN qh_catalog`); err != nil {
		closeConn(conn)
		return nil, err
	}
	return conn, nil
}

func closeConn(conn *pgx.Conn) {
	ctx, cancel := context.WithTimeout(context.Background(), closeTimeout)
	defer cancel()
	conn.Close(ctx)
}

// keepAliveDial wraps dial (which honours connect_timeout and any other dial
// settings) so TCP connections it opens use keepalive probes every period.
// pgx's default dialer already has Go's 15s default; this pins the value
// whatever dialer is configured. The listener's ping is the real detector.
func keepAliveDial(dial pgconn.DialFunc, period time.Duration) pgconn.DialFunc {
	if dial == nil {
		dial = (&net.Dialer{}).DialContext
	}
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		c, err := dial(ctx, network, addr)
		if tc, ok := c.(*net.TCPConn); ok && err == nil {
			_ = tc.SetKeepAliveConfig(net.KeepAliveConfig{Enable: true, Idle: period, Interval: period})
		}
		return c, err
	}
}
