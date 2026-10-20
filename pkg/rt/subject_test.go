// subject_test.go — Issue #55 subject-flow regressions (reworked): the
// unspecified-subject ("auto") classroom is settled by the CHILD, never a
// model — the one subject holding due words opens directly, both subjects
// holding due words asks the child (subject_ask question, answered by
// voice or the screen buttons), and the answered subject then binds the
// round through every #10 seam (scoped fetch, per-subject row,
// session_ready echo). AC2: an explicit declaration never reaches the
// clarify — the user's word outranks any ask, with zero asks and zero
// content peeks.
package rt

import (
	"strings"
	"testing"
	"time"

	"github.com/xiaotian/home-tutor-agent/pkg/lesson"
)

// TestSessionInitExplicitCategorySkipsClarify — the AC2 spine: a declared
// english/chinese NEVER reaches the clarify — zero asks, zero peeks — and
// the declared subject binds the round even when the due content would
// point the other way (no ask ever gets the chance to disagree with the
// user, so the user's word can never be silently overruled).
func TestSessionInitExplicitCategorySkipsClarify(t *testing.T) {
	for _, tc := range []struct {
		name      string
		declared  string
		peekCat   string // the subject whose content WOULD point the other way
		wantRowID string
	}{
		{"declared english over chinese-only content", lesson.CategoryEnglish, lesson.CategoryChinese, ttLessonID},
		{"declared chinese over english-only content", lesson.CategoryChinese, lesson.CategoryEnglish, ttLessonID + "-chinese"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			words := newFakeWords(ttCards("美丽", "世界"))
			words.setPeek(tc.peekCat, ttCards("美丽", "世界"))
			exec := newFakeExecutorNoRow(ttLessonID)
			// No scripted replies: ANY Chat call errors ("script exhausted"),
			// so a spurious model call would fail loudly — the zero-call
			// assertion below is the precise pin.
			llm := newFakeLLM()
			tm := NewTurnManager(newFakeASR(), llm, newFakeTTS(1, 320), exec, ttConfig(words), nil, nil)
			s := &captureSender{}
			tm.HandleControl(s, mustJSON(t, SessionInitMsg{
				Type:     TypeSessionInit,
				Audio:    AudioSpec{SampleRate: 16000, Format: "s16le", Channels: 1},
				Category: tc.declared,
			}))

			ready := s.controlsOf(t, TypeSessionReady)
			if len(ready) != 1 {
				t.Fatalf("session_ready count = %d, want 1", len(ready))
			}
			if got := ready[0]["category"]; got != tc.declared {
				t.Errorf("session_ready.category = %v, want the declared %q (never re-asked)", got, tc.declared)
			}
			if asks := s.controlsOf(t, TypeSubjectAsk); len(asks) != 0 {
				t.Errorf("subject_ask count = %d, want 0 (a declaration is final)", len(asks))
			}
			if llm.callCount() != 0 {
				t.Errorf("model Chat calls = %d, want 0 (the subject flow never asks a model)", llm.callCount())
			}
			if peeks := words.peekedCategories(); len(peeks) != 0 {
				t.Errorf("PeekDueWords categories = %v, want none (no clarify, no content read)", peeks)
			}
			if cats := words.fetchedCategories(); len(cats) != 1 || cats[0] != tc.declared {
				t.Errorf("FetchDueWords categories = %v, want [%s]", cats, tc.declared)
			}
			if ids := exec.ensureIDCalls(); len(ids) != 1 || ids[0] != tc.wantRowID {
				t.Errorf("EnsureLessonCards ids = %v, want [%s]", ids, tc.wantRowID)
			}
		})
	}
}

// ttWaitClarifyOpen waits until the FSM is GENUINELY back to an open
// listening state (state=listening, playback gate down). ttWaitListening
// matches historical state frames, and the clarify flow's ask→answer→re-ask
// sequence already has a listening frame in the log from the previous ack —
// only the live gate state tells the two apart.
func ttWaitClarifyOpen(t *testing.T, tm *TurnManager) {
	t.Helper()
	if !waitFor(t, 5*time.Second, func() bool {
		tm.mu.Lock()
		open := tm.state == StateListening && !tm.playbackPending
		tm.mu.Unlock()
		return open
	}) {
		t.Fatal("the clarify never returned to an open listening state")
	}
}

// ttSubjectInit sends one auto (unspecified-subject) session_init.
func ttSubjectInit(t *testing.T, tm *TurnManager, s *captureSender) {
	t.Helper()
	tm.HandleControl(s, mustJSON(t, SessionInitMsg{
		Type:     TypeSessionInit,
		Audio:    AudioSpec{SampleRate: 16000, Format: "s16le", Channels: 1},
		Category: CategoryAuto,
	}))
}

// ttAwaitAskSent waits until the NEXT ask/retry announcement has fully gone
// out: at least one NEW sentence beyond prevSentences, and every scheduled
// sentence's audio_end seen. The ask wordings split into several sentences,
// and only after the LAST one is out does ttAckLast ack the final sentence
// — acking mid-announcement leaves the playback gate up and the child's
// answer would be dropped.
func ttAwaitAskSent(t *testing.T, s *captureSender, prevSentences int) {
	t.Helper()
	if !waitFor(t, 5*time.Second, func() bool {
		sentences := s.countOf(TypeTeacherSentence)
		return sentences > prevSentences && s.countOf(TypeAudioEnd) >= sentences
	}) {
		t.Fatalf("ask announcement incomplete after %d sentences: sentences=%d audio_ends=%d",
			prevSentences, s.countOf(TypeTeacherSentence), s.countOf(TypeAudioEnd))
	}
}

// TestSessionInitAutoSingleSubjectStartsThatSubject — case 1: with only ONE
// subject holding due words (MORE than the fetch cap on purpose — presence,
// not count, is what the resolution reads), the auto classroom opens under
// that subject directly: no ask, no model call, and the round scopes to that
// subject alone (per-subject row, scoped fetch).
func TestSessionInitAutoSingleSubjectStartsThatSubject(t *testing.T) {
	// 12 due words exceeds dueWordsLimit (10): the cap must not change the
	// one-subject outcome either way.
	many := func(prefix string, n int) []lesson.Word {
		words := make([]lesson.Word, 0, n)
		for i := 0; i < n; i++ {
			words = append(words, lesson.Word{Word: prefix + string(rune('a'+i))})
		}
		return words
	}
	for _, tc := range []struct {
		name      string
		only      string // the ONE subject holding due words
		wantRowID string
	}{
		{"english-only due opens english", lesson.CategoryEnglish, ttLessonID},
		{"chinese-only due opens chinese", lesson.CategoryChinese, ttLessonID + "-chinese"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			words := newFakeWords()
			words.setPeek(tc.only, many("w", 12))
			words.setDue(tc.only, many("w", 12))
			exec := newFakeExecutorNoRow(ttLessonID)
			llm := newFakeLLM()
			tm := NewTurnManager(newFakeASR(), llm, newFakeTTS(1, 320), exec, ttConfig(words), nil, nil)
			s := &captureSender{}
			ttSubjectInit(t, tm, s)

			ready := s.controlsOf(t, TypeSessionReady)
			if len(ready) != 1 {
				t.Fatalf("session_ready count = %d, want 1 (the classroom opens directly)", len(ready))
			}
			if got := ready[0]["category"]; got != tc.only {
				t.Errorf("session_ready.category = %v, want the due-holding %q", got, tc.only)
			}
			if asks := s.controlsOf(t, TypeSubjectAsk); len(asks) != 0 {
				t.Errorf("subject_ask count = %d, want 0 (one subject due — nothing to ask)", len(asks))
			}
			if llm.callCount() != 0 {
				t.Errorf("model Chat calls = %d, want 0 (no model ever judges the subject)", llm.callCount())
			}
			if peeks := words.peekedCategories(); len(peeks) != 2 {
				t.Errorf("PeekDueWords categories = %v, want both subjects probed", peeks)
			}
			if cats := words.fetchedCategories(); len(cats) != 1 || cats[0] != tc.only {
				t.Errorf("FetchDueWords categories = %v, want exactly [%s]", cats, tc.only)
			}
			if ids := exec.ensureIDCalls(); len(ids) != 1 || ids[0] != tc.wantRowID {
				t.Errorf("EnsureLessonCards ids = %v, want [%s]", ids, tc.wantRowID)
			}
		})
	}
}

// TestSessionInitAutoBothSubjectsAskThenPickBinds — case 2: with BOTH
// subjects holding due words (each beyond the fetch cap), the classroom
// does NOT start and the page gets the subject_ask question; the button
// answer (subject_pick) binds the round, which then runs under the picked
// subject ONLY — per-subject row, scoped fetch, the other subject's words
// never join. A duplicate pick after the binding is a no-op.
func TestSessionInitAutoBothSubjectsAskThenPickBinds(t *testing.T) {
	enWords := make([]lesson.Word, 0, 12) // 12 > dueWordsLimit(10)
	for i := 0; i < 12; i++ {
		enWords = append(enWords, lesson.Word{Word: "en" + string(rune('a'+i))})
	}
	zhWords := make([]lesson.Word, 0, 30) // 30 > dueWordsLimit(10)
	for i := 0; i < 30; i++ {
		zhWords = append(zhWords, lesson.Word{Word: "词" + string(rune('甲'+i%20)) + string(rune('0'+i%10))})
	}
	words := newFakeWords()
	words.setPeek(lesson.CategoryEnglish, enWords)
	words.setPeek(lesson.CategoryChinese, zhWords)
	words.setDue(lesson.CategoryChinese, zhWords)
	exec := newFakeExecutorNoRow(ttLessonID)
	llm := newFakeLLM()
	tm := NewTurnManager(newFakeASR(), llm, newFakeTTS(1, 320), exec, ttConfig(words), nil, nil)
	s := &captureSender{}
	ttSubjectInit(t, tm, s)

	asks := s.controlsOf(t, TypeSubjectAsk)
	if len(asks) != 1 {
		t.Fatalf("subject_ask count = %d, want 1 (both subjects hold due words)", len(asks))
	}
	if q, _ := asks[0]["question"].(string); q != subjectAskText {
		t.Errorf("subject_ask.question = %q, want the spoken question text", q)
	}
	if ready := s.controlsOf(t, TypeSessionReady); len(ready) != 0 {
		t.Fatalf("session_ready count = %d, want 0 before the child answers", len(ready))
	}
	if llm.callCount() != 0 {
		t.Errorf("model Chat calls = %d, want 0 (the ask is the child's, never a model's)", llm.callCount())
	}
	if ids := exec.ensureIDCalls(); len(ids) != 0 {
		t.Errorf("EnsureLessonCards ids = %v, want none (no lesson row before the answer)", ids)
	}

	tm.HandleControl(s, mustJSON(t, SubjectPickMsg{Type: TypeSubjectPick, Category: lesson.CategoryChinese}))

	ready := s.controlsOf(t, TypeSessionReady)
	if len(ready) != 1 {
		t.Fatalf("session_ready count = %d, want 1 after the pick", len(ready))
	}
	if got := ready[0]["category"]; got != lesson.CategoryChinese {
		t.Errorf("session_ready.category = %v, want the picked chinese", got)
	}
	if ids := exec.ensureIDCalls(); len(ids) != 1 || ids[0] != ttLessonID+"-chinese" {
		t.Errorf("EnsureLessonCards ids = %v, want [%s-chinese]", ids, ttLessonID)
	}
	if cats := words.fetchedCategories(); len(cats) != 1 || cats[0] != lesson.CategoryChinese {
		t.Errorf("FetchDueWords categories = %v, want exactly [chinese]", cats)
	}
	exec.mu.Lock()
	round := cardWords(exec.lesson.Cards)
	exec.mu.Unlock()
	if len(round) != dueWordsLimit {
		t.Fatalf("round words = %d, want the %d-word cap", len(round), dueWordsLimit)
	}
	for _, w := range round {
		if !strings.HasPrefix(w, "词") {
			t.Errorf("round word %q is not a chinese word — the OTHER subject joined the round", w)
		}
	}
	if exec.lesson.Category != lesson.CategoryChinese {
		t.Errorf("lesson row category = %q, want chinese", exec.lesson.Category)
	}

	// A duplicate pick finds nothing parked and must not re-open anything.
	tm.HandleControl(s, mustJSON(t, SubjectPickMsg{Type: TypeSubjectPick, Category: lesson.CategoryEnglish}))
	if again := s.controlsOf(t, TypeSessionReady); len(again) != 1 {
		t.Errorf("session_ready count after a duplicate pick = %d, want still 1 (exactly-once binding)", len(again))
	}
}

// TestSessionInitAutoBothSubjectsVoiceAnswerBinds — case 2's voice channel:
// the child's SPOKEN answer binds the subject with ZERO model calls. An
// answer naming neither subject (or both) is re-asked with the short retry
// wording and the clarify holds; the next clean answer opens the classroom
// under that subject only.
func TestSessionInitAutoBothSubjectsVoiceAnswerBinds(t *testing.T) {
	enWords := make([]lesson.Word, 0, 11) // 11 > dueWordsLimit(10)
	for i := 0; i < 11; i++ {
		enWords = append(enWords, lesson.Word{Word: "en" + string(rune('a'+i))})
	}
	zhWords := make([]lesson.Word, 0, 25) // 25 > dueWordsLimit(10)
	for i := 0; i < 25; i++ {
		zhWords = append(zhWords, lesson.Word{Word: "字" + string(rune('甲'+i%20)) + string(rune('0'+i%10))})
	}
	words := newFakeWords()
	words.setPeek(lesson.CategoryEnglish, enWords)
	words.setPeek(lesson.CategoryChinese, zhWords)
	words.setDue(lesson.CategoryChinese, zhWords)
	exec := newFakeExecutorNoRow(ttLessonID)
	llm := newFakeLLM()
	asr := newFakeASR(
		asrScript{text: "今天天气不错"},  // names neither subject → re-ask
		asrScript{text: "我想先默写语文"}, // clean answer → binds chinese
	)
	tm := NewTurnManager(asr, llm, newFakeTTS(1, 320), exec,
		ttConfigWith(words, func(c *TurnManagerConfig) {
			c.VADStartGraceMs = 1 // no session_ready yet: the grace never applies
			c.VADEchoTailMs = 1   // the acked ask opens uplink immediately
		}), nil, nil)
	s := &captureSender{}
	ttSubjectInit(t, tm, s)

	// The ask question splits into several sentences (。？！): wait until
	// every scheduled sentence's audio went out, so the ack lands on the
	// FINAL sentence and uplink opens.
	ttAwaitAskSent(t, s, 0)
	ttAckLast(t, tm, s, 0)
	ttWaitClarifyOpen(t, tm)
	tm.mu.Lock()
	tm.echoTailUntil = time.Now().Add(-time.Millisecond)
	tm.mu.Unlock()
	asked := s.countOf(TypeTeacherSentence)

	// The unusable answer: re-asked, classroom still parked.
	tm.IngestAudio(s, ttUtterance())
	ttAwaitAskSent(t, s, asked) // the retry wording
	if ready := s.controlsOf(t, TypeSessionReady); len(ready) != 0 {
		t.Fatalf("session_ready after an unusable answer = %d, want 0 (clarify holds)", len(ready))
	}
	ttAckLast(t, tm, s, 0)
	ttWaitClarifyOpen(t, tm)
	// The 1ms echo tail may still be live right after the ack (the same
	// faked-expiry the VAD gating tests use) — lap it before speaking again.
	tm.mu.Lock()
	tm.echoTailUntil = time.Now().Add(-time.Millisecond)
	tm.mu.Unlock()

	// The clean spoken answer binds and the classroom opens chinese-only.
	tm.IngestAudio(s, ttUtterance())
	if !waitFor(t, 5*time.Second, func() bool { return len(s.controlsOf(t, TypeSessionReady)) == 1 }) {
		t.Fatal("session_ready never arrived after the spoken answer")
	}
	ready := s.controlsOf(t, TypeSessionReady)
	if got := ready[0]["category"]; got != lesson.CategoryChinese {
		t.Errorf("session_ready.category = %v, want the spoken chinese", got)
	}
	// The answer shows verbatim (no masking set exists yet).
	transcripts := s.controlsOf(t, TypeTranscript)
	found := false
	for _, tr := range transcripts {
		if txt, _ := tr["text"].(string); strings.Contains(txt, "我想先默写语文") {
			found = true
		}
	}
	if !found {
		t.Errorf("transcripts = %v, want the spoken answer shown verbatim", transcripts)
	}
	if llm.callCount() != 0 {
		t.Errorf("model Chat calls = %d, want 0 (keyword binding, never an LLM)", llm.callCount())
	}
	if ids := exec.ensureIDCalls(); len(ids) != 1 || ids[0] != ttLessonID+"-chinese" {
		t.Errorf("EnsureLessonCards ids = %v, want [%s-chinese]", ids, ttLessonID)
	}
	if cats := words.fetchedCategories(); len(cats) != 1 || cats[0] != lesson.CategoryChinese {
		t.Errorf("FetchDueWords categories = %v, want exactly [chinese]", cats)
	}
}

// TestSessionInitAutoNoDueKeepsEnglishNoDuePath — case 3: with NEITHER
// subject holding due words, the auto classroom lands on english and ends
// through the ordinary no-due path (the same honest ending an english
// bookmark gets over an empty vocabulary).
func TestSessionInitAutoNoDueKeepsEnglishNoDuePath(t *testing.T) {
	words := newFakeWords() // no peek snapshots, no fetch batches anywhere
	exec := newFakeExecutorNoRow(ttLessonID)
	llm := newFakeLLM()
	tm := NewTurnManager(newFakeASR(), llm, newFakeTTS(1, 320), exec, ttConfig(words), nil, nil)
	s := &captureSender{}
	ttSubjectInit(t, tm, s)

	ready := s.controlsOf(t, TypeSessionReady)
	if len(ready) != 1 {
		t.Fatalf("session_ready count = %d, want 1 (the no-due classroom still answers ready)", len(ready))
	}
	if got := ready[0]["category"]; got != lesson.CategoryEnglish {
		t.Errorf("session_ready.category = %v, want the english no-due landing", got)
	}
	if asks := s.controlsOf(t, TypeSubjectAsk); len(asks) != 0 {
		t.Errorf("subject_ask count = %d, want 0 (nothing due — nothing to ask)", len(asks))
	}
	if llm.callCount() != 0 {
		t.Errorf("model Chat calls = %d, want 0", llm.callCount())
	}
	// The spoken no-due explanation plays, and its ack ends the session with
	// the ordinary no-due reason.
	ttAwaitSpoken(t, s, 1)
	ttAckLast(t, tm, s, 0)
	if !waitFor(t, 5*time.Second, func() bool { return len(s.controlsOf(t, TypeSessionEnded)) == 1 }) {
		t.Fatal("session_ended never arrived after the no-due explanation")
	}
	if got := s.controlsOf(t, TypeSessionEnded)[0]["reason"]; got != ReasonNoDueWords {
		t.Errorf("session_ended.reason = %v, want %q", got, ReasonNoDueWords)
	}
}

// TestSubjectFromAnswer — the spoken-answer keyword table: each subject's
// names bind that subject; naming both, or neither, is NOT an answer.
func TestSubjectFromAnswer(t *testing.T) {
	for _, tc := range []struct {
		text   string
		want   string
		wantOK bool
	}{
		{"语文", lesson.CategoryChinese, true},
		{"我想先默写语文", lesson.CategoryChinese, true},
		{"写中文的吧", lesson.CategoryChinese, true},
		{"默汉字", lesson.CategoryChinese, true},
		{"英语", lesson.CategoryEnglish, true},
		{"先写英文", lesson.CategoryEnglish, true},
		{"语文和英语都写", "", false},
		{"随便", "", false},
		{"", "", false},
	} {
		got, ok := subjectFromAnswer(tc.text)
		if ok != tc.wantOK || got != tc.want {
			t.Errorf("subjectFromAnswer(%q) = (%q,%v), want (%q,%v)", tc.text, got, ok, tc.want, tc.wantOK)
		}
	}
}

// TestSubjectVoiceAnswerStaleAfterInterruptDoesNotBind — the P2 pin: a
// spoken answer recognized under an epoch the child has since INTERRUPTED
// must never bind the subject. The delayed ASR reply ("英语") lands after
// the interrupt bumped the epoch; the stale turn may not consume the parked
// clarify (no classroom, no english binding). The clarify itself survives
// the interrupt — the child says 语文 on the fresh epoch and THAT answer
// opens the classroom.
func TestSubjectVoiceAnswerStaleAfterInterruptDoesNotBind(t *testing.T) {
	en := make([]lesson.Word, 0, 11)
	for i := 0; i < 11; i++ {
		en = append(en, lesson.Word{Word: "en" + string(rune('a'+i))})
	}
	zh := make([]lesson.Word, 0, 25)
	for i := 0; i < 25; i++ {
		zh = append(zh, lesson.Word{Word: "字" + string(rune('甲'+i%20)) + string(rune('0'+i%10))})
	}
	words := newFakeWords()
	words.setPeek(lesson.CategoryEnglish, en)
	words.setPeek(lesson.CategoryChinese, zh)
	words.setDue(lesson.CategoryChinese, zh)
	exec := newFakeExecutorNoRow(ttLessonID)
	asr := newFakeASR(
		asrScript{text: "英语", delay: 400 * time.Millisecond}, // lands AFTER the interrupt
		asrScript{text: "我想默写语文"},                            // the fresh-epoch retry
	)
	tm := NewTurnManager(asr, newFakeLLM(), newFakeTTS(1, 320), exec,
		ttConfigWith(words, func(c *TurnManagerConfig) {
			c.VADStartGraceMs = 1
			c.VADEchoTailMs = 1
		}), nil, nil)
	s := &captureSender{}
	ttSubjectInit(t, tm, s)
	ttAwaitAskSent(t, s, 0)
	ttAckLast(t, tm, s, 0)
	ttWaitClarifyOpen(t, tm)
	tm.mu.Lock()
	tm.echoTailUntil = time.Now().Add(-time.Millisecond)
	tm.mu.Unlock()

	// The child starts speaking ("英语" is in the provider pipeline), then
	// interrupts: wait until the turn is mid-ASR (processing), bump the
	// epoch through the real interrupt path, then let the stale reply land.
	tm.IngestAudio(s, ttUtterance())
	if !waitFor(t, 5*time.Second, func() bool {
		tm.mu.Lock()
		processing := tm.state == StateProcessing
		tm.mu.Unlock()
		return processing
	}) {
		t.Fatal("the answer turn never reached processing")
	}
	tm.HandleControl(s, mustJSON(t, InterruptMsg{
		Type: TypeInterrupt, LastSentenceID: "", Epoch: 1,
	}))
	// Let the stale "英语" reply arrive and settle before asserting.
	time.Sleep(700 * time.Millisecond)

	if ready := s.controlsOf(t, TypeSessionReady); len(ready) != 0 {
		t.Fatalf("session_ready after a stale answer = %d, want 0 (an interrupted turn never starts a classroom)", len(ready))
	}
	tm.mu.Lock()
	parked := tm.pendingAsk != nil
	closed := tm.closed
	tm.mu.Unlock()
	if !parked {
		t.Fatal("the stale answer consumed the parked clarify")
	}
	if closed {
		t.Fatal("the stale answer closed the session")
	}
	if ids := exec.ensureIDCalls(); len(ids) != 0 {
		t.Errorf("EnsureLessonCards ids = %v, want none (nothing bound)", ids)
	}

	// The clarify survives: on the fresh epoch the child's 语文 binds and
	// the classroom opens chinese-only.
	ttWaitClarifyOpen(t, tm)
	tm.mu.Lock()
	tm.echoTailUntil = time.Now().Add(-time.Millisecond)
	tm.mu.Unlock()
	tm.IngestAudio(s, ttUtterance())
	if !waitFor(t, 5*time.Second, func() bool { return len(s.controlsOf(t, TypeSessionReady)) == 1 }) {
		t.Fatal("session_ready never arrived after the fresh-epoch answer")
	}
	if got := s.controlsOf(t, TypeSessionReady)[0]["category"]; got != lesson.CategoryChinese {
		t.Errorf("session_ready.category = %v, want the freshly answered chinese", got)
	}
	if ids := exec.ensureIDCalls(); len(ids) != 1 || ids[0] != ttLessonID+"-chinese" {
		t.Errorf("EnsureLessonCards ids = %v, want [%s-chinese]", ids, ttLessonID)
	}
}
