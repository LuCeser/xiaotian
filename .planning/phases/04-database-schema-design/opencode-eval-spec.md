# opencode Evaluation Task Specification

## Objective
Evaluate Phase 04 database schema design decisions against project constraints and technical requirements.

## Context
Phase 04 "Database Schema Design" has completed context gathering using `--auto` mode. Key decisions made:
- D-01: Treat vocab_manager.py schema as canonical for vocabulary/FSRS data
- D-02: Extend database with Go-owned auxiliary tables only
- D-03: Shared SQLite database file access via DICTATION_DB_PATH
- D-04: Minimal session bookkeeping tables (sessions, session_events)
- D-07: Repository pattern with sql.DB for database access layer
- D-10: Create ADR documenting FSRS+SQLite ownership reconciliation

## Evaluation Criteria

### 1. Constraint Validation
Evaluate decisions against project constraints:
- **Infrastructure**: 1C 1G VPS with strict memory limits (150MB Go service, 50MB audio buffer)
- **Storage**: SQLite database (avoid PostgreSQL overhead)
- **Architecture**: Must support future phases (5-12) without schema conflicts
- **Concurrency**: Support 50-100 concurrent sessions

### 2. FSRS Compatibility
Verify schema reconciliation with vocab_manager.py:
- Existing cards/review_logs tables remain canonical
- Go auxiliary tables don't conflict with Python schema
- Shared SQLite access is safe (WAL mode compatibility)
- Phase 5 FSRS orchestration can read vocabulary data

### 3. Technical Soundness
Assess database design decisions:
- Repository pattern alignment with Phase 1 DI patterns
- Session bookkeeping table design sufficiency
- Migration strategy minimalism vs. future extensibility
- Database initialization and lifecycle management

### 4. Risk Assessment
Identify potential issues:
- Schema drift risks between Go and Python
- Concurrency bottlenecks in SQLite access
- Memory impact of auxiliary tables on 1C1G VPS
- Migration complexity for future phases

## Expected Output
Provide evaluation report with:
1. **Decision validation** - Pass/Fail/Concern for each major decision
2. **Risk assessment** - High/Medium/Low risks with mitigation suggestions
3. **Recommendation** - Approve/Adjust/Reconsider specific decisions
4. **Blocking concerns** - Any issues that should block progression to planning

## Evaluation Focus Areas
- D-01/D-02: Schema reconciliation approach soundness
- D-03: Shared SQLite database concurrency safety
- D-04: Auxiliary table scope adequacy for future phases
- D-07: Repository pattern fit with existing architecture
- D-10: ADR sufficiency for resolving ownership ambiguity

## Deliverables
1. Evaluation report in `.planning/phases/04-database-schema-design/EVALUATION.md`
2. Specific recommendations for CONTEXT.md adjustments if needed
3. Risk register with mitigation strategies
4. Decision on whether to proceed to planning phase