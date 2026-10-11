# qh benchmark

Compares qh with [bytebase/dbhub](https://github.com/bytebase/dbhub) on Pagila (Postgres), Sakila (MySQL) and Chinook (SQLite).

Spec §9.2 defines the benchmark and §1 its success criteria.

## What it measures

- **Agent runs:** 30 questions per dataset, asked through a Claude model. Each server is the only tool source in its runs. The benchmark records:
  - tokens, tool calls and wall time per task;
  - accuracy against a reference SQL answer.
- **Tool-definition tokens:** measured with 1 to 5 connections registered, using the token-counting endpoint.
- **Micro-benchmarks:**
  - cold schema build on a generated 1,000-table Postgres database;
  - query latency p50/p95 on Pagila.

## Requirements

- Docker.
- Node 22.5 or later, with `npx`, for dbhub.
- `ANTHROPIC_API_KEY`, for agent runs and token counting.
- `ANTHROPIC_WORKSPACE_ID`, only for an API key that is not scoped to a workspace: it is sent as the `anthropic-workspace-id` header.

## Running

```bash
make bench                                   # everything; writes bench/results/<timestamp>/report.md
make bench ARGS="-limit 1 -runs 1"           # first: a cheap smoke run (6 conversations)
make bench ARGS="-limit 3 -runs 1"           # a larger sample
make bench ARGS="-skip-agent -skip-tokens"   # micro-benchmarks only, no API key needed
```

Run the `-limit 1 -runs 1` smoke run before a full run: it checks your API key, the model id and both servers for a few cents.

## Cost and time

A full run makes about 540 model conversations: 90 questions × 3 runs × 2 servers. Every request uses prompt caching, so a conversation re-reads its growing prefix at the cache-read price. Assuming a handful of turns per conversation, a few thousand tokens of tool output per turn and Sonnet 5.5 list prices, expect the order of magnitude:

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
