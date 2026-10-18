// practice_store_test.go — the Issue #12 whole-poem practice-record store
// tests: one row per attempt keyed by the poem (never per line), the strict
// shape gate, and the LatestByPoem mastery projection. Get/PoemsByIDs ride
// here too — the classroom fetch and the parent records projection seams.
package poem

import (
	"context"
	"errors"
	"testing"

	"github.com/xiaotian/home-tutor-agent/pkg/infra"
)

// openPracticeStore opens the shared test DB shape plus the practice store.
func openPracticeStore(t *testing.T) (*storeDB, *PracticeStore) {
	t.Helper()
	h := openTestDB(t, true) // with textbooks: Get exercises the scope join
	store, err := NewPracticeStore(h.db, infra.NewNoOpLogger())
	if err != nil {
		t.Fatalf("NewPracticeStore: %v", err)
	}
	return h, store
}

// seedPoem commits one poem and returns its stored id.
func seedPoem(t *testing.T, h *storeDB, title string, lines ...string) int64 {
	t.Helper()
	outcomes, err := h.store.Commit(context.Background(), CommitInput{
		Category: "chinese", Drafts: []Draft{{Title: title, Author: "测试", Lines: lines}},
	})
	if err != nil {
		t.Fatalf("commit %s: %v", title, err)
	}
	if len(outcomes) != 1 || outcomes[0].PoemID == 0 {
		t.Fatalf("commit %s outcome = %+v, want one id", title, outcomes)
	}
	return outcomes[0].PoemID
}

// TestRecordPracticeOneRowPerAttempt — the whole-poem scope: one submit
// writes ONE row (poem_id + lesson_id), never one row per line; attempts
// accumulate in order for the same poem.
func TestRecordPracticeOneRowPerAttempt(t *testing.T) {
	h, store := openPracticeStore(t)
	ctx := context.Background()
	poemID := seedPoem(t, h, "静夜思", "床前明月光", "疑是地上霜", "举头望明月", "低头思故乡")

	first := PracticeRecord{
		PoemID: poemID, LessonID: "r0p-20261018-080000",
		TotalLines: 4, CorrectLines: 3, Hints: 1, Mastered: false,
		ReviewedDay: "2026-10-18",
	}
	if err := store.RecordPractice(ctx, first); err != nil {
		t.Fatalf("record first: %v", err)
	}
	second := PracticeRecord{
		PoemID: poemID, LessonID: "r0p-20261019-090000",
		TotalLines: 4, CorrectLines: 4, Mastered: true,
		ReviewedDay: "2026-10-19",
	}
	if err := store.RecordPractice(ctx, second); err != nil {
		t.Fatalf("record second: %v", err)
	}

	rows, err := store.ListByPoem(ctx, poemID)
	if err != nil {
		t.Fatalf("list by poem: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("practice rows = %d, want 2 (one per attempt, not per line)", len(rows))
	}
	if rows[0].LessonID != first.LessonID || rows[0].CorrectLines != 3 || rows[0].Mastered {
		t.Errorf("first row = %+v, want the 3/4 non-mastered attempt", rows[0])
	}
	if rows[1].LessonID != second.LessonID || !rows[1].Mastered || rows[1].ReviewedDay != "2026-10-19" {
		t.Errorf("second row = %+v, want the mastered 4/4 attempt", rows[1])
	}

	latest, ok, err := store.LatestByPoem(ctx, poemID)
	if err != nil || !ok {
		t.Fatalf("latest by poem: %v ok=%v", err, ok)
	}
	if latest.LessonID != second.LessonID || !latest.Mastered {
		t.Errorf("latest = %+v, want the second attempt", latest)
	}
	p, err := h.store.Get(ctx, poemID)
	if err != nil {
		t.Fatalf("get scheduled poem: %v", err)
	}
	if !p.HasPractice || !p.LastMastered || p.NextReviewDay != "2026-10-26" {
		t.Errorf("schedule = practiced=%v mastered=%v next=%q, want mastered +7 days",
			p.HasPractice, p.LastMastered, p.NextReviewDay)
	}
}

// TestRecordPracticeShapeGate — a half-shaped mastery record rejects before
// anything is written (poem 0 / empty lesson / no Beijing day / impossible
// counts), and a legal zero-correct attempt still records.
func TestRecordPracticeShapeGate(t *testing.T) {
	h, store := openPracticeStore(t)
	ctx := context.Background()
	poemID := seedPoem(t, h, "春晓", "春眠不觉晓", "处处闻啼鸟")

	bad := []PracticeRecord{
		{PoemID: 0, LessonID: "l", TotalLines: 2, CorrectLines: 1, ReviewedDay: "2026-10-18"},
		{PoemID: poemID, LessonID: "", TotalLines: 2, CorrectLines: 1, ReviewedDay: "2026-10-18"},
		{PoemID: poemID, LessonID: "l", TotalLines: 2, CorrectLines: 1, ReviewedDay: ""},
		{PoemID: poemID, LessonID: "l", TotalLines: 0, CorrectLines: 0, ReviewedDay: "2026-10-18"},
		{PoemID: poemID, LessonID: "l", TotalLines: 2, CorrectLines: 3, ReviewedDay: "2026-10-18"},
		{PoemID: poemID, LessonID: "l", TotalLines: 2, CorrectLines: -1, ReviewedDay: "2026-10-18"},
		{PoemID: poemID, LessonID: "l", TotalLines: 2, CorrectLines: 1, ReviewedDay: "18-10-2026"},
	}
	for i, r := range bad {
		if err := store.RecordPractice(ctx, r); !errors.Is(err, ErrInvalidPoem) {
			t.Errorf("bad[%d] err = %v, want ErrInvalidPoem", i, err)
		}
	}
	// A blank-paper attempt (0 correct) is a legal, honest record.
	zero := PracticeRecord{PoemID: poemID, LessonID: "r0p-20261018-100000", TotalLines: 2, CorrectLines: 0, ReviewedDay: "2026-10-18"}
	if err := store.RecordPractice(ctx, zero); err != nil {
		t.Fatalf("record zero-correct: %v", err)
	}
	rows, err := store.ListByPoem(ctx, poemID)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(rows) != 1 || rows[0].CorrectLines != 0 || rows[0].Mastered {
		t.Fatalf("rows = %+v, want the single zero-correct attempt", rows)
	}
	p, err := h.store.Get(ctx, poemID)
	if err != nil {
		t.Fatalf("get scheduled poem: %v", err)
	}
	if !p.HasPractice || p.LastMastered || p.NextReviewDay != "2026-10-19" {
		t.Errorf("non-mastered schedule = %+v, want next-day retry", p)
	}
}

// TestLatestByPoemAbsent — an unpracticed poem answers absent with no
// error (never a fabricated mastery).
func TestLatestByPoemAbsent(t *testing.T) {
	h, store := openPracticeStore(t)
	poemID := seedPoem(t, h, "敕勒歌", "敕勒川，阴山下")
	_, ok, err := store.LatestByPoem(context.Background(), poemID)
	if err != nil {
		t.Fatalf("latest: %v", err)
	}
	if ok {
		t.Error("ok = true for an unpracticed poem, want false")
	}
}

// TestGetReturnsScopedPoem — Get returns the whole work with the Issue #4
// scope verdict the classroom branches on: supplementary (no attribution),
// then progress_pending once an attribution sits beyond the learned prefix,
// then learned after the textbook's progress moves.
func TestGetReturnsScopedPoem(t *testing.T) {
	h, _ := openPracticeStore(t)
	ctx := context.Background()
	poemID := seedPoem(t, h, "咏鹅", "鹅鹅鹅", "曲项向天歌")

	p, err := h.store.Get(ctx, poemID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if !p.InScope || p.ScopeReason != ScopeSupplementary {
		t.Fatalf("no-attribution poem scope = %s/%v, want supplementary/in", p.ScopeReason, p.InScope)
	}

	// Attribute to a textbook whose learned prefix does NOT include the
	// poem's lesson: out of scope until the progress moves (or the parent
	// includes it explicitly).
	if _, err = h.db.ExecContext(ctx,
		`INSERT INTO textbooks (name, category, lessons, learned_lessons) VALUES ('语文书', 'chinese', '["第1课","第2课"]', 1)`); err != nil {
		t.Fatalf("seed textbook: %v", err)
	}
	if aerr := h.store.Assign(ctx, poemID, 1, "第2课", false); aerr != nil {
		t.Fatalf("assign: %v", aerr)
	}
	p, err = h.store.Get(ctx, poemID)
	if err != nil {
		t.Fatalf("get after assign: %v", err)
	}
	if p.InScope || p.ScopeReason != ScopeProgressPending {
		t.Fatalf("unlearned scope = %s/%v, want progress_pending/out", p.ScopeReason, p.InScope)
	}

	if aerr := h.store.Assign(ctx, poemID, 1, "第1课", false); aerr != nil {
		t.Fatalf("assign learned lesson: %v", aerr)
	}
	p, err = h.store.Get(ctx, poemID)
	if err != nil {
		t.Fatalf("get after learned assign: %v", err)
	}
	if !p.InScope || p.ScopeReason != ScopeLearned {
		t.Fatalf("learned scope = %s/%v, want learned/in", p.ScopeReason, p.InScope)
	}
	if len(p.Lines) != 2 || p.Lines[0] != "鹅鹅鹅" {
		t.Errorf("lines = %v, want the ordered work", p.Lines)
	}

	// An unknown id is the honest not-found sentinel.
	if _, err = h.store.Get(ctx, 999); !errors.Is(err, ErrPoemNotFound) {
		t.Errorf("unknown id err = %v, want ErrPoemNotFound", err)
	}
}

// TestPoemsByIDsBulkProjection — one call serves a page of lesson rows:
// present ids resolve with title/author, absent ids simply miss, and an
// empty input is the zero-SQL fast path.
func TestPoemsByIDsBulkProjection(t *testing.T) {
	h, _ := openPracticeStore(t)
	ctx := context.Background()
	a := seedPoem(t, h, "江南", "江南可采莲")
	b := seedPoem(t, h, "画", "远看山有色", "近听水无声")

	poems, err := h.store.PoemsByIDs(ctx, []int64{a, b, 999})
	if err != nil {
		t.Fatalf("poems by ids: %v", err)
	}
	if len(poems) != 2 {
		t.Fatalf("poems = %d, want 2 (the miss is absent, not an error)", len(poems))
	}
	titles := map[int64]string{}
	for _, p := range poems {
		titles[p.ID] = p.Title
	}
	if titles[a] != "江南" || titles[b] != "画" {
		t.Errorf("titles = %v", titles)
	}

	if empty, err := h.store.PoemsByIDs(ctx, nil); err != nil || len(empty) != 0 {
		t.Errorf("empty input = %v/%v, want zero rows no error", empty, err)
	}
}
