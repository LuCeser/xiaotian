---
phase: 09-parent-vocabulary-management
plan: 03
subsystem: api
tags: [pybridge, argv, subprocess, sentinel-errors, e2e, vocab-manager, go]

# Dependency graph
requires:
  - phase: 09-parent-vocabulary-management
    provides: 09-01 tracer surface (addArgs/RunAdd shape, MgmtTimeout, Executor seam) and 09-02 Python-side CLI contract (edit flags, rename-unit/delete-words hyphenated subcommands, {"edited"/"renamed"/"deleted"} JSON keys)
provides:
  - pybridge Executor.RunEdit/RunRenameUnit/RunDeleteWords + EditOpts/EditResult, RenameUnitOpts/RenameUnitResult, DeleteWordsOpts/DeleteWordsResult (json tags verbatim per vocab_manager.py)
  - editArgs/renameUnitArgs/deleteWordsArgs pure argv builders (--db BEFORE hyphenated subcommand; pointer-driven edit flags; exactly-one-target delete validation)
  - Pitfall 1 closed structurally: delete argv carries --confirm unconditionally — no bridge code path can trigger the script's non-JSON dry-run
  - Pitfall 2 closed: ExitError.Stdout carries captured stdout on exit-1 so "No cards found" diagnostics survive to the API layer
  - classifyMgmtRun shared pure sentinel classifier (timeout/exit/empty/parse) for all four management commands
affects: [09-05, 09-07]

# Actuals (#2632)
actuals:
  tokens: 12600
  tasks: 2
  commits: 4

# Tech tracking
tech-stack:
  added: []
  patterns:
    - "Management Run methods: pre-validate -> pure argv builder -> b.run -> logResult -> classifyMgmtRun -> typed unmarshal (one shared classifier, classifyPregenResult precedent)"
    - "Destructive-command argv guard: safety flags (--confirm) appended structurally inside the builder, asserted in every table row"
    - "None-vs-empty over the wire: *string fields — nil omits the flag, non-nil (even empty) emits it, mapping cmd_edit's COALESCE semantics"

key-files:
  created: []
  modified:
    - pkg/pybridge/vocab_mgmt.go
    - pkg/pybridge/executor.go
    - pkg/pybridge/vocab_mgmt_test.go
    - pkg/pybridge/vocab_mgmt_e2e_test.go
    - pkg/fsrs/service_test.go
    - pkg/tts/generator_test.go
    - pkg/web/api/handlers_test.go

key-decisions:
  - "ExitError gained a Stdout field (additive, Unwrap unchanged) instead of mangling Error() formatting — log-scrape format stays stable; diagnostics ride structured fields"
  - "--confirm is appended inside deleteWordsArgs, not by callers — a structural guarantee that no Go code path can produce a dry-run argv (Pitfall 1)"
  - "RunDeleteWords pre-validates Category non-empty like RunAdd — an explicit empty --category would override the script's 'english' default and silently match nothing, a misleading success for a destructive op"
  - "All four management Run methods (incl. RunAdd) classify through the shared classifyMgmtRun — one classifier, identical sentinel behavior across the management surface"

patterns-established:
  - "Pattern: classifyMgmtRun(cmd, stdout, stderr, exitCode, runErr) (json.RawMessage, error) — pure post-run classifier table-testable without a subprocess"
  - "Pattern: bridge e2e tests drive Bridge methods through the real interpreter stack, read back via the script's own list subcommand on the same DB file"

requirements-completed: [VOC-01, VOC-02]

coverage:
  - id: D1
    description: "argv builders for edit/rename-unit/delete-words copy the dueArgs shape: [VocabScript, --db, VocabDBPath, <hyphenated subcommand>, ...] with --id decimal + pointer-driven flags for edit (nil omits, empty clears), three always-flags for rename-unit, exactly-one-target validation for delete"
    requirement: VOC-01
    verification:
      - kind: unit
        ref: "pkg/pybridge/vocab_mgmt_test.go#TestEditArgs"
        status: pass
      - kind: unit
        ref: "pkg/pybridge/vocab_mgmt_test.go#TestRenameUnitArgs"
        status: pass
      - kind: unit
        ref: "pkg/pybridge/vocab_mgmt_test.go#TestDeleteWordsArgs"
        status: pass
    human_judgment: false
  - id: D2
    description: "Pitfall 1 closed: every delete argv carries --confirm — asserted in every table row; the e2e round-trip returning typed Deleted counts proves no dry-run path exists (a dry-run would surface as ErrPythonEmpty/ErrPythonJSONParse)"
    requirement: VOC-02
    verification:
      - kind: unit
        ref: "pkg/pybridge/vocab_mgmt_test.go#TestDeleteWordsArgs/words_mode_joins_with_commas_and_always_confirms"
        status: pass
      - kind: unit
        ref: "pkg/pybridge/vocab_mgmt_test.go#TestDeleteWordsArgs/unit_mode_targets_the_unit_and_always_confirms"
        status: pass
      - kind: integration
        ref: "pkg/pybridge/vocab_mgmt_e2e_test.go#TestE2E_RunDeleteWords"
        status: pass
    human_judgment: false
  - id: D3
    description: "Executor seam grown to five management methods with typed Opts/Result structs; Bridge compile-time check (var _ Executor) and all three test fakes (fsrs/tts/web-api) compile against it"
    requirement: VOC-01
    verification:
      - kind: other
        ref: "command: go build ./... (green with grown Executor interface)"
        status: pass
    human_judgment: false
  - id: D4
    description: "Pitfall 2 closed: nonzero exits produce *ExitError wrapping ErrPythonExit with Stdout carrying the captured stdout — rename-unit's stdout 'No cards found' diagnostic survives to the API layer; all four sentinel classes table-tested"
    requirement: VOC-01
    verification:
      - kind: unit
        ref: "pkg/pybridge/vocab_mgmt_test.go#TestClassifyMgmtRun"
        status: pass
      - kind: integration
        ref: "pkg/pybridge/vocab_mgmt_e2e_test.go#TestE2E_RunRenameUnitNotFound"
        status: pass
    human_judgment: false
  - id: D5
    description: "Real-Python bridge round-trips: RunAdd->RunEdit word-only edit leaves other columns unchanged; RunRenameUnit renames 2 rows {renamed:2, from, to}; RunDeleteWords deletes 2 by list then the rest by unit, leaving exactly the expected rows"
    requirement: VOC-02
    verification:
      - kind: integration
        ref: "pkg/pybridge/vocab_mgmt_e2e_test.go#TestE2E_RunEditRoundTrip"
        status: pass
      - kind: integration
        ref: "pkg/pybridge/vocab_mgmt_e2e_test.go#TestE2E_RunRenameUnit"
        status: pass
      - kind: integration
        ref: "command: go test -tags=e2e -count=1 ./pkg/pybridge/ (full py-e2e suite, ok 5.8s)"
        status: pass
    human_judgment: false

# Metrics
duration: 26min
completed: 2026-09-27
status: complete
---

# Phase 9 Plan 03: Bridge expansion — RunEdit / RunRenameUnit / RunDeleteWords Summary

**Three new management seams on the pybridge Executor (edit/rename-unit/delete-words) with pure argv builders, a structurally-guaranteed --confirm, and ExitError.Stdout stdout diagnostics — proven by unit tables plus four real-Python e2e round-trips through the Bridge.**

## Performance

- **Duration:** 26 min
- **Started:** 2026-09-27T10:43:01Z
- **Completed:** 2026-09-27T11:09:22Z
- **Tasks:** 2
- **Files modified:** 7

## Accomplishments
- Executor interface now carries all five management methods (RunAdd + RunEdit/RunRenameUnit/RunDeleteWords) with typed Opts/Result structs whose json tags mirror vocab_manager.py verbatim; the Bridge compile-time check and every test fake compile against the grown seam
- argv contracts locked by table tests: --db BEFORE the hyphenated subcommand tokens, edit emits --id plus one flag per non-nil *string (nil omits / provided-empty clears — the Plan 09-02 COALESCE contract), rename-unit always carries the three flags, delete-words validates exactly one target before any argv exists
- Pitfall 1 closed structurally: deleteWordsArgs appends --confirm unconditionally — the Go layer cannot produce a dry-run argv, and the e2e delete round-trip returning typed counts proves it
- Pitfall 2 closed: ExitError.Stdout (additive field) carries captured stdout on exit-1; TestE2E_RunRenameUnitNotFound proves the 'No cards found' diagnostic survives to the API layer
- classifyMgmtRun extracted as the shared pure classifier (timeout/exit/empty/parse) for all four management commands, following the classifyPregenResult precedent

## Task Commits

Each task was committed atomically (TDD RED → GREEN per task):

1. **Task 1: argv builders — RED** - `14fee5a` (test: 3 failing suites, undefined EditOpts/editArgs/renameUnitArgs/deleteWordsArgs)
2. **Task 1: argv builders — GREEN** - `35cfe60` (feat: builders + Executor contract growth + Bridge methods + fakes)
3. **Task 2: Run methods + ExitError.Stdout — RED** - `1265d34` (test: failing classification table + 4 bridge e2e suites, undefined classifyMgmtRun/ExitError.Stdout)
4. **Task 2: Run methods + ExitError.Stdout — GREEN** - `1990191` (feat: shared classifier + Stdout field, methods refactored onto it)

## TDD Gate Compliance

| Task | RED commit | GREEN commit | Sequence valid |
|------|-----------|--------------|----------------|
| 1 (argv builders) | 14fee5a | 35cfe60 | ✓ (RED failed to compile: builders/opts undefined) |
| 2 (classifier + Stdout + e2e) | 1265d34 | 1990191 | ✓ (RED failed to compile: classifyMgmtRun/Stdout undefined) |

## Files Created/Modified
- `pkg/pybridge/vocab_mgmt.go` - editArgs/renameUnitArgs/deleteWordsArgs builders + RunEdit/RunRenameUnit/RunDeleteWords + shared classifyMgmtRun (RunAdd switched onto it)
- `pkg/pybridge/executor.go` - Edit/RenameUnit/DeleteWords Opts+Result types, Executor interface growth, ExitError.Stdout field
- `pkg/pybridge/vocab_mgmt_test.go` - TestEditArgs/TestRenameUnitArgs/TestDeleteWordsArgs tables + TestClassifyMgmtRun sentinel table + payload-absence helpers
- `pkg/pybridge/vocab_mgmt_e2e_test.go` - TestE2E_RunEditRoundTrip / TestE2E_RunRenameUnit / TestE2E_RunRenameUnitNotFound / TestE2E_RunDeleteWords (bridge-driven, real interpreter)
- `pkg/fsrs/service_test.go`, `pkg/tts/generator_test.go` - three zero-value stub methods on the Executor fakes
- `pkg/web/api/handlers_test.go` - same stub update on the api package's fake (see Deviation 1)

## Decisions Made
- ExitError gained a Stdout field rather than changing Error() formatting: the `python command "<cmd>" exited <code>: <stderr>` format is documented as stable for log scrapers, so diagnostics ride structured fields instead.
- --confirm lives inside deleteWordsArgs, not at call sites — safety flags on destructive commands must be structural, not caller discipline; every table row asserts it.
- RunDeleteWords pre-validates Category non-empty (mirroring RunAdd): an explicit empty --category would override the script's "english" argparse default and silently match nothing — a misleading "deleted 0" success for a destructive operation.
- classifyMgmtRun returns (json.RawMessage, error) so the Warning-tolerant trailing-JSON extraction is part of the table-tested classification; each Run method keeps only its typed unmarshal.
- All four management methods (including pre-existing RunAdd) classify through the one shared classifier — behavior-preserving consolidation that keeps sentinel semantics identical across the file.

## Deviations from Plan

### Auto-fixed Issues

**1. [Rule 3 - Blocking] Third Executor fake in pkg/web/api/handlers_test.go not listed in the plan**
- **Found during:** Task 1 (GREEN compile)
- **Issue:** The plan's files_modified lists only the fsrs/tts fakes, but Plan 09-01 introduced a third pybridge.Executor manual fake in pkg/web/api/handlers_test.go; growing the interface breaks its compilation.
- **Fix:** Added the same three zero-value stub methods to that fake.
- **Files modified:** pkg/web/api/handlers_test.go
- **Verification:** go build ./... and go test ./pkg/web/api/ green
- **Committed in:** 35cfe60 (Task 1 commit)

**2. [Rule 3 - Blocking] Bridge Run-method bodies landed in Task 1's commit instead of Task 2's**
- **Found during:** Task 1 (GREEN compile)
- **Issue:** Growing Executor in Task 1 breaks `var _ Executor = (*Bridge)(nil)` (bridge.go:45) — the package cannot compile until Bridge implements the new methods, yet Task 1's acceptance criteria requires `go build ./...` green with the grown interface. The plan's task split placed the interface growth (Task 1) and the method implementations (Task 2) in different commits, which cannot both hold.
- **Fix:** Task 1 GREEN delivered the three Run methods with the inline RunAdd-style classification (pre-validations included); Task 2 GREEN then delivered the ExitError.Stdout hardening, the classifyMgmtRun extraction, and all test/e2e proofs on top. Task order and each task's acceptance criteria were preserved.
- **Files modified:** pkg/pybridge/vocab_mgmt.go
- **Verification:** Task 1 commit builds green; Task 2 verification commands green
- **Committed in:** 35cfe60 (methods) + 1990191 (hardening)

**3. [Rule 1 - Bug] RunDeleteWords lacked a category guard**
- **Found during:** Task 1 (method implementation)
- **Issue:** The plan says RunDeleteWords "delegates target validation to deleteWordsArgs" — but nothing guarded an empty Category, and deleteWordsArgs always emits --category. An explicit empty value overrides the script's "english" argparse default, silently matches zero rows, and reports a false-success "deleted 0" for a destructive operation.
- **Fix:** Pre-validate Category non-empty in RunDeleteWords, matching RunAdd's existing guard.
- **Files modified:** pkg/pybridge/vocab_mgmt.go
- **Verification:** e2e delete round-trip green; guard exercised via RunAdd precedent
- **Committed in:** 35cfe60 (Task 1 commit)

**4. [Rule 1 - Bug] Sentinel table tests required a pure classifier the plan didn't name**
- **Found during:** Task 2 (RED authoring)
- **Issue:** The behavior block wants table-driven sentinel tests without faking b.run, but inline classification inside methods is not unit-reachable (b.run is a method, not injectable; real-subprocess timeout classification is Linux-only).
- **Fix:** Extracted classifyMgmtRun as a pure function — the exact classifyPregenResult precedent from Plan 03-05 recorded in STATE.md as planner-sanctioned.
- **Files modified:** pkg/pybridge/vocab_mgmt.go, pkg/pybridge/vocab_mgmt_test.go
- **Verification:** TestClassifyMgmtRun 6/6 green without a subprocess
- **Committed in:** 1990191 (Task 2 commit)

---

**Total deviations:** 4 auto-fixed (2 Rule 3 blocking, 2 Rule 1 correctness/testability)
**Impact on plan:** All fixes were required for the plan's own acceptance criteria to hold (compilation with the grown seam, honest destructive-command guards, subprocess-free sentinel coverage). The shipped contracts match the plan's must_haves verbatim; no scope creep.

## Issues Encountered
- Unrelated flake observed during the final full-repo short run: cmd/service TestPhotoOCR_LiveRealVision (live Mimo vision network test) failed once on the English handwriting fixture ("识别失败", confidence 0) while the Chinese fixture recognized at confidence 1.0; it passed at baseline before this plan's commits and shares no code path with pybridge. Classified out of scope (live-service accuracy flake); none of this plan's verification commands touch it.

## User Setup Required

None - no external service configuration required.

## Next Phase Readiness
- 09-05 (REST endpoints) can wire PUT /words/{id} / rename / delete handlers straight onto the Executor seams: sentinel mapping unchanged (ErrPythonExit → check ExitError.Stdout for 'No cards found' → not_found; the rest → 502 python_failed), and should truncate Stdout before echoing (T-09-07)
- 09-07 (UI) gets DeleteWordsOpts' two modes (word list / unit) and RenameUnitOpts' three fields as the exact API payload shapes
- REQUIREMENTS.md VOC-01/VOC-02 intentionally left Pending: sibling plans 09-05/09-07 declare the same IDs (shared-ID gate #2388) and flip them when their summaries land

---
*Phase: 09-parent-vocabulary-management*
*Completed: 2026-09-27*

## Self-Check: PASSED

- key-files.modified verified on disk (7/7 present, all committed)
- `git log --grep="09-03"` returns 4 task commits (14fee5a, 35cfe60, 1265d34, 1990191)
- All `<acceptance_criteria>` re-run green:
  - Task 1: `go test ./pkg/pybridge/ ./pkg/fsrs/ ./pkg/tts/ -short -run 'TestEditArgs|TestRenameUnitArgs|TestDeleteWordsArgs|TestAdd'` — PASS; `go build ./...` green with the grown Executor interface — PASS; --confirm asserted in every delete table row — PASS
  - Task 2: `go test ./pkg/pybridge/ -short` — PASS; ExitError exposes Stdout (unit + e2e assertions) — PASS; delete round-trip leaves exactly the expected rows (2-by-list then 1-by-unit, 0 remain) — PASS; full py-e2e via the make py-e2e path (`UV_INDEX_URL=… go test -tags=e2e -count=1 ./pkg/pybridge/`) — ok, 5.8s
- Plan `<verification>` re-run: `go test ./pkg/pybridge/ -short` green; py-e2e green; `go build ./...` green (fsrs/tts/web-api fakes updated)
