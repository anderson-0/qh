# MongoDB inference work, 2026-10-15

Base: `main` at `9c3f564` (v1.0.3 plus the SQLite benchmark). Host: Apple M5 Max, darwin/arm64, Go 1.27.2, Docker Engine 29.8.0 under Docker Desktop, `mongo:8.2`. All checks were free. They measure neither model accuracy nor token use.

## What was measured

A throwaway test timed counting, index listing, document sampling, local inference, reference verification and catalog linking. A driver command monitor counted commands and their elapsed time. Each full introspection used a fresh client with the normal five-connection pool; the connection check was timed separately. Individual count, index and sampling commands were also timed as the first catalog operation after connecting a fresh client. They took less than 1 ms each in these local checks.

The first workload matches the existing micro-benchmark: 1,000 collections with 100 documents each, with scalar, nested and array fields. The second adds a field referencing the previous collection's `_id`, producing 999 verified references. Both use deterministic `find` sampling because each collection has exactly 100 documents. These results do not cover the random `$sample` path used for larger collections.

Counting, index listing and sampling issue 1,000 commands each. The reference workload adds 999 verification queries. There is no single slow first catalog query in this fixture. An allocation profile of inference found roughly a quarter of allocated bytes in the map built for each document to track path presence.

The change replaces that map and its final scan with a document number on each path's statistics. A path counts once per document, including when it appears repeatedly inside an array. Null and undefined values still do not count. Sampling limits, command count, concurrency and reference verification are unchanged.

## Controlled comparison

The complete original inference implementation was copied from the baseline into a throwaway test under renamed symbols. The same process alternated original and candidate implementations against the same running database, opening a fresh client each time. Each row below is one original/candidate pair; times exclude connecting and include timing/command-monitor instrumentation.

| Pair | No references: original / candidate | 999 references: original / candidate |
|---|---|---|
| 1 | 320.0 / 266.5 ms | 380.7 / 357.1 ms |
| 2 | 284.5 / 263.0 ms | 363.9 / 357.3 ms |
| 3 | 290.1 / 269.7 ms | 379.4 / 365.7 ms |
| 4 | 277.8 / 268.2 ms | 386.0 / 359.6 ms |
| 5 | 334.3 / 315.4 ms | 366.4 / 344.1 ms |

Medians: **290.1 → 268.2 ms** without references (7.5% lower), and **379.4 → 357.3 ms** with references (5.8% lower). Five pairs on one host are a small sample, and always running the original first can favor the candidate through cache warming.

The stage medians below sum elapsed time across concurrent collection tasks. They include waits and overlap, so they are not CPU time and must not be added to obtain total wall time. Reference verification and linking run after collection descriptions.

| Stage | No references: original / candidate | 999 references: original / candidate |
|---|---|---|
| count | 403.153 / 397.060 ms | 381.217 / 385.903 ms |
| indexes | 458.510 / 454.706 ms | 426.566 / 445.177 ms |
| sample | 495.268 / 480.356 ms | 474.860 / 483.641 ms |
| infer | 245.861 / 177.401 ms | 248.118 / 176.336 ms |
| references | 0.443 / 0.429 ms | 96.061 / 90.641 ms |
| link | 0.003 / 0.003 ms | 0.828 / 0.799 ms |

A separate inference-only benchmark ran both implementations without profiling, five samples per case. Each operation infers 100 documents. Medians:

| Document shape | Original | Candidate | Original / candidate bytes | Original / candidate allocations |
|---|---|---|---|---|
| No reference field | 145.5 µs | 107.9 µs | 175,745 / 130,145 | 3,387 / 3,087 |
| One reference field | 149.4 µs | 128.6 µs | 180,256 / 134,656 | 3,507 / 3,207 |

The byte reductions are 25.9% and 25.3%. Timing varies with host load. The committed benchmark reproduces the candidate cases:

```bash
go test ./internal/engine/mongodb -run '^$' -bench '^BenchmarkInfer$' -benchmem -count=5
```

## Output checks

The controlled comparison checked complete catalogs, overview bytes and every collection's detail bytes after each original/candidate pair: no differences, with and without the 999 references. Another 400 deterministic randomized BSON fixtures compared all inferred fields, comments, dominant types and reference samples: no differences.

New regression tests cover a 32-collection reference chain with repeated nested fields, mixed int/long IDs, incoming links and enum values. Sparse repeated paths check nullability and the 49-document enum boundary, including null, undefined and missing values. Both tests passed on the original implementation before the production change. Existing MongoDB goldens remain unchanged.

## MCP checks

Command:

```bash
cd bench && go run ./cmd/bench -rival mongodb-mcp -skip-agent -skip-tokens
```

These runs use the unchanged generated workload without references. Query latency uses the Pagila document dataset.

| Run | qh cold schema | mongodb-mcp cold schema | qh / rival query p50 | qh / rival query p95 |
|---|---|---|---|---|
| [2026-10-15T133704](2026-10-15T133704/report.md) | 252.3 ms | 35.6 ms | 0.8 / 1.7 ms | 127.5 / 311.6 ms |
| [2026-10-15T133837](2026-10-15T133837/report.md) | 234.0 ms | 34.0 ms | 1.1 / 1.6 ms | 122.6 / 326.3 ms |

The earlier baseline was 245.9–248.7 ms for qh. These new runs overlap it, so they do not establish an overall MCP latency improvement. The controlled comparison isolates a modest gain, and the inference allocation reduction is reproducible. The roadmap's fastest-rival target remains open.

Each cold-schema number is the median of three fresh server processes sharing one running database. Database caches can warm between samples. qh connects inside the timed call and builds a full catalog before returning a capped overview; the rival connects before the call and returns names only. This is unequal work, and these timings are not database-restart measurements or general guarantees.

## Independent review

A separate reviewer compared the frozen original inference implementation at `9c3f564` with the candidate on 63 edge fixtures and 160 seeded randomized fixtures. Inferred fields, comments, dominant types, reference sample values/order and formatted overview/detail bytes matched. Cases included duplicate BSON keys, literal/nested path collisions, repeated nested arrays, reversed documents, the 49/50/51-document enum threshold, Unicode string lengths and field-count limits. The reviewer found no actionable issues.
