# ADR: FSRS+SQLite Ownership Reconciliation

**Status:** Accepted  
**Date:** 2026-08-01  
**Deciders:** Phase 4 Planning Team  
**Relates to:** 2026-07-25 Architecture Review P1-4

## Context

The Home Tutor Agent project has a Python implementation (`vocab_manager.py`) that already contains a complete FSRS algorithm and SQLite schema for vocabulary management. The Phase 3 Python CLI Integration decision (D-09) established that this Python implementation is the permanent FSRS engine and will not be reimplemented in Go.

However, the ROADMAP originally suggested Phase 4 ("Database Schema Design") and Phase 5 ("FSRS Core Implementation") would design schemas and implement FSRS from scratch in Go. This creates a dual-authority risk where two separate schema definitions could drift apart.

The architecture review (2026-07-25, P1-4) identified this as a blocking issue: "Without this decision, Phase 4/5 planning will be based on wrong premises."

## Decision

### 1. Single Source of Truth

Python `vocab_manager.py` is the **sole canonical authority** for the FSRS+SQLite schema. The canonical schema definition is located in `third_party/python/vocab_manager.py` lines 103-128, which includes:

- `cards` table (lines 103-113)
- `review_logs` table (lines 114-120)
- Indexes (lines 121-123)
- Migration pattern for `fetch_locked_until` column (lines 126-128)

Go must **never** create, alter, or drop these tables. The Python implementation remains the permanent engine per Phase 3 D-09.

### 2. Write Separation

Write ownership is strictly separated between Python and Go:

**Python owns writes to:**
- `cards` table
- `review_logs` table
- All FSRS-related tables and indexes

**Go owns writes to:**
- `sessions` table (session lifecycle tracking)
- `session_events` table (session state transitions)
- Future Go-owned auxiliary tables

**Cross-system writes are prohibited:**
- Go must not write to Python-owned tables (`cards`, `review_logs`)
- Python must not write to Go-owned tables (`sessions`, `session_events`)

SQLite WAL mode (already enabled by `vocab_manager.py` line 94) enables safe concurrent reads. Go can read Python-owned tables for FSRS orchestration (Phase 5) and analytics (Phase 9+). Python should not need to read Go-owned session tables in current requirements.

### 3. Schema Modification Process

Any change to the Python-owned schema (new column, renamed index, altered constraint) requires:

1. **ADR update first** - This ADR must be updated to reflect the schema change before the change ships
2. **Go side review** - The Go side (`CardRepository` explicit column list, schema-drift test) must be reviewed to ensure compatibility
3. **Integration test validation** - Phase 5 integration tests include schema consistency checks comparing Go auxiliary table definitions against Python's canonical schema

The database layer package (`pkg/database`) owns schema consistency validation. Any schema changes require updates to the integration test suite.

### 4. Conflict Resolution

If schema drift is detected (Phase 5 integration test per D-08-a):

1. **Python schema takes precedence** - The Python `vocab_manager.py` schema is always correct
2. **Go adapts auxiliary tables** - Go must adapt its auxiliary tables and `CardRepository` column list to match Python's schema
3. **Go never forces Python to change** - The Go side must never force Python to modify its schema

The reconciliation process:
- Integration test fails with specific schema mismatch details
- Resolution requires explicit migration script before deployment can proceed
- Operational responsibility lies with the database layer package (`pkg/database`)

## Consequences

### FSRS Parameter Updates
All FSRS algorithm state changes (stability, difficulty, due calculations) must flow through `pybridge.Executor.RunReview`. Direct Go writes to FSRS fields are prohibited. This ensures the Python FSRS engine remains authoritative.

### Go Database Access Pattern
Go reads cards via `CardRepository` using an explicit column list (never `SELECT *`). This ensures column changes fail loudly rather than silently breaking. The Go driver is `modernc.org/sqlite` (pure-Go, `CGO_ENABLED=0`) chosen because `CGO_ENABLED=0` is locked (Plan 01) for Windows local debug.

### Audit Trail
The pending architecture review todo (`2026-07-25-fsrs-sqlite-ownership-adr.md`) is marked resolved with a pointer to this ADR. The todo file is not deleted to preserve the decision audit trail.

### Future Phases
- Phase 4 creates Go-owned auxiliary tables (`sessions`, `session_events`) without conflicts
- Phase 5 orchestrates FSRS through `pybridge.RunReview` without reimplementing the algorithm
- Phase 7 uses `SessionRepository` for session lifecycle management
- Phase 10 may extend `review_logs` with analytics tables without breaking existing schema

## References

- `third_party/python/vocab_manager.py` lines 103-128 (canonical schema)
- Phase 3 D-09 (long-term Python FSRS reuse)
- Phase 3 D-10 (Phase 4 must reconcile with existing schema)
- 2026-07-25 Architecture Review P1-4 (FSRS+SQLite ownership ambiguity)
- `todos/pending/2026-07-25-fsrs-sqlite-ownership-adr.md` (pending todo resolved by this ADR)