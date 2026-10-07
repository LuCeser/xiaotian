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
	"math/rand"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xiaotian/home-tutor-agent/pkg/database"
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
// The 13.1 check-stage methods follow the same first-commit/replay/conflict
// shape on the in-memory row (checkResults holds their stored results).
type fakeExecutor struct {
	mu              sync.Mutex
	lesson          lesson.Lesson
	exists          bool
	ensureCalls     int
	ensureCardsCalls int
	stateCalls      int
	advanceCalls    []fakeAdvanceCall
	checkCalls      []fakeCheckCall
	conflictVersion int
	requestResults  map[string]lesson.AdvanceResult
	checkResults    map[string]any
}

type fakeAdvanceCall struct {
	lessonID        string
	expectedVersion int
	requestID       string
}

type fakeCheckCall struct {
	tool            string
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
		checkResults:   map[string]any{},
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

// --- 13.1-01 check-stage fakes (first-commit / replay / conflict, mirroring
// the real executor's tool_requests semantics on the in-memory row) -------

func (f *fakeExecutor) EnterCheck(_ context.Context, lessonID string, expectedVersion int, requestID string, outcomes map[string]lesson.WordOutcome) (lesson.EnterCheckResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.checkCalls = append(f.checkCalls, fakeCheckCall{"enter_check", lessonID, expectedVersion, requestID})
	if stored, ok := f.checkResults[requestID]; ok {
		res := stored.(lesson.EnterCheckResult)
		res.Idempotent = true
		return res, nil
	}
	if f.conflictVersion > 0 {
		return lesson.EnterCheckResult{}, &lesson.ConflictError{CurrentVersion: f.conflictVersion}
	}
	if !f.exists {
		return lesson.EnterCheckResult{}, lesson.ErrLessonNotFound
	}
	f.lesson.Version++
	f.lesson.Stage = lesson.StageChecking
	f.lesson.Outcomes = outcomes
	res := lesson.EnterCheckResult{Stage: f.lesson.Stage, NewVersion: f.lesson.Version, Outcomes: outcomes, Verdicts: f.lesson.Verdicts}
	f.checkResults[requestID] = res
	return res, nil
}

func (f *fakeExecutor) ApplyVerdict(_ context.Context, lessonID string, expectedVersion int, requestID string, verdict lesson.CheckVerdict) (lesson.ApplyVerdictResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.checkCalls = append(f.checkCalls, fakeCheckCall{"apply_verdict", lessonID, expectedVersion, requestID})
	if stored, ok := f.checkResults[requestID]; ok {
		res := stored.(lesson.ApplyVerdictResult)
		res.Idempotent = true
		return res, nil
	}
	if f.conflictVersion > 0 {
		return lesson.ApplyVerdictResult{}, &lesson.ConflictError{CurrentVersion: f.conflictVersion}
	}
	if !f.exists {
		return lesson.ApplyVerdictResult{}, lesson.ErrLessonNotFound
	}
	replaced := false
	for i := range f.lesson.Verdicts {
		if f.lesson.Verdicts[i].WordIndex == verdict.WordIndex {
			f.lesson.Verdicts[i] = verdict
			replaced = true
			break
		}
	}
	if !replaced {
		f.lesson.Verdicts = append(f.lesson.Verdicts, verdict)
	}
	f.lesson.Version++
	res := lesson.ApplyVerdictResult{Stage: f.lesson.Stage, NewVersion: f.lesson.Version, Verdicts: f.lesson.Verdicts}
	f.checkResults[requestID] = res
	return res, nil
}

func (f *fakeExecutor) CompleteCheck(_ context.Context, lessonID string, expectedVersion int, requestID string, unchecked bool) (lesson.CompleteCheckResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.checkCalls = append(f.checkCalls, fakeCheckCall{"complete_check", lessonID, expectedVersion, requestID})
	if stored, ok := f.checkResults[requestID]; ok {
		res := stored.(lesson.CompleteCheckResult)
		res.Idempotent = true
		return res, nil
	}
	if !f.exists {
		return lesson.CompleteCheckResult{}, lesson.ErrLessonNotFound
	}
	if f.lesson.Stage == lesson.StageSubmitted {
		// The latch already fell: idempotent no-op with the stored state.
		return lesson.CompleteCheckResult{Stage: f.lesson.Stage, NewVersion: f.lesson.Version, Unchecked: f.lesson.Unchecked, Idempotent: true}, nil
	}
	if f.conflictVersion > 0 {
		return lesson.CompleteCheckResult{}, &lesson.ConflictError{CurrentVersion: f.conflictVersion}
	}
	f.lesson.Version++
	f.lesson.Stage = lesson.StageSubmitted
	f.lesson.Unchecked = unchecked
	res := lesson.CompleteCheckResult{
		Stage: f.lesson.Stage, NewVersion: f.lesson.Version, Unchecked: unchecked,
		Verdicts: f.lesson.Verdicts, Outcomes: f.lesson.Outcomes,
	}
	f.checkResults[requestID] = res
	return res, nil
}

// ClearUnchecked mirrors the real executor's clear_unchecked semantics on
// the in-memory row (13.1-04): request_id replay, `AND unchecked = 1` gate
// (an already-cleared row is an idempotent no-op), optimistic lock.
func (f *fakeExecutor) ClearUnchecked(_ context.Context, lessonID string, expectedVersion int, requestID string) (lesson.ClearUncheckedResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.checkCalls = append(f.checkCalls, fakeCheckCall{"clear_unchecked", lessonID, expectedVersion, requestID})
	if stored, ok := f.checkResults[requestID]; ok {
		res := stored.(lesson.ClearUncheckedResult)
		res.Idempotent = true
		return res, nil
	}
	if !f.exists {
		return lesson.ClearUncheckedResult{}, lesson.ErrLessonNotFound
	}
	if !f.lesson.Unchecked {
		// Already cleared: idempotent no-op, no version bump.
		return lesson.ClearUncheckedResult{Stage: f.lesson.Stage, NewVersion: f.lesson.Version, Idempotent: true}, nil
	}
	if f.lesson.Version != expectedVersion {
		return lesson.ClearUncheckedResult{}, &lesson.ConflictError{CurrentVersion: f.lesson.Version}
	}
	f.lesson.Version++
	f.lesson.Unchecked = false
	res := lesson.ClearUncheckedResult{Stage: f.lesson.Stage, NewVersion: f.lesson.Version}
	f.checkResults[requestID] = res
	return res, nil
}

func (f *fakeExecutor) checkCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.checkCalls)
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

// TestCheckSubmitWritesReviewAndWaitsForPlaybackAck (13.1-01 rewrite of the
// completion test): the unified write executes BEFORE the summary is spoken
// and ONLY after check_submit; the checked-correct-no-hint word rates good;
// after the summary's audio_end but before its ack the session must NOT end;
// the ack releases session_ended(lesson_complete).
func TestCheckSubmitWritesReviewAndWaitsForPlaybackAck(t *testing.T) {
	words := newFakeWords(ttCards("苹果"))
	words.reviewResult = ReviewWriteResult{Requested: []string{"苹果"}, Updated: []string{"苹果"}}
	tm, s, log, exec := ttConfirmToCheckSummary(t, words)

	// 先提交后总结: the write already ran while the summary plays, and it
	// ran exactly once through the stage latch.
	if got := len(words.submissions()); got != 1 {
		t.Fatalf("SubmitReview calls = %d, want 1 (and before the summary)", got)
	}
	ratings := words.submissions()[0]
	if ratings["苹果"] != "good" {
		t.Errorf("submitted rating = %v, want 苹果:good (checked correct, no hint)", ratings)
	}

	// audio_end seen, no ack: no session end yet.
	time.Sleep(250 * time.Millisecond)
	if got := s.countOf(TypeSessionEnded); got != 0 {
		t.Fatalf("session_ended before the summary ack = %d, want 0", got)
	}

	ttAckSummaryAndEnd(t, tm, s, nil)
	if exec.advanceCount() != 1 {
		t.Errorf("advance calls = %d, want 1", exec.advanceCount())
	}
	updated := ttEventField(t, log, EventReviewSubmitted, "updated")
	if len(updated) != 1 || updated[0] != 1 {
		t.Errorf("review_submitted updated = %v, want [1]", updated)
	}
}

// TestCheckSubmitPartialReviewReportsActualCount (13.1-01 rewrite): nil
// error with a PARTIAL update set is not success — the summary carries the
// honest "saved M/N" note, review_submitted carries the real counts, the
// server records an error marker, and SubmitReview runs exactly once (no
// blind batch retry).
func TestCheckSubmitPartialReviewReportsActualCount(t *testing.T) {
	words := newFakeWords(ttCards("苹果", "香蕉"))
	words.reviewResult = ReviewWriteResult{
		Requested: []string{"苹果", "香蕉"},
		Updated:   []string{"苹果"},
		Missing:   []string{"香蕉"},
	}
	tm, s, log, _ := ttDriveToCheck(t, ttCards("苹果", "香蕉"), words)
	ttCheckAll(t, tm, s, 2)
	ttSubmitCheck(t, tm, s)

	if got := len(words.submissions()); got != 1 {
		t.Fatalf("SubmitReview calls = %d, want exactly 1 (no batch retry)", got)
	}
	ttLastSentenceEventually(t, s, "保存 1/2")
	updated := ttEventField(t, log, EventReviewSubmitted, "updated")
	if len(updated) != 1 || updated[0] != 1 {
		t.Errorf("review_submitted updated = %v, want [1] (real count)", updated)
	}
	requested := ttEventField(t, log, EventReviewSubmitted, "requested")
	if len(requested) != 1 || requested[0] != 2 {
		t.Errorf("review_submitted requested = %v, want [2]", requested)
	}
	if got := ttEventCount(t, log, EventError); got == 0 {
		t.Error("no server-side error marker for the partial update")
	}

	ttAckSummaryAndEnd(t, tm, s, nil)
}

// TestCheckSubmitZeroUpdatedWithNilErrorIsNotSuccess (13.1-01 rewrite): nil
// error + zero updates must still surface as a failure — the spoken summary
// reports 0/1 and the server records the error marker.
func TestCheckSubmitZeroUpdatedWithNilErrorIsNotSuccess(t *testing.T) {
	words := newFakeWords(ttCards("苹果"))
	words.reviewResult = ReviewWriteResult{
		Requested: []string{"苹果"},
		Updated:   nil,
		Missing:   []string{"苹果"},
	}
	tm, s, log, _ := ttConfirmToCheckSummary(t, words)

	if got := len(words.submissions()); got != 1 {
		t.Fatalf("SubmitReview calls = %d, want exactly 1", got)
	}
	ttLastSentenceEventually(t, s, "保存 0/1")
	updated := ttEventField(t, log, EventReviewSubmitted, "updated")
	if len(updated) != 1 || updated[0] != 0 {
		t.Errorf("review_submitted updated = %v, want [0]", updated)
	}
	if got := ttEventCount(t, log, EventError); got == 0 {
		t.Error("no server-side error marker for the zero update")
	}

	ttAckSummaryAndEnd(t, tm, s, nil)
}

// TestCheckSubmitReviewFailureSpeaksHonestSuffix (13.1-01 rewrite): a real
// SubmitReview error appends the record-not-saved wording — the student
// never hears a false "saved" (transparency prohibition).
func TestCheckSubmitReviewFailureSpeaksHonestSuffix(t *testing.T) {
	words := newFakeWords(ttCards("苹果"))
	words.reviewErr = fmt.Errorf("pybridge run review failed")
	tm, s, log, _ := ttConfirmToCheckSummary(t, words)

	if got := len(words.submissions()); got != 1 {
		t.Fatalf("SubmitReview calls = %d, want exactly 1", got)
	}
	ttLastSentenceEventually(t, s, "没有保存成功")
	if got := ttEventCount(t, log, EventReviewSubmitted); got != 0 {
		t.Errorf("review_submitted events = %d, want 0 (the write failed)", got)
	}
	if got := ttEventCount(t, log, EventError); got == 0 {
		t.Error("no server-side error marker for the failed write")
	}

	ttAckSummaryAndEnd(t, tm, s, nil)
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
// completion evidence appears. (13.1-01: the summary only exists after the
// unified submit, so the drive runs through the check stage.)
func TestPlaybackAckTimeoutIsVisibleAndBounded(t *testing.T) {
	words := newFakeWords(ttCards("苹果"))
	words.reviewResult = ReviewWriteResult{Requested: []string{"苹果"}, Updated: []string{"苹果"}}
	tm, s, log, _ := ttDriveToCheck(t, ttCards("苹果"), words)
	ttCheckAll(t, tm, s, 1)
	ttSubmitCheck(t, tm, s) // no summary ack is ever sent

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

	// No fabricated completion evidence: the review ran once through the
	// latch, no extra word_announced (the bridge announcement must not arm
	// the word timer or pretend a word was announced).
	if got := len(words.submissions()); got != 1 {
		t.Errorf("SubmitReview calls = %d, want 1", got)
	}
	if got := ttEventCount(t, log, EventWordAnnounced); got != 1 {
		t.Errorf("word_announced events = %d, want 1 (greeting only; no fabricated completion)", got)
	}
	if got := ttEventCount(t, log, EventPlaybackAckTimeout); got != 1 {
		t.Errorf("playback_ack_timeout events = %d, want 1", got)
	}
	tm.mu.Lock()
	closed := tm.closed
	tm.mu.Unlock()
	if !closed {
		t.Error("session never closed after the degraded ending")
	}
}

// TestCheckSubmitDisconnectCancelsAckWait (13.1-01 rewrite): a disconnect
// during the summary's ack wait cancels the wait — the session closes
// without any further messages (no session_ended, no timeout notice). The
// bounded wait's floor is 3s, so four quiet seconds after the Close prove
// the waiter never fired it.
func TestCheckSubmitDisconnectCancelsAckWait(t *testing.T) {
	words := newFakeWords(ttCards("苹果"))
	words.reviewResult = ReviewWriteResult{Requested: []string{"苹果"}, Updated: []string{"苹果"}}
	tm, s, log, _ := ttConfirmToCheckSummary(t, words)

	tm.Close(s) // transport read loop exited: cancels the ack wait
	time.Sleep(4 * time.Second)
	if got := s.countOf(TypeSessionEnded); got != 0 {
		t.Errorf("session_ended after disconnect = %d, want 0", got)
	}
	if got := ttEventCount(t, log, EventPlaybackAckTimeout); got != 0 {
		t.Errorf("playback_ack_timeout events after disconnect = %d, want 0", got)
	}
}

// ttDriveTwoConfirmCompletion runs a TWO-word lesson through both confirms
// into the check stage (13.1-01: the last commit no longer summarizes — it
// opens the check stage), leaving the classroom waiting on verdicts.
func ttDriveTwoConfirmCompletion(t *testing.T, words *fakeWordSource) (*TurnManager, *captureSender, *captureLogger, *fakeExecutor) {
	return ttDriveToCheck(t, ttCards("苹果", "香蕉"), words)
}

// ttDriveToCheck drives a lesson of len(cards) words through scripted
// confirms into the check stage: greeting acked, every confirm advanced, the
// last commit opened the check stage (check_started seen) and the bridge
// announcement settled through its ack. The caller then applies verdicts and
// submits.
func ttDriveToCheck(t *testing.T, cards []lesson.Word, words *fakeWordSource) (*TurnManager, *captureSender, *captureLogger, *fakeExecutor) {
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
	tm := NewTurnManager(asr, llm, tts, exec, ttConfig(words), NewEventRecorder(log, "t"), nil)
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
			// The next-word announcement blocks the turn until acked.
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
		// The LAST commit opens the check stage instead of a summary.
		ttCheckStartedWanted(t, s)
		// Wait for the bridge sentence BEFORE acking — acking the previous
		// announcement's last sentence would be a rejected duplicate and
		// leave the bridge announcement awaiting forever.
		spokenBefore := s.countOf(TypeTeacherSentence)
		if !waitFor(t, 5*time.Second, func() bool { return s.countOf(TypeTeacherSentence) > spokenBefore }) {
			t.Fatal("check bridge announcement never spoke")
		}
		ttAckLast(t, tm, s, 0) // the check bridge announcement
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("the entering turn never settled after the bridge ack")
		}
	}
	return tm, s, log, exec
}

// ttCheckAll marks every word correct (manual screen verdicts) and waits
// until the authoritative echo covers all n indexes.
func ttCheckAll(t *testing.T, tm *TurnManager, s *captureSender, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		tm.HandleControl(s, mustJSON(t, CheckVerdictMsg{
			Type: TypeCheckVerdict, RequestID: fmt.Sprintf("req-v-%d", i), WordIndex: i, Correct: true,
		}))
	}
	if !waitFor(t, 5*time.Second, func() bool {
		got := s.controlsOf(t, TypeCheckVerdicts)
		if len(got) == 0 {
			return false
		}
		last := got[len(got)-1]
		vs, ok := last["verdicts"].([]any)
		return ok && len(vs) == n
	}) {
		t.Fatalf("verdict echoes never covered all %d words", n)
	}
}

// ttSubmitCheck sends check_submit and waits for the honest review_result.
func ttSubmitCheck(t *testing.T, tm *TurnManager, s *captureSender) map[string]any {
	t.Helper()
	tm.HandleControl(s, mustJSON(t, CheckSubmitMsg{Type: TypeCheckSubmit, RequestID: "req-submit"}))
	var result map[string]any
	if !waitFor(t, 5*time.Second, func() bool {
		got := s.controlsOf(t, TypeReviewResult)
		if len(got) == 0 {
			return false
		}
		result = got[0]
		return true
	}) {
		t.Fatal("review_result never arrived after check_submit")
	}
	return result
}

// ttLastSentenceEventually polls until the most recent teacher sentence
// contains substr — the battle summary streams sentence-by-sentence AFTER
// review_result, so a sentence assertion must wait for the stream to reach
// it instead of reading once.
func ttLastSentenceEventually(t *testing.T, s *captureSender, substr string) {
	t.Helper()
	if !waitFor(t, 5*time.Second, func() bool {
		sents := s.controlsOf(t, TypeTeacherSentence)
		if len(sents) == 0 {
			return false
		}
		last, _ := sents[len(sents)-1]["text"].(string)
		return contains(last, substr)
	}) {
		t.Fatalf("teacher sentences never reached a last sentence containing %q", substr)
	}
}

// ttAckSummaryAndEnd acks the summary's final sentence, waits for
// session_ended(lesson_complete), and (when the caller holds a done channel)
// waits for the completion goroutine to finish.
func ttAckSummaryAndEnd(t *testing.T, tm *TurnManager, s *captureSender, done chan struct{}) {
	t.Helper()
	ttAckLast(t, tm, s, 0)
	if !waitFor(t, 5*time.Second, func() bool { return s.countOf(TypeSessionEnded) == 1 }) {
		t.Fatal("session_ended never arrived after the summary ack")
	}
	ended := s.controlsOf(t, TypeSessionEnded)[0]
	if ended["reason"] != ReasonLessonComplete {
		t.Errorf("session_ended.reason = %v, want %s", ended["reason"], ReasonLessonComplete)
	}
	if done != nil {
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("completion goroutine did not finish")
		}
	}
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

// ttConfirmToCheckSummary drives a ONE-word lesson from session_init through
// the check stage's unified submit: the summary streams and its final
// audio_end is out (summary pending its ack) when this returns. The caller
// owns the summary ack. (Supersedes ttConfirmToCompletion, 13.1-01.)
func ttConfirmToCheckSummary(t *testing.T, words *fakeWordSource) (*TurnManager, *captureSender, *captureLogger, *fakeExecutor) {
	t.Helper()
	tm, s, log, exec := ttDriveToCheck(t, ttCards("苹果"), words)
	ttCheckAll(t, tm, s, 1)
	audioEndBefore := s.countOf(TypeAudioEnd)
	ttSubmitCheck(t, tm, s)
	if !waitFor(t, 5*time.Second, func() bool { return s.countOf(TypeAudioEnd) > audioEndBefore }) {
		t.Fatal("summary audio_end never went out after the unified submit")
	}
	return tm, s, log, exec
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
	// 13.1-02 student semantics (D-08 default mode): undictated words leave
	// the wire as "" — the full reveal rides ?mode=test only (Pitfall 3).
	if len(gotWords) != 3 || gotWords[0] != "" || gotWords[1] != "" || gotWords[2] != "" {
		t.Errorf("session_ready.lesson.words = %v, want 3 masked student entries", gotWords)
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
	// 13.1-02 student masking (D-10): the subtitle shows the masked position,
	// never the word text — the TTS input still speaks it (screen-only mask).
	if !contains(firstWord, "第 1 个词") || contains(firstWord, "苹果") {
		t.Errorf("greeting first word = %q, want the masked 第 1 个词 variant", firstWord)
	}
	spoken := tts.recordedTexts()
	if len(spoken) == 0 || !contains(spoken[len(spoken)-1], "苹果") {
		t.Errorf("tts input = %v, want the raw greeting naming 苹果", spoken)
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
	// word_announced for the NEW index. 13.1-02 student masking (D-10): the
	// subtitle is the masked position variant; TTS keeps the raw word.
	sents := s.controlsOf(t, TypeTeacherSentence)
	last, _ := sents[len(sents)-1]["text"].(string)
	if contains(last, "香蕉") || !contains(last, "第 2 个词") {
		t.Errorf("next announcement = %q, want the masked 第 2 个词 variant", last)
	}
	spoken := tm.tts.(*fakeTTS).recordedTexts()
	if len(spoken) == 0 || !contains(spoken[len(spoken)-1], "香蕉") {
		t.Errorf("tts input = %v, want the raw announce naming 香蕉", spoken)
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
		"review":          ttEventCount(t, log, EventReviewSubmitted),
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
	if got := ttEventCount(t, log, EventReviewSubmitted); got != before["review"] {
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
	if ttEventCount(t, log, EventReviewSubmitted) != 0 {
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

	// 13.1-02 student masking (D-10): the subtitle carries the masked
	// position; TTS still reads the word (voice is the answer channel).
	if got := ttSentencesSince(t, s, 3); got != "好，我再读一遍。第 1 个词。" {
		t.Errorf("repeat announcement = %q, want the masked 第 1 个词 variant", got)
	}
	if spoken := tm.tts.(*fakeTTS).recordedTexts(); len(spoken) == 0 || !contains(spoken[len(spoken)-1], "苹果") {
		t.Errorf("tts input = %v, want the raw repeat reading 苹果", spoken)
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
	// 13.1-02 student masking (D-10): the subtitle masks both readings to the
	// position; TTS still reads the word twice (same spoken text as before).
	if got != "第 1 个词。第 1 个词。慢慢写，不着急。" {
		t.Errorf("slow announcement = %q, want the masked twice-repeated position variant", got)
	}
	if spoken := tm.tts.(*fakeTTS).recordedTexts(); len(spoken) == 0 || !contains(strings.Join(spoken, ""), "苹果。苹果。") {
		t.Errorf("tts input = %v, want the raw slow reading 苹果。苹果。", spoken)
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

// TestCheckRatingMatrix: the check-stage rating table (13.1 D-02 裁定) pinned
// table-driven beside the behavior matrix — checkRating shares
// ratingGood/ratingHard/ratingAgain with wordRating (single matrix family,
// never a fork): 检查对＋无提示=good；检查对＋有提示=hard；检查错=again；
// unknown results skip (""), exactly like the behavior matrix's default.
func TestCheckRatingMatrix(t *testing.T) {
	cases := []struct {
		name    string
		result  string
		hints   int
		want    string
	}{
		{"correct no hint", checkCorrect, 0, "good"},
		{"correct after hints", checkCorrect, 2, "hard"},
		{"wrong no hint", checkWrong, 0, "again"},
		{"wrong after hints", checkWrong, 3, "again"},
		{"unknown result skipped", "bogus", 0, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := checkRating(tc.result, tc.hints); got != tc.want {
				t.Errorf("checkRating(%q, %d) = %q, want %q", tc.result, tc.hints, got, tc.want)
			}
		})
	}
}

// TestUncheckedDowngrade (13.1-04, D-03): the unverified-fallback rating is
// the behavior-matrix value degraded by ONE level — good→hard, hard→again,
// again floors at again, unknown stays "" (skipped). The derivation MUST go
// through wordRating (single matrix discipline, no second matrix).
func TestUncheckedDowngrade(t *testing.T) {
	cases := []struct {
		name    string
		outcome string
		hints   int
		want    string
	}{
		{"confirmed no hint: good→hard", outcomeConfirmed, 0, ratingHard},
		{"confirmed after hints: hard→again", outcomeConfirmed, 1, ratingAgain},
		{"timeout no hint: hard→again", outcomeTimeout, 0, ratingAgain},
		{"timeout after hints: again floors", outcomeTimeout, 2, ratingAgain},
		{"hint pre-record: again floors", outcomeHint, 0, ratingAgain},
		{"unknown outcome stays empty", "bogus", 0, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := downgradeOneRating(wordRating(tc.outcome, tc.hints))
			if got != tc.want {
				t.Errorf("downgradeOneRating(wordRating(%q, %d)) = %q, want %q", tc.outcome, tc.hints, got, tc.want)
			}
		})
	}
	// The pure function itself is pinned directly, too: "" must stay "" so
	// unrecognized words are skipped by the batch assembly, not rated.
	for _, r := range []string{ratingGood, ratingHard, ratingAgain, ""} {
		want := map[string]string{ratingGood: ratingHard, ratingHard: ratingAgain, ratingAgain: ratingAgain, "": ""}[r]
		if got := downgradeOneRating(r); got != want {
			t.Errorf("downgradeOneRating(%q) = %q, want %q", r, got, want)
		}
	}
}

// TestUncheckedFallbackOnClose (13.1-04, D-03 / T-13.1-09): closing the
// session while the tutor still holds stage='checking' persists the fallback
// rating — EXACTLY ONE SubmitReview carrying the downgraded table derived
// from the STORED outcomes — flips the row to stage='submitted' with
// unchecked=1, sends no client messages, and never writes twice: a repeated
// Close AND a late end_session are both no-ops (the CompleteCheck latch plus
// the closed-session guard).
func TestUncheckedFallbackOnClose(t *testing.T) {
	cards := ttCards("苹果", "香蕉")
	words := newFakeWords(cards)
	tm, s, log, exec := ttDriveToCheck(t, cards, words)
	if got := len(words.submissions()); got != 0 {
		t.Fatalf("SubmitReview calls before close = %d, want 0", got)
	}

	// The stored row snapshot (what EnterCheck persisted) is the rating
	// input: word 0 was confirmed after 2 hints (hard → again), word 1
	// confirmed clean (good → hard). Mutating the tutor's memory NOW must
	// not matter — the row is the authority.
	exec.mu.Lock()
	exec.lesson.Outcomes["0"] = lesson.WordOutcome{Outcome: outcomeConfirmed, Hints: 2}
	exec.lesson.Outcomes["1"] = lesson.WordOutcome{Outcome: outcomeConfirmed, Hints: 0}
	exec.mu.Unlock()
	tm.mu.Lock()
	tm.tutor.perWordHints[0] = 99 // memory noise the fallback must ignore
	tm.mu.Unlock()
	words.reviewResult = ReviewWriteResult{
		Requested: []string{"苹果", "香蕉"},
		Updated:   []string{"苹果"},
		Missing:   []string{"香蕉"},
	}

	tm.Close(s) // transport read loop exited mid-check

	subs := words.submissions()
	if len(subs) != 1 {
		t.Fatalf("SubmitReview calls after close = %d, want exactly 1", len(subs))
	}
	if subs[0]["苹果"] != ratingAgain {
		t.Errorf("fallback rating 苹果 = %q, want again (confirmed+2 hints → hard → again)", subs[0]["苹果"])
	}
	if subs[0]["香蕉"] != ratingHard {
		t.Errorf("fallback rating 香蕉 = %q, want hard (confirmed no hint → good → hard)", subs[0]["香蕉"])
	}

	exec.mu.Lock()
	stage, unchecked := exec.lesson.Stage, exec.lesson.Unchecked
	exec.mu.Unlock()
	if stage != lesson.StageSubmitted || !unchecked {
		t.Errorf("row after close = stage %q unchecked %v, want submitted/true (D-03 marker)", stage, unchecked)
	}

	// No client messages belong to the fallback (the connection is gone);
	// the close itself stays the plain transport close.
	if got := s.countOf(TypeSessionEnded); got != 0 {
		t.Errorf("session_ended on disconnect-close = %d, want 0 (fallback sends no messages)", got)
	}

	// Repeated close AND a late end_session frame: the latch holds, no
	// second write, no second CompleteCheck.
	tm.Close(s)
	tm.HandleControl(s, mustJSON(t, EndSessionMsg{Type: TypeEndSession}))
	if got := len(words.submissions()); got != 1 {
		t.Errorf("SubmitReview calls after double close = %d, want still 1", got)
	}
	completeCalls := 0
	for _, c := range exec.checkCalls {
		if c.tool == "complete_check" {
			completeCalls++
		}
	}
	if completeCalls != 1 {
		t.Errorf("CompleteCheck calls = %d, want 1 (idempotent latch)", completeCalls)
	}
	if got := ttEventCount(t, log, EventUncheckedFallback); got != 1 {
		t.Errorf("unchecked_fallback events = %d, want 1", got)
	}
}

// TestCheckedSubmitNotFallback (13.1-04 regression): a lesson submitted
// through the NORMAL check flow is never touched by the close hook — no
// second SubmitReview, no unchecked marker, no fallback event.
func TestCheckedSubmitNotFallback(t *testing.T) {
	cards := ttCards("苹果")
	words := newFakeWords(cards)
	words.reviewResult = ReviewWriteResult{Requested: []string{"苹果"}, Updated: []string{"苹果"}}
	tm, s, log, exec := ttConfirmToCheckSummary(t, words)
	if got := len(words.submissions()); got != 1 {
		t.Fatalf("SubmitReview calls after normal submit = %d, want 1", got)
	}

	tm.Close(s) // closed AFTER the natural submit: the fallback must no-op

	if got := len(words.submissions()); got != 1 {
		t.Errorf("SubmitReview calls after close = %d, want still 1", got)
	}
	exec.mu.Lock()
	stage, unchecked := exec.lesson.Stage, exec.lesson.Unchecked
	exec.mu.Unlock()
	if stage != lesson.StageSubmitted || unchecked {
		t.Errorf("row = stage %q unchecked %v, want submitted/false", stage, unchecked)
	}
	if got := ttEventCount(t, log, EventUncheckedFallback); got != 0 {
		t.Errorf("unchecked_fallback events = %d, want 0 (checked submit never falls back)", got)
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

// TestCheckSubmitReviewCarriesHintAccounting (13.1-01 rewrite): a
// hinted-then-confirmed word checks correct and submits HARD (the D-02
// hint degradation) and review_submitted carries the hint aggregates
// hint_words=1 / hint_total=1 — the rating evidence stays replayable from
// the JSONL stream across the three-stage rewrite.
func TestCheckSubmitReviewCarriesHintAccounting(t *testing.T) {
	cards := ttCards("苹果")
	words := newFakeWords(cards)
	words.reviewResult = ReviewWriteResult{Requested: []string{"苹果"}, Updated: []string{"苹果"}}
	asr := newFakeASR(asrScript{text: "这个字不会写"}, asrScript{text: "好了"})
	llm := newFakeLLM(intentScript(intentHint, "上面是草字头哦"), intentScript(intentConfirm, ""))
	tm, s, log, _ := ttIntentFixtureCfg(t, cards, asr, llm, ttConfig(words))

	// Turn 1: the hint (no advance; the reply re-arms the countdown).
	ttRunIntentTurn(t, tm, s, 3)

	// Turn 2: the confirm commits the word and opens the check stage; the
	// bridge announcement plays and blocks the turn goroutine until acked.
	prev := s.countOf(TypeTeacherSentence)
	done := make(chan struct{})
	go func() {
		tm.processTurn(s, []byte("pcm"))
		close(done)
	}()
	ttCheckStartedWanted(t, s)
	if !waitFor(t, 5*time.Second, func() bool { return s.countOf(TypeTeacherSentence) > prev }) {
		t.Fatal("check bridge announcement never spoke")
	}
	ttAckLast(t, tm, s, 0)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the entering turn never settled after the bridge ack")
	}

	// Checked correct (the student insists they wrote it right) — with the
	// hint on record the unified rating must degrade to hard.
	ttCheckAll(t, tm, s, 1)
	ttSubmitCheck(t, tm, s)

	subs := words.submissions()
	if len(subs) != 1 {
		t.Fatalf("SubmitReview calls = %d, want 1", len(subs))
	}
	if got := subs[0]["苹果"]; got != "hard" {
		t.Errorf("submitted rating = %q, want hard (checked correct after a hint, D-02)", got)
	}
	hintWords := ttEventField(t, log, EventReviewSubmitted, "hint_words")
	if len(hintWords) != 1 || hintWords[0] != 1 {
		t.Errorf("review_submitted hint_words = %v, want [1]", hintWords)
	}
	hintTotal := ttEventField(t, log, EventReviewSubmitted, "hint_total")
	if len(hintTotal) != 1 || hintTotal[0] != 1 {
		t.Errorf("review_submitted hint_total = %v, want [1]", hintTotal)
	}

	ttAckSummaryAndEnd(t, tm, s, nil)
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

	// 13.1-02 student masking (D-10): an LLM reply naming the word is masked
	// on screen (•••) while TTS speaks the reply as generated.
	if got := ttSentencesSince(t, s, 3); got != "•••是一种水果。" {
		t.Errorf("meaning reply subtitle = %q, want the masked ••• reply", got)
	}
	if spoken := tm.tts.(*fakeTTS).recordedTexts(); len(spoken) == 0 || !contains(spoken[len(spoken)-1], "苹果是一种水果。") {
		t.Errorf("tts input = %v, want the raw grounded reply", spoken)
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
	// 13.1-02 student masking (D-10): the same-position confirmation masks
	// the word on screen; TTS still speaks it.
	if !contains(got, "还是第 1 个词") || contains(got, "苹果") {
		t.Errorf("resume ack = %q, want the masked same-position variant", got)
	}
	if spoken := tm.tts.(*fakeTTS).recordedTexts(); len(spoken) == 0 || !contains(spoken[len(spoken)-1], "苹果") {
		t.Errorf("tts input = %v, want the raw resume ack naming 苹果", spoken)
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
	// 13.1-02 student masking (D-10): the resume ack masks the word on
	// screen while keeping the same position.
	if got := ttSentencesSince(t, s, 4); !contains(got, "还是第 1 个词") || contains(got, "苹果") {
		t.Errorf("resume ack = %q, want the masked same-position variant", got)
	}
	if s.listeningCount() == 0 {
		t.Error("listening not restored after the resume ack")
	}
}

// ---------------------------------------------------------------------------
// 13.1-01 tracer: the three-stage manual path (dictation → check → unified
// submit → summary) over a REAL lesson Store (temp SQLite)
// ---------------------------------------------------------------------------

// TestThreeStageManualPath: the tracer's whole chain on the REAL lesson
// executor (temp database, not the in-memory fake — the check stage is a
// persistence-level model change). (a) The last word's first advance commit
// no longer writes ratings: check_started arrives with the FULL plaintext
// list (D-10 unmask point) and reason=dictation_done. (b) Per-word
// check_verdict frames echo the authoritative check_verdicts list
// (manual=true, RT-07 single render source); invalid frames are rejected
// without a write. (c) check_submit latches exactly ONE SubmitReview whose
// ratings follow the D-02 table (检查对＋无提示=good；检查对＋有提示=hard；
// 检查错=again — the hinted word degrades to hard). (d) review_result
// reports the REAL requested/updated/missing sets and the spoken summary
// carries the battle count plus the honest save note. (e) session_ended
// (lesson_complete) waits for the summary's sentence_ack. (f) A duplicate
// check_submit replays: no second FSRS write, no second review_result, no
// second summary (Pitfall 6 / T-13.1-01).
func TestThreeStageManualPath(t *testing.T) {
	cards := ttCards("苹果", "香蕉", "橘子")
	words := newFakeWords(cards)
	words.reviewResult = ReviewWriteResult{
		Requested: []string{"苹果", "香蕉", "橘子"},
		Updated:   []string{"苹果", "橘子"},
		Missing:   []string{"香蕉"},
	}

	// Real lesson Store over a temp database, bound through the production
	// LessonTools adapter (the same seam main.go wires).
	db, err := database.Open(filepath.Join(t.TempDir(), "three-stage.db"))
	if err != nil {
		t.Fatalf("open temp db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := lesson.Migrate(context.Background(), db); err != nil {
		t.Fatalf("migrate temp db: %v", err)
	}
	store, err := lesson.NewStore(db, nil)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	exec, err := lesson.NewExecutor(store, nil)
	if err != nil {
		t.Fatalf("NewExecutor: %v", err)
	}
	tools, err := NewLessonTools(store, exec)
	if err != nil {
		t.Fatalf("NewLessonTools: %v", err)
	}

	// Word 1: confirm. Word 2: hint then confirm (the hint factor must
	// degrade the checked-correct rating to hard). Word 3: confirm.
	asr := newFakeASR(
		asrScript{text: "好了"},
		asrScript{text: "这个字不会写"}, asrScript{text: "好了"},
		asrScript{text: "好了"},
	)
	llm := newFakeLLM(
		intentScript(intentConfirm, ""),
		intentScript(intentHint, "上面是草字头哦"), intentScript(intentConfirm, ""),
		intentScript(intentConfirm, ""),
	)
	tts := newFakeTTS(1, 320)
	log := &captureLogger{}
	tm := NewTurnManager(asr, llm, tts, tools, ttConfig(words), NewEventRecorder(log, "t"), nil)
	s := &captureSender{}
	ttInit(t, tm, s)
	ttAwaitSpoken(t, s, 3)
	ttAckLast(t, tm, s, 0)
	ttWaitListening(t, s)

	// runConfirmTurn drives one confirm utterance and acks whatever
	// announcement it produced (next word or the check bridge).
	spokenBefore := s.countOf(TypeTeacherSentence)
	runConfirmTurn := func() {
		done := make(chan struct{})
		go func() {
			tm.processTurn(s, []byte("pcm"))
			close(done)
		}()
		if !waitFor(t, 5*time.Second, func() bool { return s.countOf(TypeTeacherSentence) > spokenBefore }) {
			t.Fatal("confirm announcement never spoke")
		}
		ttAckLast(t, tm, s, 0)
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("confirm turn never settled after the announcement ack")
		}
	}
	runConfirmTurn()                                          // word 1 → announces word 2
	ttRunIntentTurn(t, tm, s, s.countOf(TypeTeacherSentence)) // hint on word 2
	spokenBefore = s.countOf(TypeTeacherSentence)
	runConfirmTurn() // word 2 → announces word 3
	spokenBefore = s.countOf(TypeTeacherSentence)

	// (a) The LAST confirm enters the check stage instead of writing ratings.
	done := make(chan struct{})
	go func() {
		tm.processTurn(s, []byte("pcm"))
		close(done)
	}()
	var started map[string]any
	if !waitFor(t, 5*time.Second, func() bool {
		got := s.controlsOf(t, TypeCheckStarted)
		if len(got) == 0 {
			return false
		}
		started = got[0]
		return true
	}) {
		t.Fatal("check_started never arrived after the last word advanced")
	}
	gotWords := started["words"].([]any)
	if len(gotWords) != 3 || gotWords[0] != "苹果" || gotWords[1] != "香蕉" || gotWords[2] != "橘子" {
		t.Errorf("check_started.words = %v, want the full plaintext [苹果 香蕉 橘子]", gotWords)
	}
	if started["reason"] != CheckStartedReasonDictationDone {
		t.Errorf("check_started.reason = %v, want %s", started["reason"], CheckStartedReasonDictationDone)
	}
	if got := len(words.submissions()); got != 0 {
		t.Fatalf("SubmitReview calls before check_submit = %d, want 0 (D-01: dictation end writes no rating)", got)
	}
	if ttEventCount(t, log, EventReviewSubmitted) != 0 {
		t.Error("review_submitted emitted before check_submit")
	}

	// The check bridge announcement plays through the ACK gate.
	if !waitFor(t, 5*time.Second, func() bool { return s.countOf(TypeTeacherSentence) > spokenBefore }) {
		t.Fatal("check bridge announcement never spoke")
	}
	ttAckLast(t, tm, s, 0)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the entering turn never settled after the bridge ack")
	}

	// (b) Per-word manual verdicts: 对 / 对 / 错. Each echo carries the FULL
	// stored list with manual=true (the only render source, RT-07).
	verdictCorrect := []bool{true, true, false}
	for i, correct := range verdictCorrect {
		tm.HandleControl(s, mustJSON(t, CheckVerdictMsg{
			Type: TypeCheckVerdict, RequestID: fmt.Sprintf("req-v-%d", i), WordIndex: i, Correct: correct,
		}))
		if !waitFor(t, 5*time.Second, func() bool {
			got := s.controlsOf(t, TypeCheckVerdicts)
			return len(got) == i+1
		}) {
			t.Fatalf("check_verdicts echo %d never arrived", i+1)
		}
	}
	echo := s.controlsOf(t, TypeCheckVerdicts)
	last := echo[len(echo)-1]
	echoVerdicts, ok := last["verdicts"].([]any)
	if !ok || len(echoVerdicts) != 3 {
		t.Fatalf("check_verdicts.verdicts = %v, want 3 entries", last["verdicts"])
	}
	for i, v := range echoVerdicts {
		m := v.(map[string]any)
		if m["word_index"] != float64(i) {
			t.Errorf("verdicts[%d].word_index = %v, want %d", i, m["word_index"], i)
		}
		if m["manual"] != true {
			t.Errorf("verdicts[%d].manual = %v, want true (screen verdicts are manual)", i, m["manual"])
		}
		if m["correct"] != verdictCorrect[i] {
			t.Errorf("verdicts[%d].correct = %v, want %v", i, m["correct"], verdictCorrect[i])
		}
	}

	// Invalid frames are rejected without a write: out-of-range index and an
	// empty request_id each answer a visible error and echo nothing new.
	echoCount := len(s.controlsOf(t, TypeCheckVerdicts))
	tm.HandleControl(s, mustJSON(t, CheckVerdictMsg{
		Type: TypeCheckVerdict, RequestID: "req-bad-range", WordIndex: 9, Correct: true,
	}))
	tm.HandleControl(s, mustJSON(t, CheckVerdictMsg{
		Type: TypeCheckVerdict, RequestID: "", WordIndex: 0, Correct: true,
	}))
	if got := len(s.controlsOf(t, TypeError)); got < 2 {
		t.Errorf("error messages for invalid verdict frames = %d, want >= 2", got)
	}
	if got := len(s.controlsOf(t, TypeCheckVerdicts)); got != echoCount {
		t.Errorf("check_verdicts echoes after invalid frames = %d, want %d (no write)", got, echoCount)
	}

	// (c) check_submit: exactly ONE SubmitReview with the D-02 table ratings.
	tm.HandleControl(s, mustJSON(t, CheckSubmitMsg{Type: TypeCheckSubmit, RequestID: "req-submit-1"}))
	var reviewResult map[string]any
	if !waitFor(t, 5*time.Second, func() bool {
		got := s.controlsOf(t, TypeReviewResult)
		if len(got) == 0 {
			return false
		}
		reviewResult = got[0]
		return true
	}) {
		t.Fatal("review_result never arrived after check_submit")
	}
	subs := words.submissions()
	if len(subs) != 1 {
		t.Fatalf("SubmitReview calls = %d, want exactly 1 (D-07 unified submit)", len(subs))
	}
	wantRatings := map[string]string{
		"苹果": "good",  // checked correct, no hint
		"香蕉": "hard",  // checked correct, after one hint (D-02 degradation)
		"橘子": "again", // checked wrong
	}
	for word, want := range wantRatings {
		if subs[0][word] != want {
			t.Errorf("submitted rating[%s] = %q, want %q", word, subs[0][word], want)
		}
	}

	// (d) Honest result sets + the battle summary. Fake review: 2/3 saved.
	if reviewResult["requested"] != float64(3) || reviewResult["updated"] != float64(2) || reviewResult["missing"] != float64(1) {
		t.Errorf("review_result = %v, want requested 3 / updated 2 / missing 1 (real sets)", reviewResult)
	}
	joined := ttSentencesSince(t, s, 0)
	if !contains(joined, "3 个词对了 2 个") {
		t.Errorf("summary missing the battle count: %s", joined)
	}
	if !contains(joined, "保存 2/3") {
		t.Errorf("summary missing the honest save note (updated 2 of 3): %s", joined)
	}

	// In-session duplicate submit (network retry of the same request_id)
	// replays NOTHING: still one FSRS write, one review_result, no second
	// summary, and the session has not ended yet (summary awaits its ack).
	tm.HandleControl(s, mustJSON(t, CheckSubmitMsg{Type: TypeCheckSubmit, RequestID: "req-submit-1"}))
	time.Sleep(200 * time.Millisecond)
	if got := len(words.submissions()); got != 1 {
		t.Errorf("SubmitReview calls after duplicate submit = %d, want still 1", got)
	}
	if got := s.countOf(TypeReviewResult); got != 1 {
		t.Errorf("review_result count after duplicate submit = %d, want 1 (no second replay message)", got)
	}
	if got := s.countOf(TypeSessionEnded); got != 0 {
		t.Fatalf("session_ended before the summary ack = %d, want 0", got)
	}

	// (e) session_ended(lesson_complete) strictly after the summary ack.
	ttAckLast(t, tm, s, 0)
	if !waitFor(t, 5*time.Second, func() bool { return s.countOf(TypeSessionEnded) == 1 }) {
		t.Fatal("session_ended never arrived after the summary ack")
	}
	ended := s.controlsOf(t, TypeSessionEnded)[0]
	if ended["reason"] != ReasonLessonComplete {
		t.Errorf("session_ended.reason = %v, want %s", ended["reason"], ReasonLessonComplete)
	}
	// Ordering: review_result precedes the last summary sentence (D-04: the
	// summary speaks only after the unified write).
	order := s.typeOrder()
	reviewIdx, lastSentenceIdx := -1, -1
	for i, typ := range order {
		if typ == TypeReviewResult && reviewIdx < 0 {
			reviewIdx = i
		}
		if typ == TypeTeacherSentence {
			lastSentenceIdx = i
		}
	}
	if reviewIdx < 0 || lastSentenceIdx < 0 || reviewIdx > lastSentenceIdx {
		t.Errorf("review_result at %d must precede the summary's last sentence at %d", reviewIdx, lastSentenceIdx)
	}

	// Event evidence: count-class fields only.
	if got := ttEventCount(t, log, EventCheckStarted); got != 1 {
		t.Errorf("check_started events = %d, want 1", got)
	}
	if got := ttEventCount(t, log, EventVerdictApplied); got != 3 {
		t.Errorf("verdict_applied events = %d, want 3", got)
	}
	if got := ttEventCount(t, log, EventReviewSubmitted); got != 1 {
		t.Errorf("review_submitted events = %d, want 1", got)
	}
}

// ttCheckStartedWanted is a tiny helper used by check-stage tests to wait for
// the FIRST check_started message and return it decoded.
func ttCheckStartedWanted(t *testing.T, s *captureSender) map[string]any {
	t.Helper()
	var started map[string]any
	if !waitFor(t, 5*time.Second, func() bool {
		got := s.controlsOf(t, TypeCheckStarted)
		if len(got) == 0 {
			return false
		}
		started = got[0]
		return true
	}) {
		t.Fatal("check_started never arrived")
	}
	return started
}

// ---------------------------------------------------------------------------
// 13.1-02: student-mode send-side masking (U-6/U-7, D-08/D-09/D-10). The
// masking authority is the SERVER SEND side — the DOM/devtools cannot recover
// a word that was never sent. Four TestStudentMasking* behaviors, the
// word_revealed advance reveal, and the Pitfall-4 red line (classification
// input stays raw).
// ---------------------------------------------------------------------------

// ttInitMode is ttInit with an explicit UI mode on session_init.
func ttInitMode(t *testing.T, tm *TurnManager, s *captureSender, mode string) {
	t.Helper()
	tm.HandleControl(s, mustJSON(t, SessionInitMsg{
		Type:  TypeSessionInit,
		Audio: AudioSpec{SampleRate: 16000, Format: "s16le", Channels: 1},
		Mode:  mode,
	}))
}

// TestStudentMaskingLessonView: the student-mode session_ready projection
// carries word text ONLY for i < currentIndex (revealed words); undictated
// positions are "". Test mode keeps the full reveal (13.1 之前 behavior).
func TestStudentMaskingLessonView(t *testing.T) {
	cards := ttCards("苹果", "香蕉", "橘子")
	exec := newFakeExecutor(ttLessonID, cards)
	exec.setRow(1, 2) // mid-lesson reconnect: word 0 revealed
	tm := NewTurnManager(newFakeASR(), newFakeLLM(), newFakeTTS(1, 320), exec, ttConfig(newFakeWords(cards)), nil, nil)
	s := &captureSender{}
	ttInit(t, tm, s) // absent mode → student

	ready := s.controlsOf(t, TypeSessionReady)
	if len(ready) != 1 {
		t.Fatalf("session_ready count = %d, want 1", len(ready))
	}
	got := ready[0]["lesson"].(map[string]any)["words"].([]any)
	if len(got) != 3 || got[0] != "苹果" || got[1] != "" || got[2] != "" {
		t.Errorf("student lesson.words = %v, want [苹果 \"\" \"\"] (i < currentIndex only)", got)
	}

	// Test mode: full reveal, behavior unchanged from before 13.1.
	exec2 := newFakeExecutor(ttLessonID, cards)
	exec2.setRow(1, 2)
	tm2 := NewTurnManager(newFakeASR(), newFakeLLM(), newFakeTTS(1, 320), exec2, ttConfig(newFakeWords(cards)), nil, nil)
	s2 := &captureSender{}
	ttInitMode(t, tm2, s2, ModeTest)
	ready2 := s2.controlsOf(t, TypeSessionReady)
	if len(ready2) != 1 {
		t.Fatalf("test-mode session_ready count = %d, want 1", len(ready2))
	}
	got2 := ready2[0]["lesson"].(map[string]any)["words"].([]any)
	if len(got2) != 3 || got2[0] != "苹果" || got2[1] != "香蕉" || got2[2] != "橘子" {
		t.Errorf("test lesson.words = %v, want the full reveal [苹果 香蕉 橘子]", got2)
	}
}

// TestStudentMaskingAnnounce: student-mode word announcements carry the
// masked 「第 N 个词」 subtitle (never the word text) while the TTS input
// stays the raw template — masking is screen-only (D-10).
func TestStudentMaskingAnnounce(t *testing.T) {
	cards := ttCards("苹果", "香蕉")
	asr := newFakeASR(asrScript{text: "好了"})
	llm := newFakeLLM(intentScript(intentConfirm, ""))
	tts := newFakeTTS(1, 320)
	tm := NewTurnManager(asr, llm, tts, newFakeExecutorNoRow(ttLessonID), ttConfig(newFakeWords(cards)), nil, nil)
	s := &captureSender{}
	ttInit(t, tm, s) // absent mode → student
	ttAwaitSpoken(t, s, 3)

	// No greeting subtitle names the word; the last one is the masked spot.
	sents := s.controlsOf(t, TypeTeacherSentence)
	for _, m := range sents {
		text, _ := m["text"].(string)
		if contains(text, "苹果") {
			t.Errorf("greeting subtitle leaks the word: %q", text)
		}
	}
	last, _ := sents[len(sents)-1]["text"].(string)
	if !contains(last, "第 1 个词") {
		t.Errorf("greeting subtitle = %q, want the masked 第 1 个词 variant", last)
	}
	// TTS still speaks the raw word.
	spoken := tts.recordedTexts()
	if len(spoken) == 0 || !contains(spoken[len(spoken)-1], "苹果") {
		t.Errorf("tts input = %v, want the raw greeting naming 苹果", spoken)
	}

	// The confirm's next-word announcement: masked subtitle, raw TTS.
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
		t.Fatal("confirm turn never settled")
	}
	sents = s.controlsOf(t, TypeTeacherSentence)
	next, _ := sents[len(sents)-1]["text"].(string)
	if contains(next, "香蕉") || !contains(next, "第 2 个词") {
		t.Errorf("next announcement subtitle = %q, want the masked 第 2 个词 variant", next)
	}
	spoken = tts.recordedTexts()
	if !contains(spoken[len(spoken)-1], "香蕉") {
		t.Errorf("tts input = %q, want the raw announce naming 香蕉", spoken[len(spoken)-1])
	}
}

// TestStudentMaskingTranscript: the student's transcript goes out with the
// target word replaced by ••• while the classification input is the RAW
// transcript — the Pitfall-4 red line (fake LLM captures its messages).
func TestStudentMaskingTranscript(t *testing.T) {
	cards := ttCards("apple", "banana")
	asr := newFakeASR(asrScript{text: "apple 写好了"})
	llm := newFakeLLM(intentScript(intentConfirm, ""))
	tm := NewTurnManager(asr, llm, newFakeTTS(1, 320), newFakeExecutorNoRow(ttLessonID), ttConfig(newFakeWords(cards)), nil, nil)
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
	if !waitFor(t, 5*time.Second, func() bool { return s.countOf(TypeTranscript) == 1 }) {
		t.Fatal("transcript never went out")
	}
	tr := s.controlsOf(t, TypeTranscript)[0]
	text, _ := tr["text"].(string)
	if contains(strings.ToLower(text), "apple") {
		t.Errorf("transcript leaks the word: %q", text)
	}
	if !contains(text, "•••") {
		t.Errorf("transcript = %q, want the ••• mask", text)
	}
	// Red line: the classifier saw the RAW transcript.
	if !waitFor(t, 5*time.Second, func() bool { return tm.llm.(*fakeLLM).callCount() == 1 }) {
		t.Fatal("classification never ran")
	}
	var user string
	for _, m := range tm.llm.(*fakeLLM).calls[0].messages {
		if m.Role == "user" {
			user += m.Content
		}
	}
	if !contains(user, "apple 写好了") {
		t.Errorf("classify input = %q, want the raw transcript apple 写好了 (Pitfall 4)", user)
	}
	ttAwaitSpoken(t, s, 4)
	ttAckLast(t, tm, s, 0)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("confirm turn never settled")
	}
}

// TestWordRevealedOnAdvance: the FIRST advance commit delivers
// word_revealed{word_index, word} in student mode so the chip flips to
// 已默=显词＋绿勾; the idempotent replay branch never re-sends.
func TestWordRevealedOnAdvance(t *testing.T) {
	cards := ttCards("苹果", "香蕉")
	asr := newFakeASR(asrScript{text: "好了"})
	llm := newFakeLLM(intentScript(intentConfirm, ""))
	tm := NewTurnManager(asr, llm, newFakeTTS(1, 320), newFakeExecutorNoRow(ttLessonID), ttConfig(newFakeWords(cards)), nil, nil)
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
	if !waitFor(t, 5*time.Second, func() bool { return s.countOf(TypeWordRevealed) == 1 }) {
		t.Fatal("word_revealed never arrived on the first advance commit")
	}
	rv := s.controlsOf(t, TypeWordRevealed)[0]
	if rv["word_index"] != float64(0) || rv["word"] != "苹果" {
		t.Errorf("word_revealed = %v, want index 0 苹果", rv)
	}
	ttAwaitSpoken(t, s, 4)
	ttAckLast(t, tm, s, 0)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("confirm turn never settled")
	}

	// The duplicate confirm replays idempotently: NO second word_revealed.
	before := s.countOf(TypeWordRevealed)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	tm.confirmTarget(s, ctx, cancel, 0, 0, "", TurnTarget{WordIndex: 0, ExpectedVersion: 1})
	if got := s.countOf(TypeWordRevealed); got != before {
		t.Errorf("word_revealed after replay = %d, want %d (first commit only)", got, before)
	}
}

// TestShortHistoryUnmasked: the FSM short-history deque (and the interrupt
// trim) record the RAW spoken text — the LLM context must never see the
// masked copies; only the outbound wire is desensitized.
func TestShortHistoryUnmasked(t *testing.T) {
	cards := ttCards("apple", "banana")
	asr := newFakeASR(asrScript{text: "apple 写好了"})
	llm := newFakeLLM(intentScript(intentConfirm, ""))
	tm := NewTurnManager(asr, llm, newFakeTTS(1, 320), newFakeExecutorNoRow(ttLessonID), ttConfig(newFakeWords(cards)), nil, nil)
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
		t.Fatal("confirm turn never settled")
	}

	tm.mu.Lock()
	var userTexts, teacherTexts []string
	for _, r := range tm.tutor.shortHistory {
		if r.userText != "" {
			userTexts = append(userTexts, r.userText)
		}
		if r.teacherText != "" {
			teacherTexts = append(teacherTexts, r.teacherText)
		}
	}
	tm.mu.Unlock()
	if joined := strings.Join(userTexts, "|"); !contains(joined, "apple 写好了") {
		t.Errorf("shortHistory user text = %q, want the raw transcript (LLM context stays unmasked)", joined)
	}
	if joined := strings.Join(teacherTexts, "|"); !contains(joined, "banana") {
		t.Errorf("shortHistory teacher text = %q, want the raw announcement (LLM context stays unmasked)", joined)
	}
}

// ---------------------------------------------------------------------------
// 13.1-05 practice pure layer: random word picking (D-11), the r0x- practice
// lesson-id namespace (Pitfall 10), and the next-due-days copy input (D-13).
// ---------------------------------------------------------------------------

// ptPracticeCards builds a database.Card pool with distinct words (the
// pickPracticeWords input shape — the CardRepository.List output).
func ptPracticeCards(words ...string) []database.Card {
	cards := make([]database.Card, 0, len(words))
	for _, w := range words {
		cards = append(cards, database.Card{ID: int64(len(cards) + 1), Word: w, Meaning: "释义-" + w, Example: "例句-" + w})
	}
	return cards
}

// ptPickedWords flattens a pick result to its word strings.
func ptPickedWords(picked []lesson.Word) []string {
	out := make([]string, 0, len(picked))
	for _, w := range picked {
		out = append(out, w.Word)
	}
	return out
}

// TestPickPracticeWords: the D-11 sampler — excludes today's words, returns
// exactly n when enough remain, everything when fewer, nil when nothing is
// left (the caller's honest-notice path), and maps card ground truth through.
func TestPickPracticeWords(t *testing.T) {
	pool := ptPracticeCards("apple", "banana", "cat", "dog", "egg")

	t.Run("excludes today's words", func(t *testing.T) {
		picked := pickPracticeWords(pool, map[string]bool{"apple": true, "cat": true}, 10, rand.New(rand.NewSource(1)))
		got := ptPickedWords(picked)
		if len(got) != 3 {
			t.Fatalf("picked = %v, want the 3 non-excluded words", got)
		}
		for _, w := range []string{"apple", "cat"} {
			for _, p := range got {
				if p == w {
					t.Errorf("picked contains excluded word %q: %v", w, got)
				}
			}
		}
		// Card ground truth rides through (the hint/meaning prompts need it).
		if picked[0].Meaning == "" || picked[0].Example == "" {
			t.Errorf("picked[0] = %+v, want Meaning/Example carried from the card", picked[0])
		}
	})

	t.Run("more available than n returns exactly n", func(t *testing.T) {
		picked := pickPracticeWords(pool, nil, 3, rand.New(rand.NewSource(1)))
		got := ptPickedWords(picked)
		if len(got) != 3 {
			t.Fatalf("picked = %v, want exactly 3", got)
		}
		seen := map[string]bool{}
		for _, w := range got {
			if seen[w] {
				t.Errorf("duplicate word %q in %v", w, got)
			}
			seen[w] = true
		}
	})

	t.Run("fewer available than n returns all", func(t *testing.T) {
		picked := pickPracticeWords(pool, nil, 99, rand.New(rand.NewSource(1)))
		if len(picked) != len(pool) {
			t.Errorf("picked = %d words, want all %d (不足全给)", len(picked), len(pool))
		}
	})

	t.Run("zero available returns nil", func(t *testing.T) {
		picked := pickPracticeWords(pool, map[string]bool{"apple": true, "banana": true, "cat": true, "dog": true, "egg": true}, 10, rand.New(rand.NewSource(1)))
		if len(picked) != 0 {
			t.Errorf("picked = %v, want nil/empty (honest-notice path)", picked)
		}
	})

	t.Run("shuffled across seeds", func(t *testing.T) {
		signatures := map[string]bool{}
		for seed := int64(1); seed <= 10; seed++ {
			picked := pickPracticeWords(pool, nil, 5, rand.New(rand.NewSource(seed)))
			signatures[strings.Join(ptPickedWords(picked), ",")] = true
		}
		if len(signatures) < 2 {
			t.Errorf("10 seeds produced %d distinct orderings, want >=2 (randomness)", len(signatures))
		}
	})
}

// TestResolvePracticeLessonID: the r0x- id namespace — Beijing calendar date
// plus a time suffix so multiple same-day practice sets stay distinct, and
// the r0x- prefix keeps every daily r0-YYYYMMDD lookup from ever matching it
// (Pitfall 10).
func TestResolvePracticeLessonID(t *testing.T) {
	cst := time.FixedZone("CST", 8*3600)
	now := time.Date(2026, 10, 8, 12, 30, 45, 0, cst)
	got := resolvePracticeLessonID(now)
	if !strings.HasPrefix(got, "r0x-") {
		t.Errorf("practice lesson id = %q, want the r0x- prefix (never matches the r0-YYYYMMDD lookup)", got)
	}
	if !strings.Contains(got, "20261008") {
		t.Errorf("practice lesson id = %q, want the Beijing-calendar date 20261008", got)
	}
	later := time.Date(2026, 10, 8, 12, 30, 46, 0, cst)
	if resolvePracticeLessonID(now) == resolvePracticeLessonID(later) {
		t.Error("two practice sets one second apart share an id, want distinct (time suffix)")
	}
}

// ptCardWithDue builds one card whose fsrs_card JSON carries the given due
// timestamp (the py-fsrs Card.to_json shape nextDueDays parses).
func ptCardWithDue(word, dueRFC3339 string) database.Card {
	return database.Card{
		Word:     word,
		FsrsCard: `{"due":` + strconv.Quote(dueRFC3339) + `,"stability":1.0,"difficulty":5.0,"reps":1,"lapses":0,"state":"Review"}`,
	}
}

// TestNextDueDays: the no-due copy input (D-13) — the nearest FUTURE due in
// whole days rounded up; a due later TODAY never counts (当天到期不算), an
// unparseable fsrs_card is skipped, and no future due at all returns 0 (the
// copy degrades to the no-days variant).
func TestNextDueDays(t *testing.T) {
	cst := time.FixedZone("CST", 8*3600)
	now := time.Date(2026, 10, 8, 10, 0, 0, 0, cst)

	t.Run("future due rounds up to whole days", func(t *testing.T) {
		cards := []database.Card{
			ptCardWithDue("apple", "2026-10-11T09:00:00+00:00"), // 71h ahead → ceil 3
		}
		if got := nextDueDays(cards, now); got != 3 {
			t.Errorf("nextDueDays = %d, want 3 (向上取整)", got)
		}
	})

	t.Run("nearest future due wins", func(t *testing.T) {
		cards := []database.Card{
			ptCardWithDue("apple", "2026-10-15T00:00:00+00:00"),
			ptCardWithDue("banana", "2026-10-10T02:00:00+00:00"), // ~40h → 2
		}
		if got := nextDueDays(cards, now); got != 2 {
			t.Errorf("nextDueDays = %d, want 2 (min of the future dues)", got)
		}
	})

	t.Run("due later today does not count", func(t *testing.T) {
		cards := []database.Card{
			ptCardWithDue("apple", "2026-10-08T06:00:00+00:00"), // 18h ahead, still TODAY in Beijing
		}
		if got := nextDueDays(cards, now); got != 0 {
			t.Errorf("nextDueDays = %d, want 0 (当天到期不算 → copy degrades)", got)
		}
	})

	t.Run("past and unparseable dues are skipped", func(t *testing.T) {
		cards := []database.Card{
			{Word: "legacy", FsrsCard: ""},
			{Word: "broken", FsrsCard: `{not json`},
			ptCardWithDue("past", "2026-10-01T00:00:00+00:00"),
			ptCardWithDue("tomorrow", "2026-10-09T08:00:00+00:00"), // 22h → 1
		}
		if got := nextDueDays(cards, now); got != 1 {
			t.Errorf("nextDueDays = %d, want 1 (only the tomorrow card counts)", got)
		}
	})

	t.Run("no cards returns zero", func(t *testing.T) {
		if got := nextDueDays(nil, now); got != 0 {
			t.Errorf("nextDueDays(nil) = %d, want 0", got)
		}
	})
}
