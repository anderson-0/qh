# Changelog

All notable changes to qh are listed here. Versions follow [semantic versioning](https://semver.org).

## [Unreleased]

### Added

- **Password-only connection edits.** `qh conn edit NAME --password-stdin` replaces or clears qh's stored inline database password for PostgreSQL, MySQL, MariaDB and MongoDB. Local and remote modes preserve the other connection settings and test before saving unless `--skip-test` is set. REST patches accept a transient `password` field; passwords stay hidden in responses.

- **SQLite cold-schema benchmark.** `-micro-engine sqlite` measures a generated 1,000-table SQLite schema and query latency on Chinook. It runs without Docker when agent runs and token counting are skipped.

- **Key revocation states what it covers.** Revoking a key deletes only that key; keys it created keep working until they are revoked or expire. `qh key rm` prints this notice on stderr after its success line, and `DELETE /api/v1/keys/{id}` sends it in a `QH-Warning` header. The 204 response is otherwise unchanged.

### Changed

- **Server mode refuses more address ranges and accepts only TCP.** `64:ff9b::/96`, `64:ff9b:1::/48` and `::/96` are denied by default; `--allow-cidr` re-allows them. Other translation prefixes need `--deny-cidr`.
- **MongoDB schema inference allocates less.** Path statistics count each document directly instead of building a presence map per document. Sampled fields, nullability, enums and references are unchanged.

### Fixed

- **qh opens SQLite files on Windows.** SQLite rejected the file URIs qh built for Windows paths (`invalid uri authority`). On Windows this broke SQLite connections and the local store, which every local command and `QH_DSN` mode use. Drive-letter and UNC paths now work. Linux and macOS are unchanged.
- **Errors hide every known connection secret.** Messages and hints from every connection step replace the connection string, its passwords, inline SSH and TLS keys and resolved `env:`, `file:` and `aws-sm://` values with `***`, at any length. A connection string qh cannot parse fails with fixed text, and `qh conn show` labels it. Very short passwords can hide common letters in a message.
- **Master-key creation can fall back when hard links are unsupported.** Linux, macOS and Windows try an exclusive rename of the complete key. Existing keys are never replaced, including during concurrent startup. Creation fails if both methods are unsupported.
- **MySQL writes stop on timeout or cancellation.** A verified control connection interrupts protected read-write statements, including stored programs and side-effecting functions. Confirmed InnoDB rollback releases their locks before returning; unconfirmed interruption/rollback adds a hint to check before retrying. This can use twice `pool_size` connections and add up to ten seconds for interruption and cleanup. Normal read-write calls also cost more; see the [measurements and proxy, advisory-lock and transaction limits](bench/results/mysql-write-timeout-2026-10-15.md).
- **MySQL/MariaDB cancellation has consistent errors.** Explicit and wrapped cancellation returns `error[query_error]: query canceled`. Server interruption `1317` keeps its message as `query_error` unless a context deadline establishes a timeout. Server timeout codes `3024` and `1969` still return `timeout`, including after late cancellation.
- **Postgres cancellation is distinguished from timeout.** Explicit cancellation returns `query_error` without timeout advice. Deadlines and recognized statement timeouts still return `timeout`; other SQLSTATE `57014` responses retain the server message as `query_error`. Unrecognized localized messages use that fallback.
- **MCP argument errors use `error[invalid]`.** Missing fields, wrong types and integer overflow return a single-line tool error with the SDK's validation detail. Existing service error categories and protocol errors are preserved.
- **Local-only commands respect QH_SERVER.** `mcp`, `serve`, `store copy` and `masterkey rotate` reject an environment-selected remote server before opening local files.
- **Schema text retains every foreign key on a column.** Overviews group multiple targets as `owner_id→{users.id,archive.users.id}`; details list them as `→ users.id, archive.users.id`. A later reference no longer hides an earlier one, including in folded document fields.
- **Connection opener panics no longer strand the pool.** Waiting requests receive a safe connection failure, reserved capacity is released, and normal retry backoff applies.

## [1.0.3] - 2026-10-15

### Added

- **Homebrew.** `brew install anderson-0/tap/qh` installs qh on macOS and Linux. Each release updates the cask in [anderson-0/homebrew-tap](https://github.com/anderson-0/homebrew-tap).

### Changed

- **A faster first schema call on Postgres.** On 1,000 tables it takes about 23 ms instead of 99 ms. The catalog queries no longer do work per row that a new connection pays for in full, such as looking up each comment, privilege and key column on its own. The output is unchanged.
- **MySQL schema discovery does less work.** The column query no longer materializes and sorts its results on MySQL; qh restores column order itself. Foreign-key links use an object map instead of searching the catalog for every key. Schema output is unchanged, including on MariaDB. The free benchmark now covers a generated 1,000-table MySQL database with `-micro-engine mysql`.

### Fixed

- **A foreign key to a partitioned Postgres table is listed once.** It was listed again for each partition of the referenced table.

## [1.0.2] - 2026-10-14

Easier to install and to run in a container.

### Added

- **One connection from the environment.** When `QH_DSN` is set, `qh mcp` serves that one connection (with optional `QH_ENGINE`, `QH_NAME`, `QH_DESCRIPTION` and `QH_MODE`) from a temporary store that is removed on exit, so `docker run -i --rm -e QH_DSN ghcr.io/anderson-0/qh:1 mcp` works with no setup. This mode is what containers and MCP catalogs need, because they can only pass environment variables.
- **The official MCP registry.** qh is listed as `io.github.anderson-0/qh`, pointing at the container image. Each release updates the listing.
- **Editor setup.** The README shows how to add qh to Claude Code, Claude Desktop, Cursor, VS Code, Windsurf, Zed, Codex CLI and Gemini CLI.

## [1.0.1] - 2026-10-13

Hardening for the shared store, SSH tunnels and MongoDB. Nothing changes for local use; in server mode some tenant SSH keys are now refused, and a wrong passphrase is found later (see SSH tunnels).

### Shared store

- **Reads survive restarts and failovers.** When the shared Postgres store restarts, fails over or drops a connection, the next read succeeds instead of failing. A read retries past every dead pooled connection, but not when a new connection cannot be made at all, so an outage does not trigger a burst of dials. Writes are never retried.
- **The listener detects silent connections.** The connection that carries schema-change notices pings the server when it is quiet. A dead link is replaced and cached schemas are dropped, so changes still reach agents.
- **Outage logs.** `qh serve` and `qh mcp` warn when the listener loses its connection or cannot reconnect, and log when it is back. A flapping link is counted rather than logged each time, so it cannot flood the log.
- **`qh mcp` writes warnings to stderr.** They are JSON lines; stdout stays the MCP protocol. Besides listener outages, this includes dropped query-log entries and failures to share schema changes, which were silently dropped before.

### SSH tunnels

- **Key parsing has a bounded cost.**
  - A private key larger than 16 KiB is refused.
  - In server mode, an encrypted OpenSSH key that asks for more than 128 bcrypt rounds is refused before it is run, an RSA key over 8192 bits is refused, and DSA keys are refused. qh adds no round or type limit for local use.
- **A key is decrypted once.** In server mode, creating or testing a connection no longer decrypts the key twice. As a result, saving a connection without a test no longer checks the passphrase: a wrong one is reported by the connection test, or when the connection is first used, as "ssh: the passphrase does not decrypt the private key".
- **`--deny-ssh` is checked first.** A refused tenant can no longer make qh parse or decrypt a key before being refused.
- **Empty references are refused.** A `private_key_ref` or `password_ref` that resolves to an empty value is an error, not a silent fall back to an empty password.
- **The host-key hint names the type.** The hint for an unknown host key says which type the bastion presented (`ed25519`, `ecdsa` or `rsa`), so the `ssh-keyscan -t` command it suggests fetches the right key.

### MongoDB

- **Unlisted databases are shown.** A database whose collections cannot be listed appears in the schema overview as `db: (not listed: <reason>)`, instead of looking empty or missing. The other databases still build.
- **A clearer message for an unchecked `db`.** In a connection limited to some databases, a `$out`, `$merge`, `$lookup` or `$unionWith` stage whose `db` is not a plain string now fails with `a $out stage's db must be a plain string` (error category `invalid`) rather than naming a database that does not exist.

### Docs

- **The `--allow-secret-ref` example uses `env:TENANT_DB_*`.** The old `QH_` example invited patterns such as `env:QH_*`, which would also match `QH_MASTER_KEY`.
- **Server mode and containers.** `--allow-cidr 127.0.0.0/8` does not reach the host's databases when qh runs in a container; the README now says to use `host.docker.internal`.

## [1.0.0] - 2026-10-13

The first release.

### A database server for AI agents

- **Two MCP tools, whatever the number of connections:**
  - `schema` gives an overview of tables with keys and foreign keys, or full definitions of matching objects with foreign keys in both directions, enum values and indexes.
  - `query` runs one statement and returns TSV. It returns at most 100 rows by default, and its footer says when rows were cut.
  - The MCP instructions name each connection, so an agent can query without looking up connections first.
- **Engines:**
  - PostgreSQL 13+;
  - MySQL 8.0.16+;
  - MariaDB 10.6+;
  - SQLite;
  - MongoDB 7 and 8, queried in mongosh syntax. qh parses a fixed subset and runs no JavaScript itself; `$where` and `$function` still run on the MongoDB server.
- **Connections:**
  - Connections are read-only unless created with `--mode rw`.
  - Connection strings are encrypted at rest with AES-256-GCM.
  - Changes reach an already-connected agent on its next call.

### Safety

- **Read-only connections are guarded twice.** Use a database user that can only read as well; see Read-only safety in the README.
  - Each engine refuses statements that would write before they reach the database. Postgres and MySQL/MariaDB also run queries in read-only transactions, and SQLite opens the file read-only.
  - MongoDB read-only connections accept only reads. `$out` and `$merge` are detected at any depth.
- **Unknown names give suggestions.** Errors on unknown tables, columns and collections suggest close matches.
- **Server mode guards what a tenant can reach:**
  - It refuses Unix sockets, and database addresses on loopback, link-local and unspecified ranges, including cloud metadata. `--allow-cidr` re-allows a range.
  - It refuses connection-string settings that would read files on the server or use its credentials.

### Server mode

- **Serving:** `qh serve --http` serves MCP over Streamable HTTP and a REST API with an OpenAPI description.
- **Access:** API keys have the scopes `query`, `manage` and `admin`, and workspaces isolate connections. Each key has a per-key concurrency limit.
- **Remote CLI:** `qh conn`, `query`, `schema`, `log`, `workspace` and `key` also run against a remote server.
- **Query log:** every `query` call is logged (unless `--no-query-log`), with 30-day retention by default, and can be listed with `qh log` or the REST API.

### Connections and secrets

- **Details:** a connection takes a connection string or structured fields, plus TLS settings (CA, client certificate, server name and mode).
- **SSH tunnels:** with host-key pinning, and support for a bastion host.
- **Secret references:** `env:`, `file:` and `aws-sm://` resolve passwords and connection strings at connect time. In server mode an operator allowlist controls them.

### Running several instances

- **Shared store:** `--store postgres://…` (or `QH_STORE_DSN`) puts the store in Postgres, so several `qh serve` instances can share it behind a load balancer. Changes, key revocations and schema-cache invalidations reach every instance.
- **Moving a store:** `qh store copy` moves an existing SQLite store into Postgres.
- **Key rotation:** the master key can be rotated with no downtime using `QH_MASTER_KEY_OLD`.

### Benchmarks

- **Against dbhub** on four SQL datasets: about 40% fewer tokens per task, with equal accuracy. These were measured before the SSH, shared-store and MongoDB work. The tool surface that SQL-only workspaces see has not changed since; a test pins it byte for byte.
- **Against mongodb-mcp-server 3.0.5** on two document datasets: 95% fewer tokens per task, with equal accuracy.

See [bench/README.md](https://github.com/anderson-0/qh/blob/main/bench/README.md) for both.
