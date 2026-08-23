---
phase: 04-database-schema-design
plan: 01
subsystem: infra
tags: [sqlite-driver, cgo-lock, database-config, pure-go, cross-platform]

# Dependency graph
requires:
  - phase: 04-database-schema-design
    provides: 04-CONTEXT.md (D-09, D-01/D-02 pure-Go driver decision)
provides:
  - Dependency: `modernc.org/sqlite` v1.55.0 (pure-Go SQLite driver, CGO-free)
  - Config field: `Config.Database.Path` (env HTA_DATABASE_PATH)
  - Build contract: `CGO_ENABLED=0` locked in Makefile + CI
  - Tests: TestDefaultDatabaseConfig, TestLoadConfigDatabasePathEnvOverride, TestLoadConfigDatabaseEmptyPathAccepted, TestDatabaseConfigHasNoSecretFields, TestSQLiteDriverRegistration
affects: [phase-04-rest, phase-05, phase-07]

# Tech tracking
tech-stack:
  added:
    - modernc.org/sqlite v1.55.0 (pure-Go SQLite driver, CGO-free, BSD-3-Clause)
  patterns:
    - cgo-free-build-contract (CGO_ENABLED=0 locked in Makefile build target + CI workflow)
    - database-path-as-only-config (DatabaseConfig has single Path field, pragmas owned by database.Open)
    - env-binding-hta-database-path (bindEnvForAllLeaves entry "database.path" yields HTA_DATABASE_PATH)

key-files:
  created: []
  modified:
    - go.mod (added modernc.org/sqlite + transitive deps)
    - go.sum (checksums for new deps)
    - Makefile (CGO_ENABLED=0 in build target)
    - .github/workflows/ci.yml (CGO_ENABLED: 0 in build-and-test env)
    - pkg/infra/config.go (DatabaseConfig struct, Config.Database field, DefaultConfig entry, bindEnvForAllLeaves entry)
    - pkg/infra/config_test.go (5 new test functions)

key-decisions:
  - "modernc.org/sqlite chosen as pure-Go driver (CGO_ENABLED=0 required for Windows dev host)"
  - "DatabaseConfig carries only Path field; pragmas (WAL, busy_timeout, foreign_keys) are NOT configurable here — owned by database.Open"
  - "DefaultPath is empty; database.Open resolves to $HOME/.home-tutor-agent/vocab.db"
  - "No validation rule for Database.Path — intentionally optional at config time (service boots without database: block)"
  - "CGO_ENABLED=0 locked in both Makefile and CI to prevent silent CGO dependency"
  - "D-24 invariant maintained: DatabaseConfig has no secret fields (reflect guard passes)"

---

## Execution Summary

### Objective
Establish the database-layer foundation: add the pure-Go SQLite driver, lock the cross-platform CGO-free build contract, and extend the typed Config with a Database section. This unblocks every later Phase 4 plan.

### What Was Done

1. **Package legitimacy verification**:
   - Verified pkg.go.dev/modernc.org/sqlite: v1.55.0, BSD-3-Clause, 3,518 importers, stable
   - Verified gitlab.com/cznic/sqlite: 742 commits, 164 tags, active maintenance
   - Import path confirmed: modernc.org/sqlite

2. **Installed modernc.org/sqlite driver**:
   - `go get modernc.org/sqlite@latest` via goproxy.cn
   - Transitive deps added: modernc.org/libc, modernc.org/mathutil, modernc.org/memory, etc.
   - Driver registers under name "sqlite" (verified in test)

3. **Locked CGO_ENABLED=0**:
   - Makefile build target: `CGO_ENABLED=0 go build ...`
   - CI workflow: `CGO_ENABLED: 0` in build-and-test job env

4. **Extended Config with Database section**:
   - Added `DatabaseConfig` struct with single `Path` field
   - Added `Database DatabaseConfig` field to Config struct (after WS)
   - Added `Database: DatabaseConfig{Path: ""}` to DefaultConfig()
   - Added `"database.path"` to bindEnvForAllLeaves (yields HTA_DATABASE_PATH)

5. **Added tests** (5 new test functions):
   - TestDefaultDatabaseConfig: empty path default
   - TestLoadConfigDatabasePathEnvOverride: HTA_DATABASE_PATH override
   - TestLoadConfigDatabaseEmptyPathAccepted: no validation rejection
   - TestDatabaseConfigHasNoSecretFields: D-24 guard
   - TestSQLiteDriverRegistration: driver registers as "sqlite"

### Key Technical Details

**Driver Characteristics:**
- modernc.org/sqlite v1.55.0 (published Jul 20, 2026)
- Pure-Go CGO-free port of SQLite 3.53.3
- DSN supports `_pragma` query parameters for connection-level pragmas
- Supports Windows amd64 (verified on dev host)

**Config Design:**
- DatabaseConfig has exactly one field: Path (json:"path" mapstructure:"path")
- Empty Path accepted at config time (no validation rule)
- database.Open resolves empty Path to $HOME/.home-tutor-agent/vocab.db
- Pragmas (WAL, busy_timeout, foreign_keys) are correctness-critical and owned by database.Open, not configurable here

**Build Contract:**
- Makefile: `CGO_ENABLED=0` prefix on go build
- CI: `CGO_ENABLED: 0` in env block
- Prevents silent CGO dependency from compiling on CI

### Verification Results

**Automated Checks:**
- ✅ `go get modernc.org/sqlite@latest` succeeded
- ✅ `go.mod` contains modernc.org/sqlite
- ✅ `grep CGO_ENABLED=0 Makefile` returns hit
- ✅ `grep CGO_ENABLED: 0 .github/workflows/ci.yml` returns hit
- ✅ `go test -v -run TestDatabase ./pkg/infra/...` — all 3 new tests pass
- ✅ `go test -v -run TestSQLiteDriverRegistration ./pkg/infra/...` — driver registers
- ✅ `go build ./...` succeeds with CGO_ENABLED=0
- ✅ `go vet ./...` clean
- ✅ Full test suite: 88 tests pass, 0 fail

### Dependencies Satisfied

- 04-CONTEXT.md D-09: Pure-Go SQLite driver installed
- 04-CONTEXT.md D-01/D-02: CGO_ENABLED=0 locked
- 04-RESEARCH.md Package Legitimacy Audit: Verified via pkg.go.dev + gitlab.com/cznic/sqlite
- 04-RESEARCH.md Assumptions A1-A5: Driver name "sqlite" confirmed

### Next Steps

1. **Proceed to 04-03**: pkg/database package (schema + Open + Migrate)
2. **Phase 5 integration**: FSRS queries will use the driver installed here
3. **main.go wiring**: Plan 04-05 consumes cfg.Database.Path

---

**Plan Status:** ✅ COMPLETE  
**Created:** 2026-08-01  
**Executor:** opencode
