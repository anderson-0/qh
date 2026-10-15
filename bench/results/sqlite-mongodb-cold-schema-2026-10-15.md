# SQLite and MongoDB cold-schema checks, 2026-10-15

These free micro-benchmarks start from qh v1.0.3 (`854502f`). The SQLite benchmark adds a generated workload and `-micro-engine sqlite`; the engines and MCP tool text are unchanged. Host: Apple M5 Max (darwin/arm64), Go 1.27.2, Docker Engine 29.8.0 under Docker Desktop for MongoDB. No model API was used, so these runs measure neither model accuracy nor token usage.

## SQLite

Command:

```bash
cd bench && go run ./cmd/bench -rival toolbox -micro-engine sqlite -skip-agent -skip-tokens
```

The workload is a temporary file with 1,000 tables, seven columns per table and 999 foreign keys. Each primary key is SQLite's `INTEGER PRIMARY KEY` rowid alias. SQLite has no table-comment metadata. Both servers use the same file; this is not the same catalog work as Postgres or MySQL.

| Run | qh cold schema | Toolbox cold schema | qh / Toolbox query p50 |
|---|---|---|---|
| [2026-10-15T131512](2026-10-15T131512/report.md) | 16.0 ms | 34.4 ms | 0.8 ms / 0.4 ms |
| [2026-10-15T131521](2026-10-15T131521/report.md) | 24.3 ms | 32.3 ms | 0.6 ms / 0.4 ms |

qh’s first schema call is quicker in both runs; its query p50 is higher than Toolbox’s. These measurements do not justify an engine rewrite. Query latency uses Chinook, not the generated empty tables.

## MongoDB

Command:

```bash
cd bench && go run ./cmd/bench -rival mongodb-mcp -skip-agent -skip-tokens
```

The existing workload has 1,000 collections with 100 documents each, using `mongo:8.2`. qh samples documents and builds fields, indexes and inferred references; mongodb-mcp-server returns collection names only. That difference in work prevents treating the time ratio as a comparison of equivalent schema discovery. This generated workload has no cross-collection reference fields.

| Run | qh cold schema | mongodb-mcp cold schema | qh / mongodb-mcp query p50 |
|---|---|---|---|
| [2026-10-15T130655](2026-10-15T130655/report.md) | 245.9 ms | 33.0 ms | 0.8 ms / 1.4 ms |
| [2026-10-15T130812](2026-10-15T130812/report.md) | 248.7 ms | 33.5 ms | 0.9 ms / 1.4 ms |

qh is well below the benchmark's five-second MongoDB threshold, but names-only discovery is still quicker. MongoDB query latency uses the Pagila document dataset. Future optimization should profile counting, index listing, sampling and inference separately while checking the same output.

## Timing limits

Every cold-schema number is the nearest-rank median of three fresh server processes against one running database or file. Database and filesystem caches can warm between samples. Toolbox and mongodb-mcp-server connect before the timed call; qh connects on first use, inside it. qh builds the full catalog but caps its overview at 200 objects; rivals list all 1,000 names. These small local samples are not database-restart timings or general performance guarantees.
