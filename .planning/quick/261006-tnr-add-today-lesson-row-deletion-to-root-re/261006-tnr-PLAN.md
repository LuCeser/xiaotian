---
phase: quick-261006-tnr
plan: 01
type: execute
wave: 1
depends_on: []
files_modified:
  - reset-vocab.py
  - .planning/quick/261006-tnr-add-today-lesson-row-deletion-to-root-re/verify-reset-vocab.py
autonomous: true
requirements: []
estimate:
  tokens: 22000
  raw_tokens: 11000
  tasks: 2
  confidence: low

must_haves:
  truths:
    - "reset-vocab.py deletes today's lessons row and its tool_requests rows; the lesson id is r0-YYYYMMDD computed in Asia/Shanghai (UTC+8, no DST), matching resolveLessonID in pkg/rt/dictation.go:515-521, so a reconnecting /rt session_init takes the fresh-lesson path (GetLessonState -> ErrLessonNotFound -> FetchDueWords, pkg/rt/turn.go:416-441) instead of startCompletedSession answering off the stale row"
    - "Deletion runs tool_requests before lessons (child first), both parameterized and scoped to today's lesson id only; other days' lesson rows survive"
    - "The script's verify output lists the remaining lessons rows and states whether today's id is absent"
    - "The existing cards reset behavior (clear fetch_locked_until + rewrite fsrs_card to Learning with due=now-1min) is unchanged"
    - "The verification harness operates exclusively on a sandboxed copy of .hta-data/vocab.db made through a read-only connection plus the sqlite3 backup API"
  artifacts:
    - "reset-vocab.py extended with lesson-row deletion and tracked in git by the task commit"
    - ".planning/quick/261006-tnr-add-today-lesson-row-deletion-to-root-re/verify-reset-vocab.py passing green"
  key_links:
    - "reset-vocab.py lesson-id derivation == resolveLessonID (pkg/rt/dictation.go:515-521): 'r0-' + UTC+8 YYYYMMDD"
    - "DELETE targets == lessons/tool_requests DDL (pkg/lesson/store.go:41-57) in the same vocab.db file as cards (cmd/service/main.go:269-283, config.local.yaml vocabDBPath)"
---

<objective>
Extend the root reset-vocab.py debug utility so a full reset also removes today's R0 classroom row: besides clearing cards.fetch_locked_until and rewriting fsrs_card to Learning, delete today's lessons row (lesson_id = r0-YYYYMMDD in Asia/Shanghai) and its tool_requests rows. After the reset, a reconnecting /rt client takes the fresh-lesson path in handleSessionInit (pkg/rt/turn.go:416-441: GetLessonState returns ErrLessonNotFound, so FetchDueWords runs again) and refetches due words, instead of startCompletedSession answering "today's classroom complete" off the stale row (pkg/rt/turn.go:451-452).

Purpose: the script currently leaves the completed lesson row in place, so resetting cards has no visible effect on /rt until the next Beijing-calendar day.
Output: extended reset-vocab.py (newly tracked in git), a sandbox verification harness, one atomic quick-task commit.
</objective>

<execution_context>
@D:/workspace/ai/xiaotian/.claude/gsd-core/workflows/execute-plan.md
@D:/workspace/ai/xiaotian/.claude/gsd-core/templates/summary.md
</execution_context>

<context>
@reset-vocab.py
@pkg/rt/dictation.go
@pkg/rt/turn.go
@pkg/lesson/store.go

Facts verified at planning time:
- resolveLessonID (pkg/rt/dictation.go:515-521): returns the configured id when set, else "r0-" + now.In(FixedZone("CST", 8*3600)).Format("20060102"). config.local.yaml keeps lessonId at its default (empty, line 95 note), so the r0- derivation is the live local path.
- handleSessionInit (pkg/rt/turn.go:416-455): the lesson row lookup runs BEFORE FetchDueWords. A found row with current_index >= len(cards) goes to startCompletedSession with no refetch; an absent row (ErrLessonNotFound) triggers FetchDueWords + EnsureLessonCards.
- DDL (pkg/lesson/store.go:41-57): lessons.lesson_id TEXT PRIMARY KEY, words TEXT NOT NULL (no default), cards/current_index/version have defaults; tool_requests has request_id PK, lesson_id TEXT NOT NULL, no foreign key; idx_tool_requests_lesson indexes tool_requests(lesson_id).
- lessons/tool_requests live in the SAME SQLite file as cards (cmd/service/main.go:269-283 comment + lesson.Migrate on the shared db handle); config.local.yaml vocabDBPath is ./.hta-data/vocab.db.
- reset-vocab.py opens ./.hta-data/vocab.db RELATIVE to cwd — the harness exploits this for sandbox isolation by running the script with cwd inside a temp sandbox.
- reset-vocab.py is currently UNTRACKED at the repo root; the task's atomic commit must git add it so it becomes tracked.
</context>

<tasks>

<task type="auto" tdd="true">
  <name>Task 1: Sandbox verification harness for reset-vocab.py (RED)</name>
  <files>.planning/quick/261006-tnr-add-today-lesson-row-deletion-to-root-re/verify-reset-vocab.py</files>
  <behavior>
    - Seeded completed lessons row for today's id (r0-YYYYMMDD, UTC+8) is absent from the sandbox lessons table after running reset-vocab.py
    - Seeded tool_requests rows for today's id are absent after the run (child rows deleted with the lesson)
    - A seeded lessons row for yesterday's id survives the run (deletion scoped to today only)
    - Every cards row in the sandbox has fetch_locked_until NULL after the run
    - reset-vocab.py subprocess exits 0
  </behavior>
  <precondition>.hta-data/vocab.db exists at the repo root with at least one cards row, the local Go service is stopped or idle (no concurrent writes during the snapshot), and a Python 3 interpreter is resolvable on PATH as `python`</precondition>
  <action>
    Create a stdlib-only Python harness (sqlite3, pathlib, tempfile, subprocess, datetime, shutil). Repo root is Path(__file__).resolve().parents[3] — the file sits at .planning/quick/261006-tnr-add-today-lesson-row-deletion-to-root-re/verify-reset-vocab.py, so parents[0] is the quick-task dir, parents[1] is quick, parents[2] is .planning, parents[3] is the repo root; assert (repo_root / "reset-vocab.py").exists() as a sanity check.
    Compute today_id = "r0-" + datetime.now(timezone(timedelta(hours=8))).strftime("%Y%m%d") and yesterday_id with the same clock minus timedelta(days=1) — mirroring resolveLessonID (UTC+8 fixed offset, never machine-local tz).
    Build the sandbox: sandbox = tempfile.mkdtemp(); create sandbox/.hta-data/; snapshot the live DB by opening .hta-data/vocab.db READ-ONLY (sqlite3.connect(Path(live).as_uri() + "?mode=ro", uri=True)) and calling live_conn.backup(dest_conn) into sandbox/.hta-data/vocab.db — a consistent copy even if the DB is in WAL mode.
    Seed the sandbox copy with INSERT OR REPLACE (deterministic whether or not the live DB already has today's row): one lessons row for today_id (words '["verify-today"]', current_index 1, cards '[]' — a completed-looking row), one lessons row for yesterday_id (words '["verify-yesterday"]'), and two tool_requests rows for today_id (distinct request_ids, tool 'get_lesson_state', result '{}').
    Fail fast with a clear message if the sandbox cards table is empty (the reset flow requires at least one card; live-DB state precondition).
    Run [sys.executable, str(repo_root / "reset-vocab.py")] with cwd set to the sandbox root — the script's relative ./.hta-data/vocab.db then resolves inside the sandbox — capturing stdout/stderr.
    Assert the five behaviors above by re-querying the sandbox DB; print one PASS/FAIL line per assertion plus the script's captured stdout tail; exit nonzero when any assertion fails; shutil.rmtree(sandbox, ignore_errors=True) in a finally block.
    Run the harness now: it MUST fail the today-lesson assertions (RED — reset-vocab.py does not delete lesson rows yet) while the cards-unlock assertion passes. That red run proves the harness detects the gap.
  </action>
  <verify>
    <automated>python .planning/quick/261006-tnr-add-today-lesson-row-deletion-to-root-re/verify-reset-vocab.py; ec=$?; if [ $ec -ne 0 ]; then echo "RED confirmed (exit $ec)"; else echo "UNEXPECTED GREEN"; exit 1; fi</automated>
  </verify>
  <done>Harness exists, runs to completion against the sandboxed copy, and reports FAILED lesson-row assertions with the cards assertion passing (RED). The live .hta-data/vocab.db was opened read-only and never modified.</done>
</task>

<task type="auto" tdd="true">
  <name>Task 2: Delete today's lesson row in reset-vocab.py (GREEN)</name>
  <files>reset-vocab.py</files>
  <behavior>
    - All five Task 1 behaviors now hold: today's lessons row deleted, its tool_requests rows deleted, yesterday's row survives, all cards unlocked, exit 0
  </behavior>
  <action>
    Extend reset-vocab.py. Update the module docstring to state the script also deletes today's R0 lesson row so /rt refetches due words on reconnect.
    Add timezone to the datetime import; define CST = timezone(timedelta(hours=8)) and a helper returning "r0-" + datetime.now(CST).strftime("%Y%m%d") — the exact rule of resolveLessonID (pkg/rt/dictation.go:515-521). Do NOT derive the lesson id from machine-local time or naive utcnow.
    After the FSRS rewrite loop and before conn.commit(), add a "Deleting today's lesson row" section: print the computed id, then DELETE FROM tool_requests WHERE lesson_id = ? followed by DELETE FROM lessons WHERE lesson_id = ? — child table first — printing each rowcount. Wrap the two deletes in try/except sqlite3.OperationalError printing a skip note (a pre-Phase-13 DB without these tables must not fail the whole reset); any other exception propagates to the existing handlers.
    Extend the "Verify Results" section: SELECT lesson_id, current_index, created_at FROM lessons ORDER BY created_at, print each remaining row, and print an explicit line stating whether today's id is still present (expected: absent).
    Leave the existing cards reset (fetch-lock clear + fsrs_card rewrite) and DB_PATH untouched.
    Re-run the Task 1 harness until green, then make the quick-task atomic commit include BOTH files — with git add reset-vocab.py explicitly, since the file is currently untracked at the repo root and this commit is what makes it tracked.
  </action>
  <verify>
    <automated>python .planning/quick/261006-tnr-add-today-lesson-row-deletion-to-root-re/verify-reset-vocab.py</automated>
  </verify>
  <done>Harness exits 0 with all five assertions PASS (GREEN); reset-vocab.py prints the deletion rowcounts and the remaining-lessons listing; git log --stat for the task commit shows reset-vocab.py (newly tracked) and the harness file together; git status no longer lists reset-vocab.py as untracked.</done>
</task>

</tasks>

<threat_model>
## Trust Boundaries

| Boundary | Description |
|----------|-------------|
| Operator → local SQLite | reset-vocab.py is operator-run local debug tooling mutating .hta-data/vocab.db |

## STRIDE Threat Register

| Threat ID | Category | Component | Severity | Disposition | Mitigation Plan |
|-----------|----------|-----------|----------|-------------|-----------------|
| T-261006-01 | Tampering | reset-vocab.py DELETE statements | low | mitigate | Parameterized SQL only; both deletes scoped to the computed today lesson_id |
| T-261006-02 | Denial of Service | live .hta-data/vocab.db | low | mitigate | Harness opens the live DB read-only (URI mode=ro) and snapshots via the backup API; during verification the script runs only against the sandbox copy |
| T-261006-SC | Tampering | package installs | low | accept | No package-manager installs in this task — Python stdlib only, supply-chain surface is nil |
</threat_model>

<verification>
- python .planning/quick/261006-tnr-add-today-lesson-row-deletion-to-root-re/verify-reset-vocab.py exits 0 with all assertions PASS
- git log --stat -1 shows reset-vocab.py and the harness in one atomic commit
- git status no longer lists reset-vocab.py under untracked files
</verification>

<success_criteria>
- All must_haves truths hold: today's lesson row and its tool_requests rows are deleted with the UTC+8 id rule, other-day rows survive, cards reset behavior unchanged
- Harness green; live DB untouched by verification; reset-vocab.py tracked in git
</success_criteria>

<output>
The task's atomic commit contains reset-vocab.py and .planning/quick/261006-tnr-add-today-lesson-row-deletion-to-root-re/verify-reset-vocab.py. Quick-task bookkeeping (STATE.md Quick Tasks row) is handled by the orchestrator.
</output>
