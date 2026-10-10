# qh Plan 1: Local MVP — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A working `qh` binary that serves the three MCP tools (`sources`, `schema`, `query`) over stdio for SQLite and PostgreSQL connections managed with a local CLI.

**Architecture:** One Go module. Engines (`internal/engine/postgres`, `internal/engine/sqlite`) implement a shared `engine.Engine`/`engine.Conn` interface and pass a shared conformance suite. `internal/core.Service` is the only place that applies policy (read-only, row caps, errors, suggestions, secret scrubbing); the MCP layer and the CLI are thin adapters over it. Connections live in a local SQLite store with AES-GCM-sealed connection strings.

**Tech Stack:** Go 1.27, `github.com/modelcontextprotocol/go-sdk` v1.8.0, `github.com/jackc/pgx/v5` v5.11.0, `modernc.org/sqlite` v1.60.1 (pure Go), `github.com/spf13/cobra` v1.10.2, `golang.org/x/sync` v0.24.0, `github.com/testcontainers/testcontainers-go` v0.44.0 (+ `modules/postgres`).

**Spec:** `docs/superpowers/specs/2026-10-09-qh-design.md`

## Plan series

The spec is delivered by four plans, each ending in working software:

| Plan | Delivers | Spec sections |
|---|---|---|
| **1 — Local MVP (this plan)** | stdio MCP server, SQLite + Postgres engines, local store, CLI | §3, §4 (all), §5.1–5.2 (no key rotation), §6.3, §8 (local commands), §9.1 (engine suite, unit, MCP) |
| 2 — MySQL/MariaDB | `internal/engine/mysql` passing the same conformance suite on MySQL 8 and MariaDB 11 | §4.2 MySQL row capping/timeouts, §6.3 |
| 3 — Server mode | `qh serve`, HTTP MCP, REST API + OpenAPI, workspaces, API keys and scopes, per-key limits, netguard, `--sqlite-root`, query log, `qh masterkey rotate`, structured connection fields + `tls`, `/healthz` `/readyz` | §5.1 (`api_keys`, `query_log`), §5.2 rotation, §6.1–6.2, §6.4, §7, §8 remote mode |
| 4 — Benchmark | `bench/` harness vs dbhub, exact tool-definition token count, micro-benchmarks, report | §9.2, §1 success criteria |

## Spec adjustments made while planning

These are deliberate, small departures from the spec; each is simpler or safer than the spec text.

1. **No `revision` counter.** Every tool call reads the connection row from the store (a ~20 µs indexed SQLite read). Pools and schema caches are keyed by connection id + version, so CLI edits made while `qh mcp` runs take effect on the next call without any change-detection mechanism.
2. **No separate `ClassifyError` method.** Engines return classified `*qerr.Error` values directly from `Query`/`Explain`/`Introspect`/`Ping`.
3. **`Conn.Ping` returns the server version**, so the connection test can report it.
4. **Session and transaction control statements are rejected** (`BEGIN`, `COMMIT`, `SET`, `RESET`, `USE`, `ATTACH`, …) with `error[invalid]`, because each call runs in its own transaction and a `SET` would leak into pooled connections.
5. **Tool-definition size is checked by bytes here** (`tools/list` JSON ≤ 2,400 bytes ≈ 800 tokens). The exact token count against the Anthropic API lands with the benchmark (Plan 4).
6. **The SQLite engine also classifies statements itself** before running them, because SQLite has file side effects that a read-only open does not cover (`ATTACH`, `VACUUM INTO`).
7. **Store schema version uses `PRAGMA user_version`** instead of a `meta` table.
8. **The Postgres engine always uses the extended query protocol.** pgx silently switches to the simple protocol for argument-less `Exec` calls, and the simple protocol runs multiple statements, so `COMMIT; DELETE …` could escape a read-only transaction. Every statement therefore goes through `tx.Query(..., pgx.QueryExecModeExec)`, which the server rejects if it contains more than one statement.

## Global Constraints

- Module path `github.com/anderson-0/qh`; `go 1.27` in `go.mod`.
- Pure Go: `CGO_ENABLED=0 go build ./cmd/qh` must succeed.
- Defaults: `max_rows` 100 (hard max 10,000); timeout 30 s (max 300 s); pool size 5 (max 50); cell values cut at 200 characters; schema overview ≤ 200 objects; schema detail ≤ 30 objects; schema cache TTL 10 min; idle pools closed after 10 min; pool-slot wait 10 s; server-wide cap 500 database connections; open-failure backoff 1 s doubling to 30 s.
- Connection names match `^[a-z0-9][a-z0-9_-]{0,63}$`.
- Every agent-facing error is one line: `error[<category>]: <message>. <hint>` with categories `auth`, `forbidden`, `unknown_source`, `invalid`, `read_only`, `timeout`, `connection_failed`, `query_error`, `busy`.
- Connection strings and passwords are never printed, logged, or returned; errors are scrubbed of both.
- In `qh mcp`, stdout carries only MCP protocol messages; anything else goes to stderr.
- Tests that need Docker call `testcontainers.SkipIfProviderIsNotHealthy(t)` and are skipped under `go test -short`.
- Query text is never rewritten; row caps are applied by fetching `cap + 1` rows.

## Review Focus

Inputs the spec implies but does not spell out, most likely to bite first. Each has a test in the owning task.

1. **Trailing semicolons and trailing comments** (`SELECT 1;`, `SELECT 1 -- note`) must run, not be rejected as multiple statements or break the Postgres cursor. → Task 2 (`sqlx`), Task 9 suite case `trailing_semicolon`.
2. **A database that is down** must fail fast with `connection_failed`, back off, leave other connections unaffected, and recover when it returns. → Task 12 `TestBackoffAndRecovery`, `TestFailureIsolated`.
3. **SQLite paths with spaces, and paths that do not exist.** Spaces must work (macOS "Application Support"); a missing path must fail without creating a file. → Task 9 `TestOpenMissingFileDoesNotCreate`, fixture directory `with space`.
4. **Mixed-case and quoted identifiers** (`"AuditLog"."EventName"`) must appear in schema output and be found by case-insensitive suggestions. → Task 6 `TestColumnHintCaseInsensitive`, Task 10 `TestMixedCaseIdentifiers`.
5. **Huge cell values** (1 MB text or JSON) must render as at most 200 characters plus a short suffix. → Task 4 `TestHugeValueIsBounded`.

---

## File map

```
cmd/qh/main.go                          entry point
cmd/qh/e2e_test.go                      builds the binary, drives `qh mcp` over stdio
internal/cli/root.go                    cobra root, Execute, --home
internal/cli/app.go                     opens store, keyring, pools, cache, service
internal/cli/engines.go                 blank imports registering engines
internal/cli/conn.go                    qh conn add|ls|show|edit|rm|test
internal/cli/query.go                   qh query, qh schema, qh mcp
internal/qerr/qerr.go                   classified errors
internal/sqlx/lex.go                    SQL tokenizer (Postgres, MySQL, SQLite rules)
internal/sqlx/classify.go               Classify, FirstKeyword, ReferencedTables
internal/engine/engine.go               interfaces, ConnSpec, QueryOpts, Result, registry
internal/engine/catalog.go              Catalog model, Link, Lookup, Only, Count
internal/engine/enginetest/suite.go     shared conformance suite
internal/engine/sqlite/sqlite.go        SQLite engine
internal/engine/sqlite/introspect.go    SQLite catalog queries
internal/engine/postgres/postgres.go    Postgres engine
internal/engine/postgres/introspect.go  Postgres catalog queries
internal/testutil/sqlite.go             SQLite fixture database for tests
internal/format/result.go               TSV renderer
internal/format/schema.go               schema renderer
internal/suggest/suggest.go             did-you-mean
internal/secret/secret.go               AES-256-GCM keyring, key file
internal/store/store.go                 Store interface, Connection, errors
internal/store/sqlite.go                SQLite store + migrations
internal/catalog/cache.go               schema cache
internal/pool/pool.go                   pool manager
internal/core/service.go                agent-facing operations
internal/core/connections.go            connection management
internal/mcpserver/server.go            MCP tools
Makefile, .gitignore, README.md, .github/workflows/ci.yml
```

---

### Task 1: Toolchain and scaffold

**Files:**
- Create: `go.mod`, `Makefile`, `.gitignore`, `cmd/qh/main.go`, `internal/cli/root.go`
- Test: `internal/cli/root_test.go`

**Interfaces:**
- Produces: `cli.Execute(version string, args []string, stdin io.Reader, stdout, stderr io.Writer) int`; unexported `newRoot(version string) *cobra.Command` that later tasks extend.

- [ ] **Step 1: Install Go and confirm the version**

Run: `brew install go && go version`
Expected: `go version go1.27.x darwin/arm64` (any 1.27 patch release or newer).

- [ ] **Step 2: Create the module and add the dependencies**

```bash
cd ~/Documents/GitHub/anderson-0/qh
go mod init github.com/anderson-0/qh
go get github.com/spf13/cobra@v1.10.2
```
Then edit `go.mod` so the `go` line reads `go 1.27`.

- [ ] **Step 3: Write the failing test**

`internal/cli/root_test.go`:
```go
package cli

import (
	"bytes"
	"strings"
	"testing"
)

func TestVersion(t *testing.T) {
	var out, errOut bytes.Buffer
	code := Execute("1.2.3", []string{"version"}, strings.NewReader(""), &out, &errOut)
	if code != 0 {
		t.Fatalf("exit code %d, stderr %q", code, errOut.String())
	}
	if out.String() != "1.2.3\n" {
		t.Fatalf("stdout %q", out.String())
	}
}

func TestUnknownCommandFails(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := Execute("dev", []string{"nope"}, strings.NewReader(""), &out, &errOut); code != 1 {
		t.Fatalf("exit code %d", code)
	}
	if !strings.Contains(errOut.String(), "unknown command") {
		t.Fatalf("stderr %q", errOut.String())
	}
}
```

- [ ] **Step 4: Run it to verify it fails**

Run: `go test ./internal/cli/`
Expected: FAIL — `undefined: Execute`.

- [ ] **Step 5: Implement**

`internal/cli/root.go`:
```go
// Package cli implements the qh command line.
package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"
)

// Execute runs the qh command line and returns the process exit code.
func Execute(version string, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	root := newRoot(version)
	root.SetArgs(args)
	root.SetIn(stdin)
	root.SetOut(stdout)
	root.SetErr(stderr)
	if err := root.ExecuteContext(ctx); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}

func newRoot(version string) *cobra.Command {
	root := &cobra.Command{
		Use:           "qh",
		Short:         "Token-efficient database server for AI agents",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.AddCommand(&cobra.Command{
		Use:   "version",
		Short: "Print the version",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			fmt.Fprintln(cmd.OutOrStdout(), version)
			return nil
		},
	})
	return root
}
```

`cmd/qh/main.go`:
```go
package main

import (
	"os"

	"github.com/anderson-0/qh/internal/cli"
)

var version = "dev"

func main() {
	os.Exit(cli.Execute(version, os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}
```

`Makefile`:
```make
.PHONY: build test test-short race lint

build:
	CGO_ENABLED=0 go build -trimpath -o bin/qh ./cmd/qh

test:
	go test ./...

test-short:
	go test -short ./...

race:
	go test -race ./...

lint:
	go vet ./...
```

`.gitignore`:
```
/bin/
*.test
*.out
```

- [ ] **Step 6: Run the tests and the binary**

Run: `go mod tidy && go test ./internal/cli/ && go run ./cmd/qh version`
Expected: `ok  github.com/anderson-0/qh/internal/cli`, then `dev`.

- [ ] **Step 7: Commit**

```bash
git add go.mod go.sum Makefile .gitignore cmd internal
git commit -m "chore: scaffold qh module and CLI root"
```

---

### Task 2: Classified errors and the SQL lexer

**Files:**
- Create: `internal/qerr/qerr.go`, `internal/sqlx/lex.go`, `internal/sqlx/classify.go`
- Test: `internal/qerr/qerr_test.go`, `internal/sqlx/classify_test.go`

**Interfaces:**
- Produces:
  - `qerr.Category` constants `Auth, Forbidden, UnknownSource, Invalid, ReadOnly, Timeout, ConnectionFailed, QueryError, Busy`.
  - `type qerr.Error struct { Category Category; Message, Hint, UnknownColumn, UnknownTable string; Err error }` with `Error() string`, `Unwrap() error`, `WithHint(string) *Error`.
  - `qerr.New(c Category, msg string) *Error`, `qerr.Wrap(c Category, msg string, err error) *Error`, `qerr.From(err error) *Error`.
  - `type sqlx.Dialect` with `Postgres, MySQL, SQLite`; `type sqlx.Access` with `Read, Write, DDL`.
  - `sqlx.Classify(sql string, d Dialect) (Access, error)` — errors are `*qerr.Error` with category `invalid`.
  - `sqlx.FirstKeyword(sql string, d Dialect) string`, `sqlx.ReferencedTables(sql string, d Dialect) []string`.

- [ ] **Step 1: Write the failing qerr test**

`internal/qerr/qerr_test.go`:
```go
package qerr

import (
	"errors"
	"fmt"
	"testing"
)

func TestErrorFormat(t *testing.T) {
	cases := []struct {
		err  *Error
		want string
	}{
		{New(Timeout, "query exceeded 30s").WithHint("Add WHERE/LIMIT or narrow the range"),
			"error[timeout]: query exceeded 30s. Add WHERE/LIMIT or narrow the range"},
		{New(Invalid, "query is empty."), "error[invalid]: query is empty"},
		{New(QueryError, "  boom  "), "error[query_error]: boom"},
	}
	for _, c := range cases {
		if got := c.err.Error(); got != c.want {
			t.Errorf("got %q want %q", got, c.want)
		}
	}
}

func TestFrom(t *testing.T) {
	base := New(ReadOnly, "connection is read-only")
	wrapped := fmt.Errorf("context: %w", base)
	if got := From(wrapped); got != base {
		t.Fatalf("From did not unwrap: %v", got)
	}
	plain := errors.New("disk full")
	got := From(plain)
	if got.Category != QueryError || got.Message != "disk full" || !errors.Is(got, plain) {
		t.Fatalf("From(plain) = %#v", got)
	}
}
```

- [ ] **Step 2: Run it to verify it fails**

Run: `go test ./internal/qerr/`
Expected: FAIL — `undefined: New`.

- [ ] **Step 3: Implement qerr**

`internal/qerr/qerr.go`:
```go
// Package qerr defines the classified errors agents see as one line:
// error[<category>]: <message>. <hint>
package qerr

import (
	"errors"
	"fmt"
	"strings"
)

// Category is the machine-readable error class.
type Category string

const (
	Auth             Category = "auth"
	Forbidden        Category = "forbidden"
	UnknownSource    Category = "unknown_source"
	Invalid          Category = "invalid"
	ReadOnly         Category = "read_only"
	Timeout          Category = "timeout"
	ConnectionFailed Category = "connection_failed"
	QueryError       Category = "query_error"
	Busy             Category = "busy"
)

// Error is a classified error.
type Error struct {
	Category Category
	Message  string
	Hint     string
	// UnknownColumn and UnknownTable carry the identifier the database could
	// not resolve, so the core can add suggestions.
	UnknownColumn string
	UnknownTable  string
	// Err is the underlying error. It is never shown to agents.
	Err error
}

// New returns an Error with no underlying cause.
func New(c Category, msg string) *Error { return &Error{Category: c, Message: msg} }

// Wrap returns an Error that wraps err.
func Wrap(c Category, msg string, err error) *Error {
	return &Error{Category: c, Message: msg, Err: err}
}

// WithHint sets the hint and returns e.
func (e *Error) WithHint(h string) *Error {
	e.Hint = h
	return e
}

func (e *Error) Error() string {
	msg := strings.TrimRight(strings.TrimSpace(e.Message), ".")
	s := fmt.Sprintf("error[%s]: %s", e.Category, msg)
	if h := strings.TrimSpace(e.Hint); h != "" {
		s += ". " + h
	}
	return s
}

func (e *Error) Unwrap() error { return e.Err }

// From returns err as an *Error, classifying unknown errors as query_error.
func From(err error) *Error {
	var e *Error
	if errors.As(err, &e) {
		return e
	}
	return Wrap(QueryError, err.Error(), err)
}
```

- [ ] **Step 4: Run it to verify it passes**

Run: `go test ./internal/qerr/`
Expected: PASS.

- [ ] **Step 5: Write the failing sqlx tests**

`internal/sqlx/classify_test.go`:
```go
package sqlx

import (
	"reflect"
	"strings"
	"testing"
)

func TestClassify(t *testing.T) {
	cases := []struct {
		sql     string
		d       Dialect
		want    Access
		wantErr string
	}{
		{"SELECT 1", Postgres, Read, ""},
		{"  select * from users;", Postgres, Read, ""},
		{"SELECT 1; -- trailing note", Postgres, Read, ""},
		{"SELECT 1 -- note without newline", Postgres, Read, ""},
		{"(SELECT 1) UNION (SELECT 2)", Postgres, Read, ""},
		{"WITH x AS (SELECT 1) SELECT * FROM x", Postgres, Read, ""},
		{"WITH d AS (DELETE FROM t RETURNING *) SELECT * FROM d", Postgres, Write, ""},
		{"SELECT * INTO new_t FROM t", Postgres, Write, ""},
		{"SELECT 'insert into x' AS s", Postgres, Read, ""},
		{`SELECT "update" FROM t`, Postgres, Read, ""},
		{"SELECT $$ DELETE FROM t $$", Postgres, Read, ""},
		{"SELECT $fn$ ; DROP TABLE t $fn$", Postgres, Read, ""},
		{"SELECT E'it\\'s; DROP TABLE t'", Postgres, Read, ""},
		{"SELECT 1 /* ; DROP TABLE t */", Postgres, Read, ""},
		{"SELECT 1 /* outer /* inner */ ; still comment */", Postgres, Read, ""},
		{"SELECT $1", Postgres, Read, ""},
		{"INSERT INTO t VALUES (1)", Postgres, Write, ""},
		{"update t set x = 1", Postgres, Write, ""},
		{"MERGE INTO t USING s ON true WHEN MATCHED THEN DELETE", Postgres, Write, ""},
		{"CREATE TABLE t (x int)", Postgres, DDL, ""},
		{"ALTER TABLE t ADD COLUMN y int", Postgres, DDL, ""},
		{"DROP TABLE t", Postgres, DDL, ""},
		{"COMMENT ON TABLE t IS 'x'", Postgres, DDL, ""},
		{"TRUNCATE t", Postgres, Write, ""},
		{"EXPLAIN SELECT 1", Postgres, Read, ""},
		{"EXPLAIN ANALYZE DELETE FROM t", Postgres, Write, ""},
		{"SHOW search_path", Postgres, Read, ""},
		{"TABLE users", Postgres, Read, ""},
		{"VALUES (1), (2)", Postgres, Read, ""},
		{"SELECT nextval('s')", Postgres, Read, ""},
		{"frobnicate the thing", Postgres, Write, ""},
		{"VACUUM", SQLite, Write, ""},
		{"VACUUM INTO '/tmp/x.db'", SQLite, Write, ""},
		{"PRAGMA table_info(users)", SQLite, Read, ""},
		{"PRAGMA main.table_info(users)", SQLite, Read, ""},
		{"PRAGMA query_only = 0", SQLite, Write, ""},
		{"PRAGMA journal_mode", SQLite, Write, ""},
		{"SELECT `update` FROM t", SQLite, Read, ""},
		{"SELECT [delete] FROM t", SQLite, Read, ""},
		{"SELECT 1 # comment ; DROP TABLE t", MySQL, Read, ""},
		{`SELECT "a;b"`, MySQL, Read, ""},
		{"SELECT 'a\\';DROP'", MySQL, Read, ""},
		{"DESCRIBE users", MySQL, Read, ""},
		{"SELECT 1; SELECT 2", Postgres, 0, "only one statement per call"},
		{"COMMIT; DELETE FROM t", Postgres, 0, "only one statement per call"},
		{"BEGIN", Postgres, 0, "BEGIN statements are not supported"},
		{"SET TRANSACTION READ WRITE", Postgres, 0, "SET statements are not supported"},
		{"ATTACH DATABASE 'x.db' AS x", SQLite, 0, "ATTACH statements are not supported"},
		{"USE other_db", MySQL, 0, "USE statements are not supported"},
		{"", Postgres, 0, "query is empty"},
		{"  ;  ", Postgres, 0, "query is empty"},
		{"-- just a comment", Postgres, 0, "query is empty"},
		{"42", Postgres, 0, "does not start with a statement keyword"},
		{"SELECT 'unterminated", Postgres, 0, "unterminated string"},
		{"SELECT 1 /* open", Postgres, 0, "unterminated comment"},
		{"SELECT $$ open", Postgres, 0, "unterminated dollar-quoted string"},
		{`SELECT "open`, Postgres, 0, "unterminated quoted identifier"},
	}
	for _, c := range cases {
		got, err := Classify(c.sql, c.d)
		if c.wantErr != "" {
			if err == nil || !strings.Contains(err.Error(), c.wantErr) {
				t.Errorf("Classify(%q) err = %v, want containing %q", c.sql, err, c.wantErr)
			}
			if err != nil && !strings.HasPrefix(err.Error(), "error[invalid]") {
				t.Errorf("Classify(%q) err category: %v", c.sql, err)
			}
			continue
		}
		if err != nil || got != c.want {
			t.Errorf("Classify(%q) = %v, %v; want %v", c.sql, got, err, c.want)
		}
	}
}

func TestFirstKeyword(t *testing.T) {
	cases := map[string]string{
		"(SELECT 1)":          "select",
		"  WITH x AS (SELECT 1) SELECT 1": "with",
		"/* c */ values (1)":  "values",
		"SELECT 1; SELECT 2":  "",
		"":                    "",
	}
	for sql, want := range cases {
		if got := FirstKeyword(sql, Postgres); got != want {
			t.Errorf("FirstKeyword(%q) = %q, want %q", sql, got, want)
		}
	}
}

func TestReferencedTables(t *testing.T) {
	cases := []struct {
		sql  string
		want []string
	}{
		{"SELECT * FROM users u JOIN orders o ON o.user_id = u.id", []string{"users", "orders"}},
		{"SELECT * FROM public.users, orders AS o WHERE 1=1", []string{"public.users", "orders"}},
		{`SELECT * FROM "UserAccounts"`, []string{"UserAccounts"}},
		{"UPDATE users SET x = 1", []string{"users"}},
		{"INSERT INTO audit (a) SELECT a FROM src", []string{"audit", "src"}},
		{"SELECT * FROM (SELECT 1) AS s", nil},
		{"SELECT 1", nil},
		{"SELECT 'unterminated", nil},
	}
	for _, c := range cases {
		if got := ReferencedTables(c.sql, Postgres); !reflect.DeepEqual(got, c.want) {
			t.Errorf("ReferencedTables(%q) = %#v, want %#v", c.sql, got, c.want)
		}
	}
}

func FuzzClassify(f *testing.F) {
	for _, s := range []string{"SELECT 1", "WITH x AS (SELECT 1) SELECT 1", "SELECT $$a$$", "SELECT 'a''b'", "PRAGMA x.y(1)"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		for _, d := range []Dialect{Postgres, MySQL, SQLite} {
			_, _ = Classify(s, d)
			_ = ReferencedTables(s, d)
			_ = FirstKeyword(s, d)
		}
	})
}
```

- [ ] **Step 6: Run them to verify they fail**

Run: `go test ./internal/sqlx/`
Expected: FAIL — `undefined: Classify`.

- [ ] **Step 7: Implement the lexer**

`internal/sqlx/lex.go`:
```go
// Package sqlx is a small SQL lexer used to classify statements, count them
// and find the tables they reference. It never rewrites SQL.
package sqlx

import (
	"strings"

	"github.com/anderson-0/qh/internal/qerr"
)

// Dialect selects quoting and comment rules.
type Dialect int

const (
	Postgres Dialect = iota + 1
	MySQL
	SQLite
)

type kind int

const (
	kWord   kind = iota + 1 // unquoted identifier or keyword, lowercased
	kQuoted                 // quoted identifier, case preserved
	kSemi
	kLParen
	kRParen
	kComma
	kDot
	kEquals
	kOther // literals, operators, parameters
)

type token struct {
	kind kind
	text string
}

func unterminated(what string) error {
	return qerr.New(qerr.Invalid, "unterminated "+what+" in query")
}

func tokenize(s string, d Dialect) ([]token, error) {
	var toks []token
	n := len(s)
	for i := 0; i < n; {
		c := s[i]
		switch {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\f' || c == '\v':
			i++
		case c == '-' && i+1 < n && s[i+1] == '-', c == '#' && d == MySQL:
			for i < n && s[i] != '\n' {
				i++
			}
		case c == '/' && i+1 < n && s[i+1] == '*':
			end, ok := skipBlockComment(s, i, d == Postgres)
			if !ok {
				return nil, unterminated("comment")
			}
			i = end
		case c == '\'':
			end, ok := skipQuoted(s, i, '\'', d == MySQL)
			if !ok {
				return nil, unterminated("string")
			}
			toks = append(toks, token{kOther, "'"})
			i = end
		case (c == 'e' || c == 'E') && d == Postgres && i+1 < n && s[i+1] == '\'':
			end, ok := skipQuoted(s, i+1, '\'', true)
			if !ok {
				return nil, unterminated("string")
			}
			toks = append(toks, token{kOther, "'"})
			i = end
		case c == '"' && d == MySQL:
			end, ok := skipQuoted(s, i, '"', true)
			if !ok {
				return nil, unterminated("string")
			}
			toks = append(toks, token{kOther, "'"})
			i = end
		case c == '"':
			end, text, ok := scanIdent(s, i, '"')
			if !ok {
				return nil, unterminated("quoted identifier")
			}
			toks = append(toks, token{kQuoted, text})
			i = end
		case c == '`' && d != Postgres:
			end, text, ok := scanIdent(s, i, '`')
			if !ok {
				return nil, unterminated("quoted identifier")
			}
			toks = append(toks, token{kQuoted, text})
			i = end
		case c == '[' && d == SQLite:
			end := strings.IndexByte(s[i+1:], ']')
			if end < 0 {
				return nil, unterminated("quoted identifier")
			}
			toks = append(toks, token{kQuoted, s[i+1 : i+1+end]})
			i += end + 2
		case c == '$' && d == Postgres:
			if tag, ok := dollarTag(s, i); ok {
				end := strings.Index(s[i+len(tag):], tag)
				if end < 0 {
					return nil, unterminated("dollar-quoted string")
				}
				toks = append(toks, token{kOther, "$$"})
				i += len(tag) + end + len(tag)
			} else {
				toks = append(toks, token{kOther, "$"})
				i++
			}
		case c == ';':
			toks = append(toks, token{kSemi, ";"})
			i++
		case c == '(':
			toks = append(toks, token{kLParen, "("})
			i++
		case c == ')':
			toks = append(toks, token{kRParen, ")"})
			i++
		case c == ',':
			toks = append(toks, token{kComma, ","})
			i++
		case c == '.':
			toks = append(toks, token{kDot, "."})
			i++
		case c == '=':
			toks = append(toks, token{kEquals, "="})
			i++
		case isIdentStart(c):
			j := i + 1
			for j < n && isIdentPart(s[j]) {
				j++
			}
			toks = append(toks, token{kWord, strings.ToLower(s[i:j])})
			i = j
		case c >= '0' && c <= '9':
			j := i + 1
			for j < n && (isIdentPart(s[j]) || s[j] == '.') {
				j++
			}
			toks = append(toks, token{kOther, s[i:j]})
			i = j
		default:
			toks = append(toks, token{kOther, string(c)})
			i++
		}
	}
	return toks, nil
}

func isIdentStart(c byte) bool {
	return c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= 0x80
}

func isIdentPart(c byte) bool {
	return isIdentStart(c) || c >= '0' && c <= '9' || c == '$'
}

// skipBlockComment returns the index just past the comment starting at i.
// Postgres block comments nest; the others end at the first */.
func skipBlockComment(s string, i int, nested bool) (int, bool) {
	depth := 0
	for j := i; j < len(s)-1; {
		switch {
		case s[j] == '/' && s[j+1] == '*' && (nested || depth == 0):
			depth++
			j += 2
		case s[j] == '*' && s[j+1] == '/':
			depth--
			j += 2
			if depth == 0 {
				return j, true
			}
		default:
			j++
		}
	}
	return len(s), false
}

// skipQuoted returns the index just past the quoted string starting at i.
// A doubled quote is an escaped quote; backslash escapes when enabled.
func skipQuoted(s string, i int, q byte, backslash bool) (int, bool) {
	for j := i + 1; j < len(s); j++ {
		switch {
		case backslash && s[j] == '\\':
			j++
		case s[j] == q:
			if j+1 < len(s) && s[j+1] == q {
				j++
				continue
			}
			return j + 1, true
		}
	}
	return len(s), false
}

// scanIdent reads a quoted identifier starting at i and returns its unquoted text.
func scanIdent(s string, i int, q byte) (int, string, bool) {
	var b strings.Builder
	for j := i + 1; j < len(s); j++ {
		if s[j] == q {
			if j+1 < len(s) && s[j+1] == q {
				b.WriteByte(q)
				j++
				continue
			}
			return j + 1, b.String(), true
		}
		b.WriteByte(s[j])
	}
	return len(s), "", false
}

// dollarTag reports whether a Postgres dollar-quote tag ($$ or $name$) starts at i.
func dollarTag(s string, i int) (string, bool) {
	j := i + 1
	if j < len(s) && s[j] >= '0' && s[j] <= '9' {
		return "", false // $1 is a parameter
	}
	for j < len(s) && (isIdentStart(s[j]) || s[j] >= '0' && s[j] <= '9') {
		j++
	}
	if j < len(s) && s[j] == '$' {
		return s[i : j+1], true
	}
	return "", false
}
```

- [ ] **Step 8: Implement classification**

`internal/sqlx/classify.go`:
```go
package sqlx

import (
	"strings"

	"github.com/anderson-0/qh/internal/qerr"
)

// Access is what a statement needs from the database.
type Access int

const (
	Read  Access = iota + 1 // reads only
	Write                   // changes data
	DDL                     // changes the schema; invalidates schema caches
)

func (a Access) String() string {
	switch a {
	case Read:
		return "read"
	case Write:
		return "write"
	case DDL:
		return "ddl"
	}
	return "unknown"
}

func set(words ...string) map[string]bool {
	m := make(map[string]bool, len(words))
	for _, w := range words {
		m[w] = true
	}
	return m
}

var (
	sessionControl = set("begin", "start", "commit", "rollback", "end", "abort", "savepoint",
		"release", "set", "reset", "discard", "use", "prepare", "execute", "deallocate",
		"attach", "detach", "listen", "unlisten")
	ddlWords    = set("create", "alter", "drop", "comment", "rename")
	readPragmas = set("table_info", "table_xinfo", "index_list", "index_info", "index_xinfo",
		"foreign_key_list", "database_list", "table_list", "compile_options", "collation_list",
		"function_list", "pragma_list", "module_list")
	// reserved words that end a table reference (they are never aliases).
	reserved = set("select", "where", "join", "inner", "left", "right", "full", "outer", "cross",
		"on", "group", "order", "limit", "union", "natural", "using", "having", "window", "offset",
		"fetch", "for", "lateral", "set", "values", "returning", "as", "and", "or", "not",
		"intersect", "except")
)

func statements(toks []token) [][]token {
	var out [][]token
	start := 0
	for i, t := range toks {
		if t.kind == kSemi {
			if i > start {
				out = append(out, toks[start:i])
			}
			start = i + 1
		}
	}
	if start < len(toks) {
		out = append(out, toks[start:])
	}
	return out
}

// parse tokenizes sql and returns its single statement.
func parse(sql string, d Dialect) ([]token, error) {
	toks, err := tokenize(sql, d)
	if err != nil {
		return nil, err
	}
	stmts := statements(toks)
	switch len(stmts) {
	case 0:
		return nil, qerr.New(qerr.Invalid, "query is empty")
	case 1:
		return stmts[0], nil
	}
	return nil, qerr.New(qerr.Invalid, "only one statement per call is supported").
		WithHint("Send each statement in its own query call")
}

func firstWord(t []token) string {
	for _, tk := range t {
		if tk.kind == kLParen {
			continue
		}
		if tk.kind == kWord {
			return tk.text
		}
		return ""
	}
	return ""
}

func anyOf(words map[string]bool, keys ...string) bool {
	for _, k := range keys {
		if words[k] {
			return true
		}
	}
	return false
}

// Classify returns the access sql needs. It rejects empty input, more than
// one statement, and session or transaction control statements.
func Classify(sql string, d Dialect) (Access, error) {
	t, err := parse(sql, d)
	if err != nil {
		return 0, err
	}
	words := map[string]bool{}
	for _, tk := range t {
		if tk.kind == kWord {
			words[tk.text] = true
		}
	}
	first := firstWord(t)
	switch {
	case first == "":
		return 0, qerr.New(qerr.Invalid, "query does not start with a statement keyword")
	case sessionControl[first]:
		return 0, qerr.New(qerr.Invalid, strings.ToUpper(first)+" statements are not supported").
			WithHint("Each query call runs in its own transaction")
	case ddlWords[first]:
		return DDL, nil
	case first == "select":
		if words["into"] { // SELECT ... INTO creates a table (Postgres) or writes a file (MySQL)
			return Write, nil
		}
		return Read, nil
	case first == "with":
		if anyOf(words, "insert", "update", "delete", "merge", "into") {
			return Write, nil
		}
		return Read, nil
	case first == "explain":
		if words["analyze"] && anyOf(words, "insert", "update", "delete", "merge", "create", "drop", "alter", "truncate", "into") {
			return Write, nil
		}
		return Read, nil
	case first == "values" || first == "table" || first == "show" || first == "describe" || first == "desc":
		return Read, nil
	case first == "pragma":
		if d == SQLite && readPragma(t) {
			return Read, nil
		}
		return Write, nil
	}
	return Write, nil // unknown statements fail closed
}

// readPragma reports whether t is PRAGMA [schema.]name[(arg)] for a known
// read-only pragma, with nothing assigned.
func readPragma(t []token) bool {
	for _, tk := range t {
		if tk.kind == kEquals {
			return false
		}
	}
	rest := t[1:]
	name := ""
	switch {
	case len(rest) >= 3 && rest[1].kind == kDot:
		name = rest[2].text
	case len(rest) >= 1:
		name = rest[0].text
	}
	return readPragmas[name]
}

// FirstKeyword returns the leading keyword of sql, lowercased, or "" when sql
// is not exactly one parseable statement.
func FirstKeyword(sql string, d Dialect) string {
	t, err := parse(sql, d)
	if err != nil {
		return ""
	}
	return firstWord(t)
}

// ReferencedTables returns the table names that follow FROM, JOIN, UPDATE and
// INTO, as written ("name" or "schema.name"), without duplicates.
func ReferencedTables(sql string, d Dialect) []string {
	toks, err := tokenize(sql, d)
	if err != nil {
		return nil
	}
	var out []string
	seen := map[string]bool{}
	for i, t := range toks {
		if t.kind != kWord {
			continue
		}
		switch t.text {
		case "from", "join", "update", "into":
		default:
			continue
		}
		j := i + 1
		for {
			name, next := readName(toks, j)
			if name == "" {
				break
			}
			if !seen[name] {
				seen[name] = true
				out = append(out, name)
			}
			j = skipAlias(toks, next)
			if t.text == "from" && j < len(toks) && toks[j].kind == kComma {
				j++
				continue
			}
			break
		}
	}
	return out
}

func isName(t token) bool {
	return t.kind == kQuoted || t.kind == kWord && !reserved[t.text]
}

func readName(toks []token, j int) (string, int) {
	if j >= len(toks) || !isName(toks[j]) {
		return "", j
	}
	name := toks[j].text
	j++
	if j+1 < len(toks) && toks[j].kind == kDot && (toks[j+1].kind == kWord || toks[j+1].kind == kQuoted) {
		name += "." + toks[j+1].text
		j += 2
	}
	return name, j
}

func skipAlias(toks []token, j int) int {
	if j < len(toks) && toks[j].kind == kWord && toks[j].text == "as" {
		return j + 2
	}
	if j < len(toks) && isName(toks[j]) {
		return j + 1
	}
	return j
}
```

- [ ] **Step 9: Run the tests and a short fuzz pass**

Run: `go test ./internal/sqlx/ ./internal/qerr/ && go test ./internal/sqlx/ -run=^$ -fuzz=FuzzClassify -fuzztime=20s`
Expected: PASS for both; the fuzz run reports no failures.

- [ ] **Step 10: Commit**

```bash
git add internal/qerr internal/sqlx
git commit -m "feat: add classified errors and SQL statement classifier"
```

---

### Task 3: Engine interfaces and catalog model

**Files:**
- Create: `internal/engine/engine.go`, `internal/engine/catalog.go`
- Test: `internal/engine/engine_test.go`

**Interfaces:**
- Consumes: `sqlx.Dialect`, `qerr`.
- Produces:
  - `type engine.Mode string` with `ModeRO = "ro"`, `ModeRW = "rw"`.
  - `type engine.ConnSpec struct { ID string; Version int64; Engine, DSN string; Mode Mode; PoolSize int }`.
  - `type engine.QueryOpts struct { MaxRows int; ReadOnly bool; Timeout time.Duration }`.
  - `type engine.Column struct { Name, Type string }`; `type engine.Value struct { Null bool; Text string; Binary bool; Size int }`.
  - `type engine.Result struct { Columns []Column; Rows [][]Value; Truncated bool; RowsAffected int64; Duration time.Duration }` (`RowsAffected` is -1 for row-returning statements).
  - `type engine.DSNInfo struct { Host string; Port int; Database, User, Password, Redacted string }`.
  - `type engine.Engine interface { Name() string; Dialect() sqlx.Dialect; Inspect(dsn string) (DSNInfo, error); Open(ctx context.Context, spec ConnSpec) (Conn, error) }`.
  - `type engine.Conn interface { Query(ctx, q string, o QueryOpts) (*Result, error); Explain(ctx, q string, o QueryOpts) (*Result, error); Introspect(ctx) (*Catalog, error); Ping(ctx) (string, error); Close() error }`.
  - `engine.Register(Engine)`, `engine.Get(name string) (Engine, error)`, `engine.Names() []string`.
  - `type engine.Kind string` with `KindTable, KindView, KindMatView, KindCollection`.
  - `Catalog{Namespaces []Namespace}`, `Namespace{Name string; Objects []*Object}`, `Object{Namespace, Name string; Kind Kind; Comment string; RowEstimate int64; Fields []Field; PrimaryKey []string; ForeignKeys, ReferencedBy []ForeignKey; Indexes []Index}`, `Field{Name, Type, Default, Comment string; Nullable bool; Enum []string; Inferred bool}`, `ForeignKey{Namespace, Object string; Columns []string; RefNamespace, RefObject string; RefColumns []string}`, `Index{Name string; Columns []string; Unique bool}`.
  - `(*Catalog).Link()`, `(*Catalog).Lookup(ref string) []*Object`, `(*Catalog).Only(namespaces []string) *Catalog`, `(*Catalog).Count() int`.

- [ ] **Step 1: Write the failing test**

`internal/engine/engine_test.go`:
```go
package engine

import (
	"context"
	"strings"
	"testing"

	"github.com/anderson-0/qh/internal/sqlx"
)

type fakeEngine struct{ name string }

func (f fakeEngine) Name() string                      { return f.name }
func (f fakeEngine) Dialect() sqlx.Dialect             { return sqlx.Postgres }
func (f fakeEngine) Inspect(string) (DSNInfo, error)   { return DSNInfo{}, nil }
func (f fakeEngine) Open(context.Context, ConnSpec) (Conn, error) { return nil, nil }

func TestRegistry(t *testing.T) {
	Register(fakeEngine{"fake-a"})
	if e, err := Get("fake-a"); err != nil || e.Name() != "fake-a" {
		t.Fatalf("Get = %v, %v", e, err)
	}
	_, err := Get("nope")
	if err == nil || !strings.Contains(err.Error(), "error[invalid]: unknown engine \"nope\"") || !strings.Contains(err.Error(), "fake-a") {
		t.Fatalf("Get(nope) err = %v", err)
	}
	defer func() {
		if recover() == nil {
			t.Fatal("duplicate Register did not panic")
		}
	}()
	Register(fakeEngine{"fake-a"})
}

func testCatalog() *Catalog {
	users := &Object{Namespace: "public", Name: "users", Kind: KindTable, PrimaryKey: []string{"id"}}
	orders := &Object{Namespace: "public", Name: "orders", Kind: KindTable,
		ForeignKeys: []ForeignKey{{Namespace: "public", Object: "orders", Columns: []string{"user_id"},
			RefNamespace: "public", RefObject: "users", RefColumns: []string{"id"}}}}
	events := &Object{Namespace: "analytics", Name: "Events", Kind: KindTable}
	return &Catalog{Namespaces: []Namespace{
		{Name: "analytics", Objects: []*Object{events}},
		{Name: "public", Objects: []*Object{orders, users}},
	}}
}

func TestLinkAndLookup(t *testing.T) {
	c := testCatalog()
	c.Link()
	c.Link() // idempotent
	users := c.Lookup("users")
	if len(users) != 1 || len(users[0].ReferencedBy) != 1 || users[0].ReferencedBy[0].Object != "orders" {
		t.Fatalf("users.ReferencedBy = %#v", users)
	}
	if got := c.Lookup("PUBLIC.Users"); len(got) != 1 {
		t.Fatalf("case-insensitive qualified lookup = %v", got)
	}
	if got := c.Lookup("events"); len(got) != 1 || got[0].Name != "Events" {
		t.Fatalf("case-insensitive lookup = %v", got)
	}
	if got := c.Lookup("missing"); len(got) != 0 {
		t.Fatalf("missing lookup = %v", got)
	}
	if c.Count() != 3 {
		t.Fatalf("Count = %d", c.Count())
	}
}

func TestOnly(t *testing.T) {
	c := testCatalog()
	if got := c.Only(nil); got != c {
		t.Fatal("Only(nil) should return the catalog unchanged")
	}
	got := c.Only([]string{"Public"})
	if len(got.Namespaces) != 1 || got.Namespaces[0].Name != "public" {
		t.Fatalf("Only(public) = %#v", got.Namespaces)
	}
}
```

- [ ] **Step 2: Run it to verify it fails**

Run: `go test ./internal/engine/`
Expected: FAIL — `undefined: Register`.

- [ ] **Step 3: Implement**

`internal/engine/engine.go`:
```go
// Package engine defines the interface every database engine implements and
// the engine-neutral models for results and schemas.
package engine

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/anderson-0/qh/internal/qerr"
	"github.com/anderson-0/qh/internal/sqlx"
)

// Mode is a connection's access mode.
type Mode string

const (
	ModeRO Mode = "ro"
	ModeRW Mode = "rw"
)

// ConnSpec is everything an engine needs to open a connection.
type ConnSpec struct {
	ID       string // stable connection id
	Version  int64  // increases on every change to the connection
	Engine   string
	DSN      string // plaintext connection string; never log it
	Mode     Mode
	PoolSize int
}

// QueryOpts controls one query.
type QueryOpts struct {
	MaxRows  int // rows to return; the engine fetches MaxRows+1 to detect truncation
	ReadOnly bool
	Timeout  time.Duration
}

// Column describes a result column.
type Column struct {
	Name string
	Type string
}

// Value is one cell, already in canonical text form.
type Value struct {
	Null   bool
	Text   string // booleans true/false, timestamps RFC 3339, JSON compact, numbers as the database prints them
	Binary bool   // binary data; Size holds the byte length and Text is empty
	Size   int
}

// Result is the outcome of one statement.
type Result struct {
	Columns      []Column
	Rows         [][]Value
	Truncated    bool
	RowsAffected int64 // -1 when the statement returned rows
	Duration     time.Duration
}

// DSNInfo is what qh may show about a connection string.
type DSNInfo struct {
	Host     string
	Port     int
	Database string
	User     string
	Password string // only for scrubbing error messages; never displayed
	Redacted string // the connection string with the password replaced by ***
}

// Engine is a database type.
type Engine interface {
	Name() string
	Dialect() sqlx.Dialect
	// Inspect parses dsn without connecting.
	Inspect(dsn string) (DSNInfo, error)
	Open(ctx context.Context, spec ConnSpec) (Conn, error)
}

// Conn is an open connection pool to one database. Methods return
// *qerr.Error values for every error they can classify.
type Conn interface {
	Query(ctx context.Context, q string, o QueryOpts) (*Result, error)
	Explain(ctx context.Context, q string, o QueryOpts) (*Result, error)
	Introspect(ctx context.Context) (*Catalog, error)
	// Ping checks the connection and returns the server version.
	Ping(ctx context.Context) (string, error)
	Close() error
}

var (
	mu       sync.RWMutex
	registry = map[string]Engine{}
)

// Register makes an engine available by name. It panics on duplicates.
func Register(e Engine) {
	mu.Lock()
	defer mu.Unlock()
	if _, dup := registry[e.Name()]; dup {
		panic("engine: duplicate registration of " + e.Name())
	}
	registry[e.Name()] = e
}

// Get returns the engine called name.
func Get(name string) (Engine, error) {
	mu.RLock()
	e, ok := registry[name]
	mu.RUnlock()
	if !ok {
		return nil, qerr.New(qerr.Invalid, fmt.Sprintf("unknown engine %q", name)).
			WithHint("Available: " + strings.Join(Names(), ", "))
	}
	return e, nil
}

// Names returns the registered engine names, sorted.
func Names() []string {
	mu.RLock()
	defer mu.RUnlock()
	names := make([]string, 0, len(registry))
	for n := range registry {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}
```

`internal/engine/catalog.go`:
```go
package engine

import "strings"

// Kind is the type of a schema object.
type Kind string

const (
	KindTable      Kind = "table"
	KindView       Kind = "view"
	KindMatView    Kind = "matview"
	KindCollection Kind = "collection"
)

// Catalog describes a database's schema in engine-neutral terms.
type Catalog struct {
	Namespaces []Namespace
}

// Namespace is a schema (Postgres), database (MySQL) or attached file (SQLite).
type Namespace struct {
	Name    string
	Objects []*Object
}

// Object is a table, view or collection.
type Object struct {
	Namespace    string
	Name         string
	Kind         Kind
	Comment      string
	RowEstimate  int64 // -1 when unknown
	Fields       []Field
	PrimaryKey   []string
	ForeignKeys  []ForeignKey // outgoing
	ReferencedBy []ForeignKey // incoming; filled by Link
	Indexes      []Index
}

// Field is a column, or an inferred document field.
type Field struct {
	Name     string
	Type     string
	Default  string
	Comment  string
	Nullable bool
	Enum     []string
	Inferred bool // sampled from documents rather than declared
}

// ForeignKey links Columns of Namespace.Object to RefColumns of RefNamespace.RefObject.
type ForeignKey struct {
	Namespace    string
	Object       string
	Columns      []string
	RefNamespace string
	RefObject    string
	RefColumns   []string
}

// Index is a secondary index.
type Index struct {
	Name    string
	Columns []string
	Unique  bool
}

// Link fills ReferencedBy from every object's ForeignKeys. It is idempotent.
func (c *Catalog) Link() {
	for _, ns := range c.Namespaces {
		for _, o := range ns.Objects {
			o.ReferencedBy = nil
		}
	}
	for _, ns := range c.Namespaces {
		for _, o := range ns.Objects {
			for _, fk := range o.ForeignKeys {
				if t := c.find(fk.RefNamespace, fk.RefObject); t != nil {
					t.ReferencedBy = append(t.ReferencedBy, fk)
				}
			}
		}
	}
}

func (c *Catalog) find(ns, name string) *Object {
	for _, n := range c.Namespaces {
		if n.Name != ns {
			continue
		}
		for _, o := range n.Objects {
			if o.Name == name {
				return o
			}
		}
	}
	return nil
}

// Lookup finds objects by "name" or "namespace.name", ignoring case.
func (c *Catalog) Lookup(ref string) []*Object {
	ns, name, qualified := strings.Cut(ref, ".")
	if !qualified {
		ns, name = "", ref
	}
	var out []*Object
	for _, n := range c.Namespaces {
		if ns != "" && !strings.EqualFold(n.Name, ns) {
			continue
		}
		for _, o := range n.Objects {
			if strings.EqualFold(o.Name, name) {
				out = append(out, o)
			}
		}
	}
	return out
}

// Count returns the number of objects.
func (c *Catalog) Count() int {
	n := 0
	for _, ns := range c.Namespaces {
		n += len(ns.Objects)
	}
	return n
}

// Only returns the catalog limited to the named namespaces (ignoring case).
// An empty list keeps everything and returns c itself.
func (c *Catalog) Only(namespaces []string) *Catalog {
	if len(namespaces) == 0 {
		return c
	}
	keep := map[string]bool{}
	for _, n := range namespaces {
		keep[strings.ToLower(n)] = true
	}
	out := &Catalog{}
	for _, ns := range c.Namespaces {
		if keep[strings.ToLower(ns.Name)] {
			out.Namespaces = append(out.Namespaces, ns)
		}
	}
	return out
}
```

- [ ] **Step 4: Run it to verify it passes**

Run: `go test ./internal/engine/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/engine
git commit -m "feat: add engine interfaces and catalog model"
```

---

### Task 4: TSV result renderer

**Files:**
- Create: `internal/format/result.go`
- Test: `internal/format/result_test.go`

**Interfaces:**
- Consumes: `engine.Result`, `engine.Value`.
- Produces: `format.Result(r *engine.Result) string`; `format.MaxCellRunes = 200`.

- [ ] **Step 1: Write the failing test**

`internal/format/result_test.go`:
```go
package format

import (
	"strings"
	"testing"
	"time"

	"github.com/anderson-0/qh/internal/engine"
)

func txt(s string) engine.Value { return engine.Value{Text: s} }

func TestResultTable(t *testing.T) {
	r := &engine.Result{
		Columns: []engine.Column{{Name: "id"}, {Name: "email"}, {Name: "bio"}, {Name: "avatar"}},
		Rows: [][]engine.Value{
			{txt("1"), txt("a@x.com"), txt("line1\nline2\tx\\y"), {Binary: true, Size: 2150}},
			{txt("2"), txt("b@x.com"), {Null: true}, {Binary: true, Size: 16}},
		},
		RowsAffected: -1,
		Duration:     38 * time.Millisecond,
	}
	want := "id\temail\tbio\tavatar\n" +
		"1\ta@x.com\tline1\\nline2\\tx\\\\y\t<bytes 2.1KB>\n" +
		"2\tb@x.com\tNULL\t<bytes 16B>\n" +
		"-- 2 rows · 38ms\n"
	if got := Result(r); got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestResultFooters(t *testing.T) {
	cols := []engine.Column{{Name: "n"}}
	cases := []struct {
		r    *engine.Result
		want string
	}{
		{&engine.Result{Columns: cols, Rows: [][]engine.Value{{txt("1")}}, RowsAffected: -1, Duration: 8 * time.Millisecond}, "n\n1\n-- 1 row · 8ms\n"},
		{&engine.Result{Columns: cols, RowsAffected: -1, Duration: 500 * time.Microsecond}, "n\n-- 0 rows · <1ms\n"},
		{&engine.Result{Columns: cols, Rows: [][]engine.Value{{txt("1")}, {txt("2")}}, Truncated: true, RowsAffected: -1, Duration: 12 * time.Second}, "n\n1\n2\n-- 2 rows shown, more exist (truncated) · 12.0s\n"},
		{&engine.Result{RowsAffected: 3, Duration: 12 * time.Millisecond}, "-- 3 rows affected · 12ms\n"},
		{&engine.Result{RowsAffected: 1, Duration: 12 * time.Millisecond}, "-- 1 row affected · 12ms\n"},
		{&engine.Result{RowsAffected: -1, Duration: 2 * time.Millisecond}, "-- ok · 2ms\n"},
	}
	for _, c := range cases {
		if got := Result(c.r); got != c.want {
			t.Errorf("got %q want %q", got, c.want)
		}
	}
}

func TestTruncateIsRuneAware(t *testing.T) {
	exact := strings.Repeat("é", MaxCellRunes)
	if got := cell(exact); got != exact {
		t.Fatalf("200 runes should be untouched")
	}
	long := strings.Repeat("é", MaxCellRunes+5)
	want := strings.Repeat("é", MaxCellRunes) + "…[+5 chars]"
	if got := cell(long); got != want {
		t.Fatalf("got %q", got)
	}
}

func TestHugeValueIsBounded(t *testing.T) {
	r := &engine.Result{
		Columns:      []engine.Column{{Name: "doc"}},
		Rows:         [][]engine.Value{{txt(strings.Repeat(`{"k":"v"}`, 120_000))}},
		RowsAffected: -1,
	}
	if got := Result(r); len(got) > 300 {
		t.Fatalf("output is %d bytes", len(got))
	}
}
```

- [ ] **Step 2: Run it to verify it fails**

Run: `go test ./internal/format/`
Expected: FAIL — `undefined: Result`.

- [ ] **Step 3: Implement**

`internal/format/result.go`:
```go
// Package format renders query results and schemas as compact text for agents.
package format

import (
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/anderson-0/qh/internal/engine"
)

// MaxCellRunes is the longest value shown before it is cut.
const MaxCellRunes = 200

var escaper = strings.NewReplacer(`\`, `\\`, "\t", `\t`, "\n", `\n`, "\r", `\r`)

// Result renders r as TSV with a header row and a one-line footer.
func Result(r *engine.Result) string {
	var b strings.Builder
	if len(r.Columns) > 0 {
		for i, c := range r.Columns {
			if i > 0 {
				b.WriteByte('\t')
			}
			b.WriteString(cell(c.Name))
		}
		b.WriteByte('\n')
		for _, row := range r.Rows {
			for i, v := range row {
				if i > 0 {
					b.WriteByte('\t')
				}
				b.WriteString(value(v))
			}
			b.WriteByte('\n')
		}
	}
	b.WriteString(footer(r))
	return b.String()
}

func value(v engine.Value) string {
	switch {
	case v.Null:
		return "NULL"
	case v.Binary:
		return "<bytes " + humanBytes(v.Size) + ">"
	}
	return cell(v.Text)
}

func cell(s string) string {
	s = truncate(s)
	if !strings.ContainsAny(s, "\\\t\n\r") {
		return s
	}
	return escaper.Replace(s)
}

func truncate(s string) string {
	if len(s) <= MaxCellRunes {
		return s
	}
	n := utf8.RuneCountInString(s)
	if n <= MaxCellRunes {
		return s
	}
	i := 0
	for k := 0; k < MaxCellRunes; k++ {
		_, size := utf8.DecodeRuneInString(s[i:])
		i += size
	}
	return s[:i] + fmt.Sprintf("…[+%d chars]", n-MaxCellRunes)
}

func humanBytes(n int) string {
	switch {
	case n < 1024:
		return fmt.Sprintf("%dB", n)
	case n < 1<<20:
		return fmt.Sprintf("%.1fKB", float64(n)/1024)
	case n < 1<<30:
		return fmt.Sprintf("%.1fMB", float64(n)/(1<<20))
	}
	return fmt.Sprintf("%.1fGB", float64(n)/(1<<30))
}

func plural(n int64, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return fmt.Sprintf("%d %ss", n, word)
}

func duration(d time.Duration) string {
	switch {
	case d < time.Millisecond:
		return "<1ms"
	case d < 10*time.Second:
		return fmt.Sprintf("%dms", d.Milliseconds())
	}
	return fmt.Sprintf("%.1fs", d.Seconds())
}

func footer(r *engine.Result) string {
	d := duration(r.Duration)
	if len(r.Columns) == 0 {
		if r.RowsAffected >= 0 {
			return fmt.Sprintf("-- %s affected · %s\n", plural(r.RowsAffected, "row"), d)
		}
		return "-- ok · " + d + "\n"
	}
	n := int64(len(r.Rows))
	if r.Truncated {
		return fmt.Sprintf("-- %s shown, more exist (truncated) · %s\n", plural(n, "row"), d)
	}
	return fmt.Sprintf("-- %s · %s\n", plural(n, "row"), d)
}
```

- [ ] **Step 4: Run it to verify it passes**

Run: `go test ./internal/format/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/format
git commit -m "feat: render query results as compact TSV"
```

---

### Task 5: Schema renderer

**Files:**
- Create: `internal/format/schema.go`
- Test: `internal/format/schema_test.go`

**Interfaces:**
- Consumes: `engine.Catalog` and friends.
- Produces: `type format.SchemaOpts struct { Pattern, Object string; MaxOverview, MaxDetail int }`; `format.Schema(c *engine.Catalog, o SchemaOpts) (out string, matched int)` — `matched` is the number of objects matched (for the overview, the total object count); when nothing matches it returns `("", 0)` and the caller writes the message. `format.Compact(n int64) string`.

- [ ] **Step 1: Write the failing test**

`internal/format/schema_test.go`:
```go
package format

import (
	"testing"

	"github.com/anderson-0/qh/internal/engine"
)

func schemaCatalog() *engine.Catalog {
	events := &engine.Object{Namespace: "analytics", Name: "events", Kind: engine.KindTable, RowEstimate: 10_000_000,
		Fields:     []engine.Field{{Name: "id", Type: "bigint"}, {Name: "user_id", Type: "bigint", Nullable: true}},
		PrimaryKey: []string{"id"},
		ForeignKeys: []engine.ForeignKey{{Namespace: "analytics", Object: "events", Columns: []string{"user_id"},
			RefNamespace: "public", RefObject: "users", RefColumns: []string{"id"}}}}
	orderItems := &engine.Object{Namespace: "public", Name: "order_items", Kind: engine.KindTable, RowEstimate: 4_800_000,
		Fields:     []engine.Field{{Name: "order_id", Type: "bigint"}, {Name: "sku", Type: "text"}},
		PrimaryKey: []string{"order_id", "sku"},
		ForeignKeys: []engine.ForeignKey{{Namespace: "public", Object: "order_items", Columns: []string{"order_id"},
			RefNamespace: "public", RefObject: "orders", RefColumns: []string{"id"}}},
		Indexes: []engine.Index{{Name: "order_items_pkey", Columns: []string{"order_id", "sku"}, Unique: true}}}
	orders := &engine.Object{Namespace: "public", Name: "orders", Kind: engine.KindTable, RowEstimate: 1_200_000, Comment: "Customer orders",
		Fields: []engine.Field{
			{Name: "id", Type: "bigint", Default: "nextval('orders_id_seq'::regclass)"},
			{Name: "user_id", Type: "bigint"},
			{Name: "status", Type: "order_status", Enum: []string{"pending", "paid", "shipped"}},
			{Name: "total", Type: "numeric(10,2)", Nullable: true},
		},
		PrimaryKey: []string{"id"},
		ForeignKeys: []engine.ForeignKey{{Namespace: "public", Object: "orders", Columns: []string{"user_id"},
			RefNamespace: "public", RefObject: "users", RefColumns: []string{"id"}}},
		Indexes: []engine.Index{{Name: "orders_user_id", Columns: []string{"user_id"}}}}
	paid := &engine.Object{Namespace: "public", Name: "paid_orders", Kind: engine.KindView, RowEstimate: -1,
		Fields: []engine.Field{{Name: "id", Type: "bigint", Nullable: true}}}
	users := &engine.Object{Namespace: "public", Name: "users", Kind: engine.KindTable, RowEstimate: 90_000, Comment: "App users",
		Fields: []engine.Field{
			{Name: "id", Type: "bigint"},
			{Name: "email", Type: "text", Comment: "Login email"},
			{Name: "created_at", Type: "timestamptz", Nullable: true, Default: "now()"},
		},
		PrimaryKey: []string{"id"},
		Indexes: []engine.Index{
			{Name: "users_pkey", Columns: []string{"id"}, Unique: true},
			{Name: "users_email_key", Columns: []string{"email"}, Unique: true},
		}}
	c := &engine.Catalog{Namespaces: []engine.Namespace{
		{Name: "analytics", Objects: []*engine.Object{events}},
		{Name: "public", Objects: []*engine.Object{orderItems, orders, paid, users}},
	}}
	c.Link()
	return c
}

func TestOverview(t *testing.T) {
	got, n := Schema(schemaCatalog(), SchemaOpts{})
	want := "analytics: 1 table\n" +
		"  events ~10M\n" +
		"public: 3 tables, 1 view\n" +
		"  order_items ~4.8M · orders ~1.2M · paid_orders (view) · users ~90k\n"
	if got != want || n != 5 {
		t.Fatalf("n=%d got:\n%s\nwant:\n%s", n, got, want)
	}
}

func TestOverviewCap(t *testing.T) {
	got, _ := Schema(schemaCatalog(), SchemaOpts{MaxOverview: 3})
	want := "analytics: 1 table\n" +
		"  events ~10M\n" +
		"public: 3 tables, 1 view\n" +
		"  order_items ~4.8M · orders ~1.2M · …\n" +
		"-- showing 3 of 5 objects; use pattern= to narrow\n"
	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestEmptyCatalog(t *testing.T) {
	got, n := Schema(&engine.Catalog{}, SchemaOpts{})
	if got != "-- no tables or views found\n" || n != 0 {
		t.Fatalf("got %q n=%d", got, n)
	}
}

func TestDetail(t *testing.T) {
	got, n := Schema(schemaCatalog(), SchemaOpts{Object: "ORDERS"})
	want := "public.orders ~1.2M rows: Customer orders\n" +
		"  id bigint PK =auto\n" +
		"  user_id bigint NN → users.id\n" +
		"  status order_status NN [pending|paid|shipped]\n" +
		"  total numeric(10,2)\n" +
		"  ← order_items.order_id\n" +
		"  idx (user_id)\n"
	if got != want || n != 1 {
		t.Fatalf("n=%d got:\n%s\nwant:\n%s", n, got, want)
	}
}

func TestDetailIncomingAcrossNamespaces(t *testing.T) {
	got, _ := Schema(schemaCatalog(), SchemaOpts{Object: "public.users"})
	want := "public.users ~90k rows: App users\n" +
		"  id bigint PK\n" +
		"  email text NN  # Login email\n" +
		"  created_at timestamptz =now()\n" +
		"  ← analytics.events.user_id, orders.user_id\n" +
		"  idx unique(email)\n"
	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestDetailCompositeKeyAndView(t *testing.T) {
	got, n := Schema(schemaCatalog(), SchemaOpts{Pattern: "order_items"})
	want := "public.order_items ~4.8M rows\n" +
		"  order_id bigint PK → orders.id\n" +
		"  sku text PK\n"
	if got != want || n != 1 {
		t.Fatalf("n=%d got:\n%s", n, got)
	}
	got, _ = Schema(schemaCatalog(), SchemaOpts{Object: "paid_orders"})
	if got != "public.paid_orders (view)\n  id bigint\n" {
		t.Fatalf("view got %q", got)
	}
	got, _ = Schema(schemaCatalog(), SchemaOpts{Object: "events"})
	if want := "analytics.events ~10M rows\n  id bigint PK\n  user_id bigint → public.users.id\n"; got != want {
		t.Fatalf("cross-namespace FK got %q", got)
	}
}

func TestPatternMatching(t *testing.T) {
	cases := []struct {
		pattern string
		want    int
	}{
		{"order", 3},   // substring: order_items, orders, paid_orders
		{"order*", 2},  // prefix
		{"*ORDERS", 2}, // suffix, case-insensitive: orders, paid_orders
		{"public.u*", 1},
		{"zzz", 0},
	}
	for _, c := range cases {
		if _, n := Schema(schemaCatalog(), SchemaOpts{Pattern: c.pattern}); n != c.want {
			t.Errorf("pattern %q matched %d, want %d", c.pattern, n, c.want)
		}
	}
}

func TestTooManyMatchesListsNames(t *testing.T) {
	got, n := Schema(schemaCatalog(), SchemaOpts{Pattern: "order*", MaxDetail: 1})
	want := "-- 2 objects match; showing names only. Narrow the pattern or use object=\n" +
		"public.order_items, public.orders\n"
	if got != want || n != 2 {
		t.Fatalf("n=%d got %q", n, got)
	}
}

func TestCompact(t *testing.T) {
	cases := map[int64]string{0: "0", 999: "999", 1000: "1k", 1200: "1.2k", 90_000: "90k",
		1_200_000: "1.2M", 10_000_000: "10M", 48_000_000: "48M", 3_400_000_000: "3.4B"}
	for n, want := range cases {
		if got := Compact(n); got != want {
			t.Errorf("Compact(%d) = %q, want %q", n, got, want)
		}
	}
}
```

- [ ] **Step 2: Run it to verify it fails**

Run: `go test ./internal/format/ -run 'Overview|Detail|Pattern|TooMany|Compact|Empty'`
Expected: FAIL — `undefined: Schema`.

- [ ] **Step 3: Implement**

`internal/format/schema.go`:
```go
package format

import (
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/anderson-0/qh/internal/engine"
)

// SchemaOpts selects what Schema renders.
type SchemaOpts struct {
	Pattern     string // glob with *, or a substring when it has no *
	Object      string // exact "name" or "namespace.name", ignoring case
	MaxOverview int    // default 200
	MaxDetail   int    // default 30
}

// Schema renders an overview (no Pattern/Object) or object details.
// matched is the number of matching objects (the total for the overview).
// When nothing matches it returns ("", 0).
func Schema(c *engine.Catalog, o SchemaOpts) (string, int) {
	if o.MaxOverview <= 0 {
		o.MaxOverview = 200
	}
	if o.MaxDetail <= 0 {
		o.MaxDetail = 30
	}
	if strings.TrimSpace(o.Pattern) == "" && strings.TrimSpace(o.Object) == "" {
		return overview(c, o.MaxOverview)
	}
	objs := match(c, o)
	switch {
	case len(objs) == 0:
		return "", 0
	case len(objs) > o.MaxDetail:
		return namesOnly(objs), len(objs)
	}
	var b strings.Builder
	for i, obj := range objs {
		if i > 0 {
			b.WriteByte('\n')
		}
		writeObject(&b, obj)
	}
	return b.String(), len(objs)
}

func overview(c *engine.Catalog, max int) (string, int) {
	total := c.Count()
	if total == 0 {
		return "-- no tables or views found\n", 0
	}
	var b strings.Builder
	shown := 0
	for _, ns := range c.Namespaces {
		if len(ns.Objects) == 0 {
			continue
		}
		fmt.Fprintf(&b, "%s: %s\n", ns.Name, kindCounts(ns.Objects))
		parts := make([]string, 0, len(ns.Objects))
		for _, obj := range ns.Objects {
			if shown == max {
				parts = append(parts, "…")
				break
			}
			parts = append(parts, overviewEntry(obj))
			shown++
		}
		b.WriteString("  " + strings.Join(parts, " · ") + "\n")
	}
	if shown < total {
		fmt.Fprintf(&b, "-- showing %d of %d objects; use pattern= to narrow\n", shown, total)
	}
	return b.String(), total
}

var kindLabels = []struct {
	kind      engine.Kind
	one, many string
}{
	{engine.KindTable, "table", "tables"},
	{engine.KindView, "view", "views"},
	{engine.KindMatView, "materialized view", "materialized views"},
	{engine.KindCollection, "collection", "collections"},
}

func kindCounts(objs []*engine.Object) string {
	counts := map[engine.Kind]int{}
	for _, o := range objs {
		counts[o.Kind]++
	}
	var parts []string
	for _, k := range kindLabels {
		n := counts[k.kind]
		if n == 0 {
			continue
		}
		label := k.many
		if n == 1 {
			label = k.one
		}
		parts = append(parts, fmt.Sprintf("%d %s", n, label))
	}
	return strings.Join(parts, ", ")
}

func overviewEntry(o *engine.Object) string {
	s := o.Name
	switch o.Kind {
	case engine.KindView:
		return s + " (view)"
	case engine.KindMatView:
		s += " (mview)"
	}
	if o.RowEstimate >= 0 {
		s += " ~" + Compact(o.RowEstimate)
	}
	return s
}

// Compact formats a row count as 999, 1.2k, 90k, 1.2M, 48M, 3.4B.
func Compact(n int64) string {
	switch {
	case n < 1000:
		return strconv.FormatInt(n, 10)
	case n < 1_000_000:
		return scaled(n, 1000, "k")
	case n < 1_000_000_000:
		return scaled(n, 1_000_000, "M")
	}
	return scaled(n, 1_000_000_000, "B")
}

func scaled(n, unit int64, suffix string) string {
	v := float64(n) / float64(unit)
	if v < 10 {
		return strings.TrimSuffix(strconv.FormatFloat(v, 'f', 1, 64), ".0") + suffix
	}
	return strconv.FormatFloat(math.Floor(v), 'f', 0, 64) + suffix
}

func match(c *engine.Catalog, o SchemaOpts) []*engine.Object {
	if obj := strings.TrimSpace(o.Object); obj != "" {
		return c.Lookup(obj)
	}
	re := globRegexp(o.Pattern)
	var out []*engine.Object
	for _, ns := range c.Namespaces {
		for _, x := range ns.Objects {
			if re.MatchString(x.Name) || re.MatchString(x.Namespace+"."+x.Name) {
				out = append(out, x)
			}
		}
	}
	return out
}

func globRegexp(p string) *regexp.Regexp {
	p = strings.TrimSpace(p)
	if !strings.Contains(p, "*") {
		p = "*" + p + "*"
	}
	parts := strings.Split(p, "*")
	for i := range parts {
		parts[i] = regexp.QuoteMeta(parts[i])
	}
	return regexp.MustCompile("(?i)^" + strings.Join(parts, ".*") + "$")
}

func namesOnly(objs []*engine.Object) string {
	names := make([]string, 0, len(objs))
	for i, o := range objs {
		if i == 200 {
			names = append(names, "…")
			break
		}
		names = append(names, o.Namespace+"."+o.Name)
	}
	return fmt.Sprintf("-- %d objects match; showing names only. Narrow the pattern or use object=\n%s\n",
		len(objs), strings.Join(names, ", "))
}

func writeObject(b *strings.Builder, o *engine.Object) {
	b.WriteString(o.Namespace + "." + o.Name)
	switch o.Kind {
	case engine.KindView:
		b.WriteString(" (view)")
	case engine.KindMatView:
		b.WriteString(" (materialized view)")
	}
	if o.RowEstimate >= 0 && o.Kind != engine.KindView {
		b.WriteString(" ~" + Compact(o.RowEstimate) + " rows")
	}
	if o.Comment != "" {
		b.WriteString(": " + clip(o.Comment, 120))
	}
	b.WriteByte('\n')

	pk := map[string]bool{}
	for _, c := range o.PrimaryKey {
		pk[c] = true
	}
	single := map[string]engine.ForeignKey{}
	var multi []engine.ForeignKey
	for _, fk := range o.ForeignKeys {
		if len(fk.Columns) == 1 && len(fk.RefColumns) == 1 {
			single[fk.Columns[0]] = fk
		} else {
			multi = append(multi, fk)
		}
	}
	for _, f := range o.Fields {
		b.WriteString("  " + f.Name)
		if f.Type != "" {
			b.WriteString(" " + f.Type)
		}
		if pk[f.Name] {
			b.WriteString(" PK")
		} else if !f.Nullable {
			b.WriteString(" NN")
		}
		if fk, ok := single[f.Name]; ok {
			b.WriteString(" → " + target(o.Namespace, fk.RefNamespace, fk.RefObject) + "." + fk.RefColumns[0])
		}
		if len(f.Enum) > 0 {
			b.WriteString(" [" + enumList(f.Enum) + "]")
		}
		if f.Default != "" {
			b.WriteString(" =" + defaultExpr(f.Default))
		}
		if f.Comment != "" {
			b.WriteString("  # " + clip(f.Comment, 120))
		}
		b.WriteByte('\n')
	}
	for _, fk := range multi {
		fmt.Fprintf(b, "  (%s) → %s(%s)\n", strings.Join(fk.Columns, ","),
			target(o.Namespace, fk.RefNamespace, fk.RefObject), strings.Join(fk.RefColumns, ","))
	}
	if len(o.ReferencedBy) > 0 {
		refs := make([]string, 0, len(o.ReferencedBy))
		for i, fk := range o.ReferencedBy {
			if i == 15 {
				refs = append(refs, fmt.Sprintf("…(+%d)", len(o.ReferencedBy)-15))
				break
			}
			name := target(o.Namespace, fk.Namespace, fk.Object)
			if len(fk.Columns) == 1 {
				name += "." + fk.Columns[0]
			} else {
				name += "(" + strings.Join(fk.Columns, ",") + ")"
			}
			refs = append(refs, name)
		}
		b.WriteString("  ← " + strings.Join(refs, ", ") + "\n")
	}
	var idx []string
	for _, ix := range o.Indexes {
		if sameColumns(ix.Columns, o.PrimaryKey) {
			continue
		}
		s := "(" + strings.Join(ix.Columns, ",") + ")"
		if ix.Unique {
			s = "unique" + s
		}
		idx = append(idx, s)
	}
	if len(idx) > 0 {
		b.WriteString("  idx " + strings.Join(idx, " ") + "\n")
	}
}

func target(from, ns, name string) string {
	if ns == "" || ns == from {
		return name
	}
	return ns + "." + name
}

func enumList(v []string) string {
	if len(v) > 20 {
		return strings.Join(v[:20], "|") + "|…"
	}
	return strings.Join(v, "|")
}

func defaultExpr(d string) string {
	if strings.HasPrefix(d, "nextval(") {
		return "auto"
	}
	return clip(d, 40)
}

func clip(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n]) + "…"
}

func sameColumns(a, b []string) bool {
	if len(a) != len(b) || len(a) == 0 {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
```

- [ ] **Step 4: Run it to verify it passes**

Run: `go test ./internal/format/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/format
git commit -m "feat: render schemas as compact definitions with keys and relations"
```

---

### Task 6: Did-you-mean suggestions

**Files:**
- Create: `internal/suggest/suggest.go`
- Test: `internal/suggest/suggest_test.go`

**Interfaces:**
- Consumes: `engine.Catalog`, `(*Catalog).Lookup`.
- Produces: `suggest.Distance(a, b string) int`; `type suggest.Candidate struct { Key, Label string }`; `suggest.Closest(target string, cands []Candidate, n int) []string`; `suggest.ColumnHint(c *engine.Catalog, column string, tables []string) string`; `suggest.TableHint(c *engine.Catalog, table string) string`.

- [ ] **Step 1: Write the failing test**

`internal/suggest/suggest_test.go`:
```go
package suggest

import (
	"reflect"
	"strings"
	"testing"

	"github.com/anderson-0/qh/internal/engine"
)

func TestDistance(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"kitten", "sitting", 3}, {"Email", "email", 0}, {"", "abc", 3}, {"zoë", "zoe", 1},
	}
	for _, c := range cases {
		if got := Distance(c.a, c.b); got != c.want {
			t.Errorf("Distance(%q,%q) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}

func cands(names ...string) []Candidate {
	out := make([]Candidate, len(names))
	for i, n := range names {
		out[i] = Candidate{Key: n, Label: n}
	}
	return out
}

func TestClosest(t *testing.T) {
	if got := Closest("emial", cands("email", "name", "id"), 3); !reflect.DeepEqual(got, []string{"email"}) {
		t.Fatalf("got %v", got)
	}
	if got := Closest("user", cands("orders", "user_id", "users"), 3); !reflect.DeepEqual(got, []string{"users", "user_id"}) {
		t.Fatalf("got %v", got)
	}
	if got := Closest("qqqq", cands("orders"), 3); len(got) != 0 {
		t.Fatalf("got %v", got)
	}
}

func hintCatalog() *engine.Catalog {
	return &engine.Catalog{Namespaces: []engine.Namespace{
		{Name: "audit", Objects: []*engine.Object{{Namespace: "audit", Name: "AuditLog",
			Fields: []engine.Field{{Name: "id"}, {Name: "EventName"}}}}},
		{Name: "public", Objects: []*engine.Object{
			{Namespace: "public", Name: "orders", Fields: []engine.Field{{Name: "id"}, {Name: "user_id"}, {Name: "status"}}},
			{Namespace: "public", Name: "users", Fields: []engine.Field{{Name: "id"}, {Name: "email"}, {Name: "name"}}},
		}},
	}}
}

func TestColumnHintUsesReferencedTables(t *testing.T) {
	got := ColumnHint(hintCatalog(), "u.emial", []string{"users", "orders"})
	if !strings.HasPrefix(got, "Did you mean users.email? ") {
		t.Fatalf("got %q", got)
	}
	if !strings.Contains(got, "users has: email") || !strings.Contains(got, "orders has: ") {
		t.Fatalf("got %q", got)
	}
}

func TestColumnHintCaseInsensitive(t *testing.T) {
	got := ColumnHint(hintCatalog(), "eventname", []string{"auditlog"})
	if !strings.HasPrefix(got, "Did you mean AuditLog.EventName? ") {
		t.Fatalf("got %q", got)
	}
}

func TestColumnHintWithoutKnownTables(t *testing.T) {
	if got := ColumnHint(hintCatalog(), "emial", nil); got != "Did you mean users.email?" {
		t.Fatalf("got %q", got)
	}
	if got := ColumnHint(hintCatalog(), "zzzzzz", []string{"nope"}); got != "" {
		t.Fatalf("got %q", got)
	}
}

func TestColumnHintNoCloseMatchStillListsColumns(t *testing.T) {
	got := ColumnHint(hintCatalog(), "zzzzzz", []string{"users"})
	if strings.Contains(got, "Did you mean") || !strings.HasPrefix(got, "users has: ") {
		t.Fatalf("got %q", got)
	}
}

func TestTableHint(t *testing.T) {
	if got := TableHint(hintCatalog(), "user"); got != "Did you mean public.users?" {
		t.Fatalf("got %q", got)
	}
	if got := TableHint(hintCatalog(), "auditlogs"); got != "Did you mean audit.AuditLog?" {
		t.Fatalf("got %q", got)
	}
	if got := TableHint(hintCatalog(), "qqq"); got != "Use the schema tool to list tables" {
		t.Fatalf("got %q", got)
	}
}
```

- [ ] **Step 2: Run it to verify it fails**

Run: `go test ./internal/suggest/`
Expected: FAIL — `undefined: Distance`.

- [ ] **Step 3: Implement**

`internal/suggest/suggest.go`:
```go
// Package suggest finds names close to an unknown identifier.
package suggest

import (
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/anderson-0/qh/internal/engine"
)

// Distance is the case-insensitive Levenshtein distance between a and b.
func Distance(a, b string) int {
	ra := []rune(strings.ToLower(a))
	rb := []rune(strings.ToLower(b))
	prev := make([]int, len(rb)+1)
	cur := make([]int, len(rb)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ra); i++ {
		cur[0] = i
		for j := 1; j <= len(rb); j++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev, cur = cur, prev
	}
	return prev[len(rb)]
}

// Candidate is a name to compare (Key) and the text to show for it (Label).
type Candidate struct {
	Key   string
	Label string
}

type scored struct {
	key, label string
	d          int
}

func rank(target string, cands []Candidate) []scored {
	out := make([]scored, len(cands))
	for i, c := range cands {
		out[i] = scored{c.Key, c.Label, Distance(target, c.Key)}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].d != out[j].d {
			return out[i].d < out[j].d
		}
		return out[i].label < out[j].label
	})
	return out
}

// Closest returns up to n labels whose keys are near target, best first. A key
// qualifies when its edit distance is at most max(2, len(target)/3), or when
// one name contains the other (both at least 3 characters).
func Closest(target string, cands []Candidate, n int) []string {
	t := strings.ToLower(target)
	tl := utf8.RuneCountInString(t)
	limit := max(2, tl/3)
	var out []string
	seen := map[string]bool{}
	for _, s := range rank(target, cands) {
		k := strings.ToLower(s.key)
		kl := utf8.RuneCountInString(k)
		near := s.d <= limit || tl >= 3 && kl >= 3 && (strings.Contains(k, t) || strings.Contains(t, k))
		if near && !seen[s.label] {
			seen[s.label] = true
			out = append(out, s.label)
			if len(out) == n {
				break
			}
		}
	}
	return out
}

// ColumnHint explains an unknown column. It names the closest column in the
// tables the query references and lists their columns, nearest first. With
// no known tables it searches the whole catalog.
func ColumnHint(c *engine.Catalog, column string, tables []string) string {
	col := column
	if i := strings.LastIndex(col, "."); i >= 0 {
		col = col[i+1:]
	}
	var objs []*engine.Object
	seen := map[*engine.Object]bool{}
	for _, t := range tables {
		for _, o := range c.Lookup(t) {
			if !seen[o] && len(objs) < 4 {
				seen[o] = true
				objs = append(objs, o)
			}
		}
	}
	if len(objs) == 0 {
		var all []Candidate
		for _, ns := range c.Namespaces {
			for _, o := range ns.Objects {
				for _, f := range o.Fields {
					all = append(all, Candidate{Key: f.Name, Label: o.Name + "." + f.Name})
				}
			}
		}
		if top := Closest(col, all, 5); len(top) > 0 {
			return "Did you mean " + strings.Join(top, ", ") + "?"
		}
		return ""
	}
	var parts, best []string
	for _, o := range objs {
		fields := make([]Candidate, len(o.Fields))
		for i, f := range o.Fields {
			fields[i] = Candidate{Key: f.Name, Label: f.Name}
		}
		if top := Closest(col, fields, 1); len(top) > 0 {
			best = append(best, o.Name+"."+top[0])
		}
		ranked := rank(col, fields)
		names := make([]string, 0, len(ranked))
		for _, r := range ranked {
			names = append(names, r.label)
		}
		more := ""
		if len(names) > 8 {
			names, more = names[:8], ", …"
		}
		parts = append(parts, o.Name+" has: "+strings.Join(names, ", ")+more)
	}
	hint := strings.Join(parts, "; ")
	if len(best) > 0 {
		hint = "Did you mean " + strings.Join(best, ", ") + "? " + hint
	}
	return hint
}

// TableHint explains an unknown table by naming the closest ones.
func TableHint(c *engine.Catalog, table string) string {
	name := table
	if i := strings.LastIndex(name, "."); i >= 0 {
		name = name[i+1:]
	}
	qualify := len(c.Namespaces) > 1
	var all []Candidate
	for _, ns := range c.Namespaces {
		for _, o := range ns.Objects {
			label := o.Name
			if qualify {
				label = o.Namespace + "." + o.Name
			}
			all = append(all, Candidate{Key: o.Name, Label: label})
		}
	}
	if top := Closest(name, all, 5); len(top) > 0 {
		return "Did you mean " + strings.Join(top, ", ") + "?"
	}
	return "Use the schema tool to list tables"
}
```

- [ ] **Step 4: Run it to verify it passes**

Run: `go test ./internal/suggest/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/suggest
git commit -m "feat: suggest close table and column names"
```

---

### Task 7: Credential encryption

**Files:**
- Create: `internal/secret/secret.go`
- Test: `internal/secret/secret_test.go`

**Interfaces:**
- Produces: `secret.KeySize = 32`; `secret.KeyID(key []byte) uint32`; `secret.NewKeyring(primary []byte, old ...[]byte) (*Keyring, error)`; `(*Keyring).PrimaryID() uint32`; `(*Keyring).Seal(plaintext, aad []byte) ([]byte, error)`; `(*Keyring).Open(blob, aad []byte) ([]byte, error)`; `secret.ParseKey(b64 string) ([]byte, error)`; `secret.LoadOrCreateKeyFile(path string) ([]byte, error)`; errors `ErrUnknownKey`, `ErrCorrupt`. Sealed format: `key_id (4 bytes, big-endian) || nonce (12) || ciphertext+tag`.

- [ ] **Step 1: Write the failing test**

`internal/secret/secret_test.go`:
```go
package secret

import (
	"bytes"
	"crypto/rand"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func newKey(t *testing.T) []byte {
	t.Helper()
	k := make([]byte, KeySize)
	if _, err := rand.Read(k); err != nil {
		t.Fatal(err)
	}
	return k
}

func TestSealOpen(t *testing.T) {
	k, err := NewKeyring(newKey(t))
	if err != nil {
		t.Fatal(err)
	}
	blob, err := k.Seal([]byte("postgres://u:pw@h/db"), []byte("conn-1"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(blob, []byte("pw@h")) {
		t.Fatal("ciphertext contains plaintext")
	}
	got, err := k.Open(blob, []byte("conn-1"))
	if err != nil || string(got) != "postgres://u:pw@h/db" {
		t.Fatalf("Open = %q, %v", got, err)
	}
	if _, err := k.Open(blob, []byte("conn-2")); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("wrong aad err = %v", err)
	}
	blob[len(blob)-1] ^= 1
	if _, err := k.Open(blob, []byte("conn-1")); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("tampered err = %v", err)
	}
	if _, err := k.Open([]byte{1, 2}, nil); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("short blob err = %v", err)
	}
}

func TestRotation(t *testing.T) {
	oldKey, newKeyBytes := newKey(t), newKey(t)
	old, _ := NewKeyring(oldKey)
	blob, _ := old.Seal([]byte("secret"), []byte("id"))
	both, err := NewKeyring(newKeyBytes, oldKey)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := both.Open(blob, []byte("id")); err != nil || string(got) != "secret" {
		t.Fatalf("old blob with rotated keyring = %q, %v", got, err)
	}
	resealed, _ := both.Seal([]byte("secret"), []byte("id"))
	onlyNew, _ := NewKeyring(newKeyBytes)
	if _, err := onlyNew.Open(resealed, []byte("id")); err != nil {
		t.Fatalf("resealed blob: %v", err)
	}
	if _, err := onlyNew.Open(blob, []byte("id")); !errors.Is(err, ErrUnknownKey) {
		t.Fatalf("old blob without old key err = %v", err)
	}
	if both.PrimaryID() != KeyID(newKeyBytes) {
		t.Fatal("primary should be the first key")
	}
}

func TestBadKeyLength(t *testing.T) {
	if _, err := NewKeyring(make([]byte, 16)); err == nil {
		t.Fatal("16-byte key accepted")
	}
	if _, err := ParseKey("dG9vLXNob3J0"); err == nil {
		t.Fatal("short base64 key accepted")
	}
}

func TestKeyFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "master.key")
	k1, err := LoadOrCreateKeyFile(path)
	if err != nil || len(k1) != KeySize {
		t.Fatalf("create = %d bytes, %v", len(k1), err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v, %v", info.Mode().Perm(), err)
	}
	k2, err := LoadOrCreateKeyFile(path)
	if err != nil || !bytes.Equal(k1, k2) {
		t.Fatal("reload returned a different key")
	}
}
```

- [ ] **Step 2: Run it to verify it fails**

Run: `go test ./internal/secret/`
Expected: FAIL — `undefined: KeySize`.

- [ ] **Step 3: Implement**

`internal/secret/secret.go`:
```go
// Package secret seals connection strings with AES-256-GCM.
package secret

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"
)

// KeySize is the master key length in bytes.
const KeySize = 32

var (
	ErrUnknownKey = errors.New("secret: sealed with an unknown master key")
	ErrCorrupt    = errors.New("secret: cannot decrypt (wrong key, wrong record, or corrupted data)")
)

// KeyID identifies a master key: the first 4 bytes of its SHA-256.
func KeyID(key []byte) uint32 {
	h := sha256.Sum256(key)
	return binary.BigEndian.Uint32(h[:4])
}

// Keyring seals with its primary key and opens with any of its keys.
type Keyring struct {
	primary uint32
	keys    map[uint32]cipher.AEAD
}

// NewKeyring builds a keyring whose primary key is the first argument.
func NewKeyring(primary []byte, old ...[]byte) (*Keyring, error) {
	k := &Keyring{keys: map[uint32]cipher.AEAD{}}
	for i, key := range append([][]byte{primary}, old...) {
		if len(key) != KeySize {
			return nil, fmt.Errorf("secret: master key %d is %d bytes, want %d", i, len(key), KeySize)
		}
		block, err := aes.NewCipher(key)
		if err != nil {
			return nil, err
		}
		aead, err := cipher.NewGCM(block)
		if err != nil {
			return nil, err
		}
		id := KeyID(key)
		k.keys[id] = aead
		if i == 0 {
			k.primary = id
		}
	}
	return k, nil
}

// PrimaryID returns the id of the key used by Seal.
func (k *Keyring) PrimaryID() uint32 { return k.primary }

// Seal encrypts plaintext. aad must be supplied again to Open.
func (k *Keyring) Seal(plaintext, aad []byte) ([]byte, error) {
	aead := k.keys[k.primary]
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	out := make([]byte, 4, 4+len(nonce)+len(plaintext)+aead.Overhead())
	binary.BigEndian.PutUint32(out, k.primary)
	out = append(out, nonce...)
	return aead.Seal(out, nonce, plaintext, aad), nil
}

// Open decrypts a blob produced by Seal.
func (k *Keyring) Open(blob, aad []byte) ([]byte, error) {
	if len(blob) < 4 {
		return nil, ErrCorrupt
	}
	aead, ok := k.keys[binary.BigEndian.Uint32(blob)]
	if !ok {
		return nil, ErrUnknownKey
	}
	ns := aead.NonceSize()
	if len(blob) < 4+ns {
		return nil, ErrCorrupt
	}
	pt, err := aead.Open(nil, blob[4:4+ns], blob[4+ns:], aad)
	if err != nil {
		return nil, ErrCorrupt
	}
	return pt, nil
}

// ParseKey decodes a base64 master key.
func ParseKey(b64 string) ([]byte, error) {
	key, err := base64.StdEncoding.DecodeString(strings.TrimSpace(b64))
	if err != nil {
		return nil, fmt.Errorf("secret: master key is not valid base64: %w", err)
	}
	if len(key) != KeySize {
		return nil, fmt.Errorf("secret: master key is %d bytes, want %d", len(key), KeySize)
	}
	return key, nil
}

// LoadOrCreateKeyFile reads a base64 master key from path, creating it with
// mode 0600 when it does not exist.
func LoadOrCreateKeyFile(path string) ([]byte, error) {
	b, err := os.ReadFile(path)
	if err == nil {
		return ParseKey(string(b))
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	key := make([]byte, KeySize)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, fs.ErrExist) {
		return LoadOrCreateKeyFile(path) // another process created it first
	}
	if err != nil {
		return nil, err
	}
	if _, err := f.WriteString(base64.StdEncoding.EncodeToString(key) + "\n"); err != nil {
		f.Close()
		return nil, err
	}
	return key, f.Close()
}
```

- [ ] **Step 4: Run it to verify it passes**

Run: `go test ./internal/secret/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/secret
git commit -m "feat: seal connection strings with AES-256-GCM"
```

---

### Task 8: Local store

**Files:**
- Create: `internal/store/store.go`, `internal/store/sqlite.go`
- Test: `internal/store/sqlite_test.go`

**Interfaces:**
- Produces:
  - `store.DefaultWorkspace = "default"`; errors `ErrNotFound`, `ErrExists`, `ErrConflict`.
  - `type store.Connection struct { ID, WorkspaceID, Name, Engine string; Secret []byte; SecretKeyID uint32; Host string; Port int; Database, Username, Description, Mode string; MaxRows, TimeoutMS, PoolSize int; AllowedNamespaces []string; Version int64; CreatedAt, UpdatedAt time.Time }`.
  - `type store.Store interface { CreateConnection(ctx, *Connection) error; UpdateConnection(ctx, *Connection) error; DeleteConnection(ctx, workspaceID, name string) error; GetConnection(ctx, workspaceID, name string) (*Connection, error); ListConnections(ctx, workspaceID string) ([]*Connection, error); Close() error }`.
  - `store.NewID() string` (32 hex chars); `store.OpenSQLite(path string) (*SQLite, error)`.
  - `UpdateConnection` requires `c.Version` to equal the stored version, increments it in the store and on `c`, and returns `ErrConflict` when the stored version differs.

- [ ] **Step 1: Add the driver dependency**

Run: `go get modernc.org/sqlite@v1.60.1`

- [ ] **Step 2: Write the failing test**

`internal/store/sqlite_test.go`:
```go
package store

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"testing"
)

func openTemp(t *testing.T) (*SQLite, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "with space", "qh.db")
	s, err := OpenSQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s, path
}

func sample(name string) *Connection {
	return &Connection{
		ID: NewID(), WorkspaceID: DefaultWorkspace, Name: name, Engine: "postgres",
		Secret: []byte{1, 2, 3}, SecretKeyID: 7, Host: "db.local", Port: 5432,
		Database: "shop", Username: "reader", Description: "Shop DB", Mode: "ro",
		MaxRows: 100, TimeoutMS: 30000, PoolSize: 5, AllowedNamespaces: []string{"public"},
	}
}

func TestCreateGetList(t *testing.T) {
	ctx := context.Background()
	s, _ := openTemp(t)
	c := sample("shop")
	if err := s.CreateConnection(ctx, c); err != nil {
		t.Fatal(err)
	}
	if c.Version != 1 || c.CreatedAt.IsZero() {
		t.Fatalf("after create: version %d created %v", c.Version, c.CreatedAt)
	}
	got, err := s.GetConnection(ctx, DefaultWorkspace, "shop")
	if err != nil {
		t.Fatal(err)
	}
	got.CreatedAt, got.UpdatedAt = c.CreatedAt, c.UpdatedAt
	if !reflect.DeepEqual(got, c) {
		t.Fatalf("got %#v\nwant %#v", got, c)
	}
	if err := s.CreateConnection(ctx, sample("analytics")); err != nil {
		t.Fatal(err)
	}
	list, err := s.ListConnections(ctx, DefaultWorkspace)
	if err != nil || len(list) != 2 || list[0].Name != "analytics" || list[1].Name != "shop" {
		t.Fatalf("list = %v, %v", list, err)
	}
}

func TestDuplicateAndMissing(t *testing.T) {
	ctx := context.Background()
	s, _ := openTemp(t)
	_ = s.CreateConnection(ctx, sample("shop"))
	if err := s.CreateConnection(ctx, sample("shop")); !errors.Is(err, ErrExists) {
		t.Fatalf("duplicate err = %v", err)
	}
	if _, err := s.GetConnection(ctx, DefaultWorkspace, "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("get missing err = %v", err)
	}
	if err := s.DeleteConnection(ctx, DefaultWorkspace, "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("delete missing err = %v", err)
	}
	if _, err := s.GetConnection(ctx, "other-workspace", "shop"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("other workspace err = %v", err)
	}
}

func TestUpdateVersioning(t *testing.T) {
	ctx := context.Background()
	s, _ := openTemp(t)
	c := sample("shop")
	_ = s.CreateConnection(ctx, c)
	stale := *c
	c.MaxRows = 500
	if err := s.UpdateConnection(ctx, c); err != nil || c.Version != 2 {
		t.Fatalf("update err %v version %d", err, c.Version)
	}
	got, _ := s.GetConnection(ctx, DefaultWorkspace, "shop")
	if got.MaxRows != 500 || got.Version != 2 {
		t.Fatalf("stored %d v%d", got.MaxRows, got.Version)
	}
	if err := s.UpdateConnection(ctx, &stale); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale update err = %v", err)
	}
	missing := sample("nope")
	missing.Version = 1
	if err := s.UpdateConnection(ctx, missing); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing update err = %v", err)
	}
}

func TestDeleteAndReopen(t *testing.T) {
	ctx := context.Background()
	s, path := openTemp(t)
	_ = s.CreateConnection(ctx, sample("keep"))
	_ = s.CreateConnection(ctx, sample("drop"))
	if err := s.DeleteConnection(ctx, DefaultWorkspace, "drop"); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s2, err := OpenSQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	list, _ := s2.ListConnections(ctx, DefaultWorkspace)
	if len(list) != 1 || list[0].Name != "keep" {
		t.Fatalf("after reopen: %v", list)
	}
}
```

- [ ] **Step 3: Run it to verify it fails**

Run: `go test ./internal/store/`
Expected: FAIL — `undefined: OpenSQLite`.

- [ ] **Step 4: Implement**

`internal/store/store.go`:
```go
// Package store persists workspaces and connections.
package store

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"time"
)

// DefaultWorkspace is the workspace local mode uses.
const DefaultWorkspace = "default"

var (
	ErrNotFound = errors.New("store: not found")
	ErrExists   = errors.New("store: already exists")
	ErrConflict = errors.New("store: changed concurrently")
)

// Connection is a stored database connection. Secret holds the sealed
// connection string; the plaintext is never stored.
type Connection struct {
	ID                string
	WorkspaceID       string
	Name              string
	Engine            string
	Secret            []byte
	SecretKeyID       uint32
	Host              string
	Port              int
	Database          string
	Username          string
	Description       string
	Mode              string // "ro" or "rw"
	MaxRows           int
	TimeoutMS         int
	PoolSize          int
	AllowedNamespaces []string
	Version           int64
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

// Store is the persistence interface. Plan 3 adds workspaces, keys and the query log.
type Store interface {
	CreateConnection(ctx context.Context, c *Connection) error
	UpdateConnection(ctx context.Context, c *Connection) error
	DeleteConnection(ctx context.Context, workspaceID, name string) error
	GetConnection(ctx context.Context, workspaceID, name string) (*Connection, error)
	ListConnections(ctx context.Context, workspaceID string) ([]*Connection, error)
	Close() error
}

// NewID returns a random 128-bit id as 32 hex characters.
func NewID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err) // crypto/rand does not fail on supported platforms
	}
	return hex.EncodeToString(b[:])
}
```

`internal/store/sqlite.go`:
```go
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
}

// SQLite is a Store backed by a SQLite file.
type SQLite struct {
	db  *sql.DB
	now func() time.Time
}

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

func (s *SQLite) migrate(ctx context.Context) error {
	var v int
	if err := s.db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&v); err != nil {
		return err
	}
	for i := v; i < len(migrations); i++ {
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		for _, stmt := range migrations[i] {
			if _, err := tx.ExecContext(ctx, stmt); err != nil {
				tx.Rollback()
				return err
			}
		}
		if _, err := tx.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", i+1)); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
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
		WHERE workspace_id = ? AND name = ? AND version = ?`,
		c.Engine, c.Secret, c.SecretKeyID, c.Host, c.Port, c.Database, c.Username, c.Description,
		c.Mode, c.MaxRows, c.TimeoutMS, c.PoolSize, string(ns), now.Format(time.RFC3339Nano),
		c.WorkspaceID, c.Name, c.Version)
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
```

- [ ] **Step 5: Run it to verify it passes**

Run: `go test ./internal/store/`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add go.mod go.sum internal/store
git commit -m "feat: add local SQLite store for connections"
```

---

### Task 9: Conformance suite and the SQLite engine

**Files:**
- Create: `internal/testutil/sqlite.go`, `internal/engine/enginetest/suite.go`, `internal/engine/sqlite/sqlite.go`, `internal/engine/sqlite/introspect.go`
- Test: `internal/engine/sqlite/sqlite_test.go`; golden files `internal/engine/sqlite/testdata/sqlite_{overview,orders,users}.golden` (generated in Step 6)

**Interfaces:**
- Consumes: `engine.*`, `sqlx.Classify`, `qerr`, `format.Result`, `format.Schema`.
- Produces:
  - `testutil.SQLiteFixture(t testing.TB) string` — path of a fresh fixture database inside a directory named `with space`.
  - `type enginetest.Harness struct { Engine engine.Engine; Setup func(t *testing.T) (ro, rw engine.ConnSpec); SlowQuery string; ReadOnlyEscapes []string; GoldenPrefix string }`; `enginetest.Run(t *testing.T, h Harness)`.
  - Package `sqlite` registers engine `"sqlite"` in `init`; exports `type Engine struct{}` and `Path(dsn string) (string, error)`. A SQLite DSN is a file path, optionally prefixed `sqlite://` or `file:`; `?` and `#` are rejected so callers cannot inject driver parameters.

**Fixture contract (every engine's fixture holds exactly this data):**
- `users(id PK, email NOT NULL UNIQUE, name, bio, avatar binary, created_at)`: `(1, alice@example.com, Alice, "line1\nline2\tx", 0xDEADBEEF, 2025-03-14 10:22:31)`, `(2, zoe@example.com, "Zoë 🚀", NULL…)`, `(3, nobody@example.com, NULL…)`.
- `orders(id PK, user_id NOT NULL → users.id, status NOT NULL, total numeric(12,2), note)`, index on `user_id`: ids 1–250, `user_id = id % 3 + 1`, `status = paid` for even ids else `pending`, `total = id * 1.5`.
- `order_items(order_id NOT NULL → orders.id, sku NOT NULL, qty NOT NULL, PK(order_id, sku))`: orders 1–10 × skus `sku-1`, `sku-2` (20 rows).
- view `paid_orders = SELECT id, user_id, total FROM orders WHERE status = 'paid'`.

- [ ] **Step 1: Write the fixture helper**

`internal/testutil/sqlite.go`:
```go
// Package testutil holds fixtures shared by tests in several packages.
package testutil

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

// SQLiteFixtureStatements build the shared fixture schema and rows.
var SQLiteFixtureStatements = []string{
	`CREATE TABLE users (id INTEGER PRIMARY KEY, email TEXT NOT NULL UNIQUE, name TEXT, bio TEXT, avatar BLOB, created_at TEXT)`,
	`CREATE TABLE orders (id INTEGER PRIMARY KEY, user_id INTEGER NOT NULL REFERENCES users(id),
		status TEXT NOT NULL CHECK (status IN ('pending', 'paid', 'shipped')), total NUMERIC(12,2), note TEXT)`,
	`CREATE INDEX orders_user_id ON orders (user_id)`,
	`CREATE TABLE order_items (order_id INTEGER NOT NULL REFERENCES orders(id), sku TEXT NOT NULL,
		qty INTEGER NOT NULL, PRIMARY KEY (order_id, sku))`,
	`CREATE VIEW paid_orders AS SELECT id, user_id, total FROM orders WHERE status = 'paid'`,
	`INSERT INTO users VALUES
		(1, 'alice@example.com', 'Alice', 'line1' || char(10) || 'line2' || char(9) || 'x', x'DEADBEEF', '2025-03-14T10:22:31'),
		(2, 'zoe@example.com', 'Zoë 🚀', NULL, NULL, NULL),
		(3, 'nobody@example.com', NULL, NULL, NULL, NULL)`,
	`WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i + 1 FROM n WHERE i < 250)
		INSERT INTO orders (id, user_id, status, total)
		SELECT i, (i % 3) + 1, CASE WHEN i % 2 = 0 THEN 'paid' ELSE 'pending' END, i * 1.5 FROM n`,
	`WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i + 1 FROM n WHERE i < 10)
		INSERT INTO order_items SELECT i, 'sku-1', 1 FROM n UNION ALL SELECT i, 'sku-2', 2 FROM n`,
}

// SQLiteFixture creates a fixture database in a directory whose name contains
// a space, and returns the database path.
func SQLiteFixture(t testing.TB) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "with space")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "shop.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, s := range SQLiteFixtureStatements {
		if _, err := db.Exec(s); err != nil {
			t.Fatalf("fixture statement failed: %v\n%s", err, s)
		}
	}
	return path
}
```

- [ ] **Step 2: Write the conformance suite**

`internal/engine/enginetest/suite.go`:
```go
// Package enginetest is the conformance suite every engine must pass.
package enginetest

import (
	"context"
	"flag"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/anderson-0/qh/internal/engine"
	"github.com/anderson-0/qh/internal/format"
	"github.com/anderson-0/qh/internal/qerr"
)

var update = flag.Bool("update", false, "rewrite golden files")

// Harness adapts an engine to the suite.
type Harness struct {
	Engine engine.Engine
	// Setup returns specs for one database loaded with the engine's fixture:
	// ro is opened read-only and rw read-write.
	Setup func(t *testing.T) (ro, rw engine.ConnSpec)
	// SlowQuery runs for several seconds.
	SlowQuery string
	// ReadOnlyEscapes are engine-specific statements that must fail on the
	// read-only connection without changing data.
	ReadOnlyEscapes []string
	// GoldenPrefix names this engine's golden files in testdata/.
	GoldenPrefix string
}

var commonEscapes = []string{
	"INSERT INTO users (id, email) VALUES (99, 'x@example.com')",
	"UPDATE users SET name = 'x'",
	"DELETE FROM order_items",
	"CREATE TABLE evil (x int)",
	"DROP TABLE order_items",
	"COMMIT; DELETE FROM order_items",
}

// Run runs the suite.
func Run(t *testing.T, h Harness) {
	ctx := context.Background()
	roSpec, rwSpec := h.Setup(t)
	ro := open(t, h.Engine, roSpec)
	rw := open(t, h.Engine, rwSpec)
	opts := engine.QueryOpts{MaxRows: 100, ReadOnly: true, Timeout: 10 * time.Second}

	t.Run("select", func(t *testing.T) {
		expect(t, run(t, ro, "SELECT id, email, name FROM users ORDER BY id", opts),
			"id\temail\tname\n1\talice@example.com\tAlice\n2\tzoe@example.com\tZoë 🚀\n3\tnobody@example.com\tNULL\n-- 3 rows")
	})

	t.Run("truncation", func(t *testing.T) {
		o := opts
		o.MaxRows = 3
		expect(t, run(t, ro, "SELECT id FROM orders ORDER BY id", o),
			"id\n1\n2\n3\n-- 3 rows shown, more exist (truncated)")
		expect(t, run(t, ro, "SELECT id FROM orders WHERE id <= 3 ORDER BY id", o),
			"id\n1\n2\n3\n-- 3 rows")
		expect(t, run(t, ro, "SELECT id FROM orders ORDER BY id LIMIT 50", o),
			"id\n1\n2\n3\n-- 3 rows shown, more exist (truncated)")
	})

	t.Run("trailing_semicolon", func(t *testing.T) {
		expect(t, run(t, ro, "SELECT count(*) AS n FROM orders; -- total", opts), "n\n250\n-- 1 row")
		expect(t, run(t, ro, "SELECT count(*) AS n FROM orders -- total", opts), "n\n250\n-- 1 row")
	})

	t.Run("values", func(t *testing.T) {
		expect(t, run(t, ro, "SELECT bio, avatar, created_at FROM users WHERE id = 1", opts),
			"bio\tavatar\tcreated_at\nline1\\nline2\\tx\t<bytes 4B>\t2025-03-14T10:22:31\n-- 1 row")
	})

	t.Run("read_only", func(t *testing.T) {
		_, err := ro.Query(ctx, commonEscapes[0], opts)
		if e := qerr.From(err); err == nil || e.Category != qerr.ReadOnly {
			t.Errorf("INSERT on read-only: %v", err)
		}
		for _, q := range append(append([]string{}, commonEscapes...), h.ReadOnlyEscapes...) {
			if _, err := ro.Query(ctx, q, opts); err == nil {
				t.Errorf("%q succeeded on a read-only connection", q)
			}
		}
		expect(t, run(t, rw, "SELECT (SELECT count(*) FROM users) AS u, (SELECT count(*) FROM orders) AS o, (SELECT count(*) FROM order_items) AS i", opts),
			"u\to\ti\n3\t250\t20\n-- 1 row")
		if _, err := rw.Query(ctx, "SELECT * FROM evil", opts); err == nil {
			t.Error("table evil exists after read-only escapes")
		}
	})

	t.Run("timeout", func(t *testing.T) {
		start := time.Now()
		_, err := ro.Query(ctx, h.SlowQuery, engine.QueryOpts{MaxRows: 10, ReadOnly: true, Timeout: 300 * time.Millisecond})
		if e := qerr.From(err); err == nil || e.Category != qerr.Timeout {
			t.Errorf("slow query: %v", err)
		}
		if d := time.Since(start); d > 5*time.Second {
			t.Errorf("timeout took %v", d)
		}
	})

	t.Run("unknown_identifiers", func(t *testing.T) {
		_, err := ro.Query(ctx, "SELECT email_addr FROM users", opts)
		if e := qerr.From(err); err == nil || e.Category != qerr.QueryError || e.UnknownColumn != "email_addr" {
			t.Errorf("unknown column: %#v", e)
		}
		_, err = ro.Query(ctx, "SELECT * FROM userz", opts)
		if e := qerr.From(err); err == nil || e.Category != qerr.QueryError || e.UnknownTable != "userz" {
			t.Errorf("unknown table: %#v", e)
		}
	})

	t.Run("writes", func(t *testing.T) {
		w := engine.QueryOpts{MaxRows: 100, Timeout: 10 * time.Second}
		r, err := rw.Query(ctx, "INSERT INTO users (id, email) VALUES (99, 'new@example.com')", w)
		if err != nil || r.RowsAffected != 1 || len(r.Columns) != 0 {
			t.Fatalf("insert: %+v, %v", r, err)
		}
		r, err = rw.Query(ctx, "DELETE FROM users WHERE id = 99", w)
		if err != nil || r.RowsAffected != 1 {
			t.Fatalf("delete: %+v, %v", r, err)
		}
	})

	t.Run("explain", func(t *testing.T) {
		r, err := ro.Explain(ctx, "SELECT * FROM orders WHERE id = 1", opts)
		if err != nil || len(r.Columns) != 1 || len(r.Rows) == 0 {
			t.Fatalf("explain: %+v, %v", r, err)
		}
	})

	t.Run("ping", func(t *testing.T) {
		if v, err := ro.Ping(ctx); err != nil || v == "" {
			t.Fatalf("ping = %q, %v", v, err)
		}
	})

	t.Run("introspect", func(t *testing.T) {
		cat, err := ro.Introspect(ctx)
		if err != nil {
			t.Fatal(err)
		}
		orders, users, items, paid := one(t, cat, "orders"), one(t, cat, "users"), one(t, cat, "order_items"), one(t, cat, "paid_orders")
		if !hasFK(orders, "user_id", "users", "id") {
			t.Errorf("orders FKs: %+v", orders.ForeignKeys)
		}
		if len(users.ReferencedBy) == 0 || users.ReferencedBy[0].Object != "orders" {
			t.Errorf("users.ReferencedBy: %+v", users.ReferencedBy)
		}
		if !reflect.DeepEqual(items.PrimaryKey, []string{"order_id", "sku"}) {
			t.Errorf("order_items PK: %v", items.PrimaryKey)
		}
		if paid.Kind != engine.KindView {
			t.Errorf("paid_orders kind: %v", paid.Kind)
		}
		if !hasUniqueIndex(users, "email") {
			t.Errorf("users indexes: %+v", users.Indexes)
		}
		overview, _ := format.Schema(cat, format.SchemaOpts{})
		golden(t, h.GoldenPrefix+"_overview.golden", overview)
		ordersOut, _ := format.Schema(cat, format.SchemaOpts{Object: "orders"})
		golden(t, h.GoldenPrefix+"_orders.golden", ordersOut)
		usersOut, _ := format.Schema(cat, format.SchemaOpts{Object: "users"})
		golden(t, h.GoldenPrefix+"_users.golden", usersOut)
	})
}

func open(t *testing.T, e engine.Engine, spec engine.ConnSpec) engine.Conn {
	t.Helper()
	c, err := e.Open(context.Background(), spec)
	if err != nil {
		t.Fatalf("open %s: %v", spec.ID, err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

// run executes q and renders it without the timing part of the footer.
func run(t *testing.T, c engine.Conn, q string, o engine.QueryOpts) string {
	t.Helper()
	r, err := c.Query(context.Background(), q, o)
	if err != nil {
		t.Fatalf("query %q: %v", q, err)
	}
	s := strings.TrimSuffix(format.Result(r), "\n")
	if i := strings.LastIndex(s, " · "); i >= 0 {
		s = s[:i]
	}
	return s
}

func expect(t *testing.T, got, want string) {
	t.Helper()
	if got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

func one(t *testing.T, c *engine.Catalog, name string) *engine.Object {
	t.Helper()
	objs := c.Lookup(name)
	if len(objs) != 1 {
		t.Fatalf("lookup %q found %d objects", name, len(objs))
	}
	return objs[0]
}

func hasFK(o *engine.Object, col, refObject, refCol string) bool {
	for _, fk := range o.ForeignKeys {
		if reflect.DeepEqual(fk.Columns, []string{col}) && fk.RefObject == refObject && reflect.DeepEqual(fk.RefColumns, []string{refCol}) {
			return true
		}
	}
	return false
}

func hasUniqueIndex(o *engine.Object, col string) bool {
	for _, ix := range o.Indexes {
		if ix.Unique && reflect.DeepEqual(ix.Columns, []string{col}) {
			return true
		}
	}
	return false
}

func golden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", name)
	if *update {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("missing golden file %s (run the test with -update): %v", path, err)
	}
	if got != string(want) {
		t.Errorf("%s mismatch\n--- got\n%s\n--- want\n%s", path, got, want)
	}
}
```

- [ ] **Step 3: Write the failing SQLite tests**

`internal/engine/sqlite/sqlite_test.go`:
```go
package sqlite

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/anderson-0/qh/internal/engine"
	"github.com/anderson-0/qh/internal/engine/enginetest"
	"github.com/anderson-0/qh/internal/qerr"
	"github.com/anderson-0/qh/internal/testutil"
)

func TestConformance(t *testing.T) {
	enginetest.Run(t, enginetest.Harness{
		Engine: Engine{},
		Setup: func(t *testing.T) (engine.ConnSpec, engine.ConnSpec) {
			path := testutil.SQLiteFixture(t)
			return engine.ConnSpec{ID: "ro", Version: 1, Engine: "sqlite", DSN: path, Mode: engine.ModeRO, PoolSize: 2},
				engine.ConnSpec{ID: "rw", Version: 1, Engine: "sqlite", DSN: "sqlite://" + path, Mode: engine.ModeRW, PoolSize: 2}
		},
		SlowQuery: "WITH RECURSIVE c(x) AS (SELECT 1 UNION ALL SELECT x + 1 FROM c) SELECT count(*) FROM c",
		ReadOnlyEscapes: []string{
			"ATTACH DATABASE 'qh-evil-attach.db' AS evil",
			"VACUUM INTO 'qh-evil-vacuum.db'",
			"PRAGMA query_only = 0",
		},
		GoldenPrefix: "sqlite",
	})
	for _, f := range []string{"qh-evil-attach.db", "qh-evil-vacuum.db"} {
		if _, err := os.Stat(f); err == nil {
			os.Remove(f)
			t.Errorf("%s was created", f)
		}
	}
}

func TestAttachRejectedOnReadWrite(t *testing.T) {
	path := testutil.SQLiteFixture(t)
	c, err := Engine{}.Open(context.Background(), engine.ConnSpec{ID: "rw", DSN: path, Mode: engine.ModeRW, PoolSize: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	target := filepath.Join(t.TempDir(), "attached.db")
	_, err = c.Query(context.Background(), "ATTACH DATABASE '"+target+"' AS x", engine.QueryOpts{MaxRows: 10})
	if err == nil || !strings.Contains(err.Error(), "error[invalid]") {
		t.Fatalf("attach on rw: %v", err)
	}
	if _, err := os.Stat(target); err == nil {
		t.Fatal("attach created a file")
	}
}

func TestOpenMissingFileDoesNotCreate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing.db")
	_, err := Engine{}.Open(context.Background(), engine.ConnSpec{ID: "x", DSN: path, Mode: engine.ModeRW, PoolSize: 1})
	if e := qerr.From(err); err == nil || e.Category != qerr.ConnectionFailed {
		t.Fatalf("err = %v", err)
	}
	if _, err := os.Stat(path); err == nil {
		t.Fatal("Open created the file")
	}
}

func TestPath(t *testing.T) {
	cases := map[string]string{
		"/tmp/a b.db":           "/tmp/a b.db",
		"sqlite:///tmp/a.db":    "/tmp/a.db",
		"file:/tmp/a.db":        "/tmp/a.db",
		"file:///tmp/a.db":      "/tmp/a.db",
	}
	for in, want := range cases {
		if got, err := Path(in); err != nil || got != want {
			t.Errorf("Path(%q) = %q, %v", in, got, err)
		}
	}
	for _, bad := range []string{"", "/tmp/a.db?_pragma=x", "/tmp/a.db#frag"} {
		if _, err := Path(bad); err == nil {
			t.Errorf("Path(%q) accepted", bad)
		}
	}
	info, err := Engine{}.Inspect("sqlite:///tmp/a.db")
	if err != nil || info.Database != "/tmp/a.db" || info.Redacted != "/tmp/a.db" || info.Password != "" {
		t.Fatalf("Inspect = %+v, %v", info, err)
	}
}
```

- [ ] **Step 4: Run them to verify they fail**

Run: `go test ./internal/engine/sqlite/`
Expected: FAIL — `undefined: Engine`.

- [ ] **Step 5: Implement the SQLite engine**

`internal/engine/sqlite/sqlite.go`:
```go
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
	if _, err := os.Stat(path); err != nil {
		return nil, qerr.Wrap(qerr.ConnectionFailed, "database file not found: "+path, err)
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
		if err := sc.QueryRowContext(ctx, "SELECT changes()").Scan(&res.RowsAffected); err != nil {
			return nil, classify(ctx, err)
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
```

`internal/engine/sqlite/introspect.go`:
```go
package sqlite

import (
	"context"
	"database/sql"
	"sort"
	"strings"

	"github.com/anderson-0/qh/internal/engine"
)

const (
	columnsSQL = `SELECT m.name, m.type, p.name, p.type, p."notnull", p.dflt_value, p.pk
		FROM sqlite_schema m JOIN pragma_table_info(m.name) p
		WHERE m.type IN ('table', 'view') AND m.name NOT LIKE 'sqlite_%'
		ORDER BY m.name, p.cid`
	foreignKeysSQL = `SELECT m.name, f.id, f."table", f."from", f."to"
		FROM sqlite_schema m JOIN pragma_foreign_key_list(m.name) f
		WHERE m.type = 'table' AND m.name NOT LIKE 'sqlite_%'
		ORDER BY m.name, f.id, f.seq`
	indexesSQL = `SELECT m.name, il.name, il."unique", il.origin, ii.name
		FROM sqlite_schema m JOIN pragma_index_list(m.name) il JOIN pragma_index_info(il.name) ii
		WHERE m.type = 'table' AND m.name NOT LIKE 'sqlite_%'
		ORDER BY m.name, il.name, ii.seqno`
)

func (c *conn) Introspect(ctx context.Context) (*engine.Catalog, error) {
	sc, err := c.acquire(ctx)
	if err != nil {
		return nil, err
	}
	defer sc.Close()

	ns := engine.Namespace{Name: "main"}
	objs := map[string]*engine.Object{}
	pkPos := map[string]map[int]string{}
	rows, err := sc.QueryContext(ctx, columnsSQL)
	if err != nil {
		return nil, classify(ctx, err)
	}
	for rows.Next() {
		var table, kind, col, typ string
		var notNull, pk int
		var dflt sql.NullString
		if err := rows.Scan(&table, &kind, &col, &typ, &notNull, &dflt, &pk); err != nil {
			rows.Close()
			return nil, classify(ctx, err)
		}
		o := objs[table]
		if o == nil {
			k := engine.KindTable
			if kind == "view" {
				k = engine.KindView
			}
			o = &engine.Object{Namespace: "main", Name: table, Kind: k, RowEstimate: -1}
			objs[table] = o
			ns.Objects = append(ns.Objects, o)
		}
		o.Fields = append(o.Fields, engine.Field{
			Name: col, Type: strings.ToLower(typ), Nullable: notNull == 0 && pk == 0, Default: dflt.String,
		})
		if pk > 0 {
			if pkPos[table] == nil {
				pkPos[table] = map[int]string{}
			}
			pkPos[table][pk] = col
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, classify(ctx, err)
	}
	for table, pos := range pkPos {
		keys := make([]int, 0, len(pos))
		for k := range pos {
			keys = append(keys, k)
		}
		sort.Ints(keys)
		for _, k := range keys {
			objs[table].PrimaryKey = append(objs[table].PrimaryKey, pos[k])
		}
	}

	rows, err = sc.QueryContext(ctx, foreignKeysSQL)
	if err != nil {
		return nil, classify(ctx, err)
	}
	type fkKey struct {
		table string
		id    int
	}
	fks := map[fkKey]*engine.ForeignKey{}
	var order []fkKey
	for rows.Next() {
		var table, ref, from string
		var to sql.NullString
		var id int
		if err := rows.Scan(&table, &id, &ref, &from, &to); err != nil {
			rows.Close()
			return nil, classify(ctx, err)
		}
		k := fkKey{table, id}
		fk := fks[k]
		if fk == nil {
			fk = &engine.ForeignKey{Namespace: "main", Object: table, RefNamespace: "main", RefObject: ref}
			fks[k] = fk
			order = append(order, k)
		}
		fk.Columns = append(fk.Columns, from)
		fk.RefColumns = append(fk.RefColumns, to.String) // empty means "the referenced primary key"
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, classify(ctx, err)
	}
	for _, k := range order {
		fk := fks[k]
		if target := objs[fk.RefObject]; target != nil && len(target.PrimaryKey) == len(fk.RefColumns) {
			for i, rc := range fk.RefColumns {
				if rc == "" {
					fk.RefColumns[i] = target.PrimaryKey[i]
				}
			}
		}
		if o := objs[k.table]; o != nil {
			o.ForeignKeys = append(o.ForeignKeys, *fk)
		}
	}

	rows, err = sc.QueryContext(ctx, indexesSQL)
	if err != nil {
		return nil, classify(ctx, err)
	}
	idx := map[string]*engine.Index{}
	var idxOrder []struct{ table, name string }
	for rows.Next() {
		var table, name, origin string
		var unique int
		var col sql.NullString
		if err := rows.Scan(&table, &name, &unique, &origin, &col); err != nil {
			rows.Close()
			return nil, classify(ctx, err)
		}
		if origin == "pk" {
			continue
		}
		ix := idx[name]
		if ix == nil {
			ix = &engine.Index{Name: name, Unique: unique == 1}
			idx[name] = ix
			idxOrder = append(idxOrder, struct{ table, name string }{table, name})
		}
		c := col.String
		if !col.Valid {
			c = "<expr>"
		}
		ix.Columns = append(ix.Columns, c)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, classify(ctx, err)
	}
	for _, e := range idxOrder {
		if o := objs[e.table]; o != nil {
			o.Indexes = append(o.Indexes, *idx[e.name])
		}
	}

	cat := &engine.Catalog{Namespaces: []engine.Namespace{ns}}
	cat.Link()
	return cat, nil
}
```

- [ ] **Step 6: Generate the golden files and review them**

Run: `go test ./internal/engine/sqlite/ -run TestConformance -update`
Then check that the generated files contain exactly:

`testdata/sqlite_overview.golden`:
```
main: 3 tables, 1 view
  order_items · orders · paid_orders (view) · users
```
`testdata/sqlite_orders.golden`:
```
main.orders
  id integer PK
  user_id integer NN → users.id
  status text NN
  total numeric(12,2)
  note text
  ← order_items.order_id
  idx (user_id)
```
`testdata/sqlite_users.golden`:
```
main.users
  id integer PK
  email text NN
  name text
  bio text
  avatar blob
  created_at text
  ← orders.user_id
  idx unique(email)
```
If a file differs, fix the engine (not the golden file) unless the difference is clearly better output; record any intentional difference in the commit message.

- [ ] **Step 7: Run all SQLite tests**

Run: `go test ./internal/engine/sqlite/`
Expected: PASS, and no `qh-evil-*.db` files appear in `internal/engine/sqlite/`.

- [ ] **Step 8: Commit**

```bash
git add internal/testutil internal/engine/enginetest internal/engine/sqlite
git commit -m "feat: add engine conformance suite and SQLite engine"
```

---

### Task 10: Postgres engine

**Files:**
- Create: `internal/engine/postgres/postgres.go`, `internal/engine/postgres/introspect.go`
- Test: `internal/engine/postgres/postgres_test.go`; golden files `internal/engine/postgres/testdata/postgres_{overview,orders,users}.golden`

**Interfaces:**
- Consumes: `engine.*`, `sqlx`, `qerr`, `enginetest.Run`.
- Produces: package `postgres` registers engine `"postgres"`; exports `type Engine struct{}`. Unexported `shortType(string) string`, package var `slotWait`.

- [ ] **Step 1: Add dependencies**

```bash
go get github.com/jackc/pgx/v5@v5.11.0 golang.org/x/sync@v0.24.0
go get github.com/testcontainers/testcontainers-go@v0.44.0 github.com/testcontainers/testcontainers-go/modules/postgres@v0.44.0
```

- [ ] **Step 2: Write the failing tests**

`internal/engine/postgres/postgres_test.go`:
```go
package postgres

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/anderson-0/qh/internal/engine"
	"github.com/anderson-0/qh/internal/engine/enginetest"
	"github.com/anderson-0/qh/internal/format"
	"github.com/anderson-0/qh/internal/qerr"
)

const fixture = `
CREATE TYPE order_status AS ENUM ('pending', 'paid', 'shipped');
CREATE TABLE users (id bigint PRIMARY KEY, email text NOT NULL UNIQUE, name text, bio text, avatar bytea, created_at timestamp);
COMMENT ON TABLE users IS 'App users';
COMMENT ON COLUMN users.email IS 'Login email';
CREATE TABLE orders (id bigserial PRIMARY KEY, user_id bigint NOT NULL REFERENCES users(id),
  status order_status NOT NULL DEFAULT 'pending', total numeric(12,2), note text);
CREATE INDEX orders_user_id ON orders (user_id);
CREATE TABLE order_items (order_id bigint NOT NULL REFERENCES orders(id), sku text NOT NULL, qty int NOT NULL,
  PRIMARY KEY (order_id, sku));
CREATE VIEW paid_orders AS SELECT id, user_id, total FROM orders WHERE status = 'paid';
CREATE TABLE "AuditLog" (id bigint PRIMARY KEY, "EventName" text);
INSERT INTO users VALUES
  (1, 'alice@example.com', 'Alice', E'line1\nline2\tx', '\xdeadbeef', '2025-03-14 10:22:31'),
  (2, 'zoe@example.com', 'Zoë 🚀', NULL, NULL, NULL),
  (3, 'nobody@example.com', NULL, NULL, NULL, NULL);
INSERT INTO orders (id, user_id, status, total)
  SELECT i, (i % 3) + 1, (CASE WHEN i % 2 = 0 THEN 'paid' ELSE 'pending' END)::order_status, i * 1.5
  FROM generate_series(1, 250) i;
SELECT setval('orders_id_seq', 250);
INSERT INTO order_items SELECT i, 'sku-1', 1 FROM generate_series(1, 10) i
  UNION ALL SELECT i, 'sku-2', 2 FROM generate_series(1, 10) i;
ANALYZE;
`

func startPostgres(t *testing.T, image string) string {
	t.Helper()
	if testing.Short() {
		t.Skip("needs Docker")
	}
	testcontainers.SkipIfProviderIsNotHealthy(t)
	ctx := context.Background()
	ctr, err := tcpostgres.Run(ctx, image,
		tcpostgres.WithDatabase("qh"), tcpostgres.WithUsername("qh"), tcpostgres.WithPassword("secret-pw-123"),
		tcpostgres.BasicWaitStrategies())
	t.Cleanup(func() { _ = testcontainers.TerminateContainer(ctr) })
	if err != nil {
		t.Fatal(err)
	}
	dsn, err := ctr.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	if _, err := conn.Exec(ctx, fixture); err != nil {
		t.Fatalf("fixture: %v", err)
	}
	return dsn
}

func specs(dsn string) (engine.ConnSpec, engine.ConnSpec) {
	return engine.ConnSpec{ID: "ro", Version: 1, Engine: "postgres", DSN: dsn, Mode: engine.ModeRO, PoolSize: 3},
		engine.ConnSpec{ID: "rw", Version: 1, Engine: "postgres", DSN: dsn, Mode: engine.ModeRW, PoolSize: 3}
}

func TestConformance(t *testing.T) {
	for _, image := range []string{"postgres:13-alpine", "postgres:17-alpine"} {
		t.Run(image, func(t *testing.T) {
			enginetest.Run(t, enginetest.Harness{
				Engine:    Engine{},
				Setup:     func(t *testing.T) (engine.ConnSpec, engine.ConnSpec) { return specs(startPostgres(t, image)) },
				SlowQuery: "SELECT pg_sleep(5)",
				ReadOnlyEscapes: []string{
					"SELECT nextval('orders_id_seq')",
					"WITH d AS (DELETE FROM order_items RETURNING *) SELECT count(*) FROM d",
					"DO $$ BEGIN DELETE FROM order_items; END $$",
					"SELECT * INTO evil FROM users",
					"CREATE TEMP TABLE evil_tmp (x int)",
					"SET TRANSACTION READ WRITE",
					"SELECT 1; COMMIT; DELETE FROM order_items",
				},
				GoldenPrefix: "postgres",
			})
		})
	}
}

func TestMixedCaseIdentifiers(t *testing.T) {
	ro, _ := specs(startPostgres(t, "postgres:17-alpine"))
	c, err := Engine{}.Open(context.Background(), ro)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	cat, err := c.Introspect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if out, n := format.Schema(cat, format.SchemaOpts{Pattern: "audit"}); n != 1 || !strings.Contains(out, "public.AuditLog") || !strings.Contains(out, "EventName text") {
		t.Fatalf("n=%d out=%q", n, out)
	}
	_, err = c.Query(context.Background(), `SELECT eventname FROM "AuditLog"`, engine.QueryOpts{MaxRows: 10, ReadOnly: true, Timeout: 5 * time.Second})
	if e := qerr.From(err); e.UnknownColumn != "eventname" {
		t.Fatalf("err = %#v", e)
	}
}

func TestBusy(t *testing.T) {
	ro, _ := specs(startPostgres(t, "postgres:17-alpine"))
	ro.PoolSize = 1
	c, err := Engine{}.Open(context.Background(), ro)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	old := slotWait
	slotWait = 200 * time.Millisecond
	defer func() { slotWait = old }()
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, _ = c.Query(context.Background(), "SELECT pg_sleep(2)", engine.QueryOpts{MaxRows: 1, ReadOnly: true, Timeout: 5 * time.Second})
	}()
	time.Sleep(300 * time.Millisecond)
	_, err = c.Query(context.Background(), "SELECT 1", engine.QueryOpts{MaxRows: 1, ReadOnly: true, Timeout: 5 * time.Second})
	if e := qerr.From(err); err == nil || e.Category != qerr.Busy {
		t.Fatalf("err = %v", err)
	}
	wg.Wait()
}

func TestConnectionFailed(t *testing.T) {
	_, err := Engine{}.Open(context.Background(), engine.ConnSpec{ID: "x", DSN: "postgres://u:hunter22@127.0.0.1:1/db?connect_timeout=2", Mode: engine.ModeRO, PoolSize: 1})
	if e := qerr.From(err); err == nil || e.Category != qerr.ConnectionFailed {
		t.Fatalf("err = %v", err)
	}
}

func TestInspect(t *testing.T) {
	info, err := Engine{}.Inspect("postgres://bob:hunter22@db.example.com:6543/shop?sslmode=require")
	if err != nil {
		t.Fatal(err)
	}
	if info.Host != "db.example.com" || info.Port != 6543 || info.Database != "shop" || info.User != "bob" || info.Password != "hunter22" {
		t.Fatalf("info = %+v", info)
	}
	if info.Redacted != "postgres://bob:***@db.example.com:6543/shop?sslmode=require" {
		t.Fatalf("redacted = %q", info.Redacted)
	}
	info, err = Engine{}.Inspect("host=db user=bob password=hunter22 dbname=shop")
	if err != nil || info.Redacted != "host=db user=bob password=*** dbname=shop" {
		t.Fatalf("keyword form = %+v, %v", info, err)
	}
	if _, err := (Engine{}).Inspect("postgres://bob:hunter22@[bad"); err == nil || strings.Contains(err.Error(), "hunter22") {
		t.Fatalf("bad dsn err = %v", err)
	}
}

func TestShortType(t *testing.T) {
	cases := map[string]string{
		"timestamp without time zone":    "timestamp",
		"timestamp(3) with time zone":    "timestamptz(3)",
		"time with time zone":            "timetz",
		"character varying(255)":         "varchar(255)",
		"double precision":               "float8",
		"numeric(12,2)":                  "numeric(12,2)",
	}
	for in, want := range cases {
		if got := shortType(in); got != want {
			t.Errorf("shortType(%q) = %q, want %q", in, got, want)
		}
	}
}
```

- [ ] **Step 3: Run them to verify they fail**

Run: `go test ./internal/engine/postgres/`
Expected: FAIL — `undefined: Engine`.

- [ ] **Step 4: Implement the engine**

`internal/engine/postgres/postgres.go`:
```go
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
		if q.Has("password") {
			q.Set("password", placeholder)
			u.RawQuery = q.Encode()
		}
		return strings.ReplaceAll(u.String(), placeholder, "***")
	}
	return kvPassword.ReplaceAllString(dsn, "${1}***")
}

func (Engine) Open(ctx context.Context, spec engine.ConnSpec) (engine.Conn, error) {
	cfg, err := pgxpool.ParseConfig(spec.DSN)
	if err != nil {
		return nil, qerr.New(qerr.Invalid, "invalid postgres connection string")
	}
	cfg.MaxConns = int32(max(spec.PoolSize, 1))
	cfg.MaxConnIdleTime = 10 * time.Minute
	cfg.ConnConfig.DefaultQueryExecMode = pgx.QueryExecModeExec
	if cfg.ConnConfig.ConnectTimeout == 0 {
		cfg.ConnConfig.ConnectTimeout = 10 * time.Second
	}
	cfg.ConnConfig.RuntimeParams["application_name"] = "qh"
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
	err = exec(ctx, tx, `SELECT set_config('statement_timeout', $1, true), set_config('TimeZone', 'UTC', true), set_config('DateStyle', 'ISO, YMD', true)`,
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
	timeout := qerr.Wrap(qerr.Timeout, "query exceeded the connection timeout", err).
		WithHint("Add WHERE/LIMIT or narrow the range")
	var pe *pgconn.PgError
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
	var ce *pgconn.ConnectError
	var ne net.Error
	if errors.As(err, &ce) || errors.As(err, &ne) || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return qerr.Wrap(qerr.ConnectionFailed, "cannot reach the database: "+err.Error(), err)
	}
	return qerr.Wrap(qerr.QueryError, err.Error(), err)
}
```

`internal/engine/postgres/introspect.go`:
```go
package postgres

import (
	"context"
	"strings"

	"github.com/jackc/pgx/v5"
	"golang.org/x/sync/errgroup"

	"github.com/anderson-0/qh/internal/engine"
)

const nsFilter = `n.nspname NOT IN ('pg_catalog', 'information_schema')
	AND n.nspname NOT LIKE 'pg\_toast%' AND n.nspname NOT LIKE 'pg\_temp%'`

var (
	relationsSQL = `SELECT n.nspname::text, c.relname::text, c.relkind::text, c.reltuples::bigint,
		COALESCE(obj_description(c.oid, 'pg_class'), '')
	FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
	WHERE c.relkind IN ('r', 'p', 'v', 'm', 'f') AND NOT c.relispartition
		AND has_table_privilege(c.oid, 'SELECT') AND ` + nsFilter + `
	ORDER BY 1, 2`

	columnsSQL = `SELECT n.nspname::text, c.relname::text, a.attname::text, format_type(a.atttypid, a.atttypmod),
		NOT a.attnotnull,
		CASE WHEN a.attidentity <> '' THEN 'nextval(identity)' ELSE COALESCE(pg_get_expr(d.adbin, d.adrelid), '') END,
		COALESCE(col_description(c.oid, a.attnum), ''), a.atttypid::bigint
	FROM pg_attribute a
	JOIN pg_class c ON c.oid = a.attrelid JOIN pg_namespace n ON n.oid = c.relnamespace
	LEFT JOIN pg_attrdef d ON d.adrelid = a.attrelid AND d.adnum = a.attnum
	WHERE a.attnum > 0 AND NOT a.attisdropped AND c.relkind IN ('r', 'p', 'v', 'm', 'f')
		AND NOT c.relispartition AND ` + nsFilter + `
	ORDER BY 1, 2, a.attnum`

	constraintsSQL = `SELECT n.nspname::text, c.relname::text, con.contype::text, con.conname::text,
		ARRAY(SELECT a.attname::text FROM unnest(con.conkey) WITH ORDINALITY k(attnum, ord)
			JOIN pg_attribute a ON a.attrelid = con.conrelid AND a.attnum = k.attnum ORDER BY k.ord),
		COALESCE(fn.nspname::text, ''), COALESCE(fc.relname::text, ''),
		ARRAY(SELECT a.attname::text FROM unnest(con.confkey) WITH ORDINALITY k(attnum, ord)
			JOIN pg_attribute a ON a.attrelid = con.confrelid AND a.attnum = k.attnum ORDER BY k.ord)
	FROM pg_constraint con
	JOIN pg_class c ON c.oid = con.conrelid JOIN pg_namespace n ON n.oid = c.relnamespace
	LEFT JOIN pg_class fc ON fc.oid = con.confrelid LEFT JOIN pg_namespace fn ON fn.oid = fc.relnamespace
	WHERE con.contype IN ('p', 'f') AND ` + nsFilter + `
	ORDER BY 1, 2, 4`

	indexesSQL = `SELECT n.nspname::text, c.relname::text, i.relname::text, ix.indisunique,
		ARRAY(SELECT pg_get_indexdef(ix.indexrelid, k, true) FROM generate_series(1, ix.indnkeyatts) AS k ORDER BY k)
	FROM pg_index ix
	JOIN pg_class i ON i.oid = ix.indexrelid JOIN pg_class c ON c.oid = ix.indrelid
	JOIN pg_namespace n ON n.oid = c.relnamespace
	WHERE NOT ix.indisprimary AND ` + nsFilter + `
	ORDER BY 1, 2, 3`

	enumsSQL = `SELECT e.enumtypid::bigint, e.enumlabel::text FROM pg_enum e ORDER BY e.enumtypid, e.enumsortorder`
)

type relRow struct {
	ns, name, kind, comment string
	est                     int64
}

type colRow struct {
	ns, table, name, typ, dflt, comment string
	nullable                            bool
	typeOID                             int64
}

type conRow struct {
	ns, table, kind, name, refNS, refTable string
	cols, refCols                          []string
}

type idxRow struct {
	ns, table, name string
	unique          bool
	cols            []string
}

func (c *conn) queryRows(ctx context.Context, sql string, each func(pgx.Rows) error) error {
	rows, err := c.pool.Query(ctx, sql, pgx.QueryExecModeExec)
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
		rels  []relRow
		cols  []colRow
		cons  []conRow
		idxs  []idxRow
		enums = map[int64][]string{}
	)
	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() error {
		return c.queryRows(gctx, relationsSQL, func(r pgx.Rows) error {
			var x relRow
			if err := r.Scan(&x.ns, &x.name, &x.kind, &x.est, &x.comment); err != nil {
				return err
			}
			rels = append(rels, x)
			return nil
		})
	})
	g.Go(func() error {
		return c.queryRows(gctx, columnsSQL, func(r pgx.Rows) error {
			var x colRow
			if err := r.Scan(&x.ns, &x.table, &x.name, &x.typ, &x.nullable, &x.dflt, &x.comment, &x.typeOID); err != nil {
				return err
			}
			cols = append(cols, x)
			return nil
		})
	})
	g.Go(func() error {
		return c.queryRows(gctx, constraintsSQL, func(r pgx.Rows) error {
			var x conRow
			if err := r.Scan(&x.ns, &x.table, &x.kind, &x.name, &x.cols, &x.refNS, &x.refTable, &x.refCols); err != nil {
				return err
			}
			cons = append(cons, x)
			return nil
		})
	})
	g.Go(func() error {
		return c.queryRows(gctx, indexesSQL, func(r pgx.Rows) error {
			var x idxRow
			if err := r.Scan(&x.ns, &x.table, &x.name, &x.unique, &x.cols); err != nil {
				return err
			}
			idxs = append(idxs, x)
			return nil
		})
	})
	g.Go(func() error {
		return c.queryRows(gctx, enumsSQL, func(r pgx.Rows) error {
			var oid int64
			var label string
			if err := r.Scan(&oid, &label); err != nil {
				return err
			}
			enums[oid] = append(enums[oid], label)
			return nil
		})
	})
	if err := g.Wait(); err != nil {
		return nil, c.classify(ctx, err)
	}
	return assemble(rels, cols, cons, idxs, enums), nil
}

func assemble(rels []relRow, cols []colRow, cons []conRow, idxs []idxRow, enums map[int64][]string) *engine.Catalog {
	cat := &engine.Catalog{}
	nsIndex := map[string]int{}
	objs := map[string]*engine.Object{}
	key := func(ns, name string) string { return ns + "\x00" + name }
	for _, r := range rels {
		i, ok := nsIndex[r.ns]
		if !ok {
			i = len(cat.Namespaces)
			nsIndex[r.ns] = i
			cat.Namespaces = append(cat.Namespaces, engine.Namespace{Name: r.ns})
		}
		o := &engine.Object{Namespace: r.ns, Name: r.name, Kind: kindOf(r.kind), Comment: r.comment, RowEstimate: r.est}
		if o.Kind == engine.KindView || o.RowEstimate < 0 {
			o.RowEstimate = -1
		}
		cat.Namespaces[i].Objects = append(cat.Namespaces[i].Objects, o)
		objs[key(r.ns, r.name)] = o
	}
	for _, c := range cols {
		if o := objs[key(c.ns, c.table)]; o != nil {
			o.Fields = append(o.Fields, engine.Field{
				Name: c.name, Type: shortType(c.typ), Nullable: c.nullable, Default: c.dflt,
				Comment: c.comment, Enum: enums[c.typeOID],
			})
		}
	}
	for _, c := range cons {
		o := objs[key(c.ns, c.table)]
		if o == nil {
			continue
		}
		switch c.kind {
		case "p":
			o.PrimaryKey = c.cols
		case "f":
			o.ForeignKeys = append(o.ForeignKeys, engine.ForeignKey{
				Namespace: c.ns, Object: c.table, Columns: c.cols,
				RefNamespace: c.refNS, RefObject: c.refTable, RefColumns: c.refCols,
			})
		}
	}
	for _, x := range idxs {
		if o := objs[key(x.ns, x.table)]; o != nil {
			o.Indexes = append(o.Indexes, engine.Index{Name: x.name, Columns: x.cols, Unique: x.unique})
		}
	}
	cat.Link()
	return cat
}

func kindOf(relkind string) engine.Kind {
	switch relkind {
	case "v":
		return engine.KindView
	case "m":
		return engine.KindMatView
	}
	return engine.KindTable
}

// shortType shortens Postgres's verbose type names to save tokens.
func shortType(t string) string {
	switch {
	case strings.HasSuffix(t, " without time zone"):
		t = strings.TrimSuffix(t, " without time zone")
	case strings.HasSuffix(t, " with time zone"):
		t = strings.TrimSuffix(t, " with time zone")
		if strings.HasPrefix(t, "timestamp") {
			t = "timestamptz" + strings.TrimPrefix(t, "timestamp")
		} else if strings.HasPrefix(t, "time") {
			t = "timetz" + strings.TrimPrefix(t, "time")
		}
	}
	t = strings.Replace(t, "character varying", "varchar", 1)
	return strings.Replace(t, "double precision", "float8", 1)
}
```

- [ ] **Step 5: Run the Docker-free tests**

Run: `go test -short ./internal/engine/postgres/`
Expected: PASS for `TestInspect`, `TestShortType`, `TestConnectionFailed`; the Docker tests report SKIP.

- [ ] **Step 6: Generate and review the golden files (needs Docker)**

Run: `go test ./internal/engine/postgres/ -run 'TestConformance/postgres:17' -update`
Check that the files contain exactly:

`testdata/postgres_overview.golden`:
```
public: 4 tables, 1 view
  AuditLog ~0 · order_items ~20 · orders ~250 · paid_orders (view) · users ~3
```
`testdata/postgres_orders.golden`:
```
public.orders ~250 rows
  id bigint PK =auto
  user_id bigint NN → users.id
  status order_status NN [pending|paid|shipped] ='pending'::order_status
  total numeric(12,2)
  note text
  ← order_items.order_id
  idx (user_id)
```
`testdata/postgres_users.golden`:
```
public.users ~3 rows: App users
  id bigint PK
  email text NN  # Login email
  name text
  bio text
  avatar bytea
  created_at timestamp
  ← orders.user_id
  idx unique(email)
```
On PostgreSQL 13 an empty table that was analyzed may report `~0` or no estimate; if Postgres 13 differs from 17 only in `AuditLog`'s estimate, change the fixture to insert one `AuditLog` row (`INSERT INTO "AuditLog" VALUES (1, 'login');`) and regenerate, so both versions share one golden file.

- [ ] **Step 7: Run all Postgres tests (needs Docker)**

Run: `go test ./internal/engine/postgres/`
Expected: PASS on both images.

- [ ] **Step 8: Commit**

```bash
git add go.mod go.sum internal/engine/postgres
git commit -m "feat: add Postgres engine with cursor-based row caps"
```

---

### Task 11: Schema cache

**Files:**
- Create: `internal/catalog/cache.go`
- Test: `internal/catalog/cache_test.go`

**Interfaces:**
- Consumes: `engine.Catalog`.
- Produces: `type catalog.Builder func(ctx context.Context) (*engine.Catalog, error)`; `catalog.New(ttl time.Duration) *Cache`; `(*Cache).Get(ctx, id string, version int64, build Builder) (*engine.Catalog, error)`; `(*Cache).Invalidate(id string)`.

- [ ] **Step 1: Write the failing test**

`internal/catalog/cache_test.go`:
```go
package catalog

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/anderson-0/qh/internal/engine"
)

func counting(calls *atomic.Int32) Builder {
	return func(context.Context) (*engine.Catalog, error) {
		calls.Add(1)
		return &engine.Catalog{}, nil
	}
}

func TestBuildsOnceForConcurrentCallers(t *testing.T) {
	c := New(time.Minute)
	var calls atomic.Int32
	build := func(ctx context.Context) (*engine.Catalog, error) {
		calls.Add(1)
		time.Sleep(50 * time.Millisecond)
		return &engine.Catalog{}, nil
	}
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := c.Get(context.Background(), "a", 1, build); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if n := calls.Load(); n != 1 {
		t.Fatalf("built %d times", n)
	}
}

func TestTTLVersionAndInvalidate(t *testing.T) {
	c := New(10 * time.Minute)
	now := time.Unix(0, 0)
	c.now = func() time.Time { return now }
	var calls atomic.Int32
	b := counting(&calls)
	ctx := context.Background()
	step := func(version int64, want int32) {
		t.Helper()
		if _, err := c.Get(ctx, "a", version, b); err != nil {
			t.Fatal(err)
		}
		if got := calls.Load(); got != want {
			t.Fatalf("builds = %d, want %d", got, want)
		}
	}
	step(1, 1)
	step(1, 1)
	now = now.Add(11 * time.Minute)
	step(1, 2)
	step(2, 3)
	c.Invalidate("a")
	step(2, 4)
}

func TestErrorsAreNotCached(t *testing.T) {
	c := New(time.Minute)
	var calls atomic.Int32
	fail := func(context.Context) (*engine.Catalog, error) { calls.Add(1); return nil, errors.New("boom") }
	for i := 0; i < 2; i++ {
		if _, err := c.Get(context.Background(), "a", 1, fail); err == nil {
			t.Fatal("expected error")
		}
	}
	if calls.Load() != 2 {
		t.Fatalf("calls = %d", calls.Load())
	}
}

func TestCallerCancelDoesNotFailOthers(t *testing.T) {
	c := New(time.Minute)
	release := make(chan struct{})
	build := func(ctx context.Context) (*engine.Catalog, error) {
		<-release
		return &engine.Catalog{}, ctx.Err()
	}
	ctx1, cancel1 := context.WithCancel(context.Background())
	errs := make(chan error, 2)
	go func() { _, err := c.Get(ctx1, "a", 1, build); errs <- err }()
	time.Sleep(20 * time.Millisecond)
	go func() { _, err := c.Get(context.Background(), "a", 1, build); errs <- err }()
	time.Sleep(20 * time.Millisecond)
	cancel1()
	if err := <-errs; !errors.Is(err, context.Canceled) {
		t.Fatalf("first caller err = %v", err)
	}
	close(release)
	if err := <-errs; err != nil {
		t.Fatalf("second caller err = %v", err)
	}
}

func TestInvalidateDuringBuildDiscardsResult(t *testing.T) {
	c := New(time.Minute)
	var calls atomic.Int32
	release := make(chan struct{})
	slow := func(context.Context) (*engine.Catalog, error) {
		calls.Add(1)
		<-release
		return &engine.Catalog{}, nil
	}
	done := make(chan struct{})
	go func() { c.Get(context.Background(), "a", 1, slow); close(done) }()
	time.Sleep(20 * time.Millisecond)
	c.Invalidate("a")
	close(release)
	<-done
	if _, err := c.Get(context.Background(), "a", 1, counting(&calls)); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 {
		t.Fatalf("stale build was cached; calls = %d", calls.Load())
	}
}
```

- [ ] **Step 2: Run it to verify it fails**

Run: `go test ./internal/catalog/`
Expected: FAIL — `undefined: New`.

- [ ] **Step 3: Implement**

`internal/catalog/cache.go`:
```go
// Package catalog caches each connection's schema.
package catalog

import (
	"context"
	"fmt"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"

	"github.com/anderson-0/qh/internal/engine"
)

const buildTimeout = 60 * time.Second

// Builder introspects a database.
type Builder func(ctx context.Context) (*engine.Catalog, error)

type entry struct {
	version int64
	cat     *engine.Catalog
	built   time.Time
}

// Cache holds one catalog per connection id, rebuilt when its TTL passes,
// the connection's version changes, or it is invalidated.
type Cache struct {
	ttl     time.Duration
	now     func() time.Time
	mu      sync.Mutex
	entries map[string]entry
	gens    map[string]uint64
	group   singleflight.Group
}

// New returns a cache whose entries live for ttl.
func New(ttl time.Duration) *Cache {
	return &Cache{ttl: ttl, now: time.Now, entries: map[string]entry{}, gens: map[string]uint64{}}
}

// Get returns the catalog for connection id at version. Concurrent callers
// share one build; a caller whose context ends stops waiting without
// cancelling the build for the others.
func (c *Cache) Get(ctx context.Context, id string, version int64, build Builder) (*engine.Catalog, error) {
	c.mu.Lock()
	if e, ok := c.entries[id]; ok && e.version == version && c.now().Sub(e.built) < c.ttl {
		c.mu.Unlock()
		return e.cat, nil
	}
	gen := c.gens[id]
	c.mu.Unlock()

	key := fmt.Sprintf("%s@%d#%d", id, version, gen)
	ch := c.group.DoChan(key, func() (any, error) {
		bctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), buildTimeout)
		defer cancel()
		cat, err := build(bctx)
		if err != nil {
			return nil, err
		}
		c.mu.Lock()
		if c.gens[id] == gen {
			c.entries[id] = entry{version: version, cat: cat, built: c.now()}
		}
		c.mu.Unlock()
		return cat, nil
	})
	select {
	case r := <-ch:
		if r.Err != nil {
			return nil, r.Err
		}
		return r.Val.(*engine.Catalog), nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// Invalidate drops the cached catalog for id, including any build in progress.
func (c *Cache) Invalidate(id string) {
	c.mu.Lock()
	delete(c.entries, id)
	c.gens[id]++
	c.mu.Unlock()
}
```

- [ ] **Step 4: Run it to verify it passes**

Run: `go test -race ./internal/catalog/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/catalog go.mod go.sum
git commit -m "feat: cache schemas per connection with single-flight builds"
```

---

### Task 12: Connection pool manager

**Files:**
- Create: `internal/pool/pool.go`
- Test: `internal/pool/pool_test.go`

**Interfaces:**
- Consumes: `engine.Conn`, `engine.ConnSpec`, `qerr`.
- Produces: `type pool.Opener func(ctx context.Context, spec engine.ConnSpec) (engine.Conn, error)`; `type pool.Options struct { IdleTimeout time.Duration; MaxTotalConns int; Now func() time.Time }`; `pool.New(open Opener, o Options) *Manager`; `(*Manager).Get(ctx, spec) (engine.Conn, func(), error)` — the returned func releases the connection and must be called exactly once (extra calls are no-ops); `(*Manager).Drop(id string)`; `(*Manager).Sweep()`; `(*Manager).Start(ctx)`; `(*Manager).Close()`.

- [ ] **Step 1: Write the failing test**

`internal/pool/pool_test.go`:
```go
package pool

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/anderson-0/qh/internal/engine"
	"github.com/anderson-0/qh/internal/qerr"
)

type fakeConn struct{ closed atomic.Bool }

func (f *fakeConn) Query(context.Context, string, engine.QueryOpts) (*engine.Result, error) {
	return &engine.Result{}, nil
}
func (f *fakeConn) Explain(context.Context, string, engine.QueryOpts) (*engine.Result, error) {
	return &engine.Result{}, nil
}
func (f *fakeConn) Introspect(context.Context) (*engine.Catalog, error) { return &engine.Catalog{}, nil }
func (f *fakeConn) Ping(context.Context) (string, error)                { return "fake", nil }
func (f *fakeConn) Close() error                                        { f.closed.Store(true); return nil }

type opener struct {
	mu    sync.Mutex
	opens map[string]int
	fail  map[string]error
	gate  chan struct{}
}

func newOpener() *opener { return &opener{opens: map[string]int{}, fail: map[string]error{}} }

func (o *opener) open(ctx context.Context, s engine.ConnSpec) (engine.Conn, error) {
	if o.gate != nil {
		<-o.gate
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	o.opens[s.ID]++
	if err := o.fail[s.ID]; err != nil {
		return nil, err
	}
	return &fakeConn{}, nil
}

func (o *opener) count(id string) int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.opens[id]
}

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func spec(id string, version int64, size int) engine.ConnSpec {
	return engine.ConnSpec{ID: id, Version: version, PoolSize: size}
}

func TestReuseAndVersionSwap(t *testing.T) {
	o := newOpener()
	m := New(o.open, Options{})
	ctx := context.Background()
	c1, rel1, err := m.Get(ctx, spec("a", 1, 2))
	if err != nil {
		t.Fatal(err)
	}
	c2, rel2, _ := m.Get(ctx, spec("a", 1, 2))
	if c1 != c2 || o.count("a") != 1 {
		t.Fatal("same version should reuse the pool")
	}
	rel2()
	c3, rel3, _ := m.Get(ctx, spec("a", 2, 2))
	if c3 == c1 || o.count("a") != 2 {
		t.Fatal("new version should open a new pool")
	}
	if c1.(*fakeConn).closed.Load() {
		t.Fatal("old pool closed while still in use")
	}
	rel1()
	rel1() // second call is a no-op
	if !c1.(*fakeConn).closed.Load() {
		t.Fatal("old pool not closed after last release")
	}
	rel3()
}

func TestIdleSweep(t *testing.T) {
	o := newOpener()
	clk := &clock{t: time.Unix(0, 0)}
	m := New(o.open, Options{IdleTimeout: 10 * time.Minute, Now: clk.now})
	ctx := context.Background()
	c, rel, _ := m.Get(ctx, spec("a", 1, 1))
	clk.advance(11 * time.Minute)
	m.Sweep()
	if c.(*fakeConn).closed.Load() {
		t.Fatal("swept an in-use pool")
	}
	rel()
	clk.advance(9 * time.Minute)
	m.Sweep()
	if c.(*fakeConn).closed.Load() {
		t.Fatal("swept before idle timeout")
	}
	clk.advance(2 * time.Minute)
	m.Sweep()
	if !c.(*fakeConn).closed.Load() {
		t.Fatal("idle pool not closed")
	}
	if _, rel, _ := m.Get(ctx, spec("a", 1, 1)); o.count("a") != 2 {
		t.Fatal("expected reopen after sweep")
	} else {
		rel()
	}
}

func TestBackoffAndRecovery(t *testing.T) {
	o := newOpener()
	clk := &clock{t: time.Unix(0, 0)}
	m := New(o.open, Options{Now: clk.now})
	ctx := context.Background()
	down := qerr.New(qerr.ConnectionFailed, "cannot reach the database")
	o.fail["a"] = down
	if _, _, err := m.Get(ctx, spec("a", 1, 1)); !errors.Is(err, down) {
		t.Fatalf("err = %v", err)
	}
	if _, _, err := m.Get(ctx, spec("a", 1, 1)); !errors.Is(err, down) || o.count("a") != 1 {
		t.Fatalf("within backoff: err %v, opens %d", err, o.count("a"))
	}
	clk.advance(1100 * time.Millisecond)
	m.Get(ctx, spec("a", 1, 1))
	if o.count("a") != 2 {
		t.Fatalf("opens = %d after first backoff", o.count("a"))
	}
	clk.advance(1100 * time.Millisecond) // second backoff is 2s
	m.Get(ctx, spec("a", 1, 1))
	if o.count("a") != 2 {
		t.Fatal("backoff did not double")
	}
	clk.advance(time.Second)
	delete(o.fail, "a")
	_, rel, err := m.Get(ctx, spec("a", 1, 1))
	if err != nil || o.count("a") != 3 {
		t.Fatalf("recovery: %v, opens %d", err, o.count("a"))
	}
	rel()
	if _, _, err := m.Get(ctx, spec("a", 2, 1)); err != nil {
		t.Fatalf("new version should not inherit backoff: %v", err)
	}
}

func TestFailureIsolated(t *testing.T) {
	o := newOpener()
	m := New(o.open, Options{})
	o.fail["down"] = qerr.New(qerr.ConnectionFailed, "down")
	if _, _, err := m.Get(context.Background(), spec("down", 1, 1)); err == nil {
		t.Fatal("expected failure")
	}
	if _, rel, err := m.Get(context.Background(), spec("up", 1, 1)); err != nil {
		t.Fatalf("healthy connection affected: %v", err)
	} else {
		rel()
	}
}

func TestConcurrentGetOpensOnce(t *testing.T) {
	o := newOpener()
	o.gate = make(chan struct{})
	m := New(o.open, Options{})
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, rel, err := m.Get(context.Background(), spec("a", 1, 1))
			if err != nil {
				t.Error(err)
				return
			}
			rel()
		}()
	}
	time.Sleep(50 * time.Millisecond)
	close(o.gate)
	wg.Wait()
	if o.count("a") != 1 {
		t.Fatalf("opened %d times", o.count("a"))
	}
}

func TestGlobalCap(t *testing.T) {
	o := newOpener()
	clk := &clock{t: time.Unix(0, 0)}
	m := New(o.open, Options{MaxTotalConns: 10, Now: clk.now})
	ctx := context.Background()
	a, relA, _ := m.Get(ctx, spec("a", 1, 5))
	clk.advance(time.Second)
	_, relB, _ := m.Get(ctx, spec("b", 1, 5))
	relA()
	_, relC, err := m.Get(ctx, spec("c", 1, 5))
	if err != nil {
		t.Fatalf("should evict idle pool a: %v", err)
	}
	if !a.(*fakeConn).closed.Load() {
		t.Fatal("least recently used idle pool not closed")
	}
	_, _, err = m.Get(ctx, spec("d", 1, 5))
	if e := qerr.From(err); err == nil || e.Category != qerr.Busy {
		t.Fatalf("all pools busy: err = %v", err)
	}
	relB()
	relC()
}

func TestDropAndClose(t *testing.T) {
	o := newOpener()
	m := New(o.open, Options{})
	c, rel, _ := m.Get(context.Background(), spec("a", 1, 1))
	m.Drop("a")
	if c.(*fakeConn).closed.Load() {
		t.Fatal("Drop closed an in-use pool")
	}
	rel()
	if !c.(*fakeConn).closed.Load() {
		t.Fatal("dropped pool not closed after release")
	}
	c2, _, _ := m.Get(context.Background(), spec("b", 1, 1))
	m.Close()
	if !c2.(*fakeConn).closed.Load() {
		t.Fatal("Close did not close pools")
	}
	if _, _, err := m.Get(context.Background(), spec("b", 1, 1)); err == nil {
		t.Fatal("Get after Close succeeded")
	}
}
```

- [ ] **Step 2: Run it to verify it fails**

Run: `go test ./internal/pool/`
Expected: FAIL — `undefined: New`.

- [ ] **Step 3: Implement**

`internal/pool/pool.go`:
```go
// Package pool keeps one open engine.Conn per connection version, closes idle
// ones, caps the total number of database connections, and backs off from
// databases that cannot be reached.
package pool

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"time"

	"github.com/anderson-0/qh/internal/engine"
	"github.com/anderson-0/qh/internal/qerr"
)

// Opener opens a connection.
type Opener func(ctx context.Context, spec engine.ConnSpec) (engine.Conn, error)

// Options configures a Manager. Zero values select the defaults.
type Options struct {
	IdleTimeout   time.Duration // default 10 minutes
	MaxTotalConns int           // default 500
	Now           func() time.Time
}

type handle struct {
	conn     engine.Conn
	version  int64
	size     int
	inflight int
	lastUsed time.Time
	retired  bool
}

type failure struct {
	version int64
	count   int
	retryAt time.Time
	err     error
}

// Manager hands out pooled connections.
type Manager struct {
	open Opener
	opt  Options

	mu       sync.Mutex
	closed   bool
	handles  map[string]*handle        // by connection id
	failures map[string]*failure       // by connection id
	opening  map[string]chan struct{}  // by id@version
}

var errClosed = errors.New("pool: manager closed")

// New returns a Manager.
func New(open Opener, opt Options) *Manager {
	if opt.IdleTimeout <= 0 {
		opt.IdleTimeout = 10 * time.Minute
	}
	if opt.MaxTotalConns <= 0 {
		opt.MaxTotalConns = 500
	}
	if opt.Now == nil {
		opt.Now = time.Now
	}
	return &Manager{
		open: open, opt: opt,
		handles: map[string]*handle{}, failures: map[string]*failure{}, opening: map[string]chan struct{}{},
	}
}

// Get returns an open connection for spec and a release function.
func (m *Manager) Get(ctx context.Context, spec engine.ConnSpec) (engine.Conn, func(), error) {
	key := spec.ID + "@" + strconv.FormatInt(spec.Version, 10)
	size := max(spec.PoolSize, 1)
	for {
		var toClose []engine.Conn
		m.mu.Lock()
		if m.closed {
			m.mu.Unlock()
			return nil, nil, errClosed
		}
		now := m.opt.Now()
		if h := m.handles[spec.ID]; h != nil {
			if h.version == spec.Version {
				h.inflight++
				h.lastUsed = now
				m.mu.Unlock()
				return h.conn, m.releaser(h), nil
			}
			delete(m.handles, spec.ID)
			toClose = append(toClose, retire(h)...)
		}
		if f := m.failures[spec.ID]; f != nil && f.version == spec.Version && now.Before(f.retryAt) {
			err := f.err
			m.mu.Unlock()
			closeAll(toClose)
			return nil, nil, err
		}
		if wait, ok := m.opening[key]; ok {
			m.mu.Unlock()
			closeAll(toClose)
			select {
			case <-wait:
				continue
			case <-ctx.Done():
				return nil, nil, ctx.Err()
			}
		}
		evicted, err := m.makeRoom(size)
		toClose = append(toClose, evicted...)
		if err != nil {
			m.mu.Unlock()
			closeAll(toClose)
			return nil, nil, err
		}
		done := make(chan struct{})
		m.opening[key] = done
		m.mu.Unlock()
		closeAll(toClose)

		conn, err := m.open(ctx, spec)

		m.mu.Lock()
		delete(m.opening, key)
		close(done)
		if err != nil {
			if ctx.Err() == nil {
				m.recordFailure(spec, err)
			}
			m.mu.Unlock()
			return nil, nil, err
		}
		if m.closed {
			m.mu.Unlock()
			conn.Close()
			return nil, nil, errClosed
		}
		delete(m.failures, spec.ID)
		var late []engine.Conn
		if old := m.handles[spec.ID]; old != nil {
			late = retire(old)
		}
		h := &handle{conn: conn, version: spec.Version, size: size, inflight: 1, lastUsed: m.opt.Now()}
		m.handles[spec.ID] = h
		m.mu.Unlock()
		closeAll(late)
		return conn, m.releaser(h), nil
	}
}

// retire marks h as replaced and returns its conn if nobody is using it.
func retire(h *handle) []engine.Conn {
	h.retired = true
	if h.inflight == 0 {
		return []engine.Conn{h.conn}
	}
	return nil
}

func (m *Manager) releaser(h *handle) func() {
	var once sync.Once
	return func() {
		once.Do(func() {
			m.mu.Lock()
			h.inflight--
			h.lastUsed = m.opt.Now()
			closeIt := h.retired && h.inflight == 0
			m.mu.Unlock()
			if closeIt {
				h.conn.Close()
			}
		})
	}
}

// makeRoom evicts least-recently-used idle pools until size more connections
// fit under the cap. It must be called with m.mu held.
func (m *Manager) makeRoom(size int) ([]engine.Conn, error) {
	total := 0
	for _, h := range m.handles {
		total += h.size
	}
	var out []engine.Conn
	for total+size > m.opt.MaxTotalConns {
		var lruID string
		var lru *handle
		for id, h := range m.handles {
			if h.inflight == 0 && (lru == nil || h.lastUsed.Before(lru.lastUsed)) {
				lruID, lru = id, h
			}
		}
		if lru == nil {
			return out, qerr.New(qerr.Busy, "the server's database connection limit is reached").WithHint("Retry shortly")
		}
		delete(m.handles, lruID)
		total -= lru.size
		out = append(out, lru.conn)
	}
	return out, nil
}

func (m *Manager) recordFailure(spec engine.ConnSpec, err error) {
	f := m.failures[spec.ID]
	if f == nil || f.version != spec.Version {
		f = &failure{version: spec.Version}
		m.failures[spec.ID] = f
	}
	f.count++
	f.err = err
	f.retryAt = m.opt.Now().Add(backoff(f.count))
}

// backoff is 1s, 2s, 4s, 8s, 16s, then 30s.
func backoff(n int) time.Duration {
	if n > 5 {
		return 30 * time.Second
	}
	return time.Second << (n - 1)
}

// Drop retires the pool for id; it closes once in-flight work finishes.
func (m *Manager) Drop(id string) {
	var toClose []engine.Conn
	m.mu.Lock()
	if h := m.handles[id]; h != nil {
		delete(m.handles, id)
		toClose = retire(h)
	}
	delete(m.failures, id)
	m.mu.Unlock()
	closeAll(toClose)
}

// Sweep closes pools idle for longer than IdleTimeout.
func (m *Manager) Sweep() {
	var toClose []engine.Conn
	m.mu.Lock()
	now := m.opt.Now()
	for id, h := range m.handles {
		if h.inflight == 0 && now.Sub(h.lastUsed) >= m.opt.IdleTimeout {
			delete(m.handles, id)
			toClose = append(toClose, h.conn)
		}
	}
	for id, f := range m.failures {
		if now.Sub(f.retryAt) > m.opt.IdleTimeout {
			delete(m.failures, id)
		}
	}
	m.mu.Unlock()
	closeAll(toClose)
}

// Start runs Sweep periodically until ctx ends.
func (m *Manager) Start(ctx context.Context) {
	interval := min(m.opt.IdleTimeout/2, time.Minute)
	go func() {
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				m.Sweep()
			}
		}
	}()
}

// Close closes every pool. Later Gets fail.
func (m *Manager) Close() {
	m.mu.Lock()
	m.closed = true
	toClose := make([]engine.Conn, 0, len(m.handles))
	for id, h := range m.handles {
		toClose = append(toClose, h.conn)
		delete(m.handles, id)
	}
	m.mu.Unlock()
	closeAll(toClose)
}

func closeAll(cs []engine.Conn) {
	for _, c := range cs {
		_ = c.Close()
	}
}
```

- [ ] **Step 4: Run it to verify it passes**

Run: `go test -race ./internal/pool/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/pool
git commit -m "feat: manage connection pools with versioned swaps, idle close and backoff"
```

---

### Task 13: Core service

**Files:**
- Create: `internal/core/service.go`, `internal/core/connections.go`
- Test: `internal/core/service_test.go`, `internal/core/connections_test.go`

**Interfaces:**
- Consumes: `store.Store`, `store.Connection`, `store.NewID`, `secret.Keyring`, `pool.Manager`, `catalog.Cache`, `engine.*`, `sqlx.Classify`, `sqlx.ReferencedTables`, `format.Result`, `format.Schema`, `suggest.ColumnHint`, `suggest.TableHint`, `qerr`.
- Produces:
  - `core.New(st store.Store, keys *secret.Keyring, pools *pool.Manager, cache *catalog.Cache) *Service`; `core.OpenConn` (a `pool.Opener`).
  - Agent operations: `(*Service).Sources(ctx, ws string) (string, error)`; `Schema(ctx, ws string, in SchemaInput) (string, error)` with `SchemaInput{Source, Pattern, Object string}`; `Query(ctx, ws string, in QueryInput) (string, error)` with `QueryInput{Source, Query string; MaxRows int; Explain bool}`. Errors are `*qerr.Error`, already scrubbed.
  - Management: `ConnInput{Name, Engine, DSN, Description string; Mode engine.Mode; MaxRows, TimeoutMS, PoolSize int; AllowedNamespaces []string}`; `ConnPatch{DSN, Description *string; Mode *engine.Mode; MaxRows, TimeoutMS, PoolSize *int; AllowedNamespaces *[]string}`; `ConnInfo{Name, Engine, Description string; Mode engine.Mode; Host string; Port int; Database, User, DSN string; MaxRows, TimeoutMS, PoolSize int; AllowedNamespaces []string; Version int64; CreatedAt, UpdatedAt time.Time}` (`DSN` is redacted); `TestResult{ServerVersion string; Connect time.Duration}`.
  - `CreateConnection(ctx, ws string, in ConnInput, test bool) (*ConnInfo, *TestResult, error)`; `UpdateConnection(ctx, ws, name string, p ConnPatch, test bool) (*ConnInfo, *TestResult, error)`; `DeleteConnection(ctx, ws, name string) error`; `GetConnection(ctx, ws, name string) (*ConnInfo, error)`; `ListConnections(ctx, ws string) ([]*ConnInfo, error)`; `TestConnection(ctx, ws, name string) (*TestResult, error)`; `RefreshSchema(ctx, ws, name string) (objects int, took time.Duration, err error)`.
  - Constants `DefaultMaxRows=100, MaxMaxRows=10000, DefaultTimeoutMS=30000, MaxTimeoutMS=300000, DefaultPoolSize=5, MaxPoolSize=50`.

- [ ] **Step 1: Write the failing tests**

`internal/core/service_test.go`:
```go
package core

import (
	"context"
	"crypto/rand"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/anderson-0/qh/internal/catalog"
	"github.com/anderson-0/qh/internal/engine"
	_ "github.com/anderson-0/qh/internal/engine/sqlite"
	"github.com/anderson-0/qh/internal/pool"
	"github.com/anderson-0/qh/internal/secret"
	"github.com/anderson-0/qh/internal/store"
	"github.com/anderson-0/qh/internal/testutil"
)

const ws = store.DefaultWorkspace

type harness struct {
	svc *Service
	st  *store.SQLite
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "qh.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	key := make([]byte, secret.KeySize)
	rand.Read(key)
	keys, err := secret.NewKeyring(key)
	if err != nil {
		t.Fatal(err)
	}
	pools := pool.New(OpenConn, pool.Options{})
	t.Cleanup(pools.Close)
	return &harness{svc: New(st, keys, pools, catalog.New(10*time.Minute)), st: st}
}

func (h *harness) addShop(t *testing.T, mode engine.Mode) {
	t.Helper()
	_, _, err := h.svc.CreateConnection(context.Background(), ws, ConnInput{
		Name: "shop", Engine: "sqlite", DSN: testutil.SQLiteFixture(t), Description: "Shop DB", Mode: mode,
	}, true)
	if err != nil {
		t.Fatal(err)
	}
}

func (h *harness) query(t *testing.T, in QueryInput) (string, error) {
	t.Helper()
	if in.Source == "" {
		in.Source = "shop"
	}
	return h.svc.Query(context.Background(), ws, in)
}

func errText(err error) string {
	if err == nil {
		return "<nil>"
	}
	return err.Error()
}

func TestSources(t *testing.T) {
	h := newHarness(t)
	out, err := h.svc.Sources(context.Background(), ws)
	if err != nil || out != "-- no connections. Add one with `qh conn add`\n" {
		t.Fatalf("empty: %q, %v", out, err)
	}
	h.addShop(t, engine.ModeRO)
	out, _ = h.svc.Sources(context.Background(), ws)
	if out != "shop sqlite ro — Shop DB\n" {
		t.Fatalf("got %q", out)
	}
}

func TestQuery(t *testing.T) {
	h := newHarness(t)
	h.addShop(t, engine.ModeRO)
	out, err := h.query(t, QueryInput{Query: "SELECT id, email FROM users ORDER BY id"})
	if err != nil || !strings.HasPrefix(out, "id\temail\n1\talice@example.com\n2\tzoe@example.com\n3\tnobody@example.com\n-- 3 rows · ") {
		t.Fatalf("got %q, %v", out, err)
	}
}

func TestReadOnlyAndInvalid(t *testing.T) {
	h := newHarness(t)
	h.addShop(t, engine.ModeRO)
	cases := map[string]string{
		"DELETE FROM users":  `error[read_only]: connection "shop" is read-only; only statements that read data are allowed. Use a read-write connection to change data`,
		"SELECT 1; SELECT 2": "error[invalid]: only one statement per call is supported. Send each statement in its own query call",
		"   ":                "error[invalid]: query is empty",
		"BEGIN":              "error[invalid]: BEGIN statements are not supported. Each query call runs in its own transaction",
	}
	for q, want := range cases {
		if _, err := h.query(t, QueryInput{Query: q}); errText(err) != want {
			t.Errorf("%q: got %q", q, errText(err))
		}
	}
}

func TestUnknownSource(t *testing.T) {
	h := newHarness(t)
	_, err := h.query(t, QueryInput{Source: "nope", Query: "SELECT 1"})
	if want := "error[unknown_source]: no connection named \"nope\". No connections exist yet. Add one with `qh conn add`"; errText(err) != want {
		t.Fatalf("got %q", errText(err))
	}
	h.addShop(t, engine.ModeRO)
	_, err = h.query(t, QueryInput{Source: "nope", Query: "SELECT 1"})
	if want := `error[unknown_source]: no connection named "nope". Available: shop`; errText(err) != want {
		t.Fatalf("got %q", errText(err))
	}
}

func TestSuggestions(t *testing.T) {
	h := newHarness(t)
	h.addShop(t, engine.ModeRO)
	_, err := h.query(t, QueryInput{Query: "SELECT emial FROM users"})
	if !strings.Contains(errText(err), "Did you mean users.email?") {
		t.Errorf("column: %q", errText(err))
	}
	_, err = h.query(t, QueryInput{Query: "SELECT * FROM user"})
	if !strings.Contains(errText(err), "Did you mean users?") {
		t.Errorf("table: %q", errText(err))
	}
}

func TestSchema(t *testing.T) {
	h := newHarness(t)
	h.addShop(t, engine.ModeRO)
	ctx := context.Background()
	out, err := h.svc.Schema(ctx, ws, SchemaInput{Source: "shop"})
	if err != nil || !strings.HasPrefix(out, "main: 3 tables, 1 view\n") {
		t.Fatalf("overview %q, %v", out, err)
	}
	out, _ = h.svc.Schema(ctx, ws, SchemaInput{Source: "shop", Object: "orders"})
	if !strings.Contains(out, "  user_id integer NN → users.id\n") {
		t.Fatalf("detail %q", out)
	}
	out, _ = h.svc.Schema(ctx, ws, SchemaInput{Source: "shop", Pattern: "ordr"})
	if out != "-- no objects match \"ordr\". Did you mean orders?\n" {
		t.Fatalf("no match %q", out)
	}
	n, _, err := h.svc.RefreshSchema(ctx, ws, "shop")
	if err != nil || n != 4 {
		t.Fatalf("refresh = %d, %v", n, err)
	}
}

func TestMaxRows(t *testing.T) {
	h := newHarness(t)
	h.addShop(t, engine.ModeRO)
	out, _ := h.query(t, QueryInput{Query: "SELECT id FROM orders ORDER BY id", MaxRows: 2})
	if !strings.HasPrefix(out, "id\n1\n2\n-- 2 rows shown, more exist (truncated) · ") {
		t.Fatalf("got %q", out)
	}
	out, _ = h.query(t, QueryInput{Query: "SELECT id FROM orders ORDER BY id", MaxRows: 5000})
	if !strings.Contains(out, "-- 100 rows shown, more exist (truncated)") {
		t.Fatalf("request above the connection cap: %q", out[len(out)-60:])
	}
}

func TestDDLInvalidatesSchemaCache(t *testing.T) {
	h := newHarness(t)
	h.addShop(t, engine.ModeRW)
	ctx := context.Background()
	if out, _ := h.svc.Schema(ctx, ws, SchemaInput{Source: "shop"}); strings.Contains(out, "extra") {
		t.Fatal("extra exists before CREATE")
	}
	if _, err := h.query(t, QueryInput{Query: "CREATE TABLE extra (x int)"}); err != nil {
		t.Fatal(err)
	}
	if out, _ := h.svc.Schema(ctx, ws, SchemaInput{Source: "shop"}); !strings.Contains(out, "extra") {
		t.Fatalf("cache not invalidated: %q", out)
	}
}

func TestExplain(t *testing.T) {
	h := newHarness(t)
	h.addShop(t, engine.ModeRO)
	out, err := h.query(t, QueryInput{Query: "DELETE FROM users WHERE id = 1", Explain: true})
	if err != nil || !strings.HasPrefix(out, "plan\n") {
		t.Fatalf("explain of a write on ro: %q, %v", out, err)
	}
}
```

`internal/core/connections_test.go`:
```go
package core

import (
	"bytes"
	"context"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/anderson-0/qh/internal/engine"
	"github.com/anderson-0/qh/internal/qerr"
	"github.com/anderson-0/qh/internal/sqlx"
	"github.com/anderson-0/qh/internal/testutil"
)

// leakyEngine puts its connection string and password into every error.
type leakyEngine struct{}

func (leakyEngine) Name() string          { return "leaky" }
func (leakyEngine) Dialect() sqlx.Dialect { return sqlx.Postgres }
func (leakyEngine) Inspect(dsn string) (engine.DSNInfo, error) {
	u, err := url.Parse(dsn)
	if err != nil || u.User == nil {
		return engine.DSNInfo{}, qerr.New(qerr.Invalid, "bad leaky dsn")
	}
	pw, _ := u.User.Password()
	return engine.DSNInfo{Host: u.Host, User: u.User.Username(), Password: pw, Redacted: "leaky://" + u.User.Username() + ":***@" + u.Host}, nil
}
func (leakyEngine) Open(_ context.Context, s engine.ConnSpec) (engine.Conn, error) {
	return nil, qerr.New(qerr.ConnectionFailed, "cannot connect with "+s.DSN+" (password hunter22)")
}

func init() { engine.Register(leakyEngine{}) }

func TestSecretsAreSealedAndScrubbed(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	dsn := "leaky://bob:hunter22@db.example.com"
	_, _, err := h.svc.CreateConnection(ctx, ws, ConnInput{Name: "leaky", Engine: "leaky", DSN: dsn}, true)
	if err == nil || strings.Contains(err.Error(), "hunter22") || !strings.Contains(err.Error(), "***") {
		t.Fatalf("test error not scrubbed: %v", err)
	}
	if _, _, err := h.svc.CreateConnection(ctx, ws, ConnInput{Name: "leaky", Engine: "leaky", DSN: dsn}, false); err != nil {
		t.Fatal(err)
	}
	rec, _ := h.st.GetConnection(ctx, ws, "leaky")
	if bytes.Contains(rec.Secret, []byte("hunter22")) {
		t.Fatal("password stored in plaintext")
	}
	info, _ := h.svc.GetConnection(ctx, ws, "leaky")
	if info.DSN != "leaky://bob:***@db.example.com" || info.Host != "db.example.com" || info.User != "bob" {
		t.Fatalf("info = %+v", info)
	}
	_, err = h.svc.Query(ctx, ws, QueryInput{Source: "leaky", Query: "SELECT 1"})
	if err == nil || strings.Contains(err.Error(), "hunter22") || !strings.HasPrefix(err.Error(), "error[connection_failed]") {
		t.Fatalf("query error not scrubbed: %v", err)
	}
}

func TestCreateValidation(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	db := testutil.SQLiteFixture(t)
	missing := filepath.Join(t.TempDir(), "missing.db")
	cases := []struct {
		in   ConnInput
		test bool
		want string
	}{
		{ConnInput{Name: "Shop!", Engine: "sqlite", DSN: db}, true, `error[invalid]: invalid connection name "Shop!"`},
		{ConnInput{Name: "shop", Engine: "oracle", DSN: db}, true, `error[invalid]: unknown engine "oracle"`},
		{ConnInput{Name: "shop", Engine: "sqlite"}, true, "error[invalid]: connection string is required"},
		{ConnInput{Name: "shop", Engine: "sqlite", DSN: db, Mode: "admin"}, true, "error[invalid]: mode must be ro or rw"},
		{ConnInput{Name: "shop", Engine: "sqlite", DSN: db, MaxRows: 20000}, true, "error[invalid]: max_rows must be between 1 and 10000"},
		{ConnInput{Name: "shop", Engine: "sqlite", DSN: db, TimeoutMS: 400000}, true, "error[invalid]: timeout_ms must be between 1 and 300000"},
		{ConnInput{Name: "shop", Engine: "sqlite", DSN: db, PoolSize: 51}, true, "error[invalid]: pool_size must be between 1 and 50"},
		{ConnInput{Name: "gone", Engine: "sqlite", DSN: missing}, true, "error[connection_failed]: database file not found"},
	}
	for _, c := range cases {
		_, _, err := h.svc.CreateConnection(ctx, ws, c.in, c.test)
		if !strings.HasPrefix(errText(err), c.want) {
			t.Errorf("%+v: got %q, want prefix %q", c.in, errText(err), c.want)
		}
	}
	if _, _, err := h.svc.CreateConnection(ctx, ws, ConnInput{Name: "gone", Engine: "sqlite", DSN: missing}, false); err != nil {
		t.Fatalf("skip test should save: %v", err)
	}
	info, tr, err := h.svc.CreateConnection(ctx, ws, ConnInput{Name: "shop", Engine: "sqlite", DSN: db}, true)
	if err != nil || tr == nil || !strings.HasPrefix(tr.ServerVersion, "SQLite ") {
		t.Fatalf("create = %+v %+v %v", info, tr, err)
	}
	if info.Mode != engine.ModeRO || info.MaxRows != 100 || info.TimeoutMS != 30000 || info.PoolSize != 5 || info.Version != 1 {
		t.Fatalf("defaults = %+v", info)
	}
	if _, _, err := h.svc.CreateConnection(ctx, ws, ConnInput{Name: "shop", Engine: "sqlite", DSN: db}, true); errText(err) != `error[invalid]: a connection named "shop" already exists. Edit it, or choose another name` {
		t.Fatalf("duplicate: %q", errText(err))
	}
}

func TestUpdateAppliesOnNextCall(t *testing.T) {
	h := newHarness(t)
	h.addShop(t, engine.ModeRO)
	ctx := context.Background()
	if _, err := h.query(t, QueryInput{Query: "SELECT id FROM orders"}); err != nil {
		t.Fatal(err) // opens the pool for version 1
	}
	five := 5
	info, _, err := h.svc.UpdateConnection(ctx, ws, "shop", ConnPatch{MaxRows: &five}, true)
	if err != nil || info.Version != 2 {
		t.Fatalf("update = %+v, %v", info, err)
	}
	out, _ := h.query(t, QueryInput{Query: "SELECT id FROM orders ORDER BY id"})
	if !strings.Contains(out, "-- 5 rows shown") {
		t.Fatalf("new max_rows not applied: %q", out)
	}
	rw := engine.ModeRW
	if _, _, err := h.svc.UpdateConnection(ctx, ws, "shop", ConnPatch{Mode: &rw}, true); err != nil {
		t.Fatal(err)
	}
	out, err = h.query(t, QueryInput{Query: "DELETE FROM order_items WHERE order_id = 1"})
	if err != nil || !strings.HasPrefix(out, "-- 2 rows affected") {
		t.Fatalf("write after switching to rw: %q, %v", out, err)
	}
	if _, _, err := h.svc.UpdateConnection(ctx, ws, "nope", ConnPatch{}, true); !strings.HasPrefix(errText(err), "error[unknown_source]") {
		t.Fatalf("update missing: %v", err)
	}
}

func TestDeleteAndList(t *testing.T) {
	h := newHarness(t)
	h.addShop(t, engine.ModeRO)
	ctx := context.Background()
	list, err := h.svc.ListConnections(ctx, ws)
	if err != nil || len(list) != 1 || list[0].Name != "shop" || list[0].DSN == "" {
		t.Fatalf("list = %+v, %v", list, err)
	}
	if tr, err := h.svc.TestConnection(ctx, ws, "shop"); err != nil || tr.ServerVersion == "" {
		t.Fatalf("test = %+v, %v", tr, err)
	}
	if err := h.svc.DeleteConnection(ctx, ws, "shop"); err != nil {
		t.Fatal(err)
	}
	if _, err := h.query(t, QueryInput{Query: "SELECT 1"}); !strings.HasPrefix(errText(err), "error[unknown_source]") {
		t.Fatalf("query after delete: %v", err)
	}
	if err := h.svc.DeleteConnection(ctx, ws, "shop"); !strings.HasPrefix(errText(err), "error[unknown_source]") {
		t.Fatalf("delete twice: %v", err)
	}
}

func TestAllowedNamespaces(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	_, _, err := h.svc.CreateConnection(ctx, ws, ConnInput{
		Name: "shop", Engine: "sqlite", DSN: testutil.SQLiteFixture(t), AllowedNamespaces: []string{"other", " "},
	}, true)
	if err != nil {
		t.Fatal(err)
	}
	if out, _ := h.svc.Schema(ctx, ws, SchemaInput{Source: "shop"}); out != "-- no tables or views found\n" {
		t.Fatalf("got %q", out)
	}
	info, _ := h.svc.GetConnection(ctx, ws, "shop")
	if len(info.AllowedNamespaces) != 1 || info.AllowedNamespaces[0] != "other" {
		t.Fatalf("namespaces = %v", info.AllowedNamespaces)
	}
}
```

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./internal/core/`
Expected: FAIL — `undefined: New`.

- [ ] **Step 3: Implement the agent operations**

`internal/core/service.go`:
```go
// Package core applies qh's policy (read-only enforcement, row caps,
// suggestions, secret scrubbing) on top of the engines. The MCP server and
// the CLI are thin adapters over Service.
package core

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/anderson-0/qh/internal/catalog"
	"github.com/anderson-0/qh/internal/engine"
	"github.com/anderson-0/qh/internal/format"
	"github.com/anderson-0/qh/internal/pool"
	"github.com/anderson-0/qh/internal/qerr"
	"github.com/anderson-0/qh/internal/secret"
	"github.com/anderson-0/qh/internal/sqlx"
	"github.com/anderson-0/qh/internal/store"
	"github.com/anderson-0/qh/internal/suggest"
)

// Service implements the agent tools and connection management.
type Service struct {
	store store.Store
	keys  *secret.Keyring
	pools *pool.Manager
	cache *catalog.Cache
}

// New returns a Service.
func New(st store.Store, keys *secret.Keyring, pools *pool.Manager, cache *catalog.Cache) *Service {
	return &Service{store: st, keys: keys, pools: pools, cache: cache}
}

// OpenConn opens spec with its registered engine. It is the production pool.Opener.
func OpenConn(ctx context.Context, spec engine.ConnSpec) (engine.Conn, error) {
	e, err := engine.Get(spec.Engine)
	if err != nil {
		return nil, err
	}
	return e.Open(ctx, spec)
}

// target is a stored connection with its secret opened.
type target struct {
	rec  *store.Connection
	eng  engine.Engine
	spec engine.ConnSpec
	info engine.DSNInfo
}

func (t *target) scrub(err error) error { return scrub(err, secretsOf(t.spec.DSN, t.info)...) }

func (s *Service) resolve(ctx context.Context, ws, name string) (*target, error) {
	rec, err := s.store.GetConnection(ctx, ws, name)
	if errors.Is(err, store.ErrNotFound) {
		return nil, s.unknownSource(ctx, ws, name)
	}
	if err != nil {
		return nil, err
	}
	in, err := s.load(rec)
	if err != nil {
		return nil, err
	}
	eng, err := engine.Get(rec.Engine)
	if err != nil {
		return nil, err
	}
	info, _ := eng.Inspect(in.DSN)
	return &target{
		rec: rec, eng: eng, info: info,
		spec: engine.ConnSpec{ID: rec.ID, Version: rec.Version, Engine: rec.Engine, DSN: in.DSN,
			Mode: in.Mode, PoolSize: rec.PoolSize},
	}, nil
}

func (s *Service) unknownSource(ctx context.Context, ws, name string) error {
	e := qerr.New(qerr.UnknownSource, fmt.Sprintf("no connection named %q", name))
	list, err := s.store.ListConnections(ctx, ws)
	if err != nil || len(list) == 0 {
		return e.WithHint("No connections exist yet. Add one with `qh conn add`")
	}
	names := make([]string, len(list))
	for i, c := range list {
		names[i] = c.Name
	}
	return e.WithHint("Available: " + strings.Join(names, ", "))
}

// Sources lists the workspace's connections, one per line.
func (s *Service) Sources(ctx context.Context, ws string) (string, error) {
	list, err := s.store.ListConnections(ctx, ws)
	if err != nil {
		return "", err
	}
	if len(list) == 0 {
		return "-- no connections. Add one with `qh conn add`\n", nil
	}
	var b strings.Builder
	for _, c := range list {
		fmt.Fprintf(&b, "%s %s %s", c.Name, c.Engine, c.Mode)
		if c.Description != "" {
			b.WriteString(" — " + c.Description)
		}
		b.WriteByte('\n')
	}
	return b.String(), nil
}

// SchemaInput is the schema tool's input.
type SchemaInput struct {
	Source  string
	Pattern string
	Object  string
}

// Schema renders the connection's schema overview or object details.
func (s *Service) Schema(ctx context.Context, ws string, in SchemaInput) (string, error) {
	t, err := s.resolve(ctx, ws, in.Source)
	if err != nil {
		return "", err
	}
	conn, release, err := s.pools.Get(ctx, t.spec)
	if err != nil {
		return "", t.scrub(err)
	}
	defer release()
	cat, err := s.catalog(ctx, conn, t)
	if err != nil {
		return "", t.scrub(err)
	}
	out, n := format.Schema(cat, format.SchemaOpts{Pattern: in.Pattern, Object: in.Object})
	if n == 0 && (strings.TrimSpace(in.Pattern) != "" || strings.TrimSpace(in.Object) != "") {
		what := strings.TrimSpace(in.Object)
		if what == "" {
			what = strings.TrimSpace(in.Pattern)
		}
		return fmt.Sprintf("-- no objects match %q. %s\n", what, suggest.TableHint(cat, strings.Trim(what, "*"))), nil
	}
	return out, nil
}

// QueryInput is the query tool's input.
type QueryInput struct {
	Source  string
	Query   string
	MaxRows int
	Explain bool
}

// Query runs one statement and renders the result as TSV.
func (s *Service) Query(ctx context.Context, ws string, in QueryInput) (string, error) {
	t, err := s.resolve(ctx, ws, in.Source)
	if err != nil {
		return "", err
	}
	access, err := sqlx.Classify(in.Query, t.eng.Dialect())
	if err != nil {
		return "", err
	}
	readOnly := t.spec.Mode != engine.ModeRW
	if !in.Explain && readOnly && access != sqlx.Read {
		return "", qerr.New(qerr.ReadOnly, fmt.Sprintf("connection %q is read-only; only statements that read data are allowed", t.rec.Name)).
			WithHint("Use a read-write connection to change data")
	}
	conn, release, err := s.pools.Get(ctx, t.spec)
	if err != nil {
		return "", t.scrub(err)
	}
	defer release()
	opts := engine.QueryOpts{
		MaxRows:  effectiveMaxRows(t.rec.MaxRows, in.MaxRows),
		ReadOnly: readOnly,
		Timeout:  time.Duration(t.rec.TimeoutMS) * time.Millisecond,
	}
	var res *engine.Result
	if in.Explain {
		res, err = conn.Explain(ctx, in.Query, opts)
	} else {
		res, err = conn.Query(ctx, in.Query, opts)
	}
	if err != nil {
		return "", t.scrub(s.withSuggestions(ctx, conn, t, in.Query, err))
	}
	if access == sqlx.DDL && !in.Explain {
		s.cache.Invalidate(t.rec.ID)
	}
	return format.Result(res), nil
}

func effectiveMaxRows(connMax, requested int) int {
	if requested > 0 && requested < connMax {
		return requested
	}
	return connMax
}

func (s *Service) catalog(ctx context.Context, conn engine.Conn, t *target) (*engine.Catalog, error) {
	cat, err := s.cache.Get(ctx, t.rec.ID, t.rec.Version, func(ctx context.Context) (*engine.Catalog, error) {
		return conn.Introspect(ctx)
	})
	if err != nil {
		return nil, err
	}
	return cat.Only(t.rec.AllowedNamespaces), nil
}

// withSuggestions adds did-you-mean hints to unknown-column and unknown-table errors.
func (s *Service) withSuggestions(ctx context.Context, conn engine.Conn, t *target, q string, err error) error {
	e := qerr.From(err)
	if e.UnknownColumn == "" && e.UnknownTable == "" {
		return e
	}
	cat, cerr := s.catalog(ctx, conn, t)
	if cerr != nil {
		return e
	}
	out := *e
	if out.UnknownColumn != "" {
		out.Hint = suggest.ColumnHint(cat, out.UnknownColumn, sqlx.ReferencedTables(q, t.eng.Dialect()))
	} else {
		out.Hint = suggest.TableHint(cat, out.UnknownTable)
	}
	return &out
}

// secretsOf returns the strings to scrub from errors: the password, and the
// whole connection string when it contains one.
func secretsOf(dsn string, info engine.DSNInfo) []string {
	if info.Password == "" {
		return nil
	}
	return []string{dsn, info.Password}
}

// scrub returns err as a *qerr.Error with every secret replaced by ***. The
// underlying error is dropped so it cannot leak through Unwrap.
func scrub(err error, secrets ...string) error {
	if err == nil {
		return nil
	}
	e := *qerr.From(err)
	e.Err = nil
	for _, sec := range secrets {
		if len(sec) < 4 {
			continue
		}
		e.Message = strings.ReplaceAll(e.Message, sec, "***")
		e.Hint = strings.ReplaceAll(e.Hint, sec, "***")
	}
	return &e
}
```

- [ ] **Step 4: Implement connection management**

`internal/core/connections.go`:
```go
package core

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/anderson-0/qh/internal/engine"
	"github.com/anderson-0/qh/internal/qerr"
	"github.com/anderson-0/qh/internal/store"
)

const (
	DefaultMaxRows   = 100
	MaxMaxRows       = 10_000
	DefaultTimeoutMS = 30_000
	MaxTimeoutMS     = 300_000
	DefaultPoolSize  = 5
	MaxPoolSize      = 50
)

var nameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)

// ConnInput describes a connection to create.
type ConnInput struct {
	Name              string
	Engine            string
	DSN               string
	Description       string
	Mode              engine.Mode
	MaxRows           int
	TimeoutMS         int
	PoolSize          int
	AllowedNamespaces []string
}

// ConnPatch changes the non-nil fields of a connection.
type ConnPatch struct {
	DSN               *string
	Description       *string
	Mode              *engine.Mode
	MaxRows           *int
	TimeoutMS         *int
	PoolSize          *int
	AllowedNamespaces *[]string
}

// ConnInfo is what may be shown about a connection. DSN is redacted.
type ConnInfo struct {
	Name              string
	Engine            string
	Description       string
	Mode              engine.Mode
	Host              string
	Port              int
	Database          string
	User              string
	DSN               string
	MaxRows           int
	TimeoutMS         int
	PoolSize          int
	AllowedNamespaces []string
	Version           int64
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

// TestResult reports a successful connection test.
type TestResult struct {
	ServerVersion string
	Connect       time.Duration
}

func bounded(field string, v, def, maxV int) (int, error) {
	if v == 0 {
		return def, nil
	}
	if v < 0 || v > maxV {
		return 0, qerr.New(qerr.Invalid, fmt.Sprintf("%s must be between 1 and %d", field, maxV))
	}
	return v, nil
}

func normalize(in ConnInput) (ConnInput, error) {
	in.Name = strings.TrimSpace(in.Name)
	if !nameRe.MatchString(in.Name) {
		return in, qerr.New(qerr.Invalid, fmt.Sprintf("invalid connection name %q", in.Name)).
			WithHint("Use lowercase letters, digits, _ and -, starting with a letter or digit, at most 64 characters")
	}
	in.Engine = strings.ToLower(strings.TrimSpace(in.Engine))
	if _, err := engine.Get(in.Engine); err != nil {
		return in, err
	}
	in.DSN = strings.TrimSpace(in.DSN)
	if in.DSN == "" {
		return in, qerr.New(qerr.Invalid, "connection string is required")
	}
	if in.Mode == "" {
		in.Mode = engine.ModeRO
	}
	if in.Mode != engine.ModeRO && in.Mode != engine.ModeRW {
		return in, qerr.New(qerr.Invalid, "mode must be ro or rw")
	}
	var err error
	if in.MaxRows, err = bounded("max_rows", in.MaxRows, DefaultMaxRows, MaxMaxRows); err != nil {
		return in, err
	}
	if in.TimeoutMS, err = bounded("timeout_ms", in.TimeoutMS, DefaultTimeoutMS, MaxTimeoutMS); err != nil {
		return in, err
	}
	if in.PoolSize, err = bounded("pool_size", in.PoolSize, DefaultPoolSize, MaxPoolSize); err != nil {
		return in, err
	}
	var ns []string
	for _, n := range in.AllowedNamespaces {
		if n = strings.TrimSpace(n); n != "" {
			ns = append(ns, n)
		}
	}
	in.AllowedNamespaces = ns
	in.Description = strings.TrimSpace(in.Description)
	return in, nil
}

// load opens a stored connection's secret.
func (s *Service) load(rec *store.Connection) (ConnInput, error) {
	dsn, err := s.keys.Open(rec.Secret, []byte(rec.ID))
	if err != nil {
		return ConnInput{}, fmt.Errorf("connection %q: %w", rec.Name, err)
	}
	return ConnInput{
		Name: rec.Name, Engine: rec.Engine, DSN: string(dsn), Description: rec.Description,
		Mode: engine.Mode(rec.Mode), MaxRows: rec.MaxRows, TimeoutMS: rec.TimeoutMS, PoolSize: rec.PoolSize,
		AllowedNamespaces: rec.AllowedNamespaces,
	}, nil
}

// fill seals in.DSN into rec and copies the other fields.
func (s *Service) fill(rec *store.Connection, in ConnInput, info engine.DSNInfo) error {
	sealed, err := s.keys.Seal([]byte(in.DSN), []byte(rec.ID))
	if err != nil {
		return err
	}
	rec.Engine, rec.Secret, rec.SecretKeyID = in.Engine, sealed, s.keys.PrimaryID()
	rec.Host, rec.Port, rec.Database, rec.Username = info.Host, info.Port, info.Database, info.User
	rec.Description, rec.Mode = in.Description, string(in.Mode)
	rec.MaxRows, rec.TimeoutMS, rec.PoolSize = in.MaxRows, in.TimeoutMS, in.PoolSize
	rec.AllowedNamespaces = in.AllowedNamespaces
	return nil
}

func testConn(ctx context.Context, eng engine.Engine, in ConnInput, info engine.DSNInfo) (*TestResult, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	start := time.Now()
	conn, err := eng.Open(ctx, engine.ConnSpec{ID: "test", Engine: in.Engine, DSN: in.DSN, Mode: in.Mode, PoolSize: 1})
	if err != nil {
		return nil, scrub(err, secretsOf(in.DSN, info)...)
	}
	defer conn.Close()
	v, err := conn.Ping(ctx)
	if err != nil {
		return nil, scrub(err, secretsOf(in.DSN, info)...)
	}
	return &TestResult{ServerVersion: v, Connect: time.Since(start)}, nil
}

func toInfo(rec *store.Connection, info engine.DSNInfo) *ConnInfo {
	return &ConnInfo{
		Name: rec.Name, Engine: rec.Engine, Description: rec.Description, Mode: engine.Mode(rec.Mode),
		Host: rec.Host, Port: rec.Port, Database: rec.Database, User: rec.Username, DSN: info.Redacted,
		MaxRows: rec.MaxRows, TimeoutMS: rec.TimeoutMS, PoolSize: rec.PoolSize,
		AllowedNamespaces: rec.AllowedNamespaces, Version: rec.Version,
		CreatedAt: rec.CreatedAt, UpdatedAt: rec.UpdatedAt,
	}
}

// CreateConnection validates, optionally tests, seals and stores a connection.
func (s *Service) CreateConnection(ctx context.Context, ws string, in ConnInput, test bool) (*ConnInfo, *TestResult, error) {
	in, err := normalize(in)
	if err != nil {
		return nil, nil, err
	}
	eng, _ := engine.Get(in.Engine)
	info, err := eng.Inspect(in.DSN)
	if err != nil {
		return nil, nil, scrub(err, in.DSN)
	}
	var tr *TestResult
	if test {
		if tr, err = testConn(ctx, eng, in, info); err != nil {
			return nil, nil, err
		}
	}
	rec := &store.Connection{ID: store.NewID(), WorkspaceID: ws, Name: in.Name}
	if err := s.fill(rec, in, info); err != nil {
		return nil, nil, err
	}
	if err := s.store.CreateConnection(ctx, rec); err != nil {
		if errors.Is(err, store.ErrExists) {
			return nil, nil, qerr.New(qerr.Invalid, fmt.Sprintf("a connection named %q already exists", in.Name)).
				WithHint("Edit it, or choose another name")
		}
		return nil, nil, err
	}
	return toInfo(rec, info), tr, nil
}

// UpdateConnection applies p. It tests the connection again when the
// connection string or mode changes and test is true.
func (s *Service) UpdateConnection(ctx context.Context, ws, name string, p ConnPatch, test bool) (*ConnInfo, *TestResult, error) {
	rec, err := s.store.GetConnection(ctx, ws, name)
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil, s.unknownSource(ctx, ws, name)
	}
	if err != nil {
		return nil, nil, err
	}
	in, err := s.load(rec)
	if err != nil {
		return nil, nil, err
	}
	if p.DSN != nil {
		in.DSN = *p.DSN
	}
	if p.Description != nil {
		in.Description = *p.Description
	}
	if p.Mode != nil {
		in.Mode = *p.Mode
	}
	if p.MaxRows != nil {
		in.MaxRows = *p.MaxRows
	}
	if p.TimeoutMS != nil {
		in.TimeoutMS = *p.TimeoutMS
	}
	if p.PoolSize != nil {
		in.PoolSize = *p.PoolSize
	}
	if p.AllowedNamespaces != nil {
		in.AllowedNamespaces = *p.AllowedNamespaces
	}
	if in, err = normalize(in); err != nil {
		return nil, nil, err
	}
	eng, _ := engine.Get(in.Engine)
	info, err := eng.Inspect(in.DSN)
	if err != nil {
		return nil, nil, scrub(err, in.DSN)
	}
	var tr *TestResult
	if test && (p.DSN != nil || p.Mode != nil) {
		if tr, err = testConn(ctx, eng, in, info); err != nil {
			return nil, nil, err
		}
	}
	if err := s.fill(rec, in, info); err != nil {
		return nil, nil, err
	}
	if err := s.store.UpdateConnection(ctx, rec); err != nil {
		if errors.Is(err, store.ErrConflict) {
			return nil, nil, qerr.New(qerr.Invalid, "the connection changed while it was being updated").WithHint("Retry")
		}
		return nil, nil, err
	}
	s.cache.Invalidate(rec.ID)
	return toInfo(rec, info), tr, nil
}

// DeleteConnection removes a connection and closes its pool.
func (s *Service) DeleteConnection(ctx context.Context, ws, name string) error {
	rec, err := s.store.GetConnection(ctx, ws, name)
	if errors.Is(err, store.ErrNotFound) {
		return s.unknownSource(ctx, ws, name)
	}
	if err != nil {
		return err
	}
	if err := s.store.DeleteConnection(ctx, ws, name); err != nil {
		return err
	}
	s.pools.Drop(rec.ID)
	s.cache.Invalidate(rec.ID)
	return nil
}

func (s *Service) info(rec *store.Connection) *ConnInfo {
	var di engine.DSNInfo
	if in, err := s.load(rec); err == nil {
		if eng, err := engine.Get(rec.Engine); err == nil {
			di, _ = eng.Inspect(in.DSN)
		}
	}
	if di.Redacted == "" {
		di.Redacted = "<cannot decrypt with the current master key>"
	}
	return toInfo(rec, di)
}

// GetConnection returns one connection.
func (s *Service) GetConnection(ctx context.Context, ws, name string) (*ConnInfo, error) {
	rec, err := s.store.GetConnection(ctx, ws, name)
	if errors.Is(err, store.ErrNotFound) {
		return nil, s.unknownSource(ctx, ws, name)
	}
	if err != nil {
		return nil, err
	}
	return s.info(rec), nil
}

// ListConnections returns the workspace's connections ordered by name.
func (s *Service) ListConnections(ctx context.Context, ws string) ([]*ConnInfo, error) {
	recs, err := s.store.ListConnections(ctx, ws)
	if err != nil {
		return nil, err
	}
	out := make([]*ConnInfo, len(recs))
	for i, rec := range recs {
		out[i] = s.info(rec)
	}
	return out, nil
}

// TestConnection connects with a stored connection's settings.
func (s *Service) TestConnection(ctx context.Context, ws, name string) (*TestResult, error) {
	t, err := s.resolve(ctx, ws, name)
	if err != nil {
		return nil, err
	}
	return testConn(ctx, t.eng, ConnInput{Engine: t.rec.Engine, DSN: t.spec.DSN, Mode: t.spec.Mode}, t.info)
}

// RefreshSchema rebuilds a connection's cached schema.
func (s *Service) RefreshSchema(ctx context.Context, ws, name string) (int, time.Duration, error) {
	t, err := s.resolve(ctx, ws, name)
	if err != nil {
		return 0, 0, err
	}
	start := time.Now()
	s.cache.Invalidate(t.rec.ID)
	conn, release, err := s.pools.Get(ctx, t.spec)
	if err != nil {
		return 0, 0, t.scrub(err)
	}
	defer release()
	cat, err := s.catalog(ctx, conn, t)
	if err != nil {
		return 0, 0, t.scrub(err)
	}
	return cat.Count(), time.Since(start), nil
}
```

- [ ] **Step 5: Run the tests**

Run: `go test -race ./internal/core/`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/core
git commit -m "feat: add core service with read-only policy, suggestions and connection management"
```

---

### Task 14: MCP server

**Files:**
- Create: `internal/mcpserver/server.go`
- Test: `internal/mcpserver/server_test.go`

**Interfaces:**
- Consumes: `core.Service` (`Sources`, `Schema`, `Query`), `qerr.From`.
- Produces: `type mcpserver.WorkspaceFunc func(ctx context.Context) string`; `mcpserver.New(svc *core.Service, ws WorkspaceFunc, version string) *mcp.Server` with tools `sources`, `schema`, `query`.

- [ ] **Step 1: Add the SDK**

Run: `go get github.com/modelcontextprotocol/go-sdk@v1.8.0`

- [ ] **Step 2: Write the failing test**

`internal/mcpserver/server_test.go`:
```go
package mcpserver

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/anderson-0/qh/internal/catalog"
	"github.com/anderson-0/qh/internal/core"
	_ "github.com/anderson-0/qh/internal/engine/sqlite"
	"github.com/anderson-0/qh/internal/pool"
	"github.com/anderson-0/qh/internal/secret"
	"github.com/anderson-0/qh/internal/store"
	"github.com/anderson-0/qh/internal/testutil"
)

func connect(t *testing.T) *mcp.ClientSession {
	t.Helper()
	ctx := context.Background()
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "qh.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	key := make([]byte, secret.KeySize)
	rand.Read(key)
	keys, _ := secret.NewKeyring(key)
	pools := pool.New(core.OpenConn, pool.Options{})
	t.Cleanup(pools.Close)
	svc := core.New(st, keys, pools, catalog.New(10*time.Minute))
	if _, _, err := svc.CreateConnection(ctx, store.DefaultWorkspace, core.ConnInput{
		Name: "shop", Engine: "sqlite", DSN: testutil.SQLiteFixture(t),
	}, true); err != nil {
		t.Fatal(err)
	}
	server := New(svc, func(context.Context) string { return store.DefaultWorkspace }, "test")
	serverT, clientT := mcp.NewInMemoryTransports()
	if _, err := server.Connect(ctx, serverT, nil); err != nil {
		t.Fatal(err)
	}
	session, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil).Connect(ctx, clientT, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { session.Close() })
	return session
}

func call(t *testing.T, s *mcp.ClientSession, name string, args map[string]any) (string, bool) {
	t.Helper()
	res, err := s.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return res.Content[0].(*mcp.TextContent).Text, res.IsError
}

func TestToolList(t *testing.T) {
	s := connect(t)
	res, err := s.ListTools(context.Background(), &mcp.ListToolsParams{})
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tool := range res.Tools {
		names = append(names, tool.Name)
	}
	if strings.Join(names, ",") != "query,schema,sources" && strings.Join(names, ",") != "sources,schema,query" {
		t.Fatalf("tools = %v", names)
	}
	b, _ := json.Marshal(res.Tools)
	if len(b) > 2400 {
		t.Fatalf("tool definitions are %d bytes, budget 2400 (~800 tokens):\n%s", len(b), b)
	}
}

func TestTools(t *testing.T) {
	s := connect(t)
	if out, isErr := call(t, s, "sources", map[string]any{}); isErr || out != "shop sqlite ro\n" {
		t.Fatalf("sources = %q", out)
	}
	if out, isErr := call(t, s, "query", map[string]any{"source": "shop", "query": "SELECT count(*) AS n FROM orders"}); isErr || !strings.HasPrefix(out, "n\n250\n-- 1 row · ") {
		t.Fatalf("query = %q", out)
	}
	if out, isErr := call(t, s, "query", map[string]any{"source": "shop", "query": "DELETE FROM users"}); !isErr || !strings.HasPrefix(out, "error[read_only]") {
		t.Fatalf("write = %q (isError %v)", out, isErr)
	}
	if out, isErr := call(t, s, "schema", map[string]any{"source": "shop", "object": "orders"}); isErr || !strings.Contains(out, "→ users.id") {
		t.Fatalf("schema = %q", out)
	}
	if out, isErr := call(t, s, "query", map[string]any{"source": "shop", "query": "SELECT id FROM orders", "max_rows": 2}); isErr || !strings.Contains(out, "-- 2 rows shown") {
		t.Fatalf("max_rows = %q", out)
	}
}
```

- [ ] **Step 3: Run it to verify it fails**

Run: `go test ./internal/mcpserver/`
Expected: FAIL — `undefined: New`.

- [ ] **Step 4: Implement**

`internal/mcpserver/server.go`:
```go
// Package mcpserver exposes the core service as three MCP tools.
package mcpserver

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/anderson-0/qh/internal/core"
	"github.com/anderson-0/qh/internal/qerr"
)

const instructions = "Use sources to pick a connection, schema to learn tables, columns, keys and joins, then query. " +
	"Prefer schema with a pattern over guessing names."

// WorkspaceFunc returns the workspace a request belongs to.
type WorkspaceFunc func(ctx context.Context) string

// SchemaArgs is the schema tool's input.
type SchemaArgs struct {
	Source  string `json:"source" jsonschema:"connection name from sources"`
	Pattern string `json:"pattern,omitempty" jsonschema:"name filter; * is a wildcard, e.g. *order*"`
	Object  string `json:"object,omitempty" jsonschema:"exact table, e.g. orders or public.orders"`
}

// QueryArgs is the query tool's input.
type QueryArgs struct {
	Source  string `json:"source" jsonschema:"connection name from sources"`
	Query   string `json:"query" jsonschema:"one statement in the source's language (SQL)"`
	MaxRows int    `json:"max_rows,omitempty" jsonschema:"row cap, at most the connection's limit"`
	Explain bool   `json:"explain,omitempty" jsonschema:"show the plan without running"`
}

// New returns an MCP server with the sources, schema and query tools.
func New(svc *core.Service, ws WorkspaceFunc, version string) *mcp.Server {
	s := mcp.NewServer(&mcp.Implementation{Name: "qh", Version: version}, &mcp.ServerOptions{Instructions: instructions})
	closed := false
	readOnly := &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: &closed}

	mcp.AddTool(s, &mcp.Tool{
		Name:        "sources",
		Description: "List database connections: name, engine, ro/rw, description.",
		Annotations: readOnly,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, any, error) {
		return text(svc.Sources(ctx, ws(ctx)))
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "schema",
		Description: "Describe tables. No pattern/object: overview with row estimates. " +
			"With pattern or object: columns, types, PK, NN (not null), FKs (→ references, ← referenced by), enum values, indexes.",
		Annotations: readOnly,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, a SchemaArgs) (*mcp.CallToolResult, any, error) {
		return text(svc.Schema(ctx, ws(ctx), core.SchemaInput{Source: a.Source, Pattern: a.Pattern, Object: a.Object}))
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "query",
		Description: "Run one statement; returns TSV (default cap 100 rows; the footer says when rows were cut). " +
			"Read-only connections reject writes. explain=true returns the plan without running.",
		Annotations: &mcp.ToolAnnotations{OpenWorldHint: &closed},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, a QueryArgs) (*mcp.CallToolResult, any, error) {
		return text(svc.Query(ctx, ws(ctx), core.QueryInput{Source: a.Source, Query: a.Query, MaxRows: a.MaxRows, Explain: a.Explain}))
	})
	return s
}

// text turns a service result into tool content; errors become isError results
// so the agent can read them and retry.
func text(out string, err error) (*mcp.CallToolResult, any, error) {
	if err != nil {
		return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: qerr.From(err).Error()}}}, nil, nil
	}
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: out}}}, nil, nil
}
```

- [ ] **Step 5: Run it to verify it passes**

Run: `go test -race ./internal/mcpserver/`
Expected: PASS. If `TestToolList` fails on size, shorten tool and parameter descriptions; do not raise the budget.

- [ ] **Step 6: Commit**

```bash
git add go.mod go.sum internal/mcpserver
git commit -m "feat: expose sources, schema and query as MCP tools"
```

---

### Task 15: CLI commands

**Files:**
- Modify: `internal/cli/root.go` (replace `newRoot`)
- Create: `internal/cli/app.go`, `internal/cli/engines.go`, `internal/cli/conn.go`, `internal/cli/query.go`
- Test: `internal/cli/cli_test.go`

**Interfaces:**
- Consumes: everything above.
- Produces: commands `qh version`, `qh mcp`, `qh conn add|ls|show|edit|rm|test`, `qh query`, `qh schema`; global `--home`. Data directory: `--home`, else `$QH_HOME`, else `$XDG_CONFIG_HOME/qh`, else `~/.config/qh`. Master key: `$QH_MASTER_KEY` (base64) if set, else `<home>/master.key` (created with mode 0600).

- [ ] **Step 1: Write the failing tests**

`internal/cli/cli_test.go`:
```go
package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/anderson-0/qh/internal/testutil"
)

func run(t *testing.T, home, stdin string, args ...string) (string, string, int) {
	t.Helper()
	var out, errOut bytes.Buffer
	code := Execute("test", append([]string{"--home", home}, args...), strings.NewReader(stdin), &out, &errOut)
	return out.String(), errOut.String(), code
}

func TestConnLifecycle(t *testing.T) {
	home, db := t.TempDir(), testutil.SQLiteFixture(t)
	out, errOut, code := run(t, home, "", "conn", "add", "shop", "--engine", "sqlite", "--dsn", db, "--description", "Shop DB")
	if code != 0 || !strings.HasPrefix(out, "added shop (sqlite, ro) · SQLite ") {
		t.Fatalf("add: %d %q %q", code, out, errOut)
	}
	if out, _, _ = run(t, home, "", "conn", "ls"); !strings.Contains(out, "shop") || !strings.Contains(out, "Shop DB") || !strings.HasPrefix(out, "NAME") {
		t.Fatalf("ls: %q", out)
	}
	if out, _, _ = run(t, home, "", "query", "shop", "SELECT id FROM users ORDER BY id"); !strings.HasPrefix(out, "id\n1\n2\n3\n-- 3 rows · ") {
		t.Fatalf("query: %q", out)
	}
	if _, errOut, code = run(t, home, "", "query", "shop", "DELETE FROM users"); code != 1 || !strings.HasPrefix(errOut, "error[read_only]") {
		t.Fatalf("write on ro: %d %q", code, errOut)
	}
	if out, _, _ = run(t, home, "", "schema", "shop", "users"); !strings.Contains(out, "main.users") {
		t.Fatalf("schema: %q", out)
	}
	if out, _, _ = run(t, home, "", "conn", "edit", "shop", "--mode", "rw"); out != "updated shop (version 2)\n" {
		t.Fatalf("edit: %q", out)
	}
	if out, _, _ = run(t, home, "", "query", "shop", "DELETE FROM order_items WHERE order_id = 1"); !strings.HasPrefix(out, "-- 2 rows affected") {
		t.Fatalf("write on rw: %q", out)
	}
	if out, _, _ = run(t, home, "", "conn", "show", "shop"); !strings.Contains(out, "mode: rw\n") || !strings.Contains(out, "version: 2\n") {
		t.Fatalf("show: %q", out)
	}
	if out, _, _ = run(t, home, "", "conn", "test", "shop"); !strings.HasPrefix(out, "ok · SQLite ") {
		t.Fatalf("test: %q", out)
	}
	if out, _, _ = run(t, home, "", "conn", "rm", "shop"); out != "removed shop\n" {
		t.Fatalf("rm: %q", out)
	}
	if _, errOut, code = run(t, home, "", "query", "shop", "SELECT 1"); code != 1 || !strings.HasPrefix(errOut, "error[unknown_source]") {
		t.Fatalf("after rm: %d %q", code, errOut)
	}
}

func TestDSNFromStdinAndKeyFile(t *testing.T) {
	home, db := t.TempDir(), testutil.SQLiteFixture(t)
	if out, errOut, code := run(t, home, db+"\n", "conn", "add", "shop", "--engine", "sqlite", "--dsn", "-"); code != 0 {
		t.Fatalf("add from stdin: %q %q", out, errOut)
	}
	info, err := os.Stat(filepath.Join(home, "master.key"))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("master.key: %v %v", info, err)
	}
	if _, err := os.Stat(filepath.Join(home, "qh.db")); err != nil {
		t.Fatal(err)
	}
}

func TestMissingFlags(t *testing.T) {
	home := t.TempDir()
	if _, errOut, code := run(t, home, "", "conn", "add", "shop", "--dsn", "x"); code != 1 || !strings.HasPrefix(errOut, "error[invalid]: --engine is required") {
		t.Fatalf("%d %q", code, errOut)
	}
	if _, errOut, code := run(t, home, "", "conn", "add", "shop", "--engine", "sqlite"); code != 1 || !strings.HasPrefix(errOut, "error[invalid]: --dsn is required") {
		t.Fatalf("%d %q", code, errOut)
	}
}
```

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./internal/cli/`
Expected: FAIL — `unknown command "conn"`, or a compile error for missing helpers.

- [ ] **Step 3: Implement**

`internal/cli/root.go` (replace the whole file):
```go
// Package cli implements the qh command line.
package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"
)

// Execute runs the qh command line and returns the process exit code.
func Execute(version string, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	root := newRoot(version)
	root.SetArgs(args)
	root.SetIn(stdin)
	root.SetOut(stdout)
	root.SetErr(stderr)
	if err := root.ExecuteContext(ctx); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}

// withApp wraps a command so it runs with an opened app.
type withApp func(fn func(cmd *cobra.Command, a *app, args []string) error) func(*cobra.Command, []string) error

func newRoot(version string) *cobra.Command {
	var home string
	root := &cobra.Command{
		Use:           "qh",
		Short:         "Token-efficient database server for AI agents",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.PersistentFlags().StringVar(&home, "home", "", "data directory (default $QH_HOME, else $XDG_CONFIG_HOME/qh or ~/.config/qh)")
	with := func(fn func(cmd *cobra.Command, a *app, args []string) error) func(*cobra.Command, []string) error {
		return func(cmd *cobra.Command, args []string) error {
			dir, err := homeDir(home)
			if err != nil {
				return err
			}
			a, err := openApp(dir)
			if err != nil {
				return err
			}
			defer a.Close()
			return fn(cmd, a, args)
		}
	}
	root.AddCommand(
		&cobra.Command{
			Use:   "version",
			Short: "Print the version",
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, _ []string) error {
				fmt.Fprintln(cmd.OutOrStdout(), version)
				return nil
			},
		},
		mcpCmd(with, version),
		connCmd(with),
		queryCmd(with),
		schemaCmd(with),
	)
	return root
}
```

`internal/cli/engines.go`:
```go
package cli

// Engines register themselves in init.
import (
	_ "github.com/anderson-0/qh/internal/engine/postgres"
	_ "github.com/anderson-0/qh/internal/engine/sqlite"
)
```

`internal/cli/app.go`:
```go
package cli

import (
	"os"
	"path/filepath"
	"time"

	"github.com/anderson-0/qh/internal/catalog"
	"github.com/anderson-0/qh/internal/core"
	"github.com/anderson-0/qh/internal/pool"
	"github.com/anderson-0/qh/internal/secret"
	"github.com/anderson-0/qh/internal/store"
)

type app struct {
	svc   *core.Service
	pools *pool.Manager
	st    *store.SQLite
}

func homeDir(flagValue string) (string, error) {
	if flagValue != "" {
		return flagValue, nil
	}
	if v := os.Getenv("QH_HOME"); v != "" {
		return v, nil
	}
	if v := os.Getenv("XDG_CONFIG_HOME"); v != "" {
		return filepath.Join(v, "qh"), nil
	}
	h, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(h, ".config", "qh"), nil
}

func openApp(home string) (*app, error) {
	if err := os.MkdirAll(home, 0o700); err != nil {
		return nil, err
	}
	var key []byte
	var err error
	if v := os.Getenv("QH_MASTER_KEY"); v != "" {
		key, err = secret.ParseKey(v)
	} else {
		key, err = secret.LoadOrCreateKeyFile(filepath.Join(home, "master.key"))
	}
	if err != nil {
		return nil, err
	}
	keys, err := secret.NewKeyring(key)
	if err != nil {
		return nil, err
	}
	st, err := store.OpenSQLite(filepath.Join(home, "qh.db"))
	if err != nil {
		return nil, err
	}
	pools := pool.New(core.OpenConn, pool.Options{})
	return &app{svc: core.New(st, keys, pools, catalog.New(10*time.Minute)), pools: pools, st: st}, nil
}

func (a *app) Close() {
	a.pools.Close()
	a.st.Close()
}
```

`internal/cli/conn.go`:
```go
package cli

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/anderson-0/qh/internal/core"
	"github.com/anderson-0/qh/internal/engine"
	"github.com/anderson-0/qh/internal/qerr"
	"github.com/anderson-0/qh/internal/store"
)

const ws = store.DefaultWorkspace

func connCmd(with withApp) *cobra.Command {
	c := &cobra.Command{Use: "conn", Short: "Manage database connections"}
	c.AddCommand(connAddCmd(with), connListCmd(with), connShowCmd(with), connEditCmd(with), connRemoveCmd(with), connTestCmd(with))
	return c
}

type connFlags struct {
	engine, dsn, mode, description string
	maxRows, poolSize              int
	timeout                        time.Duration
	namespaces                     []string
	skipTest                       bool
}

func (f *connFlags) register(cmd *cobra.Command, withEngine bool) {
	fl := cmd.Flags()
	if withEngine {
		fl.StringVar(&f.engine, "engine", "", "postgres or sqlite (required)")
	}
	fl.StringVar(&f.dsn, "dsn", "", "connection string, or - to read it from stdin")
	fl.StringVar(&f.mode, "mode", "ro", "ro (read-only) or rw (read-write)")
	fl.StringVar(&f.description, "description", "", "what this database holds; shown to agents")
	fl.IntVar(&f.maxRows, "max-rows", 0, "row cap per query (default 100, max 10000)")
	fl.DurationVar(&f.timeout, "timeout", 0, "query timeout (default 30s, max 5m)")
	fl.IntVar(&f.poolSize, "pool-size", 0, "open database connections (default 5, max 50)")
	fl.StringSliceVar(&f.namespaces, "namespaces", nil, "only show these schemas to agents (comma-separated)")
	fl.BoolVar(&f.skipTest, "skip-test", false, "save without connecting first")
}

// readDSN returns v, or the first line of stdin when v is "-" (keeps passwords out of shell history).
func readDSN(cmd *cobra.Command, v string) (string, error) {
	if v != "-" {
		return v, nil
	}
	line, err := bufio.NewReader(cmd.InOrStdin()).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	return strings.TrimSpace(line), nil
}

func connAddCmd(with withApp) *cobra.Command {
	var f connFlags
	cmd := &cobra.Command{
		Use:   "add <name>",
		Short: "Add a connection (tests it first unless --skip-test)",
		Args:  cobra.ExactArgs(1),
		RunE: with(func(cmd *cobra.Command, a *app, args []string) error {
			if f.engine == "" {
				return qerr.New(qerr.Invalid, "--engine is required")
			}
			if f.dsn == "" {
				return qerr.New(qerr.Invalid, "--dsn is required")
			}
			dsn, err := readDSN(cmd, f.dsn)
			if err != nil {
				return err
			}
			info, tr, err := a.svc.CreateConnection(cmd.Context(), ws, core.ConnInput{
				Name: args[0], Engine: f.engine, DSN: dsn, Description: f.description, Mode: engine.Mode(f.mode),
				MaxRows: f.maxRows, TimeoutMS: int(f.timeout.Milliseconds()), PoolSize: f.poolSize,
				AllowedNamespaces: f.namespaces,
			}, !f.skipTest)
			if err != nil {
				return err
			}
			out := fmt.Sprintf("added %s (%s, %s)", info.Name, info.Engine, info.Mode)
			if tr != nil {
				out += fmt.Sprintf(" · %s · connected in %dms", tr.ServerVersion, tr.Connect.Milliseconds())
			}
			fmt.Fprintln(cmd.OutOrStdout(), out)
			return nil
		}),
	}
	f.register(cmd, true)
	return cmd
}

func target(c *core.ConnInfo) string {
	if c.Host == "" {
		return c.Database
	}
	t := c.Host
	if c.Port != 0 {
		t += ":" + strconv.Itoa(c.Port)
	}
	if c.Database != "" {
		t += "/" + c.Database
	}
	return t
}

func connListCmd(with withApp) *cobra.Command {
	return &cobra.Command{
		Use:     "ls",
		Aliases: []string{"list"},
		Short:   "List connections",
		Args:    cobra.NoArgs,
		RunE: with(func(cmd *cobra.Command, a *app, _ []string) error {
			list, err := a.svc.ListConnections(cmd.Context(), ws)
			if err != nil {
				return err
			}
			if len(list) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "no connections; add one with: qh conn add <name> --engine postgres --dsn -")
				return nil
			}
			w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
			fmt.Fprintln(w, "NAME\tENGINE\tMODE\tTARGET\tDESCRIPTION")
			for _, c := range list {
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", c.Name, c.Engine, c.Mode, target(c), c.Description)
			}
			return w.Flush()
		}),
	}
}

func connShowCmd(with withApp) *cobra.Command {
	return &cobra.Command{
		Use:   "show <name>",
		Short: "Show a connection (password hidden)",
		Args:  cobra.ExactArgs(1),
		RunE: with(func(cmd *cobra.Command, a *app, args []string) error {
			c, err := a.svc.GetConnection(cmd.Context(), ws, args[0])
			if err != nil {
				return err
			}
			ns := "(all)"
			if len(c.AllowedNamespaces) > 0 {
				ns = strings.Join(c.AllowedNamespaces, ", ")
			}
			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "name: %s\nengine: %s\nmode: %s\ndsn: %s\n", c.Name, c.Engine, c.Mode, c.DSN)
			fmt.Fprintf(out, "description: %s\nmax_rows: %d\ntimeout: %s\npool_size: %d\nnamespaces: %s\n",
				c.Description, c.MaxRows, time.Duration(c.TimeoutMS)*time.Millisecond, c.PoolSize, ns)
			fmt.Fprintf(out, "version: %d\ncreated: %s\nupdated: %s\n", c.Version,
				c.CreatedAt.Format(time.RFC3339), c.UpdatedAt.Format(time.RFC3339))
			return nil
		}),
	}
}

func connEditCmd(with withApp) *cobra.Command {
	var f connFlags
	cmd := &cobra.Command{
		Use:   "edit <name>",
		Short: "Change a connection; only the flags you pass are changed",
		Args:  cobra.ExactArgs(1),
		RunE: with(func(cmd *cobra.Command, a *app, args []string) error {
			var p core.ConnPatch
			fl := cmd.Flags()
			if fl.Changed("dsn") {
				dsn, err := readDSN(cmd, f.dsn)
				if err != nil {
					return err
				}
				p.DSN = &dsn
			}
			if fl.Changed("mode") {
				m := engine.Mode(f.mode)
				p.Mode = &m
			}
			if fl.Changed("description") {
				p.Description = &f.description
			}
			if fl.Changed("max-rows") {
				p.MaxRows = &f.maxRows
			}
			if fl.Changed("timeout") {
				ms := int(f.timeout.Milliseconds())
				p.TimeoutMS = &ms
			}
			if fl.Changed("pool-size") {
				p.PoolSize = &f.poolSize
			}
			if fl.Changed("namespaces") {
				p.AllowedNamespaces = &f.namespaces
			}
			info, _, err := a.svc.UpdateConnection(cmd.Context(), ws, args[0], p, !f.skipTest)
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "updated %s (version %d)\n", info.Name, info.Version)
			return nil
		}),
	}
	f.register(cmd, false)
	return cmd
}

func connRemoveCmd(with withApp) *cobra.Command {
	return &cobra.Command{
		Use:     "rm <name>",
		Aliases: []string{"remove"},
		Short:   "Remove a connection",
		Args:    cobra.ExactArgs(1),
		RunE: with(func(cmd *cobra.Command, a *app, args []string) error {
			if err := a.svc.DeleteConnection(cmd.Context(), ws, args[0]); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "removed %s\n", args[0])
			return nil
		}),
	}
}

func connTestCmd(with withApp) *cobra.Command {
	return &cobra.Command{
		Use:   "test <name>",
		Short: "Connect with a stored connection's settings",
		Args:  cobra.ExactArgs(1),
		RunE: with(func(cmd *cobra.Command, a *app, args []string) error {
			tr, err := a.svc.TestConnection(cmd.Context(), ws, args[0])
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "ok · %s · %dms\n", tr.ServerVersion, tr.Connect.Milliseconds())
			return nil
		}),
	}
}
```

`internal/cli/query.go`:
```go
package cli

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/spf13/cobra"

	"github.com/anderson-0/qh/internal/core"
	"github.com/anderson-0/qh/internal/mcpserver"
	"github.com/anderson-0/qh/internal/store"
)

func queryCmd(with withApp) *cobra.Command {
	var maxRows int
	var explain bool
	cmd := &cobra.Command{
		Use:   "query <connection> <statement|->",
		Short: "Run one statement and print TSV (- reads the statement from stdin)",
		Args:  cobra.ExactArgs(2),
		RunE: with(func(cmd *cobra.Command, a *app, args []string) error {
			q := args[1]
			if q == "-" {
				b, err := io.ReadAll(cmd.InOrStdin())
				if err != nil {
					return err
				}
				q = string(b)
			}
			out, err := a.svc.Query(cmd.Context(), ws, core.QueryInput{Source: args[0], Query: q, MaxRows: maxRows, Explain: explain})
			if err != nil {
				return err
			}
			fmt.Fprint(cmd.OutOrStdout(), out)
			return nil
		}),
	}
	cmd.Flags().IntVar(&maxRows, "max-rows", 0, "row cap (at most the connection's limit)")
	cmd.Flags().BoolVar(&explain, "explain", false, "show the plan without running")
	return cmd
}

func schemaCmd(with withApp) *cobra.Command {
	var object string
	cmd := &cobra.Command{
		Use:   "schema <connection> [pattern]",
		Short: "Show the schema overview, or tables matching a pattern",
		Args:  cobra.RangeArgs(1, 2),
		RunE: with(func(cmd *cobra.Command, a *app, args []string) error {
			in := core.SchemaInput{Source: args[0], Object: object}
			if len(args) == 2 {
				in.Pattern = args[1]
			}
			out, err := a.svc.Schema(cmd.Context(), ws, in)
			if err != nil {
				return err
			}
			fmt.Fprint(cmd.OutOrStdout(), out)
			return nil
		}),
	}
	cmd.Flags().StringVar(&object, "object", "", "exact table name")
	return cmd
}

func mcpCmd(with withApp, version string) *cobra.Command {
	return &cobra.Command{
		Use:   "mcp",
		Short: "Serve MCP over stdio",
		Args:  cobra.NoArgs,
		RunE: with(func(cmd *cobra.Command, a *app, _ []string) error {
			a.pools.Start(cmd.Context())
			srv := mcpserver.New(a.svc, func(context.Context) string { return store.DefaultWorkspace }, version)
			err := srv.Run(cmd.Context(), &mcp.StdioTransport{})
			if errors.Is(err, context.Canceled) {
				return nil
			}
			return err
		}),
	}
}
```

- [ ] **Step 4: Run the tests**

Run: `go test -race ./internal/cli/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/cli
git commit -m "feat: add qh conn, query, schema and mcp commands"
```

---

### Task 16: End-to-end test, README and CI

**Files:**
- Create: `cmd/qh/e2e_test.go`, `README.md`, `.github/workflows/ci.yml`

**Interfaces:**
- Consumes: the built `qh` binary.

- [ ] **Step 1: Write the end-to-end test**

`cmd/qh/e2e_test.go`:
```go
package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/anderson-0/qh/internal/testutil"
)

func TestStdioEndToEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the binary")
	}
	bin := filepath.Join(t.TempDir(), "qh")
	build := exec.Command("go", "build", "-o", bin, ".")
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	home, db := t.TempDir(), testutil.SQLiteFixture(t)
	qh := func(args ...string) {
		t.Helper()
		if out, err := exec.Command(bin, append([]string{"--home", home}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("qh %v: %v\n%s", args, err, out)
		}
	}
	qh("conn", "add", "shop", "--engine", "sqlite", "--dsn", db)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	client := mcp.NewClient(&mcp.Implementation{Name: "e2e", Version: "0"}, nil)
	session, err := client.Connect(ctx, &mcp.CommandTransport{Command: exec.Command(bin, "--home", home, "mcp")}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()

	text := func(name string, args map[string]any) string {
		t.Helper()
		res, err := session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		return res.Content[0].(*mcp.TextContent).Text
	}
	if out := text("query", map[string]any{"source": "shop", "query": "SELECT count(*) AS n FROM orders"}); !strings.HasPrefix(out, "n\n250\n-- 1 row · ") {
		t.Fatalf("query: %q", out)
	}
	// A connection added while the server runs is visible on the next call.
	qh("conn", "add", "shop2", "--engine", "sqlite", "--dsn", db)
	if out := text("sources", map[string]any{}); !strings.Contains(out, "shop2 sqlite ro") {
		t.Fatalf("sources: %q", out)
	}
}
```

- [ ] **Step 2: Run it**

Run: `go test ./cmd/qh/`
Expected: PASS.

- [ ] **Step 3: Write the README**

`README.md`:
````markdown
# qh

A database server for AI agents, built to use as few tokens as possible. It exposes three MCP tools,
whatever the number of connections:

| Tool | What it does |
|---|---|
| `sources` | Lists connections: name, engine, read-only or read-write, description |
| `schema` | Overview with row estimates, or compact table definitions with keys, foreign keys in both directions, enum values and indexes |
| `query` | Runs one statement and returns TSV, 100 rows by default, with a footer that says when rows were cut |

Supported now: PostgreSQL 13+ and SQLite. MySQL/MariaDB, an HTTP server mode with a REST API, and a benchmark
against dbhub are planned (see `docs/superpowers/plans/`).

## Install

```bash
go install github.com/anderson-0/qh/cmd/qh@latest
```

## Add a connection

Connections are read-only unless you pass `--mode rw`. Pass `--dsn -` to read the connection string from stdin
so the password stays out of your shell history:

```bash
qh conn add shop --engine postgres --dsn - --description "Orders and customers"
```

```bash
qh conn add local --engine sqlite --dsn ./app.db
```

Connection strings are encrypted (AES-256-GCM) in `~/.config/qh/qh.db`; the key is `~/.config/qh/master.key`
(or `$QH_MASTER_KEY`). Set `$QH_HOME` to use another directory.

## Use it from Claude Code

```bash
claude mcp add qh -- qh mcp
```

Connections you add or edit with `qh conn` while the server runs are used on the agent's next call.

## Read-only safety

On read-only connections every query runs in a read-only transaction (Postgres) or on a file opened read-only
(SQLite), with statement checks on top for clear errors. Read-only transactions cannot stop everything: a
Postgres extension such as `dblink` or `postgres_fdw` can open a second connection that is not read-only. Give
qh a database user that can only read:

```sql
CREATE ROLE qh_reader LOGIN PASSWORD 'change-me';
GRANT CONNECT ON DATABASE shop TO qh_reader;
GRANT USAGE ON SCHEMA public TO qh_reader;
GRANT SELECT ON ALL TABLES IN SCHEMA public TO qh_reader;
ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT SELECT ON TABLES TO qh_reader;
```

## Development

```bash
make test        # needs Docker for the Postgres tests
make test-short  # skips Docker tests
make build       # static binary in bin/qh
```
````

- [ ] **Step 4: Add CI**

`.github/workflows/ci.yml`:
```yaml
name: ci
on: [push, pull_request]
jobs:
  test:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with:
          go-version: "1.27"
      - run: go vet ./...
      - run: go test -race ./...
      - run: CGO_ENABLED=0 go build -o bin/qh ./cmd/qh
```

- [ ] **Step 5: Full verification**

Run: `go vet ./... && go test -race ./... && make build && ./bin/qh version`
Expected: no vet findings, every package PASS (Postgres tests run when Docker is available), and `dev` printed.

- [ ] **Step 6: Commit**

```bash
git add cmd/qh/e2e_test.go README.md .github
git commit -m "test: add stdio end-to-end test, README and CI"
```
