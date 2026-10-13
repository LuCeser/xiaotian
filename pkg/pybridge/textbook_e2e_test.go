//go:build e2e

// Real-Python end-to-end tests for the Issue #4 textbook ENGINE contracts
// (the due-path scope predicate) and the Bridge textbook methods that ride
// them:
//
//   - TestE2E_TextbookScopeShapesDueSelection — the eligibility rule driven
//     through real vocab_manager.py processes: 已学完第 N 课 INCLUDES the
//     Nth lesson of the EXPLICIT list order (never dictionary order — the
//     fixture's list is 第3课,第1课,第2课 and progress 1 unlocks 第3课
//     first), 待确认 never counts as learned, include_in_scope overrides
//     progress, unassigned cards stay supplementary-eligible, the #2
//     participation switch composes by AND, and unassign returns a card to
//     supplementary eligibility. Every RunDue is a FRESH Python process
//     reading current progress — the consecutive phases prove 调整进度后
//     立即遵守 with zero caching between requests.
//   - TestE2E_TextbookMutationsPreserveFSRS — AC「资格和本次选题数量分开
//     表达，首次设置不加入能力测评」at the byte level: create/assign/
//     update-progress/unassign leave every fsrs_card byte-identical and
//     write no review_logs rows.
//   - TestE2E_DeleteWordsCleansAssignments — delete-words removes the
//     deleted cards' card_assignments rows in the SAME transaction (no
//     dangling references) while the textbook itself survives.
//
// Build tag and conventions follow vocab_mgmt_e2e_test.go (e2eBridge's
// shared venv from TestMain, per-test temp dirs, manual fakes/no testify).
package pybridge

import (
	"context"
	"database/sql"
	"errors"
	"sort"
	"strings"
	"testing"
)

// tbDueWords runs one REAL due fetch (a fresh Python process) and returns
// the selected words sorted — the observable of the Python-side scope
// predicate. Peek keeps cards due for the next call (no fetch lock).
func tbDueWords(t *testing.T, b *Bridge) []string {
	t.Helper()
	due, err := b.RunDue(context.Background(), DueOpts{Category: "english", Peek: true})
	if err != nil {
		t.Fatalf("RunDue: %v", err)
	}
	words := make([]string, 0, len(due.Cards))
	for _, c := range due.Cards {
		words = append(words, c.Word)
	}
	sort.Strings(words)
	return words
}

// tbExpectDue fails unless the real due selection equals want (sorted).
func tbExpectDue(t *testing.T, b *Bridge, want ...string) {
	t.Helper()
	sort.Strings(want)
	got := tbDueWords(t, b)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("due words = %v, want exactly %v", got, want)
	}
}

// TestE2E_TextbookScopeShapesDueSelection is the Issue #4 core acceptance
// on the due path: the textbook scope predicate composes with (and only
// with) the participation switch, honors the explicit lesson order, and
// reacts to progress changes across process boundaries immediately.
func TestE2E_TextbookScopeShapesDueSelection(t *testing.T) {
	ctx := context.Background()
	b, _, _ := e2eBridge(t, nil)

	// 8 words, insertion order ⇒ ids 1..8. Dictionary order of the lesson
	// names would rank 第1课 first; the EXPLICIT list puts 第3课 first —
	// progress 1 must unlock 第3课's words, proving list order rules.
	words := []string{"scopeC1", "scopeC2", "scopeA1", "scopeA2", "scopeB1", "scopePend1", "scopeSupp1", "scopeSupp2"}
	entries := make([]AddEntry, 0, len(words))
	for _, w := range words {
		entries = append(entries, AddEntry{Word: w, Meaning: "范围验证"})
	}
	add, err := b.RunAdd(ctx, AddOpts{Category: "english", Unit: "tb-scope-e2e", Entries: entries})
	if err != nil || add.Added != len(words) {
		t.Fatalf("RunAdd = %+v, %v; want %d added", add, err, len(words))
	}

	created, err := b.RunTextbookCreate(ctx, TextbookCreateOpts{
		Name:     "范围教材",
		Category: "english",
		Edition:  "人教版",
		Grade:    "三年级",
		Lessons:  []string{"第3课", "第1课", "第2课"},
	})
	if err != nil {
		t.Fatalf("RunTextbookCreate: %v", err)
	}
	bookID := created.ID

	assign := func(ids []int64, lesson string, include bool) {
		t.Helper()
		res, err := b.RunAssignWords(ctx, AssignWordsOpts{
			TextbookID: bookID, IDs: ids, Lesson: lesson, IncludeInScope: include,
		})
		if err != nil || res.Updated != len(ids) {
			t.Fatalf("assign %v → %q include=%v = %+v, %v; want all %d updated", ids, lesson, include, res, err, len(ids))
		}
	}
	assign([]int64{1, 2}, "第3课", false) // scopeC1/scopeC2
	assign([]int64{3, 4}, "第1课", false) // scopeA1/scopeA2
	assign([]int64{5}, "第2课", true)     // scopeB1, parent's explicit include
	assign([]int64{6}, "", false)        // scopePend1 待确认

	// Progress 0: only the include override and the two supplementary
	// words are due — 待确认 is NOT learned, unlearned lessons stay out.
	tbExpectDue(t, b, "scopeB1", "scopeSupp1", "scopeSupp2")

	// Progress 1 unlocks 第3课 (the LIST's first lesson — dictionary order
	// would have unlocked 第1课's scopeA1/scopeA2 instead).
	if _, err := b.RunTextbookUpdate(ctx, TextbookUpdateOpts{ID: bookID, LearnedLessons: intPtr(1)}); err != nil {
		t.Fatalf("set learned 1: %v", err)
	}
	tbExpectDue(t, b, "scopeB1", "scopeC1", "scopeC2", "scopeSupp1", "scopeSupp2")

	// Progress 2 adds 第1课 (list position 2). 已学完第 N 课包含第 N 课:
	// 第2课 (position 3) still stays out for the plain-assigned words.
	if _, err := b.RunTextbookUpdate(ctx, TextbookUpdateOpts{ID: bookID, LearnedLessons: intPtr(2)}); err != nil {
		t.Fatalf("set learned 2: %v", err)
	}
	tbExpectDue(t, b, "scopeA1", "scopeA2", "scopeB1", "scopeC1", "scopeC2", "scopeSupp1", "scopeSupp2")

	// Orthogonality with #2: participates=0 removes a word from selection
	// even though it is scope-eligible; re-enabling restores it. The scope
	// predicate never bypasses the participation switch.
	if _, err := b.RunSetParticipation(ctx, SetParticipationOpts{IDs: []int64{7}, Participates: false}); err != nil {
		t.Fatalf("disable scopeSupp1: %v", err)
	}
	tbExpectDue(t, b, "scopeA1", "scopeA2", "scopeB1", "scopeC1", "scopeC2", "scopeSupp2")
	if _, err := b.RunSetParticipation(ctx, SetParticipationOpts{IDs: []int64{7}, Participates: true}); err != nil {
		t.Fatalf("enable scopeSupp1: %v", err)
	}
	tbExpectDue(t, b, "scopeA1", "scopeA2", "scopeB1", "scopeC1", "scopeC2", "scopeSupp1", "scopeSupp2")

	// Unassign returns scopeC1/scopeC2 to supplementary eligibility (no
	// assignment at all) — the资格 is data, not a permanent state.
	if res, err := b.RunUnassignWords(ctx, UnassignWordsOpts{TextbookID: bookID, IDs: []int64{1, 2}}); err != nil || res.Removed != 2 {
		t.Fatalf("unassign = %+v, %v; want 2 removed", res, err)
	}
	tbExpectDue(t, b, "scopeA1", "scopeA2", "scopeB1", "scopeC1", "scopeC2", "scopeSupp1", "scopeSupp2")
}

// TestE2E_TextbookMutationsPreserveFSRS pins AC「资格和本次选题数量分开
// 表达，首次设置不加入能力测评」: every textbook mutation is pure
// administration — fsrs_card bytes and review_logs stay untouched.
func TestE2E_TextbookMutationsPreserveFSRS(t *testing.T) {
	ctx := context.Background()
	b, _, cfg := e2eBridge(t, nil)

	add, err := b.RunAdd(ctx, AddOpts{
		Category: "english", Unit: "tb-fsrs-e2e",
		Entries: []AddEntry{
			{Word: "tbFsrs1", Meaning: "一"},
			{Word: "tbFsrs2", Meaning: "二"},
		},
	})
	if err != nil || add.Added != 2 {
		t.Fatalf("RunAdd = %+v, %v", add, err)
	}
	// Real learning state on card 1: the bytes a broken mutation would clobber.
	if _, err := b.RunReview(ctx, ReviewOpts{Results: map[string]string{"tbFsrs1": "good"}, Category: "english"}); err != nil {
		t.Fatalf("seed review: %v", err)
	}
	before := e2eFsrsSnapshot(t, cfg.VocabDBPath)
	logsBefore := e2eReviewLogsCount(t, cfg.VocabDBPath)

	created, err := b.RunTextbookCreate(ctx, TextbookCreateOpts{
		Name: "进度教材", Category: "english", Lessons: []string{"一课", "二课"},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := b.RunAssignWords(ctx, AssignWordsOpts{
		TextbookID: created.ID, IDs: []int64{1, 2}, Lesson: "一课",
	}); err != nil {
		t.Fatalf("assign: %v", err)
	}
	if _, err := b.RunTextbookUpdate(ctx, TextbookUpdateOpts{
		ID: created.ID, LearnedLessons: intPtr(1), SetPrimary: true,
	}); err != nil {
		t.Fatalf("update: %v", err)
	}
	if _, err := b.RunUnassignWords(ctx, UnassignWordsOpts{TextbookID: created.ID, IDs: []int64{2}}); err != nil {
		t.Fatalf("unassign: %v", err)
	}

	if after := e2eFsrsSnapshot(t, cfg.VocabDBPath); after != before {
		t.Fatal("fsrs_card bytes changed across textbook mutations (首次设置必须不加入能力测评)")
	}
	if logs := e2eReviewLogsCount(t, cfg.VocabDBPath); logs != logsBefore {
		t.Fatalf("review_logs changed by textbook mutations: before %d after %d", logsBefore, logs)
	}
}

// TestE2E_DeleteWordsCleansAssignments proves the same-transaction cleanup:
// deleting assigned words leaves ZERO card_assignments rows for them (no
// dangling references) and the textbook row itself survives.
func TestE2E_DeleteWordsCleansAssignments(t *testing.T) {
	ctx := context.Background()
	b, _, cfg := e2eBridge(t, nil)

	add, err := b.RunAdd(ctx, AddOpts{
		Category: "english", Unit: "tb-del-e2e",
		Entries: []AddEntry{
			{Word: "tbDel1", Meaning: "一"},
			{Word: "tbDel2", Meaning: "二"},
			{Word: "tbKeep", Meaning: "留"},
		},
	})
	if err != nil || add.Added != 3 {
		t.Fatalf("RunAdd = %+v, %v", add, err)
	}
	created, err := b.RunTextbookCreate(ctx, TextbookCreateOpts{
		Name: "删除教材", Category: "english", Lessons: []string{"一课"},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := b.RunAssignWords(ctx, AssignWordsOpts{
		TextbookID: created.ID, IDs: []int64{1, 2, 3}, Lesson: "一课",
	}); err != nil {
		t.Fatalf("assign: %v", err)
	}

	if res, err := b.RunDeleteWords(ctx, DeleteWordsOpts{Words: []string{"tbDel1", "tbDel2"}, Category: "english"}); err != nil || res.Deleted != 2 {
		t.Fatalf("delete = %+v, %v; want 2 deleted", res, err)
	}

	db := e2eOpenDB(t, cfg.VocabDBPath)
	var dangling int
	if err := db.QueryRow(
		"SELECT COUNT(*) FROM card_assignments ca LEFT JOIN cards c ON c.id = ca.card_id WHERE c.id IS NULL",
	).Scan(&dangling); err != nil && !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("count dangling assignments: %v", err)
	}
	if dangling != 0 {
		t.Fatalf("dangling card_assignments after delete-words = %d, want 0", dangling)
	}
	var remaining int
	if err := db.QueryRow("SELECT COUNT(*) FROM card_assignments").Scan(&remaining); err != nil {
		t.Fatalf("count assignments: %v", err)
	}
	if remaining != 1 {
		t.Fatalf("surviving assignments = %d, want 1 (tbKeep's)", remaining)
	}
	var books int
	if err := db.QueryRow("SELECT COUNT(*) FROM textbooks WHERE id = ?", created.ID).Scan(&books); err != nil {
		t.Fatalf("count textbooks: %v", err)
	}
	if books != 1 {
		t.Fatalf("textbook row vanished after delete-words (want survived)")
	}
}
