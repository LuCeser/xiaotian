import sqlite3
import pathlib
import json
from datetime import datetime, timezone

conn = sqlite3.connect(str(pathlib.Path(__file__).resolve().parent.parent / '.hta-data' / 'vocab.db'))
c = conn.cursor()

# Get all cards with their FSRS state
c.execute("SELECT id, word, category, unit, fsrs_card, fetch_locked_until FROM cards")
rows = c.fetchall()

print(f"Total cards: {len(rows)}")
print(f"Current UTC time: {datetime.now(timezone.utc).isoformat()}")
print()

for row in rows:
    card_id, word, category, unit, fsrs_json, lock_until = row
    fsrs = json.loads(fsrs_json)
    due = fsrs.get('due', 'N/A')
    state = fsrs.get('state', 'N/A')
    lock_status = f"LOCKED until {lock_until}" if lock_until else "unlocked"
    print(f"  {card_id}: {word} (cat={category}, unit={unit}, state={state}, due={due}, {lock_status})")

conn.close()
