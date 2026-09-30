/**
 * @file Parent page — batch paste parser (Phase 9, Plan 06, D-10).
 *
 * Pure function: turns one pasted lesson (one word per line) into commit
 * rows + per-line problems. No DOM access at all — node-tested via the
 * window-or-module-global tail (state-manager.js convention).
 *
 * Contract (Plan 09-06 behavior block + RESEARCH parser contract):
 *   - One word per line; word<TAB>meaning is the preferred split.
 *   - No tab → split at the FIRST whitespace run; remaining spaces stay
 *     in the meaning ("apple 一个 红色的" → meaning "一个 红色的").
 *   - Lines starting with '#' (after trimming) and blank/whitespace-only
 *     lines (no tab) are skipped — neither rows nor errors.
 *   - A line consisting only of separators (e.g. a lone tab) → error
 *     {line, reason:'empty-word'} — never a silent skip.
 *   - A word containing an ASCII comma → error
 *     {line, reason:'word-comma'} — the delete API encodes word lists as
 *     comma-joined argv that the Python engine splits back on ",", so a
 *     comma word can never be deleted as itself (CR-01).
 *   - A word longer than 200 runes (code points, not UTF-16 units) →
 *     error {line, reason:'word-too-long'} — matches the API's
 *     entries[i].word cap (pkg/web/api/validate.go maxWordRunes).
 *   - A duplicate word WITHIN one paste → error on the SECOND occurrence,
 *     {line, reason:'duplicate:<firstLine>'}; the first stays a valid
 *     row (UI-SPEC Interaction contract: the repeat is identified).
 *   - Per-line problems are collected — one bad line never aborts the
 *     whole batch. Trailing \r (Windows paste) is trimmed first.
 *
 * Returns { rows: [{word, meaning}], errors: [{line, reason}] } with
 * 1-based line numbers counting every physical line (skipped ones too),
 * so error.line always matches what the parent sees in the textarea.
 */

(function (global) {
  'use strict';

  /** Mirrors the API cap: pkg/web/api/validate.go maxWordRunes = 200. */
  var MAX_WORD_RUNES = 200;

  /**
   * Count Unicode code points (runes), not UTF-16 code units —
   * Array.from splits on code point boundaries, so an emoji counts as
   * one rune exactly like the Go-side []rune length check.
   */
  function runeCount(word) {
    return Array.from(word).length;
  }

  /**
   * Parse pasted lesson text. See the file contract above.
   * @param {string} text - raw textarea content (may be null/undefined)
   * @returns {{rows: Array<{word: string, meaning: string}>,
   *            errors: Array<{line: number, reason: string}>}}
   */
  function parseBatch(text) {
    var rows = [];
    var errors = [];
    var firstLineByWord = {}; // word → line number of first occurrence

    var lines = String(text == null ? '' : text).split('\n');
    for (var i = 0; i < lines.length; i++) {
      var lineNumber = i + 1;
      // Windows paste artifact: strip the trailing \r of CRLF lines.
      var line = lines[i].replace(/\r$/, '');

      var tabIndex = line.indexOf('\t');
      var trimmed = line.trim();

      // Comments first — even one carrying a tab stays a comment.
      if (trimmed.charAt(0) === '#') {
        continue;
      }
      // Blank/whitespace-only lines (no tab) are skipped silently; a
      // tab line with an empty word side falls through to empty-word.
      if (trimmed === '' && tabIndex === -1) {
        continue;
      }

      var word;
      var meaning;
      if (tabIndex !== -1) {
        // Preferred split: word<TAB>meaning (each side trimmed).
        word = line.slice(0, tabIndex).trim();
        meaning = line.slice(tabIndex + 1).trim();
      } else {
        // Fallback: split the trimmed line at the FIRST whitespace run;
        // remaining spaces stay in the meaning.
        var firstSpace = trimmed.match(/\s/);
        if (firstSpace) {
          word = trimmed.slice(0, firstSpace.index);
          meaning = trimmed.slice(firstSpace.index).trim();
        } else {
          word = trimmed;
          meaning = '';
        }
      }

      if (word === '') {
        errors.push({ line: lineNumber, reason: 'empty-word' });
        continue;
      }
      // Mirrors the API's comma rejection (pkg/web/api/validate.go, CR-01):
      // the delete path joins word lists with "," and Python splits back
      // on ",", so a comma word could never round-trip as itself.
      if (word.indexOf(',') !== -1) {
        errors.push({ line: lineNumber, reason: 'word-comma' });
        continue;
      }
      if (runeCount(word) > MAX_WORD_RUNES) {
        errors.push({ line: lineNumber, reason: 'word-too-long' });
        continue;
      }
      if (Object.prototype.hasOwnProperty.call(firstLineByWord, word)) {
        errors.push({
          line: lineNumber,
          reason: 'duplicate:' + firstLineByWord[word],
        });
        continue;
      }

      firstLineByWord[word] = lineNumber;
      rows.push({ word: word, meaning: meaning });
    }

    return { rows: rows, errors: errors };
  }

  // Window-or-module-global tail (state-manager.js:295-296 pattern) so
  // the browser gets window.parseBatch and Node tests can require() it.
  global.parseBatch = parseBatch;
})(typeof window !== 'undefined' ? window : this);
