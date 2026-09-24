package session

import (
	"fmt"

	"github.com/xiaotian/home-tutor-agent/pkg/database"
)

// transition applies a user/system-driven FSM transition per DICT-04, D-03.
//
// It is a pure state-machine function: on a valid transition it mutates s.state
// (and s.wordIndex where applicable) and returns nil; on an invalid (state,
// event) pair it returns an error wrapping ErrInvalidTransition WITHOUT
// mutating any field. Callers MUST detect the sentinel via errors.Is (DICT-04,
// D-03).
//
// D-03 mandates a flat typed-enum switch — NOT a state-pattern interface and
// NOT an external FSM library (both explicitly rejected). This table encodes
// the ~15 enumerated user/system-driven transitions from RESEARCH.md §"FSM
// Transition Table". Timeout/Cancel are loop-level signals handled by the event
// loop (Plan 02), not the FSM table; they return nil here so the loop decides
// to exit without the FSM terminating it.
//
// deadline is NOT updated here — that is done by the caller (the event-loop
// transition wrapper in Plan 02). Keep this function a pure state machine.
func (s *Session) transition(ev SessionEvent) error {
	// Timeout/Cancel are loop-level signals, not FSM transitions. Return nil so
	// the event loop (Plan 02) decides to exit; transition() never terminates
	// the loop itself.
	if ev.Type == EventTimeout || ev.Type == EventCancel {
		return nil
	}

	// Protect state+wordIndex so cross-goroutine reads via State() / Deadline()
	// (sweeper, tests) are race-free against this write (D-02 single-writer with
	// mutex for -race cleanliness).
	s.mu.Lock()
	defer s.mu.Unlock()

	// Track previous state for resume-from-pause detection in onTransition.
	prevState := s.state

	switch s.state {
	case database.StateIdle:
		switch ev.Type {
		case EventControlFrame:
			if ev.ControlType == "play_word" {
				s.wordIndex = 0
				s.state = database.StateListening
				s.prevState = prevState
				return nil
			}
		}

	case database.StateListening:
		switch ev.Type {
		case EventWordComplete:
			// D-13: system-driven transition. The StreamWord-completion side
			// channel (Plan 02) pushes WordComplete; the FSM advances to
			// DICTATING.
			s.state = database.StateDictating
			s.prevState = prevState
			return nil
		case EventControlFrame:
			if ev.ControlType == "play_word" {
				// Replay the same word: stay in LISTENING (no index advance).
				return nil
			}
		}

	case database.StateDictating:
		switch ev.Type {
		case EventWordComplete:
			// 2026-09-14 UAT fix: replay-completion self-loop. A repeat/resume
			// re-stream finishes while the session is DICTATING (its stream
			// generation was already validated upstream in run()); accepting
			// the completion lets handleEvent send the word_complete frame
			// AFTER the replayed audio actually streamed. Previously this was
			// an invalid transition — replayed audio was orphaned client-side
			// and the UI sat dead until the DICTATING timeout force-advanced.
			//
			// prevState MUST be refreshed here: without it, s.prevState stays
			// PAUSED after a resume, and onTransition's resume-spawn condition
			// (DICTATING + prevState==PAUSED) re-fires on EVERY self-loop —
			// the resume replay loops forever (UAT: endless "banana").
			s.prevState = prevState
			return nil
		case EventControlFrame:
			switch ev.ControlType {
			case "next_word":
				if s.onLastWord() {
					// Last word exhausted → wait for parent review (D-12: no
					// UpdateRatings call).
					s.state = database.StateWaitingParent
					s.prevState = prevState
					return nil
				}
				s.wordIndex++
				s.state = database.StateListening
				s.prevState = prevState
				return nil
			case "pause":
				// Ticket 02 DICT-07: student pauses dictation.
				s.state = database.StatePaused
				s.prevState = prevState
				return nil
			case "repeat":
				// Ticket 02 DICT-07: replay current word audio, stay in DICTATING.
				// Set repeatPending so onTransition re-spawns StreamWord.
				s.repeatPending = true
				return nil
			case "submit_answers":
				// Ticket 05: batch submission leaving the last word's
				// DICTATING (the dictation is fully heard — an early submit
				// would grade never-dictated words, so earlier words reject).
				// Index unchanged — grading reads the whole queue, not the
				// cursor.
				if s.onLastWord() {
					s.state = database.StateSubmission
					s.prevState = prevState
					return nil
				}
			}
		}

	case database.StatePaused:
		switch ev.Type {
		case EventWordComplete:
			// 2026-09-14 UAT fix: replay-completion self-loop (repeat-from-
			// PAUSED path) — same rationale as the DICTATING self-loop above:
			// the replayed audio streamed, so the client deserves its
			// word_complete even though the session stays PAUSED. prevState
			// refresh prevents any stale prev-state condition from re-firing.
			s.prevState = prevState
			return nil
		case EventControlFrame:
			switch ev.ControlType {
			case "resume":
				// Ticket 02 DICT-07: resume from pause — return to DICTATING.
				s.state = database.StateDictating
				s.prevState = prevState
				return nil
			case "repeat":
				// Ticket 02 DICT-07: replay current word while paused.
				// Stay in PAUSED; set repeatPending for onTransition.
				s.repeatPending = true
				return nil
			case "next_word":
				// Ticket 02 DICT-07: skip word while paused.
				if s.onLastWord() {
					s.state = database.StateWaitingParent
					s.prevState = prevState
					return nil
				}
				s.wordIndex++
				s.state = database.StateListening
				s.prevState = prevState
				return nil
			}
		}

	case database.StateWaitingParent:
		switch ev.Type {
		case EventParentReview:
			s.state = database.StateFeedback
			s.prevState = prevState
			return nil
		case EventControlFrame:
			if ev.ControlType == "submit_answers" {
				// Ticket 05: batch submission — the student wrote all words on
				// paper and submits everything at once instead of the per-word
				// answer loop (which is what landed the session here with an
				// empty ratings map).
				s.state = database.StateSubmission
				s.prevState = prevState
				return nil
			}
		}

	case database.StateSubmission:
		switch ev.Type {
		case EventBatchGraded:
			// Ticket 05: grading finished (the handler already sent the
			// batch_result frame) — deliver feedback like parent_review does.
			s.state = database.StateFeedback
			s.prevState = prevState
			return nil
		}
	}

	// FEEDBACK is terminal — no transitions out (a new session is created
	// for the next round). Any event in FEEDBACK falls through to the
	// invalid-transition error below.
	return fmt.Errorf("%w: state=%s event=%v", ErrInvalidTransition, s.state, ev.Type)
}
