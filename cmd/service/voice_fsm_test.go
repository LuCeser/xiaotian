package main

// Voice → real session.Manager → real FSM integration test (Ticket 03).
//
// Where voice_test.go stops at a fake sessionPusher ("the right event was
// pushed"), this file closes the remaining loop: a voice_command whose
// transcript classifies as pause must travel through the REAL Manager
// (connID lookup → Session event channel) into the REAL FSM, transition
// DICTATING → PAUSED, and land as the observable "paused" control frame on
// the client socket.
//
// The AI seams stay faked (transcriber returns a fixed transcript; the
// classifier runs rules-only) and TTS is faked at the NewManager seams
// (TTSSessionGenerator / SessionStreamer interfaces, D-05) — everything
// between them is production code: ttsHandlers dispatch, voiceService
// settle/process/dispatch, Manager.Push, the session event loop, and the
// FSM table.

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"go.uber.org/zap"

	"github.com/xiaotian/home-tutor-agent/pkg/database"
	"github.com/xiaotian/home-tutor-agent/pkg/fsrs"
	"github.com/xiaotian/home-tutor-agent/pkg/infra"
	"github.com/xiaotian/home-tutor-agent/pkg/pybridge"
	"github.com/xiaotian/home-tutor-agent/pkg/session"
	"github.com/xiaotian/home-tutor-agent/pkg/tts"
	"github.com/xiaotian/home-tutor-agent/pkg/voice"
	"github.com/xiaotian/home-tutor-agent/pkg/ws"
)

// fsmFSRS feeds two due words and succeeds at everything else.
type fsmFSRS struct{}

func (f *fsmFSRS) SelectDueCards(_ context.Context, _ fsrs.SelectOpts) ([]pybridge.DueCard, error) {
	return []pybridge.DueCard{{ID: 1, Word: "苹果"}, {ID: 2, Word: "香蕉"}}, nil
}

func (f *fsmFSRS) UpdateRatings(_ context.Context, _ map[string]string, _ string) (pybridge.ReviewResult, error) {
	return pybridge.ReviewResult{}, nil
}

func (f *fsmFSRS) UnlockCards(_ context.Context, _ fsrs.UnlockOpts) (int, error) {
	return 0, nil
}

// fsmGen fakes TTSSessionGenerator: every word "generates" instantly.
type fsmGen struct{}

func (g *fsmGen) GenerateForSession(_ context.Context, _ string, words []string) (map[string]string, error) {
	m := make(map[string]string, len(words))
	for _, w := range words {
		m[w] = "/fake/" + w + ".pcm"
	}
	return m, nil
}

// GenerateForSessionWithSource satisfies the widened DICT-03 seam: the
// default fake reports the Volcengine source (paired "word. word." tracks →
// the word_complete audio_plays hint is 1).
func (g *fsmGen) GenerateForSessionWithSource(_ context.Context, _ string, words []string) (map[string]string, tts.AudioSource, error) {
	paths, err := g.GenerateForSession(context.Background(), "", words)
	return paths, tts.SourceVolcengine, err
}

// mimoGen reports the Mimo word source so tests can assert the word_complete
// audio_plays hint of 2 (single-pronunciation audio → the frontend
// schedules the double play).
type mimoGen struct {
	fsmGen
}

func (g *mimoGen) GenerateForSessionWithSource(_ context.Context, _ string, words []string) (map[string]string, tts.AudioSource, error) {
	paths, err := g.GenerateForSession(context.Background(), "", words)
	return paths, tts.SourceMimo, err
}

// GenerateText satisfies the Ticket-06 feedback seam (TTSSessionGenerator);
// tests that don't exercise spoken feedback get a canned success.
func (g *fsmGen) GenerateText(_ context.Context, _, _, _ string) (string, error) {
	return "/fake/feedback.pcm", nil
}

// recordingGen extends fsmGen with capture of the Ticket-06 feedback seam —
// tests assert the encouragement sentence reached GenerateText.
type recordingGen struct {
	fsmGen

	mu        sync.Mutex
	textCalls int
	lastText  string
}

// GenerateText overrides the canned fake to record what the handler asked to
// speak.
func (g *recordingGen) GenerateText(_ context.Context, _, text, _ string) (string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.textCalls++
	g.lastText = text
	return "/fake/feedback.pcm", nil
}

func (g *recordingGen) feedback() (int, string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.textCalls, g.lastText
}

func (g *fsmGen) CleanupSession(_ context.Context, _ string) error { return nil }

// fsmStreamer fakes SessionStreamer: StreamWord succeeds instantly, which
// makes the session push EventWordComplete and drives LISTENING→DICTATING
// (D-13) without any audio actually flowing.
type fsmStreamer struct{}

func (s *fsmStreamer) StreamWord(_ context.Context, _ tts.Sender, _ string, _ string, _ uint32) error {
	return nil
}

func (s *fsmStreamer) ClearSeqCounter(_ string) {}

// fsmRepo fakes SessionRepository (in-memory, always succeeds).
type fsmRepo struct{}

func (r *fsmRepo) Create(_ context.Context, _ database.Session) (int64, error) {
	return 1, nil
}

func (r *fsmRepo) UpdateState(_ context.Context, _ int64, _ database.SessionState) error {
	return nil
}

func (r *fsmRepo) AppendEvent(_ context.Context, _ database.SessionEvent) error { return nil }
func (r *fsmRepo) GetByID(_ context.Context, _ int64) (database.Session, error) {
	return database.Session{}, database.ErrSessionNotFound
}
func (r *fsmRepo) ListRecentResults(_ context.Context, _ int) ([]database.SessionResult, error) {
	return nil, nil
}

// readFrameUntil reads server frames until one of msgType arrives whose
// payload contains substr, returning that payload. Fails the test on
// timeout. Used to skip interleaved frames (voice_result, progress) while
// waiting for a specific lifecycle frame.
func readFrameUntil(t *testing.T, conn *websocket.Conn, msgType ws.MessageType, substr string) string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		header, payload := readFrame(t, conn)
		if header.MsgType == msgType && strings.Contains(string(payload), substr) {
			return string(payload)
		}
	}
	t.Fatalf("timed out waiting for %d frame containing %q", msgType, substr)
	return ""
}

// readControlUntil reads server frames until a CONTROL frame whose payload
// contains substr arrives (readFrameUntil pinned to control frames).
func readControlUntil(t *testing.T, conn *websocket.Conn, substr string) string {
	return readFrameUntil(t, conn, ws.MsgTypeControl, substr)
}

// TestVoiceCommand_PauseReachesRealFSM is the full-stack loop: mic audio +
// voice_command → (fake ASR "暂停") → rule classifier → pause verb → REAL
// Manager → REAL session event loop → FSM DICTATING→PAUSED → "paused" frame
// on the client socket.
func TestVoiceCommand_PauseReachesRealFSM(t *testing.T) {
	logger := infra.NewNoOpLogger()
	zapLogger := zap.NewNop()

	hub, err := ws.NewHub(ws.HubConfig{Limit: 10, RegisterBuffer: 32, UnregisterBuffer: 32}, zapLogger)
	if err != nil {
		t.Fatalf("failed to create hub: %v", err)
	}

	cfg := infra.DefaultConfig()
	pool, err := infra.NewWorkerPool(cfg.Worker, logger)
	if err != nil {
		t.Fatalf("failed to create worker pool: %v", err)
	}
	defer func() { _ = pool.Shutdown(context.Background()) }()

	fakeFSRS := &fsmFSRS{}
	manager, err := session.NewManager(hub, fakeFSRS, &fsmGen{}, &fsmStreamer{}, &fsmRepo{}, pool, logger, cfg)
	if err != nil {
		t.Fatalf("failed to create session manager: %v", err)
	}
	svcCtx, svcCancel := context.WithCancel(context.Background())
	defer svcCancel()
	go hub.Run(svcCtx)
	go func() {
		if startErr := manager.Start(svcCtx); startErr != nil {
			logger.Error("session manager start failed", infra.ErrField(startErr))
		}
	}()

	// Voice pipeline with the REAL manager as the session pusher.
	tr := &captureTranscriber{transcript: "暂停"}
	vs := newVoiceService(tr.transcribe, voice.NewHybridClassifier(nil), manager, logger, testVoiceConfig(0))

	handlers := ttsHandlers(manager, fakeFSRS, vs, nil, logger)

	// Capture the server-side *ws.Client through the audio route so the test
	// can compute the connID the session is registered under (the Manager
	// keys sessions by the client pointer, same as production handlers).
	serverClient := make(chan *ws.Client, 1)
	audioHandler := handlers[ws.MsgTypeAudio]
	handlers[ws.MsgTypeAudio] = func(ctx context.Context, c *ws.Client, h *ws.FrameHeader, p []byte) error {
		select {
		case serverClient <- c:
		default:
		}
		return audioHandler(ctx, c, h, p)
	}

	conn, teardown := dialVoiceTestServerOnHub(t, hub, handlers)
	defer teardown()

	// 1. Start a session through the real handler chain.
	sendControlJSON(t, conn, `{"type":"start_session","params":{"limit":2}}`)
	if ready := readControlUntil(t, conn, "session_ready"); ready == "" {
		t.Fatal("no session_ready")
	}

	// 2. Capture the server-side client via one dummy audio frame.
	sendAudioFrame(t, conn, 1, make([]byte, ws.AudioFrameSize))
	var sc *ws.Client
	select {
	case sc = <-serverClient:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out capturing server-side client")
	}
	connID := clientConnID(sc)

	// 3. Play the first word: IDLE → LISTENING; the fake streamer completes
	// instantly, pushing the system event that drives LISTENING → DICTATING.
	sendControlJSON(t, conn, `{"type":"play_word","params":{"word_id":"苹果"}}`)

	// 4. Wait (deterministic poll, ticket-02 convention) for DICTATING —
	// pause is only valid from there in the FSM table.
	pausedReached := false
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if s := manager.Lookup(connID); s != nil && s.State() == database.StateDictating {
			pausedReached = true
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !pausedReached {
		t.Fatal("session never reached DICTATING — cannot test pause verb")
	}

	// 5. The voice command itself: audio + trigger. The transcriber returns
	// 暂停, the rule fast path maps it to pause, and the REAL manager/FSM
	// must react.
	sendAudioFrame(t, conn, 2, make([]byte, ws.AudioFrameSize))
	sendControlJSON(t, conn, `{"type":"voice_command","params":{}}`)

	// 6. Observable outcome: the session's paused frame on the client socket
	// (voice_result will interleave; readControlUntil skips it).
	pausedPayload := readControlUntil(t, conn, `"type":"paused"`)
	if !strings.Contains(pausedPayload, `"paused"`) {
		t.Fatalf("paused frame payload = %s", pausedPayload)
	}

	// And the authoritative state agrees.
	if s := manager.Lookup(connID); s == nil || s.State() != database.StatePaused {
		got := "<nil>"
		if s != nil {
			got = string(s.State())
		}
		t.Errorf("session state after voice pause = %s, want PAUSED", got)
	}
}
