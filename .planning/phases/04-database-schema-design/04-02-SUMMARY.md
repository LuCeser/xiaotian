---
phase: 04-database-schema-design
plan: 02
subsystem: planning
tags: [adr, fsrs-sqlite-ownership, schema-reconciliation, write-separation, conflict-resolution, architecture-decision]

# Dependency graph
requires:
  - phase: 03-python-cli-integration
    provides: Phase 3 D-09 (long-term Python FSRS reuse), D-10 (Phase 4 must reconcile with existing schema)
  - phase: 04-database-schema-design
    provides: 04-CONTEXT.md (decisions D-01, D-02, D-03, D-03-a, D-06, D-10, D-10-a), EVALUATION.md (Required Actions #3)
provides:
  - ADR: `.planning/adr/2026-08-01-fsrs-sqlite-ownership.md` (FSRS+SQLite single-source-of-truth ownership decision)
  - Resolved todo: `todos/pending/2026-07-25-fsrs-sqlite-ownership-adr.md` (status: resolved, pointer to ADR)
  - Decision contract: Python vocab_manager.py is canonical FSRS+SQLite authority; Go extends with auxiliary tables only
affects: [phase-05, phase-07, phase-10]

# Tech tracking
tech-stack:
  added: []
  patterns:
    - architecture-decision-record-as-contract (ADR documents 4 required elements per D-10-a: single source of truth, write separation, schema modification process, conflict resolution)
    - write-ownership-separation (Python owns cards/review_logs, Go owns sessions/session_events, cross-system writes prohibited)
    - schema-drift-resolution-python-precedence (If drift detected, Python schema takes precedence; Go adapts auxiliary tables)
    - fsrs-parameter-routing-through-pybridge (All FSRS state changes flow through pybridge.Executor.RunReview, never direct Go writes)

key-files:
  created:
    - .planning/adr/2026-08-01-fsrs-sqlite-ownership.md
  modified:
    - todos/pending/2026-07-25-fsrs-sqlite-ownership-adr.md  # marked resolved with ADR pointer

key-decisions:
  - "Python vocab_manager.py is the sole canonical authority for cards/review_logs schema (D-10-a element 1)"
  - "Write separation: Python writes cards/review_logs, Go writes sessions/session_events, cross-system writes prohibited (D-03-a)"
  - "Schema modification process: changes to Python schema require explicit ADR update first (element 3)"
  - "Conflict resolution: Python schema takes precedence on drift; Go adapts auxiliary tables (element 4)"
  - "FSRS parameter updates must route through pybridge.Executor.RunReview, never direct Go writes"
  - "Go uses modernc.org/sqlite (pure-Go, CGO_ENABLED=0) for database access"
  - "Go reads cards via CardRepository using explicit column list (never SELECT *)"
  - "ADR preserves decision audit trail; todo file marked resolved but not deleted"

---

## Execution Summary

### Objective
Write the Architecture Decision Record that resolves the FSRS+SQLite ownership ambiguity flagged in the 2026-07-25 architecture review (P1-4). This ADR is the contract every later Phase 4 code plan implements.

### What Was Done

1. **Created ADR document** at `.planning/adr/2026-08-01-fsrs-sqlite-ownership.md` with all 4 required D-10-a elements:
   - Single source of truth section declaring vocab_manager.py as canonical authority
   - Write separation section documenting Python/Go ownership boundaries
   - Schema modification process requiring ADR updates before schema changes
   - Conflict resolution stating Python precedence on drift

2. **Updated pending todo** `todos/pending/2026-07-25-fsrs-sqlite-ownership-adr.md`:
   - Added Resolution section with status, timestamp, and ADR pointer
   - Preserved original Problem/Solution text for audit trail
   - Marked as resolved per plan requirements

3. **Automated verification passed**:
   - ADR file exists at exact path
   - All 4 D-10-a section headings present (Single source of truth, Write separation, Schema modification process, Conflict resolution)
   - vocab_manager.py cited as canonical schema source
   - Pending todo marked resolved with ADR pointer

### Key Technical Details

**ADR Structure:**
- Context: Explains the dual-authority risk and architecture review concern
- Decision: 4 labeled subsections addressing each D-10-a element
- Consequences: FSRS routing rules, Go access patterns, audit trail, future phase implications

**Schema Reference:**
- Canonical source: `third_party/python/vocab_manager.py` lines 103-128
- Tables: `cards` (lines 103-113), `review_logs` (lines 114-120), indexes (lines 121-123)
- Migration pattern: `fetch_locked_until` column addition (lines 126-128)

**Write Separation Rules:**
- Python owns: `cards`, `review_logs`, all FSRS-related tables/indexes
- Go owns: `sessions`, `session_events`, future auxiliary tables
- Prohibition: No cross-system writes in either direction

**Conflict Resolution Process:**
- Detection: Phase 5 integration test per D-08-a
- Resolution: Python schema always correct; Go adapts auxiliary tables
- Operational owner: `pkg/database` package

### Verification Results

**Automated Checks:**
- ✅ File exists: `.planning/adr/2026-08-01-fsrs-sqlite-ownership.md`
- ✅ Section present: "Single source of truth"
- ✅ Section present: "Write separation"
- ✅ Section present: "Schema modification process"
- ✅ Section present: "Conflict resolution"
- ✅ Reference present: "vocab_manager.py"

**Human Review Required:**
- Task 2 (blocking checkpoint): Human review of ADR completeness against 4 required elements
- Open ADR and confirm each section addresses its D-10-a element
- Verify pending todo now reads resolved with ADR pointer

### Dependencies Satisfied

- Phase 3 D-09: Long-term Python FSRS reuse decision respected
- Phase 3 D-10: Phase 4 reconciliation with existing schema completed
- 04-CONTEXT.md D-10/D-10-a: All 4 required elements documented
- EVALUATION.md Required Actions #3: ADR addresses all 4 elements

### Next Steps

1. **Human review gate**: Type "approved" or describe what is missing after reviewing ADR completeness
2. **Proceed to next plan**: After approval, advance to 04-03 or next phase plan
3. **Phase 5 integration**: Schema validation tests will reference this ADR as contract

---

**Plan Status:** ✅ COMPLETE (pending human review gate)  
**Created:** 2026-08-01  
**Executor:** opencode