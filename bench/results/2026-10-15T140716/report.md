# qh vs dbhub benchmark — 2026-10-15

Model `claude-sonnet-5-5` (effort high) · 3 run(s) per question · dbhub 1.5.0 · tool output capped at 100,000 bytes for both servers.

## Success criteria (spec §1)

| Criterion | Target | Result | Pass |
|---|---|---|---|
| Fewer total tokens per task | ≥ 40% fewer than dbhub | 40.2% fewer (360 paired runs) | ✅ |
| Accuracy | ≥ dbhub | 100.0% vs 100.0% (360 paired runs) | ✅ |
| Tool calls per task | fewer than dbhub | 1.5 vs 1.6 (360 paired runs) | ✅ |
| qh tool definitions | ≤ 800 tokens | 393 tokens | ✅ |
| Cold schema build, 1,000 Postgres tables | < 1 s | 0.02 s | ✅ |

## Results by dataset

| Dataset | Server | Accuracy | Avg total tokens | Avg input | Avg output | Avg tool calls | Avg wall (s) | Runs | Aborted | Errors | Truncated outputs |
|---|---|---|---|---|---|---|---|---|---|---|---|
| all | qh | 100% | 3090 | 2892 | 199 | 1.5 | 4.0 | 360 | 0 | 0 | 0 |
| all | dbhub | 100% | 5171 | 4924 | 247 | 1.6 | 4.0 | 360 | 0 | 0 | 0 |
| chinook | qh | 100% | 2754 | 2542 | 213 | 1.4 | 4.2 | 90 | 0 | 0 | 0 |
| chinook | dbhub | 100% | 3291 | 3059 | 233 | 1.2 | 3.4 | 90 | 0 | 0 | 0 |
| lendco | qh | 100% | 5016 | 4775 | 241 | 2.5 | 5.2 | 90 | 0 | 0 | 0 |
| lendco | dbhub | 100% | 11579 | 11191 | 388 | 2.9 | 6.0 | 90 | 0 | 0 | 0 |
| pagila | qh | 100% | 2314 | 2142 | 172 | 1.1 | 3.4 | 90 | 0 | 0 | 0 |
| pagila | dbhub | 100% | 2867 | 2692 | 175 | 1.1 | 3.3 | 90 | 0 | 0 | 0 |
| sakila | qh | 100% | 2277 | 2108 | 169 | 1.1 | 3.4 | 90 | 0 | 0 | 0 |
| sakila | dbhub | 100% | 2948 | 2755 | 193 | 1.1 | 3.2 | 90 | 0 | 0 | 0 |

## Tool-definition tokens by number of connections

| Connections | qh | dbhub |
|---|---|---|
| 1 | 393 | 579 |
| 2 | 430 | 1206 |
| 3 | 469 | 1809 |
| 4 | 508 | 2412 |
| 5 | 547 | 3015 |

## Micro-benchmarks

| | qh | dbhub |
|---|---|---|
| Cold schema, 1,000 Postgres tables (median of 3) | 22.9 ms | 2.10 s |
| Query latency p50 (Postgres) | 0.5 ms | 0.8 ms |
| Query latency p95 (Postgres) | 2.7 ms | 2.7 ms |

## Per question

| Question | Kind | qh correct | dbhub correct | qh avg tokens | dbhub avg tokens |
|---|---|---|---|---|---|
| chinook-01 | lookup | 3/3 | 3/3 | 2786 | 3755 |
| chinook-02 | lookup | 3/3 | 3/3 | 2142 | 3128 |
| chinook-03 | lookup | 3/3 | 3/3 | 2823 | 2506 |
| chinook-04 | lookup | 3/3 | 3/3 | 3281 | 4916 |
| chinook-05 | lookup | 3/3 | 3/3 | 3162 | 2499 |
| chinook-06 | join | 3/3 | 3/3 | 2154 | 2669 |
| chinook-07 | join | 3/3 | 3/3 | 2108 | 2773 |
| chinook-08 | join | 3/3 | 3/3 | 2171 | 2812 |
| chinook-09 | join | 3/3 | 3/3 | 2382 | 3110 |
| chinook-10 | join | 3/3 | 3/3 | 2247 | 2795 |
| chinook-11 | join | 3/3 | 3/3 | 2192 | 2816 |
| chinook-12 | join | 3/3 | 3/3 | 3776 | 2686 |
| chinook-13 | join | 3/3 | 3/3 | 2221 | 2864 |
| chinook-14 | join | 3/3 | 3/3 | 2216 | 3245 |
| chinook-15 | join | 3/3 | 3/3 | 2274 | 5957 |
| chinook-16 | enum | 3/3 | 3/3 | 2124 | 2731 |
| chinook-17 | enum | 3/3 | 3/3 | 2094 | 2769 |
| chinook-18 | enum | 3/3 | 3/3 | 3389 | 4573 |
| chinook-19 | enum | 3/3 | 3/3 | 2222 | 2852 |
| chinook-20 | aggregate | 3/3 | 3/3 | 2089 | 2605 |
| chinook-21 | aggregate | 3/3 | 3/3 | 3213 | 3471 |
| chinook-22 | aggregate | 3/3 | 3/3 | 2149 | 2777 |
| chinook-23 | aggregate | 3/3 | 3/3 | 5928 | 3099 |
| chinook-24 | aggregate | 3/3 | 3/3 | 2052 | 2551 |
| chinook-25 | aggregate | 3/3 | 3/3 | 4396 | 3851 |
| chinook-26 | aggregate | 3/3 | 3/3 | 2227 | 2856 |
| chinook-27 | aggregate | 3/3 | 3/3 | 3195 | 3425 |
| chinook-28 | large | 3/3 | 3/3 | 2129 | 2631 |
| chinook-29 | large | 3/3 | 3/3 | 3669 | 2878 |
| chinook-30 | misspell | 3/3 | 3/3 | 3808 | 7138 |
| lendco-01 | lookup | 3/3 | 3/3 | 4260 | 11662 |
| lendco-02 | lookup | 3/3 | 3/3 | 3473 | 7109 |
| lendco-03 | lookup | 3/3 | 3/3 | 3850 | 4929 |
| lendco-04 | lookup | 3/3 | 3/3 | 3249 | 9771 |
| lendco-05 | join | 3/3 | 3/3 | 5609 | 15505 |
| lendco-06 | join | 3/3 | 3/3 | 10970 | 16982 |
| lendco-07 | join | 3/3 | 3/3 | 4447 | 12884 |
| lendco-08 | join | 3/3 | 3/3 | 4374 | 12516 |
| lendco-09 | join | 3/3 | 3/3 | 4471 | 10383 |
| lendco-10 | join | 3/3 | 3/3 | 5702 | 9382 |
| lendco-11 | join | 3/3 | 3/3 | 7887 | 13960 |
| lendco-12 | join | 3/3 | 3/3 | 4368 | 13838 |
| lendco-13 | join | 3/3 | 3/3 | 4446 | 9313 |
| lendco-14 | join | 3/3 | 3/3 | 5751 | 13972 |
| lendco-15 | join | 3/3 | 3/3 | 4457 | 12529 |
| lendco-16 | enum | 3/3 | 3/3 | 5446 | 9889 |
| lendco-17 | enum | 3/3 | 3/3 | 5582 | 9272 |
| lendco-18 | enum | 3/3 | 3/3 | 4348 | 13167 |
| lendco-19 | enum | 3/3 | 3/3 | 5667 | 9255 |
| lendco-20 | aggregate | 3/3 | 3/3 | 4345 | 8676 |
| lendco-21 | aggregate | 3/3 | 3/3 | 3346 | 9722 |
| lendco-22 | aggregate | 3/3 | 3/3 | 7955 | 12755 |
| lendco-23 | aggregate | 3/3 | 3/3 | 4723 | 12904 |
| lendco-24 | aggregate | 3/3 | 3/3 | 4494 | 14743 |
| lendco-25 | aggregate | 3/3 | 3/3 | 5973 | 10166 |
| lendco-26 | aggregate | 3/3 | 3/3 | 4439 | 18725 |
| lendco-27 | large | 3/3 | 3/3 | 4724 | 10630 |
| lendco-28 | large | 3/3 | 3/3 | 4426 | 17397 |
| lendco-29 | misspell | 3/3 | 3/3 | 3658 | 5763 |
| lendco-30 | misspell | 3/3 | 3/3 | 4025 | 9550 |
| pagila-01 | lookup | 3/3 | 3/3 | 2011 | 2456 |
| pagila-02 | lookup | 3/3 | 3/3 | 2151 | 2642 |
| pagila-03 | lookup | 3/3 | 3/3 | 2091 | 2588 |
| pagila-04 | lookup | 3/3 | 3/3 | 2053 | 2514 |
| pagila-05 | join | 3/3 | 3/3 | 2119 | 2603 |
| pagila-06 | join | 3/3 | 3/3 | 2683 | 3669 |
| pagila-07 | join | 3/3 | 3/3 | 2059 | 2526 |
| pagila-08 | join | 3/3 | 3/3 | 2173 | 2685 |
| pagila-09 | join | 3/3 | 3/3 | 2276 | 2927 |
| pagila-10 | join | 3/3 | 3/3 | 2242 | 2720 |
| pagila-11 | join | 3/3 | 3/3 | 2093 | 2596 |
| pagila-12 | join | 3/3 | 3/3 | 2149 | 2633 |
| pagila-13 | join | 3/3 | 3/3 | 2205 | 2748 |
| pagila-14 | join | 3/3 | 3/3 | 2151 | 2673 |
| pagila-15 | join | 3/3 | 3/3 | 2239 | 2740 |
| pagila-16 | enum | 3/3 | 3/3 | 2037 | 2491 |
| pagila-17 | enum | 3/3 | 3/3 | 2095 | 2581 |
| pagila-18 | enum | 3/3 | 3/3 | 2095 | 2599 |
| pagila-19 | enum | 3/3 | 3/3 | 2109 | 2644 |
| pagila-20 | aggregate | 3/3 | 3/3 | 2057 | 2575 |
| pagila-21 | aggregate | 3/3 | 3/3 | 2043 | 2495 |
| pagila-22 | aggregate | 3/3 | 3/3 | 2201 | 2729 |
| pagila-23 | aggregate | 3/3 | 3/3 | 2291 | 2823 |
| pagila-24 | aggregate | 3/3 | 3/3 | 2175 | 2705 |
| pagila-25 | aggregate | 3/3 | 3/3 | 2327 | 2941 |
| pagila-26 | aggregate | 3/3 | 3/3 | 2119 | 2650 |
| pagila-27 | large | 3/3 | 3/3 | 3332 | 2503 |
| pagila-28 | large | 3/3 | 3/3 | 2233 | 2851 |
| pagila-29 | misspell | 3/3 | 3/3 | 3596 | 5095 |
| pagila-30 | misspell | 3/3 | 3/3 | 4025 | 5604 |
| sakila-01 | lookup | 3/3 | 3/3 | 2014 | 2473 |
| sakila-02 | lookup | 3/3 | 3/3 | 2107 | 2571 |
| sakila-03 | lookup | 3/3 | 3/3 | 2096 | 2566 |
| sakila-04 | lookup | 3/3 | 3/3 | 2048 | 2528 |
| sakila-05 | join | 3/3 | 3/3 | 2140 | 2642 |
| sakila-06 | join | 3/3 | 3/3 | 2705 | 3717 |
| sakila-07 | join | 3/3 | 3/3 | 2076 | 2585 |
| sakila-08 | join | 3/3 | 3/3 | 2142 | 2707 |
| sakila-09 | join | 3/3 | 3/3 | 2293 | 3020 |
| sakila-10 | join | 3/3 | 3/3 | 2206 | 2740 |
| sakila-11 | join | 3/3 | 3/3 | 2138 | 2674 |
| sakila-12 | join | 3/3 | 3/3 | 2152 | 2663 |
| sakila-13 | join | 3/3 | 3/3 | 2204 | 2798 |
| sakila-14 | join | 3/3 | 3/3 | 2152 | 2687 |
| sakila-15 | join | 3/3 | 3/3 | 2191 | 2743 |
| sakila-16 | enum | 3/3 | 3/3 | 2044 | 2501 |
| sakila-17 | enum | 3/3 | 3/3 | 2089 | 2576 |
| sakila-18 | enum | 3/3 | 3/3 | 2126 | 2688 |
| sakila-19 | enum | 3/3 | 3/3 | 2104 | 2705 |
| sakila-20 | aggregate | 3/3 | 3/3 | 2069 | 2521 |
| sakila-21 | aggregate | 3/3 | 3/3 | 2046 | 2515 |
| sakila-22 | aggregate | 3/3 | 3/3 | 2201 | 2825 |
| sakila-23 | aggregate | 3/3 | 3/3 | 2300 | 2995 |
| sakila-24 | aggregate | 3/3 | 3/3 | 2196 | 2787 |
| sakila-25 | aggregate | 3/3 | 3/3 | 2327 | 2967 |
| sakila-26 | aggregate | 3/3 | 3/3 | 2139 | 2616 |
| sakila-27 | large | 3/3 | 3/3 | 2038 | 2493 |
| sakila-28 | large | 3/3 | 3/3 | 2256 | 2818 |
| sakila-29 | misspell | 3/3 | 3/3 | 3803 | 5089 |
| sakila-30 | misspell | 3/3 | 3/3 | 3911 | 7222 |

## Notes

- Cold schema: qh's overview lists the first 200 of 1,000 tables with a pattern hint (it builds the full catalogue); dbhub's search_objects (summary, limit 1000) lists all 1,000.
- Cold schema is the first schema call after process start; it includes any database connection the server makes lazily (qh connects on first use).
- The three fresh processes use the same database instance; database caches and statistics can warm between runs. The reported median does not measure a database restart.

Method: tool-definition tokens are `count_tokens(noop tool + server tools, server instructions as the system prompt) − count_tokens(noop tool)`, so they include the instructions the server sends at initialization. Total tokens are input (including cache reads and writes) plus output, summed over every model turn of a task.
