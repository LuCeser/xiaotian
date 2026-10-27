/**
 * @file Parent page — batch word/meaning edit preview (Issue #80, AC-09).
 *
 * Pure functions ONLY: the parent pastes a corrected word list over the
 * words selected in 词库浏览, and this file turns that paste into an
 * honest PREVIEW — which faces change, which meanings change, which edits
 * an identity conflict must block (ADR-0003: UNIQUE(word, category) is
 * the content identity; a rename onto an existing face never overwrites
 * the other card). Nothing here touches the network or the DOM; the
 * commit wiring (逐条 PUT /words/{id}) is the browse view's job and the
 * preview's 取消 simply drops it. Node-tested via the window-or-module
 * global tail (state-manager.js convention).
 *
 * Contract:
 *   - The paste rides the 批量添加 parser's line contract (parseBatch):
 *     one word per line, word<TAB>meaning preferred, '#' comments and
 *     blank lines skipped, per-line problems reported by 1-based line.
 *   - Alignment is POSITIONAL: the k-th pasted row edits the k-th
 *     selected word. A count mismatch refuses the whole preview (a
 *     positional edit over a miscounted paste would land edits on the
 *     wrong cards — no best-effort guessing).
 *   - A row whose face AND meaning are unchanged is reported as
 *     unchanged, never as an edit.
 *   - A face change onto a face the library already carries at a
 *     DIFFERENT id is an identity conflict: that edit is BLOCKED (kept in
 *     the preview with its reason); it can never silently overwrite the
 *     other card's learning history.
 *   - A meaning longer than the API cap is blocked the same way
 *     (maxMeaningRunes in pkg/web/api/validate.go).
 */

(function (global) {
  'use strict';

  /** Mirrors the API cap: pkg/web/api/validate.go maxMeaningRunes = 1000. */
  var MAX_MEANING_RUNES = 1000;

  /** Count Unicode code points (code points, not UTF-16 units). */
  function runeCount(text) {
    return Array.from(text).length;
  }

  /**
   * alignPastedEdits — the positional alignment of a parsed paste onto
   * the selected library words. Pure.
   * @param {Array<{id, word, meaning}>} selected - the checked browse
   *   rows, in list order (their CURRENT faces are the from-side).
   * @param {Array<{word, meaning}>} rows - parseBatch's valid rows.
   * @returns {{ok: true, edits: Array<{id, wordFrom, wordTo,
   *              meaningFrom, meaningTo}>}}
   *          | {ok: false, code: 'count-mismatch', pasted: number,
   *             selected: number}
   */
  function alignPastedEdits(selected, rows) {
    var sel = selected || [];
    var pasted = rows || [];
    if (sel.length !== pasted.length) {
      return {
        ok: false,
        code: 'count-mismatch',
        pasted: pasted.length,
        selected: sel.length,
      };
    }
    var edits = [];
    for (var i = 0; i < sel.length; i++) {
      edits.push({
        id: sel[i].id,
        wordFrom: sel[i].word,
        wordTo: pasted[i].word,
        meaningFrom: sel[i].meaning == null ? '' : String(sel[i].meaning),
        meaningTo: pasted[i].meaning == null ? '' : String(pasted[i].meaning),
      });
    }
    return { ok: true, edits: edits };
  }

  /**
   * previewBatchEdits — the full preview verdict over the alignment.
   * Pure; the caller supplies the library snapshot (GET /words shape —
   * it INCLUDES the selected rows, so a rename onto another selected
   * word's face is caught as the same identity conflict).
   * @param {Array<{id, word, meaning}>} selected
   * @param {Array<{word, meaning}>} rows - parseBatch's valid rows
   * @param {Array<{id, word}>} libraryWords
   * @returns {{ok: boolean, code?: string, pasted?: number, selected?: number,
   *            edits: Array<{id, wordFrom, wordTo, meaningFrom, meaningTo,
   *                           kind: 'edit'|'unchanged',
   *                           blocked?: 'identity-conflict'|'meaning-too-long',
   *                           conflictId?: number}>,
   *            unchanged: number}}
   */
  function previewBatchEdits(selected, rows, libraryWords) {
    var aligned = alignPastedEdits(selected, rows);
    if (!aligned.ok) {
      return {
        ok: false,
        code: aligned.code,
        pasted: aligned.pasted,
        selected: aligned.selected,
        edits: [],
        unchanged: 0,
      };
    }
    // The library's face → id map (last write wins is irrelevant here:
    // faces are unique per category in a consistent snapshot).
    var idByFace = {};
    (libraryWords || []).forEach(function (w) {
      if (w && w.word) {
        idByFace[w.word] = w.id;
      }
    });
    var unchanged = 0;
    var edits = aligned.edits.map(function (e) {
      var faceChanged = e.wordTo !== e.wordFrom;
      var meaningChanged = e.meaningTo !== e.meaningFrom;
      if (!faceChanged && !meaningChanged) {
        unchanged += 1;
        return Object.assign({}, e, { kind: 'unchanged' });
      }
      var preview = Object.assign({}, e, { kind: 'edit' });
      // ADR-0003: the new face may only be this card's own; a face the
      // library carries at another id belongs to another card's identity
      // and its history — the edit is blocked, never an overwrite.
      if (faceChanged &&
          Object.prototype.hasOwnProperty.call(idByFace, e.wordTo) &&
          idByFace[e.wordTo] !== e.id) {
        preview.blocked = 'identity-conflict';
        preview.conflictId = idByFace[e.wordTo];
      } else if (runeCount(e.meaningTo) > MAX_MEANING_RUNES) {
        preview.blocked = 'meaning-too-long';
      }
      return preview;
    });
    return { ok: true, edits: edits, unchanged: unchanged };
  }

  /** Blocked-only helper for the UI's refusal line (pure). */
  function blockedEdits(preview) {
    return ((preview && preview.edits) || []).filter(function (e) {
      return e.blocked;
    });
  }

  // Window-or-module-global tail (state-manager.js convention).
  global.alignPastedEdits = alignPastedEdits;
  global.previewBatchEdits = previewBatchEdits;
  global.blockedEdits = blockedEdits;
})(typeof window !== 'undefined' ? window : this);
