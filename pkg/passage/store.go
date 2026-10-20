// Package passage stores 课文 as structured original texts (Issue #13):
// a 语文 mixed-page import recognizes 词语 entries (Python-owned cards),
// 整首古诗 (pkg/poem) and now whole PASSAGES — a passage is ONE unit:
// title, optional author, and the ordered 自然段 (paragraphs), with the
// paragraph boundaries the parent verified. 段落边界和顺序保留, and the
// text is stored verbatim — never regenerated or completed by a model.
//
// Ownership (the pkg/lesson and pkg/poem precedent): passages,
// passage_assignments and passage_scopes are GO-OWNED tables. The
// write-separation contract (D-03-a) assigns cards and card_assignments to
// Python; a passage is not a card (no FSRS state, no unit column), so its
// tables belong to the Go service exactly like poems. The Python-owned
// textbooks table stays untouched: an assignment REFERENCES a textbook row
// by id and the join reads its name/lessons/learned_lessons, so the Issue
// #4 progress rules govern passages without Python learning about them.
//
// Identity (the poem fingerprint discipline): a passage's fingerprint
// hashes the NORMALIZED full text — trimmed title, trimmed author, every
// trimmed non-empty paragraph in order. Re-confirming the same passage
// resolves to the stored row and reports existing; a DIFFERENT text that
// merely shares a title gets its own row. A row's text never mutates in
// place, which is what keeps paragraph INDEXES stable: a corrected
// original (a different fingerprint, its own row) can never silently
// re-point an existing practice range at shifted paragraphs (原文修正不得
// 使已有范围指向错误段落).
//
// Practice ranges (练习范围, AC 原文和练习范围分别表达): a range is a
// SEPARATE passage_scopes row — whole (整篇) or an explicit paragraph set
// (指定自然段, 1-based). Selecting paragraphs 2 and 3 neither deletes the
// other paragraphs nor copies the passage; adding the whole range later
// reuses the SAME stored text (以后练习全文仍使用同一份原文). Range
// identity is (passage, whole, canonical paragraph set): re-confirming
// the same selection never duplicates the row (重复确认不重复建立相同
// 范围).
package passage

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/pkg/errors"

	"github.com/xiaotian/home-tutor-agent/pkg/infra"
)

// Sentinel errors. Callers MUST branch via errors.Is (D-05).
var (
	// ErrPassageNotFound is returned by Assign, AddScope and RemoveScope
	// when no passages row carries the given id.
	ErrPassageNotFound = errors.New("passage: passage not found")

	// ErrScopeNotFound is returned by RemoveScope when the scope id does
	// not exist on the given passage.
	ErrScopeNotFound = errors.New("passage: scope not found")

	// ErrInvalidPassage is returned when caller-supplied arguments are
	// invalid (a nil store input, a commit without drafts). Nothing is
	// written when it is returned.
	ErrInvalidPassage = errors.New("passage: invalid passage arguments")

	// ErrScopeOutOfRange is returned by AddScope when a paragraph index
	// falls outside the stored passage's paragraphs (a range must point at
	// REAL paragraphs of its own text).
	ErrScopeOutOfRange = errors.New("passage: scope paragraphs out of range")
)

// Table DDL — Go-owned, registered by Migrate (the database/lesson/poem
// package CREATE TABLE IF NOT EXISTS pattern; idempotent on every boot).
var (
	passagesDDL = `CREATE TABLE IF NOT EXISTS passages (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    title TEXT NOT NULL,
    author TEXT NOT NULL DEFAULT '',
    paragraphs TEXT NOT NULL,           -- JSON array, 自然段 order
    fingerprint TEXT NOT NULL,          -- normalized full-text identity
    category TEXT NOT NULL DEFAULT 'chinese',
    created_at TEXT NOT NULL DEFAULT (datetime('now'))
)`

	idxPassagesFingerprint = `CREATE UNIQUE INDEX IF NOT EXISTS idx_passages_fingerprint ON passages(fingerprint)`

	passageAssignmentsDDL = `CREATE TABLE IF NOT EXISTS passage_assignments (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    passage_id INTEGER NOT NULL,
    textbook_id INTEGER NOT NULL,
    lesson TEXT NOT NULL DEFAULT '',
    include_in_scope INTEGER NOT NULL DEFAULT 0,
    created_at TEXT NOT NULL DEFAULT (datetime('now')),
    UNIQUE(passage_id, textbook_id)
)`

	idxPassageAssignmentsTextbook = `CREATE INDEX IF NOT EXISTS idx_passage_assignments_textbook ON passage_assignments(textbook_id)` //nolint:gosec // G101 false positive: "passage" merely contains "pass"; a DDL index statement, never a credential

	passageScopesDDL = `CREATE TABLE IF NOT EXISTS passage_scopes (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    passage_id INTEGER NOT NULL,
    whole INTEGER NOT NULL DEFAULT 0,   -- 1 = 整篇; 0 = paragraph set below
    paragraphs TEXT NOT NULL,           -- canonical JSON array; "[]" when whole
    created_at TEXT NOT NULL DEFAULT (datetime('now')),
    UNIQUE(passage_id, whole, paragraphs)
)`

	idxPassageScopesPassage = `CREATE INDEX IF NOT EXISTS idx_passage_scopes_passage ON passage_scopes(passage_id)` //nolint:gosec // G101 false positive: "passage" merely contains "pass"; a DDL index statement, never a credential
)

// Migrate creates the passage package's Go-owned tables and indexes if they
// do not exist. Idempotent: safe to call on every boot, mirroring
// lesson.Migrate and poem.Migrate (which main.go calls right after
// database.Migrate).
func Migrate(ctx context.Context, db *sql.DB) error {
	for _, stmt := range []string{
		passagesDDL, idxPassagesFingerprint,
		passageAssignmentsDDL, idxPassageAssignmentsTextbook,
		passageScopesDDL, idxPassageScopesPassage,
		passagePracticeDDL, idxPassagePracticeRange, // Issue #14: per-range 背默 records + scheduling
		passageReviewScheduleDDL,
	} {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("passage/migrate: %w", err)
		}
	}
	return nil
}

// Assignment is one passage's textbook attribution (the passage twin of
// database.TextbookAssignment and poem.Assignment): the textbook's name
// rides along so the content view renders chips without a second join
// client-side.
type Assignment struct {
	TextbookName   string `json:"name"`
	Lesson         string `json:"lesson"`
	TextbookID     int64  `json:"id"`
	IncludeInScope bool   `json:"includeInScope"`
}

// Scope-reason values — why a passage is (or is not) inside the practice
// scope under the Issue #4 rules. Frozen strings: the parent content view
// reads them. The verdict is display/management state only — the passage
// CLASSROOM lands with a later task (#14); passages never enter the
// word-dictation selection (they are not cards at all).
const (
	// ScopeSupplementary: no attribution — supplementary content, in scope
	// by default (the 补充词汇默认参与 rule; for passages the flag only
	// gates the future passage classroom).
	ScopeSupplementary = "supplementary"
	// ScopeIncluded: an attribution carries the parent's explicit
	// include_in_scope override (明确加入默写范围).
	ScopeIncluded = "included"
	// ScopeLearned: an attribution's lesson sits inside its textbook's
	// learned-lessons prefix (已学完第 N 课包含第 N 课).
	ScopeLearned = "learned"
	// ScopeProgressPending: every attribution is either lesson-less or
	// beyond its textbook's learned progress — stored fine, out of scope
	// until the progress moves (or the parent includes it explicitly).
	ScopeProgressPending = "progress_pending"
)

// Scope is one practice range as the caller states it (练习范围): whole
// (整篇) or an explicit 1-based paragraph set (指定自然段). Whole and a
// paragraph set are two DIFFERENT ranges and may coexist on one passage.
type Scope struct {
	// Paragraphs carries the 1-based 自然段 indexes, canonically sorted
	// and deduplicated before any comparison or write. It is meaningful
	// only when Whole is false (a whole range stores "[]").
	Paragraphs []int
	Whole      bool
}

// ScopeRow is one STORED practice range with its identity — the shape the
// content view renders and the mastery records (#14) reference. The practice
// projection (Issue #14) is PER RANGE: HasPractice/LastMastered/
// NextReviewDay describe THIS range's own history only — 第 2 段掌握 never
// reads as the whole passage's mastery, and a whole range starts unpracticed
// however often its paragraphs were practiced.
type ScopeRow struct {
	CreatedAt     string `json:"created_at"`
	NextReviewDay string `json:"next_review_day,omitempty"`
	Paragraphs    []int  `json:"paragraphs"`
	ID            int64  `json:"id"`
	Whole         bool   `json:"whole"`
	HasPractice   bool   `json:"has_practice"`
	LastMastered  bool   `json:"last_mastered"`
}

// Passage is one stored text plus its management projection: the ordered
// paragraphs decoded from the JSON column, the attribution rows, the
// practice ranges, and the scope verdict computed under the Issue #4
// rules (练习目标与教材进度可共同查询).
type Passage struct {
	Title       string
	Author      string
	Category    string
	CreatedAt   string
	Fingerprint string
	ScopeReason string
	Paragraphs  []string
	Assignments []Assignment
	Scopes      []ScopeRow
	ID          int64
	InScope     bool
}

// Draft is one confirmed passage on the commit path — exactly what the
// parent reviewed in the preview (title/author edited, paragraphs in
// order, the per-passage lesson) plus the optional practice range chosen
// in the same preview (nil = store the text only; the parent picks a
// range later on the content page).
type Draft struct {
	Title      string
	Author     string
	Lesson     string
	Scope      *Scope
	Paragraphs []string
}

// Commit status values (AC: 重复确认不复制课文 — the receipt names what
// actually landed, per item).
const (
	StatusAdded    = "added"
	StatusExisting = "existing"
	StatusInvalid  = "invalid"
)

// CommitOutcome is one draft's real result: the stored passage's id for
// added AND existing alike (an existing passage keeps its row — the range
// and attribution still apply), plus the range's own outcome when the
// preview sent one, or the reason an invalid draft never wrote.
type CommitOutcome struct {
	Title       string `json:"title"`
	Status      string `json:"status"`
	Reason      string `json:"reason,omitempty"`
	ScopeStatus string `json:"scope_status,omitempty"`
	ScopeReason string `json:"scope_reason,omitempty"`
	PassageID   int64  `json:"passage_id"`
	ScopeID     int64  `json:"scope_id"`
}

// CommitInput is one confirmed commit: the drafts in preview order, the
// subject (passages are a 语文 content type by default — "chinese" — but
// the structured representation is not学科-bound, an English whole
// sentence / short text may ride "english"), and the optional attribution
// target established BEFORE the commit (0 = 不指定教材; the lesson rides
// each draft).
type CommitInput struct {
	Category   string
	Drafts     []Draft
	TextbookID int64
}

// normalizeParagraphs trims every paragraph and drops the empty ones,
// preserving order (段落边界和顺序保留; blank rows are editing noise,
// never 自然段).
func normalizeParagraphs(paragraphs []string) []string {
	out := make([]string, 0, len(paragraphs))
	for _, paragraph := range paragraphs {
		if paragraph = strings.TrimSpace(paragraph); paragraph != "" {
			out = append(out, paragraph)
		}
	}
	return out
}

// Fingerprint hashes the normalized FULL text — title, author and every
// paragraph in order — so identity is the complete original, never the
// title alone (两篇同题不同文的课文是两篇课文; the same passage
// re-confirmed is the same row).
func Fingerprint(title, author string, paragraphs []string) string {
	payload := strings.TrimSpace(title) + "\n" + strings.TrimSpace(author) + "\n" +
		strings.Join(normalizeParagraphs(paragraphs), "\n")
	sum := sha256.Sum256([]byte(payload))
	return hex.EncodeToString(sum[:])
}

// canonicalParagraphs sorts, deduplicates and clamps the index set to the
// passage's real paragraph count, returning the indexes and their
// canonical JSON text (the storage/comparison form — "[2,3]" never "[3,2]"
// or "[2,2,3]", so identity comparisons are plain string equality). An
// out-of-range index (any i < 1 or i > count) reports ok=false — the
// caller rejects the whole range rather than silently dropping indexes
// (选择第 2、3 段 must land as exactly 2 and 3, or not at all).
func canonicalParagraphs(indexes []int, count int) ([]int, string, bool) {
	sorted := make([]int, 0, len(indexes))
	for _, i := range indexes {
		if i < 1 || i > count {
			return nil, "", false
		}
		sorted = append(sorted, i)
	}
	sort.Ints(sorted)
	out := make([]int, 0, len(sorted))
	for _, i := range sorted {
		if len(out) == 0 || out[len(out)-1] != i {
			out = append(out, i)
		}
	}
	if len(out) == 0 {
		return nil, "[]", true
	}
	encoded, err := json.Marshal(out)
	if err != nil {
		return nil, "", false
	}
	return out, string(encoded), true
}

// Store is the passage read/write path. Repository pattern mirrors
// pkg/lesson.Store and pkg/poem.Store: nil-guarded constructor, NoOpLogger
// fallback, sentinel errors via errors.Is.
type Store struct {
	db     *sql.DB
	logger infra.Logger
}

// NewStore constructs a passage Store from an open *sql.DB (D-47
// nil-guard; nil logger degrades to the NoOp one).
func NewStore(db *sql.DB, logger infra.Logger) (*Store, error) {
	if db == nil {
		return nil, errors.Wrap(ErrInvalidPassage, "passage/store: *sql.DB must not be nil")
	}
	if logger == nil {
		logger = infra.NewNoOpLogger()
	}
	return &Store{db: db, logger: logger}, nil
}

// Commit writes the confirmed drafts in ONE transaction. Each draft
// resolves its fingerprint FIRST (one indexed read): a hit — in the
// library or earlier in this same batch — reports existing with the
// stored row's id and never inserts; a miss inserts and reports added.
// 重复确认不复制课文, and the whole commit is all-or-nothing at the
// transaction level. A draft's practice range and the textbook
// attribution ride the same transaction (see AddScope / the assignment
// upsert below for their own per-item semantics).
func (s *Store) Commit(ctx context.Context, in CommitInput) ([]CommitOutcome, error) {
	if len(in.Drafts) == 0 {
		return nil, errors.Wrap(ErrInvalidPassage, "passage commit: no drafts")
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, errors.Wrap(err, "passage commit: begin")
	}
	defer tx.Rollback() //nolint:errcheck // no-op once Commit succeeds

	outcomes := make([]CommitOutcome, 0, len(in.Drafts))
	seen := make(map[string]int64, len(in.Drafts))
	for _, d := range in.Drafts {
		title := strings.TrimSpace(d.Title)
		paragraphs := normalizeParagraphs(d.Paragraphs)
		author := strings.TrimSpace(d.Author)
		if title == "" || len(paragraphs) == 0 {
			// The API gate rejects these first; the store stays defensive —
			// an empty shell is reported per-item, never a whole-commit
			// failure that would hide the siblings' real outcomes.
			outcomes = append(outcomes, CommitOutcome{
				Title: title, Status: StatusInvalid, Reason: "no title or no paragraphs",
			})
			continue
		}
		fp := Fingerprint(title, author, paragraphs)

		var passageID int64
		var status string
		if prior, ok := seen[fp]; ok {
			// Earlier in THIS batch — the first sighting's row governs.
			passageID, status = prior, StatusExisting
		} else {
			err = s.resolveOrInsert(ctx, tx, fp, title, author, paragraphs, in.Category, &passageID, &status)
			if err != nil {
				return nil, err
			}
			seen[fp] = passageID
		}

		outcome := CommitOutcome{Title: title, Status: status, PassageID: passageID}
		if d.Scope != nil {
			scope, serr := s.scopeTx(ctx, tx, passageID, paragraphs, *d.Scope)
			switch {
			case serr == nil:
				outcome.ScopeID, outcome.ScopeStatus = scope.ID, scope.Status
			case errors.Is(serr, ErrScopeOutOfRange):
				// The API gate rejects this first; defensively the TEXT still
				// stores (it was reviewed) while the range reports invalid —
				// the per-item receipt stays honest about both halves.
				outcome.ScopeStatus, outcome.ScopeReason = StatusInvalid, "scope paragraphs out of range"
			default:
				return nil, serr
			}
		}
		if in.TextbookID > 0 {
			if _, err = tx.ExecContext(ctx,
				`INSERT INTO passage_assignments (passage_id, textbook_id, lesson, include_in_scope)
				 VALUES (?, ?, ?, 0)
				 ON CONFLICT(passage_id, textbook_id) DO UPDATE SET lesson = excluded.lesson`,
				passageID, in.TextbookID, strings.TrimSpace(d.Lesson),
			); err != nil {
				s.logger.Error("passage commit: assignment upsert failed",
					infra.F("component", "passage/store"),
					infra.F("passage_id", passageID),
					infra.ErrField(err),
				)
				return nil, errors.Wrap(err, "passage commit: assignment")
			}
		}

		outcomes = append(outcomes, outcome)
	}

	if err = tx.Commit(); err != nil {
		return nil, errors.Wrap(err, "passage commit: commit")
	}
	return outcomes, nil
}

// resolveOrInsert resolves one draft's fingerprint inside the open commit
// transaction: an existing row reports existing, a miss inserts and
// reports added (the poem commit's inline lookup, factored out because
// the passage commit also carries scope and attribution steps).
func (s *Store) resolveOrInsert(ctx context.Context, tx *sql.Tx, fp, title, author string, paragraphs []string, category string, passageID *int64, status *string) error {
	err := tx.QueryRowContext(ctx,
		"SELECT id FROM passages WHERE fingerprint = ?", fp).Scan(passageID)
	switch {
	case err == nil:
		*status = StatusExisting
	case errors.Is(err, sql.ErrNoRows):
		paragraphsJSON, merr := json.Marshal(paragraphs)
		if merr != nil {
			return errors.Wrap(merr, "passage commit: marshal paragraphs")
		}
		var res sql.Result
		res, err = tx.ExecContext(ctx,
			"INSERT INTO passages (title, author, paragraphs, fingerprint, category) VALUES (?, ?, ?, ?, ?)",
			title, author, string(paragraphsJSON), fp, category,
		)
		if err != nil {
			s.logger.Error("passage commit: insert failed",
				infra.F("component", "passage/store"),
				infra.F("title", title),
				infra.ErrField(err),
			)
			return errors.Wrap(err, "passage commit: insert")
		}
		if *passageID, err = res.LastInsertId(); err != nil {
			return errors.Wrap(err, "passage commit: last insert id")
		}
		*status = StatusAdded
	default:
		return errors.Wrap(err, "passage commit: fingerprint lookup")
	}
	return nil
}

// scopeOutcome is scopeTx's result: the stored row's id plus the
// added/existing verdict (重复确认不重复建立相同范围).
type scopeOutcome struct {
	Status string
	ID     int64
}

// scopeTx lands ONE practice range inside an open transaction: the
// canonical (whole, paragraphs) identity is looked up first — a hit
// reports existing and never inserts; a miss inserts and reports added.
// A paragraph index outside the stored text reports ErrScopeOutOfRange
// (never a partial write).
func (s *Store) scopeTx(ctx context.Context, tx *sql.Tx, passageID int64, paragraphs []string, scope Scope) (scopeOutcome, error) {
	count := len(normalizeParagraphs(paragraphs))
	indexes := scope.Paragraphs
	if scope.Whole {
		indexes = nil // a whole range stores the canonical empty set
	}
	canonical, encoded, ok := canonicalParagraphs(indexes, count)
	if !ok || (!scope.Whole && len(canonical) == 0) {
		return scopeOutcome{}, errors.Wrap(ErrScopeOutOfRange,
			fmt.Sprintf("passage scope: passage %d has %d paragraphs", passageID, count))
	}
	whole := 0
	if scope.Whole {
		whole = 1
	}

	var scopeID int64
	err := tx.QueryRowContext(ctx,
		"SELECT id FROM passage_scopes WHERE passage_id = ? AND whole = ? AND paragraphs = ?",
		passageID, whole, encoded).Scan(&scopeID)
	switch {
	case err == nil:
		return scopeOutcome{ID: scopeID, Status: StatusExisting}, nil
	case errors.Is(err, sql.ErrNoRows):
		var res sql.Result
		res, err = tx.ExecContext(ctx,
			"INSERT INTO passage_scopes (passage_id, whole, paragraphs) VALUES (?, ?, ?)",
			passageID, whole, encoded,
		)
		if err != nil {
			s.logger.Error("passage scope insert failed",
				infra.F("component", "passage/store"),
				infra.F("passage_id", passageID),
				infra.ErrField(err),
			)
			return scopeOutcome{}, errors.Wrap(err, "passage scope: insert")
		}
		if scopeID, err = res.LastInsertId(); err != nil {
			return scopeOutcome{}, errors.Wrap(err, "passage scope: last insert id")
		}
		return scopeOutcome{ID: scopeID, Status: StatusAdded}, nil
	default:
		return scopeOutcome{}, errors.Wrap(err, "passage scope: lookup")
	}
}

// AddOutcome is AddScope's result: the stored row (id, whole, canonical
// index set, created_at) plus the added/existing verdict — re-selecting
// the same range reports existing and reuses the row (重复确认不重复建立
// 相同范围).
type AddOutcome struct {
	Status string
	Row    ScopeRow
}

// AddScope lands ONE practice range on the management path (统一内容页的
// 范围选择): same identity and validation as the commit path, in its own
// transaction. A passage id with no row reports ErrPassageNotFound; an
// out-of-range paragraph index reports ErrScopeOutOfRange with nothing
// written.
func (s *Store) AddScope(ctx context.Context, passageID int64, scope Scope) (AddOutcome, error) {
	if passageID <= 0 {
		return AddOutcome{}, errors.Wrap(ErrInvalidPassage, "passage add scope: id must be positive")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return AddOutcome{}, errors.Wrap(err, "passage add scope: begin")
	}
	defer tx.Rollback() //nolint:errcheck // no-op once Commit succeeds

	var paragraphsJSON string
	if err = tx.QueryRowContext(ctx,
		"SELECT paragraphs FROM passages WHERE id = ?", passageID).Scan(&paragraphsJSON); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return AddOutcome{}, errors.Wrap(ErrPassageNotFound, fmt.Sprintf("passage add scope: passage %d", passageID))
		}
		return AddOutcome{}, errors.Wrap(err, "passage add scope: passage lookup")
	}
	paragraphs := decodeParagraphs(paragraphsJSON)

	outcome, err := s.scopeTx(ctx, tx, passageID, paragraphs, scope)
	if err != nil {
		return AddOutcome{}, err
	}
	if err = tx.Commit(); err != nil {
		return AddOutcome{}, errors.Wrap(err, "passage add scope: commit")
	}
	row := ScopeRow{
		ID: outcome.ID, Whole: scope.Whole,
		Paragraphs: canonicalOrEmpty(scope, paragraphs),
	}
	// created_at rides the response for an honest receipt; a read failure
	// degrades to empty (the row's identity — id/whole/indexes — already
	// carries the outcome).
	_ = s.db.QueryRowContext(ctx,
		"SELECT created_at FROM passage_scopes WHERE id = ?", outcome.ID).Scan(&row.CreatedAt)
	return AddOutcome{Row: row, Status: outcome.Status}, nil
}

// canonicalOrEmpty projects the caller's scope onto the ScopeRow shape for
// the AddScope response: a whole range reads as the empty index list (its
// stored form), a paragraph set as its canonical order.
func canonicalOrEmpty(scope Scope, paragraphs []string) []int {
	if scope.Whole {
		return []int{}
	}
	canonical, _, ok := canonicalParagraphs(scope.Paragraphs, len(normalizeParagraphs(paragraphs)))
	if !ok {
		return []int{}
	}
	return canonical
}

// RemoveScope deletes ONE practice range (范围调整: removing a range
// never touches the passage, its other ranges or its attribution). A
// scope id that does not sit on the given passage reports
// ErrScopeNotFound.
func (s *Store) RemoveScope(ctx context.Context, passageID, scopeID int64) error {
	if passageID <= 0 || scopeID <= 0 {
		return errors.Wrap(ErrInvalidPassage, "passage remove scope: ids must be positive")
	}
	res, err := s.db.ExecContext(ctx,
		"DELETE FROM passage_scopes WHERE id = ? AND passage_id = ?", scopeID, passageID)
	if err != nil {
		s.logger.Error("passage remove scope failed",
			infra.F("component", "passage/store"),
			infra.F("passage_id", passageID),
			infra.F("scope_id", scopeID),
			infra.ErrField(err),
		)
		return errors.Wrap(err, "passage remove scope")
	}
	if n, terr := res.RowsAffected(); terr == nil && n == 0 {
		return errors.Wrap(ErrScopeNotFound,
			fmt.Sprintf("passage remove scope: passage %d scope %d", passageID, scopeID))
	}
	return nil
}

// Assign upserts ONE passage's attribution on the management path (统一
// 内容页的课程归属管理): the lesson and include_in_scope refresh together —
// this is the parent's explicit management action, unlike the re-import
// path which preserves an existing include. A passage id with no row
// reports ErrPassageNotFound; the textbook's existence is the CALLER's
// pre-check (it owns the TextbookRepository read).
func (s *Store) Assign(ctx context.Context, passageID, textbookID int64, lesson string, includeInScope bool) error {
	if passageID <= 0 || textbookID <= 0 {
		return errors.Wrap(ErrInvalidPassage, "passage assign: ids must be positive")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return errors.Wrap(err, "passage assign: begin")
	}
	defer tx.Rollback() //nolint:errcheck // no-op once Commit succeeds

	var one int
	if err = tx.QueryRowContext(ctx, "SELECT 1 FROM passages WHERE id = ?", passageID).Scan(&one); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return errors.Wrap(ErrPassageNotFound, fmt.Sprintf("passage assign: passage %d", passageID))
		}
		return errors.Wrap(err, "passage assign: passage lookup")
	}
	if _, err = tx.ExecContext(ctx,
		`INSERT INTO passage_assignments (passage_id, textbook_id, lesson, include_in_scope)
		 VALUES (?, ?, ?, ?)
		 ON CONFLICT(passage_id, textbook_id) DO UPDATE SET lesson = excluded.lesson, include_in_scope = excluded.include_in_scope`,
		passageID, textbookID, strings.TrimSpace(lesson), includeInScope,
	); err != nil {
		s.logger.Error("passage assign: upsert failed",
			infra.F("component", "passage/store"),
			infra.F("passage_id", passageID),
			infra.ErrField(err),
		)
		return errors.Wrap(err, "passage assign: upsert")
	}
	return tx.Commit()
}

// ListFilter narrows a List call: an empty Category returns every
// passage, an empty Query matches all.
type ListFilter struct {
	Category string
	Query    string
}

// List returns the stored passages (newest first — the content view's
// newest-first convention) with their attribution projection, practice
// ranges, scope verdict and per-range practice state (Issue #14). The
// attribution join is a LEFT JOIN against the Python-owned textbooks table
// through database.Open's shared *sql.DB; on a pre-migration database (no
// textbooks table yet) the join degrades to nameless assignments and
// progress-pending scope, never an error: the passages still render (the
// card/poem repo's same degraded-read discipline).
func (s *Store) List(ctx context.Context, filter ListFilter) ([]Passage, error) {
	query := `SELECT p.id, p.title, p.author, p.paragraphs, p.category, p.created_at FROM passages p`
	args := []any{}
	if filter.Category != "" {
		query += " WHERE p.category = ?"
		args = append(args, filter.Category)
	}
	if filter.Query != "" {
		clause := "(p.title LIKE ? ESCAPE '\\' OR p.author LIKE ? ESCAPE '\\' OR p.paragraphs LIKE ? ESCAPE '\\')"
		pattern := "%" + escapeLike(filter.Query) + "%"
		if filter.Category != "" {
			query += " AND " + clause
		} else {
			query += " WHERE " + clause
		}
		args = append(args, pattern, pattern, pattern)
	}
	query += " ORDER BY p.created_at DESC, p.id DESC"

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		s.logger.Error("passage list failed",
			infra.F("component", "passage/store"),
			infra.ErrField(err),
		)
		return nil, errors.Wrap(err, "passage list")
	}
	defer rows.Close()

	passages := []Passage{}
	for rows.Next() {
		var p Passage
		var paragraphsJSON string
		if err = rows.Scan(&p.ID, &p.Title, &p.Author, &paragraphsJSON, &p.Category, &p.CreatedAt); err != nil {
			return nil, errors.Wrap(err, "passage list: scan")
		}
		p.Paragraphs = decodeParagraphs(paragraphsJSON)
		p.Assignments = []Assignment{}
		p.Scopes = []ScopeRow{}
		passages = append(passages, p)
	}
	if err = rows.Err(); err != nil {
		return nil, errors.Wrap(err, "passage list: iteration")
	}
	if len(passages) == 0 {
		return passages, nil
	}
	if err = s.hydrate(ctx, passages); err != nil {
		return nil, err
	}
	return passages, nil
}

// Get returns ONE stored passage by id with the same attribution, range,
// scope-verdict and practice projection as List — the passage classroom's
// fetch (Issue #14: the session must start from an in-scope passage and a
// REAL stored range, so the caller branches on InScope and finds the scope
// row by id). ErrPassageNotFound is the honest miss for an unknown or
// deleted id.
func (s *Store) Get(ctx context.Context, id int64) (Passage, error) {
	passages, err := s.PassagesByIDs(ctx, []int64{id})
	if err != nil {
		return Passage{}, err
	}
	if len(passages) == 0 || passages[0].ID != id {
		return Passage{}, errors.Wrap(ErrPassageNotFound, fmt.Sprintf("passage get: passage %d", id))
	}
	return passages[0], nil
}

// PassagesByIDs returns the stored passages for the given ids (arbitrary
// order — the caller keys by id; passages missing from the store are simply
// absent from the result). One shared read backs the parent records
// projection (Issue #14: the records view needs the passage's title/author
// per lesson row without a per-row round trip).
func (s *Store) PassagesByIDs(ctx context.Context, ids []int64) ([]Passage, error) {
	if len(ids) == 0 {
		return []Passage{}, nil
	}
	placeholders := make([]string, 0, len(ids))
	args := make([]any, 0, len(ids))
	for _, id := range ids {
		placeholders = append(placeholders, "?")
		args = append(args, id)
	}
	rows, err := s.db.QueryContext(ctx,
		"SELECT id, title, author, paragraphs, category, created_at FROM passages WHERE id IN ("+strings.Join(placeholders, ",")+")", //nolint:gosec // G202: placeholders are '?', ids are bound parameters
		args...)
	if err != nil {
		s.logger.Error("passage get by ids failed",
			infra.F("component", "passage/store"),
			infra.F("count", len(ids)),
			infra.ErrField(err),
		)
		return nil, errors.Wrap(err, "passage get by ids")
	}
	defer rows.Close()

	passages := []Passage{}
	for rows.Next() {
		var p Passage
		var paragraphsJSON string
		if err = rows.Scan(&p.ID, &p.Title, &p.Author, &paragraphsJSON, &p.Category, &p.CreatedAt); err != nil {
			return nil, errors.Wrap(err, "passage get by ids: scan")
		}
		p.Paragraphs = decodeParagraphs(paragraphsJSON)
		p.Assignments = []Assignment{}
		p.Scopes = []ScopeRow{}
		passages = append(passages, p)
	}
	if err = rows.Err(); err != nil {
		return nil, errors.Wrap(err, "passage get by ids: iteration")
	}
	if len(passages) == 0 {
		return passages, nil
	}
	if err = s.hydrate(ctx, passages); err != nil {
		return nil, err
	}
	return passages, nil
}

// hydrate loads the management/practice projections onto already-scanned
// passage rows: attributions (with the Issue #4 scope verdict), practice
// ranges, and each range's own practice state (Issue #14) — the shared body
// of List and PassagesByIDs so the two reads cannot drift.
func (s *Store) hydrate(ctx context.Context, passages []Passage) error {
	byID := make(map[int64]*Passage, len(passages))
	for i := range passages {
		byID[passages[i].ID] = &passages[i]
	}

	books := map[int64]bookProgress{}
	assignments := map[int64][]Assignment{}
	if assignRows, aerr := s.db.QueryContext(ctx,
		`SELECT a.passage_id, a.textbook_id, a.lesson, a.include_in_scope,
		        t.name, t.lessons, t.learned_lessons
		 FROM passage_assignments a
		 LEFT JOIN textbooks t ON t.id = a.textbook_id
		 ORDER BY a.passage_id, a.textbook_id`); aerr != nil {
		// No textbooks table yet (a database older than Issue #4): no
		// attribution can exist either — every passage reads supplementary
		// until the table does. The honest degraded read, not a failed
		// page (the card/poem repo's pre-migration discipline).
		if !strings.Contains(aerr.Error(), "no such table") {
			s.logger.Error("passage assignments query failed",
				infra.F("component", "passage/store"),
				infra.ErrField(aerr),
			)
			return errors.Wrap(aerr, "passage list: assignments")
		}
	} else {
		defer assignRows.Close()
		if err := scanAssignments(assignRows, byID, assignments, books); err != nil {
			return err
		}
	}

	scopes := map[int64][]ScopeRow{}
	if scopeRows, serr := s.db.QueryContext(ctx,
		`SELECT id, passage_id, whole, paragraphs, created_at
		 FROM passage_scopes ORDER BY passage_id, id`); serr != nil {
		return errors.Wrap(serr, "passage list: scopes")
	} else {
		defer scopeRows.Close()
		for scopeRows.Next() {
			var passageID, whole int64
			var row ScopeRow
			var paragraphsJSON string
			if err := scopeRows.Scan(&row.ID, &passageID, &whole, &paragraphsJSON, &row.CreatedAt); err != nil {
				return errors.Wrap(err, "passage list: scopes scan")
			}
			if _, wanted := byID[passageID]; !wanted {
				continue // a range for a passage outside this page
			}
			row.Whole = whole == 1
			row.Paragraphs = decodeIndexes(paragraphsJSON)
			scopes[passageID] = append(scopes[passageID], row)
		}
		if err := scopeRows.Err(); err != nil {
			return errors.Wrap(err, "passage list: scopes iteration")
		}
	}

	for i := range passages {
		if rows, ok := assignments[passages[i].ID]; ok {
			passages[i].Assignments = rows
		}
		if rows, ok := scopes[passages[i].ID]; ok {
			passages[i].Scopes = rows
		}
		applyScope(&passages[i], books)
	}
	s.attachScopePractice(ctx, passages)
	return nil
}

// attachScopePractice fills every scope row's practice projection from the
// per-range schedule table (written atomically with each practice log, so
// its presence IS the has_practice verdict). The join key is the same
// content + range identity the records carry: a removed-and-re-added range
// re-links its own history, while a whole range and a paragraph set never
// borrow each other's state (第 2 段掌握不能算全文掌握). A read failure
// degrades to the un-practiced projection (the poem attachReviewSchedules
// discipline: presentational state never fails the page).
func (s *Store) attachScopePractice(ctx context.Context, passages []Passage) {
	if len(passages) == 0 {
		return
	}
	type scheduleRow struct {
		nextReviewDay string
		whole         bool
		lastMastered  bool
	}
	byRange := map[string]scheduleRow{}
	rows, err := s.db.QueryContext(ctx,
		`SELECT passage_id, whole, paragraphs, last_mastered, next_review_day FROM passage_review_schedule`)
	if err != nil {
		s.logger.Error("passage scope practice read failed",
			infra.F("component", "passage/store"),
			infra.ErrField(err),
		)
		return
	}
	defer rows.Close()
	for rows.Next() {
		var passageID, whole, mastered int64
		var paragraphs, next string
		if err := rows.Scan(&passageID, &whole, &paragraphs, &mastered, &next); err != nil {
			continue // defensive: a bad row must not sink the page
		}
		byRange[rangeKey(passageID, whole == 1, paragraphs)] = scheduleRow{
			whole: whole == 1, lastMastered: mastered != 0, nextReviewDay: next,
		}
	}

	for i := range passages {
		for j := range passages[i].Scopes {
			scope := &passages[i].Scopes[j]
			row, ok := byRange[scopeKeyOf(passages[i].ID, scope)]
			if !ok {
				continue
			}
			scope.HasPractice = true
			scope.LastMastered = row.lastMastered
			scope.NextReviewDay = row.nextReviewDay
		}
	}
}

// rangeKey builds the map key of one STORED range (passage id + whole flag +
// the canonical paragraphs JSON the columns already carry — callers pass the
// column verbatim, never raw client input).
func rangeKey(passageID int64, whole bool, canonicalParagraphs string) string {
	wholeFlag := 0
	if whole {
		wholeFlag = 1
	}
	return fmt.Sprintf("%d|%d|%s", passageID, wholeFlag, canonicalParagraphs)
}

// scopeKeyOf projects one ScopeRow onto the practice-state map key. The
// paragraph list is re-canonicalized (sorted + deduplicated, empty set for
// whole) so a scope row always matches its own stored range identity.
func scopeKeyOf(passageID int64, scope *ScopeRow) string {
	indexes := scope.Paragraphs
	if scope.Whole {
		indexes = nil
	}
	_, encoded, _ := canonicalParagraphs(indexes, 1<<30)
	return rangeKey(passageID, scope.Whole, encoded)
}

// scanAssignments drains the attribution join into the per-passage
// projection maps (factored out of List's body; the poem twin lives
// inline).
func scanAssignments(rows *sql.Rows, byID map[int64]*Passage, assignments map[int64][]Assignment, books map[int64]bookProgress) error {
	for rows.Next() {
		var passageID, textbookID, include int64
		var lesson string
		var nameNull, lessonsNull sql.NullString
		var learnedNull sql.NullInt64
		if err := rows.Scan(&passageID, &textbookID, &lesson, &include, &nameNull, &lessonsNull, &learnedNull); err != nil {
			return errors.Wrap(err, "passage list: assignments scan")
		}
		if _, wanted := byID[passageID]; !wanted {
			continue // an attribution for a passage outside this page
		}
		if _, seen := books[textbookID]; !seen {
			books[textbookID] = bookProgress{
				lessons: decodeLessons(lessonsNull.String),
				learned: int(learnedNull.Int64),
			}
		}
		assignments[passageID] = append(assignments[passageID], Assignment{
			TextbookID:     textbookID,
			TextbookName:   nameNull.String,
			Lesson:         lesson,
			IncludeInScope: include == 1,
		})
	}
	if err := rows.Err(); err != nil {
		return errors.Wrap(err, "passage list: assignments iteration")
	}
	return nil
}

// bookProgress is one textbook's explicit lesson order plus its learned
// count — the two columns the scope verdict reads (the poem twin).
type bookProgress struct {
	lessons []string
	learned int
}

// applyScope computes one passage's scope verdict from its attributions
// and the textbook progress map (the passage twin of poem.applyScope's
// three branches): supplementary when unassigned, included on an explicit
// include, learned when any attribution's lesson sits inside its book's
// learned prefix, else progress-pending. A book missing from the map (its
// row deleted Python-side) treats the attribution as lesson-less.
func applyScope(p *Passage, books map[int64]bookProgress) {
	if len(p.Assignments) == 0 {
		p.InScope, p.ScopeReason = true, ScopeSupplementary
		return
	}
	for _, a := range p.Assignments {
		if a.IncludeInScope {
			p.InScope, p.ScopeReason = true, ScopeIncluded
			return
		}
	}
	for _, a := range p.Assignments {
		if a.Lesson == "" {
			continue
		}
		bp, ok := books[a.TextbookID]
		if !ok {
			continue
		}
		// 已学完第 N 课包含第 N 课; a stale counter past the list's end
		// clamps to the whole list (the learned-prefix rule).
		limit := bp.learned
		if limit > len(bp.lessons) {
			limit = len(bp.lessons)
		}
		for _, name := range bp.lessons[:limit] {
			if name == a.Lesson {
				p.InScope, p.ScopeReason = true, ScopeLearned
				return
			}
		}
	}
	p.InScope, p.ScopeReason = false, ScopeProgressPending
}

// decodeParagraphs parses the paragraphs JSON column with the
// legacy-default tolerance of the poem decoders: empty raw → empty
// slice; corrupted non-empty JSON surfaces as an error the caller
// already logged.
func decodeParagraphs(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return []string{}
	}
	var paragraphs []string
	if err := json.Unmarshal([]byte(raw), &paragraphs); err != nil {
		return []string{}
	}
	if paragraphs == nil {
		paragraphs = []string{}
	}
	return paragraphs
}

// decodeIndexes parses a scope's canonical index JSON column
// defensively: NULL/invalid yields the empty list (the decodeLessons
// discipline).
func decodeIndexes(raw string) []int {
	if strings.TrimSpace(raw) == "" {
		return []int{}
	}
	var indexes []int
	if err := json.Unmarshal([]byte(raw), &indexes); err != nil {
		return []int{}
	}
	if indexes == nil {
		indexes = []int{}
	}
	return indexes
}

// decodeLessons parses a textbooks.lessons JSON column defensively (the
// parseTextbookLessons discipline: NULL/invalid yields the empty list).
func decodeLessons(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return []string{}
	}
	var lessons []string
	if err := json.Unmarshal([]byte(raw), &lessons); err != nil {
		return []string{}
	}
	out := make([]string, 0, len(lessons))
	for _, name := range lessons {
		if name = strings.TrimSpace(name); name != "" {
			out = append(out, name)
		}
	}
	return out
}

// escapeLike escapes the SQL LIKE wildcards (the card repo's helper,
// repeated locally to keep the package self-contained).
func escapeLike(s string) string {
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '\\', '%', '_':
			out = append(out, '\\', s[i])
		default:
			out = append(out, s[i])
		}
	}
	return string(out)
}
