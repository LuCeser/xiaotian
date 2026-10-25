package rt

import "testing"

func TestDispatchLifetime(t *testing.T) {
	tests := []struct {
		name       string
		transcript string
		intent     string
	}{
		{name: "hint", transcript: "给点提示", intent: intentHint},
		{name: "pause", transcript: "暂停一下", intent: intentPause},
		{name: "resume", transcript: "继续吧", intent: intentResume},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			asr := newFakeASR(asrScript{text: tc.transcript})
			llm := newFakeLLM(intentScript(tc.intent, "提示回复"))
			tm, s, _, _ := ttIntentFixture(t, ttCards("苹果", "香蕉"), asr, llm)
			if tc.intent == intentResume {
				tm.mu.Lock()
				tm.tutor.paused = true
				tm.stopWordTimerLocked()
				tm.mu.Unlock()
			}
			_, timerBefore := ttTimerState(tm)
			sentBefore := s.countOf(TypeTeacherSentence)
			hit := make(chan struct{})
			tm.dispatchInterleave = func() {
				tm.HandleControl(s, mustJSON(t, InterruptMsg{Type: TypeInterrupt}))
				close(hit)
			}
			turnSender := &closeAtControlSender{
				captureSender: s,
				tm:            tm,
				target:        TypeTeacherSentence,
				closeDone:     make(chan struct{}),
			}

			done := ttStartTurn(tm, turnSender)
			ttAwaitGate(t, hit, "dispatch probe was not reached")
			ttAwaitGate(t, done, "stale dispatch did not finish")

			tm.mu.Lock()
			paused := tm.tutor.paused
			hints := tm.tutor.perWordHints[0]
			_, hinted := tm.tutor.perWordOutcome[0]
			tm.mu.Unlock()
			switch tc.intent {
			case intentHint:
				if hints != 0 || hinted {
					t.Fatalf("stale hint changed outcome: hints=%d recorded=%v", hints, hinted)
				}
			case intentPause:
				_, timerAfter := ttTimerState(tm)
				if paused || timerAfter != timerBefore {
					t.Fatalf("stale pause changed state: paused=%v timer=%p, want false/%p", paused, timerAfter, timerBefore)
				}
			case intentResume:
				if !paused {
					t.Fatal("stale resume cleared the pause bit")
				}
			}
			if got := s.countOf(TypeTeacherSentence); got != sentBefore {
				t.Fatalf("teacher sentences = %d, want %d after stale dispatch", got, sentBefore)
			}
		})
	}
}
