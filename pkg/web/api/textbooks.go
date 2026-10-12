// textbooks.go implements the Issue #4 textbook routes:
//
//	GET  /textbooks            — list (Go repo read; optional ?category=)
//	POST /textbooks            — create (Python write via RunTextbookCreate)
//	PUT  /textbooks/{id}       — update metadata/lessons/progress/primary
//	POST /textbooks/{id}/assign   — batch assign cards to a lesson (待确认
//	                               with an empty lesson) or include-in-scope
//	POST /textbooks/{id}/unassign — drop the cards' assignments
//
// Read/write split (write-separation): reads are pure Go against SQLite
// (D-12), every mutation goes through pybridge so Python stays the sole
// writer of textbooks/card_assignments. Lesson names are validated
// against the CURRENT lesson list before any subprocess spawn (a typo
// gets a 400 naming the valid lessons, not a 502 round trip), and the
// card category must match the textbook's (an english word in a chinese
// textbook is a client error, not an infrastructure failure). Progress
// (learnedLessons ≤ len(lessons)) is validated against the FINAL list —
// the new one when the same request replaces it.
package api

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"

	"github.com/xiaotian/home-tutor-agent/pkg/database"
	"github.com/xiaotian/home-tutor-agent/pkg/infra"
	"github.com/xiaotian/home-tutor-agent/pkg/pybridge"
)

// parseTextbookID extracts the {id} path segment (non-numeric or
// non-positive → 0 + false; the caller answers 400).
func parseTextbookID(r *http.Request) (int64, bool) {
	raw := r.PathValue("id")
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		return 0, false
	}
	return id, true
}

// listTextbooks serves GET /textbooks (?category=english|chinese optional).
// On a pre-migration database the repo degrades to an empty list.
func (h *handler) listTextbooks(w http.ResponseWriter, r *http.Request) {
	category := r.URL.Query().Get("category")
	if category != "" {
		if err := ValidateCategory(category); err != nil {
			writeError(w, http.StatusBadRequest, codeInvalidRequest,
				"category must be \"english\" or \"chinese\"")
			return
		}
	}
	books, err := h.textbooks.List(r.Context(), category)
	if err != nil {
		h.logger.Error("list textbooks failed",
			infra.F("component", "web/api"),
			infra.ErrField(err),
		)
		writeError(w, http.StatusInternalServerError, codeInternal, "failed to list textbooks")
		return
	}
	if books == nil {
		books = []database.Textbook{}
	}
	writeJSON(w, textbooksResponse{Textbooks: books})
}

// createTextbook serves POST /textbooks. The created textbook is re-read
// through the repository so the response carries the full row (id,
// lessons, progress) — the script only prints {id, name}.
func (h *handler) createTextbook(w http.ResponseWriter, r *http.Request) {
	var req TextbookCreateRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if msg := validateTextbookCreateRequest(&req); msg != "" {
		writeError(w, http.StatusBadRequest, codeInvalidRequest, msg)
		return
	}
	opts := pybridge.TextbookCreateOpts{
		Name: req.Name, Category: req.Category,
		Edition: req.Edition, Stage: req.Stage, Grade: req.Grade,
		Volume: req.Volume, Module: req.Module, Lessons: req.Lessons,
	}
	if req.Lessons == nil {
		opts.Lessons = []string{}
	}
	mutation, err := h.exec.RunTextbookCreate(r.Context(), opts)
	if err != nil {
		h.mapBridgeError(w, "create textbook", err)
		return
	}
	book, err := h.textbooks.Get(r.Context(), mutation.ID)
	if err != nil {
		// The write succeeded but the read-back failed; answer with the
		// mutation payload rather than hiding the created row.
		h.logger.Warn("textbook read-back after create failed",
			infra.F("component", "web/api"),
			infra.F("textbook_id", mutation.ID),
			infra.ErrField(err),
		)
		writeJSON(w, textbookCreatedResponse{ID: mutation.ID, Name: mutation.Name})
		return
	}
	writeJSON(w, book)
}

// textbookCreatedResponse is the degraded read-back payload.
type textbookCreatedResponse struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

// updateTextbook serves PUT /textbooks/{id}. LearnedLessons is a pointer
// (0 is a legal progress) and SetPrimary a plain bool; every absent field
// keeps its column (the nil-keeps contract).
func (h *handler) updateTextbook(w http.ResponseWriter, r *http.Request) {
	id, ok := parseTextbookID(r)
	if !ok {
		writeError(w, http.StatusBadRequest, codeInvalidRequest, "textbook id must be a positive integer")
		return
	}
	var req TextbookUpdateRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if msg := validateTextbookUpdateRequest(&req); msg != "" {
		writeError(w, http.StatusBadRequest, codeInvalidRequest, msg)
		return
	}
	book, err := h.textbooks.Get(r.Context(), id)
	if err != nil {
		if errors.Is(err, database.ErrTextbookNotFound) {
			writeError(w, http.StatusNotFound, codeNotFound, "textbook not found")
			return
		}
		h.mapBridgeError(w, "read textbook", err)
		return
	}
	// Cross-field rule: progress is validated against the FINAL lesson
	// list (the incoming one when the request also replaces it).
	lessons := book.Lessons
	if req.Lessons != nil {
		lessons = req.Lessons
	}
	if req.LearnedLessons != nil && *req.LearnedLessons > len(lessons) {
		writeError(w, http.StatusBadRequest, codeInvalidRequest,
			fmt.Sprintf("learnedLessons must be 0..%d for this textbook's lesson list", len(lessons)))
		return
	}

	opts := pybridge.TextbookUpdateOpts{ID: id}
	if req.Name != nil {
		opts.Name = *req.Name
	}
	if req.Edition != nil {
		opts.Edition = *req.Edition
	}
	if req.Stage != nil {
		opts.Stage = *req.Stage
	}
	if req.Grade != nil {
		opts.Grade = *req.Grade
	}
	if req.Volume != nil {
		opts.Volume = *req.Volume
	}
	if req.Module != nil {
		opts.Module = *req.Module
	}
	if req.Lessons != nil {
		opts.HasLessons = true
		opts.Lessons = req.Lessons
	}
	if req.LearnedLessons != nil {
		opts.HasLearnedLessons = true
		opts.LearnedLessons = *req.LearnedLessons
	}
	opts.SetPrimary = req.SetPrimary

	if _, err := h.exec.RunTextbookUpdate(r.Context(), opts); err != nil {
		h.mapBridgeError(w, "update textbook", err)
		return
	}
	updated, err := h.textbooks.Get(r.Context(), id)
	if err != nil {
		h.mapBridgeError(w, "read textbook", err)
		return
	}
	writeJSON(w, updated)
}

// assignTextbookWords serves POST /textbooks/{id}/assign. Card ids are
// resolved through the card repository BEFORE the subprocess: a missing
// id answers 404 with the real ids and a category mismatch answers 400 —
// both without wasting a Python round trip (the script re-checks
// atomically for direct CLI callers).
func (h *handler) assignTextbookWords(w http.ResponseWriter, r *http.Request) {
	h.mutateAssignments(w, r, true)
}

// unassignTextbookWords serves POST /textbooks/{id}/unassign.
func (h *handler) unassignTextbookWords(w http.ResponseWriter, r *http.Request) {
	h.mutateAssignments(w, r, false)
}

// mutateAssignments is the shared assign/unassign flow: decode → validate
// the batch shape → load the textbook (404) → for assigns, validate the
// lesson name against the textbook's list (400) and every card's category
// (400) / existence (404) → run the Python mutation → respond with its
// report.
func (h *handler) mutateAssignments(w http.ResponseWriter, r *http.Request, assign bool) {
	id, ok := parseTextbookID(r)
	if !ok {
		writeError(w, http.StatusBadRequest, codeInvalidRequest, "textbook id must be a positive integer")
		return
	}
	var req TextbookAssignRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if msg := validateTextbookAssignRequest(&req); msg != "" {
		writeError(w, http.StatusBadRequest, codeInvalidRequest, msg)
		return
	}

	book, err := h.textbooks.Get(r.Context(), id)
	if err != nil {
		if errors.Is(err, database.ErrTextbookNotFound) {
			writeError(w, http.StatusNotFound, codeNotFound, "textbook not found")
			return
		}
		h.mapBridgeError(w, "read textbook", err)
		return
	}

	if assign {
		if req.Lesson != "" && !lessonInList(req.Lesson, book.Lessons) {
			writeError(w, http.StatusBadRequest, codeInvalidRequest,
				fmt.Sprintf("lesson %q is not in textbook %q's lesson list", req.Lesson, book.Name))
			return
		}
		if !h.cardsMatchCategory(w, r, req.CardIDs, book.Category) {
			return
		}
		result, err := h.exec.RunAssignWords(r.Context(), pybridge.AssignWordsOpts{
			TextbookID: id, IDs: req.CardIDs, Lesson: req.Lesson, IncludeInScope: req.IncludeInScope,
		})
		if err != nil {
			h.mapBridgeError(w, "assign words", err)
			return
		}
		writeJSON(w, result)
		return
	}

	result, err := h.exec.RunUnassignWords(r.Context(), pybridge.UnassignWordsOpts{
		TextbookID: id, IDs: req.CardIDs,
	})
	if err != nil {
		h.mapBridgeError(w, "unassign words", err)
		return
	}
	writeJSON(w, result)
}

// lessonInList is the exact-match membership check for a lesson name.
func lessonInList(lesson string, lessons []string) bool {
	for _, name := range lessons {
		if name == lesson {
			return true
		}
	}
	return false
}

// cardsMatchCategory resolves the batch through the card repository and
// writes the error response itself: missing ids answer 404 with the real
// ids, category mismatches answer 400 naming the offenders. False means
// the response is written and the caller must return; a repository read
// failure defers to the Python side's atomic check (its diagnostic maps
// through the standard bridge-error path).
func (h *handler) cardsMatchCategory(w http.ResponseWriter, r *http.Request, ids []int64, category string) bool {
	cards, err := h.cards.List(r.Context(), database.CardFilter{})
	if err != nil {
		return true
	}
	byID := make(map[int64]database.Card, len(cards))
	for _, c := range cards {
		byID[c.ID] = c
	}
	var missing, mismatched []int64
	seen := make(map[int64]struct{}, len(ids))
	for _, id := range ids {
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		card, ok := byID[id]
		if !ok {
			missing = append(missing, id)
			continue
		}
		if card.Category != category {
			mismatched = append(mismatched, id)
		}
	}
	if len(missing) > 0 {
		writeError(w, http.StatusNotFound, codeNotFound,
			"card ids not found: "+int64SliceString(missing))
		return false
	}
	if len(mismatched) > 0 {
		writeError(w, http.StatusBadRequest, codeInvalidRequest,
			fmt.Sprintf("card categories do not match the textbook category %q: ids %s", category, int64SliceString(mismatched)))
		return false
	}
	return true
}

// int64SliceString renders ids as "1, 2, 3" (the Python diagnostic shape).
func int64SliceString(ids []int64) string {
	out := ""
	for i, id := range ids {
		if i > 0 {
			out += ", "
		}
		out += strconv.FormatInt(id, 10)
	}
	return out
}

// textbooksResponse is the GET /textbooks envelope.
type textbooksResponse struct {
	Textbooks []database.Textbook `json:"textbooks"`
}
