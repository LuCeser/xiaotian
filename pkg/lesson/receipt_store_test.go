// receipt_store_test.go — the 13.3-01 save-receipt store tests (ROADMAP
// success criterion 1): the two-phase pending→saved/failed write with the
// real word sets, the idempotent completion, and the per-lesson listing the
// parent read path consumes. Temp SQLite throughout (the
// practice_store_test.go shape).
package lesson

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/xiaotian/home-tutor-agent/pkg/infra"
)

// TestReceiptBeginInsertsPendingRow (behavior 1): Begin lands exactly ONE
// lesson_save_receipts row, status pending, completed_at still empty — the
// crash-window shape 「待核实」 reads from. The requested word set is stored
// verbatim (set equality with the input) and SORTED before the JSON marshal
// (map iteration order must never leak into the row payload — the row is
// byte-compared across identical submissions by later verification).
func TestReceiptBeginInsertsPendingRow(t *testing.T) {
	store, err := NewReceiptStore(newLessonTestDB(t), infra.NewNoOpLogger())
	if err != nil {
		t.Fatalf("NewReceiptStore: %v", err)
	}
	ctx := context.Background()

	// Input deliberately unsorted: storage must normalize it.
	rec, err := store.Begin(ctx, ReceiptInput{
		LessonID:  "r0-20261010",
		RequestID: "req-submit-1",
		Kind:      ReceiptKindCheck,
		Requested: []string{"香蕉", "苹果"},
	})
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if rec.ID == 0 {
		t.Fatal("Begin returned receipt id 0, want the inserted row id")
	}

	var count int
	if err := store.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM lesson_save_receipts").Scan(&count); err != nil {
		t.Fatalf("count receipts: %v", err)
	}
	if count != 1 {
		t.Fatalf("lesson_save_receipts rows = %d, want exactly 1 after Begin", count)
	}

	var status, completedAt string
	if err := store.db.QueryRowContext(ctx,
		"SELECT status, completed_at FROM lesson_save_receipts WHERE id = ?", rec.ID,
	).Scan(&status, &completedAt); err != nil {
		t.Fatalf("read receipt row: %v", err)
	}
	if status != ReceiptStatusPending {
		t.Errorf("status = %q, want %q (pending until Complete)", status, ReceiptStatusPending)
	}
	if completedAt != "" {
		t.Errorf("completed_at = %q, want empty before Complete", completedAt)
	}

	got, err := store.ListByLesson(ctx, "r0-20261010")
	if err != nil {
		t.Fatalf("ListByLesson: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("ListByLesson rows = %d, want 1", len(got))
	}
	r := got[0]
	if r.Kind != ReceiptKindCheck || r.RequestID != "req-submit-1" || r.LessonID != "r0-20261010" {
		t.Errorf("receipt identity = kind:%q request:%q lesson:%q, want check/req-submit-1/r0-20261010", r.Kind, r.RequestID, r.LessonID)
	}
	if want := `["苹果","香蕉"]`; r.Requested == nil || len(r.Requested) != 2 {
		t.Errorf("requested = %v, want the sorted input set [苹果 香蕉]", r.Requested)
	} else if !reflect.DeepEqual(r.Requested, []string{"苹果", "香蕉"}) {
		t.Errorf("requested = %v (order %q), want the sorted set %s", r.Requested, r.Requested, want)
	}
}

// TestReceiptBeginRejectsInvalidArguments: an anonymous receipt (empty
// lesson_id/request_id/kind) could never be joined back to a lesson or an
// idempotency key honestly — Begin rejects the whole input before anything
// is written (the RecordPractice whole-batch discipline).
func TestReceiptBeginRejectsInvalidArguments(t *testing.T) {
	store, err := NewReceiptStore(newLessonTestDB(t), infra.NewNoOpLogger())
	if err != nil {
		t.Fatalf("NewReceiptStore: %v", err)
	}
	ctx := context.Background()

	for _, tc := range []struct {
		name string
		in   ReceiptInput
	}{
		{"empty lesson id", ReceiptInput{RequestID: "req-1", Kind: ReceiptKindCheck}},
		{"empty request id", ReceiptInput{LessonID: "r0-1", Kind: ReceiptKindCheck}},
		{"empty kind", ReceiptInput{LessonID: "r0-1", RequestID: "req-1"}},
	} {
		if _, err := store.Begin(ctx, tc.in); err == nil {
			t.Errorf("%s: Begin error = nil, want ErrInvalidLesson", tc.name)
		}
	}

	var count int
	if err := store.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM lesson_save_receipts").Scan(&count); err != nil {
		t.Fatalf("count receipts: %v", err)
	}
	if count != 0 {
		t.Errorf("lesson_save_receipts rows = %d, want 0 after rejected inputs", count)
	}
}

// TestReceiptCompleteFillsRealOutcome (behavior 2): Complete flips the SAME
// row to saved/failed with the REAL updated/missing sets and a non-empty
// completed_at. The failed shape carries nil updated (nothing landed) and
// the missing set the caller reports.
func TestReceiptCompleteFillsRealOutcome(t *testing.T) {
	store, err := NewReceiptStore(newLessonTestDB(t), infra.NewNoOpLogger())
	if err != nil {
		t.Fatalf("NewReceiptStore: %v", err)
	}
	ctx := context.Background()

	saved, err := store.Begin(ctx, ReceiptInput{
		LessonID: "r0-20261010", RequestID: "req-1", Kind: ReceiptKindCheck,
		Requested: []string{"苹果", "香蕉"},
	})
	if err != nil {
		t.Fatalf("Begin saved: %v", err)
	}
	if err := store.Complete(ctx, saved.ID, []string{"苹果"}, []string{"香蕉"}, true); err != nil {
		t.Fatalf("Complete saved: %v", err)
	}

	failed, err := store.Begin(ctx, ReceiptInput{
		LessonID: "r0-20261010", RequestID: "req-2", Kind: ReceiptKindPractice,
		Requested: []string{"苹果"},
	})
	if err != nil {
		t.Fatalf("Begin failed: %v", err)
	}
	if err := store.Complete(ctx, failed.ID, nil, []string{"苹果"}, false); err != nil {
		t.Fatalf("Complete failed: %v", err)
	}

	rows, err := store.ListByLesson(ctx, "r0-20261010")
	if err != nil {
		t.Fatalf("ListByLesson: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("ListByLesson rows = %d, want 2", len(rows))
	}
	byRequest := map[string]Receipt{}
	for _, r := range rows {
		byRequest[r.RequestID] = r
	}

	gotSaved, ok := byRequest["req-1"]
	if !ok {
		t.Fatal("req-1 row missing after Complete(saved)")
	}
	if gotSaved.Status != ReceiptStatusSaved {
		t.Errorf("req-1 status = %q, want saved", gotSaved.Status)
	}
	if !reflect.DeepEqual(gotSaved.Updated, []string{"苹果"}) || !reflect.DeepEqual(gotSaved.Missing, []string{"香蕉"}) {
		t.Errorf("req-1 outcome = updated:%v missing:%v, want [苹果]/[香蕉] (the real sets)", gotSaved.Updated, gotSaved.Missing)
	}
	if gotSaved.CompletedAt == "" {
		t.Error("req-1 completed_at empty, want the completion timestamp")
	}

	gotFailed, ok := byRequest["req-2"]
	if !ok {
		t.Fatal("req-2 row missing after Complete(failed)")
	}
	if gotFailed.Status != ReceiptStatusFailed {
		t.Errorf("req-2 status = %q, want failed", gotFailed.Status)
	}
	if len(gotFailed.Updated) != 0 {
		t.Errorf("req-2 updated = %v, want empty (nothing landed)", gotFailed.Updated)
	}
	if !reflect.DeepEqual(gotFailed.Missing, []string{"苹果"}) {
		t.Errorf("req-2 missing = %v, want [苹果]", gotFailed.Missing)
	}
	if gotFailed.CompletedAt == "" {
		t.Error("req-2 completed_at empty, want the completion timestamp")
	}
}

// TestReceiptCompleteIdempotent (behavior 3): a second Complete on the same
// row is a no-op — zero rows matched (the status='pending' guard), no
// error, and the row keeps the FIRST completion verbatim. A late duplicate
// callback must never rewrite an already-settled outcome.
func TestReceiptCompleteIdempotent(t *testing.T) {
	store, err := NewReceiptStore(newLessonTestDB(t), infra.NewNoOpLogger())
	if err != nil {
		t.Fatalf("NewReceiptStore: %v", err)
	}
	ctx := context.Background()

	rec, err := store.Begin(ctx, ReceiptInput{
		LessonID: "r0-20261010", RequestID: "req-1", Kind: ReceiptKindCheck,
		Requested: []string{"苹果"},
	})
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if err := store.Complete(ctx, rec.ID, []string{"苹果"}, nil, true); err != nil {
		t.Fatalf("first Complete: %v", err)
	}

	type rowState struct {
		Status      string
		Updated     []string
		Missing     []string
		CompletedAt string
	}
	read := func() rowState {
		t.Helper()
		var rs rowState
		var updatedJSON, missingJSON string
		if err := store.db.QueryRowContext(ctx,
			"SELECT status, updated, missing, completed_at FROM lesson_save_receipts WHERE id = ?", rec.ID,
		).Scan(&rs.Status, &updatedJSON, &missingJSON, &rs.CompletedAt); err != nil {
			t.Fatalf("read row: %v", err)
		}
		if err := json.Unmarshal([]byte(updatedJSON), &rs.Updated); err != nil {
			t.Fatalf("decode updated: %v", err)
		}
		if err := json.Unmarshal([]byte(missingJSON), &rs.Missing); err != nil {
			t.Fatalf("decode missing: %v", err)
		}
		return rs
	}

	before := read()
	// A second completion with a DIFFERENT outcome must not touch the row.
	if err := store.Complete(ctx, rec.ID, nil, []string{"苹果"}, false); err != nil {
		t.Errorf("second Complete error = %v, want nil no-op", err)
	}
	after := read()
	if before.Status != after.Status || before.CompletedAt != after.CompletedAt ||
		!reflect.DeepEqual(before.Updated, after.Updated) || !reflect.DeepEqual(before.Missing, after.Missing) {
		t.Errorf("row changed after the second Complete: before=%+v after=%+v", before, after)
	}
	if after.Status != ReceiptStatusSaved {
		t.Errorf("status after duplicate Complete = %q, want the first outcome (saved)", after.Status)
	}
}

// TestLatestByLessonIDs (13.3-02 behavior 4): the list-page receipt
// aggregation — per lesson, the LATEST receipt row (id max = last recorded).
// An empty id slice is the zero-SQL fast path: it must return the empty map
// WITHOUT touching the database (proven by closing the DB first — any query
// would surface as an error).
func TestLatestByLessonIDs(t *testing.T) {
	store, err := NewReceiptStore(newLessonTestDB(t), infra.NewNoOpLogger())
	if err != nil {
		t.Fatalf("NewReceiptStore: %v", err)
	}
	ctx := context.Background()

	for _, in := range []ReceiptInput{
		{LessonID: "r0-a", RequestID: "req-a1", Kind: ReceiptKindCheck, Requested: []string{"苹果"}},
		{LessonID: "r0-a", RequestID: "req-a2", Kind: ReceiptKindCorrection, Requested: []string{"苹果"}},
		{LessonID: "r0-b", RequestID: "req-b1", Kind: ReceiptKindPractice, Requested: []string{"香蕉"}},
	} {
		if _, err := store.Begin(ctx, in); err != nil {
			t.Fatalf("Begin %s: %v", in.RequestID, err)
		}
	}

	got, err := store.LatestByLessonIDs(ctx, []string{"r0-a", "r0-b", "r0-unknown"})
	if err != nil {
		t.Fatalf("LatestByLessonIDs: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("LatestByLessonIDs map size = %d, want 2 (unknown lesson absent)", len(got))
	}
	latestA, ok := got["r0-a"]
	if !ok {
		t.Fatal("r0-a missing from aggregation")
	}
	if latestA.RequestID != "req-a2" || latestA.Kind != ReceiptKindCorrection {
		t.Errorf("r0-a latest = request:%q kind:%q, want req-a2/correction (id max = last recorded)", latestA.RequestID, latestA.Kind)
	}
	if latestA.ID == 0 {
		t.Error("r0-a latest receipt id = 0, want the real row id")
	}
	latestB, ok := got["r0-b"]
	if !ok {
		t.Fatal("r0-b missing from aggregation")
	}
	if latestB.RequestID != "req-b1" || latestB.Kind != ReceiptKindPractice {
		t.Errorf("r0-b latest = request:%q kind:%q, want req-b1/practice", latestB.RequestID, latestB.Kind)
	}
}

// TestLatestByLessonIDsEmptySliceZeroSQL (13.3-02 acceptance: zero SQL on the
// empty slice): the fast path must not issue a query — proven by closing the
// DB before the call; any SQL attempt surfaces as "database is closed".
func TestLatestByLessonIDsEmptySliceZeroSQL(t *testing.T) {
	db := newLessonTestDB(t)
	store, err := NewReceiptStore(db, infra.NewNoOpLogger())
	if err != nil {
		t.Fatalf("NewReceiptStore: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close db: %v", err)
	}

	got, err := store.LatestByLessonIDs(context.Background(), []string{})
	if err != nil {
		t.Fatalf("LatestByLessonIDs(empty) error = %v, want nil (zero-SQL fast path)", err)
	}
	if got == nil || len(got) != 0 {
		t.Errorf("LatestByLessonIDs(empty) = %#v, want a non-nil empty map", got)
	}
}

// TestReceiptListByLesson (behavior 4): ListByLesson returns every row of
// the lesson in insert order — and only those. An unknown lesson lists zero
// rows with no error.
func TestReceiptListByLesson(t *testing.T) {
	store, err := NewReceiptStore(newLessonTestDB(t), infra.NewNoOpLogger())
	if err != nil {
		t.Fatalf("NewReceiptStore: %v", err)
	}
	ctx := context.Background()

	for _, in := range []ReceiptInput{
		{LessonID: "r0-a", RequestID: "req-a1", Kind: ReceiptKindCheck, Requested: []string{"苹果"}},
		{LessonID: "r0-b", RequestID: "req-b1", Kind: ReceiptKindPractice, Requested: []string{"香蕉"}},
		{LessonID: "r0-a", RequestID: "req-a2", Kind: ReceiptKindCorrection, Requested: []string{"橘子"}},
	} {
		if _, err := store.Begin(ctx, in); err != nil {
			t.Fatalf("Begin %s: %v", in.RequestID, err)
		}
	}

	gotA, err := store.ListByLesson(ctx, "r0-a")
	if err != nil {
		t.Fatalf("ListByLesson r0-a: %v", err)
	}
	if len(gotA) != 2 {
		t.Fatalf("ListByLesson(r0-a) rows = %d, want 2", len(gotA))
	}
	if gotA[0].RequestID != "req-a1" || gotA[1].RequestID != "req-a2" {
		t.Errorf("ListByLesson(r0-a) order = [%s %s], want [req-a1 req-a2] (insert order)", gotA[0].RequestID, gotA[1].RequestID)
	}

	gotB, err := store.ListByLesson(ctx, "r0-b")
	if err != nil {
		t.Fatalf("ListByLesson r0-b: %v", err)
	}
	if len(gotB) != 1 || gotB[0].Kind != ReceiptKindPractice {
		t.Errorf("ListByLesson(r0-b) = %+v, want the single practice row", gotB)
	}

	gotNone, err := store.ListByLesson(ctx, "r0-none")
	if err != nil {
		t.Fatalf("ListByLesson unknown lesson: %v", err)
	}
	if len(gotNone) != 0 {
		t.Errorf("ListByLesson(unknown) rows = %d, want 0", len(gotNone))
	}
}
