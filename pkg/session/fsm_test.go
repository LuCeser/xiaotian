package session

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/xiaotian/home-tutor-agent/pkg/database"
	"github.com/xiaotian/home-tutor-agent/pkg/infra"
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

// TestTransition_PauseResume: DICTATING + pause → PAUSED, PAUSED + resume → DICTATING
// (Ticket 02 DICT-07).
func TestTransition_PauseResume(t *testing.T) {
	queue := []pybridge.DueCard{{Word: "apple"}, {Word: "banana"}}

	// DICTATING + pause → PAUSED
	s := &Session{state: database.StateDictating, wordQueue: queue, wordIndex: 0}
	err := s.transition(SessionEvent{Type: EventControlFrame, ControlType: "pause"})
	if err != nil {
		t.Fatalf("transition(pause) returned error: %v", err)
	}
	if s.state != database.StatePaused {
		t.Errorf("state = %s, want PAUSED", s.state)
	}

	// PAUSED + resume → DICTATING
	err = s.transition(SessionEvent{Type: EventControlFrame, ControlType: "resume"})
	if err != nil {
		t.Fatalf("transition(resume) returned error: %v", err)
	}
	if s.state != database.StateDictating {
		t.Errorf("state = %s, want DICTATING", s.state)
	}
}

// TestTransition_Repeat: repeat in DICTATING and PAUSED sets repeatPending
// without changing state (Ticket 02 DICT-07).
func TestTransition_Repeat(t *testing.T) {
	queue := []pybridge.DueCard{{Word: "apple"}}

	// DICTATING + repeat → stays DICTATING, repeatPending=true
	s := &Session{state: database.StateDictating, wordQueue: queue, wordIndex: 0}
	err := s.transition(SessionEvent{Type: EventControlFrame, ControlType: "repeat"})
	if err != nil {
		t.Fatalf("transition(repeat) returned error: %v", err)
	}
	if s.state != database.StateDictating {
		t.Errorf("state = %s, want DICTATING (repeat should not change state)", s.state)
	}
	if !s.repeatPending {
		t.Error("repeatPending = false, want true")
	}

	// PAUSED + repeat → stays PAUSED, repeatPending=true
	s2 := &Session{state: database.StatePaused, wordQueue: queue, wordIndex: 0, repeatPending: false}
	err = s2.transition(SessionEvent{Type: EventControlFrame, ControlType: "repeat"})
	if err != nil {
		t.Fatalf("transition(repeat) returned error: %v", err)
	}
	if s2.state != database.StatePaused {
		t.Errorf("state = %s, want PAUSED (repeat should not change state)", s2.state)
	}
	if !s2.repeatPending {
		t.Error("repeatPending = false, want true")
	}
}

// TestTransition_NextWordFromPaused: PAUSED + next_word advances to next word
// (Ticket 02 DICT-07).
func TestTransition_NextWordFromPaused(t *testing.T) {
	queue := []pybridge.DueCard{{Word: "apple"}, {Word: "banana"}}

	s := &Session{state: database.StatePaused, wordQueue: queue, wordIndex: 0}
	err := s.transition(SessionEvent{Type: EventControlFrame, ControlType: "next_word"})
	if err != nil {
		t.Fatalf("transition(next_word from PAUSED) returned error: %v", err)
	}
	if s.state != database.StateListening {
		t.Errorf("state = %s, want LISTENING", s.state)
	}
	if s.wordIndex != 1 {
		t.Errorf("wordIndex = %d, want 1", s.wordIndex)
	}
}

// TestTransition_LastWordFromPaused: PAUSED + next_word on last word → WAITING_PARENT
// (Ticket 02 DICT-07).
func TestTransition_LastWordFromPaused(t *testing.T) {
	queue := []pybridge.DueCard{{Word: "only"}}

	s := &Session{state: database.StatePaused, wordQueue: queue, wordIndex: 0}
	err := s.transition(SessionEvent{Type: EventControlFrame, ControlType: "next_word"})
	if err != nil {
		t.Fatalf("transition(next_word from PAUSED last) returned error: %v", err)
	}
	if s.state != database.StateWaitingParent {
		t.Errorf("state = %s, want WAITING_PARENT", s.state)
	}
}

// TestTransition_InvalidPaused: invalid events in PAUSED state return error
// without mutating state (Ticket 02 DICT-07).
func TestTransition_InvalidPaused(t *testing.T) {
	queue := []pybridge.DueCard{{Word: "apple"}}

	tests := []struct {
		name string
		ev   SessionEvent
	}{
		{"word_complete in PAUSED", SessionEvent{Type: EventWordComplete}},
		{"play_word in PAUSED", SessionEvent{Type: EventControlFrame, ControlType: "play_word"}},
		{"parent_review in PAUSED", SessionEvent{Type: EventParentReview}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := &Session{state: database.StatePaused, wordQueue: queue, wordIndex: 0}
			err := s.transition(tt.ev)
			if err == nil {
				t.Fatal("transition() returned nil, want ErrInvalidTransition")
			}
			if !errors.Is(err, ErrInvalidTransition) {
				t.Errorf("transition() error does not wrap ErrInvalidTransition: %v", err)
			}
			if s.state != database.StatePaused {
				t.Errorf("state mutated: got %s, want PAUSED", s.state)
			}
		})
	}
}

// TestPrevState_Tracked: prevState is correctly set on state transitions
// (Ticket 02 DICT-07, resume-from-pause detection).
func TestPrevState_Tracked(t *testing.T) {
	queue := []pybridge.DueCard{{Word: "apple"}, {Word: "banana"}}

	// IDLE → LISTENING
	s := &Session{state: database.StateIdle, wordQueue: queue, wordIndex: 0}
	s.transition(SessionEvent{Type: EventControlFrame, ControlType: "play_word"})
	if s.prevState != database.StateIdle {
		t.Errorf("prevState after IDLE→LISTENING = %s, want IDLE", s.prevState)
	}

	// LISTENING → DICTATING
	s.transition(SessionEvent{Type: EventWordComplete})
	if s.prevState != database.StateListening {
		t.Errorf("prevState after LISTENING→DICTATING = %s, want LISTENING", s.prevState)
	}

	// DICTATING → PAUSED
	s.transition(SessionEvent{Type: EventControlFrame, ControlType: "pause"})
	if s.prevState != database.StateDictating {
		t.Errorf("prevState after DICTATING→PAUSED = %s, want DICTATING", s.prevState)
	}

	// PAUSED → DICTATING (resume)
	s.transition(SessionEvent{Type: EventControlFrame, ControlType: "resume"})
	if s.prevState != database.StatePaused {
		t.Errorf("prevState after PAUSED→DICTATING = %s, want PAUSED", s.prevState)
	}
}

// TestTimeoutFor_PausedStateFromConfig: the PAUSED-state timeout is sourced
// solely from cfg.SessionTimeout.PausedStateTimeout — no hardcoded 5m literal
// may survive in session.go (ticket 02 review fix #2/5 guard).
func TestTimeoutFor_PausedStateFromConfig(t *testing.T) {
	// Assertion 1: default-value contract — DefaultConfig pins 5m so
	// unconfigured deployments keep the pre-refactor behavior exactly.
	if got := infra.DefaultConfig().SessionTimeout.PausedStateTimeout; got != 5*time.Minute {
		t.Errorf("DefaultConfig().SessionTimeout.PausedStateTimeout = %v, want 5m", got)
	}

	// Assertion 2: timeoutFor(StatePaused) reads the config verbatim. 7m is
	// deliberately non-default so a hardcoded-5m regression fails here.
	cfg := infra.DefaultConfig()
	cfg.SessionTimeout.PausedStateTimeout = 7 * time.Minute
	s := &Session{cfg: cfg}
	if got := s.timeoutFor(database.StatePaused); got != 7*time.Minute {
		t.Errorf("timeoutFor(StatePaused) = %v, want 7m (must read cfg.SessionTimeout.PausedStateTimeout)", got)
	}

	// Assertion 3: Validate rejects a zero PausedStateTimeout with an error
	// wrapping ErrConfigInvalid (D-04 fail-fast, malformed env fails at boot).
	invalid := infra.DefaultConfig().SessionTimeout
	invalid.PausedStateTimeout = 0
	err := invalid.Validate()
	if err == nil {
		t.Fatal("SessionTimeoutConfig.Validate() returned nil for zero PausedStateTimeout, want ErrConfigInvalid")
	}
	if !errors.Is(err, infra.ErrConfigInvalid) {
		t.Errorf("Validate() error does not wrap ErrConfigInvalid: %v", err)
	}
	if !strings.Contains(err.Error(), "pausedStateTimeout") {
		t.Errorf("Validate() error %q does not mention pausedStateTimeout", err)
	}
}
