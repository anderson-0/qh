# qh Plan 8: Postgres store and multiple instances — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let several `qh serve` instances share one Postgres store, with behaviour identical to the SQLite store, and keep SQLite as the default.

**Architecture:**
- A second `store.Store` implementation (`internal/store/postgres.go`) over pgx's `database/sql` driver. Its tables live in a dedicated `qh` schema, migrated under a Postgres advisory lock.
- One conformance suite (`internal/store/storetest`) proves both stores behave the same.
- Two new store methods make multi-instance start-up and moving data safe: `CreateKeyIfNone`, and `ImportConnection`/`ImportKey`.
- An optional `store.Invalidator`, Postgres `LISTEN`/`NOTIFY` on `qh_catalog`, keeps every instance's schema cache fresh.
- The CLI selects the store with `--store`/`QH_STORE_DSN`, accepts old master keys at serve time, and adds `qh store copy`.

**Tech Stack:** Go 1.25, `github.com/jackc/pgx/v5` v5.11.0 (`stdlib`, `pgconn`; already a dependency), `modernc.org/sqlite`, testcontainers-go v0.44.0 with `modules/postgres`.

**Spec:** `docs/superpowers/specs/2026-10-11-qh-postgres-store-design.md` (approved). It extends `docs/superpowers/specs/2026-10-09-qh-design.md`; read both.

## Spec adjustments

1. **Tables live in a dedicated `qh` schema.** The migration table is `qh.schema_migrations`, not `qh_schema_migrations` in the default schema. A shared database then cannot clash with other applications' tables. Every query names `qh.<table>`.
2. **`qh store copy` creates workspaces with `CreateWorkspace`.** A workspace's `created_at` therefore becomes the copy time. Connections and keys keep their timestamps as the spec says. Workspace timestamps are shown nowhere that matters, and a third import method would add little.

## Global Constraints

- **SQLite stays the default.** With no `--store`/`QH_STORE_DSN`, behaviour and every existing test are unchanged.
- **Accepted store DSNs.** Only `postgres://` and `postgresql://` URLs are accepted. Anything else is an invalid-store error that does not echo the DSN.
- **Supported Postgres.** 13 through 17. Tests run on `postgres:13-alpine` and `postgres:17-alpine`.
- **Conformance.**
  - Both stores pass the same `storetest` suite: sentinel errors, written-back fields, cascades, ordering, empty-value encoding and `ResealConnections` semantics, exactly as listed in spec §3.2.
  - Times returned in UTC.
  - Store-written timestamps truncated to the microsecond.
  - Query-log `At` compared at millisecond precision, because SQLite stores milliseconds.
- **No secrets in errors or logs.** The store DSN's password (and `QH_STORE_PASSWORD_FILE`'s contents) never appear in errors or logs.
- **The MCP agent surface does not change.**
- **Docker-backed tests skip under `-short`.** Guard with `testing.Short()` and `testcontainers.SkipIfProviderIsNotHealthy(t)`, as the engine tests do.
- **Commit trailer:** `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`, or the name of the model actually running you.
- **Branch:** `feat/plan-8-postgres-store`. Do not push.

## Review Focus

1. **A Postgres session in a non-UTC timezone.** A DSN with `timezone=America/Sao_Paulo` must still return `CreatedAt`/`UpdatedAt` in UTC, equal to what was written (Task 2 test).
2. **The store's password never appears in an error.** `OpenPostgres` against an unreachable host, with a password in the DSN, fails without the password in the error text (Task 2 test).
3. **The listening connection dies** (database restart, failover, `pg_terminate_backend`). The listener reconnects and tells the instance to drop its whole schema cache, because notices may have been missed (Task 4 test).
4. **Two instances start at once on an empty database.** Each migration applies once, and exactly one bootstrap admin key is created. A fixed `QH_BOOTSTRAP_ADMIN_KEY` must not make the second instance fail (Tasks 2 and 3 tests).
5. **Query-log entries written by two instances interleave.** `ListQueryLog` returns them newest first in insert order across instances (Task 7 test).

---

### Task 1: The store conformance suite

**Files:**
- Create: `internal/store/storetest/suite.go`
- Create: `internal/store/sqlite_conformance_test.go` (package `store_test`)
- Modify: `internal/store/sqlite_test.go`, `keys_test.go`, `querylog_test.go`, `reseal_test.go`. These tests move into the suite.

**Interfaces:**
- Produces: `func storetest.Run(t *testing.T, open func(t *testing.T) store.Store)`. `open` returns a fresh, empty, migrated store (the `default` workspace only) and registers its own cleanup. `Run` runs every conformance case as a subtest.

The suite must import only `store` and the standard library. The runner lives in the external test package `store_test`, because a `package store` test importing `storetest`, which imports `store`, is an import cycle.

- [ ] **Step 1: Move the interface-level tests into the suite**

Create `internal/store/storetest/suite.go`:

```go
// Package storetest is the conformance suite every store.Store implementation
// must pass.
package storetest

import (
	"testing"

	"github.com/anderson-0/qh/internal/store"
)

// Run runs every conformance case against stores made by open. open returns
// a fresh, empty, migrated store and registers its own cleanup.
func Run(t *testing.T, open func(t *testing.T) store.Store) {
	for name, fn := range map[string]func(*testing.T, store.Store){
		"CreateGetList":                         testCreateGetList,
		"DuplicateAndMissing":                   testDuplicateAndMissing,
		"UpdateVersioning":                      testUpdateVersioning,
		"StaleUpdateDoesNotHitRecreatedConnection": testStaleUpdateDoesNotHitRecreatedConnection,
		"Workspaces":                            testWorkspaces,
		"Keys":                                  testKeys,
		"QueryLog":                              testQueryLog,
		"ConnectionTLSSummary":                  testConnectionTLSSummary,
		"ConnectionSSHAndRefsSummaries":         testConnectionSSHAndRefsSummaries,
		"ResealConnections":                     testResealConnections,
	} {
		t.Run(name, func(t *testing.T) { fn(t, open(t)) })
	}
}
```

Port the body of each existing test, unchanged in what it asserts, into a function of that name in this package. The sources are:

- `TestCreateGetList`, `TestDuplicateAndMissing`, `TestUpdateVersioning` and `TestStaleUpdateDoesNotHitRecreatedConnection` in `sqlite_test.go`;
- `TestWorkspaces` and `TestKeys` in `keys_test.go`;
- `TestQueryLog`, `TestConnectionTLSSummary` and `TestConnectionSSHAndRefsSummaries` in `querylog_test.go`;
- `TestResealConnections` in `reseal_test.go`.

Each becomes `testXxx(t *testing.T, s store.Store)`, taking the store as an argument instead of calling `openTemp(t)`. Qualify store identifiers (`store.Connection`, `store.ErrNotFound`, `store.DefaultWorkspace`, `store.NewID`, …).

Move the `sample(name string) *store.Connection` helper into the suite. If a body used anything other than the `Store` interface, rewrite that line through the interface. Keep it in `sqlite_test.go` only if it is SQLite-specific.

Make time assertions precision-agnostic:

- compare written and read `CreatedAt`/`UpdatedAt`/`ExpiresAt`/`LastUsedAt` with `a.Truncate(time.Microsecond).Equal(b.Truncate(time.Microsecond))`;
- compare query-log `At` at `time.Millisecond`;
- where a test sets a time itself, prefer a value already truncated to the millisecond.

Delete the moved tests from their old files. These SQLite-only tests stay in `package store`:

- `TestDeleteAndReopen`, which reopens the same file;
- `TestConcurrentFirstOpen`, which reads `s.db`;
- `TestUpgradeFromVersion1`.

Keep `openTemp` in `sqlite_test.go` for them.

- [ ] **Step 2: Run the suite against SQLite**

Create `internal/store/sqlite_conformance_test.go`:

```go
package store_test

import (
	"path/filepath"
	"testing"

	"github.com/anderson-0/qh/internal/store"
	"github.com/anderson-0/qh/internal/store/storetest"
)

func TestSQLiteConformance(t *testing.T) {
	storetest.Run(t, func(t *testing.T) store.Store {
		s, err := store.OpenSQLite(filepath.Join(t.TempDir(), "qh.db"))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { s.Close() })
		return s
	})
}
```

- [ ] **Step 3: Verify**

Run: `go vet ./internal/store/... && go test ./internal/store/... -count=1 -race -v -run 'Conformance|DeleteAndReopen|ConcurrentFirstOpen|UpgradeFromVersion1' 2>&1 | grep -E '^(=== RUN|--- (PASS|FAIL)|ok|FAIL)' | grep -c PASS`

Expected: every case passes. The number of subtests equals the number of moved tests (10), plus the 3 SQLite-only tests. Then run `go test ./internal/store/... -count=1` and confirm `ok`.

- [ ] **Step 4: Commit**

```bash
git add internal/store
git commit -m "test(store): extract a conformance suite every store must pass

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 2: The Postgres store

**Files:**
- Create: `internal/store/postgres.go`
- Create: `internal/store/open.go`
- Create: `internal/store/postgres_test.go` (package `store_test`)

**Interfaces:**
- Consumes:
  - `storetest.Run` (Task 1);
  - the unexported helpers already in `sqlite.go`, all in package `store`: `scanner`, `jsonColumn`, `nonNil`.
- Produces:
  - `type Postgres struct`, implementing `store.Store`;
  - `func OpenPostgres(ctx context.Context, dsn string) (*Postgres, error)`;
  - `func Open(ctx context.Context, dsn, dataDir string) (Store, error)`. Empty `dsn` → `OpenSQLite(filepath.Join(dataDir, "qh.db"))`. A `postgres://` or `postgresql://` prefix → `OpenPostgres`. Anything else → `ErrInvalidDSN`.
  - `var ErrInvalidDSN = errors.New("store: the store connection string must be a postgres:// URL")`
  - `var pgOpenRetry = 30 * time.Second`, a package variable tests shorten.

- [ ] **Step 1: Write the failing tests**

`internal/store/postgres_test.go`:

```go
package store_test

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/anderson-0/qh/internal/store"
	"github.com/anderson-0/qh/internal/store/storetest"
)

// pgServer starts a Postgres container and returns the admin DSN.
func pgServer(t *testing.T, image string) string {
	t.Helper()
	if testing.Short() {
		t.Skip("needs Docker")
	}
	testcontainers.SkipIfProviderIsNotHealthy(t)
	ctx := context.Background()
	ctr, err := tcpostgres.Run(ctx, image, tcpostgres.WithDatabase("postgres"),
		tcpostgres.WithUsername("qh"), tcpostgres.WithPassword("store-pw-123"), tcpostgres.BasicWaitStrategies())
	t.Cleanup(func() { _ = testcontainers.TerminateContainer(ctr) })
	if err != nil {
		t.Fatal(err)
	}
	dsn, err := ctr.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	return dsn
}

var dbSeq atomic.Int64

// freshDB creates an empty database on the server and returns its DSN.
func freshDB(t *testing.T, admin string) string {
	t.Helper()
	db, err := sql.Open("pgx", admin)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	name := fmt.Sprintf("qh_test_%d", dbSeq.Add(1))
	if _, err := db.Exec("CREATE DATABASE " + name); err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(admin)
	u.Path = "/" + name
	return u.String()
}

func openPG(t *testing.T, dsn string) *store.Postgres {
	t.Helper()
	s, err := store.OpenPostgres(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestPostgresConformance(t *testing.T) {
	for _, image := range []string{"postgres:13-alpine", "postgres:17-alpine"} {
		t.Run(image, func(t *testing.T) {
			admin := pgServer(t, image)
			storetest.Run(t, func(t *testing.T) store.Store { return openPG(t, freshDB(t, admin)) })
		})
	}
}

func TestPostgresConcurrentFirstOpen(t *testing.T) {
	dsn := freshDB(t, pgServer(t, "postgres:17-alpine"))
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s, err := store.OpenPostgres(context.Background(), dsn)
			if err != nil {
				errs <- err
				return
			}
			s.Close()
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	db, _ := sql.Open("pgx", dsn)
	defer db.Close()
	var n, ws int
	db.QueryRow(`SELECT count(*) FROM qh.schema_migrations`).Scan(&n)
	db.QueryRow(`SELECT count(*) FROM qh.workspaces`).Scan(&ws)
	if n != 1 || ws != 1 {
		t.Fatalf("migrations applied %d times, %d workspaces", n, ws)
	}
}

func TestPostgresNonUTCSessionReturnsUTC(t *testing.T) {
	dsn := freshDB(t, pgServer(t, "postgres:17-alpine"))
	s := openPG(t, dsn+"&timezone=America/Sao_Paulo")
	ctx := context.Background()
	c := &store.Connection{ID: store.NewID(), WorkspaceID: store.DefaultWorkspace, Name: "x", Engine: "postgres",
		Secret: []byte{1}, SecretKeyID: 1, Mode: "ro", MaxRows: 1, TimeoutMS: 1, PoolSize: 1}
	if err := s.CreateConnection(ctx, c); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetConnection(ctx, store.DefaultWorkspace, "x")
	if err != nil {
		t.Fatal(err)
	}
	if got.CreatedAt.Location() != time.UTC || !got.CreatedAt.Equal(c.CreatedAt) {
		t.Fatalf("created %v (loc %v), wrote %v", got.CreatedAt, got.CreatedAt.Location(), c.CreatedAt)
	}
}

func TestPostgresErrorsHideThePassword(t *testing.T) {
	store.SetPGOpenRetry(t, 300*time.Millisecond)
	_, err := store.OpenPostgres(context.Background(), "postgres://qh:hunter2-secret@127.0.0.1:1/qh?sslmode=disable&connect_timeout=1")
	if err == nil || strings.Contains(err.Error(), "hunter2-secret") {
		t.Fatalf("%v", err)
	}
	_, err = store.OpenPostgres(context.Background(), "postgres://qh:hunter2-secret@[::1/bad")
	if err == nil || strings.Contains(err.Error(), "hunter2-secret") {
		t.Fatalf("%v", err)
	}
}

func TestPostgresResealBlocksConcurrentUpdate(t *testing.T) {
	dsn := freshDB(t, pgServer(t, "postgres:17-alpine"))
	a, b := openPG(t, dsn), openPG(t, dsn)
	ctx := context.Background()
	c := &store.Connection{ID: store.NewID(), WorkspaceID: store.DefaultWorkspace, Name: "x", Engine: "postgres",
		Secret: []byte{1}, SecretKeyID: 1, Mode: "ro", MaxRows: 1, TimeoutMS: 1, PoolSize: 1}
	if err := a.CreateConnection(ctx, c); err != nil {
		t.Fatal(err)
	}
	inFn := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		_, err := a.ResealConnections(ctx, func(*store.Connection) ([]byte, uint32, error) {
			close(inFn)
			<-release
			return []byte{9}, 2, nil
		})
		done <- err
	}()
	<-inFn
	updated := make(chan error, 1)
	go func() {
		u := *c
		u.Description = "changed"
		updated <- b.UpdateConnection(ctx, &u)
	}()
	select {
	case err := <-updated:
		t.Fatalf("update did not wait for the reseal: %v", err)
	case <-time.After(300 * time.Millisecond):
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if err := <-updated; err != nil {
		t.Fatalf("update after reseal: %v", err)
	}
	got, _ := a.GetConnection(ctx, store.DefaultWorkspace, "x")
	if got.SecretKeyID != 2 || got.Description != "changed" {
		t.Fatalf("%+v", got)
	}
}

func TestOpenChoosesTheStore(t *testing.T) {
	dir := t.TempDir()
	s, err := store.Open(context.Background(), "", dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := s.(*store.SQLite); !ok {
		t.Fatalf("empty DSN opened %T", s)
	}
	s.Close()
	if _, err := store.Open(context.Background(), "mysql://x", dir); err != store.ErrInvalidDSN {
		t.Fatalf("mysql DSN: %v", err)
	}
}

func TestPostgresPasswordFile(t *testing.T) {
	dsn := freshDB(t, pgServer(t, "postgres:17-alpine"))
	u, _ := url.Parse(dsn)
	pw, _ := u.User.Password()
	u.User = url.User(u.User.Username())
	f := t.TempDir() + "/pw"
	if err := writeFile(f, pw+"\n"); err != nil {
		t.Fatal(err)
	}
	t.Setenv("QH_STORE_PASSWORD_FILE", f)
	openPG(t, u.String()).Close()
}
```

Add a tiny `writeFile` helper (`os.WriteFile(path, []byte(s), 0o600)`) to the test file.

`store.SetPGOpenRetry` is a test hook. Add it in a new file `internal/store/export_test.go`, package `store`. It works because internal and external test files compile into one test binary:

```go
package store

import (
	"testing"
	"time"
)

// SetPGOpenRetry shortens how long OpenPostgres retries, for one test.
func SetPGOpenRetry(t *testing.T, d time.Duration) {
	old := pgOpenRetry
	pgOpenRetry = d
	t.Cleanup(func() { pgOpenRetry = old })
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/store/... -count=1 -run 'Postgres|OpenChooses'`

Expected: FAIL to compile (`undefined: store.OpenPostgres`).

- [ ] **Step 3: Implement `internal/store/open.go`**

```go
package store

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
)

// ErrInvalidDSN reports a store connection string that is not a postgres URL.
var ErrInvalidDSN = errors.New("store: the store connection string must be a postgres:// URL")

// Open opens the store dsn names: the SQLite file in dataDir when dsn is
// empty, or a Postgres database.
func Open(ctx context.Context, dsn, dataDir string) (Store, error) {
	switch {
	case dsn == "":
		return OpenSQLite(filepath.Join(dataDir, "qh.db"))
	case strings.HasPrefix(dsn, "postgres://"), strings.HasPrefix(dsn, "postgresql://"):
		return OpenPostgres(ctx, dsn)
	}
	return nil, ErrInvalidDSN
}
```

- [ ] **Step 4: Implement `internal/store/postgres.go`**

```go
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
	deadline := time.Now().Add(pgOpenRetry)
	for {
		err = db.PingContext(ctx)
		if err == nil {
			break
		}
		if time.Now().After(deadline) || ctx.Err() != nil {
			db.Close()
			return nil, fmt.Errorf("store: cannot reach the postgres store at %s:%d: %s", cfg.Host, cfg.Port, redactErr(err, cfg.Password))
		}
		select {
		case <-ctx.Done():
		case <-time.After(500 * time.Millisecond):
		}
	}
	p := &Postgres{db: db, cfg: cfg, now: func() time.Time { return time.Now().UTC().Truncate(time.Microsecond) }}
	if err := p.migrate(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("store: migrate: %s", redactErr(err, cfg.Password))
	}
	return p, nil
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
```

Check before relying on it that `connColumns`, `keyColumns`, `jsonColumn`, `nonNil` and `scanner` exist in `sqlite.go` with these names and column orders (`grep -n 'connColumns\|keyColumns\|func nonNil\|type scanner' internal/store/sqlite.go`). `connColumns` must list the columns in exactly the order `scanPGConnection` and `connArgs` use. `listConnections` takes `ctx` but does not use it; drop the parameter if `go vet` or a linter complains.

- [ ] **Step 5: Run the tests to verify they pass**

Run: `go vet ./internal/store/... && go test ./internal/store/... -count=1 -race`

Expected: PASS. The Postgres tests start containers: the conformance tests on 13 and 17, concurrent first open, reseal locking, the non-UTC session and the password file. The error-redaction test needs no container.

- [ ] **Step 6: Commit**

```bash
git add internal/store go.mod go.sum
git commit -m "feat(store): add a Postgres store that passes the conformance suite

Tables live in the qh schema and migrate under an advisory lock, so
instances starting together apply each step once. Times come back in UTC
whatever the session time zone; the password never appears in errors.

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 3: Atomic bootstrap and import methods

**Files:**
- Modify: `internal/store/store.go` (interface), `internal/store/sqlite.go`, `internal/store/postgres.go`
- Modify: `internal/store/storetest/suite.go` (new cases)
- Modify: `internal/auth/manage.go` (`Bootstrap`)
- Test: `internal/store/postgres_test.go`, `internal/auth/auth_test.go`

**Interfaces:**
- Produces, on `store.Store`:
  - `CreateKeyIfNone(ctx context.Context, k *APIKey) (created bool, err error)`. It inserts `k` only when the store has no API keys. It is atomic across processes. On success it sets `k.CreatedAt` as `CreateKey` does.
  - `ImportConnection(ctx context.Context, c *Connection) error`. It inserts `c` exactly as given, including `Version`, `CreatedAt` and `UpdatedAt`, with no defaults applied. Errors are `ErrExists` and `ErrNotFound` as for `CreateConnection`.
  - `ImportKey(ctx context.Context, k *APIKey) error`. It inserts `k` exactly as given, including `CreatedAt` and `LastUsedAt`. Errors are `ErrExists` and `ErrNotFound` as for `CreateKey`.
- `auth.Bootstrap` keeps its signature `(token string, created bool, err error)`.

- [ ] **Step 1: Write the failing tests**

Add these cases to `storetest.Run`'s map (`"CreateKeyIfNone": testCreateKeyIfNone, "Import": testImport`):

```go
func testCreateKeyIfNone(t *testing.T, s store.Store) {
	ctx := context.Background()
	k := &store.APIKey{ID: store.NewID(), Prefix: "qh_aaaaaaaa", Hash: []byte{1}, Scopes: []string{"admin"}, Name: "bootstrap"}
	created, err := s.CreateKeyIfNone(ctx, k)
	if err != nil || !created || k.CreatedAt.IsZero() {
		t.Fatalf("first: %v %v %v", created, err, k.CreatedAt)
	}
	k2 := &store.APIKey{ID: store.NewID(), Prefix: "qh_bbbbbbbb", Hash: []byte{2}, Scopes: []string{"admin"}}
	if created, err := s.CreateKeyIfNone(ctx, k2); err != nil || created {
		t.Fatalf("second: %v %v", created, err)
	}
	if n, _ := s.CountKeys(ctx); n != 1 {
		t.Fatalf("keys = %d", n)
	}
}

func testImport(t *testing.T, s store.Store) {
	ctx := context.Background()
	created := time.Date(2025, 1, 2, 3, 4, 5, 123000000, time.UTC)
	updated := created.Add(time.Hour)
	c := sample("imported")
	c.Version, c.CreatedAt, c.UpdatedAt = 7, created, updated
	if err := s.ImportConnection(ctx, c); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetConnection(ctx, store.DefaultWorkspace, "imported")
	if err != nil || got.Version != 7 || !got.CreatedAt.Equal(created) || !got.UpdatedAt.Equal(updated) || string(got.Secret) != string(c.Secret) || got.SecretKeyID != c.SecretKeyID {
		t.Fatalf("%+v %v", got, err)
	}
	if err := s.ImportConnection(ctx, c); err != store.ErrExists {
		t.Fatalf("duplicate: %v", err)
	}
	bad := sample("orphan")
	bad.WorkspaceID = "nope"
	if err := s.ImportConnection(ctx, bad); err != store.ErrNotFound {
		t.Fatalf("missing workspace: %v", err)
	}
	used := created.Add(2 * time.Hour)
	k := &store.APIKey{ID: store.NewID(), Prefix: "qh_cccccccc", Hash: []byte{3}, Scopes: []string{"query"},
		WorkspaceID: store.DefaultWorkspace, Name: "copied", CreatedAt: created, LastUsedAt: &used}
	if err := s.ImportKey(ctx, k); err != nil {
		t.Fatal(err)
	}
	gk, err := s.GetKeyByPrefix(ctx, "qh_cccccccc")
	if err != nil || !gk.CreatedAt.Equal(created) || gk.LastUsedAt == nil || !gk.LastUsedAt.Equal(used) {
		t.Fatalf("%+v %v", gk, err)
	}
}
```

Add the `context` and `time` imports to `suite.go`.

In `internal/store/postgres_test.go`:

```go
func TestPostgresCreateKeyIfNoneRace(t *testing.T) {
	dsn := freshDB(t, pgServer(t, "postgres:17-alpine"))
	var wg sync.WaitGroup
	var created atomic.Int64
	for i := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s, err := store.OpenPostgres(context.Background(), dsn)
			if err != nil {
				t.Error(err)
				return
			}
			defer s.Close()
			ok, err := s.CreateKeyIfNone(context.Background(), &store.APIKey{ID: store.NewID(), Prefix: fmt.Sprintf("qh_race%04d", i), Hash: []byte{1}, Scopes: []string{"admin"}})
			if err != nil {
				t.Error(err)
			}
			if ok {
				created.Add(1)
			}
		}()
	}
	wg.Wait()
	if created.Load() != 1 {
		t.Fatalf("created %d keys", created.Load())
	}
}
```

In `internal/auth/auth_test.go`, add this to the existing tests. Use its store helper; check how it opens the store with `grep -n OpenSQLite internal/auth/auth_test.go`.

```go
func TestBootstrapTwiceWithFixedKeyDoesNotFail(t *testing.T) {
	st := openTestStore(t) // the helper this file already uses to open a SQLite store
	fixed := "qh_" + strings.Repeat("a", 8) + "_" + strings.Repeat("b", 40)
	_, created, err := Bootstrap(context.Background(), st, fixed)
	if err != nil || !created {
		t.Fatalf("first: %v %v", created, err)
	}
	_, created, err = Bootstrap(context.Background(), st, fixed)
	if err != nil || created {
		t.Fatalf("second: %v %v", created, err)
	}
}
```

The token shape (`qh_<prefix>_<secret>`) must match `ParseToken` in `internal/auth`. Check with `grep -n 'func ParseToken' -A15 internal/auth/*.go` and adjust `fixed` to a valid token if the format differs.

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/store/... ./internal/auth -count=1 -run 'Conformance|KeyIfNone|Bootstrap'`

Expected: FAIL to compile (`s.CreateKeyIfNone undefined`).

- [ ] **Step 3: Implement**

Add to the `Store` interface in `internal/store/store.go`:

```go
	// CreateKeyIfNone creates k only when the store holds no API keys, atomically
	// across processes, and reports whether it did.
	CreateKeyIfNone(ctx context.Context, k *APIKey) (bool, error)
	// ImportConnection and ImportKey insert a row exactly as given, timestamps
	// and version included (qh store copy).
	ImportConnection(ctx context.Context, c *Connection) error
	ImportKey(ctx context.Context, k *APIKey) error
```

SQLite (`internal/store/sqlite.go`):

```go
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
		_, err := conn.ExecContext(ctx, "ROLLBACK")
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
		if isUnique(err) {
			return false, ErrExists
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
```

`connColumns` puts `version` after `allowed_namespaces`, then `created_at, updated_at, tls, ssh, refs`. Check the order in `sqlite.go` and match it.

Postgres (`internal/store/postgres.go`):

```go
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
		if isPGUnique(err) {
			return false, ErrExists
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
```

Add `const bootstrapLock = 7_212_011_971` next to `migrateLock`.

In `internal/auth/manage.go`, replace the `CountKeys` check and the `CreateKey` call in `Bootstrap`:

```go
func Bootstrap(ctx context.Context, st store.Store, fixed string) (token string, created bool, err error) {
	n, err := st.CountKeys(ctx)
	if err != nil || n > 0 {
		return "", false, err // fast path: most starts find keys
	}
	k := &store.APIKey{ID: store.NewID(), Scopes: []string{ScopeAdmin}, Name: "bootstrap"}
	if fixed != "" {
		prefix, secret, ok := ParseToken(strings.TrimSpace(fixed))
		if !ok || len(secret) < 32 {
			return "", false, qerr.New(qerr.Invalid, "QH_BOOTSTRAP_ADMIN_KEY must be a qh_ key with a secret of at least 32 characters").
				WithHint("Generate one with qh key create, or leave it unset to have qh generate one")
		}
		k.Prefix, k.Hash = prefix, HashSecret(secret)
	} else {
		if token, k.Prefix, k.Hash, err = NewToken(); err != nil {
			return "", false, err
		}
	}
	// Another instance may have created the first key since the count:
	// CreateKeyIfNone is atomic, so exactly one bootstrap key exists.
	if created, err = st.CreateKeyIfNone(ctx, k); err != nil || !created {
		return "", false, err
	}
	return token, true, nil
}
```

Any test fake implementing `store.Store` must gain the three methods. Find them with `grep -rn 'store.Store\b' internal --include=*_test.go`, or with the compiler errors. Embedding the real store in a fake, as `failingLog{*store.SQLite}` does in core, covers it automatically.

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go vet ./... && go test ./internal/store/... ./internal/auth ./internal/core ./internal/httpapi ./internal/cli -count=1 -race`

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/store internal/auth internal/core internal/httpapi internal/cli
git commit -m "feat(store): atomic first-key bootstrap and verbatim import methods

Instances starting together on an empty store create exactly one admin
key; a fixed QH_BOOTSTRAP_ADMIN_KEY no longer fails the second one.

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 4: Schema-cache invalidation across instances

**Files:**
- Modify: `internal/store/store.go` (add `Invalidator`), `internal/store/postgres.go` (implement it)
- Modify: `internal/catalog/cache.go` (add `InvalidateAll`)
- Modify: `internal/core/service.go` (an `invalidate` helper and `Start`), and every `s.cache.Invalidate(` call site in `internal/core` (`connections.go`, `service.go`, `workspaces.go`)
- Test: `internal/store/postgres_test.go`, `internal/catalog/cache_test.go`, `internal/core/invalidate_test.go`

**Interfaces:**
- Produces:

```go
// Invalidator tells every instance sharing a store that a connection's cached
// schema is stale. Stores shared by several processes implement it.
type Invalidator interface {
	Invalidate(ctx context.Context, connectionID string) error
	// Invalidations delivers connection IDs other processes invalidated, and
	// "" when every cached schema must be dropped (after a reconnect, when
	// notices may have been missed). The channel closes when ctx ends.
	Invalidations(ctx context.Context) (<-chan string, error)
}
```

  - `func (c *catalog.Cache) InvalidateAll()`
  - `func (s *core.Service) Start(ctx context.Context)`: subscribes to the store's invalidations, if it has any, until `ctx` ends.
- Consumes: `Options.Logger *slog.Logger`, already in `core.Options`.

- [ ] **Step 1: Write the failing tests**

`internal/catalog/cache_test.go`: add a test that fills two entries (follow the existing tests' way of building one), calls `InvalidateAll`, and asserts both are rebuilt on the next `Get`. Count builder calls the way the existing tests do.

`internal/store/postgres_test.go`:

```go
func TestPostgresInvalidations(t *testing.T) {
	dsn := freshDB(t, pgServer(t, "postgres:17-alpine"))
	a, b := openPG(t, dsn), openPG(t, dsn)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch, err := b.Invalidations(ctx)
	if err != nil {
		t.Fatal(err)
	}
	recv := func() string {
		select {
		case id := <-ch:
			return id
		case <-time.After(10 * time.Second):
			t.Fatal("no notification")
			return ""
		}
	}
	time.Sleep(200 * time.Millisecond) // let LISTEN start
	if err := a.Invalidate(ctx, "conn-1"); err != nil {
		t.Fatal(err)
	}
	if got := recv(); got != "conn-1" {
		t.Fatalf("got %q", got)
	}
	// Kill the listening session: the listener reconnects and sends "".
	db, _ := sql.Open("pgx", dsn)
	defer db.Close()
	if _, err := db.Exec(`SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE query LIKE 'LISTEN%' OR query LIKE '%qh_catalog%'`); err != nil {
		t.Fatal(err)
	}
	if got := recv(); got != "" {
		t.Fatalf("after reconnect got %q, want \"\"", got)
	}
	if err := a.Invalidate(ctx, "conn-2"); err != nil {
		t.Fatal(err)
	}
	if got := recv(); got != "conn-2" {
		t.Fatalf("after reconnect got %q", got)
	}
	cancel()
	for range ch { // must close
	}
}
```

`internal/core/invalidate_test.go` is a unit test with a fake invalidating store. It embeds the real SQLite store from the harness and adds the two methods:

```go
package core

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/anderson-0/qh/internal/catalog"
	"github.com/anderson-0/qh/internal/engine"
	"github.com/anderson-0/qh/internal/pool"
	"github.com/anderson-0/qh/internal/store"
)

type invStore struct {
	*store.SQLite
	mu   sync.Mutex
	sent []string
	ch   chan string
}

func (s *invStore) Invalidate(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sent = append(s.sent, id)
	return nil
}

func (s *invStore) Invalidations(ctx context.Context) (<-chan string, error) {
	out := make(chan string)
	go func() {
		defer close(out)
		for {
			select {
			case <-ctx.Done():
				return
			case id := <-s.ch:
				out <- id
			}
		}
	}()
	return out, nil
}

func TestServiceSharesInvalidations(t *testing.T) {
	h := newHarness(t)
	h.addShop(t, engine.ModeRW)
	inv := &invStore{SQLite: h.st, ch: make(chan string)}
	cache := catalog.New(time.Hour)
	svc := New(inv, h.svc.keys, pool.New(OpenConn, pool.Options{}), cache)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	svc.Start(ctx)
	if _, err := svc.Schema(ctx, ws, SchemaInput{Source: "shop"}); err != nil { // fills the cache
		t.Fatal(err)
	}
	// A write through this instance tells the others.
	if _, err := svc.Query(ctx, ws, QueryInput{Source: "shop", Query: "CREATE TABLE t1 (x int)"}); err != nil {
		t.Fatal(err)
	}
	inv.mu.Lock()
	sent := append([]string(nil), inv.sent...)
	inv.mu.Unlock()
	if len(sent) == 0 {
		t.Fatal("no invalidation sent")
	}
	// Another instance's DDL: the notice drops our cached schema.
	if _, err := svc.Schema(ctx, ws, SchemaInput{Source: "shop"}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Query(ctx, ws, QueryInput{Source: "shop", Query: "SELECT 1"}); err != nil {
		t.Fatal(err)
	}
	other := New(h.st, h.svc.keys, pool.New(OpenConn, pool.Options{}), catalog.New(time.Hour))
	if _, err := other.Query(ctx, ws, QueryInput{Source: "shop", Query: "CREATE TABLE t2 (x int)"}); err != nil {
		t.Fatal(err)
	}
	rec, _ := h.st.GetConnection(ctx, ws, "shop")
	inv.ch <- rec.ID
	deadline := time.Now().Add(5 * time.Second)
	for {
		out, err := svc.Schema(ctx, ws, SchemaInput{Source: "shop", Object: "t2"})
		if err == nil && out != "" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("t2 never appeared: %q %v", out, err)
		}
		time.Sleep(50 * time.Millisecond)
	}
	inv.ch <- "" // drop everything: must not panic or block
}
```

Use the real names of the harness fields (`h.st`, `h.svc.keys`). If `Schema` for an unknown object returns an error rather than "", the loop's `err == nil && out != ""` condition already handles it.

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/catalog ./internal/core -count=1 -run 'InvalidateAll|SharesInvalidations'`

Expected: FAIL to compile (`InvalidateAll undefined`, `svc.Start undefined`).

- [ ] **Step 3: Implement**

`internal/catalog/cache.go`:

```go
// InvalidateAll drops every cached catalog, including builds in progress.
func (c *Cache) InvalidateAll() {
	c.mu.Lock()
	for id := range c.entries {
		c.gens[id]++
	}
	for id := range c.gens {
		c.gens[id]++
	}
	c.entries = map[string]entry{}
	c.mu.Unlock()
}
```

Check the real field names (`entries`, `gens`) and the entry type name in `cache.go`. Every build in progress must be discarded, which is why every `gens` key is bumped.

Add `Invalidator` to `internal/store/store.go` as shown under Interfaces.

`internal/store/postgres.go`:

```go
// Invalidate tells every instance listening on the store that id's schema is stale.
func (p *Postgres) Invalidate(ctx context.Context, id string) error {
	_, err := p.db.ExecContext(ctx, `SELECT pg_notify('qh_catalog', $1)`, id)
	return err
}

// Invalidations listens on qh_catalog with its own connection, reconnecting
// with backoff; after each reconnect it sends "" because notices may have
// been missed.
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
				select {
				case out <- n.Payload:
				case <-ctx.Done():
				}
				continue
			}
			conn.Close(context.Background())
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
				conn.Close(context.Background())
				return
			}
		}
	}()
	return out, nil
}

func (p *Postgres) listen(ctx context.Context) (*pgx.Conn, error) {
	conn, err := pgx.ConnectConfig(ctx, p.cfg.Copy())
	if err != nil {
		return nil, err
	}
	if _, err := conn.Exec(ctx, `LISTEN qh_catalog`); err != nil {
		conn.Close(context.Background())
		return nil, err
	}
	return conn, nil
}
```

`internal/core/service.go`. Add an `inv store.Invalidator` field to `Service`, set in `New` when `st` implements it:

```go
	if inv, ok := st.(store.Invalidator); ok {
		s.inv = inv
	}
```

Add:

```go
// invalidate drops id's cached schema here and, when the store is shared,
// in every other instance. Telling the others is best effort.
func (s *Service) invalidate(ctx context.Context, id string) {
	s.cache.Invalidate(id)
	if s.inv == nil {
		return
	}
	if err := s.inv.Invalidate(context.WithoutCancel(ctx), id); err != nil && s.opts.Logger != nil {
		s.opts.Logger.Warn("cannot tell other instances to drop a cached schema", "error", err)
	}
}

// Start applies other instances' schema invalidations until ctx ends. It does
// nothing when the store is not shared.
func (s *Service) Start(ctx context.Context) {
	if s.inv == nil {
		return
	}
	ch, err := s.inv.Invalidations(ctx)
	if err != nil {
		if s.opts.Logger != nil {
			s.opts.Logger.Warn("cannot listen for other instances' schema changes; cached schemas expire after their TTL", "error", err)
		}
		return
	}
	go func() {
		for id := range ch {
			if id == "" {
				s.cache.InvalidateAll()
			} else {
				s.cache.Invalidate(id)
			}
		}
	}()
}
```

Replace every `s.cache.Invalidate(x)` in `internal/core` with `s.invalidate(ctx, x)`. Each call site has a `ctx` in scope; if one does not, pass `context.Background()`. Leave `RefreshSchema`'s `s.cache.Invalidate(t.rec.ID)` as `s.invalidate(ctx, t.rec.ID)` too: a refresh on one instance refreshes everywhere.

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go vet ./... && go test ./internal/catalog ./internal/core ./internal/store/... -count=1 -race`

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/store internal/catalog internal/core
git commit -m "feat(core): share schema-cache invalidations through the store

The Postgres store relays invalidations with LISTEN/NOTIFY on qh_catalog,
so DDL or a refresh on one instance drops the cached schema on all of
them; a lost listener reconnects and drops everything.

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 5: CLI — store selection, old master keys, and starting the listener

**Files:**
- Modify: `internal/cli/root.go` (the `--store` persistent flag; refuse `--store` with `--server`)
- Modify: `internal/cli/app.go` (`app.st` becomes `store.Store`; open via `store.Open`; `storeDSN()`)
- Modify: `internal/cli/serve.go` (`QH_MASTER_KEY_OLD`; `a.svc.Start(ctx)`)
- Modify: `internal/cli/query.go` (`qh mcp`: `a.svc.Start(ctx)`)
- Modify: `internal/cli/masterkey.go` (existence check through the store)
- Modify: `internal/cli/backend.go` (if it relies on `*store.SQLite`; it should not after `app.st` changes)
- Test: `internal/cli/store_flag_test.go`

**Interfaces:**
- Consumes:
  - `store.Open(ctx, dsn, dataDir) (store.Store, error)` and `store.ErrInvalidDSN` (Task 2);
  - `(*core.Service).Start(ctx)` (Task 4).
- Produces:
  - the global flag `--store DSN`, default `$QH_STORE_DSN`;
  - `func storeDSN() string`, the value every command uses;
  - `QH_MASTER_KEY_OLD`, comma-separated base64 keys accepted by `qh serve` (and `qh mcp`) as extra keys for opening sealed data.

- [ ] **Step 1: Write the failing tests**

`internal/cli/store_flag_test.go`. Use the package's `run(t, home, stdin, args...)` helper; check its signature with `grep -n 'func run(' internal/cli/*_test.go`.

```go
package cli

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/anderson-0/qh/internal/secret"
	"github.com/anderson-0/qh/internal/testutil"
)

var cliDBSeq atomic.Int64

func pgStoreDSN(t *testing.T) string {
	t.Helper()
	if testing.Short() {
		t.Skip("needs Docker")
	}
	testcontainers.SkipIfProviderIsNotHealthy(t)
	ctx := context.Background()
	ctr, err := tcpostgres.Run(ctx, "postgres:17-alpine", tcpostgres.WithDatabase("postgres"),
		tcpostgres.WithUsername("qh"), tcpostgres.WithPassword("store-pw-123"), tcpostgres.BasicWaitStrategies())
	t.Cleanup(func() { _ = testcontainers.TerminateContainer(ctr) })
	if err != nil {
		t.Fatal(err)
	}
	admin, _ := ctr.ConnectionString(ctx, "sslmode=disable")
	db, _ := sql.Open("pgx", admin)
	defer db.Close()
	name := fmt.Sprintf("qh_cli_%d", cliDBSeq.Add(1))
	if _, err := db.Exec("CREATE DATABASE " + name); err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(admin)
	u.Path = "/" + name
	return u.String()
}

// testMasterKey returns a fresh base64 32-byte master key.
func testMasterKey(t *testing.T) string {
	t.Helper()
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(b)
}

func TestStoreFlagSelectsPostgres(t *testing.T) {
	dsn := pgStoreDSN(t)
	homeA, homeB := t.TempDir(), t.TempDir() // two hosts sharing one store and one master key
	t.Setenv("QH_MASTER_KEY", testMasterKey(t))
	db := testutil.SQLiteFixture(t)
	if _, errOut, code := run(t, homeA, "", "--store", dsn, "conn", "add", "shop", "--engine", "sqlite", "--dsn", db); code != 0 {
		t.Fatal(errOut)
	}
	t.Setenv("QH_STORE_DSN", dsn)
	out, errOut, code := run(t, homeB, "", "conn", "ls")
	if code != 0 || !strings.Contains(out, "shop") {
		t.Fatalf("%d %q %q", code, out, errOut)
	}
	if _, err := os.Stat(homeB + "/qh.db"); err == nil {
		t.Fatal("a SQLite store was created although --store was set")
	}
}

func TestStoreFlagErrors(t *testing.T) {
	_, errOut, code := run(t, t.TempDir(), "", "--store", "mysql://x", "conn", "ls")
	if code == 0 || !strings.Contains(errOut, "postgres://") {
		t.Fatalf("%d %q", code, errOut)
	}
	_, errOut, code = run(t, t.TempDir(), "", "--store", "postgres://x@127.0.0.1:1/qh", "--server", "http://127.0.0.1:1", "conn", "ls")
	if code == 0 || !strings.Contains(errOut, "--store and --server cannot be used together") {
		t.Fatalf("%d %q", code, errOut)
	}
}

func TestOldMasterKeys(t *testing.T) {
	if _, err := oldMasterKeys("not-base64"); err == nil {
		t.Fatal("bad QH_MASTER_KEY_OLD accepted")
	}
	keys, err := oldMasterKeys(testMasterKey(t) + ", " + testMasterKey(t))
	if err != nil || len(keys) != 2 {
		t.Fatalf("%d %v", len(keys), err)
	}
	if keys, err := oldMasterKeys(""); err != nil || len(keys) != 0 {
		t.Fatalf("empty: %v %v", keys, err)
	}
}
```

Add `"crypto/rand"` and `"encoding/base64"` to the imports, and drop the `secret` import. The file needs no other helpers.

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/cli -count=1 -run 'StoreFlag|OldMasterKeys'`

Expected: FAIL (`unknown flag: --store`, `undefined: oldMasterKeys`).

- [ ] **Step 3: Implement**

`internal/cli/app.go`. Add a package variable that the root flag binds, plus the reader:

```go
// storeFlag is bound to the --store persistent flag by newRoot.
var storeFlag string

// storeDSN returns the store every command uses: --store, else
// $QH_STORE_DSN, else "" (the SQLite file in the data directory).
func storeDSN() string {
	if storeFlag != "" {
		return storeFlag
	}
	return os.Getenv("QH_STORE_DSN")
}
```

Change `app.st` to `store.Store`. In `openAppWith`, replace `store.OpenSQLite(filepath.Join(home, "qh.db"))` with:

```go
	st, err := store.Open(context.Background(), storeDSN(), home)
	if errors.Is(err, store.ErrInvalidDSN) {
		return nil, qerr.New(qerr.Invalid, "--store must be a postgres:// URL").WithHint("Leave it unset to use the SQLite file in the data directory")
	}
	if err != nil {
		return nil, err
	}
```

Still create `home` (`MkdirAll`), because `master.key` lives there in local mode.

`internal/cli/root.go`. In `newRoot`, register the flag on `pf`:

```go
	pf.StringVar(&storeFlag, "store", "", "postgres:// URL of a shared store (default $QH_STORE_DSN, else the SQLite file in the data directory)")
```

At the start of the `with` closure, after computing `srv`:

```go
			if srv != "" && storeDSN() != "" {
				return qerr.New(qerr.Invalid, "--store and --server cannot be used together").
					WithHint("--server talks to a qh server; --store opens a store directly")
			}
```

Add the same check to `refuseRemote` (`internal/cli/root.go`). Commands that run locally only (`serve`, `mcp`, `masterkey`) then refuse the combination too.

`newRoot` runs for every `Execute`. Reset `storeFlag = ""` at its start, so tests that call `Execute` repeatedly in one process start clean.

`internal/cli/serve.go`. Add the parser:

```go
// oldMasterKeys parses QH_MASTER_KEY_OLD: comma-separated base64 keys that
// can still open data sealed before a rotation.
func oldMasterKeys(v string) ([][]byte, error) {
	var out [][]byte
	for _, s := range strings.Split(v, ",") {
		if s = strings.TrimSpace(s); s == "" {
			continue
		}
		k, err := secret.ParseKey(s)
		if err != nil {
			return nil, qerr.New(qerr.Invalid, "QH_MASTER_KEY_OLD must hold comma-separated base64 32-byte keys")
		}
		out = append(out, k)
	}
	return out, nil
}
```

In the serve command, before `openAppWith`, parse it with `old, err := oldMasterKeys(os.Getenv("QH_MASTER_KEY_OLD"))` and call `openAppWith(dir, opts, old...)`. After `a.pools.Start(ctx)`, call `a.svc.Start(ctx)`.

`internal/cli/query.go`, the `qh mcp` command: apply the same `QH_MASTER_KEY_OLD` parsing and `a.svc.Start(ctx)` after `a.pools.Start(ctx)`.

`internal/cli/masterkey.go`. Keep the `qh.db` existence check only when `storeDSN() == ""`. With a store DSN, skip it: `openAppWith` fails if the store is unreachable.

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go vet ./... && go test ./internal/cli ./cmd/qh -count=1`

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/cli
git commit -m "feat(cli): choose the store with --store and accept old master keys

--store (or QH_STORE_DSN) points every command at a shared Postgres store;
qh serve and qh mcp listen for other instances' schema changes and accept
QH_MASTER_KEY_OLD so a fleet keeps working while connections are re-sealed.

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 6: `qh store copy`

**Files:**
- Create: `internal/cli/store.go` (the `store` command and its `copy` subcommand, registered in `newRoot`)
- Test: `internal/cli/store_copy_test.go`

**Interfaces:**
- Consumes:
  - `store.Open`;
  - `Store.ListWorkspaces`, `ListConnections`, `ListAllKeys`, `ListQueryLog`, `CreateWorkspace`, `ImportConnection`, `ImportKey`, `AppendQueryLog`;
  - `storeDSN()` (Task 5);
  - the `pgStoreDSN` test helper (Task 5).
- Produces: the command `qh store copy --to DSN [--with-query-log]`.

- [ ] **Step 1: Write the failing test**

`internal/cli/store_copy_test.go`:

```go
package cli

import (
	"strings"
	"testing"

	"github.com/anderson-0/qh/internal/testutil"
)

func TestStoreCopyMovesEverything(t *testing.T) {
	dsn := pgStoreDSN(t)
	home := t.TempDir()
	t.Setenv("QH_MASTER_KEY", testMasterKey(t)) // the same key on both sides
	db := testutil.SQLiteFixture(t)
	for _, args := range [][]string{
		{"workspace", "create", "team"},
		{"conn", "add", "shop", "--engine", "sqlite", "--dsn", db, "--description", "Shop"},
		{"key", "create", "--scope", "query", "--name", "agent"},
		{"query", "shop", "SELECT 1"}, // one query-log entry
	} {
		if _, errOut, code := run(t, home, "", args...); code != 0 {
			t.Fatalf("%v: %s", args, errOut)
		}
	}
	out, errOut, code := run(t, home, "", "store", "copy", "--to", dsn, "--with-query-log")
	if code != 0 {
		t.Fatalf("%d %s", code, errOut)
	}
	if !strings.Contains(out, "copied 2 workspaces, 1 connections, 1 keys, 1 query-log entries") {
		t.Fatalf("summary: %q", out)
	}
	// The copied store works with the same master key.
	t.Setenv("QH_STORE_DSN", dsn)
	if out, errOut, code := run(t, t.TempDir(), "", "query", "shop", "SELECT count(*) AS n FROM orders"); code != 0 || !strings.Contains(out, "250") {
		t.Fatalf("query on the copy: %d %q %q", code, out, errOut)
	}
	// Copying again into the now non-empty store is refused.
	t.Setenv("QH_STORE_DSN", "")
	if _, errOut, code := run(t, home, "", "store", "copy", "--to", dsn); code == 0 || !strings.Contains(errOut, "is not empty") {
		t.Fatalf("second copy: %d %q", code, errOut)
	}
}
```

Match `key create`'s real flags (`go run ./cmd/qh key create --help`) and the exact way `qh query` takes its arguments (`go run ./cmd/qh query --help`). If `key create` is admin-only in local mode, use the flags it accepts. `testMasterKey(t)` is defined in `store_flag_test.go` (Task 5).

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/cli -count=1 -run TestStoreCopy`

Expected: FAIL (`unknown command "store"`).

- [ ] **Step 3: Implement `internal/cli/store.go`**

```go
package cli

import (
	"context"
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/anderson-0/qh/internal/qerr"
	"github.com/anderson-0/qh/internal/store"
)

func storeCmd(home *string) *cobra.Command {
	c := &cobra.Command{Use: "store", Short: "Manage the store that holds connections and keys"}
	var to string
	var withLog bool
	cp := &cobra.Command{
		Use:   "copy",
		Short: "Copy everything from the current store to an empty Postgres store",
		Long: "Copies workspaces, connections and API keys (and, with --with-query-log, the query log) from the current\n" +
			"store (--store, or the SQLite file in the data directory) to the empty Postgres store --to. Sealed connection\n" +
			"settings are copied as they are: keep using the same master key with the new store.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := refuseRemote(cmd); err != nil {
				return err
			}
			if to == "" {
				return qerr.New(qerr.Invalid, "--to is required").WithHint("Give the postgres:// URL of the new store")
			}
			dir, err := homeDir(*home)
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			src, err := store.Open(ctx, storeDSN(), dir)
			if err != nil {
				return err
			}
			defer src.Close()
			dst, err := store.Open(ctx, to, "")
			if errors.Is(err, store.ErrInvalidDSN) {
				return qerr.New(qerr.Invalid, "--to must be a postgres:// URL")
			}
			if err != nil {
				return err
			}
			defer dst.Close()
			if err := requireEmpty(ctx, dst); err != nil {
				return err
			}
			n, err := copyStore(ctx, src, dst, withLog)
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "copied %d workspaces, %d connections, %d keys, %d query-log entries\n",
				n.workspaces, n.connections, n.keys, n.log)
			fmt.Fprintln(cmd.OutOrStdout(), "use the same master key with the new store")
			return nil
		},
	}
	cp.Flags().StringVar(&to, "to", "", "postgres:// URL of the empty store to copy into")
	cp.Flags().BoolVar(&withLog, "with-query-log", false, "copy the query log too")
	c.AddCommand(cp)
	return c
}

// requireEmpty fails unless dst holds only the default workspace and no
// connections or keys.
func requireEmpty(ctx context.Context, dst store.Store) error {
	ws, err := dst.ListWorkspaces(ctx)
	if err != nil {
		return err
	}
	keys, err := dst.ListAllKeys(ctx)
	if err != nil {
		return err
	}
	conns, err := dst.ListConnections(ctx, store.DefaultWorkspace)
	if err != nil {
		return err
	}
	if len(ws) != 1 || len(keys) != 0 || len(conns) != 0 {
		return qerr.New(qerr.Invalid, "the target store is not empty").
			WithHint("Copy into a new, empty database")
	}
	return nil
}

type copied struct{ workspaces, connections, keys, log int }

func copyStore(ctx context.Context, src, dst store.Store, withLog bool) (copied, error) {
	var n copied
	wss, err := src.ListWorkspaces(ctx)
	if err != nil {
		return n, err
	}
	for _, w := range wss {
		if w.ID != store.DefaultWorkspace {
			if _, err := dst.CreateWorkspace(ctx, w.ID); err != nil {
				return n, fmt.Errorf("workspace %s: %w", w.ID, err)
			}
		}
		n.workspaces++
		conns, err := src.ListConnections(ctx, w.ID)
		if err != nil {
			return n, err
		}
		for _, c := range conns {
			if err := dst.ImportConnection(ctx, c); err != nil {
				return n, fmt.Errorf("connection %s/%s: %w", w.ID, c.Name, err)
			}
			n.connections++
		}
		if withLog {
			entries, err := src.ListQueryLog(ctx, store.QueryLogFilter{WorkspaceID: w.ID})
			if err != nil {
				return n, err
			}
			for i := len(entries) - 1; i >= 0; i-- { // oldest first, so new ids keep the order
				e := *entries[i]
				e.ID = 0
				if err := dst.AppendQueryLog(ctx, &e); err != nil {
					return n, err
				}
				n.log++
			}
		}
	}
	keys, err := src.ListAllKeys(ctx)
	if err != nil {
		return n, err
	}
	for _, k := range keys {
		if err := dst.ImportKey(ctx, k); err != nil {
			return n, fmt.Errorf("key %s: %w", k.Prefix, err)
		}
		n.keys++
	}
	return n, nil
}
```

Register it in `newRoot`'s `root.AddCommand(...)` list as `storeCmd(&home)`.

Copy order matters, because keys reference workspaces: workspaces and connections first, keys last.

- [ ] **Step 4: Run the test to verify it passes**

Run: `go vet ./... && go test ./internal/cli -count=1 -run 'TestStoreCopy|TestStoreFlag'`

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/cli
git commit -m "feat(cli): qh store copy moves a store into an empty Postgres store

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 7: Two instances on one store, and docs

**Files:**
- Create: `internal/httpapi/multi_instance_test.go`
- Modify: `README.md` (new section "Running several instances")

**Interfaces:**
- Consumes everything above.
- Produces: tests and docs only.

- [ ] **Step 1: Write the integration test**

`internal/httpapi/multi_instance_test.go` builds two complete servers, A and B, on one Postgres store, each with its own `core.Service`, pool manager and catalog cache, and both with the same keyring. Follow `internal/httpapi/helpers_test.go` (`newEnv`) to build an `httptest.Server` around `New(Config{...})`. Write a helper `newInstance(t, st store.Store, keys *secret.Keyring) *env` with the same fields `newEnv` returns, then:

1. **Bootstrap.** Call `auth.Bootstrap` concurrently for A and B on the empty store with a fixed token. Both succeed, exactly one key exists, and the token works on both servers.
2. **A connection is shared.** Create connection `shop` through A's REST API (a SQLite fixture DSN in local-mode service options, so no netguard is involved). Query it through B: `POST /api/v1/connections/shop/query` with `SELECT count(*) AS n FROM orders` returns 250.
3. **Revocation is immediate.** Create a query-scope key through A, use it on B (200), delete it through A, and use it again on B: 401.
4. **The schema cache is shared.** Both services must run `Start(ctx)`. Make the connection read-write. Fetch `GET /api/v1/connections/shop/schema` on B (filling B's cache), run `CREATE TABLE multi_t (x int)` through A's query endpoint, then poll B's `schema?pattern=multi_t` until it lists the table, within 5 s.
5. **The query log interleaves.** Run queries alternately through A and B (A, B, A). `GET /api/v1/log` on A lists them newest first in that insert order: the `ids` decrease, and the third entry is the oldest.

The test skips under `-short` and starts one `postgres:17-alpine` container, following `internal/store/postgres_test.go`'s helper.

- [ ] **Step 2: Document**

In `README.md`, add a section "Running several instances" after the server-mode documentation:

````markdown
## Running several instances

By default `qh serve` keeps its store in a SQLite file in `--data-dir`. To run several instances behind a load balancer, point them all at one Postgres database:

```bash
export QH_STORE_DSN='postgres://qh@db.internal:5432/qh?sslmode=verify-full'
export QH_STORE_PASSWORD_FILE=/run/secrets/qh-store-password   # or put the password in the URL
export QH_MASTER_KEY=…                                          # the same key on every instance
qh serve --http :8765 --allow-cidr 10.0.0.0/8
```

- **Store contents.** qh creates its tables in a `qh` schema on first start. Instances starting together apply each migration once and create one admin key.
- **Changes are shared.** A connection, key or workspace changed through any instance applies to all of them on the next call. A key revoked on one is refused by all at once. A schema change on one drops the cached schema everywhere.
- **No stickiness needed.** MCP over HTTP is stateless, so the load balancer needs no stickiness.
- **Limits are per instance.** That covers the per-key concurrency limit, each connection's `pool_size` (so a database sees up to instances × `pool_size` connections), and the total connection cap. Size them for the instance count.
- **Query-log times come from each instance's clock.** Keep clocks in sync.
- **The store's URL must contain everything.** Server mode clears `PG*` variables at startup.

**Moving an existing install:** stop it, then copy its store into an empty database. Keep the same master key.

```bash
qh store copy --home /var/lib/qh --to "$QH_STORE_DSN" --with-query-log
```

**Rotating the master key without downtime:**

1. Restart every instance with `QH_MASTER_KEY=<new>` and `QH_MASTER_KEY_OLD=<old>`.
2. From any host, with `QH_MASTER_KEY=<new>`, run `qh masterkey rotate --store "$QH_STORE_DSN" --old-key-file old.key`.
3. Restart every instance without `QH_MASTER_KEY_OLD`.
````

Check the flags against the CLI (`go run ./cmd/qh store copy --help`, `masterkey rotate --help`, `serve --help`) and fix any mismatch in the text.

- [ ] **Step 3: Run everything**

Run: `go vet ./... && go test ./... -count=1 && (cd bench && go test ./... -short -count=1)`

Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add internal/httpapi/multi_instance_test.go README.md
git commit -m "test: two instances on one Postgres store; document running several instances

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```
