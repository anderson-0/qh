# Edit a stored database password

Status: the user approved this written spec and the implementation plan and chose subagent execution. All six tasks are implemented on `codex/password-edit-spec`; full root tests, vet, focused race checks and the unchanged MCP surface check pass. Task 6 and whole-branch independent reviews precede the feature PR. Merge, tagging and release still require separate approval. Base: `main` at `61831b0`.

## Goal and scope

Let a user run `qh conn edit <name> --password-stdin` to replace only the inline database password in a stored connection. The user does not need the original connection string. Preserve its target, username, database and options, including TLS, topology and multi-host settings. Local CLI use and CLI use through `--server` must produce the same change through the core service.

Support PostgreSQL keyword strings and URLs, MySQL and MariaDB URLs and native driver strings, and MongoDB ordinary and SRV URIs. SQLite has no database password to edit and rejects this operation. Keep existing whole-DSN replacement available.

Do not change the MCP tools, their descriptions or instructions, schema output, query behavior, the store schema or the sealed-secret format. Username editing, a separately stored password override, new secret-reference resolution rules and changes to stdin trimming are outside this work.

## Chosen approach

Add a transient `Password *string` field to `core.ConnPatch`. Pass it through the REST client and the REST connection patch request. Core loads the existing secret, applies the patch and asks the engine to rewrite the inline password before following the existing validation, optional connection test and encrypted save path.

Engines opt in through a separate capability:

```go
type PasswordDSNSetter interface {
    SetPassword(dsn, password string) (string, error)
}
```

The base `engine.Engine` interface stays unchanged. PostgreSQL, MySQL/MariaDB and MongoDB implement the capability. Core returns a fixed `invalid` error when the engine does not implement it, including SQLite. The error contains no input values.

This approach keeps DSN syntax inside each engine and retains one persistent source for inline credentials. Requiring a complete replacement DSN would leave users reconstructing settings they cannot retrieve without redaction. A separately stored password override would add storage, precedence and compatibility rules, especially for empty passwords. Neither alternative is selected.

Do not reuse `engine.ConnSpec.Password` for persistent edits. That string is an existing runtime override for resolved `password_ref` values; it cannot represent the distinction between no patch and an explicit empty patch.

## Patch contract

| Input | Meaning |
|---|---|
| Core `Password == nil` | Do not edit the inline password. |
| Core pointer to a nonempty string | Replace the inline password with that exact string. |
| Core pointer to `""` | Clear the inline password. |
| REST `password` absent or `null` | Do not edit the inline password. |
| REST `password` string, including `""` | Apply the string as the password patch. |
| CLI `--password-stdin` or `--password-stdin=true` | Read the password and send a non-nil patch. |
| CLI flag omitted or `--password-stdin=false` | Do not read a database password or send its patch. |

Core and REST do not trim the supplied password. CLI input retains its existing trimming policy below. Clearing means that the stored DSN carries no nonempty inline password; it does not introduce a new policy for driver defaults, external authentication or fallback password sources. A connection test can therefore fail after clearing, or succeed through existing authentication behavior.

If a patch includes both a new `DSN` and `Password`, edit the new DSN. The new password must win over every inline database-password occurrence that the engine recognizes. An old password must not survive in an overridden query value, duplicate keyword or other password slot. Unrelated credentials, such as a TLS key passphrase, remain unchanged.

Apply all other patch fields first, retaining existing conflict and clearing rules:

- Supplying both `DSN` and `DSNRef` is invalid, even when either string is empty.
- Supplying `DSN` clears the old `DSNRef`. It does not clear `PasswordRef`.
- Supplying `DSNRef` clears the old inline DSN, including when `DSNRef` is empty. Clearing this reference alone does not restore an earlier DSN.
- Supplying an empty `PasswordRef` removes it. Omission preserves it.
- Existing TLS and SSH replace/clear conflicts remain unchanged.

When `Password` is present, reject the edit if the resulting `DSNRef` or `PasswordRef` is nonempty after existing whitespace normalization. Do this before resolving references or attempting the password rewrite. Do not read, resolve or freeze the database references into inline credentials. Use a fixed `invalid` message with a hint to replace the referenced DSN or clear the password reference explicitly.

A caller can move from a DSN reference to an inline DSN by supplying `DSN` and `Password` together, since `DSN` already clears `DSNRef`. A caller can remove a password reference and edit the inline password by supplying `PasswordRef` as an empty string in the same patch. Combining these is allowed if the final connection has an inline DSN and neither database reference remains. Existing validation still rejects an empty or otherwise unusable DSN. SSH references retain their existing behavior.

## Engine rewrite contract

`SetPassword` is a pure string transformation. It must not connect, resolve DNS, read environment variables, open credential/certificate/service files or resolve secret references. It must parse the supplied syntax and safely encode the new password. Syntax failures return `qerr.Invalid` with a fixed engine-specific message; parser errors must never quote the original DSN, old password or new password.

Preservation is semantic, rather than byte identity: harmless encoding or formatting normalization is allowed. Every explicitly supplied non-password setting must retain the same driver meaning and precedence. Preserve omitted settings as omitted when rebuilding would otherwise capture machine defaults. Do not rebuild from the small `DSNInfo` summary or from host/database/user fields, which would lose options. Do not use connection-opening transformations that enforce runtime settings as a serializer.

A password patch must not invent a different username. Existing anonymous or missing-user forms keep their username semantics; normal validation and the optional connection test decide whether the resulting authentication configuration is usable. If a format cannot represent the requested password while preserving the username, return a fixed `invalid` error instead of silently discarding the password or changing the username.

### PostgreSQL

Handle both `postgres://` and `postgresql://` URLs and the keyword/value grammar accepted by the pinned pgx dependency. Cover passwords absent, empty or present; escaped quotes and backslashes; whitespace; repeated password keywords; URL userinfo and query passwords; query overrides; percent encoding; socket hosts; IPv6; host lists and corresponding port lists. Preserve unrelated repeated settings and their effective precedence. Clear or replace all database-password slots so none retains the old value.

Use a syntax-aware transformation of the explicit string. The existing `pgconn.ParseConfig` merges environment defaults and can read service, pass and certificate files. Its resolved config is therefore unsuitable as the rewrite source. A syntax-only helper must follow the pinned driver's keyword and URL grammar, including forms that a generic URL parser may not accept or interpret the same way. Ordinary validation remains in the existing core path after rewriting; the setter itself must not call a parser that resolves those external inputs. No local defaults may become persisted settings as a side effect of this edit.

### MySQL and MariaDB

Handle `mysql://` and `mariadb://` URLs and go-sql-driver native DSNs, including TCP, IPv6 and Unix sockets. Preserve the username, address, database, driver options and arbitrary session parameters, with the same escaping and interpretation as the engine's existing raw parser.

For native DSNs, use a syntax-aware rewrite of the credential span, following the pinned driver grammar. Preserve the transport/address, database and option suffix verbatim, including `loc=Europe%2FParis`, charset lists, time zone values, TLS settings, timeouts and boolean options. For URLs, rewrite userinfo without reparsing options through the native driver. MariaDB uses the same rewrite semantics as MySQL.

Do not use the current `ParseDSN`/`FormatDSN` pair, or `parseRaw` which calls it, as the transformation: parsing `loc` calls `time.LoadLocation`, which can read environment-selected timezone data or zone files; formatting drops the credential block when the username is empty, even if a nonempty password was supplied. Do not use `parseConfig` either: it additionally enforces runtime options, removes controlled session parameters and adds defaults. The normal validation path after rewriting remains unchanged. Missing or empty usernames must retain their meaning; a nonempty password patch must survive the rewrite or fail with a fixed `invalid` error if the native grammar cannot represent it.

### MongoDB

Use the existing URI parser's understanding of userinfo, password offsets, hosts and options. Support `mongodb://` multi-host URIs and `mongodb+srv://` without performing SRV or TXT lookup. Preserve host order and ports, database, `authSource`, authentication mechanism, replica set, TLS and repeated options with their existing interpretation. Preserve option spelling, ordering and duplicate precedence where changing them could change behavior.

Encode credentials for the driver's URI grammar, including reserved characters and existing percent encoding in usernames. Do not feed a multi-host URI through a generic URL reconstruction that loses hosts or options. Rewriting the password must not resolve topology or authentication defaults.

## CLI and REST flow

Register `--password-stdin` for editing as well as creation. Keep creation behavior unchanged. On edit, use the flag's true value to decide whether to read and populate `ConnPatch.Password`; a flag being syntactically present with value false is not an edit.

Before any stdin read, count the active readers: `--dsn -`, database `--password-stdin`, `--ssh-password-stdin`, and `--ssh-passphrase-stdin`. More than one returns the existing `invalid` error, `only one flag can read stdin`. Apply this check in both add and edit paths. False boolean flags do not count. Keep the existing SSH flag names; there is no second database-password flag or new `--password` argument.

Reuse `readLine`: read the first line; accept EOF without a newline; apply `strings.TrimSpace`; accept an empty result, including immediate EOF; ignore later lines. Do not introduce a Scanner's default 64 KiB limit or another arbitrary password length cap. A read failure aborts before the backend update. Tests must prove a reader conflict is detected without consuming input.

The CLI sends the patch directly to its backend. It must not fetch the public connection and reconstruct its DSN: `ConnInfo.DSN` and REST GET responses are redacted. `internal/cli/root.go` already selects the local backend or REST client; both continue to reach `Service.UpdateConnection`.

Add `Password *string` to the HTTP request patch and map it directly to the core patch. Add the corresponding field to `Client.UpdateConnection` only when non-nil, preserving an empty string in JSON. An absent or JSON-null field decodes to nil; a non-string, non-null value is invalid. Document this exact contract in the OpenAPI `ConnectionPatch`, including nullable input, empty clearing, reference conflicts and supported engines. Unknown fields continue to be rejected by the existing strict JSON decoder. Existing request size limits stay unchanged.

Do not add a password field to `ConnInfo`, public connection JSON, GET/list output or successful edit output. The response stays the existing redacted connection and optional test result. The REST endpoint and authorization/workspace rules stay unchanged.

## Validation, persistence and lifecycle

After loading and applying the ordinary patch fields, check the final database-reference conflict, rewrite the inline DSN when requested, then use the existing `denySSH`, normalization, engine allow-list, input/server policy checks and inspection path. Preserve the existing relative validation order where it does not conflict with rejecting database references before rewriting. A patch is never a route around server DSN restrictions, TLS, SSH or network policy.

Include `p.Password != nil` in the existing condition that triggers a connection test. CLI tests by default; `--skip-test` and REST `skip_test=true` retain their meaning. Testing uses the rewritten DSN and the final TLS/SSH/mode settings. A failed rewrite, validation or test leaves the stored secret, metadata, version and active pool behavior unchanged. A skipped test skips connecting; it still performs all ordinary applicable validation.

On success, save through the existing encrypted secret encoding and optimistic store update. Persist the rewritten DSN inside the existing sealed payload. Add no password column, sidecar secret field or new secret format. No password enters plaintext metadata. A concurrent-version conflict retains the existing retry error and cannot overwrite a newer edit.

Retain the normal version increment and schema-cache invalidation, including the existing shared-store notification. Subsequent pool acquisition sees the new record version and retires the old version through the current pool manager. Do not promise an immediate forced close of every active session: in-flight users of an old pool retain its existing retirement behavior. Verify that later operations use the new password and cannot reuse an old-version pool as current.

The new field is secret input. Do not echo or log it or either credential-bearing DSN. Reuse existing test/open error scrubbing with the rewritten credential and add focused checks for new rewrite failure paths. Keep failures generic where a parser could include credentials. This work does not expand into unrelated security changes.

## Acceptance and verification

Tests come first during the subsequent implementation stage. Use synthetic fixture credentials and controlled test services, never real credential files.

- **Engines:** For every supported format, compare the driver's effective non-password settings before and after replacement and clearing. Cover absent and empty passwords, reserved characters, Unicode, quotes/backslashes, whitespace where supported, percent encoding and duplicate password sources. Verify no old password remains, unrelated credential settings survive, and repeated non-password precedence is unchanged. Include PostgreSQL keyword/URL multi-host fixtures and omitted defaults, MySQL/MariaDB URL/native socket and option fixtures (including `loc=Europe%2FParis` with the non-credential suffix unchanged), and MongoDB SRV/multi-host/authentication/topology fixtures. Include MySQL/MariaDB missing and empty usernames with a nonempty password patch: preserve the username and requested password when representable, otherwise return a fixed `invalid` error; never silently drop the patch. Malformed strings fail safely. Prove the setter does not call resolvers, dialers, environment-derived configuration or file-reading parsers; nonexistent service/certificate paths can remain syntax without being opened by the transformation.
- **Core:** Inspect the sealed old and new payloads with test-owned keys to prove only the intended DSN credential changes; plaintext metadata remains free of both passwords. Verify omission, explicit empty, simultaneous DSN replacement, unsupported SQLite, final database-reference rejection, explicit reference removal, unchanged ordinary conflict rules, test-before-save, skip-test, validation/test failure rollback, concurrent-version conflict, version increment, cache invalidation and new-version pool use. Assert failed edits preserve the old usable configuration.
- **CLI:** Cover omission, true and false flags, first-line and EOF input, empty/whitespace input, ignored later lines, long lines, read errors, and every pair of active stdin readers. Verify conflict rejection reads no bytes. Cover password plus a literal new DSN, database-reference clearing, local behavior and `--server` forwarding. Ensure output and errors never reveal fixture credentials.
- **REST/client:** Exercise nil omission and non-nil empty/nonempty round trips, direct `null` requests, invalid JSON types, strict unknown fields, reference conflicts, skip-test and generic parse failures. GET, list and PATCH responses retain redaction and expose no password property. Keep OpenAPI input semantics and response schemas aligned with the behavior.
- **Regression:** Retain the byte-for-byte MCP surface golden, engine schema goldens and ordinary query/conformance behavior. Run `gofmt`, `go vet ./...` and full root `go test ./...` with Docker before a PR, plus focused race checks for lifecycle tests that introduce concurrency. Use appropriate real-engine integration tests to prove rotated credentials connect and failed test updates do not persist; use `mongo:8.2` for MongoDB. No paid agent/token benchmark is needed while MCP surface and schema output remain unchanged.

Implementation will update CLI/API documentation and `CHANGELOG.md` under `[Unreleased]` with verified behavior. This spec does not claim the feature already exists. Preserve private local review notes outside public artifacts.

## Compatibility and review gate

The REST request addition is additive for existing clients. Existing stored connections and stores need no migration; older clients can continue their existing operations. A new client requesting this feature from an older server receives that server's existing unknown-field rejection. Do not claim feature support across that version mismatch.

The user approved this architectural design and the implementation plan and chose subagent execution. The six implementation tasks use one feature branch; Task 6 and whole-branch independent review remain before opening the feature PR. Keep one branch and one PR for this work. Merge, tagging, release and paid benchmark calls require their separate approvals.
