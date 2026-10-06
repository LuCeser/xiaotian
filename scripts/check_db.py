import sqlite3
import pathlib
conn = sqlite3.connect(str(pathlib.Path(__file__).resolve().parent.parent / '.hta-data' / 'vocab.db'))
c = conn.cursor()
c.execute("SELECT name FROM sqlite_master WHERE type='table'")
tables = c.fetchall()
print("Tables:", tables)
for t in tables:
    c.execute(f"SELECT COUNT(*) FROM [{t[0]}]")
    print(f"  {t[0]}: {c.fetchone()[0]} rows")
    c.execute(f"SELECT * FROM [{t[0]}] LIMIT 3")
    for row in c.fetchall():
        print(f"    {row}")
conn.close()
