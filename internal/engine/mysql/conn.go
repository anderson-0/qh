package mysql

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"strings"
	"time"

	gomysql "github.com/go-sql-driver/mysql"

	"github.com/anderson-0/qh/internal/engine"
	"github.com/anderson-0/qh/internal/qerr"
	"github.com/anderson-0/qh/internal/sqlx"
)

func init() {
	engine.Register(Engine{})
	engine.Register(Engine{name: "mariadb"})
	// The driver prints connection noise to the process's stderr; qh reports
	// errors itself, and stderr must stay clean for the CLI.
	_ = gomysql.SetLogger(log.New(io.Discard, "", 0))
}

// slotWait is how long a query waits for a free pooled connection.
var slotWait = 10 * time.Second

// drainBudget is how many surplus rows are read past the cap before the
// connection is killed instead (see collect).
const drainBudget = 1000

func (Engine) Open(ctx context.Context, spec engine.ConnSpec) (engine.Conn, error) {
	cfg, err := parseConfig(spec.DSN)
	if err != nil {
		return nil, err
	}
	if spec.Dial != nil {
		cfg.DialFunc = spec.Dial
	}
	if spec.TLS != nil {
		host, _, err := net.SplitHostPort(cfg.Addr)
		if err != nil {
			host = cfg.Addr
		}
		c, err := spec.TLS.Config(host)
		if err != nil {
			return nil, err
		}
		// cfg.TLS takes priority over the tls= setting; "false" keeps a nil
		// TLS from being re-derived from the connection string.
		cfg.TLS, cfg.AllowFallbackToPlaintext = c, false
		if c == nil {
			cfg.TLSConfig = "false"
		}
	}
	if spec.Password != "" {
		cfg.Passwd = spec.Password
	}
	connector, err := gomysql.NewConnector(cfg)
	if err != nil {
		return nil, invalidDSN()
	}
	db := sql.OpenDB(connector)
	size := max(spec.PoolSize, 1)
	db.SetMaxOpenConns(size)
	db.SetMaxIdleConns(size)
	db.SetConnMaxIdleTime(10 * time.Minute)
	pctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	var v string
	if err := db.QueryRowContext(pctx, "SELECT VERSION()").Scan(&v); err != nil {
		db.Close()
		return nil, connectErr(err)
	}
	f, _ := parseVersion(v)
	c := &conn{db: db, readOnly: spec.Mode != engine.ModeRW, dbName: cfg.DBName, flavor: f, mariaDB111: atLeast(v, 11, 1)}
	if f == flavorMySQL {
		c.writes = newWriteCanceler(c, connector, size)
	}
	return c, nil
}

type conn struct {
	db       *sql.DB
	writes   *writeCanceler
	readOnly bool
	dbName   string
	flavor   flavor
	// mariaDB111: the server reports version 11.1 or later; it matters only
	// for MariaDB (see readOnlyVar).
	mariaDB111 bool
}

func (c *conn) Close() error {
	var err error
	if c.writes != nil {
		err = c.writes.Close()
	}
	return errors.Join(err, c.db.Close())
}

func (c *conn) Ping(ctx context.Context) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	var v string
	if err := c.db.QueryRowContext(ctx, "SELECT VERSION()").Scan(&v); err != nil {
		return "", connectErr(err)
	}
	_, display := parseVersion(v)
	return display, nil
}

// Query runs one statement. The engine classifies statements itself because
// read-only transactions do not stop SELECT ... INTO OUTFILE or LOAD DATA.
func (c *conn) Query(ctx context.Context, q string, o engine.QueryOpts) (*engine.Result, error) {
	access, err := sqlx.Classify(q, sqlx.MySQL)
	if err != nil {
		return nil, err
	}
	readOnly := o.ReadOnly || c.readOnly
	if readOnly && access != sqlx.Read {
		return nil, readOnlyErr()
	}
	// Defence in depth: the lexer cannot model every quoting mode (sql_mode
	// ANSI_QUOTES and NO_BACKSLASH_ESCAPES change where strings end), so a
	// file-writing clause could be hidden inside what the lexer sees as a
	// string or comment. No read needs these words, so refuse them outright.
	if readOnly {
		if lq := strings.ToLower(q); strings.Contains(lq, "outfile") || strings.Contains(lq, "dumpfile") {
			return nil, readOnlyErr()
		}
	}
	return c.run(ctx, q, o, access, readOnly)
}

// Explain returns the plan for q. EXPLAIN does not execute the statement, but
// MySQL checks write permission (including read-only transactions) as if it
// did, so a statement that writes is explained on a read-write connection in
// a transaction that is always rolled back, and the connection is discarded.
func (c *conn) Explain(ctx context.Context, q string, o engine.QueryOpts) (*engine.Result, error) {
	access, err := sqlx.Classify(q, sqlx.MySQL)
	if err != nil {
		return nil, err
	}
	stmt := "EXPLAIN FORMAT=TREE " + q
	if c.flavor == flavorMariaDB {
		stmt = "EXPLAIN " + q
	}
	qo := engine.QueryOpts{MaxRows: 500, Timeout: o.Timeout}
	var r *engine.Result
	switch {
	case access == sqlx.Read:
		r, err = c.run(ctx, stmt, qo, sqlx.Read, true)
	case o.ReadOnly || c.readOnly:
		return nil, qerr.New(qerr.Invalid, "EXPLAIN of a statement that writes needs a read-write connection on MySQL/MariaDB").
			WithHint("Use a read-write connection, or explain a SELECT")
	default:
		r, err = c.exec(ctx, stmt, qo, access, false)
	}
	if err != nil {
		return nil, err
	}
	return planResult(r), nil
}

// setupSQL sets the per-call limits. A read-only call also makes the session
// read-only and autocommit, so the statement runs as its own read-only
// transaction without a START TRANSACTION/COMMIT round trip each.
func (c *conn) setupSQL(rows int, timeout time.Duration, readOnly bool) string {
	var s string
	if c.flavor == flavorMariaDB {
		s = fmt.Sprintf("SET SESSION sql_select_limit = %d, max_statement_time = %.3f, time_zone = '+00:00'", rows, timeout.Seconds())
	} else {
		s = fmt.Sprintf("SET SESSION sql_select_limit = %d, max_execution_time = %d, time_zone = '+00:00'", rows, timeout.Milliseconds())
	}
	if readOnly {
		s += ", autocommit = 1, " + c.readOnlyVar() + " = 1"
	}
	return s
}

// readOnlyVar is the session variable behind SET TRANSACTION READ ONLY.
// MySQL removed tx_read_only in 8.0.3. MariaDB added transaction_read_only in
// 11.1 and deprecated tx_read_only there; older versions have only the latter.
func (c *conn) readOnlyVar() string {
	if c.flavor == flavorMariaDB && !c.mariaDB111 {
		return "tx_read_only"
	}
	return "transaction_read_only"
}

func (c *conn) resetSQL() string {
	timeout := "max_execution_time"
	if c.flavor == flavorMariaDB {
		timeout = "max_statement_time"
	}
	return "SET @qh_released = RELEASE_ALL_LOCKS(), SESSION sql_select_limit = DEFAULT, " + timeout +
		" = DEFAULT, autocommit = DEFAULT, " + c.readOnlyVar() + " = DEFAULT"
}

func (c *conn) acquire(ctx context.Context) (*sql.Conn, error) {
	actx, cancel := context.WithTimeout(ctx, slotWait)
	defer cancel()
	sc, err := c.db.Conn(actx)
	if err != nil {
		if ctx.Err() == nil && errors.Is(err, context.DeadlineExceeded) {
			return nil, qerr.New(qerr.Busy, "all connections to this database are in use").WithHint("Retry shortly")
		}
		return nil, classify(ctx, err)
	}
	return sc, nil
}

// discard closes sc instead of returning it to the pool.
func discard(sc *sql.Conn) { _ = sc.Raw(func(any) error { return driver.ErrBadConn }) }

// finish ends the transaction and resets session state so the pooled
// connection carries nothing into the next call. Contract: a failure of
// UNLOCK TABLES, COMMIT or ROLLBACK is returned and discards the connection.
// A failure of the reset statement discards the connection too, but is
// returned only when commit is false: after a successful COMMIT the write
// has happened, so reporting an error would make an agent retry it.
func (c *conn) finish(sc *sql.Conn, commit, rw bool) error {
	ctx, cancel := context.WithTimeout(context.Background(), cleanupWait)
	defer cancel()
	r := c.finishContext(ctx, sc, commit, rw)
	if r.Committed {
		return nil
	}
	return r.Err
}

// finishContext records the transaction acknowledgement independently of reset.
// A reset failure cannot erase a successful commit or rollback.
func (c *conn) finishContext(ctx context.Context, sc *sql.Conn, commit, rw bool) cleanupResult {
	r := cleanupResult{}
	fail := func(err error) cleanupResult { discard(sc); r.Err = err; return r }
	if rw {
		if _, err := sc.ExecContext(ctx, "UNLOCK TABLES"); err != nil {
			return fail(err)
		}
	}
	end := "ROLLBACK"
	if commit {
		end = "COMMIT"
	}
	if _, err := sc.ExecContext(ctx, end); err != nil {
		return fail(err)
	}
	r.Committed, r.RolledBack = commit, !commit
	if _, err := sc.ExecContext(ctx, c.resetSQL()); err != nil {
		return fail(err)
	}
	return r
}

func (c *conn) run(ctx context.Context, q string, o engine.QueryOpts, access sqlx.Access, readOnly bool) (*engine.Result, error) {
	if readOnly {
		return c.read(ctx, q, o)
	}
	return c.exec(ctx, q, o, access, true)
}

func callLimits(o engine.QueryOpts) (time.Duration, int) {
	timeout := o.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	limit := o.MaxRows
	if limit <= 0 {
		limit = 100
	}
	return timeout, limit
}

// read runs a statement that only reads in two round trips: the session
// setup (read-only, autocommit) and the statement. The reset runs after the
// result is returned, and the connection goes back to the pool only once it
// has succeeded.
func (c *conn) read(ctx context.Context, q string, o engine.QueryOpts) (*engine.Result, error) {
	start := time.Now()
	timeout, limit := callLimits(o)
	// The server-side timeout fires first; this is a backstop.
	ctx, cancel := context.WithTimeout(ctx, timeout+2*time.Second)
	defer cancel()
	sc, err := c.acquire(ctx)
	if err != nil {
		return nil, err
	}
	if _, err := sc.ExecContext(ctx, c.setupSQL(limit+1, timeout, true)); err != nil {
		discard(sc)
		sc.Close()
		return nil, classify(ctx, err)
	}
	qctx, qcancel := context.WithCancel(ctx)
	defer qcancel()
	res, killed, err := collect(qctx, qcancel, sc, q, limit, true)
	if killed {
		// The driver closed the connection when the query was cancelled.
		discard(sc)
		sc.Close()
		res.Duration = time.Since(start)
		return res, nil
	}
	if err == nil && len(res.Columns) == 0 {
		err = sc.QueryRowContext(ctx, "SELECT ROW_COUNT()").Scan(&res.RowsAffected)
	}
	go c.release(sc)
	if err != nil {
		return nil, classify(ctx, err)
	}
	res.Duration = time.Since(start)
	return res, nil
}

// release resets sc's session and returns it to the pool, or discards it
// when the reset fails.
func (c *conn) release(sc *sql.Conn) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := sc.ExecContext(ctx, c.resetSQL()); err != nil {
		discard(sc)
	}
	sc.Close()
}

// exec is run with a choice of how the transaction ends: commit false always
// rolls back (used to explain a write without applying anything).
func (c *conn) exec(ctx context.Context, q string, o engine.QueryOpts, access sqlx.Access, commit bool) (*engine.Result, error) {
	if c.flavor == flavorMySQL && commit {
		return c.execProtected(ctx, q, o, access)
	}
	start := time.Now()
	timeout, limit := callLimits(o)
	// The server-side timeout fires first; this is a backstop.
	ctx, cancel := context.WithTimeout(ctx, timeout+2*time.Second)
	defer cancel()
	sc, err := c.acquire(ctx)
	if err != nil {
		return nil, err
	}
	defer sc.Close()
	if _, err := sc.ExecContext(ctx, c.setupSQL(limit+1, timeout, false)); err != nil {
		discard(sc)
		return nil, classify(ctx, err)
	}
	if _, err := sc.ExecContext(ctx, "START TRANSACTION"); err != nil {
		c.finish(sc, false, true)
		return nil, classify(ctx, err)
	}
	// Statements on a read-write connection can leave session state that the
	// reset cannot clear (CREATE TEMPORARY TABLE, HANDLER ... OPEN, CALL
	// changing sql_mode, LOCK INSTANCE FOR BACKUP), even when they fail
	// part-way. Never pool the connection after one.
	dirty := access != sqlx.Read
	fail := func(err error) (*engine.Result, error) {
		c.finish(sc, false, true)
		if dirty {
			discard(sc)
		}
		return nil, classify(ctx, err)
	}
	qctx, qcancel := context.WithCancel(ctx)
	defer qcancel()
	res, killed, err := collect(qctx, qcancel, sc, q, limit, access == sqlx.Read)
	if killed {
		// The driver closed the connection when the query was cancelled; the
		// server rolls back the transaction.
		discard(sc)
		res.Duration = time.Since(start)
		return res, nil
	}
	if err != nil {
		return fail(err)
	}
	if len(res.Columns) == 0 {
		if err := sc.QueryRowContext(ctx, "SELECT ROW_COUNT()").Scan(&res.RowsAffected); err != nil {
			return fail(err)
		}
	}
	if err := c.finish(sc, commit, true); err != nil {
		if dirty {
			discard(sc)
		}
		return nil, classify(ctx, err)
	}
	if dirty {
		discard(sc)
	}
	res.Duration = time.Since(start)
	return res, nil
}

// collect runs q and reads at most limit rows. sql_select_limit already makes
// the server stop at limit+1 for plain SELECTs; an explicit LIMIT overrides
// it, so for reads collect drains up to drainBudget surplus rows and, if more
// remain, cancels the query (the driver then closes the connection) and
// reports killed. Writes (e.g. MariaDB's DELETE ... RETURNING) always drain,
// because cancelling them would roll the write back.
func collect(ctx context.Context, cancel context.CancelFunc, sc *sql.Conn, q string, limit int, read bool) (*engine.Result, bool, error) {
	return collectWithStop(ctx, cancel, sc, q, limit, read, false)
}

func collectWithStop(ctx context.Context, onCap func(), sc *sql.Conn, q string, limit int, read, drainAfterStop bool) (result *engine.Result, killed bool, err error) {
	rows, err := sc.QueryContext(ctx, q)
	if err != nil {
		return nil, false, err
	}
	defer func() {
		closeErr := rows.Close()
		if drainAfterStop && err == nil {
			err = closeErr
		}
	}()
	terminal := func() error {
		if drainAfterStop {
			for rows.NextResultSet() {
				for rows.Next() {
				}
			}
		}
		return rows.Err()
	}
	cts, err := rows.ColumnTypes()
	if err != nil {
		return nil, false, err
	}
	res := &engine.Result{RowsAffected: -1}
	kinds := make([]colKind, len(cts))
	for i, ct := range cts {
		res.Columns = append(res.Columns, engine.Column{Name: ct.Name(), Type: strings.ToLower(ct.DatabaseTypeName())})
		kinds[i] = kindOf(ct.DatabaseTypeName())
	}
	raw := make([]sql.RawBytes, len(cts))
	ptrs := make([]any, len(cts))
	for i := range raw {
		ptrs[i] = &raw[i]
	}
	for rows.Next() {
		if len(res.Rows) == limit {
			res.Truncated = true
			if !read {
				for rows.Next() {
				}
				return res, false, terminal()
			}
			for n := 0; n < drainBudget; n++ {
				if !rows.Next() {
					return res, false, terminal()
				}
			}
			onCap()
			if drainAfterStop {
				for rows.Next() {
				}
				return res, true, terminal()
			}
			return res, true, nil
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, false, err
		}
		row := make([]engine.Value, len(raw))
		for i, b := range raw {
			row[i] = toValue(kinds[i], b)
		}
		res.Rows = append(res.Rows, row)
	}
	return res, false, terminal()
}

// execProtected preserves the query session while the guard attempts server-side
// interruption. Every return path releases registration and both reservations.
func (c *conn) execProtected(ctx context.Context, q string, o engine.QueryOpts, access sqlx.Access) (*engine.Result, error) {
	start := time.Now()
	timeout, limit := callLimits(o)
	ctx, cancel := context.WithTimeout(ctx, timeout+2*time.Second)
	defer cancel()
	call, err := c.writes.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer call.Cleanup(false)
	sc, err := c.acquire(call.Context())
	if err != nil {
		return nil, err
	}
	call.discardAfter = access != sqlx.Read
	if err = call.Prepare(sc, c.setupSQL(limit+1, timeout, false)); err != nil {
		return nil, err
	}
	qctx, err := call.Admit()
	if err != nil {
		return nil, err
	}
	res, _, statementErr := collectWithStop(qctx, call.RequestTruncation, sc, q, limit, access == sqlx.Read, true)
	stop := call.Complete(statementErr)
	if stop.Reason != stopNone || statementErr != nil || ctx.Err() != nil {
		cleanup := call.Cleanup(false)
		if err := interruptionError(ctx, stop, cleanup, statementErr); err != nil {
			return nil, err
		}
		res.Duration = time.Since(start)
		return res, nil
	}
	if len(res.Columns) == 0 {
		if err = sc.QueryRowContext(call.Context(), "SELECT ROW_COUNT()").Scan(&res.RowsAffected); err != nil {
			return nil, interruptionError(ctx, stop, call.Cleanup(false), err)
		}
	}
	if !call.CommitAllowed() {
		if errors.Is(call.Context().Err(), context.DeadlineExceeded) {
			stop.Reason = stopDeadline
		} else {
			stop.Reason = stopCallerCancel
		}
		return nil, interruptionError(ctx, stop, call.Cleanup(false), context.Canceled)
	}
	if err := interruptionError(ctx, stop, call.Cleanup(true), nil); err != nil {
		return nil, err
	}
	res.Duration = time.Since(start)
	return res, nil
}
