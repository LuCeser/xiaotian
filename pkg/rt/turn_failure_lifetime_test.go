package rt

import (
	"errors"
	"testing"
	"time"

	"github.com/xiaotian/home-tutor-agent/pkg/mimo"
)

// TestTurnFailureLifetime pins failTurn's failure report to the failing
// turn's own identity: the error message, the playback-wait sweep, the
// listening recovery and the state message are one turn effect. A close
// racing the report either refuses it outright or lands behind the accepted
// pair — the failure notice never trails session_ended, and a session that
// already ended hears nothing at all.
func TestTurnFailureLifetime(t *testing.T) {
	t.Run("close at the error enqueue keeps the report ahead of session_ended", func(t *testing.T) {
		asr := newFakeASR(asrScript{text: "老师你好"})
		llm := newFakeLLM(intentScript(intentChat, "你好呀"))
		tm, s, _, _ := ttIntentFixture(t, ttCards("苹果", "香蕉"), asr, llm)
		tm.tts.(*fakeTTS).failWith = errors.New("tts down") // the reply's playback fails
		listeningBefore := s.listeningCount()

		turnSender := &closeAtControlSender{
			captureSender: s, tm: tm, target: TypeError, closeDone: make(chan struct{}),
		}
		done := ttStartTurn(tm, turnSender)
		ttAwaitGate(t, turnSender.closeDone, "close at the error enqueue did not finish")
		ttAwaitGate(t, done, "failed turn did not finish")

		errs := s.controlsOf(t, TypeError)
		if len(errs) != 1 || errs[0]["code"] != ErrCodeProviderFailed {
			t.Fatalf("error messages = %v, want exactly one provider failure", errs)
		}
		if got := s.listeningCount(); got != listeningBefore+1 {
			t.Fatalf("listening states = %d, want %d (the accepted recovery)", got, listeningBefore+1)
		}
		order := s.typeOrder()
		errAt, endAt := -1, -1
		for i, typ := range order {
			if typ == TypeError && errAt < 0 {
				errAt = i
			}
			if typ == TypeSessionEnded && endAt < 0 {
				endAt = i
			}
		}
		if errAt < 0 || endAt < 0 || errAt > endAt {
			t.Fatalf("error at %d, session_ended at %d: the failure report must precede the goodbye", errAt, endAt)
		}
		for _, typ := range order[endAt+1:] {
			switch typ {
			case TypeState, TypeError, TypeTeacherSentence, TypeAudioStart, TypeAudioEnd:
				t.Fatalf("presentation message %s after session_ended (order=%v)", typ, order)
			}
		}
		tm.mu.Lock()
		state, closed := tm.state, tm.closed
		tm.mu.Unlock()
		if state != "" || !closed {
			t.Fatalf("state=%q closed=%v, want the closed session's reset", state, closed)
		}
	})

	t.Run("close while ASR is failing turns silent", func(t *testing.T) {
		asr := newFakeASR(asrScript{err: errors.New("asr down")})
		entered, release := asr.gateNextTranscribe()
		tm, s, _, _ := ttIntentFixture(t, ttCards("苹果", "香蕉"), asr, newFakeLLM())

		done := ttStartTurn(tm, s)
		ttAwaitGate(t, entered, "ASR was not entered")
		tm.handleEndSession(s)
		endAt := len(s.typeOrder())
		close(release)
		ttAwaitGate(t, done, "failing turn did not finish")

		if errs := s.controlsOf(t, TypeError); len(errs) != 0 {
			t.Fatalf("error messages after close = %v, want none", errs)
		}
		for _, typ := range s.typeOrder()[endAt:] {
			switch typ {
			case TypeState, TypeError, TypeTeacherSentence, TypeAudioStart, TypeAudioEnd:
				t.Fatalf("presentation message %s after session_ended", typ)
			}
		}
	})
}

// TestASREmptyDoesNotCompletePlayback: an empty (or unusable) transcription
// returns the FSM to listening without stamping a playback-completion time
// or starting an echo tail — silence never counts as played audio.
func TestASREmptyDoesNotCompletePlayback(t *testing.T) {
	for _, tc := range []struct {
		asr  *fakeASR
		name string
	}{
		{name: "empty transcription", asr: newFakeASR(asrScript{err: mimo.ErrEmptyTranscription})},
		{name: "unusable transcript", asr: newFakeASR(asrScript{text: ""})},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tm, s, _, _ := ttIntentFixture(t, ttCards("苹果", "香蕉"), tc.asr, newFakeLLM())
			tm.mu.Lock()
			completedBefore, echoBefore := tm.lastPlaybackCompletedAt, tm.echoTailUntil
			tm.mu.Unlock()
			listeningBefore := s.listeningCount()

			done := ttStartTurn(tm, s)
			select {
			case <-done:
			case <-time.After(2 * time.Second):
				t.Fatal("empty turn did not settle")
			}

			if got := s.listeningCount(); got != listeningBefore+1 {
				t.Fatalf("listening states = %d, want %d (the recovery)", got, listeningBefore+1)
			}
			if errs := s.controlsOf(t, TypeError); len(errs) != 0 {
				t.Fatalf("error messages = %v, want none", errs)
			}
			tm.mu.Lock()
			completed, echo, state := tm.lastPlaybackCompletedAt, tm.echoTailUntil, tm.state
			tm.mu.Unlock()
			if !completed.Equal(completedBefore) {
				t.Fatal("an empty transcription stamped a playback-completion time")
			}
			if !echo.Equal(echoBefore) {
				t.Fatal("an empty transcription started an echo tail")
			}
			if state != StateListening {
				t.Fatalf("state = %q, want listening", state)
			}
		})
	}
}
