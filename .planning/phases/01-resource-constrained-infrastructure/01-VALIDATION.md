---
phase: 01
slug: resource-constrained-infrastructure
# status lifecycle: draft (seeded by plan-phase) → validated (set by validate-phase §6)
# audit-milestone §5.5 distinguishes NOT-VALIDATED (draft) from PARTIAL (validated + nyquist_compliant: false) (#2117)
status: draft
nyquist_compliant: false
wave_0_complete: false
created: 2025-07-18
---

# Phase 01 — Validation Strategy

> Per-phase validation contract for feedback sampling during execution.

---

## Test Infrastructure

| Property | Value |
|----------|-------|
| **Framework** | go test |
| **Config file** | none — Wave 0 installs |
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

| Task ID | Plan | Wave | Requirement | Threat Ref | Secure Behavior | Test Type | Automated Command | File Exists | Status |
|---------|------|------|-------------|------------|-----------------|-----------|-------------------|-------------|--------|
| 01-01-01 | 01 | 1 | RES-01 | T-01-01 / — | Service starts within 150MB memory budget | unit | `go test -v -run TestServiceMemoryBudget` | ✅ W0 | ⬜ pending |
| 01-01-02 | 01 | 1 | RES-03 | T-01-02 / — | 100 concurrent session limit enforced | unit | `go test -v -run TestSessionLimit` | ✅ W0 | ⬜ pending |
| 01-01-03 | 01 | 1 | RES-04 | T-01-03 / — | Drop-oldest buffer overflow strategy | unit | `go test -v -run TestDropOldestStrategy` | ✅ W0 | ⬜ pending |
| 01-01-04 | 01 | 1 | ERR-04 | T-01-04 / — | Structured logging captures all events | unit | `go test -v -run TestStructuredLogging` | ✅ W0 | ⬜ pending |

*Status: ⬜ pending · ✅ green · ❌ red · ⚠️ flaky*

---

## Wave 0 Requirements

- [ ] `internal/infra/memory_test.go` — stubs for RES-01, RES-03, RES-04, ERR-04
- [ ] `cmd/service/main_test.go` — service lifecycle tests
- [ ] `go test` — framework verification (Go standard library)

*If none: "Existing infrastructure covers all phase requirements."*

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

- [ ] All tasks have `<automated>` verify or Wave 0 dependencies
- [ ] Sampling continuity: no 3 consecutive tasks without automated verify
- [ ] Wave 0 covers all MISSING references
- [ ] No watch-mode flags
- [ ] Feedback latency < 30s
- [ ] `nyquist_compliant: true` set in frontmatter

**Approval:** pending
