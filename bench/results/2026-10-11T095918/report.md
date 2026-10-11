# qh vs dbhub benchmark — 2026-10-11

Model `claude-sonnet-5-5` (effort high) · 3 run(s) per question · dbhub 1.5.0 · tool output capped at 100,000 bytes for both servers.

## Success criteria (spec §1)

| Criterion | Target | Result | Pass |
|---|---|---|---|
| Fewer total tokens per task | ≥ 40% fewer than dbhub | 40.3% fewer (360 paired runs) | ✅ |
| Accuracy | ≥ dbhub | 100.0% vs 100.0% (360 paired runs) | ✅ |
| Tool calls per task | fewer than dbhub | 1.6 vs 1.6 (360 paired runs) | ✅ |
| qh tool definitions | ≤ 800 tokens | not measured | — |
| Cold schema build, 1,000 tables | < 1 s | not measured | — |

## Results by dataset

| Dataset | Server | Accuracy | Avg total tokens | Avg input | Avg output | Avg tool calls | Avg wall (s) | Runs | Aborted | Errors | Truncated outputs |
|---|---|---|---|---|---|---|---|---|---|---|---|
| all | qh | 100% | 3088 | 2886 | 202 | 1.6 | 3.2 | 360 | 0 | 0 | 0 |
| all | dbhub | 100% | 5174 | 4922 | 251 | 1.6 | 3.5 | 360 | 0 | 0 | 0 |
| chinook | qh | 100% | 2762 | 2549 | 213 | 1.4 | 3.1 | 90 | 0 | 0 | 0 |
| chinook | dbhub | 100% | 3327 | 3093 | 234 | 1.3 | 3.2 | 90 | 0 | 0 | 0 |
| lendco | qh | 100% | 4931 | 4685 | 246 | 2.6 | 4.3 | 90 | 0 | 0 | 0 |
| lendco | dbhub | 100% | 11568 | 11169 | 399 | 3.1 | 5.3 | 90 | 0 | 0 | 0 |
| pagila | qh | 100% | 2357 | 2180 | 177 | 1.2 | 2.7 | 90 | 0 | 0 | 0 |
| pagila | dbhub | 100% | 2909 | 2731 | 178 | 1.1 | 2.9 | 90 | 0 | 0 | 0 |
| sakila | qh | 100% | 2304 | 2132 | 173 | 1.1 | 2.8 | 90 | 0 | 0 | 0 |
| sakila | dbhub | 100% | 2890 | 2697 | 193 | 1.1 | 2.7 | 90 | 0 | 0 | 0 |

## Per question

| Question | Kind | qh correct | dbhub correct | qh avg tokens | dbhub avg tokens |
|---|---|---|---|---|---|
| chinook-01 | lookup | 3/3 | 3/3 | 2404 | 3436 |
| chinook-02 | lookup | 3/3 | 3/3 | 2142 | 3838 |
| chinook-03 | lookup | 3/3 | 3/3 | 2823 | 3437 |
| chinook-04 | lookup | 3/3 | 3/3 | 2879 | 4916 |
| chinook-05 | lookup | 3/3 | 3/3 | 3162 | 2499 |
| chinook-06 | join | 3/3 | 3/3 | 2156 | 2690 |
| chinook-07 | join | 3/3 | 3/3 | 2108 | 2773 |
| chinook-08 | join | 3/3 | 3/3 | 2171 | 2812 |
| chinook-09 | join | 3/3 | 3/3 | 2382 | 3094 |
| chinook-10 | join | 3/3 | 3/3 | 2247 | 2791 |
| chinook-11 | join | 3/3 | 3/3 | 2192 | 2840 |
| chinook-12 | join | 3/3 | 3/3 | 4570 | 2704 |
| chinook-13 | join | 3/3 | 3/3 | 2211 | 2842 |
| chinook-14 | join | 3/3 | 3/3 | 2216 | 2751 |
| chinook-15 | join | 3/3 | 3/3 | 2318 | 6775 |
| chinook-16 | enum | 3/3 | 3/3 | 2124 | 2923 |
| chinook-17 | enum | 3/3 | 3/3 | 2094 | 2769 |
| chinook-18 | enum | 3/3 | 3/3 | 3389 | 4573 |
| chinook-19 | enum | 3/3 | 3/3 | 2222 | 2852 |
| chinook-20 | aggregate | 3/3 | 3/3 | 2089 | 2567 |
| chinook-21 | aggregate | 3/3 | 3/3 | 2823 | 4087 |
| chinook-22 | aggregate | 3/3 | 3/3 | 2975 | 2774 |
| chinook-23 | aggregate | 3/3 | 3/3 | 5948 | 3082 |
| chinook-24 | aggregate | 3/3 | 3/3 | 2052 | 2551 |
| chinook-25 | aggregate | 3/3 | 3/3 | 4385 | 3630 |
| chinook-26 | aggregate | 3/3 | 3/3 | 2230 | 2702 |
| chinook-27 | aggregate | 3/3 | 3/3 | 2397 | 3487 |
| chinook-28 | large | 3/3 | 3/3 | 2687 | 2600 |
| chinook-29 | large | 3/3 | 3/3 | 3640 | 2947 |
| chinook-30 | misspell | 3/3 | 3/3 | 3816 | 6066 |
| lendco-01 | lookup | 3/3 | 3/3 | 4260 | 10362 |
| lendco-02 | lookup | 3/3 | 3/3 | 3471 | 7128 |
| lendco-03 | lookup | 3/3 | 3/3 | 3850 | 9118 |
| lendco-04 | lookup | 3/3 | 3/3 | 3257 | 11630 |
| lendco-05 | join | 3/3 | 3/3 | 5601 | 11351 |
| lendco-06 | join | 3/3 | 3/3 | 8551 | 18494 |
| lendco-07 | join | 3/3 | 3/3 | 5761 | 12347 |
| lendco-08 | join | 3/3 | 3/3 | 4374 | 9170 |
| lendco-09 | join | 3/3 | 3/3 | 4479 | 12416 |
| lendco-10 | join | 3/3 | 3/3 | 5702 | 10920 |
| lendco-11 | join | 3/3 | 3/3 | 7980 | 13381 |
| lendco-12 | join | 3/3 | 3/3 | 4366 | 11842 |
| lendco-13 | join | 3/3 | 3/3 | 4460 | 9305 |
| lendco-14 | join | 3/3 | 3/3 | 5715 | 10952 |
| lendco-15 | join | 3/3 | 3/3 | 4485 | 12994 |
| lendco-16 | enum | 3/3 | 3/3 | 5446 | 13571 |
| lendco-17 | enum | 3/3 | 3/3 | 5582 | 10415 |
| lendco-18 | enum | 3/3 | 3/3 | 4334 | 12543 |
| lendco-19 | enum | 3/3 | 3/3 | 5939 | 9256 |
| lendco-20 | aggregate | 3/3 | 3/3 | 4348 | 9550 |
| lendco-21 | aggregate | 3/3 | 3/3 | 3346 | 9607 |
| lendco-22 | aggregate | 3/3 | 3/3 | 7955 | 13845 |
| lendco-23 | aggregate | 3/3 | 3/3 | 4625 | 14847 |
| lendco-24 | aggregate | 3/3 | 3/3 | 5062 | 12564 |
| lendco-25 | aggregate | 3/3 | 3/3 | 4101 | 9642 |
| lendco-26 | aggregate | 3/3 | 3/3 | 4443 | 18859 |
| lendco-27 | large | 3/3 | 3/3 | 4283 | 10596 |
| lendco-28 | large | 3/3 | 3/3 | 4444 | 15019 |
| lendco-29 | misspell | 3/3 | 3/3 | 3661 | 5758 |
| lendco-30 | misspell | 3/3 | 3/3 | 4049 | 9553 |
| pagila-01 | lookup | 3/3 | 3/3 | 2011 | 2456 |
| pagila-02 | lookup | 3/3 | 3/3 | 2151 | 2648 |
| pagila-03 | lookup | 3/3 | 3/3 | 2091 | 2592 |
| pagila-04 | lookup | 3/3 | 3/3 | 2053 | 2514 |
| pagila-05 | join | 3/3 | 3/3 | 2165 | 2605 |
| pagila-06 | join | 3/3 | 3/3 | 2673 | 4233 |
| pagila-07 | join | 3/3 | 3/3 | 2059 | 3280 |
| pagila-08 | join | 3/3 | 3/3 | 2178 | 2685 |
| pagila-09 | join | 3/3 | 3/3 | 2279 | 2928 |
| pagila-10 | join | 3/3 | 3/3 | 2243 | 2708 |
| pagila-11 | join | 3/3 | 3/3 | 3247 | 2597 |
| pagila-12 | join | 3/3 | 3/3 | 2149 | 2633 |
| pagila-13 | join | 3/3 | 3/3 | 2205 | 2744 |
| pagila-14 | join | 3/3 | 3/3 | 2160 | 2662 |
| pagila-15 | join | 3/3 | 3/3 | 2231 | 2783 |
| pagila-16 | enum | 3/3 | 3/3 | 2037 | 2493 |
| pagila-17 | enum | 3/3 | 3/3 | 2095 | 2572 |
| pagila-18 | enum | 3/3 | 3/3 | 2123 | 2599 |
| pagila-19 | enum | 3/3 | 3/3 | 2109 | 2644 |
| pagila-20 | aggregate | 3/3 | 3/3 | 2057 | 2561 |
| pagila-21 | aggregate | 3/3 | 3/3 | 2043 | 2495 |
| pagila-22 | aggregate | 3/3 | 3/3 | 2201 | 2729 |
| pagila-23 | aggregate | 3/3 | 3/3 | 2291 | 2849 |
| pagila-24 | aggregate | 3/3 | 3/3 | 2185 | 2705 |
| pagila-25 | aggregate | 3/3 | 3/3 | 2327 | 2941 |
| pagila-26 | aggregate | 3/3 | 3/3 | 2119 | 2650 |
| pagila-27 | large | 3/3 | 3/3 | 3323 | 2814 |
| pagila-28 | large | 3/3 | 3/3 | 2229 | 2897 |
| pagila-29 | misspell | 3/3 | 3/3 | 3479 | 5095 |
| pagila-30 | misspell | 3/3 | 3/3 | 4179 | 5152 |
| sakila-01 | lookup | 3/3 | 3/3 | 2014 | 2473 |
| sakila-02 | lookup | 3/3 | 3/3 | 2129 | 2572 |
| sakila-03 | lookup | 3/3 | 3/3 | 2091 | 2592 |
| sakila-04 | lookup | 3/3 | 3/3 | 2048 | 2528 |
| sakila-05 | join | 3/3 | 3/3 | 2140 | 2642 |
| sakila-06 | join | 3/3 | 3/3 | 2705 | 3424 |
| sakila-07 | join | 3/3 | 3/3 | 2076 | 2569 |
| sakila-08 | join | 3/3 | 3/3 | 2142 | 2752 |
| sakila-09 | join | 3/3 | 3/3 | 2334 | 2976 |
| sakila-10 | join | 3/3 | 3/3 | 2213 | 2772 |
| sakila-11 | join | 3/3 | 3/3 | 2132 | 2655 |
| sakila-12 | join | 3/3 | 3/3 | 2152 | 2663 |
| sakila-13 | join | 3/3 | 3/3 | 2218 | 2777 |
| sakila-14 | join | 3/3 | 3/3 | 2152 | 2687 |
| sakila-15 | join | 3/3 | 3/3 | 2205 | 2812 |
| sakila-16 | enum | 3/3 | 3/3 | 2036 | 2513 |
| sakila-17 | enum | 3/3 | 3/3 | 2104 | 2576 |
| sakila-18 | enum | 3/3 | 3/3 | 2126 | 2688 |
| sakila-19 | enum | 3/3 | 3/3 | 2104 | 2705 |
| sakila-20 | aggregate | 3/3 | 3/3 | 2069 | 2542 |
| sakila-21 | aggregate | 3/3 | 3/3 | 2046 | 2515 |
| sakila-22 | aggregate | 3/3 | 3/3 | 2213 | 2825 |
| sakila-23 | aggregate | 3/3 | 3/3 | 2300 | 2995 |
| sakila-24 | aggregate | 3/3 | 3/3 | 2234 | 2787 |
| sakila-25 | aggregate | 3/3 | 3/3 | 2327 | 2948 |
| sakila-26 | aggregate | 3/3 | 3/3 | 2139 | 2653 |
| sakila-27 | large | 3/3 | 3/3 | 2054 | 2493 |
| sakila-28 | large | 3/3 | 3/3 | 2269 | 2906 |
| sakila-29 | misspell | 3/3 | 3/3 | 3808 | 4567 |
| sakila-30 | misspell | 3/3 | 3/3 | 4539 | 6101 |

Method: tool-definition tokens are `count_tokens(noop tool + server tools, server instructions as the system prompt) − count_tokens(noop tool)`, so they include the instructions the server sends at initialization. Total tokens are input (including cache reads and writes) plus output, summed over every model turn of a task.
