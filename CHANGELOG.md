# Changelog

All notable changes to qh are listed here. Versions follow [semantic versioning](https://semver.org).

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
  - MongoDB 7 and 8, queried in mongosh syntax. qh parses a fixed subset and never runs JavaScript.
- **Connections:**
  - Connections are read-only unless created with `--mode rw`.
  - Connection strings are encrypted at rest with AES-256-GCM.
  - Changes reach an already-connected agent on its next call.

### Safety

- **Read-only connections run reads only.**
  - Each engine refuses statements that would write before they reach the database. Postgres and MySQL/MariaDB also run queries in read-only transactions, and SQLite opens the file read-only.
  - MongoDB read-only connections accept only reads. `$out` and `$merge` are detected at any depth.
- **Unknown names give suggestions.** Errors on unknown tables, columns and collections suggest close matches.
- **Server mode guards what a tenant can reach:**
  - It refuses Unix sockets, and database addresses on loopback and link-local ranges, including cloud metadata. `--allow-cidr` re-allows a range.
  - It refuses connection-string settings that would read files on the server or use its credentials.

### Server mode

- **Serving:** `qh serve --http` serves MCP over Streamable HTTP and a REST API with an OpenAPI description.
- **Access:** API keys have the scopes `query`, `manage` and `admin`, and workspaces isolate connections. Each key has a per-key concurrency limit.
- **Remote CLI:** `qh conn`, `qh query`, `qh key` and the other commands also run against a remote server.
- **Query log:** every `query` call is logged, with 30-day retention by default, and can be listed with `qh log` or the REST API.

### Connections and secrets

- **Details:** a connection takes a connection string or structured fields, plus TLS settings (CA, client certificate, server name and mode).
- **SSH tunnels:** with host-key pinning, and support for a bastion host.
- **Secret references:** `env:`, `file:` and `aws-sm://` resolve passwords and connection strings at connect time. In server mode an operator allowlist controls them.

### Running several instances

- **Shared store:** `--store postgres://…` (or `QH_STORE_DSN`) puts the store in Postgres, so several `qh serve` instances can share it behind a load balancer. Changes, key revocations and schema-cache invalidations reach every instance.
- **Moving a store:** `qh store copy` moves an existing SQLite store into Postgres.
- **Key rotation:** the master key can be rotated with no downtime using `QH_MASTER_KEY_OLD`.

### Benchmarks

- **Against dbhub** on four SQL datasets: about 40% fewer tokens per task, with equal accuracy.
- **Against mongodb-mcp-server 3.0.5** on two document datasets: 95% fewer tokens per task, with equal accuracy.

See `bench/README.md` for both.
