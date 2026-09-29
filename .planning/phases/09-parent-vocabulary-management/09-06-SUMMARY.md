---
phase: 09-parent-vocabulary-management
plan: 06
subsystem: ui
tags: [batch-paste-parser, tabs, preview-table, datalist, vanilla-js, playwright-e2e, dom-safety]

# Dependency graph
requires:
  - phase: 09-parent-vocabulary-management
    provides: 09-01 add-words tracer (POST /words contract, fetchJSON, refresh hook, SingleAddForm v1) + 09-04/09-05 REST surface (GET /words, GET /units?category=, DELETE /words for smoke cleanup)
provides:
  - parseBatch(text) pure paste parser — DOM-free IIFE with the window-or-module-global tail, node-tested and wired into the npm test chain
  - AddCard — the 添加单词 card's 单个添加 | 批量添加 tabs (D-10) hosting the final forms
  - Final SingleAddForm — D-06 category radio with 请选择分类 validation, D-08 only 单词 required, shared unit datalist, disabled+spinner loading, success toast retaining category+unit
  - BatchAddForm — paste → 解析并预览 → preview table (# / 单词 / 意思 / 状态, per-line Warning rows, 320px inner scroll) → 确认添加（{n} 个） → transparency banner 已添加 {a} 个，跳过 {b} 个重复单词
  - Shared #unit-options datalist populated by parent-app.js on init and category change from GET /api/v1/units
affects: [09-07, 09-08]

# Actuals (#2632)
actuals:
  tokens: 14600   # chars/4 over the realized diffs of b400a63 + 891ea46 + 287ddd4 (estimate was 38000)
  tasks: 2
  commits: 3

# Tech tracking
tech-stack:
  added: []
  patterns:
    - "Pre-commit snapshot for honest partial-import reporting: the API returns only {added,skipped} counts, so BatchAddForm snapshots GET /words?category= before committing and derives the readable skipped list (skips are UNIQUE(word,category) ⇒ skipped == pasted ∩ existing); on snapshot failure the counts still render — transparency never depends on the extra request"
    - "Client-side entry-cap mirror: MAX_BATCH_ENTRIES (100) mirrors pkg/web/api maxEntries so an oversized batch is refused before the request is ever sent (bounded bodies, Plan 09-01 caps)"
    - "Shared datalist idiom: one #unit-options element in parent.html serves BOTH forms' 单元 inputs (list attribute); parent-app.js owns its population lifecycle"

key-files:
  created:
    - pkg/web/static/js/parent-batch-parser.js
    - tests/test-parent-batch-parser.js
  modified:
    - pkg/web/static/js/parent-modes.js
    - pkg/web/static/js/parent-app.js
    - pkg/web/static/parent.html
    - pkg/web/static/styles/parent.css
    - package.json

key-decisions:
  - "Adopted the interrupted session's uncommitted Task 2 work (parent-modes.js +781, parent-app.js +99, parent.html +25, parent.css +189) after per-file diff review against the plan spec — every spec item was present and verified green; zero fragments rewritten (Plan 09-04 precedent)"
  - "Skipped-word naming via pre-commit snapshot rather than extending the API response — keeps the 09-01 addWords contract untouched while satisfying the transparency prohibition's 'affected words readable' clause"
  - "Batch commit refused client-side above 100 entries with a split-paste hint; commit button disabled at zero valid rows — bounded bodies enforced before the request is sent"
  - "Empty-preview copy distinguishes the two states: 粘贴后点「解析并预览」 (nothing parsed yet) vs 没有可添加的单词，请检查格式 (parsed, everything skipped as comments/blank)"

patterns-established:
  - "Pattern: pure-function module + window-or-module-global tail + node test in the npm chain (state-manager.js convention, now the parent-side analog of test-batch-form.js)"
  - "Pattern: transparency banner — both counts always rendered, Warning variant + derived word list when skipped > 0 (Pitfall 3 made mechanical at the UI layer)"

requirements-completed: [VOC-01, WEB-01]  # copied from plan frontmatter verbatim; REQUIREMENTS.md flips remain gated by siblings 09-07/09-08 (shared-ID rule #2388 — ready-ids returned 0/2 this plan)

coverage:
  - id: D1
    description: "parseBatch(text) pure parser: one word per line, tab-first split, first-whitespace-run fallback, # comments and blank lines skipped, \r trimmed, per-line errors (empty-word / word-too-long / duplicate with first-line reference), mixed input returns rows AND errors together"
    requirement: VOC-01
    verification:
      - kind: unit
        ref: "tests/test-parent-batch-parser.js#all numbered assertion blocks (1-9)"
        status: pass
      - kind: unit
        ref: "command: node tests/test-parent-batch-parser.js ; npm test (parser is the final link of the chain)"
        status: pass
    human_judgment: false
  - id: D2
    description: "Dual-mode 添加单词 card: 单个添加|批量添加 tabs, final single form (D-06/D-08, datalist, loading, toast), batch paste → 解析并预览 → preview table with Warning rows → 确认添加（{n} 个） → banner 已添加 {a} 个，跳过 {b} 个重复单词 (Warning color + readable skipped words when b>0), browse + datalist refresh without reload"
    requirement: VOC-01
    verification:
      - kind: automated_ui
        ref: "playwright: temporary batch-smoke spec against the real run.ps1 service — 3-line paste → 3 valid preview rows → 已添加 3 个，跳过 0 个 (success class) → browse rows tagged with the pasted unit → duplicate re-paste → 已添加 0 个，跳过 3 个重复单词 (warning class, skipped words listed); 6/6 suite passed incl. the 09-01 tracer regression"
        status: pass
      - kind: other
        ref: "command: rg innerHTML over parent-modes.js/parent-app.js/parent-batch-parser.js → no matches; parseBatch referenced 3× in parent-modes.js"
        status: pass
    human_judgment: false

# Metrics
duration: ~25min (resume session — Task 1 + all Task 2 implementation were committed/authored by the interrupted first session)
completed: 2026-09-29
status: complete
---

# Phase 9 Plan 06: Add-words UI — batch paste parser + dual-mode 添加单词 card Summary

**Pure node-tested parseBatch paste parser plus the tabbed 单个添加|批量添加 card whose batch flow (paste → preview with per-line Warning rows → single commit → 已添加 {a} 个，跳过 {b} 个重复单词 banner) makes one-lesson-one-unit capture work end-to-end with honest partial-import reporting.**

## Performance

- **Duration:** ~25 min (resume session: verification, adoption review, smoke execution, close-out)
- **Started:** 2026-09-29T14:10:00Z (resume)
- **Completed:** 2026-09-29T14:45:00Z
- **Tasks:** 2
- **Files modified:** 7

## Accomplishments
- The parent's dominant workflow is live: paste a 10–20 word textbook lesson, preview it (valid rows numbered, per-line problems as Warning rows excluded from the commit count), assign one unit, commit once (D-10)
- Transparency prohibition is mechanical: both counts always render, and when words are skipped the banner turns Warning and NAMES the affected words (derived from a pre-commit snapshot; the API itself returns counts only)
- parseBatch is a DOM-free module with 9 numbered node-test assertion blocks wired into `npm test` — parser logic testable without a browser (fast feedback cycle per CONTEXT.md)
- Final single-form shape landed: D-06 required category radio with 请选择分类 validation, D-08 only 单词 required, shared unit datalist refreshed per category, loading states with kept labels, success toast retaining category+unit for rapid consecutive entry

## Task Commits

Each task was committed atomically:

1. **Task 1: parent-batch-parser.js — pure paste parser** — RED `b400a63` (test(09-06): add failing test for batch paste parser), GREEN `891ea46` (feat(09-06): implement batch paste parser (pure, node-tested)) — committed by the interrupted first session; verified green this session, not redone
2. **Task 2: Dual-mode 添加单词 card** — `287ddd4` (feat(09-06): add dual-mode add-words card (tabs + batch paste preview)) — adopted the interrupted session's uncommitted in-flight work after diff review; completed and committed this session

**Plan metadata:** see docs commit for SUMMARY/STATE/ROADMAP.

## TDD Gate Compliance

| Task | RED commit | GREEN commit | Sequence valid |
|------|-----------|--------------|----------------|
| 1 (batch parser) | b400a63 | 891ea46 | ✓ |

_Task 2 is a plain auto task (UI wiring) — not TDD-gated; its behavior is covered by the Playwright smoke._

## Files Created/Modified
- `pkg/web/static/js/parent-batch-parser.js` (NEW) — parseBatch IIFE, window-or-module-global tail, zero DOM references
- `tests/test-parent-batch-parser.js` (NEW) — numbered assertion blocks 1–9 covering every behavior clause
- `package.json` — scripts.test chain grew `&& node tests/test-parent-batch-parser.js`
- `pkg/web/static/js/parent-modes.js` — AddCard tabs, final SingleAddForm, BatchAddForm (parse → preview → commit → banner), loading buttons, category/unit field builders
- `pkg/web/static/js/parent-app.js` — AddCard wiring, shared datalist population (init all-categories + per-category change), refresh hooks
- `pkg/web/static/parent.html` — parser script tag before parent-modes.js, shared #unit-options datalist, ?v=20260928 cache-bust
- `pkg/web/static/styles/parent.css` — tabs (3px accent underline, 44px targets), preview table (8px rhythm, sticky header, 320px scroll, Warning rows), result banner (#059669/#d97706), CSS spinner

## Decisions Made
- Resume reconciliation: the interrupted executor's uncommitted Task 2 work (the plan's exact declared file set) was diff-reviewed file-by-file against the Task 2 spec and adopted — every action item (tabs controller, D-06 radio pair, D-08 optional fields, datalist wiring, preview table, zero-row disable, banner copy/colors, createElement/textContent, firstChild-removeChild, module tail) was already present; this session verified, smoke-tested, and committed it without rewriting anything (Plan 09-04 precedent)
- The batch tab's skipped-words list comes from a pre-commit GET /words?category= snapshot instead of an API change: skips are UNIQUE(word, category), so skipped == pasted ∩ existing; snapshot failure degrades to the honest counts with a generic note
- Client-side 100-entry refusal mirrors the API's maxEntries — oversized pastes get a split-paste hint before any request is sent
- The manual-smoke acceptance was executed mechanically via the Playwright harness with a temporary spec (created, run, deleted — not committed, since tests/e2e is outside this plan's files_modified); permanent batch e2e coverage belongs to 09-08 (收尾 E2E)

## Deviations from Plan

None - plan executed exactly as written. (The Task 2 adoption is a resume-context action, not a spec deviation — the adopted work matched the plan's file set and passed every verify/acceptance check without fixes.)

## Issues Encountered
- Transient `go test -short ./...` failure in cmd/service immediately after the Playwright run: the e2e webServer's service was still tearing down and held :8080. Isolated re-run passed, port verified free, full suite green on the second run — environmental contention, not a code defect.
- The manual-smoke criterion could not be satisfied by the existing committed e2e spec (it covers only the 09-01 single-add tracer); resolved with the temporary-spec approach above. The tracer spec doubling as the single-form regression check passed unchanged through the new AddCard wrapper — evidence the adoption broke nothing.

## User Setup Required

None - no external service configuration required.

## Next Phase Readiness
- 09-07 (browse/organize + settings UI) builds against the same REST surface; the add card is final — no further parent-modes.js churn expected except the edit modal it will add
- The shared datalist and onCategoryChange wiring are reusable as-is for 09-07's category filtering
- REQUIREMENTS.md VOC-01/WEB-01 intentionally left Pending: siblings 09-07/09-08 declare the same IDs (shared-ID gate #2388; ready-ids returned 0/2); they flip when those summaries land
- Smoke was net-zero on the environment: the throwaway unit (3 words) was deleted via DELETE /api/v1/words in the spec's afterEach

---
*Phase: 09-parent-vocabulary-management*
*Completed: 2026-09-29*

## Self-Check: PASSED

- key-files verified on disk (7/7 present, all committed): parent-batch-parser.js, test-parent-batch-parser.js (created); parent-modes.js, parent-app.js, parent.html, parent.css, package.json (modified)
- `git log --grep="09-06"` returns 3 task commits (b400a63, 891ea46, 287ddd4)
- All `<acceptance_criteria>` re-run green:
  - Task 1: `node tests/test-parent-batch-parser.js` — PASS; `npm test` full chain — PASS; numbered assertion blocks 1–9 present; parser has zero DOM references (require works in plain node)
  - Task 2: innerHTML grep over parent-modes.js/parent-app.js/parent-batch-parser.js — clean (also insertAdjacentHTML/outerHTML/document.write clean); parseBatch referenced 3× in parent-modes.js; Playwright smoke on the real service — 6/6 suite passed (batch flow both banner colors + tracer regression)
- Plan `<verification>` re-run: `npm test` green; `go test -short ./...` all 14 packages ok; live paste→preview→commit→banner→browse-refresh cycle proven by the e2e run
