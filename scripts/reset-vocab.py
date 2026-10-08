#!/usr/bin/env python3
"""
Reset all vocabulary to Learning state and clear fetch locks

Also deletes today's R0 classroom row (lesson_id "r0-YYYYMMDD" in
Asia/Shanghai) and its tool_requests rows, so a reconnecting /rt session
takes the fresh-lesson path (GetLessonState -> ErrLessonNotFound ->
FetchDueWords) and re-fetches due words instead of resuming the stale
completed lesson row.
"""

import sqlite3
import json
import pathlib
import sys
from datetime import datetime, timedelta, timezone

# Anchored to this script's location (repo root/.hta-data), same paradigm as
# scripts/check_db.py — the same database is found from any CWD.
DB_PATH = str(pathlib.Path(__file__).resolve().parent.parent / ".hta-data" / "vocab.db")

# Asia/Shanghai as a fixed offset — UTC+8, no DST. Same rule as
# resolveLessonID (pkg/rt/dictation.go:515-521): never machine-local time.
CST = timezone(timedelta(hours=8))


def today_lesson_id() -> str:
    """Derive today's R0 lesson id exactly like resolveLessonID."""
    return "r0-" + datetime.now(CST).strftime("%Y%m%d")

def reset_vocabulary():
    """Reset all vocabulary to Learning state"""

    try:
        conn = sqlite3.connect(DB_PATH)
        cursor = conn.cursor()

        print("=== Reset Vocabulary State to Learning ===")

        # Check current vocabulary
        cursor.execute("SELECT COUNT(*) FROM cards")
        count = cursor.fetchone()[0]
        print(f"Found {count} words")

        if count == 0:
            print("No words in database")
            return False

        # Show current state
        cursor.execute("""
            SELECT id, word, fsrs_card, fetch_locked_until
            FROM cards
            LIMIT 3
        """)
        print("\nCurrent state:")
        for row in cursor.fetchall():
            fsrs = json.loads(row[2]) if row[2] else {}
            print(f"  ID={row[0]}, word={row[1]}, state={fsrs.get('state')}, due={fsrs.get('due')}, locked={row[3]}")

        # Clear all fetch locks
        print("\nClearing fetch locks...")
        cursor.execute("""
            UPDATE cards
            SET fetch_locked_until = NULL
            WHERE fetch_locked_until IS NOT NULL
        """)
        unlocked = cursor.rowcount
        print(f"  Cleared {unlocked} fetch locks")

        # Update all cards to Learning state with immediate due time
        print("\nUpdating FSRS state to Learning...")
        cursor.execute("SELECT id, fsrs_card FROM cards")
        cards = cursor.fetchall()

        updated_count = 0
        for card_id, fsrs_json in cards:
            try:
                fsrs = json.loads(fsrs_json) if fsrs_json else {}
                fsrs['state'] = 1  # Learning
                fsrs['step'] = 0
                fsrs['stability'] = 0
                fsrs['difficulty'] = 0
                # Set due time to now - 1 minute (immediately available)
                fsrs['due'] = (datetime.utcnow() - timedelta(minutes=1)).isoformat() + '+00:00'

                cursor.execute("""
                    UPDATE cards
                    SET fsrs_card = ?
                    WHERE id = ?
                """, (json.dumps(fsrs), card_id))
                updated_count += 1
            except Exception as e:
                print(f"  Warning: Failed to update card {card_id}: {e}")

        print(f"  Updated {updated_count} cards to Learning state")

        # Delete today's R0 lesson row (child table first) so a
        # reconnecting /rt session re-fetches due words instead of
        # startCompletedSession answering off the stale completed row.
        today_id = today_lesson_id()
        print(f"\nDeleting today's lesson row (lesson_id={today_id})...")
        try:
            cursor.execute(
                "DELETE FROM tool_requests WHERE lesson_id = ?", (today_id,)
            )
            print(f"  Deleted {cursor.rowcount} tool_requests rows")
            cursor.execute(
                "DELETE FROM lessons WHERE lesson_id = ?", (today_id,)
            )
            print(f"  Deleted {cursor.rowcount} lessons rows")
        except sqlite3.OperationalError as e:
            # A pre-Phase-13 DB without these tables must not fail the reset.
            print(f"  [SKIP] Lesson tables not available ({e}); skipping lesson-row deletion")

        conn.commit()

        # Verify results
        print("\n=== Verify Results ===")
        cursor.execute("""
            SELECT id, word, fsrs_card, fetch_locked_until
            FROM cards
            LIMIT 3
        """)
        print("Updated state:")
        for row in cursor.fetchall():
            fsrs = json.loads(row[2]) if row[2] else {}
            print(f"  ID={row[0]}, word={row[1]}, state={fsrs.get('state')}, due={fsrs.get('due')}, locked={row[3]}")

        # Check due cards
        cursor.execute("""
            SELECT COUNT(*) FROM cards
            WHERE fetch_locked_until IS NULL
        """)
        due_count = cursor.fetchone()[0]
        print(f"\n[OK] {due_count} words now available (unlocked)")

        # Lesson-row verification: list remaining rows and state whether
        # today's id is gone (expected: absent).
        try:
            print("\nRemaining lessons rows:")
            cursor.execute(
                "SELECT lesson_id, current_index, created_at"
                " FROM lessons ORDER BY created_at"
            )
            rows = cursor.fetchall()
            if not rows:
                print("  (none)")
            for row in rows:
                print(f"  lesson_id={row[0]}, current_index={row[1]}, created_at={row[2]}")
            if any(r[0] == today_id for r in rows):
                print(f"[WARN] Today's lesson row {today_id} is STILL PRESENT (expected absent)")
            else:
                print(f"[OK] Today's lesson row {today_id} is absent (fresh lesson on reconnect)")
        except sqlite3.OperationalError as e:
            print(f"  [SKIP] lessons table not available ({e}); skipping lesson verification")

        conn.close()

        print("\n[OK] Reset complete! Ready for testing")
        return True

    except sqlite3.Error as e:
        print(f"[ERROR] Database error: {e}")
        return False
    except Exception as e:
        print(f"[ERROR] Error: {e}")
        return False

if __name__ == "__main__":
    success = reset_vocabulary()
    sys.exit(0 if success else 1)
