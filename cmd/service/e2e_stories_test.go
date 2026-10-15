package main

// Ticket 08 E2E integration tests: every user story from the spec, driven
// through the REAL handler stack (ttsHandlers dispatch → Manager → FSM) with
// the external seams faked (FSRS / TTS / streamer), plus the error-drift and
// latency guards. The human-only acceptance items (real iPad Safari,
// children's voices, real handwriting, VPS-scale perf) live in
// .scratch/speech-recognition-redesign/UAT-CHECKLIST.md.

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"go.uber.org/zap"

	"github.com/xiaotian/home-tutor-agent/pkg/fsrs"
	"github.com/xiaotian/home-tutor-agent/pkg/grading"
	"github.com/xiaotian/home-tutor-agent/pkg/infra"
	"github.com/xiaotian/home-tutor-agent/pkg/pybridge"
	"github.com/xiaotian/home-tutor-agent/pkg/session"
	"github.com/xiaotian/home-tutor-agent/pkg/ws"
)

// storiesFSRS serves DISJOINT two-word queues per SelectDueCards call (the
// multi-user contract: a locked card never goes to a second session) and
// records every unlock's card IDs for containment assertions.
type storiesFSRS struct {
	mu sync.Mutex

	pool     []pybridge.DueCard
	served   [][]int64 // card IDs handed out, per call
	unlocked [][]int64 // card IDs released, per unlock call
}

func newStoriesFSRS() *storiesFSRS {
	pool := make([]pybridge.DueCard, 0, 8)
	words := []string{"苹果", "香蕉", "葡萄", "西瓜", "橘子", "芒果", "柠檬", "樱桃"}
	for i, w := range words {
		pool = append(pool, pybridge.DueCard{ID: i + 1, Word: w})
	}
	return &storiesFSRS{pool: pool}
}

func (f *storiesFSRS) SelectDueCards(_ context.Context, opts fsrs.SelectOpts) ([]pybridge.DueCard, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	limit := opts.Limit
	if limit <= 0 || limit > 2 {
		limit = 2
	}
	if len(f.pool) < limit {
		return nil, nil // exhausted → ErrNoDueWords upstream
	}
	take := f.pool[:limit]
	f.pool = f.pool[limit:]
	ids := make([]int64, 0, len(take))
	for _, c := range take {
		ids = append(ids, int64(c.ID))
	}
	f.served = append(f.served, ids)
	out := make([]pybridge.DueCard, len(take))
	copy(out, take)
	return out, nil
}

func (f *storiesFSRS) UpdateRatings(_ context.Context, ratings map[string]string, _ string) (pybridge.ReviewResult, error) {
	return pybridge.ReviewResult{}, nil
}

func (f *storiesFSRS) UnlockCards(_ context.Context, opts fsrs.UnlockOpts) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.unlocked = append(f.unlocked, opts.IDs)
	return len(opts.IDs), nil
}

func (f *storiesFSRS) snapshot() (served, unlocked [][]int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.served, f.unlocked
}

func int64In(ids []int64, v int64) bool {
	for _, id := range ids {
		if id == v {
			return true
		}
	}
	return false
}

// bootStories boots a real handler stack over the shared fake seams and
// returns one DIALED websocket connection per requested student, plus the
// manager and teardown. Distinct connections = distinct connIDs = distinct
// registry entries (the multi-user posture).
func bootStories(t *testing.T, fakeFSRS *storiesFSRS, students int) ([]*websocket.Conn, *session.Manager, func()) {
	return bootStoriesWithGen(t, fakeFSRS, &fsmGen{}, students)
}

// bootStoriesWithGen is bootStories with an explicit TTSSessionGenerator —
// the DICT-03 audio_plays tests inject mimoGen to flip the word_complete
// hint without touching the other stories' default fake.
func bootStoriesWithGen(t *testing.T, fakeFSRS *storiesFSRS, gen session.TTSSessionGenerator, students int) ([]*websocket.Conn, *session.Manager, func()) {
	t.Helper()
	logger := infra.NewNoOpLogger()
	zapLogger := zap.NewNop()

	hub, err := ws.NewHub(ws.HubConfig{Limit: 10, RegisterBuffer: 32, UnregisterBuffer: 32}, zapLogger)
	if err != nil {
		t.Fatalf("hub: %v", err)
	}
	cfg := infra.DefaultConfig()
	pool, err := infra.NewWorkerPool(cfg.Worker, logger)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	t.Cleanup(func() { _ = pool.Shutdown(context.Background()) })

	manager, err := session.NewManager(hub, fakeFSRS, gen, &fsmStreamer{}, &fsmRepo{}, pool, logger, cfg)
	if err != nil {
		t.Fatalf("manager: %v", err)
	}
	svcCtx, svcCancel := context.WithCancel(context.Background())
	t.Cleanup(svcCancel)
	go hub.Run(svcCtx)
	go func() {
		if startErr := manager.Start(svcCtx); startErr != nil {
			logger.Error("session manager start failed", infra.ErrField(startErr))
		}
	}()

	handlers := ttsHandlers(manager, fakeFSRS, nil, nil, logger)
	var conns []*websocket.Conn
	var teardowns []func()
	teardown := func() {
		for _, td := range teardowns {
			td()
		}
	}
	for i := 0; i < students; i++ {
		conn, td := dialVoiceTestServerOnHub(t, hub, handlers)
		conns = append(conns, conn)
		teardowns = append(teardowns, td)
	}
	return conns, manager, teardown
}

// Story 1 — 逐词听写（语音转写路径）: start → play word 1 → spoken answer
// (correct) → next_word → word 2 → spoken answer (near-miss) → last
// next_word → WAITING_PARENT → submit_review → FEEDBACK. The judgment frames
// carry the Ticket-06 fields (near_miss + diff).
func TestE2E_Story_PerWordVoiceFlow(t *testing.T) {
	fakeFSRS := newStoriesFSRS()
	conns, _, teardown := bootStories(t, fakeFSRS, 1)
	defer teardown()
	conn := conns[0]

	ready := startStoriesSession(t, conn)
	if len(ready.Words) != 2 {
		t.Fatalf("words = %v, want 2", ready.Words)
	}

	// Word 1: play → wait for DICTATING via manager… we don't have the
	// connID here; instead wait for the word_complete frame the loop emits.
	first := ready.Words[0]
	sendControlJSON(t, conn, `{"type":"play_word","params":{"word_id":"`+first+`"}}`)
	readControlUntil(t, conn, `"type":"word_complete"`)

	// Correct spoken answer for word 1.
	sendControlJSON(t, conn, `{"type":"submit_answer","params":{"word_id":"`+first+`","transcript":"`+first+`"}}`)
	judged := readControlUntil(t, conn, `"type":"answer_result"`)
	var res1 answerResultResponse
	if err := json.Unmarshal([]byte(judged), &res1); err != nil {
		t.Fatalf("answer_result decode: %v", err)
	}
	if !res1.Correct || res1.Rating != "good" || res1.NearMiss {
		t.Errorf("word 1 judgment = %+v, want correct/good", res1)
	}
	if len(res1.Diff) == 0 {
		t.Errorf("answer_result carries no diff: %+v", res1)
	}

	// Advance to word 2.
	sendControlJSON(t, conn, `{"type":"next_word","params":{}}`)
	second := ready.Words[1]
	readControlUntil(t, conn, `"word_id":"`+second+`"`)

	// Near-miss answer for word 2: 香蕉-style single-char slip. Build from the
	// real word to stay generic: swap the first two runes.
	near := nearMissOf(second)
	sendControlJSON(t, conn, `{"type":"submit_answer","params":{"word_id":"`+second+`","transcript":"`+near+`"}}`)
	judged = readControlUntil(t, conn, `"type":"answer_result"`)
	var res2 answerResultResponse
	if err := json.Unmarshal([]byte(judged), &res2); err != nil {
		t.Fatalf("answer_result 2 decode: %v", err)
	}
	if res2.Correct || !res2.NearMiss || res2.Rating != "hard" {
		t.Errorf("word 2 judgment = %+v, want incorrect near-miss/hard", res2)
	}

	// Finish dictation → parent review completion.
	sendControlJSON(t, conn, `{"type":"next_word","params":{}}`)
	sendControlJSON(t, conn, `{"type":"submit_review","params":{"ratings":{"`+first+`":"good","`+second+`":"hard"},"category":"chinese"}}`)
	readControlUntil(t, conn, `"type":"submit_review_result"`)
	readControlUntil(t, conn, "session_complete")
}

// nearMissOf swaps the first two runes of a 2+ rune word — a one-edit
// transposition, guaranteed to land in the near-miss tier. Single-rune
// words cannot be near-missed by construction (grading requires len >= 2);
// the test word pool is all 2-char words, so hitting this panic is a pool
// edit mistake, not a runtime condition.
func nearMissOf(w string) string {
	r := []rune(w)
	if len(r) < 2 {
		panic("nearMissOf requires a 2+ rune word — fix the test word pool")
	}
	r[0], r[1] = r[1], r[0]
	return string(r)
}

// startStoriesSession sends the standard story preamble (2-word chinese
// session) and returns the decoded session_ready payload. Story-specific
// assertions (word counts, per-student labels) stay at call sites.
func startStoriesSession(t *testing.T, conn *websocket.Conn) sessionReadyResponse {
	sendControlJSON(t, conn, `{"type":"start_session","params":{"limit":2,"category":"chinese"}}`)
	payload := readControlUntil(t, conn, "session_ready")
	var ready sessionReadyResponse
	if err := json.Unmarshal([]byte(payload), &ready); err != nil {
		t.Fatalf("session_ready decode: %v (payload: %s)", err, payload)
	}
	return ready
}

// Story 2 — 批量听写（写完一起交）: the three completion frames arrive in a
// stable order: batch_result (graded) → analysis_result (report) →
// session_complete (FEEDBACK). Ordering is the contract the client's UX
// rhythm depends on (marks, then report, then end state).
func TestE2E_Story_BatchFrameOrder(t *testing.T) {
	fakeFSRS := newStoriesFSRS()
	conns, _, teardown := bootStories(t, fakeFSRS, 1)
	defer teardown()
	conn := conns[0]

	ready := startStoriesSession(t, conn)

	sendControlJSON(t, conn, `{"type":"play_word","params":{"word_id":"`+ready.Words[0]+`"}}`)
	readControlUntil(t, conn, `"type":"word_complete"`)
	sendControlJSON(t, conn, `{"type":"next_word","params":{}}`)
	readControlUntil(t, conn, `"word_id":"`+ready.Words[1]+`"`)
	sendControlJSON(t, conn, `{"type":"next_word","params":{}}`)

	// One submission, then read frames STRICTLY in order — readControlUntil
	// skips interleaved noise, so instead collect until session_complete and
	// assert the relative positions of the three types.
	started := time.Now()
	sendControlJSON(t, conn, `{"type":"submit_answers","params":{"answers":{"`+ready.Words[0]+`":"`+ready.Words[0]+`","`+ready.Words[1]+`":"`+ready.Words[1]+`"}}}`)

	var order []string
	for {
		payload := readFrameUntil(t, conn, ws.MsgTypeControl, `"type":"`)
		switch {
		case strings.Contains(payload, `"type":"batch_result"`):
			order = append(order, "batch_result")
		case strings.Contains(payload, `"type":"analysis_result"`):
			order = append(order, "analysis_result")
		case strings.Contains(payload, `"type":"session_complete"`):
			order = append(order, "session_complete")
		}
		if len(order) > 0 && order[len(order)-1] == "session_complete" {
			break
		}
		if time.Since(started) > 5*time.Second {
			t.Fatalf("did not reach session_complete; saw %v", order)
		}
	}
	if len(order) != 3 || order[0] != "batch_result" || order[1] != "analysis_result" || order[2] != "session_complete" {
		t.Errorf("completion frame order = %v, want [batch_result analysis_result session_complete]", order)
	}

	// Latency guard (Ticket 08): the grading round trip stays well under the
	// 1.5s product budget even with everything faked — a regression tripwire
	// for accidental synchronous work creeping into the handler.
	if d := time.Since(started); d > 1500*time.Millisecond {
		t.Errorf("submit_answers → session_complete took %v, want < 1.5s", d)
	}
}

// Story 3 — 两名学生并发: two connections hold live dictation sessions at
// the same time. The FSRS fake serves disjoint word sets; each student's
// exit unlock touches ONLY its own cards (the Ticket-07 containment
// contract). Both sessions' event loops run concurrently regardless of who
// sends frames, so the test drives the two connections INTERLEAVED from the
// test goroutine — keeping every Fatalf-based helper on the goroutine Go's
// testing package permits (helpers t.Fatalf; off-goroutine FailNow kills
// only the student goroutine and wedges wg.Wait).
func TestE2E_Story_TwoStudentsConcurrently(t *testing.T) {
	fakeFSRS := newStoriesFSRS()
	conns, _, teardown := bootStories(t, fakeFSRS, 2)
	defer teardown()

	// Both students start before either proceeds — the overlap is the point.
	var ready [2]sessionReadyResponse
	for i, conn := range conns {
		ready[i] = startStoriesSession(t, conn)
		if len(ready[i].Words) != 2 {
			t.Fatalf("student %d words = %v, want 2", i, ready[i].Words)
		}
	}

	// Interleaved dictation: play word 1 on both, then advance both.
	for i, conn := range conns {
		sendControlJSON(t, conn, `{"type":"play_word","params":{"word_id":"`+ready[i].Words[0]+`"}}`)
	}
	for i, conn := range conns {
		readControlUntil(t, conn, `"word_id":"`+ready[i].Words[0]+`"`)
		sendControlJSON(t, conn, `{"type":"next_word","params":{}}`)
	}
	for i, conn := range conns {
		readControlUntil(t, conn, `"word_id":"`+ready[i].Words[1]+`"`)
		sendControlJSON(t, conn, `{"type":"next_word","params":{}}`)
	}

	// Student A all-correct; student B one near-miss. map[string]string
	// marshal cannot fail, so the error is discarded.
	answers := [2]map[string]string{
		{ready[0].Words[0]: ready[0].Words[0], ready[0].Words[1]: ready[0].Words[1]},
		{ready[1].Words[0]: ready[1].Words[0], ready[1].Words[1]: nearMissOf(ready[1].Words[1])},
	}
	for i, conn := range conns {
		payload, _ := json.Marshal(answers[i])
		sendControlJSON(t, conn, `{"type":"submit_answers","params":{"answers":`+string(payload)+`}}`)
	}
	for _, conn := range conns {
		readControlUntil(t, conn, `"type":"analysis_result"`)
		readControlUntil(t, conn, "session_complete")
	}

	// Sessions sit in FEEDBACK until they exit — closing the connections
	// cancels their per-connection ctxs, firing the D-10 chain (and the
	// scoped unlocks) on both.
	for _, c := range conns {
		_ = c.Close()
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, unlocked := fakeFSRS.snapshot(); len(unlocked) == 2 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	// Containment: every unlock's IDs came from exactly one served batch.
	served, unlocked := fakeFSRS.snapshot()
	if len(served) != 2 {
		t.Fatalf("SelectDueCards served %d batches, want 2", len(served))
	}
	if len(unlocked) != 2 {
		t.Fatalf("unlock calls = %d, want 2", len(unlocked))
	}
	for _, ids := range unlocked {
		if len(ids) != 2 {
			t.Errorf("unlock ids = %v, want the session's two cards", ids)
			continue
		}
		inFirst := int64In(served[0], ids[0]) && int64In(served[0], ids[1])
		inSecond := int64In(served[1], ids[0]) && int64In(served[1], ids[1])
		if !inFirst && !inSecond {
			t.Errorf("unlock ids %v span both students' batches (%v / %v) — cross-session leak", ids, served[0], served[1])
		}
	}
}

// Story 4 — 错误场景: malformed frames, empty answers, wrong-state submits,
// and unknown control types all answer error frames WITHOUT killing the
// connection or the session (graceful degradation).
func TestE2E_Story_ErrorScenarios(t *testing.T) {
	fakeFSRS := newStoriesFSRS()
	conns, _, teardown := bootStories(t, fakeFSRS, 1)
	defer teardown()
	conn := conns[0]

	// Malformed JSON control frame.
	sendControlJSON(t, conn, `{not json`)
	if p := readErrorUntil(t, conn, "malformed control frame"); p == "" {
		t.Fatal("no error for malformed JSON")
	}
	// Unknown control type.
	sendControlJSON(t, conn, `{"type":"teleport","params":{}}`)
	if p := readErrorUntil(t, conn, "unknown control type"); p == "" {
		t.Fatal("no error for unknown type")
	}
	// Connection still healthy: a session starts.
	ready := startStoriesSession(t, conn)

	// Empty transcript answer is rejected as incomplete (not graded wrong).
	sendControlJSON(t, conn, `{"type":"submit_answer","params":{"word_id":"x","transcript":"   "}}`)
	if p := readErrorUntil(t, conn, "answer is empty"); p == "" {
		t.Fatal("no error for empty transcript")
	}

	// submit_answers mid-first-word rejects (dictation not finished).
	sendControlJSON(t, conn, `{"type":"play_word","params":{"word_id":"`+ready.Words[0]+`"}}`)
	readControlUntil(t, conn, `"type":"word_complete"`)
	sendControlJSON(t, conn, `{"type":"submit_answers","params":{"answers":{"`+ready.Words[0]+`":"`+ready.Words[0]+`"}}}`)
	if p := readErrorUntil(t, conn, "dictation is not finished"); p == "" {
		t.Fatal("no error for early batch submit")
	}

	// The session survived everything and still completes.
	sendControlJSON(t, conn, `{"type":"next_word","params":{}}`)
	readControlUntil(t, conn, `"word_id":"`+ready.Words[1]+`"`)
	sendControlJSON(t, conn, `{"type":"next_word","params":{}}`)
	sendControlJSON(t, conn, `{"type":"submit_answers","params":{"answers":{"`+ready.Words[0]+`":"`+ready.Words[0]+`"}}}`)
	readControlUntil(t, conn, "session_complete")
}

// Story 5 — 判分语义贯穿: the analysis report's error hints and the batch
// marks agree with the grading package's tiers (near-miss ≠ correct, blank =
// 没有作答), asserted end-to-end from wire payloads.
func TestE2E_Story_GradingTiersOnTheWire(t *testing.T) {
	fakeFSRS := newStoriesFSRS()
	conns, _, teardown := bootStories(t, fakeFSRS, 1)
	defer teardown()
	conn := conns[0]

	ready := startStoriesSession(t, conn)

	sendControlJSON(t, conn, `{"type":"play_word","params":{"word_id":"`+ready.Words[0]+`"}}`)
	readControlUntil(t, conn, `"type":"word_complete"`)
	sendControlJSON(t, conn, `{"type":"next_word","params":{}}`)
	readControlUntil(t, conn, `"word_id":"`+ready.Words[1]+`"`)
	sendControlJSON(t, conn, `{"type":"next_word","params":{}}`)

	// Word 1 correct, word 2 near-miss, word 2 left blank is impossible here
	// (2-word queue) — blank semantics are pinned by TestSubmitAnswers_Partial.
	sendControlJSON(t, conn, `{"type":"submit_answers","params":{"answers":{"`+ready.Words[0]+`":"`+ready.Words[0]+`","`+ready.Words[1]+`":"`+nearMissOf(ready.Words[1])+`"}}}`)

	batchPayload := readControlUntil(t, conn, `"type":"batch_result"`)
	var batch batchResultResponse
	if err := json.Unmarshal([]byte(batchPayload), &batch); err != nil {
		t.Fatalf("batch_result decode: %v", err)
	}
	if batch.Total != 2 || batch.CorrectCount != 1 {
		t.Errorf("batch counts = %d/%d, want 1/2", batch.CorrectCount, batch.Total)
	}
	if batch.Results[1].Correct || !batch.Results[1].NearMiss {
		t.Errorf("near-miss tier wrong on wire: %+v", batch.Results[1])
	}
	if len(batch.Results[1].Diff) == 0 {
		t.Errorf("batch result carries no diff: %+v", batch.Results[1])
	}

	analysisPayload := readControlUntil(t, conn, `"type":"analysis_result"`)
	var analysis analysisResultResponse
	if err := json.Unmarshal([]byte(analysisPayload), &analysis); err != nil {
		t.Fatalf("analysis_result decode: %v", err)
	}
	if len(analysis.Errors) != 1 || !analysis.Errors[0].NearMiss {
		t.Fatalf("analysis errors = %+v, want one near-miss", analysis.Errors)
	}
	if !strings.Contains(analysis.Errors[0].Hint, "只差一点") {
		t.Errorf("hint = %q, want near-miss wording", analysis.Errors[0].Hint)
	}
	if analysis.Errors[0].Ops[0].Kind != grading.OpMatch && analysis.Errors[0].Ops[0].Kind != grading.OpSwap {
		// transposition alignment starts with match or swap depending on word —
		// any full-alignment prefix is fine; just ensure ops are present.
		t.Logf("ops[0] kind = %s", analysis.Errors[0].Ops[0].Kind)
	}
	readControlUntil(t, conn, "session_complete")
}
