# qh vs toolbox benchmark — 2026-10-15

Model `claude-sonnet-5-5` (effort high) · 3 run(s) per question · toolbox 1.14.0 · tool output capped at 100,000 bytes for both servers.

## Success criteria (spec §1)

| Criterion | Target | Result | Pass |
|---|---|---|---|
| Fewer total tokens per task | ≥ 40% fewer than toolbox | not measured | — |
| Accuracy | ≥ toolbox | not measured | — |
| Tool calls per task | fewer than toolbox | not measured | — |
| qh tool definitions | ≤ 800 tokens | not measured | — |
| Cold schema build, 1,000 MySQL tables | < 1 s | 0.02 s | ✅ |

## Results by dataset

| Dataset | Server | Accuracy | Avg total tokens | Avg input | Avg output | Avg tool calls | Avg wall (s) | Runs | Aborted | Errors | Truncated outputs |
|---|---|---|---|---|---|---|---|---|---|---|---|

## Micro-benchmarks

| | qh | toolbox |
|---|---|---|
| Cold schema, 1,000 MySQL tables (median of 3) | 22.6 ms | 29.5 ms |
| Query latency p50 (MySQL) | 2.4 ms | 0.4 ms |
| Query latency p95 (MySQL) | 12.6 ms | 5.5 ms |

## Per question

| Question | Kind | qh correct | toolbox correct | qh avg tokens | toolbox avg tokens |
|---|---|---|---|---|---|

## Notes

- Cold schema: qh's overview lists the first 200 of 1,000 tables with a pattern hint (it builds the full catalogue); Toolbox's list_tables (output_format simple) lists all 1,000 names. Toolbox connects when it starts, so its figure excludes connecting; qh's includes it.
- Cold schema is the first schema call after process start; it includes any database connection the server makes lazily (qh connects on first use).
- The three fresh processes use the same database instance; database caches and statistics can warm between runs. The reported median does not measure a database restart.

Method: tool-definition tokens are `count_tokens(noop tool + server tools, server instructions as the system prompt) − count_tokens(noop tool)`, so they include the instructions the server sends at initialization. Total tokens are input (including cache reads and writes) plus output, summed over every model turn of a task.
