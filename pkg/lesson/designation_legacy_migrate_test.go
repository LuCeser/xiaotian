package lesson

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xiaotian/home-tutor-agent/pkg/database"
)

// legacyDesignationsDDL matches feat/issue-80 @ bdf9b07 before request_key (U10).
const legacyDesignationsDDL = `CREATE TABLE designations (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    textbook_id INTEGER NOT NULL,
    lesson TEXT NOT NULL,
    category TEXT NOT NULL DEFAULT '',
    cards TEXT NOT NULL DEFAULT '[]',
    status TEXT NOT NULL DEFAULT 'pending',
    created_at TEXT NOT NULL DEFAULT (datetime('now')),
    consumed_at TEXT NOT NULL DEFAULT ''
)`

// TestMigrateLegacyDesignationsAddsRequestKey — creating idx before ALTER
// breaks real VPS databases; this test must fail if Migrate regresses.
func TestMigrateLegacyDesignationsAddsRequestKey(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "legacy-designations.db")
	db, err := database.Open(dbPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	ctx := context.Background()
	if _, err := db.ExecContext(ctx, legacyDesignationsDDL); err != nil {
		t.Fatalf("legacy DDL: %v", err)
	}
	if err := Migrate(ctx, db); err != nil {
		t.Fatalf("Migrate on legacy designations: %v", err)
	}
	has, err := designationsHasColumn(ctx, db, "request_key")
	if err != nil || !has {
		t.Fatalf("request_key column missing after migrate: has=%v err=%v", has, err)
	}

	const key = "legacy-dup"
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
