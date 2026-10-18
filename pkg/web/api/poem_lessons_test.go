// poem_lessons_test.go — the Issue #12 parent records projection over REAL
// stores: a whole-poem 背默 attempt row renders with its work identity
// (kind=poem, title/author, method 背默), the per-LINE judgment counts, and
// the honest save status — the judgment and the save stay separately
// expressible, and word rows keep their existing shape untouched.
package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/xiaotian/home-tutor-agent/pkg/infra"
	"github.com/xiaotian/home-tutor-agent/pkg/lesson"
	"github.com/xiaotian/home-tutor-agent/pkg/poem"
)

// seedPoemLesson lands one COMPLETED poem attempt: the poem in the store,
// the r0p- lesson row with per-line manual verdicts (3 of 4 correct), and a
// saved kind=poem receipt. Returns the lesson id.
func seedPoemLesson(t *testing.T, h http.Handler, lessons *lesson.Store, receipts *lesson.ReceiptStore, poemStore *poem.Store) string {
	t.Helper()
	ctx := t.Context()

	outcomes, err := poemStore.Commit(ctx, poem.CommitInput{
		Category: "chinese",
		Drafts:   []poem.Draft{{Title: "静夜思", Author: "李白", Lines: []string{"床前明月光", "疑是地上霜", "举头望明月", "低头思故乡"}}},
	})
	if err != nil {
		t.Fatalf("commit poem: %v", err)
	}
	poemID := outcomes[0].PoemID

	const lessonID = "r0p-20261018-090000"
	if _, err = lessons.EnsurePoemLesson(ctx, lessonID, poemID, []string{"床前明月光", "疑是地上霜", "举头望明月", "低头思故乡"}); err != nil {
		t.Fatalf("ensure poem lesson: %v", err)
	}
	exec, err := lesson.NewExecutor(lessons, infra.NewNoOpLogger())
	if err != nil {
		t.Fatalf("NewExecutor: %v", err)
	}
	enter, err := exec.EnterCheck(ctx, lessonID, 1, lessonID+"-enter", map[string]lesson.WordOutcome{
		"1": {Outcome: "hint", Hints: 1},
	})
	if err != nil {
		t.Fatalf("enter check: %v", err)
	}
	v := enter.NewVersion
	for i, correct := range []bool{true, true, true, false} {
		var res lesson.ApplyVerdictResult
		res, err = exec.ApplyVerdict(ctx, lessonID, v, fmt.Sprintf("rv-%d", i), lesson.CheckVerdict{WordIndex: i, Correct: correct, Manual: true})
		if err != nil {
			t.Fatalf("apply verdict %d: %v", i, err)
		}
		v = res.NewVersion
	}
	if _, err = exec.CompleteCheck(ctx, lessonID, v, lessonID+"-submit", false); err != nil {
		t.Fatalf("complete check: %v", err)
	}

	rec, err := receipts.Begin(ctx, lesson.ReceiptInput{
		LessonID: lessonID, RequestID: lessonID + "-submit",
		Kind: lesson.ReceiptKindPoem, Requested: []string{"静夜思"},
	})
	if err != nil {
		t.Fatalf("begin receipt: %v", err)
	}
	if err = receipts.Complete(ctx, rec.ID, []string{"静夜思"}, nil, true); err != nil {
		t.Fatalf("complete receipt: %v", err)
	}
	return lessonID
}

// getLessonsJSON fetches and decodes GET /lessons.
func getLessonsJSON(t *testing.T, h http.Handler) []lessonListItem {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/lessons", nil)
	res := httptest.NewRecorder()
	h.ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("GET /lessons = %d: %s", res.Code, res.Body.String())
	}
	var body struct {
		Lessons []lessonListItem `json:"lessons"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode lessons: %v", err)
	}
	return body.Lessons
}

// TestPoemLessonRecordsProjection — the records list and detail distinguish
// the poem work, the practice method and the mastery result; the save
// status rides the receipt as for every other row. Driven through one temp
// DB shared by the poem store, the lesson stores and the handler (the real
// wiring shape).
func TestPoemLessonRecordsProjection(t *testing.T) {
	db, lessons, _, receipts := newLessonTestStores(t)
	if err := poem.Migrate(t.Context(), db); err != nil {
		t.Fatalf("poem migrate: %v", err)
	}
	poemStore, err := poem.NewStore(db, infra.NewNoOpLogger())
	if err != nil {
		t.Fatalf("poem store: %v", err)
	}
	h := NewHandler(&fakeExecutor{}, &fakeCardRepo{}, nil, lessons, nil, receipts,
		nil, nil, poemStore, infra.PythonConfig{}, infra.NewNoOpLogger())

	lessonID := seedPoemLesson(t, h, lessons, receipts, poemStore)

	rows := getLessonsJSON(t, h)
	if len(rows) != 1 {
		t.Fatalf("lessons rows = %d, want 1", len(rows))
	}
	row := rows[0]
	if row.Kind != "poem" || row.Method != "背默" {
		t.Errorf("kind/method = %s/%s, want poem/背默", row.Kind, row.Method)
	}
	if row.PoemTitle != "静夜思" || row.PoemAuthor != "李白" || row.PoemID == 0 {
		t.Errorf("poem identity = %q/%q/%d, want 静夜思/李白/non-zero", row.PoemTitle, row.PoemAuthor, row.PoemID)
	}
	if row.Category != "chinese" {
		t.Errorf("category = %s, want chinese", row.Category)
	}
	if row.WordCount != 4 || row.Correct != 3 || row.Wrong != 1 || !row.Completed {
		t.Errorf("judgment = %d lines %d correct %d wrong completed=%v, want 4/3/1/true", row.WordCount, row.Correct, row.Wrong, row.Completed)
	}
	if row.SaveStatus != lesson.ReceiptStatusSaved {
		t.Errorf("save_status = %s, want saved (the judgment and the save stay separate)", row.SaveStatus)
	}

	// The detail: the poem's lines as the words array, per-line manual
	// verdicts as the results array, and the receipt verbatim.
	req := httptest.NewRequest(http.MethodGet, "/lessons/"+lessonID, nil)
	res := httptest.NewRecorder()
	h.ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("GET /lessons/%s = %d: %s", lessonID, res.Code, res.Body.String())
	}
	var detail lessonDetailResponse
	if err := json.Unmarshal(res.Body.Bytes(), &detail); err != nil {
		t.Fatalf("decode detail: %v", err)
	}
	if detail.Kind != "poem" || detail.PoemTitle != "静夜思" || detail.Method != "背默" {
		t.Errorf("detail identity = %s/%q/%s, want poem/静夜思/背默", detail.Kind, detail.PoemTitle, detail.Method)
	}
	if len(detail.Words) != 4 || detail.Words[0].Word != "床前明月光" {
		t.Errorf("detail words = %+v, want the four ordered lines", detail.Words)
	}
	if len(detail.Results) != 4 || detail.Results[3].Correct || detail.Results[3].Manual == nil || !*detail.Results[3].Manual {
		t.Errorf("detail results = %+v, want 4 manual verdicts (line 4 wrong)", detail.Results)
	}
	if len(detail.SaveReceipts) != 1 || detail.SaveReceipts[0].Kind != lesson.ReceiptKindPoem {
		t.Errorf("detail receipts = %+v, want one kind=poem receipt", detail.SaveReceipts)
	}
	if want := []string{"静夜思"}; len(detail.SaveReceipts) == 1 && len(detail.SaveReceipts[0].Requested) != 1 {
		t.Errorf("receipt requested = %+v, want %v (the whole work)", detail.SaveReceipts[0].Requested, want)
	}
}

// TestWordLessonRowsKeepLegacyShape — the poem projection adds fields
// WITHOUT touching word rows: a plain r0- lesson renders with no poem keys.
func TestWordLessonRowsKeepLegacyShape(t *testing.T) {
	db, lessons, _, receipts := newLessonTestStores(t)
	if err := poem.Migrate(t.Context(), db); err != nil {
		t.Fatalf("poem migrate: %v", err)
	}
	poemStore, err := poem.NewStore(db, infra.NewNoOpLogger())
	if err != nil {
		t.Fatalf("poem store: %v", err)
	}
	h := NewHandler(&fakeExecutor{}, &fakeCardRepo{}, nil, lessons, nil, receipts,
		nil, nil, poemStore, infra.PythonConfig{}, infra.NewNoOpLogger())

	if _, err = lessons.EnsureLessonCards(t.Context(), "r0-20261018", []lesson.Word{{Word: "苹果"}}, "chinese"); err != nil {
		t.Fatalf("ensure word lesson: %v", err)
	}
	rows := getLessonsJSON(t, h)
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(rows))
	}
	if rows[0].Kind != "lesson" || rows[0].Method != "" || rows[0].PoemTitle != "" || rows[0].PoemID != 0 {
		t.Errorf("word row = %+v, want the legacy shape without poem fields", rows[0])
	}
}
