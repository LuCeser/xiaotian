// photo-entry.spec.js — J02 / J03 (issue #58, e2e-test-plan §7 step 13):
// the photo-import journey on the REAL parent page against a REAL isolated
// service, using the repo's existing fixture photo + canned recognition
// (no real photos, no real OCR — the plan's B-layer boundary). The strict
// stub's `image` script carries the fixture reply, so every OTHER vision
// call is a violation:
//
//   J02  recognition is a DRAFT: the formal library is untouched until the
//        parent's final edits are committed — then the library holds
//        EXACTLY the checked rows with the edited meanings (conflict keeps
//        the library's meaning, unchecked never lands), still queryable
//        after a refresh;
//   J03  a failing recognition shows the honest error (no fake success,
//        old entries unharmed), the SAME form retries successfully, and a
//        retried commit writes each word exactly once;
//   J02c (second commit) the imported words then feed a REAL classroom:
//        the dictation greeting actually plays them (natural ACKs).
//
// Fixture reply order is pinned by tests/e2e/fixtures/photo-wordlist.png's
// sibling reply: 0 apple(conflict) · 1 banana · 2 ice cream · 3 go for a
// walk · 4 river(exists) · 5 banana(dup). Per-test isolation via
// spec-service.js; shared stub reset in every finally.
'use strict';

const fs = require('node:fs');
const path = require('node:path');
const { test, expect } = require('@playwright/test');
const { startSpecService, makeStubControl } = require('./helpers/spec-service');
const { installRtObserver, waitWs, controls } = require('./helpers/rt-observe');

const stub = makeStubControl();

const FIXTURES = path.resolve(__dirname, '../e2e/fixtures');
const fixtureReply = (name) => JSON.parse(fs.readFileSync(path.join(FIXTURES, name), 'utf8')).content;
const WORDLIST_PNG = path.join(FIXTURES, 'photo-wordlist.png');
const WORDLIST2_PNG = path.join(FIXTURES, 'photo-wordlist2.png');

/** The photo tab of 添加单词, on this test's own service. */
async function openPhotoTab(page, svc) {
  await page.goto(svc.url + 'parent.html');
  await page.getByRole('tab', { name: '词库' }).click();
  await page.getByRole('tab', { name: '照片导入' }).click();
  const panel = page.locator('#add-panel-photo');
  await expect(panel).toBeVisible();
  return panel;
}

/** Upload + tap 开始识别 through the real form (no outcome wait). */
async function startRecognize(page, file) {
  await page.locator('input[name="parent-category-photo"][value="english"]').check();
  await page.locator('#parent-photo-file').setInputFiles(file);
  await page.getByRole('button', { name: '开始识别' }).click();
}

/** startRecognize + wait for the review rows (the success outcome). */
async function recognize(page, file) {
  await startRecognize(page, file);
  await expect(page.locator('.photo-row').first()).toBeVisible({ timeout: 30_000 });
}

/** word → meaning snapshot of the english library (the formal truth). */
async function library(svc) {
  const words = (await svc.listWords({ category: 'english' })).body.words || [];
  return new Map(words.map((w) => [w.word, w.meaning]));
}

test('J02 draft stays out of the library; edited+checked rows save exactly; refresh still queries', async ({ page }) => {
  test.setTimeout(90_000);
  const svc = await startSpecService();
  try {
    // Conflict fixtures: apple conflicts (different meaning), river exists.
    expect((await svc.seedWords({ category: 'english', unit: 'e2e-m1-photo-seed', entries: [
      { word: 'apple', meaning: '苹果（水果）' },
      { word: 'river', meaning: '河流' },
    ] })).ok).toBe(true);
    const before = await library(svc);

    expect((await stub.scripts({ asr: [], llm: [], tts: [], image: [{ content: fixtureReply('photo-wordlist.reply.json') }] })).ok).toBe(true);
    await openPhotoTab(page, svc);
    await recognize(page, WORDLIST_PNG);
    await expect(page.locator('.photo-row')).toHaveCount(6);

    // 识别前后正式词库不变: the draft is client-side only.
    expect(await library(svc)).toEqual(before);

    // The conflict row is named and not submittable; the in-photo duplicate
    // flags its first occurrence.
    const row = (i) => page.locator(`.photo-row[data-row-index="${i}"]`);
    await expect(row(0)).toHaveClass(/photo-row-conflict/);
    await expect(row(0).locator('.photo-status')).toContainText('词库释义：苹果（水果）');
    await expect(row(0).locator('.photo-row-check')).toBeDisabled();
    await expect(row(5)).toHaveClass(/photo-row-invalid/);
    await expect(row(5).locator('.photo-status')).toContainText('与第 2 行重复');

    // Parent's final edits govern: edit one meaning, uncheck one row.
    await row(3).locator('input[aria-label="释义 第 4 行"]').fill('去散步');
    await row(2).locator('.photo-row-check').uncheck();
    await page.getByRole('button', { name: '确认添加（2 个）' }).click();

    // Partial success shown as partial — never a plain success.
    const banner = page.locator('.result-banner');
    await expect(banner).toHaveClass(/result-banner-warning/);
    await expect(banner.locator('.result-summary')).toContainText('已添加 2 个');
    await expect(banner.locator('.result-summary')).toContainText('未提交 4 个');
    await expect(banner).toContainText('apple（释义不同）');
    await expect(banner).toContainText('river（已有）');
    await expect(banner).toContainText('ice cream（未勾选）');
    await expect(banner).toContainText('banana（与第 2 行重复）');

    // The REAL library carries exactly the parent's final edits…
    const after = await library(svc);
    expect(after.get('banana')).toBe('香蕉');
    expect(after.get('go for a walk')).toBe('去散步');
    expect(after.has('ice cream'), 'the unchecked row never lands').toBe(false);
    expect(after.get('apple'), 'the conflict keeps the library meaning').toBe('苹果（水果）');
    expect(after.get('river')).toBe('河流');
    expect(after.size).toBe(before.size + 2);

    // …and 刷新可查.
    await page.reload();
    await page.getByRole('tab', { name: '词库' }).click();
    await expect(page.locator('.word-row[data-word="banana"]')).toBeVisible({ timeout: 15_000 });
    await expect(page.locator('.word-row[data-word="go for a walk"] .word-meaning')).toHaveText('去散步');

    expect((await stub.state()).violations).toEqual([]);
  } finally {
    await stub.reset();
    await svc.stop();
  }
});

test('J03 recognition failure is honest; retry on the same form succeeds and never double-writes', async ({ page }) => {
  test.setTimeout(90_000);
  const svc = await startSpecService();
  try {
    const before = await library(svc);
    await openPhotoTab(page, svc);

    // The provider fails through the whole client retry ladder (mimo
    // defaults to 4 total attempts on 5xx) — every failure is scripted, so
    // the strict stub stays clean while the service still sees a failure.
    expect((await stub.scripts({
      asr: [], llm: [], tts: [],
      image: [{ fail: { status: 500 } }, { fail: { status: 500 } }, { fail: { status: 500 } }, { fail: { status: 500 } }],
    })).ok).toBe(true);
    await startRecognize(page, WORDLIST2_PNG);
    await expect(page.locator('.photo-row')).toHaveCount(0);
    await expect(page.locator('#parent-photo-form-error')).toContainText('识别失败', { timeout: 30_000 });
    expect(await library(svc), '失败不产生任何写入').toEqual(before);

    // The SAME form retries with a working provider.
    expect((await stub.scripts({ asr: [], llm: [], tts: [], image: [{ content: fixtureReply('photo-wordlist2.reply.json') }] })).ok).toBe(true);
    await recognize(page, WORDLIST2_PNG);
    await expect(page.locator('.photo-row')).toHaveCount(2);
    await page.getByRole('button', { name: '确认添加（2 个）' }).click();
    await expect(page.locator('.result-banner .result-summary')).toContainText('已添加 2 个', { timeout: 30_000 });

    // 不重复写入: each retried word exists exactly once with its meaning.
    const words = (await svc.listWords({ category: 'english' })).body.words;
    const suns = words.filter((w) => w.word === 'sun');
    expect(suns).toHaveLength(1);
    expect(suns[0].meaning).toBe('太阳');
    expect(words.filter((w) => w.word === 'moon')).toHaveLength(1);
    expect((await stub.state()).violations).toEqual([]);
  } finally {
    await stub.reset();
    await svc.stop();
  }
});

// J02c — the journey's classroom half: the words imported from the photo
// actually FEED a real dictation. Import sun/moon (photo-wordlist2), then
// open the student classroom on the SAME service: english is the only
// subject with due words, so the generic entry opens it directly (J04's
// rule), the greeting streams through the production player with natural
// ACKs, and its first-word sentence names an imported word — 导入后真正
// 播放, nothing borrowed from another subject.
test('J02c imported words feed the real classroom and actually play', async ({ page }) => {
  test.setTimeout(90_000);
  const svc = await startSpecService();
  await installRtObserver(page);
  try {
    expect((await stub.scripts({
      asr: [], llm: [], tts: [
        { ms: 1200, match: '你好呀' },
        { ms: 1200, match: '个词要写' },
        { ms: 1200, match: '第 1 个词' },
      ],
      image: [{ content: fixtureReply('photo-wordlist2.reply.json') }],
    })).ok).toBe(true);

    await openPhotoTab(page, svc);
    await recognize(page, WORDLIST2_PNG);
    await page.getByRole('button', { name: '确认添加（2 个）' }).click();
    await expect(page.locator('.result-banner .result-summary')).toContainText('已添加 2 个', { timeout: 30_000 });

    // The classroom: generic entry, english-only due words → direct open.
    await page.goto(svc.url + 'tutor/');
    await page.locator('#rt-start').click();
    await waitWs(page, (log) => log.some((e) => e.dir === 'in' && e.type === 'session_ready'
      && e.msg.category === 'english'
      && Array.isArray(e.msg.lesson.words) && e.msg.lesson.words.length === 2
      && e.msg.lesson.words.every((w) => w === '')),
    { timeoutMs: 30000, label: 'session_ready for the imported words' });
    expect((await controls(page)).filter((e) => e.type === 'subject_ask')).toHaveLength(0);

    // 真正播放: every greeting sentence acks through the production player,
    // and the first dictated word is one of the IMPORTED words.
    await waitWs(page, (log) => {
      const starts = log.filter((e) => e.dir === 'in' && e.type === 'audio_start');
      const ends = log.filter((e) => e.dir === 'in' && e.type === 'audio_end');
      const acks = log.filter((e) => e.dir === 'out' && e.type === 'sentence_ack');
      return starts.length > 0 && ends.length === starts.length && acks.length === starts.length;
    }, { timeoutMs: 30000, label: 'greeting fully acked by the production player' });
    const ttsTexts = (await stub.state()).requests.filter((q) => q.category === 'tts').map((q) => q.text);
    expect(ttsTexts.some((t) => /第 1 个词，(sun|moon)。/.test(t)), 'first dictated word is an imported word').toBe(true);

    await expect(page.locator('#rt-progress-text')).toHaveText(/第 1 \/ 2 个词/);
    const rows = (await svc.listLessons()).body.lessons;
    expect(rows).toHaveLength(1);
    expect(rows[0]).toMatchObject({ category: 'english', word_count: 2 });
    expect((await stub.state()).violations).toEqual([]);
  } finally {
    await stub.reset();
    await svc.stop();
  }
});
