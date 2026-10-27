// arrangement.go implements the Issue #80 指定默写 creation route:
//
//	POST /designations — select + lock one textbook lesson's eligible
//	                     words (the Python designate command) and record
//	                     the snapshot as a pending designations row.
//
// 只保留创建指定: no progress proposal rides this route — the route never
// writes learned_lessons, include_in_scope or participates anywhere. The
// Python side claims the winners with fetch_locked_until; this handler
// stores exactly what came back so the classroom consumption step (pending
// → consumed after the lessons row lands) replays the same list, never a
// re-query. Occupied / too-many / empty targets are typed OUTCOMES of a
// healthy subprocess, answered with their own error codes — never
// python_failed, and never a stored row.
package api

import (
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/xiaotian/home-tutor-agent/pkg/database"
	"github.com/xiaotian/home-tutor-agent/pkg/infra"
	"github.com/xiaotian/home-tutor-agent/pkg/lesson"
	"github.com/xiaotian/home-tutor-agent/pkg/pybridge"
)

// Error codes for the designation route (locked strings, same
// {"error":{"code","message"}} envelope as the other routes).
const (
	codeDesignationOccupied = "designation_occupied"
	codeDesignationTooMany  = "designation_too_many"
	codeDesignationEmpty    = "designation_empty"
)

// DesignationCreateRequest is the POST /designations body: the textbook id
// and one lesson name from its list (待确认/课表外 cannot be designated).
type DesignationCreateRequest struct {
	Lesson     string `json:"lesson"`
	TextbookID int64  `json:"textbookId"`
}

// validateDesignationCreateRequest enforces the shape before any read or
// subprocess: positive textbook id and a non-empty, bounded lesson name.
func validateDesignationCreateRequest(req *DesignationCreateRequest) string {
	if req.TextbookID <= 0 {
		return "textbookId must be a positive integer"
	}
	if strings.TrimSpace(req.Lesson) == "" {
		return "lesson is required (待确认课次不能指定)"
	}
	if len(req.Lesson) > 100 {
		return "lesson must be at most 100 characters"
	}
	return ""
}

// createDesignation serves POST /designations. Flow: decode → validate
// shape → load the textbook (404) → validate the lesson against its list
// (400, the assign-route discipline — both without a subprocess) → run
// the Python designate (select + lock, one BEGIN IMMEDIATE transaction)
// → store the snapshot as a pending designation on the ok branch.
func (h *handler) createDesignation(w http.ResponseWriter, r *http.Request) {
	var req DesignationCreateRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if msg := validateDesignationCreateRequest(&req); msg != "" {
		writeError(w, http.StatusBadRequest, codeInvalidRequest, msg)
		return
	}
	book, err := h.textbooks.Get(r.Context(), req.TextbookID)
	if err != nil {
		if errors.Is(err, database.ErrTextbookNotFound) {
			writeError(w, http.StatusNotFound, codeNotFound, "textbook not found")
			return
		}
		h.mapBridgeError(w, "read textbook", err)
		return
	}
	if !slices.Contains(book.Lessons, req.Lesson) {
		writeError(w, http.StatusBadRequest, codeInvalidRequest,
			fmt.Sprintf("lesson %q is not in textbook %q's lesson list", req.Lesson, book.Name))
		return
	}

	result, err := h.exec.RunDesignate(r.Context(), pybridge.DesignateOpts{
		TextbookID: req.TextbookID, Lesson: req.Lesson,
	})
	if err != nil {
		h.mapBridgeError(w, "designate words", err)
		return
	}
	switch result.Status {
	case pybridge.DesignateStatusOK:
		if len(result.Cards) == 0 {
			// 课表内但无可练词: nothing was locked; explain instead of
			// storing an empty designation (AC-15 指定目标里没有可练词
			// 时说明原因).
			writeError(w, http.StatusConflict, codeDesignationEmpty,
				"这个课次没有可练的词：可能没有词归属到该课次，或都已移出课堂")
			return
		}
		cards := make([]lesson.DesignationCard, 0, len(result.Cards))
		for _, c := range result.Cards {
			cards = append(cards, lesson.DesignationCard{
				ID: c.ID, Word: c.Word, Meaning: c.Meaning, Example: c.Example,
			})
		}
		created, storeErr := h.lessons.CreateDesignation(r.Context(), lesson.DesignationInput{
			TextbookID: req.TextbookID, Lesson: req.Lesson,
			Category: book.Category, Cards: cards,
		})
		if storeErr != nil {
			h.mapBridgeError(w, "store designation", storeErr)
			return
		}
		writeJSON(w, designationResponse{
			ID: created.ID, TextbookID: created.TextbookID, Lesson: created.Lesson,
			Category: created.Category, Status: created.Status, WordCount: len(created.Cards),
		})
	case pybridge.DesignateStatusOccupied:
		writeError(w, http.StatusConflict, codeDesignationOccupied,
			"目标词正在其他练习中占用，请稍后再试")
	case pybridge.DesignateStatusTooMany:
		writeError(w, http.StatusUnprocessableEntity, codeDesignationTooMany,
			fmt.Sprintf("指定课次共有 %d 个可练词，超过单次 %d 个上限",
				result.Total, pybridge.MaxDesignateLimit))
	default:
		h.logger.Error("designate words: unknown status",
			infra.F("component", "web/api"),
			infra.F("status", result.Status),
		)
		writeError(w, http.StatusBadGateway, codePythonFailed, "operation failed, please retry")
	}
}

// designationResponse is the POST /designations success payload: identity
// and counts only — NO word surfaces, the same leak-guard discipline as
// the lessons routes' LessonSummary projection.
type designationResponse struct {
	Lesson     string `json:"lesson"`
	Category   string `json:"category"`
	Status     string `json:"status"`
	ID         int64  `json:"id"`
	TextbookID int64  `json:"textbookId"`
	WordCount  int    `json:"wordCount"`
}
