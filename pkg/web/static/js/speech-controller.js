/**
 * @file SpeechController — Web Speech API wrapper for dictation recognition.
 *
 * Captures the student's spoken answer after hearing a word, using
 * webkitSpeechRecognition (iPad Safari). Handles iOS quirks including
 * programmatic start() outside gesture stack and silent failures.
 *
 * Exposes: window.SpeechController
 */
(function (global) {
  'use strict';

  /** Default timeout for speech recognition (ms). */
  var RECOGNITION_TIMEOUT = 8000;

  /**
   * SpeechController wraps Web Speech API for single-word dictation recognition.
   *
   * @class
   */
  class SpeechController {
    constructor() {
      /** @type {SpeechRecognition|null} */
      this.recognition = null;
      /** Whether the browser supports Web Speech API. */
      this.supported = false;
      /** Whether recognition is currently active. */
      this.listening = false;
      /** Current word ID being recognized. */
      this.currentWordId = null;
      /** Timeout guard ID. */
      this._timeoutId = null;
      /** Language for recognition (set per session). */
      this.lang = 'en-US';
      /** @type {function|null} Result callback: (wordId, transcript) => void */
      this._onResult = null;
      /** @type {function|null} Error callback: (wordId, errorType) => void */
      this._onError = null;
    }

    /**
     * Initialize speech recognition. Call once at session start.
     * @param {string} [lang='en-US'] — recognition language ('en-US' or 'zh-CN').
     * @returns {boolean} true if initialized successfully.
     */
    init(lang) {
      if (this.recognition) {
        // Already initialized — just update language if needed.
        if (lang) this.recognition.lang = lang;
        this.lang = lang || this.lang;
        return true;
      }

      var Ctor = window.SpeechRecognition || window.webkitSpeechRecognition;
      if (!Ctor) {
        console.error('[speech] Web Speech API not supported');
        this.supported = false;
        return false;
      }

      this.recognition = new Ctor();
      this.recognition.lang = lang || this.lang;
      this.recognition.continuous = false;
      this.recognition.interimResults = false;
      this.recognition.maxAlternatives = 1;

      var self = this;

      this.recognition.onresult = function (event) {
        self._clearTimeout();
        var lastResult = event.results[event.results.length - 1];
        if (lastResult.isFinal) {
          var transcript = lastResult[0].transcript.trim();
          console.log('[speech] result: "' + transcript + '"');
          self.listening = false;
          if (typeof self._onResult === 'function') {
            self._onResult(self.currentWordId, transcript);
          }
          self.currentWordId = null;
        }
      };

      this.recognition.onerror = function (event) {
        self._clearTimeout();
        console.warn('[speech] error: ' + event.error);
        self.listening = false;
        if (typeof self._onError === 'function') {
          self._onError(self.currentWordId, event.error);
        }
        self.currentWordId = null;
      };

      this.recognition.onend = function () {
        self._clearTimeout();
        // If we were listening and got no result, treat as timeout/no-speech.
        if (self.listening) {
          console.warn('[speech] recognition ended without result');
          self.listening = false;
          if (typeof self._onError === 'function') {
            self._onError(self.currentWordId, 'no-speech');
          }
          self.currentWordId = null;
        }
      };

      this.supported = true;
      this.lang = lang || this.lang;
      console.log('[speech] initialized (lang=' + this.lang + ')');
      return true;
    }

    /**
     * Register result callback.
     * @param {function} fn — (wordId, transcript) => void
     */
    onResult(fn) { this._onResult = fn; }

    /**
     * Register error callback.
     * @param {function} fn — (wordId, errorType) => void
     */
    onError(fn) { this._onError = fn; }

    /**
     * Start listening for a word. Must be called after audio playback ends.
     * On iPad Safari, programmatic start() outside gesture stack may silently
     * fail — the 8s timeout guard catches this.
     *
     * @param {string} wordId — the word being dictated.
     */
    startListening(wordId) {
      if (!this.recognition) {
        console.error('[speech] not initialized');
        if (typeof this._onError === 'function') {
          this._onError(wordId, 'not-initialized');
        }
        return;
      }

      // Abort any in-progress recognition.
      if (this.listening) {
        try { this.recognition.abort(); } catch (e) { /* ignore */ }
      }

      this.currentWordId = wordId;
      this.listening = true;

      console.log('[speech] startListening for "' + wordId + '"');

      try {
        this.recognition.start();

        // Timeout guard for iPad Safari silent failures.
        var self = this;
        this._timeoutId = setTimeout(function () {
          if (self.listening && self.currentWordId === wordId) {
            console.warn('[speech] timeout — no result in ' + RECOGNITION_TIMEOUT + 'ms');
            try { self.recognition.stop(); } catch (e) { /* ignore */ }
            self.listening = false;
            if (typeof self._onError === 'function') {
              self._onError(wordId, 'timeout');
            }
            self.currentWordId = null;
          }
        }, RECOGNITION_TIMEOUT);
      } catch (e) {
        console.error('[speech] start() error: ' + e.message);
        this.listening = false;
        if (typeof this._onError === 'function') {
          this._onError(wordId, 'start-failed');
        }
        this.currentWordId = null;
      }
    }

    /**
     * Stop listening (e.g. on session cancel).
     */
    stopListening() {
      this._clearTimeout();
      if (this.recognition && this.listening) {
        try { this.recognition.stop(); } catch (e) { /* ignore */ }
      }
      this.listening = false;
      this.currentWordId = null;
    }

    /**
     * Clean up resources.
     */
    cleanup() {
      this.stopListening();
      this.recognition = null;
      this._onResult = null;
      this._onError = null;
      this.supported = false;
    }

    /** @private */
    _clearTimeout() {
      if (this._timeoutId) {
        clearTimeout(this._timeoutId);
        this._timeoutId = null;
      }
    }
  }

  // Language-aware normalization for answer comparison.
  SpeechController.normalizeWord = function (word, lang) {
    if (!word) return '';
    var w = word.toLowerCase().trim();
    if (lang === 'zh-CN' || lang === 'zh' || lang === 'chinese') {
      // Chinese: strip punctuation and whitespace, preserve CJK.
      return w.replace(/[\s，。！？、；：""''（）【】《》\.,!\?;:'"()\[\]{}<>]/g, '');
    }
    // English: strip non-alphanumeric.
    return w.replace(/[^a-z0-9]/g, '');
  };

  global.SpeechController = SpeechController;
})(typeof window !== 'undefined' ? window : this);
