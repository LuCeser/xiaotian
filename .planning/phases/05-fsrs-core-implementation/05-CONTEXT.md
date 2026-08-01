# Phase 5: FSRS Core Implementation - Context

**Gathered:** 2026-08-01  
**Status:** Auto-generated (no human discussion)  
**Generation Method:** Automated analysis based on codebase context and prior phase decisions

<domain>
## Phase Boundary

This phase delivers **FSRS service orchestration layer** — a Go service layer that coordinates automatic due word selection and feedback-driven parameter updates through the Python bridge. Per Phase 3 D-09 decision, this is **NOT** a reimplementation of FSRS in Go; it is an orchestration layer that calls the existing `vocab_manager.py` FSRS engine via `pybridge.Executor`.

**In scope (FSRS-01, FSRS-02, FSRS-03, FSRS-04):**
- FSRS-01: System automatically selects due words based on FSRS algorithm (via Python bridge)
- FSRS-02: System supports configurable word count limits (20-30 words default)
- FSRS-03: System implements fetch-lock pattern preventing concurrent session conflicts
- FSRS-04: Parent feedback ratings (good/hard/again/easy) trigger FSRS parameter updates

**Out of scope (explicitly deferred — do NOT pull in):**
- FSRS algorithm implementation → stays in Python `vocab_manager.py` (Phase 3 D-09)
- Session orchestration / when to trigger selection → Phase 7
- WebSocket integration → Phase 2 (already implemented)
- Parent feedback UI → Phase 10

</domain>

<decisions>
## Implementation Decisions

### 1. FSRS Service Layer Architecture
**D-01:** Single-layer service architecture (Service pattern only). Create `pkg/fsrs` with `FSRSService` interface that directly exposes `SelectDueCards(opts SelectOpts) ([]DueCard, error)` and `UpdateRatings(ratings map[string]string) (UpdateResult, error)` methods. Internally, these methods call `pybridge.Executor.RunDue` and `RunReview`. This follows Phase 1 DI constructor pattern (`NewFSRSService(exec Executor, logger Logger, cfg Config)`) and keeps the layer simple for current small-scale needs.

**Rationale:** Current codebase follows simple service patterns (pkg/pybridge, pkg/database). A two-layer architecture (Service+Domain) would be over-engineering for orchestration that directly delegates to Python. Single layer enables future refactoring without premature complexity.

### 2. Fetch Lock Implementation Strategy  
**D-02:** Leverage Python-side fetch lock, not Go-side reimplementation. The `vocab_manager.py` already has `fetch_locked_until` column and `--fetch-lock-hours` flag. Go layer controls lock duration via `DueOpts.FetchLockHours` parameter to `RunDue`, but does **not** implement separate lock logic. This respects Phase 4's write-separation contract (Python owns cards table writes).

**D-02-a:** Fetch lock behavior contract. When cards are locked (fetch_locked_until > NOW), `RunDue` returns an empty array rather than an error. This design allows Phase 7 session orchestration to distinguish "no cards available due to lock" from actual errors. The empty result is a signal to the caller that another session holds the lock, not a system failure.

**Rationale:** Avoids duplicate lock implementations and potential race conditions. Python FSRS engine is the authority per Phase 3 D-09; Go only orchestrates. The fetch lock is a database-level concern owned by Python. Empty array on lock provides clear signal without requiring Go-side lock state tracking.

### 3. Default Configuration Strategy
**D-03:** Extend Phase 1 `Config` struct with FSRS operational fields under `fsrs.*` section:
- `DefaultWordCount: int` (default: 25, within 20-30 range per ROADMAP.md FSRS-02)
- `FetchLockHours: float` (default: 24.0 hours)
- `DefaultUnit: string` (default: "" for all units)
- `DefaultCategory: string` (default: "english")

These flow from config → `FSRSService` constructor → `SelectDueCards` defaults. Follows Phase 1 D-52/D-53 layered loader pattern (defaults → file → env with `HTA_FSRS_` prefix).

**Rationale:** Consistent with existing Phase 1 infrastructure patterns. Config-driven behavior enables runtime adjustment without code changes.

### 4. Error Handling and Concurrency Control
**D-04:** Reuse Phase 3 global semaphore for concurrency control. Do **not** introduce new FSRS-specific concurrency limits. The `pybridge.Executor` already caps concurrent Python processes (D-02 global bounded semaphore). `FSRSService` methods share this limit; multiple concurrent `SelectDueCards` calls queue behind the semaphore.

**D-05:** Error handling follows Phase 1 structured logging pattern. All errors from `RunDue`/`RunReview` are wrapped with context ("fsrs service: select due cards") and logged via `infra.Logger`. Semantic errors (timeout, empty result, parse failure) preserve pybridge sentinel types for caller branching. No retry logic in FSRS layer; retries are caller concern (Phase 7 session orchestration).

**Rationale:** Consistent error handling across codebase. Reusing existing semaphore prevents resource exhaustion on the 1C/1G VPS. No layer-specific retries avoid cascading timeout storms.

</decisions>

<canonical_refs>
## Canonical References

**Downstream agents MUST read these before planning or implementing.**

### Roadmap & Phase Definition
- `.planning/ROADMAP.md` — Phase 5 goal, success criteria, requirement mapping (FSRS-01..FSRS-04)
- `.planning/ROADMAP.md#phase-5-fsrs-core-implementation` — Phase 5 definition (depends on Phase 3, Phase 4)

### Project Requirements & Context
- `.planning/REQUIREMENTS.md` — v1 requirements; FSRS-01..FSRS-04 (automatic selection, configurable limits, fetch-lock, feedback updates)
- `.planning/PROJECT.md` — project constraints (150MB Go memory, 1C/1G VPS, Go WebSocket + Python microservices architecture)
- `.planning/PROJECT.md#constraints` — infrastructure constraints the FSRS layer must respect

### Prior Phase Context (Critical for Phase 5)
- `.planning/phases/03-python-cli-integration/03-CONTEXT.md` — **MUST READ** — Phase 3 D-09 (long-term reuse of Python FSRS+SQLite), D-02 (global bounded semaphore), RunDue/RunReview contracts
- `.planning/phases/04-database-schema-design/04-CONTEXT.md` — **MUST READ** — Phase 4 D-01/D-03-a (write-separation contract: Python owns cards/review_logs, Go reads only via CardRepository)
- `.planning/phases/02-websocket-communication-layer/02-CONTEXT.md` — Phase 2 WebSocket infrastructure for future session delivery
- `.planning/phases/01-resource-constrained-infrastructure/01-CONTEXT.md` — Phase 1 DI patterns (D-42/D-47), Config system (D-52/D-53), structured logging (Zap)

### Architecture Decision Record
- `.planning/adr/2026-08-01-fsrs-sqlite-ownership.md` — **MUST READ** — Resolves P1-4 architecture review item: Python vocab_manager.py owns canonical FSRS+SQLite schema; Go extends with auxiliary tables only. This constrains Phase 5 to read-only FSRS orchestration.

### Codebase Integration Points
- `pkg/pybridge/executor.go` — Executor interface with RunDue/RunReview methods (PY-02, PY-03)
- `pkg/pybridge/vocab.go` — DueCard/UpdatedCard contracts, argv construction patterns
- `pkg/database/card_repo.go` — CardRepository read-only interface (per Phase 4 D-03-a, Go NEVER writes to cards table)
- `pkg/infra/config.go` — Config struct + Viper layered loader pattern to extend with fsrs.* fields

</canonical_refs>

<code_context>
## Existing Code Insights

### Reusable Assets
- `pkg/pybridge/executor.go` — Executor interface with RunDue (returns DueResult) and RunReview (accepts ReviewOpts, returns ReviewResult). Phase 5 FSRSService composes this interface.
- `pkg/pybridge/vocab.go` — DueOpts structure (Category, Limit, Units, Exclude, Peek, FetchLockHours) maps directly to vocab_manager.py CLI flags.
- `pkg/database/card_repo.go` — CardRepository read-only access to Python-owned cards table. Phase 5 uses this for validation/logging, NOT for FSRS logic.
- `pkg/infra/config.go` — Typed Config struct + Viper layered loader (defaults < file < env, prefix `HTA_`). Phase 5 extends this with `fsrs.*` operational block.

### Established Patterns
- **Module layout:** `github.com/xiaotian/home-tutor-agent` with layered `cmd/pkg/internal` structure. New `pkg/fsrs` package follows cmd→pkg/infra→pkg/fsrs dependency rule.
- **Constructor DI:** Phase 1 D-42/D-47 pattern. FSRSService accepts Executor, Logger, and Config in constructor: `NewFSRSService(exec pybridge.Executor, logger infra.Logger, cfg Config) (FSRSService, error)`.
- **Manual fakes:** Phase 1 D-48 pattern. Tests use manual fake Executor instead of mocking frameworks. FSRSService enables this via interface composition.
- **Table-driven tests:** Phase 1 D-28/D-29 pattern. FSRS service tests use table-driven cases for success/error paths.
- **Config secret rule:** Phase 1 D-24 / Phase 3 D-05. No API keys/secrets in Config struct; FSRSService has no secret handling.

### Integration Points
- `cmd/service/main.go` — Service startup where FSRSService is constructed and wired (constructor DI), alongside existing infra/ws components.
- Future Phase 7 session orchestration — Will call FSRSService.SelectDueCards to fetch words for dictation sessions.
- Future Phase 10 parent feedback — Will call FSRSService.UpdateRatings to submit performance ratings.

**Note:** Phase 5 does **NOT** use `pkg/database/card_repo.go` directly. All FSRS operations flow through `pybridge.Executor.RunDue/RunReview`, which handles database access on the Python side per Phase 4 write-separation contract. CardRepository exists for future phases (e.g., Phase 9 vocabulary management) that need direct Go-side read access.

### Python FSRS Engine (Canonical Source)
- `third_party/python/vocab_manager.py` — **MUST READ** — FSRS Scheduler (lines 78-84), due command (lines 209-291), review command with rating_map including verified "easy": Rating.Easy support (lines 359-364). Phase 5 orchestrates this engine; it does **not** replace it.

</code_context>

<specifics>
## Specific Ideas

No "I want it like X" external references. All decisions derived from:
- Phase 3 D-09: Python FSRS+SQLite is permanent engine
- Phase 4 write-separation contract: Go reads cards, Python updates
- Phase 1 infrastructure patterns: DI, Config, structured logging
- Resource constraints: 1C/1G VPS, reuse existing semaphore, no new concurrency primitives

</specifics>

<deferred>
## Deferred Ideas

- **Advanced retry logic** → Phase 7 session orchestration. Phase 5 exposes raw errors; caller decides retry strategy.
- **FSRS parameter tuning UI** → Phase 9 (parent vocabulary management). Phase 5 only passes through configuration.
- **Analytics/metrics** → Phase 10 (FEED-04 requirement). Phase 5 focuses on core orchestration only.
- **Alternative FSRS engines** → explicitly NOT planned (Phase 3 D-09). Python vocab_manager.py is permanent.

</deferred>

---

*Phase: 05-FSRS Core Implementation*  
*Context auto-generated: 2026-08-01*  
*Generation method: Automated analysis based on codebase patterns and prior phase decisions*  
*Note: No human discussion was conducted. All decisions derived from existing codebase context and Phase 1-4 constraints.*