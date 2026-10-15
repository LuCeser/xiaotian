package session

import (
	"context"
	"testing"
	"time"

	"github.com/xiaotian/home-tutor-agent/pkg/database"
	"github.com/xiaotian/home-tutor-agent/pkg/infra"
	"github.com/xiaotian/home-tutor-agent/pkg/pybridge"
)

func TestPushDropsWhenBufferFull(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := &Session{
		id:     "s",
		events: make(chan SessionEvent, eventBufferSize),
		runCtx: ctx,
		logger: infra.NewNoOpLogger(),
	}
	for i := 0; i < eventBufferSize; i++ {
		s.Push(SessionEvent{Type: EventControlFrame, ControlType: "play_word"})
	}
	if got := s.DroppedCount(); got != 0 {
		t.Fatalf("DroppedCount = %d, want 0 after filling the buffer", got)
	}

	start := time.Now()
	s.Push(SessionEvent{Type: EventControlFrame, ControlType: "next_word"})
	if elapsed := time.Since(start); elapsed > 200*time.Millisecond {
		t.Fatalf("Push blocked for %v; a full buffer must drop", elapsed)
	}
	if got := s.DroppedCount(); got != 1 {
		t.Fatalf("DroppedCount = %d, want 1", got)
	}

	n := 0
	for {
		select {
		case ev := <-s.events:
			n++
			if ev.ControlType == "next_word" {
				t.Fatal("overflow event was queued")
			}
		default:
			if n != eventBufferSize {
				t.Fatalf("buffered events = %d, want %d", n, eventBufferSize)
			}
			return
		}
	}
}

func TestPushCancelledDoesNotBlockOrCount(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	s := &Session{
		events: make(chan SessionEvent, 1),
		runCtx: ctx,
		logger: infra.NewNoOpLogger(),
	}
	s.events <- SessionEvent{Type: EventControlFrame, ControlType: "play_word"}

	start := time.Now()
	s.Push(SessionEvent{Type: EventControlFrame, ControlType: "next_word"})
	if elapsed := time.Since(start); elapsed > 200*time.Millisecond {
		t.Fatalf("Push blocked for %v on a cancelled session", elapsed)
	}
	if got := s.DroppedCount(); got != 0 {
		t.Fatalf("DroppedCount = %d, want 0 (cancellation is not a drop)", got)
	}
}

func TestSkipTimedOutWordAdvancesAndResetsDeadline(t *testing.T) {
	s := &Session{
		state: database.StateDictating,
		wordQueue: []pybridge.DueCard{
			{Word: "apple"},
			{Word: "banana"},
		},
		deadline: time.Now().Add(-time.Second),
		logger:   infra.NewNoOpLogger(),
	}
	s.cfg.SessionTimeout.ListeningStateTimeout = 2 * time.Second

	before := time.Now()
	s.skipTimedOutWord()

	if got := s.State(); got != database.StateListening {
		t.Fatalf("state = %s, want %s", got, database.StateListening)
	}
	if got := s.CurrentWord(); got != "banana" {
		t.Fatalf("CurrentWord = %q, want banana", got)
	}
	if !s.Deadline().After(before.Add(time.Second)) {
		t.Fatalf("deadline %v was not moved at least 1s past %v", s.Deadline(), before)
	}
}

func TestSkipTimedOutWordResetsDeadlineWhenTransitionFails(t *testing.T) {
	s := &Session{
		state:    database.StateIdle, // next_word is not valid from IDLE
		deadline: time.Now().Add(-time.Second),
		logger:   infra.NewNoOpLogger(),
	}
	s.cfg.SessionTimeout.IdleStateTimeout = time.Second

	before := time.Now()
	s.skipTimedOutWord()

	if got := s.State(); got != database.StateIdle {
		t.Fatalf("state = %s, want %s", got, database.StateIdle)
	}
	if !s.Deadline().After(before.Add(500 * time.Millisecond)) {
		t.Fatalf("deadline %v stayed in the past (timer would refire immediately)", s.Deadline())
	}
}
