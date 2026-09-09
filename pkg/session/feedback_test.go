package session_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/xiaotian/home-tutor-agent/pkg/fsrs"
	"github.com/xiaotian/home-tutor-agent/pkg/infra"
	"github.com/xiaotian/home-tutor-agent/pkg/session"
)

// bootFeedbackSession builds a started session wired with the fakes needed to
// observe SpeakFeedback (generator + streamer). The event loop is live but
// receives no events.
func bootFeedbackSession(t *testing.T, gen *fakeTTSGenerator, streamer *fakeStreamer) *session.Session {
	t.Helper()
	s, err := session.NewSession(
		context.Background(),
		nil, // client — fakes tolerate nil Senders
		"conn-fb",
		&fakeFSRSService{dueCards: threeCards()},
		gen,
		streamer,
		nil, // repo
		nil, // pool
		nil, // hub
		infra.NewNoOpLogger(),
		testCfg(),
		fsrs.SelectOpts{Limit: 3},
	)
	if err != nil {
		t.Fatalf("NewSession() error: %v", err)
	}
	s.Start(context.Background())
	t.Cleanup(func() {
		select { // let the loop exit before the test returns
		case <-s.Done():
		case <-time.After(2 * time.Second):
		}
	})
	return s
}

// TestSpeakFeedback_GeneratesAndStreams: the encouragement sentence is
// synthesized via GenerateText (text + language verbatim) and then streamed
// under the session's own ID with the text as the word key.
func TestSpeakFeedback_GeneratesAndStreams(t *testing.T) {
	gen := &fakeTTSGenerator{}
	streamer := &fakeStreamer{}
	s := bootFeedbackSession(t, gen, streamer)

	const text = "太棒了！3 个词全部正确！"
	s.SpeakFeedback(text, "chinese")

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if gen.TextCalls() == 1 && streamer.StreamCalls() == 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got := gen.TextCalls(); got != 1 {
		t.Fatalf("GenerateText calls = %d, want 1", got)
	}
	if gotText, gotLang := gen.LastText(); gotText != text || gotLang != "chinese" {
		t.Errorf("GenerateText(%q,%q), want (%q,chinese)", gotText, gotLang, text)
	}
	if got := streamer.LastWordID(); got != text {
		t.Errorf("StreamWord wordID = %q, want the feedback text itself", got)
	}
	if got := streamer.LastSessionID(); got != s.ID() {
		t.Errorf("StreamWord sessionID = %q, want %q", got, s.ID())
	}
}

// TestSpeakFeedback_GenerationFailureSkipsStream: when synthesis fails the
// stream call never happens (nothing is cached to stream) — spoken feedback
// is best-effort, the failure is logged not surfaced.
func TestSpeakFeedback_GenerationFailureSkipsStream(t *testing.T) {
	gen := &fakeTTSGenerator{textErr: errors.New("tts down")}
	streamer := &fakeStreamer{}
	s := bootFeedbackSession(t, gen, streamer)

	s.SpeakFeedback("加油！", "chinese")

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && gen.TextCalls() == 0 {
		time.Sleep(10 * time.Millisecond)
	}
	if gen.TextCalls() != 1 {
		t.Fatalf("GenerateText calls = %d, want 1", gen.TextCalls())
	}
	// Give a would-be stream call time to (wrongly) fire, then assert none.
	time.Sleep(100 * time.Millisecond)
	if got := streamer.StreamCalls(); got != 0 {
		t.Errorf("StreamWord calls after generation failure = %d, want 0", got)
	}
}

// TestSpeakFeedback_NoGenerator: nil gen (or streamer) is a silent no-op —
// the disabled-mode wiring must not panic.
func TestSpeakFeedback_NoGenerator(t *testing.T) {
	s, err := session.NewSession(
		context.Background(), nil, "conn-fb2",
		&fakeFSRSService{dueCards: threeCards()},
		&fakeTTSGenerator{}, nil,
		nil, nil, nil, infra.NewNoOpLogger(), testCfg(),
		fsrs.SelectOpts{Limit: 3},
	)
	if err != nil {
		t.Fatalf("NewSession() error: %v", err)
	}
	s.Start(context.Background())
	select {
	case <-s.Done():
	case <-time.After(2 * time.Second):
	}

	s.SpeakFeedback("不应该响起", "chinese") // must not panic on the exited session
}
