package lesson

import (
	"context"
	"strings"
	"testing"
)

// TestDesignationsRequestKeyUniqueIndex — removing idx_designations_request_key
// from Migrate must make this test fail (Issue #80 U10 mutation guard).
func TestDesignationsRequestKeyUniqueIndex(t *testing.T) {
	db := newLessonTestDB(t)
	ctx := context.Background()

	const key = "dup-key"
	ins := `INSERT INTO designations (textbook_id, lesson, category, cards, status, request_key)
		VALUES (1, '第1课', 'english', '[]', 'pending', ?)`
	if _, err := db.ExecContext(ctx, ins, key); err != nil {
		t.Fatalf("first insert: %v", err)
	}
	if _, err := db.ExecContext(ctx, ins, key); err == nil {
		t.Fatal("second insert with same request_key succeeded, want UNIQUE violation")
	} else if !strings.Contains(strings.ToLower(err.Error()), "unique") {
		t.Fatalf("second insert error = %v, want UNIQUE constraint", err)
	}
}
