package api

// textbooks_test.go — the Issue #4 route acceptance at the HTTP layer:
// validation gates (lesson membership, batch shape, progress bounds),
// the 404/400 pre-subprocess error mapping, the create read-back
// degradation, and the GET /words attribution projection. The Python
// bridge is the recording fakeExecutor; the textbook repository is REAL
// over the per-test temp SQLite (the same read path production uses).

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/xiaotian/home-tutor-agent/pkg/database"
	"github.com/xiaotian/home-tutor-agent/pkg/infra"
	"github.com/xiaotian/home-tutor-agent/pkg/pybridge"
)

// newTextbookTestHandler wires a handler with a REAL textbook repository
// over the temp DB and the recording fakeExecutor + fakeCardRepo.
func newTextbookTestHandler(t *testing.T, exec *fakeExecutor, cards *fakeCardRepo) (http.Handler, *sql.DB) {
	t.Helper()
	db, lessons, practice, receipts := newLessonTestStores(t)
	textbooks, err := database.NewTextbookRepository(db, nil)
	if err != nil {
		t.Fatalf("NewTextbookRepository: %v", err)
	}
	h := NewHandler(exec, cards, textbooks, lessons, practice, receipts,
		nil, infra.PythonConfig{}, infra.NewNoOpLogger())
	return h, db
}

// seedTextbookRow inserts one textbook row and returns its id.
func seedTextbookRow(t *testing.T, h *sql.DB, id int64, name, lessons string, learned int) {
	t.Helper()
	if _, err := h.Exec(
		`INSERT INTO textbooks (id, name, category, lessons, learned_lessons)
		 VALUES (?, ?, 'english', ?, ?)`, id, name, lessons, learned,
	); err != nil {
		t.Fatalf("seed textbook row: %v", err)
	}
}

// ensureTextbookTables creates the Issue #4 tables on the temp DB (they
// are Python-created in production; tests create them directly).
func ensureTextbookTables(t *testing.T, h *sql.DB) {
	t.Helper()
	for _, ddl := range []string{
		`CREATE TABLE IF NOT EXISTS textbooks (
			id INTEGER PRIMARY KEY AUTOINCREMENT, name TEXT NOT NULL,
			category TEXT NOT NULL DEFAULT 'english', edition TEXT NOT NULL DEFAULT '',
			stage TEXT NOT NULL DEFAULT '', grade TEXT NOT NULL DEFAULT '',
			volume TEXT NOT NULL DEFAULT '', module TEXT NOT NULL DEFAULT '',
			lessons TEXT NOT NULL DEFAULT '[]',
			learned_lessons INTEGER NOT NULL DEFAULT 0,
			is_primary INTEGER NOT NULL DEFAULT 0,
			created_at TEXT NOT NULL DEFAULT (datetime('now')),
			UNIQUE(name, category))`,
		`CREATE TABLE IF NOT EXISTS card_assignments (
			id INTEGER PRIMARY KEY AUTOINCREMENT, card_id INTEGER NOT NULL,
			textbook_id INTEGER NOT NULL, lesson TEXT NOT NULL DEFAULT '',
			include_in_scope INTEGER NOT NULL DEFAULT 0,
			created_at TEXT NOT NULL DEFAULT (datetime('now')),
			UNIQUE(card_id, textbook_id))`,
	} {
		if _, err := h.Exec(ddl); err != nil {
			t.Fatalf("ensure textbook tables: %v", err)
		}
	}
}

func TestTextbookRoutes(t *testing.T) {
	t.Run("GET /textbooks degrades to an empty list on a fresh database", func(t *testing.T) {
		h, _ := newTextbookTestHandler(t, &fakeExecutor{}, &fakeCardRepo{})
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/textbooks", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d (%s)", rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), `"textbooks":[]`) {
			t.Fatalf("body = %s, want an empty textbooks array", rec.Body.String())
		}
	})

	t.Run("POST /textbooks validates shape before any subprocess", func(t *testing.T) {
		exec := &fakeExecutor{}
		h, _ := newTextbookTestHandler(t, exec, &fakeCardRepo{})
		for name, body := range map[string]string{
			"missing name":        `{"category":"english"}`,
			"bad category":        `{"name":"X","category":"math"}`,
			"duplicate lesson":    `{"name":"X","lessons":["第1课","第1课"]}`,
			"empty lesson":        `{"name":"X","lessons":[""]}`,
			"oversized lesson":    `{"name":"X","lessons":["` + strings.Repeat("课", 101) + `"]}`,
		} {
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/textbooks", strings.NewReader(body)))
			if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), codeInvalidRequest) {
				t.Fatalf("%s: status = %d (%s), want 400 invalid_request", name, rec.Code, rec.Body.String())
			}
		}
		if len(exec.textbookCreateCalls) != 0 {
			t.Fatalf("subprocess calls = %d, want 0 on validation failures", len(exec.textbookCreateCalls))
		}
	})

	t.Run("POST /textbooks answers the full row when the read-back finds it", func(t *testing.T) {
		exec := &fakeExecutor{textbookCreateResult: pybridge.TextbookMutationResult{ID: 7, Name: "人教版"}}
		h, db := newTextbookTestHandler(t, exec, &fakeCardRepo{})
		ensureTextbookTables(t, db)
		seedTextbookRow(t, db, 7, "人教版", `["第1课","第2课"]`, 0)

		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/textbooks",
			strings.NewReader(`{"name":"人教版","category":"english","lessons":["第1课","第2课"]}`)))
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d (%s)", rec.Code, rec.Body.String())
		}
		var book database.Textbook
		if err := json.Unmarshal(rec.Body.Bytes(), &book); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if book.ID != 7 || book.LearnedLessons != 0 || len(book.Lessons) != 2 {
			t.Fatalf("read-back = %+v", book)
		}
	})

	t.Run("POST /textbook read-back miss degrades to the mutation payload", func(t *testing.T) {
		exec := &fakeExecutor{textbookCreateResult: pybridge.TextbookMutationResult{ID: 9, Name: "新教材"}}
		h, _ := newTextbookTestHandler(t, exec, &fakeCardRepo{})
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/textbooks",
			strings.NewReader(`{"name":"新教材"}`)))
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"id":9`) {
			t.Fatalf("status = %d (%s), want 200 with the id payload", rec.Code, rec.Body.String())
		}
	})

	t.Run("PUT /textbooks/{id} maps the gated fields and validates progress", func(t *testing.T) {
		exec := &fakeExecutor{}
		cards := &fakeCardRepo{}
		h, db := newTextbookTestHandler(t, exec, cards)
		ensureTextbookTables(t, db)
		seedTextbookRow(t, db, 3, "旧名", `["第1课","第2课"]`, 1)

		// progress beyond the current lesson list → 400, no subprocess.
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/textbooks/3",
			strings.NewReader(`{"learnedLessons":3}`)))
		if rec.Code != http.StatusBadRequest || len(exec.textbookUpdateCalls) != 0 {
			t.Fatalf("oversize progress: status = %d, calls = %d", rec.Code, len(exec.textbookUpdateCalls))
		}
		// progress beyond the NEW lesson list in the same request → 400.
		rec = httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/textbooks/3",
			strings.NewReader(`{"lessons":["仅一课"],"learnedLessons":2}`)))
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("progress vs new list: status = %d (%s)", rec.Code, rec.Body.String())
		}
		// an empty body → at-least-one-change rejection.
		rec = httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/textbooks/3", strings.NewReader(`{}`)))
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("empty update: status = %d", rec.Code)
		}
		// unknown textbook → 404.
		rec = httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/textbooks/999",
			strings.NewReader(`{"learnedLessons":1}`)))
		if rec.Code != http.StatusNotFound {
			t.Fatalf("unknown textbook: status = %d", rec.Code)
		}

		// legal update: progress + primary; read-back serves the SEEDED row
		// (the fake executor does not write SQLite — the request mapping is
		// what this asserts; the real write path is the pybridge e2e).
		rec = httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/textbooks/3",
			strings.NewReader(`{"learnedLessons":2,"setPrimary":true,"grade":"三年级"}`)))
		if rec.Code != http.StatusOK {
			t.Fatalf("legal update: status = %d (%s)", rec.Code, rec.Body.String())
		}
		calls := exec.textbookUpdateCalls
		if len(calls) != 1 || !calls[0].HasLearnedLessons || calls[0].LearnedLessons != 2 ||
			!calls[0].SetPrimary || calls[0].Grade != "三年级" || calls[0].HasLessons {
			t.Fatalf("update opts = %+v", calls[0])
		}
	})

	t.Run("assign validates lesson membership, card existence and category", func(t *testing.T) {
		exec := &fakeExecutor{}
		cards := &fakeCardRepo{cards: []database.Card{
			{ID: 1, Word: "apple", Category: "english"},
			{ID: 2, Word: "词语", Category: "chinese"},
		}}
		h, db := newTextbookTestHandler(t, exec, cards)
		ensureTextbookTables(t, db)
		seedTextbookRow(t, db, 5, "主教材", `["第1课","第2课"]`, 1)

		// lesson not in the textbook's list → 400 naming the textbook.
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/textbooks/5/assign",
			strings.NewReader(`{"cardIds":[1],"lesson":"第9课"}`)))
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "主教材") {
			t.Fatalf("unknown lesson: status = %d (%s)", rec.Code, rec.Body.String())
		}
		// missing card → 404 with the id.
		rec = httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/textbooks/5/assign",
			strings.NewReader(`{"cardIds":[1,42],"lesson":"第1课"}`)))
		if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "42") {
			t.Fatalf("missing card: status = %d (%s)", rec.Code, rec.Body.String())
		}
		// category mismatch → 400 naming the offending id.
		rec = httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/textbooks/5/assign",
			strings.NewReader(`{"cardIds":[2],"lesson":"第1课"}`)))
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "2") {
			t.Fatalf("category mismatch: status = %d (%s)", rec.Code, rec.Body.String())
		}
		// unknown textbook → 404.
		rec = httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/textbooks/99/assign",
			strings.NewReader(`{"cardIds":[1],"lesson":"第1课"}`)))
		if rec.Code != http.StatusNotFound {
			t.Fatalf("unknown textbook: status = %d", rec.Code)
		}
		// empty lesson (待确认) + include flag is legal and reaches the bridge.
		exec.textbookCreateCalls = nil
		rec = httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/textbooks/5/assign",
			strings.NewReader(`{"cardIds":[1],"lesson":"","includeInScope":true}`)))
		if rec.Code != http.StatusOK {
			t.Fatalf("待确认 assign: status = %d (%s)", rec.Code, rec.Body.String())
		}
		if len(exec.assignCalls) != 1 || exec.assignCalls[0].TextbookID != 5 ||
			exec.assignCalls[0].Lesson != "" || !exec.assignCalls[0].IncludeInScope {
			t.Fatalf("assign opts = %+v", exec.assignCalls[0])
		}

		// unassign passes through with the batch.
		rec = httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/textbooks/5/unassign",
			strings.NewReader(`{"cardIds":[1]}`)))
		if rec.Code != http.StatusOK || len(exec.unassignCalls) != 1 {
			t.Fatalf("unassign: status = %d, calls = %d", rec.Code, len(exec.unassignCalls))
		}
	})

	t.Run("GET /words carries the textbook attribution projection", func(t *testing.T) {
		cards := &fakeCardRepo{cards: []database.Card{
			{ID: 1, Word: "apple", Meaning: "苹果", Category: "english", Participates: true},
			{ID: 2, Word: "banana", Meaning: "香蕉", Category: "english", Participates: true},
		}}
		h, db := newTextbookTestHandler(t, &fakeExecutor{}, cards)
		ensureTextbookTables(t, db)
		seedTextbookRow(t, db, 4, "主教材", `["第1课"]`, 1)
		if _, err := db.Exec(
			`INSERT INTO card_assignments (card_id, textbook_id, lesson, include_in_scope) VALUES (1, 4, '第1课', 0)`,
		); err != nil {
			t.Fatalf("seed assignment: %v", err)
		}

		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/words", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d", rec.Code)
		}
		var resp struct {
			Words []struct {
				Word      string `json:"word"`
				Textbooks []struct {
					Name    string `json:"name"`
					Lesson  string `json:"lesson"`
				} `json:"textbooks"`
			} `json:"words"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if len(resp.Words) != 2 {
			t.Fatalf("words = %+v", resp.Words)
		}
		if w := resp.Words[0]; len(w.Textbooks) != 1 || w.Textbooks[0].Name != "主教材" || w.Textbooks[0].Lesson != "第1课" {
			t.Fatalf("apple attribution = %+v", w.Textbooks)
		}
		if w := resp.Words[1]; len(w.Textbooks) != 0 {
			t.Fatalf("banana attribution = %+v, want empty", w.Textbooks)
		}
	})
}
