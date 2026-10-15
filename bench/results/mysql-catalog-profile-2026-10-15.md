# MySQL catalog follow-up, 2026-10-15

Base: `main` at `20b4f3e`, after the v1.0.3 MySQL work and MongoDB PR #21. Production code is unchanged. Host: Apple M5 Max, darwin/arm64, Go 1.27.2, Docker Engine 29.8.0 under Docker Desktop. All checks were free; model accuracy and token usage were not measured.

## Method

A throwaway test in the MySQL package generated 1,000 tables with seven columns, comments and 999 foreign keys, alongside the existing five-object conformance fixture. Totals: 1,005 objects, 7,019 column rows, 1,001 foreign-key rows and 2,006 index rows. It ran on `mysql:8.4` and `mariadb:11.4`.

An overlay timed the four existing catalog queries and Go assembly. Five full introspections each opened a fresh engine with the normal five-connection pool. `Engine.Open` and `Introspect` were timed separately. The first introspection on each database is shown separately below because later fresh clients share the running database's caches.

Each catalog query was then issued as the first query on a fresh SQL connection, five times. Those isolated query timings include opening the network connection and reading all rows. They follow the full introspections, so the server is already warm. Ordered and unordered variants alternate their order each pair. The unordered probes only remove `ORDER BY`; they do not reconstruct a catalog and are not candidates for shipping as written. `COLUMNS` is already unordered, so its second probe repeats the same SQL.

## Full introspection

Times in milliseconds. The initial `Engine.Open` call is excluded from `Introspect`; additional pooled connections can still open during its concurrent catalog queries:

| Run | MySQL open | MySQL introspect | MariaDB open | MariaDB introspect |
|---|---|---|---|---|
| 0: initial database | 1.520 | 175.980 | 1.463 | 25.884 |
| 1 | 1.796 | 18.404 | 1.341 | 11.165 |
| 2 | 1.432 | 18.232 | 1.243 | 12.181 |
| 3 | 1.386 | 17.743 | 1.296 | 11.781 |
| 4 | 1.313 | 17.538 | 1.162 | 11.651 |

The initial MySQL `TABLES` query took 172.2 ms; subsequent calls took 4.7–5.3 ms. The three-process MCP median below reduces this first-call effect, but does not describe a cold database restart.

Median stages from the four subsequent full introspections:

| Stage | MySQL | MariaDB |
|---|---|---|
| TABLES | 4.823 ms | 6.569 ms |
| COLUMNS | 15.827 ms | 9.900 ms |
| KEY_COLUMN_USAGE | 7.061 ms | 6.098 ms |
| STATISTICS | 9.235 ms | 4.052 ms |
| assemble | 2.132 ms | 1.614 ms |

The catalog queries run concurrently, so their times must not be added. Assembly follows them. `COLUMNS` is the critical path in these subsequent calls.

## First query on fresh connections

Medians of five isolated calls; connection establishment is included:

| Query | MySQL current / no SQL ordering | MariaDB current / no SQL ordering |
|---|---|---|
| TABLES | 3.687 / 3.182 ms | 5.466 / 4.976 ms |
| COLUMNS | 13.637 / 13.694 ms | 9.098 / 9.002 ms |
| KEY_COLUMN_USAGE | 5.219 / 4.338 ms | 4.620 / 4.374 ms |
| STATISTICS | 7.933 / 6.902 ms | 4.150 / 3.910 ms |

MySQL `EXPLAIN ANALYZE` confirms sorts in the current `TABLES`, `KEY_COLUMN_USAGE` and `STATISTICS` queries. Their isolated reductions are about 0.5–1.0 ms each; this does not demonstrate an end-to-end improvement because all finish before `COLUMNS` in the full profile.

The existing column query streams without a sort. Its plan still visits 1,005 tables and checks `can_access_column` and object visibility per column, followed by collation and character-set lookups for 7,019 column rows. Those checks are inside MySQL's `information_schema.COLUMNS` view. This probe found no supported replacement that preserves visibility and the returned metadata. It does not claim every possible rewrite has been ruled out.

## Free MCP checks

Command, on unchanged production code:

```bash
cd bench && go run ./cmd/bench -rival toolbox -micro-engine mysql -skip-agent -skip-tokens
```

| Run | qh cold schema | Toolbox cold schema | qh / Toolbox query p50 | qh / Toolbox query p95 |
|---|---|---|---|---|
| [2026-10-15T134947](2026-10-15T134947/report.md) | 22.4 ms | 21.3 ms | 0.6 / 0.4 ms | 6.6 / 5.9 ms |
| [2026-10-15T135037](2026-10-15T135037/report.md) | 22.4 ms | 24.2 ms | 0.6 / 0.4 ms | 6.5 / 5.8 ms |

Each cold-schema result is the median of three fresh processes against one running database. qh's first samples were 215.2 and 181.9 ms; the remaining samples were 21.1–22.4 ms. These runs do not reset database caches between samples. Query latency uses Sakila, not the generated tables.

qh builds the full catalog, connects inside the timed call and returns a capped overview of 200 objects. Toolbox returns all 1,000 names and connects before timing begins. On the profiling fixture, qh's initial connection check alone took 1.3–1.8 ms after the initial call. That is the same order as the MCP timing differences, but the separate measurements do not attribute the full difference to connecting.

## Recommendation

Keep the current engine implementation. qh is behind in one MCP run and ahead in the other, so these small samples do not establish a stable fastest-rival result. Query p50 remains higher than Toolbox's. Removing sorts from non-critical queries has no demonstrated end-to-end gain, and the column query already streams.

The evidence supports recording the profile rather than shipping another SQL rewrite. More engine work would need a new measured hypothesis; the first-ever `TABLES` call is a separate cost hidden by the three-process median. Roadmap item 2's acceptance target remains open. Refreshing the dbhub comparison is the next ordered item and still needs approval for paid model calls.
