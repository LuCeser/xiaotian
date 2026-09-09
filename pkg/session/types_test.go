package session_test

import (
	"errors"
	"testing"

	"github.com/xiaotian/home-tutor-agent/pkg/session"
)

func TestTypes(t *testing.T) {
	t.Run("EventType_constants_distinct", func(t *testing.T) {
		seen := map[session.EventType]bool{}
		for _, e := range []session.EventType{
			session.EventControlFrame,
			session.EventWordComplete,
			session.EventTimeout,
			session.EventCancel,
			session.EventParentReview,
		} {
			if seen[e] {
				t.Fatalf("duplicate EventType value 0x%02x", uint8(e))
			}
			seen[e] = true
		}
	})

	t.Run("EventType_String", func(t *testing.T) {
		cases := map[session.EventType]string{
			session.EventControlFrame: "control_frame",
			session.EventWordComplete: "word_complete",
			session.EventTimeout:      "timeout",
			session.EventCancel:       "cancel",
			session.EventParentReview: "parent_review",
		}
		for ev, want := range cases {
			if got := ev.String(); got != want {
				t.Errorf("EventType(0x%02x).String() = %q, want %q", uint8(ev), got, want)
			}
		}
	})

	t.Run("EventType_unknown", func(t *testing.T) {
		if got, want := session.EventType(0xFF).String(), "unknown(0xff)"; got != want {
			t.Errorf("unknown EventType String = %q, want %q", got, want)
		}
	})

	t.Run("sentinel_errors_is_detectable", func(t *testing.T) {
		sentinels := []error{
			session.ErrInvalidTransition,
			session.ErrNoDueWords,
			session.ErrSessionClosed,
			session.ErrManagerAlreadyRunning,
		}
		for _, e := range sentinels {
			if e == nil {
				t.Fatal("sentinel error is nil")
			}
			// errors.Is(samePointer, samePointer) is always true; this just
			// exercises the Is plumbing and guards against accidental nil/wrap.
			if !errors.Is(e, e) {
				t.Fatalf("sentinel %v not errors.Is-detectable", e)
			}
		}
		// Sentinels must be mutually distinct pointers.
		for i := 0; i < len(sentinels); i++ {
			for j := i + 1; j < len(sentinels); j++ {
				if errors.Is(sentinels[i], sentinels[j]) {
					t.Fatalf("distinct sentinels collide: %v == %v", sentinels[i], sentinels[j])
				}
			}
		}
	})
}
