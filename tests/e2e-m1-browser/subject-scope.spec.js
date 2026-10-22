// subject-scope.spec.js — J04 / J06 / J07 (issue #58, e2e-test-plan §7 step
// 12; #55 regression): the subject-scope rules against a REAL isolated
// service and the REAL tutor page, under the plan's current ruling —
// 默写开始时不让模型猜学科；一科有词直接开始，两科都有就让孩子选:
//
//   J04  only ONE subject holds due words → the generic (auto) entry opens
//        that subject DIRECTLY: no subject_ask, real first-word playback,
//        and the classroom's subject+word list match what was seeded;
//   J07  an EXPLICIT subject entry (?category=…, the home doors) is never
//        overridden (no ask even when both subjects hold due words), the
//        10-word cap yields to what exists (3 seeded → 3 practiced), and a
//        subject with NO due words ends honestly (no_due_words) without
//        creating an empty lesson row or borrowing the other subject's
//        words;
//   J06  (added with the fake-mic step below) both subjects due, the child
//        answers the ask BY VOICE: an ambiguous answer keeps asking (no
//        lesson row, no classification LLM), a clean 英语 binds english.
//
// Per-test isolation: each test launches its own service+temp SQLite via
// spec-service.js (lesson rows are first-content-wins per subject — a
// shared instance could hand a later test an earlier test's words). The
// strict stub is shared: every test resets it in its finally block.
'use strict';

const { test, expect } = require('@playwright/test');
const { startSpecService, makeStubControl } = require('./helpers/spec-service');
const { installRtObserver, waitWs, controls } = require('./helpers/rt-observe');

const stub = makeStubControl();

// Greeting TTS scripts, match-pinned per sentence (splitSentences yields
// 你好呀！/ 今天我们有 N 个词要写。/ 听好哦：第 1 个词，X。 — any order or
// count drift is a stub violation, never a soft pass).
const greetingScripts = () => ([
  { ms: 1200, match: '你好呀' },
  { ms: 1200, match: '个词要写' },
  { ms: 1200, match: '第 1 个词' },
]);

/** Open a classroom on THIS test's service and return after session_ready. */
async function openClassroom(page, svc, readyPred, label) {
  await page.goto(svc.url + 'tutor/');
  await page.locator('#rt-start').click();
  await waitWs(page, (log) => log.some((e) => e.dir === 'in' && e.type === 'session_ready' && readyPred(e.msg)),
    { timeoutMs: 30000, label });
}

/** Every announcement sentence acked naturally (production onended → ack). */
async function waitFullyAcked(page, label, timeoutMs = 30000) {
  await waitWs(page, (log) => {
    const starts = log.filter((e) => e.dir === 'in' && e.type === 'audio_start');
    const ends = log.filter((e) => e.dir === 'in' && e.type === 'audio_end');
    const acks = log.filter((e) => e.dir === 'out' && e.type === 'sentence_ack');
    return starts.length > 0 && ends.length === starts.length && acks.length === starts.length;
  }, { timeoutMs, label: label + ' (fully acked)' });
}

/** After a fully-acked announcement the FSM settles listening (echo tail starts). */
async function waitSettledListening(page, label) {
  await waitWs(page, (log) => {
    const ends = log.filter((e) => e.dir === 'in' && e.type === 'audio_end');
    const lastEnd = ends.length ? ends[ends.length - 1].i : -1;
    return log.some((e) => e.i > lastEnd && e.dir === 'in' && e.type === 'state' && e.msg.value === 'listening');
  }, { label: label + ' (settled listening)' });
}

/** Announcement that hands control back: fully acked AND listening settled. */
async function waitAnnouncedAndListening(page, label) {
  await waitFullyAcked(page, label);
  await waitSettledListening(page, label);
}

test('J04 chinese-only due words: auto entry opens chinese directly, no ask, plays the first word', async ({ page }) => {
  test.setTimeout(90_000);
  const svc = await startSpecService();
  await installRtObserver(page);
  try {
    const seeded = ['晨曦', '暮霭'];
    const r = await svc.seedWords({ category: 'chinese', unit: 'e2e-m1-j04', entries: seeded.map((word) => ({ word })) });
    expect(r.ok, 'seed chinese').toBe(true);
    expect((await stub.scripts({ asr: [], llm: [], image: [], tts: greetingScripts() })).ok).toBe(true);

    await openClassroom(page, svc, (m) => m.category === 'chinese'
      && m.lesson.words.length === seeded.length && m.lesson.words.every((w) => w === ''),
    'session_ready chinese with the seeded words');

    // 不出现选科询问: the whole session carries no subject_ask control.
    expect((await controls(page)).filter((e) => e.type === 'subject_ask')).toHaveLength(0);

    // 实际播放首词: the greeting acks through the production player, and
    // its first-word sentence names a SEEDED word (科目和词表一致).
    await waitAnnouncedAndListening(page, 'greeting');
    const ttsTexts = (await stub.state()).requests.filter((q) => q.category === 'tts').map((q) => q.text);
    expect(ttsTexts.some((t) => /第 1 个词，(晨曦|暮霭)。/.test(t)), 'first word is a seeded chinese word').toBe(true);

    await expect(page.locator('#rt-progress-text')).toHaveText(/第 1 \/ 2 个词/);
    const rows = (await svc.listLessons()).body.lessons;
    expect(rows).toHaveLength(1);
    expect(rows[0]).toMatchObject({ category: 'chinese', word_count: 2 });
    expect((await controls(page)).filter((e) => e.type === 'turn_start')).toHaveLength(0);
    expect((await stub.state()).violations).toEqual([]);
  } finally {
    await stub.reset();
    await svc.stop();
  }
});

test('J07 explicit chinese entry: never overridden, no ask, only the 3 existing words', async ({ page }) => {
  test.setTimeout(90_000);
  const svc = await startSpecService();
  await installRtObserver(page);
  try {
    // BOTH subjects hold due words — auto would ask; the explicit door must not.
    const r = await svc.seedWords({ category: 'chinese', unit: 'e2e-m1-j07a', entries: [{ word: '巍峨' }, { word: '峭壁' }, { word: '蜿蜒' }] });
    expect(r.ok, 'seed chinese ×3').toBe(true);
    expect((await svc.seedWords({ category: 'english', unit: 'e2e-m1-j07a-en', entries: [{ word: 'e2e-m1-j07a-en' }] })).ok).toBe(true);
    expect((await stub.scripts({ asr: [], llm: [], image: [], tts: greetingScripts() })).ok).toBe(true);

    await page.goto(svc.url + 'tutor/?category=chinese');
    await page.locator('#rt-start').click();
    // The page declares the explicit subject on the wire, and the server
    // both honors it and echoes it back — 显式选择不被覆盖.
    await waitWs(page, (log) => log.some((e) => e.dir === 'out' && e.type === 'session_init' && e.msg.category === 'chinese'),
      { label: 'session_init carries the explicit chinese' });
    await waitWs(page, (log) => log.some((e) => e.dir === 'in' && e.type === 'session_ready'
      && e.msg.category === 'chinese' && e.msg.lesson.words.length === 3 && e.msg.lesson.words.every((w) => w === '')),
    { timeoutMs: 30000, label: 'session_ready chinese with 3 words' });

    // 上限 10 而只有 3 个 → 只练 3 个: no ask appeared, exactly 3 words.
    expect((await controls(page)).filter((e) => e.type === 'subject_ask')).toHaveLength(0);
    await waitAnnouncedAndListening(page, 'greeting');
    const rows = (await svc.listLessons()).body.lessons;
    expect(rows).toHaveLength(1);
    expect(rows[0]).toMatchObject({ category: 'chinese', word_count: 3 });
    expect((await stub.state()).violations).toEqual([]);
  } finally {
    await stub.reset();
    await svc.stop();
  }
});

test('J07 no due words: honest no_due_words end, no empty row, no cross-subject borrowing', async ({ page }) => {
  test.setTimeout(90_000);
  const svc = await startSpecService();
  await installRtObserver(page);
  try {
    // Chinese holds NOTHING due; english holds two — the declared chinese
    // classroom must end honestly rather than borrow the english words.
    expect((await svc.seedWords({ category: 'english', unit: 'e2e-m1-j07b-en', entries: [{ word: 'e2e-m1-j07b-a' }, { word: 'e2e-m1-j07b-b' }] })).ok).toBe(true);
    const oneNoDue = [{ ms: 1200, match: '没有要到期的单词' }];
    expect((await stub.scripts({ asr: [], llm: [], image: [], tts: oneNoDue })).ok).toBe(true);

    await page.goto(svc.url + 'tutor/?category=chinese');
    await page.locator('#rt-start').click();
    await waitWs(page, (log) => log.some((e) => e.dir === 'in' && e.type === 'session_ready'
      && e.msg.category === 'chinese' && Array.isArray(e.msg.lesson.words) && e.msg.lesson.words.length === 0),
    { timeoutMs: 30000, label: 'session_ready chinese with an EMPTY word table' });

    // The explanation plays and acks naturally, THEN the session ends with
    // the no-due reason (a natural ending never settles listening — the
    // session is over) — 不建空课堂, 不跨科借词.
    await waitFullyAcked(page, 'no-due explanation');
    await waitWs(page, (log) => log.some((e) => e.dir === 'in' && e.type === 'session_ended' && e.msg.reason === 'no_due_words'),
      { timeoutMs: 30000, label: 'session_ended(no_due_words) after the acked explanation' });
    expect((await svc.listLessons()).body.lessons, 'no empty lesson row').toHaveLength(0);
    const en = (await svc.listWords({ category: 'english' })).body.words.map((w) => w.word);
    expect(en).toEqual(['e2e-m1-j07b-a', 'e2e-m1-j07b-b']); // untouched by the empty classroom

    // Second session — the auto entry with NEITHER subject due: the english
    // landing + the same honest ending (resolveAutoSubject's neither-branch).
    const del = await fetch(svc.url + 'api/v1/words', { method: 'DELETE', headers: { 'content-type': 'application/json' }, body: JSON.stringify({ category: 'english', unit: 'e2e-m1-j07b-en' }) });
    expect(del.ok, 'drop the english unit').toBe(true);
    expect((await stub.scripts({ asr: [], llm: [], image: [], tts: oneNoDue })).ok).toBe(true);

    await page.goto(svc.url + 'tutor/');
    await page.locator('#rt-start').click();
    await waitWs(page, (log) => log.some((e) => e.dir === 'out' && e.type === 'session_init' && e.msg.category === 'auto'),
      { label: 'session_init declares auto' });
    await waitWs(page, (log) => log.some((e) => e.dir === 'in' && e.type === 'session_ready'
      && e.msg.category === 'english' && e.msg.lesson.words.length === 0),
    { timeoutMs: 30000, label: 'auto with nothing due lands on the english no-due end' });
    await waitFullyAcked(page, 'second no-due explanation');
    await waitWs(page, (log) => log.some((e) => e.dir === 'in' && e.type === 'session_ended' && e.msg.reason === 'no_due_words'),
      { timeoutMs: 30000, label: 'second session_ended(no_due_words)' });
    expect((await svc.listLessons()).body.lessons).toHaveLength(0);
    expect((await stub.state()).violations).toEqual([]);
  } finally {
    await stub.reset();
    await svc.stop();
  }
});
