---
phase: 04-database-schema-design
plan: 04
subsystem: database
tags: [sqlite, repository, session, card, read-only, write-separation]

# Dependency graph
requires:
  - phase: 04-database-schema-design
    provides: modernc.org/sqlite driver, Open(), Migrate(), newTestDB helper, Session/SessionEvent/SessionState types
  - phase: 03-python-cli-integration
    provides: pybridge.Executor interface pattern, compile-time var _ check convention
  - phase: 01-foundation
    provides: infra.Logger, infra.NewNoOpLogger(), infra.F(), infra.ErrField()
provides:
  - SessionRepository interface (Create, UpdateState, AppendEvent, GetByID)
  - CardRepository interface (GetByID, ListByUnit — read-only)
  - sqliteSessionRepository + sqliteCardRepository implementations
  - Card struct (mirrors Python cards columns, FsrsCard as opaque string)
  - ErrSessionNotFound, ErrEmptyConnectionID, ErrCardNotFound sentinels
  - NewSessionRepository, NewCardRepository constructors
affects: [phase-05, phase-07, phase-10]

# Actuals (#2632)
actuals:
  tokens: 4600
  tasks: 2
  commits: 2

# Tech tracking
tech-stack:
  added: []
  patterns:
    - repository-interface-di (SessionRepository/CardRepository with *sql.DB + Logger constructor injection)
    - read-only-type-enforcement (CardRepository exposes no write methods — compile-time guarantee)
    - opaque-column-binding (FsrsCard as raw string — encoding/json never imported)

key-files:
  created:
    - pkg/database/session_repo.go (SessionRepository interface + sqliteSessionRepository + NewSessionRepository)
    - pkg/database/card_repo.go (CardRepository read-only interface + sqliteCardRepository + NewCardRepository + Card struct)
    - pkg/database/session_repo_test.go (10 table-driven tests for SessionRepository CRUD)
    - pkg/database/card_repo_test.go (9 table-driven tests seeding Python cards schema)
  modified: []

key-decisions:
  - "CardRepository is strictly read-only (D-01/D-03-a): no INSERT/UPDATE/DELETE methods; writes route through pybridge.RunReview"
  - "FsrsCard stored as opaque string — Go never imports encoding/json for it (RESEARCH Pitfall 5)"
  - "All card queries use explicit column list, never SELECT * (RESEARCH Pitfall 4)"
  - "scanCard helper extracts row scanning to avoid duplication between GetByID and ListByUnit"
  - "ErrSessionNotFound wraps sql.ErrNoRows for errors.Is detection"

patterns-established:
  - "Repository pattern: interface + unexported impl + var _ compile-time check + nil-guard constructor"
  - "Test seeding: newTestDBWithCards creates Python-owned cards table for realistic integration-style tests"

requirements-completed: [VOC-04]

coverage:
  - id: D1
    description: "SessionRepository CRUD operations (Create, UpdateState, AppendEvent, GetByID) with parameterized queries"
    requirement: VOC-04
    verification:
      - kind: unit
        ref: "pkg/database/session_repo_test.go#TestSessionRepository"
        status: pass
    human_judgment: false
  - id: D2
    description: "CardRepository read-only access (GetByID, ListByUnit) with explicit column lists and opaque fsrs_card"
    requirement: VOC-04
    verification:
      - kind: unit
        ref: "pkg/database/card_repo_test.go#TestCardRepository"
        status: pass
    human_judgment: false
  - id: D3
    description: "Constructor nil-guards (db rejection) and nil-logger substitution for both repositories"
    requirement: VOC-04
    verification:
      - kind: unit
        ref: "pkg/database/session_repo_test.go#TestSessionRepository/NewSessionRepository_with_nil_db_returns_error"
        status: pass
      - kind: unit
        ref: "pkg/database/card_repo_test.go#TestCardRepository/NewCardRepository_with_nil_db_returns_error"
        status: pass
    human_judgment: false
  - id: D4
    description: "Write-separation enforcement: CardRepository has no write methods, grep for INSERT/UPDATE/DELETE returns zero"
    requirement: VOC-04
    verification:
      - kind: other
        ref: "grep -rE INSERT|UPDATE|DELETE pkg/database/card_repo.go — zero hits"
        status: pass
    human_judgment: false
  - id: D5
    description: "Sentinel errors (ErrSessionNotFound, ErrCardNotFound) are errors.Is-detectable"
    requirement: VOC-04
    verification:
      - kind: unit
        ref: "pkg/database/session_repo_test.go#TestSessionRepository/GetByID_for_missing_id_returns_not-found_sentinel"
        status: pass
      - kind: unit
        ref: "pkg/database/card_repo_test.go#TestCardRepository/GetByID_for_missing_id_returns_not-found_sentinel"
        status: pass
    human_judgment: false

# Metrics
duration: 6min
completed: 2026-08-01
status: complete
---

# Phase 4 Plan 4: Repository Interfaces Summary

**SessionRepository (Go-owned CRUD) + CardRepository (Python-owned read-only) with compile-time interface checks, parameterized queries, and opaque fsrs_card binding**

## Performance

- **Duration:** 6 min
- **Started:** 2026-08-01T11:30:20Z
- **Completed:** 2026-08-01T11:36:21Z
- **Tasks:** 2
- **Files created:** 4

## Accomplishments
- SessionRepository interface with Create, UpdateState, AppendEvent, GetByID — full CRUD for Go-owned session tables
- CardRepository read-only interface with GetByID and ListByUnit — zero write methods (D-01/D-03-a enforced at type level)
- Card struct mirrors Python cards columns with FsrsCard as opaque string (Pitfall 5: no encoding/json import)
- All queries use explicit column lists, never SELECT * (Pitfall 4: schema drift fails loudly)
- Both constructors reject nil db and substitute no-op logger when nil (D-47)
- 19 new tests passing (10 session + 9 card) — all repository behaviors verified

## Task Commits

Each task was committed atomically:

1. **Task 1: SessionRepository interface + sqlite implementation + tests** - `71f5f7d` (feat)
2. **Task 2: CardRepository read-only interface + sqlite implementation + tests** - `24d222f` (feat)

## Files Created/Modified
- `pkg/database/session_repo.go` - SessionRepository interface, sqliteSessionRepository, NewSessionRepository, ErrSessionNotFound/ErrEmptyConnectionID sentinels
- `pkg/database/session_repo_test.go` - 10 table-driven tests: nil guards, CRUD, state transitions, events, multiple operations
- `pkg/database/card_repo.go` - Card struct, CardRepository read-only interface, sqliteCardRepository, NewCardRepository, ErrCardNotFound, scanCard helper, cardColumns constant
- `pkg/database/card_repo_test.go` - 9 table-driven tests: nil guards, field population, not-found, unit filtering, empty unit, opaque fsrs_card, column enumeration

## Decisions Made
- CardRepository is strictly read-only — no write methods exist; this is enforced at the Go type level, not by convention
- FsrsCard exposed as raw string; Go never parses the JSON — FSRS logic stays in Python via pybridge
- Explicit column list in all card queries; Python schema changes will produce compile-time-adjacent loud failures instead of silent data corruption
- scanCard helper extracted to share row scanning between GetByID and ListByUnit
- ErrSessionNotFound and ErrCardNotFound follow the same sentinel pattern as infra.ErrConnectionNotFound

## Deviations from Plan

None - plan executed exactly as written.

## Issues Encountered

None.

## User Setup Required

None - no external service configuration required.

## Next Phase Readiness
- Plan 05 (main.go wiring) can call NewSessionRepository and NewCardRepository at boot
- Plan 07 (session management) uses SessionRepository for lifecycle tracking
- Plan 10 (parent feedback) uses CardRepository to read cards for display
- Write-separation rule (D-03-a) is now enforced at the type level — no Go write path to cards exists

## Self-Check: PASSED

- `go test ./pkg/database/... -v -run "TestSessionRepository|TestCardRepository"` — 19/19 PASS
- `grep -rE "INSERT|UPDATE|DELETE" pkg/database/card_repo.go` — zero hits
- Card struct exposes FsrsCard as string; no encoding/json import for it
- Both constructors reject nil db and accept nil logger (tested)

---
*Phase: 04-database-schema-design*
*Completed: 2026-08-01*
