---
phase: 09-parent-vocabulary-management
plan: 05
subsystem: api
tags: [rest-api, http-handlers, error-envelope, fsrs, atomic-write, validation, go]

# Dependency graph
requires:
  - phase: 09-parent-vocabulary-management
    provides: 09-03 bridge seams (RunEdit/RunRenameUnit/RunDeleteWords + ExitError.Stdout diagnostics), 09-02 Python contracts (cmd_edit JSON, fsrs_config.json loader semantics), 09-04 read endpoints (listWords/listUnits + locked error envelope)
provides:
  - Five new routes on the /api/v1 internal mux: PUT /words/{id}, POST /units/rename, DELETE /words, GET /settings/fsrs, PUT /settings/fsrs — the complete Phase 9 REST contract (8 routes total with the 09-01/09-04 read surface)
  - not_found error code wired end-to-end: vocab_manager.py's stdout diagnostics ("Card N not found" / "No cards found with unit ...") surface as 404 messages truncated to 300 runes (Pitfall 2 closed at the API layer, T-09-07 bounded)
  - fsrs_config.go: readFSRSConfig (never errors — missing/corrupt → 0.85/180 defaults) + writeFSRSConfig (same-directory temp → write → Sync → Close → os.Rename; any failure removes the temp and preserves the original)
  - Request contracts: EditRequest (pointer fields = None-vs-empty over the wire, category key rejected via DisallowUnknownFields), RenameRequest, DeleteRequest (words XOR unit), FSRSSettings (two knobs only — extra scheduler fields are 400, not silently ignored)
  - cmd/service/main.go wiring: NewHandler(exec, cards, cfg, logger) — DataDir reaches the settings handlers config-derived, never user input (T-09-10)
affects: [09-06, 09-07, 09-08]

# Actuals (#2632)
actuals:
  tokens: 12000
  tasks: 3
  commits: 6

# Tech tracking
tech-stack:
  added: []
  patterns:
    - "Atomic config write: os.CreateTemp in the SAME directory → write → Sync → Close → os.Rename, with defer os.Remove guarding every exit path — a half-written config can never exist at the target path (D-14/T-09-06)"
    - "Shared mapBridgeError for all three mutation handlers: ExitError.Stdout not-found diagnostic → 404 not_found; the four pybridge sentinels → 502 python_failed; anything else → 500 internal — one mapping, no per-handler drift"
    - "Struct declaration order defines on-disk JSON key order — FSRSSettings marshals desired_retention before maximum_interval deterministically (VOC-03 ordering edge)"

key-files:
  created:
    - pkg/web/api/fsrs_config.go
    - pkg/web/api/fsrs_config_test.go
  modified:
    - pkg/web/api/handlers.go
    - pkg/web/api/validate.go
    - pkg/web/api/handlers_test.go
    - cmd/service/main.go

key-decisions:
  - "One shared mapBridgeError (edit/rename/delete) instead of per-handler sentinel switches — the not_found/Pitfall-2 mapping lives in exactly one place; addWords keeps its 09-01 inline mapping untouched (behavior-preserving scope)"
  - "Stdout diagnostics are substring-matched ('not found' / 'No cards found') and truncated to 300 runes before echoing — bounded command output only in the envelope (T-09-07), never stderr or stack traces"
  - "editWord rejects id ≤ 0 (not just unparseable) — PathValue '0' or negative would otherwise reach the bridge and come back as a confusing script-level not-found"
  - "Read side of fsrs_config.json never errors to the client (missing/corrupt → defaults) — mirrors Plan 09-02's Python loader fallback semantics so GET is always a valid settings snapshot"

patterns-established:
  - "Pattern: atomic config replace via same-directory temp + Sync + Rename — the in-repo precedent for any future config-file writer"
  - "Pattern: subprocess stdout diagnostics ride the error envelope as bounded not_found messages — the script's user-facing text reaches the parent UI verbatim (Pitfall 2 end-to-end)"

requirements-completed: [VOC-01, VOC-02, VOC-03]  # copied from plan frontmatter verbatim; REQUIREMENTS.md flips are gated by siblings 09-06/07/08 (shared-ID rule #2388) — all three remain Pending until those summaries land

coverage:
  - id: D1
    description: "PUT /words/{id}: numeric-path parse (invalid/≤0 → 400), strict decode (category key → 400, D-07 read-only), pointer-field mapping onto EditOpts, 200 {\"edited\":{id,word}} echo; missing id → 404 not_found with the script's stdout diagnostic"
    requirement: VOC-01
    verification:
      - kind: unit
        ref: "pkg/web/api/handlers_test.go#TestEditWord"
        status: pass
      - kind: unit
        ref: "pkg/web/api/handlers_test.go#TestEditWordNotFound"
        status: pass
      - kind: e2e
        ref: "live: PUT /api/v1/words/{id} round trip against running service — edit echoed, GET /words reflected, 404/400 branches verified (V4-V8)"
        status: pass
    human_judgment: false
  - id: D2
    description: "POST /units/rename: three required fields, 200 {\"renamed\",from,to}; nonexistent unit → 404 not_found carrying the stdout diagnostic (Pitfall 2 closed end-to-end); sentinel failure → 502 python_failed"
    requirement: VOC-02
    verification:
      - kind: unit
        ref: "pkg/web/api/handlers_test.go#TestRenameUnit"
        status: pass
      - kind: unit
        ref: "pkg/web/api/handlers_test.go#TestRenameUnitPythonFailure"
        status: pass
      - kind: e2e
        ref: "live: rename of real unit returned renamed count, GET /units reflected the new name, nonexistent unit returned 404 not_found (V9-V11)"
        status: pass
    human_judgment: false
  - id: D3
    description: "DELETE /words: words-XOR-unit target validation (both/neither → 400), ≤100 entries, enum-checked category; handler is ALWAYS the confirmed path (no dry-run surface — --confirm structural guarantee from 09-03); 200 {\"deleted\":N}"
    requirement: VOC-02
    verification:
      - kind: unit
        ref: "pkg/web/api/handlers_test.go#TestDeleteWordsByWordList"
        status: pass
      - kind: unit
        ref: "pkg/web/api/handlers_test.go#TestDeleteWordsByUnit"
        status: pass
      - kind: unit
        ref: "pkg/web/api/handlers_test.go#TestDeleteWordsValidation"
        status: pass
      - kind: unit
        ref: "pkg/web/api/handlers_test.go#TestDeleteWordsPythonFailure"
        status: pass
      - kind: e2e
        ref: "live: throwaway word deleted by word-list mode (others intact), throwaway unit deleted by unit mode, XOR branches returned 400 (V12-V16)"
        status: pass
    human_judgment: false
  - id: D4
    description: "GET/PUT /settings/fsrs: GET returns file values or 0.85/180 defaults (corrupt file → defaults, never an error); PUT bounds-validates retention [0.7,0.95] and integer interval [1,36500], rejects extra scheduler fields via DisallowUnknownFields (two-knobs prohibition, D-13)"
    requirement: VOC-03
    verification:
      - kind: unit
        ref: "pkg/web/api/fsrs_config_test.go#TestGetFSRSDefaults"
        status: pass
      - kind: unit
        ref: "pkg/web/api/fsrs_config_test.go#TestGetFSRSFromFile"
        status: pass
      - kind: unit
        ref: "pkg/web/api/fsrs_config_test.go#TestGetFSRSCorruptFile"
        status: pass
      - kind: unit
        ref: "pkg/web/api/fsrs_config_test.go#TestPutFSRSValidation"
        status: pass
      - kind: e2e
        ref: "live: PUT 0.88/200 → GET echoes → 0.69/0.96/0/36501/2.5/extra-field all 400 invalid_request (S1-S10)"
        status: pass
    human_judgment: false
  - id: D5
    description: "Atomic fsrs_config.json write: same-directory temp → write → Sync → Close → os.Rename; failure removes temp and preserves the original; re-save is idempotent; file serializes with fixed key order desired_retention then maximum_interval"
    requirement: VOC-03
    verification:
      - kind: unit
        ref: "pkg/web/api/fsrs_config_test.go#TestPutFSRSWritesAtomically"
        status: pass
      - kind: e2e
        ref: "live: on-disk file bytes exactly {\"desired_retention\":0.88,\"maximum_interval\":200}; subsequent vocab_manager.py spawns (add/edit/rename/delete) all succeeded reading the Go-written file — the Python reader consumes the Go writer's output (S4 + V1-V16)"
        status: pass
    human_judgment: false
  - id: D6
    description: "Route wiring final: NewHandler(exec, cards, cfg, logger) in cmd/service/main.go mounts the complete Phase 9 API — 8 routes on the internal mux, one error envelope, four error codes (invalid_request, not_found, python_failed, internal)"
    requirement: VOC-01
    verification:
      - kind: other
        ref: "command: go build -o bin/service.exe ./cmd/service (green) + live service boot serving all 8 routes (31/31 live acceptance checks)"
        status: pass
    human_judgment: false

# Metrics
duration: 16min
completed: 2026-09-28
status: complete
---

# Phase 9 Plan 05: Complete REST surface — edit/rename/delete endpoints + FSRS settings file API Summary

**Five new REST endpoints (PUT /words/{id}, POST /units/rename, DELETE /words, GET/PUT /settings/fsrs) completing the Phase 9 parent API — script stdout diagnostics surface as 404 not_found, and the two-knob FSRS config persists through an atomic temp+rename write that the Python scheduler consumes on its next spawn.**

## Performance

- **Duration:** 16 min (resume session — the interrupted first run had already committed all implementation; this session ran verification + close-out)
- **Started:** 2026-09-28T22:41:37Z
- **Completed:** 2026-09-28T22:57:40Z
- **Tasks:** 3 (each TDD RED → GREEN)
- **Files modified:** 6 (+1199 / −12)

## Accomplishments
- The complete Phase 9 REST contract is final: 8 routes on the /api/v1 internal mux (3 read routes from 09-01/09-04 + 5 new mutation/settings routes), one error envelope, four error codes — Plans 09-06/09-07 build UI purely against this surface
- Pitfall 2 closed at the API layer: vocab_manager.py's stdout not-found diagnostics ("Card N not found", "No cards found with unit ...") ride ExitError.Stdout into a 404 not_found envelope, truncated to 300 runes (T-09-07)
- fsrs_config.json is written atomically (same-directory temp → Sync → os.Rename): no partial file can ever exist at the target path, re-saving is idempotent, and the fixed key order matches the Python loader key-for-key (D-14, VOC-03 ordering/adjacency edges)
- Delete is XOR-validated at the API (words XOR unit → else 400) and ALWAYS routes to the confirmed delete path — no dry-run surface exists at the API level (VOC-01 two-gate guarantee, server side)
- Live acceptance: 31/31 checks against the real service + real Python subprocesses, including the settings round trip proving the Python reader consumes the Go writer's file on every subsequent spawn

## Task Commits

Each task was committed atomically (TDD RED → GREEN per task):

1. **Task 1: Edit + rename endpoints** — RED `02e411a` (test(09-05): add failing edit/rename handler suites), GREEN `757b1d8` (feat(09-05): add PUT /words/{id} + POST /units/rename endpoints)
2. **Task 2: Delete endpoint with XOR validation** — RED `efb98a9` (test(09-05): add failing delete-words handler suites), GREEN `6f5688e` (feat(09-05): add DELETE /words endpoint with XOR target validation)
3. **Task 3: FSRS settings + atomic config write** — RED `fcf0784` (test(09-05): add failing FSRS settings handler suites), GREEN `07f9ea8` (feat(09-05): add GET/PUT /settings/fsrs with atomic config write)

## TDD Gate Compliance

| Task | RED commit | GREEN commit | Sequence valid |
|------|-----------|--------------|----------------|
| 1 (edit + rename) | 02e411a | 757b1d8 | ✓ |
| 2 (delete) | efb98a9 | 6f5688e | ✓ |
| 3 (fsrs settings) | fcf0784 | 07f9ea8 | ✓ |

## Files Created/Modified
- `pkg/web/api/fsrs_config.go` (NEW) — readFSRSConfig/writeFSRSConfig (atomic temp+rename), FSRSSettings struct, D-13 bounds constants, getFSRS/putFSRS handlers
- `pkg/web/api/fsrs_config_test.go` (NEW) — five suites: defaults / from-file / corrupt-file / validation table / atomic-write (incl. read-only-directory failure case)
- `pkg/web/api/handlers.go` — editWord/renameUnit/deleteWords handlers + route registrations; shared mapBridgeError + stdoutNotFoundMsg (300-rune truncation)
- `pkg/web/api/validate.go` — EditRequest/RenameRequest/DeleteRequest/FSRSSettings types + validateEditRequest/validateRenameRequest/validateDeleteRequest/ValidateFSRSSettings
- `pkg/web/api/handlers_test.go` — eight new suites (TestEditWord, TestEditWordNotFound, TestRenameUnit, TestRenameUnitPythonFailure, TestDeleteWordsByWordList, TestDeleteWordsByUnit, TestDeleteWordsValidation, TestDeleteWordsPythonFailure); fake Executor grew RunEdit/RunRenameUnit/RunDeleteWords
- `cmd/service/main.go` — NewHandler call site grew `cfg.Python` (DataDir reaches the settings handlers)

## Decisions Made
- One shared mapBridgeError serves edit/rename/delete: ExitError.Stdout carrying a not-found diagnostic → 404 not_found (truncated to 300 runes); any of the four pybridge sentinels → 502 python_failed; anything else → 500 internal. The 09-01 addWords inline mapping was left untouched to keep the diff behavior-preserving.
- Stdout diagnostics are substring-matched ("not found" / "No cards found") rather than exact-match — the script's wording can evolve without breaking the mapping, and non-matching stdout falls through to the sentinel mapping honestly.
- editWord rejects id ≤ 0 at parse time, not just unparseable tokens — "0" and negatives would otherwise reach the bridge and return as confusing script-level errors.
- The read side of fsrs_config.json never errors to the client: missing or corrupt file → 0.85/180 defaults, mirroring the Python loader's fallback semantics (Plan 09-02) so GET always returns a valid settings snapshot.
- Live acceptance was net-zero on the environment: fsrs_config.json was snapshotted and restored (it did not exist locally — the test removed it after), and all throwaway words/units added during checks were deleted by the endpoints under test.

## Deviations from Plan

None - plan executed exactly as written. (The interrupted first session committed the implementation; this resume session's verification found no defects requiring fixes.)

## Issues Encountered
- Factual note: the plan's success_criteria says "7 routes on the internal mux"; the shipped surface has 8 (GET+POST /words, GET /units, PUT /words/{id}, POST /units/rename, DELETE /words, GET+PUT /settings/fsrs). The plan undercounted the three pre-existing 09-01/09-04 read routes plus its five additions; no behavioral impact.
- Prior-session process failure (root cause of the interruption): the first run launched the service in the foreground and blocked; this resume ran it per the background service recipe (Start-Process + poll + try/finally cleanup) — 31/31 checks green and :8080 verified released afterward.

## User Setup Required

None - no external service configuration required.

## Next Phase Readiness
- 09-06 (add-words UI) and 09-07 (browse/organize + settings UI) can build purely against the final API: EditRequest's pointer fields (absent = keep, empty = clear), DeleteRequest's two modes, RenameRequest's three fields, and the exact error-envelope codes (invalid_request / not_found / python_failed / internal)
- 09-07's delete confirmation dialog completes the second VOC-01 gate (client-side count-showing confirm) — the server-side gate shipped here
- REQUIREMENTS.md VOC-01/VOC-02/VOC-03 intentionally left Pending: sibling plans 09-06/09-07/09-08 declare the same IDs (shared-ID gate #2388); they flip when those summaries land
- Local .hta-data/vocab.db unchanged by acceptance (throwaway rows deleted via the endpoints themselves)

---
*Phase: 09-parent-vocabulary-management*
*Completed: 2026-09-28*

## Self-Check: PASSED

- key-files verified on disk (6/6 present, all committed): fsrs_config.go, fsrs_config_test.go (created); handlers.go, validate.go, handlers_test.go, cmd/service/main.go (modified)
- `git log --grep="09-05"` returns 6 task commits (02e411a, 757b1d8, efb98a9, 6f5688e, fcf0784, 07f9ea8)
- All `<acceptance_criteria>` re-run green:
  - Task 1: `go test ./pkg/web/api/ -short -run 'TestEditWord|TestRenameUnit'` — PASS (within full-suite run); live: PUT edits a word and GET /words reflects the change; rename of a real unit returned the renamed count — PASS
  - Task 2: `go test ./pkg/web/api/ -short -run 'TestDeleteWords'` — PASS (within full-suite run); live: throwaway word deleted by word-list mode, witness word intact — PASS
  - Task 3: `go test ./pkg/web/api/ -short -run 'TestGetFSRS|TestPutFSRS'; go build ./...` — PASS; live: PUT 0.88/200 → GET 0.88/200 → file contains exactly the two keys → subsequent Python spawns (add/edit/rename/delete) all succeeded with the file present — PASS
- Plan `<verification>` re-run:
  - `go test -short -count=1 ./...` — all 13 packages ok (incl. pkg/web/api)
  - Live service round trips — 31/31 checks passed (settings round trip + file bytes + validation table; edit → list reflects; rename → units reflects; delete → words gone, XOR 400s; Python diagnostics as 404 not_found)
  - Cleanup: 0 service.exe processes, :8080 released, fsrs_config.json restored to pre-test state
