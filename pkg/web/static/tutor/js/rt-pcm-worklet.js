/**
 * @file rt-pcm-worklet — AudioWorklet capture module for continuous mic
 *       collection on the /tutor/ page (Phase 13, Plan 05, RT-02).
 *
 * Loaded ONLY via `new AudioContext().audioWorklet.addModule('js/rt-pcm-worklet.js')`
 * from rt-capture.js — never as a plain <script> (registerProcessor scope
 * has no AudioWorkletProcessor outside the worklet realm). When
 * AudioWorklet is unavailable (older Safari), rt-capture falls back to a
 * ScriptProcessor and this file is not fetched at all.
 *
 * Runs on the audio render thread: accumulate input samples into 1024-sample
 * frames and postMessage one COPIED Float32Array per frame. All downsampling
 * and int16 conversion stay on the main thread (rt-capture.js) so the DSP
 * remains unit-testable on Node.
 */
class RtPcmProcessor extends AudioWorkletProcessor {
  constructor() {
    super();
    this._frame = new Float32Array(1024);
    this._filled = 0;
  }

  process(inputs) {
    var input = inputs[0];
    if (input && input.length > 0 && input[0]) {
      var channel = input[0];
      for (var i = 0; i < channel.length; i++) {
        this._frame[this._filled] = channel[i];
        this._filled += 1;
        if (this._filled === this._frame.length) {
          // Copy before posting: the transferable-less postMessage clones,
          // but slice() keeps the ring buffer ownership unambiguous.
          this.port.postMessage(this._frame.slice(0));
          this._filled = 0;
        }
      }
    }
    return true; // keep the processor alive for the whole session
  }
}

registerProcessor('rt-pcm', RtPcmProcessor);
