---
phase: quick
plan: 260801-kdq
type: execute
wave: 1
depends_on: []
files_modified:
  - pkg/ws/hub_test.go
autonomous: true
requirements: [WS-CANCEL-STABILITY]
tags: [ws, test-flake, test-stability, hub]
must_haves:
  truths:
    - "TestHubStopsOnContextCancel proves hub.Run is live via a functional signal (Clients()==1 after Register), not runtime.NumGoroutine counting"
    - "TestHubStopsOnContextCancel proves hub.Run exited after cancel via hub.closed==true, deterministically observable regardless of OS scheduler timing"
    - "TestHubStopsOnContextCancel proves shutdown via Register returning false on a closed hub"
    - "No call to runtime.NumGoroutine remains in hub_test.go (integration_test.go is untouched and may still use it)"
    - "The test passes 50 consecutive runs with -race, proving the flake is eliminated"
  artifacts:
    - "pkg/ws/hub_test.go with rewritten TestHubStopsOnContextCancel and new waitFor helper"
  key_links:
    - "hub.closed flag (hub.go) observed by test → proves Run returned (defer Close only fires on return)"
    - "hub.Clients() count observed by test → proves Run's select loop is consuming the register channel"
    - "hub.Register() returning false → proves Close ran (closed flag short-circuits Register)"
---

<objective>
Root-fix the pkg/ws TestHubStopsOnContextCancel flake by replacing scheduler-fragile runtime.NumGoroutine() goroutine-count assertions with deterministic, behavior-based functional signals that are observable regardless of OS scheduler timing.

Purpose: The test intermittently failed under full-suite concurrent load on Windows because runtime.NumGoroutine() depends on OS scheduling — the count can momentarily dip/rise for reasons unrelated to hub.Run's actual lifecycle, causing false negatives (260801-fh3 raised the timeout 500ms→2s which only masked, not fixed, the flake). The verifier running the full suite still flakes.

The fix proves the same intent — "hub.Run goroutine started" and "hub.Run goroutine exited after cancel" — using signals that are deterministic:

1. **Run is live:** Register a probe client, observe hub.Clients()==1. Only Run's select-loop register branch can increment the client count, so a rising count is functional proof Run is consuming its register channel — not a scheduling artefact.

2. **Run exited after cancel:** Poll hub.closed. Close is deferred inside Run (hub.go line 111: `defer h.Close()`), so closed==true deterministically proves Run has returned, which means its goroutine has exited (no CR-01 leak). This is the same guarantee the goroutine count tried to prove, but without scheduler dependence.

3. **Shutdown is functional:** Register returns false on a closed hub — a behavioral assertion, not a timing one.

Output: Rewritten pkg/ws/hub_test.go (single file, test-only change; no production code touched).
</objective>

<execution_context>
@D:/workspace/ai/xiaotian/.claude/gsd-core/workflows/execute-plan.md
@D:/workspace/ai/xiaotian/.claude/gsd-core/templates/summary.md
</execution_context>

<context>
@.claude/CLAUDE.md
@pkg/ws/hub.go
@pkg/ws/hub_test.go
</context>

<tasks>

<task type="auto">
  <name>Task 1: Rewrite TestHubStopsOnContextCancel with deterministic functional signals, replace goroutine-count helpers with a poll helper</name>
  <files>pkg/ws/hub_test.go</files>

  <behavior>
    - TestHubStopsOnContextCancel: registering a probe client then polling hub.Clients()==1 succeeds within 2s (Run is consuming its register channel).
    - TestHubStopsOnContextCancel: after cancel(), polling hub.closed==true succeeds within 2s (deferred Close ran → Run returned → goroutine exited).
    - TestHubStopsOnContextCancel: after close, hub.Register(&Client{}) returns false (functional shutdown).
    - The test passes 50 consecutive iterations with -race enabled (no flake).
  </behavior>

  <action>
    Single-file edit to pkg/ws/hub_test.go. Three coordinated changes:

    **(A) Rewrite TestHubStopsOnContextCancel (currently lines 186-225).**

    Replace the entire function body with a version that uses no runtime.NumGoroutine calls. Structure:
    - Build HubConfig{Limit:100, RegisterBuffer:32, UnregisterBuffer:32}, NewHub, context.WithCancel.
    - `go hub.Run(ctx)`.
    - **Liveness proof (replaces the goroutine-count "started" assertion):** Create a bare probe `probe := &Client{}` (zero-value struct). It is safe to register a zero-value Client because the Hub's accept path only stores the pointer as a map key in `h.clients[probe] = true` (hub.go line 127) and never dereferences it — Close/drainUnregister do not iterate registered clients. Call `hub.Register(probe)` and assert it returns true (false would mean the hub is already closed or saturated). Then poll `hub.Clients() == 1` via the new waitFor helper with a 2s timeout — only Run's select-loop register branch increments the client count, so this is deterministic functional proof that Run is live and consuming the register channel.
    - **Exit proof (replaces the goroutine-count "drain" assertion):** Call `cancel()`. Run's ctx.Done branch (drainUnregister + return) fires, and the deferred `h.Close()` (hub.go line 111) flips hub.closed. Poll `hub.closed == true` via waitFor with a 2s timeout. Because Close is deferred inside Run, closed==true deterministically proves Run has returned — i.e. its goroutine exited (no CR-01 leak).
    - **Functional shutdown proof:** Assert `hub.Register(&Client{})` returns false — a closed hub short-circuits Register (hub.go lines 187-189).
    - Remove the `baseGoroutines := runtime.NumGoroutine()` capture and all goroutine-count comparison logic.

    **(B) Add the waitFor poll helper.**

    Add a new helper function `waitFor(t *testing.T, timeout time.Duration, cond func() bool, msg string)` near where the old helpers were (after the last test, around line 318). It must: call `t.Helper()`; compute `deadline := time.Now().Add(timeout)`; loop `for time.Now().Before(deadline)` calling `runtime.Gosched()` then `if cond() { return }`; on timeout call `t.Fatalf("%s (timed out after %v)", msg, timeout)`. Add a doc comment explaining it replaces the scheduler-fragile runtime.NumGoroutine polls (waitForGoroutineIncrease/Drain) with deterministic, behaviour-based conditions.

    **(C) Delete the two dead helpers.**

    Delete `waitForGoroutineIncrease` (currently lines 319-332) and `waitForGoroutineDrain` (currently lines 334-345) entirely, including their doc comments. After this rewrite they have zero references in the file. Do NOT touch integration_test.go — it has its own NumGoroutine usage at lines 477/489 that is out of scope.

    **Import hygiene:** The `runtime` import stays (waitFor uses runtime.Gosched). After editing, run `go vet ./pkg/ws/` — if it reports any now-unused import (none expected: context/runtime/sync/sync-atomic/testing/time/zap are all still referenced by other tests), remove it. Run `gofmt -s -w pkg/ws/hub_test.go` to normalize formatting.

    Do NOT modify hub.go or any production code. Do NOT modify integration_test.go.
  </action>

  <verify>
    <automated>
      cd /d/workspace/ai/xiaotian && go vet ./pkg/ws/ &&
      gofmt -s -l pkg/ws/hub_test.go &&
      go test ./pkg/ws/ -run '^TestHubStopsOnContextCancel$' -count=50 -race &&
      echo "FLAKE-CHECK: 50 iterations passed"
    </automated>
  </verify>

  <done>
    - TestHubStopsOnContextCancel contains zero runtime.NumGoroutine calls (grep -c 'NumGoroutine' in the function returns 0).
    - waitForGoroutineIncrease and waitForGoroutineDrain are deleted (grep across hub_test.go returns no matches).
    - A new waitFor helper exists and is the only poll mechanism used by the rewritten test.
    - `go vet ./pkg/ws/` is clean.
    - `gofmt -s -l pkg/ws/hub_test.go` produces no output (already formatted).
    - `go test ./pkg/ws/ -run '^TestHubStopsOnContextCancel$' -count=50 -race` passes all 50 iterations.
    - hub.go and integration_test.go are unmodified.
  </done>
</task>

</tasks>

<threat_model>
## Trust Boundaries

This plan modifies test code only (pkg/ws/hub_test.go). No production code, no external inputs, no new dependencies, no network/auth/data-store surfaces are introduced or altered. No trust boundary is crossed or modified.

## STRIDE Threat Register

| Threat ID | Category | Component | Severity | Disposition | Mitigation Plan |
|-----------|----------|-----------|----------|-------------|-----------------|
| T-quick-01 | Tampering | N/A — test-only change | low | accept | No production code is modified; the change cannot affect runtime security posture. The probe `&Client{}` zero-value is never sent real data and is never dereferenced by the Hub accept path (hub.go stores it as a map key only). |
</threat_model>

<verification>
- `go vet ./pkg/ws/` — clean (no unused imports, no structural issues).
- `gofmt -s -l pkg/ws/hub_test.go` — no output (idiomatic formatting).
- `go test ./pkg/ws/ -run '^TestHubStopsOnContextCancel$' -count=50 -race` — all 50 pass (deterministic; flake eliminated).
- `go test ./pkg/ws/ -race` — full package suite green (no collateral breakage from deleting helpers).
- Confirm via grep that hub.go and integration_test.go are unchanged in the working tree.
</verification>

<success_criteria>
- TestHubStopsOnContextCancel no longer relies on runtime.NumGoroutine() for any assertion.
- The test uses three deterministic functional signals: Clients()==1 (liveness), hub.closed==true (exit), Register()==false (shutdown).
- 50 consecutive -race iterations pass, demonstrating the scheduler-timing flake is root-fixed rather than masked.
- Only pkg/ws/hub_test.go is modified; hub.go and integration_test.go are untouched.
</success_criteria>

<output>
Create `.planning/quick/260801-kdq-ws-testhubstopsoncontextcancel-flake/260801-kdq-SUMMARY.md` when done.
</output>
