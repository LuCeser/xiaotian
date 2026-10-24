package rt

import (
	"encoding/json"
	"sync"
	"testing"
)

// closeAtSpeakingSender closes the real manager immediately before the
// announcement's state=speaking message is queued. Under the unified turn
// entry the enqueue rides tm.mu, so the close (TryLock fails) defers to a
// goroutine and the ACCEPTED state still lands ahead of session_ended; a
// regression that moves the send back outside the lock would close
// synchronously and let session_ended win the order.
type closeAtSpeakingSender struct {
	*captureSender
	tm        *TurnManager
	closeDone chan struct{}
	once      sync.Once
}

func (s *closeAtSpeakingSender) SendControl(v any) error {
	if m, ok := v.(StateMsg); ok && m.Type == TypeState && m.Value == StateSpeaking {
		s.once.Do(func() {
			closeSession := func() {
				s.tm.handleEndSession(s)
				close(s.closeDone)
			}
			if s.tm.mu.TryLock() {
				s.tm.mu.Unlock()
				closeSession()
			} else {
				go closeSession()
			}
		})
	}
	return s.captureSender.SendControl(v)
}

// TestSpeakRegistrationLifetime pins speakTurn's two registration branches to
// the unified turn-effects entry: the start segment (history, sentence reset,
// state=speaking, playbackPending) and the per-sentence registration
// (pendingPlayback, turnSentences, teacher_sentence, audio_start). A close
// that lands after the caller's entry check, at the speaking enqueue, or
// between the two branches must leave nothing behind — no history round, no
// FSM revival, no message past session_ended.
func TestSpeakRegistrationLifetime(t *testing.T) {
	t.Run("close after the entry check keeps the start registration silent", func(t *testing.T) {
		asr := newFakeASR(asrScript{text: "老师你好"})
		llm := newFakeLLM(intentScript(intentChat, "你好呀"))
		tm, captured, _, _ := ttIntentFixture(t, ttCards("苹果", "香蕉"), asr, llm)
		before := ttShortHistory(t, tm)
		sentencesBefore := captured.countOf(TypeTeacherSentence)
		startsBefore := captured.countOf(TypeAudioStart)
		speakingBefore := ttSpeakingStates(t, captured) // the fixture's greeting already spoke
		tm.mu.Lock()
		registeredBefore := len(tm.turnSentences) // the greeting's sentences stay (the reset never ran)
		tm.mu.Unlock()
		hit := make(chan struct{})
		tm.speakTurnInterleave = func() {
			tm.speakTurnInterleave = nil
			tm.handleEndSession(captured)
			close(hit)
		}

		done := ttStartTurn(tm, captured)
		ttAwaitGate(t, hit, "speakTurn entry probe was not reached")
		ttAwaitGate(t, done, "turn did not finish after the close")

		history := ttShortHistory(t, tm)
		if len(history) != len(before)+1 || history[len(history)-1].teacherText != "" {
			t.Fatalf("history after close = %+v, want only the bare student round", history)
		}
		if got := ttSpeakingStates(t, captured); got != speakingBefore {
			t.Fatalf("state=speaking messages = %d, want %d after close", got, speakingBefore)
		}
		if got := captured.countOf(TypeTeacherSentence); got != sentencesBefore {
			t.Fatalf("teacher sentences = %d, want %d after close", got, sentencesBefore)
		}
		if got := captured.countOf(TypeAudioStart); got != startsBefore {
			t.Fatalf("audio starts = %d, want %d after close", got, startsBefore)
		}
		tm.mu.Lock()
		state, sentences, pending, closed := tm.state, len(tm.turnSentences), len(tm.pendingPlayback), tm.closed
		tm.mu.Unlock()
		if state != "" || sentences != registeredBefore || pending != 0 || !closed {
			t.Fatalf("state=%q turnSentences=%d pending=%d closed=%v, want a silent closed session",
				state, sentences, pending, closed)
		}
	})

	t.Run("close at the speaking enqueue keeps the accepted state ahead of session_ended", func(t *testing.T) {
		asr := newFakeASR(asrScript{text: "老师你好"})
		llm := newFakeLLM(intentScript(intentChat, "你好呀"))
		tm, captured, _, _ := ttIntentFixture(t, ttCards("苹果", "香蕉"), asr, llm)
		speakingBefore := ttSpeakingStates(t, captured) // the fixture's greeting already spoke
		s := &closeAtSpeakingSender{captureSender: captured, tm: tm, closeDone: make(chan struct{})}

		done := ttStartTurn(tm, s)
		ttAwaitGate(t, s.closeDone, "close at the speaking enqueue did not finish")
		ttAwaitGate(t, done, "turn did not finish after the close")

		captured.mu.Lock()
		endAt, speakingAt, speaking := -1, -1, 0
		for i, b := range captured.controls {
			var probe struct {
				Type  string `json:"type"`
				Value string `json:"value"`
			}
			if json.Unmarshal(b, &probe) != nil {
				continue
			}
			if probe.Type == TypeSessionEnded && endAt < 0 {
				endAt = i
			}
			if probe.Type == TypeState && probe.Value == StateSpeaking {
				speaking++
				speakingAt = i
			}
		}
		var late []string
		for _, b := range captured.controls[endAt+1:] {
			var probe struct {
				Type string `json:"type"`
			}
			if json.Unmarshal(b, &probe) != nil {
				continue
			}
			if probe.Type == TypeState || probe.Type == TypeTeacherSentence ||
				probe.Type == TypeAudioStart || probe.Type == TypeAudioEnd {
				late = append(late, probe.Type)
			}
		}
		captured.mu.Unlock()
		if endAt < 0 {
			t.Fatal("session_ended was never captured")
		}
		if speaking != speakingBefore+1 || speakingAt > endAt {
			t.Fatalf("state=speaking count=%d at=%d, want exactly the announcement's own before session_ended at %d",
				speaking, speakingAt, endAt)
		}
		if len(late) != 0 {
			t.Fatalf("presentation messages after session_ended = %v", late)
		}
		history := ttShortHistory(t, tm)
		if last := history[len(history)-1]; last.teacherText == "" {
			t.Fatalf("history = %+v, want the accepted start registration's teacher round", history)
		}
	})

	t.Run("close after the start registration keeps per-sentence registration silent", func(t *testing.T) {
		asr := newFakeASR(asrScript{text: "老师你好"})
		llm := newFakeLLM(intentScript(intentChat, "你好呀"))
		tm, captured, _, _ := ttIntentFixture(t, ttCards("苹果", "香蕉"), asr, llm)
		sentencesBefore := captured.countOf(TypeTeacherSentence)
		startsBefore := captured.countOf(TypeAudioStart)
		endsBefore := captured.countOf(TypeAudioEnd)
		audioBefore := captured.audioCount()
		hit := make(chan struct{})
		tm.speakRegistrationInterleave = func() {
			tm.speakRegistrationInterleave = nil
			tm.handleEndSession(captured)
			close(hit)
		}

		done := ttStartTurn(tm, captured)
		ttAwaitGate(t, hit, "per-sentence registration probe was not reached")
		ttAwaitGate(t, done, "turn did not finish after the close")

		if got := captured.countOf(TypeTeacherSentence); got != sentencesBefore {
			t.Fatalf("teacher sentences = %d, want %d after close", got, sentencesBefore)
		}
		if got := captured.countOf(TypeAudioStart); got != startsBefore {
			t.Fatalf("audio starts = %d, want %d after close", got, startsBefore)
		}
		if got := captured.countOf(TypeAudioEnd); got != endsBefore {
			t.Fatalf("audio ends = %d, want %d after close", got, endsBefore)
		}
		if got := captured.audioCount(); got != audioBefore {
			t.Fatalf("audio chunks = %d, want %d after close", got, audioBefore)
		}
		tm.mu.Lock()
		state, sentences, pending := tm.state, len(tm.turnSentences), len(tm.pendingPlayback)
		tm.mu.Unlock()
		if state != "" || sentences != 0 || pending != 0 {
			t.Fatalf("state=%q turnSentences=%d pending=%d, want no late per-sentence registration",
				state, sentences, pending)
		}
		history := ttShortHistory(t, tm)
		if last := history[len(history)-1]; last.teacherText == "" {
			t.Fatalf("history = %+v, want the accepted start registration preserved", history)
		}
	})
}

// ttSpeakingStates counts state=speaking control messages.
func ttSpeakingStates(t *testing.T, s *captureSender) int {
	t.Helper()
	n := 0
	for _, m := range s.controlsOf(t, TypeState) {
		if v, _ := m["value"].(string); v == StateSpeaking {
			n++
		}
	}
	return n
}
