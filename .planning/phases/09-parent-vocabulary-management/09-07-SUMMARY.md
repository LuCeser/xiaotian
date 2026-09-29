---
phase: 09-parent-vocabulary-management
plan: 07
subsystem: ui
tags: [browse-view, unit-groups, collapse-state, debounced-search, modal-focus-trap, destructive-confirm, fsrs-settings-ui, vanilla-js, playwright-e2e, dom-safety]

# Dependency graph
requires:
  - phase: 09-parent-vocabulary-management
    provides: 09-04/09-05 REST surface (GET /words?q=&category=, GET /units?category= merge-by-name, PUT /words/{id}, POST /units/rename, DELETE /words, GET/PUT /settings/fsrs) + 09-06 add card (AddCard, shared #unit-options datalist, __parentToast)
provides:
  - BrowseView — the full 浏览词库 experience: toolbar (300ms-debounced search + ✕ clear + 全部/英语/中文 pills), client-side unit groups with Go-computed count badges, skeleton/empty/no-match/error states
  - Collapse persistence contract — localStorage key parent.units.collapsed (JSON array of unit names), read defensively on every render
  - Edit modal (编辑单词, D-08/D-07 semantics) + rename modal (Pitfall 2 surfaced verbatim) + destructive delete-word/delete-unit confirms with exact-scope copy
  - SettingsCard — the 学习设置 card (D-15): two FSRS sliders with hints + live 当前/默认 readouts, 保存设置 → PUT, request-free 恢复默认, D-13 note
  - Final page layout — ≥1024px two-column grid (D-03: browse 2fr / rail 1fr), card padding 16→24px at ≥768px
affects: [09-08]

# Actuals (#2632)
actuals:
  tokens: 26000   # chars/4 over the realized diffs of bee92a8 + 34517f0 + 009f522 (~1750 lines across 5 files; estimate was 50000)
  tasks: 3
  commits: 3

# Tech tracking
tech-stack:
  added: []
  patterns:
    - "BrowseView controller: toolbar built once, only the list region re-renders; fetch sequence token drops stale responses when a debounced search races a pill switch"
    - "Exact-scope destructive confirms: unit ops iterate the categories present in the group's rows, and header actions are withheld in the 全部+search combination so the dialog copy always matches the DELETE {category, unit} server scope (T-09-05)"
    - "Sibling-button group headers: the 48px aria-expanded toggle is a SIBLING of the 重命名/删除 actions — buttons never nest (invalid HTML breaks the a11y tree)"
    - "Modal helper contract: role=dialog + aria-modal + title-labelled, Tab-trapped, Esc closes, overlay click inert (destructive confirms are not dismissible by stray taps), initial focus = first input or 取消"

key-files:
  created: []
  modified:
    - pkg/web/static/js/parent-browse.js
    - pkg/web/static/js/parent-modes.js
    - pkg/web/static/js/parent-app.js
    - pkg/web/static/parent.html
    - pkg/web/static/styles/parent.css

key-decisions:
  - "Unit-level 重命名/删除 act per-category across the categories present in the group's rows; header actions are WITHHELD in the 全部+search combination — the destructive confirm copy must always match what DELETE {category, unit} will actually do (T-09-05 exact-scope guarantee)"
  - "Group headers keep the aria-expanded toggle as a sibling of the action buttons rather than nesting buttons inside the toggle (invalid HTML); 未分组 group gets no header actions (no {category, unit} target exists) but keeps word-level ops and collapse"
  - "Edit modal sends ALL four fields (provided-empty clears the column) because it renders the word's full current state; category shows as a read-only badge per D-07"
  - "SettingsCard interval slider caps at 365 per the plan though the API allows 36500 — values above were never settable from this UI; retention clamped client-side to 0.70–0.95 before PUT"
  - "Manual-smoke acceptance executed mechanically via the Playwright harness with a throwaway spec (created, run, deleted — not committed, tests/e2e is outside this plan's files_modified); the permanent full-flow e2e belongs to 09-08 (09-06 precedent)"

patterns-established:
  - "Pattern: BrowseView selector hooks for e2e — .unit-group[data-unit], .unit-toggle, .unit-count, .search-input, .search-clear, .row-action; .word-row[data-word]/.word-text kept from the 09-01 tracer contract"
  - "Pattern: stale-response guard via monotonic fetch sequence token in refresh()-style components"

requirements-completed: [VOC-02, VOC-03, WEB-01]  # copied from plan frontmatter verbatim; REQUIREMENTS.md flips stay gated on sibling 09-08 (shared-ID rule #2388 — ready-ids returned 0/3 this plan)

coverage:
  - id: D1
    description: "Browse experience: toolbar with debounced search (✕ 清空搜索), 全部/英语/中文 pills, collapsible unit groups (48px aria-expanded toggle + {n} 个单词 badge from GET /units), collapse persistence in localStorage parent.units.collapsed, skeleton/empty/no-match/error states"
    requirement: VOC-02
    verification:
      - kind: other
        ref: "command: rg innerHTML over parent-browse.js → no markup setters; parent.units.collapsed ×3, aria-expanded ×3, setTimeout ×3"
        status: pass
      - kind: automated_ui
        ref: "playwright: throwaway smoke-0907 spec — group renders 2 rows with badge '2 个单词'; collapse hides body + aria-expanded=false; RELOAD → still collapsed (localStorage); search narrows to 1 row; no-match copy renders; 清空搜索 restores both"
        status: pass
    human_judgment: false
  - id: D2
    description: "编辑单词 modal: D-08 only 单词 required (inline 请输入单词), D-07 read-only category badge, PUT /api/v1/words/{id} with loading state → 已保存 toast → list reflects the change"
    requirement: WEB-01
    verification:
      - kind: automated_ui
        ref: "playwright: throwaway smoke-0907 spec — meaning edit round-trips into the row; empty 单词 marks the inline error; Esc closes the dialog"
        status: pass
    human_judgment: false
  - id: D3
    description: "Rename + destructive deletes: 将「{old}」重命名为 → POST /units/rename (not_found surfaces the backend message verbatim in an Error toast, Pitfall 2); delete word/unit destructive confirms with exact word/count copy → DELETE /api/v1/words; dialog is the only confirmation layer"
    requirement: VOC-02
    verification:
      - kind: automated_ui
        ref: "playwright: throwaway smoke-0907 spec — rename updates the group; unit deleted behind the open rename dialog → error toast carries backend not-found text; delete-word removes exactly that row; delete-unit copy shows 全部 1 个单词 and the group disappears"
        status: pass
      - kind: other
        ref: "command: rg 确认删除 ×5, 无法恢复 ×2 in parent-browse.js"
        status: pass
    human_judgment: false
  - id: D4
    description: "学习设置 card (D-15): two sliders with hints + live 当前 {v} · 默认 {d} readouts, D-13 note, 保存设置 → clamped PUT /api/v1/settings/fsrs → toast 已保存，下次听写生效, 恢复默认 resets BOTH sliders with NO request"
    requirement: VOC-03
    verification:
      - kind: automated_ui
        ref: "playwright: throwaway smoke-0907 spec — readouts track slider input; 恢复默认 with a request listener attached → zero requests fired, values reset; 保存设置 PUT body {0.88, 150} verified via waitForRequest, toast text exact, GET reflects saved values; server defaults restored afterwards"
        status: pass
      - kind: other
        ref: "command: rg 学习设置 ×3, 已保存，下次听写生效 ×2 in parent-modes.js"
        status: pass
    human_judgment: false
  - id: D5
    description: "Responsive layout (D-03): <1024px single column stacked in DOM order; ≥1024px two-column grid (浏览词库 2fr spanning both rows / 添加单词+学习设置 rail 1fr), container max-width 1120px centered, card padding 16→24px at ≥768px"
    requirement: WEB-01
    verification:
      - kind: automated_ui
        ref: "playwright: throwaway smoke-0907 spec — computed grid-template-columns is two tracks at 1200px viewport and one track at 800px"
        status: pass
    human_judgment: false
  - id: D6
    description: "UI Considerations backstop rows: long-text wrapping/ellipsis and 20+-unit overflow behavior in the real viewport matrix"
    verification: []
    human_judgment: true
    rationale: "UI-SPEC marks these 🧪 backstop — they are to be verified visually in 09-08's e2e viewport matrix, not by this plan's smoke"

# Metrics
duration: 30min
completed: 2026-09-29
status: complete
---

# Phase 9 Plan 07: Browse + organize UI and 学习设置 card Summary

**Collapsible unit-group browse with 300ms-debounced search, edit/rename/delete modals carrying exact-scope destructive confirms and verbatim backend error toasts, plus the D-15 学习设置 sliders card — the parent page now consumes every Plan 09-01/04/05 API route and wears its final ≥1024px two-column layout.**

## Performance

- **Duration:** 30 min
- **Started:** 2026-09-29T14:40:15Z
- **Completed:** 2026-09-29T15:10:33Z
- **Tasks:** 3
- **Files modified:** 5

## Accomplishments
- 浏览词库 reached its final shape: search + category pills + collapsible unit groups with Go-computed count badges; collapse state survives reload via `localStorage parent.units.collapsed`; skeleton/empty-library/no-match/load-failure states all render the UI-SPEC copy
- Every mutation endpoint has its consumer: edit (D-08/D-07 semantics), rename (Pitfall 2 surfaced verbatim from the backend), delete word/unit behind destructive confirms whose copy states the exact word/count the API will act on
- 学习设置 card (D-15) live: sliders with hints and 当前/默认 readouts, request-free 恢复默认, clamped 保存设置 → PUT → toast; the D-13 future-only note renders
- Final D-03 layout: single column on mobile, two-column grid (browse 2fr / rail 1fr) at ≥1024px

## Task Commits

Each task was committed atomically:

1. **Task 1: Browse toolbar + unit groups + word rows + edit modal** — `bee92a8` (feat)
2. **Task 2: Rename + destructive-delete confirms wired to endpoints** — `34517f0` (feat)
3. **Task 3: 学习设置 card + final page assembly + responsive two-column grid** — `009f522` (feat)

**Plan metadata:** see the docs commit for SUMMARY/STATE/ROADMAP.

## Files Created/Modified
- `pkg/web/static/js/parent-browse.js` — rebuilt around BrowseView: toolbar (debounced search, ✕ clear, pills), unit groups (aria-expanded toggle + badge, localStorage collapse), word rows (tracer selectors kept), shared Modal helper (focus trap/Esc/inert overlay), edit/rename/delete modals, all four UI states
- `pkg/web/static/js/parent-modes.js` — SettingsCard (sliders, readouts, restore-defaults, save) + slider field builder + range-fill helper
- `pkg/web/static/js/parent-app.js` — BrowseView + SettingsCard wiring (fetchWords/fetchUnits/editWord/renameUnit/deleteWords/settings handlers), onMutated datalist refresh, settings-container mount check
- `pkg/web/static/parent.html` — script comment reflects BrowseView, `?v=20260929` cache-bust, settings-section comment updated (mount unhides)
- `pkg/web/static/styles/parent.css` — toolbar/pills, unit-group header/body, row actions, skeleton shimmer, modal overlay + danger emphasis, destructive buttons, range slider + track fill, empty-state copy, ≥768px padding, ≥1024px two-column grid

## Decisions Made
- Unit-level ops iterate the categories present in the group's rows (the API requires category; a merged 全部 group can span both) — and header 重命名/删除 are withheld in the one ambiguous combination (全部 + active search) so a confirm's "全部 {n} 个单词" copy can never exceed the server scope (T-09-05)
- The 48px group header keeps the aria-expanded toggle as a SIBLING of the action buttons — nested buttons are invalid HTML and break the accessibility tree; 未分组 gets no header actions (no `{category, unit}` target) but keeps collapse and word-level ops
- Edit modal sends all four fields (provided-empty clears the column): it renders the word's full current state, so empty-visible means clear — D-08's only-单词-required validation stays client-side
- Interval slider tops at 365 (plan spec) although the API allows 36500 — nothing above 365 was ever settable from this UI; retention clamped to 0.70–0.95 and rounded to 2 decimals before PUT
- Manual smoke ran mechanically through the Playwright harness (throwaway spec, deleted after, not committed); permanent full-flow e2e coverage belongs to 09-08 (09-06 precedent)

## Deviations from Plan

### Auto-fixed Issues

**1. [Task-boundary adjustment] Unit-header 重命名/删除 and row 删除 buttons rendered + wired together in Task 2 instead of rendered inert in Task 1**
- **Found during:** Task 1 (unit groups)
- **Issue:** Task 1's action text lists the header/row 删除 buttons among the UnitGroup elements, but their behavior is Task 2's scope — rendering them inert in Task 1 would ship a commit with dead destructive buttons (a stub, contradicting the stub-tracking rule)
- **Fix:** Task 1 shipped groups/rows with the 编辑 action only; Task 2 added the 删除/重命名 buttons together with their modals and CSS. Every Task 1 acceptance criterion still passed at its own commit
- **Files modified:** pkg/web/static/js/parent-browse.js, pkg/web/static/styles/parent.css
- **Verification:** Task 1 greps green at `bee92a8`; Task 2 greps + smoke green at `34517f0`
- **Committed in:** bee92a8 + 34517f0

---

**Total deviations:** 1 auto-adjusted (task-boundary ordering; no spec content changed)
**Impact on plan:** None — both tasks' acceptance criteria verified green at their own commits; the adjustment only prevents a stub-bearing intermediate commit.

## Issues Encountered
- The throwaway smoke spec needed two fixes of its own (never the app): a strict-mode locator ambiguity (`.form-error` matches all four edit-modal fields) and a test-logic bug where the stale-rename simulation deleted the whole unit behind the dialog, leaving nothing for the delete steps — the app behaved correctly in both runs, and the final spec passed 3.6s green covering all acceptance criteria.
- `go test -short ./...` ran clean on the first attempt this time (the :8080 e2e-teardown contention pitfall from 09-06 did not materialize — the webServer had already exited before the run).

## User Setup Required

None - no external service configuration required.

## Next Phase Readiness
- 09-08 (收尾 E2E) builds on a stable selector surface: `.unit-group[data-unit]`, `.unit-toggle`, `.unit-count`, `.search-input`, `.search-clear`, `.row-action`, plus the kept `.word-row[data-word]`/`.word-text` tracer hooks
- REQUIREMENTS.md VOC-01/02/03 + WEB-01 intentionally left Pending: 09-08 declares the same IDs (shared-ID gate #2388; ready-ids returned 0/3 this plan) — they flip when 09-08's SUMMARY lands
- The smoke left the environment net-zero: every created word/unit was deleted through the real API inside the test, and FSRS settings were restored to 0.85/180 via PUT
- UI-SPEC's two 🧪 backstop rows (long-text, overflow) are the documented visual-verification debt for 09-08's viewport matrix (coverage D6)

---
*Phase: 09-parent-vocabulary-management*
*Completed: 2026-09-29*

## Self-Check: PASSED

- key-files verified on disk and committed (5/5): parent-browse.js, parent-modes.js, parent-app.js, parent.html, parent.css
- `git log --grep="09-07"` returns the 3 task commits (bee92a8, 34517f0, 009f522)
- All `<acceptance_criteria>` re-run green:
  - Task 1: markup-setter grep over parent-browse.js clean; parent.units.collapsed ×3; aria-expanded ×3; debounced search (setTimeout) ×3; smoke proved collapse-survives-reload, live search narrowing, edit modal round-trip
  - Task 2: 确认删除 ×5; 无法恢复 ×2; smoke proved rename updates the group, delete-word removes exactly one row, delete-unit removes the group, stale rename surfaces the backend not-found text in an Error toast
  - Task 3: 学习设置 ×3 and 已保存，下次听写生效 ×2 in parent-modes.js; 1024px ×3 in parent.css; smoke proved live readouts, request-free 恢复默认, save toast + GET reflection, visible note line, two-column at 1200px and stacked at 800px
- Plan `<verification>` re-run: `npm test` green (all 7 node test files); `go test -short ./...` green (all 14 packages ok); manual smoke of every UI-SPEC Interaction row executed via the Playwright harness — add single/batch paths exercised by the pre-existing tracer + 09-06 flow, search/clear/edit/rename(+not-found)/delete word/delete unit/settings save/restore all green
