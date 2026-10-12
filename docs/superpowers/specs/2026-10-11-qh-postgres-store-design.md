# qh — Postgres store and multiple server instances (design spec)

Status: draft for review, 2026-10-11. This extends `2026-10-09-qh-design.md`. Its §2 lists "Running several server instances against one store (needs a Postgres-backed `Store`; the interface allows it)" under "Not in v1". That spec governs everything not covered here.

## 1. Purpose

Today `qh serve` keeps its store in one SQLite file, so a deployment is one process on one host. Teams that host qh for many agents want:

- several instances behind a load balancer, for availability and rolling upgrades;
- a store that lives with their other operational data and is backed up by their usual tools.

**Success criteria:**

- **Postgres store.** With `--store postgres://…` (or `QH_STORE_DSN`), every qh command uses a Postgres store that implements the same `store.Store` interface, with the same behaviour as SQLite. One shared conformance suite proves this against both stores.
- **Instances are interchangeable.** Two or more `qh serve` instances on one Postgres store behave like one server to clients:
  - a connection, key or workspace changed through any instance takes effect on the next call to every instance;
  - a key revoked on one instance is refused everywhere at once;
  - a schema change seen by one instance is not served stale by the others;
  - starting several instances at once on an empty store creates exactly one admin key and applies each migration once.
- **Master-key rotation works on a running fleet,** with no downtime.
- **Existing installs can move.** `qh store copy` moves an existing SQLite store to Postgres without decrypting anything.
- **Defaults are unchanged.** SQLite stays the default. Local mode and existing installs behave exactly as before.

## 2. Decisions

| Question | Decision | Why |
|---|---|---|
| How the store is chosen | A global `--store DSN` flag, defaulting to `$QH_STORE_DSN`. Empty means SQLite in the data directory, as today. Only `postgres://` and `postgresql://` URLs are accepted. | Every command (serve, mcp, conn, key, masterkey, store copy) must reach the same store. An explicit DSN is needed because server mode clears `PG*` variables at startup (`clearPGEnv`). |
| Driver | pgx v5 through `database/sql` (`github.com/jackc/pgx/v5/stdlib`). pgx is already a dependency. | The SQLite store is written against `database/sql`, so the Postgres store can mirror it method for method. |
| Schema types | Native types: `timestamptz`, `bytea`, `bigserial` for the query-log id, `text` for JSON summaries and scopes as SQLite stores them. | Readable for operators. The encoding stays inside each store. |
| Migrations | A `qh_schema_migrations(version)` table, applied in one transaction under `pg_advisory_xact_lock`. | Concurrent first starts serialise, and each step is applied once. This replaces SQLite's `BEGIN IMMEDIATE` + `user_version`. |
| Schema cache across instances | Each store reports invalidations. Postgres does it with `LISTEN`/`NOTIFY` on channel `qh_catalog`; SQLite does nothing, because it is one process. | A DDL statement or `RefreshSchema` on one instance drops the cached schema everywhere within milliseconds. The cost is one listening connection per instance. |
| Admin-key bootstrap | It becomes atomic: a new store method creates the key only if none exists. SQLite does it in one `BEGIN IMMEDIATE` transaction; Postgres under an advisory lock. | Two instances starting on an empty store must create one key, not two. A fixed `QH_BOOTSTRAP_ADMIN_KEY` must not make the second instance exit. |
| Rotating the master key on a running fleet | `qh serve` accepts `QH_MASTER_KEY_OLD` (comma-separated) as extra keys for opening sealed data. Documented procedure in §5. | The keyring already supports old keys. Instances that can open both keys never fail while connections are re-sealed. |
| Limits that are per process | They stay per instance and are documented: the per-key concurrency limit, `pool_size`, and `MaxTotalConns`. | A cluster-wide limiter needs shared state on every request. Operators size each instance's limits knowing the instance count. |
| Moving an existing install | `qh store copy --to DSN` copies everything from the current store, sealed secrets byte for byte, into an empty Postgres store. | No re-encryption is needed, as long as both sides use the same master key. |

## 3. The Postgres store

### 3.1 Code layout

| File | Contents |
|---|---|
| `internal/store/store.go` | The `Store` interface, plus `CreateKeyIfNone` (§4.1), `ImportConnection` and `ImportKey` (§4.5), and the optional `Invalidator` (§4.2). Both stores implement the new methods. |
| `internal/store/open.go` | `Open(ctx, dsn, dataDir)`. Empty `dsn` → `OpenSQLite(dataDir/qh.db)`; a `postgres://` or `postgresql://` URL → `OpenPostgres`; anything else is a `qerr.Invalid` error. |
| `internal/store/postgres.go` | `type Postgres struct`, with every `Store` method. |
| `internal/store/storetest/suite.go` | The shared conformance suite (§6), after `internal/engine/enginetest`. |

### 3.2 Behaviour that must match SQLite exactly

The conformance suite pins each of these.

- **Sentinel errors.**
  - `ErrExists` on a duplicate connection name in a workspace, a duplicate workspace, or a duplicate key prefix. Postgres SQLSTATE `23505`.
  - `ErrNotFound` when a connection or key names a missing workspace (SQLSTATE `23503`), and on deleting or getting something that does not exist.
  - `ErrConflict` on a stale `UpdateConnection`. The update matches `workspace_id, name, id, version`; on 0 rows it reads again and returns `ErrNotFound` if the row is gone, otherwise `ErrConflict`.
- **Fields written back on success.**
  - Create sets `Version = 1`, `CreatedAt` and `UpdatedAt`.
  - Update bumps `Version` and sets `UpdatedAt`.
  - `CreateKey` sets `CreatedAt`.
  - `AppendQueryLog` sets `ID`, and `At` when it was zero.
- **Deletes.**
  - Deleting a workspace cascades to its connections, keys and query-log entries.
  - Query-log entries have no foreign key to their connection, so they outlive it.
  - The `default` workspace exists from the first migration.
- **Ordering.**
  - `ListConnections` by name.
  - `ListWorkspaces` by id.
  - Keys by `created_at, id`.
  - `ListQueryLog` newest first by `id`. The id is a `bigserial`, so the order is the insert order across all instances.
  - `QueryLogFilter.Limit <= 0` means no limit.
- **Times.** They are returned in UTC, and round-trip to the microsecond. Postgres `timestamptz` has microsecond precision, so the suite compares with `Truncate(time.Microsecond)`.
- **Empty values.** `AllowedNamespaces` empty is returned as nil. Empty `TLS`, `SSH` and `Refs` summaries are returned as "".
- **`ResealConnections(fn)`.** Everything happens in one transaction:
  - it reads every connection with `SELECT … FOR UPDATE`;
  - `fn` returning a nil secret skips the row;
  - any error from `fn` rolls everything back;
  - the version is not bumped.
- **`Ping`, `Close`.**

### 3.3 Connection

- The pool is `database/sql` over pgx stdlib, with `MaxOpenConns` fixed at 10. The store's load is small: one lookup per tool call, plus writes.
- The store's DSN is the operator's, so the netguard policy and the tenant allow-list do not apply to it.
- TLS comes from the DSN (`sslmode`, `sslrootcert`). The password comes from the DSN, or from a file named by `QH_STORE_PASSWORD_FILE`, for Kubernetes secrets.
- `Open` retries for up to 30 s while the database is unreachable, so instances can start before their database. After that it fails with `connection_failed`.

## 4. Changes beyond the store

### 4.1 Atomic bootstrap

New method: `CreateKeyIfNone(ctx, k *APIKey) (created bool, err error)`. It creates `k` only if the store has no keys.

`auth.Bootstrap` uses it instead of `CountKeys` followed by `CreateKey`. When `QH_BOOTSTRAP_ADMIN_KEY` is set and another instance created the same key first, `created` is false and startup continues: no error, and no key printed.

### 4.2 Schema-cache invalidation

A store may implement this optional interface:

```go
// Invalidator tells every instance sharing the store that a connection's
// cached schema is stale.
type Invalidator interface {
	Invalidate(ctx context.Context, connectionID string) error
	Invalidations(ctx context.Context) (<-chan string, error) // ends when ctx ends
}
```

- **Postgres** implements it:
  - `Invalidate` runs `SELECT pg_notify('qh_catalog', id)`.
  - `Invalidations` holds one dedicated pgx connection running `LISTEN qh_catalog`, and reconnects with backoff.
  - After a reconnect it sends the empty string, meaning invalidate everything, because notices may have been missed.
- **SQLite** does not implement it.
- **`core.Service`:**
  - Whenever it invalidates the catalog cache today (non-read statements, update, delete, `RefreshSchema`, workspace delete), it also calls `Invalidate` when the store has an Invalidator. That call is best effort: errors are logged through `Options.Logger` and never fail the call.
  - A goroutine started with the service (`Service.Start(ctx)`, called by `qh serve`) applies received IDs to the local cache. The empty string clears the whole cache.

### 4.3 Store selection in the CLI

- The global flag is `--store DSN`, defaulting to `$QH_STORE_DSN`. `--server` and `--store` are exclusive: remote mode talks to a server, not a store.
- `openAppWith` calls `store.Open(ctx, dsn, home)`. The `app.st` field becomes `store.Store`. `master.key` handling in the data directory is unchanged; server mode still requires `QH_MASTER_KEY`.
- `qh masterkey rotate` checks that the store exists through the store (it has the `default` workspace), not by checking that `qh.db` exists on disk.
- In server mode `clearPGEnv` still runs. It runs before the store opens, which is why the DSN must carry everything. That rule is documented.

### 4.4 Old master keys in `qh serve`

`QH_MASTER_KEY_OLD` holds comma-separated base64 keys. They are parsed like `QH_MASTER_KEY` and passed to `secret.NewKeyring` as old keys. Instances can then open data sealed under any listed key, while sealing new data under `QH_MASTER_KEY`.

### 4.5 `qh store copy`

```
qh store copy --to postgres://… [--with-query-log]
```

- It reads the current store (`--store`, or the data directory's SQLite) and writes to `--to`, which must be empty: only the `default` workspace, and no connections or keys.
- Workspaces, connections and keys are copied unchanged. That includes `Secret`, `SecretKeyID`, `Version`, IDs and timestamps.
  - Two store methods are needed: `ImportConnection` and `ImportKey`. They insert exactly what they are given, with no defaults applied.
- The query log is copied only with `--with-query-log`. Its entries get new ids, inserted in the original order.
- It prints the counts.
- It does not decrypt anything. The same master key must be used afterwards, and the command says so.

## 5. Operating several instances

The README gains a "Running several instances" section:

- **Startup.** Every instance runs with the same `--store`, the same `QH_MASTER_KEY` and the same flags (`--allow-cidr`, `--allow-secret-ref`, …). The load balancer needs no stickiness, because MCP over HTTP is stateless.
- **Limits are per instance:**
  - the per-key concurrency limit;
  - each connection's `pool_size`, so a database sees up to instances × `pool_size` connections;
  - the total connection cap.
- **Rotating the master key, with no downtime:**
  1. Restart every instance with `QH_MASTER_KEY=<new>` and `QH_MASTER_KEY_OLD=<old>`.
  2. Run `qh masterkey rotate --store … --old-key-file old.key` once, from any host with `QH_MASTER_KEY=<new>`.
  3. Restart every instance without `QH_MASTER_KEY_OLD`.
- **Query-log times** come from each instance's clock, so keep clocks in sync (NTP). The order of entries is the store's insert order.

## 6. Testing

- **The conformance suite (`storetest.Run(t, open func(t) store.Store)`):**
  - It holds every current store test that goes through the interface, with the SQLite-only parts (the upgrade from v1, the concurrent first open on a file) left in `sqlite_test.go`.
  - It runs against SQLite (a temp file) and against Postgres, on `postgres:13-alpine` and `postgres:17-alpine` via testcontainers (Docker; skipped under `-short`).
- **Postgres-only tests:**
  - eight goroutines opening an empty database at once apply each migration once;
  - `CreateKeyIfNone` racing from eight goroutines creates one key;
  - `ResealConnections` blocks a concurrent `UpdateConnection` until it commits;
  - `Invalidations` delivers a notice sent from another `Postgres` instance, and sends "" after its listening connection is killed (`pg_terminate_backend`).
- **Multi-instance test (Docker).** Two complete `httpapi` servers built on one Postgres store, each with its own `core.Service`, pools and catalog cache:
  - a connection created through A is queried through B;
  - a key revoked on A is refused by B;
  - a `CREATE TABLE` run through A's query endpoint shows up in B's schema;
  - starting both on an empty store creates one admin key.
- **CLI tests:**
  - `--store` and `QH_STORE_DSN` select the store;
  - `--store` with `--server` is refused;
  - `qh store copy` from a populated SQLite store reproduces every row, and the copied connection can be queried with the same master key;
  - copying into a non-empty store is refused.
- **Regression.** Every existing test still passes on SQLite unchanged.

## 7. Not in this version

- A cluster-wide per-key limiter or shared pool limits.
- Other store backends (MySQL, etcd).
- Read replicas, or splitting reads from writes.
- Automatic master-key distribution.
- The spec's `meta.revision` change counter (§5.1). The code re-reads the store on every call instead, which also covers multiple instances.
