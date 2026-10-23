// subject-playback.spec.js — J05, the m1 browser line's first core B-layer
// acceptance (issue #58, e2e-test-plan §7 step 6): BOTH subjects hold due
// words, the child taps a pick button MID-ASK, and the REAL sequence must
// hold — real epoch switch (interrupt_ack old→old+1), the new classroom's
// opening audio under the NEW epoch with NO re-sent turn_start (the exact
// historical fake-green: a mock补发 turn_start made both sides pass while
// the browser dropped the real opening), and the new opening ACKing through
// the production player only (real onended → sentence_ack — plan §2 rule 2:
// the test never sends, calls or fakes an ack).
//
// Real parts: /tutor/ page, rt-client/rt-playback, real Go /rt + Python +
// SQLite (per-run temp DB), real REST seeding. Replaced parts: vendor
// answers (strict stub, tone PCM), mic source (silent fake device — see
// playwright.m1.config.js).
'use strict';

const { test, expect } = require('@playwright/test');
const {
  installRtObserver, waitWs, controls, playbackState, playedBeforeAck, listeningAfterPlayback,
} = require('./helpers/rt-observe');
const { startSpecService, makeStubControl } = require('./helpers/spec-service');

const stub = makeStubControl();
let svc;

test.beforeEach(async () => {
  await stub.reset();
  svc = await startSpecService();
});
test.afterEach(async () => {
  try { if (svc) await svc.stop(); } finally {
    svc = null;
    await stub.reset();
  }
});

async function stubScripts(def) {
  const res = await stub.scripts(def);
  expect(res.ok, 'strict stub scripts install').toBeTruthy();
}

function stubState() {
  return stub.state();
}

// Both sentences families get the SAME tone length: FIFO + a mid-playback
// click make the ask's consumed count nondeterministic (1..3), so the
// leftover ask entries must serve the greeting harmlessly. The click waits
// for a real binary frame, so 1.2 s still guarantees mid-playback selection
// while all three greeting sentences remain well inside the ACK deadline.
function toneEntries(count) {
  return Array.from({ length: count }, () => ({ ms: 1200 }));
}

test('J05 mid-ask button pick: real epoch switch, no turn_start, natural new-opening ack', async ({ page }) => {
  await installRtObserver(page);
  const chineseWords = ['晨光', '暮色'];
  const englishWords = ['e2e-m1-english-a', 'e2e-m1-english-b'];

  // Seed BOTH subjects through the real REST → Python → SQLite path (the
  // ask's precondition); a fresh per-run DB guarantees due-now cards.
  const seed = async (category, unit, words) => {
    const res = await svc.seedWords({ category, unit, entries: words.map((word) => ({ word })) });
    expect(res.ok, 'seed ' + category).toBeTruthy();
  };
  await seed('chinese', 'e2e-m1-subject', chineseWords);
  await seed('english', 'e2e-m1-subject-en', englishWords);

  // Ask announcement = 3 sentences, greeting = 3; click timing makes the
  // ask consume 1..3, so install 6 and assert zero stub violations at the end.
  await stubScripts({ asr: [], llm: [], image: [], tts: toneEntries(6) });
  expect((await stubState()).violations).toEqual([]);

  const lessonsNow = async () => (await svc.listLessons()).body.lessons;
  expect(await lessonsNow(), 'no classroom row before the session').toHaveLength(0);

  await page.goto(svc.url + 'tutor/');
  await page.locator('#rt-start').click();

  // The teacher is ASKING: subject_ask arrived and its audio is really
  // streaming (first binary frame down the socket, epoch E0's audio_start).
  await waitWs(page, (log) => log.some((e) => e.dir === 'in' && e.type === 'subject_ask')
    && log.some((e) => e.dir === 'in' && e.type === 'audio_start')
    && log.some((e) => e.dir === 'in' && e.kind === 'binary' && e.bytes > 0),
  { label: 'ask audio streaming' });
  await expect(page.locator('#rt-subject-ask')).toBeVisible();

  const before = await controls(page);
  const askEpoch = before.find((e) => e.dir === 'in' && e.type === 'audio_start').msg.epoch;
  const nextEpoch = askEpoch + 1;
  const at = (dir, type, epoch) => (e) => e.dir === dir && e.type === type
    && (!epoch || (e.msg && e.msg.epoch === epoch));

  // J05: 未选择前不创建课堂 — still zero rows while parked on the ask.
  expect(await lessonsNow(), 'no classroom row while the ask is parked').toHaveLength(0);

  // The pick, MID-PLAYBACK of the ask (J05's 询问中点击).
  await page.locator('#rt-subject-zh').click();

  // Real epoch switch: interrupt_ack old→old+1 with old === the ask epoch.
  await waitWs(page, (log) => log.some((e) => e.dir === 'in' && e.type === 'interrupt_ack'
    && e.msg.old_epoch === askEpoch && e.msg.new_epoch === nextEpoch),
  { label: 'interrupt_ack epoch switch' });

  // New classroom under the PICKED subject only; student mode keeps words
  // masked (the server is the masking authority — never mode=test crutches).
  await waitWs(page, (log) => log.some((e) => e.dir === 'in' && e.type === 'session_ready'
    && e.msg.category === 'chinese'
    && Array.isArray(e.msg.lesson.words) && e.msg.lesson.words.length === 2
    && e.msg.lesson.words.every((w) => w === '')),
  { label: 'session_ready for the picked chinese classroom' });

  // The new opening's every sentence ACKs NATURALLY: production onended per
  // source + audio_end per sentence → the page's own sentence_ack frames.
  await waitWs(page, (log) => {
    const starts = log.filter(at('in', 'audio_start', nextEpoch));
    const ends = log.filter(at('in', 'audio_end', nextEpoch));
    const acks = log.filter(at('out', 'sentence_ack', nextEpoch));
    if (starts.length !== 3 || ends.length !== 3 || acks.length !== 3) return false;
    const ids = starts.map((e) => e.msg.sentence_id);
    return new Set(ids).size === 3 && ids.every((sid) => {
      const end = ends.find((e) => e.msg.sentence_id === sid);
      const ack = acks.find((e) => e.msg.sentence_id === sid);
      return end && ack && ack.i > end.i;
    });
  }, { timeoutMs: 30000, label: 'new opening fully acked by the production player' });

  // Vendor-side evidence ties those three naturally played sentences to
  // the selected subject. The greeting must speak one of the actual Chinese
  // fixture words; picking an English card or stopping after only the hello
  // sentence cannot pass on the coincidental two-word count.
  const ttsTexts = (await stubState()).requests
    .filter((r) => r.category === 'tts').map((r) => r.text);
  const greetingTts = ttsTexts.filter((text) => text.includes('你好呀')
    || text.includes('个词要写') || text.includes('第 1 个词'));
  expect(greetingTts, 'the selected classroom emits its complete three-sentence greeting').toHaveLength(3);
  expect(greetingTts.some((text) => chineseWords.some((word) => text.includes(word))),
    'the first dictated word comes from the selected Chinese fixture').toBe(true);
  expect(ttsTexts.some((text) => englishWords.some((word) => text.includes(word))),
    'no English fixture word reaches TTS').toBe(false);

  // The server ACCEPTED those acks: it settles listening only after the
  // opening's real playback completion (audio_end ≠ completion).
  const newOpeningIds = (await controls(page)).filter(at('in', 'audio_start', nextEpoch))
    .map((e) => e.msg.sentence_id);
  await waitWs(page, (log) => listeningAfterPlayback(log, nextEpoch, newOpeningIds),
    { label: 'server settled listening only after every matching ack' });

  // Wire-level invariants (the whole flow legitimately carries zero
  // turn_start: announcements never open a turn — 无补发 turn_start).
  const after = await controls(page);
  expect(after.filter((e) => e.type === 'turn_start'), 'no turn_start anywhere').toHaveLength(0);
  expect(after.filter((e) => e.dir === 'out' && e.type === 'sentence_ack' && e.msg.epoch === askEpoch),
    'the cancelled ask never acked').toHaveLength(0);
  expect(after.filter((e) => e.dir === 'out' && e.type === 'interrupt'),
    'the silent mic never fired a child-side interrupt').toHaveLength(0);

  // Player-side evidence: the epoch bump stopped the old queue (stopNow),
  // the old epoch never acks, and every new-epoch sentence does.
  const playback = await playbackState(page);
  const trace = playback.trace;
  expect(trace).toContain('stopNow');
  expect(trace.filter((t) => t.startsWith('ack:' + askEpoch + ':'))).toHaveLength(0);
  const newSids = after.filter(at('in', 'audio_start', nextEpoch)).map((e) => e.msg.sentence_id);
  expect(newSids).toHaveLength(3);
  for (const sid of newSids) {
    expect(playedBeforeAck(trace, nextEpoch, sid), 'onended before ack for ' + sid).toBe(true);
  }
  expect(playback.droppedChunks, 'no audio chunk was discarded by the production player').toBe(0);

  // UI + persistence together (plan rule 7): the ask panel is gone, the
  // picked classroom renders its progress, and exactly ONE lesson row
  // exists — chinese, its 2 words only.
  await expect(page.locator('#rt-subject-ask')).toBeHidden();
  await expect(page.locator('#rt-progress-text')).toHaveText(/第 1 \/ 2 个词/);
  const rows = await lessonsNow();
  expect(rows).toHaveLength(1);
  expect(rows[0]).toMatchObject({ category: 'chinese', word_count: 2, lesson_id: 'e2e-m1-browser-chinese' });

  // End the session before the 60 s word timer can speak again.
  await page.locator('#rt-end').click();
  await waitWs(page, (log) => log.some((e) => e.dir === 'in' && e.type === 'session_ended'),
    { label: 'session_ended after explicit end' });

  const state = await stubState();
  expect(state.violations, 'the strict stub saw only scripted traffic').toEqual([]);
});
