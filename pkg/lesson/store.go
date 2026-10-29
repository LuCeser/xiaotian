// Package lesson provides the R0 classroom-state and idempotent
// tool-execution layer for the realtime tutor loop (Phase 13). It owns two
// Go-side SQLite tables: lessons (per-lesson word list and progress cursor)
// and tool_requests (one row per executed LLM tool call, keyed by request_id
// for idempotency).
//
// Lesson writes run in short SQLite transactions that are decoupled from the
// voice/cancel stack: cancelling TTS generation never rolls back an
// already-committed classroom tool result (13-CONTEXT).
package lesson

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/pkg/errors"

	"github.com/xiaotian/home-tutor-agent/pkg/infra"
)

// Sentinel errors for the lesson package. Callers MUST branch on these via
// errors.Is rather than string matching (mirrors database.SessionRepository,
// D-05 rich error handling).
var (
	// ErrLessonNotFound is returned when no lessons row exists for the given
	// lesson_id (GetLesson, Executor.AdvanceWord).
	ErrLessonNotFound = errors.New("lesson: lesson not found")

	// ErrInvalidLesson is returned when caller-supplied arguments are invalid
	// (empty lesson_id, empty words, empty request_id, or a corrupted
	// tool_requests result residue). Nothing is written to the database when
	// it is returned.
	ErrInvalidLesson = errors.New("lesson: invalid lesson arguments")

	// ErrDesignationNotFound is returned when no designations row exists for
	// the given id (ConsumeDesignation).
	ErrDesignationNotFound = errors.New("lesson: designation not found")
)

// Table DDL constants — R0 classroom tables, Go-owned (13-PATTERNS.md).
// Mirrors the database package's CREATE TABLE IF NOT EXISTS registration
// pattern (schema.go): words/result columns carry JSON payloads.
var (
	lessonsDDL = `CREATE TABLE IF NOT EXISTS lessons (
    lesson_id TEXT PRIMARY KEY,
    words TEXT NOT NULL,            -- JSON array
    cards TEXT NOT NULL DEFAULT '[]', -- JSON array, elements are Word
    current_index INTEGER NOT NULL DEFAULT 0,
    version INTEGER NOT NULL DEFAULT 1,
    stage TEXT NOT NULL DEFAULT '',   -- 13.1 check stage: '' | 'checking' | 'submitted'
    outcomes TEXT NOT NULL DEFAULT '{}',  -- JSON object: word index → WordOutcome
    verdicts TEXT NOT NULL DEFAULT '[]',  -- JSON array of CheckVerdict
    unchecked INTEGER NOT NULL DEFAULT 0, -- 13.1 D-03: lesson closed without a check
    category TEXT NOT NULL DEFAULT '',    -- Issue #10: 'english' | 'chinese' ('' = legacy english)
    created_at TEXT NOT NULL DEFAULT (datetime('now')),
    updated_at TEXT NOT NULL DEFAULT (datetime('now'))
)`

	toolRequestsDDL = `CREATE TABLE IF NOT EXISTS tool_requests (
    request_id TEXT PRIMARY KEY,    -- MiMo tool_call id
    lesson_id TEXT NOT NULL,
    tool TEXT NOT NULL,
    result TEXT NOT NULL,
    created_at TEXT NOT NULL DEFAULT (datetime('now'))
)`

	idxToolRequestsLesson = `CREATE INDEX IF NOT EXISTS idx_tool_requests_lesson ON tool_requests(lesson_id)`

	// alterLessonsAddCards backfills the cards column on legacy lessons
	// tables created before it existed (13-06 deployments). Purely additive:
	// existing rows keep their data and read cards as the empty '[]' array.
	alterLessonsAddCards = `ALTER TABLE lessons ADD COLUMN cards TEXT NOT NULL DEFAULT '[]'`

	// The 13.1 check-stage columns (alterLessonsAddStage/Outcomes/Verdicts/
	// Unchecked) follow the same purely-additive PRAGMA-probe pattern: legacy
	// rows read back the defaults (stage=''/outcomes='{}'/verdicts='[]'/
	// unchecked=0) and keep short-circuiting as completed sessions. The
	// unchecked column is owned HERE (13.1-04 consumes Lesson.Unchecked for
	// the fallback/correction paths on top of this task's scan).
	alterLessonsAddStage     = `ALTER TABLE lessons ADD COLUMN stage TEXT NOT NULL DEFAULT ''`
	alterLessonsAddOutcomes  = `ALTER TABLE lessons ADD COLUMN outcomes TEXT NOT NULL DEFAULT '{}'`
	alterLessonsAddVerdicts  = `ALTER TABLE lessons ADD COLUMN verdicts TEXT NOT NULL DEFAULT '[]'`
	alterLessonsAddUnchecked = `ALTER TABLE lessons ADD COLUMN unchecked INTEGER NOT NULL DEFAULT 0`

	// alterLessonsAddCategory (Issue #10): the classroom's subject travels
	// WITH the record — '' on legacy rows means the English-only era and
	// reads back as CategoryEnglish (NormalizeCategory), never as "unknown".
	alterLessonsAddCategory = `ALTER TABLE lessons ADD COLUMN category TEXT NOT NULL DEFAULT ''`

	// alterLessonsAddPoemID (Issue #12): a whole-poem 背默 classroom stamps
	// the practiced work's poems.id here — 0 on every non-poem row (the
	// default), and the parent records projection's join key for the poem's
	// title/author. The lesson row IS the attempt record (its verdicts are
	// the per-line judgment); the poem_practice_logs row written at submit is
	// the SAVE, so a failed save must not lose the poem identity either —
	// hence the column lives on this row, not only the log.
	alterLessonsAddPoemID = `ALTER TABLE lessons ADD COLUMN poem_id INTEGER NOT NULL DEFAULT 0`

	// alterLessonsAddPassage (Issue #14): a passage 背默 classroom stamps the
	// practiced work's passages.id here plus the RANGE identity it practiced
	// — passage_scope_id (traceability to the passage_scopes row, which the
	// parent may later remove and re-create) and the range's own identity
	// columns (whole + the canonical paragraph set), mirroring the
	// passage_practice_logs identity. 0/'[]' on every non-passage row. The
	// lesson row is the attempt + per-paragraph judgment; the range columns
	// must survive independently of the scope row so resume, the records
	// projection and a later submit all read the SAME range the child
	// practiced (原文范围对应关系), never a re-derived guess.
	alterLessonsAddPassageID      = `ALTER TABLE lessons ADD COLUMN passage_id INTEGER NOT NULL DEFAULT 0`
	alterLessonsAddPassageScopeID = `ALTER TABLE lessons ADD COLUMN passage_scope_id INTEGER NOT NULL DEFAULT 0`
	alterLessonsAddPassageWhole   = `ALTER TABLE lessons ADD COLUMN passage_whole INTEGER NOT NULL DEFAULT 0`
	alterLessonsAddPassageParas   = `ALTER TABLE lessons ADD COLUMN passage_paragraphs TEXT NOT NULL DEFAULT '[]'`

	// designationsDDL (Issue #80 指定默写): one row per CREATED designation.
	// Selection and the fetch-lock claim live in the Python designate
	// command; this row snapshots WHAT was selected so the classroom
	// consumption replays exactly that list, never a re-query. status
	// starts pending and flips to consumed exactly once, only after the
	// lessons row write succeeded (ConsumeDesignation). The row never
	// carries progress/include_in_scope/participates state — creation
	// writes none of them (创建不写进度、加入标记和参与开关).
	designationsDDL = `CREATE TABLE IF NOT EXISTS designations (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    textbook_id INTEGER NOT NULL,
    lesson TEXT NOT NULL,
    category TEXT NOT NULL DEFAULT '',
    cards TEXT NOT NULL DEFAULT '[]',  -- JSON array of DesignationCard
    status TEXT NOT NULL DEFAULT 'pending',
    created_at TEXT NOT NULL DEFAULT (datetime('now')),
    consumed_at TEXT NOT NULL DEFAULT '',
    request_key TEXT NOT NULL DEFAULT ''
)`

	// idxDesignationsPending serves the classroom's pending-designation
	// probe (the consumption step, Issue #80 step 14).
	idxDesignationsPending = `CREATE INDEX IF NOT EXISTS idx_designations_pending ON designations(status, id)`

	// idxDesignationsRequestKey enforces one designation per client request
	// key (Issue #80 U10): same-key retries read back the same row; different
	// targets must use different keys.
	idxDesignationsRequestKey = `CREATE UNIQUE INDEX IF NOT EXISTS idx_designations_request_key ON designations(request_key) WHERE request_key != ''`

	alterDesignationsAddRequestKey = `ALTER TABLE designations ADD COLUMN request_key TEXT NOT NULL DEFAULT ''`
)

// Word is one vocabulary card carried with the lesson row: the surface word,
// its meaning, and an example sentence. It is the ground-truth payload the
// server uses for prompts ("不会写" hints) and meaning explanations — the
// LLM never invents these.
//
// The json tags align verbatim with pybridge.DueCard's wire contract
// (word/meaning/example, vocab_manager.py's due output), so the FSRS due
// fetch (13-10 PyFSRS adapter) maps into this type losslessly.
type Word struct {
	Word    string `json:"word"`
	Meaning string `json:"meaning"`
	Example string `json:"example"`
}

// WordOutcome is one word's recorded dictation-stage behavior (13.1-01):
// the U-9 outcome vocabulary ("confirmed"/"timeout"/"hint") plus the word's
// hint-request count. The check stage snapshots these into the lessons row
// at EnterCheck so the unified rating (checkRating(result, hints)) survives
// reconnects — per-word behavior used to live only in memory.
type WordOutcome struct {
	Outcome string `json:"outcome"`
	Hints   int    `json:"hints"`
}

// CheckVerdict is one word's check-stage verdict (13.1-01): whether the
// written word was judged correct, whether the verdict came from a human
// (screen 对/错 tap in this plan; OCR prefills arrive in 13.1-03 with
// Manual=false), and the recognized handwriting text when an OCR source
// produced it. Verdicts persist in the lessons row's verdicts JSON column.
type CheckVerdict struct {
	OcrText   string `json:"ocr_text,omitempty"`
	WordIndex int    `json:"word_index"`
	Correct   bool   `json:"correct"`
	Manual    bool   `json:"manual"`
}

// Lesson-stage stage values stored in the lessons row (13.1-01). Empty means
// "no check stage recorded" — every pre-13.1 row and every completed session
// keeps the empty default and short-circuits on reconnect exactly as before.
const (
	StageChecking  = "checking"
	StageSubmitted = "submitted"
)

// Migrate creates the lesson package's Go-owned tables and index if they do
// not exist, then idempotently backfills the additive columns (cards, then
// the 13.1 check-stage columns) on legacy lessons tables that predate them:
// the live schema is probed via PRAGMA table_info(lessons) and ALTER TABLE
// runs only when a column is absent, so VPS databases created during 13-06
// gain everything with no manual intervention. Idempotent overall: safe to
// call on every boot, mirroring database.Migrate.
func Migrate(ctx context.Context, db *sql.DB) error {
	statements := []string{
		lessonsDDL,
		toolRequestsDDL,
		idxToolRequestsLesson,
		practiceLogsDDL, // 13.1-05 (D-12): Go-owned practice records, never the FSRS schedule
		idxPracticeLogsDay,
		receiptDDL, // 13.3-01 (ROADMAP SC-1): persistent save receipts, pending → saved/failed
		idxReceiptsLesson,
		designationsDDL, // Issue #80 指定默写: created designations, pending → consumed exactly once
		idxDesignationsPending,
	}

	for _, stmt := range statements {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("lesson/migrate: %w", err)
		}
	}

	for _, col := range []struct {
		name string
		ddl  string
	}{
		{"cards", alterLessonsAddCards},
		{"stage", alterLessonsAddStage},
		{"outcomes", alterLessonsAddOutcomes},
		{"verdicts", alterLessonsAddVerdicts},
		{"unchecked", alterLessonsAddUnchecked},
		{"category", alterLessonsAddCategory},
		{"poem_id", alterLessonsAddPoemID},
		{"passage_id", alterLessonsAddPassageID},
		{"passage_scope_id", alterLessonsAddPassageScopeID},
		{"passage_whole", alterLessonsAddPassageWhole},
		{"passage_paragraphs", alterLessonsAddPassageParas},
	} {
		has, err := lessonsHasColumn(ctx, db, col.name)
		if err != nil {
			return fmt.Errorf("lesson/migrate: %w", err)
		}
		if !has {
			if _, err := db.ExecContext(ctx, col.ddl); err != nil {
				return fmt.Errorf("lesson/migrate: %w", err)
			}
		}
	}

	hasReqKey, err := designationsHasColumn(ctx, db, "request_key")
	if err != nil {
		return fmt.Errorf("lesson/migrate: %w", err)
	}
	if !hasReqKey {
		if _, err := db.ExecContext(ctx, alterDesignationsAddRequestKey); err != nil {
			return fmt.Errorf("lesson/migrate: %w", err)
		}
	}
	if _, err := db.ExecContext(ctx, idxDesignationsRequestKey); err != nil {
		return fmt.Errorf("lesson/migrate: %w", err)
	}

	return nil
}

func designationsHasColumn(ctx context.Context, db *sql.DB, name string) (bool, error) {
	rows, err := db.QueryContext(ctx, "PRAGMA table_info(designations)")
	if err != nil {
		return false, err
	}
	defer rows.Close()

	for rows.Next() {
		var (
			cid       int
			colName   string
			ctype     string
			notNull   int
			dfltValue sql.NullString
			pk        int
		)
		if err := rows.Scan(&cid, &colName, &ctype, &notNull, &dfltValue, &pk); err != nil {
			return false, err
		}
		if colName == name {
			return true, nil
		}
	}
	return false, rows.Err()
}

// lessonsHasColumn reports whether the lessons table already carries the
// named column (the generalized PRAGMA table_info probe; the original
// cards-column check was its first caller).
func lessonsHasColumn(ctx context.Context, db *sql.DB, name string) (bool, error) {
	rows, err := db.QueryContext(ctx, "PRAGMA table_info(lessons)")
	if err != nil {
		return false, err
	}
	defer rows.Close()

	for rows.Next() {
		var (
			cid       int
			colName   string
			ctype     string
			notNull   int
			dfltValue sql.NullString
			pk        int
		)
		if err := rows.Scan(&cid, &colName, &ctype, &notNull, &dfltValue, &pk); err != nil {
			return false, err
		}
		if colName == name {
			return true, nil
		}
	}
	return false, rows.Err()
}

// Lesson is one row of the lessons table. Words is the JSON-serialized word
// list on disk ([]string); Cards is the JSON-serialized word-card list
// ([]Word) — the meaning/example ground truth persisted with the classroom
// row so a same-day reconnect does not lose it. Version drives optimistic
// concurrency for advance_word writes (Executor.AdvanceWord). Category is
// the classroom's subject (Issue #10): "" on rows written before the column
// existed — normalize via NormalizeCategory, which maps it to English (the
// product was English-only then), never to "unknown".
//
// The 13.1 check-stage fields (Stage/Outcomes/Verdicts/Unchecked) persist the
// three-stage classroom across sessions: Stage records ” (no check recorded)
// / "checking" / "submitted"; Outcomes snapshots the dictation-stage behavior
// keyed by word index ("0", "1", …); Verdicts carries the stored check
// verdicts in submission order; Unchecked marks a lesson that was closed
// without finishing its check (the D-03 fallback consumer is 13.1-04).
//
// PoemID (Issue #12) is non-zero only on whole-poem 背默 rows: the practiced
// poems.id whose LINES ride in Words/Cards (one line = one card, the check
// stage's word_index space). It is the records projection's join key — the
// poem's title/author are read from the poems table at projection time, never
// duplicated into the row.
//
// Passage fields (Issue #14) are set only on passage 背默 rows: the practiced
// passages.id whose selected PARAGRAPHS ride in Words/Cards (one paragraph =
// one card, in the original's own order). The range identity (Whole +
// Paragraphs, canonical 1-based) travels WITH the attempt — the records
// projection shows the range without re-deriving it, and the submit saves
// under the same content + range identity the practice store keys on.
// PassageScopeID is traceability only (the scope row may be removed and
// re-created under a new id while this row's range stays what the child
// practiced).
type Lesson struct {
	Outcomes          map[string]WordOutcome
	LessonID          string
	Stage             string
	CreatedAt         string
	UpdatedAt         string
	Category          string
	PassageParagraphs []int
	Verdicts          []CheckVerdict
	Cards             []Word
	Words             []string
	CurrentIndex      int
	Version           int
	PoemID            int64
	PassageID         int64
	PassageScopeID    int64
	Unchecked         bool
	PassageWhole      bool
}

// Vocabulary-category values the lessons row can carry (Issue #10). The
// literals match the cards table's category enum (vocab_manager.py / D-06):
// "english" and "chinese".
const (
	CategoryEnglish = "english"
	CategoryChinese = "chinese"
)

// NormalizeCategory maps a stored/declared category onto the canonical
// English/Chinese pair: "" (a pre-Issue-#10 row or an absent field) reads as
// English — the product was English-only before Chinese classrooms existed,
// so the default is a historical fact, not a guess. Any other value also
// reads as English: the column is written only through EnsureLessonCards,
// which validates at the gate, so an off-enum value is corruption best
// rendered as the legacy default rather than surfaced to a parent as
// "unknown subject".
func NormalizeCategory(v string) string {
	if v == CategoryChinese {
		return CategoryChinese
	}
	return CategoryEnglish
}

// Store is the create-once/read path for lessons rows. Repository pattern
// mirrors database.sqliteSessionRepository: nil-guarded constructor,
// NoOpLogger fallback, sentinel errors detected via errors.Is.
type Store struct {
	db     *sql.DB
	logger infra.Logger
}

// NewStore constructs a lesson Store from an open *sql.DB. Returns an error
// if db is nil (D-47 nil-guard). If logger is nil, infra.NewNoOpLogger() is
// substituted so the store is always safe to use.
func NewStore(db *sql.DB, logger infra.Logger) (*Store, error) {
	if db == nil {
		return nil, errors.Wrap(ErrInvalidLesson, "lesson/store: *sql.DB must not be nil")
	}
	if logger == nil {
		logger = infra.NewNoOpLogger()
	}
	return &Store{db: db, logger: logger}, nil
}

// EnsureLesson creates the lessons row for lessonID if absent (INSERT OR
// IGNORE: a second call with different words returns the FIRST stored
// content, version untouched) and reads the full row back. Empty lessonID or
// empty words returns ErrInvalidLesson without touching the database.
func (s *Store) EnsureLesson(ctx context.Context, lessonID string, words []string) (Lesson, error) {
	if lessonID == "" || len(words) == 0 {
		return Lesson{}, errors.Wrap(ErrInvalidLesson, "ensure lesson: lesson_id and words must be non-empty")
	}

	wordsJSON, err := json.Marshal(words)
	if err != nil {
		return Lesson{}, errors.Wrap(err, "ensure lesson: marshal words")
	}

	if _, err = s.db.ExecContext(ctx,
		"INSERT OR IGNORE INTO lessons (lesson_id, words) VALUES (?, ?)",
		lessonID,
		string(wordsJSON),
	); err != nil {
		s.logger.Error("lesson ensure: insert failed",
			infra.F("component", "lesson/store"),
			infra.F("lesson_id", lessonID),
			infra.ErrField(err),
		)
		return Lesson{}, errors.Wrap(err, "ensure lesson: insert")
	}

	created, err := s.GetLesson(ctx, lessonID)
	if err != nil {
		return Lesson{}, errors.Wrap(err, "ensure lesson: read back")
	}
	return created, nil
}

// EnsureLessonCards creates the lessons row for lessonID if absent, carrying
// both the plain word list (extracted from the cards), the full word-card
// JSON and the classroom's subject (Issue #10), and reads the full row back.
// INSERT OR IGNORE semantics identical to EnsureLesson: first content wins, a
// second call with different cards returns the first stored content and
// leaves version untouched. category is normalized here ("" → english), so a
// legacy caller that never learned about subjects keeps writing the
// historical default.
//
// Boundary (deliberate, tested): INSERT OR IGNORE is NOT a replacement
// mechanism. A pre-existing row with an EMPTY cards column — e.g. a legacy
// row created before cards existed — is not backfilled by a second
// EnsureLessonCards call; the caller (13-08 FSM) must detect that state and
// surface lesson_unavailable instead of indexing empty word cards or
// re-running RunDue. Completed rows are likewise read-only for 13-08; this
// store never rebuilds them.
func (s *Store) EnsureLessonCards(ctx context.Context, lessonID string, cards []Word, category string) (Lesson, error) {
	if lessonID == "" || len(cards) == 0 {
		return Lesson{}, errors.Wrap(ErrInvalidLesson, "ensure lesson cards: lesson_id and cards must be non-empty")
	}

	cardsJSON, err := json.Marshal(cards)
	if err != nil {
		return Lesson{}, errors.Wrap(err, "ensure lesson cards: marshal cards")
	}

	words := make([]string, 0, len(cards))
	for _, c := range cards {
		words = append(words, c.Word)
	}
	wordsJSON, err := json.Marshal(words)
	if err != nil {
		return Lesson{}, errors.Wrap(err, "ensure lesson cards: marshal words")
	}

	if _, err = s.db.ExecContext(ctx,
		"INSERT OR IGNORE INTO lessons (lesson_id, words, cards, category) VALUES (?, ?, ?, ?)",
		lessonID,
		string(wordsJSON),
		string(cardsJSON),
		NormalizeCategory(category),
	); err != nil {
		s.logger.Error("lesson ensure cards: insert failed",
			infra.F("component", "lesson/store"),
			infra.F("lesson_id", lessonID),
			infra.ErrField(err),
		)
		return Lesson{}, errors.Wrap(err, "ensure lesson cards: insert")
	}

	created, err := s.GetLesson(ctx, lessonID)
	if err != nil {
		return Lesson{}, errors.Wrap(err, "ensure lesson cards: read back")
	}
	return created, nil
}

// EnsurePoemLesson creates the lessons row for ONE whole-poem 背默 attempt
// (Issue #12): the poem's ordered LINES ride as the cards (one line = one
// card, the check stage's word_index space), the subject is always 语文
// (poems are a Chinese content type — never parameterized), and poem_id
// stamps the practiced work. INSERT OR IGNORE semantics identical to
// EnsureLessonCards: first content wins, the store never rebuilds a row —
// a poem re-attempt resolves a FRESH lesson id from the caller instead.
func (s *Store) EnsurePoemLesson(ctx context.Context, lessonID string, poemID int64, lines []string) (Lesson, error) {
	if lessonID == "" || poemID <= 0 || len(lines) == 0 {
		return Lesson{}, errors.Wrap(ErrInvalidLesson, "ensure poem lesson: lesson_id, poem_id and lines must be non-empty")
	}

	cards := make([]Word, 0, len(lines))
	words := make([]string, 0, len(lines))
	for _, line := range lines {
		cards = append(cards, Word{Word: line})
		words = append(words, line)
	}
	cardsJSON, err := json.Marshal(cards)
	if err != nil {
		return Lesson{}, errors.Wrap(err, "ensure poem lesson: marshal cards")
	}
	wordsJSON, err := json.Marshal(words)
	if err != nil {
		return Lesson{}, errors.Wrap(err, "ensure poem lesson: marshal words")
	}

	if _, err = s.db.ExecContext(ctx,
		"INSERT OR IGNORE INTO lessons (lesson_id, words, cards, category, poem_id) VALUES (?, ?, ?, ?, ?)",
		lessonID,
		string(wordsJSON),
		string(cardsJSON),
		CategoryChinese,
		poemID,
	); err != nil {
		s.logger.Error("lesson ensure poem: insert failed",
			infra.F("component", "lesson/store"),
			infra.F("lesson_id", lessonID),
			infra.F("poem_id", poemID),
			infra.ErrField(err),
		)
		return Lesson{}, errors.Wrap(err, "ensure poem lesson: insert")
	}

	created, err := s.GetLesson(ctx, lessonID)
	if err != nil {
		return Lesson{}, errors.Wrap(err, "ensure poem lesson: read back")
	}
	return created, nil
}

// LatestPoemLessonID returns the lesson id of the poem's most recent attempt
// row ("" when the poem has never been practiced) — the resume probe of the
// poem classroom's session_init: an IN-FLIGHT row (stage ” or checking) is
// resumed under its own id, a submitted one means the attempt settled and a
// new init opens a fresh attempt. Same ORDER BY shape as ListLessons
// (created_at DESC, lesson_id DESC as the same-second tiebreak).
func (s *Store) LatestPoemLessonID(ctx context.Context, poemID int64) (string, error) {
	if poemID <= 0 {
		return "", errors.Wrap(ErrInvalidLesson, "latest poem lesson: poem_id must be positive")
	}
	var lessonID string
	err := s.db.QueryRowContext(ctx,
		"SELECT lesson_id FROM lessons WHERE poem_id = ? ORDER BY created_at DESC, lesson_id DESC LIMIT 1",
		poemID,
	).Scan(&lessonID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", nil
		}
		s.logger.Error("latest poem lesson failed",
			infra.F("component", "lesson/store"),
			infra.F("poem_id", poemID),
			infra.ErrField(err),
		)
		return "", errors.Wrap(err, "latest poem lesson")
	}
	return lessonID, nil
}

// EnsurePassageLesson creates the lessons row for ONE passage-range 背默
// attempt (Issue #14): the range's own ordered PARAGRAPHS ride as the cards
// (one paragraph = one card, the check stage's word_index space — 按所选段
// 落的原文顺序组织), the row carries the passage's own subject, and the
// passage id plus the FULL range identity (scope id for traceability, whole +
// canonical paragraphs as the identity) are stamped so the attempt's judgment,
// its resume and its save all reference the range the child actually
// practiced. INSERT OR IGNORE semantics identical to EnsurePoemLesson: first
// content wins, the store never rebuilds a row — a re-attempt resolves a
// FRESH lesson id from the caller instead.
func (s *Store) EnsurePassageLesson(ctx context.Context, lessonID string, passageID, scopeID int64, whole bool, paragraphs []int, texts []string, category string) (Lesson, error) {
	if lessonID == "" || passageID <= 0 || len(texts) == 0 {
		return Lesson{}, errors.Wrap(ErrInvalidLesson, "ensure passage lesson: lesson_id, passage_id and paragraphs must be non-empty")
	}
	// The range identity is canonicalized exactly like the passage store's
	// scope columns: sorted, deduplicated, and EMPTY iff whole (选择第 2、3
	// 段 must land as exactly 2 and 3).
	canonical, err := CanonicalParagraphs(paragraphs)
	if err != nil {
		return Lesson{}, errors.Wrap(ErrInvalidLesson, "ensure passage lesson: "+err.Error())
	}
	if !whole && len(canonical) == 0 {
		return Lesson{}, errors.Wrap(ErrInvalidLesson, "ensure passage lesson: a paragraph range must name at least one paragraph")
	}
	if whole && len(canonical) > 0 {
		return Lesson{}, errors.Wrap(ErrInvalidLesson, "ensure passage lesson: a whole range carries no paragraph set")
	}
	if whole {
		// A whole range stores the canonical EMPTY set — a non-nil empty
		// slice so the JSON column reads "[]" (never "null"), exactly the
		// passage store's own whole-scope row shape.
		canonical = []int{}
	}
	paragraphsJSON, err := json.Marshal(canonical)
	if err != nil {
		return Lesson{}, errors.Wrap(err, "ensure passage lesson: marshal paragraphs")
	}

	cards := make([]Word, 0, len(texts))
	words := make([]string, 0, len(texts))
	for _, text := range texts {
		cards = append(cards, Word{Word: text})
		words = append(words, text)
	}
	cardsJSON, err := json.Marshal(cards)
	if err != nil {
		return Lesson{}, errors.Wrap(err, "ensure passage lesson: marshal cards")
	}
	wordsJSON, err := json.Marshal(words)
	if err != nil {
		return Lesson{}, errors.Wrap(err, "ensure passage lesson: marshal words")
	}

	wholeFlag := 0
	if whole {
		wholeFlag = 1
	}
	if _, err = s.db.ExecContext(ctx,
		"INSERT OR IGNORE INTO lessons (lesson_id, words, cards, category, passage_id, passage_scope_id, passage_whole, passage_paragraphs) VALUES (?, ?, ?, ?, ?, ?, ?, ?)",
		lessonID,
		string(wordsJSON),
		string(cardsJSON),
		NormalizeCategory(category),
		passageID,
		scopeID,
		wholeFlag,
		string(paragraphsJSON),
	); err != nil {
		s.logger.Error("lesson ensure passage: insert failed",
			infra.F("component", "lesson/store"),
			infra.F("lesson_id", lessonID),
			infra.F("passage_id", passageID),
			infra.ErrField(err),
		)
		return Lesson{}, errors.Wrap(err, "ensure passage lesson: insert")
	}

	created, err := s.GetLesson(ctx, lessonID)
	if err != nil {
		return Lesson{}, errors.Wrap(err, "ensure passage lesson: read back")
	}
	return created, nil
}

// CanonicalParagraphs sorts, deduplicates and validates a 1-based paragraph
// index set (Issue #14's shared range form): every index must be a positive
// int (the passage store re-validates against the real paragraph count — this
// gate only rejects shapes that can never name a range).
func CanonicalParagraphs(paragraphs []int) ([]int, error) {
	sorted := append([]int(nil), paragraphs...)
	sort.Ints(sorted)
	out := make([]int, 0, len(sorted))
	for _, i := range sorted {
		if i < 1 {
			return nil, fmt.Errorf("paragraph index %d is not positive", i)
		}
		if len(out) == 0 || out[len(out)-1] != i {
			out = append(out, i)
		}
	}
	return out, nil
}

// LatestPassageLessonID returns the lesson id of the passage RANGE's most
// recent attempt row ("" when never practiced) — the passage classroom's
// resume probe (Issue #14). The lookup keys on the content + range identity
// (passage_id + whole + canonical paragraphs), NOT the scope row id: a range
// the parent removed and re-added keeps resuming under its in-flight attempt.
// Same ORDER BY shape as ListLessons (created_at DESC, lesson_id DESC as the
// same-second tiebreak).
func (s *Store) LatestPassageLessonID(ctx context.Context, passageID int64, whole bool, paragraphs []int) (string, error) {
	if passageID <= 0 {
		return "", errors.Wrap(ErrInvalidLesson, "latest passage lesson: passage_id must be positive")
	}
	canonical, err := CanonicalParagraphs(paragraphs)
	if err != nil {
		return "", errors.Wrap(ErrInvalidLesson, "latest passage lesson: "+err.Error())
	}
	wholeFlag := 0
	encoded := "[]"
	if whole {
		wholeFlag = 1
	} else {
		if len(canonical) == 0 {
			return "", errors.Wrap(ErrInvalidLesson, "latest passage lesson: a paragraph range must name at least one paragraph")
		}
		blob, merr := json.Marshal(canonical)
		if merr != nil {
			return "", errors.Wrap(merr, "latest passage lesson: marshal paragraphs")
		}
		encoded = string(blob)
	}
	var lessonID string
	err = s.db.QueryRowContext(ctx,
		"SELECT lesson_id FROM lessons WHERE passage_id = ? AND passage_whole = ? AND passage_paragraphs = ? ORDER BY created_at DESC, lesson_id DESC LIMIT 1",
		passageID, wholeFlag, encoded,
	).Scan(&lessonID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", nil
		}
		s.logger.Error("latest passage lesson failed",
			infra.F("component", "lesson/store"),
			infra.F("passage_id", passageID),
			infra.ErrField(err),
		)
		return "", errors.Wrap(err, "latest passage lesson")
	}
	return lessonID, nil
}

// decodeParagraphsJSON decodes the lessons passage_paragraphs column with
// legacy-default tolerance (empty/invalid raw → the empty set — a
// non-passage row never carries one).
func decodeParagraphsJSON(raw string) []int {
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

// decodeOutcomesJSON decodes the lessons outcomes column with legacy-default
// tolerance: an empty raw value (a row written before the column's backfill)
// decodes to the empty map, corrupted non-empty JSON surfaces as an error.
func decodeOutcomesJSON(raw string) (map[string]WordOutcome, error) {
	if strings.TrimSpace(raw) == "" {
		return map[string]WordOutcome{}, nil
	}
	var m map[string]WordOutcome
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return nil, err
	}
	if m == nil {
		m = map[string]WordOutcome{}
	}
	return m, nil
}

// decodeVerdictsJSON decodes the lessons verdicts column with the same
// legacy-default tolerance as decodeOutcomesJSON.
func decodeVerdictsJSON(raw string) ([]CheckVerdict, error) {
	if strings.TrimSpace(raw) == "" {
		return []CheckVerdict{}, nil
	}
	var v []CheckVerdict
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		return nil, err
	}
	if v == nil {
		v = []CheckVerdict{}
	}
	return v, nil
}

// GetLesson returns the lessons row for lessonID. Returns ErrLessonNotFound
// when no row exists. A corrupted words or cards column (invalid JSON)
// surfaces as a wrapped unmarshal error. The four 13.1 check-stage columns
// are decoded with legacy-default tolerance: an empty raw value (a row that
// predates a column's backfill) decodes to the zero default instead of
// failing, so historical rows keep reading as stageless/unchecked.
func (s *Store) GetLesson(ctx context.Context, lessonID string) (Lesson, error) {
	var (
		l                Lesson
		wordsJSON        string
		cardsJSON        string
		stage            string
		outcomesJSON     string
		verdictsJSON     string
		unchecked        int
		passageWhole     int
		passageParasJSON string
	)

	err := s.db.QueryRowContext(ctx,
		"SELECT lesson_id, words, cards, stage, outcomes, verdicts, unchecked, current_index, version, created_at, updated_at, category, poem_id, passage_id, passage_scope_id, passage_whole, passage_paragraphs FROM lessons WHERE lesson_id = ?",
		lessonID,
	).Scan(
		&l.LessonID,
		&wordsJSON,
		&cardsJSON,
		&stage,
		&outcomesJSON,
		&verdictsJSON,
		&unchecked,
		&l.CurrentIndex,
		&l.Version,
		&l.CreatedAt,
		&l.UpdatedAt,
		&l.Category,
		&l.PoemID,
		&l.PassageID,
		&l.PassageScopeID,
		&passageWhole,
		&passageParasJSON,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Lesson{}, errors.Wrap(ErrLessonNotFound, fmt.Sprintf("get lesson %q", lessonID))
		}
		s.logger.Error("lesson get failed",
			infra.F("component", "lesson/store"),
			infra.F("lesson_id", lessonID),
			infra.ErrField(err),
		)
		return Lesson{}, errors.Wrap(err, "get lesson")
	}

	if err = json.Unmarshal([]byte(wordsJSON), &l.Words); err != nil {
		s.logger.Error("lesson get: unmarshal words failed",
			infra.F("component", "lesson/store"),
			infra.F("lesson_id", lessonID),
			infra.ErrField(err),
		)
		return Lesson{}, errors.Wrap(err, fmt.Sprintf("get lesson %q: unmarshal words", lessonID))
	}

	if err = json.Unmarshal([]byte(cardsJSON), &l.Cards); err != nil {
		s.logger.Error("lesson get: unmarshal cards failed",
			infra.F("component", "lesson/store"),
			infra.F("lesson_id", lessonID),
			infra.ErrField(err),
		)
		return Lesson{}, errors.Wrap(err, fmt.Sprintf("get lesson %q: unmarshal cards", lessonID))
	}

	// Check-stage columns: legacy-default tolerance for EMPTY raw values;
	// corrupted non-empty JSON still surfaces as a wrapped error (the same
	// honesty contract as words/cards — a silently-empty verdicts column
	// could double-submit FSRS reviews).
	l.Stage = stage
	l.Unchecked = unchecked != 0
	// Passage range columns (Issue #14): the whole flag plus the canonical
	// paragraph set, with the same legacy-default tolerance (a non-passage
	// row reads PassageID 0 and the empty set — the projection keys off the
	// id, never off the paragraphs alone).
	l.PassageWhole = passageWhole == 1
	l.PassageParagraphs = decodeParagraphsJSON(passageParasJSON)
	outcomes, err := decodeOutcomesJSON(outcomesJSON)
	if err != nil {
		s.logger.Error("lesson get: unmarshal outcomes failed",
			infra.F("component", "lesson/store"),
			infra.F("lesson_id", lessonID),
			infra.ErrField(err),
		)
		return Lesson{}, errors.Wrap(err, fmt.Sprintf("get lesson %q: unmarshal outcomes", lessonID))
	}
	l.Outcomes = outcomes
	verdicts, err := decodeVerdictsJSON(verdictsJSON)
	if err != nil {
		s.logger.Error("lesson get: unmarshal verdicts failed",
			infra.F("component", "lesson/store"),
			infra.F("lesson_id", lessonID),
			infra.ErrField(err),
		)
		return Lesson{}, errors.Wrap(err, fmt.Sprintf("get lesson %q: unmarshal verdicts", lessonID))
	}
	l.Verdicts = verdicts

	return l, nil
}

// SummarizeLesson projects an already-loaded Lesson into its parent-facing
// summary (13.3-02) — the SAME manual-only counting rule ListLessons applies,
// kept in ONE function so the list and detail projections cannot drift
// (完成勾≠写对: Correct/Wrong count manual verdicts exclusively).
func SummarizeLesson(l Lesson) LessonSummary {
	sum := LessonSummary{
		LessonID:     l.LessonID,
		CreatedAt:    l.CreatedAt,
		Stage:        l.Stage,
		Unchecked:    l.Unchecked,
		CurrentIndex: l.CurrentIndex,
		WordCount:    len(l.Cards),
		Category:     NormalizeCategory(l.Category),
		PoemID:       l.PoemID,
		// Issue #14: the passage range identity rides the summary verbatim —
		// the records view shows the range the child actually practiced, and
		// the passage title/author resolve through PassageID's own join.
		PassageID:         l.PassageID,
		PassageWhole:      l.PassageWhole,
		PassageParagraphs: l.PassageParagraphs,
	}
	for _, v := range l.Verdicts {
		if !v.Manual {
			continue // OCR prefills and other derived verdicts stay unconfirmed
		}
		if v.Correct {
			sum.Correct++
		} else {
			sum.Wrong++
		}
	}
	sum.Unconfirmed = sum.WordCount - (sum.Correct + sum.Wrong)
	if sum.Unconfirmed < 0 {
		// A corrupt row carrying more manual verdicts than words must not
		// project a negative count to the parent view.
		sum.Unconfirmed = 0
	}
	sum.Completed = LessonCompleted(sum.Stage, sum.CurrentIndex, sum.WordCount)
	return sum
}

// LessonCompleted reports whether a lessons row counts as completed for the
// parent-facing read projection (13.3-02): the whitelist follows the
// authoritative completion branch table (pkg/rt/turn.go lesson-init switch) —
// ONLY stage=submitted, or a legacy row (stage=”) whose cursor walked past
// the last card, is completed; checking, in-progress and empty-cards rows are
// not. Unchecked is deliberately not an input: the D-03 fallback also lands
// at stage=submitted, so it drives a display marker only.
func LessonCompleted(stage string, currentIndex, cardLen int) bool {
	if stage == StageSubmitted {
		return true
	}
	return stage == "" && cardLen > 0 && currentIndex >= cardLen
}

// LessonSummary is one row of the parent-facing recent-lessons projection
// (13.3-02): identity + display shape, NO word surface fields (the leak
// guard's first line — the type itself cannot carry a word). Correct/Wrong
// count ONLY manual verdicts (完成勾≠写对); Unconfirmed is the remainder.
type LessonSummary struct {
	LessonID  string
	CreatedAt string
	Stage     string
	// Category is the row's subject, already normalized ('' → english) —
	// the parent records view renders a subject badge from it (Issue #10).
	Category string
	// PassageID is non-zero only on passage 背默 rows (Issue #14) — the
	// records projection's join key for the passage's title/author.
	// PassageWhole/PassageParagraphs carry the practiced RANGE identity so
	// the parent view shows the range the child actually practiced (实际范
	// 围), and a paragraph attempt never reads as a whole-passage one.
	PassageParagraphs []int
	PassageID         int64
	// PoemID is non-zero only on whole-poem 背默 rows (Issue #12) — the
	// records projection's join key for the poem's title/author. The summary
	// carries the id ONLY; the title never rides a per-row round trip.
	PoemID       int64
	CurrentIndex int
	WordCount    int
	Correct      int
	Wrong        int
	Unconfirmed  int
	// PassageWhole: see the Passage fields above (Issue #14).
	PassageWhole bool
	Unchecked    bool
	Completed    bool
}

// ListLessons limit normalization — the bounds live HERE, in the store, so a
// client-supplied large value can never penetrate (T-13.3-05): <=0 → default
// 20, >50 → hard cap 50. The single-connection pool (database.Open
// SetMaxOpenConns(1)) shares every query with the classroom write path; an
// unbounded parent query would stall the child's lesson.
const (
	listLessonsDefaultLimit = 20
	listLessonsMaxLimit     = 50
)

func normalizeListLessonsLimit(limit int) int {
	switch {
	case limit <= 0:
		return listLessonsDefaultLimit
	case limit > listLessonsMaxLimit:
		return listLessonsMaxLimit
	default:
		return limit
	}
}

// ListLessons returns the newest lessons rows (created_at DESC — RESEARCH A4:
// same-day r0x- practice rows sort by timestamp, not lesson-id lexicographic
// order; lesson_id DESC is the same-second tiebreak), bounded by the
// normalized limit. Each row carries the summary aggregates ONLY: word count
// from the cards JSON, Correct/Wrong counting manual verdicts exclusively,
// Unconfirmed as the remainder. Explicit columns, one bounded query, one
// connection discipline (P4). A corrupted cards/verdicts JSON surfaces as a
// wrapped error — a silently-empty verdict list would misstate the counts.
func (s *Store) ListLessons(ctx context.Context, limit int) ([]LessonSummary, error) {
	rows, err := s.db.QueryContext(ctx,
		"SELECT lesson_id, stage, unchecked, current_index, cards, verdicts, created_at, category, poem_id, passage_id, passage_scope_id, passage_whole, passage_paragraphs FROM lessons ORDER BY created_at DESC, lesson_id DESC LIMIT ?",
		normalizeListLessonsLimit(limit),
	)
	if err != nil {
		s.logger.Error("lesson list failed",
			infra.F("component", "lesson/store"),
			infra.ErrField(err),
		)
		return nil, errors.Wrap(err, "list lessons")
	}
	defer rows.Close()

	var summaries []LessonSummary
	for rows.Next() {
		var (
			row              Lesson
			cardsJSON        string
			verdictsJSON     string
			unchecked        int
			passageWhole     int
			passageParasJSON string
		)
		if err := rows.Scan(&row.LessonID, &row.Stage, &unchecked, &row.CurrentIndex, &cardsJSON, &verdictsJSON, &row.CreatedAt, &row.Category, &row.PoemID, &row.PassageID, &row.PassageScopeID, &passageWhole, &passageParasJSON); err != nil {
			return nil, errors.Wrap(err, "list lessons: scan")
		}
		row.Unchecked = unchecked != 0
		row.PassageWhole = passageWhole == 1
		row.PassageParagraphs = decodeParagraphsJSON(passageParasJSON)

		cards, err := decodeCardsJSON(cardsJSON)
		if err != nil {
			s.logger.Error("lesson list: unmarshal cards failed",
				infra.F("component", "lesson/store"),
				infra.F("lesson_id", row.LessonID),
				infra.ErrField(err),
			)
			return nil, errors.Wrap(err, fmt.Sprintf("list lessons %q: unmarshal cards", row.LessonID))
		}
		row.Cards = cards

		verdicts, err := decodeVerdictsJSON(verdictsJSON)
		if err != nil {
			s.logger.Error("lesson list: unmarshal verdicts failed",
				infra.F("component", "lesson/store"),
				infra.F("lesson_id", row.LessonID),
				infra.ErrField(err),
			)
			return nil, errors.Wrap(err, fmt.Sprintf("list lessons %q: unmarshal verdicts", row.LessonID))
		}
		row.Verdicts = verdicts

		summaries = append(summaries, SummarizeLesson(row))
	}
	return summaries, rows.Err()
}

// decodeCardsJSON decodes the lessons cards column with the same
// legacy-default tolerance as decodeOutcomesJSON (an empty raw value reads as
// the empty slice; corrupted non-empty JSON surfaces as an error).
func decodeCardsJSON(raw string) ([]Word, error) {
	if strings.TrimSpace(raw) == "" {
		return []Word{}, nil
	}
	var cards []Word
	if err := json.Unmarshal([]byte(raw), &cards); err != nil {
		return nil, err
	}
	if cards == nil {
		cards = []Word{}
	}
	return cards, nil
}

// Designation status values — the two-phase designation lifecycle (Issue
// #80 指定默写). pending = created (the designate command selected and
// locked the words) but no classroom row written yet; consumed = the
// lessons row landed and ConsumeDesignation flipped the row — exactly
// once, never back.
const (
	DesignationStatusPending  = "pending"
	DesignationStatusConsumed = "consumed"
)

// designationCardLimit is the single-session cap (AC-11 单次最多 100 个),
// mirroring pybridge.MaxDesignateLimit and vocab_manager.py
// MAX_DESIGNATE_LIMIT. The store re-validates so no caller can store an
// oversized snapshot even if a bridge cap drifts.
const designationCardLimit = 100

// DesignationCard is one selected word inside a designation row: the card
// identity plus the word/meaning/example ground truth the classroom row
// needs. The json tags match pybridge.DesignateCard's payload so the api
// layer maps the snapshot losslessly.
type DesignationCard struct {
	Word    string `json:"word"`
	Meaning string `json:"meaning"`
	Example string `json:"example"`
	ID      int64  `json:"id"`
}

// Designation is one row of the designations table. Cards is the decoded
// snapshot column; CreatedAt/ConsumedAt are SQLite datetime('now') strings
// (ConsumedAt stays "" while pending).
type Designation struct {
	Lesson     string
	Category   string
	Status     string
	CreatedAt  string
	ConsumedAt string
	RequestKey string
	Cards      []DesignationCard
	ID         int64
	TextbookID int64
}

// DesignationInput is CreateDesignation's payload: the designation's
// textbook/lesson identity, its subject, and the selected card snapshot.
type DesignationInput struct {
	Lesson     string
	Category   string
	Cards      []DesignationCard
	TextbookID int64
}

// CreateDesignation stores one CREATED designation as pending: the words
// the designate command selected and locked, snapshotted together with the
// textbook/lesson identity and the subject. It writes no progress,
// include_in_scope or participates state — those columns belong to their
// own owners (创建不写进度、加入标记和参与开关). Validation (identity
// non-empty, 1..designationCardLimit cards) rejects before anything is
// written; category is normalized like every lessons row ("" → english).
func (s *Store) CreateDesignation(ctx context.Context, in DesignationInput) (Designation, error) {
	if in.TextbookID <= 0 || in.Lesson == "" {
		return Designation{}, errors.Wrap(ErrInvalidLesson,
			"create designation: textbook_id and lesson must be non-empty")
	}
	if len(in.Cards) == 0 || len(in.Cards) > designationCardLimit {
		return Designation{}, errors.Wrap(ErrInvalidLesson, fmt.Sprintf(
			"create designation: cards must contain 1..%d entries, got %d",
			designationCardLimit, len(in.Cards)))
	}
	cardsJSON, err := json.Marshal(in.Cards)
	if err != nil {
		return Designation{}, errors.Wrap(err, "create designation: marshal cards")
	}
	category := NormalizeCategory(in.Category)
	res, err := s.db.ExecContext(ctx,
		"INSERT INTO designations (textbook_id, lesson, category, cards, status, created_at, consumed_at) "+
			"VALUES (?, ?, ?, ?, ?, datetime('now'), '')",
		in.TextbookID, in.Lesson, category, string(cardsJSON), DesignationStatusPending,
	)
	if err != nil {
		s.logger.Error("designation create: insert failed",
			infra.F("component", "lesson/store"),
			infra.F("textbook_id", in.TextbookID),
			infra.F("lesson", in.Lesson),
			infra.ErrField(err),
		)
		return Designation{}, errors.Wrap(err, "create designation: insert")
	}
	id, err := res.LastInsertId()
	if err != nil {
		return Designation{}, errors.Wrap(err, "create designation: last insert id")
	}
	// Single-statement contract: the returned Designation carries what the
	// INSERT wrote; created_at/consumed_at stay DB-side (read paths own
	// them).
	return Designation{
		ID:         id,
		TextbookID: in.TextbookID,
		Lesson:     in.Lesson,
		Category:   category,
		Cards:      in.Cards,
		Status:     DesignationStatusPending,
	}, nil
}

// GetDesignationByRequestKey loads one designations row by its client
// request key (Issue #80 U10 idempotent create). ErrDesignationNotFound
// when no row exists.
func (s *Store) GetDesignationByRequestKey(ctx context.Context, requestKey string) (Designation, error) {
	requestKey = strings.TrimSpace(requestKey)
	if requestKey == "" {
		return Designation{}, errors.Wrap(ErrInvalidLesson, "get designation: request key is required")
	}
	var (
		d         Designation
		cardsJSON string
	)
	err := s.db.QueryRowContext(ctx,
		"SELECT id, textbook_id, lesson, category, cards, status, created_at, consumed_at, request_key "+
			"FROM designations WHERE request_key = ?",
		requestKey,
	).Scan(&d.ID, &d.TextbookID, &d.Lesson, &d.Category, &cardsJSON, &d.Status,
		&d.CreatedAt, &d.ConsumedAt, &d.RequestKey)
	if errors.Is(err, sql.ErrNoRows) {
		return Designation{}, errors.Wrap(ErrDesignationNotFound,
			fmt.Sprintf("get designation by request key %q", requestKey))
	}
	if err != nil {
		return Designation{}, errors.Wrap(err, "get designation by request key")
	}
	cards, err := decodeDesignationCardsJSON(cardsJSON)
	if err != nil {
		return Designation{}, errors.Wrap(err, "get designation by request key: decode cards")
	}
	d.Cards = cards
	return d, nil
}

func decodeDesignationCardsJSON(raw string) ([]DesignationCard, error) {
	if strings.TrimSpace(raw) == "" {
		return []DesignationCard{}, nil
	}
	var cards []DesignationCard
	if err := json.Unmarshal([]byte(raw), &cards); err != nil {
		return nil, err
	}
	if cards == nil {
		cards = []DesignationCard{}
	}
	return cards, nil
}

// ConsumeDesignation flips a pending designation to consumed EXACTLY ONCE
// (Issue #80): the WHERE status='pending' guard makes the first call the
// only one that wins, and callers may invoke it only AFTER the classroom
// (lessons) row write succeeded — 课堂行写入成功时才消费, a crash between
// the two statements leaves the row pending and therefore re-consumable.
// Returns true the first time; false with a nil error when the row was
// already consumed (the deliberate no-win, not an error); false with
// ErrDesignationNotFound when no such row exists.
func (s *Store) ConsumeDesignation(ctx context.Context, id int64) (bool, error) {
	if id <= 0 {
		return false, errors.Wrap(ErrInvalidLesson, "consume designation: id must be positive")
	}
	res, err := s.db.ExecContext(ctx,
		"UPDATE designations SET status = ?, consumed_at = datetime('now') WHERE id = ? AND status = ?",
		DesignationStatusConsumed, id, DesignationStatusPending,
	)
	if err != nil {
		s.logger.Error("designation consume: update failed",
			infra.F("component", "lesson/store"),
			infra.F("designation_id", id),
			infra.ErrField(err),
		)
		return false, errors.Wrap(err, "consume designation: update")
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, errors.Wrap(err, "consume designation: rows affected")
	}
	if n == 1 {
		return true, nil
	}
	// Zero affected rows: already consumed or missing — distinguish so a
	// caller never reads "nothing to do" as "never existed".
	var status string
	err = s.db.QueryRowContext(ctx,
		"SELECT status FROM designations WHERE id = ?", id).Scan(&status)
	if errors.Is(err, sql.ErrNoRows) {
		return false, errors.Wrap(ErrDesignationNotFound, fmt.Sprintf("consume designation %d", id))
	}
	if err != nil {
		s.logger.Error("designation consume: read back failed",
			infra.F("component", "lesson/store"),
			infra.F("designation_id", id),
			infra.ErrField(err),
		)
		return false, errors.Wrap(err, "consume designation: read back")
	}
	if status == DesignationStatusConsumed {
		return false, nil // the idempotent no-win: the first consume is history
	}
	return false, errors.Wrap(ErrInvalidLesson,
		fmt.Sprintf("consume designation %d: unexpected status %q", id, status))
}
