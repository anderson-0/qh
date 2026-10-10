# qh Plan 3: Server mode — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** `qh serve` runs qh as an HTTP server. It offers MCP over HTTP and a REST API for connections, workspaces and API keys. Every request is authenticated and scoped to a workspace. The server cannot be used to reach loopback or cloud-metadata addresses, or to read files outside an allowed directory.

**Architecture:**
- **New packages:**
  - `internal/auth`: API keys, scopes, bootstrap.
  - `internal/netguard`: dial-time address policy.
  - `internal/httpapi`: routes, middleware, REST handlers, MCP handler, OpenAPI document.
- **Store:** gains workspaces and keys (migration 2).
- **Core:** gains workspace operations, structured query/catalog accessors, and server-mode options.
- **Engines:** accept a dialer (Postgres, MySQL) and a file root (SQLite) through `engine.ConnSpec`.
- **CLI:** gains `qh serve`, `qh workspace` and `qh key`.

**Tech Stack:** Go 1.27 standard library (`net/http` method+wildcard routing, `log/slog`, `net/netip`, `crypto/subtle`, `math/big`). The existing `github.com/modelcontextprotocol/go-sdk` v1.8.0 (`mcp.NewStreamableHTTPHandler`, `mcp.StreamableClientTransport`). No new third-party dependencies.

**Spec:** `docs/superpowers/specs/2026-10-09-qh-design.md`: §5.1 (`api_keys`), §6.1, §6.2, §6.4, §7, §8 (`serve`, `key`, `workspace`). Plans 1 and 2 (`docs/superpowers/plans/2026-10-09-qh-plan-1-local-mvp.md`, `…-2026-10-10-qh-plan-2-mysql.md`) built everything this plan extends.

## Plan series (renumbered)

| Plan | Delivers |
|---|---|
| 1, 2 | Done: local MVP; MySQL/MariaDB |
| **3 (this plan)** | `qh serve` (local and server mode), MCP over HTTP, REST API for connections, workspaces and keys, auth and scopes, per-key concurrency limit, netguard, `--sqlite-root`, OpenAPI, `/healthz`, `/readyz` |
| 4 | Query log (§5.1), `qh masterkey rotate` (§5.2), structured connection fields and `tls` (§7), CLI remote mode `--server/--key` (§8) |
| 5 | Benchmark against dbhub (§9.2) |

## Spec adjustments made while planning

1. **A workspace's id is its name.** Names follow the connection-name rule. The `default` workspace cannot be deleted.
2. **Local mode** (`qh serve` without `--http`) listens on `127.0.0.1:8765` with no authentication, using the `default` workspace. Server mode (`--http ADDR`) always requires API keys, so "refuses non-loopback without auth" holds by construction.
3. **Unix-socket database connections are refused in server mode.** The spec is silent, but a socket would bypass the address policy.
4. **`last_used_at` is written at most once per minute per key,** so authentication does not turn every request into a write.
5. **Unexpected errors** (store failures and other non-`qerr` errors) return HTTP 500 `internal` with a generic message, and are logged. They are not reported as `query_error`.
6. **JSON query results** encode binary values as `{"bytes": N}`, NULL as `null`, and everything else as strings.
7. **`POST /api/v1/connections/{name}/test`** tests a stored connection. The spec's `qh conn test` needs it remotely in Plan 4.
8. **Local-mode MCP over HTTP** acts as an admin of the `default` workspace. An `X-QH-Workspace` header may pick another workspace.

## Global Constraints

Plans 1 and 2's Global Constraints still apply: one-line `error[<category>]: …` agent errors, no secrets in output or logs, pure Go, and Docker tests skip under `-short`. Additionally:

- **API key format:** `qh_<prefix>_<secret>`.
  - `prefix` is 8 characters from `[a-z0-9]`.
  - `secret` is 32 random bytes written in base62 (`math/big` `Text(62)`).
  - Only the SHA-256 of `secret` is stored, and it is compared with `subtle.ConstantTimeCompare`.
- **Scopes:** `query`, `manage`, `admin`; `admin` implies the other two.
- **Workspace keys** belong to one workspace. **Admin keys** belong to none and name a workspace with the `X-QH-Workspace` header.
- **Server mode requires `QH_MASTER_KEY`.**
  - On first start, if the store has no keys, it creates one admin key.
  - It uses `QH_BOOTSTRAP_ADMIN_KEY` when that is set to a valid key. Otherwise it generates one and prints it once to stderr as `admin API key (shown once): qh_…`.
- **Per-key concurrency:** at most 4 limited requests at once (query, schema and MCP). A request waits up to 10 s for a slot, then gets `busy` (HTTP 429, `Retry-After: 1`).
- **Network policy (server mode):**
  - Default deny list: `127.0.0.0/8`, `::1/128`, `0.0.0.0/8`, `::/128`, `169.254.0.0/16`, `fe80::/10`, `fd00:ec2::254/128`, `100.100.100.200/32`.
  - `--allow-cidr` takes precedence over the deny list; `--deny-cidr` adds to it.
  - Addresses are checked after DNS resolution, and the checked IP is the one dialled.
  - IPv4-mapped IPv6 addresses are unmapped before the check.
- **SQLite in server mode** is disabled unless `--sqlite-root DIR` is given. Paths must stay inside `DIR` both lexically and after resolving symlinks.
- **HTTP errors** are `application/problem+json` objects `{"type": <category>, "title": <message>, "detail": <hint>, "status": <code>}`, with this status mapping:

  | Category | Status |
  |---|---|
  | `auth` | 401 (plus `WWW-Authenticate: Bearer`) |
  | `forbidden` | 403 |
  | `unknown_source` | 404 |
  | `invalid` | 400 |
  | `read_only` | 403 |
  | `timeout` | 504 |
  | `connection_failed` | 502 |
  | `query_error` | 422 |
  | `busy` | 429 |
  | unexpected errors | 500, type `internal` |
- **Request handling:**
  - Request bodies are capped at 1 MiB, and JSON bodies reject unknown fields.
  - Logs (JSON via `log/slog` to stderr) record method, path, status, duration and the key prefix. They never record bodies, connection strings or secrets.
- **Routing** uses the standard library only: `http.ServeMux` with patterns like `"POST /api/v1/connections"` and `r.PathValue("name")`.
- **`qh serve` output:** prints `qh listening on http://<addr> (<local|server> mode)` to stderr, and shuts down gracefully (10 s) on SIGINT/SIGTERM.

## Review Focus

1. **An admin key calling a workspace endpoint without `X-QH-Workspace`** must get a clear 400, never act on `default`. → Task 6 `TestAdminNeedsWorkspaceHeader`.
2. **A key that is deleted or expires** must stop working on the next request, with no caching of the check. → Task 2 `TestAuthenticateExpiredAndDeleted`.
3. **Disguised loopback addresses must be refused:** `localhost`, `127.1`, `[::ffff:127.0.0.1]`, and a public-looking name resolving to loopback. → Task 3 `TestDialRefusesLoopbackForms`.
4. **SQLite path tricks must be rejected:** `../`, absolute paths outside the root, and a symlink inside the root pointing out. → Task 4 `TestFileRoot`.
5. **One key flooding the server** must get 429 with `Retry-After` while another key keeps working. → Task 6 `TestPerKeyLimit`.

---

## File map

```
internal/store/store.go, sqlite.go        + Workspace, APIKey, migration 2, workspace/key methods, Ping
internal/auth/keys.go                     token format, hashing, scopes, Principal, ResolveWorkspace
internal/auth/authenticator.go            Authenticator (lookup, constant-time compare, expiry, throttled touch)
internal/auth/manage.go                   CreateKey, ListKeys, DeleteKey, Bootstrap, ParseExpiry
internal/netguard/netguard.go             Policy, Allowed, DialContext
internal/engine/engine.go                 ConnSpec.Dial, ConnSpec.FileRoot
internal/engine/postgres/postgres.go      use spec.Dial
internal/engine/mysql/conn.go             use spec.Dial
internal/engine/sqlite/sqlite.go          enforce spec.FileRoot
internal/core/service.go, connections.go  Options, spec plumbing, QueryResult, Catalog, TestInput
internal/core/workspaces.go               CreateWorkspace, ListWorkspaces, DeleteWorkspace, WorkspaceExists
internal/format/schema.go                 Matching (exported matcher)
internal/httpapi/api.go                   Config, New, route table, middleware, limiter, problem responses
internal/httpapi/connections.go           connection, query and schema handlers + JSON shapes
internal/httpapi/admin.go                 workspace and key handlers
internal/httpapi/mcp.go                   MCP over HTTP
internal/httpapi/openapi.json, openapi.go OpenAPI 3.1 document (embedded) and its route
internal/cli/serve.go                     qh serve
internal/cli/admin.go                     qh workspace, qh key
internal/cli/app.go                       openAppWith(home, core.Options)
cmd/qh/serve_e2e_test.go                  server-mode end-to-end test against the built binary
README.md                                 server mode section
```

---

### Task 1: Workspaces and API keys in the store

**Files:**
- Modify: `internal/store/store.go`, `internal/store/sqlite.go`
- Test: `internal/store/keys_test.go`

**Interfaces:**
- Consumes: existing `SQLite`, `migrations`, `isUnique`, `scanner`, `s.now`, `ErrNotFound`/`ErrExists`.
- Produces:
  - `type store.Workspace struct { ID string; CreatedAt time.Time }`. The ID is the workspace name.
  - `type store.APIKey struct { ID, WorkspaceID, Prefix string; Hash []byte; Scopes []string; Name string; CreatedAt time.Time; ExpiresAt, LastUsedAt *time.Time }`. `WorkspaceID == ""` means an admin key.
  - New `Store` interface methods:
    - `CreateWorkspace(ctx, id string) (*Workspace, error)` — `ErrExists` for a duplicate.
    - `ListWorkspaces(ctx) ([]*Workspace, error)` — ordered by id.
    - `DeleteWorkspace(ctx, id string) error` — `ErrNotFound` when missing; cascades to the workspace's connections and keys.
    - `CreateKey(ctx, k *APIKey) error` — `ErrExists` for a duplicate prefix; `ErrNotFound` for an unknown workspace; sets `k.CreatedAt`.
    - `GetKeyByPrefix(ctx, prefix string) (*APIKey, error)`.
    - `ListKeys(ctx, workspaceID string) ([]*APIKey, error)` — `""` lists admin keys.
    - `ListAllKeys(ctx) ([]*APIKey, error)`.
    - `DeleteKey(ctx, id string) error` — `ErrNotFound` when missing.
    - `TouchKey(ctx, id string, at time.Time) error`.
    - `CountKeys(ctx) (int, error)`.
    - `Ping(ctx) error`.

- [ ] **Step 1: Write the failing test**

`internal/store/keys_test.go`:
```go
package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func TestWorkspaces(t *testing.T) {
	ctx := context.Background()
	s, _ := openTemp(t)
	if _, err := s.CreateWorkspace(ctx, "team"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateWorkspace(ctx, "team"); !errors.Is(err, ErrExists) {
		t.Fatalf("duplicate: %v", err)
	}
	list, err := s.ListWorkspaces(ctx)
	if err != nil || len(list) != 2 || list[0].ID != "default" || list[1].ID != "team" || list[1].CreatedAt.IsZero() {
		t.Fatalf("list: %+v %v", list, err)
	}
	c := sample("shop")
	c.WorkspaceID = "team"
	if err := s.CreateConnection(ctx, c); err != nil {
		t.Fatal(err)
	}
	k := &APIKey{ID: NewID(), WorkspaceID: "team", Prefix: "abcd1234", Hash: []byte{1}, Scopes: []string{"query"}}
	if err := s.CreateKey(ctx, k); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteWorkspace(ctx, "team"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetConnection(ctx, "team", "shop"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("connection survived its workspace: %v", err)
	}
	if _, err := s.GetKeyByPrefix(ctx, "abcd1234"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("key survived its workspace: %v", err)
	}
	if err := s.DeleteWorkspace(ctx, "team"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("delete twice: %v", err)
	}
}

func TestKeys(t *testing.T) {
	ctx := context.Background()
	s, _ := openTemp(t)
	if n, err := s.CountKeys(ctx); err != nil || n != 0 {
		t.Fatalf("count = %d %v", n, err)
	}
	exp := time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)
	admin := &APIKey{ID: NewID(), Prefix: "admin001", Hash: []byte{9, 9}, Scopes: []string{"admin"}, Name: "bootstrap"}
	ws := &APIKey{ID: NewID(), WorkspaceID: "default", Prefix: "work0001", Hash: []byte{1, 2}, Scopes: []string{"query", "manage"}, Name: "ci", ExpiresAt: &exp}
	for _, k := range []*APIKey{admin, ws} {
		if err := s.CreateKey(ctx, k); err != nil {
			t.Fatal(err)
		}
		if k.CreatedAt.IsZero() {
			t.Fatal("CreatedAt not set")
		}
	}
	dup := *ws
	dup.ID = NewID()
	if err := s.CreateKey(ctx, &dup); !errors.Is(err, ErrExists) {
		t.Fatalf("duplicate prefix: %v", err)
	}
	orphan := &APIKey{ID: NewID(), WorkspaceID: "nope", Prefix: "orphan01", Hash: []byte{1}, Scopes: []string{"query"}}
	if err := s.CreateKey(ctx, orphan); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown workspace: %v", err)
	}
	got, err := s.GetKeyByPrefix(ctx, "work0001")
	if err != nil {
		t.Fatal(err)
	}
	if got.WorkspaceID != "default" || !reflect.DeepEqual(got.Scopes, []string{"query", "manage"}) || got.Name != "ci" ||
		got.ExpiresAt == nil || !got.ExpiresAt.Equal(exp) || got.LastUsedAt != nil || !reflect.DeepEqual(got.Hash, []byte{1, 2}) {
		t.Fatalf("got %+v", got)
	}
	used := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	if err := s.TouchKey(ctx, got.ID, used); err != nil {
		t.Fatal(err)
	}
	got, _ = s.GetKeyByPrefix(ctx, "work0001")
	if got.LastUsedAt == nil || !got.LastUsedAt.Equal(used) {
		t.Fatalf("last used %v", got.LastUsedAt)
	}
	if l, _ := s.ListKeys(ctx, ""); len(l) != 1 || l[0].Prefix != "admin001" || l[0].WorkspaceID != "" {
		t.Fatalf("admin keys %+v", l)
	}
	if l, _ := s.ListKeys(ctx, "default"); len(l) != 1 || l[0].Prefix != "work0001" {
		t.Fatalf("workspace keys %+v", l)
	}
	if l, _ := s.ListAllKeys(ctx); len(l) != 2 {
		t.Fatalf("all keys %+v", l)
	}
	if n, _ := s.CountKeys(ctx); n != 2 {
		t.Fatalf("count %d", n)
	}
	if err := s.DeleteKey(ctx, admin.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteKey(ctx, admin.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("delete twice: %v", err)
	}
	if err := s.Ping(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestUpgradeFromVersion1(t *testing.T) {
	ctx := context.Background()
	s, path := openTemp(t)
	s.Close()
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{"DROP TABLE api_keys", "PRAGMA user_version = 1"} {
		if _, err := raw.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	raw.Close()
	s2, err := OpenSQLite(filepath.Clean(path))
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	if err := s2.CreateKey(ctx, &APIKey{ID: NewID(), Prefix: "upgr0001", Hash: []byte{1}, Scopes: []string{"admin"}}); err != nil {
		t.Fatalf("v1 store not upgraded: %v", err)
	}
}
```

- [ ] **Step 2: Run it to verify it fails**

Run: `go test ./internal/store/ -run 'TestWorkspaces|TestKeys|TestUpgradeFromVersion1'`
Expected: FAIL — `s.CreateWorkspace undefined`.

- [ ] **Step 3: Implement**

Append to `internal/store/store.go`:
```go
// Workspace isolates connections and keys. Its ID is its name.
type Workspace struct {
	ID        string
	CreatedAt time.Time
}

// APIKey is a stored API key. Only the SHA-256 of the key's secret is kept.
type APIKey struct {
	ID          string
	WorkspaceID string // empty for admin keys, which belong to no workspace
	Prefix      string
	Hash        []byte
	Scopes      []string
	Name        string
	CreatedAt   time.Time
	ExpiresAt   *time.Time
	LastUsedAt  *time.Time
}
```
Then add these methods to the `Store` interface, after `ListConnections`:
```go
	CreateWorkspace(ctx context.Context, id string) (*Workspace, error)
	ListWorkspaces(ctx context.Context) ([]*Workspace, error)
	DeleteWorkspace(ctx context.Context, id string) error
	CreateKey(ctx context.Context, k *APIKey) error
	GetKeyByPrefix(ctx context.Context, prefix string) (*APIKey, error)
	ListKeys(ctx context.Context, workspaceID string) ([]*APIKey, error)
	ListAllKeys(ctx context.Context) ([]*APIKey, error)
	DeleteKey(ctx context.Context, id string) error
	TouchKey(ctx context.Context, id string, at time.Time) error
	CountKeys(ctx context.Context) (int, error)
	Ping(ctx context.Context) error
```

In `internal/store/sqlite.go`, append a second element to `migrations`, after the closing `},` of the first:
```go
	{
		`CREATE TABLE api_keys (
			id TEXT PRIMARY KEY,
			workspace_id TEXT REFERENCES workspaces(id) ON DELETE CASCADE,
			prefix TEXT NOT NULL UNIQUE,
			hash BLOB NOT NULL,
			scopes TEXT NOT NULL,
			name TEXT NOT NULL DEFAULT '',
			created_at TEXT NOT NULL,
			expires_at TEXT,
			last_used_at TEXT
		)`,
		`CREATE INDEX api_keys_workspace ON api_keys (workspace_id)`,
	},
```
Then append these functions to `internal/store/sqlite.go`. Add `"strings"` to its imports if it is not there already.
```go
func isForeignKey(err error) bool {
	var se *msqlite.Error
	return errors.As(err, &se) && se.Code() == sqlitelib.SQLITE_CONSTRAINT_FOREIGNKEY
}

func formatTime(t *time.Time) any {
	if t == nil {
		return nil
	}
	return t.UTC().Format(time.RFC3339Nano)
}

func parseTime(s sql.NullString) (*time.Time, error) {
	if !s.Valid {
		return nil, nil
	}
	t, err := time.Parse(time.RFC3339Nano, s.String)
	if err != nil {
		return nil, err
	}
	return &t, nil
}

func (s *SQLite) Ping(ctx context.Context) error { return s.db.PingContext(ctx) }

func (s *SQLite) CreateWorkspace(ctx context.Context, id string) (*Workspace, error) {
	now := s.now()
	_, err := s.db.ExecContext(ctx, `INSERT INTO workspaces (id, name, created_at) VALUES (?, ?, ?)`,
		id, id, now.Format(time.RFC3339Nano))
	if isUnique(err) {
		return nil, ErrExists
	}
	if err != nil {
		return nil, err
	}
	return &Workspace{ID: id, CreatedAt: now}, nil
}

func (s *SQLite) ListWorkspaces(ctx context.Context) ([]*Workspace, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, created_at FROM workspaces ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Workspace
	for rows.Next() {
		var w Workspace
		var created string
		if err := rows.Scan(&w.ID, &created); err != nil {
			return nil, err
		}
		if w.CreatedAt, err = time.Parse(time.RFC3339Nano, created); err != nil {
			return nil, err
		}
		out = append(out, &w)
	}
	return out, rows.Err()
}

func (s *SQLite) DeleteWorkspace(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM workspaces WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil {
		return err
	} else if n == 0 {
		return ErrNotFound
	}
	return nil
}

const keyColumns = `id, workspace_id, prefix, hash, scopes, name, created_at, expires_at, last_used_at`

func (s *SQLite) CreateKey(ctx context.Context, k *APIKey) error {
	now := s.now()
	var ws any
	if k.WorkspaceID != "" {
		ws = k.WorkspaceID
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO api_keys (`+keyColumns+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, NULL)`,
		k.ID, ws, k.Prefix, k.Hash, strings.Join(k.Scopes, ","), k.Name, now.Format(time.RFC3339Nano), formatTime(k.ExpiresAt))
	switch {
	case isUnique(err):
		return ErrExists
	case isForeignKey(err):
		return ErrNotFound
	case err != nil:
		return err
	}
	k.CreatedAt = now
	return nil
}

func scanKey(r scanner) (*APIKey, error) {
	var k APIKey
	var ws, expires, used sql.NullString
	var scopes, created string
	if err := r.Scan(&k.ID, &ws, &k.Prefix, &k.Hash, &scopes, &k.Name, &created, &expires, &used); err != nil {
		return nil, err
	}
	k.WorkspaceID = ws.String
	if scopes != "" {
		k.Scopes = strings.Split(scopes, ",")
	}
	var err error
	if k.CreatedAt, err = time.Parse(time.RFC3339Nano, created); err != nil {
		return nil, err
	}
	if k.ExpiresAt, err = parseTime(expires); err != nil {
		return nil, err
	}
	if k.LastUsedAt, err = parseTime(used); err != nil {
		return nil, err
	}
	return &k, nil
}

func (s *SQLite) GetKeyByPrefix(ctx context.Context, prefix string) (*APIKey, error) {
	k, err := scanKey(s.db.QueryRowContext(ctx, `SELECT `+keyColumns+` FROM api_keys WHERE prefix = ?`, prefix))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return k, err
}

func (s *SQLite) listKeys(ctx context.Context, where string, args ...any) ([]*APIKey, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+keyColumns+` FROM api_keys `+where+` ORDER BY created_at, id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*APIKey
	for rows.Next() {
		k, err := scanKey(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

func (s *SQLite) ListKeys(ctx context.Context, workspaceID string) ([]*APIKey, error) {
	if workspaceID == "" {
		return s.listKeys(ctx, `WHERE workspace_id IS NULL`)
	}
	return s.listKeys(ctx, `WHERE workspace_id = ?`, workspaceID)
}

func (s *SQLite) ListAllKeys(ctx context.Context) ([]*APIKey, error) { return s.listKeys(ctx, ``) }

func (s *SQLite) DeleteKey(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM api_keys WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil {
		return err
	} else if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *SQLite) TouchKey(ctx context.Context, id string, at time.Time) error {
	_, err := s.db.ExecContext(ctx, `UPDATE api_keys SET last_used_at = ? WHERE id = ?`, at.UTC().Format(time.RFC3339Nano), id)
	return err
}

func (s *SQLite) CountKeys(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM api_keys`).Scan(&n)
	return n, err
}
```

- [ ] **Step 4: Run the tests**

Run: `go test -race ./internal/store/ && go vet ./...`
Expected: PASS. Every other package still builds; nothing else implements `Store`.

- [ ] **Step 5: Commit**

```bash
git add internal/store
git commit -m "feat(store): add workspaces and API keys"
```

---

### Task 2: API keys, scopes and authentication

**Files:**
- Create: `internal/auth/keys.go`, `internal/auth/authenticator.go`, `internal/auth/manage.go`
- Test: `internal/auth/auth_test.go`

**Interfaces:**
- Consumes: `store.Store` (key and workspace methods from Task 1), `store.NewID`, `store.DefaultWorkspace`, `store.ErrNotFound`/`ErrExists`, `qerr`.
- Produces:
  - Scopes: `auth.ScopeQuery = "query"`, `auth.ScopeManage = "manage"`, `auth.ScopeAdmin = "admin"`.
  - `type auth.Principal struct { KeyID, Prefix, Workspace string; Scopes map[string]bool }` with `Has(scope string) bool` (admin implies every scope) and `IsAdmin() bool`.
  - `auth.Local() *Principal`: KeyID `"local"`, Workspace `store.DefaultWorkspace`, scope admin.
  - `auth.ResolveWorkspace(p *Principal, header string) (string, error)`.
  - Tokens: `auth.NewToken() (token, prefix string, hash []byte, err error)`, `auth.ParseToken(token string) (prefix, secret string, ok bool)`, `auth.HashSecret(secret string) []byte`.
  - `auth.NewAuthenticator(st store.Store) *Authenticator`, with `(*Authenticator).Authenticate(ctx, header string) (*Principal, error)`.
  - Key management:
    - `type auth.KeyInput struct { Workspace string; Scopes []string; Name string; ExpiresIn time.Duration }`.
    - `type auth.KeyInfo struct { ID, Prefix, Name, Workspace string; Scopes []string; CreatedAt time.Time; ExpiresAt, LastUsedAt *time.Time }`.
    - `auth.CreateKey(ctx, st, in KeyInput) (string, *KeyInfo, error)`.
    - `auth.ListKeys(ctx, st, workspace string, all bool) ([]*KeyInfo, error)`.
    - `auth.DeleteKey(ctx, st, id string) error`.
    - `auth.Bootstrap(ctx, st, fixed string) (token string, created bool, err error)`.
    - `auth.ParseExpiry(s string) (time.Duration, error)` accepts `""`, Go durations (`720h`) and whole days (`90d`).

- [ ] **Step 1: Write the failing test**

`internal/auth/auth_test.go`:
```go
package auth

import (
	"context"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/anderson-0/qh/internal/qerr"
	"github.com/anderson-0/qh/internal/store"
)

func newStore(t *testing.T) *store.SQLite {
	t.Helper()
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "qh.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

var tokenRe = regexp.MustCompile(`^qh_[a-z0-9]{8}_[0-9A-Za-z]{30,}$`)

func TestTokens(t *testing.T) {
	tok, prefix, hash, err := NewToken()
	if err != nil || !tokenRe.MatchString(tok) || len(hash) != 32 {
		t.Fatalf("token %q hash %d %v", tok, len(hash), err)
	}
	p, secret, ok := ParseToken(tok)
	if !ok || p != prefix || string(HashSecret(secret)) != string(hash) {
		t.Fatalf("parse: %q %q %v", p, secret, ok)
	}
	for _, bad := range []string{"", "qh_", "qh_short_x", "xx_abcdefgh_secret", "qh_ABCDEFGH_secret", "qh_abcdefgh_", "qh_abcdefgh_sec ret"} {
		if _, _, ok := ParseToken(bad); ok {
			t.Errorf("ParseToken(%q) accepted", bad)
		}
	}
	t2, _, _, _ := NewToken()
	if t2 == tok {
		t.Fatal("tokens repeat")
	}
}

func TestPrincipal(t *testing.T) {
	q := &Principal{Workspace: "team", Scopes: map[string]bool{ScopeQuery: true}}
	if !q.Has(ScopeQuery) || q.Has(ScopeManage) || q.IsAdmin() {
		t.Fatal("query key scopes")
	}
	admin := &Principal{Scopes: map[string]bool{ScopeAdmin: true}}
	if !admin.Has(ScopeQuery) || !admin.Has(ScopeManage) || !admin.IsAdmin() {
		t.Fatal("admin implies all scopes")
	}
	if ws, err := ResolveWorkspace(q, "other"); err != nil || ws != "team" {
		t.Fatalf("workspace keys ignore the header: %q %v", ws, err)
	}
	if ws, err := ResolveWorkspace(admin, " other "); err != nil || ws != "other" {
		t.Fatalf("admin header: %q %v", ws, err)
	}
	if _, err := ResolveWorkspace(admin, ""); err == nil || !strings.HasPrefix(err.Error(), "error[invalid]: admin keys must name a workspace") {
		t.Fatalf("admin without header: %v", err)
	}
	if ws, err := ResolveWorkspace(Local(), ""); err != nil || ws != store.DefaultWorkspace {
		t.Fatalf("local: %q %v", ws, err)
	}
}

func TestAuthenticate(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	tok, info, err := CreateKey(ctx, st, KeyInput{Workspace: "default", Scopes: []string{ScopeQuery}, Name: "ci"})
	if err != nil {
		t.Fatal(err)
	}
	a := NewAuthenticator(st)
	p, err := a.Authenticate(ctx, "Bearer "+tok)
	if err != nil || p.KeyID != info.ID || p.Workspace != "default" || !p.Has(ScopeQuery) || p.Prefix != info.Prefix {
		t.Fatalf("authenticate: %+v %v", p, err)
	}
	k, _ := st.GetKeyByPrefix(ctx, info.Prefix)
	if k.LastUsedAt == nil {
		t.Fatal("last_used_at not recorded")
	}
	prefix, _, _ := ParseToken(tok)
	for header, want := range map[string]string{
		"":                                "missing or malformed Authorization header",
		"Basic abc":                       "missing or malformed Authorization header",
		"Bearer nope":                     "invalid API key",
		"Bearer qh_" + prefix + "_wrong0": "invalid API key",
		"Bearer qh_zzzzzzzz_abc":          "invalid API key",
	} {
		_, err := a.Authenticate(ctx, header)
		if e := qerr.From(err); err == nil || e.Category != qerr.Auth || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: %v", header, err)
		}
	}
}

func TestAuthenticateExpiredAndDeleted(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	a := NewAuthenticator(st)
	now := time.Now()
	a.now = func() time.Time { return now }
	tok, info, err := CreateKey(ctx, st, KeyInput{Workspace: "default", Scopes: []string{ScopeQuery}, ExpiresIn: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.Authenticate(ctx, "Bearer "+tok); err != nil {
		t.Fatal(err)
	}
	now = now.Add(2 * time.Hour)
	if _, err := a.Authenticate(ctx, "Bearer "+tok); err == nil || !strings.Contains(err.Error(), "API key expired") {
		t.Fatalf("expired: %v", err)
	}
	tok2, info2, _ := CreateKey(ctx, st, KeyInput{Workspace: "default", Scopes: []string{ScopeQuery}})
	if _, err := a.Authenticate(ctx, "Bearer "+tok2); err != nil {
		t.Fatal(err)
	}
	if err := DeleteKey(ctx, st, info2.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Authenticate(ctx, "Bearer "+tok2); err == nil || qerr.From(err).Category != qerr.Auth {
		t.Fatalf("deleted key still works: %v", err)
	}
	_ = info
}

func TestCreateKeyValidation(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	cases := map[string]KeyInput{
		"error[invalid]: at least one scope is required":        {Workspace: "default"},
		`error[invalid]: unknown scope "root"`:                  {Workspace: "default", Scopes: []string{"root"}},
		"error[invalid]: admin keys cannot belong to a workspace": {Workspace: "default", Scopes: []string{ScopeAdmin}},
		"error[invalid]: workspace keys need a workspace":       {Scopes: []string{ScopeQuery}},
		`error[unknown_source]: no workspace named "nope"`:      {Workspace: "nope", Scopes: []string{ScopeQuery}},
	}
	for want, in := range cases {
		if _, _, err := CreateKey(ctx, st, in); err == nil || !strings.HasPrefix(err.Error(), want) {
			t.Errorf("%+v: %v, want prefix %q", in, err, want)
		}
	}
	if _, info, err := CreateKey(ctx, st, KeyInput{Scopes: []string{ScopeAdmin}, Name: "ops"}); err != nil || info.Workspace != "" {
		t.Fatalf("admin key: %+v %v", info, err)
	}
	if list, _ := ListKeys(ctx, st, "", true); len(list) != 1 {
		t.Fatalf("list all: %v", list)
	}
}

func TestBootstrap(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	tok, created, err := Bootstrap(ctx, st, "")
	if err != nil || !created || !tokenRe.MatchString(tok) {
		t.Fatalf("bootstrap: %q %v %v", tok, created, err)
	}
	p, err := NewAuthenticator(st).Authenticate(ctx, "Bearer "+tok)
	if err != nil || !p.IsAdmin() || p.Workspace != "" {
		t.Fatalf("bootstrap key: %+v %v", p, err)
	}
	if tok2, created, err := Bootstrap(ctx, st, ""); err != nil || created || tok2 != "" {
		t.Fatalf("second bootstrap: %q %v %v", tok2, created, err)
	}

	st2 := newStore(t)
	fixed, _, _, _ := NewToken()
	if tok, created, err := Bootstrap(ctx, st2, fixed); err != nil || !created || tok != "" {
		t.Fatalf("fixed bootstrap: %q %v %v", tok, created, err)
	}
	if _, err := NewAuthenticator(st2).Authenticate(ctx, "Bearer "+fixed); err != nil {
		t.Fatalf("fixed key: %v", err)
	}
	if _, _, err := Bootstrap(ctx, newStore(t), "not-a-key"); err == nil || !strings.Contains(err.Error(), "QH_BOOTSTRAP_ADMIN_KEY") {
		t.Fatalf("bad fixed key: %v", err)
	}
}

func TestParseExpiry(t *testing.T) {
	cases := map[string]time.Duration{"": 0, "90d": 90 * 24 * time.Hour, "720h": 720 * time.Hour, "30m": 30 * time.Minute}
	for in, want := range cases {
		if got, err := ParseExpiry(in); err != nil || got != want {
			t.Errorf("ParseExpiry(%q) = %v %v", in, got, err)
		}
	}
	for _, bad := range []string{"soon", "-1h", "0d", "1.5d"} {
		if _, err := ParseExpiry(bad); err == nil {
			t.Errorf("ParseExpiry(%q) accepted", bad)
		}
	}
}
```

- [ ] **Step 2: Run it to verify it fails**

Run: `go test ./internal/auth/`
Expected: FAIL — package does not exist / `undefined: NewToken`.

- [ ] **Step 3: Implement**

`internal/auth/keys.go`:
```go
// Package auth implements qh's API keys, scopes and request authentication.
package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"math/big"
	"strings"

	"github.com/anderson-0/qh/internal/qerr"
	"github.com/anderson-0/qh/internal/store"
)

const (
	ScopeQuery  = "query"
	ScopeManage = "manage"
	ScopeAdmin  = "admin"
)

var validScopes = map[string]bool{ScopeQuery: true, ScopeManage: true, ScopeAdmin: true}

// Principal is an authenticated caller.
type Principal struct {
	KeyID     string
	Prefix    string
	Workspace string // empty for admin keys
	Scopes    map[string]bool
}

// Has reports whether p may use scope. Admin implies every scope.
func (p *Principal) Has(scope string) bool { return p.Scopes[ScopeAdmin] || p.Scopes[scope] }

// IsAdmin reports whether p holds the admin scope.
func (p *Principal) IsAdmin() bool { return p.Scopes[ScopeAdmin] }

// Local is the principal every request acts as in local mode.
func Local() *Principal {
	return &Principal{KeyID: "local", Workspace: store.DefaultWorkspace, Scopes: map[string]bool{ScopeAdmin: true}}
}

// ResolveWorkspace returns the workspace a request acts on. Workspace keys
// always use their own workspace; admin keys name one with the
// X-QH-Workspace header, falling back to their own (local mode).
func ResolveWorkspace(p *Principal, header string) (string, error) {
	header = strings.TrimSpace(header)
	if !p.IsAdmin() {
		return p.Workspace, nil
	}
	if header != "" {
		return header, nil
	}
	if p.Workspace != "" {
		return p.Workspace, nil
	}
	return "", qerr.New(qerr.Invalid, "admin keys must name a workspace").WithHint("Send the X-QH-Workspace header")
}

const prefixAlphabet = "abcdefghijklmnopqrstuvwxyz0123456789"

// NewToken returns a new key qh_<prefix>_<secret>, its prefix, and the
// SHA-256 of its secret (the only part that is stored).
func NewToken() (token, prefix string, hash []byte, err error) {
	raw := make([]byte, 8+32)
	if _, err := rand.Read(raw); err != nil {
		return "", "", nil, err
	}
	var p strings.Builder
	for _, b := range raw[:8] {
		p.WriteByte(prefixAlphabet[int(b)%len(prefixAlphabet)])
	}
	secret := new(big.Int).SetBytes(raw[8:]).Text(62)
	return "qh_" + p.String() + "_" + secret, p.String(), HashSecret(secret), nil
}

// ParseToken splits a key into its prefix and secret.
func ParseToken(token string) (prefix, secret string, ok bool) {
	rest, ok := strings.CutPrefix(token, "qh_")
	if !ok {
		return "", "", false
	}
	prefix, secret, ok = strings.Cut(rest, "_")
	if !ok || len(prefix) != 8 || secret == "" {
		return "", "", false
	}
	for _, c := range prefix {
		if !strings.ContainsRune(prefixAlphabet, c) {
			return "", "", false
		}
	}
	for _, c := range secret {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z') {
			return "", "", false
		}
	}
	return prefix, secret, true
}

// HashSecret is the stored form of a key's secret.
func HashSecret(secret string) []byte {
	h := sha256.Sum256([]byte(secret))
	return h[:]
}
```

`internal/auth/authenticator.go`:
```go
package auth

import (
	"context"
	"crypto/subtle"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/anderson-0/qh/internal/qerr"
	"github.com/anderson-0/qh/internal/store"
)

// Authenticator checks API keys against the store on every request, so a
// deleted or expired key stops working immediately.
type Authenticator struct {
	st      store.Store
	now     func() time.Time
	mu      sync.Mutex
	touched map[string]time.Time
}

// NewAuthenticator returns an Authenticator backed by st.
func NewAuthenticator(st store.Store) *Authenticator {
	return &Authenticator{st: st, now: time.Now, touched: map[string]time.Time{}}
}

func authErr(msg string) *qerr.Error {
	return qerr.New(qerr.Auth, msg).WithHint("Send Authorization: Bearer qh_…")
}

// Authenticate validates an Authorization header value.
func (a *Authenticator) Authenticate(ctx context.Context, header string) (*Principal, error) {
	tok, ok := strings.CutPrefix(header, "Bearer ")
	if !ok {
		return nil, authErr("missing or malformed Authorization header")
	}
	prefix, secret, ok := ParseToken(strings.TrimSpace(tok))
	if !ok {
		return nil, authErr("invalid API key")
	}
	k, err := a.st.GetKeyByPrefix(ctx, prefix)
	if errors.Is(err, store.ErrNotFound) {
		return nil, authErr("invalid API key")
	}
	if err != nil {
		return nil, err
	}
	if subtle.ConstantTimeCompare(k.Hash, HashSecret(secret)) != 1 {
		return nil, authErr("invalid API key")
	}
	now := a.now()
	if k.ExpiresAt != nil && !now.Before(*k.ExpiresAt) {
		return nil, authErr("API key expired")
	}
	a.touch(ctx, k.ID, now)
	p := &Principal{KeyID: k.ID, Prefix: k.Prefix, Workspace: k.WorkspaceID, Scopes: map[string]bool{}}
	for _, s := range k.Scopes {
		p.Scopes[s] = true
	}
	return p, nil
}

// touch records last use at most once a minute per key. A failure to record
// it never fails the request.
func (a *Authenticator) touch(ctx context.Context, id string, now time.Time) {
	a.mu.Lock()
	last, seen := a.touched[id]
	if seen && now.Sub(last) < time.Minute {
		a.mu.Unlock()
		return
	}
	a.touched[id] = now
	a.mu.Unlock()
	_ = a.st.TouchKey(ctx, id, now)
}
```

`internal/auth/manage.go`:
```go
package auth

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/anderson-0/qh/internal/qerr"
	"github.com/anderson-0/qh/internal/store"
)

// KeyInput describes a key to create.
type KeyInput struct {
	Workspace string // empty for an admin key
	Scopes    []string
	Name      string
	ExpiresIn time.Duration // 0 for no expiry
}

// KeyInfo is what may be shown about a key. It never includes the secret.
type KeyInfo struct {
	ID         string
	Prefix     string
	Name       string
	Workspace  string
	Scopes     []string
	CreatedAt  time.Time
	ExpiresAt  *time.Time
	LastUsedAt *time.Time
}

func toInfo(k *store.APIKey) *KeyInfo {
	return &KeyInfo{ID: k.ID, Prefix: k.Prefix, Name: k.Name, Workspace: k.WorkspaceID, Scopes: k.Scopes,
		CreatedAt: k.CreatedAt, ExpiresAt: k.ExpiresAt, LastUsedAt: k.LastUsedAt}
}

func validate(in KeyInput) error {
	if len(in.Scopes) == 0 {
		return qerr.New(qerr.Invalid, "at least one scope is required").WithHint("Use query, manage or admin")
	}
	admin := false
	for _, s := range in.Scopes {
		if !validScopes[s] {
			return qerr.New(qerr.Invalid, fmt.Sprintf("unknown scope %q", s)).WithHint("Use query, manage or admin")
		}
		admin = admin || s == ScopeAdmin
	}
	if admin && in.Workspace != "" {
		return qerr.New(qerr.Invalid, "admin keys cannot belong to a workspace")
	}
	if !admin && in.Workspace == "" {
		return qerr.New(qerr.Invalid, "workspace keys need a workspace")
	}
	if in.ExpiresIn < 0 {
		return qerr.New(qerr.Invalid, "expiry must be positive")
	}
	return nil
}

// CreateKey creates a key and returns it in full. The full key is never
// stored or shown again.
func CreateKey(ctx context.Context, st store.Store, in KeyInput) (string, *KeyInfo, error) {
	in.Name = strings.Join(strings.Fields(in.Name), " ")
	if err := validate(in); err != nil {
		return "", nil, err
	}
	for {
		tok, prefix, hash, err := NewToken()
		if err != nil {
			return "", nil, err
		}
		k := &store.APIKey{ID: store.NewID(), WorkspaceID: in.Workspace, Prefix: prefix, Hash: hash, Scopes: in.Scopes, Name: in.Name}
		if in.ExpiresIn > 0 {
			exp := time.Now().UTC().Add(in.ExpiresIn)
			k.ExpiresAt = &exp
		}
		err = st.CreateKey(ctx, k)
		switch {
		case errors.Is(err, store.ErrExists):
			continue // prefix collision; draw again
		case errors.Is(err, store.ErrNotFound):
			return "", nil, qerr.New(qerr.UnknownSource, fmt.Sprintf("no workspace named %q", in.Workspace))
		case err != nil:
			return "", nil, err
		}
		return tok, toInfo(k), nil
	}
}

// ListKeys lists the keys of one workspace ("" lists admin keys), or every
// key when all is true.
func ListKeys(ctx context.Context, st store.Store, workspace string, all bool) ([]*KeyInfo, error) {
	var keys []*store.APIKey
	var err error
	if all {
		keys, err = st.ListAllKeys(ctx)
	} else {
		keys, err = st.ListKeys(ctx, workspace)
	}
	if err != nil {
		return nil, err
	}
	out := make([]*KeyInfo, len(keys))
	for i, k := range keys {
		out[i] = toInfo(k)
	}
	return out, nil
}

// DeleteKey revokes a key.
func DeleteKey(ctx context.Context, st store.Store, id string) error {
	err := st.DeleteKey(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		return qerr.New(qerr.UnknownSource, fmt.Sprintf("no API key with id %q", id))
	}
	return err
}

// Bootstrap creates the first admin key when the store has none. When fixed
// is set (QH_BOOTSTRAP_ADMIN_KEY) that key is stored and token is empty;
// otherwise a new key is generated and returned so it can be shown once.
func Bootstrap(ctx context.Context, st store.Store, fixed string) (token string, created bool, err error) {
	n, err := st.CountKeys(ctx)
	if err != nil || n > 0 {
		return "", false, err
	}
	k := &store.APIKey{ID: store.NewID(), Scopes: []string{ScopeAdmin}, Name: "bootstrap"}
	if fixed != "" {
		prefix, secret, ok := ParseToken(strings.TrimSpace(fixed))
		if !ok {
			return "", false, qerr.New(qerr.Invalid, "QH_BOOTSTRAP_ADMIN_KEY is not a valid qh API key").
				WithHint("Generate one with qh key create, or unset it to have one generated")
		}
		k.Prefix, k.Hash = prefix, HashSecret(secret)
	} else {
		if token, k.Prefix, k.Hash, err = NewToken(); err != nil {
			return "", false, err
		}
	}
	if err := st.CreateKey(ctx, k); err != nil {
		return "", false, err
	}
	return token, true, nil
}

// ParseExpiry reads an expiry such as "90d" or "720h"; "" means none.
func ParseExpiry(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, nil
	}
	bad := qerr.New(qerr.Invalid, fmt.Sprintf("invalid expiry %q", s)).WithHint("Use whole days such as 90d, or a duration such as 720h")
	if days, ok := strings.CutSuffix(s, "d"); ok {
		n, err := strconv.Atoi(days)
		if err != nil || n <= 0 {
			return 0, bad
		}
		return time.Duration(n) * 24 * time.Hour, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil || d <= 0 {
		return 0, bad
	}
	return d, nil
}
```

- [ ] **Step 4: Run the tests**

Run: `go test -race ./internal/auth/ && go vet ./...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/auth
git commit -m "feat(auth): add API keys, scopes, authentication and bootstrap"
```

---

### Task 3: Network policy

**Files:**
- Create: `internal/netguard/netguard.go`
- Test: `internal/netguard/netguard_test.go`

**Interfaces:**
- Produces:
  - `netguard.DefaultDeny []string`.
  - `netguard.New(allow, deny []string) (*Policy, error)`. `deny` is added to `DefaultDeny`. Invalid CIDRs return a `qerr` invalid error naming the bad value.
  - `(*Policy).Allowed(ip netip.Addr) bool`.
  - `(*Policy).DialContext(ctx, network, addr string) (net.Conn, error)`. A refusal returns `*netguard.DeniedError`.
  - `type netguard.DeniedError struct{ Target string }`.
  - Field `Policy.Resolve func(ctx context.Context, host string) ([]netip.Addr, error)`. Tests replace it; the default is `net.DefaultResolver.LookupNetIP(ctx, "ip", host)`.

- [ ] **Step 1: Write the failing test**

`internal/netguard/netguard_test.go`:
```go
package netguard

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"strings"
	"testing"
)

func mustPolicy(t *testing.T, allow, deny []string) *Policy {
	t.Helper()
	p, err := New(allow, deny)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestAllowed(t *testing.T) {
	p := mustPolicy(t, nil, []string{"10.0.0.0/8"})
	cases := map[string]bool{
		"127.0.0.1": false, "127.9.9.9": false, "::1": false, "0.0.0.0": false, "::": false,
		"169.254.169.254": false, "fe80::1": false, "fd00:ec2::254": false, "100.100.100.200": false,
		"::ffff:127.0.0.1": false, "10.1.2.3": false,
		"192.168.1.10": true, "172.16.0.5": true, "8.8.8.8": true, "2606:4700::1111": true,
	}
	for ip, want := range cases {
		if got := p.Allowed(netip.MustParseAddr(ip)); got != want {
			t.Errorf("Allowed(%s) = %v, want %v", ip, got, want)
		}
	}
	open := mustPolicy(t, []string{"127.0.0.0/8"}, nil)
	if !open.Allowed(netip.MustParseAddr("127.0.0.1")) || open.Allowed(netip.MustParseAddr("169.254.169.254")) {
		t.Fatal("allow list must take precedence only for its own ranges")
	}
	if _, err := New([]string{"not-a-cidr"}, nil); err == nil || !strings.Contains(err.Error(), "not-a-cidr") {
		t.Fatalf("bad cidr: %v", err)
	}
}

func TestDialRefusesLoopbackForms(t *testing.T) {
	p := mustPolicy(t, nil, nil)
	p.Resolve = func(_ context.Context, host string) ([]netip.Addr, error) {
		switch host {
		case "localhost", "evil.example.com":
			return []netip.Addr{netip.MustParseAddr("127.0.0.1")}, nil
		}
		return nil, errors.New("unexpected lookup of " + host)
	}
	for _, addr := range []string{"127.0.0.1:5432", "127.1:5432", "[::1]:5432", "[::ffff:127.0.0.1]:5432", "localhost:5432", "evil.example.com:5432", "169.254.169.254:80"} {
		_, err := p.DialContext(context.Background(), "tcp", addr)
		var de *DeniedError
		if !errors.As(err, &de) || !strings.Contains(err.Error(), "not allowed by this server's network policy") {
			t.Errorf("%s: %v", addr, err)
		}
	}
	if _, err := p.DialContext(context.Background(), "unix", "/var/run/postgresql/.s.PGSQL.5432"); err == nil {
		t.Error("unix socket allowed")
	}
}

func TestDialAllowedTarget(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		if c, err := ln.Accept(); err == nil {
			c.Close()
		}
	}()
	p := mustPolicy(t, []string{"127.0.0.0/8"}, nil)
	c, err := p.DialContext(context.Background(), "tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("allowed dial failed: %v", err)
	}
	c.Close()
}

func TestDialSkipsDeniedAddressesOfAName(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		if c, err := ln.Accept(); err == nil {
			c.Close()
		}
	}()
	_, port, _ := net.SplitHostPort(ln.Addr().String())
	p := mustPolicy(t, []string{"127.0.0.2/32"}, nil) // only 127.0.0.2 is allowed …
	p.Resolve = func(context.Context, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("169.254.169.254"), netip.MustParseAddr("127.0.0.1")}, nil
	}
	if _, err := p.DialContext(context.Background(), "tcp", "db.example.com:"+port); err == nil {
		t.Fatal("dialled a denied address") // … so neither resolved address may be dialled
	}
}
```

- [ ] **Step 2: Run it to verify it fails**

Run: `go test ./internal/netguard/`
Expected: FAIL — `undefined: New`.

- [ ] **Step 3: Implement**

`internal/netguard/netguard.go`:
```go
// Package netguard decides which database addresses a qh server may dial.
// It checks the resolved IP and dials exactly that IP, so DNS tricks cannot
// redirect a connection to a denied address after the check.
package netguard

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"strings"

	"github.com/anderson-0/qh/internal/qerr"
)

// DefaultDeny covers loopback, unspecified, link-local (including cloud
// metadata at 169.254.169.254) and the AWS and Alibaba metadata addresses.
var DefaultDeny = []string{
	"127.0.0.0/8", "::1/128", "0.0.0.0/8", "::/128",
	"169.254.0.0/16", "fe80::/10", "fd00:ec2::254/128", "100.100.100.200/32",
}

// Policy allows an address when it is inside an allow range, or outside
// every deny range.
type Policy struct {
	allow, deny []netip.Prefix
	// Resolve looks up a host name; tests replace it.
	Resolve func(ctx context.Context, host string) ([]netip.Addr, error)
}

// DeniedError reports a refused target.
type DeniedError struct{ Target string }

func (e *DeniedError) Error() string {
	return "connections to " + e.Target + " are not allowed by this server's network policy"
}

func parse(list []string) ([]netip.Prefix, error) {
	out := make([]netip.Prefix, 0, len(list))
	for _, s := range list {
		p, err := netip.ParsePrefix(strings.TrimSpace(s))
		if err != nil {
			return nil, qerr.New(qerr.Invalid, fmt.Sprintf("invalid CIDR %q", s))
		}
		out = append(out, p.Masked())
	}
	return out, nil
}

// New builds a policy from allow ranges and extra deny ranges.
func New(allow, deny []string) (*Policy, error) {
	a, err := parse(allow)
	if err != nil {
		return nil, err
	}
	d, err := parse(append(append([]string{}, DefaultDeny...), deny...))
	if err != nil {
		return nil, err
	}
	return &Policy{allow: a, deny: d, Resolve: func(ctx context.Context, host string) ([]netip.Addr, error) {
		return net.DefaultResolver.LookupNetIP(ctx, "ip", host)
	}}, nil
}

// Allowed reports whether ip may be dialled.
func (p *Policy) Allowed(ip netip.Addr) bool {
	ip = ip.Unmap()
	for _, r := range p.allow {
		if r.Contains(ip) {
			return true
		}
	}
	for _, r := range p.deny {
		if r.Contains(ip) {
			return false
		}
	}
	return true
}

// DialContext dials addr through the policy. Unix sockets are refused.
func (p *Policy) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	if strings.HasPrefix(network, "unix") {
		return nil, &DeniedError{Target: "unix socket " + addr}
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	var ips []netip.Addr
	if ip, err := netip.ParseAddr(host); err == nil {
		ips = []netip.Addr{ip}
	} else if ip4, ok := legacyIPv4(host); ok {
		ips = []netip.Addr{ip4}
	} else if ips, err = p.Resolve(ctx, host); err != nil {
		return nil, err
	}
	var d net.Dialer
	var lastErr error = &DeniedError{Target: host}
	for _, ip := range ips {
		if !p.Allowed(ip) {
			continue
		}
		c, err := d.DialContext(ctx, network, net.JoinHostPort(ip.Unmap().String(), port))
		if err == nil {
			return c, nil
		}
		lastErr = err
	}
	return nil, lastErr
}

// legacyIPv4 parses shorthand IPv4 forms such as 127.1, which some resolvers
// accept, so they are checked as addresses rather than looked up.
func legacyIPv4(host string) (netip.Addr, bool) {
	parts := strings.Split(host, ".")
	if len(parts) < 2 || len(parts) > 3 {
		return netip.Addr{}, false
	}
	var nums []uint64
	for _, s := range parts {
		var n uint64
		if _, err := fmt.Sscanf(s, "%d", &n); err != nil || fmt.Sprint(n) != s {
			return netip.Addr{}, false
		}
		nums = append(nums, n)
	}
	var v uint32
	switch len(nums) {
	case 2: // a.b → a.0.0.b
		if nums[0] > 255 || nums[1] > 0xffffff {
			return netip.Addr{}, false
		}
		v = uint32(nums[0])<<24 | uint32(nums[1])
	case 3: // a.b.c → a.b.0.c
		if nums[0] > 255 || nums[1] > 255 || nums[2] > 0xffff {
			return netip.Addr{}, false
		}
		v = uint32(nums[0])<<24 | uint32(nums[1])<<16 | uint32(nums[2])
	}
	return netip.AddrFrom4([4]byte{byte(v >> 24), byte(v >> 16), byte(v >> 8), byte(v)}), true
}
```

- [ ] **Step 4: Run the tests**

Run: `go test -race ./internal/netguard/ && go vet ./...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/netguard
git commit -m "feat(netguard): add a dial-time address policy"
```

---

### Task 4: Engines honour a dialer and a file root

**Files:**
- Modify: `internal/engine/engine.go` (`ConnSpec`), `internal/engine/postgres/postgres.go` (`Open`), `internal/engine/mysql/conn.go` (`Open`), `internal/engine/sqlite/sqlite.go` (`Open`)
- Test: `internal/engine/postgres/dial_test.go`, `internal/engine/mysql/dial_test.go`, `internal/engine/sqlite/root_test.go`

**Interfaces:**
- Consumes: existing engine `Open` functions.
- Produces two new `engine.ConnSpec` fields:
  - `Dial func(ctx context.Context, network, addr string) (net.Conn, error)` — nil means the default dialer.
  - `FileRoot string` — empty means unrestricted.

- [ ] **Step 1: Write the failing tests**

`internal/engine/postgres/dial_test.go`:
```go
package postgres

import (
	"context"
	"errors"
	"net"
	"strings"
	"testing"

	"github.com/anderson-0/qh/internal/engine"
	"github.com/anderson-0/qh/internal/qerr"
)

func TestOpenUsesDial(t *testing.T) {
	called := false
	spec := engine.ConnSpec{ID: "x", DSN: "postgres://u:hunter22@db.example.com:5432/db?connect_timeout=2", Mode: engine.ModeRO, PoolSize: 1,
		Dial: func(context.Context, string, string) (net.Conn, error) { called = true; return nil, errors.New("blocked by test policy") }}
	_, err := Engine{}.Open(context.Background(), spec)
	if !called || qerr.From(err).Category != qerr.ConnectionFailed || !strings.Contains(err.Error(), "blocked by test policy") || strings.Contains(err.Error(), "hunter22") {
		t.Fatalf("called=%v err=%v", called, err)
	}
}
```

`internal/engine/mysql/dial_test.go`:
```go
package mysql

import (
	"context"
	"errors"
	"net"
	"strings"
	"testing"

	"github.com/anderson-0/qh/internal/engine"
	"github.com/anderson-0/qh/internal/qerr"
)

func TestOpenUsesDial(t *testing.T) {
	called := false
	spec := engine.ConnSpec{ID: "x", DSN: "mysql://u:hunter22@db.example.com:3306/db?timeout=2s", Mode: engine.ModeRO, PoolSize: 1,
		Dial: func(context.Context, string, string) (net.Conn, error) { called = true; return nil, errors.New("blocked by test policy") }}
	_, err := Engine{}.Open(context.Background(), spec)
	if !called || qerr.From(err).Category != qerr.ConnectionFailed || !strings.Contains(err.Error(), "blocked by test policy") || strings.Contains(err.Error(), "hunter22") {
		t.Fatalf("called=%v err=%v", called, err)
	}
}
```

`internal/engine/sqlite/root_test.go`:
```go
package sqlite

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/anderson-0/qh/internal/engine"
	"github.com/anderson-0/qh/internal/testutil"
)

func TestFileRoot(t *testing.T) {
	inside := testutil.SQLiteFixture(t)
	root := filepath.Dir(inside)
	outside := testutil.SQLiteFixture(t)
	link := filepath.Join(root, "escape.db")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	open := func(dsn string) error {
		c, err := Engine{}.Open(context.Background(), engine.ConnSpec{ID: "x", DSN: dsn, Mode: engine.ModeRO, PoolSize: 1, FileRoot: root})
		if err == nil {
			c.Close()
		}
		return err
	}
	if err := open(inside); err != nil {
		t.Fatalf("inside root: %v", err)
	}
	// t.TempDir directories are siblings, so this walks from inside the root to the other fixture.
	traversal := filepath.Join(root, "..", "..", filepath.Base(filepath.Dir(filepath.Dir(outside))), "with space", "shop.db")
	for _, dsn := range []string{outside, link, traversal, "/etc/hosts"} {
		err := open(dsn)
		if err == nil || !strings.HasPrefix(err.Error(), "error[invalid]: sqlite path is outside the allowed directory") {
			t.Errorf("%s: %v", dsn, err)
		}
	}
}
```

- [ ] **Step 2: Run them to verify they fail**

Run: `go test -short ./internal/engine/... -run 'TestOpenUsesDial|TestFileRoot'`
Expected: FAIL — `unknown field Dial in struct literal` / `unknown field FileRoot`.

- [ ] **Step 3: Implement**

In `internal/engine/engine.go`, add `"net"` to the imports and two fields at the end of `ConnSpec`:
```go
	// Dial, when set, opens every network connection to the database. Server
	// mode passes the netguard policy here. Nil uses the engine's default.
	Dial func(ctx context.Context, network, addr string) (net.Conn, error)
	// FileRoot, when set, confines file-based databases to this directory.
	FileRoot string
```

In `internal/engine/postgres/postgres.go` `Open`, directly after the line that sets `cfg.ConnConfig.DefaultQueryExecMode`:
```go
	if spec.Dial != nil {
		cfg.ConnConfig.DialFunc = spec.Dial
		// Resolve inside Dial only: pgx would otherwise look the host up
		// itself first, and the policy must see (and dial) the one answer.
		cfg.ConnConfig.LookupFunc = func(_ context.Context, host string) ([]string, error) { return []string{host}, nil }
	}
```

In `internal/engine/mysql/conn.go` `Open`, directly after `cfg, err := parseConfig(spec.DSN)` and its error check:
```go
	if spec.Dial != nil {
		cfg.DialFunc = spec.Dial
	}
```

In `internal/engine/sqlite/sqlite.go` `Open`:
- Directly after `path, err := Path(spec.DSN)` and its error check, add:
```go
	if spec.FileRoot != "" {
		if err := within(spec.FileRoot, path); err != nil {
			return nil, err
		}
	}
```
- Directly after the existing `os.Stat(path)` check, add:
```go
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
```
- Append the helper:
```go
// within rejects path unless it lies inside root.
func within(root, path string) error {
	rel, err := filepath.Rel(filepath.Clean(root), filepath.Clean(path))
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return qerr.New(qerr.Invalid, "sqlite path is outside the allowed directory").
			WithHint("This server only opens SQLite files under its --sqlite-root")
	}
	return nil
}
```
The lexical check runs before `os.Stat`. That way a path outside the root never reveals whether the file exists.

- [ ] **Step 4: Run the tests**

Run: `go test -short ./internal/engine/... && go vet ./...`
Expected: PASS. Then run the Docker suites once: `go test ./internal/engine/...`. Expected: PASS; no behaviour changes when `Dial` and `FileRoot` are unset.

- [ ] **Step 5: Commit**

```bash
git add internal/engine
git commit -m "feat(engine): let callers supply a dialer and a SQLite file root"
```

---

### Task 5: Core: server options, workspaces, structured results

**Files:**
- Modify: `internal/core/service.go`, `internal/core/connections.go`, `internal/format/schema.go`
- Create: `internal/core/workspaces.go`
- Test: `internal/core/server_test.go`, and append to `internal/format/schema_test.go`

**Interfaces:**
- Consumes: `engine.ConnSpec.Dial`/`FileRoot` (Task 4); store workspace methods (Task 1).
- Produces:
  - `type core.Options struct { Dial func(ctx context.Context, network, addr string) (net.Conn, error); SQLiteRoot string; SQLiteDisabled bool }`.
  - `core.New(st, keys, pools, cache *catalog.Cache, opts ...Options) *Service`. Existing three-or-four-argument calls keep compiling.
  - `(*Service).QueryResult(ctx, ws string, in QueryInput) (*engine.Result, error)`. `Query` now renders its result.
  - `(*Service).Catalog(ctx, ws, name string) (*engine.Catalog, error)`. The catalog is already filtered by the connection's allowed namespaces.
  - `(*Service).TestInput(ctx, in ConnInput) (*TestResult, error)` tests unsaved connection details. An empty name is allowed.
  - Workspace operations:
    - `type core.WorkspaceInfo struct { Name string; CreatedAt time.Time }`.
    - `CreateWorkspace(ctx, name string) (*WorkspaceInfo, error)`.
    - `ListWorkspaces(ctx) ([]*WorkspaceInfo, error)`.
    - `DeleteWorkspace(ctx, name string) error`. `default` cannot be deleted. Pools and caches of the workspace's connections are dropped.
    - `WorkspaceExists(ctx, name string) (bool, error)`.
  - `format.Matching(c *engine.Catalog, pattern, object string) []*engine.Object` returns every object when both arguments are empty.

- [ ] **Step 1: Write the failing tests**

Append to `internal/format/schema_test.go`:
```go
func TestMatching(t *testing.T) {
	c := schemaCatalog()
	if got := Matching(c, "", ""); len(got) != 5 {
		t.Fatalf("all: %d", len(got))
	}
	if got := Matching(c, "order*", ""); len(got) != 2 {
		t.Fatalf("pattern: %d", len(got))
	}
	if got := Matching(c, "", "public.users"); len(got) != 1 || got[0].Name != "users" {
		t.Fatalf("object: %+v", got)
	}
}
```

`internal/core/server_test.go`:
```go
package core

import (
	"context"
	"crypto/rand"
	"errors"
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/anderson-0/qh/internal/catalog"
	"github.com/anderson-0/qh/internal/engine"
	_ "github.com/anderson-0/qh/internal/engine/postgres"
	"github.com/anderson-0/qh/internal/pool"
	"github.com/anderson-0/qh/internal/qerr"
	"github.com/anderson-0/qh/internal/secret"
	"github.com/anderson-0/qh/internal/store"
	"github.com/anderson-0/qh/internal/testutil"
)

func newServiceWith(t *testing.T, st *store.SQLite, opts Options) *Service {
	t.Helper()
	key := make([]byte, secret.KeySize)
	rand.Read(key)
	keys, err := secret.NewKeyring(key)
	if err != nil {
		t.Fatal(err)
	}
	pools := pool.New(OpenConn, pool.Options{})
	t.Cleanup(pools.Close)
	return New(st, keys, pools, catalog.New(10*time.Minute), opts)
}

func openStore(t *testing.T) *store.SQLite {
	t.Helper()
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "qh.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func TestSQLiteDisabled(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	db := testutil.SQLiteFixture(t)
	open := newServiceWith(t, st, Options{})
	if _, _, err := open.CreateConnection(ctx, ws, ConnInput{Name: "shop", Engine: "sqlite", DSN: db}, true); err != nil {
		t.Fatal(err)
	}
	locked := newServiceWith(t, st, Options{SQLiteDisabled: true})
	_, _, err := locked.CreateConnection(ctx, ws, ConnInput{Name: "other", Engine: "sqlite", DSN: db}, false)
	if err == nil || !strings.HasPrefix(err.Error(), "error[invalid]: SQLite connections are disabled on this server") {
		t.Fatalf("create: %v", err)
	}
	if _, err := locked.Query(ctx, ws, QueryInput{Source: "shop", Query: "SELECT 1"}); err == nil || qerr.From(err).Category != qerr.Invalid {
		t.Fatalf("existing sqlite connection still usable: %v", err)
	}
	if _, err := locked.TestInput(ctx, ConnInput{Engine: "sqlite", DSN: db}); err == nil {
		t.Fatal("TestInput allowed sqlite")
	}
}

func TestSQLiteRoot(t *testing.T) {
	ctx := context.Background()
	inside := testutil.SQLiteFixture(t)
	outside := testutil.SQLiteFixture(t)
	svc := newServiceWith(t, openStore(t), Options{SQLiteRoot: filepath.Dir(inside)})
	if _, _, err := svc.CreateConnection(ctx, ws, ConnInput{Name: "in", Engine: "sqlite", DSN: inside}, true); err != nil {
		t.Fatalf("inside: %v", err)
	}
	if _, _, err := svc.CreateConnection(ctx, ws, ConnInput{Name: "out", Engine: "sqlite", DSN: outside}, true); err == nil || !strings.Contains(err.Error(), "outside the allowed directory") {
		t.Fatalf("outside: %v", err)
	}
}

func TestDialOption(t *testing.T) {
	ctx := context.Background()
	called := false
	svc := newServiceWith(t, openStore(t), Options{Dial: func(context.Context, string, string) (net.Conn, error) {
		called = true
		return nil, errors.New("refused by policy")
	}})
	_, _, err := svc.CreateConnection(ctx, ws, ConnInput{Name: "pg", Engine: "postgres", DSN: "postgres://u:hunter22@db.example.com/db?connect_timeout=2"}, true)
	if !called || err == nil || !strings.Contains(err.Error(), "refused by policy") || strings.Contains(err.Error(), "hunter22") {
		t.Fatalf("called=%v err=%v", called, err)
	}
}

func TestQueryResultCatalogAndTestInput(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	h.addShop(t, engine.ModeRO)
	r, err := h.svc.QueryResult(ctx, ws, QueryInput{Source: "shop", Query: "SELECT count(*) AS n FROM orders"})
	if err != nil || len(r.Columns) != 1 || r.Columns[0].Name != "n" || r.Rows[0][0].Text != "250" {
		t.Fatalf("query result: %+v %v", r, err)
	}
	cat, err := h.svc.Catalog(ctx, ws, "shop")
	if err != nil || len(cat.Lookup("users")) != 1 {
		t.Fatalf("catalog: %v", err)
	}
	tr, err := h.svc.TestInput(ctx, ConnInput{Engine: "sqlite", DSN: testutil.SQLiteFixture(t)})
	if err != nil || !strings.HasPrefix(tr.ServerVersion, "SQLite ") {
		t.Fatalf("test input: %+v %v", tr, err)
	}
	if _, err := h.svc.TestInput(ctx, ConnInput{Engine: "sqlite", DSN: filepath.Join(t.TempDir(), "missing.db")}); qerr.From(err).Category != qerr.ConnectionFailed {
		t.Fatalf("missing file: %v", err)
	}
}

func TestWorkspaceOperations(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	if _, err := h.svc.CreateWorkspace(ctx, "team"); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{
		"team":    `error[invalid]: a workspace named "team" already exists`,
		"Team!":   `error[invalid]: invalid workspace name "Team!"`,
		"":        `error[invalid]: invalid workspace name ""`,
	} {
		if _, err := h.svc.CreateWorkspace(ctx, name); err == nil || !strings.HasPrefix(err.Error(), want) {
			t.Errorf("%q: %v", name, err)
		}
	}
	list, err := h.svc.ListWorkspaces(ctx)
	if err != nil || len(list) != 2 || list[0].Name != "default" || list[1].Name != "team" {
		t.Fatalf("list: %+v %v", list, err)
	}
	if _, _, err := h.svc.CreateConnection(ctx, "team", ConnInput{Name: "shop", Engine: "sqlite", DSN: testutil.SQLiteFixture(t)}, true); err != nil {
		t.Fatal(err)
	}
	if _, err := h.svc.Query(ctx, "team", QueryInput{Source: "shop", Query: "SELECT 1"}); err != nil {
		t.Fatal(err) // opens a pool for the connection
	}
	if ok, _ := h.svc.WorkspaceExists(ctx, "team"); !ok {
		t.Fatal("team should exist")
	}
	if err := h.svc.DeleteWorkspace(ctx, "default"); err == nil || !strings.HasPrefix(err.Error(), "error[invalid]: the default workspace cannot be deleted") {
		t.Fatalf("delete default: %v", err)
	}
	if err := h.svc.DeleteWorkspace(ctx, "team"); err != nil {
		t.Fatal(err)
	}
	if ok, _ := h.svc.WorkspaceExists(ctx, "team"); ok {
		t.Fatal("team still exists")
	}
	if err := h.svc.DeleteWorkspace(ctx, "team"); err == nil || !strings.HasPrefix(err.Error(), `error[unknown_source]: no workspace named "team"`) {
		t.Fatalf("delete twice: %v", err)
	}
	if _, err := h.svc.Query(ctx, "team", QueryInput{Source: "shop", Query: "SELECT 1"}); qerr.From(err).Category != qerr.UnknownSource {
		t.Fatalf("connection outlived its workspace: %v", err)
	}
}
```

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./internal/core/ ./internal/format/`
Expected: FAIL — `undefined: Options` / `undefined: Matching`.

- [ ] **Step 3: Implement**

`internal/format/schema.go`: append:
```go
// Matching returns the objects Schema would describe for pattern/object, or
// every object when both are empty.
func Matching(c *engine.Catalog, pattern, object string) []*engine.Object {
	if strings.TrimSpace(pattern) == "" && strings.TrimSpace(object) == "" {
		var out []*engine.Object
		for _, ns := range c.Namespaces {
			out = append(out, ns.Objects...)
		}
		return out
	}
	return match(c, SchemaOpts{Pattern: pattern, Object: object})
}
```

`internal/core/service.go`:
- Add `"net"` to imports.
- Add the `Options` type, an `opts` field on `Service`, and the variadic `New`:
```go
// Options configures a Service for server mode. The zero value is local mode.
type Options struct {
	// Dial, when set, opens every database network connection (netguard).
	Dial func(ctx context.Context, network, addr string) (net.Conn, error)
	// SQLiteRoot, when set, confines SQLite files to this directory.
	SQLiteRoot string
	// SQLiteDisabled refuses SQLite connections entirely.
	SQLiteDisabled bool
}
```
In `type Service struct`, add the field `opts Options`. Replace `New` with:
```go
// New returns a Service. opts (at most one) configures server mode.
func New(st store.Store, keys *secret.Keyring, pools *pool.Manager, cache *catalog.Cache, opts ...Options) *Service {
	s := &Service{store: st, keys: keys, pools: pools, cache: cache}
	if len(opts) > 0 {
		s.opts = opts[0]
	}
	return s
}

// allowEngine refuses engines this server does not permit.
func (s *Service) allowEngine(name string) error {
	if s.opts.SQLiteDisabled && name == "sqlite" {
		return qerr.New(qerr.Invalid, "SQLite connections are disabled on this server").
			WithHint("Start qh serve with --sqlite-root DIR to allow SQLite files under DIR")
	}
	return nil
}

// connSpec builds the spec engines open, carrying the server's options.
func (s *Service) connSpec(id string, version int64, eng, dsn string, mode engine.Mode, poolSize int) engine.ConnSpec {
	return engine.ConnSpec{ID: id, Version: version, Engine: eng, DSN: dsn, Mode: mode, PoolSize: poolSize,
		Dial: s.opts.Dial, FileRoot: s.opts.SQLiteRoot}
}
```
- In `resolve`, directly after `eng, err := engine.Get(rec.Engine)` and its error check, add:
```go
	if err := s.allowEngine(rec.Engine); err != nil {
		return nil, err
	}
```
  Also replace the `spec: engine.ConnSpec{…}` literal with `spec: s.connSpec(rec.ID, rec.Version, rec.Engine, in.DSN, in.Mode, rec.PoolSize),`.
- Rename `func (s *Service) Query(` to `func (s *Service) QueryResult(`, and change its return type to `(*engine.Result, error)`. Replace every `return "", ` inside it with `return nil, `, and its final `return format.Result(res), nil` with `return res, nil`. Update the doc comment to `// QueryResult runs one statement and returns its result.` Then add:
```go
// Query runs one statement and renders the result as TSV.
func (s *Service) Query(ctx context.Context, ws string, in QueryInput) (string, error) {
	res, err := s.QueryResult(ctx, ws, in)
	if err != nil {
		return "", err
	}
	return format.Result(res), nil
}

// Catalog returns a connection's schema, limited to its allowed namespaces.
func (s *Service) Catalog(ctx context.Context, ws, name string) (*engine.Catalog, error) {
	t, err := s.resolve(ctx, ws, name)
	if err != nil {
		return nil, err
	}
	conn, release, err := s.pools.Get(ctx, t.spec)
	if err != nil {
		return nil, t.scrub(err)
	}
	defer release()
	cat, err := s.catalog(ctx, conn, t)
	if err != nil {
		return nil, t.scrub(err)
	}
	return cat, nil
}
```

`internal/core/connections.go`:
- Turn `testConn` into a method that uses the server options. Replace its signature and its `eng.Open` line:
```go
func (s *Service) testConn(ctx context.Context, eng engine.Engine, in ConnInput, info engine.DSNInfo) (*TestResult, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	start := time.Now()
	conn, err := eng.Open(ctx, s.connSpec("test", 0, in.Engine, in.DSN, in.Mode, 1))
```
  Then change the three call sites from `testConn(` to `s.testConn(`: in `CreateConnection`, `UpdateConnection` and `TestConnection`.
- In `CreateConnection` and in `UpdateConnection`, directly after the `normalize` call and its error check, add:
```go
	if err := s.allowEngine(in.Engine); err != nil {
		return nil, nil, err
	}
```
- Append:
```go
// TestInput connects with connection details that are not saved. The name
// may be empty.
func (s *Service) TestInput(ctx context.Context, in ConnInput) (*TestResult, error) {
	if strings.TrimSpace(in.Name) == "" {
		in.Name = "test"
	}
	in, err := normalize(in)
	if err != nil {
		return nil, err
	}
	if err := s.allowEngine(in.Engine); err != nil {
		return nil, err
	}
	eng, _ := engine.Get(in.Engine)
	info, err := eng.Inspect(in.DSN)
	if err != nil {
		return nil, scrub(err, in.DSN)
	}
	return s.testConn(ctx, eng, in, info)
}
```

`internal/core/workspaces.go`:
```go
package core

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/anderson-0/qh/internal/qerr"
	"github.com/anderson-0/qh/internal/store"
)

// WorkspaceInfo is what may be shown about a workspace.
type WorkspaceInfo struct {
	Name      string
	CreatedAt time.Time
}

func noWorkspace(name string) error {
	return qerr.New(qerr.UnknownSource, fmt.Sprintf("no workspace named %q", name))
}

// CreateWorkspace creates an empty workspace.
func (s *Service) CreateWorkspace(ctx context.Context, name string) (*WorkspaceInfo, error) {
	if !nameRe.MatchString(name) {
		return nil, qerr.New(qerr.Invalid, fmt.Sprintf("invalid workspace name %q", name)).
			WithHint("Use lowercase letters, digits, _ and -, starting with a letter or digit, at most 64 characters")
	}
	w, err := s.store.CreateWorkspace(ctx, name)
	if errors.Is(err, store.ErrExists) {
		return nil, qerr.New(qerr.Invalid, fmt.Sprintf("a workspace named %q already exists", name))
	}
	if err != nil {
		return nil, err
	}
	return &WorkspaceInfo{Name: w.ID, CreatedAt: w.CreatedAt}, nil
}

// ListWorkspaces returns every workspace ordered by name.
func (s *Service) ListWorkspaces(ctx context.Context) ([]*WorkspaceInfo, error) {
	list, err := s.store.ListWorkspaces(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]*WorkspaceInfo, len(list))
	for i, w := range list {
		out[i] = &WorkspaceInfo{Name: w.ID, CreatedAt: w.CreatedAt}
	}
	return out, nil
}

// WorkspaceExists reports whether a workspace exists.
func (s *Service) WorkspaceExists(ctx context.Context, name string) (bool, error) {
	list, err := s.store.ListWorkspaces(ctx)
	if err != nil {
		return false, err
	}
	for _, w := range list {
		if w.ID == name {
			return true, nil
		}
	}
	return false, nil
}

// DeleteWorkspace removes a workspace with its connections and keys, and
// closes the connections' pools.
func (s *Service) DeleteWorkspace(ctx context.Context, name string) error {
	if name == store.DefaultWorkspace {
		return qerr.New(qerr.Invalid, "the default workspace cannot be deleted")
	}
	conns, err := s.store.ListConnections(ctx, name)
	if err != nil {
		return err
	}
	if err := s.store.DeleteWorkspace(ctx, name); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return noWorkspace(name)
		}
		return err
	}
	for _, c := range conns {
		s.pools.Drop(c.ID)
		s.cache.Invalidate(c.ID)
	}
	return nil
}
```

- [ ] **Step 4: Run the tests**

Run: `go test -race ./internal/core/ ./internal/format/ && go vet ./... && go test -short ./...`
Expected: PASS. Existing callers of `core.New` and `Service.Query` (cli, mcpserver, tests) compile unchanged.

- [ ] **Step 5: Commit**

```bash
git add internal/core internal/format
git commit -m "feat(core): add server options, workspaces and structured query results"
```

---

### Task 6: HTTP API core: middleware and connection endpoints

**Files:**
- Create: `internal/httpapi/api.go`, `internal/httpapi/connections.go`
- Create (placeholders filled in Task 7): `internal/httpapi/admin.go`, `internal/httpapi/mcp.go`, `internal/httpapi/openapi.go`, `internal/httpapi/openapi.json`
- Test: `internal/httpapi/helpers_test.go`, `internal/httpapi/api_test.go`

**Interfaces:**
- Consumes: `core.Service` (connections, `QueryResult`, `Catalog`, `TestInput`, `WorkspaceExists`), `auth.Authenticator`, `auth.Principal`, `auth.Local`, `auth.ResolveWorkspace`, `format.Matching`, `qerr`.
- Produces:
  - `type httpapi.Config struct { Service *core.Service; Store store.Store; Auth *auth.Authenticator; Version string; Logger *slog.Logger; PerKeyLimit int; SlotWait time.Duration }`. A nil `Auth` means local mode.
  - `httpapi.New(cfg Config) http.Handler`.
  - Unexported, used by Task 7:
    - `(*api).routes() []route`;
    - `(*api).fail(w, r, err)`;
    - `writeJSON`, `decode`;
    - `principal(r)`, `workspace(r)`;
    - the `route` struct;
    - `api.mcp` (an `http.Handler`).

Task 7 fills in the admin and MCP handlers and the OpenAPI document. So that this task compiles and its tests run, Step 3 creates those four files with the minimal content given there. Task 7 replaces them.

- [ ] **Step 1: Write the test helpers and failing tests**

`internal/httpapi/helpers_test.go`:
```go
package httpapi

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/anderson-0/qh/internal/auth"
	"github.com/anderson-0/qh/internal/catalog"
	"github.com/anderson-0/qh/internal/core"
	_ "github.com/anderson-0/qh/internal/engine/sqlite"
	"github.com/anderson-0/qh/internal/pool"
	"github.com/anderson-0/qh/internal/secret"
	"github.com/anderson-0/qh/internal/store"
)

type env struct {
	srv   *httptest.Server
	svc   *core.Service
	st    *store.SQLite
	admin string // admin key (server mode only)
}

// newEnv starts the API in server mode (auth on) or local mode.
func newEnv(t *testing.T, server bool, mutate func(*Config)) *env {
	t.Helper()
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
	cfg := Config{Service: svc, Store: st, Version: "test"}
	e := &env{svc: svc, st: st}
	if server {
		cfg.Auth = auth.NewAuthenticator(st)
		if e.admin, _, err = auth.Bootstrap(context.Background(), st, ""); err != nil {
			t.Fatal(err)
		}
	}
	if mutate != nil {
		mutate(&cfg)
	}
	e.srv = httptest.NewServer(New(cfg))
	t.Cleanup(e.srv.Close)
	return e
}

// key creates a workspace key with the given scopes.
func (e *env) key(t *testing.T, workspace string, scopes ...string) string {
	t.Helper()
	tok, _, err := auth.CreateKey(context.Background(), e.st, auth.KeyInput{Workspace: workspace, Scopes: scopes})
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

type reply struct {
	status int
	header http.Header
	body   string
}

func (r reply) json(t *testing.T, v any) {
	t.Helper()
	if err := json.Unmarshal([]byte(r.body), v); err != nil {
		t.Fatalf("decode %q: %v", r.body, err)
	}
}

// do sends a request. body may be nil, a string (sent raw) or any value (sent as JSON).
func (e *env) do(t *testing.T, method, path, token string, body any, headers ...string) reply {
	t.Helper()
	var rd io.Reader
	switch b := body.(type) {
	case nil:
	case string:
		rd = bytes.NewBufferString(b)
	default:
		raw, _ := json.Marshal(b)
		rd = bytes.NewReader(raw)
	}
	req, _ := http.NewRequest(method, e.srv.URL+path, rd)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return reply{status: res.StatusCode, header: res.Header, body: string(b)}
}
```

`internal/httpapi/api_test.go`:
```go
package httpapi

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/anderson-0/qh/internal/auth"
	"github.com/anderson-0/qh/internal/testutil"
)

type problemBody struct {
	Type   string `json:"type"`
	Title  string `json:"title"`
	Detail string `json:"detail"`
	Status int    `json:"status"`
}

func expectProblem(t *testing.T, r reply, status int, typ, titlePrefix string) {
	t.Helper()
	var p problemBody
	r.json(t, &p)
	if r.status != status || p.Status != status || p.Type != typ || !strings.HasPrefix(p.Title, titlePrefix) ||
		r.header.Get("Content-Type") != "application/problem+json" {
		t.Fatalf("got %d %+v (%s), want %d %s %q", r.status, p, r.header.Get("Content-Type"), status, typ, titlePrefix)
	}
}

func TestHealthAndUnknownPaths(t *testing.T) {
	e := newEnv(t, true, nil)
	if r := e.do(t, "GET", "/healthz", "", nil); r.status != 200 || r.body != "ok\n" {
		t.Fatalf("healthz %d %q", r.status, r.body)
	}
	if r := e.do(t, "GET", "/readyz", "", nil); r.status != 200 {
		t.Fatalf("readyz %d", r.status)
	}
	expectProblem(t, e.do(t, "GET", "/nope", "", nil), 404, "not_found", "no such endpoint")
}

func TestAuthRequired(t *testing.T) {
	e := newEnv(t, true, nil)
	r := e.do(t, "GET", "/api/v1/connections", "", nil)
	expectProblem(t, r, 401, "auth", "missing or malformed Authorization header")
	if r.header.Get("WWW-Authenticate") != "Bearer" {
		t.Fatalf("WWW-Authenticate %q", r.header.Get("WWW-Authenticate"))
	}
	expectProblem(t, e.do(t, "GET", "/api/v1/connections", "qh_aaaaaaaa_nope", nil), 401, "auth", "invalid API key")
}

func TestAdminNeedsWorkspaceHeader(t *testing.T) {
	e := newEnv(t, true, nil)
	expectProblem(t, e.do(t, "GET", "/api/v1/connections", e.admin, nil), 400, "invalid", "admin keys must name a workspace")
	if r := e.do(t, "GET", "/api/v1/connections", e.admin, nil, "X-QH-Workspace", "default"); r.status != 200 || strings.TrimSpace(r.body) != "[]" {
		t.Fatalf("with header: %d %s", r.status, r.body)
	}
	expectProblem(t, e.do(t, "GET", "/api/v1/connections", e.admin, nil, "X-QH-Workspace", "ghost"), 404, "unknown_source", `no workspace named "ghost"`)
}

func TestScopes(t *testing.T) {
	e := newEnv(t, true, nil)
	q := e.key(t, "default", auth.ScopeQuery)
	expectProblem(t, e.do(t, "POST", "/api/v1/connections", q, map[string]any{"name": "x", "engine": "sqlite", "dsn": "/tmp/x"}),
		403, "forbidden", "this API key lacks the manage scope")
	expectProblem(t, e.do(t, "GET", "/api/v1/workspaces", q, nil), 403, "forbidden", "this API key lacks the admin scope")
}

func TestLocalModeNeedsNoKey(t *testing.T) {
	e := newEnv(t, false, nil)
	if r := e.do(t, "GET", "/api/v1/connections", "", nil); r.status != 200 {
		t.Fatalf("local mode: %d %s", r.status, r.body)
	}
}

func TestConnectionLifecycle(t *testing.T) {
	e := newEnv(t, true, nil)
	k := e.key(t, "default", auth.ScopeQuery, auth.ScopeManage)
	db := testutil.SQLiteFixture(t)

	r := e.do(t, "POST", "/api/v1/connections", k, map[string]any{"name": "shop", "engine": "sqlite", "dsn": db, "description": "Shop DB"})
	var c connectionJSON
	r.json(t, &c)
	if r.status != 201 || c.Name != "shop" || c.Mode != "ro" || c.DSN != db || !strings.HasPrefix(c.ServerVersion, "SQLite ") || c.ConnectMS == nil || c.Version != 1 {
		t.Fatalf("create: %d %s", r.status, r.body)
	}
	expectProblem(t, e.do(t, "POST", "/api/v1/connections", k, map[string]any{"name": "shop", "engine": "sqlite", "dsn": db}),
		400, "invalid", `a connection named "shop" already exists`)

	var list []connectionJSON
	e.do(t, "GET", "/api/v1/connections", k, nil).json(t, &list)
	if len(list) != 1 || list[0].Name != "shop" {
		t.Fatalf("list: %+v", list)
	}

	r = e.do(t, "POST", "/api/v1/connections/shop/query", k, map[string]any{"query": "SELECT count(*) AS n FROM orders"})
	if r.status != 200 || !strings.HasPrefix(r.body, "n\n250\n-- 1 row · ") || !strings.HasPrefix(r.header.Get("Content-Type"), "text/plain") {
		t.Fatalf("query text: %d %q", r.status, r.body)
	}
	r = e.do(t, "POST", "/api/v1/connections/shop/query", k, map[string]any{"query": "SELECT id, avatar, name FROM users WHERE id IN (1, 3) ORDER BY id"}, "Accept", "application/json")
	var q struct {
		Columns []struct{ Name string } `json:"columns"`
		Rows    [][]any                 `json:"rows"`
		Truncated bool                  `json:"truncated"`
	}
	r.json(t, &q)
	if r.status != 200 || len(q.Columns) != 3 || q.Columns[1].Name != "avatar" || len(q.Rows) != 2 || q.Rows[0][0] != "1" ||
		q.Rows[0][1].(map[string]any)["bytes"] != float64(4) || q.Rows[1][2] != nil {
		t.Fatalf("query json: %d %s", r.status, r.body)
	}
	expectProblem(t, e.do(t, "POST", "/api/v1/connections/shop/query", k, map[string]any{"query": "DELETE FROM users"}),
		403, "read_only", `connection "shop" is read-only`)

	if r := e.do(t, "GET", "/api/v1/connections/shop/schema?object=users", k, nil); r.status != 200 || !strings.Contains(r.body, "main.users") {
		t.Fatalf("schema text: %d %s", r.status, r.body)
	}
	var s struct {
		Objects []struct {
			Name   string `json:"name"`
			Fields []struct{ Name string } `json:"fields"`
		} `json:"objects"`
	}
	e.do(t, "GET", "/api/v1/connections/shop/schema?pattern=user*", k, nil, "Accept", "application/json").json(t, &s)
	if len(s.Objects) != 1 || s.Objects[0].Name != "users" || len(s.Objects[0].Fields) != 6 {
		t.Fatalf("schema json: %+v", s)
	}

	r = e.do(t, "PATCH", "/api/v1/connections/shop", k, map[string]any{"mode": "rw"})
	r.json(t, &c)
	if r.status != 200 || c.Mode != "rw" || c.Version != 2 {
		t.Fatalf("patch: %d %s", r.status, r.body)
	}
	if r := e.do(t, "POST", "/api/v1/connections/shop/schema/refresh", k, nil); r.status != 200 || !strings.Contains(r.body, `"objects":4`) {
		t.Fatalf("refresh: %d %s", r.status, r.body)
	}
	if r := e.do(t, "POST", "/api/v1/connections/shop/test", k, nil); r.status != 200 || !strings.Contains(r.body, `"server_version":"SQLite `) {
		t.Fatalf("test stored: %d %s", r.status, r.body)
	}
	if r := e.do(t, "POST", "/api/v1/connections/test", k, map[string]any{"engine": "sqlite", "dsn": db}); r.status != 200 {
		t.Fatalf("test input: %d %s", r.status, r.body)
	}
	if r := e.do(t, "DELETE", "/api/v1/connections/shop", k, nil); r.status != 204 {
		t.Fatalf("delete: %d %s", r.status, r.body)
	}
	expectProblem(t, e.do(t, "GET", "/api/v1/connections/shop", k, nil), 404, "unknown_source", `no connection named "shop"`)
}

func TestRequestBodies(t *testing.T) {
	e := newEnv(t, true, nil)
	k := e.key(t, "default", auth.ScopeManage)
	expectProblem(t, e.do(t, "POST", "/api/v1/connections", k, "{not json"), 400, "invalid", "invalid JSON body")
	expectProblem(t, e.do(t, "POST", "/api/v1/connections", k, `{"name":"x","password":"p"}`), 400, "invalid", "invalid JSON body")
	big := `{"name":"` + strings.Repeat("x", 2<<20) + `"}`
	expectProblem(t, e.do(t, "POST", "/api/v1/connections", k, big), 400, "invalid", "request body is larger than 1 MiB")
}

func TestPerKeyLimit(t *testing.T) {
	e := newEnv(t, true, func(c *Config) { c.PerKeyLimit = 1; c.SlotWait = 200 * time.Millisecond })
	a := e.key(t, "default", auth.ScopeQuery, auth.ScopeManage)
	b := e.key(t, "default", auth.ScopeQuery)
	db := testutil.SQLiteFixture(t)
	if r := e.do(t, "POST", "/api/v1/connections", a, map[string]any{"name": "shop", "engine": "sqlite", "dsn": db, "timeout_ms": 2000}); r.status != 201 {
		t.Fatalf("create: %d %s", r.status, r.body)
	}
	slow := map[string]any{"query": "WITH RECURSIVE c(x) AS (SELECT 1 UNION ALL SELECT x + 1 FROM c) SELECT count(*) FROM c"}
	body, _ := json.Marshal(slow)
	req, _ := http.NewRequest("POST", e.srv.URL+"/api/v1/connections/shop/query", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+a)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { // holds a's only slot until the 2s timeout; no t calls off the test goroutine
		defer wg.Done()
		if res, err := http.DefaultClient.Do(req); err == nil {
			res.Body.Close()
		}
	}()
	time.Sleep(300 * time.Millisecond)
	r := e.do(t, "POST", "/api/v1/connections/shop/query", a, map[string]any{"query": "SELECT 1"})
	expectProblem(t, r, 429, "busy", "this API key already has 1 requests running")
	if r.header.Get("Retry-After") != "1" {
		t.Fatalf("Retry-After %q", r.header.Get("Retry-After"))
	}
	if r := e.do(t, "POST", "/api/v1/connections/shop/query", b, map[string]any{"query": "SELECT 1 AS one"}); r.status != 200 {
		t.Fatalf("another key was blocked: %d %s", r.status, r.body)
	}
	wg.Wait()
}
```

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./internal/httpapi/`
Expected: FAIL — package does not exist / `undefined: New`.

- [ ] **Step 3: Implement**

`internal/httpapi/api.go`:
```go
// Package httpapi serves qh over HTTP: MCP at /mcp and the REST API under
// /api/v1, with API-key authentication, workspace scoping and per-key
// concurrency limits.
package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/anderson-0/qh/internal/auth"
	"github.com/anderson-0/qh/internal/core"
	"github.com/anderson-0/qh/internal/qerr"
	"github.com/anderson-0/qh/internal/store"
)

const maxBody = 1 << 20

// Config configures the API.
type Config struct {
	Service     *core.Service
	Store       store.Store
	Auth        *auth.Authenticator // nil: local mode, every request acts as auth.Local()
	Version     string
	Logger      *slog.Logger
	PerKeyLimit int           // limited requests per key at once; default 4
	SlotWait    time.Duration // wait for a slot before busy; default 10s
}

type route struct {
	method    string // "" matches every method
	pattern   string
	scope     string // "" for public routes
	workspace bool   // resolve the caller's workspace
	limited   bool   // counts against the per-key concurrency limit
	handle    http.HandlerFunc
}

type api struct {
	cfg     Config
	limiter *limiter
	mcpMu   sync.Mutex
	servers map[string]*mcp.Server
	mcp     http.Handler
}

// New returns the HTTP handler.
func New(cfg Config) http.Handler {
	if cfg.PerKeyLimit <= 0 {
		cfg.PerKeyLimit = 4
	}
	if cfg.SlotWait <= 0 {
		cfg.SlotWait = 10 * time.Second
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.New(slog.NewJSONHandler(io.Discard, nil))
	}
	a := &api{cfg: cfg, limiter: newLimiter(cfg.PerKeyLimit, cfg.SlotWait), servers: map[string]*mcp.Server{}}
	a.mcp = mcp.NewStreamableHTTPHandler(a.mcpServer, &mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true})
	mux := http.NewServeMux()
	for _, rt := range a.routes() {
		pattern := rt.pattern
		if rt.method != "" {
			pattern = rt.method + " " + rt.pattern
		}
		mux.Handle(pattern, a.wrap(rt))
	}
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		writeProblem(w, http.StatusNotFound, "not_found", "no such endpoint", "See /api/v1/openapi.json")
	})
	return a.recoverPanics(a.logRequests(mux))
}

func (a *api) routes() []route {
	const q, m, ad = auth.ScopeQuery, auth.ScopeManage, auth.ScopeAdmin
	return []route{
		{"GET", "/healthz", "", false, false, a.healthz},
		{"GET", "/readyz", "", false, false, a.readyz},
		{"GET", "/api/v1/openapi.json", "", false, false, serveOpenAPI},
		{"", "/mcp", q, true, true, a.mcp.ServeHTTP},
		{"GET", "/api/v1/connections", q, true, false, a.listConnections},
		{"POST", "/api/v1/connections", m, true, false, a.createConnection},
		{"POST", "/api/v1/connections/test", m, true, false, a.testInput},
		{"GET", "/api/v1/connections/{name}", q, true, false, a.getConnection},
		{"PATCH", "/api/v1/connections/{name}", m, true, false, a.updateConnection},
		{"DELETE", "/api/v1/connections/{name}", m, true, false, a.deleteConnection},
		{"POST", "/api/v1/connections/{name}/test", m, true, false, a.testConnection},
		{"GET", "/api/v1/connections/{name}/schema", q, true, true, a.getSchema},
		{"POST", "/api/v1/connections/{name}/schema/refresh", m, true, true, a.refreshSchema},
		{"POST", "/api/v1/connections/{name}/query", q, true, true, a.query},
		{"GET", "/api/v1/workspaces", ad, false, false, a.listWorkspaces},
		{"POST", "/api/v1/workspaces", ad, false, false, a.createWorkspace},
		{"DELETE", "/api/v1/workspaces/{name}", ad, false, false, a.deleteWorkspace},
		{"GET", "/api/v1/keys", m, false, false, a.listKeys},
		{"POST", "/api/v1/keys", m, false, false, a.createKey},
		{"DELETE", "/api/v1/keys/{id}", m, false, false, a.deleteKey},
	}
}

type (
	principalKey struct{}
	workspaceKey struct{}
	infoKey      struct{}
)

type requestInfo struct{ key string }

func principal(r *http.Request) *auth.Principal {
	p, _ := r.Context().Value(principalKey{}).(*auth.Principal)
	return p
}

func workspace(r *http.Request) string {
	ws, _ := r.Context().Value(workspaceKey{}).(string)
	return ws
}

func (a *api) authenticate(r *http.Request) (*auth.Principal, error) {
	if a.cfg.Auth == nil {
		return auth.Local(), nil
	}
	return a.cfg.Auth.Authenticate(r.Context(), r.Header.Get("Authorization"))
}

func (a *api) resolveWorkspace(ctx context.Context, p *auth.Principal, header string) (string, error) {
	ws, err := auth.ResolveWorkspace(p, header)
	if err != nil {
		return "", err
	}
	ok, err := a.cfg.Service.WorkspaceExists(ctx, ws)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", qerr.New(qerr.UnknownSource, fmt.Sprintf("no workspace named %q", ws))
	}
	return ws, nil
}

// wrap applies the body limit, authentication, scope check, workspace
// resolution and the per-key limit that rt asks for.
func (a *api) wrap(rt route) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, maxBody)
		if rt.scope == "" {
			rt.handle(w, r)
			return
		}
		ctx := r.Context()
		p, err := a.authenticate(r)
		if err != nil {
			a.fail(w, r, err)
			return
		}
		if info, ok := ctx.Value(infoKey{}).(*requestInfo); ok {
			info.key = p.Prefix
		}
		if !p.Has(rt.scope) {
			a.fail(w, r, qerr.New(qerr.Forbidden, "this API key lacks the "+rt.scope+" scope"))
			return
		}
		ctx = context.WithValue(ctx, principalKey{}, p)
		if rt.workspace {
			ws, err := a.resolveWorkspace(ctx, p, r.Header.Get("X-QH-Workspace"))
			if err != nil {
				a.fail(w, r, err)
				return
			}
			ctx = context.WithValue(ctx, workspaceKey{}, ws)
		}
		if rt.limited {
			release, err := a.limiter.acquire(ctx, p.KeyID)
			if err != nil {
				a.fail(w, r, err)
				return
			}
			defer release()
		}
		rt.handle(w, r.WithContext(ctx))
	})
}

type limiter struct {
	per   int
	wait  time.Duration
	mu    sync.Mutex
	slots map[string]chan struct{}
}

func newLimiter(per int, wait time.Duration) *limiter {
	return &limiter{per: per, wait: wait, slots: map[string]chan struct{}{}}
}

func (l *limiter) acquire(ctx context.Context, key string) (func(), error) {
	l.mu.Lock()
	ch := l.slots[key]
	if ch == nil {
		ch = make(chan struct{}, l.per)
		l.slots[key] = ch
	}
	l.mu.Unlock()
	release := func() { <-ch }
	select {
	case ch <- struct{}{}:
		return release, nil
	default:
	}
	t := time.NewTimer(l.wait)
	defer t.Stop()
	select {
	case ch <- struct{}{}:
		return release, nil
	case <-t.C:
		return nil, qerr.New(qerr.Busy, fmt.Sprintf("this API key already has %d requests running", l.per)).WithHint("Retry shortly")
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

var statusOf = map[qerr.Category]int{
	qerr.Auth: http.StatusUnauthorized, qerr.Forbidden: http.StatusForbidden, qerr.UnknownSource: http.StatusNotFound,
	qerr.Invalid: http.StatusBadRequest, qerr.ReadOnly: http.StatusForbidden, qerr.Timeout: http.StatusGatewayTimeout,
	qerr.ConnectionFailed: http.StatusBadGateway, qerr.QueryError: http.StatusUnprocessableEntity, qerr.Busy: http.StatusTooManyRequests,
}

type problem struct {
	Type   string `json:"type"`
	Title  string `json:"title"`
	Detail string `json:"detail,omitempty"`
	Status int    `json:"status"`
}

func oneLine(s string) string { return strings.TrimRight(strings.Join(strings.Fields(s), " "), ".") }

func writeProblem(w http.ResponseWriter, status int, typ, title, detail string) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(problem{Type: typ, Title: oneLine(title), Detail: oneLine(detail), Status: status})
}

// fail writes err as a problem. Errors qh did not classify are logged and
// reported as a generic 500 so internal details never reach the client.
func (a *api) fail(w http.ResponseWriter, r *http.Request, err error) {
	var e *qerr.Error
	if !errors.As(err, &e) {
		a.cfg.Logger.Error("internal error", "method", r.Method, "path", r.URL.Path, "error", err.Error())
		writeProblem(w, http.StatusInternalServerError, "internal", "internal server error", "")
		return
	}
	status := statusOf[e.Category]
	if status == 0 {
		status = http.StatusInternalServerError
	}
	switch status {
	case http.StatusUnauthorized:
		w.Header().Set("WWW-Authenticate", "Bearer")
	case http.StatusTooManyRequests:
		w.Header().Set("Retry-After", "1")
	}
	writeProblem(w, status, string(e.Category), e.Message, e.Hint)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeText(w http.ResponseWriter, s string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = io.WriteString(w, s)
}

func wantsJSON(r *http.Request) bool { return strings.Contains(r.Header.Get("Accept"), "application/json") }

// decode reads a JSON body, rejecting unknown fields and oversized bodies.
func decode(r *http.Request, v any) error {
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			return qerr.New(qerr.Invalid, "request body is larger than 1 MiB")
		}
		return qerr.New(qerr.Invalid, "invalid JSON body: "+err.Error())
	}
	return nil
}

func (a *api) healthz(w http.ResponseWriter, _ *http.Request) { writeText(w, "ok\n") }

func (a *api) readyz(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if err := a.cfg.Store.Ping(ctx); err != nil {
		writeProblem(w, http.StatusServiceUnavailable, "unavailable", "the store is not reachable", "")
		return
	}
	writeText(w, "ok\n")
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (s *statusWriter) WriteHeader(code int) {
	if s.status == 0 {
		s.status = code
	}
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusWriter) Write(b []byte) (int, error) {
	if s.status == 0 {
		s.status = http.StatusOK
	}
	return s.ResponseWriter.Write(b)
}

func (s *statusWriter) Flush() {
	if f, ok := s.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (s *statusWriter) Unwrap() http.ResponseWriter { return s.ResponseWriter }

// logRequests logs one line per request. It never logs bodies or headers.
func (a *api) logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		info := &requestInfo{}
		sw := &statusWriter{ResponseWriter: w}
		next.ServeHTTP(sw, r.WithContext(context.WithValue(r.Context(), infoKey{}, info)))
		a.cfg.Logger.Info("request", "method", r.Method, "path", r.URL.Path, "status", sw.status,
			"duration_ms", time.Since(start).Milliseconds(), "key", info.key)
	})
}

func (a *api) recoverPanics(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if v := recover(); v != nil {
				if v == http.ErrAbortHandler {
					panic(v)
				}
				a.cfg.Logger.Error("panic", "method", r.Method, "path", r.URL.Path, "panic", fmt.Sprint(v))
				writeProblem(w, http.StatusInternalServerError, "internal", "internal server error", "")
			}
		}()
		next.ServeHTTP(w, r)
	})
}
```

`internal/httpapi/connections.go`:
```go
package httpapi

import (
	"net/http"
	"time"

	"github.com/anderson-0/qh/internal/core"
	"github.com/anderson-0/qh/internal/engine"
	"github.com/anderson-0/qh/internal/format"
)

type connectionJSON struct {
	Name              string    `json:"name"`
	Engine            string    `json:"engine"`
	Description       string    `json:"description,omitempty"`
	Mode              string    `json:"mode"`
	Host              string    `json:"host,omitempty"`
	Port              int       `json:"port,omitempty"`
	Database          string    `json:"database,omitempty"`
	User              string    `json:"user,omitempty"`
	DSN               string    `json:"dsn"`
	MaxRows           int       `json:"max_rows"`
	TimeoutMS         int       `json:"timeout_ms"`
	PoolSize          int       `json:"pool_size"`
	AllowedNamespaces []string  `json:"allowed_namespaces,omitempty"`
	Version           int64     `json:"version"`
	CreatedAt         time.Time `json:"created_at"`
	UpdatedAt         time.Time `json:"updated_at"`
	ServerVersion     string    `json:"server_version,omitempty"`
	ConnectMS         *int64    `json:"connect_ms,omitempty"`
}

func toConnection(c *core.ConnInfo, tr *core.TestResult) connectionJSON {
	out := connectionJSON{Name: c.Name, Engine: c.Engine, Description: c.Description, Mode: string(c.Mode),
		Host: c.Host, Port: c.Port, Database: c.Database, User: c.User, DSN: c.DSN,
		MaxRows: c.MaxRows, TimeoutMS: c.TimeoutMS, PoolSize: c.PoolSize, AllowedNamespaces: c.AllowedNamespaces,
		Version: c.Version, CreatedAt: c.CreatedAt, UpdatedAt: c.UpdatedAt}
	if tr != nil {
		ms := tr.Connect.Milliseconds()
		out.ServerVersion, out.ConnectMS = tr.ServerVersion, &ms
	}
	return out
}

type connectionInput struct {
	Name              string   `json:"name"`
	Engine            string   `json:"engine"`
	DSN               string   `json:"dsn"`
	Description       string   `json:"description,omitempty"`
	Mode              string   `json:"mode,omitempty"`
	MaxRows           int      `json:"max_rows,omitempty"`
	TimeoutMS         int      `json:"timeout_ms,omitempty"`
	PoolSize          int      `json:"pool_size,omitempty"`
	AllowedNamespaces []string `json:"allowed_namespaces,omitempty"`
}

func (in connectionInput) core() core.ConnInput {
	return core.ConnInput{Name: in.Name, Engine: in.Engine, DSN: in.DSN, Description: in.Description, Mode: engine.Mode(in.Mode),
		MaxRows: in.MaxRows, TimeoutMS: in.TimeoutMS, PoolSize: in.PoolSize, AllowedNamespaces: in.AllowedNamespaces}
}

type connectionPatch struct {
	DSN               *string   `json:"dsn"`
	Description       *string   `json:"description"`
	Mode              *string   `json:"mode"`
	MaxRows           *int      `json:"max_rows"`
	TimeoutMS         *int      `json:"timeout_ms"`
	PoolSize          *int      `json:"pool_size"`
	AllowedNamespaces *[]string `json:"allowed_namespaces"`
}

func (p connectionPatch) core() core.ConnPatch {
	out := core.ConnPatch{DSN: p.DSN, Description: p.Description, MaxRows: p.MaxRows, TimeoutMS: p.TimeoutMS,
		PoolSize: p.PoolSize, AllowedNamespaces: p.AllowedNamespaces}
	if p.Mode != nil {
		m := engine.Mode(*p.Mode)
		out.Mode = &m
	}
	return out
}

type testJSON struct {
	ServerVersion string `json:"server_version"`
	ConnectMS     int64  `json:"connect_ms"`
}

func skipTest(r *http.Request) bool { return r.URL.Query().Get("skip_test") == "true" }

func (a *api) listConnections(w http.ResponseWriter, r *http.Request) {
	list, err := a.cfg.Service.ListConnections(r.Context(), workspace(r))
	if err != nil {
		a.fail(w, r, err)
		return
	}
	out := make([]connectionJSON, 0, len(list))
	for _, c := range list {
		out = append(out, toConnection(c, nil))
	}
	writeJSON(w, http.StatusOK, out)
}

func (a *api) getConnection(w http.ResponseWriter, r *http.Request) {
	c, err := a.cfg.Service.GetConnection(r.Context(), workspace(r), r.PathValue("name"))
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toConnection(c, nil))
}

func (a *api) createConnection(w http.ResponseWriter, r *http.Request) {
	var in connectionInput
	if err := decode(r, &in); err != nil {
		a.fail(w, r, err)
		return
	}
	c, tr, err := a.cfg.Service.CreateConnection(r.Context(), workspace(r), in.core(), !skipTest(r))
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, toConnection(c, tr))
}

func (a *api) updateConnection(w http.ResponseWriter, r *http.Request) {
	var p connectionPatch
	if err := decode(r, &p); err != nil {
		a.fail(w, r, err)
		return
	}
	c, tr, err := a.cfg.Service.UpdateConnection(r.Context(), workspace(r), r.PathValue("name"), p.core(), !skipTest(r))
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toConnection(c, tr))
}

func (a *api) deleteConnection(w http.ResponseWriter, r *http.Request) {
	if err := a.cfg.Service.DeleteConnection(r.Context(), workspace(r), r.PathValue("name")); err != nil {
		a.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *api) testInput(w http.ResponseWriter, r *http.Request) {
	var in connectionInput
	if err := decode(r, &in); err != nil {
		a.fail(w, r, err)
		return
	}
	tr, err := a.cfg.Service.TestInput(r.Context(), in.core())
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, testJSON{ServerVersion: tr.ServerVersion, ConnectMS: tr.Connect.Milliseconds()})
}

func (a *api) testConnection(w http.ResponseWriter, r *http.Request) {
	tr, err := a.cfg.Service.TestConnection(r.Context(), workspace(r), r.PathValue("name"))
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, testJSON{ServerVersion: tr.ServerVersion, ConnectMS: tr.Connect.Milliseconds()})
}

func (a *api) refreshSchema(w http.ResponseWriter, r *http.Request) {
	n, d, err := a.cfg.Service.RefreshSchema(r.Context(), workspace(r), r.PathValue("name"))
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]int64{"objects": int64(n), "duration_ms": d.Milliseconds()})
}

type fieldJSON struct {
	Name     string   `json:"name"`
	Type     string   `json:"type,omitempty"`
	Nullable bool     `json:"nullable"`
	Default  string   `json:"default,omitempty"`
	Comment  string   `json:"comment,omitempty"`
	Enum     []string `json:"enum,omitempty"`
}

type foreignKeyJSON struct {
	Namespace    string   `json:"namespace"`
	Object       string   `json:"object"`
	Columns      []string `json:"columns"`
	RefNamespace string   `json:"ref_namespace"`
	RefObject    string   `json:"ref_object"`
	RefColumns   []string `json:"ref_columns"`
}

type indexJSON struct {
	Name    string   `json:"name"`
	Columns []string `json:"columns"`
	Unique  bool     `json:"unique"`
}

type objectJSON struct {
	Namespace    string           `json:"namespace"`
	Name         string           `json:"name"`
	Kind         string           `json:"kind"`
	Comment      string           `json:"comment,omitempty"`
	RowEstimate  int64            `json:"row_estimate"`
	Fields       []fieldJSON      `json:"fields"`
	PrimaryKey   []string         `json:"primary_key,omitempty"`
	ForeignKeys  []foreignKeyJSON `json:"foreign_keys,omitempty"`
	ReferencedBy []foreignKeyJSON `json:"referenced_by,omitempty"`
	Indexes      []indexJSON      `json:"indexes,omitempty"`
}

func toForeignKeys(fks []engine.ForeignKey) []foreignKeyJSON {
	var out []foreignKeyJSON
	for _, fk := range fks {
		out = append(out, foreignKeyJSON{Namespace: fk.Namespace, Object: fk.Object, Columns: fk.Columns,
			RefNamespace: fk.RefNamespace, RefObject: fk.RefObject, RefColumns: fk.RefColumns})
	}
	return out
}

func toObject(o *engine.Object) objectJSON {
	out := objectJSON{Namespace: o.Namespace, Name: o.Name, Kind: string(o.Kind), Comment: o.Comment, RowEstimate: o.RowEstimate,
		Fields: make([]fieldJSON, 0, len(o.Fields)), PrimaryKey: o.PrimaryKey,
		ForeignKeys: toForeignKeys(o.ForeignKeys), ReferencedBy: toForeignKeys(o.ReferencedBy)}
	for _, f := range o.Fields {
		out.Fields = append(out.Fields, fieldJSON{Name: f.Name, Type: f.Type, Nullable: f.Nullable, Default: f.Default, Comment: f.Comment, Enum: f.Enum})
	}
	for _, ix := range o.Indexes {
		out.Indexes = append(out.Indexes, indexJSON{Name: ix.Name, Columns: ix.Columns, Unique: ix.Unique})
	}
	return out
}

func (a *api) getSchema(w http.ResponseWriter, r *http.Request) {
	pattern, object := r.URL.Query().Get("pattern"), r.URL.Query().Get("object")
	if !wantsJSON(r) {
		out, err := a.cfg.Service.Schema(r.Context(), workspace(r), core.SchemaInput{Source: r.PathValue("name"), Pattern: pattern, Object: object})
		if err != nil {
			a.fail(w, r, err)
			return
		}
		writeText(w, out)
		return
	}
	cat, err := a.cfg.Service.Catalog(r.Context(), workspace(r), r.PathValue("name"))
	if err != nil {
		a.fail(w, r, err)
		return
	}
	objs := format.Matching(cat, pattern, object)
	out := make([]objectJSON, 0, len(objs))
	for _, o := range objs {
		out = append(out, toObject(o))
	}
	writeJSON(w, http.StatusOK, map[string]any{"objects": out})
}

type queryRequest struct {
	Query   string `json:"query"`
	MaxRows int    `json:"max_rows,omitempty"`
	Explain bool   `json:"explain,omitempty"`
}

type columnJSON struct {
	Name string `json:"name"`
	Type string `json:"type,omitempty"`
}

type queryJSON struct {
	Columns      []columnJSON `json:"columns"`
	Rows         [][]any      `json:"rows"`
	Truncated    bool         `json:"truncated"`
	RowsAffected *int64       `json:"rows_affected,omitempty"`
	DurationMS   int64        `json:"duration_ms"`
}

func toQuery(r *engine.Result) queryJSON {
	q := queryJSON{Columns: make([]columnJSON, 0, len(r.Columns)), Rows: make([][]any, 0, len(r.Rows)),
		Truncated: r.Truncated, DurationMS: r.Duration.Milliseconds()}
	for _, c := range r.Columns {
		q.Columns = append(q.Columns, columnJSON{Name: c.Name, Type: c.Type})
	}
	for _, row := range r.Rows {
		out := make([]any, len(row))
		for i, v := range row {
			switch {
			case v.Null:
				out[i] = nil
			case v.Binary:
				out[i] = map[string]int{"bytes": v.Size}
			default:
				out[i] = v.Text
			}
		}
		q.Rows = append(q.Rows, out)
	}
	if r.RowsAffected >= 0 {
		n := r.RowsAffected
		q.RowsAffected = &n
	}
	return q
}

func (a *api) query(w http.ResponseWriter, r *http.Request) {
	var req queryRequest
	if err := decode(r, &req); err != nil {
		a.fail(w, r, err)
		return
	}
	in := core.QueryInput{Source: r.PathValue("name"), Query: req.Query, MaxRows: req.MaxRows, Explain: req.Explain}
	if !wantsJSON(r) {
		out, err := a.cfg.Service.Query(r.Context(), workspace(r), in)
		if err != nil {
			a.fail(w, r, err)
			return
		}
		writeText(w, out)
		return
	}
	res, err := a.cfg.Service.QueryResult(r.Context(), workspace(r), in)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toQuery(res))
}
```

Placeholders. Task 7 replaces each of these files.

`internal/httpapi/admin.go`:
```go
package httpapi

import "net/http"

func (a *api) listWorkspaces(w http.ResponseWriter, r *http.Request)  { w.WriteHeader(http.StatusNotImplemented) }
func (a *api) createWorkspace(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNotImplemented) }
func (a *api) deleteWorkspace(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNotImplemented) }
func (a *api) listKeys(w http.ResponseWriter, r *http.Request)        { w.WriteHeader(http.StatusNotImplemented) }
func (a *api) createKey(w http.ResponseWriter, r *http.Request)       { w.WriteHeader(http.StatusNotImplemented) }
func (a *api) deleteKey(w http.ResponseWriter, r *http.Request)       { w.WriteHeader(http.StatusNotImplemented) }
```

`internal/httpapi/mcp.go`:
```go
package httpapi

import (
	"net/http"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func (a *api) mcpServer(*http.Request) *mcp.Server { return nil }
```

`internal/httpapi/openapi.go`:
```go
package httpapi

import "net/http"

func serveOpenAPI(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNotImplemented) }
```
Create `internal/httpapi/openapi.json` as an empty file. Task 7 fills it.

- [ ] **Step 4: Run the tests**

Run: `go test -race ./internal/httpapi/ && go vet ./...`
Expected: PASS for every test in `api_test.go`.

- [ ] **Step 5: Commit**

```bash
git add internal/httpapi
git commit -m "feat(httpapi): add REST connection endpoints with auth, workspaces and per-key limits"
```

---

### Task 7: HTTP API: workspaces, keys, MCP over HTTP and the OpenAPI document

**Files:**
- Modify (replace the Task 6 placeholders): `internal/httpapi/admin.go`, `internal/httpapi/mcp.go`, `internal/httpapi/openapi.go`, `internal/httpapi/openapi.json`
- Test: `internal/httpapi/admin_test.go`, `internal/httpapi/mcp_test.go`, `internal/httpapi/openapi_test.go`

**Interfaces:**
- Consumes:
  - from Task 6: `api`, `route`, `routes()`, `fail`, `writeJSON`, `decode`, `principal`, `workspace`;
  - `core.Service` workspace operations;
  - `auth` key management;
  - `mcpserver.New`.
- Produces: the finished endpoints. No new exported names.

- [ ] **Step 1: Write the failing tests**

`internal/httpapi/admin_test.go`:
```go
package httpapi

import (
	"strings"
	"testing"

	"github.com/anderson-0/qh/internal/auth"
)

func TestWorkspacesAndKeys(t *testing.T) {
	e := newEnv(t, true, nil)
	if r := e.do(t, "POST", "/api/v1/workspaces", e.admin, map[string]any{"name": "team"}); r.status != 201 || !strings.Contains(r.body, `"name":"team"`) {
		t.Fatalf("create workspace: %d %s", r.status, r.body)
	}
	expectProblem(t, e.do(t, "POST", "/api/v1/workspaces", e.admin, map[string]any{"name": "team"}), 400, "invalid", `a workspace named "team" already exists`)
	var ws []struct{ Name string }
	e.do(t, "GET", "/api/v1/workspaces", e.admin, nil).json(t, &ws)
	if len(ws) != 2 || ws[1].Name != "team" {
		t.Fatalf("list workspaces: %+v", ws)
	}

	var created struct {
		ID        string   `json:"id"`
		Key       string   `json:"key"`
		Workspace string   `json:"workspace"`
		Scopes    []string `json:"scopes"`
	}
	r := e.do(t, "POST", "/api/v1/keys", e.admin, map[string]any{"workspace": "team", "scopes": []string{"query", "manage"}, "name": "team-admin", "expires_in": "90d"})
	r.json(t, &created)
	if r.status != 201 || !strings.HasPrefix(created.Key, "qh_") || created.Workspace != "team" {
		t.Fatalf("admin creates team key: %d %s", r.status, r.body)
	}
	team := created.Key

	expectProblem(t, e.do(t, "POST", "/api/v1/keys", team, map[string]any{"scopes": []string{"admin"}}), 403, "forbidden", "only admin keys can create admin keys")
	expectProblem(t, e.do(t, "POST", "/api/v1/keys", team, map[string]any{"workspace": "default", "scopes": []string{"query"}}), 403, "forbidden", "manage keys can only create keys for their own workspace")
	r = e.do(t, "POST", "/api/v1/keys", team, map[string]any{"scopes": []string{"query"}, "name": "agent"})
	r.json(t, &created)
	if r.status != 201 || created.Workspace != "team" {
		t.Fatalf("team creates a query key: %d %s", r.status, r.body)
	}
	agentID := created.ID

	var keys []struct {
		ID        string `json:"id"`
		Workspace string `json:"workspace"`
		Key       string `json:"key"`
	}
	e.do(t, "GET", "/api/v1/keys", team, nil).json(t, &keys)
	if len(keys) != 2 {
		t.Fatalf("team sees %d keys", len(keys))
	}
	for _, k := range keys {
		if k.Workspace != "team" || k.Key != "" {
			t.Fatalf("key listing leaks: %+v", k)
		}
	}
	e.do(t, "GET", "/api/v1/keys", e.admin, nil).json(t, &keys)
	if len(keys) != 3 {
		t.Fatalf("admin sees %d keys", len(keys))
	}
	e.key(t, "default", auth.ScopeQuery)
	e.do(t, "GET", "/api/v1/keys?workspace=default", e.admin, nil).json(t, &keys)
	if len(keys) != 1 || keys[0].Workspace != "default" {
		t.Fatalf("admin filter: %+v", keys)
	}
	defaultID := keys[0].ID
	expectProblem(t, e.do(t, "DELETE", "/api/v1/keys/"+defaultID, team, nil), 404, "unknown_source", "no API key with id")
	if r := e.do(t, "DELETE", "/api/v1/keys/"+agentID, team, nil); r.status != 204 {
		t.Fatalf("delete own key: %d %s", r.status, r.body)
	}

	expectProblem(t, e.do(t, "DELETE", "/api/v1/workspaces/default", e.admin, nil), 400, "invalid", "the default workspace cannot be deleted")
	if r := e.do(t, "DELETE", "/api/v1/workspaces/team", e.admin, nil); r.status != 204 {
		t.Fatalf("delete workspace: %d %s", r.status, r.body)
	}
	expectProblem(t, e.do(t, "GET", "/api/v1/connections", team, nil), 401, "auth", "invalid API key")
}
```

`internal/httpapi/mcp_test.go`:
```go
package httpapi

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/anderson-0/qh/internal/auth"
	"github.com/anderson-0/qh/internal/testutil"
)

type bearer struct{ token string }

func (b bearer) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+b.token)
	return http.DefaultTransport.RoundTrip(r)
}

func mcpCall(t *testing.T, e *env, token, tool string, args map[string]any) (string, bool) {
	t.Helper()
	ctx := context.Background()
	tr := &mcp.StreamableClientTransport{Endpoint: e.srv.URL + "/mcp", HTTPClient: &http.Client{Transport: bearer{token}}, DisableStandaloneSSE: true}
	s, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil).Connect(ctx, tr, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	res, err := s.CallTool(ctx, &mcp.CallToolParams{Name: tool, Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	return res.Content[0].(*mcp.TextContent).Text, res.IsError
}

func TestMCPOverHTTPIsWorkspaceScoped(t *testing.T) {
	e := newEnv(t, true, nil)
	if _, err := e.svc.CreateWorkspace(context.Background(), "other"); err != nil {
		t.Fatal(err)
	}
	mine := e.key(t, "default", auth.ScopeQuery, auth.ScopeManage)
	theirs := e.key(t, "other", auth.ScopeQuery)
	if r := e.do(t, "POST", "/api/v1/connections", mine, map[string]any{"name": "shop", "engine": "sqlite", "dsn": testutil.SQLiteFixture(t)}); r.status != 201 {
		t.Fatalf("create: %d %s", r.status, r.body)
	}
	if out, isErr := mcpCall(t, e, mine, "sources", map[string]any{}); isErr || out != "shop sqlite ro\n" {
		t.Fatalf("sources: %q", out)
	}
	if out, isErr := mcpCall(t, e, mine, "query", map[string]any{"source": "shop", "query": "SELECT count(*) AS n FROM orders"}); isErr || !strings.HasPrefix(out, "n\n250\n") {
		t.Fatalf("query: %q", out)
	}
	if out, _ := mcpCall(t, e, theirs, "sources", map[string]any{}); !strings.HasPrefix(out, "-- no connections") {
		t.Fatalf("other workspace sees: %q", out)
	}
	if out, isErr := mcpCall(t, e, theirs, "query", map[string]any{"source": "shop", "query": "SELECT 1"}); !isErr || !strings.HasPrefix(out, "error[unknown_source]") {
		t.Fatalf("other workspace queried shop: %q", out)
	}
	expectProblem(t, e.do(t, "GET", "/api/v1/connections/shop", theirs, nil), 404, "unknown_source", `no connection named "shop"`)
	if r := e.do(t, "POST", "/mcp", "", `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`); r.status != 401 {
		t.Fatalf("mcp without a key: %d", r.status)
	}
}
```

`internal/httpapi/openapi_test.go`:
```go
package httpapi

import (
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"testing"
)

func TestOpenAPIMatchesRoutes(t *testing.T) {
	var doc struct {
		OpenAPI string                                `json:"openapi"`
		Paths   map[string]map[string]json.RawMessage `json:"paths"`
	}
	if err := json.Unmarshal(openapiDoc, &doc); err != nil {
		t.Fatalf("openapi.json: %v", err)
	}
	if !strings.HasPrefix(doc.OpenAPI, "3.1") {
		t.Fatalf("openapi version %q", doc.OpenAPI)
	}
	documented := map[string]bool{}
	for path, ops := range doc.Paths {
		for method := range ops {
			if method != "parameters" {
				documented[strings.ToUpper(method)+" "+path] = true
			}
		}
	}
	a := &api{mcp: http.NotFoundHandler()}
	served := map[string]bool{}
	for _, rt := range a.routes() {
		m := rt.method
		if m == "" {
			m = "POST" // /mcp is documented as POST
		}
		served[m+" "+rt.pattern] = true
	}
	var missing, extra []string
	for k := range served {
		if !documented[k] {
			missing = append(missing, k)
		}
	}
	for k := range documented {
		if !served[k] {
			extra = append(extra, k)
		}
	}
	sort.Strings(missing)
	sort.Strings(extra)
	if len(missing)+len(extra) > 0 {
		t.Fatalf("undocumented routes %v; documented but not served %v", missing, extra)
	}
}

func TestOpenAPIIsServedPublicly(t *testing.T) {
	e := newEnv(t, true, nil)
	r := e.do(t, "GET", "/api/v1/openapi.json", "", nil)
	if r.status != 200 || !strings.Contains(r.body, `"openapi"`) || r.header.Get("Content-Type") != "application/json" {
		t.Fatalf("%d %s", r.status, r.header.Get("Content-Type"))
	}
}
```

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./internal/httpapi/ -run 'TestWorkspacesAndKeys|TestMCPOverHTTPIsWorkspaceScoped|TestOpenAPI'`
Expected: FAIL — 501 responses from the placeholders, and `openapiDoc` undefined.

- [ ] **Step 3: Implement**

`internal/httpapi/admin.go` (replace the file):
```go
package httpapi

import (
	"fmt"
	"net/http"
	"time"

	"github.com/anderson-0/qh/internal/auth"
	"github.com/anderson-0/qh/internal/qerr"
)

type workspaceJSON struct {
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"created_at"`
}

func (a *api) listWorkspaces(w http.ResponseWriter, r *http.Request) {
	list, err := a.cfg.Service.ListWorkspaces(r.Context())
	if err != nil {
		a.fail(w, r, err)
		return
	}
	out := make([]workspaceJSON, 0, len(list))
	for _, ws := range list {
		out = append(out, workspaceJSON{Name: ws.Name, CreatedAt: ws.CreatedAt})
	}
	writeJSON(w, http.StatusOK, out)
}

func (a *api) createWorkspace(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name string `json:"name"`
	}
	if err := decode(r, &in); err != nil {
		a.fail(w, r, err)
		return
	}
	ws, err := a.cfg.Service.CreateWorkspace(r.Context(), in.Name)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, workspaceJSON{Name: ws.Name, CreatedAt: ws.CreatedAt})
}

func (a *api) deleteWorkspace(w http.ResponseWriter, r *http.Request) {
	if err := a.cfg.Service.DeleteWorkspace(r.Context(), r.PathValue("name")); err != nil {
		a.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type keyJSON struct {
	ID         string     `json:"id"`
	Prefix     string     `json:"prefix"`
	Name       string     `json:"name,omitempty"`
	Workspace  string     `json:"workspace,omitempty"`
	Scopes     []string   `json:"scopes"`
	CreatedAt  time.Time  `json:"created_at"`
	ExpiresAt  *time.Time `json:"expires_at,omitempty"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
	Key        string     `json:"key,omitempty"` // only in the response that creates the key
}

func toKey(k *auth.KeyInfo) keyJSON {
	return keyJSON{ID: k.ID, Prefix: k.Prefix, Name: k.Name, Workspace: k.Workspace, Scopes: k.Scopes,
		CreatedAt: k.CreatedAt, ExpiresAt: k.ExpiresAt, LastUsedAt: k.LastUsedAt}
}

// listKeys: admin keys see every key (or one workspace with ?workspace=);
// manage keys see their own workspace's keys.
func (a *api) listKeys(w http.ResponseWriter, r *http.Request) {
	p := principal(r)
	var list []*auth.KeyInfo
	var err error
	if p.IsAdmin() {
		filter := r.URL.Query()
		list, err = auth.ListKeys(r.Context(), a.cfg.Store, filter.Get("workspace"), !filter.Has("workspace"))
	} else {
		list, err = auth.ListKeys(r.Context(), a.cfg.Store, p.Workspace, false)
	}
	if err != nil {
		a.fail(w, r, err)
		return
	}
	out := make([]keyJSON, 0, len(list))
	for _, k := range list {
		out = append(out, toKey(k))
	}
	writeJSON(w, http.StatusOK, out)
}

// createKey: admin keys create any key; manage keys create query/manage keys
// for their own workspace only.
func (a *api) createKey(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Workspace string   `json:"workspace,omitempty"`
		Scopes    []string `json:"scopes"`
		Name      string   `json:"name,omitempty"`
		ExpiresIn string   `json:"expires_in,omitempty"`
	}
	if err := decode(r, &in); err != nil {
		a.fail(w, r, err)
		return
	}
	exp, err := auth.ParseExpiry(in.ExpiresIn)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	if p := principal(r); !p.IsAdmin() {
		if in.Workspace != "" && in.Workspace != p.Workspace {
			a.fail(w, r, qerr.New(qerr.Forbidden, "manage keys can only create keys for their own workspace"))
			return
		}
		for _, s := range in.Scopes {
			if s == auth.ScopeAdmin {
				a.fail(w, r, qerr.New(qerr.Forbidden, "only admin keys can create admin keys"))
				return
			}
		}
		in.Workspace = p.Workspace
	}
	tok, info, err := auth.CreateKey(r.Context(), a.cfg.Store, auth.KeyInput{Workspace: in.Workspace, Scopes: in.Scopes, Name: in.Name, ExpiresIn: exp})
	if err != nil {
		a.fail(w, r, err)
		return
	}
	out := toKey(info)
	out.Key = tok
	writeJSON(w, http.StatusCreated, out)
}

// deleteKey: manage keys may revoke only their own workspace's keys, and
// other workspaces' keys look like they do not exist.
func (a *api) deleteKey(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if p := principal(r); !p.IsAdmin() {
		list, err := auth.ListKeys(r.Context(), a.cfg.Store, p.Workspace, false)
		if err != nil {
			a.fail(w, r, err)
			return
		}
		found := false
		for _, k := range list {
			found = found || k.ID == id
		}
		if !found {
			a.fail(w, r, qerr.New(qerr.UnknownSource, fmt.Sprintf("no API key with id %q", id)))
			return
		}
	}
	if err := auth.DeleteKey(r.Context(), a.cfg.Store, id); err != nil {
		a.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
```

`internal/httpapi/mcp.go` (replace the file):
```go
package httpapi

import (
	"context"
	"net/http"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/anderson-0/qh/internal/mcpserver"
)

// mcpServer returns the MCP server for the request's workspace, built once
// per workspace. The route has already authenticated the caller and
// resolved the workspace.
func (a *api) mcpServer(r *http.Request) *mcp.Server {
	ws := workspace(r)
	a.mcpMu.Lock()
	defer a.mcpMu.Unlock()
	s, ok := a.servers[ws]
	if !ok {
		s = mcpserver.New(a.cfg.Service, func(context.Context) string { return ws }, a.cfg.Version)
		a.servers[ws] = s
	}
	return s
}
```

`internal/httpapi/openapi.go` (replace the file):
```go
package httpapi

import (
	_ "embed"
	"net/http"
)

//go:embed openapi.json
var openapiDoc []byte

func serveOpenAPI(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(openapiDoc)
}
```

`internal/httpapi/openapi.json` (replace the empty file):
```json
{
  "openapi": "3.1.0",
  "info": {
    "title": "qh API",
    "version": "1",
    "description": "Manage database connections, workspaces and API keys, and query databases. Authenticate with Authorization: Bearer qh_…; admin keys name a workspace with X-QH-Workspace."
  },
  "components": {
    "securitySchemes": { "bearer": { "type": "http", "scheme": "bearer" } },
    "parameters": {
      "name": { "name": "name", "in": "path", "required": true, "schema": { "type": "string" } },
      "workspaceHeader": { "name": "X-QH-Workspace", "in": "header", "required": false, "schema": { "type": "string" }, "description": "Required for admin keys on workspace endpoints." },
      "skipTest": { "name": "skip_test", "in": "query", "required": false, "schema": { "type": "boolean" } }
    },
    "responses": {
      "problem": { "description": "Error", "content": { "application/problem+json": { "schema": { "$ref": "#/components/schemas/Problem" } } } },
      "noContent": { "description": "Done" }
    },
    "schemas": {
      "Problem": { "type": "object", "required": ["type", "title", "status"], "properties": {
        "type": { "type": "string", "enum": ["auth", "forbidden", "unknown_source", "invalid", "read_only", "timeout", "connection_failed", "query_error", "busy", "internal", "not_found", "unavailable"] },
        "title": { "type": "string" }, "detail": { "type": "string" }, "status": { "type": "integer" } } },
      "ConnectionInput": { "type": "object", "required": ["name", "engine", "dsn"], "additionalProperties": false, "properties": {
        "name": { "type": "string", "pattern": "^[a-z0-9][a-z0-9_-]{0,63}$" }, "engine": { "type": "string", "enum": ["postgres", "mysql", "mariadb", "sqlite"] },
        "dsn": { "type": "string" }, "description": { "type": "string" }, "mode": { "type": "string", "enum": ["ro", "rw"] },
        "max_rows": { "type": "integer" }, "timeout_ms": { "type": "integer" }, "pool_size": { "type": "integer" },
        "allowed_namespaces": { "type": "array", "items": { "type": "string" } } } },
      "ConnectionPatch": { "type": "object", "additionalProperties": false, "properties": {
        "dsn": { "type": "string" }, "description": { "type": "string" }, "mode": { "type": "string", "enum": ["ro", "rw"] },
        "max_rows": { "type": "integer" }, "timeout_ms": { "type": "integer" }, "pool_size": { "type": "integer" },
        "allowed_namespaces": { "type": "array", "items": { "type": "string" } } } },
      "Connection": { "type": "object", "properties": {
        "name": { "type": "string" }, "engine": { "type": "string" }, "description": { "type": "string" }, "mode": { "type": "string" },
        "host": { "type": "string" }, "port": { "type": "integer" }, "database": { "type": "string" }, "user": { "type": "string" },
        "dsn": { "type": "string", "description": "Password replaced by ***" }, "max_rows": { "type": "integer" }, "timeout_ms": { "type": "integer" },
        "pool_size": { "type": "integer" }, "allowed_namespaces": { "type": "array", "items": { "type": "string" } }, "version": { "type": "integer" },
        "created_at": { "type": "string", "format": "date-time" }, "updated_at": { "type": "string", "format": "date-time" },
        "server_version": { "type": "string" }, "connect_ms": { "type": "integer" } } },
      "TestResult": { "type": "object", "properties": { "server_version": { "type": "string" }, "connect_ms": { "type": "integer" } } },
      "QueryRequest": { "type": "object", "required": ["query"], "additionalProperties": false, "properties": {
        "query": { "type": "string" }, "max_rows": { "type": "integer" }, "explain": { "type": "boolean" } } },
      "QueryResult": { "type": "object", "properties": {
        "columns": { "type": "array", "items": { "type": "object", "properties": { "name": { "type": "string" }, "type": { "type": "string" } } } },
        "rows": { "type": "array", "items": { "type": "array", "items": {} }, "description": "Text values as strings, NULL as null, binary as {\"bytes\": N}" },
        "truncated": { "type": "boolean" }, "rows_affected": { "type": "integer" }, "duration_ms": { "type": "integer" } } },
      "Workspace": { "type": "object", "properties": { "name": { "type": "string" }, "created_at": { "type": "string", "format": "date-time" } } },
      "KeyInput": { "type": "object", "required": ["scopes"], "additionalProperties": false, "properties": {
        "workspace": { "type": "string", "description": "Omit for admin keys; manage keys always create keys for their own workspace" },
        "scopes": { "type": "array", "items": { "type": "string", "enum": ["query", "manage", "admin"] } },
        "name": { "type": "string" }, "expires_in": { "type": "string", "description": "90d or a Go duration such as 720h" } } },
      "Key": { "type": "object", "properties": {
        "id": { "type": "string" }, "prefix": { "type": "string" }, "name": { "type": "string" }, "workspace": { "type": "string" },
        "scopes": { "type": "array", "items": { "type": "string" } }, "created_at": { "type": "string", "format": "date-time" },
        "expires_at": { "type": "string", "format": "date-time" }, "last_used_at": { "type": "string", "format": "date-time" },
        "key": { "type": "string", "description": "Full key; present only in the create response" } } }
    }
  },
  "security": [{ "bearer": [] }],
  "paths": {
    "/healthz": { "get": { "summary": "Process is up", "security": [], "responses": { "200": { "description": "ok" } } } },
    "/readyz": { "get": { "summary": "Store is reachable", "security": [], "responses": { "200": { "description": "ok" }, "503": { "$ref": "#/components/responses/problem" } } } },
    "/api/v1/openapi.json": { "get": { "summary": "This document", "security": [], "responses": { "200": { "description": "OpenAPI document" } } } },
    "/mcp": { "post": { "summary": "MCP Streamable HTTP endpoint (stateless); scope query", "parameters": [{ "$ref": "#/components/parameters/workspaceHeader" }],
      "responses": { "200": { "description": "JSON-RPC response" }, "default": { "$ref": "#/components/responses/problem" } } } },
    "/api/v1/connections": {
      "get": { "summary": "List connections; scope query", "parameters": [{ "$ref": "#/components/parameters/workspaceHeader" }],
        "responses": { "200": { "description": "Connections", "content": { "application/json": { "schema": { "type": "array", "items": { "$ref": "#/components/schemas/Connection" } } } } }, "default": { "$ref": "#/components/responses/problem" } } },
      "post": { "summary": "Create a connection (tested first unless skip_test=true); scope manage",
        "parameters": [{ "$ref": "#/components/parameters/workspaceHeader" }, { "$ref": "#/components/parameters/skipTest" }],
        "requestBody": { "required": true, "content": { "application/json": { "schema": { "$ref": "#/components/schemas/ConnectionInput" } } } },
        "responses": { "201": { "description": "Created", "content": { "application/json": { "schema": { "$ref": "#/components/schemas/Connection" } } } }, "default": { "$ref": "#/components/responses/problem" } } } },
    "/api/v1/connections/test": { "post": { "summary": "Test connection details without saving; scope manage", "parameters": [{ "$ref": "#/components/parameters/workspaceHeader" }],
      "requestBody": { "required": true, "content": { "application/json": { "schema": { "$ref": "#/components/schemas/ConnectionInput" } } } },
      "responses": { "200": { "description": "Reachable", "content": { "application/json": { "schema": { "$ref": "#/components/schemas/TestResult" } } } }, "default": { "$ref": "#/components/responses/problem" } } } },
    "/api/v1/connections/{name}": {
      "parameters": [{ "$ref": "#/components/parameters/name" }, { "$ref": "#/components/parameters/workspaceHeader" }],
      "get": { "summary": "Get a connection; scope query", "responses": { "200": { "description": "Connection", "content": { "application/json": { "schema": { "$ref": "#/components/schemas/Connection" } } } }, "default": { "$ref": "#/components/responses/problem" } } },
      "patch": { "summary": "Change a connection; scope manage", "parameters": [{ "$ref": "#/components/parameters/skipTest" }],
        "requestBody": { "required": true, "content": { "application/json": { "schema": { "$ref": "#/components/schemas/ConnectionPatch" } } } },
        "responses": { "200": { "description": "Updated", "content": { "application/json": { "schema": { "$ref": "#/components/schemas/Connection" } } } }, "default": { "$ref": "#/components/responses/problem" } } },
      "delete": { "summary": "Delete a connection; scope manage", "responses": { "204": { "$ref": "#/components/responses/noContent" }, "default": { "$ref": "#/components/responses/problem" } } } },
    "/api/v1/connections/{name}/test": { "post": { "summary": "Test a stored connection; scope manage",
      "parameters": [{ "$ref": "#/components/parameters/name" }, { "$ref": "#/components/parameters/workspaceHeader" }],
      "responses": { "200": { "description": "Reachable", "content": { "application/json": { "schema": { "$ref": "#/components/schemas/TestResult" } } } }, "default": { "$ref": "#/components/responses/problem" } } } },
    "/api/v1/connections/{name}/schema": { "get": { "summary": "Schema overview or details (text/plain by default, JSON with Accept: application/json); scope query",
      "parameters": [{ "$ref": "#/components/parameters/name" }, { "$ref": "#/components/parameters/workspaceHeader" },
        { "name": "pattern", "in": "query", "schema": { "type": "string" } }, { "name": "object", "in": "query", "schema": { "type": "string" } }],
      "responses": { "200": { "description": "Schema", "content": { "text/plain": {}, "application/json": {} } }, "default": { "$ref": "#/components/responses/problem" } } } },
    "/api/v1/connections/{name}/schema/refresh": { "post": { "summary": "Rebuild the cached schema; scope manage",
      "parameters": [{ "$ref": "#/components/parameters/name" }, { "$ref": "#/components/parameters/workspaceHeader" }],
      "responses": { "200": { "description": "Objects and time taken", "content": { "application/json": {} } }, "default": { "$ref": "#/components/responses/problem" } } } },
    "/api/v1/connections/{name}/query": { "post": { "summary": "Run one statement (TSV by default, JSON with Accept: application/json); scope query",
      "parameters": [{ "$ref": "#/components/parameters/name" }, { "$ref": "#/components/parameters/workspaceHeader" }],
      "requestBody": { "required": true, "content": { "application/json": { "schema": { "$ref": "#/components/schemas/QueryRequest" } } } },
      "responses": { "200": { "description": "Result", "content": { "text/plain": {}, "application/json": { "schema": { "$ref": "#/components/schemas/QueryResult" } } } }, "default": { "$ref": "#/components/responses/problem" } } } },
    "/api/v1/workspaces": {
      "get": { "summary": "List workspaces; scope admin", "responses": { "200": { "description": "Workspaces", "content": { "application/json": { "schema": { "type": "array", "items": { "$ref": "#/components/schemas/Workspace" } } } } }, "default": { "$ref": "#/components/responses/problem" } } },
      "post": { "summary": "Create a workspace; scope admin",
        "requestBody": { "required": true, "content": { "application/json": { "schema": { "type": "object", "required": ["name"], "properties": { "name": { "type": "string" } } } } } },
        "responses": { "201": { "description": "Created", "content": { "application/json": { "schema": { "$ref": "#/components/schemas/Workspace" } } } }, "default": { "$ref": "#/components/responses/problem" } } } },
    "/api/v1/workspaces/{name}": { "delete": { "summary": "Delete a workspace with its connections and keys; scope admin",
      "parameters": [{ "$ref": "#/components/parameters/name" }],
      "responses": { "204": { "$ref": "#/components/responses/noContent" }, "default": { "$ref": "#/components/responses/problem" } } } },
    "/api/v1/keys": {
      "get": { "summary": "List keys (admin: all or ?workspace=; manage: own workspace); scope manage",
        "parameters": [{ "name": "workspace", "in": "query", "schema": { "type": "string" } }],
        "responses": { "200": { "description": "Keys", "content": { "application/json": { "schema": { "type": "array", "items": { "$ref": "#/components/schemas/Key" } } } } }, "default": { "$ref": "#/components/responses/problem" } } },
      "post": { "summary": "Create a key; the full key appears only in this response; scope manage",
        "requestBody": { "required": true, "content": { "application/json": { "schema": { "$ref": "#/components/schemas/KeyInput" } } } },
        "responses": { "201": { "description": "Created", "content": { "application/json": { "schema": { "$ref": "#/components/schemas/Key" } } } }, "default": { "$ref": "#/components/responses/problem" } } } },
    "/api/v1/keys/{id}": { "delete": { "summary": "Revoke a key; scope manage",
      "parameters": [{ "name": "id", "in": "path", "required": true, "schema": { "type": "string" } }],
      "responses": { "204": { "$ref": "#/components/responses/noContent" }, "default": { "$ref": "#/components/responses/problem" } } } }
  }
}
```

- [ ] **Step 4: Run the tests**

Run: `go test -race ./internal/httpapi/ && go vet ./...`
Expected: PASS for all httpapi tests, including Task 6's.

- [ ] **Step 5: Commit**

```bash
git add internal/httpapi
git commit -m "feat(httpapi): add workspace and key endpoints, MCP over HTTP and the OpenAPI document"
```

---

### Task 8: `qh serve`, `qh workspace`, `qh key`, end-to-end test and README

**Files:**
- Create: `internal/cli/serve.go`, `internal/cli/admin.go`, `cmd/qh/serve_e2e_test.go`
- Modify: `internal/cli/app.go` (add `openAppWith`), `internal/cli/root.go` (register the commands), `cmd/qh/e2e_test.go` (use the shared build helper), `README.md`
- Test: `internal/cli/admin_test.go`, `cmd/qh/serve_e2e_test.go`

**Interfaces:**
- Consumes: `httpapi.New`/`Config`, `auth.NewAuthenticator`, `auth.Bootstrap`, `auth.CreateKey`/`ListKeys`/`DeleteKey`/`ParseExpiry`, `netguard.New`, `core.Options`, `core.Service` workspace operations.
- Produces:
  - `qh serve [--http ADDR] [--data-dir DIR] [--sqlite-root DIR] [--allow-cidr CIDR]… [--deny-cidr CIDR]…`;
  - `qh workspace create|ls|rm`;
  - `qh key create|ls|rm`;
  - `openAppWith(home string, opts core.Options) (*app, error)`.

- [ ] **Step 1: Write the failing tests**

`internal/cli/admin_test.go`:
```go
package cli

import (
	"regexp"
	"strings"
	"testing"
)

func TestWorkspaceAndKeyCommands(t *testing.T) {
	home := t.TempDir()
	if out, errOut, code := run(t, home, "", "workspace", "create", "team"); code != 0 || out != "created workspace team\n" {
		t.Fatalf("create: %d %q %q", code, out, errOut)
	}
	if out, _, _ := run(t, home, "", "workspace", "ls"); !strings.Contains(out, "default") || !strings.Contains(out, "team") {
		t.Fatalf("ls: %q", out)
	}
	out, errOut, code := run(t, home, "", "key", "create", "--workspace", "team", "--scopes", "query,manage", "--name", "ci", "--expires", "30d")
	if code != 0 || !regexp.MustCompile(`(?m)^qh_[a-z0-9]{8}_[0-9A-Za-z]+$`).MatchString(out) || !strings.Contains(out, "will not be shown again") {
		t.Fatalf("key create: %d %q %q", code, out, errOut)
	}
	if out, _, code = run(t, home, "", "key", "create", "--scopes", "admin"); code != 0 || !strings.Contains(out, "admin key") {
		t.Fatalf("admin key: %d %q", code, out)
	}
	out, _, _ = run(t, home, "", "key", "ls")
	if !strings.HasPrefix(out, "ID") || strings.Count(out, "\n") != 3 || !strings.Contains(out, "query,manage") || strings.Contains(out, "qh_") {
		t.Fatalf("key ls: %q", out)
	}
	id := strings.Fields(strings.Split(out, "\n")[1])[0]
	if out, _, code := run(t, home, "", "key", "rm", id); code != 0 || out != "revoked key "+id+"\n" {
		t.Fatalf("key rm: %d %q", code, out)
	}
	if _, errOut, code := run(t, home, "", "key", "create", "--workspace", "team", "--scopes", "root"); code != 1 || !strings.HasPrefix(errOut, `error[invalid]: unknown scope "root"`) {
		t.Fatalf("bad scope: %d %q", code, errOut)
	}
	if out, _, code := run(t, home, "", "workspace", "rm", "team"); code != 0 || out != "deleted workspace team\n" {
		t.Fatalf("rm: %d %q", code, out)
	}
}

func TestServeFlagValidation(t *testing.T) {
	home := t.TempDir()
	t.Setenv("QH_MASTER_KEY", "")
	if _, errOut, code := run(t, home, "", "serve", "--http", "127.0.0.1:0"); code != 1 || !strings.HasPrefix(errOut, "error[invalid]: server mode needs QH_MASTER_KEY") {
		t.Fatalf("missing master key: %d %q", code, errOut)
	}
	if _, errOut, code := run(t, home, "", "serve", "--sqlite-root", home); code != 1 || !strings.HasPrefix(errOut, "error[invalid]: --allow-cidr, --deny-cidr and --sqlite-root apply to server mode only") {
		t.Fatalf("server-only flags in local mode: %d %q", code, errOut)
	}
}
```

`cmd/qh/serve_e2e_test.go`:
```go
package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/anderson-0/qh/internal/testutil"
)

// buildBinary builds cmd/qh once per test into a temp dir.
func buildBinary(t *testing.T) string {
	t.Helper()
	if testing.Short() {
		t.Skip("builds the binary")
	}
	bin := filepath.Join(t.TempDir(), "qh")
	build := exec.Command("go", "build", "-o", bin, ".")
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	return bin
}

type bearerRT struct{ token string }

func (b bearerRT) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+b.token)
	return http.DefaultTransport.RoundTrip(r)
}

func TestServeEndToEnd(t *testing.T) {
	bin := buildBinary(t)
	db := testutil.SQLiteFixture(t)
	key := make([]byte, 32)
	rand.Read(key)
	cmd := exec.Command(bin, "serve", "--http", "127.0.0.1:0", "--data-dir", t.TempDir(), "--sqlite-root", filepath.Dir(db))
	cmd.Env = append(os.Environ(), "QH_MASTER_KEY="+base64.StdEncoding.EncodeToString(key), "QH_BOOTSTRAP_ADMIN_KEY=")
	stderr, _ := cmd.StderrPipe()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })

	var base, admin string
	lines := bufio.NewScanner(stderr)
	deadline := time.After(20 * time.Second)
	found := make(chan struct{})
	go func() {
		for lines.Scan() {
			l := lines.Text()
			if v, ok := strings.CutPrefix(l, "admin API key (shown once): "); ok {
				admin = v
			}
			if v, ok := strings.CutPrefix(l, "qh listening on "); ok {
				base = strings.Fields(v)[0]
				close(found)
				break
			}
		}
		io.Copy(io.Discard, stderr)
	}()
	select {
	case <-found:
	case <-deadline:
		t.Fatal("server did not start")
	}
	if admin == "" {
		t.Fatal("no admin key printed")
	}

	call := func(method, path, token string, body any) (int, string) {
		t.Helper()
		var rd io.Reader
		if body != nil {
			b, _ := json.Marshal(body)
			rd = bytes.NewReader(b)
		}
		req, _ := http.NewRequest(method, base+path, rd)
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		req.Header.Set("Accept", "application/json")
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return res.StatusCode, string(b)
	}

	if code, _ := call("GET", "/api/v1/connections", "", nil); code != 401 {
		t.Fatalf("no key: %d", code)
	}
	if code, body := call("POST", "/api/v1/workspaces", admin, map[string]any{"name": "team"}); code != 201 {
		t.Fatalf("workspace: %d %s", code, body)
	}
	code, body := call("POST", "/api/v1/keys", admin, map[string]any{"workspace": "team", "scopes": []string{"query", "manage"}})
	var k struct{ Key string }
	json.Unmarshal([]byte(body), &k)
	if code != 201 || k.Key == "" {
		t.Fatalf("key: %d %s", code, body)
	}
	if code, body := call("POST", "/api/v1/connections", k.Key, map[string]any{"name": "shop", "engine": "sqlite", "dsn": db}); code != 201 {
		t.Fatalf("sqlite inside root: %d %s", code, body)
	}
	if code, body := call("POST", "/api/v1/connections", k.Key, map[string]any{"name": "evil", "engine": "postgres", "dsn": "postgres://u:p@127.0.0.1:5432/db?connect_timeout=2"}); code != 502 || !strings.Contains(body, "not allowed by this server's network policy") {
		t.Fatalf("loopback postgres: %d %s", code, body)
	}
	if code, body := call("POST", "/api/v1/connections/shop/query", k.Key, map[string]any{"query": "SELECT count(*) AS n FROM orders"}); code != 200 || !strings.Contains(body, `"rows":[["250"]]`) {
		t.Fatalf("query: %d %s", code, body)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	tr := &mcp.StreamableClientTransport{Endpoint: base + "/mcp", HTTPClient: &http.Client{Transport: bearerRT{k.Key}}, DisableStandaloneSSE: true}
	session, err := mcp.NewClient(&mcp.Implementation{Name: "e2e", Version: "0"}, nil).Connect(ctx, tr, nil)
	if err != nil {
		t.Fatal(err)
	}
	res, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "sources", Arguments: map[string]any{}})
	if err != nil || res.Content[0].(*mcp.TextContent).Text != "shop sqlite ro\n" {
		t.Fatalf("mcp sources: %v %+v", err, res)
	}
	session.Close()

	if err := cmd.Process.Signal(os.Interrupt); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("server exited with %v", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("server did not shut down")
	}
}
```

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./internal/cli/ -run 'TestWorkspaceAndKeyCommands|TestServeFlagValidation' && go test ./cmd/qh/ -run TestServeEndToEnd`
Expected: FAIL — `unknown command "workspace"` / `unknown command "serve"`.

- [ ] **Step 3: Implement**

In `internal/cli/app.go`, rename `openApp` to `openAppWith(home string, opts core.Options) (*app, error)`. Change its `core.New(...)` call to pass `opts` as the last argument, and add:
```go
func openApp(home string) (*app, error) { return openAppWith(home, core.Options{}) }
```

In `internal/cli/root.go` `newRoot`, add `serveCmd(&home, version), workspaceCmd(with), keyCmd(with),` to the `root.AddCommand(...)` list.

`internal/cli/serve.go`:
```go
package cli

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"github.com/anderson-0/qh/internal/auth"
	"github.com/anderson-0/qh/internal/core"
	"github.com/anderson-0/qh/internal/httpapi"
	"github.com/anderson-0/qh/internal/netguard"
	"github.com/anderson-0/qh/internal/qerr"
)

const localAddr = "127.0.0.1:8765"

func serveCmd(home *string, version string) *cobra.Command {
	var listen, dataDir, sqliteRoot string
	var allow, deny []string
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Serve MCP over HTTP and the REST API (local mode on " + localAddr + ", or server mode with --http)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			server := listen != ""
			var opts core.Options
			addr := localAddr
			if server {
				addr = listen
				if os.Getenv("QH_MASTER_KEY") == "" {
					return qerr.New(qerr.Invalid, "server mode needs QH_MASTER_KEY").
						WithHint("Set it to a base64-encoded 32-byte key, e.g. openssl rand -base64 32")
				}
				policy, err := netguard.New(allow, deny)
				if err != nil {
					return err
				}
				opts.Dial = policy.DialContext
				if sqliteRoot == "" {
					opts.SQLiteDisabled = true
				} else if opts.SQLiteRoot, err = filepath.Abs(sqliteRoot); err != nil {
					return err
				}
			} else if len(allow)+len(deny) > 0 || sqliteRoot != "" {
				return qerr.New(qerr.Invalid, "--allow-cidr, --deny-cidr and --sqlite-root apply to server mode only").WithHint("Add --http ADDR")
			}
			dir := dataDir
			if dir == "" {
				var err error
				if dir, err = homeDir(*home); err != nil {
					return err
				}
			}
			a, err := openAppWith(dir, opts)
			if err != nil {
				return err
			}
			defer a.Close()
			stderr := cmd.ErrOrStderr()
			cfg := httpapi.Config{Service: a.svc, Store: a.st, Version: version, Logger: slog.New(slog.NewJSONHandler(stderr, nil))}
			mode := "local"
			if server {
				mode = "server"
				cfg.Auth = auth.NewAuthenticator(a.st)
				tok, created, err := auth.Bootstrap(cmd.Context(), a.st, os.Getenv("QH_BOOTSTRAP_ADMIN_KEY"))
				if err != nil {
					return err
				}
				if tok != "" {
					fmt.Fprintf(stderr, "admin API key (shown once): %s\n", tok)
				} else if created {
					fmt.Fprintln(stderr, "admin API key taken from QH_BOOTSTRAP_ADMIN_KEY")
				}
			}
			ln, err := net.Listen("tcp", addr)
			if err != nil {
				return fmt.Errorf("cannot listen on %s: %w", addr, err)
			}
			fmt.Fprintf(stderr, "qh listening on http://%s (%s mode)\n", ln.Addr(), mode)
			srv := &http.Server{Handler: httpapi.New(cfg), ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 2 * time.Minute}
			ctx := cmd.Context()
			a.pools.Start(ctx)
			done := make(chan error, 1)
			go func() { done <- srv.Serve(ln) }()
			select {
			case err := <-done:
				return err
			case <-ctx.Done():
				sctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				if err := srv.Shutdown(sctx); err != nil {
					return err
				}
				<-done
				return nil
			}
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&listen, "http", "", "server mode: listen on this address (e.g. :8080); requires QH_MASTER_KEY and API keys")
	fl.StringVar(&dataDir, "data-dir", "", "data directory (default: the same as --home)")
	fl.StringVar(&sqliteRoot, "sqlite-root", "", "server mode: allow SQLite files under this directory (SQLite is disabled otherwise)")
	fl.StringSliceVar(&allow, "allow-cidr", nil, "server mode: allow database addresses in this range, even if denied by default (repeatable)")
	fl.StringSliceVar(&deny, "deny-cidr", nil, "server mode: also refuse database addresses in this range (repeatable)")
	return cmd
}
```

`internal/cli/admin.go`:
```go
package cli

import (
	"fmt"
	"slices"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/anderson-0/qh/internal/auth"
)

func workspaceCmd(with withApp) *cobra.Command {
	c := &cobra.Command{Use: "workspace", Short: "Manage workspaces (local store)"}
	c.AddCommand(
		&cobra.Command{Use: "create <name>", Short: "Create a workspace", Args: cobra.ExactArgs(1),
			RunE: with(func(cmd *cobra.Command, a *app, args []string) error {
				if _, err := a.svc.CreateWorkspace(cmd.Context(), args[0]); err != nil {
					return err
				}
				fmt.Fprintf(cmd.OutOrStdout(), "created workspace %s\n", args[0])
				return nil
			})},
		&cobra.Command{Use: "ls", Aliases: []string{"list"}, Short: "List workspaces", Args: cobra.NoArgs,
			RunE: with(func(cmd *cobra.Command, a *app, _ []string) error {
				list, err := a.svc.ListWorkspaces(cmd.Context())
				if err != nil {
					return err
				}
				w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
				fmt.Fprintln(w, "NAME\tCREATED")
				for _, ws := range list {
					fmt.Fprintf(w, "%s\t%s\n", ws.Name, ws.CreatedAt.Format(time.RFC3339))
				}
				return w.Flush()
			})},
		&cobra.Command{Use: "rm <name>", Aliases: []string{"remove"}, Short: "Delete a workspace with its connections and keys", Args: cobra.ExactArgs(1),
			RunE: with(func(cmd *cobra.Command, a *app, args []string) error {
				if err := a.svc.DeleteWorkspace(cmd.Context(), args[0]); err != nil {
					return err
				}
				fmt.Fprintf(cmd.OutOrStdout(), "deleted workspace %s\n", args[0])
				return nil
			})},
	)
	return c
}

func keyCmd(with withApp) *cobra.Command {
	c := &cobra.Command{Use: "key", Short: "Manage API keys for server mode (local store)"}
	var workspace, name, expires string
	var scopes []string
	create := &cobra.Command{Use: "create", Short: "Create an API key (shown once)", Args: cobra.NoArgs,
		RunE: with(func(cmd *cobra.Command, a *app, _ []string) error {
			exp, err := auth.ParseExpiry(expires)
			if err != nil {
				return err
			}
			ws := workspace
			if slices.Contains(scopes, auth.ScopeAdmin) && !cmd.Flags().Changed("workspace") {
				ws = ""
			}
			tok, info, err := auth.CreateKey(cmd.Context(), a.st, auth.KeyInput{Workspace: ws, Scopes: scopes, Name: name, ExpiresIn: exp})
			if err != nil {
				return err
			}
			owner := "workspace " + info.Workspace
			if info.Workspace == "" {
				owner = "admin key"
			}
			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "created key %s (%s, %s)\n%s\nStore it now; it will not be shown again.\n", info.ID, strings.Join(info.Scopes, ","), owner, tok)
			return nil
		})}
	create.Flags().StringVar(&workspace, "workspace", "default", "workspace the key belongs to (admin keys belong to none)")
	create.Flags().StringSliceVar(&scopes, "scopes", []string{auth.ScopeQuery}, "query, manage and/or admin")
	create.Flags().StringVar(&name, "name", "", "label for the key")
	create.Flags().StringVar(&expires, "expires", "", "lifetime such as 90d or 720h (default: never)")
	c.AddCommand(create,
		&cobra.Command{Use: "ls", Aliases: []string{"list"}, Short: "List API keys", Args: cobra.NoArgs,
			RunE: with(func(cmd *cobra.Command, a *app, _ []string) error {
				list, err := auth.ListKeys(cmd.Context(), a.st, "", true)
				if err != nil {
					return err
				}
				w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
				fmt.Fprintln(w, "ID\tPREFIX\tWORKSPACE\tSCOPES\tNAME\tLAST USED\tEXPIRES")
				when := func(t *time.Time) string {
					if t == nil {
						return "-"
					}
					return t.Format(time.RFC3339)
				}
				for _, k := range list {
					ws := k.Workspace
					if ws == "" {
						ws = "(admin)"
					}
					fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", k.ID, k.Prefix, ws, strings.Join(k.Scopes, ","), k.Name, when(k.LastUsedAt), when(k.ExpiresAt))
				}
				return w.Flush()
			})},
		&cobra.Command{Use: "rm <id>", Aliases: []string{"revoke"}, Short: "Revoke an API key", Args: cobra.ExactArgs(1),
			RunE: with(func(cmd *cobra.Command, a *app, args []string) error {
				if err := auth.DeleteKey(cmd.Context(), a.st, args[0]); err != nil {
					return err
				}
				fmt.Fprintf(cmd.OutOrStdout(), "revoked key %s\n", args[0])
				return nil
			})},
	)
	return c
}
```

In `cmd/qh/e2e_test.go`, replace the inline `go build` block in `TestStdioEndToEnd` (the `testing.Short` skip, the `bin :=` line, the `exec.Command("go", "build", …)` and its error check) with `bin := buildBinary(t)`, and remove any imports that become unused.

`README.md`: before the "Read-only safety" section, add a "Server mode" section with this content:
````markdown
## Server mode

Run qh for a team or as the backend of a product:

```bash
export QH_MASTER_KEY=$(openssl rand -base64 32)   # keep it: it decrypts stored connection strings
qh serve --http :8080 --data-dir /var/lib/qh
```

On first start qh prints one admin API key (`admin API key (shown once): qh_…`), or uses `QH_BOOTSTRAP_ADMIN_KEY`
if you set it. Every request needs `Authorization: Bearer <key>`:

- **Workspaces** isolate connections. Admin keys manage workspaces and keys and pick a workspace with the
  `X-QH-Workspace` header.
- **Scopes:** `query` (MCP tools, schema, query), `manage` (create, change and delete connections; create
  query/manage keys for the same workspace), `admin` (everything).
- **MCP** is served at `/mcp` (Streamable HTTP). The REST API is under `/api/v1`; see `/api/v1/openapi.json`.

```bash
curl -s -X POST localhost:8080/api/v1/workspaces -H "Authorization: Bearer $ADMIN" -d '{"name":"team"}'
curl -s -X POST localhost:8080/api/v1/keys -H "Authorization: Bearer $ADMIN" \
  -d '{"workspace":"team","scopes":["query","manage"],"name":"team"}'
curl -s -X POST localhost:8080/api/v1/connections -H "Authorization: Bearer $TEAM" \
  -d '{"name":"shop","engine":"postgres","dsn":"postgres://reader:…@db.internal/shop"}'
claude mcp add --transport http qh http://localhost:8080/mcp --header "Authorization: Bearer $TEAM"
```

Server mode refuses database addresses on loopback and link-local ranges (including cloud metadata at
169.254.169.254), and Unix sockets. Use `--allow-cidr 127.0.0.0/8` to allow a local database, and `--deny-cidr`
to block more (for example your private ranges). SQLite is off unless you pass `--sqlite-root DIR`; files must
then live under `DIR`. Each API key may run 4 queries at once; extra requests get HTTP 429.

Without `--http`, `qh serve` runs in local mode on `127.0.0.1:8765` with no keys, for tools on your own machine.
````

- [ ] **Step 4: Run the tests and full verification**

Run: `go test ./internal/cli/ ./cmd/qh/ && go vet ./... && go test -race ./... && make build`
Expected: all PASS (Docker running). `TestServeEndToEnd` and `TestStdioEndToEnd` both build and drive the real binary.

- [ ] **Step 5: Commit**

```bash
git add internal/cli cmd/qh README.md
git commit -m "feat(cli): add qh serve, qh workspace and qh key"
```
