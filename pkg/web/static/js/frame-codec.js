/**
 * @file FrameCodec — JavaScript port of the Phase-2 binary frame protocol
 *       (pkg/ws/protocol.go + pkg/ws/codec.go + pkg/crc16/crc16.go).
 *
 * Context (G-08-3): the backend WebSocket layer only accepts BinaryMessage
 * frames and decodes them with DecodeFrame (16-byte big-endian header +
 * CRC-16-CCITT). The student interface previously sent JSON as TEXT frames,
 * which were silently dropped at the server boundary
 * ("non-binary ws message ignored"). This codec lets the frontend speak the
 * same wire format in both directions.
 *
 * Wire format (big-endian, 16-byte header):
 *   byte  0     : Version    (0x01)
 *   byte  1     : MsgType    (1=audio, 2=control, 3=error, 4=heartbeat,
 *                             5=session, 6=status, 7=image)
 *   bytes 2-5   : Sequence   (uint32, monotonic per sender)
 *   bytes 6-9   : Timestamp  (uint32 microseconds, optional — encoded as 0)
 *   bytes 10-11 : PayloadLen (uint16, payload byte length)
 *   bytes 12-13 : Reserved   (uint16, 0)
 *   bytes 14-15 : CRC16      (uint16, CRC-16-CCITT over bytes 0-13)
 *   bytes 16..  : payload
 *
 * CRC-16-CCITT variant: poly 0x1021, init 0xFFFF, no reflection, no final XOR
 * — byte-for-byte identical to pkg/crc16/crc16.go.
 *
 * Loaded via a <script defer> tag BEFORE websocket-client.js so the client
 * can encode/decode frames. Exposes window.FrameCodec + window.SequenceCounter.
 * No module bundler, no build step (1C/1G VPS constraint).
 */
(function (global) {
  'use strict';

  var HEADER_SIZE = 16;
  var PROTOCOL_VERSION = 0x01;
  var AUDIO_FRAME_SIZE = 640;

  var MSG_TYPE = {
    AUDIO: 0x01,
    CONTROL: 0x02,
    ERROR: 0x03,
    HEARTBEAT: 0x04,
    SESSION: 0x05,
    STATUS: 0x06,
    IMAGE: 0x07 // photo-answer JPEG chunks (Ticket 04)
  };

  /**
   * CRC-16-CCITT (poly 0x1021, init 0xFFFF, no reflection, no final XOR).
   * Faithful port of pkg/crc16/crc16.go. Operates on any array-like of bytes
   * (Uint8Array view, full buffer, or subarray). Returns an unsigned 16-bit int.
   */
  function crc16ccitt(bytes) {
    var crc = 0xFFFF;
    var len = bytes.length;
    for (var i = 0; i < len; i++) {
      crc ^= (bytes[i] << 8);
      for (var j = 0; j < 8; j++) {
        if (crc & 0x8000) {
          crc = ((crc << 1) ^ 0x1021) & 0xFFFF;
        } else {
          crc = (crc << 1) & 0xFFFF;
        }
      }
    }
    return crc & 0xFFFF;
  }

  /**
   * Coerce an input (ArrayBuffer, Uint8Array, or array-like) into a Uint8Array
   * view without copying when possible. Centralizes the "what did the caller
   * hand us" handling for encode/decode.
   */
  function asBytes(input) {
    if (input instanceof Uint8Array) {
      return input;
    }
    if (input instanceof ArrayBuffer) {
      return new Uint8Array(input);
    }
    if (input && typeof input.byteLength === 'number' && input.buffer instanceof ArrayBuffer) {
      // Typed-array view (e.g. Int8Array); reuse its buffer view.
      return new Uint8Array(input.buffer, input.byteOffset, input.byteLength);
    }
    // Fallback: treat as array-like of numbers.
    return new Uint8Array(input || []);
  }

  var FrameCodec = {
    HEADER_SIZE: HEADER_SIZE,
    VERSION: PROTOCOL_VERSION,
    AUDIO_FRAME_SIZE: AUDIO_FRAME_SIZE,
    MSG_TYPE: MSG_TYPE,

    /**
     * Encode a complete frame (header + payload) as a new ArrayBuffer.
     *
     * @param {number} msgType  - one of MSG_TYPE.*
     * @param {number} sequence - uint32 sequence number (use SequenceCounter)
     * @param {Uint8Array|ArrayBuffer} payload - frame payload bytes
     * @returns {ArrayBuffer} framed binary message, ready for ws.send()
     */
    encode: function encode(msgType, sequence, payload) {
      var payloadBytes = asBytes(payload);
      var payloadLen = payloadBytes.length;
      if (payloadLen > 0xFFFF) {
        throw new Error('frame payload too large: ' + payloadLen + ' > 65535');
      }

      var buf = new ArrayBuffer(HEADER_SIZE + payloadLen);
      var view = new DataView(buf);
      var bytes = new Uint8Array(buf);

      bytes[0] = PROTOCOL_VERSION;
      bytes[1] = msgType & 0xFF;
      // Big-endian multi-byte fields (matches Go binary.BigEndian).
      view.setUint32(2, sequence >>> 0, false);
      view.setUint32(6, 0, false);          // timestamp: unused on the wire
      view.setUint16(10, payloadLen, false);
      view.setUint16(12, 0, false);         // reserved

      // CRC over header bytes 0-13 (all fields except the CRC itself).
      var crc = crc16ccitt(bytes.subarray(0, 14));
      view.setUint16(14, crc, false);

      // Payload follows the 16-byte header.
      bytes.set(payloadBytes, HEADER_SIZE);
      return buf;
    },

    /**
     * Decode a framed binary message. Validates CRC, protocol version, and
     * declared payload length — matching pkg/ws/codec.go DecodeFrame.
     *
     * @param {ArrayBuffer|Uint8Array} buf - the raw binary frame
     * @returns {object} {version, msgType, sequence, timestamp, payloadLen,
     *                    reserved, crc16, payload (Uint8Array view)}
     * @throws {Error} on short frame, CRC mismatch, unsupported version, or
     *                 payload-length mismatch.
     */
    decode: function decode(buf) {
      var input = asBytes(buf);
      if (input.length < HEADER_SIZE) {
        throw new Error('invalid frame: too short (' + input.length + ' < ' + HEADER_SIZE + ')');
      }
      var view = new DataView(input.buffer, input.byteOffset, input.byteLength);

      var expectedCRC = view.getUint16(14, false);
      var actualCRC = crc16ccitt(input.subarray(0, 14));
      if (expectedCRC !== actualCRC) {
        throw new Error('crc-16 checksum failed');
      }

      var version = input[0];
      if (version !== PROTOCOL_VERSION) {
        throw new Error('unsupported protocol version: 0x' + version.toString(16));
      }

      var msgType = input[1];
      var sequence = view.getUint32(2, false);
      var timestamp = view.getUint32(6, false);
      var payloadLen = view.getUint16(10, false);
      var reserved = view.getUint16(12, false);

      var payload = input.subarray(HEADER_SIZE);
      if (payloadLen !== payload.length) {
        throw new Error(
          'invalid frame: payload length mismatch (declared ' + payloadLen +
          ', actual ' + payload.length + ')'
        );
      }

      return {
        version: version,
        msgType: msgType,
        sequence: sequence,
        timestamp: timestamp,
        payloadLen: payloadLen,
        reserved: reserved,
        crc16: expectedCRC,
        payload: payload
      };
    }
  };

  /**
   * Monotonic uint32 sequence counter (mirrors backend D-12 semantics). First
   * call returns 1 (matches Go atomic.AddUint32 starting from 0). Wraps at the
   * 32-bit boundary with >>> 0.
   */
  function SequenceCounter() {
    this._seq = 0;
  }
  SequenceCounter.prototype.next = function next() {
    this._seq = (this._seq + 1) >>> 0;
    return this._seq;
  };
  SequenceCounter.prototype.reset = function reset() {
    this._seq = 0;
  };

  global.FrameCodec = FrameCodec;
  global.SequenceCounter = SequenceCounter;
})(typeof window !== 'undefined' ? window : this);
