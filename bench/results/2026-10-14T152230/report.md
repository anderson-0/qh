# qh vs toolbox-data benchmark — 2026-10-14

Model `claude-sonnet-5-5` (effort high) · 3 run(s) per question · toolbox-data 1.14.0 · tool output capped at 100,000 bytes for both servers.

## Success criteria (spec §1)

| Criterion | Target | Result | Pass |
|---|---|---|---|
| Fewer total tokens per task | ≥ 40% fewer than toolbox-data | 76.4% fewer (180 paired runs) | ✅ |
| Accuracy | ≥ toolbox-data | 100.0% vs 100.0% (180 paired runs) | ✅ |
| Tool calls per task | fewer than toolbox-data | 1.9 vs 2.2 (180 paired runs) | ✅ |
| qh tool definitions | ≤ 800 tokens | 392 tokens | ✅ |
| Cold schema build, 1,000 tables | < 1 s | not measured | — |

## Results by dataset

| Dataset | Server | Accuracy | Avg total tokens | Avg input | Avg output | Avg tool calls | Avg wall (s) | Runs | Aborted | Errors | Truncated outputs |
|---|---|---|---|---|---|---|---|---|---|---|---|
| all | qh | 100% | 3653 | 3442 | 210 | 1.9 | 4.7 | 180 | 0 | 0 | 0 |
| all | toolbox-data | 100% | 15463 | 15215 | 247 | 2.2 | 4.8 | 180 | 0 | 0 | 0 |
| lendco | qh | 100% | 4909 | 4665 | 244 | 2.6 | 5.7 | 90 | 0 | 0 | 0 |
| lendco | toolbox-data | 100% | 22287 | 22014 | 274 | 2.6 | 5.5 | 90 | 0 | 0 | 0 |
| pagila | qh | 100% | 2396 | 2219 | 177 | 1.2 | 3.6 | 90 | 0 | 0 | 0 |
| pagila | toolbox-data | 100% | 8639 | 8417 | 221 | 1.8 | 4.1 | 90 | 0 | 0 | 0 |

## Tool-definition tokens by number of connections

| Connections | qh | toolbox-data |
|---|---|---|
| 1 | 392 | 2054 |
| 2 | 428 | — |
| 3 | 466 | — |
| 4 | 504 | — |
| 5 | 542 | — |

## Per question

| Question | Kind | qh correct | toolbox-data correct | qh avg tokens | toolbox-data avg tokens |
|---|---|---|---|---|---|
| lendco-01 | lookup | 3/3 | 3/3 | 4260 | 14520 |
| lendco-02 | lookup | 3/3 | 3/3 | 3469 | 14476 |
| lendco-03 | lookup | 3/3 | 3/3 | 3850 | 11470 |
| lendco-04 | lookup | 3/3 | 3/3 | 3249 | 13360 |
| lendco-05 | join | 3/3 | 3/3 | 5717 | 19357 |
| lendco-06 | join | 3/3 | 3/3 | 7147 | 18140 |
| lendco-07 | join | 3/3 | 3/3 | 4409 | 21346 |
| lendco-08 | join | 3/3 | 3/3 | 4374 | 16883 |
| lendco-09 | join | 3/3 | 3/3 | 4482 | 15005 |
| lendco-10 | join | 3/3 | 3/3 | 5702 | 22319 |
| lendco-11 | join | 3/3 | 3/3 | 7914 | 22213 |
| lendco-12 | join | 3/3 | 3/3 | 4366 | 17836 |
| lendco-13 | join | 3/3 | 3/3 | 4458 | 21364 |
| lendco-14 | join | 3/3 | 3/3 | 5591 | 16927 |
| lendco-15 | join | 3/3 | 3/3 | 4468 | 26719 |
| lendco-16 | enum | 3/3 | 3/3 | 5446 | 19837 |
| lendco-17 | enum | 3/3 | 3/3 | 5851 | 30444 |
| lendco-18 | enum | 3/3 | 3/3 | 4428 | 30596 |
| lendco-19 | enum | 3/3 | 3/3 | 5939 | 14624 |
| lendco-20 | aggregate | 3/3 | 3/3 | 4333 | 30381 |
| lendco-21 | aggregate | 3/3 | 3/3 | 3346 | 30355 |
| lendco-22 | aggregate | 3/3 | 3/3 | 7955 | 30547 |
| lendco-23 | aggregate | 3/3 | 3/3 | 4611 | 22783 |
| lendco-24 | aggregate | 3/3 | 3/3 | 4494 | 44925 |
| lendco-25 | aggregate | 3/3 | 3/3 | 5854 | 25517 |
| lendco-26 | aggregate | 3/3 | 3/3 | 5121 | 14790 |
| lendco-27 | large | 3/3 | 3/3 | 4283 | 30349 |
| lendco-28 | large | 3/3 | 3/3 | 4444 | 30577 |
| lendco-29 | misspell | 3/3 | 3/3 | 3663 | 10389 |
| lendco-30 | misspell | 3/3 | 3/3 | 4031 | 30561 |
| pagila-01 | lookup | 3/3 | 3/3 | 2011 | 9625 |
| pagila-02 | lookup | 3/3 | 3/3 | 2146 | 5463 |
| pagila-03 | lookup | 3/3 | 3/3 | 2091 | 5405 |
| pagila-04 | lookup | 3/3 | 3/3 | 2935 | 8245 |
| pagila-05 | join | 3/3 | 3/3 | 2119 | 5499 |
| pagila-06 | join | 3/3 | 3/3 | 2673 | 6128 |
| pagila-07 | join | 3/3 | 3/3 | 2059 | 8875 |
| pagila-08 | join | 3/3 | 3/3 | 2180 | 10684 |
| pagila-09 | join | 3/3 | 3/3 | 2273 | 9968 |
| pagila-10 | join | 3/3 | 3/3 | 2206 | 5552 |
| pagila-11 | join | 3/3 | 3/3 | 3678 | 9708 |
| pagila-12 | join | 3/3 | 3/3 | 2149 | 5463 |
| pagila-13 | join | 3/3 | 3/3 | 2205 | 8500 |
| pagila-14 | join | 3/3 | 3/3 | 2148 | 5518 |
| pagila-15 | join | 3/3 | 3/3 | 2235 | 7531 |
| pagila-16 | enum | 3/3 | 3/3 | 2037 | 8219 |
| pagila-17 | enum | 3/3 | 3/3 | 2095 | 12120 |
| pagila-18 | enum | 3/3 | 3/3 | 2113 | 8425 |
| pagila-19 | enum | 3/3 | 3/3 | 2109 | 8330 |
| pagila-20 | aggregate | 3/3 | 3/3 | 2057 | 5381 |
| pagila-21 | aggregate | 3/3 | 3/3 | 2043 | 7278 |
| pagila-22 | aggregate | 3/3 | 3/3 | 2201 | 9841 |
| pagila-23 | aggregate | 3/3 | 3/3 | 2291 | 9887 |
| pagila-24 | aggregate | 3/3 | 3/3 | 2185 | 9802 |
| pagila-25 | aggregate | 3/3 | 3/3 | 2327 | 8581 |
| pagila-26 | aggregate | 3/3 | 3/3 | 2119 | 9872 |
| pagila-27 | large | 3/3 | 3/3 | 3316 | 9695 |
| pagila-28 | large | 3/3 | 3/3 | 2229 | 9958 |
| pagila-29 | misspell | 3/3 | 3/3 | 3479 | 15210 |
| pagila-30 | misspell | 3/3 | 3/3 | 4177 | 14392 |

## Notes

- Toolbox's prebuilt configuration serves one database per process and its tools depend on the engine; tool tokens are measured with one Postgres connection (Pagila). Its MySQL and SQLite tool sets are smaller.

Method: tool-definition tokens are `count_tokens(noop tool + server tools, server instructions as the system prompt) − count_tokens(noop tool)`, so they include the instructions the server sends at initialization. Total tokens are input (including cache reads and writes) plus output, summed over every model turn of a task.
