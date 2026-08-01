# Phase 4: Database Schema Design - Context

**Gathered:** 2026-08-01
**Status:** Ready for planning

<domain>
## Phase Boundary

This phase delivers **SQLite schema reconciliation and Go-owned auxiliary data design** for the Home Tutor Agent database layer. The phase scope is constrained by Phase 3 D-09/D-10 decisions: `vocab_manager.py` already implements a permanent FSRS+SQLite schema that is the canonical source of truth for vocabulary and FSRS metadata. Phase 4 does not re-design this schema; it documents the reconciliation decision, designs Go-owned auxiliary tables for session bookkeeping, and establishes the database access layer pattern that Phase 5+ will use.

**In scope (VOC-04):**
- Schema reconciliation decision and ADR documentation for FSRS+SQLite ownership
- Go-owned auxiliary tables for session bookkeeping (session_state, active_sessions)
- Database access layer design (repository pattern with sql.DB, migration strategy)
- Schema documentation in code + architecture decision record

**Out of scope (explicitly deferred — do NOT pull in):**
- Vocabulary/FSRS schema changes — `vocab_manager.py` schema is canonical per Phase 3 D-09
- TTS audio cache table design → Phase 6 (TTS streaming)
- Parent feedback extensions to review_logs → Phase 10 (FEED-* requirements)
- Schema migration tooling → Phase 5/7 when sync complexity increases

</domain>

<decisions>
## Implementation Decisions

### Schema Reconciliation Strategy
- **D-01:** Treat `vocab_manager.py` schema as canonical for vocabulary and FSRS data. Phase 4 does not modify the existing `cards` or `review_logs` tables. The Python implementation remains the permanent engine (Phase 3 D-09).
- **D-02:** Phase 4 extends the database with Go-owned auxiliary tables only. New tables serve Go concerns (session bookkeeping, state management) and avoid conflicts with the Python schema.
- **D-03:** Database file sharing strategy — Go and Python access the same SQLite database file (`vocab.db`) via `DICTATION_DB_PATH` environment variable. Go uses SQLite's built-in concurrency (WAL mode already enabled by vocab_manager) to safely read/write auxiliary tables while Python owns `cards`/`review_logs`.

### Go-Owned Auxiliary Data
- **D-04:** Minimal session bookkeeping tables only. Phase 4 creates `sessions` and `session_events` tables for session lifecycle tracking (IDLE → LISTENING → DICTATING → WAITING_PARENT → FEEDBACK).
- **D-05:** No TTS audio cache tables in Phase 4. Audio file path references from `pregenerate_dictation.py` are managed in-memory during Phase 6; persistent cache metadata can be added later if needed.
- **D-06:** Parent feedback data remains in `review_logs` per Phase 3 design. Phase 10 (FEED-*) may extend this with analytics tables, but Phase 4 does not pre-design those extensions.

### Database Access Layer Design
- **D-07:** Repository pattern with `sql.DB`. Phase 4 creates `pkg/database` package with repository interfaces (`SessionRepository`, `CardRepository` for reads) that accept `*sql.DB` dependencies. Aligns with Phase 1 DI patterns (D-42/D-47 constructor injection).
- **D-08:** Minimal migration strategy. Phase 4 uses `CREATE TABLE IF NOT EXISTS` for auxiliary tables and checks column existence via `PRAGMA table_info` before `ALTER TABLE` (same pattern as vocab_manager's migration code). No formal migration framework; schema drift detection is manual.
- **D-09:** Database initialization in `cmd/service/main.go`. The `sql.DB` connection is opened during service startup, configured via `Config.DatabasePath` (new field extending Phase 1 Config), and injected into repositories. Connection lifetime matches service lifetime.

### Schema Documentation Approach
- **D-10:** ADR for FSRS+SQLite ownership reconciliation. Phase 4 creates `.planning/adr/2026-08-01-fsrs-sqlite-ownership.md` documenting the decision that Python `vocab_manager.py` owns the canonical vocabulary/FSRS schema and Go extends it with auxiliary tables only. This resolves the pending architecture review todo.
- **D-11:** Schema documentation in code comments. Each table creation includes a comment header documenting purpose, key relationships, and ownership (Python vs Go). A README in `pkg/database` documents the overall schema architecture.
- **D-12:** No separate schema documentation file. The canonical schema definition lives in code (both Python's `vocab_manager.py` and Go's migration logic), keeping docs close to implementation.

### Claude's Discretion
- Exact column set for `sessions` and `session_events` tables (minimal fields needed for session lifecycle and state transitions).
- Repository interface method signatures (card read methods for Phase 5, session CRUD methods).
- `Config.DatabasePath` field location and default value (suggested: `$HOME/.home-tutor-agent/vocab.db` for consistency with Python defaults).
- Whether `CardRepository` is read-only (Go reads vocabulary data) or also has write methods for future parent-managed vocabulary operations (Phase 9).

### Folded Todos
- **"Phase 4前定 FSRS + SQLite 的 single-source-of-truth 所有权（需 ADR）"** (from architecture review 2026-07-25). This todo (score 0.6) is folded into Phase 4 scope. The decision (D-10/D-11) resolves the ownership ambiguity: Python `vocab_manager.py` owns the canonical FSRS/vocabulary schema; Phase 4 documents this as an ADR and designs Go-owned auxiliary tables without conflicts.

</decisions>

<canonical_refs>
## Canonical References

**Downstream agents MUST read these before planning or implementing.**

### Roadmap & Phase Definition
- `.planning/ROADMAP.md` — Phase 4 goal, success criteria, requirement mapping (VOC-04).
- `.planning/ROADMAP.md#phase-4-database-schema-design` — Phase 4 definition (SQLite schema for vocabulary and FSRS metadata, depends on Phase 3).

### Project Requirements & Context
- `.planning/REQUIREMENTS.md` — v1 requirements; VOC-04 (SQLite schema with proper schema), FEED-02 (4 performance ratings: good/hard/again/easy).
- `.planning/PROJECT.md` — project constraints (SQLite storage, 1C/1G VPS, resource constraints), Phase 3 Python integration context.
- `.planning/STATE.md` — Current project state, completed phases (01-03), pending architecture review todos.

### Prior Phase Context (Critical for Phase 4)
- `.planning/phases/03-python-cli-integration/03-CONTEXT.md` — **MUST READ** — Phase 3 D-09 (long-term reuse of Python FSRS+SQLite) and D-10 (Phase 4 must reconcile with vocab_manager's existing schema). These decisions constrain Phase 4's scope.
- `.planning/phases/02-websocket-communication-layer/02-CONTEXT.md` — Phase 2 decisions for connection lifecycle and session management that inform Phase 4 session table design.
- `.planning/phases/01-resource-constrained-infrastructure/01-CONTEXT.md` — Phase 1 infrastructure patterns (DI, resource management, Config system) that Phase 4 database layer should follow.

### Python Asset Schema (Canonical Source)
- `third_party/python/vocab_manager.py` — **MUST READ** — Lines 103-128 contain the canonical database schema definition (`CREATE TABLE IF NOT EXISTS cards`, `CREATE TABLE IF NOT EXISTS review_logs`, indexes, migration for `fetch_locked_until`). Phase 4 must not duplicate or conflict with this schema.

### Architecture Review Requirement
- `todos/pending/2026-07-25-fsrs-sqlite-ownership-adr.md` — Pending architecture review item (P1-4) requiring Phase 4 to define FSRS+SQLite ownership before designing schema. This is resolved by D-10/D-11 decisions.

</canonical_refs>

<code_context>
## Existing Code Insights

### Reusable Assets
- `third_party/python/vocab_manager.py` (lines 87-95) — Database initialization pattern: `sqlite3.connect()`, `row_factory=sqlite3.Row`, `PRAGMA journal_mode=WAL`, directory creation. Go's `sql.Open()` should mirror this pattern (WAL mode enables safe concurrent read/write).
- `pkg/infra/config.go` (from Phase 1) — Typed `Config` struct + Viper layered loader. Phase 4 extends this with a `DatabasePath` field for SQLite file location.
- `pkg/infra/logging.go` (from Phase 1) — Zap logger with `infra.F()` / `infra.ErrField()` helpers. Database layer should use structured logging for connection events, query errors, and migration diagnostics.

### Established Patterns
- **Module layout:** `github.com/xiaotian/home-tutor-agent` with layered `cmd/pkg/internal` structure. New `pkg/database` package follows the Phase 1 dependency rule (cmd → pkg/infra → pkg/database).
- **Constructor DI:** Phase 1 D-42/D-47 pattern for dependency injection. Repositories accept `*sql.DB` and `*zap.Logger` in constructors, enabling testability.
- **Manual fakes:** Phase 1 D-48 pattern. Tests use manual fake implementations instead of mocking frameworks. `Repository` interfaces enable this.
- **Table-driven tests:** Phase 1 D-28/D-29 pattern. Database layer tests use table-driven test cases for migration logic, repository CRUD operations, and error scenarios.

### Integration Points
- `cmd/service/main.go` — Service startup where `sql.DB` connection is opened and injected into repositories. Database initialization happens during service bootstrap.
- Phase 5 `pkg/fsrs` layer — Will read from `CardRepository` (read-only access to `vocab_manager`'s `cards` table) for due word selection.
- Phase 7 session orchestration — Will use `SessionRepository` for session lifecycle management and state transitions.
- `vocab_manager.py` subprocess — Phase 3 bridge continues to write to `cards`/`review_logs` tables; Go reads these tables via `CardRepository` (SQLite concurrency safety).

</code_context>

<specifics>
## Specific Ideas

No "I want it like X" references beyond the decisions above. The Phase 4 scope is primarily architectural (reconciling with existing schema, establishing patterns for future phases) rather than user-facing features. Decisions are guided by technical constraints from Phase 3 and the need to support Phase 5+ FSRS orchestration and session management workflows.

</specifics>

<deferred>
## Deferred Ideas

- **Schema migration tooling** (Flyway/Go-migrate equivalent) → Phase 5/7 when sync complexity increases. Phase 4 uses minimal `CREATE TABLE IF NOT EXISTS` + column existence checks.
- **TTS audio cache metadata tables** → Phase 6 (TTS streaming). Phase 4 does not pre-design audio caching schema.
- **Parent feedback analytics tables** (performance tracking, retention metrics) → Phase 10 (FEED-04 requirement). Phase 4 keeps `review_logs` as-is.
- **Vocabulary management operations tables** (units, lessons, categories) → Phase 9 (VOC-01/02 requirements). Current schema has string fields; structured management is deferred.
- **Advanced schema validation/linting** → Future operational concern. Phase 4 focuses on functional correctness.

</deferred>

---

*Phase: 04-Database Schema Design*
*Context gathered: 2026-08-01*