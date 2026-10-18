/**
 * Parent poem-recitation E2E — 整首古诗背默 (Issue #12): the WHOLE flow
 * against the ISOLATED real service — import-side seeding through the real
 * commit API, the student classroom through the REAL browser page and /rt
 * WebSocket (session
 * → task announcement → 写完了 → line check against the poem's own text →
 * photo OCR prefill → manual verdicts → whole-poem submit), and the
 * parent's records view showing the work, the practice way, the mastery
 * result and the REAL save status.
 *
 * Test boundary (the suite's shared rule): ONLY the external services are
 * replaced — the local mimo stub answers the vision call with the answer
 * sheet's canned per-line reply (photo-zh-poem-answer.jpg, a real rendered
 * 默写 page with ONE real mistake on line 4, see
 * scripts/gen-poem-answer-fixture.py), the poem classifier's confirm
 * intent (text-poem-confirm.reply.json) and the classroom's TTS streams
 * (canned SSE silence, Issue #12 stub addition). Everything else is the
 * real stack: real WebSocket → real Go service → real SQLite (per-run
 * temp DB). The child's「voice」is synthesized PCM through the server's
 * real VAD/ASR path (the stub transcribes it as 好了 — the turn runs the
 * REAL classification).
 *
 * Flows:
 *   1. whole flow — an in-scope poem opens a 背默 classroom: session_ready
 *      masks the lines in EVERY mode (mode=test included) and carries the
 *      task view; no teacher sentence ever leaks a line; 写完了 opens the
 *      check rows against the poem's own ordered lines; the photo's honest
 *      transcription (mistake verbatim) prefills manual=false verdicts; the
 *      human verdicts gate the submit; ONE whole-poem record + ONE
 *      kind=poem receipt land; the parent list/detail distinguish the
 *      work, 背默 and the 3/4-line mastery with save 已保存.
 *   2. entry & gates — the content view's 背默这首 door opens the student
 *      page with the poem id (in-scope only: an out-of-scope poem's door is
 *      disabled and its session_init answers the honest coded refusal; an
 *      unknown id too).
 */
// @ts-check
const { test, expect } = require('@playwright/test');
const WebSocket = require('ws');
const path = require('node:path');

const ANSWER_FIXTURE = path.join(__dirname, 'fixtures', 'photo-zh-poem-answer.jpg');
const POEM_LINES = ['床前明月光', '疑是地上霜', '举头望明月', '低头思故乡'];
const RT_URL = 'ws://127.0.0.1:18080/rt';
const RT_ORIGIN = 'http://127.0.0.1:18080';

/** commitPoem seeds one whole work through the REAL commit path. */
async function commitPoem(request, title) {
  const res = await request.post('/api/v1/poems', {
    data: {
      category: 'chinese',
      poems: [{ title, author: '李白', lines: POEM_LINES, lesson: '' }],
    },
  });
  expect(res.status()).toBe(200);
  const body = await res.json();
  expect(body.added).toBe(1);
  return body.results[0].poem_id;
}

/**
 * drivePoemClassroom runs the whole classroom from the real student page.
 * It starts the page, sends one 写完了 voice turn through that page's live
 * socket, uploads the answer sheet through the file picker, confirms every
 * line with the rendered controls, and submits with the rendered button.
 */
async function drivePoemClassroom(page, poemId, title) {
  const pageErrors = [];
  page.on('pageerror', (err) => pageErrors.push(String(err)));

  await page.goto('/tutor/?poem=' + poemId + '&category=chinese&mode=test');
  // Keep the committed JPEG bytes stable for the exact-byte local vision
  // stub. The real file picker, RTPhoto validation, chunking, transport and
  // server OCR path still run; the resize/encode helper has its own tests.
  await page.evaluate(() => {
    window.RTPhoto.prototype._downscale = async (file) =>
      new Uint8Array(await file.arrayBuffer());
  });
  await page.locator('#rt-start').click();
  await expect
    .poll(() => page.evaluate(() => window.__rtTutor && window.__rtTutor.client.sessionId),
      { timeout: 30_000 })
    .not.toBeNull();
  await expect
    .poll(() => page.evaluate(() => window.__rtTutor.observe().poem), { timeout: 30_000 })
    .toEqual({ title, lineCount: 4 });

  const poemBanner = page.locator('#rt-poem');
  await expect(poemBanner).toContainText('《' + title + '》');
  await expect(poemBanner).toContainText('李白');
  await expect(poemBanner).toContainText('共 4 句');
  await expect(poemBanner).toContainText('背默');
  expect(await page.evaluate(() => window.__rtTutor.sessionState.words)).toEqual(['', '', '', '']);

  await expect(page.locator('#rt-trace .rt-trace-teacher').first()).toBeVisible({ timeout: 30_000 });
  await expect.poll(() => page.getAttribute('#rt-status', 'data-state'), { timeout: 30_000 })
    .toBe('listening');
  const teacherTexts = await page.locator('#rt-trace .rt-trace-teacher').allTextContents();
  const preCheckText = await page.locator('body').innerText();

  // The browser page owns the live socket. Inject one 30ms-cadence PCM turn
  // through it so the real VAD/ASR/classifier handles 写完了.
  await page.evaluate(async () => {
    const ws = window.__rtTutor.client.ws;
    const speech = new Int16Array(480);
    for (let i = 0; i < speech.length; i++) {
      speech[i] = Math.round(9000 * Math.sin((2 * Math.PI * 440 * i) / 16000));
    }
    const silence = new Int16Array(480);
    const sendFrames = (frame, count) => new Promise((resolve) => {
      let sent = 0;
      const timer = setInterval(() => {
        if (ws.readyState !== WebSocket.OPEN || sent >= count) {
          clearInterval(timer);
          resolve();
          return;
        }
        ws.send(frame.buffer);
        sent += 1;
      }, 30);
    });
    await sendFrames(speech, 60);
    await sendFrames(silence, 50);
  });

  await expect(page.locator('#rt-check')).toBeVisible({ timeout: 45_000 });
  const rows = page.locator('#rt-check .rt-check-row');
  await expect(rows).toHaveCount(4);
  for (let i = 0; i < POEM_LINES.length; i++) {
    await expect(rows.nth(i).locator('.rt-check-word')).toHaveText((i + 1) + '. ' + POEM_LINES[i]);
  }

  const [chooser] = await Promise.all([
    page.waitForEvent('filechooser'),
    page.locator('#rt-check .rt-check-photo').click(),
  ]);
  await chooser.setFiles(ANSWER_FIXTURE);
  await expect
    .poll(() => page.evaluate(() => window.__rtTutor.observe().check.ocrRows), { timeout: 30_000 })
    .toBe(4);
  const ocrTexts = await page.locator('#rt-check .rt-ocr-text').allTextContents();

  for (let i = 0; i < POEM_LINES.length; i++) {
    await rows.nth(i).locator('button').nth(i < 3 ? 0 : 1).click();
    await expect
      .poll(() => page.evaluate(() => window.__rtTutor.observe().check.checked),
        { timeout: 10_000 })
      .toBe(i + 1);
  }
  const submit = page.locator('#rt-check .rt-check-submit');
  await expect(submit).toBeEnabled({ timeout: 10_000 });
  await submit.click();
  await expect(page.locator('#rt-banner')).toContainText('背默完成', { timeout: 45_000 });
  await expect(page.locator('#rt-banner')).toContainText('整首背默结果保存好啦');

  return { pageErrors, teacherTexts, preCheckText, ocrTexts };
}

/** rtPoemInitError opens a session_init for a poem expected to REFUSE. */
function rtPoemInitError(poemId) {
  return new Promise((resolve, reject) => {
    const ws = new WebSocket(RT_URL, { origin: RT_ORIGIN });
    const fail = setTimeout(() => reject(new Error('error timeout')), 30_000);
    ws.on('error', (err) => { clearTimeout(fail); reject(err); });
    ws.on('open', () => {
      ws.send(JSON.stringify({
        type: 'session_init',
        audio: { sampleRate: 16000, format: 's16le', channels: 1 },
        mode: 'test', category: 'chinese', poem: poemId,
      }));
    });
    ws.on('message', (data) => {
      const msg = JSON.parse(String(data));
      if (msg.type === 'error') {
        clearTimeout(fail);
        ws.close();
        resolve(msg);
      } else if (msg.type === 'session_ready') {
        clearTimeout(fail);
        ws.close();
        reject(new Error('unexpected session_ready: ' + JSON.stringify(msg)));
      }
    });
  });
}

test('whole flow: 背默 → 检查 → 提交 → 家长查看，整首保存且判定与保存分开表达', async ({ page, request }) => {
  const title = '静夜思-e2e-' + Date.now();
  const poemId = await commitPoem(request, title);

  const run = await drivePoemClassroom(page, poemId, title);

  // The student page opened on the whole work, with every line masked.
  expect(run.pageErrors, JSON.stringify(run.pageErrors)).toEqual([]);
  const spoken = run.teacherTexts.join('\n');
  expect(spoken).toContain(title);
  expect(spoken).toContain('默写');
  for (const line of POEM_LINES) {
    expect(spoken).not.toContain(line);
    expect(run.preCheckText).not.toContain(line);
  }

  // The browser upload rendered the photo's honest line-4 mistake.
  expect(run.ocrTexts).toHaveLength(4);
  expect(run.ocrTexts[3]).toContain('低头思故香');

  // The records API: the work, the practice way, the 3/4 mastery, the save.
  const listRes = await request.get('/api/v1/lessons');
  expect(listRes.ok()).toBeTruthy();
  const rows = (await listRes.json()).lessons.filter((l) => l.poem_id === poemId);
  expect(rows.length).toBe(1);
  const row = rows[0];
  expect(row.kind).toBe('poem');
  expect(row.method).toBe('背默');
  expect(row.poem_title).toBe(title);
  expect(row.poem_author).toBe('李白');
  expect(row.category).toBe('chinese');
  expect(row.word_count).toBe(4);
  expect(row.correct).toBe(3);
  expect(row.wrong).toBe(1);
  expect(row.completed).toBe(true);
  expect(row.save_status).toBe('saved');

  const detailRes = await request.get('/api/v1/lessons/' + encodeURIComponent(row.lesson_id));
  expect(detailRes.ok()).toBeTruthy();
  const detail = await detailRes.json();
  expect(detail.words.map((w) => w.word)).toEqual(POEM_LINES);
  expect(detail.results.length).toBe(4);
  expect(detail.results[3].correct).toBe(false);
  expect(detail.save_receipts.length).toBe(1);
  expect(detail.save_receipts[0].kind).toBe('poem');
  expect(detail.save_receipts[0].status).toBe('saved');
  expect(detail.save_receipts[0].requested).toEqual([title]);

  // A failed whole-poem attempt is scheduled for another practice day.
  const poemsRes = await request.get('/api/v1/poems?category=chinese&q=' + encodeURIComponent(title));
  expect(poemsRes.ok()).toBeTruthy();
  const practiced = (await poemsRes.json()).poems.find((p) => p.id === poemId);
  expect(practiced.has_practice).toBe(true);
  expect(practiced.last_mastered).toBe(false);
  expect(practiced.next_review_day).toMatch(/^\d{4}-\d{2}-\d{2}$/);

  // The parent's records view: the row, then the detail.
  await page.goto('/parent.html');
  const lessonRow = page.locator('.lesson-row[data-lesson-id="' + row.lesson_id + '"]');
  await expect(lessonRow).toBeVisible({ timeout: 15_000 });
  await expect(lessonRow).toContainText('古诗背默');
  await expect(lessonRow).toContainText('《' + title + '》');
  await expect(lessonRow).toContainText('李白');
  await expect(lessonRow).toContainText('背默');
  await expect(lessonRow).toContainText('共 4 句，对 3 句');
  await expect(lessonRow).toContainText('已保存');

  await lessonRow.click();
  const detailPanel = page.locator('.lesson-detail');
  await expect(detailPanel).toBeVisible({ timeout: 15_000 });
  await expect(detailPanel).toContainText('古诗背默');
  await expect(detailPanel).toContainText('《' + title + '》');
  await expect(detailPanel).toContainText('按整首记录掌握，不进入单词复习计划');
  await expect(detailPanel).toContainText('共 4 句，对 3 句');
  await expect(detailPanel.locator('.lesson-word-row').nth(3)).toContainText('低头思故乡');
  const receiptLine = detailPanel.locator('.lesson-receipt', { hasText: '古诗背默记录' });
  await expect(receiptLine).toBeVisible();
  await expect(receiptLine).toContainText('已保存');
  await expect(receiptLine).toContainText('保存 1 / 1');

  // The parent content view exposes the persisted next-practice decision.
  await page.getByRole('tab', { name: '词库' }).click();
  await page.locator('.type-pills button', { hasText: '古诗' }).click();
  const poemCard = page.locator('.poem-card', { hasText: title });
  await expect(poemCard).toContainText('上次尚未整首掌握');
  await expect(poemCard).toContainText(practiced.next_review_day);
  await expect(poemCard).toContainText('再练');
});

test('entry & gates: 背默这首 opens the student page; 范围外/未知古诗诚实拒绝', async ({ page, request }) => {
  const title = '咏鹅-e2e-' + Date.now();
  const poemId = await commitPoem(request, title);

  // The content view: the in-scope work carries the live door.
  await page.goto('/parent.html');
  await page.getByRole('tab', { name: '词库' }).click();
  await page.locator('.type-pills button', { hasText: '古诗' }).click();
  const card = page.locator('.poem-card', { hasText: title });
  await expect(card).toBeVisible({ timeout: 15_000 });
  const door = card.getByRole('button', { name: '背默这首' });
  await expect(door).toBeEnabled();

  // The door opens the student tutor page with the poem id.
  const popup = page.waitForEvent('popup');
  await door.click();
  const student = await popup;
  await expect(student).toHaveURL(new RegExp('/tutor/\\?poem=' + poemId + '&category=chinese'));

  // An out-of-scope poem: textbook attribution beyond the learned prefix.
  const book = await request.post('/api/v1/textbooks', {
    data: { name: 'e2e-poem-gate-book-' + Date.now(), category: 'chinese', lessons: ['第1课', '第2课'] },
  });
  const bookId = (await book.json()).id;
  expect(bookId).toBeTruthy();
  const pendingTitle = '春晓-e2e-' + Date.now();
  const pendingId = await commitPoem(request, pendingTitle);
  const assign = await request.post('/api/v1/poems/' + pendingId + '/assign', {
    data: { textbook_id: bookId, lesson: '第2课', includeInScope: false },
  });
  expect(assign.ok(), await assign.text()).toBeTruthy();

  // The API's scope verdict backs the refusal (课次尚未学到).
  const poemsRes = await request.get('/api/v1/poems?category=chinese&q=' + encodeURIComponent(pendingTitle));
  const pending = (await poemsRes.json()).poems.find((p) => p.id === pendingId);
  expect(pending.in_scope).toBe(false);
  expect(pending.scope_reason).toBe('progress_pending');

  // The classroom refuses honestly — never a silent word lesson.
  const scopeErr = await rtPoemInitError(pendingId);
  expect(scopeErr.code).toBe('lesson_unavailable');
  expect(scopeErr.message).toContain('学习范围外');

  // An unknown poem id refuses the same honest way.
  const missingErr = await rtPoemInitError(999999);
  expect(missingErr.code).toBe('lesson_unavailable');

  // And the out-of-scope card's door is disabled with the honest reason.
  await page.locator('.type-pills button', { hasText: '词语' }).click();
  await page.locator('.type-pills button', { hasText: '古诗' }).click();
  const pendingCard = page.locator('.poem-card', { hasText: pendingTitle });
  await expect(pendingCard).toBeVisible({ timeout: 15_000 });
  await expect(pendingCard.getByRole('button', { name: '暂不在学习范围' })).toBeDisabled();
});
