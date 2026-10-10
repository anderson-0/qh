# Plan 3 (server mode): deferred review findings

Findings from the per-task and final reviews of Plan 3 that were judged safe to defer. The final review triaged each one as can-wait. Items fixed before merge are not listed.

## Parked after the final review

- URL DSN with ? inside IPv6 brackets (postgres://u@[::1?]/db) makes withSettings append forced settings into the dbname, so pgconn reads operator default ~/.pgpass / ~/.postgresql — Ruling: park; password is still cleared, client certs stripped, host unresolvable, no tenant-chosen path read — fix with bracket-aware query detection in Plan 4
- Inspect still reads operator ~/.pgpass / ~/.postgresql at fixed paths for the scrub list (never sent)
- IPv6 ranges like ::/80 never match IPv4 addresses
- revoking a manage key does not cascade to keys it created

## Deferred minors by task

- **Task 1**: CreateKey duplicate PK id not mapped to ErrExists (random 128-bit ids)
- **Task 1**: TouchKey on unknown id returns nil silently
- **Task 1**: scopes stored comma-joined; validated in auth (Task 2 unknown scope)
- **Task 1**: no test that admin keys survive workspace delete / v1 upgrade preserves connections
- **Task 2**: Bearer scheme match is case-sensitive (RFC 7235 says case-insensitive)
- **Task 2**: Authenticate returns raw store errors (httpapi maps non-qerr to 500 internal)
- **Task 2**: expired-key hint "Send Authorization: Bearer qh_…" is misleading
- **Task 2**: touch throttle untested; failed TouchKey suppresses retry for a minute
- **Task 2**: CreateKey prefix-collision retry loop has no attempt cap / ctx check
- **Task 2**: ParseExpiry "Nd" overflows time.Duration above ~106751 days
- **Task 2**: duplicate scopes stored (query,query)
- **Task 2**: invalid QH_BOOTSTRAP_ADMIN_KEY silently ignored once keys exist; env key re-created if all keys deleted — Task 8 could warn
- **Task 2**: prefix byte%36 slightly non-uniform (not secret)
- **Task 2**: ResolveWorkspace returns "" for a non-admin key with no workspace (validate prevents it)
- **Task 2**: auth_test.go `_ = info` dead variable (plan-mandated)
- **Task 2**: Bootstrap check-then-insert not transactional (two simultaneous first starts)
- **Task 3**: Allowed(invalid Addr) returns true; fail closed
- **Task 3**: IPv4-compatible ::/96 and NAT64 64:ff9b::/96 loopback forms not denied (not a working bypass on macOS)
- **Task 3**: non-tcp networks pass through to net.Dialer; could allow only tcp/tcp4/tcp6
- **Task 3**, plan-mandated: unix and skip-denied tests only assert err != nil; should assert *DeniedError and use a short ctx timeout
- **Task 3**: empty resolver result reported as policy refusal
- **Task 4**, plan-mandated: sqlite traversal test path is cleaned by filepath.Join so no ../ reaches Open; add a concatenated ../ DSN and a relative ../x.db
- **Task 4**: symlink inside root leaks target existence (Stat before EvalSymlinks check)
- **Task 4**: TOCTOU — checks `real` but sql.Open opens `path`; open `real`
- **Task 4**: no test that the lexical check precedes Stat (non-existent outside path)
- **Task 4**: relative FileRoot rejects all files (Task 8 makes it absolute)
- **Task 5**, plan-mandated: WorkspaceExists scans all workspaces per request
- **Task 5**, plan-mandated: DeleteWorkspace list-then-delete not transactional; test does not assert pools/cache dropped
- **Task 5**: UpdateConnection metadata-only edits blocked for SQLite connections while SQLite is disabled
- **Task 5**: TestSQLiteDisabled relies implicitly on a foreign keyring; no positive check for permitted engines
- **Task 6**, plan-mandated: recoverPanics writes a problem after headers were sent; panicked requests get no request log line
- **Task 6**, plan-mandated: client disconnect while waiting for a slot logged as internal error / 500
- **Task 6**, plan-mandated: wrong method returns 404 not 405 (catch-all "/")
- **Task 6**, plan-mandated: fail() for an unmapped qerr category returns 500 with raw type/message, unlogged
- **Task 6**, plan-mandated: problem.detail omitted when empty
- **Task 6**: decode accepts trailing data after the JSON value
- **Task 6**: limiter slot map never shrinks
- **Task 6**: connection-test routes are not per-key limited (spec lists only query/schema/MCP)
- **Task 7**: /mcp route accepts all methods but OpenAPI documents only POST (parity test maps "" to POST)
- **Task 7**: GET /api/v1/keys?workspace= (empty) lists admin keys; unknown workspace returns [] — undocumented
- **Task 7**: per-workspace MCP server cache never shrinks
- **Task 7**: OpenAPI lacks response schemas for /schema JSON and /schema/refresh
- **Task 7**: no tests for admin key naming a workspace (400) and manage key deleting an admin key id (404)
- **Task 7**: deleteKey ownership check lists all workspace keys (O(n))
- **Task 8**: listen and filepath.Abs errors print without error[category] prefix
- **Task 8**: bootstrap prints the admin key before listen; a busy port leaves a printed key for a server that never started
- **Task 8**: Shutdown timeout exits 1 without srv.Close()
- **Task 8**: no tests for local-mode serve, SIGTERM, QH_BOOTSTRAP_ADMIN_KEY, restart without reprint; e2e hides server stderr on startup failure; cmd.Wait while stderr goroutine still reads
