// textbook_progress.go implements the parent teaching-progress
// interpretation endpoint (Issue #82): POST /textbooks/progress/interpret.
//
// Contract:
//   - Request: {"message": "现在学到第三单元了"} — ONE parent sentence in
//     natural language, 1..maxProgressMessageRunes runes.
//   - Response 200: {"status": "proposed", "textbookId", "textbookName",
//     "lesson", "learnedLessons"} — a DRAFT understanding only: the model
//     picked the book+lesson from the registered catalogs, the server
//     verified the lesson verbatim against the authoritative list and
//     resolved the count by list position (已学完第 N 课含第 N 课).
//     "unclear" carries a reason (the ONE question to ask) plus optional
//     candidates when only the book is in doubt; "not_progress" explains
//     that the sentence is not a progress statement. NOTHING is written by
//     this route — the parent confirms the proposal, and persistence rides
//     the ordinary PUT /textbooks/{id} verified path, so understanding
//     progress can never start a classroom or touch cards.
//   - Errors use the locked {"error":{"code","message"}} envelope:
//     invalid_request (400, empty/oversize message),
//     interpretation_failed (502, the LLM call failed or its reply was
//     unparseable — retryable),
//     interpretation_unavailable (503, no LLM key configured — the same
//     degraded transparency as photo recognition and meaning supplement).
package api

import (
	"net/http"

	"github.com/xiaotian/home-tutor-agent/pkg/infra"
	"github.com/xiaotian/home-tutor-agent/pkg/ocr"
)

// Route-specific error codes (locked wire contract; see file comment).
const (
	codeInterpretationFailed      = "interpretation_failed"
	codeInterpretationUnavailable = "interpretation_unavailable"
)

// maxProgressMessageRunes caps one progress sentence: a parent states
// progress in a sentence, and a longer payload could only be a paste —
// the same api-habit bound the other prompt-carrying routes apply.
const maxProgressMessageRunes = 200

// ProgressInterpretRequest is the decoded POST body.
type ProgressInterpretRequest struct {
	Message string `json:"message"`
}

// progressCandidateJSON is one element of the unclear response's minimal
// confirmation choices.
type progressCandidateJSON struct {
	TextbookID     int64  `json:"textbookId"`
	TextbookName   string `json:"textbookName"`
	Lesson         string `json:"lesson"`
	LearnedLessons int    `json:"learnedLessons"`
}

// progressInterpretResponse is the 200 payload for every status; absent
// fields stay omitted (omitempty), never null.
type progressInterpretResponse struct {
	Status         string                  `json:"status"`
	TextbookID     int64                   `json:"textbookId,omitempty"`
	TextbookName   string                  `json:"textbookName,omitempty"`
	Lesson         string                  `json:"lesson,omitempty"`
	LearnedLessons int                     `json:"learnedLessons,omitempty"`
	Reason         string                  `json:"reason,omitempty"`
	Candidates     []progressCandidateJSON `json:"candidates,omitempty"`
}

// interpretTextbookProgress serves POST /textbooks/progress/interpret.
// h.interpreter is nil when the service booted without an LLM key — the
// 503 branch keeps the endpoint mounted and honest, and the parent page's
// per-book progress selects remain the always-available manual path.
func (h *handler) interpretTextbookProgress(w http.ResponseWriter, r *http.Request) {
	if h.interpreter == nil {
		writeError(w, http.StatusServiceUnavailable, codeInterpretationUnavailable,
			"progress interpretation is not configured on this server")
		return
	}
	var req ProgressInterpretRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	msg := []rune(req.Message)
	if len(msg) < 1 || len(msg) > maxProgressMessageRunes {
		writeError(w, http.StatusBadRequest, codeInvalidRequest,
			"message must contain between 1 and 200 characters")
		return
	}

	// The catalog is the REAL repository read (zero Python spawns, D-12):
	// the model only ever sees registered books and their authoritative
	// lesson lists.
	books, err := h.textbooks.List(r.Context(), "")
	if err != nil {
		h.logger.Error("progress interpret: textbook list failed",
			infra.F("component", "web/api"),
			infra.ErrField(err),
		)
		writeError(w, http.StatusInternalServerError, codeInternal, "internal error")
		return
	}
	if len(books) == 0 {
		// No catalog to interpret against — answer honestly without
		// spending an LLM round trip.
		writeJSON(w, progressInterpretResponse{
			Status: ocr.ProgressUnclear,
			Reason: "还没有登记教材。请先在下方新建教材并填写课次名单，再说明学到哪里。",
		})
		return
	}

	catalog := make([]ocr.ProgressTextbook, len(books))
	for i, b := range books {
		catalog[i] = ocr.ProgressTextbook{
			ID: b.ID, Name: b.Name, Category: b.Category,
			Edition: b.Edition, Stage: b.Stage, Grade: b.Grade,
			Volume: b.Volume, Module: b.Module,
			Lessons:        b.Lessons,
			LearnedLessons: b.LearnedLessons,
		}
	}
	res, err := h.interpreter.Interpret(r.Context(), req.Message, catalog)
	if err != nil {
		// Transport failure or an unparseable reply: the parent may retry
		// or use the manual selects (generation_failed's mapping).
		h.logger.Error("progress interpretation failed",
			infra.F("component", "web/api"),
			infra.ErrField(err),
		)
		writeError(w, http.StatusBadGateway, codeInterpretationFailed,
			"progress interpretation service failed, please retry")
		return
	}

	out := progressInterpretResponse{Status: res.Status}
	switch res.Status {
	case ocr.ProgressProposed:
		out.TextbookID = res.TextbookID
		out.TextbookName = res.TextbookName
		out.Lesson = res.Lesson
		out.LearnedLessons = res.LearnedLessons
	default:
		out.Reason = res.Reason
		out.Lesson = res.Lesson
		for _, c := range res.Candidates {
			out.Candidates = append(out.Candidates, progressCandidateJSON{
				TextbookID: c.TextbookID, TextbookName: c.TextbookName,
				Lesson: c.Lesson, LearnedLessons: c.LearnedLessons,
			})
		}
	}
	writeJSON(w, out)
}
