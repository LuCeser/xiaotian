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
	"reflect"
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

	editResult pybridge.EditResult
	editErr    error
	editCalls  []pybridge.EditOpts

	renameResult pybridge.RenameUnitResult
	renameErr    error
	renameCalls  []pybridge.RenameUnitOpts

	deleteResult pybridge.DeleteWordsResult
	deleteErr    error
	deleteCalls  []pybridge.DeleteWordsOpts
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

func (f *fakeExecutor) RunEdit(_ context.Context, opts pybridge.EditOpts) (pybridge.EditResult, error) {
	f.editCalls = append(f.editCalls, opts)
	return f.editResult, f.editErr
}

func (f *fakeExecutor) RunRenameUnit(_ context.Context, opts pybridge.RenameUnitOpts) (pybridge.RenameUnitResult, error) {
	f.renameCalls = append(f.renameCalls, opts)
	return f.renameResult, f.renameErr
}

func (f *fakeExecutor) RunDeleteWords(_ context.Context, opts pybridge.DeleteWordsOpts) (pybridge.DeleteWordsResult, error) {
	f.deleteCalls = append(f.deleteCalls, opts)
	return f.deleteResult, f.deleteErr
}

// fakeCardRepo is a manual fake for database.CardRepository that captures
// the received filter and returns a canned slice. CountByUnit captures the
// requested category and returns the canned unitCounts (Plan 09-04).
type fakeCardRepo struct {
	cards      []database.Card
	listErr    error
	filters    []database.CardFilter
	getErr     error
	byUnit     []database.Card
	unitsSeen  []string
	unitCounts []database.UnitCount
	countErr   error
	countsSeen []string
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

func (f *fakeCardRepo) CountByUnit(_ context.Context, category string) ([]database.UnitCount, error) {
	f.countsSeen = append(f.countsSeen, category)
	return f.unitCounts, f.countErr
}

func newTestHandler(exec pybridge.Executor, cards database.CardRepository) http.Handler {
	return NewHandler(exec, cards, infra.PythonConfig{}, infra.NewNoOpLogger())
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

	t.Run("comma-containing word returns 400 invalid_request", func(t *testing.T) {
		// CR-01: the delete path joins words with "," and Python splits
		// back on ",", so a comma word must never enter the library.
		h := newTestHandler(&fakeExecutor{}, &fakeCardRepo{})
		req := httptest.NewRequest(http.MethodPost, "/words",
			strings.NewReader(`{"category":"english","entries":[{"word":"a,b"}]}`))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)

		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400 for comma word (body=%s)", rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), `"code":"invalid_request"`) {
			t.Errorf("body = %s, want invalid_request error envelope", rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), "must not contain commas") {
			t.Errorf("body = %s, want the comma rule named", rec.Body.String())
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

func TestListUnits(t *testing.T) {
	t.Run("no category forwards empty string and returns units envelope", func(t *testing.T) {
		repo := &fakeCardRepo{unitCounts: []database.UnitCount{
			{Unit: "unitA", Count: 3},
			{Unit: "unitB", Count: 2},
		}}
		h := newTestHandler(&fakeExecutor{}, repo)

		req := httptest.NewRequest(http.MethodGet, "/units", nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
		}
		if len(repo.countsSeen) != 1 || repo.countsSeen[0] != "" {
			t.Fatalf("CountByUnit received %v, want exactly one call with empty category", repo.countsSeen)
		}
		var resp struct {
			Units []struct {
				Unit  string `json:"unit"`
				Count int    `json:"count"`
			} `json:"units"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("decode response %q: %v", rec.Body.String(), err)
		}
		if len(resp.Units) != 2 {
			t.Fatalf("units length = %d, want 2", len(resp.Units))
		}
		if resp.Units[0].Unit != "unitA" || resp.Units[0].Count != 3 ||
			resp.Units[1].Unit != "unitB" || resp.Units[1].Count != 2 {
			t.Errorf("units = %+v, want the canned UnitCount rows", resp.Units)
		}
	})

	t.Run("category=chinese forwards the category to the repo", func(t *testing.T) {
		repo := &fakeCardRepo{unitCounts: []database.UnitCount{
			{Unit: "unitA", Count: 1},
		}}
		h := newTestHandler(&fakeExecutor{}, repo)

		req := httptest.NewRequest(http.MethodGet, "/units?category=chinese", nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
		}
		if len(repo.countsSeen) != 1 || repo.countsSeen[0] != "chinese" {
			t.Fatalf("CountByUnit received %v, want exactly one call with category chinese", repo.countsSeen)
		}
	})

	t.Run("unknown category returns 400 invalid_request", func(t *testing.T) {
		h := newTestHandler(&fakeExecutor{}, &fakeCardRepo{})
		req := httptest.NewRequest(http.MethodGet, "/units?category=francais", nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)

		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400 for category %q", rec.Code, "francais")
		}
		if !strings.Contains(rec.Body.String(), `"code":"invalid_request"`) {
			t.Errorf("body = %s, want invalid_request error envelope", rec.Body.String())
		}
	})

	t.Run("ordering is passed through from the canned sorted data", func(t *testing.T) {
		// Canned data is already ORDER BY unit ascending, mirroring the
		// real repository contract — the handler must not reorder or drop.
		repo := &fakeCardRepo{unitCounts: []database.UnitCount{
			{Unit: "", Count: 2},
			{Unit: "alpha", Count: 1},
			{Unit: "beta", Count: 5},
			{Unit: "zeta", Count: 3},
		}}
		h := newTestHandler(&fakeExecutor{}, repo)

		req := httptest.NewRequest(http.MethodGet, "/units?category=english", nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
		}
		var resp struct {
			Units []database.UnitCount `json:"units"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("decode response %q: %v", rec.Body.String(), err)
		}
		if !reflect.DeepEqual(resp.Units, repo.unitCounts) {
			t.Errorf("units = %+v, want verbatim passthrough of %+v", resp.Units, repo.unitCounts)
		}
	})
}

func TestEditWord(t *testing.T) {
	t.Run("full body maps pointer fields and echoes the edited card", func(t *testing.T) {
		exec := &fakeExecutor{}
		exec.editResult.Edited.ID = 42
		exec.editResult.Edited.Word = "apple"
		h := newTestHandler(exec, &fakeCardRepo{})

		body := `{"word":"apple","meaning":"苹果","example":"I eat an apple.","unit":"L1"}`
		req := httptest.NewRequest(http.MethodPut, "/words/42", strings.NewReader(body))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
		}
		var resp struct {
			Edited struct {
				ID   int64  `json:"id"`
				Word string `json:"word"`
			} `json:"edited"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("decode response %q: %v", rec.Body.String(), err)
		}
		if resp.Edited.ID != 42 || resp.Edited.Word != "apple" {
			t.Errorf("response edited = %+v, want {42 apple}", resp.Edited)
		}
		if len(exec.editCalls) != 1 {
			t.Fatalf("RunEdit calls = %d, want 1", len(exec.editCalls))
		}
		got := exec.editCalls[0]
		if got.ID != 42 {
			t.Errorf("EditOpts.ID = %d, want 42", got.ID)
		}
		if got.Word == nil || *got.Word != "apple" {
			t.Errorf("EditOpts.Word = %v, want &\"apple\"", got.Word)
		}
		if got.Meaning == nil || *got.Meaning != "苹果" {
			t.Errorf("EditOpts.Meaning = %v, want &\"苹果\"", got.Meaning)
		}
		if got.Example == nil || *got.Example != "I eat an apple." {
			t.Errorf("EditOpts.Example = %v, want the example pointer", got.Example)
		}
		if got.Unit == nil || *got.Unit != "L1" {
			t.Errorf("EditOpts.Unit = %v, want &\"L1\"", got.Unit)
		}
	})

	t.Run("absent optional fields stay nil pointers", func(t *testing.T) {
		exec := &fakeExecutor{}
		h := newTestHandler(exec, &fakeCardRepo{})

		req := httptest.NewRequest(http.MethodPut, "/words/7",
			strings.NewReader(`{"word":"pear"}`))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
		}
		if len(exec.editCalls) != 1 {
			t.Fatalf("RunEdit calls = %d, want 1", len(exec.editCalls))
		}
		got := exec.editCalls[0]
		if got.Word == nil || *got.Word != "pear" {
			t.Errorf("EditOpts.Word = %v, want &\"pear\"", got.Word)
		}
		if got.Meaning != nil || got.Example != nil || got.Unit != nil {
			t.Errorf("optional pointers = %v/%v/%v, want all nil (absent = keep column)",
				got.Meaning, got.Example, got.Unit)
		}
	})

	t.Run("missing word returns 400 invalid_request", func(t *testing.T) {
		h := newTestHandler(&fakeExecutor{}, &fakeCardRepo{})
		req := httptest.NewRequest(http.MethodPut, "/words/1",
			strings.NewReader(`{"meaning":"x"}`))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)

		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400", rec.Code)
		}
		if !strings.Contains(rec.Body.String(), `"code":"invalid_request"`) {
			t.Errorf("body = %s, want invalid_request error envelope", rec.Body.String())
		}
	})

	t.Run("empty word returns 400 invalid_request", func(t *testing.T) {
		h := newTestHandler(&fakeExecutor{}, &fakeCardRepo{})
		req := httptest.NewRequest(http.MethodPut, "/words/1",
			strings.NewReader(`{"word":""}`))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)

		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400", rec.Code)
		}
		if !strings.Contains(rec.Body.String(), `"code":"invalid_request"`) {
			t.Errorf("body = %s, want invalid_request error envelope", rec.Body.String())
		}
	})

	t.Run("comma-containing word returns 400 invalid_request", func(t *testing.T) {
		// CR-01: editing a word into "a,b" would set up a later delete to
		// splinter it into unintended deletions — rejected at the gate.
		h := newTestHandler(&fakeExecutor{}, &fakeCardRepo{})
		req := httptest.NewRequest(http.MethodPut, "/words/1",
			strings.NewReader(`{"word":"a,b"}`))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)

		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400 for comma word (body=%s)", rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), `"code":"invalid_request"`) {
			t.Errorf("body = %s, want invalid_request error envelope", rec.Body.String())
		}
	})

	t.Run("non-numeric id returns 400 invalid_request", func(t *testing.T) {
		h := newTestHandler(&fakeExecutor{}, &fakeCardRepo{})
		req := httptest.NewRequest(http.MethodPut, "/words/abc",
			strings.NewReader(`{"word":"x"}`))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)

		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400 for id abc", rec.Code)
		}
		if !strings.Contains(rec.Body.String(), `"code":"invalid_request"`) {
			t.Errorf("body = %s, want invalid_request error envelope", rec.Body.String())
		}
	})

	t.Run("category key in body returns 400 (read-only per D-07)", func(t *testing.T) {
		// DisallowUnknownFields must reject "category" outright — the edit
		// scope never moves a card between categories (D-07).
		h := newTestHandler(&fakeExecutor{}, &fakeCardRepo{})
		req := httptest.NewRequest(http.MethodPut, "/words/1",
			strings.NewReader(`{"word":"x","category":"english"}`))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)

		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400 for unknown field category", rec.Code)
		}
		if !strings.Contains(rec.Body.String(), `"code":"invalid_request"`) {
			t.Errorf("body = %s, want invalid_request error envelope", rec.Body.String())
		}
	})
}

func TestEditWordNotFound(t *testing.T) {
	exec := &fakeExecutor{editErr: &pybridge.ExitError{
		Cmd: "edit", ExitCode: 1, Stdout: "Card 42 not found", Err: pybridge.ErrPythonExit,
	}}
	h := newTestHandler(exec, &fakeCardRepo{})

	req := httptest.NewRequest(http.MethodPut, "/words/42",
		strings.NewReader(`{"word":"x"}`))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (body=%s)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"code":"not_found"`) {
		t.Errorf("body = %s, want not_found error envelope", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "Card 42 not found") {
		t.Errorf("body = %s, want the script stdout diagnostic as message", rec.Body.String())
	}
}

func TestRenameUnit(t *testing.T) {
	t.Run("valid body forwards three fields and echoes renamed/from/to", func(t *testing.T) {
		exec := &fakeExecutor{renameResult: pybridge.RenameUnitResult{
			Renamed: 2, From: "L1", To: "L2",
		}}
		h := newTestHandler(exec, &fakeCardRepo{})

		req := httptest.NewRequest(http.MethodPost, "/units/rename",
			strings.NewReader(`{"old_unit":"L1","new_unit":"L2","category":"english"}`))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
		}
		var resp struct {
			Renamed int    `json:"renamed"`
			From    string `json:"from"`
			To      string `json:"to"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("decode response %q: %v", rec.Body.String(), err)
		}
		if resp.Renamed != 2 || resp.From != "L1" || resp.To != "L2" {
			t.Errorf("response = %+v, want {2 L1 L2}", resp)
		}
		if len(exec.renameCalls) != 1 {
			t.Fatalf("RunRenameUnit calls = %d, want 1", len(exec.renameCalls))
		}
		got := exec.renameCalls[0]
		if got.OldUnit != "L1" || got.NewUnit != "L2" || got.Category != "english" {
			t.Errorf("forwarded RenameUnitOpts = %+v, want {L1 L2 english}", got)
		}
	})

	t.Run("missing field returns 400", func(t *testing.T) {
		cases := map[string]string{
			"old_unit":  `{"new_unit":"L2","category":"english"}`,
			"new_unit":  `{"old_unit":"L1","category":"english"}`,
			"category":  `{"old_unit":"L1","new_unit":"L2"}`,
			"empty old": `{"old_unit":"","new_unit":"L2","category":"english"}`,
		}
		for name, body := range cases {
			t.Run(name, func(t *testing.T) {
				h := newTestHandler(&fakeExecutor{}, &fakeCardRepo{})
				req := httptest.NewRequest(http.MethodPost, "/units/rename",
					strings.NewReader(body))
				rec := httptest.NewRecorder()
				h.ServeHTTP(rec, req)

				if rec.Code != http.StatusBadRequest {
					t.Fatalf("%s: status = %d, want 400 (body=%s)", name, rec.Code, rec.Body.String())
				}
				if !strings.Contains(rec.Body.String(), `"code":"invalid_request"`) {
					t.Errorf("%s: body = %s, want invalid_request envelope", name, rec.Body.String())
				}
			})
		}
	})

	t.Run("no-cards stdout diagnostic maps to 404 not_found (Pitfall 2)", func(t *testing.T) {
		exec := &fakeExecutor{renameErr: &pybridge.ExitError{
			Cmd:      "rename-unit",
			ExitCode: 1,
			Stdout:   `No cards found with unit 'L1' in category 'english'`,
			Err:      pybridge.ErrPythonExit,
		}}
		h := newTestHandler(exec, &fakeCardRepo{})

		req := httptest.NewRequest(http.MethodPost, "/units/rename",
			strings.NewReader(`{"old_unit":"L1","new_unit":"L2","category":"english"}`))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)

		if rec.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want 404 (body=%s)", rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), `"code":"not_found"`) {
			t.Errorf("body = %s, want not_found error envelope", rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), "No cards found with unit 'L1'") {
			t.Errorf("body = %s, want the stdout diagnostic as message", rec.Body.String())
		}
	})
}

func TestRenameUnitPythonFailure(t *testing.T) {
	exec := &fakeExecutor{renameErr: pybridge.ErrPythonTimeout}
	h := newTestHandler(exec, &fakeCardRepo{})

	req := httptest.NewRequest(http.MethodPost, "/units/rename",
		strings.NewReader(`{"old_unit":"L1","new_unit":"L2","category":"english"}`))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502 for ErrPythonTimeout", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"code":"python_failed"`) {
		t.Errorf("body = %s, want python_failed error envelope", rec.Body.String())
	}
}

func TestDeleteWordsByWordList(t *testing.T) {
	exec := &fakeExecutor{deleteResult: pybridge.DeleteWordsResult{Deleted: 2}}
	h := newTestHandler(exec, &fakeCardRepo{})

	req := httptest.NewRequest(http.MethodDelete, "/words",
		strings.NewReader(`{"category":"english","words":["a","b"]}`))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	var resp struct {
		Deleted int `json:"deleted"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response %q: %v", rec.Body.String(), err)
	}
	if resp.Deleted != 2 {
		t.Errorf("deleted = %d, want 2 (fake passthrough)", resp.Deleted)
	}
	if len(exec.deleteCalls) != 1 {
		t.Fatalf("RunDeleteWords calls = %d, want 1", len(exec.deleteCalls))
	}
	got := exec.deleteCalls[0]
	if len(got.Words) != 2 || got.Words[0] != "a" || got.Words[1] != "b" {
		t.Errorf("forwarded Words = %v, want [a b]", got.Words)
	}
	if got.Category != "english" {
		t.Errorf("forwarded Category = %q, want english", got.Category)
	}
	if got.Unit != "" {
		t.Errorf("forwarded Unit = %q, want empty in word-list mode", got.Unit)
	}
}

func TestDeleteWordsByUnit(t *testing.T) {
	exec := &fakeExecutor{deleteResult: pybridge.DeleteWordsResult{Deleted: 5}}
	h := newTestHandler(exec, &fakeCardRepo{})

	req := httptest.NewRequest(http.MethodDelete, "/words",
		strings.NewReader(`{"category":"english","unit":"u1"}`))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	if len(exec.deleteCalls) != 1 {
		t.Fatalf("RunDeleteWords calls = %d, want 1", len(exec.deleteCalls))
	}
	got := exec.deleteCalls[0]
	if got.Unit != "u1" {
		t.Errorf("forwarded Unit = %q, want u1 (unit mode)", got.Unit)
	}
	if len(got.Words) != 0 {
		t.Errorf("forwarded Words = %v, want empty in unit mode", got.Words)
	}
}

func TestDeleteWordsValidation(t *testing.T) {
	cases := map[string]string{
		"both targets":      `{"category":"english","words":["a"],"unit":"u1"}`,
		"neither target":    `{"category":"english"}`,
		"missing category":  `{"words":["a"]}`,
		"unknown category":  `{"category":"francais","words":["a"]}`,
		"empty words array": `{"category":"english","words":[]}`,
		"comma in word":     `{"category":"english","words":["a,b"]}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			h := newTestHandler(&fakeExecutor{}, &fakeCardRepo{})
			req := httptest.NewRequest(http.MethodDelete, "/words",
				strings.NewReader(body))
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)

			if rec.Code != http.StatusBadRequest {
				t.Fatalf("%s: status = %d, want 400 (body=%s)", name, rec.Code, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), `"code":"invalid_request"`) {
				t.Errorf("%s: body = %s, want invalid_request envelope", name, rec.Body.String())
			}
		})
	}

	t.Run("101 words returns 400", func(t *testing.T) {
		var sb strings.Builder
		sb.WriteString(`{"category":"english","words":[`)
		for i := 0; i < 101; i++ {
			if i > 0 {
				sb.WriteString(",")
			}
			sb.WriteString(`"w"`)
		}
		sb.WriteString(`]}`)
		h := newTestHandler(&fakeExecutor{}, &fakeCardRepo{})
		req := httptest.NewRequest(http.MethodDelete, "/words", strings.NewReader(sb.String()))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)

		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400 for 101 words", rec.Code)
		}
		if !strings.Contains(rec.Body.String(), `"code":"invalid_request"`) {
			t.Errorf("body = %s, want invalid_request envelope", rec.Body.String())
		}
	})
}

func TestDeleteWordsPythonFailure(t *testing.T) {
	exec := &fakeExecutor{deleteErr: &pybridge.ExitError{
		Cmd: "delete-words", ExitCode: 1, Stderr: "boom", Err: pybridge.ErrPythonExit,
	}}
	h := newTestHandler(exec, &fakeCardRepo{})

	req := httptest.NewRequest(http.MethodDelete, "/words",
		strings.NewReader(`{"category":"english","words":["a"]}`))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502 for ExitError", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"code":"python_failed"`) {
		t.Errorf("body = %s, want python_failed error envelope", rec.Body.String())
	}
}
