# MySQL cold-schema measurements, 2026-10-15

Workload: MySQL 8.4, 1,000 generated tables, seven columns per table, a primary key, a comment and 999 foreign keys. Host: Apple M5 Max (darwin/arm64), Go 1.27.2, Docker Engine 29.8.0 under Docker Desktop. These runs used no model API.

## Controlled comparison

The baseline binary used `internal/engine/mysql/introspect.go` from `80dfb466`, applied through a Go build overlay. The candidate streams `information_schema.COLUMNS` without SQL ordering, sorts columns by ordinal in Go, and reuses the object map when linking foreign keys. Both binaries otherwise use the same source. Each sample starts a fresh qh MCP process. Old and new alternate order on the same database instance.

| Sample | First process | Old (ms) | New (ms) |
|---|---|---|---|
| 1 | old | 207.951 | 35.570 |
| 2 | new | 34.327 | 27.947 |
| 3 | old | 37.040 | 27.569 |
| 4 | new | 28.304 | 24.589 |
| 5 | old | 29.517 | 22.432 |
| 6 | new | 27.246 | 22.736 |

The nearest-rank median of six samples is 29.517 ms before and 24.589 ms after (about 17% less time). Some individual samples overlap. The first old call also pays the one-time database statistics cost; fresh processes do not reset MySQL caches. This is a small local sample, not a general performance guarantee or a database-restart measurement.

## Profile and output checks

The original column query took about 17–20 ms as the first query on a new connection, including connecting, on a 1,000-table schema plus the conformance fixture. `EXPLAIN ANALYZE` showed a temporary table and sort over 7,019 column rows. The unsorted variant streamed rows without that temporary table. The final assembler links each foreign key through the existing object map instead of scanning the objects again. Table statistics, foreign-key queries and index queries remain unchanged.

Independent old/new comparisons on MySQL 8.0/8.4 and MariaDB 10.11/11.4, each with named and unnamed database connections, found identical catalogs and byte-identical overview and every object's detail output. Fixtures included views with reordered columns, composite keys and indexes, defaults, enums, sets, generated columns, comments and multiple incoming foreign keys. A separate comparison covered case-tied child names, unsorted namespaces and absent targets. Retained unit tests cover ordinal ordering and incoming-reference ordering. The profiling and comparison tests were temporary and are not shipped.

## Final Toolbox comparisons

Both runs used:

```bash
cd bench && go run ./cmd/bench -rival toolbox -micro-engine mysql -skip-agent -skip-tokens
```

| Run | qh cold schema | Toolbox cold schema | qh / Toolbox query p50 |
|---|---|---|---|
| [2026-10-15T125439](2026-10-15T125439/report.md) | 23.7 ms | 21.7 ms | 0.6 ms / 0.5 ms |
| [2026-10-15T125507](2026-10-15T125507/report.md) | 23.3 ms | 19.5 ms | 0.6 ms / 0.5 ms |

Each cold-schema figure is the median of three fresh processes sharing a running database. The first qh sample in each run also pays the initial table-statistics cost; the median reduces that one-time effect. qh builds the full catalog but lists the first 200 table names; Toolbox returns all 1,000 names and connects before timing begins. These runs measure neither model accuracy nor token usage. qh is still slightly slower in these two comparisons, so the roadmap's fastest-rival target remains open. MySQL query execution was not changed by this work.
