package database

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/pkg/errors"
	"github.com/xiaotian/home-tutor-agent/pkg/infra"
)

// Sentinel errors for CardRepository. Callers MUST branch on these via
// errors.Is rather than string matching (D-05 rich error handling).
var (
	// ErrCardNotFound is returned by GetByID when no card exists with
	// the given id. Detectable via errors.Is, mirroring ErrSessionNotFound.
	ErrCardNotFound = errors.New("card not found")
)

// Card mirrors the Python-owned cards columns from vocab_manager.py
// (lines 103-128). The FsrsCard field is an opaque JSON string — Go NEVER
// parses it into typed FSRS fields (RESEARCH Pitfall 5). If Go needs
// stability/difficulty/due, it calls pybridge.Executor.RunDue instead.
//
// Write-separation contract (D-03-a): Python owns writes to the cards table
// via pybridge.Executor.RunReview. Go only reads.
type Card struct {
	ID               int64
	Word             string
	Meaning          string
	Example          string
	Unit             string
	Category         string
	FsrsCard         string // opaque JSON — never parsed in Go (Pitfall 5)
	CreatedAt        string
	FetchLockedUntil string // nullable; empty string when NULL
	Participates     bool   // eligible for automatic dictation/practice selection
}

// CardRepository provides read-only access to the Python-owned cards table.
// Go NEVER writes to this table — FSRS state updates route through
// pybridge.Executor.RunReview (D-01/D-03-a/D-09).
//
// The interface exposes read methods only. There is deliberately no
// write method (INSERT/UPDATE/DELETE) — this is enforced at the type level.
// All queries use an explicit column list, never SELECT *, so a Python-side
// column change fails loudly rather than silently returning wrong data
// (RESEARCH Pitfall 4).
type CardRepository interface {
	// GetByID returns the card with the given database ID.
	// Returns ErrCardNotFound when no card exists.
	GetByID(ctx context.Context, id int64) (Card, error)

	// ListByUnit returns cards in the named unit. An empty unit string
	// returns all cards.
	ListByUnit(ctx context.Context, unit string) ([]Card, error)

	// List returns cards matching the (optionally empty) CardFilter.
	// Empty filter fields are omitted from the WHERE clause; the result
	// is ordered by (unit, id) for stable rendering (Phase 9, D-12).
	List(ctx context.Context, filter CardFilter) ([]Card, error)

	// CountByUnit returns one UnitCount row per distinct unit, ordered by
	// unit name ascending (port of vocab_manager.py:605). A non-empty
	// category scopes the grouping to that category — equal-named units
	// across categories keep separate counts (RESEARCH Pitfall 7); an
	// empty category merges counts across ALL categories by unit name
	// (documented intentional merge, D-09 naming-convention semantics).
	CountByUnit(ctx context.Context, category string) ([]UnitCount, error)
}

// Compile-time interface satisfaction check (D-43, mirrors pybridge pattern).
var _ CardRepository = (*sqliteCardRepository)(nil)

// sqliteCardRepository is the concrete CardRepository implementation
// backed by *sql.DB (modernc.org/sqlite). Read-only by design (D-01/D-03-a).
type sqliteCardRepository struct {
	db     *sql.DB
	logger infra.Logger
}

// NewCardRepository constructs a CardRepository from an open *sql.DB.
// Returns an error if db is nil (D-47 nil-guard). If logger is nil,
// infra.NewNoOpLogger() is substituted so the repository is always safe to use.
func NewCardRepository(db *sql.DB, logger infra.Logger) (CardRepository, error) {
	if db == nil {
		return nil, errors.Wrap(ErrCardNotFound, "database/card_repo: *sql.DB must not be nil")
	}
	if logger == nil {
		logger = infra.NewNoOpLogger()
	}
	return &sqliteCardRepository{db: db, logger: logger}, nil
}

// cardColumns is the explicit column list for SELECT queries.
// Matches vocab_manager.py cards schema (lines 103-128) plus the
// fetch_locked_until migration (lines 126-128).
// NEVER use SELECT * — explicit columns fail loudly on schema drift
// (RESEARCH Pitfall 4).
const cardBaseColumns = "id, word, meaning, example, unit, category, fsrs_card, created_at, fetch_locked_until"

// cardColumns resolves the Python-owned schema on each read. Existing
// installations may be opened before any Python command has run the new
// migration; those rows participate by default. Rechecking lets the same
// service process observe the column as soon as Python adds it.
func (r *sqliteCardRepository) cardColumns(ctx context.Context) (string, bool, error) {
	rows, err := r.db.QueryContext(ctx, "PRAGMA table_info(cards)")
	if err != nil {
		return "", false, err
	}
	defer rows.Close()
	hasParticipation := false
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, columnType string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			return "", false, err
		}
		if name == "participates" {
			hasParticipation = true
		}
	}
	if err := rows.Err(); err != nil {
		return "", false, err
	}
	if hasParticipation {
		return cardBaseColumns + ", participates", true, nil
	}
	return cardBaseColumns + ", 1 AS participates", false, nil
}

// CardFilter narrows a CardRepository.List query. Every field is optional —
// an empty field is omitted from the WHERE clause (empty filter = all
// cards). Query is a substring match against word OR meaning (LIKE with
// user-supplied wildcards escaped, RESEARCH Pitfall 6).
type CardFilter struct {
	// ParticipatingOnly restricts automatic classroom selection to cards
	// enabled by the parent. Management views leave this false.
	ParticipatingOnly bool
	// ScopeEligibleOnly restricts automatic classroom selection to cards
	// inside the Issue #4 textbook scope: unassigned (supplementary)
	// cards pass; assigned cards need an include_in_scope override or a
	// lesson inside their textbook's learned progress. Composes with
	// ParticipatingOnly via AND (both switches must admit the word).
	// Management views leave this false — the browse list must show
	// out-of-scope words so the parent can manage them.
	ScopeEligibleOnly bool
	// Category restricts to one vocabulary category ("english"/"chinese").
	// Empty means all categories.
	Category string

	// Unit restricts to one unit (exact match). Empty means all units.
	Unit string

	// Query is a free-text substring matched against word OR meaning.
	// Empty means no text filter.
	Query string
}

// scanCard scans a single row into a Card struct. Shared by GetByID and
// ListByUnit to avoid duplication.
func scanCard(row interface{ Scan(...any) error }) (Card, error) {
	var c Card
	var fetchLocked sql.NullString

	err := row.Scan(
		&c.ID,
		&c.Word,
		&c.Meaning,
		&c.Example,
		&c.Unit,
		&c.Category,
		&c.FsrsCard,
		&c.CreatedAt,
		&fetchLocked,
		&c.Participates,
	)
	if err != nil {
		return Card{}, err
	}

	if fetchLocked.Valid {
		c.FetchLockedUntil = fetchLocked.String
	}

	return c, nil
}

// GetByID returns the card with the given database ID.
// Returns ErrCardNotFound when no card exists.
func (r *sqliteCardRepository) GetByID(ctx context.Context, id int64) (Card, error) {
	columns, _, err := r.cardColumns(ctx)
	if err != nil {
		return Card{}, errors.Wrap(err, "card schema")
	}
	c, err := scanCard(r.db.QueryRowContext(ctx,
		fmt.Sprintf("SELECT %s FROM cards WHERE id = ?", columns),
		id,
	))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Card{}, errors.Wrap(ErrCardNotFound, fmt.Sprintf("get card by id %d", id))
		}
		r.logger.Error("card get by id failed",
			infra.F("component", "database/card_repo"),
			infra.F("card_id", id),
			infra.ErrField(err),
		)
		return Card{}, errors.Wrap(err, "card get by id")
	}

	return c, nil
}

// ListByUnit returns cards in the named unit. An empty unit string
// returns all cards.
func (r *sqliteCardRepository) ListByUnit(ctx context.Context, unit string) ([]Card, error) {
	columns, _, err := r.cardColumns(ctx)
	if err != nil {
		return nil, errors.Wrap(err, "card schema")
	}
	query := fmt.Sprintf("SELECT %s FROM cards", columns)
	args := []any{}

	if unit != "" {
		query += " WHERE unit = ?"
		args = append(args, unit)
	}

	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		r.logger.Error("card list by unit failed",
			infra.F("component", "database/card_repo"),
			infra.F("unit", unit),
			infra.ErrField(err),
		)
		return nil, errors.Wrap(err, "card list by unit")
	}
	defer rows.Close()

	var cards []Card
	for rows.Next() {
		c, err := scanCard(rows)
		if err != nil {
			r.logger.Error("card scan row failed",
				infra.F("component", "database/card_repo"),
				infra.ErrField(err),
			)
			return nil, errors.Wrap(err, "card list by unit: scan")
		}
		cards = append(cards, c)
	}

	if err := rows.Err(); err != nil {
		r.logger.Error("card list by unit iteration",
			infra.F("component", "database/card_repo"),
			infra.ErrField(err),
		)
		return nil, errors.Wrap(err, "card list by unit: iteration")
	}

	return cards, nil
}

// UnitCount is one row of the unit grouping query (Phase 9, VOC-02/D-12):
// a unit name and the number of cards in it. Units stay flat strings
// grouped lexicographically (ORDER BY unit) — grouping is presentation-
// only, never a server-side hierarchy (VOC-02 prohibition). JSON tags
// serve the GET /api/v1/units response shape directly.
type UnitCount struct {
	Unit  string `json:"unit"`
	Count int    `json:"count"`
}

// escapeLike escapes the SQL LIKE wildcards (%, _) and the ESCAPE character
// itself (\) in a user-supplied query string so the pattern matches
// literally (RESEARCH Pitfall 6). Use with `LIKE ? ESCAPE '\'`.
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

// List returns cards matching the filter (Phase 9, D-12). WHERE parts are
// appended per NON-EMPTY filter field, args are always parameterized
// (never string-concatenated user input), and the result is ordered by
// (unit, id) for a stable browse-list rendering. Empty filter = all cards.
func (r *sqliteCardRepository) List(ctx context.Context, filter CardFilter) ([]Card, error) {
	columns, hasParticipation, err := r.cardColumns(ctx)
	if err != nil {
		return nil, errors.Wrap(err, "card schema")
	}
	// The "c" alias anchors the Issue #4 scope predicate's correlated
	// subqueries (card_assignments na/ia/la reference c.id); unqualified
	// filter columns keep resolving against the same single table.
	query := fmt.Sprintf("SELECT %s FROM cards c", columns)
	args := []any{}
	// First predicate needs WHERE, the rest AND — one flag tracks it across
	// every filter field (participation included).
	predicateOpen := false
	addPredicate := func(sql string, values ...any) {
		if predicateOpen {
			query += " AND"
		} else {
			query += " WHERE"
			predicateOpen = true
		}
		query += sql
		args = append(args, values...)
	}
	if filter.ParticipatingOnly && hasParticipation {
		addPredicate(" participates = 1")
	}
	if filter.ScopeEligibleOnly {
		scopeSQL, scopeArgs, err := textbookScopePredicate(ctx, r.db)
		if err != nil {
			r.logger.Error("textbook scope predicate failed",
				infra.F("component", "database/card_repo"),
				infra.ErrField(err),
			)
			return nil, errors.Wrap(err, "card list: scope")
		}
		if scopeSQL != "" {
			// Bare parenthesized expression — addPredicate handles the
			// WHERE/AND alternation; every parameter rides scopeArgs.
			addPredicate(" "+scopeSQL, scopeArgs...)
		}
		// An empty fragment = no scope tables yet: every card is
		// supplementary and eligible (pre-migration degraded read).
	}
	if filter.Category != "" {
		addPredicate(" category = ?", filter.Category)
	}
	if filter.Unit != "" {
		addPredicate(" unit = ?", filter.Unit)
	}
	if filter.Query != "" {
		pattern := "%" + escapeLike(filter.Query) + "%"
		addPredicate(" (word LIKE ? ESCAPE '\\' OR meaning LIKE ? ESCAPE '\\')", pattern, pattern)
	}
	query += " ORDER BY unit, id"

	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		r.logger.Error("card list failed",
			infra.F("component", "database/card_repo"),
			infra.F("category", filter.Category),
			infra.F("unit", filter.Unit),
			infra.ErrField(err),
		)
		return nil, errors.Wrap(err, "card list")
	}
	defer rows.Close()

	var cards []Card
	for rows.Next() {
		c, err := scanCard(rows)
		if err != nil {
			r.logger.Error("card scan row failed",
				infra.F("component", "database/card_repo"),
				infra.ErrField(err),
			)
			return nil, errors.Wrap(err, "card list: scan")
		}
		cards = append(cards, c)
	}

	if err := rows.Err(); err != nil {
		r.logger.Error("card list iteration",
			infra.F("component", "database/card_repo"),
			infra.ErrField(err),
		)
		return nil, errors.Wrap(err, "card list: iteration")
	}

	return cards, nil
}

// CountByUnit returns one UnitCount row per distinct unit, ordered by unit
// name ascending (Phase 9, VOC-02/D-12 — port of the vocab_manager.py:605
// shape: SELECT unit, COUNT(*) ... GROUP BY unit ORDER BY unit). The
// category filter is parameterized (never string-built, Shared Pattern 5)
// and appended only when non-empty; read-only, no Python subprocess (D-12).
func (r *sqliteCardRepository) CountByUnit(ctx context.Context, category string) ([]UnitCount, error) {
	query := "SELECT unit, COUNT(*) as count FROM cards"
	args := []any{}

	if category != "" {
		query += " WHERE category = ?"
		args = append(args, category)
	}
	query += " GROUP BY unit ORDER BY unit"

	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		r.logger.Error("card count by unit failed",
			infra.F("component", "database/card_repo"),
			infra.F("category", category),
			infra.ErrField(err),
		)
		return nil, errors.Wrap(err, "card count by unit")
	}
	defer rows.Close()

	units := []UnitCount{}
	for rows.Next() {
		var uc UnitCount
		if err := rows.Scan(&uc.Unit, &uc.Count); err != nil {
			r.logger.Error("card count by unit scan failed",
				infra.F("component", "database/card_repo"),
				infra.ErrField(err),
			)
			return nil, errors.Wrap(err, "card count by unit: scan")
		}
		units = append(units, uc)
	}

	if err := rows.Err(); err != nil {
		r.logger.Error("card count by unit iteration",
			infra.F("component", "database/card_repo"),
			infra.ErrField(err),
		)
		return nil, errors.Wrap(err, "card count by unit: iteration")
	}

	return units, nil
}
