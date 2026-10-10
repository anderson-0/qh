# qh Plan 2: MySQL/MariaDB engine — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A `mysql` engine (alias `mariadb`) that passes the shared engine conformance suite on MySQL 8.0, MySQL 8.4, MariaDB 10.11 and MariaDB 11.4, and is available from `qh conn add --engine mysql`.

**Architecture:** One package, `internal/engine/mysql`, implementing `engine.Engine`/`engine.Conn` on `database/sql` with `github.com/go-sql-driver/mysql`. Every statement runs on a dedicated pooled connection inside `START TRANSACTION [READ ONLY]`. A session `SET` applies the row cap (`sql_select_limit`), the timeout and UTC. Afterwards a reset releases locks and restores the defaults, so nothing leaks into the next call. The flavour (MySQL or MariaDB) is detected at connect from `VERSION()`.

**Tech Stack:** Go 1.27, `github.com/go-sql-driver/mysql` v1.10.1 (pure Go), `github.com/testcontainers/testcontainers-go/modules/mysql` v0.44.0, `github.com/testcontainers/testcontainers-go/modules/mariadb` v0.44.0. Everything else is already in `go.mod`.

**Spec:** `docs/superpowers/specs/2026-10-09-qh-design.md` (§4.2 MySQL row capping and timeouts, §6.3 read-only enforcement, §9.1 conformance suite). Plan 1 (`docs/superpowers/plans/2026-10-09-qh-plan-1-local-mvp.md`) built the interfaces this plan implements; read its Global Constraints and Spec adjustments.

## Verified facts (probed against mysql:8.4.11 and mariadb:11.4.13 on 2026-10-10)

The plan's code relies on these. Do not "fix" code that follows them.
- **Trailing terminators:** `SELECT 1;`, `SELECT 1; -- c` and `SELECT 1 -- c` all succeed on both flavours without `multiStatements`.
- **Row cap:** `SET SESSION sql_select_limit = N` caps a top-level `SELECT`. An explicit `LIMIT` in the query overrides it.
- **Read-only transactions:** inside `START TRANSACTION READ ONLY`, both `INSERT` and `CREATE TEMPORARY TABLE` fail with error 1792.
- **Timeouts:**
  - MySQL `max_execution_time` interrupts a long `SELECT` with error 3024, but `SELECT SLEEP(2)` just returns `1` with no error.
  - MariaDB `max_statement_time` interrupts both, with error 1969.
- **EXPLAIN:** MySQL's `EXPLAIN FORMAT=TREE` returns one column named `EXPLAIN`. MariaDB rejects `FORMAT=TREE` (error 1791) and returns a tabular `EXPLAIN`.
- **Unknown names:**
  - Error 1054 reads `Unknown column 'x' in 'field list'` (MySQL) or `... in 'SELECT'` (MariaDB).
  - Error 1146 reads `Table 'qh.x' doesn't exist`.
- **`DatabaseTypeName()` values:** `BIGINT`, `INT`, `VARCHAR`, `TEXT`, `BLOB` (binary), `JSON` (MySQL; MariaDB reports `TEXT`), `ENUM`, `DATETIME`, `TIMESTAMP`, `BIT`, `UNSIGNED BIGINT`.
- **`information_schema.COLUMNS`:**
  - MySQL: `COLUMN_TYPE` is `bigint` with no width; `COLUMN_DEFAULT` is SQL NULL when there is no default, and string defaults are unquoted (`a`).
  - MariaDB: `bigint(20)`; `COLUMN_DEFAULT` is the string `NULL` when the default is NULL, and string defaults are quoted (`'a'`).
  - Both report `auto_increment` in `EXTRA`.
- **Lock release:** `SET @qh = RELEASE_ALL_LOCKS(), SESSION sql_select_limit = DEFAULT` works on both.
- **Driver reset:** `go-sql-driver/mysql` has no way to send `COM_RESET_CONNECTION`; its `ResetSession` only checks liveness. Session state must be reset explicitly.

## Spec adjustments made while planning

1. **The engine classifies statements itself** and rejects non-read statements on read-only connections, as the SQLite engine does. Read-only transactions do not stop `SELECT … INTO OUTFILE`/`DUMPFILE` (server file writes) or `LOAD DATA`.
2. **The session is reset after every statement:**
   - `RELEASE_ALL_LOCKS()` releases named locks taken with `GET_LOCK`.
   - `sql_select_limit` and the timeout variable go back to `DEFAULT`.
   - On read-write connections, `UNLOCK TABLES` also runs.
   - If any reset step fails, the connection is discarded.
   - User variables (`@x`) are not reset. They cannot affect safety, and enumerating them needs `performance_schema` privileges.
3. **Early stop:** when a query with an explicit `LIMIT` returns more than the cap, the engine first drains up to 1,000 surplus rows. If more remain, it cancels the query, which makes the driver close the connection, and discards that connection. The spec says "discarded rather than drained"; the bounded drain avoids a reconnect in the common case, where `sql_select_limit` has already stopped the server at cap + 1.
4. **MariaDB `EXPLAIN` is rendered as one `plan` column**, one `key=value …` line per plan row. The spec said tabular TSV; one shape for every engine keeps the conformance suite and agent output uniform.
5. **Timeouts for non-`SELECT` statements on MySQL.** `max_execution_time` covers only `SELECT`, so other statements rely on the client deadline. That deadline closes the connection, and the server rolls back the open transaction, though it may finish executing the statement first. MariaDB's `max_statement_time` covers every statement.
6. **Introspection scope:** when the connection string names a database, introspection covers only that database. Otherwise it covers every database except `mysql`, `information_schema`, `performance_schema` and `sys`.
7. **`mariadb` is registered as an alias engine name** with identical behaviour.
8. **Minimum versions:** MySQL 8.0.16+ (for `EXPLAIN FORMAT=TREE`) and MariaDB 10.6+ (for `RELEASE_ALL_LOCKS`).

## Global Constraints

Plan 1's Global Constraints all still apply. In particular:
- pure Go (`CGO_ENABLED=0`);
- query text is never rewritten;
- caps fetch cap + 1 rows;
- every agent-facing error is one line, `error[<category>]: <message>. <hint>`;
- connection strings and passwords are never printed or logged;
- Docker tests call `testcontainers.SkipIfProviderIsNotHealthy(t)` and skip under `-short`.

Additionally:
- **Driver:** `github.com/go-sql-driver/mysql` v1.10.1, imported as `gomysql`, because the package itself is named `mysql`.
- **Images tested:** `mysql:8.0`, `mysql:8.4`, `mariadb:10.11`, `mariadb:11.4`. Golden file prefixes: `mysql` for both MySQL images, `mariadb` for both MariaDB images.
- **Driver options forced on every connection,** whatever the user's connection string says:
  - `MultiStatements = false`
  - `AllowAllFiles = false`
  - `ParseTime = false`
  - `InterpolateParams = false`
  - `Loc = time.UTC`
  - `Timeout` defaults to 10 s when unset.
- **Accepted connection-string forms:** `mysql://user:pass@host[:port]/db?params`, `mariadb://…` (same), and the go-sql-driver form `user:pass@tcp(host:port)/db?params` (also `unix(/path)`). The default port is 3306.

## Review Focus

1. **An explicit `LIMIT` far above the cap** (`… LIMIT 60000`, cap 5) must return 5 rows marked truncated within seconds, and the next call on the same pool must work. → Task 2 `TestExplicitLimitAboveCap`.
2. **Session state carried between pooled connections** (a `GET_LOCK` lock; a `sql_select_limit` left behind that would truncate introspection) must not survive a call. → Task 2 `TestSessionStateDoesNotLeak`.
3. **Emoji and other utf8mb4 data on MariaDB,** whose server default charset can be latin1 in older versions, must round-trip. → Task 2 fixture sets `utf8mb4` and the suite's `select` case includes `Zoë 🚀`.
4. **Connection strings as people paste them:** URL with and without a port, IPv6, the go-sql-driver form, a unix socket, `mariadb://`. → Task 1 `TestInspect`.
5. **Dangerous options in a pasted connection string** (`multiStatements=true`, `allowAllFiles=true`) must be ignored. → Task 1 `TestParseConfigEnforcesSafety`.

---

## File map

```
internal/engine/mysql/mysql.go        Engine, registration, connection-string parsing, Inspect, redaction
internal/engine/mysql/values.go       column kinds, value rendering, EXPLAIN shaping, ENUM parsing, version parsing
internal/engine/mysql/errors.go       error classification (query time and connect time)
internal/engine/mysql/conn.go         Open, Ping, Query, Explain, run, collect, finish, acquire
internal/engine/mysql/introspect.go   information_schema catalog queries
internal/engine/mysql/unit_test.go    tests that need no Docker
internal/engine/mysql/mysql_test.go   Docker tests: conformance suite, leaks, limits, busy, auth
internal/engine/mysql/layers_test.go  database-level read-only test bypassing classification
internal/engine/mysql/testdata/{mysql,mariadb}_{overview,orders,users}.golden
internal/cli/engines.go               register the engine in the binary
internal/cli/conn.go                  --engine help text
README.md                             supported databases
```

---

### Task 1: Connection strings, values, errors (no Docker)

**Files:**
- Create: `internal/engine/mysql/mysql.go`, `internal/engine/mysql/values.go`, `internal/engine/mysql/errors.go`
- Test: `internal/engine/mysql/unit_test.go`

**Interfaces:**
- Consumes: `engine.Register`, `engine.DSNInfo`, `engine.Value`, `engine.Result`, `engine.Column`, `qerr`, `sqlx.MySQL`.
- Produces (package `mysql`, used by Task 2):
  - `type Engine struct{ name string }` with `Name()` (`"mysql"` for the zero value), `Dialect()` and `Inspect(dsn)`. `Open` and the `init` that registers the engines come in Task 2.
  - `parseRaw(dsn string) (*gomysql.Config, error)`, `parseConfig(dsn string) (*gomysql.Config, error)`, `invalidDSN() error`.
  - `type colKind int` with `kindText, kindBinary, kindDatetime, kindTimestamp, kindJSON, kindBit`; `kindOf(dbType string) colKind`; `toValue(k colKind, b []byte) engine.Value`.
  - `planResult(r *engine.Result) *engine.Result`; `parseEnum(colType string) []string`.
  - `type flavor int` with `flavorMySQL, flavorMariaDB`; `parseVersion(v string) (flavor, string)`.
  - `classify(ctx context.Context, err error) error`; `connectErr(err error) error`; `readOnlyErr() *qerr.Error`.

Note: `Engine` does not satisfy `engine.Engine` until Task 2 adds `Open`, so this task's `mysql.go` has no `init`; registration lives in Task 2's `conn.go`.

- [ ] **Step 1: Add the driver**

Run: `go get github.com/go-sql-driver/mysql@v1.10.1`

- [ ] **Step 2: Write the failing tests**

`internal/engine/mysql/unit_test.go`:
```go
package mysql

import (
	"context"
	"errors"
	"net"
	"reflect"
	"strings"
	"testing"
	"time"

	gomysql "github.com/go-sql-driver/mysql"

	"github.com/anderson-0/qh/internal/engine"
	"github.com/anderson-0/qh/internal/qerr"
)

func TestInspect(t *testing.T) {
	cases := []struct {
		dsn                  string
		host, db, user, pass string
		port                 int
		redacted             string
	}{
		{"mysql://bob:hunter22@db.example.com:3307/shop?tls=true", "db.example.com", "shop", "bob", "hunter22", 3307,
			"mysql://bob:***@db.example.com:3307/shop?tls=true"},
		{"mariadb://bob@db/shop", "db", "shop", "bob", "", 3306, "mariadb://bob@db/shop"},
		{"mysql://bob:pw@[::1]/shop", "::1", "shop", "bob", "pw", 3306, "mysql://bob:***@[::1]/shop"},
	}
	for _, c := range cases {
		info, err := Engine{}.Inspect(c.dsn)
		if err != nil {
			t.Fatalf("%s: %v", c.dsn, err)
		}
		if info.Host != c.host || info.Port != c.port || info.Database != c.db || info.User != c.user || info.Password != c.pass || info.Redacted != c.redacted {
			t.Errorf("%s: %+v", c.dsn, info)
		}
	}

	info, err := Engine{}.Inspect("bob:hunter22@tcp(db:3307)/shop?tls=true")
	if err != nil {
		t.Fatal(err)
	}
	if info.Host != "db" || info.Port != 3307 || info.Database != "shop" || info.Password != "hunter22" {
		t.Fatalf("native form: %+v", info)
	}
	if !strings.Contains(info.Redacted, "bob:***@tcp(db:3307)/shop") || strings.Contains(info.Redacted, "hunter22") || !strings.Contains(info.Redacted, "tls=true") {
		t.Fatalf("native redaction: %q", info.Redacted)
	}

	info, err = Engine{}.Inspect("bob:pw@unix(/var/run/mysqld/mysqld.sock)/shop")
	if err != nil || info.Host != "/var/run/mysqld/mysqld.sock" || info.Port != 0 {
		t.Fatalf("unix socket: %+v %v", info, err)
	}

	_, err = Engine{}.Inspect("mysql://bob:hunter22@db/shop?timeout=notaduration")
	if err == nil || !strings.HasPrefix(err.Error(), "error[invalid]: invalid mysql connection string") || strings.Contains(err.Error(), "hunter22") {
		t.Fatalf("bad dsn: %v", err)
	}
}

func TestParseConfigEnforcesSafety(t *testing.T) {
	cfg, err := parseConfig("mysql://u:p@h/db?multiStatements=true&allowAllFiles=true&parseTime=true&interpolateParams=true")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.MultiStatements || cfg.AllowAllFiles || cfg.ParseTime || cfg.InterpolateParams || cfg.Loc != time.UTC || cfg.Timeout != 10*time.Second {
		t.Fatalf("unsafe options kept: %+v", cfg)
	}
	cfg, _ = parseConfig("mysql://u:p@h/db?timeout=3s")
	if cfg.Timeout != 3*time.Second {
		t.Fatalf("user timeout not kept: %v", cfg.Timeout)
	}
}

func TestEngineNames(t *testing.T) {
	if (Engine{}).Name() != "mysql" || (Engine{name: "mariadb"}).Name() != "mariadb" {
		t.Fatal("names")
	}
}

func TestToValue(t *testing.T) {
	cases := []struct {
		kind colKind
		in   []byte
		want engine.Value
	}{
		{kindText, nil, engine.Value{Null: true}},
		{kindText, []byte("Zoë 🚀"), engine.Value{Text: "Zoë 🚀"}},
		{kindBinary, []byte{0xde, 0xad, 0xbe, 0xef}, engine.Value{Binary: true, Size: 4}},
		{kindDatetime, []byte("2025-03-14 10:22:31"), engine.Value{Text: "2025-03-14T10:22:31"}},
		{kindTimestamp, []byte("2025-03-14 10:22:31.5"), engine.Value{Text: "2025-03-14T10:22:31.5Z"}},
		{kindJSON, []byte(`{"a": 1, "b": [1, 2]}`), engine.Value{Text: `{"a":1,"b":[1,2]}`}},
		{kindJSON, []byte(`not json`), engine.Value{Text: "not json"}},
		{kindBit, []byte{0x01}, engine.Value{Text: "1"}},
		{kindBit, []byte{0x01, 0x00}, engine.Value{Text: "256"}},
	}
	for _, c := range cases {
		if got := toValue(c.kind, c.in); got != c.want {
			t.Errorf("toValue(%v, %q) = %+v, want %+v", c.kind, c.in, got, c.want)
		}
	}
	kinds := map[string]colKind{"BLOB": kindBinary, "VARBINARY": kindBinary, "LONGBLOB": kindBinary, "TEXT": kindText,
		"DATETIME": kindDatetime, "TIMESTAMP": kindTimestamp, "JSON": kindJSON, "BIT": kindBit, "UNSIGNED BIGINT": kindText}
	for in, want := range kinds {
		if got := kindOf(in); got != want {
			t.Errorf("kindOf(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestPlanResult(t *testing.T) {
	tree := &engine.Result{Columns: []engine.Column{{Name: "EXPLAIN"}}, RowsAffected: -1,
		Rows: [][]engine.Value{{{Text: "-> Rows fetched before execution  (cost=0..0 rows=1)\n    -> Index lookup\n"}}}}
	got := planResult(tree)
	if len(got.Columns) != 1 || got.Columns[0].Name != "plan" || len(got.Rows) != 2 || got.Rows[1][0].Text != "    -> Index lookup" {
		t.Fatalf("tree: %+v", got)
	}
	tabular := &engine.Result{Columns: []engine.Column{{Name: "id"}, {Name: "table"}, {Name: "key"}, {Name: "rows"}}, RowsAffected: -1,
		Rows: [][]engine.Value{{{Text: "1"}, {Text: "orders"}, {Null: true}, {Text: "250"}}}}
	got = planResult(tabular)
	if len(got.Rows) != 1 || got.Rows[0][0].Text != "id=1 table=orders rows=250" {
		t.Fatalf("tabular: %+v", got)
	}
}

func TestParseEnum(t *testing.T) {
	cases := map[string][]string{
		"enum('pending','paid','shipped')": {"pending", "paid", "shipped"},
		"enum('it''s','a,b')":              {"it's", "a,b"},
		"set('x')":                         {"x"},
		"varchar(10)":                      nil,
		"text":                             nil,
	}
	for in, want := range cases {
		if got := parseEnum(in); !reflect.DeepEqual(got, want) {
			t.Errorf("parseEnum(%q) = %#v, want %#v", in, got, want)
		}
	}
}

func TestParseVersion(t *testing.T) {
	cases := []struct {
		in      string
		flavor  flavor
		display string
	}{
		{"8.4.11", flavorMySQL, "MySQL 8.4.11"},
		{"8.0.40-0ubuntu0.22.04.1", flavorMySQL, "MySQL 8.0.40"},
		{"11.4.13-MariaDB-ubu2404", flavorMariaDB, "MariaDB 11.4.13"},
		{"10.11.9-MariaDB", flavorMariaDB, "MariaDB 10.11.9"},
	}
	for _, c := range cases {
		f, d := parseVersion(c.in)
		if f != c.flavor || d != c.display {
			t.Errorf("parseVersion(%q) = %v %q", c.in, f, d)
		}
	}
}

func TestClassify(t *testing.T) {
	ctx := context.Background()
	cat := func(err error) qerr.Category { return qerr.From(err).Category }
	e := qerr.From(classify(ctx, &gomysql.MySQLError{Number: 1054, Message: "Unknown column 'u.emial' in 'field list'"}))
	if e.Category != qerr.QueryError || e.UnknownColumn != "u.emial" {
		t.Errorf("1054: %#v", e)
	}
	e = qerr.From(classify(ctx, &gomysql.MySQLError{Number: 1146, Message: "Table 'qh.userz' doesn't exist"}))
	if e.Category != qerr.QueryError || e.UnknownTable != "userz" {
		t.Errorf("1146: %#v", e)
	}
	for _, n := range []uint16{3024, 1969, 1317} {
		if c := cat(classify(ctx, &gomysql.MySQLError{Number: n, Message: "interrupted"})); c != qerr.Timeout {
			t.Errorf("%d: %v", n, c)
		}
	}
	if c := cat(classify(ctx, &gomysql.MySQLError{Number: 1792, Message: "Cannot execute statement in a READ ONLY transaction."})); c != qerr.ReadOnly {
		t.Errorf("1792: %v", c)
	}
	if got := classify(ctx, &gomysql.MySQLError{Number: 1064, Message: "You have an error in your SQL syntax"}).Error(); got != "error[query_error]: You have an error in your SQL syntax" {
		t.Errorf("1064: %q", got)
	}
	if c := cat(classify(ctx, context.DeadlineExceeded)); c != qerr.Timeout {
		t.Errorf("deadline: %v", c)
	}
	if got := connectErr(&gomysql.MySQLError{Number: 1045, Message: "Access denied for user 'u'@'h' (using password: YES)"}).Error(); !strings.HasPrefix(got, "error[connection_failed]: authentication failed: Access denied") {
		t.Errorf("1045: %q", got)
	}
	netErr := &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connection refused")}
	if got := connectErr(netErr).Error(); !strings.HasPrefix(got, "error[connection_failed]: cannot reach the database: dial tcp") || !strings.Contains(got, "connection refused") {
		t.Errorf("net: %q", got)
	}
	if c := cat(connectErr(context.DeadlineExceeded)); c != qerr.ConnectionFailed {
		t.Errorf("connect deadline: %v", c)
	}
}
```

- [ ] **Step 3: Run them to verify they fail**

Run: `go test -short ./internal/engine/mysql/`
Expected: FAIL — `undefined: Engine` (and the other names).

- [ ] **Step 4: Implement**

`internal/engine/mysql/mysql.go`:
```go
// Package mysql is the MySQL and MariaDB engine.
package mysql

import (
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"

	gomysql "github.com/go-sql-driver/mysql"

	"github.com/anderson-0/qh/internal/engine"
	"github.com/anderson-0/qh/internal/qerr"
	"github.com/anderson-0/qh/internal/sqlx"
)

// Engine is the MySQL/MariaDB engine. The zero value is named "mysql";
// Engine{name: "mariadb"} is an alias with identical behaviour, because the
// flavour is detected at connect time.
type Engine struct{ name string }

func (e Engine) Name() string {
	if e.name == "" {
		return "mysql"
	}
	return e.name
}

func (Engine) Dialect() sqlx.Dialect { return sqlx.MySQL }

func invalidDSN() error {
	return qerr.New(qerr.Invalid, "invalid mysql connection string").
		WithHint("Use mysql://user:password@host:3306/database or user:password@tcp(host:3306)/database")
}

func isURL(u *url.URL) bool { return u.Scheme == "mysql" || u.Scheme == "mariadb" }

// parseRaw parses a mysql:// or mariadb:// URL, or a go-sql-driver DSN,
// without changing any option. Errors never include the input.
func parseRaw(dsn string) (*gomysql.Config, error) {
	dsn = strings.TrimSpace(dsn)
	if u, err := url.Parse(dsn); err == nil && isURL(u) {
		params := "/"
		if u.RawQuery != "" {
			params = "/?" + u.RawQuery
		}
		cfg, err := gomysql.ParseDSN(params)
		if err != nil {
			return nil, invalidDSN()
		}
		if u.User != nil {
			cfg.User = u.User.Username()
			cfg.Passwd, _ = u.User.Password()
		}
		host := u.Hostname()
		if host == "" {
			host = "127.0.0.1"
		}
		port := u.Port()
		if port == "" {
			port = "3306"
		}
		cfg.Net = "tcp"
		cfg.Addr = net.JoinHostPort(host, port)
		cfg.DBName = strings.TrimPrefix(u.Path, "/")
		return cfg, nil
	}
	cfg, err := gomysql.ParseDSN(dsn)
	if err != nil {
		return nil, invalidDSN()
	}
	return cfg, nil
}

// parseConfig is parseRaw plus the options qh always enforces: one statement
// per query, no LOAD DATA LOCAL access to this machine's files, results as
// text, UTC.
func parseConfig(dsn string) (*gomysql.Config, error) {
	cfg, err := parseRaw(dsn)
	if err != nil {
		return nil, err
	}
	cfg.MultiStatements = false
	cfg.AllowAllFiles = false
	cfg.ParseTime = false
	cfg.InterpolateParams = false
	cfg.Loc = time.UTC
	if cfg.Timeout == 0 {
		cfg.Timeout = 10 * time.Second
	}
	return cfg, nil
}

func (Engine) Inspect(dsn string) (engine.DSNInfo, error) {
	cfg, err := parseRaw(dsn)
	if err != nil {
		return engine.DSNInfo{}, err
	}
	info := engine.DSNInfo{Database: cfg.DBName, User: cfg.User, Password: cfg.Passwd, Redacted: redact(dsn, cfg)}
	if cfg.Net == "unix" {
		info.Host = cfg.Addr
		return info, nil
	}
	host, port, err := net.SplitHostPort(cfg.Addr)
	if err != nil {
		host = cfg.Addr
	}
	info.Host = host
	info.Port, _ = strconv.Atoi(port)
	return info, nil
}

// placeholder stands in for the password while a DSN is re-encoded.
const placeholder = "QHREDACTEDPASSWORD"

func redact(dsn string, cfg *gomysql.Config) string {
	dsn = strings.TrimSpace(dsn)
	if u, err := url.Parse(dsn); err == nil && isURL(u) {
		if u.User != nil {
			if _, ok := u.User.Password(); ok {
				u.User = url.UserPassword(u.User.Username(), placeholder)
			}
		}
		return strings.ReplaceAll(u.String(), placeholder, "***")
	}
	if cfg.Passwd == "" {
		return dsn
	}
	c := cfg.Clone()
	c.Passwd = placeholder
	return strings.ReplaceAll(c.FormatDSN(), placeholder, "***")
}
```

`internal/engine/mysql/values.go`:
```go
package mysql

import (
	"bytes"
	"encoding/json"
	"strconv"
	"strings"

	"github.com/anderson-0/qh/internal/engine"
)

type colKind int

const (
	kindText colKind = iota
	kindBinary
	kindDatetime
	kindTimestamp
	kindJSON
	kindBit
)

// kindOf maps go-sql-driver's DatabaseTypeName to how values are rendered.
// The driver reports binary-charset blobs and strings as BLOB/BINARY/VARBINARY
// and text ones as TEXT/CHAR/VARCHAR.
func kindOf(dbType string) colKind {
	switch strings.ToUpper(dbType) {
	case "BLOB", "TINYBLOB", "MEDIUMBLOB", "LONGBLOB", "BINARY", "VARBINARY", "GEOMETRY":
		return kindBinary
	case "DATETIME":
		return kindDatetime
	case "TIMESTAMP":
		return kindTimestamp
	case "JSON":
		return kindJSON
	case "BIT":
		return kindBit
	}
	return kindText
}

// toValue converts a text-protocol value to its canonical form. TIMESTAMP is
// rendered in UTC with Z because every session runs with time_zone '+00:00'.
func toValue(k colKind, b []byte) engine.Value {
	if b == nil {
		return engine.Value{Null: true}
	}
	switch k {
	case kindBinary:
		return engine.Value{Binary: true, Size: len(b)}
	case kindDatetime:
		return engine.Value{Text: strings.Replace(string(b), " ", "T", 1)}
	case kindTimestamp:
		return engine.Value{Text: strings.Replace(string(b), " ", "T", 1) + "Z"}
	case kindJSON:
		var buf bytes.Buffer
		if json.Compact(&buf, b) == nil {
			return engine.Value{Text: buf.String()}
		}
	case kindBit:
		var n uint64
		for _, x := range b {
			n = n<<8 | uint64(x)
		}
		return engine.Value{Text: strconv.FormatUint(n, 10)}
	}
	return engine.Value{Text: string(b)}
}

// planResult shapes EXPLAIN output as one "plan" column. MySQL's FORMAT=TREE
// returns one multi-line value (split into lines); MariaDB's tabular EXPLAIN
// becomes one "column=value …" line per row, skipping NULLs.
func planResult(r *engine.Result) *engine.Result {
	out := &engine.Result{Columns: []engine.Column{{Name: "plan", Type: "text"}}, RowsAffected: -1, Duration: r.Duration, Truncated: r.Truncated}
	if len(r.Columns) == 1 {
		for _, row := range r.Rows {
			for _, line := range strings.Split(row[0].Text, "\n") {
				if strings.TrimSpace(line) != "" {
					out.Rows = append(out.Rows, []engine.Value{{Text: line}})
				}
			}
		}
		return out
	}
	for _, row := range r.Rows {
		var parts []string
		for i, v := range row {
			if !v.Null && v.Text != "" {
				parts = append(parts, r.Columns[i].Name+"="+v.Text)
			}
		}
		out.Rows = append(out.Rows, []engine.Value{{Text: strings.Join(parts, " ")}})
	}
	return out
}

// parseEnum returns the values of an enum(...) or set(...) COLUMN_TYPE, or
// nil for any other type. Quotes inside values are doubled ('').
func parseEnum(colType string) []string {
	open := strings.IndexByte(colType, '(')
	if open < 0 || !strings.HasSuffix(colType, ")") {
		return nil
	}
	s := colType[open+1 : len(colType)-1]
	var out []string
	var b strings.Builder
	in := false
	for i := 0; i < len(s); i++ {
		ch := s[i]
		switch {
		case !in && ch == '\'':
			in = true
			b.Reset()
		case in && ch == '\'' && i+1 < len(s) && s[i+1] == '\'':
			b.WriteByte('\'')
			i++
		case in && ch == '\'':
			in = false
			out = append(out, b.String())
		case in:
			b.WriteByte(ch)
		}
	}
	return out
}

type flavor int

const (
	flavorMySQL flavor = iota
	flavorMariaDB
)

// parseVersion reads VERSION() output, e.g. "8.4.11" or "11.4.13-MariaDB-ubu2404".
func parseVersion(v string) (flavor, string) {
	f, name := flavorMySQL, "MySQL"
	if strings.Contains(strings.ToLower(v), "mariadb") {
		f, name = flavorMariaDB, "MariaDB"
	}
	num, _, _ := strings.Cut(v, "-")
	return f, name + " " + num
}
```

`internal/engine/mysql/errors.go`:
```go
package mysql

import (
	"context"
	"database/sql/driver"
	"errors"
	"io"
	"net"
	"regexp"
	"strings"

	gomysql "github.com/go-sql-driver/mysql"

	"github.com/anderson-0/qh/internal/qerr"
)

var (
	unknownColumn = regexp.MustCompile(`Unknown column '([^']+)'`)
	noSuchTable   = regexp.MustCompile(`Table '([^']+)' doesn't exist`)
)

func readOnlyErr() *qerr.Error {
	return qerr.New(qerr.ReadOnly, "connection is read-only; only statements that read data are allowed").
		WithHint("Use a read-write connection to change data")
}

func timeoutErr(err error) *qerr.Error {
	return qerr.Wrap(qerr.Timeout, "query exceeded the connection timeout", err).
		WithHint("Add WHERE/LIMIT or narrow the range")
}

// classify maps an error from a running statement to a qerr category.
func classify(ctx context.Context, err error) error {
	var qe *qerr.Error
	if errors.As(err, &qe) {
		return qe
	}
	var me *gomysql.MySQLError
	if errors.As(err, &me) {
		switch me.Number {
		case 3024, 1969, 1317: // max_execution_time, max_statement_time, interrupted
			return timeoutErr(err)
		case 1792, 1290: // read-only transaction, --read-only server
			return readOnlyErr()
		case 1054:
			e := qerr.Wrap(qerr.QueryError, me.Message, err)
			if m := unknownColumn.FindStringSubmatch(me.Message); m != nil {
				e.UnknownColumn = m[1]
			}
			return e
		case 1146:
			e := qerr.Wrap(qerr.QueryError, me.Message, err)
			if m := noSuchTable.FindStringSubmatch(me.Message); m != nil {
				name := m[1]
				if i := strings.LastIndex(name, "."); i >= 0 {
					name = name[i+1:]
				}
				e.UnknownTable = name
			}
			return e
		case 1045, 1044, 1049:
			return connectErr(err)
		}
		return qerr.Wrap(qerr.QueryError, me.Message, err)
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return timeoutErr(err)
	}
	var ne net.Error
	if errors.As(err, &ne) || errors.Is(err, driver.ErrBadConn) || errors.Is(err, gomysql.ErrInvalidConn) || errors.Is(err, io.EOF) {
		return qerr.Wrap(qerr.ConnectionFailed, "lost the connection to the database: "+err.Error(), err)
	}
	return qerr.Wrap(qerr.QueryError, err.Error(), err)
}

// connectErr maps an error raised while connecting. Everything here is
// connection_failed; the message says why.
func connectErr(err error) error {
	var qe *qerr.Error
	if errors.As(err, &qe) {
		return qe
	}
	var me *gomysql.MySQLError
	if errors.As(err, &me) {
		if me.Number == 1045 || me.Number == 1044 {
			return qerr.Wrap(qerr.ConnectionFailed, "authentication failed: "+me.Message, err)
		}
		return qerr.Wrap(qerr.ConnectionFailed, me.Message, err)
	}
	var ne net.Error
	if errors.Is(err, context.DeadlineExceeded) || errors.As(err, &ne) && ne.Timeout() {
		return qerr.Wrap(qerr.ConnectionFailed, "timed out connecting to the database", err)
	}
	return qerr.Wrap(qerr.ConnectionFailed, "cannot reach the database: "+err.Error(), err)
}
```

- [ ] **Step 5: Run the tests**

Run: `go mod tidy && go test -short ./internal/engine/mysql/ && go vet ./internal/engine/mysql/`
Expected: PASS. (`go mod tidy` keeps the driver because the package now imports it.)

- [ ] **Step 6: Commit**

```bash
git add go.mod go.sum internal/engine/mysql
git commit -m "feat(mysql): parse connection strings, render values and classify errors"
```

---

### Task 2: Engine connection, queries, introspection and conformance

**Files:**
- Create: `internal/engine/mysql/conn.go`, `internal/engine/mysql/introspect.go`
- Test: `internal/engine/mysql/mysql_test.go`, `internal/engine/mysql/layers_test.go`; golden files `internal/engine/mysql/testdata/{mysql,mariadb}_{overview,orders,users}.golden` (generated in Step 6)

**Interfaces:**
- Consumes: everything from Task 1; `engine.ConnSpec`, `engine.QueryOpts`, `engine.Catalog` and friends; `sqlx.Classify`; `enginetest.Run` and `enginetest.Harness`; `format.Schema`.
- Produces: `(Engine).Open(ctx, spec) (engine.Conn, error)`, so `Engine` now implements `engine.Engine`; `init` registers `Engine{}` ("mysql") and `Engine{name: "mariadb"}`; package var `slotWait`.

- [ ] **Step 1: Add the test-container modules**

Run: `go get github.com/testcontainers/testcontainers-go/modules/mysql@v0.44.0 github.com/testcontainers/testcontainers-go/modules/mariadb@v0.44.0`

- [ ] **Step 2: Write the failing tests**

`internal/engine/mysql/mysql_test.go`:
```go
package mysql

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	tcmariadb "github.com/testcontainers/testcontainers-go/modules/mariadb"
	tcmysql "github.com/testcontainers/testcontainers-go/modules/mysql"

	"github.com/anderson-0/qh/internal/engine"
	"github.com/anderson-0/qh/internal/engine/enginetest"
	"github.com/anderson-0/qh/internal/qerr"
)

// fixture holds the shared conformance data (see enginetest) plus a mixed-case table.
func fixture() []string {
	s := []string{
		"ALTER DATABASE qh CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci",
		`CREATE TABLE users (id BIGINT PRIMARY KEY, email VARCHAR(255) NOT NULL UNIQUE COMMENT 'Login email',
			name VARCHAR(255), bio TEXT, avatar BLOB, created_at DATETIME) COMMENT 'App users'`,
		`CREATE TABLE orders (id BIGINT AUTO_INCREMENT PRIMARY KEY, user_id BIGINT NOT NULL,
			status ENUM('pending','paid','shipped') NOT NULL DEFAULT 'pending', total DECIMAL(12,2), note TEXT,
			INDEX orders_user_id (user_id), FOREIGN KEY (user_id) REFERENCES users(id))`,
		`CREATE TABLE order_items (order_id BIGINT NOT NULL, sku VARCHAR(64) NOT NULL, qty INT NOT NULL,
			PRIMARY KEY (order_id, sku), FOREIGN KEY (order_id) REFERENCES orders(id))`,
		"CREATE VIEW paid_orders AS SELECT id, user_id, total FROM orders WHERE status = 'paid'",
		"CREATE TABLE AuditLog (id BIGINT PRIMARY KEY, EventName TEXT)",
		`INSERT INTO users VALUES
			(1, 'alice@example.com', 'Alice', CONCAT('line1', CHAR(10), 'line2', CHAR(9), 'x'), X'DEADBEEF', '2025-03-14 10:22:31'),
			(2, 'zoe@example.com', 'Zoë 🚀', NULL, NULL, NULL),
			(3, 'nobody@example.com', NULL, NULL, NULL, NULL)`,
	}
	var orders, items []string
	for i := 1; i <= 250; i++ {
		status := "pending"
		if i%2 == 0 {
			status = "paid"
		}
		orders = append(orders, fmt.Sprintf("(%d, %d, '%s', %d * 1.5)", i, i%3+1, status, i))
	}
	for i := 1; i <= 10; i++ {
		items = append(items, fmt.Sprintf("(%d, 'sku-1', 1)", i), fmt.Sprintf("(%d, 'sku-2', 2)", i))
	}
	return append(s,
		"INSERT INTO orders (id, user_id, status, total) VALUES "+strings.Join(orders, ", "),
		"INSERT INTO order_items VALUES "+strings.Join(items, ", "),
		"INSERT INTO AuditLog VALUES (1, 'login')",
		"ANALYZE TABLE users, orders, order_items, AuditLog",
	)
}

// startDB starts image, loads the fixture and returns a go-sql-driver DSN.
func startDB(t *testing.T, image string) string {
	t.Helper()
	if testing.Short() {
		t.Skip("needs Docker")
	}
	testcontainers.SkipIfProviderIsNotHealthy(t)
	ctx := context.Background()
	var dsn string
	if strings.HasPrefix(image, "mariadb") {
		ctr, err := tcmariadb.Run(ctx, image, tcmariadb.WithDatabase("qh"), tcmariadb.WithUsername("qh"), tcmariadb.WithPassword("secret-pw-123"))
		t.Cleanup(func() { _ = testcontainers.TerminateContainer(ctr) })
		if err != nil {
			t.Fatal(err)
		}
		if dsn, err = ctr.ConnectionString(ctx); err != nil {
			t.Fatal(err)
		}
	} else {
		ctr, err := tcmysql.Run(ctx, image, tcmysql.WithDatabase("qh"), tcmysql.WithUsername("qh"), tcmysql.WithPassword("secret-pw-123"))
		t.Cleanup(func() { _ = testcontainers.TerminateContainer(ctr) })
		if err != nil {
			t.Fatal(err)
		}
		if dsn, err = ctr.ConnectionString(ctx); err != nil {
			t.Fatal(err)
		}
	}
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, s := range fixture() {
		if _, err := db.ExecContext(ctx, s); err != nil {
			t.Fatalf("fixture: %v\n%s", err, s)
		}
	}
	return dsn
}

func specs(dsn string) (engine.ConnSpec, engine.ConnSpec) {
	return engine.ConnSpec{ID: "ro", Version: 1, Engine: "mysql", DSN: dsn, Mode: engine.ModeRO, PoolSize: 3},
		engine.ConnSpec{ID: "rw", Version: 1, Engine: "mysql", DSN: dsn, Mode: engine.ModeRW, PoolSize: 3}
}

func openConn(t *testing.T, spec engine.ConnSpec) engine.Conn {
	t.Helper()
	c, err := Engine{}.Open(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

const slowQuery = "SELECT COUNT(*) FROM orders a, orders b, orders c, orders d WHERE a.total + b.total + c.total + d.total > 0"

func TestConformance(t *testing.T) {
	images := []struct{ image, golden string }{
		{"mysql:8.0", "mysql"}, {"mysql:8.4", "mysql"}, {"mariadb:10.11", "mariadb"}, {"mariadb:11.4", "mariadb"},
	}
	for _, im := range images {
		t.Run(im.image, func(t *testing.T) {
			enginetest.Run(t, enginetest.Harness{
				Engine:    Engine{},
				Setup:     func(t *testing.T) (engine.ConnSpec, engine.ConnSpec) { return specs(startDB(t, im.image)) },
				SlowQuery: slowQuery,
				ReadOnlyEscapes: []string{
					"CREATE TEMPORARY TABLE evil_tmp (x int)",
					"SELECT * FROM users INTO OUTFILE '/tmp/qh-evil.txt'",
					"LOAD DATA LOCAL INFILE '/etc/hosts' INTO TABLE users",
					"HANDLER users OPEN",
					"SET SESSION TRANSACTION READ WRITE",
					"SELECT 1; COMMIT; DELETE FROM order_items",
					"/*! DELETE FROM order_items */ SELECT 1",
					"REPLACE INTO users (id, email) VALUES (1, 'x@example.com')",
				},
				GoldenPrefix: im.golden,
			})
		})
	}
}

func TestEnginesRegistered(t *testing.T) {
	for _, name := range []string{"mysql", "mariadb"} {
		e, err := engine.Get(name)
		if err != nil || e.Name() != name {
			t.Fatalf("Get(%q) = %v, %v", name, e, err)
		}
	}
}

func TestSessionStateDoesNotLeak(t *testing.T) {
	dsn := startDB(t, "mysql:8.4")
	ro, _ := specs(dsn)
	ro.PoolSize = 1
	c := openConn(t, ro)
	ctx := context.Background()
	opts := engine.QueryOpts{MaxRows: 10, ReadOnly: true, Timeout: 5 * time.Second}

	r, err := c.Query(ctx, "SELECT GET_LOCK('qh_leak', 0)", opts)
	if err != nil || r.Rows[0][0].Text != "1" {
		t.Fatalf("get_lock: %+v %v", r, err)
	}
	other, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	var free int
	if err := other.QueryRowContext(ctx, "SELECT IS_FREE_LOCK('qh_leak')").Scan(&free); err != nil || free != 1 {
		t.Fatalf("named lock leaked: free=%d err=%v", free, err)
	}

	capped := opts
	capped.MaxRows = 2
	if r, err := c.Query(ctx, "SELECT id FROM orders ORDER BY id", capped); err != nil || !r.Truncated {
		t.Fatalf("capped: %+v %v", r, err)
	}
	cat, err := c.Introspect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if orders := cat.Lookup("orders"); len(orders) != 1 || len(orders[0].Fields) != 5 {
		t.Fatalf("sql_select_limit leaked into introspection: %+v", orders)
	}
}

func TestExplicitLimitAboveCap(t *testing.T) {
	ro, _ := specs(startDB(t, "mariadb:11.4"))
	ro.PoolSize = 1
	c := openConn(t, ro)
	ctx := context.Background()
	start := time.Now()
	r, err := c.Query(ctx, "SELECT a.id FROM orders a, orders b ORDER BY a.id, b.id LIMIT 60000",
		engine.QueryOpts{MaxRows: 5, ReadOnly: true, Timeout: 20 * time.Second})
	if err != nil || len(r.Rows) != 5 || !r.Truncated {
		t.Fatalf("explicit limit: %+v %v", r, err)
	}
	if d := time.Since(start); d > 10*time.Second {
		t.Fatalf("took %v", d)
	}
	if r, err := c.Query(ctx, "SELECT 1 AS one", engine.QueryOpts{MaxRows: 5, ReadOnly: true, Timeout: 5 * time.Second}); err != nil || r.Rows[0][0].Text != "1" {
		t.Fatalf("next query after an early stop: %+v %v", r, err)
	}
}

func TestBusy(t *testing.T) {
	ro, _ := specs(startDB(t, "mysql:8.4"))
	ro.PoolSize = 1
	c := openConn(t, ro)
	old := slotWait
	slotWait = 200 * time.Millisecond
	defer func() { slotWait = old }()
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, _ = c.Query(context.Background(), slowQuery, engine.QueryOpts{MaxRows: 1, ReadOnly: true, Timeout: 2 * time.Second})
	}()
	time.Sleep(300 * time.Millisecond)
	_, err := c.Query(context.Background(), "SELECT 1", engine.QueryOpts{MaxRows: 1, ReadOnly: true, Timeout: 5 * time.Second})
	if e := qerr.From(err); err == nil || e.Category != qerr.Busy {
		t.Fatalf("err = %v", err)
	}
	wg.Wait()
}

func TestAuthFailed(t *testing.T) {
	dsn := startDB(t, "mysql:8.4")
	bad := strings.Replace(dsn, "secret-pw-123", "wrong-pw", 1)
	_, err := Engine{}.Open(context.Background(), engine.ConnSpec{ID: "x", DSN: bad, Mode: engine.ModeRO, PoolSize: 1})
	if err == nil || !strings.HasPrefix(err.Error(), "error[connection_failed]: authentication failed") || strings.Contains(err.Error(), "wrong-pw") {
		t.Fatalf("err = %v", err)
	}
}

func TestConnectionFailed(t *testing.T) {
	for _, host := range []string{"127.0.0.1", "localhost"} {
		_, err := Engine{}.Open(context.Background(), engine.ConnSpec{ID: "x",
			DSN: "mysql://u:hunter22@" + host + ":1/db?timeout=2s", Mode: engine.ModeRO, PoolSize: 1})
		if e := qerr.From(err); err == nil || e.Category != qerr.ConnectionFailed {
			t.Fatalf("%s: %v", host, err)
		}
		if msg := err.Error(); strings.Contains(msg, "\n") || strings.Contains(msg, "hunter22") {
			t.Fatalf("%s: %q", host, msg)
		}
	}
}
```

`internal/engine/mysql/layers_test.go`:
```go
package mysql

import (
	"context"
	"testing"
	"time"

	"github.com/anderson-0/qh/internal/engine"
	"github.com/anderson-0/qh/internal/qerr"
	"github.com/anderson-0/qh/internal/sqlx"
)

// TestReadOnlyTransactionLayer calls run directly, bypassing the engine's own
// classification, to prove the database's read-only transaction holds too.
func TestReadOnlyTransactionLayer(t *testing.T) {
	ro, _ := specs(startDB(t, "mysql:8.4"))
	c := openConn(t, ro).(*conn)
	ctx := context.Background()
	opts := engine.QueryOpts{MaxRows: 10, ReadOnly: true, Timeout: 5 * time.Second}
	for _, q := range []string{
		"INSERT INTO users (id, email) VALUES (98, 'layer@example.com')",
		"CREATE TEMPORARY TABLE layer_tmp (x int)",
	} {
		_, err := c.run(ctx, q, opts, sqlx.Write, true)
		if e := qerr.From(err); err == nil || e.Category != qerr.ReadOnly {
			t.Errorf("%q: %v", q, err)
		}
	}
	r, err := c.run(ctx, "SELECT COUNT(*) FROM users", opts, sqlx.Read, true)
	if err != nil || r.Rows[0][0].Text != "3" {
		t.Fatalf("data changed: %+v %v", r, err)
	}
}
```

- [ ] **Step 3: Run them to verify they fail**

Run: `go test -short ./internal/engine/mysql/`
Expected: FAIL — `Engine.Open undefined` / `undefined: conn` / `undefined: slotWait`.

- [ ] **Step 4: Implement the connection and query path**

`internal/engine/mysql/conn.go`:
```go
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
// connection carries nothing into the next call. Any failure discards it.
func (c *conn) finish(sc *sql.Conn, commit, rw bool) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var err error
	if rw {
		_, err = sc.ExecContext(ctx, "UNLOCK TABLES")
	}
	if err == nil {
		end := "ROLLBACK"
		if commit {
			end = "COMMIT"
		}
		_, err = sc.ExecContext(ctx, end)
	}
	if err == nil {
		_, err = sc.ExecContext(ctx, c.resetSQL())
	}
	if err != nil {
		discard(sc)
	}
	return err
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
```

`internal/engine/mysql/introspect.go`:
```go
package mysql

import (
	"context"
	"database/sql"
	"strings"

	"golang.org/x/sync/errgroup"

	"github.com/anderson-0/qh/internal/engine"
)

const systemSchemas = "'mysql', 'information_schema', 'performance_schema', 'sys'"

// filter scopes catalog queries to the connection's database, or to every
// non-system database when the connection string names none.
func (c *conn) filter(col string) (string, []any) {
	if c.dbName != "" {
		return col + " = ?", []any{c.dbName}
	}
	return col + " NOT IN (" + systemSchemas + ")", nil
}

type tableRow struct {
	schema, name, kind, comment string
	rows                        sql.NullInt64
}

type columnRow struct {
	schema, table, name, colType, dataType, nullable, extra, comment string
	dflt                                                             sql.NullString
}

type keyRow struct {
	schema, table, constraint, column, refSchema, refTable, refColumn string
}

type indexRow struct {
	schema, table, index string
	nonUnique            int
	column               sql.NullString
}

func (c *conn) queryRows(ctx context.Context, q string, args []any, each func(*sql.Rows) error) error {
	rows, err := c.db.QueryContext(ctx, q, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		if err := each(rows); err != nil {
			return err
		}
	}
	return rows.Err()
}

func (c *conn) Introspect(ctx context.Context) (*engine.Catalog, error) {
	var (
		tables  []tableRow
		columns []columnRow
		keys    []keyRow
		indexes []indexRow
	)
	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() error {
		f, args := c.filter("TABLE_SCHEMA")
		return c.queryRows(gctx, `SELECT TABLE_SCHEMA, TABLE_NAME, TABLE_TYPE, TABLE_ROWS, COALESCE(TABLE_COMMENT, '')
			FROM information_schema.TABLES WHERE `+f+` ORDER BY TABLE_SCHEMA, TABLE_NAME`, args, func(r *sql.Rows) error {
			var x tableRow
			if err := r.Scan(&x.schema, &x.name, &x.kind, &x.rows, &x.comment); err != nil {
				return err
			}
			tables = append(tables, x)
			return nil
		})
	})
	g.Go(func() error {
		f, args := c.filter("TABLE_SCHEMA")
		return c.queryRows(gctx, `SELECT TABLE_SCHEMA, TABLE_NAME, COLUMN_NAME, COLUMN_TYPE, DATA_TYPE, IS_NULLABLE,
				COLUMN_DEFAULT, EXTRA, COALESCE(COLUMN_COMMENT, '')
			FROM information_schema.COLUMNS WHERE `+f+` ORDER BY TABLE_SCHEMA, TABLE_NAME, ORDINAL_POSITION`, args, func(r *sql.Rows) error {
			var x columnRow
			if err := r.Scan(&x.schema, &x.table, &x.name, &x.colType, &x.dataType, &x.nullable, &x.dflt, &x.extra, &x.comment); err != nil {
				return err
			}
			columns = append(columns, x)
			return nil
		})
	})
	g.Go(func() error {
		f, args := c.filter("TABLE_SCHEMA")
		return c.queryRows(gctx, `SELECT TABLE_SCHEMA, TABLE_NAME, CONSTRAINT_NAME, COLUMN_NAME,
				REFERENCED_TABLE_SCHEMA, REFERENCED_TABLE_NAME, REFERENCED_COLUMN_NAME
			FROM information_schema.KEY_COLUMN_USAGE
			WHERE REFERENCED_TABLE_NAME IS NOT NULL AND `+f+`
			ORDER BY TABLE_SCHEMA, TABLE_NAME, CONSTRAINT_NAME, ORDINAL_POSITION`, args, func(r *sql.Rows) error {
			var x keyRow
			if err := r.Scan(&x.schema, &x.table, &x.constraint, &x.column, &x.refSchema, &x.refTable, &x.refColumn); err != nil {
				return err
			}
			keys = append(keys, x)
			return nil
		})
	})
	g.Go(func() error {
		f, args := c.filter("TABLE_SCHEMA")
		return c.queryRows(gctx, `SELECT TABLE_SCHEMA, TABLE_NAME, INDEX_NAME, NON_UNIQUE, COLUMN_NAME
			FROM information_schema.STATISTICS WHERE `+f+`
			ORDER BY TABLE_SCHEMA, TABLE_NAME, INDEX_NAME, SEQ_IN_INDEX`, args, func(r *sql.Rows) error {
			var x indexRow
			if err := r.Scan(&x.schema, &x.table, &x.index, &x.nonUnique, &x.column); err != nil {
				return err
			}
			indexes = append(indexes, x)
			return nil
		})
	})
	if err := g.Wait(); err != nil {
		return nil, classify(ctx, err)
	}
	return c.assemble(tables, columns, keys, indexes), nil
}

func (c *conn) assemble(tables []tableRow, columns []columnRow, keys []keyRow, indexes []indexRow) *engine.Catalog {
	cat := &engine.Catalog{}
	nsIndex := map[string]int{}
	objs := map[string]*engine.Object{}
	key := func(schema, name string) string { return schema + "\x00" + name }
	for _, t := range tables {
		kind := engine.KindTable
		switch t.kind {
		case "VIEW", "SYSTEM VIEW":
			kind = engine.KindView
		case "SEQUENCE":
			continue
		}
		i, ok := nsIndex[t.schema]
		if !ok {
			i = len(cat.Namespaces)
			nsIndex[t.schema] = i
			cat.Namespaces = append(cat.Namespaces, engine.Namespace{Name: t.schema})
		}
		o := &engine.Object{Namespace: t.schema, Name: t.name, Kind: kind, RowEstimate: -1}
		if kind == engine.KindTable {
			o.Comment = t.comment
			if t.rows.Valid {
				o.RowEstimate = t.rows.Int64
			}
		}
		cat.Namespaces[i].Objects = append(cat.Namespaces[i].Objects, o)
		objs[key(t.schema, t.name)] = o
	}
	for _, col := range columns {
		o := objs[key(col.schema, col.table)]
		if o == nil {
			continue
		}
		f := engine.Field{Name: col.name, Type: col.colType, Nullable: col.nullable == "YES", Comment: col.comment}
		if col.dataType == "enum" || col.dataType == "set" {
			f.Type = col.dataType
			f.Enum = parseEnum(col.colType)
		}
		switch {
		case strings.Contains(col.extra, "auto_increment"):
			f.Default = "auto"
		case col.dflt.Valid && !(c.flavor == flavorMariaDB && col.dflt.String == "NULL"):
			f.Default = col.dflt.String
		}
		o.Fields = append(o.Fields, f)
	}
	type fkKey struct{ schema, table, constraint string }
	fks := map[fkKey]*engine.ForeignKey{}
	var fkOrder []fkKey
	for _, k := range keys {
		id := fkKey{k.schema, k.table, k.constraint}
		fk := fks[id]
		if fk == nil {
			fk = &engine.ForeignKey{Namespace: k.schema, Object: k.table, RefNamespace: k.refSchema, RefObject: k.refTable}
			fks[id] = fk
			fkOrder = append(fkOrder, id)
		}
		fk.Columns = append(fk.Columns, k.column)
		fk.RefColumns = append(fk.RefColumns, k.refColumn)
	}
	for _, id := range fkOrder {
		if o := objs[key(id.schema, id.table)]; o != nil {
			o.ForeignKeys = append(o.ForeignKeys, *fks[id])
		}
	}
	type ixKey struct{ schema, table, index string }
	ixs := map[ixKey]*engine.Index{}
	var ixOrder []ixKey
	for _, x := range indexes {
		o := objs[key(x.schema, x.table)]
		if o == nil {
			continue
		}
		col := x.column.String
		if !x.column.Valid {
			col = "<expr>"
		}
		if x.index == "PRIMARY" {
			o.PrimaryKey = append(o.PrimaryKey, col)
			continue
		}
		id := ixKey{x.schema, x.table, x.index}
		ix := ixs[id]
		if ix == nil {
			ix = &engine.Index{Name: x.index, Unique: x.nonUnique == 0}
			ixs[id] = ix
			ixOrder = append(ixOrder, id)
		}
		ix.Columns = append(ix.Columns, col)
	}
	for _, id := range ixOrder {
		o := objs[key(id.schema, id.table)]
		o.Indexes = append(o.Indexes, *ixs[id])
	}
	cat.Link()
	return cat
}
```

- [ ] **Step 5: Run the Docker-free tests**

Run: `go mod tidy && go test -short ./internal/engine/mysql/ && go vet ./...`
Expected: PASS. Task 1 tests and `TestConnectionFailed` run; the Docker tests report SKIP.

- [ ] **Step 6: Generate and review the golden files (needs Docker)**

Run: `go test ./internal/engine/mysql/ -run 'TestConformance/(mysql:8.4|mariadb:11.4)' -update`

Check the generated files. The expected contents are below.

`testdata/mysql_overview.golden`:
```
qh: 4 tables, 1 view
  AuditLog ~1 · order_items ~20 · orders ~250 · paid_orders (view) · users ~3
```
`testdata/mysql_orders.golden`:
```
qh.orders ~250 rows
  id bigint PK =auto
  user_id bigint NN → users.id
  status enum NN [pending|paid|shipped] =pending
  total decimal(12,2)
  note text
  ← order_items.order_id
  idx (user_id)
```
`testdata/mysql_users.golden`:
```
qh.users ~3 rows: App users
  id bigint PK
  email varchar(255) NN  # Login email
  name varchar(255)
  bio text
  avatar blob
  created_at datetime
  ← orders.user_id
  idx unique(email)
```
The `mariadb_*` files should differ only in MariaDB's own spellings: `bigint(20)` and `int(11)` widths, and the quoted default `='pending'`.

**Row estimates:** InnoDB statistics on these small tables after `ANALYZE TABLE` should be exact. If an estimate differs from the exact count, keep the generated value only if it is identical across three consecutive runs and identical between the two images of the same flavour. If it varies, stop and report it rather than editing the golden file by hand.

- [ ] **Step 7: Run every MySQL test on all four images (needs Docker)**

Run: `go test ./internal/engine/mysql/ -v -run . 2>&1 | grep -E '^(=== RUN|--- |PASS|FAIL|ok)' | tail -60`
Expected: every test passes, including `TestConformance/mysql:8.0`, `/mysql:8.4`, `/mariadb:10.11` and `/mariadb:11.4`.

If one version produces a different golden file than its sibling (for example 8.0 vs 8.4), stop and report it. Do not split the golden prefixes on your own.

- [ ] **Step 8: Commit**

```bash
git add go.mod go.sum internal/engine/mysql
git commit -m "feat(mysql): add MySQL/MariaDB engine with read-only transactions and session reset"
```

---

### Task 3: Make the engine available in the CLI

**Files:**
- Modify: `internal/cli/engines.go`, `internal/cli/conn.go` (the `--engine` flag help text), `README.md`
- Test: `internal/cli/cli_test.go`

**Interfaces:**
- Consumes: the `mysql` package (registers `mysql` and `mariadb` in `init`).

- [ ] **Step 1: Write the failing test**

Append to `internal/cli/cli_test.go`:
```go
func TestMySQLEngineIsRegistered(t *testing.T) {
	home := t.TempDir()
	_, errOut, code := run(t, home, "", "conn", "add", "shop", "--engine", "mysql",
		"--dsn", "mysql://u:hunter22@127.0.0.1:1/db?timeout=1s")
	if code != 1 || !strings.HasPrefix(errOut, "error[connection_failed]") || strings.Contains(errOut, "hunter22") {
		t.Fatalf("%d %q", code, errOut)
	}
	out, errOut, code := run(t, home, "", "conn", "add", "maria", "--engine", "mariadb",
		"--dsn", "mariadb://u:hunter22@127.0.0.1:1/db", "--skip-test")
	if code != 0 || !strings.HasPrefix(out, "added maria (mariadb, ro)") {
		t.Fatalf("%d %q %q", code, out, errOut)
	}
	if out, _, _ = run(t, home, "", "conn", "show", "maria"); !strings.Contains(out, "dsn: mariadb://u:***@127.0.0.1:1/db") {
		t.Fatalf("show: %q", out)
	}
}
```

- [ ] **Step 2: Run it to verify it fails**

Run: `go test ./internal/cli/ -run TestMySQLEngineIsRegistered`
Expected: FAIL — `error[invalid]: unknown engine "mysql"`.

- [ ] **Step 3: Register the engine and update texts**

`internal/cli/engines.go`:
```go
package cli

// Engines register themselves in init.
import (
	_ "github.com/anderson-0/qh/internal/engine/mysql"
	_ "github.com/anderson-0/qh/internal/engine/postgres"
	_ "github.com/anderson-0/qh/internal/engine/sqlite"
)
```

In `internal/cli/conn.go`, change the `--engine` flag's usage string from `"postgres or sqlite (required)"` to `"postgres, mysql (alias mariadb) or sqlite (required)"`.

In `README.md`:
- Change the sentence beginning "Supported now:" to: "Supported now: PostgreSQL 13+, MySQL 8.0.16+, MariaDB 10.6+ and SQLite."
- Change "MySQL/MariaDB, an HTTP server mode…" to start at "An HTTP server mode…". MySQL is no longer planned; it is done.
- After the SQLite `qh conn add` example, add:
````markdown
```bash
qh conn add app --engine mysql --dsn - --description "Product catalogue"
```
````
- In "Read-only safety", add after the Postgres SQL block:
````markdown
For MySQL/MariaDB, grant only `SELECT` (and `SHOW VIEW` for views):

```sql
CREATE USER 'qh_reader'@'%' IDENTIFIED BY 'change-me';
GRANT SELECT, SHOW VIEW ON shop.* TO 'qh_reader'@'%';
```

Don't grant the `FILE` privilege: with it, `SELECT LOAD_FILE(...)` reads files on the database server.
````

- [ ] **Step 4: Run the tests and full verification**

Run: `go test ./internal/cli/ && go vet ./... && go test -race ./... && make build && ./bin/qh conn add --help | grep -- --engine`
Expected: all PASS (Docker running), and the help line shows `postgres, mysql (alias mariadb) or sqlite (required)`.

- [ ] **Step 5: Commit**

```bash
git add internal/cli README.md
git commit -m "feat(cli): enable the MySQL/MariaDB engine"
```
