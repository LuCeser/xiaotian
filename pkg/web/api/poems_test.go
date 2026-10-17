// poems_test.go — the Issue #11 /poems route tests: the commit's whole-work
// semantics over the REAL poem store (real SQLite temp DB, the suite's
// shared discipline — no store is replaced), the unified-content list with
// its subject filter and scope verdict, the explicit assign management
// path, and the gate validations (400/404/503).
package api

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/xiaotian/home-tutor-agent/pkg/database"
	"github.com/xiaotian/home-tutor-agent/pkg/infra"
	"github.com/xiaotian/home-tutor-agent/pkg/poem"
)

// newPoemTestHandler wires a handler with the REAL poem store and textbook
// repository over one temp DB (the textbooks tables created directly —
// Python owns them in production, tests create them like textbooks_test).
func newPoemTestHandler(t *testing.T) (http.Handler, *sql.DB) {
	t.Helper()
	db, lessons, practice, receipts := newLessonTestStores(t)
	if err := poem.Migrate(t.Context(), db); err != nil {
		t.Fatalf("poem migrate: %v", err)
	}
	poems, err := poem.NewStore(db, infra.NewNoOpLogger())
	if err != nil {
		t.Fatalf("poem.NewStore: %v", err)
	}
	ensureTextbookTables(t, db)
	textbooks, err := database.NewTextbookRepository(db, nil)
	if err != nil {
		t.Fatalf("NewTextbookRepository: %v", err)
	}
	h := NewHandler(&fakeExecutor{}, &fakeCardRepo{}, textbooks, lessons, practice, receipts,
		nil, nil, poems, infra.PythonConfig{}, infra.NewNoOpLogger())
	return h, db
}

// postPoems posts one commit body and returns the recorder.
func postPoems(t *testing.T, h http.Handler, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/poems", strings.NewReader(body))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// getPoems fetches the list and decodes the envelope.
func getPoems(t *testing.T, h http.Handler, query string) poemsResponse {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/poems"+query, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /poems%s status = %d (body=%s)", query, rec.Code, rec.Body.String())
	}
	var resp poemsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode GET /poems: %v", err)
	}
	return resp
}

const jingYeSi = `{"category":"chinese","poems":[{"title":"静夜思","author":"李白","lines":["床前明月光，疑是地上霜。","举头望明月，低头思故乡。"],"lesson":"第8课"}]}`

// TestCommitPoemsWholeWork — the commit lands the poem as ONE work with
// its ordered lines, reports per-poem outcomes, and a repeat confirmation
// answers existing with the SAME id (重复确认不复制作品 — the store's
// fingerprint rule observed through the real HTTP surface).
func TestCommitPoemsWholeWork(t *testing.T) {
	h, db := newPoemTestHandler(t)

	rec := postPoems(t, h, jingYeSi)
	if rec.Code != http.StatusOK {
		t.Fatalf("commit status = %d (body=%s)", rec.Code, rec.Body.String())
	}
	var first poemsCommitResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &first); err != nil {
		t.Fatalf("decode commit: %v", err)
	}
	if first.Added != 1 || first.Existing != 0 || len(first.Results) != 1 {
		t.Fatalf("first commit = %+v, want one added", first)
	}
	storedID := first.Results[0].PoemID

	// The same confirmation again: existing, same row, no copy.
	rec = postPoems(t, h, jingYeSi)
	if rec.Code != http.StatusOK {
		t.Fatalf("re-commit status = %d", rec.Code)
	}
	var second poemsCommitResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &second); err != nil {
		t.Fatalf("decode re-commit: %v", err)
	}
	if second.Added != 0 || second.Existing != 1 || second.Results[0].PoemID != storedID {
		t.Fatalf("re-commit = %+v, want existing with id %d", second, storedID)
	}

	var count int
	if err := db.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM poems").Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 1 {
		t.Errorf("poems rows = %d, want 1", count)
	}

	// The list carries the whole work: ordered lines, never a bag of words.
	list := getPoems(t, h, "")
	if len(list.Poems) != 1 {
		t.Fatalf("list len = %d, want 1", len(list.Poems))
	}
	p := list.Poems[0]
	if p.Title != "静夜思" || p.Author != "李白" || len(p.Lines) != 2 ||
		p.Lines[0] != "床前明月光，疑是地上霜。" || p.Lines[1] != "举头望明月，低头思故乡。" {
		t.Errorf("listed poem = %+v, want the whole ordered work", p)
	}
	if !p.InScope || p.ScopeReason != "supplementary" {
		t.Errorf("scope = %v/%q, want supplementary in scope", p.InScope, p.ScopeReason)
	}
}

// TestCommitPoemsWithTextbook — the attribution target rides the commit:
// the poem gains a textbook chip with its lesson, and the scope verdict
// follows the Issue #4 progress rules (a lesson beyond the learned prefix
// stores fine but stays out of scope until the progress moves).
func TestCommitPoemsWithTextbook(t *testing.T) {
	h, db := newPoemTestHandler(t)
	seedTextbookRow(t, db, 7, "统编语文", `["第1课","第8课","第9课"]`, 1)

	body := strings.Replace(jingYeSi, `"category":"chinese"`, `"category":"chinese","textbook_id":7`, 1)
	if rec := postPoems(t, h, body); rec.Code != http.StatusOK {
		t.Fatalf("commit status = %d (body=%s)", rec.Code, rec.Body.String())
	}

	list := getPoems(t, h, "?category=chinese")
	if len(list.Poems) != 1 {
		t.Fatalf("list len = %d, want 1", len(list.Poems))
	}
	p := list.Poems[0]
	if len(p.Textbooks) != 1 || p.Textbooks[0].TextbookName != "统编语文" || p.Textbooks[0].Lesson != "第8课" {
		t.Fatalf("textbooks = %+v, want 统编语文·第8课", p.Textbooks)
	}
	if p.InScope || p.ScopeReason != "progress_pending" {
		t.Errorf("scope = %v/%q, want progress-pending (第8课 beyond learned=1)", p.InScope, p.ScopeReason)
	}

	// The progress moves to include 第8课 — the poem enters the scope with
	// no re-import (范围随进度生效, verified through the read surface).
	if _, err := db.ExecContext(t.Context(), "UPDATE textbooks SET learned_lessons = 2 WHERE id = 7"); err != nil {
		t.Fatalf("advance progress: %v", err)
	}
	list = getPoems(t, h, "?category=chinese")
	if !list.Poems[0].InScope || list.Poems[0].ScopeReason != "learned" {
		t.Errorf("scope after progress = %v/%q, want learned", list.Poems[0].InScope, list.Poems[0].ScopeReason)
	}
}

// TestAssignPoem — the content view's management action refreshes the
// lesson AND the include flag together; an unknown poem is an honest 404
// and an unknown textbook a 400.
func TestAssignPoem(t *testing.T) {
	h, db := newPoemTestHandler(t)
	seedTextbookRow(t, db, 7, "统编语文", `["第1课","第8课"]`, 0)
	if rec := postPoems(t, h, jingYeSi); rec.Code != http.StatusOK {
		t.Fatalf("commit status = %d", rec.Code)
	}
	list := getPoems(t, h, "")
	poemID := list.Poems[0].ID

	assign := func(body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/poems/"+strconv.FormatInt(poemID, 10)+"/assign",
			strings.NewReader(body))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}

	if rec := assign(`{"textbook_id":7,"lesson":"第8课","includeInScope":true}`); rec.Code != http.StatusOK {
		t.Fatalf("assign status = %d (body=%s)", rec.Code, rec.Body.String())
	}
	list = getPoems(t, h, "")
	if len(list.Poems[0].Textbooks) != 1 || !list.Poems[0].Textbooks[0].IncludeInScope ||
		!list.Poems[0].InScope || list.Poems[0].ScopeReason != "included" {
		t.Fatalf("after assign = %+v, want included via explicit include", list.Poems[0])
	}

	// Unknown poem: 404 not_found.
	req := httptest.NewRequest(http.MethodPost, "/poems/999/assign",
		strings.NewReader(`{"textbook_id":7,"lesson":"","includeInScope":false}`))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("assign unknown poem status = %d, want 404", rec.Code)
	}

	// Unknown textbook: 400 invalid_request BEFORE anything writes.
	if rec := assign(`{"textbook_id":404,"lesson":"","includeInScope":false}`); rec.Code != http.StatusBadRequest {
		t.Errorf("assign unknown textbook status = %d, want 400", rec.Code)
	}
}

// TestCommitPoemsValidation — the gate rejects what the store must never
// see: an off-enum category, an empty or over-cap batch, a poem without a
// title or without any non-empty line, and a nonexistent textbook target.
func TestCommitPoemsValidation(t *testing.T) {
	h, db := newPoemTestHandler(t)

	for _, tc := range []struct {
		name string
		body string
	}{
		{"off-enum category", `{"category":"math","poems":[{"title":"咏鹅","lines":["鹅"]}]}`},
		{"empty batch", `{"category":"chinese","poems":[]}`},
		{"over-cap batch", `{"category":"chinese","poems":[` + repeatPoems(21) + `]}`},
		{"missing title", `{"category":"chinese","poems":[{"title":"","lines":["一行"]}]}`},
		{"all-empty lines", `{"category":"chinese","poems":[{"title":"空","lines":["","  "]}]}`},
		{"missing lines", `{"category":"chinese","poems":[{"title":"空"}]}`},
		{"nonexistent textbook", `{"category":"chinese","textbook_id":99,"poems":[{"title":"咏鹅","lines":["鹅"]}]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if rec := postPoems(t, h, tc.body); rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400 (body=%s)", rec.Code, rec.Body.String())
			}
		})
	}

	// Nothing wrote: the rejected batches never reached the store.
	var count int
	if err := db.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM poems").Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 0 {
		t.Errorf("poems rows = %d, want 0 (rejections write nothing)", count)
	}
}

// TestListPoemsSubjectFilter — the unified content view's subject filter:
// poems are chinese-category content, so ?category=english lists none.
func TestListPoemsSubjectFilter(t *testing.T) {
	h, _ := newPoemTestHandler(t)
	if rec := postPoems(t, h, jingYeSi); rec.Code != http.StatusOK {
		t.Fatalf("commit status = %d", rec.Code)
	}
	if got := getPoems(t, h, "?category=english"); len(got.Poems) != 0 {
		t.Errorf("?category=english = %d poems, want 0", len(got.Poems))
	}
	if got := getPoems(t, h, "?category=chinese"); len(got.Poems) != 1 {
		t.Errorf("?category=chinese = %d poems, want 1", len(got.Poems))
	}
	// An off-enum category is a 400, never a silent all.
	req := httptest.NewRequest(http.MethodGet, "/poems?category=fr", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("off-enum category status = %d, want 400", rec.Code)
	}
}

// TestPoemsUnavailableDegradedBoot — a handler wired without the poem store
// mounts the routes but answers 503 poems_unavailable (the recognizer's
// RT-10 transparency precedent: the endpoint explains itself instead of
// 404ing).
func TestPoemsUnavailableDegradedBoot(t *testing.T) {
	h := newTestHandler(t, &fakeExecutor{}, &fakeCardRepo{})
	for _, tc := range []struct {
		name string
		do   func()
	}{
		{"list", func() {
			req := httptest.NewRequest(http.MethodGet, "/poems", nil)
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), codePoemsUnavailable) {
				t.Errorf("GET /poems = %d/%s, want 503 %s", rec.Code, rec.Body.String(), codePoemsUnavailable)
			}
		}},
		{"commit", func() {
			rec := postPoems(t, h, jingYeSi)
			if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), codePoemsUnavailable) {
				t.Errorf("POST /poems = %d/%s, want 503 %s", rec.Code, rec.Body.String(), codePoemsUnavailable)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) { tc.do() })
	}
}

// repeatPoems builds n minimal valid poem objects (the over-cap probe).
func repeatPoems(n int) string {
	parts := make([]string, 0, n)
	for i := 0; i < n; i++ {
		parts = append(parts, `{"title":"诗`+strconv.Itoa(i)+`","lines":["一行"]}`)
	}
	return strings.Join(parts, ",")
}
