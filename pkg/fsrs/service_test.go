// service_test.go exercises FSRSService orchestration: config-default merging,
// pybridge delegation, and error passthrough. Test framework: stdlib `testing`
// only, table-driven + t.Run subtests, manual fakes (D-48, no testify/mock).
// Mirrors vocab_test.go / card_repo_test.go style.
package fsrs

import (
	"context"
	"errors"
	"testing"

	"github.com/xiaotian/home-tutor-agent/pkg/infra"
	"github.com/xiaotian/home-tutor-agent/pkg/pybridge"
)

// validCfg returns a minimal valid FSRSConfig for test construction.
func validCfg() infra.FSRSConfig {
	return infra.FSRSConfig{
		DefaultWordCount: 25,
		FetchLockHours:   24.0,
		DefaultUnit:      "",
		DefaultCategory:  "english",
	}
}

// fakeExecutor satisfies pybridge.Executor with preset return values and
// call-capture slices for assertion on forwarded opts.
type fakeExecutor struct {
	dueResult    pybridge.DueResult
	dueErr       error
	reviewResult pybridge.ReviewResult
	reviewErr    error
	pregenResult pybridge.PregenResult
	pregenErr    error

	dueCalls    []pybridge.DueOpts
	reviewCalls []pybridge.ReviewOpts
}

func (f *fakeExecutor) RunDue(_ context.Context, opts pybridge.DueOpts) (pybridge.DueResult, error) {
	f.dueCalls = append(f.dueCalls, opts)
	return f.dueResult, f.dueErr
}

func (f *fakeExecutor) RunReview(_ context.Context, opts pybridge.ReviewOpts) (pybridge.ReviewResult, error) {
	f.reviewCalls = append(f.reviewCalls, opts)
	return f.reviewResult, f.reviewErr
}

func (f *fakeExecutor) RunPregenerate(_ context.Context, _ pybridge.PregenOpts) (pybridge.PregenResult, error) {
	return f.pregenResult, f.pregenErr
}

func (f *fakeExecutor) RunUnlock(_ context.Context, _ pybridge.UnlockOpts) (pybridge.UnlockResult, error) {
	return pybridge.UnlockResult{}, nil
}

func (f *fakeExecutor) RunAdd(_ context.Context, _ pybridge.AddOpts) (pybridge.AddResult, error) {
	return pybridge.AddResult{}, nil
}

func (f *fakeExecutor) RunEdit(_ context.Context, _ pybridge.EditOpts) (pybridge.EditResult, error) {
	return pybridge.EditResult{}, nil
}

func (f *fakeExecutor) RunRenameUnit(_ context.Context, _ pybridge.RenameUnitOpts) (pybridge.RenameUnitResult, error) {
	return pybridge.RenameUnitResult{}, nil
}

func (f *fakeExecutor) RunDeleteWords(_ context.Context, _ pybridge.DeleteWordsOpts) (pybridge.DeleteWordsResult, error) {
	return pybridge.DeleteWordsResult{}, nil
}

// --- NewFSRSService validation ---

func TestNewFSRSServiceValidation(t *testing.T) {
	cases := []struct {
		name    string
		exec    pybridge.Executor
		cfg     infra.FSRSConfig
		wantErr error
	}{
		{"nil executor", nil, validCfg(), ErrFSRSInvalid},
		{"invalid defaultWordCount", &fakeExecutor{}, infra.FSRSConfig{DefaultWordCount: 0, FetchLockHours: 24.0, DefaultCategory: "english"}, ErrFSRSInvalid},
		{"invalid fetchLockHours", &fakeExecutor{}, infra.FSRSConfig{DefaultWordCount: 25, FetchLockHours: 0, DefaultCategory: "english"}, ErrFSRSInvalid},
		{"empty defaultCategory", &fakeExecutor{}, infra.FSRSConfig{DefaultWordCount: 25, FetchLockHours: 24.0, DefaultCategory: ""}, ErrFSRSInvalid},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := NewFSRSService(tc.exec, nil, tc.cfg)
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if !errors.Is(err, tc.wantErr) {
				t.Errorf("error = %v, want errors.Is %v", err, tc.wantErr)
			}
		})
	}
}

func TestNewFSRSServiceNilLogger(t *testing.T) {
	svc, err := NewFSRSService(&fakeExecutor{}, nil, validCfg())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if svc == nil {
		t.Fatal("expected non-nil service")
	}
}

// --- SelectDueCards ---

func TestSelectDueCards(t *testing.T) {
	cards := []pybridge.DueCard{{ID: 1, Word: "hello"}}
	fake := &fakeExecutor{dueResult: pybridge.DueResult{Cards: cards}}
	svc, err := NewFSRSService(fake, nil, validCfg())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	result, err := svc.SelectDueCards(context.Background(), SelectOpts{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result) != 1 {
		t.Errorf("got %d cards, want 1", len(result))
	}
	if result[0].Word != "hello" {
		t.Errorf("word = %q, want %q", result[0].Word, "hello")
	}
}

func TestSelectDueCardsConfigDefaults(t *testing.T) {
	cases := []struct {
		name      string
		cfg       infra.FSRSConfig
		opts      SelectOpts
		wantLimit int
		wantCat   string
		wantLock  float64
	}{
		{
			"config defaults when opts zero",
			infra.FSRSConfig{DefaultWordCount: 25, FetchLockHours: 24.0, DefaultCategory: "english"},
			SelectOpts{},
			25, "english", 24.0,
		},
		{
			"opts override config",
			infra.FSRSConfig{DefaultWordCount: 25, FetchLockHours: 24.0, DefaultCategory: "english"},
			SelectOpts{Limit: 10, Category: "chinese", FetchLockHours: 48.0},
			10, "chinese", 48.0,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fake := &fakeExecutor{dueResult: pybridge.DueResult{Cards: []pybridge.DueCard{{ID: 1}}}}
			svc, err := NewFSRSService(fake, nil, tc.cfg)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			_, err = svc.SelectDueCards(context.Background(), tc.opts)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(fake.dueCalls) != 1 {
				t.Fatalf("expected 1 RunDue call, got %d", len(fake.dueCalls))
			}
			call := fake.dueCalls[0]
			if call.Limit != tc.wantLimit {
				t.Errorf("Limit = %d, want %d", call.Limit, tc.wantLimit)
			}
			if call.Category != tc.wantCat {
				t.Errorf("Category = %q, want %q", call.Category, tc.wantCat)
			}
			if call.FetchLockHours != tc.wantLock {
				t.Errorf("FetchLockHours = %v, want %v", call.FetchLockHours, tc.wantLock)
			}
		})
	}
}

func TestSelectDueCardsUnitsDefault(t *testing.T) {
	fake := &fakeExecutor{dueResult: pybridge.DueResult{Cards: []pybridge.DueCard{{ID: 1}}}}
	cfg := infra.FSRSConfig{
		DefaultWordCount: 25,
		FetchLockHours:   24.0,
		DefaultUnit:      "unit3",
		DefaultCategory:  "english",
	}
	svc, err := NewFSRSService(fake, nil, cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// When opts.Units is empty and cfg.DefaultUnit is non-empty,
	// units should be [cfg.DefaultUnit].
	_, err = svc.SelectDueCards(context.Background(), SelectOpts{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(fake.dueCalls) != 1 {
		t.Fatalf("expected 1 RunDue call, got %d", len(fake.dueCalls))
	}
	units := fake.dueCalls[0].Units
	if len(units) != 1 || units[0] != "unit3" {
		t.Errorf("Units = %v, want [unit3]", units)
	}
}

func TestSelectDueCardsUnitsExplicit(t *testing.T) {
	fake := &fakeExecutor{dueResult: pybridge.DueResult{Cards: []pybridge.DueCard{{ID: 1}}}}
	cfg := infra.FSRSConfig{
		DefaultWordCount: 25,
		FetchLockHours:   24.0,
		DefaultUnit:      "unit3",
		DefaultCategory:  "english",
	}
	svc, err := NewFSRSService(fake, nil, cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// When opts.Units is explicitly set, it should override cfg.DefaultUnit.
	_, err = svc.SelectDueCards(context.Background(), SelectOpts{Units: []string{"unit5", "unit6"}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	units := fake.dueCalls[0].Units
	if len(units) != 2 || units[0] != "unit5" || units[1] != "unit6" {
		t.Errorf("Units = %v, want [unit5 unit6]", units)
	}
}

func TestSelectDueCardsFetchLock(t *testing.T) {
	fake := &fakeExecutor{dueResult: pybridge.DueResult{Cards: []pybridge.DueCard{{ID: 1}}}}
	svc, err := NewFSRSService(fake, nil, validCfg())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	_, err = svc.SelectDueCards(context.Background(), SelectOpts{FetchLockHours: 48.0})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if fake.dueCalls[0].FetchLockHours != 48.0 {
		t.Errorf("FetchLockHours = %v, want 48.0", fake.dueCalls[0].FetchLockHours)
	}
}

func TestSelectDueCardsLockedReturnsEmpty(t *testing.T) {
	// When no cards are due (all locked), result is empty slice, NOT an error.
	fake := &fakeExecutor{dueResult: pybridge.DueResult{Cards: []pybridge.DueCard{}}}
	svc, err := NewFSRSService(fake, nil, validCfg())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	result, err := svc.SelectDueCards(context.Background(), SelectOpts{})
	if err != nil {
		t.Fatalf("expected nil error for empty result, got: %v", err)
	}
	if len(result) != 0 {
		t.Errorf("expected empty slice, got %d cards", len(result))
	}
}

func TestSelectDueCardsErrorPassthrough(t *testing.T) {
	fake := &fakeExecutor{dueErr: pybridge.ErrPythonTimeout}
	svc, err := NewFSRSService(fake, nil, validCfg())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	_, err = svc.SelectDueCards(context.Background(), SelectOpts{})
	if !errors.Is(err, pybridge.ErrPythonTimeout) {
		t.Errorf("error = %v, want errors.Is ErrPythonTimeout", err)
	}
}

// --- UpdateRatings ---

func TestUpdateRatings(t *testing.T) {
	fake := &fakeExecutor{
		reviewResult: pybridge.ReviewResult{
			Updated: []pybridge.UpdatedCard{{Word: "hello", Rating: "good"}},
		},
	}
	svc, err := NewFSRSService(fake, nil, validCfg())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	ratings := map[string]string{"hello": "good", "world": "hard"}
	result, err := svc.UpdateRatings(context.Background(), ratings, "english")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Updated) != 1 {
		t.Errorf("expected 1 updated card, got %d", len(result.Updated))
	}
	if result.Updated[0].Word != "hello" {
		t.Errorf("word = %q, want %q", result.Updated[0].Word, "hello")
	}

	// Verify forwarded opts match inputs.
	if len(fake.reviewCalls) != 1 {
		t.Fatalf("expected 1 RunReview call, got %d", len(fake.reviewCalls))
	}
	call := fake.reviewCalls[0]
	if call.Category != "english" {
		t.Errorf("Category = %q, want %q", call.Category, "english")
	}
	if len(call.Results) != 2 || call.Results["hello"] != "good" || call.Results["world"] != "hard" {
		t.Errorf("Results = %v, want map[hello:good world:hard]", call.Results)
	}
}

func TestUpdateRatingsErrorPassthrough(t *testing.T) {
	fake := &fakeExecutor{reviewErr: pybridge.ErrPythonExit}
	svc, err := NewFSRSService(fake, nil, validCfg())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	_, err = svc.UpdateRatings(context.Background(), map[string]string{"hello": "good"}, "english")
	if !errors.Is(err, pybridge.ErrPythonExit) {
		t.Errorf("error = %v, want errors.Is ErrPythonExit", err)
	}
}
