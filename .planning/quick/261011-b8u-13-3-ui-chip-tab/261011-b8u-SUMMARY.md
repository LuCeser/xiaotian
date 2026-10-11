---
phase: 13.3-parent-tutor-fusion
plan: quick-261011-b8u
subsystem: ui
tags: [css, wcag-aa, contrast, touch-target, accent-unification, vanilla-js]

# Dependency graph
requires:
  - phase: 13.3-parent-tutor-fusion
    provides: the audited parent workspace (save chips, tab bar) and tutor 回首页 door whose CSS this task fixes
provides:
  - Save-chip saved/pending text at WCAG AA contrast (5.21:1 / 4.84:1) on unchanged tinted backgrounds
  - Single family accent blue #2563eb across home→parent and tutor→home navigation doors
  - .parent-tab 60px touch target matching the home doors / .rt-btn-home convention
affects: [13.3 ui audits, future parent/tutor styling, e2e-rt suites]

# Actuals (#2632) — pairs with the plan's `estimate` to calibrate future estimates.
# Same estimateTokens scale (chars/4 over the realized diff), never a harness token count.
actuals:
  tokens: 346
  tasks: 3
  commits: 3
plan_head_before: b21946bdc7e1fb4ccd5b4411252e67d745c73c3c
plan_head_after: a930e75cabdeb0bac43da44fd29040f309c05a4d

# Tech tracking
tech-stack:
  added: []
  patterns:
    - "Scoped literal color override on tinted chip backgrounds — root tokens (--success/--warning) stay untouched for their ten white/surface consumers"

key-files:
  created: []
  modified:
    - pkg/web/static/styles/parent.css
    - pkg/web/static/tutor/styles/tutor.css

key-decisions:
  - "Chip text darkened via scoped literals (#047857/#b45309) instead of retokening --success/--warning — ten other token consumers on white backgrounds stay correct (plan-mandated)"
  - "回首页 uses hardcoded hex #2563eb, not var(--accent) — parent.css :root does not load on tutor pages; tutor.css's pre-existing hardcoded-hex convention kept"
  - ".parent-tab fixed at 60px (parity) not the audit's 48px minimum — plan explicitly chose convention parity"

patterns-established:
  - "Contrast annotations inline on changed declarations (ratio + why) so future edits do not silently regress AA"

requirements-completed: [UI-13.3-FIX-1, UI-13.3-FIX-2, UI-13.3-FIX-3]

coverage:
  - id: D1
    description: "Save-chip saved/pending text contrast >= 4.5:1 WCAG AA (#047857 on #ecfdf5, #b45309 on #fffbeb), backgrounds and root tokens unchanged"
    requirement: UI-13.3-FIX-1
    verification:
      - kind: other
        ref: "computed WCAG ratio: #047857/#ecfdf5 = 5.21:1, #b45309/#fffbeb = 4.84:1 (relative-luminance one-liner, re-measured at execution)"
        status: pass
      - kind: other
        ref: "grep gate: CHIP-CONTRAST-OK (.save-chip-saved declares #047857, .save-chip-pending declares #b45309)"
        status: pass
    human_judgment: false
  - id: D2
    description: "Tutor 回首页 (.rt-banner .rt-btn-home) background is #2563eb, the family accent; all other tutor.css #2f6fed internals untouched"
    requirement: UI-13.3-FIX-2
    verification:
      - kind: other
        ref: "grep gate: ACCENT-UNIFIED-OK (block contains #2563eb, no #2f6fed); git diff shows one-line change; 6 pre-existing #2f6fed usages elsewhere intact"
        status: pass
    human_judgment: false
  - id: D3
    description: ".parent-tab min-height 60px with padding unchanged; chip copy and 回首页 door behavior regression-free on the mock origin"
    requirement: UI-13.3-FIX-3
    verification:
      - kind: e2e
        ref: "npx playwright test -c tests/playwright.rt.config.js tests/e2e-rt/parent-lessons.spec.js tests/e2e-rt/home-button.spec.js — 12 passed (43.6s), mock server auto-booted on :8099"
        status: pass
      - kind: other
        ref: "grep gate: TAB-TARGET-OK + node --check pkg/web/static/js/parent-lessons.js"
        status: pass
    human_judgment: false

# Metrics
duration: 5min
completed: 2026-10-11
status: complete
---

# Quick Task 261011-b8u: 13.3 UI Audit Top-3 Fixes Summary

**Save-chip text darkened to WCAG AA literals (#047857/#b45309), tutor 回首页 unified to the family accent #2563eb, and .parent-tab raised to the 60px touch convention — three atomic one-file commits, 12/12 e2e green.**

## Performance

- **Duration:** 5 min
- **Started:** 2026-10-11T00:15:27Z
- **Completed:** 2026-10-11T00:20:05Z
- **Tasks:** 3
- **Files modified:** 2

## Accomplishments
- Save-chip honest-copy payload now passes WCAG AA: `.save-chip-saved` 5.21:1, `.save-chip-pending` 4.84:1 (both >= 4.5:1 at 14px semibold), backgrounds (#ecfdf5/#fffbeb) and root tokens (--success/--warning) byte-identical
- The tutor→home door reads as the same product blue as home→parent: `.rt-banner .rt-btn-home` background #2f6fed → #2563eb, single one-line diff; the 6 other pre-existing #2f6fed tutor-internal usages (state chips, word-chip highlight, progress gradient, check buttons) untouched per audit scoping
- `.parent-tab` touch target 44px → 60px, matching home doors and `.rt-btn-home`; padding and all other declarations unchanged; no centering rules added (button UA default centers the label)
- No selector renamed — `.save-chip*`, `.parent-tab`, `.rt-btn-home` keep their names, so tests/e2e-rt bindings are intact (verified by the passing suites)

## Task Commits

Each task was committed atomically:

1. **Task 1: Save-chip text contrast to WCAG AA (green-700 / amber-700)** - `cc20638` (fix)
2. **Task 2: Unify 回首页 accent blue to the family value #2563eb** - `de393e3` (fix)
3. **Task 3: .parent-tab touch target 60px, then run the full gate** - `a930e75` (fix)

## Files Created/Modified
- `pkg/web/static/styles/parent.css` - chip text literals (2 lines) + tab min-height (1 line); each change annotated inline with its ratio/convention rationale
- `pkg/web/static/tutor/styles/tutor.css` - one background declaration #2f6fed → #2563eb with family-accent comment

## Verification Evidence

- **Value-only check:** cumulative `git diff b21946b..HEAD --stat` = 2 files, 4 insertions + 4 deletions; no selector, HTML, or JS file changed
- **Contrast re-measured at execution** (relative-luminance, WCAG 2.x formula): saved 5.21:1, pending 4.84:1 — matches the plan's planning-time measurements exactly
- **Syntax gate:** `node --check pkg/web/static/js/parent-lessons.js` PASS
- **E2E smoke (mock origin, port 8099 auto-booted, chromium preinstalled):** `npx playwright test -c tests/playwright.rt.config.js tests/e2e-rt/parent-lessons.spec.js tests/e2e-rt/home-button.spec.js` → **12 passed (43.6s)**, 0 failed, 0 skipped. Ran by FILE PATH as the plan mandated (`-g "parent"` would have matched zero test titles). No test asserts the changed hex values or tab height (grep over tests/ confirms) — the pass certifies no collateral breakage of chip copy and door behavior

## Decisions Made
- Added brief inline CSS comments on the three changed declarations (ratio values / family-accent note / touch-parity note). The plan did not ask for them; they guard against future edits silently regressing AA or re-diverging the accent. No comment text was added beyond the changed lines; the rt-btn-home block's existing comment untouched as instructed.
- Everything else followed the plan verbatim, including exact commit messages.

## Deviations from Plan

None - plan executed exactly as written.

## Issues Encountered

None — port 8099 was free, chromium was already installed, both suites passed first run.

## Adjacent Observation (out of scope, per plan verification section)

- `.save-chip-failed` (#dc2626 on #fef2f2) measures 4.41:1 — marginally below the 4.5:1 AA threshold. Candidate follow-up alongside the audit's other unfixed minors (page title 单词库管理, 17px/18px one-off CTA sizes). Deliberately not fixed here: the plan locked scope to the Top-3.

## User Setup Required

None - no external service configuration required.

## Next Phase Readiness
- The three priority findings of 13.3-UI-REVIEW.md are closed; remaining audit minors (title copy, one-off sizes, failed-chip 4.41:1, tablist arrow-key pattern) stay open for a future batch
- E2E suites remain green on the mock origin; no test changes were needed or made

## Self-Check: PASSED
- Files: pkg/web/static/styles/parent.css, pkg/web/static/tutor/styles/tutor.css, and this SUMMARY all exist on disk
- Commits: cc20638, de393e3, a930e75 all present in HEAD history (worktree-agent-ad557e6655c71c425, base b21946b)
- Commits measured from ledger: `git rev-list --count b21946b..HEAD` = 3 (matches frontmatter)
