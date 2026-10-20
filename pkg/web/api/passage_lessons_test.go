// passage_lessons_test.go — the Issue #14 parent records projection over
// REAL stores: a passage 背默 attempt row renders with its work identity
// (kind=passage, title/author, method 背默) AND the practiced range, the
// per-paragraph judgment counts, and the saved kind=passage receipt — a
// paragraph attempt and a whole attempt of the same passage stay two
// distinguishable rows.
package api

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/xiaotian/home-tutor-agent/pkg/infra"
	"github.com/xiaotian/home-tutor-agent/pkg/lesson"
	"github.com/xiaotian/home-tutor-agent/pkg/passage"
)

// seedPassageAttempt lands ONE completed passage attempt through the real
// stores: the passage + its practice range, the r0w- lesson row with
// per-paragraph manual verdicts, and a saved kind=passage receipt naming
// the work + range. Returns (lesson id, passage id, scope id).
func seedPassageAttempt(t *testing.T, db *sql.DB, lessons *lesson.Store, receipts *lesson.ReceiptStore, passageStore *passage.Store, lessonID string, scope passage.Scope, correct []bool) (string, int64, int64) {
	t.Helper()
	ctx := t.Context()

	paragraphs := []string{
		"秋天的雨，是一把钥匙。",
		"秋天的雨，有一盒五彩缤纷的颜料。",
		"秋天的雨，藏着非常好闻的气味。",
		"秋天的雨，吹起了金色的小喇叭。",
	}
	outcomes, err := passageStore.Commit(ctx, passage.CommitInput{
		Category: "chinese",
		Drafts:   []passage.Draft{{Title: "秋天的雨", Paragraphs: paragraphs, Scope: &scope}},
	})
	if err != nil {
		t.Fatalf("commit passage: %v", err)
	}
	if outcomes[0].ScopeID <= 0 {
		t.Fatalf("commit outcome = %+v, want the scope row landed", outcomes[0])
	}
	passageID, scopeID := outcomes[0].PassageID, outcomes[0].ScopeID

	texts := paragraphs
	if !scope.Whole {
		texts = nil
		for _, no := range scope.Paragraphs {
			texts = append(texts, paragraphs[no-1])
		}
	}
	if _, err = lessons.EnsurePassageLesson(ctx, lessonID, passageID, scopeID, scope.Whole, scope.Paragraphs, texts, "chinese"); err != nil {
		t.Fatalf("ensure passage lesson: %v", err)
	}
	exec, err := lesson.NewExecutor(lessons, infra.NewNoOpLogger())
	if err != nil {
		t.Fatalf("NewExecutor: %v", err)
	}
	enter, err := exec.EnterCheck(ctx, lessonID, 1, lessonID+"-enter", map[string]lesson.WordOutcome{})
	if err != nil {
		t.Fatalf("enter check: %v", err)
	}
	v := enter.NewVersion
	for i, correctOne := range correct {
		var res lesson.ApplyVerdictResult
		res, err = exec.ApplyVerdict(ctx, lessonID, v, fmt.Sprintf("%s-rv-%d", lessonID, i), lesson.CheckVerdict{WordIndex: i, Correct: correctOne, Manual: true})
		if err != nil {
			t.Fatalf("apply verdict %d: %v", i, err)
		}
		v = res.NewVersion
	}
	if _, err = exec.CompleteCheck(ctx, lessonID, v, lessonID+"-submit", false); err != nil {
		t.Fatalf("complete check: %v", err)
	}

	entry := "秋天的雨（整篇）"
	if !scope.Whole {
		entry = "秋天的雨（第 2、3 段）"
	}
	rec, err := receipts.Begin(ctx, lesson.ReceiptInput{
		LessonID: lessonID, RequestID: lessonID + "-submit",
		Kind: lesson.ReceiptKindPassage, Requested: []string{entry},
	})
	if err != nil {
		t.Fatalf("begin receipt: %v", err)
	}
	if err = receipts.Complete(ctx, rec.ID, []string{entry}, nil, true); err != nil {
		t.Fatalf("complete receipt: %v", err)
	}

	// The submit's OTHER write: the range practice record (what the content
	// view's per-scope state reads).
	practice, err := passage.NewPracticeStore(db, nil)
	if err != nil {
		t.Fatalf("practice store: %v", err)
	}
	correctCount := 0
	for _, c := range correct {
		if c {
			correctCount++
		}
	}
	if err = practice.RecordPractice(ctx, passage.PracticeRecord{
		PassageID: passageID, ScopeID: scopeID,
		Whole: scope.Whole, Paragraphs: scope.Paragraphs,
		LessonID: lessonID, TotalParagraphs: len(correct), CorrectParagraphs: correctCount,
		Mastered: correctCount == len(correct), ReviewedDay: "2026-10-19",
	}); err != nil {
		t.Fatalf("record practice: %v", err)
	}
	return lessonID, passageID, scopeID
}

// TestPassageLessonRecordsProjection — the list and detail carry the work,
// the ACTUAL range (第 2、3 段 vs 整篇 as two distinct rows of the SAME
// passage), the per-paragraph judgment, and the saved kind=passage receipt.
func TestPassageLessonRecordsProjection(t *testing.T) {
	db, lessons, _, receipts := newLessonTestStores(t)
	if err := passage.Migrate(t.Context(), db); err != nil {
		t.Fatalf("passage migrate: %v", err)
	}
	passageStore, err := passage.NewStore(db, infra.NewNoOpLogger())
	if err != nil {
		t.Fatalf("passage store: %v", err)
	}
	h := NewHandler(&fakeExecutor{}, &fakeCardRepo{}, nil, lessons, nil, receipts,
		nil, nil, nil, passageStore, infra.PythonConfig{}, infra.NewNoOpLogger())

	paragraphID, passageID, _ := seedPassageAttempt(t, db, lessons, receipts, passageStore,
		"r0w-20261019-080000", passage.Scope{Paragraphs: []int{2, 3}}, []bool{true, false})
	wholeID, samePassage, _ := seedPassageAttempt(t, db, lessons, receipts, passageStore,
		"r0w-20261019-090000", passage.Scope{Whole: true}, []bool{true, true, true, false})
	if passageID != samePassage {
		t.Fatalf("the two attempts landed on different passages: %d vs %d (同一份原文)", passageID, samePassage)
	}

	rows := getLessonsJSON(t, h)
	if len(rows) != 2 {
		t.Fatalf("lessons rows = %d, want 2 (one per range attempt)", len(rows))
	}
	byID := map[string]lessonListItem{}
	for _, row := range rows {
		byID[row.LessonID] = row
	}
	paragraphRow, ok := byID[paragraphID]
	if !ok {
		t.Fatalf("the paragraph attempt row is missing: %+v", rows)
	}
	if paragraphRow.Kind != "passage" || paragraphRow.Method != "背默" {
		t.Errorf("kind/method = %s/%s, want passage/背默", paragraphRow.Kind, paragraphRow.Method)
	}
	if paragraphRow.PassageTitle != "秋天的雨" || paragraphRow.PassageID != passageID {
		t.Errorf("passage identity = %q/%d, want 秋天的雨/%d", paragraphRow.PassageTitle, paragraphRow.PassageID, passageID)
	}
	if paragraphRow.PassageWhole || len(paragraphRow.PassageParagraphs) != 2 ||
		paragraphRow.PassageParagraphs[0] != 2 || paragraphRow.PassageParagraphs[1] != 3 {
		t.Errorf("paragraph row range = whole %v / %v, want [2 3]", paragraphRow.PassageWhole, paragraphRow.PassageParagraphs)
	}
	if paragraphRow.WordCount != 2 || paragraphRow.Correct != 1 || paragraphRow.Wrong != 1 || !paragraphRow.Completed {
		t.Errorf("paragraph judgment = %+v, want 2 paragraphs 1/1 completed", paragraphRow)
	}
	if paragraphRow.SaveStatus != lesson.ReceiptStatusSaved {
		t.Errorf("paragraph save_status = %s, want saved", paragraphRow.SaveStatus)
	}

	wholeRow := byID[wholeID]
	if !wholeRow.PassageWhole || len(wholeRow.PassageParagraphs) != 0 {
		t.Errorf("whole row range = whole %v / %v, want whole/empty", wholeRow.PassageWhole, wholeRow.PassageParagraphs)
	}
	if wholeRow.WordCount != 4 || wholeRow.Correct != 3 {
		t.Errorf("whole judgment = %+v, want 4 paragraphs 3 correct", wholeRow)
	}

	// The detail: the range's own paragraphs as the words array, the
	// manual verdicts, and the receipt naming the work + range verbatim.
	req := httptest.NewRequest(http.MethodGet, "/lessons/"+paragraphID, nil)
	res := httptest.NewRecorder()
	h.ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("GET /lessons/%s = %d: %s", paragraphID, res.Code, res.Body.String())
	}
	var detail lessonDetailResponse
	if err = json.Unmarshal(res.Body.Bytes(), &detail); err != nil {
		t.Fatalf("decode detail: %v", err)
	}
	if len(detail.Words) != 2 || detail.Words[0].Word != "秋天的雨，有一盒五彩缤纷的颜料。" {
		t.Errorf("detail words = %+v, want the range's own ordered paragraphs", detail.Words)
	}
	if detail.PassageTitle != "秋天的雨" || detail.PassageParagraphs[0] != 2 {
		t.Errorf("detail passage projection = %+v", detail.lessonListItem)
	}
	if len(detail.Results) != 2 || detail.Results[1].Correct {
		t.Errorf("detail results = %+v, want the 1/2 manual judgment", detail.Results)
	}
	if len(detail.SaveReceipts) != 1 || detail.SaveReceipts[0].Kind != lesson.ReceiptKindPassage {
		t.Fatalf("receipts = %+v, want one kind=passage receipt", detail.SaveReceipts)
	}
	if want := "秋天的雨（第 2、3 段）"; detail.SaveReceipts[0].Requested[0] != want {
		t.Errorf("receipt requested = %v, want %q", detail.SaveReceipts[0].Requested, want)
	}

	// The content view's scope rows: BOTH ranges of the same passage read
	// their own practice state (the parent sees them as independent
	// follow-up targets — 指定段落可独立安排后续练习).
	scopeReq := httptest.NewRequest(http.MethodGet, "/passages", nil)
	scopeRes := httptest.NewRecorder()
	h.ServeHTTP(scopeRes, scopeReq)
	if scopeRes.Code != http.StatusOK {
		t.Fatalf("GET /passages = %d: %s", scopeRes.Code, scopeRes.Body.String())
	}
	var body struct {
		Passages []struct {
			Scopes []struct {
				NextReviewDay string `json:"next_review_day"`
				Paragraphs    []int  `json:"paragraphs"`
				Whole         bool   `json:"whole"`
				HasPractice   bool   `json:"has_practice"`
				LastMastered  bool   `json:"last_mastered"`
			} `json:"scopes"`
			ID int64 `json:"id"`
		} `json:"passages"`
	}
	if err = json.Unmarshal(scopeRes.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode passages: %v", err)
	}
	if len(body.Passages) != 1 || body.Passages[0].ID != passageID {
		t.Fatalf("passages = %+v, want the ONE shared original (不复制课文)", body.Passages)
	}
	if len(body.Passages[0].Scopes) != 2 {
		t.Fatalf("scopes = %+v, want the paragraph set AND whole on one passage", body.Passages[0].Scopes)
	}
	for _, scope := range body.Passages[0].Scopes {
		if !scope.HasPractice || scope.NextReviewDay == "" {
			t.Errorf("scope %+v lacks its own practice state", scope)
		}
		if scope.Whole && scope.LastMastered {
			t.Error("the whole range reads mastered from a 3/4 attempt — ranges must not borrow mastery")
		}
	}
}
