package main

// Photo-answer OCR wiring (Phase 8.2, Ticket 04): routes streamed photo
// chunks into a voice.Buffers assembler registry, and serves the
// photo_submit control trigger end-to-end — assemble → JPEG validation →
// Mimo vision OCR → answer judging against the session's current word, with
// a photo_result frame back to the client on every path (judged,
// low-confidence with best guess, or failed) so the UI can always fall back
// to the manual input.

import (
	"context"
	"time"

	"github.com/xiaotian/home-tutor-agent/pkg/infra"
	"github.com/xiaotian/home-tutor-agent/pkg/mimo"
	"github.com/xiaotian/home-tutor-agent/pkg/ocr"
	"github.com/xiaotian/home-tutor-agent/pkg/voice"
	"github.com/xiaotian/home-tutor-agent/pkg/ws"
)

// photoBufferIdleTTL bounds how long an abandoned per-connection photo
// buffer survives its last chunk (same rationale as voiceBufferIdleTTL; the
// photos are larger, so reclamation matters more on the 1C1G VPS).
const photoBufferIdleTTL = 30 * time.Second

// photoService carries the photo-OCR pipeline dependencies for the ws
// handlers. A nil *photoService means photo submission is disabled (no
// MIMO_API_KEY): the image-frame handler becomes a no-op and photo_submit
// rejects with an error frame — the manual input remains the full fallback.
type photoService struct {
	buffers        *voice.Buffers
	pipeline       *ocr.Pipeline
	judge          answerJudger
	logger         infra.Logger
	assembleWait   time.Duration
	requestTimeout time.Duration
}

// newPhotoService composes the photo pipeline from the pkg/ocr seams. read
// is the Reader seam (mimoReader adapts the production client; tests inject
// a plain closure); judge is the answer-judging seam over the session
// Manager.
func newPhotoService(read ocr.Reader, judge answerJudger, logger infra.Logger, cfg infra.PhotoConfig) *photoService {
	return &photoService{
		// Second Buffers instance (independent of voice's): keyed by the same
		// connIDs but capped and swept for image sizes.
		buffers:        voice.NewBuffers(cfg.MaxImageBytes, photoBufferIdleTTL),
		pipeline:       ocr.NewPipeline(read, cfg.ConfidenceThreshold),
		judge:          judge,
		logger:         logger,
		assembleWait:   cfg.AssembleWait,
		requestTimeout: cfg.RequestTimeout,
	}
}

// mimoReader adapts the mimo ChatVision call onto the ocr.Reader seam,
// keeping the mimo dependency out of the service layer's test surface. NO
// MaxTokens cap: mimo-v2.5 runs in thinking mode, whose reasoning alone can
// exceed a few hundred tokens — a 256 cap (live-observed) truncated the
// model mid-thought with no final JSON at all. The wall-clock bound is the
// handler's requestTimeout; the reply itself is a tiny JSON object.
func mimoReader(mc mimo.Client, model string) ocr.Reader {
	return func(ctx context.Context, systemPrompt, userPrompt string, imageJPEG []byte) (string, error) {
		resp, err := mc.ChatVision(ctx, systemPrompt, userPrompt, imageJPEG, "image/jpeg",
			mimo.WithChatModel(model))
		if err != nil {
			return "", err
		}
		return resp.Text(), nil
	}
}

// handlePhotoChunk buffers one inbound JPEG chunk (MsgTypeImage). Nil
// receiver (photo disabled) drops the frame — the photo UI is hidden in that
// mode, so stray frames are silent.
func (ps *photoService) handlePhotoChunk(_ context.Context, client *ws.Client, header *ws.FrameHeader, payload []byte) error {
	if ps == nil {
		return nil
	}
	connID := clientConnID(client)
	if err := ps.buffers.Add(connID, header.Sequence, payload); err != nil {
		// Cap exceeded: the upload is spoiled. Drop the whole entry so a
		// follow-up photo_submit reports no photo rather than a truncated
		// one, and tell the client to stop.
		ps.buffers.Drop(connID)
		ps.logger.Warn("photo upload overflowed cap",
			infra.F("conn_id", connID),
			infra.ErrField(err),
		)
		sendErrorFrame(client, "photo too large")
	}
	return nil
}

// photoResultResponse is the control frame reply to a photo_submit trigger,
// sent on every path so the client UI can always show an outcome:
//
//   - judged:     OCR confident → Correct/Expected/Rating valid
//   - low conf:   LowConfidence=true, Text carries the best guess (prefill
//     the manual input)
//   - failed:     Error carries the reason; Fallback="manual" when manual
//     input can actually resolve it (OCR failed), empty when it cannot
//     (session guard rejected — resubmitting text would hit the same guard)
type photoResultResponse struct {
	Type          string  `json:"type"` // "photo_result"
	Text          string  `json:"text"`
	Confidence    float64 `json:"confidence"`
	LowConfidence bool    `json:"low_confidence"`
	Error         string  `json:"error,omitempty"`
	Fallback      string  `json:"fallback,omitempty"` // "manual" | "" (see above)

	// Judged distinguishes "judged incorrect" (Judged=true, Correct=false)
	// from "not judged at all" (low confidence / failure).
	Judged   bool   `json:"judged"`
	Correct  bool   `json:"correct"`
	Expected string `json:"expected"`
	Rating   string `json:"rating"` // "" when not judged
}

// ocrPhoto settles the connection's buffered image chunks and runs the OCR
// pipeline once; the caller owns the reply shape. The upload is consumed
// either way — never let one photo's chunks bleed into the next. Shared by
// the judging path (photo_submit, Ticket 04) and the OCR-only path
// (photo_ocr, Ticket 05).
func (ps *photoService) ocrPhoto(ctx context.Context, connID string) (res ocr.Result, jpegLen int, err error) {
	jpeg := settleAssembled(ps.buffers, connID, ps.assembleWait)

	// Bound the vision call so a wedged upstream cannot pin router workers.
	callCtx, cancel := context.WithTimeout(ctx, ps.requestTimeout)
	res, err = ps.pipeline.Process(callCtx, jpeg)
	cancel()

	ps.buffers.Drop(connID)
	return res, len(jpeg), err
}

// handlePhotoSubmit serves the photo_submit control trigger: wait for any
// straggler image chunks, run the OCR pipeline, judge the recognized text
// against the session's current word, reply. Nil receiver (photo disabled)
// rejects with an error frame.
func (ps *photoService) handlePhotoSubmit(ctx context.Context, client *ws.Client, _ []byte) error {
	if ps == nil {
		sendErrorFrame(client, "photo submission unavailable")
		return nil
	}
	connID := clientConnID(client)

	res, jpegLen, err := ps.ocrPhoto(ctx, connID)

	switch {
	case err == nil:
		j, reject := ps.judge(connID, res.Text)
		if reject != "" {
			ps.logger.Warn("photo answer not judged",
				infra.F("conn_id", connID),
				infra.F("reason", reject),
			)
			return sendControlFrame(client, ws.MsgTypeControl, photoResultResponse{
				Type: "photo_result", Text: res.Text, Confidence: res.Confidence,
				Error: reject,
			})
		}
		ps.logger.Info("photo answer judged",
			infra.F("conn_id", connID),
			infra.F("text", res.Text),
			infra.F("confidence", res.Confidence),
			infra.F("correct", j.Correct),
		)
		return sendControlFrame(client, ws.MsgTypeControl, photoResultResponse{
			Type: "photo_result", Text: res.Text, Confidence: res.Confidence,
			Judged: true, Correct: j.Correct, Expected: j.Expected, Rating: j.Rating,
		})

	case err == ocr.ErrLowConfidence:
		// Best guess still ships — the client prefills the manual input.
		return sendControlFrame(client, ws.MsgTypeControl, photoResultResponse{
			Type: "photo_result", Text: res.Text, Confidence: res.Confidence,
			LowConfidence: true,
		})

	default:
		ps.logger.Warn("photo OCR failed",
			infra.F("conn_id", connID),
			infra.F("jpeg_bytes", jpegLen),
			infra.ErrField(err),
		)
		return sendControlFrame(client, ws.MsgTypeControl, photoResultResponse{
			Type:     "photo_result",
			Error:    "识别失败，请手动输入",
			Fallback: "manual",
		})
	}
}

// handlePhotoOCR serves the photo_ocr control trigger (Ticket 05): run the
// OCR pipeline WITHOUT judging — the batch-submission form fills the word's
// input with the recognized text, so there is no session/current-word guard
// and no rating. Replies carry the same shape as photo_result but the type
// "photo_ocr_result" so the client routes them to the form, not the
// per-word feedback flow.
func (ps *photoService) handlePhotoOCR(ctx context.Context, client *ws.Client, _ []byte) error {
	if ps == nil {
		sendErrorFrame(client, "photo OCR unavailable")
		return nil
	}
	connID := clientConnID(client)

	res, jpegLen, err := ps.ocrPhoto(ctx, connID)

	switch {
	case err == nil || err == ocr.ErrLowConfidence:
		// Success and low confidence both ship the recognized text — the
		// form prefills either way; LowConfidence flags the guess so the
		// form asks the student to double-check it.
		return sendControlFrame(client, ws.MsgTypeControl, photoResultResponse{
			Type: "photo_ocr_result", Text: res.Text, Confidence: res.Confidence,
			LowConfidence: err == ocr.ErrLowConfidence,
		})

	default:
		ps.logger.Warn("photo OCR (form fill) failed",
			infra.F("conn_id", connID),
			infra.F("jpeg_bytes", jpegLen),
			infra.ErrField(err),
		)
		return sendControlFrame(client, ws.MsgTypeControl, photoResultResponse{
			Type:     "photo_ocr_result",
			Error:    "识别失败，请直接输入",
			Fallback: "manual",
		})
	}
}

// photoCancelOKResponse acks a photo_cancel so the client (and tests) can
// sequence a restart AFTER the discard — same cross-route ordering hazard
// and contract as voice_cancel_ok.
type photoCancelOKResponse struct {
	Type string `json:"type"` // "photo_cancel_ok"
}

// handlePhotoCancel discards the connection's in-flight upload (the client
// aborted: retake, session ended, page hidden). Without this the assembler
// keeps the canceled chunks and the NEXT upload's sequence numbers (which
// restart at 1) are absorbed as duplicates — Assemble would then sort the
// stale bytes ahead of the fresh ones and OCR the wrong photo. Nil receiver
// is a no-op: with photo disabled there is no buffer to discard.
func (ps *photoService) handlePhotoCancel(_ context.Context, client *ws.Client, _ []byte) error {
	if ps == nil {
		return nil
	}
	connID := clientConnID(client)
	ps.buffers.Drop(connID)
	ps.logger.Debug("photo upload canceled",
		infra.F("conn_id", connID),
	)
	return sendControlFrame(client, ws.MsgTypeControl, photoCancelOKResponse{Type: "photo_cancel_ok"})
}
