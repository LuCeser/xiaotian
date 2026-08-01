---
phase: 04-database-schema-design
plan: 03
subsystem: database
tags: [sqlite, database, wal, migration, schema]

# Dependency graph
requires:
  - phase: 04-database-schema-design
    provides: modernc.org/sqlite driver, CGO_ENABLED=0 lock, DatabaseConfig
provides:
  - New package: pkg/database (schema, db, migrate)
  - Types: SessionState, Session, SessionEvent
  - Functions: Open(), Migrate()
  - Sentinel error: ErrEmptyPath
  - DDL constants: sessionsDDL, sessionEventsDDL, idxSessionsConnection, idxEventsSession
  - Shared test helper: newTestDB(t)
  - Documentation: pkg/database/README.md
affects: [phase-04-rest, phase-05, phase-07]

# Actuals (#2632)
actuals:
  tokens: 8600
  tasks: 2
  commits: 2

# Tech tracking
tech-stack:
  added: []
  patterns:
    - wal-concurrency-model (WAL mode with SetMaxOpenConns(1) for safe Go/Python shared access)
    - idempotent-migration (CREATE TABLE IF NOT EXISTS mirroring vocab_manager.py pattern)
    - write-separation-documentation (Python owns cards/review_logs, Go owns sessions/session_events)

key-files:
  created:
    - pkg/database/schema.go (SessionState type, Session/SessionEvent structs, DDL constants)
    - pkg/database/db.go (Open() constructor with WAL/busy_timeout/foreign_keys/synchronous pragmas)
    - pkg/database/migrate.go (Idempotent Migrate() creating Go-owned tables)
    - pkg/database/db_test.go (11 tests for Open, pragmas, MaxOpenConns, newTestDB helper)
    - pkg/database/migrate_test.go (7 tests for table creation, indexes, idempotency, FK)
    - pkg/database/README.md (Schema architecture, write-separation rules, WAL concurrency docs)
  modified: []

key-decisions:
  - "Open() uses ErrEmptyPath sentinel (errors.Is-detectable) for empty dbPath"
  - "Open() does NOT call db.Ping — pure construction step, caller decides connectivity"
  - "Migrate() executes DDL statements sequentially (not as single executescript) for better error context"
  - "newTestDB enables foreign_keys pragma to match production Open() DSN behavior"
  - "Session struct includes metadata TEXT column for Phase 7 extensibility without schema changes"

patterns-established:
  - "newTestDB(t) helper: shared test infrastructure reused by Plan 04 repository tests"
  - "PRAGMA verification pattern: query PRAGMA after Open to assert pragmas applied correctly"

requirements-completed: [VOC-04]

coverage:
  - id: D1
    description: "Open() constructor with WAL/busy_timeout/foreign_keys/synchronous pragmas verified via PRAGMA queries"
    requirement: VOC-04
    verification:
      - kind: unit
        ref: "pkg/database/db_test.go#TestOpen_Pragmas"
        status: pass
      - kind: unit
        ref: "pkg/database/db_test.go#TestOpen_MaxOpenConns"
        status: pass
    human_judgment: false
  - id: D2
    description: "Open() creates parent directory and rejects empty path"
    requirement: VOC-04
    verification:
      - kind: unit
        ref: "pkg/database/db_test.go#TestOpen"
        status: pass
    human_judgment: false
  - id: D3
    description: "Migrate() creates sessions + session_events + indexes idempotently"
    requirement: VOC-04
    verification:
      - kind: unit
        ref: "pkg/database/migrate_test.go#TestMigrate_CreatesSessionsTable"
        status: pass
      - kind: unit
        ref: "pkg/database/migrate_test.go#TestMigrate_CreatesSessionEventsTable"
        status: pass
      - kind: unit
        ref: "pkg/database/migrate_test.go#TestMigrate_CreatesIndexes"
        status: pass
      - kind: unit
        ref: "pkg/database/migrate_test.go#TestMigrate_Idempotent"
        status: pass
    human_judgment: false
  - id: D4
    description: "Migrate() does NOT create Python-owned tables (cards, review_logs)"
    requirement: VOC-04
    verification:
      - kind: unit
        ref: "pkg/database/migrate_test.go#TestMigrate_DoesNotCreatePythonTables"
        status: pass
    human_judgment: false
  - id: D5
    description: "Foreign key enforcement works between sessions and session_events"
    requirement: VOC-04
    verification:
      - kind: unit
        ref: "pkg/database/migrate_test.go#TestMigrate_CreatesForeignKey"
        status: pass
    human_judgment: false
  - id: D6
    description: "README documents write-separation rules and points to ADR"
    requirement: VOC-04
    verification: []
    human_judgment: true
    rationale: "Documentation completeness requires human review to verify accuracy of cross-references and completeness of write-separation contract"

# Metrics
duration: 15min
completed: 2026-08-01
status: complete
---

# Phase 4 Plan 3: pkg/database Schema + Open + Migrate Summary

**SQLite connection with WAL/busy_timeout/foreign_keys/synchronous pragmas, idempotent migration for Go-owned sessions/session_events tables, shared newTestDB helper**

## Performance

- **Duration:** 15 min
- **Started:** 2026-08-01T19:00:00Z
- **Completed:** 2026-08-01T19:15:00Z
- **Tasks:** 2
- **Files created:** 6

## Accomplishments
- Created `pkg/database` package with schema types, Open() constructor, and idempotent Migrate()
- Open() configures WAL mode with busy_timeout=5000, foreign_keys=ON, synchronous=NORMAL via DSN pragmas
- Migrate() creates sessions + session_events tables + indexes using CREATE TABLE IF NOT EXISTS
- 11 tests pass covering Open(), pragma verification, migration, idempotency, FK enforcement, and no Python table creation
- README documents write-separation contract and WAL concurrency model

## Task Commits

Each task was committed atomically:

1. **Task 1: schema.go types + db.go Open() + db_test.go + migrate.go + migrate_test.go** - `4a0c713` (feat)
2. **Task 2: README.md** - `84cca08` (docs)

## Files Created/Modified
- `pkg/database/schema.go` - SessionState type, Session/SessionEvent structs, DDL constants for Go-owned tables
- `pkg/database/db.go` - Open() constructor with WAL/busy_timeout/foreign_keys/synchronous pragmas and ErrEmptyPath sentinel
- `pkg/database/migrate.go` - Idempotent Migrate() creating sessions + session_events tables and indexes
- `pkg/database/db_test.go` - 5 test functions covering Open(), pragmas, MaxOpenConns, parent dir creation, shared newTestDB helper
- `pkg/database/migrate_test.go` - 6 test functions covering table creation, indexes, idempotency, no Python tables, cancelled context, FK enforcement
- `pkg/database/README.md` - Schema architecture, write-separation rules, WAL concurrency model, migration strategy

## Decisions Made
- Open() uses ErrEmptyPath sentinel (errors.Is-detectable) consistent with pkg/infra pattern
- Open() does NOT call db.Ping — pure construction step (matches pkg/infra constructor pattern)
- newTestDB enables foreign_keys pragma to match production Open() DSN behavior
- Session struct includes metadata TEXT column for Phase 7 extensibility without schema changes

## Deviations from Plan

None - plan executed exactly as written.

## Issues Encountered

None.

## User Setup Required

None - no external service configuration required.

## Next Phase Readiness
- pkg/database is ready for Plan 04 (repositories) to inject *sql.DB
- Plan 05 (main.go wiring) can call database.Open() + database.Migrate() at boot
- newTestDB helper is exported within package for Plan 04 repository tests

---
*Phase: 04-database-schema-design*
*Completed: 2026-08-01*
