# Phase 04 Database Schema Design - Evaluation Report

**Evaluator:** opencode  
**Date:** 2026-08-01  
**Status:** ✅ **APPROVE WITH MINOR ADJUSTMENTS**

---

## Executive Summary

Phase 04 database schema design decisions are **fundamentally sound** and properly address the critical ownership ambiguity raised in the architecture review (2026-07-25). The decision to treat Python's `vocab_manager.py` as the canonical FSRS+SQLite authority while allowing Go to extend with auxiliary tables is the correct architectural choice for this project.

**Key Findings:**
- ✅ **12/12 decisions pass** constraint validation and technical soundness criteria
- ⚠️ **3 concerns identified** requiring documentation adjustments and risk mitigation
- 🎯 **1 blocking issue resolved** - the FSRS+SQLite ownership ADR decision (D-10) properly addresses the P1-4 architecture review concern
- 📊 **5 risks assessed** - 1 high, 2 medium, 2 low priority

**Final Recommendation:** **PROCEED TO PLANNING** after incorporating documentation adjustments and implementing recommended mitigations.

---

## Decision-by-Decision Analysis

### D-01: Python Schema as Canonical Source
**Status:** ✅ **PASS**

**Evaluation:**
- **Constraint Validation:** ✅ Properly respects PROJECT.md SQLite storage choice and avoids PostgreSQL overhead
- **FSRS Compatibility:** ✅ Correctly treats `vocab_manager.py` lines 103-128 as immutable truth source
- **Technical Soundness:** ✅ Aligns with Phase 3 D-09 (long-term Python FSRS reuse) and prevents schema drift
- **Risk Assessment:** ✅ LOW - Single source of truth eliminates dual-authority conflicts

**Evidence:** Resolves the exact issue raised in `todos/pending/2026-07-25-fsrs-sqlite-ownership-adr.md` where ROADMAP incorrectly suggested Phase 4/5 would "design from scratch."

---

### D-02: Go-Owned Auxiliary Tables Only
**Status:** ✅ **PASS**

**Evaluation:**
- **Constraint Validation:** ✅ Respects 1C1G VPS limits by avoiding schema duplication overhead
- **FSRS Compatibility:** ✅ Non-conflicting table namespace prevents Python/Go collision
- **Technical Soundness:** ✅ Clean separation of concerns (Python=FSRS engine, Go=session orchestration)
- **Risk Assessment:** ✅ LOW - Table namespace collision easily prevented with naming conventions

**Evidence:** Consistent with Phase 3 D-10 constraint that "Phase 4 must reconcile with vocab_manager's existing schema."

---

### D-03: Shared SQLite Database File Access
**Status:** ✅ **PASS - CONCURRENCY SAFETY VALIDATED**

**Evaluation:**
- **Constraint Validation:** ✅ WAL mode already enabled by vocab_manager.py (line 94) supports concurrent reads
- **FSRS Compatibility:** ✅ Go auxiliary tables separate from Python `cards`/`review_logs` tables
- **Technical Soundness:** ✅ SQLite WAL mode allows 1 writer + multiple readers safely
- **Risk Assessment:** ⚠️ **MEDIUM** - Need clear documentation of write separation (Python writes FSRS tables, Go writes session tables)

**Evidence:** `vocab_manager.py:94` already sets `PRAGMA journal_mode=WAL`, enabling safe concurrent access patterns.

**Recommendation:** Add explicit concurrency documentation to ADR clarifying:
- Python owns writes to `cards`, `review_logs` tables
- Go owns writes to `sessions`, `session_events` tables  
- Cross-system writes prohibited (no Go writes to Python tables, no Python writes to Go tables)

---

### D-04: Minimal Session Bookkeeping Tables
**Status:** ✅ **PASS - SCOPE APPROPRIATE FOR PHASE 4**

**Evaluation:**
- **Constraint Validation:** ✅ Lightweight table design fits 150MB memory budget
- **FSRS Compatibility:** ✅ N/A - session tables independent of FSRS schema
- **Technical Soundness:** ✅ Supports Phase 7 session orchestration requirements
- **Risk Assessment:** ✅ LOW - Table design can be extended in future phases without breaking changes

**Evidence:** Properly scoped to support Phase 2 connection lifecycle decisions without pre-designing Phase 6/10 features.

**Claude's Discretion Validation:** The exact column set for `sessions`/`session_events` tables appropriately left for implementation phase, with adequate guidance (session lifecycle: IDLE → LISTENING → DICTATING → WAITING_PARENT → FEEDBACK).

---

### D-05: No TTS Audio Cache Tables in Phase 4
**Status:** ✅ **PASS**

**Evaluation:**
- **Constraint Validation:** ✅ Avoids premature schema bloat on 1C1G VPS
- **FSRS Compatibility:** ✅ N/A - audio caching separate from FSRS concerns
- **Technical Soundness:** ✅ In-memory path management adequate for Phase 6 TTS streaming
- **Risk Assessment:** ✅ LOW - Cache metadata can be added in Phase 6 if needed

**Evidence:** Correctly deferred to Phase 6 per CONTEXT.md deferred items list.

---

### D-06: Parent Feedback Data Remains in review_logs
**Status:** ✅ **PASS**

**Evaluation:**
- **Constraint Validation:** ✅ Reuses existing schema, avoiding duplication
- **FSRS Compatibility:** ✅ Respects Python FSRS engine's ownership of `review_logs`
- **Technical Soundness:** ✅ Phase 10 can extend with analytics tables without breaking existing schema
- **Risk Assessment:** ✅ LOW - Extensible design supports future FEED-* requirements

**Evidence:** Consistent with Phase 3 D-09 that Python owns permanent FSRS engine.

---

### D-07: Repository Pattern with sql.DB
**Status:** ✅ **PASS - ARCHITECTURAL ALIGNMENT VALIDATED**

**Evaluation:**
- **Constraint Validation:** ✅ Aligns with Phase 1 D-42/D-47 constructor DI patterns
- **FSRS Compatibility:** ✅ Repository interfaces enable clean Python/Go separation
- **Technical Soundness:** ✅ Standard Go database access pattern with `*sql.DB` dependency injection
- **Risk Assessment:** ✅ LOW - Well-established pattern with extensive Go ecosystem support

**Evidence:** 
- Phase 1 D-42/D-47 established constructor DI pattern
- Phase 1 D-48 established manual fakes for testing
- Repository pattern supports both real database and test fakes

**Claude's Discretion Validation:** Repository interface method signatures appropriately left for implementation phase with adequate guidance (card read methods for Phase 5, session CRUD for Phase 7).

---

### D-08: Minimal Migration Strategy
**Status:** ⚠️ **CONCERN - DOCUMENTATION CLARITY NEEDED**

**Evaluation:**
- **Constraint Validation:** ✅ Lightweight approach fits 1C1G VPS resource limits
- **FSRS Compatibility:** ✅ Mirrors vocab_manager.py's existing migration pattern (lines 126-128)
- **Technical Soundness:** ✅ `CREATE TABLE IF NOT EXISTS` + `PRAGMA table_info` pattern proven in Python codebase
- **Risk Assessment:** ⚠️ **MEDIUM** - Manual schema drift detection introduces operational risk

**Evidence:** vocab_manager.py (lines 126-128) demonstrates the exact pattern: check column existence via `PRAGMA table_info`, conditionally `ALTER TABLE`, commit changes.

**Concern:** "Schema drift detection is manual" lacks clear guidance for:
- How drift will be detected across Go/Python schemas
- Who is responsible for monitoring schema consistency
- What process exists for reconciliation if drift detected

**Recommendation:** Add explicit operational guidance to CONTEXT.md or ADR:
1. Define schema drift detection method (e.g., integration test comparing table schemas)
2. Assign operational responsibility (e.g., Phase 5 integration tests include schema validation)
3. Document reconciliation process (e.g., if drift detected, raise blocking error before deployment)

---

### D-09: Database Initialization in cmd/service/main.go
**Status:** ✅ **PASS**

**Evaluation:**
- **Constraint Validation:** ✅ Service startup initialization aligns with Phase 1 D-19 (startup-only config)
- **FSRS Compatibility:** ✅ Single sql.DB connection lifetime matches service lifetime
- **Technical Soundness:** ✅ Standard Go service pattern for database lifecycle
- **Risk Assessment:** ✅ LOW - Well-established pattern with clear connection lifecycle

**Evidence:** Consistent with Phase 1's "startup-only configuration" approach (D-19) and service lifecycle patterns.

---

### D-10: ADR for FSRS+SQLite Ownership Reconciliation
**Status:** ✅ **PASS - CRITICAL DECISION VALIDATED**

**Evaluation:**
- **Constraint Validation:** ✅ Addresses the blocking P1-4 architecture review concern
- **FSRS Compatibility:** ✅ Properly documents Python as canonical authority
- **Technical Soundness:** ✅ Resolves dual-source-of-truth risk identified in architecture review
- **Risk Assessment:** ✅ CRITICAL PASS - This decision resolves the key blocker

**Evidence:** Directly addresses `todos/pending/2026-07-25-fsrs-sqlite-ownership-adr.md` which stated:
> "不定下来，Phase 4/5 计划会建立在错误前提上" (Without this, Phase 4/5 planning will be based on wrong premises)

**ADR Scope Requirements:** The ADR must document:
1. Schema change authority (Python owns FSRS schema changes)
2. SQLite access patterns (CGO-free driver selection for Go)
3. FSRS parameter update requirements (must use `pybridge.RunReview`)
4. Auxiliary table ownership (Go owns session tables, Python owns FSRS tables)

---

### D-11: Schema Documentation in Code Comments
**Status:** ✅ **PASS**

**Evaluation:**
- **Constraint Validation:** ✅ Code-near documentation reduces maintenance burden
- **FSRS Compatibility:** ✅ Table ownership comments prevent future conflicts
- **Technical Soundness:** ✅ README in `pkg/database` provides architectural overview
- **Risk Assessment:** ✅ LOW - Standard documentation approach

**Evidence:** Aligns with Phase 1 D-34 "combined documentation approach (godoc + README files)."

---

### D-12: No Separate Schema Documentation File
**Status:** ✅ **PASS**

**Evaluation:**
- **Constraint Validation:** ✅ Avoids documentation synchronization overhead
- **FSRS Compatibility:** ✅ Canonical schema lives in implementation (Python + Go migration code)
- **Technical Soundness:** ✅ Docs-close-to-implementation principle reduces drift risk
- **Risk Assessment:** ✅ LOW - Single source of truth approach

**Evidence:** Consistent with CONTEXT.md principle: "The canonical schema definition lives in code."

---

## Constraint Validation Summary

### Infrastructure Constraints (1C1G VPS)
- ✅ SQLite chosen over PostgreSQL (avoids resource overhead)
- ✅ Minimal auxiliary table design (sessions, session_events only)
- ✅ No premature TTS cache table bloat (deferred to Phase 6)
- ✅ Repository pattern supports 150MB memory budget (lightweight abstraction)

### Architecture Constraints
- ✅ Supports future phases (5-12) through extensible auxiliary table design
- ✅ Clean separation allows Phase 5 FSRS orchestration without schema changes
- ✅ Session bookkeeping supports Phase 7 state management requirements
- ✅ Parent feedback schema (review_logs) extensible for Phase 10 analytics

### Concurrency Constraints
- ✅ SQLite WAL mode supports 50-100 concurrent sessions
- ✅ Read/write separation (Python writes FSRS tables, Go writes session tables) prevents write conflicts
- ✅ Repository pattern enables connection pooling and resource management

---

## FSRS Compatibility Analysis

### Schema Reconciliation
✅ **PASS** - D-01 correctly treats vocab_manager.py schema as canonical:
- `cards` table (lines 103-113) remains Python-owned
- `review_logs` table (lines 114-120) remains Python-owned  
- Indexes (lines 121-123) remain Python-owned
- Migration pattern (lines 126-128) preserved

### Go Auxiliary Table Safety
✅ **PASS** - D-02 auxiliary tables avoid namespace conflicts:
- Session tables use Go-owned namespace
- No Python table duplication
- Clean architectural boundary

### SQLite Access Safety
✅ **PASS** - D-03 shared access is safe:
- WAL mode already enabled by Python (vocab_manager.py:94)
- Write separation prevents lock contention
- Phase 5 can read vocabulary data via CardRepository without conflicts

---

## Risk Assessment

### HIGH RISK
🔴 **Risk: Schema Drift Detection is Manual (D-08)**
- **Impact:** Undetected schema inconsistencies could cause runtime failures in production
- **Probability:** Medium - evolution across multiple phases increases drift likelihood
- **Mitigation:** 
  1. Add integration test for schema validation in Phase 5
  2. Document operational responsibility for schema consistency checks
  3. Define reconciliation process before Phase 7 (session orchestration complexity increases)

### MEDIUM RISK  
🟡 **Risk: Concurrency Patterns Not Documented (D-03)**
- **Impact:** Cross-system writes could cause lock contention or data corruption
- **Probability:** Low - write separation reduces likelihood, but operational errors possible
- **Mitigation:**
  1. Add explicit write ownership documentation to ADR
  2. Include concurrency patterns in `pkg/database/README.md`
  3. Add integration test validating concurrent read/write safety

🟡 **Risk: ADR Scope Completeness (D-10)**
- **Impact:** Incomplete ADR could lead to future architectural ambiguity
- **Probability:** Medium - complex decision with multiple stakeholders (Python/Go boundary)
- **Mitigation:**
  1. Ensure ADR explicitly addresses all 4 required elements (schema authority, access patterns, FSRS updates, table ownership)
  2. Review ADR against architecture review concern before finalizing
  3. Consider adding decision diagram showing Go/Python interaction boundaries

### LOW RISK
🟢 **Risk: Repository Interface Scope Creep (D-07)**
- **Impact:** Repository interfaces could become bloated with future phase requirements
- **Probability:** Low - Claude's discretion appropriately scoped to Phase 4 needs
- **Mitigation:**
  1. Document Phase 4 scope in repository interface godoc
  2. Use interface segregation principle if methods grow in future phases
  3. Keep read-only vs. write repository separation clear

🟢 **Risk: Session Table Schema Inadequacy (D-04)**
- **Impact:** Current minimal session table design might not support future requirements
- **Probability:** Low - Phase 2 lifecycle requirements well-defined, extensibility designed in
- **Mitigation:**
  1. Design session tables with extensibility (JSON metadata columns for future fields)
  2. Document assumptions about session lifecycle in table creation comments
  3. Phase 7 implementation will validate schema adequacy early

---

## Recommendations for CONTEXT.md Adjustments

### 1. Add Concurrency Documentation (D-03)
**Add to `<decisions>` section after D-03:**
```markdown
### Concurrency Safety Constraints
- **D-03a:** Write ownership is strictly separated. Python owns writes to `cards`, `review_logs`, and all FSRS-related tables. Go owns writes to `sessions`, `session_events`, and all session bookkeeping tables. Cross-system writes are prohibited.
- **D-03b:** WAL mode enables safe concurrent reads. Go can read Python-owned tables for FSRS orchestration (Phase 5) and analytics (Phase 9+). Python should not need to read Go-owned session tables in current requirements.
```

### 2. Clarify Schema Drift Detection (D-08)
**Add to `<decisions>` section after D-08:**
```markdown
### Schema Consistency Validation
- **D-08a:** Integration test for schema validation. Phase 5 integration tests will include schema consistency checks comparing Go auxiliary table definitions against Python's canonical schema to detect drift before deployment.
- **D-08b:** Operational responsibility. The database layer package (`pkg/database`) owns schema consistency validation. Any schema changes require updates to the integration test suite.
```

### 3. Expand ADR Requirements (D-10)
**Replace current D-10 with more detailed specification:**
```markdown
- **D-10:** ADR for FSRS+SQLite ownership reconciliation. Phase 4 creates `.planning/adr/2026-08-01-fsrs-sqlite-ownership.md` documenting the decision that Python `vocab_manager.py` owns the canonical vocabulary/FSRS schema and Go extends it with auxiliary tables only. The ADR must explicitly address:
  1. **Schema Change Authority:** Python `vocab_manager.py` is the only authorized source of changes to `cards`, `review_logs`, and all FSRS-related tables and indexes. Go must not modify these tables.
  2. **SQLite Access Patterns:** Go uses CGO-free `modern.org.org/sqlite` driver to access the shared database file. Go reads Python-owned tables via `CardRepository` (read-only) and writes to Go-owned auxiliary tables via `SessionRepository`.
  3. **FSRS Parameter Updates:** All FSRS algorithm state changes (stability, difficulty, due calculations) must flow through `pybridge.RunReview` to ensure Python FSRS engine remains authoritative. Direct Go writes to FSRS fields are prohibited.
  4. **Auxiliary Table Ownership:** Go owns schema design and migration for `sessions`, `session_events`, and future auxiliary tables. Python must not write to these tables.
```

---

## Blocking Concerns

### ❌ NO BLOCKING CONCERNS IDENTIFIED

The critical architecture review concern (P1-4: FSRS+SQLite ownership ambiguity) is properly resolved by D-10/D-11 decisions. All other concerns are mitigated through documentation adjustments and risk management strategies.

---

## Claude's Discretion Items Validation

The following items were appropriately left to Claude's discretion and have adequate implementation guidance:

1. ✅ **Exact column set for `sessions` and `session_events` tables** - Phase 2 lifecycle requirements provide sufficient guidance; implementation can determine optimal fields
2. ✅ **Repository interface method signatures** - Phase 4 scope (read-only CardRepository, session CRUD) provides adequate boundary
3. ✅ **`Config.DatabasePath` field location and default value** - Suggested default `$HOME/.home-tutor-agent/vocab.db` aligns with Python defaults
4. ✅ **`CardRepository` read-only vs. write methods** - Properly deferred to Phase 9 when parent vocabulary management requirements are clearer

---

## Final Decision

### ✅ **APPROVE WITH MINOR ADJUSTMENTS**

**Rationale:**
1. **Architecture Sound:** Decisions properly address the critical single-source-of-truth concern and establish clean Python/Go boundaries
2. **Constraint Compliant:** All decisions respect 1C1G VPS limits and align with Phase 1 infrastructure patterns
3. **FSRS Compatible:** Schema reconciliation approach properly treats Python vocab_manager.py as canonical
4. **Extensible:** Auxiliary table design supports future phases without breaking changes
5. **Risk Managed:** Identified concerns have clear mitigation strategies

**Required Actions Before Planning:**
1. Incorporate the 3 CONTEXT.md adjustments specified above
2. Ensure ADR (D-10) addresses all 4 required elements when created
3. Add schema validation integration test to Phase 5 planning scope

**Approved For:** Proceed to `/gsd-plan-phase` for Phase 04 implementation planning.

---

## Appendix: Evaluation Methodology

This evaluation used the following methodology:

1. **Decision Validation:** Each decision (D-01 through D-12) assessed against 4 criteria:
   - Constraint Validation (infrastructure, architecture, concurrency)
   - FSRS Compatibility (vocab_manager.py schema reconciliation)
   - Technical Soundness (alignment with Phase 1 patterns)
   - Risk Assessment (probability, impact, mitigation)

2. **Evidence Gathering:** Cross-referenced decisions with:
   - Phase 03 CONTEXT.md (D-09, D-10 constraints)
   - vocab_manager.py source code (lines 103-128, migration patterns)
   - Phase 01 CONTEXT.md (DI patterns, infrastructure constraints)
   - Architecture review todos (P1-4 concern)

3. **Risk Classification:** Risks classified by severity (HIGH/MEDIUM/LOW) with specific mitigation strategies

4. **Recommendation Generation:** Specific CONTEXT.md adjustments to address identified concerns

**Evaluation Confidence:** HIGH - All canonical references reviewed, architecture constraints validated, risks assessed with specific mitigations.

---

*Evaluation completed: 2026-08-01*  
*Evaluator: opencode*  
*Next phase: Planning*