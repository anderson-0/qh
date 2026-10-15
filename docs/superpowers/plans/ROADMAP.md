# qh roadmap

Last updated 2026-10-15, after v1.0.3 at `main` 854502f. This file is the plan across features. Each feature still gets its own spec in `docs/superpowers/specs/` and plan in this folder.

**The goal:** beat other database MCP servers on tokens and speed with equal accuracy, measured by `bench/`. Then make qh easy to find and install.

## Done

| Version | What shipped |
|---|---|
| 1.0.0 | Postgres, MySQL/MariaDB, SQLite and MongoDB engines; two MCP tools; server mode (REST, keys, workspaces, query log); SSH tunnels; `env:`/`file:`/`aws-sm://` secret references; a Postgres store shared by several instances (plans 1–9) |
| 1.0.1 | Hardening: store reads survive restarts and failovers, a listener liveness ping, outage logs, SSH key parsing limits, a note for MongoDB databases that could not be listed |
| 1.0.2 | `QH_DSN` single-connection mode for containers; a listing in the official MCP registry; setup docs for eight editors |
| 1.0.3 | A Homebrew cask (`brew install anderson-0/tap/qh`), a 4× faster first schema call on Postgres, a Postgres foreign key no longer duplicated per partition, and less work in MySQL schema discovery |
| Unreleased | A generated SQLite cold-schema benchmark, recorded engine speed checks, less allocation in MongoDB inference and schema text that retains all foreign keys per column |

### Benchmarks so far

All runs: `claude-sonnet-5-5` at effort high, 3 runs per question, equal accuracy (100%) in every comparison.

| Rival | Datasets | Tokens per task |
|---|---|---|
| MCP Toolbox 1.14.0, prebuilt config | Postgres, MySQL, SQLite | 83.6% fewer with qh |
| MCP Toolbox 1.14.0, `data` toolset | Postgres | 76.4% fewer |
| dbhub 1.5.0 | Postgres, MySQL, SQLite | 40.2% fewer (2026-10-15, `main` at `0656d51`; one full run) |
| mongodb-mcp-server 3.0.5 | MongoDB | 95.2% fewer |

**Speed:** the first schema call on 1,000 Postgres tables takes 23 ms, against Toolbox's 22–26 ms and dbhub's 2.10 s. Query latency p50 is about 0.5 ms; Toolbox's is 0.4 ms.

### Distribution

- **Live:** GitHub releases with binaries; the image `ghcr.io/anderson-0/qh`; `go install`; the official MCP registry (`io.github.anderson-0/qh`); the Homebrew tap.
- **Pending:** the Docker MCP catalog, [docker/mcp-registry#5680](https://github.com/docker/mcp-registry/pull/5680), waiting for Docker's review.
- **Listed by others, unclaimed:** mcprush.com (`anderson-0/qh-mcp`), added without being asked. Claiming it is optional.

## Next, in order

2. **Speed on the other engines.** The micro-benchmarks now cover Postgres, MySQL, SQLite and MongoDB.
   - Done: a generated 1,000-table MySQL workload, selected with `-micro-engine mysql`. Streaming columns and using a map for foreign-key links preserve schema output. A controlled comparison measured 29.5 ms before and 24.6 ms after. See the [measurements and caveats](../../../bench/results/mysql-cold-schema-2026-10-15.md). The target below remains open; this is one measured improvement.
   - Done: a follow-up fresh-connection profile on MySQL 8.4 and MariaDB 11.4. `COLUMNS` remains the critical path; isolated sort savings do not establish an overall gain. On unchanged code, two more MCP runs measured qh at 22.4 ms and Toolbox at 21.3/24.2 ms. Initial qh samples were 181.9–215.2 ms; the three-process median hides that first-call cost. See [the profile and caveats](../../../bench/results/mysql-catalog-profile-2026-10-15.md).
   - Done: two free SQLite and MongoDB checks. SQLite took 16.0–24.3 ms versus Toolbox's 32.3–34.4 ms on 1,000 tables. MongoDB took 245.9–248.7 ms versus 33.0–33.5 ms for names only; qh also samples and infers schema. See the [reports and caveats](../../../bench/results/sqlite-mongodb-cold-schema-2026-10-15.md).
   - MongoDB profiling found a per-document presence map in inference. Removing it cuts allocated bytes by 25–26% in the generated samples. Controlled introspection medians were 290.1 → 268.2 ms without references and 379.4 → 357.3 ms with 999 verified references, with matching output. Normal MCP runs overlap the earlier baseline, so they do not establish an overall latency improvement. See [the measurements and limits](../../../bench/results/mongodb-inference-2026-10-15.md). The standard generated benchmark still has no cross-collection reference fields; the controlled probe and new integration test cover them separately.
   - No further engine rewrite is justified by these checks. MySQL lacks a stable fastest-rival result in these small samples; a separate first-ever catalog metric could expose its initial cost. MongoDB still issues three catalog commands per collection and extra reference lookups. More engine work needs a new measured hypothesis that preserves output. SQLite needs no engine rewrite on current evidence. The paid refresh in item 3 below is measured; further paid runs need approval.
   - Acceptance: qh's first schema call is at or below the fastest rival on each engine, with byte-identical output (the golden conformance tests pass).
3. **Done: refresh the dbhub numbers.** The [2026-10-15 full run](../../../bench/results/dbhub-refresh-2026-10-15.md) at `0656d51` measured 40.2% fewer tokens, equal 100% accuracy and 1.5 versus 1.6 tool calls on 360 paired tasks. There were no errors, aborts or truncated outputs. The 40% target passed by only 0.239 percentage points; one full run is not a guaranteed saving. Production code and MCP tool text are unchanged.
4. **Agent-facing correctness:**
   - Fixed: schema overview and detail text retain every single-column foreign key in catalog order. Multiple targets appear as `owner_id→{users.id,archive.users.id}` in an overview and `→ users.id, archive.users.id` in details. Regression tests cover SQL, composite references and folded inferred fields.
   - Fixed: MCP SDK argument-validation and decoding errors use single-line `error[invalid]` tool results. Existing service categories, protocol errors and tool definitions are preserved.
   - Fixed: Postgres explicit cancellation returns `query_error` without timeout advice. Deadlines and recognized statement timeouts retain `timeout`; other `57014` messages, including unrecognized localized messages, retain the server detail as `query_error`. PG13/17 tests cover cancellation, timeouts and pool recovery.
   - Fixed: MySQL/MariaDB explicit and wrapped cancellation uses `query_error`, consistent with Postgres. Server interruption `1317` retains its message as `query_error` unless a context deadline establishes a timeout; codes `3024`/`1969` retain `timeout` even after late cancellation. Bare `context.Canceled` already had the intended category; its message is now consistent.
5. **Robustness:**
   - Implemented on `codex/mysql-write-timeout`, pending final checks/review and PR: MySQL server-side write interruption with observed InnoDB rollback/lock recovery. The [approved spec](../specs/2026-10-15-qh-mysql-write-timeout-design.md) and [plan](2026-10-15-qh-mysql-write-timeout.md) preserve MariaDB/read-only paths and the MCP surface. [Measurements](../../../bench/results/mysql-write-timeout-2026-10-15.md) show higher normal read-write latency and up to twice the pool connections; proxy/advisory-lock/transaction limits are explicit.
   - A panic in the pool opener wedges that connection until restart.
   - The first run fails on filesystems without hard links (`master.key` is published with `os.Link`).
   - `QH_SERVER` is not honoured by the local-only check in the CLI.
   - `conn edit` has no `--password-stdin`.
6. **Security hardening.** There are open server-mode items. Their details are kept out of this public repo, in the maintainer's local handoff notes. Report anything new through `SECURITY.md`.
7. **Repository hygiene** (ask the user):
   - a tag ruleset for `v*`;
   - immutable releases;
   - switch the cask's `postflight` to `postflight_steps` once GoReleaser releases `hooks.post.install_steps` (after 2.18.3).
8. **Optional reach:** Glama (needs the user's sign-in) and then an awesome-mcp-servers PR, whose bot requires a Glama badge. Smithery needs a per-platform MCPB bundle and an account; skipped for now.

About 40 further minor items (test gaps, error-message wording, edge-case input validation) remain from the review rounds of plans 1–5. The `*-deferred-findings.md` files list them, though some of those entries are now fixed.

## Not planned

- **SQL Server and Oracle:** the user ruled these not a priority.
- From the spec's "Not in v1" list: cloud IAM auth, a web UI (the OpenAPI spec is published), multi-statement scripts, write approval workflows, restricting a key to some connections, Prometheus metrics, and importing dbhub TOML.
- **decisionkit** (closed-form decisions for agents): tried on 58 past review findings. It scored 88% on fix-or-defer against a 78% baseline, but rated both critical security bypasses as merely important. Not adopted for reviews.
