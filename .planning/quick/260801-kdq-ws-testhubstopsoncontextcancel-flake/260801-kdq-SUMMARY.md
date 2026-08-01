---
phase: quick
plan: 260801-kdq
subsystem: ws
tags: [ws, test-flake, test-stability, hub]
dependency_graph:
  requires: []
  provides:
    - "Deterministic TestHubStopsOnContextCancel (no runtime.NumGoroutine dependency)"
  affects:
    - pkg/ws/hub_test.go
tech_stack:
  added: []
  patterns:
    - "Behaviour-based polling assertions over scheduler-sensitive goroutine counts"
key_files:
  created: []
  modified:
    - pkg/ws/hub_test.go
decisions:
  - "Reuse the existing package-level waitFor poller instead of adding a colliding helper"
  - "Run flake verification without -race because the env has no C compiler; flake was scheduler-timing, not a data race"
metrics:
  duration: 322s
  completed: 2026-08-01
  tasks: 1
  files: 1
requirements: [WS-CANCEL-STABILITY]
status: complete
---

# Quick 260801-kdq: Root-fix TestHubStopsOnContextCancel flake Summary

Rewrote `TestHubStopsOnContextCancel` to prove hub.Run lifecycle via deterministic functional signals (Clients()==1, hub.closed, Register()==false) instead of scheduler-fragile `runtime.NumGoroutine()` counting.

## What Changed

Single file: `pkg/ws/hub_test.go` (+58 / -65).

1. **`TestHubStopsOnContextCancel` rewritten.** All `runtime.NumGoroutine` calls and the baseline-goroutine logic removed. The test now proves three things deterministically:
   - **Liveness:** register a zero-value `probe := &Client{}` (safe — the Hub accept path stores it only as a map key, never dereferences it), assert `Register` returns true, then poll `hub.Clients() == 1`. Only Run's select-loop register branch increments the count, so this is functional proof Run is consuming its register channel.
   - **Exit:** `cancel()`, then poll `hub.closed == true`. `Close` is deferred inside `Run`, so `closed==true` deterministically proves Run has returned (its goroutine exited — no CR-01 leak).
   - **Shutdown:** `hub.Register(&Client{})` returns false on the closed hub.

2. **Dead helpers deleted.** `waitForGoroutineIncrease` and `waitForGoroutineDrain` (and their doc comments) removed — zero references remain.

3. **`runtime` import removed** (was only used by the deleted helpers; now referenced only in explanatory doc comments).

4. **`gofmt -s -w`** applied; the `-s` pass also normalised struct-field colon alignment in the other hub tests for consistency.

## Deviations from Plan

### Auto-fixed Issues

**1. [Rule 3 - Blocking] Reused existing `waitFor` instead of adding a new colliding helper**
- **Found during:** Task 1, on first `go vet`.
- **Issue:** The plan called for adding a new `waitFor(t, timeout, cond, msg)` helper. A package-level `waitFor(t, timeout, msg string, cond func() bool) bool` already exists in `connection_test.go:20` (used by the integration tests for the same deterministic-poll purpose). Adding a second `waitFor` caused a redeclaration error.
- **Fix:** Deleted the newly-added helper and reused the existing one. Adapted the two call sites to the existing signature `(t, timeout, msg, cond) bool` with an explicit `t.Fatalf` on timeout (the existing helper returns bool rather than fatalling). The existing helper polls with a 2ms sleep instead of `runtime.Gosched`; both are valid deterministic polls, and reusing it keeps the package consistent.
- **Files modified:** pkg/ws/hub_test.go
- **Commit:** b65ad5e
- **Net effect on plan intent:** Preserved — the test still asserts on behaviour-based conditions, not goroutine counts. No new helper duplication.

**2. [Rule 3 - Blocking / environment] Flake verification run without `-race`**
- **Found during:** Task 1, verification step.
- **Issue:** `-race` requires cgo, but the environment has no C compiler (`gcc` not found, no mingw/TDM installed) and `CGO_ENABLED=0` is the project default. The plan's verification specified `-count=50 -race`.
- **Fix / rationale:** The original flake (260801-fh3) was a scheduler-timing flake in `NumGoroutine` counting — NOT a data race. It manifested and was observed without `-race`. Therefore 50 isolated iterations (no `-race`) plus 3 full-suite rounds (no `-race`) are valid proof that this specific flake is gone. The `-race` flag is belt-and-suspenders for data races, which were never the failure mode here. Installing a C compiler toolchain was deemed out of scope (heavy, environment-altering) and not auto-fixable.
- **Verification actually run:** `go test ./pkg/ws/ -run '^TestHubStopsOnContextCancel$' -count=50` (50/50 pass) and `go test ./... -count=3` (all green).

## Verification Results

```
=== go vet ./pkg/ws/ ===
VET-OK

=== gofmt -s -l pkg/ws/hub_test.go ===
GOFMT-OK (no output)

=== go build ./... ===
BUILD-OK

=== isolated: go test ./pkg/ws/ -run '^TestHubStopsOnContextCancel$' -count=50 ===
--- PASS: TestHubStopsOnContextCancel (0.00s)   [x50]
PASS
ok  	github.com/xiaotian/home-tutor-agent/pkg/ws	0.802s
EXIT=0     → 50/50 PASS

=== full-suite load (original flake trigger): go test ./... -count=3 ===
ok  	github.com/xiaotian/home-tutor-agent/cmd/service	4.389s
?    	github.com/xiaotian/home-tutor-agent/internal/wstest	[no test files]
?    	github.com/xiaotian/home-tutor-agent/pkg/crc16	[no test files]
ok  	github.com/xiaotian/home-tutor-agent/pkg/infra	2.715s
ok  	github.com/xiaotian/home-tutor-agent/pkg/pybridge	0.783s
ok  	github.com/xiaotian/home-tutor-agent/pkg/ws	16.108s
EXIT=0     → 3/3 rounds all green, including pkg/ws (heaviest)
```

Done-criteria greps:
- `runtime.NumGoroutine` as a **call** in hub_test.go: none (only in explanatory comments).
- `waitForGoroutineIncrease` / `waitForGoroutineDrain`: no references anywhere.
- `hub.go` and `integration_test.go` in `git diff`: unmodified (empty diff).

## Flake Conclusion

Root-fixed, not masked. The previous fix (260801-fh3, 500ms→2s timeout) only widened the timing window; this rewrite removes the scheduler-dependent assertion entirely. `hub.Clients()` and `hub.closed` are deterministic state readouts guarded by mutexes — they cannot flake on OS scheduling. 50 isolated runs + 3 full-suite rounds (the exact condition that originally triggered the flake) pass clean.

## Known Stubs

None.

## Self-Check: PASSED

- `pkg/ws/hub_test.go`: FOUND (modified, committed)
- commit `b65ad5e`: FOUND in `git log`
