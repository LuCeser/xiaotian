// handlers_test.go exercises the pkg/web/api handlers with manual fakes
// (D-48, no testify — card_repo_test.go convention, infra.NewNoOpLogger).
//
// Coverage (Plan 09-01 Task 2 behavior block):
//   - TestAddWords: valid body passthrough, missing/unknown category,
//     entry-count cap, unknown JSON field rejection (DisallowUnknownFields),
//     and the python_failed sentinel mapping.
//   - TestListWords: filter decode (category/unit/q), unknown category
//     query rejection, and the {"words":[...]} snake_case response shape.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/xiaotian/home-tutor-agent/pkg/database"
	"github.com/xiaotian/home-tutor-agent/pkg/infra"
	"github.com/xiaotian/home-tutor-agent/pkg/pybridge"
)

// fakeExecutor is a manual fake for pybridge.Executor (D-48).
type fakeExecutor struct {
	addResult pybridge.AddResult
	addErr    error
	addCalls  []pybridge.AddOpts
}

func (f *fakeExecutor) RunAdd(_ context.Context, opts pybridge.AddOpts) (pybridge.AddResult, error) {
	f.addCalls = append(f.addCalls, opts)
	return f.addResult, f.addErr
}

func (f *fakeExecutor) RunDue(_ context.Context, _ pybridge.DueOpts) (pybridge.DueResult, error) {
	return pybridge.DueResult{}, nil
}

func (f *fakeExecutor) RunReview(_ context.Context, _ pybridge.ReviewOpts) (pybridge.ReviewResult, error) {
	return pybridge.ReviewResult{}, nil
}

func (f *fakeExecutor) RunPregenerate(_ context.Context, _ pybridge.PregenOpts) (pybridge.PregenResult, error) {
	return pybridge.PregenResult{}, nil
}

func (f *fakeExecutor) RunUnlock(_ context.Context, _ pybridge.UnlockOpts) (pybridge.UnlockResult, error) {
	return pybridge.UnlockResult{}, nil
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

// fakeCardRepo is a manual fake for database.CardRepository that captures
// the received filter and returns a canned slice.
type fakeCardRepo struct {
	cards     []database.Card
	listErr   error
	filters   []database.CardFilter
	getErr    error
	byUnit    []database.Card
	unitsSeen []string
}

func (f *fakeCardRepo) GetByID(_ context.Context, _ int64) (database.Card, error) {
	return database.Card{}, f.getErr
}

func (f *fakeCardRepo) ListByUnit(_ context.Context, unit string) ([]database.Card, error) {
	f.unitsSeen = append(f.unitsSeen, unit)
	return f.byUnit, nil
}

func (f *fakeCardRepo) List(_ context.Context, filter database.CardFilter) ([]database.Card, error) {
	f.filters = append(f.filters, filter)
	return f.cards, f.listErr
}

func newTestHandler(exec pybridge.Executor, cards database.CardRepository) http.Handler {
	return NewHandler(exec, cards, infra.NewNoOpLogger())
}

func TestAddWords(t *testing.T) {
	t.Run("valid body returns 200 with added/skipped passthrough", func(t *testing.T) {
		exec := &fakeExecutor{addResult: pybridge.AddResult{Added: 3, Skipped: 2}}
		h := newTestHandler(exec, &fakeCardRepo{})

		body := `{"category":"chinese","unit":"L1","entries":[{"word":"你好","meaning":"hello","example":"你好世界"}]}`
		req := httptest.NewRequest(http.MethodPost, "/words", strings.NewReader(body))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
		}
		var resp struct {
			Added   int `json:"added"`
			Skipped int `json:"skipped"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("decode response %q: %v", rec.Body.String(), err)
		}
		if resp.Added != 3 || resp.Skipped != 2 {
			t.Errorf("response = {added:%d,skipped:%d}, want {3,2} (fake passthrough)",
				resp.Added, resp.Skipped)
		}
		if len(exec.addCalls) != 1 {
			t.Fatalf("RunAdd calls = %d, want 1", len(exec.addCalls))
		}
		got := exec.addCalls[0]
		if got.Category != "chinese" || got.Unit != "L1" || len(got.Entries) != 1 ||
			got.Entries[0].Word != "你好" || got.Entries[0].Meaning != "hello" {
			t.Errorf("forwarded AddOpts = %+v, want the decoded request fields", got)
		}
	})

	t.Run("missing category returns 400 invalid_request", func(t *testing.T) {
		h := newTestHandler(&fakeExecutor{}, &fakeCardRepo{})
		req := httptest.NewRequest(http.MethodPost, "/words",
			strings.NewReader(`{"entries":[{"word":"x"}]}`))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)

		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400", rec.Code)
		}
		if !strings.Contains(rec.Body.String(), `"code":"invalid_request"`) {
			t.Errorf("body = %s, want invalid_request error envelope", rec.Body.String())
		}
	})

	t.Run("unknown category returns 400", func(t *testing.T) {
		h := newTestHandler(&fakeExecutor{}, &fakeCardRepo{})
		req := httptest.NewRequest(http.MethodPost, "/words",
			strings.NewReader(`{"category":"francais","entries":[{"word":"x"}]}`))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)

		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400 for category %q", rec.Code, "francais")
		}
		if !strings.Contains(rec.Body.String(), `"code":"invalid_request"`) {
			t.Errorf("body = %s, want invalid_request error envelope", rec.Body.String())
		}
	})

	t.Run("101 entries returns 400", func(t *testing.T) {
		var sb strings.Builder
		sb.WriteString(`{"category":"english","entries":[`)
		for i := 0; i < 101; i++ {
			if i > 0 {
				sb.WriteString(",")
			}
			sb.WriteString(`{"word":"w` + strings.Repeat("a", 1) + `"}`)
		}
		sb.WriteString(`]}`)
		h := newTestHandler(&fakeExecutor{}, &fakeCardRepo{})
		req := httptest.NewRequest(http.MethodPost, "/words", strings.NewReader(sb.String()))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)

		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400 for 101 entries", rec.Code)
		}
		if !strings.Contains(rec.Body.String(), "entries") {
			t.Errorf("body = %s, want the offending field (entries) named", rec.Body.String())
		}
	})

	t.Run("unknown JSON field returns 400", func(t *testing.T) {
		// RESEARCH Pitfall 12: DisallowUnknownFields must reject a typo like
		// "catagory" rather than silently ignoring it.
		h := newTestHandler(&fakeExecutor{}, &fakeCardRepo{})
		req := httptest.NewRequest(http.MethodPost, "/words",
			strings.NewReader(`{"catagory":"english","entries":[{"word":"x"}]}`))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)

		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400 for unknown JSON field", rec.Code)
		}
		if !strings.Contains(rec.Body.String(), `"code":"invalid_request"`) {
			t.Errorf("body = %s, want invalid_request error envelope", rec.Body.String())
		}
	})

	t.Run("executor ExitError maps to 502 python_failed", func(t *testing.T) {
		exec := &fakeExecutor{
			addErr: &pybridge.ExitError{
				Cmd: "add", ExitCode: 1, Stderr: "boom", Err: pybridge.ErrPythonExit,
			},
		}
		h := newTestHandler(exec, &fakeCardRepo{})
		req := httptest.NewRequest(http.MethodPost, "/words",
			strings.NewReader(`{"category":"english","entries":[{"word":"x"}]}`))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)

		if rec.Code != http.StatusBadGateway {
			t.Fatalf("status = %d, want 502 for ExitError wrapping ErrPythonExit", rec.Code)
		}
		if !strings.Contains(rec.Body.String(), `"code":"python_failed"`) {
			t.Errorf("body = %s, want python_failed error envelope", rec.Body.String())
		}
	})

	t.Run("timeout sentinel maps to 502 python_failed", func(t *testing.T) {
		exec := &fakeExecutor{addErr: pybridge.ErrPythonTimeout}
		h := newTestHandler(exec, &fakeCardRepo{})
		req := httptest.NewRequest(http.MethodPost, "/words",
			strings.NewReader(`{"category":"english","entries":[{"word":"x"}]}`))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)

		if rec.Code != http.StatusBadGateway {
			t.Fatalf("status = %d, want 502 for ErrPythonTimeout", rec.Code)
		}
	})

	t.Run("unexpected error maps to 500 internal", func(t *testing.T) {
		exec := &fakeExecutor{addErr: errors.New("programming bug")}
		h := newTestHandler(exec, &fakeCardRepo{})
		req := httptest.NewRequest(http.MethodPost, "/words",
			strings.NewReader(`{"category":"english","entries":[{"word":"x"}]}`))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)

		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("status = %d, want 500 for non-sentinel error", rec.Code)
		}
		if !strings.Contains(rec.Body.String(), `"code":"internal"`) {
			t.Errorf("body = %s, want internal error envelope", rec.Body.String())
		}
	})
}

func TestListWords(t *testing.T) {
	t.Run("fake repo receives the decoded filter", func(t *testing.T) {
		repo := &fakeCardRepo{cards: []database.Card{
			{ID: 1, Word: "apple", Meaning: "苹果", Unit: "L1", Category: "english"},
		}}
		h := newTestHandler(&fakeExecutor{}, repo)

		req := httptest.NewRequest(http.MethodGet,
			"/words?category=english&unit=L1&q=app", nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
		}
		if len(repo.filters) != 1 {
			t.Fatalf("List calls = %d, want 1", len(repo.filters))
		}
		got := repo.filters[0]
		if got.Category != "english" || got.Unit != "L1" || got.Query != "app" {
			t.Errorf("forwarded filter = %+v, want {english L1 app}", got)
		}
	})

	t.Run("unknown category query value returns 400", func(t *testing.T) {
		h := newTestHandler(&fakeExecutor{}, &fakeCardRepo{})
		req := httptest.NewRequest(http.MethodGet, "/words?category=klingon", nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)

		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400 for unknown category query", rec.Code)
		}
		if !strings.Contains(rec.Body.String(), `"code":"invalid_request"`) {
			t.Errorf("body = %s, want invalid_request error envelope", rec.Body.String())
		}
	})

	t.Run("response body shape is words array with snake_case fields", func(t *testing.T) {
		repo := &fakeCardRepo{cards: []database.Card{
			{ID: 7, Word: "apple", Meaning: "苹果", Example: "I eat an apple.",
				Unit: "L1", Category: "english"},
		}}
		h := newTestHandler(&fakeExecutor{}, repo)
		req := httptest.NewRequest(http.MethodGet, "/words", nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)

		var resp struct {
			Words []struct {
				ID       int64  `json:"id"`
				Word     string `json:"word"`
				Meaning  string `json:"meaning"`
				Example  string `json:"example"`
				Unit     string `json:"unit"`
				Category string `json:"category"`
			} `json:"words"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("decode response %q: %v", rec.Body.String(), err)
		}
		if len(resp.Words) != 1 {
			t.Fatalf("words length = %d, want 1", len(resp.Words))
		}
		w := resp.Words[0]
		if w.ID != 7 || w.Word != "apple" || w.Meaning != "苹果" ||
			w.Example != "I eat an apple." || w.Unit != "L1" || w.Category != "english" {
			t.Errorf("word entry = %+v, want the canned card fields", w)
		}
	})

	t.Run("repo error maps to 500 internal", func(t *testing.T) {
		repo := &fakeCardRepo{listErr: errors.New("db down")}
		h := newTestHandler(&fakeExecutor{}, repo)
		req := httptest.NewRequest(http.MethodGet, "/words", nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)

		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("status = %d, want 500 for repo failure", rec.Code)
		}
		if !strings.Contains(rec.Body.String(), `"code":"internal"`) {
			t.Errorf("body = %s, want internal error envelope", rec.Body.String())
		}
	})
}
