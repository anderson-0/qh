# Password-only Connection Editing Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let local and remote users replace or clear a stored inline database password with `qh conn edit NAME --password-stdin`, preserving all other connection settings.

**Architecture:** A transient `core.ConnPatch.Password *string` crosses the CLI/client/REST boundary. Core loads the original encrypted DSN and invokes an optional engine capability to rewrite its inline credential, then follows existing validation, test, encryption and version-based invalidation paths. Engines own syntax-only rewrites; no persistent override or migration is introduced.

**Tech Stack:** Go 1.27, Cobra, existing pgx v5.11.0, go-sql-driver/mysql v1.10.1, MongoDB driver v2.9.2, existing store/keyring and testcontainers. No new dependency.

**Spec:** [Password-edit design](../specs/2026-10-15-qh-password-edit-design.md), explicitly approved by the user at `101465c`. The user also approved this plan and chose subagent execution. Tasks 1–5 passed their independent reviews. Task 6 is implemented and full root tests, vet, focused race checks and the unchanged MCP surface check pass. Its independent review and whole-branch review precede the feature PR.

## Global Constraints

- "Do not change the MCP tools, their descriptions or instructions, schema output, query behavior, the store schema or the sealed-secret format."
- "The base `engine.Engine` interface stays unchanged." Add only `PasswordDSNSetter.SetPassword(dsn, password string) (string, error)`; SQLite remains unsupported.
- "Core and REST do not trim the supplied password." Nil/absent/null means omission; non-nil empty means clear. Clearing does not change existing driver fallback policy.
- "If a patch includes both a new `DSN` and `Password`, edit the new DSN." Retain all existing DSN/reference/TLS/SSH conflict and clearing rules.
- "When `Password` is present, reject the edit if the resulting `DSNRef` or `PasswordRef` is nonempty after existing whitespace normalization." Reject before rewriting or resolving those database references.
- "`SetPassword` is a pure string transformation." No network, DNS, environment, credential/certificate/service/zone-file access or reference resolution; no resolved configuration serialization.
- "Preservation is semantic, rather than byte identity"; MySQL native non-credential suffixes specifically remain verbatim. Remove every superseded inline database-password slot while retaining unrelated credential settings.
- "Reuse `readLine`: read the first line; accept EOF without a newline; apply `strings.TrimSpace`; accept an empty result, including immediate EOF; ignore later lines." No arbitrary 64 KiB cap. Only active true flags count as stdin readers.
- "Do not add a password field to `ConnInfo`, public connection JSON, GET/list output or successful edit output." Never print/log secrets or credential-bearing DSNs; use synthetic fixtures and test-owned files only.
- "No paid agent/token benchmark is needed while MCP surface and schema output remain unchanged." No paid run, merge, tag or release without separate approval.
- One branch and one PR cover this feature, including its spec and plan. Commit as `Anderson <anderson.consultant.it@outlook.com>`; stage only each task's named files. Existing private notes stay private.

## Review Focus

- PostgreSQL query-password overrides, duplicate keywords and escaped endings must not retain an old effective password or swallow a replacement (Task 1).
- MySQL `loc=Europe%2FParis` and empty usernames must not cause hidden file access, option rewriting or silent loss of a nonempty patch (Task 2).
- MongoDB percent-encoded usernames and repeated options must keep their interpretation across multi-host/SRV rewrites without DNS (Task 3).
- Same-patch reference removal, a concurrent edit and a failed authentication test must preserve the correct stored version and pool credentials (Task 4).
- REST null versus empty and CLI false/empty/long/error input must reach core with exact presence semantics and no accidental stdin consumption (Tasks 5–6).

---

## File boundaries and dependencies

| Owner | Files and responsibility |
|---|---|
| Task 1 | `internal/engine/engine.go`: optional capability; new `internal/engine/postgres/password.go` and `password_test.go`: PostgreSQL transformation and fixtures. |
| Task 2 | New `internal/engine/mysql/password.go` and `password_test.go`: one method on existing `Engine` for both MySQL and MariaDB. |
| Task 3 | New `internal/engine/mongodb/password.go` and `password_test.go`: URI transformation using existing syntax parser. |
| Task 4 | `internal/core/connections.go`: transient field and update integration; new `internal/core/password_test.go` and `password_integration_test.go`: storage/lifecycle and real authentication; extend `invalidate_test.go` for shared invalidation. |
| Task 5 | `internal/httpapi/connections.go`, `openapi.json`, new `connections_password_test.go`, `openapi_test.go`; `internal/client/client.go` and new `password_test.go`: request transport, contract and redaction. |
| Task 6 | `internal/cli/conn.go`, new `conn_password_test.go`, `remote_test.go`, `README.md`, `CHANGELOG.md`, `docs/HANDOFF.md`: flag, stdin, end-to-end use and verified delivery notes. |

`internal/cli/root.go`, `internal/core/service.go`, engine parser files and existing test helpers are read-only reference unless a focused test demonstrates a necessary change within the approved design. No restructuring of existing large files.

Tasks 1–3 own different engine directories. Task 1 alone owns `engine.go`; once its optional interface declaration is fixed, up to three engine workers can proceed in parallel if the user chooses subagent execution. Do not create a setup-only task. Task 4 waits for all three engine deliverables; Task 5 waits for Task 4; Task 6 waits for Tasks 4–5. Each task has its own test cycle and review; the final whole-branch review follows Task 6. The user chose subagent execution.

## Task 1: Optional capability and PostgreSQL rewriting

**Files:** Modify `internal/engine/engine.go`; create `internal/engine/postgres/password.go`, `internal/engine/postgres/password_test.go`.

**Interfaces:**
- Consumes: existing `postgres.Engine`, `qerr.New(qerr.Invalid, message)` and pinned pgx syntax as read-only references.
- Produces: `engine.PasswordDSNSetter` with `SetPassword(dsn, password string) (string, error)`; `(postgres.Engine).SetPassword(dsn, password string) (string, error)`.
- Does not change: `Engine.Inspect(dsn string) (engine.DSNInfo, error)` or the base `engine.Engine` requirements.

- [ ] **Step 1: Write `TestSetPasswordPostgres`, `TestSetPasswordPostgresPure` and `TestSetPasswordPostgresInvalid`.** Start with a local method-interface assertion so the test compiles before the capability exists:

```go
setter, ok := any(Engine{}).(interface { SetPassword(string, string) (string, error) })
if !ok { t.Fatal("postgres lacks password editing") }
got, err := setter.SetPassword("postgres://reader@db/app?application_name=agent", "replacement")
if err != nil { t.Fatal("rewrite failed") }
info, err := (Engine{}).Inspect(got)
if err != nil || info.User != "reader" || info.Database != "app" || info.Password != "replacement" { t.Fatal("credential semantics changed") }
```

Use table fixtures for both schemes and keyword syntax, absent/empty/present credentials, duplicate keywords and URL query-password overrides, escaped quotes/backslashes, trailing escaped endings, whitespace, reserved characters/Unicode, socket hosts, IPv6, host/port lists, duplicate non-password precedence and unrelated TLS-key passphrases. For replacement and empty clearing, compare effective non-password settings using pgconn only in controlled test fixtures; assert all old database-password slots are gone. Assert fixed `invalid` errors contain neither input nor fixture passwords. Pure-setter cases preserve explicit nonexistent service/certificate paths without opening them and do not materialize environment/default keys; keep effective-config comparisons separate from these cases.

- [ ] **Step 2: Run RED.** `go test ./internal/engine/postgres -run '^TestSetPasswordPostgres' -count=1`; expect the explicit missing-capability assertion to fail, not an unrelated parser/compiler failure.
- [ ] **Step 3: Add the optional interface and implement the PostgreSQL method.** Parse explicit keyword spans with pgx-compatible escaping/last-value rules and explicit URL components/query occurrences with its host-list grammar. Rewrite/remove password slots safely; preserve non-password meaning and omitted settings. Do not call resolved `pgconn.ParseConfig` from the setter or serialize `DSNInfo`. Reject unrepresentable credentials with a fixed `invalid` message. Keep helpers private in `password.go`; no edits to redaction/server policy unless a new credential case proves one necessary.
- [ ] **Step 4: Run GREEN.** Repeat the RED command, then `go test -short ./internal/engine/postgres`; all tests pass. Inspect setter call paths to confirm no config/environment/file resolver is reachable; compare syntax fixtures to the pinned parser grammar rather than inferring purity from success alone.
- [ ] **Step 5: Review and commit the independently testable capability/PostgreSQL change.** Run `gofmt -w` on these Go files and `go vet ./internal/engine/postgres`; commit only the three task files with `feat: preserve postgres settings when editing passwords` after the selected review gate.

## Task 2: MySQL and MariaDB rewriting

**Files:** Create `internal/engine/mysql/password.go`, `internal/engine/mysql/password_test.go`. Read `mysql.go` and pinned driver `dsn.go`.

**Interfaces:**
- Consumes: Task 1's `engine.PasswordDSNSetter`; existing `type Engine struct{ name string }`, `isURL(*url.URL) bool` and `invalidDSN() error`.
- Produces: `(mysql.Engine).SetPassword(dsn, password string) (string, error)` for `Engine{}` and `Engine{name: "mariadb"}`. There is no second MariaDB engine type or setter.

- [ ] **Step 1: Write `TestSetPasswordMySQL`, `TestSetPasswordMySQLPure` and `TestSetPasswordMySQLInvalid`.** Assert the method interface dynamically as in Task 1, then test both engine names and both URL schemes, native TCP/IPv6/Unix sockets, reserved credentials/Unicode, no/empty usernames, empty clearing, database escaping, charset lists, time zone values, TLS, boolean flags, arbitrary session parameters and duplicate-option precedence. In the table-driven native cases, `suffix` is the original transport/database/options substring and `gotSuffix` is extracted by the same structural boundary used for the fixture assertion:

```go
if gotSuffix != suffix { t.Fatal("non-credential suffix changed") }
if err == nil && parsed.Passwd != next { t.Fatal("requested password was silently dropped") }
if err == nil && parsed.User != before.User { t.Fatal("username changed") }
```

Use `loc=Europe%2FParis` to pin verbatim suffix preservation and an unavailable synthetic location to prove rewriting does not load it. Driver parsing belongs only in valid semantic test comparisons after transformation. Missing/empty-user cases must preserve the requested password where representable, otherwise produce fixed `invalid`; assert neither silent success nor invented username. Invalid syntax errors must not echo fixture secrets.

- [ ] **Step 2: Run RED.** `go test ./internal/engine/mysql -run '^TestSetPasswordMySQL' -count=1`; expect the missing-capability assertion.
- [ ] **Step 3: Implement the method in `password.go`.** Rewrite the native credential span according to the pinned driver's delimiter rules, preserving the remaining bytes. For URL forms rewrite userinfo without driver option parsing. Do not call `ParseDSN`, `FormatDSN`, `parseRaw` or `parseConfig` from the transformation: `loc` can read zone data, and formatting can discard passwords for empty usernames. Normal core validation afterward is unchanged.
- [ ] **Step 4: Run GREEN.** Repeat the RED command and `go test -short ./internal/engine/mysql`; require all passing, including the unchanged suffix and empty-user cases. Review the setter call graph for environment/zone-file access.
- [ ] **Step 5: Review and commit.** `gofmt -w internal/engine/mysql/password.go internal/engine/mysql/password_test.go`; `go vet ./internal/engine/mysql`; commit the two files with `feat: edit mysql and mariadb passwords without rewriting options` after review.

## Task 3: MongoDB rewriting

**Files:** Create `internal/engine/mongodb/password.go`, `internal/engine/mongodb/password_test.go`. Read existing `mongodb.go` URI parser.

**Interfaces:**
- Consumes: Task 1's `engine.PasswordDSNSetter`; existing `parseURI(dsn string) (*uri, error)`, parsed password offsets/options and `badURI() error`.
- Produces: `(mongodb.Engine).SetPassword(dsn, password string) (string, error)`.

- [ ] **Step 1: Write `TestSetPasswordMongoDB`, `TestSetPasswordMongoDBPure` and `TestSetPasswordMongoDBInvalid`.** Dynamically assert the capability before invoking it. Table-test ordinary/SRV URIs, multi-host/IPv6/ports, absent/empty credentials, reserved characters/Unicode, already encoded usernames, empty clearing, `authSource`, authentication mechanism, replica set, TLS and repeated `&`/`;` options. Check syntax with `parseURI`; avoid driver SRV parsing in offline fixtures:

```go
if after.user != before.user || after.db != before.db || !reflect.DeepEqual(after.hosts, before.hosts) { t.Fatal("target or username changed") }
if after.password != next || !reflect.DeepEqual(after.options, before.options) { t.Fatal("password or option preservation failed") }
```

Use a nonresolving synthetic SRV hostname and nonexistent certificate path that the syntax parser accepts; transformation succeeds without resolution or file access. Assert malformed URI errors are fixed `invalid`, do not contain input/credentials, and missing-user edits either retain username semantics or reject unrepresentable syntax.

- [ ] **Step 2: Run RED.** `go test ./internal/engine/mongodb -run '^TestSetPasswordMongoDB' -count=1`; expect the missing-capability assertion.
- [ ] **Step 3: Implement the method in `password.go`.** Follow the existing URI parser's userinfo boundaries/offsets; insert or replace encoded password bytes while preserving hosts, database and options. Do not resolve SRV/TXT or round-trip the full multi-host URI through a generic URL builder. Reparse only with the syntax-only helper where needed.
- [ ] **Step 4: Run GREEN.** Repeat the RED command and `go test -short ./internal/engine/mongodb`; require all passing. Check call paths for DNS/environment/file access.
- [ ] **Step 5: Review and commit.** Format both files, run `go vet ./internal/engine/mongodb`, and commit with `feat: preserve mongodb topology when editing passwords` after review.

## Task 4: Core patch, sealed save and lifecycle

**Files:** Modify `internal/core/connections.go`, `internal/core/invalidate_test.go`; create `internal/core/password_test.go`, `internal/core/password_integration_test.go`.

**Interfaces:**
- Consumes: all engine setters; existing `newHarness(t *testing.T) *harness`, `h.st.GetConnection`, `h.svc.load(rec)`, `Service.fill` and the store's version check.
- Produces: `core.ConnPatch.Password *string`; unchanged `(*Service).UpdateConnection(ctx context.Context, ws, name string, p ConnPatch, test bool) (*ConnInfo, *TestResult, error)` with password semantics.
- Existing `ConnSpec.Password`, `ConnInfo`, storage format, pool API and `OpenConn` signatures remain unchanged.

- [ ] **Step 1: Write core regression tests.** Declare the new pointer field as an inert API seam so tests compile, without wiring update behavior. Add `TestPasswordPatchStored`, `TestPasswordPatchRefs`, `TestPasswordPatchRollback`, `TestPasswordPatchLifecycle`, `TestPasswordPatchPolicy`, and `TestPasswordPatchConcurrentConflict`. Start with a skip-test PostgreSQL fixture and inspect the test-owned sealed payload through existing helpers:

```go
info, tr, err := h.svc.UpdateConnection(ctx, ws, "pg", ConnPatch{Password: &next}, false)
if err != nil || tr != nil || info.Version != before.Version+1 { t.Fatal("update contract failed") }
rec, err := h.st.GetConnection(ctx, ws, "pg")
if err != nil { t.Fatal("stored record missing") }
in, err := h.svc.load(rec)
if err != nil { t.Fatal("cannot decrypt test record") }
di, err := eng.Inspect(in.DSN)
if err != nil || di.Password != next { t.Fatal("patch was not persisted inside the DSN") }
if bytes.Contains(rec.Secret, []byte(next)) { t.Fatal("credential stored unsealed") }
```

Table assertions cover nil omission, exact whitespace/empty strings, DSN+password using the new target, SQLite/other unsupported engines, every final database-reference combination including whitespace normalization and explicit removal, DSN+DSNRef conflicts even when empty, and unchanged TLS/SSH conflicts. Use nonexistent test-owned reference paths and resolver counters to prove rejection precedes reference work; no real files. Compare all loaded non-password settings before/after and scan plaintext metadata/public info.

- [ ] **Step 2: Add lifecycle/policy and integration assertions before wiring behavior.** Use an instrumented test-only engine/pool opener to record the tested/opened DSN and deterministic channel barriers for concurrent updates. Assert `test=true` actually tests the replacement, a failed test/validation/rewrite preserves the old record/secret/version and usable pool, `test=false` makes no connection, success invalidates cache and subsequent acquisition uses the new version, and in-flight old-version work follows existing retirement semantics. Extend the `invStore` pattern in `invalidate_test.go` to observe shared invalidation after success only. Force a store version conflict with the first editor paused before save and assert the winning edit survives. Test server engine/DSN/TLS/SSH/network restrictions, plus error scrubbing with synthetic leaky engine errors, without expanding their policies.

Add `TestPasswordPatchAuthentication` in the integration file, with table subtests for PostgreSQL 17, MySQL 8.4, MariaDB 11.4 and MongoDB `mongo:8.2`. Test-owned accounts start with one password, are rotated through independent administrative connections, then update through `Service.UpdateConnection(test=true)` and execute a query. A wrong-password update fails without replacing the good stored credential for each engine. PostgreSQL also exercises a one-slot pool across the version change. Follow existing testcontainers core-test lifecycle style, with short-test skips and registered container cleanup. Any new fixture helpers live in this test file; do not import other packages' private `startPostgres`, `startDB` or `mongoURI` helpers.

- [ ] **Step 3: Run RED.** `go test -short ./internal/core -run '^TestPasswordPatch' -count=1`; expect the loaded DSN to retain its old credential, missing password-triggered testing, or unsupported/ref edits incorrectly succeeding. `go test ./internal/core -run '^TestPasswordPatchAuthentication$' -count=1` must also expose the ignored patch against real authentication.
- [ ] **Step 4: Wire the patch inside `UpdateConnection`.** After ordinary patch application, reject final trimmed database references when password is present, get the existing engine and assert the optional capability, rewrite `in.DSN`, then retain the existing validation/check/inspect/test/fill/store/invalidate path. Add `p.Password != nil` to its test condition. Keep the input pointer transient and errors fixed; do not set `ConnInput.Password` or runtime `ConnSpec.Password` to persist the edit. Do not add manual forced pool closure or a new secret field.
- [ ] **Step 5: Run GREEN and real-engine authentication checks.** Repeat both RED commands; run `go test -short ./internal/core` and `go test -race -short ./internal/core -run '^TestPasswordPatch(Lifecycle|ConcurrentConflict)' -count=1`. All four authentication subtests written in Step 2 must execute and pass with healthy Docker, not merely skip. The PostgreSQL scenario pins version/pool behavior; the other engines pin their own credential parsing.
- [ ] **Step 6: Review and commit.** Format the four task files, `go vet ./internal/core`, and commit them with `feat: rotate inline passwords through the connection update lifecycle` after review. Do not modify pool/store production code without a demonstrated requirement and renewed scope check.

## Task 5: REST, client and OpenAPI

**Files:** Modify `internal/httpapi/connections.go`, `internal/httpapi/openapi.json`, `internal/httpapi/openapi_test.go`, `internal/client/client.go`; create `internal/httpapi/connections_password_test.go`, `internal/client/password_test.go`.

**Interfaces:**
- Consumes: `core.ConnPatch.Password *string` and Task 4 update semantics; existing HTTP `newEnv(t, server, mutate)`/`e.do`, client `server(t)` and `New(baseURL, key, workspace string)` helpers.
- Produces: request-only `connectionPatch.Password *string` tagged `json:"password"`; direct mapping through `connectionPatch.core() (core.ConnPatch, error)`; client field serialization through unchanged `(*Client).UpdateConnection(ctx context.Context, name string, p core.ConnPatch, test bool) (*core.ConnInfo, *core.TestResult, error)`.

- [ ] **Step 1: Write `TestConnectionPasswordPatchREST`, `TestClientPasswordPatch` and `TestOpenAPIPasswordPatch`.** Create connections through existing synthetic DSN fixtures and `skip_test=true`, then exercise absent/null/empty/nonempty inputs, exact whitespace, wrong JSON types, unknown fields, same-patch DSN/reference changes, SQLite refusal and normal auth/workspace isolation. Decode the same request fixtures into `connectionPatch` and call its existing `core()` method to assert exact pointer/value semantics; Task 4 already proves core persistence. Add a test-only handler around the client's request to inspect actual JSON presence. In client table subtests, `body` is the decoded captured request map and `wantPresent`/`next` come from nil/empty/nonempty patch cases:

```go
value, present := body["password"]
if present != wantPresent { t.Fatal("password presence changed in transport") }
if present && value != next { t.Fatal("password bytes changed in transport") }
```

Direct REST null/omission must map to a nil core pointer; empty must map to a non-nil pointer to `""`. End-to-end requests must receive the corresponding existing success/error responses. Verify GET/list/PATCH outputs omit a `password` property and contain neither fixture secret, including malformed-DSN and authentication errors. Assert `ConnectionPatch.properties.password` allows string/null, while response `Connection` has no password property and `additionalProperties:false` remains. Retain request size limits. Exercise skip-test forwarding; an older-server strict decoder rejects the new field instead of simulating unsupported success.

- [ ] **Step 2: Run RED.** `go test -short ./internal/httpapi ./internal/client -run 'Test(ConnectionPasswordPatchREST|ClientPasswordPatch|OpenAPIPasswordPatch)' -count=1`; expect REST unknown-field rejection, client missing-field capture, and missing OpenAPI property.
- [ ] **Step 3: Wire the request/client fields and schema.** Pass the pointer directly into core; client adds `body["password"] = *p.Password` only for non-nil. OpenAPI 3.1 documents string/null, empty clearing, supported engines, reference conflicts and no new response field. Keep shared strict decoding, existing routes, authorization and successful response shape unchanged.
- [ ] **Step 4: Run GREEN.** Repeat RED, then `go test -short ./internal/httpapi ./internal/client`; require all passing, including OpenAPI route checks and existing SSH/reference patches.
- [ ] **Step 5: Review and commit.** Format changed Go files, run `go vet ./internal/httpapi ./internal/client`, and commit the six task files with `feat: accept password-only patches over the rest api` after review.

## Task 6: CLI stdin behavior, documentation and complete delivery checks

**Files:** Modify `internal/cli/conn.go`, `internal/cli/remote_test.go`, `README.md`, `CHANGELOG.md`, `docs/HANDOFF.md`; create `internal/cli/conn_password_test.go`.

**Interfaces:**
- Consumes: `core.ConnPatch.Password`, existing `withBackend`, `backend.UpdateConnection`, `connFlags.stdinReaders() int`, `readLine(cmd *cobra.Command) (string, error)` and Task 5 remote serialization.
- Produces: edit support in unchanged `connEditCmd(with withBackend) *cobra.Command`/`(*connFlags).register(cmd *cobra.Command, create bool)`; no new backend methods, `--password` flag or root routing.
- Test seams: existing `run(t *testing.T, home, stdin string, args ...string) (string, string, int)` and `remoteServer(t *testing.T) (string, string)`; use `Execute(..., stdin io.Reader, ...)` directly for counting/failing readers rather than changing `run` globally.

- [ ] **Step 1: Write `TestConnEditPasswordStdin`, `TestConnPasswordStdinConflicts` and `TestRemotePasswordEdit`.** With a test-owned local connection, table-test omitted/true/false flags, first line, no newline, immediate EOF, blank/whitespace, ignored later lines, a line above 64 KiB, and I/O errors. Use a backend recorder through `connEditCmd(with)` to inspect pointer presence/contents without redacted GET reconstruction; embed the existing `backend` interface in the test recorder and override only `UpdateConnection`, with a failing `GetConnection` override to catch unwanted reads. In the command fixture, `recorded` is the captured `core.ConnPatch`:

```go
if recorded.Password == nil || *recorded.Password != "replacement" { t.Fatal("stdin password was not forwarded") }
if code != 0 || strings.Contains(out+errOut, "replacement") { t.Fatal("edit failed or disclosed the fixture password") }
```

For every pair among DSN stdin/database/SSH password/SSH passphrase, assert error text `only one flag can read stdin`, reader count zero and update count zero in add and edit. False boolean flags do not count; database false and omission produce nil. Count read failures as no update. Use real `run` local and `remoteServer` end-to-end tests for clearing, reference rejection/removal and simultaneous literal DSN replacement; remote tests use an explicit workspace for admin keys. Test default `test=true` and `--skip-test` forwarding. Add creation-regression cases so moving flag registration cannot alter add behavior.

- [ ] **Step 2: Run RED.** `go test -short ./internal/cli -run 'Test(ConnEditPasswordStdin|ConnPasswordStdinConflicts|RemotePasswordEdit)' -count=1`; expect the edit's unknown `--password-stdin` flag or absent forwarded patch, not a fixture setup/authentication failure.
- [ ] **Step 3: Implement the CLI wiring.** Register the existing flag in both add/edit, retain `stdinReaders()`'s active-value check before any stdin read, and only call `readLine`/populate `p.Password` when `f.passwordStdin` is true. Send the patch directly through the backend. Keep existing trim/EOF/later-line policy and output. Do not add a scanner, secret flag, GET-rewrite workflow or extra flag spelling.
- [ ] **Step 4: Run GREEN.** Repeat RED and `go test -short ./internal/cli`; all tests pass and existing add/SSH flag tests stay green.
- [ ] **Step 5: Document verified behavior.** Add a password-edit section near README connection management using `qh conn edit NAME --password-stdin`, without shell-history password examples. Describe empty clearing, stdin trimming/one-reader rule, remote mode, reference restrictions, supported engines/SQLite and older-server rejection. Add the user-visible change to `CHANGELOG.md` under `[Unreleased]`; update handoff with actual test results and remaining approval state, excluding private notes.
- [ ] **Step 6: Run complete branch verification before the final review/PR.** Format only changed Go files; `git diff --check` must be silent. Run `go vet ./...`, `go test ./...` with healthy Docker, and `go test -race -short ./internal/core -run '^TestPasswordPatch(Lifecycle|ConcurrentConflict)' -count=1`. Run `go test ./internal/mcpserver -run '^TestSQLSurfaceUnchanged$' -count=1`; never regenerate its golden. Full tests cover existing schema goldens/conformance. Confirm no diff to MCP tool text, schema goldens, store migrations, dependencies or private files. Fix failures through their owning test cycle; do not broaden to paid benchmarks. Record actual commands/results and meaningful limits.
- [ ] **Step 7: Review and commit the CLI/documentation deliverable; then request whole-branch review.** Commit only the six task files with `feat: read password-only connection edits from stdin`. Resolve findings and rerun affected checks; repeat full verification only when subsequent changes justify it. A single feature PR may then be prepared with the approved spec, plan and implementation; attach it to the chat. Ask before merging, tagging or releasing.

## Coverage and handoff

The spec's engine transformation contract maps to Tasks 1–3; presence/reference/validation/encryption/version/pool requirements to Task 4; additive REST/client/OpenAPI compatibility to Task 5; stdin/local-versus-remote/docs and unchanged MCP/schema acceptance to Task 6. Real authentication and rollback tests belong to Task 4 rather than a separate test-only ticket.

The user explicitly approved this plan and chose subagent execution after approving the written spec. Plan self-review checked the global constraints, review-focus lines, exact interfaces and fixture helpers. Tasks 1–5 passed their independent reviews; Task 6 and whole-branch review remain before the feature PR. Merge, tagging, release and paid benchmark calls require separate approvals.
