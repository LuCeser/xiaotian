// parent-meaning-core.js — the Issue #8 supplement pure functions: which
// preview rows are eligible for meaning supplementation, how a generate
// reply mutates them, and the one display rule for the source marker
// (ADR-0005). No DOM, no fetch — the Node unit test pins the contract
// (the window.parseBatch precedent; see tests/test-parent-photo-meaning.js).
//
// Eligibility (ADR-0005 #4): ONLY rows that will take the add path with an
// empty meaning — status 'new', meaning '', still checked. exists/conflict
// rows never supplement (the library meaning governs; the Issue #6
// confirmation semantics are untouched), and an unchecked row is not worth
// an LLM call. Supplementation needs a confirmed textbook context; the
// wiring hides the affordance entirely for 不指定教材 — these functions
// never see those calls.
(function (global) {
  'use strict';

  /**
   * isSupplementTarget — the ONE eligibility rule (mirror of isCommittable's
   * single-home pattern): a row the supplement may fill.
   */
  function isSupplementTarget(row) {
    return !!row && row.status === 'new' && !row.meaning && row.checked !== false;
  }

  /**
   * supplementWords — the distinct words of the eligible rows, row order
   * (the request payload's word list; classifyPhotoEntries already keeps
   * words distinct, the dedup here is belt-and-braces for edited rows).
   */
  function supplementWords(rows) {
    var seen = {};
    var words = [];
    (rows || []).forEach(function (row) {
      if (!isSupplementTarget(row)) {
        return;
      }
      var word = String(row.word || '').trim();
      if (word && !seen[word]) {
        seen[word] = true;
        words.push(word);
      }
    });
    return words;
  }

  /**
   * applyGeneratedMeanings — fold one generate reply ({meanings, missing})
   * into the rows. Returns a NEW rows array (the applyLessonToPage kin):
   *   eligible row, word answered    → meaning filled, meaningSource
   *                                   'generated', genFailed cleared
   *   eligible row, word unanswered  → meaning stays '', meaningSource
   *                                   'material', genFailed set (the row
   *                                   stays submittable — 保留缺失)
   *   anything else                  → untouched (a parent-typed meaning
   *                                   is never overwritten: material
   *                                   original and manual text both outrank
   *                                   the model, AC-1).
   */
  function applyGeneratedMeanings(rows, reply) {
    var meanings = (reply && reply.meanings) || {};
    var missing = {};
    ((reply && reply.missing) || []).forEach(function (w) {
      missing[w] = true;
    });
    return (rows || []).map(function (row) {
      if (!isSupplementTarget(row)) {
        return row;
      }
      var word = String(row.word || '').trim();
      var next = {};
      for (var key in row) {
        if (Object.prototype.hasOwnProperty.call(row, key)) {
          next[key] = row[key];
        }
      }
      if (Object.prototype.hasOwnProperty.call(meanings, word)) {
        next.meaning = meanings[word];
        next.meaningSource = 'generated';
        next.genFailed = false;
      } else {
        next.genFailed = !!missing[word];
      }
      return next;
    });
  }

  /**
   * markGenerationFailed — the call-level failure counterpart of
   * reply.missing: words whose slice never got a reply (transport error,
   * unparseable) get the same honest per-row failure flag, so the
   * affordance switches to 重试生成 and the rows show 生成失败 while
   * staying submittable (ADR-0005 #5). Only still-eligible rows are
   * marked — a meaning typed during the call resolves the row. Returns a
   * NEW rows array.
   */
  function markGenerationFailed(rows, words) {
    var set = {};
    (words || []).forEach(function (w) {
      set[String(w || '').trim()] = true;
    });
    return (rows || []).map(function (row) {
      if (!isSupplementTarget(row) || !set[String(row.word || '').trim()]) {
        return row;
      }
      var next = {};
      for (var key in row) {
        if (Object.prototype.hasOwnProperty.call(row, key)) {
          next[key] = row[key];
        }
      }
      next.genFailed = true;
      return next;
    });
  }

  /**
   * cancelGeneratedMeaning — the parent's 取消 for one supplemented row
   * (AC-4): back to missing-meaning, source 'material', failure flag
   * cleared. Returns a NEW rows array.
   */
  function cancelGeneratedMeaning(rows, index) {
    return (rows || []).map(function (row, i) {
      if (i !== index || !row || row.meaningSource !== 'generated') {
        return row;
      }
      var next = {};
      for (var key in row) {
        if (Object.prototype.hasOwnProperty.call(row, key)) {
          next[key] = row[key];
        }
      }
      next.meaning = '';
      next.meaningSource = 'material';
      next.genFailed = false;
      return next;
    });
  }

  /**
   * meaningMarkerVisible — the ONE display rule for the 系统补充 marker
   * (ADR-0005 #3): meaning non-empty AND source 'generated'. An emptied
   * gloss never carries a marker (the server stores the same shape).
   */
  function meaningMarkerVisible(row) {
    return !!row && !!row.meaning && row.meaningSource === 'generated';
  }

  /**
   * genFailedVisible — the 生成失败 note's display rule: only a still-empty
   * meaning shows the failure (typing a manual meaning resolves the row).
   */
  function genFailedVisible(row) {
    return !!row && !row.meaning && !!row.genFailed;
  }

  /**
   * meaningGenSummary — the affordance's counts. target = how many words a
   * click would send (every still-missing eligible row — a failed row IS
   * one, retry re-sends exactly these); failed = how many of those carry a
   * previous unanswered attempt. failed > 0 switches the label to
   * 重试生成（target 词）, else 补充缺失释义（target 词）. The counts
   * overlap by design: failed rows are targets too.
   */
  function meaningGenSummary(rows) {
    var target = 0;
    var failed = 0;
    (rows || []).forEach(function (row) {
      if (isSupplementTarget(row)) {
        target++;
        if (genFailedVisible(row)) {
          failed++;
        }
      }
    });
    return { target: target, failed: failed };
  }

  // Window-or-module-global tail (state-manager.js convention).
  global.isSupplementTarget = isSupplementTarget;
  global.supplementWords = supplementWords;
  global.applyGeneratedMeanings = applyGeneratedMeanings;
  global.markGenerationFailed = markGenerationFailed;
  global.cancelGeneratedMeaning = cancelGeneratedMeaning;
  global.meaningMarkerVisible = meaningMarkerVisible;
  global.genFailedVisible = genFailedVisible;
  global.meaningGenSummary = meaningGenSummary;
})(typeof window !== 'undefined' ? window : this);
