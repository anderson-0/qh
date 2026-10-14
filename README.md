# qh

A database server for AI agents, built to use as few tokens as possible. It exposes two MCP tools,
whatever the number of connections, and names the connections in its MCP instructions:

| Tool | What it does |
|---|---|
| `schema` | Overview of tables with their columns, keys and foreign keys, or full definitions with types, foreign keys in both directions, enum values and indexes. With no arguments in a workspace with several connections, lists the connections |
| `query` | Runs one statement and returns TSV, 100 rows by default, with a footer that says when rows were cut |

Supported now: PostgreSQL 13+, MySQL 8.0.16+, MariaDB 10.6+, SQLite and MongoDB (7 and 8).
`qh serve` adds an HTTP server mode with a REST API, and `bench/` compares qh with dbhub and with mongodb-mcp-server (see `bench/README.md`).

## Install

Download a binary for Linux, macOS or Windows (amd64 or arm64) from the
[releases page](https://github.com/anderson-0/qh/releases), unpack it and put `qh` on your `PATH`.
Each release lists SHA-256 checksums in `checksums.txt`. The binaries are not signed: on macOS, a binary
downloaded with a browser needs `xattr -d com.apple.quarantine qh` before its first run.

Or run the server image, which starts server mode on port 8765 and keeps its store in the `/data` volume.
Create the master key once and keep it: it decrypts the stored connection strings, so pass the same key every
time you recreate the container, for example to upgrade:

```bash
openssl rand -base64 32 > qh-master.key && chmod 600 qh-master.key
docker run -d --name qh -p 127.0.0.1:8765:8765 -v qh-data:/data \
  -e QH_MASTER_KEY="$(cat qh-master.key)" ghcr.io/anderson-0/qh:1
docker logs qh   # shows the admin API key once, on first start
```

- **Exposure.** API keys travel in plain HTTP, so the example listens on localhost only. Put a TLS proxy in
  front of qh before exposing it to other machines.
- **Arguments.** Arguments after the image name replace the default `serve --http :8765`.
- **Reaching host databases.** Inside the container, `localhost` is the container itself. Reach a database on
  the Docker host as `host.docker.internal`; on Linux, add `--add-host=host.docker.internal:host-gateway`.
- **Health checks.** `GET /healthz` answers `ok` without a key, for liveness and readiness probes.
- **Storage.** The image runs as uid 65532:
  - a named volume works as shown;
  - a bind-mounted directory must be writable by that uid (`chown 65532 DIR`);
  - on Kubernetes, set `securityContext.fsGroup: 65532`.

Or build it with Go:

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

### One connection from the environment

Docker's MCP catalog and the official MCP registry configure a server through environment variables only.
When `QH_DSN` is set, `qh mcp` serves exactly that one connection and ignores the connections added with
`qh conn add`:

```bash
docker run -i --rm -e QH_DSN='postgres://reader:…@db.example.com/shop' ghcr.io/anderson-0/qh:1 mcp
```

- **`QH_DSN`:** the connection string. The engine comes from its scheme (`postgres://`, `postgresql://`,
  `mysql://`, `mongodb://`, `mongodb+srv://`, `sqlite://`, `file:`) or from a path ending in `.db`, `.sqlite`
  or `.sqlite3`. Set `QH_ENGINE` (`postgres`, `mysql`, `mongodb` or `sqlite`) for any other string; it always wins.
- **`QH_NAME`:** the connection name agents pass as `source` (default `db`).
- **`QH_DESCRIPTION`:** optional text that tells agents what the database holds.
- **`QH_MODE`:** `ro` (default) or `rw`.

This mode keeps nothing on disk: the store and the query log are a temporary file removed on exit, the random
master key exists only in memory, and the data directory and `master.key` are never created. The string is
checked at startup but the database is not contacted, so an unreachable database fails per query rather than
stopping the server. `QH_DSN` cannot be combined with `--store`, `QH_STORE_DSN`, `--server` or a non-default `--workspace`.

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
169.254.169.254), and Unix sockets. Use `--allow-cidr 127.0.0.0/8` to allow a local database (this does not help
when qh runs in a container, because the host's databases are not on the container's loopback; see the
`host.docker.internal` note under Install), and `--deny-cidr` to block more (for example your private ranges). SQLite is off unless you pass `--sqlite-root DIR`; files must
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

## Running several instances

By default `qh serve` keeps its store in a SQLite file in `--data-dir`. To run several instances behind a load balancer, point them all at one Postgres database:

```bash
export QH_STORE_DSN='postgres://qh@db.internal:5432/qh?sslmode=verify-full'
export QH_STORE_PASSWORD_FILE=/run/secrets/qh-store-password   # or put the password in the URL
export QH_MASTER_KEY=…                                          # the same key on every instance
qh serve --http :8765 --allow-cidr 10.0.0.0/8
```

- **Store contents.** qh creates its tables in a `qh` schema on first start. Instances starting together apply each migration once and create one admin key; only the instance that created it prints it (set `QH_BOOTSTRAP_ADMIN_KEY` the same on every instance to choose the key yourself).
- **Changes are shared.** A connection, key or workspace changed through any instance applies to all of them on the next call. A key revoked on one is refused by all at once. A schema change made through one instance (a write or DDL statement sent to `query`, a connection change, `refresh`, or deleting a workspace) drops the cached schema everywhere. DDL run directly against the database still waits for the cache to expire.
- **No stickiness needed.** MCP over HTTP is stateless, so the load balancer needs no stickiness.
- **Limits are per instance.** That covers the per-key concurrency limit, each connection's `pool_size` (so a database sees up to instances × `pool_size` connections), and the total connection cap. Size them for the instance count.
- **Query-log times come from each instance's clock.** Keep clocks in sync.
- **The store's URL must contain everything except the password.** Server mode clears `PG*` variables at startup, so host, port, database, user and TLS settings all go in the URL; the password may come from `QH_STORE_PASSWORD_FILE` instead.
- **The master key comes from `QH_MASTER_KEY` only.** With a shared store, qh never reads or creates `master.key` in the data directory: a key file on one host could seal data under a key the other instances lack. Every command that opens the store (`serve`, `mcp`, `conn`, `query`, `masterkey rotate`, and the rest) fails without `QH_MASTER_KEY` when `--store` or `QH_STORE_DSN` is set.
- **Connections per process.** Every qh process (each `qh serve`, and each `qh mcp` too) holds up to 10 pooled store connections plus 1 for listening to changes. Size the database's `max_connections` for the number of processes × 11, and mind this when many `qh mcp` processes share the store.

**Moving an existing install:** stop it, then copy its store into an empty database. Keep the same master key (the copy moves sealed data as it is, so it does not need the key itself). The target's password goes in the `--to` URL or in `QH_STORE_PASSWORD_FILE`. The copy reads the store selected by `--store` or `QH_STORE_DSN`, so leave `QH_STORE_DSN` unset for this command and give the new database with `--to`:

```bash
qh store copy --home /var/lib/qh --to 'postgres://qh@db.internal:5432/qh?sslmode=verify-full' --with-query-log
```

**Rotating the master key without downtime:**

1. Restart every instance with `QH_MASTER_KEY=<new>` and `QH_MASTER_KEY_OLD=<old>`. Each can open connections sealed with either key. Wait until every instance has restarted before changing any connection (`conn add`, `conn edit`, and the like), because a connection sealed under the new key cannot be opened by an instance that still runs with the old key only.
2. From any host, with `QH_MASTER_KEY=<new>` and the store password (in the URL or `QH_STORE_PASSWORD_FILE`), run `qh masterkey rotate --store "$QH_STORE_DSN" --old-key-file old.key`, where `old.key` holds the old key. (Instances running with both keys may stay up; the single-instance advice to stop `qh serve` first does not apply.)
3. Restart every instance without `QH_MASTER_KEY_OLD`.

Commands run from an admin host between these steps read `QH_MASTER_KEY_OLD` too, so they can open connections sealed with either key.

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

## SSH tunnels and secret references

Reach a database through a bastion host:

```bash
qh conn add prod --engine postgres --dsn 'postgres://app@db.internal:5432/app' --password-ref env:PROD_DB_PASSWORD \
  --ssh-host jump@bastion.example.com --ssh-key ~/.ssh/id_ed25519
```

The first run fails and shows the bastion's host-key fingerprint. Verify it (`ssh-keyscan -t ed25519 bastion.example.com | ssh-keygen -lf -`) and add `--ssh-host-key SHA256:…`. qh prefers the bastion's Ed25519 host key, as OpenSSH does, and refuses any other key. The key file is read by the CLI and stored sealed, like `--tls-key`. Give the SSH user with `--ssh-user` or as `user@` in `--ssh-host`. For password authentication use `--ssh-password-stdin`; for an encrypted key use `--ssh-passphrase-stdin`.

Instead of storing a password or connection string, a connection can reference one:

| Reference | Value |
|---|---|
| `env:NAME` | an environment variable of the qh process |
| `file:/path` | a file's contents (at most 64 KiB, trailing newline removed), e.g. a Docker or Kubernetes secret |
| `aws-sm://<name or ARN>[#key]` | an AWS Secrets Manager secret, or one field of a JSON secret, read with the qh process's AWS credentials |

Use them with `--dsn-ref`, `--password-ref`, `--ssh-key-ref`, `--ssh-passphrase-ref` or `--ssh-password-ref`. References are resolved when qh connects and never stored. After a rotation, an open connection keeps working until it fails to authenticate. That call returns `connection_failed`, and the next call reconnects with the new value.

**Server mode.** A tenant must not be able to read the server's environment or cloud secrets, or tunnel into its network:

- References are refused unless the operator allows them: `qh serve --http :8765 --allow-secret-ref 'env:TENANT_DB_*' --allow-secret-ref 'aws-sm://arn:aws:secretsmanager:us-east-1:123456789012:secret:qh/*'`.
- An allowed reference can be used by every tenant with a manage key on that server, and a tenant can read its value (for example by pointing a connection at a server it controls, which then receives the password). Allow only secrets you would hand to all tenants. Tenants who must not share secrets need separate servers.
- For `file:` patterns, a mounted secret that is a symlink (as in Kubernetes secret volumes) must be allowed with a directory pattern such as `file:/var/run/secrets/db/*`, because the real path behind the symlink must also match.
- Both the bastion and the database behind it are checked against the server's network policy (`--allow-cidr`/`--deny-cidr`). By default only loopback, unspecified and link-local addresses (including the cloud metadata addresses) are refused, so private ranges such as `10.0.0.0/8` stay reachable; add `--deny-cidr 10.0.0.0/8 …` to keep tenants out of them. A database on the bastion's own loopback is allowed.
- A tenant's encrypted SSH private key may use at most 128 KDF rounds (`ssh-keygen -a`; the default is 16). Re-encrypt one with more using `ssh-keygen -p -a 100 -f KEY`. RSA keys may be at most 8192 bits, and DSA keys are refused. The key is decrypted when qh connects, so a wrong passphrase is reported by the connection test, not when the connection is saved without one.
- `--deny-ssh` turns tunnels off.

## Query log

qh logs every `query` call: connection, API key, statement (up to 8 KiB), rows, duration and error category.

- Show it with `qh log [--connection NAME] [--since 24h] [--limit N]`, or `GET /api/v1/query-log` (scope `manage`).
- `qh serve` and `qh mcp` keep 30 days (`--query-log-retention 7d`), or log nothing with `--no-query-log`.
- The log stores statements as sent. If statements carry sensitive literals, turn it off or shorten the retention.

## Rotating the master key

With the default SQLite store, stop `qh serve` first. With a shared Postgres store, do not stop the instances: follow the steps under [Running several instances](#running-several-instances) instead.

```bash
printf '%s\n' "$OLD_QH_MASTER_KEY" > old.key     # the previous key
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
- Prefer `QH_KEY` to `--key`: command-line arguments are visible to other users of the machine and saved in shell history.
- `mcp`, `serve` and `masterkey` always run locally.

## MongoDB

```bash
qh conn add events --engine mongodb --dsn -    # paste mongodb://user:pw@host:27017/events
qh query events 'db.orders.find({status: "paid"}).sort({total: -1}).limit(5)'
```

Agents query MongoDB through the same `schema` and `query` tools, in mongosh syntax. qh parses a fixed subset and never runs JavaScript:

- **Reads:** `find`, `findOne`, `aggregate`, `countDocuments`, `estimatedDocumentCount`, `distinct`, `getIndexes` and `db.getCollectionNames()`. `find` takes `.sort`, `.limit`, `.skip`, `.project`, `.hint`, `.collation`, `.count()` and `.explain()`.
- **Writes and admin calls:** these need a read-write connection. A write that is only partly applied reports how much was written.
  - Document writes: `insertOne/Many`, `updateOne/Many`, `replaceOne`, `deleteOne/Many` and `findOneAnd*`.
  - Aggregates with `$out`/`$merge`.
  - Admin calls: `createIndex(es)`, `dropIndex(es)`, `drop`, `renameCollection` and `db.createCollection`.
- **Literals:** relaxed JSON plus `ObjectId()`, `ISODate()`, `new Date("…")`, `NumberLong()`, `NumberInt()`, `NumberDecimal()`, `UUID()`, `Timestamp()`, `MinKey()`, `MaxKey()` and `/regex/flags`.
- **Results:** TSV, with `_id` first. Nested documents and arrays are compact JSON, and a missing field is `NULL`.
- **Schema:** sampled from up to 100 documents per collection.
  - Fields show as dotted paths with their types, and the overview folds nested fields as `address{city,country,…}`.
  - String fields list the values seen, ending in `…`, only when at least 50 sampled documents have the field, with at most 8 distinct values, none longer than 40 characters.
  - Fields named `<x>_id`, `<x>Id`, `<x>_key` or `<x>_ref`, such as `customer_id`, show as references (`→customers`). The target is the collection named `<x>` or `<x>s`, or else the one collection whose name ends in `_<x>` or `_<x>s`. The field's type must match the target's `_id` type, and at least 4 of 5 sampled values must exist in the target (all of them, if fewer than 5 were sampled).
- **Databases:** the database in the connection string is the default, and the only one qh introspects. If it names none, every non-system database is listed and `db.<collection>` uses `test`, as mongosh does. Without `--namespaces` (`allowed_namespaces` in the REST API), `db.getSiblingDB("x")` still reaches other databases, and the server's own authorisation decides what is allowed. With it, a query is refused with `forbidden` if it names another database, either through `getSiblingDB` or in a `$out`, `$merge`, `$lookup` or `$unionWith` stage.
- **Server mode:** connection strings may only use an allowlist of options (names are matched the way the driver matches them): authentication and replica-set settings, `tls`/`ssl`, timeouts, pool sizes, read and write concerns, compression and similar. Options that read files on the server, such as `tlsCAFile`, `tlsCertificateKeyFile`, `tlsCertificateKeyFilePassword` and their `ssl*` aliases, are refused, and so are `tlsInsecure`, `tlsAllowInvalidCertificates` and `tlsAllowInvalidHostnames`. Use qh's TLS settings instead. `authMechanism` may be `SCRAM-SHA-1`, `SCRAM-SHA-256`, `PLAIN` or `MONGODB-X509`; the others (`MONGODB-AWS`, `GSSAPI`, `MONGODB-OIDC`) take the qh host's own credentials. `authMechanismProperties` is refused. The driver's OCSP checks go through the same address guard as database connections.
- **Server-side JavaScript:** qh does not block `$where` and `$function`; they run as part of reads. On MongoDB 7 and 8 they have no database handle, so they cannot reach other databases. A MongoDB role cannot forbid them for a user who may `find` or `aggregate`; only disabling server-side scripting on the server (`security.javascriptEnabled: false`) does.

Use a MongoDB user with the `read` role for read-only connections; qh checks every call, and the role is a second line of defence.

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
make test        # needs Docker for the Postgres, MySQL/MariaDB and MongoDB tests
make test-short  # skips Docker tests
make build       # static binary in bin/qh
```

## License

MIT. See [LICENSE](LICENSE).
