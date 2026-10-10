# Plan 2: deferred review findings

Findings from the per-task and final reviews of Plan 2 (MySQL/MariaDB) that were not fixed before merge. Triage them into later plans.

## Parked after the final fix wave (fix first)

- M4 regression: url.Values.Encode turns "," into %2C so charset=utf8mb4,utf8 URLs fail — Ruling: park for the next plan (fix: escape only "/" in RawQuery) — real but narrow; multi-charset URLs are rare — cost if wrong: users with a charset list must use the native DSN form until fixed.
- M5 regression: binary/varbinary literal defaults (reported as hex 0x78) now render quoted '0x78' — Ruling: park for the next plan (fix: drop binary/varbinary from the quoted types) — display-only — cost if wrong: misleading default shown for binary columns.
- sqlx.Classify returns Read for TABLE/VALUES … INTO OUTFILE (server writes the file); on MySQL ro the C1b text guard blocks it — Ruling: park for the next plan (treat "into" after table/values like select) — guarded today — cost if wrong: none while the guard exists.
- C1b guard also refuses ro reads whose identifiers contain "outfile"/"dumpfile", with a misleading read-only message — Ruling: park (dedicated message later) — safety over convenience — cost if wrong: rare false refusals.

## Deferred by ruling

- MySQL writes have no server-side timeout: send KILL QUERY from a separate connection when the client backstop fires (M3).
- A Read statement on a read-write connection that calls a function with side effects (temporary tables) can leave state on the pooled connection (M7).
- Ping and Introspect have no slot-wait/busy handling (M8).

## Other minor findings

- **Task 1:** 1290 mapped to read_only too broadly; 1044/1049 during a statement reported as connection_failed; malformed mysql:// URL can fall back to native parsing (should be invalid; hint to percent-encode); context.Canceled → query_error; tests lack special-char password and native-form forced-option cases
- **Task 1:** unescaped "/" in a URL query value (e.g. time_zone=Europe/Paris) now fails as invalid (encode as %2F)
- **Task 2:** acquire reports busy for any slot-wait DeadlineExceeded even when the server is down (check db.Stats InUse >= MaxOpenConnections)
- **Task 2:** MySQL writes have no server-side timeout (KILL QUERY on backstop); slot wait eats the query time budget (cross-engine); panic path would pool a dirty conn (unreachable today); introspection lacks busy/timeout handling; Explain accepts FOR CONNECTION n; no tests for clean conn after a query error or rw UNLOCK TABLES path; default spelling differs by flavour
- **Task 2:** failed rw non-Read statements (e.g. CALL that sets sql_mode then errors) are pooled after finish(false) — also discard on those paths
- **Task 2:** closure in assemble shadows receiver c; rw Read statements can leave user variables
- **Task 3:** CLI test's password-absent check is near-vacuous for "connection refused"; first conn add failure doesn't assert nothing was saved; README read-only paragraph is long
