---
phase: 09-parent-vocabulary-management
plan: 01
subsystem: api
tags: [rest, pybridge, sqlite, playwright, vanilla-js, parent-page]

# Dependency graph
requires:
  - phase: 03-python-cli-integration
    provides: pybridge Bridge/Executor (run engine, sentinels, extractTrailingJSON) and vocab_manager.py add subcommand
  - phase: 04-database-layer
    provides: CardRepository (sqliteCardRepository, cardColumns, scanCard) and multi-process WAL Open
  - phase: 08-student-web-interface
    provides: StaticFileServer static serving, IIFE window.X module pattern, Playwright e2e harness with run.ps1 webServer
provides:
  - POST/GET /api/v1/words REST surface mounted once in main.go (pkg/web/api NewHandler, {"error":{code,message}} envelope)
  - pybridge Executor.RunAdd + addArgs argv contract for management commands (list-mode JSON, inline-vs-temp-file)
  - PythonConfig.MgmtTimeout shared by all Phase 9 management commands
  - CardRepository.List (CardFilter: category/unit/LIKE-escaped q, ORDER BY unit,id)
  - parent.html page skeleton + parent.css design tokens + parent-app/parent-modes/parent-browse JS modules (SingleAddForm, WordList, fetchJSON)
  - Playwright tracer e2e (tests/e2e/parent-management.spec.js, -g tracer)
affects: [09-02, 09-03, 09-04, 09-05, 09-06, 09-07, 09-08]

# Actuals (#2632)
actuals:
  tokens: 28000
  tasks: 2
  commits: 3

# Tech tracking
tech-stack:
  added: []
  patterns:
    - "Go 1.22 method-pattern routes inside a package-internal mux, mounted once via http.StripPrefix at /api/v1/"
    - "REST handler JSON encode/decode pattern (Content-Type before WriteHeader; MaxBytesReader + DisallowUnknownFields)"
    - "argv contract for vocab_manager.py management subcommands (--db BEFORE subcommand, hyphenated tokens)"
    - "parent-page IIFE module pattern mirroring the student page (window.X globals, availability checks, defer order, ?v= cache busting)"

key-files:
  created:
    - pkg/pybridge/vocab_mgmt.go
    - pkg/web/api/handlers.go
    - pkg/web/api/validate.go
    - pkg/web/static/parent.html
    - pkg/web/static/styles/parent.css
    - pkg/web/static/js/parent-app.js
    - pkg/web/static/js/parent-modes.js
    - pkg/web/static/js/parent-browse.js
    - tests/e2e/parent-management.spec.js
    - pkg/pybridge/vocab_mgmt_test.go
    - pkg/web/api/handlers_test.go
    - pkg/database/card_repo_query_test.go
  modified:
    - pkg/pybridge/executor.go
    - pkg/pybridge/bridge.go
    - pkg/pybridge/bridge_test.go
    - pkg/pybridge/pregenerate_test.go
    - pkg/infra/config.go
    - config.example.yaml
    - pkg/database/card_repo.go
    - cmd/service/main.go
    - pkg/fsrs/service_test.go
    - pkg/tts/generator_test.go

key-decisions:
  - "Go default DB path now prefers python.vocabDBPath when database.path is empty — CardRepository must read the Python-owned vocab DB (D-12) or the read path sees 'no such table: cards'"
  - "addArgs always marshals list-mode JSON entries so example survives; dict mode would drop it (Pitfall 4)"
  - "MgmtTimeout is ONE shared timeout for all Phase 9 management commands (add/edit/rename/delete), mirroring DueTimeout exactly"
  - "Sentinel mapping in addWords: four pybridge sentinels -> 502 python_failed; anything else -> 500 internal"

patterns-established:
  - "Pattern: new management bridge methods copy vocab.go dueArgs/RunDue shape verbatim (argv builder + sentinel classification)"
  - "Pattern: pkg/web/api owns all /api/v1 route logic; main.go only mounts /api/v1/ once"
  - "Pattern: error envelope codes locked: invalid_request / python_failed / internal"

requirements-completed: [VOC-01, WEB-01]

coverage:
  - id: D1
    description: "Parent adds one word from /parent.html (fill 单词, select 英语, set 单元, click 添加单词) and the word appears in the 浏览词库 list without reload — against the real service (run.ps1 → Go → Python venv → SQLite)"
    requirement: VOC-01
    verification:
      - kind: e2e
        ref: "tests/e2e/parent-management.spec.js#tracer: parent adds one word from parent.html and sees it in the browse list"
        status: pass
    human_judgment: false
  - id: D2
    description: "POST /api/v1/words valid body returns 200 {\"added\":N,\"skipped\":N}; invalid category/entries/unknown-field bodies return 400 invalid_request; GET /api/v1/words?category= returns snake_case words from CardRepository"
    requirement: WEB-01
    verification:
      - kind: integration
        ref: "curl -X POST localhost:8080/api/v1/words (chinese/e2e-tracer) → {\"added\":1,\"skipped\":0}; GET ?category=chinese contains the word; missing/unknown category → 400 invalid_request"
        status: pass
      - kind: unit
        ref: "pkg/web/api/handlers_test.go#TestAddWords"
        status: pass
      - kind: unit
        ref: "pkg/web/api/handlers_test.go#TestListWords"
        status: pass
    human_judgment: false
  - id: D3
    description: "addArgs argv contract: [VocabScript, --db, VocabDBPath, add, --category ...] with --db BEFORE the subcommand, --unit omit-when-empty, list-mode JSON payload, inline-vs-temp-file selection, cleanup removes temp file"
    requirement: VOC-01
    verification:
      - kind: unit
        ref: "pkg/pybridge/vocab_mgmt_test.go#TestAddArgs"
        status: pass
    human_judgment: false
  - id: D4
    description: "CardRepository.List (category/unit/q with LIKE escaping, ORDER BY unit,id) reads SQLite with zero Python subprocess spawns on the read path"
    requirement: WEB-01
    verification:
      - kind: unit
        ref: "pkg/database/card_repo_query_test.go#TestCardRepoList"
        status: pass
      - kind: integration
        ref: "server.log grep '\"component\":\"pybridge\"' = 0 entries during GET /api/v1/words"
        status: pass
    human_judgment: false
  - id: D5
    description: "Parent page visual quality per UI-SPEC (neutral palette, 44px touch targets, typography roles) — structure/CSS tokens are in place and wired, but visual adequacy needs human eyes on an iPad"
    verification: []
    human_judgment: true
    rationale: "No automated assertion covers visual adequacy; the tracer e2e proves behavior only. Route to human UAT for the look-and-feel on the target device."

# Metrics
duration: 39min
completed: 2026-09-27
status: complete
---

# Phase 9 Plan 01: Tracer — single-word add end-to-end Summary

**One word flows from parent.html through POST /api/v1/words → api validation → pybridge.RunAdd → vocab_manager.py add → SQLite → CardRepository.List → GET /api/v1/words → browse list render, proven by a passing Playwright tracer test against the real service.**

## Performance

- **Duration:** 39 min
- **Started:** 2026-09-27T09:19:06Z
- **Completed:** 2026-09-27T09:58:16Z
- **Tasks:** 2
- **Files modified:** 22 (12 created, 10 modified)

## Accomplishments
- The minimum parent-management feedback loop works end-to-end: add from the page → word visible in the browse list without reload (Playwright tracer green against the real Go + Python + SQLite stack)
- REST skeleton locked in: pkg/web/api with Go 1.22 method-pattern routes, 64KiB MaxBytesReader + DisallowUnknownFields decoding, category enum validation, entry/rune caps, and the {"error":{code,message}} envelope (invalid_request / python_failed / internal) mounted ONCE at /api/v1/ in main.go
- Bridge contract extended additively: Executor.RunAdd + AddEntry/AddOpts/AddResult with argv built per the --db-before-subcommand contract, list-mode JSON payload, inline-vs-temp-file selection, and the four-sentinel classification copied from RunDue
- CardRepository.List (CardFilter) gives Go-side reads with LIKE escaping and (unit,id)-stable ordering — zero Python spawns on the read path (verified: no pybridge log entries)
- Parent page modules follow the student-page IIFE pattern (parent-app/parent-modes/parent-browse), XSS-safe createElement/textContent rendering, D-06 category required with 请选择分类, D-08 only 单词 required, category/unit retained after success

## Task Commits

Each task was committed atomically:

1. **Task 1: End-to-end tracer — add one word through every layer** - `c481633` (feat)
2. **Task 2: Unit tests for the tracer surface — addArgs, api handlers, CardRepository.List** - `83f90b9` (test)

## Files Created/Modified
- `pkg/pybridge/vocab_mgmt.go` - addArgs + Bridge.RunAdd (management command path)
- `pkg/pybridge/executor.go` - Executor.RunAdd + AddEntry/AddOpts/AddResult contract
- `pkg/pybridge/bridge.go` - NewBridge MgmtTimeout defense-in-depth validation
- `pkg/infra/config.go` - PythonConfig.MgmtTimeout (field/default/validation/env binding)
- `config.example.yaml` - mgmtTimeout documented
- `pkg/database/card_repo.go` - CardFilter + List (additive; ListByUnit untouched)
- `pkg/web/api/handlers.go` - NewHandler, GET/POST /words, writeError envelope
- `pkg/web/api/validate.go` - ValidateCategory, decodeJSON (MaxBytesReader+DisallowUnknownFields), entry caps
- `cmd/service/main.go` - single /api/v1/ mount; cardRepo placeholder deleted; DB default prefers python.vocabDBPath
- `pkg/web/static/parent.html` - zh-CN parent page skeleton (settings section hidden until 09-07)
- `pkg/web/static/styles/parent.css` - neutral design tokens per UI-SPEC (no kid theming)
- `pkg/web/static/js/parent-app.js` - fetchJSON wrapper + component wiring + refresh hook
- `pkg/web/static/js/parent-modes.js` - SingleAddForm (category radios, validation copy, toasts)
- `pkg/web/static/js/parent-browse.js` - WordList (flat rows, badges, refresh, error/empty states)
- `tests/e2e/parent-management.spec.js` - Playwright tracer test (-g tracer)
- `pkg/pybridge/vocab_mgmt_test.go` - TestAddArgs (argv contract + payload thresholds)
- `pkg/web/api/handlers_test.go` - TestAddWords / TestListWords (manual fakes, D-48)
- `pkg/database/card_repo_query_test.go` - TestCardRepoList (Pitfall 6/7 fixtures)
- `pkg/fsrs/service_test.go`, `pkg/tts/generator_test.go` - stub RunAdd on Executor fakes (compile)
- `pkg/pybridge/bridge_test.go`, `pkg/pybridge/pregenerate_test.go` - MgmtTimeout in test fixtures + validation case

## Decisions Made
- Go default DB path resolution now prefers `python.vocabDBPath` when `database.path` is empty: CardRepository must read the SAME file Python writes (multi-process WAL was already the design intent in database.Open). Without this the read path 500s with "no such table: cards" because the legacy `$HOME/.home-tutor-agent/vocab.db` default is a different file.
- `MgmtTimeout` is one shared timeout for ALL Phase 9 management commands rather than per-command timeouts (add/edit/rename/delete are the same in-process SQLite profile).
- addWords maps the four pybridge sentinels to 502 python_failed and everything else to 500 internal; no raw stderr reaches the client (T-09-07).
- SingleAddForm retains category + unit after a successful add (rapid consecutive entry, UI-SPEC auto-decided) and clears only word/meaning/example.

## Deviations from Plan

### Auto-fixed Issues

**1. [Rule 3 - Blocking] Go default DB path pointed the read path at the wrong file**
- **Found during:** Task 1 (tracer e2e verification)
- **Issue:** With no `database:` block in config, main.go defaulted to `$HOME/.home-tutor-agent/vocab.db` while Python writes cards to `python.vocabDBPath` (`./.hta-data/vocab.db`); GET /api/v1/words failed with `no such table: cards` — the plan's acceptance criteria (GET contains the added word) were unreachable.
- **Fix:** Default resolution prefers `cfg.Python.VocabDBPath`, falling back to the legacy default only when both are empty. Matches the existing multi-process WAL design intent in database.Open.
- **Files modified:** cmd/service/main.go
- **Verification:** Tracer e2e green; GET returns the added word; zero pybridge spawns on reads
- **Committed in:** c481633 (Task 1 commit)

**2. [Rule 1 - Bug] e2e locators ambiguous / wrong ARIA role**
- **Found during:** Task 1 (tracer e2e run)
- **Issue:** `getByLabel('单词')` matched both the section title (添加单词 contains 单词) and the input; the 单元 input with a datalist association has ARIA role combobox, not textbox.
- **Fix:** Exact-match role queries (`getByRole('textbox', { name: '单词', exact: true })`, `getByRole('combobox', ...)`).
- **Files modified:** tests/e2e/parent-management.spec.js
- **Verification:** tracer test passes deterministically
- **Committed in:** c481633 (Task 1 commit)

**3. [Rule 3 - Blocking] PythonConfig test fixtures lacked MgmtTimeout**
- **Found during:** Task 1 (go test after config change)
- **Issue:** NewBridge's new MgmtTimeout > 0 check (mirroring DueTimeout defense-in-depth) rejected the hand-built PythonConfig literals in bridge_test.go/pregenerate_test.go.
- **Fix:** Added MgmtTimeout to validTestCfg/validPregenCfg and a "mgmtTimeout zero" validation case.
- **Files modified:** pkg/pybridge/bridge_test.go, pkg/pybridge/pregenerate_test.go
- **Verification:** go test -short ./pkg/pybridge/ green
- **Committed in:** c481633 (Task 1 commit)

---

**Total deviations:** 3 auto-fixed (1 blocking environment/wiring, 2 test-surface corrections)
**Impact on plan:** All fixes were required for the plan's own acceptance criteria to hold. No scope creep; D-04 held (index.html untouched).

## Issues Encountered
- Playwright tears down its spawned webServer after a run, so the curl acceptance checks required restarting the service via run.ps1 (health probe lives on :8081, service on :8080) — operational note, not a defect.
- Task 2 is flagged `tdd="true"` but the plan's tracer-first structure places implementation (Task 1) before unit tests (Task 2), so no RED commit precedes the feat commit; the plan itself dictates this order and both tasks carry their own verifications.

## User Setup Required

None - no external service configuration required.

## Next Phase Readiness
- The wire contract expansion plans 09-02..09-08 reuse is in place: /api/v1 single mount (add routes inside pkg/web/api), sentinel→envelope mapping, addArgs pattern for edit/rename-unit/delete-words, parent-page module pattern, and CardFilter for browse/grouping
- The e2e tracer leaves rows in unit `e2e-tracer`; Plan 09-08's cleanup pass should delete them once the delete endpoint ships
- UAT note: D5 (visual adequacy of the parent page) is routed to human judgment in the coverage block

---
*Phase: 09-parent-vocabulary-management*
*Completed: 2026-09-27*

## Self-Check: PASSED

- key-files.created verified on disk (12/12 present, all committed)
- `git log --grep="09-01"` returns 2 task commits (c481633, 83f90b9)
- All `<acceptance_criteria>` re-run green:
  - `go build ./...` compiles with extended Executor + CardRepository interfaces and both updated fakes — PASS
  - `npx playwright test tests/e2e/parent-management.spec.js -g tracer` — PASS (word added via page form visible in browse list)
  - POST chinese/e2e-tracer → `{"added":1,"skipped":0}`; GET ?category=chinese contains the word; POST missing category → 400 invalid_request — PASS
  - Read path issues no Python subprocess (0 pybridge log entries during GET) — PASS
- Plan `<verification>` re-run: tracer e2e green vs real service; `go test -short ./...` green (all 16 packages ok)
