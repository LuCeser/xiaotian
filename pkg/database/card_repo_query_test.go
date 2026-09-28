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
//
// CountByUnit coverage (Plan 09-04 Task 1 behavior block):
//   - category scoping: equal-named units across categories never merge
//     their counts under a category filter (Pitfall 7 disposition)
//   - empty category merges counts across categories by unit name
//     (documented intentional merge, D-09 naming-convention semantics)
//   - rows are ordered by unit name ascending
//   - zero rows → empty NON-NIL slice; a one-word unit reports count 1
//   - empty-string units (words added without a unit) are countable
package database

import (
	"context"
	"database/sql"
	"reflect"
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

// Expected CountByUnit shapes over newQueryTestRepo's fixtures:
//
//	english: unitA=3 (apple, banana, aardvark), unitB=2, unitC=1,
//	         unitD=3 (under_score, 100%, back\slash)
//	chinese: unitA=1 (春天)
func TestCountByUnit_CategoryScoped(t *testing.T) {
	repo, _ := newQueryTestRepo(t)

	en, err := repo.CountByUnit(context.Background(), "english")
	if err != nil {
		t.Fatalf("CountByUnit(english) error = %v", err)
	}
	wantEn := []UnitCount{
		{Unit: "unitA", Count: 3},
		{Unit: "unitB", Count: 2},
		{Unit: "unitC", Count: 1},
		{Unit: "unitD", Count: 3},
	}
	if !reflect.DeepEqual(en, wantEn) {
		t.Errorf("CountByUnit(english) = %+v, want %+v (unitA must be 3, not 4 — 春天 excluded, Pitfall 7)", en, wantEn)
	}

	zh, err := repo.CountByUnit(context.Background(), "chinese")
	if err != nil {
		t.Fatalf("CountByUnit(chinese) error = %v", err)
	}
	wantZh := []UnitCount{{Unit: "unitA", Count: 1}}
	if !reflect.DeepEqual(zh, wantZh) {
		t.Errorf("CountByUnit(chinese) = %+v, want %+v (no cross-category merge under a filter)", zh, wantZh)
	}
}

func TestCountByUnit_AllCategories(t *testing.T) {
	repo, _ := newQueryTestRepo(t)

	got, err := repo.CountByUnit(context.Background(), "")
	if err != nil {
		t.Fatalf("CountByUnit(\"\") error = %v", err)
	}
	// unitA appears ONCE with the combined english+chinese count — the
	// documented intentional merge-by-name across categories (Plan 09-04).
	want := []UnitCount{
		{Unit: "unitA", Count: 4},
		{Unit: "unitB", Count: 2},
		{Unit: "unitC", Count: 1},
		{Unit: "unitD", Count: 3},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("CountByUnit(\"\") = %+v, want %+v (unitA merged by name = 4)", got, want)
	}
}

func TestCountByUnit_Ordering(t *testing.T) {
	repo, _ := newQueryTestRepo(t)

	got, err := repo.CountByUnit(context.Background(), "")
	if err != nil {
		t.Fatalf("CountByUnit(\"\") error = %v", err)
	}
	for i := 1; i < len(got); i++ {
		if got[i-1].Unit >= got[i].Unit {
			t.Fatalf("order violated at %d: %q before %q — want ORDER BY unit ascending",
				i, got[i-1].Unit, got[i].Unit)
		}
	}
}

func TestCountByUnit_EmptyAndSingle(t *testing.T) {
	t.Run("zero rows returns empty non-nil slice", func(t *testing.T) {
		db := newTestDBWithCards(t)
		if _, err := db.Exec("DELETE FROM cards"); err != nil {
			t.Fatalf("clear cards: %v", err)
		}
		repo, _ := NewCardRepository(db, infra.NewNoOpLogger())

		got, err := repo.CountByUnit(context.Background(), "")
		if err != nil {
			t.Fatalf("CountByUnit(\"\") error = %v", err)
		}
		if got == nil {
			t.Fatal("CountByUnit on empty table returned nil, want empty non-nil slice")
		}
		if len(got) != 0 {
			t.Fatalf("CountByUnit on empty table returned %d rows, want 0", len(got))
		}
	})

	t.Run("one-word unit reports count 1", func(t *testing.T) {
		db := newTestDBWithCards(t)
		if _, err := db.Exec("DELETE FROM cards"); err != nil {
			t.Fatalf("clear cards: %v", err)
		}
		if _, err := db.Exec(
			"INSERT INTO cards (word, meaning, example, unit, category, fsrs_card) VALUES (?, ?, '', ?, ?, '')",
			"solo", "唯一的词", "unitX", "english",
		); err != nil {
			t.Fatalf("seed solo card: %v", err)
		}
		repo, _ := NewCardRepository(db, infra.NewNoOpLogger())

		got, err := repo.CountByUnit(context.Background(), "english")
		if err != nil {
			t.Fatalf("CountByUnit(english) error = %v", err)
		}
		want := []UnitCount{{Unit: "unitX", Count: 1}}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("CountByUnit(english) = %+v, want %+v (one-word unit counts 1)", got, want)
		}
	})
}

func TestCountByUnit_EmptyStringUnit(t *testing.T) {
	db := newTestDBWithCards(t)
	// Two words added without a unit (unit = ''). UNIQUE(word, category)
	// requires distinct words.
	for _, w := range []string{"orphan1", "orphan2"} {
		if _, err := db.Exec(
			"INSERT INTO cards (word, meaning, example, unit, category, fsrs_card) VALUES (?, ?, '', ?, ?, '')",
			w, "无单元词", "", "english",
		); err != nil {
			t.Fatalf("seed %q: %v", w, err)
		}
	}
	repo, _ := NewCardRepository(db, infra.NewNoOpLogger())

	got, err := repo.CountByUnit(context.Background(), "english")
	if err != nil {
		t.Fatalf("CountByUnit(english) error = %v", err)
	}
	// The empty-string unit row must exist with BOTH orphan words counted
	// (countable, not dropped). ORDER BY unit puts "" first — it is the
	// lexicographic minimum.
	want := []UnitCount{
		{Unit: "", Count: 2},
		{Unit: "unitA", Count: 2},
		{Unit: "unitB", Count: 2},
		{Unit: "unitC", Count: 1},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("CountByUnit(english) = %+v, want %+v (empty-string unit groups both orphans)", got, want)
	}
}
