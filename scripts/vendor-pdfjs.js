// vendor-pdfjs.js — copies the pinned pdfjs-dist UMD build into the site's
// static vendor directory (ADR-0004: the PDF page split runs in the browser
// via pdf.js; the files ship with the site so no CDN is involved).
//
// Run after changing the pdfjs-dist version in package.json:
//   npm install && npm run vendor:pdfjs
// Copies pdf.min.js + pdf.worker.min.js (UMD build — works on the no-build
// static page and older iPad Safari) plus the Apache-2.0 license notice.
'use strict';

const fs = require('node:fs');
const path = require('node:path');

const repo = path.resolve(__dirname, '..');
const src = path.join(repo, 'node_modules', 'pdfjs-dist');
const out = path.join(repo, 'pkg', 'web', 'static', 'vendor', 'pdfjs');

const files = ['build/pdf.min.js', 'build/pdf.worker.min.js', 'LICENSE'];

fs.mkdirSync(out, { recursive: true });
for (const rel of files) {
  fs.copyFileSync(path.join(src, rel), path.join(out, path.basename(rel)));
  console.log(`vendored ${rel} -> ${path.relative(repo, path.join(out, path.basename(rel)))}`);
}
