# dbhub refresh, 2026-10-15

This run measures qh at `0656d5181c236be1ae11e13212950c6a0f8e38f7` on `main`, after v1.0.3 and the MongoDB inference change. Production code, benchmark code and MCP tool text were unchanged during the run. Host: Apple M5 Max, darwin/arm64, Go 1.27.2, Docker Engine 29.8.0 under Docker Desktop.

## Method

The [smoke run](2026-10-15T140415/report.md) used two questions per dataset and one repetition: 16 conversations, all correct, with no errors or aborts. Its 53.1% token result is not the full comparison.

The [full run](2026-10-15T140716/report.md) used the defaults: `claude-sonnet-5-5`, effort `high`, dbhub 1.5.0, four concurrent conversations, 30 questions per dataset and three repetitions. Pagila, Sakila, Chinook and Lendco give 720 conversations, or 360 paired task runs. Both servers use the same prompts, model and 100,000-byte tool-output cap. qh caps query rows at 100; dbhub has no row cap. The per-conversation token budget is 1,000,000. Datasets and servers run sequentially, with qh first per dataset; the four-way parallelism applies within each server batch.

Commands, after loading the repository credentials without printing them:

```bash
make bench ARGS="-rival dbhub -limit 2 -runs 1"
make bench ARGS="-rival dbhub"
```

## Full result

| Measure | qh | dbhub |
|---|---|---|
| Mean tokens per task | 3,090.35 | 5,171.18 |
| Accuracy | 360/360 | 360/360 |
| Total tool calls | 546 | 572 |
| Mean tool calls per task | 1.517 | 1.589 |
| Mean task wall time | 4.032 s | 3.982 s |
| Tool-definition tokens, one connection | 393 | 579 |
| Tool-definition tokens, five connections | 547 | 3,015 |
| Cold schema, 1,000 Postgres tables | 22.9 ms | 2.10 s |
| Postgres query p50 / p95 | 0.5 / 2.7 ms | 0.8 / 2.7 ms |

The token saving is **40.239%**, reported as **40.2%**. Both servers answered every task correctly, with zero errors, aborts or truncated outputs. All expected dataset/question/repetition identities appear once per server; no paired tasks were excluded. Total input plus output tokens were 1,112,525 for qh and 1,861,626 for dbhub.

This refresh passes the benchmark's 40% token target, but its margin is only 0.239 percentage points. The three earlier runs at `e242674` measured 40.3–40.4%. One new full run does not establish a guaranteed saving or attribute the difference from those runs to a code change. qh's average task wall time was slightly higher here; the run does not show an overall task-latency advantage.

Cold-schema timing is the median of three fresh processes against one running Postgres instance. Database caches can warm. qh builds a complete catalog and returns a capped overview of 200 objects; dbhub returns summaries for all 1,000. These are first schema calls, not database-restart timings. Query latency uses Pagila.

## Usage and elapsed time

The full run recorded 2,813,800 input tokens, including cache reads/writes, and 160,351 output tokens. At the [published Sonnet 5.5 rates](https://platform.claude.com/docs/en/about-claude/pricing), treating all recorded input as five-minute cache writes ($2.50 per million) and output at $10 per million gives **$8.64**. Including the smoke conversations gives **$8.82**. Cache reads cost less, so this deliberately overprices them.

These are estimates from recorded conversation usage, not an invoice. The harness merges cache token categories and does not retain the small preflight request's usage. Taxes, discounts and account-specific billing are not captured. The full run took about 13 minutes, including setup, token counting and micro-benchmarks. These observations replace the older broad cost/time estimate for this particular comparison; other workloads can differ.
