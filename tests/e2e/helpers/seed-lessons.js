/**
 * Seed Go-owned lesson / receipt / practice rows into the hermetic parent
 * e2e SQLite DB (PARENT_E2E_DB). Used by family-journey browser specs so
 * parent.html assertions do not depend on a full realtime voice lesson.
 *
 * Iron rule: only the isolated temp DB from playwright.parent.config.js —
 * never .hta-data or any production path.
 */
// @ts-check
const fs = require('node:fs');
const { DatabaseSync } = require('node:sqlite');

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

function sleepMs(ms) {
  const end = Date.now() + ms;
  while (Date.now() < end) {
    /* spin — keep helper free of Atomics/SharedArrayBuffer requirements */
  }
}

/** Wait until the service has created/migrated the DB file. */
function waitForDb(timeoutMs = 60_000) {
  const p = dbPath();
  const start = Date.now();
  while (Date.now() - start < timeoutMs) {
    if (fs.existsSync(p)) {
      try {
        const db = new DatabaseSync(p, { readOnly: true });
        const row = db.prepare(
          "SELECT name FROM sqlite_master WHERE type='table' AND name='lessons'",
        ).get();
        db.close();
        if (row) return p;
      } catch {
        // migrate in progress
      }
    }
    sleepMs(100);
  }
  throw new Error(`timed out waiting for migrated lessons table at ${p}`);
}

function openRw() {
  waitForDb();
  // Service holds WAL; retry briefly on SQLITE_BUSY.
  let lastErr;
  for (let i = 0; i < 40; i++) {
    try {
      const db = new DatabaseSync(dbPath());
      db.exec('PRAGMA busy_timeout=5000');
      return db;
    } catch (err) {
      lastErr = err;
      sleepMs(50);
    }
  }
  throw lastErr || new Error('openRw failed');
}

function withRetry(fn) {
  let lastErr;
  for (let i = 0; i < 40; i++) {
    const db = openRw();
    try {
      fn(db);
      db.close();
      return;
    } catch (err) {
      try { db.close(); } catch { /* ignore */ }
      lastErr = err;
      sleepMs(50);
    }
  }
  throw lastErr || new Error('withRetry failed');
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
  withRetry((db) => {
    db.prepare(
      `INSERT OR REPLACE INTO lessons
        (lesson_id, words, cards, current_index, version, stage, outcomes, verdicts, unchecked, created_at, updated_at)
       VALUES (?, ?, ?, ?, 2, ?, ?, ?, ?, ?, ?)`,
    ).run(
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
    );
  });
}

/**
 * @param {{
 *   lessonId: string,
 *   requestId: string,
 *   kind: string,
 *   status: 'pending'|'saved'|'failed',
 *   requested?: string[],
 * }} opts
 */
function seedReceipt(opts) {
  const requested = JSON.stringify(opts.requested || ['alpha', 'bravo', 'charlie']);
  const updated = opts.status === 'saved' ? requested : '[]';
  const completedAt = opts.status === 'pending' ? '' : opts.createdAt || '2026-10-14 12:01:00';
  withRetry((db) => {
    db.prepare(
      `INSERT INTO lesson_save_receipts
        (lesson_id, request_id, kind, requested, updated, missing, status, created_at, completed_at)
       VALUES (?, ?, ?, ?, ?, '[]', ?, ?, ?)`,
    ).run(
      opts.lessonId,
      opts.requestId,
      opts.kind,
      requested,
      updated,
      opts.status,
      opts.createdAt || '2026-10-14 12:00:00',
      completedAt,
    );
  });
}

/**
 * @param {{ lessonId: string, word: string, rating?: string, correct?: number, day?: string }} opts
 */
function seedPracticeWord(opts) {
  withRetry((db) => {
    db.prepare(
      `INSERT INTO practice_logs
        (lesson_id, word, rating, correct, hints, reviewed_at, reviewed_day)
       VALUES (?, ?, ?, ?, 0, datetime('now'), ?)`,
    ).run(
      opts.lessonId,
      opts.word,
      opts.rating || 'good',
      opts.correct == null ? 1 : opts.correct,
      opts.day || '2026-10-14',
    );
  });
}

/** @returns {number} */
function countReviewLogs() {
  const db = openRw();
  try {
    const row = db.prepare('SELECT COUNT(*) AS c FROM review_logs').get();
    db.close();
    return row ? Number(row.c) : 0;
  } catch {
    db.close();
    return 0;
  }
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
