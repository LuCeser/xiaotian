package lesson

// designation_test.go — the Issue #80 designations table contract on the
// real store (temp SQLite + Migrate, the store_test.go conventions):
// creation stores the snapshot as pending WITHOUT touching any other
// table, and ConsumeDesignation wins exactly once — 课堂行写入成功时才消费.

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func designationInput(n int) DesignationInput {
	cards := make([]DesignationCard, 0, n)
	for i := 1; i <= n; i++ {
		cards = append(cards, DesignationCard{
			ID: int64(i), Word: "词", Meaning: "义", Example: "",
		})
	}
	return DesignationInput{
		TextbookID: 7, Lesson: "第4单元", Category: "chinese", Cards: cards,
	}
}

func TestCreateDesignationStoresPendingSnapshot(t *testing.T) {
	db := newLessonTestDB(t)
	store, err := NewStore(db, nil)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	ctx := context.Background()

	created, err := store.CreateDesignation(ctx, designationInput(3))
	if err != nil {
		t.Fatalf("CreateDesignation: %v", err)
	}
	if created.ID <= 0 || created.Status != DesignationStatusPending {
		t.Fatalf("created = %+v, want a pending row with a positive id", created)
	}
	if created.Category != CategoryChinese {
		t.Fatalf("category = %q, want normalized %q", created.Category, CategoryChinese)
	}

	// The row on disk: pending, snapshot intact, and NO other table grew.
	var status, cardsJSON, consumedAt string
	if err := db.QueryRow(
		"SELECT status, cards, consumed_at FROM designations WHERE id = ?",
		created.ID,
	).Scan(&status, &cardsJSON, &consumedAt); err != nil {
		t.Fatalf("read designation row: %v", err)
	}
	if status != DesignationStatusPending || consumedAt != "" {
		t.Fatalf("row = status %q consumed_at %q, want pending/empty", status, consumedAt)
	}
	if !strings.Contains(cardsJSON, `"word":"词"`) || !strings.Contains(cardsJSON, `"id":3`) {
		t.Fatalf("cards column = %s, want the full 3-card snapshot", cardsJSON)
	}
	var others int
	if err := db.QueryRow(
		"SELECT (SELECT COUNT(*) FROM lessons) + (SELECT COUNT(*) FROM lesson_save_receipts)",
	).Scan(&others); err != nil {
		t.Fatalf("count sibling tables: %v", err)
	}
	if others != 0 {
		t.Fatalf("creation wrote %d rows into sibling tables, want 0 (不写进度、加入标记和参与开关)", others)
	}
}

func TestCreateDesignationRejectsInvalidInput(t *testing.T) {
	db := newLessonTestDB(t)
	store, err := NewStore(db, nil)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	ctx := context.Background()

	cases := map[string]DesignationInput{
		"no textbook":    {Lesson: "第4单元", Cards: designationInput(1).Cards},
		"no lesson":      {TextbookID: 7, Cards: designationInput(1).Cards},
		"no cards":       {TextbookID: 7, Lesson: "第4单元"},
		"over the limit": {TextbookID: 7, Lesson: "第4单元", Cards: designationInput(designationCardLimit + 1).Cards},
	}
	for name, in := range cases {
		if _, err := store.CreateDesignation(ctx, in); !errors.Is(err, ErrInvalidLesson) {
			t.Fatalf("%s: error = %v, want ErrInvalidLesson", name, err)
		}
	}
	var rows int
	if err := db.QueryRow("SELECT COUNT(*) FROM designations").Scan(&rows); err != nil {
		t.Fatalf("count designations: %v", err)
	}
	if rows != 0 {
		t.Fatalf("rejected inputs wrote %d rows, want 0", rows)
	}
}

func TestConsumeDesignationWinsExactlyOnce(t *testing.T) {
	db := newLessonTestDB(t)
	store, err := NewStore(db, nil)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	ctx := context.Background()

	created, err := store.CreateDesignation(ctx, designationInput(2))
	if err != nil {
		t.Fatalf("CreateDesignation: %v", err)
	}

	// First consume — the only one that wins.
	won, err := store.ConsumeDesignation(ctx, created.ID)
	if err != nil || !won {
		t.Fatalf("first ConsumeDesignation = %v, %v; want true, nil", won, err)
	}
	var status, consumedAt string
	if err := db.QueryRow(
		"SELECT status, consumed_at FROM designations WHERE id = ?", created.ID,
	).Scan(&status, &consumedAt); err != nil {
		t.Fatalf("read consumed row: %v", err)
	}
	if status != DesignationStatusConsumed || consumedAt == "" {
		t.Fatalf("row = status %q consumed_at %q, want consumed with a timestamp", status, consumedAt)
	}

	// Second consume — the deliberate no-win: no error, no rewrite of
	// history (consumed_at keeps the FIRST win's timestamp).
	won, err = store.ConsumeDesignation(ctx, created.ID)
	if err != nil || won {
		t.Fatalf("second ConsumeDesignation = %v, %v; want false, nil", won, err)
	}
	var status2, consumedAt2 string
	if err := db.QueryRow(
		"SELECT status, consumed_at FROM designations WHERE id = ?", created.ID,
	).Scan(&status2, &consumedAt2); err != nil {
		t.Fatalf("re-read consumed row: %v", err)
	}
	if consumedAt2 != consumedAt {
		t.Fatalf("consumed_at rewritten by the second consume: %q -> %q", consumedAt, consumedAt2)
	}

	// Unknown id and non-positive id stay loud, never silent no-wins.
	if _, err := store.ConsumeDesignation(ctx, 999); !errors.Is(err, ErrDesignationNotFound) {
		t.Fatalf("unknown id error = %v, want ErrDesignationNotFound", err)
	}
	if _, err := store.ConsumeDesignation(ctx, 0); !errors.Is(err, ErrInvalidLesson) {
		t.Fatalf("zero id error = %v, want ErrInvalidLesson", err)
	}
}
