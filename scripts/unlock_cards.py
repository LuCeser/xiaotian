import sqlite3
import pathlib

conn = sqlite3.connect(str(pathlib.Path(__file__).resolve().parent.parent / '.hta-data' / 'vocab.db'))
c = conn.cursor()

# Clear all fetch locks
c.execute("UPDATE cards SET fetch_locked_until = NULL")
print(f"Cleared locks on {c.rowcount} cards")

conn.commit()

# Verify
c.execute("SELECT id, word, fetch_locked_until FROM cards")
for row in c.fetchall():
    print(f"  {row[0]}: {row[1]} - lock={row[2]}")

conn.close()
