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

func TestCheckQuietBridgeAckTimeoutStaysOpenAndSubmitSucceeds(t *testing.T) {
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

func TestCheckQuietGradingIgnoresUplinkSpeech(t *testing.T) {
	cards := ttCards("苹果")
	words := newFakeWords(cards)
	tm, s, _, _ := ttDriveToCheck(t, cards, words)
	if !waitFor(t, 5*time.Second, func() bool { return gradingCount(s) > 0 }) {
		t.Fatal("grading state never broadcast after bridge ack")
	}
	asr, ok := tm.asr.(*fakeASR)
	if !ok {
		t.Fatalf("tm.asr is %T, want *fakeASR", tm.asr)
	}
	fedBefore := asr.fedCount()
	before := s.countOf(TypeTeacherSentence)
	// Outlive BOTH incidental gates first — the 500ms post-ack echo tail and
	// the 1s session_ready start grace (the instant fakes finish the drive
	// inside the grace): the assertion must put the STATE gate on trial
	// alone, or a dropped-by-grace utterance would keep it green for free.
	time.Sleep(1200 * time.Millisecond)
	tm.IngestAudio(s, ttUtterance())
	// ttUtterance carries its own trailing silence, so a still-listening
	// session would close VAD and answer well inside this window — the fakes
	// reply instantly once a turn starts. A negative assertion must outlive
	// that pipeline instead of sampling the unchanged count at once.
	time.Sleep(2 * time.Second)
	// The ASR call count is the primary gate proof (#96 review P3): a leaked
	// turn calls Transcribe even when the fake's script is exhausted (it
	// answers an error and no teacher sentence follows), so teacher_sentence
	// alone could stay flat while recognition still ran.
	if got := asr.fedCount(); got != fedBefore {
		t.Fatalf("ASR calls = %d after uplink in grading, want unchanged %d", got, fedBefore)
	}
	if got := s.countOf(TypeTeacherSentence); got != before {
		t.Fatalf("teacher_sentence count = %d, want unchanged %d after uplink in grading", got, before)
	}
	if got := len(words.submitCalls); got != 0 {
		t.Fatalf("SubmitReview calls = %d, want 0", got)
	}
}

// TestCheckQuietRefusesReadWordDuringBridge (#96 review P2-1): a word-ordinal
// tap mid-bridge sends interrupt → read_word. The interrupt bumps the epoch
// and puts the FSM back to listening while checking still holds — the
// read_word must be refused by the checking gate (not by the state gate), or
// it plays through the ordinary ack wait whose timeout orderlyCloses the
// grading session.
func TestCheckQuietRefusesReadWordDuringBridge(t *testing.T) {
	cards := ttCards("苹果", "香蕉")
	words := newFakeWords(cards)
	words.reviewResult = ReviewWriteResult{Updated: []string{"苹果", "香蕉"}}
	tm, s, _, _ := ttDriveToCheckBridgePending(t, cards, words)

	tm.HandleControl(s, mustJSON(t, InterruptMsg{Type: TypeInterrupt}))
	before := s.countOf(TypeTeacherSentence)
	errBefore := s.countOf(TypeError)
	tm.HandleControl(s, mustJSON(t, ReadWordMsg{Type: TypeReadWord, WordIndex: 0, CurrentIndex: len(cards)}))
	time.Sleep(300 * time.Millisecond)
	if got := s.countOf(TypeTeacherSentence); got != before {
		t.Fatalf("check-stage re-read spoke %d sentences, want 0", got-before)
	}
	if got := s.countOf(TypeError); got != errBefore+1 {
		t.Fatalf("check-stage re-read errors = %d, want exactly one refusal", got-errBefore)
	}
	tm.mu.Lock()
	closed := tm.closed
	tm.mu.Unlock()
	if closed {
		t.Fatal("a word-ordinal tap during the bridge must not close the session")
	}

	// The grading session survives the stray tap: verdicts still land, the
	// one review write answers, and the normal ending still completes.
	ttCheckAll(t, tm, s, len(cards))
	result := ttSubmitCheck(t, tm, s)
	if int(result["updated"].(float64)) != len(cards) {
		t.Fatalf("review_result.updated = %v, want %d after the refused re-read", result["updated"], len(cards))
	}
	ttAckSummaryAndEnd(t, tm, s, nil)
}

func TestCheckQuietBridgeTimeoutDuringSubmitDoesNotCancelReview(t *testing.T) {
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
