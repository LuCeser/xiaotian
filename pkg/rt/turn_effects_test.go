package rt

import (
	"encoding/json"
	"sync"
	"testing"
	"time"
)

// closeAtControlSender closes the real manager immediately before the
// selected control message is queued. If the caller holds tm.mu, closing has
// to wait; if it does not, session_ended wins the ordering race.
type closeAtControlSender struct {
	*captureSender
	tm        *TurnManager
	target    string
	closeDone chan struct{}
	once      sync.Once
}

func (s *closeAtControlSender) SendControl(v any) error {
	b, _ := json.Marshal(v)
	var message struct {
		Type string `json:"type"`
	}
	_ = json.Unmarshal(b, &message)
	if message.Type == s.target {
		s.once.Do(func() {
			closeSession := func() {
				s.tm.handleEndSession(s)
				close(s.closeDone)
			}
			if s.tm.mu.TryLock() {
				s.tm.mu.Unlock()
				closeSession()
			} else {
				go closeSession()
			}
		})
	}
	return s.captureSender.SendControl(v)
}

func TestTurnEffects(t *testing.T) {
	t.Run("current turn applies state and control together", func(t *testing.T) {
		tm := NewTurnManager(nil, nil, nil, nil, TurnManagerConfig{}, nil, nil)
		tm.epoch = 7
		s := &captureSender{}

		applied := tm.applyTurnAt(7, func() error {
			tm.state = StateProcessing
			return s.SendControl(StateMsg{Type: TypeState, Value: StateProcessing})
		})

		if !applied || tm.state != StateProcessing || s.countOf(TypeState) != 1 {
			t.Fatalf("applied=%v state=%q state messages=%d", applied, tm.state, s.countOf(TypeState))
		}
	})

	for _, tc := range []struct {
		name   string
		epoch  uint64
		closed bool
	}{
		{name: "stale turn is rejected", epoch: 8},
		{name: "closed session is rejected", epoch: 7, closed: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tm := NewTurnManager(nil, nil, nil, nil, TurnManagerConfig{}, nil, nil)
			tm.epoch, tm.closed = tc.epoch, tc.closed
			s := &captureSender{}

			applied := tm.applyTurnAt(7, func() error {
				tm.state = StateProcessing
				return s.SendControl(StateMsg{Type: TypeState, Value: StateProcessing})
			})

			if applied || tm.state != "" || s.countOf(TypeState) != 0 {
				t.Fatalf("applied=%v state=%q state messages=%d", applied, tm.state, s.countOf(TypeState))
			}
		})
	}

	t.Run("close during enqueue follows the accepted effect", func(t *testing.T) {
		tm := NewTurnManager(nil, nil, nil, nil, TurnManagerConfig{}, nil, nil)
		tm.epoch = 7
		s := &closeAtControlSender{
			captureSender: &captureSender{}, tm: tm, target: TypeState, closeDone: make(chan struct{}),
		}

		if !tm.applyTurnAt(7, func() error {
			tm.state = StateProcessing
			return s.SendControl(StateMsg{Type: TypeState, Value: StateProcessing})
		}) {
			t.Fatal("current effect was rejected")
		}
		select {
		case <-s.closeDone:
		case <-time.After(time.Second):
			t.Fatal("close did not finish")
		}

		s.captureSender.mu.Lock()
		defer s.captureSender.mu.Unlock()
		if len(s.controls) != 2 || string(s.controls[0]) == "" {
			t.Fatalf("controls = %q, want state then session_ended", s.controls)
		}
		var first, second struct {
			Type string `json:"type"`
		}
		_ = json.Unmarshal(s.controls[0], &first)
		_ = json.Unmarshal(s.controls[1], &second)
		if first.Type != TypeState || second.Type != TypeSessionEnded {
			t.Fatalf("control order = [%s %s], want [%s %s]", first.Type, second.Type, TypeState, TypeSessionEnded)
		}
	})
}
