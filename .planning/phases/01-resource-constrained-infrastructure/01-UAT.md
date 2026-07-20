---
status: testing
phase: 01-resource-constrained-infrastructure
source: [01-VERIFICATION.md]
started: 2026-07-20T23:56:57Z
updated: 2026-07-20T23:56:57Z
---

## Current Test

number: 1
name: Memory budget holds ≤150MB under real concurrent load (RES-01 "under load")
expected: |
  Run bin/service on the production-form target (real 1C/1G VPS or cgroups-limited CI env).
  Ramp to 100 concurrent sessions, sample hta_memory_used_bytes every 5s for 5+ minutes.
  Confirm memory never exceeds 150MB, and as usage approaches 95% the hta_load_shedding
  gauge flips to 1 and /health returns HTTP 503.
awaiting: user response

## Tests

### Auto-verified (no action needed)

#### A1. Go module / skeleton (01-01)
expected: Module, layered dirs, Makefile, golangci-lint, CI workflow, pre-commit hooks, README, clean build.
result: pass
source: automated (8/8 checks — see prior UAT record below)

#### A2. 100-session limit + backpressure (RES-03)
expected: 100 registrations succeed; 101st returns ErrTooManyConnections; TokenBucket throttles per connection.
result: pass
source: automated — TestSessionLimit (pkg/infra/connection_test.go), TestTokenBucket

#### A3. Drop-oldest buffer overflow, no crash (RES-04)
expected: Overflow Put drops oldest buffer, TotalDrops increments, no panic; proven under concurrency.
result: pass
source: automated — TestDropOldestStrategy, TestBufferPoolConcurrent (100 goroutines × 1000 ops)

#### A4. Structured logging with levels (ERR-04)
expected: JSON output with msg/level/ts + custom fields; Debug/Info/Warn/Error levels correct; component tags.
result: pass
source: automated — TestStructuredLogging, TestLogLevels (pkg/infra/logging_test.go)

### Human verification required

### 1. Memory budget ≤150MB under real concurrent load (RES-01 "under load")
expected: On a 1C/1G target (or cgroups-limited env), ramp bin/service to 100 concurrent sessions.
Sample hta_memory_used_bytes every 5s for 5+ minutes. Memory stays ≤150MB; near 95% usage the
load-shed flag trips (hta_load_shedding → 1) and /health returns 503. Idle memory ~7% (~10.5MB)
already proven by TestMainMemoryUnder150MBAtIdle.
result: [pending]

## Summary

total: 5
passed: 4
issues: 0
pending: 1
skipped: 0
blocked: 0

## Gaps

- RES-01 "under load": mechanism fully implemented and unit-tested (MemoryMonitor → LoadShedder →
  Server.loadShedding → /health 503), but no test drives real concurrent load through the assembled
  Server on target hardware. Requires a load test on a 1C/1G VPS or cgroups-limited CI runner.

---

## Prior UAT record (01-01 skeleton — preserved)

The original 01-UAT.md covered plan 01-01's 8 build/tooling checks (module init, directory layout,
Makefile, golangci-lint, CI workflow, pre-commit hooks, README, clean build) — all passed (8/8).
Those results are subsumed by test A1 above.
