# qh — SSH tunnels and secret references (design spec)

Status: draft for review, 2026-10-11. This extends `2026-10-09-qh-design.md`, whose §2 lists both features under "Not in v1". That spec still governs everything not covered here.

## 1. Purpose

Two common setups are out of reach today:

- **Databases behind a bastion host.** Many production databases are reachable only through an SSH jump host. dbhub supports SSH tunnels; qh does not.
- **Credentials that live elsewhere.** Teams keep database passwords in environment variables, in secret files mounted by Docker or Kubernetes, or in AWS Secrets Manager. qh can only store a copy, which goes stale when the secret rotates.

**Success criteria:**

- **Tunnels.** A connection can reach a Postgres or MySQL/MariaDB database through an SSH bastion, using key or password authentication. The bastion's host key must be pinned.
- **Secret references.** A connection's database password, its whole connection string, or its SSH credentials can be references (`env:`, `file:`, `aws-sm://`), resolved when qh connects. A rotated secret is picked up without editing the connection.
- **Server mode stays closed by default.** A tenant with a `manage` key cannot use either feature to reach a network the operator did not allow, or to read a secret the operator did not allow (§5).
- **The agent-facing surface does not change.** The MCP tools, instructions and output are untouched, so the benchmark token result is unaffected.

## 2. Decisions

| Question | Decision | Why |
|---|---|---|
| Where the features work | Both modes. Local mode is unrestricted; server mode is opt-in by the operator. | Local mode runs as the user and exposes nothing new. In server mode, tenants must not borrow the server's network or secrets. |
| Secret sources in this version | `env:`, `file:`, `aws-sm://` | Covers environment variables, mounted secret files and the one manager the spec names. A resolver interface leaves room for Vault and GCP later. |
| Bastion identity | A pinned host-key fingerprint is required in both modes. | Without it, a network attacker can impersonate the bastion and capture the database password. One rule for both modes keeps it simple. |
| SSH authentication | Private key (with optional passphrase) or password. ssh-agent is not supported. | Agent forwarding to a server process makes no sense, and local users can use a key file. |
| How references are marked | Separate `*_ref` fields, never a prefix inside a plain value | A literal password that happens to start with `env:` must never be read as a reference. |
| When references resolve | When a connection pool opens, never stored. An authentication failure drops the pool so the next call resolves again. | Rotated secrets are picked up with no edit and no restart. |

## 3. Model

### 3.1 Connection fields

`core.ConnInput`, `ConnPatch`, REST and the CLI gain:

| Field | Type | Meaning |
|---|---|---|
| `dsn_ref` | string | Reference whose value is the whole connection string. Exclusive with `dsn` and the structured fields. |
| `password_ref` | string | Reference whose value is the database password. It replaces any password in `dsn` or `password`. Not allowed for SQLite. |
| `ssh` | object or null | The tunnel. PATCH: absent means unchanged, `null` removes it, an object replaces it, following the `tls` precedent. Not allowed for SQLite. |

The `ssh` object:

| Field | Required | Meaning |
|---|---|---|
| `host` | yes | Bastion host name or IP. |
| `port` | no | Defaults to 22. |
| `user` | yes | SSH user name. |
| `host_key` | yes | The bastion's host-key fingerprint, `SHA256:<base64>` as printed by `ssh-keygen -lf`. |
| `private_key` / `private_key_ref` | one of these, or a password | PEM private key (OpenSSH or PKCS#8 format), or a reference to one. |
| `passphrase` / `passphrase_ref` | no | Passphrase for an encrypted private key. |
| `password` / `password_ref` | one of these, or a key | Password authentication. |

**Validation** (`engine.SSH.Validate`):

- `host`, `user` and a well-formed `host_key` are present.
- Exactly one of key or password authentication is given.
- A literal private key parses. An encrypted key needs a passphrase or a passphrase reference.
- Each reference parses (§4.1).
- The literal and `_ref` forms of the same field are never both set.

Error messages never include key material, passwords or resolved secret values.

### 3.2 Storage

This follows the TLS precedent.

- **Sealed envelope.** The envelope (`core/sealed.go`) gains `SSH *engine.SSH` and `Refs` (`dsn_ref`, `password_ref`). Older envelopes without these fields still decode. `qh masterkey rotate` re-seals them like everything else.
  - References are not secret themselves. They are sealed anyway so that the decrypted envelope stays the single source of truth.
- **Plain columns, summaries only.** A new migration (the store's second) adds:
  - an `ssh` column holding `SSHInfo` JSON: host, port, user, host_key, auth method (`key` or `password`), and which values are references, with the reference strings;
  - a `refs` column holding the `dsn_ref` and `password_ref` strings.
- **What users see.** `ConnInfo`, `qh conn show` and REST responses show these summaries, never key material or resolved values.

### 3.3 Engine spec

`engine.ConnSpec` changes as follows:

- **New `Server bool`.** The Postgres engine currently treats `Dial != nil` as meaning server mode. A local tunnel also sets `Dial`, so the server-mode rules move to this explicit flag:
  - the connection-string allow-list;
  - no password from the environment or files;
  - the lookup override.
  `core.connSpec` sets it from `Options` (server mode is set when `Options.Dial` is set, exactly as now).
- **New `Password string`.** When set, it overrides the connection string's password. Postgres sets `ConnConfig.Password`; MySQL sets `Config.Passwd`. This lets a `password_ref` apply to any connection string without rewriting it.
- **Unchanged `Dial`.** It is still "how to open a network connection to the database". With a tunnel it is the tunnel's dialer (§3.4).

Engines need no SSH-specific code.

### 3.4 Tunnel

The new package `internal/tunnel` contains a `Tunnel` with `Dial(ctx, network, addr) (net.Conn, error)` and `Close()`.

- **One SSH client per pool.** Database connections are multiplexed as `direct-tcpip` channels over it. The client connects lazily and reconnects once if it dropped. Keep-alives run every 30 s, and three missed replies close it.
- **Host key.** `ssh.FixedHostKey` is applied through the fingerprint. A mismatch fails with `ssh: host key mismatch for bastion:22 (got SHA256:…, expected SHA256:…)`. When the key is unknown during a connection test, the error names the fingerprint the server presented, so the user can verify it and set it.
- **Reaching the bastion.** In server mode the TCP connection to the bastion goes through the netguard policy (`Policy.DialContext`). In local mode it uses a plain dialer.
- **Reaching the database.** In server mode the target host is resolved by qh and each address is checked with `Policy.Allowed`. Loopback addresses are exempt, because through a tunnel they mean the bastion's own loopback, which is a common place for the database to run. The bastion is then asked to connect to the checked IP, never the name, so it cannot be pointed elsewhere by DNS. If qh cannot resolve the name, the error says so and suggests the IP address. In local mode the name is passed to the bastion unchanged.

`core.OpenConn` wires it up: when `spec.SSH` is set, it builds the tunnel, sets `spec.Dial = tunnel.Dial`, opens the engine, and returns a `Conn` wrapper whose `Close` closes the engine connection and then the tunnel. The pool manager needs no change. A tunnel lives exactly as long as its pool, so it is rebuilt on a version bump or an idle sweep.

## 4. Secret references

### 4.1 Syntax

| Reference | Value |
|---|---|
| `env:NAME` | The environment variable `NAME`, which must be set and non-empty. |
| `file:/abs/path` | The file's contents with one trailing newline removed. The path must be absolute and the file at most 64 KiB. |
| `aws-sm://<secret-id>` | The secret string. `<secret-id>` is a name or ARN, as AWS accepts. |
| `aws-sm://<secret-id>#<key>` | Field `<key>` of a JSON secret string, e.g. `aws-sm://prod/db#password`. |

`aws-sm` uses the AWS SDK for Go v2 (`config` and `secretsmanager` only), with the default credential chain of the qh process. The region comes from the ARN when one is given, otherwise from the default chain.

### 4.2 Resolution

The new package `internal/secretref` provides:

- `Parse(s) (Ref, error)`;
- `Resolver` with `Resolve(ctx, Ref) (string, error)`, one implementation per scheme;
- `Set`, which dispatches to a resolver by scheme and holds the allow-list (§5.2).

Resolution happens in `core.OpenConn` (and in connection tests), before the engine opens:

- `dsn_ref` produces `spec.DSN`. In server mode, the resolved string then passes the same `CheckServerDSN` as a literal one.
- `password_ref` produces `spec.Password`.
- The SSH references produce the tunnel's credentials.

The rules for resolved values:

- They live only in the pool's memory. They are never written to the store and never logged.
- The resolve cache (`core.Service.resolved`) keeps the references, not the values.
- Each resolution is bounded by 10 s.
- Errors name the reference and the reason only, e.g. `secret reference env:QH_TENANT_DB: not set`, `aws-sm://prod/db: access denied` or `…#password: no such key`. They never include a value. They classify as `connection_failed`.

**Rotation.** When opening a pool fails with an authentication error, qh drops the pool (as today) and the next call resolves again. An open pool keeps working until it is idle-swept (10 min) or its connections fail authentication. A failure there also drops the pool: in Postgres SQLSTATE class `28`, in MySQL error `1045`. A rotated secret is therefore picked up at the latest on the next call after the old one stops working.

## 5. Server mode

### 5.1 Tunnels

Tunnels are allowed, under the existing network policy (§3.4):

- The bastion must be an allowed address.
- The target must be an allowed address, apart from the loopback exemption.

Tunnels need no opt-in flag. An operator who wants no tunnels at all passes `qh serve --deny-ssh`, which refuses connections with `ssh` on create, update, test and open.

### 5.2 Secret references

References are off by default. `qh serve --allow-secret-ref PATTERN`, repeatable, allows the references matching a pattern:

- `*` matches any run of characters.
- The match is against the whole reference string as the tenant wrote it, e.g. `env:QH_TENANT_*` or `aws-sm://arn:aws:secretsmanager:us-east-1:123456789012:secret:qh/*`.
- For `file:`, the path is also cleaned and symlinks are resolved. The resolved path must match too, so `file:/run/secrets/../../etc/shadow` and symlinks out of the allowed directory are refused.

A reference that matches no pattern is refused:

- on create, update and connection test, with `error[forbidden]: secret reference env:HOME is not allowed on this server` and the hint `The operator can allow it with qh serve --allow-secret-ref`;
- again at resolution time, in case the server restarted with narrower flags.

Local mode accepts every reference.

## 6. Interfaces

### 6.1 CLI (`qh conn add` / `qh conn edit`)

| Flag | Meaning |
|---|---|
| `--dsn-ref REF`, `--password-ref REF` | Database references. |
| `--ssh-host [user@]host[:port]`, `--ssh-user USER` | Bastion. |
| `--ssh-host-key SHA256:…` | Pinned fingerprint. |
| `--ssh-key FILE` | Private key, read by the CLI process and sent as text, as with `--tls-key`. |
| `--ssh-key-ref REF` | Private key as a reference. |
| `--ssh-passphrase-stdin`, `--ssh-passphrase-ref REF` | Passphrase. |
| `--ssh-password-stdin`, `--ssh-password-ref REF` | Password authentication. |
| `--no-ssh` (edit only) | Remove the tunnel. |

Adding a connection with `--ssh-host` but no `--ssh-host-key` runs the connection test as usual. Because the key is unknown, the test fails with the presented fingerprint and the hint `Verify it (ssh-keyscan -t ed25519 bastion | ssh-keygen -lf -) and add --ssh-host-key SHA256:…`. The connection is not saved.

`qh conn show` prints the `ssh` and reference summaries.

### 6.2 REST and OpenAPI

- `ConnectionInput` and `ConnectionPatch` gain `dsn_ref`, `password_ref` and `ssh` (schema `SSH`).
- `Connection` gains `ssh` (schema `SSHInfo`) and `refs`.
- `openapi.json` and `client.Client` are updated in step.

### 6.3 Connection test

The test reports which stage failed:

- secret resolution;
- reaching the bastion;
- the bastion's host key;
- SSH authentication;
- the bastion reaching the database;
- the database itself.

Each stage has its own message.

## 7. Testing

- **Unit tests:**
  - `secretref`: parsing, env and file resolution (including the size cap, relative paths, `..` and symlink escapes), allow-list matching, and errors never containing values.
  - `aws-sm` against a fake `secretsmanager` client.
  - `engine.SSH.Validate`.
  - Envelope decoding of old and new formats.
- **Tunnel tests:**
  - An in-process SSH server built on `golang.org/x/crypto/ssh`, a test helper that accepts a key or password and forwards `direct-tcpip`.
  - The cases: host-key mismatch and unknown key; key, encrypted key and password authentication; keep-alive reconnect.
  - In server mode: a denied bastion address, a denied target, and the loopback-target exemption.
- **Integration tests (Docker):**
  - Postgres and MySQL queried through the in-process bastion. The database container is reachable only via the test helper's forwarding, which proves traffic took the tunnel.
  - A `password_ref` rotated between two calls (env changed, pool dropped by an authentication failure) is picked up.
- **CLI, REST and end-to-end tests:**
  - flags, PATCH semantics for `ssh` (absent, null, object);
  - `--allow-secret-ref` refusal and acceptance, `qh serve --deny-ssh`;
  - the fingerprint hint flow.
- **Regression tests:**
  - Moving the server-mode checks to `ConnSpec.Server` keeps every existing Postgres server-mode test green.
  - A tunnel in local mode does not trigger them.

## 8. Not in this version

- ssh-agent and `~/.ssh/known_hosts` trust.
- Jump chains (more than one bastion).
- Vault, GCP Secret Manager and Azure Key Vault resolvers.
- Per-key restrictions on which references a tenant may use. The allow-list is per server.
- Rotating an SSH host key without editing the connection.
