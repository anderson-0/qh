# MySQL write cancellation on the server

Status: written spec approved by the user after commit `65621f1`; the [implementation plan](../plans/2026-10-15-qh-mysql-write-timeout.md) awaits review. Base: `main` at `99b4c39` (PR #27 merged). This document describes intended behavior, not an implemented or measured fix.

## Goal and scope

When a MySQL write exceeds qh's existing client deadline, request interruption on the server, roll back transactional work where possible, and return a classified error. A closed client socket alone must not count as confirmed interruption. Cancellation must work with `pool_size=1` and with every query slot occupied.

Protect MySQL 8.0/8.4 calls through `conn.exec` that execute a user statement, including `CALL` and reads on a read-write connection: a stored function can write despite a statement starting with `SELECT`. Explain-only calls do not execute the user's write. Preserve the read-only fast path and MariaDB's existing native timeout behavior; test MariaDB 10.11/11.4 for compatibility.

Keep MCP names, descriptions, instructions, successful result formatting and schema output unchanged. No new error category, configuration field or privilege requirement. Paid benchmarks remain outside this work.

## Why a separate connection

MySQL's `max_execution_time` covers read-only `SELECT`, not general writes. The pinned Go driver's cancellation watcher closes its connection. `KILL QUERY` interrupts a statement while retaining its session, allowing explicit rollback; `KILL CONNECTION` also terminates the session and is not the selected mechanism. A successful kill command acknowledges a request, not completed interruption. These are documented limitations, not performance measurements. [MySQL timeouts](https://dev.mysql.com/doc/refman/8.4/en/server-system-variables.html#sysvar_max_execution_time), [KILL semantics and own-session permissions](https://dev.mysql.com/doc/refman/8.4/en/kill.html), [driver v1.10.1](https://github.com/go-sql-driver/mysql/blob/v1.10.1/connection.go).

MariaDB already limits ordinary writes through `max_statement_time`, with limitations for stored procedures. This change does not promise to extend that coverage. [MariaDB timeout scope](https://mariadb.com/docs/server/ha-and-performance/optimization-and-tuning/query-optimizations/aborting-statements).

## Connections and identity

- Retain the fully configured connector from `Open`: resolved password, TLS, custom dialer, SSH tunnel/network policy and enforced DSN settings apply to control connections too. Never reparse credentials or bypass the configured dialer.
- Add a private control pool, opened lazily for protected calls, with at most `pool_size` connections. Each protected call reserves one query connection and one control connection before dispatching its user statement. Control acquisition uses the call's existing deadline. Failure returns a classified error before executing the statement.
- `pool_size` still limits concurrent queries. Protected workloads can require up to twice that many database connections, including idle connections. Servers or accounts with a lower connection limit can reject a protected call during preparation. Document this cost.
- Capture `CONNECTION_ID()` and `@@server_uuid` on the query session after starting its transaction. Bind the reserved control session to the same server UUID before dispatch. Both sessions must remain attached to their physical backend through completion, including implicit commits. Transaction-only proxy pinning is insufficient for DDL or stored-program commits. Backend mismatch aborts preparation and rolls back, without running the user statement. The guarantee covers direct MySQL sessions and proxies that preserve this physical affinity; a UUID comparison does not prove arbitrary proxy behavior.
- Acquire a fresh per-call advisory lock on the query session with `GET_LOCK(name, 0)`, using `qh_cancel_` plus 128 random bits encoded as hex. Require result `1` before admitting the user statement. The control session checks `@@server_uuid` and `IS_USED_LOCK(name) = target_id` before killing. The session lock survives entry into stored programs and transaction boundaries; a SQL-text marker would not, because process-list text can show a nested statement. [Lock semantics](https://dev.mysql.com/doc/refman/8.4/en/locking-functions.html), [process-list text](https://dev.mysql.com/doc/refman/8.4/en/information-schema-processlist-table.html).
- Also require the target to be executing a query. Label the final internal preparation query with a unique comment and exclude that preparation text from the match: its server cleanup may briefly outlive its response. Once the user statement is admitted, any other active query on the reserved target belongs to that call, including nested statements. If it has not appeared yet, poll for execution or completion within the same interruption budget. Never kill an idle session solely because it owns the lock.
- A missing/mismatched lock or backend means no kill. Establish completion from the query response; otherwise report unconfirmed interruption. If user SQL releases the internal lock, for example through `RELEASE_ALL_LOCKS()`, the verifiable interruption path is lost; a later cancellation receives that fallback. Cover this case in tests.
- Use that same reserved control connection for the checks and numeric `KILL QUERY` command. Do not retry a kill through a replacement connection. Do not issue kills against an unverified backend or target. Endpoints that cannot preserve backend sessions are outside the interruption guarantee.
- Do not add generated markers, connection identifiers or raw control errors to qh's diagnostics or query logs. User-requested metadata results retain their normal meaning, including the observable internal lock described below. Query logs retain the original user SQL. Ordinary own-session permissions must suffice; do not request `SUPER`, `PROCESS` or `CONNECTION_ADMIN`.

The lock-owner check and physical-session reservation are complementary: the lock identifies the session, while reservation prevents the session from being handed to another qh request between checking and killing. Release the internal lock on every preparation failure and completion path; rollback alone does not release an advisory lock. The existing session reset releases all advisory locks, and a discarded connection releases its locks when the server ends the session.

The additional advisory lock is observable in database metadata and changes the count returned by a user-issued `RELEASE_ALL_LOCKS()`. Original user SQL remains unchanged. `GET_LOCK()` is documented as unsafe for statement-based replication; include that compatibility limit in the delivered notes. These are explicit costs of the chosen identity check, not changes to the MCP definitions.

## Execution lifecycle

Preserve the current timeout budget: `QueryOpts.Timeout` (30 seconds by default), the existing two-second client backstop, and any earlier caller deadline. This task does not change how pool wait is charged. Preparation uses that context normally and never admits a user statement after it has expired; cancellation after admission can race dispatch as described below.

A call registers with engine shutdown before reserving resources. Closing the engine cancels both preparation and execution; no new registration may pass the closed-state check. Once prepared, a per-call guard owns the statement and the two reserved sessions:

1. Arm cancellation before dispatch. Cancellation that wins before admission prevents dispatch altogether. Once admitted, dispatch can race cancellation: the guard must allow for the statement not yet appearing on the server. The driver's statement context retains request values but defers caller cancellation to the guard, so the socket stays usable while interruption is attempted.
2. Statement completion and caller cancellation compete through one synchronized state transition. Completion disarms the guard. Cancellation claims it and attempts the verified kill using a fresh context, with a five-second total budget for checks, kill acknowledgement and the statement's terminal response.
3. No follow-up SQL, rollback, commit, connection release or reuse can begin until the guard has stopped and any in-flight kill attempt has joined. Track the interruption reason explicitly: caller cancellation, client deadline, engine shutdown or internal row cap. Internal truncation must not arm a later kill against another call.
4. If the statement responds before that budget ends, join the guard, then use the existing rollback/reset path for a canceled call. On interruption failure or missing terminal response, cancel the driver's statement context and discard the query connection. Never return it to the pool.
5. Rollback/reset and control-session cleanup share a separate five-second bound. Thus cancellation handling can add up to ten seconds after the client deadline, including returning or discarding both sessions. This is a bound on qh's waiting, not a promise that every server operation or rollback finishes in that time.
6. Statement completion disarms killing but does not authorize commit. At a separate synchronized commit-start decision, join the guard and recheck caller cancellation and shutdown; roll back if either already won. Once commit starts, retain the existing commit outcome handling. Cancellation is not an atomic undo of a concurrent commit, implicitly committed DDL, stored-program commits or nontransactional changes.

For the protected read-write path, a row-cap interruption goes through the guard before the driver closes the socket. The collector must still consume the terminal response after requesting interruption. Confirmed interruption and rollback preserve the existing successful truncated result while the original call remains active. If cleanup cannot be confirmed, discard the result/session and return `query_error` with the fixed hint below. Native timeout codes and actual caller cancellation retain their precedence. The read-only collector path remains unchanged.

Release the control session after its transaction/state has been cleaned; discard it on uncertainty. There must be no pending kill after returning a result. `Close` blocks new guards, cancels active guards, joins their bounded cleanup and closes both pools. Registration and shutdown must be synchronized so a guard cannot escape shutdown or race a wait-group registration.

## Errors and confirmation

Keep PR #27's precedence: known server timeout codes `3024`/`1969` remain `timeout`; explicit client cancellation and shutdown are `query_error`; a client deadline is `timeout`. Preserve underlying causes. An interruption `1317` caused by this guard is classified from its recorded reason and the original request state, not the driver's later internal cancellation context. Do not turn successful internal truncation into a client cancellation error.

Confirmed interruption requires the statement's terminal response. For an InnoDB rollback guarantee, rollback must also complete successfully. Kill acknowledgement, socket closure or error `1094` (unknown thread) alone is insufficient proof.

If interruption or rollback cannot be confirmed, retain the timeout/cancellation category and add the fixed hint: `Server cancellation or rollback could not be confirmed; check the database before retrying a write`. Do not append raw driver/control errors. Record no claim that a nontransactional write was undone. A known successful commit retains its successful outcome.

## Tests and measurement

Tests come before implementation. Use normal database users and independent observers; synchronize on the actual query/lock state rather than starting cancellation after a fixed sleep.

- Reproduce a timed-out InnoDB write still executing/holding locks after the old client returns, on MySQL 8.0 and 8.4. Bound and clean up the reproduction with a test-owned control connection.
- After the fix, verify configured timeout and explicit cancellation stop that write, release its locks, preserve original table data and allow a follow-up query through a one-slot pool. Exercise `UPDATE`, `CALL` and the read-write execution path. Do not infer rollback just from a timeout error or a working replacement connection.
- With all query slots occupied, interrupt the intended call and prove an independent request is unaffected. Check cancellation before admission and during delayed dispatch, transient preparation activity, completion/cancel races, late kills, row-cap truncation, rollback/commit ordering and concurrent shutdown. Deterministic guard tests should force both sides of each race; run them under the race detector.
- Cover control acquisition failure, denied kill, broken control socket, backend/lock-owner mismatch, explicit release of the marker lock and missing terminal response. Verify bounded return, discarded sessions and the fixed unconfirmed hint. Verify control dialing uses the same injected dialer/TLS policy. Confirm a normal query sends no kill and releases its marker lock.
- Retain four-version conformance, schema goldens, error classifications and MCP surface golden. Run `gofmt`, root and benchmark-module `go vet ./...` and full `go test ./...` before the PR, plus focused race checks and independent review.
- Measure normal read and write overhead against `99b4c39` on the same MySQL instance, alternating revision order: at least five batches of 100 calls after warm-up, for a simple `SELECT` and a one-row InnoDB `UPDATE`, with `pool_size=1`. Report per-batch p50/p95, extra SQL round trips, connection counts and cold-control cost separately. Run two free MySQL micro-benchmarks for read/schema compatibility. Publish the cost and caveats; do not claim no overhead or a speedup without evidence.

## Files and delivery

Keep implementation inside `internal/engine/mysql`: `conn.go` owns connector/pool lifecycle and execution integration; a new `cancel.go` owns the per-call guard and interruption checks. Add focused guard tests and extend `mysql_test.go`/`dial_test.go` as needed. Update the changelog, handoff and roadmap only with implemented, tested behavior. One branch and one PR include this spec, the subsequent approved plan and implementation. Merge still requires explicit approval.
