# qh vs dbhub benchmark — 2026-10-11

Model `claude-sonnet-5-5` (effort high) · 3 run(s) per question · dbhub 1.5.0 · tool output capped at 100,000 bytes for both servers.

## Success criteria (spec §1)

| Criterion | Target | Result | Pass |
|---|---|---|---|
| Fewer total tokens per task | ≥ 40% fewer than dbhub | 40.4% fewer (360 paired runs) | ✅ |
| Accuracy | ≥ dbhub | 100.0% vs 100.0% (360 paired runs) | ✅ |
| Tool calls per task | fewer than dbhub | 1.6 vs 1.6 (360 paired runs) | ✅ |
| qh tool definitions | ≤ 800 tokens | 393 tokens | ✅ |
| Cold schema build, 1,000 tables | < 1 s | 0.08 s | ✅ |

## Results by dataset

| Dataset | Server | Accuracy | Avg total tokens | Avg input | Avg output | Avg tool calls | Avg wall (s) | Runs | Aborted | Errors | Truncated outputs |
|---|---|---|---|---|---|---|---|---|---|---|---|
| all | qh | 100% | 3092 | 2890 | 202 | 1.6 | 3.3 | 360 | 0 | 0 | 0 |
| all | dbhub | 100% | 5188 | 4933 | 256 | 1.6 | 3.4 | 360 | 0 | 0 | 0 |
| chinook | qh | 100% | 2809 | 2594 | 215 | 1.4 | 3.1 | 90 | 0 | 0 | 0 |
| chinook | dbhub | 100% | 3285 | 3050 | 235 | 1.2 | 3.0 | 90 | 0 | 0 | 0 |
| lendco | qh | 100% | 4885 | 4638 | 248 | 2.6 | 4.4 | 90 | 0 | 0 | 0 |
| lendco | dbhub | 100% | 11619 | 11207 | 412 | 3.1 | 5.2 | 90 | 0 | 0 | 0 |
| pagila | qh | 100% | 2381 | 2205 | 176 | 1.2 | 2.8 | 90 | 0 | 0 | 0 |
| pagila | dbhub | 100% | 2925 | 2746 | 178 | 1.1 | 2.7 | 90 | 0 | 0 | 0 |
| sakila | qh | 100% | 2294 | 2123 | 171 | 1.1 | 2.8 | 90 | 0 | 0 | 0 |
| sakila | dbhub | 100% | 2924 | 2726 | 198 | 1.1 | 2.7 | 90 | 0 | 0 | 0 |

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
| Cold schema, 1,000 tables (median of 3) | 83.7 ms | 2.08 s |
| Query latency p50 | 0.5 ms | 0.7 ms |
| Query latency p95 | 2.5 ms | 2.8 ms |

## Per question

| Question | Kind | qh correct | dbhub correct | qh avg tokens | dbhub avg tokens |
|---|---|---|---|---|---|
| chinook-01 | lookup | 3/3 | 3/3 | 2786 | 2968 |
| chinook-02 | lookup | 3/3 | 3/3 | 2142 | 5041 |
| chinook-03 | lookup | 3/3 | 3/3 | 2047 | 3903 |
| chinook-04 | lookup | 3/3 | 3/3 | 3281 | 3741 |
| chinook-05 | lookup | 3/3 | 3/3 | 3162 | 2481 |
| chinook-06 | join | 3/3 | 3/3 | 2156 | 2690 |
| chinook-07 | join | 3/3 | 3/3 | 2108 | 2744 |
| chinook-08 | join | 3/3 | 3/3 | 2171 | 2812 |
| chinook-09 | join | 3/3 | 3/3 | 2382 | 3078 |
| chinook-10 | join | 3/3 | 3/3 | 2247 | 4116 |
| chinook-11 | join | 3/3 | 3/3 | 2711 | 2840 |
| chinook-12 | join | 3/3 | 3/3 | 4570 | 2704 |
| chinook-13 | join | 3/3 | 3/3 | 2211 | 2843 |
| chinook-14 | join | 3/3 | 3/3 | 2216 | 2739 |
| chinook-15 | join | 3/3 | 3/3 | 2308 | 6061 |
| chinook-16 | enum | 3/3 | 3/3 | 2124 | 2883 |
| chinook-17 | enum | 3/3 | 3/3 | 2094 | 2741 |
| chinook-18 | enum | 3/3 | 3/3 | 3407 | 4573 |
| chinook-19 | enum | 3/3 | 3/3 | 2222 | 2831 |
| chinook-20 | aggregate | 3/3 | 3/3 | 2076 | 2567 |
| chinook-21 | aggregate | 3/3 | 3/3 | 3213 | 3337 |
| chinook-22 | aggregate | 3/3 | 3/3 | 2149 | 2777 |
| chinook-23 | aggregate | 3/3 | 3/3 | 5936 | 3060 |
| chinook-24 | aggregate | 3/3 | 3/3 | 2052 | 2551 |
| chinook-25 | aggregate | 3/3 | 3/3 | 4392 | 3594 |
| chinook-26 | aggregate | 3/3 | 3/3 | 3523 | 2723 |
| chinook-27 | aggregate | 3/3 | 3/3 | 2409 | 3487 |
| chinook-28 | large | 3/3 | 3/3 | 2687 | 2600 |
| chinook-29 | large | 3/3 | 3/3 | 3662 | 2968 |
| chinook-30 | misspell | 3/3 | 3/3 | 3821 | 5094 |
| lendco-01 | lookup | 3/3 | 3/3 | 4260 | 11318 |
| lendco-02 | lookup | 3/3 | 3/3 | 3469 | 7128 |
| lendco-03 | lookup | 3/3 | 3/3 | 3850 | 9117 |
| lendco-04 | lookup | 3/3 | 3/3 | 3249 | 11535 |
| lendco-05 | join | 3/3 | 3/3 | 5607 | 10845 |
| lendco-06 | join | 3/3 | 3/3 | 6945 | 18280 |
| lendco-07 | join | 3/3 | 3/3 | 5761 | 12347 |
| lendco-08 | join | 3/3 | 3/3 | 4372 | 11375 |
| lendco-09 | join | 3/3 | 3/3 | 4472 | 12416 |
| lendco-10 | join | 3/3 | 3/3 | 5716 | 11193 |
| lendco-11 | join | 3/3 | 3/3 | 7968 | 13953 |
| lendco-12 | join | 3/3 | 3/3 | 4366 | 11382 |
| lendco-13 | join | 3/3 | 3/3 | 4459 | 9317 |
| lendco-14 | join | 3/3 | 3/3 | 5717 | 9405 |
| lendco-15 | join | 3/3 | 3/3 | 4511 | 13464 |
| lendco-16 | enum | 3/3 | 3/3 | 5446 | 10653 |
| lendco-17 | enum | 3/3 | 3/3 | 5582 | 10396 |
| lendco-18 | enum | 3/3 | 3/3 | 4533 | 12571 |
| lendco-19 | enum | 3/3 | 3/3 | 5939 | 9254 |
| lendco-20 | aggregate | 3/3 | 3/3 | 4754 | 7655 |
| lendco-21 | aggregate | 3/3 | 3/3 | 3346 | 9434 |
| lendco-22 | aggregate | 3/3 | 3/3 | 7955 | 14982 |
| lendco-23 | aggregate | 3/3 | 3/3 | 4683 | 12946 |
| lendco-24 | aggregate | 3/3 | 3/3 | 4408 | 12564 |
| lendco-25 | aggregate | 3/3 | 3/3 | 4101 | 9640 |
| lendco-26 | aggregate | 3/3 | 3/3 | 5127 | 18720 |
| lendco-27 | large | 3/3 | 3/3 | 3835 | 9683 |
| lendco-28 | large | 3/3 | 3/3 | 4444 | 21570 |
| lendco-29 | misspell | 3/3 | 3/3 | 3659 | 5758 |
| lendco-30 | misspell | 3/3 | 3/3 | 4021 | 9664 |
| pagila-01 | lookup | 3/3 | 3/3 | 2011 | 2456 |
| pagila-02 | lookup | 3/3 | 3/3 | 2149 | 2616 |
| pagila-03 | lookup | 3/3 | 3/3 | 2091 | 2584 |
| pagila-04 | lookup | 3/3 | 3/3 | 2053 | 2514 |
| pagila-05 | join | 3/3 | 3/3 | 2150 | 2603 |
| pagila-06 | join | 3/3 | 3/3 | 2673 | 4233 |
| pagila-07 | join | 3/3 | 3/3 | 2059 | 4084 |
| pagila-08 | join | 3/3 | 3/3 | 2188 | 2685 |
| pagila-09 | join | 3/3 | 3/3 | 2273 | 2928 |
| pagila-10 | join | 3/3 | 3/3 | 2243 | 2748 |
| pagila-11 | join | 3/3 | 3/3 | 3892 | 2578 |
| pagila-12 | join | 3/3 | 3/3 | 2149 | 2639 |
| pagila-13 | join | 3/3 | 3/3 | 2205 | 2744 |
| pagila-14 | join | 3/3 | 3/3 | 2160 | 2669 |
| pagila-15 | join | 3/3 | 3/3 | 2231 | 2783 |
| pagila-16 | enum | 3/3 | 3/3 | 2038 | 2493 |
| pagila-17 | enum | 3/3 | 3/3 | 2095 | 2572 |
| pagila-18 | enum | 3/3 | 3/3 | 2113 | 2578 |
| pagila-19 | enum | 3/3 | 3/3 | 2109 | 2644 |
| pagila-20 | aggregate | 3/3 | 3/3 | 2057 | 2568 |
| pagila-21 | aggregate | 3/3 | 3/3 | 2043 | 2495 |
| pagila-22 | aggregate | 3/3 | 3/3 | 2187 | 2729 |
| pagila-23 | aggregate | 3/3 | 3/3 | 2291 | 2823 |
| pagila-24 | aggregate | 3/3 | 3/3 | 2185 | 2683 |
| pagila-25 | aggregate | 3/3 | 3/3 | 2327 | 2941 |
| pagila-26 | aggregate | 3/3 | 3/3 | 2119 | 2650 |
| pagila-27 | large | 3/3 | 3/3 | 3330 | 2809 |
| pagila-28 | large | 3/3 | 3/3 | 2229 | 2882 |
| pagila-29 | misspell | 3/3 | 3/3 | 3595 | 5095 |
| pagila-30 | misspell | 3/3 | 3/3 | 4177 | 4915 |
| sakila-01 | lookup | 3/3 | 3/3 | 2014 | 2473 |
| sakila-02 | lookup | 3/3 | 3/3 | 2129 | 2571 |
| sakila-03 | lookup | 3/3 | 3/3 | 2091 | 2592 |
| sakila-04 | lookup | 3/3 | 3/3 | 2048 | 2528 |
| sakila-05 | join | 3/3 | 3/3 | 2140 | 2661 |
| sakila-06 | join | 3/3 | 3/3 | 2705 | 4010 |
| sakila-07 | join | 3/3 | 3/3 | 2076 | 2585 |
| sakila-08 | join | 3/3 | 3/3 | 2142 | 2769 |
| sakila-09 | join | 3/3 | 3/3 | 2291 | 3020 |
| sakila-10 | join | 3/3 | 3/3 | 2213 | 2783 |
| sakila-11 | join | 3/3 | 3/3 | 2151 | 2679 |
| sakila-12 | join | 3/3 | 3/3 | 2152 | 2646 |
| sakila-13 | join | 3/3 | 3/3 | 2204 | 2805 |
| sakila-14 | join | 3/3 | 3/3 | 2152 | 2687 |
| sakila-15 | join | 3/3 | 3/3 | 2191 | 2812 |
| sakila-16 | enum | 3/3 | 3/3 | 2036 | 2501 |
| sakila-17 | enum | 3/3 | 3/3 | 2104 | 2576 |
| sakila-18 | enum | 3/3 | 3/3 | 2126 | 2713 |
| sakila-19 | enum | 3/3 | 3/3 | 2104 | 2705 |
| sakila-20 | aggregate | 3/3 | 3/3 | 2069 | 2542 |
| sakila-21 | aggregate | 3/3 | 3/3 | 2046 | 2530 |
| sakila-22 | aggregate | 3/3 | 3/3 | 2211 | 2857 |
| sakila-23 | aggregate | 3/3 | 3/3 | 2300 | 2995 |
| sakila-24 | aggregate | 3/3 | 3/3 | 2196 | 2787 |
| sakila-25 | aggregate | 3/3 | 3/3 | 2327 | 2986 |
| sakila-26 | aggregate | 3/3 | 3/3 | 2139 | 2670 |
| sakila-27 | large | 3/3 | 3/3 | 2054 | 2493 |
| sakila-28 | large | 3/3 | 3/3 | 2269 | 2906 |
| sakila-29 | misspell | 3/3 | 3/3 | 3806 | 4827 |
| sakila-30 | misspell | 3/3 | 3/3 | 4329 | 6007 |

## Notes

- Cold schema: qh's overview lists the first 200 of 1,000 tables with a pattern hint (it builds the full catalogue); dbhub's search_objects (summary, limit 1000) lists all 1,000.
- Cold schema is the first schema call after process start; it includes any database connection the server makes lazily (qh connects on first use).

Method: tool-definition tokens are `count_tokens(noop tool + server tools, server instructions as the system prompt) − count_tokens(noop tool)`, so they include the instructions the server sends at initialization. Total tokens are input (including cache reads and writes) plus output, summed over every model turn of a task.
