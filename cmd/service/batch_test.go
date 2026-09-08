package main

// Batch answer submission tests (Ticket 05): submit_answers through the REAL
// Manager + REAL FSM — DICTATING/WAITING_PARENT → SUBMISSION → FEEDBACK —
// with grading (compareAnswer) and the server-side FSRS UpdateRatings call
// observed end-to-end on the client socket. Fakes: fsmGen/fsmStreamer/fsmRepo
// from voice_fsm_test.go; batchFSRS below adds ratings capture.

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/xiaotian/home-tutor-agent/pkg/database"
	"github.com/xiaotian/home-tutor-agent/pkg/fsrs"
	"github.com/xiaotian/home-tutor-agent/pkg/infra"
	"github.com/xiaotian/home-tutor-agent/pkg/pybridge"
	"github.com/xiaotian/home-tutor-agent/pkg/session"
	"github.com/xiaotian/home-tutor-agent/pkg/ws"
	"go.uber.org/zap"
)

// batchFSRS feeds two due words and CAPTURES the ratings UpdateRatings
// received (the thing the batch handler must submit server-side).
type batchFSRS struct {
	mu      sync.Mutex
	ratings map[string]string
	calls   int
}

func (f *batchFSRS) SelectDueCards(_ context.Context, _ fsrs.SelectOpts) ([]pybridge.DueCard, error) {
	return []pybridge.DueCard{{ID: 1, Word: "苹果"}, {ID: 2, Word: "香蕉"}}, nil
}

func (f *batchFSRS) UpdateRatings(_ context.Context, ratings map[string]string, _ string) (pybridge.ReviewResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ratings = ratings
	f.calls++
	return pybridge.ReviewResult{}, nil
}

func (f *batchFSRS) UnlockCards(_ context.Context, _ fsrs.UnlockOpts) (int, error) {
	return 0, nil
}

func (f *batchFSRS) snapshot() (map[string]string, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make(map[string]string, len(f.ratings))
	for k, v := range f.ratings {
		out[k] = v
	}
	return out, f.calls
}

// bootBatchSession boots the real-Manager stack (voice_fsm_test pattern),
// starts a two-word session, plays it to the WAITING_PARENT state, and
// returns the dialed connection + manager + the connID the session is
// registered under + teardown. stopAtFirstWord returns instead at word 1's
// DICTATING (mid-dictation).
func bootBatchSession(t *testing.T, fakeFSRS *batchFSRS, stopAtFirstWord bool) (*websocket.Conn, *session.Manager, string, func()) {
	t.Helper()
	logger := infra.NewNoOpLogger()
	zapLogger := zap.NewNop()

	hub, err := ws.NewHub(ws.HubConfig{Limit: 10, RegisterBuffer: 32, UnregisterBuffer: 32}, zapLogger)
	if err != nil {
		t.Fatalf("failed to create hub: %v", err)
	}

	cfg := infra.DefaultConfig()
	pool, err := infra.NewWorkerPool(cfg.Worker, logger)
	if err != nil {
		t.Fatalf("failed to create worker pool: %v", err)
	}
	t.Cleanup(func() { _ = pool.Shutdown(context.Background()) })

	manager, err := session.NewManager(hub, fakeFSRS, &fsmGen{}, &fsmStreamer{}, &fsmRepo{}, pool, logger, cfg)
	if err != nil {
		t.Fatalf("failed to create session manager: %v", err)
	}
	svcCtx, svcCancel := context.WithCancel(context.Background())
	t.Cleanup(svcCancel)
	go hub.Run(svcCtx)
	go manager.Start(svcCtx)

	// Capture the server-side *ws.Client through the audio route (the
	// Manager keys sessions by the client pointer, same as production).
	handlers := ttsHandlers(manager, fakeFSRS, nil, nil, logger)
	connIDCh := make(chan string, 1)
	audioHandler := handlers[ws.MsgTypeAudio]
	handlers[ws.MsgTypeAudio] = func(ctx context.Context, c *ws.Client, h *ws.FrameHeader, p []byte) error {
		select {
		case connIDCh <- clientConnID(c):
		default:
		}
		return audioHandler(ctx, c, h, p)
	}

	conn, teardown := dialVoiceTestServerOnHub(t, hub, handlers)

	// 1. Start a session through the real handler chain. Category "chinese"
	// selects zh normalization in compareAnswer (the words are Chinese; the
	// default en path would strip CJK to empty strings and match anything).
	sendControlJSON(t, conn, `{"type":"start_session","params":{"limit":2,"category":"chinese"}}`)
	if ready := readControlUntil(t, conn, "session_ready"); ready == "" {
		t.Fatal("no session_ready")
	}

	// 2. Capture the connID via one dummy audio frame.
	sendAudioFrame(t, conn, 1, make([]byte, ws.AudioFrameSize))
	var connID string
	select {
	case connID = <-connIDCh:
	case <-time.After(5 * time.Second):
		teardown()
		t.Fatal("timed out capturing server-side connID")
	}

	// 3. Drive to WAITING_PARENT: play both words and advance past the last.
	// Waits pin state AND current word — polling state alone can't tell word
	// 1's DICTATING from word 2's, and a next_word sent into the interim
	// LISTENING is dropped by the FSM as invalid.
	sendControlJSON(t, conn, `{"type":"play_word","params":{"word_id":"苹果"}}`)
	waitBatchWord(t, manager, connID, database.StateDictating, "苹果")
	if stopAtFirstWord {
		return conn, manager, connID, teardown
	}
	sendControlJSON(t, conn, `{"type":"next_word","params":{}}`)
	waitBatchWord(t, manager, connID, database.StateDictating, "香蕉")
	sendControlJSON(t, conn, `{"type":"next_word","params":{}}`)
	waitBatchState(t, manager, connID, database.StateWaitingParent)

	return conn, manager, connID, teardown
}

// waitBatchWord polls the manager until the connection's session reaches the
// wanted state with the wanted current word (deterministic poll, ticket-02
// convention).
func waitBatchWord(t *testing.T, manager *session.Manager, connID string, want database.SessionState, word string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if s := manager.Lookup(connID); s != nil && s.State() == want && s.CurrentWord() == word {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("session never reached %s at word %s", want, word)
}

// waitBatchState polls the manager until the connection's session reaches the
// wanted state (deterministic poll, ticket-02 convention).
func waitBatchState(t *testing.T, manager *session.Manager, connID string, want database.SessionState) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if s := manager.Lookup(connID); s != nil && s.State() == want {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("session never reached %s", want)
}

// readErrorUntil reads server frames until an ERROR frame whose payload
// contains substr, returning that payload (twin of readControlUntil).
func readErrorUntil(t *testing.T, conn *websocket.Conn, substr string) string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		header, payload := readFrame(t, conn)
		if header.MsgType == ws.MsgTypeError && strings.Contains(string(payload), substr) {
			return string(payload)
		}
	}
	t.Fatalf("timed out waiting for error frame containing %q", substr)
	return ""
}

// TestSubmitAnswers_BatchToFeedback: the full batch spine — submit_answers
// with answers for every queue word → batch_result (graded per word, in
// queue order) → FSRS UpdateRatings server-side → SUBMISSION → FEEDBACK →
// session_complete.
func TestSubmitAnswers_BatchToFeedback(t *testing.T) {
	fakeFSRS := &batchFSRS{}
	conn, manager, connID, teardown := bootBatchSession(t, fakeFSRS, false)
	defer teardown()

	sendControlJSON(t, conn, `{"type":"submit_answers","params":{"answers":{"苹果":"苹果","香蕉":"香焦"}}}`)

	// batch_result: queue order, per-word judgment (second answer is wrong).
	payload := readControlUntil(t, conn, "batch_result")
	var result batchResultResponse
	if err := json.Unmarshal([]byte(payload), &result); err != nil {
		t.Fatalf("batch_result JSON decode: %v", err)
	}
	if result.Type != "batch_result" {
		t.Fatalf("reply type = %q, want batch_result", result.Type)
	}
	if len(result.Results) != 2 {
		t.Fatalf("results = %d, want 2", len(result.Results))
	}
	if result.Results[0].Word != "苹果" || result.Results[1].Word != "香蕉" {
		t.Errorf("results not in queue order: %+v", result.Results)
	}
	if !result.Results[0].Correct || result.Results[0].Rating != "good" {
		t.Errorf("苹果 result = %+v, want correct/good", result.Results[0])
	}
	if result.Results[1].Correct || result.Results[1].Rating != "again" {
		t.Errorf("香蕉 (typo 香焦) result = %+v, want incorrect/again", result.Results[1])
	}
	if result.Results[1].Heard != "香焦" {
		t.Errorf("heard = %q, want 香焦", result.Results[1].Heard)
	}
	if result.CorrectCount != 1 || result.Total != 2 {
		t.Errorf("counts = %d/%d, want 1/2", result.CorrectCount, result.Total)
	}

	// The FSRS ratings were submitted server-side, one call, all words.
	ratings, calls := fakeFSRS.snapshot()
	if calls != 1 {
		t.Errorf("UpdateRatings calls = %d, want 1", calls)
	}
	if ratings["苹果"] != "good" || ratings["香蕉"] != "again" {
		t.Errorf("ratings = %v, want 苹果:good 香蕉:again", ratings)
	}

	// The FSM spine: session_complete (FEEDBACK) follows.
	readControlUntil(t, conn, "session_complete")
	if s := manager.Lookup(connID); s == nil || s.State() != database.StateFeedback {
		got := "<nil>"
		if s != nil {
			got = string(s.State())
		}
		t.Errorf("session state after batch = %s, want FEEDBACK", got)
	}
}

// TestSubmitAnswers_PartialAnswers: unanswered words grade incorrect/again
// (classroom semantics: a blank is wrong), the session still completes.
func TestSubmitAnswers_PartialAnswers(t *testing.T) {
	fakeFSRS := &batchFSRS{}
	conn, manager, connID, teardown := bootBatchSession(t, fakeFSRS, false)
	defer teardown()

	sendControlJSON(t, conn, `{"type":"submit_answers","params":{"answers":{"苹果":"苹果"}}}`)

	payload := readControlUntil(t, conn, "batch_result")
	var result batchResultResponse
	if err := json.Unmarshal([]byte(payload), &result); err != nil {
		t.Fatalf("batch_result JSON decode: %v", err)
	}
	if result.Total != 2 || result.CorrectCount != 1 {
		t.Errorf("counts = %d/%d, want 1/2", result.CorrectCount, result.Total)
	}
	if result.Results[1].Heard != "" || result.Results[1].Correct || result.Results[1].Rating != "again" {
		t.Errorf("unanswered word result = %+v, want empty heard / incorrect / again", result.Results[1])
	}

	ratings, _ := fakeFSRS.snapshot()
	if ratings["香蕉"] != "again" {
		t.Errorf("unanswered word rating = %q, want again", ratings["香蕉"])
	}

	readControlUntil(t, conn, "session_complete")
	if s := manager.Lookup(connID); s == nil || s.State() != database.StateFeedback {
		t.Errorf("session did not reach FEEDBACK after partial batch")
	}
}

// TestSubmitAnswers_Validation: structural rejections answer with error
// frames and leave the session in WAITING_PARENT (no grading, no FSRS call).
func TestSubmitAnswers_Validation(t *testing.T) {
	fakeFSRS := &batchFSRS{}
	conn, manager, connID, teardown := bootBatchSession(t, fakeFSRS, false)
	defer teardown()

	cases := []struct {
		name    string
		payload string
		wantMsg string
	}{
		{"empty map", `{"type":"submit_answers","params":{"answers":{}}}`, "answers map is empty"},
		{"unknown word", `{"type":"submit_answers","params":{"answers":{"梨子":"梨子"}}}`, "unknown word"},
		{"empty answer text", `{"type":"submit_answers","params":{"answers":{"苹果":"  "}}}`, "is empty"},
	}
	for _, tc := range cases {
		sendControlJSON(t, conn, tc.payload)
		if payload := readErrorUntil(t, conn, tc.wantMsg); payload == "" {
			t.Fatalf("%s: no error frame containing %q", tc.name, tc.wantMsg)
		}
	}

	// The session survived untouched.
	if s := manager.Lookup(connID); s == nil || s.State() != database.StateWaitingParent {
		t.Error("session state changed by rejected submissions")
	}
	if _, calls := fakeFSRS.snapshot(); calls != 0 {
		t.Errorf("UpdateRatings calls = %d, want 0 for rejected submissions", calls)
	}

	// A subsequent VALID submission still works (validation is non-destructive).
	sendControlJSON(t, conn, `{"type":"submit_answers","params":{"answers":{"苹果":"苹果","香蕉":"香蕉"}}}`)
	readControlUntil(t, conn, "batch_result")
	readControlUntil(t, conn, "session_complete")
}

// TestSubmitAnswers_NoSession: without an active session the handler rejects
// with an error frame.
func TestSubmitAnswers_NoSession(t *testing.T) {
	conn, teardown := dialVoiceTestServer(t, ttsHandlers(nil, nil, nil, nil, infra.NewNoOpLogger()))
	defer teardown()

	sendControlJSON(t, conn, `{"type":"submit_answers","params":{"answers":{"苹果":"苹果"}}}`)

	header, payload := readFrame(t, conn)
	if header.MsgType != ws.MsgTypeError {
		t.Fatalf("reply msg type = %s, want error", header.MsgType)
	}
	if !strings.Contains(string(payload), "no active session") {
		t.Errorf("error payload = %s, want no active session", payload)
	}
}

// TestSubmitAnswers_EarlyDictatingRejected: submitting while still on a
// NON-last word rejects (an early submit would grade never-dictated words);
// the session stays in DICTATING and a later, legal submission path remains.
func TestSubmitAnswers_EarlyDictatingRejected(t *testing.T) {
	fakeFSRS := &batchFSRS{}
	conn, manager, connID, teardown := bootBatchSession(t, fakeFSRS, true)
	defer teardown()

	sendControlJSON(t, conn, `{"type":"submit_answers","params":{"answers":{"苹果":"苹果"}}}`)
	if payload := readErrorUntil(t, conn, "dictation is not finished yet"); payload == "" {
		t.Fatal("no rejection for an early-DICTATING batch submit")
	}

	// Untouched: still dictating word 1, no FSRS write.
	if s := manager.Lookup(connID); s == nil || s.State() != database.StateDictating {
		t.Error("early submit mutated the session state")
	}
	if _, calls := fakeFSRS.snapshot(); calls != 0 {
		t.Errorf("UpdateRatings calls = %d, want 0", calls)
	}
}
