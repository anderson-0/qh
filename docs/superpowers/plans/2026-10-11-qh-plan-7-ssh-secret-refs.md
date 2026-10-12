# qh Plan 7: SSH tunnels and secret references — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let a connection reach its database through an SSH bastion, and take its credentials from `env:`, `file:` or `aws-sm://` references, with server mode closed by default.

**Architecture:**

- Two new leaf packages:
  - `internal/secretref` parses and resolves references, and holds the server's allow-list.
  - `internal/tunnel` is one SSH client per pool, which dials the database over `direct-tcpip` channels.
- Engines learn only two new `ConnSpec` fields: `Server` and `Password`.
- `core.OpenConn`, the pool opener, does the rest:
  - resolves references into the spec;
  - builds the tunnel and sets `spec.Dial` to it;
  - returns a `Conn` wrapper that closes the tunnel with the pool.
- The connection model (core, store, CLI, REST, client) gains `dsn_ref`, `password_ref` and an `ssh` block, following the existing `tls` precedent.

**Tech Stack:**

- Go 1.25;
- `golang.org/x/crypto/ssh`, currently indirect at v0.54.0 (make it direct);
- `github.com/aws/aws-sdk-go-v2/config` and `.../service/secretsmanager` (new);
- pgx v5, go-sql-driver/mysql v1.10, testcontainers-go.

**Spec:** `docs/superpowers/specs/2026-10-11-qh-ssh-tunnels-secret-refs-design.md`. It extends `docs/superpowers/specs/2026-10-09-qh-design.md`; read both.

## Spec adjustments

1. **`host_key` may be empty in `engine.SSH.Validate`, but saving needs it.**
   - The spec's fingerprint flow runs a connection test without a host key and shows the presented fingerprint (§6.1).
   - So `Validate` accepts an empty `host_key`, and `CreateConnection`/`UpdateConnection` refuse to save one: `ssh.host_key is required`.
   - The test always fails first when it runs, because a tunnel with no host key rejects every key.
2. **Any `connection_failed` error from a query drops the pool, not only authentication failures (§4.2).**
   - This also recovers a tunnel whose bastion went away.
   - Reopening costs one connection setup.
3. **`secretref.NewSet` cannot fail.** Server patterns are validated once at `qh serve` start by `secretref.CheckPattern`.

## Global Constraints

- **Agent-facing surface unchanged.** The MCP tools, instructions and output do not change; `internal/mcpserver` tests stay as they are.
- **No secrets in errors.** Errors, logs and `ConnInfo` never contain key material, passwords or resolved secret values. References themselves (`env:NAME`, …) are not secret and may appear.
- **Server mode is closed by default:**
  - a reference that matches no `--allow-secret-ref` pattern is refused with `error[forbidden]`;
  - `qh serve --deny-ssh` refuses tunnels;
  - tunnels pass the network policy for both the bastion and the target, with loopback targets exempt.
- **Local mode accepts every reference and every tunnel.**
- **Errors stay one line,** `error[<category>]: <message>. <hint>` (qerr).
- **Commit trailer on every commit:** `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`. If your harness says another model is running you, name that model instead.
- **Branch:** `feat/plan-7-ssh-secrets`. Do not push.
- **Docker-backed tests skip under `-short`:** guard with `testing.Short()` and `testcontainers.SkipIfProviderIsNotHealthy(t)`, as the engine tests do.

## Review Focus

1. **A resolved secret inside an engine error.** For example, a `dsn_ref` resolving to a DSN with a wrong password: the error must not contain the resolved DSN or password (Task 7 test).
2. **The bastion dropping existing SSH connections mid-session.** The next dial reconnects instead of failing for good (Task 5 test).
3. **Many concurrent dials through one tunnel** (a pool of 5 opening at once). There must be no race, and only one SSH connection (Task 5 test, run with `-race`).
4. **A `file:` reference escaping the allowed directory** with `..` or a symlink, in server mode. It is refused (Task 2 test).
5. **Switching a connection between a literal `dsn` and a `dsn_ref` on edit.** The other one is cleared, not both kept (Task 6 test).

---

### Task 1: `ConnSpec.Server` and `ConnSpec.Password`

**Files:**
- Modify: `internal/engine/engine.go` (`ConnSpec`)
- Modify: `internal/engine/postgres/postgres.go` (`poolConfig`, around lines 264-307)
- Modify: `internal/engine/mysql/conn.go` (`Open`, around lines 37-60)
- Modify: `internal/core/service.go` (`connSpec`)
- Test: `internal/engine/postgres/server_test.go`, `internal/engine/postgres/postgres_test.go`, `internal/engine/mysql/mysql_test.go`

**Interfaces:**
- Produces:
  - `engine.ConnSpec.Server bool`: server mode. Engines apply their server-mode limits only when it is set.
  - `engine.ConnSpec.Password string`: when non-empty, it replaces the connection string's password.

- [ ] **Step 1: Add the fields**

In `internal/engine/engine.go`, add to `ConnSpec` after `TLS`:

```go
	// Server is set in server mode: engines apply their server-mode limits
	// (Postgres: the connection-string allow-list, and no password from the
	// environment or files). Dial alone no longer implies it: a local SSH
	// tunnel sets Dial too.
	Server bool
	// Password, when set, replaces the connection string's password (a
	// resolved password_ref).
	Password string
```

- [ ] **Step 2: Write the failing tests**

In `internal/engine/postgres/server_test.go`, every `engine.ConnSpec{…Dial: …}` that tests server-mode behaviour must now also set `Server: true`. That covers the specs at about lines 114, 165, 209, 261 and 306. Add `Server: true,` to each.

Then add to `internal/engine/postgres/server_test.go`:

```go
// A custom dialer without Server is a local SSH tunnel: the server-mode
// allow-list must not apply.
func TestLocalDialIsNotServerMode(t *testing.T) {
	dsn := "postgres://u:p@db.example.com/app?passfile=/nonexistent&sslmode=disable"
	if _, err := poolConfig(engine.ConnSpec{DSN: dsn, PoolSize: 1, Dial: dialTo("127.0.0.1:1")}); err != nil {
		t.Fatalf("local dial refused: %v", err)
	}
	if _, err := poolConfig(engine.ConnSpec{DSN: dsn, PoolSize: 1, Dial: dialTo("127.0.0.1:1"), Server: true}); err == nil {
		t.Fatal("server mode accepted passfile")
	}
}

func TestPasswordOverride(t *testing.T) {
	cfg, err := poolConfig(engine.ConnSpec{DSN: "postgres://u:wrong@db.example.com/app", PoolSize: 1, Password: "right"})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ConnConfig.Password != "right" {
		t.Fatalf("password = %q", cfg.ConnConfig.Password)
	}
	cfg, err = poolConfig(engine.ConnSpec{DSN: "postgres://u@db.example.com/app", PoolSize: 1, Password: "right", Server: true, Dial: dialTo("127.0.0.1:1")})
	if err != nil || cfg.ConnConfig.Password != "right" {
		t.Fatalf("server mode: %v %q", err, cfg.ConnConfig.Password)
	}
}
```

In `internal/engine/mysql/mysql_test.go`, add (Docker):

```go
func TestPasswordOverride(t *testing.T) {
	dsn := startDB(t, "mysql:8.4")
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	pw, _ := u.User.Password()
	u.User = url.UserPassword(u.User.Username(), "wrong")
	ro, _ := specs(u.String())
	ro.Password = pw
	c := openConn(t, ro)
	if r, err := c.Query(context.Background(), "SELECT 1", engine.QueryOpts{MaxRows: 1, ReadOnly: true, Timeout: 5 * time.Second}); err != nil || r.Rows[0][0].Text != "1" {
		t.Fatalf("%+v %v", r, err)
	}
}
```

If `startDB` returns a DSN that is not a URL, check `internal/engine/mysql/mysql_test.go` for its format first. Then build the wrong-password DSN in that format, keeping the test's intent: the DSN's password is wrong and `spec.Password` is right.

- [ ] **Step 3: Run the tests to verify they fail**

Run: `go test ./internal/engine/postgres -run 'TestLocalDialIsNotServerMode|TestPasswordOverride' -count=1`

Expected: FAIL. The local dial is refused with the allow-list error, and the password stays `wrong`.

- [ ] **Step 4: Implement**

In `internal/engine/postgres/postgres.go` `poolConfig`:

- Change the first two `if spec.Dial != nil {` checks (the allow-list block and the password/client-cert block) to `if spec.Server {`.
- Keep the third check, which sets `DialFunc` and `LookupFunc`, as `if spec.Dial != nil {`. A tunnel needs the host name passed through unresolved.
- Update the doc comment: "In server mode (spec.Server) the connection string may use only serverKeys…".
- After the server block that clears `cc.Password`, add:

```go
	if spec.Password != "" {
		cfg.ConnConfig.Password = spec.Password
	}
```

In `internal/engine/mysql/conn.go` `Open`, before `gomysql.NewConnector(cfg)`, add:

```go
	if spec.Password != "" {
		cfg.Passwd = spec.Password
	}
```

In `internal/core/service.go` `connSpec`, set `Server: s.opts.Dial != nil` in the `engine.ConnSpec` literal.

- [ ] **Step 5: Run the tests to verify they pass**

Run: `go test ./internal/engine/... ./internal/core -count=1`

Expected: PASS. The Postgres server-mode suite stays green with `Server: true` added.

- [ ] **Step 6: Commit**

```bash
git add internal/engine internal/core/service.go
git commit -m "feat(engine): make server mode explicit and let a spec override the password

ConnSpec.Server now turns on the Postgres server-mode limits; a custom
dialer alone no longer does, because a local SSH tunnel sets one too.
ConnSpec.Password replaces the connection string's password.

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 2: `internal/secretref` — parsing, `env:`, `file:`, allow-list

**Files:**
- Create: `internal/secretref/secretref.go`
- Test: `internal/secretref/secretref_test.go`

**Interfaces:**
- Produces:
  - `type Ref struct { Raw, Scheme, Name, Key string }`. `Scheme` is `"env"`, `"file"` or `"aws-sm"`. `Name` is the variable name, the absolute path or the AWS secret id. `Key` is the aws-sm JSON field.
  - `func Parse(s string) (Ref, error)`: `qerr.Invalid` on a bad reference.
  - `type Resolver interface { Resolve(ctx context.Context, r Ref) (string, error) }`
  - `func NewSet(restricted bool, allow []string) *Set`: `restricted` is true in server mode.
  - `func (s *Set) With(scheme string, r Resolver) *Set`: replaces a resolver, for tests.
  - `func (s *Set) Check(raw string) (Ref, error)`: parse plus the allow-list. Errors are `qerr.Invalid` or `qerr.Forbidden`.
  - `func (s *Set) Resolve(ctx context.Context, raw string) (string, error)`: Check, then resolve with a 10 s bound. Errors are `qerr.ConnectionFailed` and never contain the value.
  - `func CheckPattern(p string) error`: validates a server `--allow-secret-ref` pattern.

- [ ] **Step 1: Write the failing tests**

`internal/secretref/secretref_test.go`:

```go
package secretref

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/anderson-0/qh/internal/qerr"
)

func cat(err error) qerr.Category { return qerr.From(err).Category }

func TestParse(t *testing.T) {
	for in, want := range map[string]Ref{
		"env:QH_DB_PW":                 {Raw: "env:QH_DB_PW", Scheme: "env", Name: "QH_DB_PW"},
		"file:/run/secrets/db":         {Raw: "file:/run/secrets/db", Scheme: "file", Name: "/run/secrets/db"},
		"aws-sm://prod/db":             {Raw: "aws-sm://prod/db", Scheme: "aws-sm", Name: "prod/db"},
		"aws-sm://prod/db#password":    {Raw: "aws-sm://prod/db#password", Scheme: "aws-sm", Name: "prod/db", Key: "password"},
		"aws-sm://arn:aws:secretsmanager:eu-west-1:1:secret:x#k": {Raw: "aws-sm://arn:aws:secretsmanager:eu-west-1:1:secret:x#k", Scheme: "aws-sm", Name: "arn:aws:secretsmanager:eu-west-1:1:secret:x", Key: "k"},
	} {
		got, err := Parse(in)
		if err != nil || got != want {
			t.Errorf("%s: %+v %v", in, got, err)
		}
	}
	for _, bad := range []string{"", "env:", "env:1X", "env:A-B", "file:relative/path", "aws-sm://", "aws-sm://x#", "vault://x", "ENV:X"} {
		if _, err := Parse(bad); cat(err) != qerr.Invalid {
			t.Errorf("%q: %v", bad, err)
		}
	}
}

func TestResolveEnvAndFile(t *testing.T) {
	ctx := context.Background()
	s := NewSet(false, nil)
	t.Setenv("QH_TEST_SECRET", "s3cret-value")
	if v, err := s.Resolve(ctx, "env:QH_TEST_SECRET"); err != nil || v != "s3cret-value" {
		t.Fatalf("env: %q %v", v, err)
	}
	t.Setenv("QH_TEST_EMPTY", "")
	if _, err := s.Resolve(ctx, "env:QH_TEST_EMPTY"); cat(err) != qerr.ConnectionFailed || !strings.Contains(err.Error(), "env:QH_TEST_EMPTY: not set") {
		t.Fatalf("empty env: %v", err)
	}
	dir := t.TempDir()
	p := filepath.Join(dir, "pw")
	os.WriteFile(p, []byte("file-secret\n"), 0o600)
	if v, err := s.Resolve(ctx, "file:"+p); err != nil || v != "file-secret" {
		t.Fatalf("file: %q %v", v, err)
	}
	crlf := filepath.Join(dir, "crlf")
	os.WriteFile(crlf, []byte("a\r\n"), 0o600)
	if v, _ := s.Resolve(ctx, "file:"+crlf); v != "a" {
		t.Fatalf("crlf: %q", v)
	}
	big := filepath.Join(dir, "big")
	os.WriteFile(big, make([]byte, 64<<10+1), 0o600)
	if _, err := s.Resolve(ctx, "file:"+big); err == nil || !strings.Contains(err.Error(), "larger than 64 KiB") {
		t.Fatalf("big: %v", err)
	}
	if _, err := s.Resolve(ctx, "file:"+filepath.Join(dir, "missing")); err == nil || !strings.Contains(err.Error(), "no such file") {
		t.Fatalf("missing: %v", err)
	}
}

func TestErrorsNeverContainValues(t *testing.T) {
	t.Setenv("QH_TEST_JSONISH", "{not json")
	s := NewSet(false, nil)
	// env and file never fail after reading, so check the aws-sm JSON path
	// through a fake in aws_test.go; here check a read error message.
	_, err := s.Resolve(context.Background(), "file:/definitely/not/here")
	if err == nil || strings.Contains(err.Error(), "{not json") {
		t.Fatalf("%v", err)
	}
}

func TestAllowList(t *testing.T) {
	dir := t.TempDir()
	allowed := filepath.Join(dir, "allowed")
	os.Mkdir(allowed, 0o700)
	os.WriteFile(filepath.Join(allowed, "pw"), []byte("x"), 0o600)
	outside := filepath.Join(dir, "outside")
	os.WriteFile(outside, []byte("y"), 0o600)
	os.Symlink(outside, filepath.Join(allowed, "link"))

	s := NewSet(true, []string{"env:QH_TENANT_*", "file:" + allowed + "/*"})
	for _, ok := range []string{"env:QH_TENANT_DB", "file:" + filepath.Join(allowed, "pw")} {
		if _, err := s.Check(ok); err != nil {
			t.Errorf("%s refused: %v", ok, err)
		}
	}
	for _, bad := range []string{
		"env:HOME", "env:QH_TENANTX", "aws-sm://anything",
		"file:" + allowed + "/../outside",  // .. escape
		"file:" + filepath.Join(allowed, "link"), // symlink escape
	} {
		_, err := s.Check(bad)
		if cat(err) != qerr.Forbidden || !strings.Contains(err.Error(), "is not allowed on this server") ||
			!strings.Contains(err.Error(), "qh serve --allow-secret-ref") {
			t.Errorf("%s: %v", bad, err)
		}
	}
	// Local mode accepts everything.
	if _, err := NewSet(false, nil).Check("env:HOME"); err != nil {
		t.Fatal(err)
	}
}

func TestCheckPattern(t *testing.T) {
	for _, ok := range []string{"env:QH_*", "file:/run/secrets/*", "aws-sm://arn:aws:secretsmanager:*"} {
		if err := CheckPattern(ok); err != nil {
			t.Errorf("%s: %v", ok, err)
		}
	}
	for _, bad := range []string{"", "*", "QH_*", "vault://x"} {
		if err := CheckPattern(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/secretref -count=1`

Expected: FAIL (`undefined: Parse`, …).

- [ ] **Step 3: Implement `internal/secretref/secretref.go`**

```go
// Package secretref resolves secret references (env:NAME, file:/path,
// aws-sm://id[#key]) and, in server mode, holds the operator's allow-list.
// Resolved values are never put in errors.
package secretref

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/anderson-0/qh/internal/qerr"
)

// Ref is a parsed secret reference.
type Ref struct {
	Raw    string // as written
	Scheme string // env, file or aws-sm
	Name   string // variable name, absolute path, or AWS secret id
	Key    string // aws-sm: JSON field of the secret, or ""
}

var envName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

const awsPrefix = "aws-sm://"

func invalid(s, msg string) error {
	return qerr.New(qerr.Invalid, fmt.Sprintf("invalid secret reference %q: %s", s, msg)).
		WithHint("Use env:NAME, file:/absolute/path or aws-sm://<secret-id>[#key]")
}

// Parse reads a reference without resolving it.
func Parse(s string) (Ref, error) {
	switch {
	case strings.HasPrefix(s, "env:"):
		if n := s[len("env:"):]; envName.MatchString(n) {
			return Ref{Raw: s, Scheme: "env", Name: n}, nil
		}
		return Ref{}, invalid(s, "not an environment variable name")
	case strings.HasPrefix(s, "file:"):
		if p := s[len("file:"):]; filepath.IsAbs(p) {
			return Ref{Raw: s, Scheme: "file", Name: p}, nil
		}
		return Ref{}, invalid(s, "the path must be absolute")
	case strings.HasPrefix(s, awsPrefix):
		id, key, hasKey := strings.Cut(s[len(awsPrefix):], "#")
		if id == "" {
			return Ref{}, invalid(s, "the secret id is empty")
		}
		if hasKey && key == "" {
			return Ref{}, invalid(s, "the key after # is empty")
		}
		return Ref{Raw: s, Scheme: "aws-sm", Name: id, Key: key}, nil
	}
	return Ref{}, invalid(s, "unknown scheme")
}

// Resolver returns the value a reference points to.
type Resolver interface {
	Resolve(ctx context.Context, r Ref) (string, error)
}

// Set resolves references; when restricted (server mode) only those matching
// an allowed pattern.
type Set struct {
	restricted bool
	allow      []*regexp.Regexp
	resolvers  map[string]Resolver
}

// NewSet returns a Set with the env, file and aws-sm resolvers. Patterns
// use * for any run of characters; check them first with CheckPattern.
func NewSet(restricted bool, allow []string) *Set {
	s := &Set{restricted: restricted, resolvers: map[string]Resolver{
		"env": envResolver{}, "file": fileResolver{}, "aws-sm": newAWS(),
	}}
	for _, p := range allow {
		s.allow = append(s.allow, glob(p))
	}
	return s
}

// With replaces the resolver for scheme (tests).
func (s *Set) With(scheme string, r Resolver) *Set {
	s.resolvers[scheme] = r
	return s
}

// CheckPattern validates an --allow-secret-ref pattern.
func CheckPattern(p string) error {
	for _, prefix := range []string{"env:", "file:", awsPrefix} {
		if strings.HasPrefix(p, prefix) && len(p) > len(prefix) {
			return nil
		}
	}
	return qerr.New(qerr.Invalid, fmt.Sprintf("invalid --allow-secret-ref pattern %q", p)).
		WithHint("Start it with env:, file: or aws-sm://, e.g. env:QH_TENANT_*")
}

func glob(p string) *regexp.Regexp {
	parts := strings.Split(p, "*")
	for i := range parts {
		parts[i] = regexp.QuoteMeta(parts[i])
	}
	return regexp.MustCompile("^" + strings.Join(parts, ".*") + "$")
}

func (s *Set) allowed(raw string) bool {
	for _, re := range s.allow {
		if re.MatchString(raw) {
			return true
		}
	}
	return false
}

func forbidden(raw string) error {
	return qerr.New(qerr.Forbidden, "secret reference "+raw+" is not allowed on this server").
		WithHint("The operator can allow it with qh serve --allow-secret-ref")
}

// Check parses raw and, in server mode, applies the allow-list. For file:
// the cleaned path and, when the file exists, its symlink-resolved path must
// both match.
func (s *Set) Check(raw string) (Ref, error) {
	r, err := Parse(raw)
	if err != nil || !s.restricted {
		return r, err
	}
	if !s.allowed(raw) {
		return r, forbidden(raw)
	}
	if r.Scheme == "file" {
		clean := filepath.Clean(r.Name)
		if !s.allowed("file:" + clean) {
			return r, forbidden(raw)
		}
		if real, err := filepath.EvalSymlinks(clean); err == nil && !s.allowed("file:"+real) {
			return r, forbidden(raw)
		}
	}
	return r, nil
}

// Resolve checks raw and returns its value, within 10 seconds.
func (s *Set) Resolve(ctx context.Context, raw string) (string, error) {
	r, err := s.Check(raw)
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	v, err := s.resolvers[r.Scheme].Resolve(ctx, r)
	if err != nil {
		var qe *qerr.Error
		if errors.As(err, &qe) {
			return "", qe
		}
		return "", failed(raw, err.Error())
	}
	return v, nil
}

// failed is a resolution error; reason must never contain the value.
func failed(raw, reason string) error {
	return qerr.New(qerr.ConnectionFailed, "secret reference "+raw+": "+reason)
}

type envResolver struct{}

func (envResolver) Resolve(_ context.Context, r Ref) (string, error) {
	if v := os.Getenv(r.Name); v != "" {
		return v, nil
	}
	return "", failed(r.Raw, "not set")
}

const maxFile = 64 << 10

type fileResolver struct{}

func (fileResolver) Resolve(_ context.Context, r Ref) (string, error) {
	f, err := os.Open(filepath.Clean(r.Name))
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return "", failed(r.Raw, "no such file")
	case err != nil:
		return "", failed(r.Raw, "cannot read the file")
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, maxFile+1))
	if err != nil {
		return "", failed(r.Raw, "cannot read the file")
	}
	if len(b) > maxFile {
		return "", failed(r.Raw, "the file is larger than 64 KiB")
	}
	v := strings.TrimSuffix(string(b), "\n")
	return strings.TrimSuffix(v, "\r"), nil
}
```

In this task, `newAWS` is a stub so the package compiles; Task 3 replaces it. Add to the same file:

```go
// newAWS is replaced in Task 3.
func newAWS() Resolver { return awsStub{} }

type awsStub struct{}

func (awsStub) Resolve(_ context.Context, r Ref) (string, error) {
	return "", failed(r.Raw, "AWS Secrets Manager is not available in this build")
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/secretref -count=1 -race`

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/secretref
git commit -m "feat(secretref): parse and resolve env: and file: references with a server allow-list

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 3: The `aws-sm://` resolver

**Files:**
- Create: `internal/secretref/aws.go`
- Modify: `internal/secretref/secretref.go` (delete the `newAWS` stub and `awsStub`)
- Modify: `go.mod`, `go.sum`
- Test: `internal/secretref/aws_test.go`

**Interfaces:**
- Consumes: `Ref`, `Resolver` and `failed(raw, reason)` from Task 2.
- Produces: `func newAWS() Resolver`. The real resolver uses the default credential chain of the qh process, with the region taken from an ARN when one is given.

- [ ] **Step 1: Add the dependencies**

Run:
```bash
go get github.com/aws/aws-sdk-go-v2/config github.com/aws/aws-sdk-go-v2/service/secretsmanager github.com/aws/smithy-go
```

- [ ] **Step 2: Write the failing tests**

`internal/secretref/aws_test.go`:

```go
package secretref

import (
	"context"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
	"github.com/aws/smithy-go"

	"github.com/anderson-0/qh/internal/qerr"
)

type fakeSM struct {
	region  string
	secrets map[string]*string
	err     error
}

func (f *fakeSM) GetSecretValue(_ context.Context, in *secretsmanager.GetSecretValueInput, _ ...func(*secretsmanager.Options)) (*secretsmanager.GetSecretValueOutput, error) {
	if f.err != nil {
		return nil, f.err
	}
	v, ok := f.secrets[aws.ToString(in.SecretId)]
	if !ok {
		return nil, &smithy.GenericAPIError{Code: "ResourceNotFoundException", Message: "nope"}
	}
	return &secretsmanager.GetSecretValueOutput{SecretString: v}, nil
}

func TestAWSResolver(t *testing.T) {
	fake := &fakeSM{secrets: map[string]*string{
		"plain":  aws.String("pw-value"),
		"json":   aws.String(`{"password":"json-pw","port":5432}`),
		"binary": nil,
		"arn:aws:secretsmanager:eu-west-1:123456789012:secret:x": aws.String("by-arn"),
	}}
	var regions []string
	r := &awsResolver{newClient: func(_ context.Context, region string) (smAPI, error) {
		regions = append(regions, region)
		return fake, nil
	}}
	s := NewSet(false, nil).With("aws-sm", r)
	ctx := context.Background()
	for ref, want := range map[string]string{
		"aws-sm://plain":         "pw-value",
		"aws-sm://json#password": "json-pw",
		"aws-sm://json#port":     "5432",
		"aws-sm://arn:aws:secretsmanager:eu-west-1:123456789012:secret:x": "by-arn",
	} {
		if v, err := s.Resolve(ctx, ref); err != nil || v != want {
			t.Errorf("%s: %q %v", ref, v, err)
		}
	}
	if !strings.Contains(strings.Join(regions, ","), "eu-west-1") {
		t.Errorf("ARN region not used: %v", regions)
	}
	for ref, msg := range map[string]string{
		"aws-sm://missing":      "not found",
		"aws-sm://json#nokey":   "no such key",
		"aws-sm://plain#field":  "not JSON",
		"aws-sm://binary":       "binary",
	} {
		_, err := s.Resolve(ctx, ref)
		if qerr.From(err).Category != qerr.ConnectionFailed || !strings.Contains(err.Error(), msg) {
			t.Errorf("%s: %v", ref, err)
		}
		if err != nil && (strings.Contains(err.Error(), "pw-value") || strings.Contains(err.Error(), "json-pw")) {
			t.Errorf("%s leaked a value: %v", ref, err)
		}
	}
	fake.err = &smithy.GenericAPIError{Code: "AccessDeniedException", Message: "denied"}
	if _, err := s.Resolve(ctx, "aws-sm://plain"); err == nil || !strings.Contains(err.Error(), "access denied") {
		t.Errorf("access denied: %v", err)
	}
}

func TestRegionOf(t *testing.T) {
	if got := regionOf("arn:aws:secretsmanager:us-east-2:1:secret:a"); got != "us-east-2" {
		t.Fatal(got)
	}
	if got := regionOf("prod/db"); got != "" {
		t.Fatal(got)
	}
}
```

- [ ] **Step 3: Run the tests to verify they fail**

Run: `go test ./internal/secretref -run 'TestAWS|TestRegionOf' -count=1`

Expected: FAIL (`undefined: awsResolver`).

- [ ] **Step 4: Implement**

Delete `newAWS`, `awsStub` and its `Resolve` from `secretref.go`. Create `internal/secretref/aws.go`:

```go
package secretref

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
	"github.com/aws/smithy-go"

	"github.com/anderson-0/qh/internal/qerr"
)

type smAPI interface {
	GetSecretValue(ctx context.Context, in *secretsmanager.GetSecretValueInput, opts ...func(*secretsmanager.Options)) (*secretsmanager.GetSecretValueOutput, error)
}

// awsResolver reads AWS Secrets Manager with the qh process's default
// credential chain. Clients are made once per region.
type awsResolver struct {
	newClient func(ctx context.Context, region string) (smAPI, error)
	mu        sync.Mutex
	clients   map[string]smAPI
}

func newAWS() Resolver {
	return &awsResolver{newClient: func(ctx context.Context, region string) (smAPI, error) {
		var opts []func(*config.LoadOptions) error
		if region != "" {
			opts = append(opts, config.WithRegion(region))
		}
		cfg, err := config.LoadDefaultConfig(ctx, opts...)
		if err != nil {
			return nil, err
		}
		return secretsmanager.NewFromConfig(cfg), nil
	}}
}

// regionOf returns the region of a Secrets Manager ARN, or "" for a name.
func regionOf(id string) string {
	parts := strings.Split(id, ":")
	if len(parts) >= 4 && parts[0] == "arn" && parts[2] == "secretsmanager" {
		return parts[3]
	}
	return ""
}

func (a *awsResolver) client(ctx context.Context, region string) (smAPI, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if c, ok := a.clients[region]; ok {
		return c, nil
	}
	c, err := a.newClient(ctx, region)
	if err != nil {
		return nil, err
	}
	if a.clients == nil {
		a.clients = map[string]smAPI{}
	}
	a.clients[region] = c
	return c, nil
}

func (a *awsResolver) Resolve(ctx context.Context, r Ref) (string, error) {
	c, err := a.client(ctx, regionOf(r.Name))
	if err != nil {
		return "", qerr.New(qerr.ConnectionFailed, "secret reference "+r.Raw+": cannot load the AWS configuration").
			WithHint("Check the AWS credentials and region of the qh process")
	}
	out, err := c.GetSecretValue(ctx, &secretsmanager.GetSecretValueInput{SecretId: aws.String(r.Name)})
	if err != nil {
		var ae smithy.APIError
		if errors.As(err, &ae) {
			switch ae.ErrorCode() {
			case "ResourceNotFoundException":
				return "", failed(r.Raw, "not found")
			case "AccessDeniedException":
				return "", failed(r.Raw, "access denied")
			case "DecryptionFailure":
				return "", failed(r.Raw, "cannot be decrypted")
			}
			return "", failed(r.Raw, "AWS error "+ae.ErrorCode())
		}
		return "", qerr.New(qerr.ConnectionFailed, "secret reference "+r.Raw+": cannot reach AWS Secrets Manager").
			WithHint("Check the AWS credentials and region of the qh process")
	}
	if out.SecretString == nil {
		return "", failed(r.Raw, "the secret is binary; only string secrets are supported")
	}
	if r.Key == "" {
		return *out.SecretString, nil
	}
	var fields map[string]any
	if json.Unmarshal([]byte(*out.SecretString), &fields) != nil {
		return "", failed(r.Raw, "the secret is not JSON, so #"+r.Key+" cannot be read")
	}
	v, ok := fields[r.Key]
	if !ok {
		return "", failed(r.Raw, "no such key")
	}
	switch v := v.(type) {
	case string:
		return v, nil
	case float64, bool:
		return fmt.Sprint(v), nil
	}
	return "", failed(r.Raw, "field "+r.Key+" is not a string, number or boolean")
}
```

- [ ] **Step 5: Run the tests to verify they pass**

Run: `go mod tidy && go test ./internal/secretref -count=1 -race && go build ./...`

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add go.mod go.sum internal/secretref
git commit -m "feat(secretref): resolve aws-sm:// references from AWS Secrets Manager

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 4: `engine.SSH` — settings, validation, summary

**Files:**
- Create: `internal/engine/ssh.go`
- Modify: `go.mod` (`golang.org/x/crypto` becomes a direct dependency; `go mod tidy` moves it)
- Test: `internal/engine/ssh_test.go`

**Interfaces:**
- Consumes: `secretref.Parse` (Task 2).
- Produces:
  - `type SSH struct { Host string; Port int; User string; HostKey string; PrivateKey, PrivateKeyRef, Passphrase, PassphraseRef, Password, PasswordRef string }`. The JSON tags are `host, port, user, host_key, private_key, private_key_ref, passphrase, passphrase_ref, password, password_ref`, all `omitempty` except host, user and host_key.
  - `type SSHInfo struct { Host string; Port int; User string; HostKey string; Auth string; Refs map[string]string }`. The JSON tags are `host, port, user, host_key, auth, refs,omitempty`. `Auth` is `"key"` or `"password"`. `Refs` maps a field name (`private_key`, `passphrase`, `password`) to its reference.
  - `func (s *SSH) Validate() error`: `qerr.Invalid`. An empty `HostKey` is allowed (spec adjustment 1).
  - `func (s *SSH) Info() *SSHInfo`: nil-safe.
  - `func (s *SSH) RefStrings() []string`: the non-empty references.
  - `func (s *SSH) Addr() string`: `host:port`, with port 22 when 0.

- [ ] **Step 1: Write the failing tests**

`internal/engine/ssh_test.go`:

```go
package engine

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
)

func keyPEM(t *testing.T, passphrase string) string {
	t.Helper()
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	var b *pem.Block
	var err error
	if passphrase == "" {
		b, err = ssh.MarshalPrivateKey(priv, "")
	} else {
		b, err = ssh.MarshalPrivateKeyWithPassphrase(priv, "", []byte(passphrase))
	}
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(b))
}

const fp = "SHA256:47DEQpj8HBSa+/TImW+5JCeuQeRkm5NMpJWZG3hSuFU"

func TestSSHValidate(t *testing.T) {
	key, enc := keyPEM(t, ""), keyPEM(t, "pp")
	ok := []SSH{
		{Host: "bastion", User: "u", HostKey: fp, PrivateKey: key},
		{Host: "bastion", User: "u", HostKey: fp, PrivateKey: enc, Passphrase: "pp"},
		{Host: "bastion", User: "u", HostKey: fp, PrivateKey: enc, PassphraseRef: "env:QH_PP"},
		{Host: "bastion", User: "u", HostKey: fp, PrivateKeyRef: "file:/k", PassphraseRef: "env:P"},
		{Host: "bastion", User: "u", HostKey: fp, Password: "pw"},
		{Host: "bastion", Port: 2222, User: "u", HostKey: fp, PasswordRef: "aws-sm://x#pw"},
		{Host: "bastion", User: "u", PrivateKey: key}, // host key may be empty (fingerprint flow)
	}
	for i, s := range ok {
		if err := s.Validate(); err != nil {
			t.Errorf("ok[%d]: %v", i, err)
		}
	}
	bad := map[string]SSH{
		"ssh.host is required":                  {User: "u", HostKey: fp, Password: "pw"},
		"ssh.user is required":                  {Host: "b", HostKey: fp, Password: "pw"},
		"ssh.port must be between 1 and 65535":  {Host: "b", Port: 70000, User: "u", HostKey: fp, Password: "pw"},
		"ssh.host_key must look like SHA256:":   {Host: "b", User: "u", HostKey: "MD5:aa", Password: "pw"},
		"exactly one of":                        {Host: "b", User: "u", HostKey: fp},
		"exactly one of ":                       {Host: "b", User: "u", HostKey: fp, Password: "pw", PrivateKey: key},
		"ssh.password and ssh.password_ref":     {Host: "b", User: "u", HostKey: fp, Password: "pw", PasswordRef: "env:X"},
		"invalid secret reference":              {Host: "b", User: "u", HostKey: fp, PasswordRef: "env:"},
		"is encrypted":                          {Host: "b", User: "u", HostKey: fp, PrivateKey: enc},
		"does not decrypt":                      {Host: "b", User: "u", HostKey: fp, PrivateKey: enc, Passphrase: "wrong"},
		"is not encrypted":                      {Host: "b", User: "u", HostKey: fp, PrivateKey: key, Passphrase: "pp"},
		"not a PEM private key":                 {Host: "b", User: "u", HostKey: fp, PrivateKey: "junk"},
		"passphrase applies only":               {Host: "b", User: "u", HostKey: fp, Password: "pw", Passphrase: "pp"},
	}
	for want, s := range bad {
		err := s.Validate()
		if err == nil || !strings.Contains(err.Error(), strings.TrimSpace(want)) {
			t.Errorf("%q: %v", want, err)
		}
		if err != nil && (strings.Contains(err.Error(), "BEGIN OPENSSH") || strings.Contains(err.Error(), "wrong")) {
			t.Errorf("%q leaked a secret: %v", want, err)
		}
	}
}

func TestSSHInfoAndRefs(t *testing.T) {
	s := &SSH{Host: "b", User: "u", HostKey: fp, PrivateKeyRef: "file:/k", PassphraseRef: "env:P"}
	i := s.Info()
	if i.Auth != "key" || i.Port != 22 || i.Refs["private_key"] != "file:/k" || i.Refs["passphrase"] != "env:P" {
		t.Fatalf("%+v", i)
	}
	if got := strings.Join(s.RefStrings(), ","); got != "file:/k,env:P" {
		t.Fatal(got)
	}
	if s.Addr() != "b:22" || (&SSH{Host: "::1", Port: 2222}).Addr() != "[::1]:2222" {
		t.Fatal(s.Addr())
	}
	if (*SSH)(nil).Info() != nil {
		t.Fatal("nil info")
	}
	if (&SSH{Host: "b", User: "u", Password: "x"}).Info().Auth != "password" {
		t.Fatal("password auth")
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/engine -run 'TestSSH' -count=1`

Expected: FAIL (`undefined: SSH`).

- [ ] **Step 3: Implement `internal/engine/ssh.go`**

```go
package engine

import (
	"errors"
	"fmt"
	"net"
	"regexp"
	"strconv"
	"strings"

	"golang.org/x/crypto/ssh"

	"github.com/anderson-0/qh/internal/qerr"
	"github.com/anderson-0/qh/internal/secretref"
)

// SSH is a connection's tunnel through a bastion host. The private key,
// passphrase and password are secret: they are sealed with the connection
// string and never shown. The *Ref fields are secret references resolved
// when the pool opens.
type SSH struct {
	Host          string `json:"host"`
	Port          int    `json:"port,omitempty"`
	User          string `json:"user"`
	HostKey       string `json:"host_key"`
	PrivateKey    string `json:"private_key,omitempty"`
	PrivateKeyRef string `json:"private_key_ref,omitempty"`
	Passphrase    string `json:"passphrase,omitempty"`
	PassphraseRef string `json:"passphrase_ref,omitempty"`
	Password      string `json:"password,omitempty"`
	PasswordRef   string `json:"password_ref,omitempty"`
}

// SSHInfo is what may be shown about a tunnel.
type SSHInfo struct {
	Host    string            `json:"host"`
	Port    int               `json:"port"`
	User    string            `json:"user"`
	HostKey string            `json:"host_key"`
	Auth    string            `json:"auth"` // key or password
	Refs    map[string]string `json:"refs,omitempty"`
}

var hostKeyRe = regexp.MustCompile(`^SHA256:[A-Za-z0-9+/]{43}$`)

func sshInvalid(msg string) error {
	return qerr.New(qerr.Invalid, msg).
		WithHint("ssh needs host, user, host_key (SHA256:… from ssh-keygen -lf) and either a private key or a password")
}

func (s *SSH) port() int {
	if s.Port == 0 {
		return 22
	}
	return s.Port
}

// Addr is the bastion's host:port.
func (s *SSH) Addr() string { return net.JoinHostPort(s.Host, strconv.Itoa(s.port())) }

// Validate checks s without connecting. An empty HostKey is allowed: the
// connection test then reports the bastion's fingerprint. Errors never
// contain key material or passwords.
func (s *SSH) Validate() error {
	if strings.TrimSpace(s.Host) == "" {
		return sshInvalid("ssh.host is required")
	}
	if s.Port < 0 || s.Port > 65535 {
		return sshInvalid("ssh.port must be between 1 and 65535")
	}
	if strings.TrimSpace(s.User) == "" {
		return sshInvalid("ssh.user is required")
	}
	if s.HostKey != "" && !hostKeyRe.MatchString(s.HostKey) {
		return sshInvalid("ssh.host_key must look like SHA256:… (43 base64 characters, as printed by ssh-keygen -lf)")
	}
	for _, f := range []struct{ name, lit, ref string }{
		{"private_key", s.PrivateKey, s.PrivateKeyRef},
		{"passphrase", s.Passphrase, s.PassphraseRef},
		{"password", s.Password, s.PasswordRef},
	} {
		if f.lit != "" && f.ref != "" {
			return sshInvalid(fmt.Sprintf("ssh.%s and ssh.%s_ref cannot both be set", f.name, f.name))
		}
		if f.ref != "" {
			if _, err := secretref.Parse(f.ref); err != nil {
				return err
			}
		}
	}
	key := s.PrivateKey != "" || s.PrivateKeyRef != ""
	pw := s.Password != "" || s.PasswordRef != ""
	if key == pw {
		return sshInvalid("ssh needs exactly one of private_key (or private_key_ref) and password (or password_ref)")
	}
	if !key && (s.Passphrase != "" || s.PassphraseRef != "") {
		return sshInvalid("ssh.passphrase applies only with a private key")
	}
	if s.PrivateKey != "" {
		return checkKey(s.PrivateKey, s.Passphrase, s.PassphraseRef != "")
	}
	return nil
}

func checkKey(key, pass string, passRef bool) error {
	_, err := ssh.ParseRawPrivateKey([]byte(key))
	var missing *ssh.PassphraseMissingError
	switch {
	case err == nil:
		if pass != "" {
			return sshInvalid("ssh.passphrase is set but the private key is not encrypted")
		}
		return nil
	case errors.As(err, &missing):
		if passRef {
			return nil
		}
		if pass == "" {
			return sshInvalid("the ssh private key is encrypted; give ssh.passphrase or ssh.passphrase_ref")
		}
		if _, err := ssh.ParseRawPrivateKeyWithPassphrase([]byte(key), []byte(pass)); err != nil {
			return sshInvalid("ssh.passphrase does not decrypt the private key")
		}
		return nil
	}
	return sshInvalid("ssh.private_key is not a PEM private key in OpenSSH or PKCS#8 format")
}

// Info returns the displayable summary of s, or nil when s is nil.
func (s *SSH) Info() *SSHInfo {
	if s == nil {
		return nil
	}
	i := &SSHInfo{Host: s.Host, Port: s.port(), User: s.User, HostKey: s.HostKey, Auth: "password"}
	if s.PrivateKey != "" || s.PrivateKeyRef != "" {
		i.Auth = "key"
	}
	for name, ref := range map[string]string{"private_key": s.PrivateKeyRef, "passphrase": s.PassphraseRef, "password": s.PasswordRef} {
		if ref != "" {
			if i.Refs == nil {
				i.Refs = map[string]string{}
			}
			i.Refs[name] = ref
		}
	}
	return i
}

// RefStrings returns the secret references s uses, in field order.
func (s *SSH) RefStrings() []string {
	var out []string
	for _, r := range []string{s.PrivateKeyRef, s.PassphraseRef, s.PasswordRef} {
		if r != "" {
			out = append(out, r)
		}
	}
	return out
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go mod tidy && go test ./internal/engine -count=1`

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add go.mod go.sum internal/engine/ssh.go internal/engine/ssh_test.go
git commit -m "feat(engine): add SSH tunnel settings with validation and a display summary

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 5: `internal/tunnel` and its test SSH server

**Files:**
- Create: `internal/tunnel/tunnel.go`
- Create: `internal/tunnel/sshtest/sshtest.go`, an in-process SSH server for tests
- Test: `internal/tunnel/tunnel_test.go`

**Interfaces:**
- Consumes: `qerr`; `netguard.DeniedError` (`internal/netguard`, `type DeniedError struct{ Target string }`).
- Produces:
  - `type Config struct { Addr, User, HostKey, PrivateKey, Passphrase, Password string; DialBastion func(ctx context.Context, network, addr string) (net.Conn, error); CheckTarget func(netip.Addr) bool; KeepAlive time.Duration }`
  - `func New(cfg Config) (*Tunnel, error)`
  - `func (t *Tunnel) Connect(ctx context.Context) error`
  - `func (t *Tunnel) Dial(ctx context.Context, network, addr string) (net.Conn, error)`
  - `func (t *Tunnel) Close() error`
  - `sshtest.Start(t testing.TB, o sshtest.Options) *sshtest.Server`, where `Options{User, Password string; AuthorizedKey ssh.PublicKey; Forward func(target string) (net.Conn, error)}`. The server has `Addr string`, `Fingerprint string`, `Targets() []string`, `Conns() int` and `DropClients()`.
  - `sshtest.Key(t testing.TB, passphrase string) (pemText string, pub ssh.PublicKey)`

- [ ] **Step 1: Write the test SSH server, `internal/tunnel/sshtest/sshtest.go`**

```go
// Package sshtest runs an in-process SSH server that forwards direct-tcpip
// channels, for tunnel tests.
package sshtest

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"io"
	"net"
	"strconv"
	"sync"
	"testing"

	"golang.org/x/crypto/ssh"
)

// Options configures the server. Exactly one of Password and AuthorizedKey
// is normally set. Forward dials a requested target; nil uses net.Dial.
type Options struct {
	User          string
	Password      string
	AuthorizedKey ssh.PublicKey
	Forward       func(target string) (net.Conn, error)
}

// Server is a running test SSH server.
type Server struct {
	Addr        string
	Fingerprint string
	mu          sync.Mutex
	targets     []string
	conns       map[*ssh.ServerConn]bool
	accepted    int
}

// Key returns a new ed25519 private key as PEM (encrypted when passphrase is
// set) and its public key.
func Key(t testing.TB, passphrase string) (string, ssh.PublicKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	var b *pem.Block
	if passphrase == "" {
		b, err = ssh.MarshalPrivateKey(priv, "")
	} else {
		b, err = ssh.MarshalPrivateKeyWithPassphrase(priv, "", []byte(passphrase))
	}
	if err != nil {
		t.Fatal(err)
	}
	sp, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(b)), sp
}

// Start listens on 127.0.0.1 and serves until the test ends.
func Start(t testing.TB, o Options) *Server {
	t.Helper()
	_, hostPriv, _ := ed25519.GenerateKey(rand.Reader)
	signer, err := ssh.NewSignerFromKey(hostPriv)
	if err != nil {
		t.Fatal(err)
	}
	cfg := &ssh.ServerConfig{}
	if o.Password != "" {
		cfg.PasswordCallback = func(c ssh.ConnMetadata, pw []byte) (*ssh.Permissions, error) {
			if c.User() == o.User && string(pw) == o.Password {
				return nil, nil
			}
			return nil, io.EOF
		}
	}
	if o.AuthorizedKey != nil {
		cfg.PublicKeyCallback = func(c ssh.ConnMetadata, k ssh.PublicKey) (*ssh.Permissions, error) {
			if c.User() == o.User && string(k.Marshal()) == string(o.AuthorizedKey.Marshal()) {
				return nil, nil
			}
			return nil, io.EOF
		}
	}
	cfg.AddHostKey(signer)
	forward := o.Forward
	if forward == nil {
		forward = func(target string) (net.Conn, error) { return net.Dial("tcp", target) }
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{Addr: ln.Addr().String(), Fingerprint: ssh.FingerprintSHA256(signer.PublicKey()), conns: map[*ssh.ServerConn]bool{}}
	t.Cleanup(func() { ln.Close(); s.DropClients() })
	go func() {
		for {
			nc, err := ln.Accept()
			if err != nil {
				return
			}
			go s.serve(nc, cfg, forward)
		}
	}()
	return s
}

func (s *Server) serve(nc net.Conn, cfg *ssh.ServerConfig, forward func(string) (net.Conn, error)) {
	sc, chans, reqs, err := ssh.NewServerConn(nc, cfg)
	if err != nil {
		nc.Close()
		return
	}
	s.mu.Lock()
	s.conns[sc] = true
	s.accepted++
	s.mu.Unlock()
	defer func() { s.mu.Lock(); delete(s.conns, sc); s.mu.Unlock() }()
	go func() {
		for r := range reqs {
			if r.WantReply {
				r.Reply(r.Type == "keepalive@openssh.com", nil)
			}
		}
	}()
	for nch := range chans {
		if nch.ChannelType() != "direct-tcpip" {
			nch.Reject(ssh.UnknownChannelType, "only direct-tcpip")
			continue
		}
		var p struct {
			Host     string
			Port     uint32
			OrigHost string
			OrigPort uint32
		}
		if err := ssh.Unmarshal(nch.ExtraData(), &p); err != nil {
			nch.Reject(ssh.ConnectionFailed, "bad payload")
			continue
		}
		target := net.JoinHostPort(p.Host, strconv.Itoa(int(p.Port)))
		s.mu.Lock()
		s.targets = append(s.targets, target)
		s.mu.Unlock()
		up, err := forward(target)
		if err != nil {
			nch.Reject(ssh.ConnectionFailed, "connection refused")
			continue
		}
		ch, creqs, err := nch.Accept()
		if err != nil {
			up.Close()
			continue
		}
		go ssh.DiscardRequests(creqs)
		go func() { io.Copy(ch, up); ch.CloseWrite(); ch.Close() }()
		go func() { io.Copy(up, ch); up.Close() }()
	}
	sc.Wait()
}

// Targets returns the targets clients asked the server to reach.
func (s *Server) Targets() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.targets...)
}

// Conns returns how many SSH connections were accepted in total.
func (s *Server) Conns() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.accepted
}

// DropClients closes every open SSH connection (a bastion restart).
func (s *Server) DropClients() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for c := range s.conns {
		c.Close()
	}
}
```

- [ ] **Step 2: Write the failing tunnel tests, `internal/tunnel/tunnel_test.go`**

```go
package tunnel

import (
	"context"
	"errors"
	"io"
	"net"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/anderson-0/qh/internal/netguard"
	"github.com/anderson-0/qh/internal/qerr"
	"github.com/anderson-0/qh/internal/tunnel/sshtest"
)

// echo starts a TCP echo server and returns its address.
func echo(t *testing.T) string {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() { io.Copy(c, c); c.Close() }()
		}
	}()
	return ln.Addr().String()
}

func roundTrip(t *testing.T, c net.Conn) {
	t.Helper()
	defer c.Close()
	c.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := c.Write([]byte("ping")); err != nil {
		t.Fatal(err)
	}
	b := make([]byte, 4)
	if _, err := io.ReadFull(c, b); err != nil || string(b) != "ping" {
		t.Fatalf("%q %v", b, err)
	}
}

func TestKeyAndPasswordAuth(t *testing.T) {
	target := echo(t)
	key, pub := sshtest.Key(t, "")
	enc, encPub := sshtest.Key(t, "pp")
	ctx := context.Background()
	for name, c := range map[string]struct {
		srv sshtest.Options
		cfg Config
	}{
		"key":           {sshtest.Options{User: "u", AuthorizedKey: pub}, Config{User: "u", PrivateKey: key}},
		"encrypted key": {sshtest.Options{User: "u", AuthorizedKey: encPub}, Config{User: "u", PrivateKey: enc, Passphrase: "pp"}},
		"password":      {sshtest.Options{User: "u", Password: "pw"}, Config{User: "u", Password: "pw"}},
	} {
		t.Run(name, func(t *testing.T) {
			srv := sshtest.Start(t, c.srv)
			c.cfg.Addr, c.cfg.HostKey = srv.Addr, srv.Fingerprint
			tun, err := New(c.cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer tun.Close()
			conn, err := tun.Dial(ctx, "tcp", target)
			if err != nil {
				t.Fatal(err)
			}
			roundTrip(t, conn)
		})
	}
}

func TestHostKeyAndAuthFailures(t *testing.T) {
	srv := sshtest.Start(t, sshtest.Options{User: "u", Password: "pw"})
	ctx := context.Background()
	other := "SHA256:47DEQpj8HBSa+/TImW+5JCeuQeRkm5NMpJWZG3hSuFU"
	for want, cfg := range map[string]Config{
		"ssh: host key mismatch for " + srv.Addr + " (got " + srv.Fingerprint + ", expected " + other + ")": {Addr: srv.Addr, User: "u", Password: "pw", HostKey: other},
		"ssh: unknown host key for " + srv.Addr + ": it presented " + srv.Fingerprint:                          {Addr: srv.Addr, User: "u", Password: "pw"},
		"ssh: authentication failed for u@" + srv.Addr:                                                         {Addr: srv.Addr, User: "u", Password: "wrong", HostKey: srv.Fingerprint},
		"ssh: cannot reach the bastion 127.0.0.1:1":                                                             {Addr: "127.0.0.1:1", User: "u", Password: "pw", HostKey: srv.Fingerprint},
	} {
		tun, err := New(cfg)
		if err != nil {
			t.Fatal(err)
		}
		err = tun.Connect(ctx)
		tun.Close()
		if qerr.From(err).Category != qerr.ConnectionFailed || !strings.Contains(err.Error(), want) {
			t.Errorf("want %q, got %v", want, err)
		}
		if err != nil && strings.Contains(err.Error(), "wrong") {
			t.Errorf("password leaked: %v", err)
		}
	}
	tun, _ := New(Config{Addr: srv.Addr, User: "u", Password: "pw"})
	err := tun.Connect(ctx)
	tun.Close()
	if !strings.Contains(err.Error(), "--ssh-host-key "+srv.Fingerprint) {
		t.Errorf("unknown key hint: %v", err)
	}
}

func TestTargetUnreachableFromBastion(t *testing.T) {
	srv := sshtest.Start(t, sshtest.Options{User: "u", Password: "pw"})
	tun, _ := New(Config{Addr: srv.Addr, User: "u", Password: "pw", HostKey: srv.Fingerprint})
	defer tun.Close()
	_, err := tun.Dial(context.Background(), "tcp", "127.0.0.1:1")
	if err == nil || !strings.Contains(err.Error(), "ssh: the bastion could not reach 127.0.0.1:1") {
		t.Fatal(err)
	}
}

func TestReconnectAfterBastionDrop(t *testing.T) {
	target := echo(t)
	srv := sshtest.Start(t, sshtest.Options{User: "u", Password: "pw"})
	tun, _ := New(Config{Addr: srv.Addr, User: "u", Password: "pw", HostKey: srv.Fingerprint})
	defer tun.Close()
	ctx := context.Background()
	c, err := tun.Dial(ctx, "tcp", target)
	if err != nil {
		t.Fatal(err)
	}
	roundTrip(t, c)
	srv.DropClients()
	time.Sleep(50 * time.Millisecond)
	c, err = tun.Dial(ctx, "tcp", target)
	if err != nil {
		t.Fatalf("no reconnect: %v", err)
	}
	roundTrip(t, c)
	if srv.Conns() != 2 {
		t.Fatalf("ssh connections = %d, want 2", srv.Conns())
	}
}

func TestConcurrentDialsShareOneConnection(t *testing.T) {
	target := echo(t)
	srv := sshtest.Start(t, sshtest.Options{User: "u", Password: "pw"})
	tun, _ := New(Config{Addr: srv.Addr, User: "u", Password: "pw", HostKey: srv.Fingerprint})
	defer tun.Close()
	var wg sync.WaitGroup
	errs := make(chan error, 10)
	for range 10 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c, err := tun.Dial(context.Background(), "tcp", target)
			if err != nil {
				errs <- err
				return
			}
			c.Write([]byte("x"))
			b := make([]byte, 1)
			io.ReadFull(c, b)
			c.Close()
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	if srv.Conns() != 1 {
		t.Fatalf("ssh connections = %d, want 1", srv.Conns())
	}
}

func TestServerModeTargetPolicy(t *testing.T) {
	target := echo(t) // 127.0.0.1:port — loopback, exempt
	srv := sshtest.Start(t, sshtest.Options{User: "u", Password: "pw"})
	deny := func(a netip.Addr) bool { return !a.IsPrivate() }
	tun, _ := New(Config{Addr: srv.Addr, User: "u", Password: "pw", HostKey: srv.Fingerprint, CheckTarget: deny})
	defer tun.Close()
	ctx := context.Background()
	c, err := tun.Dial(ctx, "tcp", target)
	if err != nil {
		t.Fatalf("loopback target refused: %v", err)
	}
	roundTrip(t, c)
	_, err = tun.Dial(ctx, "tcp", "10.1.2.3:5432")
	var de *netguard.DeniedError
	if !errors.As(err, &de) {
		t.Fatalf("private target allowed: %v", err)
	}
	_, err = tun.Dial(ctx, "tcp", "no-such-host.invalid:5432")
	if err == nil || !strings.Contains(err.Error(), "Use the database's IP address") {
		t.Fatalf("unresolvable name: %v", err)
	}
	// The bastion is asked for the checked IP, never the name.
	for _, tg := range srv.Targets() {
		if strings.Contains(tg, "localhost") {
			t.Fatalf("name sent to bastion: %v", srv.Targets())
		}
	}
}

func TestBastionDialUsesPolicy(t *testing.T) {
	srv := sshtest.Start(t, sshtest.Options{User: "u", Password: "pw"})
	called := false
	tun, _ := New(Config{Addr: srv.Addr, User: "u", Password: "pw", HostKey: srv.Fingerprint,
		DialBastion: func(ctx context.Context, n, a string) (net.Conn, error) {
			called = true
			return nil, &netguard.DeniedError{Target: a}
		}})
	defer tun.Close()
	err := tun.Connect(context.Background())
	var de *netguard.DeniedError
	if !called || !errors.As(err, &de) {
		t.Fatalf("called=%v err=%v", called, err)
	}
}
```

- [ ] **Step 3: Run the tests to verify they fail**

Run: `go test ./internal/tunnel/... -count=1`

Expected: FAIL (`undefined: New`).

- [ ] **Step 4: Implement `internal/tunnel/tunnel.go`**

```go
// Package tunnel reaches databases through an SSH bastion: one SSH client
// per pool, one direct-tcpip channel per database connection.
package tunnel

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/anderson-0/qh/internal/netguard"
	"github.com/anderson-0/qh/internal/qerr"
)

// Config describes a tunnel. Credentials are already resolved.
type Config struct {
	Addr       string // bastion host:port
	User       string
	HostKey    string // SHA256 fingerprint; "" rejects every key, reporting the one presented
	PrivateKey string // PEM; empty for password authentication
	Passphrase string
	Password   string
	// DialBastion opens the TCP connection to the bastion (server mode: the
	// netguard policy). Nil uses a plain dialer.
	DialBastion func(ctx context.Context, network, addr string) (net.Conn, error)
	// CheckTarget, when set (server mode), must allow every address the
	// target resolves to, except loopback (the bastion's own). The bastion
	// is then sent the checked IP, never the name.
	CheckTarget func(netip.Addr) bool
	KeepAlive   time.Duration // default 30s; three missed replies close the client
}

// Tunnel is safe for concurrent use.
type Tunnel struct {
	cfg    Config
	ccfg   *ssh.ClientConfig
	mu     sync.Mutex
	client *ssh.Client
	closed bool
	stop   chan struct{}
}

type hostKeyError struct{ got string }

func (e *hostKeyError) Error() string { return "host key " + e.got }

// New prepares a tunnel without connecting.
func New(cfg Config) (*Tunnel, error) {
	if cfg.KeepAlive <= 0 {
		cfg.KeepAlive = 30 * time.Second
	}
	if cfg.DialBastion == nil {
		var d net.Dialer
		cfg.DialBastion = d.DialContext
	}
	var auth ssh.AuthMethod
	if cfg.PrivateKey != "" {
		var key any
		var err error
		if cfg.Passphrase != "" {
			key, err = ssh.ParseRawPrivateKeyWithPassphrase([]byte(cfg.PrivateKey), []byte(cfg.Passphrase))
		} else {
			key, err = ssh.ParseRawPrivateKey([]byte(cfg.PrivateKey))
		}
		if err != nil {
			return nil, qerr.New(qerr.ConnectionFailed, "ssh: the private key cannot be read (wrong passphrase or not a PEM private key)")
		}
		signer, err := ssh.NewSignerFromKey(key)
		if err != nil {
			return nil, qerr.New(qerr.ConnectionFailed, "ssh: unsupported private key type")
		}
		auth = ssh.PublicKeys(signer)
	} else {
		auth = ssh.Password(cfg.Password)
	}
	t := &Tunnel{cfg: cfg, stop: make(chan struct{})}
	t.ccfg = &ssh.ClientConfig{
		User: cfg.User, Auth: []ssh.AuthMethod{auth}, Timeout: 15 * time.Second,
		HostKeyCallback: func(_ string, _ net.Addr, key ssh.PublicKey) error {
			if got := ssh.FingerprintSHA256(key); got != cfg.HostKey {
				return &hostKeyError{got: got}
			}
			return nil
		},
	}
	return t, nil
}

func (t *Tunnel) failed(msg string) *qerr.Error { return qerr.New(qerr.ConnectionFailed, "ssh: "+msg) }

// Connect opens the SSH connection now, so errors name the failing stage.
func (t *Tunnel) Connect(ctx context.Context) error {
	_, err := t.get(ctx)
	return err
}

func (t *Tunnel) get(ctx context.Context) (*ssh.Client, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return nil, t.failed("the tunnel is closed")
	}
	if t.client != nil {
		return t.client, nil
	}
	nc, err := t.cfg.DialBastion(ctx, "tcp", t.cfg.Addr)
	if err != nil {
		var de *netguard.DeniedError
		if errors.As(err, &de) {
			return nil, err
		}
		return nil, t.failed("cannot reach the bastion " + t.cfg.Addr + ": " + err.Error())
	}
	if dl, ok := ctx.Deadline(); ok {
		nc.SetDeadline(dl)
	}
	c, chans, reqs, err := ssh.NewClientConn(nc, t.cfg.Addr, t.ccfg)
	if err != nil {
		nc.Close()
		return nil, t.handshakeErr(err)
	}
	nc.SetDeadline(time.Time{})
	t.client = ssh.NewClient(c, chans, reqs)
	go t.keepAlive(t.client)
	return t.client, nil
}

func (t *Tunnel) handshakeErr(err error) error {
	var hk *hostKeyError
	switch {
	case errors.As(err, &hk) && t.cfg.HostKey == "":
		host, port, _ := net.SplitHostPort(t.cfg.Addr)
		return t.failed(fmt.Sprintf("unknown host key for %s: it presented %s", t.cfg.Addr, hk.got)).
			WithHint(fmt.Sprintf("Verify it (ssh-keyscan -p %s %s | ssh-keygen -lf -), then set it with --ssh-host-key %s", port, host, hk.got))
	case errors.As(err, &hk):
		return t.failed(fmt.Sprintf("host key mismatch for %s (got %s, expected %s)", t.cfg.Addr, hk.got, t.cfg.HostKey)).
			WithHint("If the bastion's key really changed, verify the new one and edit the connection")
	case strings.Contains(err.Error(), "unable to authenticate"):
		return t.failed(fmt.Sprintf("authentication failed for %s@%s", t.cfg.User, t.cfg.Addr))
	}
	return t.failed("handshake with the bastion " + t.cfg.Addr + " failed")
}

func (t *Tunnel) drop(c *ssh.Client) {
	t.mu.Lock()
	if t.client == c {
		t.client = nil
	}
	t.mu.Unlock()
	c.Close()
}

func (t *Tunnel) keepAlive(c *ssh.Client) {
	tick := time.NewTicker(t.cfg.KeepAlive)
	defer tick.Stop()
	gone := make(chan struct{})
	go func() { c.Wait(); close(gone) }()
	missed := 0
	for {
		select {
		case <-t.stop:
			return
		case <-gone:
			t.drop(c)
			return
		case <-tick.C:
			reply := make(chan error, 1)
			go func() { _, _, err := c.SendRequest("keepalive@openssh.com", true, nil); reply <- err }()
			select {
			case err := <-reply:
				if err != nil {
					missed++
				} else {
					missed = 0
				}
			case <-time.After(t.cfg.KeepAlive):
				missed++
			}
			if missed >= 3 {
				t.drop(c)
				return
			}
		}
	}
}

// target applies the server-mode policy to addr and returns what the
// bastion is asked to reach.
func (t *Tunnel) target(ctx context.Context, addr string) (string, error) {
	if t.cfg.CheckTarget == nil {
		return addr, nil
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return "", t.failed("invalid database address " + addr)
	}
	var ips []netip.Addr
	if ip, err := netip.ParseAddr(host); err == nil {
		ips = []netip.Addr{ip}
	} else if ips, err = net.DefaultResolver.LookupNetIP(ctx, "ip", host); err != nil || len(ips) == 0 {
		return "", t.failed("cannot resolve "+host+" to check it against this server's network policy").
			WithHint("Use the database's IP address")
	}
	for _, ip := range ips {
		ip = ip.Unmap()
		if !ip.IsLoopback() && !t.cfg.CheckTarget(ip) {
			return "", &netguard.DeniedError{Target: addr}
		}
	}
	return net.JoinHostPort(ips[0].Unmap().String(), port), nil
}

// Dial opens a connection to addr through the bastion. A dropped SSH
// connection is reopened once.
func (t *Tunnel) Dial(ctx context.Context, network, addr string) (net.Conn, error) {
	target, err := t.target(ctx, addr)
	if err != nil {
		return nil, err
	}
	var last error
	for range 2 {
		c, err := t.get(ctx)
		if err != nil {
			return nil, err
		}
		conn, err := c.DialContext(ctx, "tcp", target)
		if err == nil {
			return conn, nil
		}
		var oce *ssh.OpenChannelError
		if errors.As(err, &oce) {
			return nil, t.failed("the bastion could not reach " + addr + ": " + oce.Message)
		}
		if ctx.Err() != nil {
			return nil, err
		}
		last = err
		t.drop(c)
	}
	return nil, t.failed("the connection to the bastion was lost: " + last.Error())
}

// Close closes the SSH connection; Dial fails afterwards.
func (t *Tunnel) Close() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return nil
	}
	t.closed = true
	close(t.stop)
	if t.client != nil {
		t.client.Close()
		t.client = nil
	}
	return nil
}
```

Check before relying on it: `netguard.DeniedError` must be the exported type with a `Target` field (`internal/netguard/netguard.go:32`). If `ssh.Client.DialContext` is missing in this `x/crypto` version (`go doc golang.org/x/crypto/ssh Client.DialContext`), use `c.Dial("tcp", target)`. If you do, stop the dial when `ctx` ends: close the returned conn when ctx is done before it returns.

- [ ] **Step 5: Run the tests to verify they pass**

Run: `go test ./internal/tunnel/... -count=1 -race`

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/tunnel go.mod go.sum
git commit -m "feat(tunnel): reach databases through an SSH bastion with a pinned host key

One SSH client per tunnel, reconnected once when it drops, with
keep-alives; in server mode the target is resolved, checked against the
network policy (loopback is the bastion's own and exempt) and sent to
the bastion as an IP.

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 6: Connection model — references and `ssh` in core and the store

**Files:**
- Modify: `internal/store/sqlite.go` (migration, `connColumns`, Create/Update/scan), `internal/store/store.go` (`Connection`)
- Modify: `internal/core/sealed.go` (envelope)
- Modify: `internal/core/connections.go` (`ConnInput`, `ConnPatch`, `ConnInfo`, `normalize`, `connString`, `load`, `fill`, `toInfo`, `info`, Create/Update/TestInput)
- Modify: `internal/core/service.go` (`Options`, `Service.refs`, `New`, `checkInput`, `resolve`)
- Test: `internal/core/refs_test.go`, `internal/store/sqlite_test.go` (one migration test)

**Interfaces:**
- Consumes:
  - `engine.SSH` and its `Validate`, `Info`, `RefStrings` (Task 4);
  - `secretref.NewSet`, `Set.Check` (Task 2).
- Produces:
  - `core.ConnInput` gains `DSNRef, PasswordRef string; SSH *engine.SSH`.
  - `core.ConnPatch` gains `DSNRef, PasswordRef *string; SSH *engine.SSH; ClearSSH bool`. Setting `DSN` clears `DSNRef` and vice versa; an empty `*PasswordRef` removes the reference.
  - `core.ConnInfo` gains `SSH *engine.SSHInfo; DSNRef, PasswordRef string`.
  - `core.Options` gains `SecretRefs []string`, the server allow patterns; `DenySSH bool`; and `CheckTarget func(netip.Addr) bool`.
  - `store.Connection` gains `SSH string` (JSON `SSHInfo`, or "") and `Refs string` (JSON `{"dsn_ref":…,"password_ref":…}`, or "").
  - The envelope gains `SSH`, `DSNRef` and `PasswordRef`. `decodeSecret` returns the whole envelope.

This task changes only the model and validation. Opening through refs and tunnels is Task 7, so the tests here create connections with `test=false`.

- [ ] **Step 1: Write the failing tests**

`internal/core/refs_test.go`:

```go
package core

import (
	"context"
	"strings"
	"testing"

	"github.com/anderson-0/qh/internal/engine"
	"github.com/anderson-0/qh/internal/qerr"
)

const fp = "SHA256:47DEQpj8HBSa+/TImW+5JCeuQeRkm5NMpJWZG3hSuFU"

func TestRefsAndSSHAreStoredAndShown(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	in := ConnInput{Name: "pg", Engine: "postgres", DSNRef: "env:QH_PG_DSN", PasswordRef: "file:/run/secrets/pw",
		SSH: &engine.SSH{Host: "bastion", User: "u", HostKey: fp, Password: "ssh-pw"}}
	info, _, err := h.svc.CreateConnection(ctx, ws, in, false)
	if err != nil {
		t.Fatal(err)
	}
	if info.DSNRef != "env:QH_PG_DSN" || info.PasswordRef != "file:/run/secrets/pw" || info.SSH == nil ||
		info.SSH.Host != "bastion" || info.SSH.Port != 22 || info.SSH.Auth != "password" || info.DSN != "(from env:QH_PG_DSN)" {
		t.Fatalf("%+v %+v", info, info.SSH)
	}
	got, err := h.svc.GetConnection(ctx, ws, "pg")
	if err != nil || got.DSNRef != "env:QH_PG_DSN" || got.SSH == nil || got.SSH.HostKey != fp {
		t.Fatalf("%+v %v", got, err)
	}
	// The SSH password is sealed, never in the summary.
	rec, _ := h.st.GetConnection(ctx, ws, "pg")
	if strings.Contains(rec.SSH, "ssh-pw") || strings.Contains(rec.Refs, "ssh-pw") {
		t.Fatal("ssh password stored in plain columns")
	}
	in2, err := h.svc.load(rec)
	if err != nil || in2.SSH == nil || in2.SSH.Password != "ssh-pw" || in2.DSNRef != "env:QH_PG_DSN" {
		t.Fatalf("load: %+v %v", in2, err)
	}
}

func TestSwitchingBetweenDSNAndRef(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	if _, _, err := h.svc.CreateConnection(ctx, ws, ConnInput{Name: "pg", Engine: "postgres", DSN: "postgres://u@db/app"}, false); err != nil {
		t.Fatal(err)
	}
	ref := "env:QH_PG_DSN"
	info, _, err := h.svc.UpdateConnection(ctx, ws, "pg", ConnPatch{DSNRef: &ref}, false)
	if err != nil || info.DSNRef != ref || info.DSN != "(from env:QH_PG_DSN)" {
		t.Fatalf("to ref: %+v %v", info, err)
	}
	dsn := "postgres://u@db2/app"
	info, _, err = h.svc.UpdateConnection(ctx, ws, "pg", ConnPatch{DSN: &dsn}, false)
	if err != nil || info.DSNRef != "" || info.Host != "db2" {
		t.Fatalf("back to dsn: %+v %v", info, err)
	}
	if _, _, err := h.svc.UpdateConnection(ctx, ws, "pg", ConnPatch{DSN: &dsn, DSNRef: &ref}, false); err == nil {
		t.Fatal("dsn and dsn_ref together accepted")
	}
}

func TestRefAndSSHValidation(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	for want, in := range map[string]ConnInput{
		"give dsn_ref or dsn":              {Name: "a", Engine: "postgres", DSN: "postgres://x@y/z", DSNRef: "env:X"},
		"invalid secret reference":         {Name: "a", Engine: "postgres", DSNRef: "env:"},
		"password_ref applies only":        {Name: "a", Engine: "sqlite", DSN: "/tmp/x.db", PasswordRef: "env:X"},
		"ssh applies only":                 {Name: "a", Engine: "sqlite", DSN: "/tmp/x.db", SSH: &engine.SSH{Host: "b", User: "u", HostKey: fp, Password: "p"}},
		"ssh.user is required":             {Name: "a", Engine: "postgres", DSN: "postgres://x@y/z", SSH: &engine.SSH{Host: "b", HostKey: fp, Password: "p"}},
		"ssh.host_key is required":         {Name: "a", Engine: "postgres", DSN: "postgres://x@y/z", SSH: &engine.SSH{Host: "b", User: "u", Password: "p"}},
	} {
		_, _, err := h.svc.CreateConnection(ctx, ws, in, false)
		if qerr.From(err).Category != qerr.Invalid || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: %v", want, err)
		}
	}
}

func TestServerModeAllowList(t *testing.T) {
	st := openStore(t)
	svc := newServiceWith(t, st, Options{Dial: dialNowhere, SecretRefs: []string{"env:QH_TENANT_*"}, DenySSH: true})
	ctx := context.Background()
	_, _, err := svc.CreateConnection(ctx, ws, ConnInput{Name: "a", Engine: "postgres", DSNRef: "env:HOME"}, false)
	if qerr.From(err).Category != qerr.Forbidden || !strings.Contains(err.Error(), "env:HOME is not allowed on this server") {
		t.Fatalf("env:HOME: %v", err)
	}
	if _, _, err := svc.CreateConnection(ctx, ws, ConnInput{Name: "b", Engine: "postgres", DSNRef: "env:QH_TENANT_DB"}, false); err != nil {
		t.Fatalf("allowed ref: %v", err)
	}
	_, _, err = svc.CreateConnection(ctx, ws, ConnInput{Name: "c", Engine: "postgres", DSN: "postgres://x@10.0.0.1/z",
		SSH: &engine.SSH{Host: "b", User: "u", HostKey: fp, Password: "p"}}, false)
	if qerr.From(err).Category != qerr.Forbidden || !strings.Contains(err.Error(), "SSH tunnels are disabled on this server") {
		t.Fatalf("deny ssh: %v", err)
	}
	_, _, err = svc.CreateConnection(ctx, ws, ConnInput{Name: "d", Engine: "postgres", DSN: "postgres://x@10.0.0.1/z",
		SSH: &engine.SSH{Host: "b", User: "u", HostKey: fp, PasswordRef: "env:HOME"}}, false)
	if qerr.From(err).Category != qerr.Forbidden {
		t.Fatalf("ssh ref checked before deny-ssh or not at all: %v", err)
	}
}
```

`dialNowhere` is a test helper. Add it to `refs_test.go` if no equivalent exists. It sets `Options.Dial` so the service is in server mode without dialing anywhere:

```go
func dialNowhere(context.Context, string, string) (net.Conn, error) { return nil, errors.New("no network in tests") }
```

Use the existing `openStore` and `newServiceWith` helpers (`internal/core/querylog_test.go` uses them). Add the `errors` and `net` imports.

Add to `internal/store/sqlite_test.go` a check that a store created before this migration upgrades. Open a fresh store and check that `CreateConnection` and `GetConnection` round-trip `SSH: "{\"host\":\"b\"}"` and `Refs: "{\"dsn_ref\":\"env:X\"}"`, and that empty values come back as `""`. Follow the style of the existing connection tests in that file.

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/core -run 'TestRefs|TestSwitching|TestRefAndSSH|TestServerModeAllowList' -count=1`

Expected: FAIL to compile (`unknown field DSNRef`).

- [ ] **Step 3: Store**

In `internal/store/sqlite.go`, append a fourth migration to `migrations`:

```go
	{
		`ALTER TABLE connections ADD COLUMN ssh TEXT NOT NULL DEFAULT '{}'`,
		`ALTER TABLE connections ADD COLUMN refs TEXT NOT NULL DEFAULT '{}'`,
	},
```

Make these changes:

- Add `, ssh, refs` to the end of `connColumns`.
- In `CreateConnection`, add two more `?` placeholders and pass `tlsColumn(c.SSH), tlsColumn(c.Refs)`. `tlsColumn` already maps "" to `'{}'`; rename it to `jsonColumn` and update its callers, because it now serves three columns.
- In `UpdateConnection`, add `ssh = ?, refs = ?` with the same values.
- In `scanConnection`, scan two more strings and map `"{}"` to `""` as for `tls`.

In `internal/store/store.go` `Connection`, add after `TLS`:

```go
	SSH  string // JSON summary of the SSH tunnel (engine.SSHInfo); "" when there is none
	Refs string // JSON {"dsn_ref","password_ref"}; "" when the connection uses none
```

- [ ] **Step 4: Envelope**

Replace `internal/core/sealed.go`'s `envelope`, `encodeSecret` and `decodeSecret` with:

```go
// envelope is the plaintext of a sealed secret: the connection string, the
// TLS and SSH settings (whose keys and passwords are secret too) and the
// secret references.
type envelope struct {
	V           int         `json:"v"`
	DSN         string      `json:"dsn"`
	TLS         *engine.TLS `json:"tls,omitempty"`
	SSH         *engine.SSH `json:"ssh,omitempty"`
	DSNRef      string      `json:"dsn_ref,omitempty"`
	PasswordRef string      `json:"password_ref,omitempty"`
}

func encodeSecret(in ConnInput) ([]byte, error) {
	return json.Marshal(envelope{V: 2, DSN: in.DSN, TLS: in.TLS, SSH: in.SSH, DSNRef: in.DSNRef, PasswordRef: in.PasswordRef})
}

func decodeSecret(pt []byte) (envelope, error) {
	if !bytes.HasPrefix(pt, envelopePrefix) {
		return envelope{DSN: string(pt)}, nil
	}
	var e envelope
	err := json.Unmarshal(pt, &e)
	return e, err
}
```

Update every caller (`grep -rn 'decodeSecret\|encodeSecret' internal/core`), including the tests and `rotate.go` if it calls them. Envelopes written by Plan 4, which lack the new keys, still decode, so add one test to the existing sealed tests: an old `{"v":2,"dsn":"x","tls":null}` decodes with an empty `SSH` and empty references.

- [ ] **Step 5: Core model**

In `internal/core/connections.go`:

Add to `ConnInput` after `TLS`:

```go
	DSNRef      string      // secret reference for the whole connection string (instead of DSN)
	PasswordRef string      // secret reference for the database password
	SSH         *engine.SSH // tunnel through a bastion
```

Add to `ConnPatch`:

```go
	DSNRef      *string     // replaces the connection string with a reference (clears DSN)
	PasswordRef *string     // "" removes the password reference
	SSH         *engine.SSH // replaces the tunnel
	ClearSSH    bool        // removes the tunnel
```

Add to `ConnInfo` after `TLS`:

```go
	SSH         *engine.SSHInfo
	DSNRef      string
	PasswordRef string
```

At the top of `connString`, add a `dsn_ref` branch:

```go
	if ref := strings.TrimSpace(in.DSNRef); ref != "" {
		if strings.TrimSpace(in.DSN) != "" || in.Host != "" || in.Port != 0 || in.Database != "" || in.Username != "" || in.Password != "" || len(in.Params) > 0 {
			return "", qerr.New(qerr.Invalid, "give dsn_ref or dsn (or host, port, database, username, password and params), not both")
		}
		return "", nil
	}
```

In `normalize`, after the `connString` call and before the TLS block:

```go
	in.DSNRef, in.PasswordRef = strings.TrimSpace(in.DSNRef), strings.TrimSpace(in.PasswordRef)
	for _, r := range []string{in.DSNRef, in.PasswordRef} {
		if r != "" {
			if _, err := secretref.Parse(r); err != nil {
				return in, err
			}
		}
	}
	if in.PasswordRef != "" && in.Engine == "sqlite" {
		return in, qerr.New(qerr.Invalid, "password_ref applies only to network databases")
	}
	if in.SSH != nil {
		if in.Engine == "sqlite" {
			return in, qerr.New(qerr.Invalid, "ssh applies only to network databases")
		}
		s := *in.SSH
		s.Host, s.User = strings.TrimSpace(s.Host), strings.TrimSpace(s.User)
		if s.Port == 0 {
			s.Port = 22
		}
		if err := s.Validate(); err != nil {
			return in, err
		}
		in.SSH = &s
	}
```

`load` copies the new envelope fields:

```go
	e, err := decodeSecret(pt)
	...
	return ConnInput{Name: rec.Name, Engine: rec.Engine, DSN: e.DSN, TLS: e.TLS, SSH: e.SSH, DSNRef: e.DSNRef, PasswordRef: e.PasswordRef, …}
```

`fill` calls `encodeSecret(in)` and also writes the summaries:

```go
	rec.SSH, rec.Refs = "", ""
	if in.SSH != nil {
		b, err := json.Marshal(in.SSH.Info())
		if err != nil {
			return err
		}
		rec.SSH = string(b)
	}
	if in.DSNRef != "" || in.PasswordRef != "" {
		b, err := json.Marshal(map[string]string{"dsn_ref": in.DSNRef, "password_ref": in.PasswordRef})
		if err != nil {
			return err
		}
		rec.Refs = string(b)
	}
```

Add parse helpers next to `tlsInfo`:

```go
func sshInfo(s string) *engine.SSHInfo {
	if s == "" {
		return nil
	}
	var i engine.SSHInfo
	if json.Unmarshal([]byte(s), &i) != nil {
		return nil
	}
	return &i
}

func refsOf(s string) (dsnRef, passwordRef string) {
	var m map[string]string
	if s != "" && json.Unmarshal([]byte(s), &m) == nil {
		return m["dsn_ref"], m["password_ref"]
	}
	return "", ""
}
```

In `toInfo`, set `SSH: sshInfo(rec.SSH)`, and `DSNRef`, `PasswordRef` from `refsOf(rec.Refs)`.

Everywhere `checkDSN`/`Inspect` runs on `in.DSN` (`CreateConnection`, `UpdateConnection`, `TestInput`, `info`), skip both when `in.DSNRef != ""`. Use `info := engine.DSNInfo{Redacted: "(from " + in.DSNRef + ")"}` instead. Write it once as a helper and use it in all four places:

```go
// inspect checks and parses in's connection string; a dsn_ref connection's
// string is only known when the pool opens.
func (s *Service) inspect(eng engine.Engine, in ConnInput) (engine.DSNInfo, error) {
	if in.DSNRef != "" {
		return engine.DSNInfo{Redacted: "(from " + in.DSNRef + ")"}, nil
	}
	if err := s.checkDSN(eng, in.DSN); err != nil {
		return engine.DSNInfo{}, err
	}
	info, err := eng.Inspect(in.DSN)
	if err != nil {
		return info, scrub(err, in.DSN)
	}
	return info, nil
}
```

In `info(rec)`, call `s.inspect` and keep the existing fallbacks:

- if `inspect` returns a `checkDSN` error, use `"<connection string refused by this server>"`;
- if the secret cannot be decrypted, use `"<cannot decrypt with the current master key>"`.

`UpdateConnection` applies the new patch fields before `normalize`:

```go
	if p.DSN != nil && p.DSNRef != nil {
		return nil, nil, qerr.New(qerr.Invalid, "give dsn or dsn_ref, not both")
	}
	if p.DSN != nil {
		in.DSNRef = ""
	}
	if p.DSNRef != nil {
		in.DSN, in.DSNRef = "", *p.DSNRef
	}
	if p.PasswordRef != nil {
		in.PasswordRef = *p.PasswordRef
	}
	if p.ClearSSH && p.SSH != nil {
		return nil, nil, qerr.New(qerr.Invalid, "give ssh or clear it, not both")
	}
	if p.ClearSSH {
		in.SSH = nil
	}
	if p.SSH != nil {
		in.SSH = p.SSH
	}
```

Also extend its test condition to `p.DSN != nil || p.DSNRef != nil || p.PasswordRef != nil || p.SSH != nil || p.ClearSSH || p.Mode != nil || p.TLS != nil || p.ClearTLS`.

In `internal/core/service.go`:

```go
	// SecretRefs are the --allow-secret-ref patterns; in server mode only
	// matching references are accepted. Local mode accepts every reference.
	SecretRefs []string
	// DenySSH refuses connections that use an SSH tunnel.
	DenySSH bool
	// CheckTarget is the network policy's address check (server mode); SSH
	// tunnel targets must pass it.
	CheckTarget func(netip.Addr) bool
```

Add these fields to `Options`. Add `refs *secretref.Set` to `Service`, and build it in `New` after `opts` is set:

```go
	s.refs = secretref.NewSet(s.opts.Dial != nil, s.opts.SecretRefs)
```

Add this check, and call it in `CreateConnection`, `UpdateConnection` and `TestInput` right after `allowEngine`:

```go
// checkInput applies the server's limits on tunnels and secret references.
func (s *Service) checkInput(in ConnInput) error {
	refs := []string{in.DSNRef, in.PasswordRef}
	if in.SSH != nil {
		refs = append(refs, in.SSH.RefStrings()...)
	}
	for _, r := range refs {
		if r == "" {
			continue
		}
		if _, err := s.refs.Check(r); err != nil {
			return err
		}
	}
	if in.SSH != nil && s.opts.DenySSH {
		return qerr.New(qerr.Forbidden, "SSH tunnels are disabled on this server").
			WithHint("The operator turned them off with qh serve --deny-ssh")
	}
	return nil
}
```

`resolve` must call `s.checkInput(in)` after `load`. That refuses a stored reference that is no longer allowed after a restart with narrower flags.

In `CreateConnection` and `UpdateConnection`, after the optional test and before `fill`:

```go
	if in.SSH != nil && in.SSH.HostKey == "" {
		return nil, nil, qerr.New(qerr.Invalid, "ssh.host_key is required").
			WithHint("Run without --skip-test to see the bastion's fingerprint, verify it, then pass --ssh-host-key")
	}
```

- [ ] **Step 6: Run the tests to verify they pass**

Run: `go vet ./... && go test ./internal/core ./internal/store ./internal/cli ./internal/httpapi -count=1`

Expected: PASS. CLI and REST are not wired yet, so their existing tests are only regression checks.

- [ ] **Step 7: Commit**

```bash
git add internal/core internal/store
git commit -m "feat(core): store secret references and SSH tunnels with connections

dsn_ref, password_ref and ssh are sealed with the connection; plain
summaries go in two new columns. Server mode refuses references outside
--allow-secret-ref and, with --deny-ssh, tunnels.

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 7: Opening through references and tunnels

**Files:**
- Create: `internal/core/open.go` (move `OpenConn` from `service.go` and extend it)
- Modify: `internal/engine/engine.go` (`ConnSpec` fields)
- Modify: `internal/core/service.go` (`connSpec`, `runQuery`, the catalog path), `internal/core/connections.go` (`testConn`, `TestConnection`)
- Test: `internal/core/open_test.go` (unit, with the in-process bastion), `internal/core/tunnel_pg_test.go` (Docker)

**Interfaces:**
- Consumes: `tunnel.New`, `Config`, `Connect`, `Dial`, `Close` (Task 5); `sshtest` (Task 5); `Set.Resolve` (Task 2); `ConnInput.SSH`, `DSNRef`, `PasswordRef` and `Options.CheckTarget` (Task 6).
- Produces: new `ConnSpec` fields:
  - `SSH *SSH`
  - `DSNRef, PasswordRef string`
  - `Resolve func(ctx context.Context, ref string) (string, error)`
  - `CheckTarget func(netip.Addr) bool`

  `core.OpenConn(ctx, spec)` resolves them; engines never read them.

- [ ] **Step 1: Add the `ConnSpec` fields**

In `internal/engine/engine.go` `ConnSpec`, after `Password`:

```go
	// SSH, DSNRef and PasswordRef are resolved by core.OpenConn (the pool
	// opener) into Dial, DSN and Password; engines never read them.
	SSH         *SSH
	DSNRef      string
	PasswordRef string
	// Resolve resolves a secret reference, applying the server allow-list.
	Resolve func(ctx context.Context, ref string) (string, error)
	// CheckTarget is the network policy's address check in server mode.
	CheckTarget func(netip.Addr) bool
```

Add `"net/netip"` to the imports.

- [ ] **Step 2: Write the failing tests**

`internal/core/open_test.go`:

```go
package core

import (
	"context"
	"strings"
	"testing"

	"github.com/anderson-0/qh/internal/engine"
	"github.com/anderson-0/qh/internal/qerr"
	"github.com/anderson-0/qh/internal/tunnel/sshtest"
)

func TestOpenConnUnknownHostKeyNamesFingerprint(t *testing.T) {
	srv := sshtest.Start(t, sshtest.Options{User: "u", Password: "pw"})
	h := newHarness(t)
	_, err := h.svc.TestInput(context.Background(), ws, ConnInput{Engine: "postgres", DSN: "postgres://u:dbpw@db.internal:5432/app?sslmode=disable",
		SSH: &engine.SSH{Host: "127.0.0.1", Port: portOf(t, srv.Addr), User: "u", Password: "pw"}})
	if err == nil || !strings.Contains(err.Error(), "unknown host key") || !strings.Contains(err.Error(), "--ssh-host-key "+srv.Fingerprint) {
		t.Fatalf("%v", err)
	}
}

func TestOpenConnResolvesRefsAndScrubsThem(t *testing.T) {
	h := newHarness(t)
	t.Setenv("QH_TEST_DSN", "postgres://u:resolved-dsn-pw@127.0.0.1:1/app?sslmode=disable&connect_timeout=2")
	t.Setenv("QH_TEST_PW", "resolved-pw-value")
	_, err := h.svc.TestInput(context.Background(), ws, ConnInput{Engine: "postgres", DSNRef: "env:QH_TEST_DSN", PasswordRef: "env:QH_TEST_PW"})
	if err == nil {
		t.Fatal("connected to port 1")
	}
	if s := err.Error(); strings.Contains(s, "resolved-dsn-pw") || strings.Contains(s, "resolved-pw-value") {
		t.Fatalf("resolved secret in error: %v", err)
	}
	_, err = h.svc.TestInput(context.Background(), ws, ConnInput{Engine: "postgres", DSNRef: "env:QH_TEST_UNSET"})
	if qerr.From(err).Category != qerr.ConnectionFailed || !strings.Contains(err.Error(), "env:QH_TEST_UNSET: not set") {
		t.Fatalf("unset: %v", err)
	}
}
```

Add `portOf(t, addr) int` (`net.SplitHostPort` + `strconv.Atoi`) to the test file.

`internal/core/tunnel_pg_test.go` (Docker). The database is given to qh only as `db.internal:5432`, a name that resolves nowhere. The test bastion's `Forward` maps that name to the container's real address, so a query that works proves the traffic went through the tunnel.

```go
package core

import (
	"context"
	"net"
	"net/url"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/anderson-0/qh/internal/engine"
	"github.com/anderson-0/qh/internal/tunnel/sshtest"
)

func startPG(t *testing.T) (realAddr, user, password string) {
	if testing.Short() {
		t.Skip("needs Docker")
	}
	testcontainers.SkipIfProviderIsNotHealthy(t)
	ctx := context.Background()
	ctr, err := tcpostgres.Run(ctx, "postgres:17-alpine", tcpostgres.WithDatabase("app"),
		tcpostgres.WithUsername("app"), tcpostgres.WithPassword("first-pw"), tcpostgres.BasicWaitStrategies())
	t.Cleanup(func() { _ = testcontainers.TerminateContainer(ctr) })
	if err != nil {
		t.Fatal(err)
	}
	dsn, err := ctr.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(dsn)
	return u.Host, "app", "first-pw"
}

func TestQueryThroughTunnelAndRotatePassword(t *testing.T) {
	real, user, pw := startPG(t)
	key, pub := sshtest.Key(t, "")
	srv := sshtest.Start(t, sshtest.Options{User: "jump", AuthorizedKey: pub,
		Forward: func(target string) (net.Conn, error) {
			if target != "db.internal:5432" {
				return nil, net.UnknownNetworkError(target)
			}
			return net.Dial("tcp", real)
		}})
	h := newHarness(t)
	ctx := context.Background()
	t.Setenv("QH_TEST_PG_PW", pw)
	_, tr, err := h.svc.CreateConnection(ctx, ws, ConnInput{Name: "pg", Engine: "postgres", PoolSize: 1,
		DSN: "postgres://" + user + "@db.internal:5432/app?sslmode=disable", PasswordRef: "env:QH_TEST_PG_PW",
		SSH: &engine.SSH{Host: "127.0.0.1", Port: portOf(t, srv.Addr), User: "jump", HostKey: srv.Fingerprint, PrivateKey: key}}, true)
	if err != nil || tr == nil {
		t.Fatalf("create: %v", err)
	}
	out, err := h.svc.Query(ctx, ws, QueryInput{Source: "pg", Query: "SELECT current_user"})
	if err != nil || !strings.Contains(out, user) {
		t.Fatalf("query: %q %v", out, err)
	}
	if got := srv.Targets(); len(got) == 0 || got[0] != "db.internal:5432" {
		t.Fatalf("targets %v", got)
	}

	// Rotate: change the password in Postgres and in the environment, then
	// end the pooled session so the next call must reconnect.
	admin, err := pgx.Connect(ctx, "postgres://"+user+":"+pw+"@"+real+"/app?sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close(ctx)
	if _, err := admin.Exec(ctx, "ALTER USER app PASSWORD 'second-pw'"); err != nil {
		t.Fatal(err)
	}
	t.Setenv("QH_TEST_PG_PW", "second-pw")
	if _, err := admin.Exec(ctx, "SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE usename = 'app' AND pid <> pg_backend_pid()"); err != nil {
		t.Fatal(err)
	}
	var lastErr error
	for range 3 { // the first call may fail on the dead session; the pool is then dropped and reopened
		if out, lastErr = h.svc.Query(ctx, ws, QueryInput{Source: "pg", Query: "SELECT 1 AS n"}); lastErr == nil {
			break
		}
	}
	if lastErr != nil || !strings.HasPrefix(out, "n\n1\n") {
		t.Fatalf("after rotation: %q %v", out, lastErr)
	}
}
```

- [ ] **Step 3: Run the tests to verify they fail**

Run: `go test ./internal/core -run 'TestOpenConn|TestQueryThroughTunnel' -count=1`

Expected: FAIL. The SSH settings are ignored, so the dial to `db.internal` fails with a DNS error rather than "unknown host key".

- [ ] **Step 4: Implement `internal/core/open.go`**

Delete `OpenConn` from `service.go`, then create:

```go
package core

import (
	"context"

	"github.com/anderson-0/qh/internal/engine"
	"github.com/anderson-0/qh/internal/qerr"
	"github.com/anderson-0/qh/internal/tunnel"
)

// OpenConn opens spec with its registered engine. It is the production
// pool.Opener. It resolves the spec's secret references, builds its SSH
// tunnel, and returns a connection that closes the tunnel with it.
// Resolved values never reach errors.
func OpenConn(ctx context.Context, spec engine.ConnSpec) (engine.Conn, error) {
	e, err := engine.Get(spec.Engine)
	if err != nil {
		return nil, err
	}
	var secrets []string
	fail := func(err error) (engine.Conn, error) {
		if len(secrets) == 0 {
			return nil, err
		}
		return nil, scrub(err, secrets...)
	}
	resolve := func(ref string) (string, error) {
		if spec.Resolve == nil {
			return "", qerr.New(qerr.ConnectionFailed, "secret reference "+ref+": no resolver")
		}
		v, err := spec.Resolve(ctx, ref)
		if err == nil {
			secrets = append(secrets, v)
		}
		return v, err
	}
	if spec.DSNRef != "" {
		if spec.DSN, err = resolve(spec.DSNRef); err != nil {
			return fail(err)
		}
		if _, err := e.Inspect(spec.DSN); err != nil {
			return fail(qerr.New(qerr.ConnectionFailed, "secret reference "+spec.DSNRef+": not a valid "+spec.Engine+" connection string"))
		}
		if c, ok := e.(engine.ServerDSNChecker); ok && spec.Server {
			if err := c.CheckServerDSN(spec.DSN); err != nil {
				return fail(err)
			}
		}
	}
	if spec.PasswordRef != "" {
		if spec.Password, err = resolve(spec.PasswordRef); err != nil {
			return fail(err)
		}
	}
	var tun *tunnel.Tunnel
	if s := spec.SSH; s != nil {
		cfg := tunnel.Config{Addr: s.Addr(), User: s.User, HostKey: s.HostKey,
			PrivateKey: s.PrivateKey, Passphrase: s.Passphrase, Password: s.Password,
			DialBastion: spec.Dial, CheckTarget: spec.CheckTarget}
		for _, f := range []struct {
			ref string
			dst *string
		}{{s.PrivateKeyRef, &cfg.PrivateKey}, {s.PassphraseRef, &cfg.Passphrase}, {s.PasswordRef, &cfg.Password}} {
			if f.ref == "" {
				continue
			}
			if *f.dst, err = resolve(f.ref); err != nil {
				return fail(err)
			}
		}
		secrets = append(secrets, cfg.Password, cfg.Passphrase)
		if tun, err = tunnel.New(cfg); err != nil {
			return fail(err)
		}
		if err := tun.Connect(ctx); err != nil {
			tun.Close()
			return fail(err)
		}
		spec.Dial = tun.Dial
	}
	c, err := e.Open(ctx, spec)
	if err != nil {
		if tun != nil {
			tun.Close()
		}
		return fail(err)
	}
	if tun == nil {
		return c, nil
	}
	return &tunneledConn{Conn: c, tun: tun}, nil
}

// tunneledConn closes its tunnel after the engine connection.
type tunneledConn struct {
	engine.Conn
	tun *tunnel.Tunnel
}

func (c *tunneledConn) Close() error {
	err := c.Conn.Close()
	c.tun.Close()
	return err
}
```

`scrub` skips strings shorter than 4 characters (see `service.go`), so the empty passphrase is harmless.

If `engine.Conn` has more methods than `Close`, embedding forwards them. Also check whether core uses `engine.Conn` type assertions anywhere, e.g. `grep -rn 'conn.(' internal/core`. If it does, forward those interfaces too.

- [ ] **Step 5: Wire the spec, the connection test and pool drops**

In `service.go`, change `connSpec` to build from a `ConnInput`:

```go
// connSpec builds the spec engines open for workspace ws, carrying the
// server's options. Workspace names cannot contain path separators.
func (s *Service) connSpec(ws, id string, version int64, in ConnInput) engine.ConnSpec {
	spec := engine.ConnSpec{ID: id, Version: version, Engine: in.Engine, DSN: in.DSN, Mode: in.Mode, PoolSize: in.PoolSize,
		Dial: s.opts.Dial, TLS: in.TLS, Server: s.opts.Dial != nil, SSH: in.SSH, DSNRef: in.DSNRef, PasswordRef: in.PasswordRef,
		Resolve: s.refs.Resolve, CheckTarget: s.opts.CheckTarget}
	if s.opts.SQLiteRoot != "" {
		spec.FileRoot = filepath.Join(s.opts.SQLiteRoot, ws)
	}
	return spec
}
```

Update its callers:

- `resolve` uses `s.connSpec(ws, rec.ID, rec.Version, in)`. `load` already sets `in.PoolSize` from the record.
- `testConn` now takes a spec:

```go
func (s *Service) testConn(ctx context.Context, spec engine.ConnSpec, info engine.DSNInfo) (*TestResult, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	spec.ID, spec.Version, spec.PoolSize = "test", 0, 1
	start := time.Now()
	conn, err := OpenConn(ctx, spec)
	if err != nil {
		return nil, scrub(err, secretsOf(spec.DSN, info)...)
	}
	defer conn.Close()
	v, err := conn.Ping(ctx)
	if err != nil {
		return nil, scrub(err, secretsOf(spec.DSN, info)...)
	}
	return &TestResult{ServerVersion: v, Connect: time.Since(start)}, nil
}
```

  `CreateConnection`, `UpdateConnection` and `TestInput` call `s.testConn(ctx, s.connSpec(ws, "test", 0, in), info)`. `TestConnection` calls `s.testConn(ctx, t.spec, t.info)`.
- In `runQuery`, after a query error and before returning, drop the pool when the connection itself failed (spec adjustment 2):

```go
	if err != nil {
		if qerr.From(err).Category == qerr.ConnectionFailed {
			s.pools.Drop(t.rec.ID) // reopen next time: re-resolves secrets and reconnects the tunnel
		}
		return nil, t.scrub(s.withSuggestions(ctx, conn, t, in.Query, err))
	}
```

  Note that `release()` is deferred. Calling `pools.Drop` while this call still holds the handle is fine, because `retire` closes the pool once its in-flight count reaches zero. Check `retire` in `internal/pool/pool.go` to confirm.
- Apply the same drop where the schema catalog is fetched (`s.catalog(ctx, conn, t)` in `Schema` and `RefreshSchema`), with a small helper so all three sites stay one line:

```go
// dropIfBroken drops t's pool when err means its connection failed.
func (s *Service) dropIfBroken(t *target, err error) {
	if err != nil && qerr.From(err).Category == qerr.ConnectionFailed {
		s.pools.Drop(t.rec.ID)
	}
}
```

- [ ] **Step 6: Run the tests to verify they pass**

Run: `go vet ./... && go test ./internal/core -count=1 -race && go test ./... -count=1`

Expected: PASS, including the Docker tunnel and rotation test.

- [ ] **Step 7: Commit**

```bash
git add internal/core internal/engine/engine.go
git commit -m "feat(core): open connections through secret references and SSH tunnels

OpenConn resolves dsn_ref, password_ref and SSH references, connects the
tunnel first so errors name the failing stage, and closes the tunnel
with the pool. A query that fails on the connection drops the pool, so a
rotated secret or a restarted bastion is picked up on the next call.

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 8: CLI — connection flags, `conn show`, and `qh serve` flags

**Files:**
- Modify: `internal/cli/conn.go` (`connFlags`, `register`, a new `ssh()` method, `connAddCmd`, `connEditCmd`, `connShowCmd`)
- Modify: `internal/cli/serve.go` (`--allow-secret-ref`, `--deny-ssh`, `opts.CheckTarget`)
- Test: `internal/cli/conn_ssh_test.go`, `internal/cli/serve_test.go` (or the file holding the existing serve flag tests)

**Interfaces:**
- Consumes:
  - `core.ConnInput`/`ConnPatch`/`ConnInfo` fields and `core.Options.SecretRefs`/`DenySSH`/`CheckTarget` (Task 6);
  - `secretref.CheckPattern` (Task 2);
  - `netguard.Policy.Allowed` (`func (p *Policy) Allowed(ip netip.Addr) bool`).
- Produces: these flags:
  - on `qh conn add` and `qh conn edit`:
    - `--dsn-ref`, `--password-ref`;
    - `--ssh-host [user@]host[:port]`, `--ssh-user`, `--ssh-host-key`;
    - `--ssh-key FILE`, `--ssh-key-ref`;
    - `--ssh-passphrase-stdin`, `--ssh-passphrase-ref`;
    - `--ssh-password-stdin`, `--ssh-password-ref`;
  - on `qh conn edit` only: `--no-ssh`;
  - on `qh serve`: `--allow-secret-ref` (repeatable) and `--deny-ssh`.

- [ ] **Step 1: Write the failing tests**

Look at the existing CLI tests (`internal/cli/*_test.go`). They drive `Execute(...)` through a `run(t, home, stdin, args...)` helper. Write `internal/cli/conn_ssh_test.go` in that style:

```go
package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/anderson-0/qh/internal/tunnel/sshtest"
)

func TestConnAddWithRefsAndSSHSkipTest(t *testing.T) {
	home := t.TempDir()
	key, _ := sshtest.Key(t, "")
	keyFile := filepath.Join(t.TempDir(), "id")
	os.WriteFile(keyFile, []byte(key), 0o600)
	fp := "SHA256:47DEQpj8HBSa+/TImW+5JCeuQeRkm5NMpJWZG3hSuFU"
	out, errOut, code := run(t, home, "", "conn", "add", "pg", "--engine", "postgres", "--dsn-ref", "env:QH_PG_DSN",
		"--password-ref", "file:/run/secrets/pw", "--ssh-host", "jump@bastion.example.com:2222", "--ssh-host-key", fp,
		"--ssh-key", keyFile, "--skip-test")
	if code != 0 {
		t.Fatalf("%d %s", code, errOut)
	}
	out, errOut, code = run(t, home, "", "conn", "show", "pg")
	for _, want := range []string{
		"dsn: (from env:QH_PG_DSN)",
		"password: from file:/run/secrets/pw",
		"ssh: jump@bastion.example.com:2222 · host key " + fp + " · key",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("show lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "BEGIN OPENSSH") {
		t.Fatal("key printed")
	}
	_, _, code = run(t, home, "", "conn", "edit", "pg", "--no-ssh", "--skip-test")
	out, _, _ = run(t, home, "", "conn", "show", "pg")
	if code != 0 || !strings.Contains(out, "ssh: none") {
		t.Fatalf("no-ssh: %d\n%s", code, out)
	}
}

func TestConnAddSSHFlagErrors(t *testing.T) {
	home := t.TempDir()
	for want, args := range map[string][]string{
		"--ssh-user needs --ssh-host":       {"--ssh-user", "u"},
		"only one flag can read stdin":      {"--password-stdin", "--ssh-password-stdin", "--ssh-host", "b"},
		"invalid --ssh-host port":           {"--ssh-host", "b:notaport"},
	} {
		a := append([]string{"conn", "add", "x", "--engine", "postgres", "--dsn", "postgres://u@db/app", "--skip-test"}, args...)
		_, errOut, code := run(t, home, "", a...)
		if code == 0 || !strings.Contains(errOut, want) {
			t.Errorf("%q: %d %s", want, code, errOut)
		}
	}
}

func TestServeRefFlagsNeedServerMode(t *testing.T) {
	_, errOut, code := run(t, t.TempDir(), "", "serve", "--allow-secret-ref", "env:X_*")
	if code == 0 || !strings.Contains(errOut, "apply to server mode only") {
		t.Fatalf("%d %s", code, errOut)
	}
	t.Setenv("QH_MASTER_KEY", "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=")
	_, errOut, code = run(t, t.TempDir(), "", "serve", "--http", "127.0.0.1:0", "--allow-secret-ref", "QH_*")
	if code == 0 || !strings.Contains(errOut, "invalid --allow-secret-ref pattern") {
		t.Fatalf("%d %s", code, errOut)
	}
}
```

If `run`'s signature differs (`grep -n 'func run(' internal/cli/*_test.go`), adapt the calls but keep the assertions. Pick the master-key value in the same way existing serve tests do.

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/cli -run 'TestConnAddWithRefs|TestConnAddSSHFlagErrors|TestServeRefFlags' -count=1`

Expected: FAIL (`unknown flag: --dsn-ref`).

- [ ] **Step 3: Implement the connection flags**

Add to `connFlags`:

```go
	dsnRef, passwordRef                  string
	sshHost, sshUser, sshHostKey, sshKey string
	sshKeyRef, sshPassphraseRef          string
	sshPasswordRef                       string
	sshPassphraseStdin, sshPasswordStdin bool
	noSSH                                bool
```

Add to `register`. Put `--no-ssh` next to `--no-tls` in the edit-only branch; everything else is common:

```go
	fl.StringVar(&f.dsnRef, "dsn-ref", "", "secret reference for the whole connection string (env:NAME, file:/path or aws-sm://id[#key])")
	fl.StringVar(&f.passwordRef, "password-ref", "", "secret reference for the database password")
	fl.StringVar(&f.sshHost, "ssh-host", "", "reach the database through this SSH bastion: [user@]host[:port]")
	fl.StringVar(&f.sshUser, "ssh-user", "", "SSH user (or user@ in --ssh-host)")
	fl.StringVar(&f.sshHostKey, "ssh-host-key", "", "the bastion's host-key fingerprint, SHA256:… (omit it once to be shown it)")
	fl.StringVar(&f.sshKey, "ssh-key", "", "file with the SSH private key (read here and sent sealed)")
	fl.StringVar(&f.sshKeyRef, "ssh-key-ref", "", "secret reference for the SSH private key")
	fl.BoolVar(&f.sshPassphraseStdin, "ssh-passphrase-stdin", false, "read the SSH key passphrase from the first line of stdin")
	fl.StringVar(&f.sshPassphraseRef, "ssh-passphrase-ref", "", "secret reference for the SSH key passphrase")
	fl.BoolVar(&f.sshPasswordStdin, "ssh-password-stdin", false, "read the SSH password from the first line of stdin")
	fl.StringVar(&f.sshPasswordRef, "ssh-password-ref", "", "secret reference for the SSH password")
```

and, in the `else` branch:

```go
		fl.BoolVar(&f.noSSH, "no-ssh", false, "remove the SSH tunnel")
```

Add the methods:

```go
// stdinReaders counts the flags that read stdin; at most one may.
func (f *connFlags) stdinReaders() int {
	n := 0
	for _, b := range []bool{f.dsn == "-", f.passwordStdin, f.sshPassphraseStdin, f.sshPasswordStdin} {
		if b {
			n++
		}
	}
	return n
}

// ssh returns the tunnel the flags describe, or nil when --ssh-host is absent.
func (f *connFlags) ssh(cmd *cobra.Command) (*engine.SSH, error) {
	fl := cmd.Flags()
	if !fl.Changed("ssh-host") {
		for _, n := range []string{"ssh-user", "ssh-host-key", "ssh-key", "ssh-key-ref", "ssh-passphrase-stdin",
			"ssh-passphrase-ref", "ssh-password-stdin", "ssh-password-ref"} {
			if fl.Changed(n) {
				return nil, qerr.New(qerr.Invalid, "--"+n+" needs --ssh-host")
			}
		}
		return nil, nil
	}
	s := &engine.SSH{User: f.sshUser, HostKey: f.sshHostKey, PrivateKeyRef: f.sshKeyRef,
		PassphraseRef: f.sshPassphraseRef, PasswordRef: f.sshPasswordRef}
	hp := f.sshHost
	if u, rest, ok := strings.Cut(hp, "@"); ok {
		if s.User == "" {
			s.User = u
		}
		hp = rest
	}
	if h, p, err := net.SplitHostPort(hp); err == nil {
		port, err := strconv.Atoi(p)
		if err != nil {
			return nil, qerr.New(qerr.Invalid, "invalid --ssh-host port "+strconv.Quote(p))
		}
		s.Host, s.Port = h, port
	} else {
		s.Host = hp
	}
	if f.sshKey != "" {
		b, err := os.ReadFile(f.sshKey)
		if err != nil {
			return nil, qerr.New(qerr.Invalid, "cannot read "+f.sshKey)
		}
		s.PrivateKey = string(b)
	}
	var err error
	if f.sshPassphraseStdin {
		if s.Passphrase, err = readLine(cmd); err != nil {
			return nil, err
		}
	}
	if f.sshPasswordStdin {
		if s.Password, err = readLine(cmd); err != nil {
			return nil, err
		}
	}
	return s, nil
}
```

`net.SplitHostPort("b:notaport")` succeeds with port `notaport`, so the `Atoi` check produces the "invalid --ssh-host port" error the test expects.

In `connAddCmd`:

- Replace the `--dsn - and --password-stdin` check with `if f.stdinReaders() > 1 { return qerr.New(qerr.Invalid, "only one flag can read stdin") }`.
- Extend the "--dsn is required" condition with `&& f.dsnRef == ""`.
- Set `in.DSNRef, in.PasswordRef = f.dsnRef, f.passwordRef` and `in.SSH, err = f.ssh(cmd)`.

In `connEditCmd`:

- Add the same stdin check.
- `if fl.Changed("dsn-ref") { p.DSNRef = &f.dsnRef }` and `if fl.Changed("password-ref") { p.PasswordRef = &f.passwordRef }`.
- `p.ClearSSH = f.noSSH`, then `p.SSH, err = f.ssh(cmd)`.

In `connShowCmd`, print after the `dsn`/`tls` line:

```go
			pw := "(in the connection string)"
			if c.PasswordRef != "" {
				pw = "from " + c.PasswordRef
			}
			fmt.Fprintf(out, "password: %s\nssh: %s\n", pw, sshSummary(c.SSH))
```

with:

```go
func sshSummary(s *engine.SSHInfo) string {
	if s == nil {
		return "none"
	}
	out := fmt.Sprintf("%s@%s · host key %s · %s", s.User, net.JoinHostPort(s.Host, strconv.Itoa(s.Port)), s.HostKey, s.Auth)
	if len(s.Refs) > 0 {
		var refs []string
		for _, k := range []string{"private_key", "passphrase", "password"} {
			if r := s.Refs[k]; r != "" {
				refs = append(refs, k+" from "+r)
			}
		}
		out += " (" + strings.Join(refs, ", ") + ")"
	}
	return out
}
```

Add the `"net"` import.

- [ ] **Step 4: Implement the `qh serve` flags**

In `internal/cli/serve.go`, declare `var allowRefs []string; var denySSH bool` with the other flag variables. Register them:

```go
	cmd.Flags().StringArrayVar(&allowRefs, "allow-secret-ref", nil, "server mode: allow secret references matching this pattern (repeatable; * is a wildcard), e.g. env:QH_TENANT_*")
	cmd.Flags().BoolVar(&denySSH, "deny-ssh", false, "server mode: refuse connections that use an SSH tunnel")
```

In the server branch, after `opts.Dial = policy.DialContext`:

```go
				opts.CheckTarget = policy.Allowed
				for _, p := range allowRefs {
					if err := secretref.CheckPattern(p); err != nil {
						return err
					}
				}
				opts.SecretRefs, opts.DenySSH = allowRefs, denySSH
```

Extend the local-mode guard so these flags also need `--http`:

```go
			} else if len(allow)+len(deny)+len(allowRefs) > 0 || sqliteRoot != "" || denySSH {
				return qerr.New(qerr.Invalid, "--allow-cidr, --deny-cidr, --sqlite-root, --allow-secret-ref and --deny-ssh apply to server mode only").WithHint("Add --http ADDR")
```

Existing tests assert the old message. Find them with `grep -rn 'apply to server mode only' internal cmd` and update them to the new text.

- [ ] **Step 5: Run the tests to verify they pass**

Run: `go vet ./... && go test ./internal/cli ./cmd/qh -count=1`

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/cli
git commit -m "feat(cli): flags for SSH tunnels and secret references

qh conn add/edit take --dsn-ref, --password-ref and --ssh-* flags (keys
are read here and sent sealed, like TLS keys); conn show prints the tunnel
and references; qh serve takes --allow-secret-ref and --deny-ssh.

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 9: REST API, OpenAPI and the remote client

**Files:**
- Modify: `internal/httpapi/connections.go` (`connectionJSON`, `toConnection`, `connectionInput`, `connectionPatch`)
- Modify: `internal/httpapi/openapi.json`
- Modify: `internal/client/client.go` (`connectionJSON`, `info`, `CreateConnection`, `UpdateConnection`)
- Test: `internal/httpapi/connections_ssh_test.go`, `internal/client/client_test.go`

**Interfaces:**
- Consumes: the core fields from Task 6; `engine.SSH` and `engine.SSHInfo` JSON tags (Task 4).
- Produces:
  - `POST /api/v1/connections` accepts `dsn_ref`, `password_ref` and `ssh`.
  - `PATCH` accepts `dsn_ref`, `password_ref` and `ssh` (absent: unchanged; `null`: removed; object: replaced; unknown fields refused).
  - Responses carry `ssh` (`SSHInfo`), `dsn_ref` and `password_ref`.

- [ ] **Step 1: Write the failing tests**

Follow the existing httpapi tests (`grep -n 'func newEnv\|func (e \*env) do' internal/httpapi/*_test.go`). Create `internal/httpapi/connections_ssh_test.go`:

```go
package httpapi

import (
	"strings"
	"testing"

	"github.com/anderson-0/qh/internal/auth"
)

func TestConnectionSSHAndRefsOverREST(t *testing.T) {
	e := newEnv(t, false, nil) // local mode: every reference allowed
	k := e.key(t, "default", auth.ScopeManage)
	fp := "SHA256:47DEQpj8HBSa+/TImW+5JCeuQeRkm5NMpJWZG3hSuFU"
	r := e.do(t, "POST", "/api/v1/connections?skip_test=true", k, map[string]any{
		"name": "pg", "engine": "postgres", "dsn_ref": "env:QH_PG_DSN", "password_ref": "env:QH_PG_PW",
		"ssh": map[string]any{"host": "bastion", "user": "jump", "host_key": fp, "password": "ssh-secret"},
	})
	if r.status != 201 {
		t.Fatalf("create: %d %s", r.status, r.body)
	}
	for _, want := range []string{`"dsn_ref":"env:QH_PG_DSN"`, `"password_ref":"env:QH_PG_PW"`, `"host_key":"` + fp + `"`, `"auth":"password"`} {
		if !strings.Contains(r.body, want) {
			t.Errorf("body lacks %s: %s", want, r.body)
		}
	}
	if strings.Contains(r.body, "ssh-secret") {
		t.Fatal("ssh password returned")
	}
	if r := e.do(t, "PATCH", "/api/v1/connections/pg?skip_test=true", k, map[string]any{"ssh": nil}); r.status != 200 || strings.Contains(r.body, `"ssh"`) {
		t.Fatalf("clear ssh: %d %s", r.status, r.body)
	}
	if r := e.do(t, "PATCH", "/api/v1/connections/pg?skip_test=true", k, map[string]any{"ssh": map[string]any{"host": "b", "bogus": 1}}); r.status != 400 {
		t.Fatalf("unknown ssh field: %d %s", r.status, r.body)
	}
}

func TestServerRefusesUnlistedRef(t *testing.T) {
	e := newEnv(t, true, nil) // server mode, no --allow-secret-ref
	k := e.key(t, "default", auth.ScopeManage)
	expectProblem(t, e.do(t, "POST", "/api/v1/connections?skip_test=true", k, map[string]any{
		"name": "pg", "engine": "postgres", "dsn_ref": "env:HOME"}), 403, "forbidden", "env:HOME is not allowed on this server")
}
```

If `newEnv`'s signature differs, adapt the calls to its real one. Check its server-mode flag, and how `core.Options` reaches the service in tests.

In `internal/client/client_test.go`, add a round trip: create a connection with `DSNRef`, `PasswordRef` and an `SSH` block through `client.Client` against the test server, then `GetConnection`. Assert that `SSH.HostKey`, `DSNRef` and `PasswordRef` come back. Follow the existing create/get test in that file.

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/httpapi ./internal/client -count=1`

Expected: FAIL. The unknown fields are rejected (400) on create.

- [ ] **Step 3: Implement**

In `internal/httpapi/connections.go`:

- `connectionJSON` gains `SSH *engine.SSHInfo` `json:"ssh,omitempty"`, `DSNRef string` `json:"dsn_ref,omitempty"` and `PasswordRef string` `json:"password_ref,omitempty"`. `toConnection` copies `c.SSH`, `c.DSNRef` and `c.PasswordRef`.
- `connectionInput` gains `DSNRef string` `json:"dsn_ref,omitempty"`, `PasswordRef string` `json:"password_ref,omitempty"` and `SSH *engine.SSH` `json:"ssh,omitempty"`. `core()` copies them.
- `connectionPatch` gains `DSNRef *string` `json:"dsn_ref"`, `PasswordRef *string` `json:"password_ref"` and `SSH json.RawMessage` `json:"ssh"`. `core()` copies `DSNRef` and `PasswordRef`, and decodes `ssh` exactly like `tls`:

```go
	switch raw := bytes.TrimSpace(p.SSH); {
	case len(raw) == 0:
	case string(raw) == "null":
		out.ClearSSH = true
	default:
		var s engine.SSH
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&s); err != nil {
			return out, qerr.New(qerr.Invalid, "invalid JSON body: ssh: "+err.Error())
		}
		out.SSH = &s
	}
```

In `internal/httpapi/openapi.json`:

- In `ConnectionInput.properties`, add `"dsn_ref": { "type": "string", "description": "Secret reference (env:NAME, file:/path, aws-sm://id[#key]) for the whole connection string; instead of dsn" }`, `"password_ref": { "type": "string", "description": "Secret reference for the database password" }` and `"ssh": { "$ref": "#/components/schemas/SSH" }`. Update its `description` to mention `dsn_ref`.
- In `ConnectionPatch.properties`, add `"dsn_ref": { "type": "string" }`, `"password_ref": { "type": "string", "description": "Empty removes it" }` and `"ssh": { "oneOf": [{ "$ref": "#/components/schemas/SSH" }, { "type": "null" }], "description": "An object replaces the tunnel; null removes it" }`.
- In `Connection.properties`, add `"ssh": { "$ref": "#/components/schemas/SSHInfo" }`, `"dsn_ref": { "type": "string" }` and `"password_ref": { "type": "string" }`.
- Add these schemas after `TLSInfo`:

```json
      "SSH": { "type": "object", "required": ["host", "user"], "additionalProperties": false, "properties": {
        "host": { "type": "string" }, "port": { "type": "integer", "description": "Default 22" }, "user": { "type": "string" },
        "host_key": { "type": "string", "description": "Bastion host-key fingerprint, SHA256:…; required to save" },
        "private_key": { "type": "string", "description": "PEM private key; stored sealed and never returned" },
        "private_key_ref": { "type": "string" }, "passphrase": { "type": "string", "description": "Stored sealed and never returned" },
        "passphrase_ref": { "type": "string" }, "password": { "type": "string", "description": "Stored sealed and never returned" },
        "password_ref": { "type": "string" } } },
      "SSHInfo": { "type": "object", "properties": {
        "host": { "type": "string" }, "port": { "type": "integer" }, "user": { "type": "string" }, "host_key": { "type": "string" },
        "auth": { "type": "string", "enum": ["key", "password"] },
        "refs": { "type": "object", "additionalProperties": { "type": "string" }, "description": "Field name to secret reference" } } },
```

In `internal/client/client.go`:

- `connectionJSON` gains `SSH *engine.SSHInfo` `json:"ssh"`, `DSNRef string` `json:"dsn_ref"` and `PasswordRef string` `json:"password_ref"`. `info()` copies them into `core.ConnInfo`.
- `CreateConnection` adds `set("dsn_ref", in.DSNRef, in.DSNRef != "")`, `set("password_ref", in.PasswordRef, in.PasswordRef != "")` and `set("ssh", in.SSH, in.SSH != nil)`.
- `UpdateConnection` adds:

```go
	if p.DSNRef != nil {
		body["dsn_ref"] = *p.DSNRef
	}
	if p.PasswordRef != nil {
		body["password_ref"] = *p.PasswordRef
	}
	switch {
	case p.ClearSSH && p.SSH != nil:
		return nil, nil, qerr.New(qerr.Invalid, "give ssh or clear it, not both")
	case p.ClearSSH:
		body["ssh"] = nil
	case p.SSH != nil:
		body["ssh"] = p.SSH
	}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go vet ./... && go test ./internal/httpapi ./internal/client ./internal/cli ./cmd/qh -count=1`

Expected: PASS, including `TestOpenAPIMatchesRoutes`.

- [ ] **Step 5: Commit**

```bash
git add internal/httpapi internal/client
git commit -m "feat(api): SSH tunnels and secret references in the REST API and client

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 10: MySQL through a tunnel, end-to-end CLI check, and docs

**Files:**
- Test: `internal/core/tunnel_mysql_test.go` (Docker)
- Test: `cmd/qh/ssh_e2e_test.go` (the real binary, through the in-process bastion)
- Modify: `README.md` (new section "SSH tunnels and secret references")

**Interfaces:**
- Consumes: everything above.
- Produces: no new code, only tests and docs.

- [ ] **Step 1: Write the MySQL tunnel test**

`internal/core/tunnel_mysql_test.go` follows `tunnel_pg_test.go` (Task 7). It uses `tcmysql.Run(ctx, "mysql:8.4", tcmysql.WithDatabase("app"), tcmysql.WithUsername("app"), tcmysql.WithPassword("mysql-pw"))` and the container's `Host`/`MappedPort("3306/tcp")`. The bastion's `Forward` maps `db.internal:3306` to the real address. It creates a connection with:

- `Engine: "mysql"`;
- `DSN: "mysql://app@db.internal:3306/app"`;
- `PasswordRef: "env:QH_TEST_MY_PW"`;
- an `SSH` password-auth block;
- `test=true`.

Then it runs `SELECT 1 AS n` and asserts the output starts with `n\n1\n` and that `srv.Targets()[0] == "db.internal:3306"`.

Skip with `testing.Short()` and `testcontainers.SkipIfProviderIsNotHealthy(t)`. Use the module import `tcmysql "github.com/testcontainers/testcontainers-go/modules/mysql"`, which is already in `go.mod`.

- [ ] **Step 2: Write the end-to-end CLI test**

`cmd/qh/ssh_e2e_test.go` uses the existing `buildBinary(t)` helper (`cmd/qh/e2e_test.go`). It runs the real binary with `--home` set to a temp dir:

1. Start `sshtest.Start` with password auth and a `Forward` that always fails. This test checks the CLI flow, not a database.
2. Run `qh conn add pg --engine postgres --dsn postgres://u@db.internal:5432/app --ssh-host u@127.0.0.1:<port> --ssh-password-stdin`, with stdin `pw\n`. Assert:
   - a non-zero exit;
   - stderr contains `unknown host key` and `--ssh-host-key <srv.Fingerprint>`;
   - `qh conn ls` afterwards does not list `pg`.
3. Re-run with `--ssh-host-key <srv.Fingerprint>`. Assert a non-zero exit, now with stderr containing `ssh: the bastion could not reach db.internal:5432`. That proves the host key was accepted and the tunnel reached the forwarding stage.

- [ ] **Step 3: Run the tests**

Run: `go test ./internal/core -run TestQueryThroughTunnel -count=1 && go test ./internal/core -run Mysql -count=1 && go test ./cmd/qh -count=1`

Expected: PASS. These pass on the first run because the features exist. If one fails, it is a real bug in Tasks 6–9: report it as DONE_WITH_CONCERNS with the output, and do not weaken the test.

- [ ] **Step 4: Document**

In `README.md`, add a section after the connection/TLS documentation (find it with `grep -n 'tls' README.md`):

````markdown
## SSH tunnels and secret references

Reach a database through a bastion host:

```bash
qh conn add prod --engine postgres --dsn 'postgres://app@db.internal:5432/app' --password-ref env:PROD_DB_PASSWORD \
  --ssh-host jump@bastion.example.com --ssh-key ~/.ssh/id_ed25519
```

The first run fails and shows the bastion's host-key fingerprint. Verify it (`ssh-keyscan bastion.example.com | ssh-keygen -lf -`) and add `--ssh-host-key SHA256:…`. qh refuses any other key. The key file is read by the CLI and stored sealed, like `--tls-key`. Use `--ssh-password-stdin` for password authentication.

Instead of storing a password or connection string, a connection can reference one:

| Reference | Value |
|---|---|
| `env:NAME` | an environment variable of the qh process |
| `file:/path` | a file's contents (at most 64 KiB, trailing newline removed), e.g. a Docker or Kubernetes secret |
| `aws-sm://<name or ARN>[#key]` | an AWS Secrets Manager secret, or one field of a JSON secret, read with the qh process's AWS credentials |

Use them with `--dsn-ref`, `--password-ref`, `--ssh-key-ref`, `--ssh-passphrase-ref` or `--ssh-password-ref`. References are resolved when qh connects and never stored. After a rotation, the next call that fails to authenticate reconnects with the new value.

**Server mode.** A tenant must not be able to read the server's environment or cloud secrets, or tunnel into its network:

- References are refused unless the operator allows them: `qh serve --http :8765 --allow-secret-ref 'env:QH_TENANT_*' --allow-secret-ref 'aws-sm://arn:aws:secretsmanager:us-east-1:123456789012:secret:qh/*'`.
- Tunnels must pass `--allow-cidr`/`--deny-cidr` for both the bastion and the database behind it. A database on the bastion's own loopback is allowed.
- `--deny-ssh` turns tunnels off.
````

- [ ] **Step 5: Full suite and commit**

Run: `go vet ./... && go test ./... -count=1 && (cd bench && go test ./... -short -count=1)`

Expected: PASS.

```bash
git add internal/core/tunnel_mysql_test.go cmd/qh/ssh_e2e_test.go README.md
git commit -m "test: MySQL through a tunnel and the CLI fingerprint flow; document tunnels and references

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```
