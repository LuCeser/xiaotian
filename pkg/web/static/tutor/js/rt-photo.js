/**
 * @file RTPhoto — camera-capture photo for the check stage (13.1-03, D-05).
 *
 * Students photograph their whole notebook page during the check stage; the
 * JPEG is downscaled client-side, chunked into 32KiB photo_chunk JSON
 * control messages (base64, /rt single channel), and the server runs the
 * OCR check pass as soon as the final chunk lands — there is no separate
 * submit message. The recognition results come back as ocr_result + the
 * authoritative check_verdicts echo; the page renders them (index.html).
 *
 * This is the photo-controller.js (Ticket 04) port onto the /rt JSON
 * control channel: the constants, the quality ladder, and the file-input
 * capture path are VERBATIM; the transport differs — chunks ship as
 * photo_chunk JSON messages via the injected client.sendJSON instead of
 * the legacy /ws binary FrameCodec (the /rt audio binary channel stays
 * untouched, Pitfall 5), and cancel sends photo_cancel only when at least
 * one chunk shipped (the _shipped>0 rule).
 *
 * Capture uses <input type="file" accept="image/jpeg" capture="environment">,
 * NOT getUserMedia: file-capture is the iPad Safari standard camera path,
 * needs no permission prompt upfront, and the picker input is removed after
 * use (some Safari versions never reset .value otherwise).
 *
 * Downscale ladder (canvas → JPEG): 1600px@q0.8 → 1600px@q0.6 → 1280px@q0.6
 * → 1024px@q0.5, stopping at ≤768KiB (MAX_UPLOAD_BYTES, under the server's
 * 1MiB photo cap). Handwriting stays legible at 1600px on a notebook page.
 *
 * Exposes: window.RTPhoto, plus the pure helpers as constructor statics for
 * the node unit tests (sliceChunks / bytesToBase64 / pickLadderStep and the
 * four ported constants).
 */
(function (global) {
  'use strict';

  /** Longest image edge after downscale (px). */
  var MAX_EDGE = 1600;
  /** Raw JPEG bytes per photo_chunk control message (32KiB — base64 stays
   * far under the /rt 65551-byte read limit). */
  var CHUNK_BYTES = 32768;
  /** Client-side upload cap — under the server's 1MiB photo cap. */
  var MAX_UPLOAD_BYTES = 786432;
  /** JPEG re-encode ladder: first fit ≤ MAX_UPLOAD_BYTES wins. */
  var QUALITY_LADDER = [
    { quality: 0.8, edge: 1600 },
    { quality: 0.6, edge: 1600 },
    { quality: 0.6, edge: 1280 },
    { quality: 0.5, edge: 1024 }
  ];

  /**
   * Pure: slice raw JPEG bytes into CHUNK_BYTES chunks, keeping the final
   * partial chunk. An exact multiple yields no empty tail chunk.
   * @param {Uint8Array} jpeg
   * @returns {Uint8Array[]}
   */
  function sliceChunks(jpeg) {
    var chunks = [];
    for (var off = 0; off < jpeg.length; off += CHUNK_BYTES) {
      var end = Math.min(off + CHUNK_BYTES, jpeg.length);
      chunks.push(jpeg.subarray ? jpeg.subarray(off, end) : jpeg.slice(off, end));
    }
    return chunks;
  }

  /**
   * Pure: bytes → standard base64 (btoa exists in browsers and Node ≥16).
   * @param {Uint8Array} bytes
   * @returns {string}
   */
  function bytesToBase64(bytes) {
    var bin = '';
    for (var i = 0; i < bytes.length; i++) {
      bin += String.fromCharCode(bytes[i]);
    }
    return btoa(bin);
  }

  /**
   * Pure: pick the first ladder step whose encoded size fits the upload
   * cap. sizes[i] is the encoded byte count of QUALITY_LADDER[i]; returns
   * {step, index} or null when every step overflows (caller reports
   * 'too-large').
   * @param {number[]} sizes
   */
  function pickLadderStep(sizes) {
    for (var i = 0; i < QUALITY_LADDER.length; i++) {
      if (typeof sizes[i] === 'number' && sizes[i] <= MAX_UPLOAD_BYTES) {
        return { step: QUALITY_LADDER[i], index: i };
      }
    }
    return null;
  }

  /**
   * RTPhoto captures one notebook-page photo during the check stage and
   * ships it as photo_chunk JSON control messages (final=true triggers the
   * server's OCR pass — no separate submit message).
   *
   * @class
   * @param {RTCClient} client - connected client; needs sendJSON (the /rt
   *        control channel).
   */
  class RTPhoto {
    constructor(client) {
      this.client = client;

      // File-capture needs no secure context and no upfront permission —
      // only canvas imaging, which every target browser has.
      this.supported = !!(global.document && global.Image &&
        typeof global.document.createElement('canvas').getContext === 'function');

      /** True from ship success until reset() (the button locks). */
      this.isProcessing = false;

      /** photo_chunk sequence, monotonic per upload (restarts at 1). */
      this._seq = 0;
      /** Chunks shipped for the current upload (cancel signal). */
      this._shipped = 0;

      /** @type {function|null} Called with an errorType string. */
      this._onError = null;
    }

    /**
     * Register the error callback (errorType: 'not-supported' |
     * 'not-an-image' | 'too-large' | 'read-failed' | 'encode-failed').
     * @param {function} fn
     */
    onError(fn) { this._onError = fn; }

    /**
     * Unlock the button: the page calls this when ocr_result (or a terminal
     * error) arrived — the upload cycle is over either way.
     */
    reset() { this.isProcessing = false; }

    /**
     * Open the system camera / photo picker. One tap: the picked file is
     * downscaled and shipped immediately — a bad photo simply comes back as
     * gated/failed OCR and the manual 对/错 taps take over (D-05: the human
     * confirmation pass is mandatory regardless).
     */
    pickPhoto() {
      if (!this.supported) {
        this._emitError('not-supported');
        return;
      }
      if (this.isProcessing) return;

      var self = this;
      var input = global.document.createElement('input');
      input.type = 'file';
      input.accept = 'image/jpeg';
      input.setAttribute('capture', 'environment'); // rear camera on iPad Safari
      input.style.display = 'none';
      input.addEventListener('change', function () {
        var file = input.files && input.files[0];
        // Remove the picker regardless of outcome — a fresh pick must get a
        // fresh input (some Safari versions never reset .value otherwise).
        if (input.parentNode) input.parentNode.removeChild(input);
        if (file) self._handleFile(file);
      });
      global.document.body.appendChild(input);
      input.click();
    }

    /**
     * Abort the in-flight photo (retake, session ended, page hidden). If
     * chunks were already shipped, tells the server to discard them —
     * otherwise the assembler would absorb the next upload's sequence
     * numbers (restart at 1) as duplicates and OCR the stale bytes.
     */
    cancel() {
      this.isProcessing = false;
      if (this._shipped > 0) {
        this.client.sendJSON({ type: 'photo_cancel' });
      }
      this._resetCounters();
    }

    /** Release everything (page unload). */
    cleanup() {
      this.cancel();
      this._onError = null;
    }

    // ------------------------------------------------------------------
    // @private capture path: file → downscale → chunked photo_chunk JSON
    // ------------------------------------------------------------------

    /**
     * Validate, downscale, and ship one picked file.
     * @private
     * @param {File} file
     */
    async _handleFile(file) {
      if (!file.type || String(file.type).indexOf('image/') !== 0) {
        this._emitError('not-an-image');
        return;
      }
      var jpeg;
      try {
        jpeg = await this._downscale(file);
      } catch (downscaleErr) {
        console.error('[photo] downscale failed:', downscaleErr);
        this._emitError('encode-failed');
        return;
      }
      if (!jpeg) {
        this._emitError('too-large');
        return;
      }
      this.shipJPEG(jpeg);
    }

    /**
     * Re-encode the picked image as a size-capped JPEG via the ladder.
     * Resolves with the JPEG bytes, or null when even the last step exceeds
     * MAX_UPLOAD_BYTES (caller reports 'too-large').
     * @private
     * @param {File} file
     * @returns {Promise<Uint8Array|null>}
     */
    async _downscale(file) {
      var dataURL = await this._readAsDataURL(file);
      var img = await this._loadImage(dataURL);

      var canvas = global.document.createElement('canvas');
      for (var i = 0; i < QUALITY_LADDER.length; i++) {
        var step = QUALITY_LADDER[i];
        var scale = Math.min(1, step.edge / Math.max(img.width, img.height));
        canvas.width = Math.max(1, Math.round(img.width * scale));
        canvas.height = Math.max(1, Math.round(img.height * scale));
        var ctx = canvas.getContext('2d');
        ctx.drawImage(img, 0, 0, canvas.width, canvas.height);
        var blob = await this._canvasToJpegBlob(canvas, step.quality);
        if (blob && blob.size <= MAX_UPLOAD_BYTES) {
          return new Uint8Array(await blob.arrayBuffer());
        }
      }
      return null;
    }

    /**
     * Ship the JPEG as CHUNK_BYTES photo_chunk JSON control messages
     * (sequence from 1, base64 payload, final=true on the last — the
     * server's OCR pass fires on it). DOM-free: the node tests drive this
     * directly with a fake client. Refuses an oversize image without
     * shipping.
     * @param {Uint8Array} jpeg
     * @returns {boolean} whether the upload shipped
     */
    shipJPEG(jpeg) {
      if (jpeg.length > MAX_UPLOAD_BYTES) {
        this._emitError('too-large');
        return false;
      }
      this._resetCounters();
      var chunks = sliceChunks(jpeg);
      for (var i = 0; i < chunks.length; i++) {
        this._seq++;
        this.client.sendJSON({
          type: 'photo_chunk',
          seq: this._seq,
          data: bytesToBase64(chunks[i]),
          final: i === chunks.length - 1
        });
        this._shipped++;
      }
      this.isProcessing = true;
      return true;
    }

    /**
     * @private
     */
    _resetCounters() {
      this._seq = 0;
      this._shipped = 0;
    }

    /**
     * @private
     * @param {File} file
     * @returns {Promise<string>} data URL
     */
    _readAsDataURL(file) {
      return new Promise(function (resolve, reject) {
        var reader = new global.FileReader();
        reader.onload = function () { resolve(reader.result); };
        reader.onerror = function () { reject(reader.error || new Error('read failed')); };
        reader.readAsDataURL(file);
      });
    }

    /**
     * @private
     * @param {string} dataURL
     * @returns {Promise<HTMLImageElement>} decoded image
     */
    _loadImage(dataURL) {
      return new Promise(function (resolve, reject) {
        var img = new global.Image();
        img.onload = function () { resolve(img); };
        img.onerror = function () { reject(new Error('image decode failed')); };
        img.src = dataURL;
      });
    }

    /**
     * @private
     * @param {HTMLCanvasElement} canvas
     * @param {number} quality - JPEG quality 0..1
     * @returns {Promise<Blob|null>}
     */
    _canvasToJpegBlob(canvas, quality) {
      return new Promise(function (resolve) {
        canvas.toBlob(function (blob) { resolve(blob); }, 'image/jpeg', quality);
      });
    }

    /**
     * @private
     */
    _emitError(errorType) {
      console.warn('[photo] ' + errorType);
      if (typeof this._onError === 'function') this._onError(errorType);
    }
  }

  // Pure helpers + the ported constants, exposed for the node unit tests
  // (tests/test-rt-photo.js) and for any caller that needs the ladder math.
  RTPhoto.MAX_EDGE = MAX_EDGE;
  RTPhoto.CHUNK_BYTES = CHUNK_BYTES;
  RTPhoto.MAX_UPLOAD_BYTES = MAX_UPLOAD_BYTES;
  RTPhoto.QUALITY_LADDER = QUALITY_LADDER;
  RTPhoto.sliceChunks = sliceChunks;
  RTPhoto.bytesToBase64 = bytesToBase64;
  RTPhoto.pickLadderStep = pickLadderStep;

  global.RTPhoto = RTPhoto;
})(typeof window !== 'undefined' ? window : this);
