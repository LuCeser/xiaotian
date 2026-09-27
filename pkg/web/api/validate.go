// Package api implements the parent-facing REST endpoints under /api/v1
// (Phase 9, WEB-01). cmd/service/main.go mounts the package's NewHandler
// ONCE at "/api/v1/" (http.StripPrefix) — all route logic lives here, not
// in main.go (single-mount contract, Plan 09-01).
//
// This file (validate.go) owns request validation for the api package:
// the category enum (D-06), JSON body decoding with strict unknown-field
// rejection (RESEARCH Pitfall 12) and a 64KiB body cap (T-09-04), and the
// entry/field length caps for the add path. Validation style mirrors
// pkg/infra/config.go FSRSConfig.Validate: fail-fast with a specific
// message naming the offending field.
package api

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/pkg/errors"
)

// maxBodyBytes bounds every JSON request body (T-09-04 DoS mitigation).
// 64KiB comfortably holds a 100-entry batch with max-length fields; the
// server's ReadTimeout bounds slow writes.
const maxBodyBytes = 64 << 10 // 64KiB

// Entry and field caps for the add path. The batch cap matches
// pybridge's reviewInlineEntryCap / addInlineEntryCap (100); the rune
// caps are generous for real vocabulary content while bounding storage
// and rendering cost. Runes, not bytes: Chinese words are ≤200
// CHARACTERS, not bytes.
const (
	maxEntries      = 100
	maxWordRunes    = 200
	maxMeaningRunes = 1000
	maxExampleRunes = 2000
	maxUnitRunes    = 200
)

// CategoryEnglish / CategoryChinese are the only valid category values
// (D-06). The column default is "english" (vocab_manager.py:109).
const (
	CategoryEnglish = "english"
	CategoryChinese = "chinese"
)

// errBadCategory is the internal marker for a category enum violation.
var errBadCategory = errors.New("category must be english or chinese")

// ValidateCategory reports whether v is a valid vocabulary category
// (D-06: exactly english or chinese; vocab_manager.py column default is
// "english" at vocab_manager.py:109).
func ValidateCategory(v string) error {
	if v != CategoryEnglish && v != CategoryChinese {
		return errBadCategory
	}
	return nil
}

// AddRequest is the decoded POST /words body shape.
type AddRequest struct {
	Category string `json:"category"`
	Unit     string `json:"unit"`
	Entries  []struct {
		Word    string `json:"word"`
		Meaning string `json:"meaning"`
		Example string `json:"example"`
	} `json:"entries"`
}

// decodeJSON decodes a JSON request body strictly: the body is capped at
// maxBodyBytes via http.MaxBytesReader (T-09-04) and unknown fields are
// rejected (RESEARCH Pitfall 12 — a typo like "catagory" must 400, not be
// silently ignored). On failure it writes the error envelope itself and
// returns false so the caller can simply return.
func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			writeError(w, http.StatusBadRequest, "invalid_request",
				fmt.Sprintf("request body exceeds %d bytes", maxBodyBytes))
			return false
		}
		writeError(w, http.StatusBadRequest, "invalid_request",
			"invalid JSON body: "+err.Error())
		return false
	}
	return true
}

// validateAddRequest enforces the add-path caps: category enum (D-06),
// batch size in [1, maxEntries], and per-field rune caps with the
// offending field named in the message (T-09-04). Returns "" on success
// or the error message on failure.
func validateAddRequest(req *AddRequest) string {
	if err := ValidateCategory(req.Category); err != nil {
		return "category must be \"english\" or \"chinese\""
	}
	if len(req.Entries) < 1 || len(req.Entries) > maxEntries {
		return fmt.Sprintf("entries must contain between 1 and %d items, got %d",
			maxEntries, len(req.Entries))
	}
	for i, e := range req.Entries {
		if n := len([]rune(e.Word)); n > maxWordRunes {
			return fmt.Sprintf("entries[%d].word exceeds %d characters", i, maxWordRunes)
		}
		if n := len([]rune(e.Meaning)); n > maxMeaningRunes {
			return fmt.Sprintf("entries[%d].meaning exceeds %d characters", i, maxMeaningRunes)
		}
		if n := len([]rune(e.Example)); n > maxExampleRunes {
			return fmt.Sprintf("entries[%d].example exceeds %d characters", i, maxExampleRunes)
		}
	}
	if n := len([]rune(req.Unit)); n > maxUnitRunes {
		return fmt.Sprintf("unit exceeds %d characters", maxUnitRunes)
	}
	return ""
}
