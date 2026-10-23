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
const {
  installRtObserver, waitWs, controls, playbackState, playedBeforeAck, listeningAfterPlayback,
} = require('./helpers/rt-observe');
const { installFakeMic, playPcm16, tonePcm16Base64 } = require('./helpers/fake-mic');

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

/** The log's next insertion index — a round's waits match ONLY entries at
 *  or after its mark, so a later round can never be satisfied by an
 *  earlier round's messages (review P1: cumulative matching let a
 *  still-playing or never-played round pass as "fully acked"). */
async function logMark(page) {
  const view = await controls(page);
  return view.length ? view[view.length - 1].i + 1 : 0;
}

/** Every announcement sentence acked naturally (production onended → ack),
 *  round-scoped by `from`: only entries at/after the mark belong to this
 *  round, each acked after its own audio_end on the wire. `sentences` (when
 *  > 0) pins the round's sentence count, so the wait completes exactly when
 *  the WHOLE round played — never after its first sentence.
 *  @param {{from?: number, sentences?: number, timeoutMs?: number}} [opts] */
async function waitFullyAcked(page, label, opts = {}) {
  const { from = 0, sentences = 0, timeoutMs = 30000 } = opts;
  await waitWs(page, (log) => {
    const at = (dir, type) => (e) => e.i >= from && e.dir === dir && e.type === type;
    const starts = log.filter(at('in', 'audio_start'));
    const ends = log.filter(at('in', 'audio_end'));
    const acks = log.filter(at('out', 'sentence_ack'));
    if (sentences > 0 ? starts.length !== sentences : starts.length === 0) return false;
    if (ends.length !== starts.length || acks.length !== starts.length) return false;
    return starts.every((s) => {
      const end = ends.find((e) => e.msg.sentence_id === s.msg.sentence_id);
      const ack = acks.find((e) => e.msg.sentence_id === s.msg.sentence_id);
      return !!end && !!ack && ack.i > end.i;
    });
  }, { timeoutMs, label: label + ' (fully acked)' });
}

/** After a fully-acked announcement the FSM settles listening (echo tail
 *  starts) — require every matching ACK and reject any early resume. */
async function waitSettledListening(page, label, opts = {}) {
  const { from = 0 } = opts;
  await waitWs(page, (log) => {
    const round = log.filter((e) => e.i >= from);
    const starts = round.filter((e) => e.dir === 'in' && e.type === 'audio_start');
    if (!starts.length) return false;
    const epoch = starts[0].msg.epoch;
    if (!starts.every((e) => e.msg.epoch === epoch)) return false;
    return listeningAfterPlayback(round, epoch, starts.map((e) => e.msg.sentence_id));
  }, { label: label + ' (settled listening)' });
}

/** Announcement that hands control back: fully acked AND listening settled. */
async function waitAnnouncedAndListening(page, label, opts = {}) {
  await waitFullyAcked(page, label, opts);
  await waitSettledListening(page, label, opts);
}

/** Real-playback acceptance (#66's helpers): every sentence announced so
 *  far on THIS page load really played — the production player's own
 *  onended precedes each ack and it discarded no chunk (wire counts alone
 *  accept audio that was dropped before it ever played). */
async function expectReallyPlayed(page, label) {
  const [playback, all] = await Promise.all([playbackState(page), controls(page)]);
  const starts = all.filter((e) => e.dir === 'in' && e.type === 'audio_start');
  expect(starts.length, label + ': sentences were announced').toBeGreaterThan(0);
  for (const s of starts) {
    expect(playedBeforeAck(playback.trace, s.msg.epoch, s.msg.sentence_id),
      label + ': onended before ack for ' + s.msg.epoch + ':' + s.msg.sentence_id).toBe(true);
  }
  expect(playback.droppedChunks, label + ': no audio chunk was discarded by the production player').toBe(0);
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
    await waitAnnouncedAndListening(page, 'greeting', { sentences: 3 });
    await expectReallyPlayed(page, 'greeting');
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
    await waitAnnouncedAndListening(page, 'greeting', { sentences: 3 });
    await expectReallyPlayed(page, 'greeting');
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
    await waitFullyAcked(page, 'no-due explanation', { sentences: 1 });
    await expectReallyPlayed(page, 'no-due explanation');
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
    await waitFullyAcked(page, 'second no-due explanation', { sentences: 1 });
    await expectReallyPlayed(page, 'second no-due explanation');
    await waitWs(page, (log) => log.some((e) => e.dir === 'in' && e.type === 'session_ended' && e.msg.reason === 'no_due_words'),
      { timeoutMs: 30000, label: 'second session_ended(no_due_words)' });
    expect((await svc.listLessons()).body.lessons).toHaveLength(0);
    expect((await stub.state()).violations).toEqual([]);
  } finally {
    await stub.reset();
    await svc.stop();
  }
});

// J06 — both subjects hold due words and the child answers the REAL ask by
// VOICE (the synthetic mic replaces only the capture source; the production
// RTCapture → VAD → ASR → keyword-binding chain runs untouched). The
// subject decision belongs to the child's words, matched mechanically —
// NEVER a classification LLM: an ambiguous answer (都可以) keeps the ask
// parked (re-asks, no lesson row), a clean 英语 binds the english
// classroom through the same interrupt_ack handshake as a button pick.
test('J06 voice subject answer: ambiguous keeps asking (no LLM), 英语 binds english', async ({ page }) => {
  test.setTimeout(90_000);
  const svc = await startSpecService();
  await installRtObserver(page);
  await installFakeMic(page);
  try {
    expect((await svc.seedWords({ category: 'chinese', unit: 'e2e-m1-j06-zh', entries: [{ word: '晨光' }, { word: '暮色' }] })).ok).toBe(true);
    expect((await svc.seedWords({ category: 'english', unit: 'e2e-m1-j06-en', entries: [{ word: 'e2e-m1-j06-a' }, { word: 'e2e-m1-j06-b' }] })).ok).toBe(true);
    // Ask (3) → retry after the ambiguous answer (2) → greeting (3), each
    // match-pinned; the ASR replies carry the child's two answers.
    expect((await stub.scripts({
      asr: [{ content: '都可以' }, { content: '英语' }],
      llm: [], image: [],
      tts: [
        { ms: 1200, match: '都有要默写的词语' },
        { ms: 1200, match: '哪一科' },
        { ms: 1200, match: '也可以点屏幕上的按钮' },
        { ms: 1200, match: '没听清' },
        { ms: 1200, match: '请说语文，或者英语' },
        ...greetingScripts(),
      ],
    })).ok).toBe(true);

    await page.goto(svc.url + 'tutor/');
    await page.locator('#rt-start').click();
    await expect(page.locator('#rt-subject-ask')).toBeVisible();
    await waitAnnouncedAndListening(page, 'ask', { sentences: 3 });
    await expectReallyPlayed(page, 'ask');

    // The child's FIRST answer — ambiguous. The round mark is taken BEFORE
    // the answer plays: everything announced after it is the retry (the ask
    // round already settled), and the retry wait matches ONLY from there —
    // the ask's messages can never satisfy it, so an unplayed or
    // still-playing retry goes red. Respect the production echo tail
    // (500 ms) before speaking; the capture graph carries the tone to the
    // server VAD and ONE scripted ASR call runs.
    const retryFrom = await logMark(page);
    await page.waitForTimeout(700);
    await playPcm16(page, tonePcm16Base64(2000), 16000);
    await waitWs(page, (log) => log.some((e) => e.dir === 'in' && e.type === 'transcript' && e.msg.text === '都可以'),
      { timeoutMs: 20000, label: "transcript of the ambiguous answer" });
    // The retry rides the SAME epoch (no interrupt handshake for a re-ask),
    // plays fully — both sentences — and hands control back; the ask stays
    // parked.
    await waitAnnouncedAndListening(page, 'retry', { from: retryFrom, sentences: 2 });
    await expectReallyPlayed(page, 'retry');
    const [askEpoch] = (await controls(page)).filter((e) => e.dir === 'in' && e.type === 'audio_start')
      .map((e) => e.msg.epoch);
    expect((await controls(page)).filter((e) => e.dir === 'in' && e.type === 'interrupt_ack')).toHaveLength(0);
    expect((await svc.listLessons()).body.lessons, 'ambiguous answer creates no classroom').toHaveLength(0);
    await expect(page.locator('#rt-subject-ask')).toBeVisible();

    // The SECOND answer — 英语 — binds through the real voice path. The
    // mark is taken BEFORE speaking: everything announced after it is the
    // english greeting, so the greeting wait below cannot race past the
    // first sentence nor borrow an earlier round's evidence.
    const greetingFrom = await logMark(page);
    await page.waitForTimeout(700);
    await playPcm16(page, tonePcm16Base64(2000), 16000);
    await waitWs(page, (log) => log.some((e) => e.dir === 'in' && e.type === 'transcript' && e.msg.text === '英语'),
      { timeoutMs: 20000, label: 'transcript of the 英语 answer' });
    await waitWs(page, (log) => log.some((e) => e.dir === 'in' && e.type === 'interrupt_ack'
      && e.msg.old_epoch === askEpoch && e.msg.new_epoch === askEpoch + 1),
    { timeoutMs: 20000, label: 'interrupt_ack epoch switch after the voice pick' });
    await waitWs(page, (log) => log.some((e) => e.dir === 'in' && e.type === 'session_ready'
      && e.msg.category === 'english'
      && Array.isArray(e.msg.lesson.words) && e.msg.lesson.words.length === 2
      && e.msg.lesson.words.every((w) => w === '')),
    { timeoutMs: 30000, label: 'session_ready for the voice-picked english classroom' });
    await waitAnnouncedAndListening(page, 'greeting', { from: greetingFrom, sentences: 3 });
    await expectReallyPlayed(page, 'greeting');

    // The binding was mechanical: two turns (one turn_start per utterance),
    // exactly two ASR calls, ZERO classification-LLM calls. Every answer
    // waited for a settled round, so the child NEVER interrupted the
    // teacher; and all 8 scripted TTS sentences (ask 3 + retry 2 + greeting
    // 3) were really spoken — none left unconsumed.
    const all = await controls(page);
    expect(all.filter((e) => e.type === 'turn_start')).toHaveLength(2);
    expect(all.filter((e) => e.dir === 'out' && e.type === 'interrupt'),
      '每轮回答都等到 listening，客户端从未打断').toHaveLength(0);
    const state = await stub.state();
    expect(state.requests.filter((q) => q.category === 'asr')).toHaveLength(2);
    expect(state.requests.filter((q) => q.category === 'llm'), '选科从不调用分类 LLM').toHaveLength(0);
    expect(state.requests.filter((q) => q.category === 'tts'),
      '询问3句+重问2句+开场3句的 TTS 脚本全部真实消费').toHaveLength(8);

    await expect(page.locator('#rt-subject-ask')).toBeHidden();
    const rows = (await svc.listLessons()).body.lessons;
    expect(rows).toHaveLength(1);
    expect(rows[0]).toMatchObject({ category: 'english', word_count: 2 });
    expect(state.violations).toEqual([]);
  } finally {
    await stub.reset();
    await svc.stop();
  }
});
