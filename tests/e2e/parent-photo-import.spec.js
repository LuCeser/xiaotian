/**
 * Parent photo-import E2E (Issue #3): the 英语词表照片识别 → 确认 → 投入默写
 * chain against the ISOLATED real service.
 *
 * Test boundary (Issue #3's shared rule): ONLY the external recognition
 * service is replaced — the local mimo stub (tests/e2e/helpers/mimo-stub.js,
 * reached through voice.baseUrl + a dummy MIMO_API_KEY) answers vision
 * calls with fixture-matched canned replies. Everything else is the real
 * stack: real browser → real Go service → real Python bridge → real SQLite
 * (per-run temp DB), and the classroom check drives the real /rt WebSocket.
 *
 * Flows:
 *   1. full import — recognize a word-list PNG (the stub's reply includes
 *      a conflicting existing word, an identical existing word, and an
 *      in-photo duplicate), edit a meaning, uncheck a row, commit; the
 *      per-item result banner must show partial success as partial, and
 *      the real library must contain exactly what was submitted (edits
 *      honored, conflicts kept their original meaning, nothing else).
 *   2. cancel — 放弃识别结果 provably writes nothing (API snapshot diff).
 *   3. failure + retry — an unregistered image makes the stub answer 500;
 *      the UI shows the reason and the SAME form retries successfully.
 *   4. classroom selection — after a real commit, a raw /rt session_init
 *      (mode=test for the full-reveal lesson view) receives session_ready
 *      whose lesson words include the photo-imported words: the import
 *      really feeds dictation.
 *
 * Test-data hygiene (T-09-12): writes confined to e2e-photo-* units,
 * removed in finally-shaped cleanups, so the suite stays idempotent.
 */
// @ts-check
const { test, expect } = require('@playwright/test');
const path = require('node:path');
const WebSocket = require('ws');

const CAT = 'english';
const UNIT_IMPORT = 'e2e-photo-import';
const UNIT_SEED = 'e2e-photo-seed';
const UNIT_CLASSROOM = 'e2e-photo-classroom';

const FIXTURES = path.resolve(__dirname, 'fixtures');
const FIXTURE_MAIN = path.join(FIXTURES, 'photo-wordlist.png');
const FIXTURE_CLASSROOM = path.join(FIXTURES, 'photo-wordlist2.png');

// A valid 1x1 PNG the stub has NO registered reply for: the stub answers
// 500, which the service must surface as a retryable recognition failure.
const UNREGISTERED_PNG = Buffer.from(
  'iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg==',
  'base64');

/** DELETE one whole unit through the real API. A 404 (already gone) is fine. */
async function deleteUnit(request, unit) {
  await request.delete('/api/v1/words', { data: { category: CAT, unit } }).catch(() => {});
}

/** Real-library snapshot: word → meaning for the english category. */
async function libraryWords(request) {
  const resp = await request.get('/api/v1/words?category=' + CAT);
  const data = await resp.json();
  const map = new Map();
  for (const w of data.words || []) {
    map.set(w.word, w.meaning);
  }
  return map;
}

/** Open the parent page and switch into the 照片导入 tab of 添加单词. */
async function openPhotoTab(page) {
  await page.goto('/parent.html');
  await page.getByRole('tab', { name: '词库' }).click();
  await expect(page.locator('#addSection')).toBeVisible();
  await page.getByRole('tab', { name: '照片导入' }).click();
  const panel = page.locator('#add-panel-photo');
  await expect(panel).toBeVisible();
  return panel;
}

/** Upload a file, run 开始识别, and wait for the review rows to render.
 *  Issue #10: the SAME entry now requires the subject radio — this suite
 *  imports English word lists, so 英语 is selected explicitly (the
 *  pre-#10 fixed subject became a pick, never a silent default). */
async function recognize(page, file) {
  await page.locator('input[name="parent-category-photo"][value="english"]').check();
  await page.locator('#parent-photo-file').setInputFiles(file);
  await page.getByRole('button', { name: '开始识别' }).click();
  await expect(page.locator('.photo-row').first()).toBeVisible({ timeout: 30_000 });
}

test('photo import: edit → check → commit lands in the real library, partial shown as partial', async ({ page, request }) => {
  await deleteUnit(request, UNIT_IMPORT);
  await deleteUnit(request, UNIT_SEED);
  // Seed the two existing-word cases: river identical, apple conflicting.
  const seed = await request.post('/api/v1/words', {
    data: {
      category: CAT, unit: UNIT_SEED,
      entries: [
        { word: 'river', meaning: '河流' },
        { word: 'apple', meaning: '苹果（水果）' },
      ],
    },
  });
  expect(seed.ok()).toBeTruthy();

  try {
    await openPhotoTab(page);
    await recognize(page, FIXTURE_MAIN);

    // The stub reply order is pinned by photo-wordlist.reply.json:
    // 0 apple · 1 banana · 2 ice cream · 3 go for a walk · 4 river · 5 banana(dup)
    const row = (i) => page.locator(`.photo-row[data-row-index="${i}"]`);

    // Conflict: names the library's meaning and keeps it; not submittable.
    await expect(row(0)).toHaveClass(/photo-row-conflict/);
    await expect(row(0).locator('.photo-status')).toContainText('词库释义：苹果（水果）');
    await expect(row(0).locator('.photo-row-check')).toBeDisabled();
    // Identical existing word.
    await expect(row(4)).toHaveClass(/photo-row-exists/);
    // In-photo duplicate flags the FIRST occurrence.
    await expect(row(5)).toHaveClass(/photo-row-invalid/);
    await expect(row(5).locator('.photo-status')).toContainText('与第 2 行重复');
    // New rows: checked; the phrase keeps its spaces as ONE entry.
    await expect(row(2).locator('.photo-row-check')).toBeChecked();
    await expect(row(2).locator('input[aria-label="单词 第 3 行"]')).toHaveValue('ice cream');

    // Parent's final edits govern: change one meaning, uncheck one row.
    await row(3).locator('input[aria-label="释义 第 4 行"]').fill('去散步');
    await row(2).locator('.photo-row-check').uncheck();
    await expect(page.getByRole('button', { name: '确认添加（2 个）' })).toBeVisible();

    // Commit through the real REST → Python → SQLite path.
    await page.getByRole('button', { name: '确认添加（2 个）' }).click();

    // Partial success is never a plain success: 已添加 2 + 未提交 4.
    const banner = page.locator('.result-banner');
    await expect(banner).toHaveClass(/result-banner-warning/);
    await expect(banner.locator('.result-summary')).toContainText('已添加 2 个');
    await expect(banner.locator('.result-summary')).toContainText('未提交 4 个');
    await expect(banner.locator('.result-detail').first()).toContainText('已添加：banana、go for a walk');
    // Every unsubmitted word is listed with ITS reason (per-item honesty).
    await expect(banner).toContainText('apple（释义不同）');
    await expect(banner).toContainText('river（已有）');
    await expect(banner).toContainText('ice cream（未勾选）');
    await expect(banner).toContainText('banana（与第 2 行重复）');

    // The REAL library carries exactly the parent's final edits…
    const lib = await libraryWords(request);
    expect(lib.get('banana')).toBe('香蕉');
    expect(lib.get('go for a walk')).toBe('去散步');
    // …the unchecked row was NOT written…
    expect(lib.has('ice cream')).toBe(false);
    // …and the conflicting word kept its original meaning (no silent
    // overwrite; the library's value survives byte-for-byte).
    expect(lib.get('apple')).toBe('苹果（水果）');

    // The browse list refreshes without a reload.
    await expect(page.locator('.word-row[data-word="banana"]')).toBeVisible({ timeout: 15_000 });
    await expect(page.locator('.word-row[data-word="go for a walk"]')).toBeVisible();
  } finally {
    await deleteUnit(request, UNIT_IMPORT);
    await deleteUnit(request, UNIT_SEED);
  }
});

test('photo import: 放弃识别结果 writes nothing to the real library', async ({ page, request }) => {
  await deleteUnit(request, UNIT_IMPORT);
  const before = await libraryWords(request);
  try {
    const panel = await openPhotoTab(page);
    await recognize(page, FIXTURE_MAIN);
    await expect(page.locator('.photo-row')).toHaveCount(6);

    await page.getByRole('button', { name: '放弃识别结果' }).click();
    await expect(page.locator('.photo-row')).toHaveCount(0);
    // Scoped to the photo panel: the batch tab carries its own preview-empty.
    await expect(panel.locator('.preview-empty')).toBeVisible();

    // The recognition draft was client-side only: the real library is
    // byte-identical to the pre-recognition snapshot.
    const after = await libraryWords(request);
    expect(after.size).toBe(before.size);
    for (const [word, meaning] of before) {
      expect(after.get(word)).toBe(meaning);
    }
  } finally {
    await deleteUnit(request, UNIT_IMPORT);
  }
});

test('photo import: recognition failure shows the reason and retry succeeds', async ({ page, request }) => {
  await deleteUnit(request, UNIT_IMPORT);
  try {
    await openPhotoTab(page);

    // Unregistered image → stub 500 → service 502 recognition_failed →
    // the form shows the reason; the same form stays usable for a retry.
    await page.locator('input[name="parent-category-photo"][value="english"]').check();
    await page.locator('#parent-photo-file').setInputFiles({
      name: 'unregistered.png', mimeType: 'image/png', buffer: UNREGISTERED_PNG,
    });
    await page.getByRole('button', { name: '开始识别' }).click();
    await expect(page.locator('#parent-photo-form-error')).toContainText('识别失败', { timeout: 30_000 });
    await expect(page.locator('.photo-row')).toHaveCount(0);

    // Retry with a registered word-list photo on the SAME form.
    await recognize(page, FIXTURE_MAIN);
    await expect(page.locator('.photo-row')).toHaveCount(6);
  } finally {
    await deleteUnit(request, UNIT_IMPORT);
  }
});

test('photo import: reconcile-failure never shows server-confirmed adds as failures', async ({ page, request }) => {
  await deleteUnit(request, UNIT_IMPORT);
  await deleteUnit(request, UNIT_CLASSROOM);
  try {
    const panel = await openPhotoTab(page);
    await recognize(page, FIXTURE_CLASSROOM); // sun + moon, both new
    // Scoped: the single/batch forms carry their own 单元 comboboxes.
    await panel.getByRole('combobox', { name: '单元', exact: true }).fill(UNIT_IMPORT);

    // The race: ANOTHER device adds 'sun' (different meaning) after this
    // page's snapshot was taken — the commit will add moon, skip sun.
    const raced = await request.post('/api/v1/words', {
      data: { category: CAT, unit: UNIT_CLASSROOM, entries: [{ word: 'sun', meaning: '太阳系' }] },
    });
    expect(raced.ok()).toBeTruthy();

    // And the post-commit snapshot fetch fails (network blip): the banner
    // must fall back to the server's counts — 已添加 1（服务器确认）＋未核对
    // — never naming the added word a failure.
    await page.route('**/api/v1/words?category=english*', async (route) => {
      await route.abort('failed');
    }, { times: 6 });
    await page.getByRole('button', { name: '确认添加（2 个）' }).click();

    const banner = page.locator('.result-banner');
    await expect(banner.locator('.result-summary')).toContainText('已添加 1 个', { timeout: 30_000 });
    await expect(banner.locator('.result-summary')).toContainText('未核对 2 个');
    await expect(banner).toContainText('服务器确认');
    await expect(banner).toContainText('未核对（词库读取失败，请刷新词库查看）：sun、moon');
    await expect(banner).not.toContainText('未添加成功');
    await page.unroute('**/api/v1/words?category=english*');

    // The library truth (real reads): moon added by the page, sun kept the
    // racing device's meaning (original preserved, no overwrite).
    const lib = await libraryWords(request);
    expect(lib.get('moon')).toBe('月亮');
    expect(lib.get('sun')).toBe('太阳系');
    expect(panel).toBeVisible(); // form still alive for the next photo
  } finally {
    await deleteUnit(request, UNIT_IMPORT);
    await deleteUnit(request, UNIT_CLASSROOM);
  }
});

test('classroom: photo-imported words are selected for dictation', async ({ page, request }) => {
  await deleteUnit(request, UNIT_CLASSROOM);
  try {
    await openPhotoTab(page);
    await recognize(page, FIXTURE_CLASSROOM);
    await page.getByRole('button', { name: '确认添加（2 个）' }).click();
    await expect(page.locator('.result-banner .result-summary')).toContainText('已添加 2 个', { timeout: 30_000 });

    // Real classroom start: raw /rt session_init (mode=test selects the
    // full-reveal lesson view — safe: this instance's DB is a per-run
    // throwaway). session_ready carries the SELECTED words; it is sent
    // before any announcement audio, so stubbed TTS traffic cannot race
    // the assertion.
    const ready = await new Promise((resolve, reject) => {
      const ws = new WebSocket('ws://127.0.0.1:18080/rt', {
        origin: 'http://127.0.0.1:18080',
      });
      const fail = setTimeout(() => reject(new Error('session_ready timeout')), 30_000);
      ws.on('error', (err) => { clearTimeout(fail); reject(err); });
      ws.on('open', () => {
        ws.send(JSON.stringify({
          type: 'session_init',
          audio: { sampleRate: 16000, format: 's16le', channels: 1 },
          mode: 'test',
        }));
      });
      ws.on('message', (data) => {
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

    const selected = ready.lesson.words;
    expect(selected).toContain('sun');
    expect(selected).toContain('moon');
  } finally {
    await deleteUnit(request, UNIT_CLASSROOM);
  }
});

// ---------------------------------------------------------------------------
// Issue #42 — the 单词 column of the review table must stay VISUALLY
// readable. The acceptance run caught an auto-table-layout collapse: the
// width:100% inputs have a near-zero min-content, so the fixed-size cells
// (nowrap badge, status chip) starved the 单词 track down to 34px (an 18px
// input) while every DOM-value assertion stayed green — the words were in
// the DOM the whole time, just invisible. Discriminators here are
// GEOMETRY, not values: a column narrower than the floor fails even
// though toHaveValue('ice cream') keeps passing.
// ---------------------------------------------------------------------------
test('review rows: the 单词 column stays visually readable (Issue #42 width regression)', async ({ page, request }) => {
  await deleteUnit(request, UNIT_IMPORT);
  try {
    await openPhotoTab(page);
    await recognize(page, FIXTURE_MAIN);
    const row = (i) => page.locator(`.photo-row[data-row-index="${i}"]`);

    /** Geometry of one row's word/meaning inputs + the column headers. */
    await row(2).scrollIntoViewIfNeeded();
    const geo = await page.evaluate(() => {
      const pick = (sel) => document.querySelector(sel);
      const word = pick('.photo-row input[aria-label="单词 第 3 行"]');
      const meaning = pick('.photo-row input[aria-label="释义 第 3 行"]');
      const ths = Array.from(document.querySelectorAll('.photo-preview-table thead th'));
      return {
        wordW: word.getBoundingClientRect().width,
        wordH: word.getBoundingClientRect().height,
        meaningW: meaning.getBoundingClientRect().width,
        cols: ths.map((th) => ({ label: th.textContent.trim(), w: th.getBoundingClientRect().width })),
        tableW: pick('.photo-preview-table').getBoundingClientRect().width,
      };
    });
    const col = (name) => geo.cols.find((c) => c.label === name);

    // The 34px-collapse regression floor: the word INPUT must show real
    // text (~116px under the fixed layout; 80px leaves margin for the
    // 4-column mode and font tweaks without letting a collapse pass).
    expect(geo.wordW, '单词 input visible width >= 80px').toBeGreaterThanOrEqual(80);
    expect(geo.wordH, 'in-cell touch floor stays 44px (Issue #18)').toBeGreaterThanOrEqual(44);
    // The column track itself keeps its floor and never becomes the
    // narrowest content column...
    expect(col('单词').w, '单词 column >= 120px').toBeGreaterThanOrEqual(120);
    // ...while 释义（原文） stays the visual center (widest column) — the
    // fixed-layout contract that makes the split deterministic.
    expect(col('释义（原文）').w, '释义 stays the widest column').toBeGreaterThan(col('单词').w);
    expect(geo.meaningW).toBeGreaterThan(80);
    // Issue #81: inside the ≥1024px side-by-side workspace the table floor
    // dropped from 640px to 560px to give the review column more room
    // beside the material rail. The per-column acceptance floors above are
    // unchanged (the table still overflows its own scroller sideways when
    // the available width is tighter than the floor).
    expect(geo.tableW, 'table keeps its 560px min-width floor').toBeGreaterThanOrEqual(560);
  } finally {
    await deleteUnit(request, UNIT_IMPORT);
  }
});

// ---------------------------------------------------------------------------
// Issue #18 — the review rows' inline checkboxes/radios meet the 44px
// touch floor. The input keeps its 18px visual size; the hit area is the
// wrapping <label> (Chrome zeroes USED padding on appearance:auto
// checkboxes, so the reverted padding + negative-margin approach on the
// input itself cannot work). Discriminators are behavioral, not CSS
// declarations: a tap INSIDE the 44px label but OUTSIDE the 18px input
// must toggle — under the bare-input implementation that corner is empty
// cell space and the tap does nothing.
// ---------------------------------------------------------------------------
test('review rows: checkbox and conflict radios meet the 44px touch floor (Issue #18)', async ({ page, request }) => {
  await deleteUnit(request, UNIT_IMPORT);
  await deleteUnit(request, UNIT_SEED);
  // Same seed as the main flow: apple conflicts, river exists.
  const seed = await request.post('/api/v1/words', {
    data: {
      category: CAT, unit: UNIT_SEED,
      entries: [
        { word: 'river', meaning: '河流' },
        { word: 'apple', meaning: '苹果（水果）' },
      ],
    },
  });
  expect(seed.ok()).toBeTruthy();

  try {
    await openPhotoTab(page);
    await recognize(page, FIXTURE_MAIN);
    const row = (i) => page.locator(`.photo-row[data-row-index="${i}"]`);

    /** Label/input geometry for one .photo-row-check. */
    const hitMetrics = (i) => row(i).locator('.photo-row-check').evaluate((el) => {
      const label = el.closest('label');
      const l = (label || el).getBoundingClientRect();
      const b = el.getBoundingClientRect();
      return {
        wrapped: !!label,
        labelX: l.x, labelY: l.y, labelW: l.width, labelH: l.height,
        inputW: b.width, inputH: b.height,
      };
    });

    // Row 2 (ice cream) is committable and checked: 44px label hit area,
    // 18px visual input. The review table renders below the fold — bring
    // each probed row into the viewport FIRST: the metrics and the corner
    // taps below are viewport-coordinate based (page.mouse).
    await row(2).scrollIntoViewIfNeeded();
    const m = await hitMetrics(2);
    expect(m.wrapped, 'the row checkbox rides a wrapping label (Issue #18 方案 A)').toBe(true);
    expect(m.labelW, 'hit area width >= 44px').toBeGreaterThanOrEqual(44);
    expect(m.labelH, 'hit area height >= 44px').toBeGreaterThanOrEqual(44);
    expect(m.inputW, 'visual size stays ~18px (no giant checkbox)').toBeLessThanOrEqual(20);
    expect(m.inputH, 'visual size stays ~18px (no giant checkbox)').toBeLessThanOrEqual(20);

    // A corner tap 3px inside the label is 10px OUTSIDE the centered
    // 18px input — it must still toggle (unchecks the checked row).
    await page.mouse.click(m.labelX + m.labelW - 3, m.labelY + m.labelH - 3);
    await expect(row(2).locator('.photo-row-check')).not.toBeChecked();

    // The disabled conflict row's corner tap stays a no-op: label
    // forwarding never bypasses the disabled state.
    await row(0).scrollIntoViewIfNeeded();
    const c = await hitMetrics(0);
    expect(c.wrapped).toBe(true);
    await page.mouse.click(c.labelX + 3, c.labelY + 3);
    await expect(row(0).locator('.photo-row-check')).toBeDisabled();
    await expect(row(0).locator('.photo-row-check')).not.toBeChecked();

    // Textbook mode renders the conflict radios; their labels are 44px
    // tall and tapping the option TEXT (not the 18px radio) resolves.
    // Scoped to row 0: earlier suites may leave other conflicting words
    // in the shared per-run library, which legitimately render their own
    // choice blocks (the suite's DB is per-run, not per-test).
    await page.locator('#parent-photo-textbook').selectOption('new');
    await expect(row(0).locator('.photo-conflict-choice')).toHaveCount(1);
    const optionH = await row(0).locator('.photo-conflict-option').first()
      .evaluate((el) => el.getBoundingClientRect().height);
    expect(optionH, 'conflict option label height >= 44px').toBeGreaterThanOrEqual(44);
    await row(0).locator('.photo-conflict-option').first().click();
    await expect(row(0).locator('.photo-conflict-choice input[value="keep"]')).toBeChecked();
  } finally {
    await deleteUnit(request, UNIT_IMPORT);
    await deleteUnit(request, UNIT_SEED);
  }
});
