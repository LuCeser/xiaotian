// turn_thinking_timeout_test.go — Issue #94 U5: the 「思考中」 turn deadline
// (rt.processingTimeout). Every test enters through the REAL production
// entry (IngestAudio → VAD close → processTurn goroutine), never a direct
// helper call: the deadline must be observable on the wire a child sees.
// The two fail-window tests stage the interrupt/close INSIDE failTurn's
// race window by hooking the error-event emission — the one seam that sits
// lock-free both before the fix (between the epoch check and the state
// flip) and after it (after the whole critical section).
package rt

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/xiaotian/home-tutor-agent/pkg/infra"
	"github.com/xiaotian/home-tutor-agent/pkg/mimo"
)

// thinkingHangLLM wedges every Chat until the turn ctx dies, then answers
// with the ctx error itself — the hung upstream of the 「思考中」 hang. Unlike
// the scripted fakeLLM it honors ctx, so the deadline (not the test) ends
// the call.
type thinkingHangLLM struct {
	mu    sync.Mutex
	calls int
}

func (l *thinkingHangLLM) Chat(ctx context.Context, _ []mimo.Message, _ ...mimo.ChatOption) (mimo.ChatResponse, error) {
	l.mu.Lock()
	l.calls++
	l.mu.Unlock()
	<-ctx.Done()
	return mimo.ChatResponse{}, ctx.Err()
}

func (l *thinkingHangLLM) callCount() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.calls
}

// thinkingHangThenChatLLM wedges the FIRST Chat until its ctx dies (turn 1's
// 「思考中」 hang) and answers every later Chat with a fixed verdict — the
// fresh post-interrupt turn must be allowed to complete normally.
type thinkingHangThenChatLLM struct {
	mu      sync.Mutex
	calls   int
	verdict mimo.ChatResponse
}

func newThinkingHangThenChatLLM(t *testing.T) *thinkingHangThenChatLLM {
	t.Helper()
	script := intentScript(intentChat, "好的，我们继续。")
	return &thinkingHangThenChatLLM{verdict: wireChatResponse(t, script.wire)}
}

func (l *thinkingHangThenChatLLM) Chat(ctx context.Context, _ []mimo.Message, _ ...mimo.ChatOption) (mimo.ChatResponse, error) {
	l.mu.Lock()
	l.calls++
	n := l.calls
	l.mu.Unlock()
	if n == 1 {
		<-ctx.Done()
		return mimo.ChatResponse{}, ctx.Err()
	}
	return l.verdict, nil
}

func (l *thinkingHangThenChatLLM) callCount() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.calls
}

// failWindowLogger routes every entry to the embedded capture and, exactly
// once, fires a test hook the moment the thinking_timeout rt_event error is
// emitted. On the unfixed failTurn that emission sat INSIDE the race window
// (after the epoch check, before the state flip), so the hook lands the
// interrupt/close exactly where the P1 interleaving lives; on the fixed one
// it runs after the whole critical section and must change nothing.
type failWindowLogger struct {
	*captureLogger
	mu    sync.Mutex
	fired bool
	hook  func()
}

func (l *failWindowLogger) Info(msg string, fields ...infra.Field) {
	if msg == "rt_event" {
		for _, f := range fields {
			if f.Key != "code" || f.Value != ErrCodeThinkingTimeout {
				continue
			}
			l.mu.Lock()
			fired := l.fired
			l.fired = true
			l.mu.Unlock()
			if !fired {
				l.hook() // entries the hook itself emits see fired=true
			}
			break
		}
	}
	l.captureLogger.Info(msg, fields...)
}

// ttThinkingFixture boots a one-word classroom with a short 「思考中」
// deadline and fakes the VAD clocks open (the same 1ms grace/tail +
// expired-echo-tail trick the VAD gating tests use) so an IngestAudio
// utterance really reaches processTurn. The logger feeds BOTH the recorder
// and the manager — the correlated timeout Warn must land on it too.
func ttThinkingFixture(t *testing.T, timeout time.Duration, asr ASRProvider, llm LLMProvider, tts TTSSynthesizer, log infra.Logger) (*TurnManager, *captureSender) {
	t.Helper()
	tm := NewTurnManager(asr, llm, tts, newFakeExecutorNoRow(ttLessonID),
		ttConfigWith(newFakeWords(ttCards("苹果")), func(c *TurnManagerConfig) {
			c.ProcessingTimeout = timeout
			c.VADStartGraceMs = 1
			c.VADEchoTailMs = 1
		}), NewEventRecorder(log, "t"), log)
	s := &captureSender{}
	ttInit(t, tm, s)
	ttAwaitSpoken(t, s, 3)
	ttAckLast(t, tm, s, 0)
	ttWaitListening(t, s)
	tm.mu.Lock()
	tm.echoTailUntil = time.Now().Add(-time.Millisecond)
	tm.mu.Unlock()
	return tm, s
}

// ttLastError returns the newest ErrorMsg on the wire, or nil.
func ttLastError(t *testing.T, s *captureSender) map[string]any {
	t.Helper()
	errs := s.controlsOf(t, TypeError)
	if len(errs) == 0 {
		return nil
	}
	return errs[len(errs)-1]
}

// ttLastStateValue returns the newest StateMsg value ("" when none).
func ttLastStateValue(t *testing.T, s *captureSender) string {
	t.Helper()
	states := s.controlsOf(t, TypeState)
	if len(states) == 0 {
		return ""
	}
	v, _ := states[len(states)-1]["value"].(string)
	return v
}

// ttThinkingTimeoutEvent finds the rt_event error entry carrying the
// thinking_timeout code, or nil.
func ttThinkingTimeoutEvent(t *testing.T, log *captureLogger) *captureEntry {
	t.Helper()
	for _, e := range log.snapshot() {
		if e.msg != "rt_event" {
			continue
		}
		if fieldValue(t, e, "event") != EventError {
			continue
		}
		if code, _ := fieldValue(t, e, "code").(string); code == ErrCodeThinkingTimeout {
			found := e
			return &found
		}
	}
	return nil
}

// thinkingTimeoutWarnMsg is the correlated Warn's message (Issue #94 U5).
const thinkingTimeoutWarnMsg = "dictation turn processing deadline exceeded"

// ttThinkingTimeoutWarn finds the correlated timeout Warn entry, or nil.
func ttThinkingTimeoutWarn(t *testing.T, log *captureLogger) *captureEntry {
	t.Helper()
	for _, e := range log.snapshot() {
		if e.msg == thinkingTimeoutWarnMsg {
			found := e
			return &found
		}
	}
	return nil
}

// ttAssertTimeoutWarn asserts the correlated Warn entry exists and carries
// the acceptance fields: op names the wedged step, error_class=timeout, and
// session/turn ids correlate it with the rt_event stream.
func ttAssertTimeoutWarn(t *testing.T, log *captureLogger, step string, turnID uint64) {
	t.Helper()
	w := ttThinkingTimeoutWarn(t, log)
	if w == nil {
		t.Fatal("no correlated timeout Warn was logged")
	}
	if got, _ := fieldValue(t, *w, "op").(string); got != step {
		t.Errorf("warn op = %q, want %q", got, step)
	}
	if got, _ := fieldValue(t, *w, "error_class").(string); got != "timeout" {
		t.Errorf("warn error_class = %q, want timeout", got)
	}
	if sid, _ := fieldValue(t, *w, "session_id").(string); sid != "t" {
		t.Errorf("warn session_id = %q, want the recorder's \"t\"", sid)
	}
	if tid, _ := fieldValue(t, *w, "turn_id").(uint64); tid != turnID {
		t.Errorf("warn turn_id = %d, want %d", tid, turnID)
	}
}

// ttWireEntry is one control message on the wire, in send order — only the
// fields the ordering assertions need (type; StateMsg value).
type ttWireEntry struct {
	typ string
	val string
}

// ttWire snapshots the wire in send order.
func ttWire(t *testing.T, s *captureSender) []ttWireEntry {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]ttWireEntry, 0, len(s.controls))
	for _, b := range s.controls {
		var probe struct {
			Type  string `json:"type"`
			Value string `json:"value"`
		}
		if err := json.Unmarshal(b, &probe); err != nil {
			t.Fatalf("decode captured control %s: %v", b, err)
		}
		out = append(out, ttWireEntry{typ: probe.Type, val: probe.Value})
	}
	return out
}

// TestProcessTurnThinkingTimeoutAfterHangingChat: the classification Chat
// wedges; the turn deadline (250ms here) must surface as the thinking_timeout
// ErrorMsg, return the classroom to listening, leave the ASR un-retried, the
// classification UN-retried (one Chat total) and the TTS silent (no spoken
// fallback), and log the rt_event error with the session/turn ids and
// op=chat beside the correlated Warn. Reverting the WithTimeout back to a
// plain cancel leaves the hang unbounded and this test red by its own 5s;
// reverting the classifyTurn no-retry guard turns the call-count assertion
// red.
func TestProcessTurnThinkingTimeoutAfterHangingChat(t *testing.T) {
	asr := newFakeASR(asrScript{text: "你好"})
	llm := &thinkingHangLLM{}
	tts := newFakeTTS(1, 320)
	log := &captureLogger{}
	tm, s := ttThinkingFixture(t, 250*time.Millisecond, asr, llm, tts, log)
	spokenBefore := len(tts.recordedTexts())

	tm.IngestAudio(s, ttUtterance())

	if !waitFor(t, 5*time.Second, func() bool {
		e := ttLastError(t, s)
		return e != nil && e["code"] == ErrCodeThinkingTimeout
	}) {
		t.Fatal("thinking_timeout ErrorMsg never arrived — the hung Chat outlived the test bound")
	}
	e := ttLastError(t, s)
	if msg, _ := e["message"].(string); msg != thinkingTimeoutNotice {
		t.Errorf("error message = %q, want %q", msg, thinkingTimeoutNotice)
	}
	if rec, _ := e["recoverable"].(bool); !rec {
		t.Error("error recoverable = false, want true (the child can simply speak again)")
	}
	if !waitFor(t, 5*time.Second, func() bool { return ttLastStateValue(t, s) == StateListening }) {
		t.Fatalf("state after the timeout = %q, want listening (the classroom must reopen)", ttLastStateValue(t, s))
	}
	if got := asr.fedCount(); got != 1 {
		t.Errorf("asr calls = %d, want 1 (an expired ctx must not run the invalid-response retry)", got)
	}
	if got := llm.callCount(); got != 1 {
		t.Errorf("llm calls = %d, want 1 (an expired ctx must not run the classification retry)", got)
	}
	if got := len(tts.recordedTexts()); got != spokenBefore {
		t.Errorf("tts calls %d → %d, want unchanged (no spoken fallback on the expired ctx)", spokenBefore, got)
	}
	ev := ttThinkingTimeoutEvent(t, log)
	if ev == nil {
		t.Fatal("no rt_event error entry carries code=thinking_timeout")
	}
	if sid, _ := fieldValue(t, *ev, "session_id").(string); sid != "t" {
		t.Errorf("event session_id = %q, want the recorder's \"t\"", sid)
	}
	if tid, _ := fieldValue(t, *ev, "turn_id").(uint64); tid == 0 {
		t.Error("event turn_id missing/zero, want the turn's id")
	}
	if got, _ := fieldValue(t, *ev, "op").(string); got != "chat" {
		t.Errorf("event op = %q, want \"chat\" (the deadline hit the classification step)", got)
	}
	if got, _ := fieldValue(t, *ev, "error_class").(string); got != "timeout" {
		t.Errorf("event error_class = %q, want timeout", got)
	}
	tid, _ := fieldValue(t, *ev, "turn_id").(uint64)
	ttAssertTimeoutWarn(t, log, "chat", tid)
}

// TestProcessTurnThinkingTimeoutAfterHangingASRRetry: the first ASR answers
// ErrInvalidResponse and the ONE allowed retry hangs past the deadline (the
// fake's delay ignores ctx on purpose). The deadline check must cover the
// retry too: the child sees thinking_timeout with the fixed notice and
// op=asr — never the generic provider_failed with the ASR wording — and no
// classification Chat ever leaves. Removing the retry-path check turns this
// red through the provider_failed code.
func TestProcessTurnThinkingTimeoutAfterHangingASRRetry(t *testing.T) {
	asr := newFakeASR(
		asrScript{err: mimo.ErrInvalidResponse},
		asrScript{err: fmt.Errorf("asr upstream gone"), delay: 600 * time.Millisecond},
	)
	llm := newFakeLLM(intentScript(intentChat, "好的，我们继续。"))
	log := &captureLogger{}
	tm, s := ttThinkingFixture(t, 250*time.Millisecond, asr, llm, newFakeTTS(1, 320), log)

	tm.IngestAudio(s, ttUtterance())

	if !waitFor(t, 5*time.Second, func() bool {
		e := ttLastError(t, s)
		return e != nil && e["code"] == ErrCodeThinkingTimeout
	}) {
		t.Fatalf("thinking_timeout ErrorMsg never arrived; last error = %v", ttLastError(t, s))
	}
	if msg, _ := ttLastError(t, s)["message"].(string); msg != thinkingTimeoutNotice {
		t.Errorf("error message = %q, want %q", msg, thinkingTimeoutNotice)
	}
	if got := asr.fedCount(); got != 2 {
		t.Errorf("asr calls = %d, want 2 (invalid response, then the one retry)", got)
	}
	if got := llm.callCount(); got != 0 {
		t.Errorf("llm calls = %d, want 0 (an expired ctx must never reach classification)", got)
	}
	if !waitFor(t, 5*time.Second, func() bool { return ttLastStateValue(t, s) == StateListening }) {
		t.Fatalf("state after the timeout = %q, want listening", ttLastStateValue(t, s))
	}
	ev := ttThinkingTimeoutEvent(t, log)
	if ev == nil {
		t.Fatal("no rt_event error entry carries code=thinking_timeout")
	}
	if got, _ := fieldValue(t, *ev, "op").(string); got != "asr" {
		t.Errorf("event op = %q, want \"asr\" (the deadline hit the ASR retry step)", got)
	}
	tid, _ := fieldValue(t, *ev, "turn_id").(uint64)
	ttAssertTimeoutWarn(t, log, "asr", tid)
}

// TestProcessTurnThinkingTimeoutRaceVerdictInsideDeadline: a classification
// that lands comfortably INSIDE the deadline is the winning path — the model
// reply is spoken and acked, no thinking_timeout may ever appear, and the
// classroom settles back to listening.
func TestProcessTurnThinkingTimeoutRaceVerdictInsideDeadline(t *testing.T) {
	asr := newFakeASR(asrScript{text: "你好"})
	llm := newFakeLLM(intentScript(intentChat, "好的，我们继续。"))
	tts := newFakeTTS(1, 320)
	log := &captureLogger{}
	tm, s := ttThinkingFixture(t, 800*time.Millisecond, asr, llm, tts, log)
	sentBefore := s.countOf(TypeTeacherSentence)

	entered, release := llm.gateNextChat()
	tm.IngestAudio(s, ttUtterance())
	<-entered // the classification request is out; the model stays silent
	time.Sleep(150 * time.Millisecond)
	close(release) // the verdict lands ~650ms before the deadline

	if !waitFor(t, 5*time.Second, func() bool { return s.countOf(TypeTeacherSentence) == sentBefore+1 }) {
		t.Fatal("the model reply never spoke — the winning verdict was not dispatched")
	}
	ttAckLast(t, tm, s, 0)
	if !waitFor(t, 5*time.Second, func() bool { return ttLastStateValue(t, s) == StateListening }) {
		t.Fatalf("state after the turn = %q, want listening", ttLastStateValue(t, s))
	}
	if e := ttLastError(t, s); e != nil {
		t.Errorf("unexpected ErrorMsg on the winning turn: %v", e)
	}
	if ev := ttThinkingTimeoutEvent(t, log); ev != nil {
		t.Error("a thinking_timeout event fired on a turn that beat the deadline")
	}
	texts := tts.recordedTexts()
	if len(texts) == 0 || texts[len(texts)-1] != "好的，我们继续。" {
		t.Errorf("tts spoke %q, want the model reply 好的，我们继续。 (not the spoken fallback)", texts)
	}
}

// TestProcessTurnThinkingTimeoutVerdictCrossesDeadlineWhileSpeaking: the
// verdict lands inside the deadline and dispatches; the deadline then
// crosses while the reply is still unacked (speaking). The winning normal
// turn must not be retro-failed — no thinking_timeout error, event or Warn
// may appear — and the ack settles the classroom to listening as usual.
func TestProcessTurnThinkingTimeoutVerdictCrossesDeadlineWhileSpeaking(t *testing.T) {
	asr := newFakeASR(asrScript{text: "你好"})
	llm := newFakeLLM(intentScript(intentChat, "好的，我们继续。"))
	tts := newFakeTTS(1, 320)
	log := &captureLogger{}
	tm, s := ttThinkingFixture(t, 250*time.Millisecond, asr, llm, tts, log)
	sentBefore := s.countOf(TypeTeacherSentence)

	tm.IngestAudio(s, ttUtterance())
	if !waitFor(t, 5*time.Second, func() bool { return s.countOf(TypeTeacherSentence) == sentBefore+1 }) {
		t.Fatal("the verdict inside the deadline never spoke")
	}
	time.Sleep(400 * time.Millisecond) // the 250ms deadline crosses mid-speaking, reply unacked

	if e := ttLastError(t, s); e != nil {
		t.Fatalf("the winning turn was retro-failed after its deadline crossed: %v", e)
	}
	if ev := ttThinkingTimeoutEvent(t, log); ev != nil {
		t.Error("a thinking_timeout event fired after the winning turn had dispatched")
	}
	if w := ttThinkingTimeoutWarn(t, log); w != nil {
		t.Error("a timeout Warn fired after the winning turn had dispatched")
	}
	ttAckLast(t, tm, s, 0)
	if !waitFor(t, 5*time.Second, func() bool { return ttLastStateValue(t, s) == StateListening }) {
		t.Fatalf("state after the ack = %q, want listening", ttLastStateValue(t, s))
	}
	// Re-checked AFTER the settle: a retro-fitted post-dispatch deadline
	// check would fire only once the announcement completed.
	if e := ttLastError(t, s); e != nil {
		t.Fatalf("an error followed the completed turn: %v", e)
	}
	if ev := ttThinkingTimeoutEvent(t, log); ev != nil {
		t.Error("a thinking_timeout event fired after the winning turn had dispatched")
	}
	if w := ttThinkingTimeoutWarn(t, log); w != nil {
		t.Error("a timeout Warn fired after the winning turn had dispatched")
	}
	texts := tts.recordedTexts()
	if len(texts) == 0 || texts[len(texts)-1] != "好的，我们继续。" {
		t.Errorf("tts spoke %q, want the model reply 好的，我们继续。", texts)
	}
}

// TestProcessTurnLateThinkingTimeoutSparesInterruptedTurn: the deadline hits
// while the classification is held, then the child interrupts (epoch bump +
// ack), and only THEN does the verdict land — late, on an expired ctx. The
// interrupt owns the UX: no thinking_timeout (no error at all) may follow
// the interrupt ack, and the listening state the interrupt set stands.
func TestProcessTurnLateThinkingTimeoutSparesInterruptedTurn(t *testing.T) {
	asr := newFakeASR(asrScript{text: "你好"})
	llm := newFakeLLM(intentScript(intentChat, "好的，我们继续。"))
	log := &captureLogger{}
	tm, s := ttThinkingFixture(t, 400*time.Millisecond, asr, llm, newFakeTTS(1, 320), log)

	entered, release := llm.gateNextChat()
	tm.IngestAudio(s, ttUtterance())
	<-entered
	time.Sleep(600 * time.Millisecond) // the 400ms deadline passes mid-flight
	tm.HandleControl(s, mustJSON(t, InterruptMsg{Type: TypeInterrupt}))
	if !waitFor(t, 5*time.Second, func() bool { return s.countOf(TypeInterruptAck) == 1 }) {
		t.Fatal("interrupt_ack never arrived")
	}
	close(release) // the verdict finally returns — expired ctx, stale epoch
	if !waitFor(t, 5*time.Second, func() bool { return llm.callCount() >= 1 }) {
		t.Fatal("the held Chat never returned")
	}
	time.Sleep(150 * time.Millisecond) // let a would-be late timeout fire

	if e := ttLastError(t, s); e != nil {
		t.Fatalf("late deadline observation errored an interrupted turn: %v", e)
	}
	if ev := ttThinkingTimeoutEvent(t, log); ev != nil {
		t.Error("a thinking_timeout event fired after the interrupt owned the turn")
	}
	if got := ttLastStateValue(t, s); got != StateListening {
		t.Errorf("state = %q, want the interrupt's listening to stand", got)
	}
	if got := s.countOf(TypeSessionEnded); got != 0 {
		t.Errorf("session_ended = %d, want 0 (the interrupt must not end the session)", got)
	}
}

// TestProcessTurnThinkingTimeoutInterruptInsideFailWindow (P1): the deadline
// fires, and the interrupt lands INSIDE failTurn's race window — after the
// epoch check observed the turn current, before the state flip — staged
// deterministically by hooking the error-event emission. The interrupt owns
// the UX: its ack closes the episode, a FRESH turn then enters processing,
// and the stale failTurn must neither emit anything behind the interrupt
// ack nor flip the fresh turn out of processing. Reverting failTurn to a
// check-then-act split turns this red on both halves.
func TestProcessTurnThinkingTimeoutInterruptInsideFailWindow(t *testing.T) {
	capture := &captureLogger{}
	log := &failWindowLogger{captureLogger: capture}
	asr := newFakeASR(asrScript{text: "你好"}, asrScript{text: "你好"})
	llm := newThinkingHangThenChatLLM(t)
	tts := newFakeTTS(1, 320)
	tm, s := ttThinkingFixture(t, 250*time.Millisecond, asr, llm, tts, log)
	sentBefore := s.countOf(TypeTeacherSentence)
	interruptJSON := mustJSON(t, InterruptMsg{Type: TypeInterrupt})
	var armMu sync.Mutex // guards the late-armed ASR hold channels
	var asrEntered, asrRelease chan struct{}
	hookDone := make(chan struct{})
	var hookedState string
	var hookedEpoch uint64
	log.hook = func() {
		defer close(hookDone)
		// The interrupt completes inside the window: epoch bumped, ack on
		// the wire, classroom listening. Then the FRESH turn enters
		// processing and parks in ASR — the exact state a stale failTurn
		// must never touch.
		tm.HandleControl(s, interruptJSON)
		tm.IngestAudio(s, ttUtterance())
		armMu.Lock()
		entered := asrEntered
		armMu.Unlock()
		<-entered
		tm.mu.Lock()
		hookedState = tm.state
		hookedEpoch = tm.epoch
		tm.mu.Unlock()
	}

	tm.IngestAudio(s, ttUtterance()) // turn 1: the Chat wedges to the deadline
	if !waitFor(t, 5*time.Second, func() bool { return llm.callCount() >= 1 }) {
		t.Fatal("turn 1 never reached its hung Chat")
	}
	// Turn 1's Transcribe has returned by now — the hold parks the FRESH
	// turn's ASR, never turn 1's.
	armMu.Lock()
	asrEntered, asrRelease = asr.gateNextTranscribe()
	armMu.Unlock()

	select {
	case <-hookDone:
	case <-time.After(5 * time.Second):
		t.Fatal("the fail-window hook never ran — the thinking_timeout error event never fired")
	}
	if hookedState != StateProcessing {
		t.Fatalf("the fresh turn's state at the hook = %q, want processing", hookedState)
	}
	time.Sleep(150 * time.Millisecond) // room for a would-be stale failTurn to land
	tm.mu.Lock()
	st := tm.state
	tm.mu.Unlock()
	if st != StateProcessing {
		t.Errorf("a stale failTurn flipped the fresh processing turn to %q (P1: the epoch check and the side effects must be one critical section)", st)
	}

	wire := ttWire(t, s)
	lastStart, errAt, ackAt := -1, -1, -1
	for i, e := range wire {
		switch e.typ {
		case TypeTurnStart:
			lastStart = i
		case TypeError:
			errAt = i
		case TypeInterruptAck:
			ackAt = i
		}
	}
	if lastStart < 0 {
		t.Fatal("the fresh turn never announced turn_start")
	}
	for _, e := range wire[lastStart+1:] {
		if e.typ == TypeState && e.val == StateListening {
			t.Error("a stale StateMsg(listening) landed behind the fresh turn's turn_start (P1)")
		}
	}
	if errAt < 0 || ackAt < 0 || errAt > ackAt {
		t.Errorf("wire order: thinking_timeout error at %d, interrupt_ack at %d — the error must never trail the ack", errAt, ackAt)
	}

	// The fresh turn completes normally once its ASR returns: exactly one
	// error total (turn 1's thinking_timeout), the model reply speaks, the
	// ack settles the classroom, the session lives on.
	close(asrRelease)
	if !waitFor(t, 5*time.Second, func() bool { return s.countOf(TypeTeacherSentence) == sentBefore+1 }) {
		t.Fatal("the fresh turn's reply never spoke — the interrupt's turn was collateral damage")
	}
	ttAckLast(t, tm, s, hookedEpoch) // the fresh turn's sentences live at the post-interrupt epoch
	if !waitFor(t, 5*time.Second, func() bool { return ttLastStateValue(t, s) == StateListening }) {
		t.Fatalf("state after the fresh turn = %q, want listening", ttLastStateValue(t, s))
	}
	if got := s.countOf(TypeError); got != 1 {
		t.Errorf("error count = %d, want exactly 1 (turn 1's thinking_timeout)", got)
	}
	if got := llm.callCount(); got != 2 {
		t.Errorf("llm calls = %d, want 2 (turn 1's hung Chat + turn 2's verdict)", got)
	}
	if got := s.countOf(TypeSessionEnded); got != 0 {
		t.Errorf("session_ended = %d, want 0", got)
	}
}

// TestProcessTurnThinkingTimeoutCloseInsideFailWindow (P1): the close lands
// inside the same failTurn race window. Nothing — no error, no state — may
// follow session_ended on the wire: a check-then-act failTurn still flips
// its state and enqueues a listening StateMsg BEHIND session_ended, which
// this test catches.
func TestProcessTurnThinkingTimeoutCloseInsideFailWindow(t *testing.T) {
	capture := &captureLogger{}
	log := &failWindowLogger{captureLogger: capture}
	asr := newFakeASR(asrScript{text: "你好"})
	llm := &thinkingHangLLM{}
	tm, s := ttThinkingFixture(t, 250*time.Millisecond, asr, llm, newFakeTTS(1, 320), log)
	endJSON := mustJSON(t, EndSessionMsg{Type: TypeEndSession})
	hookDone := make(chan struct{})
	log.hook = func() {
		defer close(hookDone)
		tm.HandleControl(s, endJSON) // close inside the window: session_ended ships
	}

	tm.IngestAudio(s, ttUtterance()) // the Chat wedges to the deadline

	select {
	case <-hookDone:
	case <-time.After(5 * time.Second):
		t.Fatal("the fail-window hook never ran — the thinking_timeout error event never fired")
	}
	time.Sleep(150 * time.Millisecond) // room for a would-be stale failTurn to land

	tm.mu.Lock()
	closed := tm.closed
	tm.mu.Unlock()
	if !closed {
		t.Error("the session is not closed after end_session")
	}
	wire := ttWire(t, s)
	endedAt, errAt := -1, -1
	for i, e := range wire {
		switch e.typ {
		case TypeSessionEnded:
			endedAt = i
		case TypeError:
			errAt = i
		}
	}
	if endedAt < 0 {
		t.Fatal("session_ended never arrived")
	}
	for _, e := range wire[endedAt+1:] {
		t.Errorf("control %q followed session_ended on the wire (P1: a stale failTurn spoke after the close)", e.typ)
	}
	if errAt < 0 || errAt > endedAt {
		t.Errorf("wire order: thinking_timeout error at %d, session_ended at %d — the error must precede the close", errAt, endedAt)
	}
	if got := s.countOf(TypeError); got != 1 {
		t.Errorf("error count = %d, want exactly 1 (the thinking_timeout itself)", got)
	}
}
