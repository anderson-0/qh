# qh vs mongodb-mcp benchmark — 2026-10-13

Model `claude-sonnet-5-5` (effort high) · 3 run(s) per question · mongodb-mcp 3.0.5 · tool output capped at 100,000 bytes for both servers.

## Success criteria (spec §1)

| Criterion | Target | Result | Pass |
|---|---|---|---|
| Fewer total tokens per task | ≥ 40% fewer than mongodb-mcp | 95.2% fewer (180 paired runs) | ✅ |
| Accuracy | ≥ mongodb-mcp | 100.0% vs 100.0% (180 paired runs) | ✅ |
| Tool calls per task | fewer than mongodb-mcp | 2.6 vs 4.9 (180 paired runs) | ✅ |
| qh tool definitions | ≤ 800 tokens | 406 tokens | ✅ |
| Cold schema build, 1,000 collections | < 5 s | 0.22 s | ✅ |

## Results by dataset

| Dataset | Server | Accuracy | Avg total tokens | Avg input | Avg output | Avg tool calls | Avg wall (s) | Runs | Aborted | Errors | Truncated outputs |
|---|---|---|---|---|---|---|---|---|---|---|---|
| all | qh | 100% | 5026 | 4765 | 261 | 2.6 | 5.7 | 180 | 0 | 0 | 0 |
| all | mongodb-mcp | 100% | 103636 | 103004 | 632 | 4.9 | 10.2 | 180 | 0 | 0 | 0 |
| lendco-docs | qh | 100% | 5192 | 4922 | 270 | 2.7 | 5.9 | 90 | 0 | 0 | 0 |
| lendco-docs | mongodb-mcp | 100% | 107108 | 106457 | 651 | 5.1 | 10.4 | 90 | 0 | 0 | 0 |
| pagila-docs | qh | 100% | 4859 | 4608 | 251 | 2.6 | 5.5 | 90 | 0 | 0 | 0 |
| pagila-docs | mongodb-mcp | 100% | 100164 | 99551 | 613 | 4.8 | 10.0 | 90 | 0 | 0 | 0 |

## Tool-definition tokens by number of connections

| Connections | qh | mongodb-mcp |
|---|---|---|
| 1 | 406 | 17757 |
| 2 | 422 | — |
| 3 | 439 | — |
| 4 | 456 | — |
| 5 | 473 | — |

## Micro-benchmarks

| | qh | mongodb-mcp |
|---|---|---|
| Cold schema, 1,000 collections (median of 3) | 220.0 ms | 31.8 ms |
| Query latency p50 | 0.9 ms | 1.6 ms |
| Query latency p95 | 119.5 ms | 293.3 ms |

## Per question

| Question | Kind | qh correct | mongodb-mcp correct | qh avg tokens | mongodb-mcp avg tokens |
|---|---|---|---|---|---|
| lendco-docs-01 | lookup | 3/3 | 3/3 | 4169 | 91455 |
| lendco-docs-02 | lookup | 3/3 | 3/3 | 4118 | 111696 |
| lendco-docs-03 | lookup | 3/3 | 3/3 | 4102 | 97401 |
| lendco-docs-04 | lookup | 3/3 | 3/3 | 4222 | 96961 |
| lendco-docs-05 | join | 3/3 | 3/3 | 4062 | 119034 |
| lendco-docs-06 | join | 3/3 | 3/3 | 7132 | 107739 |
| lendco-docs-07 | join | 3/3 | 3/3 | 5956 | 105796 |
| lendco-docs-08 | join | 3/3 | 3/3 | 7475 | 104617 |
| lendco-docs-09 | join | 3/3 | 3/3 | 6466 | 100212 |
| lendco-docs-10 | join | 3/3 | 3/3 | 4195 | 97746 |
| lendco-docs-11 | join | 3/3 | 3/3 | 6394 | 122356 |
| lendco-docs-12 | join | 3/3 | 3/3 | 5803 | 121148 |
| lendco-docs-13 | join | 3/3 | 3/3 | 5920 | 106308 |
| lendco-docs-14 | join | 3/3 | 3/3 | 4108 | 119444 |
| lendco-docs-15 | join | 3/3 | 3/3 | 6859 | 124618 |
| lendco-docs-16 | enum | 3/3 | 3/3 | 3993 | 97830 |
| lendco-docs-17 | enum | 3/3 | 3/3 | 4227 | 98541 |
| lendco-docs-18 | enum | 3/3 | 3/3 | 5845 | 112495 |
| lendco-docs-19 | enum | 3/3 | 3/3 | 4102 | 98723 |
| lendco-docs-20 | aggregate | 3/3 | 3/3 | 4690 | 100907 |
| lendco-docs-21 | aggregate | 3/3 | 3/3 | 4185 | 97146 |
| lendco-docs-22 | aggregate | 3/3 | 3/3 | 6042 | 104627 |
| lendco-docs-23 | aggregate | 3/3 | 3/3 | 6615 | 101683 |
| lendco-docs-24 | aggregate | 3/3 | 3/3 | 6138 | 124983 |
| lendco-docs-25 | aggregate | 3/3 | 3/3 | 4604 | 105933 |
| lendco-docs-26 | aggregate | 3/3 | 3/3 | 5752 | 112649 |
| lendco-docs-27 | large | 3/3 | 3/3 | 4082 | 97681 |
| lendco-docs-28 | large | 3/3 | 3/3 | 4359 | 107852 |
| lendco-docs-29 | misspell | 3/3 | 3/3 | 3837 | 97909 |
| lendco-docs-30 | misspell | 3/3 | 3/3 | 6315 | 127753 |
| pagila-docs-01 | lookup | 3/3 | 3/3 | 3832 | 76156 |
| pagila-docs-02 | lookup | 3/3 | 3/3 | 3526 | 102951 |
| pagila-docs-03 | lookup | 3/3 | 3/3 | 3660 | 90177 |
| pagila-docs-04 | lookup | 3/3 | 3/3 | 4633 | 96952 |
| pagila-docs-05 | join | 3/3 | 3/3 | 5094 | 90116 |
| pagila-docs-06 | join | 3/3 | 3/3 | 6984 | 100411 |
| pagila-docs-07 | join | 3/3 | 3/3 | 5657 | 104945 |
| pagila-docs-08 | join | 3/3 | 3/3 | 8420 | 104812 |
| pagila-docs-09 | join | 3/3 | 3/3 | 4842 | 100303 |
| pagila-docs-10 | join | 3/3 | 3/3 | 4041 | 77444 |
| pagila-docs-11 | join | 3/3 | 3/3 | 5674 | 97035 |
| pagila-docs-12 | join | 3/3 | 3/3 | 5611 | 98944 |
| pagila-docs-13 | join | 3/3 | 3/3 | 5812 | 109148 |
| pagila-docs-14 | join | 3/3 | 3/3 | 3969 | 70929 |
| pagila-docs-15 | join | 3/3 | 3/3 | 5904 | 99600 |
| pagila-docs-16 | enum | 3/3 | 3/3 | 3593 | 90979 |
| pagila-docs-17 | enum | 3/3 | 3/3 | 3741 | 124246 |
| pagila-docs-18 | enum | 3/3 | 3/3 | 5675 | 97839 |
| pagila-docs-19 | enum | 3/3 | 3/3 | 3724 | 96281 |
| pagila-docs-20 | aggregate | 3/3 | 3/3 | 3693 | 103553 |
| pagila-docs-21 | aggregate | 3/3 | 3/3 | 3647 | 83811 |
| pagila-docs-22 | aggregate | 3/3 | 3/3 | 5805 | 97643 |
| pagila-docs-23 | aggregate | 3/3 | 3/3 | 4313 | 107607 |
| pagila-docs-24 | aggregate | 3/3 | 3/3 | 5816 | 107656 |
| pagila-docs-25 | aggregate | 3/3 | 3/3 | 5257 | 110666 |
| pagila-docs-26 | aggregate | 3/3 | 3/3 | 4193 | 118075 |
| pagila-docs-27 | large | 3/3 | 3/3 | 3423 | 97690 |
| pagila-docs-28 | large | 3/3 | 3/3 | 5689 | 112879 |
| pagila-docs-29 | misspell | 3/3 | 3/3 | 3748 | 116100 |
| pagila-docs-30 | misspell | 3/3 | 3/3 | 5790 | 119949 |

## Notes

- mongodb-mcp-server serves one connection per process; its tool tokens are measured with one
- Cold schema: qh samples 100 documents from each of 1,000 collections and infers fields and references; mongodb-mcp-server's list-collections returns names only. mongodb-mcp-server connects when it starts, so its figure excludes connecting; qh's includes it.
- Cold schema is the first schema call after process start; it includes any database connection the server makes lazily (qh connects on first use).

Method: tool-definition tokens are `count_tokens(noop tool + server tools, server instructions as the system prompt) − count_tokens(noop tool)`, so they include the instructions the server sends at initialization. Total tokens are input (including cache reads and writes) plus output, summed over every model turn of a task.
