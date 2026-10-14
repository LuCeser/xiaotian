// meaning_generate.go implements the parent missing-meaning supplement
// endpoint (Issue #8): POST /words/meaning/generate.
//
// Contract:
//   - Request: {"words": ["apple", ...], "textbook": {"edition",
//     "stage","grade","volume","module"}, "learnedLessons": 12|null} — the
//     words whose material carried NO meaning plus the CONFIRMED textbook
//     context that grounds the generation (ADR-0005 #2: the preview's
//     textbook panel values; learnedLessons null = 尚未登记学习进度, the
//     shape a book created in this very import has).
//   - Response 200: {"meanings": {"word": "n. 苹果"}, "missing":
//     ["word"]} — a DRAFT SUPPLEMENT ONLY. Nothing is written by this
//     route; the preview marks the rows (source=generated), the parent
//     edits/cancels/retries, and the final confirmed meaning reaches the
//     ordinary POST /words add path. Words with no usable answer land in
//     missing — an honest per-word failure, NOT an HTTP error: those rows
//     stay missing-meaning and remain submittable as-is.
//   - Errors use the locked {"error":{"code","message"}} envelope with
//     three route-specific codes: invalid_request (400, empty/oversize
//     word list — the shared gate), generation_failed (502, the LLM call
//     failed or its reply was unparseable — retryable),
//     generation_unavailable (503, no LLM key configured; the same
//     RT-10-style degraded transparency as photo recognition).
package api

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/xiaotian/home-tutor-agent/pkg/infra"
	"github.com/xiaotian/home-tutor-agent/pkg/ocr"
)

// Route-specific error codes (locked wire contract; see file comment).
const (
	codeGenerationFailed      = "generation_failed"
	codeGenerationUnavailable = "generation_unavailable"
)

// MeaningGenerateRequest is the decoded POST /words/meaning/generate body.
// Textbook reuses ocr.MeaningTextbook verbatim — one definition of the
// confirmed-context shape across the seam (the prompt builder consumes the
// same struct).
type MeaningGenerateRequest struct {
	Words    []string            `json:"words"`
	Textbook ocr.MeaningTextbook `json:"textbook"`
	// LearnedLessons is nil when no progress is registered (a new book).
	LearnedLessons *int `json:"learnedLessons"`
}

// normalizeMeaningWords trims, drops empties and de-duplicates (request
// order preserved) — the same normalization the generator applies, run at
// the gate so the caps judge the EFFECTIVE word list.
func normalizeMeaningWords(words []string) []string {
	seen := make(map[string]bool, len(words))
	out := make([]string, 0, len(words))
	for _, w := range words {
		v := strings.TrimSpace(w)
		if v == "" || seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	return out
}

// validateMeaningGenerateRequest enforces the supplement caps: an
// effective list of 1..maxEntries words (the add path's batch ceiling —
// a longer list could never be committed in one request anyway), each
// within maxWordRunes, and a non-negative learned-lessons gauge. Returns
// "" on success or the error message on failure.
func validateMeaningGenerateRequest(req *MeaningGenerateRequest) string {
	words := normalizeMeaningWords(req.Words)
	if len(words) < 1 {
		return "words must contain at least one non-empty word"
	}
	if len(words) > maxEntries {
		return fmt.Sprintf("words must contain between 1 and %d non-empty words", maxEntries)
	}
	for _, w := range words {
		if n := len([]rune(w)); n > maxWordRunes {
			return fmt.Sprintf("a word exceeds %d characters", maxWordRunes)
		}
	}
	if req.LearnedLessons != nil && *req.LearnedLessons < 0 {
		return "learnedLessons must be 0 or greater"
	}
	return ""
}

// generateWordMeanings serves POST /words/meaning/generate. h.generator is
// nil when the service booted without an LLM key — the 503 branch keeps
// the endpoint mounted and honest (the preview explains why supplement is
// off instead of guessing at a 404).
func (h *handler) generateWordMeanings(w http.ResponseWriter, r *http.Request) {
	if h.generator == nil {
		writeError(w, http.StatusServiceUnavailable, codeGenerationUnavailable,
			"meaning generation is not configured on this server")
		return
	}

	var req MeaningGenerateRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if msg := validateMeaningGenerateRequest(&req); msg != "" {
		writeError(w, http.StatusBadRequest, codeInvalidRequest, msg)
		return
	}

	res, err := h.generator.Generate(r.Context(), ocr.MeaningRequest{
		Words:          normalizeMeaningWords(req.Words),
		Textbook:       req.Textbook,
		LearnedLessons: req.LearnedLessons,
	})
	if err != nil {
		switch {
		case errors.Is(err, ocr.ErrEmptyWords), errors.Is(err, ocr.ErrTooManyEntries):
			writeError(w, http.StatusBadRequest, codeInvalidRequest,
				fmt.Sprintf("words must contain between 1 and %d non-empty words", maxEntries))
		default:
			// LLM transport failure or an unparseable reply: the parent
			// may retry the supplement.
			h.logger.Error("meaning generation failed",
				infra.F("component", "web/api"),
				infra.ErrField(err),
			)
			writeError(w, http.StatusBadGateway, codeGenerationFailed,
				"meaning generation service failed, please retry")
		}
		return
	}
	writeJSON(w, res)
}
