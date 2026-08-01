---
phase: 04-database-schema-design
plan: 05
subsystem: database
tags: [sqlite, bootstrap, lifecycle, migration, shutdown, repository]

# Dependency graph
requires:
  - phase: 04-database-schema-design
    provides: modernc.org/sqlite driver, CGO_ENABLED=0 lock, DatabaseConfig, Open(), Migrate(), SessionRepository, CardRepository
  - phase: 01-foundation
    provides: infra.Logger, infra.F(), infra.ErrField(), infra.NewServer, Config
  - phase: 02-websocket-communication-layer
    provides: ws.NewHub, ws.NewRouter, ws.NewServer, ws.ServerConfig
  - phase: 03-python-cli-integration
    provides: pybridge.NewBridge, PythonConfig
provides:
  - Database bootstrap in run(): database.Open + database.Migrate + NewCardRepository + NewSessionRepository
  - Deferred db.Close() ordered after listener shutdown (Pitfall 6)
  - Default path resolution: empty cfg.Database.Path → $HOME/.home-tutor-agent/vocab.db
  - homeDir() helper function
  - TestMainDatabaseConnection: proves Open+Migrate+repos wire at boot
  - TestMainDatabaseDefaultPath: proves empty path resolves to HOME default
affects: [phase-05, phase-07]

# Actuals (#2632)
actuals:
  tokens: 3200
  tasks: 1
  commits: 2

# Tech tracking
tech-stack:
  added: []
  patterns:
    - deferred-db-close (db.Close() via defer, ordered after all listener shutdown)
    - home-dir-default-resolution (empty Path → $HOME/.home-tutor-agent/vocab.db via homeDir())
    - underscore-assignment-placeholders (_ = cardRepo/sessionRepo with consumer comments)

key-files:
  created: []
  modified:
    - cmd/service/main.go (database bootstrap + shutdown wiring + homeDir helper)
    - cmd/service/main_test.go (TestMainDatabaseConnection + TestMainDatabaseDefaultPath)

key-decisions:
  - "db.Close() via defer ensures it runs AFTER shutdown block (HTTP/WS listeners stop first, then defer closes DB)"
  - "homeDir() uses os.UserHomeDir() with \".\" fallback for robustness"
  - "Database bootstrap placed after pyBridge, before RegisterMetrics — mirrors existing boot ordering"
  - "cardRepo/sessionRepo held with underscore-assignment + consumer comments (Phase 5/7), not unused-variable suppressed"

patterns-established:
  - "Database bootstrap pattern: Open → defer Close → Migrate → construct repos, all in run()"
  - "DB-lifecycle boot test: re-open DB read-only after shutdown, query sqlite_master for table existence"

requirements-completed: [VOC-04]

coverage:
  - id: D1
    description: "run() opens SQLite via database.Open, runs Migrate, constructs both repos, closes DB on shutdown"
    requirement: VOC-04
    verification:
      - kind: integration
        ref: "cmd/service/main_test.go#TestMainDatabaseConnection"
        status: pass
    human_judgment: false
  - id: D2
    description: "Empty cfg.Database.Path resolves to $HOME/.home-tutor-agent/vocab.db"
    requirement: VOC-04
    verification:
      - kind: integration
        ref: "cmd/service/main_test.go#TestMainDatabaseDefaultPath"
        status: pass
    human_judgment: false
  - id: D3
    description: "Existing boot tests (7 tests) remain green with database wiring in place"
    requirement: VOC-04
    verification:
      - kind: integration
        ref: "cmd/service/main_test.go#TestMainLoadsDefaultConfig through TestMainBootsWithNoWSConfigBlock"
        status: pass
    human_judgment: false
  - id: D4
    description: "db.Close() invoked in shutdown path (deferred after all listener shutdown)"
    requirement: VOC-04
    verification:
      - kind: other
        ref: "grep db.Close cmd/service/main.go — hit in deferred closure"
        status: pass
    human_judgment: false

# Metrics
duration: 19min
completed: 2026-08-01
status: complete
---

# Phase 4 Plan 5: Database Bootstrap Wiring Summary

**SQLite Open+Migrate+repo construction wired into run(), deferred db.Close() ordered after listener shutdown, default path resolution for empty cfg.Database.Path**

## Performance

- **Duration:** 19 min
- **Started:** 2026-08-01T19:40:05Z
- **Completed:** 2026-08-01T19:59:12Z
- **Tasks:** 1
- **Files modified:** 2

## Accomplishments
- database.Open + database.Migrate + NewCardRepository + NewSessionRepository wired into run() after pyBridge construction
- Deferred db.Close() ordered after all listener shutdown (Pitfall 6 mitigation — WAL files don't leak across restarts)
- Default path resolution: empty cfg.Database.Path → $HOME/.home-tutor-agent/vocab.db via homeDir() helper
- TestMainDatabaseConnection proves the full vertical slice: boot → Open → Migrate → repos → shutdown → verify tables exist
- TestMainDatabaseDefaultPath proves the empty-path → HOME default resolution on both Unix and Windows (USERPROFILE)
- All 7 existing boot tests remain green (no regression)

## Task Commits

Each task was committed atomically:

1. **Task 1 (RED): test for database bootstrap lifecycle** - `2b615e2` (test)
2. **Task 1 (GREEN): wire database.Open + Migrate + repos + db.Close** - `9d1a81a` (feat)

## Files Created/Modified
- `cmd/service/main.go` — Added `database` import, database bootstrap block (Open/Migrate/repos), deferred db.Close(), homeDir() helper
- `cmd/service/main_test.go` — Added `database/sql` + `modernc.org/sqlite` imports, TestMainDatabaseConnection, TestMainDatabaseDefaultPath, import_database_check helper

## Decisions Made
- db.Close() via Go defer ensures it runs AFTER the shutdown block completes (HTTP/WS listeners stop first, then defer closes DB)
- homeDir() returns os.UserHomeDir() result, falling back to "." on error
- Database bootstrap placed after pyBridge construction, before RegisterMetrics — consistent with existing boot ordering
- cardRepo/sessionRepo held via underscore-assignment with consumer comments, mirroring existing _ = pyBridge pattern

## Deviations from Plan

None - plan executed exactly as written.

## Issues Encountered

- Windows: os.UserHomeDir() checks USERPROFILE before HOME. TestMainDatabaseDefaultPath needed t.Setenv("USERPROFILE", tmpHome) in addition to HOME.

## Self-Check: PASSED

- `go test ./cmd/service/... -v` — 9/9 PASS (7 existing + 2 new)
- `go build ./...` — succeeds with CGO_ENABLED=0
- `grep database.Open cmd/service/main.go` — hit at line 133
- `grep database.Migrate cmd/service/main.go` — hit at line 153
- `grep db.Close cmd/service/main.go` — hit at line 143 (deferred closure)
- No cards/review_logs writes in main.go (D-03-a write-separation)

---

*Phase: 04-database-schema-design*
*Completed: 2026-08-01*
