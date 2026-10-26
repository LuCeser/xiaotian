// photo-flow.js — the shared browser-flow helpers of the two photo-textbook
// spec families (parent-photo-textbook.spec.js since Issue #5,
// parent-photo-reimport.spec.js since Issue #6). The per-suite UNIT and
// book names stay in the specs; everything a photo-import e2e needs to
// drive the real page and probe the real classroom lives here, so the
// copies that once travelled spec-to-spec have one home.
//
// Conventions these helpers encode (do not fork them back into a spec):
//   - recognize() never fills the 单元 field: these suites commit in
//     textbook mode, where the per-word lesson is the only 课次 and the
//     unit tag stays empty (Issue #89). Suite isolation therefore rides
//     on the WORD list, not a unit tag: every spec deletes exactly its
//     own words (deleteWords) in before/finally (T-09-12 hygiene).
//   - awaitReceipt() asserts the Issue #6 per-category summary shape
//     (教材「…」：新增 N 个，合并 M 个 …).
//   - practiceSelection() probes the ISOLATED service's /rt (hard-coded
//     127.0.0.1:18080, the config.parent-e2e.yaml addr); ≥1.1s apart —
//     r0x lesson ids are second-granular and same-second probes replay
//     the first's frozen row.
'use strict';

const { expect } = require('@playwright/test');
const WebSocket = require('ws');

/** Open the parent page into the 照片导入 tab and recognize one photo.
 *  Issue #10: the SAME entry now requires the subject radio — these suites
 *  import English word lists, so they select 英语 (the pre-#10 default
 *  became an explicit pick, never a silent one). */
async function recognize(page, file) {
  await page.goto('/parent.html');
  await page.getByRole('tab', { name: '词库' }).click();
  await page.locator('#addSection').waitFor();
  await page.getByRole('tab', { name: '照片导入' }).click();
  const panel = page.locator('#add-panel-photo');
  await panel.waitFor();
  await panel.locator('input[name="parent-category-photo"][value="english"]').check();
  await page.locator('#parent-photo-file').setInputFiles(file);
  await page.getByRole('button', { name: '开始识别' }).click();
  await page.locator('.photo-row').first().waitFor({ timeout: 30_000 });
  return panel;
}

/** DELETE one suite's words through the real API (failures swallowed —
 *  cleanup must never mask the test's own verdict). Textbook-mode commits
 *  carry no unit tag (Issue #89), so the exact word list is the only
 *  per-suite isolation key left. */
async function deleteWords(request, words) {
  if (!words || !words.length) {
    return;
  }
  await request.delete('/api/v1/words', {
    data: { category: 'english', words },
  }).catch(() => {});
}

/** GET /words rows for the english category keyed by word surface. */
async function wordRows(request) {
  const resp = await request.get('/api/v1/words?category=english');
  const data = await resp.json();
  const map = new Map();
  for (const w of data.words || []) {
    map.set(w.word, w);
  }
  return map;
}

/** Real textbook rows (english category). */
async function textbooks(request) {
  const resp = await request.get('/api/v1/textbooks?category=english');
  const data = await resp.json();
  return data.textbooks || [];
}

/**
 * Wait for the commit receipt's per-category summary (Issue #6 shape):
 * awaitReceipt(page, bookName, { 新增: 2, 合并: 1, 待处理差异: 1 }).
 */
async function awaitReceipt(page, bookName, categories) {
  const text = Object.entries(categories)
    .map(([label, n]) => label + ' ' + n + ' 个')
    .join('，');
  await expect(page.locator('.result-banner .result-summary')).toContainText(
    '教材「' + bookName + '」：' + text, { timeout: 30_000 });
}

/** One real classroom probe: raw /rt practice init (mode=test). */
function practiceSelection() {
  return new Promise((resolve, reject) => {
    const ws = new WebSocket('ws://127.0.0.1:18080/rt', {
      origin: 'http://127.0.0.1:18080',
    });
    const fail = setTimeout(() => reject(new Error('session_ready timeout')), 30_000);
    ws.on('error', (err) => { clearTimeout(fail); reject(err); });
    ws.on('open', () => {
      ws.send(JSON.stringify({
        type: 'session_init',
        practice: true,
        mode: 'test',
        audio: { sampleRate: 16000, format: 's16le', channels: 1 },
      }));
    });
    ws.on('message', (data, isBinary) => {
      if (isBinary) return; // teacher audio rides binary frames; only control JSON here
      const msg = JSON.parse(String(data));
      if (msg.type === 'session_ready') {
        clearTimeout(fail);
        ws.close();
        resolve(msg.lesson.words);
      } else if (msg.type === 'error') {
        clearTimeout(fail);
        ws.close();
        reject(new Error('session error: ' + JSON.stringify(msg)));
      }
    });
  });
}

module.exports = {
  recognize,
  deleteWords,
  wordRows,
  textbooks,
  awaitReceipt,
  practiceSelection,
};
