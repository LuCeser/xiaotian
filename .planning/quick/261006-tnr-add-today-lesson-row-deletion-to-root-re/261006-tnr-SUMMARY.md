---
phase: quick-261006-tnr
plan: 01
subsystem: tooling
tags: [reset-vocab, sqlite, fsrs, rt-dictation, debug-utility]
requires:
  - ".hta-data/vocab.db (cards/lessons/tool_requests tables, live local DB)"
  - "resolveLessonID id rule (pkg/rt/dictation.go:515-521)"
provides:
  - "reset-vocab.py full-reset now deletes today's R0 lesson row + its tool_requests rows (newly tracked in git)"
  - "verify-reset-vocab.py sandbox harness (re-runnable, stdlib-only)"
affects:
  - "/rt reconnect behavior after a full reset: fresh-lesson path (FetchDueWords) instead of startCompletedSession off the stale row"
tech-stack:
  added: []
  patterns:
    - "read-only URI snapshot + sqlite3 backup API for live-DB isolation (WAL-safe)"
    - "UTC+8 fixed-offset timezone for Beijing-calendar lesson ids (mirrors time.FixedZone in Go)"
key-files:
  created:
    - .planning/quick/261006-tnr-add-today-lesson-row-deletion-to-root-re/verify-reset-vocab.py
  modified:
    - reset-vocab.py
decisions:
  - "Lesson id derived with timezone(timedelta(hours=8)) fixed offset — byte-identical rule to resolveLessonID; never machine-local tz"
  - "Child-first deletion (tool_requests then lessons), both parameterized and scoped to today's id only (T-261006-01)"
  - "OperationalError guard also covers the verify-section lessons SELECT (Rule 2): without it the pre-Phase-13 skip path would crash on the very next statement, defeating the plan's 'must not fail the whole reset' intent"
metrics:
  duration: 6 min
  completed: 2026-10-06
  tasks: 2
  commits: 2
status: complete
plan_head_before: 9dc996b
plan_head_after: ed94a38
actuals:
  tokens: 2950
  tasks: 2
  commits: 2
---

# Quick Task 261006-tnr Summary

**One-liner:** reset-vocab.py full reset now also deletes today's r0-YYYYMMDD lesson row and its tool_requests child rows (UTC+8 id rule mirroring resolveLessonID), so a reconnecting /rt session re-fetches due words instead of answering "classroom complete" off the stale row — verified green against a sandboxed backup-API snapshot of the live DB.

## What Was Built

### Task 1 — Sandbox verification harness (RED) — commit 2b0fad1
`.planning/quick/261006-tnr-add-today-lesson-row-deletion-to-root-re/verify-reset-vocab.py` (stdlib-only):
- Snapshots the live `.hta-data/vocab.db` **read-only** (`mode=ro` URI) into a temp sandbox via the sqlite3 **backup API** — consistent even in WAL mode; the live DB is never modified.
- Seeds (INSERT OR REPLACE): a completed-looking lessons row for today's id, a row for yesterday's id, two tool_requests rows for today.
- Runs `reset-vocab.py` with cwd inside the sandbox so its relative `./.hta-data/vocab.db` resolves to the sandbox copy.
- Five assertions; exit nonzero on any failure; sandbox removed in `finally`.
- RED confirmed: lesson-row assertions FAILED, cards assertion PASSED (3/5) — proving the harness detects the gap.

### Task 2 — Lesson-row deletion in reset-vocab.py (GREEN) — commit ed94a38
- Module docstring documents the lesson-row deletion behavior.
- `CST = timezone(timedelta(hours=8))` + `today_lesson_id()` → `"r0-" + YYYYMMDD`, byte-identical rule to `resolveLessonID` (pkg/rt/dictation.go:515-521); never machine-local time.
- Deletion section after the FSRS rewrite, before commit: prints the computed id, `DELETE FROM tool_requests WHERE lesson_id = ?` then `DELETE FROM lessons WHERE lesson_id = ?` (child first), each rowcount printed.
- Verify Results extended: lists remaining lessons rows (lesson_id/current_index/created_at) and prints an explicit present/absent line for today's id.
- Existing cards reset behavior (fetch-lock clear + fsrs_card rewrite to Learning, due=now-1min) untouched; `DB_PATH` untouched.

## Verification

- Harness final run: **5/5 PASS, exit 0** (today's lessons row absent; its tool_requests absent; yesterday's row survives; all cards `fetch_locked_until` NULL; subprocess exit 0).
- Script output evidence in-run: `Deleted 9 tool_requests rows` / `Deleted 1 lessons rows` (9 = 7 live + 2 seeded for today's id), remaining rows `r0-test` + `r0-20261005`, `[OK] Today's lesson row r0-20261006 is absent`.
- Live DB post-run read-only check: `r0-20261006` still present with its 7 tool_requests — the harness never touched it.
- `git log --stat ed94a38`: both files in the one atomic commit; `reset-vocab.py` newly tracked (`create mode`, no longer untracked).

## Deviations from Plan

### Auto-fixed Issues

**1. [Rule 2 - Missing critical functionality] OperationalError guard on the verify-section lessons SELECT**
- **Found during:** Task 2
- **Issue:** The plan mandated try/except OperationalError on the two DELETEs so a pre-Phase-13 DB without lessons/tool_requests tables must not fail the whole reset — but the newly added verify SELECT on `lessons` had no guard, so on exactly that DB the script would crash immediately after the skip note, defeating the stated intent.
- **Fix:** Same try/except + `[SKIP]` note around the verify SELECT.
- **Files modified:** reset-vocab.py
- **Commit:** ed94a38

**2. [Rule 3 - Blocking evidence gap] Harness stdout tail widened 12 → 30 lines**
- **Found during:** Task 2
- **Issue:** The 12-line tail window cut off the deletion section, hiding the rowcount evidence the Task 2 done criterion requires.
- **Fix:** Tail window widened; assertions unchanged; harness re-run green.
- **Files modified:** verify-reset-vocab.py (same commit, per plan files_modified)
- **Commit:** ed94a38

## Environment Notes

- `python` on PATH is the broken Windows Store alias stub (exit 49, no output); the harness was run with `third_party/python/.venv/Scripts/python.exe` (3.12.0) per the task constraints' fallback. `[sys.executable, ...]` keeps the sandbox subprocess on the same interpreter automatically.
- Commits landed on `main` per the orchestrator's explicit sequential-run constraint (established quick-task convention in this repo).

## Self-Check: PASSED

- reset-vocab.py exists and is tracked (`git status` clean for it): FOUND
- verify-reset-vocab.py exists: FOUND
- Commit 2b0fad1 (RED harness): FOUND in git log
- Commit ed94a38 (GREEN implementation, both files): FOUND in git log
- Harness green (5/5, exit 0): CONFIRMED in final run
