// Package postgres is the PostgreSQL engine.
//
// Every statement goes through the extended query protocol
// (pgx.QueryExecModeExec via Query, never an argument-less Exec, which pgx
// sends with the simple protocol). The server rejects more than one statement
// in the extended protocol, so `COMMIT; DELETE ...` cannot leave the
// read-only transaction.
package postgres

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/anderson-0/qh/internal/engine"
	"github.com/anderson-0/qh/internal/qerr"
	"github.com/anderson-0/qh/internal/sqlx"
)

func init() { engine.Register(Engine{}) }

// slotWait is how long a query waits for a free pooled connection.
var slotWait = 10 * time.Second

// Engine is the PostgreSQL engine.
type Engine struct{}

func (Engine) Name() string          { return "postgres" }
func (Engine) Dialect() sqlx.Dialect { return sqlx.Postgres }

var kvPassword = regexp.MustCompile(`(?i)(password\s*=\s*)('(?:[^'\\]|\\.)*'|\S+)`)

func (Engine) Inspect(dsn string) (engine.DSNInfo, error) {
	cfg, err := pgconn.ParseConfig(dsn)
	if err != nil {
		return engine.DSNInfo{}, qerr.New(qerr.Invalid, "invalid postgres connection string")
	}
	return engine.DSNInfo{
		Host: cfg.Host, Port: int(cfg.Port), Database: cfg.Database, User: cfg.User,
		Password: cfg.Password, Redacted: redact(dsn),
	}, nil
}

// placeholder stands in for the password while the URL is re-encoded; the
// URL encoder would escape "***" itself.
const placeholder = "QHREDACTEDPASSWORD"

func redact(dsn string) string {
	if u, err := url.Parse(dsn); err == nil && (u.Scheme == "postgres" || u.Scheme == "postgresql") {
		if u.User != nil {
			if _, ok := u.User.Password(); ok {
				u.User = url.UserPassword(u.User.Username(), placeholder)
			}
		}
		q := u.Query()
		redacted := false
		for name := range q {
			if strings.Contains(strings.ToLower(name), "password") { // password, sslpassword, ...
				q.Set(name, placeholder)
				redacted = true
			}
		}
		if redacted {
			u.RawQuery = q.Encode()
		}
		return strings.ReplaceAll(u.String(), placeholder, "***")
	}
	return kvPassword.ReplaceAllString(dsn, "${1}***")
}

// serverKeys are the only settings a connection string may carry in server
// mode. Everything else is refused, in particular the keys that make pgconn
// read server files (passfile, service, servicefile, sslcert, sslkey,
// sslrootcert). pgconn accepts dbname and database for one another, and
// ssl=true for sslmode=require.
var serverKeys = []string{
	"host", "port", "dbname", "user", "password", "sslmode", "sslsni", "sslnegotiation", "connect_timeout",
	"target_session_attrs", "application_name", "channel_binding", "require_auth",
	"min_protocol_version", "max_protocol_version",
}

// serverKeysNoPassword is serverKeys without "password".
var serverKeysNoPassword = func() []string {
	var out []string
	for _, k := range serverKeys {
		if k != "password" {
			out = append(out, k)
		}
	}
	return out
}()

// noFiles clears the file settings pgconn otherwise takes from $HOME
// (~/.pgpass and ~/.postgresql/postgresql.{crt,key}, root.crt) or from PG*
// variables. A setting in the connection string wins over both.
var noFiles = []string{"passfile", "sslcert", "sslkey", "sslrootcert"}

func refusedSettings() error {
	return qerr.New(qerr.Invalid, "this server only accepts these Postgres connection settings: "+strings.Join(serverKeys, ", ")).
		WithHint("Remove the other settings from the connection string")
}

// withSettings appends key=value settings to dsn, in its own form (URL or
// keyword/value). Every append to a connection string goes through here.
func withSettings(dsn string, keys []string, value string) string {
	if strings.HasPrefix(dsn, "postgres://") || strings.HasPrefix(dsn, "postgresql://") {
		pairs := make([]string, len(keys))
		for i, k := range keys {
			pairs[i] = k + "=" + url.QueryEscape(value)
		}
		// Find the query the way pgconn does: after the user info (which ends
		// at the first '@' seen before any '/'), at the first '?'.
		rest := dsn[strings.Index(dsn, "://")+3:]
		if i := strings.IndexAny(rest, "@/"); i >= 0 && rest[i] == '@' {
			rest = rest[i+1:]
		}
		switch {
		case !strings.Contains(rest, "?"):
			return dsn + "?" + strings.Join(pairs, "&")
		case strings.HasSuffix(dsn, "?") || strings.HasSuffix(dsn, "&"):
			return dsn + strings.Join(pairs, "&")
		default:
			return dsn + "&" + strings.Join(pairs, "&")
		}
	}
	pairs := make([]string, len(keys))
	for i, k := range keys {
		pairs[i] = k + "='" + value + "'"
	}
	// Two spaces: in an unquoted value a trailing backslash escapes the next
	// character, so with one space `application_name=x\` would swallow the
	// first appended setting into its value.
	return dsn + "  " + strings.Join(pairs, " ")
}

// usesOnly reports whether every setting in dsn is in allowed, without
// reading any file. pgconn checks the allow-list first; the invalid
// connect_timeout appended here (the last occurrence of a key wins) then
// stops it before it opens a passfile, service file or certificate.
func usesOnly(dsn string, allowed []string) bool {
	_, err := pgconn.ParseConfigWithOptions(withSettings(dsn, []string{"connect_timeout"}, "-1"),
		pgconn.ParseConfigOptions{ConnStringAllowedKeys: allowed})
	// pgconn's ParseConfigError has no exported reason; the message is fixed.
	return err == nil || !strings.Contains(err.Error(), "is not in ConnStringAllowedKeys")
}

// checkServerDSN validates a server-mode connection string and reports
// whether it carries a password itself. Errors never quote dsn.
func checkServerDSN(dsn string) (hasPassword bool, err error) {
	if !usesOnly(dsn, serverKeys) {
		return false, refusedSettings()
	}
	if _, err := pgconn.ParseConfigWithOptions(withSettings(dsn, noFiles, ""),
		pgconn.ParseConfigOptions{ConnStringAllowedKeys: append(append([]string{}, serverKeys...), noFiles...)}); err != nil {
		return false, qerr.New(qerr.Invalid, "invalid postgres connection string")
	}
	return !usesOnly(dsn, serverKeysNoPassword), nil
}

// CheckServerDSN implements engine.ServerDSNChecker: server mode accepts
// only serverKeys, so a tenant cannot make qh read server files.
func (Engine) CheckServerDSN(dsn string) error {
	_, err := checkServerDSN(dsn)
	return err
}

// withoutClientCert returns c without any client certificate.
func withoutClientCert(c *tls.Config) *tls.Config {
	if c == nil {
		return nil
	}
	c = c.Clone()
	c.Certificates, c.GetClientCertificate = nil, nil
	return c
}

// poolConfig builds the pool configuration for spec. In server mode
// (spec.Dial set) the connection string may use only serverKeys, the
// password comes only from it (never PGPASSWORD, PGPASSFILE, ~/.pgpass or a
// service file), and no default certificate or pgpass file applies.
func poolConfig(spec engine.ConnSpec) (*pgxpool.Config, error) {
	dsn, hasPassword := spec.DSN, true
	if spec.Dial != nil {
		var err error
		if hasPassword, err = checkServerDSN(dsn); err != nil {
			return nil, err
		}
		dsn = withSettings(dsn, noFiles, "")
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, qerr.New(qerr.Invalid, "invalid postgres connection string")
	}
	if spec.Dial != nil {
		cc := cfg.ConnConfig
		if !hasPassword {
			cc.Password = ""
		}
		cc.TLSConfig = withoutClientCert(cc.TLSConfig)
		for _, fb := range cc.Fallbacks {
			fb.TLSConfig = withoutClientCert(fb.TLSConfig)
		}
	}
	cfg.MaxConns = int32(max(spec.PoolSize, 1))
	cfg.MaxConnIdleTime = 10 * time.Minute
	cfg.ConnConfig.DefaultQueryExecMode = pgx.QueryExecModeExec
	if spec.Dial != nil {
		cfg.ConnConfig.DialFunc = spec.Dial
		// Resolve inside Dial only: pgx would otherwise look the host up
		// itself first, and the policy must see (and dial) the one answer.
		cfg.ConnConfig.LookupFunc = func(_ context.Context, host string) ([]string, error) { return []string{host}, nil }
	}
	if cfg.ConnConfig.ConnectTimeout == 0 {
		cfg.ConnConfig.ConnectTimeout = 10 * time.Second
	}
	cfg.ConnConfig.RuntimeParams["application_name"] = "qh"
	cfg.AfterRelease = resetSession
	return cfg, nil
}

func (Engine) Open(ctx context.Context, spec engine.ConnSpec) (engine.Conn, error) {
	cfg, err := poolConfig(spec)
	if err != nil {
		return nil, err
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, qerr.Wrap(qerr.ConnectionFailed, "cannot create connection pool", err)
	}
	c := &conn{pool: pool, readOnly: spec.Mode != engine.ModeRW}
	if _, err := c.Ping(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	return c, nil
}

// resetSession runs when a connection goes back to the pool, on every path
// (success, query error, rollback), after its transaction has ended. A
// statement may have changed session state (set_config(..., false), advisory
// locks, temp objects, prepared statements); DISCARD ALL wipes all of it so
// the next user starts clean. pgxpool keeps the connection checked out until
// this returns, and destroys it when it returns false, so a failed reset
// never returns a dirty connection to the pool.
func resetSession(c *pgx.Conn) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := c.Exec(ctx, "DISCARD ALL", pgx.QueryExecModeSimpleProtocol)
	return err == nil
}

type conn struct {
	pool     *pgxpool.Pool
	readOnly bool
}

func (c *conn) Close() error {
	c.pool.Close()
	return nil
}

func (c *conn) Ping(ctx context.Context) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	var v string
	if err := c.pool.QueryRow(ctx, "SHOW server_version", pgx.QueryExecModeExec).Scan(&v); err != nil {
		return "", c.classify(ctx, err)
	}
	return "PostgreSQL " + v, nil
}

func (c *conn) Query(ctx context.Context, q string, o engine.QueryOpts) (*engine.Result, error) {
	access, err := sqlx.Classify(q, sqlx.Postgres)
	if err != nil {
		return nil, err
	}
	kw := sqlx.FirstKeyword(q, sqlx.Postgres)
	cursor := access == sqlx.Read && (kw == "select" || kw == "values" || kw == "table" || kw == "with")
	return c.run(ctx, q, o, cursor, o.ReadOnly || c.readOnly)
}

func (c *conn) Explain(ctx context.Context, q string, o engine.QueryOpts) (*engine.Result, error) {
	if _, err := sqlx.Classify(q, sqlx.Postgres); err != nil {
		return nil, err
	}
	return c.run(ctx, "EXPLAIN (FORMAT TEXT) "+q, engine.QueryOpts{MaxRows: 500, Timeout: o.Timeout}, false, true)
}

func (c *conn) acquire(ctx context.Context) (*pgxpool.Conn, error) {
	actx, cancel := context.WithTimeout(ctx, slotWait)
	defer cancel()
	pc, err := c.pool.Acquire(actx)
	if err != nil {
		st := c.pool.Stat()
		if ctx.Err() == nil && errors.Is(actx.Err(), context.DeadlineExceeded) && st.AcquiredConns() >= st.MaxConns() {
			return nil, qerr.New(qerr.Busy, "all connections to this database are in use").WithHint("Retry shortly")
		}
		return nil, c.classify(ctx, err)
	}
	return pc, nil
}

// exec runs a statement that returns nothing, through the extended protocol.
func exec(ctx context.Context, tx pgx.Tx, sql string, args ...any) error {
	rows, err := tx.Query(ctx, sql, append([]any{pgx.QueryExecModeExec}, args...)...)
	if err != nil {
		return err
	}
	rows.Close()
	return rows.Err()
}

func (c *conn) run(ctx context.Context, q string, o engine.QueryOpts, cursor, readOnly bool) (*engine.Result, error) {
	start := time.Now()
	timeout := o.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	// The server-side statement_timeout fires first; this is a backstop.
	ctx, cancel := context.WithTimeout(ctx, timeout+2*time.Second)
	defer cancel()
	pc, err := c.acquire(ctx)
	if err != nil {
		return nil, err
	}
	defer pc.Release()
	mode := pgx.ReadWrite
	if readOnly {
		mode = pgx.ReadOnly
	}
	tx, err := pc.BeginTx(ctx, pgx.TxOptions{AccessMode: mode})
	if err != nil {
		return nil, c.classify(ctx, err)
	}
	defer tx.Rollback(context.WithoutCancel(ctx))
	err = exec(ctx, tx, `SELECT set_config('statement_timeout', $1, true), set_config('TimeZone', 'UTC', true), set_config('DateStyle', 'ISO, YMD', true), set_config('bytea_output', 'hex', true)`,
		strconv.FormatInt(timeout.Milliseconds(), 10))
	if err != nil {
		return nil, c.classify(ctx, err)
	}
	limit := o.MaxRows
	if limit <= 0 {
		limit = 100
	}
	stmt := q
	if cursor {
		if err := exec(ctx, tx, "DECLARE qh_cur NO SCROLL CURSOR FOR "+q); err != nil {
			return nil, c.classify(ctx, err)
		}
		stmt = fmt.Sprintf("FETCH FORWARD %d FROM qh_cur", limit+1)
	}
	rows, err := tx.Query(ctx, stmt, pgx.QueryExecModeExec)
	if err != nil {
		return nil, c.classify(ctx, err)
	}
	res, err := collect(rows, pc.Conn().TypeMap(), limit)
	if err != nil {
		return nil, c.classify(ctx, err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, c.classify(ctx, err)
	}
	res.Duration = time.Since(start)
	return res, nil
}

func collect(rows pgx.Rows, tm *pgtype.Map, limit int) (*engine.Result, error) {
	defer rows.Close()
	fds := rows.FieldDescriptions()
	res := &engine.Result{RowsAffected: -1}
	for _, fd := range fds {
		if fd.Format != pgx.TextFormatCode {
			return nil, fmt.Errorf("postgres: unexpected binary result for column %q", fd.Name)
		}
		res.Columns = append(res.Columns, engine.Column{Name: fd.Name, Type: typeName(tm, fd.DataTypeOID)})
	}
	for rows.Next() {
		if len(res.Rows) == limit {
			res.Truncated = true
			break
		}
		raw := rows.RawValues()
		row := make([]engine.Value, len(raw))
		for i, b := range raw {
			row[i] = toValue(fds[i].DataTypeOID, b)
		}
		res.Rows = append(res.Rows, row)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(fds) == 0 {
		res.RowsAffected = rows.CommandTag().RowsAffected()
	}
	return res, nil
}

func typeName(tm *pgtype.Map, oid uint32) string {
	if t, ok := tm.TypeForOID(oid); ok {
		return t.Name
	}
	return strconv.FormatUint(uint64(oid), 10)
}

// toValue converts a text-format value to its canonical form.
func toValue(oid uint32, b []byte) engine.Value {
	if b == nil {
		return engine.Value{Null: true}
	}
	s := string(b)
	switch oid {
	case pgtype.BoolOID:
		switch s {
		case "t":
			s = "true"
		case "f":
			s = "false"
		}
	case pgtype.ByteaOID:
		if strings.HasPrefix(s, `\x`) {
			return engine.Value{Binary: true, Size: (len(s) - 2) / 2}
		}
		return engine.Value{Binary: true, Size: len(b)}
	case pgtype.TimestamptzOID:
		s = strings.Replace(s, " ", "T", 1)
		if strings.HasSuffix(s, "+00") {
			s = strings.TrimSuffix(s, "+00") + "Z"
		}
	case pgtype.TimestampOID:
		s = strings.Replace(s, " ", "T", 1)
	case pgtype.JSONOID, pgtype.JSONBOID:
		var buf bytes.Buffer
		if json.Compact(&buf, b) == nil {
			s = buf.String()
		}
	}
	return engine.Value{Text: s}
}

var (
	colRe = regexp.MustCompile(`column "?([^"\s]+)"? does not exist`)
	relRe = regexp.MustCompile(`relation "([^"]+)" does not exist`)
)

func (c *conn) classify(ctx context.Context, err error) error {
	var qe *qerr.Error
	if errors.As(err, &qe) {
		return qe
	}
	var pe *pgconn.PgError
	var ne net.Error
	// Connect errors first: pgconn wraps the server's PgError and any dial
	// timeout inside them.
	var ce *pgconn.ConnectError
	if errors.As(err, &ce) {
		switch {
		case errors.As(err, &pe) && strings.HasPrefix(pe.Code, "28"):
			return qerr.Wrap(qerr.ConnectionFailed, "authentication failed: "+pe.Message, err)
		case errors.As(err, &pe) && pe.Code == "3D000":
			return qerr.Wrap(qerr.ConnectionFailed, pe.Message, err)
		case errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &ne) && ne.Timeout()):
			return qerr.Wrap(qerr.ConnectionFailed, "timed out connecting to the database", err)
		}
		// qerr collapses the multi-line dial details onto one line.
		return qerr.Wrap(qerr.ConnectionFailed, "cannot reach the database: "+err.Error(), err)
	}
	timeout := qerr.Wrap(qerr.Timeout, "query exceeded the connection timeout", err).
		WithHint("Add WHERE/LIMIT or narrow the range")
	if errors.As(err, &pe) {
		switch pe.Code {
		case "57014":
			return timeout
		case "25006":
			return qerr.Wrap(qerr.ReadOnly, "connection is read-only; only statements that read data are allowed", err).
				WithHint("Use a read-write connection to change data")
		case "42703":
			e := qerr.Wrap(qerr.QueryError, pe.Message, err)
			if m := colRe.FindStringSubmatch(pe.Message); m != nil {
				e.UnknownColumn = m[1]
			}
			return e
		case "42P01":
			e := qerr.Wrap(qerr.QueryError, pe.Message, err)
			if m := relRe.FindStringSubmatch(pe.Message); m != nil {
				e.UnknownTable = m[1]
			}
			return e
		}
		msg := pe.Message
		if pe.Detail != "" {
			msg += " (" + pe.Detail + ")"
		}
		return qerr.Wrap(qerr.QueryError, msg, err).WithHint(pe.Hint)
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return timeout
	}
	if errors.As(err, &ne) || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return qerr.Wrap(qerr.ConnectionFailed, "cannot reach the database: "+err.Error(), err)
	}
	return qerr.Wrap(qerr.QueryError, err.Error(), err)
}
