/**
 * Seed Go-owned lesson / receipt / practice rows into the hermetic parent
 * e2e SQLite DB (PARENT_E2E_DB). Uses the repo venv Python + sqlite3 so WAL
 * access matches the service (node:sqlite readOnly struggled against the
 * live writer). Never touches .hta-data / production paths.
 */
// @ts-check
const fs = require('node:fs');
const path = require('node:path');
const { execFileSync } = require('node:child_process');

const ROOT = path.resolve(__dirname, '..', '..', '..');

function dbPath() {
  const p = process.env.PARENT_E2E_DB;
  if (!p) {
    throw new Error('PARENT_E2E_DB unset — playwright.parent.config.js must export it');
  }
  if (/[/\\]\.hta-data([/\\]|$)/.test(p)) {
    throw new Error(`refusing to seed production-looking DB path: ${p}`);
  }
  return p;
}

function resolvePython() {
  if (process.env.HTA_PYTHON_INTERPRETER && fs.existsSync(process.env.HTA_PYTHON_INTERPRETER)) {
    return process.env.HTA_PYTHON_INTERPRETER;
  }
  const candidates = [
    path.join(ROOT, 'third_party', 'python', '.venv', 'bin', 'python'),
    path.join(ROOT, 'third_party', 'python', '.venv', 'Scripts', 'python.exe'),
    path.join(ROOT, '.venv', 'bin', 'python'),
    path.join(ROOT, 'venv', 'bin', 'python'),
  ];
  const found = candidates.find((c) => fs.existsSync(c));
  if (!found) {
    throw new Error('no venv python for seed-lessons helper');
  }
  return found;
}

function py(script, args = [], timeoutMs = 90_000) {
  return execFileSync(resolvePython(), ['-c', script, ...args], {
    encoding: 'utf8',
    cwd: ROOT,
    timeout: timeoutMs,
  });
}

/** Wait until the service has created/migrated the DB file. */
function waitForDb(timeoutMs = 60_000) {
  const p = dbPath();
  const start = Date.now();
  const script = `
import sqlite3, sys, time
db = sys.argv[1]
deadline = time.time() + float(sys.argv[2])
last = ''
while time.time() < deadline:
    try:
        con = sqlite3.connect(db, timeout=5)
        row = con.execute(
            "SELECT name FROM sqlite_master WHERE type='table' AND name='lessons'"
        ).fetchone()
        con.close()
        if row:
            print('ok')
            raise SystemExit(0)
        last = 'no lessons table yet'
    except Exception as e:
        last = str(e)
    time.sleep(0.1)
print('timeout: ' + last)
raise SystemExit(2)
`;
  try {
    const out = py(script, [p, String(timeoutMs / 1000)], timeoutMs + 5_000);
    if (!out.includes('ok')) {
      throw new Error(out.trim() || 'waitForDb failed');
    }
    return p;
  } catch (err) {
    const msg = (err && err.stdout) ? String(err.stdout)
      : (err && err.message) ? String(err.message) : String(err);
    throw new Error(`timed out waiting for migrated lessons table at ${p}: ${msg}`);
  }
}

function execSQL(sql, paramsJson) {
  waitForDb();
  const script = `
import sqlite3, sys, json
db, sql, params = sys.argv[1], sys.argv[2], json.loads(sys.argv[3])
con = sqlite3.connect(db, timeout=30)
con.execute('PRAGMA busy_timeout=5000')
for attempt in range(40):
    try:
        con.execute(sql, params)
        con.commit()
        print('ok')
        break
    except sqlite3.OperationalError as e:
        if 'locked' in str(e).lower() and attempt < 39:
            import time; time.sleep(0.05)
            continue
        raise
con.close()
`;
  py(script, [dbPath(), sql, JSON.stringify(paramsJson)]);
}

const CARDS_THREE = JSON.stringify([
  { word: 'alpha', meaning: 'α', example: 'alpha one' },
  { word: 'bravo', meaning: 'β', example: 'bravo two' },
  { word: 'charlie', meaning: 'γ', example: 'charlie three' },
]);

/**
 * @param {{
 *   lessonId: string,
 *   stage?: string,
 *   unchecked?: number,
 *   currentIndex?: number,
 *   cardsJson?: string,
 *   verdictsJson?: string,
 *   outcomesJson?: string,
 *   createdAt?: string,
 * }} opts
 */
function seedLesson(opts) {
  const {
    lessonId,
    stage = 'submitted',
    unchecked = 0,
    currentIndex = 3,
    cardsJson = CARDS_THREE,
    verdictsJson = JSON.stringify([
      { word_index: 0, correct: true, manual: true },
      { word_index: 1, correct: false, manual: true },
      { word_index: 2, correct: true, manual: true },
    ]),
    outcomesJson = '{}',
    createdAt = '2026-10-14 12:00:00',
  } = opts;
  execSQL(
    `INSERT OR REPLACE INTO lessons
      (lesson_id, words, cards, current_index, version, stage, outcomes, verdicts, unchecked, created_at, updated_at)
     VALUES (?, ?, ?, ?, 2, ?, ?, ?, ?, ?, ?)`,
    [
      lessonId,
      '["alpha","bravo","charlie"]',
      cardsJson,
      currentIndex,
      stage,
      outcomesJson,
      verdictsJson,
      unchecked,
      createdAt,
      createdAt,
    ],
  );
}

/**
 * @param {{
 *   lessonId: string,
 *   requestId: string,
 *   kind: string,
 *   status: 'pending'|'saved'|'failed',
 *   requested?: string[],
 *   createdAt?: string,
 * }} opts
 */
function seedReceipt(opts) {
  const requested = JSON.stringify(opts.requested || ['alpha', 'bravo', 'charlie']);
  const updated = opts.status === 'saved' ? requested : '[]';
  const completedAt = opts.status === 'pending' ? '' : opts.createdAt || '2026-10-14 12:01:00';
  execSQL(
    `INSERT INTO lesson_save_receipts
      (lesson_id, request_id, kind, requested, updated, missing, status, created_at, completed_at)
     VALUES (?, ?, ?, ?, ?, '[]', ?, ?, ?)`,
    [
      opts.lessonId,
      opts.requestId,
      opts.kind,
      requested,
      updated,
      opts.status,
      opts.createdAt || '2026-10-14 12:00:00',
      completedAt,
    ],
  );
}

/**
 * @param {{ lessonId: string, word: string, rating?: string, correct?: number, day?: string }} opts
 */
function seedPracticeWord(opts) {
  execSQL(
    `INSERT INTO practice_logs
      (lesson_id, word, rating, correct, hints, reviewed_at, reviewed_day)
     VALUES (?, ?, ?, ?, 0, datetime('now'), ?)`,
    [
      opts.lessonId,
      opts.word,
      opts.rating || 'good',
      opts.correct == null ? 1 : opts.correct,
      opts.day || '2026-10-14',
    ],
  );
}

/** @returns {number} */
function countReviewLogs() {
  waitForDb();
  const script = `
import sqlite3, sys
con = sqlite3.connect(sys.argv[1], timeout=30)
try:
    n = con.execute('SELECT COUNT(*) FROM review_logs').fetchone()[0]
except Exception:
    n = 0
print(n)
con.close()
`;
  const out = py(script, [dbPath()]);
  return parseInt(out.trim(), 10) || 0;
}

module.exports = {
  dbPath,
  waitForDb,
  seedLesson,
  seedReceipt,
  seedPracticeWord,
  countReviewLogs,
  CARDS_THREE,
};
