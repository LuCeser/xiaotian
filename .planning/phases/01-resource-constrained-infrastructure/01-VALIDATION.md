---
phase: 01
slug: resource-constrained-infrastructure
# status lifecycle: draft (seeded by plan-phase) → validated (set by validate-phase §6)
# audit-milestone §5.5 distinguishes NOT-VALIDATED (draft) from PARTIAL (validated + nyquist_compliant: false) (#2117)
status: draft
# nyquist_compliant reflects whether the PLAN achieves Nyquist-rate feedback sampling.
# True here: every implementation task has `<automated>` verify + TDD tests; quick run < 30s.
# Execution-time compliance is re-asserted by validate-phase §6 before flipping status → validated.
nyquist_compliant: true
# wave_0_complete reflects whether the test-infrastructure prerequisite is planned.
# True here: Wave 0 reduces to `go test` framework availability, delivered by Plan 01 Task 1
# (go.mod init). No separate Wave 0 stub files are needed — every behavioral test is created
# TDD-style by the implementation plan that owns the behavior (see Per-Task Verification Map).
wave_0_complete: true
created: 2025-07-18
---

# Phase 01 — Validation Strategy

> Per-phase validation contract for feedback sampling during execution.

---

## Test Infrastructure

| Property | Value |
|----------|-------|
| **Framework** | go test (Go stdlib) + testify (assertions, added by Plan 02) |
| **Config file** | none — Go convention (`*_test.go` co-located with source) |
| **Quick run command** | `go test -v -short ./...` |
| **Full suite command** | `go test -v -race ./...` |
| **Estimated runtime** | ~30 seconds |

---

## Sampling Rate

- **After every task commit:** Run `go test -v -short ./...`
- **After every plan wave:** Run `go test -v -race ./...`
- **Before `/gsd-verify-work`:** Full suite must be green
- **Max feedback latency:** 30 seconds

---

## Per-Task Verification Map

Each row maps a phase requirement to the test that proves it, the **plan/task that creates the test file**, and the wave in which that test lands. Tests are written TDD-style alongside their implementation (no separate Wave 0 stubs — see Wave 0 Requirements below).

| Task ID | Plan | Wave | Requirement | Threat Ref | Secure Behavior | Test Type | Automated Command | Test Created By | Status |
|---------|------|------|-------------|------------|-----------------|-----------|-------------------|-----------------|--------|
| 01-04-02 | 04 | 3 | RES-01 | T-01-14 | Service stays within 150MB memory budget; load shed at 95% | unit | `go test -v -race ./pkg/infra/ -run TestServiceMemoryBudget` | Plan 04 Task 2 → `pkg/infra/memory_test.go` | ⬜ pending |
| 01-06-02 | 06 | 3 | RES-03 | T-01-22 | 100 concurrent session limit enforced (101st rejected) | unit | `go test -v -race ./pkg/infra/ -run TestSessionLimit` | Plan 06 Task 2 → `pkg/infra/connection_test.go` | ⬜ pending |
| 01-05-02 | 05 | 3 | RES-04 | T-01-19 | Drop-oldest buffer overflow strategy without crashes | unit | `go test -v -race ./pkg/infra/ -run TestDropOldestStrategy` | Plan 05 Task 2 → `pkg/infra/pool_test.go` | ⬜ pending |
| 01-03-01 | 03 | 2 | ERR-04 | T-01-09 | Structured JSON logging with sanitization | unit | `go test -v -race ./pkg/infra/ -run TestStructuredLogging` | Plan 03 Task 1 → `pkg/infra/logging_test.go` | ⬜ pending |

**End-to-end integration coverage** (cross-cutting, not requirement-specific):

| Task ID | Plan | Wave | Behavior | Test Created By | Status |
|---------|------|------|----------|-----------------|--------|
| 01-08-01 | 08 | 4 | Server coordinates all components, graceful shutdown releases goroutines | Plan 08 Task 1 → `pkg/infra/server_test.go` | ⬜ pending |
| 01-08-02 | 08 | 4 | Health (200/503) and Prometheus `/metrics` endpoints wrapped by recovery middleware | Plan 08 Task 2 → `pkg/infra/health_test.go` | ⬜ pending |
| 01-08-03 | 08 | 4 | main.go loads config, handles SIGTERM/SIGINT, starts /health and /metrics servers, stays under 150MB at idle | Plan 08 Task 3 → `cmd/service/main_test.go` | ⬜ pending |

*Status: ⬜ pending · ✅ green · ❌ red · ⚠️ flaky*

---

## Wave 0 Requirements

Wave 0 in this phase is **minimal**: just the `go test` framework itself, which Plan 01 Task 1 establishes via `go mod init` (making `go test ./...` runnable). Plan 02 Task 2 adds `testify` for assertion ergonomics before any implementation plan needs it.

**No separate Wave 0 stub test files are created.** Every behavioral test is written TDD-style by the implementation plan that owns the behavior, co-located under `pkg/infra/` (single infrastructure package per D-02) or `cmd/service/`:

- [x] `go test` framework — enabled by Plan 01 Task 1 (`go mod init github.com/xiaotian/home-tutor-agent`)
- [x] `testify` assertion library — added by Plan 02 Task 2 (`go get github.com/stretchr/testify`)
- [x] Make targets for fast feedback — `make test-short` (Plan 01 Task 2) matches the Quick run command

The previously-listed `internal/infra/memory_test.go` path was incorrect (D-02 mandates a single `pkg/infra` package; Plan 04 creates `pkg/infra/memory_test.go`). All test paths in the Per-Task Verification Map above reflect the actual files the implementation plans create.

---

## Manual-Only Verifications

| Behavior | Requirement | Why Manual | Test Instructions |
|----------|-------------|------------|-------------------|
| Memory usage under production load | RES-01 | Requires system monitoring | Run service with load test and monitor `runtime.ReadMemStats` |
| Graceful shutdown signal handling | RES-01 | Requires external signal | Send SIGTERM and verify clean shutdown |
| Backpressure behavior under extreme load | RES-03, RES-04 | Requires concurrent client simulation | Run 200+ concurrent clients and observe rejection/drop behavior |

*If none: "All phase behaviors have automated verification."*

---

## Validation Sign-Off

- [x] All tasks have `<automated>` verify or Wave 0 dependencies
- [x] Sampling continuity: no 3 consecutive tasks without automated verify (every implementation task uses `tdd="true"` with `<behavior>` + `<automated>`)
- [x] Wave 0 covers all MISSING references (no MISSING references — every test is created by its owning plan; see Per-Task Verification Map)
- [x] No watch-mode flags (all `<automated>` commands are one-shot `go test` / `go build` / `grep` invocations)
- [x] Feedback latency < 30s (quick run via `make test-short` / `go test -v -short ./...`)
- [x] `nyquist_compliant: true` set in frontmatter
- [x] `wave_0_complete: true` set in frontmatter (Wave 0 reduces to framework install, delivered by Plan 01)

**Approval:** plan-level validation contract sound. Status remains `draft` until `validate-phase §6` flips it to `validated` after execution.
