// card_repo_query_test.go exercises CardRepository.List (Phase 9, D-12)
// against in-memory SQLite, reusing card_repo_test.go's seed fixtures.
//
// Coverage (Plan 09-01 Task 2 behavior block):
//   - category filter excludes the other category
//   - unit filter (exact match)
//   - q matches word OR meaning
//   - q containing %, _ or the escape char is treated literally
//     (RESEARCH Pitfall 6)
//   - english+chinese rows sharing one unit name are distinguishable via
//     the category filter (RESEARCH Pitfall 7)
//   - result order is (unit, id)-stable
package database

import (
	"context"
	"database/sql"
	"testing"

	"github.com/xiaotian/home-tutor-agent/pkg/infra"
)

// seedQueryFixtures inserts the Pitfall 6/7 fixtures on top of
// seedCardsTable's five english cards: a chinese card sharing unitA's name
// and cards carrying LIKE metacharacters in word/meaning.
func seedQueryFixtures(t *testing.T, db *sql.DB) {
	t.Helper()
	fixtures := []struct {
		word, meaning, unit, category string
	}{
		// Pitfall 7: same unit name, different category.
		{"春天", "spring season", "unitA", "chinese"},
		// Pitfall 6: literal LIKE metacharacters in data.
		{"under_score", "underscore word", "unitD", "english"},
		{"100%", "percent word", "unitD", "english"},
		{"back\\slash", "escape char word", "unitD", "english"},
		// For order assertion: unitB already has cat(id3) and dog(id4) from
		// seedCardsTable; this appends one more english row in unitA.
		{"aardvark", "第一只动物", "unitA", "english"},
	}
	for _, f := range fixtures {
		if _, err := db.Exec(
			"INSERT INTO cards (word, meaning, example, unit, category, fsrs_card) VALUES (?, ?, '', ?, ?, '')",
			f.word, f.meaning, f.unit, f.category,
		); err != nil {
			t.Fatalf("seed query fixture %q: %v", f.word, err)
		}
	}
}

func newQueryTestRepo(t *testing.T) (CardRepository, *sql.DB) {
	t.Helper()
	db := newTestDBWithCards(t)
	seedQueryFixtures(t, db)
	repo, err := NewCardRepository(db, infra.NewNoOpLogger())
	if err != nil {
		t.Fatalf("NewCardRepository: %v", err)
	}
	return repo, db
}

func TestCardRepoList(t *testing.T) {
	t.Run("category filter excludes the other category", func(t *testing.T) {
		repo, _ := newQueryTestRepo(t)
		cards, err := repo.List(context.Background(), CardFilter{Category: "chinese"})
		if err != nil {
			t.Fatalf("List(chinese) error = %v", err)
		}
		if len(cards) != 1 {
			t.Fatalf("List(chinese) returned %d cards, want 1", len(cards))
		}
		if cards[0].Word != "春天" || cards[0].Category != "chinese" {
			t.Errorf("card = %+v, want the seeded chinese card", cards[0])
		}
	})

	t.Run("unit filter is exact match", func(t *testing.T) {
		repo, _ := newQueryTestRepo(t)
		cards, err := repo.List(context.Background(), CardFilter{Unit: "unitC"})
		if err != nil {
			t.Fatalf("List(unitC) error = %v", err)
		}
		if len(cards) != 1 {
			t.Fatalf("List(unitC) returned %d cards, want 1", len(cards))
		}
		if cards[0].Word != "elephant" {
			t.Errorf("card word = %q, want elephant", cards[0].Word)
		}
	})

	t.Run("q matches word", func(t *testing.T) {
		repo, _ := newQueryTestRepo(t)
		cards, err := repo.List(context.Background(), CardFilter{Query: "eleph"})
		if err != nil {
			t.Fatalf("List(eleph) error = %v", err)
		}
		if len(cards) != 1 || cards[0].Word != "elephant" {
			t.Fatalf("List(eleph) = %+v, want exactly elephant", cards)
		}
	})

	t.Run("q matches meaning", func(t *testing.T) {
		repo, _ := newQueryTestRepo(t)
		cards, err := repo.List(context.Background(), CardFilter{Query: "大象"})
		if err != nil {
			t.Fatalf("List(大象) error = %v", err)
		}
		if len(cards) != 1 || cards[0].Word != "elephant" {
			t.Fatalf("List(大象) = %+v, want exactly elephant (meaning match)", cards)
		}
	})

	t.Run("underscore in q is literal, not a wildcard (Pitfall 6)", func(t *testing.T) {
		repo, _ := newQueryTestRepo(t)
		// "r_" as a raw LIKE pattern would match "apple"/"elephant" (any r
		// followed by any char); escaped, it must match ONLY under_score.
		cards, err := repo.List(context.Background(), CardFilter{Query: "r_"})
		if err != nil {
			t.Fatalf("List(r_) error = %v", err)
		}
		if len(cards) != 1 || cards[0].Word != "under_score" {
			t.Fatalf("List(r_) = %+v, want exactly under_score (literal underscore)", cards)
		}
	})

	t.Run("percent in q is literal (Pitfall 6)", func(t *testing.T) {
		repo, _ := newQueryTestRepo(t)
		cards, err := repo.List(context.Background(), CardFilter{Query: "00%"})
		if err != nil {
			t.Fatalf("List(00%%) error = %v", err)
		}
		if len(cards) != 1 || cards[0].Word != "100%" {
			t.Fatalf("List(00%%) = %+v, want exactly 100%% (literal percent)", cards)
		}
	})

	t.Run("escape char in q is literal (Pitfall 6)", func(t *testing.T) {
		repo, _ := newQueryTestRepo(t)
		cards, err := repo.List(context.Background(), CardFilter{Query: `back\sl`})
		if err != nil {
			t.Fatalf("List(back\\sl) error = %v", err)
		}
		if len(cards) != 1 || cards[0].Word != `back\slash` {
			t.Fatalf("List(back\\sl) = %+v, want exactly back\\slash (literal backslash)", cards)
		}
	})

	t.Run("shared unit name separated by category filter (Pitfall 7)", func(t *testing.T) {
		repo, _ := newQueryTestRepo(t)

		all, err := repo.List(context.Background(), CardFilter{Unit: "unitA"})
		if err != nil {
			t.Fatalf("List(unitA) error = %v", err)
		}
		// unitA holds: apple, banana (seed) + aardvark (english) + 春天 (chinese).
		if len(all) != 4 {
			t.Fatalf("List(unitA) returned %d cards, want 4", len(all))
		}

		en, err := repo.List(context.Background(), CardFilter{Category: "english", Unit: "unitA"})
		if err != nil {
			t.Fatalf("List(english, unitA) error = %v", err)
		}
		if len(en) != 3 {
			t.Fatalf("List(english, unitA) returned %d cards, want 3", len(en))
		}
		for _, c := range en {
			if c.Category != "english" {
				t.Errorf("card %q has category %q, want english", c.Word, c.Category)
			}
		}
	})

	t.Run("result order is (unit, id)-stable", func(t *testing.T) {
		repo, _ := newQueryTestRepo(t)
		cards, err := repo.List(context.Background(), CardFilter{})
		if err != nil {
			t.Fatalf("List(empty) error = %v", err)
		}
		if len(cards) != 10 {
			t.Fatalf("List(empty) returned %d cards, want 10 (5 seed + 5 fixtures)", len(cards))
		}
		for i := 1; i < len(cards); i++ {
			prev, cur := cards[i-1], cards[i]
			if prev.Unit > cur.Unit || (prev.Unit == cur.Unit && prev.ID > cur.ID) {
				t.Fatalf("order violated at %d: (%s,#%d) before (%s,#%d) — want ORDER BY unit, id",
					i, prev.Unit, prev.ID, cur.Unit, cur.ID)
			}
		}
	})

	t.Run("combined filters AND together", func(t *testing.T) {
		repo, _ := newQueryTestRepo(t)
		cards, err := repo.List(context.Background(), CardFilter{
			Category: "english", Unit: "unitD", Query: "score",
		})
		if err != nil {
			t.Fatalf("List combined error = %v", err)
		}
		if len(cards) != 1 || cards[0].Word != "under_score" {
			t.Fatalf("List combined = %+v, want exactly under_score", cards)
		}
	})
}
