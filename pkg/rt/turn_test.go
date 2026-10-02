// turn_test.go — fake-driven behavior tests for the turn manager (13-04
// Task 2): tool loop, epoch cancellation, history trimming, half-duplex
// gating, and argument validation. The full binary→VAD→provider→binary
// chain over a real websocket is covered by turn_integration_test.go.
package rt

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xiaotian/home-tutor-agent/pkg/lesson"
	"github.com/xiaotian/home-tutor-agent/pkg/mimo"
)

// ---------------------------------------------------------------------------
// Test doubles: capture sender, fake executor, blocking LLM wrapper
// ---------------------------------------------------------------------------

// captureSender records every control message (as marshaled JSON) and audio
// chunk a Sender would deliver.
type captureSender struct {
	mu         sync.Mutex
	controls   [][]byte
	audioN     int
	audioBytes int
}

func (c *captureSender) SendControl(v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	c.mu.Lock()
	c.controls = append(c.controls, b)
	c.mu.Unlock()
	return nil
}

func (c *captureSender) SendAudio(b []byte) error {
	c.mu.Lock()
	c.audioN++
	c.audioBytes += len(b)
	c.mu.Unlock()
	return nil
}

func (c *captureSender) audioCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.audioN
}

// controlsOf decodes every captured control of the given wire type.
func (c *captureSender) controlsOf(t *testing.T, typ string) []map[string]any {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []map[string]any
	for _, b := range c.controls {
		var probe struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(b, &probe) != nil || probe.Type != typ {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal(b, &m); err != nil {
			t.Fatalf("decode captured control %s: %v", b, err)
		}
		out = append(out, m)
	}
	return out
}

func (c *captureSender) countOf(typ string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for _, b := range c.controls {
		var probe struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(b, &probe) == nil && probe.Type == typ {
			n++
		}
	}
	return n
}

// fakeExecutor is the in-memory LessonExecutor: scripted lesson state plus
// call recording. conflictVersion > 0 makes every AdvanceWord return a
// ConflictError instead of advancing.
type fakeExecutor struct {
	mu              sync.Mutex
	lesson          lesson.Lesson
	ensureCalls     int
	stateCalls      int
	advanceCalls    []fakeAdvanceCall
	conflictVersion int
}

type fakeAdvanceCall struct {
	lessonID        string
	expectedVersion int
	requestID       string
}

func newFakeExecutor(lessonID string, words []string) *fakeExecutor {
	return &fakeExecutor{
		lesson: lesson.Lesson{LessonID: lessonID, Words: words, CurrentIndex: 0, Version: 1},
	}
}

func (f *fakeExecutor) EnsureLesson(_ context.Context, _ string, _ []string) (lesson.Lesson, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ensureCalls++
	return f.lesson, nil
}

func (f *fakeExecutor) GetLessonState(_ context.Context, _ string) (lesson.Lesson, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stateCalls++
	return f.lesson, nil
}

func (f *fakeExecutor) AdvanceWord(_ context.Context, lessonID string, expectedVersion int, requestID string) (lesson.AdvanceResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.advanceCalls = append(f.advanceCalls, fakeAdvanceCall{lessonID, expectedVersion, requestID})
	if f.conflictVersion > 0 {
		return lesson.AdvanceResult{}, &lesson.ConflictError{CurrentVersion: f.conflictVersion}
	}
	f.lesson.CurrentIndex++
	f.lesson.Version++
	return lesson.AdvanceResult{NewIndex: f.lesson.CurrentIndex, NewVersion: f.lesson.Version}, nil
}

func (f *fakeExecutor) advanceCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.advanceCalls)
}

// gatingLLM wraps a fakeLLM and parks the Nth Chat call on a gate (or ctx
// cancellation) — the shape the tool-loop interrupt test needs.
type gatingLLM struct {
	inner     *fakeLLM
	blockOn   int // 1-based call index that blocks
	entered   chan struct{}
	enterOnce sync.Once
	gate      chan struct{}
	calls     atomic.Int32
}

func newGatingLLM(inner *fakeLLM, blockOn int) *gatingLLM {
	return &gatingLLM{
		inner:   inner,
		blockOn: blockOn,
		entered: make(chan struct{}),
		gate:    make(chan struct{}),
	}
}

func (g *gatingLLM) Chat(ctx context.Context, messages []mimo.Message, opts ...mimo.ChatOption) (mimo.ChatResponse, error) {
	if int(g.calls.Add(1)) == g.blockOn {
		g.enterOnce.Do(func() { close(g.entered) })
		select {
		case <-g.gate:
		case <-ctx.Done():
			return mimo.ChatResponse{}, ctx.Err()
		}
	}
	return g.inner.Chat(ctx, messages, opts...)
}

// ---------------------------------------------------------------------------
// Fixtures and helpers
// ---------------------------------------------------------------------------

const (
	ttLessonID   = "L1"
	ttFinalText  = "好，我们继续写下一个词。"
	ttFinalWire  = `{"choices":[{"index":0,"message":{"role":"assistant","content":"` + ttFinalText + `"},"finish_reason":"stop"}]}`
	ttTwoSent    = "这是第一句话。这是第二句话。"
	ttTwoSentWre = `{"choices":[{"index":0,"message":{"role":"assistant","content":"` + ttTwoSent + `"},"finish_reason":"stop"}]}`
)

// ttToolCallWire builds a tool_calls response wire for advance_word.
func ttToolCallWire(args string) string {
	return `{"choices":[{"index":0,"message":{"role":"assistant","content":"","tool_calls":[{"id":"call_abc","type":"function","function":{"name":"advance_word","arguments":"` + args + `"}}]},"finish_reason":"tool_calls"}]}`
}

const ttGoodArgs = `{\"lesson_id\":\"L1\",\"expected_version\":1}`

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}

func ttConfig() TurnManagerConfig {
	return TurnManagerConfig{
		LessonID:        ttLessonID,
		LessonWords:     []string{"苹果", "香蕉", "橘子"},
		MaxToolLoops:    3,
		HistoryMaxTurns: 20,
	}
}

func ttInit(t *testing.T, tm *TurnManager, s *captureSender) {
	t.Helper()
	tm.HandleControl(s, mustJSON(t, SessionInitMsg{
		Type:  TypeSessionInit,
		Audio: AudioSpec{SampleRate: 16000, Format: "s16le", Channels: 1},
	}))
}

// lastAssistantContent returns the newest plain assistant message content
// (no tool_calls, not a tool answer).
func lastAssistantContent(msgs []mimo.Message) (string, bool) {
	for i := len(msgs) - 1; i >= 0; i-- {
		m := msgs[i]
		if m.Role == "assistant" && len(m.ToolCalls) == 0 && m.ToolCallID == "" {
			return m.Content, true
		}
	}
	return "", false
}

// chanClosed polls a closed-channel signal inside waitFor.
func chanClosed(ch chan struct{}) bool {
	select {
	case <-ch:
		return true
	default:
		return false
	}
}

// ---------------------------------------------------------------------------
// session_init / ping
// ---------------------------------------------------------------------------

func TestTurnSessionInitHappyPath(t *testing.T) {
	asr := newFakeASR(asrScript{text: "你好"})
	exec := newFakeExecutor(ttLessonID, []string{"苹果"})
	tm := NewTurnManager(asr, newFakeLLM(), newFakeTTS(1, 320), exec, ttConfig(), nil, nil)
	s := &captureSender{}
	ttInit(t, tm, s)

	ready := s.controlsOf(t, TypeSessionReady)
	if len(ready) != 1 {
		t.Fatalf("session_ready count = %d, want 1 (got %v)", len(ready), ready)
	}
	audio := ready[0]["audio"].(map[string]any)
	if audio["sampleRate"] != float64(16000) || audio["format"] != "s16le" || audio["channels"] != float64(1) {
		t.Errorf("session_ready.audio = %v, want 16000/s16le/1", audio)
	}
	tts := ready[0]["tts"].(map[string]any)
	if tts["sampleRate"] != float64(TTSOutputSampleRate) {
		t.Errorf("session_ready.tts.sampleRate = %v, want %d", tts["sampleRate"], TTSOutputSampleRate)
	}
	l := ready[0]["lesson"].(map[string]any)
	if l["id"] != ttLessonID || l["version"] != float64(1) {
		t.Errorf("session_ready.lesson = %v, want L1 v1", l)
	}
	if s.countOf(TypeState) == 0 {
		t.Error("no state message after session_init")
	}
}

func TestTurnSessionInitDegraded(t *testing.T) {
	tm := NewTurnManager(nil, nil, nil, newFakeExecutor(ttLessonID, nil), ttConfig(), nil, nil)
	s := &captureSender{}
	ttInit(t, tm, s)

	errs := s.controlsOf(t, TypeError)
	if len(errs) != 1 {
		t.Fatalf("error count = %d, want 1", len(errs))
	}
	if errs[0]["code"] != ErrCodeVoiceUnavailable {
		t.Errorf("code = %v, want %s", errs[0]["code"], ErrCodeVoiceUnavailable)
	}
	if errs[0]["recoverable"] != false {
		t.Errorf("recoverable = %v, want false", errs[0]["recoverable"])
	}
	if s.countOf(TypeSessionReady) != 0 {
		t.Error("session_ready sent despite nil providers")
	}
}

func TestTurnSessionInitRejectsBadFormat(t *testing.T) {
	asr := newFakeASR()
	tm := NewTurnManager(asr, newFakeLLM(), newFakeTTS(1, 320), newFakeExecutor(ttLessonID, nil), ttConfig(), nil, nil)
	s := &captureSender{}
	tm.HandleControl(s, mustJSON(t, SessionInitMsg{
		Type:  TypeSessionInit,
		Audio: AudioSpec{SampleRate: 48000, Format: "f32le", Channels: 2},
	}))

	errs := s.controlsOf(t, TypeError)
	if len(errs) != 1 || errs[0]["code"] != ErrCodeUnsupportedFormat {
		t.Fatalf("errors = %v, want one unsupported_format", errs)
	}
}

func TestTurnPingPong(t *testing.T) {
	tm := NewTurnManager(nil, nil, nil, nil, ttConfig(), nil, nil)
	s := &captureSender{}
	tm.HandleControl(s, []byte(`{"type":"ping","t_client_ms":4242}`))

	pongs := s.controlsOf(t, TypePong)
	if len(pongs) != 1 {
		t.Fatalf("pong count = %d, want 1", len(pongs))
	}
	if pongs[0]["t_client_ms"] != float64(4242) {
		t.Errorf("t_client_ms = %v, want 4242", pongs[0]["t_client_ms"])
	}
	if mono, _ := pongs[0]["t_server_mono_ns"].(float64); mono < 0 {
		t.Errorf("t_server_mono_ns = %v, want >= 0", pongs[0]["t_server_mono_ns"])
	}
}

func TestTurnUnknownControlRejected(t *testing.T) {
	tm := NewTurnManager(nil, nil, nil, nil, ttConfig(), nil, nil)
	s := &captureSender{}
	tm.HandleControl(s, []byte(`{"type":"teacher_sentence"}`))
	errs := s.controlsOf(t, TypeError)
	if len(errs) != 1 || errs[0]["code"] != ErrCodeInvalidMessage {
		t.Fatalf("errors = %v, want one invalid_message", errs)
	}
}

// ---------------------------------------------------------------------------
// Tool loop (先执行后播报)
// ---------------------------------------------------------------------------

// TestTurnToolLoopExecutesBeforeSpeaking: fakeLLM scripts
// [tool_calls(advance_word) → final text]. The executor runs exactly once
// under the tool_call id; the second Chat sees the role:"tool" answer; the
// lesson update and only THEN the spoken sentence go out.
func TestTurnToolLoopExecutesBeforeSpeaking(t *testing.T) {
	asr := newFakeASR(asrScript{text: "写完了"})
	llm := newFakeLLM(
		llmScript{wire: ttToolCallWire(ttGoodArgs)},
		llmScript{wire: ttFinalWire},
	)
	tts := newFakeTTS(2, 320)
	exec := newFakeExecutor(ttLessonID, []string{"苹果", "香蕉"})
	tm := NewTurnManager(asr, llm, tts, exec, ttConfig(), nil, nil)
	s := &captureSender{}
	ttInit(t, tm, s)

	tm.processTurn(s, []byte("pcm"))

	if got := exec.advanceCount(); got != 1 {
		t.Fatalf("executor advance calls = %d, want exactly 1", got)
	}
	call := exec.advanceCalls[0]
	if call.lessonID != ttLessonID || call.expectedVersion != 1 || call.requestID != "call_abc" {
		t.Errorf("advance call = %+v, want L1/1/call_abc (request_id must be the tool_call id)", call)
	}

	if llm.callCount() != 2 {
		t.Fatalf("llm calls = %d, want 2", llm.callCount())
	}
	second := llm.calls[1].messages
	var toolMsg *mimo.Message
	var replayMsg *mimo.Message
	for i := range second {
		if second[i].Role == "tool" && second[i].ToolCallID == "call_abc" {
			m := second[i]
			toolMsg = &m
		}
		if second[i].Role == "assistant" && len(second[i].ToolCalls) == 1 {
			m := second[i]
			replayMsg = &m
		}
	}
	if toolMsg == nil {
		t.Fatalf("second Chat history has no role:tool answer for call_abc: %+v", second)
	}
	if !strings.Contains(toolMsg.Content, `"ok":true`) || !strings.Contains(toolMsg.Content, `"new_index":1`) {
		t.Errorf("tool result = %s, want ok:true new_index:1", toolMsg.Content)
	}
	if replayMsg == nil || replayMsg.ToolCalls[0].ID != "call_abc" {
		t.Errorf("assistant tool_calls replay missing: %+v", second)
	}

	updates := s.controlsOf(t, TypeLessonUpdate)
	if len(updates) != 1 {
		t.Fatalf("lesson_update count = %d, want 1", len(updates))
	}
	if updates[0]["version"] != float64(2) || updates[0]["request_id"] != "call_abc" {
		t.Errorf("lesson_update = %v, want version 2 / call_abc", updates[0])
	}

	sentences := s.controlsOf(t, TypeTeacherSentence)
	if len(sentences) != 1 || sentences[0]["text"] != ttFinalText {
		t.Fatalf("teacher_sentence = %v, want the final text after tools", sentences)
	}
	if s.audioCount() == 0 {
		t.Error("no audio sent for the final sentence")
	}
	if s.countOf(TypeAudioStart) < 1 {
		t.Error("no audio_start message")
	}
	if s.countOf(TypeState) < 1 {
		t.Error("no final listening state message")
	}
}

// TestTurnToolLoopConflictFedBack: a ConflictError becomes a
// version_conflict tool result — verbal resolution, no retry write, no
// panic, and the turn still speaks a final text.
func TestTurnToolLoopConflictFedBack(t *testing.T) {
	asr := newFakeASR(asrScript{text: "下一个"})
	llm := newFakeLLM(
		llmScript{wire: ttToolCallWire(ttGoodArgs)},
		llmScript{wire: ttFinalWire},
	)
	exec := newFakeExecutor(ttLessonID, []string{"苹果"})
	exec.conflictVersion = 7
	tm := NewTurnManager(asr, llm, newFakeTTS(1, 320), exec, ttConfig(), nil, nil)
	s := &captureSender{}
	ttInit(t, tm, s)

	tm.processTurn(s, []byte("pcm"))

	if got := exec.advanceCount(); got != 1 {
		t.Fatalf("advance calls = %d, want 1", got)
	}
	if llm.callCount() < 2 {
		t.Fatalf("llm calls = %d, want 2", llm.callCount())
	}
	second := llm.calls[1].messages
	found := false
	for _, m := range second {
		if m.Role == "tool" && strings.Contains(m.Content, `"error":"version_conflict"`) && strings.Contains(m.Content, `"current_version":7`) {
			found = true
		}
	}
	if !found {
		t.Fatalf("no version_conflict tool result fed back: %+v", second)
	}
	if s.countOf(TypeLessonUpdate) != 0 {
		t.Error("lesson_update sent for a failed write")
	}
	if s.countOf(TypeTeacherSentence) != 1 {
		t.Error("turn did not speak a final text after the conflict")
	}
}

// ---------------------------------------------------------------------------
// Argument validation (T-13-03 / T-13-05b / M-5)
// ---------------------------------------------------------------------------

func TestTurnValidateToolArgs(t *testing.T) {
	ver := 3
	if a, err := validateToolArgs(ttLessonID, "advance_word", `{"lesson_id":"L1","expected_version":3}`); err != nil || a == nil || *a.ExpectedVersion != ver {
		t.Fatalf("parsed args = %+v err=%v, want expected_version 3", a, err)
	}
	if _, err := validateToolArgs(ttLessonID, "advance_word", `{"lesson_id":"L1","expected_version":3}`); err != nil {
		t.Fatalf("valid advance_word rejected: %v", err)
	}
	if _, err := validateToolArgs(ttLessonID, "get_lesson_state", `{"lesson_id":"L1"}`); err != nil {
		t.Fatalf("valid get_lesson_state rejected: %v", err)
	}

	bad := []struct {
		name, args string
	}{
		{"broken json", `{"lesson_id":"L1",`},
		{"unknown tool args-shape", `{"lesson_id":"L1","expected_version":1}`}, // used with unknown name below
		{"empty lesson_id", `{"lesson_id":"","expected_version":1}`},
		{"foreign lesson_id", `{"lesson_id":"OTHER","expected_version":1}`},
		{"missing expected_version", `{"lesson_id":"L1"}`},
		{"string version", `{"lesson_id":"L1","expected_version":"1"}`},
		{"float version", `{"lesson_id":"L1","expected_version":1.5}`},
		{"zero version", `{"lesson_id":"L1","expected_version":0}`},
	}
	for _, c := range bad {
		name := c.name
		args := c.args
		tool := "advance_word"
		if name == "unknown tool args-shape" {
			tool = "delete_everything"
		}
		if _, err := validateToolArgs(ttLessonID, tool, args); err == nil {
			t.Errorf("%s: accepted, want rejection", name)
		}
	}
}

// TestTurnToolLoopInvalidArgsNotExecuted: broken-JSON arguments and a
// foreign lesson_id both feed invalid_arguments back to the model WITHOUT
// any executor call (M-5).
func TestTurnToolLoopInvalidArgsNotExecuted(t *testing.T) {
	cases := []struct {
		name string
		args string
	}{
		{"broken json", `{\"lesson_id\":\"L1\",\"expected_version\":1`},
		{"foreign lesson id", `{\"lesson_id\":\"OTHER\",\"expected_version\":1}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			asr := newFakeASR(asrScript{text: "下一个"})
			llm := newFakeLLM(
				llmScript{wire: ttToolCallWire(tc.args)},
				llmScript{wire: ttFinalWire},
			)
			exec := newFakeExecutor(ttLessonID, []string{"苹果"})
			tm := NewTurnManager(asr, llm, newFakeTTS(1, 320), exec, ttConfig(), nil, nil)
			s := &captureSender{}
			ttInit(t, tm, s)

			tm.processTurn(s, []byte("pcm"))

			if exec.advanceCount()+exec.stateCalls != 0 {
				t.Fatalf("executor touched: advance=%d state=%d, want 0/0", exec.advanceCount(), exec.stateCalls)
			}
			if llm.callCount() < 2 {
				t.Fatalf("llm calls = %d, want 2 (invalid arguments must be fed back for a retry)", llm.callCount())
			}
			var toolContent string
			for _, m := range llm.calls[1].messages {
				if m.Role == "tool" && m.ToolCallID == "call_abc" {
					toolContent = m.Content
				}
			}
			if !strings.Contains(toolContent, "invalid_arguments") {
				t.Fatalf("tool result = %q, want invalid_arguments feedback", toolContent)
			}
			// The model gets its one retry and the turn completes.
			if s.countOf(TypeTeacherSentence) != 1 {
				t.Error("turn did not complete after invalid-arguments feedback")
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Epoch cancellation
// ---------------------------------------------------------------------------

// TestTurnInterruptStopsAudioAndBumpsEpoch: with the TTS streaming
// continuously, an interrupt must (a) bump the epoch, (b) stop ALL further
// audio bytes to the sender, (c) ack with new_epoch > old_epoch.
func TestTurnInterruptStopsAudioAndBumpsEpoch(t *testing.T) {
	asr := newFakeASR(asrScript{text: "你好"})
	llm := newFakeLLM(llmScript{wire: ttFinalWire})
	tts := newFakeTTS(0, 320)
	tts.streamUntilCancel = true
	tts.delay = 20 * time.Millisecond
	tm := NewTurnManager(asr, llm, tts, newFakeExecutor(ttLessonID, nil), ttConfig(), nil, nil)
	s := &captureSender{}
	ttInit(t, tm, s)

	done := make(chan struct{})
	go func() {
		tm.processTurn(s, []byte("pcm"))
		close(done)
	}()

	if !waitFor(t, 5*time.Second, func() bool { return s.audioCount() >= 3 }) {
		t.Fatal("TTS stream never produced chunks")
	}

	tm.HandleControl(s, mustJSON(t, InterruptMsg{Type: TypeInterrupt, Epoch: 1}))
	snapshot := s.audioCount()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("processTurn did not return after interrupt")
	}
	if got := s.audioCount(); got != snapshot {
		t.Fatalf("audio after interrupt: %d chunks before ack, %d after — late audio leaked", snapshot, got)
	}

	acks := s.controlsOf(t, TypeInterruptAck)
	if len(acks) != 1 {
		t.Fatalf("interrupt_ack count = %d, want 1", len(acks))
	}
	if acks[0]["new_epoch"].(float64) <= acks[0]["old_epoch"].(float64) {
		t.Errorf("epoch = old %v -> new %v, want new > old", acks[0]["old_epoch"], acks[0]["new_epoch"])
	}
}

// TestTurnInterruptDuringToolLoopKeepsToolRecords: interrupt while the
// second Chat hop is blocked → no further Chat/TTS issues for that turn,
// the assistant(tool_calls) + role:"tool" records stay in history verbatim,
// and the next turn's Chat sees them followed directly by its user message
// (no spoken-text message in between).
func TestTurnInterruptDuringToolLoopKeepsToolRecords(t *testing.T) {
	asr := newFakeASR(asrScript{text: "下一个"}, asrScript{text: "还在吗"})
	inner := newFakeLLM(
		llmScript{wire: ttToolCallWire(ttGoodArgs)},
		llmScript{wire: ttFinalWire},
	)
	llm := newGatingLLM(inner, 2)
	tts := newFakeTTS(1, 320)
	exec := newFakeExecutor(ttLessonID, []string{"苹果"})
	tm := NewTurnManager(asr, llm, tts, exec, ttConfig(), nil, nil)
	s := &captureSender{}
	ttInit(t, tm, s)

	done := make(chan struct{})
	go func() {
		tm.processTurn(s, []byte("pcm"))
		close(done)
	}()

	if !waitFor(t, 5*time.Second, func() bool { return exec.advanceCount() == 1 && chanClosed(llm.entered) }) {
		t.Fatal("tool never executed or second Chat never started")
	}
	// Second hop is now parked. Interrupt: ctx cancellation unblocks it.
	tm.HandleControl(s, mustJSON(t, InterruptMsg{Type: TypeInterrupt}))
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("processTurn did not return after interrupt")
	}

	if got := len(tts.recordedTexts()); got != 0 {
		t.Fatalf("tts calls after interrupted tool loop = %d, want 0", got)
	}
	if exec.advanceCount() != 1 {
		t.Errorf("advance calls = %d, want 1 (exactly-once semantics)", exec.advanceCount())
	}

	// The next turn proceeds on a history that preserves the tool facts.
	tm.processTurn(s, []byte("pcm"))
	if inner.callCount() < 2 {
		t.Fatalf("llm calls = %d, want 2", inner.callCount())
	}
	msgs := inner.calls[1].messages
	if len(msgs) < 5 {
		t.Fatalf("next Chat history = %+v, want [system user assistant(tool_calls) tool user]", msgs)
	}
	if msgs[4].Role != "user" {
		t.Fatalf("message 4 = %+v, want the new user turn", msgs[4])
	}
	var sawToolReplay, sawToolAnswer bool
	for _, m := range msgs[:4] {
		if m.Role == "assistant" && len(m.ToolCalls) == 1 && m.ToolCalls[0].ID == "call_abc" {
			sawToolReplay = true
		}
		if m.Role == "tool" && m.ToolCallID == "call_abc" {
			sawToolAnswer = true
		}
		if m.Role == "assistant" && len(m.ToolCalls) == 0 && m.ToolCallID == "" && m.Content != "" {
			t.Fatalf("spoken-text message survived a tool-loop interrupt: %+v", m)
		}
	}
	if !sawToolReplay || !sawToolAnswer {
		t.Fatalf("tool records missing after interrupt: replay=%v answer=%v", sawToolReplay, sawToolAnswer)
	}
}

// ---------------------------------------------------------------------------
// History trimming to actually-played sentences (M-2 / Pitfall 9)
// ---------------------------------------------------------------------------

// trimFixture runs one speaking turn (two sentences, slow TTS) until the
// given ack set is applied and an interrupt lands mid-speech, then runs a
// follow-up turn and returns the follow-up Chat's message slice.
func trimFixture(t *testing.T, ackFirst bool, lastSentenceID string) []mimo.Message {
	t.Helper()
	asr := newFakeASR(asrScript{text: "开始吧"}, asrScript{text: "继续"})
	llm := newFakeLLM(
		llmScript{wire: ttTwoSentWre},
		llmScript{wire: ttFinalWire},
	)
	tts := newFakeTTS(1, 320)
	tts.delay = 250 * time.Millisecond
	tm := NewTurnManager(asr, llm, tts, newFakeExecutor(ttLessonID, nil), ttConfig(), nil, nil)
	s := &captureSender{}
	ttInit(t, tm, s)

	done := make(chan struct{})
	go func() {
		tm.processTurn(s, []byte("pcm"))
		close(done)
	}()
	if !waitFor(t, 5*time.Second, func() bool { return s.countOf(TypeTeacherSentence) >= 2 }) {
		t.Fatal("both teacher sentences never went out")
	}
	if ackFirst {
		tm.HandleControl(s, mustJSON(t, SentenceAckMsg{Type: TypeSentenceAck, SentenceID: "t1-s0", Epoch: 0}))
	}
	tm.HandleControl(s, mustJSON(t, InterruptMsg{
		Type:           TypeInterrupt,
		Epoch:          0,
		LastSentenceID: lastSentenceID,
		PlayedUntilMs:  1234,
	}))
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("speaking turn did not return after interrupt")
	}

	// Follow-up turn: its Chat reveals the trimmed history.
	tm.processTurn(s, []byte("pcm"))
	if llm.callCount() < 2 {
		t.Fatalf("llm calls = %d, want 2", llm.callCount())
	}
	return llm.calls[1].messages
}

func TestTurnInterruptTrimsHistoryToPlayedSentences(t *testing.T) {
	t.Run("only first sentence acked", func(t *testing.T) {
		msgs := trimFixture(t, true, "")
		content, ok := lastAssistantContent(msgs)
		if !ok {
			t.Fatalf("no assistant message in trimmed history: %+v", msgs)
		}
		if content != "这是第一句话。" {
			t.Errorf("trimmed content = %q, want only the first sentence", content)
		}
	})
	t.Run("zero sentences acked removes message", func(t *testing.T) {
		msgs := trimFixture(t, false, "")
		if _, ok := lastAssistantContent(msgs); ok {
			t.Fatalf("spoken-text message survived zero-played interrupt: %+v", msgs)
		}
	})
	t.Run("ack union interrupt last_sentence_id", func(t *testing.T) {
		msgs := trimFixture(t, true, "t1-s1")
		content, ok := lastAssistantContent(msgs)
		if !ok {
			t.Fatalf("no assistant message in union-trimmed history: %+v", msgs)
		}
		if content != ttTwoSent {
			t.Errorf("union content = %q, want both sentences", content)
		}
	})
}

// ---------------------------------------------------------------------------
// Half-duplex gating (Pattern 3)
// ---------------------------------------------------------------------------

// TestTurnHalfDuplexGatesUplinkWhileSpeaking: while the teacher speaks,
// uplink audio is dropped — the ASR must not be called for it.
func TestTurnHalfDuplexGatesUplinkWhileSpeaking(t *testing.T) {
	asr := newFakeASR(asrScript{text: "你好"})
	llm := newFakeLLM(llmScript{wire: ttFinalWire})
	tts := newFakeTTS(0, 320)
	tts.streamUntilCancel = true
	tts.delay = 50 * time.Millisecond
	tm := NewTurnManager(asr, llm, tts, newFakeExecutor(ttLessonID, nil), ttConfig(), nil, nil)
	s := &captureSender{}
	ttInit(t, tm, s)

	done := make(chan struct{})
	go func() {
		tm.processTurn(s, []byte("pcm"))
		close(done)
	}()
	if !waitFor(t, 5*time.Second, func() bool { return s.audioCount() >= 1 }) {
		t.Fatal("teacher never started speaking")
	}

	// Loud uplink during speaking: dropped by the gate, never transcribed.
	loud := vadSine(12000, 200)
	tm.IngestAudio(s, loud)
	tm.IngestAudio(s, loud)
	time.Sleep(150 * time.Millisecond)

	asr.mu.Lock()
	fed := len(asr.fedBytes)
	asr.mu.Unlock()
	if fed != 1 {
		t.Errorf("asr calls = %d, want 1 (only the turn's own utterance; gated uplink must not transcribe)", fed)
	}

	// Clean up the streaming turn.
	tm.HandleControl(s, mustJSON(t, InterruptMsg{Type: TypeInterrupt}))
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("turn did not end after interrupt")
	}
}

// ---------------------------------------------------------------------------
// splitSentences
// ---------------------------------------------------------------------------

func TestTurnSplitSentences(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"你好。今天写三个词。", []string{"你好。", "今天写三个词。"}},
		{"真的吗？太棒了！", []string{"真的吗？", "太棒了！"}},
		{"先写第一个；写完告诉我。", []string{"先写第一个；", "写完告诉我。"}},
		{"这是一段没有标点的很长很长的话", []string{"这是一段没有标点的很长很长的话"}},
		{"Hello. How are you?", []string{"Hello.", "How are you?"}},
		{"第一行\n第二行", []string{"第一行", "第二行"}},
		{"只有一句。", []string{"只有一句。"}},
		{"", nil},
		{"   ", nil},
	}
	for _, c := range cases {
		got := splitSentences(c.in)
		if len(got) != len(c.want) {
			t.Errorf("splitSentences(%q) = %v, want %v", c.in, got, c.want)
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("splitSentences(%q)[%d] = %q, want %q", c.in, i, got[i], c.want[i])
			}
		}
	}
}

// ---------------------------------------------------------------------------
// System prompt values (CONTEXT prohibitions)
// ---------------------------------------------------------------------------

// TestTurnSystemPromptCarriesLockedRules: the persona must forbid invented
// progress, forbid advancing on silence, require confirmation before
// advance_word, and forbid punitive wording (13-CONTEXT prohibitions).
func TestTurnSystemPromptCarriesLockedRules(t *testing.T) {
	p := teacherSystemPrompt
	for _, want := range []string{"get_lesson_state", "advance_word", "沉默", "不会写", "明确"} {
		if !strings.Contains(p, want) {
			t.Errorf("system prompt missing %q — CONTEXT locked rule absent", want)
		}
	}
	if !strings.Contains(p, "严禁") {
		t.Error("system prompt lacks explicit prohibitions (严禁)")
	}
	if len(lessonToolDefs) != 2 {
		t.Errorf("lessonToolDefs = %d tools, want 2", len(lessonToolDefs))
	}
}
