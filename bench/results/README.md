# Recorded benchmark runs

Each directory holds one `make bench` run: `report.md` is the report, and `raw.jsonl` has one line per model conversation. Other local runs are not committed.

The three runs below measure `main` at e242674 (Plans 1–6 and #9). Each is a full run: 4 datasets × 30 questions × 3 runs × 2 servers, with `claude-sonnet-5-5` at effort high against dbhub 1.5.0.

| Run | Tokens per task vs dbhub | Accuracy (qh vs dbhub) | Tool calls (qh vs dbhub) |
|---|---|---|---|
| [2026-10-11T092602](2026-10-11T092602/report.md) | 40.4% fewer | 100% vs 100% | 1.6 vs 1.6 |
| [2026-10-11T095918](2026-10-11T095918/report.md) | 40.3% fewer | 100% vs 100% | 1.6 vs 1.6 |
| [2026-10-11T100958](2026-10-11T100958/report.md) | 40.4% fewer | 100% vs 100% | 1.6 vs 1.7 |

All three pass every spec §1 criterion, and the token result varies by only 0.1 points between runs.

Only the first run includes the tool-definition and micro-benchmark sections:

- Tool definitions: 393 tokens against dbhub's 579.
- Cold schema build, 1,000 tables: 0.08 s against 2.08 s.
- Query latency p50: 0.5 ms against 0.7 ms.
