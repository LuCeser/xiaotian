---
phase: 09-parent-vocabulary-management
plan: 04
subsystem: api
tags: [sqlite, group-by, rest, go, card-repository, units]

# Dependency graph
requires:
  - phase: 09-parent-vocabulary-management (09-01)
    provides: CardRepository.List + in-memory sqlite fixtures (seedCardsTable/newTestDBWithCards); pkg/web/api NewHandler internal mux, {"error":{code,message}} envelope, ValidateCategory
provides:
  - CardRepository.CountByUnit(ctx, category) + UnitCount type — parameterized GROUP BY unit ORDER BY unit; category-scoped when filtered, intentional merge-by-name when empty
  - GET /api/v1/units?category= endpoint (enum-checked, 400 invalid_request) returning {"units":[{"unit":...,"count":...}]} in one round-trip, zero Python spawns
  - TestCountByUnit_* (5 in-memory-sqlite suites) + TestListUnits (4 fake-based subtests) pinning shared-unit-name and empty-unit edge behavior
affects: [09-06, 09-07, 09-08]

# Actuals (#2632)
actuals:
  tokens: 4300
  tasks: 2
  commits: 3

# Tech tracking
tech-stack:
  added: []
  patterns:
    - "Aggregate reads port the Python GROUP BY shape parameterized into CardRepository (SELECT unit, COUNT(*) ... GROUP BY unit ORDER BY unit) — never string-built SQL"

key-files:
  created: []
  modified:
    - pkg/database/card_repo.go
    - pkg/database/card_repo_query_test.go
    - pkg/web/api/handlers.go
    - pkg/web/api/handlers_test.go

key-decisions:
  - "unitsResponse reuses database.UnitCount directly instead of a handler-local struct — the wire shape {\"units\":[{\"unit\",\"count\"}]} has one definition and cannot drift"
  - "Resume reconciliation: the interrupted run's uncommitted Task 2 work (route + listUnits + fake CountByUnit + 4 subtests) was diff-reviewed and ADOPTED rather than rewritten; only its seed INSERT arg-count bug needed fixing"
  - "Live acceptance check done against a fresh run.ps1 service instance — the stale dev service from the prior session predated the new route"

patterns-established:
  - "Pattern: aggregate/count endpoints read via CardRepository SQL only (D-12) — handler imports no pybridge, ordered passthrough of repo rows untouched"

requirements-completed: [VOC-02]

coverage:
  - id: D1
    description: "CardRepository.CountByUnit groups by unit within one category when filtered, merges equal-named units across categories only when unfiltered (Pitfall 7 disposition), orders by unit ascending, returns empty non-nil slice for zero rows, and counts empty-string-unit words"
    requirement: VOC-02
    verification:
      - kind: unit
        ref: "pkg/database/card_repo_query_test.go#TestCountByUnit_CategoryScoped / _AllCategories / _Ordering / _EmptyAndSingle / _EmptyStringUnit"
        status: pass
    human_judgment: false
  - id: D2
    description: "GET /api/v1/units?category= returns 200 {\"units\":[{\"unit\",\"count\"}]} in one round-trip; empty category reaches the repo as \"\"; unknown category values rejected 400 invalid_request with the locked envelope"
    requirement: VOC-02
    verification:
      - kind: unit
        ref: "pkg/web/api/handlers_test.go#TestListUnits (4 subtests incl. francais → 400 and ordering passthrough)"
        status: pass
      - kind: integration
        ref: "live curl via run.ps1 service: /api/v1/units?category=chinese → {\"units\":[{\"unit\":\"e2e-tracer\",\"count\":1}]}; english/count 2 separately; no category merges to 3; category=francais → 400 invalid_request"
        status: pass
    human_judgment: false
  - id: D3
    description: "Read path spawns zero Python subprocesses (D-12): pkg/database imports no os/exec and the live units GETs produced no pybridge log entries"
    requirement: VOC-02
    verification:
      - kind: other
        ref: "rg -n 'os/exec' pkg/database/ → no matches; server.log '\"component\":\"pybridge\"' count during live GETs = 0"
        status: pass
    human_judgment: false

# Metrics
duration: 26min
completed: 2026-09-28
status: complete
---

# Phase 9 Plan 04: Unit reads Summary

**CardRepository.CountByUnit (parameterized GROUP BY unit, category-scoped) plus GET /api/v1/units serve unit names + word counts in one Python-free round-trip for the browse headers and add-form datalist.**

## Performance

- **Duration:** 26 min (resume session; Task 1 was already committed by the interrupted run)
- **Started:** 2026-09-28T01:04:00Z (resume)
- **Completed:** 2026-09-28T01:30:00Z
- **Tasks:** 2 (Task 1 verified-already-done, Task 2 completed from partial work)
- **Files modified:** 4

## Accomplishments
- `CardRepository.CountByUnit(ctx, category)` + `UnitCount{Unit,Count}`: one parameterized `SELECT unit, COUNT(*) ... GROUP BY unit ORDER BY unit` (port of vocab_manager.py:557-562) with the category WHERE clause added only when non-empty — category filters keep shared unit names separate (Pitfall 7), the unfiltered view intentionally merges by name (D-09)
- `GET /api/v1/units` live on the internal mux: optional `category` query param, non-empty values pass `ValidateCategory` (unknown → 400 invalid_request, A5), response reuses `database.UnitCount` so the wire shape cannot drift; ordering passes through untouched
- Five in-memory-sqlite suites (`TestCountByUnit_*`) pin category scoping, merge semantics, ordering, empty-slice/one-word units, and empty-string-unit counting; four fake-based `TestListUnits` subtests pin envelope shape, category forwarding, 400 rejection, and ordering passthrough
- D-12 proven both statically (no `os/exec` import in pkg/database) and live (zero pybridge log entries during units GETs against the running service)

## Task Commits

Each task was committed atomically:

1. **Task 1: CardRepository.CountByUnit — parameterized GROUP BY with category scoping** - `c5ad0b2` (test, RED) + `2f38f78` (feat, GREEN) — committed by the interrupted run, verified green this session
2. **Task 2: GET /api/v1/units endpoint — enum-checked, one round-trip unit+count payload** - `28c5a37` (feat; adopted partial work completed and committed this session)

**Plan metadata:** see docs commits for SUMMARY/STATE/ROADMAP.

_Note: Task 1 is TDD-gated (RED test commit precedes GREEN feat commit); Task 2 is a plain auto task._

## Files Created/Modified
- `pkg/database/card_repo.go` - UnitCount struct + CountByUnit on interface and sqliteCardRepository (errors wrapped, component-tagged)
- `pkg/database/card_repo_query_test.go` - five TestCountByUnit_* suites on the 09-01 in-memory fixtures (+ seed INSERT arg-count fix)
- `pkg/web/api/handlers.go` - "GET /units" route, unitsResponse envelope, listUnits handler (ValidateCategory → CountByUnit → writeJSON)
- `pkg/web/api/handlers_test.go` - fakeCardRepo.CountByUnit (captures category) + TestListUnits (4 subtests)

## Decisions Made
- `unitsResponse` embeds `[]database.UnitCount` directly — one definition of the wire shape, no handler-local duplicate that could drift.
- Resume reconciliation: the interrupted executor's uncommitted Task 2 work was diff-reviewed (`git diff --ignore-cr-at-eol`) and adopted as-is — its design (fake capture fields, four subtests mirroring the plan's test spec, listWords-mirroring handler) was sound; only the fixture bug below needed fixing.
- Live acceptance was run against a freshly restarted run.ps1 service because the still-running dev instance predated the new route.

## Deviations from Plan

### Auto-fixed Issues

**1. [Rule 1 - Bug] TestCountByUnit_EmptyAndSingle seed INSERT passed 5 args to 4 placeholders**
- **Found during:** Task 2 resume (present in the interrupted run's uncommitted work; fix authored there, adopted here)
- **Issue:** The INSERT declared `VALUES (?, ?, '', ?, ?, '')` (4 placeholders) but bound 5 values, so the subtest would fail with an arg-count mismatch, not an assertion failure.
- **Fix:** Dropped the stray extra binding (`"solo"`) so word/meaning/unit/category bind positionally as intended.
- **Files modified:** pkg/database/card_repo_query_test.go
- **Verification:** `go test ./pkg/database/ -short -run 'TestCountByUnit'` green; suite fails on behavior, not binding.
- **Committed in:** 28c5a37 (Task 2 commit)

---

**Total deviations:** 1 auto-fixed (1 bug, originating in adopted partial work)
**Impact on plan:** None — the fix was required for the plan's own test suites to execute. No scope creep.

## Issues Encountered
- A stale dev service instance (started 2026-09-27, pre-09-04 binary) was still listening on :8080; restarted via detached run.ps1 to satisfy the "endpoint live on the running service" acceptance criterion. Operational note, not a defect.
- Task 1's RED/GREEN commits predate this session (interrupted run); verified rather than re-executed — no re-commit, per resume instructions.

## User Setup Required

None - no external service configuration required.

## Next Phase Readiness
- Plan 09-06 (datalist) and 09-07 (browse unit grouping) can consume `GET /api/v1/units` directly; the empty-string unit row IS included in payloads and 09-07 must render it deliberately (UI truth carried from the plan's assumptions block)
- REQUIREMENTS.md VOC-02 intentionally left Pending: sibling plans 09-05/09-07 declare the same ID (shared-ID gate #2388) and flip it when their summaries land
- e2e-tracer rows (3 total across english/chinese) remain in the vocab DB for Plan 09-08's cleanup pass once delete ships

---
*Phase: 09-parent-vocabulary-management*
*Completed: 2026-09-28*

## Self-Check: PASSED

- key-files.modified verified on disk, all committed (git status clean for the 4 files)
- `git log --grep="09-04"` returns 3 production commits (c5ad0b2, 2f38f78, 28c5a37)
- All `<acceptance_criteria>` re-run green:
  - Task 1: `go test ./pkg/database/ -short -run 'TestCountByUnit|TestCardRepoList'` — PASS (5 CountByUnit suites + List suites)
  - Task 1: `rg -n "os/exec" pkg/database/` — no matches (D-12 source assertion)
  - Task 2: `go test ./pkg/web/api/ -short -run 'TestListUnits|TestListWords|TestAddWords'` — PASS
  - Task 2: live `GET localhost:8080/api/v1/units?category=chinese` → 200 `{"units":[{"unit":"e2e-tracer","count":1}]}` — PASS (also english/none/400 cases)
- Plan `<verification>` re-run: `go test ./pkg/database/ ./pkg/web/api/ -short` green; `go build ./...` ok; no os/exec in pkg/database; 0 pybridge log entries during live reads
