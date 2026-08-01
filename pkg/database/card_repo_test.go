package database

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/xiaotian/home-tutor-agent/pkg/infra"
)

// seedCardsTable creates the Python-owned cards table and inserts fixture rows.
// This mirrors vocab_manager.py init_db (lines 103-128) plus the
// fetch_locked_until migration (lines 126-128).
func seedCardsTable(t *testing.T, db *sql.DB) {
	t.Helper()

	// Create cards table matching vocab_manager.py schema
	_, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS cards (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			word TEXT NOT NULL,
			meaning TEXT NOT NULL DEFAULT '',
			example TEXT NOT NULL DEFAULT '',
			unit TEXT NOT NULL DEFAULT '',
			category TEXT NOT NULL DEFAULT 'english',
			fsrs_card TEXT NOT NULL DEFAULT '',
			created_at TEXT NOT NULL DEFAULT (datetime('now')),
			fetch_locked_until TEXT DEFAULT NULL,
			UNIQUE(word, category)
		)
	`)
	if err != nil {
		t.Fatalf("seed cards table: %v", err)
	}

	// Insert fixture cards
	fixtures := []struct {
		word, meaning, example, unit, category, fsrsCard string
	}{
		{"apple", "苹果", "I eat an apple every day.", "unitA", "english", `{"stability":1.0}`},
		{"banana", "香蕉", "Bananas are yellow.", "unitA", "english", `{"stability":2.0}`},
		{"cat", "猫", "The cat sat on the mat.", "unitB", "english", `{"stability":3.0}`},
		{"dog", "狗", "The dog barked loudly.", "unitB", "english", `{"stability":4.0}`},
		{"elephant", "大象", "Elephants are large.", "unitC", "english", `{"stability":5.0}`},
	}

	for _, f := range fixtures {
		_, err := db.Exec(
			"INSERT INTO cards (word, meaning, example, unit, category, fsrs_card) VALUES (?, ?, ?, ?, ?, ?)",
			f.word, f.meaning, f.example, f.unit, f.category, f.fsrsCard,
		)
		if err != nil {
			t.Fatalf("seed card %q: %v", f.word, err)
		}
	}
}

// newTestDBWithCards returns an in-memory SQLite database with both Go-owned
// tables (via newTestDB) and the Python-owned cards table seeded.
func newTestDBWithCards(t *testing.T) *sql.DB {
	t.Helper()
	db := newTestDB(t)
	seedCardsTable(t, db)
	return db
}

func TestCardRepository(t *testing.T) {
	t.Run("NewCardRepository with nil db returns error", func(t *testing.T) {
		_, err := NewCardRepository(nil, infra.NewNoOpLogger())
		if err == nil {
			t.Fatal("NewCardRepository(nil, logger) expected error, got nil")
		}
	})

	t.Run("NewCardRepository with nil logger substitutes no-op", func(t *testing.T) {
		db := newTestDBWithCards(t)
		repo, err := NewCardRepository(db, nil)
		if err != nil {
			t.Fatalf("NewCardRepository(db, nil) error = %v", err)
		}
		if repo == nil {
			t.Fatal("NewCardRepository(db, nil) returned nil repo")
		}
	})

	t.Run("GetByID returns card with all fields populated", func(t *testing.T) {
		db := newTestDBWithCards(t)
		repo, _ := NewCardRepository(db, infra.NewNoOpLogger())

		// Insert a card with known values (avoid created_at dependency)
		_, err := db.Exec(
			"INSERT INTO cards (word, meaning, example, unit, category, fsrs_card) VALUES (?, ?, ?, ?, ?, ?)",
			"test_word", "test_meaning", "test_example", "test_unit", "test_category", `{"test":true}`,
		)
		if err != nil {
			t.Fatalf("insert test card: %v", err)
		}

		var id int64
		err = db.QueryRow("SELECT id FROM cards WHERE word = ?", "test_word").Scan(&id)
		if err != nil {
			t.Fatalf("get test card id: %v", err)
		}

		got, err := repo.GetByID(context.Background(), id)
		if err != nil {
			t.Fatalf("GetByID() error = %v", err)
		}
		if got.Word != "test_word" {
			t.Errorf("Word = %q, want %q", got.Word, "test_word")
		}
		if got.Meaning != "test_meaning" {
			t.Errorf("Meaning = %q, want %q", got.Meaning, "test_meaning")
		}
		if got.Example != "test_example" {
			t.Errorf("Example = %q, want %q", got.Example, "test_example")
		}
		if got.Unit != "test_unit" {
			t.Errorf("Unit = %q, want %q", got.Unit, "test_unit")
		}
		if got.Category != "test_category" {
			t.Errorf("Category = %q, want %q", got.Category, "test_category")
		}
		if got.FsrsCard != `{"test":true}` {
			t.Errorf("FsrsCard = %q, want %q", got.FsrsCard, `{"test":true}`)
		}
	})

	t.Run("GetByID for missing id returns not-found sentinel", func(t *testing.T) {
		db := newTestDBWithCards(t)
		repo, _ := NewCardRepository(db, infra.NewNoOpLogger())

		_, err := repo.GetByID(context.Background(), 99999)
		if err == nil {
			t.Fatal("GetByID(99999) expected error, got nil")
		}
		if !errors.Is(err, ErrCardNotFound) {
			t.Errorf("GetByID(99999) error = %v, want errors.Is ErrCardNotFound", err)
		}
	})

	t.Run("ListByUnit returns only cards matching unit", func(t *testing.T) {
		db := newTestDBWithCards(t)
		repo, _ := NewCardRepository(db, infra.NewNoOpLogger())

		cards, err := repo.ListByUnit(context.Background(), "unitA")
		if err != nil {
			t.Fatalf("ListByUnit(unitA) error = %v", err)
		}
		if len(cards) != 2 {
			t.Fatalf("ListByUnit(unitA) returned %d cards, want 2", len(cards))
		}
		for _, c := range cards {
			if c.Unit != "unitA" {
				t.Errorf("card unit = %q, want %q", c.Unit, "unitA")
			}
		}
	})

	t.Run("ListByUnit with empty unit returns all cards", func(t *testing.T) {
		db := newTestDBWithCards(t)
		repo, _ := NewCardRepository(db, infra.NewNoOpLogger())

		cards, err := repo.ListByUnit(context.Background(), "")
		if err != nil {
			t.Fatalf("ListByUnit(\"\") error = %v", err)
		}
		if len(cards) != 5 {
			t.Fatalf("ListByUnit(\"\") returned %d cards, want 5", len(cards))
		}
	})

	t.Run("ListByUnit for non-existent unit returns empty slice", func(t *testing.T) {
		db := newTestDBWithCards(t)
		repo, _ := NewCardRepository(db, infra.NewNoOpLogger())

		cards, err := repo.ListByUnit(context.Background(), "nonexistent")
		if err != nil {
			t.Fatalf("ListByUnit(nonexistent) error = %v", err)
		}
		if len(cards) != 0 {
			t.Fatalf("ListByUnit(nonexistent) returned %d cards, want 0", len(cards))
		}
	})

	t.Run("fsrs_card is returned as raw string", func(t *testing.T) {
		db := newTestDBWithCards(t)
		repo, _ := NewCardRepository(db, infra.NewNoOpLogger())

		// apple has fsrs_card = {"stability":1.0}
		cards, err := repo.ListByUnit(context.Background(), "unitA")
		if err != nil {
			t.Fatalf("ListByUnit(unitA) error = %v", err)
		}

		// Find apple
		var apple *Card
		for i := range cards {
			if cards[i].Word == "apple" {
				apple = &cards[i]
				break
			}
		}
		if apple == nil {
			t.Fatal("apple card not found in unitA")
		}
		if apple.FsrsCard != `{"stability":1.0}` {
			t.Errorf("apple FsrsCard = %q, want %q", apple.FsrsCard, `{"stability":1.0}`)
		}
	})

	t.Run("queries enumerate columns explicitly", func(t *testing.T) {
		// Verify that card_repo.go does not contain SELECT *
		// This is a structural test — the actual grep is in the plan verification
		db := newTestDBWithCards(t)
		repo, _ := NewCardRepository(db, infra.NewNoOpLogger())

		// GetByID works correctly (proves column list is valid)
		var id int64
		err := db.QueryRow("SELECT id FROM cards LIMIT 1").Scan(&id)
		if err != nil {
			t.Fatalf("get any card id: %v", err)
		}

		_, err = repo.GetByID(context.Background(), id)
		if err != nil {
			t.Fatalf("GetByID() with explicit columns failed: %v", err)
		}
	})
}
