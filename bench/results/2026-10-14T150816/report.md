# qh vs toolbox benchmark — 2026-10-14

Model `claude-sonnet-5-5` (effort high) · 3 run(s) per question · toolbox 1.14.0 · tool output capped at 100,000 bytes for both servers.

## Success criteria (spec §1)

| Criterion | Target | Result | Pass |
|---|---|---|---|
| Fewer total tokens per task | ≥ 40% fewer than toolbox | 83.6% fewer (360 paired runs) | ✅ |
| Accuracy | ≥ toolbox | 100.0% vs 100.0% (360 paired runs) | ✅ |
| Tool calls per task | fewer than toolbox | 1.6 vs 2.2 (360 paired runs) | ✅ |
| qh tool definitions | ≤ 800 tokens | 392 tokens | ✅ |
| Cold schema build, 1,000 tables | < 1 s | 0.10 s | ✅ |

## Results by dataset

| Dataset | Server | Accuracy | Avg total tokens | Avg input | Avg output | Avg tool calls | Avg wall (s) | Runs | Aborted | Errors | Truncated outputs |
|---|---|---|---|---|---|---|---|---|---|---|---|
| all | qh | 100% | 3118 | 2916 | 202 | 1.6 | 4.0 | 360 | 0 | 0 | 0 |
| all | toolbox | 100% | 19067 | 18812 | 254 | 2.2 | 4.8 | 360 | 0 | 0 | 0 |
| chinook | qh | 100% | 2818 | 2603 | 215 | 1.4 | 4.7 | 90 | 0 | 0 | 0 |
| chinook | toolbox | 100% | 11012 | 10739 | 272 | 2.2 | 5.0 | 90 | 0 | 0 | 0 |
| lendco | qh | 100% | 4977 | 4730 | 247 | 2.6 | 5.0 | 90 | 0 | 0 | 0 |
| lendco | toolbox | 100% | 35604 | 35301 | 304 | 3.0 | 6.0 | 90 | 0 | 0 | 0 |
| pagila | qh | 100% | 2396 | 2220 | 176 | 1.2 | 3.4 | 90 | 0 | 0 | 0 |
| pagila | toolbox | 100% | 21200 | 20970 | 230 | 1.8 | 3.8 | 90 | 0 | 0 | 0 |
| sakila | qh | 100% | 2279 | 2110 | 170 | 1.1 | 3.0 | 90 | 0 | 0 | 0 |
| sakila | toolbox | 100% | 8451 | 8240 | 211 | 1.6 | 4.1 | 90 | 0 | 0 | 0 |

## Tool-definition tokens by number of connections

| Connections | qh | toolbox |
|---|---|---|
| 1 | 392 | 6663 |
| 2 | 428 | — |
| 3 | 466 | — |
| 4 | 504 | — |
| 5 | 542 | — |

## Micro-benchmarks

| | qh | toolbox |
|---|---|---|
| Cold schema, 1,000 tables (median of 3) | 99.4 ms | 28.4 ms |
| Query latency p50 | 0.6 ms | 0.4 ms |
| Query latency p95 | 2.8 ms | 2.9 ms |

## Per question

| Question | Kind | qh correct | toolbox correct | qh avg tokens | toolbox avg tokens |
|---|---|---|---|---|---|
| chinook-01 | lookup | 3/3 | 3/3 | 2404 | 3210 |
| chinook-02 | lookup | 3/3 | 3/3 | 2142 | 3368 |
| chinook-03 | lookup | 3/3 | 3/3 | 2435 | 16744 |
| chinook-04 | lookup | 3/3 | 3/3 | 2879 | 3699 |
| chinook-05 | lookup | 3/3 | 3/3 | 3162 | 16644 |
| chinook-06 | join | 3/3 | 3/3 | 2154 | 8542 |
| chinook-07 | join | 3/3 | 3/3 | 2108 | 3393 |
| chinook-08 | join | 3/3 | 3/3 | 2187 | 16780 |
| chinook-09 | join | 3/3 | 3/3 | 3719 | 5726 |
| chinook-10 | join | 3/3 | 3/3 | 2263 | 13206 |
| chinook-11 | join | 3/3 | 3/3 | 2192 | 16813 |
| chinook-12 | join | 3/3 | 3/3 | 4568 | 12587 |
| chinook-13 | join | 3/3 | 3/3 | 2221 | 17218 |
| chinook-14 | join | 3/3 | 3/3 | 2216 | 4494 |
| chinook-15 | join | 3/3 | 3/3 | 2320 | 16960 |
| chinook-16 | enum | 3/3 | 3/3 | 2124 | 5055 |
| chinook-17 | enum | 3/3 | 3/3 | 2094 | 5703 |
| chinook-18 | enum | 3/3 | 3/3 | 3389 | 7232 |
| chinook-19 | enum | 3/3 | 3/3 | 2214 | 5814 |
| chinook-20 | aggregate | 3/3 | 3/3 | 2089 | 3360 |
| chinook-21 | aggregate | 3/3 | 3/3 | 3213 | 4423 |
| chinook-22 | aggregate | 3/3 | 3/3 | 2149 | 16795 |
| chinook-23 | aggregate | 3/3 | 3/3 | 5933 | 17160 |
| chinook-24 | aggregate | 3/3 | 3/3 | 2052 | 3260 |
| chinook-25 | aggregate | 3/3 | 3/3 | 4394 | 17428 |
| chinook-26 | aggregate | 3/3 | 3/3 | 3523 | 16819 |
| chinook-27 | aggregate | 3/3 | 3/3 | 2796 | 17359 |
| chinook-28 | large | 3/3 | 3/3 | 2129 | 16778 |
| chinook-29 | large | 3/3 | 3/3 | 3664 | 17051 |
| chinook-30 | misspell | 3/3 | 3/3 | 3801 | 16723 |
| lendco-01 | lookup | 3/3 | 3/3 | 4260 | 32956 |
| lendco-02 | lookup | 3/3 | 3/3 | 3471 | 32912 |
| lendco-03 | lookup | 3/3 | 3/3 | 5273 | 29838 |
| lendco-04 | lookup | 3/3 | 3/3 | 3257 | 31776 |
| lendco-05 | join | 3/3 | 3/3 | 5831 | 46399 |
| lendco-06 | join | 3/3 | 3/3 | 6949 | 36738 |
| lendco-07 | join | 3/3 | 3/3 | 4885 | 35242 |
| lendco-08 | join | 3/3 | 3/3 | 4374 | 35319 |
| lendco-09 | join | 3/3 | 3/3 | 4472 | 33386 |
| lendco-10 | join | 3/3 | 3/3 | 5702 | 36629 |
| lendco-11 | join | 3/3 | 3/3 | 7887 | 36508 |
| lendco-12 | join | 3/3 | 3/3 | 4366 | 38671 |
| lendco-13 | join | 3/3 | 3/3 | 4460 | 35215 |
| lendco-14 | join | 3/3 | 3/3 | 5583 | 35394 |
| lendco-15 | join | 3/3 | 3/3 | 4467 | 37465 |
| lendco-16 | enum | 3/3 | 3/3 | 5446 | 33003 |
| lendco-17 | enum | 3/3 | 3/3 | 5582 | 36791 |
| lendco-18 | enum | 3/3 | 3/3 | 4434 | 35346 |
| lendco-19 | enum | 3/3 | 3/3 | 5939 | 33078 |
| lendco-20 | aggregate | 3/3 | 3/3 | 4758 | 39694 |
| lendco-21 | aggregate | 3/3 | 3/3 | 3346 | 33045 |
| lendco-22 | aggregate | 3/3 | 3/3 | 7955 | 36537 |
| lendco-23 | aggregate | 3/3 | 3/3 | 4821 | 37467 |
| lendco-24 | aggregate | 3/3 | 3/3 | 5086 | 44442 |
| lendco-25 | aggregate | 3/3 | 3/3 | 5854 | 33419 |
| lendco-26 | aggregate | 3/3 | 3/3 | 4450 | 33203 |
| lendco-27 | large | 3/3 | 3/3 | 4282 | 33042 |
| lendco-28 | large | 3/3 | 3/3 | 4444 | 37498 |
| lendco-29 | misspell | 3/3 | 3/3 | 3659 | 31899 |
| lendco-30 | misspell | 3/3 | 3/3 | 4025 | 35217 |
| pagila-01 | lookup | 3/3 | 3/3 | 2011 | 23452 |
| pagila-02 | lookup | 3/3 | 3/3 | 2145 | 19686 |
| pagila-03 | lookup | 3/3 | 3/3 | 2091 | 14623 |
| pagila-04 | lookup | 3/3 | 3/3 | 2053 | 22072 |
| pagila-05 | join | 3/3 | 3/3 | 2119 | 14639 |
| pagila-06 | join | 3/3 | 3/3 | 2683 | 15362 |
| pagila-07 | join | 3/3 | 3/3 | 2059 | 22224 |
| pagila-08 | join | 3/3 | 3/3 | 2178 | 27549 |
| pagila-09 | join | 3/3 | 3/3 | 2279 | 22429 |
| pagila-10 | join | 3/3 | 3/3 | 2211 | 14757 |
| pagila-11 | join | 3/3 | 3/3 | 3462 | 24533 |
| pagila-12 | join | 3/3 | 3/3 | 2149 | 14681 |
| pagila-13 | join | 3/3 | 3/3 | 2205 | 22325 |
| pagila-14 | join | 3/3 | 3/3 | 2169 | 14755 |
| pagila-15 | join | 3/3 | 3/3 | 2231 | 14768 |
| pagila-16 | enum | 3/3 | 3/3 | 2037 | 22099 |
| pagila-17 | enum | 3/3 | 3/3 | 2093 | 22134 |
| pagila-18 | enum | 3/3 | 3/3 | 2948 | 22252 |
| pagila-19 | enum | 3/3 | 3/3 | 2109 | 22157 |
| pagila-20 | aggregate | 3/3 | 3/3 | 2057 | 14599 |
| pagila-21 | aggregate | 3/3 | 3/3 | 2043 | 22067 |
| pagila-22 | aggregate | 3/3 | 3/3 | 2201 | 23681 |
| pagila-23 | aggregate | 3/3 | 3/3 | 2273 | 23768 |
| pagila-24 | aggregate | 3/3 | 3/3 | 2185 | 23639 |
| pagila-25 | aggregate | 3/3 | 3/3 | 2327 | 22408 |
| pagila-26 | aggregate | 3/3 | 3/3 | 2119 | 23655 |
| pagila-27 | large | 3/3 | 3/3 | 3315 | 27282 |
| pagila-28 | large | 3/3 | 3/3 | 2229 | 22405 |
| pagila-29 | misspell | 3/3 | 3/3 | 3712 | 17884 |
| pagila-30 | misspell | 3/3 | 3/3 | 4177 | 38106 |
| sakila-01 | lookup | 3/3 | 3/3 | 2014 | 9893 |
| sakila-02 | lookup | 3/3 | 3/3 | 2118 | 5764 |
| sakila-03 | lookup | 3/3 | 3/3 | 2091 | 5764 |
| sakila-04 | lookup | 3/3 | 3/3 | 2048 | 8808 |
| sakila-05 | join | 3/3 | 3/3 | 2125 | 5803 |
| sakila-06 | join | 3/3 | 3/3 | 2705 | 6480 |
| sakila-07 | join | 3/3 | 3/3 | 2076 | 5808 |
| sakila-08 | join | 3/3 | 3/3 | 2142 | 10031 |
| sakila-09 | join | 3/3 | 3/3 | 2290 | 10220 |
| sakila-10 | join | 3/3 | 3/3 | 2213 | 5889 |
| sakila-11 | join | 3/3 | 3/3 | 2136 | 9948 |
| sakila-12 | join | 3/3 | 3/3 | 2152 | 5818 |
| sakila-13 | join | 3/3 | 3/3 | 2204 | 9057 |
| sakila-14 | join | 3/3 | 3/3 | 2152 | 5830 |
| sakila-15 | join | 3/3 | 3/3 | 2196 | 5943 |
| sakila-16 | enum | 3/3 | 3/3 | 2036 | 8770 |
| sakila-17 | enum | 3/3 | 3/3 | 2104 | 8850 |
| sakila-18 | enum | 3/3 | 3/3 | 2133 | 9022 |
| sakila-19 | enum | 3/3 | 3/3 | 2104 | 5840 |
| sakila-20 | aggregate | 3/3 | 3/3 | 2069 | 5737 |
| sakila-21 | aggregate | 3/3 | 3/3 | 2046 | 5732 |
| sakila-22 | aggregate | 3/3 | 3/3 | 2201 | 10102 |
| sakila-23 | aggregate | 3/3 | 3/3 | 2335 | 10158 |
| sakila-24 | aggregate | 3/3 | 3/3 | 2196 | 10059 |
| sakila-25 | aggregate | 3/3 | 3/3 | 2327 | 6050 |
| sakila-26 | aggregate | 3/3 | 3/3 | 2139 | 10034 |
| sakila-27 | large | 3/3 | 3/3 | 2038 | 9934 |
| sakila-28 | large | 3/3 | 3/3 | 2265 | 9088 |
| sakila-29 | misspell | 3/3 | 3/3 | 3806 | 16281 |
| sakila-30 | misspell | 3/3 | 3/3 | 3912 | 16807 |

## Notes

- Toolbox's prebuilt configuration serves one database per process and its tools depend on the engine; tool tokens are measured with one Postgres connection (Pagila). Its MySQL and SQLite tool sets are smaller.
- Cold schema: qh's overview lists the first 200 of 1,000 tables with a pattern hint (it builds the full catalogue); Toolbox's list_tables (output_format simple) lists all 1,000 names. Toolbox connects when it starts, so its figure excludes connecting; qh's includes it.
- Cold schema is the first schema call after process start; it includes any database connection the server makes lazily (qh connects on first use).

Method: tool-definition tokens are `count_tokens(noop tool + server tools, server instructions as the system prompt) − count_tokens(noop tool)`, so they include the instructions the server sends at initialization. Total tokens are input (including cache reads and writes) plus output, summed over every model turn of a task.
