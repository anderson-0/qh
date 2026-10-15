# Recorded benchmark runs

Each directory holds one `make bench` run: `report.md` is the report, and `raw.jsonl`, when present, has one line per model conversation. Other local runs are not committed.

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
