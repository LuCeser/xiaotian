package database

// textbook_repo_test.go — Issue #4's database-layer acceptance: the Go
// read side of the textbook tables and, critically, the eligibility
// predicate that guards automatic selection on the practice path. The
// fixture encodes BOTH order traps the acceptance demands:
//
//   - T1 lessons ["第3课","第1课","第2课"] with learned=2: 第3课 is the
//     FIRST lesson (learned) while 第2课 is the third (unlearned) —
//     dictionary order would decide exactly the opposite.
//   - T2 lessons ["B课","A课"] with learned=1: only B课 is learned.
//
// plus US-33 (one learned assignment among several admits the word),
// 待确认 (empty lesson never learned), include_in_scope (parent
// override), and the #2 orthogonality (participates AND scope).

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/xiaotian/home-tutor-agent/pkg/infra"
)

// seedTextbookTables creates the Python-owned Issue #4 tables and the
// fixture matrix described in the file comment.
func seedTextbookTables(t *testing.T, db *sql.DB) {
	t.Helper()
	if _, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS textbooks (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			name TEXT NOT NULL,
			category TEXT NOT NULL DEFAULT 'english',
			edition TEXT NOT NULL DEFAULT '',
			stage TEXT NOT NULL DEFAULT '',
			grade TEXT NOT NULL DEFAULT '',
			volume TEXT NOT NULL DEFAULT '',
			module TEXT NOT NULL DEFAULT '',
			lessons TEXT NOT NULL DEFAULT '[]',
			learned_lessons INTEGER NOT NULL DEFAULT 0,
			is_primary INTEGER NOT NULL DEFAULT 0,
			created_at TEXT NOT NULL DEFAULT (datetime('now')),
			UNIQUE(name, category)
		);
		CREATE TABLE IF NOT EXISTS card_assignments (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			card_id INTEGER NOT NULL,
			textbook_id INTEGER NOT NULL,
			lesson TEXT NOT NULL DEFAULT '',
			include_in_scope INTEGER NOT NULL DEFAULT 0,
			created_at TEXT NOT NULL DEFAULT (datetime('now')),
			UNIQUE(card_id, textbook_id)
		);
	`); err != nil {
		t.Fatalf("seed textbook tables: %v", err)
	}
	// T1: explicit order deliberately contradicts dictionary order.
	if _, err := db.Exec(`INSERT INTO textbooks (id, name, category, lessons, learned_lessons, is_primary)
		VALUES (1, '主教材', 'english', '["第3课","第1课","第2课"]', 2, 1)`); err != nil {
		t.Fatalf("seed T1: %v", err)
	}
	// T2: independent progress (learned only its first lesson).
	if _, err := db.Exec(`INSERT INTO textbooks (id, name, category, lessons, learned_lessons)
		VALUES (2, '课外教材', 'english', '["B课","A课"]', 1)`); err != nil {
		t.Fatalf("seed T2: %v", err)
	}
	// Assignments for cards 2..10 (card ids follow seedCardsTable order
	// plus the extra cards seeded by the scope test).
	assign := func(card, textbook int64, lesson string, include bool) {
		t.Helper()
		inc := 0
		if include {
			inc = 1
		}
		if _, err := db.Exec(
			"INSERT INTO card_assignments (card_id, textbook_id, lesson, include_in_scope) VALUES (?, ?, ?, ?)",
			card, textbook, lesson, inc,
		); err != nil {
			t.Fatalf("seed assignment card=%d tb=%d: %v", card, textbook, err)
		}
	}
	assign(2, 1, "第1课", false) // learned (explicit slot 2)
	assign(3, 1, "第2课", false) // unlearned (explicit slot 3 — dict order lies)
	assign(4, 1, "第3课", false) // learned (explicit slot 1)
	assign(5, 1, "", false)      // 待确认
	assign(6, 1, "", true)       // 待确认 + parent include override
	assign(7, 2, "A课", false)   // T2 unlearned
	assign(8, 2, "B课", false)   // T2 learned
	assign(9, 1, "第2课", false) // unlearned in T1 …
	assign(9, 2, "B课", false)   // … but learned in T2 → US-33 admits
	assign(10, 1, "第1课", false) // learned, but participates=0 (orthogonality)
}

// TestTextbookRepository covers the read surface: creation-order listing
// with assignment counts, category scoping, Get's not-found sentinel, the
// per-card attribution projection, and the pre-migration degraded reads.
func TestTextbookRepository(t *testing.T) {
	ctx := context.Background()

	t.Run("pre-migration database degrades to empty reads", func(t *testing.T) {
		db := newTestDB(t) // Go tables only — no cards, no textbooks
		repo, err := NewTextbookRepository(db, nil)
		if err != nil {
			t.Fatalf("NewTextbookRepository: %v", err)
		}
		books, err := repo.List(ctx, "")
		if err != nil || len(books) != 0 {
			t.Fatalf("List on bare db = %v, %v; want empty, nil", books, err)
		}
		if _, err := repo.Get(ctx, 1); !errors.Is(err, ErrTextbookNotFound) {
			t.Fatalf("Get on bare db err = %v, want ErrTextbookNotFound", err)
		}
		byCard, err := repo.AssignmentsByCardIDs(ctx, []int64{1, 2})
		if err != nil || len(byCard) != 0 {
			t.Fatalf("AssignmentsByCardIDs on bare db = %v, %v; want empty", byCard, err)
		}
		if n, err := repo.CountAssignments(ctx, 1); err != nil || n != 0 {
			t.Fatalf("CountAssignments on bare db = %d, %v; want 0", n, err)
		}
	})

	t.Run("list, get, assignments", func(t *testing.T) {
		db := newTestDBWithCards(t)
		seedTextbookTables(t, db)
		// One extra card so the fixture ids line up (cards 6..10).
		for _, w := range []string{"fig", "grape", "hat", "ink", "jam"} {
			if _, err := db.Exec(
				"INSERT INTO cards (word, meaning, unit, category, fsrs_card) VALUES (?, '', '', 'english', '{}')", w,
			); err != nil {
				t.Fatalf("seed extra card %s: %v", w, err)
			}
		}
		repo, err := NewTextbookRepository(db, nil)
		if err != nil {
			t.Fatalf("NewTextbookRepository: %v", err)
		}

		books, err := repo.List(ctx, "english")
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		if len(books) != 2 || books[0].Name != "主教材" || books[1].Name != "课外教材" {
			t.Fatalf("List = %+v; want T1 then T2 in creation order", books)
		}
		if !books[0].IsPrimary || books[0].LearnedLessons != 2 {
			t.Fatalf("T1 = %+v; want primary with progress 2", books[0])
		}
		if got := books[0].Lessons; len(got) != 3 || got[0] != "第3课" || got[2] != "第2课" {
			t.Fatalf("T1 lessons = %v; want explicit order preserved", got)
		}
		// T1 owns cards 2,3,4,5,6,9,10 → 7 assignments.
		if books[0].AssignmentCount != 7 || books[1].AssignmentCount != 3 {
			t.Fatalf("assignment counts = %d/%d; want 7/3", books[0].AssignmentCount, books[1].AssignmentCount)
		}

		book, err := repo.Get(ctx, 2)
		if err != nil || book.Name != "课外教材" {
			t.Fatalf("Get(2) = %+v, %v", book, err)
		}
		if _, err := repo.Get(ctx, 99); !errors.Is(err, ErrTextbookNotFound) {
			t.Fatalf("Get(99) err = %v, want ErrTextbookNotFound", err)
		}

		byCard, err := repo.AssignmentsByCardIDs(ctx, []int64{1, 3, 9})
		if err != nil {
			t.Fatalf("AssignmentsByCardIDs: %v", err)
		}
		if _, has := byCard[1]; has {
			t.Fatal("card 1 (supplementary) must have no attribution entries")
		}
		if len(byCard[3]) != 1 || byCard[3][0].TextbookName != "主教材" || byCard[3][0].Lesson != "第2课" {
			t.Fatalf("card 3 attribution = %+v", byCard[3])
		}
		if len(byCard[9]) != 2 { // US-33 shape: one card, two textbooks
			t.Fatalf("card 9 attribution = %+v; want two entries", byCard[9])
		}
	})
}

// TestCardScopeEligibleOnly is the practice-path eligibility matrix: the
// explicit-order traps, 待确认, include override, US-33 multi-assignment,
// and the #2 orthogonality. Card numbering follows seedCardsTable (5
// cards) plus five extras:
//
//	1  supplementary        → eligible
//	2  T1 第1课 (learned)    → eligible
//	3  T1 第2课 (unlearned)  → out (dict order would admit it — the trap)
//	4  T1 第3课 (learned)    → eligible (dict order would exclude it — the trap)
//	5  T1 待确认             → out (never auto-learned)
//	6  T1 待确认 + include   → eligible (parent override)
//	7  T2 A课 (unlearned)    → out
//	8  T2 B课 (learned)      → eligible
//	9  T1 第2课 + T2 B课     → eligible (US-33: one learned suffices)
//	10 T1 第1课, disabled    → eligible by scope, out under ParticipatingOnly
func TestCardScopeEligibleOnly(t *testing.T) {
	db := newTestDBWithCards(t)
	for _, w := range []string{"fig", "grape", "hat", "ink", "jam"} {
		if _, err := db.Exec(
			"INSERT INTO cards (word, meaning, unit, category, fsrs_card) VALUES (?, '', '', 'english', '{}')", w,
		); err != nil {
			t.Fatalf("seed extra card %s: %v", w, err)
		}
	}
	if _, err := db.Exec("UPDATE cards SET participates = 0 WHERE id = 10"); err != nil {
		t.Fatalf("disable card 10: %v", err)
	}
	seedTextbookTables(t, db)
	repo, err := NewCardRepository(db, nil)
	if err != nil {
		t.Fatalf("NewCardRepository: %v", err)
	}

	wantScope := map[int64]bool{
		1: true, 2: true, 3: false, 4: true, 5: false,
		6: true, 7: false, 8: true, 9: true, 10: true,
	}
	got := map[int64]bool{}
	cards, err := repo.List(context.Background(), CardFilter{ScopeEligibleOnly: true})
	if err != nil {
		t.Fatalf("List(ScopeEligibleOnly): %v", err)
	}
	for _, c := range cards {
		got[c.ID] = true
	}
	for id, want := range wantScope {
		if got[id] != want {
			t.Fatalf("card %d eligible = %v, want %v (selected: %v)", id, got[id], want, got)
		}
	}

	// Orthogonality: participates AND scope — card 10 drops out once both
	// switches apply, card 3 stays out, card 9 stays in.
	both := map[int64]bool{}
	cards, err = repo.List(context.Background(), CardFilter{ParticipatingOnly: true, ScopeEligibleOnly: true})
	if err != nil {
		t.Fatalf("List(both): %v", err)
	}
	for _, c := range cards {
		both[c.ID] = true
	}
	for _, id := range []int64{1, 2, 4, 6, 8, 9} {
		if !both[id] {
			t.Fatalf("card %d missing from scope+participating selection: %v", id, both)
		}
	}
	for _, id := range []int64{3, 5, 7, 10} {
		if both[id] {
			t.Fatalf("card %d present in scope+participating selection: %v", id, both)
		}
	}

	// Management views stay unfiltered: the plain list still shows every
	// card (out-of-scope words remain manageable).
	cards, err = repo.List(context.Background(), CardFilter{})
	if err != nil || len(cards) != 10 {
		t.Fatalf("plain List = %d cards, err=%v; want all 10 visible", len(cards), err)
	}

	// Progress bump has immediate effect: T1 learned 2→3 admits card 3.
	if _, err := db.Exec("UPDATE textbooks SET learned_lessons = 3 WHERE id = 1"); err != nil {
		t.Fatalf("bump T1 progress: %v", err)
	}
	cards, err = repo.List(context.Background(), CardFilter{ScopeEligibleOnly: true})
	if err != nil {
		t.Fatalf("List after bump: %v", err)
	}
	inScope := map[int64]bool{}
	for _, c := range cards {
		inScope[c.ID] = true
	}
	if !inScope[3] {
		t.Fatalf("card 3 still out after T1 progress 2→3: %v", inScope)
	}
}

// TestCardScopeOnLegacyDB pins the degraded read: a database with cards
// but no textbook tables keeps every card eligible (the pre-#4 behavior,
// same contract as the missing participates column).
func TestCardScopeOnLegacyDB(t *testing.T) {
	db := newTestDBWithCards(t)
	repo, err := NewCardRepository(db, nil)
	if err != nil {
		t.Fatalf("NewCardRepository: %v", err)
	}
	cards, err := repo.List(context.Background(), CardFilter{ScopeEligibleOnly: true})
	if err != nil || len(cards) != 5 {
		t.Fatalf("legacy scope list = %d cards, err=%v; want all 5 eligible", len(cards), err)
	}
}

// TestScopePredicateFirstPredicate pins the WHERE alternation: a scope-
// ONLY filter must produce valid SQL (the predicate is the first term —
// the addPredicate splice, not a raw AND append).
func TestScopePredicateFirstPredicate(t *testing.T) {
	db := newTestDBWithCards(t)
	seedTextbookTables(t, db)
	repo, err := NewCardRepository(db, nil)
	if err != nil {
		t.Fatalf("NewCardRepository: %v", err)
	}
	cards, err := repo.List(context.Background(), CardFilter{
		ScopeEligibleOnly: true, Category: "english", Unit: "unitA",
	})
	if err != nil {
		t.Fatalf("List(scope+category+unit): %v", err)
	}
	// unitA holds cards 1 (apple, supplementary) and 2 (banana, learned).
	if len(cards) != 2 || cards[0].ID != 1 || cards[1].ID != 2 {
		t.Fatalf("scoped unitA = %+v; want cards 1 and 2", cards)
	}
}

// Interface nil-guard sanity (D-47).
func TestNewTextbookRepositoryNilDB(t *testing.T) {
	if _, err := NewTextbookRepository(nil, infra.NewNoOpLogger()); err == nil {
		t.Fatal("nil db accepted, want rejection")
	}
}
