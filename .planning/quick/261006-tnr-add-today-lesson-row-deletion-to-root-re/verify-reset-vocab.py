#!/usr/bin/env python3
"""
Verification harness for reset-vocab.py (quick task 261006-tnr).

Asserts that a full reset also deletes TODAY'S R0 classroom row (lesson_id =
"r0-" + Asia/Shanghai YYYYMMDD) and its tool_requests child rows, so a
reconnecting /rt session takes the fresh-lesson path (FetchDueWords) instead
of startCompletedSession answering off the stale completed row.

Isolation: the live .hta-data/vocab.db is opened READ-ONLY and snapshotted
into a temp sandbox through the sqlite3 backup API (consistent even in WAL
mode). reset-vocab.py is then run with its cwd inside the sandbox, so its
relative ./.hta-data/vocab.db resolves to the sandbox copy only. The live DB
is never modified.

Stdlib only. Run from anywhere; the repo root is derived from __file__.
"""

import shutil
import sqlite3
import subprocess
import sys
import tempfile
from datetime import datetime, timedelta, timezone
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parents[3]
SCRIPT = REPO_ROOT / "reset-vocab.py"
LIVE_DB = REPO_ROOT / ".hta-data" / "vocab.db"

# Asia/Shanghai as a fixed offset — UTC+8, no DST. Mirrors resolveLessonID
# (pkg/rt/dictation.go): time.FixedZone("CST", 8*3600). Never machine-local.
CST = timezone(timedelta(hours=8))


def lesson_ids():
    now = datetime.now(CST)
    today = "r0-" + now.strftime("%Y%m%d")
    yesterday = "r0-" + (now - timedelta(days=1)).strftime("%Y%m%d")
    return today, yesterday


def build_sandbox(sandbox: Path, today_id: str, yesterday_id: str) -> Path:
    """Snapshot the live DB read-only via the backup API, then seed rows."""
    (sandbox / ".hta-data").mkdir(parents=True, exist_ok=True)
    dest_path = sandbox / ".hta-data" / "vocab.db"

    live = sqlite3.connect(LIVE_DB.as_uri() + "?mode=ro", uri=True)
    try:
        dest = sqlite3.connect(dest_path)
        try:
            live.backup(dest)
        finally:
            dest.close()
    finally:
        live.close()

    conn = sqlite3.connect(dest_path)
    try:
        if conn.execute("SELECT COUNT(*) FROM cards").fetchone()[0] == 0:
            raise SystemExit(
                "FATAL: sandbox cards table is empty — the reset flow "
                "requires at least one card (live-DB state precondition)"
            )
        # INSERT OR REPLACE so seeding is deterministic whether or not the
        # live DB already carries today's row.
        conn.execute(
            "INSERT OR REPLACE INTO lessons (lesson_id, words, cards, current_index)"
            " VALUES (?, ?, ?, ?)",
            (today_id, '["verify-today"]', '[]', 1),  # completed-looking row
        )
        conn.execute(
            "INSERT OR REPLACE INTO lessons (lesson_id, words) VALUES (?, ?)",
            (yesterday_id, '["verify-yesterday"]'),
        )
        for req_id in (f"verify-req-a-{today_id}", f"verify-req-b-{today_id}"):
            conn.execute(
                "INSERT OR REPLACE INTO tool_requests (request_id, lesson_id, tool, result)"
                " VALUES (?, ?, ?, ?)",
                (req_id, today_id, "get_lesson_state", "{}"),
            )
        conn.commit()
    finally:
        conn.close()
    return dest_path


def main() -> int:
    if not SCRIPT.exists():
        raise SystemExit(f"FATAL: {SCRIPT} not found")
    if not LIVE_DB.exists():
        raise SystemExit(f"FATAL: live DB {LIVE_DB} not found")

    today_id, yesterday_id = lesson_ids()
    print(f"today_id={today_id}  yesterday_id={yesterday_id}")

    sandbox = Path(tempfile.mkdtemp(prefix="verify-reset-vocab-"))
    results = []
    try:
        build_sandbox(sandbox, today_id, yesterday_id)

        proc = subprocess.run(
            [sys.executable, str(SCRIPT)],
            cwd=sandbox,
            capture_output=True,
            text=True,
        )

        def check(name, ok):
            results.append(ok)
            print(f"{'PASS' if ok else 'FAIL'}: {name}")

        db = sqlite3.connect(sandbox / ".hta-data" / "vocab.db")
        try:
            n_today = db.execute(
                "SELECT COUNT(*) FROM lessons WHERE lesson_id = ?", (today_id,)
            ).fetchone()[0]
            check("today's lessons row absent after reset", n_today == 0)

            n_req = db.execute(
                "SELECT COUNT(*) FROM tool_requests WHERE lesson_id = ?", (today_id,)
            ).fetchone()[0]
            check("today's tool_requests rows absent after reset", n_req == 0)

            n_yest = db.execute(
                "SELECT COUNT(*) FROM lessons WHERE lesson_id = ?", (yesterday_id,)
            ).fetchone()[0]
            check("yesterday's lessons row survives (deletion scoped to today)", n_yest == 1)

            locked = db.execute(
                "SELECT COUNT(*) FROM cards WHERE fetch_locked_until IS NOT NULL"
            ).fetchone()[0]
            check("every cards row has fetch_locked_until NULL", locked == 0)
        finally:
            db.close()

        check("reset-vocab.py subprocess exits 0", proc.returncode == 0)

        print("\n--- reset-vocab.py stdout tail ---")
        lines = (proc.stdout or "").strip().splitlines()
        for line in lines[-12:]:
            print(f"  {line}")
        if proc.returncode != 0 and proc.stderr:
            print("--- stderr ---")
            print(proc.stderr)
    finally:
        shutil.rmtree(sandbox, ignore_errors=True)

    failed = sum(1 for ok in results if not ok)
    print(f"\n{len(results) - failed}/{len(results)} assertions passed")
    return 0 if failed == 0 else 1


if __name__ == "__main__":
    sys.exit(main())
