/**
 * @file PhotoAnswerController — camera-capture answer submission (Ticket 04).
 *
 * Students photograph their handwritten answer; the JPEG is downscaled
 * client-side, chunked into IMAGE frames (MsgType 0x07), streamed to the
 * backend, and a photo_submit control trigger runs Mimo vision OCR +
 * answer judging (see cmd/service/photo.go). The reply is a photo_result
 * frame; low-confidence or failed OCR surfaces the manual-input fallback,
 * which submits through the existing submit_answer path.
 *
 * Capture uses <input type="file" accept="image/*" capture="environment">,
 * NOT getUserMedia: file-capture is the iPad Safari standard camera path,
 * needs no permission prompt upfront, and — unlike getUserMedia — works over
 * plain http (LAN testing), mirroring the secure-context note in
 * voice-command-controller.js.
 *
 * Downscale ladder (canvas → JPEG): 1600px@q0.8 → 1600px@q0.6 → 1280px@q0.6
 * → 1024px@q0.5, stopping at ≤768KiB (MAX_UPLOAD_BYTES, under the server's
 * 1MiB photo cap). Handwriting stays legible at 1600px on a notebook page;
 * a modern iPad camera far exceeds it, so the first step usually suffices.
 *
 * Chunking: raw JPEG bytes in 32KiB IMAGE frames (wire payload cap is the
 * uint16 65535; 32KiB leaves headroom), sequence numbers restart at 1 per
 * photo — photo_cancel tells the server to discard a partially-shipped photo
 * so the restart cannot be absorbed as duplicates (same hazard voice_cancel
 * closes for recordings).
 *
 * Exposes: window.PhotoAnswerController
 */
(function (global) {
  'use strict';

  /** Longest image edge after downscale (px). */
  var MAX_EDGE = 1600;
  /** JPEG bytes per IMAGE frame (wire uint16 payload cap is 65535). */
  var CHUNK_BYTES = 32768;
  /** Client-side upload cap — under the server's 1MiB photo.maxImageBytes. */
  var MAX_UPLOAD_BYTES = 786432;
  /** JPEG re-encode ladder: first fit ≤ MAX_UPLOAD_BYTES wins. */
  var QUALITY_LADDER = [
    { quality: 0.8, edge: 1600 },
    { quality: 0.6, edge: 1600 },
    { quality: 0.6, edge: 1280 },
    { quality: 0.5, edge: 1024 }
  ];

  /**
   * PhotoAnswerController captures one photo answer and ships it to the
   * backend as IMAGE frames plus a photo_submit trigger.
   *
   * @class
   * @param {WebSocketClient} wsClient - connected client; needs send()
   *        (control frames) and sendFrame() (binary image frames).
   */
  class PhotoAnswerController {
    constructor(wsClient) {
      this.wsClient = wsClient;

      // File-capture needs no secure context and no upfront permission —
      // only canvas imaging, which every target browser has.
      this.supported = !!(global.document && global.Image &&
        typeof global.document.createElement('canvas').getContext === 'function');

      /** True from pick success until resultReceived() (button locked). */
      this.isProcessing = false;

      /** IMAGE-frame sequence, monotonic per photo (restarts at 1). */
      this._seq = 0;
      /** Frames shipped for the current photo (cancel signal). */
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
     * Called by app.js when a photo_result (or terminal error) arrives:
     * clears the processing lock so the button re-enables.
     */
    resultReceived() { this.isProcessing = false; }

    /**
     * Open the system camera / photo picker. One tap: the picked file is
     * downscaled and submitted immediately — a bad photo simply comes back
     * as low-confidence/failed OCR and the manual input takes over.
     *
     * @param {string} [trigger] - control type sent after the frames:
     *        "photo_submit" (default — judge against the current word) or
     *        "photo_ocr" (Ticket 05 — OCR only, the batch form fills the
     *        word's input with the recognized text).
     */
    pickPhoto(trigger) {
      if (!this.supported) {
        this._emitError('not-supported');
        return;
      }
      if (this.isProcessing) return;

      var self = this;
      var input = global.document.createElement('input');
      input.type = 'file';
      input.accept = 'image/jpeg,image/png,image/heic';
      input.setAttribute('capture', 'environment'); // rear camera on iPad Safari
      input.style.display = 'none';
      input.addEventListener('change', function () {
        var file = input.files && input.files[0];
        // Remove the picker regardless of outcome — a fresh pick must get a
        // fresh input (some Safari versions never reset .value otherwise).
        if (input.parentNode) input.parentNode.removeChild(input);
        if (file) self._handleFile(file, trigger);
      });
      global.document.body.appendChild(input);
      input.click();
    }

    /**
     * Abort the in-flight photo (session ended, page hidden). If chunks were
     * already streamed, tells the server to discard them — otherwise the
     * assembler would absorb the next photo's sequence numbers (restart at
     * 1) as duplicates and OCR the stale bytes.
     */
    cancel() {
      this.isProcessing = false;
      if (this._shipped > 0) {
        this.wsClient.send({ type: 'photo_cancel', params: {} });
      }
      this._resetCounters();
    }

    /** Release everything (page unload). */
    cleanup() {
      this.cancel();
      this._onError = null;
    }

    // ------------------------------------------------------------------
    // @private capture path: file → downscale → chunked IMAGE frames
    // ------------------------------------------------------------------

    /**
     * Validate, downscale, and ship one picked file.
     * @private
     * @param {File} file
     * @param {string} [trigger] - control type sent after the frames
     */
    async _handleFile(file, trigger) {
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
      this._ship(jpeg, trigger);
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
     * Ship the JPEG as CHUNK_BYTES IMAGE frames (sequence from 1) and send
     * the trigger control frame. Refuses an oversize image without shipping.
     * @private
     * @param {Uint8Array} jpeg
     * @param {string} [trigger] - control type ("photo_submit" default)
     */
    _ship(jpeg, trigger) {
      if (jpeg.length > MAX_UPLOAD_BYTES) {
        this._emitError('too-large');
        return;
      }
      this._resetCounters();
      var msgType = (global.FrameCodec && global.FrameCodec.MSG_TYPE.IMAGE) || 0x07;
      for (var offset = 0; offset < jpeg.length; offset += CHUNK_BYTES) {
        var end = Math.min(offset + CHUNK_BYTES, jpeg.length);
        this._seq++;
        this.wsClient.sendFrame(msgType, this._seq, jpeg.subarray(offset, end));
        this._shipped++;
      }
      this.isProcessing = true;
      this.wsClient.send({ type: trigger || 'photo_submit', params: {} });
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

  global.PhotoAnswerController = PhotoAnswerController;
})(typeof window !== 'undefined' ? window : this);
