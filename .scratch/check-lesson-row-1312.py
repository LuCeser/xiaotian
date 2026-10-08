# 13-12 Task 1 provenance check: Go-side lessons row for the daily lesson.
# Scratch-only; NEVER committed (sanitized counts only go into CHECKPOINTS).
import sqlite3, os, json, sys

go_db = sys.argv[1] if len(sys.argv) > 1 else os.path.expanduser('~/.home-tutor-agent/vocab.db')
print('checking db:', go_db, '| exists:', os.path.exists(go_db))
con = sqlite3.connect(go_db)
cur = con.cursor()
tables = [r[0] for r in cur.execute("select name from sqlite_master where type='table'")]
print('tables:', tables)
if 'lessons' in tables:
    cols = [r[1] for r in cur.execute('pragma table_info(lessons)')]
    print('lessons cols:', cols)
    rows = list(cur.execute("select * from lessons where lesson_id like 'r0-%' order by created_at"))
    print('r0 lesson rows:', len(rows))
    for r in rows:
        d = dict(zip(cols, r))
        words = d.get('words')
        n = 0
        if words:
            try:
                parsed = json.loads(words)
                n = len(parsed) if isinstance(parsed, list) else 1
            except Exception:
                n = 1
        print('lesson:', d.get('lesson_id'), '| version:', d.get('version'),
              '| current_index:', d.get('current_index'),
              '| words_count:', n, '| created_at:', d.get('created_at'),
              '| updated_at:', d.get('updated_at'))
con.close()
