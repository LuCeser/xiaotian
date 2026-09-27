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
const cardColumns = "id, word, meaning, example, unit, category, fsrs_card, created_at, fetch_locked_until"

// CardFilter narrows a CardRepository.List query. Every field is optional —
// an empty field is omitted from the WHERE clause (empty filter = all
// cards). Query is a substring match against word OR meaning (LIKE with
// user-supplied wildcards escaped, RESEARCH Pitfall 6).
type CardFilter struct {
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
	c, err := scanCard(r.db.QueryRowContext(ctx,
		fmt.Sprintf("SELECT %s FROM cards WHERE id = ?", cardColumns),
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
	query := fmt.Sprintf("SELECT %s FROM cards", cardColumns)
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
	query := fmt.Sprintf("SELECT %s FROM cards", cardColumns)
	args := []any{}

	if filter.Category != "" {
		query += " WHERE category = ?"
		args = append(args, filter.Category)
	}
	if filter.Unit != "" {
		if len(args) == 0 {
			query += " WHERE"
		} else {
			query += " AND"
		}
		query += " unit = ?"
		args = append(args, filter.Unit)
	}
	if filter.Query != "" {
		pattern := "%" + escapeLike(filter.Query) + "%"
		if len(args) == 0 {
			query += " WHERE"
		} else {
			query += " AND"
		}
		query += " (word LIKE ? ESCAPE '\\' OR meaning LIKE ? ESCAPE '\\')"
		args = append(args, pattern, pattern)
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
