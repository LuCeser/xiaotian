// store_test.go — the Issue #13 passage store tests: full-text identity
// (fingerprint, never title-only), idempotent commit and scope creation
// (重复确认不复制课文、不重复建立相同范围), range/text separation (选择第
// 2、3 段不动其他段落), corrected-text isolation (原文修正不得使已有范围
// 指向错误段落), attribution upsert semantics and the Issue #4 scope
// verdict over textbook progress.
package passage

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/xiaotian/home-tutor-agent/pkg/database"
	"github.com/xiaotian/home-tutor-agent/pkg/infra"
)

// openTestDB opens the shared database.Open pool, runs passage.Migrate
// and (optionally) creates the Python-owned textbooks table the
// attribution join reads — the shape a real deployment has once any
// Python textbook command has run.
func openTestDB(t *testing.T, withTextbooks bool) *storeDB {
	t.Helper()
	db, err := database.Open(filepath.Join(t.TempDir(), "passage.db"))
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()
	if err = Migrate(ctx, db); err != nil {
		t.Fatalf("passage migrate: %v", err)
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

// newTestTextbook inserts one Python-owned textbook row and reports its
// id (the attribution join's target).
func newTestTextbook(t *testing.T, h *storeDB, lessons string, learned int) int64 {
	t.Helper()
	res, err := h.db.ExecContext(context.Background(),
		`INSERT INTO textbooks (name, category, lessons, learned_lessons) VALUES (?, ?, ?, ?)`,
		"统编版语文二年级下册", "chinese", lessons, learned)
	if err != nil {
		t.Fatalf("insert textbook: %v", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("textbook id: %v", err)
	}
	return id
}

// autumnRain is the shared fixture: a five-paragraph passage whose
// indexes the scope tests reference.
func autumnRain() Draft {
	return Draft{
		Title: "秋天的雨",
		Paragraphs: []string{
			"秋天的雨，是一把钥匙。",
			"秋天的雨，有一盒五彩缤纷的颜料。",
			"秋天的雨，藏着非常好闻的气味。",
			"秋天的雨，吹起了金色的小喇叭。",
			"秋天的雨，带给大地的是一曲丰收的歌。",
		},
	}
}

// TestMigrateIdempotent is the boot-safety probe.
func TestMigrateIdempotent(t *testing.T) {
	db, err := database.Open(filepath.Join(t.TempDir(), "passage.db"))
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

// TestCommitFullTextIdentity — the fingerprint covers the FULL text: the
// same passage re-confirmed reports existing and never inserts a second
// row; a DIFFERENT text that merely shares the title adds as its own row
// (身份不能只以标题判断).
func TestCommitFullTextIdentity(t *testing.T) {
	h := openTestDB(t, false)
	ctx := context.Background()

	first, err := h.store.Commit(ctx, CommitInput{Category: "chinese", Drafts: []Draft{autumnRain()}})
	if err != nil {
		t.Fatalf("commit: %v", err)
	}
	if len(first) != 1 || first[0].Status != StatusAdded || first[0].PassageID == 0 {
		t.Fatalf("first commit should add: %+v", first)
	}

	again, err := h.store.Commit(ctx, CommitInput{Category: "chinese", Drafts: []Draft{autumnRain()}})
	if err != nil {
		t.Fatalf("re-commit: %v", err)
	}
	if len(again) != 1 || again[0].Status != StatusExisting || again[0].PassageID != first[0].PassageID {
		t.Fatalf("re-confirm should report existing on the same row: %+v", again)
	}

	var count int
	if err = h.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM passages").Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 1 {
		t.Fatalf("same text must not duplicate the passage, got %d rows", count)
	}

	edited := autumnRain()
	edited.Paragraphs = append(edited.Paragraphs[:4], "秋天的雨，带给大地的是一曲丰收的歌，带给小朋友的是一首欢乐的歌。")
	other, err := h.store.Commit(ctx, CommitInput{Category: "chinese", Drafts: []Draft{edited}})
	if err != nil {
		t.Fatalf("commit edited: %v", err)
	}
	if other[0].Status != StatusAdded || other[0].PassageID == first[0].PassageID {
		t.Fatalf("a different text sharing the title is its own passage: %+v", other)
	}
}

// TestCommitInBatchDuplicate — the same text twice in ONE batch lands as
// one row; the second sighting reports existing on the first's id.
func TestCommitInBatchDuplicate(t *testing.T) {
	h := openTestDB(t, false)
	outcomes, err := h.store.Commit(context.Background(), CommitInput{
		Category: "chinese",
		Drafts:   []Draft{autumnRain(), autumnRain()},
	})
	if err != nil {
		t.Fatalf("commit: %v", err)
	}
	if outcomes[0].Status != StatusAdded || outcomes[1].Status != StatusExisting {
		t.Fatalf("batch duplicate should add then existing: %+v", outcomes)
	}
	if outcomes[0].PassageID != outcomes[1].PassageID {
		t.Fatalf("batch duplicate should share the row: %+v", outcomes)
	}
}

// TestCommitInvalidDraftsReportedNotFatal — an empty shell reports
// invalid per-item while the healthy sibling still lands (the poem
// commit's honest per-item receipt).
func TestCommitInvalidDraftsReportedNotFatal(t *testing.T) {
	h := openTestDB(t, false)
	outcomes, err := h.store.Commit(context.Background(), CommitInput{
		Category: "chinese",
		Drafts:   []Draft{{Title: "", Paragraphs: []string{"正文"}}, autumnRain()},
	})
	if err != nil {
		t.Fatalf("commit: %v", err)
	}
	if outcomes[0].Status != StatusInvalid || outcomes[0].PassageID != 0 {
		t.Fatalf("untitled draft should report invalid: %+v", outcomes[0])
	}
	if outcomes[1].Status != StatusAdded {
		t.Fatalf("healthy sibling should still add: %+v", outcomes[1])
	}
}

// TestCommitParagraphScopeSeparateFromText — AC 原文和练习范围分别表达:
// committing with a paragraph-2,3 range stores the range as its own row,
// leaves every paragraph in place, and re-confirming the same range never
// duplicates it (重复确认不重复建立相同范围). Adding the whole range later
// reuses the SAME passage (以后练习全文仍使用同一份原文).
func TestCommitParagraphScopeSeparateFromText(t *testing.T) {
	h := openTestDB(t, false)
	ctx := context.Background()

	scoped := autumnRain()
	scoped.Scope = &Scope{Paragraphs: []int{2, 3}}
	first, err := h.store.Commit(ctx, CommitInput{Category: "chinese", Drafts: []Draft{scoped}})
	if err != nil {
		t.Fatalf("commit: %v", err)
	}
	if first[0].ScopeStatus != StatusAdded || first[0].ScopeID == 0 {
		t.Fatalf("scope should add on first commit: %+v", first[0])
	}

	again, err := h.store.Commit(ctx, CommitInput{Category: "chinese", Drafts: []Draft{scoped}})
	if err != nil {
		t.Fatalf("re-commit: %v", err)
	}
	if again[0].Status != StatusExisting || again[0].ScopeStatus != StatusExisting || again[0].ScopeID != first[0].ScopeID {
		t.Fatalf("re-confirm should keep one passage and one range: %+v", again[0])
	}

	var scopeCount int
	if err = h.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM passage_scopes").Scan(&scopeCount); err != nil {
		t.Fatalf("count scopes: %v", err)
	}
	if scopeCount != 1 {
		t.Fatalf("duplicate confirmation must not duplicate the range, got %d", scopeCount)
	}

	rows, err := h.store.List(ctx, ListFilter{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("one passage expected, got %d", len(rows))
	}
	if len(rows[0].Paragraphs) != 5 {
		t.Fatalf("selecting paragraphs 2,3 must not delete the others, got %d paragraphs", len(rows[0].Paragraphs))
	}
	if rows[0].Paragraphs[0] != "秋天的雨，是一把钥匙。" || rows[0].Paragraphs[4] != "秋天的雨，带给大地的是一曲丰收的歌。" {
		t.Fatalf("paragraph order broken: %+v", rows[0].Paragraphs)
	}
	if len(rows[0].Scopes) != 1 || rows[0].Scopes[0].Whole || len(rows[0].Scopes[0].Paragraphs) != 2 {
		t.Fatalf("paragraph range should ride the projection: %+v", rows[0].Scopes)
	}
	if rows[0].Scopes[0].Paragraphs[0] != 2 || rows[0].Scopes[0].Paragraphs[1] != 3 {
		t.Fatalf("range indexes should be canonical 2,3: %+v", rows[0].Scopes[0])
	}

	// The whole range coexists on the same text — no second passage.
	whole, err := h.store.AddScope(ctx, rows[0].ID, Scope{Whole: true})
	if err != nil {
		t.Fatalf("add whole scope: %v", err)
	}
	if whole.Status != StatusAdded || whole.Row.ID == first[0].ScopeID {
		t.Fatalf("whole range is a different range, got the same id %d", whole.Row.ID)
	}
	var passageCount int
	if err = h.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM passages").Scan(&passageCount); err != nil {
		t.Fatalf("count passages: %v", err)
	}
	if passageCount != 1 {
		t.Fatalf("practising the whole text must reuse the same passage, got %d rows", passageCount)
	}
}

// TestScopeCanonicalIdentity — the same index set in a different order or
// with duplicates is the SAME range (existing), not a second row.
func TestScopeCanonicalIdentity(t *testing.T) {
	h := openTestDB(t, false)
	ctx := context.Background()
	first, err := h.store.Commit(ctx, CommitInput{
		Category: "chinese",
		Drafts:   []Draft{func() Draft { d := autumnRain(); d.Scope = &Scope{Paragraphs: []int{2, 3}}; return d }()},
	})
	if err != nil {
		t.Fatalf("commit: %v", err)
	}
	reordered, err := h.store.AddScope(ctx, first[0].PassageID, Scope{Paragraphs: []int{3, 2, 2}})
	if err != nil {
		t.Fatalf("add reordered: %v", err)
	}
	if reordered.Status != StatusExisting || reordered.Row.ID != first[0].ScopeID {
		t.Fatalf("[3,2,2] is the same range as [2,3], got id %d want %d", reordered.Row.ID, first[0].ScopeID)
	}
}

// TestAddScopeOutOfRange — a range may only reference REAL paragraphs of
// its own text: index 0 and index 6 on a five-paragraph passage are
// rejected with nothing written (选择范围必须指向真实段落).
func TestAddScopeOutOfRange(t *testing.T) {
	h := openTestDB(t, false)
	ctx := context.Background()
	first, err := h.store.Commit(ctx, CommitInput{Category: "chinese", Drafts: []Draft{autumnRain()}})
	if err != nil {
		t.Fatalf("commit: %v", err)
	}
	for _, indexes := range [][]int{{0}, {6}, {1, 6}, {}} {
		if _, err = h.store.AddScope(ctx, first[0].PassageID, Scope{Paragraphs: indexes}); err == nil {
			t.Fatalf("indexes %v should be out of range", indexes)
		}
	}
	var count int
	if err = h.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM passage_scopes").Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 0 {
		t.Fatalf("rejected ranges must not write, got %d rows", count)
	}
}

// TestCommitScopeOutOfRange — the commit path reports the range invalid
// per-item while the reviewed TEXT still stores (the honest two-half
// receipt; the API gate rejects this before the store ever sees it).
func TestCommitScopeOutOfRange(t *testing.T) {
	h := openTestDB(t, false)
	d := autumnRain()
	d.Scope = &Scope{Paragraphs: []int{9}}
	outcomes, err := h.store.Commit(context.Background(), CommitInput{Category: "chinese", Drafts: []Draft{d}})
	if err != nil {
		t.Fatalf("commit: %v", err)
	}
	if outcomes[0].Status != StatusAdded || outcomes[0].ScopeStatus != StatusInvalid {
		t.Fatalf("text adds, range reports invalid: %+v", outcomes[0])
	}
}

// TestCorrectedTextNeverRepointsRanges — AC 原文修正不得使已有范围指向
// 错误段落: committing a CORRECTED text (a paragraph merged away) lands
// as its own row; the original row's paragraph range still points at the
// original five paragraphs.
func TestCorrectedTextNeverRepointsRanges(t *testing.T) {
	h := openTestDB(t, false)
	ctx := context.Background()

	scoped := autumnRain()
	scoped.Scope = &Scope{Paragraphs: []int{2, 3}}
	first, err := h.store.Commit(ctx, CommitInput{Category: "chinese", Drafts: []Draft{scoped}})
	if err != nil {
		t.Fatalf("commit: %v", err)
	}

	corrected := autumnRain()
	corrected.Paragraphs = []string{
		"秋天的雨，是一把钥匙。",
		"秋天的雨，有一盒五彩缤纷的颜料。",
		"秋天的雨，藏着非常好闻的气味。秋天的雨，吹起了金色的小喇叭。",
		"秋天的雨，带给大地的是一曲丰收的歌。",
	}
	second, err := h.store.Commit(ctx, CommitInput{Category: "chinese", Drafts: []Draft{corrected}})
	if err != nil {
		t.Fatalf("commit corrected: %v", err)
	}
	if second[0].PassageID == first[0].PassageID {
		t.Fatalf("corrected text is its own row, got the same id")
	}

	rows, err := h.store.List(ctx, ListFilter{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("two texts expected, got %d", len(rows))
	}
	for _, p := range rows {
		if p.ID == first[0].PassageID {
			if len(p.Paragraphs) != 5 || len(p.Scopes) != 1 || p.Scopes[0].Paragraphs[1] != 3 {
				t.Fatalf("original row's text and range must stay untouched: %+v", p)
			}
		}
	}
}

// TestRemoveScope — removing one range never touches the passage, its
// other ranges or its attribution (范围可调整，不删原文).
func TestRemoveScope(t *testing.T) {
	h := openTestDB(t, false)
	ctx := context.Background()
	d := autumnRain()
	d.Scope = &Scope{Paragraphs: []int{1}}
	outcomes, err := h.store.Commit(ctx, CommitInput{Category: "chinese", Drafts: []Draft{d}})
	if err != nil {
		t.Fatalf("commit: %v", err)
	}
	passageID := outcomes[0].PassageID
	whole, err := h.store.AddScope(ctx, passageID, Scope{Whole: true})
	if err != nil {
		t.Fatalf("add whole: %v", err)
	}
	wholeID := whole.Row.ID

	if err = h.store.RemoveScope(ctx, passageID, outcomes[0].ScopeID); err != nil {
		t.Fatalf("remove scope: %v", err)
	}
	rows, err := h.store.List(ctx, ListFilter{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(rows) != 1 || len(rows[0].Paragraphs) != 5 {
		t.Fatalf("passage and its paragraphs must survive: %+v", rows)
	}
	if len(rows[0].Scopes) != 1 || rows[0].Scopes[0].ID != wholeID {
		t.Fatalf("only the removed range goes, the whole range stays: %+v", rows[0].Scopes)
	}

	if err = h.store.RemoveScope(ctx, passageID, outcomes[0].ScopeID); err == nil {
		t.Fatalf("removing a missing range should report ErrScopeNotFound")
	}
	if err = h.store.RemoveScope(ctx, passageID+99, wholeID); err == nil {
		t.Fatalf("a range on another passage must not be removable")
	}
}

// TestCommitStickyInclude — the re-import path refreshes the lesson but
// never clears an explicit include (导入不能清除已做的加入决定); Assign —
// the parent's explicit management action — refreshes both together.
func TestCommitStickyInclude(t *testing.T) {
	h := openTestDB(t, true)
	ctx := context.Background()
	bookID := newTestTextbook(t, h, `["第1课","第2课","第3课"]`, 1)

	d := autumnRain()
	d.Lesson = "第2课"
	if _, err := h.store.Commit(ctx, CommitInput{Category: "chinese", TextbookID: bookID, Drafts: []Draft{d}}); err != nil {
		t.Fatalf("commit: %v", err)
	}
	if err := h.store.Assign(ctx, 1, bookID, "第2课", true); err != nil {
		t.Fatalf("assign include: %v", err)
	}

	d.Lesson = "第3课" // re-import corrects the lesson
	if _, err := h.store.Commit(ctx, CommitInput{Category: "chinese", TextbookID: bookID, Drafts: []Draft{d}}); err != nil {
		t.Fatalf("re-commit: %v", err)
	}
	rows, err := h.store.List(ctx, ListFilter{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(rows) != 1 || len(rows[0].Assignments) != 1 {
		t.Fatalf("one passage with one attribution expected: %+v", rows)
	}
	a := rows[0].Assignments[0]
	if a.Lesson != "第3课" || !a.IncludeInScope {
		t.Fatalf("re-import refreshes the lesson but keeps the include: %+v", a)
	}
	if !rows[0].InScope || rows[0].ScopeReason != ScopeIncluded {
		t.Fatalf("explicit include keeps the passage in scope: %+v", rows[0])
	}

	if err = h.store.Assign(ctx, 1, bookID, "第1课", false); err != nil {
		t.Fatalf("assign exclude: %v", err)
	}
	rows, err = h.store.List(ctx, ListFilter{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if rows[0].Assignments[0].IncludeInScope || rows[0].ScopeReason != ScopeLearned {
		t.Fatalf("第1课 inside the learned prefix keeps it learned after the explicit exclude: %+v", rows[0])
	}
}

// TestAssignMissingPassage — the management path names its target.
func TestAssignMissingPassage(t *testing.T) {
	h := openTestDB(t, true)
	bookID := newTestTextbook(t, h, `["第1课"]`, 1)
	if err := h.store.Assign(context.Background(), 42, bookID, "第1课", false); err == nil {
		t.Fatalf("assigning a missing passage should fail")
	}
	if _, err := h.store.AddScope(context.Background(), 42, Scope{Whole: true}); err == nil {
		t.Fatalf("adding a range to a missing passage should fail")
	}
}

// TestScopeVerdict — the Issue #4 three-branch verdict over textbook
// progress: supplementary, included, learned (已学完第 N 课包含第 N 课)
// and progress-pending.
func TestScopeVerdict(t *testing.T) {
	h := openTestDB(t, true)
	ctx := context.Background()

	if _, err := h.store.Commit(ctx, CommitInput{
		Category: "chinese",
		Drafts:   []Draft{func() Draft { d := autumnRain(); d.Title = "雪孩子"; return d }()},
	}); err != nil {
		t.Fatalf("commit: %v", err)
	}

	rows, err := h.store.List(ctx, ListFilter{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if !rows[0].InScope || rows[0].ScopeReason != ScopeSupplementary {
		t.Fatalf("unassigned passage reads supplementary: %+v", rows[0])
	}

	bookID := newTestTextbook(t, h, `["第1课","第2课","第3课","第4课"]`, 2)
	learned, err := h.store.Commit(ctx, CommitInput{
		Category: "chinese",
		Drafts:   []Draft{func() Draft { d := autumnRain(); d.Title = "植物妈妈有办法"; d.Lesson = "第2课"; return d }()},
	})
	if err != nil {
		t.Fatalf("commit: %v", err)
	}
	if err = h.store.Assign(ctx, learned[0].PassageID, bookID, "第2课", false); err != nil {
		t.Fatalf("assign: %v", err)
	}
	pending, err := h.store.Commit(ctx, CommitInput{
		Category: "chinese",
		Drafts:   []Draft{func() Draft { d := autumnRain(); d.Title = "黄山奇石"; d.Lesson = "第4课"; return d }()},
	})
	if err != nil {
		t.Fatalf("commit: %v", err)
	}
	if err = h.store.Assign(ctx, pending[0].PassageID, bookID, "第4课", false); err != nil {
		t.Fatalf("assign: %v", err)
	}

	rows, err = h.store.List(ctx, ListFilter{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	verdicts := map[string]string{}
	for _, p := range rows {
		verdicts[p.Title] = p.ScopeReason
	}
	if verdicts["植物妈妈有办法"] != ScopeLearned || verdicts["黄山奇石"] != ScopeProgressPending {
		t.Fatalf("learned-prefix verdicts wrong: %+v", verdicts)
	}
}

// TestListFilters — category and query narrow the projection; a passage
// without ranges reads an empty array, never nil.
func TestListFilters(t *testing.T) {
	h := openTestDB(t, false)
	ctx := context.Background()
	if _, err := h.store.Commit(ctx, CommitInput{
		Category: "chinese",
		Drafts:   []Draft{autumnRain()},
	}); err != nil {
		t.Fatalf("commit: %v", err)
	}
	if _, err := h.store.Commit(ctx, CommitInput{
		Category: "english",
		Drafts:   []Draft{{Title: "The Cat", Paragraphs: []string{"The cat sat on the mat."}}},
	}); err != nil {
		t.Fatalf("commit english: %v", err)
	}

	rows, err := h.store.List(ctx, ListFilter{Category: "chinese"})
	if err != nil {
		t.Fatalf("list chinese: %v", err)
	}
	if len(rows) != 1 || rows[0].Title != "秋天的雨" {
		t.Fatalf("category filter broken: %+v", rows)
	}
	rows, err = h.store.List(ctx, ListFilter{Query: "五彩缤纷"})
	if err != nil {
		t.Fatalf("list query: %v", err)
	}
	if len(rows) != 1 || rows[0].Title != "秋天的雨" {
		t.Fatalf("paragraph text should match the query: %+v", rows)
	}
	if rows[0].Scopes == nil || rows[0].Assignments == nil {
		t.Fatalf("empty projections must be empty arrays, never nil: %+v", rows[0])
	}
}

// TestListPreTextbookDatabase — a database older than Issue #4 (no
// textbooks table) still lists passages; the attribution join degrades
// to no readable attributions (supplementary), never a failed page (the
// poem store's same degraded-read discipline).
func TestListPreTextbookDatabase(t *testing.T) {
	h := openTestDB(t, false)
	ctx := context.Background()
	if _, err := h.store.Commit(ctx, CommitInput{
		Category: "chinese",
		Drafts:   []Draft{func() Draft { d := autumnRain(); d.Lesson = "第2课"; return d }()},
	}); err != nil {
		t.Fatalf("commit: %v", err)
	}
	// An attribution row without its textbooks table — the join reads
	// nothing, the passage still renders.
	if _, err := h.db.ExecContext(ctx,
		"INSERT INTO passage_assignments (passage_id, textbook_id, lesson) VALUES (1, 7, '第2课')"); err != nil {
		t.Fatalf("insert assignment: %v", err)
	}
	rows, err := h.store.List(ctx, ListFilter{})
	if err != nil {
		t.Fatalf("list on pre-textbook database: %v", err)
	}
	if len(rows) != 1 || len(rows[0].Assignments) != 0 {
		t.Fatalf("degraded attribution expected: %+v", rows)
	}
	if rows[0].ScopeReason != ScopeSupplementary {
		t.Fatalf("unreadable attributions read supplementary, never fail the page: %+v", rows[0])
	}
}

// TestNewStoreNilGuards — D-47 discipline.
func TestNewStoreNilGuards(t *testing.T) {
	if _, err := NewStore(nil, nil); err == nil {
		t.Fatalf("nil db must fail")
	}
	db, err := database.Open(filepath.Join(t.TempDir(), "passage.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close() //nolint:errcheck // test teardown
	if _, err = NewStore(db, nil); err != nil {
		t.Fatalf("nil logger degrades to NoOp: %v", err)
	}
}
