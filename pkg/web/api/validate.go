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

// Meaning-source values (Issue #8 / ADR-0005): the only distinguishable
// provenance is a system-supplemented meaning; everything else — material
// original, library legacy, hand-typed — is "material". The Python column
// and the JS display rule use the same literals (cross-language, one
// enum, documented here).
const (
	MeaningSourceMaterial  = "material"
	MeaningSourceGenerated = "generated"
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
		// MeaningSource is the Issue #8 provenance flag: "generated" for a
		// system-supplemented meaning, "material"/absent for everything
		// else. Validated here; an empty meaning always stores "material"
		// regardless (Python-side normalize — an empty gloss never carries
		// a marker).
		MeaningSource string `json:"meaning_source"`
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
	Meaning *string `json:"meaning"`
	Example *string `json:"example"`
	Unit    *string `json:"unit"`
	Word    string  `json:"word"`
}

// ParticipationRequest is the narrow body for changing classroom
// eligibility (PUT /words/participation). A pointer distinguishes an
// explicit false from a missing key (an absent participates is a 400 —
// the caller must state the intent, never default it).
type ParticipationRequest struct {
	Participates *bool   `json:"participates"`
	IDs          []int64 `json:"ids"`
}

// validateIDBatch is the shared 1..500-unique-positive ids guard (the
// batch contract set-participation introduced; assign/unassign reuse it
// verbatim). field names the JSON array in the messages ("ids",
// "cardIds"). Returns "" on success.
func validateIDBatch(field string, ids []int64) string {
	if len(ids) < 1 || len(ids) > 500 {
		return fmt.Sprintf("%s must contain between 1 and 500 items", field)
	}
	seen := make(map[int64]struct{}, len(ids))
	for i, id := range ids {
		if id <= 0 {
			return fmt.Sprintf("%s[%d] must be positive", field, i)
		}
		if _, exists := seen[id]; exists {
			return fmt.Sprintf("%s[%d] is duplicated", field, i)
		}
		seen[id] = struct{}{}
	}
	return ""
}

// validateParticipationRequest mirrors the script-side guard exactly
// (validateIDBatch): rejecting here keeps a malformed batch out of the
// subprocess entirely, while the Python re-check stays authoritative for
// direct CLI callers (double validation, same shape as
// validateEditRequest vs cmd_edit).
func validateParticipationRequest(req *ParticipationRequest) string {
	if req.Participates == nil {
		return "participates is required"
	}
	return validateIDBatch("ids", req.IDs)
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
	Unit     string   `json:"unit"`
	Category string   `json:"category"`
	Words    []string `json:"words"`
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
// silently ignored). On failure it writes the error envelope itself and
// returns false so the caller can simply return.
//
//nolint:misspell // deliberate typo example: rejected (RESEARCH Pitfall 12 — a typo like "catagory" must 400, not be
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
		// Issue #8: the provenance enum is validated at the gate so a
		// malformed flag fails as 400 invalid_request, never leaking into
		// the stored column (Python re-normalizes the empty-meaning case).
		if e.MeaningSource != "" && e.MeaningSource != MeaningSourceMaterial &&
			e.MeaningSource != MeaningSourceGenerated {
			return fmt.Sprintf("entries[%d].meaning_source must be %q or %q",
				i, MeaningSourceMaterial, MeaningSourceGenerated)
		}
	}
	if n := len([]rune(req.Unit)); n > maxUnitRunes {
		return fmt.Sprintf("unit exceeds %d characters", maxUnitRunes)
	}
	return ""
}

// Textbook route caps (Issue #4): the textbook name and each descriptive
// metadata field cap at maxTextbookFieldRunes; a lesson list carries at
// most maxTextbookLessons names (a real semester is ~100; 200 leaves
// headroom). The batch cap reuses the participation shape (1..500 unique
// ids — one subprocess, one transaction).
const (
	maxTextbookFieldRunes  = 100
	maxTextbookLessons     = 200
	maxTextbookLessonRunes = 100
)

// TextbookCreateRequest is the decoded POST /textbooks body. Name and
// category are required; the five descriptive fields are optional
// free-form text (US-7: 不要求填满所有层级); lessons is the EXPLICIT
// teaching order and may be absent (filled in later, US-8).
type TextbookCreateRequest struct {
	Name     string   `json:"name"`
	Category string   `json:"category"`
	Edition  string   `json:"edition"`
	Stage    string   `json:"stage"`
	Grade    string   `json:"grade"`
	Volume   string   `json:"volume"`
	Module   string   `json:"module"`
	Lessons  []string `json:"lessons"`
}

// TextbookUpdateRequest is the decoded PUT /textbooks/{id} body. Every
// field is a pointer (or Lessons nil-vs-empty) so absent keeps the
// column; LearnedLessons is a pointer because 0 is a legal progress.
type TextbookUpdateRequest struct {
	Name           *string  `json:"name"`
	Edition        *string  `json:"edition"`
	Stage          *string  `json:"stage"`
	Grade          *string  `json:"grade"`
	Volume         *string  `json:"volume"`
	Module         *string  `json:"module"`
	LearnedLessons *int     `json:"learnedLessons"`
	Lessons        []string `json:"lessons"`
	AppendLessons  []string `json:"appendLessons"`
	SetPrimary     bool     `json:"setPrimary"`
}

// TextbookAssignRequest is the decoded POST /textbooks/{id}/assign and
// /textbooks/{id}/unassign body. Lesson is the empty-string-legal lesson
// name (待确认); IncludeInScope is the parent's explicit override.
type TextbookAssignRequest struct {
	Lesson         string  `json:"lesson"`
	CardIDs        []int64 `json:"cardIds"`
	IncludeInScope bool    `json:"includeInScope"`
}

// validateTextbookName checks one name-shaped field.
func validateTextbookName(field, value string) string {
	if value == "" {
		return field + " must not be empty"
	}
	if n := len([]rune(value)); n > maxTextbookFieldRunes {
		return fmt.Sprintf("%s exceeds %d characters", field, maxTextbookFieldRunes)
	}
	return ""
}

// validateTextbookLessons checks the explicit lesson list: unique,
// non-empty, individually capped (dictionary order is irrelevant — the
// slice IS the order).
func validateTextbookLessons(lessons []string) string {
	if len(lessons) > maxTextbookLessons {
		return fmt.Sprintf("lessons must contain at most %d items, got %d", maxTextbookLessons, len(lessons))
	}
	seen := make(map[string]struct{}, len(lessons))
	for i, name := range lessons {
		if name == "" {
			return fmt.Sprintf("lessons[%d] must not be empty", i)
		}
		if n := len([]rune(name)); n > maxTextbookLessonRunes {
			return fmt.Sprintf("lessons[%d] exceeds %d characters", i, maxTextbookLessonRunes)
		}
		if _, dup := seen[name]; dup {
			return fmt.Sprintf("lessons[%d] %q is duplicated", i, name)
		}
		seen[name] = struct{}{}
	}
	return ""
}

// validateTextbookCreateRequest validates POST /textbooks. Returns "" on
// success or the invalid_request message.
func validateTextbookCreateRequest(req *TextbookCreateRequest) string {
	if req.Category == "" {
		req.Category = CategoryEnglish
	}
	if err := ValidateCategory(req.Category); err != nil {
		return "category must be \"english\" or \"chinese\""
	}
	if msg := validateTextbookName("name", req.Name); msg != "" {
		return msg
	}
	for _, field := range []struct {
		name, value string
	}{
		{"edition", req.Edition}, {"stage", req.Stage}, {"grade", req.Grade},
		{"volume", req.Volume}, {"module", req.Module},
	} {
		if n := len([]rune(field.value)); n > maxTextbookFieldRunes {
			return fmt.Sprintf("%s exceeds %d characters", field.name, maxTextbookFieldRunes)
		}
	}
	return validateTextbookLessons(req.Lessons)
}

// validateTextbookUpdateRequest validates PUT /textbooks/{id}: the
// pointer/nil-keeps shape plus the same field rules, and at least one
// change must be requested (a fully-absent body is a caller bug).
func validateTextbookUpdateRequest(req *TextbookUpdateRequest) string {
	if req.Name != nil {
		if msg := validateTextbookName("name", *req.Name); msg != "" {
			return msg
		}
	}
	for _, field := range []struct {
		value *string
		name  string
	}{
		{value: req.Edition, name: "edition"},
		{value: req.Stage, name: "stage"},
		{value: req.Grade, name: "grade"},
		{value: req.Volume, name: "volume"},
		{value: req.Module, name: "module"},
	} {
		if field.value != nil && len([]rune(*field.value)) > maxTextbookFieldRunes {
			return fmt.Sprintf("%s exceeds %d characters", field.name, maxTextbookFieldRunes)
		}
	}
	if req.Lessons != nil && req.AppendLessons != nil {
		return "lessons and appendLessons are mutually exclusive"
	}
	if req.Lessons != nil {
		if msg := validateTextbookLessons(req.Lessons); msg != "" {
			return msg
		}
	}
	if req.AppendLessons != nil {
		if msg := validateTextbookLessons(req.AppendLessons); msg != "" {
			return msg
		}
	}
	if req.LearnedLessons != nil && (*req.LearnedLessons < 0 || *req.LearnedLessons > maxTextbookLessons) {
		return fmt.Sprintf("learnedLessons must be 0..%d", maxTextbookLessons)
	}
	changed := req.Name != nil || req.Edition != nil || req.Stage != nil ||
		req.Grade != nil || req.Volume != nil || req.Module != nil ||
		req.Lessons != nil || req.AppendLessons != nil || req.LearnedLessons != nil || req.SetPrimary
	if !changed {
		return "at least one change is required"
	}
	return ""
}

// validateTextbookAssignRequest applies the shared batch guard
// (validateIDBatch, "cardIds") plus the lesson name cap. The lesson's
// MEMBERSHIP in the textbook's list is checked by the handler against the
// repository row (it needs the textbook, not just the body).
func validateTextbookAssignRequest(req *TextbookAssignRequest) string {
	if msg := validateIDBatch("cardIds", req.CardIDs); msg != "" {
		return msg
	}
	if n := len([]rune(req.Lesson)); n > maxTextbookLessonRunes {
		return fmt.Sprintf("lesson exceeds %d characters", maxTextbookLessonRunes)
	}
	return ""
}

// Poem route caps (Issue #11): one commit carries at most maxPoems whole
// poems (a photo page holds a handful; the ocr pipeline caps its own
// recognition at the same bound), each field capped for storage/rendering
// cost. Runes, not bytes.
const (
	maxPoems           = 20
	maxPoemTitleRunes  = 200
	maxPoemAuthorRunes = 100
	maxPoemLineRunes   = 200
	maxPoemLines       = 64
)

// PoemCommitRequest is the decoded POST /poems body: the confirmed drafts
// plus the attribution target established in the preview (0 = 不指定教材).
// Category is optional and defaults to chinese — poems are a 语文 content
// type; the explicit field keeps the wire honest if more subjects ever
// gain poem-shaped works.
type PoemCommitRequest struct {
	Category string `json:"category"`
	Poems    []struct {
		Title  string   `json:"title"`
		Author string   `json:"author"`
		Lesson string   `json:"lesson"`
		Lines  []string `json:"lines"`
	} `json:"poems"`
	TextbookID int64 `json:"textbook_id"`
}

// validatePoemCommitRequest enforces the commit-path contract: category
// enum (default chinese), batch size, per-poem title/author/lesson caps
// and the lines rules (at least one non-empty line, ≤ maxPoemLines lines,
// each ≤ maxPoemLineRunes). Returns "" on success or the invalid_request
// message naming the offending field.
func validatePoemCommitRequest(req *PoemCommitRequest) string {
	if req.Category == "" {
		req.Category = CategoryChinese
	}
	if err := ValidateCategory(req.Category); err != nil {
		return "category must be \"english\" or \"chinese\""
	}
	if len(req.Poems) < 1 || len(req.Poems) > maxPoems {
		return fmt.Sprintf("poems must contain between 1 and %d items, got %d", maxPoems, len(req.Poems))
	}
	if req.TextbookID < 0 {
		return "textbook_id must not be negative"
	}
	for i, p := range req.Poems {
		if strings.TrimSpace(p.Title) == "" {
			return fmt.Sprintf("poems[%d].title must not be empty", i)
		}
		if n := len([]rune(p.Title)); n > maxPoemTitleRunes {
			return fmt.Sprintf("poems[%d].title exceeds %d characters", i, maxPoemTitleRunes)
		}
		if n := len([]rune(p.Author)); n > maxPoemAuthorRunes {
			return fmt.Sprintf("poems[%d].author exceeds %d characters", i, maxPoemAuthorRunes)
		}
		if n := len([]rune(p.Lesson)); n > maxTextbookLessonRunes {
			return fmt.Sprintf("poems[%d].lesson exceeds %d characters", i, maxTextbookLessonRunes)
		}
		if len(p.Lines) < 1 || len(p.Lines) > maxPoemLines {
			return fmt.Sprintf("poems[%d].lines must contain between 1 and %d items, got %d", i, maxPoemLines, len(p.Lines))
		}
		nonEmpty := 0
		for j, line := range p.Lines {
			if strings.TrimSpace(line) == "" {
				continue
			}
			nonEmpty++
			if n := len([]rune(line)); n > maxPoemLineRunes {
				return fmt.Sprintf("poems[%d].lines[%d] exceeds %d characters", i, j, maxPoemLineRunes)
			}
		}
		if nonEmpty == 0 {
			return fmt.Sprintf("poems[%d].lines must contain at least one non-empty line", i)
		}
	}
	return ""
}

// PoemAssignRequest is the decoded POST /poems/{id}/assign body — the
// content view's explicit attribution management (lesson + include
// refresh together).
type PoemAssignRequest struct {
	Lesson         string `json:"lesson"`
	TextbookID     int64  `json:"textbook_id"`
	IncludeInScope bool   `json:"includeInScope"`
}

// validatePoemAssignRequest enforces the management-path contract: a
// positive textbook id, the lesson cap, and the include flag stated
// explicitly (an absent bool defaults to false — for a management action
// the caller always sends the whole desired state, matching the word
// assign endpoint's shape).
func validatePoemAssignRequest(req *PoemAssignRequest) string {
	if req.TextbookID <= 0 {
		return "textbook_id must be positive"
	}
	if n := len([]rune(req.Lesson)); n > maxTextbookLessonRunes {
		return fmt.Sprintf("lesson exceeds %d characters", maxTextbookLessonRunes)
	}
	return ""
}

// Passage route caps (Issue #13): one commit carries at most maxPassages
// whole passages (a photo page holds one or two; the ocr pipeline caps
// its own recognition at the same bound), each field capped for
// storage/rendering cost. A 自然段 runs far longer than a verse line —
// maxPassageParagraphRunes is the poem line cap's prose counterpart.
// Runes, not bytes.
const (
	maxPassages              = 10
	maxPassageTitleRunes     = 200
	maxPassageAuthorRunes    = 100
	maxPassageParagraphRunes = 1000
	maxPassageParagraphs     = 60
)

// PassageScopeRequest is the decoded practice-range object (练习范围) —
// inside a commit draft AND the standalone scope route's body: whole
// (整篇) XOR an explicit 1-based paragraph set (指定自然段). An absent
// scope on a draft means "store the text only, the parent picks a range
// later"; on the scope route a body without whole and without paragraphs
// is rejected below.
type PassageScopeRequest struct {
	Paragraphs []int `json:"paragraphs"`
	Whole      bool  `json:"whole"`
}

// validatePassageScopeShape enforces the range's own contract, shared by
// the commit and scope routes: whole carries NO paragraphs (the two forms
// are mutually exclusive); a paragraph set names at least one index, at
// most maxPassageParagraphs, every index positive. paragraphCount > 0
// additionally bounds the indexes to a REAL paragraph of the passage the
// range targets (选择范围必须指向真实段落) — the store re-validates
// against its own stored text.
func validatePassageScopeShape(scope *PassageScopeRequest, paragraphCount int, field string) string {
	if scope.Whole {
		if len(scope.Paragraphs) != 0 {
			return field + " must not carry paragraphs when whole is true"
		}
		return ""
	}
	if len(scope.Paragraphs) == 0 {
		return field + " must be whole or name at least one paragraph"
	}
	if len(scope.Paragraphs) > maxPassageParagraphs {
		return fmt.Sprintf("%s must contain between 1 and %d paragraphs, got %d",
			field, maxPassageParagraphs, len(scope.Paragraphs))
	}
	for _, i := range scope.Paragraphs {
		if i < 1 {
			return field + " paragraph numbers are 1-based and must be positive"
		}
		if paragraphCount > 0 && i > paragraphCount {
			return fmt.Sprintf("%s names paragraph %d but the passage has %d paragraphs", field, i, paragraphCount)
		}
	}
	return ""
}

// PassageCommitRequest is the decoded POST /passages body: the confirmed
// drafts (paragraphs in 自然段 order, the preview's range choice riding
// each draft) plus the attribution target established in the preview
// (0 = 不指定教材). Category is optional and defaults to chinese — the
// acceptance focuses 语文课文, but the structured original is not
// subject-bound (an English whole sentence / short text rides "english").
type PassageCommitRequest struct {
	Category string `json:"category"`
	Passages []struct {
		Title      string               `json:"title"`
		Author     string               `json:"author"`
		Lesson     string               `json:"lesson"`
		Scope      *PassageScopeRequest `json:"scope"`
		Paragraphs []string             `json:"paragraphs"`
	} `json:"passages"`
	TextbookID int64 `json:"textbook_id"`
}

// validatePassageCommitRequest enforces the commit-path contract:
// category enum (default chinese), batch size, per-passage
// title/author/lesson caps and the paragraph rules (at least one
// non-empty paragraph — empty rows are editing noise the store drops —
// ≤ maxPassageParagraphs paragraphs, each ≤ maxPassageParagraphRunes),
// then each draft's range shape against THAT draft's own non-empty
// paragraph count. Returns "" on success or the invalid_request message
// naming the offending field.
func validatePassageCommitRequest(req *PassageCommitRequest) string {
	if req.Category == "" {
		req.Category = CategoryChinese
	}
	if err := ValidateCategory(req.Category); err != nil {
		return "category must be \"english\" or \"chinese\""
	}
	if len(req.Passages) < 1 || len(req.Passages) > maxPassages {
		return fmt.Sprintf("passages must contain between 1 and %d items, got %d", maxPassages, len(req.Passages))
	}
	if req.TextbookID < 0 {
		return "textbook_id must not be negative"
	}
	for i, p := range req.Passages {
		if strings.TrimSpace(p.Title) == "" {
			return fmt.Sprintf("passages[%d].title must not be empty", i)
		}
		if n := len([]rune(p.Title)); n > maxPassageTitleRunes {
			return fmt.Sprintf("passages[%d].title exceeds %d characters", i, maxPassageTitleRunes)
		}
		if n := len([]rune(p.Author)); n > maxPassageAuthorRunes {
			return fmt.Sprintf("passages[%d].author exceeds %d characters", i, maxPassageAuthorRunes)
		}
		if n := len([]rune(p.Lesson)); n > maxTextbookLessonRunes {
			return fmt.Sprintf("passages[%d].lesson exceeds %d characters", i, maxTextbookLessonRunes)
		}
		if len(p.Paragraphs) < 1 || len(p.Paragraphs) > maxPassageParagraphs {
			return fmt.Sprintf("passages[%d].paragraphs must contain between 1 and %d items, got %d",
				i, maxPassageParagraphs, len(p.Paragraphs))
		}
		nonEmpty := 0
		for j, paragraph := range p.Paragraphs {
			if strings.TrimSpace(paragraph) == "" {
				continue
			}
			nonEmpty++
			if n := len([]rune(paragraph)); n > maxPassageParagraphRunes {
				return fmt.Sprintf("passages[%d].paragraphs[%d] exceeds %d characters",
					i, j, maxPassageParagraphRunes)
			}
		}
		if nonEmpty == 0 {
			return fmt.Sprintf("passages[%d].paragraphs must contain at least one non-empty paragraph", i)
		}
		if p.Scope != nil {
			if msg := validatePassageScopeShape(p.Scope, nonEmpty,
				fmt.Sprintf("passages[%d].scope", i)); msg != "" {
				return msg
			}
		}
	}
	return ""
}

// PassageAssignRequest is the decoded POST /passages/{id}/assign body —
// the content view's explicit attribution management (lesson + include
// refresh together), the poem twin verbatim.
type PassageAssignRequest struct {
	Lesson         string `json:"lesson"`
	TextbookID     int64  `json:"textbook_id"`
	IncludeInScope bool   `json:"includeInScope"`
}

// validatePassageAssignRequest mirrors validatePoemAssignRequest: a
// positive textbook id and the lesson cap.
func validatePassageAssignRequest(req *PassageAssignRequest) string {
	if req.TextbookID <= 0 {
		return "textbook_id must be positive"
	}
	if n := len([]rune(req.Lesson)); n > maxTextbookLessonRunes {
		return fmt.Sprintf("lesson exceeds %d characters", maxTextbookLessonRunes)
	}
	return ""
}
