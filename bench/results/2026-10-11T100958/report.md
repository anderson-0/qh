# qh vs dbhub benchmark — 2026-10-11

Model `claude-sonnet-5-5` (effort high) · 3 run(s) per question · dbhub 1.5.0 · tool output capped at 100,000 bytes for both servers.

## Success criteria (spec §1)

| Criterion | Target | Result | Pass |
|---|---|---|---|
| Fewer total tokens per task | ≥ 40% fewer than dbhub | 40.4% fewer (360 paired runs) | ✅ |
| Accuracy | ≥ dbhub | 100.0% vs 100.0% (360 paired runs) | ✅ |
| Tool calls per task | fewer than dbhub | 1.6 vs 1.7 (360 paired runs) | ✅ |
| qh tool definitions | ≤ 800 tokens | not measured | — |
| Cold schema build, 1,000 tables | < 1 s | not measured | — |

## Results by dataset

| Dataset | Server | Accuracy | Avg total tokens | Avg input | Avg output | Avg tool calls | Avg wall (s) | Runs | Aborted | Errors | Truncated outputs |
|---|---|---|---|---|---|---|---|---|---|---|---|
| all | qh | 100% | 3091 | 2889 | 202 | 1.6 | 3.3 | 360 | 0 | 0 | 0 |
| all | dbhub | 100% | 5190 | 4936 | 254 | 1.7 | 3.5 | 360 | 0 | 0 | 0 |
| chinook | qh | 100% | 2725 | 2514 | 211 | 1.4 | 3.3 | 90 | 0 | 0 | 0 |
| chinook | dbhub | 100% | 3342 | 3106 | 236 | 1.3 | 3.2 | 90 | 0 | 0 | 0 |
| lendco | qh | 100% | 4962 | 4713 | 250 | 2.6 | 4.3 | 90 | 0 | 0 | 0 |
| lendco | dbhub | 100% | 11638 | 11235 | 403 | 3.2 | 5.1 | 90 | 0 | 0 | 0 |
| pagila | qh | 100% | 2377 | 2201 | 176 | 1.2 | 2.8 | 90 | 0 | 0 | 0 |
| pagila | dbhub | 100% | 2891 | 2711 | 180 | 1.1 | 2.7 | 90 | 0 | 0 | 0 |
| sakila | qh | 100% | 2300 | 2129 | 171 | 1.1 | 2.9 | 90 | 0 | 0 | 0 |
| sakila | dbhub | 100% | 2888 | 2693 | 195 | 1.1 | 2.9 | 90 | 0 | 0 | 0 |

## Per question

| Question | Kind | qh correct | dbhub correct | qh avg tokens | dbhub avg tokens |
|---|---|---|---|---|---|
| chinook-01 | lookup | 3/3 | 3/3 | 2786 | 2968 |
| chinook-02 | lookup | 3/3 | 3/3 | 2142 | 3840 |
| chinook-03 | lookup | 3/3 | 3/3 | 2047 | 3903 |
| chinook-04 | lookup | 3/3 | 3/3 | 2478 | 4916 |
| chinook-05 | lookup | 3/3 | 3/3 | 3162 | 4856 |
| chinook-06 | join | 3/3 | 3/3 | 2154 | 2690 |
| chinook-07 | join | 3/3 | 3/3 | 2108 | 2744 |
| chinook-08 | join | 3/3 | 3/3 | 2171 | 2812 |
| chinook-09 | join | 3/3 | 3/3 | 2382 | 3173 |
| chinook-10 | join | 3/3 | 3/3 | 2263 | 2824 |
| chinook-11 | join | 3/3 | 3/3 | 2192 | 2840 |
| chinook-12 | join | 3/3 | 3/3 | 4570 | 2704 |
| chinook-13 | join | 3/3 | 3/3 | 2221 | 2886 |
| chinook-14 | join | 3/3 | 3/3 | 2216 | 2739 |
| chinook-15 | join | 3/3 | 3/3 | 2276 | 5938 |
| chinook-16 | enum | 3/3 | 3/3 | 2124 | 2958 |
| chinook-17 | enum | 3/3 | 3/3 | 2094 | 2742 |
| chinook-18 | enum | 3/3 | 3/3 | 3426 | 4573 |
| chinook-19 | enum | 3/3 | 3/3 | 2222 | 2852 |
| chinook-20 | aggregate | 3/3 | 3/3 | 2076 | 2567 |
| chinook-21 | aggregate | 3/3 | 3/3 | 3213 | 3815 |
| chinook-22 | aggregate | 3/3 | 3/3 | 2149 | 2774 |
| chinook-23 | aggregate | 3/3 | 3/3 | 5936 | 3062 |
| chinook-24 | aggregate | 3/3 | 3/3 | 2052 | 2551 |
| chinook-25 | aggregate | 3/3 | 3/3 | 4405 | 3635 |
| chinook-26 | aggregate | 3/3 | 3/3 | 2869 | 2748 |
| chinook-27 | aggregate | 3/3 | 3/3 | 2397 | 3487 |
| chinook-28 | large | 3/3 | 3/3 | 2129 | 2600 |
| chinook-29 | large | 3/3 | 3/3 | 3677 | 2968 |
| chinook-30 | misspell | 3/3 | 3/3 | 3820 | 5083 |
| lendco-01 | lookup | 3/3 | 3/3 | 4260 | 10379 |
| lendco-02 | lookup | 3/3 | 3/3 | 3471 | 7121 |
| lendco-03 | lookup | 3/3 | 3/3 | 3850 | 9061 |
| lendco-04 | lookup | 3/3 | 3/3 | 3249 | 11533 |
| lendco-05 | join | 3/3 | 3/3 | 5831 | 13296 |
| lendco-06 | join | 3/3 | 3/3 | 9604 | 19911 |
| lendco-07 | join | 3/3 | 3/3 | 5761 | 12347 |
| lendco-08 | join | 3/3 | 3/3 | 4374 | 11379 |
| lendco-09 | join | 3/3 | 3/3 | 4470 | 12416 |
| lendco-10 | join | 3/3 | 3/3 | 5702 | 10920 |
| lendco-11 | join | 3/3 | 3/3 | 7968 | 13424 |
| lendco-12 | join | 3/3 | 3/3 | 4366 | 12309 |
| lendco-13 | join | 3/3 | 3/3 | 4449 | 9323 |
| lendco-14 | join | 3/3 | 3/3 | 5717 | 10920 |
| lendco-15 | join | 3/3 | 3/3 | 4471 | 12994 |
| lendco-16 | enum | 3/3 | 3/3 | 5446 | 9619 |
| lendco-17 | enum | 3/3 | 3/3 | 5595 | 10422 |
| lendco-18 | enum | 3/3 | 3/3 | 4533 | 12547 |
| lendco-19 | enum | 3/3 | 3/3 | 5939 | 9257 |
| lendco-20 | aggregate | 3/3 | 3/3 | 4337 | 11751 |
| lendco-21 | aggregate | 3/3 | 3/3 | 3346 | 9607 |
| lendco-22 | aggregate | 3/3 | 3/3 | 7955 | 16117 |
| lendco-23 | aggregate | 3/3 | 3/3 | 4602 | 12773 |
| lendco-24 | aggregate | 3/3 | 3/3 | 5039 | 12561 |
| lendco-25 | aggregate | 3/3 | 3/3 | 4101 | 8019 |
| lendco-26 | aggregate | 3/3 | 3/3 | 4471 | 18703 |
| lendco-27 | large | 3/3 | 3/3 | 3844 | 9683 |
| lendco-28 | large | 3/3 | 3/3 | 4444 | 15262 |
| lendco-29 | misspell | 3/3 | 3/3 | 3658 | 5765 |
| lendco-30 | misspell | 3/3 | 3/3 | 4015 | 9714 |
| pagila-01 | lookup | 3/3 | 3/3 | 2011 | 2456 |
| pagila-02 | lookup | 3/3 | 3/3 | 2151 | 2625 |
| pagila-03 | lookup | 3/3 | 3/3 | 2100 | 2588 |
| pagila-04 | lookup | 3/3 | 3/3 | 2472 | 2514 |
| pagila-05 | join | 3/3 | 3/3 | 2129 | 2607 |
| pagila-06 | join | 3/3 | 3/3 | 2679 | 4234 |
| pagila-07 | join | 3/3 | 3/3 | 2059 | 2526 |
| pagila-08 | join | 3/3 | 3/3 | 2172 | 2728 |
| pagila-09 | join | 3/3 | 3/3 | 2273 | 2928 |
| pagila-10 | join | 3/3 | 3/3 | 2243 | 2728 |
| pagila-11 | join | 3/3 | 3/3 | 3248 | 2578 |
| pagila-12 | join | 3/3 | 3/3 | 2149 | 2633 |
| pagila-13 | join | 3/3 | 3/3 | 2205 | 2744 |
| pagila-14 | join | 3/3 | 3/3 | 2169 | 2663 |
| pagila-15 | join | 3/3 | 3/3 | 2231 | 2783 |
| pagila-16 | enum | 3/3 | 3/3 | 2037 | 2493 |
| pagila-17 | enum | 3/3 | 3/3 | 2095 | 2581 |
| pagila-18 | enum | 3/3 | 3/3 | 2113 | 2599 |
| pagila-19 | enum | 3/3 | 3/3 | 2109 | 2644 |
| pagila-20 | aggregate | 3/3 | 3/3 | 2060 | 2575 |
| pagila-21 | aggregate | 3/3 | 3/3 | 2043 | 2495 |
| pagila-22 | aggregate | 3/3 | 3/3 | 2201 | 2729 |
| pagila-23 | aggregate | 3/3 | 3/3 | 2291 | 2849 |
| pagila-24 | aggregate | 3/3 | 3/3 | 2185 | 2705 |
| pagila-25 | aggregate | 3/3 | 3/3 | 2327 | 2941 |
| pagila-26 | aggregate | 3/3 | 3/3 | 2119 | 2650 |
| pagila-27 | large | 3/3 | 3/3 | 3307 | 3367 |
| pagila-28 | large | 3/3 | 3/3 | 2229 | 2897 |
| pagila-29 | misspell | 3/3 | 3/3 | 3718 | 5095 |
| pagila-30 | misspell | 3/3 | 3/3 | 4177 | 4775 |
| sakila-01 | lookup | 3/3 | 3/3 | 2014 | 2473 |
| sakila-02 | lookup | 3/3 | 3/3 | 2129 | 2571 |
| sakila-03 | lookup | 3/3 | 3/3 | 2091 | 2592 |
| sakila-04 | lookup | 3/3 | 3/3 | 2048 | 2528 |
| sakila-05 | join | 3/3 | 3/3 | 2140 | 2660 |
| sakila-06 | join | 3/3 | 3/3 | 2705 | 3717 |
| sakila-07 | join | 3/3 | 3/3 | 2076 | 2614 |
| sakila-08 | join | 3/3 | 3/3 | 2142 | 2745 |
| sakila-09 | join | 3/3 | 3/3 | 2291 | 3022 |
| sakila-10 | join | 3/3 | 3/3 | 2206 | 2804 |
| sakila-11 | join | 3/3 | 3/3 | 2134 | 2673 |
| sakila-12 | join | 3/3 | 3/3 | 2152 | 2646 |
| sakila-13 | join | 3/3 | 3/3 | 2204 | 2805 |
| sakila-14 | join | 3/3 | 3/3 | 2152 | 2687 |
| sakila-15 | join | 3/3 | 3/3 | 2191 | 2790 |
| sakila-16 | enum | 3/3 | 3/3 | 2036 | 2513 |
| sakila-17 | enum | 3/3 | 3/3 | 2104 | 2587 |
| sakila-18 | enum | 3/3 | 3/3 | 2126 | 2638 |
| sakila-19 | enum | 3/3 | 3/3 | 2104 | 2705 |
| sakila-20 | aggregate | 3/3 | 3/3 | 2069 | 2542 |
| sakila-21 | aggregate | 3/3 | 3/3 | 2046 | 2530 |
| sakila-22 | aggregate | 3/3 | 3/3 | 2213 | 2793 |
| sakila-23 | aggregate | 3/3 | 3/3 | 2298 | 2981 |
| sakila-24 | aggregate | 3/3 | 3/3 | 2196 | 2787 |
| sakila-25 | aggregate | 3/3 | 3/3 | 2327 | 2967 |
| sakila-26 | aggregate | 3/3 | 3/3 | 2139 | 2653 |
| sakila-27 | large | 3/3 | 3/3 | 2054 | 2493 |
| sakila-28 | large | 3/3 | 3/3 | 2263 | 2906 |
| sakila-29 | misspell | 3/3 | 3/3 | 3808 | 4580 |
| sakila-30 | misspell | 3/3 | 3/3 | 4537 | 5624 |

Method: tool-definition tokens are `count_tokens(noop tool + server tools, server instructions as the system prompt) − count_tokens(noop tool)`, so they include the instructions the server sends at initialization. Total tokens are input (including cache reads and writes) plus output, summed over every model turn of a task.
