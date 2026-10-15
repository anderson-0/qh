# MySQL Server-side Write Cancellation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Interrupt timed-out MySQL writes through a verified control connection, with observable rollback/recovery and bounded, truthful failure reporting.

**Architecture:** Register each protected call before acquiring resources, then reserve a query session and a separate control session. A per-call guard verifies backend/advisory-lock identity, arbitrates completion and interruption, and joins before cleanup or commit. Keep the read-only fast path and MariaDB execution unchanged.

**Tech Stack:** Go 1.27, `database/sql`, the existing `go-sql-driver/mysql` v1.10.1, testcontainers, MySQL 8.0/8.4, MariaDB 10.11/11.4. No new dependency.

**Spec:** [Approved design](../specs/2026-10-15-qh-mysql-write-timeout-design.md), approved by the user after commit `65621f1`. Behavioral comparison base: `99b4c39`.

## Global Constraints

- "Cancellation must work with `pool_size=1` and with every query slot occupied."
- "Keep MCP names, descriptions, instructions, successful result formatting and schema output unchanged. No new error category, configuration field or privilege requirement. Paid benchmarks remain outside this work."
- "Preserve the current timeout budget: `QueryOpts.Timeout` (30 seconds by default), the existing two-second client backstop, and any earlier caller deadline."
- "Track the interruption reason explicitly: caller cancellation, client deadline, engine shutdown or internal row cap."
- "Rollback/reset and control-session cleanup share a separate five-second bound." Interruption has its own five-second total bound. Production budgets are constants, not new settings.
- "Do not retry a kill through a replacement connection." Both reserved sessions require physical backend affinity across implicit commits; arbitrary multiplexing proxies are outside the guarantee.
- Fixed hint: `Server cancellation or rollback could not be confirmed; check the database before retrying a write`.
- Keep one branch/PR, `codex/mysql-write-timeout`. Tests first; review each task and the complete branch. Ask before merging, tagging or releasing. Preserve existing untracked media and benchmark directories; never stage them.

## Review Focus

Each item below has an owning test in the tasks; these are the less obvious conditions to review alongside the direct spec cases.

1. A stored function can write through a `SELECT`, and `CALL` can show an inner statement in PROCESSLIST: protect the read-write execution path and identify its session independently of SQL text (Tasks 1, 3).
2. Cancellation can arrive before admission, during dispatch or after completion but before commit: no missed interruption, preparation kill or late commit (Tasks 2, 3).
3. A control session can fail or reconnect onto a different backend: no unchecked replacement kill, secret-bearing error or false confirmation (Tasks 1, 2).
4. A socket error is not a terminal server response; a successful COMMIT followed by reset failure is still a committed write: preserve both distinctions (Tasks 2, 3).
5. Shutdown can race resource acquisition, while internal row-cap cancellation has no canceled caller context: join all registered work and preserve successful, confirmed truncation (Tasks 1–3).

## Files and private interfaces

| File | Responsibility |
|---|---|
| `internal/engine/mysql/cancel.go` (new) | Per-engine registration/control-pool lifecycle, per-call preparation, guard state, verified interruption and bounded cleanup |
| `internal/engine/mysql/cancel_test.go` (new) | Guard/lifecycle/identity tests with deterministic event barriers |
| `internal/engine/mysql/cancel_testdriver_test.go` (new) | A strict `driver.Connector` test fixture: real `database/sql` connections, scripted SQL/results/failures, connection IDs and event barriers |
| `internal/engine/mysql/write_timeout_test.go` (new) | Real server work, locks, rollback, stored-program and saturated-pool tests; reuse `startDB`/`specs` |
| `internal/engine/mysql/conn.go` | Retain configured connector; initialize/close cancellation support; route protected execution; shared collector and finish integration |
| `internal/engine/mysql/errors.go` | Format recorded interruption outcomes, preserving PR #27 classifications |
| `internal/engine/mysql/dial_test.go`, `tls_test.go` | Same connector/dialer/password/TLS policy for the control path |
| `bench/results/mysql-write-timeout-2026-10-15.md` (new) | Measured overhead, server-stop evidence and limits |
| `CHANGELOG.md`, `docs/HANDOFF.md`, `docs/superpowers/plans/ROADMAP.md` | Implemented behavior and next work, updated after validation |

Use these package-private interfaces throughout the tasks. Names do not alter the engine/MCP API:

```go
const interruptWait = 5 * time.Second
const cleanupWait = 5 * time.Second
const cancelPoll = 10 * time.Millisecond
const unconfirmedHint = "Server cancellation or rollback could not be confirmed; check the database before retrying a write"

type stopReason uint8 // stopNone, stopCallerCancel, stopDeadline, stopShutdown, stopRowCap
type stopOutcome struct {
    Reason stopReason
    Admitted, StatementDone bool
    Err error // internal interruption cause; never copied into agent text
}
type cleanupResult struct {
    Committed, RolledBack bool // actual server acknowledgement, not intended action
    Err error // internal cleanup failure
}

func newWriteCanceler(owner *conn, connector driver.Connector, size int) *writeCanceler
func (w *writeCanceler) Begin(ctx context.Context) (*writeCall, error)
func (w *writeCanceler) Close() error
func (c *writeCall) Context() context.Context
func (c *writeCall) Prepare(query *sql.Conn, setupSQL string) error
func (c *writeCall) Admit() (context.Context, error)
func (c *writeCall) RequestTruncation()
func (c *writeCall) Complete(statementErr error) stopOutcome
func (c *writeCall) CommitAllowed() bool
func (c *writeCall) Cleanup(commit bool) cleanupResult

func (c *conn) execProtected(ctx context.Context, q string, o engine.QueryOpts, access sqlx.Access) (*engine.Result, error)
func (c *conn) finishContext(ctx context.Context, sc *sql.Conn, commit, rw bool) cleanupResult
func interruptionError(ctx context.Context, stop stopOutcome, cleanup cleanupResult, statementErr error) error
```

`Prepare` immediately takes ownership of `query`, including when it fails. It applies setup, starts the main transaction, reserves control, establishes the marker and verifies identity. `Cleanup` owns both sessions and registration; call it exactly once on every path, including failed acquisition/preparation. It is idempotent for defensive early-return handling. A `Begin` with no acquired query still unregisters through `Cleanup(false)`.

`Complete` is called after the collector has closed/drained its rows. Only a clean protocol completion or an actual server error packet establishes `StatementDone`; socket closure, local context cancellation and unknown-thread replies on control do not. `Complete` joins the interruption worker. `CommitAllowed` is a distinct synchronized commit-start decision, not a synonym for completion. `Cleanup(true)` requires that decision and retains a known successful commit even if subsequent reset/control cleanup fails.

The test connector is a behavioral fixture, not a second guard implementation. Reject unexpected SQL/connections, return literal server values, and control scheduling through channels. Assert observable SQL order, preserved data/outcomes and bounded return. Keep all test-only hooks and fixture methods in `_test.go` files.

---

### Task 1: Reserve and identify cancellable sessions; own their lifecycle

**Files:** create `cancel.go`, `cancel_test.go`, `cancel_testdriver_test.go`; modify `conn.go`, `dial_test.go`, `tls_test.go`.

**Interfaces:** produces `newWriteCanceler`, `Begin`, `Context`, `Prepare`, `Cleanup`, `Close`, `finishContext` and `cleanupResult`. `conn` gains `writes *writeCanceler`. Later tasks use the reserved sessions through `writeCall`; they must not create another control pool or bypass `Begin`.

- [ ] **1. Write the lifecycle/preparation tests before their implementation.** In `cancel_test.go`, use a strict connector with distinct query/control IDs and literal UUIDs. Assert:

```go
// TestWriteControlPreparation: bound control session, same UUID, acquired marker.
if gotUUID != "server-a" || lockResult != int64(1) || ownerID != uint64(41) { t.Fatal("identity not established") }
// TestWriteControlNoEscalation: unexpected privileges/connection replacements fail the fixture.
// TestWriteCloseDuringPreparation: Close cancels blocked acquisition; both Close and Cleanup return.
// TestWritePreparationFailure: user statement count == 0; all reserved resources released.
```

Cover UUID mismatch, `GET_LOCK` result `0`/`NULL`, unavailable control capacity, failed setup/BEGIN, failed marker acquisition, expired caller and registration after Close. Assert two concurrent `Begin`/Close schedules cannot miss registration; repeated Close is safe. A pure read-only/MariaDB open issues no control SQL or extra dial. `Cleanup(false)` without `Prepare` must also finish registration. Add `TestWriteFinishAcknowledgement`: COMMIT or ROLLBACK acknowledgement survives a subsequent reset failure in the corresponding result field; reset failure discards the session, and the legacy finish wrapper still reports successful COMMIT as success.

- [ ] **2. Run and record the red tests.** `go test -short ./internal/engine/mysql -run 'TestWrite(Control|Close|Preparation|Finish)' -count=1`. Supply only the minimal declarations needed to compile, then observe the named behavioral assertions fail before implementing them; a build error alone is not regression evidence.
- [ ] **3. Implement the private lifecycle/preparation contract.** Reuse the already configured connector from `Open`; create the control `sql.DB` lazily, with the same maximum/idle sizes and idle lifetime as the query pool. Initialize `writes` only for MySQL; nil remains valid for existing test catalogs/MariaDB. Serialize registration against Close before acquiring anything. `Close` cancels preparing/active calls, waits for their cleanup, then closes control; `conn.Close` closes the main pool afterwards and preserves close errors.

Use a 42-character lock name (`qh_cancel_` + 32 hex digits) from `crypto/rand`; `GET_LOCK(name, 0)` must yield `1`. After setup/BEGIN, the final main preparation SELECT captures ID/UUID/acquires the marker and includes its unique internal comment. Reserve a control `*sql.Conn`, begin its transaction, verify UUID/lock owner and retain it through cleanup. Parameterize lock names; interpolate only the checked numeric target ID into KILL. Main user SQL is never rewritten. UUID mismatch/lock failure prevents admission. Cleanup releases the marker explicitly when necessary; rollback alone is insufficient.

Move the existing finish SQL into `finishContext`, recording COMMIT/ROLLBACK acknowledgement separately from reset errors. Keep `finish` as the existing five-second wrapper: a known successful commit still returns success after reset failure. `Cleanup(false)` uses one five-second context for main rollback/reset and control transaction cleanup, discards uncertain sessions, then unregisters. Do not leave this foundational method dependent on Task 3.
- [ ] **4. Verify connector policy with a real MySQL 8.4 test.** Extend dial/TLS tests using a recording dialer that delegates to the fixture server, a resolved password overriding a deliberately invalid DSN password, and `TLSRequire`. Assert both server sessions are encrypted and every connection went through that dialer. Reject the control dial on a second case: no user SQL executes and no secret/raw control detail appears in the returned error. Do not print DSNs.
- [ ] **5. Run the focused tests and short suite.** `go test ./internal/engine/mysql -run 'TestWrite(Control|Close|Preparation|Finish)|TestControlUses' -count=1`, then `go test -short ./internal/engine/mysql`. Expect PASS; no control sessions remain owned after cleanup. Record review findings, fix them, run `gofmt`/`go vet ./...`, then commit this component.

### Task 2: Arbitrate interruption and verify the server target

**Files:** modify `cancel.go`, `cancel_test.go`, `cancel_testdriver_test.go`; add real identity cases to `write_timeout_test.go`.

**Interfaces:** consumes Task 1's registered/prepared call; produces `Admit`, `RequestTruncation`, `Complete`, `CommitAllowed` and `stopOutcome`. `Cleanup` now joins the guard before using either session.

- [ ] **1. Write deterministic guard tests first.** Every case controls SQL completion/cancellation through channels, not a scheduling sleep. Literal expected outcomes include:

```go
// TestWriteGuardCancelBeforeAdmission
if userStatements != 0 || killStatements != 0 { t.Fatal("dispatched after cancellation") }
// TestWriteGuardDeadline: original cause remains DeadlineExceeded, not the driver's later Canceled.
if out.Reason != stopDeadline || !out.StatementDone { t.Fatalf("outcome = %+v", out) }
// TestWriteGuardCompletionWins
if killStatements != 0 { t.Fatal("late kill after completion") }
// TestWriteGuardMissingResponse
if out.StatementDone { t.Fatal("kill acknowledgement is not completion") }
```

Also force delayed dispatch (`TestWriteGuardDispatchRace`); transient preparation activity; idle lock ownership; matching active target; nested-statement text; wrong UUID/owner; explicit marker release; denied KILL; broken control socket; a queued kill when main completion arrives; unknown thread with/without main response; raw EOF vs server error; shutdown with an active request (`TestWriteGuardShutdown`); and a second call acquiring the session immediately after cleanup (`TestWriteGuardLateKill`). Assert KILL runs only on the original reserved control session, never touches the second call, and joins before any follow-up SQL/release. Cover row-cap reason without canceling the caller. Add `TestWriteCleanupBudget`: hold the kill response until the real five-second interruption budget expires, then hold rollback/control cleanup until their shared five-second budget expires; require return within 12 seconds total, with no goroutine continuing to issue SQL. Keep tests that wait for production timeouts out of repeated stress loops.
- [ ] **2. Run the named tests to red.** `go test -short ./internal/engine/mysql -run 'TestWriteGuard|TestWriteCleanupBudget' -count=1`. Record the missing behavior each test catches.
- [ ] **3. Implement the guard.** Use one mutex for admission/completion/stop/commit decisions, and one worker with a joined done channel. The statement context keeps caller values but is canceled by the guard only after completion or the interruption deadline. Watch the original request and engine lifetime separately; store the reason. A cancellation winner checks UUID, advisory owner and active-command state on the held control session; poll at `cancelPoll` while dispatch is pending, excluding the uniquely tagged final preparation SELECT. Issue at most one verified kill, then await the main statement's terminal response. The shared interruption context bounds all those operations to `interruptWait`; expiry closes the driver's statement context. Control failure never triggers a reconnect/retry. Completion disarms future kills and wakes polling, but cannot authorize commit.
- [ ] **4. Test real identity on MySQL 8.0/8.4.** `TestWriteGuardStoredProgramIdentity` creates a procedure whose inner statement is active, confirms the outer SQL is not the identity condition, and interrupts the intended held session. `TestWriteGuardReleasedMarker` clears the lock and verifies no KILL is attempted and confirmation is false unless main actually completes. Observe own sessions with a normal fixture user. These tests exercise preparation/guard directly, before routing production writes through it.
- [ ] **5. Run green tests and race checks.** `go test ./internal/engine/mysql -run 'TestWriteGuard|TestWriteCleanupBudget' -count=1`; `go test -race -short ./internal/engine/mysql -run 'TestWriteGuard(CancelBeforeAdmission|CompletionWins|DispatchRace|Shutdown|LateKill)$' -count=20`. Expect PASS with no late SQL after a test returns. Review this task, repair findings, run formatting/vet and commit.

### Task 3: Integrate protected execution, rollback and truncation

**Files:** modify `conn.go`, `cancel.go`, `errors.go`; extend `write_timeout_test.go`, `cancel_test.go`, `unit_test.go` as needed.

**Interfaces:** consumes Tasks 1–2, including `finishContext`; produces `execProtected`, `interruptionError`. Existing `exec`, `finish`, `collect` signatures remain available to unchanged MariaDB/read-only/explain paths.

- [ ] **1. Add real server regressions before integration.** `TestWriteTimeoutStopsServer` runs MySQL 8.0/8.4 with `pool_size=1`. Use an InnoDB UPDATE ordered by primary key that changes row 1 immediately and sleeps 20 seconds while updating row 2; use `Timeout: 100*time.Millisecond`, so the existing client backstop is 2.1 seconds. Observe its actual process ID and the row-1 lock before awaiting the timeout. After return require no running target, original row values and a successful `SELECT ... FOR UPDATE NOWAIT` from an independent transaction. Always install test-owned KILL/rollback cleanup so the old-code failure cannot leak a 20-second server task.

`TestWriteCancelStopsServer` cancels only after the observer sees that lock. `TestWriteCallStopsServer` wraps the same work in a procedure. `TestWriteFunctionStopsServer` exercises a side-effecting function called through SELECT on an RW connection; configure only the disposable fixture server to allow function creation if binary logging requires it, without granting the production caller elevated privileges. Each test also verifies a follow-up query and the classified category/cause. Run them on the old routing now and capture the observed failure; if the proposed reproduction does not demonstrate continuing work, investigate rather than claiming it did.

```go
if qerr.From(err).Category != qerr.Timeout { t.Fatalf("error = %v", err) }
if stillRunning || !lockAcquired || name1 != "Alice" || name2 != "Zoë 🚀" {
    t.Fatal("server work or uncommitted effects survived timeout")
}
```

- [ ] **2. Add integration/failure tests first.** `TestWritePoolSaturatedCancellation` uses two occupied query slots, cancels one actual UPDATE and proves the other call/data are unaffected. `TestWriteCommitOrdering` cancels after statement completion but before the commit-start decision: observe rollback, zero COMMITs. Its companion lets commit win, then fails reset/control cleanup: require successful write output, one COMMIT and discarded uncertain sessions. `TestWriteUnconfirmed` covers denied kill, missing response, raw socket failure and failed rollback; require the exact fixed hint, preserved category/cause and no raw control strings. No hint is needed for an unstarted statement.

`TestWriteRowCap` returns at least `drainBudget+MaxRows+2` rows with an explicit LIMIT overriding the session limit. Require successful capped/truncated output only after confirmed statement completion and rollback; lost-marker/control-failure variants return `query_error` with the hint. Check native server timeout precedence and actual caller cancellation during truncation. `TestWriteCloseDuringExecution` checks engine Close joins both preparing and executing calls before closing pools. Scripted SQL tests verify ordering; real DB tests verify data/lock outcomes.
- [ ] **3. Run the new tests to red against existing execution.** `go test ./internal/engine/mysql -run 'TestWrite(Timeout|Cancel|Call|Function|Pool|Commit|Unconfirmed|RowCap|CloseDuringExecution)' -count=1`. Separate actual old-code behavior failures from fixture/setup problems in the log.
- [ ] **4. Route only MySQL user execution through `execProtected`.** At `exec`, dispatch `flavorMySQL && commit` to the protected implementation. Register before `acquire`, preserve `callLimits` plus the two-second backstop, and pass acquired ownership to `Prepare`. After `Admit`, run unchanged user SQL through the guarded statement context. After the collector terminates, call `Complete` and join before ROW_COUNT, cleanup or commit. Nonprotected MariaDB/explain calls retain their current route.

Refactor the collector behind its existing wrapper into `collectWithStop(ctx context.Context, onCap func(), sc *sql.Conn, q string, limit int, read, drainAfterStop bool) (*engine.Result, bool, error)`. The existing wrapper supplies its original cancel function with `drainAfterStop=false`; protected execution supplies `RequestTruncation` with `true`. On the latter path, signal truncation asynchronously then drain/close for the terminal response; preserve already collected rows even when the terminal server error is 1317. The guard's five-second fallback must unblock a stalled drain. Never truncate writes merely because they return rows.

- [ ] **5. Integrate shared cleanup and error confirmation.** Use Task 1's `finishContext` through `Cleanup`, with one `cleanupWait` context between main rollback/reset and control transaction cleanup, then return/discard both sessions and unregister. Cleanup after known commit may discard control without changing write success. Do not infer rollback failure from reset failure if rollback was acknowledged.

`interruptionError` preserves known timeout codes before recorded cancellation, maps deadline to `timeout` and caller/shutdown to `error[query_error]: query canceled`, wraps actual causes, and adds only the fixed hint when admitted work lacks a terminal response or required rollback acknowledgement. A network error alone is not confirmation. Successful confirmed truncation returns its result while the original call is active; unconfirmed truncation discards its result and returns query_error. Do not pass internal control messages to `classify` as agent-visible text.
- [ ] **6. Run green, compatibility and focused race checks.** Run the above tests, existing `TestCancellationAndRecovery`, `TestConformance`, explicit-limit/session-state/failed-write/EXPLAIN tests, and `TestSQLSurfaceUnchanged`. Then `go test -race ./internal/engine/mysql -run 'TestWrite|TestCancellationAndRecovery' -count=1`. Review this task, resolve findings, run gofmt/vet and commit. No golden regeneration is authorized; investigate any difference.

### Task 4: Measure cost, complete verification and open one PR

**Files:** create `bench/results/mysql-write-timeout-2026-10-15.md`; modify changelog/handoff/roadmap. Temporary profiling files stay outside committed source/results unless explicitly part of the report.

**Interfaces:** consumes the complete protected execution path and existing benchmark CLI. Produces a reproducible measured report and reviewed PR; no new engine API.

- [ ] **1. Run an alternating old/new comparison on one MySQL instance.** Use `git archive 99b4c39` into a temporary directory for baseline source; do not switch/reset the shared checkout. Add the same temporary engine-package measurement probe to both copies. Use one fixture server and one-slot pools: a simple SELECT on RO, the same SELECT on RW, and a one-row InnoDB UPDATE on RW. After warm-up run at least five batches of 100 calls per revision/workload, alternating which revision runs first. Record raw per-call durations, per-batch p50/p95, round trips and opened/retained connection counts. Record a separate first protected call with a cold control pool. A normal protected query must produce zero KILL commands. Store no credential/DSN in results; do not infer overhead from isolated best timings.
- [ ] **2. Run two free read/schema micro-benchmarks.** `cd bench && go run ./cmd/bench -rival toolbox -micro-engine mysql -skip-agent -skip-tokens`, twice. Compare with current reports using their warm-database/three-process caveats. No Anthropic calls, `.env.local` loading or tool-surface changes are needed.
- [ ] **3. Write the report and delivered notes.** Include measured overhead and method; observed server stop, InnoDB rollback and lock recovery; up to twice the pool connections; five-second interruption plus shared five-second cleanup; physical-affinity/proxy limits; explicit marker release and advisory-lock metadata/count effects; statement-based replication limit; nontransactional/implicit/concurrent-commit caveats; unconfirmed-hint behavior. Mark only tested roadmap behavior complete. Update the handoff with actual PR/test state, not this plan's intended outcome.
- [ ] **4. Run final checks once on the final code.** `gofmt -l internal/engine/mysql/*.go` must print nothing; `go vet ./...` and full root `go test ./...` must pass with Docker. In `bench`, run vet/full tests. Run `git diff --check` and verify schema/MCP golden files are unchanged. Record which results were cached; do not claim fresh Docker coverage for a skipped or cached run. Broaden/repeat only for changed code, failures or unresolved concerns.
- [ ] **5. Complete independent whole-branch review.** Review the implementation and measurements against the approved spec and this plan. Resolve findings; rerun affected checks after fixes. Remove temporary probes, never the pre-existing untracked files. Commit measured documentation and plan progress on this branch.
- [ ] **6. Push and create the PR after checks pass.** Assert the GitHub account is `anderson-0` before remote operations; switch back if another task changed it. Push with the gh credential helper and use `gh pr create --body-file` for measured claims/caveats. Attach the PR to this chat, report CI and ask for merge approval. Do not enable auto-merge, merge, tag or release without explicit approval.

## Self-review and execution gate

Coverage: preparation/identity/connection policy → Task 1; interruption/admission/target/cleanup bounds → Task 2; public execution, commit, errors, truncation, server data/locks and shutdown → Task 3; compatibility, overhead and delivery → Task 4. Every Review Focus item has a named owning test. Types and signatures above are shared across tasks; no private API from an undefined task is assumed.

The user approved the spec, not this plan or an execution method. Review this plan and select execution before implementation. Inline execution is the recommended default because these four tasks share a small set of private lifecycle interfaces; retain per-task reviews and a final independent review. Delegated implementation is also available if the user explicitly chooses it.
