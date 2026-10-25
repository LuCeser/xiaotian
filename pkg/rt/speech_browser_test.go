//go:build rtbrowser

// speech_browser_test.go — issue #69 备选一（issue-69-alt1.md 步骤 2）的浏览器
// 测试基础设施：在测试进程内组装生产形态的 /rt 服务端——真实 Handler、真实
// connWriter、真实 TurnManager、生产 tutor 静态页面（pkg/web/static/tutor）、
// 需要持久化时使用临时 SQLite。外部语音供应商为 fake（providers_fake_test.go），
// 浏览器由 Node 驱动辅助器（testdata/speech-browser/driver.js）以真实 Chromium
// 打开生产页面驱动，只读观察结果回传。
//
// 隔离（计划 §六）：127.0.0.1 随机端口 + t.TempDir() 数据库，绝不接触开发
// 8080 或其它套件端口，不连接真实供应商。运行方式：
//
//	GOTOOLCHAIN=go1.26.9 go test -tags=rtbrowser ./pkg/rt -run 'P' -count=1
//
// 本文件的辅助器绝不替浏览器发送 ACK、绝不替服务端补发消息；对 ACK 的全部
// 断言都来自真实页面原生播放完成（rt-playback 的 onended）后的真实上行帧。
package rt

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xiaotian/home-tutor-agent/pkg/database"
	"github.com/xiaotian/home-tutor-agent/pkg/infra"
	"github.com/xiaotian/home-tutor-agent/pkg/lesson"
	"github.com/xiaotian/home-tutor-agent/pkg/mimo"
	"github.com/xiaotian/home-tutor-agent/pkg/ocr"
)

// speechPageAck is one sentence_ack the real page sent (observed read-only).
// PlayedBeforeAck is the driver's per-sentence verdict that the page's native
// onended preceded that ack — an empty-play (dropped PCM) or fabricated ack
// answers false here.
type speechPageAck struct {
	Epoch           uint64 `json:"epoch"`
	SentenceID      string `json:"sentenceId"`
	PlayedBeforeAck bool   `json:"playedBeforeAck"`
}

// speechPlaybackObservation mirrors rt-observe.js playbackState (subset).
type speechPlaybackObservation struct {
	Trace         []string `json:"trace"`
	DroppedChunks int      `json:"droppedChunks"`
}

// speechMicObservation mirrors fake-mic.js micInfo: the synthetic source the
// production capture actually adopted.
type speechMicObservation struct {
	ContextState string `json:"contextState"`
	PCMPlays     int    `json:"pcmPlays"`
	Tracks       []struct {
		Kind  string `json:"kind"`
		State string `json:"state"`
	} `json:"tracks"`
}

// speechDriverResult is the JSON contract of driver.js's SPEECH_RESULT line.
type speechDriverResult struct {
	OK       bool   `json:"ok"`
	Scenario string `json:"scenario"`
	Err      string `json:"error"`

	WSOpened         bool                      `json:"wsOpened"`
	DownBinaryFrames int                       `json:"downBinaryFrames"`
	DownBinaryBytes  int64                     `json:"downBinaryBytes"`
	PageAcks         []speechPageAck           `json:"pageAcks"`
	Playback         speechPlaybackObservation `json:"playback"`
	Mic              speechMicObservation      `json:"mic"`

	// starve-probe scenario fields: what the page REALLY received inside the
	// hold window while a fault control was active.
	HoldControlIn     int `json:"holdControlIn"`
	HoldBinaryIn      int `json:"holdBinaryIn"`
	WSCloseCount      int `json:"wsCloseCount"`
	LateVerdictEchoes int `json:"lateVerdictEchoes"`

	// voice-verdict-close scenario fields: the state-frame trace after the
	// settled round (the natural voice turn's evidence) and whether any late
	// teacher sentence followed the close.
	VoiceStates          []string `json:"voiceStates"`
	LateTeacherSentences int      `json:"lateTeacherSentences"`

	// ocr-prefill-close scenario fields: late ocr_result frames after close.
	LateOcrResults int `json:"lateOcrResults"`

	// check-submit-close scenario fields: late review_result frames and late
	// summary sentences after the close.
	LateReviewResults int `json:"lateReviewResults"`

	// resume-close-window scenario fields (1.13a): the presentation frames
	// that landed BEFORE the close, and any downlink after it.
	CheckStartedBeforeClose     int `json:"checkStarted"`
	TeacherSentencesBeforeClose int `json:"teacherSentences"`
	LateFramesAfterClose        int `json:"lateFrames"`
}

var speechExpectedOpening = []string{
	"你好呀！",
	"今天我们有 2 个词要写。",
	"听好哦：第 1 个词。",
}

var speechExpectedCheckResume = []string{
	"欢迎回来！",
	"我们继续检查刚才那 2 个词，接着点「对」或「错」告诉我吧。",
}

// speechHoldTTS wraps the fixture's fake TTS with a hold gate — the
// provider-return control point of step 4, kept in the rtbrowser build only
// (the production providers_fake_test.go stays untouched). While the gate is
// open every SynthesizeStream call parks (ctx-interruptible) before the
// inner synthesizer produces anything.
type speechHoldTTS struct {
	inner TTSSynthesizer

	mu   sync.Mutex
	hold chan struct{}
	hits int
}

func (s *speechHoldTTS) SynthesizeStream(ctx context.Context, text string, onAudio AudioChunkFunc, opts ...mimo.TTSOption) (*mimo.StreamResult, error) {
	s.mu.Lock()
	hold := s.hold
	if hold != nil {
		s.hits++
	}
	s.mu.Unlock()
	if hold != nil {
		select {
		case <-hold:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return s.inner.SynthesizeStream(ctx, text, onAudio, opts...)
}

func (s *speechHoldTTS) holdHits() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.hits
}

// speechManagerTracker wraps one connection's real TurnManager: it counts
// connections/closes and records the sentence_ack frames the BROWSER actually
// sent (parsed from the real inbound control stream — observation only, the
// message is always forwarded to the production handler untouched).
type speechManagerTracker struct {
	*TurnManager
	fixture *speechBrowserFixture

	mu     sync.Mutex
	acks   []SentenceAckMsg
	sender Sender
	closed bool
	closes int
}

func (m *speechManagerTracker) HandleControl(s Sender, data []byte) {
	m.mu.Lock()
	m.sender = s
	m.mu.Unlock()
	var probe struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(data, &probe); err == nil && probe.Type == TypeSentenceAck {
		var ack SentenceAckMsg
		if err := json.Unmarshal(data, &ack); err == nil {
			m.mu.Lock()
			m.acks = append(m.acks, ack)
			m.mu.Unlock()
		}
	}
	m.TurnManager.HandleControl(s, data)
}

func (m *speechManagerTracker) currentSender() Sender {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.sender
}

func (m *speechManagerTracker) Close(s Sender) {
	m.TurnManager.Close(s)
	if writer, ok := s.(*connWriter); ok {
		go func() {
			<-writer.done
			m.fixture.writerDone.Add(1)
		}()
	}
	m.mu.Lock()
	m.closed = true
	m.closes++
	m.mu.Unlock()
}

func (m *speechManagerTracker) ackCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.acks)
}

func (m *speechManagerTracker) wasClosed() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.closed
}

func (m *speechManagerTracker) closeCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.closes
}

// speechBrowserFixture is one test's isolated production-shaped server.
type speechBrowserFixture struct {
	t *testing.T

	ln  net.Listener
	srv *http.Server

	asr               *fakeASR
	llm               *fakeLLM
	tts               TTSSynthesizer
	words             *fakeWordSource
	tools             *LessonTools
	exec              LessonExecutor
	db                *sql.DB
	dbPath            string
	receipts          *lesson.ReceiptStore
	wordsOverride     WordSource
	expectedSentences []string

	mu       sync.Mutex
	managers []*speechManagerTracker

	// ocrRead, when armed, is wired into every future manager's photo
	// channel (cfg.OCR + the real pipeline around it) — the check stage's
	// 拍照检查 button then runs the production photo→OCR→prefill path.
	ocrRead func(ctx context.Context, systemPrompt, userPrompt string, imageJPEG []byte) (string, error)

	// timerProbe, when set, is installed as every manager's timerFactory
	// (the existing test seam): arms are counted while the REAL AfterFunc
	// still runs — the timing control point of step 4.
	timerProbe *speechTimerProbe

	// resumeHold, when set, is installed as every manager's
	// checkResumeInterleave — the window between the resume's IO and its
	// panel presentation (1.13a's browser claim-order probe). closeEntryHold
	// likewise rides closeSessionAt's init-write-lock window.
	resumeHold     func()
	closeEntryHold func()

	handlerActive, handlerDone        atomic.Int64
	writerDone                        atomic.Int64
	providerStarted, providerDone     atomic.Int64
	announcementRun, announcementDone atomic.Int64
}

type speechObservedTTS struct {
	inner   TTSSynthesizer
	fixture *speechBrowserFixture
}

type speechVerdictGate struct {
	LessonExecutor
	entered chan struct{}
	release chan struct{}
}

func (g *speechVerdictGate) ApplyVerdict(ctx context.Context, lessonID string, expectedVersion int, requestID string, verdict lesson.CheckVerdict) (lesson.ApplyVerdictResult, error) {
	close(g.entered)
	<-g.release
	return g.LessonExecutor.ApplyVerdict(ctx, lessonID, expectedVersion, requestID, verdict)
}

// speechBatchGate gates a MULTI-write batch (an OCR prefill pass writes per
// word): every ApplyVerdict call parks on the one shared release, and the
// first entry signals entered exactly once.
type speechBatchGate struct {
	LessonExecutor
	entered     chan struct{}
	release     chan struct{}
	enteredOnce sync.Once
}

func (g *speechBatchGate) ApplyVerdict(ctx context.Context, lessonID string, expectedVersion int, requestID string, verdict lesson.CheckVerdict) (lesson.ApplyVerdictResult, error) {
	g.enteredOnce.Do(func() { close(g.entered) })
	<-g.release
	return g.LessonExecutor.ApplyVerdict(ctx, lessonID, expectedVersion, requestID, verdict)
}

// speechHeldRead holds one OCR vision call: the reader parks before the
// scripted reply, so the prefill batch's ADMISSION happens only after the
// hold releases (the close-first ordering of TestCloseDuringOcrPrefills).
type speechHeldRead struct {
	inner   *fakeOCRReader
	entered chan struct{}
	hold    chan struct{}
	once    sync.Once
}

func (h *speechHeldRead) read(ctx context.Context, systemPrompt, userPrompt string, imageJPEG []byte) (string, error) {
	h.once.Do(func() { close(h.entered) })
	<-h.hold
	return h.inner.read(ctx, systemPrompt, userPrompt, imageJPEG)
}

func (s *speechObservedTTS) SynthesizeStream(ctx context.Context, text string, onAudio AudioChunkFunc, opts ...mimo.TTSOption) (*mimo.StreamResult, error) {
	s.fixture.providerStarted.Add(1)
	defer s.fixture.providerDone.Add(1)
	return s.inner.SynthesizeStream(ctx, text, onAudio, opts...)
}

// speechTimerProbe counts word-timer arms seen through the timerFactory seam.
type speechTimerProbe struct {
	arms atomic.Int64
}

func (p *speechTimerProbe) count() int64 { return p.arms.Load() }

// useTimerProbe installs the counting timer factory on every future manager.
func (f *speechBrowserFixture) useTimerProbe() *speechTimerProbe {
	f.t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.timerProbe != nil {
		f.t.Fatal("timer probe already installed")
	}
	f.timerProbe = &speechTimerProbe{}
	return f.timerProbe
}

func (f *speechBrowserFixture) gateTTS(hold chan struct{}) *speechHoldTTS {
	f.mu.Lock()
	defer f.mu.Unlock()
	gated := &speechHoldTTS{inner: f.tts, hold: hold}
	f.tts = gated
	return gated
}

// speechRepoRoot locates the repository root from this test file's path.
func speechRepoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate the test file for repo root discovery")
	}
	root := filepath.Dir(filepath.Dir(filepath.Dir(file))) // pkg/rt/x.go → root
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("repo root not found above %s: %v", file, err)
	}
	return root
}

// newSpeechBrowserFixture assembles the in-process production server: the
// real /rt Handler (real connWriter + TurnManager per connection) plus the
// PRODUCTION tutor static page, over a temp SQLite classroom store. Voice
// providers are the fake trio; the due vocabulary holds chinese words only,
// so the page's default auto subject resolves straight into that classroom.
func newSpeechBrowserFixture(t *testing.T) *speechBrowserFixture {
	t.Helper()

	root := speechRepoRoot(t)
	base := t.TempDir()
	dbPath := filepath.Join(base, "vocab.db")
	db, err := database.Open(dbPath)
	if err != nil {
		t.Fatalf("open temp sqlite: %v", err)
	}
	ctx := context.Background()
	if err := database.Migrate(ctx, db); err != nil {
		t.Fatalf("database migrate: %v", err)
	}
	if err := lesson.Migrate(ctx, db); err != nil {
		t.Fatalf("lesson migrate: %v", err)
	}
	lessonStore, err := lesson.NewStore(db, infra.NewNoOpLogger())
	if err != nil {
		t.Fatalf("lesson store: %v", err)
	}
	lessonExec, err := lesson.NewExecutor(lessonStore, infra.NewNoOpLogger())
	if err != nil {
		t.Fatalf("lesson executor: %v", err)
	}
	tools, err := NewLessonTools(lessonStore, lessonExec)
	if err != nil {
		t.Fatalf("lesson tools: %v", err)
	}

	due := []lesson.Word{
		{Word: "听写浏览器", Meaning: "fixture 第一词"},
		{Word: "第二词", Meaning: "fixture 第二词"},
	}
	f := &speechBrowserFixture{
		t:                 t,
		db:                db,
		dbPath:            dbPath,
		tools:             tools,
		exec:              tools,
		expectedSentences: append([]string(nil), speechExpectedOpening...),
		asr:               newFakeASR(),
		llm:               newFakeLLM(),
		// Two non-zero sawtooth chunks per sentence (~250ms each at 24kHz
		// s16le): audible-length PCM so real playback and onended are stable.
		// The inter-chunk delay mirrors the real TTS first-packet latency
		// (participation e2e does the same): connWriter's two select-fed
		// queues do not ORDER control vs audio frames (the issue-69-alt1
		// step-15 defect), so a zero-delay fake lets the first PCM chunk
		// race ahead of audio_start and the page drops it (empty-sentence
		// ack). The ordering fix itself is step 14–15's frame queue, not a
		// fixture workaround.
		tts: func() *fakeTTS {
			tts := newFakeTTS(2, 12000)
			tts.delay = 30 * time.Millisecond
			return tts
		}(),
		words: &fakeWordSource{
			dueByCat:  map[string][]lesson.Word{"chinese": due},
			peekByCat: map[string][]lesson.Word{"chinese": due[:1]},
		},
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	f.ln = ln

	handler, err := NewHandler(
		ServerConfig{AllowedOrigins: []string{ln.Addr().String()}, HeartbeatInterval: 30 * time.Second},
		f.newManager,
		infra.NewNoOpLogger(),
	)
	if err != nil {
		t.Fatalf("rt handler: %v", err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/rt", func(w http.ResponseWriter, r *http.Request) {
		f.handlerActive.Add(1)
		defer f.handlerActive.Add(-1)
		defer f.handlerDone.Add(1)
		handler.HandleWebSocket(w, r)
	})
	tutorDir := filepath.Join(root, "pkg", "web", "static", "tutor")
	if _, err := os.Stat(tutorDir); err != nil {
		t.Fatalf("tutor static dir: %v", err)
	}
	mux.Handle("/tutor/", http.StripPrefix("/tutor/", infra.StaticFileServer(tutorDir, infra.NewNoOpLogger())))

	f.srv = &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = f.srv.Serve(ln) }()
	t.Cleanup(f.close)
	return f
}

// newManager is the per-connection factory: a real TurnManager over the fake
// providers and the real temp-SQLite lesson tools, wrapped by the tracker.
func (f *speechBrowserFixture) newManager() SessionManager {
	f.mu.Lock()
	tts, probe, executor, ocrRead := f.tts, f.timerProbe, f.exec, f.ocrRead
	words, receipts := f.wordsOverride, f.receipts
	resumeHold, closeEntryHold := f.resumeHold, f.closeEntryHold
	f.mu.Unlock()
	if words == nil {
		words = f.words
	}
	tm := NewTurnManager(f.asr, f.llm, &speechObservedTTS{inner: tts, fixture: f}, executor, TurnManagerConfig{
		LessonID: "speech-browser-fixture",
		Words:    words,
		Receipts: receipts,
	}, NewEventRecorder(infra.NewNoOpLogger(), "speech-browser"), infra.NewNoOpLogger())
	tm.checkResumeInterleave = resumeHold
	tm.closeEntryInterleave = closeEntryHold
	if ocrRead != nil {
		// The production photo channel (ttBindPhoto's wiring, fixture-wide):
		// the scripted reader under the REAL pipeline, so 拍照检查 rides the
		// real assembler, JPEG validation and confidence gate.
		tm.cfg.OCR = ocrRead
		tm.ocrPipeline = ocr.NewPipeline(ocr.Reader(ocrRead), tm.cfg.PhotoConfidence)
	}
	if probe != nil {
		tm.timerFactory = func(d time.Duration, fn func()) *time.Timer {
			probe.arms.Add(1)
			return time.AfterFunc(d, fn)
		}
	}
	tm.starterInterleave = func(task func()) {
		f.announcementRun.Add(1)
		defer f.announcementDone.Add(1)
		task()
	}
	m := &speechManagerTracker{TurnManager: tm, fixture: f}
	f.mu.Lock()
	f.managers = append(f.managers, m)
	f.mu.Unlock()
	return m
}

func (f *speechBrowserFixture) seedChecking() {
	f.t.Helper()
	ctx := context.Background()
	lessonID := lessonIDForCategory("speech-browser-fixture", time.Now(), lesson.CategoryChinese)
	cards := []lesson.Word{{Word: "听写浏览器"}, {Word: "第二词"}}
	l, err := f.tools.EnsureLessonCards(ctx, lessonID, cards, lesson.CategoryChinese)
	if err == nil {
		var advance lesson.AdvanceResult
		advance, err = f.tools.AdvanceWord(ctx, lessonID, l.Version, "browser-seed-1")
		if err == nil {
			advance, err = f.tools.AdvanceWord(ctx, lessonID, advance.NewVersion, "browser-seed-2")
		}
		if err == nil {
			_, err = f.tools.EnterCheck(ctx, lessonID, advance.NewVersion, "browser-seed-check", nil)
		}
	}
	if err != nil {
		f.t.Fatalf("seed checking lesson: %v", err)
	}
	f.mu.Lock()
	f.expectedSentences = append([]string(nil), speechExpectedCheckResume...)
	f.mu.Unlock()
}

// seedCheckingWithOutcomes extends seedChecking with REAL stored outcomes on
// the enter_check write (word 0 confirmed clean → good → hard; word 1
// confirmed after two hints → hard → again): the D-03 fallback's degraded
// batch derives from the STORED outcomes, and an empty outcomes map would
// honestly rate nothing (1.11's shape, shared with 1.12b's multi-write case).
func (f *speechBrowserFixture) seedCheckingWithOutcomes() {
	f.t.Helper()
	ctx := context.Background()
	lessonID := lessonIDForCategory("speech-browser-fixture", time.Now(), lesson.CategoryChinese)
	cards := []lesson.Word{{Word: "听写浏览器"}, {Word: "第二词"}}
	l, err := f.tools.EnsureLessonCards(ctx, lessonID, cards, lesson.CategoryChinese)
	if err == nil {
		var advance lesson.AdvanceResult
		advance, err = f.tools.AdvanceWord(ctx, lessonID, l.Version, "fallback-seed-1")
		if err == nil {
			advance, err = f.tools.AdvanceWord(ctx, lessonID, advance.NewVersion, "fallback-seed-2")
		}
		if err == nil {
			_, err = f.tools.EnterCheck(ctx, lessonID, advance.NewVersion, "fallback-seed-check", map[string]lesson.WordOutcome{
				"0": {Outcome: outcomeConfirmed, Hints: 0}, // good → hard
				"1": {Outcome: outcomeConfirmed, Hints: 2}, // hard → again
			})
		}
	}
	if err != nil {
		f.t.Fatalf("seed checking lesson with outcomes: %v", err)
	}
	f.mu.Lock()
	f.expectedSentences = append([]string(nil), speechExpectedCheckResume...)
	f.mu.Unlock()
}

// seedCheckingWithManualVerdicts extends seedChecking with two real manual
// verdict writes: the panel then re-renders from the authoritative echo with
// every word confirmed, and the 提交 button enables (the client gate mirrors
// the server's manual gate).
func (f *speechBrowserFixture) seedCheckingWithManualVerdicts() {
	f.t.Helper()
	f.seedChecking()
	ctx := context.Background()
	lessonID := lessonIDForCategory("speech-browser-fixture", time.Now(), lesson.CategoryChinese)
	l, err := f.tools.GetLessonState(ctx, lessonID)
	if err != nil {
		f.t.Fatalf("read seeded lesson: %v", err)
	}
	for idx := range l.Words {
		res, err := f.tools.ApplyVerdict(ctx, lessonID, l.Version, fmt.Sprintf("browser-seed-manual-%d", idx),
			lesson.CheckVerdict{WordIndex: idx, Correct: true, Manual: true})
		if err != nil {
			f.t.Fatalf("seed manual verdict %d: %v", idx, err)
		}
		l.Version = res.NewVersion
	}
}

func (f *speechBrowserFixture) gateScreenVerdict() (entered, release chan struct{}) {
	f.mu.Lock()
	defer f.mu.Unlock()
	g := &speechVerdictGate{LessonExecutor: f.exec, entered: make(chan struct{}), release: make(chan struct{})}
	f.exec = g
	return g.entered, g.release
}

// gateOcrPrefillWrites gates a whole prefill BATCH: every ApplyVerdict parks
// on the shared release, first entry signals once.
func (f *speechBrowserFixture) gateOcrPrefillWrites() (entered, release chan struct{}) {
	f.mu.Lock()
	defer f.mu.Unlock()
	g := &speechBatchGate{LessonExecutor: f.exec, entered: make(chan struct{}), release: make(chan struct{})}
	f.exec = g
	return g.entered, g.release
}

// speechOneGate is one reusable entered/release pair (first hit signals);
// openUp is idempotent so a t.Cleanup can pair with an explicit release.
type speechOneGate struct {
	entered  chan struct{}
	release  chan struct{}
	armOnce  sync.Once
	openOnce sync.Once
}

func newSpeechOneGate() *speechOneGate {
	return &speechOneGate{entered: make(chan struct{}), release: make(chan struct{})}
}

func (g *speechOneGate) arm()  { g.armOnce.Do(func() { close(g.entered) }) }
func (g *speechOneGate) wait() { <-g.release }
func (g *speechOneGate) openUp() {
	g.arm()
	g.openOnce.Do(func() { close(g.release) })
}

// speechSubmitGate wraps the executor's CompleteCheck at the two formal-
// submit control points (1.12a's window set, installed for 1.09's orderings):
// before parks AHEAD of the real latch write; committed parks AFTER the real
// write succeeded but BEFORE its result returns to the manager.
type speechSubmitGate struct {
	LessonExecutor
	before    *speechOneGate
	committed *speechOneGate
}

func (g *speechSubmitGate) CompleteCheck(ctx context.Context, lessonID string, expectedVersion int, requestID string, unchecked bool) (lesson.CompleteCheckResult, error) {
	if g.before != nil {
		g.before.arm()
		g.before.wait()
	}
	res, err := g.LessonExecutor.CompleteCheck(ctx, lessonID, expectedVersion, requestID, unchecked)
	if err == nil && g.committed != nil {
		g.committed.arm()
		g.committed.wait()
	}
	return res, err
}

// gateCheckSubmit arms the chosen CompleteCheck control points.
func (f *speechBrowserFixture) gateCheckSubmit(before, committed bool) (beforeGate, committedGate *speechOneGate) {
	f.mu.Lock()
	defer f.mu.Unlock()
	g := &speechSubmitGate{LessonExecutor: f.exec}
	if before {
		g.before = newSpeechOneGate()
		beforeGate = g.before
	}
	if committed {
		g.committed = newSpeechOneGate()
		committedGate = g.committed
	}
	f.exec = g
	return beforeGate, committedGate
}

// armPhotoOCR wires the scripted fake OCR reader into every future manager's
// photo channel. replies follow fakeOCRReader's per-call queue.
func (f *speechBrowserFixture) armPhotoOCR(replies ...string) *fakeOCRReader {
	f.mu.Lock()
	defer f.mu.Unlock()
	reader := &fakeOCRReader{replies: replies}
	f.ocrRead = reader.read
	return reader
}

// holdPhotoOCR arms the scripted reader behind a hold: the vision call parks
// after entering, so the prefill batch only starts when the hold releases.
func (f *speechBrowserFixture) holdPhotoOCR(replies ...string) (entered, release chan struct{}) {
	f.mu.Lock()
	defer f.mu.Unlock()
	h := &speechHeldRead{inner: &fakeOCRReader{replies: replies}, entered: make(chan struct{}), hold: make(chan struct{})}
	f.ocrRead = h.read
	return h.entered, h.hold
}

// scriptVoiceCheckVerdict arms the fake providers for ONE spoken verdict turn:
// the synthetic-mic tone rides the production capture into the real VAD, the
// fake ASR answers the transcript, and the fake LLM classifies it to
// check_right on the given word — the natural voice entry (applyVoiceVerdict),
// the screen taps' sibling. The scripted answers queue behind whatever the
// connection already consumed.
func (f *speechBrowserFixture) scriptVoiceCheckVerdict(transcript string, index int) {
	f.t.Helper()
	f.asr.mu.Lock()
	f.asr.script = append(f.asr.script, asrScript{text: transcript})
	f.asr.mu.Unlock()
	inner := fmt.Sprintf(`{\"intent\":\"%s\",\"reply\":\"\",\"index\":%d}`, intentCheckRight, index)
	f.llm.mu.Lock()
	f.llm.script = append(f.llm.script, llmScript{
		wire: fmt.Sprintf(`{"choices":[{"message":{"content":"%s"}}]}`, inner),
	})
	f.llm.mu.Unlock()
}

func (f *speechBrowserFixture) url() string { return "http://" + f.ln.Addr().String() + "/" }

// close tears the fixture down: stop accepting, close every live connection,
// then drop the temp SQLite handle. Safe to call twice (t.Cleanup + explicit).
func (f *speechBrowserFixture) close() {
	if f.srv != nil {
		_ = f.srv.Close()
	}
	if f.db != nil {
		_ = f.db.Close()
	}
}

// waitFirstManager blocks until a browser connection created its manager.
func (f *speechBrowserFixture) waitFirstManager(timeout time.Duration) *speechManagerTracker {
	f.t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		f.mu.Lock()
		n := len(f.managers)
		var m *speechManagerTracker
		if n > 0 {
			m = f.managers[0]
		}
		f.mu.Unlock()
		if m != nil {
			return m
		}
		time.Sleep(20 * time.Millisecond)
	}
	f.t.Fatal("no /rt connection was created before the deadline")
	return nil
}

// waitManagersClosed blocks until every created manager saw Close (the
// transport calls it exactly once per connection when its read loop exits).
func (f *speechBrowserFixture) waitManagersClosed(timeout time.Duration) bool {
	f.t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		f.mu.Lock()
		all := append([]*speechManagerTracker(nil), f.managers...)
		f.mu.Unlock()
		if len(all) > 0 {
			done := true
			for _, m := range all {
				if !m.wasClosed() {
					done = false
					break
				}
			}
			if done {
				return true
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	return false
}

// runDriver spawns the Node browser driver and returns its parsed result.
// extraArgs append verbatim (e.g. "--hold-ms", "3000" for starve-probe).
// A failed scenario comes back as speechDriverResult{OK:false, Err:...}
// with the driver's combined output attached for diagnosis.
func (f *speechBrowserFixture) runDriver(scenario string, timeout time.Duration, extraArgs ...string) speechDriverResult {
	f.t.Helper()
	root := speechRepoRoot(f.t)
	driver := filepath.Join(root, "pkg", "rt", "testdata", "speech-browser", "driver.js")

	ctx, cancel := context.WithTimeout(context.Background(), timeout+15*time.Second)
	defer cancel()
	args := append([]string{
		driver,
		"--url", f.url() + "tutor/",
		"--scenario", scenario,
		"--timeout-ms", fmt.Sprint(int64(timeout / time.Millisecond)),
	}, extraArgs...)
	if scenario == "fixture" || scenario == "held-fixture" || scenario == "close-after-opening" ||
		scenario == "screen-verdict-close" || scenario == "voice-verdict-close" ||
		scenario == "ocr-prefill-close" || scenario == "check-submit-close" {
		f.mu.Lock()
		sentences := append([]string(nil), f.expectedSentences...)
		f.mu.Unlock()
		expected, err := json.Marshal(sentences)
		if err != nil {
			f.t.Fatalf("encode expected opening: %v", err)
		}
		args = append(args, "--expected-sentences-json", string(expected))
	}
	cmd := exec.CommandContext(ctx, "node", args...)
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	runErr := cmd.Run()

	var res speechDriverResult
	line := lastSpeechResultLine(out.String())
	if line == "" {
		f.t.Fatalf("driver produced no SPEECH_RESULT line (run err=%v)\nstdout:\n%s\nstderr:\n%s",
			runErr, out.String(), errOut.String())
	}
	if err := json.Unmarshal([]byte(line), &res); err != nil {
		f.t.Fatalf("driver result decode: %v\nline: %s", err, line)
	}
	if runErr != nil && res.OK {
		f.t.Fatalf("driver exited with error %v but reported ok", runErr)
	}
	if !res.OK {
		f.t.Fatalf("driver scenario %q failed: %s\nstderr:\n%s", scenario, res.Err, errOut.String())
	}
	return res
}

func TestCloseTransportFailure(t *testing.T) {
	f := newSpeechBrowserFixture(t)
	var attempts atomic.Int64
	restore, _ := speechFaultSetSend(func(kind string, payload []byte) error {
		if kind != "control" {
			return nil
		}
		var message struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(payload, &message) == nil && message.Type == TypeSessionEnded {
			attempts.Add(1)
			return errTerminalRefused
		}
		return nil
	})
	t.Cleanup(restore)

	settled := f.watchAnnouncementSettled(60 * time.Second)
	driver := f.runDriverAsync("close-after-opening", 45*time.Second, "--hold-ms", "1000")
	select {
	case <-settled:
	case <-time.After(10 * time.Second):
		t.Fatal("opening did not settle before the close test")
	}
	m := f.waitFirstManager(5 * time.Second)
	cancelCount := ttArmCancelCounting(m.TurnManager)
	res := <-driver
	if res.WSCloseCount != 1 {
		t.Fatalf("original WebSocket close events = %d, want 1", res.WSCloseCount)
	}
	if attempts.Load() != 1 {
		t.Fatalf("session_ended attempts = %d, want 1", attempts.Load())
	}
	f.assertOriginalTasksExited()
	if m.closeCount() != 1 || atomic.LoadInt32(cancelCount) != 1 {
		t.Fatalf("manager closes=%d cleanup cancels=%d, want 1/1", m.closeCount(), atomic.LoadInt32(cancelCount))
	}
}

func TestCloseDuringScreenVerdict(t *testing.T) {
	f := newSpeechBrowserFixture(t)
	f.seedChecking()
	entered, release := f.gateScreenVerdict()
	var releaseOnce sync.Once
	releaseGate := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(releaseGate)

	driver := f.runDriverAsync("screen-verdict-close", 45*time.Second)
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatal("screen verdict did not reach the real persistence gate")
	}
	m := f.waitFirstManager(5 * time.Second)
	if !m.tryTerminalClose(m.currentSender(), ReasonClientEnd) {
		t.Fatal("test close did not obtain session ownership")
	}
	releaseGate()
	res := <-driver
	if res.WSCloseCount != 1 || res.LateVerdictEchoes != 0 {
		t.Fatalf("close events=%d late verdict echoes=%d, want 1/0", res.WSCloseCount, res.LateVerdictEchoes)
	}
	f.assertOriginalTasksExited()

	lessonID := lessonIDForCategory("speech-browser-fixture", time.Now(), lesson.CategoryChinese)
	stored, err := f.tools.GetLessonState(context.Background(), lessonID)
	if err != nil {
		t.Fatalf("read stored lesson: %v", err)
	}
	if len(stored.Verdicts) != 1 || !stored.Verdicts[0].Manual || !stored.Verdicts[0].Correct {
		t.Fatalf("stored verdicts = %+v, want the accepted manual screen verdict", stored.Verdicts)
	}
	m.mu.Lock()
	pending := len(m.checkWrites)
	m.mu.Unlock()
	if pending != 0 {
		t.Fatalf("completed check-write admissions = %d, want 0", pending)
	}
}

// TestCloseDuringVoiceVerdict 是 1.07b 的验收：检查阶段的自然语音入口
// （合成麦克风音调 → 生产采集 → 服务端 VAD → ASR → 检查分类 → 语音判定）
// 在真实判定写入的持久化门上与关闭竞争——已接纳的判定保存仍然完成（保存
// 责任不随轮次取消而取消），关闭后没有任何迟到的回显或播报，登记全部收回。
func TestCloseDuringVoiceVerdict(t *testing.T) {
	f := newSpeechBrowserFixture(t)
	f.seedChecking()
	f.scriptVoiceCheckVerdict("第一个词对了", 1)
	entered, release := f.gateScreenVerdict()
	var releaseOnce sync.Once
	releaseGate := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(releaseGate)

	driver := f.runDriverAsync("voice-verdict-close", 45*time.Second)
	select {
	case <-entered:
	case <-time.After(30 * time.Second):
		t.Fatal("spoken verdict did not reach the real persistence gate — the natural voice turn never landed")
	}
	m := f.waitFirstManager(5 * time.Second)
	if !m.tryTerminalClose(m.currentSender(), ReasonClientEnd) {
		t.Fatal("test close did not obtain session ownership")
	}
	releaseGate()
	res := <-driver
	if res.WSCloseCount != 1 || res.LateVerdictEchoes != 0 || res.LateTeacherSentences != 0 {
		t.Fatalf("close=%d late echoes=%d late teacher sentences=%d, want 1/0/0",
			res.WSCloseCount, res.LateVerdictEchoes, res.LateTeacherSentences)
	}
	voiceTurn := false
	for _, v := range res.VoiceStates {
		if v == StateProcessing {
			voiceTurn = true
		}
	}
	if !voiceTurn {
		t.Fatalf("state trace after the settled round = %v, want a real processing turn from the spoken verdict", res.VoiceStates)
	}
	f.assertOriginalTasksExited()

	lessonID := lessonIDForCategory("speech-browser-fixture", time.Now(), lesson.CategoryChinese)
	stored, err := f.tools.GetLessonState(context.Background(), lessonID)
	if err != nil {
		t.Fatalf("read stored lesson: %v", err)
	}
	if len(stored.Verdicts) != 1 || !stored.Verdicts[0].Manual || !stored.Verdicts[0].Correct {
		t.Fatalf("stored verdicts = %+v, want the accepted spoken manual verdict", stored.Verdicts)
	}
	m.mu.Lock()
	pending := len(m.checkWrites)
	m.mu.Unlock()
	if pending != 0 {
		t.Fatalf("completed check-write admissions = %d, want 0", pending)
	}
}

// TestCloseDuringOcrPrefills 是 1.08 的验收：真实拍照入口（页面 file input →
// canvas 降采样 → 分块 photo_chunk → 生产组装器 → OCR → 预填批次）与关闭的
// 两种顺序——批次已接纳、写入在途时关闭：保存完成，人工判定优先规则保留；
// 批次尚未接纳（OCR 在途）时关闭：不接纳新批次，零写入。两种顺序都无迟到
// 回显／ocr_result／播报，登记全部收回。
func TestCloseDuringOcrPrefills(t *testing.T) {
	// Both words recognized positionally → two prefill writes per batch.
	prefillReplies := []string{`[{"text":"听写浏览器","confidence":0.95},{"text":"第二词","confidence":0.95}]`}
	storedVerdicts := func(f *speechBrowserFixture) []lesson.CheckVerdict {
		t.Helper()
		lessonID := lessonIDForCategory("speech-browser-fixture", time.Now(), lesson.CategoryChinese)
		l, err := f.tools.GetLessonState(context.Background(), lessonID)
		if err != nil {
			t.Fatalf("read stored lesson: %v", err)
		}
		return l.Verdicts
	}

	t.Run("in-flight batch at close keeps manual priority", func(t *testing.T) {
		f := newSpeechBrowserFixture(t)
		f.seedChecking()
		f.armPhotoOCR(prefillReplies...)
		entered, release := f.gateOcrPrefillWrites()
		var releaseOnce sync.Once
		releaseGate := func() { releaseOnce.Do(func() { close(release) }) }
		t.Cleanup(releaseGate)

		driver := f.runDriverAsync("ocr-prefill-close", 45*time.Second)
		select {
		case <-entered:
		case <-time.After(30 * time.Second):
			t.Fatal("OCR prefill batch did not reach the first real persistence gate")
		}
		m := f.waitFirstManager(5 * time.Second)

		// A cross-connection manual verdict lands while the batch's first
		// write parks (another tab's tap — §七 multi-connection rule): the
		// stored truth must keep it and drop the word's prefill.
		lessonID := lessonIDForCategory("speech-browser-fixture", time.Now(), lesson.CategoryChinese)
		l, err := f.tools.GetLessonState(context.Background(), lessonID)
		if err != nil {
			t.Fatalf("read lesson state: %v", err)
		}
		if _, err := f.tools.ApplyVerdict(context.Background(), lessonID, l.Version, "cross-tab-manual-1",
			lesson.CheckVerdict{WordIndex: 0, Correct: true, Manual: true}); err != nil {
			t.Fatalf("cross-connection manual verdict: %v", err)
		}
		if !m.tryTerminalClose(m.currentSender(), ReasonClientEnd) {
			t.Fatal("test close did not obtain session ownership")
		}
		releaseGate()
		res := <-driver
		if res.WSCloseCount != 1 || res.LateVerdictEchoes != 0 || res.LateOcrResults != 0 || res.LateTeacherSentences != 0 {
			t.Fatalf("close=%d late echoes=%d ocr=%d sentences=%d, want 1/0/0/0",
				res.WSCloseCount, res.LateVerdictEchoes, res.LateOcrResults, res.LateTeacherSentences)
		}
		f.assertOriginalTasksExited()

		verdicts := storedVerdicts(f)
		if len(verdicts) != 2 {
			t.Fatalf("stored verdicts = %+v, want the manual tap plus the surviving prefill", verdicts)
		}
		byIndex := map[int]lesson.CheckVerdict{}
		for _, v := range verdicts {
			byIndex[v.WordIndex] = v
		}
		if v := byIndex[0]; !v.Manual || !v.Correct {
			t.Fatalf("word 0 verdict = %+v, want the cross-connection manual verdict to stand", v)
		}
		if v := byIndex[1]; v.Manual || !v.Correct {
			t.Fatalf("word 1 verdict = %+v, want the accepted OCR prefill (manual=false)", v)
		}
		m.mu.Lock()
		pending := len(m.checkWrites)
		m.mu.Unlock()
		if pending != 0 {
			t.Fatalf("completed check-write admissions = %d, want 0", pending)
		}
	})

	t.Run("batch arriving after close writes nothing", func(t *testing.T) {
		f := newSpeechBrowserFixture(t)
		f.seedChecking()
		entered, release := f.holdPhotoOCR(prefillReplies...)
		var releaseOnce sync.Once
		releaseHold := func() { releaseOnce.Do(func() { close(release) }) }
		t.Cleanup(releaseHold)

		driver := f.runDriverAsync("ocr-prefill-close", 45*time.Second)
		select {
		case <-entered:
		case <-time.After(30 * time.Second):
			t.Fatal("the real OCR vision call never started")
		}
		m := f.waitFirstManager(5 * time.Second)
		if !m.tryTerminalClose(m.currentSender(), ReasonClientEnd) {
			t.Fatal("test close did not obtain session ownership")
		}
		releaseHold()
		res := <-driver
		if res.WSCloseCount != 1 || res.LateVerdictEchoes != 0 || res.LateOcrResults != 0 {
			t.Fatalf("close=%d late echoes=%d ocr=%d, want 1/0/0",
				res.WSCloseCount, res.LateVerdictEchoes, res.LateOcrResults)
		}
		f.assertOriginalTasksExited()

		if verdicts := storedVerdicts(f); len(verdicts) != 0 {
			t.Fatalf("stored verdicts = %+v, want zero writes — a batch arriving after close is not admitted", verdicts)
		}
		m.mu.Lock()
		pending := len(m.checkWrites)
		m.mu.Unlock()
		if pending != 0 {
			t.Fatalf("registered check-write admissions = %d, want 0", pending)
		}
	})
}

// TestAcceptedSubmitSurvivesClose 是 1.09 的验收：正式提交（真实 UI 提交按钮
// → check_submit → CompleteCheck 锁存 → 保存与回执）与关闭的两种顺序——实际
// 提交发生前关闭、提交已落库但尚未返回时关闭。已接纳的提交保存责任延续到
// 实际保存及回执完成（持久化独立上下文，不随轮次取消），保存恰好一次，关
// 闭后没有迟到的 review_result 或总结播报，登记在播报前收回。
func TestAcceptedSubmitSurvivesClose(t *testing.T) {
	assertOutcome := func(t *testing.T, f *speechBrowserFixture, m *speechManagerTracker, res speechDriverResult) {
		t.Helper()
		if res.WSCloseCount != 1 || res.LateReviewResults != 0 || res.LateTeacherSentences != 0 {
			t.Fatalf("close=%d late review=%d late sentences=%d, want 1/0/0",
				res.WSCloseCount, res.LateReviewResults, res.LateTeacherSentences)
		}
		f.assertOriginalTasksExited()

		lessonID := lessonIDForCategory("speech-browser-fixture", time.Now(), lesson.CategoryChinese)
		l, err := f.tools.GetLessonState(context.Background(), lessonID)
		if err != nil {
			t.Fatalf("read stored lesson: %v", err)
		}
		if l.Stage != lesson.StageSubmitted {
			t.Fatalf("stored stage = %q, want submitted — the accepted submit's latch must complete", l.Stage)
		}
		if len(l.Verdicts) != 2 {
			t.Fatalf("stored verdicts = %+v, want both manual verdicts kept", l.Verdicts)
		}
		if subs := len(f.words.submissions()); subs != 1 {
			t.Fatalf("review saves = %d, want exactly one save for the accepted submit", subs)
		}
		m.mu.Lock()
		pending := len(m.checkWrites)
		m.mu.Unlock()
		if pending != 0 {
			t.Fatalf("completed check-write admissions = %d, want 0 (released before the announcement)", pending)
		}
	}

	t.Run("close before the actual commit", func(t *testing.T) {
		f := newSpeechBrowserFixture(t)
		f.seedCheckingWithManualVerdicts()
		before, _ := f.gateCheckSubmit(true, false)
		t.Cleanup(before.openUp)

		driver := f.runDriverAsync("check-submit-close", 45*time.Second)
		select {
		case <-before.entered:
		case <-time.After(30 * time.Second):
			t.Fatal("the formal submit never reached the real latch write")
		}
		m := f.waitFirstManager(5 * time.Second)
		if !m.tryTerminalClose(m.currentSender(), ReasonClientEnd) {
			t.Fatal("test close did not obtain session ownership")
		}
		before.openUp()
		assertOutcome(t, f, m, <-driver)
	})

	t.Run("close after commit before return", func(t *testing.T) {
		f := newSpeechBrowserFixture(t)
		f.seedCheckingWithManualVerdicts()
		_, committed := f.gateCheckSubmit(false, true)
		t.Cleanup(committed.openUp)

		driver := f.runDriverAsync("check-submit-close", 45*time.Second)
		select {
		case <-committed.entered:
		case <-time.After(30 * time.Second):
			t.Fatal("the formal submit never committed its latch write")
		}
		m := f.waitFirstManager(5 * time.Second)
		if !m.tryTerminalClose(m.currentSender(), ReasonClientEnd) {
			t.Fatal("test close did not obtain session ownership")
		}
		committed.openUp()
		assertOutcome(t, f, m, <-driver)
	})
}

// TestCloseFallbackAfterAcceptedWrites 是 1.11 的验收：检查阶段（无人工
// 判定落库）关闭时，一笔已接纳的判定写入仍在途——兜底先在锁外等它完成，
// 再读取真实 SQLite 的权威行，仅在仍 checking 时用权威版本竞争兜底提交。
// 判定写入在关闭后落库推进了行版本：兜底结果不依赖关闭快照里的旧版本，
// 行最终 submitted+unchecked，FSRS 只有一批降级评分，关闭后没有迟到回显。
func TestCloseFallbackAfterAcceptedWrites(t *testing.T) {
	f := newSpeechBrowserFixture(t)
	// seedChecking's shape, but the enter_check write carries REAL stored
	// outcomes — the fallback's degraded batch derives from them, and an
	// empty outcomes map would honestly rate nothing.
	{
		ctx := context.Background()
		lessonID := lessonIDForCategory("speech-browser-fixture", time.Now(), lesson.CategoryChinese)
		cards := []lesson.Word{{Word: "听写浏览器"}, {Word: "第二词"}}
		l, err := f.tools.EnsureLessonCards(ctx, lessonID, cards, lesson.CategoryChinese)
		if err == nil {
			var advance lesson.AdvanceResult
			advance, err = f.tools.AdvanceWord(ctx, lessonID, l.Version, "fallback-seed-1")
			if err == nil {
				advance, err = f.tools.AdvanceWord(ctx, lessonID, advance.NewVersion, "fallback-seed-2")
			}
			if err == nil {
				_, err = f.tools.EnterCheck(ctx, lessonID, advance.NewVersion, "fallback-seed-check", map[string]lesson.WordOutcome{
					"0": {Outcome: outcomeConfirmed, Hints: 0}, // good → hard
					"1": {Outcome: outcomeConfirmed, Hints: 2}, // hard → again
				})
			}
		}
		if err != nil {
			t.Fatalf("seed checking lesson: %v", err)
		}
		f.mu.Lock()
		f.expectedSentences = append([]string(nil), speechExpectedCheckResume...)
		f.mu.Unlock()
	}
	entered, release := f.gateScreenVerdict()
	var releaseOnce sync.Once
	releaseGate := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(releaseGate)

	driver := f.runDriverAsync("screen-verdict-close", 45*time.Second)
	select {
	case <-entered:
	case <-time.After(30 * time.Second):
		t.Fatal("the screen verdict did not reach the real persistence gate")
	}
	m := f.waitFirstManager(5 * time.Second)
	// The goodbye close claims the fallback while the accepted verdict write
	// is parked AHEAD of the real SQLite write.
	m.handleEndSession(m.currentSender())
	releaseGate()
	res := <-driver
	if res.WSCloseCount != 1 || res.LateVerdictEchoes != 0 {
		t.Fatalf("close=%d late verdict echoes=%d, want 1/0", res.WSCloseCount, res.LateVerdictEchoes)
	}
	f.assertOriginalTasksExited()

	// The fallback's one review batch is its terminal persistence step —
	// once it lands, the authoritative row has long been decided.
	if !waitFor(t, 10*time.Second, func() bool { return len(f.words.submissions()) == 1 }) {
		t.Fatalf("fallback review batches = %d, want exactly one degraded batch", len(f.words.submissions()))
	}
	subs := f.words.submissions()
	if subs[0]["听写浏览器"] != ratingHard || subs[0]["第二词"] != ratingAgain {
		t.Fatalf("fallback degraded batch = %v, want hard/again from the stored outcomes", subs[0])
	}
	lessonID := lessonIDForCategory("speech-browser-fixture", time.Now(), lesson.CategoryChinese)
	stored, err := f.tools.GetLessonState(context.Background(), lessonID)
	if err != nil {
		t.Fatalf("read stored lesson: %v", err)
	}
	if stored.Stage != lesson.StageSubmitted || !stored.Unchecked {
		t.Fatalf("stored stage=%q unchecked=%v, want submitted+unchecked — the fallback must land on the authoritative version, not the snapshot's",
			stored.Stage, stored.Unchecked)
	}
	if len(stored.Verdicts) != 1 || !stored.Verdicts[0].Manual || !stored.Verdicts[0].Correct {
		t.Fatalf("stored verdicts = %+v, want the accepted manual screen verdict kept", stored.Verdicts)
	}
	m.mu.Lock()
	pending := len(m.checkWrites)
	m.mu.Unlock()
	if pending != 0 {
		t.Fatalf("completed check-write admissions = %d, want 0", pending)
	}
}

// TestCloseFallbackMultipleWrites 是 1.12b 的验收：两笔已接纳但尚未提交的
// 判定（一个 OCR 预填批次内的两笔逐词写入，1.06 纪律下由一次登记覆盖）与
// 关闭竞争——兜底先在锁外等批次完成，再读权威行：两笔写入已把版本连续推
// 进两次，兜底用权威版本竞争一次成功，行 submitted+unchecked、两笔预填保
// 留、FSRS 只有一批降级评分、关闭后没有迟到回显。读回门（1.12a）把「读
// 权威」的时机握在测试手里：读门在批次仍驻留时打开——跳过等待的实现会在
// 门放行处读到写入落库前的旧版本并闩住它，随后两笔写入全部冲突丢失（撤
// 销验证的红证据即此断言）。
func TestCloseFallbackMultipleWrites(t *testing.T) {
	f := newSpeechBrowserFixture(t)
	f.seedCheckingWithOutcomes()
	f.armPhotoOCR(`[{"text":"听写浏览器","confidence":0.95},{"text":"第二词","confidence":0.95}]`)
	verdictEntered, verdictRelease := f.gateOcrPrefillWrites() // the batch's per-word ApplyVerdict hold
	readGate := f.gateStateRead()
	var releaseOnce sync.Once
	releaseWrites := func() { releaseOnce.Do(func() { close(verdictRelease) }) }
	t.Cleanup(releaseWrites)

	lessonID := lessonIDForCategory("speech-browser-fixture", time.Now(), lesson.CategoryChinese)
	seeded, err := f.tools.GetLessonState(context.Background(), lessonID)
	if err != nil {
		t.Fatalf("read seeded lesson: %v", err)
	}

	driver := f.runDriverAsync("ocr-prefill-close", 45*time.Second)
	m := f.waitFirstManager(5 * time.Second)
	select {
	case <-verdictEntered:
	case res := <-driver:
		t.Fatalf("driver finished before the batch: ok=%v err=%s", res.OK, res.Err)
	case <-time.After(30 * time.Second):
		m.mu.Lock()
		pending := len(m.checkWrites)
		m.mu.Unlock()
		t.Fatalf("the prefill batch never hit the gate: admissions=%d", pending)
	}
	// The batch's ONE admission is registered before its first write parks.
	if !waitFor(t, 10*time.Second, func() bool {
		m.mu.Lock()
		defer m.mu.Unlock()
		return len(m.checkWrites) == 1
	}) {
		t.Fatal("the prefill batch was never admitted")
	}
	// Hold the read gate NOW: the fixture/resume reads are long past, the
	// parked batch issues no reads, so the fallback's read is the first one
	// of the held window — holding before the close removes any start-up
	// race a fast (wait-skipping) fallback could win.
	readGate.hold()
	// The goodbye close claims the fallback while the batch's writes are
	// parked AHEAD of their real SQLite commits.
	m.handleEndSession(m.currentSender())
	// Open the held read gate while the writes are STILL parked: the
	// waiting fallback has not reached the gate yet; an implementation that
	// skips the wait reads the PRE-write version here.
	readGate.openUp()
	// Release the batch: both prefill writes commit as v+1 and v+2; the
	// waiting fallback then reads v+2 and wins the latch on the fresh
	// version.
	releaseWrites()
	res := <-driver
	if res.WSCloseCount != 1 || res.LateVerdictEchoes != 0 || res.LateOcrResults != 0 {
		t.Fatalf("close=%d late echoes=%d late ocr=%d, want 1/0/0",
			res.WSCloseCount, res.LateVerdictEchoes, res.LateOcrResults)
	}
	f.assertOriginalTasksExited()

	if !waitFor(t, 10*time.Second, func() bool { return len(f.words.submissions()) == 1 }) {
		t.Fatalf("fallback review batches = %d, want exactly one degraded batch", len(f.words.submissions()))
	}
	subs := f.words.submissions()
	if subs[0]["听写浏览器"] != ratingHard || subs[0]["第二词"] != ratingAgain {
		t.Fatalf("fallback degraded batch = %v, want hard/again from the stored outcomes", subs[0])
	}
	stored, err := f.tools.GetLessonState(context.Background(), lessonID)
	if err != nil {
		t.Fatalf("read stored lesson: %v", err)
	}
	if stored.Stage != lesson.StageSubmitted || !stored.Unchecked {
		t.Fatalf("stored stage=%q unchecked=%v, want submitted+unchecked on the post-wait authoritative version",
			stored.Stage, stored.Unchecked)
	}
	// The consecutive-version evidence: both prefill writes landed (v+1,
	// v+2) and the fallback's latch added exactly one more.
	if stored.Version != seeded.Version+3 {
		t.Fatalf("stored version = %d, want seeded %d + 2 prefills + 1 latch", stored.Version, seeded.Version)
	}
	if len(stored.Verdicts) != 2 {
		t.Fatalf("stored verdicts = %+v, want BOTH accepted prefill writes kept after the close", stored.Verdicts)
	}
	for _, v := range stored.Verdicts {
		if v.Manual || !v.Correct {
			t.Fatalf("stored verdicts = %+v, want both OCR prefills (manual=false, correct)", stored.Verdicts)
		}
	}
	m.mu.Lock()
	pending := len(m.checkWrites)
	m.mu.Unlock()
	if pending != 0 {
		t.Fatalf("completed check-write admissions = %d, want 0", pending)
	}
}

// TestCloseFallbackVersusSubmit 是 1.12c 的验收：正式提交与关闭的三种顺
// 序，全部经真实保存链路（useRealFSRS：SubmitReview 走真实 pybridge 落进
// 本夹具的临时 SQLite，回执进同一库）核对持久结果——不只数 fake 调用。
//   - 提交先接纳（latch 写前关闭）：关闭等已接纳的提交落库，读权威行
//     submitted 后让位——只有提交自己的 kind=check 回执与 FSRS 批；
//   - 提交已落库、尚未返回（committed 窗口关闭）：同一让位语义，提交保
//     存与回执完整；
//   - 关闭先接纳：迟到的提交经生产入口被拒（零写入），兜底独占保存——
//     kind=unchecked_fallback 回执与降级 FSRS 批恰好各一。
func TestCloseFallbackVersusSubmit(t *testing.T) {
	receiptsOf := func(f *speechBrowserFixture) []lesson.Receipt {
		t.Helper()
		lessonID := lessonIDForCategory("speech-browser-fixture", time.Now(), lesson.CategoryChinese)
		got, err := f.receipts.ListByLesson(context.Background(), lessonID)
		if err != nil {
			t.Fatalf("list receipts: %v", err)
		}
		return got
	}
	reviewLogs := func(f *speechBrowserFixture) int {
		t.Helper()
		var n int
		if err := f.db.QueryRow(`SELECT COUNT(*) FROM review_logs`).Scan(&n); err != nil {
			t.Fatalf("count review_logs: %v", err)
		}
		return n
	}
	storedLesson := func(f *speechBrowserFixture) lesson.Lesson {
		t.Helper()
		lessonID := lessonIDForCategory("speech-browser-fixture", time.Now(), lesson.CategoryChinese)
		l, err := f.tools.GetLessonState(context.Background(), lessonID)
		if err != nil {
			t.Fatalf("read stored lesson: %v", err)
		}
		return l
	}

	t.Run("submit admitted before the close", func(t *testing.T) {
		f := newSpeechBrowserFixture(t)
		probe := f.useRealFSRS(t)
		f.seedCheckingWithManualVerdicts()
		before, _ := f.gateCheckSubmit(true, false)
		t.Cleanup(before.openUp)

		driver := f.runDriverAsync("check-submit-close", 45*time.Second)
		select {
		case <-before.entered:
		case <-time.After(30 * time.Second):
			t.Fatal("the formal submit never reached its pre-latch window")
		}
		m := f.waitFirstManager(5 * time.Second)
		// The goodbye close claims the fallback while the ACCEPTED submit is
		// parked ahead of its latch write; the fallback must wait for it,
		// then yield to the submitted row it reads back.
		m.handleEndSession(m.currentSender())
		before.openUp()
		res := <-driver
		if res.WSCloseCount != 1 || res.LateReviewResults != 0 || res.LateTeacherSentences != 0 {
			t.Fatalf("close=%d late review=%d late sentences=%d, want 1/0/0",
				res.WSCloseCount, res.LateReviewResults, res.LateTeacherSentences)
		}
		f.assertOriginalTasksExited()

		l := storedLesson(f)
		if l.Stage != lesson.StageSubmitted || l.Unchecked {
			t.Fatalf("stored stage=%q unchecked=%v, want the submit's submitted+checked", l.Stage, l.Unchecked)
		}
		if got := reviewLogs(f); got != 2 {
			t.Fatalf("review_logs rows = %d, want exactly the submit's 2-word batch", got)
		}
		if calls := probe.submitCalls(); calls != 1 {
			t.Fatalf("real review writes = %d, want exactly 1 (the submit's; the fallback yields)", calls)
		}
		recs := receiptsOf(f)
		if len(recs) != 1 || recs[0].Kind != lesson.ReceiptKindCheck {
			t.Fatalf("receipts = %+v, want exactly one kind=check (the submit's own)", recs)
		}
		m.mu.Lock()
		pending := len(m.checkWrites)
		m.mu.Unlock()
		if pending != 0 {
			t.Fatalf("completed check-write admissions = %d, want 0", pending)
		}
	})

	t.Run("submit committed, close before return", func(t *testing.T) {
		f := newSpeechBrowserFixture(t)
		probe := f.useRealFSRS(t)
		f.seedCheckingWithManualVerdicts()
		_, committed := f.gateCheckSubmit(false, true)
		t.Cleanup(committed.openUp)

		driver := f.runDriverAsync("check-submit-close", 45*time.Second)
		select {
		case <-committed.entered:
		case <-time.After(30 * time.Second):
			t.Fatal("the formal submit never committed its latch write")
		}
		m := f.waitFirstManager(5 * time.Second)
		m.handleEndSession(m.currentSender())
		committed.openUp()
		res := <-driver
		if res.WSCloseCount != 1 || res.LateReviewResults != 0 || res.LateTeacherSentences != 0 {
			t.Fatalf("close=%d late review=%d late sentences=%d, want 1/0/0",
				res.WSCloseCount, res.LateReviewResults, res.LateTeacherSentences)
		}
		f.assertOriginalTasksExited()

		l := storedLesson(f)
		if l.Stage != lesson.StageSubmitted || l.Unchecked {
			t.Fatalf("stored stage=%q unchecked=%v, want the submit's submitted+checked", l.Stage, l.Unchecked)
		}
		if got := reviewLogs(f); got != 2 {
			t.Fatalf("review_logs rows = %d, want exactly the submit's 2-word batch", got)
		}
		if calls := probe.submitCalls(); calls != 1 {
			t.Fatalf("real review writes = %d, want exactly 1", calls)
		}
		recs := receiptsOf(f)
		if len(recs) != 1 || recs[0].Kind != lesson.ReceiptKindCheck {
			t.Fatalf("receipts = %+v, want exactly one kind=check", recs)
		}
		m.mu.Lock()
		pending := len(m.checkWrites)
		m.mu.Unlock()
		if pending != 0 {
			t.Fatalf("completed check-write admissions = %d, want 0", pending)
		}
	})

	t.Run("close first: the late submit is refused, the fallback owns the save", func(t *testing.T) {
		f := newSpeechBrowserFixture(t)
		probe := f.useRealFSRS(t)
		f.seedCheckingWithOutcomes()
		entered, release := f.gateScreenVerdict()
		var releaseOnce sync.Once
		releaseGate := func() { releaseOnce.Do(func() { close(release) }) }
		t.Cleanup(releaseGate)

		driver := f.runDriverAsync("screen-verdict-close", 45*time.Second)
		select {
		case <-entered:
		case <-time.After(30 * time.Second):
			t.Fatal("the screen verdict did not reach the persistence gate")
		}
		m := f.waitFirstManager(5 * time.Second)
		m.handleEndSession(m.currentSender())
		releaseGate()
		res := <-driver
		if res.WSCloseCount != 1 || res.LateVerdictEchoes != 0 {
			t.Fatalf("close=%d late verdict echoes=%d, want 1/0", res.WSCloseCount, res.LateVerdictEchoes)
		}
		f.assertOriginalTasksExited()

		// A submit frame arriving past the sealed close rides the production
		// entry (the read loop's last-message equivalent): it must be
		// refused with zero persistence effects.
		m.HandleControl(m.currentSender(), mustJSON(t, CheckSubmitMsg{
			Type: TypeCheckSubmit, RequestID: "late-submit-after-close",
		}))

		l := storedLesson(f)
		if l.Stage != lesson.StageSubmitted || !l.Unchecked {
			t.Fatalf("stored stage=%q unchecked=%v, want the fallback's submitted+unchecked", l.Stage, l.Unchecked)
		}
		if got := reviewLogs(f); got != 2 {
			t.Fatalf("review_logs rows = %d, want exactly the fallback's 2-word degraded batch", got)
		}
		if calls := probe.submitCalls(); calls != 1 {
			t.Fatalf("real review writes = %d, want exactly 1 (the fallback's; the late submit writes nothing)", calls)
		}
		recs := receiptsOf(f)
		if len(recs) != 1 || recs[0].Kind != lesson.ReceiptKindUncheckedFallback {
			t.Fatalf("receipts = %+v, want exactly one kind=unchecked_fallback", recs)
		}
		m.mu.Lock()
		pending := len(m.checkWrites)
		m.mu.Unlock()
		if pending != 0 {
			t.Fatalf("completed check-write admissions = %d, want 0", pending)
		}
	})
}

// TestCloseSnapshotBrowser 是 1.13a 的验收：用自然播放夹具替换
// close_snapshot_atomic 的代 ACK 初始化，核对统一关闭声明的单临界区契
// 约在两种真实顺序下成立。
//   - 呈现胜出窗口：resume 的面板呈现（checking 置位、check_started 发
//     出）落在关闭的入口窗口里——声明的决定看到呈现后的状态，兜底仍被
//     拥有并恰好落一次，行 submitted+unchecked；
//   - 关闭胜出窗口：resume 呈现驻留在 IO 之后、绑定之前时关闭直接跑
//     完——声明看到 checking=false，无兜底、无呈现（check_started 与教
//     师句均为零），行保持 checking 供恢复，close 后无任何下行帧。
func TestCloseSnapshotBrowser(t *testing.T) {
	t.Run("presentation landing inside the close window still gets its fallback", func(t *testing.T) {
		f := newSpeechBrowserFixture(t)
		f.seedCheckingWithOutcomes()
		atResume, resumeProceed := make(chan struct{}), make(chan struct{})
		atCloseEntry, closeProceed := make(chan struct{}), make(chan struct{})
		var resumeOnce, closeOnce sync.Once
		f.mu.Lock()
		f.resumeHold = func() { resumeOnce.Do(func() { close(atResume) }); <-resumeProceed }
		f.closeEntryHold = func() { closeOnce.Do(func() { close(atCloseEntry) }); <-closeProceed }
		f.mu.Unlock()

		driver := f.runDriverAsync("resume-close-window", 45*time.Second)
		select {
		case <-atResume:
		case <-time.After(30 * time.Second):
			t.Fatal("the resume never reached its pre-presentation window")
		}
		m := f.waitFirstManager(5 * time.Second)
		// The goodbye parks inside the close entry (init-write lock held,
		// tm.mu not yet) while the presentation is still parked behind its
		// own window — the old two-lock snapshot window, driven through the
		// REAL resume path.
		closeDone := make(chan struct{})
		go func() {
			m.handleEndSession(m.currentSender())
			close(closeDone)
		}()
		select {
		case <-atCloseEntry:
		case <-time.After(10 * time.Second):
			t.Fatal("the close never entered its claim window")
		}
		// Release the presentation INTO the window: the tutor flips to
		// checking and check_started ships before the claim decides.
		close(resumeProceed)
		if !waitFor(t, 10*time.Second, func() bool {
			m.mu.Lock()
			defer m.mu.Unlock()
			return m.tutor != nil && m.tutor.checking
		}) {
			t.Fatal("the resume presentation never landed inside the close window")
		}
		close(closeProceed)
		select {
		case <-closeDone:
		case <-time.After(10 * time.Second):
			t.Fatal("the close never finished after the presentation")
		}
		res := <-driver
		if res.WSCloseCount != 1 || res.LateFramesAfterClose != 0 {
			t.Fatalf("close=%d late frames=%d, want 1/0", res.WSCloseCount, res.LateFramesAfterClose)
		}
		if res.CheckStartedBeforeClose != 1 || res.TeacherSentencesBeforeClose < 1 {
			t.Fatalf("check_started=%d teacher sentences=%d, want the presentation to have landed",
				res.CheckStartedBeforeClose, res.TeacherSentencesBeforeClose)
		}
		f.assertOriginalTasksExited()

		if !waitFor(t, 10*time.Second, func() bool { return len(f.words.submissions()) == 1 }) {
			t.Fatalf("fallback review batches = %d, want exactly one degraded batch", len(f.words.submissions()))
		}
		lessonID := lessonIDForCategory("speech-browser-fixture", time.Now(), lesson.CategoryChinese)
		stored, err := f.tools.GetLessonState(context.Background(), lessonID)
		if err != nil {
			t.Fatalf("read stored lesson: %v", err)
		}
		if stored.Stage != lesson.StageSubmitted || !stored.Unchecked {
			t.Fatalf("stored stage=%q unchecked=%v, want the post-presentation fallback's submitted+unchecked",
				stored.Stage, stored.Unchecked)
		}
		m.mu.Lock()
		pending := len(m.checkWrites)
		m.mu.Unlock()
		if pending != 0 {
			t.Fatalf("completed check-write admissions = %d, want 0", pending)
		}
	})

	t.Run("a close winning the window refuses the presentation and keeps checking", func(t *testing.T) {
		f := newSpeechBrowserFixture(t)
		f.seedChecking()
		atResume, resumeProceed := make(chan struct{}), make(chan struct{})
		var resumeOnce sync.Once
		f.mu.Lock()
		f.resumeHold = func() { resumeOnce.Do(func() { close(atResume) }); <-resumeProceed }
		f.mu.Unlock()

		driver := f.runDriverAsync("resume-close-window", 45*time.Second)
		select {
		case <-atResume:
		case <-time.After(30 * time.Second):
			t.Fatal("the resume never reached its pre-presentation window")
		}
		m := f.waitFirstManager(5 * time.Second)
		// The goodbye runs to completion while the presentation is still
		// parked: the claim saw no checking classroom, so no fallback exists
		// and the late presentation is refused outright.
		m.handleEndSession(m.currentSender())
		close(resumeProceed)
		res := <-driver
		if res.WSCloseCount != 1 || res.LateFramesAfterClose != 0 {
			t.Fatalf("close=%d late frames=%d, want 1/0", res.WSCloseCount, res.LateFramesAfterClose)
		}
		if res.CheckStartedBeforeClose != 0 || res.TeacherSentencesBeforeClose != 0 {
			t.Fatalf("check_started=%d teacher sentences=%d, want the refused presentation to ship nothing",
				res.CheckStartedBeforeClose, res.TeacherSentencesBeforeClose)
		}
		// The refused presentation never starts an announcement — that zero
		// IS the refusal's evidence, so only the connection tasks are
		// asserted here (assertOriginalTasksExited demands a settled
		// opening announcement this ordering never has).
		if !waitFor(t, 15*time.Second, func() bool {
			return f.handlerActive.Load() == 0 && f.handlerDone.Load() == 1 &&
				f.writerDone.Load() == 1 &&
				f.providerStarted.Load() == f.providerDone.Load() &&
				f.announcementRun.Load() == 0
		}) {
			t.Fatalf("connection tasks did not exit: handler=%d/%d writer=%d provider=%d/%d announcement=%d/%d",
				f.handlerActive.Load(), f.handlerDone.Load(), f.writerDone.Load(),
				f.providerStarted.Load(), f.providerDone.Load(),
				f.announcementRun.Load(), f.announcementDone.Load())
		}

		lessonID := lessonIDForCategory("speech-browser-fixture", time.Now(), lesson.CategoryChinese)
		stored, err := f.tools.GetLessonState(context.Background(), lessonID)
		if err != nil {
			t.Fatalf("read stored lesson: %v", err)
		}
		if stored.Stage != lesson.StageChecking || stored.Unchecked {
			t.Fatalf("stored stage=%q unchecked=%v, want checking for recovery", stored.Stage, stored.Unchecked)
		}
		if got := len(f.words.submissions()); got != 0 {
			t.Fatalf("review batches = %d, want 0 (no fallback without the check stage)", got)
		}
		m.mu.Lock()
		closedFlag, pending := m.closed, len(m.checkWrites)
		m.mu.Unlock()
		if !closedFlag || pending != 0 {
			t.Fatalf("closed=%v admissions=%d, want true/0", closedFlag, pending)
		}
	})
}

func (f *speechBrowserFixture) runDriverAsync(scenario string, timeout time.Duration, extraArgs ...string) <-chan speechDriverResult {
	f.t.Helper()
	done := make(chan speechDriverResult, 1)
	go func() {
		defer close(done)
		done <- f.runDriver(scenario, timeout, extraArgs...)
	}()
	return done
}

func (f *speechBrowserFixture) waitOneManager(timeout time.Duration) {
	f.t.Helper()
	f.waitFirstManager(timeout)
	f.mu.Lock()
	n := len(f.managers)
	f.mu.Unlock()
	if n != 1 {
		f.t.Fatalf("browser created %d managers, want the original connection only", n)
	}
}

func (f *speechBrowserFixture) assertOriginalTasksExited() {
	f.t.Helper()
	if !waitFor(f.t, 15*time.Second, func() bool {
		return f.handlerActive.Load() == 0 && f.handlerDone.Load() == 1 &&
			f.writerDone.Load() == 1 && f.providerStarted.Load() > 0 &&
			f.providerStarted.Load() == f.providerDone.Load() &&
			f.announcementRun.Load() > 0 && f.announcementRun.Load() == f.announcementDone.Load()
	}) {
		f.t.Fatalf("original tasks did not exit: handler=%d/%d writer=%d provider=%d/%d announcement=%d/%d",
			f.handlerActive.Load(), f.handlerDone.Load(), f.writerDone.Load(),
			f.providerStarted.Load(), f.providerDone.Load(),
			f.announcementRun.Load(), f.announcementDone.Load())
	}
}

// runDriverCheck executes the driver's --check self-check (计划步骤 3「辅助
// 器检查」)：机械验证驱动器没有代发 ACK／替服务器发消息／执行任意页面 JS
// 的接口，scenario 只是固定枚举。
func (f *speechBrowserFixture) runDriverCheck() {
	f.t.Helper()
	root := speechRepoRoot(f.t)
	driver := filepath.Join(root, "pkg", "rt", "testdata", "speech-browser", "driver.js")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var out, errOut bytes.Buffer
	cmd := exec.CommandContext(ctx, "node", driver, "--check")
	cmd.Stdout, cmd.Stderr = &out, &errOut
	if err := cmd.Run(); err != nil {
		f.t.Fatalf("driver --check run: %v\nstdout:\n%s\nstderr:\n%s", err, out.String(), errOut.String())
	}
	line := lastSpeechResultLine(out.String())
	if line == "" {
		f.t.Fatalf("driver --check produced no SPEECH_RESULT line\nstdout:\n%s", out.String())
	}
	var res struct {
		OK    bool `json:"ok"`
		Check struct {
			Scenarios  []string `json:"scenarios"`
			Violations []string `json:"violations"`
			Assertions struct {
				Valid                  bool `json:"valid"`
				MissingRejected        bool `json:"missingRejected"`
				EarlyListeningRejected bool `json:"earlyListeningRejected"`
			} `json:"observationAssertions"`
		} `json:"selfCheck"`
	}
	if err := json.Unmarshal([]byte(line), &res); err != nil {
		f.t.Fatalf("driver --check decode: %v\nline: %s", err, line)
	}
	if !res.OK {
		f.t.Fatalf("driver self-check violations: %v", res.Check.Violations)
	}
	if !res.Check.Assertions.Valid || !res.Check.Assertions.MissingRejected || !res.Check.Assertions.EarlyListeningRejected {
		f.t.Fatalf("driver observation self-check incomplete: %+v", res.Check.Assertions)
	}
	found := false
	for _, s := range res.Check.Scenarios {
		if s == "fixture" {
			found = true
		}
	}
	if !found {
		f.t.Fatalf("driver scenarios %v lack the fixture scenario", res.Check.Scenarios)
	}
}

// lastSpeechResultLine extracts the final "SPEECH_RESULT " JSON payload.
func lastSpeechResultLine(out string) string {
	trimmed := strings.TrimRight(out, "\n")
	if i := strings.LastIndex(trimmed, "\n"); i >= 0 {
		trimmed = trimmed[i+1:]
	}
	return strings.TrimPrefix(trimmed, "SPEECH_RESULT ")
}

// watchAnnouncementSettled polls the first connection's manager until the
// opening announcement settles through the browser's acks: the playback gate
// released AND the per-word timer armed (a send-completion shortcut never
// arms the timer; a timeout path closes instead). The channel fires at most
// once; it stays open on timeout — callers pair it with their own deadline.
func (f *speechBrowserFixture) watchAnnouncementSettled(timeout time.Duration) <-chan struct{} {
	ch := make(chan struct{})
	go func() {
		deadline := time.Now().Add(timeout)
		for time.Now().Before(deadline) {
			f.mu.Lock()
			var m *speechManagerTracker
			if len(f.managers) > 0 {
				m = f.managers[0]
			}
			f.mu.Unlock()
			if m != nil {
				m.TurnManager.mu.Lock()
				settled := !m.TurnManager.playbackPending && m.TurnManager.wordTimer != nil
				m.TurnManager.mu.Unlock()
				if settled {
					close(ch)
					return
				}
			}
			time.Sleep(10 * time.Millisecond)
		}
	}()
	return ch
}

// assertNaturalCompletion runs one fixture round and asserts the natural
// completion evidence: every sentence_ack followed its own native onended,
// zero dropped chunks, the synthetic mic adopted — together proving the
// read-only observer does not alter native playback.
func (f *speechBrowserFixture) assertNaturalCompletion() {
	f.t.Helper()
	res := f.runDriver("fixture", 45*time.Second)
	if !res.WSOpened {
		f.t.Fatal("the real page never opened its /rt WebSocket")
	}
	if len(res.PageAcks) == 0 {
		f.t.Fatal("the real page sent no sentence_ack (natural playback did not complete)")
	}
	for _, ack := range res.PageAcks {
		if !ack.PlayedBeforeAck {
			f.t.Fatalf("sentence_ack for %q (epoch %d) did not follow its native onended — trace %v",
				ack.SentenceID, ack.Epoch, res.Playback.Trace)
		}
	}
	if res.Playback.DroppedChunks != 0 {
		f.t.Fatalf("playback dropped %d chunks — the fixture PCM must play whole", res.Playback.DroppedChunks)
	}
}

// assertAllManagersClosed waits for every session to be released.
func (f *speechBrowserFixture) assertAllManagersClosed() {
	f.t.Helper()
	if !f.waitManagersClosed(15 * time.Second) {
		f.mu.Lock()
		n := len(f.managers)
		f.mu.Unlock()
		f.t.Fatalf("%d /rt session(s) still alive after the browser left", n)
	}
}

// TestSpeechBrowserFaultControls is 步骤 4 的验收（发送位置与队列消费两类
// 控制点）：控制点在真实执行位置命中（页面侧帧停滞 + Go 侧命中计数），
// 解除控制后任务正常退出（完成轮 + 无遗留会话），只读观察器不改变原生
// 播放（完成轮 playedBeforeAck 全真）。
func TestSpeechBrowserFaultControls(t *testing.T) {
	t.Run("send site intercept hits and clears", func(t *testing.T) {
		f := newSpeechBrowserFixture(t)

		held := make(chan struct{})
		var releaseOnce sync.Once
		release := func() { releaseOnce.Do(func() { close(held) }) }
		restore, hits := speechFaultSetSend(func(string, []byte) error {
			<-held
			return nil
		})
		t.Cleanup(release)
		t.Cleanup(restore)

		driver := f.runDriverAsync("held-fixture", 45*time.Second, "--hold-ms", "500")
		if !waitFor(t, 5*time.Second, func() bool { return hits() > 0 }) {
			t.Fatal("send-site control point never hit")
		}
		f.waitOneManager(5 * time.Second)
		release()
		restore()
		<-driver
		f.assertOriginalTasksExited()
	})

	t.Run("pump consumption gate hits and releases", func(t *testing.T) {
		f := newSpeechBrowserFixture(t)

		held := make(chan struct{})
		var releaseOnce sync.Once
		release := func() { releaseOnce.Do(func() { close(held) }) }
		restore, waits := speechFaultSetPumpGate(func() <-chan struct{} { return held })
		t.Cleanup(restore)

		driver := f.runDriverAsync("held-fixture", 45*time.Second, "--hold-ms", "500")
		if !waitFor(t, 5*time.Second, func() bool { return waits() > 0 }) {
			t.Fatal("pump consumption control point never hit")
		}
		f.waitOneManager(5 * time.Second)
		release()
		restore()
		<-driver
		f.assertOriginalTasksExited()
	})

	t.Run("tts hold starves audio only", func(t *testing.T) {
		f := newSpeechBrowserFixture(t)

		held := make(chan struct{})
		var releaseOnce sync.Once
		release := func() { releaseOnce.Do(func() { close(held) }) }
		gated := f.gateTTS(held)
		t.Cleanup(release)

		driver := f.runDriverAsync("held-fixture", 45*time.Second, "--hold-ms", "500")
		if !waitFor(t, 5*time.Second, func() bool { return gated.holdHits() > 0 }) {
			t.Fatal("provider-return control point never hit")
		}
		f.waitOneManager(5 * time.Second)
		release()
		<-driver
		f.assertOriginalTasksExited()
	})

	t.Run("timer factory seam records arms", func(t *testing.T) {
		f := newSpeechBrowserFixture(t)
		probe := f.useTimerProbe()

		f.assertNaturalCompletion()
		if probe.count() == 0 {
			t.Fatal("timing control point never hit — the word timer never armed through the factory")
		}
		f.assertOriginalTasksExited()
	})
}

// TestSpeechBrowserFixture is 步骤 2 的验收：真实生产页面能连上进程内真实
// /rt 服务端，自然完成一段非零 PCM 的教师播报（原生 onended → 真实
// sentence_ack，服务器侧门控解除、计时启动），销毁后无遗留连接与进程。
func TestSpeechBrowserFixture(t *testing.T) {
	f := newSpeechBrowserFixture(t)
	f.runDriverCheck()

	// The server-side settle evidence must be observed WHILE the page is
	// alive: handleSentenceAck only wakes the announcement goroutine, and
	// the page's departure (Close) suspends the word timer afterwards — a
	// post-hoc probe would race that cleanup. Watch concurrently instead.
	settled := f.watchAnnouncementSettled(60 * time.Second)

	res := f.runDriver("fixture", 45*time.Second)

	if !res.WSOpened {
		t.Fatal("the real page never opened its /rt WebSocket")
	}
	if res.DownBinaryFrames == 0 || res.DownBinaryBytes == 0 {
		t.Fatalf("page received no downlink PCM: frames=%d bytes=%d", res.DownBinaryFrames, res.DownBinaryBytes)
	}
	if len(res.PageAcks) == 0 {
		t.Fatal("the real page sent no sentence_ack (natural playback did not complete)")
	}
	// 步骤 3 验收：观察到原生播放结束和生产 ACK——每句 ACK 都必须由该句的
	// 原生 onended 先行（丢块空播 ACK 在 playedBeforeAck 上必为 false，开发
	// 期间已在零延迟 fakeTTS 的乱序时序下实测变红）。
	for _, ack := range res.PageAcks {
		if !ack.PlayedBeforeAck {
			t.Fatalf("sentence_ack for %q (epoch %d) did not follow its native onended — trace %v",
				ack.SentenceID, ack.Epoch, res.Playback.Trace)
		}
	}
	// 复用的合成麦克风被页面生产 capture 真实采用（fake-mic.js 工具的验收）。
	if len(res.Mic.Tracks) == 0 || res.Mic.Tracks[0].State != "live" {
		t.Fatalf("synthetic mic not adopted by the production capture: %+v", res.Mic)
	}

	// Server side: the browser's real acks reached the production handler
	// and settled the announcement (gate released + word timer armed while
	// the session was live).
	m := f.waitFirstManager(5 * time.Second)
	if got := m.ackCount(); got == 0 {
		t.Fatal("server saw no sentence_ack from the browser")
	}
	select {
	case <-settled:
	case <-time.After(5 * time.Second):
		t.Fatal("the opening announcement never settled through the browser's acks while the page was live")
	}

	// Teardown: the driver process already exited (runDriver waited for it);
	// the page's departure must close every server-side session.
	if !f.waitManagersClosed(15 * time.Second) {
		f.mu.Lock()
		n := len(f.managers)
		f.mu.Unlock()
		t.Fatalf("%d /rt session(s) still alive after the browser left", n)
	}
	f.close()
}
