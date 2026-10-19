// passages.go implements the Issue #13 passage routes under /api/v1:
//
//   - POST /passages — the confirmed commit. The parent reviewed/edited/
//     checked the recognized drafts (title, author, ordered 自然段) and
//     picked each passage's practice range in the SAME preview; this
//     endpoint is the ONLY write path (未确认的原文不正式入库 — the
//     recognition route stays draft-only). Each passage lands as ONE
//     structured original (fingerprint identity, never title) with its
//     range and optional attribution in one transaction; a repeat
//     confirmation reports existing and never duplicates.
//   - GET /passages?category=&q= — the unified content view's 课文 page:
//     the structured originals with their ranges (练习目标), attribution
//     projection and the Issue #4 scope verdict (练习目标与教材进度可
//     共同查询).
//   - POST /passages/{id}/assign — the content view's explicit attribution
//     management (lesson + include_in_scope refresh together).
//   - POST /passages/{id}/scopes — add (or re-select) ONE practice range:
//     whole or a 1-based paragraph set. The same selection re-confirmed
//     reports existing and reuses the row (重复确认不重复建立相同范围).
//   - DELETE /passages/{id}/scopes/{sid} — remove ONE range (范围可调整
//     而不用重复导入; the passage, its other ranges and its attribution
//     all stay).
//
// Error envelope: the package's locked {"error":{"code","message"}}
// shape. Route-specific codes: passages_unavailable (503, the passage
// store was not wired — a degraded boot, the poems precedent),
// invalid_request (400, including an out-of-range paragraph set — a
// range must point at REAL paragraphs of its own text), not_found (404,
// assign/scope target).
package api

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/xiaotian/home-tutor-agent/pkg/database"
	"github.com/xiaotian/home-tutor-agent/pkg/infra"
	"github.com/xiaotian/home-tutor-agent/pkg/passage"
)

// codePassagesUnavailable is the 503 code for an unwired passage store.
const codePassagesUnavailable = "passages_unavailable"

// passageStore is the nil-safe accessor for the optional passage
// dependency: a degraded boot (or a cheap test handler) mounts the routes
// but answers 503 honestly instead of 404ing (the poems precedent).
func (h *handler) passageStore() *passage.Store {
	return h.passages
}

// commitPassages serves POST /passages. Validation runs at the gate; the
// store commits in one transaction and the response reports PER-PASSAGE
// outcomes (added/existing/invalid, plus each range's own outcome) and
// the counts — 重复确认不复制课文, and a partial reality is never
// rendered as a plain success.
func (h *handler) commitPassages(w http.ResponseWriter, r *http.Request) {
	store := h.passageStore()
	if store == nil {
		writeError(w, http.StatusServiceUnavailable, codePassagesUnavailable,
			"passage storage is not configured on this server")
		return
	}

	var req PassageCommitRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if msg := validatePassageCommitRequest(&req); msg != "" {
		writeError(w, http.StatusBadRequest, codeInvalidRequest, msg)
		return
	}

	// The attribution target must EXIST before anything writes: a commit
	// against a deleted textbook id would store attribution rows pointing
	// nowhere (the word/poem flow's resolve-target-first rule).
	if req.TextbookID > 0 && h.textbooks != nil {
		if _, err := h.textbooks.Get(r.Context(), req.TextbookID); err != nil {
			if errors.Is(err, database.ErrTextbookNotFound) {
				writeError(w, http.StatusBadRequest, codeInvalidRequest,
					"textbook_id does not exist")
				return
			}
			h.logger.Error("commit passages: textbook lookup failed",
				infra.F("component", "web/api"),
				infra.ErrField(err),
			)
			writeError(w, http.StatusInternalServerError, codeInternal,
				"failed to verify the textbook")
			return
		}
	}

	input := passage.CommitInput{Category: req.Category, TextbookID: req.TextbookID}
	for _, p := range req.Passages {
		draft := passage.Draft{
			Title: p.Title, Author: p.Author,
			Lesson: p.Lesson, Paragraphs: p.Paragraphs,
		}
		if p.Scope != nil {
			draft.Scope = &passage.Scope{Whole: p.Scope.Whole, Paragraphs: p.Scope.Paragraphs}
		}
		input.Drafts = append(input.Drafts, draft)
	}
	outcomes, err := store.Commit(r.Context(), input)
	if err != nil {
		h.logger.Error("commit passages failed",
			infra.F("component", "web/api"),
			infra.ErrField(err),
		)
		writeError(w, http.StatusInternalServerError, codeInternal,
			"failed to save passages, please retry")
		return
	}

	added, existing := 0, 0
	for _, o := range outcomes {
		switch o.Status {
		case passage.StatusAdded:
			added++
		case passage.StatusExisting:
			existing++
		}
	}
	writeJSON(w, passagesCommitResponse{
		Results: outcomes, Added: added, Existing: existing,
	})
}

// passagesCommitResponse is the POST /passages body: the per-passage
// outcomes plus the honest counts (added+existing+invalid =
// len(passages) by construction).
type passagesCommitResponse struct {
	Results  []passage.CommitOutcome `json:"results"`
	Added    int                     `json:"added"`
	Existing int                     `json:"existing"`
}

// passageJSON is one row of the GET /passages response: the structured
// original plus the management projection. Paragraphs and scopes are
// ALWAYS arrays (never null); the scope verdict is display/management
// state — the passage classroom lands with a later task, and a passage
// can never enter the word-dictation selection (passages are not cards).
type passageJSON struct {
	Title       string               `json:"title"`
	Author      string               `json:"author"`
	Category    string               `json:"category"`
	CreatedAt   string               `json:"created_at"`
	ScopeReason string               `json:"scope_reason"`
	Paragraphs  []string             `json:"paragraphs"`
	Textbooks   []passage.Assignment `json:"textbooks"`
	Scopes      []passage.ScopeRow   `json:"scopes"`
	ID          int64                `json:"id"`
	InScope     bool                 `json:"in_scope"`
}

// passagesResponse is the GET /passages envelope.
type passagesResponse struct {
	Passages []passageJSON `json:"passages"`
}

// listPassages serves GET /passages?category=&q= (both optional — the
// unified content view's subject filter rides category, the search box
// rides q). A non-empty category must pass the D-06 enum check.
func (h *handler) listPassages(w http.ResponseWriter, r *http.Request) {
	store := h.passageStore()
	if store == nil {
		writeError(w, http.StatusServiceUnavailable, codePassagesUnavailable,
			"passage storage is not configured on this server")
		return
	}

	category := r.URL.Query().Get("category")
	q := r.URL.Query().Get("q")
	if category != "" {
		if err := ValidateCategory(category); err != nil {
			writeError(w, http.StatusBadRequest, codeInvalidRequest,
				"category must be \"english\" or \"chinese\"")
			return
		}
	}

	rows, err := store.List(r.Context(), passage.ListFilter{Category: category, Query: q})
	if err != nil {
		h.logger.Error("list passages failed",
			infra.F("component", "web/api"),
			infra.ErrField(err),
		)
		writeError(w, http.StatusInternalServerError, codeInternal,
			"failed to list passages")
		return
	}

	passages := make([]passageJSON, 0, len(rows))
	for _, p := range rows {
		row := passageJSON{
			ID: p.ID, Title: p.Title, Author: p.Author, Category: p.Category,
			CreatedAt: p.CreatedAt, Paragraphs: p.Paragraphs,
			InScope: p.InScope, ScopeReason: p.ScopeReason,
			Textbooks: []passage.Assignment{},
			Scopes:    []passage.ScopeRow{},
		}
		if p.Assignments != nil {
			row.Textbooks = p.Assignments
		}
		if p.Scopes != nil {
			row.Scopes = p.Scopes
		}
		passages = append(passages, row)
	}
	writeJSON(w, passagesResponse{Passages: passages})
}

// assignPassage serves POST /passages/{id}/assign — the content view's
// explicit attribution management. The passage id parses from the path;
// a missing passage is a 404, a missing textbook a 400 (the target of a
// management action must exist before anything writes).
func (h *handler) assignPassage(w http.ResponseWriter, r *http.Request) {
	store := h.passageStore()
	if store == nil {
		writeError(w, http.StatusServiceUnavailable, codePassagesUnavailable,
			"passage storage is not configured on this server")
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusBadRequest, codeInvalidRequest, "invalid passage id")
		return
	}

	var req PassageAssignRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if msg := validatePassageAssignRequest(&req); msg != "" {
		writeError(w, http.StatusBadRequest, codeInvalidRequest, msg)
		return
	}
	if h.textbooks != nil {
		if _, err = h.textbooks.Get(r.Context(), req.TextbookID); err != nil {
			if errors.Is(err, database.ErrTextbookNotFound) {
				writeError(w, http.StatusBadRequest, codeInvalidRequest,
					"textbook_id does not exist")
				return
			}
			h.logger.Error("assign passage: textbook lookup failed",
				infra.F("component", "web/api"),
				infra.ErrField(err),
			)
			writeError(w, http.StatusInternalServerError, codeInternal,
				"failed to verify the textbook")
			return
		}
	}

	if err = store.Assign(r.Context(), id, req.TextbookID, req.Lesson, req.IncludeInScope); err != nil {
		if errors.Is(err, passage.ErrPassageNotFound) {
			writeError(w, http.StatusNotFound, codeNotFound, "passage not found")
			return
		}
		h.logger.Error("assign passage failed",
			infra.F("component", "web/api"),
			infra.F("passage_id", id),
			infra.ErrField(err),
		)
		writeError(w, http.StatusInternalServerError, codeInternal,
			"failed to save the attribution")
		return
	}
	writeJSON(w, map[string]string{"status": "assigned"})
}

// addPassageScope serves POST /passages/{id}/scopes — the content view's
// range selection/re-selection. The gate validates the shape; the store
// validates the indexes against the STORED text (a range must point at
// real paragraphs of its own 原文 — an edited draft elsewhere never
// shifts another passage's indexes) and lands the range idempotently.
func (h *handler) addPassageScope(w http.ResponseWriter, r *http.Request) {
	store := h.passageStore()
	if store == nil {
		writeError(w, http.StatusServiceUnavailable, codePassagesUnavailable,
			"passage storage is not configured on this server")
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusBadRequest, codeInvalidRequest, "invalid passage id")
		return
	}

	var req PassageScopeRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if msg := validatePassageScopeShape(&req, 0, "scope"); msg != "" {
		writeError(w, http.StatusBadRequest, codeInvalidRequest, msg)
		return
	}

	outcome, err := store.AddScope(r.Context(), id, passage.Scope{
		Whole: req.Whole, Paragraphs: req.Paragraphs,
	})
	if err != nil {
		switch {
		case errors.Is(err, passage.ErrPassageNotFound):
			writeError(w, http.StatusNotFound, codeNotFound, "passage not found")
		case errors.Is(err, passage.ErrScopeOutOfRange):
			writeError(w, http.StatusBadRequest, codeInvalidRequest,
				"scope paragraphs out of range for this passage")
		default:
			h.logger.Error("add passage scope failed",
				infra.F("component", "web/api"),
				infra.F("passage_id", id),
				infra.ErrField(err),
			)
			writeError(w, http.StatusInternalServerError, codeInternal,
				"failed to save the practice range")
		}
		return
	}
	writeJSON(w, passageScopeResponse{Status: outcome.Status, Scope: outcome.Row})
}

// passageScopeResponse is the POST /passages/{id}/scopes body: the
// landed range plus its added/existing verdict (an honest receipt for a
// re-confirmation: nothing duplicated, the stored row reused).
type passageScopeResponse struct {
	Status string           `json:"status"`
	Scope  passage.ScopeRow `json:"scope"`
}

// removePassageScope serves DELETE /passages/{id}/scopes/{sid}: removing
// one range never touches the passage, its other ranges or its
// attribution. A scope id that does not sit on the given passage is a
// 404.
func (h *handler) removePassageScope(w http.ResponseWriter, r *http.Request) {
	store := h.passageStore()
	if store == nil {
		writeError(w, http.StatusServiceUnavailable, codePassagesUnavailable,
			"passage storage is not configured on this server")
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusBadRequest, codeInvalidRequest, "invalid passage id")
		return
	}
	scopeID, err := strconv.ParseInt(r.PathValue("sid"), 10, 64)
	if err != nil || scopeID <= 0 {
		writeError(w, http.StatusBadRequest, codeInvalidRequest, "invalid scope id")
		return
	}

	if err = store.RemoveScope(r.Context(), id, scopeID); err != nil {
		if errors.Is(err, passage.ErrScopeNotFound) {
			writeError(w, http.StatusNotFound, codeNotFound, "scope not found")
			return
		}
		h.logger.Error("remove passage scope failed",
			infra.F("component", "web/api"),
			infra.F("passage_id", id),
			infra.F("scope_id", scopeID),
			infra.ErrField(err),
		)
		writeError(w, http.StatusInternalServerError, codeInternal,
			"failed to remove the practice range")
		return
	}
	writeJSON(w, map[string]string{"status": "removed"})
}
