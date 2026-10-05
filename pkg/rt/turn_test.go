// turn_test.go — fake-driven behavior tests for the dictation FSM turn
// manager (13-08): opening greeting, confirm/idempotent/conflict advance
// branches, legacy/completed lesson paths, sentence-ack identity, and the
// pending-before-audio-start registration. The full binary→VAD→classify→
// dispatch chain over a real websocket is covered by
// turn_integration_test.go (13-08 Task 3).
package rt

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xiaotian/home-tutor-agent/pkg/lesson"
)

// ---------------------------------------------------------------------------
// Test doubles: capture sender, fake executor
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

// listeningCount counts StateMsg values equal to listening.
func (c *captureSender) listeningCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for _, b := range c.controls {
		var m StateMsg
		if json.Unmarshal(b, &m) == nil && m.Value == StateListening {
			n++
		}
	}
	return n
}

// fakeExecutor is the in-memory LessonExecutor: scripted lesson state plus
// call recording. conflictVersion > 0 makes every AdvanceWord return a
// ConflictError; requestResults replays the first stored result for a
// duplicate requestID (Idempotent=true), mirroring the tool_requests table.
type fakeExecutor struct {
	mu              sync.Mutex
	lesson          lesson.Lesson
	exists          bool
	ensureCalls     int
	ensureCardsCalls int
	stateCalls      int
	advanceCalls    []fakeAdvanceCall
	conflictVersion int
	requestResults  map[string]lesson.AdvanceResult
}

type fakeAdvanceCall struct {
	lessonID        string
	expectedVersion int
	requestID       string
}

func cardWords(cards []lesson.Word) []string {
	words := make([]string, 0, len(cards))
	for _, c := range cards {
		words = append(words, c.Word)
	}
	return words
}

// newFakeExecutor builds an executor whose lesson row EXISTS with the given
// cards (empty cards = the legacy-row case).
func newFakeExecutor(lessonID string, cards []lesson.Word) *fakeExecutor {
	return &fakeExecutor{
		lesson:         lesson.Lesson{LessonID: lessonID, Words: cardWords(cards), Cards: cards, CurrentIndex: 0, Version: 1},
		exists:         true,
		requestResults: map[string]lesson.AdvanceResult{},
	}
}

// newFakeExecutorNoRow builds an executor with NO lesson row
// (GetLessonState → ErrLessonNotFound, so session_init takes the fetch path).
func newFakeExecutorNoRow(lessonID string) *fakeExecutor {
	e := newFakeExecutor(lessonID, nil)
	e.exists = false
	return e
}

func (f *fakeExecutor) EnsureLesson(_ context.Context, _ string, _ []string) (lesson.Lesson, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ensureCalls++
	return f.lesson, nil
}

func (f *fakeExecutor) EnsureLessonCards(_ context.Context, _ string, cards []lesson.Word) (lesson.Lesson, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ensureCardsCalls++
	f.lesson.Cards = cards
	f.lesson.Words = cardWords(cards)
	f.exists = true
	return f.lesson, nil
}

func (f *fakeExecutor) GetLessonState(_ context.Context, _ string) (lesson.Lesson, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stateCalls++
	if !f.exists {
		return lesson.Lesson{}, lesson.ErrLessonNotFound
	}
	return f.lesson, nil
}

func (f *fakeExecutor) AdvanceWord(_ context.Context, lessonID string, expectedVersion int, requestID string) (lesson.AdvanceResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.advanceCalls = append(f.advanceCalls, fakeAdvanceCall{lessonID, expectedVersion, requestID})
	if stored, ok := f.requestResults[requestID]; ok {
		stored.Idempotent = true
		return stored, nil
	}
	if f.conflictVersion > 0 {
		return lesson.AdvanceResult{}, &lesson.ConflictError{CurrentVersion: f.conflictVersion}
	}
	f.lesson.CurrentIndex++
	f.lesson.Version++
	res := lesson.AdvanceResult{NewIndex: f.lesson.CurrentIndex, NewVersion: f.lesson.Version}
	f.requestResults[requestID] = res
	return res, nil
}

func (f *fakeExecutor) advanceCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.advanceCalls)
}

func (f *fakeExecutor) setRow(index, version int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lesson.CurrentIndex = index
	f.lesson.Version = version
}

// ---------------------------------------------------------------------------
// Fixtures and helpers
// ---------------------------------------------------------------------------

const ttLessonID = "L1"

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}

func ttConfig(words WordSource) TurnManagerConfig {
	return TurnManagerConfig{LessonID: ttLessonID, Words: words}
}

func ttInit(t *testing.T, tm *TurnManager, s *captureSender) {
	t.Helper()
	tm.HandleControl(s, mustJSON(t, SessionInitMsg{
		Type:  TypeSessionInit,
		Audio: AudioSpec{SampleRate: 16000, Format: "s16le", Channels: 1},
	}))
}

// ttAwaitSpoken waits until at least n teacher sentences went out.
func ttAwaitSpoken(t *testing.T, s *captureSender, n int) {
	t.Helper()
	if !waitFor(t, 5*time.Second, func() bool { return s.countOf(TypeTeacherSentence) >= n }) {
		t.Fatalf("teacher sentences = %d, want >= %d", s.countOf(TypeTeacherSentence), n)
	}
}

// ttAckLast sends the sentence_ack for the most recently announced sentence
// (the announcement's final sentence) and returns its id.
func ttAckLast(t *testing.T, tm *TurnManager, s *captureSender, epoch uint64) string {
	t.Helper()
	sents := s.controlsOf(t, TypeTeacherSentence)
	if len(sents) == 0 {
		t.Fatal("no teacher sentence to ack")
	}
	sid, _ := sents[len(sents)-1]["sentence_id"].(string)
	tm.HandleControl(s, mustJSON(t, SentenceAckMsg{Type: TypeSentenceAck, SentenceID: sid, Epoch: epoch}))
	return sid
}

// ttEventCount counts recorded rt_event entries of the given event name.
func ttEventCount(t *testing.T, log *captureLogger, event string) int {
	t.Helper()
	n := 0
	for _, e := range log.snapshot() {
		if e.msg != "rt_event" {
			continue
		}
		for _, f := range e.fields {
			if f.Key == "event" && f.Value == event {
				n++
			}
		}
	}
	return n
}

// ttWaitListening waits for the FSM to hand control back after an ack.
func ttWaitListening(t *testing.T, s *captureSender) {
	t.Helper()
	if !waitFor(t, 5*time.Second, func() bool { return s.listeningCount() > 0 }) {
		t.Fatal("FSM never returned to listening after the valid ack")
	}
}

// ---------------------------------------------------------------------------
// session_init / ping
// ---------------------------------------------------------------------------

func TestTurnSessionInitHappyPath(t *testing.T) {
	words := newFakeWords(ttCards("苹果", "香蕉", "橘子"))
	exec := newFakeExecutorNoRow(ttLessonID)
	tm := NewTurnManager(newFakeASR(), newFakeLLM(), newFakeTTS(1, 320), exec, ttConfig(words), nil, nil)
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
	gotWords := l["words"].([]any)
	if len(gotWords) != 3 || gotWords[0] != "苹果" {
		t.Errorf("session_ready.lesson.words = %v, want [苹果 香蕉 橘子]", gotWords)
	}
	if exec.ensureCardsCalls != 1 {
		t.Errorf("EnsureLessonCards calls = %d, want 1", exec.ensureCardsCalls)
	}
	if words.fetchCount() != 1 {
		t.Errorf("FetchDueWords calls = %d, want 1", words.fetchCount())
	}
}

func TestTurnSessionInitDegraded(t *testing.T) {
	tm := NewTurnManager(nil, nil, nil, newFakeExecutor(ttLessonID, ttCards("苹果")), ttConfig(newFakeWords(ttCards("苹果"))), nil, nil)
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

func TestTurnSessionInitNoWordSource(t *testing.T) {
	tm := NewTurnManager(newFakeASR(), newFakeLLM(), newFakeTTS(1, 320), newFakeExecutor(ttLessonID, ttCards("苹果")), TurnManagerConfig{LessonID: ttLessonID}, nil, nil)
	s := &captureSender{}
	ttInit(t, tm, s)

	errs := s.controlsOf(t, TypeError)
	if len(errs) != 1 || errs[0]["code"] != ErrCodeLessonUnavailable {
		t.Fatalf("errors = %v, want one lesson_unavailable", errs)
	}
	if s.countOf(TypeSessionReady) != 0 {
		t.Error("session_ready sent without a WordSource")
	}
}

func TestTurnSessionInitRejectsBadFormat(t *testing.T) {
	tm := NewTurnManager(newFakeASR(), newFakeLLM(), newFakeTTS(1, 320), newFakeExecutorNoRow(ttLessonID), ttConfig(newFakeWords(ttCards("苹果"))), nil, nil)
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
	tm := NewTurnManager(nil, nil, nil, nil, ttConfig(nil), nil, nil)
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
	tm := NewTurnManager(nil, nil, nil, nil, ttConfig(nil), nil, nil)
	s := &captureSender{}
	tm.HandleControl(s, []byte(`{"type":"teacher_sentence"}`))
	errs := s.controlsOf(t, TypeError)
	if len(errs) != 1 || errs[0]["code"] != ErrCodeInvalidMessage {
		t.Fatalf("errors = %v, want one invalid_message", errs)
	}
}

// ---------------------------------------------------------------------------
// Dictation FSM behavior (13-08 Task 1)
// ---------------------------------------------------------------------------

// TestDictationOpeningGreetsAndAnnounces: session_ready carries the due
// word table, the teacher speaks the opening + FIRST word with ZERO LLM
// calls, and the FSM returns to listening (plus word_announced) only after
// the greeting's valid final ack.
func TestDictationOpeningGreetsAndAnnounces(t *testing.T) {
	log := &captureLogger{}
	words := newFakeWords(ttCards("苹果", "香蕉"))
	exec := newFakeExecutorNoRow(ttLessonID)
	tts := newFakeTTS(1, 320)
	tm := NewTurnManager(newFakeASR(), newFakeLLM(), tts, exec, ttConfig(words), NewEventRecorder(log, "t"), nil)
	s := &captureSender{}
	ttInit(t, tm, s)

	ready := s.controlsOf(t, TypeSessionReady)
	if len(ready) != 1 {
		t.Fatalf("session_ready count = %d, want 1", len(ready))
	}
	l := ready[0]["lesson"].(map[string]any)
	if got := l["words"].([]any); len(got) != 2 {
		t.Errorf("lesson.words = %v, want 2 due words", got)
	}

	// Greeting (3 sentences: hello + word count + first word) goes out with
	// no LLM call.
	ttAwaitSpoken(t, s, 3)
	if llm := tm.llm.(*fakeLLM); llm.callCount() != 0 {
		t.Errorf("llm calls during greeting = %d, want 0 (opening is template-only)", llm.callCount())
	}
	sents := s.controlsOf(t, TypeTeacherSentence)
	hello, _ := sents[0]["text"].(string)
	count, _ := sents[1]["text"].(string)
	firstWord, _ := sents[len(sents)-1]["text"].(string)
	if !contains(hello, "你好") {
		t.Errorf("greeting hello = %q, want the teacher hello", hello)
	}
	if !contains(count, "2 个词") {
		t.Errorf("greeting word count = %q, want the word count", count)
	}
	if !contains(firstWord, "苹果") || !contains(firstWord, "第 1 个词") {
		t.Errorf("greeting first word = %q, want 第 1 个词 苹果", firstWord)
	}

	// No listening before the ack: TTS completion and audio_end never lift
	// the playback gate.
	if s.listeningCount() != 0 {
		t.Errorf("listening before ack = %d, want 0", s.listeningCount())
	}

	ttAckLast(t, tm, s, 0)
	ttWaitListening(t, s)
	if ttEventCount(t, log, EventWordAnnounced) != 1 {
		t.Errorf("word_announced events = %d, want 1", ttEventCount(t, log, EventWordAnnounced))
	}
}

// TestDictationConfirmAdvancesAndAnnouncesNext: one classification Chat
// returns confirm; the FIRST AdvanceWord commit records the outcome, sends
// lesson_update with the FSM-derived requestID, and announces the NEXT word.
func TestDictationConfirmAdvancesAndAnnouncesNext(t *testing.T) {
	log := &captureLogger{}
	words := newFakeWords(ttCards("苹果", "香蕉", "橘子"))
	exec := newFakeExecutorNoRow(ttLessonID)
	asr := newFakeASR(asrScript{text: "好了"})
	llm := newFakeLLM(intentScript(intentConfirm, ""))
	tm := NewTurnManager(asr, llm, newFakeTTS(1, 320), exec, ttConfig(words), NewEventRecorder(log, "t"), nil)
	s := &captureSender{}
	ttInit(t, tm, s)
	ttAwaitSpoken(t, s, 3)
	ttAckLast(t, tm, s, 0)
	ttWaitListening(t, s)

	// processTurn blocks inside the next-word announcement's ack wait —
	// drive it on a goroutine and ack the announcement when it appears.
	done := make(chan struct{})
	go func() {
		tm.processTurn(s, []byte("pcm"))
		close(done)
	}()
	ttAwaitSpoken(t, s, 4)
	ttAckLast(t, tm, s, 0)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("confirm turn did not settle after the announce ack")
	}

	if exec.advanceCount() != 1 {
		t.Fatalf("advance calls = %d, want 1", exec.advanceCount())
	}
	call := exec.advanceCalls[0]
	if call.requestID != "L1-advance-0" || call.expectedVersion != 1 || call.lessonID != ttLessonID {
		t.Errorf("advance call = %+v, want L1-advance-0 @ v1", call)
	}
	updates := s.controlsOf(t, TypeLessonUpdate)
	if len(updates) != 1 {
		t.Fatalf("lesson_update count = %d, want 1", len(updates))
	}
	if updates[0]["version"] != float64(2) || updates[0]["current_index"] != float64(1) || updates[0]["request_id"] != "L1-advance-0" {
		t.Errorf("lesson_update = %v, want v2 idx1 L1-advance-0", updates[0])
	}
	if ttEventCount(t, log, EventWordAdvanced) != 1 {
		t.Errorf("word_advanced events = %d, want 1", ttEventCount(t, log, EventWordAdvanced))
	}
	if llm.callCount() != 1 {
		t.Errorf("llm calls = %d, want 1 (single classification call)", llm.callCount())
	}

	// Next word announced (already acked above); the ack armed
	// word_announced for the NEW index.
	sents := s.controlsOf(t, TypeTeacherSentence)
	last, _ := sents[len(sents)-1]["text"].(string)
	if !contains(last, "香蕉") || !contains(last, "第 2 个词") {
		t.Errorf("next announcement = %q, want 第 2 个词 香蕉", last)
	}
	if !waitFor(t, 5*time.Second, func() bool { return ttEventCount(t, log, EventWordAnnounced) == 2 }) {
		t.Fatalf("word_announced events = %d, want 2 (greeting + next word)", ttEventCount(t, log, EventWordAnnounced))
	}
}

// TestDictationDoubleConfirmIdempotent: a late confirm re-using the SAME
// captured target replays Idempotent=true — the local cursor aligns to the
// FIRST result and nothing else happens: no outcome write, no message, no
// narration, no event.
func TestDictationDoubleConfirmIdempotent(t *testing.T) {
	log := &captureLogger{}
	words := newFakeWords(ttCards("苹果", "香蕉", "橘子"))
	exec := newFakeExecutorNoRow(ttLessonID)
	asr := newFakeASR(asrScript{text: "好了"})
	llm := newFakeLLM(intentScript(intentConfirm, ""))
	tm := NewTurnManager(asr, llm, newFakeTTS(1, 320), exec, ttConfig(words), NewEventRecorder(log, "t"), nil)
	s := &captureSender{}
	ttInit(t, tm, s)
	ttAwaitSpoken(t, s, 3)
	ttAckLast(t, tm, s, 0)
	ttWaitListening(t, s)

	done := make(chan struct{})
	go func() {
		tm.processTurn(s, []byte("pcm"))
		close(done)
	}()
	ttAwaitSpoken(t, s, 4)
	ttAckLast(t, tm, s, 0)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("first confirm never settled after the announce ack")
	}
	before := map[string]int{
		"lesson_update":   s.countOf(TypeLessonUpdate),
		"teacher_sentence": s.countOf(TypeTeacherSentence),
		"word_advanced":   ttEventCount(t, log, EventWordAdvanced),
		"review":          ttEventCount(t, log, EventReviewWritten),
	}

	// The late duplicate confirm carries the ORIGINAL captured target.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	tm.confirmTarget(s, ctx, cancel, 0, 0, "", TurnTarget{WordIndex: 0, ExpectedVersion: 1})

	if exec.advanceCount() != 2 {
		t.Fatalf("advance calls = %d, want 2 (the duplicate still hits the executor)", exec.advanceCount())
	}
	if exec.advanceCalls[1].requestID != "L1-advance-0" {
		t.Errorf("duplicate requestID = %q, want L1-advance-0", exec.advanceCalls[1].requestID)
	}
	if got := s.countOf(TypeLessonUpdate); got != before["lesson_update"] {
		t.Errorf("lesson_update after duplicate = %d, want %d (no new message)", got, before["lesson_update"])
	}
	if got := s.countOf(TypeTeacherSentence); got != before["teacher_sentence"] {
		t.Errorf("teacher_sentence after duplicate = %d, want %d (no re-announcement)", got, before["teacher_sentence"])
	}
	if got := ttEventCount(t, log, EventWordAdvanced); got != before["word_advanced"] {
		t.Errorf("word_advanced after duplicate = %d, want %d", got, before["word_advanced"])
	}
	if got := ttEventCount(t, log, EventReviewWritten); got != before["review"] {
		t.Errorf("review_written after duplicate = %d, want %d (no completion)", got, before["review"])
	}
	if s.listeningCount() == 0 {
		t.Error("duplicate confirm did not settle back to listening")
	}
}

// TestDictationVersionConflictAlignsWithoutSideEffects: a version conflict
// read-backs and aligns the local cursor — no retry write, no message, no
// narration, no completion.
func TestDictationVersionConflictAlignsWithoutSideEffects(t *testing.T) {
	log := &captureLogger{}
	words := newFakeWords(ttCards("苹果"))
	exec := newFakeExecutorNoRow(ttLessonID)
	exec.conflictVersion = 7
	asr := newFakeASR(asrScript{text: "好了"})
	llm := newFakeLLM(intentScript(intentConfirm, ""))
	tm := NewTurnManager(asr, llm, newFakeTTS(1, 320), exec, ttConfig(words), NewEventRecorder(log, "t"), nil)
	s := &captureSender{}
	ttInit(t, tm, s)
	ttAwaitSpoken(t, s, 2)
	ttAckLast(t, tm, s, 0)
	ttWaitListening(t, s)
	greetingSents := s.countOf(TypeTeacherSentence)

	tm.processTurn(s, []byte("pcm"))

	if exec.advanceCount() != 1 {
		t.Fatalf("advance calls = %d, want 1 (no retry write)", exec.advanceCount())
	}
	exec.mu.Lock()
	states := exec.stateCalls
	exec.mu.Unlock()
	if states == 0 {
		t.Error("conflict branch never read the lesson state back for alignment")
	}
	if s.countOf(TypeLessonUpdate) != 0 {
		t.Error("lesson_update sent for a conflicted write")
	}
	if got := s.countOf(TypeTeacherSentence); got != greetingSents {
		t.Errorf("teacher_sentence after conflict = %d, want %d (no narration)", got, greetingSents)
	}
	if ttEventCount(t, log, EventWordAdvanced) != 0 {
		t.Error("word_advanced emitted for a conflicted write")
	}
	ttWaitListening(t, s)
}

// TestLegacyEmptyCardsIsLessonUnavailable: a pre-existing lessons row with
// EMPTY cards (legacy r0-test) fails visibly — never a re-RunDue, never a
// fake backfill.
func TestLegacyEmptyCardsIsLessonUnavailable(t *testing.T) {
	log := &captureLogger{}
	words := newFakeWords(ttCards("苹果"))
	exec := newFakeExecutor(ttLessonID, nil) // row exists, cards empty
	tm := NewTurnManager(newFakeASR(), newFakeLLM(), newFakeTTS(1, 320), exec, ttConfig(words), NewEventRecorder(log, "t"), nil)
	s := &captureSender{}
	ttInit(t, tm, s)

	errs := s.controlsOf(t, TypeError)
	if len(errs) != 1 || errs[0]["code"] != ErrCodeLessonUnavailable {
		t.Fatalf("errors = %v, want one lesson_unavailable", errs)
	}
	if s.countOf(TypeSessionReady) != 0 {
		t.Error("session_ready sent for a legacy empty-cards row")
	}
	if words.fetchCount() != 0 {
		t.Errorf("FetchDueWords calls = %d, want 0 (no re-RunDue for an existing row)", words.fetchCount())
	}
	if exec.ensureCardsCalls != 0 {
		t.Errorf("EnsureLessonCards calls = %d, want 0 (no fake backfill)", exec.ensureCardsCalls)
	}
}

// TestCompletedReconnectDoesNotReviewOrNarrate: reconnecting to a finished
// lesson restores the completion state only — session_ready + session_ended
// (lesson_complete), no summary replay, no second SubmitReview.
func TestCompletedReconnectDoesNotReviewOrNarrate(t *testing.T) {
	log := &captureLogger{}
	words := newFakeWords(ttCards("苹果", "香蕉", "橘子"))
	exec := newFakeExecutor(ttLessonID, ttCards("苹果", "香蕉", "橘子"))
	exec.setRow(3, 4)
	tts := newFakeTTS(1, 320)
	tm := NewTurnManager(newFakeASR(), newFakeLLM(), tts, exec, ttConfig(words), NewEventRecorder(log, "t"), nil)
	s := &captureSender{}
	ttInit(t, tm, s)

	ready := s.controlsOf(t, TypeSessionReady)
	if len(ready) != 1 {
		t.Fatalf("session_ready count = %d, want 1", len(ready))
	}
	if ready[0]["lesson"].(map[string]any)["current_index"] != float64(3) {
		t.Errorf("session_ready.lesson = %v, want current_index 3", ready[0]["lesson"])
	}
	ended := s.controlsOf(t, TypeSessionEnded)
	if len(ended) != 1 || ended[0]["reason"] != ReasonLessonComplete {
		t.Fatalf("session_ended = %v, want one lesson_complete", ended)
	}
	if s.countOf(TypeTeacherSentence) != 0 {
		t.Errorf("teacher_sentence = %d, want 0 (no summary replay)", s.countOf(TypeTeacherSentence))
	}
	if got := len(tts.recordedTexts()); got != 0 {
		t.Errorf("tts calls = %d, want 0", got)
	}
	if got := len(words.submissions()); got != 0 {
		t.Errorf("SubmitReview calls = %d, want 0 (no second review)", got)
	}
	if ttEventCount(t, log, EventReviewWritten) != 0 {
		t.Error("review_written emitted on a completed reconnect")
	}
}

// TestSentenceAckRejectsStaleUnknownAndDuplicate: only the current-epoch,
// registered, first ack consumes the pending entry; stale, unknown, and
// duplicate acks emit sanitized diagnostics with zero state side effects.
func TestSentenceAckRejectsStaleUnknownAndDuplicate(t *testing.T) {
	log := &captureLogger{}
	words := newFakeWords(ttCards("苹果", "香蕉"))
	tm := NewTurnManager(newFakeASR(), newFakeLLM(), newFakeTTS(1, 320), newFakeExecutorNoRow(ttLessonID), ttConfig(words), NewEventRecorder(log, "t"), nil)
	s := &captureSender{}
	ttInit(t, tm, s)
	ttAwaitSpoken(t, s, 2)

	ack := func(sid string, epoch uint64) {
		tm.HandleControl(s, mustJSON(t, SentenceAckMsg{Type: TypeSentenceAck, SentenceID: sid, Epoch: epoch}))
	}
	ack("t1-s0", 5) // stale epoch
	ack("t1-s99", 0) // unknown sentence
	if s.listeningCount() != 0 {
		t.Fatal("rejected acks lifted the playback gate")
	}
	sid := ttAckLast(t, tm, s, 0) // valid final ack
	ttWaitListening(t, s)
	ack(sid, 0) // duplicate

	if got := ttEventCount(t, log, EventWordAnnounced); got != 1 {
		t.Errorf("word_announced events = %d, want exactly 1", got)
	}
	if got := ttEventCount(t, log, EventSentenceAckRejected); got != 3 {
		t.Errorf("sentence_ack_rejected events = %d, want 3 (stale + unknown + duplicate)", got)
	}
}

// TestDictationRegistersPendingBeforeAudioStartAndObservesEarlyFinalAck:
// the pending entry is registered BEFORE the sentence's audio_start is
// queued (an ack sent while the final sentence is still streaming is
// accepted), and the sticky completion bit means the early final ack is
// never lost by the later await call.
func TestDictationRegistersPendingBeforeAudioStartAndObservesEarlyFinalAck(t *testing.T) {
	log := &captureLogger{}
	words := newFakeWords(ttCards("苹果", "香蕉"))
	tts := newFakeTTS(3, 320)
	tts.delay = 100 * time.Millisecond
	tm := NewTurnManager(newFakeASR(), newFakeLLM(), tts, newFakeExecutorNoRow(ttLessonID), ttConfig(words), NewEventRecorder(log, "t"), nil)
	s := &captureSender{}
	ttInit(t, tm, s)

	// Greeting is 3 sentences; wait until the FINAL sentence's audio_start
	// went out, then ack BEFORE its audio_end / before any await call.
	ttAwaitSpoken(t, s, 3)
	if !waitFor(t, 5*time.Second, func() bool { return s.countOf(TypeAudioStart) >= 3 }) {
		t.Fatal("final sentence audio_start never went out")
	}
	tm.HandleControl(s, mustJSON(t, SentenceAckMsg{Type: TypeSentenceAck, SentenceID: "t1-s2", Epoch: 0}))

	ttWaitListening(t, s)
	if got := ttEventCount(t, log, EventWordAnnounced); got != 1 {
		t.Errorf("word_announced events = %d, want 1 (early final ack observed)", got)
	}
	if got := ttEventCount(t, log, EventPlaybackAckTimeout); got != 0 {
		t.Errorf("playback_ack_timeout events = %d, want 0", got)
	}
	if got := ttEventCount(t, log, EventError); got != 0 {
		t.Errorf("error events = %d, want 0 (the early ack must not be rejected)", got)
	}
}

// ---------------------------------------------------------------------------
// Epoch cancellation + half-duplex gating (13-04 semantics, FSM form)
// ---------------------------------------------------------------------------

// TestTurnInterruptStopsAudioAndBumpsEpoch: with the TTS streaming
// continuously, an interrupt must (a) bump the epoch, (b) stop ALL further
// audio bytes to the sender, (c) ack with new_epoch > old_epoch, and (d)
// wake the playback-ack waiter without further messages.
func TestTurnInterruptStopsAudioAndBumpsEpoch(t *testing.T) {
	asr := newFakeASR(asrScript{text: "你好"})
	llm := newFakeLLM(intentScript(intentChat, "这是一句比较长的回答，慢慢说给你听。"))
	tts := newFakeTTS(1, 320) // greeting completes instantly
	tm := NewTurnManager(asr, llm, tts, newFakeExecutorNoRow(ttLessonID), ttConfig(newFakeWords(ttCards("苹果"))), nil, nil)
	s := &captureSender{}
	ttInit(t, tm, s)
	ttAwaitSpoken(t, s, 2)
	ttAckLast(t, tm, s, 0)
	ttWaitListening(t, s)

	tts.streamUntilCancel = true // the chat reply streams forever
	tts.delay = 20 * time.Millisecond
	base := s.audioCount() // greeting chunks already delivered — anchor on them
	done := make(chan struct{})
	go func() {
		tm.processTurn(s, []byte("pcm"))
		close(done)
	}()

	if !waitFor(t, 5*time.Second, func() bool { return s.audioCount() >= base+3 }) {
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

// TestTurnHalfDuplexGatesUplinkWhileSpeaking: while the teacher speaks —
// including the playback-pending window — uplink audio is dropped; the ASR
// must not be called for it.
func TestTurnHalfDuplexGatesUplinkWhileSpeaking(t *testing.T) {
	asr := newFakeASR(asrScript{text: "你好"})
	llm := newFakeLLM(intentScript(intentChat, "好的，听我说。"))
	tts := newFakeTTS(1, 320)
	tm := NewTurnManager(asr, llm, tts, newFakeExecutorNoRow(ttLessonID), ttConfig(newFakeWords(ttCards("苹果"))), nil, nil)
	s := &captureSender{}
	ttInit(t, tm, s)
	ttAwaitSpoken(t, s, 2)
	ttAckLast(t, tm, s, 0)
	ttWaitListening(t, s)

	tts.streamUntilCancel = true
	tts.delay = 50 * time.Millisecond
	base := s.audioCount() // greeting chunks already delivered — anchor on them
	done := make(chan struct{})
	go func() {
		tm.processTurn(s, []byte("pcm"))
		close(done)
	}()
	if !waitFor(t, 5*time.Second, func() bool { return s.audioCount() >= base+1 }) {
		t.Fatal("teacher never started speaking")
	}

	// Loud uplink during speaking/pending: dropped by the gate, never
	// transcribed.
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

// contains is a tiny strings.Contains wrapper keeping assertions terse.
func contains(haystack, needle string) bool {
	return strings.Contains(haystack, needle)
}
