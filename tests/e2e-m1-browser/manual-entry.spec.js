// manual-entry.spec.js — J01 (issue #58, e2e-test-plan §7 step 11): the
// parent manually enters 语文 and 英语 words on the REAL parent page, and
// the journey must hold end to end against a REAL isolated service:
//
//   保存后刷新可查 + 词面/释义/学科正确 (UI browse + REST persistence),
//   录入到开课之间不直接预置课堂行 (zero lesson rows while only entered),
//   then 开课、真实播放 — both subjects hold due words, the child answers
//   the REAL subject ask with the 语文 button, and the opened classroom's
//   greeting streams and ACKs through the production player only (real
//   onended → sentence_ack — plan §2 rule 2; the test never sends, calls or
//   fakes an ack, and never fabricates a server control).
//
// Isolation: this spec launches its OWN service+temp SQLite (spec-service)
// — the shared instance hands out one lesson row per subject per run, and
// J01 must create its own. The strict stub is shared: reset in finally.
// Replaced parts: vendor answers only (tone TTS); mic = config silent device.
'use strict';

const { test, expect } = require('@playwright/test');
const { startSpecService, makeStubControl } = require('./helpers/spec-service');
const { installRtObserver, waitWs, controls } = require('./helpers/rt-observe');

const stub = makeStubControl();

const ZH_UNIT = 'e2e-m1-entry-zh';
const EN_UNIT = 'e2e-m1-entry-en';
const ZH_WORDS = [
  { word: '晓月', meaning: '拂晓时的月亮' },
  { word: '静谧', meaning: '安静的样子' },
];
const EN_WORDS = [{ word: 'luminous', meaning: '发光的，明亮的' }];

test('J01 manual entry: save→refresh→query correct, no lesson row pre-seeded, real classroom plays the entered words', async ({ page }) => {
  test.setTimeout(90_000);
  const svc = await startSpecService();
  await installRtObserver(page);
  try {
    // ---------------- 1. 家长分别手动录入语文、英语词条 ----------------
    await page.goto(svc.url + 'parent.html');
    await page.getByRole('tab', { name: '词库' }).click();
    // Completion signal per add = that word's browse row appearing (real
    // POST → Python → SQLite, then the no-reload list refresh — both
    // directions proven). The success TOAST is asserted only on the first
    // add: it is a 2.5 s transient, so on consecutive adds a leftover toast
    // can satisfy a matcher while the new POST is still in flight.
    const addOne = async (radioName, { word, meaning }, unit) => {
      await page.getByRole('radio', { name: radioName }).check();
      await page.getByRole('textbox', { name: '单词', exact: true }).fill(word);
      await page.getByRole('textbox', { name: '意思', exact: true }).fill(meaning);
      await page.getByRole('combobox', { name: '单元', exact: true }).fill(unit);
      await page.getByRole('button', { name: '添加单词' }).click();
      await expect(page.locator(`.word-row[data-word="${word}"]`), 'row for ' + word)
        .toBeVisible({ timeout: 15_000 });
    };
    await addOne('中文', ZH_WORDS[0], ZH_UNIT);
    await expect(page.locator('.toast-success')).toContainText('已添加');
    await addOne('中文', ZH_WORDS[1], ZH_UNIT);
    await addOne('英语', EN_WORDS[0], EN_UNIT);

    // Every entered word renders with its 词面 intact (no reload happened).
    for (const w of [...ZH_WORDS, ...EN_WORDS]) {
      await expect(page.locator(`.word-row[data-word="${w.word}"] .word-text`)).toHaveText(w.word);
    }

    // ---------------- 2. 保存后刷新可查 + 学科正确 ----------------
    await page.reload();
    await page.getByRole('tab', { name: '词库' }).click();
    const pills = page.locator('[aria-label="分类筛选"]');
    await pills.getByRole('button', { name: '中文', exact: true }).click();
    await expect(page.locator(`.word-row[data-word="${ZH_WORDS[0].word}"]`)).toBeVisible();
    await expect(page.locator(`.word-row[data-word="${EN_WORDS[0].word}"]`)).toHaveCount(0);
    await pills.getByRole('button', { name: '英语', exact: true }).click();
    await expect(page.locator(`.word-row[data-word="${EN_WORDS[0].word}"]`)).toBeVisible();
    await expect(page.locator(`.word-row[data-word="${ZH_WORDS[0].word}"]`)).toHaveCount(0);

    // REST persistence agrees on 词面/释义/学科 (word/meaning/category).
    const zh = (await svc.listWords({ category: 'chinese' })).body.words;
    const en = (await svc.listWords({ category: 'english' })).body.words;
    expect(zh.map((w) => [w.word, w.meaning, w.category])).toEqual(ZH_WORDS.map((w) => [w.word, w.meaning, 'chinese']));
    expect(en.map((w) => [w.word, w.meaning, w.category])).toEqual(EN_WORDS.map((w) => [w.word, w.meaning, 'english']));

    // 录入到开课之间不直接预置课堂行: entries alone never create a lesson.
    expect((await svc.listLessons()).body.lessons).toHaveLength(0);

    // ---------------- 3. 开课、真实播放 ----------------
    // Ask = 3 sentences, greeting = 3 — each pinned by substring so any
    // order/count drift is a stub violation, not a soft pass.
    const r = await stub.scripts({
      asr: [], llm: [], image: [],
      tts: [
        { ms: 1200, match: '都有要默写的词语' },
        { ms: 1200, match: '哪一科' },
        { ms: 1200, match: '也可以点屏幕上的按钮' },
        { ms: 1200, match: '你好呀' },
        { ms: 1200, match: '个词要写' },
        { ms: 1200, match: '第 1 个词' },
      ],
    });
    expect(r.ok, 'stub scripts install').toBe(true);

    await page.goto(svc.url + 'tutor/');
    await page.locator('#rt-start').click();
    await expect(page.locator('#rt-subject-ask')).toBeVisible();
    // The ask finishes NATURALLY (production ack per sentence), then the
    // server settles listening — the click below rides a fully-acked ask.
    await waitWs(page, (log) => {
      const starts = log.filter((e) => e.dir === 'in' && e.type === 'audio_start');
      const ends = log.filter((e) => e.dir === 'in' && e.type === 'audio_end');
      const acks = log.filter((e) => e.dir === 'out' && e.type === 'sentence_ack');
      return starts.length === 3 && ends.length === 3 && acks.length === 3
        && log.some((e) => e.i > (ends.at(-1) ? ends.at(-1).i : -1) && e.dir === 'in' && e.type === 'state' && e.msg.value === 'listening');
    }, { timeoutMs: 30000, label: 'ask fully acked then listening' });
    expect((await svc.listLessons()).body.lessons, 'still no lesson row while parked on the ask').toHaveLength(0);

    // The child answers 语文 — a REAL pick through the real page.
    const [askEpoch] = (await controls(page)).filter((e) => e.dir === 'in' && e.type === 'audio_start')
      .map((e) => e.msg.epoch);
    await page.locator('#rt-subject-zh').click();
    const nextEpoch = askEpoch + 1;
    await waitWs(page, (log) => log.some((e) => e.dir === 'in' && e.type === 'interrupt_ack'
      && e.msg.old_epoch === askEpoch && e.msg.new_epoch === nextEpoch),
    { label: 'interrupt_ack epoch switch' });

    // The opened classroom is the 语文 one, holding EXACTLY the entered
    // words (count + student masking — the server is the masking authority).
    await waitWs(page, (log) => log.some((e) => e.dir === 'in' && e.type === 'session_ready'
      && e.msg.category === 'chinese'
      && Array.isArray(e.msg.lesson.words) && e.msg.lesson.words.length === ZH_WORDS.length
      && e.msg.lesson.words.every((w) => w === '')),
    { label: 'session_ready for the entered chinese words' });

    // 真实播放: every greeting sentence acks through the production player.
    await waitWs(page, (log) => {
      const at = (dir, type) => (e) => e.dir === dir && e.type === type && e.msg.epoch === nextEpoch;
      return log.filter(at('in', 'audio_start')).length > 0
        && log.filter(at('in', 'audio_end')).length === log.filter(at('in', 'audio_start')).length
        && log.filter(at('out', 'sentence_ack')).length === log.filter(at('in', 'audio_start')).length;
    }, { timeoutMs: 30000, label: 'greeting fully acked by the production player' });

    // 真实课堂能选到: the greeting's first-word sentence names an ENTERED
    // word (the stub saw the real TTS text the server synthesized).
    const ttsTexts = (await stub.state()).requests.filter((q) => q.category === 'tts').map((q) => q.text);
    expect(ttsTexts.some((t) => /第 1 个词，(晓月|静谧)。/.test(t)), 'first dictated word is an entered word')
      .toBe(true);

    // UI + persistence together (plan rule 7): progress renders, exactly ONE
    // lesson row exists — chinese, the entered count, no english leak.
    await expect(page.locator('#rt-subject-ask')).toBeHidden();
    await expect(page.locator('#rt-progress-text')).toHaveText(/第 1 \/ 2 个词/);
    const rows = (await svc.listLessons()).body.lessons;
    expect(rows).toHaveLength(1);
    expect(rows[0]).toMatchObject({ category: 'chinese', word_count: 2, lesson_id: 'e2e-m1-browser-chinese' });

    // Wire invariants: the journey legitimately carries zero turn_start and
    // made zero asr/llm vendor calls (no voice, no model subject guessing).
    expect((await controls(page)).filter((e) => e.type === 'turn_start')).toHaveLength(0);
    const state = await stub.state();
    expect(state.requests.filter((q) => q.category === 'asr')).toHaveLength(0);
    expect(state.requests.filter((q) => q.category === 'llm')).toHaveLength(0);

    await page.locator('#rt-end').click();
    await waitWs(page, (log) => log.some((e) => e.dir === 'in' && e.type === 'session_ended'),
      { label: 'session_ended after explicit end' });

    const final = await stub.state();
    expect(final.violations, 'the strict stub saw only scripted traffic').toEqual([]);
  } finally {
    await stub.reset();
    await svc.stop();
  }
});
