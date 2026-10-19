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
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/xiaotian/home-tutor-agent/pkg/database"
	"github.com/xiaotian/home-tutor-agent/pkg/infra"
	"github.com/xiaotian/home-tutor-agent/pkg/lesson"
	"github.com/xiaotian/home-tutor-agent/pkg/pybridge"
)

// fakeExecutor is a manual fake for pybridge.Executor (D-48).
type fakeExecutor struct {
	renameErr            error
	addErr               error
	textbookUpdateErr    error
	textbookCreateErr    error
	editErr              error
	assignErr            error
	deleteErr            error
	participationErr     error
	unassignErr          error
	renameResult         pybridge.RenameUnitResult
	editCalls            []pybridge.EditOpts
	renameCalls          []pybridge.RenameUnitOpts
	participationCalls   []pybridge.SetParticipationOpts
	unassignCalls        []pybridge.UnassignWordsOpts
	deleteCalls          []pybridge.DeleteWordsOpts
	editResult           pybridge.EditResult
	textbookCreateCalls  []pybridge.TextbookCreateOpts
	addCalls             []pybridge.AddOpts
	textbookUpdateCalls  []pybridge.TextbookUpdateOpts
	assignCalls          []pybridge.AssignWordsOpts
	textbookCreateResult pybridge.TextbookMutationResult
	textbookUpdateResult pybridge.TextbookMutationResult
	participationResult  pybridge.SetParticipationResult
	unassignResult       pybridge.UnassignWordsResult
	assignResult         pybridge.AssignWordsResult
	addResult            pybridge.AddResult
	deleteResult         pybridge.DeleteWordsResult
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

func (f *fakeExecutor) RunSetParticipation(_ context.Context, opts pybridge.SetParticipationOpts) (pybridge.SetParticipationResult, error) {
	f.participationCalls = append(f.participationCalls, opts)
	return f.participationResult, f.participationErr
}

func (f *fakeExecutor) RunRenameUnit(_ context.Context, opts pybridge.RenameUnitOpts) (pybridge.RenameUnitResult, error) {
	f.renameCalls = append(f.renameCalls, opts)
	return f.renameResult, f.renameErr
}

func (f *fakeExecutor) RunDeleteWords(_ context.Context, opts pybridge.DeleteWordsOpts) (pybridge.DeleteWordsResult, error) {
	f.deleteCalls = append(f.deleteCalls, opts)
	return f.deleteResult, f.deleteErr
}

func (f *fakeExecutor) RunTextbookCreate(_ context.Context, opts pybridge.TextbookCreateOpts) (pybridge.TextbookMutationResult, error) {
	f.textbookCreateCalls = append(f.textbookCreateCalls, opts)
	return f.textbookCreateResult, f.textbookCreateErr
}

func (f *fakeExecutor) RunTextbookUpdate(_ context.Context, opts pybridge.TextbookUpdateOpts) (pybridge.TextbookMutationResult, error) {
	f.textbookUpdateCalls = append(f.textbookUpdateCalls, opts)
	return f.textbookUpdateResult, f.textbookUpdateErr
}

func (f *fakeExecutor) RunAssignWords(_ context.Context, opts pybridge.AssignWordsOpts) (pybridge.AssignWordsResult, error) {
	f.assignCalls = append(f.assignCalls, opts)
	return f.assignResult, f.assignErr
}

func (f *fakeExecutor) RunUnassignWords(_ context.Context, opts pybridge.UnassignWordsOpts) (pybridge.UnassignWordsResult, error) {
	f.unassignCalls = append(f.unassignCalls, opts)
	return f.unassignResult, f.unassignErr
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

func (f *fakeCardRepo) CategoriesByIDs(_ context.Context, ids []int64) (map[int64]string, error) {
	byID := make(map[int64]database.Card, len(f.cards))
	for _, c := range f.cards {
		byID[c.ID] = c
	}
	cats := make(map[int64]string, len(ids))
	for _, id := range ids {
		if c, ok := byID[id]; ok {
			cats[id] = c.Category
		}
	}
	return cats, nil
}

func (f *fakeCardRepo) CountByUnit(_ context.Context, category string) ([]database.UnitCount, error) {
	f.countsSeen = append(f.countsSeen, category)
	return f.unitCounts, f.countErr
}

// newLessonTestStores opens a temp SQLite database (lesson.Migrate applied)
// and returns the db handle (for seeding) plus the three read-path stores the
// lessons routes consume. Every handler test gets REAL stores — no nil
// dependency anywhere (Pitfall 3).
func newLessonTestStores(t *testing.T) (*sql.DB, *lesson.Store, *lesson.PracticeStore, *lesson.ReceiptStore) {
	t.Helper()
	db, err := database.Open(filepath.Join(t.TempDir(), "api-lessons.db"))
	if err != nil {
		t.Fatalf("open lessons test db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err = lesson.Migrate(context.Background(), db); err != nil {
		t.Fatalf("migrate lessons test db: %v", err)
	}
	lessons, err := lesson.NewStore(db, infra.NewNoOpLogger())
	if err != nil {
		t.Fatalf("lesson.NewStore: %v", err)
	}
	practice, err := lesson.NewPracticeStore(db, infra.NewNoOpLogger())
	if err != nil {
		t.Fatalf("lesson.NewPracticeStore: %v", err)
	}
	receipts, err := lesson.NewReceiptStore(db, infra.NewNoOpLogger())
	if err != nil {
		t.Fatalf("lesson.NewReceiptStore: %v", err)
	}
	return db, lessons, practice, receipts
}

// newTestHandlerWithStores wires a handler around caller-owned stores (the
// lessons-route tests seed them through the db handle). The recognizer is
// nil here — photo-recognition routes have their own harness in
// photo_recognize_test.go.
func newTestHandlerWithStores(exec pybridge.Executor, cards database.CardRepository, lessons *lesson.Store, practice *lesson.PracticeStore, receipts *lesson.ReceiptStore) http.Handler {
	return NewHandler(exec, cards, nil, lessons, practice, receipts, nil, nil, nil, nil, infra.PythonConfig{}, infra.NewNoOpLogger())
}

func newTestHandler(t *testing.T, exec pybridge.Executor, cards database.CardRepository) http.Handler {
	t.Helper()
	_, lessons, practice, receipts := newLessonTestStores(t)
	return newTestHandlerWithStores(exec, cards, lessons, practice, receipts)
}

// seedLessonRow inserts a lessons row with full control over the projection
// inputs (stage/verdicts/outcomes/cursor) — the shape the classroom write
// path produces, written directly so tests pin the read contract.
func seedLessonRow(t *testing.T, db *sql.DB, lessonID, cardsJSON, verdictsJSON, outcomesJSON, stage string, currentIndex, unchecked int, createdAt string) {
	t.Helper()
	if _, err := db.ExecContext(context.Background(),
		"INSERT INTO lessons (lesson_id, words, cards, verdicts, outcomes, stage, current_index, unchecked, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
		lessonID, `["词"]`, cardsJSON, verdictsJSON, outcomesJSON, stage, currentIndex, unchecked, createdAt, createdAt,
	); err != nil {
		t.Fatalf("seed lesson %s: %v", lessonID, err)
	}
}

func TestAddWords(t *testing.T) {
	t.Run("valid body returns 200 with added/skipped passthrough", func(t *testing.T) {
		exec := &fakeExecutor{addResult: pybridge.AddResult{Added: 3, Skipped: 2}}
		h := newTestHandler(t, exec, &fakeCardRepo{})

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

	t.Run("meaning_source forwarded to the bridge, enum validated at the gate", func(t *testing.T) {
		// Issue #8: generated provenance rides the add path untouched;
		// a malformed flag is a 400 (invalid_request), never stored.
		exec := &fakeExecutor{}
		h := newTestHandler(t, exec, &fakeCardRepo{})

		body := `{"category":"english","entries":[{"word":"apple","meaning":"n. 苹果","meaning_source":"generated"},{"word":"banana","meaning":"香蕉"}]}`
		req := httptest.NewRequest(http.MethodPost, "/words", strings.NewReader(body))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
		}
		if len(exec.addCalls) != 1 {
			t.Fatalf("RunAdd calls = %d, want 1", len(exec.addCalls))
		}
		entries := exec.addCalls[0].Entries
		if entries[0].MeaningSource != "generated" || entries[1].MeaningSource != "" {
			t.Errorf("forwarded MeaningSource = [%q,%q], want [generated,\"\"]",
				entries[0].MeaningSource, entries[1].MeaningSource)
		}

		bad := `{"category":"english","entries":[{"word":"apple","meaning":"x","meaning_source":"textbook"}]}`
		req = httptest.NewRequest(http.MethodPost, "/words", strings.NewReader(bad))
		rec = httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400 (body=%s)", rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), "meaning_source") {
			t.Errorf("body = %s, want the offending field named", rec.Body.String())
		}
	})

	t.Run("missing category returns 400 invalid_request", func(t *testing.T) {
		h := newTestHandler(t, &fakeExecutor{}, &fakeCardRepo{})
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
		h := newTestHandler(t, &fakeExecutor{}, &fakeCardRepo{})
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
		h := newTestHandler(t, &fakeExecutor{}, &fakeCardRepo{})
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

	t.Run("empty word returns 400 invalid_request", func(t *testing.T) {
		// WR-01: an empty word must fail validation (400) rather than leak
		// to Python's exit-1 and surface as a misleading 502 python_failed.
		h := newTestHandler(t, &fakeExecutor{}, &fakeCardRepo{})
		req := httptest.NewRequest(http.MethodPost, "/words",
			strings.NewReader(`{"category":"english","entries":[{"word":""}]}`))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)

		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400 for empty word (body=%s)", rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), `"code":"invalid_request"`) {
			t.Errorf("body = %s, want invalid_request error envelope", rec.Body.String())
		}
	})

	t.Run("comma-containing word returns 400 invalid_request", func(t *testing.T) {
		// CR-01: the delete path joins words with "," and Python splits
		// back on ",", so a comma word must never enter the library.
		h := newTestHandler(t, &fakeExecutor{}, &fakeCardRepo{})
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
		//nolint:misspell // deliberate typo example: "catagory" is rejected rather than silently ignored.
		h := newTestHandler(t, &fakeExecutor{}, &fakeCardRepo{})
		req := httptest.NewRequest(http.MethodPost, "/words",
			strings.NewReader(`{"catagory":"english","entries":[{"word":"x"}]}`)) //nolint:misspell // deliberate typo fixture
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
		h := newTestHandler(t, exec, &fakeCardRepo{})
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
		h := newTestHandler(t, exec, &fakeCardRepo{})
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
		h := newTestHandler(t, exec, &fakeCardRepo{})
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
		h := newTestHandler(t, &fakeExecutor{}, repo)

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
		h := newTestHandler(t, &fakeExecutor{}, &fakeCardRepo{})
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
				Unit: "L1", Category: "english", Participates: true},
		}}
		h := newTestHandler(t, &fakeExecutor{}, repo)
		req := httptest.NewRequest(http.MethodGet, "/words", nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)

		var resp struct {
			Words []struct {
				Word         string `json:"word"`
				Meaning      string `json:"meaning"`
				Example      string `json:"example"`
				Unit         string `json:"unit"`
				Category     string `json:"category"`
				ID           int64  `json:"id"`
				Participates bool   `json:"participates"`
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
			w.Example != "I eat an apple." || w.Unit != "L1" || w.Category != "english" || !w.Participates {
			t.Errorf("word entry = %+v, want the canned card fields", w)
		}
	})

	t.Run("repo error maps to 500 internal", func(t *testing.T) {
		repo := &fakeCardRepo{listErr: errors.New("db down")}
		h := newTestHandler(t, &fakeExecutor{}, repo)
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

func TestSetWordParticipation(t *testing.T) {
	t.Run("false maps only the participation field", func(t *testing.T) {
		exec := &fakeExecutor{}
		h := newTestHandler(t, exec, &fakeCardRepo{})
		req := httptest.NewRequest(http.MethodPut, "/words/participation", strings.NewReader(`{"ids":[42,43],"participates":false}`))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
		}
		if len(exec.participationCalls) != 1 {
			t.Fatalf("RunSetParticipation calls = %d, want 1", len(exec.participationCalls))
		}
		got := exec.participationCalls[0]
		if !reflect.DeepEqual(got.IDs, []int64{42, 43}) || got.Participates {
			t.Fatalf("SetParticipationOpts = %+v, want ids [42 43] and false", got)
		}
	})
	t.Run("missing field is rejected before Python", func(t *testing.T) {
		exec := &fakeExecutor{}
		h := newTestHandler(t, exec, &fakeCardRepo{})
		req := httptest.NewRequest(http.MethodPut, "/words/participation", strings.NewReader(`{}`))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400", rec.Code)
		}
		if len(exec.participationCalls) != 0 {
			t.Fatalf("RunSetParticipation calls = %d, want 0", len(exec.participationCalls))
		}
	})
	t.Run("bridge failure is reported", func(t *testing.T) {
		exec := &fakeExecutor{participationErr: pybridge.ErrPythonTimeout}
		h := newTestHandler(t, exec, &fakeCardRepo{})
		req := httptest.NewRequest(http.MethodPut, "/words/participation", strings.NewReader(`{"ids":[42],"participates":true}`))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadGateway {
			t.Fatalf("status = %d, want 502", rec.Code)
		}
		if !strings.Contains(rec.Body.String(), `"code":"python_failed"`) {
			t.Fatalf("body = %s", rec.Body.String())
		}
	})
	t.Run("missing id diagnostic maps to 404 not_found (all-or-nothing rollback)", func(t *testing.T) {
		exec := &fakeExecutor{participationErr: &pybridge.ExitError{
			Cmd: "set-participation", ExitCode: 1,
			Stdout: "Error: card ids not found: 7, 42", Err: pybridge.ErrPythonExit,
		}}
		h := newTestHandler(t, exec, &fakeCardRepo{})
		req := httptest.NewRequest(http.MethodPut, "/words/participation", strings.NewReader(`{"ids":[7,42],"participates":false}`))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want 404 (body=%s)", rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), `"code":"not_found"`) ||
			!strings.Contains(rec.Body.String(), "card ids not found: 7, 42") {
			t.Fatalf("body = %s, want not_found envelope carrying the missing-id diagnostic", rec.Body.String())
		}
	})
}

func TestListUnits(t *testing.T) {
	t.Run("no category forwards empty string and returns units envelope", func(t *testing.T) {
		repo := &fakeCardRepo{unitCounts: []database.UnitCount{
			{Unit: "unitA", Count: 3},
			{Unit: "unitB", Count: 2},
		}}
		h := newTestHandler(t, &fakeExecutor{}, repo)

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
		h := newTestHandler(t, &fakeExecutor{}, repo)

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
		h := newTestHandler(t, &fakeExecutor{}, &fakeCardRepo{})
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
		h := newTestHandler(t, &fakeExecutor{}, repo)

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
		h := newTestHandler(t, exec, &fakeCardRepo{})

		body := `{"word":"apple","meaning":"苹果","example":"I eat an apple.","unit":"L1"}`
		req := httptest.NewRequest(http.MethodPut, "/words/42", strings.NewReader(body))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
		}
		var resp struct {
			Edited struct {
				Word string `json:"word"`
				ID   int64  `json:"id"`
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
		h := newTestHandler(t, exec, &fakeCardRepo{})

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
		h := newTestHandler(t, &fakeExecutor{}, &fakeCardRepo{})
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
		h := newTestHandler(t, &fakeExecutor{}, &fakeCardRepo{})
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
		h := newTestHandler(t, &fakeExecutor{}, &fakeCardRepo{})
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
		h := newTestHandler(t, &fakeExecutor{}, &fakeCardRepo{})
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
		h := newTestHandler(t, &fakeExecutor{}, &fakeCardRepo{})
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
	h := newTestHandler(t, exec, &fakeCardRepo{})

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
		h := newTestHandler(t, exec, &fakeCardRepo{})

		req := httptest.NewRequest(http.MethodPost, "/units/rename",
			strings.NewReader(`{"old_unit":"L1","new_unit":"L2","category":"english"}`))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
		}
		var resp struct {
			From    string `json:"from"`
			To      string `json:"to"`
			Renamed int    `json:"renamed"`
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
				h := newTestHandler(t, &fakeExecutor{}, &fakeCardRepo{})
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
		h := newTestHandler(t, exec, &fakeCardRepo{})

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
	h := newTestHandler(t, exec, &fakeCardRepo{})

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
	h := newTestHandler(t, exec, &fakeCardRepo{})

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
	h := newTestHandler(t, exec, &fakeCardRepo{})

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
			h := newTestHandler(t, &fakeExecutor{}, &fakeCardRepo{})
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
		h := newTestHandler(t, &fakeExecutor{}, &fakeCardRepo{})
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
	h := newTestHandler(t, exec, &fakeCardRepo{})

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

// ---------------------------------------------------------------------------
// 13.3-02: the read-only parent achievement routes (GET /lessons,
// GET /lessons/{id}).
// ---------------------------------------------------------------------------

// testLessonCards is the three-word card JSON used across the lessons-route
// tests. The surfaces 苹果/香蕉/橘子 are the leak canaries: they must NEVER
// appear in any in-progress lesson's response (behaviors 2/4).
const testLessonCards = `[{"word":"苹果","meaning":"apple","example":"I eat an apple."},{"word":"香蕉","meaning":"banana","example":"Banana split."},{"word":"橘子","meaning":"orange","example":"Orange juice."}]`

// summaryItemKeys is the EXACT key set an in-progress lesson may expose —
// list row and detail summary alike. Anything beyond this set on an
// in-progress row is a leak-guard breach. category (Issue #10) is summary
// metadata — the subject badge, never a word surface.
var summaryItemKeys = []string{
	"lesson_id", "created_at", "word_count", "stage", "kind", "completed",
	"unchecked", "save_status", "correct", "wrong", "unconfirmed", "category",
}

// assertSummaryKeysExact fails when the decoded row's key set differs from
// the summary allowlist (stronger than absence-of-specific-keys: a newly
// added word-surface field cannot slip through).
func assertSummaryKeysExact(t *testing.T, row map[string]any) {
	t.Helper()
	if len(row) != len(summaryItemKeys) {
		t.Fatalf("row has %d keys (%v), want exactly %d summary keys", len(row), keysOf(row), len(summaryItemKeys))
	}
	for _, k := range summaryItemKeys {
		if _, ok := row[k]; !ok {
			t.Errorf("row missing summary key %q (keys: %v)", k, keysOf(row))
		}
	}
}

func keysOf(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func TestLessonAPIReceiptFailureDegrades(t *testing.T) {
	for _, tc := range []struct {
		name         string
		stage        string
		historical   bool
		missingTable bool
	}{
		{name: "corrupt latest", stage: "submitted"},
		{name: "corrupt history with healthy latest", stage: "submitted", historical: true},
		{name: "missing receipt table", stage: "submitted", missingTable: true},
		{name: "checking lesson stays summary only", stage: "checking"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, lessons, practice, receipts := newLessonTestStores(t)
			h := newTestHandlerWithStores(&fakeExecutor{}, &fakeCardRepo{}, lessons, practice, receipts)
			const id = "r0-20261010"
			seedLessonRow(t, db, id, testLessonCards,
				`[{"word_index":0,"correct":true,"manual":true}]`, `{}`,
				tc.stage, 3, 0, "2026-10-10 12:00:00")
			seedLessonRow(t, db, "r0-20261009", testLessonCards, `[]`, `{}`,
				"checking", 3, 0, "2026-10-09 12:00:00")
			brokenID, err := receipts.Begin(context.Background(), lesson.ReceiptInput{
				LessonID: id, RequestID: "broken", Kind: lesson.ReceiptKindCheck,
				Requested: []string{"苹果"},
			})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec("UPDATE lesson_save_receipts SET requested = ? WHERE id = ?", "{broken", brokenID.ID); err != nil {
				t.Fatal(err)
			}
			if tc.historical {
				goodID, err := receipts.Begin(context.Background(), lesson.ReceiptInput{
					LessonID: id, RequestID: "healthy", Kind: lesson.ReceiptKindCheck,
					Requested: []string{"苹果"},
				})
				if err != nil {
					t.Fatal(err)
				}
				if err := receipts.Complete(context.Background(), goodID.ID, []string{"苹果"}, nil, true); err != nil {
					t.Fatal(err)
				}
			}
			if tc.missingTable {
				if _, err := db.Exec("DROP TABLE lesson_save_receipts"); err != nil {
					t.Fatal(err)
				}
			}

			list := httptest.NewRecorder()
			h.ServeHTTP(list, httptest.NewRequest(http.MethodGet, "/lessons", nil))
			if list.Code != http.StatusOK {
				t.Fatalf("list status = %d: %s", list.Code, list.Body.String())
			}
			var page struct {
				Lessons []map[string]any `json:"lessons"`
			}
			if err := json.Unmarshal(list.Body.Bytes(), &page); err != nil {
				t.Fatal(err)
			}
			if len(page.Lessons) != 2 || page.Lessons[0]["lesson_id"] != id {
				t.Fatalf("lesson list lost rows or order: %v", page.Lessons)
			}
			wantListStatus := "unknown"
			if tc.historical {
				wantListStatus = "saved" // Latest-row aggregation is still readable.
			}
			if page.Lessons[0]["save_status"] != wantListStatus {
				t.Errorf("list save_status = %v, want %s", page.Lessons[0]["save_status"], wantListStatus)
			}
			for _, row := range page.Lessons {
				assertSummaryKeysExact(t, row)
			}

			detail := httptest.NewRecorder()
			h.ServeHTTP(detail, httptest.NewRequest(http.MethodGet, "/lessons/"+id, nil))
			if detail.Code != http.StatusOK {
				t.Fatalf("detail status = %d: %s", detail.Code, detail.Body.String())
			}
			var row map[string]any
			if err := json.Unmarshal(detail.Body.Bytes(), &row); err != nil {
				t.Fatal(err)
			}
			if row["save_status"] != "unknown" {
				t.Errorf("detail save_status = %v, want unknown", row["save_status"])
			}
			if tc.stage == "checking" {
				assertSummaryKeysExact(t, row)
				for _, canary := range []string{"苹果", "香蕉", "橘子"} {
					if strings.Contains(detail.Body.String(), canary) {
						t.Errorf("checking detail leaked %s", canary)
					}
				}
			} else {
				words, ok := row["words"].([]any)
				if !ok || len(words) != 3 {
					t.Errorf("words = %v, want all three cards", row["words"])
				}
				results, ok := row["results"].([]any)
				if !ok || len(results) != 1 || row["correct"] != float64(1) {
					t.Errorf("results/count changed: %v", row)
				}
				rows, ok := row["save_receipts"].([]any)
				if !ok || len(rows) != 0 {
					t.Errorf("save_receipts = %v, want []", row["save_receipts"])
				}
			}
		})
	}
}

func TestLessonAPIPrimaryReadFailureStillErrors(t *testing.T) {
	db, lessons, practice, receipts := newLessonTestStores(t)
	h := newTestHandlerWithStores(&fakeExecutor{}, &fakeCardRepo{}, lessons, practice, receipts)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/lessons", "/lessons/r0-20261010"} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		if w.Code != http.StatusInternalServerError {
			t.Errorf("%s status = %d, want 500", path, w.Code)
		}
	}
}

func TestListLessonsAPI(t *testing.T) {
	db, lessons, practice, receipts := newLessonTestStores(t)
	h := newTestHandlerWithStores(&fakeExecutor{}, &fakeCardRepo{}, lessons, practice, receipts)

	seedLessonRow(t, db, "r0-20261010", testLessonCards,
		`[{"word_index":0,"correct":true,"manual":true},{"word_index":1,"correct":false,"manual":true},{"word_index":2,"correct":true,"manual":false,"ocr_text":"橘了"}]`,
		`{"0":{"outcome":"confirmed","hints":0},"1":{"outcome":"confirmed","hints":1},"2":{"outcome":"hint","hints":2}}`,
		lesson.StageSubmitted, 3, 0, "2026-10-10 08:00:00")
	// r0-20261009 is IN the check stage: the check panel is open, the word
	// list is on the student's screen — the leak guard's hardest case
	// (RESEARCH Pitfall 2).
	seedLessonRow(t, db, "r0-20261009", testLessonCards, `[]`, `{}`,
		lesson.StageChecking, 1, 0, "2026-10-09 09:00:00")

	t.Run("no params returns 200 with the lessons array", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/lessons", nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
		}
		var resp struct {
			Lessons []map[string]any `json:"lessons"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("decode response %q: %v", rec.Body.String(), err)
		}
		if len(resp.Lessons) != 2 {
			t.Fatalf("lessons rows = %d, want 2", len(resp.Lessons))
		}
		if resp.Lessons[0]["lesson_id"] != "r0-20261010" {
			t.Errorf("first row = %v, want the newest lesson (created_at DESC)", resp.Lessons[0]["lesson_id"])
		}
	})

	t.Run("limit=abc and limit=0 are 400 invalid_request", func(t *testing.T) {
		for _, q := range []string{"?limit=abc", "?limit=0", "?limit=-2"} {
			req := httptest.NewRequest(http.MethodGet, "/lessons"+q, nil)
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)

			if rec.Code != http.StatusBadRequest {
				t.Fatalf("%s: status = %d, want 400 (body=%s)", q, rec.Code, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), `"code":"invalid_request"`) {
				t.Errorf("%s: body = %s, want invalid_request envelope", q, rec.Body.String())
			}
		}
	})

	t.Run("limit=1 passes through to the store", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/lessons?limit=1", nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
		}
		var resp struct {
			Lessons []map[string]any `json:"lessons"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if len(resp.Lessons) != 1 || resp.Lessons[0]["lesson_id"] != "r0-20261010" {
			t.Errorf("lessons = %v, want exactly the newest row", resp.Lessons)
		}
	})

	t.Run("in-progress row carries summary keys ONLY", func(t *testing.T) {
		// Behavior 2 — the leak-guard negative: the checking-stage row must
		// be a summary row with the EXACT summary key set, and no word
		// surface may appear anywhere in the response body.
		req := httptest.NewRequest(http.MethodGet, "/lessons", nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", rec.Code)
		}
		var envelope map[string]json.RawMessage
		if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
			t.Fatalf("decode envelope: %v", err)
		}
		var rows []map[string]any
		if err := json.Unmarshal(envelope["lessons"], &rows); err != nil {
			t.Fatalf("decode lessons: %v", err)
		}
		var checking map[string]any
		for _, row := range rows {
			if row["lesson_id"] == "r0-20261009" {
				checking = row
			}
		}
		if checking == nil {
			t.Fatal("checking row missing from list")
		}
		assertSummaryKeysExact(t, checking)
		if checking["completed"] != false {
			t.Errorf("checking row completed = %v, want false", checking["completed"])
		}
		if checking["kind"] != "lesson" {
			t.Errorf("checking row kind = %v, want lesson", checking["kind"])
		}
		for _, surface := range []string{"苹果", "香蕉", "橘子", `"words"`, `"verdicts"`, `"results"`} {
			if strings.Contains(rec.Body.String(), surface) {
				t.Errorf("list response leaks %s (body=%s)", surface, rec.Body.String())
			}
		}
	})

	t.Run("save_status reflects the receipt table, unknown when absent", func(t *testing.T) {
		// Behavior 7 (list half): a lesson whose receipt is still PENDING
		// reads save_status verbatim from the receipt row; a receipt-less
		// row reads unknown — never inferred from stage.
		if _, err := receipts.Begin(context.Background(), lesson.ReceiptInput{
			LessonID: "r0-20261010", RequestID: "req-1", Kind: lesson.ReceiptKindCheck,
			Requested: []string{"苹果", "香蕉", "橘子"},
		}); err != nil {
			t.Fatalf("Begin: %v", err)
		}
		req := httptest.NewRequest(http.MethodGet, "/lessons", nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", rec.Code)
		}
		var resp struct {
			Lessons []map[string]any `json:"lessons"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if resp.Lessons[0]["save_status"] != "pending" {
			t.Errorf("row with a pending receipt save_status = %v, want pending (status verbatim)", resp.Lessons[0]["save_status"])
		}
		if resp.Lessons[1]["save_status"] != "unknown" {
			t.Errorf("receipt-less checking row save_status = %v, want unknown", resp.Lessons[1]["save_status"])
		}
	})
}

func TestGetLessonAPI(t *testing.T) {
	db, lessons, practice, receipts := newLessonTestStores(t)
	h := newTestHandlerWithStores(&fakeExecutor{}, &fakeCardRepo{}, lessons, practice, receipts)

	// Completed lesson with verdicts/outcomes and a settled receipt (behavior 3).
	seedLessonRow(t, db, "r0-20261010", testLessonCards,
		`[{"word_index":0,"correct":true,"manual":true},{"word_index":1,"correct":false,"manual":true},{"word_index":2,"correct":true,"manual":false,"ocr_text":"橘了"}]`,
		`{"0":{"outcome":"confirmed","hints":0},"1":{"outcome":"confirmed","hints":1},"2":{"outcome":"hint","hints":2}}`,
		lesson.StageSubmitted, 3, 0, "2026-10-10 08:00:00")
	rec1, err := receipts.Begin(context.Background(), lesson.ReceiptInput{
		LessonID: "r0-20261010", RequestID: "req-1", Kind: lesson.ReceiptKindCheck,
		Requested: []string{"苹果", "香蕉", "橘子"},
	})
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if err := receipts.Complete(context.Background(), rec1.ID, []string{"苹果", "香蕉", "橘子"}, nil, true); err != nil {
		t.Fatalf("Complete: %v", err)
	}

	// Checking-stage and mid-dictation rows (behavior 4 — both summaries).
	seedLessonRow(t, db, "r0-20261009", testLessonCards, `[]`, `{}`,
		lesson.StageChecking, 1, 0, "2026-10-09 09:00:00")
	seedLessonRow(t, db, "r0-20261008", testLessonCards, `[]`, `{}`,
		"", 1, 0, "2026-10-08 10:00:00")

	// Legacy completed row with NO receipt (behavior 7).
	seedLessonRow(t, db, "r0-20261007", testLessonCards, `[]`, `{}`,
		"", 3, 0, "2026-10-07 10:00:00")

	// Practice lesson (behavior 6): a completed r0x- row with practice_logs
	// rows and its own practice-kind receipt.
	seedLessonRow(t, db, "r0x-20261008-120000",
		`[{"word":"apple","meaning":"苹果","example":"I eat an apple."},{"word":"banana","meaning":"香蕉","example":"Banana split."}]`,
		`[]`, `{}`, "", 2, 0, "2026-10-08 12:00:00")
	if err := practice.RecordPractice(context.Background(), []lesson.PracticeRecord{
		{LessonID: "r0x-20261008-120000", Word: "apple", Rating: "good", Correct: true, Hints: 0, ReviewedDay: "2026-10-08"},
		{LessonID: "r0x-20261008-120000", Word: "banana", Rating: "again", Correct: false, Hints: 2, ReviewedDay: "2026-10-08"},
	}); err != nil {
		t.Fatalf("RecordPractice: %v", err)
	}
	if _, err := receipts.Begin(context.Background(), lesson.ReceiptInput{
		LessonID: "r0x-20261008-120000", RequestID: "req-p1", Kind: lesson.ReceiptKindPractice,
		Requested: []string{"apple", "banana"},
	}); err != nil {
		t.Fatalf("Begin practice receipt: %v", err)
	}

	fetch := func(t *testing.T, lessonID string) (int, string) {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "/lessons/"+lessonID, nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code, rec.Body.String()
	}

	t.Run("completed lesson returns full per-word detail", func(t *testing.T) {
		// Behavior 3.
		code, body := fetch(t, "r0-20261010")
		if code != http.StatusOK {
			t.Fatalf("status = %d, want 200 (body=%s)", code, body)
		}
		var resp struct {
			LessonID   string `json:"lesson_id"`
			SaveStatus string `json:"save_status"`
			Words      []struct {
				Word    string `json:"word"`
				Meaning string `json:"meaning"`
				Example string `json:"example"`
			} `json:"words"`
			Results []struct {
				Manual  *bool  `json:"manual"`
				Word    string `json:"word"`
				Outcome string `json:"outcome"`
				OcrText string `json:"ocr_text"`
				Hints   int    `json:"hints"`
				Correct bool   `json:"correct"`
			} `json:"results"`
			SaveReceipts []struct {
				LessonID    string   `json:"lesson_id"`
				RequestID   string   `json:"request_id"`
				Kind        string   `json:"kind"`
				Status      string   `json:"status"`
				CreatedAt   string   `json:"created_at"`
				CompletedAt string   `json:"completed_at"`
				Requested   []string `json:"requested"`
				Updated     []string `json:"updated"`
				Missing     []string `json:"missing"`
			} `json:"save_receipts"`
		}
		if err := json.Unmarshal([]byte(body), &resp); err != nil {
			t.Fatalf("decode detail %q: %v", body, err)
		}
		if resp.LessonID != "r0-20261010" || resp.SaveStatus != "saved" {
			t.Errorf("identity/save = %s/%s, want r0-20261010/saved", resp.LessonID, resp.SaveStatus)
		}
		if len(resp.Words) != 3 || resp.Words[0].Word != "苹果" || resp.Words[0].Meaning != "apple" || resp.Words[0].Example != "I eat an apple." {
			t.Errorf("words = %+v, want the three seeded cards", resp.Words)
		}
		if len(resp.Results) != 3 {
			t.Fatalf("results rows = %d, want 3", len(resp.Results))
		}
		r0, r1, r2 := resp.Results[0], resp.Results[1], resp.Results[2]
		if r0.Word != "苹果" || !r0.Correct || r0.Manual == nil || !*r0.Manual || r0.Hints != 0 || r0.Outcome != "confirmed" || r0.OcrText != "" {
			t.Errorf("results[0] = %+v, want confirmed manual 苹果", r0)
		}
		if r1.Word != "香蕉" || r1.Correct || r1.Manual == nil || !*r1.Manual || r1.Hints != 1 {
			t.Errorf("results[1] = %+v, want manual-wrong 香蕉 with 1 hint", r1)
		}
		if r2.Word != "橘子" || r2.Manual == nil || *r2.Manual || r2.OcrText != "橘了" || r2.Outcome != "hint" || r2.Hints != 2 {
			t.Errorf("results[2] = %+v, want OCR prefill 橘子 with ocr_text 橘了", r2)
		}
		if len(resp.SaveReceipts) != 1 {
			t.Fatalf("save_receipts rows = %d, want 1", len(resp.SaveReceipts))
		}
		rc := resp.SaveReceipts[0]
		if rc.Kind != lesson.ReceiptKindCheck || rc.Status != lesson.ReceiptStatusSaved || rc.RequestID != "req-1" || rc.LessonID != "r0-20261010" {
			t.Errorf("receipt identity = %+v, want check/saved/req-1/r0-20261010", rc)
		}
		// Word sets are stored SORTED (the receipt storage contract), so the
		// wire order is byte-sorted, not submission order.
		if !reflect.DeepEqual(rc.Requested, []string{"橘子", "苹果", "香蕉"}) || !reflect.DeepEqual(rc.Updated, []string{"橘子", "苹果", "香蕉"}) || !reflect.DeepEqual(rc.Missing, []string{}) {
			t.Errorf("receipt sets = %v/%v/%v, want full requested+updated and empty missing", rc.Requested, rc.Updated, rc.Missing)
		}
		if rc.CreatedAt == "" || rc.CompletedAt == "" {
			t.Errorf("receipt timestamps = %q/%q, want both non-empty", rc.CreatedAt, rc.CompletedAt)
		}
	})

	t.Run("checking and mid-dictation lessons return summaries only", func(t *testing.T) {
		// Behavior 4 — both in-progress shapes, the leak guard's hardest
		// cases: the check-panel row AND the still-writing row.
		for _, id := range []string{"r0-20261009", "r0-20261008"} {
			code, body := fetch(t, id)
			if code != http.StatusOK {
				t.Fatalf("%s: status = %d, want 200 (body=%s)", id, code, body)
			}
			var row map[string]any
			if err := json.Unmarshal([]byte(body), &row); err != nil {
				t.Fatalf("%s: decode %q: %v", id, body, err)
			}
			assertSummaryKeysExact(t, row)
			if row["completed"] != false {
				t.Errorf("%s: completed = %v, want false", id, row["completed"])
			}
			for _, surface := range []string{"苹果", "香蕉", "橘子", `"words"`, `"verdicts"`, `"results"`, `"save_receipts"`} {
				if strings.Contains(body, surface) {
					t.Errorf("%s: detail leaks %s (body=%s)", id, surface, body)
				}
			}
		}
	})

	t.Run("unknown lesson is 404 not_found", func(t *testing.T) {
		code, body := fetch(t, "r0-19990101")
		if code != http.StatusNotFound {
			t.Fatalf("status = %d, want 404 (body=%s)", code, body)
		}
		if !strings.Contains(body, `"code":"not_found"`) || !strings.Contains(body, "lesson not found") {
			t.Errorf("body = %s, want not_found / lesson not found", body)
		}
	})

	t.Run("path-injected and oversized ids are 400 invalid_request", func(t *testing.T) {
		// The mux pattern already structurally excludes "/" (it can only
		// terminate a segment); backslash and whitespace CAN ride inside a
		// path value, so the whitelist must reject them (T-13.3-06).
		long := strings.Repeat("r", 65)
		for _, id := range []string{"r0%201", "r0%5C1", long} {
			code, body := fetch(t, id)
			if code != http.StatusBadRequest {
				t.Fatalf("%q: status = %d, want 400 (body=%s)", id, code, body)
			}
			if !strings.Contains(body, `"code":"invalid_request"`) {
				t.Errorf("%q: body = %s, want invalid_request envelope", id, body)
			}
		}
	})

	t.Run("practice lesson detail reads practice_logs and practice receipts", func(t *testing.T) {
		// Behavior 6.
		code, body := fetch(t, "r0x-20261008-120000")
		if code != http.StatusOK {
			t.Fatalf("status = %d, want 200 (body=%s)", code, body)
		}
		var resp struct {
			Kind  string `json:"kind"`
			Words []struct {
				Word string `json:"word"`
			} `json:"words"`
			Results []struct {
				Word    string `json:"word"`
				Rating  string `json:"rating"`
				Hints   int    `json:"hints"`
				Correct bool   `json:"correct"`
			} `json:"results"`
			SaveReceipts []struct {
				Kind   string `json:"kind"`
				Status string `json:"status"`
			} `json:"save_receipts"`
		}
		if err := json.Unmarshal([]byte(body), &resp); err != nil {
			t.Fatalf("decode practice detail %q: %v", body, err)
		}
		if resp.Kind != "practice" {
			t.Errorf("kind = %q, want practice (r0x- prefix)", resp.Kind)
		}
		if len(resp.Words) != 2 || resp.Words[0].Word != "apple" {
			t.Errorf("words = %+v, want the two practice cards", resp.Words)
		}
		if len(resp.Results) != 2 {
			t.Fatalf("results rows = %d, want 2 (practice_logs)", len(resp.Results))
		}
		if resp.Results[0].Word != "apple" || !resp.Results[0].Correct || resp.Results[0].Hints != 0 || resp.Results[0].Rating != "good" {
			t.Errorf("results[0] = %+v, want the seeded apple practice record", resp.Results[0])
		}
		if resp.Results[1].Word != "banana" || resp.Results[1].Correct || resp.Results[1].Hints != 2 || resp.Results[1].Rating != "again" {
			t.Errorf("results[1] = %+v, want the seeded banana practice record", resp.Results[1])
		}
		if len(resp.SaveReceipts) != 1 || resp.SaveReceipts[0].Kind != lesson.ReceiptKindPractice {
			t.Errorf("save_receipts = %+v, want exactly one practice-kind receipt", resp.SaveReceipts)
		}
	})

	t.Run("legacy completed lesson without receipts reads unknown and empty", func(t *testing.T) {
		// Behavior 7: save_status=unknown on the detail row, and
		// save_receipts serializes as an EMPTY ARRAY (not null, not absent).
		code, body := fetch(t, "r0-20261007")
		if code != http.StatusOK {
			t.Fatalf("status = %d, want 200 (body=%s)", code, body)
		}
		if !strings.Contains(body, `"save_receipts":[]`) {
			t.Errorf("body = %s, want save_receipts as an empty array", body)
		}
		var row map[string]any
		if err := json.Unmarshal([]byte(body), &row); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if row["save_status"] != "unknown" {
			t.Errorf("save_status = %v, want unknown (no receipt row)", row["save_status"])
		}
		if row["completed"] != true {
			t.Errorf("completed = %v, want true (legacy cursor completion)", row["completed"])
		}
		// A completed lesson DOES expose words — but results from an empty
		// verdicts column must still be an array, not null.
		if !strings.Contains(body, `"results":[]`) {
			t.Errorf("body = %s, want results as an empty array", body)
		}
	})
}
