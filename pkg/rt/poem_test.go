// poem_test.go — the Issue #12 whole-poem 背默 classroom tests: the scope
// gate and masked session view, the task announcement (never a line), the
// honest refusals (repeat/meaning) and the no-leak line hint, the
// confirm→check bridge, and the whole-poem submit (one practice record, kind
// =poem receipt, honest save failure).
package rt

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xiaotian/home-tutor-agent/pkg/database"
	"github.com/xiaotian/home-tutor-agent/pkg/lesson"
	"github.com/xiaotian/home-tutor-agent/pkg/poem"
)

// ttPoemLines is the fixture work: 静夜思, four ordered lines.
var ttPoemLines = []string{"床前明月光", "疑是地上霜", "举头望明月", "低头思故乡"}

// fakePoemSource pins one poem behind PoemSource.
type fakePoemSource struct {
	err error
	p   poem.Poem
}

func (f *fakePoemSource) FetchPoem(_ context.Context, _ int64) (poem.Poem, error) {
	return f.p, f.err
}

// fakePoemPractice captures the whole-poem practice writes.
type fakePoemPractice struct {
	err     error
	records []poem.PracticeRecord
	mu      sync.Mutex
}

func (f *fakePoemPractice) RecordPractice(_ context.Context, r poem.PracticeRecord) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.records = append(f.records, r)
	return nil
}

func (f *fakePoemPractice) recorded() []poem.PracticeRecord {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]poem.PracticeRecord(nil), f.records...)
}

// ttInScopePoem builds the fixture work as an in-scope supplementary poem.
func ttInScopePoem() poem.Poem {
	return poem.Poem{
		ID: 42, Title: "静夜思", Author: "李白", Category: "chinese",
		Lines:   append([]string(nil), ttPoemLines...),
		InScope: true, ScopeReason: poem.ScopeSupplementary,
	}
}

// poemIntentScript builds one LLM script carrying the FULL intent payload
// (the hint cue needs the 1-based index).
func poemIntentScript(intent, reply string, index int) llmScript {
	payload, _ := json.Marshal(map[string]any{"intent": intent, "reply": reply, "index": index})
	wire := `{"choices":[{"index":0,"message":{"role":"assistant","content":` + strconv.Quote(string(payload)) + `}}]}`
	return llmScript{wire: wire}
}

// newPoemTestManager wires a poem-classroom manager: a fresh executor row,
// the fake poem source/practice store, and NO word source (the poem
// classroom must never touch it).
func newPoemTestManager(t *testing.T, asr *fakeASR, llm *fakeLLM, mutate func(*TurnManagerConfig)) (*TurnManager, *captureSender, *fakeExecutor, *fakePoemPractice) {
	t.Helper()
	exec := newFakeExecutorNoRow(ttLessonID)
	practice := &fakePoemPractice{}
	cfg := ttConfigWith(nil, func(c *TurnManagerConfig) {
		c.Poems = &fakePoemSource{p: ttInScopePoem()}
		c.PoemPractice = practice
		if mutate != nil {
			mutate(c)
		}
	})
	tm := NewTurnManager(asr, llm, newFakeTTS(2, 320), exec, cfg, NewEventRecorder(&captureLogger{}, "t"), nil)
	s := &captureSender{}
	return tm, s, exec, practice
}

// ttPoemInit opens the poem classroom and settles the greeting.
func ttPoemInit(t *testing.T, tm *TurnManager, s *captureSender, poemID int64, mode string) map[string]any {
	t.Helper()
	tm.HandleControl(s, mustJSON(t, SessionInitMsg{
		Type:  TypeSessionInit,
		Mode:  mode,
		Poem:  poemID,
		Audio: AudioSpec{SampleRate: 16000, Format: "s16le", Channels: 1},
	}))
	ready := s.controlsOf(t, TypeSessionReady)
	if len(ready) != 1 {
		t.Fatalf("session_ready count = %d, want 1", len(ready))
	}
	ttAwaitSpoken(t, s, 1)
	ttAckLast(t, tm, s, 0)
	ttWaitListening(t, s)
	return ready[0]
}

// ttPoemTurn runs one voice turn against the poem classroom and settles its
// announcement (the caller scripts the ASR text and the intent payload).
func ttPoemTurn(t *testing.T, tm *TurnManager, s *captureSender) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		tm.processTurn(s, []byte("pcm"))
		close(done)
	}()
	spokenBefore := s.countOf(TypeTeacherSentence)
	if !waitFor(t, 5*time.Second, func() bool { return s.countOf(TypeTeacherSentence) > spokenBefore }) {
		t.Fatal("the poem turn never spoke its announcement")
	}
	ttAckLast(t, tm, s, 0)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the poem turn never settled after its ack")
	}
}

// allTeacherTexts concatenates every teacher sentence so far (leak probes).
func allTeacherTexts(t *testing.T, s *captureSender) string {
	t.Helper()
	var b strings.Builder
	for _, m := range s.controlsOf(t, TypeTeacherSentence) {
		text, _ := m["text"].(string)
		b.WriteString(text)
		b.WriteString("\n")
	}
	return b.String()
}

// TestPoemSessionInitMaskedTask — the classroom opens on an IN-SCOPE whole
// work: session_ready carries the poem task view (never the text), the
// lesson view stays masked in EVERY mode (even test — 背默 must not hand out
// the original), and the greeting announces the TASK without any line.
func TestPoemSessionInitMaskedTask(t *testing.T) {
	for _, mode := range []string{ModeStudent, ModeTest} {
		tm, s, exec, _ := newPoemTestManager(t, newFakeASR(), newFakeLLM(), nil)
		ready := ttPoemInit(t, tm, s, 42, mode)

		poemView, ok := ready["poem"].(map[string]any)
		if !ok {
			t.Fatalf("mode %s: session_ready has no poem view: %v", mode, ready)
		}
		if poemView["title"] != "静夜思" || poemView["author"] != "李白" {
			t.Errorf("mode %s: poem view = %v, want 静夜思/李白", mode, poemView)
		}
		if poemView["method"] != "背默" {
			t.Errorf("mode %s: poem method = %v, want 背默", mode, poemView["method"])
		}
		if lineCount := int(poemView["line_count"].(float64)); lineCount != 4 {
			t.Errorf("mode %s: line_count = %d, want 4", mode, lineCount)
		}
		if ready["category"] != CategoryChinese {
			t.Errorf("mode %s: category = %v, want chinese (poems are 语文)", mode, ready["category"])
		}

		lessonView, _ := ready["lesson"].(map[string]any)
		words, _ := lessonView["words"].([]any)
		if len(words) != 4 {
			t.Fatalf("mode %s: lesson words = %v, want 4 slots", mode, words)
		}
		for i, w := range words {
			if w != "" {
				t.Errorf("mode %s: words[%d] = %v, want masked \"\" pre-check", mode, i, w)
			}
		}

		spoken := allTeacherTexts(t, s)
		if !strings.Contains(spoken, "静夜思") || !strings.Contains(spoken, "默写") {
			t.Errorf("mode %s: greeting = %q, want the 背默 task copy naming the work", mode, spoken)
		}
		for _, line := range ttPoemLines {
			if strings.Contains(spoken, line) {
				t.Errorf("mode %s: greeting leaked the line %q", mode, line)
			}
		}

		// The attempt row: r0p- id, poem_id stamped, chinese, lines as cards.
		exec.mu.Lock()
		id, poemID, category, n := exec.lesson.LessonID, exec.lesson.PoemID, exec.lesson.Category, len(exec.lesson.Cards)
		exec.mu.Unlock()
		if !strings.HasPrefix(id, "r0p-") || poemID != 42 || category != lesson.CategoryChinese || n != 4 {
			t.Fatalf("mode %s: row = %s poem %d %s %d cards", mode, id, poemID, category, n)
		}
		if exec.advanceCount() != 0 {
			t.Errorf("mode %s: advance calls = %d, want 0 (no per-line pacing)", mode, exec.advanceCount())
		}
	}
}

// TestPoemInitGates — the honest init refusals: no source, unknown poem,
// out-of-scope poem each answer a coded error and never a word lesson.
func TestPoemInitGates(t *testing.T) {
	newTM := func(src PoemSource) (*TurnManager, *captureSender, *fakeExecutor) {
		exec := newFakeExecutorNoRow(ttLessonID)
		cfg := ttConfigWith(nil, func(c *TurnManagerConfig) { c.Poems = src })
		tm := NewTurnManager(newFakeASR(), newFakeLLM(), newFakeTTS(2, 320), exec, cfg, NewEventRecorder(&captureLogger{}, "t"), nil)
		return tm, &captureSender{}, exec
	}

	// Nil source.
	tm, s, exec := newTM(nil)
	tm.HandleControl(s, mustJSON(t, SessionInitMsg{Type: TypeSessionInit, Poem: 42, Audio: AudioSpec{SampleRate: 16000, Format: "s16le", Channels: 1}}))
	if s.countOf(TypeError) != 1 || s.countOf(TypeSessionReady) != 0 {
		t.Errorf("nil source: errors=%d ready=%d, want a coded error and no session", s.countOf(TypeError), s.countOf(TypeSessionReady))
	}
	if exec.ensureCalls != 0 {
		t.Errorf("nil source: ensure calls = %d, want 0", exec.ensureCalls)
	}

	// Unknown poem.
	tm, s, _ = newTM(&fakePoemSource{err: poem.ErrPoemNotFound})
	tm.HandleControl(s, mustJSON(t, SessionInitMsg{Type: TypeSessionInit, Poem: 99, Audio: AudioSpec{SampleRate: 16000, Format: "s16le", Channels: 1}}))
	if s.countOf(TypeError) != 1 || s.countOf(TypeSessionReady) != 0 {
		t.Errorf("unknown poem: errors=%d ready=%d, want a coded error and no session", s.countOf(TypeError), s.countOf(TypeSessionReady))
	}

	// Out of scope (progress_pending): the honest refusal — never a silent
	// fallback into a due-word lesson.
	outOfScope := ttInScopePoem()
	outOfScope.InScope = false
	outOfScope.ScopeReason = poem.ScopeProgressPending
	tm, s, _ = newTM(&fakePoemSource{p: outOfScope})
	tm.HandleControl(s, mustJSON(t, SessionInitMsg{Type: TypeSessionInit, Poem: 42, Audio: AudioSpec{SampleRate: 16000, Format: "s16le", Channels: 1}}))
	if s.countOf(TypeError) != 1 || s.countOf(TypeSessionReady) != 0 {
		t.Errorf("out-of-scope poem: errors=%d ready=%d, want a coded error and no session", s.countOf(TypeError), s.countOf(TypeSessionReady))
	}
	if exec.ensureCalls != 0 {
		t.Errorf("out-of-scope poem: ensure calls = %d, want 0", exec.ensureCalls)
	}
}

// TestPoemRepeatRefusedAndHintCueNoLeak — the 背默 red line: repeat is
// refused with fixed copy, and a hint for line 2 speaks ONLY that line's
// opening characters; no teacher sentence ever carries a full line.
func TestPoemRepeatRefusedAndHintCueNoLeak(t *testing.T) {
	asr := newFakeASR(asrScript{text: "这句想不起来了"}, asrScript{text: "第二句不会"})
	llm := newFakeLLM(
		poemIntentScript(intentRepeat, "老师再读一遍", 0),
		poemIntentScript(intentHint, "给你提示", 2),
	)
	tm, s, _, _ := newPoemTestManager(t, asr, llm, nil)
	ttPoemInit(t, tm, s, 42, ModeStudent)

	ttPoemTurn(t, tm, s) // repeat → the honest refusal
	ttPoemTurn(t, tm, s) // hint line 2 → the opening cue

	spoken := allTeacherTexts(t, s)
	if !strings.Contains(spoken, "老师不能把诗句念给你听") {
		t.Errorf("the repeat refusal copy never spoke: %q", spoken)
	}
	if !strings.Contains(spoken, "第 2 句") || !strings.Contains(spoken, "疑是") {
		t.Errorf("the line-2 cue never spoke its opening characters: %q", spoken)
	}
	for _, line := range ttPoemLines {
		if strings.Contains(spoken, line) {
			t.Errorf("a teacher sentence leaked the full line %q", line)
		}
	}

	// The hint accounting landed on line 2's outcome slot (U-9).
	tm.mu.Lock()
	outcomes := tm.tutor.snapshotCheckOutcomes()
	tm.mu.Unlock()
	if o, ok := outcomes["1"]; !ok || o.Hints != 1 {
		t.Errorf("line-2 outcome = %v (ok=%v), want one recorded hint", o, ok)
	}
}

// TestPoemConfirmSubmitWholeScope — 写完了 opens the check panel against the
// poem's own lines (reason poem_done); manual per-line verdicts gate the
// submit; ONE whole-poem practice record lands (3/4 correct, not mastered)
// with a kind=poem receipt whose requested set is the TITLE alone, and the
// session ends lesson_complete.
func TestPoemConfirmSubmitWholeScope(t *testing.T) {
	db, err := database.Open(filepath.Join(t.TempDir(), "poem-receipt.db"))
	if err != nil {
		t.Fatalf("open receipt db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()
	if err = lesson.Migrate(ctx, db); err != nil {
		t.Fatalf("migrate receipt db: %v", err)
	}
	receipts, err := lesson.NewReceiptStore(db, nil)
	if err != nil {
		t.Fatalf("NewReceiptStore: %v", err)
	}

	asr := newFakeASR(asrScript{text: "写完了"})
	llm := newFakeLLM(poemIntentScript(intentConfirm, "好了", 0))
	tm, s, exec, practice := newPoemTestManager(t, asr, llm, func(c *TurnManagerConfig) {
		c.Receipts = receipts
	})
	ttPoemInit(t, tm, s, 42, ModeStudent)

	// 写完了 → the check panel (blocks on the bridge announcement's ack).
	spokenBefore := s.countOf(TypeTeacherSentence)
	done := make(chan struct{})
	go func() {
		tm.processTurn(s, []byte("pcm"))
		close(done)
	}()
	started := ttCheckStartedWanted(t, s)
	if started["reason"] != CheckStartedReasonPoemDone {
		t.Errorf("check_started.reason = %v, want %s", started["reason"], CheckStartedReasonPoemDone)
	}
	words, _ := started["words"].([]any)
	if len(words) != 4 || words[0] != ttPoemLines[0] {
		t.Fatalf("check rows = %v, want the poem's four lines in order", words)
	}
	if !waitFor(t, 5*time.Second, func() bool { return s.countOf(TypeTeacherSentence) > spokenBefore }) {
		t.Fatal("the poem check bridge never spoke")
	}
	ttAckLast(t, tm, s, 0)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the confirm turn never settled")
	}

	// Manual verdicts: lines 1-3 对, line 4 错 (the whole-poem judgment).
	for i := 0; i < 4; i++ {
		tm.HandleControl(s, mustJSON(t, CheckVerdictMsg{
			Type: TypeCheckVerdict, RequestID: "pv-" + strconv.Itoa(i), WordIndex: i, Correct: i < 3,
		}))
	}
	tm.HandleControl(s, mustJSON(t, CheckSubmitMsg{Type: TypeCheckSubmit, RequestID: "poem-submit"}))

	var result map[string]any
	if !waitFor(t, 5*time.Second, func() bool {
		got := s.controlsOf(t, TypeReviewResult)
		if len(got) == 0 {
			return false
		}
		result = got[0]
		return true
	}) {
		t.Fatal("review_result never arrived after the poem submit")
	}
	if int(result["requested"].(float64)) != 1 || int(result["updated"].(float64)) != 1 {
		t.Errorf("review_result = %v, want requested=1 updated=1 (one whole-poem save)", result)
	}
	// The wrap line streams sentence-by-sentence AFTER review_result — wait
	// for the 3/4 result copy before acking the final sentence.
	if !waitFor(t, 5*time.Second, func() bool { return strings.Contains(allTeacherTexts(t, s), "对了 3 句") }) {
		t.Fatalf("the summary never spoke the 3/4 line result: %q", allTeacherTexts(t, s))
	}
	ttAckLast(t, tm, s, 0)
	if !waitFor(t, 5*time.Second, func() bool { return s.countOf(TypeSessionEnded) == 1 }) {
		t.Fatal("session_ended never arrived after the poem summary ack")
	}
	if ended := s.controlsOf(t, TypeSessionEnded)[0]; ended["reason"] != ReasonLessonComplete {
		t.Errorf("session_ended.reason = %v, want %s", ended["reason"], ReasonLessonComplete)
	}
	if !strings.Contains(allTeacherTexts(t, s), "对了 3 句") {
		t.Errorf("the summary never spoke the 3/4 line result: %q", allTeacherTexts(t, s))
	}

	// ONE practice record, whole-poem scope.
	records := practice.recorded()
	if len(records) != 1 {
		t.Fatalf("practice records = %d, want exactly 1 (one row per attempt, never per line)", len(records))
	}
	rec := records[0]
	if rec.PoemID != 42 || rec.TotalLines != 4 || rec.CorrectLines != 3 || rec.Mastered {
		t.Errorf("record = %+v, want poem 42 3/4 not mastered", rec)
	}
	if rec.ReviewedDay == "" {
		t.Error("record reviewed_day empty, want the Beijing day")
	}

	// The receipt: kind poem, requested = the title ALONE.
	got, err := receipts.ListByLesson(ctx, exec.lesson.LessonID)
	if err != nil {
		t.Fatalf("ListByLesson: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("receipt rows = %d, want 1", len(got))
	}
	rc := got[0]
	if rc.Kind != lesson.ReceiptKindPoem || rc.Status != lesson.ReceiptStatusSaved {
		t.Errorf("receipt = %s/%s, want poem/saved", rc.Kind, rc.Status)
	}
	if want := []string{"静夜思"}; len(rc.Requested) != 1 || rc.Requested[0] != want[0] {
		t.Errorf("receipt requested = %v, want %v (the whole work, not per-line entries)", rc.Requested, want)
	}

	// The row settled submitted under the poem id.
	exec.mu.Lock()
	stage, poemID := exec.lesson.Stage, exec.lesson.PoemID
	exec.mu.Unlock()
	if stage != lesson.StageSubmitted || poemID != 42 {
		t.Errorf("row = stage %s poem %d, want submitted/42", stage, poemID)
	}
}

// TestPoemSubmitSaveFailure — a failed practice-record write is reported
// honestly: review_result updated=0, the receipt flips failed, the judgment
// (stage=submitted) survives, and the session still ends normally.
func TestPoemSubmitSaveFailure(t *testing.T) {
	db, err := database.Open(filepath.Join(t.TempDir(), "poem-receipt-fail.db"))
	if err != nil {
		t.Fatalf("open receipt db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()
	if err = lesson.Migrate(ctx, db); err != nil {
		t.Fatalf("migrate receipt db: %v", err)
	}
	receipts, err := lesson.NewReceiptStore(db, nil)
	if err != nil {
		t.Fatalf("NewReceiptStore: %v", err)
	}

	asr := newFakeASR(asrScript{text: "写完了"})
	llm := newFakeLLM(poemIntentScript(intentConfirm, "好了", 0))
	tm, s, exec, practice := newPoemTestManager(t, asr, llm, func(c *TurnManagerConfig) {
		c.Receipts = receipts
		c.PoemPractice = &fakePoemPractice{err: errors.New("poem db down")}
	})
	ttPoemInit(t, tm, s, 42, ModeStudent)

	spokenBefore := s.countOf(TypeTeacherSentence)
	done := make(chan struct{})
	go func() {
		tm.processTurn(s, []byte("pcm"))
		close(done)
	}()
	ttCheckStartedWanted(t, s)
	if !waitFor(t, 5*time.Second, func() bool { return s.countOf(TypeTeacherSentence) > spokenBefore }) {
		t.Fatal("the poem check bridge never spoke")
	}
	ttAckLast(t, tm, s, 0)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the confirm turn never settled")
	}

	for i := 0; i < 4; i++ {
		tm.HandleControl(s, mustJSON(t, CheckVerdictMsg{
			Type: TypeCheckVerdict, RequestID: "fv-" + strconv.Itoa(i), WordIndex: i, Correct: true,
		}))
	}
	tm.HandleControl(s, mustJSON(t, CheckSubmitMsg{Type: TypeCheckSubmit, RequestID: "poem-submit-fail"}))

	var result map[string]any
	if !waitFor(t, 5*time.Second, func() bool {
		got := s.controlsOf(t, TypeReviewResult)
		if len(got) == 0 {
			return false
		}
		result = got[0]
		return true
	}) {
		t.Fatal("review_result never arrived after the failed poem submit")
	}
	if int(result["updated"].(float64)) != 0 || int(result["missing"].(float64)) != 1 {
		t.Errorf("review_result = %v, want updated=0 missing=1 (the honest miss)", result)
	}
	if len(practice.recorded()) != 0 {
		t.Errorf("practice records = %d, want 0 (the write failed)", len(practice.recorded()))
	}

	got, err := receipts.ListByLesson(ctx, exec.lesson.LessonID)
	if err != nil {
		t.Fatalf("ListByLesson: %v", err)
	}
	if len(got) != 1 || got[0].Status != lesson.ReceiptStatusFailed {
		t.Fatalf("receipt = %+v, want one failed row", got)
	}
	exec.mu.Lock()
	stage := exec.lesson.Stage
	exec.mu.Unlock()
	if stage != lesson.StageSubmitted {
		t.Errorf("row stage = %s, want submitted (the judgment survives the save failure)", stage)
	}

	ttLastSentenceEventually(t, s, "没有保存成功")
	ttAckLast(t, tm, s, 0)
	if !waitFor(t, 5*time.Second, func() bool { return s.countOf(TypeSessionEnded) == 1 }) {
		t.Fatal("session_ended never arrived after the failed-save summary")
	}
}

// TestPoemResumeInFlightCheck — a reconnect onto a poem row whose check
// never finished reopens the PANEL under the SAME lesson id (no fresh
// attempt row) and keeps the poem identity.
func TestPoemResumeInFlightCheck(t *testing.T) {
	exec := newFakeExecutor(ttLessonID, ttCards(ttPoemLines...))
	exec.mu.Lock()
	exec.lesson.PoemID = 42
	exec.lesson.Stage = lesson.StageChecking
	exec.mu.Unlock()

	cfg := ttConfigWith(nil, func(c *TurnManagerConfig) {
		c.Poems = &fakePoemSource{p: ttInScopePoem()}
		c.PoemPractice = &fakePoemPractice{}
	})
	tm := NewTurnManager(newFakeASR(), newFakeLLM(), newFakeTTS(2, 320), exec, cfg, NewEventRecorder(&captureLogger{}, "t"), nil)
	s := &captureSender{}
	tm.HandleControl(s, mustJSON(t, SessionInitMsg{
		Type: TypeSessionInit, Poem: 42,
		Audio: AudioSpec{SampleRate: 16000, Format: "s16le", Channels: 1},
	}))

	if got := s.countOf(TypeSessionReady); got != 1 {
		t.Fatalf("session_ready = %d, want 1", got)
	}
	ready := s.controlsOf(t, TypeSessionReady)[0]
	if _, ok := ready["poem"].(map[string]any); !ok {
		t.Errorf("the resumed session_ready carries no poem view: %v", ready)
	}
	if started := ttCheckStartedWanted(t, s); started["reason"] != CheckStartedReasonResume {
		t.Errorf("check_started.reason = %v, want resume (the SAME row reopens)", started["reason"])
	}
	// No fresh attempt row was ensured.
	if exec.ensureCalls != 0 {
		t.Errorf("ensure calls = %d, want 0 (the in-flight row resumes)", exec.ensureCalls)
	}
	tm.mu.Lock()
	poemBound := tm.tutor.poem != nil && tm.tutor.poem.Title == "静夜思"
	tm.mu.Unlock()
	if !poemBound {
		t.Error("the resumed tutor lost the poem identity")
	}
}

func TestResolvePoemLessonIDSeparatesWorksInSameSecond(t *testing.T) {
	now := time.Date(2026, 10, 18, 8, 0, 0, 123, time.UTC)
	a := resolvePoemLessonID(now, 42)
	b := resolvePoemLessonID(now, 43)
	if a == b {
		t.Fatalf("two works received the same attempt id %q", a)
	}
	if !strings.HasPrefix(a, "r0p-") || !strings.HasSuffix(a, "-42") {
		t.Fatalf("poem attempt id = %q, want r0p namespace and work identity", a)
	}
}

// A connection loss during the human check keeps the same attempt open.
// Re-entering the poem must restore that check instead of starting the
// writing task again and abandoning the already confirmed rows.
func TestPoemDisconnectDuringCheckResumesSameAttempt(t *testing.T) {
	exec := newFakeExecutor(ttLessonID, ttCards(ttPoemLines...))
	exec.mu.Lock()
	exec.lesson.PoemID = 42
	exec.lesson.Stage = lesson.StageChecking
	exec.mu.Unlock()

	config := func(c *TurnManagerConfig) {
		c.Poems = &fakePoemSource{p: ttInScopePoem()}
		c.PoemPractice = &fakePoemPractice{}
	}
	first := NewTurnManager(newFakeASR(), newFakeLLM(), newFakeTTS(2, 320), exec,
		ttConfigWith(nil, config), NewEventRecorder(&captureLogger{}, "first"), nil)
	firstSender := &captureSender{}
	first.HandleControl(firstSender, mustJSON(t, SessionInitMsg{
		Type: TypeSessionInit, Poem: 42,
		Audio: AudioSpec{SampleRate: 16000, Format: "s16le", Channels: 1},
	}))
	first.Close(firstSender)

	exec.mu.Lock()
	stage, unchecked := exec.lesson.Stage, exec.lesson.Unchecked
	exec.mu.Unlock()
	if stage != lesson.StageChecking || unchecked {
		t.Fatalf("disconnect changed poem check to stage=%q unchecked=%v", stage, unchecked)
	}

	second := NewTurnManager(newFakeASR(), newFakeLLM(), newFakeTTS(2, 320), exec,
		ttConfigWith(nil, config), NewEventRecorder(&captureLogger{}, "second"), nil)
	secondSender := &captureSender{}
	second.HandleControl(secondSender, mustJSON(t, SessionInitMsg{
		Type: TypeSessionInit, Poem: 42,
		Audio: AudioSpec{SampleRate: 16000, Format: "s16le", Channels: 1},
	}))
	started := ttCheckStartedWanted(t, secondSender)
	if started["reason"] != CheckStartedReasonResume {
		t.Fatalf("reconnected check reason = %v, want resume", started["reason"])
	}
	if exec.ensureCalls != 0 {
		t.Fatalf("ensure calls = %d, want the original attempt", exec.ensureCalls)
	}
}
