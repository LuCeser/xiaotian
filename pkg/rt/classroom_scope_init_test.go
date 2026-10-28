package rt

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/xiaotian/home-tutor-agent/pkg/lesson"
)

// holdFirstDueFetch parks the first FetchDueWords until release (R1 T2).
type holdFirstDueFetch struct {
	WordSource
	entered chan struct{}
	release chan struct{}
	once    sync.Once
	// openOnce makes openUp idempotent (§5.1): every failure path in the T2
	// test reclaims through it, and a repeated release must pair safely with
	// an explicit openUp instead of panicking on close-of-closed-channel.
	openOnce sync.Once
}

func newHoldFirstDueFetch(inner WordSource) *holdFirstDueFetch {
	return &holdFirstDueFetch{
		WordSource: inner,
		entered:    make(chan struct{}),
		release:    make(chan struct{}),
	}
}

func (h *holdFirstDueFetch) FetchDueWords(ctx context.Context, limit int, category string) ([]lesson.Word, error) {
	h.once.Do(func() { close(h.entered) })
	<-h.release
	return h.WordSource.FetchDueWords(ctx, limit, category)
}

func (h *holdFirstDueFetch) openUp() {
	h.openOnce.Do(func() { close(h.release) })
}

// ttInitCheckingRow opens a checking lesson through real session_init.
func ttInitCheckingRow(t *testing.T, exec *fakeExecutor, words WordSource) (*TurnManager, *captureSender) {
	t.Helper()
	cards := exec.lesson.Cards
	exec.lesson.Stage = lesson.StageChecking
	exec.lesson.CurrentIndex = len(cards)
	exec.lesson.Version = 4
	exec.lesson.Verdicts = make([]lesson.CheckVerdict, len(cards))
	for i := range cards {
		exec.lesson.Verdicts[i] = lesson.CheckVerdict{WordIndex: i, Correct: true, Manual: true}
	}
	tm := NewTurnManager(newFakeASR(), newFakeLLM(), newFakeTTS(1, 320), exec,
		ttConfig(words), NewEventRecorder(&captureLogger{}, "t"), nil)
	s := &captureSender{}
	ttInit(t, tm, s)
	return tm, s
}

// TestLateClassroomInitCannotOverrideActive (rethink2 T2): a slower dictation
// init must not install session_ready or a tutor after a newer practice init
// has already won the connection's classroom generation.
func TestLateClassroomInitCannotOverrideActive(t *testing.T) {
	words := newFakeWords(ttCards("苹果", "香蕉"))
	fetchHold := newHoldFirstDueFetch(words)
	exec := newFakeExecutorNoRow(ttLessonID)

	practice := &fakePracticeSource{cards: ptPracticeCards("cat", "dog")}
	ps := newTestPracticeStore(t)
	cfg := TurnManagerConfig{
		LessonID: ttLessonID, Words: fetchHold,
		Practice: practice, PracticeStore: ps,
	}
	tm := NewTurnManager(newFakeASR(), newFakeLLM(), newFakeTTS(1, 320), exec, cfg,
		NewEventRecorder(&captureLogger{}, "t"), nil)
	s := &captureSender{}

	initA := mustJSON(t, SessionInitMsg{
		Type: TypeSessionInit, Audio: AudioSpec{SampleRate: 16000, Format: "s16le", Channels: 1},
	})
	initB := mustJSON(t, SessionInitMsg{
		Type: TypeSessionInit, Practice: true,
		Audio: AudioSpec{SampleRate: 16000, Format: "s16le", Channels: 1},
	})

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		tm.HandleControl(s, initA)
	}()

	select {
	case <-fetchHold.entered:
	case <-time.After(3 * time.Second):
		fetchHold.openUp()
		wg.Wait()
		t.Fatal("the first session_init never reached FetchDueWords")
	}

	tm.HandleControl(s, initB)
	if !waitFor(t, 5*time.Second, func() bool { return s.countOf(TypeSessionReady) >= 1 }) {
		fetchHold.openUp()
		wg.Wait()
		t.Fatal("practice session_ready never arrived")
	}
	if !waitFor(t, 5*time.Second, func() bool { return s.countOf(TypeTeacherSentence) >= 1 }) {
		fetchHold.openUp()
		wg.Wait()
		t.Fatal("practice greeting never started before the late dictation init is released")
	}
	readyAfterB := s.countOf(TypeSessionReady)
	sentsAfterB := s.countOf(TypeTeacherSentence)

	tm.mu.Lock()
	practiceLesson := tm.tutor != nil && tm.tutor.lessonID != "" && tm.tutor.practice
	tm.mu.Unlock()
	if !practiceLesson {
		fetchHold.openUp()
		wg.Wait()
		t.Fatal("active tutor is not the practice classroom after B won")
	}

	fetchHold.openUp()
	wg.Wait()

	if got := s.countOf(TypeSessionReady); got != readyAfterB {
		t.Fatalf("session_ready count = %d, want %d (late dictation init must not ship ready)", got, readyAfterB)
	}
	if got := s.countOf(TypeTeacherSentence); got != sentsAfterB {
		t.Fatalf("teacher_sentence count = %d, want %d (late dictation init must not greet)", got, sentsAfterB)
	}
	tm.mu.Lock()
	stillPractice := tm.tutor != nil && tm.tutor.practice
	tm.mu.Unlock()
	if !stillPractice {
		t.Fatal("late dictation init replaced the practice classroom tutor")
	}
}

// TestSupersededClassroomSubmitCannotWriteBack (rethink2 T1 init/writeback):
// after classroom B replaces checking classroom A on the same connection, A's
// in-flight formal submit must not flip B's in-memory checking state.
func TestSupersededClassroomSubmitCannotWriteBack(t *testing.T) {
	words := newFakeWords(ttCards("苹果", "香蕉"))
	exec := newFakeExecutor(ttLessonID, ttCards("苹果", "香蕉"))
	tm, s := ttInitCheckingRow(t, exec, words)
	tm.taskTrack = &serverTaskTracker{} // S1: whole-task settle for the waits below

	hold := newHoldFirstCompleteCheck(exec)
	tm.exec = hold

	reclaimAndFail := func(msg string) {
		hold.openUp()
		if !waitFormalSubmitSettled(t, tm, hold, 5*time.Second) {
			t.Fatalf("%s (reclaim: CompleteCheck calls=%d returned=%d never settled)",
				msg, hold.callCount(), hold.returnedCount())
		}
		t.Fatal(msg)
	}

	tm.HandleControl(s, mustJSON(t, CheckSubmitMsg{Type: TypeCheckSubmit, RequestID: "scope-a-submit"}))
	select {
	case <-hold.entered:
	case <-time.After(3 * time.Second):
		reclaimAndFail("classroom A submit never reached CompleteCheck")
	}

	practice := &fakePracticeSource{cards: ptPracticeCards("cat", "dog")}
	ps := newTestPracticeStore(t)
	cfg := TurnManagerConfig{
		LessonID: ttLessonID, Words: words,
		Practice: practice, PracticeStore: ps,
	}
	tm.cfg = cfg
	if fe, ok := hold.LessonExecutor.(*fakeExecutor); ok {
		// EnsureLessonCards reuses the in-memory row: reset index/stage so
		// practice greeting does not inherit checking CurrentIndex, but keep
		// Version=4 so A's admitted CompleteCheck still succeeds (success path).
		fe.mu.Lock()
		fe.lesson.CurrentIndex = 0
		fe.lesson.Stage = ""
		fe.lesson.Verdicts = nil
		fe.mu.Unlock()
	}
	tm.HandleControl(s, mustJSON(t, SessionInitMsg{
		Type: TypeSessionInit, Practice: true,
		Audio: AudioSpec{SampleRate: 16000, Format: "s16le", Channels: 1},
	}))
	if !waitFor(t, 5*time.Second, func() bool {
		tm.mu.Lock()
		ok := tm.tutor != nil && tm.tutor.practice
		tm.mu.Unlock()
		return ok
	}) {
		reclaimAndFail("practice classroom B never installed")
	}

	tm.mu.Lock()
	wantVer := tm.tutor.version
	wantChecking := tm.tutor.checking
	tm.mu.Unlock()

	hold.openUp()
	if !waitFormalSubmitSettled(t, tm, hold, 5*time.Second) {
		reclaimAndFail(fmt.Sprintf("classroom A submit task never finished (calls=%d returned=%d)",
			hold.callCount(), hold.returnedCount()))
	}

	tm.mu.Lock()
	gotVer, gotChecking := tm.tutor.version, tm.tutor.checking
	stillB := tm.tutor.practice
	tm.mu.Unlock()
	if !stillB {
		t.Fatal("active tutor is no longer the practice classroom after A's save finished")
	}
	if gotVer != wantVer {
		t.Fatalf("tutor.version = %d, want %d (superseded submit must not write back)", gotVer, wantVer)
	}
	if gotChecking != wantChecking {
		t.Fatalf("tutor.checking = %v, want %v (superseded submit must not flip B's stage on success path)", gotChecking, wantChecking)
	}
}

// TestClassroomReplaceNotifiesClientEpoch (R1 review P1): after classroom A's
// opening is underway, replacing with classroom B must ship interrupt_ack
// BEFORE B's session_ready so production playback adopts the bumped epoch and
// accepts B's greeting audio_start.
func TestClassroomReplaceNotifiesClientEpoch(t *testing.T) {
	words := newFakeWords(ttCards("苹果", "香蕉"))
	exec := newFakeExecutorNoRow(ttLessonID)
	practice := &fakePracticeSource{cards: ptPracticeCards("cat", "dog")}
	ps := newTestPracticeStore(t)
	cfg := TurnManagerConfig{
		LessonID: ttLessonID, Words: words,
		Practice: practice, PracticeStore: ps,
	}
	tm := NewTurnManager(newFakeASR(), newFakeLLM(), newFakeTTS(1, 320), exec, cfg,
		NewEventRecorder(&captureLogger{}, "t"), nil)
	s := &captureSender{}

	ttInit(t, tm, s)
	if !waitFor(t, 5*time.Second, func() bool { return s.countOf(TypeTeacherSentence) >= 1 }) {
		t.Fatal("classroom A greeting never started before replace")
	}
	tm.mu.Lock()
	epochA := tm.epoch
	tm.mu.Unlock()
	ttAckLast(t, tm, s, epochA)
	sentsBeforeB := s.countOf(TypeTeacherSentence)
	startsBeforeB := s.countOf(TypeAudioStart)

	tm.HandleControl(s, mustJSON(t, SessionInitMsg{
		Type: TypeSessionInit, Practice: true,
		Audio: AudioSpec{SampleRate: 16000, Format: "s16le", Channels: 1},
	}))
	if !waitFor(t, 5*time.Second, func() bool { return s.countOf(TypeSessionReady) >= 2 }) {
		t.Fatal("practice session_ready never arrived")
	}

	acks := s.controlsOf(t, TypeInterruptAck)
	if len(acks) < 1 {
		t.Fatalf("interrupt_ack count = %d, want at least 1 (classroom replace must notify playback epoch)", len(acks))
	}
	s.mu.Lock()
	order := append([][]byte(nil), s.controls...)
	s.mu.Unlock()
	firstAck, secondReady := -1, -1
	readyCount := 0
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
		if probe.Type == TypeSessionReady {
			readyCount++
			if readyCount == 2 {
				secondReady = i
			}
		}
	}
	if firstAck < 0 || secondReady < 0 || firstAck > secondReady {
		t.Fatalf("interrupt_ack index=%d second session_ready index=%d, want ack before B's ready", firstAck, secondReady)
	}
	if !waitFor(t, 5*time.Second, func() bool { return s.countOf(TypeAudioStart) > startsBeforeB }) {
		t.Fatalf("audio_start count = %d, want > %d (B opening must register after epoch notify)", s.countOf(TypeAudioStart), startsBeforeB)
	}
	if got := s.countOf(TypeTeacherSentence); got <= sentsBeforeB {
		t.Fatalf("teacher_sentence count = %d, want > %d (B greeting must enqueue after epoch notify)", got, sentsBeforeB)
	}
}

// TestCheckResumeReinitSpeaksBridge (R1 review P2): repeating session_init into
// the same checking row on one connection bumps playback epoch on replace;
// the resume bridge must still speak under the post-install epoch.
func TestCheckResumeReinitSpeaksBridge(t *testing.T) {
	words := newFakeWords(ttCards("苹果", "香蕉"))
	exec := newFakeExecutor(ttLessonID, ttCards("苹果", "香蕉"))
	tm, s := ttInitCheckingRow(t, exec, words)
	if !waitFor(t, 5*time.Second, func() bool { return s.countOf(TypeTeacherSentence) >= 1 }) {
		t.Fatal("first check resume bridge never spoke")
	}
	tm.mu.Lock()
	epoch := tm.epoch
	tm.mu.Unlock()
	ttAckLast(t, tm, s, epoch)
	bridgeBefore := s.countOf(TypeTeacherSentence)

	ttInit(t, tm, s)
	if !waitFor(t, 5*time.Second, func() bool { return s.countOf(TypeCheckStarted) >= 2 }) {
		t.Fatal("second check_started never arrived on re-init")
	}
	if !waitFor(t, 5*time.Second, func() bool { return s.countOf(TypeTeacherSentence) > bridgeBefore }) {
		t.Fatalf("teacher_sentence count = %d, want > %d (resume bridge must run after classroom replace epoch)", s.countOf(TypeTeacherSentence), bridgeBefore)
	}
}

// TestSameLessonReinitDuringInflightSaveRefused (issue #69 rethink3 S4): a
// same-lessonID re-init that arrives while THIS connection's accepted submit
// for the row is still running must not build a classroom on the pre-save row
// state — the honest answer is a RECOVERABLE init error, nothing installed,
// no blind fallback; the save itself keeps running and lands normally (its
// classroom is still the live scope), and a retry after it settles reads the
// row's true post-save state (the completed short-circuit).
func TestSameLessonReinitDuringInflightSaveRefused(t *testing.T) {
	words := newFakeWords(ttCards("苹果", "香蕉"))
	exec := newFakeExecutor(ttLessonID, ttCards("苹果", "香蕉"))
	tm, s := ttInitCheckingRow(t, exec, words)
	tm.taskTrack = &serverTaskTracker{} // S1: whole-task settle for the waits below

	hold := newHoldFirstCompleteCheck(exec)
	tm.exec = hold

	reclaimAndFail := func(msg string) {
		hold.openUp()
		if !waitFormalSubmitSettled(t, tm, hold, 5*time.Second) {
			t.Fatalf("%s (reclaim: CompleteCheck calls=%d returned=%d never settled)",
				msg, hold.callCount(), hold.returnedCount())
		}
		t.Fatal(msg)
	}

	// The real submit entry admits the save; the latch write parks inside the
	// real executor hold (the row's state is moving by construction).
	tm.HandleControl(s, mustJSON(t, CheckSubmitMsg{Type: TypeCheckSubmit, RequestID: "busy-reinit-submit"}))
	select {
	case <-hold.entered:
	case <-time.After(3 * time.Second):
		reclaimAndFail("the submit never reached CompleteCheck")
	}

	readyBefore := s.countOf(TypeSessionReady)
	startedBefore := s.countOf(TypeCheckStarted)

	// The same-lessonID re-init (the same production session_init entry the
	// connection's read loop would dispatch) is refused with a recoverable
	// error while that admission is live.
	ttInit(t, tm, s)

	errCount, recoverable := 0, false
	s.mu.Lock()
	sentControls := append([][]byte(nil), s.controls...)
	s.mu.Unlock()
	for _, raw := range sentControls {
		var probe struct {
			Type        string `json:"type"`
			Code        string `json:"code"`
			Recoverable bool   `json:"recoverable"`
		}
		if json.Unmarshal(raw, &probe) == nil && probe.Type == TypeError &&
			probe.Code == ErrCodeLessonUnavailable {
			errCount++
			recoverable = probe.Recoverable
		}
	}
	if errCount != 1 || !recoverable {
		reclaimAndFail(fmt.Sprintf("in-flight-save reinit answered %d lesson errors (recoverable=%v), want exactly 1 recoverable", errCount, recoverable))
	}
	if got := s.countOf(TypeSessionReady); got != readyBefore {
		reclaimAndFail(fmt.Sprintf("session_ready after the refused reinit = %d, want %d (no classroom on stale row state)", got, readyBefore))
	}
	if got := s.countOf(TypeCheckStarted); got != startedBefore {
		reclaimAndFail(fmt.Sprintf("check_started after the refused reinit = %d, want %d (no panel re-open)", got, startedBefore))
	}
	tm.mu.Lock()
	scopeGen := tm.activeScope.gen
	tm.mu.Unlock()

	// The save keeps running and lands on its OWN classroom (still the live
	// scope): the writeback applies and the natural ending follows.
	hold.openUp()
	if !waitFormalSubmitSettled(t, tm, hold, 5*time.Second) {
		reclaimAndFail("the accepted submit never finished after the refusal")
	}
	if !waitFor(t, 5*time.Second, func() bool { return s.countOf(TypeReviewResult) >= 1 }) {
		reclaimAndFail("the submit's review_result never arrived — the refusal must not stall the save's own presentation")
	}
	tm.mu.Lock()
	gotVer, gotChecking := tm.tutor.version, tm.tutor.checking
	stillScope := tm.activeScope != nil && tm.activeScope.gen == scopeGen
	tm.mu.Unlock()
	if !stillScope {
		reclaimAndFail("the refusal replaced the live classroom's scope")
	}
	exec.mu.Lock()
	rowVer, rowStage := exec.lesson.Version, exec.lesson.Stage
	exec.mu.Unlock()
	if gotVer != rowVer || gotChecking {
		t.Fatalf("tutor version/checking = %d/%v, want %d/false (the live submit's writeback stands)", gotVer, gotChecking, rowVer)
	}
	if rowStage != lesson.StageSubmitted {
		t.Fatalf("row stage = %q, want submitted (the accepted save completed)", rowStage)
	}

	// A retry after the save settles reads the row's true state: the
	// completed short-circuit, not a classroom built on checking-era state.
	exec.mu.Lock()
	exec.lesson.CurrentIndex = len(exec.lesson.Cards)
	exec.mu.Unlock()
	ttInit(t, tm, s)
	if !waitFor(t, 5*time.Second, func() bool { return s.countOf(TypeSessionEnded) >= 1 }) {
		t.Fatal("the post-save retry never reached the completed short-circuit")
	}
	if got := s.countOf(TypeSessionReady); got != readyBefore {
		t.Fatalf("session_ready after the post-save retry = %d, want %d (completed rows short-circuit without a new ready)", got, readyBefore)
	}
}
