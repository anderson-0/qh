# qh vs mongodb-mcp benchmark — 2026-10-15

Model `claude-sonnet-5-5` (effort high) · 3 run(s) per question · mongodb-mcp 3.0.5 · tool output capped at 100,000 bytes for both servers.

## Success criteria (spec §1)

| Criterion | Target | Result | Pass |
|---|---|---|---|
| Fewer total tokens per task | ≥ 40% fewer than mongodb-mcp | not measured | — |
| Accuracy | ≥ mongodb-mcp | not measured | — |
| Tool calls per task | fewer than mongodb-mcp | not measured | — |
| qh tool definitions | ≤ 800 tokens | not measured | — |
| Cold schema build, 1,000 collections | < 5 s | 0.23 s | ✅ |

## Results by dataset

| Dataset | Server | Accuracy | Avg total tokens | Avg input | Avg output | Avg tool calls | Avg wall (s) | Runs | Aborted | Errors | Truncated outputs |
|---|---|---|---|---|---|---|---|---|---|---|---|

## Micro-benchmarks

| | qh | mongodb-mcp |
|---|---|---|
| Cold schema, 1,000 collections (median of 3) | 234.0 ms | 34.0 ms |
| Query latency p50 | 1.1 ms | 1.6 ms |
| Query latency p95 | 122.6 ms | 326.3 ms |

## Per question

| Question | Kind | qh correct | mongodb-mcp correct | qh avg tokens | mongodb-mcp avg tokens |
|---|---|---|---|---|---|

## Notes

- Cold schema: qh samples 100 documents from each of 1,000 collections and infers fields and references; mongodb-mcp-server's list-collections returns names only. mongodb-mcp-server connects when it starts, so its figure excludes connecting; qh's includes it.
- Cold schema is the first schema call after process start; it includes any database connection the server makes lazily (qh connects on first use).
- The three fresh processes use the same database instance; database caches and statistics can warm between runs. The reported median does not measure a database restart.

Method: tool-definition tokens are `count_tokens(noop tool + server tools, server instructions as the system prompt) − count_tokens(noop tool)`, so they include the instructions the server sends at initialization. Total tokens are input (including cache reads and writes) plus output, summed over every model turn of a task.
