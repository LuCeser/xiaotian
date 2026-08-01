# Phase 4: Database Schema Design - Research

**Researched:** 2026-08-01
**Domain:** SQLite schema reconciliation, CGO-free Go database access layer, multi-process WAL concurrency
**Confidence:** MEDIUM (CGO constraint and Python schema HIGH; modernc.org/sqlite driver specifics ASSUMED due to network restrictions)

## Summary

Phase 4 is an **architecture-and-patterns phase**, not a feature phase. The project's vocabulary/FSRS schema already exists in `vocab_manager.py` (lines 103-128) and is locked as canonical by Phase 3 D-09/D-10. Phase 4's job is to (1) document this reconciliation as an ADR, (2) add two Go-owned auxiliary tables (`sessions`, `session_events`) for session bookkeeping that Phase 7 needs, (3) establish the `pkg/database` repository pattern that all future phases use for direct SQLite access, and (4) wire the `sql.DB` connection into the Phase 1 service bootstrap.

The most critical technical finding: **`CGO_ENABLED=0` is confirmed via `go env`** on the Windows dev host, and the project's cross-compilation/cross-platform-debugging stance (MEMORY.md: "Windows local debug required") mandates a **pure-Go SQLite driver**. `mattn/go-sqlite3` (the dominant Go SQLite driver) requires CGO and cannot compile on Windows without a C toolchain. This makes **`modernc.org/sqlite` the only viable choice** — it is a pure-Go transpilation of the SQLite C source that registers under database/sql with zero CGO dependency `[ASSUMED]`.

The second critical finding: **the project does not explicitly enforce `CGO_ENABLED=0`** in the Makefile or CI workflow. The current `go env CGO_ENABLED=0` is auto-detected on Windows (no gcc). On the Linux CI runner (ubuntu-latest), CGO defaults to 1 because gcc is present. If the project later adopts `mattn/go-sqlite3` or any CGO dependency, it would compile on CI but fail locally on Windows. The planner should add explicit `CGO_ENABLED=0` to the Makefile build target and CI workflow to lock the cross-platform contract.

The concurrency model for Go/Python shared-database access is safe under WAL mode: Python (via `sqlite3` module) already enables `PRAGMA journal_mode=WAL` (vocab_manager.py line 94), which allows concurrent readers during writes. Go reads the `cards` table via `CardRepository` (read-only), writes only to `sessions`/`session_events` (write-separated per D-03-a). The `busy_timeout` pragma handles brief cross-process lock contention.

**Primary recommendation:** Use `modernc.org/sqlite` (pure Go), `SetMaxOpenConns(1)` for serialized Go database access, DSN with `_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(ON)`, and add explicit `CGO_ENABLED=0` to the build configuration.

<user_constraints>
## User Constraints (from CONTEXT.md)

### Locked Decisions

#### Schema Reconciliation Strategy
- **D-01:** Treat `vocab_manager.py` schema as canonical for vocabulary and FSRS data. Phase 4 does not modify the existing `cards` or `review_logs` tables. The Python implementation remains the permanent engine (Phase 3 D-09).
- **D-02:** Phase 4 extends the database with Go-owned auxiliary tables only. New tables serve Go concerns (session bookkeeping, state management) and avoid conflicts with the Python schema.
- **D-03:** Database file sharing strategy — Go and Python access the same SQLite database file (`vocab.db`) via `DICTATION_DB_PATH` environment variable. Go uses SQLite's built-in concurrency (WAL mode already enabled by vocab_manager) to safely read/write auxiliary tables while Python owns `cards`/`review_logs`.
- **D-03-a:** Clear write separation documentation. ADR must explicitly document: Python owns writes to `cards`/`review_logs` tables; Go owns writes to `sessions`/`session_events` tables; cross-system writes prohibited (no Go writes to Python tables, no Python writes to Go tables).

#### Go-Owned Auxiliary Data
- **D-04:** Minimal session bookkeeping tables only. Phase 4 creates `sessions` and `session_events` tables for session lifecycle tracking (IDLE → LISTENING → DICTATING → WAITING_PARENT → FEEDBACK).
- **D-05:** No TTS audio cache tables in Phase 4. Audio file path references from `pregenerate_dictation.py` are managed in-memory during Phase 6; persistent cache metadata can be added later if needed.
- **D-06:** Parent feedback data remains in `review_logs` per Phase 3 design. Phase 10 (FEED-*) may extend this with analytics tables, but Phase 4 does not pre-design those extensions.

#### Database Access Layer Design
- **D-07:** Repository pattern with `sql.DB`. Phase 4 creates `pkg/database` package with repository interfaces (`SessionRepository`, `CardRepository` for reads) that accept `*sql.DB` dependencies. Aligns with Phase 1 DI patterns (D-42/D-47 constructor injection).
- **D-08:** Minimal migration strategy. Phase 4 uses `CREATE TABLE IF NOT EXISTS` for auxiliary tables and checks column existence via `PRAGMA table_info` before `ALTER TABLE` (same pattern as vocab_manager's migration code). No formal migration framework; schema drift detection is manual.
- **D-08-a:** Schema drift detection method. Phase 5 integration tests include schema validation comparing Go auxiliary table schemas against expected definitions. Any drift raises blocking error before deployment.
- **D-08-b:** Operational responsibility assignment. Phase 5 executor validates schema consistency as part of integration test suite; CI pipeline enforces schema checks before each deployment.
- **D-08-c:** Reconciliation process. If drift detected, integration test fails with specific schema mismatch details; resolution requires explicit migration script before deployment can proceed.
- **D-09:** Database initialization in `cmd/service/main.go`. The `sql.DB` connection is opened during service startup, configured via `Config.DatabasePath` (new field extending Phase 1 Config), and injected into repositories. Connection lifetime matches service lifetime.

#### Schema Documentation Approach
- **D-10:** ADR for FSRS+SQLite ownership reconciliation. Phase 4 creates `.planning/adr/2026-08-01-fsrs-sqlite-ownership.md` documenting the decision that Python `vocab_manager.py` owns the canonical vocabulary/FSRS schema and Go extends it with auxiliary tables only. This resolves the pending architecture review todo.
- **D-10-a:** ADR must address 4 required elements: (1) Single source of truth declaration — Python vocab_manager.py is canonical FSRS+SQLite authority; (2) Write separation rules — Python writes cards/review_logs, Go writes sessions/session_events; (3) Schema modification process — Changes to Python schema require explicit ADR update; (4) Conflict resolution — If schema drift occurs, Python schema takes precedence, Go must adapt auxiliary tables.
- **D-11:** Schema documentation in code comments. Each table creation includes a comment header documenting purpose, key relationships, and ownership (Python vs Go). A README in `pkg/database` documents the overall schema architecture.
- **D-12:** No separate schema documentation file. The canonical schema definition lives in code (both Python's `vocab_manager.py` and Go's migration logic), keeping docs close to implementation.

### Claude's Discretion
- Exact column set for `sessions` and `session_events` tables (minimal fields needed for session lifecycle and state transitions).
- Repository interface method signatures (card read methods for Phase 5, session CRUD methods).
- `Config.DatabasePath` field location and default value (suggested: `$HOME/.home-tutor-agent/vocab.db` for consistency with Python defaults).
- Whether `CardRepository` is read-only (Go reads vocabulary data) or also has write methods for future parent-managed vocabulary operations (Phase 9).

### Deferred Ideas (OUT OF SCOPE)
- **Schema migration tooling** (Flyway/Go-migrate equivalent) → Phase 5/7 when sync complexity increases. Phase 4 uses minimal `CREATE TABLE IF NOT EXISTS` + column existence checks.
- **TTS audio cache metadata tables** → Phase 6 (TTS streaming). Phase 4 does not pre-design audio caching schema.
- **Parent feedback analytics tables** (performance tracking, retention metrics) → Phase 10 (FEED-04 requirement). Phase 4 keeps `review_logs` as-is.
- **Vocabulary management operations tables** (units, lessons, categories) → Phase 9 (VOC-01/02 requirements). Current schema has string fields; structured management is deferred.
- **Advanced schema validation/linting** → Future operational concern. Phase 4 focuses on functional correctness.
</user_constraints>

<phase_requirements>
## Phase Requirements

| ID | Description | Research Support |
|----|-------------|------------------|
| VOC-04 | System stores vocabulary data in SQLite database with proper schema | The schema already exists in `vocab_manager.py` (verified from source). Phase 4 documents it as canonical (D-01), adds Go-owned session tables (D-04), and establishes the repository pattern for Go to read vocabulary data (D-07 CardRepository). The ADR (D-10) formalizes the single-source-of-truth declaration. |
</phase_requirements>

## Project Constraints (from CLAUDE.md)

| Directive | Source | Research Impact |
|-----------|--------|-----------------|
| Go for WebSocket service, Python for business logic | CLAUDE.md Technology Stack | Phase 4 does NOT re-implement FSRS in Go — it reads Python's schema and adds Go session tables only |
| SQLite database (avoids PostgreSQL overhead) | CLAUDE.md Constraints | Confirmed: schema lives in shared `vocab.db`; no external DB server |
| 150MB memory budget for Go service | CLAUDE.md Constraints | `modernc.org/sqlite` pure-Go transpilation is heavier than CGO version but acceptable; `SetMaxOpenConns(1)` minimizes connection memory |
| 1C 1G VPS | CLAUDE.md Constraints | Single SQLite writer connection + WAL mode is the correct concurrency model for this resource envelope |
| Support 50-100 concurrent sessions | CLAUDE.md Constraints | WAL mode + busy_timeout handles concurrent read load; writes are low-frequency (session state transitions, not per-frame) |
| Windows local debug required | MEMORY.md | Forces `CGO_ENABLED=0` → `modernc.org/sqlite` (pure Go) instead of `mattn/go-sqlite3` (CGO) |
| China mirror installs | MEMORY.md | `go mod download` for modernc.org/sqlite may need GOPROXY mirror configuration on VPS |
| Python venvs via uv | MEMORY.md | Database path shared via env var, not Python venv; no venv change needed for Phase 4 |

## Architectural Responsibility Map

| Capability | Primary Tier | Secondary Tier | Rationale |
|------------|-------------|----------------|-----------|
| Vocabulary/FSRS schema (cards, review_logs) | Database (Python-owned) | — | `vocab_manager.py` is canonical (D-01/D-09); Go never writes these tables |
| FSRS algorithm execution | Python subprocess (pybridge) | — | FSRS logic stays in Python (Phase 3 D-09); Go orchestrates via `Executor` interface, never re-implements |
| Session lifecycle bookkeeping | Database (Go-owned) | API/Backend | `sessions`/`session_events` are Go-only tables (D-02/D-04); SessionRepository manages them |
| Card read access (Go side) | Database (Go read-only) | — | `CardRepository` reads `cards` table directly for fast lookup without subprocess overhead (D-07) |
| Database connection management | API/Backend (service bootstrap) | Database | `sql.DB` opened in `cmd/service/main.go`, injected into repositories (D-09); connection lifetime = service lifetime |
| Schema migration (Go tables) | Database (Go-owned) | — | `CREATE TABLE IF NOT EXISTS` + `PRAGMA table_info` pattern (D-08); no migration framework |
| Schema drift detection | Testing | CI | Phase 5 integration tests validate schema (D-08-a); CI enforces before deploy (D-08-b) |

## Standard Stack

### Core

| Library | Version | Purpose | Why Standard |
|---------|---------|---------|--------------|
| `modernc.org/sqlite` | `[ASSUMED: latest, not verified via proxy due to network restrictions]` | Pure-Go SQLite driver for `database/sql` | The ONLY viable CGO-free SQLite driver for Go. Required because `CGO_ENABLED=0` is confirmed on Windows dev host. `mattn/go-sqlite3` (the dominant alternative) requires CGO and cannot compile without a C toolchain. `[ASSUMED]` |
| `database/sql` (stdlib) | go 1.26 | Database access interface | Standard library; `modernc.org/sqlite` implements `driver.Driver` for it. Already the established Go DB pattern. |
| `github.com/pkg/errors` | v0.9.1 (in go.mod) | Error wrapping | Already used across `pkg/infra` and `pkg/pybridge`; consistent sentinel-error pattern (`errors.Is`/`errors.As`). |

### Supporting

| Library | Version | Purpose | When to Use |
|---------|---------|---------|-------------|
| `github.com/xiaotian/home-tutor-agent/pkg/infra` | internal | Config, Logger, DI patterns | Config extension with `DatabasePath`; `infra.Logger` for structured logging; constructor DI pattern |
| `github.com/xiaotian/home-tutor-agent/pkg/pybridge` | internal | FSRS write operations | Phase 5+ writes FSRS state via `pybridge.Executor.RunReview`; Go never writes `cards`/`review_logs` directly |

### Alternatives Considered

| Instead of | Could Use | Tradeoff |
|------------|-----------|----------|
| `modernc.org/sqlite` | `mattn/go-sqlite3` | `mattn` is faster and more mature, but REQUIRES CGO (`CGO_ENABLED=1`). Breaks Windows local debug (MEMORY.md hard requirement). **Not viable for this project.** |
| `modernc.org/sqlite` | `crawshaw.io/sqlite` / `zombiezen.com/go/sqlite` | These are direct-binding wrappers (not `database/sql` compatible). Breaks the standard `*sql.DB` DI pattern established in D-07. Would require custom connection management. **Not recommended.** |
| `SetMaxOpenConns(1)` | Separate read/write pools | WAL allows concurrent readers, so multiple read connections could improve read throughput. BUT adds complexity (two `*sql.DB` instances, connection lifecycle management). For 1C/1G VPS with 50-100 sessions, the bottleneck is TTS generation (Python subprocess), not DB read throughput. **Simple single connection is correct for Phase 4.** |
| `CREATE TABLE IF NOT EXISTS` | `golang-migrate` / `goose` / `atlas` | Formal migration frameworks provide versioning, rollback, and drift detection. But D-08 explicitly defers migration tooling to Phase 5/7. The Python side uses the same `CREATE TABLE IF NOT EXISTS` pattern (vocab_manager.py lines 102-132). **Consistency with Python > framework features for Phase 4.** |

**Installation:**
```bash
go get modernc.org/sqlite
```

**Version verification:** Could NOT verify via `go list -m -versions` or Go proxy (network restrictions in this environment blocked `proxy.golang.org`). The planner/executor MUST run `go get modernc.org/sqlite` and verify the version resolves. This is flagged as a `[ASSUMED]` dependency — the planner should add a `checkpoint:human-verify` task before `go get`.

## Package Legitimacy Audit

> The Go ecosystem package-legitimacy check could not be executed (network restrictions blocked `proxy.golang.org`, and the `gsd-tools query package-legitimacy` command does not support the Go ecosystem).

| Package | Registry | Age | Downloads | Source Repo | Verdict | Disposition |
|---------|----------|-----|-----------|-------------|---------|-------------|
| `modernc.org/sqlite` | Go module proxy | `[ASSUMED: ~5+ years]` | `[ASSUMED: widely used]` | `gitlab.com/cznic/sqlite` | `[ASSUMED]` — not verified this session | Flagged — planner must add `checkpoint:human-verify` before `go get` |

**Packages flagged for human verification:**
- `modernc.org/sqlite` — discovered from training knowledge and referenced in project CONTEXT.md/EVALUATION.md, but NOT verified against the Go module registry this session due to network restrictions. The planner MUST verify:
  1. `go get modernc.org/sqlite` resolves successfully
  2. The import path and driver name (`"sqlite"`) are correct
  3. The DSN format matches the `_pragma=` parameter style
  4. No malicious postinstall/hooks (Go modules don't have postinstall scripts, but verify the module's `go.sum` entry matches `sum.golang.org`)

*Rationale for `modernc.org/sqlite` despite no registry verification:* The package is the well-known pure-Go SQLite transpilation maintained by Jan Mercl (cznic), hosted at `gitlab.com/cznic/sqlite`. It is the standard recommendation in the Go community for CGO-free SQLite. The CGO constraint (`CGO_ENABLED=0` confirmed via `go env`) leaves no alternative. However, `[ASSUMED]` status is maintained per protocol until verified.

## Architecture Patterns

### System Architecture Diagram

```
                    ┌─────────────────────────────────────────────┐
                    │           vocab.db (SQLite, WAL mode)        │
                    │                                             │
                    │  ┌─────────────┐    ┌──────────────────┐   │
                    │  │ Python-owned│    │  Go-owned         │   │
                    │  │ tables      │    │  tables           │   │
                    │  │             │    │                   │   │
                    │  │ • cards     │    │ • sessions        │   │
                    │  │ • review_   │    │ • session_events  │   │
                    │  │   logs      │    │                   │   │
                    │  └──────┬──────┘    └────────┬──────────┘   │
                    │         │                    │              │
                    │  WAL mode: 1 writer + N readers             │
                    │  busy_timeout: 5000ms                       │
                    └─────────┼────────────────────┼──────────────┘
                              │                    │
                    ┌─────────┴──────┐    ┌────────┴───────────┐
                    │  Python side   │    │   Go side          │
                    │                │    │                    │
   FSRS writes ───► │ vocab_manager  │    │ sql.DB (1 conn)    │
   (due/review)     │ .py via        │    │   │                │
                    │ pybridge       │    │   ├─ CardRepo      │
                    │                │    │   │  (READ-ONLY     │
                    │ sqlite3 module │    │   │   cards table)  │
                    │ PRAGMA WAL     │    │   │                 │
                    │ BEGIN IMMEDIATE│    │   ├─ SessionRepo    │
                    │ for fetch-lock │    │   │  (READ/WRITE     │
                    └────────────────┘    │   │   sessions/     │
                                          │   │   session_events)│
                                          │   │                  │
                                          │   └─ Migrate()       │
                                          │     (CREATE TABLE    │
                                          │      IF NOT EXISTS)  │
                                          │                      │
                                          │ cmd/service/main.go  │
                                          │ opens at boot (D-09) │
                                          └──────────────────────┘

Data flow:
  1. Python writes cards/review_logs (via pybridge subprocess, WAL serializes)
  2. Go reads cards via CardRepository (WAL allows concurrent read during Python write)
  3. Go writes sessions/session_events via SessionRepository (separate tables, no conflict)
  4. busy_timeout handles brief lock contention between Go and Python writers
  5. Write separation (D-03-a): Go NEVER writes cards/review_logs; Python NEVER writes sessions
```

### Recommended Project Structure
```
pkg/
├── database/
│   ├── db.go              # Open() constructor: sql.Open + pragma setup + SetMaxOpenConns(1)
│   ├── migrate.go         # Migrate(): CREATE TABLE IF NOT EXISTS for Go-owned tables
│   ├── schema.go          # Schema constants, table DDL strings, column definitions
│   ├── session_repo.go    # SessionRepository interface + sqliteSessionRepository impl
│   ├── session_repo_test  # Table-driven unit tests (manual fakes or in-memory SQLite)
│   ├── card_repo.go       # CardRepository interface (read-only) + sqliteCardRepository impl
│   ├── card_repo_test     # Table-driven unit tests
│   ├── migrate_test.go    # Migration idempotency tests
│   └── README.md          # Schema architecture overview (D-11)
└── infra/
    └── config.go          # Extended with DatabasePath field (D-09)
```

### Pattern 1: Repository Interface with Constructor DI (D-07, D-42, D-47)

**What:** Each table group gets a small, focused repository interface. The concrete implementation accepts `*sql.DB` and `infra.Logger` via constructor. Tests inject manual fakes (D-48).

**When to use:** All database access in Phase 4+.

**Example:**
```go
// Source: Pattern derived from pkg/pybridge/executor.go (Phase 3) and
// pkg/infra/connection.go (Phase 1 D-42/D-47 constructor DI)

// CardRepository provides read-only access to the Python-owned cards table.
// Go NEVER writes to this table — FSRS state updates flow through
// pybridge.Executor.RunReview (Phase 3 D-09).
type CardRepository interface {
    // GetByID returns the card with the given database ID.
    GetByID(ctx context.Context, id int64) (Card, error)
    // ListByUnit returns cards in the named unit (empty unit = all).
    ListByUnit(ctx context.Context, unit string) ([]Card, error)
}

// SessionRepository manages Go-owned session bookkeeping tables.
type SessionRepository interface {
    // Create inserts a new session record, returning its ID.
    Create(ctx context.Context, session Session) (int64, error)
    // UpdateState transitions a session to the given lifecycle state.
    UpdateState(ctx context.Context, id int64, state SessionState) error
    // AppendEvent records a session lifecycle event.
    AppendEvent(ctx context.Context, event SessionEvent) error
}

// sqliteCardRepository is the concrete implementation.
// Compile-time interface satisfaction check (D-43, mirrors pybridge pattern).
var _ CardRepository = (*sqliteCardRepository)(nil)

type sqliteCardRepository struct {
    db     *sql.DB
    logger infra.Logger
}

// NewCardRepository constructs a CardRepository from an open *sql.DB.
// logger may be nil — substituted with NewNoOpLogger() per D-47.
func NewCardRepository(db *sql.DB, logger infra.Logger) (CardRepository, error) {
    if db == nil {
        return nil, errors.New("database/db: *sql.DB must not be nil")
    }
    if logger == nil {
        logger = infra.NewNoOpLogger()
    }
    return &sqliteCardRepository{db: db, logger: logger}, nil
}
```

### Pattern 2: Database Connection Bootstrap (D-09)

**What:** A single `Open()` function opens the SQLite file, applies pragmas, and returns a configured `*sql.DB`. Called once at service startup.

**When to use:** `cmd/service/main.go` boot sequence.

**Example:**
```go
// Source: Pattern derived from vocab_manager.py get_db() (lines 87-95)
// and Phase 1 infra.NewServer() constructor pattern.

// Open returns a *sql.DB configured for shared multi-process WAL access.
// The caller is responsible for Close() at service shutdown.
//
// Pragmas applied:
//   - journal_mode=WAL: enables concurrent readers during writes (matches Python)
//   - busy_timeout=5000: cross-process write contention waits up to 5s
//   - foreign_keys=ON: SQLite defaults to OFF; we want FK enforcement
//   - synchronous=NORMAL: good WAL balance of safety and speed
func Open(dbPath string) (*sql.DB, error) {
    if dbPath == "" {
        return nil, errors.New("database/db: dbPath must not be empty")
    }
    // Create parent directory (mirrors vocab_manager.py get_db lines 89-91)
    dir := filepath.Dir(dbPath)
    if dir != "" && dir != "." {
        if err := os.MkdirAll(dir, 0o755); err != nil {
            return nil, errors.Wrap(err, "database/db: create db directory")
        }
    }

    // DSN with pragmas. The _pragma= query parameter is the modernc.org/sqlite
    // convention for applying PRAGMA statements at connection open time.
    // [ASSUMED: DSN format not verified this session — see Package Legitimacy Audit]
    dsn := fmt.Sprintf("file:%s?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(ON)&_pragma=synchronous(NORMAL)",
        dbPath)

    db, err := sql.Open("sqlite", dsn)
    if err != nil {
        return nil, errors.Wrap(err, "database/db: sql.Open")
    }

    // SetMaxOpenConns(1) serializes all Go DB operations through one
    // connection. This prevents SQLITE_BUSY errors within Go's own pool.
    // SQLite serializes writes regardless; WAL allows concurrent reads but
    // the single connection keeps the model simple and correct for the
    // 1C/1G VPS scale where DB throughput is NOT the bottleneck.
    db.SetMaxOpenConns(1)
    db.SetMaxIdleConns(1)
    db.SetConnMaxLifetime(0) // Connection lives for service lifetime

    return db, nil
}
```

### Pattern 3: Idempotent Migration (D-08)

**What:** `CREATE TABLE IF NOT EXISTS` for Go-owned tables. Checks column existence via `PRAGMA table_info` before `ALTER TABLE`. Mirrors vocab_manager.py lines 102-132.

**When to use:** Called at service startup after `Open()`, before repository construction.

**Example:**
```go
// Source: Mirrors vocab_manager.py init_db() (lines 101-132).

// Migrate creates Go-owned auxiliary tables if they do not exist.
// Idempotent: safe to call on every boot. Does NOT touch Python-owned
// tables (cards, review_logs) — those are managed by vocab_manager.py.
func Migrate(ctx context.Context, db *sql.DB) error {
    _, err := db.ExecContext(ctx, `
        CREATE TABLE IF NOT EXISTS sessions (
            id INTEGER PRIMARY KEY AUTOINCREMENT,
            connection_id TEXT NOT NULL,
            state TEXT NOT NULL DEFAULT 'IDLE',
            word_count INTEGER NOT NULL DEFAULT 0,
            created_at TEXT NOT NULL DEFAULT (datetime('now')),
            updated_at TEXT NOT NULL DEFAULT (datetime('now'))
        );
        CREATE TABLE IF NOT EXISTS session_events (
            id INTEGER PRIMARY KEY AUTOINCREMENT,
            session_id INTEGER NOT NULL,
            event_type TEXT NOT NULL,
            payload TEXT NOT NULL DEFAULT '',
            occurred_at TEXT NOT NULL DEFAULT (datetime('now')),
            FOREIGN KEY (session_id) REFERENCES sessions(id)
        );
        CREATE INDEX IF NOT EXISTS idx_sessions_connection ON sessions(connection_id);
        CREATE INDEX IF NOT EXISTS idx_events_session ON session_events(session_id);
    `)
    if err != nil {
        return errors.Wrap(err, "database/migrate: create tables")
    }
    return nil
}
```

### Anti-Patterns to Avoid

- **Go writing to `cards`/`review_logs`:** Violates D-01/D-03-a. All FSRS state changes MUST flow through `pybridge.Executor.RunReview` so Python remains the single authority. Go reads are fine (WAL allows concurrent reads); Go writes to Python tables are prohibited.
- **Using `mattn/go-sqlite3`:** Requires CGO. Cannot compile on Windows dev host (`CGO_ENABLED=0` confirmed). Breaks the MEMORY.md "Windows local debug required" constraint.
- **Multiple `*sql.DB` instances to the same file:** Creates unpredictable connection pool behavior with SQLite. Use ONE `*sql.DB` with `SetMaxOpenConns(1)` for the Go service.
- **Skipping `busy_timeout`:** Without it, cross-process write contention (Go writes sessions while Python writes cards) immediately returns `SQLITE_BUSY` instead of waiting. Always set `busy_timeout=5000` in the DSN.
- **Setting `synchronous=FULL` with WAL:** Overkill for this use case. `NORMAL` provides near-full crash safety with WAL mode at much better performance. `FULL` halves write throughput for no practical benefit on a dictation tutor.
- **Using `datetime('now')` vs Go timestamps:** SQLite's `datetime('now')` returns UTC strings. vocab_manager.py uses the same convention (Python `datetime.now(timezone.utc)`). For consistency, let SQLite handle timestamps in DDL defaults, and use UTC string comparison in queries.

## Don't Hand-Roll

| Problem | Don't Build | Use Instead | Why |
|---------|-------------|-------------|-----|
| SQLite driver | CGO bindings via `cgo` + libsqlite3 | `modernc.org/sqlite` (pure Go) | Transpiled from C source; handles all SQLite edge cases (encoding, collation, FTS, etc.) that a hand-rolled binding would miss |
| Connection pooling | Custom goroutine-based DB access queue | `database/sql` + `SetMaxOpenConns(1)` | stdlib `database/sql` already handles connection lifecycle, idle reaping, and context cancellation. Custom pooling adds bugs for zero benefit. |
| Migration versioning | Custom schema-version table + migration runner | `CREATE TABLE IF NOT EXISTS` + `PRAGMA table_info` (D-08) | Phase 4 has 2 tables. A migration framework is premature (D-08 explicitly defers it). Python uses the same pattern (vocab_manager.py lines 126-131). |
| FSRS parameter storage | Custom Go table for stability/difficulty/due | Read `cards.fsrs_card` JSON column via CardRepository | The FSRS state is already stored as a JSON blob in the `cards` table by `vocab_manager.py`. Don't duplicate it. |
| Session state machine | Custom state-transition validation table | `sessions.state` TEXT column with app-level validation | Session states (IDLE → LISTENING → ...) are validated in Go application logic, not DB constraints. A separate state-machine table adds complexity for 5 states. |

**Key insight:** Phase 4 is a **pattern-establishment phase**. The temptation is to build infrastructure (migration framework, ORM, connection pool manager). Resist it. The two auxiliary tables + two repository interfaces + one `Open()` function are all the infrastructure Phase 5-12 need. Add complexity only when a concrete future phase demands it.

## Common Pitfalls

### Pitfall 1: CGO_ENABLED inconsistency between dev and CI
**What goes wrong:** Code compiles on Linux CI (CGO defaults to 1, gcc present) but fails on Windows dev host (CGO auto-disabled, no gcc). Or: `go get mattn/go-sqlite3` succeeds on CI, developer can't build locally.
**Why it happens:** The Makefile and CI workflow do NOT explicitly set `CGO_ENABLED=0`. It's auto-detected per-platform. `go env CGO_ENABLED` returned `0` on this Windows host (verified this session) but would return `1` on ubuntu-latest CI.
**How to avoid:** (1) Use `modernc.org/sqlite` (pure Go, works everywhere). (2) Add `CGO_ENABLED=0` to the Makefile build target and CI workflow explicitly to lock the contract:
```makefile
build:
	CGO_ENABLED=0 go build -ldflags '-s -w' -o $(BINARY) ./cmd/service
```
**Warning signs:** `go build` succeeds on CI but fails on Windows with `cgo: C compiler "gcc" not found`.

### Pitfall 2: SQLITE_BUSY errors from concurrent Go/Python writes
**What goes wrong:** Go writes to `sessions` table while Python writes to `cards` table. Without `busy_timeout`, the second writer immediately gets `SQLITE_BUSY` error.
**Why it happens:** SQLite's default `busy_timeout` is 0. In WAL mode, multiple readers can run concurrently with one writer, but two simultaneous writers (even to different tables) contend on the single write lock.
**How to avoid:** Always set `busy_timeout=5000` (5 seconds) in the DSN. This makes the second writer wait up to 5s for the lock instead of failing immediately. Also: Go and Python write to DIFFERENT tables (D-03-a), so write contention is brief (microseconds for small row counts).
**Warning signs:** Intermittent `database is locked` errors in logs, especially under concurrent session load.

### Pitfall 3: DSN format mismatch between drivers
**What goes wrong:** Developer copies a `mattn/go-sqlite3` DSN (which uses `?_journal_mode=WAL&_busy_timeout=5000`) into `modernc.org/sqlite` code (which uses `?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)`). Pragmas silently don't apply.
**Why it happens:** The two drivers use different DSN parameter conventions. `mattn` uses underscore-prefixed query params; `modernc` uses `_pragma=name(value)` syntax.
**How to avoid:** Use the `modernc.org/sqlite` `_pragma=` convention consistently. Verify pragmas applied correctly at startup by querying `PRAGMA journal_mode` and asserting it returns `wal`.
**Warning signs:** WAL mode not actually enabled (verify with `SELECT * FROM pragma_journal_mode`); `database is locked` errors despite DSN claiming `busy_timeout`.

### Pitfall 4: Stale schema assumption — `cards` table column drift
**What goes wrong:** Phase 4 code assumes the `cards` table has columns X/Y/Z, but `vocab_manager.py` later adds or renames a column. Go queries fail at runtime.
**Why it happens:** The `cards` schema is owned by Python (D-01). Phase 4 reads it via CardRepository. If vocab_manager.py's `init_db()` adds a new column (via its `PRAGMA table_info` + `ALTER TABLE` migration pattern, lines 126-131), Go's CardRepository must be updated to match.
**How to avoid:** (1) The ADR (D-10-a element 3) requires that Python schema changes need an explicit ADR update, which surfaces the change for Go-side review. (2) Phase 5 integration tests (D-08-a) validate schema against expected definitions. (3) Use `SELECT explicit_column_list` not `SELECT *` in CardRepository queries — explicit columns fail loudly if the schema changes, rather than silently returning wrong data.
**Warning signs:** `no such column` errors in production; `fsrs_card` JSON structure changes.

### Pitfall 5: `fsrs_card` column is opaque JSON — don't parse it in Go
**What goes wrong:** Developer tries to parse the `fsrs_card` JSON column in Go to extract stability/difficulty/due for display, duplicating FSRS logic.
**Why it happens:** The column looks like structured data that Go "should" be able to parse.
**How to avoid:** The `fsrs_card` column is the serialized FSRS `Card` object (Python `fsrs` library). Its internal JSON structure is Python-owned. Go should NOT parse it — if Go needs stability/difficulty/due, it should call `pybridge.Executor.RunDue` (which returns those fields as typed Go structs: `DueCard.Stability`, `DueCard.Difficulty`, `DueCard.Due`). CardRepository returns the raw `fsrs_card` column as an opaque string.
**Warning signs:** Go code importing `encoding/json` to unmarshal `fsrs_card` into a struct that mirrors Python's `fsrs.Card` internal fields.

### Pitfall 6: Forgetting to Close() the database on shutdown
**What goes wrong:** Service shuts down without closing `sql.DB`. SQLite WAL files (`vocab.db-wal`, `vocab.db-shm`) are left behind, potentially growing large over multiple unclean shutdowns.
**Why it happens:** The Phase 1 shutdown sequence (main.go lines 272-310) closes HTTP servers and the WS subsystem but does not yet have a database to close.
**How to avoid:** Add `db.Close()` to the shutdown sequence in `cmd/service/main.go`, AFTER all repositories have stopped using it, BEFORE the process exits. Use `defer db.Close()` in the boot function, or add it to the existing `shutdownCtx` block.
**Warning signs:** `vocab.db-wal` file growing unbounded; checkpoint warnings in logs.

## Code Examples

### Complete: Database Open + Migrate + Repository Construction

```go
// Source: Derived from Phase 1 main.go boot pattern + vocab_manager.py get_db/init_db.

package main

// In cmd/service/main.go run() function, after logger construction:

// Phase 4 (D-09): open the shared SQLite database.
// The path comes from Config.DatabasePath (new field, mirrors PythonConfig.VocabDBPath).
// sql.DB lifetime = service lifetime. Closed during shutdown.
dbPath := cfg.DatabasePath
if dbPath == "" {
    dbPath = filepath.Join(homeDir(), ".home-tutor-agent", "vocab.db")
}
db, err := database.Open(dbPath)
if err != nil {
    logger.Error("database open failed",
        infra.F("component", "infra.main"),
        infra.F("path", dbPath),
        infra.ErrField(err),
    )
    return 1
}
defer func() {
    if err := db.Close(); err != nil {
        logger.Warn("database close error",
            infra.F("component", "infra.main"),
            infra.ErrField(err),
        )
    }
}()

// Run idempotent migration for Go-owned tables (D-08).
// Does NOT touch Python-owned cards/review_logs.
if err := database.Migrate(ctx, db); err != nil {
    logger.Error("database migration failed",
        infra.F("component", "infra.main"),
        infra.ErrField(err),
    )
    return 1
}

// Construct repositories (D-07, D-42 constructor DI).
cardRepo, err := database.NewCardRepository(db, logger)
if err != nil {
    logger.Error("card repository construction failed",
        infra.F("component", "infra.main"),
        infra.ErrField(err),
    )
    return 1
}
sessionRepo, err := database.NewSessionRepository(db, logger)
if err != nil {
    logger.Error("session repository construction failed",
        infra.F("component", "infra.main"),
        infra.ErrField(err),
    )
    return 1
}
_ = cardRepo     // consumed by Phase 5 FSRS orchestration
_ = sessionRepo  // consumed by Phase 7 session management
```

### Config Extension

```go
// Source: Pattern from pkg/infra/config.go PythonConfig (Phase 3).

// In Config struct, add after the WS field:
type Config struct {
    // ... existing fields ...
    Database DatabaseConfig `json:"database" mapstructure:"database"`
}

// DatabaseConfig tunes the SQLite database connection (Phase 4, D-09).
// Holds only the path; pragmas are hardcoded in database.Open() because they
// are correctness-critical (WAL mode, busy_timeout, foreign_keys) and should
// not be operator-configurable.
type DatabaseConfig struct {
    // Path is the SQLite database file location. Go and Python share this
    // file (D-03). Default mirrors PythonConfig.VocabDBPath. The value
    // SHOULD match cfg.Python.VocabDBPath so both sides hit the same file.
    Path string `json:"path" mapstructure:"path"`
}

// In DefaultConfig():
Database: DatabaseConfig{
    Path: "",  // empty → database.Open resolves to $HOME/.home-tutor-agent/vocab.db
},

// In bindEnvForAllLeaves, add:
"database.path",

// In Validate(), add:
// Database.Path is optional (defaults resolve at Open time), so no validation
// needed unless we want to enforce non-empty. For Phase 4, allow empty so
// the service boots without a config file (Phase 1 D-03 env-default).
```

### Testing: In-Memory SQLite for Repository Tests

```go
// Source: Pattern from pkg/pybridge/bridge_test.go (Phase 3, manual fakes D-48).
// For repository tests, use a real in-memory SQLite database instead of a
// fake — this tests the actual SQL queries against a real SQLite engine.

package database

import (
    "context"
    "testing"

    "github.com/xiaotian/home-tutor-agent/pkg/infra"
)

// newTestDB returns an in-memory SQLite database with Go-owned tables
// migrated. Uses modernc.org/sqlite (same driver as production). The :memory:
// DSN creates a fresh private database per test — no cross-test leakage.
// Caller does NOT need to Close() — the test framework tears down the
// process; for longer-lived tests use t.Cleanup(func(){ db.Close() }).
func newTestDB(t *testing.T) *sql.DB {
    t.Helper()
    // In-memory DSN: each connection gets its own private database.
    // With SetMaxOpenConns(1) all queries hit the same in-memory DB.
    db, err := sql.Open("sqlite", ":memory:")
    if err != nil {
        t.Fatalf("open test db: %v", err)
    }
    db.SetMaxOpenConns(1)
    if err := Migrate(context.Background(), db); err != nil {
        t.Fatalf("migrate test db: %v", err)
    }
    t.Cleanup(func() { db.Close() })
    return db
}

func TestSessionRepository_Create(t *testing.T) {
    tests := []struct {
        name    string
        session Session
        wantErr bool
    }{
        {
            name: "valid session",
            session: Session{ConnectionID: "conn-abc", State: StateIdle},
            wantErr: false,
        },
        {
            name: "empty connection id rejected",
            session: Session{ConnectionID: "", State: StateIdle},
            wantErr: true,
        },
    }

    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            db := newTestDB(t)
            repo, err := NewSessionRepository(db, infra.NewNoOpLogger())
            if err != nil {
                t.Fatalf("construct repo: %v", err)
            }
            id, err := repo.Create(context.Background(), tt.session)
            if (err != nil) != tt.wantErr {
                t.Errorf("Create() error = %v, wantErr %v", err, tt.wantErr)
            }
            if !tt.wantErr && id <= 0 {
                t.Errorf("Create() returned invalid id: %d", id)
            }
        })
    }
}
```

## State of the Art

| Old Approach | Current Approach | When Changed | Impact |
|--------------|------------------|--------------|--------|
| `mattn/go-sqlite3` (CGO-based) | `modernc.org/sqlite` (pure Go transpilation) | modernc.org/sqlite stable since ~2020 | Pure-Go SQLite enables cross-compilation and CGO-free builds. Slightly slower than CGO version but eliminates C toolchain dependency. |
| DELETE-rollback journal | WAL (Write-Ahead Logging) | SQLite 3.7.0 (2010) | WAL allows concurrent readers during writes. Critical for Go/Python shared-database access. vocab_manager.py already uses WAL. |
| ORM-based repositories (GORM, etc.) | `database/sql` + plain SQL | Go community consensus | ORMs add complexity and hide SQL. For 2 simple tables with known schema, plain SQL + repository interface is simpler and more transparent. |
| Migration frameworks (golang-migrate) | `CREATE TABLE IF NOT EXISTS` | — | For 2 tables, a migration framework is premature. Consistent with Python side's approach (vocab_manager.py uses the same idempotent pattern). |

**Deprecated/outdated:**
- `mattn/go-sqlite3` with `CGO_ENABLED=1`: Not viable for this project (Windows dev constraint).
- `crawshaw.io/sqlite`: Archived/deprecated; the successor is `zombiezen.com/go/sqlite`, but neither uses `database/sql` interface.
- Rollback journal mode: WAL mode is strictly better for concurrent access. vocab_manager.py already sets WAL.

## Assumptions Log

> Claims tagged `[ASSUMED]` that the planner should verify before execution.

| # | Claim | Section | Risk if Wrong |
|---|-------|---------|---------------|
| A1 | `modernc.org/sqlite` exists on the Go module registry and is the canonical pure-Go SQLite driver | Standard Stack | If the module path is wrong or archived, `go get` fails. Mitigation: executor runs `go get` and verifies resolution. |
| A2 | `modernc.org/sqlite` registers under the driver name `"sqlite"` (not `"sqlite3"`) for `database/sql` | Code Examples | If wrong, `sql.Open("sqlite", ...)` returns "driver not found". Mitigation: check driver name in package docs or `sql.Drivers()` after import. |
| A3 | DSN format uses `?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)` query parameter syntax | Code Examples | If wrong, pragmas silently don't apply (WAL not enabled, busy_timeout stays 0). Mitigation: verify with `SELECT * FROM pragma_journal_mode` after open. |
| A4 | `modernc.org/sqlite` is mature, widely-used, and safe for production use | Standard Stack | If the package has unresolved bugs or is abandoned, Go database access is unreliable. Mitigation: check package age, issue tracker, recent commits at `gitlab.com/cznic/sqlite`. |
| A5 | `mattn/go-sqlite3` cannot compile with `CGO_ENABLED=0` | Alternatives Considered | If false (i.e., a pure-Go build mode exists), the driver choice rationale changes. Well-established: mattn is a CGO wrapper, no pure-Go mode exists. |
| A6 | SQLite WAL mode allows a second process to read while the first process writes, even to different tables | Architecture Diagram | If false, Go reads would block during Python writes. Well-established: WAL readers see a consistent snapshot and never block writers. |
| A7 | `SetMaxOpenConns(1)` is the correct pooling strategy for SQLite with Go's `database/sql` | Code Examples | If wrong, could cause unnecessary serialization. Alternative: separate read/write pools. For 1C/1G VPS with low-frequency writes, single-connection is the pragmatic correct choice. |
| A8 | The `sessions`/`session_events` table column set proposed in the code example is sufficient for Phase 7 needs | Code Examples | If Phase 7 needs additional columns, a migration is needed. Mitigated by D-08-b: ALTER TABLE via `PRAGMA table_info` check pattern. |
| A9 | On Linux CI (ubuntu-latest), `CGO_ENABLED` defaults to 1 | Pitfall 1 | If the Go setup action already sets `CGO_ENABLED=0`, the inconsistency risk is lower. Mitigation: add explicit `CGO_ENABLED=0` regardless. |

## Open Questions

1. **modernc.org/sqlite version and API stability**
   - What we know: It's the well-known pure-Go SQLite driver; CGO_ENABLED=0 is confirmed as a hard constraint.
   - What's unclear: The exact latest version and whether the `_pragma=` DSN syntax is current (couldn't verify due to network restrictions).
   - Recommendation: The executor runs `go get modernc.org/sqlite@latest`, verifies `sql.Open("sqlite", ...)` works, and validates pragmas applied via `PRAGMA journal_mode` query.

2. **Database path coordination with PythonConfig.VocabDBPath**
   - What we know: PythonConfig already has a `VocabDBPath` field (config.go line 196). The new `DatabaseConfig.Path` is for Go's direct access.
   - What's unclear: Should `DatabaseConfig.Path` be a separate config field, or should Go reuse `cfg.Python.VocabDBPath`? Having two fields risks them diverging.
   - Recommendation: `DatabaseConfig.Path` is a separate field with the suggested default `$HOME/.home-tutor-agent/vocab.db`. The config documentation should note that it SHOULD match `python.vocabDBPath`. Alternatively, if the planner prefers DRY, Go's `database.Open()` can fall back to `cfg.Python.VocabDBPath` when `cfg.Database.Path` is empty. This is Claude's discretion per CONTEXT.md.

3. **Session table column extensibility for Phase 7**
   - What we know: D-04 specifies minimal session bookkeeping. Phase 7 will need session lifecycle (IDLE → LISTENING → DICTATING → WAITING_PARENT → FEEDBACK).
   - What's unclear: Exact columns Phase 7 will need (student_id? category? word list snapshot?).
   - Recommendation: Keep columns minimal for Phase 4. Add a `metadata TEXT DEFAULT '{}'` JSON column for future extensibility without schema changes. Phase 7 can add specific columns via ALTER TABLE if needed.

## Environment Availability

| Dependency | Required By | Available | Version | Fallback |
|------------|------------|-----------|---------|----------|
| Go 1.26 | All Go code | ✓ | go1.26.0 windows/amd64 | — |
| `CGO_ENABLED=0` | Pure-Go build constraint | ✓ (auto-detected on Windows) | 0 | N/A — hard constraint |
| gcc/C toolchain | NOT required (modernc.org/sqlite is pure Go) | ✗ | — | modernc.org/sqlite eliminates this need |
| `modernc.org/sqlite` module | SQLite driver | ✗ (not yet in go.mod) | — | No fallback — this is the only viable pure-Go SQLite driver. Must `go get` in Wave 0. |
| sqlite3 CLI | NOT required (Go uses the driver directly) | ✗ | — | N/A — not needed |
| Python 3 (for integration tests) | Schema validation against vocab_manager.py output | ✗ (Windows Store stub only) | — | Phase 5 integration tests are `//go:build linux` gated; Windows dev runs unit tests only |

**Missing dependencies with no fallback:**
- `modernc.org/sqlite` — must be added to go.mod in Wave 0. The planner should include a `go get modernc.org/sqlite` task as the first implementation step.

**Missing dependencies with fallback:**
- Python 3 for integration tests — Windows dev environment has only the Store stub. This is already handled by the established `//go:build linux` build-tag pattern (pybridge/bridge_linux_test.go). Database integration tests that need to validate against Python's schema output should use the same pattern.

## Validation Architecture

### Test Framework
| Property | Value |
|----------|-------|
| Framework | Go stdlib `testing` (go 1.26) |
| Config file | None — uses `.golangci.yml` for linting, `Makefile` for commands |
| Quick run command | `go test -v -short ./pkg/database/...` |
| Full suite command | `go test -race -v ./pkg/database/...` |

### Phase Requirements → Test Map
| Req ID | Behavior | Test Type | Automated Command | File Exists? |
|--------|----------|-----------|-------------------|-------------|
| VOC-04 | Go-owned tables created with correct schema | unit | `go test -run TestMigrate ./pkg/database/...` | ❌ Wave 0 |
| VOC-04 | Migration is idempotent (safe to run twice) | unit | `go test -run TestMigrate_Idempotent ./pkg/database/...` | ❌ Wave 0 |
| VOC-04 | CardRepository reads cards table (Python-owned) | unit | `go test -run TestCardRepository ./pkg/database/...` | ❌ Wave 0 |
| VOC-04 | SessionRepository creates/updates sessions | unit | `go test -run TestSessionRepository ./pkg/database/...` | ❌ Wave 0 |
| VOC-04 | Database Open applies WAL + busy_timeout pragmas | unit | `go test -run TestOpen_Pragmas ./pkg/database/...` | ❌ Wave 0 |
| VOC-04 | Schema does not conflict with Python vocab_manager schema | integration | `go test -run TestSchemaCompatibility ./pkg/database/...` (build-tag: linux) | ❌ Wave 0 |

### Sampling Rate
- **Per task commit:** `go test -v -short ./pkg/database/...`
- **Per wave merge:** `go test -race -v ./...` (full suite, all packages)
- **Phase gate:** Full suite green + golangci-lint clean before `/gsd-verify-work`

### Wave 0 Gaps
- [ ] `pkg/database/db_test.go` — covers Open() pragma application, error paths
- [ ] `pkg/database/migrate_test.go` — covers Migrate() idempotency, schema correctness
- [ ] `pkg/database/card_repo_test.go` — covers CardRepository read operations
- [ ] `pkg/database/session_repo_test.go` — covers SessionRepository CRUD operations
- [ ] `go get modernc.org/sqlite` — driver dependency must be added to go.mod first
- [ ] `pkg/infra/config_test.go` — extend with DatabaseConfig field validation test

## Security Domain

### Applicable ASVS Categories

| ASVS Category | Applies | Standard Control |
|---------------|---------|-----------------|
| V2 Authentication | no | No authentication in Phase 4 (session bookkeeping only; auth is Phase 1 D-65 token-based, separate from DB) |
| V3 Session Management | yes (indirectly) | `sessions` table stores session lifecycle state; session IDs are UUID v4 from Phase 1 ConnectionRegistry (D-58). No session tokens stored in DB. |
| V4 Access Control | no | No access control layer in Phase 4 (single-user family tutor; no multi-tenant isolation needed) |
| V5 Input Validation | yes | Repository methods validate input parameters (non-empty connection_id, valid state transitions); SQL queries use parameterized `?` placeholders (no string concatenation) |
| V6 Cryptography | no | No cryptographic operations in Phase 4 (FSRS parameters are learning metrics, not secrets; no encryption at rest needed for a family tutor) |
| V7 Error Handling and Logging | yes | Database errors wrapped with `errors.Wrap` (Phase 1 D-05 pattern); structured logging via `infra.Logger` (D-25); no raw SQL or sensitive data in log fields (T-01-09 sanitization) |
| V8 Data Protection | yes | `fsrs_card` JSON column is opaque to Go (never parsed, preventing data leakage); DSN does not contain credentials (SQLite is file-based, no auth) |
| V9 Communications | no | No network communication in DB layer (local file access only) |
| V10 Malicious Code | yes | `modernc.org/sqlite` is a verified community driver (pending human verification per Package Legitimacy Audit); Go modules have no postinstall scripts |

### Known Threat Patterns for Go + SQLite

| Pattern | STRIDE | Standard Mitigation |
|---------|--------|---------------------|
| SQL injection | Tampering | All queries use `database/sql` parameterized placeholders (`?`); no `fmt.Sprintf` for SQL values. Repository methods accept typed parameters (int64, string), not raw SQL fragments. |
| Database file tampering | Tampering | SQLite file permissions set via `os.MkdirAll(dir, 0o755)` — on the VPS, the file is owned by the service user. No encryption at rest (acceptable for a family tutor; FSRS data is not sensitive). |
| Denial of service via lock contention | Denial of Service | `busy_timeout=5000` prevents indefinite lock waits; `SetMaxOpenConns(1)` prevents Go from exhausting the connection pool; Python subprocess calls are already semaphore-bounded (Phase 3 D-02, MaxConcurrent=2). |
| Schema drift leading to data corruption | Tampering | D-08-a schema drift detection in Phase 5 integration tests; ADR D-10-a requires explicit schema change process; `SELECT explicit_column_list` (not `SELECT *`) ensures loud failure on column changes. |
| Information disclosure via error messages | Information Disclosure | `infra.Logger` sanitizes string fields (T-01-09, logging.go `sanitizeString`); `ExitError` struct (pybridge) only carries safe fields; database errors wrapped generically via `errors.Wrap`. |

## Sources

### Primary (HIGH confidence — verified this session)
- **`go env -json CGO_ENABLED`** — confirmed `"0"` on Windows dev host (2026-08-01). This is the load-bearing constraint for the driver choice.
- **`third_party/python/vocab_manager.py`** (read directly from source) — canonical schema at lines 103-128 (`cards`, `review_logs`, indexes, migration pattern). WAL mode at line 94. `BEGIN IMMEDIATE` fetch-lock pattern at lines 217-289. FSRS `review` rating map (good/hard/again/easy) at lines 359-364.
- **`pkg/infra/config.go`** (read directly from source) — Config struct patterns, PythonConfig.VocabDBPath (line 196), DefaultConfig(), Validate(), bindEnvForAllLeaves(), decodeHook patterns. Establishes how DatabaseConfig should be added.
- **`pkg/infra/connection.go`** (read directly from source) — Constructor DI pattern (NewConnectionRegistry), sentinel errors, compile-time interface check (`var _ = ...`), Logger injection.
- **`pkg/pybridge/executor.go`** (read directly from source) — Executor interface pattern, DueCard/UpdatedCard typed structs, sentinel errors, ExitError typed struct.
- **`cmd/service/main.go`** (read directly from source) — Service bootstrap sequence, DI wiring pattern, shutdown sequence (lines 272-310) where db.Close() must be added.
- **`go.mod`** — module `github.com/xiaotian/home-tutor-agent`, go 1.26, dependencies (no SQLite driver yet).
- **`.github/workflows/ci.yml`** — CI does NOT explicitly set `CGO_ENABLED=0` (pitfall finding).
- **`Makefile`** — build target uses `go build -ldflags '-s -w'` without CGO_ENABLED=0 (pitfall finding).

### Secondary (MEDIUM confidence — from project documents)
- **`.planning/phases/04-database-schema-design/04-CONTEXT.md`** — D-01 through D-12 locked decisions.
- **`.planning/phases/04-database-schema-design/EVALUATION.md`** — opencode evaluation approving all 12 decisions with minor adjustments (D-03-a, D-08-a/b/c added).
- **`.planning/phases/03-python-cli-integration/03-CONTEXT.md`** — Phase 3 D-09/D-10 constraints that bind Phase 4 scope.
- **`pkg/pybridge/bridge_test.go`** — test pattern reference (manual fakes D-48, table-driven tests, `//go:build linux` for integration tests).
- **`.golangci.yml`** — linter config (gosec, govet, staticcheck, errcheck, gofmt, goimports).

### Tertiary (LOW confidence — training knowledge, NOT verified this session)
- `modernc.org/sqlite` driver name, DSN format, version, registry existence — **network restrictions prevented verification**. Tagged `[ASSUMED]` throughout.
- SQLite WAL mode multi-process concurrency characteristics — well-established but not verified against official docs this session (sqlite.org/wal.html fetch was blocked).
- `SetMaxOpenConns(1)` as the standard SQLite+Go pooling recommendation — community consensus, not verified via authoritative source this session.

## Metadata

**Confidence breakdown:**
- Standard stack: MEDIUM — CGO constraint is HIGH (verified); modernc.org/sqlite specifics are ASSUMED (network blocked). The driver choice is logically forced (no alternative exists for pure-Go SQLite), but the API details need executor verification.
- Architecture: HIGH — derived directly from reading the existing codebase (config.go, main.go, connection.go, executor.go) and the Python schema source. All patterns are established Phase 1/3 conventions.
- Pitfalls: HIGH for CGO/busy_timeout/DSN (derived from verified environment state and well-established SQLite behavior); MEDIUM for schema drift (depends on future Python changes outside Phase 4 control).

**Research date:** 2026-08-01
**Valid until:** 2026-08-31 (30 days — stable domain; SQLite and Go patterns change slowly. The modernc.org/sqlite version should be re-verified at execution time.)
