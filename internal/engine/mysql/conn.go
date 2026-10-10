package mysql

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"log"
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
	return &conn{db: db, readOnly: spec.Mode != engine.ModeRW, dbName: cfg.DBName, flavor: f}, nil
}

type conn struct {
	db       *sql.DB
	readOnly bool
	dbName   string
	flavor   flavor
}

func (c *conn) Close() error { return c.db.Close() }

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

func (c *conn) Explain(ctx context.Context, q string, o engine.QueryOpts) (*engine.Result, error) {
	if _, err := sqlx.Classify(q, sqlx.MySQL); err != nil {
		return nil, err
	}
	stmt := "EXPLAIN FORMAT=TREE " + q
	if c.flavor == flavorMariaDB {
		stmt = "EXPLAIN " + q
	}
	r, err := c.run(ctx, stmt, engine.QueryOpts{MaxRows: 500, Timeout: o.Timeout}, sqlx.Read, true)
	if err != nil {
		return nil, err
	}
	return planResult(r), nil
}

func (c *conn) setupSQL(rows int, timeout time.Duration) string {
	if c.flavor == flavorMariaDB {
		return fmt.Sprintf("SET SESSION sql_select_limit = %d, max_statement_time = %.3f, time_zone = '+00:00'", rows, timeout.Seconds())
	}
	return fmt.Sprintf("SET SESSION sql_select_limit = %d, max_execution_time = %d, time_zone = '+00:00'", rows, timeout.Milliseconds())
}

func (c *conn) resetSQL() string {
	if c.flavor == flavorMariaDB {
		return "SET @qh_released = RELEASE_ALL_LOCKS(), SESSION sql_select_limit = DEFAULT, max_statement_time = DEFAULT"
	}
	return "SET @qh_released = RELEASE_ALL_LOCKS(), SESSION sql_select_limit = DEFAULT, max_execution_time = DEFAULT"
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
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if rw {
		if _, err := sc.ExecContext(ctx, "UNLOCK TABLES"); err != nil {
			discard(sc)
			return err
		}
	}
	end := "ROLLBACK"
	if commit {
		end = "COMMIT"
	}
	if _, err := sc.ExecContext(ctx, end); err != nil {
		discard(sc)
		return err
	}
	if _, err := sc.ExecContext(ctx, c.resetSQL()); err != nil {
		discard(sc)
		if !commit {
			return err
		}
	}
	return nil
}

func (c *conn) run(ctx context.Context, q string, o engine.QueryOpts, access sqlx.Access, readOnly bool) (*engine.Result, error) {
	start := time.Now()
	timeout := o.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	limit := o.MaxRows
	if limit <= 0 {
		limit = 100
	}
	// The server-side timeout fires first; this is a backstop.
	ctx, cancel := context.WithTimeout(ctx, timeout+2*time.Second)
	defer cancel()
	sc, err := c.acquire(ctx)
	if err != nil {
		return nil, err
	}
	defer sc.Close()
	if _, err := sc.ExecContext(ctx, c.setupSQL(limit+1, timeout)); err != nil {
		discard(sc)
		return nil, classify(ctx, err)
	}
	begin := "START TRANSACTION"
	if readOnly {
		begin = "START TRANSACTION READ ONLY"
	}
	if _, err := sc.ExecContext(ctx, begin); err != nil {
		c.finish(sc, false, !readOnly)
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
		c.finish(sc, false, !readOnly)
		return nil, classify(ctx, err)
	}
	if len(res.Columns) == 0 {
		if err := sc.QueryRowContext(ctx, "SELECT ROW_COUNT()").Scan(&res.RowsAffected); err != nil {
			c.finish(sc, false, !readOnly)
			return nil, classify(ctx, err)
		}
	}
	if err := c.finish(sc, true, !readOnly); err != nil {
		return nil, classify(ctx, err)
	}
	if !readOnly && access != sqlx.Read {
		// Statements on a read-write connection can leave session state that
		// the reset cannot clear (CREATE TEMPORARY TABLE, HANDLER ... OPEN,
		// CALL changing sql_mode, LOCK INSTANCE FOR BACKUP). Never pool the
		// connection after one.
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
	rows, err := sc.QueryContext(ctx, q)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
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
				return res, false, rows.Err()
			}
			for n := 0; n < drainBudget; n++ {
				if !rows.Next() {
					return res, false, rows.Err()
				}
			}
			cancel()
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
	return res, false, rows.Err()
}
