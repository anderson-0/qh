# qh

A database server for AI agents, built to use as few tokens as possible. It exposes three MCP tools,
whatever the number of connections:

| Tool | What it does |
|---|---|
| `sources` | Lists connections: name, engine, read-only or read-write, description |
| `schema` | Overview with row estimates, or compact table definitions with keys, foreign keys in both directions, enum values and indexes |
| `query` | Runs one statement and returns TSV, 100 rows by default, with a footer that says when rows were cut |

Supported now: PostgreSQL 13+, MySQL 8.0.16+, MariaDB 10.6+ and SQLite. An HTTP server mode with a REST API, and a benchmark
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

```bash
qh conn add app --engine mysql --dsn - --description "Product catalogue"
```

Connection strings are encrypted (AES-256-GCM) in `~/.config/qh/qh.db`; the key is `~/.config/qh/master.key`
(or `$QH_MASTER_KEY`). `$XDG_CONFIG_HOME/qh` is used instead of `~/.config/qh` when `$XDG_CONFIG_HOME` is set;
set `$QH_HOME` to use another directory.

## Use it from Claude Code

```bash
claude mcp add qh -- qh mcp
```

Connections you add or edit with `qh conn` while the server runs are used on the agent's next call.

## Server mode

Run qh for a team or as the backend of a product:

```bash
export QH_MASTER_KEY=$(openssl rand -base64 32)   # keep it: it decrypts stored connection strings
qh serve --http :8080 --data-dir /var/lib/qh
```

On first start qh prints one admin API key (`admin API key (shown once): qh_…`), or uses `QH_BOOTSTRAP_ADMIN_KEY`
if you set it; that must be a `qh_` key whose secret has at least 32 characters (for example one printed by
`qh key create --scopes admin`). Every request needs `Authorization: Bearer <key>`:

- **Workspaces** isolate connections. Admin keys manage workspaces and keys and pick a workspace with the
  `X-QH-Workspace` header.
- **Scopes:** `query` (MCP tools, schema, query), `manage` (create, change and delete connections; create
  query/manage keys for the same workspace, which expire no later than the key that creates them), `admin`
  (everything).
- **MCP** is served at `/mcp` (Streamable HTTP). The REST API is under `/api/v1`; see `/api/v1/openapi.json`.
- **`qh key` and `qh workspace`** act on the local store at `--home`, or on a server with `--server`/`--key` (see Remote CLI).

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
then live under `DIR/<workspace>/`, so each workspace sees only its own directory. Each API key may run 4
queries at once; an extra request waits up to 10 s for a free slot, then gets HTTP 429.

Server mode ignores `PG*` environment variables (it unsets them at startup and names them on stderr), and a
Postgres connection string may only use these settings: `host`, `port`, `dbname`, `user`, `password`,
`sslmode`, `sslsni`, `sslnegotiation`, `connect_timeout`, `target_session_attrs`, `application_name`,
`channel_binding`, `require_auth`, `min_protocol_version` and `max_protocol_version`. Settings that read
files on the server (`passfile`, `service`, `sslcert`, `sslrootcert`, …) are refused, and `~/.pgpass` and
`~/.postgresql/` are not used.

Without `--http`, `qh serve` runs in local mode on `127.0.0.1:8765` with no keys, for tools on your own machine.
Local mode is unauthenticated: every process on the machine can use it, with all your connections. It only
accepts requests addressed to localhost and refuses cross-origin browser requests.

## Connection details and TLS

Instead of a connection string you can give the parts, which qh assembles and escapes:

```bash
printf '%s\n' "$PASSWORD" | qh conn add shop --engine postgres --host db.internal --database shop \
  --user reader --password-stdin --param application_name=qh
```

Over REST, send `host`, `port`, `database`, `username`, `password` and `params` instead of `dsn`.

TLS settings replace whatever the connection string says about TLS:

```bash
qh conn add shop --engine postgres --dsn - --tls-mode verify-full --tls-ca ca.pem --tls-cert client.pem --tls-key client.key
```

- Modes:
  - `disable`;
  - `require`, which encrypts without checking the certificate;
  - `verify-ca`, which checks the certificate chain;
  - `verify-full`, which checks the chain and the host name, or `--tls-server-name`.
- The private key is sealed with the connection string, and it is never shown again.
- In server mode this is the only way to use client certificates, because connection-string settings that read
  server files are refused.
- `qh conn edit NAME --no-tls` removes the settings.

## Query log

qh logs every `query` call: connection, API key, statement (up to 8 KiB), rows, duration and error category.

- Show it with `qh log [--connection NAME] [--since 24h] [--limit N]`, or `GET /api/v1/query-log` (scope `manage`).
- `qh serve` and `qh mcp` keep 30 days (`--query-log-retention 7d`), or log nothing with `--no-query-log`.
- The log stores statements as sent. If statements carry sensitive literals, turn it off or shorten the retention.

## Rotating the master key

```bash
qh serve ...                                   # stop the server first
export QH_MASTER_KEY=$(openssl rand -base64 32) # the new key
qh masterkey rotate --old-key-file old.key --home /var/lib/qh
```

In local mode, move `master.key` aside (e.g. to `old.key`) and run `qh masterkey rotate --old-key-file old.key --generate`;
`--generate` creates the new `master.key`. Without it, rotate refuses to run when no current key exists, so a missing
`QH_MASTER_KEY` can never silently re-seal everything with a key you did not choose.

Every stored connection is re-sealed with the new key in one transaction. If one cannot be opened with the old
key, nothing changes. Restart the server with the new key. The old key is no longer needed.

## Remote CLI

Every `conn`, `query`, `schema`, `log`, `workspace` and `key` command can run against a server:

```bash
export QH_SERVER=https://qh.example.com QH_KEY=qh_…
qh conn ls
qh query shop "SELECT count(*) FROM orders"
qh --key "$ADMIN" --workspace team key create --scopes query,manage   # admin keys name a workspace
```

- `--workspace` (or `QH_WORKSPACE`) also picks the workspace for local commands.
- The CLI refuses to send a key over plain `http://` to another machine.
- `mcp`, `serve` and `masterkey` always run locally.

## Read-only safety

On read-only connections every query runs in a read-only transaction (Postgres, MySQL/MariaDB) or on a file opened
read-only (SQLite), and qh also rejects statements that would write before they reach the database. Read-only
transactions cannot stop everything. In Postgres, an extension such as `dblink` or `postgres_fdw` can open a
second connection that is not read-only. In MySQL/MariaDB, a read-only transaction does not limit server-side
file access, so don't grant `FILE` (`LOAD_FILE`, `SELECT ... INTO OUTFILE`). Give qh a database user that can
only read:

```sql
CREATE ROLE qh_reader LOGIN PASSWORD 'change-me';
GRANT CONNECT ON DATABASE shop TO qh_reader;
GRANT USAGE ON SCHEMA public TO qh_reader;
GRANT SELECT ON ALL TABLES IN SCHEMA public TO qh_reader;
ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT SELECT ON TABLES TO qh_reader;
```

For MySQL/MariaDB, grant only `SELECT` (and `SHOW VIEW` for views):

```sql
CREATE USER 'qh_reader'@'%' IDENTIFIED BY 'change-me';
GRANT SELECT, SHOW VIEW ON shop.* TO 'qh_reader'@'%';
```

Don't grant the `FILE` privilege: with it, `SELECT LOAD_FILE(...)` reads files on the database server.

## Development

```bash
make test        # needs Docker for the Postgres and MySQL/MariaDB tests
make test-short  # skips Docker tests
make build       # static binary in bin/qh
```
