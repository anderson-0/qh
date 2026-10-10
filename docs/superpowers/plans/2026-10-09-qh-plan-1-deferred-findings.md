# Plan 1: deferred review findings

Minor findings from the per-task and final reviews of Plan 1 that were not fixed before merge. Triage them into Plans 2–4.

Carried to Plan 3 by ruling: MCP SDK argument-validation errors are not in error[invalid] format (add a middleware); an older-version pool open can replace a newer pool (churn only).

- **Task 1:** unknown-command error is cobra's raw text, not error[invalid] format (root.go)
- **Task 1:** no -ldflags version injection in Makefile build
- **Task 1:** TestUnknownCommandFails does not assert empty stdout
- **Task 2:** fuzz has no Read+";SELECT 1" invariant; EXPLAIN ANALYZE write list lacks execute/declare/refresh; MySQL ANSI_QUOTES not modelled; Classify accepts Dialect 0; ReferencedTables over-collects function names (suggestion-only)
- **Task 2:** no MySQL backtick-with-semicolon test case
- **Task 3:** TestRegistry leaves "fake-a" in the global registry (breaks -count=2); Link is O(FKs×objects); find is case-sensitive vs Lookup (doc it); Only shares *Object — link before filtering
- **Task 4:** escaped cell may reach ~2x MaxCellRunes (doc "measured before escaping"); text "NULL"/"<bytes…>"/"-- " collide with markers; no tests for header escaping, \r, truncated+escaped cell; humanBytes 1024.0KB at boundary
- **Task 5:** duplicate single-col FK on same column overwritten; incoming refs omit non-PK target column; composite PK not marked composite; Inferred fields shown NN; overview prints header+… per namespace after cap; untested composite FK/incoming/caps/matview/collection; Compact round vs floor; named constants for 200/15/20; pattern also matches namespace name
- **Task 6:** no tests for 8-col/4-object/5-result caps; rank sorts all candidates (perf on huge catalogs); distance computed twice per table; no global hint length budget; quoted/dotted identifiers not unquoted
- **Task 7:** reject duplicate 4-byte key ids; warn on key file perms looser than 0600; Seal/Open accept empty aad; tests for wire format, nonce uniqueness, short blobs with known id, ErrExist path; tests discard errors / nil info on Stat failure
- **Task 7:** os.Link ErrExist branch untested; filesystems without hard links now error
- **Task 8:** UpdateConnection WHERE lacks `id = ?`, so a stale update can overwrite a deleted-and-recreated connection of the same name (incl. its secret)
- **Task 8:** RowsAffected errors ignored; StrictPragmas set on every open (sync.Once); tls column unmapped; tests don't cover time round-trip, nil namespaces, mode CHECK, two-handle WAL read; tests ignore setup errors
- **Task 8:** open backoff caps at 160ms not 100ms; worst-case open ~15s; every open takes BEGIN IMMEDIATE (could fast-path when schema current); concurrency test in-process only
- **Task 9:** limit-call error unwrapped; Explain drops Truncated; full pool + short timeout reports timeout not busy; one broken view fails whole introspection, no introspection timeout; suite escapes accept any error (could require read_only/invalid)
- **Task 10:** session state can leak across pooled connections (set_config(...,false), advisory locks) — consider DISCARD ALL / reset in AfterRelease
- **Task 10:** busy reported as timeout when query timeout < slotWait; 57014 on client cancel reported as timeout; bytea_output=escape size wrong (set bytea_output hex), BC timestamptz, arrays not canonical, unknown OID type names; introspection no slot wait/timeout; cursor cap limits side-effect fns on rw; URL sslpassword not redacted; TestBusy sleep-based
- **Task 10:** connect-failed message repeats the dial detail when pgx lists an address twice
- **Task 11:** duplicate build window after a miss (re-check inside flight); older version build can overwrite newer entry; cancelled ctx still starts a build; gens/entries never pruned; no test for fresh flight after Invalidate mid-build; sleep-based tests; document Catalog as read-only
- **Task 12:** an older-version open can replace a newer pool (never install a lower version)
- **Task 12:** Drop doesn't cancel an in-progress open; waiter ctx.Err and errClosed not qerr-classified; backoff skipped when caller timeout < connect timeout; makeRoom evicts then still fails; Start edge cases (tiny IdleTimeout panics, double Start, Close doesn't stop sweeper); Close closes in-use pools (doc as shutdown); single-flight test sleep-based; coverage gaps (30s ceiling, waiter shares failure, Close racing open)
- **Task 12:** a panicking Opener leaves a permanent opening entry/cap reservation
- **Task 13:** full DSN scrubbed only when a password was parsed; failed Inspect in resolve fails open (nothing scrubbed); passwords <4 chars never scrubbed
- **Task 13:** store/keyring errors returned as plain errors (not qerr, no hint); unknownSource hides store failures; info() placeholder blames decrypt for engine/Inspect failures; DeleteConnection race can reopen a pool for a deleted id; test gaps (AAD move, namespaces in suggestions, invalidation on update/delete, scrub on Schema/Refresh/Test, MaxRows ≤ 0)
- **Task 14:** SDK argument-validation errors are isError but not in error[invalid] format; unchecked Content[0] assertion in test; no test locking unknown_source/validation as isError
- **Task 15:** no tests for homeDir precedence, QH_MASTER_KEY, edit preserving other fields, show/ls redaction with a password DSN; empty stdin DSN gives normalize's message; MkdirAll doesn't tighten an existing home dir
- **Task 16:** session.Close error only logged in e2e; ci.yml lacks permissions block
