//go:build e2e

// Real-Python end-to-end tests for the Issue #6 re-import contracts
// (ADR-0003: 内容身份 = (word, category), 来源 = card_assignments, 释义差异
// = 显式确认): every scenario runs the REAL engine (vocab_manager.py in the
// shared venv, per-test temp DB) the way the parent photo-import commit
// drives it — add, edit (采用照片释义), assign — and proves the durable
// business results:
//
//   - TestE2E_ReimportIsIdempotent — AC-1: re-adding the same words with
//     the same meanings creates no second card, no new learning history,
//     no duplicate assignment row; repeated submission of the very same
//     batch keeps skipping. The card id (the anchor of FSRS and logs)
//     survives every re-import unchanged.
//   - TestE2E_MultiSourceAtLeastOneLearned — AC-2/US-32/33: one word with
//     sources in TWO textbooks stays selected while ANY source's lesson is
//     learned (the unlearned source never blocks); both source rows stay;
//     a word whose ONLY source is unlearned stays out; participates=0
//     (家长移出) removes the word despite a learned source (AC-4 组合规则).
//   - TestE2E_ConfirmedMeaningRidesSelection — AC-3/AC-7: a conflicting
//     re-add NEVER overwrites the stored meaning (未确认不覆盖); the
//     confirmed adoption (the ordinary edit call) swaps the meaning into
//     due selection while fsrs_card bytes and review_logs stay identical.
package pybridge

import (
	"context"
	"database/sql"
	"errors"
	"testing"
)

// reimportCardIDs reads the id → word map straight from the temp DB (the
// identity anchor the assertions pin).
func reimportCardIDs(t *testing.T, dbPath string) map[string]int64 {
	t.Helper()
	db := e2eOpenDB(t, dbPath)
	defer db.Close()
	rows, err := db.Query("SELECT id, word FROM cards")
	if err != nil {
		t.Fatalf("query cards: %v", err)
	}
	defer rows.Close()
	out := map[string]int64{}
	for rows.Next() {
		var id int64
		var word string
		if err := rows.Scan(&id, &word); err != nil {
			t.Fatalf("scan card: %v", err)
		}
		out[word] = id
	}
	return out
}

// reimportAssignmentCount counts card_assignments rows (whole DB — each
// test owns a fresh temp DB).
func reimportAssignmentCount(t *testing.T, dbPath string) int {
	t.Helper()
	db := e2eOpenDB(t, dbPath)
	defer db.Close()
	var n int
	if err := db.QueryRow("SELECT COUNT(*) FROM card_assignments").Scan(&n); err != nil {
		t.Fatalf("count assignments: %v", err)
	}
	return n
}

// TestE2E_ReimportIsIdempotent drives the AC-1 engine truth: the add path
// is a no-op for (word, category) identities that already exist, however
// many times the same page is re-submitted, and the assign upsert never
// duplicates a source row.
func TestE2E_ReimportIsIdempotent(t *testing.T) {
	ctx := context.Background()
	b, _, cfg := e2eBridge(t, nil)

	entries := []AddEntry{
		{Word: "reimp1", Meaning: "同一"},
		{Word: "reimp2", Meaning: "同样"},
	}
	add, err := b.RunAdd(ctx, AddOpts{Category: "english", Unit: "reimp-e2e", Entries: entries})
	if err != nil || add.Added != 2 {
		t.Fatalf("first RunAdd = %+v, %v; want 2 added", add, err)
	}
	// Real learning history on reimp1 — the bytes a duplicate would orphan.
	if _, err := b.RunReview(ctx, ReviewOpts{Results: map[string]string{"reimp1": "good"}, Category: "english"}); err != nil {
		t.Fatalf("seed review: %v", err)
	}
	created, err := b.RunTextbookCreate(ctx, TextbookCreateOpts{
		Name: "重传教材", Category: "english", Lessons: []string{"第1课", "第2课"},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := b.RunAssignWords(ctx, AssignWordsOpts{
		TextbookID: created.ID, IDs: []int64{1, 2}, Lesson: "第1课",
	}); err != nil {
		t.Fatalf("assign: %v", err)
	}

	idsBefore := reimportCardIDs(t, cfg.VocabDBPath)
	fsrsBefore := e2eFsrsSnapshot(t, cfg.VocabDBPath)
	logsBefore := e2eReviewLogsCount(t, cfg.VocabDBPath)

	// The SAME page re-submitted twice in a row (same words, same meanings):
	// every row skips, nothing is written a second time.
	for round := 0; round < 2; round++ {
		readd, err := b.RunAdd(ctx, AddOpts{Category: "english", Unit: "reimp-e2e", Entries: entries})
		if err != nil {
			t.Fatalf("re-add round %d: %v", round, err)
		}
		if readd.Added != 0 || readd.Skipped != 2 {
			t.Fatalf("re-add round %d = added %d skipped %d; want 0/2 (重复提交不重复写入)", round, readd.Added, readd.Skipped)
		}
		if _, err := b.RunAssignWords(ctx, AssignWordsOpts{
			TextbookID: created.ID, IDs: []int64{1, 2}, Lesson: "第1课",
		}); err != nil {
			t.Fatalf("re-assign round %d: %v", round, err)
		}
	}

	after := reimportCardIDs(t, cfg.VocabDBPath)
	if len(after) != len(idsBefore) {
		t.Fatalf("cards after re-import = %d, want %d (no duplicate content)", len(after), len(idsBefore))
	}
	for word, id := range idsBefore {
		if got := after[word]; got != id {
			t.Fatalf("card %q id changed by re-import: %d → %d (identity must be stable)", word, id, got)
		}
	}
	if after := e2eFsrsSnapshot(t, cfg.VocabDBPath); after != fsrsBefore {
		t.Fatal("fsrs_card bytes changed across re-import (重复导入不创建新的学习历史)")
	}
	if logs := e2eReviewLogsCount(t, cfg.VocabDBPath); logs != logsBefore {
		t.Fatalf("review_logs changed by re-import: %d → %d", logsBefore, logs)
	}
	if n := reimportAssignmentCount(t, cfg.VocabDBPath); n != 2 {
		t.Fatalf("assignment rows after re-import = %d, want 2 (one source per card)", n)
	}
}

// TestE2E_MultiSourceAtLeastOneLearned proves US-32/33 and the AC-4
// combination rule on the real scope predicate: sources compose by OR
// (any learned source admits the word), the participation switch still
// gates everything, and no source row is lost.
func TestE2E_MultiSourceAtLeastOneLearned(t *testing.T) {
	ctx := context.Background()
	b, _, cfg := e2eBridge(t, nil)

	// multiBoth: two sources (book A learned lesson, book B unlearned).
	// multiBOnly: one source (book B unlearned lesson) — the control.
	add, err := b.RunAdd(ctx, AddOpts{
		Category: "english", Unit: "reimp-multi-e2e",
		Entries: []AddEntry{{Word: "multiBoth", Meaning: "双源"}, {Word: "multiBOnly", Meaning: "单源"}},
	})
	if err != nil || add.Added != 2 {
		t.Fatalf("RunAdd = %+v, %v", add, err)
	}
	bookA, err := b.RunTextbookCreate(ctx, TextbookCreateOpts{
		Name: "多源教材A", Category: "english", Lessons: []string{"A1课", "A2课"},
	})
	if err != nil {
		t.Fatalf("create A: %v", err)
	}
	bookB, err := b.RunTextbookCreate(ctx, TextbookCreateOpts{
		Name: "多源教材B", Category: "english", Lessons: []string{"B1课", "B2课"},
	})
	if err != nil {
		t.Fatalf("create B: %v", err)
	}
	if _, err := b.RunAssignWords(ctx, AssignWordsOpts{
		TextbookID: bookA.ID, IDs: []int64{1}, Lesson: "A1课",
	}); err != nil {
		t.Fatalf("assign A: %v", err)
	}
	if _, err := b.RunAssignWords(ctx, AssignWordsOpts{
		TextbookID: bookB.ID, IDs: []int64{1, 2}, Lesson: "B2课",
	}); err != nil {
		t.Fatalf("assign B: %v", err)
	}

	// Nobody has progress: both words out (B2课 unlearned everywhere).
	tbExpectDue(t, b /* nothing */)

	// Book A learns A1课: multiBoth becomes eligible THROUGH book A while
	// its book B source (B2课) is still unlearned — the unlearned source
	// must not block. multiBOnly stays out.
	if _, err := b.RunTextbookUpdate(ctx, TextbookUpdateOpts{ID: bookA.ID, LearnedLessons: intPtr(1)}); err != nil {
		t.Fatalf("progress A: %v", err)
	}
	tbExpectDue(t, b, "multiBoth")

	// Both source rows still exist — US-32's 保留多个来源.
	db := e2eOpenDB(t, cfg.VocabDBPath)
	var sources int
	if err := db.QueryRow(
		"SELECT COUNT(*) FROM card_assignments WHERE card_id = 1",
	).Scan(&sources); err != nil && !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("count sources: %v", err)
	}
	db.Close()
	if sources != 2 {
		t.Fatalf("multiBoth sources = %d, want 2 (one per textbook)", sources)
	}

	// AC-4 combination: the parent's explicit 移出 (participates=0) holds
	// against the learned source; re-enabling restores eligibility with
	// the SAME sources intact (learning history untouched by the toggles).
	if _, err := b.RunSetParticipation(ctx, SetParticipationOpts{IDs: []int64{1}, Participates: false}); err != nil {
		t.Fatalf("移出 multiBoth: %v", err)
	}
	tbExpectDue(t, b)
	if _, err := b.RunSetParticipation(ctx, SetParticipationOpts{IDs: []int64{1}, Participates: true}); err != nil {
		t.Fatalf("re-enable multiBoth: %v", err)
	}
	tbExpectDue(t, b, "multiBoth")
}

// TestE2E_ConfirmedMeaningRidesSelection pins AC-3/AC-7 at the engine
// level: an unresolved conflicting re-add never overwrites the stored
// meaning; the parent-confirmed adoption (the ordinary edit call the
// photo commit issues) swaps the meaning that real due selection then
// reads — while the FSRS bytes and review logs of the word stay identical.
// (History preservation under re-import WITH reviews is
// TestE2E_ReimportIsIdempotent's half; a fresh card stays due, which is
// what lets selection observe the meaning directly.)
func TestE2E_ConfirmedMeaningRidesSelection(t *testing.T) {
	ctx := context.Background()
	b, _, cfg := e2eBridge(t, nil)

	add, err := b.RunAdd(ctx, AddOpts{
		Category: "english", Unit: "reimp-meaning-e2e",
		Entries: []AddEntry{{Word: "meaningWord", Meaning: "词库原释义"}},
	})
	if err != nil || add.Added != 1 {
		t.Fatalf("RunAdd = %+v, %v", add, err)
	}
	fsrsBefore := e2eFsrsSnapshot(t, cfg.VocabDBPath)
	logsBefore := e2eReviewLogsCount(t, cfg.VocabDBPath)

	// The conflicting re-add (same word, DIFFERENT meaning) skips — the
	// stored meaning must stay the original (未确认差异不静默覆盖).
	if readd, err := b.RunAdd(ctx, AddOpts{
		Category: "english", Unit: "reimp-meaning-e2e",
		Entries: []AddEntry{{Word: "meaningWord", Meaning: "照片新释义"}},
	}); err != nil || readd.Added != 0 || readd.Skipped != 1 {
		t.Fatalf("conflicting re-add = %+v, %v; want 0 added 1 skipped", readd, err)
	}
	due, err := b.RunDue(ctx, DueOpts{Category: "english", Peek: true})
	if err != nil {
		t.Fatalf("due after conflict: %v", err)
	}
	if len(due.Cards) != 1 || due.Cards[0].Meaning != "词库原释义" {
		t.Fatalf("due meaning after unresolved conflict = %+v; want the original", due.Cards)
	}

	// The confirmed adoption: the edit call the photo commit rides. The
	// meaning swaps; FSRS bytes and logs stay identical (D-07).
	if _, err := b.RunEdit(ctx, EditOpts{ID: 1, Meaning: strPtr("照片新释义")}); err != nil {
		t.Fatalf("adopt edit: %v", err)
	}
	due, err = b.RunDue(ctx, DueOpts{Category: "english", Peek: true})
	if err != nil {
		t.Fatalf("due after adoption: %v", err)
	}
	if len(due.Cards) != 1 || due.Cards[0].Meaning != "照片新释义" {
		t.Fatalf("due meaning after adoption = %+v; want the confirmed 照片新释义 (课堂使用确认后的释义)", due.Cards)
	}
	if after := e2eFsrsSnapshot(t, cfg.VocabDBPath); after != fsrsBefore {
		t.Fatal("fsrs_card bytes changed across adoption edit (学习历史必须保留)")
	}
	if logs := e2eReviewLogsCount(t, cfg.VocabDBPath); logs != logsBefore {
		t.Fatalf("review_logs changed by adoption edit: %d → %d", logsBefore, logs)
	}
}
