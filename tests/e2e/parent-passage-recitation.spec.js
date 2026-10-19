/**
 * Parent passage-recitation E2E — 课文指定段落背默 (Issue #14): the WHOLE
 * flow against the ISOLATED real service — seeding through the real commit
 * API (one stored passage, a paragraph range, later the whole range on the
 * SAME original), the student classroom through the REAL browser page and
 * /rt WebSocket (session → task announcement naming the range → 写完了 →
 * paragraph check rows against the range's own ordered text → manual
 * verdicts → range submit), and the parent's records view showing the
 * work, the ACTUAL range, the result and the REAL save status.
 *
 * The issue's core acceptance runs as ONE continuous story: practice 第
 * 2、3 段 (both right), THEN practice 整篇 (one wrong) — the two attempts
 * stay two distinguishable records of the SAME passage, the whole range
 * never reads mastered from the paragraph success, the original is never
 * duplicated, and each scope row carries its own follow-up plan.
 *
 * Test boundary (the suite's shared rule): ONLY the external services are
 * replaced — the local mimo stub answers the passage classifier's confirm
 * intent (text-passage-confirm.reply.json) and the classroom's TTS streams
 * (canned SSE silence). Everything else is the real stack: real WebSocket
 * → real Go service → real SQLite (per-run temp DB). The child's「voice」is
 * synthesized PCM through the server's real VAD/ASR path (the stub
 * transcribes it — the turn runs the REAL classification).
 */
// @ts-check
const { test, expect } = require('@playwright/test');

const PARAGRAPHS = [
  '秋天的雨，是一把钥匙。',
  '秋天的雨，有一盒五彩缤纷的颜料。',
  '秋天的雨，藏着非常好闻的气味。',
];
const RT_URL = 'ws://127.0.0.1:18080/rt';
const RT_ORIGIN = 'http://127.0.0.1:18080';

/** commitPassage seeds one passage through the REAL commit path. */
async function commitPassage(request, title, withParagraphScope) {
  const data = {
    category: 'chinese',
    passages: [{
      title,
      paragraphs: PARAGRAPHS,
      lesson: '',
      scope: withParagraphScope ? { paragraphs: [2, 3] } : undefined,
    }],
  };
  const res = await request.post('/api/v1/passages', { data });
  expect(res.status(), await res.text()).toBe(200);
  const body = await res.json();
  expect(body.added).toBe(1);
  return { passageId: body.results[0].passage_id, scopeId: body.results[0].scope_id };
}

/** addScope lands another range on the SAME stored passage. */
async function addScope(request, passageId, payload) {
  const res = await request.post('/api/v1/passages/' + passageId + '/scopes', { data: payload });
  expect(res.status(), await res.text()).toBe(200);
  return (await res.json()).scope;
}

/**
 * drivePassageClassroom runs one classroom session from the real student
 * page: start, one 写完了 voice turn through the page's live socket, manual
 * verdicts on the rendered rows, submit — returns what the page showed.
 */
async function drivePassageClassroom(page, title, passageId, scopeId) {
  const pageErrors = [];
  page.on('pageerror', (err) => pageErrors.push(String(err)));

  await page.goto('/tutor/?passage=' + passageId + '&scope=' + scopeId + '&category=chinese&mode=test');
  await page.locator('#rt-start').click();
  await expect
    .poll(() => page.evaluate(() => window.__rtTutor && window.__rtTutor.client.sessionId),
      { timeout: 30_000 })
    .not.toBeNull();
  await expect
    .poll(() => page.evaluate(() => window.__rtTutor.observe().passage), { timeout: 30_000 })
    .toBeTruthy();

  const banner = page.locator('#rt-passage');
  await expect(banner).toBeVisible();
  await expect(banner).toContainText('《' + title + '》');
  const task = await page.evaluate(() => window.__rtTutor.observe().passage);
  // Masked in EVERY mode: the chips never carry the text before the check.
  expect(await page.evaluate(() => window.__rtTutor.sessionState.words))
    .toEqual(Array(task.paragraphCount).fill(''));
  const preCheckText = await page.locator('body').innerText();
  for (const paragraph of PARAGRAPHS) {
    expect(preCheckText).not.toContain(paragraph);
  }

  await expect(page.locator('#rt-trace .rt-trace-teacher').first()).toBeVisible({ timeout: 30_000 });
  await expect.poll(() => page.getAttribute('#rt-status', 'data-state'), { timeout: 30_000 })
    .toBe('listening');
  const teacherTexts = await page.locator('#rt-trace .rt-trace-teacher').allTextContents();
  const spoken = teacherTexts.join('\n');
  expect(spoken).toContain(title);
  for (const paragraph of PARAGRAPHS) {
    expect(spoken).not.toContain(paragraph);
  }

  // One 写完了 voice turn through the page's live socket — the real
  // VAD/ASR/classifier path (the stub scripts the confirm intent).
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
  await expect(rows).toHaveCount(task.paragraphCount);
  return { pageErrors, task, rows, banner };
}

/** confirmAndSubmit taps every row's verdict buttons and submits. */
async function confirmAndSubmit(page, rows, verdicts) {
  for (let i = 0; i < verdicts.length; i++) {
    await rows.nth(i).locator('button').nth(verdicts[i] ? 0 : 1).click();
    await expect
      .poll(() => page.evaluate(() => window.__rtTutor.observe().check.checked),
        { timeout: 10_000 })
      .toBe(i + 1);
  }
  const submit = page.locator('#rt-check .rt-check-submit');
  await expect(submit).toBeEnabled({ timeout: 10_000 });
  await submit.click();
}

test('连续练习：第 2、3 段背默 → 整篇背默，范围独立记录且同一份原文', async ({ page, request }) => {
  const title = '秋天的雨-e2e-' + Date.now();
  const seeded = await commitPassage(request, title, true);

  // ---- Attempt 1: the paragraph range through the real student page.
  const first = await drivePassageClassroom(page, title, seeded.passageId, seeded.scopeId);
  expect(first.pageErrors, JSON.stringify(first.pageErrors)).toEqual([]);
  expect(first.task.whole).toBe(false);
  expect(first.task.paragraphs).toEqual([2, 3]);
  expect(first.task.paragraphCount).toBe(2);
  // The check rows are the RANGE's own ordered paragraphs, labeled by the
  // original numbering — no unselected paragraph rides along.
  await expect(first.rows.nth(0).locator('.rt-check-word')).toHaveText('第 2 段. ' + PARAGRAPHS[1]);
  await expect(first.rows.nth(1).locator('.rt-check-word')).toHaveText('第 3 段. ' + PARAGRAPHS[2]);
  await confirmAndSubmit(page, first.rows, [true, true]);
  await expect(page.locator('#rt-banner')).toContainText('背默完成', { timeout: 45_000 });
  await expect(page.locator('#rt-banner')).toContainText('这次的背默结果保存好啦');
  // The spoken wrap line names the range's 2/2 mastery (the server's copy);
  // it streams sentence-by-sentence, so probe every teacher line.
  await expect
    .poll(() => page.evaluate(() =>
      Array.from(document.querySelectorAll('#rt-trace .rt-trace-teacher'))
        .map(function (el) { return el.innerText; }).join('\n')), { timeout: 15_000 })
    .toContain('全对');

  // The records API: the paragraph attempt, its ACTUAL range, save status.
  const list1 = (await (await request.get('/api/v1/lessons?limit=50')).json()).lessons
    .filter((l) => l.passage_id === seeded.passageId);
  expect(list1.length).toBe(1);
  const paragraphRow = list1[0];
  expect(paragraphRow.kind).toBe('passage');
  expect(paragraphRow.method).toBe('背默');
  expect(paragraphRow.passage_title).toBe(title);
  // passage_whole rides omitempty: absent = a paragraph attempt (the row's
  // own range is the paragraph set below).
  expect(paragraphRow.passage_whole || false).toBe(false);
  expect(paragraphRow.passage_paragraphs).toEqual([2, 3]);
  expect(paragraphRow.word_count).toBe(2);
  expect(paragraphRow.correct).toBe(2);
  expect(paragraphRow.completed).toBe(true);
  expect(paragraphRow.save_status).toBe('saved');
  const detail1 = await (await request.get('/api/v1/lessons/' + encodeURIComponent(paragraphRow.lesson_id))).json();
  expect(detail1.words.map((w) => w.word)).toEqual([PARAGRAPHS[1], PARAGRAPHS[2]]);
  expect(detail1.save_receipts.length).toBe(1);
  expect(detail1.save_receipts[0].kind).toBe('passage');
  expect(detail1.save_receipts[0].status).toBe('saved');
  expect(detail1.save_receipts[0].requested).toEqual([title + '（第 2、3 段）']);

  // ---- Attempt 2: the WHOLE range on the SAME original (不重新导入).
  const wholeScope = await addScope(request, seeded.passageId, { whole: true });
  expect(wholeScope.whole).toBe(true);

  const second = await drivePassageClassroom(page, title, seeded.passageId, wholeScope.id);
  expect(second.pageErrors, JSON.stringify(second.pageErrors)).toEqual([]);
  expect(second.task.whole).toBe(true);
  expect(second.task.paragraphs).toEqual([1, 2, 3]);
  await expect(second.rows.nth(0).locator('.rt-check-word')).toHaveText('第 1 段. ' + PARAGRAPHS[0]);
  await expect(second.rows.nth(2).locator('.rt-check-word')).toHaveText('第 3 段. ' + PARAGRAPHS[2]);
  await confirmAndSubmit(page, second.rows, [true, true, false]);
  await expect(page.locator('#rt-banner')).toContainText('整篇', { timeout: 45_000 });
  // The spoken wrap line names the whole range's 2/3 result.
  await expect
    .poll(() => page.evaluate(() =>
      Array.from(document.querySelectorAll('#rt-trace .rt-trace-teacher'))
        .map(function (el) { return el.innerText; }).join('\n')), { timeout: 15_000 })
    .toContain('对了 2 段');

  // Both attempts ride the SAME passage row — 不复制课文、不伪造全文历史.
  const passages = (await (await request.get('/api/v1/passages?category=chinese&q=' + encodeURIComponent(title))).json()).passages;
  expect(passages.length).toBe(1);
  expect(passages[0].paragraphs).toEqual(PARAGRAPHS);
  expect(passages[0].scopes.length).toBe(2);

  const list2 = (await (await request.get('/api/v1/lessons?limit=50')).json()).lessons
    .filter((l) => l.passage_id === seeded.passageId);
  expect(list2.length).toBe(2);
  const wholeRow = list2.find((l) => l.passage_whole);
  expect(wholeRow.word_count).toBe(3);
  expect(wholeRow.correct).toBe(2);
  // The paragraph mastery NEVER reads as the whole's (第 2、3 段掌握不算全
  // 文掌握) — each scope row keeps its own state and follow-up day.
  const paragraphScope = passages[0].scopes.find((s) => !s.whole);
  const wholeScopeState = passages[0].scopes.find((s) => s.whole);
  expect(paragraphScope.has_practice).toBe(true);
  expect(paragraphScope.last_mastered).toBe(true);
  expect(paragraphScope.next_review_day).toMatch(/^\d{4}-\d{2}-\d{2}$/);
  expect(wholeScopeState.has_practice).toBe(true);
  expect(wholeScopeState.last_mastered).toBe(false);
  expect(wholeScopeState.next_review_day).toMatch(/^\d{4}-\d{2}-\d{2}$/);

  // ---- The parent's records view renders both rows with their ranges.
  await page.goto('/parent.html');
  // Newest-first ordering puts the whole attempt on top — pick the paragraph
  // row BY ITS RANGE, the exact distinction the records view must carry.
  const paragraphRecord = page.locator('.lesson-row', { hasText: title })
    .filter({ hasText: '第 2、3 段' });
  await expect(paragraphRecord).toBeVisible({ timeout: 15_000 });
  await expect(paragraphRecord).toContainText('课文背默');
  await expect(paragraphRecord).toContainText('第 2、3 段');
  await expect(paragraphRecord).toContainText('2 段全对，已掌握');
  await expect(paragraphRecord).toContainText('已保存');
  await paragraphRecord.click();
  const detail = page.locator('.lesson-detail');
  await expect(detail).toBeVisible({ timeout: 15_000 });
  await expect(detail).toContainText('按本次范围记录掌握');
  await expect(detail.locator('.lesson-word-row').first()).toContainText('第 2 段');
  const receiptLine = detail.locator('.lesson-receipt', { hasText: '课文背默记录' });
  await expect(receiptLine).toContainText('已保存');

  // The content view: each scope row is an independent practice target with
  // its own door and its own follow-up plan (指定段落可独立安排后续练习).
  await page.getByRole('tab', { name: '词库' }).click();
  await page.locator('.type-pills button', { hasText: '课文' }).click();
  const card = page.locator('.passage-card', { hasText: title });
  await expect(card).toBeVisible({ timeout: 15_000 });
  await expect(card.locator('.passage-scope-row', { hasText: '第 2、3 段' })).toContainText('上次已掌握');
  await expect(card.locator('.passage-scope-row', { hasText: '整篇' })).toContainText('上次尚未掌握');
  expect(await card.getByRole('button', { name: '背默第 2、3 段' }).count()).toBe(1);
  expect(await card.getByRole('button', { name: '背默整篇' }).count()).toBe(1);
});

test('入口与闸门：范围行开门带范围 id；范围外课文诚实拒绝', async ({ page, request }) => {
  const title = '小小的船-e2e-' + Date.now();
  const seeded = await commitPassage(request, title, true);

  // The content view's per-range door opens the student page with BOTH ids.
  await page.goto('/parent.html');
  await page.getByRole('tab', { name: '词库' }).click();
  await page.locator('.type-pills button', { hasText: '课文' }).click();
  const card = page.locator('.passage-card', { hasText: title });
  await expect(card).toBeVisible({ timeout: 15_000 });
  const door = card.getByRole('button', { name: '背默第 2、3 段' });
  await expect(door).toBeEnabled();
  const popup = page.waitForEvent('popup');
  await door.click();
  const student = await popup;
  await expect(student).toHaveURL(new RegExp('/tutor/\\?passage=' + seeded.passageId + '&scope=' + seeded.scopeId));

  // An out-of-scope passage: attribution beyond the learned prefix.
  const book = await request.post('/api/v1/textbooks', {
    data: { name: 'e2e-passage-gate-book-' + Date.now(), category: 'chinese', lessons: ['第1课', '第2课'] },
  });
  const bookId = (await book.json()).id;
  const assign = await request.post('/api/v1/passages/' + seeded.passageId + '/assign', {
    data: { textbook_id: bookId, lesson: '第2课', includeInScope: false },
  });
  expect(assign.ok(), await assign.text()).toBeTruthy();

  const pending = (await (await request.get('/api/v1/passages?category=chinese&q=' + encodeURIComponent(title))).json()).passages[0];
  expect(pending.in_scope).toBe(false);

  // The classroom refuses honestly — never a silent word lesson.
  const WebSocket = require('ws');
  const refusal = await new Promise((resolve, reject) => {
    const ws = new WebSocket(RT_URL, { origin: RT_ORIGIN });
    const fail = setTimeout(() => reject(new Error('error timeout')), 30_000);
    ws.on('error', (err) => { clearTimeout(fail); reject(err); });
    ws.on('open', () => {
      ws.send(JSON.stringify({
        type: 'session_init',
        audio: { sampleRate: 16000, format: 's16le', channels: 1 },
        mode: 'test', category: 'chinese',
        passage: seeded.passageId, passage_scope: seeded.scopeId,
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
  expect(refusal.code).toBe('lesson_unavailable');
  expect(refusal.message).toContain('学习范围外');

  // And the out-of-scope card's doors disable with the honest reason.
  await page.locator('.type-pills button', { hasText: '词语' }).click();
  await page.locator('.type-pills button', { hasText: '课文' }).click();
  const pendingCard = page.locator('.passage-card', { hasText: title });
  await expect(pendingCard).toBeVisible({ timeout: 15_000 });
  await expect(pendingCard.getByRole('button', { name: '暂不在学习范围' }).first()).toBeDisabled();
});
