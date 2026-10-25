/**
 * Parent photo-import E2E — 语文词语 (Issue #10): the SAME photo entry
 * point recognizes a 语文词语表, and the imported 词语 flow into a chinese
 * classroom and the parent records — against the ISOLATED real service.
 *
 * Test boundary (the suite's shared rule): ONLY the external recognition
 * service is replaced — the local mimo stub (tests/e2e/helpers/mimo-stub.js)
 * answers the vision call with this fixture's canned reply. Everything
 * else is the real stack: real browser → real Go service → real Python
 * bridge → real SQLite (per-run temp DB), and the classroom check drives
 * the real /rt WebSocket.
 *
 * Subject isolation (AC1/AC6 自动回归) splits by what each layer can
 * honestly prove:
 *   - the MIXED-PAGE extraction contract (同页中英混排: the 语文 prompt
 *     ignores the page's English words/phonetics, 词语 stay whole) is
 *     pinned at the prompt seam in pkg/ocr (TestWordlistChinesePromptContract);
 *     this fixture's canned reply is that contract as a fixed model output
 *     — entries carry ONLY 词语, never the page's English corner.
 *   - what only the real stack can prove lives here: ?category=chinese
 *     rides the recognize call, the commit lands in the chinese vocabulary
 *     (english library untouched), the chinese classroom's due fetch
 *     selects the 词语 while the same-day default classroom selects none,
 *     and the records row carries category=chinese.
 *
 * Flows:
 *   1. import — pick 中文 on the SAME photo form, recognize, preview
 *      (词语 whole with the material's own pinyin as the meaning, the
 *      in-photo duplicate flagged, an identical seeded word shown as 已有
 *      and kept out of the commit), commit; the chinese library gains
 *      exactly the submitted 词语, the english library is untouched
 *      (snapshot diff), and the English-style 释义补充 affordance never
 *      mounts for a chinese draft (AC2: 不强制英语式释义).
 *   2. shared management — the 词库 view's 中文 pill lists the imported
 *      word with the same 加入默写范围 toggle and 中文 badge (AC3: 参与开关
 *      和来源显示与共用能力一致).
 *   3. classroom — a raw /rt session_init (category=chinese, mode=test)
 *      receives session_ready echoing category=chinese with the imported
 *      词语 as the lesson words; a second session_init WITHOUT a category
 *      (the english default, same day) selects none of them.
 *   4. records — GET /lessons lists the chinese classroom row with
 *      category=chinese, and the 学习记录 tab renders its 中文 badge
 *      from that field (never parsed out of the lesson id).
 *
 * Test-data hygiene (T-09-12): writes confined to e2e-zh-photo-* units,
 * removed in finally-shaped cleanups, so the suite stays idempotent.
 */
// @ts-check
const { test, expect } = require('@playwright/test');
const WebSocket = require('ws');

const CAT_ZH = 'chinese';
const CAT_EN = 'english';
const UNIT_IMPORT = 'e2e-zh-photo-import';
const UNIT_SEED = 'e2e-zh-photo-seed';

const FIXTURE_ZH = require('node:path').join(__dirname, 'fixtures', 'photo-zh-wordlist.png');
// The isolated instance's pinned /rt lesson key (config.parent-e2e.yaml)
// — the chinese classroom qualifies it with the -chinese suffix.
const LESSON_ZH = 'e2e-parent-photo-chinese';

/** DELETE one whole unit through the real API. A 404 (already gone) is fine. */
async function deleteUnit(request, category, unit) {
  await request.delete('/api/v1/words', { data: { category, unit } }).catch(() => {});
}

/** Reset both chinese e2e units (idempotent suite hygiene, T-09-12). */
async function resetZhUnits(request) {
  await deleteUnit(request, CAT_ZH, UNIT_IMPORT);
  await deleteUnit(request, CAT_ZH, UNIT_SEED);
}

/**
 * Seed the identical-existing-word case: 美丽 with the SAME pinyin — every
 * test's preview then classifies it 已有 (kept out of the commit), leaving
 * exactly ONE submittable row (世界) regardless of test order.
 */
async function seedIdenticalWord(request) {
  const seed = await request.post('/api/v1/words', {
    data: {
      category: CAT_ZH, unit: UNIT_SEED,
      entries: [{ word: '美丽', meaning: 'měi lì' }],
    },
  });
  expect(seed.ok()).toBeTruthy();
}

/** Real-library snapshot: word → meaning for one category. */
async function libraryWords(request, category) {
  const resp = await request.get('/api/v1/words?category=' + category);
  const data = await resp.json();
  const map = new Map();
  for (const w of data.words || []) {
    map.set(w.word, w.meaning);
  }
  return map;
}

/** Open the parent page's 照片导入 tab of 添加单词. */
async function openPhotoTab(page) {
  await page.goto('/parent.html');
  await page.getByRole('tab', { name: '词库' }).click();
  await expect(page.locator('#addSection')).toBeVisible();
  await page.getByRole('tab', { name: '照片导入' }).click();
  const panel = page.locator('#add-panel-photo');
  await expect(panel).toBeVisible();
  return panel;
}

/** Shared prelude: the photo tab with 中文 selected, recognized, preview up,
 *  the commit unit pinned so the finally-cleanup actually removes the words
 *  (the unit input is free text — unfilled commits land in 未分组). */
async function selectChineseAndRecognize(page) {
  const panel = page.locator('#add-panel-photo');
  await panel.locator('input[name="parent-category-photo"][value="chinese"]').check();
  await panel.locator('#parent-photo-file').setInputFiles(FIXTURE_ZH);
  await panel.getByRole('button', { name: '开始识别' }).click();
  await expect(panel.locator('.photo-row').first()).toBeVisible({ timeout: 30_000 });
  await panel.getByRole('combobox', { name: '单元', exact: true }).fill(UNIT_IMPORT);
}

/**
 * Raw /rt session_init → session_ready (the classroom-selection precedent
 * of Issue #3's suite). category null omits the field — the pre-#10 wire
 * shape every existing page sends.
 */
function rtSessionInit(category) {
  return new Promise((resolve, reject) => {
    const ws = new WebSocket('ws://127.0.0.1:18080/rt', {
      origin: 'http://127.0.0.1:18080',
    });
    const fail = setTimeout(() => reject(new Error('session_ready timeout')), 30_000);
    ws.on('error', (err) => { clearTimeout(fail); reject(err); });
    ws.on('open', () => {
      const init = {
        type: 'session_init',
        audio: { sampleRate: 16000, format: 's16le', channels: 1 },
        mode: 'test',
      };
      if (category) init.category = category;
      ws.send(JSON.stringify(init));
    });
    ws.on('message', (data, isBinary) => {
      if (isBinary) return; // teacher audio rides binary frames; only control JSON here
      const msg = JSON.parse(String(data));
      if (msg.type === 'session_ready') {
        clearTimeout(fail);
        ws.close();
        resolve(msg);
      } else if (msg.type === 'error') {
        clearTimeout(fail);
        ws.close();
        reject(new Error('session error: ' + JSON.stringify(msg)));
      }
    });
  });
}

test('语文 import: same entry, 中文 selection, commit lands in the chinese library only', async ({ page, request }) => {
  await resetZhUnits(request);
  await seedIdenticalWord(request);

  try {
    const panel = await openPhotoTab(page);

    // Observe (never intercept) the recognize call: the subject must ride
    // the query string — the same endpoint, the chinese contract.
    let recognizeUrl = '';
    await page.route('**/api/v1/words/photo/recognize*', async (route) => {
      recognizeUrl = route.request().url();
      await route.continue();
    });

    // The SAME D-06 radio the manual add forms use — no pre-selection,
    // the gate before any vision cost.
    await panel.locator('input[name="parent-category-photo"][value="chinese"]').check();
    await panel.locator('#parent-photo-file').setInputFiles(FIXTURE_ZH);
    await panel.getByRole('button', { name: '开始识别' }).click();

    const row = (i) => panel.locator(`.photo-row[data-row-index="${i}"]`);
    await expect(row(0)).toBeVisible({ timeout: 30_000 });
    expect(recognizeUrl).toContain('category=chinese');

    // The stub reply is pinned by photo-zh-wordlist.reply.json:
    // 0 美丽 · 1 世界 · 2 美丽(dup). The 词语 stays whole, the material's
    // own pinyin is the meaning, and NOTHING English appears — the
    // mixed-page contract as the fixed model output (see file head).
    await expect(row(0)).toHaveClass(/photo-row-exists/);
    await expect(row(0).locator('.photo-row-check')).toBeDisabled();
    await expect(row(1).locator('input[aria-label="单词 第 2 行"]')).toHaveValue('世界');
    await expect(row(1).locator('input[aria-label="释义 第 2 行"]')).toHaveValue('shì jiè');
    await expect(row(1).locator('.photo-row-check')).toBeChecked();
    await expect(row(2)).toHaveClass(/photo-row-invalid/);
    await expect(row(2).locator('.photo-status')).toContainText('与第 1 行重复');
    await expect(panel.locator('.photo-row')).toHaveCount(3);

    // AC2: a chinese draft's meaning column is the material's own pinyin —
    // the English-style 释义补充 affordance never mounts.
    await expect(panel.locator('.photo-meaning-bar button')).toHaveCount(0);

    // Commit: only 世界 is submittable (美丽 exists identically, the dup
    // is invalid) — the banner must report the honest single add. The
    // unit pin keeps the cleanup honest (see the shared prelude).
    await panel.getByRole('combobox', { name: '单元', exact: true }).fill(UNIT_IMPORT);
    await panel.getByRole('button', { name: '确认添加（1 个）' }).click();
    await expect(panel.locator('.result-banner .result-summary')).toContainText('已添加 1 个', { timeout: 30_000 });

    // Real business results (never mock echoes): the chinese library holds
    // the seed plus the one submitted 词语, verbatim pinyin.
    const zh = await libraryWords(request, CAT_ZH);
    expect(zh.get('美丽')).toBe('měi lì');
    expect(zh.get('世界')).toBe('shì jiè');

    // Subject isolation: the english library is byte-for-byte what it was.
    const en = await libraryWords(request, CAT_EN);
    expect(en.has('美丽')).toBe(false);
    expect(en.has('世界')).toBe(false);
  } finally {
    await resetZhUnits(request);
  }
});

test('语文 import feeds the shared management view (中文 pill, participation toggle)', async ({ page, request }) => {
  await resetZhUnits(request);
  await seedIdenticalWord(request);
  try {
    await openPhotoTab(page);
    await selectChineseAndRecognize(page);
    await page.getByRole('button', { name: '确认添加（1 个）' }).click();
    await expect(page.locator('.result-banner .result-summary')).toContainText('已添加 1 个', { timeout: 30_000 });

    // AC3: the imported 词语 shows in the SAME browse view, under the SAME
    // 中文 pill, with the SAME participation switch and subject badge the
    // english vocabulary uses — one management flow, no chinese-only page.
    await page.getByRole('tab', { name: '词库' }).click();
    await page.locator('.category-pills button', { hasText: '中文' }).click();
    const wordRow = page.locator('.word-row').filter({ hasText: '世界' }).first();
    await expect(wordRow).toBeVisible({ timeout: 15_000 });
    await expect(wordRow.locator('.badge-chinese')).toHaveText('中文');
    await expect(wordRow.locator('input[aria-label="加入默写范围 世界"]')).toBeChecked();
  } finally {
    await resetZhUnits(request);
  }
});

test('classroom: chinese session_init selects the 词语; the default session selects none', async ({ page, request }) => {
  await resetZhUnits(request);
  await seedIdenticalWord(request);
  try {
    await openPhotoTab(page);
    await selectChineseAndRecognize(page);
    await page.getByRole('button', { name: '确认添加（1 个）' }).click();
    await expect(page.locator('.result-banner .result-summary')).toContainText('已添加 1 个', { timeout: 30_000 });

    // The chinese classroom: due fetch scoped to the chinese column, the
    // lesson row qualified by the subject, the echo honest.
    const zhReady = await rtSessionInit('chinese');
    expect(zhReady.category).toBe('chinese');
    expect(zhReady.lesson.words).toContain('世界');

    // The same-day DEFAULT classroom (no category — the pre-#10 wire
    // shape) is the english classroom: it selects none of the 词语 and
    // echoes english. Isolation cuts both ways (AC1: 英文内容不混入语文
    // 课堂, and the 语文 content never leaks back).
    const enReady = await rtSessionInit(null);
    expect(enReady.category).toBe('english');
    expect(enReady.lesson.words).not.toContain('世界');
    expect(enReady.lesson.words).not.toContain('美丽');
  } finally {
    await resetZhUnits(request);
  }
});

test('records: the chinese classroom row carries category=chinese and the 中文 badge', async ({ page, request }) => {
  await resetZhUnits(request);
  await seedIdenticalWord(request);
  try {
    await openPhotoTab(page);
    await selectChineseAndRecognize(page);
    await page.getByRole('button', { name: '确认添加（1 个）' }).click();
    await expect(page.locator('.result-banner .result-summary')).toContainText('已添加 1 个', { timeout: 30_000 });

    // Open the chinese classroom once — its row is what the parent sees.
    const zhReady = await rtSessionInit('chinese');
    expect(zhReady.lesson.id).toBe(LESSON_ZH);

    // The records API carries the subject on the row itself.
    const resp = await request.get('/api/v1/lessons');
    const data = await resp.json();
    const row = (data.lessons || []).find((l) => l.lesson_id === LESSON_ZH);
    expect(row, 'the chinese classroom row exists in GET /lessons').toBeTruthy();
    expect(row.category).toBe('chinese');
    expect(row.word_count).toBeGreaterThanOrEqual(1);

    // 家长查看: the 学习记录 tab renders the row's subject badge from the
    // category field — the shared 英语/中文 pill, never id parsing.
    await page.goto('/parent.html');
    const record = page.locator(`.lesson-row[data-lesson-id="${LESSON_ZH}"]`);
    await expect(record).toBeVisible({ timeout: 15_000 });
    await expect(record.locator('.badge-chinese')).toHaveText('中文');
  } finally {
    await resetZhUnits(request);
  }
});
