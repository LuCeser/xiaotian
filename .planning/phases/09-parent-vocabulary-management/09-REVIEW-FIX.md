---
status: all_fixed
findings_in_scope: 6
fixed: 6
skipped: 0
iteration: 1
phase: "09"
phase_name: parent-vocabulary-management
---

# Code Review Fix Report: Phase 9

fix_scope: `critical_warning` — CR-01 and WR-01..WR-05 fixed, one atomic commit per finding. IN-01..IN-04 skipped (info-level, out of scope).

## Per-finding results

### CR-01 — fixed
**Reject comma-containing words across add/edit/delete validation.**
Commit: `3a3f75ab0469b8ce30aba1ae155746b334947159`

Files changed:
- `pkg/web/api/validate.go` — `validateAddRequest` (entries loop, before rune check), `validateEditRequest` (after required check), and `validateDeleteRequest` (words loop, after empty check) now reject any word containing an ASCII comma: `entries[%d].word must not contain commas` / `word must not contain commas` / `words[%d] must not contain commas`, mirroring the Python `cmd_delete_words` comma-split contract (`strings` import added).
- `pkg/web/static/js/parent-batch-parser.js` — per-word validation rejects words containing `,` after the empty-word check, pushing `{line, reason: 'word-comma'}` (same error shape as existing parse errors); file-header contract updated.
- `pkg/web/static/js/parent-modes.js` — `describeParseError` maps `word-comma` → `单词不能包含英文逗号` (otherwise the parent would see the raw reason code); doc comment updated.
- `pkg/web/api/handlers_test.go` — new subtests: add comma word → 400 invalid_request naming the comma rule; edit comma word → 400; `TestDeleteWordsValidation` table gains `comma in word` case.
- `tests/test-parent-batch-parser.js` — new block 7: single comma word → `word-comma` error; mixed paste flags only the comma line (blocks 7-9 renumbered).

Verification: `go test ./pkg/web/api/ -run "TestAddWords|TestEditWord|TestDeleteWords"` pass; `node tests/test-parent-batch-parser.js` all assertions passed; `node --check` clean on both JS modules.

### WR-01 — fixed
**Add path rejects empty word → 400 invalid_request instead of 502 python_failed.**
Commit: `bd764bf5b91e72e7f6d076668c5d801a75c741ce`

Files changed:
- `pkg/web/api/validate.go` — `validateAddRequest` entries loop starts with `if e.Word == ""` → `entries[%d].word must not be empty` (mirrors `validateEditRequest`); doc comment updated.
- `pkg/web/api/handlers_test.go` — new `TestAddWords` subtest: empty word → 400 with invalid_request envelope.

Verification: `go test ./pkg/web/api/ -run TestAddWords -count=1 -v` — all 10 subtests pass including `empty_word_returns_400_invalid_request`.

### WR-02 — fixed
**readFSRSConfig falls back to defaults on out-of-bounds file values.**
Commit: `e0502760b46028b456c590e5fdccf8987e109269`

Files changed:
- `pkg/web/api/fsrs_config.go` — after successful `json.Unmarshal`, `readFSRSConfig` runs `ValidateFSRSSettings`; on failure returns `fsrsSettingsDefaults()` (whole-file fallback mirroring Python `load_scheduler`), so GET never reports values the scheduler will not use.
- `pkg/web/api/fsrs_config_test.go` — new `TestGetFSRSOutOfBoundsFile`: retention 1.7 / 0.5, missing `maximum_interval` (decodes to 0), interval 99999 → all return defaults; boundary-valid file (0.95/36500) still returned verbatim.

Verification: `go test ./pkg/web/api/ -run TestGetFSRS -count=1 -v` — all pass (9 subtests across 4 tests).

### WR-03 — fixed
**delete-words not-found prints trailing `{"deleted": 0}` with exit 0 (idempotent no-op, not 502).**
Commit: `b47fd67b2662eca0ac694d18ec50af45ea899e1f`

Files changed:
- `third_party/python/vocab_manager.py` — `cmd_delete_words` option (b) minimal change: both not-found branches (unit-mode "No cards found with unit ..." and words-mode "No matching cards found") now `print(json.dumps({"deleted": 0}))` + `sys.exit(0)`, so Go `classifyMgmtRun`/`extractTrailingJSON` parses a typed result instead of erroring ErrPythonJSONParse → 502. Normal deletions keep printing their existing result JSON (verified byte-identical).
- `pkg/pybridge/vocab_mgmt_e2e_test.go` — `TestE2E_RunDeleteWords` extended: delete nonexistent unit and nonexistent word through the REAL interpreter+script+SQLite stack → nil error, `Deleted == 0` (the path e2e's `.catch(()=>{})` previously masked).

Go side confirmed NO change needed: `DeleteWordsResult` models only `deleted` (executor.go:372-374), so `{"deleted": 0}` unmarshals and the handler echoes a 200; `parent-browse.js` renders `已删除 0 个单词` via `deleted += (result && result.deleted) || 0`.

Verification: `py -3` store-stub unusable (exit 9009); used repo venv interpreter instead. `third_party/python/.venv/Scripts/python.exe -m py_compile third_party/python/vocab_manager.py` → exit 0. Behavioral no-DB-adjacent check against a temp SQLite DB: unit-mode not-found → `{"deleted": 0}` exit 0; words-mode not-found → `{"deleted": 0}` exit 0; normal delete → `{"deleted": 1, "words": ["apple"]}` unchanged; immediate re-delete → `{"deleted": 0}`. Plus `go test ./pkg/pybridge/ -tags=e2e -run TestE2E_RunDeleteWords -count=1` PASS through the real stack.

### WR-04 — fixed
**e2e full-flow snapshots and restores fsrs settings in finally.**
Commit: `104f6128ea188904c64eab3ef3687d4f7f14cd8d`

Files changed:
- `tests/e2e/parent-management.spec.js` — full-flow test snapshots `await (await request.get('/api/v1/settings/fsrs')).json()` at the start; existing `finally` block keeps the three `deleteUnit` cleanup calls and adds `await request.put('/api/v1/settings/fsrs', { data: settingsBefore }).catch(() => {})`. Step 7's `恢复默认` UI interaction stays (it tests the button) with comments updated to note the finally-restore is the authoritative hygiene step; file-header hygiene note updated.

Verification: `node --check tests/e2e/parent-management.spec.js` clean (Playwright suite not run locally — requires the full webServer harness; change is snapshot/restore-only around existing verified steps).

### WR-05 — fixed
**Config.Validate rejects empty python.dataDir (Pitfall 8 single-resolution rule).**
Commit: `307be22fb80216a5662b32ac35584b6546212197`

Files changed:
- `pkg/infra/config.go` — `Config.Validate()` adds, after the mgmtTimeout check: `errors.Wrap(ErrConfigInvalid, "python.dataDir must not be empty (anchors fsrs_config.json writer and DICTATION_DATA_DIR reader on one directory)")`; `DataDir` field doc explains the anchor role.
- `pkg/infra/config_test.go` — `setRequiredPythonEnv` now also sets `HTA_PYTHON_DATADIR` (covers all 13 LoadConfig-success tests); `TestWSConfigValidate` struct-based subtest populates `cfg.Python.DataDir`; new `TestLoadConfigInvalidPythonDataDir` (empty dataDir → ErrConfigInvalid via errors.Is).
- `pkg/infra/static_test.go` — `TestStaticRouteValidationWhenEnabled` populates `cfg.Python.DataDir = t.TempDir()`.
- `pkg/infra/server_test.go` — `fastServerConfig()` populates `cfg.Python.DataDir` (NewServer's defensive Validate gate).
- `cmd/service/main_test.go` — ripple fix: `clearHTAEnv` (which supplies the required python env pair for boot tests) also sets `HTA_PYTHON_DATADIR`; without it 7 `TestMain*` boot tests failed with the new validation error. All pass again.

Verification: `go test ./pkg/infra/ -run TestLoadConfigInvalidPythonDataDir -count=1 -v` PASS; full `go test ./... -count=1`: every package ok — sole failure is `TestPhotoOCR_LiveRealVision` (live Mimo vision API call returning confidence 0; environmental, gated on MIMO_API_KEY, unrelated to this fix — present before these changes).

## Skipped findings

### IN-01..IN-04 — skipped (info-level, out of scope)
- IN-01 (stdoutNotFoundMsg substring heuristic) — info-level, out of scope.
- IN-02 (context.Canceled classified as ErrPythonTimeout) — info-level, out of scope.
- IN-03 (out-of-plan mimo/run.ps1 changes bookkeeping) — info-level, out of scope.
- IN-04 (duplicated DOM helpers across parent JS modules) — info-level, out of scope.

## Verification summary

- `go build ./...` — PASS.
- `go test ./pkg/web/api/... ./pkg/pybridge/... ./pkg/infra/... -count=1` — PASS (all three ok).
- `go test ./... -count=1` — all packages ok; only pre-existing live-API `TestPhotoOCR_LiveRealVision` fails (unrelated, environmental).
- `go test ./pkg/pybridge/ -tags=e2e -count=1` (real interpreter + script + SQLite) — PASS.
- Python: `py_compile vocab_manager.py` exit 0 (repo venv interpreter; bare `python` is a Windows store stub) + live behavioral checks of the WR-03 not-found/normal/idempotent paths against a temp DB.
- Node: full `package.json` test chain (7 suites) all pass; `node --check` clean on touched JS files.

Commits (one per finding, in order): `3a3f75a` CR-01, `bd764bf` WR-01, `e050276` WR-02, `b47fd67` WR-03, `104f612` WR-04, `307be22` WR-05. No `.planning/` files committed; pre-existing CRLF-noise and untracked scratch files left untouched (staged files were content-identical to HEAD apart from the deliberate edits, verified via empty content diffs under `core.autocrlf=true`).
