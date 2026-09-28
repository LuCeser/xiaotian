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
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/xiaotian/home-tutor-agent/pkg/database"
	"github.com/xiaotian/home-tutor-agent/pkg/infra"
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
// (Python is the sole writer, ADR write-separation) and the
// CardRepository for the read path (D-12: zero Python spawns on reads).
type handler struct {
	exec   pybridge.Executor
	cards  database.CardRepository
	logger infra.Logger
}

// NewHandler builds the /api/v1 handler with its internal method-pattern
// ServeMux. Logger may be nil (NewNoOpLogger is substituted, D-47) so
// tests can construct the handler cheaply.
func NewHandler(exec pybridge.Executor, cards database.CardRepository, logger infra.Logger) http.Handler {
	if logger == nil {
		logger = infra.NewNoOpLogger()
	}
	h := &handler{exec: exec, cards: cards, logger: logger}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /words", h.listWords)
	mux.HandleFunc("POST /words", h.addWords)
	mux.HandleFunc("GET /units", h.listUnits)
	mux.HandleFunc("PUT /words/{id}", h.editWord)
	mux.HandleFunc("POST /units/rename", h.renameUnit)
	return mux
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

// wordJSON is one element of the GET /words response array. Field tags
// are snake_case per the shared response convention (health.go:18-26).
type wordJSON struct {
	ID       int64  `json:"id"`
	Word     string `json:"word"`
	Meaning  string `json:"meaning"`
	Example  string `json:"example"`
	Unit     string `json:"unit"`
	Category string `json:"category"`
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

	words := make([]wordJSON, 0, len(cards))
	for _, c := range cards {
		words = append(words, wordJSON{
			ID:       c.ID,
			Word:     c.Word,
			Meaning:  c.Meaning,
			Example:  c.Example,
			Unit:     c.Unit,
			Category: c.Category,
		})
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
			Word:    e.Word,
			Meaning: e.Meaning,
			Example: e.Example,
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
