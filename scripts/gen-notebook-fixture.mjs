// One-off generator for tests/e2e-rt/real/fixtures/notebook-words.jpg — the
// committed notebook-page photo the L3 driver uploads for the check stage's
// REAL OCR pass (13.1-07). Renders the ten seeded words handwriting-style on
// white; kept under the 768 KiB client upload cap.
import { chromium } from '@playwright/test';
import fs from 'node:fs';

const seed = JSON.parse(fs.readFileSync('tests/e2e-rt/real/seed-words.json', 'utf8'));
const words = seed.entries.map((e) => e.word);
const browser = await chromium.launch();
const page = await browser.newPage({ viewport: { width: 1400, height: 400 }, deviceScaleFactor: 1 });
await page.setContent(`<html><body style="margin:0;background:#ffffff">
  <div style="padding:70px 90px;font-family:'Segoe Script','Comic Sans MS',cursive;font-size:64px;line-height:2.3;color:#141414">
    ${words.map((w) => `<div>${w}</div>`).join('')}
  </div>
</body></html>`);
await page.screenshot({ path: 'tests/e2e-rt/real/fixtures/notebook-words.jpg', type: 'jpeg', quality: 80, fullPage: true });
await browser.close();
const size = fs.statSync('tests/e2e-rt/real/fixtures/notebook-words.jpg').size;
console.log('fixture written:', size, 'bytes');
if (size > 786432) { console.error('OVER the 768KiB client cap'); process.exit(1); }
