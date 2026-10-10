# qh Plan 4: TLS, connection fields, query log, key rotation, remote CLI — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Finish the v1 operational surface. This plan adds:
- TLS settings per connection, including client certificates in server mode;
- connection details given as separate fields instead of a connection string;
- a query log with retention;
- `qh masterkey rotate`;
- a CLI that can drive a remote qh server through the REST API.

**Architecture:**
- **Engine:** gains a TLS model (`engine.TLS`) that each network engine turns into a `crypto/tls` config. It also gains an optional `DSNBuilder`, which builds a connection string from fields.
- **Core:**
  - seals the TLS settings together with the connection string, in a JSON envelope; old raw-string blobs still open;
  - logs every `query` call through the store;
  - re-seals all connections when the master key rotates.
- **REST API:** gains the new fields and `GET /api/v1/query-log`.
- **CLI:**
  - talks to a `backend` interface, implemented locally by the core service and remotely by a new `internal/client` REST client;
  - selects remote mode with `--server`/`--key`.

**Tech Stack:** Go 1.27 standard library (`crypto/tls`, `crypto/x509`, `net/http`). The existing pgx v5.11.0, go-sql-driver v1.10.1, modernc SQLite, cobra and testcontainers. No new dependencies.

**Spec:** `docs/superpowers/specs/2026-10-09-qh-design.md`. This plan covers:
- §5.1: the `query_log` table, `--query-log-retention` and `--no-query-log`;
- §5.2: `qh masterkey rotate`;
- §7: the `host`/`port`/`database`/`username`/`password`/`params` fields, `tls`, and `GET /query-log`;
- §8: `--server`/`--key` and `QH_SERVER`/`QH_KEY`, plus `qh masterkey rotate --old-key-file`.

Earlier plans, all merged:
- `docs/superpowers/plans/2026-10-09-qh-plan-1-local-mvp.md`
- `docs/superpowers/plans/2026-10-10-qh-plan-2-mysql.md`
- `docs/superpowers/plans/2026-10-10-qh-plan-3-server.md`

The deferred findings from Plan 3 are in `docs/superpowers/plans/2026-10-10-qh-plan-3-deferred-findings.md`. Task 1 fixes the parked bracketed-IPv6 Postgres URL item.

## Plan series

| Plan | Delivers |
|---|---|
| 1, 2, 3 | Done: local MVP, MySQL/MariaDB, server mode |
| **4 (this plan)** | Per-connection TLS, structured connection fields, query log, `qh masterkey rotate`, remote CLI, the parked Postgres URL fix |
| 5 | Benchmark against dbhub (§9.2) |

## Spec adjustments made while planning

1. **No `meta.revision` table.** The spec planned it so a running `qh mcp` would notice CLI changes. Every tool call already reads its connection row from the store, and pools are keyed by the row's `version`. A change made by another process therefore applies on the next call.
2. **`query_log` columns:**
   - `row_count` replaces `rows`, which is an SQL keyword.
   - `connection_name` is added, so the log still names connections that were deleted.
   - `at` is stored as Unix milliseconds, so it sorts and compares as a number.
   - Only `query` tool calls are logged. `tool` is `query` or `explain`.
   - Statement text is cut to 8 KiB.
   - A failed log write never fails the query.
3. **Query-log flags:** `--query-log-retention` (default `30d`) and `--no-query-log` belong to `qh serve` and `qh mcp`. Both prune at start and then hourly. One-shot CLI commands log too, but never prune.
4. **`qh log`** is a new command that reads the query log, locally or remotely. The spec's CLI list did not include one.
5. **Structured fields:** `host`, `port`, `database`, `username`, `password` and `params` are accepted by `POST /connections` and `POST /connections/test`, as an alternative to `dsn`. `PATCH` changes connection details only through `dsn` and `tls`.
6. **The response field `user` is renamed `username`**, matching the spec and the new input field. This is safe because the API has not been released.
7. **TLS:**
   - Shape: `tls` is an object `{mode, ca, cert, key, server_name}`.
     - `mode` is one of `disable`, `require`, `verify-ca`, `verify-full`.
     - `ca`, `cert` and `key` are PEM text.
   - When set, it replaces any TLS settings in the connection string.
   - Storage: it is sealed together with the connection string, because `key` is secret.
   - Display: the `tls` column and all responses carry only a summary, `{mode, ca: bool, client_cert: bool, server_name}`.
   - Engines: SQLite rejects it.
   - Server mode: this is how client certificates are supplied, now that connection-string file settings are refused.
8. **`qh masterkey rotate --old-key-file F`:**
   - The current key, from `QH_MASTER_KEY` or `master.key`, becomes the primary. F holds the previous key.
   - Every connection is re-sealed in one store transaction.
   - The connection `version` is not changed.
   - The server must be stopped during rotation, and the README says so.
9. **Remote mode:**
   - `--server URL --key KEY` (or `QH_SERVER`/`QH_KEY`) works for `conn`, `query`, `schema`, `log`, `workspace` and `key`. `mcp`, `serve` and `masterkey` refuse it.
   - `--workspace` (`QH_WORKSPACE`) becomes a global flag, replacing `key create --workspace`. Remote mode sends it as `X-QH-Workspace` only when it was given explicitly.
   - The CLI refuses `http://` URLs for hosts other than loopback, so a key is never sent in clear text.

## Global Constraints

The Global Constraints of Plans 1–3 still apply:
- agent and user errors are one-line `error[<category>]: …`;
- no secrets in output, logs or errors (this now includes TLS private keys);
- pure Go;
- Docker tests skip under `-short`;
- server-mode network policy and SQLite confinement.

Additionally:

- **TLS modes:**
  - `disable`: plain connection.
  - `require`: encrypted, but the certificate is not checked.
  - `verify-ca`: the certificate chain must verify against `ca`, or against the system roots when `ca` is empty. The name is not checked.
  - `verify-full`: `verify-ca`, plus the certificate must match the host, or `server_name` when given.
  - Minimum TLS version 1.2. `cert` and `key` must be given together.
- **Sealed secret format:**
  - New and changed connections seal the JSON `{"v":2,"dsn":"…","tls":{…}}`.
  - A plaintext that does not start with `{"v":2,` is a pre-Plan-4 raw connection string, and opens as such.
- **Query log:**
  - Retention defaults to 30 days.
  - `GET /api/v1/query-log` requires the `manage` scope, is scoped to the workspace, returns entries newest first, and returns at most 1,000 per request (default 100).
- **Remote CLI:**
  - Problem responses become `error[<type>]: <title>. <detail>`.
  - A transport failure is `error[connection_failed]: cannot reach qh server at <url>`. It never contains the key.

## Review Focus

1. **A connection saved before Plan 4** has a raw connection-string blob and an empty `tls` column. It must keep working, and `masterkey rotate` must re-seal it. → Task 4 `TestLegacySecretStillOpens`; Task 6 `TestRotateMasterKey` includes a legacy row.
2. **A password given as a separate field with URL-special characters** (`p@ss/w:rd%?#`) must reach the database unchanged. → Task 2 `TestBuildDSNRoundTrip`.
3. **A TLS private key must never appear** in an API response, a CLI listing or an error. → Task 7 `TestTLSKeyNeverReturned`; Task 1 `TestTLSValidate` checks that errors carry no key text.
4. **A remote CLI pointed at `http://` on a non-loopback host** must refuse before sending the key. → Task 9 `TestRemoteRefusesPlainHTTP`.
5. **When the query log cannot be written**, queries must still succeed. → Task 5 `TestQueryLogFailureDoesNotFailQuery`.

---

## File map

```
internal/testutil/tlscert.go              throwaway CA, server and client certificates, TLS test server
internal/engine/tls.go                    TLS model: Validate, Config, Info
internal/engine/engine.go                 ConnSpec.TLS, DSNFields, DSNBuilder
internal/engine/postgres/postgres.go      apply TLS; BuildDSN; refuse '?' inside a bracketed host (server mode)
internal/engine/mysql/conn.go, mysql.go   apply TLS; BuildDSN
internal/engine/sqlite/sqlite.go          refuse TLS; BuildDSN (database = file path)
internal/store/store.go, sqlite.go        Connection.TLS, migration 3 (query_log), query-log methods, ResealConnections
internal/core/sealed.go                   sealed secret envelope
internal/core/connections.go              fields → DSN, TLS on create/update/test, TLS summary
internal/core/service.go                  TLS in specs; query logging; WithKeyID
internal/core/querylog.go                 QueryLog, PruneQueryLog, entry types
internal/core/rotate.go                   RotateMasterKey
internal/httpapi/connections.go           fields and tls in bodies; username; tls summary
internal/httpapi/querylog.go              GET /api/v1/query-log
internal/httpapi/api.go                   key id into the request context; route
internal/httpapi/openapi.json             new fields, schemas and path
internal/cli/backend.go                   backend interface + local implementation
internal/cli/conn.go, query.go, admin.go  use the backend; field and TLS flags
internal/cli/log.go                       qh log
internal/cli/masterkey.go                 qh masterkey rotate
internal/cli/serve.go, query.go (mcp)     --query-log-retention, --no-query-log, pruning loop
internal/client/client.go                 REST client implementing the backend
cmd/qh/serve_e2e_test.go                  remote CLI against the built binary
README.md                                 TLS, fields, query log, rotation, remote CLI
```

---

### Task 1: TLS settings in the engines, and the bracketed-host fix

**Files:**
- Create: `internal/testutil/tlscert.go`, `internal/engine/tls.go`
- Modify: `internal/engine/engine.go` (`ConnSpec`), `internal/engine/postgres/postgres.go`, `internal/engine/mysql/conn.go` (`Open`), `internal/engine/sqlite/sqlite.go` (`Open`)
- Test: `internal/engine/tls_test.go`, `internal/engine/postgres/tls_test.go`, `internal/engine/mysql/tls_test.go`, `internal/engine/sqlite/tls_test.go`

**Interfaces:**
- Consumes: existing `ConnSpec`, the Postgres `poolConfig`, `checkServerDSN`, the MySQL `parseConfig`, SQLite `Open`, `qerr`.
- Produces:
  - `type engine.TLS struct { Mode, CA, Cert, Key, ServerName string }`. The JSON tags are `mode`, `ca`, `cert`, `key` and `server_name`.
  - The constants `engine.TLSDisable`, `engine.TLSRequire`, `engine.TLSVerifyCA` and `engine.TLSVerifyFull`.
  - `(*TLS).Validate() error`.
  - `(*TLS).Config(host string) (*tls.Config, error)`, which returns nil for `disable`.
  - `(*TLS).Info() *TLSInfo`.
  - `type engine.TLSInfo struct { Mode string; CA, ClientCert bool; ServerName string }`. The JSON tags are `mode`, `ca`, `client_cert` and `server_name`.
  - `ConnSpec.TLS *engine.TLS`.
  - `testutil.NewTLSFixture(t) *TLSFixture`, with fields `CA`, `ServerCert`, `ServerKey`, `ClientCert` and `ClientKey` (all PEM).
  - `(*TLSFixture).TLSServer(t) (addr string, clientCerts <-chan int)`.

- [ ] **Step 1: Write the test helpers and failing tests**

`internal/testutil/tlscert.go`:
```go
package testutil

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"testing"
	"time"
)

// TLSFixture is a throwaway CA, a server certificate for localhost and
// 127.0.0.1, and a client certificate, all PEM-encoded.
type TLSFixture struct {
	CA         string
	ServerCert string
	ServerKey  string
	ClientCert string
	ClientKey  string
}

func pemOf(typ string, der []byte) string {
	return string(pem.EncodeToMemory(&pem.Block{Type: typ, Bytes: der}))
}

// NewTLSFixture generates a fresh fixture.
func NewTLSFixture(t testing.TB) *TLSFixture {
	t.Helper()
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	caTmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "qh test CA"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTmpl, caTmpl, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	caCert, err := x509.ParseCertificate(caDER)
	if err != nil {
		t.Fatal(err)
	}
	leaf := func(serial int64, cn string, usage x509.ExtKeyUsage, dns []string, ips []net.IP) (string, string) {
		key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		tmpl := &x509.Certificate{
			SerialNumber: big.NewInt(serial), Subject: pkix.Name{CommonName: cn},
			NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour),
			KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{usage},
			DNSNames: dns, IPAddresses: ips,
		}
		der, err := x509.CreateCertificate(rand.Reader, tmpl, caCert, &key.PublicKey, caKey)
		if err != nil {
			t.Fatal(err)
		}
		kb, err := x509.MarshalECPrivateKey(key)
		if err != nil {
			t.Fatal(err)
		}
		return pemOf("CERTIFICATE", der), pemOf("EC PRIVATE KEY", kb)
	}
	f := &TLSFixture{CA: pemOf("CERTIFICATE", caDER)}
	f.ServerCert, f.ServerKey = leaf(2, "localhost", x509.ExtKeyUsageServerAuth, []string{"localhost"}, []net.IP{net.ParseIP("127.0.0.1")})
	f.ClientCert, f.ClientKey = leaf(3, "qh-client", x509.ExtKeyUsageClientAuth, nil, nil)
	return f
}

// TLSServer listens on 127.0.0.1 with the fixture's server certificate. For
// each connection whose handshake succeeds it sends the number of client
// certificates the client presented, then closes the connection.
func (f *TLSFixture) TLSServer(t testing.TB) (addr string, clientCerts <-chan int) {
	t.Helper()
	pair, err := tls.X509KeyPair([]byte(f.ServerCert), []byte(f.ServerKey))
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM([]byte(f.CA))
	ln, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{
		Certificates: []tls.Certificate{pair}, ClientAuth: tls.VerifyClientCertIfGiven, ClientCAs: pool,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	ch := make(chan int, 16)
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			tc := c.(*tls.Conn)
			if tc.Handshake() == nil {
				ch <- len(tc.ConnectionState().PeerCertificates)
			}
			c.Close()
		}
	}()
	return ln.Addr().String(), ch
}
```

`internal/engine/tls_test.go`:
```go
package engine

import (
	"crypto/tls"
	"strings"
	"testing"

	"github.com/anderson-0/qh/internal/testutil"
)

func TestTLSValidate(t *testing.T) {
	f := testutil.NewTLSFixture(t)
	bad := []struct {
		tls  TLS
		want string
	}{
		{TLS{}, "tls.mode is required"},
		{TLS{Mode: "prefer"}, `unknown tls.mode "prefer"`},
		{TLS{Mode: TLSDisable, CA: f.CA}, "tls.mode disable takes no other tls settings"},
		{TLS{Mode: TLSRequire, Cert: f.ClientCert}, "tls.cert and tls.key must be given together"},
		{TLS{Mode: TLSVerifyFull, CA: "not pem"}, "tls.ca contains no PEM certificate"},
		{TLS{Mode: TLSRequire, Cert: f.ClientCert, Key: f.ServerKey}, "tls.cert and tls.key are not a valid PEM certificate and key pair"},
	}
	for _, c := range bad {
		err := c.tls.Validate()
		if err == nil || !strings.HasPrefix(err.Error(), "error[invalid]: "+c.want) {
			t.Errorf("%q: got %v, want %q", c.tls.Mode, err, c.want)
		}
		if err != nil && strings.Contains(err.Error(), "PRIVATE KEY") {
			t.Errorf("error leaks key material: %v", err)
		}
	}
	good := []TLS{
		{Mode: TLSDisable},
		{Mode: TLSRequire},
		{Mode: TLSVerifyCA, CA: f.CA},
		{Mode: TLSVerifyFull, CA: f.CA, Cert: f.ClientCert, Key: f.ClientKey, ServerName: "db.internal"},
	}
	for _, g := range good {
		if err := g.Validate(); err != nil {
			t.Errorf("%s: %v", g.Mode, err)
		}
	}
}

func TestTLSInfo(t *testing.T) {
	f := testutil.NewTLSFixture(t)
	got := (&TLS{Mode: TLSVerifyFull, CA: f.CA, Cert: f.ClientCert, Key: f.ClientKey, ServerName: "db"}).Info()
	if *got != (TLSInfo{Mode: TLSVerifyFull, CA: true, ClientCert: true, ServerName: "db"}) {
		t.Fatalf("%+v", got)
	}
	if (*TLS)(nil).Info() != nil {
		t.Fatal("nil TLS has info")
	}
}

func dialTLS(addr string, cfg *tls.Config) error {
	c, err := tls.Dial("tcp", addr, cfg)
	if err != nil {
		return err
	}
	return c.Close()
}

func TestTLSConfigModes(t *testing.T) {
	f, other := testutil.NewTLSFixture(t), testutil.NewTLSFixture(t)
	addr, certs := f.TLSServer(t)
	cfg := func(tl TLS, host string) *tls.Config {
		t.Helper()
		c, err := tl.Config(host)
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	ok := func(name string, c *tls.Config) int {
		t.Helper()
		if err := dialTLS(addr, c); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		return <-certs
	}
	fails := func(name string, c *tls.Config) {
		t.Helper()
		if dialTLS(addr, c) == nil {
			t.Fatalf("%s: connected", name)
		}
	}
	ok("require ignores the certificate", cfg(TLS{Mode: TLSRequire}, "anything.example"))
	ok("verify-full with the right CA and name", cfg(TLS{Mode: TLSVerifyFull, CA: f.CA}, "localhost"))
	fails("verify-full with a wrong name", cfg(TLS{Mode: TLSVerifyFull, CA: f.CA}, "db.example.com"))
	fails("verify-full with a wrong CA", cfg(TLS{Mode: TLSVerifyFull, CA: other.CA}, "localhost"))
	ok("server_name overrides the host", cfg(TLS{Mode: TLSVerifyFull, CA: f.CA, ServerName: "localhost"}, "10.0.0.1"))
	ok("verify-ca ignores the name", cfg(TLS{Mode: TLSVerifyCA, CA: f.CA}, "db.example.com"))
	fails("verify-ca with a wrong CA", cfg(TLS{Mode: TLSVerifyCA, CA: other.CA}, "localhost"))
	if n := ok("client certificate", cfg(TLS{Mode: TLSVerifyFull, CA: f.CA, Cert: f.ClientCert, Key: f.ClientKey}, "localhost")); n != 1 {
		t.Fatalf("server saw %d client certificates", n)
	}
	if c, err := (&TLS{Mode: TLSDisable}).Config("h"); c != nil || err != nil {
		t.Fatalf("disable: %v %v", c, err)
	}
	if c := cfg(TLS{Mode: TLSRequire}, "h"); c.MinVersion != tls.VersionTLS12 {
		t.Fatalf("min version %x", c.MinVersion)
	}
}
```

`internal/engine/postgres/tls_test.go`:
```go
package postgres

import (
	"context"
	"errors"
	"net"
	"strings"
	"testing"

	"github.com/anderson-0/qh/internal/engine"
	"github.com/anderson-0/qh/internal/testutil"
)

func TestPoolConfigAppliesTLS(t *testing.T) {
	f := testutil.NewTLSFixture(t)
	spec := engine.ConnSpec{
		DSN: "postgres://u:p@db1.example.com:5432,db2.example.com:5433/app?sslmode=prefer",
		TLS: &engine.TLS{Mode: engine.TLSVerifyFull, CA: f.CA, Cert: f.ClientCert, Key: f.ClientKey},
	}
	cfg, err := poolConfig(spec)
	if err != nil {
		t.Fatal(err)
	}
	cc := cfg.ConnConfig
	if cc.TLSConfig == nil || cc.TLSConfig.ServerName != "db1.example.com" || len(cc.TLSConfig.Certificates) != 1 {
		t.Fatalf("main host TLS: %+v", cc.TLSConfig)
	}
	if len(cc.Fallbacks) != 1 || cc.Fallbacks[0].Host != "db2.example.com" || cc.Fallbacks[0].Port != 5433 ||
		cc.Fallbacks[0].TLSConfig == nil || cc.Fallbacks[0].TLSConfig.ServerName != "db2.example.com" {
		t.Fatalf("fallbacks: %+v", cc.Fallbacks)
	}

	spec.DSN, spec.TLS = "postgres://u:p@db1.example.com/app?sslmode=require", &engine.TLS{Mode: engine.TLSDisable}
	if cfg, err = poolConfig(spec); err != nil {
		t.Fatal(err)
	}
	if cfg.ConnConfig.TLSConfig != nil || len(cfg.ConnConfig.Fallbacks) != 0 {
		t.Fatalf("disable kept TLS: %+v %+v", cfg.ConnConfig.TLSConfig, cfg.ConnConfig.Fallbacks)
	}

	// Server mode strips default certificate files, but keeps the client
	// certificate given through the TLS settings.
	spec.DSN = "postgres://u:p@db1.example.com/app"
	spec.TLS = &engine.TLS{Mode: engine.TLSRequire, Cert: f.ClientCert, Key: f.ClientKey}
	spec.Dial = func(context.Context, string, string) (net.Conn, error) { return nil, errors.New("not dialled") }
	if cfg, err = poolConfig(spec); err != nil {
		t.Fatal(err)
	}
	if cfg.ConnConfig.TLSConfig == nil || len(cfg.ConnConfig.TLSConfig.Certificates) != 1 {
		t.Fatalf("server mode lost the client certificate: %+v", cfg.ConnConfig.TLSConfig)
	}
}

func TestServerDSNRefusesQueryInsideBrackets(t *testing.T) {
	for _, dsn := range []string{"postgres://u@[::1?]/db", "postgresql://u:p@[db.example.com?x]/db", "postgres://h1,[::1?]/db"} {
		err := Engine{}.CheckServerDSN(dsn)
		if err == nil || !strings.HasPrefix(err.Error(), "error[invalid]: invalid postgres connection string") {
			t.Errorf("%s: %v", dsn, err)
		}
	}
	if err := (Engine{}).CheckServerDSN("postgres://u@[::1]:5432/db?sslmode=disable"); err != nil {
		t.Fatalf("plain IPv6 host refused: %v", err)
	}
}
```

`internal/engine/mysql/tls_test.go`:
```go
package mysql

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/anderson-0/qh/internal/engine"
	"github.com/anderson-0/qh/internal/testutil"
)

func TestTLSSettings(t *testing.T) {
	dsn := startDB(t, "mysql:8.4") // MySQL 8 generates a self-signed server certificate
	cipher := func(tl *engine.TLS) (string, error) {
		spec := engine.ConnSpec{ID: "tls", Version: 1, Engine: "mysql", DSN: dsn, Mode: engine.ModeRO, PoolSize: 1, TLS: tl}
		c, err := Engine{}.Open(context.Background(), spec)
		if err != nil {
			return "", err
		}
		defer c.Close()
		res, err := c.Query(context.Background(), "SHOW SESSION STATUS LIKE 'Ssl_cipher'",
			engine.QueryOpts{MaxRows: 10, ReadOnly: true, Timeout: 10 * time.Second})
		if err != nil {
			return "", err
		}
		return res.Rows[0][1].Text, nil
	}
	if c, err := cipher(&engine.TLS{Mode: engine.TLSRequire}); err != nil || c == "" {
		t.Fatalf("require: cipher %q, %v", c, err)
	}
	if c, err := cipher(&engine.TLS{Mode: engine.TLSDisable}); err != nil || c != "" {
		t.Fatalf("disable: cipher %q, %v", c, err)
	}
	f := testutil.NewTLSFixture(t)
	if _, err := cipher(&engine.TLS{Mode: engine.TLSVerifyCA, CA: f.CA}); err == nil || !strings.HasPrefix(err.Error(), "error[connection_failed]") {
		t.Fatalf("verify-ca with a foreign CA: %v", err)
	}
}
```

`internal/engine/sqlite/tls_test.go`:
```go
package sqlite

import (
	"context"
	"strings"
	"testing"

	"github.com/anderson-0/qh/internal/engine"
	"github.com/anderson-0/qh/internal/testutil"
)

func TestTLSRefused(t *testing.T) {
	spec := engine.ConnSpec{ID: "x", DSN: testutil.SQLiteFixture(t), Mode: engine.ModeRO, PoolSize: 1, TLS: &engine.TLS{Mode: engine.TLSRequire}}
	_, err := Engine{}.Open(context.Background(), spec)
	if err == nil || !strings.HasPrefix(err.Error(), "error[invalid]: tls applies only to network databases") {
		t.Fatalf("%v", err)
	}
}
```

- [ ] **Step 2: Run them to verify they fail**

Run: `go test -short ./internal/engine/...`
Expected: FAIL to build, with `undefined: TLS`, `unknown field TLS in struct literal of type engine.ConnSpec`, and `undefined: testutil.NewTLSFixture`.

- [ ] **Step 3: Implement**

`internal/engine/tls.go`:
```go
package engine

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"

	"github.com/anderson-0/qh/internal/qerr"
)

// TLS modes.
const (
	TLSDisable    = "disable"     // plain connection
	TLSRequire    = "require"     // encrypted; the certificate is not checked
	TLSVerifyCA   = "verify-ca"   // the chain must verify against CA (or the system roots)
	TLSVerifyFull = "verify-full" // verify-ca, and the certificate must name the host or ServerName
)

// TLS is a connection's TLS settings. When set, it replaces any TLS settings
// in the connection string. Key is secret: it is sealed with the connection
// string and never shown.
type TLS struct {
	Mode       string `json:"mode"`
	CA         string `json:"ca,omitempty"`   // PEM certificates to trust instead of the system roots
	Cert       string `json:"cert,omitempty"` // PEM client certificate
	Key        string `json:"key,omitempty"`  // PEM private key for Cert
	ServerName string `json:"server_name,omitempty"`
}

// TLSInfo is what may be shown about a connection's TLS settings.
type TLSInfo struct {
	Mode       string `json:"mode"`
	CA         bool   `json:"ca,omitempty"`
	ClientCert bool   `json:"client_cert,omitempty"`
	ServerName string `json:"server_name,omitempty"`
}

// Info returns the displayable summary of t, or nil when t is nil.
func (t *TLS) Info() *TLSInfo {
	if t == nil {
		return nil
	}
	return &TLSInfo{Mode: t.Mode, CA: t.CA != "", ClientCert: t.Cert != "", ServerName: t.ServerName}
}

func tlsInvalid(msg string) error {
	return qerr.New(qerr.Invalid, msg).
		WithHint("tls.mode is disable, require, verify-ca or verify-full; tls.ca, tls.cert and tls.key are PEM text")
}

// Validate checks t without connecting. Its errors never contain key material.
func (t *TLS) Validate() error {
	switch t.Mode {
	case TLSDisable, TLSRequire, TLSVerifyCA, TLSVerifyFull:
	case "":
		return tlsInvalid("tls.mode is required")
	default:
		return tlsInvalid(fmt.Sprintf("unknown tls.mode %q", t.Mode))
	}
	if t.Mode == TLSDisable && (t.CA != "" || t.Cert != "" || t.Key != "" || t.ServerName != "") {
		return tlsInvalid("tls.mode disable takes no other tls settings")
	}
	if (t.Cert == "") != (t.Key == "") {
		return tlsInvalid("tls.cert and tls.key must be given together")
	}
	_, err := t.Config("validate.invalid")
	return err
}

// Config builds the crypto/tls configuration for connecting to host. It
// returns nil for mode disable. Errors never contain key material.
func (t *TLS) Config(host string) (*tls.Config, error) {
	if t.Mode == TLSDisable {
		return nil, nil
	}
	c := &tls.Config{MinVersion: tls.VersionTLS12, ServerName: host}
	if t.ServerName != "" {
		c.ServerName = t.ServerName
	}
	if t.CA != "" {
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM([]byte(t.CA)) {
			return nil, tlsInvalid("tls.ca contains no PEM certificate")
		}
		c.RootCAs = pool
	}
	if t.Cert != "" {
		pair, err := tls.X509KeyPair([]byte(t.Cert), []byte(t.Key))
		if err != nil {
			return nil, tlsInvalid("tls.cert and tls.key are not a valid PEM certificate and key pair")
		}
		c.Certificates = []tls.Certificate{pair}
	}
	switch t.Mode {
	case TLSRequire:
		c.InsecureSkipVerify = true
	case TLSVerifyCA:
		// crypto/tls checks the chain and the name together, or neither, so
		// skip its check and verify the chain alone.
		c.InsecureSkipVerify = true
		roots := c.RootCAs
		c.VerifyPeerCertificate = func(raw [][]byte, _ [][]*x509.Certificate) error { return verifyChain(raw, roots) }
	}
	return c, nil
}

// verifyChain checks that the server's certificate chains to roots (the
// system roots when nil), without checking its name.
func verifyChain(raw [][]byte, roots *x509.CertPool) error {
	if len(raw) == 0 {
		return errors.New("tls: the server sent no certificate")
	}
	certs := make([]*x509.Certificate, len(raw))
	for i, b := range raw {
		c, err := x509.ParseCertificate(b)
		if err != nil {
			return err
		}
		certs[i] = c
	}
	inter := x509.NewCertPool()
	for _, c := range certs[1:] {
		inter.AddCert(c)
	}
	_, err := certs[0].Verify(x509.VerifyOptions{Roots: roots, Intermediates: inter})
	return err
}
```

In `internal/engine/engine.go`, add this field to `ConnSpec`, after `FileRoot`:
```go
	// TLS, when set, replaces the connection string's TLS settings.
	TLS *TLS
```

In `internal/engine/postgres/postgres.go`:

1. Add this function after `withoutClientCert`:
```go
// applyTLS replaces the connection string's TLS settings with t, for the
// main host and every fallback host. It drops the extra plain/TLS attempt
// that sslmode=prefer or allow adds for each host.
func applyTLS(cc *pgconn.Config, t *engine.TLS) error {
	c, err := t.Config(cc.Host)
	if err != nil {
		return err
	}
	cc.TLSConfig = c
	seen := map[string]bool{net.JoinHostPort(cc.Host, strconv.Itoa(int(cc.Port))): true}
	var fallbacks []*pgconn.FallbackConfig
	for _, fb := range cc.Fallbacks {
		k := net.JoinHostPort(fb.Host, strconv.Itoa(int(fb.Port)))
		if seen[k] {
			continue
		}
		seen[k] = true
		if fb.TLSConfig, err = t.Config(fb.Host); err != nil {
			return err
		}
		fallbacks = append(fallbacks, fb)
	}
	cc.Fallbacks = fallbacks
	return nil
}
```
2. In `poolConfig`, directly after the `if spec.Dial != nil { cc := cfg.ConnConfig … }` block that strips client certificates, add the block below. It must come after that block: server mode strips default certificate files, but must keep a certificate given through `spec.TLS`.
```go
	if spec.TLS != nil {
		if err := applyTLS(cfg.ConnConfig, spec.TLS); err != nil {
			return nil, err
		}
	}
```
3. Add this function before `checkServerDSN`, and call it first inside `checkServerDSN`:
```go
// questionInBrackets reports whether a URL connection string has a '?'
// inside a bracketed host. pgconn reads that '?' as part of the host, so the
// settings withSettings appends would not reach the query.
func questionInBrackets(dsn string) bool {
	if !strings.HasPrefix(dsn, "postgres://") && !strings.HasPrefix(dsn, "postgresql://") {
		return false
	}
	rest := dsn[strings.Index(dsn, "://")+3:]
	if i := strings.IndexAny(rest, "@/"); i >= 0 && rest[i] == '@' {
		rest = rest[i+1:]
	}
	inside := false
	for _, r := range rest {
		switch {
		case r == '[':
			inside = true
		case r == ']':
			inside = false
		case r == '?' && inside:
			return true
		case r == '/' && !inside:
			return false
		}
	}
	return false
}
```
The new first lines of `checkServerDSN`:
```go
	if questionInBrackets(dsn) {
		return false, qerr.New(qerr.Invalid, "invalid postgres connection string")
	}
```

In `internal/engine/mysql/conn.go` `Open`, add this after the `if spec.Dial != nil { … }` block (`net` is already imported in `mysql.go`; add it to `conn.go`'s imports):
```go
	if spec.TLS != nil {
		host, _, err := net.SplitHostPort(cfg.Addr)
		if err != nil {
			host = cfg.Addr
		}
		c, err := spec.TLS.Config(host)
		if err != nil {
			return nil, err
		}
		// cfg.TLS takes priority over the tls= setting; "false" keeps a nil
		// TLS from being re-derived from the connection string.
		cfg.TLS, cfg.AllowFallbackToPlaintext = c, false
		if c == nil {
			cfg.TLSConfig = "false"
		}
	}
```

In `internal/engine/sqlite/sqlite.go` `Open`, add this as the first statement:
```go
	if spec.TLS != nil {
		return nil, qerr.New(qerr.Invalid, "tls applies only to network databases")
	}
```

- [ ] **Step 4: Run the tests**

Run: `go test -race ./internal/engine/... ./internal/testutil/ && go vet ./...`
Expected: all PASS. `TestTLSSettings` needs Docker. The other new tests do not.

- [ ] **Step 5: Commit**

```bash
git add internal/testutil/tlscert.go internal/engine
git commit -m "feat(engine): add per-connection TLS settings and refuse '?' inside bracketed Postgres hosts"
```

---

### Task 2: Connection strings built from separate fields

**Files:**
- Create: `internal/engine/dsn.go`
- Modify: `internal/engine/postgres/postgres.go`, `internal/engine/mysql/mysql.go`, `internal/engine/sqlite/sqlite.go` (add `BuildDSN`)
- Test: `internal/engine/dsn_test.go`, `internal/engine/postgres/dsn_test.go`, `internal/engine/mysql/dsn_test.go`, `internal/engine/sqlite/dsn_test.go`

**Interfaces:**
- Consumes: each engine's `Inspect` (it is used to check the round trip); the MySQL `parseRaw`.
- Produces:
  - `type engine.DSNFields struct { Host string; Port int; Database, User, Password string; Params map[string]string }`.
  - `type engine.DSNBuilder interface { BuildDSN(f DSNFields) (string, error) }`.
  - `engine.BuildURL(scheme string, f DSNFields) (string, error)`.
  - `postgres.Engine`, `mysql.Engine` and `sqlite.Engine` all implement `DSNBuilder`.

- [ ] **Step 1: Write the failing tests**

`internal/engine/dsn_test.go`:
```go
package engine

import (
	"strings"
	"testing"
)

func TestBuildURLRejects(t *testing.T) {
	for _, c := range []struct {
		f    DSNFields
		want string
	}{
		{DSNFields{Database: "app"}, "host is required"},
		{DSNFields{Host: "db", Port: 70000}, "port must be between 1 and 65535"},
		{DSNFields{Host: "db", Password: "secret-pw"}, "a password needs a username"},
		{DSNFields{Host: "db/evil"}, "host contains characters that are not allowed"},
		{DSNFields{Host: "u@evil"}, "host contains characters that are not allowed"},
		{DSNFields{Host: "[::1]"}, "host contains characters that are not allowed"},
	} {
		_, err := BuildURL("postgres", c.f)
		if err == nil || !strings.HasPrefix(err.Error(), "error[invalid]: "+c.want) {
			t.Errorf("%+v: %v", c.f, err)
		}
		if err != nil && strings.Contains(err.Error(), "secret-pw") {
			t.Errorf("error leaks the password: %v", err)
		}
	}
}

func TestBuildURL(t *testing.T) {
	got, err := BuildURL("mysql", DSNFields{Host: "::1", Port: 3307, Database: "app", User: "u", Password: "p w", Params: map[string]string{"b": "2", "a": "1"}})
	if err != nil || got != "mysql://u:p%20w@[::1]:3307/app?a=1&b=2" {
		t.Fatalf("%q %v", got, err)
	}
	if got, _ := BuildURL("postgres", DSNFields{Host: "::1"}); got != "postgres://[::1]/" {
		t.Fatalf("%q", got)
	}
}
```

`internal/engine/postgres/dsn_test.go`:
```go
package postgres

import (
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/anderson-0/qh/internal/engine"
)

func TestBuildDSNRoundTrip(t *testing.T) {
	for _, f := range []engine.DSNFields{
		{Host: "db.example.com", Port: 5433, Database: "app", User: "reader", Password: "p@ss/w:rd%?#",
			Params: map[string]string{"sslmode": "require", "application_name": "a b"}},
		{Host: "::1", Database: "my db", User: "u"},
	} {
		dsn, err := Engine{}.BuildDSN(f)
		if err != nil {
			t.Fatal(err)
		}
		info, err := Engine{}.Inspect(dsn)
		if err != nil {
			t.Fatal(err)
		}
		port := f.Port
		if port == 0 {
			port = 5432
		}
		if info.Host != f.Host || info.Port != port || info.Database != f.Database || info.User != f.User || info.Password != f.Password {
			t.Fatalf("%q round trip: %+v", dsn, info)
		}
		if f.Password != "" && strings.Contains(info.Redacted, "rd%") {
			t.Fatalf("redacted string shows the password: %q", info.Redacted)
		}
		if f.Params != nil {
			cfg, err := pgconn.ParseConfig(dsn)
			if err != nil || cfg.RuntimeParams["application_name"] != "a b" {
				t.Fatalf("params: %v %v", cfg.RuntimeParams, err)
			}
		}
	}
}
```

`internal/engine/mysql/dsn_test.go`:
```go
package mysql

import (
	"testing"
	"time"

	"github.com/anderson-0/qh/internal/engine"
)

func TestBuildDSNRoundTrip(t *testing.T) {
	for _, f := range []engine.DSNFields{
		{Host: "db.example.com", Port: 3307, Database: "app", User: "reader", Password: "p@ss/w:rd%?#", Params: map[string]string{"timeout": "5s"}},
		{Host: "::1", Database: "shop", User: "u"},
	} {
		dsn, err := Engine{}.BuildDSN(f)
		if err != nil {
			t.Fatal(err)
		}
		info, err := Engine{}.Inspect(dsn)
		if err != nil {
			t.Fatal(err)
		}
		port := f.Port
		if port == 0 {
			port = 3306
		}
		if info.Host != f.Host || info.Port != port || info.Database != f.Database || info.User != f.User || info.Password != f.Password {
			t.Fatalf("%q round trip: %+v", dsn, info)
		}
		if f.Params != nil {
			cfg, err := parseRaw(dsn)
			if err != nil || cfg.Timeout != 5*time.Second {
				t.Fatalf("params: %v %v", cfg, err)
			}
		}
	}
}
```

`internal/engine/sqlite/dsn_test.go`:
```go
package sqlite

import (
	"strings"
	"testing"

	"github.com/anderson-0/qh/internal/engine"
)

func TestBuildDSN(t *testing.T) {
	if got, err := (Engine{}).BuildDSN(engine.DSNFields{Database: "/data/shop.db"}); err != nil || got != "/data/shop.db" {
		t.Fatalf("%q %v", got, err)
	}
	if _, err := (Engine{}).BuildDSN(engine.DSNFields{}); err == nil || !strings.HasPrefix(err.Error(), "error[invalid]: database (the file path) is required") {
		t.Fatalf("%v", err)
	}
	if _, err := (Engine{}).BuildDSN(engine.DSNFields{Host: "h", Database: "x.db"}); err == nil ||
		!strings.HasPrefix(err.Error(), "error[invalid]: sqlite takes only database, the path of the database file") {
		t.Fatalf("%v", err)
	}
}
```

- [ ] **Step 2: Run them to verify they fail**

Run: `go test -short ./internal/engine/...`
Expected: FAIL to build: `undefined: BuildURL`, `undefined: DSNFields`, `Engine{}.BuildDSN undefined`.

- [ ] **Step 3: Implement**

`internal/engine/dsn.go`:
```go
package engine

import (
	"net"
	"net/url"
	"strconv"
	"strings"

	"github.com/anderson-0/qh/internal/qerr"
)

// DSNFields are connection details given one by one instead of as a
// connection string.
type DSNFields struct {
	Host     string
	Port     int // 0 for the engine's default port
	Database string
	User     string
	Password string
	Params   map[string]string
}

// DSNBuilder is implemented by engines that build a connection string from
// DSNFields. Errors never contain the password.
type DSNBuilder interface {
	BuildDSN(f DSNFields) (string, error)
}

// BuildURL builds scheme://user:password@host:port/database?params with every
// part escaped, for engines whose connection strings are URLs. IPv6 hosts
// are given without brackets.
func BuildURL(scheme string, f DSNFields) (string, error) {
	switch {
	case f.Host == "":
		return "", qerr.New(qerr.Invalid, "host is required")
	case strings.ContainsAny(f.Host, "/?#@[]\\ \t\r\n"):
		return "", qerr.New(qerr.Invalid, "host contains characters that are not allowed").
			WithHint("Give a host name or an IP address, without brackets or a port")
	case f.Port < 0 || f.Port > 65535:
		return "", qerr.New(qerr.Invalid, "port must be between 1 and 65535")
	case f.User == "" && f.Password != "":
		return "", qerr.New(qerr.Invalid, "a password needs a username")
	}
	host := f.Host
	if f.Port != 0 {
		host = net.JoinHostPort(f.Host, strconv.Itoa(f.Port))
	} else if strings.Contains(f.Host, ":") {
		host = "[" + f.Host + "]"
	}
	u := url.URL{Scheme: scheme, Host: host, Path: "/" + f.Database}
	switch {
	case f.Password != "":
		u.User = url.UserPassword(f.User, f.Password)
	case f.User != "":
		u.User = url.User(f.User)
	}
	if len(f.Params) > 0 {
		q := url.Values{}
		for k, v := range f.Params {
			q.Set(k, v)
		}
		u.RawQuery = q.Encode() // sorted by key
	}
	return u.String(), nil
}
```

`internal/engine/postgres/postgres.go`. Add:
```go
// BuildDSN implements engine.DSNBuilder.
func (Engine) BuildDSN(f engine.DSNFields) (string, error) { return engine.BuildURL("postgres", f) }
```

`internal/engine/mysql/mysql.go`. Add:
```go
// BuildDSN implements engine.DSNBuilder.
func (Engine) BuildDSN(f engine.DSNFields) (string, error) { return engine.BuildURL("mysql", f) }
```

`internal/engine/sqlite/sqlite.go`. Add:
```go
// BuildDSN implements engine.DSNBuilder: a SQLite database is a file path.
func (Engine) BuildDSN(f engine.DSNFields) (string, error) {
	if f.Host != "" || f.Port != 0 || f.User != "" || f.Password != "" || len(f.Params) > 0 {
		return "", qerr.New(qerr.Invalid, "sqlite takes only database, the path of the database file")
	}
	if strings.TrimSpace(f.Database) == "" {
		return "", qerr.New(qerr.Invalid, "database (the file path) is required")
	}
	return f.Database, nil
}
```

- [ ] **Step 4: Run the tests**

Run: `go test -short ./internal/engine/... && go vet ./...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/engine
git commit -m "feat(engine): build connection strings from separate fields"
```

---

### Task 3: Store: TLS summary, query log, re-sealing

**Files:**
- Modify: `internal/store/store.go`, `internal/store/sqlite.go`
- Test: `internal/store/querylog_test.go`, `internal/store/reseal_test.go`

**Interfaces:**
- Consumes: the existing `SQLite`, `migrations`, `scanConnection`, `connColumns`, `s.now`.
- Produces:
  - `Connection.TLS string`: the JSON summary of the TLS settings, or "" when there are none. It is stored as `'{}'` in the existing `tls` column.
  - `type store.QueryLogEntry struct { ID int64; WorkspaceID, ConnectionID, ConnectionName, KeyID, Tool, Query string; RowCount, DurationMS int64; ErrorCategory string; At time.Time }`.
  - `type store.QueryLogFilter struct { WorkspaceID, ConnectionName string; Since time.Time; Limit int }`.
  - New `Store` methods:
    - `AppendQueryLog(ctx, e *QueryLogEntry) error`. It sets `e.ID`, and sets `e.At` when it is zero.
    - `ListQueryLog(ctx, f QueryLogFilter) ([]*QueryLogEntry, error)`. Results are newest first; `Limit <= 0` means no limit.
    - `PruneQueryLog(ctx, before time.Time) (int64, error)`.
    - `ResealConnections(ctx, fn func(c *Connection) (secret []byte, keyID uint32, err error)) (int, error)`.
  - Migration 3 creates `query_log`.

- [ ] **Step 1: Write the failing tests**

`internal/store/querylog_test.go`:
```go
package store

import (
	"context"
	"testing"
	"time"
)

func TestQueryLog(t *testing.T) {
	ctx := context.Background()
	s, _ := openTemp(t)
	if _, err := s.CreateWorkspace(ctx, "team"); err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	add := func(ws, conn string, at time.Time, cat string) {
		t.Helper()
		e := &QueryLogEntry{WorkspaceID: ws, ConnectionID: "id-" + conn, ConnectionName: conn, KeyID: "k1", Tool: "query",
			Query: "SELECT 1", RowCount: 1, DurationMS: 3, ErrorCategory: cat, At: at}
		if err := s.AppendQueryLog(ctx, e); err != nil || e.ID == 0 {
			t.Fatalf("append: %v id %d", err, e.ID)
		}
	}
	add(DefaultWorkspace, "shop", base, "")
	add(DefaultWorkspace, "shop", base.Add(time.Hour), "read_only")
	add(DefaultWorkspace, "crm", base.Add(2*time.Hour), "")
	add("team", "shop", base.Add(3*time.Hour), "")

	all, err := s.ListQueryLog(ctx, QueryLogFilter{WorkspaceID: DefaultWorkspace})
	if err != nil || len(all) != 3 || all[0].ConnectionName != "crm" || !all[2].At.Equal(base) {
		t.Fatalf("list: %v %+v", err, all)
	}
	if all[1].ErrorCategory != "read_only" || all[1].KeyID != "k1" || all[1].Tool != "query" || all[1].RowCount != 1 || all[1].DurationMS != 3 {
		t.Fatalf("fields: %+v", all[1])
	}
	shop, _ := s.ListQueryLog(ctx, QueryLogFilter{WorkspaceID: DefaultWorkspace, ConnectionName: "shop", Limit: 1})
	if len(shop) != 1 || !shop[0].At.Equal(base.Add(time.Hour)) {
		t.Fatalf("filter + limit: %+v", shop)
	}
	since, _ := s.ListQueryLog(ctx, QueryLogFilter{WorkspaceID: DefaultWorkspace, Since: base.Add(time.Hour)})
	if len(since) != 2 {
		t.Fatalf("since: %+v", since)
	}
	if n, err := s.PruneQueryLog(ctx, base.Add(90*time.Minute)); err != nil || n != 2 {
		t.Fatalf("prune: %d %v", n, err)
	}
	if rest, _ := s.ListQueryLog(ctx, QueryLogFilter{WorkspaceID: DefaultWorkspace}); len(rest) != 1 {
		t.Fatalf("after prune: %+v", rest)
	}
	if err := s.DeleteWorkspace(ctx, "team"); err != nil {
		t.Fatal(err)
	}
	if team, _ := s.ListQueryLog(ctx, QueryLogFilter{WorkspaceID: "team"}); len(team) != 0 {
		t.Fatalf("deleting a workspace kept its log: %+v", team)
	}
}

func TestConnectionTLSSummary(t *testing.T) {
	ctx := context.Background()
	s, _ := openTemp(t)
	c := sample("tls")
	c.TLS = `{"mode":"verify-full","ca":true}`
	if err := s.CreateConnection(ctx, c); err != nil {
		t.Fatal(err)
	}
	got, _ := s.GetConnection(ctx, DefaultWorkspace, "tls")
	if got.TLS != c.TLS {
		t.Fatalf("tls %q", got.TLS)
	}
	got.TLS = ""
	if err := s.UpdateConnection(ctx, got); err != nil {
		t.Fatal(err)
	}
	if again, _ := s.GetConnection(ctx, DefaultWorkspace, "tls"); again.TLS != "" {
		t.Fatalf("cleared tls %q", again.TLS)
	}
}
```

`internal/store/reseal_test.go`:
```go
package store

import (
	"context"
	"errors"
	"testing"
)

func TestResealConnections(t *testing.T) {
	ctx := context.Background()
	s, _ := openTemp(t)
	if _, err := s.CreateWorkspace(ctx, "team"); err != nil {
		t.Fatal(err)
	}
	a, b, c := sample("a"), sample("b"), sample("c")
	c.WorkspaceID = "team"
	for _, x := range []*Connection{a, b, c} {
		if err := s.CreateConnection(ctx, x); err != nil {
			t.Fatal(err)
		}
	}
	seen := 0
	n, err := s.ResealConnections(ctx, func(x *Connection) ([]byte, uint32, error) {
		seen++
		if x.Name == "b" {
			return nil, 0, nil // unchanged
		}
		return []byte("new-" + x.Name), 9, nil
	})
	if err != nil || n != 2 || seen != 3 {
		t.Fatalf("reseal: n=%d seen=%d %v", n, seen, err)
	}
	got, _ := s.GetConnection(ctx, "team", "c")
	if string(got.Secret) != "new-c" || got.SecretKeyID != 9 || got.Version != 1 {
		t.Fatalf("c: %q %d v%d", got.Secret, got.SecretKeyID, got.Version)
	}
	if got, _ := s.GetConnection(ctx, DefaultWorkspace, "b"); string(got.Secret) != "\x01\x02\x03" || got.SecretKeyID != 7 {
		t.Fatalf("b changed: %q %d", got.Secret, got.SecretKeyID)
	}

	boom := errors.New("boom")
	_, err = s.ResealConnections(ctx, func(x *Connection) ([]byte, uint32, error) {
		if x.Name == "c" {
			return nil, 0, boom
		}
		return []byte("second"), 10, nil
	})
	if !errors.Is(err, boom) {
		t.Fatalf("error: %v", err)
	}
	if got, _ := s.GetConnection(ctx, DefaultWorkspace, "a"); string(got.Secret) != "new-a" {
		t.Fatalf("failed reseal changed a: %q", got.Secret)
	}
}
```

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./internal/store/`
Expected: FAIL to build: `undefined: QueryLogEntry`, `c.TLS undefined`, `s.ResealConnections undefined`.

- [ ] **Step 3: Implement**

In `internal/store/store.go`:

1. Add this field to `Connection`, after `AllowedNamespaces`:
```go
	TLS               string // JSON summary of the TLS settings (engine.TLSInfo); "" when there are none
```
2. Add these types:
```go
// QueryLogEntry is one logged query call.
type QueryLogEntry struct {
	ID             int64
	WorkspaceID    string
	ConnectionID   string
	ConnectionName string // kept so entries of deleted connections still name them
	KeyID          string // "" for local calls
	Tool           string // "query" or "explain"
	Query          string
	RowCount       int64
	DurationMS     int64
	ErrorCategory  string // "" when the call succeeded
	At             time.Time
}

// QueryLogFilter selects query log entries of one workspace.
type QueryLogFilter struct {
	WorkspaceID    string
	ConnectionName string    // "" for every connection
	Since          time.Time // zero for no lower bound
	Limit          int       // <= 0 for no limit
}
```
3. Add these methods to the `Store` interface, before `Ping`:
```go
	AppendQueryLog(ctx context.Context, e *QueryLogEntry) error
	ListQueryLog(ctx context.Context, f QueryLogFilter) ([]*QueryLogEntry, error)
	PruneQueryLog(ctx context.Context, before time.Time) (int64, error)
	ResealConnections(ctx context.Context, fn func(c *Connection) (secret []byte, keyID uint32, err error)) (int, error)
```
Change the interface's doc comment to `// Store is the persistence interface.`

In `internal/store/sqlite.go`:

1. Append migration 3 to `migrations`:
```go
	{
		`CREATE TABLE query_log (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
			connection_id TEXT NOT NULL,
			connection_name TEXT NOT NULL,
			key_id TEXT NOT NULL DEFAULT '',
			tool TEXT NOT NULL,
			query TEXT NOT NULL,
			row_count INTEGER NOT NULL,
			duration_ms INTEGER NOT NULL,
			error_category TEXT NOT NULL DEFAULT '',
			at INTEGER NOT NULL
		)`,
		`CREATE INDEX query_log_workspace ON query_log (workspace_id, id)`,
		`CREATE INDEX query_log_at ON query_log (at)`,
	},
```
2. Replace `connColumns`, adding `tls` at the end:
```go
const connColumns = `id, workspace_id, name, engine, secret, secret_key_id, host, port, "database",
	username, description, mode, max_rows, timeout_ms, pool_size, allowed_namespaces, version,
	created_at, updated_at, tls`
```
3. Add this helper:
```go
// tlsColumn stores an empty TLS summary as '{}', the column's default.
func tlsColumn(s string) string {
	if s == "" {
		return "{}"
	}
	return s
}
```
4. In `CreateConnection`, use this insert. There is one more placeholder, and `tlsColumn(c.TLS)` is the last argument:
```go
	_, err = s.db.ExecContext(ctx, `INSERT INTO connections (`+connColumns+`)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 1, ?, ?, ?)`,
		c.ID, c.WorkspaceID, c.Name, c.Engine, c.Secret, c.SecretKeyID, c.Host, c.Port, c.Database,
		c.Username, c.Description, c.Mode, c.MaxRows, c.TimeoutMS, c.PoolSize, string(ns),
		now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano), tlsColumn(c.TLS))
```
5. In `UpdateConnection`, set `tls`:
```go
	res, err := s.db.ExecContext(ctx, `UPDATE connections SET engine = ?, secret = ?, secret_key_id = ?,
		host = ?, port = ?, "database" = ?, username = ?, description = ?, mode = ?, max_rows = ?,
		timeout_ms = ?, pool_size = ?, allowed_namespaces = ?, tls = ?, version = version + 1, updated_at = ?
		WHERE workspace_id = ? AND name = ? AND id = ? AND version = ?`,
		c.Engine, c.Secret, c.SecretKeyID, c.Host, c.Port, c.Database, c.Username, c.Description,
		c.Mode, c.MaxRows, c.TimeoutMS, c.PoolSize, string(ns), tlsColumn(c.TLS), now.Format(time.RFC3339Nano),
		c.WorkspaceID, c.Name, c.ID, c.Version)
```
6. In `scanConnection`, scan `tls` last:
```go
	var ns, created, updated, tls string
	err := r.Scan(&c.ID, &c.WorkspaceID, &c.Name, &c.Engine, &c.Secret, &c.SecretKeyID, &c.Host, &c.Port,
		&c.Database, &c.Username, &c.Description, &c.Mode, &c.MaxRows, &c.TimeoutMS, &c.PoolSize, &ns,
		&c.Version, &created, &updated, &tls)
	if err != nil {
		return nil, err
	}
	if tls != "{}" {
		c.TLS = tls
	}
```
7. Add the query-log and re-seal methods:
```go
func (s *SQLite) AppendQueryLog(ctx context.Context, e *QueryLogEntry) error {
	if e.At.IsZero() {
		e.At = s.now()
	}
	res, err := s.db.ExecContext(ctx, `INSERT INTO query_log (workspace_id, connection_id, connection_name, key_id,
		tool, query, row_count, duration_ms, error_category, at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		e.WorkspaceID, e.ConnectionID, e.ConnectionName, e.KeyID, e.Tool, e.Query, e.RowCount, e.DurationMS,
		e.ErrorCategory, e.At.UnixMilli())
	if err != nil {
		return err
	}
	e.ID, err = res.LastInsertId()
	return err
}

func (s *SQLite) ListQueryLog(ctx context.Context, f QueryLogFilter) ([]*QueryLogEntry, error) {
	q := `SELECT id, workspace_id, connection_id, connection_name, key_id, tool, query, row_count, duration_ms,
		error_category, at FROM query_log WHERE workspace_id = ?`
	args := []any{f.WorkspaceID}
	if f.ConnectionName != "" {
		q += ` AND connection_name = ?`
		args = append(args, f.ConnectionName)
	}
	if !f.Since.IsZero() {
		q += ` AND at >= ?`
		args = append(args, f.Since.UnixMilli())
	}
	limit := f.Limit
	if limit <= 0 {
		limit = -1 // SQLite: no limit
	}
	q += ` ORDER BY id DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*QueryLogEntry
	for rows.Next() {
		var e QueryLogEntry
		var at int64
		if err := rows.Scan(&e.ID, &e.WorkspaceID, &e.ConnectionID, &e.ConnectionName, &e.KeyID, &e.Tool, &e.Query,
			&e.RowCount, &e.DurationMS, &e.ErrorCategory, &at); err != nil {
			return nil, err
		}
		e.At = time.UnixMilli(at).UTC()
		out = append(out, &e)
	}
	return out, rows.Err()
}

func (s *SQLite) PruneQueryLog(ctx context.Context, before time.Time) (int64, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM query_log WHERE at < ?`, before.UnixMilli())
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// ResealConnections calls fn for every connection in every workspace inside
// one write transaction, and stores the secret fn returns. A nil secret
// leaves the connection unchanged. If fn fails, nothing is changed. Versions
// stay the same: the connection string itself does not change.
func (s *SQLite) ResealConnections(ctx context.Context, fn func(c *Connection) ([]byte, uint32, error)) (int, error) {
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return 0, err
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return 0, err
	}
	n, err := reseal(ctx, conn, fn)
	if err == nil {
		_, err = conn.ExecContext(ctx, "COMMIT")
	}
	if err != nil {
		conn.ExecContext(context.Background(), "ROLLBACK")
		return 0, err
	}
	return n, nil
}

func reseal(ctx context.Context, conn *sql.Conn, fn func(c *Connection) ([]byte, uint32, error)) (int, error) {
	rows, err := conn.QueryContext(ctx, `SELECT `+connColumns+` FROM connections ORDER BY workspace_id, name`)
	if err != nil {
		return 0, err
	}
	var all []*Connection
	for rows.Next() {
		c, err := scanConnection(rows)
		if err != nil {
			rows.Close()
			return 0, err
		}
		all = append(all, c)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, err
	}
	rows.Close()
	n := 0
	for _, c := range all {
		secret, keyID, err := fn(c)
		if err != nil {
			return 0, err
		}
		if secret == nil {
			continue
		}
		if _, err := conn.ExecContext(ctx, `UPDATE connections SET secret = ?, secret_key_id = ? WHERE id = ?`, secret, keyID, c.ID); err != nil {
			return 0, err
		}
		n++
	}
	return n, nil
}
```

- [ ] **Step 4: Run the tests**

Run: `go test -race ./internal/store/ && go vet ./...`
Expected: PASS for all store tests, old and new. Other packages that wrap `store.Store` in tests, if any, must still compile; `go vet ./...` covers that.

- [ ] **Step 5: Commit**

```bash
git add internal/store
git commit -m "feat(store): add the query log, a TLS summary per connection, and re-sealing"
```

---

### Task 4: Core: connection fields and TLS settings

**Files:**
- Create: `internal/core/sealed.go`
- Modify: `internal/core/connections.go`, `internal/core/service.go`
- Test: `internal/core/details_test.go`

**Interfaces:**
- Consumes:
  - from Task 1: `engine.TLS`, `engine.TLSInfo`, `(*TLS).Validate`, `(*TLS).Info`, `ConnSpec.TLS`;
  - from Task 2: `engine.DSNFields`, `engine.DSNBuilder`;
  - from Task 3: `store.Connection.TLS`.
- Produces:
  - `ConnInput` gains `Host string; Port int; Database, Username, Password string; Params map[string]string; TLS *engine.TLS`.
  - `ConnPatch` gains `TLS *engine.TLS; ClearTLS bool`.
  - `ConnInfo` gains `TLS *engine.TLSInfo`.
  - Secrets are sealed in the envelope format, and older raw-string secrets still open.
  - `connSpec(ws, id, version, eng, dsn, mode, poolSize, tls)`, which takes `tls` as a new last parameter.

- [ ] **Step 1: Write the failing tests**

`internal/core/details_test.go`:
```go
package core

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/anderson-0/qh/internal/engine"
	_ "github.com/anderson-0/qh/internal/engine/postgres"
	"github.com/anderson-0/qh/internal/store"
	"github.com/anderson-0/qh/internal/testutil"
)

func TestFieldsBuildTheConnectionString(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	db := testutil.SQLiteFixture(t)
	info, _, err := h.svc.CreateConnection(ctx, ws, ConnInput{Name: "shop", Engine: "sqlite", Database: db}, true)
	if err != nil || info.Database != db {
		t.Fatalf("sqlite fields: %+v %v", info, err)
	}
	pg, _, err := h.svc.CreateConnection(ctx, ws, ConnInput{Name: "pg", Engine: "postgres", Host: "db.example.com", Port: 5433,
		Database: "app", Username: "reader", Password: "p@ss/w:rd", Params: map[string]string{"sslmode": "disable"}}, false)
	if err != nil {
		t.Fatal(err)
	}
	if pg.Host != "db.example.com" || pg.Port != 5433 || pg.Database != "app" || pg.User != "reader" ||
		strings.Contains(pg.DSN, "p@ss") || !strings.Contains(pg.DSN, "***") {
		t.Fatalf("postgres fields: %+v", pg)
	}
	for _, c := range []struct {
		in   ConnInput
		want string
	}{
		{ConnInput{Name: "x", Engine: "postgres", DSN: "postgres://h/db", Host: "h"}, "error[invalid]: give either dsn or host, port, database, username, password and params, not both"},
		{ConnInput{Name: "x", Engine: "leaky", Host: "h"}, `error[invalid]: engine "leaky" needs a connection string (dsn)`},
		{ConnInput{Name: "x", Engine: "postgres", Database: "app"}, "error[invalid]: host is required"},
		{ConnInput{Name: "x", Engine: "postgres"}, "error[invalid]: connection string is required"},
	} {
		if _, _, err := h.svc.CreateConnection(ctx, ws, c.in, false); !strings.HasPrefix(errText(err), c.want) {
			t.Errorf("%+v: %q", c.in, errText(err))
		}
	}
}

func TestTLSSettingsAreSealedAndSummarized(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	f := testutil.NewTLSFixture(t)
	tl := &engine.TLS{Mode: engine.TLSVerifyFull, CA: f.CA, Cert: f.ClientCert, Key: f.ClientKey, ServerName: "db.internal"}
	info, _, err := h.svc.CreateConnection(ctx, ws, ConnInput{Name: "pg", Engine: "postgres", DSN: "postgres://u:pw@db.example.com/app", TLS: tl}, false)
	if err != nil {
		t.Fatal(err)
	}
	want := engine.TLSInfo{Mode: engine.TLSVerifyFull, CA: true, ClientCert: true, ServerName: "db.internal"}
	if info.TLS == nil || *info.TLS != want {
		t.Fatalf("summary: %+v", info.TLS)
	}
	rec, _ := h.st.GetConnection(ctx, ws, "pg")
	if bytes.Contains(rec.Secret, []byte("PRIVATE KEY")) || strings.Contains(rec.TLS, "PRIVATE") || strings.Contains(rec.TLS, "BEGIN") {
		t.Fatalf("TLS material stored in clear: %q", rec.TLS)
	}
	if got, _ := h.svc.GetConnection(ctx, ws, "pg"); got.TLS == nil || *got.TLS != want {
		t.Fatalf("get: %+v", got.TLS)
	}
	tgt, err := h.svc.resolve(ctx, ws, "pg")
	if err != nil || tgt.spec.TLS == nil || tgt.spec.TLS.Key != f.ClientKey || tgt.spec.TLS.ServerName != "db.internal" {
		t.Fatalf("spec TLS: %+v %v", tgt, err)
	}

	if _, _, err := h.svc.CreateConnection(ctx, ws, ConnInput{Name: "s", Engine: "sqlite", DSN: testutil.SQLiteFixture(t), TLS: &engine.TLS{Mode: engine.TLSRequire}}, false); !strings.HasPrefix(errText(err), "error[invalid]: tls applies only to network databases") {
		t.Fatalf("sqlite tls: %q", errText(err))
	}
	if _, _, err := h.svc.CreateConnection(ctx, ws, ConnInput{Name: "b", Engine: "postgres", DSN: "postgres://h/db", TLS: &engine.TLS{Mode: "prefer"}}, false); !strings.HasPrefix(errText(err), `error[invalid]: unknown tls.mode "prefer"`) {
		t.Fatalf("bad mode: %q", errText(err))
	}
	if _, _, err := h.svc.UpdateConnection(ctx, ws, "pg", ConnPatch{TLS: tl, ClearTLS: true}, false); !strings.HasPrefix(errText(err), "error[invalid]: give tls or clear it, not both") {
		t.Fatalf("both: %q", errText(err))
	}

	info, _, err = h.svc.UpdateConnection(ctx, ws, "pg", ConnPatch{ClearTLS: true}, false)
	if err != nil || info.TLS != nil || info.Version != 2 {
		t.Fatalf("clear: %+v %v", info, err)
	}
	if tgt, _ := h.svc.resolve(ctx, ws, "pg"); tgt.spec.TLS != nil {
		t.Fatal("cleared TLS still applied")
	}
	info, _, err = h.svc.UpdateConnection(ctx, ws, "pg", ConnPatch{TLS: &engine.TLS{Mode: engine.TLSRequire}}, false)
	if err != nil || info.TLS == nil || info.TLS.Mode != engine.TLSRequire {
		t.Fatalf("set: %+v %v", info, err)
	}
}

func TestLegacySecretStillOpens(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	db := testutil.SQLiteFixture(t)
	rec := &store.Connection{ID: store.NewID(), WorkspaceID: ws, Name: "old", Engine: "sqlite", Database: db,
		Mode: "ro", MaxRows: 100, TimeoutMS: 30000, PoolSize: 5}
	sealed, err := h.svc.keys.Seal([]byte(db), []byte(rec.ID)) // the pre-Plan-4 format: the raw connection string
	if err != nil {
		t.Fatal(err)
	}
	rec.Secret, rec.SecretKeyID = sealed, h.svc.keys.PrimaryID()
	if err := h.st.CreateConnection(ctx, rec); err != nil {
		t.Fatal(err)
	}
	if out, err := h.svc.Query(ctx, ws, QueryInput{Source: "old", Query: "SELECT count(*) AS n FROM orders"}); err != nil || !strings.HasPrefix(out, "n\n250\n") {
		t.Fatalf("query: %q %v", out, err)
	}
	if info, _ := h.svc.GetConnection(ctx, ws, "old"); info.DSN != db || info.TLS != nil {
		t.Fatalf("info: %+v", info)
	}
	d := "edited"
	if _, _, err := h.svc.UpdateConnection(ctx, ws, "old", ConnPatch{Description: &d}, false); err != nil {
		t.Fatal(err)
	}
	again, _ := h.st.GetConnection(ctx, ws, "old")
	pt, err := h.svc.keys.Open(again.Secret, []byte(again.ID))
	if err != nil || !bytes.HasPrefix(pt, []byte(`{"v":2,`)) {
		t.Fatalf("edit did not re-seal in the new format: %v", err)
	}
}
```

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./internal/core/`
Expected: FAIL to build: `unknown field Database in struct literal of type ConnInput`, `unknown field TLS`, `unknown field ClearTLS`.

- [ ] **Step 3: Implement**

`internal/core/sealed.go`:
```go
package core

import (
	"bytes"
	"encoding/json"

	"github.com/anderson-0/qh/internal/engine"
)

// envelopePrefix starts every secret sealed since Plan 4. Older secrets are
// the raw connection string, which never starts with it.
var envelopePrefix = []byte(`{"v":2,`)

// envelope is the plaintext of a sealed secret: the connection string and
// the TLS settings, whose private key is secret too.
type envelope struct {
	V   int         `json:"v"`
	DSN string      `json:"dsn"`
	TLS *engine.TLS `json:"tls,omitempty"`
}

func encodeSecret(dsn string, tls *engine.TLS) ([]byte, error) {
	return json.Marshal(envelope{V: 2, DSN: dsn, TLS: tls})
}

func decodeSecret(pt []byte) (string, *engine.TLS, error) {
	if !bytes.HasPrefix(pt, envelopePrefix) {
		return string(pt), nil, nil
	}
	var e envelope
	if err := json.Unmarshal(pt, &e); err != nil {
		return "", nil, err
	}
	return e.DSN, e.TLS, nil
}
```

In `internal/core/connections.go`:

1. Replace `ConnInput`:
```go
// ConnInput describes a connection to create. Connection details are either
// DSN, or the separate fields Host … Params, which the engine turns into a
// connection string.
type ConnInput struct {
	Name              string
	Engine            string
	DSN               string
	Host              string
	Port              int
	Database          string
	Username          string
	Password          string
	Params            map[string]string
	TLS               *engine.TLS
	Description       string
	Mode              engine.Mode
	MaxRows           int
	TimeoutMS         int
	PoolSize          int
	AllowedNamespaces []string
}
```
2. Add these two fields to `ConnPatch`:
```go
	TLS               *engine.TLS // replaces the TLS settings
	ClearTLS          bool        // removes the TLS settings
```
3. Add this field to `ConnInfo`, after `DSN`:
```go
	TLS               *engine.TLSInfo
```
4. Replace the start of `normalize`, from the engine check through the connection-string check. The rest of the function stays as it is.
```go
	in.Engine = strings.ToLower(strings.TrimSpace(in.Engine))
	eng, err := engine.Get(in.Engine)
	if err != nil {
		return in, err
	}
	if in.DSN, err = connString(eng, in); err != nil {
		return in, err
	}
	in.Host, in.Port, in.Database, in.Username, in.Password, in.Params = "", 0, "", "", "", nil
	if in.TLS != nil {
		if in.Engine == "sqlite" {
			return in, qerr.New(qerr.Invalid, "tls applies only to network databases")
		}
		if err := in.TLS.Validate(); err != nil {
			return in, err
		}
	}
```
Delete the old `in.DSN = strings.TrimSpace(in.DSN)` line and the `if in.DSN == "" { … }` block. Also delete the later `var err error` line (just before the `bounded` calls), because `err` is now declared above.

5. Add `connString`:
```go
// connString returns in.DSN, or builds a connection string from the
// separate fields. Giving both is an error.
func connString(eng engine.Engine, in ConnInput) (string, error) {
	dsn := strings.TrimSpace(in.DSN)
	fields := in.Host != "" || in.Port != 0 || in.Database != "" || in.Username != "" || in.Password != "" || len(in.Params) > 0
	if !fields {
		if dsn == "" {
			return "", qerr.New(qerr.Invalid, "connection string is required").
				WithHint("Give dsn, or host (database for sqlite) with port, database, username, password and params")
		}
		return dsn, nil
	}
	if dsn != "" {
		return "", qerr.New(qerr.Invalid, "give either dsn or host, port, database, username, password and params, not both")
	}
	b, ok := eng.(engine.DSNBuilder)
	if !ok {
		return "", qerr.New(qerr.Invalid, fmt.Sprintf("engine %q needs a connection string (dsn)", eng.Name()))
	}
	return b.BuildDSN(engine.DSNFields{Host: strings.TrimSpace(in.Host), Port: in.Port, Database: in.Database,
		User: in.Username, Password: in.Password, Params: in.Params})
}
```
6. Replace `load` and `fill`. Add the helper `tlsInfo` (`encoding/json` is a new import):
```go
// load opens a stored connection's secret.
func (s *Service) load(rec *store.Connection) (ConnInput, error) {
	pt, err := s.keys.Open(rec.Secret, []byte(rec.ID))
	if err != nil {
		return ConnInput{}, fmt.Errorf("connection %q: %w", rec.Name, err)
	}
	dsn, tls, err := decodeSecret(pt)
	if err != nil {
		return ConnInput{}, fmt.Errorf("connection %q: cannot read its sealed settings: %w", rec.Name, err)
	}
	return ConnInput{
		Name: rec.Name, Engine: rec.Engine, DSN: dsn, TLS: tls, Description: rec.Description,
		Mode: engine.Mode(rec.Mode), MaxRows: rec.MaxRows, TimeoutMS: rec.TimeoutMS, PoolSize: rec.PoolSize,
		AllowedNamespaces: rec.AllowedNamespaces,
	}, nil
}

// fill seals in.DSN and in.TLS into rec and copies the other fields.
func (s *Service) fill(rec *store.Connection, in ConnInput, info engine.DSNInfo) error {
	pt, err := encodeSecret(in.DSN, in.TLS)
	if err != nil {
		return err
	}
	sealed, err := s.keys.Seal(pt, []byte(rec.ID))
	if err != nil {
		return err
	}
	rec.Engine, rec.Secret, rec.SecretKeyID = in.Engine, sealed, s.keys.PrimaryID()
	rec.Host, rec.Port, rec.Database, rec.Username = info.Host, info.Port, info.Database, info.User
	rec.Description, rec.Mode = in.Description, string(in.Mode)
	rec.MaxRows, rec.TimeoutMS, rec.PoolSize = in.MaxRows, in.TimeoutMS, in.PoolSize
	rec.AllowedNamespaces = in.AllowedNamespaces
	rec.TLS = ""
	if in.TLS != nil {
		b, err := json.Marshal(in.TLS.Info())
		if err != nil {
			return err
		}
		rec.TLS = string(b)
	}
	return nil
}

// tlsInfo parses a stored TLS summary.
func tlsInfo(s string) *engine.TLSInfo {
	if s == "" {
		return nil
	}
	var t engine.TLSInfo
	if json.Unmarshal([]byte(s), &t) != nil {
		return nil
	}
	return &t
}
```
7. In `toInfo`, add `TLS: tlsInfo(rec.TLS),` to the struct literal.
8. In `testConn`, pass the TLS settings through. The `eng.Open` line becomes:
```go
	conn, err := eng.Open(ctx, s.connSpec(ws, "test", 0, in.Engine, in.DSN, in.Mode, 1, in.TLS))
```
9. In `UpdateConnection`, after the `if p.AllowedNamespaces != nil { … }` block, add:
```go
	if p.ClearTLS && p.TLS != nil {
		return nil, nil, qerr.New(qerr.Invalid, "give tls or clear it, not both")
	}
	if p.ClearTLS {
		in.TLS = nil
	}
	if p.TLS != nil {
		in.TLS = p.TLS
	}
```
Change its test condition to `if test && (p.DSN != nil || p.Mode != nil || p.TLS != nil || p.ClearTLS) {`.

10. In `TestConnection`, add the TLS settings to the input:
```go
	return s.testConn(ctx, ws, t.eng, ConnInput{Engine: t.rec.Engine, DSN: t.spec.DSN, Mode: t.spec.Mode, TLS: t.spec.TLS}, t.info)
```

In `internal/core/service.go`:

1. Give `connSpec` a `tls` parameter:
```go
func (s *Service) connSpec(ws, id string, version int64, eng, dsn string, mode engine.Mode, poolSize int, tls *engine.TLS) engine.ConnSpec {
	spec := engine.ConnSpec{ID: id, Version: version, Engine: eng, DSN: dsn, Mode: mode, PoolSize: poolSize, Dial: s.opts.Dial, TLS: tls}
	if s.opts.SQLiteRoot != "" {
		spec.FileRoot = filepath.Join(s.opts.SQLiteRoot, ws)
	}
	return spec
}
```
2. In `resolve`, pass `in.TLS` as the last argument:
```go
		spec: s.connSpec(ws, rec.ID, rec.Version, rec.Engine, in.DSN, in.Mode, rec.PoolSize, in.TLS),
```

- [ ] **Step 4: Run the tests**

Run: `go test -race ./internal/core/ && go vet ./... && go test -short ./...`
Expected: PASS, including every earlier core test. `TestCreateValidation` still sees `error[invalid]: connection string is required`.

- [ ] **Step 5: Commit**

```bash
git add internal/core
git commit -m "feat(core): accept connection fields and seal TLS settings with the connection string"
```

---

### Task 5: Core: the query log

**Files:**
- Create: `internal/core/querylog.go`
- Modify: `internal/core/service.go` (`Options`, `QueryResult`)
- Test: `internal/core/querylog_test.go`

**Interfaces:**
- Consumes: from Task 3, `store.QueryLogEntry`, `store.QueryLogFilter`, `AppendQueryLog`, `ListQueryLog` and `PruneQueryLog`; `auth.ParseExpiry`.
- Produces:
  - `core.Options.NoQueryLog bool`.
  - `core.WithKeyID(ctx, id string) context.Context`.
  - `type core.QueryLogEntry struct { ID int64; Connection, KeyID, Tool, Query string; Rows int64; Duration time.Duration; ErrorCategory string; At time.Time }`.
  - `type core.QueryLogFilter struct { Connection string; Since time.Time; Limit int }`.
  - `(*Service).QueryLog(ctx, ws string, f QueryLogFilter) ([]*QueryLogEntry, error)`.
  - `(*Service).PruneQueryLog(ctx, retention time.Duration) (int64, error)`.
  - `core.ParseSince(v string, now time.Time) (time.Time, error)`.
  - The constants `core.DefaultLogLimit = 100` and `core.MaxLogLimit = 1000`.

- [ ] **Step 1: Write the failing tests**

`internal/core/querylog_test.go`:
```go
package core

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/anderson-0/qh/internal/catalog"
	"github.com/anderson-0/qh/internal/engine"
	"github.com/anderson-0/qh/internal/pool"
	"github.com/anderson-0/qh/internal/store"
	"github.com/anderson-0/qh/internal/testutil"
)

func TestQueryLogRecordsCalls(t *testing.T) {
	h := newHarness(t)
	h.addShop(t, engine.ModeRO)
	ctx := WithKeyID(context.Background(), "key-1")
	if _, err := h.svc.Query(ctx, ws, QueryInput{Source: "shop", Query: "SELECT id FROM users"}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.svc.Query(ctx, ws, QueryInput{Source: "shop", Query: "DELETE FROM users"}); err == nil {
		t.Fatal("write on ro")
	}
	if _, err := h.svc.Query(context.Background(), ws, QueryInput{Source: "shop", Query: "SELECT 1", Explain: true}); err != nil {
		t.Fatal(err)
	}
	h.svc.Query(ctx, ws, QueryInput{Source: "nope", Query: "SELECT 1"}) // unknown connections are not logged
	long := "SELECT '" + strings.Repeat("é", 5000) + "'"
	if _, err := h.svc.Query(ctx, ws, QueryInput{Source: "shop", Query: long}); err != nil {
		t.Fatal(err)
	}

	got, err := h.svc.QueryLog(context.Background(), ws, QueryLogFilter{})
	if err != nil || len(got) != 4 {
		t.Fatalf("log: %v %+v", err, got)
	}
	if len(got[0].Query) > 8192 || !utf8.ValidString(got[0].Query) || !strings.HasPrefix(got[0].Query, "SELECT 'é") {
		t.Fatalf("long query kept as %d bytes", len(got[0].Query))
	}
	if got[1].Tool != "explain" || got[1].KeyID != "" {
		t.Fatalf("explain: %+v", got[1])
	}
	if got[2].ErrorCategory != "read_only" || got[2].Rows != 0 {
		t.Fatalf("read_only: %+v", got[2])
	}
	if e := got[3]; e.Tool != "query" || e.Rows != 3 || e.KeyID != "key-1" || e.Connection != "shop" || e.ErrorCategory != "" ||
		e.Query != "SELECT id FROM users" || time.Since(e.At) > time.Minute {
		t.Fatalf("query: %+v", e)
	}
	if one, _ := h.svc.QueryLog(context.Background(), ws, QueryLogFilter{Limit: 1}); len(one) != 1 {
		t.Fatalf("limit: %+v", one)
	}
	if none, _ := h.svc.QueryLog(context.Background(), ws, QueryLogFilter{Connection: "crm"}); len(none) != 0 {
		t.Fatalf("filter: %+v", none)
	}
	if _, err := h.svc.QueryLog(context.Background(), ws, QueryLogFilter{Limit: 1001}); !strings.HasPrefix(errText(err), "error[invalid]: limit must be between 1 and 1000") {
		t.Fatalf("limit check: %q", errText(err))
	}
	if _, err := h.svc.CreateWorkspace(context.Background(), "team"); err != nil {
		t.Fatal(err)
	}
	if other, _ := h.svc.QueryLog(context.Background(), "team", QueryLogFilter{}); len(other) != 0 {
		t.Fatalf("another workspace sees: %+v", other)
	}
}

func TestQueryLogDisabledAndPruned(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	off := newServiceWith(t, st, Options{NoQueryLog: true})
	if _, _, err := off.CreateConnection(ctx, ws, ConnInput{Name: "shop", Engine: "sqlite", DSN: testutil.SQLiteFixture(t)}, false); err != nil {
		t.Fatal(err)
	}
	if _, err := off.Query(ctx, ws, QueryInput{Source: "shop", Query: "SELECT 1"}); err != nil {
		t.Fatal(err)
	}
	if got, _ := off.QueryLog(ctx, ws, QueryLogFilter{}); len(got) != 0 {
		t.Fatalf("logged while disabled: %+v", got)
	}
	old := &store.QueryLogEntry{WorkspaceID: ws, ConnectionID: "c", ConnectionName: "shop", Tool: "query", Query: "SELECT 1", At: time.Now().Add(-48 * time.Hour)}
	recent := &store.QueryLogEntry{WorkspaceID: ws, ConnectionID: "c", ConnectionName: "shop", Tool: "query", Query: "SELECT 2", At: time.Now()}
	for _, e := range []*store.QueryLogEntry{old, recent} {
		if err := st.AppendQueryLog(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	if n, err := off.PruneQueryLog(ctx, 24*time.Hour); err != nil || n != 1 {
		t.Fatalf("prune: %d %v", n, err)
	}
}

type failingLog struct{ *store.SQLite }

func (failingLog) AppendQueryLog(context.Context, *store.QueryLogEntry) error { return errors.New("disk full") }

func TestQueryLogFailureDoesNotFailQuery(t *testing.T) {
	h := newHarness(t)
	h.addShop(t, engine.ModeRO)
	svc := New(failingLog{h.st}, h.svc.keys, pool.New(OpenConn, pool.Options{}), catalog.New(time.Minute))
	if out, err := svc.Query(context.Background(), ws, QueryInput{Source: "shop", Query: "SELECT count(*) AS n FROM orders"}); err != nil || !strings.HasPrefix(out, "n\n250\n") {
		t.Fatalf("%q %v", out, err)
	}
}

func TestParseSince(t *testing.T) {
	now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	for in, want := range map[string]time.Time{
		"2026-10-01T00:00:00Z": time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC),
		"90m":                  now.Add(-90 * time.Minute),
		"7d":                   now.Add(-7 * 24 * time.Hour),
	} {
		if got, err := ParseSince(in, now); err != nil || !got.Equal(want) {
			t.Errorf("%s: %v %v", in, got, err)
		}
	}
	if _, err := ParseSince("yesterday", now); !strings.HasPrefix(errText(err), "error[invalid]: since must be an RFC 3339 time or a duration such as 24h or 7d") {
		t.Fatalf("%q", errText(err))
	}
}
```

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./internal/core/ -run 'QueryLog|ParseSince'`
Expected: FAIL to build: `undefined: WithKeyID`, `undefined: QueryLogFilter`, `unknown field NoQueryLog`.

- [ ] **Step 3: Implement**

`internal/core/querylog.go`:
```go
package core

import (
	"context"
	"time"
	"unicode/utf8"

	"github.com/anderson-0/qh/internal/auth"
	"github.com/anderson-0/qh/internal/engine"
	"github.com/anderson-0/qh/internal/qerr"
	"github.com/anderson-0/qh/internal/store"
)

const (
	DefaultLogLimit = 100
	MaxLogLimit     = 1000
	// maxLoggedQuery caps the statement text kept per entry.
	maxLoggedQuery = 8 << 10
)

type keyIDKey struct{}

// WithKeyID records which API key makes the calls in ctx, for the query log.
func WithKeyID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, keyIDKey{}, id)
}

func keyIDOf(ctx context.Context) string {
	id, _ := ctx.Value(keyIDKey{}).(string)
	return id
}

// QueryLogEntry is one logged query call.
type QueryLogEntry struct {
	ID            int64
	Connection    string
	KeyID         string // "" for local calls
	Tool          string // "query" or "explain"
	Query         string
	Rows          int64 // rows returned, or rows changed by a write
	Duration      time.Duration
	ErrorCategory string // "" when the call succeeded
	At            time.Time
}

// QueryLogFilter selects entries. Connection "" and a zero Since mean no
// filter; Limit 0 means DefaultLogLimit.
type QueryLogFilter struct {
	Connection string
	Since      time.Time
	Limit      int
}

// truncateUTF8 cuts s to at most n bytes without splitting a character.
func truncateUTF8(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// logQuery records one query call. A failed write is dropped: the log must
// never fail or slow a query beyond the insert itself.
func (s *Service) logQuery(ctx context.Context, ws string, t *target, in QueryInput, res *engine.Result, err error, d time.Duration) {
	if s.opts.NoQueryLog {
		return
	}
	e := &store.QueryLogEntry{
		WorkspaceID: ws, ConnectionID: t.rec.ID, ConnectionName: t.rec.Name, KeyID: keyIDOf(ctx),
		Tool: "query", Query: truncateUTF8(in.Query, maxLoggedQuery), DurationMS: d.Milliseconds(),
	}
	if in.Explain {
		e.Tool = "explain"
	}
	switch {
	case err != nil:
		e.ErrorCategory = string(qerr.From(err).Category)
	case res.RowsAffected >= 0:
		e.RowCount = res.RowsAffected
	default:
		e.RowCount = int64(len(res.Rows))
	}
	_ = s.store.AppendQueryLog(context.WithoutCancel(ctx), e)
}

// QueryLog returns the workspace's logged query calls, newest first.
func (s *Service) QueryLog(ctx context.Context, ws string, f QueryLogFilter) ([]*QueryLogEntry, error) {
	if f.Limit == 0 {
		f.Limit = DefaultLogLimit
	}
	if f.Limit < 0 || f.Limit > MaxLogLimit {
		return nil, qerr.New(qerr.Invalid, "limit must be between 1 and 1000")
	}
	rows, err := s.store.ListQueryLog(ctx, store.QueryLogFilter{WorkspaceID: ws, ConnectionName: f.Connection, Since: f.Since, Limit: f.Limit})
	if err != nil {
		return nil, err
	}
	out := make([]*QueryLogEntry, len(rows))
	for i, r := range rows {
		out[i] = &QueryLogEntry{ID: r.ID, Connection: r.ConnectionName, KeyID: r.KeyID, Tool: r.Tool, Query: r.Query,
			Rows: r.RowCount, Duration: time.Duration(r.DurationMS) * time.Millisecond, ErrorCategory: r.ErrorCategory, At: r.At}
	}
	return out, nil
}

// PruneQueryLog deletes entries older than retention, in every workspace.
func (s *Service) PruneQueryLog(ctx context.Context, retention time.Duration) (int64, error) {
	return s.store.PruneQueryLog(ctx, time.Now().Add(-retention))
}

// ParseSince reads a query-log lower bound: an RFC 3339 time, or a duration
// back from now such as 90m, 24h or 7d.
func ParseSince(v string, now time.Time) (time.Time, error) {
	if t, err := time.Parse(time.RFC3339, v); err == nil {
		return t, nil
	}
	if d, err := auth.ParseExpiry(v); err == nil && d > 0 {
		return now.Add(-d), nil
	}
	return time.Time{}, qerr.New(qerr.Invalid, "since must be an RFC 3339 time or a duration such as 24h or 7d")
}
```

In `internal/core/service.go`:

1. Add this field to `Options`:
```go
	// NoQueryLog turns the query log off.
	NoQueryLog bool
```
2. Split `QueryResult`, so that every call on a resolved connection is logged. Rename the existing function to `runQuery`. Its signature becomes `func (s *Service) runQuery(ctx context.Context, t *target, in QueryInput) (*engine.Result, error)`. Remove its first four lines, the `resolve` call and its error check, because `t` is now a parameter. The rest of its body stays the same. Then add:
```go
// QueryResult runs one statement and returns its result.
func (s *Service) QueryResult(ctx context.Context, ws string, in QueryInput) (*engine.Result, error) {
	t, err := s.resolve(ctx, ws, in.Source)
	if err != nil {
		return nil, err
	}
	start := time.Now()
	res, err := s.runQuery(ctx, t, in)
	s.logQuery(ctx, ws, t, in, res, err, time.Since(start))
	return res, err
}
```

- [ ] **Step 4: Run the tests**

Run: `go test -race ./internal/core/ && go vet ./...`
Expected: PASS, including the earlier core tests.

- [ ] **Step 5: Commit**

```bash
git add internal/core
git commit -m "feat(core): log query calls with retention pruning"
```

---

### Task 6: `qh masterkey rotate`

**Files:**
- Create: `internal/core/rotate.go`, `internal/cli/masterkey.go`
- Modify: `internal/cli/app.go` (old keys, and keep the keyring), `internal/cli/root.go` (register the command)
- Test: `internal/core/rotate_test.go`, `internal/cli/masterkey_test.go`

**Interfaces:**
- Consumes: from Task 3, `store.ResealConnections`; `secret.NewKeyring`, `secret.KeyID`, `secret.ParseKey`; from Task 4, the sealed envelope.
- Produces:
  - `(*core.Service).RotateMasterKey(ctx) (resealed, current int, err error)`.
  - `openAppWith(home string, opts core.Options, oldKeys ...[]byte) (*app, error)`.
  - `app.keys *secret.Keyring`.
  - The command `qh masterkey rotate --old-key-file F`.

- [ ] **Step 1: Write the failing tests**

`internal/core/rotate_test.go`:
```go
package core

import (
	"context"
	"crypto/rand"
	"strings"
	"testing"
	"time"

	"github.com/anderson-0/qh/internal/catalog"
	"github.com/anderson-0/qh/internal/pool"
	"github.com/anderson-0/qh/internal/secret"
	"github.com/anderson-0/qh/internal/store"
	"github.com/anderson-0/qh/internal/testutil"
)

func randomKey(t *testing.T) []byte {
	t.Helper()
	k := make([]byte, secret.KeySize)
	if _, err := rand.Read(k); err != nil {
		t.Fatal(err)
	}
	return k
}

func keyring(t *testing.T, primary []byte, old ...[]byte) *secret.Keyring {
	t.Helper()
	k, err := secret.NewKeyring(primary, old...)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func serviceWith(t *testing.T, st *store.SQLite, keys *secret.Keyring) *Service {
	t.Helper()
	pools := pool.New(OpenConn, pool.Options{})
	t.Cleanup(pools.Close)
	return New(st, keys, pools, catalog.New(time.Minute))
}

// insertRaw stores a connection sealed in the pre-Plan-4 format.
func insertRaw(t *testing.T, st *store.SQLite, keys *secret.Keyring, name, dsn string) {
	t.Helper()
	rec := &store.Connection{ID: store.NewID(), WorkspaceID: ws, Name: name, Engine: "sqlite", Database: dsn,
		Mode: "ro", MaxRows: 100, TimeoutMS: 30000, PoolSize: 5}
	sealed, err := keys.Seal([]byte(dsn), []byte(rec.ID))
	if err != nil {
		t.Fatal(err)
	}
	rec.Secret, rec.SecretKeyID = sealed, keys.PrimaryID()
	if err := st.CreateConnection(context.Background(), rec); err != nil {
		t.Fatal(err)
	}
}

func TestRotateMasterKey(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	db := testutil.SQLiteFixture(t)
	oldKey, newKey := randomKey(t), randomKey(t)
	old := keyring(t, oldKey)
	if _, _, err := serviceWith(t, st, old).CreateConnection(ctx, ws, ConnInput{Name: "shop", Engine: "sqlite", DSN: db}, false); err != nil {
		t.Fatal(err)
	}
	insertRaw(t, st, old, "legacy", db)

	n, cur, err := serviceWith(t, st, keyring(t, newKey, oldKey)).RotateMasterKey(ctx)
	if err != nil || n != 2 || cur != 0 {
		t.Fatalf("rotate: %d %d %v", n, cur, err)
	}
	fresh := serviceWith(t, st, keyring(t, newKey))
	for _, name := range []string{"shop", "legacy"} {
		rec, _ := st.GetConnection(ctx, ws, name)
		if rec.SecretKeyID != secret.KeyID(newKey) || rec.Version != 1 {
			t.Fatalf("%s: key %08x version %d", name, rec.SecretKeyID, rec.Version)
		}
		if out, err := fresh.Query(ctx, ws, QueryInput{Source: name, Query: "SELECT count(*) AS n FROM orders"}); err != nil || !strings.HasPrefix(out, "n\n250\n") {
			t.Fatalf("%s with only the new key: %q %v", name, out, err)
		}
	}
	if n, cur, err := serviceWith(t, st, keyring(t, newKey, oldKey)).RotateMasterKey(ctx); err != nil || n != 0 || cur != 2 {
		t.Fatalf("second rotation: %d %d %v", n, cur, err)
	}

	// A connection sealed with a key the keyring lacks stops the rotation, and nothing changes.
	stranger := keyring(t, randomKey(t))
	insertRaw(t, st, stranger, "stray", db)
	insertRaw(t, st, old, "later", db)
	_, _, err = serviceWith(t, st, keyring(t, newKey, oldKey)).RotateMasterKey(ctx)
	if !strings.HasPrefix(errText(err), `error[invalid]: connection "stray" in workspace "default" cannot be opened with the current or the old master key`) {
		t.Fatalf("stray: %q", errText(err))
	}
	if rec, _ := st.GetConnection(ctx, ws, "later"); rec.SecretKeyID != secret.KeyID(oldKey) {
		t.Fatal("a failed rotation changed a connection")
	}
}
```

`internal/cli/masterkey_test.go`:
```go
package cli

import (
	"crypto/rand"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/anderson-0/qh/internal/testutil"
)

func TestMasterkeyRotate(t *testing.T) {
	t.Setenv("QH_MASTER_KEY", "")
	home, db := t.TempDir(), testutil.SQLiteFixture(t)
	if _, errOut, code := run(t, home, "", "conn", "add", "shop", "--engine", "sqlite", "--dsn", db); code != 0 {
		t.Fatalf("add: %s", errOut)
	}
	old := filepath.Join(t.TempDir(), "old.key")
	if err := os.Rename(filepath.Join(home, "master.key"), old); err != nil {
		t.Fatal(err)
	}
	// The next command generates a new master.key, which cannot open shop yet.
	out, errOut, code := run(t, home, "", "masterkey", "rotate", "--old-key-file", old)
	if code != 0 || !strings.HasPrefix(out, "re-sealed 1 of 1 connections with master key ") {
		t.Fatalf("rotate: %d %q %q", code, out, errOut)
	}
	if out, _, _ := run(t, home, "", "query", "shop", "SELECT count(*) AS n FROM orders"); !strings.HasPrefix(out, "n\n250\n") {
		t.Fatalf("query after rotation: %q", out)
	}
	if out, _, _ := run(t, home, "", "masterkey", "rotate", "--old-key-file", old); !strings.HasPrefix(out, "re-sealed 0 of 1 connections") {
		t.Fatalf("second rotation: %q", out)
	}
	if _, errOut, code := run(t, home, "", "masterkey", "rotate", "--old-key-file", filepath.Join(home, "master.key")); code != 1 ||
		!strings.HasPrefix(errOut, "error[invalid]: the old key is the current master key") {
		t.Fatalf("same key: %d %q", code, errOut)
	}
	if _, errOut, code := run(t, home, "", "masterkey", "rotate"); code != 1 || !strings.HasPrefix(errOut, "error[invalid]: --old-key-file is required") {
		t.Fatalf("missing flag: %d %q", code, errOut)
	}

	// A wrong old key changes nothing.
	home2 := t.TempDir()
	run(t, home2, "", "conn", "add", "shop", "--engine", "sqlite", "--dsn", db)
	os.Remove(filepath.Join(home2, "master.key"))
	wrong := filepath.Join(t.TempDir(), "wrong.key")
	k := make([]byte, 32)
	rand.Read(k)
	os.WriteFile(wrong, []byte(base64.StdEncoding.EncodeToString(k)+"\n"), 0o600)
	if _, errOut, code := run(t, home2, "", "masterkey", "rotate", "--old-key-file", wrong); code != 1 ||
		!strings.HasPrefix(errOut, `error[invalid]: connection "shop" in workspace "default" cannot be opened`) {
		t.Fatalf("wrong key: %d %q", code, errOut)
	}
}
```

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./internal/core/ -run Rotate && go test ./internal/cli/ -run Masterkey`
Expected: FAIL: `RotateMasterKey undefined`, and `unknown command "masterkey"`.

- [ ] **Step 3: Implement**

`internal/core/rotate.go`:
```go
package core

import (
	"context"
	"fmt"

	"github.com/anderson-0/qh/internal/qerr"
	"github.com/anderson-0/qh/internal/store"
)

// RotateMasterKey re-seals every connection, in every workspace, with the
// keyring's primary key. Connections already sealed with it are counted in
// current and left alone. Everything happens in one store transaction: if a
// connection cannot be opened with the keyring's keys, nothing changes.
func (s *Service) RotateMasterKey(ctx context.Context) (resealed, current int, err error) {
	primary := s.keys.PrimaryID()
	resealed, err = s.store.ResealConnections(ctx, func(c *store.Connection) ([]byte, uint32, error) {
		if c.SecretKeyID == primary {
			current++
			return nil, 0, nil
		}
		pt, err := s.keys.Open(c.Secret, []byte(c.ID))
		if err != nil {
			return nil, 0, qerr.New(qerr.Invalid, fmt.Sprintf("connection %q in workspace %q cannot be opened with the current or the old master key", c.Name, c.WorkspaceID)).
				WithHint("Pass the key it was sealed with as --old-key-file; nothing was changed")
		}
		sealed, err := s.keys.Seal(pt, []byte(c.ID))
		return sealed, primary, err
	})
	if err != nil {
		return 0, 0, err
	}
	return resealed, current, nil
}
```
The plaintext is re-sealed byte for byte. A raw pre-Plan-4 string stays raw, and it still opens (Task 4).

In `internal/cli/app.go`:

1. Add `keys *secret.Keyring` to `app`.
2. Change `openAppWith` to take old keys and keep the keyring:
```go
func openAppWith(home string, opts core.Options, oldKeys ...[]byte) (*app, error) {
```
Inside it, the keyring line becomes `keys, err := secret.NewKeyring(key, oldKeys...)`. The return becomes:
```go
	return &app{svc: core.New(st, keys, pools, catalog.New(10*time.Minute), opts), pools: pools, st: st, keys: keys}, nil
```

`internal/cli/masterkey.go`:
```go
package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/anderson-0/qh/internal/core"
	"github.com/anderson-0/qh/internal/qerr"
	"github.com/anderson-0/qh/internal/secret"
)

func masterkeyCmd(home *string) *cobra.Command {
	c := &cobra.Command{Use: "masterkey", Short: "Manage the master key that seals connection strings"}
	var oldFile string
	rotate := &cobra.Command{
		Use:   "rotate",
		Short: "Re-seal every connection with the current master key (stop qh serve first)",
		Long: "Re-seals every stored connection with the current master key (QH_MASTER_KEY, or master.key in the data directory).\n" +
			"--old-key-file holds the previous key. Stop any running qh serve or qh mcp first, and restart it with the new key afterwards.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if oldFile == "" {
				return qerr.New(qerr.Invalid, "--old-key-file is required").WithHint("Give the file holding the previous base64 master key")
			}
			b, err := os.ReadFile(oldFile)
			if err != nil {
				return qerr.Wrap(qerr.Invalid, "cannot read --old-key-file", err)
			}
			old, err := secret.ParseKey(string(b))
			if err != nil {
				return qerr.New(qerr.Invalid, "--old-key-file does not hold a base64 32-byte key")
			}
			dir, err := homeDir(*home)
			if err != nil {
				return err
			}
			a, err := openAppWith(dir, core.Options{}, old)
			if err != nil {
				return err
			}
			defer a.Close()
			if secret.KeyID(old) == a.keys.PrimaryID() {
				return qerr.New(qerr.Invalid, "the old key is the current master key").
					WithHint("Set QH_MASTER_KEY (or master.key) to the new key, and pass the previous one as --old-key-file")
			}
			n, cur, err := a.svc.RotateMasterKey(cmd.Context())
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "re-sealed %d of %d connections with master key %08x\n", n, n+cur, a.keys.PrimaryID())
			return nil
		},
	}
	rotate.Flags().StringVar(&oldFile, "old-key-file", "", "file holding the previous base64 master key")
	c.AddCommand(rotate)
	return c
}
```
In `internal/cli/root.go`, add `masterkeyCmd(&home),` to the `root.AddCommand(...)` list.

- [ ] **Step 4: Run the tests**

Run: `go test -race ./internal/core/ ./internal/cli/ && go vet ./...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/core internal/cli
git commit -m "feat: add qh masterkey rotate"
```

---

### Task 7: REST API: fields, TLS and the query log

**Files:**
- Create: `internal/httpapi/querylog.go`
- Modify: `internal/httpapi/connections.go`, `internal/httpapi/api.go` (route and key id), `internal/httpapi/openapi.json`
- Test: `internal/httpapi/details_test.go`

**Interfaces:**
- Consumes: from Task 4, `core.ConnInput` (fields and TLS), `core.ConnPatch` (`TLS`, `ClearTLS`) and `core.ConnInfo.TLS`; from Task 5, `core.WithKeyID`, `core.QueryLog`, `core.QueryLogFilter` and `core.ParseSince`; `engine.TLS` and `engine.TLSInfo`.
- Produces:
  - Request bodies for `POST /api/v1/connections` and `POST /api/v1/connections/test` accept `host`, `port`, `database`, `username`, `password`, `params` and `tls`.
  - `PATCH` accepts `tls`, where an object replaces the settings and `null` removes them.
  - Connection responses carry `username` (renamed from `user`) and `tls`, the summary.
  - `GET /api/v1/query-log?connection=&since=&limit=`, which needs the `manage` scope and is workspace-scoped.
  - Wire shapes that Task 9's client mirrors:
    - `connectionJSON`;
    - `queryLogJSON {id, connection, key_id, tool, query, rows, duration_ms, error_category, at}`.

- [ ] **Step 1: Write the failing tests**

`internal/httpapi/details_test.go`:
```go
package httpapi

import (
	"context"
	"strings"
	"testing"

	"github.com/anderson-0/qh/internal/auth"
	_ "github.com/anderson-0/qh/internal/engine/postgres"
	"github.com/anderson-0/qh/internal/testutil"
)

func TestConnectionFieldsAndTLS(t *testing.T) {
	e := newEnv(t, true, nil)
	k := e.key(t, "default", auth.ScopeQuery, auth.ScopeManage)
	f := testutil.NewTLSFixture(t)
	r := e.do(t, "POST", "/api/v1/connections?skip_test=true", k, map[string]any{
		"name": "pg", "engine": "postgres", "host": "db.example.com", "port": 5433, "database": "app",
		"username": "reader", "password": "hunter22-pw", "params": map[string]string{"application_name": "x"},
		"tls": map[string]any{"mode": "verify-full", "ca": f.CA, "cert": f.ClientCert, "key": f.ClientKey},
	})
	var c connectionJSON
	r.json(t, &c)
	if r.status != 201 || c.Username != "reader" || c.Host != "db.example.com" || c.Port != 5433 || c.Database != "app" ||
		c.TLS == nil || c.TLS.Mode != "verify-full" || !c.TLS.CA || !c.TLS.ClientCert || strings.Contains(r.body, "hunter22-pw") {
		t.Fatalf("create: %d %s", r.status, r.body)
	}
	r = e.do(t, "PATCH", "/api/v1/connections/pg?skip_test=true", k, `{"tls": null}`)
	r.json(t, &c)
	if r.status != 200 || c.TLS != nil || c.Version != 2 {
		t.Fatalf("clear tls: %d %s", r.status, r.body)
	}
	r = e.do(t, "PATCH", "/api/v1/connections/pg?skip_test=true", k, map[string]any{"tls": map[string]any{"mode": "require"}})
	r.json(t, &c)
	if r.status != 200 || c.TLS == nil || c.TLS.Mode != "require" || c.TLS.ClientCert {
		t.Fatalf("set tls: %d %s", r.status, r.body)
	}
	expectProblem(t, e.do(t, "PATCH", "/api/v1/connections/pg", k, map[string]any{"tls": map[string]any{"mode": "bogus"}}),
		400, "invalid", `unknown tls.mode "bogus"`)
	expectProblem(t, e.do(t, "PATCH", "/api/v1/connections/pg", k, `{"tls": {"mode": "require", "pin": "x"}}`),
		400, "invalid", "invalid JSON body")
	expectProblem(t, e.do(t, "POST", "/api/v1/connections", k, map[string]any{"name": "x", "engine": "postgres", "dsn": "postgres://h/db", "host": "h"}),
		400, "invalid", "give either dsn or host")
	if r := e.do(t, "POST", "/api/v1/connections/test", k, map[string]any{"engine": "sqlite", "database": testutil.SQLiteFixture(t)}); r.status != 200 {
		t.Fatalf("test with fields: %d %s", r.status, r.body)
	}
}

func TestTLSKeyNeverReturned(t *testing.T) {
	e := newEnv(t, true, nil)
	k := e.key(t, "default", auth.ScopeQuery, auth.ScopeManage)
	f := testutil.NewTLSFixture(t)
	body := map[string]any{"name": "pg", "engine": "postgres", "dsn": "postgres://u@db.example.com/app",
		"tls": map[string]any{"mode": "require", "cert": f.ClientCert, "key": f.ClientKey}}
	mismatched := map[string]any{"engine": "postgres", "dsn": "postgres://u@db.example.com/app",
		"tls": map[string]any{"mode": "require", "cert": f.ClientCert, "key": f.ServerKey}}
	bodies := []string{
		e.do(t, "POST", "/api/v1/connections?skip_test=true", k, body).body,
		e.do(t, "GET", "/api/v1/connections", k, nil).body,
		e.do(t, "GET", "/api/v1/connections/pg", k, nil).body,
		e.do(t, "POST", "/api/v1/connections?skip_test=true", k, body).body, // duplicate: an error response
		e.do(t, "POST", "/api/v1/connections/test", k, mismatched).body,     // invalid key pair: an error response
	}
	for i, b := range bodies {
		if strings.Contains(b, "PRIVATE KEY") || strings.Contains(b, "BEGIN CERTIFICATE") {
			t.Fatalf("response %d returns TLS material: %s", i, b)
		}
	}
}

func keyID(t *testing.T, e *env, token string) string {
	t.Helper()
	prefix, _, ok := auth.ParseToken(token)
	if !ok {
		t.Fatalf("bad token %q", token)
	}
	k, err := e.st.GetKeyByPrefix(context.Background(), prefix)
	if err != nil {
		t.Fatal(err)
	}
	return k.ID
}

func TestQueryLogEndpoint(t *testing.T) {
	e := newEnv(t, true, nil)
	m := e.key(t, "default", auth.ScopeQuery, auth.ScopeManage)
	q := e.key(t, "default", auth.ScopeQuery)
	if r := e.do(t, "POST", "/api/v1/connections", m, map[string]any{"name": "shop", "engine": "sqlite", "dsn": testutil.SQLiteFixture(t)}); r.status != 201 {
		t.Fatalf("create: %d %s", r.status, r.body)
	}
	e.do(t, "POST", "/api/v1/connections/shop/query", q, map[string]any{"query": "SELECT id FROM users"})
	e.do(t, "POST", "/api/v1/connections/shop/query", q, map[string]any{"query": "DELETE FROM users"})
	if out, isErr := mcpCall(t, e, q, "query", map[string]any{"source": "shop", "query": "SELECT 1 AS one"}); isErr {
		t.Fatalf("mcp query: %s", out)
	}
	type entry struct {
		Connection    string `json:"connection"`
		KeyID         string `json:"key_id"`
		Tool          string `json:"tool"`
		Query         string `json:"query"`
		Rows          int64  `json:"rows"`
		ErrorCategory string `json:"error_category"`
	}
	var log []entry
	r := e.do(t, "GET", "/api/v1/query-log", m, nil)
	r.json(t, &log)
	qid := keyID(t, e, q)
	if r.status != 200 || len(log) != 3 || log[0].Query != "SELECT 1 AS one" || log[0].KeyID != qid ||
		log[1].ErrorCategory != "read_only" || log[2].Rows != 3 || log[2].KeyID != qid || log[2].Connection != "shop" || log[2].Tool != "query" {
		t.Fatalf("log: %d %s", r.status, r.body)
	}
	for path, n := range map[string]int{"?limit=1": 1, "?connection=crm": 0, "?since=1h": 3, "?since=2000-01-01T00:00:00Z": 3} {
		var got []entry
		e.do(t, "GET", "/api/v1/query-log"+path, m, nil).json(t, &got)
		if len(got) != n {
			t.Errorf("%s: %d entries", path, len(got))
		}
	}
	expectProblem(t, e.do(t, "GET", "/api/v1/query-log?limit=x", m, nil), 400, "invalid", "limit must be a number")
	expectProblem(t, e.do(t, "GET", "/api/v1/query-log?limit=5000", m, nil), 400, "invalid", "limit must be between 1 and 1000")
	expectProblem(t, e.do(t, "GET", "/api/v1/query-log?since=yesterday", m, nil), 400, "invalid", "since must be an RFC 3339 time")
	expectProblem(t, e.do(t, "GET", "/api/v1/query-log", q, nil), 403, "forbidden", "this API key lacks the manage scope")
}
```

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./internal/httpapi/ -run 'Fields|TLSKey|QueryLogEndpoint'`
Expected: FAIL to build: `c.Username undefined`, `c.TLS undefined`.

- [ ] **Step 3: Implement**

In `internal/httpapi/connections.go`:

1. In `connectionJSON`, replace the `User` field with `Username`, and add `TLS` after `DSN`:
```go
	Username          string          `json:"username,omitempty"`
	DSN               string          `json:"dsn"`
	TLS               *engine.TLSInfo `json:"tls,omitempty"`
```
In `toConnection`, set `Username: c.User` (replacing `User: c.User`), and add `TLS: c.TLS`.

2. Replace `connectionInput` and its `core` method:
```go
type connectionInput struct {
	Name              string            `json:"name"`
	Engine            string            `json:"engine"`
	DSN               string            `json:"dsn,omitempty"`
	Host              string            `json:"host,omitempty"`
	Port              int               `json:"port,omitempty"`
	Database          string            `json:"database,omitempty"`
	Username          string            `json:"username,omitempty"`
	Password          string            `json:"password,omitempty"`
	Params            map[string]string `json:"params,omitempty"`
	TLS               *engine.TLS       `json:"tls,omitempty"`
	Description       string            `json:"description,omitempty"`
	Mode              string            `json:"mode,omitempty"`
	MaxRows           int               `json:"max_rows,omitempty"`
	TimeoutMS         int               `json:"timeout_ms,omitempty"`
	PoolSize          int               `json:"pool_size,omitempty"`
	AllowedNamespaces []string          `json:"allowed_namespaces,omitempty"`
}

func (in connectionInput) core() core.ConnInput {
	return core.ConnInput{Name: in.Name, Engine: in.Engine, DSN: in.DSN, Host: in.Host, Port: in.Port, Database: in.Database,
		Username: in.Username, Password: in.Password, Params: in.Params, TLS: in.TLS, Description: in.Description,
		Mode: engine.Mode(in.Mode), MaxRows: in.MaxRows, TimeoutMS: in.TimeoutMS, PoolSize: in.PoolSize,
		AllowedNamespaces: in.AllowedNamespaces}
}
```
3. Replace `connectionPatch` and its `core` method. `tls` is raw JSON so that an absent field and `null` mean different things. The new imports are `bytes`, `encoding/json` and `qerr`.
```go
type connectionPatch struct {
	DSN               *string         `json:"dsn"`
	Description       *string         `json:"description"`
	Mode              *string         `json:"mode"`
	MaxRows           *int            `json:"max_rows"`
	TimeoutMS         *int            `json:"timeout_ms"`
	PoolSize          *int            `json:"pool_size"`
	AllowedNamespaces *[]string       `json:"allowed_namespaces"`
	TLS               json.RawMessage `json:"tls"` // absent: unchanged; null: removed; object: replaced
}

func (p connectionPatch) core() (core.ConnPatch, error) {
	out := core.ConnPatch{DSN: p.DSN, Description: p.Description, MaxRows: p.MaxRows, TimeoutMS: p.TimeoutMS,
		PoolSize: p.PoolSize, AllowedNamespaces: p.AllowedNamespaces}
	if p.Mode != nil {
		m := engine.Mode(*p.Mode)
		out.Mode = &m
	}
	switch raw := bytes.TrimSpace(p.TLS); {
	case len(raw) == 0:
	case string(raw) == "null":
		out.ClearTLS = true
	default:
		var t engine.TLS
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&t); err != nil {
			return out, qerr.New(qerr.Invalid, "invalid JSON body: tls: "+err.Error())
		}
		out.TLS = &t
	}
	return out, nil
}
```
4. In `updateConnection`, use the new signature:
```go
	cp, err := p.core()
	if err != nil {
		a.fail(w, r, err)
		return
	}
	c, tr, err := a.cfg.Service.UpdateConnection(r.Context(), workspace(r), r.PathValue("name"), cp, !skipTest(r))
```

`internal/httpapi/querylog.go`:
```go
package httpapi

import (
	"net/http"
	"strconv"
	"time"

	"github.com/anderson-0/qh/internal/core"
	"github.com/anderson-0/qh/internal/qerr"
)

type queryLogJSON struct {
	ID            int64     `json:"id"`
	Connection    string    `json:"connection"`
	KeyID         string    `json:"key_id,omitempty"`
	Tool          string    `json:"tool"`
	Query         string    `json:"query"`
	Rows          int64     `json:"rows"`
	DurationMS    int64     `json:"duration_ms"`
	ErrorCategory string    `json:"error_category,omitempty"`
	At            time.Time `json:"at"`
}

func (a *api) queryLog(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := core.QueryLogFilter{Connection: q.Get("connection")}
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			a.fail(w, r, qerr.New(qerr.Invalid, "limit must be a number"))
			return
		}
		f.Limit = n
	}
	if v := q.Get("since"); v != "" {
		t, err := core.ParseSince(v, time.Now())
		if err != nil {
			a.fail(w, r, err)
			return
		}
		f.Since = t
	}
	entries, err := a.cfg.Service.QueryLog(r.Context(), workspace(r), f)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	out := make([]queryLogJSON, 0, len(entries))
	for _, e := range entries {
		out = append(out, queryLogJSON{ID: e.ID, Connection: e.Connection, KeyID: e.KeyID, Tool: e.Tool, Query: e.Query,
			Rows: e.Rows, DurationMS: e.Duration.Milliseconds(), ErrorCategory: e.ErrorCategory, At: e.At})
	}
	writeJSON(w, http.StatusOK, out)
}
```

In `internal/httpapi/api.go`:

1. In `routes()`, add this before the `/api/v1/workspaces` entries:
```go
		{"GET", "/api/v1/query-log", m, true, false, a.queryLog},
```
2. In `wrap`, directly after `ctx = context.WithValue(ctx, principalKey{}, p)`, add:
```go
		if a.cfg.Auth != nil {
			ctx = core.WithKeyID(ctx, p.KeyID) // the query log records which key ran each query
		}
```
The context reaches MCP tool calls too, because the go-sdk Streamable HTTP handler serves each request with its request context. If `TestQueryLogEndpoint`'s MCP entry comes back with an empty `key_id`, the SDK did not pass the context through. In that case, stop and report NEEDS_CONTEXT; do not work around it.

In `internal/httpapi/openapi.json`, make these changes:

1. Replace the `ConnectionInput` schema with:
```json
      "ConnectionInput": { "type": "object", "required": ["name", "engine"], "additionalProperties": false,
        "description": "Give dsn, or host (database for sqlite) with port, database, username, password and params; not both",
        "properties": {
        "name": { "type": "string", "pattern": "^[a-z0-9][a-z0-9_-]{0,63}$" }, "engine": { "type": "string", "enum": ["postgres", "mysql", "mariadb", "sqlite"] },
        "dsn": { "type": "string" }, "host": { "type": "string" }, "port": { "type": "integer" }, "database": { "type": "string" },
        "username": { "type": "string" }, "password": { "type": "string" },
        "params": { "type": "object", "additionalProperties": { "type": "string" } },
        "tls": { "$ref": "#/components/schemas/TLS" },
        "description": { "type": "string" }, "mode": { "type": "string", "enum": ["ro", "rw"] },
        "max_rows": { "type": "integer" }, "timeout_ms": { "type": "integer" }, "pool_size": { "type": "integer" },
        "allowed_namespaces": { "type": "array", "items": { "type": "string" } } } },
```
2. In `ConnectionPatch`, add this property:
```json
        "tls": { "oneOf": [{ "$ref": "#/components/schemas/TLS" }, { "type": "null" }], "description": "An object replaces the TLS settings; null removes them" },
```
3. In `Connection`, replace `"user": { "type": "string" }` with `"username": { "type": "string" }`, and add:
```json
        "tls": { "$ref": "#/components/schemas/TLSInfo" },
```
4. After the `TestResult` schema, add:
```json
      "TLS": { "type": "object", "required": ["mode"], "additionalProperties": false, "properties": {
        "mode": { "type": "string", "enum": ["disable", "require", "verify-ca", "verify-full"] },
        "ca": { "type": "string", "description": "PEM CA certificates to trust instead of the system roots" },
        "cert": { "type": "string", "description": "PEM client certificate" },
        "key": { "type": "string", "description": "PEM private key for cert; stored sealed and never returned" },
        "server_name": { "type": "string", "description": "Name the server certificate must carry (verify-full)" } } },
      "TLSInfo": { "type": "object", "properties": {
        "mode": { "type": "string" }, "ca": { "type": "boolean" }, "client_cert": { "type": "boolean" }, "server_name": { "type": "string" } } },
      "QueryLogEntry": { "type": "object", "properties": {
        "id": { "type": "integer" }, "connection": { "type": "string" }, "key_id": { "type": "string" },
        "tool": { "type": "string", "enum": ["query", "explain"] }, "query": { "type": "string", "description": "Cut to 8 KiB" },
        "rows": { "type": "integer" }, "duration_ms": { "type": "integer" }, "error_category": { "type": "string" },
        "at": { "type": "string", "format": "date-time" } } },
```
5. In `paths`, before `"/api/v1/workspaces"`, add:
```json
    "/api/v1/query-log": { "get": { "summary": "Logged query calls, newest first; scope manage",
      "parameters": [{ "$ref": "#/components/parameters/workspaceHeader" },
        { "name": "connection", "in": "query", "schema": { "type": "string" } },
        { "name": "since", "in": "query", "schema": { "type": "string" }, "description": "RFC 3339 time, or a duration back from now such as 24h or 7d" },
        { "name": "limit", "in": "query", "schema": { "type": "integer", "minimum": 1, "maximum": 1000, "default": 100 } }],
      "responses": { "200": { "description": "Entries", "content": { "application/json": { "schema": { "type": "array", "items": { "$ref": "#/components/schemas/QueryLogEntry" } } } } },
        "default": { "$ref": "#/components/responses/problem" } } } },
```

- [ ] **Step 4: Run the tests**

Run: `go test -race ./internal/httpapi/ && go vet ./...`
Expected: PASS for every httpapi test. That includes `TestOpenAPIMatchesRoutes`, which now sees `GET /api/v1/query-log` both in the route table and in the document. Update any earlier httpapi test that reads the JSON field `user`.

- [ ] **Step 5: Commit**

```bash
git add internal/httpapi
git commit -m "feat(httpapi): accept connection fields and TLS settings, and serve the query log"
```

---

### Task 8: CLI: a backend interface, field and TLS flags, `qh log`, query-log flags

**Files:**
- Create: `internal/cli/backend.go`, `internal/cli/log.go`
- Replace: `internal/cli/root.go`, `internal/cli/conn.go`, `internal/cli/query.go`, `internal/cli/admin.go`
- Modify: `internal/cli/serve.go` (query-log flags, pruning, refuse `--server`), `internal/cli/masterkey.go` (refuse `--server`)
- Test: `internal/cli/details_test.go`

**Interfaces:**
- Consumes:
  - from Task 4: `core.ConnInput` (fields and TLS), `core.ConnPatch` (`TLS`, `ClearTLS`) and `core.ConnInfo.TLS`;
  - from Task 5: `core.Options.NoQueryLog`, `QueryLog`, `PruneQueryLog`, `ParseSince` and `QueryLogFilter`;
  - from Task 6: `masterkeyCmd(home *string)` and `openAppWith(home, opts, oldKeys...)`.
- Produces, for Task 9:
  - The `backend` interface and `localBackend`.
  - The type `withBackend func(fn func(cmd *cobra.Command, b backend, args []string) error) func(*cobra.Command, []string) error`.
  - `workspaceOf(cmd) (name string, explicit bool)`.
  - `refuseRemote(cmd) error`.
  - Persistent flags `--home` and `--workspace` (default `default`, or `$QH_WORKSPACE`).
  - `qh log`, plus `--no-query-log` and `--query-log-retention` on `serve` and `mcp`.

The `backend` interface (Task 9's `client.Client` implements it too):
```go
type backend interface {
	ListConnections(ctx context.Context) ([]*core.ConnInfo, error)
	GetConnection(ctx context.Context, name string) (*core.ConnInfo, error)
	CreateConnection(ctx context.Context, in core.ConnInput, test bool) (*core.ConnInfo, *core.TestResult, error)
	UpdateConnection(ctx context.Context, name string, p core.ConnPatch, test bool) (*core.ConnInfo, *core.TestResult, error)
	DeleteConnection(ctx context.Context, name string) error
	TestConnection(ctx context.Context, name string) (*core.TestResult, error)
	Query(ctx context.Context, in core.QueryInput) (string, error)
	Schema(ctx context.Context, in core.SchemaInput) (string, error)
	QueryLog(ctx context.Context, f core.QueryLogFilter) ([]*core.QueryLogEntry, error)
	CreateWorkspace(ctx context.Context, name string) (*core.WorkspaceInfo, error)
	ListWorkspaces(ctx context.Context) ([]*core.WorkspaceInfo, error)
	DeleteWorkspace(ctx context.Context, name string) error
	CreateKey(ctx context.Context, in auth.KeyInput) (string, *auth.KeyInfo, error)
	ListKeys(ctx context.Context) ([]*auth.KeyInfo, error)
	DeleteKey(ctx context.Context, id string) error
}
```

- [ ] **Step 1: Write the failing tests**

`internal/cli/details_test.go`:
```go
package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/anderson-0/qh/internal/testutil"
)

func TestConnFieldAndTLSFlags(t *testing.T) {
	home := t.TempDir()
	f := testutil.NewTLSFixture(t)
	ca := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(ca, []byte(f.CA), 0o600); err != nil {
		t.Fatal(err)
	}
	out, errOut, code := run(t, home, "s3cret-pw\n", "conn", "add", "pg", "--engine", "postgres", "--host", "db.example.com",
		"--port", "5433", "--database", "app", "--user", "reader", "--password-stdin", "--param", "application_name=qh-cli",
		"--tls-mode", "verify-full", "--tls-ca", ca, "--skip-test")
	if code != 0 || out != "added pg (postgres, ro)\n" {
		t.Fatalf("add: %d %q %q", code, out, errOut)
	}
	out, _, _ = run(t, home, "", "conn", "show", "pg")
	if !strings.Contains(out, "dsn: postgres://reader:***@db.example.com:5433/app?application_name=qh-cli\n") ||
		!strings.Contains(out, "tls: verify-full (ca)\n") || strings.Contains(out, "s3cret-pw") {
		t.Fatalf("show: %q", out)
	}
	if out, _, _ = run(t, home, "", "conn", "edit", "pg", "--no-tls", "--skip-test"); out != "updated pg (version 2)\n" {
		t.Fatalf("edit --no-tls: %q", out)
	}
	if out, _, _ = run(t, home, "", "conn", "show", "pg"); !strings.Contains(out, "tls: none") {
		t.Fatalf("show after --no-tls: %q", out)
	}
	if _, errOut, code := run(t, home, "", "conn", "edit", "pg", "--tls-ca", ca); code != 1 || !strings.HasPrefix(errOut, "error[invalid]: --tls-ca needs --tls-mode") {
		t.Fatalf("tls flag without mode: %d %q", code, errOut)
	}
	if _, errOut, code := run(t, home, "x\n", "conn", "add", "b", "--engine", "postgres", "--dsn", "-", "--password-stdin"); code != 1 ||
		!strings.HasPrefix(errOut, "error[invalid]: --dsn - and --password-stdin cannot both read stdin") {
		t.Fatalf("two stdin readers: %d %q", code, errOut)
	}
	if _, errOut, code := run(t, home, "", "conn", "add", "c", "--engine", "postgres", "--dsn", "postgres://h/db", "--host", "h"); code != 1 ||
		!strings.HasPrefix(errOut, "error[invalid]: give either dsn or host") {
		t.Fatalf("dsn and host: %d %q", code, errOut)
	}
}

func TestQueryLogCommand(t *testing.T) {
	home, db := t.TempDir(), testutil.SQLiteFixture(t)
	run(t, home, "", "conn", "add", "shop", "--engine", "sqlite", "--dsn", db)
	run(t, home, "", "query", "shop", "SELECT id\nFROM users")
	run(t, home, "", "query", "shop", "DELETE FROM users")
	out, errOut, code := run(t, home, "", "log")
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if code != 0 || len(lines) != 3 || !strings.HasPrefix(lines[0], "AT") || !strings.Contains(lines[1], "read_only") ||
		!strings.HasSuffix(lines[2], "SELECT id FROM users") {
		t.Fatalf("log: %d %q %q", code, out, errOut)
	}
	if out, _, _ := run(t, home, "", "log", "--limit", "1"); strings.Count(out, "\n") != 2 {
		t.Fatalf("limit: %q", out)
	}
	if out, _, _ := run(t, home, "", "log", "--connection", "crm"); out != "no logged queries\n" {
		t.Fatalf("filter: %q", out)
	}
	if _, errOut, code := run(t, home, "", "log", "--since", "yesterday"); code != 1 || !strings.HasPrefix(errOut, "error[invalid]: since must be") {
		t.Fatalf("since: %d %q", code, errOut)
	}
}

func TestQueryLogFlags(t *testing.T) {
	home := t.TempDir()
	for _, args := range [][]string{{"mcp", "--query-log-retention", "0"}, {"serve", "--query-log-retention", "soon"}} {
		if _, errOut, code := run(t, home, "", args...); code != 1 ||
			!strings.HasPrefix(errOut, "error[invalid]: --query-log-retention must be a positive duration such as 30d or 72h") {
			t.Fatalf("%v: %d %q", args, code, errOut)
		}
	}
}

func TestWorkspaceFlag(t *testing.T) {
	home, db := t.TempDir(), testutil.SQLiteFixture(t)
	run(t, home, "", "workspace", "create", "team")
	if out, errOut, code := run(t, home, "", "--workspace", "team", "conn", "add", "shop", "--engine", "sqlite", "--dsn", db); code != 0 {
		t.Fatalf("add in team: %q %q", out, errOut)
	}
	if out, _, _ := run(t, home, "", "conn", "ls"); !strings.HasPrefix(out, "no connections") {
		t.Fatalf("default workspace: %q", out)
	}
	if out, _, _ := run(t, home, "", "--workspace", "team", "conn", "ls"); !strings.Contains(out, "shop") {
		t.Fatalf("team: %q", out)
	}
	t.Setenv("QH_WORKSPACE", "team")
	if out, _, _ := run(t, home, "", "query", "shop", "SELECT 1 AS one"); !strings.HasPrefix(out, "one\n1\n") {
		t.Fatalf("QH_WORKSPACE: %q", out)
	}
}
```

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./internal/cli/ -run 'FieldAndTLS|QueryLogCommand|QueryLogFlags|WorkspaceFlag'`
Expected: FAIL: `unknown flag: --host`, `unknown command "log"`, `unknown flag: --query-log-retention`, `unknown flag: --workspace`.

- [ ] **Step 3: Implement**

`internal/cli/backend.go`:
```go
package cli

import (
	"context"

	"github.com/anderson-0/qh/internal/auth"
	"github.com/anderson-0/qh/internal/core"
)

// backend is what the connection, query, log, workspace and key commands
// act on: the local store (localBackend), or a qh server (client.Client).
type backend interface {
	ListConnections(ctx context.Context) ([]*core.ConnInfo, error)
	GetConnection(ctx context.Context, name string) (*core.ConnInfo, error)
	CreateConnection(ctx context.Context, in core.ConnInput, test bool) (*core.ConnInfo, *core.TestResult, error)
	UpdateConnection(ctx context.Context, name string, p core.ConnPatch, test bool) (*core.ConnInfo, *core.TestResult, error)
	DeleteConnection(ctx context.Context, name string) error
	TestConnection(ctx context.Context, name string) (*core.TestResult, error)
	Query(ctx context.Context, in core.QueryInput) (string, error)
	Schema(ctx context.Context, in core.SchemaInput) (string, error)
	QueryLog(ctx context.Context, f core.QueryLogFilter) ([]*core.QueryLogEntry, error)
	CreateWorkspace(ctx context.Context, name string) (*core.WorkspaceInfo, error)
	ListWorkspaces(ctx context.Context) ([]*core.WorkspaceInfo, error)
	DeleteWorkspace(ctx context.Context, name string) error
	CreateKey(ctx context.Context, in auth.KeyInput) (string, *auth.KeyInfo, error)
	ListKeys(ctx context.Context) ([]*auth.KeyInfo, error)
	DeleteKey(ctx context.Context, id string) error
}

// localBackend works on the local store, in one workspace.
type localBackend struct {
	a  *app
	ws string
}

func (b *localBackend) ListConnections(ctx context.Context) ([]*core.ConnInfo, error) {
	return b.a.svc.ListConnections(ctx, b.ws)
}

func (b *localBackend) GetConnection(ctx context.Context, name string) (*core.ConnInfo, error) {
	return b.a.svc.GetConnection(ctx, b.ws, name)
}

func (b *localBackend) CreateConnection(ctx context.Context, in core.ConnInput, test bool) (*core.ConnInfo, *core.TestResult, error) {
	return b.a.svc.CreateConnection(ctx, b.ws, in, test)
}

func (b *localBackend) UpdateConnection(ctx context.Context, name string, p core.ConnPatch, test bool) (*core.ConnInfo, *core.TestResult, error) {
	return b.a.svc.UpdateConnection(ctx, b.ws, name, p, test)
}

func (b *localBackend) DeleteConnection(ctx context.Context, name string) error {
	return b.a.svc.DeleteConnection(ctx, b.ws, name)
}

func (b *localBackend) TestConnection(ctx context.Context, name string) (*core.TestResult, error) {
	return b.a.svc.TestConnection(ctx, b.ws, name)
}

func (b *localBackend) Query(ctx context.Context, in core.QueryInput) (string, error) {
	return b.a.svc.Query(ctx, b.ws, in)
}

func (b *localBackend) Schema(ctx context.Context, in core.SchemaInput) (string, error) {
	return b.a.svc.Schema(ctx, b.ws, in)
}

func (b *localBackend) QueryLog(ctx context.Context, f core.QueryLogFilter) ([]*core.QueryLogEntry, error) {
	return b.a.svc.QueryLog(ctx, b.ws, f)
}

func (b *localBackend) CreateWorkspace(ctx context.Context, name string) (*core.WorkspaceInfo, error) {
	return b.a.svc.CreateWorkspace(ctx, name)
}

func (b *localBackend) ListWorkspaces(ctx context.Context) ([]*core.WorkspaceInfo, error) {
	return b.a.svc.ListWorkspaces(ctx)
}

func (b *localBackend) DeleteWorkspace(ctx context.Context, name string) error {
	return b.a.svc.DeleteWorkspace(ctx, name)
}

func (b *localBackend) CreateKey(ctx context.Context, in auth.KeyInput) (string, *auth.KeyInfo, error) {
	return auth.CreateKey(ctx, b.a.st, in)
}

func (b *localBackend) ListKeys(ctx context.Context) ([]*auth.KeyInfo, error) {
	return auth.ListKeys(ctx, b.a.st, "", true)
}

func (b *localBackend) DeleteKey(ctx context.Context, id string) error {
	return auth.DeleteKey(ctx, b.a.st, id)
}
```

`internal/cli/root.go` (replace the file):
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

	"github.com/anderson-0/qh/internal/qerr"
	"github.com/anderson-0/qh/internal/store"
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

// withBackend wraps a command so it runs against a backend.
type withBackend func(fn func(cmd *cobra.Command, b backend, args []string) error) func(*cobra.Command, []string) error

// workspaceOf returns the workspace a command acts on, and whether the user
// named it (--workspace or $QH_WORKSPACE) rather than taking the default.
func workspaceOf(cmd *cobra.Command) (string, bool) {
	ws, _ := cmd.Flags().GetString("workspace")
	if cmd.Flags().Changed("workspace") {
		return ws, true
	}
	if v := os.Getenv("QH_WORKSPACE"); v != "" {
		return v, true
	}
	return ws, false
}

// refuseRemote stops a local-only command from ignoring --server.
func refuseRemote(cmd *cobra.Command) error {
	if s, _ := cmd.Flags().GetString("server"); s != "" {
		return qerr.New(qerr.Invalid, cmd.CommandPath()+" runs locally only").WithHint("Remove --server")
	}
	return nil
}

func newRoot(version string) *cobra.Command {
	var home string
	root := &cobra.Command{
		Use:           "qh",
		Short:         "Token-efficient database server for AI agents",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	pf := root.PersistentFlags()
	pf.StringVar(&home, "home", "", "data directory (default $QH_HOME, else $XDG_CONFIG_HOME/qh or ~/.config/qh)")
	pf.String("workspace", store.DefaultWorkspace, "workspace to act on (default $QH_WORKSPACE, else default)")
	pf.String("server", "", "") // defined here so local-only commands can refuse it; Task 9 gives it meaning
	pf.MarkHidden("server")
	with := func(fn func(cmd *cobra.Command, b backend, args []string) error) func(*cobra.Command, []string) error {
		return func(cmd *cobra.Command, args []string) error {
			ws, _ := workspaceOf(cmd)
			dir, err := homeDir(home)
			if err != nil {
				return err
			}
			a, err := openApp(dir)
			if err != nil {
				return err
			}
			defer a.Close()
			return fn(cmd, &localBackend{a: a, ws: ws}, args)
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
		mcpCmd(&home, version),
		connCmd(with),
		queryCmd(with),
		schemaCmd(with),
		logCmd(with),
		serveCmd(&home, version),
		workspaceCmd(with),
		keyCmd(with),
		masterkeyCmd(&home),
	)
	return root
}
```

`internal/cli/conn.go` (replace the file):
```go
package cli

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/anderson-0/qh/internal/core"
	"github.com/anderson-0/qh/internal/engine"
	"github.com/anderson-0/qh/internal/qerr"
)

func connCmd(with withBackend) *cobra.Command {
	c := &cobra.Command{Use: "conn", Short: "Manage database connections"}
	c.AddCommand(connAddCmd(with), connListCmd(with), connShowCmd(with), connEditCmd(with), connRemoveCmd(with), connTestCmd(with))
	return c
}

type connFlags struct {
	engine, dsn, mode, description string
	host, database, user           string
	port                           int
	passwordStdin                  bool
	params                         map[string]string
	maxRows, poolSize              int
	timeout                        time.Duration
	namespaces                     []string
	skipTest                       bool
	tlsMode, tlsCA, tlsCert        string
	tlsKey, tlsServerName          string
	noTLS                          bool
}

func (f *connFlags) register(cmd *cobra.Command, create bool) {
	fl := cmd.Flags()
	if create {
		fl.StringVar(&f.engine, "engine", "", "postgres, mysql (alias mariadb) or sqlite (required)")
		fl.StringVar(&f.host, "host", "", "database host, instead of --dsn")
		fl.IntVar(&f.port, "port", 0, "database port (default: the engine's)")
		fl.StringVar(&f.database, "database", "", "database name, or the file path for sqlite")
		fl.StringVar(&f.user, "user", "", "database user")
		fl.BoolVar(&f.passwordStdin, "password-stdin", false, "read the password from the first line of stdin")
		fl.StringToStringVar(&f.params, "param", nil, "extra connection setting key=value (repeatable)")
	} else {
		fl.BoolVar(&f.noTLS, "no-tls", false, "remove the TLS settings")
	}
	fl.StringVar(&f.dsn, "dsn", "", "connection string, or - to read it from stdin")
	fl.StringVar(&f.mode, "mode", "ro", "ro (read-only) or rw (read-write)")
	fl.StringVar(&f.description, "description", "", "what this database holds; shown to agents")
	fl.IntVar(&f.maxRows, "max-rows", 0, "row cap per query (default 100, max 10000)")
	fl.DurationVar(&f.timeout, "timeout", 0, "query timeout (default 30s, max 5m)")
	fl.IntVar(&f.poolSize, "pool-size", 0, "open database connections (default 5, max 50)")
	fl.StringSliceVar(&f.namespaces, "namespaces", nil, "only show these schemas to agents (comma-separated)")
	fl.BoolVar(&f.skipTest, "skip-test", false, "save without connecting first")
	fl.StringVar(&f.tlsMode, "tls-mode", "", "disable, require, verify-ca or verify-full; replaces TLS settings in the connection string")
	fl.StringVar(&f.tlsCA, "tls-ca", "", "file with PEM CA certificates to trust")
	fl.StringVar(&f.tlsCert, "tls-cert", "", "file with a PEM client certificate")
	fl.StringVar(&f.tlsKey, "tls-key", "", "file with the PEM private key for --tls-cert")
	fl.StringVar(&f.tlsServerName, "tls-server-name", "", "name the server certificate must carry (verify-full)")
}

// tls returns the TLS settings the flags describe, or nil when --tls-mode is absent.
func (f *connFlags) tls(cmd *cobra.Command) (*engine.TLS, error) {
	fl := cmd.Flags()
	if !fl.Changed("tls-mode") {
		for _, n := range []string{"tls-ca", "tls-cert", "tls-key", "tls-server-name"} {
			if fl.Changed(n) {
				return nil, qerr.New(qerr.Invalid, "--"+n+" needs --tls-mode")
			}
		}
		return nil, nil
	}
	t := &engine.TLS{Mode: f.tlsMode, ServerName: f.tlsServerName}
	for _, file := range []struct {
		path string
		dst  *string
	}{{f.tlsCA, &t.CA}, {f.tlsCert, &t.Cert}, {f.tlsKey, &t.Key}} {
		if file.path == "" {
			continue
		}
		b, err := os.ReadFile(file.path)
		if err != nil {
			return nil, qerr.New(qerr.Invalid, "cannot read "+file.path)
		}
		*file.dst = string(b)
	}
	return t, nil
}

// readLine returns the first line of stdin.
func readLine(cmd *cobra.Command) (string, error) {
	line, err := bufio.NewReader(cmd.InOrStdin()).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	return strings.TrimSpace(line), nil
}

// readDSN returns v, or the first line of stdin when v is "-" (keeps passwords out of shell history).
func readDSN(cmd *cobra.Command, v string) (string, error) {
	if v != "-" {
		return v, nil
	}
	return readLine(cmd)
}

func connAddCmd(with withBackend) *cobra.Command {
	var f connFlags
	cmd := &cobra.Command{
		Use:   "add <name>",
		Short: "Add a connection (tests it first unless --skip-test)",
		Args:  cobra.ExactArgs(1),
		RunE: with(func(cmd *cobra.Command, b backend, args []string) error {
			if f.engine == "" {
				return qerr.New(qerr.Invalid, "--engine is required")
			}
			if f.dsn == "" && f.host == "" && f.database == "" {
				return qerr.New(qerr.Invalid, "--dsn is required").
					WithHint("Or give --host (--database for sqlite) with --port, --database, --user, --password-stdin and --param")
			}
			if f.dsn == "-" && f.passwordStdin {
				return qerr.New(qerr.Invalid, "--dsn - and --password-stdin cannot both read stdin")
			}
			in := core.ConnInput{
				Name: args[0], Engine: f.engine, Host: f.host, Port: f.port, Database: f.database, Username: f.user,
				Params: f.params, Description: f.description, Mode: engine.Mode(f.mode), MaxRows: f.maxRows,
				TimeoutMS: int(f.timeout.Milliseconds()), PoolSize: f.poolSize, AllowedNamespaces: f.namespaces,
			}
			var err error
			if f.dsn != "" {
				if in.DSN, err = readDSN(cmd, f.dsn); err != nil {
					return err
				}
			}
			if f.passwordStdin {
				if in.Password, err = readLine(cmd); err != nil {
					return err
				}
			}
			if in.TLS, err = f.tls(cmd); err != nil {
				return err
			}
			info, tr, err := b.CreateConnection(cmd.Context(), in, !f.skipTest)
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

func tlsSummary(t *engine.TLSInfo) string {
	if t == nil {
		return "none (the connection string's settings apply)"
	}
	var parts []string
	if t.CA {
		parts = append(parts, "ca")
	}
	if t.ClientCert {
		parts = append(parts, "client cert")
	}
	if t.ServerName != "" {
		parts = append(parts, "server name "+t.ServerName)
	}
	if len(parts) == 0 {
		return t.Mode
	}
	return t.Mode + " (" + strings.Join(parts, ", ") + ")"
}

func connListCmd(with withBackend) *cobra.Command {
	return &cobra.Command{
		Use:     "ls",
		Aliases: []string{"list"},
		Short:   "List connections",
		Args:    cobra.NoArgs,
		RunE: with(func(cmd *cobra.Command, b backend, _ []string) error {
			list, err := b.ListConnections(cmd.Context())
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

func connShowCmd(with withBackend) *cobra.Command {
	return &cobra.Command{
		Use:   "show <name>",
		Short: "Show a connection (password hidden)",
		Args:  cobra.ExactArgs(1),
		RunE: with(func(cmd *cobra.Command, b backend, args []string) error {
			c, err := b.GetConnection(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			ns := "(all)"
			if len(c.AllowedNamespaces) > 0 {
				ns = strings.Join(c.AllowedNamespaces, ", ")
			}
			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "name: %s\nengine: %s\nmode: %s\ndsn: %s\ntls: %s\n", c.Name, c.Engine, c.Mode, c.DSN, tlsSummary(c.TLS))
			fmt.Fprintf(out, "description: %s\nmax_rows: %d\ntimeout: %s\npool_size: %d\nnamespaces: %s\n",
				c.Description, c.MaxRows, time.Duration(c.TimeoutMS)*time.Millisecond, c.PoolSize, ns)
			fmt.Fprintf(out, "version: %d\ncreated: %s\nupdated: %s\n", c.Version,
				c.CreatedAt.Format(time.RFC3339), c.UpdatedAt.Format(time.RFC3339))
			return nil
		}),
	}
}

func connEditCmd(with withBackend) *cobra.Command {
	var f connFlags
	cmd := &cobra.Command{
		Use:   "edit <name>",
		Short: "Change a connection; only the flags you pass are changed",
		Args:  cobra.ExactArgs(1),
		RunE: with(func(cmd *cobra.Command, b backend, args []string) error {
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
			p.ClearTLS = f.noTLS
			var err error
			if p.TLS, err = f.tls(cmd); err != nil {
				return err
			}
			info, _, err := b.UpdateConnection(cmd.Context(), args[0], p, !f.skipTest)
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

func connRemoveCmd(with withBackend) *cobra.Command {
	return &cobra.Command{
		Use:     "rm <name>",
		Aliases: []string{"remove"},
		Short:   "Remove a connection",
		Args:    cobra.ExactArgs(1),
		RunE: with(func(cmd *cobra.Command, b backend, args []string) error {
			if err := b.DeleteConnection(cmd.Context(), args[0]); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "removed %s\n", args[0])
			return nil
		}),
	}
}

func connTestCmd(with withBackend) *cobra.Command {
	return &cobra.Command{
		Use:   "test <name>",
		Short: "Connect with a stored connection's settings",
		Args:  cobra.ExactArgs(1),
		RunE: with(func(cmd *cobra.Command, b backend, args []string) error {
			tr, err := b.TestConnection(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "ok · %s · %dms\n", tr.ServerVersion, tr.Connect.Milliseconds())
			return nil
		}),
	}
}
```

`internal/cli/query.go` (replace the file):
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
)

func queryCmd(with withBackend) *cobra.Command {
	var maxRows int
	var explain bool
	cmd := &cobra.Command{
		Use:   "query <connection> <statement|->",
		Short: "Run one statement and print TSV (- reads the statement from stdin)",
		Args:  cobra.ExactArgs(2),
		RunE: with(func(cmd *cobra.Command, b backend, args []string) error {
			q := args[1]
			if q == "-" {
				raw, err := io.ReadAll(cmd.InOrStdin())
				if err != nil {
					return err
				}
				q = string(raw)
			}
			out, err := b.Query(cmd.Context(), core.QueryInput{Source: args[0], Query: q, MaxRows: maxRows, Explain: explain})
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

func schemaCmd(with withBackend) *cobra.Command {
	var object string
	cmd := &cobra.Command{
		Use:   "schema <connection> [pattern]",
		Short: "Show the schema overview, or tables matching a pattern",
		Args:  cobra.RangeArgs(1, 2),
		RunE: with(func(cmd *cobra.Command, b backend, args []string) error {
			in := core.SchemaInput{Source: args[0], Object: object}
			if len(args) == 2 {
				in.Pattern = args[1]
			}
			out, err := b.Schema(cmd.Context(), in)
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

func mcpCmd(home *string, version string) *cobra.Command {
	var lf queryLogFlags
	cmd := &cobra.Command{
		Use:   "mcp",
		Short: "Serve MCP over stdio (local store)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := refuseRemote(cmd); err != nil {
				return err
			}
			retention, err := lf.retention()
			if err != nil {
				return err
			}
			dir, err := homeDir(*home)
			if err != nil {
				return err
			}
			a, err := openAppWith(dir, core.Options{NoQueryLog: lf.off})
			if err != nil {
				return err
			}
			defer a.Close()
			ctx := cmd.Context()
			a.pools.Start(ctx)
			if !lf.off {
				pruneQueryLog(ctx, a.svc, retention)
			}
			ws, _ := workspaceOf(cmd)
			srv := mcpserver.New(a.svc, func(context.Context) string { return ws }, version)
			err = srv.Run(ctx, &mcp.StdioTransport{})
			if errors.Is(err, context.Canceled) {
				return nil
			}
			return err
		},
	}
	lf.register(cmd)
	return cmd
}
```

`internal/cli/log.go`:
```go
package cli

import (
	"fmt"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/anderson-0/qh/internal/core"
)

func logCmd(with withBackend) *cobra.Command {
	var connection, since string
	var limit int
	cmd := &cobra.Command{
		Use:   "log",
		Short: "Show logged query calls, newest first",
		Args:  cobra.NoArgs,
		RunE: with(func(cmd *cobra.Command, b backend, _ []string) error {
			f := core.QueryLogFilter{Connection: connection, Limit: limit}
			if since != "" {
				t, err := core.ParseSince(since, time.Now())
				if err != nil {
					return err
				}
				f.Since = t
			}
			entries, err := b.QueryLog(cmd.Context(), f)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if len(entries) == 0 {
				fmt.Fprintln(out, "no logged queries")
				return nil
			}
			w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
			fmt.Fprintln(w, "AT\tCONNECTION\tTOOL\tROWS\tMS\tERROR\tKEY\tQUERY")
			dash := func(s string) string {
				if s == "" {
					return "-"
				}
				return s
			}
			for _, e := range entries {
				fmt.Fprintf(w, "%s\t%s\t%s\t%d\t%d\t%s\t%s\t%s\n", e.At.Local().Format(time.RFC3339), e.Connection, e.Tool,
					e.Rows, e.Duration.Milliseconds(), dash(e.ErrorCategory), dash(e.KeyID), strings.Join(strings.Fields(e.Query), " "))
			}
			return w.Flush()
		}),
	}
	cmd.Flags().StringVar(&connection, "connection", "", "only this connection")
	cmd.Flags().StringVar(&since, "since", "", "only calls since this RFC 3339 time, or this long ago (24h, 7d)")
	cmd.Flags().IntVar(&limit, "limit", 0, "at most this many entries (default 100, max 1000)")
	return cmd
}
```

`internal/cli/admin.go` (replace the file):
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

func workspaceCmd(with withBackend) *cobra.Command {
	c := &cobra.Command{Use: "workspace", Short: "Manage workspaces"}
	c.AddCommand(
		&cobra.Command{Use: "create <name>", Short: "Create a workspace", Args: cobra.ExactArgs(1),
			RunE: with(func(cmd *cobra.Command, b backend, args []string) error {
				if _, err := b.CreateWorkspace(cmd.Context(), args[0]); err != nil {
					return err
				}
				fmt.Fprintf(cmd.OutOrStdout(), "created workspace %s\n", args[0])
				return nil
			})},
		&cobra.Command{Use: "ls", Aliases: []string{"list"}, Short: "List workspaces", Args: cobra.NoArgs,
			RunE: with(func(cmd *cobra.Command, b backend, _ []string) error {
				list, err := b.ListWorkspaces(cmd.Context())
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
			RunE: with(func(cmd *cobra.Command, b backend, args []string) error {
				if err := b.DeleteWorkspace(cmd.Context(), args[0]); err != nil {
					return err
				}
				fmt.Fprintf(cmd.OutOrStdout(), "deleted workspace %s\n", args[0])
				return nil
			})},
	)
	return c
}

func keyCmd(with withBackend) *cobra.Command {
	c := &cobra.Command{Use: "key", Short: "Manage API keys for server mode"}
	var name, expires string
	var scopes []string
	create := &cobra.Command{Use: "create", Short: "Create an API key (shown once) in --workspace", Args: cobra.NoArgs,
		RunE: with(func(cmd *cobra.Command, b backend, _ []string) error {
			exp, err := auth.ParseExpiry(expires)
			if err != nil {
				return err
			}
			ws, explicit := workspaceOf(cmd)
			if slices.Contains(scopes, auth.ScopeAdmin) && !explicit {
				ws = "" // admin keys belong to no workspace unless one is named
			}
			tok, info, err := b.CreateKey(cmd.Context(), auth.KeyInput{Workspace: ws, Scopes: scopes, Name: name, ExpiresIn: exp})
			if err != nil {
				return err
			}
			owner := "workspace " + info.Workspace
			if info.Workspace == "" {
				owner = "admin key"
			}
			fmt.Fprintf(cmd.OutOrStdout(), "created key %s (%s, %s)\n%s\nStore it now; it will not be shown again.\n",
				info.ID, strings.Join(info.Scopes, ","), owner, tok)
			return nil
		})}
	create.Flags().StringSliceVar(&scopes, "scopes", []string{auth.ScopeQuery}, "query, manage and/or admin")
	create.Flags().StringVar(&name, "name", "", "label for the key")
	create.Flags().StringVar(&expires, "expires", "", "lifetime such as 90d or 720h (default: never)")
	c.AddCommand(create,
		&cobra.Command{Use: "ls", Aliases: []string{"list"}, Short: "List API keys", Args: cobra.NoArgs,
			RunE: with(func(cmd *cobra.Command, b backend, _ []string) error {
				list, err := b.ListKeys(cmd.Context())
				if err != nil {
					return err
				}
				w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
				fmt.Fprintln(w, "ID\tPREFIX\tWORKSPACE\tSCOPES\tNAME\tLAST USED\tEXPIRES\tCREATED BY")
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
					by := k.CreatedBy
					if by == "" {
						by = "-"
					}
					fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", k.ID, k.Prefix, ws, strings.Join(k.Scopes, ","), k.Name, when(k.LastUsedAt), when(k.ExpiresAt), by)
				}
				return w.Flush()
			})},
		&cobra.Command{Use: "rm <id>", Aliases: []string{"revoke"}, Short: "Revoke an API key", Args: cobra.ExactArgs(1),
			RunE: with(func(cmd *cobra.Command, b backend, args []string) error {
				if err := b.DeleteKey(cmd.Context(), args[0]); err != nil {
					return err
				}
				fmt.Fprintf(cmd.OutOrStdout(), "revoked key %s\n", args[0])
				return nil
			})},
	)
	return c
}
```

In `internal/cli/serve.go`:

1. Add the query-log flags type and the pruning loop (`auth` and `time` are already imported):
```go
// queryLogFlags are the query-log settings of qh serve and qh mcp.
type queryLogFlags struct {
	off  bool
	keep string
}

func (f *queryLogFlags) register(cmd *cobra.Command) {
	cmd.Flags().BoolVar(&f.off, "no-query-log", false, "do not log query calls")
	cmd.Flags().StringVar(&f.keep, "query-log-retention", "30d", "how long to keep logged query calls, e.g. 30d or 72h")
}

func (f *queryLogFlags) retention() (time.Duration, error) {
	d, err := auth.ParseExpiry(f.keep)
	if err != nil || d <= 0 {
		return 0, qerr.New(qerr.Invalid, "--query-log-retention must be a positive duration such as 30d or 72h")
	}
	return d, nil
}

// pruneQueryLog deletes expired query-log entries now, then hourly until ctx ends.
func pruneQueryLog(ctx context.Context, svc *core.Service, retention time.Duration) {
	go func() {
		tick := time.NewTicker(time.Hour)
		defer tick.Stop()
		for {
			_, _ = svc.PruneQueryLog(ctx, retention)
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
			}
		}
	}()
}
```
2. In `serveCmd`:
   - declare `var lf queryLogFlags` and call `lf.register(cmd)` next to the other flags;
   - at the very top of `RunE`, add:
```go
			if err := refuseRemote(cmd); err != nil {
				return err
			}
			retention, err := lf.retention()
			if err != nil {
				return err
			}
```
   - set `opts.NoQueryLog = lf.off` before `openAppWith`. Change `var opts core.Options` so that the assignment happens in both modes;
   - directly after `a.pools.Start(ctx)`, add:
```go
			if !lf.off {
				pruneQueryLog(ctx, a.svc, retention)
			}
```
   - inside `RunE`, a later `err` that was declared with `:=` may now need `=`. Fix any redeclaration the compiler reports.

In `internal/cli/masterkey.go`, make the first statement of the `rotate` `RunE` this:
```go
			if err := refuseRemote(cmd); err != nil {
				return err
			}
```

- [ ] **Step 4: Run the tests**

Run: `go test -race ./internal/cli/ ./cmd/qh/ && go vet ./...`
Expected: PASS for every CLI test, old and new:
- `TestMissingFlags` still sees `--dsn is required`;
- `TestWorkspaceAndKeyCommands` still passes, now through the global `--workspace`;
- both e2e tests pass.

- [ ] **Step 5: Commit**

```bash
git add internal/cli
git commit -m "feat(cli): run commands through a backend; add field and TLS flags, qh log and query-log settings"
```

---

### Task 9: Remote CLI, end-to-end check and README

**Files:**
- Create: `internal/client/client.go`
- Modify: `internal/cli/root.go` (`--server`/`--key`, remote branch), `cmd/qh/serve_e2e_test.go`, `README.md`
- Test: `internal/client/client_test.go`, `internal/cli/remote_test.go`

**Interfaces:**
- Consumes:
  - from Task 7: the REST wire format (`connectionJSON` with `username`/`tls`, `queryLogJSON`, problem+json, `X-QH-Workspace`);
  - from Task 8: `backend`, `withBackend`, `workspaceOf`, `refuseRemote`.
- Produces:
  - `client.New(base, key, workspace string) (*client.Client, error)`.
  - `*client.Client` implements every `backend` method.
  - Persistent flags `--server` (or `$QH_SERVER`) and `--key` (or `$QH_KEY`).

- [ ] **Step 1: Write the failing tests**

`internal/client/client_test.go`:
```go
package client

import (
	"context"
	"crypto/rand"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/anderson-0/qh/internal/auth"
	"github.com/anderson-0/qh/internal/catalog"
	"github.com/anderson-0/qh/internal/core"
	"github.com/anderson-0/qh/internal/engine"
	_ "github.com/anderson-0/qh/internal/engine/postgres"
	_ "github.com/anderson-0/qh/internal/engine/sqlite"
	"github.com/anderson-0/qh/internal/httpapi"
	"github.com/anderson-0/qh/internal/pool"
	"github.com/anderson-0/qh/internal/secret"
	"github.com/anderson-0/qh/internal/store"
	"github.com/anderson-0/qh/internal/testutil"
)

// server starts a server-mode API and returns its URL and an admin key.
func server(t *testing.T) (string, string) {
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
	svc := core.New(st, keys, pools, catalog.New(time.Minute))
	admin, _, err := auth.Bootstrap(context.Background(), st, "")
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(httpapi.New(httpapi.Config{Service: svc, Store: st, Auth: auth.NewAuthenticator(st), Version: "test"}))
	t.Cleanup(srv.Close)
	return srv.URL, admin
}

func TestClientAgainstServer(t *testing.T) {
	ctx := context.Background()
	url, admin := server(t)
	a, err := New(url, admin, "")
	if err != nil {
		t.Fatal(err)
	}
	if ws, err := a.CreateWorkspace(ctx, "team"); err != nil || ws.Name != "team" {
		t.Fatalf("workspace: %+v %v", ws, err)
	}
	if list, _ := a.ListWorkspaces(ctx); len(list) != 2 {
		t.Fatalf("workspaces: %+v", list)
	}
	tok, info, err := a.CreateKey(ctx, auth.KeyInput{Workspace: "team", Scopes: []string{"query", "manage"}, Name: "ci", ExpiresIn: 48 * time.Hour})
	if err != nil || !strings.HasPrefix(tok, "qh_") || info.Workspace != "team" || info.ExpiresAt == nil {
		t.Fatalf("key: %q %+v %v", tok, info, err)
	}
	c, _ := New(url, tok, "")
	db := testutil.SQLiteFixture(t)
	ci, tr, err := c.CreateConnection(ctx, core.ConnInput{Name: "shop", Engine: "sqlite", Database: db, Description: "Shop"}, true)
	if err != nil || ci.Name != "shop" || ci.Database != db || tr == nil || !strings.HasPrefix(tr.ServerVersion, "SQLite ") {
		t.Fatalf("create: %+v %+v %v", ci, tr, err)
	}
	f := testutil.NewTLSFixture(t)
	pg, _, err := c.CreateConnection(ctx, core.ConnInput{Name: "pg", Engine: "postgres", Host: "db.example.com", Username: "u",
		Password: "hunter22-pw", TLS: &engine.TLS{Mode: engine.TLSVerifyFull, CA: f.CA}}, false)
	if err != nil || pg.User != "u" || pg.TLS == nil || pg.TLS.Mode != "verify-full" || !pg.TLS.CA {
		t.Fatalf("pg: %+v %v", pg, err)
	}
	pg, _, err = c.UpdateConnection(ctx, "pg", core.ConnPatch{ClearTLS: true}, false)
	if err != nil || pg.TLS != nil || pg.Version != 2 {
		t.Fatalf("clear tls: %+v %v", pg, err)
	}
	if list, _ := c.ListConnections(ctx); len(list) != 2 || list[1].Name != "shop" {
		t.Fatalf("list: %+v", list)
	}
	if got, err := c.GetConnection(ctx, "shop"); err != nil || got.Description != "Shop" {
		t.Fatalf("get: %+v %v", got, err)
	}
	if tr, err := c.TestConnection(ctx, "shop"); err != nil || !strings.HasPrefix(tr.ServerVersion, "SQLite ") {
		t.Fatalf("test: %+v %v", tr, err)
	}
	if out, err := c.Query(ctx, core.QueryInput{Source: "shop", Query: "SELECT count(*) AS n FROM orders"}); err != nil || !strings.HasPrefix(out, "n\n250\n") {
		t.Fatalf("query: %q %v", out, err)
	}
	if out, err := c.Schema(ctx, core.SchemaInput{Source: "shop", Pattern: "user*"}); err != nil || !strings.Contains(out, "main.users") {
		t.Fatalf("schema: %q %v", out, err)
	}
	if log, err := c.QueryLog(ctx, core.QueryLogFilter{Connection: "shop", Since: time.Now().Add(-time.Hour), Limit: 10}); err != nil ||
		len(log) != 1 || log[0].Rows != 1 || log[0].KeyID != info.ID {
		t.Fatalf("log: %+v %v", log, err)
	}
	if _, err := c.Query(ctx, core.QueryInput{Source: "nope", Query: "SELECT 1"}); err == nil || !strings.HasPrefix(err.Error(), `error[unknown_source]: no connection named "nope"`) {
		t.Fatalf("unknown: %v", err)
	}
	if err := c.DeleteConnection(ctx, "pg"); err != nil {
		t.Fatal(err)
	}
	keys, err := a.ListKeys(ctx)
	if err != nil || len(keys) != 2 {
		t.Fatalf("keys: %+v %v", keys, err)
	}
	if err := a.DeleteKey(ctx, info.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := c.ListConnections(ctx); err == nil || !strings.HasPrefix(err.Error(), "error[auth]: invalid API key") {
		t.Fatalf("revoked key: %v", err)
	}
	if err := a.DeleteWorkspace(ctx, "team"); err != nil {
		t.Fatal(err)
	}
	if _, err := a.ListConnections(ctx); err == nil || !strings.HasPrefix(err.Error(), "error[invalid]: admin keys must name a workspace") {
		t.Fatalf("admin without workspace: %v", err)
	}
	inDefault, _ := New(url, admin, "default")
	if list, err := inDefault.ListConnections(ctx); err != nil || len(list) != 0 {
		t.Fatalf("admin with workspace: %+v %v", list, err)
	}
}

func TestNewRefuses(t *testing.T) {
	for _, c := range []struct{ base, key, want string }{
		{"http://db.example.com:8080", "qh_k", "error[invalid]: refusing to send an API key over plain http to db.example.com:8080"},
		{"ftp://x", "qh_k", "error[invalid]: --server must be an http(s) URL"},
		{"https://user:pw@x", "qh_k", "error[invalid]: --server must be an http(s) URL"},
		{"https://qh.example.com", "", "error[invalid]: remote mode needs an API key"},
	} {
		if _, err := New(c.base, c.key, ""); err == nil || !strings.HasPrefix(err.Error(), c.want) {
			t.Errorf("%s: %v", c.base, err)
		}
	}
	for _, ok := range []string{"http://127.0.0.1:8765", "http://localhost:8765/", "http://[::1]:8765", "https://qh.example.com"} {
		if _, err := New(ok, "qh_k", ""); err != nil {
			t.Errorf("%s: %v", ok, err)
		}
	}
	c, _ := New("http://127.0.0.1:1", "qh_secretkey", "")
	_, err := c.ListConnections(context.Background())
	if err == nil || err.Error() != "error[connection_failed]: cannot reach qh server at http://127.0.0.1:1" {
		t.Fatalf("unreachable: %v", err)
	}
}
```

`internal/cli/remote_test.go`:
```go
package cli

import (
	"context"
	"crypto/rand"
	"net/http/httptest"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/anderson-0/qh/internal/auth"
	"github.com/anderson-0/qh/internal/catalog"
	"github.com/anderson-0/qh/internal/core"
	"github.com/anderson-0/qh/internal/httpapi"
	"github.com/anderson-0/qh/internal/pool"
	"github.com/anderson-0/qh/internal/secret"
	"github.com/anderson-0/qh/internal/store"
	"github.com/anderson-0/qh/internal/testutil"
)

func remoteServer(t *testing.T) (string, string) {
	t.Helper()
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "server.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	key := make([]byte, secret.KeySize)
	rand.Read(key)
	keys, _ := secret.NewKeyring(key)
	pools := pool.New(core.OpenConn, pool.Options{})
	t.Cleanup(pools.Close)
	admin, _, err := auth.Bootstrap(context.Background(), st, "")
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(httpapi.New(httpapi.Config{Service: core.New(st, keys, pools, catalog.New(time.Minute)),
		Store: st, Auth: auth.NewAuthenticator(st), Version: "test"}))
	t.Cleanup(srv.Close)
	return srv.URL, admin
}

func TestRemoteMode(t *testing.T) {
	url, admin := remoteServer(t)
	home, db := t.TempDir(), testutil.SQLiteFixture(t)
	as := func(key string, args ...string) (string, string, int) {
		return run(t, home, "", append([]string{"--server", url, "--key", key}, args...)...)
	}
	if out, errOut, code := as(admin, "workspace", "create", "team"); code != 0 || out != "created workspace team\n" {
		t.Fatalf("workspace: %d %q %q", code, out, errOut)
	}
	out, errOut, code := as(admin, "--workspace", "team", "key", "create", "--scopes", "query,manage")
	tok := regexp.MustCompile(`(?m)^qh_\S+$`).FindString(out)
	if code != 0 || tok == "" || !strings.Contains(out, "workspace team") {
		t.Fatalf("key: %d %q %q", code, out, errOut)
	}
	if out, errOut, code := as(tok, "conn", "add", "shop", "--engine", "sqlite", "--database", db); code != 0 || !strings.HasPrefix(out, "added shop (sqlite, ro) · SQLite ") {
		t.Fatalf("add: %d %q %q", code, out, errOut)
	}
	if out, _, _ := as(tok, "conn", "ls"); !strings.Contains(out, "shop") {
		t.Fatalf("ls: %q", out)
	}
	if out, _, _ := as(tok, "query", "shop", "SELECT count(*) AS n FROM orders"); !strings.HasPrefix(out, "n\n250\n") {
		t.Fatalf("query: %q", out)
	}
	if out, _, _ := as(tok, "schema", "shop", "users"); !strings.Contains(out, "main.users") {
		t.Fatalf("schema: %q", out)
	}
	if out, _, _ := as(tok, "log"); !strings.Contains(out, "shop") || !strings.Contains(out, "SELECT count(*) AS n FROM orders") {
		t.Fatalf("log: %q", out)
	}
	if _, errOut, code := as(tok, "conn", "show", "nope"); code != 1 || !strings.HasPrefix(errOut, `error[unknown_source]: no connection named "nope"`) {
		t.Fatalf("unknown: %d %q", code, errOut)
	}
	if _, errOut, code := as(admin, "conn", "ls"); code != 1 || !strings.HasPrefix(errOut, "error[invalid]: admin keys must name a workspace") {
		t.Fatalf("admin without --workspace: %d %q", code, errOut)
	}
	if _, errOut, code := as(admin, "mcp"); code != 1 || !strings.HasPrefix(errOut, "error[invalid]: qh mcp runs locally only") {
		t.Fatalf("mcp: %d %q", code, errOut)
	}
	if _, errOut, code := as(admin, "masterkey", "rotate", "--old-key-file", "x"); code != 1 || !strings.HasPrefix(errOut, "error[invalid]: qh masterkey rotate runs locally only") {
		t.Fatalf("masterkey: %d %q", code, errOut)
	}
	t.Setenv("QH_SERVER", url)
	t.Setenv("QH_KEY", tok)
	if out, _, _ := run(t, home, "", "conn", "ls"); !strings.Contains(out, "shop") {
		t.Fatalf("QH_SERVER and QH_KEY: %q", out)
	}
}

func TestRemoteRefusesPlainHTTP(t *testing.T) {
	_, errOut, code := run(t, t.TempDir(), "", "--server", "http://db.example.com:8765", "--key", "qh_abcdefgh_secret", "conn", "ls")
	if code != 1 || !strings.HasPrefix(errOut, "error[invalid]: refusing to send an API key over plain http to db.example.com:8765") ||
		strings.Contains(errOut, "qh_abcdefgh_secret") {
		t.Fatalf("%d %q", code, errOut)
	}
}
```

In `cmd/qh/serve_e2e_test.go` `TestServeEndToEnd`, add this check after the REST query check and before the MCP section:
```go
	remote := exec.Command(bin, "--server", base, "--key", k.Key, "conn", "ls")
	if out, err := remote.CombinedOutput(); err != nil || !strings.Contains(string(out), "shop") {
		t.Fatalf("remote CLI: %v %s", err, out)
	}
```

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./internal/client/ ./internal/cli/ -run 'Client|NewRefuses|Remote'`
Expected: FAIL: package `internal/client` has no non-test files (`undefined: New`), and `unknown flag: --key`.

- [ ] **Step 3: Implement**

`internal/client/client.go`:
```go
// Package client calls a qh server's REST API. The CLI uses it in remote mode.
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/anderson-0/qh/internal/auth"
	"github.com/anderson-0/qh/internal/core"
	"github.com/anderson-0/qh/internal/engine"
	"github.com/anderson-0/qh/internal/qerr"
)

// Client is a REST client for one server, key and (optionally) workspace.
type Client struct {
	base      string
	key       string
	workspace string // sent as X-QH-Workspace when not ""
	http      *http.Client
}

// New returns a client for the server at base. It refuses plain http to
// hosts other than this machine, so the key never crosses a network in clear.
func New(base, key, workspace string) (*Client, error) {
	u, err := url.Parse(strings.TrimRight(base, "/"))
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" {
		return nil, qerr.New(qerr.Invalid, "--server must be an http(s) URL such as https://qh.example.com")
	}
	if u.Scheme == "http" && !loopback(u.Hostname()) {
		return nil, qerr.New(qerr.Invalid, "refusing to send an API key over plain http to "+u.Host).
			WithHint("Use https, or a server on this machine")
	}
	if key == "" {
		return nil, qerr.New(qerr.Invalid, "remote mode needs an API key").WithHint("Pass --key or set QH_KEY")
	}
	return &Client{base: u.String(), key: key, workspace: workspace, http: &http.Client{Timeout: 6 * time.Minute}}, nil
}

func loopback(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip, err := netip.ParseAddr(host)
	return err == nil && ip.Unmap().IsLoopback()
}

// send makes one request and returns the response body. Error responses
// become *qerr.Error values with the server's category, message and hint.
func (c *Client) send(ctx context.Context, method, path string, body any, accept string) ([]byte, error) {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, rd)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.key)
	req.Header.Set("Accept", accept)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.workspace != "" {
		req.Header.Set("X-QH-Workspace", c.workspace)
	}
	res, err := c.http.Do(req)
	if err != nil {
		return nil, qerr.New(qerr.ConnectionFailed, "cannot reach qh server at "+c.base) // err may carry the request; drop it
	}
	defer res.Body.Close()
	b, err := io.ReadAll(io.LimitReader(res.Body, 64<<20))
	if err != nil {
		return nil, qerr.New(qerr.ConnectionFailed, "lost the connection to qh server at "+c.base)
	}
	if res.StatusCode >= 400 {
		var p struct{ Type, Title, Detail string }
		if strings.HasPrefix(res.Header.Get("Content-Type"), "application/problem+json") && json.Unmarshal(b, &p) == nil && p.Type != "" {
			return nil, &qerr.Error{Category: qerr.Category(p.Type), Message: p.Title, Hint: p.Detail}
		}
		return nil, qerr.New(qerr.ConnectionFailed, fmt.Sprintf("qh server at %s answered HTTP %d", c.base, res.StatusCode))
	}
	return b, nil
}

func (c *Client) call(ctx context.Context, method, path string, body, out any) error {
	b, err := c.send(ctx, method, path, body, "application/json")
	if err != nil || out == nil {
		return err
	}
	if err := json.Unmarshal(b, out); err != nil {
		return qerr.New(qerr.ConnectionFailed, "unexpected response from qh server at "+c.base)
	}
	return nil
}

func (c *Client) text(ctx context.Context, method, path string, body any) (string, error) {
	b, err := c.send(ctx, method, path, body, "text/plain")
	return string(b), err
}

func conn(name string) string { return "/api/v1/connections/" + url.PathEscape(name) }

type connectionJSON struct {
	Name              string          `json:"name"`
	Engine            string          `json:"engine"`
	Description       string          `json:"description"`
	Mode              string          `json:"mode"`
	Host              string          `json:"host"`
	Port              int             `json:"port"`
	Database          string          `json:"database"`
	Username          string          `json:"username"`
	DSN               string          `json:"dsn"`
	TLS               *engine.TLSInfo `json:"tls"`
	MaxRows           int             `json:"max_rows"`
	TimeoutMS         int             `json:"timeout_ms"`
	PoolSize          int             `json:"pool_size"`
	AllowedNamespaces []string        `json:"allowed_namespaces"`
	Version           int64           `json:"version"`
	CreatedAt         time.Time       `json:"created_at"`
	UpdatedAt         time.Time       `json:"updated_at"`
	ServerVersion     string          `json:"server_version"`
	ConnectMS         *int64          `json:"connect_ms"`
}

func (j connectionJSON) info() (*core.ConnInfo, *core.TestResult) {
	ci := &core.ConnInfo{Name: j.Name, Engine: j.Engine, Description: j.Description, Mode: engine.Mode(j.Mode),
		Host: j.Host, Port: j.Port, Database: j.Database, User: j.Username, DSN: j.DSN, TLS: j.TLS,
		MaxRows: j.MaxRows, TimeoutMS: j.TimeoutMS, PoolSize: j.PoolSize, AllowedNamespaces: j.AllowedNamespaces,
		Version: j.Version, CreatedAt: j.CreatedAt, UpdatedAt: j.UpdatedAt}
	if j.ConnectMS == nil {
		return ci, nil
	}
	return ci, &core.TestResult{ServerVersion: j.ServerVersion, Connect: time.Duration(*j.ConnectMS) * time.Millisecond}
}

type testJSON struct {
	ServerVersion string `json:"server_version"`
	ConnectMS     int64  `json:"connect_ms"`
}

func (c *Client) ListConnections(ctx context.Context) ([]*core.ConnInfo, error) {
	var list []connectionJSON
	if err := c.call(ctx, "GET", "/api/v1/connections", nil, &list); err != nil {
		return nil, err
	}
	out := make([]*core.ConnInfo, len(list))
	for i, j := range list {
		out[i], _ = j.info()
	}
	return out, nil
}

func (c *Client) GetConnection(ctx context.Context, name string) (*core.ConnInfo, error) {
	var j connectionJSON
	if err := c.call(ctx, "GET", conn(name), nil, &j); err != nil {
		return nil, err
	}
	ci, _ := j.info()
	return ci, nil
}

func skip(test bool) string {
	if test {
		return ""
	}
	return "?skip_test=true"
}

func (c *Client) CreateConnection(ctx context.Context, in core.ConnInput, test bool) (*core.ConnInfo, *core.TestResult, error) {
	body := map[string]any{"name": in.Name, "engine": in.Engine}
	set := func(k string, v any, ok bool) {
		if ok {
			body[k] = v
		}
	}
	set("dsn", in.DSN, in.DSN != "")
	set("host", in.Host, in.Host != "")
	set("port", in.Port, in.Port != 0)
	set("database", in.Database, in.Database != "")
	set("username", in.Username, in.Username != "")
	set("password", in.Password, in.Password != "")
	set("params", in.Params, len(in.Params) > 0)
	set("tls", in.TLS, in.TLS != nil)
	set("description", in.Description, in.Description != "")
	set("mode", string(in.Mode), in.Mode != "")
	set("max_rows", in.MaxRows, in.MaxRows != 0)
	set("timeout_ms", in.TimeoutMS, in.TimeoutMS != 0)
	set("pool_size", in.PoolSize, in.PoolSize != 0)
	set("allowed_namespaces", in.AllowedNamespaces, len(in.AllowedNamespaces) > 0)
	var j connectionJSON
	if err := c.call(ctx, "POST", "/api/v1/connections"+skip(test), body, &j); err != nil {
		return nil, nil, err
	}
	ci, tr := j.info()
	return ci, tr, nil
}

func (c *Client) UpdateConnection(ctx context.Context, name string, p core.ConnPatch, test bool) (*core.ConnInfo, *core.TestResult, error) {
	body := map[string]any{}
	if p.DSN != nil {
		body["dsn"] = *p.DSN
	}
	if p.Description != nil {
		body["description"] = *p.Description
	}
	if p.Mode != nil {
		body["mode"] = string(*p.Mode)
	}
	if p.MaxRows != nil {
		body["max_rows"] = *p.MaxRows
	}
	if p.TimeoutMS != nil {
		body["timeout_ms"] = *p.TimeoutMS
	}
	if p.PoolSize != nil {
		body["pool_size"] = *p.PoolSize
	}
	if p.AllowedNamespaces != nil {
		body["allowed_namespaces"] = *p.AllowedNamespaces
	}
	switch {
	case p.ClearTLS && p.TLS != nil:
		return nil, nil, qerr.New(qerr.Invalid, "give tls or clear it, not both")
	case p.ClearTLS:
		body["tls"] = nil
	case p.TLS != nil:
		body["tls"] = p.TLS
	}
	var j connectionJSON
	if err := c.call(ctx, "PATCH", conn(name)+skip(test), body, &j); err != nil {
		return nil, nil, err
	}
	ci, tr := j.info()
	return ci, tr, nil
}

func (c *Client) DeleteConnection(ctx context.Context, name string) error {
	return c.call(ctx, "DELETE", conn(name), nil, nil)
}

func (c *Client) TestConnection(ctx context.Context, name string) (*core.TestResult, error) {
	var j testJSON
	if err := c.call(ctx, "POST", conn(name)+"/test", nil, &j); err != nil {
		return nil, err
	}
	return &core.TestResult{ServerVersion: j.ServerVersion, Connect: time.Duration(j.ConnectMS) * time.Millisecond}, nil
}

func (c *Client) Query(ctx context.Context, in core.QueryInput) (string, error) {
	body := map[string]any{"query": in.Query}
	if in.MaxRows != 0 {
		body["max_rows"] = in.MaxRows
	}
	if in.Explain {
		body["explain"] = true
	}
	return c.text(ctx, "POST", conn(in.Source)+"/query", body)
}

func (c *Client) Schema(ctx context.Context, in core.SchemaInput) (string, error) {
	q := url.Values{}
	if in.Pattern != "" {
		q.Set("pattern", in.Pattern)
	}
	if in.Object != "" {
		q.Set("object", in.Object)
	}
	path := conn(in.Source) + "/schema"
	if len(q) > 0 {
		path += "?" + q.Encode()
	}
	return c.text(ctx, "GET", path, nil)
}

func (c *Client) QueryLog(ctx context.Context, f core.QueryLogFilter) ([]*core.QueryLogEntry, error) {
	q := url.Values{}
	if f.Connection != "" {
		q.Set("connection", f.Connection)
	}
	if !f.Since.IsZero() {
		q.Set("since", f.Since.UTC().Format(time.RFC3339))
	}
	if f.Limit != 0 {
		q.Set("limit", strconv.Itoa(f.Limit))
	}
	path := "/api/v1/query-log"
	if len(q) > 0 {
		path += "?" + q.Encode()
	}
	var list []struct {
		ID            int64     `json:"id"`
		Connection    string    `json:"connection"`
		KeyID         string    `json:"key_id"`
		Tool          string    `json:"tool"`
		Query         string    `json:"query"`
		Rows          int64     `json:"rows"`
		DurationMS    int64     `json:"duration_ms"`
		ErrorCategory string    `json:"error_category"`
		At            time.Time `json:"at"`
	}
	if err := c.call(ctx, "GET", path, nil, &list); err != nil {
		return nil, err
	}
	out := make([]*core.QueryLogEntry, len(list))
	for i, e := range list {
		out[i] = &core.QueryLogEntry{ID: e.ID, Connection: e.Connection, KeyID: e.KeyID, Tool: e.Tool, Query: e.Query,
			Rows: e.Rows, Duration: time.Duration(e.DurationMS) * time.Millisecond, ErrorCategory: e.ErrorCategory, At: e.At}
	}
	return out, nil
}

type workspaceJSON struct {
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"created_at"`
}

func (c *Client) CreateWorkspace(ctx context.Context, name string) (*core.WorkspaceInfo, error) {
	var j workspaceJSON
	if err := c.call(ctx, "POST", "/api/v1/workspaces", map[string]string{"name": name}, &j); err != nil {
		return nil, err
	}
	return &core.WorkspaceInfo{Name: j.Name, CreatedAt: j.CreatedAt}, nil
}

func (c *Client) ListWorkspaces(ctx context.Context) ([]*core.WorkspaceInfo, error) {
	var list []workspaceJSON
	if err := c.call(ctx, "GET", "/api/v1/workspaces", nil, &list); err != nil {
		return nil, err
	}
	out := make([]*core.WorkspaceInfo, len(list))
	for i, j := range list {
		out[i] = &core.WorkspaceInfo{Name: j.Name, CreatedAt: j.CreatedAt}
	}
	return out, nil
}

func (c *Client) DeleteWorkspace(ctx context.Context, name string) error {
	return c.call(ctx, "DELETE", "/api/v1/workspaces/"+url.PathEscape(name), nil, nil)
}

type keyJSON struct {
	ID         string     `json:"id"`
	Prefix     string     `json:"prefix"`
	Name       string     `json:"name"`
	Workspace  string     `json:"workspace"`
	Scopes     []string   `json:"scopes"`
	CreatedAt  time.Time  `json:"created_at"`
	ExpiresAt  *time.Time `json:"expires_at"`
	LastUsedAt *time.Time `json:"last_used_at"`
	CreatedBy  string     `json:"created_by"`
	Key        string     `json:"key"`
}

func (j keyJSON) info() *auth.KeyInfo {
	return &auth.KeyInfo{ID: j.ID, Prefix: j.Prefix, Name: j.Name, Workspace: j.Workspace, Scopes: j.Scopes,
		CreatedAt: j.CreatedAt, ExpiresAt: j.ExpiresAt, LastUsedAt: j.LastUsedAt, CreatedBy: j.CreatedBy}
}

func (c *Client) CreateKey(ctx context.Context, in auth.KeyInput) (string, *auth.KeyInfo, error) {
	body := map[string]any{"scopes": in.Scopes}
	if in.Workspace != "" {
		body["workspace"] = in.Workspace
	}
	if in.Name != "" {
		body["name"] = in.Name
	}
	if in.ExpiresIn > 0 {
		body["expires_in"] = in.ExpiresIn.String()
	}
	var j keyJSON
	if err := c.call(ctx, "POST", "/api/v1/keys", body, &j); err != nil {
		return "", nil, err
	}
	return j.Key, j.info(), nil
}

func (c *Client) ListKeys(ctx context.Context) ([]*auth.KeyInfo, error) {
	var list []keyJSON
	if err := c.call(ctx, "GET", "/api/v1/keys", nil, &list); err != nil {
		return nil, err
	}
	out := make([]*auth.KeyInfo, len(list))
	for i, j := range list {
		out[i] = j.info()
	}
	return out, nil
}

func (c *Client) DeleteKey(ctx context.Context, id string) error {
	return c.call(ctx, "DELETE", "/api/v1/keys/"+url.PathEscape(id), nil, nil)
}
```

In `internal/cli/root.go`:

1. Replace the hidden placeholder flag with real flags:
```go
	var server, key string
	pf.StringVar(&server, "server", "", "qh server URL; commands then use its REST API (default $QH_SERVER)")
	pf.StringVar(&key, "key", "", "API key for --server (default $QH_KEY)")
```
Delete the `pf.String("server", "", "")` and `pf.MarkHidden("server")` lines.

2. At the start of the `with` closure's inner function, before the local branch, add:
```go
			srv := server
			if srv == "" {
				srv = os.Getenv("QH_SERVER")
			}
			if srv != "" {
				k := key
				if k == "" {
					k = os.Getenv("QH_KEY")
				}
				header := ""
				if ws, explicit := workspaceOf(cmd); explicit {
					header = ws // sent only when named, so admin keys never act on default by accident
				}
				c, err := client.New(srv, k, header)
				if err != nil {
					return err
				}
				return fn(cmd, c, args)
			}
```
3. Add a compile-time check, and the `client` import:
```go
var _ backend = (*client.Client)(nil)
```

`refuseRemote` already reads the `--server` flag, so it now refuses `--server` on `mcp`, `serve` and `masterkey rotate` with that command's path. `QH_SERVER` is not refused there, so a shell that sets it can still run a local server.

`README.md`: add these sections before "Read-only safety". Also, in the "Server mode" section, replace the sentence about `--home` for key and workspace commands with: "`qh key` and `qh workspace` act on the local store at `--home`, or on a server with `--server`/`--key` (see Remote CLI)."
````markdown
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
export QH_MASTER_KEY=$(openssl rand -base64 32) # the new key (local mode: move master.key away instead)
qh masterkey rotate --old-key-file old.key --home /var/lib/qh
```

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
````

- [ ] **Step 4: Run the tests and full verification**

Run: `go test ./internal/client/ ./internal/cli/ ./cmd/qh/ && gofmt -l . && go vet ./... && go test -race ./... && make build`
Expected:
- all PASS, with Docker running;
- `gofmt -l .` prints nothing;
- `TestServeEndToEnd` runs the real binary in remote mode.

- [ ] **Step 5: Commit**

```bash
git add internal/client internal/cli cmd/qh README.md
git commit -m "feat: add a remote CLI mode over the REST API"
```
