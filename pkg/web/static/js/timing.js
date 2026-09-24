/**
 * @file timing — shared pure timing helpers for the student dictation
 *       interface (P0-1 pacing fix).
 *
 * adaptiveWritingMs mirrors the backend's per-word writing timeout formula
 * (pkg/session/timing.go AdaptiveTimeout): 8000ms base + 1000ms per
 * character beyond 4 characters, capped at 15000ms. The batch-mode
 * auto-advance uses it so the next word is not dictated while the student
 * is still writing the current one.
 *
 * Pure and deterministic (no DOM, no timers, no I/O) so it is unit-testable
 * on stock Node (tests/test-timing.js) exactly like the Go original.
 *
 * NOTE: Go's AdaptiveTimeout measures len(word) in BYTES; this measures
 * UTF-16 code units. They agree for the English words this app dictates
 * (word IDs are the word text, e.g. "apple"); CJK would differ, and the
 * character-based reading is the documented intent.
 *
 * Loaded via a <script defer> tag BEFORE websocket-client.js in index.html;
 * exposes adaptiveWritingMs as a window global. No module bundler, no build
 * step. (Under Node the IIFE's `global` argument is module.exports, so
 * require() surfaces the function for tests — same pattern as
 * ui-components.js.)
 */
(function (global) {
  'use strict';

  // Mirrors pkg/session/timing.go AdaptiveTimeout constants.
  var BASE_MS = 8000; // words ≤ MIN_CHARS characters get the base window
  var PER_CHAR_MS = 1000; // each character beyond MIN_CHARS adds this much
  var MIN_CHARS = 4;
  var MAX_MS = 15000;

  /**
   * Adaptive writing gap for one dictated word, in milliseconds. Mirrors
   * AdaptiveTimeout in pkg/session/timing.go: 8s base, +1s per character
   * beyond 4, capped at 15s — longer/harder words get more writing time,
   * the traditional teacher-student dictation rhythm.
   *
   * @param {string} word - the word the student just heard and is writing
   *        (word IDs are the word text, e.g. "apple"). Non-string input
   *        falls back to length 0.
   * @returns {number} gap in milliseconds, between 8000 and 15000.
   */
  function adaptiveWritingMs(word) {
    var n = typeof word === 'string' ? word.length : 0;
    var extra = n > MIN_CHARS ? n - MIN_CHARS : 0;
    var ms = BASE_MS + extra * PER_CHAR_MS;
    if (ms > MAX_MS) {
      ms = MAX_MS;
    }
    return ms;
  }

  global.adaptiveWritingMs = adaptiveWritingMs;
})(typeof window !== 'undefined' ? window : this);
