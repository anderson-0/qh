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

## Running

```bash
make bench                                   # everything; writes bench/results/<timestamp>/report.md
make bench ARGS="-limit 3 -runs 1"           # a quick, cheap smoke run
make bench ARGS="-skip-agent -skip-tokens"   # micro-benchmarks only, no API key needed
```

A full run makes about 540 model conversations: 90 questions × 3 runs × 2 servers. Check your API budget before starting.

`raw.jsonl` gets one line per finished run, so an interrupted run keeps what it measured.

## Fairness

Both servers get the same model, prompts, turn limit (30) and tool-output cap (100,000 characters). Each runs read-only, with its own defaults:
- qh: 100-row cap.
- dbhub: no row cap.

Answers are submitted through a `submit_answer` tool and compared with the reference rows:
- numbers within a relative 1e-6;
- strings trimmed and compared case-insensitively;
- row order ignored unless the question asks for one.
