# MySQL write cancellation and overhead, 2026-10-15

The fix stops observed timed-out InnoDB writes before returning on MySQL 8.0 and 8.4. It also covers explicit cancellation, stored procedures (including later result sets), side-effecting functions called through SELECT, row-cap interruption and engine shutdown. The old implementation can return while the server still runs and holds locks. A procedure with an initial result set can even finish later and return success after the deadline.

This adds measurable cost to read-write calls. On this machine, the median of five batch p50s rose from 0.763 to 1.718 ms for a read-write SELECT and from 2.004 to 2.940 ms for a one-row UPDATE. Read-only SELECT medians were 0.381 and 0.374 ms; that small difference is not evidence of a speed improvement.

## Controlled comparison

Baseline `99b4c39`, implementation `edde383`; Go 1.27.2, macOS arm64, Docker Desktop, one disposable MySQL 8.4 instance and the existing normal-user fixture. No paid API calls. Each engine has `pool_size=1`. Each worker opens its engine, runs 20 warm-up calls, then times 100 calls individually. There are five batches per workload/revision; revision order alternates old/new, new/old. Both revisions use the same server. Tests and other profiling runs were finished before this comparison.

Workloads: `SELECT id,name FROM users WHERE id=1` on read-only and read-write connections, and `UPDATE users SET name=IF(name='probe-a','probe-b','probe-a') WHERE id=1` on read-write connections. The UPDATE changes data each time. Workers run sequentially. The read-only cleanup runs asynchronously, as before; status collection waits for its query slot to become idle.

All figures below are milliseconds. These are per-batch quantiles, not confidence intervals or an end-to-end MCP measurement. A local container has low network latency; added requests can cost more over a remote connection. The probe includes normal engine transaction/reset behavior and connection setup when a write discards its session.

| Workload | Batch | Old p50 | New p50 | Old p95 | New p95 |
|---|---:|---:|---:|---:|---:|
| ro-select | 1 | 0.419 | 0.377 | 0.518 | 0.461 |
| ro-select | 2 | 0.381 | 0.366 | 0.422 | 0.408 |
| ro-select | 3 | 0.382 | 0.374 | 0.428 | 0.484 |
| ro-select | 4 | 0.378 | 0.374 | 0.448 | 0.434 |
| ro-select | 5 | 0.381 | 0.426 | 0.486 | 1.064 |
| rw-select | 1 | 0.714 | 1.581 | 0.802 | 2.045 |
| rw-select | 2 | 0.763 | 1.605 | 1.434 | 1.888 |
| rw-select | 3 | 0.800 | 1.718 | 1.407 | 1.999 |
| rw-select | 4 | 0.768 | 1.771 | 1.236 | 2.276 |
| rw-select | 5 | 0.720 | 1.736 | 0.980 | 2.933 |
| rw-update | 1 | 2.044 | 2.714 | 2.640 | 3.513 |
| rw-update | 2 | 1.778 | 2.687 | 1.945 | 3.405 |
| rw-update | 3 | 1.801 | 2.940 | 2.075 | 3.370 |
| rw-update | 4 | 2.004 | 3.026 | 2.180 | 3.392 |
| rw-update | 5 | 2.155 | 3.218 | 2.640 | 4.803 |

Server counters per 100 measured calls:

| Workload | SQL statements old/new | Prepares old/new | New physical dials old/new | Live tracked sockets after batch old/new |
|---|---:|---:|---:|---:|
| ro-select | 300 / 300 | 0 / 0 | 0 / 0 | 1 / 1 |
| rw-select | 600 / 1,100 | 0 / 200 | 0 / 0 | 1 / 2 |
| rw-update | 800 / 1,300 | 0 / 200 | 100 / 100 | 0 / 1 |

SQL counts use the global `Questions` delta, subtracting the one status observation. They include driver initialization SQL on newly opened sessions. `Questions` excludes prepares and statement closes; `Com_stmt_prepare` adds two prepares per protected call. Thus the protected path adds five SQL statements and two prepare request/response exchanges per call, plus two statement-close commands (which do not require a server response). These counts exclude authentication handshakes. All measured `Com_kill` deltas were zero. See [MySQL's status-counter definitions](https://dev.mysql.com/doc/refman/8.4/en/server-status-variables.html).

Dials/live sockets are counted by the injected dialer and its Close wrapper; they exclude the independent status observer. Both versions discard query sessions after writes. The new control session remains pooled, so normal read-write SELECT retains two sockets; after UPDATE only the control socket remains. These small workloads do not measure throughput under load.

## First call with a cold control pool

Each sample below uses a fresh engine. Opening it has already connected its query pool to run VERSION; no query warm-up occurs. This separates the first control connection cost from steady-state batches. Values are individual call durations in milliseconds, in batch order.

| Workload | Old samples | New samples |
|---|---|---|
| ro-select | 0.358, 0.388, 0.413, 0.403, 0.432 | 0.448, 0.407, 0.357, 0.380, 0.811 |
| rw-select | 0.997, 1.107, 1.032, 1.091, 1.086 | 2.826, 2.985, 3.555, 3.430, 3.132 |
| rw-update | 2.309, 1.837, 1.889, 2.017, 1.880 | 4.059, 4.284, 4.257, 4.140, 3.828 |

The first protected call opens one extra socket; the first read-only call opens none beyond the engine's existing query socket. These samples are not first-ever database-start measurements.

## Server-side evidence

`TestWriteTimeoutStopsServer`, `TestWriteCancelStopsServer`, `TestWriteCallStopsServer` and `TestWriteFunctionStopsServer` observe an actual UPDATE that changes row 1 and sleeps while processing row 2. Before cancellation, an independent normal-user transaction receives NOWAIT error 3572 on row 1. On both MySQL versions, the old path returns with the target still active and that lock held. The corrected routine observer matches inner SQL as well as the caller: stored routine text need not retain its original comment.

With protected execution, those tests require the target to be inactive, NOWAIT to acquire the row, both original row values to remain, and a query through the one-slot pool to succeed. `TestWriteCallLaterResults` covers a procedure that returns an initial result before the write. It returns nil after the old server finishes its 20-second write; the new path consumes later results with cancellation armed and returns the deadline error after interruption/rollback.

Separate old-path checks fail for a saturated two-slot pool (canceled write still holds its lock), internal row-cap interruption (transaction lock still held), and Close during execution (write finishes successfully). The new tests pass. The saturated test proves the other write is unaffected. Row-cap tests use a function that writes a counter and require confirmed rollback/lock release before returning truncated success.

Deterministic tests cover lost markers, backend/owner mismatch, denied kill, broken control, missing terminal response, unknown thread replies, pre-admission cancellation, delayed dispatch, preparation/idle activity, completion/cancel races, late kills and commit ordering. They distinguish a terminal server error from socket EOF, and preserve a known successful COMMIT despite subsequent cleanup failures. Public-path fixtures cover row-cap control/marker failure and native-timeout/caller-cancellation precedence. Production-budget tests bound interruption to five seconds and shared rollback/control cleanup to another five, with a 12-second test allowance.

The four-version MySQL/MariaDB conformance and cancellation checks pass, as do the unchanged schema and MCP surface goldens. MariaDB and the read-only execution route retain their existing behavior.

## Costs and limits

- Protected read-write workloads can use up to twice `pool_size` physical connections, including idle connections. Control capacity is reserved before user SQL; a server/account connection limit can reject preparation.
- Direct MySQL sessions and proxies with physical backend affinity are supported. Transaction-only proxy pinning is insufficient across implicit commits. UUID/lock checks do not establish support for arbitrary multiplexing proxies.
- A fresh internal advisory lock identifies each query session. It is visible in database metadata, changes user-issued `RELEASE_ALL_LOCKS()` counts and is documented as unsafe for statement-based replication. User SQL releasing it removes the verified interruption route. Original user SQL is unchanged; qh does not log generated markers or session identifiers. See [MySQL locking semantics](https://dev.mysql.com/doc/refman/8.4/en/locking-functions.html).
- Kill acknowledgement alone does not prove completion. For an InnoDB rollback claim, qh requires the main terminal response and rollback acknowledgement. Otherwise the error carries `Server cancellation or rollback could not be confirmed; check the database before retrying a write` and the session is discarded.
- Cancellation can add up to ten seconds after the existing client deadline: five for interruption, then five shared by cleanup. This bounds qh's waiting, not every server rollback. It cannot undo a concurrent successful commit, implicit DDL/stored-program commits or nontransactional effects. A confirmed COMMIT remains successful even if cleanup later fails.

## Reproduction artifacts

[Raw batch records](mysql-write-timeout-2026-10-15/latency.jsonl) contain every duration in nanoseconds, batch p50/p95, counters and socket counts; no credentials, generated lock names or session IDs. [The temporary probe](mysql-write-timeout-2026-10-15/probe.go.txt) is saved as report data, outside compiled Go source.

To repeat: archive each revision into a temporary checkout, copy the probe to each engine package as `z_write_measure_test.go`, and compile each with `go test -c -o /tmp/qh-write-{old,new}.test ./internal/engine/mysql`. Run the new binary with `QH_WRITE_PROBE_ROLE=leader`, `QH_WRITE_PROBE_OLD` and `QH_WRITE_PROBE_NEW` pointing to the binaries, and `QH_WRITE_PROBE_OUTPUT` pointing to a new JSONL result. Use `-test.run '^TestWriteProbeLeader$' -test.timeout 3m`. The leader creates one fixture, passes its connection settings to workers only in memory, alternates revisions and terminates its container. It needs Docker and calls no model API.

## Free MCP micro-benchmarks

Both runs built qh from the current parent module. Agent runs and token counting were disabled, so accuracy and tokens were not measured. See [run 1](2026-10-15T163148/report.md) and [run 2](2026-10-15T163315/report.md).

| Metric | Run 1 qh / Toolbox | Run 2 qh / Toolbox |
|---|---:|---:|
| Schema median of three processes | 22.6 / 29.5 ms | 23.3 / 24.0 ms |
| Query p50 | 2.4 / 0.4 ms | 0.6 / 0.4 ms |
| Query p95 | 12.6 / 5.5 ms | 5.6 / 5.7 ms |

The qh schema samples were 210.1, 22.6, 20.2 ms and 184.9, 22.4, 23.3 ms. Three fresh processes share one database, so caches warm; medians conceal the initial cost. Toolbox connects before its timed schema call; qh's timing includes connecting. qh builds the full catalog but lists 200 tables with a pattern hint, while Toolbox lists 1,000 names. The schema medians overlap earlier qh measurements around 22.4 ms; no schema speed improvement is claimed. Query timings vary substantially and do not establish a latency win. These read/schema MCP measurements use different workloads and timing boundaries from the controlled engine probe above.
