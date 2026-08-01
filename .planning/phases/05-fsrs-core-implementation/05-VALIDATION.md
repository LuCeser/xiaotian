---
phase: 05
slug: fsrs-core-implementation
# status lifecycle: draft (seeded by plan-phase) → validated (set by validate-phase §6)
# audit-milestone §5.5 distinguishes NOT-VALIDATED (draft) from PARTIAL (validated + nyquist_compliant: false) (#2117)
status: draft
nyquist_compliant: false
wave_0_complete: false
created: 2026-08-01
---

# Phase 05 — Validation Strategy

> Per-phase validation contract for feedback sampling during execution.

---

## Test Infrastructure

| Property | Value |
|----------|-------|
| **Framework** | go test |
| **Config file** | none — uses existing pkg/infra/testutils |
| **Quick run command** | `go test ./pkg/fsrs/... -v -run TestFSRSService -timeout 30s` |
| **Full suite command** | `go test ./pkg/fsrs/... -v -timeout 60s` |
| **Estimated runtime** | ~5 seconds |

---

## Sampling Rate

- **After every task commit:** Run `go test ./pkg/fsrs/... -v -run TestFSRSService -timeout 30s`
- **After every plan wave:** Run `go test ./pkg/fsrs/... -v -timeout 60s`
- **Before `/gsd-verify-work`:** Full suite must be green
- **Max feedback latency:** 5 seconds

---

## Per-Task Verification Map

| Task ID | Plan | Wave | Requirement | Threat Ref | Secure Behavior | Test Type | Automated Command | File Exists | Status |
|---------|------|------|-------------|------------|-----------------|-----------|-------------------|-------------|--------|
| {N}-01-01 | 01 | 1 | FSRS-01 | T-{N}-01 / — | N/A | unit | `go test ./pkg/fsrs/... -run TestSelectDueCards` | ❌ W0 | ⬜ pending |

*Status: ⬜ pending · ✅ green · ❌ red · ⚠️ flaky*

---

## Wave 0 Requirements

- [ ] `pkg/fsrs/service_test.go` — FSRSService interface stubs for FSRS-01, FSRS-02, FSRS-03, FSRS-04
- [ ] `pkg/fsrs/mock_executor.go` — manual fake Executor for table-driven tests
- [ ] `pkg/fsrs/test_helpers.go` — shared test helpers (DueCard fixtures, Result builders)

*Note: Existing Go test infrastructure covers all phase requirements.*

---

## Manual-Only Verifications

| Behavior | Requirement | Why Manual | Test Instructions |
|----------|-------------|------------|-------------------|
| Integration test with real vocab_manager.py | FSRS-01, FSRS-04 | Requires Python environment and SQLite database | Run integration test: `go test ./pkg/fsrs/... -run TestIntegration` with PYTHONPATH set |

---

## Validation Sign-Off

- [ ] All tasks have `<automated>` verify or Wave 0 dependencies
- [ ] Sampling continuity: no 3 consecutive tasks without automated verify
- [ ] Wave 0 covers all MISSING references
- [ ] No watch-mode flags
- [ ] Feedback latency < 5s
- [ ] `nyquist_compliant: true` set in frontmatter

**Approval:** pending
