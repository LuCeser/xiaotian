// Package api implements the parent-facing REST endpoints under /api/v1
// (Phase 9, WEB-01).
//
// handlers.go owns the api.Handler: an internal http.ServeMux using Go 1.22
// method patterns ("GET /words", "POST /words"). cmd/service/main.go mounts
// it ONCE via publicMux.Handle("/api/v1/", http.StripPrefix("/api/v1",
// api.NewHandler(...))) — the single-mount contract (Plan 09-01). Route
// names are locked here (RESEARCH Pitfall 11: exact paths, no trailing
// slashes — "/words" not "/words/").
//
// JSON responses follow the pkg/infra/health.go encode pattern: the
// Content-Type header is set BEFORE WriteHeader, then json.NewEncoder.
// All error responses use the locked {"error":{"code","message"}}
// envelope (T-09-07: code+message only — no stack traces, no raw stderr).
package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/xiaotian/home-tutor-agent/pkg/database"
	"github.com/xiaotian/home-tutor-agent/pkg/infra"
	"github.com/xiaotian/home-tutor-agent/pkg/lesson"
	"github.com/xiaotian/home-tutor-agent/pkg/ocr"
	"github.com/xiaotian/home-tutor-agent/pkg/passage"
	"github.com/xiaotian/home-tutor-agent/pkg/poem"
	"github.com/xiaotian/home-tutor-agent/pkg/pybridge"
)

// Error codes locked by the wire contract (Plan 09-01; the parent page and
// e2e specs depend on these strings):
//   - invalid_request: client sent a malformed/invalid body or query.
//   - not_found:       the edit/rename/delete target does not exist (the
//     script's stdout diagnostic rides as the message — Pitfall 2).
//   - python_failed:   the Python subprocess layer failed (sentinel error).
//   - internal:        unexpected server-side failure.
const (
	codeInvalidRequest = "invalid_request"
	codeNotFound       = "not_found"
	codePythonFailed   = "python_failed"
	codeInternal       = "internal"
)

// maxStdoutDiagnosticRunes bounds the stdout diagnostic echoed to clients
// in the not_found message (T-09-07: bounded command output only — no
// stderr dumps, no stack traces). ~300 runes covers the script's
// diagnostics with headroom while capping response size.
const maxStdoutDiagnosticRunes = 300

// handler is the api package's http.Handler. Dependencies arrive via
// constructor DI (D-42): the pybridge Executor for the write path
// (Python is the sole writer, ADR write-separation), the CardRepository
// for the read path (D-12: zero Python spawns on reads), the PythonConfig
// whose DataDir anchors the fsrs_config.json write path (Plan 09-05 —
// config-derived, never user input, T-09-10), and — since 13.3-02 — the
// three Go-owned lesson stores behind the read-only parent achievement
// routes (GET /lessons, GET /lessons/{id}): the lesson store for rows and
// completion projection, the practice store for extra-practice detail, and
// the receipt store for the honest save status (submitted ≠ written-to-FSRS).
// Since Issue #3, recognizer carries the word-list photo pipeline (nil when
// the service boots without a vision key — POST /words/photo/recognize then
// answers 503 recognition_unavailable instead of 404ing). Since Issue #8,
// generator carries the missing-meaning supplement pipeline (nil without
// an LLM key — POST /words/meaning/generate answers 503
// generation_unavailable instead of 404ing). Since Issue #11, poems is the
// Go-owned whole-poem store behind the /poems routes (nil on a degraded
// boot — they answer 503 poems_unavailable instead of 404ing). Since
// Issue #13, passages is the Go-owned structured-original store behind
// the /passages routes (nil on a degraded boot — they answer 503
// passages_unavailable instead of 404ing).
type handler struct {
	exec       pybridge.Executor
	cards      database.CardRepository
	textbooks  database.TextbookRepository
	logger     infra.Logger
	lessons    *lesson.Store
	practice   *lesson.PracticeStore
	receipts   *lesson.ReceiptStore
	recognizer *ocr.WordlistPipeline
	generator  *ocr.MeaningGenerator
	poems      *poem.Store
	passages   *passage.Store
	cfg        infra.PythonConfig
}

// NewHandler builds the /api/v1 handler with its internal method-pattern
// ServeMux. Logger may be nil (NewNoOpLogger is substituted, D-47) so
// tests can construct the handler cheaply. recognizer and generator may
// be nil (photo recognition / meaning supplement unavailable — see the
// handler struct comment).
func NewHandler(exec pybridge.Executor, cards database.CardRepository, textbooks database.TextbookRepository, lessons *lesson.Store, practice *lesson.PracticeStore, receipts *lesson.ReceiptStore, recognizer *ocr.WordlistPipeline, generator *ocr.MeaningGenerator, poems *poem.Store, passages *passage.Store, cfg infra.PythonConfig, logger infra.Logger) http.Handler {
	if logger == nil {
		logger = infra.NewNoOpLogger()
	}
	h := &handler{exec: exec, cards: cards, textbooks: textbooks, lessons: lessons, practice: practice, receipts: receipts, recognizer: recognizer, generator: generator, poems: poems, passages: passages, cfg: cfg, logger: logger}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /words", h.listWords)
	mux.HandleFunc("POST /words", h.addWords)
	mux.HandleFunc("POST /words/photo/recognize", h.recognizeWordsPhoto)
	mux.HandleFunc("POST /words/meaning/generate", h.generateWordMeanings)
	mux.HandleFunc("GET /units", h.listUnits)
	mux.HandleFunc("PUT /words/{id}", h.editWord)
	mux.HandleFunc("PUT /words/participation", h.setWordParticipation)
	// Issue #4: textbook attribution + learned progress. Reads are pure
	// Go (the repo); writes go through pybridge (Python stays the sole
	// writer of textbooks/card_assignments).
	mux.HandleFunc("GET /textbooks", h.listTextbooks)
	mux.HandleFunc("POST /textbooks", h.createTextbook)
	mux.HandleFunc("PUT /textbooks/{id}", h.updateTextbook)
	mux.HandleFunc("POST /textbooks/{id}/assign", h.assignTextbookWords)
	mux.HandleFunc("POST /textbooks/{id}/unassign", h.unassignTextbookWords)
	mux.HandleFunc("POST /units/rename", h.renameUnit)
	mux.HandleFunc("DELETE /words", h.deleteWords)
	mux.HandleFunc("GET /settings/fsrs", h.getFSRS)
	mux.HandleFunc("PUT /settings/fsrs", h.putFSRS)
	// 13.3-02: the read-only parent achievement routes (ROADMAP success
	// criterion 2). The leak guard lives in the PROJECTION — an in-progress
	// lesson never reaches the response as anything but a summary row.
	mux.HandleFunc("GET /lessons", h.listLessons)
	mux.HandleFunc("GET /lessons/{id}", h.getLesson)
	// Issue #11: the whole-poem content routes (commit / list / assign).
	// The commit is the ONLY write path — the recognize route stays a
	// draft-only draft (未确认的原文不正式入库).
	mux.HandleFunc("POST /poems", h.commitPoems)
	mux.HandleFunc("GET /poems", h.listPoems)
	mux.HandleFunc("POST /poems/{id}/assign", h.assignPoem)
	// Issue #13: the structured-passage content routes (commit / list /
	// assign / practice-range selection). Same single-write-path rule.
	mux.HandleFunc("POST /passages", h.commitPassages)
	mux.HandleFunc("GET /passages", h.listPassages)
	mux.HandleFunc("POST /passages/{id}/assign", h.assignPassage)
	mux.HandleFunc("POST /passages/{id}/scopes", h.addPassageScope)
	mux.HandleFunc("DELETE /passages/{id}/scopes/{sid}", h.removePassageScope)
	return mux
}

// setWordParticipation handles PUT /words/participation with body
// {"ids", "participates"} (Issue #2): batch classroom-eligibility changes
// in ONE request → one Python process → one SQLite transaction. Validation
// (1..500 unique positive ids, explicit participates) runs here before the
// bridge; the script's all-or-nothing semantics mean a missing id surfaces
// as a 404 not_found via mapBridgeError's stdout mapping (Pitfall 2), and
// a 200 always means every requested card was updated — the parent UI can
// trust success as full success. Success responds 200 with the script's
// {requested, updated, missing} payload.
func (h *handler) setWordParticipation(w http.ResponseWriter, r *http.Request) {
	var req ParticipationRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if msg := validateParticipationRequest(&req); msg != "" {
		writeError(w, http.StatusBadRequest, codeInvalidRequest, msg)
		return
	}
	result, err := h.exec.RunSetParticipation(r.Context(), pybridge.SetParticipationOpts{
		IDs: req.IDs, Participates: *req.Participates,
	})
	if err != nil {
		h.mapBridgeError(w, "set word participation", err)
		return
	}
	writeJSON(w, result)
}

// stdoutNotFoundMsg inspects the captured subprocess stdout for the
// not-found diagnostics vocab_manager.py prints to STDOUT with exit 1
// (Pitfall 2):
//   - "Card 42 not found"            (cmd_edit / cmd_reset shape)
//   - "No cards found with unit ..." (rename-unit / delete-words unit mode)
//
// On a match it returns the message for the not_found envelope, truncated
// to maxStdoutDiagnosticRunes (T-09-07). Any other stdout (or empty)
// reports false so the caller falls through to the sentinel mapping.
func stdoutNotFoundMsg(stdout string) (string, bool) {
	trimmed := strings.TrimSpace(stdout)
	if trimmed == "" {
		return "", false
	}
	if !strings.Contains(trimmed, "not found") &&
		!strings.Contains(trimmed, "No cards found") {
		return "", false
	}
	if runes := []rune(trimmed); len(runes) > maxStdoutDiagnosticRunes {
		trimmed = string(runes[:maxStdoutDiagnosticRunes])
	}
	return trimmed, true
}

// mapBridgeError maps a pybridge Executor error onto the HTTP error
// envelope (shared by the editWord / renameUnit / deleteWords mutation
// handlers):
//  1. ExitError (ErrPythonExit) whose Stdout carries a not-found diagnostic
//     → 404 not_found with the truncated stdout text as the message —
//     Pitfall 2 closed end-to-end (Plan 09-03 added ExitError.Stdout).
//  2. Any of the four pybridge sentinels → 502 python_failed (T-09-07:
//     code+message only, never raw stderr).
//  3. Anything else → 500 internal.
func (h *handler) mapBridgeError(w http.ResponseWriter, op string, err error) {
	var exitErr *pybridge.ExitError
	if errors.Is(err, pybridge.ErrPythonExit) && errors.As(err, &exitErr) {
		if msg, ok := stdoutNotFoundMsg(exitErr.Stdout); ok {
			writeError(w, http.StatusNotFound, codeNotFound, msg)
			return
		}
	}
	switch {
	case errors.Is(err, pybridge.ErrPythonExit),
		errors.Is(err, pybridge.ErrPythonTimeout),
		errors.Is(err, pybridge.ErrPythonJSONParse),
		errors.Is(err, pybridge.ErrPythonEmpty):
		h.logger.Error(op+": python bridge failed",
			infra.F("component", "web/api"),
			infra.ErrField(err),
		)
		writeError(w, http.StatusBadGateway, codePythonFailed,
			"operation failed, please retry")
	default:
		h.logger.Error(op+" failed",
			infra.F("component", "web/api"),
			infra.ErrField(err),
		)
		writeError(w, http.StatusInternalServerError, codeInternal,
			"internal error")
	}
}

// editWord handles PUT /words/{id} with body {"word", "meaning?",
// "example?", "unit?"} (Plan 09-05). The id is parsed from the path
// (non-numeric or non-positive → 400 invalid_request); the body decodes
// strictly (a "category" key is an unknown field → 400, D-07 read-only).
// Pointer fields map straight onto pybridge.EditOpts so nil stays nil (keep
// the column) and provided-empty clears it (Plan 09-02 COALESCE contract).
// Success responds 200 with the script's {"edited":{"id","word"}} payload.
func (h *handler) editWord(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusBadRequest, codeInvalidRequest,
			"invalid word id")
		return
	}

	var req EditRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if msg := validateEditRequest(&req); msg != "" {
		writeError(w, http.StatusBadRequest, codeInvalidRequest, msg)
		return
	}

	result, err := h.exec.RunEdit(r.Context(), pybridge.EditOpts{
		ID:      id,
		Word:    &req.Word,
		Meaning: req.Meaning,
		Example: req.Example,
		Unit:    req.Unit,
	})
	if err != nil {
		h.mapBridgeError(w, "edit word", err)
		return
	}
	writeJSON(w, result)
}

// renameUnit handles POST /units/rename with body {"old_unit", "new_unit",
// "category"} (Plan 09-05). It dispatches straight to
// pybridge.RunRenameUnit; when the old unit matches no cards, the script's
// stdout diagnostic surfaces as a 404 not_found via mapBridgeError
// (Pitfall 2 end-to-end). Success responds 200 with the script's
// {"renamed","from","to"} payload.
func (h *handler) renameUnit(w http.ResponseWriter, r *http.Request) {
	var req RenameRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if msg := validateRenameRequest(&req); msg != "" {
		writeError(w, http.StatusBadRequest, codeInvalidRequest, msg)
		return
	}

	result, err := h.exec.RunRenameUnit(r.Context(), pybridge.RenameUnitOpts{
		OldUnit:  req.OldUnit,
		NewUnit:  req.NewUnit,
		Category: req.Category,
	})
	if err != nil {
		h.mapBridgeError(w, "rename unit", err)
		return
	}
	writeJSON(w, result)
}

// deleteWords handles DELETE /words with body {"words": [...]} XOR
// {"unit": ...} plus required {"category"} (Plan 09-05). The handler
// performs NO preview/dry-run logic of any kind: confirmation is entirely
// client-side (Plan 09-07's count-showing dialog), and this API is ALWAYS
// the confirmed path — RunDeleteWords dispatches to a deleteWordsArgs
// invocation that carries --confirm unconditionally (Plan 09-03 structural
// guarantee, Pitfall 1). XOR target validation happens in
// validateDeleteRequest (400 invalid_request) before any bridge call.
// Success responds 200 with the script's {"deleted": N} payload.
func (h *handler) deleteWords(w http.ResponseWriter, r *http.Request) {
	var req DeleteRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if msg := validateDeleteRequest(&req); msg != "" {
		writeError(w, http.StatusBadRequest, codeInvalidRequest, msg)
		return
	}

	result, err := h.exec.RunDeleteWords(r.Context(), pybridge.DeleteWordsOpts{
		Words:    req.Words,
		Unit:     req.Unit,
		Category: req.Category,
	})
	if err != nil {
		h.mapBridgeError(w, "delete words", err)
		return
	}
	writeJSON(w, result)
}

// wordJSON is one element of the GET /words response array. Field tags
// are snake_case per the shared response convention (health.go:18-26).
type wordJSON struct {
	Word     string `json:"word"`
	Meaning  string `json:"meaning"`
	Example  string `json:"example"`
	Unit     string `json:"unit"`
	Category string `json:"category"`
	// MeaningSource rides along for the browse view's 系统 supplement
	// marker (Issue #8): the display rule is meaning non-empty AND
	// meaning_source == "generated" — the client applies it, the wire
	// just carries the provenance.
	MeaningSource string `json:"meaning_source"`
	// ScopeReason / GroupLesson / InScope are the textbook gate the browse
	// page groups and explains by (Issue #89). They follow the same rule as
	// dictation eligibility. Participation stays a separate field: dictation
	// requires both. ScopeReason is supplementary when the word has no
	// textbook assignment; GroupLesson is then unused and the page groups
	// by Unit. GroupTextbookID names the book GroupLesson came from — two
	// books can share a lesson name, so the lesson alone cannot key a
	// group. The two strings sit with the other strings, ahead of the
	// slice: a slice between pointer fields extends the GC pointer prefix.
	ScopeReason     string                        `json:"scope_reason"`
	GroupLesson     string                        `json:"group_lesson"`
	Textbooks       []database.TextbookAssignment `json:"textbooks"`
	ID              int64                         `json:"id"`
	GroupTextbookID int64                         `json:"group_textbook_id"`
	InScope         bool                          `json:"in_scope"`
	Participates    bool                          `json:"participates"`
}

// wordsResponse is the GET /words response envelope.
type wordsResponse struct {
	Words []wordJSON `json:"words"`
}

// addResponse is the POST /words response body, mirroring the Python
// result contract {"added": N, "skipped": N} (vocab_manager.py:206).
type addResponse struct {
	Added   int `json:"added"`
	Skipped int `json:"skipped"`
}

// errorBody is the locked error envelope shape.
type errorBody struct {
	Error errorEnvelope `json:"error"`
}

type errorEnvelope struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// writeError emits {"error":{"code":...,"message":...}} with the JSON
// Content-Type set BEFORE WriteHeader (health.go pattern).
func writeError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	_ = enc.Encode(errorBody{Error: errorEnvelope{Code: code, Message: message}})
}

// writeJSON emits a 200 JSON body with the Content-Type header set BEFORE
// WriteHeader (health.go:61-64 pattern).
func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	enc := json.NewEncoder(w)
	_ = enc.Encode(v)
}

// listWords handles GET /words?category=&unit=&q= (all params optional —
// empty means unfiltered). A non-empty category must pass the enum check
// (D-06) or the request is rejected with 400 invalid_request. The read
// path goes straight to CardRepository.List — NO Python subprocess is
// spawned (D-12).
func (h *handler) listWords(w http.ResponseWriter, r *http.Request) {
	category := r.URL.Query().Get("category")
	unit := r.URL.Query().Get("unit")
	q := r.URL.Query().Get("q")

	if category != "" {
		if err := ValidateCategory(category); err != nil {
			writeError(w, http.StatusBadRequest, codeInvalidRequest,
				"category must be \"english\" or \"chinese\"")
			return
		}
	}

	cards, err := h.cards.List(r.Context(), database.CardFilter{
		Category: category,
		Unit:     unit,
		Query:    q,
	})
	if err != nil {
		h.logger.Error("list words failed",
			infra.F("component", "web/api"),
			infra.ErrField(err),
		)
		writeError(w, http.StatusInternalServerError, codeInternal,
			"failed to list words")
		return
	}

	// Issue #4: the textbook attribution projection rides along so the
	// browse chips (教材·课次 / 待确认) need no second request. An
	// UNFILTERED browse asks for the whole projection directly — building
	// a whole-library IN-list for it would only re-parse N placeholders. A
	// repo failure still answers 200 (the words render), but it is NOT an
	// empty attribution: [] is reserved for a projection that VERIFIED the
	// card has none, while a failed (or unwired) read emits textbooks:null
	// so clients degrade eligibility to 资格待核实 instead of guessing
	// "no attribution" (PR #88 P1).
	//
	// Issue #89: the browse group and the 「尚未学到」 line read this same
	// textbook gate dictation uses. Scope failure degrades to supplementary
	// (no hold line) only when attribution is also empty; a word that still
	// carries assignments keeps an empty scope_reason so the page groups by
	// the lesson without inventing eligibility.
	ids := make([]int64, 0, len(cards))
	for _, c := range cards {
		ids = append(ids, c.ID)
	}
	attribution := make(map[int64][]database.TextbookAssignment, len(cards))
	attributionKnown := h.textbooks != nil
	scopes := map[int64]database.CardScope{}
	if h.textbooks != nil && len(cards) > 0 {
		var byCard map[int64][]database.TextbookAssignment
		var attrErr error
		if category == "" && unit == "" && q == "" {
			byCard, attrErr = h.textbooks.AllAssignments(r.Context())
		} else {
			byCard, attrErr = h.textbooks.AssignmentsByCardIDs(r.Context(), ids)
		}
		if attrErr == nil {
			attribution = byCard
		} else {
			attributionKnown = false
			h.logger.Warn("word attribution projection failed",
				infra.F("component", "web/api"),
				infra.ErrField(attrErr),
			)
		}
		var scopeErr error
		scopes, scopeErr = h.textbooks.BrowseScopes(r.Context(), ids)
		if scopeErr != nil {
			h.logger.Warn("word scope projection failed",
				infra.F("component", "web/api"),
				infra.ErrField(scopeErr),
			)
			scopes = map[int64]database.CardScope{}
		}
	}

	words := make([]wordJSON, 0, len(cards))
	for _, c := range cards {
		row := wordJSON{
			ID:            c.ID,
			Word:          c.Word,
			Meaning:       c.Meaning,
			Example:       c.Example,
			Unit:          c.Unit,
			Category:      c.Category,
			MeaningSource: c.MeaningSource,
			Participates:  c.Participates,
			InScope:       true,
			ScopeReason:   database.ScopeSupplementary,
		}
		if attributionKnown {
			row.Textbooks = []database.TextbookAssignment{}
			if rows, ok := attribution[c.ID]; ok {
				row.Textbooks = rows
			}
		}
		if sc, ok := scopes[c.ID]; ok {
			row.InScope = sc.InScope
			row.ScopeReason = sc.Reason
			row.GroupLesson = sc.GroupLesson
			row.GroupTextbookID = sc.GroupTextbookID
		} else if len(row.Textbooks) > 0 {
			// Assignments arrived and the verdict did not. Leave the reason
			// empty so the page groups by the lesson and does not claim the
			// word is supplementary.
			row.InScope = false
			row.ScopeReason = ""
		}
		words = append(words, row)
	}
	writeJSON(w, wordsResponse{Words: words})
}

// addWords handles POST /words with body {"category","unit","entries[]"}.
// Flow: strict decode → validation → pybridge.RunAdd (Python is the sole
// writer, ADR write-separation) → 200 {"added":N,"skipped":N}.
//
// Sentinel mapping (Shared Pattern 2): errors.Is against the four pybridge
// sentinels → 502 python_failed; anything else → 500 internal.
func (h *handler) addWords(w http.ResponseWriter, r *http.Request) {
	var req AddRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if msg := validateAddRequest(&req); msg != "" {
		writeError(w, http.StatusBadRequest, codeInvalidRequest, msg)
		return
	}

	opts := pybridge.AddOpts{Category: req.Category, Unit: req.Unit}
	for _, e := range req.Entries {
		opts.Entries = append(opts.Entries, pybridge.AddEntry{
			Word:          e.Word,
			Meaning:       e.Meaning,
			Example:       e.Example,
			MeaningSource: e.MeaningSource,
		})
	}

	result, err := h.exec.RunAdd(r.Context(), opts)
	if err != nil {
		switch {
		case errors.Is(err, pybridge.ErrPythonExit),
			errors.Is(err, pybridge.ErrPythonTimeout),
			errors.Is(err, pybridge.ErrPythonJSONParse),
			errors.Is(err, pybridge.ErrPythonEmpty):
			h.logger.Error("add words: python bridge failed",
				infra.F("component", "web/api"),
				infra.ErrField(err),
			)
			writeError(w, http.StatusBadGateway, codePythonFailed,
				"failed to save words, please retry")
		default:
			h.logger.Error("add words failed",
				infra.F("component", "web/api"),
				infra.ErrField(err),
			)
			writeError(w, http.StatusInternalServerError, codeInternal,
				"internal error")
		}
		return
	}

	writeJSON(w, addResponse{Added: result.Added, Skipped: result.Skipped})
}

// unitsResponse is the GET /units response envelope. Elements reuse
// database.UnitCount so the wire shape stays {"units":[{"unit":...,
// "count":...}]} by construction — one definition, no drift.
type unitsResponse struct {
	Units []database.UnitCount `json:"units"`
}

// listUnits handles GET /units?category= (optional; empty = all categories
// with the documented merge-by-name semantics from
// CardRepository.CountByUnit). A non-empty category must pass the enum
// check (D-06) or the request is rejected with 400 invalid_request (A5
// log-and-reject). The read path goes straight to
// CardRepository.CountByUnit — NO Python subprocess is spawned (D-12).
// Ordering is the repository's ORDER BY unit, passed through untouched.
func (h *handler) listUnits(w http.ResponseWriter, r *http.Request) {
	category := r.URL.Query().Get("category")

	if category != "" {
		if err := ValidateCategory(category); err != nil {
			writeError(w, http.StatusBadRequest, codeInvalidRequest,
				"category must be \"english\" or \"chinese\"")
			return
		}
	}

	units, err := h.cards.CountByUnit(r.Context(), category)
	if err != nil {
		h.logger.Error("list units failed",
			infra.F("component", "web/api"),
			infra.ErrField(err),
		)
		writeError(w, http.StatusInternalServerError, codeInternal,
			"failed to list units")
		return
	}

	writeJSON(w, unitsResponse{Units: units})
}

// lessonListItem is one row of the parent lessons projection (13.3-02) —
// summary fields ONLY. The type structurally cannot carry a word surface:
// the list endpoint serializes exactly this shape for EVERY row, and the
// detail endpoint reuses it for in-progress lessons (the leak guard's
// serialization-layer white-list — "not in the JSON" is the only "not
// leaked", RESEARCH Pattern 2).
type lessonListItem struct {
	PassageAuthor string `json:"passage_author,omitempty"`
	LessonID      string `json:"lesson_id"`
	Stage         string `json:"stage"`
	Kind          string `json:"kind"`
	SaveStatus    string `json:"save_status"`
	// Category is the lesson's subject ("english"/"chinese", Issue #10) —
	// the records view renders its subject badge from this field, never by
	// parsing the lesson id.
	Category string `json:"category"`
	// PoemTitle/PoemAuthor/Method ride ONLY whole-poem 背默 rows (Issue #12,
	// empty otherwise): the records view distinguishes 古诗作品、练习方式 and
	// the per-line judgment from the same row. PoemID is the join key the
	// projection filled the title through.
	PoemTitle    string `json:"poem_title,omitempty"`
	PoemAuthor   string `json:"poem_author,omitempty"`
	Method       string `json:"method,omitempty"`
	PassageTitle string `json:"passage_title,omitempty"`
	CreatedAt    string `json:"created_at"`
	// Passage fields ride ONLY passage 背默 rows (Issue #14): the work
	// identity (title/author joined through PassageID) plus the practiced
	// RANGE carried by the lesson row itself — the records view shows 作
	// 品、实际范围、结果、保存状态 from one row, and a paragraph attempt
	// never renders as a whole-passage one.
	PassageParagraphs []int `json:"passage_paragraphs,omitempty"`
	PassageID         int64 `json:"passage_id,omitempty"`
	WordCount         int   `json:"word_count"`
	Correct           int   `json:"correct"`
	Wrong             int   `json:"wrong"`
	Unconfirmed       int   `json:"unconfirmed"`
	PoemID            int64 `json:"poem_id,omitempty"`
	PassageWhole      bool  `json:"passage_whole,omitempty"`
	Completed         bool  `json:"completed"`
	Unchecked         bool  `json:"unchecked"`
}

// lessonsResponse is the GET /lessons response envelope.
type lessonsResponse struct {
	Lessons []lessonListItem `json:"lessons"`
}

// lessonWordJSON is one element of the detail response's words array — the
// meaning/example ground truth persisted with the lesson row.
type lessonWordJSON struct {
	Word    string `json:"word"`
	Meaning string `json:"meaning"`
	Example string `json:"example"`
}

// lessonResultJSON is one element of the detail response's results array.
// Lesson rows fill word/correct/manual/hints/outcome/ocr_text (manual is
// always present — nil would hide an OCR prefill); practice rows fill
// word/correct/hints/rating from the practice_logs record.
type lessonResultJSON struct {
	Manual  *bool  `json:"manual,omitempty"`
	Word    string `json:"word"`
	Rating  string `json:"rating,omitempty"`
	Outcome string `json:"outcome,omitempty"`
	OcrText string `json:"ocr_text,omitempty"`
	Hints   int    `json:"hints"`
	Correct bool   `json:"correct"`
}

// receiptJSON is one element of the detail response's save_receipts array —
// the receipt row verbatim (word-set columns are JSON string arrays by
// storage contract).
type receiptJSON struct {
	LessonID    string   `json:"lesson_id"`
	RequestID   string   `json:"request_id"`
	Kind        string   `json:"kind"`
	Status      string   `json:"status"`
	CreatedAt   string   `json:"created_at"`
	CompletedAt string   `json:"completed_at"`
	Requested   []string `json:"requested"`
	Updated     []string `json:"updated"`
	Missing     []string `json:"missing"`
}

// lessonDetailResponse is the completed-lesson GET /lessons/{id} body: the
// summary row plus the per-word detail. All three detail keys are ALWAYS
// present (empty arrays, never null/absent) so the parent view's emptiness
// is data-driven, not shape-driven.
type lessonDetailResponse struct {
	Words        []lessonWordJSON   `json:"words"`
	Results      []lessonResultJSON `json:"results"`
	SaveReceipts []receiptJSON      `json:"save_receipts"`
	lessonListItem
}

// lessonKind classifies a lesson id for the parent view's type badge:
// r0w- passage 背默 attempts (Issue #14), r0p- whole-poem 背默 attempts
// (Issue #12), r0x- extra-practice sessions, r0- classroom lessons,
// anything else unknown.
func lessonKind(lessonID string) string {
	switch {
	case strings.HasPrefix(lessonID, "r0w-"):
		return "passage"
	case strings.HasPrefix(lessonID, "r0p-"):
		return "poem"
	case strings.HasPrefix(lessonID, "r0x-"):
		return "practice"
	case strings.HasPrefix(lessonID, "r0-"):
		return "lesson"
	default:
		return "unknown"
	}
}

// applyPoemProjection fills a poem row's work identity (title/author/method)
// from the resolved poem map. A poem the store can no longer serve (or a
// nil store, degraded boot) keeps its id and kind — the row still renders,
// only without the title; the judgment and save status never depended on
// the join.
func applyPoemProjection(item *lessonListItem, poems map[int64]poem.Poem, poemID int64) {
	if poemID <= 0 {
		return
	}
	item.PoemID = poemID
	item.Method = "背默"
	if p, ok := poems[poemID]; ok {
		item.PoemTitle = p.Title
		item.PoemAuthor = p.Author
	}
}

// resolvePoemsForSummaries bulk-resolves the page's poem rows into one map
// (ONE store round trip, the poems.go projection seam). A nil poem store or
// a lookup error degrades to the empty map — assignment data is
// presentational here, exactly like the word attribution projection.
func (h *handler) resolvePoemsForSummaries(ctx context.Context, summaries []lesson.LessonSummary) map[int64]poem.Poem {
	ids := make([]int64, 0, 4)
	for _, s := range summaries {
		if s.PoemID > 0 {
			ids = append(ids, s.PoemID)
		}
	}
	if len(ids) == 0 || h.poems == nil {
		return map[int64]poem.Poem{}
	}
	rows, err := h.poems.PoemsByIDs(ctx, ids)
	if err != nil {
		h.logger.Warn("poem projection failed",
			infra.F("component", "web/api"),
			infra.ErrField(err),
		)
		return map[int64]poem.Poem{}
	}
	out := make(map[int64]poem.Poem, len(rows))
	for _, p := range rows {
		out[p.ID] = p
	}
	return out
}

// applyPassageProjection fills a passage row's work identity from the
// resolved passage map; the RANGE always comes from the lesson row's own
// summary (what the child actually practiced — the poem projection's same
// degraded-join discipline: a passage the store can no longer serve keeps
// its id and range, only the title reads empty).
func applyPassageProjection(item *lessonListItem, passages map[int64]passage.Passage, summary lesson.LessonSummary) {
	if summary.PassageID <= 0 {
		return
	}
	item.PassageID = summary.PassageID
	item.PassageWhole = summary.PassageWhole
	item.PassageParagraphs = summary.PassageParagraphs
	item.Method = "背默"
	if p, ok := passages[summary.PassageID]; ok {
		item.PassageTitle = p.Title
		item.PassageAuthor = p.Author
	}
}

// resolvePassagesForSummaries bulk-resolves the page's passage rows into
// one map (ONE store round trip). A nil passage store or a lookup error
// degrades to the empty map — the title is presentational, the range and
// the judgment never depended on the join.
func (h *handler) resolvePassagesForSummaries(ctx context.Context, summaries []lesson.LessonSummary) map[int64]passage.Passage {
	ids := make([]int64, 0, 4)
	for _, s := range summaries {
		if s.PassageID > 0 {
			ids = append(ids, s.PassageID)
		}
	}
	if len(ids) == 0 || h.passages == nil {
		return map[int64]passage.Passage{}
	}
	rows, err := h.passages.PassagesByIDs(ctx, ids)
	if err != nil {
		h.logger.Warn("passage projection failed",
			infra.F("component", "web/api"),
			infra.ErrField(err),
		)
		return map[int64]passage.Passage{}
	}
	out := make(map[int64]passage.Passage, len(rows))
	for _, p := range rows {
		out[p.ID] = p
	}
	return out
}

// newLessonListItem projects a store summary + the lesson's latest receipt
// into the wire row. save_status comes ONLY from the receipt table — a
// missing row reads "unknown" (保存状态未知), NEVER inferred from
// stage=submitted (submitted ≠ written to FSRS). For in-progress lessons the
// judgement counts output as 0 values: a lesson still in the room leaks
// progress through its counts.
func newLessonListItem(s lesson.LessonSummary, receipt lesson.Receipt, hasReceipt bool) lessonListItem {
	saveStatus := "unknown"
	if hasReceipt {
		saveStatus = receipt.Status
	}
	item := lessonListItem{
		LessonID:   s.LessonID,
		CreatedAt:  s.CreatedAt,
		WordCount:  s.WordCount,
		Stage:      s.Stage,
		Kind:       lessonKind(s.LessonID),
		Completed:  s.Completed,
		Unchecked:  s.Unchecked,
		SaveStatus: saveStatus,
		Category:   s.Category,
	}
	if s.Completed {
		item.Correct = s.Correct
		item.Wrong = s.Wrong
		item.Unconfirmed = s.Unconfirmed
	}
	return item
}

// validLessonID mirrors the infra.RTConfig LessonID whitelist口径 (T-13.3-06):
// at most 64 runes, no whitespace, no path separators. The mux pattern
// already structurally excludes "/" from a path value; "/" and "\" are both
// rejected here as defense-in-depth.
func validLessonID(id string) bool {
	if id == "" || utf8.RuneCountInString(id) > 64 {
		return false
	}
	if strings.IndexFunc(id, func(r rune) bool {
		return r == ' ' || r == '\t' || r == '\n' || r == '\r' || r == '\v' || r == '\f'
	}) >= 0 {
		return false
	}
	return !strings.ContainsAny(id, `/\`)
}

// listLessons handles GET /lessons (13.3-02, ROADMAP success criterion 2):
// the recent-lessons list for the parent view. Optional ?limit (default 20)
// is validated at the gate and HARD-CAPPED in the store (≤50 — a client
// value can never penetrate; the single-connection pool shares every query
// with the classroom write path, T-13.3-05). Rows are summary-only and
// include r0x- practice rows; save_status aggregates from the receipt
// table. Errors log and use the error envelope — never raw details.
func (h *handler) listLessons(w http.ResponseWriter, r *http.Request) {
	limit := 0
	if raw := r.URL.Query().Get("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed <= 0 {
			writeError(w, http.StatusBadRequest, codeInvalidRequest,
				"limit must be a positive integer")
			return
		}
		limit = parsed
	}

	summaries, err := h.lessons.ListLessons(r.Context(), limit)
	if err != nil {
		h.logger.Error("list lessons failed",
			infra.F("component", "web/api"),
			infra.ErrField(err),
		)
		writeError(w, http.StatusInternalServerError, codeInternal,
			"failed to list lessons")
		return
	}

	ids := make([]string, len(summaries))
	for i, s := range summaries {
		ids[i] = s.LessonID
	}
	latest, err := h.receipts.LatestByLessonIDs(r.Context(), ids)
	if err != nil {
		h.logger.Error("list lessons: receipt aggregation failed",
			infra.F("component", "web/api"),
			infra.ErrField(err),
		)
		// Receipt metadata must not make otherwise readable lessons unavailable.
		// Discard any partial aggregation: unknown is the honest fallback.
		latest = nil
	}

	items := make([]lessonListItem, 0, len(summaries))
	poems := h.resolvePoemsForSummaries(r.Context(), summaries)
	passages := h.resolvePassagesForSummaries(r.Context(), summaries)
	for _, s := range summaries {
		receipt, hasReceipt := latest[s.LessonID]
		item := newLessonListItem(s, receipt, hasReceipt)
		applyPoemProjection(&item, poems, s.PoemID)
		applyPassageProjection(&item, passages, s)
		items = append(items, item)
	}
	writeJSON(w, lessonsResponse{Lessons: items})
}

// getLesson handles GET /lessons/{id} (13.3-02): the per-lesson detail.
// The whitelist projection (RESEARCH Pattern 2 / Pitfall 2) is the leak
// guard: ONLY a completed lesson (lesson.LessonCompleted — the turn.go
// branch-table whitelist) returns the full per-word shape; EVERYTHING else —
// checking, mid-dictation, anything unexpected — serializes as the summary
// row, structurally without word surfaces. Practice lessons read their
// results from practice_logs; classroom lessons from verdicts+outcomes;
// save receipts are output verbatim (unknown stays unknown — never
// inferred from the stage).
func (h *handler) getLesson(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !validLessonID(id) {
		writeError(w, http.StatusBadRequest, codeInvalidRequest,
			"invalid lesson id")
		return
	}

	l, err := h.lessons.GetLesson(r.Context(), id)
	if err != nil {
		if errors.Is(err, lesson.ErrLessonNotFound) {
			writeError(w, http.StatusNotFound, codeNotFound,
				"lesson not found")
			return
		}
		h.logger.Error("get lesson failed",
			infra.F("component", "web/api"),
			infra.F("lesson_id", id),
			infra.ErrField(err),
		)
		writeError(w, http.StatusInternalServerError, codeInternal,
			"internal error")
		return
	}

	latest, err := h.receipts.LatestByLessonIDs(r.Context(), []string{id})
	if err != nil {
		h.logger.Error("get lesson: receipt lookup failed",
			infra.F("component", "web/api"),
			infra.F("lesson_id", id),
			infra.ErrField(err),
		)
		latest = nil
	}
	receipt, hasReceipt := latest[id]
	summary := lesson.SummarizeLesson(l)
	item := newLessonListItem(summary, receipt, hasReceipt)
	poems := h.resolvePoemsForSummaries(r.Context(), []lesson.LessonSummary{summary})
	applyPoemProjection(&item, poems, summary.PoemID)
	passages := h.resolvePassagesForSummaries(r.Context(), []lesson.LessonSummary{summary})
	applyPassageProjection(&item, passages, summary)

	if !lesson.LessonCompleted(l.Stage, l.CurrentIndex, len(l.Cards)) {
		// In-progress: the summary row IS the whole response — the word
		// surface keys do not exist in the serialization at all.
		writeJSON(w, item)
		return
	}

	detail := lessonDetailResponse{
		lessonListItem: item,
		Words:          make([]lessonWordJSON, 0, len(l.Cards)),
		Results:        []lessonResultJSON{},
		SaveReceipts:   []receiptJSON{},
	}
	for _, c := range l.Cards {
		detail.Words = append(detail.Words, lessonWordJSON{
			Word:    c.Word,
			Meaning: c.Meaning,
			Example: c.Example,
		})
	}

	if lessonKind(id) == "practice" {
		// Extra-practice results live in practice_logs (never in verdicts).
		records, recErr := h.practice.ListPracticeByLesson(r.Context(), id)
		if recErr != nil {
			h.logger.Error("get lesson: practice read failed",
				infra.F("component", "web/api"),
				infra.F("lesson_id", id),
				infra.ErrField(recErr),
			)
			writeError(w, http.StatusInternalServerError, codeInternal,
				"internal error")
			return
		}
		for _, rec := range records {
			correct := rec.Correct
			detail.Results = append(detail.Results, lessonResultJSON{
				Word:    rec.Word,
				Correct: correct,
				Hints:   rec.Hints,
				Rating:  rec.Rating,
			})
		}
	} else {
		for _, v := range l.Verdicts {
			res := lessonResultJSON{
				Correct: v.Correct,
				OcrText: v.OcrText,
			}
			manual := v.Manual
			res.Manual = &manual
			if v.WordIndex >= 0 && v.WordIndex < len(l.Cards) {
				res.Word = l.Cards[v.WordIndex].Word
			}
			if o, ok := l.Outcomes[strconv.Itoa(v.WordIndex)]; ok {
				res.Hints = o.Hints
				res.Outcome = o.Outcome
			}
			detail.Results = append(detail.Results, res)
		}
	}

	receiptRows, err := h.receipts.ListByLesson(r.Context(), id)
	if err != nil {
		h.logger.Error("get lesson: receipt list failed",
			infra.F("component", "web/api"),
			infra.F("lesson_id", id),
			infra.ErrField(err),
		)
		// Even a healthy latest row cannot verify an unreadable receipt history.
		detail.SaveStatus = "unknown"
		receiptRows = nil
	}
	for _, rc := range receiptRows {
		detail.SaveReceipts = append(detail.SaveReceipts, receiptJSON{
			LessonID:    rc.LessonID,
			RequestID:   rc.RequestID,
			Kind:        rc.Kind,
			Requested:   rc.Requested,
			Updated:     rc.Updated,
			Missing:     rc.Missing,
			Status:      rc.Status,
			CreatedAt:   rc.CreatedAt,
			CompletedAt: rc.CompletedAt,
		})
	}

	writeJSON(w, detail)
}
