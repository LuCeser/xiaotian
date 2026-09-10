// Package fsrs orchestrates FSRS spaced-repetition operations by delegating
// to the Python vocab_manager.py engine via pybridge.Executor (Phase 3 D-09).
// Go NEVER reimplements the FSRS algorithm — it only composes parameters,
// forwards calls, and passes results. All FSRS state mutations flow through
// RunReview; Go never writes to the cards table directly (ADR write-separation).
package fsrs

import (
	"context"
	"fmt"

	"github.com/pkg/errors"

	"github.com/xiaotian/home-tutor-agent/pkg/infra"
	"github.com/xiaotian/home-tutor-agent/pkg/pybridge"
)

// Sentinel errors for FSRSService. Callers MUST branch on these via
// errors.Is rather than string matching (D-05 rich error handling).
var (
	// ErrFSRSInvalid is returned by NewFSRSService when constructor
	// arguments fail validation (nil executor, invalid config).
	ErrFSRSInvalid = errors.New("fsrs service configuration invalid")
)

// FSRSService is the orchestration interface for FSRS spaced-repetition
// operations (FSRS-01 through FSRS-04). It wraps pybridge.Executor and
// merges caller-supplied SelectOpts with FSRSConfig defaults. Go NEVER
// reimplements the FSRS algorithm — it only composes parameters, forwards
// calls, and passes results back verbatim.
type FSRSService interface {
	// SelectDueCards retrieves due cards for a dictation session. The opts
	// fields are merged with FSRSConfig defaults: zero-valued opts fall back
	// to the configured defaults. When all cards are locked (fetch_lock),
	// returns an empty slice and nil error — NEVER an error (D-02-a).
	SelectDueCards(ctx context.Context, opts SelectOpts) ([]pybridge.DueCard, error)

	// UpdateRatings forwards the rating map and category to the Python
	// FSRS engine verbatim. Go does NOT validate or map ratings — any
	// mapping decision belongs to a future phase (Pitfall 4).
	UpdateRatings(ctx context.Context, ratings map[string]string, category string) (pybridge.ReviewResult, error)

	// UnlockCards clears FSRS fetch locks via pybridge.Executor.RunUnlock.
	// With opts.IDs set, exactly those cards unlock (Ticket 07 scoped
	// release — one session's exit must not drop another's locks).
	// Otherwise the category/unit sweep applies, with a zero-valued
	// Category falling back to cfg.DefaultCategory. Returns the number of
	// cards unlocked.
	UnlockCards(ctx context.Context, opts UnlockOpts) (int, error)
}

// SelectOpts carries the caller's per-request preferences for SelectDueCards.
// Zero-valued fields are replaced by FSRSConfig defaults (Phase 5 D-02-a).
type SelectOpts struct {
	// Limit caps the number of cards returned. <= 0 → cfg.DefaultWordCount.
	Limit int

	// Category filters by vocabulary category. Empty → cfg.DefaultCategory.
	Category string

	// Units restricts the result to the named units. nil/empty → cfg.DefaultUnit.
	Units []string

	// FetchLockHours overrides the default fetch-lock window. <= 0 → cfg.FetchLockHours.
	FetchLockHours float64
}

// UnlockOpts carries the caller's per-request preferences for UnlockCards.
// Zero-valued fields are replaced by FSRSConfig defaults.
type UnlockOpts struct {
	// Category filters by vocabulary category. Empty → cfg.DefaultCategory.
	Category string

	// Unit restricts the unlock to one unit. Empty = all units.
	Unit string

	// IDs requests a scoped per-card release (Ticket 07): exactly these card
	// IDs unlock, nothing else — one session's exit must not drop another
	// session's in-flight locks. Non-empty IDs take precedence over
	// Category/Unit.
	IDs []int64
}

// Compile-time interface satisfaction check (D-43).
var _ FSRSService = (*fsrsService)(nil)

// fsrsService is the concrete FSRSService implementation backed by
// pybridge.Executor. Pure orchestration — no FSRS algorithm logic.
type fsrsService struct {
	exec   pybridge.Executor
	logger infra.Logger
	cfg    infra.FSRSConfig
}

// NewFSRSService constructs an FSRSService from an Executor, Logger, and
// FSRSConfig. Returns ErrFSRSInvalid if exec is nil or cfg.Validate fails.
// If logger is nil, infra.NewNoOpLogger() is substituted (D-47).
func NewFSRSService(exec pybridge.Executor, logger infra.Logger, cfg infra.FSRSConfig) (FSRSService, error) {
	if exec == nil {
		return nil, errors.Wrap(ErrFSRSInvalid, "pybridge.Executor must not be nil")
	}
	if err := cfg.Validate(); err != nil {
		// Wrap the underlying error so the field-specific message and the
		// ErrConfigInvalid chain survive, while keeping ErrFSRSInvalid
		// reachable via the second %w (Go 1.20+ multi-%w). Callers may branch
		// on either sentinel (D-05 rich error handling).
		return nil, fmt.Errorf("fsrs config validation failed: %w (sentinel %w)",
			err, ErrFSRSInvalid)
	}
	if logger == nil {
		logger = infra.NewNoOpLogger()
	}
	return &fsrsService{exec: exec, logger: logger, cfg: cfg}, nil
}

// SelectDueCards retrieves due cards for a dictation session. It merges the
// caller's SelectOpts with FSRSConfig defaults, builds pybridge.DueOpts,
// and delegates to pybridge.Executor.RunDue. On success, returns the card
// slice (possibly empty when all cards are locked — D-02-a).
func (s *fsrsService) SelectDueCards(ctx context.Context, opts SelectOpts) ([]pybridge.DueCard, error) {
	// Merge: zero-value opts fall back to cfg defaults (Pitfall 4 guard).
	limit := opts.Limit
	if limit <= 0 {
		limit = s.cfg.DefaultWordCount
	}

	category := opts.Category
	if category == "" {
		category = s.cfg.DefaultCategory
	}

	units := opts.Units
	if len(units) == 0 && s.cfg.DefaultUnit != "" {
		units = []string{s.cfg.DefaultUnit}
	}

	fetchLockHours := opts.FetchLockHours
	if fetchLockHours <= 0 {
		fetchLockHours = s.cfg.FetchLockHours
	}

	result, err := s.exec.RunDue(ctx, pybridge.DueOpts{
		Category:       category,
		Limit:          limit,
		Units:          units,
		FetchLockHours: fetchLockHours,
	})
	if err != nil {
		s.logger.Error("fsrs service: select due cards failed",
			infra.F("component", "fsrs"),
			infra.ErrField(err),
		)
		return nil, errors.Wrap(err, "fsrs service: select due cards")
	}
	return result.Cards, nil
}

// UpdateRatings forwards the rating map and category to the Python FSRS
// engine via pybridge.Executor.RunReview. Ratings are forwarded verbatim —
// Go does NOT validate or rewrite them (D-03-a).
func (s *fsrsService) UpdateRatings(ctx context.Context, ratings map[string]string, category string) (pybridge.ReviewResult, error) {
	result, err := s.exec.RunReview(ctx, pybridge.ReviewOpts{
		Results:  ratings,
		Category: category,
	})
	if err != nil {
		s.logger.Error("fsrs service: update ratings failed",
			infra.F("component", "fsrs"),
			infra.ErrField(err),
		)
		return pybridge.ReviewResult{}, errors.Wrap(err, "fsrs service: update ratings")
	}
	return result, nil
}

// UnlockCards clears FSRS fetch locks for the given category (and
// optionally a single unit). It merges the caller's UnlockOpts with
// FSRSConfig defaults, builds pybridge.UnlockOpts, and delegates to
// pybridge.Executor.RunUnlock. On success, returns the number of cards
// unlocked.
func (s *fsrsService) UnlockCards(ctx context.Context, opts UnlockOpts) (int, error) {
	// Scoped per-card release (Ticket 07): IDs win, category defaults don't
	// apply — the caller names exactly what it locked.
	if len(opts.IDs) > 0 {
		result, err := s.exec.RunUnlock(ctx, pybridge.UnlockOpts{IDs: opts.IDs})
		if err != nil {
			s.logger.Error("fsrs service: unlock cards failed",
				infra.F("component", "fsrs"),
				infra.F("ids", len(opts.IDs)),
				infra.ErrField(err),
			)
			return 0, errors.Wrap(err, "fsrs service: unlock cards")
		}
		return result.Unlocked, nil
	}

	category := opts.Category
	if category == "" {
		category = s.cfg.DefaultCategory
	}

	result, err := s.exec.RunUnlock(ctx, pybridge.UnlockOpts{
		Category: category,
		Unit:     opts.Unit,
	})
	if err != nil {
		s.logger.Error("fsrs service: unlock cards failed",
			infra.F("component", "fsrs"),
			infra.ErrField(err),
		)
		return 0, errors.Wrap(err, "fsrs service: unlock cards")
	}
	return result.Unlocked, nil
}
