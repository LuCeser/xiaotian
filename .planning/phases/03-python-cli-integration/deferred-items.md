# Phase 03 — Deferred Items (out-of-scope discoveries)

Discovered while executing Phase 03 plans. Logged per the executor scope-boundary
rule: pre-existing failures in unrelated packages are NOT auto-fixed.

## Item 1: pkg/ws TestHubStopsOnContextCancel flakiness under repo-wide `-short`

- **Discovered during:** Plan 03-01 Task 1 verification (`go test -short ./...`)
- **Package:** `pkg/ws` (Phase 2 deliverable — not touched by Phase 03)
- **Symptom:** When the whole repo is tested with `go test -short ./...`,
  `TestHubStopsOnContextCancel` occasionally fails with
  "Expected goroutine count to increase after starting hub.Run". Passes
  consistently when run in isolation (`go test -run TestHubStopsOnContextCancel ./pkg/ws/`).
- **Likely cause:** Goroutine-count assertion is timing-sensitive; under
  parallel test load the goroutine scheduler lets hub.Run spin up slower than
  the test's polling window.
- **Action:** Out of scope for Phase 03. Carry forward to Phase 02 maintenance
  or the next repo-wide test-stabilisation pass. Not a PythonConfig regression —
  the Phase 03 Plan 01 changes touch only `pkg/infra/*` and `config.example.yaml`.
