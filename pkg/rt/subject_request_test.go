// subject_request_test.go — issue #69 rethink3 S2（初始化身份作为完整请求
// 传递）: the subject clarify parks the WHOLE init request — message, bound
// subject and reserved classroom generation as ONE identity — and the
// binding's locked half consumes all of it in a single critical section. The
// continuations here resume LATE, after a newer session_init on the same
// connection has replaced the shared identity fields, exactly as the
// scheduler could interleave the voice-answer turn against the read loop:
// the late binding must neither install under the newer reservation nor
// disturb it, and the newer request must still complete its own
// initialization.
package rt

import (
	"context"
	"testing"
	"time"

	"github.com/xiaotian/home-tutor-agent/pkg/database"
	"github.com/xiaotian/home-tutor-agent/pkg/lesson"
)

// ttPerIDExecutor keys lesson rows by id: the shared fakeExecutor holds ONE
// row, so A's stale continuation creating its own subject's row would make
// B's GetLessonState find A's row under B's id — the S2 interleavings need
// the real store's per-id rows. The call ledgers (ensureIDs, stateIDs) stay
// on the embedded fake so the existing assertions keep working.
type ttPerIDExecutor struct {
	*fakeExecutor
	rows map[string]lesson.Lesson
}

// GetLessonState implements LessonExecutor (per-id read-back).
func (e *ttPerIDExecutor) GetLessonState(_ context.Context, lessonID string) (lesson.Lesson, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.stateCalls++
	e.stateIDs = append(e.stateIDs, lessonID)
	if l, ok := e.rows[lessonID]; ok {
		return l, nil
	}
	return lesson.Lesson{}, lesson.ErrLessonNotFound
}

// EnsureLessonCards implements LessonExecutor (per-id first-content-wins
// insert, like the real INSERT OR IGNORE).
func (e *ttPerIDExecutor) EnsureLessonCards(_ context.Context, lessonID string, cards []lesson.Word, category string) (lesson.Lesson, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.ensureCardsCalls++
	e.ensureIDs = append(e.ensureIDs, lessonID)
	if l, ok := e.rows[lessonID]; ok {
		return l, nil
	}
	l := lesson.Lesson{LessonID: lessonID, Cards: cards, Words: cardWords(cards), Category: category}
	e.rows[lessonID] = l
	return l, nil
}

// seedRow installs a PRE-EXISTING row under its own id — yesterday's
// classroom — without touching the call ledgers the assertions read.
func (e *ttPerIDExecutor) seedRow(lessonID string, cards []lesson.Word, category string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.rows[lessonID] = lesson.Lesson{
		LessonID: lessonID, Cards: cards, Words: cardWords(cards), Category: category,
	}
}

// ttSubjectScopeFixture builds the both-subjects-due vocabulary S2 runs on:
// A answers the clarify with 语文, B's own pick is english, so every
// classroom, row and fetch below is attributable to exactly one request.
func ttSubjectScopeFixture(t *testing.T) (*TurnManager, *captureSender, *fakeWordSource, *ttPerIDExecutor) {
	t.Helper()
	en := ttCards("en1", "en2", "en3")
	zh := ttCards("词1", "词2", "词3")
	words := newFakeWords()
	words.setPeek(lesson.CategoryEnglish, en)
	words.setPeek(lesson.CategoryChinese, zh)
	words.setDue(lesson.CategoryEnglish, en)
	words.setDue(lesson.CategoryChinese, zh)
	exec := &ttPerIDExecutor{fakeExecutor: newFakeExecutorNoRow(ttLessonID), rows: map[string]lesson.Lesson{}}
	tm := NewTurnManager(newFakeASR(), newFakeLLM(), newFakeTTS(1, 320), exec, ttConfig(words), nil, nil)
	tm.taskTrack = &serverTaskTracker{}
	return tm, &captureSender{}, words, exec
}

// ttLateBindingParkAndTake arms A's clarify and consumes it with A's locked
// half — 语文, epoch 0 — returning the paused request with A's ask settled.
func ttLateBindingParkAndTake(t *testing.T, tm *TurnManager, s *captureSender) (classroomInitRequest, uint64, int) {
	t.Helper()
	ttSubjectInit(t, tm, s)
	if asks := s.controlsOf(t, TypeSubjectAsk); len(asks) != 1 {
		t.Fatalf("subject_ask count = %d, want 1 (A's clarify)", len(asks))
	}
	ttAwaitAskSent(t, s, 0)
	aSentences := s.countOf(TypeTeacherSentence)
	ttAckLast(t, tm, s, 0) // settle A's ask goroutine so only B's ask stays live
	req, oldEpoch, ok := tm.takeSubjectAsk(lesson.CategoryChinese, 0, true)
	if !ok {
		t.Fatal("takeSubjectAsk refused A's live clarify")
	}
	return req, oldEpoch, aSentences
}

// ttParkReplacementAsk parks B's OWN auto clarify on the same connection —
// the read loop's session_init that lands while A's binding sits between its
// halves — and returns the sentence count its ask announcement reached.
func ttParkReplacementAsk(t *testing.T, tm *TurnManager, s *captureSender, prevSentences int) int {
	t.Helper()
	ttSubjectInit(t, tm, s)
	ttAwaitAskSent(t, s, prevSentences)
	tm.mu.Lock()
	bParked := tm.pendingAsk != nil
	pendingGen := tm.pendingClassroomGen
	tm.mu.Unlock()
	if !bParked {
		t.Fatal("B's session_init parked no clarify")
	}
	if pendingGen != 2 {
		t.Fatalf("pending classroom generation = %d, want 2 (B's reservation)", pendingGen)
	}
	return s.countOf(TypeTeacherSentence)
}

// TestLateSubjectBindingCannotBorrowReplacementReservation — the S2
// acceptance: A's voice answer has CONSUMED the clarify message (locked half
// done) when the read loop parks B's own auto clarify on the same
// connection. A's unlocked half then resumes: it must not install under B's
// reserved generation (no late session_ready), must not consume B's parked
// identity, and B's own pick must still open B's classroom. Driving the two
// halves around B's real session_init is the deterministic form of the
// scheduler interleaving — the discipline takeSubjectAsk was split out for.
func TestLateSubjectBindingCannotBorrowReplacementReservation(t *testing.T) {
	tm, s, words, exec := ttSubjectScopeFixture(t)
	req, oldEpoch, aSentences := ttLateBindingParkAndTake(t, tm, s)
	bSentences := ttParkReplacementAsk(t, tm, s, aSentences)

	// A's unlocked half resumes. Every install decision in the continuation
	// is synchronous, so each fact below is deterministic at this point.
	tm.finalizeSubjectAsk(s, req, oldEpoch, lesson.CategoryChinese, "voice")

	if ready := s.controlsOf(t, TypeSessionReady); len(ready) != 0 {
		t.Fatalf("session_ready after A's late binding = %d, want 0 — the late binding must not install on B's reservation", len(ready))
	}
	tm.mu.Lock()
	bStillParked := tm.pendingAsk != nil
	tm.mu.Unlock()
	if !bStillParked {
		t.Fatal("A's late binding consumed B's parked clarify identity")
	}
	// A's continuation ran under its OWN bound subject — never the shared
	// field B's park has since rewritten to "auto".
	if cats := words.fetchedCategories(); len(cats) != 1 || cats[0] != lesson.CategoryChinese {
		t.Errorf("FetchDueWords categories after A's continuation = %v, want [chinese] (the request's own bound subject)", cats)
	}
	if ids := exec.ensureIDCalls(); len(ids) != 1 || ids[0] != ttLessonID+chineseLessonIDSuffix {
		t.Errorf("EnsureLessonCards ids = %v, want [%s] (A's own subject's row)", ids, ttLessonID+chineseLessonIDSuffix)
	}

	// B's pick then opens B's classroom — A's late binding kept the
	// reservation B needs.
	tm.HandleControl(s, mustJSON(t, SubjectPickMsg{Type: TypeSubjectPick, Category: lesson.CategoryEnglish}))
	ready := s.controlsOf(t, TypeSessionReady)
	if len(ready) != 1 {
		t.Fatalf("session_ready after B's pick = %d, want exactly 1 (B's own classroom)", len(ready))
	}
	if got := ready[0]["category"]; got != lesson.CategoryEnglish {
		t.Fatalf("session_ready.category = %v, want B's english — the late binding borrowed or consumed B's reservation", got)
	}
	tm.mu.Lock()
	scopeGen := uint64(0)
	if tm.activeScope != nil {
		scopeGen = tm.activeScope.gen
	}
	tutor := tm.tutor
	tm.mu.Unlock()
	if scopeGen != 2 || tutor == nil {
		t.Fatalf("active classroom gen=%d tutor=%v, want gen 2 with B's tutor installed", scopeGen, tutor)
	}

	// The two epoch notices shipped in order — A's transition first, then
	// B's pick bump — so the client's epoch only ever moves forward.
	acks := s.controlsOf(t, TypeInterruptAck)
	if len(acks) != 2 {
		t.Fatalf("interrupt_ack count = %d, want 2 (A's binding, then B's pick)", len(acks))
	}
	if got := acks[0]["old_epoch"]; got != float64(0) {
		t.Errorf("first interrupt_ack.old_epoch = %v, want 0 (A's transition)", got)
	}
	if got := acks[1]["old_epoch"]; got != float64(1) {
		t.Errorf("second interrupt_ack.old_epoch = %v, want 1 (B's pick)", got)
	}

	// B's greeting plays and settles; every server task then truly ends
	// before the final assertions.
	ttAwaitAskSent(t, s, bSentences)
	tm.mu.Lock()
	epoch := tm.epoch
	tm.mu.Unlock()
	ttAckLast(t, tm, s, epoch)
	ttWaitClarifyOpen(t, tm)
	if !ttPoll(5*time.Second, tm.serverTasksSettled) {
		t.Fatal("server tasks never settled after both classrooms' work")
	}
	if again := s.controlsOf(t, TypeSessionReady); len(again) != 1 {
		t.Errorf("session_ready count at the end = %d, want still 1 (only B's classroom)", len(again))
	}
	if ended := s.controlsOf(t, TypeSessionEnded); len(ended) != 0 {
		t.Errorf("session_ended = %d, want 0 (B's classroom must stay live)", len(ended))
	}
	if cats := words.fetchedCategories(); len(cats) != 2 || cats[0] != lesson.CategoryChinese || cats[1] != lesson.CategoryEnglish {
		t.Errorf("FetchDueWords categories = %v, want [chinese english] (each request under its own subject)", cats)
	}
	if ids := exec.ensureIDCalls(); len(ids) != 2 || ids[0] != ttLessonID+chineseLessonIDSuffix || ids[1] != ttLessonID {
		t.Errorf("EnsureLessonCards ids = %v, want [%s-chinese %s] (each request its own row)", ids, ttLessonID, ttLessonID)
	}
}

// TestLateSubjectBindingSkipsStaleEpochNotice — the late binding's client
// epoch notice: when B's clarify has ALREADY been picked and installed
// before A's unlocked half resumes, the epoch has moved past A's transition
// and B's own ack has shipped. The late binding must not also ship its stale
// (old → old+1) interrupt_ack: the client adopts interrupt_ack.new_epoch
// verbatim and stops its playback (rt-playback's onEpochBump), so the stale
// pair landing after the newer one would drag the player's epoch BACKWARDS
// and kill B's live audio — blocking the tutor install alone is not enough.
func TestLateSubjectBindingSkipsStaleEpochNotice(t *testing.T) {
	tm, s, words, _ := ttSubjectScopeFixture(t)
	req, oldEpoch, aSentences := ttLateBindingParkAndTake(t, tm, s)
	bSentences := ttParkReplacementAsk(t, tm, s, aSentences)

	// The child picks ENGLISH before A resumes: B's binding bumps 1→2,
	// ships its ack and installs B's classroom.
	tm.HandleControl(s, mustJSON(t, SubjectPickMsg{Type: TypeSubjectPick, Category: lesson.CategoryEnglish}))
	ready := s.controlsOf(t, TypeSessionReady)
	if len(ready) != 1 {
		t.Fatalf("session_ready after B's pick = %d, want 1 (B installed first)", len(ready))
	}
	if got := ready[0]["category"]; got != lesson.CategoryEnglish {
		t.Fatalf("session_ready.category = %v, want B's english", got)
	}

	// A's unlocked half finally resumes, its transition long superseded.
	tm.finalizeSubjectAsk(s, req, oldEpoch, lesson.CategoryChinese, "voice")

	acks := s.controlsOf(t, TypeInterruptAck)
	if len(acks) != 1 {
		t.Fatalf("interrupt_ack count = %d, want 1 — the late binding must not ship its stale epoch notice after B's newer one", len(acks))
	}
	if got := acks[0]["old_epoch"]; got != float64(1) {
		t.Errorf("interrupt_ack.old_epoch = %v, want 1 (B's pick, the only notice)", got)
	}
	if got := acks[0]["new_epoch"]; got != float64(2) {
		t.Errorf("interrupt_ack.new_epoch = %v, want 2", got)
	}
	// A installed nothing and left B's classroom untouched.
	if again := s.controlsOf(t, TypeSessionReady); len(again) != 1 {
		t.Errorf("session_ready after A's late binding = %d, want still 1 (only B's)", len(again))
	}
	tm.mu.Lock()
	epoch, scopeGen := tm.epoch, uint64(0)
	if tm.activeScope != nil {
		scopeGen = tm.activeScope.gen
	}
	tm.mu.Unlock()
	if epoch != 2 {
		t.Errorf("epoch after A's late binding = %d, want 2 (A must not move it again)", epoch)
	}
	if scopeGen != 2 {
		t.Errorf("active classroom gen = %d, want 2 (B's install survives)", scopeGen)
	}

	// B's greeting still plays and settles normally.
	ttAwaitAskSent(t, s, bSentences)
	ttAckLast(t, tm, s, epoch)
	ttWaitClarifyOpen(t, tm)
	if !ttPoll(5*time.Second, tm.serverTasksSettled) {
		t.Fatal("server tasks never settled")
	}
	if ended := s.controlsOf(t, TypeSessionEnded); len(ended) != 0 {
		t.Errorf("session_ended = %d, want 0 (B stays live)", len(ended))
	}
	// B picked first, A's continuation fetched after it — each under its own
	// subject, in that order.
	if cats := words.fetchedCategories(); len(cats) != 2 || cats[0] != lesson.CategoryEnglish || cats[1] != lesson.CategoryChinese {
		t.Errorf("FetchDueWords categories = %v, want [english chinese]", cats)
	}
}

// ttPracticeCategoryRecorder records the subject each practice pick runs
// under — the identity the S2 request must carry into the 加练 branch.
type ttPracticeCategoryRecorder struct {
	*fakePracticeSource
	cats []string
}

// List implements PracticeSource (records the filter's subject first).
func (r *ttPracticeCategoryRecorder) List(ctx context.Context, filter database.CardFilter) ([]database.Card, error) {
	r.mu.Lock()
	r.cats = append(r.cats, filter.Category)
	r.mu.Unlock()
	return r.fakePracticeSource.List(ctx, filter)
}

// recordedCategories returns the recorded practice-pick subjects in order.
func (r *ttPracticeCategoryRecorder) recordedCategories() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.cats...)
}

// TestLatePracticeBindingKeepsRequestSubject — the 加练 half of the
// inventory: a practice=true auto init parks on the SAME clarify (the ask
// branch precedes the practice branch), so its late continuation must carry
// the request's bound subject into handlePracticeInit — the shared field B's
// park has rewritten to "auto" must never scope A's practice pick.
func TestLatePracticeBindingKeepsRequestSubject(t *testing.T) {
	tm, s, words, exec := ttSubjectScopeFixture(t)
	practice := &ttPracticeCategoryRecorder{fakePracticeSource: &fakePracticeSource{cards: ptPracticeCards("cat", "dog")}}
	tm.cfg.Practice = practice
	tm.cfg.PracticeStore = newTestPracticeStore(t)

	// A's practice auto init parks on the clarify; the locked half binds 语文.
	tm.HandleControl(s, mustJSON(t, SessionInitMsg{
		Type: TypeSessionInit, Practice: true, Category: CategoryAuto,
		Audio: AudioSpec{SampleRate: 16000, Format: "s16le", Channels: 1},
	}))
	ttAwaitAskSent(t, s, 0)
	aSentences := s.countOf(TypeTeacherSentence)
	ttAckLast(t, tm, s, 0)
	req, oldEpoch, ok := tm.takeSubjectAsk(lesson.CategoryChinese, 0, true)
	if !ok {
		t.Fatal("takeSubjectAsk refused A's live clarify")
	}
	bSentences := ttParkReplacementAsk(t, tm, s, aSentences)

	// A's continuation runs the practice branch under its OWN subject.
	tm.finalizeSubjectAsk(s, req, oldEpoch, lesson.CategoryChinese, "voice")
	if got := practice.recordedCategories(); len(got) != 1 || got[0] != lesson.CategoryChinese {
		t.Errorf("practice pick subjects = %v, want [chinese] (the request's bound subject, never the shared field)", got)
	}
	if ready := s.controlsOf(t, TypeSessionReady); len(ready) != 0 {
		t.Fatalf("session_ready after A's late practice binding = %d, want 0 (generation 1 is superseded)", len(ready))
	}

	// B's pick still opens B's classroom, and every task settles.
	tm.HandleControl(s, mustJSON(t, SubjectPickMsg{Type: TypeSubjectPick, Category: lesson.CategoryEnglish}))
	ready := s.controlsOf(t, TypeSessionReady)
	if len(ready) != 1 {
		t.Fatalf("session_ready after B's pick = %d, want 1", len(ready))
	}
	if got := ready[0]["category"]; got != lesson.CategoryEnglish {
		t.Fatalf("session_ready.category = %v, want B's english", got)
	}
	ttAwaitAskSent(t, s, bSentences)
	tm.mu.Lock()
	epoch := tm.epoch
	tm.mu.Unlock()
	ttAckLast(t, tm, s, epoch)
	ttWaitClarifyOpen(t, tm)
	if !ttPoll(5*time.Second, tm.serverTasksSettled) {
		t.Fatal("server tasks never settled")
	}
	if got := practice.recordedCategories(); len(got) != 1 {
		t.Errorf("practice pick subjects = %v, want exactly one practice pick (B is a dictation init)", got)
	}
	if ids := exec.ensureIDCalls(); len(ids) != 2 {
		t.Errorf("EnsureLessonCards ids = %v, want A's practice row and B's english row", ids)
	}
	if cats := words.fetchedCategories(); len(cats) != 1 || cats[0] != lesson.CategoryEnglish {
		t.Errorf("FetchDueWords categories = %v, want [english] (only B's dictation fetch)", cats)
	}
}

// TestLateBindingRowAuthorityCannotRewriteLiveSubject — the 学科写入 half of
// the inventory: A's late continuation finds YESTERDAY'S chinese row (the
// row is the subject authority on a resume), but its reservation is
// superseded — it must not install that authority over the shared subject
// B's live english classroom runs under, or every later scoped write of B's
// (review submits included) would land in the wrong subject's row.
func TestLateBindingRowAuthorityCannotRewriteLiveSubject(t *testing.T) {
	tm, s, words, exec := ttSubjectScopeFixture(t)
	exec.seedRow(ttLessonID+chineseLessonIDSuffix, ttCards("旧词"), lesson.CategoryChinese)

	req, oldEpoch, aSentences := ttLateBindingParkAndTake(t, tm, s)
	bSentences := ttParkReplacementAsk(t, tm, s, aSentences)

	// B picks english first and installs its classroom.
	tm.HandleControl(s, mustJSON(t, SubjectPickMsg{Type: TypeSubjectPick, Category: lesson.CategoryEnglish}))
	ready := s.controlsOf(t, TypeSessionReady)
	if len(ready) != 1 {
		t.Fatalf("session_ready after B's pick = %d, want 1", len(ready))
	}
	if got := ready[0]["category"]; got != lesson.CategoryEnglish {
		t.Fatalf("session_ready.category = %v, want B's english", got)
	}

	// A's late continuation resumes yesterday's chinese ROW — the authority
	// may only be installed by the request that still owns the connection.
	tm.finalizeSubjectAsk(s, req, oldEpoch, lesson.CategoryChinese, "voice")
	if got := tm.sessionCategory(); got != lesson.CategoryEnglish {
		t.Fatalf("shared subject after A's late row-resume = %q, want english — a superseded continuation must not rewrite the live classroom's subject", got)
	}
	if again := s.controlsOf(t, TypeSessionReady); len(again) != 1 {
		t.Errorf("session_ready after A's late row-resume = %d, want still 1 (only B's)", len(again))
	}
	// A resumed the ROW (no fresh fetch); only B's dictation fetch ran.
	if cats := words.fetchedCategories(); len(cats) != 1 || cats[0] != lesson.CategoryEnglish {
		t.Errorf("FetchDueWords categories = %v, want [english] (a row resume never fetches)", cats)
	}
	if ids := exec.ensureIDCalls(); len(ids) != 1 || ids[0] != ttLessonID {
		t.Errorf("EnsureLessonCards ids = %v, want [%s] (a row resume never writes)", ids, ttLessonID)
	}

	// B's classroom is intact and its greeting settles; every task ends.
	ttAwaitAskSent(t, s, bSentences)
	tm.mu.Lock()
	epoch := tm.epoch
	tm.mu.Unlock()
	ttAckLast(t, tm, s, epoch)
	ttWaitClarifyOpen(t, tm)
	if !ttPoll(5*time.Second, tm.serverTasksSettled) {
		t.Fatal("server tasks never settled")
	}
	if ended := s.controlsOf(t, TypeSessionEnded); len(ended) != 0 {
		t.Errorf("session_ended = %d, want 0 (B stays live)", len(ended))
	}
}
