//go:build e2e

// Real-Python end-to-end tests for the Issue #80 `designate` command
// (指定默写) driven through Bridge.RunDesignate:
//
//   - TestE2E_DesignateIgnoresProgressDueAndUnit — the selection rule at
//     full scale: 20 words whose lesson sits beyond learned_lessons, only 5
//     of them FSRS-due, come back as ALL 20 (progress prefix, due dates and
//     cards.unit are not inputs); participates=0 words stay out; progress,
//     include_in_scope, fsrs_card bytes and review_logs are untouched; the
//     winners carry the occupancy lock.
//   - TestE2E_DesignateRejectsOverLimit — 101 eligible words answer
//     too_many with the real total and lock NOTHING.
//   - TestE2E_DesignateOccupiedKeepsLocksUntouched — one unexpired lock in
//     the target rejects the whole batch (occupied) and leaves every lock
//     byte (set or unset) exactly as it was.
//   - TestE2E_DesignateConcurrentExactlyOneWins — two parallel designates
//     on the same target: exactly one ok with all ids, the other occupied.
//     This is the pair the plan's red-check breaks by removing the lock
//     check or the BEGIN IMMEDIATE transaction.
//   - TestE2E_DesignateValidatesTarget — unknown textbook id / lesson
//     outside the list are stdout diagnostics + exit 1 (the 404/400
//     mapping chain), never typed results.
//
// Conventions follow textbook_e2e_test.go (e2eBridge's shared venv, manual
// helpers, no testify).
package pybridge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

// dgAddWords inserts count words named prefix+1..prefix+count in one or two
// RunAdd batches (the bridge caps a batch at 100 entries) and returns the
// expected card-id span [first, last] — ids follow insertion order.
func dgAddWords(t *testing.T, b *Bridge, prefix string, count int) []int64 {
	t.Helper()
	entries := make([]AddEntry, 0, count)
	for i := 1; i <= count; i++ {
		entries = append(entries, AddEntry{Word: fmt.Sprintf("%s%d", prefix, i), Meaning: "指定"})
	}
	for start := 0; start < len(entries); start += 100 {
		end := min(start+100, len(entries))
		add, err := b.RunAdd(context.Background(), AddOpts{
			Category: "english", Unit: "dg-无关unit", Entries: entries[start:end],
		})
		if err != nil || add.Added != end-start {
			t.Fatalf("RunAdd[%d:%d] = %+v, %v; want %d added", start, end, add, err, end-start)
		}
	}
	ids := make([]int64, 0, count)
	for i := 1; i <= count; i++ {
		ids = append(ids, int64(i))
	}
	return ids
}

// dgSetupBook creates one textbook whose lesson list puts targetLesson at
// position 2, then sets learned_lessons=1 so targetLesson sits beyond the
// learned prefix (第 N 课 includes lesson N only).
func dgSetupBook(t *testing.T, b *Bridge, name, targetLesson string) int64 {
	t.Helper()
	created, err := b.RunTextbookCreate(context.Background(), TextbookCreateOpts{
		Name: name, Category: "english", Lessons: []string{"第1课", targetLesson},
	})
	if err != nil {
		t.Fatalf("create textbook: %v", err)
	}
	if _, err := b.RunTextbookUpdate(context.Background(), TextbookUpdateOpts{
		ID: created.ID, LearnedLessons: intPtr(1),
	}); err != nil {
		t.Fatalf("set learned 1: %v", err)
	}
	return created.ID
}

// dgAssignLesson assigns ids to the book's target lesson with
// include_in_scope=0 (the designation must not need the parent override).
func dgAssignLesson(t *testing.T, b *Bridge, bookID int64, lesson string, ids []int64) {
	t.Helper()
	res, err := b.RunAssignWords(context.Background(), AssignWordsOpts{
		TextbookID: bookID, IDs: ids, Lesson: lesson, IncludeInScope: false,
	})
	if err != nil || res.Updated != len(ids) {
		t.Fatalf("assign %d ids = %+v, %v; want all updated", len(ids), res, err)
	}
}

// dgCardIDs extracts the selected card ids in the result's own order.
func dgCardIDs(res DesignateResult) []int64 {
	ids := make([]int64, 0, len(res.Cards))
	for _, c := range res.Cards {
		ids = append(ids, c.ID)
	}
	return ids
}

// dgExpectIDs fails unless got equals want element-wise (order included).
func dgExpectIDs(t *testing.T, got, want []int64) {
	t.Helper()
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("ids = %v, want exactly %v", got, want)
	}
}

// dgLocks reads every card's fetch_locked_until straight from SQLite,
// keyed by card id ("" = unlocked).
func dgLocks(t *testing.T, dbPath string) map[int64]string {
	t.Helper()
	db := e2eOpenDB(t, dbPath)
	rows, err := db.Query("SELECT id, fetch_locked_until FROM cards ORDER BY id")
	if err != nil {
		t.Fatalf("read locks: %v", err)
	}
	defer rows.Close()
	locks := make(map[int64]string)
	for rows.Next() {
		var id int64
		var lock *string
		if err := rows.Scan(&id, &lock); err != nil {
			t.Fatalf("scan lock: %v", err)
		}
		if lock != nil {
			locks[id] = *lock
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("lock rows: %v", err)
	}
	return locks
}

// dgIncludeFlags reads the include_in_scope column for one textbook's
// assignments, keyed by card id — the byte-level baseline for 「导入/指定
// 不改加入标记」.
func dgIncludeFlags(t *testing.T, dbPath string, bookID int64) map[int64]int {
	t.Helper()
	db := e2eOpenDB(t, dbPath)
	rows, err := db.Query(
		"SELECT card_id, include_in_scope FROM card_assignments WHERE textbook_id = ?", bookID)
	if err != nil {
		t.Fatalf("read includes: %v", err)
	}
	defer rows.Close()
	flags := make(map[int64]int)
	for rows.Next() {
		var id int64
		var flag int
		if err := rows.Scan(&id, &flag); err != nil {
			t.Fatalf("scan include: %v", err)
		}
		flags[id] = flag
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("include rows: %v", err)
	}
	return flags
}

// dgDuePassed parses every card's fsrs_card JSON and reports which card ids
// are FSRS-due (due <= now). The due PATH cannot surface the target words
// (their lesson sits beyond learned_lessons), so the card bytes are the
// only honest observable for 「只有 5 个到期」.
func dgDuePassed(t *testing.T, dbPath string) map[int64]bool {
	t.Helper()
	db := e2eOpenDB(t, dbPath)
	rows, err := db.Query("SELECT id, fsrs_card FROM cards ORDER BY id")
	if err != nil {
		t.Fatalf("read fsrs cards: %v", err)
	}
	defer rows.Close()
	due := make(map[int64]bool)
	now := time.Now().UTC()
	for rows.Next() {
		var id int64
		var raw string
		if err := rows.Scan(&id, &raw); err != nil {
			t.Fatalf("scan fsrs card: %v", err)
		}
		var card struct {
			Due time.Time `json:"due"`
		}
		if err := json.Unmarshal([]byte(raw), &card); err != nil {
			t.Fatalf("parse fsrs_card of card %d: %v", id, err)
		}
		due[id] = !card.Due.After(now)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("fsrs rows: %v", err)
	}
	return due
}

// TestE2E_DesignateIgnoresProgressDueAndUnit pins the AC-11 selection rule
// end to end: the ONLY inputs are textbook id, lesson name and the
// participation switch.
func TestE2E_DesignateIgnoresProgressDueAndUnit(t *testing.T) {
	ctx := context.Background()
	b, _, cfg := e2eBridge(t, nil)

	// 20 eligible + 2 to disable (participates=0) + 1 supplementary word
	// with no assignment. Insertion order ⇒ ids 1..23.
	ids := dgAddWords(t, b, "dg", 22)
	supp, err := b.RunAdd(ctx, AddOpts{
		Category: "english",
		Entries:  []AddEntry{{Word: "dgSupp", Meaning: "补充"}},
	})
	if err != nil || supp.Added != 1 {
		t.Fatalf("add supplementary = %+v, %v", supp, err)
	}

	// 15 of the 20 eligible words get a "good" review: their FSRS due moves
	// ~10 minutes into the future (learning step 2). The other 5 stay
	// due=now (new cards are always due).
	reviewed := map[string]string{}
	for _, id := range ids[:15] {
		reviewed[fmt.Sprintf("dg%d", id)] = "good"
	}
	if _, err := b.RunReview(ctx, ReviewOpts{Results: reviewed, Category: "english"}); err != nil {
		t.Fatalf("seed reviews: %v", err)
	}

	bookID := dgSetupBook(t, b, "指定教材A", "第4单元")
	dgAssignLesson(t, b, bookID, "第4单元", ids) // all 22, include_in_scope=0

	// 移出课堂的两个词仍归属该课次 —— 指定也不能把它们捞回来 (AC-15)。
	if _, err := b.RunSetParticipation(ctx, SetParticipationOpts{IDs: ids[20:22], Participates: false}); err != nil {
		t.Fatalf("disable participation: %v", err)
	}

	// Baselines: only 5 of the 20 eligible words are FSRS-due, and the due
	// PATH picks NONE of them (第4单元 is beyond learned_lessons=1) while the
	// supplementary word stays due — the control that proves the exclusion
	// is progress, not participation.
	duePassed := dgDuePassed(t, cfg.VocabDBPath)
	dueCount := 0
	for _, id := range ids[:20] {
		if duePassed[id] {
			dueCount++
		}
	}
	if dueCount != 5 {
		t.Fatalf("FSRS-due eligible words = %d, want 5 (15 reviewed into the future)", dueCount)
	}
	peek, err := b.RunDue(ctx, DueOpts{Category: "english", Peek: true})
	if err != nil {
		t.Fatalf("peek due: %v", err)
	}
	if len(peek.Cards) != 1 || peek.Cards[0].Word != "dgSupp" {
		t.Fatalf("due peek = %+v, want only the supplementary word", peek.Cards)
	}

	fsrsBefore := e2eFsrsSnapshot(t, cfg.VocabDBPath)
	logsBefore := e2eReviewLogsCount(t, cfg.VocabDBPath)
	includesBefore := dgIncludeFlags(t, cfg.VocabDBPath, bookID)
	if len(includesBefore) != 22 {
		t.Fatalf("assignment rows = %d, want 22", len(includesBefore))
	}

	res, err := b.RunDesignate(ctx, DesignateOpts{TextbookID: bookID, Lesson: "第4单元"})
	if err != nil {
		t.Fatalf("RunDesignate: %v", err)
	}
	if res.Status != DesignateStatusOK || res.Total != 20 || len(res.Cards) != 20 {
		t.Fatalf("designate = status %q total %d cards %d, want ok/20/20", res.Status, res.Total, len(res.Cards))
	}
	dgExpectIDs(t, dgCardIDs(res), ids[:20])
	if res.Cards[0].Word != "dg1" || res.Cards[0].Meaning != "指定" {
		t.Fatalf("first card = %+v, want the full word payload", res.Cards[0])
	}

	// 进度、加入标记、学习状态、复习记录都不变 —— 指定只加锁。
	if _, learned := tbReadLessonsProgress(t, cfg.VocabDBPath, bookID); learned != 1 {
		t.Fatalf("learned_lessons = %d, want 1 unchanged", learned)
	}
	if got := dgIncludeFlags(t, cfg.VocabDBPath, bookID); !mapsEqualInt(got, includesBefore) {
		t.Fatalf("include_in_scope changed by designate: %v -> %v", includesBefore, got)
	}
	if after := e2eFsrsSnapshot(t, cfg.VocabDBPath); after != fsrsBefore {
		t.Fatal("fsrs_card bytes changed across designate (指定不写复习)")
	}
	if logs := e2eReviewLogsCount(t, cfg.VocabDBPath); logs != logsBefore {
		t.Fatalf("review_logs changed by designate: %d -> %d", logsBefore, logs)
	}

	// The 20 winners carry the occupancy lock; the disabled and
	// supplementary words keep NULL.
	locks := dgLocks(t, cfg.VocabDBPath)
	for _, id := range ids[:20] {
		if locks[id] == "" {
			t.Fatalf("card %d not locked after ok designate", id)
		}
	}
	for _, id := range ids[20:22] {
		if locks[id] != "" {
			t.Fatalf("participates=0 card %d locked by designate", id)
		}
	}
	if locks[23] != "" {
		t.Fatal("supplementary card 23 locked by designate")
	}
}

// mapsEqualInt reports map equality for the include-flag baselines.
func mapsEqualInt(a, b map[int64]int) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

// TestE2E_DesignateRejectsOverLimit — 101 eligible words answer too_many
// with the real total and lock nothing (AC-11 超过则拒绝并说明总数).
func TestE2E_DesignateRejectsOverLimit(t *testing.T) {
	ctx := context.Background()
	b, _, cfg := e2eBridge(t, nil)

	ids := dgAddWords(t, b, "dgl", 101)
	bookID := dgSetupBook(t, b, "指定教材B", "第5单元")
	dgAssignLesson(t, b, bookID, "第5单元", ids)

	res, err := b.RunDesignate(ctx, DesignateOpts{TextbookID: bookID, Lesson: "第5单元"})
	if err != nil {
		t.Fatalf("RunDesignate: %v", err)
	}
	if res.Status != DesignateStatusTooMany || res.Total != 101 || len(res.Cards) != 0 {
		t.Fatalf("designate = status %q total %d cards %d, want too_many/101/0",
			res.Status, res.Total, len(res.Cards))
	}
	for id, lock := range dgLocks(t, cfg.VocabDBPath) {
		if lock != "" {
			t.Fatalf("card %d locked by a rejected designate", id)
		}
	}
}

// TestE2E_DesignateOccupiedKeepsLocksUntouched — one unexpired lock in the
// target rejects the whole batch and leaves every lock exactly as it was.
func TestE2E_DesignateOccupiedKeepsLocksUntouched(t *testing.T) {
	ctx := context.Background()
	b, _, cfg := e2eBridge(t, nil)

	ids := dgAddWords(t, b, "dgo", 3)
	bookID := dgSetupBook(t, b, "指定教材C", "第6单元")
	dgAssignLesson(t, b, bookID, "第6单元", ids)

	first, err := b.RunDesignate(ctx, DesignateOpts{TextbookID: bookID, Lesson: "第6单元"})
	if err != nil || first.Status != DesignateStatusOK || len(first.Cards) != 3 {
		t.Fatalf("first designate = %+v, %v; want ok with 3 cards", first, err)
	}
	// Release two of the three: card 1 keeps the unexpired lock.
	if _, err := b.RunUnlock(ctx, UnlockOpts{IDs: ids[1:3]}); err != nil {
		t.Fatalf("unlock: %v", err)
	}
	locksBefore := dgLocks(t, cfg.VocabDBPath)
	if locksBefore[1] == "" || locksBefore[2] != "" || locksBefore[3] != "" {
		t.Fatalf("setup locks = %v, want only card 1 locked", locksBefore)
	}

	res, err := b.RunDesignate(ctx, DesignateOpts{TextbookID: bookID, Lesson: "第6单元"})
	if err != nil {
		t.Fatalf("RunDesignate: %v", err)
	}
	if res.Status != DesignateStatusOccupied || res.Total != 3 || len(res.Cards) != 0 {
		t.Fatalf("designate = status %q total %d, want occupied/3", res.Status, res.Total)
	}
	// 其余锁时间不变: nothing was written, nothing was cleared.
	if got := dgLocks(t, cfg.VocabDBPath); !locksEqual(got, locksBefore) {
		t.Fatalf("locks changed by occupied reject: %v -> %v", locksBefore, got)
	}
}

// locksEqual reports map equality for the lock baselines.
func locksEqual(a, b map[int64]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

// TestE2E_DesignateConcurrentExactlyOneWins — two parallel designates on
// the same target: exactly one ok with all ids, the other occupied. The
// 60-word target widens the winner's UPDATE window so a broken
// transactionality reliably shows up as two winners.
func TestE2E_DesignateConcurrentExactlyOneWins(t *testing.T) {
	ctx := context.Background()
	b, _, _ := e2eBridge(t, nil)

	ids := dgAddWords(t, b, "dgc", 60)
	bookID := dgSetupBook(t, b, "指定教材D", "第7单元")
	dgAssignLesson(t, b, bookID, "第7单元", ids)

	results := make([]DesignateResult, 2)
	errs := make([]error, 2)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := range 2 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			results[i], errs[i] = b.RunDesignate(ctx, DesignateOpts{
				TextbookID: bookID, Lesson: "第7单元",
			})
		}(i)
	}
	close(start)
	wg.Wait()

	okCount, okLen := 0, 0
	for i := range 2 {
		if errs[i] != nil {
			t.Fatalf("concurrent designate %d: %v", i, errs[i])
		}
		switch results[i].Status {
		case DesignateStatusOK:
			okCount++
			okLen = len(results[i].Cards)
		case DesignateStatusOccupied:
			// the loser
		default:
			t.Fatalf("concurrent designate %d = %q, want ok or occupied", i, results[i].Status)
		}
	}
	if okCount != 1 || okLen != 60 {
		t.Fatalf("winners = %d (winner cards %d), want exactly 1 winner with 60 cards", okCount, okLen)
	}
	// The winner's card list is the full target, in id order.
	for i := range 2 {
		if results[i].Status == DesignateStatusOK {
			dgExpectIDs(t, dgCardIDs(results[i]), ids)
		}
	}
}

// TestE2E_DesignateValidatesTarget — unknown textbook id and a lesson
// outside the list are stdout diagnostics + exit 1 (the 404/400 mapping
// chain the api layer rides), never typed ok results.
func TestE2E_DesignateValidatesTarget(t *testing.T) {
	ctx := context.Background()
	b, _, _ := e2eBridge(t, nil)

	ids := dgAddWords(t, b, "dgv", 2)
	bookID := dgSetupBook(t, b, "指定教材E", "第8单元")
	dgAssignLesson(t, b, bookID, "第8单元", ids)

	if _, err := b.RunDesignate(ctx, DesignateOpts{TextbookID: 999, Lesson: "第8单元"}); err == nil {
		t.Fatal("unknown textbook id accepted")
	} else if !errors.Is(err, ErrPythonExit) {
		t.Fatalf("unknown textbook id error = %v, want ErrPythonExit", err)
	}
	if _, err := b.RunDesignate(ctx, DesignateOpts{TextbookID: bookID, Lesson: "不存在课"}); err == nil {
		t.Fatal("lesson outside the list accepted")
	} else if !errors.Is(err, ErrPythonExit) {
		t.Fatalf("lesson-not-in-list error = %v, want ErrPythonExit", err)
	}
	// 待确认 (empty lesson) never even spawns a subprocess.
	if _, err := b.RunDesignate(ctx, DesignateOpts{TextbookID: bookID, Lesson: ""}); err == nil {
		t.Fatal("empty lesson accepted")
	}
}
