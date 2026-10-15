# qh vs dbhub benchmark — 2026-10-15

Model `claude-sonnet-5-5` (effort high) · 1 run(s) per question · dbhub 1.5.0 · tool output capped at 100,000 bytes for both servers.

## Success criteria (spec §1)

| Criterion | Target | Result | Pass |
|---|---|---|---|
| Fewer total tokens per task | ≥ 40% fewer than dbhub | 53.1% fewer (8 paired runs) | ✅ |
| Accuracy | ≥ dbhub | 100.0% vs 100.0% (8 paired runs) | ✅ |
| Tool calls per task | fewer than dbhub | 1.2 vs 1.8 (8 paired runs) | ✅ |
| qh tool definitions | ≤ 800 tokens | 393 tokens | ✅ |
| Cold schema build, 1,000 Postgres tables | < 1 s | 0.02 s | ✅ |

## Results by dataset

| Dataset | Server | Accuracy | Avg total tokens | Avg input | Avg output | Avg tool calls | Avg wall (s) | Runs | Aborted | Errors | Truncated outputs |
|---|---|---|---|---|---|---|---|---|---|---|---|
| all | qh | 100% | 2526 | 2382 | 144 | 1.2 | 5.3 | 8 | 0 | 0 | 0 |
| all | dbhub | 100% | 5381 | 5181 | 200 | 1.8 | 6.4 | 8 | 0 | 0 | 0 |
| chinook | qh | 100% | 2082 | 1953 | 129 | 1.0 | 2.4 | 2 | 0 | 0 | 0 |
| chinook | dbhub | 100% | 3749 | 3554 | 194 | 2.0 | 14.3 | 2 | 0 | 0 | 0 |
| lendco | qh | 100% | 3866 | 3679 | 186 | 2.0 | 5.4 | 2 | 0 | 0 | 0 |
| lendco | dbhub | 100% | 12722 | 12386 | 335 | 3.0 | 5.3 | 2 | 0 | 0 | 0 |
| pagila | qh | 100% | 2084 | 1952 | 132 | 1.0 | 10.4 | 2 | 0 | 0 | 0 |
| pagila | dbhub | 100% | 2532 | 2397 | 134 | 1.0 | 3.2 | 2 | 0 | 0 | 0 |
| sakila | qh | 100% | 2072 | 1944 | 127 | 1.0 | 3.2 | 2 | 0 | 0 | 0 |
| sakila | dbhub | 100% | 2523 | 2386 | 136 | 1.0 | 2.6 | 2 | 0 | 0 | 0 |

## Tool-definition tokens by number of connections

| Connections | qh | dbhub |
|---|---|---|
| 1 | 393 | 579 |
| 2 | 430 | 1206 |
| 3 | 469 | 1809 |
| 4 | 508 | 2412 |
| 5 | 547 | 3015 |

## Micro-benchmarks

| | qh | dbhub |
|---|---|---|
| Cold schema, 1,000 Postgres tables (median of 3) | 23.9 ms | 2.54 s |
| Query latency p50 (Postgres) | 1.6 ms | 1.9 ms |
| Query latency p95 (Postgres) | 5.9 ms | 4.8 ms |

## Per question

| Question | Kind | qh correct | dbhub correct | qh avg tokens | dbhub avg tokens |
|---|---|---|---|---|---|
| chinook-01 | lookup | 1/1 | 1/1 | 2022 | 4863 |
| chinook-02 | lookup | 1/1 | 1/1 | 2142 | 2635 |
| lendco-01 | lookup | 1/1 | 1/1 | 4260 | 11683 |
| lendco-02 | lookup | 1/1 | 1/1 | 3471 | 13760 |
| pagila-01 | lookup | 1/1 | 1/1 | 2011 | 2456 |
| pagila-02 | lookup | 1/1 | 1/1 | 2156 | 2607 |
| sakila-01 | lookup | 1/1 | 1/1 | 2014 | 2473 |
| sakila-02 | lookup | 1/1 | 1/1 | 2129 | 2573 |

## Notes

- Cold schema: qh's overview lists the first 200 of 1,000 tables with a pattern hint (it builds the full catalogue); dbhub's search_objects (summary, limit 1000) lists all 1,000.
- Cold schema is the first schema call after process start; it includes any database connection the server makes lazily (qh connects on first use).
- The three fresh processes use the same database instance; database caches and statistics can warm between runs. The reported median does not measure a database restart.

Method: tool-definition tokens are `count_tokens(noop tool + server tools, server instructions as the system prompt) − count_tokens(noop tool)`, so they include the instructions the server sends at initialization. Total tokens are input (including cache reads and writes) plus output, summed over every model turn of a task.
