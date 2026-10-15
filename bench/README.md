# qh benchmark

Compares qh with a rival MCP database server:
- [bytebase/dbhub](https://github.com/bytebase/dbhub) or Google's [MCP Toolbox for Databases](https://github.com/googleapis/mcp-toolbox) on Pagila (Postgres), Sakila (MySQL), Chinook (SQLite) and Lendco (Postgres);
- MongoDB's official [mongodb-mcp-server](https://github.com/mongodb-js/mongodb-mcp-server) on Pagila and Lendco modelled as documents.

Models know the first three sample databases by heart, so they can answer without looking at the schema. Lendco is Pagila's data rebuilt under unfamiliar table and column names (`harness/lendco.sql`), so its questions measure schema discovery too. Its 30 questions mirror Pagila's, and a test checks that each has the same answer as its Pagila twin.

Spec §9.2 defines the benchmark and §1 its success criteria.

## What it measures

- **Agent runs:** 30 questions per dataset, asked through a Claude model. Each server is the only tool source in its runs. The benchmark records:
  - tokens, tool calls and wall time per task;
  - accuracy against a reference SQL answer.
- **Tool-definition tokens:** measured with 1 to 5 connections registered, using the token-counting endpoint.
- **Micro-benchmarks:**
  - cold schema build on a generated 1,000-table Postgres database, or MySQL/SQLite with `-micro-engine mysql`/`sqlite`;
  - query latency p50/p95 on Pagila (Postgres), Sakila (MySQL) or Chinook (SQLite).

## Requirements

- Docker for Postgres, MySQL and MongoDB. SQLite-only micro-benchmarks do not need it.
- Node 22.5 or later, with `npx`, for dbhub, mongodb-mcp-server and MCP Toolbox.
- `ANTHROPIC_API_KEY`, for agent runs and token counting.
- `ANTHROPIC_WORKSPACE_ID`, only for an API key that is not scoped to a workspace: it is sent as the `anthropic-workspace-id` header.

## Running

```bash
make bench                                   # everything; writes bench/results/<timestamp>/report.md
make bench ARGS="-limit 1 -runs 1"           # first: a cheap smoke run (8 conversations)
make bench ARGS="-limit 3 -runs 1"           # a larger sample
make bench ARGS="-skip-agent -skip-tokens"   # micro-benchmarks only, no API key needed
make bench ARGS="-rival toolbox -micro-engine mysql -skip-agent -skip-tokens" # MySQL only, free
make bench ARGS="-rival toolbox -micro-engine sqlite -skip-agent -skip-tokens" # SQLite only, free
```

`-micro-engine` selects only the micro-benchmarks; `-datasets` still selects the agent workloads. SQL rivals default to Postgres. The MongoDB rival defaults to MongoDB and accepts no SQL micro-engine. Cold schema is the median of three fresh server processes against one database instance, so database caches can warm between samples. It measures the first schema call, not a database restart.

MySQL and SQLite use 1,000 generated tables with seven columns, a primary key and a foreign key to the previous table. MySQL also has table comments. SQLite has no catalog metadata for them, and its `INTEGER PRIMARY KEY` aliases the rowid instead of creating a separate primary-key index. Compare servers on the same engine; the catalog work differs between engines.

Run the `-limit 1 -runs 1` smoke run before a full run: it checks your API key, the model id and both servers for a few cents.

## MongoDB

```bash
make bench ARGS="-rival mongodb-mcp -limit 1 -runs 1"   # smoke run first
make bench ARGS="-rival mongodb-mcp"                    # pagila-docs and lendco-docs, 3 runs
```

- **Rival:** MongoDB's official [`mongodb-mcp-server`](https://github.com/mongodb-js/mongodb-mcp-server), pinned to 3.0.5 and run with `--readOnly` and telemetry off.
- **Datasets:** `pagila-docs` and `lendco-docs` are Pagila and Lendco modelled as documents.
  - Addresses are embedded, films embed their actors and categories, and rentals and payments reference other collections by key.
  - They reuse the 60 SQL questions. Every question has an aggregate twin that a test checks against the SQL reference answer.
- **Fairness:**
  - Neither server gets a dataset description, because mongodb-mcp-server has no place for one.
  - Both servers cap results at 100 rows or documents by default.
  - mongodb-mcp-server serves one connection per process, so its tool-definition tokens are measured with one connection.
  - mongodb-mcp-server's `find` returns 10 documents unless the model passes a limit; its cap is 100.
  - mongodb-mcp-server wraps its results in an `<untrusted-user-data>` notice. That is its real behaviour and the benchmark keeps it, so it costs that server tokens.
- **Cost:** about 360 conversations (60 questions × 3 runs × 2 servers), roughly half a SQL run.

## MCP Toolbox for Databases

```bash
make bench ARGS="-rival toolbox -limit 1 -runs 1"   # smoke run first
make bench ARGS="-rival toolbox"                    # the four SQL datasets, 3 runs
make bench ARGS="-rival toolbox-data"               # Pagila and Lendco with Toolbox's data toolset
```

- **Rival:** Google's MCP Toolbox for Databases, pinned to 1.14.0 (`@toolbox-sdk/server` through `npx`), with its version check off.
- **Configuration:** the prebuilt configuration for each dataset's engine, the setup Toolbox's documentation gives (`--prebuilt=postgres` and so on). Its tools depend on the engine:

  | Engine | `toolbox` | `toolbox-data` |
  |---|---|---|
  | Postgres (Pagila, Lendco) | 29 tools | 8 (the `data` toolset) |
  | MySQL (Sakila) | 9 | 4 |
  | SQLite (Chinook) | 2 | 2 (no toolsets) |

  `toolbox` is what a user gets by following the documentation. `toolbox-data` is Toolbox's own way to trim its Postgres tools to querying and the catalogue, so the comparison does not rest on Toolbox's monitoring tools alone. It runs on Pagila and Lendco by default.
- **Fairness:**
  - Toolbox has no read-only mode: its `execute_sql` can write. The benchmark's databases are throwaway containers and a temporary SQLite copy, so this changes nothing measured.
  - Toolbox has no place for a dataset description, and sends no server instructions. qh gets the description, as against dbhub.
  - Toolbox caps no rows; qh caps at 100. Both get the same 100,000-byte tool-output cap.
  - A prebuilt configuration holds one database per process, so Toolbox's tool-definition tokens are measured with one Postgres connection.
  - Toolbox connects when it starts, so its cold-schema figure excludes connecting; qh's includes it. Its schema call is `list_tables` with names only.
- **Results (2026-10-14, `claude-sonnet-5-5` at effort high, 3 runs per question):**

  | | qh | `toolbox` | qh | `toolbox-data` |
  |---|---|---|---|---|
  | Datasets | all four | all four | Pagila, Lendco | Pagila, Lendco |
  | Tokens per task | 3,118 | 19,067 | 3,653 | 15,463 |
  | qh's saving | 83.6% | | 76.4% | |
  | Accuracy | 100% | 100% | 100% | 100% |
  | Tool calls per task | 1.6 | 2.2 | 1.9 | 2.2 |
  | Tool definitions, one Postgres connection | 392 | 6,663 | 392 | 2,054 |

  - The micro-benchmarks in that run favour Toolbox. Its first schema call on 1,000 tables took 28 ms against qh's 99 ms, and query latency p50 was 0.4 ms against 0.6 ms.
  - qh's first schema call was then made faster: re-measured with `-skip-agent -skip-tokens`, it takes 23 ms against Toolbox's 22–26 ms. qh's figure includes connecting and a catalogue with keys and foreign keys; Toolbox lists names only and is already connected.
  - Each task was still quicker end to end with qh: 4.0 s against 4.8 s on average.
  - Lendco, the dataset with unfamiliar names, shows the largest gap. qh used 4,977 tokens per task, `toolbox` 35,604 and `toolbox-data` 22,287.

## Cost and time

A full run makes about 720 model conversations: 120 questions × 3 runs × 2 servers. Every request uses prompt caching, so a conversation re-reads its growing prefix at the cache-read price. Assuming a handful of turns per conversation, a few thousand tokens of tool output per turn and Sonnet 5.5 list prices, expect the order of magnitude:

- **Cost:** tens to low hundreds of dollars for the whole run. dbhub conversations cost more than qh ones, because its tool definitions and its uncapped row output are larger.
- **Time:** 1.5 to 3 hours at `-parallel 4` (the default), plus a few minutes for the datasets and the micro-benchmarks.

These are estimates, not quotes; check your API budget before a full run. Two flags bound the spend:

- `-token-budget` (default 1,000,000) stops one conversation once its input plus output tokens, cache reads included, pass the ceiling. The run is recorded as failed ("token budget exceeded"), not aborted, so it counts against its server.
- `-effort` (default `high`) is sent as `output_config.effort` on every request. `low` or `medium` cost less but change what is measured.

`raw.jsonl` gets one line per finished run, so an interrupted run keeps what it measured.

## Fairness

Both servers get the same model, prompts, turn limit (30) and tool-output cap (100,000 bytes). Each runs read-only, with its own defaults:
- qh: 100-row cap.
- dbhub: no row cap.

Answers are submitted through a `submit_answer` tool and compared with the reference rows:
- numbers within a relative 1e-6;
- strings trimmed and compared case-insensitively;
- row order ignored unless the question asks for one.

The comparison tolerates a relative 1e-6 on numbers: `|a − b| ≤ 1e-6 × max(1, |a|, |b|)`. For totals near 67,000 that is about 0.067, so an answer one cent off is accepted.

## Aborted and failed runs

- **Aborted** runs did not really happen, so they are left out of every figure: the server failed to start, or the model API failed in a transient way (429, 5xx or 529 after the SDK's retries, a network error).
- **Failed** runs count against their server, as wrong answers with an error: the model gave no answer, hit the turn limit, the token budget, the 10-minute run timeout or `max_tokens`, refused, or the API rejected the request (for example 400 "prompt is too long").

Interrupting a benchmark (Ctrl-C) records nothing for the runs still in flight: unfinished runs are dropped, not aborted.

The success criteria on tokens, accuracy and tool calls use only the (dataset, question, run) triples that both servers completed, and the report says how many that is. A server that lost a run to an outage therefore never flatters or penalises the other. The by-dataset table still covers every non-aborted run of each server.

## What this does not cover

Spec §1's criterion that a new connection is queryable without a restart is covered by qh's own integration tests, not by this benchmark.
