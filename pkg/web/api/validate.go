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
	"strings"

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

// EditRequest is the decoded PUT /words/{id} body shape (Plan 09-05).
// Word is required; the other three fields are *string so "provided but
// empty" (clear the column) stays distinguishable from "absent" (keep the
// column) — the same None-vs-empty wire contract as pybridge.EditOpts and
// Plan 09-02's cmd_edit COALESCE semantics. There is intentionally NO
// category field: category is read-only per D-07, and decodeJSON's
// DisallowUnknownFields turns any attempt to send one into a 400.
type EditRequest struct {
	Word    string  `json:"word"`
	Meaning *string `json:"meaning"`
	Example *string `json:"example"`
	Unit    *string `json:"unit"`
}

// RenameRequest is the decoded POST /units/rename body shape (Plan 09-05).
// All three fields are required.
type RenameRequest struct {
	OldUnit  string `json:"old_unit"`
	NewUnit  string `json:"new_unit"`
	Category string `json:"category"`
}

// DeleteRequest is the decoded DELETE /words body shape (Plan 09-05).
// Exactly one target must be set — Words (explicit word list) XOR Unit
// (whole unit) — mirroring the script's mutually-exclusive argparse group
// and deleteWordsArgs' builder-level XOR rule. Category is always required
// (an empty --category would override the script's "english" default and
// silently match nothing — a misleading success for a destructive op).
type DeleteRequest struct {
	Words    []string `json:"words"`
	Unit     string   `json:"unit"`
	Category string   `json:"category"`
}

// validateDeleteRequest enforces the delete-path contract: category
// required and enum-checked, exactly one of Words (1..maxEntries items,
// each ≤ maxWordRunes) or Unit (≤ maxUnitRunes). Returns "" on success or
// the error message naming the offending condition.
func validateDeleteRequest(req *DeleteRequest) string {
	if err := ValidateCategory(req.Category); err != nil {
		return "category must be \"english\" or \"chinese\""
	}
	hasWords := len(req.Words) > 0
	hasUnit := req.Unit != ""
	if hasWords && hasUnit {
		return "words and unit are mutually exclusive: set exactly one target"
	}
	if !hasWords && !hasUnit {
		return "exactly one target is required: words (non-empty list) or unit"
	}
	if hasUnit {
		if n := len([]rune(req.Unit)); n > maxUnitRunes {
			return fmt.Sprintf("unit exceeds %d characters", maxUnitRunes)
		}
		return ""
	}
	if len(req.Words) > maxEntries {
		return fmt.Sprintf("words must contain at most %d items, got %d",
			maxEntries, len(req.Words))
	}
	for i, word := range req.Words {
		if word == "" {
			return fmt.Sprintf("words[%d] must not be empty", i)
		}
		// CR-01: deleteWordsArgs joins Words with "," and the script splits
		// it back on "," (vocab_manager.py cmd_delete_words), so a word
		// containing a comma would splinter into multiple unintended
		// deletions. Reject it here — mirroring the Python split contract —
		// so the words list always round-trips exactly as confirmed.
		if strings.Contains(word, ",") {
			return fmt.Sprintf("words[%d] must not contain commas", i)
		}
		if n := len([]rune(word)); n > maxWordRunes {
			return fmt.Sprintf("words[%d] exceeds %d characters", i, maxWordRunes)
		}
	}
	return ""
}

// validateEditRequest enforces the edit-path caps (Plan 09-05): word
// required and ≤ maxWordRunes; each PROVIDED optional field within its cap
// (an absent pointer is not checked — it means "keep the column"). A word
// containing a comma is rejected (CR-01): while edit does not itself use
// the comma-join encoding, allowing "a,b" into the library would set up a
// later delete to splinter it (see validateDeleteRequest). Returns "" on
// success or the error message naming the offending field.
func validateEditRequest(req *EditRequest) string {
	if req.Word == "" {
		return "word is required"
	}
	if strings.Contains(req.Word, ",") {
		return "word must not contain commas"
	}
	if n := len([]rune(req.Word)); n > maxWordRunes {
		return fmt.Sprintf("word exceeds %d characters", maxWordRunes)
	}
	if req.Meaning != nil {
		if n := len([]rune(*req.Meaning)); n > maxMeaningRunes {
			return fmt.Sprintf("meaning exceeds %d characters", maxMeaningRunes)
		}
	}
	if req.Example != nil {
		if n := len([]rune(*req.Example)); n > maxExampleRunes {
			return fmt.Sprintf("example exceeds %d characters", maxExampleRunes)
		}
	}
	if req.Unit != nil {
		if n := len([]rune(*req.Unit)); n > maxUnitRunes {
			return fmt.Sprintf("unit exceeds %d characters", maxUnitRunes)
		}
	}
	return ""
}

// validateRenameRequest enforces the rename-path contract: all three fields
// required (the script's argparse required=True), category enum-checked
// (D-06), and both unit names within the unit rune cap.
func validateRenameRequest(req *RenameRequest) string {
	if err := ValidateCategory(req.Category); err != nil {
		return "category must be \"english\" or \"chinese\""
	}
	if req.OldUnit == "" {
		return "old_unit is required"
	}
	if req.NewUnit == "" {
		return "new_unit is required"
	}
	if n := len([]rune(req.OldUnit)); n > maxUnitRunes {
		return fmt.Sprintf("old_unit exceeds %d characters", maxUnitRunes)
	}
	if n := len([]rune(req.NewUnit)); n > maxUnitRunes {
		return fmt.Sprintf("new_unit exceeds %d characters", maxUnitRunes)
	}
	return ""
}

// ValidateFSRSSettings enforces the locked D-13 two-knob ranges
// (Plan 09-05): desired_retention ∈ [0.7, 0.95] and maximum_interval an
// integer in [1, 36500]. Message shape copies infra FSRSConfig.Validate:
// fail-fast, first offender named with its value, so the parent UI can
// show exactly which knob was rejected.
func ValidateFSRSSettings(s FSRSSettings) error {
	if s.DesiredRetention < fsrsRetentionMin || s.DesiredRetention > fsrsRetentionMax {
		return fmt.Errorf("desired_retention must be in [%v, %v], got %v",
			fsrsRetentionMin, fsrsRetentionMax, s.DesiredRetention)
	}
	if s.MaximumInterval < fsrsIntervalMin || s.MaximumInterval > fsrsIntervalMax {
		return fmt.Errorf("maximum_interval must be an integer in [%d, %d], got %d",
			fsrsIntervalMin, fsrsIntervalMax, s.MaximumInterval)
	}
	return nil
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
// offending field named in the message (T-09-04). An empty word is
// rejected here (WR-01) — mirroring validateEditRequest — so it fails as
// 400 invalid_request instead of leaking to Python's exit-1 and surfacing
// as a misleading 502 python_failed. Returns "" on success or the error
// message on failure.
func validateAddRequest(req *AddRequest) string {
	if err := ValidateCategory(req.Category); err != nil {
		return "category must be \"english\" or \"chinese\""
	}
	if len(req.Entries) < 1 || len(req.Entries) > maxEntries {
		return fmt.Sprintf("entries must contain between 1 and %d items, got %d",
			maxEntries, len(req.Entries))
	}
	for i, e := range req.Entries {
		if e.Word == "" {
			return fmt.Sprintf("entries[%d].word must not be empty", i)
		}
		// CR-01: the delete path encodes word lists as comma-joined argv
		// (--words a,b) that Python splits back on ",", so a word
		// containing a comma could never be deleted as itself — deleting
		// it would remove OTHER words instead. Reject at the entry gate.
		if strings.Contains(e.Word, ",") {
			return fmt.Sprintf("entries[%d].word must not contain commas", i)
		}
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
