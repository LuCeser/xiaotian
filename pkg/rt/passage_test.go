// passage_test.go — the Issue #14 passage 背默 classroom tests: the range
// gate and masked session view, the task announcement (never a paragraph),
// the range's own ordered check rows (不混入未选择段落), the honest refusals
// and the no-leak paragraph hint, the range-scoped submit (one practice
// record keyed by content + range, kind=passage receipt, honest save
// failure) and the whole/paragraph identity split.
package rt

import (
	"context"
	"errors"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xiaotian/home-tutor-agent/pkg/database"
	"github.com/xiaotian/home-tutor-agent/pkg/lesson"
	"github.com/xiaotian/home-tutor-agent/pkg/passage"
)

// ttPassageParagraphs is the fixture work: 秋天的雨, five ordered paragraphs.
var ttPassageParagraphs = []string{
	"秋天的雨，是一把钥匙。",
	"秋天的雨，有一盒五彩缤纷的颜料。",
	"秋天的雨，藏着非常好闻的气味。",
	"秋天的雨，吹起了金色的小喇叭。",
	"秋天的雨，带给大地的是一曲丰收的歌。",
}

// fakePassageSource pins one passage behind PassageSource.
type fakePassageSource struct {
	err error
	p   passage.Passage
}

func (f *fakePassageSource) FetchPassage(_ context.Context, _ int64) (passage.Passage, error) {
	return f.p, f.err
}

// fakePassagePractice captures the passage practice writes.
type fakePassagePractice struct {
	err     error
	records []passage.PracticeRecord
	mu      sync.Mutex
}

func (f *fakePassagePractice) RecordPractice(_ context.Context, r passage.PracticeRecord) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.records = append(f.records, r)
	return nil
}

func (f *fakePassagePractice) recorded() []passage.PracticeRecord {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]passage.PracticeRecord(nil), f.records...)
}

// ttInScopePassage builds the fixture work as an in-scope supplementary
// passage carrying the two practice ranges the tests practice: paragraphs
// 2,3 (scope id 51) and whole (scope id 52).
func ttInScopePassage() passage.Passage {
	return passage.Passage{
		ID: 77, Title: "秋天的雨", Category: "chinese",
		Paragraphs: append([]string(nil), ttPassageParagraphs...),
		InScope:    true, ScopeReason: passage.ScopeSupplementary,
		Scopes: []passage.ScopeRow{
			{ID: 51, Paragraphs: []int{2, 3}},
			{ID: 52, Whole: true},
		},
	}
}

// ttPassageInit opens the passage classroom and settles the greeting.
func ttPassageInit(t *testing.T, tm *TurnManager, s *captureSender, passageID, scopeID int64, mode string) map[string]any {
	t.Helper()
	tm.HandleControl(s, mustJSON(t, SessionInitMsg{
		Type:         TypeSessionInit,
		Mode:         mode,
		Passage:      passageID,
		PassageScope: scopeID,
		Audio:        AudioSpec{SampleRate: 16000, Format: "s16le", Channels: 1},
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

// newPassageTestManager wires a passage-classroom manager: a fresh executor
// row, the fake passage source/practice store, and NO word source (the
// passage classroom must never touch it).
func newPassageTestManager(t *testing.T, asr *fakeASR, llm *fakeLLM, mutate func(*TurnManagerConfig)) (*TurnManager, *captureSender, *fakeExecutor, *fakePassagePractice) {
	t.Helper()
	exec := newFakeExecutorNoRow(ttLessonID)
	practice := &fakePassagePractice{}
	cfg := ttConfigWith(nil, func(c *TurnManagerConfig) {
		c.Passages = &fakePassageSource{p: ttInScopePassage()}
		c.PassagePractice = practice
		if mutate != nil {
			mutate(c)
		}
	})
	tm := NewTurnManager(asr, llm, newFakeTTS(2, 320), exec, cfg, NewEventRecorder(&captureLogger{}, "t"), nil)
	s := &captureSender{}
	return tm, s, exec, practice
}

// TestPassageSessionInitMaskedTask — the classroom opens on an IN-SCOPE
// range: session_ready carries the passage task view with the ORIGINAL
// paragraph numbering (never the text), the lesson view stays masked in
// EVERY mode (even test — 背默 must not hand out the original), and the
// greeting announces the TASK — the title and the range — without any
// paragraph.
func TestPassageSessionInitMaskedTask(t *testing.T) {
	for _, mode := range []string{ModeStudent, ModeTest} {
		tm, s, _, _ := newPassageTestManager(t, newFakeASR(), newFakeLLM(), nil)
		ready := ttPassageInit(t, tm, s, 77, 51, mode)

		view, ok := ready["passage"].(map[string]any)
		if !ok {
			t.Fatalf("mode %s: session_ready has no passage view: %v", mode, ready)
		}
		if view["title"] != "秋天的雨" || view["method"] != "背默" || view["scope_id"] != float64(51) {
			t.Fatalf("mode %s: passage view = %v", mode, view)
		}
		if whole, _ := view["whole"].(bool); whole {
			t.Fatalf("mode %s: paragraph scope reads whole", mode)
		}
		paragraphs, _ := view["paragraphs"].([]any)
		if len(paragraphs) != 2 || paragraphs[0] != float64(2) || paragraphs[1] != float64(3) {
			t.Fatalf("mode %s: view paragraphs = %v, want the original numbering [2 3]", mode, paragraphs)
		}

		lessonView, _ := ready["lesson"].(map[string]any)
		words, _ := lessonView["words"].([]any)
		if len(words) != 2 || words[0] != "" || words[1] != "" {
			t.Fatalf("mode %s: lesson words = %v, want masked paragraphs", mode, words)
		}

		spoken := allTeacherTexts(t, s)
		if !strings.Contains(spoken, "秋天的雨") || !strings.Contains(spoken, "第 2、3 段") {
			t.Fatalf("mode %s: the greeting never named the task + range: %q", mode, spoken)
		}
		for _, paragraph := range ttPassageParagraphs {
			if strings.Contains(spoken, paragraph) {
				t.Fatalf("mode %s: the greeting leaked a paragraph: %q", mode, paragraph)
			}
		}
	}
}

// TestPassageInitGates — the honest coded refusals: no source, an unknown
// passage, an out-of-scope passage, a scope row that is not this passage's,
// and a passage init without a scope id. Never a silent word lesson.
func TestPassageInitGates(t *testing.T) {
	refusals := []struct {
		mutate func(*TurnManagerConfig)
		pb     func(passage.Passage) passage.Passage
		name   string
		scope  int64
	}{
		{
			mutate: func(c *TurnManagerConfig) { c.Passages = nil },
			scope:  51,
			name:   "no source",
		},
		{
			// The fake returns the same row regardless of id — emulate the
			// miss through the source's error instead.
			mutate: func(c *TurnManagerConfig) {
				c.Passages = &fakePassageSource{err: passage.ErrPassageNotFound}
			},
			scope: 51,
			name:  "unknown passage",
		},
		{
			pb: func(p passage.Passage) passage.Passage {
				p.InScope, p.ScopeReason = false, passage.ScopeProgressPending
				return p
			},
			scope: 51,
			name:  "out of scope",
		},
		{
			pb: func(p passage.Passage) passage.Passage {
				p.Scopes = []passage.ScopeRow{{ID: 51, Paragraphs: []int{2, 3}}}
				return p
			},
			scope: 999,
			name:  "scope row not on this passage",
		},
		{
			pb: func(p passage.Passage) passage.Passage {
				p.Scopes = []passage.ScopeRow{{ID: 88, Paragraphs: []int{9}}}
				return p
			},
			scope: 88,
			name:  "corrupt range index",
		},
	}
	for _, refusal := range refusals {
		t.Run(refusal.name, func(t *testing.T) {
			p := ttInScopePassage()
			if refusal.pb != nil {
				p = refusal.pb(p)
			}
			final := p
			tm, s, _, _ := newPassageTestManager(t, newFakeASR(), newFakeLLM(), func(c *TurnManagerConfig) {
				if refusal.mutate != nil {
					refusal.mutate(c)
				} else {
					c.Passages = &fakePassageSource{p: final}
				}
			})
			tm.HandleControl(s, mustJSON(t, SessionInitMsg{
				Type: TypeSessionInit, Mode: ModeStudent,
				Passage: 77, PassageScope: refusal.scope,
				Audio: AudioSpec{SampleRate: 16000, Format: "s16le", Channels: 1},
			}))
			errs := s.controlsOf(t, TypeError)
			if len(errs) != 1 {
				t.Fatalf("error count = %d, want 1", len(errs))
			}
			if errs[0]["code"] != ErrCodeLessonUnavailable {
				t.Fatalf("error code = %v, want %s", errs[0]["code"], ErrCodeLessonUnavailable)
			}
			if n := s.countOf(TypeSessionReady); n != 0 {
				t.Fatalf("session_ready count = %d, want 0 (never a silent lesson swap)", n)
			}
		})
	}

	// A passage without a scope id is a client bug, refused honestly.
	tm, s, _, _ := newPassageTestManager(t, newFakeASR(), newFakeLLM(), nil)
	tm.HandleControl(s, mustJSON(t, SessionInitMsg{
		Type: TypeSessionInit, Mode: ModeStudent, Passage: 77,
		Audio: AudioSpec{SampleRate: 16000, Format: "s16le", Channels: 1},
	}))
	errs := s.controlsOf(t, TypeError)
	if len(errs) != 1 || errs[0]["code"] != ErrCodeLessonUnavailable {
		t.Fatalf("no-scope init errors = %v, want one lesson_unavailable", errs)
	}
}

// TestPassageCheckRowsOnlySelectedRange — 写完了 opens the check panel
// against the range's OWN ordered paragraphs only: paragraphs 2 and 3 in
// original order, never an unselected one, never a re-ordered set.
func TestPassageCheckRowsOnlySelectedRange(t *testing.T) {
	asr := newFakeASR(asrScript{text: "写完了"})
	llm := newFakeLLM(poemIntentScript(intentConfirm, "好了", 0))
	tm, s, _, _ := newPassageTestManager(t, asr, llm, nil)
	ttPassageInit(t, tm, s, 77, 51, ModeStudent)

	spokenBefore := s.countOf(TypeTeacherSentence)
	done := make(chan struct{})
	go func() {
		tm.processTurn(s, []byte("pcm"))
		close(done)
	}()
	started := ttCheckStartedWanted(t, s)
	if started["reason"] != CheckStartedReasonPassageDone {
		t.Fatalf("check_started.reason = %v, want %s", started["reason"], CheckStartedReasonPassageDone)
	}
	words, _ := started["words"].([]any)
	if len(words) != 2 || words[0] != ttPassageParagraphs[1] || words[1] != ttPassageParagraphs[2] {
		t.Fatalf("check rows = %v, want exactly paragraphs 2 and 3 in original order", words)
	}
	if !waitFor(t, 5*time.Second, func() bool { return s.countOf(TypeTeacherSentence) > spokenBefore }) {
		t.Fatal("the passage check bridge never spoke")
	}
	ttAckLast(t, tm, s, 0)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the confirm turn never settled")
	}
}

// TestPassageRepeatRefusedAndHintCueNoLeak — repeat is refused honestly
// (背默 ≠ 听写), and a hint names only the ORIGINAL paragraph number with
// its opening characters — never the paragraph itself.
func TestPassageRepeatRefusedAndHintCueNoLeak(t *testing.T) {
	asr := newFakeASR(
		asrScript{text: "想不起来第二段"},
		asrScript{text: "再念一遍"},
	)
	llm := newFakeLLM(
		poemIntentScript(intentHint, "老师提醒你", 2),
		poemIntentScript(intentRepeat, "再念一遍", 0),
	)
	tm, s, _, _ := newPassageTestManager(t, asr, llm, nil)
	ttPassageInit(t, tm, s, 77, 51, ModeStudent)

	ttPassageTurn(t, tm, s) // the hint turn settles
	if !strings.Contains(allTeacherTexts(t, s), "第 2 段的开头是「秋天」") {
		t.Fatalf("the hint never gave the opening cue: %q", allTeacherTexts(t, s))
	}
	if spoken := allTeacherTexts(t, s); strings.Contains(spoken, ttPassageParagraphs[1]) {
		t.Fatalf("the hint leaked the paragraph: %q", ttPassageParagraphs[1])
	}

	ttPassageTurn(t, tm, s) // the repeat turn settles
	if !strings.Contains(allTeacherTexts(t, s), "不能把课文念给你听") {
		t.Fatalf("the repeat never got the honest refusal: %q", allTeacherTexts(t, s))
	}
}

// ttPassageTurn runs one voice turn against the passage classroom and
// settles its announcement (the poem twin).
func ttPassageTurn(t *testing.T, tm *TurnManager, s *captureSender) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		tm.processTurn(s, []byte("pcm"))
		close(done)
	}()
	spokenBefore := s.countOf(TypeTeacherSentence)
	if !waitFor(t, 5*time.Second, func() bool { return s.countOf(TypeTeacherSentence) > spokenBefore }) {
		t.Fatal("the passage turn never spoke its announcement")
	}
	ttAckLast(t, tm, s, 0)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the passage turn never settled after its ack")
	}
}

// TestPassageConfirmSubmitRangeScope — the full paragraph-range submit: the
// manual gate, ONE practice record keyed by content + range (scope id rides
// along as traceability), ONE kind=passage receipt entry naming the range,
// and the honest review_result.
func TestPassageConfirmSubmitRangeScope(t *testing.T) {
	db, err := database.Open(filepath.Join(t.TempDir(), "passage-receipt.db"))
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
	tm, s, exec, practice := newPassageTestManager(t, asr, llm, func(c *TurnManagerConfig) {
		c.Receipts = receipts
	})
	ttPassageInit(t, tm, s, 77, 51, ModeStudent)

	spokenBefore := s.countOf(TypeTeacherSentence)
	done := make(chan struct{})
	go func() {
		tm.processTurn(s, []byte("pcm"))
		close(done)
	}()
	ttCheckStartedWanted(t, s)
	if !waitFor(t, 5*time.Second, func() bool { return s.countOf(TypeTeacherSentence) > spokenBefore }) {
		t.Fatal("the passage check bridge never spoke")
	}
	ttAckLast(t, tm, s, 0)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the confirm turn never settled")
	}

	// Manual verdicts: paragraph 2 对, paragraph 3 错 (the range judgment).
	for i := 0; i < 2; i++ {
		tm.HandleControl(s, mustJSON(t, CheckVerdictMsg{
			Type: TypeCheckVerdict, RequestID: "pv-" + strconv.Itoa(i), WordIndex: i, Correct: i == 0,
		}))
	}
	tm.HandleControl(s, mustJSON(t, CheckSubmitMsg{Type: TypeCheckSubmit, RequestID: "passage-submit"}))

	var result map[string]any
	if !waitFor(t, 5*time.Second, func() bool {
		got := s.controlsOf(t, TypeReviewResult)
		if len(got) == 0 {
			return false
		}
		result = got[0]
		return true
	}) {
		t.Fatal("review_result never arrived after the passage submit")
	}
	if int(result["requested"].(float64)) != 1 || int(result["updated"].(float64)) != 1 {
		t.Errorf("review_result = %v, want requested=1 updated=1 (one range save)", result)
	}
	if !waitFor(t, 5*time.Second, func() bool {
		return strings.Contains(allTeacherTexts(t, s), "对了 1 段")
	}) {
		t.Fatalf("the summary never spoke the 1/2 range result: %q", allTeacherTexts(t, s))
	}
	ttAckLast(t, tm, s, 0)
	if !waitFor(t, 5*time.Second, func() bool { return s.countOf(TypeSessionEnded) == 1 }) {
		t.Fatal("session_ended never arrived after the passage summary ack")
	}

	// ONE practice record, content + range identity.
	records := practice.recorded()
	if len(records) != 1 {
		t.Fatalf("practice records = %d, want exactly 1 (one row per range attempt)", len(records))
	}
	rec := records[0]
	if rec.PassageID != 77 || rec.ScopeID != 51 || rec.Whole {
		t.Fatalf("record identity = passage %d scope %d whole %v, want 77/51/false", rec.PassageID, rec.ScopeID, rec.Whole)
	}
	if len(rec.Paragraphs) != 2 || rec.Paragraphs[0] != 2 || rec.Paragraphs[1] != 3 {
		t.Fatalf("record paragraphs = %v, want [2 3]", rec.Paragraphs)
	}
	if rec.TotalParagraphs != 2 || rec.CorrectParagraphs != 1 || rec.Mastered {
		t.Fatalf("record verdict = %+v, want 1/2 not mastered", rec)
	}
	if rec.ReviewedDay == "" {
		t.Error("record reviewed_day empty, want the Beijing day")
	}

	// The receipt: kind passage, ONE entry naming the work + the range.
	got, err := receipts.ListByLesson(ctx, exec.lesson.LessonID)
	if err != nil {
		t.Fatalf("ListByLesson: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("receipt rows = %d, want 1", len(got))
	}
	rc := got[0]
	if rc.Kind != lesson.ReceiptKindPassage || rc.Status != lesson.ReceiptStatusSaved {
		t.Errorf("receipt = %s/%s, want passage/saved", rc.Kind, rc.Status)
	}
	if len(rc.Requested) != 1 || rc.Requested[0] != "秋天的雨（第 2、3 段）" {
		t.Errorf("receipt requested = %v, want the work + range entry", rc.Requested)
	}

	exec.mu.Lock()
	stage, passageID, whole := exec.lesson.Stage, exec.lesson.PassageID, exec.lesson.PassageWhole
	exec.mu.Unlock()
	if stage != lesson.StageSubmitted || passageID != 77 || whole {
		t.Errorf("row = stage %s passage %d whole %v, want submitted/77/false", stage, passageID, whole)
	}
}

// TestPassageWholeRangeSubmitIdentity — a whole-range submit stores the
// EMPTY paragraph set with whole=true: the range identity never borrows the
// display numbering, and the summary counts the WHOLE work.
func TestPassageWholeRangeSubmitIdentity(t *testing.T) {
	asr := newFakeASR(asrScript{text: "写完了"})
	llm := newFakeLLM(poemIntentScript(intentConfirm, "好了", 0))
	tm, s, _, practice := newPassageTestManager(t, asr, llm, nil)
	ready := ttPassageInit(t, tm, s, 77, 52, ModeStudent)

	view, _ := ready["passage"].(map[string]any)
	if whole, _ := view["whole"].(bool); !whole {
		t.Fatalf("whole scope view = %v", view)
	}
	paragraphs, _ := view["paragraphs"].([]any)
	if len(paragraphs) != 5 || paragraphs[0] != float64(1) || paragraphs[4] != float64(5) {
		t.Fatalf("whole view paragraphs = %v, want [1..5]", paragraphs)
	}

	spokenBefore := s.countOf(TypeTeacherSentence)
	done := make(chan struct{})
	go func() {
		tm.processTurn(s, []byte("pcm"))
		close(done)
	}()
	started := ttCheckStartedWanted(t, s)
	if words, _ := started["words"].([]any); len(words) != 5 {
		t.Fatalf("whole check rows = %d, want all 5 paragraphs", len(words))
	}
	if !waitFor(t, 5*time.Second, func() bool { return s.countOf(TypeTeacherSentence) > spokenBefore }) {
		t.Fatal("the whole-range check bridge never spoke")
	}
	ttAckLast(t, tm, s, 0)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the confirm turn never settled")
	}

	for i := 0; i < 5; i++ {
		tm.HandleControl(s, mustJSON(t, CheckVerdictMsg{
			Type: TypeCheckVerdict, RequestID: "wv-" + strconv.Itoa(i), WordIndex: i, Correct: true,
		}))
	}
	tm.HandleControl(s, mustJSON(t, CheckSubmitMsg{Type: TypeCheckSubmit, RequestID: "whole-submit"}))
	if !waitFor(t, 5*time.Second, func() bool {
		return strings.Contains(allTeacherTexts(t, s), "整篇一共 5 段全对")
	}) {
		t.Fatalf("the summary never spoke the whole mastery: %q", allTeacherTexts(t, s))
	}
	ttAckLast(t, tm, s, 0)

	records := practice.recorded()
	if len(records) != 1 {
		t.Fatalf("records = %d, want 1", len(records))
	}
	rec := records[0]
	if !rec.Whole || len(rec.Paragraphs) != 0 || !rec.Mastered || rec.TotalParagraphs != 5 {
		t.Fatalf("whole record = %+v, want whole/empty set/mastered/5", rec)
	}
}

// TestPassageSubmitSaveFailure — a failed practice-record write is reported
// honestly: review_result updated=0, the receipt flips failed, the judgment
// (stage=submitted) survives, and the session still ends normally (部分写
// 入失败的诚实反馈).
func TestPassageSubmitSaveFailure(t *testing.T) {
	db, err := database.Open(filepath.Join(t.TempDir(), "passage-receipt-fail.db"))
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
	tm, s, exec, _ := newPassageTestManager(t, asr, llm, func(c *TurnManagerConfig) {
		c.Receipts = receipts
		c.PassagePractice = &fakePassagePractice{err: errors.New("passage db down")}
	})
	ttPassageInit(t, tm, s, 77, 51, ModeStudent)

	spokenBefore := s.countOf(TypeTeacherSentence)
	done := make(chan struct{})
	go func() {
		tm.processTurn(s, []byte("pcm"))
		close(done)
	}()
	ttCheckStartedWanted(t, s)
	if !waitFor(t, 5*time.Second, func() bool { return s.countOf(TypeTeacherSentence) > spokenBefore }) {
		t.Fatal("the passage check bridge never spoke")
	}
	ttAckLast(t, tm, s, 0)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the confirm turn never settled")
	}

	for i := 0; i < 2; i++ {
		tm.HandleControl(s, mustJSON(t, CheckVerdictMsg{
			Type: TypeCheckVerdict, RequestID: "fv-" + strconv.Itoa(i), WordIndex: i, Correct: true,
		}))
	}
	tm.HandleControl(s, mustJSON(t, CheckSubmitMsg{Type: TypeCheckSubmit, RequestID: "passage-submit-fail"}))

	var result map[string]any
	if !waitFor(t, 5*time.Second, func() bool {
		got := s.controlsOf(t, TypeReviewResult)
		if len(got) == 0 {
			return false
		}
		result = got[0]
		return true
	}) {
		t.Fatal("review_result never arrived after the failed save")
	}
	if int(result["updated"].(float64)) != 0 || int(result["missing"].(float64)) != 1 {
		t.Fatalf("review_result = %v, want updated=0 missing=1", result)
	}
	if !waitFor(t, 5*time.Second, func() bool {
		return strings.Contains(allTeacherTexts(t, s), "没有保存成功")
	}) {
		t.Fatalf("the summary never spoke the honest failure: %q", allTeacherTexts(t, s))
	}
	ttAckLast(t, tm, s, 0)

	got, err := receipts.ListByLesson(ctx, exec.lesson.LessonID)
	if err != nil {
		t.Fatalf("ListByLesson: %v", err)
	}
	if len(got) != 1 || got[0].Status != lesson.ReceiptStatusFailed || got[0].Kind != lesson.ReceiptKindPassage {
		t.Fatalf("receipt = %+v, want passage/failed", got)
	}
	exec.mu.Lock()
	stage := exec.lesson.Stage
	exec.mu.Unlock()
	if stage != lesson.StageSubmitted {
		t.Fatalf("row stage = %s, want submitted (the judgment survived the failed save)", stage)
	}
}

// TestPassageResumeInFlightCheck — a reconnect while the check panel is open
// reopens the SAME attempt with its range identity intact: session_ready
// carries the passage view re-read from the ROW, and the submit saves under
// the row's range.
func TestPassageResumeInFlightCheck(t *testing.T) {
	asr := newFakeASR(asrScript{text: "写完了"})
	llm := newFakeLLM(poemIntentScript(intentConfirm, "好了", 0))
	tm, s, exec, practice := newPassageTestManager(t, asr, llm, nil)
	ttPassageInit(t, tm, s, 77, 51, ModeStudent)

	spokenBefore := s.countOf(TypeTeacherSentence)
	done := make(chan struct{})
	go func() {
		tm.processTurn(s, []byte("pcm"))
		close(done)
	}()
	ttCheckStartedWanted(t, s)
	if !waitFor(t, 5*time.Second, func() bool { return s.countOf(TypeTeacherSentence) > spokenBefore }) {
		t.Fatal("the passage check bridge never spoke")
	}
	ttAckLast(t, tm, s, 0)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the confirm turn never settled")
	}

	// A second connection resumes the in-flight check panel.
	s2 := &captureSender{}
	tm.HandleControl(s2, mustJSON(t, SessionInitMsg{
		Type: TypeSessionInit, Mode: ModeStudent, Passage: 77, PassageScope: 51,
		Audio: AudioSpec{SampleRate: 16000, Format: "s16le", Channels: 1},
	}))
	ready := s2.controlsOf(t, TypeSessionReady)
	if len(ready) != 1 {
		t.Fatalf("resume session_ready count = %d, want 1", len(ready))
	}
	view, ok := ready[0]["passage"].(map[string]any)
	if !ok || view["title"] != "秋天的雨" || view["scope_id"] != float64(51) {
		t.Fatalf("resume passage view = %v", ready[0]["passage"])
	}
	paragraphs, _ := view["paragraphs"].([]any)
	if len(paragraphs) != 2 || paragraphs[0] != float64(2) {
		t.Fatalf("resume view paragraphs = %v, want [2 3]", paragraphs)
	}
	checkStarted := s2.controlsOf(t, TypeCheckStarted)
	if len(checkStarted) != 1 {
		t.Fatalf("resume check_started count = %d, want 1 (the unfinished check reopened)", len(checkStarted))
	}

	// The resumed submit saves under the ROW's range identity.
	for i := 0; i < 2; i++ {
		tm.HandleControl(s2, mustJSON(t, CheckVerdictMsg{
			Type: TypeCheckVerdict, RequestID: "rv-" + strconv.Itoa(i), WordIndex: i, Correct: true,
		}))
	}
	tm.HandleControl(s2, mustJSON(t, CheckSubmitMsg{Type: TypeCheckSubmit, RequestID: "resume-submit"}))
	if !waitFor(t, 5*time.Second, func() bool { return len(practice.recorded()) == 1 }) {
		t.Fatal("the resumed submit never saved its practice record")
	}
	rec := practice.recorded()[0]
	if rec.PassageID != 77 || rec.Whole || len(rec.Paragraphs) != 2 || !rec.Mastered {
		t.Fatalf("resumed record = %+v, want paragraph range 2,3 mastered", rec)
	}
	_ = exec
}
