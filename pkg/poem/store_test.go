// store_test.go — the Issue #11 poem store tests: whole-work identity
// (fingerprint, never title-only), idempotent commit (重复确认不复制作品),
// attribution upsert semantics (sticky include on re-import, explicit
// refresh on Assign) and the Issue #4 scope verdict over textbook progress.
package poem

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"

	"github.com/xiaotian/home-tutor-agent/pkg/database"
	"github.com/xiaotian/home-tutor-agent/pkg/infra"
)

// openTestDB opens the shared database.Open pool, runs poem.Migrate and
// (optionally) creates the Python-owned textbooks table the attribution
// join reads — the shape a real deployment has once any Python textbook
// command has run.
func openTestDB(t *testing.T, withTextbooks bool) *storeDB {
	t.Helper()
	db, err := database.Open(filepath.Join(t.TempDir(), "poem.db"))
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()
	if err = Migrate(ctx, db); err != nil {
		t.Fatalf("poem migrate: %v", err)
	}
	if withTextbooks {
		// The Python-owned shape (vocab_manager.py): lessons is an explicit
		// JSON array, learned_lessons the count of its prefix that counts
		// as 已学完.
		if _, err = db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS textbooks (
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
			created_at TEXT NOT NULL DEFAULT (datetime('now'))
		)`); err != nil {
			t.Fatalf("create textbooks: %v", err)
		}
	}
	store, err := NewStore(db, infra.NewNoOpLogger())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	return &storeDB{db: db, store: store}
}

// storeDB bundles the open pool and the store under test.
type storeDB struct {
	db    *sql.DB
	store *Store
}

// TestMigrateIdempotent is the boot-safety probe.
func TestMigrateIdempotent(t *testing.T) {
	db, err := database.Open(filepath.Join(t.TempDir(), "poem.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close() //nolint:errcheck // test teardown
	ctx := context.Background()
	if err = Migrate(ctx, db); err != nil {
		t.Fatalf("first migrate: %v", err)
	}
	if err = Migrate(ctx, db); err != nil {
		t.Fatalf("second migrate: %v", err)
	}
}

// TestCommitWholeWorkIdentity — the fingerprint covers the FULL work: the
// same poem re-confirmed reports existing and never inserts a second row;
// a DIFFERENT poem that merely shares the title adds as its own work
// (身份不能只以标题判断).
func TestCommitWholeWorkIdentity(t *testing.T) {
	h := openTestDB(t, false)
	ctx := context.Background()

	draft := Draft{Title: "咏柳", Author: "贺知章", Lines: []string{"碧玉妆成一树高", "万条垂下绿丝绦"}}
	first, err := h.store.Commit(ctx, CommitInput{Drafts: []Draft{draft}, Category: "chinese"})
	if err != nil {
		t.Fatalf("first commit: %v", err)
	}
	if len(first) != 1 || first[0].Status != StatusAdded || first[0].PoemID == 0 {
		t.Fatalf("first outcome = %+v, want added with id", first)
	}

	// Re-confirm the SAME work (author present, lines re-trimmed): existing,
	// same row id, no copy.
	again, err := h.store.Commit(ctx, CommitInput{Drafts: []Draft{
		{Title: " 咏柳 ", Author: "贺知章", Lines: []string{"碧玉妆成一树高", "", "万条垂下绿丝绦"}},
	}, Category: "chinese"})
	if err != nil {
		t.Fatalf("re-commit: %v", err)
	}
	if len(again) != 1 || again[0].Status != StatusExisting || again[0].PoemID != first[0].PoemID {
		t.Fatalf("re-commit outcome = %+v, want existing with the same id", again)
	}

	// Same TITLE, different text: a different work, its own row.
	other, err := h.store.Commit(ctx, CommitInput{Drafts: []Draft{
		{Title: "咏柳", Author: "别人", Lines: []string{"完全不同的诗句"}},
	}, Category: "chinese"})
	if err != nil {
		t.Fatalf("same-title commit: %v", err)
	}
	if len(other) != 1 || other[0].Status != StatusAdded || other[0].PoemID == first[0].PoemID {
		t.Fatalf("same-title outcome = %+v, want added as a separate work", other)
	}

	// Two rows total, ever.
	var count int
	if err = h.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM poems").Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 2 {
		t.Errorf("poems rows = %d, want 2 (one per distinct work)", count)
	}
}

// TestCommitInBatchDuplicate — the same fingerprint twice inside ONE commit
// resolves to the first sighting: added then existing, one row.
func TestCommitInBatchDuplicate(t *testing.T) {
	h := openTestDB(t, false)
	ctx := context.Background()
	outcomes, err := h.store.Commit(ctx, CommitInput{
		Category: "chinese",
		Drafts: []Draft{
			{Title: "春晓", Author: "孟浩然", Lines: []string{"春眠不觉晓", "处处闻啼鸟"}},
			{Title: "春晓", Author: "孟浩然", Lines: []string{"春眠不觉晓", "处处闻啼鸟"}},
		},
	})
	if err != nil {
		t.Fatalf("commit: %v", err)
	}
	if len(outcomes) != 2 || outcomes[0].Status != StatusAdded || outcomes[1].Status != StatusExisting {
		t.Fatalf("outcomes = %+v, want added then existing", outcomes)
	}
	if outcomes[0].PoemID != outcomes[1].PoemID {
		t.Errorf("poem ids %d vs %d, want the same row", outcomes[0].PoemID, outcomes[1].PoemID)
	}
}

// TestCommitInvalidDraftsReportedNotFatal — a draft without a title or
// without lines reports invalid per-item while its siblings still land
// (逐项返回实际处理结果, never a blanket failure hiding the real outcomes).
func TestCommitInvalidDraftsReportedNotFatal(t *testing.T) {
	h := openTestDB(t, false)
	ctx := context.Background()
	outcomes, err := h.store.Commit(ctx, CommitInput{
		Category: "chinese",
		Drafts: []Draft{
			{Title: "", Author: "无名", Lines: []string{"没有题目"}},
			{Title: "登鹳雀楼", Author: "王之涣", Lines: []string{"白日依山尽", "黄河入海流"}},
			{Title: "空壳", Lines: nil},
		},
	})
	if err != nil {
		t.Fatalf("commit: %v", err)
	}
	if len(outcomes) != 3 {
		t.Fatalf("outcomes len = %d, want 3", len(outcomes))
	}
	if outcomes[0].Status != StatusInvalid || outcomes[2].Status != StatusInvalid {
		t.Errorf("invalid statuses = %q/%q, want invalid/invalid", outcomes[0].Status, outcomes[2].Status)
	}
	if outcomes[1].Status != StatusAdded {
		t.Errorf("middle outcome = %+v, want the valid poem added", outcomes[1])
	}
}

// TestCommitStickyInclude — a re-import refreshes the attribution's lesson
// but NEVER clears a parent's explicit include_in_scope (the word path's
// sticky-include rule), while Assign — the explicit management action —
// refreshes both.
func TestCommitStickyInclude(t *testing.T) {
	h := openTestDB(t, true)
	ctx := context.Background()
	if _, err := h.db.ExecContext(ctx,
		`INSERT INTO textbooks (name, category, lessons, learned_lessons) VALUES ('语文读本', 'chinese', '["第1课","第2课","第3课"]', 1)`); err != nil {
		t.Fatalf("seed textbook: %v", err)
	}
	var bookID int64
	if err := h.db.QueryRowContext(ctx, "SELECT id FROM textbooks").Scan(&bookID); err != nil {
		t.Fatalf("book id: %v", err)
	}

	draft := Draft{Title: "静夜思", Author: "李白", Lines: []string{"床前明月光"}, Lesson: "第2课"}
	if _, err := h.store.Commit(ctx, CommitInput{Drafts: []Draft{draft}, Category: "chinese", TextbookID: bookID}); err != nil {
		t.Fatalf("commit: %v", err)
	}

	// The parent explicitly includes the poem (the management action).
	poems, err := h.store.List(ctx, ListFilter{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if err := h.store.Assign(ctx, poems[0].ID, bookID, "第2课", true); err != nil {
		t.Fatalf("assign include: %v", err)
	}

	// Re-import the same poem under the same book with a corrected lesson:
	// existing work, lesson refreshes, include STAYS set.
	if _, err := h.store.Commit(ctx, CommitInput{Drafts: []Draft{draft}, Category: "chinese", TextbookID: bookID}); err != nil {
		t.Fatalf("re-commit: %v", err)
	}
	poems, err = h.store.List(ctx, ListFilter{})
	if err != nil {
		t.Fatalf("list after re-commit: %v", err)
	}
	if len(poems) != 1 {
		t.Fatalf("poems len = %d, want 1 (no copy)", len(poems))
	}
	if len(poems[0].Assignments) != 1 {
		t.Fatalf("assignments = %+v, want one", poems[0].Assignments)
	}
	a := poems[0].Assignments[0]
	if !a.IncludeInScope {
		t.Error("include_in_scope was cleared by the re-import; want sticky")
	}
	if a.Lesson != "第2课" {
		t.Errorf("lesson = %q, want 第2课", a.Lesson)
	}
	if poems[0].ScopeReason != ScopeIncluded || !poems[0].InScope {
		t.Errorf("scope = %v/%q, want in scope via explicit include", poems[0].InScope, poems[0].ScopeReason)
	}
}

// TestScopeVerdict — the Issue #4 rules govern poems through the textbook
// join: supplementary (unassigned) in scope; a lesson inside the learned
// prefix in scope (已学完第 N 课包含第 N 课); a lesson beyond it stored but
// out of scope; an explicit include overrides everything.
func TestScopeVerdict(t *testing.T) {
	h := openTestDB(t, true)
	ctx := context.Background()
	if _, err := h.db.ExecContext(ctx,
		`INSERT INTO textbooks (name, category, lessons, learned_lessons) VALUES ('统编语文', 'chinese', '["第1课","第8课","第9课"]', 2)`); err != nil {
		t.Fatalf("seed textbook: %v", err)
	}
	var bookID int64
	if err := h.db.QueryRowContext(ctx, "SELECT id FROM textbooks").Scan(&bookID); err != nil {
		t.Fatalf("book id: %v", err)
	}

	mk := func(title, lesson string, lines ...string) Draft {
		return Draft{Title: title, Author: "佚名", Lines: lines, Lesson: lesson}
	}
	if _, err := h.store.Commit(ctx, CommitInput{
		Category:   "chinese",
		TextbookID: bookID,
		Drafts: []Draft{
			mk("已学诗", "第8课", "第一行"), // inside the learned prefix (第1、8课)
			mk("未学诗", "第9课", "第二行"), // beyond it
			mk("待确认诗", "", "第三行"),   // lesson-less
		},
	}); err != nil {
		t.Fatalf("commit: %v", err)
	}
	// An unassigned supplementary poem.
	if _, err := h.store.Commit(ctx, CommitInput{
		Category: "chinese",
		Drafts:   []Draft{mk("课外诗", "", "第四行")},
	}); err != nil {
		t.Fatalf("commit supplementary: %v", err)
	}

	poems, err := h.store.List(ctx, ListFilter{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	byTitle := map[string]Poem{}
	for _, p := range poems {
		byTitle[p.Title] = p
	}
	if p := byTitle["已学诗"]; !p.InScope || p.ScopeReason != ScopeLearned {
		t.Errorf("已学诗 scope = %v/%q, want learned", p.InScope, p.ScopeReason)
	}
	if p := byTitle["未学诗"]; p.InScope || p.ScopeReason != ScopeProgressPending {
		t.Errorf("未学诗 scope = %v/%q, want progress-pending (stored, out of scope)", p.InScope, p.ScopeReason)
	}
	if p := byTitle["待确认诗"]; p.InScope || p.ScopeReason != ScopeProgressPending {
		t.Errorf("待确认诗 scope = %v/%q, want progress-pending (lesson-less assignment)", p.InScope, p.ScopeReason)
	}
	if p := byTitle["课外诗"]; !p.InScope || p.ScopeReason != ScopeSupplementary {
		t.Errorf("课外诗 scope = %v/%q, want supplementary", p.InScope, p.ScopeReason)
	}

	// The parent moves progress to 第9课 — the once-pending poem enters the
	// scope without any re-import (范围随进度生效).
	if _, err := h.db.ExecContext(ctx, "UPDATE textbooks SET learned_lessons = 3"); err != nil {
		t.Fatalf("advance progress: %v", err)
	}
	poems, err = h.store.List(ctx, ListFilter{})
	if err != nil {
		t.Fatalf("list after progress: %v", err)
	}
	for _, p := range poems {
		if p.Title == "未学诗" {
			if !p.InScope || p.ScopeReason != ScopeLearned {
				t.Errorf("未学诗 scope after progress = %v/%q, want learned", p.InScope, p.ScopeReason)
			}
		}
	}
}

// TestAssignMissingPoem — the management path names its target honestly:
// an unknown poem id is ErrPoemNotFound, nothing written.
func TestAssignMissingPoem(t *testing.T) {
	h := openTestDB(t, true)
	ctx := context.Background()
	if _, err := h.db.ExecContext(ctx,
		`INSERT INTO textbooks (name, category) VALUES ('书', 'chinese')`); err != nil {
		t.Fatalf("seed textbook: %v", err)
	}
	var bookID int64
	if err := h.db.QueryRowContext(ctx, "SELECT id FROM textbooks").Scan(&bookID); err != nil {
		t.Fatalf("book id: %v", err)
	}
	err := h.store.Assign(ctx, 999, bookID, "第1课", false)
	if !errors.Is(err, ErrPoemNotFound) {
		t.Fatalf("Assign unknown poem: err = %v, want ErrPoemNotFound", err)
	}
}

// TestListPreTextbookDatabase — on a database with no textbooks table at
// all (pre-Issue-#4 shape), poems still list and every one reads
// supplementary: the degraded read, never a failed page.
func TestListPreTextbookDatabase(t *testing.T) {
	h := openTestDB(t, false)
	ctx := context.Background()
	if _, err := h.store.Commit(ctx, CommitInput{
		Category: "chinese",
		Drafts:   []Draft{{Title: "悯农", Author: "李绅", Lines: []string{"锄禾日当午"}}},
	}); err != nil {
		t.Fatalf("commit: %v", err)
	}
	poems, err := h.store.List(ctx, ListFilter{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(poems) != 1 || !poems[0].InScope || poems[0].ScopeReason != ScopeSupplementary {
		t.Fatalf("poems = %+v, want one supplementary poem", poems)
	}
}

// TestListFilters — category and query filters narrow the page; the query
// matches title, author AND line text (a poem is findable by any verse).
func TestListFilters(t *testing.T) {
	h := openTestDB(t, false)
	ctx := context.Background()
	if _, err := h.store.Commit(ctx, CommitInput{
		Category: "chinese",
		Drafts: []Draft{
			{Title: "静夜思", Author: "李白", Lines: []string{"床前明月光"}},
			{Title: "春晓", Author: "孟浩然", Lines: []string{"处处闻啼鸟"}},
		},
	}); err != nil {
		t.Fatalf("commit: %v", err)
	}
	byTitle, err := h.store.List(ctx, ListFilter{Query: "静夜思"})
	if err != nil || len(byTitle) != 1 {
		t.Fatalf("query by title = %v (err %v), want 1", byTitle, err)
	}
	byAuthor, err := h.store.List(ctx, ListFilter{Query: "李白"})
	if err != nil || len(byAuthor) != 1 {
		t.Fatalf("query by author = %v (err %v), want 1", byAuthor, err)
	}
	byLine, err := h.store.List(ctx, ListFilter{Query: "闻啼鸟"})
	if err != nil || len(byLine) != 1 || byLine[0].Title != "春晓" {
		t.Fatalf("query by line = %v (err %v), want 春晓", byLine, err)
	}
	byCategory, err := h.store.List(ctx, ListFilter{Category: "english"})
	if err != nil || len(byCategory) != 0 {
		t.Fatalf("category filter = %v (err %v), want empty", byCategory, err)
	}
}
