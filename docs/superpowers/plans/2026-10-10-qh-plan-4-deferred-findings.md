# Plan 4: deferred review findings

Findings from the per-task and final reviews of Plan 4 that were judged safe to defer (the final review triaged each as can-wait). Items fixed before merge are not listed.

## Deferred minors

- **Task 1**, plan-mandated: sslnegotiation=direct + spec.TLS drops ALPN NextProtos (fails closed); clear SSLNegotiation or copy NextProtos in applyTLS
- **Task 1**, plan-mandated: local mode still reads DSN/default TLS files before spec.TLS replaces them; missing file fails Open
- **Task 1**: unix-socket hosts get TLS forced (pg) / socket path as ServerName (mysql)
- **Task 1**: no tests for empty-ca system roots, intermediates, sslmode=allow, live PG TLS
- **Task 2**: params can override structured fields (host/port/user/password/dbname); server-mode allow-list still applies
- **Task 2**: host validation lets control chars, ',' (multi-host) and 'a:80' (bracketed) through
- **Task 2**: tests don't pin '+'/%20 behaviour, pg sslmode, mysql special db names/redaction; no DSNBuilder compile-time assertions
- **Task 2**: sqlite BuildDSN returns untrimmed database
- **Task 3**: no dedicated v2→v3 upgrade test
- **Task 3**, plan-mandated: panic in reseal fn leaves BEGIN IMMEDIATE open; ROLLBACK error ignored
- **Task 3**: AppendQueryLog keeps ns precision in e.At; list orders by id not at; thin edge tests
- **Task 4**: corrupt tls summary displays as no TLS; stale UpdateConnection comment; vacuous ciphertext assertion; no test for password-as-field scrub on failed test or server-mode forbidden param via fields; %+v of secret-bearing structs in test failures; raw json error from decodeSecret
- **Task 5**, plan-mandated: synchronous log insert with no deadline (WithoutCancel) can block queries on a locked store
- **Task 5**: nil res guard in logQuery; tests miss Since filter, write RowsAffected, Duration, cancelled ctx
- **Task 6**: --old-key-file read error shows no cause; ErrCorrupt gets the "pass the key it was sealed with" hint; rows counted current by column only; no multi-workspace rotate test; CLI test ignores setup errors
- **Task 7**: ?limit=0 returns default 100 instead of 400 (OpenAPI says minimum 1)
- **Task 7**: OpenAPI ConnectionInput requires name, but /connections/test does not
- **Task 8**, plan-mandated: --server refusal untested in Task 8 (Task 9 remote_test covers mcp and masterkey; serve still untested); --no-query-log and flag-over-env precedence untested
- **Task 8**: --param password=… and comma-splitting in StringToString; readLine trims/accepts empty passwords
- **Task 8**: prune goroutine not joined before a.Close(); prune errors swallowed
- **Task 8**: tests don't clear QH_WORKSPACE from the developer's env; admin key create binds to $QH_WORKSPACE when set
- **Task 9**: redirect hint attached to every non-problem >=300 (e.g. proxy 502); CreateKey drops Workspace without a client workspace (document on interface)
- **Final**: key ls/rm refuse a stale QH_WORKSPACE; --generate key left behind if opening the store fails after generation; mcp starts pools before the workspace check

## Also noted by the final review (can-wait)

- One-shot local CLI commands (qh query) always log and cannot opt out of the query log.
- refuseRemote checks only --server; with QH_SERVER exported, mcp/serve/masterkey run locally; --key without --server is ignored.
- The admin-without-workspace hint says 'Send the X-QH-Workspace header' even to CLI users.
- The query-log insert is synchronous on the query path (bounded by the 5 s busy timeout); consider a buffered writer.
- Connections created from fields can change their password only by supplying a whole DSN (conn edit has no --password-stdin).
- No tests for a v2→v3 upgrade, MCP-over-HTTP key_id end to end, or a live Postgres TLS handshake (all verified by probe).
