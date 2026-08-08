package session

import (
	"errors"
	"testing"

	"github.com/xiaotian/home-tutor-agent/pkg/database"
	"github.com/xiaotian/home-tutor-agent/pkg/pybridge"
)

// TestTransition covers every valid user/system-driven FSM transition (DICT-04,
// D-03). Table-driven, mirroring the style at session_repo_test.go.
func TestTransition(t *testing.T) {
	queue3 := []pybridge.DueCard{{Word: "apple"}, {Word: "banana"}, {Word: "cherry"}}

	tests := []struct {
		name      string
		from      database.SessionState
		queue     []pybridge.DueCard
		index     int
		ev        SessionEvent
		wantState database.SessionState
		wantIndex int
	}{
		{
			name:      "IDLE to LISTENING on play_word",
			from:      database.StateIdle,
			queue:     queue3,
			ev:        SessionEvent{Type: EventControlFrame, ControlType: "play_word"},
			wantState: database.StateListening,
			wantIndex: 0,
		},
		{
			name:      "LISTENING to DICTATING on word_complete (D-13)",
			from:      database.StateListening,
			queue:     queue3,
			index:     0,
			ev:        SessionEvent{Type: EventWordComplete},
			wantState: database.StateDictating,
			wantIndex: 0,
		},
		{
			name:      "LISTENING replay stays LISTENING",
			from:      database.StateListening,
			queue:     queue3,
			index:     1,
			ev:        SessionEvent{Type: EventControlFrame, ControlType: "play_word"},
			wantState: database.StateListening,
			wantIndex: 1,
		},
		{
			name:      "DICTATING to LISTENING on next_word (more words)",
			from:      database.StateDictating,
			queue:     queue3,
			index:     0,
			ev:        SessionEvent{Type: EventControlFrame, ControlType: "next_word"},
			wantState: database.StateListening,
			wantIndex: 1,
		},
		{
			name:      "DICTATING to LISTENING on next_word (second advance)",
			from:      database.StateDictating,
			queue:     queue3,
			index:     1,
			ev:        SessionEvent{Type: EventControlFrame, ControlType: "next_word"},
			wantState: database.StateListening,
			wantIndex: 2,
		},
		{
			name:      "WAITING_PARENT to FEEDBACK on parent_review",
			from:      database.StateWaitingParent,
			queue:     queue3,
			ev:        SessionEvent{Type: EventParentReview},
			wantState: database.StateFeedback,
			wantIndex: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := &Session{state: tt.from, wordQueue: tt.queue, wordIndex: tt.index}
			if err := s.transition(tt.ev); err != nil {
				t.Fatalf("transition() returned unexpected error: %v", err)
			}
			if s.state != tt.wantState {
				t.Errorf("state = %s, want %s", s.state, tt.wantState)
			}
			if s.wordIndex != tt.wantIndex {
				t.Errorf("wordIndex = %d, want %d", s.wordIndex, tt.wantIndex)
			}
		})
	}
}

// TestTransition_LastWord: DICTATING with wordIndex == len(queue)-1 +
// next_word transitions to WAITING_PARENT, not LISTENING (D-12).
func TestTransition_LastWord(t *testing.T) {
	queue1 := []pybridge.DueCard{{Word: "only"}}
	s := &Session{state: database.StateDictating, wordQueue: queue1, wordIndex: 0}

	err := s.transition(SessionEvent{Type: EventControlFrame, ControlType: "next_word"})
	if err != nil {
		t.Fatalf("transition() returned unexpected error: %v", err)
	}
	if s.state != database.StateWaitingParent {
		t.Errorf("state = %s, want %s (last word should go to WAITING_PARENT)", s.state, database.StateWaitingParent)
	}
}

// TestTransition_Invalid: invalid (state, event) pairs return an error wrapping
// ErrInvalidTransition WITHOUT mutating s.state or s.wordIndex.
func TestTransition_Invalid(t *testing.T) {
	queue2 := []pybridge.DueCard{{Word: "apple"}, {Word: "banana"}}

	tests := []struct {
		name  string
		from  database.SessionState
		index int
		ev    SessionEvent
	}{
		{
			name: "IDLE plus word_complete",
			from: database.StateIdle,
			ev:   SessionEvent{Type: EventWordComplete},
		},
		{
			name: "FEEDBACK plus control_frame",
			from: database.StateFeedback,
			ev:   SessionEvent{Type: EventControlFrame, ControlType: "play_word"},
		},
		{
			name:  "WAITING_PARENT plus word_complete",
			from:  database.StateWaitingParent,
			index: 0,
			ev:    SessionEvent{Type: EventWordComplete},
		},
		{
			name: "IDLE plus parent_review",
			from: database.StateIdle,
			ev:   SessionEvent{Type: EventParentReview},
		},
		{
			name: "DICTATING plus play_word (only next_word is valid here)",
			from: database.StateDictating,
			ev:   SessionEvent{Type: EventControlFrame, ControlType: "play_word"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := &Session{state: tt.from, wordQueue: queue2, wordIndex: tt.index}
			err := s.transition(tt.ev)
			if err == nil {
				t.Fatal("transition() returned nil, want ErrInvalidTransition")
			}
			if !errors.Is(err, ErrInvalidTransition) {
				t.Errorf("transition() error does not wrap ErrInvalidTransition: %v", err)
			}
			// The invalid call must NOT mutate state or wordIndex.
			if s.state != tt.from {
				t.Errorf("state mutated: got %s, want %s (invalid transition must not mutate)", s.state, tt.from)
			}
			if s.wordIndex != tt.index {
				t.Errorf("wordIndex mutated: got %d, want %d (invalid transition must not mutate)", s.wordIndex, tt.index)
			}
		})
	}
}

// TestTransition_TimeoutCancelReturnNil: timeout/cancel events return nil from
// transition() (the event loop handles them, not the FSM table).
func TestTransition_TimeoutCancelReturnNil(t *testing.T) {
	s := &Session{state: database.StateListening, wordQueue: []pybridge.DueCard{{Word: "x"}}}

	for _, evType := range []EventType{EventTimeout, EventCancel} {
		err := s.transition(SessionEvent{Type: evType})
		if err != nil {
			t.Errorf("transition(%s) returned error %v, want nil (loop-level signal)", evType, err)
		}
	}
	// State must be unchanged.
	if s.state != database.StateListening {
		t.Errorf("state changed after timeout/cancel: got %s", s.state)
	}
}
