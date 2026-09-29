---
phase: 09-parent-vocabulary-management
plan: 08
subsystem: testing
tags: [playwright-e2e, responsive-matrix, backstop-verification, webkit-line-clamp, test-data-cleanup, vanilla-js, computed-style-discriminators]

# Dependency graph
requires:
  - phase: 09-parent-vocabulary-management
    provides: 09-06 add-words UI (batch tabs/preview/banner) + 09-07 browse/organize/settings UI (groups, modals, sliders, D-03 grid) + the full 09-01/04/05 REST surface served by the real run.ps1 service
provides:
  - Full parent-management flow e2e (tests/e2e/parent-management.spec.js): batch add with duplicate-skip banner → group browse + collapse persistence → search/clear → edit → rename → delete word → settings save/restore → UI cleanup with a finally-shaped API backstop; idempotent across consecutive runs
  - 3-viewport responsive matrix (375x812 / 820x1180 / 1280x800) asserting the D-03 discriminators via computed styles/matchMedia + ≥16px text inputs
  - Backstop evidence for the two 🧪 UI-SPEC rows: 2-line meaning clamp + unit-name ellipsis/title, 320px preview cap + page scroll with collapsed group — 5 full-page screenshots under test-results/
  - Rule-2 fix: .word-meaning -webkit-line-clamp: 2 and .unit-name title attribute (the styles 09-08's must-haves assert, absent from 09-07's CSS)
  - Phase-gate record: go -short (13 pkgs) + npm test (7 files) + playwright 7/7 + pybridge e2e, all green
affects: [verify-work, phase-09-close]

# Actuals (#2632)
actuals:
  tokens: 4300   # chars/4 over the realized diff of 730732d + e904f7a (445+/7- across 3 files; estimate was 30000)
  tasks: 2
  commits: 2

# Tech tracking
tech-stack:
  added: []
  patterns:
    - "Behavioral clamp assertion: this Chromium computes display:-webkit-box as flow-root while -webkit-line-clamp still applies — assert clamp/box-orient/overflow computed values PLUS rendered height within N line-heights, never the display alias"
    - "Deterministic duplicate-skip fixture: API-seed the word the batch paste re-includes, so the server MUST skip it and the 已添加/跳过 banner counts are stable across runs"
    - "finally-shaped e2e cleanup: UI deletion is the asserted happy path; an API DELETE of every e2e-* unit in finally guarantees net-zero even on mid-flow failure (T-09-12)"

key-files:
  created: []
  modified:
    - tests/e2e/parent-management.spec.js
    - pkg/web/static/styles/parent.css
    - pkg/web/static/js/parent-browse.js

key-decisions:
  - "Backstop styles shipped under Rule 2 inside the e2e plan: the 2-line meaning clamp and unit-name full-text availability asserted by this plan's must-haves were absent from the Plan 09-07 styles — fixed in the Task 2 commit rather than shipping a test that proves the spec false"
  - "E2E net-zero made structural: dedicated e2e-* units only, UI cleanup asserts the exact-count destructive copy, API DELETE backstop in finally; suite proven idempotent (two consecutive green runs)"
  - "The ≥16px input assert scoped to text-entry inputs (input[type=text]/textarea): iOS zoom-on-focus targets; radio inputs carry Chrome's UA 13.33px default and are not zoom targets"
  - "Chromium clamp discrimination is behavioral (height ≤ 2 line-heights), not display:-webkit-box equality"

patterns-established:
  - "Pattern: viewport matrix in one test via setViewportSize loop with per-viewport expectation table + full-page screenshot per stop (ipad-layout.spec.js discriminator approach, generalized)"

requirements-completed: [WEB-01, VOC-01, VOC-02, VOC-03]  # copied from plan frontmatter verbatim; REQUIREMENTS.md flips gated on the shared-ID ready-ids re-check after this SUMMARY

coverage:
  - id: D1
    description: "Full parent flow proven in a real browser against the real stack: batch add (4-line paste incl. # comment + duplicate) → 3 valid preview rows → 确认添加（3 个） → 已添加 2 个，跳过 1 个重复单词 Warning banner naming the skipped word → group badge 3 个单词 → collapse survives reload → search narrows/✕ restores → edit round-trips 意思 → rename updates the header → delete word behind exact-word confirm (badge decrements) → settings 0.90 save/reload/GET → UI restore to 0.85/180 → UI deletion of e2e-full-2 and e2e-tracer with exact-count copy"
    requirement: WEB-01
    verification:
      - kind: e2e
        ref: "tests/e2e/parent-management.spec.js#full flow: batch add → browse → search → edit → rename → delete → settings → cleanup"
        status: pass
    human_judgment: false
  - id: D2
    description: "Cleanup leaves the library as found and the suite is idempotent: no e2e-* unit survives a run (asserted in-flow via group absence + API backstop in finally), verified by two consecutive green runs"
    verification:
      - kind: e2e
        ref: "command: npx playwright test tests/e2e/parent-management.spec.js — run twice back-to-back, 2/2 then 2/2 pass"
        status: pass
    human_judgment: false
  - id: D3
    description: "Responsive matrix at phone 375x812 / tablet 820x1180 / desktop 1280x800: <1024px single column (display:block, DOM-order stacking, card padding 16px→24px at ≥768px, toolbar stacks on phone); ≥1024px two-column grid (display:grid, 2 tracks, browse wider than rail, container max-width 1120px); every text input ≥16px at all three stops"
    requirement: WEB-01
    verification:
      - kind: e2e
        ref: "tests/e2e/parent-management.spec.js#responsive matrix (phone/tablet/desktop) + backstop visual checks"
        status: pass
    human_judgment: false
  - id: D4
    description: "Long-text backstop (UI-SPEC 🧪 row): over-length meaning clamps to 2 lines with hidden overflow; over-length unit name stays single-line ellipsized with the full text available (title attribute)"
    requirement: WEB-01
    verification:
      - kind: e2e
        ref: "tests/e2e/parent-management.spec.js#responsive matrix — webkitLineClamp=2, box-orient vertical, overflow hidden, rendered height ≤ 2 line-heights; unit-name nowrap/ellipsis/hidden + title equals the full name"
        status: pass
    human_judgment: true
    rationale: "UI-SPEC marks this row 🧪 backstop — visual adequacy of the ellipsis/clamp rendering is routed to the human; durable evidence at test-results/parent-management-responsi-eb979-ktop-backstop-visual-checks/{backstop-longtext-desktop.png, matrix-phone-375x812.png, matrix-tablet-820x1180.png, matrix-desktop-1280x800.png} (regenerated each run)"
  - id: D5
    description: "Overflow backstop (UI-SPEC 🧪 row): 25-line paste renders a preview region capped at 320px with inner scroll (sticky header); the long page scrolls with a collapsed group present — no 20-unit seeding, the fixture unit is deleted afterwards"
    verification:
      - kind: e2e
        ref: "tests/e2e/parent-management.spec.js#responsive matrix — maxHeight 320px, overflowY auto, scrollHeight > clientHeight, document scrollHeight > innerHeight after collapse"
        status: pass
    human_judgment: true
    rationale: "UI-SPEC marks this row 🧪 backstop — visual confirmation of the capped-table scroll UX is routed to the human; durable evidence at test-results/parent-management-responsi-eb979-ktop-backstop-visual-checks/backstop-preview-overflow.png (regenerated each run)"
  - id: D6
    description: "Phase full-suite gate recorded for /gsd-verify-work: go test -count=1 -short ./... (13 pkgs ok), npm test (7 node test files), npx playwright test (7/7 incl. student dictation-flow + ipad-layout), pybridge real-Python e2e (ok)"
    verification:
      - kind: integration
        ref: "command: go test -count=1 -short ./... ; npm test ; npx playwright test ; go test -tags=e2e -count=1 ./pkg/pybridge/ — all green 2026-09-29"
        status: pass
    human_judgment: false

# Metrics
duration: 22min
completed: 2026-09-29
status: complete
---

# Phase 9 Plan 08: E2E capstone — full parent flow + 3-viewport matrix + phase gate Summary

**Playwright now drives the complete parent management flow against the real service in one idempotent spec (batch add with honest skip counts → collapse-persistent browse → search → edit → rename → delete → settings round-trip → self-cleaning teardown), plus a 3-viewport D-03 matrix and screenshot-backed proof for the two 🧪 UI-SPEC backstop rows — the phase gate (go -short / npm / playwright 7/7 / pybridge e2e) recorded green.**

## Performance

- **Duration:** 22 min
- **Started:** 2026-09-29T15:26:46Z
- **Completed:** 2026-09-29T15:49:12Z
- **Tasks:** 2
- **Files modified:** 3

## Accomplishments
- The CONTEXT.md minimum end-to-end-flow standard is met for the whole phase: the complete management flow runs through real browser → REST → pybridge → vocab_manager.py → SQLite in one spec, ending with the library left as found (e2e-full-2 and e2e-tracer deleted via the UI with exact-count copy; API backstop in finally)
- The duplicate-skip transparency banner is exercised deterministically: the test API-seeds `e2e-dup-seed`, the paste re-includes it, and the banner must show 已添加 2 个，跳过 1 个重复单词 (Warning variant, skipped word named) — server counts, stable across runs
- D-03 responsive matrix green at 375x812 / 820x1180 / 1280x800 with computed-style + matchMedia discriminators (single column below 1024px, 2fr/1fr grid + 1120px cap at ≥1024px, toolbar stacks on phone, card padding 16→24px, text inputs ≥16px everywhere)
- Both 🧪 UI-SPEC backstop rows carry automated asserts AND full-page screenshot evidence under test-results/ (long-text clamp/ellipsis + 320px preview cap + page scroll with collapsed group)
- Phase gate recorded green: `go test -count=1 -short ./...` (13 pkgs), `npm test` (7 files), `npx playwright test` (7/7 incl. student specs), pybridge real-Python e2e ok

## Task Commits

Each task was committed atomically:

1. **Task 1: Full-flow spec — add → preview → commit → group → search → edit → rename → delete → settings, with cleanup** - `730732d` (test)
2. **Task 2: Viewport matrix (phone/tablet/desktop) + backstop visual checks + phase full-suite gate** - `e904f7a` (test, carries the Rule-2 backstop-style fix)

**Plan metadata:** see the docs commit for SUMMARY/STATE/ROADMAP/REQUIREMENTS.

## Files Created/Modified
- `tests/e2e/parent-management.spec.js` — kept the 09-01 tracer; added the full-flow test (dedicated e2e-full unit, try/finally API cleanup, settings restore to 0.85/180) and the responsive-matrix + backstop test (per-viewport discriminator table, backstop fixtures, 5 screenshots)
- `pkg/web/static/styles/parent.css` — `.word-meaning` gains the UI-SPEC long-text clamp (`display: -webkit-box; -webkit-box-orient: vertical; -webkit-line-clamp: 2; overflow: hidden`)
- `pkg/web/static/js/parent-browse.js` — `.unit-name` span gains `title = unit` so the full name stays available beyond the single-line ellipsis

## Decisions Made
- Shipped the missing backstop styles (Rule 2) instead of writing asserts that codify spec-noncompliance: the plan's must-have truth "meanings wrap to a maximum of 2 lines then ellipsize" and Task 2's "computed -webkit-line-clamp / overflow behavior" assume styles "(from Plan 09-07)" that were never shipped; the fix is 8 CSS lines + 1 JS line, asserted behaviorally (rendered height ≤ 2 line-heights — this Chromium computes `display:-webkit-box` as `flow-root` while the clamp applies)
- The ≥16px no-zoom assert covers text-entry inputs (`input[type=text]`, `textarea`) — the iOS zoom-on-focus surface; radio inputs keep Chrome's UA default (13.33px) and are not zoom targets
- Matrix/backstop fixtures reuse one throwaway `e2e-backstop-*` unit (over-length name + long meaning), preview-only overflow paste (never committed), and per-test API cleanup — the family library stays clean without 20-unit seeding
- Cleanup deletes the Plan 09-01 `e2e-tracer` leftovers through the UI (the tracer test precedes the full-flow test in the same file, so the group exists in a suite run; the step is conditional for standalone runs)

## Deviations from Plan

### Auto-fixed Issues

**1. [Rule 2 - Missing Critical] Shipped the 2-line meaning clamp + unit-name title attribute the plan asserts**
- **Found during:** Task 2 (backstop visual checks)
- **Issue:** The plan's must-have truth and Task 2's automated assert require `-webkit-line-clamp` styles "(from Plan 09-07)", but `parent.css` never shipped them (only the unit-name ellipsis existed) and no full-text-beyond-ellipsis affordance existed — the automated assert would have had to either fail or prove the UI-SPEC false
- **Fix:** Added the clamp block to `.word-meaning` (parent.css) and `name.title = unit` in `buildGroup` (parent-browse.js); asserted via computed clamp/orient/overflow values plus the rendered 2-line height (behavioral, immune to the Chromium `flow-root` display aliasing)
- **Files modified:** pkg/web/static/styles/parent.css, pkg/web/static/js/parent-browse.js
- **Verification:** Full parent spec 3/3 green (twice); screenshots show the 2-line clamp and ellipsized header
- **Committed in:** e904f7a (part of the Task 2 commit)

---

**Total deviations:** 1 auto-fixed (Rule 2 — missing functionality the plan's assertions depend on; file scope extended beyond `files_modified` for this reason, explicitly staged by path)
**Impact on plan:** None negative — the phase's must-have truth is now TRUE with automated + screenshot proof; the fix is revertible independently.

## Issues Encountered
- First `go test -v -short ./...` attempt (run in parallel with `npm test`) ended FAIL with the failing package outside the captured tail; an isolated fresh `go test -count=1 -short ./...` re-run was fully green (13 pkgs, exit 0, zero FAIL lines) — the documented transient-contention pattern from 09-06, not treated as a failure
- Playwright's strict mode caught the first ≥16px input assert scanning ALL inputs (radio inputs report Chrome's UA 13.3333px default) — scoped to text-entry inputs, the actual zoom-on-focus surface
- Pre-existing leftovers observed in the live library: units `e2e-0907-unit-1790694195008` (2 words) and `e2e-0907-unit-del-1790694469304` (1 word) from the prior session's throwaway smoke, present before this run started. Out of this plan's cleanup scope (only e2e-full/e2e-full-2/e2e-tracer/e2e-backstop are ours); flagged for a one-click manual removal or verifier decision
- This plan's screenshot evidence lives under gitignored `test-results/` per the plan spec — the exact paths are recorded in the coverage block; a re-run of the parent spec regenerates them

## User Setup Required

None - no external service configuration required.

## Next Phase Readiness
- Phase 9 is 8/8 — ready for `/gsd-verify-work` (phase gate results recorded in coverage D6; VOC-01/02/03 + WEB-01 REQUIREMENTS flips ride the shared-ID gate with this SUMMARY)
- The parent page now has browser-E2E coverage for every UI-SPEC Interaction row; future UI changes regress against the real service in ~50s
- Residual for the verifier: the two pre-existing `e2e-0907-*` units noted above; screenshots under test-results/ for the backstop rows

---
*Phase: 09-parent-vocabulary-management*
*Completed: 2026-09-29*

## Self-Check: PASSED

- key-files verified on disk and committed (3/3): tests/e2e/parent-management.spec.js, pkg/web/static/styles/parent.css, pkg/web/static/js/parent-browse.js
- `git log --grep="09-08"` returns the 2 task commits (730732d, e904f7a)
- All `<acceptance_criteria>` re-run green:
  - Task 1: `npx playwright test tests/e2e/parent-management.spec.js` — 2/2 pass, run TWICE consecutively (idempotence criterion); cleanup asserts both e2e groups gone; settings file restored to 0.85/180 (asserted via GET in-test and visible in the backstop screenshot readouts)
  - Task 2: matrix green at all three viewports (3/3 with the full file); 5 screenshots captured under test-results/parent-management-responsi-eb979-ktop-backstop-visual-checks/ and visually inspected (long-text clamp + ellipsis, 320px capped preview, per-viewport layouts); phase gate: `go test -count=1 -short ./...` exit 0 (13 pkgs), `npm test` all 7 files pass, `npx playwright test` 7/7, `go test -tags=e2e -count=1 ./pkg/pybridge/` ok
- Plan `<verification>` re-run: all four suite commands green; consecutive-run idempotence satisfied by the two back-to-back spec runs
