# Plan 5 (benchmark): deferred review findings

Findings from the per-task and final reviews of Plan 5 that were judged safe to defer. Items fixed before merge are not listed.

## Parked after the final review

- servers run sequentially per dataset (no interleaving); report lacks qh commit/image digests/Node version; preflight failure skips the micro phase (accepted)
- a run held past the 10-minute run timeout by 429 backoff is recorded as a failed "run timed out", not aborted (the SDK returns a bare DeadlineExceeded; the runWithTimeout branch and its fake-model test do not reflect real SDK behaviour) — Ruling: park; rare (needs >10 min of rate-limit backoff), visible through the per-server timeout counts in the report notes; fix by recording the last HTTP status via option.WithMiddleware and testing against an httptest 429 server

## Deferred minors by task

- **Task 1**: .part left on failed download; mismatch on cached file errors instead of re-downloading (plan-mandated); no download timeout/size cap; chinook URI by concatenation; Fetch/untar test gaps; unsynchronised hits counter in test
- **Task 2**: numeric-looking strings coerced ("007"==7, NaN never equal); other numeric/bool/time types handled loosely; large ints lose precision and 1e-6 relative tolerance passes off-by-one above ~1e6 (plan-mandated); compare tests don't exercise relative scale; LoadQuestions error discarded in test; chinook-25 inner join wording; pagila/sakila-25 has 10 tied films; no Unicode normalization
- **Task 5**: no test pinning the 1,000-table/200-cap behaviour; ignored isErr in TestMicroQH; ANALYZE on empty tables is a no-op
- **Task 6**: note wording "first 200"/"builds the full catalogue" (verified by Task 5 review against internal/format/schema.go); dbhub version printed when dbhub not run; micro/toolTokens untested without Docker; incremental-write test only checks counts; cap is bytes not characters in README; results/ not gitignored; latency compares capped qh vs uncapped dbhub (note); latency always uses Pagila
