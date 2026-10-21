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
)

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
