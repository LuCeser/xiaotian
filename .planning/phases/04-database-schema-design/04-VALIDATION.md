---
phase: 04
slug: database-schema-design
# status lifecycle: draft (seeded by plan-phase) → validated (set by validate-phase §6)
# audit-milestone §5.5 distinguishes NOT-VALIDATED (draft) from PARTIAL (validated + nyquist_compliant: false) (#2117)
status: draft
nyquist_compliant: false
wave_0_complete: false
created: 2026-08-01
---

# Phase 04 — Validation Strategy

> Per-phase validation contract for feedback sampling during execution.

---

## Test Infrastructure

| Property | Value |
|----------|-------|
| **Framework** | go test |
| **Config file** | none — Wave 0 installs |
| **Quick run command** | `go test ./pkg/database/... -v -run TestSessionRepository` |
| **Full suite command** | `go test ./... -v` |
| **Estimated runtime** | ~30 seconds |

---

## Sampling Rate

- **After every task commit:** Run `go test ./pkg/database/... -v -run TestSessionRepository`
- **After every plan wave:** Run `go test ./... -v`
- **Before `/gsd-verify-work`:** Full suite must be green
- **Max feedback latency:** 60 seconds

---

## Per-Task Verification Map

| Task ID | Plan | Wave | Requirement | Threat Ref | Secure Behavior | Test Type | Automated Command | File Exists | Status |
|---------|------|------|-------------|------------|-----------------|-----------|-------------------|-------------|--------|
| 04-01-01 | 01 | 1 | VOC-04 | T-04-01 | Repository pattern with *sql.DB injection | unit | `go test ./pkg/database/... -v -run TestNewSessionRepository` | ✅ W0 | ⬜ pending |
| 04-01-02 | 01 | 1 | VOC-04 | T-04-02 | CREATE TABLE IF NOT EXISTS for auxiliary tables | integration | `go test ./pkg/database/... -v -run TestMigrateSessions` | ✅ W0 | ⬜ pending |
| 04-01-03 | 01 | 1 | VOC-04 | T-04-03 | Schema validation against drift | integration | `go test ./pkg/database/... -v -run TestSchemaValidation` | ✅ W0 | ⬜ pending |
| 04-02-01 | 02 | 1 | VOC-04 | — | ADR documentation completeness | manual | File exists check | ✅ W0 | ⬜ pending |
| 04-03-01 | 03 | 1 | VOC-04 | T-04-01 | Database connection lifecycle in service bootstrap | integration | `go test ./cmd/service/... -v -run TestMainDatabaseConnection` | ✅ W0 | ⬜ pending |

*Status: ⬜ pending · ✅ green · ❌ red · ⚠️ flaky*

---

## Wave 0 Requirements

- [ ] `pkg/database/session_repository_test.go` — stubs for SessionRepository CRUD operations
- [ ] `pkg/database/card_repository_test.go` — stubs for CardRepository read operations
- [ ] `pkg/database/migration_test.go` — stubs for schema migration and validation tests
- [ ] `cmd/service/main_test.go` — stub for database connection lifecycle test

*If none: "Existing infrastructure covers all phase requirements."*

---

## Manual-Only Verifications

| Behavior | Requirement | Why Manual | Test Instructions |
|----------|-------------|------------|-------------------|
| ADR documentation completeness | D-10/D-10-a | Requires human review of decision completeness | Open `.planning/adr/2026-08-01-fsrs-sqlite-ownership.md` and verify all 4 required elements are present |
| Schema documentation in code comments | D-11 | Code review judgment | Open each table creation in `pkg/database/migration.go` and verify comment headers exist |

*If none: "All phase behaviors have automated verification."*

---

## Validation Sign-Off

- [ ] All tasks have `<automated>` verify or Wave 0 dependencies
- [ ] Sampling continuity: no 3 consecutive tasks without automated verify
- [ ] Wave 0 covers all MISSING references
- [ ] No watch-mode flags
- [ ] Feedback latency < 60s
- [ ] `nyquist_compliant: true` set in frontmatter

**Approval:** pending
