// Package poem stores WHOLE classical poems as complete works (Issue #11):
// a 语文 mixed-page import recognizes 词语 entries (which live in the
// Python-owned cards table) and 整首古诗, and the poem is ONE unit — title,
// optional author, and the ordered verse lines — never a bag of unrelated
// word cards.
//
// Ownership (the pkg/lesson precedent): poems and poem_assignments are
// GO-OWNED tables. The write-separation contract (D-03-a) assigns cards and
// card_assignments to Python; a poem is not a card (no FSRS state, no unit
// column, a different shape entirely), so its table belongs to the Go
// service the same way lessons/practice_logs/save_receipts do. The
// Python-owned textbooks table stays untouched: an assignment REFERENCES a
// textbook row by id and the join reads its name/lessons/learned_lessons,
// so the Issue #4 progress rules govern poems without Python learning
// about them.
//
// Identity (AC: 重复确认不复制作品, 完整文本身份不能只以标题判断): a
// poem's fingerprint hashes the NORMALIZED full work — trimmed title,
// trimmed author, every trimmed non-empty line in order. Re-confirming the
// same poem resolves to the stored row and reports existing; a DIFFERENT
// poem that merely shares a title gets its own row.
package poem

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/pkg/errors"

	"github.com/xiaotian/home-tutor-agent/pkg/infra"
)

// Sentinel errors. Callers MUST branch via errors.Is (D-05).
var (
	// ErrPoemNotFound is returned by Assign when no poems row carries the
	// given id.
	ErrPoemNotFound = errors.New("poem: poem not found")

	// ErrInvalidPoem is returned when caller-supplied arguments are invalid
	// (a nil store input, a commit without drafts). Nothing is written when
	// it is returned.
	ErrInvalidPoem = errors.New("poem: invalid poem arguments")
)

// Table DDL — Go-owned, registered by Migrate (the database/lesson package
// CREATE TABLE IF NOT EXISTS pattern; idempotent on every boot).
var (
	poemsDDL = `CREATE TABLE IF NOT EXISTS poems (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    title TEXT NOT NULL,
    author TEXT NOT NULL DEFAULT '',
    lines TEXT NOT NULL,               -- JSON array, verse order
    fingerprint TEXT NOT NULL,         -- normalized full-work identity
    category TEXT NOT NULL DEFAULT 'chinese',
    created_at TEXT NOT NULL DEFAULT (datetime('now'))
)`

	idxPoemsFingerprint = `CREATE UNIQUE INDEX IF NOT EXISTS idx_poems_fingerprint ON poems(fingerprint)`

	poemAssignmentsDDL = `CREATE TABLE IF NOT EXISTS poem_assignments (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    poem_id INTEGER NOT NULL,
    textbook_id INTEGER NOT NULL,
    lesson TEXT NOT NULL DEFAULT '',
    include_in_scope INTEGER NOT NULL DEFAULT 0,
    created_at TEXT NOT NULL DEFAULT (datetime('now')),
    UNIQUE(poem_id, textbook_id)
)`

	idxPoemAssignmentsTextbook = `CREATE INDEX IF NOT EXISTS idx_poem_assignments_textbook ON poem_assignments(textbook_id)`
)

// Migrate creates the poem package's Go-owned tables and indexes if they do
// not exist. Idempotent: safe to call on every boot, mirroring
// lesson.Migrate (which main.go calls right after database.Migrate).
func Migrate(ctx context.Context, db *sql.DB) error {
	for _, stmt := range []string{poemsDDL, idxPoemsFingerprint, poemAssignmentsDDL, idxPoemAssignmentsTextbook} {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("poem/migrate: %w", err)
		}
	}
	return nil
}

// Assignment is one poem's textbook attribution (the poem twin of
// database.TextbookAssignment): the textbook's name rides along so the
// content view renders chips without a second join client-side.
type Assignment struct {
	TextbookName   string `json:"name"`
	Lesson         string `json:"lesson"`
	TextbookID     int64  `json:"id"`
	IncludeInScope bool   `json:"includeInScope"`
}

// Scope-reason values — why a poem is (or is not) inside the practice
// scope under the Issue #4 rules. Frozen strings: the parent content view
// reads them. For now the verdict is display/management state only — the
// poem CLASSROOM lands with the next task (#12); poems never enter the
// word-dictation selection (they are not cards at all).
const (
	// ScopeSupplementary: no attribution — supplementary content, in scope
	// by default (the 补充词汇默认参与 rule; for poems the flag only gates
	// the future poem classroom).
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

// Poem is one stored work plus its management projection: the ordered
// lines decoded from the JSON column, the attribution rows, and the scope
// verdict computed under the Issue #4 rules.
type Poem struct {
	Title       string
	Author      string
	Category    string
	CreatedAt   string
	Lines       []string
	Assignments []Assignment
	Fingerprint string
	InScope     bool
	ScopeReason string
	ID          int64
}

// Draft is one confirmed poem on the commit path — exactly what the parent
// reviewed in the preview (title/author edited, lines in order, the
// per-poem lesson).
type Draft struct {
	Title  string
	Author string
	Lines  []string
	Lesson string
}

// Commit status values (AC: 重复确认不复制作品 — the receipt names what
// actually landed, per item).
const (
	StatusAdded    = "added"
	StatusExisting = "existing"
	StatusInvalid  = "invalid"
)

// CommitOutcome is one draft's real result: the stored poem's id for
// added AND existing alike (an existing poem keeps its row — attribution
// still applies), or the reason an invalid draft never wrote.
type CommitOutcome struct {
	Title  string `json:"title"`
	Status string `json:"status"`
	Reason string `json:"reason,omitempty"`
	PoemID int64  `json:"poem_id"`
}

// CommitInput is one confirmed commit: the drafts in preview order, the
// subject (poems are a 语文 content type — "chinese"), and the optional
// attribution target established BEFORE the commit (0 = 不指定教材; the
// lesson rides each draft).
type CommitInput struct {
	Drafts     []Draft
	Category   string
	TextbookID int64
}

// normalizeLines trims every line and drops the empty ones, preserving
// order (分行和顺序保留; blank rows are editing noise, never verse).
func normalizeLines(lines []string) []string {
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		if line = strings.TrimSpace(line); line != "" {
			out = append(out, line)
		}
	}
	return out
}

// Fingerprint hashes the normalized FULL work — title, author and every
// line in order — so identity is the complete text, never the title alone
// (两首同题不同文的诗是两首诗; the same poem re-confirmed is the same row).
func Fingerprint(title, author string, lines []string) string {
	payload := strings.TrimSpace(title) + "\n" + strings.TrimSpace(author) + "\n" +
		strings.Join(normalizeLines(lines), "\n")
	sum := sha256.Sum256([]byte(payload))
	return hex.EncodeToString(sum[:])
}

// Store is the poem read/write path. Repository pattern mirrors
// pkg/lesson.Store: nil-guarded constructor, NoOpLogger fallback, sentinel
// errors via errors.Is.
type Store struct {
	db     *sql.DB
	logger infra.Logger
}

// NewStore constructs a poem Store from an open *sql.DB (D-47 nil-guard;
// nil logger degrades to the NoOp one).
func NewStore(db *sql.DB, logger infra.Logger) (*Store, error) {
	if db == nil {
		return nil, errors.Wrap(ErrInvalidPoem, "poem/store: *sql.DB must not be nil")
	}
	if logger == nil {
		logger = infra.NewNoOpLogger()
	}
	return &Store{db: db, logger: logger}, nil
}

// Commit writes the confirmed drafts in ONE transaction. Each draft
// resolves its fingerprint FIRST (one indexed read): a hit — in the library
// or earlier in this same batch — reports existing with the stored row's
// id and never inserts; a miss inserts and reports added. 重复确认不复制
// 作品, and the whole commit is all-or-nothing at the transaction level.
// When TextbookID > 0 every committed poem also upserts its attribution
// row: the lesson refreshes, while include_in_scope stays untouched on an
// existing row — a re-import must never silently clear a parent's explicit
// include (the sticky-include rule the word path applies).
func (s *Store) Commit(ctx context.Context, in CommitInput) ([]CommitOutcome, error) {
	if len(in.Drafts) == 0 {
		return nil, errors.Wrap(ErrInvalidPoem, "poem commit: no drafts")
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, errors.Wrap(err, "poem commit: begin")
	}
	defer tx.Rollback() //nolint:errcheck // no-op once Commit succeeds

	outcomes := make([]CommitOutcome, 0, len(in.Drafts))
	seen := make(map[string]int64, len(in.Drafts))
	for _, d := range in.Drafts {
		title := strings.TrimSpace(d.Title)
		lines := normalizeLines(d.Lines)
		author := strings.TrimSpace(d.Author)
		if title == "" || len(lines) == 0 {
			// The API gate rejects these first; the store stays defensive —
			// an empty shell is reported per-item, never a whole-commit
			// failure that would hide the siblings' real outcomes.
			outcomes = append(outcomes, CommitOutcome{
				Title: title, Status: StatusInvalid, Reason: "no title or no lines",
			})
			continue
		}
		fp := Fingerprint(title, author, lines)

		var poemID int64
		var status string
		if prior, ok := seen[fp]; ok {
			// Earlier in THIS batch — the first sighting's row governs.
			poemID, status = prior, StatusExisting
		} else {
			err = tx.QueryRowContext(ctx,
				"SELECT id FROM poems WHERE fingerprint = ?", fp).Scan(&poemID)
			switch {
			case err == nil:
				status = StatusExisting
			case errors.Is(err, sql.ErrNoRows):
				linesJSON, merr := json.Marshal(lines)
				if merr != nil {
					return nil, errors.Wrap(merr, "poem commit: marshal lines")
				}
				var res sql.Result
				res, err = tx.ExecContext(ctx,
					"INSERT INTO poems (title, author, lines, fingerprint, category) VALUES (?, ?, ?, ?, ?)",
					title, author, string(linesJSON), fp, in.Category)
				if err != nil {
					s.logger.Error("poem commit: insert failed",
						infra.F("component", "poem/store"),
						infra.F("title", title),
						infra.ErrField(err),
					)
					return nil, errors.Wrap(err, "poem commit: insert")
				}
				if poemID, err = res.LastInsertId(); err != nil {
					return nil, errors.Wrap(err, "poem commit: last insert id")
				}
				status = StatusAdded
			default:
				return nil, errors.Wrap(err, "poem commit: fingerprint lookup")
			}
			seen[fp] = poemID
		}

		if in.TextbookID > 0 {
			if _, err = tx.ExecContext(ctx,
				`INSERT INTO poem_assignments (poem_id, textbook_id, lesson, include_in_scope)
				 VALUES (?, ?, ?, 0)
				 ON CONFLICT(poem_id, textbook_id) DO UPDATE SET lesson = excluded.lesson`,
				poemID, in.TextbookID, strings.TrimSpace(d.Lesson),
			); err != nil {
				s.logger.Error("poem commit: assignment upsert failed",
					infra.F("component", "poem/store"),
					infra.F("poem_id", poemID),
					infra.ErrField(err),
				)
				return nil, errors.Wrap(err, "poem commit: assignment")
			}
		}

		outcomes = append(outcomes, CommitOutcome{Title: title, Status: status, PoemID: poemID})
	}

	if err = tx.Commit(); err != nil {
		return nil, errors.Wrap(err, "poem commit: commit")
	}
	return outcomes, nil
}

// Assign upserts ONE poem's attribution on the management path (统一内容页
// 的课程归属管理): the lesson and include_in_scope refresh together — this
// is the parent's explicit management action, unlike the re-import path
// which preserves an existing include. A poem id with no row reports
// ErrPoemNotFound; the textbook's existence is the CALLER's pre-check (it
// owns the TextbookRepository read).
func (s *Store) Assign(ctx context.Context, poemID, textbookID int64, lesson string, includeInScope bool) error {
	if poemID <= 0 || textbookID <= 0 {
		return errors.Wrap(ErrInvalidPoem, "poem assign: ids must be positive")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return errors.Wrap(err, "poem assign: begin")
	}
	defer tx.Rollback() //nolint:errcheck // no-op once Commit succeeds

	var one int
	if err = tx.QueryRowContext(ctx, "SELECT 1 FROM poems WHERE id = ?", poemID).Scan(&one); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return errors.Wrap(ErrPoemNotFound, fmt.Sprintf("poem assign: poem %d", poemID))
		}
		return errors.Wrap(err, "poem assign: poem lookup")
	}
	if _, err = tx.ExecContext(ctx,
		`INSERT INTO poem_assignments (poem_id, textbook_id, lesson, include_in_scope)
		 VALUES (?, ?, ?, ?)
		 ON CONFLICT(poem_id, textbook_id) DO UPDATE SET lesson = excluded.lesson, include_in_scope = excluded.include_in_scope`,
		poemID, textbookID, strings.TrimSpace(lesson), includeInScope,
	); err != nil {
		s.logger.Error("poem assign: upsert failed",
			infra.F("component", "poem/store"),
			infra.F("poem_id", poemID),
			infra.ErrField(err),
		)
		return errors.Wrap(err, "poem assign: upsert")
	}
	return tx.Commit()
}

// ListFilter narrows a List call: an empty Category returns every poem,
// an empty Query matches all.
type ListFilter struct {
	Category string
	Query    string
}

// List returns the stored poems (newest first — the content view's newest-
// first convention) with their attribution projection and scope verdict.
// The attribution join is a LEFT JOIN against the Python-owned textbooks
// table through database.Open's shared *sql.DB; on a pre-migration database
// (no textbooks table yet — nothing has ever created one) the join degrades
// to nameless assignments and progress-pending scope, never an error: the
// poems still render (the card repo's same degraded-read discipline).
func (s *Store) List(ctx context.Context, filter ListFilter) ([]Poem, error) {
	query := `SELECT p.id, p.title, p.author, p.lines, p.category, p.created_at FROM poems p`
	args := []any{}
	if filter.Category != "" {
		query += " WHERE p.category = ?"
		args = append(args, filter.Category)
	}
	if filter.Query != "" {
		clause := "(p.title LIKE ? ESCAPE '\\' OR p.author LIKE ? ESCAPE '\\' OR p.lines LIKE ? ESCAPE '\\')"
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
		s.logger.Error("poem list failed",
			infra.F("component", "poem/store"),
			infra.ErrField(err),
		)
		return nil, errors.Wrap(err, "poem list")
	}
	defer rows.Close()

	poems := []Poem{}
	byID := map[int64]*Poem{}
	for rows.Next() {
		var p Poem
		var linesJSON string
		if err = rows.Scan(&p.ID, &p.Title, &p.Author, &linesJSON, &p.Category, &p.CreatedAt); err != nil {
			return nil, errors.Wrap(err, "poem list: scan")
		}
		p.Lines = decodeLines(linesJSON)
		p.Assignments = []Assignment{}
		poems = append(poems, p)
	}
	if err = rows.Err(); err != nil {
		return nil, errors.Wrap(err, "poem list: iteration")
	}
	if len(poems) == 0 {
		return poems, nil
	}
	for i := range poems {
		byID[poems[i].ID] = &poems[i]
	}

	// The attribution projection rides ONE query for the whole page: the
	// assignment rows joined with the textbook's name/lessons/learned
	// progress so the scope verdict needs no second round trip per poem.
	books := map[int64]bookProgress{}
	assignments := map[int64][]Assignment{}
	if assignRows, aerr := s.db.QueryContext(ctx,
		`SELECT a.poem_id, a.textbook_id, a.lesson, a.include_in_scope,
		        t.name, t.lessons, t.learned_lessons
		 FROM poem_assignments a
		 LEFT JOIN textbooks t ON t.id = a.textbook_id
		 ORDER BY a.poem_id, a.textbook_id`); aerr != nil {
		// No textbooks table yet (a database older than Issue #4): no
		// attribution can exist either — every poem reads supplementary
		// until the table does. The honest degraded read, not a failed
		// page (the card repo's pre-migration discipline).
		if !strings.Contains(aerr.Error(), "no such table") {
			s.logger.Error("poem assignments query failed",
				infra.F("component", "poem/store"),
				infra.ErrField(aerr),
			)
			return nil, errors.Wrap(aerr, "poem list: assignments")
		}
	} else {
		defer assignRows.Close()
		for assignRows.Next() {
			var poemID, textbookID, include int64
			var lesson string
			var nameNull, lessonsNull sql.NullString
			var learnedNull sql.NullInt64
			if err = assignRows.Scan(&poemID, &textbookID, &lesson, &include, &nameNull, &lessonsNull, &learnedNull); err != nil {
				return nil, errors.Wrap(err, "poem list: assignments scan")
			}
			if _, wanted := byID[poemID]; !wanted {
				continue // an assignment for a poem outside this page
			}
			if _, seen := books[textbookID]; !seen {
				books[textbookID] = bookProgress{
					lessons: decodeLessons(lessonsNull.String),
					learned: int(learnedNull.Int64),
				}
			}
			assignments[poemID] = append(assignments[poemID], Assignment{
				TextbookID:     textbookID,
				TextbookName:   nameNull.String,
				Lesson:         lesson,
				IncludeInScope: include == 1,
			})
		}
		if err = assignRows.Err(); err != nil {
			return nil, errors.Wrap(err, "poem list: assignments iteration")
		}
	}
	for i := range poems {
		if rows, ok := assignments[poems[i].ID]; ok {
			poems[i].Assignments = rows
		}
		applyScope(&poems[i], books)
	}
	return poems, nil
}

// bookProgress is one textbook's explicit lesson order plus its learned
// count — the two columns the scope verdict reads.
type bookProgress struct {
	lessons []string
	learned int
}

// applyScope computes one poem's scope verdict from its assignments and
// the textbook progress map (the poem twin of textbookScopePredicate's
// three branches): supplementary when unassigned, included on an explicit
// include, learned when any attribution's lesson sits inside its book's
// learned prefix, else progress-pending. A book missing from the map (its
// row deleted Python-side) treats the attribution as lesson-less.
func applyScope(p *Poem, books map[int64]bookProgress) {
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
		// clamps to the whole list (textbookScopePredicate's same rule).
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

// decodeLines parses the lines JSON column with the legacy-default
// tolerance of lesson's decoders: empty raw → empty slice; corrupted
// non-empty JSON surfaces as an error the caller already logged.
func decodeLines(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return []string{}
	}
	var lines []string
	if err := json.Unmarshal([]byte(raw), &lines); err != nil {
		return []string{}
	}
	if lines == nil {
		lines = []string{}
	}
	return lines
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
