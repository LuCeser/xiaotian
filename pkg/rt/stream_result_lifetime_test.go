package rt

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/xiaotian/home-tutor-agent/pkg/infra"
	"github.com/xiaotian/home-tutor-agent/pkg/mimo"
)

// gatedTTS streams normally until the configured 1-based call, where it
// parks the provider BETWEEN two chunks: the first chunk already went out
// through the real PCM callback, the second waits for the test's release.
// That parks the turn goroutine inside the provider with no lock held —
// the exact window a close or interrupt lands in while audio is in flight.
type gatedTTS struct {
	entered  chan struct{}
	release  chan struct{}
	gateCall int
	mu       sync.Mutex
	calls    int
}

func newGatedTTS(gateCall int) *gatedTTS {
	return &gatedTTS{entered: make(chan struct{}), release: make(chan struct{}), gateCall: gateCall}
}

func (g *gatedTTS) SynthesizeStream(_ context.Context, _ string, onAudio AudioChunkFunc, _ ...mimo.TTSOption) (*mimo.StreamResult, error) {
	g.mu.Lock()
	g.calls++
	gated := g.calls == g.gateCall
	g.mu.Unlock()
	pcm := make([]byte, 320)
	if err := onAudio(pcm, false); err != nil {
		return &mimo.StreamResult{}, err
	}
	if gated {
		close(g.entered)
		<-g.release
		if err := onAudio(pcm, true); err != nil {
			return &mimo.StreamResult{}, err
		}
	}
	return &mimo.StreamResult{}, nil
}

var _ TTSSynthesizer = (*gatedTTS)(nil)

// gatingLogger parks the FIRST rt_event of the given name right after it is
// recorded: the turn goroutine blocks inside Emit with no tm.mu held, so the
// test can close the session between the timeout observation and the turn
// effect that would ship the notice.
type gatingLogger struct {
	*captureLogger
	entered chan struct{}
	release chan struct{}
	event   string
	once    sync.Once
}

func (l *gatingLogger) Info(msg string, fields ...infra.Field) {
	l.captureLogger.Info(msg, fields...)
	if msg != "rt_event" {
		return
	}
	for _, f := range fields {
		if f.Key != "event" || f.Value != l.event {
			continue
		}
		l.once.Do(func() {
			close(l.entered)
			<-l.release
		})
	}
}

// ttSpeakFixture is ttIntentFixture with the caller's TTS and logger: the
// greeting plays out normally (the calls before the gate), and the scripted
// intent turn is the one that parks inside the provider.
func ttSpeakFixture(t *testing.T, tts TTSSynthesizer, log infra.Logger, asr *fakeASR, llm *fakeLLM) (*TurnManager, *captureSender) {
	t.Helper()
	exec := newFakeExecutorNoRow(ttLessonID)
	tm := NewTurnManager(asr, llm, tts, exec, ttConfig(newFakeWords(ttCards("苹果", "香蕉"))), NewEventRecorder(log, "t"), nil)
	s := &captureSender{}
	ttInit(t, tm, s)
	ttAwaitSpoken(t, s, 3) // greeting (hello + count + first word)
	ttAckLast(t, tm, s, 0)
	ttWaitListening(t, s)
	return tm, s
}

// ttGatedSpeakTurn starts the scripted chat turn and returns once its TTS
// call is parked between chunks (first chunk already shipped), plus the
// baselines to compare the late second chunk and audio_end against.
func ttGatedSpeakTurn(t *testing.T, tm *TurnManager, s *captureSender, tts *gatedTTS) (done chan struct{}, audioAfterFirst int, endsBefore int) {
	t.Helper()
	endsBefore = s.countOf(TypeAudioEnd)
	done = ttStartTurn(tm, s)
	ttAwaitGate(t, tts.entered, "gated TTS call was not reached")
	audioAfterFirst = s.audioCount()
	return done, audioAfterFirst, endsBefore
}

// TestStreamResultLifetime pins speakTurn's in-flight stream results to the
// announcement's own identity: a PCM chunk or audio_end arriving after the
// session closed — or after an interrupt bumped the epoch — must not ship
// audio, grow the sample accounting, or touch the new turn's state.
func TestStreamResultLifetime(t *testing.T) {
	t.Run("close while the provider is mid-sentence refuses the late chunk and audio_end", func(t *testing.T) {
		asr := newFakeASR(asrScript{text: "老师你好"})
		llm := newFakeLLM(intentScript(intentChat, "你好呀"))
		// Greeting = 3 sentence calls; the reply turn parks on call 4.
		tm, captured := ttSpeakFixture(t, newGatedTTS(4), &captureLogger{}, asr, llm)
		g := tm.tts.(*gatedTTS)
		done, audioAfterFirst, endsBefore := ttGatedSpeakTurn(t, tm, captured, g)

		tm.handleEndSession(captured)
		close(g.release)
		ttAwaitGate(t, done, "turn did not finish after the close")

		if got := captured.audioCount(); got != audioAfterFirst {
			t.Fatalf("audio chunks = %d, want %d after close", got, audioAfterFirst)
		}
		if got := captured.countOf(TypeAudioEnd); got != endsBefore {
			t.Fatalf("audio ends = %d, want %d after close", got, endsBefore)
		}
		tm.mu.Lock()
		pending, closed := len(tm.pendingPlayback), tm.closed
		tm.mu.Unlock()
		if pending != 0 || !closed {
			t.Fatalf("pending=%d closed=%v, want the close sweep to stand", pending, closed)
		}
	})

	t.Run("interrupt while the provider is mid-sentence refuses the late chunk and audio_end", func(t *testing.T) {
		asr := newFakeASR(asrScript{text: "老师你好"})
		llm := newFakeLLM(intentScript(intentChat, "你好呀"))
		tm, captured := ttSpeakFixture(t, newGatedTTS(4), &captureLogger{}, asr, llm)
		g := tm.tts.(*gatedTTS)
		done, audioAfterFirst, endsBefore := ttGatedSpeakTurn(t, tm, captured, g)
		sentencesBefore := captured.countOf(TypeTeacherSentence)

		tm.HandleControl(captured, mustJSON(t, InterruptMsg{Type: TypeInterrupt}))
		tm.mu.Lock()
		epochAfterInterrupt := tm.epoch
		tm.mu.Unlock()
		close(g.release)
		ttAwaitGate(t, done, "turn did not finish after the interrupt")

		if got := captured.audioCount(); got != audioAfterFirst {
			t.Fatalf("audio chunks = %d, want %d after interrupt", got, audioAfterFirst)
		}
		if got := captured.countOf(TypeAudioEnd); got != endsBefore {
			t.Fatalf("audio ends = %d, want %d after interrupt", got, endsBefore)
		}
		if got := captured.countOf(TypeTeacherSentence); got != sentencesBefore {
			t.Fatalf("teacher sentences = %d, want %d after interrupt", got, sentencesBefore)
		}
		tm.mu.Lock()
		epoch, state, pending := tm.epoch, tm.state, len(tm.pendingPlayback)
		tm.mu.Unlock()
		if epoch != epochAfterInterrupt {
			t.Fatalf("epoch = %d, want the interrupt's %d unmoved by the late stream", epoch, epochAfterInterrupt)
		}
		if state != StateListening || pending != 0 {
			t.Fatalf("state=%q pending=%d, want the interrupt's listening state untouched", state, pending)
		}
	})
}

// TestPlaybackTimeoutLifetime pins the ack-timeout RESULT to the
// announcement's own epoch: a live timeout still reports once and closes in
// order, while a close that wins the race to the fired timer suppresses the
// notice instead of shipping a playback complaint past session_ended.
func TestPlaybackTimeoutLifetime(t *testing.T) {
	t.Run("live timeout still reports and closes in order", func(t *testing.T) {
		asr := newFakeASR(asrScript{text: "老师你好"})
		llm := newFakeLLM(intentScript(intentChat, "你好呀"))
		tm, captured, _, _ := ttIntentFixture(t, ttCards("苹果", "香蕉"), asr, llm)

		done := ttStartTurn(tm, captured)
		select {
		case <-done: // no ack ever arrives: the bounded wait times out for real
		case <-time.After(8 * time.Second):
			t.Fatal("unacked announcement did not time out")
		}

		errs := captured.controlsOf(t, TypeError)
		if len(errs) != 1 || errs[0]["code"] != ErrCodePlaybackAckTimeout {
			t.Fatalf("error messages = %v, want exactly one playback_ack_timeout", errs)
		}
		// 单元 1 review P1: the orderly degraded close is a FULL close — the
		// terminal session_ended carries playback_timeout and the transport
		// terminates with it; the original connection must not outlive the
		// close just because the browser keeps answering heartbeats.
		ended := captured.controlsOf(t, TypeSessionEnded)
		if len(ended) != 1 || ended[0]["reason"] != ReasonPlaybackTimeout {
			t.Fatalf("session_ended = %v, want exactly one carrying playback_timeout", ended)
		}
		captured.mu.Lock()
		terminated, termReason := captured.terminated, captured.reason
		captured.mu.Unlock()
		if !terminated || termReason != nil {
			t.Fatalf("terminated=%v reason=%v, want the transport terminated normally", terminated, termReason)
		}
		tm.mu.Lock()
		closed, state := tm.closed, tm.state
		tm.mu.Unlock()
		if !closed || state != "" {
			t.Fatalf("closed=%v state=%q, want the orderly close to stand", closed, state)
		}
	})

	t.Run("close that wins the raced timer suppresses the timeout notice", func(t *testing.T) {
		asr := newFakeASR(asrScript{text: "老师你好"})
		llm := newFakeLLM(intentScript(intentChat, "你好呀"))
		log := &gatingLogger{
			captureLogger: &captureLogger{}, event: EventPlaybackAckTimeout,
			entered: make(chan struct{}), release: make(chan struct{}),
		}
		// Greeting = 3 TTS calls; the reply's single sentence streams
		// normally and the turn parks in the (real, ~3s) ack wait.
		tm, captured := ttSpeakFixture(t, newFakeTTS(1, 320), log, asr, llm)

		done := ttStartTurn(tm, captured)
		select {
		case <-log.entered: // the timer fired; Emit is parked, mu is free
		case <-time.After(8 * time.Second):
			t.Fatal("ack timeout event never fired")
		}
		tm.handleEndSession(captured)
		close(log.release)
		ttAwaitGate(t, done, "turn did not finish after the close")

		if errs := captured.controlsOf(t, TypeError); len(errs) != 0 {
			t.Fatalf("error messages after close = %v, want none", errs)
		}
		if got := captured.countOf(TypeSessionEnded); got != 1 {
			t.Fatalf("session_ended count = %d, want exactly the close's own", got)
		}
		tm.mu.Lock()
		closed := tm.closed
		tm.mu.Unlock()
		if !closed {
			t.Fatal("session is not closed")
		}
	})
}
