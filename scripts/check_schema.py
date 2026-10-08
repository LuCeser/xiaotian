import sqlite3
import pathlib
conn = sqlite3.connect(str(pathlib.Path(__file__).resolve().parent.parent / '.hta-data' / 'vocab.db'))
c = conn.cursor()
c.execute("PRAGMA table_info(cards)")
for row in c.fetchall():
    print(row)
conn.close()
