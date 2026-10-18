// poems.go implements the Issue #11 poem routes under /api/v1:
//
//   - POST /poems — the confirmed commit. The parent reviewed/edited/
//     checked the recognized drafts in the preview; this endpoint is the
//     ONLY write path (未确认的原文不正式入库 — the recognition route stays
//     draft-only). Each poem lands as ONE whole work (title, author,
//     ordered lines) in the Go-owned poems table; a repeat confirmation
//     reports existing and never duplicates (fingerprint identity, not
//     title). The optional textbook_id attribution rides the commit with
//     each poem's own lesson — the sticky-include rule preserves an
//     explicit include on re-import.
//   - GET /poems?category=&q= — the unified content view's 古诗 page:
//     whole works with their attribution projection and the Issue #4
//     scope verdict (已学进度规则仍适用于古诗).
//   - POST /poems/{id}/assign — the content view's explicit attribution
//     management (lesson + include_in_scope refresh together).
//
// Error envelope: the package's locked {"error":{"code","message"}}
// shape. Route-specific codes: poems_unavailable (503, the poem store
// was not wired — a degraded boot, the recognizer's RT-10 transparency
// precedent), invalid_request (400), not_found (404, assign target).
package api

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/xiaotian/home-tutor-agent/pkg/database"
	"github.com/xiaotian/home-tutor-agent/pkg/infra"
	"github.com/xiaotian/home-tutor-agent/pkg/poem"
)

// codePoemsUnavailable is the 503 code for an unwired poem store.
const codePoemsUnavailable = "poems_unavailable"

// poemStore is the nil-safe accessor for the optional poem dependency: a
// degraded boot (or a cheap test handler) mounts the routes but answers
// 503 honestly instead of 404ing (the recognizer's precedent).
func (h *handler) poemStore() *poem.Store {
	return h.poems
}

// commitPoems serves POST /poems. Validation runs at the gate; the store
// commits in one transaction and the response reports PER-POEM outcomes
// (added/existing/invalid) plus the counts — 重复确认不复制作品, and a
// partial reality is never rendered as a plain success.
func (h *handler) commitPoems(w http.ResponseWriter, r *http.Request) {
	store := h.poemStore()
	if store == nil {
		writeError(w, http.StatusServiceUnavailable, codePoemsUnavailable,
			"poem storage is not configured on this server")
		return
	}

	var req PoemCommitRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if msg := validatePoemCommitRequest(&req); msg != "" {
		writeError(w, http.StatusBadRequest, codeInvalidRequest, msg)
		return
	}

	// The attribution target must EXIST before anything writes: a commit
	// against a deleted textbook id would store attribution rows pointing
	// nowhere (the join degrades, the parent sees a nameless chip) — the
	// word flow's resolve-target-first rule, enforced server-side.
	if req.TextbookID > 0 && h.textbooks != nil {
		if _, err := h.textbooks.Get(r.Context(), req.TextbookID); err != nil {
			if errors.Is(err, database.ErrTextbookNotFound) {
				writeError(w, http.StatusBadRequest, codeInvalidRequest,
					"textbook_id does not exist")
				return
			}
			h.logger.Error("commit poems: textbook lookup failed",
				infra.F("component", "web/api"),
				infra.ErrField(err),
			)
			writeError(w, http.StatusInternalServerError, codeInternal,
				"failed to verify the textbook")
			return
		}
	}

	input := poem.CommitInput{Category: req.Category, TextbookID: req.TextbookID}
	for _, p := range req.Poems {
		input.Drafts = append(input.Drafts, poem.Draft{
			Title: p.Title, Author: p.Author, Lines: p.Lines, Lesson: p.Lesson,
		})
	}
	outcomes, err := store.Commit(r.Context(), input)
	if err != nil {
		h.logger.Error("commit poems failed",
			infra.F("component", "web/api"),
			infra.ErrField(err),
		)
		writeError(w, http.StatusInternalServerError, codeInternal,
			"failed to save poems, please retry")
		return
	}

	added, existing := 0, 0
	for _, o := range outcomes {
		switch o.Status {
		case poem.StatusAdded:
			added++
		case poem.StatusExisting:
			existing++
		}
	}
	writeJSON(w, poemsCommitResponse{
		Results: outcomes, Added: added, Existing: existing,
	})
}

// poemsCommitResponse is the POST /poems body: the per-poem outcomes plus
// the honest counts (added+existing+invalid = len(poems) by construction).
type poemsCommitResponse struct {
	Results  []poem.CommitOutcome `json:"results"`
	Added    int                  `json:"added"`
	Existing int                  `json:"existing"`
}

// poemJSON is one row of the GET /poems response: the whole work plus the
// management projection. Lines are ALWAYS an array (never null); the scope
// verdict is display/management state — the poem classroom lands with the
// NEXT task, and a poem can never enter the word-dictation selection
// (poems are not cards).
type poemJSON struct {
	Title       string            `json:"title"`
	Author      string            `json:"author"`
	Category    string            `json:"category"`
	CreatedAt   string            `json:"created_at"`
	ScopeReason string            `json:"scope_reason"`
	Lines       []string          `json:"lines"`
	Textbooks   []poem.Assignment `json:"textbooks"`
	ID          int64             `json:"id"`
	InScope     bool              `json:"in_scope"`
}

// poemsResponse is the GET /poems envelope.
type poemsResponse struct {
	Poems []poemJSON `json:"poems"`
}

// listPoems serves GET /poems?category=&q= (both optional — the unified
// content view's subject filter rides category, the search box rides q).
// A non-empty category must pass the D-06 enum check.
func (h *handler) listPoems(w http.ResponseWriter, r *http.Request) {
	store := h.poemStore()
	if store == nil {
		writeError(w, http.StatusServiceUnavailable, codePoemsUnavailable,
			"poem storage is not configured on this server")
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

	rows, err := store.List(r.Context(), poem.ListFilter{Category: category, Query: q})
	if err != nil {
		h.logger.Error("list poems failed",
			infra.F("component", "web/api"),
			infra.ErrField(err),
		)
		writeError(w, http.StatusInternalServerError, codeInternal,
			"failed to list poems")
		return
	}

	poems := make([]poemJSON, 0, len(rows))
	for _, p := range rows {
		row := poemJSON{
			ID: p.ID, Title: p.Title, Author: p.Author, Category: p.Category,
			CreatedAt: p.CreatedAt, Lines: p.Lines,
			InScope: p.InScope, ScopeReason: p.ScopeReason,
			Textbooks: []poem.Assignment{},
		}
		if p.Assignments != nil {
			row.Textbooks = p.Assignments
		}
		poems = append(poems, row)
	}
	writeJSON(w, poemsResponse{Poems: poems})
}

// assignPoem serves POST /poems/{id}/assign — the content view's explicit
// attribution management. The poem id parses from the path; a missing poem
// is a 404, a missing textbook a 400 (the target of a management action
// must exist before anything writes).
func (h *handler) assignPoem(w http.ResponseWriter, r *http.Request) {
	store := h.poemStore()
	if store == nil {
		writeError(w, http.StatusServiceUnavailable, codePoemsUnavailable,
			"poem storage is not configured on this server")
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusBadRequest, codeInvalidRequest, "invalid poem id")
		return
	}

	var req PoemAssignRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if msg := validatePoemAssignRequest(&req); msg != "" {
		writeError(w, http.StatusBadRequest, codeInvalidRequest, msg)
		return
	}
	if h.textbooks != nil {
		if _, err := h.textbooks.Get(r.Context(), req.TextbookID); err != nil {
			if errors.Is(err, database.ErrTextbookNotFound) {
				writeError(w, http.StatusBadRequest, codeInvalidRequest,
					"textbook_id does not exist")
				return
			}
			h.logger.Error("assign poem: textbook lookup failed",
				infra.F("component", "web/api"),
				infra.ErrField(err),
			)
			writeError(w, http.StatusInternalServerError, codeInternal,
				"failed to verify the textbook")
			return
		}
	}

	if err := store.Assign(r.Context(), id, req.TextbookID, req.Lesson, req.IncludeInScope); err != nil {
		if errors.Is(err, poem.ErrPoemNotFound) {
			writeError(w, http.StatusNotFound, codeNotFound, "poem not found")
			return
		}
		h.logger.Error("assign poem failed",
			infra.F("component", "web/api"),
			infra.F("poem_id", id),
			infra.ErrField(err),
		)
		writeError(w, http.StatusInternalServerError, codeInternal,
			"failed to save the attribution")
		return
	}
	writeJSON(w, map[string]string{"status": "assigned"})
}
