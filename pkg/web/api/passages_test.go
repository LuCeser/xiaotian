// passages_test.go — the Issue #13 /passages route tests: the commit's
// structured-original semantics over the REAL passage store (real SQLite
// temp DB, the suite's shared discipline — no store is replaced), the
// unified-content list with its ranges and scope verdict, the practice
// range selection routes, the explicit assign management path, and the
// gate validations (400/404/503).
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
	"github.com/xiaotian/home-tutor-agent/pkg/passage"
)

// newPassageTestHandler wires a handler with the REAL passage store and
// textbook repository over one temp DB (the textbooks tables created
// directly — Python owns them in production, tests create them like
// textbooks_test).
func newPassageTestHandler(t *testing.T) (http.Handler, *sql.DB) {
	t.Helper()
	db, lessons, practice, receipts := newLessonTestStores(t)
	if err := passage.Migrate(t.Context(), db); err != nil {
		t.Fatalf("passage migrate: %v", err)
	}
	passages, err := passage.NewStore(db, infra.NewNoOpLogger())
	if err != nil {
		t.Fatalf("passage.NewStore: %v", err)
	}
	ensureTextbookTables(t, db)
	textbooks, err := database.NewTextbookRepository(db, nil)
	if err != nil {
		t.Fatalf("NewTextbookRepository: %v", err)
	}
	h := NewHandler(&fakeExecutor{}, &fakeCardRepo{}, textbooks, lessons, practice, receipts,
		nil, nil, nil, passages, infra.PythonConfig{}, infra.NewNoOpLogger())
	return h, db
}

// postPassages posts one commit body and returns the recorder.
func postPassages(t *testing.T, h http.Handler, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/passages", strings.NewReader(body))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// getPassages fetches the list and decodes the envelope.
func getPassages(t *testing.T, h http.Handler, query string) passagesResponse {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/passages"+query, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /passages%s status = %d (body=%s)", query, rec.Code, rec.Body.String())
	}
	var resp passagesResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode GET /passages: %v", err)
	}
	return resp
}

// autumnRainBody is the shared commit fixture: five 自然段 plus a
// paragraph-2,3 range (老师布置的范围) and the page's 背诵 mark driving
// that default client-side.
const autumnRainBody = `{"category":"chinese","passages":[{"title":"秋天的雨","author":"","lesson":"第3课","paragraphs":["秋天的雨，是一把钥匙。","秋天的雨，有一盒五彩缤纷的颜料。","秋天的雨，藏着非常好闻的气味。","秋天的雨，吹起了金色的小喇叭。","秋天的雨，带给大地的是一曲丰收的歌。"],"scope":{"paragraphs":[2,3]}}]}`

// TestCommitPassagesWithScope — the commit lands the passage as ONE
// structured original with its ordered paragraphs and the chosen range;
// a repeat confirmation answers existing with the SAME ids (重复确认不
// 复制课文、不重复建立相同范围); the list carries both the practice
// range and the textbook-progress verdict (练习目标与教材进度可共同
// 查询).
func TestCommitPassagesWithScope(t *testing.T) {
	h, db := newPassageTestHandler(t)

	rec := postPassages(t, h, autumnRainBody)
	if rec.Code != http.StatusOK {
		t.Fatalf("commit status = %d (body=%s)", rec.Code, rec.Body.String())
	}
	var first passagesCommitResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &first); err != nil {
		t.Fatalf("decode commit: %v", err)
	}
	if first.Added != 1 || first.Existing != 0 || len(first.Results) != 1 {
		t.Fatalf("first commit = %+v, want one added", first)
	}
	r := first.Results[0]
	if r.Status != "added" || r.ScopeStatus != "added" || r.PassageID == 0 || r.ScopeID == 0 {
		t.Fatalf("first outcome = %+v, want added passage and scope", r)
	}

	rec = postPassages(t, h, autumnRainBody)
	if rec.Code != http.StatusOK {
		t.Fatalf("re-commit status = %d", rec.Code)
	}
	var second passagesCommitResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &second); err != nil {
		t.Fatalf("decode re-commit: %v", err)
	}
	if second.Added != 0 || second.Existing != 1 {
		t.Fatalf("re-commit = %+v, want existing", second)
	}
	if second.Results[0].PassageID != r.PassageID || second.Results[0].ScopeID != r.ScopeID ||
		second.Results[0].ScopeStatus != "existing" {
		t.Fatalf("re-commit = %+v, want the same passage and range rows", second.Results[0])
	}

	for _, table := range []string{"passages", "passage_scopes"} {
		var count int
		if err := db.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM "+table).Scan(&count); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		if count != 1 {
			t.Errorf("%s rows = %d, want 1", table, count)
		}
	}

	list := getPassages(t, h, "")
	if len(list.Passages) != 1 {
		t.Fatalf("list len = %d, want 1", len(list.Passages))
	}
	p := list.Passages[0]
	if p.Title != "秋天的雨" || len(p.Paragraphs) != 5 || p.Paragraphs[0] != "秋天的雨，是一把钥匙。" {
		t.Fatalf("listed passage = %+v, want the ordered paragraphs intact", p)
	}
	if len(p.Scopes) != 1 || p.Scopes[0].Whole || len(p.Scopes[0].Paragraphs) != 2 {
		t.Fatalf("scopes = %+v, want the 2,3 paragraph range", p.Scopes)
	}
	if !p.InScope || p.ScopeReason != "supplementary" {
		t.Errorf("scope = %v/%q, want supplementary in scope", p.InScope, p.ScopeReason)
	}
}

// TestCommitPassagesValidation — the gate rejects category/batch/field
// violations AND range violations (a range must be whole XOR paragraphs,
// its indexes inside the draft's own paragraphs) before anything writes.
func TestCommitPassagesValidation(t *testing.T) {
	h, _ := newPassageTestHandler(t)
	replace := func(old, new string) string {
		return strings.ReplaceAll(autumnRainBody, old, new)
	}
	cases := []struct {
		name string
		body string
		want string
	}{
		{"bad category", replace(`"category":"chinese"`, `"category":"math"`), "category"},
		{"empty batch", `{"category":"chinese","passages":[]}`, "passages"},
		{"empty title", replace(`"title":"秋天的雨"`, `"title":" "`), "title"},
		{"no paragraphs", replace(`["秋天的雨，是一把钥匙。","秋天的雨，有一盒五彩缤纷的颜料。","秋天的雨，藏着非常好闻的气味。","秋天的雨，吹起了金色的小喇叭。","秋天的雨，带给大地的是一曲丰收的歌。"]`, `[" ",""]`), "non-empty paragraph"},
		{"scope out of range", replace(`"paragraphs":[2,3]`, `"paragraphs":[2,6]`), "has 5 paragraphs"},
		{"scope index zero", replace(`"paragraphs":[2,3]`, `"paragraphs":[0]`), "1-based"},
		{"scope empty", replace(`"scope":{"paragraphs":[2,3]}`, `"scope":{}`), "at least one paragraph"},
		{"scope whole with paragraphs", replace(`"scope":{"paragraphs":[2,3]}`, `"scope":{"whole":true,"paragraphs":[2]}`), "must not carry paragraphs"},
		{"negative textbook", replace(`{"category":"chinese",`, `{"category":"chinese","textbook_id":-1,`), "negative"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := postPassages(t, h, tc.body)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d (body=%s), want 400", rec.Code, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), tc.want) {
				t.Fatalf("body = %s, want it to name %q", rec.Body.String(), tc.want)
			}
		})
	}
}

// TestPassageScopes — the range selection routes: adding the whole range
// to a passage that already carries a paragraph range keeps BOTH (以后
// 练习全文仍使用同一份原文); re-adding the same selection reports
// existing with the same id; an out-of-range set is a 400 naming the
// rule; removing one range leaves the passage, its paragraphs and the
// sibling range intact (范围可调整而不用重复导入).
func TestPassageScopes(t *testing.T) {
	h, db := newPassageTestHandler(t)
	rec := postPassages(t, h, autumnRainBody)
	if rec.Code != http.StatusOK {
		t.Fatalf("commit status = %d (body=%s)", rec.Code, rec.Body.String())
	}
	var committed passagesCommitResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &committed); err != nil {
		t.Fatalf("decode: %v", err)
	}
	passageID := committed.Results[0].PassageID
	base := "/passages/" + strconv.FormatInt(passageID, 10)

	post := func(body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, base+"/scopes", strings.NewReader(body))
		served := httptest.NewRecorder()
		h.ServeHTTP(served, req)
		return served
	}
	del := func(sid int64) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodDelete, base+"/scopes/"+strconv.FormatInt(sid, 10), nil)
		served := httptest.NewRecorder()
		h.ServeHTTP(served, req)
		return served
	}

	whole := post(`{"whole":true}`)
	if whole.Code != http.StatusOK {
		t.Fatalf("add whole status = %d (body=%s)", whole.Code, whole.Body.String())
	}
	var wholeResp passageScopeResponse
	if err := json.Unmarshal(whole.Body.Bytes(), &wholeResp); err != nil {
		t.Fatalf("decode whole: %v", err)
	}
	if wholeResp.Status != "added" || !wholeResp.Scope.Whole || wholeResp.Scope.ID == committed.Results[0].ScopeID {
		t.Fatalf("whole = %+v, want a NEW whole range", wholeResp)
	}

	if again := post(`{"paragraphs":[3,2,2]}`); again.Code != http.StatusOK {
		t.Fatalf("re-add status = %d", again.Code)
	} else {
		var resp passageScopeResponse
		if err := json.Unmarshal(again.Body.Bytes(), &resp); err != nil {
			t.Fatalf("decode re-add: %v", err)
		}
		if resp.Status != "existing" || resp.Scope.ID != committed.Results[0].ScopeID {
			t.Fatalf("re-add = %+v, want existing on the same range row", resp)
		}
	}

	if out := post(`{"paragraphs":[9]}`); out.Code != http.StatusBadRequest ||
		!strings.Contains(out.Body.String(), "out of range") {
		t.Fatalf("out-of-range = %d %s, want 400 out of range", out.Code, out.Body.String())
	}
	if missing := post(`{}`); missing.Code != http.StatusBadRequest {
		t.Fatalf("empty scope = %d, want 400", missing.Code)
	}

	req := httptest.NewRequest(http.MethodPost, "/passages/999/scopes", strings.NewReader(`{"whole":true}`))
	served := httptest.NewRecorder()
	h.ServeHTTP(served, req)
	if served.Code != http.StatusNotFound {
		t.Fatalf("missing passage = %d, want 404", served.Code)
	}

	if removed := del(committed.Results[0].ScopeID); removed.Code != http.StatusOK {
		t.Fatalf("remove status = %d (body=%s)", removed.Code, removed.Body.String())
	}
	if gone := del(committed.Results[0].ScopeID); gone.Code != http.StatusNotFound {
		t.Fatalf("remove again = %d, want 404", gone.Code)
	}

	list := getPassages(t, h, "")
	if len(list.Passages) != 1 || len(list.Passages[0].Paragraphs) != 5 {
		t.Fatalf("passage and its paragraphs must survive a range removal: %+v", list.Passages)
	}
	if len(list.Passages[0].Scopes) != 1 || !list.Passages[0].Scopes[0].Whole {
		t.Fatalf("only the removed range goes, the whole range stays: %+v", list.Passages[0].Scopes)
	}
	var count int
	if err := db.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM passages").Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 1 {
		t.Errorf("passages rows = %d, want 1 (ranges never copy the text)", count)
	}
}

// TestAssignPassage — the explicit attribution management: a missing
// passage 404s, a missing textbook 400s, a real assignment lands with the
// lesson and the include refresh.
func TestAssignPassage(t *testing.T) {
	h, db := newPassageTestHandler(t)
	if rec := postPassages(t, h, autumnRainBody); rec.Code != http.StatusOK {
		t.Fatalf("commit status = %d", rec.Code)
	}
	list := getPassages(t, h, "")
	passageID := list.Passages[0].ID
	seedTextbookRow(t, db, 7, "统编语文", `["第1课","第3课","第9课"]`, 1)

	post := func(id int64, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/passages/"+strconv.FormatInt(id, 10)+"/assign", strings.NewReader(body))
		served := httptest.NewRecorder()
		h.ServeHTTP(served, req)
		return served
	}
	if rec := post(999, `{"textbook_id":7,"lesson":"第3课","includeInScope":false}`); rec.Code != http.StatusNotFound {
		t.Fatalf("missing passage = %d, want 404", rec.Code)
	}
	if rec := post(passageID, `{"textbook_id":42,"lesson":"第3课"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("missing textbook = %d, want 400", rec.Code)
	}
	if rec := post(passageID, `{"textbook_id":7,"lesson":"第9课","includeInScope":true}`); rec.Code != http.StatusOK {
		t.Fatalf("assign status = %d (body=%s)", rec.Code, rec.Body.String())
	}

	list = getPassages(t, h, "")
	p := list.Passages[0]
	if len(p.Textbooks) != 1 || p.Textbooks[0].TextbookName != "统编语文" || p.Textbooks[0].Lesson != "第9课" {
		t.Fatalf("attribution = %+v", p.Textbooks)
	}
	if !p.InScope || p.ScopeReason != "included" {
		t.Fatalf("verdict = %v/%q, want the explicit include", p.InScope, p.ScopeReason)
	}

	// Withdrawing the explicit include leaves 第9课 beyond the learned
	// prefix (第1课): stored fine, out of scope until the progress moves.
	if rec := post(passageID, `{"textbook_id":7,"lesson":"第9课","includeInScope":false}`); rec.Code != http.StatusOK {
		t.Fatalf("withdraw status = %d", rec.Code)
	}
	list = getPassages(t, h, "")
	if list.Passages[0].InScope || list.Passages[0].ScopeReason != "progress_pending" {
		t.Fatalf("verdict = %v/%q, want progress-pending", list.Passages[0].InScope, list.Passages[0].ScopeReason)
	}
}

// TestListPassagesSubjectFilter — category narrows the projection; the
// structured representation is not subject-bound (an English short text
// stores under "english" and stays out of the 语文 view).
func TestListPassagesSubjectFilter(t *testing.T) {
	h, _ := newPassageTestHandler(t)
	if rec := postPassages(t, h, autumnRainBody); rec.Code != http.StatusOK {
		t.Fatalf("commit chinese: %d", rec.Code)
	}
	english := `{"category":"english","passages":[{"title":"The Cat","paragraphs":["The cat sat on the mat.","It looked at the dog."]}]}`
	if rec := postPassages(t, h, english); rec.Code != http.StatusOK {
		t.Fatalf("commit english: %d (body=%s)", rec.Code, rec.Body.String())
	}

	all := getPassages(t, h, "")
	if len(all.Passages) != 2 {
		t.Fatalf("all len = %d, want 2", len(all.Passages))
	}
	zh := getPassages(t, h, "?category=chinese")
	if len(zh.Passages) != 1 || zh.Passages[0].Title != "秋天的雨" {
		t.Fatalf("chinese filter = %+v", zh.Passages)
	}
	en := getPassages(t, h, "?category=english")
	if len(en.Passages) != 1 || en.Passages[0].Category != "english" {
		t.Fatalf("english filter = %+v", en.Passages)
	}
	if rec := httptest.NewRequest(http.MethodGet, "/passages?category=math", nil); true {
		served := httptest.NewRecorder()
		h.ServeHTTP(served, rec)
		if served.Code != http.StatusBadRequest {
			t.Fatalf("bad category = %d, want 400", served.Code)
		}
	}
}

// TestPassagesUnavailableDegradedBoot — a handler wired without the
// passage store keeps every route mounted and answers 503 honestly (the
// poems precedent): the parent UI explains itself instead of hitting a
// 404.
func TestPassagesUnavailableDegradedBoot(t *testing.T) {
	_, lessons, practice, receipts := newLessonTestStores(t)
	h := NewHandler(&fakeExecutor{}, &fakeCardRepo{}, nil, lessons, practice, receipts,
		nil, nil, nil, nil, infra.PythonConfig{}, infra.NewNoOpLogger())

	for _, tc := range []struct{ method, path string }{
		{http.MethodPost, "/passages"},
		{http.MethodGet, "/passages"},
		{http.MethodPost, "/passages/1/assign"},
		{http.MethodPost, "/passages/1/scopes"},
		{http.MethodDelete, "/passages/1/scopes/1"},
	} {
		req := httptest.NewRequest(tc.method, tc.path, nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("%s %s = %d, want 503", tc.method, tc.path, rec.Code)
		}
		if !strings.Contains(rec.Body.String(), codePassagesUnavailable) {
			t.Fatalf("%s %s body = %s, want %s", tc.method, tc.path, rec.Body.String(), codePassagesUnavailable)
		}
	}
}
