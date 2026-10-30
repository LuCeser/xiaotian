package rt

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/xiaotian/home-tutor-agent/pkg/lesson"
)

func gradingCount(s *captureSender) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, b := range s.controls {
		var m StateMsg
		if json.Unmarshal(b, &m) == nil && m.Value == StateGrading {
			n++
		}
	}
	return n
}

func playbackAckTimeoutCount(s *captureSender) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, b := range s.controls {
		var m ErrorMsg
		if json.Unmarshal(b, &m) == nil && m.Code == ErrCodePlaybackAckTimeout {
			n++
		}
	}
	return n
}

// ttDriveToCheckBridgePending is ttDriveToCheck but leaves the bridge
// announcement awaiting its sentence_ack.
func ttDriveToCheckBridgePending(t *testing.T, cards []lesson.Word, words *fakeWordSource) (*TurnManager, *captureSender, *captureLogger, *fakeExecutor) {
	t.Helper()
	log := &captureLogger{}
	exec := newFakeExecutorNoRow(ttLessonID)
	asrScripts := make([]asrScript, len(cards))
	llmScripts := make([]llmScript, len(cards))
	for i := range cards {
		asrScripts[i] = asrScript{text: "好了"}
		llmScripts[i] = intentScript(intentConfirm, "")
	}
	asr := newFakeASR(asrScripts...)
	llm := newFakeLLM(llmScripts...)
	tts := newFakeTTS(2, 320)
	tts.delay = 20 * time.Millisecond
	tm := NewTurnManager(asr, llm, tts, exec, ttConfigWith(words, nil), NewEventRecorder(log, "t"), nil)
	s := &captureSender{}
	ttInit(t, tm, s)
	ttAwaitSpoken(t, s, 3)
	ttAckLast(t, tm, s, 0)
	ttWaitListening(t, s)

	for i := 0; i < len(cards); i++ {
		done := make(chan struct{})
		go func() {
			tm.processTurn(s, []byte("pcm"))
			close(done)
		}()
		if i < len(cards)-1 {
			if !waitFor(t, 5*time.Second, func() bool { return s.countOf(TypeLessonUpdate) == i+1 }) {
				t.Fatalf("confirm %d never committed", i)
			}
			ttAckLast(t, tm, s, 0)
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Fatalf("confirm %d never settled", i)
			}
			continue
		}
		ttCheckStartedWanted(t, s)
		spokenBefore := s.countOf(TypeTeacherSentence)
		if !waitFor(t, 5*time.Second, func() bool { return s.countOf(TypeTeacherSentence) > spokenBefore }) {
			t.Fatal("check bridge announcement never spoke")
		}
	}
	return tm, s, log, exec
}

func TestCheckBridgeAckTimeoutStaysOpenAndSubmitSucceeds(t *testing.T) {
	cards := ttCards("苹果", "香蕉")
	words := newFakeWords(cards)
	words.reviewResult = ReviewWriteResult{Updated: []string{"苹果", "香蕉"}}
	tm, s, _, _ := ttDriveToCheckBridgePending(t, cards, words)

	if !waitFor(t, 6*time.Second, func() bool { return gradingCount(s) > 0 }) {
		t.Fatal("FSM never entered grading after bridge ack timeout")
	}
	if playbackAckTimeoutCount(s) != 0 {
		t.Fatalf("playback_ack_timeout controls = %d, want 0", playbackAckTimeoutCount(s))
	}
	tm.mu.Lock()
	closed := tm.closed
	tm.mu.Unlock()
	if closed {
		t.Fatal("session closed after bridge timeout")
	}
	if s.countOf(TypeSessionEnded) != 0 {
		t.Fatal("session_ended must not fire after bridge timeout")
	}

	ttCheckAll(t, tm, s, len(cards))
	result := ttSubmitCheck(t, tm, s)
	if int(result["updated"].(float64)) != len(cards) {
		t.Fatalf("review_result.updated = %v, want %d", result["updated"], len(cards))
	}
}

func TestCheckGradingIgnoresUplinkSpeech(t *testing.T) {
	cards := ttCards("苹果")
	words := newFakeWords(cards)
	tm, s, _, _ := ttDriveToCheck(t, cards, words)
	if !waitFor(t, 5*time.Second, func() bool { return gradingCount(s) > 0 }) {
		t.Fatal("grading state never broadcast after bridge ack")
	}
	before := s.countOf(TypeTeacherSentence)
	tm.IngestAudio(s, make([]byte, 4096))
	time.Sleep(300 * time.Millisecond)
	if got := s.countOf(TypeTeacherSentence); got != before {
		t.Fatalf("teacher_sentence count = %d, want unchanged %d after uplink in grading", got, before)
	}
	if got := len(words.submitCalls); got != 0 {
		t.Fatalf("SubmitReview calls = %d, want 0", got)
	}
}

func TestCheckBridgeTimeoutDuringSubmitDoesNotCancelReview(t *testing.T) {
	cards := ttCards("苹果", "香蕉")
	words := newFakeWords(cards)
	block := make(chan struct{})
	words.reviewBlock = block
	words.reviewResult = ReviewWriteResult{Updated: []string{"苹果", "香蕉"}}
	tm, s, _, _ := ttDriveToCheckBridgePending(t, cards, words)
	ttCheckAll(t, tm, s, len(cards))

	go tm.HandleControl(s, mustJSON(t, CheckSubmitMsg{Type: TypeCheckSubmit, RequestID: "req-overlap"}))

	if !waitFor(t, 6*time.Second, func() bool { return gradingCount(s) > 0 }) {
		t.Fatal("bridge wait never ended into grading")
	}
	close(block)

	var result map[string]any
	if !waitFor(t, 5*time.Second, func() bool {
		got := s.controlsOf(t, TypeReviewResult)
		if len(got) == 0 {
			return false
		}
		result = got[0]
		return true
	}) {
		t.Fatal("review_result never arrived")
	}
	if len(words.submitCalls) != 1 {
		t.Fatalf("SubmitReview calls = %d, want 1", len(words.submitCalls))
	}
	if int(result["updated"].(float64)) != len(cards) {
		t.Fatalf("review_result.updated = %v, want %d", result["updated"], len(cards))
	}
}
