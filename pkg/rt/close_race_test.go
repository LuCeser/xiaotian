// close_race_test.go — J15 narrow-timing closes (e2e-test-plan step 14, PR
// #60 review @f94a75e): an end_session landing in the micro-windows between
// the classroom-init protections — between the liveness read and the row
// creation, between the announcement's entry gate and its first
// registration, between the starter's closed check and its session_ready
// send — must end the story: nothing on the wire after session_ended, no
// store write behind it. Every interleave is pinned by a hook or a TryLock
// probe, never by a sleep.
package rt

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/xiaotian/home-tutor-agent/pkg/lesson"
)

// ensureHookExec wraps the fake executor and fires a hook at the top of
// EnsureLessonCards — the row-creation IO point, where the 建行前 window
// lives. A wrapper (not a struct field) per f94a75e: the fakes' field order
// stays fieldalignment-clean.
type ensureHookExec struct {
	*fakeExecutor
	onEnsure func()
}

// EnsureLessonCards implements LessonExecutor (the hook rides ahead of the
// delegate).
func (e *ensureHookExec) EnsureLessonCards(ctx context.Context, lessonID string, cards []lesson.Word, category string) (lesson.Lesson, error) {
	if e.onEnsure != nil {
		e.onEnsure()
	}
	return e.fakeExecutor.EnsureLessonCards(ctx, lessonID, cards, category)
}

// TestEndSessionBeforeRowCreationWritesNothing — the 建行前 window (J15:
// 结束后不得重新建课、不得写入): the picked classroom's init has passed its
// liveness read, and an end_session completing INSIDE the row-creation call
// must still kill the write. The probe fires the end only when tm.mu is
// free at that moment (the subjectTakeInterleave pattern): an
// implementation that checks closed and writes in ONE lock hold is
// unreachable — the end cannot land inside, the classroom opens normally,
// and the else-branch settles it; a split implementation gets raced
// deterministically and turns this red.
func TestEndSessionBeforeRowCreationWritesNothing(t *testing.T) {
	words := newFakeWords()
	words.setPeek(lesson.CategoryEnglish, ttCards("en1", "en2", "en3"))
	words.setPeek(lesson.CategoryChinese, ttCards("词1", "词2", "词3"))
	words.setDue(lesson.CategoryChinese, ttCards("词1", "词2", "词3"))
	exec := &ensureHookExec{fakeExecutor: newFakeExecutorNoRow(ttLessonID)}
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

	endFired := false
	exec.onEnsure = func() {
		if tm.mu.TryLock() {
			tm.mu.Unlock()
			tm.handleEndSession(s)
			endFired = true
		}
	}
	tm.finalizeSubjectAsk(s, m, oldEpoch, lesson.CategoryChinese, "voice")

	if endFired {
		if ids := exec.ensureIDCalls(); len(ids) != 0 {
			t.Errorf("EnsureLessonCards ids = %v, want none (a closed session writes no row)", ids)
		}
		if ready := s.controlsOf(t, TypeSessionReady); len(ready) != 0 {
			t.Errorf("session_ready after the mid-init end_session = %d, want 0", len(ready))
		}
		if got := s.countOf(TypeTeacherSentence); got != sentences {
			t.Errorf("teacher sentences after the mid-init end_session = %d, want still %d (no greeting)", got, sentences)
		}
		if ended := s.controlsOf(t, TypeSessionEnded); len(ended) != 1 {
			t.Fatalf("session_ended count = %d, want exactly the end_session's own 1", len(ended))
		}
		if order := s.typeOrder(); order[len(order)-1] != TypeSessionEnded {
			t.Errorf("last wire message = %q, want session_ended (nothing may follow it)", order[len(order)-1])
		}
		return
	}
	// The window is closed under the lock: the end cannot land inside it, so
	// the classroom must have opened normally — row, ready, greeting.
	if ids := exec.ensureIDCalls(); len(ids) != 1 {
		t.Fatalf("EnsureLessonCards ids = %v, want the one healthy row", ids)
	}
	if ready := s.controlsOf(t, TypeSessionReady); len(ready) != 1 {
		t.Fatalf("session_ready count = %d, want 1 (no end landed — the classroom opens)", len(ready))
	}
	ttAwaitAskSent(t, s, sentences)
	tm.mu.Lock()
	epoch := tm.epoch
	tm.mu.Unlock()
	ttAckLast(t, tm, s, epoch)
	ttWaitClarifyOpen(t, tm)
}

// closedAtEndedSender records, at the moment session_ended ships out of
// handleEndSession, whether closed was ALREADY set: the goodbye must never
// precede its own flag (PR #60 review, J15) — every lock-disciplined sender
// relies on closed being true before the goodbye hits the wire, so its
// check-and-send hold either finishes before the close or refuses after it.
type closedAtEndedSender struct {
	*captureSender
	tm       *TurnManager
	closed   bool
	observed bool
}

// SendControl implements Sender.
func (c *closedAtEndedSender) SendControl(v any) error {
	if !c.observed {
		var probe struct {
			Type string `json:"type"`
		}
		if b, err := json.Marshal(v); err == nil && json.Unmarshal(b, &probe) == nil && probe.Type == TypeSessionEnded {
			c.tm.mu.Lock()
			c.closed = c.tm.closed
			c.tm.mu.Unlock()
			c.observed = true
		}
	}
	return c.captureSender.SendControl(v)
}

// TestEndSessionBeforeAnnouncementRegistrationSendsNothing — the 播报注册前
// window (J15: 结束后无后续消息): the greeting has PASSED
// runAnnouncementDisplayAt's entry gate, and an end_session completing
// before speakTurn's own gates must still kill it. A close does not bump
// the epoch, so the epoch comparison alone would let a dead session's
// greeting register playback waits, drive the FSM to speaking and ship
// subtitles/audio_start behind session_ended. The announceEntryInterleave
// seam pins the window exactly: the entry gate has passed, the lock is
// down, speakTurn not yet entered.
func TestEndSessionBeforeAnnouncementRegistrationSendsNothing(t *testing.T) {
	tm := NewTurnManager(newFakeASR(), newFakeLLM(), newFakeTTS(1, 320), newFakeExecutorNoRow(ttLessonID), ttConfig(newFakeWords()), nil, nil)
	s := &closedAtEndedSender{captureSender: &captureSender{}, tm: tm}
	tm.mu.Lock()
	tm.state = StateListening // a live classroom at epoch 0
	tm.mu.Unlock()

	tm.announceEntryInterleave = func() { tm.handleEndSession(s) }
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	tm.runAnnouncementDisplayAt(s, 0, ctx, cancel, "同学们好，我们开始听写。", "同学们好，我们开始听写。", purposeGreeting, nil)

	if !s.observed {
		t.Fatal("the seam never shipped session_ended")
	}
	if !s.closed {
		t.Error("session_ended shipped before closed was set — the goodbye precedes its own flag")
	}
	if ended := s.controlsOf(t, TypeSessionEnded); len(ended) != 1 {
		t.Fatalf("session_ended count = %d, want exactly the end_session's own 1", len(ended))
	}
	if order := s.typeOrder(); order[len(order)-1] != TypeSessionEnded {
		t.Errorf("last wire message = %q, want session_ended (nothing may follow it)", order[len(order)-1])
	}
	if got := s.countOf(TypeTeacherSentence); got != 0 {
		t.Errorf("teacher sentences from the post-end greeting = %d, want 0", got)
	}
	if got := s.countOf(TypeAudioStart); got != 0 {
		t.Errorf("audio_start from the post-end greeting = %d, want 0", got)
	}
	tm.mu.Lock()
	gate := tm.playbackPending
	waits := len(tm.pendingPlayback)
	state := tm.state
	tm.mu.Unlock()
	if gate {
		t.Error("the post-end greeting raised a playback gate")
	}
	if waits != 0 {
		t.Errorf("pendingPlayback entries after the end = %d, want 0", waits)
	}
	if state == StateSpeaking {
		t.Error("the post-end greeting drove the closed FSM to speaking")
	}
}

// endRaceSender races ONE end_session against the session_ready send: the
// probe fires only when tm.mu is FREE at the moment the starter ships its
// ready message. A disciplined starter holds the lock through the closed
// check AND the send (and the close paths set closed before shipping
// session_ended), so the probe can never fire — the classroom opens
// normally. An undisciplined one gets its goodbye strictly BEFORE the ready
// message, and the wire-order assertion goes red.
type endRaceSender struct {
	*captureSender
	tm    *TurnManager
	fired bool
}

// SendControl implements Sender.
func (e *endRaceSender) SendControl(v any) error {
	if !e.fired {
		var probe struct {
			Type string `json:"type"`
		}
		if b, err := json.Marshal(v); err == nil && json.Unmarshal(b, &probe) == nil && probe.Type == TypeSessionReady {
			if e.tm.mu.TryLock() {
				e.tm.mu.Unlock()
				e.tm.handleEndSession(e)
				e.fired = true
			}
		}
	}
	return e.captureSender.SendControl(v)
}

// TestSessionReadyCannotFollowSessionEnded — Codex PR #60 review @f94a75e
// P2, starter half (J15: 结束后无后续消息): the closed check and the
// session_ready send must be ONE tm.mu hold. The end raced here is the
// read loop's own end_session — exactly the interleaving the voice-picked
// classroom's init goroutine cannot exclude by checking alone.
func TestSessionReadyCannotFollowSessionEnded(t *testing.T) {
	exec := newFakeExecutorNoRow(ttLessonID)
	tm := NewTurnManager(newFakeASR(), newFakeLLM(), newFakeTTS(1, 320), exec, ttConfig(newFakeWords(ttCards("美丽", "世界"))), nil, nil)
	s := &endRaceSender{captureSender: &captureSender{}, tm: tm}
	// The wrapper itself must be the Sender the init rides — the probe lives
	// on the session_ready send path.
	tm.HandleControl(s, mustJSON(t, SessionInitMsg{
		Type:  TypeSessionInit,
		Audio: AudioSpec{SampleRate: 16000, Format: "s16le", Channels: 1},
	}))

	if s.fired {
		order := s.typeOrder()
		firstReady, firstEnded := -1, -1
		for i, typ := range order {
			if typ == TypeSessionReady && firstReady < 0 {
				firstReady = i
			}
			if typ == TypeSessionEnded && firstEnded < 0 {
				firstEnded = i
			}
		}
		if firstEnded < firstReady {
			t.Errorf("wire order = session_ended(@%d) before session_ready(@%d) — the ready send escaped the closed check's lock hold", firstEnded, firstReady)
		}
		return
	}
	// No end could land: the classroom opened normally and must be healthy.
	if ready := s.controlsOf(t, TypeSessionReady); len(ready) != 1 {
		t.Fatalf("session_ready count = %d, want 1", len(ready))
	}
	if ended := s.controlsOf(t, TypeSessionEnded); len(ended) != 0 {
		t.Fatalf("session_ended count = %d, want 0 (the probe never fired)", len(ended))
	}
	ttAwaitSpoken(t, s.captureSender, 1)
	ttAckLast(t, tm, s.captureSender, 0)
	ttWaitClarifyOpen(t, tm)
}
