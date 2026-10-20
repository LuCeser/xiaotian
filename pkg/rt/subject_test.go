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
	"context"
	"encoding/json"
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

// TestSubjectPickDuringAskStopsAskPlayback — the P1 pin: tapping a pick
// button while the ask question is STILL playing must fully terminate the
// ask's announcement before the picked classroom starts. Concretely: the
// epoch bumps (the ask's in-flight audio and waits all go stale), the ask's
// playback wait is GONE (an ack for the ask's final sentence can no longer
// settle-listening over the greeting's uplink gate, and the ask's ack
// timeout can no longer orderly-close the fresh classroom), and the
// greeting then plays and acks normally.
func TestSubjectPickDuringAskStopsAskPlayback(t *testing.T) {
	en := make([]lesson.Word, 0, 12)
	for i := 0; i < 12; i++ {
		en = append(en, lesson.Word{Word: "en" + string(rune('a'+i))})
	}
	zh := make([]lesson.Word, 0, 30)
	for i := 0; i < 30; i++ {
		zh = append(zh, lesson.Word{Word: "词" + string(rune('甲'+i%20)) + string(rune('0'+i%10))})
	}
	words := newFakeWords()
	words.setPeek(lesson.CategoryEnglish, en)
	words.setPeek(lesson.CategoryChinese, zh)
	words.setDue(lesson.CategoryChinese, zh)
	exec := newFakeExecutorNoRow(ttLessonID)
	tm := NewTurnManager(newFakeASR(), newFakeLLM(), newFakeTTS(1, 320), exec, ttConfig(words), nil, nil)
	s := &captureSender{}
	ttSubjectInit(t, tm, s)

	// The ask is mid-announcement: every sentence's audio went out, the ack
	// has NOT landed — the ask goroutine is parked in its playback wait.
	ttAwaitAskSent(t, s, 0)
	askFinal, _ := s.controlsOf(t, TypeTeacherSentence)[s.countOf(TypeTeacherSentence)-1]["sentence_id"].(string)
	askSentences := s.countOf(TypeTeacherSentence)
	if askFinal == "" {
		t.Fatal("no ask sentence to probe with")
	}

	// The child taps 默写语文 while the question is still out.
	tm.HandleControl(s, mustJSON(t, SubjectPickMsg{Type: TypeSubjectPick, Category: lesson.CategoryChinese}))
	if !waitFor(t, 5*time.Second, func() bool { return len(s.controlsOf(t, TypeSessionReady)) == 1 }) {
		t.Fatal("session_ready never arrived after the pick")
	}

	// The epoch bumped and the ask's playback wait is gone: a stale ack for
	// the ask's final sentence must be REJECTED (unknown_or_stale), leaving
	// the greeting's own uplink gate untouched.
	tm.mu.Lock()
	epoch := tm.epoch
	tm.mu.Unlock()
	if epoch == 0 {
		t.Error("epoch never bumped — the ask announcement was not terminated")
	}
	tm.HandleControl(s, mustJSON(t, SentenceAckMsg{Type: TypeSentenceAck, SentenceID: askFinal, Epoch: 0}))
	time.Sleep(150 * time.Millisecond)

	// The greeting is out now; its gate must still hold (only ITS final ack
	// may settle listening).
	ttAwaitAskSent(t, s, askSentences)
	tm.mu.Lock()
	gate := tm.playbackPending
	_, askWaitAlive := tm.pendingPlayback[pendingKey(0, askFinal)]
	closed := tm.closed
	tm.mu.Unlock()
	if askWaitAlive {
		t.Error("the ask's playback wait survived the pick — its ack/timeout still races the new classroom")
	}
	if !gate {
		t.Error("the stale ask ack lifted the greeting's uplink gate")
	}
	if closed {
		t.Error("the pick closed the session")
	}

	// The fresh classroom is healthy: the greeting's own final ack settles
	// listening normally.
	ttAckLast(t, tm, s, epoch)
	ttWaitClarifyOpen(t, tm)
	if ended := s.controlsOf(t, TypeSessionEnded); len(ended) != 0 {
		t.Errorf("session_ended = %v, want none (the ask's timeout must not orderly-close the classroom)", ended)
	}
}

// TestSubjectPickSendsEpochBumpToFrontend — the P1 wire pin: a pick during
// the ask must ship an interrupt_ack carrying the epoch bump to the client
// BEFORE the classroom's session_ready/greeting. The client's playback
// adopts the epoch ONLY from turn_start/interrupt_ack, and every
// announcement (the greeting included) carries no turn_start — without this
// ack the greeting's audio_start would be rejected as a stale epoch, no
// playback ack would ever come back, and the fresh classroom would die on
// its playback-ack timeout.
func TestSubjectPickSendsEpochBumpToFrontend(t *testing.T) {
	en := make([]lesson.Word, 0, 12)
	for i := 0; i < 12; i++ {
		en = append(en, lesson.Word{Word: "en" + string(rune('a'+i))})
	}
	zh := make([]lesson.Word, 0, 30)
	for i := 0; i < 30; i++ {
		zh = append(zh, lesson.Word{Word: "词" + string(rune('甲'+i%20)) + string(rune('0'+i%10))})
	}
	words := newFakeWords()
	words.setPeek(lesson.CategoryEnglish, en)
	words.setPeek(lesson.CategoryChinese, zh)
	words.setDue(lesson.CategoryChinese, zh)
	exec := newFakeExecutorNoRow(ttLessonID)
	tm := NewTurnManager(newFakeASR(), newFakeLLM(), newFakeTTS(1, 320), exec, ttConfig(words), nil, nil)
	s := &captureSender{}
	ttSubjectInit(t, tm, s)
	ttAwaitAskSent(t, s, 0) // the ask is out, unacked

	tm.HandleControl(s, mustJSON(t, SubjectPickMsg{Type: TypeSubjectPick, Category: lesson.CategoryChinese}))
	if !waitFor(t, 5*time.Second, func() bool { return len(s.controlsOf(t, TypeSessionReady)) == 1 }) {
		t.Fatal("session_ready never arrived after the pick")
	}

	acks := s.controlsOf(t, TypeInterruptAck)
	if len(acks) != 1 {
		t.Fatalf("interrupt_ack count = %d, want 1 (the epoch bump must reach the client)", len(acks))
	}
	if got := acks[0]["old_epoch"]; got != float64(0) {
		t.Errorf("interrupt_ack.old_epoch = %v, want 0", got)
	}
	if got := acks[0]["new_epoch"]; got != float64(1) {
		t.Errorf("interrupt_ack.new_epoch = %v, want 1", got)
	}
	// Order matters: the client must have adopted the new epoch BEFORE the
	// greeting's sentences start arriving.
	s.mu.Lock()
	order := append([][]byte(nil), s.controls...)
	s.mu.Unlock()
	firstAck, firstReady := -1, -1
	for i, raw := range order {
		var probe struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(raw, &probe) != nil {
			continue
		}
		if probe.Type == TypeInterruptAck && firstAck < 0 {
			firstAck = i
		}
		if probe.Type == TypeSessionReady && firstReady < 0 {
			firstReady = i
		}
	}
	if firstAck < 0 || firstReady < 0 || firstAck > firstReady {
		t.Errorf("interrupt_ack index = %d, session_ready index = %d, want the ack first", firstAck, firstReady)
	}
}

// TestAskGoroutineRefusesBumpedEpoch — the P3 scheduling pin: a pick
// processed BEFORE the ask goroutine first runs must leave the ask dead.
// The sequence is deterministic here: arm the clarify (parking epoch 0),
// process the pick's stopAskPlayback (epoch 0→1), and only THEN let the ask
// goroutine enter with the epoch it fixed at arm time — it must refuse
// (speakTurn mutates playback state and registers waits BEFORE any TTS
// call, so a cancelled context alone would not have stopped it).
func TestAskGoroutineRefusesBumpedEpoch(t *testing.T) {
	en := make([]lesson.Word, 0, 12)
	for i := 0; i < 12; i++ {
		en = append(en, lesson.Word{Word: "en" + string(rune('a'+i))})
	}
	zh := make([]lesson.Word, 0, 30)
	for i := 0; i < 30; i++ {
		zh = append(zh, lesson.Word{Word: "词" + string(rune('甲'+i%20)) + string(rune('0'+i%10))})
	}
	words := newFakeWords()
	words.setPeek(lesson.CategoryEnglish, en)
	words.setPeek(lesson.CategoryChinese, zh)
	tm := NewTurnManager(newFakeASR(), newFakeLLM(), newFakeTTS(1, 320), newFakeExecutorNoRow(ttLessonID), ttConfig(words), nil, nil)
	s := &captureSender{}

	// Arm the clarify exactly as startSubjectAsk does, WITHOUT launching the
	// announcement goroutine: pendingAsk parked, state listening, ask epoch
	// fixed at the (pre-bump) value 0.
	initMsg := SessionInitMsg{Type: TypeSessionInit, Audio: AudioSpec{SampleRate: 16000, Format: "s16le", Channels: 1}, Category: CategoryAuto}
	tm.mu.Lock()
	tm.pendingAsk = &initMsg
	tm.state = StateListening
	askEpoch := tm.epoch
	tm.mu.Unlock()

	// The pick lands first: its full termination bumps the epoch.
	tm.stopAskPlayback(s)

	// The ask goroutine finally enters, carrying the epoch fixed at arm time.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	tm.runAnnouncementDisplayAt(s, askEpoch, ctx, cancel, subjectAskText, subjectAskText, purposeSubjectAsk, nil)

	if got := s.countOf(TypeTeacherSentence); got != 0 {
		t.Errorf("teacher sentences from the dead ask = %d, want 0 (a bumped ask must never speak)", got)
	}
	tm.mu.Lock()
	waits := len(tm.pendingPlayback)
	state := tm.state
	tm.mu.Unlock()
	if waits != 0 {
		t.Errorf("pendingPlayback entries after the dead ask = %d, want 0", waits)
	}
	if state != StateListening {
		t.Errorf("state after the dead ask = %q, want listening untouched", state)
	}
}

// ttArmAsk parks the clarify exactly as startSubjectAsk does — pendingAsk
// set, listening, the ask's epoch fixed at its pre-bump value — WITHOUT
// launching the announcement goroutine, so a test can interleave the pick
// BEFORE the ask (or its retry) first runs, deterministically.
func ttArmAsk(tm *TurnManager) uint64 {
	initMsg := SessionInitMsg{Type: TypeSessionInit, Audio: AudioSpec{SampleRate: 16000, Format: "s16le", Channels: 1}, Category: CategoryAuto}
	tm.mu.Lock()
	defer tm.mu.Unlock()
	tm.pendingAsk = &initMsg
	tm.state = StateListening
	return tm.epoch
}

// TestAskRetryRidesAnswerTurnEpoch — the review P2 pin: a spoken answer's
// retry announcement must carry the ANSWER TURN's captured epoch. If a pick
// lands between the answer's epoch check and the retry's announcement
// start, the bumped current epoch must NOT be re-read — the retry would
// otherwise pass runAnnouncementDisplay's fresh snapshot and speak "老师
// 没听清" over the classroom the pick just opened.
func TestAskRetryRidesAnswerTurnEpoch(t *testing.T) {
	words := newFakeWords()
	words.setPeek(lesson.CategoryEnglish, ttCards("en1", "en2", "en3"))
	words.setPeek(lesson.CategoryChinese, ttCards("词1", "词2", "词3"))
	tm := NewTurnManager(newFakeASR(), newFakeLLM(), newFakeTTS(1, 320), newFakeExecutorNoRow(ttLessonID), ttConfig(words), nil, nil)
	s := &captureSender{}

	answerEpoch := ttArmAsk(tm) // the child's answer turn captured epoch 0

	// The pick is processed BEFORE the retry announcement starts: full
	// termination, epoch bump, everything.
	tm.stopAskPlayback(s)

	// The retry finally launches, carrying the answer turn's epoch.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	tm.runAnnouncementDisplayAt(s, answerEpoch, ctx, cancel, subjectAskRetryText, subjectAskRetryText, purposeSubjectAsk, nil)

	if got := s.countOf(TypeTeacherSentence); got != 0 {
		t.Errorf("teacher sentences from the dead retry = %d, want 0 (a bumped retry must never speak)", got)
	}
	tm.mu.Lock()
	waits := len(tm.pendingPlayback)
	state := tm.state
	pending := tm.playbackPending
	tm.mu.Unlock()
	if waits != 0 {
		t.Errorf("pendingPlayback entries after the dead retry = %d, want 0", waits)
	}
	if state == StateSpeaking {
		t.Error("the dead retry drove the FSM to speaking")
	}
	if pending {
		t.Error("the dead retry raised a playback gate")
	}

	// The REAL call path pin: handleSubjectAnswerTurn itself, answering with
	// the (now stale) turn epoch, must stay equally dead. If the call site
	// ever regresses to runAnnouncement's fresh-epoch snapshot, the retry
	// speaks over the picked classroom and this goes red.
	words2 := newFakeWords()
	words2.setPeek(lesson.CategoryEnglish, ttCards("en1", "en2", "en3"))
	words2.setPeek(lesson.CategoryChinese, ttCards("词1", "词2", "词3"))
	tm2 := NewTurnManager(newFakeASR(), newFakeLLM(), newFakeTTS(1, 320), newFakeExecutorNoRow(ttLessonID), ttConfig(words2), nil, nil)
	s2 := &captureSender{}
	answerEpoch2 := ttArmAsk(tm2)
	tm2.stopAskPlayback(s2)
	ctx2, cancel2 := context.WithCancel(context.Background())
	defer cancel2()
	tm2.handleSubjectAnswerTurn(s2, "今天天气不错", answerEpoch2, ctx2, cancel2)
	if got := s2.countOf(TypeTeacherSentence); got != 0 {
		t.Errorf("teacher sentences from the dead retry call path = %d, want 0", got)
	}
}

// TestSettledStaleAnnouncerCannotLiftNewClassroomGate — the review P2 pin:
// when the ask's FINAL ack lands just BEFORE a pick bumps the epoch and
// opens the new classroom, the ask goroutine's late recovery from its
// playback wait still sees completed=true — its settle (and its timeout
// twin) must be epoch-gated ATOMICALLY with the state update, or it lifts
// the fresh greeting's playbackPending gate / closes the classroom.
func TestSettledStaleAnnouncerCannotLiftNewClassroomGate(t *testing.T) {
	en := make([]lesson.Word, 0, 12)
	for i := 0; i < 12; i++ {
		en = append(en, lesson.Word{Word: "en" + string(rune('a'+i))})
	}
	zh := make([]lesson.Word, 0, 30)
	for i := 0; i < 30; i++ {
		zh = append(zh, lesson.Word{Word: "词" + string(rune('甲'+i%20)) + string(rune('0'+i%10))})
	}
	words := newFakeWords()
	words.setPeek(lesson.CategoryEnglish, en)
	words.setPeek(lesson.CategoryChinese, zh)
	words.setDue(lesson.CategoryChinese, zh)
	exec := newFakeExecutorNoRow(ttLessonID)
	tm := NewTurnManager(newFakeASR(), newFakeLLM(), newFakeTTS(1, 320), exec, ttConfig(words), nil, nil)
	s := &captureSender{}
	ttSubjectInit(t, tm, s)

	// The ask plays out and its FINAL ack lands: the ask goroutine is
	// released from its wait with completed=true — but suppose it has not
	// gotten any further yet (the pick is processed first).
	ttAwaitAskSent(t, s, 0)
	askSentences := s.countOf(TypeTeacherSentence)
	askEpoch := uint64(0)
	ttAckLast(t, tm, s, askEpoch)

	// The pick: full termination + the new classroom, whose greeting gate
	// is now pending under epoch 1.
	tm.HandleControl(s, mustJSON(t, SubjectPickMsg{Type: TypeSubjectPick, Category: lesson.CategoryChinese}))
	if !waitFor(t, 5*time.Second, func() bool { return len(s.controlsOf(t, TypeSessionReady)) == 1 }) {
		t.Fatal("session_ready never arrived after the pick")
	}
	if !waitFor(t, 5*time.Second, func() bool {
		tm.mu.Lock()
		defer tm.mu.Unlock()
		return tm.playbackPending
	}) {
		t.Fatal("the greeting's playback gate never armed")
	}

	// The ask goroutine finally recovers: settle under its OWN (now stale)
	// epoch must be refused — the greeting's gate stays down.
	if tm.settleListeningAt(s, askEpoch) {
		t.Error("the stale announcer's settle took effect over the new classroom")
	}
	tm.mu.Lock()
	gate := tm.playbackPending
	closed := tm.closed
	tm.mu.Unlock()
	if !gate {
		t.Error("the stale announcer's settle lifted the greeting's uplink gate")
	}
	if closed {
		t.Error("the settle path closed the fresh classroom")
	}

	// The timeout twin: a stale orderlyClose must not close it either.
	tm.orderlyCloseAt(askEpoch)
	tm.mu.Lock()
	closed = tm.closed
	tm.mu.Unlock()
	if closed {
		t.Error("the stale announcer's timeout closed the fresh classroom")
	}
	if ended := s.controlsOf(t, TypeSessionEnded); len(ended) != 0 {
		t.Errorf("session_ended = %v, want none from a stale announcer", ended)
	}

	// The greeting's own final ack (epoch 1) settles normally — the healthy
	// path is untouched. Wait for the greeting's sentences to be fully out
	// first, so the ack lands on ITS final sentence, not on a leftover ask
	// sentence the slower (-race) scheduling has not yet replaced.
	ttAwaitAskSent(t, s, askSentences)
	tm.mu.Lock()
	current := tm.epoch
	tm.mu.Unlock()
	ttAckLast(t, tm, s, current)
	ttWaitClarifyOpen(t, tm)
}

// TestMidBindingAskTimeoutCannotClosePickedClassroom — the review P2/P3 pin:
// the pick's locked half consumes the pendingAsk AND bumps the epoch in ONE
// lock hold. The seam (subjectTakeInterleave) fires between the two steps
// and probes the discipline DIRECTLY: TryLock on tm.mu succeeds only if the
// implementation dropped the hold between the steps — and the probe then
// runs the ask's OLD-epoch ack timeout synchronously in that very gap, so a
// split implementation (consume → unlock → bump) deterministically closes
// the picked classroom and turns this red. While the hold is continuous the
// probe can never interleave anything, and the post-take timeout below is
// refused by the already-bumped epoch.
func TestMidBindingAskTimeoutCannotClosePickedClassroom(t *testing.T) {
	en := make([]lesson.Word, 0, 12)
	for i := 0; i < 12; i++ {
		en = append(en, lesson.Word{Word: "en" + string(rune('a'+i))})
	}
	zh := make([]lesson.Word, 0, 30)
	for i := 0; i < 30; i++ {
		zh = append(zh, lesson.Word{Word: "词" + string(rune('甲'+i%20)) + string(rune('0'+i%10))})
	}
	words := newFakeWords()
	words.setPeek(lesson.CategoryEnglish, en)
	words.setPeek(lesson.CategoryChinese, zh)
	words.setDue(lesson.CategoryChinese, zh)
	exec := newFakeExecutorNoRow(ttLessonID)
	tm := NewTurnManager(newFakeASR(), newFakeLLM(), newFakeTTS(1, 320), exec, ttConfig(words), nil, nil)
	s := &captureSender{}
	ttSubjectInit(t, tm, s)
	ttAwaitAskSent(t, s, 0)
	askSentences := s.countOf(TypeTeacherSentence)
	tm.mu.Lock()
	askEpoch := tm.epoch
	tm.mu.Unlock()

	// The ask's ack timeout, injected between the consumption and the bump:
	// only runnable if the take dropped tm.mu in between.
	tm.subjectTakeInterleave = func() {
		if tm.mu.TryLock() {
			tm.mu.Unlock()
			tm.orderlyCloseAt(askEpoch)
		}
	}
	m, oldEpoch, ok := tm.takeSubjectAsk(lesson.CategoryChinese, askEpoch, true)
	tm.subjectTakeInterleave = nil
	if !ok {
		t.Fatal("takeSubjectAsk refused a live clarify")
	}

	// And even a timeout landing only AFTER the take must find the old epoch
	// stale — the bump belongs to the take's own lock hold.
	tm.orderlyCloseAt(oldEpoch)
	tm.mu.Lock()
	closed := tm.closed
	epoch := tm.epoch
	tm.mu.Unlock()
	if closed {
		t.Fatal("the mid-binding timeout closed the picked classroom")
	}
	if epoch != oldEpoch+1 {
		t.Fatalf("epoch = %d, want %d (the bump belongs to the take's own lock hold)", epoch, oldEpoch+1)
	}
	if ended := s.controlsOf(t, TypeSessionEnded); len(ended) != 0 {
		t.Errorf("session_ended = %v, want none from the refused timeout", ended)
	}

	// The unlocked half then opens the classroom, alive and well.
	tm.finalizeSubjectAsk(s, m, oldEpoch, lesson.CategoryChinese, "voice")
	if ready := s.controlsOf(t, TypeSessionReady); len(ready) != 1 {
		t.Fatalf("session_ready count = %d, want 1", len(ready))
	}
	if got := s.controlsOf(t, TypeSessionReady)[0]["category"]; got != lesson.CategoryChinese {
		t.Errorf("session_ready.category = %v, want chinese", got)
	}
	// And the greeting plays and settles normally under the new epoch.
	ttAwaitAskSent(t, s, askSentences)
	ttAckLast(t, tm, s, epoch)
	ttWaitClarifyOpen(t, tm)
}

// TestFinalizeSkipsClosedSession — an end_session that closes the session
// between the binding's locked and unlocked halves must not be answered
// with session_ready or a greeting: a closed classroom can never be acked.
func TestFinalizeSkipsClosedSession(t *testing.T) {
	words := newFakeWords()
	words.setPeek(lesson.CategoryEnglish, ttCards("en1", "en2", "en3"))
	words.setPeek(lesson.CategoryChinese, ttCards("词1", "词2", "词3"))
	exec := newFakeExecutorNoRow(ttLessonID)
	tm := NewTurnManager(newFakeASR(), newFakeLLM(), newFakeTTS(1, 320), exec, ttConfig(words), nil, nil)
	s := &captureSender{}
	ttSubjectInit(t, tm, s)
	ttAwaitAskSent(t, s, 0)
	sentences := s.countOf(TypeTeacherSentence)

	m, oldEpoch, ok := tm.takeSubjectAsk(lesson.CategoryEnglish, 0, false)
	if !ok {
		t.Fatal("takeSubjectAsk refused a live clarify")
	}
	// An end_session wins the race between the halves.
	tm.internalClose()

	tm.finalizeSubjectAsk(s, m, oldEpoch, lesson.CategoryEnglish, "button")
	if ready := s.controlsOf(t, TypeSessionReady); len(ready) != 0 {
		t.Errorf("session_ready after close = %d, want 0 (a closed session must not open a classroom)", len(ready))
	}
	if got := s.countOf(TypeTeacherSentence); got != sentences {
		t.Errorf("teacher sentences after close = %d, want still %d (no greeting for a closed session)", got, sentences)
	}
	if ids := exec.ensureIDCalls(); len(ids) != 0 {
		t.Errorf("EnsureLessonCards ids = %v, want none", ids)
	}
}

// TestEndSessionDuringPickedInitIOSendsNoClassroom — PR #60 review P2: the
// voice answer's finalizeSubjectAsk passes its stillOpen gate, then runs the
// picked classroom's init IO (due fetch → row creation → session_ready →
// greeting). An end_session the read loop processes DURING that IO must end
// the story: no lesson row, no session_ready, no greeting — nothing on the
// wire after session_ended. The interleave is deterministic: end_session
// fires from INSIDE the due fetch (a real IO point of the picked init),
// never from a sleep. An entry-only stillOpen check passes this test's
// ordering and still ships the classroom — the gates must sit at the sends.
func TestEndSessionDuringPickedInitIOSendsNoClassroom(t *testing.T) {
	en := make([]lesson.Word, 0, 12)
	for i := 0; i < 12; i++ {
		en = append(en, lesson.Word{Word: "en" + string(rune('a'+i))})
	}
	zh := make([]lesson.Word, 0, 30)
	for i := 0; i < 30; i++ {
		zh = append(zh, lesson.Word{Word: "词" + string(rune('甲'+i%20)) + string(rune('0'+i%10))})
	}
	words := newFakeWords()
	words.setPeek(lesson.CategoryEnglish, en)
	words.setPeek(lesson.CategoryChinese, zh)
	words.setDue(lesson.CategoryChinese, zh)
	exec := newFakeExecutorNoRow(ttLessonID)
	tm := NewTurnManager(newFakeASR(), newFakeLLM(), newFakeTTS(1, 320), exec, ttConfig(words), nil, nil)
	s := &captureSender{}
	ttSubjectInit(t, tm, s)
	ttAwaitAskSent(t, s, 0)
	sentences := s.countOf(TypeTeacherSentence)
	ttAckLast(t, tm, s, 0) // settle the ask so its goroutine is done interfering

	// The spoken answer's locked half binds 语文 (epoch 0 = the answer turn's).
	m, oldEpoch, ok := tm.takeSubjectAsk(lesson.CategoryChinese, 0, true)
	if !ok {
		t.Fatal("takeSubjectAsk refused a live clarify")
	}

	// end_session lands while the picked classroom's due fetch is running —
	// AFTER finalizeSubjectAsk's stillOpen gate has already passed.
	words.onFetch = func() { tm.handleEndSession(s) }

	tm.finalizeSubjectAsk(s, m, oldEpoch, lesson.CategoryChinese, "voice")

	if ready := s.controlsOf(t, TypeSessionReady); len(ready) != 0 {
		t.Errorf("session_ready after mid-init end_session = %d, want 0", len(ready))
	}
	if got := s.countOf(TypeTeacherSentence); got != sentences {
		t.Errorf("teacher sentences after mid-init end_session = %d, want still %d (no greeting)", got, sentences)
	}
	if ids := exec.ensureIDCalls(); len(ids) != 0 {
		t.Errorf("EnsureLessonCards ids = %v, want none (a closed session creates no row)", ids)
	}
	if ended := s.controlsOf(t, TypeSessionEnded); len(ended) != 1 {
		t.Fatalf("session_ended count = %d, want exactly the end_session's own 1", len(ended))
	}
	if order := s.typeOrder(); order[len(order)-1] != TypeSessionEnded {
		t.Errorf("last wire message = %q, want session_ended (nothing may follow it)", order[len(order)-1])
	}
}

// TestClosedSessionRefusesLateAnnouncementEntries — the second half of the
// review P2: a greeting/no-due goroutine launched just before an end_session
// but not yet scheduled must die AT ENTRY, not speak into the closed session.
// The closed refusal rides the SAME lock hold as runAnnouncementDisplayAt's
// epoch check, and runNaturalEnding gets its own entry gate — a close that
// does not bump the epoch must still kill a not-yet-started announcement.
func TestClosedSessionRefusesLateAnnouncementEntries(t *testing.T) {
	tm := NewTurnManager(newFakeASR(), newFakeLLM(), newFakeTTS(1, 320), newFakeExecutorNoRow(ttLessonID), ttConfig(newFakeWords()), nil, nil)
	s := &captureSender{}
	tm.handleEndSession(s) // session_ended out; the session is closed

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	tm.runAnnouncementDisplayAt(s, 0, ctx, cancel, "同学们好，我们开始听写", "同学们好，我们开始听写", purposeGreeting, nil)
	if got := s.countOf(TypeTeacherSentence); got != 0 {
		t.Errorf("teacher sentences from a late greeting entry = %d, want 0", got)
	}
	tm.runNaturalEnding(s, ctx, cancel, "今天没有要默写的词语", purposeNoDue, ReasonNoDueWords)
	if got := s.countOf(TypeTeacherSentence); got != 0 {
		t.Errorf("teacher sentences from a late no-due entry = %d, want 0", got)
	}
	if got := s.countOf(TypeError); got != 0 {
		t.Errorf("error messages from a late no-due entry = %d, want 0 (no ack-timeout theater)", got)
	}
	if got := s.countOf(TypeSessionEnded); got != 1 {
		t.Errorf("session_ended count = %d, want 1 (only the end_session's own)", got)
	}
}
