package main

// Ticket 06 handler tests: the analysis_result frame (score, char-level
// errors, patterns, encouragement), batch_graded persistence, and the
// history-merged trends/repeat-words — all through the real Manager + FSM
// boot used by the Ticket-05 batch tests.

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/xiaotian/home-tutor-agent/pkg/database"
	"github.com/xiaotian/home-tutor-agent/pkg/grading"
)

// recordingRepo extends fsmRepo with capture of AppendEvent (persistence
// assertions) and a prefilled ListRecentResults (history feeding).
type recordingRepo struct {
	fsmRepo
	events  []database.SessionEvent
	results []database.SessionResult

	mu sync.Mutex
}

func (r *recordingRepo) AppendEvent(_ context.Context, e database.SessionEvent) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, e)
	return nil
}

func (r *recordingRepo) ListRecentResults(_ context.Context, limit int) ([]database.SessionResult, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if limit <= 0 || len(r.results) == 0 {
		return nil, nil
	}
	return r.results, nil
}

func (r *recordingRepo) appended() []database.SessionEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]database.SessionEvent, len(r.events))
	copy(out, r.events)
	return out
}

// readAnalysis reads frames until the analysis_result reply and decodes it.
func readAnalysis(t *testing.T, conn *websocket.Conn) analysisResultResponse {
	t.Helper()
	payload := readControlUntil(t, conn, "analysis_result")
	var a analysisResultResponse
	if err := json.Unmarshal([]byte(payload), &a); err != nil {
		t.Fatalf("analysis_result JSON decode: %v", err)
	}
	return a
}

// TestSubmitAnswers_AnalysisResult: the report frame follows batch_result —
// score, per-word errors carrying the char ops + hint, the near-miss flag,
// encouragement, and the current session as the sole trend point when no
// history exists.
func TestSubmitAnswers_AnalysisResult(t *testing.T) {
	fakeFSRS := &batchFSRS{}
	conn, manager, connID, teardown := bootBatchSession(t, fakeFSRS, &recordingRepo{}, &fsmGen{}, false)
	defer teardown()

	sendControlJSON(t, conn, `{"type":"submit_answers","params":{"answers":{"苹果":"苹果","香蕉":"香焦"}}}`)
	readControlUntil(t, conn, "batch_result")
	analysis := readAnalysis(t, conn)

	if analysis.Type != "analysis_result" {
		t.Errorf("type = %q", analysis.Type)
	}
	if analysis.Total != 2 || analysis.CorrectCount != 1 || analysis.Score != 50 {
		t.Errorf("counts = %d/%d score=%d, want 1/2 score=50", analysis.CorrectCount, analysis.Total, analysis.Score)
	}
	if len(analysis.Errors) != 1 || analysis.Errors[0].Word != "香蕉" {
		t.Fatalf("errors = %+v", analysis.Errors)
	}
	e := analysis.Errors[0]
	if !e.NearMiss || e.Heard != "香焦" {
		t.Errorf("error flags = %+v", e)
	}
	if len(e.Ops) != 2 || e.Ops[0].Kind != grading.OpMatch || e.Ops[1].Kind != grading.OpSub ||
		e.Ops[1].Expected != "蕉" || e.Ops[1].Heard != "焦" {
		t.Errorf("ops = %+v, want full alignment [match 香, sub 蕉→焦]", e.Ops)
	}
	if !strings.Contains(e.Hint, "只差一点") || !strings.Contains(e.Hint, "「蕉」写成了「焦」") {
		t.Errorf("hint = %q", e.Hint)
	}
	if !strings.Contains(analysis.Encouragement, "对了 1 个") {
		t.Errorf("encouragement = %q", analysis.Encouragement)
	}
	// No history: trends carry exactly the current session, dated today.
	if len(analysis.Trends) != 1 {
		t.Fatalf("trends = %+v, want exactly the current session", analysis.Trends)
	}
	if analysis.Trends[0].Score != 50 || analysis.Trends[0].Date != time.Now().Format("01-02") {
		t.Errorf("current trend = %+v", analysis.Trends[0])
	}
	if len(analysis.RepeatWords) != 0 {
		t.Errorf("repeat words = %+v, want none without history", analysis.RepeatWords)
	}

	// The report precedes the FSM completion frame.
	readControlUntil(t, conn, "session_complete")
	if s := manager.Lookup(connID); s == nil || s.State() != database.StateFeedback {
		t.Error("session did not reach FEEDBACK")
	}
}

// TestSubmitAnswers_PersistsGradedResult: one batch_graded event lands in the
// session repository carrying category, counts, and per-word answers — the
// parent-review / trends record.
func TestSubmitAnswers_PersistsGradedResult(t *testing.T) {
	fakeFSRS := &batchFSRS{}
	repo := &recordingRepo{}
	conn, _, _, teardown := bootBatchSession(t, fakeFSRS, repo, &fsmGen{}, false)
	defer teardown()

	sendControlJSON(t, conn, `{"type":"submit_answers","params":{"answers":{"苹果":"苹果","香蕉":"香焦"}}}`)
	readControlUntil(t, conn, "session_complete")

	events := repo.appended()
	var graded *database.SessionEvent
	for i := range events {
		if events[i].EventType == "batch_graded" {
			graded = &events[i]
		}
	}
	if graded == nil {
		t.Fatalf("no batch_graded event among %+v", events)
	}
	if graded.SessionID != 1 { // fsmRepo hands out audit row id 1
		t.Errorf("event session id = %d, want 1", graded.SessionID)
	}
	var stored storedGradedResult
	if err := json.Unmarshal([]byte(graded.Payload), &stored); err != nil {
		t.Fatalf("stored payload decode: %v", err)
	}
	if stored.Category != "chinese" || stored.CorrectCount != 1 || stored.Total != 2 {
		t.Errorf("stored = %+v", stored)
	}
	if len(stored.Results) != 2 || stored.Results[1].Word != "香蕉" || stored.Results[1].Heard != "香焦" || !stored.Results[1].NearMiss {
		t.Errorf("stored results = %+v", stored.Results)
	}
}

// TestSubmitAnswers_TrendsFromHistory: past graded sessions prepend to the
// trend list (oldest first, current last) and today's wrong words that were
// missed before come back flagged as repeats.
func TestSubmitAnswers_TrendsFromHistory(t *testing.T) {
	fakeFSRS := &batchFSRS{}
	repo := &recordingRepo{}
	// Newest first, as ListRecentResults returns them. 香蕉 was missed twice
	// before; 苹果 never.
	repo.results = []database.SessionResult{
		{SessionID: 3, CreatedAt: "2026-09-08 10:00:00", Payload: `{"category":"chinese","correct_count":0,"total":2,"results":[{"word":"香蕉","heard":"香焦","correct":false}]}`},
		{SessionID: 2, CreatedAt: "2026-09-07 10:00:00", Payload: `{"category":"chinese","correct_count":1,"total":2,"results":[{"word":"香蕉","heard":"","correct":false},{"word":"苹果","heard":"苹果","correct":true}]}`},
	}
	conn, _, _, teardown := bootBatchSession(t, fakeFSRS, repo, &fsmGen{}, false)
	defer teardown()

	sendControlJSON(t, conn, `{"type":"submit_answers","params":{"answers":{"苹果":"苹果","香蕉":"香焦"}}}`)
	readControlUntil(t, conn, "batch_result")
	analysis := readAnalysis(t, conn)

	// Trends: history (oldest first) + current last.
	if len(analysis.Trends) != 3 {
		t.Fatalf("trends = %+v, want 2 history + current", analysis.Trends)
	}
	if analysis.Trends[0].Date != "09-07" || analysis.Trends[1].Date != "09-08" {
		t.Errorf("history order = %+v %+v, want 09-07 then 09-08", analysis.Trends[0], analysis.Trends[1])
	}
	if analysis.Trends[0].Score != 50 || analysis.Trends[1].Score != 0 {
		t.Errorf("history scores = %+v %+v", analysis.Trends[0], analysis.Trends[1])
	}
	if last := analysis.Trends[2]; last.Date != time.Now().Format("01-02") || last.Score != 50 {
		t.Errorf("current trend = %+v, want last with score 50", last)
	}

	// Repeat misses: 香蕉 wrong today AND twice before; 苹果 correct today.
	if len(analysis.RepeatWords) != 1 || analysis.RepeatWords[0].Word != "香蕉" || analysis.RepeatWords[0].Times != 2 {
		t.Errorf("repeat words = %+v, want 香蕉×2", analysis.RepeatWords)
	}
}

// TestApplyHistory_SkipsUnparsable: a corrupt history payload never fails the
// report — the row is skipped.
func TestApplyHistory_SkipsUnparsable(t *testing.T) {
	report := grading.Analyze([]grading.WordInput{
		{Word: "香蕉", Heard: "香焦", Judgment: grading.GradeWord("香蕉", "香焦", "zh")},
	})
	analysis := buildAnalysisResponse("sess-1", report)
	applyHistory(analysis, []database.SessionResult{
		{SessionID: 9, CreatedAt: "2026-09-08 10:00:00", Payload: `not json`},
	}, "chinese")
	if len(analysis.Trends) != 1 {
		t.Errorf("trends = %+v, want only the current entry", analysis.Trends)
	}
	if len(analysis.RepeatWords) != 0 {
		t.Errorf("repeat words = %+v, want none from corrupt history", analysis.RepeatWords)
	}
}

// TestApplyHistory_CategoryFilter: English sessions stay out of a Chinese
// session's trend — mixed word sets would mislead.
func TestApplyHistory_CategoryFilter(t *testing.T) {
	report := grading.Analyze([]grading.WordInput{
		{Word: "香蕉", Heard: "香焦", Judgment: grading.GradeWord("香蕉", "香焦", "zh")},
	})
	analysis := buildAnalysisResponse("sess-1", report)
	applyHistory(analysis, []database.SessionResult{
		{SessionID: 8, CreatedAt: "2026-09-08 10:00:00", Payload: `{"category":"english","correct_count":5,"total":5,"results":[]}`},
		{SessionID: 7, CreatedAt: "2026-09-07 10:00:00", Payload: `{"category":"chinese","correct_count":1,"total":2,"results":[{"word":"香蕉","heard":"","correct":false}]}`},
	}, "chinese")
	if len(analysis.Trends) != 2 {
		t.Fatalf("trends = %+v, want the chinese row + current only", analysis.Trends)
	}
	if analysis.Trends[0].Date != "09-07" {
		t.Errorf("kept wrong history row: %+v", analysis.Trends[0])
	}
	// The english session's words never reach the repeat counts.
	if len(analysis.RepeatWords) != 1 || analysis.RepeatWords[0].Word != "香蕉" {
		t.Errorf("repeat words = %+v, want only 香蕉", analysis.RepeatWords)
	}
}

// TestSubmitReview_SpeaksFeedback: the per-word completion path reads its
// summary aloud too — submit_review (good/easy = correct) drives
// WAITING_PARENT → FEEDBACK and the encouragement reaches the TTS seam.
func TestSubmitReview_SpeaksFeedback(t *testing.T) {
	fakeFSRS := &batchFSRS{}
	gen := &recordingGen{}
	conn, _, _, teardown := bootBatchSession(t, fakeFSRS, &recordingRepo{}, gen, false)
	defer teardown()

	sendControlJSON(t, conn, `{"type":"submit_review","params":{"ratings":{"苹果":"good","香蕉":"again"},"category":"chinese"}}`)
	readControlUntil(t, conn, "session_complete")

	deadline := time.Now().Add(3 * time.Second)
	calls, text := gen.feedback()
	for time.Now().Before(deadline) && calls == 0 {
		time.Sleep(10 * time.Millisecond)
		calls, text = gen.feedback()
	}
	if calls != 1 {
		t.Fatalf("GenerateText calls = %d, want 1", calls)
	}
	// 1 of 2 correct → 50 → the keep-trying tier names the count.
	if !strings.Contains(text, "对了 1 个") {
		t.Errorf("spoken text = %q, want the count-naming encouragement", text)
	}
}

// shortDate / percent edge cases.
func TestShortDate(t *testing.T) {
	cases := map[string]string{
		"2026-09-08 10:00:00": "09-08",
		"2026-01-02":          "01-02",
		"garbage":             "garbage",
		"":                    "",
	}
	for in, want := range cases {
		if got := shortDate(in); got != want {
			t.Errorf("shortDate(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestPercent(t *testing.T) {
	if got := grading.Percent(0, 0); got != 0 {
		t.Errorf("Percent(0,0) = %d", got)
	}
	if got := grading.Percent(1, 2); got != 50 {
		t.Errorf("Percent(1,2) = %d", got)
	}
	if got := grading.Percent(2, 3); got != 67 {
		t.Errorf("Percent(2,3) = %d, want 67", got)
	}
	if !grading.RatingCountsAsCorrect("good") || !grading.RatingCountsAsCorrect("easy") {
		t.Error("good/easy must count as correct")
	}
	if grading.RatingCountsAsCorrect("hard") || grading.RatingCountsAsCorrect("again") {
		t.Error("hard/again must not count as correct")
	}
}
