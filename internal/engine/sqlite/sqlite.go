// Package sqlite is the SQLite engine.
package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	msqlite "modernc.org/sqlite"
	sqlitelib "modernc.org/sqlite/lib"

	"github.com/anderson-0/qh/internal/engine"
	"github.com/anderson-0/qh/internal/qerr"
	"github.com/anderson-0/qh/internal/sqlx"
)

func init() {
	msqlite.StrictPragmas(true)
	engine.Register(Engine{})
}

// slotWait is how long a query waits for a free connection.
var slotWait = 10 * time.Second

// Engine is the SQLite engine.
type Engine struct{}

func (Engine) Name() string          { return "sqlite" }
func (Engine) Dialect() sqlx.Dialect { return sqlx.SQLite }

// Path returns the absolute database path in dsn: a file path, optionally
// prefixed with sqlite:// or file:. Query strings and fragments are rejected
// so driver parameters cannot be injected.
func Path(dsn string) (string, error) {
	p := strings.TrimSpace(dsn)
	for _, prefix := range []string{"sqlite:", "file:"} {
		if strings.HasPrefix(p, prefix) {
			p = strings.TrimPrefix(p, prefix)
			if strings.HasPrefix(p, "//") {
				p = p[2:]
			}
			break
		}
	}
	if p == "" {
		return "", qerr.New(qerr.Invalid, "sqlite connection string must be a file path")
	}
	if strings.ContainsAny(p, "?#") {
		return "", qerr.New(qerr.Invalid, "sqlite path must not contain ? or #")
	}
	return filepath.Abs(p)
}

func (Engine) Inspect(dsn string) (engine.DSNInfo, error) {
	p, err := Path(dsn)
	if err != nil {
		return engine.DSNInfo{}, err
	}
	return engine.DSNInfo{Database: p, Redacted: p}, nil
}

func (Engine) Open(ctx context.Context, spec engine.ConnSpec) (engine.Conn, error) {
	path, err := Path(spec.DSN)
	if err != nil {
		return nil, err
	}
	if spec.FileRoot != "" {
		if err := within(spec.FileRoot, path); err != nil {
			return nil, err
		}
	}
	if _, err := os.Stat(path); err != nil {
		return nil, qerr.Wrap(qerr.ConnectionFailed, "database file not found: "+path, err)
	}
	if spec.FileRoot != "" {
		real, err := filepath.EvalSymlinks(path)
		if err != nil {
			return nil, qerr.Wrap(qerr.ConnectionFailed, "database file not found: "+path, err)
		}
		root, err := filepath.EvalSymlinks(spec.FileRoot)
		if err != nil {
			return nil, qerr.Wrap(qerr.Invalid, "the allowed sqlite directory does not exist", err)
		}
		if err := within(root, real); err != nil {
			return nil, err
		}
	}
	readOnly := spec.Mode != engine.ModeRW
	q := "mode=rw&_busy_timeout=5000"
	if readOnly {
		q = "mode=ro&_busy_timeout=5000&_query_only=1"
	}
	u := url.URL{Scheme: "file", Path: path, RawQuery: q}
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return nil, qerr.Wrap(qerr.ConnectionFailed, "cannot open database", err)
	}
	size := max(spec.PoolSize, 1)
	db.SetMaxOpenConns(size)
	db.SetMaxIdleConns(size)
	db.SetConnMaxIdleTime(10 * time.Minute)
	c := &conn{db: db, readOnly: readOnly}
	if _, err := c.Ping(ctx); err != nil {
		db.Close()
		return nil, err
	}
	return c, nil
}

type conn struct {
	db       *sql.DB
	readOnly bool
}

func (c *conn) Close() error { return c.db.Close() }

func (c *conn) Ping(ctx context.Context) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	var v string
	if err := c.db.QueryRowContext(ctx, "SELECT sqlite_version()").Scan(&v); err != nil {
		return "", qerr.Wrap(qerr.ConnectionFailed, "cannot read database: "+cleanMessage(err.Error()), err)
	}
	return "SQLite " + v, nil
}

func readOnlyErr() *qerr.Error {
	return qerr.New(qerr.ReadOnly, "connection is read-only; only statements that read data are allowed").
		WithHint("Use a read-write connection to change data")
}

// Query runs one statement. SQLite has file side effects a read-only open does
// not cover (ATTACH, VACUUM INTO), so the engine classifies statements too.
func (c *conn) Query(ctx context.Context, q string, o engine.QueryOpts) (*engine.Result, error) {
	access, err := sqlx.Classify(q, sqlx.SQLite)
	if err != nil {
		return nil, err
	}
	if (c.readOnly || o.ReadOnly) && access != sqlx.Read {
		return nil, readOnlyErr()
	}
	return c.run(ctx, q, o)
}

func (c *conn) Explain(ctx context.Context, q string, o engine.QueryOpts) (*engine.Result, error) {
	// Classify rejects pragma assignments, which SQLite applies while
	// preparing the statement, even under EXPLAIN.
	if _, err := sqlx.Classify(q, sqlx.SQLite); err != nil {
		return nil, err
	}
	r, err := c.run(ctx, "EXPLAIN QUERY PLAN "+q, engine.QueryOpts{MaxRows: 500, Timeout: o.Timeout})
	if err != nil {
		return nil, err
	}
	out := &engine.Result{Columns: []engine.Column{{Name: "plan", Type: "text"}}, RowsAffected: -1, Duration: r.Duration}
	depth := map[string]int{"0": -1}
	for _, row := range r.Rows { // columns: id, parent, notused, detail
		d := depth[row[1].Text] + 1
		depth[row[0].Text] = d
		out.Rows = append(out.Rows, []engine.Value{{Text: strings.Repeat("  ", d) + row[3].Text}})
	}
	return out, nil
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
	// No ATTACH: an attached file would sidestep the read-only open.
	if _, err := msqlite.Limit(sc, sqlitelib.SQLITE_LIMIT_ATTACHED, 0); err != nil {
		sc.Close()
		return nil, err
	}
	return sc, nil
}

func (c *conn) run(ctx context.Context, q string, o engine.QueryOpts) (*engine.Result, error) {
	start := time.Now()
	if o.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, o.Timeout)
		defer cancel()
	}
	sc, err := c.acquire(ctx)
	if err != nil {
		return nil, err
	}
	defer sc.Close()
	var before int64
	if err := sc.QueryRowContext(ctx, "SELECT total_changes()").Scan(&before); err != nil {
		return nil, classify(ctx, err)
	}
	rows, err := sc.QueryContext(ctx, q)
	if err != nil {
		return nil, classify(ctx, err)
	}
	defer rows.Close()
	cts, err := rows.ColumnTypes()
	if err != nil {
		return nil, classify(ctx, err)
	}
	res := &engine.Result{RowsAffected: -1}
	for _, ct := range cts {
		res.Columns = append(res.Columns, engine.Column{Name: ct.Name(), Type: strings.ToLower(ct.DatabaseTypeName())})
	}
	limit := o.MaxRows
	if limit <= 0 {
		limit = 100
	}
	vals := make([]any, len(cts))
	ptrs := make([]any, len(cts))
	for i := range vals {
		ptrs[i] = &vals[i]
	}
	for rows.Next() {
		if len(res.Rows) == limit {
			res.Truncated = true
			break
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, classify(ctx, err)
		}
		row := make([]engine.Value, len(vals))
		for i, v := range vals {
			row[i] = toValue(v)
		}
		res.Rows = append(res.Rows, row)
	}
	if err := rows.Err(); err != nil {
		return nil, classify(ctx, err)
	}
	rows.Close()
	if len(cts) == 0 {
		// changes() keeps the last INSERT/UPDATE/DELETE count through other
		// statements, so only trust it when this statement changed rows.
		var after int64
		if err := sc.QueryRowContext(ctx, "SELECT total_changes()").Scan(&after); err != nil {
			return nil, classify(ctx, err)
		}
		res.RowsAffected = 0
		if after != before {
			if err := sc.QueryRowContext(ctx, "SELECT changes()").Scan(&res.RowsAffected); err != nil {
				return nil, classify(ctx, err)
			}
		}
	}
	res.Duration = time.Since(start)
	return res, nil
}

func toValue(v any) engine.Value {
	switch x := v.(type) {
	case nil:
		return engine.Value{Null: true}
	case int64:
		return engine.Value{Text: strconv.FormatInt(x, 10)}
	case float64:
		return engine.Value{Text: strconv.FormatFloat(x, 'g', -1, 64)}
	case bool:
		return engine.Value{Text: strconv.FormatBool(x)}
	case string:
		return engine.Value{Text: x}
	case []byte:
		return engine.Value{Binary: true, Size: len(x)}
	case time.Time:
		return engine.Value{Text: x.Format(time.RFC3339Nano)}
	}
	return engine.Value{Text: fmt.Sprint(v)}
}

var (
	noColumn  = regexp.MustCompile(`no such column: ([^\s()]+)`)
	noTable   = regexp.MustCompile(`no such table: ([^\s()]+)`)
	msgPrefix = regexp.MustCompile(`^SQL logic error: `)
	msgCode   = regexp.MustCompile(` \(\d+\)$`)
)

func cleanMessage(m string) string {
	return msgCode.ReplaceAllString(msgPrefix.ReplaceAllString(m, ""), "")
}

func classify(ctx context.Context, err error) error {
	var qe *qerr.Error
	if errors.As(err, &qe) {
		return qe
	}
	timeout := qerr.Wrap(qerr.Timeout, "query exceeded the connection timeout", err).
		WithHint("Add WHERE/LIMIT or narrow the range")
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return timeout
	}
	var se *msqlite.Error
	if errors.As(err, &se) {
		switch se.Code() & 0xff {
		case sqlitelib.SQLITE_READONLY:
			return readOnlyErr()
		case sqlitelib.SQLITE_INTERRUPT:
			return timeout
		}
	}
	msg := cleanMessage(err.Error())
	e := qerr.Wrap(qerr.QueryError, msg, err)
	if m := noColumn.FindStringSubmatch(msg); m != nil {
		e.UnknownColumn = m[1]
	}
	if m := noTable.FindStringSubmatch(msg); m != nil {
		e.UnknownTable = strings.TrimPrefix(m[1], "main.")
	}
	return e
}

// within rejects path unless it lies inside root.
func within(root, path string) error {
	rel, err := filepath.Rel(filepath.Clean(root), filepath.Clean(path))
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return qerr.New(qerr.Invalid, "sqlite path is outside the allowed directory").
			WithHint("This server only opens SQLite files under its --sqlite-root")
	}
	return nil
}
