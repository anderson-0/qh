package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/stdlib"
)

// pgOpenRetry bounds how long OpenPostgres waits for an unreachable
// database, so instances can start before it.
var pgOpenRetry = 30 * time.Second

// migrateLock is the advisory-lock key that serialises schema migrations.
const migrateLock = 7_212_011_970

// bootstrapLock is the advisory-lock key that serialises first-key creation.
const bootstrapLock = 7_212_011_971

// pgMigrations[i] moves the qh schema from version i to i+1.
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
}

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
			return nil, fmt.Errorf("store: cannot read QH_STORE_PASSWORD_FILE %s", f)
		}
		cfg.Password = strings.TrimRight(string(b), "\r\n")
	}
	db := stdlib.OpenDB(*cfg)
	db.SetMaxOpenConns(10)
	db.SetMaxIdleConns(10)
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
	if _, err := tx.ExecContext(ctx, `CREATE SCHEMA IF NOT EXISTS qh`); err != nil {
		return err
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

func (p *Postgres) Close() error                   { return p.db.Close() }
func (p *Postgres) Ping(ctx context.Context) error { return p.db.PingContext(ctx) }

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
	c, err := scanPGConnection(p.db.QueryRowContext(ctx, `SELECT `+connColumns+` FROM qh.connections
		WHERE workspace_id = $1 AND name = $2`, workspaceID, name))
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
	rows, err := p.db.QueryContext(ctx, `SELECT `+connColumns+` FROM qh.connections WHERE workspace_id = $1 ORDER BY name`, workspaceID)
	return p.listConnections(ctx, rows, err)
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
	k, err := scanPGKey(p.db.QueryRowContext(ctx, `SELECT `+keyColumns+` FROM qh.api_keys WHERE prefix = $1`, prefix))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return k, err
}

func (p *Postgres) listKeys(ctx context.Context, where string, args ...any) ([]*APIKey, error) {
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
	err := p.db.QueryRowContext(ctx, `SELECT count(*) FROM qh.api_keys`).Scan(&n)
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
	go func() {
		defer close(out)
		backoff := 500 * time.Millisecond
		for {
			n, err := conn.WaitForNotification(ctx)
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
			for {
				select {
				case <-ctx.Done():
					return
				case <-time.After(backoff):
				}
				if conn, err = p.listen(ctx); err == nil {
					backoff = 500 * time.Millisecond
					break
				}
				backoff = min(backoff*2, 30*time.Second)
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

// fromNotice splits a notice payload into the connection ID and whether this
// process sent it. A payload without an origin prefix is forwarded as is.
func (p *Postgres) fromNotice(payload string) (id string, own bool) {
	origin, id, ok := strings.Cut(payload, ":")
	if !ok {
		return payload, false
	}
	return id, origin == p.origin
}

// listen opens a connection that listens on qh_catalog; each attempt is
// bounded by listenTimeout.
func (p *Postgres) listen(ctx context.Context) (*pgx.Conn, error) {
	ctx, cancel := context.WithTimeout(ctx, listenTimeout)
	defer cancel()
	conn, err := pgx.ConnectConfig(ctx, p.cfg.Copy())
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
