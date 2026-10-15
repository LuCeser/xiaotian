/**
 * @file Parent page — 扫描 PDF 拆页运行时 (Issue #7): loads the vendored
 *       pdf.js, renders ONE page at a time into a ≤768KiB JPEG blob, and
 *       hands it to the ordinary photo-recognize call. The per-page state
 *       machine, merge and slicing rules live in parent-pdf-core.js
 *       (pure, Node-tested); this file is the pdf.js + canvas + DOM I/O
 *       half, covered by the browser e2e (ADR-0004 #1/#2).
 *
 * Contract (Issue #7 acceptance criteria):
 *   - PdfPageRunner.isAvailable() — the vendored pdf.js actually loaded;
 *     a missing script degrades the photo form to the PNG/JPEG-only path
 *     with an honest message, never a dead file picker (degrade-to-'
 *     two-tabs' precedent).
 *   - open(file) → { doc, pageCount } — the WHOLE file is read into one
 *     ArrayBuffer (the 40MiB cap was checked before this point); pdf.js
 *     keeps page data lazy, so memory stays one-buffer + one page.
 *   - renderPage(doc, no) → Promise<Blob> — one page rendered at
 *     PDF_RENDER_EDGE (~1600px long edge), white-backed, then walked down
 *     the photo ladder (global.PHOTO_QUALITY_LADDER, exported by
 *     parent-photo.js — ONE encoder of the upload budget) to stay under
 *     global.PHOTO_MAX_UPLOAD_BYTES. Sequential callers ⇒ bounded memory.
 *   - close(doc) — destroy the pdf.js document (discard / new file /
 *     post-commit reset); the page loop itself never outlives the form.
 *
 * Security (Pitfall 10): nothing here renders markup — blobs and numbers
 * only; all text surfaces live in parent-photo.js.
 */

(function (global) {
  'use strict';

  /** Longest render edge — the photo ladder's first rung (1600px). */
  var PDF_RENDER_EDGE = 1600;

  /** Upper bound on the scale factor (tiny pages must not blow up). */
  var MAX_RENDER_SCALE = 3;

  function PdfPageRunner() {}

  /** The vendored pdf.js is present (script-tag degrade check). */
  PdfPageRunner.isAvailable = function () {
    return !!(global.pdfjsLib && typeof global.pdfjsLib.getDocument === 'function');
  };

  /**
   * The vendored build's version, read from the pdf.min.js script tag's
   * own ?v= — parent.html is the ONE place the version is written; the
   * worker fetch stays cache-coherent with it without a second literal.
   */
  function vendoredVersion() {
    var tag = document.querySelector('script[src*="vendor/pdfjs/pdf.min.js"]');
    var match = tag && tag.src ? tag.src.match(/[?&]v=([^&]+)/) : null;
    return match ? match[1] : '';
  }

  /**
   * open — read the file and hand it to pdf.js. Rejects with friendly
   * Chinese messages: unreadable file, damaged/encrypted PDF. The buffer
   * is transferred to the worker (single use — retry re-opens nothing,
   * the doc stays alive for page re-render until close()).
   */
  PdfPageRunner.open = function (file) {
    if (!PdfPageRunner.isAvailable()) {
      return Promise.reject(new Error('PDF 支持未加载，请刷新页面或改用照片导入'));
    }
    // workerSrc once, stamped with the main script's own ?v= (cache
    // coherence with the tag that loaded pdfjsLib).
    global.pdfjsLib.GlobalWorkerOptions.workerSrc =
      '/vendor/pdfjs/pdf.worker.min.js' +
      (vendoredVersion() ? '?v=' + vendoredVersion() : '');
    return file.arrayBuffer().then(function (buffer) {
      return global.pdfjsLib.getDocument({ data: buffer }).promise;
    }).then(function (doc) {
      if (!doc || !doc.numPages) {
        PdfPageRunner.close(doc);
        throw new Error('这个 PDF 没有可识别的页面');
      }
      return { doc: doc, pageCount: doc.numPages };
    }, function (err) {
      if (err && err.name === 'PasswordException') {
        throw new Error('PDF 已加密，请先解除密码保护再上传');
      }
      throw new Error('无法读取这个 PDF（可能已损坏），请换一个文件');
    });
  };

  /**
   * renderPage — one page → one budget JPEG blob. The pristine render is
   * kept on its own canvas; each ladder rung that needs a smaller edge
   * scales down from the PRISTINE pixels (never progressively), mirroring
   * prepareUpload's per-rung rescale of the original image.
   */
  PdfPageRunner.renderPage = function (doc, pageNo) {
    var ladder = global.PHOTO_QUALITY_LADDER || [];
    var budget = global.PHOTO_MAX_UPLOAD_BYTES || 768 * 1024;
    return doc.getPage(pageNo).then(function (page) {
      var base = page.getViewport({ scale: 1 });
      var scale = Math.min(
        PDF_RENDER_EDGE / Math.max(base.width, base.height) || 1,
        MAX_RENDER_SCALE);
      var viewport = page.getViewport({ scale: scale });
      var canvas = document.createElement('canvas');
      canvas.width = Math.max(1, Math.floor(viewport.width));
      canvas.height = Math.max(1, Math.floor(viewport.height));
      var ctx = canvas.getContext('2d');
      // Scanned pages sit on white; a transparent PDF must not go black.
      ctx.fillStyle = '#ffffff';
      ctx.fillRect(0, 0, canvas.width, canvas.height);
      return page.render({
        canvasContext: ctx,
        viewport: viewport,
        background: '#ffffff',
      }).promise.then(function () {
        return firstBudgetBlob(canvas, ladder, budget);
      });
    });
  };

  /** close — release the pdf.js document and its worker-side data. */
  PdfPageRunner.close = function (doc) {
    if (doc && typeof doc.destroy === 'function') {
      try {
        doc.destroy();
      } catch (e) {
        // A destroyed doc is the goal; a throw on the way out is noise.
      }
    }
  };

  /**
   * firstBudgetBlob — walk the ladder over the pristine canvas: each rung
   * re-scales from the original and re-encodes JPEG until a blob fits the
   * budget. Resolves null when even the last rung stays over (the honest
   * too-large outcome — that page reports 识别失败, nothing is uploaded).
   */
  function firstBudgetBlob(canvas, ladder, budget) {
    var longest = Math.max(canvas.width, canvas.height);
    var rungs = ladder.length ? ladder : [{ edge: longest, quality: 0.6 }];
    var found = Promise.resolve(null);
    rungs.forEach(function (rung) {
      found = found.then(function (blob) {
        if (blob) {
          return blob;
        }
        var target = canvasToRung(canvas, rung, longest);
        return canvasToJpegBlob(target, rung.quality).then(function (blob) {
          return blob && blob.size <= budget ? blob : null;
        });
      });
    });
    return found;
  }

  /** The canvas for one rung: the pristine one when big enough, else a
   * white-backed downscale of it. */
  function canvasToRung(canvas, rung, longest) {
    if (longest <= rung.edge) {
      return canvas;
    }
    var scale = rung.edge / longest;
    var small = document.createElement('canvas');
    small.width = Math.max(1, Math.round(canvas.width * scale));
    small.height = Math.max(1, Math.round(canvas.height * scale));
    var ctx = small.getContext('2d');
    ctx.fillStyle = '#ffffff';
    ctx.fillRect(0, 0, small.width, small.height);
    ctx.drawImage(canvas, 0, 0, small.width, small.height);
    return small;
  }

  function canvasToJpegBlob(canvas, quality) {
    return new Promise(function (resolve) {
      canvas.toBlob(function (blob) {
        resolve(blob);
      }, 'image/jpeg', quality);
    });
  }

  // Window-or-module-global tail (state-manager.js convention).
  global.PdfPageRunner = PdfPageRunner;
})(typeof window !== 'undefined' ? window : this);
