package rt

import (
	"strings"
	"testing"
	"time"
)

func TestReadWordSelectionDoesNotAdvance(t *testing.T) {
	for _, index := range []int{0, 1} {
		t.Run([]string{"0", "1"}[index], func(t *testing.T) {
			cards := ttCards("苹果", "香蕉", "橘子")
			exec := newFakeExecutor(ttLessonID, cards)
			exec.setRow(1, 2)
			tts := newFakeTTS(1, 320)
			tm := NewTurnManager(newFakeASR(), newFakeLLM(), tts, exec, ttConfig(newFakeWords(cards)), nil, nil)
			t.Cleanup(tm.internalClose)
			s := &captureSender{}
			ttInit(t, tm, s)
			ttAwaitSpoken(t, s, 3)
			ttAckLast(t, tm, s, 0)
			ttWaitListening(t, s)
			before := s.countOf(TypeTeacherSentence)
			tm.HandleControl(s, mustJSON(t, map[string]any{"type": "read_word", "word_index": index, "current_index": 1}))
			if got := s.countOf(TypeError); got != 0 {
				t.Fatalf("number click rejected: %v", s.controlsOf(t, TypeError))
			}
			ttAwaitSpoken(t, s, before+2)
			if spoken := tts.recordedTexts(); !strings.Contains(spoken[len(spoken)-1], cards[index].Word) {
				t.Fatalf("reading = %v, want selected word %s", spoken, cards[index].Word)
			}
			for _, msg := range s.controlsOf(t, TypeTeacherSentence) {
				if strings.Contains(msg["text"].(string), cards[index].Word) {
					t.Errorf("reading subtitle revealed answer: %v", msg)
				}
			}
			ttAckLast(t, tm, s, 0)
			tm.mu.Lock()
			cursor, version := tm.tutor.currentIndex, tm.tutor.version
			tm.mu.Unlock()
			if cursor != 1 || version != 2 || s.countOf(TypeLessonUpdate) != 0 {
				t.Fatalf("reading changed lesson: cursor=%d version=%d updates=%d", cursor, version, s.countOf(TypeLessonUpdate))
			}
		})
	}
}

func TestReadWordRejectsFutureAndStaleSelection(t *testing.T) {
	tm, s, _, _ := ttIntentFixture(t, ttCards("苹果", "香蕉"), newFakeASR(), newFakeLLM())
	t.Cleanup(tm.internalClose)
	before := s.countOf(TypeTeacherSentence)
	for _, msg := range []map[string]any{
		{"type": "read_word", "word_index": 1, "current_index": 0},
		{"type": "read_word", "word_index": -1, "current_index": 0},
		{"type": "read_word", "word_index": 0, "current_index": 1},
	} {
		tm.HandleControl(s, mustJSON(t, msg))
	}
	if s.countOf(TypeError) != 3 || s.countOf(TypeTeacherSentence) != before {
		t.Fatal("invalid selections must not announce or move the lesson")
	}
}

func TestDictationAdvanceKeepsAllAnswersHidden(t *testing.T) {
	tm, s, _, _ := ttIntentFixture(t, ttCards("苹果", "香蕉"), newFakeASR(asrScript{text: "好了"}), newFakeLLM(intentScript(intentConfirm, "")))
	t.Cleanup(tm.internalClose)
	done := make(chan struct{})
	go func() { tm.processTurn(s, []byte("pcm")); close(done) }()
	ttAwaitSpoken(t, s, 4)
	if s.countOf(TypeWordRevealed) != 0 {
		t.Fatalf("answer was revealed after one word: %v", s.controlsOf(t, TypeWordRevealed))
	}
	tm.mu.Lock()
	masked := tm.tutor.maskedWords()
	tm.mu.Unlock()
	if len(masked) != 2 {
		t.Fatalf("completed word no longer masked in subtitles: %v", masked)
	}
	ttAckLast(t, tm, s, 0)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("confirm did not settle")
	}
}

func TestReadWordKeepsPauseAndCheckVerdicts(t *testing.T) {
	t.Run("paused", func(t *testing.T) {
		tm, s, _, _ := ttIntentFixture(t, ttCards("苹果", "香蕉"), newFakeASR(), newFakeLLM())
		t.Cleanup(tm.internalClose)
		tm.mu.Lock()
		tm.tutor.paused = true
		tm.mu.Unlock()
		before := s.countOf(TypeTeacherSentence)
		tm.HandleControl(s, mustJSON(t, ReadWordMsg{Type: TypeReadWord, WordIndex: 0, CurrentIndex: 0}))
		ttAwaitSpoken(t, s, before+2)
		ttAckLast(t, tm, s, 0)
		if !waitFor(t, time.Second, func() bool {
			tm.mu.Lock()
			defer tm.mu.Unlock()
			return tm.state == StateListening && tm.tutor.paused && tm.wordTimer == nil
		}) {
			t.Fatal("re-reading must keep the lesson paused with no timer")
		}
	})
	t.Run("checking", func(t *testing.T) {
		words := newFakeWords(ttCards("苹果"))
		tm, s, _, _ := ttDriveToCheck(t, ttCards("苹果"), words)
		t.Cleanup(tm.internalClose)
		// #96 U2: the check panel settles to grading and re-reading is
		// refused — the panel rows show the words themselves, and the chip
		// stays honestly disabled on the page (canReadWord excludes grading).
		before := s.countOf(TypeTeacherSentence)
		tm.HandleControl(s, mustJSON(t, ReadWordMsg{Type: TypeReadWord, WordIndex: 0, CurrentIndex: 1}))
		if got := s.countOf(TypeTeacherSentence); got != before {
			t.Fatalf("check-stage re-read spoke %d new sentences, want 0", got-before)
		}
		if s.countOf(TypeError) != 1 {
			t.Fatal("check-stage reading must be refused with a visible error")
		}
		if s.countOf(TypeCheckVerdicts) != 0 || s.countOf(TypeReviewResult) != 0 {
			t.Fatal("check-stage reading must not submit or mark a verdict")
		}
	})
}

func TestReadWordRequiredFields(t *testing.T) {
	for _, raw := range []string{`{"type":"read_word"}`, `{"type":"read_word","word_index":0}`, `{"type":"read_word","current_index":0}`, `{"type":"read_word","word_index":0.5,"current_index":0}`} {
		if _, err := ParseControl([]byte(raw)); err == nil {
			t.Errorf("accepted malformed reading: %s", raw)
		}
	}
	roundTrip(t, ReadWordMsg{Type: TypeReadWord, WordIndex: 1, CurrentIndex: 2}, ReadWordMsg{})
}
