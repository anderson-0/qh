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

For MySQL/MariaDB, grant only `SELECT` (and `SHOW VIEW` for views):

```sql
CREATE USER 'qh_reader'@'%' IDENTIFIED BY 'change-me';
GRANT SELECT, SHOW VIEW ON shop.* TO 'qh_reader'@'%';
```

Don't grant the `FILE` privilege: with it, `SELECT LOAD_FILE(...)` reads files on the database server.

## Development

```bash
make test        # needs Docker for the Postgres tests
make test-short  # skips Docker tests
make build       # static binary in bin/qh
```
