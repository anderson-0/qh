# Recorded benchmark runs

Each directory holds one `make bench` run: `report.md` is the report, and `raw.jsonl`, when present, has one line per model conversation. Other local runs are not committed.

## Current dbhub comparison

The [2026-10-15 refresh](dbhub-refresh-2026-10-15.md) measures `main` at `0656d51`: 720 conversations, all correct, with no errors or aborts. qh used **40.2% fewer tokens per task**, with 1.5 tool calls versus dbhub's 1.6. The margin over the 40% target is small; this is one full run. Task wall time was 4.032 s for qh versus 3.982 s for dbhub.

| Run | Tokens per task vs dbhub | Accuracy (qh vs dbhub) | Paired runs |
|---|---|---|---|
| [2026-10-15T140716](2026-10-15T140716/report.md) | 40.2% fewer | 100% vs 100% | 360 |

The [two-question smoke run](2026-10-15T140415/report.md) is recorded separately and is not used for the full-run claim. The refresh note states settings, raw totals, timing differences and usage estimates.

## Earlier dbhub measurements

The three runs below measure `main` at e242674 (Plans 1–6 and #9). Each is a full run: 4 datasets × 30 questions × 3 runs × 2 servers, with `claude-sonnet-5-5` at effort high against dbhub 1.5.0.

| Run | Tokens per task vs dbhub | Accuracy (qh vs dbhub) | Tool calls (qh vs dbhub) |
|---|---|---|---|
| [2026-10-11T092602](2026-10-11T092602/report.md) | 40.4% fewer | 100% vs 100% | 1.6 vs 1.6 |
| [2026-10-11T095918](2026-10-11T095918/report.md) | 40.3% fewer | 100% vs 100% | 1.6 vs 1.6 |
| [2026-10-11T100958](2026-10-11T100958/report.md) | 40.4% fewer | 100% vs 100% | 1.6 vs 1.7 |

All three pass every spec §1 criterion, and the token result varies by only 0.1 points between runs.

Only the first run includes the tool-definition and micro-benchmark sections.

- Tool definitions: 393 tokens against dbhub's 579.
- Cold schema build, 1,000 tables: 0.08 s against 2.08 s.
- Query latency p50: 0.5 ms against 0.7 ms.

## Against MCP Toolbox for Databases

Both runs measure `main` after v1.0.2, with `claude-sonnet-5-5` at effort high against Toolbox 1.14.0, 3 runs per question.

| Run | Rival | Datasets | Tokens per task | Accuracy (qh vs rival) | Tool calls (qh vs rival) |
|---|---|---|---|---|---|
| [2026-10-14T150816](2026-10-14T150816/report.md) | `toolbox` (prebuilt, all tools) | all four | 83.6% fewer | 100% vs 100% | 1.6 vs 2.2 |
| [2026-10-14T152230](2026-10-14T152230/report.md) | `toolbox-data` (Postgres `data` toolset) | Pagila, Lendco | 76.4% fewer | 100% vs 100% | 1.9 vs 2.2 |

The first run includes the micro-benchmarks. Toolbox's first schema call on 1,000 tables took 28 ms and qh's 99 ms. Toolbox lists table names only and connects when it starts, so its figure leaves out connecting; qh builds the full catalogue and connects first.

## MySQL cold schema

The [2026-10-15 measurements](mysql-cold-schema-2026-10-15.md) compare old and new qh binaries on a generated 1,000-table MySQL schema, then run the free benchmark against Toolbox twice. The controlled comparison measured 29.5 ms before and 24.6 ms after. These are fresh-process measurements against a running database, not database-restart timings. qh still trails Toolbox slightly; the report states the output and connection-timing differences. No model calls were made.

## SQLite and MongoDB cold schema

The [2026-10-15 checks](sqlite-mongodb-cold-schema-2026-10-15.md) record two free runs per engine. SQLite takes 16.0–24.3 ms with qh versus Toolbox's 32.3–34.4 ms on 1,000 tables. MongoDB takes 245.9–248.7 ms with qh versus 33.0–33.5 ms for names-only discovery; qh also samples documents and infers schema. The note links all four reports and explains the differences in work and cache state. No model calls were made.

## MongoDB inference allocations

The [2026-10-15 profiling report](mongodb-inference-2026-10-15.md) separates MongoDB catalog commands from local inference and includes a workload with 999 verified references. Counting path presence directly cuts inference allocation bytes by 25–26% in the generated samples. Controlled introspection medians improve by 5.8–7.5%; the two normal MCP runs overlap the earlier baseline, so they do not establish an overall latency win. Output comparisons and an independent review found no differences. No model calls were made.

## MySQL catalog follow-up

The [fresh-connection profile](mysql-catalog-profile-2026-10-15.md) times every catalog query on MySQL 8.4 and MariaDB 11.4. Columns remain the critical path; isolated sort savings do not establish an overall gain. Two unchanged-code MCP runs measured qh at 22.4 ms and Toolbox at 21.3/24.2 ms, with connection/output differences stated. The initial MySQL catalog call was much slower than the subsequent fresh-process calls. Production code is unchanged, and no model calls were made.
