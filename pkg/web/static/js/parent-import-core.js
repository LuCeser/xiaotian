/**
 * Issue #80 path B — per-photo recognition identity (U3).
 * Pure store: each page/photo slot holds its own header meta and OCR
 * entries (including original lessons). Retries REPLACE the slot; they
 * never append a second snapshot for the same photo index.
 */
(function (global) {
  'use strict';

  function normalizePhotoPageNo(pageNo) {
    var n = pageNo == null ? 0 : Number(pageNo);
    return n > 0 ? n : 1;
  }

  function copyMeta(meta) {
    if (!meta || typeof meta !== 'object') {
      return null;
    }
    var out = {};
    Object.keys(meta).forEach(function (key) {
      var v = meta[key];
      out[key] = v == null ? '' : String(v);
    });
    return out;
  }

  function copyEntry(entry) {
    if (!entry) {
      return { word: '', meaning: '', lesson: '' };
    }
    return {
      word: entry.word == null ? '' : String(entry.word),
      meaning: entry.meaning == null ? '' : String(entry.meaning),
      lesson: entry.lesson == null ? '' : String(entry.lesson),
    };
  }

  function createImportPhotoState() {
    return { photos: {} };
  }

  function clearImportPhotoState(state) {
    if (!state) {
      return;
    }
    state.photos = {};
  }

  /**
   * setPhotoRecognition — register one landing from a real recognize entry
   * (single / multi / retry). Parsed shape matches parseRecognized.
   */
  function setPhotoRecognition(state, pageNo, parsed) {
    if (!state) {
      return;
    }
    var key = String(normalizePhotoPageNo(pageNo));
    var entries = (parsed && parsed.entries ? parsed.entries : []).map(copyEntry);
    state.photos[key] = {
      meta: parsed && parsed.meta ? copyMeta(parsed.meta) : null,
      entries: entries,
    };
  }

  function getPhotoRecognition(state, pageNo) {
    if (!state) {
      return null;
    }
    return state.photos[String(normalizePhotoPageNo(pageNo))] || null;
  }

  /** Ordered meta slots for mergePhotoMetas (index 0 → page 1). */
  function photoMetaListFromState(state) {
    if (!state || !state.photos) {
      return [];
    }
    var nums = Object.keys(state.photos).map(Number).filter(function (n) {
      return n > 0;
    }).sort(function (a, b) { return a - b; });
    if (!nums.length) {
      return [];
    }
    var max = nums[nums.length - 1];
    var list = [];
    for (var p = 1; p <= max; p++) {
      var rec = state.photos[String(p)];
      list.push(rec && rec.meta ? copyMeta(rec.meta) : null);
    }
    return list;
  }

  function recognitionEntriesForPage(state, pageNo) {
    var rec = getPhotoRecognition(state, pageNo);
    if (!rec || !rec.entries) {
      return [];
    }
    return rec.entries.map(copyEntry);
  }

  function recognitionMetaForPage(state, pageNo) {
    var rec = getPhotoRecognition(state, pageNo);
    return rec && rec.meta ? copyMeta(rec.meta) : null;
  }

  /** Original OCR row for a preview row (survives reclassify edits). */
  function originalEntryForRow(state, row) {
    if (!row) {
      return null;
    }
    var rec = getPhotoRecognition(state, row.page);
    if (!rec || !rec.entries) {
      return null;
    }
    var word = row.word == null ? '' : String(row.word);
    for (var i = 0; i < rec.entries.length; i++) {
      if (rec.entries[i].word === word) {
        return copyEntry(rec.entries[i]);
      }
    }
    return null;
  }

  global.createImportPhotoState = createImportPhotoState;
  global.clearImportPhotoState = clearImportPhotoState;
  global.setPhotoRecognition = setPhotoRecognition;
  global.getPhotoRecognition = getPhotoRecognition;
  global.photoMetaListFromState = photoMetaListFromState;
  global.recognitionEntriesForPage = recognitionEntriesForPage;
  global.recognitionMetaForPage = recognitionMetaForPage;
  global.originalEntryForRow = originalEntryForRow;
  global.normalizePhotoPageNo = normalizePhotoPageNo;
})(typeof window !== 'undefined' ? window : this);
