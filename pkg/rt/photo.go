// photo.go — the photo-check upload channel (13.1-03, D-05/Pitfall 5): the
// check stage's notebook photo rides the CONTROL channel as base64 JSON
// chunks so the audio BINARY channel stays untouched. The assembler is a
// per-session buffer owned by the TurnManager under the single mutex; a
// completed upload (final=true) triggers the OCR check pass immediately —
// there is no separate submit message.
//
// The timing-event constants below live HERE beside their only users
// (timing.go is not touched by 13.1-03), following check.go's naming style.
// All fields stay count/index-class (T-13-06b desensitization boundary).

package rt

import (
	"encoding/base64"

	"github.com/pkg/errors"

	"github.com/xiaotian/home-tutor-agent/pkg/infra"
)

// Photo-check timing-event vocabulary (13.1-03). Frozen JSONL identifiers.
const (
	// EventOcrCompleted: one photo's OCR finished; recognized/total counts
	// only — never word text and never image bytes (T-13-06b, privacy P2).
	EventOcrCompleted = "ocr_completed"
	// EventPhotoCanceled: an in-flight upload was discarded (retake/abort).
	// No payload beyond the discarded flag.
	EventPhotoCanceled = "photo_canceled"
)

// Photo-channel defaults and bounds (13.1-03). They mirror PhotoConfig's
// documented defaults so the /rt path needs no new config sections; main.go
// injects the real values from the existing photo block (Task 3). The OCR
// call's request timeout is NOT frozen here — it flows in through
// TurnManagerConfig.PhotoRequestTimeout (Issue #44), whose fallback defers
// to the photo config block's own default.
const (
	// defaultPhotoMaxBytes mirrors PhotoConfig.MaxImageBytes' 1MiB default:
	// the assembled JPEG memory guard on the 1C1G VPS (T-13.1-06).
	defaultPhotoMaxBytes = 1 << 20
	// defaultPhotoConfidence mirrors PhotoConfig.ConfidenceThreshold's 0.6
	// default — the OCR prefill confidence floor.
	defaultPhotoConfidence = 0.6
)

// Fixed human wordings for the photo channel's visible errors (short,
// child-safe, honest; the codes carry the machine meaning).
const (
	// photoUnavailableNotice: no OCR reader configured server-side — the
	// check stage keeps working manual-only (RT-10 degraded semantics).
	photoUnavailableNotice = "拍照检查现在用不了，我们看着屏幕逐个确认吧"
	// photoStageClosedNotice: photo frames arriving outside the check stage.
	photoStageClosedNotice = "现在不是拍照检查的时间哦"
	// photoOcrFailedNotice: the vision call failed — manual check continues.
	photoOcrFailedNotice = "这张照片没认出来，我们看着屏幕逐个确认吧"
	// photoChunkInvalidNotice: bad base64, a broken sequence, or an
	// over-cap upload (the buffer was cleared — the student retakes).
	photoChunkInvalidNotice = "这张照片没有传完整，再拍一次吧"
)

// photoBuffer holds ONE in-flight upload's decoded chunks (13.1-03).
//
// PRIVACY (prohibition P2 / T-13.1-08): the child's handwriting photo lives
// ONLY in this in-memory map and flows through exactly one OCR call — never
// written to disk, never logged, never stored in the lessons row. Session
// close, photo_cancel, an over-cap upload, and each new upload's first chunk
// all discard it.
type photoBuffer struct {
	chunks map[int][]byte // seq → decoded chunk bytes
	total  int            // running decoded byte total
}

func newPhotoBuffer() *photoBuffer {
	return &photoBuffer{chunks: map[int][]byte{}}
}

// reset discards the whole buffer (cancel/close/over-cap/new-upload).
func (b *photoBuffer) reset() {
	b.chunks = map[int][]byte{}
	b.total = 0
}

// nextSeq is the sequence number the buffer expects next (1-based,
// contiguous per upload).
func (b *photoBuffer) nextSeq() int { return len(b.chunks) + 1 }

// add absorbs one decoded chunk under the assembler's rules:
//   - seq 1 RESETS the buffer (each upload restarts at 1 — the retake
//     contract; this is also why cancel-clear matters, Pitfall 5);
//   - any other seq must be exactly contiguous, else it is rejected and the
//     buffer stays untouched (out-of-order/duplicate frames never absorb);
//   - the running byte total is capped: an over-cap upload is REJECTED AND
//     CLEARED (T-13.1-06 — a spoiled upload never lingers).
//
// ok reports whether the chunk was absorbed.
func (b *photoBuffer) add(seq int, chunk []byte, capBytes int) bool {
	if seq == 1 {
		b.reset()
	} else if seq != b.nextSeq() {
		return false
	}
	if b.total+len(chunk) > capBytes {
		b.reset()
		return false
	}
	b.chunks[seq] = chunk
	b.total += len(chunk)
	return true
}

// assemble concatenates the chunks in sequence order. The caller consumes
// the buffer right after (one upload never bleeds into the next).
func (b *photoBuffer) assemble() []byte {
	out := make([]byte, 0, b.total)
	for i := 1; i <= len(b.chunks); i++ {
		out = append(out, b.chunks[i]...)
	}
	return out
}

// handlePhotoChunk absorbs one photo_chunk control frame (13.1-03). Valid
// only inside a live check stage; anything else answers a sanitized error
// and leaves the buffer untouched. A final chunk completes the upload: the
// buffer is consumed and the OCR check pass runs on its own goroutine (the
// vision call takes seconds; the read loop must keep serving).
func (tm *TurnManager) handlePhotoChunk(s Sender, m PhotoChunkMsg) {
	tm.mu.Lock()
	alive := tm.tutor != nil && !tm.closed && tm.tutor.checking
	pipeline := tm.ocrPipeline
	capBytes := tm.cfg.PhotoMaxBytes
	if !alive {
		tm.mu.Unlock()
		tm.recorder.Emit(EventError,
			infra.F("code", ErrCodeInvalidMessage),
			infra.F("photo", "outside_check_stage"),
		)
		tm.sendOpenError(s, ErrCodeInvalidMessage, photoStageClosedNotice, true)
		return
	}
	if pipeline == nil {
		// Honest degradation: no OCR reader configured server-side. A coded,
		// child-friendly error answers the first chunk; the check stage
		// keeps working manual-only (nothing is buffered — there is no path
		// it could ever take).
		tm.mu.Unlock()
		tm.sendOpenError(s, ErrCodePhotoUnavailable, photoUnavailableNotice, true)
		return
	}
	if m.Seq < 1 {
		tm.mu.Unlock()
		tm.recorder.Emit(EventError,
			infra.F("code", ErrCodeInvalidMessage),
			infra.F("photo", "bad_seq"),
		)
		tm.sendOpenError(s, ErrCodeInvalidMessage, photoChunkInvalidNotice, true)
		return
	}
	if tm.photo == nil {
		tm.photo = newPhotoBuffer()
	}
	chunk, err := base64.StdEncoding.DecodeString(m.Data)
	if err == nil && !tm.photo.add(m.Seq, chunk, capBytes) {
		err = errPhotoChunkRejected
	}
	if err != nil {
		// Bad base64, a broken sequence, or an over-cap upload (add() has
		// already cleared the buffer in the over-cap case).
		tm.mu.Unlock()
		tm.recorder.Emit(EventError,
			infra.F("code", ErrCodeInvalidMessage),
			infra.F("photo", "bad_chunk"),
		)
		tm.sendOpenError(s, ErrCodeInvalidMessage, photoChunkInvalidNotice, true)
		return
	}
	final := m.Final
	var jpeg []byte
	if final {
		jpeg = tm.photo.assemble()
		tm.photo = nil // consumed: one upload never bleeds into the next
	}
	tm.mu.Unlock()

	if final {
		go tm.runCheckPhoto(s, jpeg)
	}
}

// errPhotoChunkRejected marks a structurally valid frame the assembler's
// discipline refused (sequence/cap); it stays unexported — the wire only
// ever sees the sanitized notice.
var errPhotoChunkRejected = errors.New("rt: photo chunk rejected")

// handlePhotoCancel discards the in-flight upload unconditionally (13.1-03
// retake discipline — the cmd/service photo_cancel incident shape: without
// the clear, the next upload's sequence numbers restart at 1 and would be
// absorbed as duplicates, OCR-ing stale bytes).
func (tm *TurnManager) handlePhotoCancel(s Sender, m PhotoCancelMsg) {
	tm.mu.Lock()
	had := tm.photo != nil && tm.photo.total > 0
	tm.photo = nil
	tm.mu.Unlock()
	tm.recorder.Emit(EventPhotoCanceled, infra.F("discarded", had))
}
