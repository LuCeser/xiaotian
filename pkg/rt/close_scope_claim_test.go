// close_scope_claim_test.go — issue #69 rethink3 S3: classroom-task terminal
// closes carry scope identity into the unified close claim; a stale completed
// replay must not end the replacement classroom, while connection goodbye
// still closes whatever is live.
//
// Review P2-5: every failure path follows the one order — record the
// observation, release EVERY control point, wait the original task AND the
// whole-task observation, and only then report. No assertion Fatals while a
// gate still holds the original task.
package rt

import (
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xiaotian/home-tutor-agent/pkg/lesson"
)

// ttCompletedRowFixture is a finished dictation row for the short-circuit
// replay path (session_ready then scoped lesson_complete close).
func ttCompletedRowFixture(t *testing.T) (*TurnManager, *captureSender, *fakeExecutor, *fakeWordSource) {
	t.Helper()
	cards := ttCards("苹果", "香蕉", "橘子")
	exec := newFakeExecutor(ttLessonID, cards)
	exec.setRow(len(cards), len(cards)+1)
	words := newFakeWords(cards)
	tm := NewTurnManager(newFakeASR(), newFakeLLM(), newFakeTTS(1, 320), exec, ttConfig(words), nil, nil)
	tm.taskTrack = &serverTaskTracker{}
	return tm, &captureSender{}, exec, words
}

// ttStaleCloseReclaim is the two tests' shared §5.1 failure egress: release
// the parked completed-close gate (exactly once), wait the original init task
// and the whole-task observation, and only then report.
type ttStaleCloseReclaim struct {
	t     *testing.T
	tm    *TurnManager
	aDone *int32
	gate  chan struct{}
	once  sync.Once
}

func (r *ttStaleCloseReclaim) release() {
	r.once.Do(func() { close(r.gate) })
}

func (r *ttStaleCloseReclaim) fail(msg string) {
	r.release()
	waitFor(r.t, 5*time.Second, func() bool { return atomic.LoadInt32(r.aDone) == 1 })
	ttPoll(5*time.Second, r.tm.serverTasksSettled)
	r.t.Fatal(msg)
}

// TestStaleCompletedCloseCannotEndReplacementClassroom is the S3 acceptance:
// A's completed replay pauses before its scoped close; B installs on the
// same connection and finishes its natural opening; A's resume must not
// close B or terminate the connection — B must still answer control frames.
func TestStaleCompletedCloseCannotEndReplacementClassroom(t *testing.T) {
	tm, s, exec, words := ttCompletedRowFixture(t)
	var aDone int32
	reclaim := &ttStaleCloseReclaim{t: t, tm: tm, aDone: &aDone, gate: make(chan struct{})}
	tm.completedCloseInterleave = func() { <-reclaim.gate }

	go func() {
		ttInit(t, tm, s)
		atomic.StoreInt32(&aDone, 1)
	}()
	if !waitFor(t, 2*time.Second, func() bool { return s.countOf(TypeSessionReady) == 1 }) {
		reclaim.fail("A's completed replay never shipped session_ready")
	}
	// Observation recorded while A's close is still parked.
	endedWhileParked := s.countOf(TypeSessionEnded)

	// B replaces A with an in-progress dictation row and finishes opening.
	bCards := ttCards("词1", "词2", "词3")
	words.setDue(lesson.CategoryChinese, bCards)
	exec.mu.Lock()
	exec.lesson = lesson.Lesson{
		LessonID: ttLessonID, Stage: "", Cards: bCards,
		Words: cardWords(bCards), CurrentIndex: 0, Version: 1, Category: lesson.CategoryChinese,
	}
	exec.mu.Unlock()
	ttInit(t, tm, s)
	if got := s.countOf(TypeSessionReady); got != 2 {
		reclaim.fail(fmt.Sprintf("session_ready count = %d, want 2 (A then B)", got))
	}
	if !waitFor(t, 5*time.Second, func() bool { return s.countOf(TypeTeacherSentence) >= 3 }) {
		reclaim.fail("B's opening never spoke before A's stale close resumed")
	}
	tm.mu.Lock()
	bEpoch := tm.epoch
	tm.mu.Unlock()
	ttAckLast(t, tm, s, bEpoch)
	if !waitFor(t, 5*time.Second, tm.serverTasksSettled) {
		reclaim.fail("B's opening tasks never settled before A's stale close resumed")
	}
	// Observation recorded with B live and every task settled.
	tm.mu.Lock()
	bGen := uint64(0)
	if tm.activeScope != nil {
		bGen = tm.activeScope.gen
	}
	tm.mu.Unlock()

	reclaim.release()
	if !waitFor(t, 2*time.Second, func() bool { return atomic.LoadInt32(&aDone) == 1 }) {
		t.Fatal("A's completed init never finished after the stale close ran")
	}
	if !ttPoll(5*time.Second, tm.serverTasksSettled) {
		t.Fatal("server tasks never settled after the stale close ran")
	}
	if endedWhileParked != 0 {
		t.Fatalf("session_ended before B = %d, want 0 — the close must stay parked", endedWhileParked)
	}
	if got := s.countOf(TypeSessionEnded); got != 0 {
		t.Fatalf("session_ended after A's stale close = %d, want 0 — the superseded scope must not close B's live classroom", got)
	}
	tm.mu.Lock()
	closed := tm.closed
	liveGen := uint64(0)
	if tm.activeScope != nil {
		liveGen = tm.activeScope.gen
	}
	tm.mu.Unlock()
	if closed {
		t.Fatal("the stale scoped close marked the session closed — B's classroom was ended early")
	}
	if bGen != 2 || liveGen != 2 {
		t.Fatalf("live classroom gen = %d (B observed %d), want 2 (B owns the live scope before and after the stale close)", liveGen, bGen)
	}
	s.mu.Lock()
	terminated := s.terminated
	s.mu.Unlock()
	if terminated {
		t.Fatal("the stale scoped close terminated the transport — the connection ended before B could continue")
	}

	tm.HandleControl(s, mustJSON(t, PingMsg{Type: TypePing, TClientMs: 1}))
	if pongs := s.controlsOf(t, TypePong); len(pongs) != 1 {
		t.Fatalf("pong after stale close = %d, want 1 — B's live classroom must keep handling messages", len(pongs))
	}
}

// TestClientGoodbyeStillClosesLiveClassroom pins connection-initiated close
// staying scope-free: after B owns the live classroom, handleEndSession must
// still claim once and ship the single client-end goodbye.
func TestClientGoodbyeStillClosesLiveClassroom(t *testing.T) {
	tm, s, exec, words := ttCompletedRowFixture(t)
	var aDone int32
	reclaim := &ttStaleCloseReclaim{t: t, tm: tm, aDone: &aDone, gate: make(chan struct{})}
	tm.completedCloseInterleave = func() { <-reclaim.gate }

	go func() {
		ttInit(t, tm, s)
		atomic.StoreInt32(&aDone, 1)
	}()
	if !waitFor(t, 2*time.Second, func() bool { return s.countOf(TypeSessionReady) == 1 }) {
		reclaim.fail("completed replay never shipped session_ready")
	}
	bCards := ttCards("词1", "词2")
	words.setDue(lesson.CategoryChinese, bCards)
	exec.mu.Lock()
	exec.lesson = lesson.Lesson{
		LessonID: ttLessonID, Stage: "", Cards: bCards,
		Words: cardWords(bCards), CurrentIndex: 0, Version: 1, Category: lesson.CategoryChinese,
	}
	exec.mu.Unlock()
	ttInit(t, tm, s)
	if !waitFor(t, 5*time.Second, func() bool { return s.countOf(TypeTeacherSentence) >= 3 }) {
		reclaim.fail("B's opening never spoke before the goodbye")
	}
	tm.mu.Lock()
	bEpoch := tm.epoch
	tm.mu.Unlock()
	ttAckLast(t, tm, s, bEpoch)
	reclaim.release()
	if !waitFor(t, 2*time.Second, tm.serverTasksSettled) {
		t.Fatal("server tasks never settled before client goodbye")
	}

	tm.handleEndSession(s)
	if got := s.countOf(TypeSessionEnded); got != 1 {
		t.Fatalf("session_ended after client goodbye = %d, want exactly 1", got)
	}
	tm.mu.Lock()
	closed := tm.closed
	tm.mu.Unlock()
	if !closed {
		t.Fatal("client goodbye left the session open")
	}
	s.mu.Lock()
	terminated := s.terminated
	s.mu.Unlock()
	if !terminated {
		t.Fatal("client goodbye never terminated the transport")
	}
}
