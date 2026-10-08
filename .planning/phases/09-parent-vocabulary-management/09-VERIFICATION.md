---
phase: 09-parent-vocabulary-management
verified: 2026-09-30T00:00:00Z
status: human_needed
score: 57/57 must-have truths verified (4 roadmap SCs + 53 plan truths) · 9/9 prohibitions held
behavior_unverified: 0
overrides_applied: 0
human_verification:
  - test: "Review the backstop visual evidence (UI-SPEC 🧪 rows): open test-results/parent-management-*-backstop-visual-checks/ screenshots after a spec re-run — long-text 2-line clamp + unit-name ellipsis (backstop-longtext-desktop.png), 320px-capped batch preview scroll (backstop-preview-overflow.png), per-viewport layouts (matrix-phone/tablet/desktop)."
    expected: "Meanings clamp at 2 lines then ellipsize; unit names single-line ellipsize with full text on hover/focus (title); preview table scrolls internally at 320px; layouts match D-03 at all three viewports."
    why_human: "Automated asserts passed (webkitLineClamp=2, rendered height ≤ 2 line-heights, maxHeight 320px, scrollHeight > clientHeight — e2e green twice, commits 730732d/e904f7a), but UI-SPEC marks these rows 🧪 backstop: visual adequacy of the clamp/ellipsis/scroll UX needs human eyes; screenshots live under gitignored test-results/ (regenerated per run)."
  - test: "Open /parent.html on the real iPad Safari (target device) and run the page visually: add one word (single + batch), browse/search/collapse groups, open edit/rename modals, move sliders."
    expected: "Neutral management look renders correctly, 44px touch targets feel right, no zoom-on-focus (inputs ≥16px verified in Chromium; iOS behavior needs the device), sliders/modals usable with touch."
    why_human: "09-01 coverage D5 routed parent-page visual quality to human judgment ('visual adequacy needs human eyes on an iPad'). The D-03 responsive matrix was verified in Chromium at 3 viewports; the parent page has not yet been opened on real iPad hardware (Phase 8 needed a real-device UAT to close the equivalent item). Phase 12 SC2 will exercise the parent flow on iPad as part of end-to-end validation — this item can be closed there if not closed sooner."
---

# Phase 9 Verification — Goal-Backward Report (2026-09-30)

**Goal (ROADMAP):** Parent interface for adding, editing, and organizing vocabulary with FSRS configuration
**Re-verification:** No — initial verification (no prior 09-VERIFICATION.md)

**Verdict:** ✅ All 57 must-have truths verified against the actual codebase; all 19 artifacts exist, are substantive, and are wired with real data flowing; all 9 prohibitions held with mechanical/test evidence; requirements VOC-01/02/03 + WEB-01 flips to Complete are justified by shipped artifacts. Two narrow visual items route to human (backstop screenshot review + real-iPad pass) — no code gaps found.

## Evidence Layers

| Layer | Result | Source |
|---|---|---|
| `go test -short -count=1 ./...` | 14/14 packages ok (verifier's own run, 2026-09-30) | this session |
| `npm test` | 7/7 node test files pass incl. test-parent-batch-parser.js (verifier's own run) | this session |
| `go test -tags=e2e -run TestE2E_VocabEdit_PreservesFSRS ./pkg/pybridge/ -v` | PASS (0.35s, real venv subprocess — D-07 core promise spot-checked first-hand) | this session |
| Playwright `tests/e2e/parent-management.spec.js` (tracer + full-flow + 3-viewport matrix + backstop) | 3/3 green, run twice back-to-back in the last hour (commits 730732d, e904f7a); per dispatch instructions the verifier cited the runs + verified spec content directly instead of re-running | 09-08-SUMMARY coverage D1-D5; spec read line-by-line |
| pybridge real-Python e2e (12 TestE2E_* tests) | phase gate green 2026-09-29 (09-08 coverage D6); all 12 test names enumerated on disk; 1 re-run PASS this session | 09-08-SUMMARY + this session |
| Live acceptance (31/31 checks, real service + real Python) | green 2026-09-28 (edit/rename/delete/settings round trips incl. on-disk file bytes + subsequent Python spawns consuming the Go-written file) | 09-05-SUMMARY coverage D1-D6 |
| `git log --grep="09-0"` | 26 production commits across 8 plans, all SUMMARY-documented hashes present (c481633…e904f7a) | this session |

## Goal Achievement

### Observable Truths

**ROADMAP Success Criteria (the contract):**

| # | Truth | Status | Evidence |
|---|---|---|---|
| 1 | Parent can add vocabulary words with Chinese translations, pronunciations, and example sentences | ✓ VERIFIED | Add flow live end-to-end: parent.html 添加单词 card → POST /api/v1/words (handlers.go:346) → pybridge.RunAdd (vocab_mgmt.go:321) → vocab_manager.py add → SQLite → CardRepository.List → browse list; proven by tracer + full-flow e2e (green twice). Pronunciation note: per locked decision D-05 (09-CONTEXT.md:51), VOC-01's "pronunciations" is reinterpreted as the Phase 6 TTS read-aloud capability — the parent surface intentionally never collects/stores pronunciation (documented scope calibration, not a silent cut). |
| 2 | Parent can organize words into units and lessons | ✓ VERIFIED | Units are flat strings per D-09: browse renders collapsible unit groups with Go-computed count badges (parent-browse.js, `parent.units.collapsed` localStorage), rename via POST /units/rename, unit-level delete, unit datalist on add forms; "lessons" = unit naming conventions (09-04 prohibition explicitly forbids a server-side hierarchy — held). e2e asserts group badge, rename update, unit deletion. |
| 3 | Parent can configure FSRS parameters (difficulty, retention interval) | ✓ VERIFIED | Exactly two knobs per locked decision D-13 (09-CONTEXT.md:99): 学习设置 card sliders 记忆保持率/最长复习间隔 → PUT /api/v1/settings/fsrs → atomic writeFSRSConfig (fsrs_config.go:95, temp+Sync+Rename) → load_scheduler() reads the same file on every spawn (vocab_manager.py:97-131, Pitfall 8 single-resolution). "Difficulty" in the SC wording is an internal FSRS card state; the exposed configuration surface is the D-13 two-knob calibration (documented). e2e asserts save → reload → GET reflects 0.90, restore to 0.85/180. |
| 4 | Interface persists all changes to SQLite database | ✓ VERIFIED | Python is sole writer (ADR write-separation): all mutations dispatch through vocab_manager.py subprocesses to the cards table; Go reads the SAME file (main.go:223-233 DB default prefers python.vocabDBPath). e2e proves persistence across page reloads (collapse state, settings, edited meanings); live acceptance verified on-disk fsrs_config.json bytes + round trips. |

**Plan-level truths (condensed; every PLAN truth verified — code read + test evidence):**

| Plan | Truths | Status | Key evidence |
|---|---|---|---|
| 09-01 tracer (5) | add e2e without reload; POST/GET contract; 400 validation matrix; argv --db-before-subcommand; sentinel classification | 5/5 ✓ | handlers.go/validate.go/vocab_mgmt.go read; TestAddWords/TestListWords/TestAddArgs/TestCardRepoList green in verifier's `go test -short` run; tracer e2e green; zero pybridge log entries on read path (09-01 coverage D4) |
| 09-02 Python engine (5) | edit subcommand; not-found exit-1; config warn-and-fallback; config values consumed; empty-config defaults | 5/5 ✓ | cmd_edit (vocab_manager.py:688-718, COALESCE, no fsrs_card) + load_scheduler (:97-131) read; 8 TestE2E_* names enumerated; TestE2E_VocabEdit_PreservesFSRS re-run PASS this session |
| 09-03 bridge (5) | Run* methods; --confirm ALWAYS; ExitError.Stdout; rename/delete argv; edit pointer flags | 5/5 ✓ | vocab_mgmt.go:162-474 read (deleteWordsArgs appends --confirm unconditionally :238; classifyMgmtRun populates Stdout :280); TestEditArgs/TestRenameUnitArgs/TestDeleteWordsArgs/TestClassifyMgmtRun green in verifier's run; 4 TestE2E_Run* green per phase gate |
| 09-04 unit reads (4) | GET /units one round-trip; CountByUnit scoping/merge; enum validation; flat-units edge assumption | 4/4 ✓ | card_repo.go:314 CountByUnit + handlers.go:406 listUnits read; 5 TestCountByUnit_* + TestListUnits green in verifier's run; live curl 200/400 cases (09-04 coverage D2) |
| 09-05 REST surface (7) | PUT /words/{id}; rename 404 diagnostics; delete XOR always-confirmed; GET/PUT settings; bounds validation; atomic replace; idempotent fixed-key-order | 7/7 ✓ | handlers.go:152-240 + fsrs_config.go read; 13 handler/fsrs test suites green in verifier's run (incl. TestPutFSRSWritesAtomically failure case); 31/31 live checks (09-05 coverage) |
| 09-06 add UI (8) | parseBatch contract; dual tabs + preview + banner; empty preview; per-line warning rows; partial-form (D-06/D-08, no pronunciation field); loading states; datalist; one-unit-per-batch | 8/8 ✓ | parser is pure + node-tested in npm chain (verifier's npm test run); 48 symbol/copy matches in parent-modes.js; no innerHTML (rg clean); batch smoke + full-flow e2e assert both banner colors with named skipped words |
| 09-07 browse/settings UI (15) | unit groups + collapse persistence; debounced search + pills; edit modal; rename modal; destructive confirms; empty/no-match/skeleton/error states; zero-one-many; loading buttons; 学习设置 card; responsive grid | 15/15 ✓ | parent-browse.js (934 lines, 76 key-symbol matches: `parent.units.collapsed`, aria-expanded, 确认删除×5, 无法恢复×2) + SettingsCard in parent-modes.js read; full-flow e2e asserts collapse-reload/search/edit/rename/delete-word/settings; 09-07 throwaway smoke recorded all interaction rows; grid discriminators asserted in matrix test |
| 09-08 e2e capstone (4) | full flow in one spec; 3-viewport matrix; long-text backstop; overflow backstop | 4/4 ✓ | spec read line-by-line (425 lines): tracer + full-flow with finally API cleanup + viewport loop 375/820/1280 with matchMedia/computed-style table + clamp/overflow asserts; green twice (730732d, e904f7a); consecutive-run idempotence recorded |

**Score:** 57/57 truths verified (4 roadmap SCs + 53 plan truths; 0 failed, 0 behavior-unverified — every behavior-dependent truth has a passing behavioral test from the verifier's own runs or the cited green runs)

### Required Artifacts

All 19 artifact paths from PLAN frontmatter exist and are substantive (line counts verified): pkg/web/api/handlers.go (429), validate.go (260), fsrs_config.go (167), fsrs_config_test.go (269), handlers_test.go (748), pkg/pybridge/vocab_mgmt.go (474), vocab_mgmt_test.go (600), vocab_mgmt_e2e_test.go (602), pkg/database/card_repo.go + card_repo_query_test.go, third_party/python/vocab_manager.py (961), parent.html (88), parent.css (827), parent-app.js (218), parent-modes.js (908), parent-browse.js (934), parent-batch-parser.js (108), tests/e2e/parent-management.spec.js (425), tests/test-parent-batch-parser.js (127).

Two `verify.artifacts` pattern misses are symbol-naming deviations, not gaps:
- 09-07 `contains: "UnitGroup"` — implemented as the `.unit-group` DOM class inside the BrowseView controller (parent-browse.js:563) rather than a class-per-component symbol; all grouping behaviors (aria-expanded toggle, count badge, collapse persistence) are present and e2e-asserted.
- 09-08 `contains: "parent-management"` — the string lives in the filename and commit history, not the file body; the file is the spec itself (425 lines, 3 tests).

### Key Link Verification

`verify.key-links` reported not-found on all links — tool false negatives (prose-style from/to fields are not file paths; Windows regex quirk on `api\.NewHandler`). All 15 links manually verified:

| From → To | Status | Evidence |
|---|---|---|
| main.go → api.NewHandler single mount | ✓ WIRED | main.go:511-512 `publicMux.Handle("/api/v1/", http.StripPrefix("/api/v1", api.NewHandler(pyBridge, cardRepo, cfg.Python, logger)))` — exactly one /api/v1/ mount |
| addWords → RunAdd; editWord → RunEdit; renameUnit → RunRenameUnit; deleteWords → RunDeleteWords | ✓ WIRED | handlers.go:365, 169, 199, 230 |
| parent-app.js fetchJSON → all 8 routes | ✓ WIRED | parent-app.js:158-218 (words GET/POST/PUT/DELETE, units, units/rename, settings/fsrs GET/PUT) |
| load_scheduler → DataDir/fsrs_config.json | ✓ WIRED | vocab_manager.py:99 `os.path.join(DEFAULT_DATA_DIR, "fsrs_config.json")` — same file the Go writer targets (Pitfall 8) |
| dispatch dict → cmd_edit | ✓ WIRED | vocab_manager.py:947 `"edit": cmd_edit` |
| ExitError.Stdout → API not_found mapping | ✓ WIRED | vocab_mgmt.go:280 populates; handlers.go:118-123 consumes (stdoutNotFoundMsg, 300-rune truncation) |
| BatchAddForm commit → POST /words | ✓ WIRED | parent-modes.js commit path → parent-app.js:195 |
| package.json → parser test | ✓ WIRED | package.json:3 `&& node tests/test-parent-batch-parser.js` |
| UnitGroup collapse → localStorage | ✓ WIRED | parent-browse.js:38 `COLLAPSE_STORAGE_KEY = 'parent.units.collapsed'` |
| SettingsCard save → PUT settings | ✓ WIRED | parent-modes.js → parent-app.js:218 |
| e2e spec → real service | ✓ WIRED | spec:43/94/335 `page.goto('/parent.html')` via playwright webServer (run.ps1) |

### Data-Flow Trace (Level 4)

| Artifact | Data Variable | Source | Produces Real Data | Status |
|---|---|---|---|---|
| parent-browse.js BrowseView | word rows / unit groups | GET /api/v1/words + /units → CardRepository SQL on the Python-written SQLite file (main.go:233 default resolution) | Yes — e2e sees real added words | ✓ FLOWING |
| parent-modes.js SettingsCard | slider readouts | GET /settings/fsrs → readFSRSConfig (file or defaults) | Yes — e2e save/reload/GET round trip at 0.90 | ✓ FLOWING |
| handlers.go addWords response | added/skipped | real vocab_manager.py stdout via classifyMgmtRun | Yes — duplicate-skip banner deterministic in e2e | ✓ FLOWING |

No static/hollow data paths found.

### Behavioral Spot-Checks

| Behavior | Command | Result | Status |
|---|---|---|---|
| Full Go suite | `go test -short -count=1 ./...` | 14/14 ok | ✓ PASS |
| Full node chain | `npm test` | 7/7 files pass | ✓ PASS |
| D-07 edit preserves FSRS bytes (real subprocess) | `go test -tags=e2e -run TestE2E_VocabEdit_PreservesFSRS ./pkg/pybridge/ -v` | PASS 0.35s | ✓ PASS |
| Playwright parent spec (cited, not re-run per dispatch) | `npx playwright test tests/e2e/parent-management.spec.js` | 3/3 ×2 consecutive runs (730732d, e904f7a) | ✓ PASS |
| fsrs_config.json on-disk state | file read | `{"desired_retention":0.85,"maximum_interval":180}` — exactly two keys, fixed order, restored by e2e as designed | ✓ PASS |

### Requirements Coverage

| Requirement | Source Plans | Status | Evidence |
|---|---|---|---|
| VOC-01 (add/edit words) | 09-01, 02, 03, 05, 06, 08 | ✓ SATISFIED | Full add/edit surface e2e-proven; pronunciation per D-05 = TTS read-aloud (Phase 6, already Complete); duplicate-skip transparency mechanical (banner + Warning color + named words) |
| VOC-02 (organize units/lessons) | 09-03, 04, 05, 07, 08 | ✓ SATISFIED | Unit groups/rename/delete + Go-side counts; flat-strings per D-09 (hierarchy prohibited by design); shared-unit-name and empty-unit edges pinned by TestCountByUnit_* |
| VOC-03 (configure FSRS) | 09-02, 05, 07, 08 | ✓ SATISFIED | Two-knob D-13 contract end-to-end: UI sliders → atomic Go writer → Python reader consumed on next spawn (live-proven); invalid config can never break the student path (warn-and-fallback, 4 e2e tests) |
| WEB-01 (parent web interface) | 09-01, 06, 07, 08 | ✓ SATISFIED | Complete interface consuming all 8 routes; every UI-SPEC Interaction row covered by e2e (12 implemented rows + 2 backstop rows with automated asserts + screenshots); URL-only access per D-4 |

REQUIREMENTS.md flips verified honest: VOC-01/02/03 + WEB-01 marked [x] in the checklist AND "Complete" in the traceability table (lines 135-137, 179). No orphaned requirements mapped to Phase 9.

### Prohibitions (9/9 held — mechanical/test evidence, not bare judgment)

| Plan | Prohibition | Status | Evidence |
|---|---|---|---|
| 09-01 | No link/nav from student page to parent page (D-04) | ✓ HELD | `rg parent pkg/web/static/index.html` → 0 matches; index.html untouched by phase commits |
| 09-02 | edit MUST NOT touch fsrs_card | ✓ HELD | cmd_edit (vocab_manager.py:688-718) references only word/meaning/example/unit; TestE2E_VocabEdit_PreservesFSRS PASS (verifier re-run) |
| 09-02 | Config change MUST NOT rewrite card states | ✓ HELD | load_scheduler only constructs the Scheduler; no UPDATE on the config path |
| 09-04 | No server-side unit→lesson hierarchy | ✓ HELD | No schema changes in pkg/database (cards table unchanged; grouping is presentation-only) |
| 09-04 | Reads MUST NOT spawn Python | ✓ HELD | `rg os/exec pkg/database/` → 0 matches; live GETs produced 0 pybridge log entries (09-04 coverage D3) |
| 09-05 | No merge-units REST route | ✓ HELD | handlers.go registers exactly 8 routes; "merge" appears only in a merge-by-name comment |
| 09-05 | Only two FSRS knobs accepted | ✓ HELD | FSRSSettings struct + decodeJSON DisallowUnknownFields; TestPutFSRSValidation asserts extra field → 400 |
| 09-05 | Delete always confirmed (server gate) | ✓ HELD | deleteWordsArgs appends --confirm unconditionally (vocab_mgmt.go:238), asserted in every test table row; handler performs no preview logic |
| 09-06 | Batch import never reports plain success on skips | ✓ HELD | Banner always renders both counts; Warning color + named skipped words when b>0; e2e asserts 已添加 2 个，跳过 1 个重复单词 |

### Anti-Patterns Found

None. Zero TBD/FIXME/XXX/TODO/HACK/PLACEHOLDER markers across all phase-modified files; no innerHTML/insertAdjacentHTML/document.write in the four parent JS files (Pitfall 10 held); no stub returns; no console.log-only handlers.

### Human Verification Required

See frontmatter `human_verification` — 2 items:
1. **Backstop visual review** — screenshots under test-results/ (regenerated per run); automated clamp/ellipsis/overflow asserts already passed twice.
2. **Parent page on real iPad Safari** — visual quality + touch feel on target hardware (09-01 D5 deferral); Chromium 3-viewport matrix already green; can be closed at Phase 12 SC2 (parent flow on iPad) if not sooner.

### Gaps Summary

No code gaps. All must-haves verified; no stubs, no unwired links, no debt markers, no orphaned requirements. Residuals (non-blocking):

| # | Residual | Severity | Disposition |
|---|---|---|---|
| 1 | Stale dev data: 3 rows in unit `e2e-0907-unit-1790694195008` (english) left in local .hta-data/vocab.db by a prior session's throwaway smoke (pre-09-08 scope) | Low (dev data only) | Flagged in 09-08-SUMMARY Issues; one-click manual removal via the parent UI (delete unit), or ignore — does not affect any test (current e2e units e2e-tracer/e2e-full/e2e-full-2/e2e-backstop all confirmed cleaned by this verifier's DB read) |
| 2 | Backstop screenshots live under gitignored test-results/ | Info | Durable paths recorded in 09-08 coverage D4/D5; a spec re-run regenerates them |
| 3 | 09-06/09-07 manual smokes were executed via temporary (deleted) Playwright specs | Info | Compensated: the permanent 09-08 full-flow spec covers the same behaviors and is committed; all smoke assertions re-verified in the permanent spec's green runs |

**Status rationale:** human_needed per the decision tree (rule 2): every truth is VERIFIED with test evidence, but two visual-adequacy items require human judgment (Step 8: visual appearance always needs human). No gaps_found triggers (rule 1 clean). The 2 items are narrow, evidence-backed, and have a named closure path (screenshot review now; iPad pass now or at Phase 12).

---

_Verified: 2026-09-30_
_Verifier: gsd-verifier (local general lane stand-in)_
