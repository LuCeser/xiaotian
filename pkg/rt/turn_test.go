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
	"fmt"
	"os"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xiaotian/home-tutor-agent/pkg/lesson"
	"github.com/xiaotian/home-tutor-agent/pkg/mimo"
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

// ttConfig builds the standard test config.
func ttConfig(words WordSource) TurnManagerConfig {
	return TurnManagerConfig{LessonID: ttLessonID, Words: words}
}

// ttConfigWith builds the standard config and lets a test override timing
// fields (the 13-09 timer / VAD gating tests).
func ttConfigWith(words WordSource, mutate func(*TurnManagerConfig)) TurnManagerConfig {
	cfg := ttConfig(words)
	if mutate != nil {
		mutate(&cfg)
	}
	return cfg
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

// ---------------------------------------------------------------------------
// Intent classification contract (13-08 Task 2)
// ---------------------------------------------------------------------------

// TestClassifyMessagesCarryVocabGroundTruthAndSafety: source assertion that
// the classification request carries the eight-value intent vocabulary, the
// word ground truth (meaning/example), the position, the strict JSON output
// contract, the child-safety wording prohibition, the hint no-leak rule and
// the meaning honesty rule.
func TestClassifyMessagesCarryVocabGroundTruthAndSafety(t *testing.T) {
	cards := ttCards("苹果")
	tm := NewTurnManager(newFakeASR(), newFakeLLM(), newFakeTTS(1, 320), newFakeExecutorNoRow(ttLessonID), ttConfig(newFakeWords(cards)), nil, nil)
	ttBindTutor(tm, cards)
	tm.mu.Lock()
	tm.tutor.appendUserRound("你好老师")
	tm.mu.Unlock()

	msgs := tm.buildClassifyMessages("这个字不会写", TurnTarget{WordIndex: 0, ExpectedVersion: 1, Word: "苹果"})
	if len(msgs) != 2 {
		t.Fatalf("classify messages = %d, want [system user]", len(msgs))
	}
	system, user := msgs[0].Content, msgs[1].Content
	for _, want := range []string{
		"confirm", "repeat", "slow", "hint", "meaning", "pause", "resume", "chat",
		"JSON", "严禁任何羞辱", "苹果", "释义-苹果", "例句-苹果", "第 1 个词",
		"部分提示", "凭印象写",
	} {
		if !contains(system, want) {
			t.Errorf("classify system prompt missing %q", want)
		}
	}
	if !contains(user, "这个字不会写") {
		t.Errorf("classify user message missing the transcript: %q", user)
	}
	if !contains(user, "学生：你好老师") {
		t.Errorf("classify user message missing the short-history deque: %q", user)
	}
}

// TestClassifyRetriesOnceOnInvalidJSON: a first unusable output (no JSON)
// triggers exactly one corrected retry — the second call's conversation
// carries the appended user-level "JSON only" nudge — and the valid answer
// wins.
func TestClassifyRetriesOnceOnInvalidJSON(t *testing.T) {
	cards := ttCards("苹果")
	llm := newFakeLLM(
		llmScript{resp: wireChatResponse(t, `{"choices":[{"message":{"content":"我觉得你是想说写完了"}}]}`)},
		intentScript(intentHint, "上面是草字头哦"),
	)
	tm := NewTurnManager(newFakeASR(), llm, newFakeTTS(1, 320), newFakeExecutorNoRow(ttLessonID), ttConfig(newFakeWords(cards)), nil, nil)
	ttBindTutor(tm, cards)

	intent, reply := tm.classifyTurn(context.Background(), "不会写", TurnTarget{WordIndex: 0, ExpectedVersion: 1, Word: "苹果"})
	if intent != intentHint {
		t.Errorf("intent = %q, want hint", intent)
	}
	if reply != "上面是草字头哦" {
		t.Errorf("reply = %q, want the scripted hint", reply)
	}
	if llm.callCount() != 2 {
		t.Fatalf("llm calls = %d, want 2 (one retry)", llm.callCount())
	}
	retry := llm.calls[1].messages
	if len(retry) != 3 {
		t.Fatalf("retry conversation = %d messages, want 3", len(retry))
	}
	if retry[2].Role != "user" || !contains(retry[2].Content, "JSON") {
		t.Errorf("retry nudge = %+v, want a user-level JSON correction", retry[2])
	}
}

// TestClassifyFallsBackToChatAfterSecondFailure: two unusable outputs
// collapse deterministically to chat + the fixed spoken fallback — no
// user-visible ErrorMsg, one server-side provider_failed event.
func TestClassifyFallsBackToChatAfterSecondFailure(t *testing.T) {
	log := &captureLogger{}
	cards := ttCards("苹果")
	garbage := llmScript{resp: wireChatResponse(t, `{"choices":[{"message":{"content":"哈哈"}}]}`)}
	llm := newFakeLLM(garbage, garbage)
	tm := NewTurnManager(newFakeASR(), llm, newFakeTTS(1, 320), newFakeExecutorNoRow(ttLessonID), ttConfig(newFakeWords(cards)), NewEventRecorder(log, "t"), nil)
	ttBindTutor(tm, cards)

	intent, reply := tm.classifyTurn(context.Background(), "嗯嗯", TurnTarget{WordIndex: 0, ExpectedVersion: 1, Word: "苹果"})
	if intent != intentChat {
		t.Errorf("intent = %q, want chat", intent)
	}
	if reply != classifyFallbackReply {
		t.Errorf("reply = %q, want the fixed fallback", reply)
	}
	if llm.callCount() != 2 {
		t.Errorf("llm calls = %d, want 2 (exactly one retry)", llm.callCount())
	}
	if got := ttEventCount(t, log, EventError); got != 1 {
		t.Errorf("error events = %d, want 1 (server-side only)", got)
	}
	if got := ttEventCount(t, log, EventIntentClassified); got != 1 {
		t.Errorf("intent_classified events = %d, want 1", got)
	}
}

// ---------------------------------------------------------------------------
// Completion path: honest review write + ack-gated natural ending (Task 2)
// ---------------------------------------------------------------------------

// TestCompletionWritesReviewAndWaitsForPlaybackAck: the review write
// actually executes BEFORE the summary is spoken; after the summary's
// audio_end but before its ack the session must NOT end; the ack releases
// session_ended(lesson_complete).
func TestCompletionWritesReviewAndWaitsForPlaybackAck(t *testing.T) {
	words := newFakeWords(ttCards("苹果"))
	words.reviewResult = ReviewWriteResult{Requested: []string{"苹果"}, Updated: []string{"苹果"}}
	tm, s, log, exec, done := ttConfirmToCompletion(t, words, 5)

	// 先执行后播报: the write already ran while the summary plays.
	if got := len(words.submissions()); got != 1 {
		t.Fatalf("SubmitReview calls = %d, want 1 (and before the summary)", got)
	}
	ratings := words.submissions()[0]
	if ratings["苹果"] != "good" {
		t.Errorf("submitted rating = %v, want 苹果:good", ratings)
	}

	// audio_end seen, no ack: no session end yet.
	time.Sleep(250 * time.Millisecond)
	if got := s.countOf(TypeSessionEnded); got != 0 {
		t.Fatalf("session_ended before the summary ack = %d, want 0", got)
	}

	ttAckLast(t, tm, s, 0)
	if !waitFor(t, 5*time.Second, func() bool { return s.countOf(TypeSessionEnded) == 1 }) {
		t.Fatal("session_ended never arrived after the summary ack")
	}
	ended := s.controlsOf(t, TypeSessionEnded)[0]
	if ended["reason"] != ReasonLessonComplete {
		t.Errorf("reason = %v, want %s", ended["reason"], ReasonLessonComplete)
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("completion goroutine did not finish")
	}
	if exec.advanceCount() != 1 {
		t.Errorf("advance calls = %d, want 1", exec.advanceCount())
	}
	updated := ttEventField(t, log, EventReviewWritten, "updated")
	if len(updated) != 1 || updated[0] != 1 {
		t.Errorf("review_written updated = %v, want [1]", updated)
	}
}

// TestCompletionPartialReviewReportsActualCount: nil error with a PARTIAL
// update set is not success — the summary carries the honest "saved M/N"
// note, the event carries the real counts, the server records an error
// marker, and SubmitReview runs exactly once (no blind batch retry).
func TestCompletionPartialReviewReportsActualCount(t *testing.T) {
	words := newFakeWords(ttCards("苹果", "香蕉"))
	words.reviewResult = ReviewWriteResult{
		Requested: []string{"苹果", "香蕉"},
		Updated:   []string{"苹果"},
		Missing:   []string{"香蕉"},
	}
	tm, s, log, _, done := ttDriveTwoConfirmCompletion(t, words)

	if got := len(words.submissions()); got != 1 {
		t.Fatalf("SubmitReview calls = %d, want exactly 1 (no batch retry)", got)
	}
	sents := s.controlsOf(t, TypeTeacherSentence)
	last, _ := sents[len(sents)-1]["text"].(string)
	if !contains(last, "保存 1/2") {
		t.Errorf("summary = %q, want the honest 保存 1/2 note", last)
	}
	updated := ttEventField(t, log, EventReviewWritten, "updated")
	if len(updated) != 1 || updated[0] != 1 {
		t.Errorf("review_written updated = %v, want [1] (real count)", updated)
	}
	requested := ttEventField(t, log, EventReviewWritten, "requested")
	if len(requested) != 1 || requested[0] != 2 {
		t.Errorf("review_written requested = %v, want [2]", requested)
	}
	if got := ttEventCount(t, log, EventError); got == 0 {
		t.Error("no server-side error marker for the partial update")
	}

	ttAckLast(t, tm, s, 0)
	if !waitFor(t, 5*time.Second, func() bool { return s.countOf(TypeSessionEnded) == 1 }) {
		t.Fatal("session_ended never arrived after the summary ack")
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("completion goroutine did not finish")
	}
}

// TestCompletionZeroUpdatedWithNilErrorIsNotSuccess: nil error + zero
// updates must still surface as a failure — the spoken summary reports 0/1
// and the server records the error marker.
func TestCompletionZeroUpdatedWithNilErrorIsNotSuccess(t *testing.T) {
	words := newFakeWords(ttCards("苹果"))
	words.reviewResult = ReviewWriteResult{
		Requested: []string{"苹果"},
		Updated:   nil,
		Missing:   []string{"苹果"},
	}
	tm, s, log, _, done := ttConfirmToCompletion(t, words, 6)

	if got := len(words.submissions()); got != 1 {
		t.Fatalf("SubmitReview calls = %d, want exactly 1", got)
	}
	sents := s.controlsOf(t, TypeTeacherSentence)
	last, _ := sents[len(sents)-1]["text"].(string)
	if !contains(last, "保存 0/1") {
		t.Errorf("summary = %q, want the honest 保存 0/1 note", last)
	}
	updated := ttEventField(t, log, EventReviewWritten, "updated")
	if len(updated) != 1 || updated[0] != 0 {
		t.Errorf("review_written updated = %v, want [0]", updated)
	}
	if got := ttEventCount(t, log, EventError); got == 0 {
		t.Error("no server-side error marker for the zero update")
	}

	ttAckLast(t, tm, s, 0)
	if !waitFor(t, 5*time.Second, func() bool { return s.countOf(TypeSessionEnded) == 1 }) {
		t.Fatal("session_ended never arrived after the summary ack")
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("completion goroutine did not finish")
	}
}

// TestCompletionReviewFailureSpeaksHonestSuffix: a real SubmitReview error
// appends the record-not-saved wording — the student never hears a false
// "saved" (transparency prohibition).
func TestCompletionReviewFailureSpeaksHonestSuffix(t *testing.T) {
	words := newFakeWords(ttCards("苹果"))
	words.reviewErr = fmt.Errorf("pybridge run review failed")
	tm, s, log, _, done := ttConfirmToCompletion(t, words, 6)

	if got := len(words.submissions()); got != 1 {
		t.Fatalf("SubmitReview calls = %d, want exactly 1", got)
	}
	sents := s.controlsOf(t, TypeTeacherSentence)
	last, _ := sents[len(sents)-1]["text"].(string)
	if !contains(last, "没有保存成功") {
		t.Errorf("summary = %q, want the honest record-not-saved suffix", last)
	}
	if got := ttEventCount(t, log, EventReviewWritten); got != 0 {
		t.Errorf("review_written events = %d, want 0 (the write failed)", got)
	}
	if got := ttEventCount(t, log, EventError); got == 0 {
		t.Error("no server-side error marker for the failed write")
	}

	ttAckLast(t, tm, s, 0)
	if !waitFor(t, 5*time.Second, func() bool { return s.countOf(TypeSessionEnded) == 1 }) {
		t.Fatal("session_ended never arrived after the summary ack")
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("completion goroutine did not finish")
	}
}

// TestNoDueWordsWaitsForPlaybackAck: the zero-due explanation is fully
// spoken and the session ends ONLY on its actual-playback ack — the ending
// never truncates the explanation.
func TestNoDueWordsWaitsForPlaybackAck(t *testing.T) {
	log := &captureLogger{}
	words := newFakeWords() // zero due words
	tts := newFakeTTS(2, 320)
	tts.delay = 20 * time.Millisecond
	tm := NewTurnManager(newFakeASR(), newFakeLLM(), tts, newFakeExecutorNoRow(ttLessonID), ttConfig(words), NewEventRecorder(log, "t"), nil)
	s := &captureSender{}
	ttInit(t, tm, s)

	ready := s.controlsOf(t, TypeSessionReady)
	if len(ready) != 1 {
		t.Fatalf("session_ready count = %d, want 1", len(ready))
	}
	if got := ready[0]["lesson"].(map[string]any)["words"].([]any); len(got) != 0 {
		t.Errorf("lesson.words = %v, want empty", got)
	}

	ttAwaitSpoken(t, s, 1)
	if !waitFor(t, 5*time.Second, func() bool { return s.countOf(TypeAudioEnd) >= 1 }) {
		t.Fatal("explanation audio_end never went out")
	}
	time.Sleep(250 * time.Millisecond)
	if got := s.countOf(TypeSessionEnded); got != 0 {
		t.Fatalf("session_ended before the explanation ack = %d, want 0", got)
	}

	ttAckLast(t, tm, s, 0)
	if !waitFor(t, 5*time.Second, func() bool { return s.countOf(TypeSessionEnded) == 1 }) {
		t.Fatal("session_ended never arrived after the explanation ack")
	}
	ended := s.controlsOf(t, TypeSessionEnded)[0]
	if ended["reason"] != ReasonNoDueWords {
		t.Errorf("reason = %v, want %s", ended["reason"], ReasonNoDueWords)
	}
}

// TestPlaybackAckTimeoutIsVisibleAndBounded: without any summary ack the
// bounded wait expires — ErrorMsg(playback_ack_timeout) goes out FIRST,
// then session_ended still carries the original lesson_complete reason (the
// degraded order; the timeout itself is never an end reason). No fabricated
// completion evidence appears.
func TestPlaybackAckTimeoutIsVisibleAndBounded(t *testing.T) {
	words := newFakeWords(ttCards("苹果"))
	words.reviewResult = ReviewWriteResult{Requested: []string{"苹果"}, Updated: []string{"苹果"}}
	_, s, log, _, done := ttConfirmToCompletion(t, words, 5) // no ack is ever sent

	// The wait is bounded (min 3s): the timeout ErrorMsg must appear well
	// inside the test budget, then the natural end follows.
	timeoutErr := waitFor(t, 10*time.Second, func() bool {
		for _, e := range s.controlsOf(t, TypeError) {
			if e["code"] == ErrCodePlaybackAckTimeout {
				return true
			}
		}
		return false
	})
	if !timeoutErr {
		t.Fatal("playback_ack_timeout ErrorMsg never went out")
	}
	if !waitFor(t, 5*time.Second, func() bool { return s.countOf(TypeSessionEnded) == 1 }) {
		t.Fatal("session_ended never followed the ack timeout")
	}

	// Order: the timeout ErrorMsg precedes the session end.
	types := s.typeOrder()
	lastErr, firstEnd := -1, -1
	for i, tp := range types {
		if tp == TypeError {
			lastErr = i
		}
		if tp == TypeSessionEnded && firstEnd < 0 {
			firstEnd = i
		}
	}
	if lastErr < 0 || firstEnd < 0 || lastErr > firstEnd {
		t.Fatalf("degraded order violated: last error at %d, first session_ended at %d", lastErr, firstEnd)
	}
	ended := s.controlsOf(t, TypeSessionEnded)[0]
	if ended["reason"] != ReasonLessonComplete {
		t.Errorf("reason = %v, want the original %s (never a timeout reason)", ended["reason"], ReasonLessonComplete)
	}

	// No fabricated completion evidence: the review ran once, no extra
	// word_announced (the timeout must not arm the word timer or pretend a
	// word was announced).
	if got := len(words.submissions()); got != 1 {
		t.Errorf("SubmitReview calls = %d, want 1", got)
	}
	if got := ttEventCount(t, log, EventWordAnnounced); got != 1 {
		t.Errorf("word_announced events = %d, want 1 (greeting only; no fabricated completion)", got)
	}
	if got := ttEventCount(t, log, EventPlaybackAckTimeout); got != 1 {
		t.Errorf("playback_ack_timeout events = %d, want 1", got)
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("completion goroutine did not finish after the timeout")
	}
}

// TestCompletionDisconnectCancelsAckWait: a disconnect during the summary's
// ack wait cancels the wait — the session closes without any further
// messages (no session_ended, no timeout notice).
func TestCompletionDisconnectCancelsAckWait(t *testing.T) {
	words := newFakeWords(ttCards("苹果"))
	words.reviewResult = ReviewWriteResult{Requested: []string{"苹果"}, Updated: []string{"苹果"}}
	tm, s, log, _, done := ttConfirmToCompletion(t, words, 5)

	tm.Close(s) // transport read loop exited: cancels the ack wait
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("completion goroutine did not return after the disconnect")
	}
	if got := s.countOf(TypeSessionEnded); got != 0 {
		t.Errorf("session_ended after disconnect = %d, want 0", got)
	}
	if got := ttEventCount(t, log, EventPlaybackAckTimeout); got != 0 {
		t.Errorf("playback_ack_timeout events after disconnect = %d, want 0", got)
	}
}

// ttDriveTwoConfirmCompletion runs a TWO-word lesson through both confirms
// into the summary's audio_end window, returning before the summary ack.
func ttDriveTwoConfirmCompletion(t *testing.T, words *fakeWordSource) (*TurnManager, *captureSender, *captureLogger, *fakeExecutor, chan struct{}) {
	t.Helper()
	log := &captureLogger{}
	exec := newFakeExecutorNoRow(ttLessonID)
	asr := newFakeASR(asrScript{text: "好了"}, asrScript{text: "好了"})
	llm := newFakeLLM(intentScript(intentConfirm, ""), intentScript(intentConfirm, ""))
	tts := newFakeTTS(2, 320)
	tts.delay = 20 * time.Millisecond
	tm := NewTurnManager(asr, llm, tts, exec, ttConfig(words), NewEventRecorder(log, "t"), nil)
	s := &captureSender{}
	ttInit(t, tm, s)
	ttAwaitSpoken(t, s, 3)
	ttAckLast(t, tm, s, 0)
	ttWaitListening(t, s)

	// Turn 1: confirm word 1; the next-word announcement blocks the turn
	// goroutine until its ack.
	done1 := make(chan struct{})
	go func() {
		tm.processTurn(s, []byte("pcm"))
		close(done1)
	}()
	ttAwaitSpoken(t, s, 4)
	ttAckLast(t, tm, s, 0)
	select {
	case <-done1:
	case <-time.After(5 * time.Second):
		t.Fatal("first confirm never settled")
	}

	// Turn 2: confirm word 2 → completion summary (3 sentences).
	done2 := make(chan struct{})
	go func() {
		tm.processTurn(s, []byte("pcm"))
		close(done2)
	}()
	ttAwaitSpoken(t, s, 7) // 3 greeting + 1 announce + 3 summary
	if !waitFor(t, 5*time.Second, func() bool { return s.countOf(TypeAudioEnd) >= 7 }) {
		t.Fatal("summary audio_end never went out")
	}
	return tm, s, log, exec, done2
}

// typeOrder returns the decoded wire type of every captured control, in
// arrival order (for message-ordering assertions).
func (c *captureSender) typeOrder() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]string, 0, len(c.controls))
	for _, b := range c.controls {
		var probe struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(b, &probe) == nil {
			out = append(out, probe.Type)
		}
	}
	return out
}

// ttEventField returns the values of one field across all recorded entries
// of the given event name.
func ttEventField(t *testing.T, log *captureLogger, event, key string) []any {
	t.Helper()
	var vals []any
	for _, e := range log.snapshot() {
		if e.msg != "rt_event" {
			continue
		}
		matched, val := false, any(nil)
		for _, f := range e.fields {
			if f.Key == "event" && f.Value == event {
				matched = true
			}
			if f.Key == key {
				val = f.Value
			}
		}
		if matched {
			vals = append(vals, val)
		}
	}
	return vals
}

// ttBindTutor attaches a fresh tutor to the manager (white-box classify
// tests that bypass session_init).
func ttBindTutor(tm *TurnManager, cards []lesson.Word) {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	tm.tutor = newDictationTutor(lesson.Lesson{
		LessonID: ttLessonID, Words: cardWords(cards), Cards: cards, Version: 1,
	})
}

// ttWaitListening waits for the FSM to hand control back after an ack.
func ttWaitListening(t *testing.T, s *captureSender) {
	t.Helper()
	if !waitFor(t, 5*time.Second, func() bool { return s.listeningCount() > 0 }) {
		t.Fatal("FSM never returned to listening after the valid ack")
	}
}

// ttConfirmToCompletion drives a ONE-word lesson from session_init through
// the completion summary's final audio_end (summary pending its ack), with
// wantSpoken the expected total teacher-sentence count (3 greeting + N
// summary sentences). The caller owns the summary ack.
func ttConfirmToCompletion(t *testing.T, words *fakeWordSource, wantSpoken int) (*TurnManager, *captureSender, *captureLogger, *fakeExecutor, chan struct{}) {
	t.Helper()
	log := &captureLogger{}
	exec := newFakeExecutorNoRow(ttLessonID)
	asr := newFakeASR(asrScript{text: "好了"})
	llm := newFakeLLM(intentScript(intentConfirm, ""))
	tts := newFakeTTS(2, 320)
	tts.delay = 20 * time.Millisecond
	tm := NewTurnManager(asr, llm, tts, exec, ttConfig(words), NewEventRecorder(log, "t"), nil)
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
	ttAwaitSpoken(t, s, wantSpoken)
	if !waitFor(t, 5*time.Second, func() bool { return s.countOf(TypeAudioEnd) >= wantSpoken }) {
		t.Fatalf("summary audio_end never went out (spoken=%d)", s.countOf(TypeTeacherSentence))
	}
	return tm, s, log, exec, done
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
// Interrupt trim on the FSM short-history deque (Pitfall 9, 13-08 semantics)
// ---------------------------------------------------------------------------

// ttTrimFixture drives one chat turn whose reply is two sentences on slow
// TTS and returns once both sentences went out (announcement pending).
func ttTrimFixture(t *testing.T, ttsDelay time.Duration) (*TurnManager, *captureSender) {
	t.Helper()
	asr := newFakeASR(asrScript{text: "开始吧"})
	llm := newFakeLLM(intentScript(intentChat, "这是第一句话。这是第二句话。"))
	tts := newFakeTTS(1, 320)
	tts.delay = ttsDelay
	tm := NewTurnManager(asr, llm, tts, newFakeExecutorNoRow(ttLessonID), ttConfig(newFakeWords(ttCards("苹果"))), nil, nil)
	s := &captureSender{}
	ttInit(t, tm, s)
	ttAwaitSpoken(t, s, 3) // greeting
	ttAckLast(t, tm, s, 0)
	ttWaitListening(t, s)

	go tm.processTurn(s, []byte("pcm"))
	ttAwaitSpoken(t, s, 5) // greeting 3 + reply 2
	return tm, s
}

// TestTurnInterruptTrimsShortHistoryToPlayedSentences: the interrupt trims
// the FSM short-history deque's newest teacher reply to the sentences the
// student actually heard; zero-played removes the reply while the student's
// own utterance stays.
func TestTurnInterruptTrimsShortHistoryToPlayedSentences(t *testing.T) {
	t.Run("played sentence survives in the deque", func(t *testing.T) {
		tm, s := ttTrimFixture(t, 250*time.Millisecond)
		// The reply announcement's sentences are t3-s0/t3-s1 (turn 1 =
		// greeting, turn 2 = the utterance, turn 3 = the announcement).
		tm.HandleControl(s, mustJSON(t, SentenceAckMsg{Type: TypeSentenceAck, SentenceID: "t3-s0", Epoch: 0}))
		tm.HandleControl(s, mustJSON(t, InterruptMsg{Type: TypeInterrupt, Epoch: 0}))

		tm.mu.Lock()
		defer tm.mu.Unlock()
		n := len(tm.tutor.shortHistory)
		if n < 2 {
			t.Fatalf("short history = %d rounds, want >= 2", n)
		}
		last := tm.tutor.shortHistory[n-1]
		if last.userText != "开始吧" {
			t.Errorf("user text = %q, want 开始吧 (the student's own utterance stays)", last.userText)
		}
		if last.teacherText != "这是第一句话。" {
			t.Errorf("trimmed teacher text = %q, want only the played first sentence", last.teacherText)
		}
	})

	t.Run("zero played removes the reply", func(t *testing.T) {
		tm, s := ttTrimFixture(t, 250*time.Millisecond)
		tm.HandleControl(s, mustJSON(t, InterruptMsg{Type: TypeInterrupt, Epoch: 0}))

		tm.mu.Lock()
		defer tm.mu.Unlock()
		last := tm.tutor.shortHistory[len(tm.tutor.shortHistory)-1]
		if last.teacherText != "" {
			t.Errorf("teacher text = %q, want empty (zero-played reply removed)", last.teacherText)
		}
		if last.userText != "开始吧" {
			t.Errorf("user text = %q, want 开始吧 (the round survives)", last.userText)
		}
	})
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

// ---------------------------------------------------------------------------
// 13-09 Task 1: the four command intents + pause/resume (TestIntent*)
// ---------------------------------------------------------------------------

// stateCount counts StateMsg values equal to the given state value.
func (c *captureSender) stateCount(value string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for _, b := range c.controls {
		var m StateMsg
		if json.Unmarshal(b, &m) == nil && m.Value == value {
			n++
		}
	}
	return n
}

// ttIntentFixture drives a dictation session from session_init through the
// greeting's valid ack into listening, with the caller's scripted ASR/LLM
// queued for the intent turn(s) under test.
func ttIntentFixture(t *testing.T, cards []lesson.Word, asr *fakeASR, llm *fakeLLM) (*TurnManager, *captureSender, *captureLogger, *fakeExecutor) {
	return ttIntentFixtureCfg(t, cards, asr, llm, ttConfig(newFakeWords(cards)))
}

// ttIntentFixtureCfg is ttIntentFixture with an explicit config (timing
// fields for the 13-09 tests).
func ttIntentFixtureCfg(t *testing.T, cards []lesson.Word, asr *fakeASR, llm *fakeLLM, cfg TurnManagerConfig) (*TurnManager, *captureSender, *captureLogger, *fakeExecutor) {
	t.Helper()
	log := &captureLogger{}
	exec := newFakeExecutorNoRow(ttLessonID)
	tm := NewTurnManager(asr, llm, newFakeTTS(1, 320), exec, cfg, NewEventRecorder(log, "t"), nil)
	s := &captureSender{}
	ttInit(t, tm, s)
	ttAwaitSpoken(t, s, 3) // greeting (hello + count + first word)
	ttAckLast(t, tm, s, 0)
	ttWaitListening(t, s)
	return tm, s, log, exec
}

// ttRunIntentTurn drives one scripted utterance through the REAL
// processTurn, acks the reply announcement once it appears, and waits for
// the turn to settle. prevSent is the teacher-sentence count before the
// turn (every intent replies with at least one announcement).
func ttRunIntentTurn(t *testing.T, tm *TurnManager, s *captureSender, prevSent int) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		tm.processTurn(s, []byte("pcm"))
		close(done)
	}()
	if !waitFor(t, 5*time.Second, func() bool { return s.countOf(TypeTeacherSentence) > prevSent }) {
		t.Fatal("intent reply announcement never spoke")
	}
	ttAckLast(t, tm, s, 0)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("intent turn never settled after the reply ack")
	}
}

// ttPaused reads the classroom pause bit (white-box).
func ttPaused(t *testing.T, tm *TurnManager) bool {
	t.Helper()
	tm.mu.Lock()
	defer tm.mu.Unlock()
	return tm.tutor != nil && tm.tutor.paused
}

// ttLastSentence returns the most recent teacher sentence text.
func ttLastSentence(t *testing.T, s *captureSender) string {
	t.Helper()
	sents := s.controlsOf(t, TypeTeacherSentence)
	if len(sents) == 0 {
		t.Fatal("no teacher sentence captured")
	}
	text, _ := sents[len(sents)-1]["text"].(string)
	return text
}

// ttSentencesSince returns every teacher sentence text from the given index
// onward, JOINED — announcements are sentence-streamed, so one template
// reply arrives as several teacher_sentence messages (字幕先行, per-sentence
// TTS) and the spoken whole is their concatenation.
func ttSentencesSince(t *testing.T, s *captureSender, from int) string {
	t.Helper()
	sents := s.controlsOf(t, TypeTeacherSentence)
	if len(sents) < from {
		t.Fatalf("teacher sentences = %d, want >= %d", len(sents), from)
	}
	var b strings.Builder
	for _, m := range sents[from:] {
		text, _ := m["text"].(string)
		b.WriteString(text)
	}
	return b.String()
}

// TestIntentDispatchSwitchCoversVocabulary: source assertion — the
// dispatchTurn switch covers the eight-value intent vocabulary exhaustively
// with NO default/fall-through-to-chat branch left (Task 1 acceptance).
func TestIntentDispatchSwitchCoversVocabulary(t *testing.T) {
	src, err := os.ReadFile("turn.go")
	if err != nil {
		t.Fatalf("read turn.go: %v", err)
	}
	text := string(src)
	start := strings.Index(text, "func (tm *TurnManager) dispatchTurn")
	if start < 0 {
		t.Fatal("dispatchTurn not found in turn.go")
	}
	end := strings.Index(text[start:], "\nfunc ")
	if end < 0 {
		t.Fatal("cannot delimit dispatchTurn body")
	}
	body := text[start : start+end]
	for _, ident := range []string{
		"intentConfirm", "intentRepeat", "intentSlow", "intentHint",
		"intentMeaning", "intentPause", "intentResume", "intentChat",
	} {
		if !strings.Contains(body, "case "+ident+":") {
			t.Errorf("dispatchTurn switch missing case %s", ident)
		}
	}
	if strings.Contains(body, "default:") {
		t.Error("dispatchTurn switch carries a default branch — the fall-back-to-chat leftover must be gone")
	}
}

// TestIntentRepeatReAnnouncesCurrentWord: repeat re-reads the CURRENT word
// from the template; the cursor never moves, nothing advances, and the FSM
// hands control back after the reading's ack.
func TestIntentRepeatReAnnouncesCurrentWord(t *testing.T) {
	cards := ttCards("苹果", "香蕉")
	asr := newFakeASR(asrScript{text: "再读一遍"})
	llm := newFakeLLM(intentScript(intentRepeat, ""))
	tm, s, log, exec := ttIntentFixture(t, cards, asr, llm)

	ttRunIntentTurn(t, tm, s, 3)

	if got := ttSentencesSince(t, s, 3); got != repeatText("苹果") {
		t.Errorf("repeat announcement = %q, want %q", got, repeatText("苹果"))
	}
	tm.mu.Lock()
	idx := tm.tutor.currentIndex
	tm.mu.Unlock()
	if idx != 0 {
		t.Errorf("cursor moved to %d, want 0 (repeat never advances)", idx)
	}
	if exec.advanceCount() != 0 {
		t.Errorf("advance calls = %d, want 0", exec.advanceCount())
	}
	if s.countOf(TypeLessonUpdate) != 0 {
		t.Error("lesson_update sent for a repeat")
	}
	if ttEventCount(t, log, EventWordAdvanced) != 0 {
		t.Error("word_advanced emitted for a repeat")
	}
	ttWaitListening(t, s)
}

// TestIntentSlowReadsWordWithPausesNotSpelling: slow reads the word twice
// with full-stop pauses — and NEVER spells it out character by character
// (plan prohibition #1: the dictation leak ban; source-level assertion).
func TestIntentSlowReadsWordWithPausesNotSpelling(t *testing.T) {
	cards := ttCards("苹果")
	asr := newFakeASR(asrScript{text: "慢一点读"})
	llm := newFakeLLM(intentScript(intentSlow, ""))
	tm, s, _, exec := ttIntentFixture(t, cards, asr, llm)

	ttRunIntentTurn(t, tm, s, 3)

	got := ttSentencesSince(t, s, 3)
	if got != slowReadText("苹果") {
		t.Errorf("slow announcement = %q, want %q", got, slowReadText("苹果"))
	}
	if !contains(got, "苹果。苹果。") {
		t.Errorf("slow announcement = %q, want the word repeated twice with full-stop pauses", got)
	}
	if regexp.MustCompile(`[a-zA-Z]`).MatchString(got) {
		t.Errorf("slow announcement = %q, want NO pinyin/letters (no spelling out)", got)
	}
	if contains(got, "苹。果") {
		t.Errorf("slow announcement = %q, spelled the word character by character", got)
	}
	if exec.advanceCount() != 0 {
		t.Errorf("advance calls = %d, want 0", exec.advanceCount())
	}
	ttWaitListening(t, s)
}

// TestIntentHintRecordsAgainAndSpeaksReply: hint records the "again"
// outcome for the current word and speaks the classification reply (the
// no-leak constraint lives in the system prompt; dispatch consumes reply).
func TestIntentHintRecordsAgainAndSpeaksReply(t *testing.T) {
	cards := ttCards("苹果")
	asr := newFakeASR(asrScript{text: "这个字不会写"})
	llm := newFakeLLM(intentScript(intentHint, "上面是草字头哦"))
	tm, s, _, exec := ttIntentFixture(t, cards, asr, llm)

	ttRunIntentTurn(t, tm, s, 3)

	if got := ttSentencesSince(t, s, 3); got != "上面是草字头哦" {
		t.Errorf("hint reply = %q, want the scripted hint", got)
	}
	tm.mu.Lock()
	outcome, ok := tm.tutor.perWordOutcome[0]
	tm.mu.Unlock()
	if !ok || outcome != outcomeHint {
		t.Errorf("perWordOutcome[0] = (%q, %v), want hint recorded", outcome, ok)
	}
	if exec.advanceCount() != 0 {
		t.Errorf("advance calls = %d, want 0", exec.advanceCount())
	}
	if s.countOf(TypeLessonUpdate) != 0 {
		t.Error("lesson_update sent for a hint")
	}
	ttWaitListening(t, s)
}

// ---------------------------------------------------------------------------
// U-9 裁定（2026-10-06）：hint 感知评级——以「提示后是否写对」为准：
// hint 后 confirm=hard；hint 后 timeout/仍错=again；无提示路径不变
// （confirm=good、timeout=hard，由既有 Race 测试继续守护）。
// ---------------------------------------------------------------------------

// TestHintThenConfirmRatesHard: after a hint request the student confirms
// the word — per the U-9 裁定（2026-10-06） the rating must be hard (helped
// but finished), never the old terminal-overwrite good.
func TestHintThenConfirmRatesHard(t *testing.T) {
	cards := ttCards("苹果", "香蕉")
	asr := newFakeASR(asrScript{text: "这个字不会写"}, asrScript{text: "好了"})
	llm := newFakeLLM(intentScript(intentHint, "上面是草字头哦"), intentScript(intentConfirm, ""))
	tm, s, _, _ := ttIntentFixture(t, cards, asr, llm)

	// Turn 1: the hint (no advance; the reply re-arms the countdown).
	ttRunIntentTurn(t, tm, s, 3)
	// Turn 2: the confirm commits word 0 and announces word 1.
	ttRunIntentTurn(t, tm, s, s.countOf(TypeTeacherSentence))

	tm.mu.Lock()
	rating := tm.tutor.ratings()["苹果"]
	tm.mu.Unlock()
	if rating != "hard" {
		t.Errorf("rating after hint+confirm = %q, want hard (U-9 裁定 2026-10-06)", rating)
	}
}

// TestHintThenTimeoutRatesAgain: after a hint request the word timer's
// auto-advance commits the word — per the U-9 裁定（2026-10-06） the rating
// must be again (still unfinished even with help), never the old
// terminal-overwrite hard.
func TestHintThenTimeoutRatesAgain(t *testing.T) {
	cards := ttCards("苹果", "香蕉")
	asr := newFakeASR(asrScript{text: "这个字不会写"})
	llm := newFakeLLM(intentScript(intentHint, "上面是草字头哦"))
	tm, s, _, _ := ttIntentFixture(t, cards, asr, llm)

	// Turn 1: the hint (no advance).
	ttRunIntentTurn(t, tm, s, 3)

	// The timeout drives the advance with the captured target (the
	// TestTimeoutConfirmRaceConfirmWins manual-drive pattern); it blocks in
	// the next-word announcement until the test acks it.
	fired := make(chan struct{})
	go func() {
		tm.timeoutAdvance(s, 0, TurnTarget{WordIndex: 0, ExpectedVersion: 1, Word: "苹果"})
		close(fired)
	}()
	if !waitFor(t, 5*time.Second, func() bool { return s.countOf(TypeLessonUpdate) == 1 }) {
		t.Fatal("the timeout never committed")
	}
	ttAwaitSpoken(t, s, 5) // 3 greeting + 1 hint reply + 1 next-word announce
	ttAckLast(t, tm, s, 0)
	select {
	case <-fired:
	case <-time.After(5 * time.Second):
		t.Fatal("the timeout goroutine never returned")
	}

	tm.mu.Lock()
	rating := tm.tutor.ratings()["苹果"]
	tm.mu.Unlock()
	if rating != "again" {
		t.Errorf("rating after hint+timeout = %q, want again (U-9 裁定 2026-10-06)", rating)
	}
}

// TestWordRatingMatrix: the pure rating matrix is pinned table-driven — the
// matrix is the U-9 裁定（2026-10-06） verbatim, and wordRating is its single
// reference point (Phase 13.1 unverified-fallback must reuse it, never fork).
func TestWordRatingMatrix(t *testing.T) {
	cases := []struct {
		name    string
		outcome string
		hints   int
		want    string
	}{
		{"confirmed no hint", outcomeConfirmed, 0, "good"},
		{"confirmed after hints", outcomeConfirmed, 2, "hard"},
		{"timeout no hint", outcomeTimeout, 0, "hard"},
		{"timeout after one hint", outcomeTimeout, 1, "again"},
		{"hint pre-record one hint", outcomeHint, 1, "again"},
		{"hint pre-record three hints", outcomeHint, 3, "again"},
		{"unknown outcome skipped", "bogus", 0, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := wordRating(tc.outcome, tc.hints); got != tc.want {
				t.Errorf("wordRating(%q, %d) = %q, want %q", tc.outcome, tc.hints, got, tc.want)
			}
		})
	}
}

// TestHintCountIncrementsAndEvents: every hint dispatch increments the
// word's hint count and emits hint_given with count-class fields only
// (word_index + hint_count, the U-9 裁定 2026-10-06 per-word accounting) —
// two consecutive hints on one word count 2 and emit the running count [1,2].
func TestHintCountIncrementsAndEvents(t *testing.T) {
	cards := ttCards("苹果")
	asr := newFakeASR(asrScript{text: "这个字不会写"}, asrScript{text: "还是不会写"})
	llm := newFakeLLM(intentScript(intentHint, "上面是草字头哦"), intentScript(intentHint, "下面是木字底哦"))
	tm, s, log, _ := ttIntentFixture(t, cards, asr, llm)

	ttRunIntentTurn(t, tm, s, 3)
	ttRunIntentTurn(t, tm, s, s.countOf(TypeTeacherSentence))

	tm.mu.Lock()
	n := tm.tutor.perWordHints[0]
	tm.mu.Unlock()
	if n != 2 {
		t.Errorf("perWordHints[0] = %d, want 2 (same word hinted twice counts twice)", n)
	}
	if got := ttEventCount(t, log, EventHintGiven); got != 2 {
		t.Errorf("hint_given events = %d, want 2", got)
	}
	counts := ttEventField(t, log, EventHintGiven, "hint_count")
	if len(counts) != 2 || counts[0] != 1 || counts[1] != 2 {
		t.Errorf("hint_given hint_count = %v, want [1 2] (running per-word count)", counts)
	}
	indexes := ttEventField(t, log, EventHintGiven, "word_index")
	if len(indexes) != 2 || indexes[0] != 0 || indexes[1] != 0 {
		t.Errorf("hint_given word_index = %v, want [0 0]", indexes)
	}
}

// TestCompletionReviewCarriesHintAccounting: a hinted-then-confirmed word
// submits hard (U-9 裁定 2026-10-06) and review_written carries the hint
// aggregates hint_words=1 / hint_total=1 — the rating evidence stays
// replayable from the JSONL stream.
func TestCompletionReviewCarriesHintAccounting(t *testing.T) {
	cards := ttCards("苹果")
	words := newFakeWords(cards)
	words.reviewResult = ReviewWriteResult{Requested: []string{"苹果"}, Updated: []string{"苹果"}}
	asr := newFakeASR(asrScript{text: "这个字不会写"}, asrScript{text: "好了"})
	llm := newFakeLLM(intentScript(intentHint, "上面是草字头哦"), intentScript(intentConfirm, ""))
	tm, s, log, _ := ttIntentFixtureCfg(t, cards, asr, llm, ttConfig(words))

	// Turn 1: the hint (no advance; the reply re-arms the countdown).
	ttRunIntentTurn(t, tm, s, 3)

	// Turn 2: the confirm commits the word, drives completion; the summary
	// plays and blocks the turn goroutine until the test acks it.
	prev := s.countOf(TypeTeacherSentence)
	audioEndBefore := s.countOf(TypeAudioEnd)
	done := make(chan struct{})
	go func() {
		tm.processTurn(s, []byte("pcm"))
		close(done)
	}()
	ttAwaitSpoken(t, s, prev+2) // the wrap-up summary (2 sentences: the hint line counts only words whose FINAL outcome is hint)
	if !waitFor(t, 5*time.Second, func() bool { return s.countOf(TypeAudioEnd) > audioEndBefore }) {
		t.Fatal("summary audio_end never went out")
	}
	ttAckLast(t, tm, s, 0)
	if !waitFor(t, 5*time.Second, func() bool { return s.countOf(TypeSessionEnded) == 1 }) {
		t.Fatal("session_ended never arrived after the summary ack")
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("completion goroutine did not finish")
	}

	subs := words.submissions()
	if len(subs) != 1 {
		t.Fatalf("SubmitReview calls = %d, want 1", len(subs))
	}
	if got := subs[0]["苹果"]; got != "hard" {
		t.Errorf("submitted rating = %q, want hard (U-9 裁定 2026-10-06)", got)
	}
	hintWords := ttEventField(t, log, EventReviewWritten, "hint_words")
	if len(hintWords) != 1 || hintWords[0] != 1 {
		t.Errorf("review_written hint_words = %v, want [1]", hintWords)
	}
	hintTotal := ttEventField(t, log, EventReviewWritten, "hint_total")
	if len(hintTotal) != 1 || hintTotal[0] != 1 {
		t.Errorf("review_written hint_total = %v, want [1]", hintTotal)
	}
}

// TestIntentMeaningSpeaksGroundedReply: meaning speaks the classification
// reply (grounded via the system prompt's word ground truth); no outcome is
// recorded — asking for the meaning is not an attempt.
func TestIntentMeaningSpeaksGroundedReply(t *testing.T) {
	cards := ttCards("苹果")
	asr := newFakeASR(asrScript{text: "这个词什么意思"})
	llm := newFakeLLM(intentScript(intentMeaning, "苹果是一种水果。"))
	tm, s, _, exec := ttIntentFixture(t, cards, asr, llm)

	ttRunIntentTurn(t, tm, s, 3)

	if got := ttSentencesSince(t, s, 3); got != "苹果是一种水果。" {
		t.Errorf("meaning reply = %q, want the scripted grounded reply", got)
	}
	tm.mu.Lock()
	n := len(tm.tutor.perWordOutcome)
	tm.mu.Unlock()
	if n != 0 {
		t.Errorf("outcomes recorded = %d, want 0 (meaning is not an attempt)", n)
	}
	if exec.advanceCount() != 0 {
		t.Errorf("advance calls = %d, want 0", exec.advanceCount())
	}
	ttWaitListening(t, s)
}

// TestIntentPauseAckLeavesClientPaused: the pause command sets the
// independent classroom bit, the acknowledgement plays, and after it
// settles the student STILL sees paused — no silent resume to listening.
func TestIntentPauseAckLeavesClientPaused(t *testing.T) {
	cards := ttCards("苹果")
	asr := newFakeASR(asrScript{text: "我要休息一下"})
	llm := newFakeLLM(intentScript(intentPause, ""))
	tm, s, _, exec := ttIntentFixture(t, cards, asr, llm)
	listeningBefore := s.listeningCount()

	ttRunIntentTurn(t, tm, s, 3)

	if got := ttSentencesSince(t, s, 3); got != pauseAckText() {
		t.Errorf("pause ack = %q, want %q", got, pauseAckText())
	}
	if !ttPaused(t, tm) {
		t.Error("classroom pause bit not set")
	}
	if _, handle := ttTimerState(tm); handle != nil {
		t.Error("countdown not suspended by the pause")
	}
	if got := s.listeningCount(); got != listeningBefore {
		t.Errorf("listening broadcasts = %d, want %d (no silent resume after the ack)",
			got, listeningBefore)
	}
	if got := s.stateCount(StatePaused); got == 0 {
		t.Error("no paused state broadcast")
	}
	if s.countOf(TypeLessonUpdate) != 0 {
		t.Error("lesson_update sent for a pause")
	}
	if exec.advanceCount() != 0 {
		t.Errorf("advance calls = %d, want 0", exec.advanceCount())
	}
}

// TestIntentResumeRecognizedWhilePausedAndRearmsAfterAck: the resume
// command is recognized through the real processTurn while the classroom is
// paused, clears the pause bit, confirms the SAME word position, and hands
// control back to listening after the confirmation's ack.
func TestIntentResumeRecognizedWhilePausedAndRearmsAfterAck(t *testing.T) {
	cards := ttCards("苹果")
	asr := newFakeASR(asrScript{text: "休息一下"}, asrScript{text: "继续"})
	llm := newFakeLLM(intentScript(intentPause, ""), intentScript(intentResume, ""))
	tm, s, _, _ := ttIntentFixture(t, cards, asr, llm)

	ttRunIntentTurn(t, tm, s, 3) // pause
	if !ttPaused(t, tm) {
		t.Fatal("pause bit not set before the resume turn")
	}
	if _, handle := ttTimerState(tm); handle != nil {
		t.Fatal("countdown not suspended by the pause")
	}
	ttRunIntentTurn(t, tm, s, 4) // resume

	if ttPaused(t, tm) {
		t.Error("pause bit still set after the resume")
	}
	if _, handle := ttTimerState(tm); handle == nil {
		t.Error("countdown not re-armed after the resume ack")
	}
	got := ttSentencesSince(t, s, 4)
	if !contains(got, "还是第 1 个词") || !contains(got, "苹果") {
		t.Errorf("resume ack = %q, want the same word position (第 1 个词 苹果)", got)
	}
	if s.listeningCount() == 0 {
		t.Error("no listening broadcast after the resume ack settled")
	}
}

// TestIntentConfirmWhilePausedIsGentleRejected: a confirm while paused
// never advances — the gentle rejection speaks, the pause bit holds, and no
// lesson_update / executor write happens.
func TestIntentConfirmWhilePausedIsGentleRejected(t *testing.T) {
	cards := ttCards("苹果")
	asr := newFakeASR(asrScript{text: "休息一下"}, asrScript{text: "好了"})
	llm := newFakeLLM(intentScript(intentPause, ""), intentScript(intentConfirm, ""))
	tm, s, _, exec := ttIntentFixture(t, cards, asr, llm)
	listeningBefore := s.listeningCount()

	ttRunIntentTurn(t, tm, s, 3) // pause
	ttRunIntentTurn(t, tm, s, 4) // confirm while paused

	if got := ttSentencesSince(t, s, 4); got != confirmPausedRejectText() {
		t.Errorf("paused confirm reply = %q, want the gentle rejection %q", got, confirmPausedRejectText())
	}
	if !ttPaused(t, tm) {
		t.Error("pause bit cleared by a paused confirm")
	}
	if exec.advanceCount() != 0 {
		t.Errorf("advance calls = %d, want 0 (confirm must not advance while paused)", exec.advanceCount())
	}
	if s.countOf(TypeLessonUpdate) != 0 {
		t.Error("lesson_update sent for a paused confirm")
	}
	if got := s.listeningCount(); got != listeningBefore {
		t.Errorf("listening broadcasts = %d, want %d (still paused after the rejection)", got, listeningBefore)
	}
}

// ---------------------------------------------------------------------------
// 13-09 Task 2: the 60s per-word confirm countdown (TestWordTimer*,
// TestStaleTimer*, TestTimeoutConfirmRace*)
// ---------------------------------------------------------------------------

// ttTimerState reads the timer generation and live handle (white-box).
func ttTimerState(tm *TurnManager) (uint64, *time.Timer) {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	return tm.timerGen, tm.wordTimer
}

// TestWordTimerArmsOnlyAfterPlaybackAck: the countdown arms ONLY on the
// announcement's valid final sentence_ack — TTS completion and audio_end
// never arm it.
func TestWordTimerArmsOnlyAfterPlaybackAck(t *testing.T) {
	cards := ttCards("苹果")
	log := &captureLogger{}
	tm := NewTurnManager(newFakeASR(), newFakeLLM(), newFakeTTS(1, 320), newFakeExecutorNoRow(ttLessonID),
		ttConfigWith(newFakeWords(cards), nil), NewEventRecorder(log, "t"), nil)
	s := &captureSender{}
	ttInit(t, tm, s)

	// Fully spoken (TTS done, audio_end out) but NOT acked: nothing armed.
	ttAwaitSpoken(t, s, 3)
	gen, handle := ttTimerState(tm)
	if handle != nil || gen != 0 {
		t.Fatalf("timer armed before the playback ack: gen=%d handle=%v", gen, handle)
	}

	ttAckLast(t, tm, s, 0)
	if !waitFor(t, 5*time.Second, func() bool { _, h := ttTimerState(tm); return h != nil }) {
		t.Fatal("timer never armed after the greeting's valid ack")
	}
	if gen, _ := ttTimerState(tm); gen != 1 {
		t.Errorf("timerGen = %d, want 1 after the first arm", gen)
	}
}

// TestWordTimerResetInvalidatesQueuedCallbackSameEpochAndIndex: the armed
// callback is QUEUED (captured by the injected factory), then a re-arm
// happens with the same epoch and the same word index — when the old
// callback finally runs it must be dead on arrival (T-13-16), with zero
// side effects and the new handle untouched.
func TestWordTimerResetInvalidatesQueuedCallbackSameEpochAndIndex(t *testing.T) {
	cards := ttCards("苹果", "香蕉")
	log := &captureLogger{}
	exec := newFakeExecutorNoRow(ttLessonID)
	tm := NewTurnManager(newFakeASR(), newFakeLLM(), newFakeTTS(1, 320), exec,
		ttConfigWith(newFakeWords(cards), func(c *TurnManagerConfig) { c.WordTimeout = time.Hour }),
		NewEventRecorder(log, "t"), nil)

	// Capture the armed callback instead of scheduling it: the deterministic
	// "already queued" callback.
	tm.mu.Lock()
	var queued func()
	tm.timerFactory = func(_ time.Duration, cb func()) *time.Timer {
		queued = cb // invoked under tm.mu by armWordTimerLocked
		return time.NewTimer(time.Hour) // the injected handle never fires
	}
	tm.mu.Unlock()

	s := &captureSender{}
	ttInit(t, tm, s)
	ttAwaitSpoken(t, s, 3)
	ttAckLast(t, tm, s, 0)
	ttWaitListening(t, s)

	tm.mu.Lock()
	stale := queued
	gen1 := tm.timerGen
	tm.mu.Unlock()
	if stale == nil {
		t.Fatal("arm never captured the queued callback")
	}

	// Reset: same epoch, same word index, nothing changed but the
	// generation.
	tm.armWordTimer()
	gen2, handle := ttTimerState(tm)
	if gen2 != gen1+1 {
		t.Fatalf("re-arm generation = %d, want %d", gen2, gen1+1)
	}
	if handle == nil {
		t.Fatal("re-arm left no live handle")
	}

	// The OLD queued callback finally acquires the lock: dropped, zero side
	// effects.
	stale()
	if exec.advanceCount() != 0 {
		t.Errorf("stale queued callback advanced the lesson (%d calls)", exec.advanceCount())
	}
	if ttEventCount(t, log, EventTimeoutFired) != 0 {
		t.Error("timeout_fired emitted by a stale queued callback")
	}
	if s.countOf(TypeLessonUpdate) != 0 {
		t.Error("lesson_update sent by a stale queued callback")
	}
	if gen, h := ttTimerState(tm); gen != gen2 || h == nil {
		t.Error("the stale callback disturbed the new handle")
	}
}

// TestWordTimerEmptyTranscriptionDoesNotReset: an ASR empty transcription
// is tolerated silently — no classification, no user-visible error, and the
// countdown (generation AND handle) is untouched: hearing silence is not an
// interaction.
func TestWordTimerEmptyTranscriptionDoesNotReset(t *testing.T) {
	cards := ttCards("苹果")
	asr := newFakeASR(asrScript{err: mimo.ErrEmptyTranscription})
	llm := newFakeLLM(intentScript(intentConfirm, "")) // must NOT be consumed
	tm, s, log, exec := ttIntentFixture(t, cards, asr, llm)

	gen0, h0 := ttTimerState(tm)
	tm.processTurn(s, []byte("pcm")) // settles synchronously (no announcement)

	gen1, h1 := ttTimerState(tm)
	if gen1 != gen0 || h1 != h0 {
		t.Errorf("timer disturbed by an empty transcription: gen %d→%d", gen0, gen1)
	}
	if llm.callCount() != 0 {
		t.Errorf("classification ran for an empty transcription (%d calls)", llm.callCount())
	}
	if exec.advanceCount() != 0 {
		t.Errorf("advance calls = %d, want 0", exec.advanceCount())
	}
	if got := s.controlsOf(t, TypeError); len(got) != 0 {
		t.Errorf("user-visible errors = %v, want none (silence is not a fault)", got)
	}
	marked := false
	for _, v := range ttEventField(t, log, EventSpeechEnded, "empty") {
		if v == true {
			marked = true
		}
	}
	if !marked {
		t.Error("no speech_ended empty=true marker recorded")
	}
	ttWaitListening(t, s)
}

// TestWordTimerProcessingBranchRearms: the firing during an in-flight
// classification is handled BEFORE the generic state rejection — the firing
// is discarded and a fresh countdown re-armed (no advance); once listening
// resumes, the re-armed countdown drives the real timeout advance.
func TestWordTimerProcessingBranchRearms(t *testing.T) {
	cards := ttCards("苹果", "香蕉")
	cfg := ttConfigWith(newFakeWords(cards), func(c *TurnManagerConfig) { c.WordTimeout = 40 * time.Millisecond })
	tm, s, log, exec := ttIntentFixtureCfg(t, cards, newFakeASR(), newFakeLLM(), cfg)
	gen0, _ := ttTimerState(tm)

	tm.mu.Lock()
	tm.state = StateProcessing
	tm.mu.Unlock()

	if !waitFor(t, 5*time.Second, func() bool {
		g, h := ttTimerState(tm)
		return g > gen0 && h != nil
	}) {
		t.Fatal("the firing during processing never re-armed a fresh countdown")
	}
	if exec.advanceCount() != 0 {
		t.Errorf("advance calls during processing = %d, want 0", exec.advanceCount())
	}
	if ttEventCount(t, log, EventTimeoutFired) != 0 {
		t.Error("timeout_fired emitted for a discarded firing")
	}
	if s.countOf(TypeLessonUpdate) != 0 {
		t.Error("lesson_update sent for a discarded firing")
	}
	tm.mu.Lock()
	st := tm.state
	tm.mu.Unlock()
	if st != StateProcessing {
		t.Errorf("state = %q, want processing (the discard must not disturb the turn)", st)
	}

	// Listening resumes; the re-armed countdown then drives the advance.
	tm.mu.Lock()
	tm.state = StateListening
	tm.mu.Unlock()
	if !waitFor(t, 5*time.Second, func() bool { return s.countOf(TypeLessonUpdate) == 1 }) {
		t.Fatal("the re-armed countdown never advanced after listening resumed")
	}
	if ttEventCount(t, log, EventTimeoutFired) != 1 {
		t.Errorf("timeout_fired events = %d, want 1", ttEventCount(t, log, EventTimeoutFired))
	}
	tm.mu.Lock()
	outcome := tm.tutor.perWordOutcome[0]
	tm.mu.Unlock()
	if outcome != outcomeTimeout {
		t.Errorf("outcome = %q, want timeout", outcome)
	}
	// The advance's next-word announcement stays pending its ack — the test
	// intentionally never acks it, and no further countdown can fire (the
	// announce-ack re-arm point never ran).
}

// TestWordTimerSuspendedWhilePaused: the pause command stops the countdown
// (generation bump, handle dropped); even a hand-crafted firing under the
// current generation is dropped by the paused check, and arming while
// paused does not resurrect it.
func TestWordTimerSuspendedWhilePaused(t *testing.T) {
	cards := ttCards("苹果")
	asr := newFakeASR(asrScript{text: "休息一下"})
	llm := newFakeLLM(intentScript(intentPause, ""))
	cfg := ttConfigWith(newFakeWords(cards), func(c *TurnManagerConfig) { c.WordTimeout = 30 * time.Millisecond })
	tm, s, log, exec := ttIntentFixtureCfg(t, cards, asr, llm, cfg)

	ttRunIntentTurn(t, tm, s, 3) // pause
	if !ttPaused(t, tm) {
		t.Fatal("classroom not paused")
	}
	gen, handle := ttTimerState(tm)
	if handle != nil {
		t.Error("countdown still armed while paused")
	}
	if gen < 2 {
		t.Errorf("timerGen = %d, want the stop bump (>=2)", gen)
	}

	tm.mu.Lock()
	target := tm.tutor.turnTarget()
	cur := tm.timerGen
	tm.mu.Unlock()
	tm.onWordTimeout(cur, 0, target) // fully "valid" except the pause bit
	if exec.advanceCount() != 0 {
		t.Error("timeout advanced while paused")
	}
	if ttEventCount(t, log, EventTimeoutFired) != 0 {
		t.Error("timeout_fired emitted while paused")
	}

	tm.armWordTimer() // must not resurrect the countdown while paused
	if _, handle := ttTimerState(tm); handle != nil {
		t.Error("arming while paused resurrected the countdown")
	}
}

// TestStaleTimerIgnored: a firing with a stale generation, a stale epoch, a
// wrong word index, or a stale expected version is dropped with zero side
// effects — the full TurnTarget is re-verified inside the lock.
func TestStaleTimerIgnored(t *testing.T) {
	cards := ttCards("苹果", "香蕉")
	tm, s, log, exec := ttIntentFixtureCfg(t, cards, newFakeASR(), newFakeLLM(),
		ttConfigWith(newFakeWords(cards), nil)) // 60s default: never fires live
	gen, _ := ttTimerState(tm)
	tm.mu.Lock()
	target := tm.tutor.turnTarget()
	tm.mu.Unlock()

	tm.onWordTimeout(gen-1, 0, target)                                                // stale generation
	tm.onWordTimeout(gen, 7, target)                                                  // stale epoch
	tm.onWordTimeout(gen, 0, TurnTarget{WordIndex: 1, ExpectedVersion: 1, Word: "香蕉"}) // wrong word
	tm.onWordTimeout(gen, 0, TurnTarget{WordIndex: 0, ExpectedVersion: 9, Word: "苹果"}) // stale version

	if exec.advanceCount() != 0 {
		t.Errorf("advance calls = %d, want 0 (every stale firing ignored)", exec.advanceCount())
	}
	if ttEventCount(t, log, EventTimeoutFired) != 0 {
		t.Error("timeout_fired emitted by a stale firing")
	}
	if s.countOf(TypeLessonUpdate) != 0 {
		t.Error("lesson_update sent by a stale firing")
	}
}

// TestTimeoutConfirmRaceConfirmWins: confirm commits first; the late
// timeout carrying the SAME captured target replays Idempotent=true — align
// only, and the confirmed outcome is never overwritten.
func TestTimeoutConfirmRaceConfirmWins(t *testing.T) {
	cards := ttCards("苹果", "香蕉")
	asr := newFakeASR(asrScript{text: "好了"})
	llm := newFakeLLM(intentScript(intentConfirm, ""))
	tm, s, log, exec := ttIntentFixture(t, cards, asr, llm)

	// Confirm first: the real processTurn path commits word0 (the turn
	// settles after the next-word announcement's ack).
	ttRunIntentTurn(t, tm, s, 3)
	if s.countOf(TypeLessonUpdate) != 1 {
		t.Fatalf("lesson_update = %d, want 1 (confirm committed)", s.countOf(TypeLessonUpdate))
	}

	// The timeout arrives late with the SAME target: idempotent replay.
	tm.timeoutAdvance(s, 0, TurnTarget{WordIndex: 0, ExpectedVersion: 1, Word: "苹果"})

	if s.countOf(TypeLessonUpdate) != 1 {
		t.Errorf("lesson_update = %d, want 1 (the losing timeout added none)", s.countOf(TypeLessonUpdate))
	}
	if ttEventCount(t, log, EventTimeoutFired) != 0 {
		t.Error("the losing timeout claimed it advanced (timeout_fired emitted)")
	}
	if ttEventCount(t, log, EventWordAdvanced) != 1 {
		t.Errorf("word_advanced events = %d, want 1", ttEventCount(t, log, EventWordAdvanced))
	}
	tm.mu.Lock()
	outcome := tm.tutor.perWordOutcome[0]
	rating := tm.tutor.ratings()["苹果"]
	tm.mu.Unlock()
	if outcome != outcomeConfirmed {
		t.Errorf("outcome = %q, want confirmed (never overwritten by the timeout)", outcome)
	}
	if rating != "good" {
		t.Errorf("rating = %q, want good", rating)
	}
	if exec.advanceCount() != 2 {
		t.Errorf("advance calls = %d, want 2 (the replay still hits the executor)", exec.advanceCount())
	}
}

// TestTimeoutConfirmRaceTimeoutWins: the timeout commits first (outcome
// hard, timeout_fired emitted); the late confirm with the SAME captured
// target replays idempotently and never overwrites the outcome.
func TestTimeoutConfirmRaceTimeoutWins(t *testing.T) {
	cards := ttCards("苹果", "香蕉")
	tm, s, log, exec := ttIntentFixtureCfg(t, cards, newFakeASR(), newFakeLLM(),
		ttConfigWith(newFakeWords(cards), nil)) // 60s default: no live firing
	tm.mu.Lock()
	gen, target := tm.timerGen, tm.tutor.turnTarget()
	tm.mu.Unlock()

	// Timeout first: fires on its own goroutine, commits, announces the
	// next word; the test acks that announcement.
	fired := make(chan struct{})
	go func() {
		tm.onWordTimeout(gen, 0, target)
		close(fired)
	}()
	if !waitFor(t, 5*time.Second, func() bool { return s.countOf(TypeLessonUpdate) == 1 }) {
		t.Fatal("the timeout never committed")
	}
	ttAwaitSpoken(t, s, 4)
	ttAckLast(t, tm, s, 0)
	select {
	case <-fired:
	case <-time.After(5 * time.Second):
		t.Fatal("the timeout goroutine never returned")
	}

	// The late confirm carries the ORIGINAL captured target: idempotent
	// replay, align only.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	tm.confirmTarget(s, ctx, cancel, 0, 0, "", target)

	if s.countOf(TypeLessonUpdate) != 1 {
		t.Errorf("lesson_update = %d, want 1 (the losing confirm added none)", s.countOf(TypeLessonUpdate))
	}
	if ttEventCount(t, log, EventTimeoutFired) != 1 {
		t.Errorf("timeout_fired events = %d, want 1 (first commit only)", ttEventCount(t, log, EventTimeoutFired))
	}
	if ttEventCount(t, log, EventWordAdvanced) != 1 {
		t.Errorf("word_advanced events = %d, want 1", ttEventCount(t, log, EventWordAdvanced))
	}
	tm.mu.Lock()
	outcome := tm.tutor.perWordOutcome[0]
	rating := tm.tutor.ratings()["苹果"]
	tm.mu.Unlock()
	if outcome != outcomeTimeout {
		t.Errorf("outcome = %q, want timeout (never overwritten by the late confirm)", outcome)
	}
	if rating != "hard" {
		t.Errorf("rating = %q, want hard", rating)
	}
	if exec.advanceCount() != 2 {
		t.Errorf("advance calls = %d, want 2 (the replay still hits the executor)", exec.advanceCount())
	}
}

// ---------------------------------------------------------------------------
// 13-09 Task 3: uplink silence windows + ASR tolerance/retry
// (TestVAD*, TestASR*, TestPlaybackPending*, TestPausedStillRecognizes*)
// ---------------------------------------------------------------------------

// ttUtterance is one VAD-closable utterance burst: 300ms speech + 900ms
// silence (hangover 700ms closes it).
func ttUtterance() []byte {
	return append(vadSine(12000, 300), vadSilence(900)...)
}

// TestVADStartGraceSuppressesEarlyAudio: uplink inside the session_ready
// start grace is dropped before the VAD — a mic-open transient can never
// cut a phantom turn; the same utterance goes through once it lapses.
func TestVADStartGraceSuppressesEarlyAudio(t *testing.T) {
	cards := ttCards("苹果")
	log := &captureLogger{}
	asr := newFakeASR(asrScript{text: "好了"})
	tm := NewTurnManager(asr, newFakeLLM(), newFakeTTS(1, 320), newFakeExecutorNoRow(ttLessonID),
		ttConfigWith(newFakeWords(cards), func(c *TurnManagerConfig) {
			c.VADStartGraceMs = 10 * 1000 // 10s: stays active for the whole test
			c.VADEchoTailMs = 1
		}), NewEventRecorder(log, "t"), nil)
	s := &captureSender{}
	ttInit(t, tm, s)
	ttAwaitSpoken(t, s, 3)
	ttAckLast(t, tm, s, 0)
	ttWaitListening(t, s) // the 1ms tail lapses; the 10s grace holds

	tm.IngestAudio(s, ttUtterance())
	tm.IngestAudio(s, ttUtterance())
	time.Sleep(100 * time.Millisecond)
	if got := len(asr.fedBytes); got != 0 {
		t.Fatalf("asr calls during the start grace = %d, want 0", got)
	}

	// Once the grace lapses (faked clock), the same utterance goes through.
	tm.mu.Lock()
	tm.sessionReadyAt = time.Now().Add(-11 * time.Second)
	tm.mu.Unlock()
	tm.IngestAudio(s, ttUtterance())
	if !waitFor(t, 5*time.Second, func() bool { return len(asr.fedBytes) == 1 }) {
		t.Fatalf("asr calls after the grace = %d, want 1", len(asr.fedBytes))
	}
}

// TestVADEchoTailStartsAtPlaybackAck: a delayed ack keeps gating ordinary
// VAD; the valid ack starts the echo tail which keeps gating for its
// window; only after the tail lapses does ordinary uplink reach the ASR.
// lastPlaybackCompletedAt is stamped by the ack path and ONLY by it.
func TestVADEchoTailStartsAtPlaybackAck(t *testing.T) {
	cards := ttCards("苹果")
	log := &captureLogger{}
	asr := newFakeASR(asrScript{text: "好了"})
	tm := NewTurnManager(asr, newFakeLLM(), newFakeTTS(1, 320), newFakeExecutorNoRow(ttLessonID),
		ttConfigWith(newFakeWords(cards), func(c *TurnManagerConfig) {
			c.VADStartGraceMs = 1
			c.VADEchoTailMs = 400
		}), NewEventRecorder(log, "t"), nil)
	s := &captureSender{}
	ttInit(t, tm, s)

	// Fully spoken (audio_end out), NOT acked: still gated.
	ttAwaitSpoken(t, s, 3)
	tm.IngestAudio(s, ttUtterance())
	time.Sleep(50 * time.Millisecond)
	if got := len(asr.fedBytes); got != 0 {
		t.Fatalf("asr calls during playback-pending = %d, want 0", got)
	}

	// The valid ack lands: listening, but the echo tail keeps gating.
	ttAckLast(t, tm, s, 0)
	ttWaitListening(t, s)
	tm.IngestAudio(s, ttUtterance())
	time.Sleep(50 * time.Millisecond)
	if got := len(asr.fedBytes); got != 0 {
		t.Fatalf("asr calls during the echo tail = %d, want 0", got)
	}

	// Tail lapsed (faked clock): the same utterance goes through.
	tm.mu.Lock()
	tm.echoTailUntil = time.Now().Add(-time.Millisecond)
	tm.mu.Unlock()
	tm.IngestAudio(s, ttUtterance())
	if !waitFor(t, 5*time.Second, func() bool { return len(asr.fedBytes) == 1 }) {
		t.Fatalf("asr calls after the tail = %d, want 1", len(asr.fedBytes))
	}
	tm.mu.Lock()
	stamped := !tm.lastPlaybackCompletedAt.IsZero()
	tm.mu.Unlock()
	if !stamped {
		t.Error("lastPlaybackCompletedAt never stamped by the ack path")
	}
}

// TestVADEchoTailRejectsStaleAck: a stale-epoch ack is rejected with zero
// side effects — the playback gate holds, no tail starts, no listening
// broadcast, and ordinary uplink stays dropped.
func TestVADEchoTailRejectsStaleAck(t *testing.T) {
	cards := ttCards("苹果")
	log := &captureLogger{}
	asr := newFakeASR(asrScript{text: "好了"})
	tm := NewTurnManager(asr, newFakeLLM(), newFakeTTS(1, 320), newFakeExecutorNoRow(ttLessonID),
		ttConfigWith(newFakeWords(cards), func(c *TurnManagerConfig) {
			c.VADStartGraceMs = 1
			c.VADEchoTailMs = 400
		}), NewEventRecorder(log, "t"), nil)
	s := &captureSender{}
	ttInit(t, tm, s)
	ttAwaitSpoken(t, s, 3)

	tm.HandleControl(s, mustJSON(t, SentenceAckMsg{Type: TypeSentenceAck, SentenceID: "t1-s0", Epoch: 9}))
	time.Sleep(50 * time.Millisecond)

	tm.mu.Lock()
	pending := tm.playbackPending
	tailZero := tm.echoTailUntil.IsZero()
	tm.mu.Unlock()
	if !pending {
		t.Error("stale ack cleared the playback gate")
	}
	if !tailZero {
		t.Error("stale ack started the echo tail")
	}
	if s.listeningCount() != 0 {
		t.Error("stale ack restored listening")
	}
	tm.IngestAudio(s, ttUtterance())
	time.Sleep(50 * time.Millisecond)
	if got := len(asr.fedBytes); got != 0 {
		t.Errorf("asr calls after a stale ack = %d, want 0", got)
	}
}

// TestPlaybackPendingInterruptStillImmediate: while ordinary uplink is
// gated by the playback window, an interrupt control message is honored
// immediately — it cancels the announcement and lifts the gate for real
// utterances.
func TestPlaybackPendingInterruptStillImmediate(t *testing.T) {
	cards := ttCards("苹果", "香蕉")
	asr := newFakeASR(asrScript{text: "你好"})
	llm := newFakeLLM(intentScript(intentChat, "好的，听我说。"))
	tts := newFakeTTS(1, 320)
	tm := NewTurnManager(asr, llm, tts, newFakeExecutorNoRow(ttLessonID),
		ttConfigWith(newFakeWords(cards), func(c *TurnManagerConfig) {
			c.VADStartGraceMs = 1
			c.VADEchoTailMs = 1
		}), nil, nil)
	s := &captureSender{}
	ttInit(t, tm, s)
	ttAwaitSpoken(t, s, 3)
	ttAckLast(t, tm, s, 0)
	ttWaitListening(t, s)

	tts.streamUntilCancel = true // only the CHAT reply streams forever
	tts.delay = 50 * time.Millisecond

	base := s.audioCount()
	done := make(chan struct{})
	go func() {
		tm.processTurn(s, []byte("pcm"))
		close(done)
	}()
	if !waitFor(t, 5*time.Second, func() bool { return s.audioCount() >= base+1 }) {
		t.Fatal("teacher never started speaking")
	}

	// Ordinary uplink during the playback window: dropped.
	tm.IngestAudio(s, ttUtterance())
	time.Sleep(50 * time.Millisecond)

	// The interrupt is immediate despite the gate.
	tm.HandleControl(s, mustJSON(t, InterruptMsg{Type: TypeInterrupt}))
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the interrupt did not take effect promptly")
	}
	if s.countOf(TypeInterruptAck) != 1 {
		t.Errorf("interrupt_ack = %d, want 1", s.countOf(TypeInterruptAck))
	}

	// With the gate lifted, a real utterance reaches the ASR (the gated
	// burst above was never transcribed).
	tm.IngestAudio(s, ttUtterance())
	if !waitFor(t, 5*time.Second, func() bool { return len(asr.fedBytes) == 1 }) {
		t.Fatalf("asr calls after the interrupt = %d, want 1", len(asr.fedBytes))
	}
}

// TestPlaybackPendingTimeoutIsVisibleNoTimerAdvance: an ordinary
// announcement whose ack never arrives degrades VISIBLY inside the bounded
// wait (ErrorMsg(playback_ack_timeout)) and orderly-closes — it never
// stamps a playback completion, never starts the echo tail, never arms or
// re-arms the countdown, and never advances a word.
func TestPlaybackPendingTimeoutIsVisibleNoTimerAdvance(t *testing.T) {
	cards := ttCards("苹果")
	asr := newFakeASR(asrScript{text: "你好"})
	llm := newFakeLLM(intentScript(intentChat, "这是一句普通的回复。"))
	tm, s, log, exec := ttIntentFixture(t, cards, asr, llm)
	gen0, _ := ttTimerState(tm) // armed by the greeting ack, 60s: silent
	tm.mu.Lock()
	last0, tail0 := tm.lastPlaybackCompletedAt, tm.echoTailUntil
	tm.mu.Unlock()

	done := make(chan struct{})
	go func() {
		tm.processTurn(s, []byte("pcm"))
		close(done)
	}()
	if !waitFor(t, 5*time.Second, func() bool { return s.countOf(TypeTeacherSentence) > 3 }) {
		t.Fatal("the reply announcement never spoke")
	}
	// No ack is ever sent: the bounded wait (min 3s) must expire in budget.
	if !waitFor(t, 10*time.Second, func() bool {
		for _, e := range s.controlsOf(t, TypeError) {
			if e["code"] == ErrCodePlaybackAckTimeout {
				return true
			}
		}
		return false
	}) {
		t.Fatal("playback_ack_timeout never went out for the ordinary announcement")
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the turn goroutine never returned after the ack timeout")
	}

	tm.mu.Lock()
	closed := tm.closed
	gen1, h1 := tm.timerGen, tm.wordTimer
	last1, tail1 := tm.lastPlaybackCompletedAt, tm.echoTailUntil
	tm.mu.Unlock()
	if !closed {
		t.Error("session not orderly closed after the ack timeout")
	}
	if h1 != nil || gen1 != gen0+1 {
		// The orderly close STOPS the countdown (one generation bump, handle
		// dropped); nothing may have re-armed it.
		t.Errorf("countdown after the timeout: gen %d→%d handle-nil=%v, want exactly the close's stop bump",
			gen0, gen1, h1 == nil)
	}
	if !last1.Equal(last0) || !tail1.Equal(tail0) {
		t.Error("a fabricated playback completion (tail/lastPlaybackCompletedAt moved) after the timeout")
	}
	if exec.advanceCount() != 0 {
		t.Errorf("advance calls = %d, want 0", exec.advanceCount())
	}
	if ttEventCount(t, log, EventWordAdvanced) != 0 || ttEventCount(t, log, EventTimeoutFired) != 0 {
		t.Error("word_advanced/timeout_fired emitted for a timed-out announcement")
	}
}

// TestASREmptyTranscriptionSilentlyIgnoredAndTimerUnchanged: an ASR empty
// transcription is zero-user-visible-fault silence — no error message, no
// classification, no teacher response, no advance, listening restored, and
// the countdown untouched.
func TestASREmptyTranscriptionSilentlyIgnoredAndTimerUnchanged(t *testing.T) {
	cards := ttCards("苹果")
	asr := newFakeASR(asrScript{err: mimo.ErrEmptyTranscription})
	llm := newFakeLLM(intentScript(intentChat, "不应该被调用"))
	tm, s, log, exec := ttIntentFixture(t, cards, asr, llm)
	gen0, h0 := ttTimerState(tm)
	listeningBefore := s.listeningCount()

	tm.processTurn(s, []byte("pcm"))

	if llm.callCount() != 0 {
		t.Errorf("classification calls = %d, want 0 (nothing was said)", llm.callCount())
	}
	if got := s.countOf(TypeTeacherSentence); got != 3 {
		t.Errorf("teacher sentences = %d, want 3 (greeting only; no response turn)", got)
	}
	if got := s.listeningCount(); got <= listeningBefore {
		t.Error("listening not restored after the empty transcription")
	}
	if got := s.controlsOf(t, TypeError); len(got) != 0 {
		t.Errorf("user-visible errors = %v, want none", got)
	}
	if ttEventCount(t, log, EventError) != 0 {
		t.Error("error event emitted for a legal empty transcription")
	}
	if exec.advanceCount() != 0 {
		t.Errorf("advance calls = %d, want 0", exec.advanceCount())
	}
	gen1, h1 := ttTimerState(tm)
	if gen1 != gen0 || h1 != h0 {
		t.Error("the countdown was disturbed by an empty transcription")
	}
}

// TestASRInvalidResponseRetriedOnce: a corrupted ASR response is retried
// EXACTLY once on the same ctx; the recovered transcription proceeds
// through classification normally — no visible failure on the success path.
func TestASRInvalidResponseRetriedOnce(t *testing.T) {
	cards := ttCards("苹果", "香蕉")
	asr := newFakeASR(asrScript{err: mimo.ErrInvalidResponse}, asrScript{text: "好了"})
	llm := newFakeLLM(intentScript(intentConfirm, ""))
	tm, s, log, exec := ttIntentFixture(t, cards, asr, llm)

	ttRunIntentTurn(t, tm, s, 3)

	if len(asr.fedBytes) != 2 {
		t.Errorf("asr calls = %d, want 2 (exactly one retry)", len(asr.fedBytes))
	}
	if llm.callCount() != 1 {
		t.Errorf("classification calls = %d, want 1", llm.callCount())
	}
	if s.countOf(TypeLessonUpdate) != 1 {
		t.Errorf("lesson_update = %d, want 1 (the retry recovered the confirm)", s.countOf(TypeLessonUpdate))
	}
	if exec.advanceCount() != 1 {
		t.Errorf("advance calls = %d, want 1", exec.advanceCount())
	}
	if ttEventCount(t, log, EventError) != 0 {
		t.Error("error event emitted on the recovered path")
	}
}

// TestPausedStillRecognizesResumeThroughRealProcessTurn: while the
// classroom pause bit holds, a student utterance still enters through the
// REAL processTurn (uplink is not gated by the pause overlay), the resume
// intent is recognized, and the classroom un-pauses with the same word
// position.
func TestPausedStillRecognizesResumeThroughRealProcessTurn(t *testing.T) {
	cards := ttCards("苹果")
	asr := newFakeASR(asrScript{text: "休息一下"}, asrScript{text: "老师我们继续吧"})
	llm := newFakeLLM(intentScript(intentPause, ""), intentScript(intentResume, ""))
	tm, s, _, _ := ttIntentFixture(t, cards, asr, llm)

	ttRunIntentTurn(t, tm, s, 3) // pause
	if !ttPaused(t, tm) {
		t.Fatal("pause bit not set before the resume utterance")
	}

	ttRunIntentTurn(t, tm, s, 4) // resume through the real processTurn

	if ttPaused(t, tm) {
		t.Error("classroom still paused after the recognized resume")
	}
	if got := ttSentencesSince(t, s, 4); !contains(got, "还是第 1 个词") || !contains(got, "苹果") {
		t.Errorf("resume ack = %q, want the same word position", got)
	}
	if s.listeningCount() == 0 {
		t.Error("listening not restored after the resume ack")
	}
}
