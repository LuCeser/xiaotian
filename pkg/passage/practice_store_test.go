// practice_store_test.go — the Issue #14 passage practice-record tests:
// content + range identity (第 2 段掌握不算第 3 段或全文掌握), per-range
// schedule replacement, the strict record gate, and the content view's
// per-scope practice projection — including a removed-and-re-added range
// re-linking its own history (scope id is traceability, never identity).
package passage

import (
	"context"
	"testing"

	"github.com/pkg/errors"
)

// practiceTestDB opens the shared store DB plus the practice store.
func practiceTestDB(t *testing.T) (*storeDB, *PracticeStore) {
	t.Helper()
	h := openTestDB(t, false)
	practice, err := NewPracticeStore(h.db, nil)
	if err != nil {
		t.Fatalf("NewPracticeStore: %v", err)
	}
	return h, practice
}

// commitFixture commits the shared five-paragraph passage and reports its
// id (the practice tests only need a real passage row for projections).
func commitFixture(t *testing.T, h *storeDB) int64 {
	t.Helper()
	outcomes, err := h.store.Commit(context.Background(), CommitInput{
		Category: "chinese",
		Drafts:   []Draft{autumnRain()},
	})
	if err != nil {
		t.Fatalf("commit fixture: %v", err)
	}
	if outcomes[0].Status != StatusAdded {
		t.Fatalf("commit fixture status = %s, want added", outcomes[0].Status)
	}
	return outcomes[0].PassageID
}

// record builds one minimal valid practice record.
func record(passageID int64, whole bool, paragraphs []int, correct, total int, mastered bool) PracticeRecord {
	return PracticeRecord{
		PassageID: passageID, LessonID: "r0w-test", ScopeID: 7,
		Whole: whole, Paragraphs: paragraphs,
		TotalParagraphs: total, CorrectParagraphs: correct,
		Mastered: mastered, ReviewedDay: "2026-10-19",
	}
}

// TestRecordPracticeRangeIdentityIsCanonical — [3,2] and [2,3] are ONE
// range: the row stores the canonical form and both lookups find it.
func TestRecordPracticeRangeIdentityIsCanonical(t *testing.T) {
	h, practice := practiceTestDB(t)
	ctx := context.Background()
	passageID := commitFixture(t, h)

	if err := practice.RecordPractice(ctx, record(passageID, false, []int{3, 2}, 2, 2, true)); err != nil {
		t.Fatalf("record: %v", err)
	}
	got, ok, err := practice.LatestByRange(ctx, passageID, false, []int{2, 3})
	if err != nil || !ok {
		t.Fatalf("latest by [2,3]: ok=%v err=%v", ok, err)
	}
	if len(got.Paragraphs) != 2 || got.Paragraphs[0] != 2 || got.Paragraphs[1] != 3 {
		t.Fatalf("latest paragraphs = %v, want canonical [2 3]", got.Paragraphs)
	}
	if !got.Mastered || got.CorrectParagraphs != 2 {
		t.Fatalf("latest verdict = %+v", got)
	}
}

// TestRangeMasteryNeverCrossesRanges — the core Issue #14 identity rule:
// mastering paragraphs 2 and 3 leaves paragraph 5, a DIFFERENT paragraph
// set and the whole passage unpracticed (第 2 段掌握不能算第 3 段或全文掌
// 握), and the schedule rows stay per range.
func TestRangeMasteryNeverCrossesRanges(t *testing.T) {
	h, practice := practiceTestDB(t)
	ctx := context.Background()
	passageID := commitFixture(t, h)

	twoThree := record(passageID, false, []int{2, 3}, 2, 2, true)
	twoThree.LessonID = "r0w-a"
	if err := practice.RecordPractice(ctx, twoThree); err != nil {
		t.Fatalf("record 2,3: %v", err)
	}

	for _, probe := range []struct {
		name       string
		paragraphs []int
		whole      bool
	}{
		{"paragraph 5", []int{5}, false},
		{"paragraph 3 alone", []int{3}, false},
		{"the whole passage", nil, true},
	} {
		rec, ok, err := practice.LatestByRange(ctx, passageID, probe.whole, probe.paragraphs)
		if err != nil {
			t.Fatalf("latest %s: %v", probe.name, err)
		}
		if ok {
			t.Fatalf("latest %s: found %+v, want absent", probe.name, rec)
		}
	}

	// The content projection agrees: only the 2,3 scope row reads practiced.
	if _, err := h.store.AddScope(ctx, passageID, Scope{Paragraphs: []int{2, 3}}); err != nil {
		t.Fatalf("add scope 2,3: %v", err)
	}
	if _, err := h.store.AddScope(ctx, passageID, Scope{Whole: true}); err != nil {
		t.Fatalf("add whole scope: %v", err)
	}
	rows, err := h.store.List(ctx, ListFilter{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	for _, scope := range rows[0].Scopes {
		if scope.Whole {
			if scope.HasPractice || scope.LastMastered || scope.NextReviewDay != "" {
				t.Fatalf("whole scope reads practiced: %+v", scope)
			}
			continue
		}
		if !scope.HasPractice || !scope.LastMastered || scope.NextReviewDay != "2026-10-26" {
			t.Fatalf("2,3 scope projection = %+v, want mastered with 7-day review", scope)
		}
	}
}

// TestScheduleReplacementPerRange — a later non-mastered attempt replaces
// ONLY its own range's schedule row (next day, unmastered); the sibling
// range's mastered schedule is untouched.
func TestScheduleReplacementPerRange(t *testing.T) {
	h, practice := practiceTestDB(t)
	ctx := context.Background()
	passageID := commitFixture(t, h)

	mastered := record(passageID, false, []int{2, 3}, 2, 2, true)
	mastered.LessonID = "r0w-a"
	if err := practice.RecordPractice(ctx, mastered); err != nil {
		t.Fatalf("record 2,3: %v", err)
	}
	missed := record(passageID, true, nil, 2, 5, false)
	missed.LessonID = "r0w-b"
	if err := practice.RecordPractice(ctx, missed); err != nil {
		t.Fatalf("record whole: %v", err)
	}

	para, ok, err := practice.LatestByRange(ctx, passageID, false, []int{2, 3})
	if err != nil || !ok {
		t.Fatalf("latest 2,3: ok=%v err=%v", ok, err)
	}
	if !para.Mastered {
		t.Fatalf("2,3 mastery rewritten by the whole attempt: %+v", para)
	}
	whole, ok, err := practice.LatestByRange(ctx, passageID, true, nil)
	if err != nil || !ok {
		t.Fatalf("latest whole: ok=%v err=%v", ok, err)
	}
	if whole.Mastered || whole.CorrectParagraphs != 2 || whole.TotalParagraphs != 5 {
		t.Fatalf("whole latest = %+v", whole)
	}

	// The schedule projection mirrors the split: 2,3 consolidates in 7
	// days, the whole passage retries tomorrow.
	rows, err := h.store.List(ctx, ListFilter{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	for _, scope := range rows[0].Scopes {
		if scope.Whole {
			if !scope.HasPractice || scope.LastMastered || scope.NextReviewDay != "2026-10-20" {
				t.Fatalf("whole schedule = %+v, want unmastered next-day retry", scope)
			}
		}
	}
}

// TestRecordPracticeGate — a half-shaped record rejects BEFORE anything is
// written (no log row, no schedule row).
func TestRecordPracticeGate(t *testing.T) {
	h, practice := practiceTestDB(t)
	ctx := context.Background()
	passageID := commitFixture(t, h)

	bad := []PracticeRecord{
		{PassageID: 0, LessonID: "r0w-x", ReviewedDay: "2026-10-19", TotalParagraphs: 2, CorrectParagraphs: 1, Paragraphs: []int{2}},
		{PassageID: passageID, LessonID: "", ReviewedDay: "2026-10-19", TotalParagraphs: 2, CorrectParagraphs: 1, Paragraphs: []int{2}},
		{PassageID: passageID, LessonID: "r0w-x", ReviewedDay: "19-10-2026", TotalParagraphs: 2, CorrectParagraphs: 1, Paragraphs: []int{2}},
		{PassageID: passageID, LessonID: "r0w-x", ReviewedDay: "2026-10-19", TotalParagraphs: 0, Paragraphs: []int{2}},
		{PassageID: passageID, LessonID: "r0w-x", ReviewedDay: "2026-10-19", TotalParagraphs: 2, CorrectParagraphs: 3, Paragraphs: []int{2}},
		{PassageID: passageID, LessonID: "r0w-x", ReviewedDay: "2026-10-19", TotalParagraphs: 2, CorrectParagraphs: 1},
		{PassageID: passageID, LessonID: "r0w-x", ReviewedDay: "2026-10-19", TotalParagraphs: 2, CorrectParagraphs: 2, Whole: true, Paragraphs: []int{1}},
		{PassageID: passageID, LessonID: "r0w-x", ReviewedDay: "2026-10-19", TotalParagraphs: 2, CorrectParagraphs: 2, Paragraphs: []int{0}},
	}
	for i, r := range bad {
		if err := practice.RecordPractice(ctx, r); !errors.Is(err, ErrInvalidPassage) {
			t.Fatalf("bad record %d: err = %v, want ErrInvalidPassage", i, err)
		}
	}
	var n int
	if err := h.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM passage_practice_logs").Scan(&n); err != nil || n != 0 {
		t.Fatalf("logs count = %d err=%v, want 0", n, err)
	}
	if err := h.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM passage_review_schedule").Scan(&n); err != nil || n != 0 {
		t.Fatalf("schedule count = %d err=%v, want 0", n, err)
	}
}

// TestScopeIDIsTraceabilityNotIdentity — removing a range and re-adding the
// same selection creates a NEW scope row id, yet the practice history keyed
// by (passage, whole, paragraphs) keeps counting and the content projection
// re-links (保留过去段落历史).
func TestScopeIDIsTraceabilityNotIdentity(t *testing.T) {
	h, practice := practiceTestDB(t)
	ctx := context.Background()
	passageID := commitFixture(t, h)

	first, err := h.store.AddScope(ctx, passageID, Scope{Paragraphs: []int{2, 3}})
	if err != nil {
		t.Fatalf("add scope: %v", err)
	}
	attempt := record(passageID, false, []int{2, 3}, 1, 2, false)
	attempt.ScopeID = first.Row.ID
	attempt.LessonID = "r0w-a"
	if err = practice.RecordPractice(ctx, attempt); err != nil {
		t.Fatalf("record: %v", err)
	}
	if err = h.store.RemoveScope(ctx, passageID, first.Row.ID); err != nil {
		t.Fatalf("remove scope: %v", err)
	}

	again, err := h.store.AddScope(ctx, passageID, Scope{Paragraphs: []int{3, 2}})
	if err != nil {
		t.Fatalf("re-add scope: %v", err)
	}
	if again.Status != StatusAdded || again.Row.ID == first.Row.ID {
		t.Fatalf("re-add outcome = %+v, want a fresh row", again)
	}

	history, err := practice.ListByRange(ctx, passageID, false, []int{2, 3})
	if err != nil {
		t.Fatalf("list by range: %v", err)
	}
	if len(history) != 1 || history[0].ScopeID != first.Row.ID {
		t.Fatalf("history = %+v, want the original row with the old scope id", history)
	}

	rows, err := h.store.List(ctx, ListFilter{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(rows[0].Scopes) != 1 || !rows[0].Scopes[0].HasPractice {
		t.Fatalf("re-added scope projection = %+v, want its history re-linked", rows[0].Scopes)
	}
}

// TestListByRangeOrdersOldestFirst — re-attempts append; the read keeps
// first-recorded-first order and LatestByRange picks the newest.
func TestListByRangeOrdersOldestFirst(t *testing.T) {
	h, practice := practiceTestDB(t)
	ctx := context.Background()
	passageID := commitFixture(t, h)

	for i, correct := range []int{1, 2, 2} {
		attempt := record(passageID, false, []int{2, 3}, correct, 2, correct == 2)
		attempt.LessonID = "r0w-" + string(rune('a'+i))
		if err := practice.RecordPractice(ctx, attempt); err != nil {
			t.Fatalf("record %d: %v", i, err)
		}
	}
	history, err := practice.ListByRange(ctx, passageID, false, []int{2, 3})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(history) != 3 {
		t.Fatalf("history length = %d, want 3", len(history))
	}
	if history[0].CorrectParagraphs != 1 || history[2].CorrectParagraphs != 2 {
		t.Fatalf("history order = %+v", history)
	}
	latest, ok, err := practice.LatestByRange(ctx, passageID, false, []int{2, 3})
	if err != nil || !ok || !latest.Mastered {
		t.Fatalf("latest = %+v ok=%v err=%v, want the mastered third attempt", latest, ok, err)
	}
}

// TestGetPassageForClassroom — Get resolves one passage with scopes and the
// Issue #4 verdict; an unknown id is ErrPassageNotFound.
func TestGetPassageForClassroom(t *testing.T) {
	h, _ := practiceTestDB(t)
	ctx := context.Background()
	passageID := commitFixture(t, h)
	if _, err := h.store.AddScope(ctx, passageID, Scope{Whole: true}); err != nil {
		t.Fatalf("add scope: %v", err)
	}

	got, err := h.store.Get(ctx, passageID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.ID != passageID || !got.InScope || len(got.Scopes) != 1 || !got.Scopes[0].Whole {
		t.Fatalf("get = %+v", got)
	}

	if _, err = h.store.Get(ctx, 999999); !errors.Is(err, ErrPassageNotFound) {
		t.Fatalf("unknown id err = %v, want ErrPassageNotFound", err)
	}
}
