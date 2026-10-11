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
	"cmp"
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

// BuildDSN implements engine.DSNBuilder.
func (Engine) BuildDSN(f engine.DSNFields) (string, error) { return engine.BuildURL("postgres", f) }

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
	// The settings go both before and after dsn. A tenant's last value can
	// swallow what follows it (`application_name=` takes the next word, since
	// pgconn skips blanks after '='; a trailing backslash escapes the next
	// space), but nothing can swallow the prefix, and the tenant cannot
	// override it because the allow-list refuses these keys. The suffix,
	// after two spaces, keeps the last word when it is not swallowed (the
	// connect_timeout sentinel relies on that).
	joined := strings.Join(pairs, " ")
	return joined + " " + dsn + "  " + joined
}

// usesOnly reports whether every setting in dsn is in allowed, without
// reading any file. pgconn checks the allow-list first; the invalid
// connect_timeout appended here (the last occurrence of a key wins) then
// stops it before it opens a passfile, service file or certificate. If the
// tenant's last value swallows the sentinel, the verdict is still the
// allow-list's (taken before any file access); the parse then continues and
// may read only the operator's own default files (no file key can pass the
// allow-list, and qh serve clears PG*), and its result is discarded.
func usesOnly(dsn string, allowed []string) bool {
	_, err := pgconn.ParseConfigWithOptions(withSettings(dsn, []string{"connect_timeout"}, "-1"),
		pgconn.ParseConfigOptions{ConnStringAllowedKeys: allowed})
	// pgconn's ParseConfigError has no exported reason; the message is fixed.
	return err == nil || !strings.Contains(err.Error(), "is not in ConnStringAllowedKeys")
}

// questionInBrackets reports whether a URL connection string has a '?'
// inside a bracketed host. pgconn reads that '?' as part of the host, so the
// settings withSettings appends would not reach the query.
func questionInBrackets(dsn string) bool {
	if !strings.HasPrefix(dsn, "postgres://") && !strings.HasPrefix(dsn, "postgresql://") {
		return false
	}
	rest := dsn[strings.Index(dsn, "://")+3:]
	if i := strings.IndexAny(rest, "@/"); i >= 0 && rest[i] == '@' {
		rest = rest[i+1:]
	}
	inside := false
	for _, r := range rest {
		switch {
		case r == '[':
			inside = true
		case r == ']':
			inside = false
		case r == '?' && inside:
			return true
		case r == '/' && !inside:
			return false
		}
	}
	return false
}

// checkServerDSN validates a server-mode connection string and reports
// whether it carries a password itself. Errors never quote dsn.
func checkServerDSN(dsn string) (hasPassword bool, err error) {
	if questionInBrackets(dsn) {
		return false, qerr.New(qerr.Invalid, "invalid postgres connection string")
	}
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

// applyTLS replaces the connection string's TLS settings with t, for the
// main host and every fallback host. It drops the extra plain/TLS attempt
// that sslmode=prefer or allow adds for each host.
func applyTLS(cc *pgconn.Config, t *engine.TLS) error {
	c, err := t.Config(cc.Host)
	if err != nil {
		return err
	}
	cc.TLSConfig = c
	seen := map[string]bool{net.JoinHostPort(cc.Host, strconv.Itoa(int(cc.Port))): true}
	var fallbacks []*pgconn.FallbackConfig
	for _, fb := range cc.Fallbacks {
		k := net.JoinHostPort(fb.Host, strconv.Itoa(int(fb.Port)))
		if seen[k] {
			continue
		}
		seen[k] = true
		if fb.TLSConfig, err = t.Config(fb.Host); err != nil {
			return err
		}
		fallbacks = append(fallbacks, fb)
	}
	cc.Fallbacks = fallbacks
	return nil
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
	if spec.TLS != nil {
		if err := applyTLS(&cfg.ConnConfig.Config, spec.TLS); err != nil {
			return nil, err
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

// run executes q in one pipelined round trip:
//
//	BEGIN [READ ONLY]; set_config(...); [DECLARE qh_cur ... FOR q; FETCH n] | q; COMMIT; Sync
//	ROLLBACK; Sync      -- ends the transaction a failed statement left open
//	DISCARD ALL; Sync   -- wipes session state the statement may have changed
//
// Each statement is its own Parse message in the extended protocol, which
// rejects more than one statement, so q cannot end the read-only
// transaction. A statement's error makes the server skip the rest of its
// segment up to the Sync. DISCARD ALL cannot run inside a transaction or a
// pipelined implicit transaction, so it gets a segment of its own; a
// statement may have changed session state (set_config(..., false),
// advisory locks, temp objects, prepared statements), and the next user must
// start clean. A connection whose reset did not complete is closed, so
// pgxpool destroys it instead of pooling it.
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
	limit := o.MaxRows
	if limit <= 0 {
		limit = 100
	}
	begin := "BEGIN READ WRITE"
	if readOnly {
		begin = "BEGIN READ ONLY"
	}
	stmts := []string{begin, `SELECT set_config('statement_timeout', $1, true), set_config('TimeZone', 'UTC', true), set_config('DateStyle', 'ISO, YMD', true), set_config('bytea_output', 'hex', true)`}
	if cursor {
		stmts = append(stmts, "DECLARE qh_cur NO SCROLL CURSOR FOR "+q, fmt.Sprintf("FETCH FORWARD %d FROM qh_cur", limit+1))
	} else {
		stmts = append(stmts, q)
	}
	data := len(stmts) - 1
	stmts = append(stmts, "COMMIT")
	ms := [][]byte{[]byte(strconv.FormatInt(timeout.Milliseconds(), 10))}

	pgc := pc.Conn().PgConn()
	p := pgc.StartPipeline(ctx)
	for i, s := range stmts {
		var params [][]byte
		if i == 1 {
			params = ms
		}
		p.SendQueryParams(s, params, nil, nil, nil)
	}
	p.SendPipelineSync()
	p.SendQueryParams("ROLLBACK", nil, nil, nil, nil)
	p.SendPipelineSync()
	p.SendQueryParams("DISCARD ALL", nil, nil, nil, nil)
	p.SendPipelineSync()

	var (
		res       *engine.Result
		queryErr  error // the first error of the statement segment
		resetErr  error // an error of the ROLLBACK or DISCARD ALL segment
		connErr   error // the pipeline broke
		syncs, ix int
	)
	if err := p.Flush(); err != nil {
		connErr = err
	}
	for connErr == nil && syncs < 3 {
		r, err := p.GetResults()
		var pe *pgconn.PgError
		switch {
		case errors.As(err, &pe):
			if syncs == 0 {
				queryErr = cmp.Or(queryErr, err)
			} else {
				resetErr = cmp.Or(resetErr, err)
			}
			continue
		case err != nil:
			connErr = err
			continue
		}
		switch r := r.(type) {
		case *pgconn.ResultReader:
			if syncs == 0 && ix == data {
				res, err = collect(r, pc.Conn().TypeMap(), limit)
			} else {
				_, err = r.Close()
			}
			ix++
			if err != nil {
				if !errors.As(err, &pe) {
					connErr = err
				} else if syncs == 0 {
					queryErr = cmp.Or(queryErr, err)
				} else {
					resetErr = cmp.Or(resetErr, err)
				}
			}
		case *pgconn.PipelineSync:
			syncs++
		default:
			connErr = fmt.Errorf("postgres: unexpected pipeline result %T", r)
		}
	}
	if err := p.Close(); err != nil && connErr == nil && syncs < 3 {
		connErr = err
	}
	if connErr != nil || resetErr != nil {
		// The session may hold state from q: never pool it.
		_ = pgc.Close(context.WithoutCancel(ctx))
	}
	switch {
	case queryErr != nil:
		return nil, c.classify(ctx, queryErr)
	case connErr != nil && syncs == 0:
		return nil, c.classify(ctx, connErr)
	case res == nil:
		return nil, c.classify(ctx, errors.New("postgres: statement returned no result"))
	}
	res.Duration = time.Since(start)
	return res, nil
}

// collect reads at most limit rows from r and closes it.
func collect(r *pgconn.ResultReader, tm *pgtype.Map, limit int) (*engine.Result, error) {
	fds := r.FieldDescriptions()
	res := &engine.Result{RowsAffected: -1}
	for _, fd := range fds {
		if fd.Format != pgx.TextFormatCode {
			r.Close()
			return nil, fmt.Errorf("postgres: unexpected binary result for column %q", fd.Name)
		}
		res.Columns = append(res.Columns, engine.Column{Name: fd.Name, Type: typeName(tm, fd.DataTypeOID)})
	}
	for r.NextRow() {
		if len(res.Rows) == limit {
			res.Truncated = true
			continue // Close would drain the rest anyway
		}
		raw := r.Values()
		row := make([]engine.Value, len(raw))
		for i, b := range raw {
			row[i] = toValue(fds[i].DataTypeOID, b)
		}
		res.Rows = append(res.Rows, row)
	}
	tag, err := r.Close()
	if err != nil {
		return nil, err
	}
	if len(fds) == 0 {
		res.RowsAffected = tag.RowsAffected()
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
