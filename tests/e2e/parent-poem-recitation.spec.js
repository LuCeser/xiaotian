/**
 * Parent poem-recitation E2E — 整首古诗背默 (Issue #12): the WHOLE flow
 * against the ISOLATED real service — import-side seeding through the real
 * commit API, the student classroom over the REAL /rt WebSocket (session
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
const fs = require('node:fs');
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

/** sineFrame builds one 30ms 16kHz s16le frame above the VAD start RMS. */
function speechFrame() {
  const samples = 480; // 30ms at 16kHz
  const buf = Buffer.alloc(samples * 2);
  for (let i = 0; i < samples; i++) {
    const v = Math.round(9000 * Math.sin((2 * Math.PI * 440 * i) / 16000));
    buf.writeInt16LE(v, i * 2);
  }
  return buf;
}

/**
 * drivePoemClassroom runs the student-side WHOLE classroom over the real
 * /rt socket: continuous mic pump (speech while speakUntil holds, silence
 * otherwise — the faithful-browser shape), sentence acks after every
 * audio_end, the one 写完了 voice turn through the real VAD/ASR/LLM path,
 * the answer-sheet photo upload, the manual per-line verdicts and the
 * submit. Resolves with the captured control messages.
 */
function drivePoemClassroom(poemId) {
  return new Promise((resolve, reject) => {
    const captured = {
      ready: null,
      teacherTexts: [],
      checkStarted: null,
      ocr: null,
      review: null,
      ended: null,
      errors: [],
    };
    const ws = new WebSocket(RT_URL, { origin: RT_ORIGIN });
    ws.binaryType = 'nodebuffer';

    const silence = Buffer.alloc(960); // 30ms of s16le zeros
    const speech = speechFrame();
    let speakUntil = 0;
    let spokeOnce = false;
    let submitted = false;
    let verdictEcho = 0;
    const acked = new Set();
    const fail = setTimeout(() => {
      cleanup();
      reject(new Error('poem classroom timeout: ' + JSON.stringify(captured)));
    }, 90_000);

    let pump = null;
    function cleanup() {
      clearTimeout(fail);
      if (pump) clearInterval(pump);
      try { ws.close(); } catch (_) { /* already closed */ }
    }

    ws.on('error', (err) => {
      cleanup();
      reject(err);
    });
    ws.on('open', () => {
      ws.send(JSON.stringify({
        type: 'session_init',
        audio: { sampleRate: 16000, format: 's16le', channels: 1 },
        mode: 'test', // even test mode must NOT reveal a poem's lines
        category: 'chinese',
        poem: poemId,
      }));
      // The faithful-browser mic: never muted, 30ms cadence.
      pump = setInterval(() => {
        if (ws.readyState !== WebSocket.OPEN) return;
        ws.send(Date.now() < speakUntil ? speech : silence);
      }, 30);
    });
    ws.on('message', (data, isBinary) => {
      if (isBinary) return;
      let msg;
      try {
        msg = JSON.parse(String(data));
      } catch (_) {
        return;
      }
      switch (msg.type) {
        case 'session_ready':
          captured.ready = msg;
          break;
        case 'teacher_sentence':
          if (msg.text) captured.teacherTexts.push(msg.text);
          break;
        case 'audio_end':
          if (msg.sentence_id && !acked.has(msg.sentence_id)) {
            acked.add(msg.sentence_id);
            ws.send(JSON.stringify({
              type: 'sentence_ack',
              sentence_id: msg.sentence_id,
              epoch: msg.epoch,
            }));
          }
          break;
        case 'state':
          // The greeting settled: speak 写完了 once, past the echo tail.
          if (msg.value === 'listening' && captured.ready && !spokeOnce &&
            captured.teacherTexts.length > 0) {
            spokeOnce = true;
            setTimeout(() => {
              speakUntil = Date.now() + 1800;
            }, 800);
          }
          break;
        case 'check_started': {
          captured.checkStarted = msg;
          // The whole answer sheet in one shot (the page is ~30KB — a
          // single final photo_chunk; the server's assembler accepts it).
          const jpeg = fs.readFileSync(ANSWER_FIXTURE);
          ws.send(JSON.stringify({
            type: 'photo_chunk',
            seq: 1,
            data: jpeg.toString('base64'),
            final: true,
          }));
          break;
        }
        case 'ocr_result': {
          captured.ocr = msg;
          // The human pass: confirm lines 1-3 对 and line 4 错 (the OCR's
          // honest verbatim read backs the mistake — 沿用人工确认/纠错).
          for (let i = 0; i < POEM_LINES.length; i++) {
            ws.send(JSON.stringify({
              type: 'check_verdict',
              request_id: 'e2e-poem-v-' + i,
              word_index: i,
              correct: i < 3,
            }));
          }
          break;
        }
        case 'check_verdicts':
          verdictEcho = ((msg.verdicts || []).filter((v) => v.manual)).length;
          if (verdictEcho >= POEM_LINES.length && !submitted) {
            submitted = true;
            ws.send(JSON.stringify({
              type: 'check_submit',
              request_id: 'e2e-poem-submit',
            }));
          }
          break;
        case 'review_result':
          captured.review = msg;
          break;
        case 'session_ended':
          captured.ended = msg;
          cleanup();
          resolve(captured);
          break;
        case 'error':
          captured.errors.push(msg);
          break;
        default:
          break;
      }
    });
  });
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

  const run = await drivePoemClassroom(poemId);

  // The classroom opened on the whole work — masked, in EVERY mode.
  expect(run.errors).toEqual([]);
  expect(run.ready.poem).toBeTruthy();
  expect(run.ready.poem.title).toBe(title);
  expect(run.ready.poem.author).toBe('李白');
  expect(run.ready.poem.line_count).toBe(4);
  expect(run.ready.poem.method).toBe('背默');
  expect(run.ready.category).toBe('chinese');
  expect(run.ready.lesson.words).toEqual(['', '', '', '']);

  // The 背默 red line: no teacher sentence ever carried a line.
  const spoken = run.teacherTexts.join('\n');
  expect(spoken).toContain(title);
  expect(spoken).toContain('默写');
  for (const line of POEM_LINES) {
    expect(spoken).not.toContain(line);
  }

  // The check rows are the poem's own ordered lines.
  expect(run.checkStarted.reason).toBe('poem_done');
  expect(run.checkStarted.words).toEqual(POEM_LINES);

  // The photo's honest per-line read prefilled (line 4's mistake verbatim).
  const entries = run.ocr.entries || [];
  expect(entries.length).toBe(4);
  expect(entries[3].ocr_text).toBe('低头思故香');
  expect(entries[3].correct).toBe(false);

  // ONE whole-poem save, honestly reported.
  expect(run.review.requested).toBe(1);
  expect(run.review.updated).toBe(1);
  expect(run.ended.reason).toBe('lesson_complete');

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
