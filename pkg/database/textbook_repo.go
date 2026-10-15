package database

// textbook_repo.go — the Go READ side of the Issue #4 textbook tables
// (Python's vocab_manager.py owns the writes via textbook-create/update,
// assign-words/unassign-words). The split mirrors CardRepository: reads
// stay pure Go against SQLite (zero Python subprocess spawns, D-12), and
// the repository degrades honestly on pre-migration databases — a database
// opened before any Python textbook command ran simply has no textbooks
// and no assignments, so every card reads as supplementary vocabulary.
//
// The scope predicate that guards automatic selection (cmd_due's Go-side
// twin for the practice path) also lives here: card_repo.List consults it
// through CardFilter.ScopeEligibleOnly so both eligibility switches
// (participates × textbook scope) compose with AND, exactly like the
// Python choke points.

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/pkg/errors"

	"github.com/xiaotian/home-tutor-agent/pkg/infra"
)

// ErrTextbookNotFound is returned by Get when no textbook row carries the
// given id. Detectable via errors.Is (D-05).
var ErrTextbookNotFound = errors.New("textbook not found")

// Textbook mirrors the Python-owned textbooks columns. Lessons is the
// parsed EXPLICIT teaching order — the repository never lets callers see
// the raw JSON string (Issue #4: 课次顺序是显式的，字典顺序不算数).
type Textbook struct {
	Volume         string   `json:"volume"`
	Name           string   `json:"name"`
	Category       string   `json:"category"`
	Edition        string   `json:"edition"`
	Stage          string   `json:"stage"`
	Grade          string   `json:"grade"`
	Module         string   `json:"module"`
	CreatedAt      string   `json:"createdAt"`
	Lessons        []string `json:"lessons"`
	ID             int64    `json:"id"`
	LearnedLessons int      `json:"learnedLessons"`
	// AssignmentCount is filled by List — how many cards are assigned to
	// this textbook (the parent-facing 教材词数). Not a column.
	AssignmentCount int  `json:"assignmentCount"`
	IsPrimary       bool `json:"isPrimary"`
}

// TextbookAssignment is the per-word projection GET /words attaches to
// each row (the browse chips' data source): the textbook's name rides
// along so the UI never needs a second join.
type TextbookAssignment struct {
	TextbookName   string `json:"name"`
	Lesson         string `json:"lesson"`
	TextbookID     int64  `json:"id"`
	IncludeInScope bool   `json:"includeInScope"`
}

// TextbookRepository provides read-only access to the Python-owned
// textbooks/card_assignments tables (write-separation contract: Go never
// writes these; mutations route through pybridge).
type TextbookRepository interface {
	// List returns textbooks ordered by (created_at, id) — creation order,
	// stable for rendering. A non-empty category scopes the list. On a
	// pre-migration database (tables absent) it returns an empty slice,
	// never an error.
	List(ctx context.Context, category string) ([]Textbook, error)

	// Get returns one textbook by id (ErrTextbookNotFound when absent or
	// when the table does not exist yet).
	Get(ctx context.Context, id int64) (Textbook, error)

	// AssignmentsByCardIDs returns the attribution rows for the given
	// cards, keyed by card id (absent key = no assignment). Textbook names
	// are resolved in the same query so callers render directly.
	AssignmentsByCardIDs(ctx context.Context, cardIDs []int64) (map[int64][]TextbookAssignment, error)

	// AllAssignments is the unfiltered variant — the projection GET /words
	// attaches on an unscoped browse (building a whole-library IN-list for
	// it would only re-parse N placeholders).
	AllAssignments(ctx context.Context) (map[int64][]TextbookAssignment, error)
}

// Compile-time interface satisfaction check (D-43).
var _ TextbookRepository = (*sqliteTextbookRepository)(nil)

type sqliteTextbookRepository struct {
	db     *sql.DB
	logger infra.Logger
}

// NewTextbookRepository constructs a TextbookRepository from an open
// *sql.DB (nil-guarded, nil-logger-substituted — NewCardRepository
// contract).
func NewTextbookRepository(db *sql.DB, logger infra.Logger) (TextbookRepository, error) {
	if db == nil {
		return nil, errors.Wrap(ErrTextbookNotFound, "database/textbook_repo: *sql.DB must not be nil")
	}
	if logger == nil {
		logger = infra.NewNoOpLogger()
	}
	return &sqliteTextbookRepository{db: db, logger: logger}, nil
}

// textbookTablesExist probes sqlite_master for the Issue #4 tables — the
// same per-read degradation CardRepository.cardColumns applies to the
// participates column.
func textbookTablesExist(ctx context.Context, db *sql.DB) (bool, error) {
	var one int
	err := db.QueryRowContext(ctx,
		"SELECT 1 FROM sqlite_master WHERE type = 'table' AND name IN ('textbooks', 'card_assignments')",
	).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

// parseTextbookLessons decodes the lessons JSON column defensively: a
// NULL/invalid payload yields an empty list, never a read failure (the
// column is Python-owned; a defensive parse keeps the read path alive).
func parseTextbookLessons(raw string) []string {
	if raw == "" {
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

// List implements TextbookRepository.List.
func (r *sqliteTextbookRepository) List(ctx context.Context, category string) ([]Textbook, error) {
	exists, err := textbookTablesExist(ctx, r.db)
	if err != nil {
		return nil, errors.Wrap(err, "textbook tables probe")
	}
	if !exists {
		return []Textbook{}, nil
	}
	query := `
		SELECT t.id, t.name, t.category, t.edition, t.stage, t.grade, t.volume, t.module,
		       t.lessons, t.learned_lessons, t.is_primary, t.created_at,
		       (SELECT COUNT(*) FROM card_assignments a WHERE a.textbook_id = t.id) AS assignment_count
		FROM textbooks t`
	args := []any{}
	if category != "" {
		query += " WHERE t.category = ?"
		args = append(args, category)
	}
	query += " ORDER BY t.created_at, t.id"

	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		r.logger.Error("textbook list failed",
			infra.F("component", "database/textbook_repo"),
			infra.ErrField(err),
		)
		return nil, errors.Wrap(err, "textbook list")
	}
	defer rows.Close()

	books := []Textbook{}
	for rows.Next() {
		book, err := scanTextbook(rows)
		if err != nil {
			r.logger.Error("textbook scan failed",
				infra.F("component", "database/textbook_repo"),
				infra.ErrField(err),
			)
			return nil, errors.Wrap(err, "textbook list: scan")
		}
		books = append(books, book)
	}
	if err := rows.Err(); err != nil {
		return nil, errors.Wrap(err, "textbook list: iteration")
	}
	return books, nil
}

// Get implements TextbookRepository.Get.
func (r *sqliteTextbookRepository) Get(ctx context.Context, id int64) (Textbook, error) {
	exists, err := textbookTablesExist(ctx, r.db)
	if err != nil {
		return Textbook{}, errors.Wrap(err, "textbook tables probe")
	}
	if !exists {
		return Textbook{}, errors.Wrap(ErrTextbookNotFound, fmt.Sprintf("get textbook by id %d", id))
	}
	row := r.db.QueryRowContext(ctx, `
		SELECT t.id, t.name, t.category, t.edition, t.stage, t.grade, t.volume, t.module,
		       t.lessons, t.learned_lessons, t.is_primary, t.created_at,
		       (SELECT COUNT(*) FROM card_assignments a WHERE a.textbook_id = t.id) AS assignment_count
		FROM textbooks t WHERE t.id = ?`, id)
	book, err := scanTextbook(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Textbook{}, errors.Wrap(ErrTextbookNotFound, fmt.Sprintf("get textbook by id %d", id))
		}
		return Textbook{}, errors.Wrap(err, "get textbook")
	}
	return book, nil
}

// scanTextbook scans one textbooks row (shared by List and Get).
func scanTextbook(row interface{ Scan(...any) error }) (Textbook, error) {
	var t Textbook
	var lessonsJSON string
	var primary int
	if err := row.Scan(
		&t.ID, &t.Name, &t.Category, &t.Edition, &t.Stage, &t.Grade,
		&t.Volume, &t.Module, &lessonsJSON, &t.LearnedLessons, &primary,
		&t.CreatedAt, &t.AssignmentCount,
	); err != nil {
		return Textbook{}, err
	}
	t.IsPrimary = primary == 1
	t.Lessons = parseTextbookLessons(lessonsJSON)
	return t, nil
}

// assignmentsProjection is the shared attribution SELECT: the assignment
// row plus the textbook's name (so callers render directly, no second
// join client-side).
const assignmentsProjection = `
	SELECT a.card_id, a.textbook_id, a.lesson, a.include_in_scope, t.name
	FROM card_assignments a JOIN textbooks t ON t.id = a.textbook_id`

// scanAssignments runs one attribution query and folds the rows into the
// per-card map. The map only carries keys for cards that HAVE assignments
// — absence means supplementary (the scope predicate's first branch).
func (r *sqliteTextbookRepository) scanAssignments(ctx context.Context, query string, args ...any) (map[int64][]TextbookAssignment, error) {
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		r.logger.Error("assignments query failed",
			infra.F("component", "database/textbook_repo"),
			infra.ErrField(err),
		)
		return nil, errors.Wrap(err, "assignments query")
	}
	defer rows.Close()
	out := map[int64][]TextbookAssignment{}
	for rows.Next() {
		var cardID, textbookID, include int64
		var lesson, name string
		if err := rows.Scan(&cardID, &textbookID, &lesson, &include, &name); err != nil {
			return nil, errors.Wrap(err, "assignments query: scan")
		}
		out[cardID] = append(out[cardID], TextbookAssignment{
			TextbookID: textbookID, TextbookName: name,
			Lesson: lesson, IncludeInScope: include == 1,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, errors.Wrap(err, "assignments query: iteration")
	}
	return out, nil
}

// AssignmentsByCardIDs implements TextbookRepository.AssignmentsByCardIDs.
func (r *sqliteTextbookRepository) AssignmentsByCardIDs(ctx context.Context, cardIDs []int64) (map[int64][]TextbookAssignment, error) {
	if len(cardIDs) == 0 {
		return map[int64][]TextbookAssignment{}, nil
	}
	exists, err := textbookTablesExist(ctx, r.db)
	if err != nil {
		return nil, errors.Wrap(err, "textbook tables probe")
	}
	if !exists {
		return map[int64][]TextbookAssignment{}, nil
	}
	placeholders := strings.Repeat("?,", len(cardIDs))
	placeholders = placeholders[:len(placeholders)-1]
	args := make([]any, 0, len(cardIDs))
	for _, id := range cardIDs {
		args = append(args, id)
	}
	return r.scanAssignments(ctx, assignmentsProjection+
		" WHERE a.card_id IN ("+placeholders+") ORDER BY a.card_id, a.textbook_id", args...)
}

// AllAssignments implements TextbookRepository.AllAssignments — the same
// projection without the IN filter.
func (r *sqliteTextbookRepository) AllAssignments(ctx context.Context) (map[int64][]TextbookAssignment, error) {
	exists, err := textbookTablesExist(ctx, r.db)
	if err != nil {
		return nil, errors.Wrap(err, "textbook tables probe")
	}
	if !exists {
		return map[int64][]TextbookAssignment{}, nil
	}
	return r.scanAssignments(ctx, assignmentsProjection+" ORDER BY a.card_id, a.textbook_id")
}

// textbookScopePredicate composes the Issue #4 eligibility SQL fragment
// for card_repo.List (the Go twin of vocab_manager.py scope_predicate —
// same three branches, same computed allowed-lesson sets, no JSON1
// dependency). It returns a parenthesized expression (no WHERE/AND
// leader) plus its parameters, referencing the cards alias "c" — callers
// splice it into their predicate list. An EMPTY fragment means "no scope
// state" (tables absent — every card reads as supplementary; callers
// skip appending). When no textbook has learned lessons the learned
// branch collapses to the constant 0 (nothing counts as learned).
func textbookScopePredicate(ctx context.Context, db *sql.DB) (string, []any, error) {
	exists, err := textbookTablesExist(ctx, db)
	if err != nil {
		return "", nil, err
	}
	if !exists {
		return "", nil, nil
	}
	rows, err := db.QueryContext(ctx,
		"SELECT id, lessons, learned_lessons FROM textbooks WHERE learned_lessons > 0")
	if err != nil {
		return "", nil, err
	}
	defer rows.Close()

	type allowed struct {
		lessons    []string
		textbookID int64
	}
	var books []allowed
	for rows.Next() {
		var id, learned int64
		var lessonsJSON string
		if err := rows.Scan(&id, &lessonsJSON, &learned); err != nil {
			return "", nil, err
		}
		all := parseTextbookLessons(lessonsJSON)
		if int(learned) > len(all) {
			learned = int64(len(all))
		}
		if learned == 0 {
			continue
		}
		books = append(books, allowed{textbookID: id, lessons: all[:learned]})
	}
	if err := rows.Err(); err != nil {
		return "", nil, err
	}

	var branches []string
	var args []any
	for _, b := range books {
		branch := "(la.textbook_id = ? AND la.lesson IN ("
		branch += strings.Repeat("?,", len(b.lessons))
		branch = branch[:len(branch)-1] + "))"
		branches = append(branches, branch)
		args = append(args, b.textbookID)
		for _, lesson := range b.lessons {
			args = append(args, lesson)
		}
	}
	learnedSQL := "0"
	if len(branches) > 0 {
		learnedSQL = strings.Join(branches, " OR ")
	}
	return "(\n" +
		"  NOT EXISTS (SELECT 1 FROM card_assignments na WHERE na.card_id = c.id)\n" +
		"  OR EXISTS (SELECT 1 FROM card_assignments ia WHERE ia.card_id = c.id AND ia.include_in_scope = 1)\n" +
		"  OR EXISTS (SELECT 1 FROM card_assignments la WHERE la.card_id = c.id AND (" + learnedSQL + "))\n" +
		" )", args, nil
}
