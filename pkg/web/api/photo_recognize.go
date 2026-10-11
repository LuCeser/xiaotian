// photo_recognize.go implements the parent word-list photo draft endpoint
// (Issue #3): POST /words/photo/recognize.
//
// Contract:
//   - Request: the raw PNG or JPEG bytes as the body (the declared
//     Content-Type is ignored; the pipeline sniffs the magic bytes and is
//     authoritative). One image per request — the single-photo limit Issue #3
//     settles for this flow (multi-page PDF is a later task).
//   - Response 200: {"entries":[{"word","meaning","confidence"}]} — a
//     RECOGNITION DRAFT ONLY. Nothing is written to any database by this
//     route; the parent reviews/edits the draft in the preview and commits
//     through the ordinary POST /words path (Python stays the sole writer).
//     An empty entries array is a legal outcome (no word list found in the
//     photo) — the UI says so, it is not an error.
//   - Errors use the locked {"error":{"code","message"}} envelope with three
//     route-specific codes: image_too_large (413, the photo config's byte
//     cap), recognition_failed (502, the vision call failed or its reply was
//     unparseable — retryable), recognition_unavailable (503, no vision key
//     configured; RT-10-style degraded transparency: the endpoint explains
//     itself instead of disappearing).
package api

import (
	"errors"
	"io"
	"net/http"

	"github.com/xiaotian/home-tutor-agent/pkg/infra"
	"github.com/xiaotian/home-tutor-agent/pkg/ocr"
)

// Route-specific error codes (locked wire contract; see file comment).
const (
	codeImageTooLarge          = "image_too_large"
	codeRecognitionFailed      = "recognition_failed"
	codeRecognitionUnavailable = "recognition_unavailable"
)

// recognizeWordsPhoto serves POST /words/photo/recognize. h.recognizer is
// nil when the service booted without a vision key — the 503 branch keeps
// the endpoint mounted and honest (the parent UI shows why photo import is
// off instead of guessing at a 404).
func (h *handler) recognizeWordsPhoto(w http.ResponseWriter, r *http.Request) {
	if h.recognizer == nil {
		writeError(w, http.StatusServiceUnavailable, codeRecognitionUnavailable,
			"photo recognition is not configured on this server")
		return
	}

	// The byte cap comes from the pipeline itself (built from the photo
	// config's MaxImageBytes in cmd/service) so the HTTP layer and the
	// pipeline can never disagree about the limit. MaxBytesReader bounds the
	// buffered body before any vision cost (1C1G VPS memory guard).
	body := http.MaxBytesReader(w, r.Body, int64(h.recognizer.MaxBytes()))
	image, err := io.ReadAll(body)
	if err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			writeError(w, http.StatusRequestEntityTooLarge, codeImageTooLarge,
				"image exceeds the size limit")
			return
		}
		writeError(w, http.StatusBadRequest, codeInvalidRequest, "failed to read image body")
		return
	}

	entries, err := h.recognizer.Recognize(r.Context(), image)
	if err != nil {
		switch {
		case errors.Is(err, ocr.ErrEmptyImage), errors.Is(err, ocr.ErrNotImage):
			writeError(w, http.StatusBadRequest, codeInvalidRequest,
				"image must be a PNG or JPEG photo of a word list")
		case errors.Is(err, ocr.ErrImageTooLarge):
			writeError(w, http.StatusRequestEntityTooLarge, codeImageTooLarge,
				"image exceeds the size limit")
		case errors.Is(err, ocr.ErrTooManyEntries):
			writeError(w, http.StatusBadRequest, codeInvalidRequest,
				"too many word-list entries in one photo")
		default:
			// Vision transport failure or an unparseable model reply: the
			// parent may retry the same photo.
			h.logger.Error("word photo recognition failed",
				infra.F("component", "web/api"),
				infra.ErrField(err),
			)
			writeError(w, http.StatusBadGateway, codeRecognitionFailed,
				"recognition service failed, please retry")
		}
		return
	}

	// entries is never nil-typed here on the happy path, but the wire shape
	// must be an array even when the pipeline returns an empty slice.
	if entries == nil {
		entries = []ocr.WordlistEntry{}
	}
	writeJSON(w, wordlistResponse{Entries: entries})
}

// wordlistResponse is the POST /words/photo/recognize 200 body.
type wordlistResponse struct {
	Entries []ocr.WordlistEntry `json:"entries"`
}
