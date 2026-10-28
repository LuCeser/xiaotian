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
	"github.com/xiaotian/home-tutor-agent/pkg/passage"
	"github.com/xiaotian/home-tutor-agent/pkg/poem"
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

	// correction-submit-close scenario field (1.10a re-proof): the verdict
	// taps the page actually sent.
	VerdictTaps int `json:"verdictTaps"`

	// practice-submit-close scenario field: the texts of any teacher
	// sentence that landed after the close.
	LateSentenceTexts []string `json:"lateSentenceTexts"`
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

// speechExecTracker sits at the BOTTOM of the fixture's executor chain (every
// gate mounts ABOVE it): it counts CompleteCheck entries and exits — checked
// submits of every classroom kind AND the unchecked close fallback — so a
// reclaim can observe the server-side latch tasks actually leaving the
// executor (review R0-P2: the driver's own result never proves that; the
// fallback can still be parked on a test gate when the driver returns).
type speechExecTracker struct {
	LessonExecutor
	entered atomic.Int64
	exited  atomic.Int64
}

func (t *speechExecTracker) CompleteCheck(ctx context.Context, lessonID string, expectedVersion int, requestID string, unchecked bool) (lesson.CompleteCheckResult, error) {
	t.entered.Add(1)
	res, err := t.LessonExecutor.CompleteCheck(ctx, lessonID, expectedVersion, requestID, unchecked)
	t.exited.Add(1)
	return res, err
}

// latchInFlight counts CompleteCheck calls that entered but have not returned
// (a submit or fallback parked on a gate counts as in flight).
func (t *speechExecTracker) latchInFlight() int64 {
	return t.entered.Load() - t.exited.Load()
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

	asr                  *fakeASR
	llm                  *fakeLLM
	tts                  TTSSynthesizer
	words                *fakeWordSource
	tools                *LessonTools
	exec                 LessonExecutor
	db                   *sql.DB
	dbPath               string
	receipts             *lesson.ReceiptStore
	wordsOverride        WordSource
	passages             PassageSource
	poems                PoemSource
	poemPracticeStore    *poem.PracticeStore
	passagePracticeStore *passage.PracticeStore
	practice             PracticeSource
	practiceStore        *lesson.PracticeStore
	expectedSentences    []string
	// expectedPCMPerSentence is the fake TTS's chunk count per sentence — the
	// driver's round observation demands EXACTLY that many PCM frames inside
	// every sentence window (缺块/乱序 both reject), so the fixture owns the
	// number it actually synthesizes.
	expectedPCMPerSentence int

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
	// likewise rides closeSessionAt's init-write-lock window. settleHold
	// parks an announcement between its valid final ack and the protected
	// settle (1.14a's probe).
	resumeHold     func()
	closeEntryHold func()
	settleHold     func()

	handlerActive, handlerDone        atomic.Int64
	writerDone                        atomic.Int64
	providerStarted, providerDone     atomic.Int64
	announcementRun, announcementDone atomic.Int64

	// execTrack is the fixture's bottom executor tracker (see its type) — the
	// completion observation reclaims wait on for parked latch tasks.
	execTrack *speechExecTracker
}

type speechObservedTTS struct {
	inner   TTSSynthesizer
	fixture *speechBrowserFixture
}

type speechVerdictGate struct {
	LessonExecutor
	gate *speechOneGate
}

func (g *speechVerdictGate) ApplyVerdict(ctx context.Context, lessonID string, expectedVersion int, requestID string, verdict lesson.CheckVerdict) (lesson.ApplyVerdictResult, error) {
	g.gate.hit()
	g.gate.wait()
	return g.LessonExecutor.ApplyVerdict(ctx, lessonID, expectedVersion, requestID, verdict)
}

// speechBatchGate gates a MULTI-write batch (an OCR prefill pass writes per
// word): every ApplyVerdict call parks on the one shared release, and the
// first entry signals entered exactly once.
type speechBatchGate struct {
	LessonExecutor
	gate *speechOneGate
}

func (g *speechBatchGate) ApplyVerdict(ctx context.Context, lessonID string, expectedVersion int, requestID string, verdict lesson.CheckVerdict) (lesson.ApplyVerdictResult, error) {
	g.gate.hit()
	g.gate.wait()
	return g.LessonExecutor.ApplyVerdict(ctx, lessonID, expectedVersion, requestID, verdict)
}

// speechHeldRead holds one OCR vision call: the reader parks before the
// scripted reply, so the prefill batch's ADMISSION happens only after the
// hold releases (the close-first ordering of TestCloseDuringOcrPrefills).
type speechHeldRead struct {
	inner *fakeOCRReader
	gate  *speechOneGate
}

func (h *speechHeldRead) read(ctx context.Context, systemPrompt, userPrompt string, imageJPEG []byte) (string, error) {
	h.gate.hit()
	h.gate.wait()
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
	execTrack := &speechExecTracker{LessonExecutor: tools}
	f := &speechBrowserFixture{
		t:                      t,
		db:                     db,
		dbPath:                 dbPath,
		tools:                  tools,
		exec:                   execTrack,
		execTrack:              execTrack,
		expectedSentences:      append([]string(nil), speechExpectedOpening...),
		expectedPCMPerSentence: 2,
		asr:                    newFakeASR(),
		llm:                    newFakeLLM(),
		// Two non-zero sawtooth chunks per sentence (~250ms each at 24kHz
		// s16le): audible-length PCM so real playback and onended are stable.
		// Zero inter-chunk delay: the fixture must NOT slow the supplier to
		// smuggle ordering past connWriter (路径 A 单元 1 门禁) — the wire
		// itself carries control and PCM in enqueue order (conn.go's single
		// outbound FIFO), and the driver's round observation rejects both a
		// missing chunk and a chunk outside its start/end window.
		tts: newFakeTTS(2, 12000),
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
	words, receipts, passages, poems := f.wordsOverride, f.receipts, f.passages, f.poems
	poemPractice, passagePractice := f.poemPracticeStore, f.passagePracticeStore
	practice, practiceStore := f.practice, f.practiceStore
	resumeHold, closeEntryHold, settleHold := f.resumeHold, f.closeEntryHold, f.settleHold
	f.mu.Unlock()
	if words == nil {
		words = f.words
	}
	tm := NewTurnManager(f.asr, f.llm, &speechObservedTTS{inner: tts, fixture: f}, executor, TurnManagerConfig{
		LessonID:        "speech-browser-fixture",
		Words:           words,
		Receipts:        receipts,
		Passages:        passages,
		Poems:           poems,
		PoemPractice:    poemPractice,
		PassagePractice: passagePractice,
		Practice:        practice,
		PracticeStore:   practiceStore,
	}, NewEventRecorder(infra.NewNoOpLogger(), "speech-browser"), infra.NewNoOpLogger())
	tm.checkResumeInterleave = resumeHold
	tm.closeEntryInterleave = closeEntryHold
	tm.settleInterleave = settleHold
	// S1: every manager observes its whole server tasks (submits, starters,
	// close fallbacks) — settled waits ride this, not the verified fields.
	tm.taskTrack = &serverTaskTracker{}
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

// seedCorrection closes a checking lesson through the REAL fallback shape
// (CompleteCheck with unchecked=true): the next reconnect reopens it as the
// post-hoc correction panel with derived (manual=false) verdicts — the
// classroom 1.10a's correction submit rides on a real browser connection.
func (f *speechBrowserFixture) seedCorrection() {
	f.t.Helper()
	f.seedCheckingWithOutcomes()
	ctx := context.Background()
	lessonID := lessonIDForCategory("speech-browser-fixture", time.Now(), lesson.CategoryChinese)
	l, err := f.tools.GetLessonState(ctx, lessonID)
	if err != nil {
		f.t.Fatalf("read seeded lesson: %v", err)
	}
	if _, err := f.tools.CompleteCheck(ctx, lessonID, l.Version, "browser-seed-fallback", true); err != nil {
		f.t.Fatalf("seed correction row: %v", err)
	}
	f.mu.Lock()
	// speakTurn splits on the exclamation mark — the expected list mirrors
	// the two server-side sentences (speechExpectedCheckResume's shape).
	f.expectedSentences = []string{
		"欢迎回来！",
		"上次没来得及检查，我们现在补上：屏幕上有 2 个词，老师先猜了一个，你来点「对」或「错」告诉老师真正的答案吧。",
	}
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

// speechReviewGate parks the FIRST SubmitReview after mounting — the
// corrections path's diff write (1.10a) rides cfg.Words, not the lesson
// executor, so its close-race control point sits here.
type speechReviewGate struct {
	WordSource
	gate *speechOneGate
	once sync.Once
}

func (g *speechReviewGate) SubmitReview(ctx context.Context, results map[string]string, category string) (ReviewWriteResult, error) {
	g.once.Do(func() {
		g.gate.hit()
		g.gate.wait()
	})
	return g.WordSource.SubmitReview(ctx, results, category)
}

// gateReviewWrite mounts the one-shot SubmitReview hold on every future
// manager's word source (the current override chain, fake or real).
func (f *speechBrowserFixture) gateReviewWrite() *speechOneGate {
	f.mu.Lock()
	defer f.mu.Unlock()
	inner := WordSource(f.words)
	if f.wordsOverride != nil {
		inner = f.wordsOverride
	}
	g := &speechReviewGate{WordSource: inner, gate: newSpeechOneGate()}
	f.wordsOverride = g
	return g.gate
}

// speechPassageSource is the fixture's passage content seam: one stored
// work with one range scope, answering everything else as not-found.
type speechPassageSource struct {
	content passage.Passage
}

func (s speechPassageSource) FetchPassage(ctx context.Context, passageID int64) (passage.Passage, error) {
	if passageID != s.content.ID {
		return passage.Passage{}, passage.ErrPassageNotFound
	}
	return s.content, nil
}

// seedPassage opens the passage classroom's inputs: the content source (the
// 测试课文 work, one range scope = paragraph 2) and the range's initial
// lesson row (mid-writing, nothing dictated yet) — a fresh page connect
// (?passage=77&scope=51) then re-announces the task. expectedSentences
// mirror speakTurn's sentence split of the greeting.
func (f *speechBrowserFixture) seedPassage() {
	f.t.Helper()
	f.mu.Lock()
	f.passages = speechPassageSource{content: passage.Passage{
		ID: 77, Title: "测试课文", Category: "chinese", InScope: true,
		Paragraphs: []string{"床前明月光。", "疑是地上霜。"},
		Scopes:     []passage.ScopeRow{{ID: 51, Paragraphs: []int{2}, Whole: false}},
	}}
	f.mu.Unlock()
	lessonID := resolvePassageLessonID(time.Now(), 77, 51)
	if _, err := f.tools.EnsurePassageLesson(context.Background(), lessonID, 77, 51, false,
		[]int{2}, []string{"疑是地上霜。"}, lesson.CategoryChinese); err != nil {
		f.t.Fatalf("seed passage lesson: %v", err)
	}
	f.mu.Lock()
	f.expectedSentences = []string{
		"今天的任务是把《测试课文》第 2 段默写下来。",
		"这次要写 1 个自然段，请凭记忆按顺序写在纸上，一段写一行。",
		"想不起来可以喊我提醒，写完了告诉我，我们对答案。",
	}
	f.mu.Unlock()
}

// speechPoemSource is the fixture's poem content seam (the passage twin).
type speechPoemSource struct {
	content poem.Poem
}

func (s speechPoemSource) FetchPoem(ctx context.Context, poemID int64) (poem.Poem, error) {
	if poemID != s.content.ID {
		return poem.Poem{}, poem.ErrPoemNotFound
	}
	return s.content, nil
}

// seedPoem opens the poem classroom's inputs: the content source (静夜思,
// two lines, in scope) and the attempt's initial lesson row. expected
// sentences mirror speakTurn's split of the greeting (poemGreetingText with
// total=2).
func (f *speechBrowserFixture) seedPoem() {
	f.t.Helper()
	f.mu.Lock()
	f.poems = speechPoemSource{content: poem.Poem{
		ID: 77, Title: "静夜思", Author: "李白", Category: "chinese", InScope: true,
		Lines: []string{"床前明月光，疑是地上霜。", "举头望明月，低头思故乡。"},
	}}
	f.mu.Unlock()
	lessonID := resolvePoemLessonID(time.Now(), 77)
	if _, err := f.tools.EnsurePoemLesson(context.Background(), lessonID, 77,
		[]string{"床前明月光，疑是地上霜。", "举头望明月，低头思故乡。"}); err != nil {
		f.t.Fatalf("seed poem lesson: %v", err)
	}
	f.mu.Lock()
	f.expectedSentences = []string{
		"今天的任务是把《静夜思》默写下来，是李白写的。",
		"这首诗一共有 2 句，请凭记忆把整首诗写在纸上，一句写一行。",
		"想不起来可以喊我提醒，写完了告诉我，我们对答案。",
	}
	f.mu.Unlock()
}

// speechPracticeSource is the fixture's practice vocabulary seam: a fixed
// card list the picker samples from (minus today's dictated words).
type speechPracticeSource struct {
	cards []database.Card
}

func (s speechPracticeSource) List(ctx context.Context, filter database.CardFilter) ([]database.Card, error) {
	return s.cards, nil
}

// seedCompletedLesson closes the daily word lesson through the real
// CompleteCheck (submitted+checked): a fresh connect short-circuits to
// session_ended(lesson_complete) whose banner carries the 再练一组 entry.
func (f *speechBrowserFixture) seedCompletedLesson() {
	f.t.Helper()
	f.seedCheckingWithManualVerdicts()
	ctx := context.Background()
	lessonID := lessonIDForCategory("speech-browser-fixture", time.Now(), lesson.CategoryChinese)
	l, err := f.tools.GetLessonState(ctx, lessonID)
	if err != nil {
		f.t.Fatalf("read seeded lesson: %v", err)
	}
	if _, err := f.tools.CompleteCheck(ctx, lessonID, l.Version, "browser-seed-complete", false); err != nil {
		f.t.Fatalf("seed completed lesson: %v", err)
	}
}

// armPoemPassageSaveChain wires poem/passage practice stores and save
// receipts on this fixture's temp SQLite — the production submit paths for
// 1.10c/d, not the degraded zero-saved branch.
func (f *speechBrowserFixture) armPoemPassageSaveChain() {
	f.t.Helper()
	ctx := context.Background()
	if err := poem.Migrate(ctx, f.db); err != nil {
		f.t.Fatalf("poem migrate: %v", err)
	}
	if err := passage.Migrate(ctx, f.db); err != nil {
		f.t.Fatalf("passage migrate: %v", err)
	}
	poemStore, err := poem.NewPracticeStore(f.db, infra.NewNoOpLogger())
	if err != nil {
		f.t.Fatalf("poem practice store: %v", err)
	}
	passageStore, err := passage.NewPracticeStore(f.db, infra.NewNoOpLogger())
	if err != nil {
		f.t.Fatalf("passage practice store: %v", err)
	}
	receipts, err := lesson.NewReceiptStore(f.db, infra.NewNoOpLogger())
	if err != nil {
		f.t.Fatalf("receipt store: %v", err)
	}
	f.mu.Lock()
	f.poemPracticeStore = poemStore
	f.passagePracticeStore = passageStore
	f.receipts = receipts
	f.mu.Unlock()
}

// usePractice arms the practice inputs on this fixture: two fresh practice
// cards (never today's dictated words) plus the REAL practice store on the
// same temp SQLite.
func (f *speechBrowserFixture) usePractice() {
	f.t.Helper()
	store, err := lesson.NewPracticeStore(f.db, infra.NewNoOpLogger())
	if err != nil {
		f.t.Fatalf("practice store: %v", err)
	}
	f.mu.Lock()
	f.practice = speechPracticeSource{cards: []database.Card{
		{Word: "练习甲", Meaning: "加练第一词", Category: "chinese"},
		{Word: "练习乙", Meaning: "加练第二词", Category: "chinese"},
	}}
	f.practiceStore = store
	f.mu.Unlock()
}

// scriptSpokenConfirm arms ONE spoken 写完了 turn: the synthetic tone rides
// the production capture into the real VAD, the fake ASR answers 好了, and
// the fake LLM classifies it to confirm.
func (f *speechBrowserFixture) scriptSpokenConfirm() {
	f.t.Helper()
	f.asr.mu.Lock()
	f.asr.script = append(f.asr.script, asrScript{text: "好了"})
	f.asr.mu.Unlock()
	inner := fmt.Sprintf(`{\"intent\":\"%s\",\"reply\":\"\",\"index\":0}`, intentConfirm)
	f.llm.mu.Lock()
	f.llm.script = append(f.llm.script, llmScript{
		wire: fmt.Sprintf(`{"choices":[{"message":{"content":"%s"}}]}`, inner),
	})
	f.llm.mu.Unlock()
}

func (f *speechBrowserFixture) gateScreenVerdict() *speechOneGate {
	f.mu.Lock()
	defer f.mu.Unlock()
	g := &speechVerdictGate{LessonExecutor: f.exec, gate: newSpeechOneGate()}
	f.exec = g
	return g.gate
}

// gateOcrPrefillWrites gates a whole prefill BATCH: every ApplyVerdict parks
// on the shared release, first entry signals once.
func (f *speechBrowserFixture) gateOcrPrefillWrites() *speechOneGate {
	f.mu.Lock()
	defer f.mu.Unlock()
	g := &speechBatchGate{LessonExecutor: f.exec, gate: newSpeechOneGate()}
	f.exec = g
	return g.gate
}

// speechOneGate is one reusable hit/release control point: the first hit
// signals entered once, every waiter parks on the same release, and hit and
// release are STRICTLY separated (计划 §5.1) — release() never closes
// entered, because releasing an unh-it control point must not fabricate the
// hit observation. Both are idempotent so a t.Cleanup can pair with an
// explicit call; hits counts every arrival (the hit observation) atomically.
type speechOneGate struct {
	entered  chan struct{}
	released chan struct{}
	hits     atomic.Int64
	hitOnce  sync.Once
	openOnce sync.Once
}

func newSpeechOneGate() *speechOneGate {
	return &speechOneGate{entered: make(chan struct{}), released: make(chan struct{})}
}

func (g *speechOneGate) hit() {
	g.hits.Add(1)
	g.hitOnce.Do(func() { close(g.entered) })
}
func (g *speechOneGate) wait()           { <-g.released }
func (g *speechOneGate) hitCount() int64 { return g.hits.Load() }
func (g *speechOneGate) release() {
	g.openOnce.Do(func() { close(g.released) })
}

// speechChHit waits (bounded) for one hit observation and returns it — it
// never releases and never Fatals: the timeout only bounds the OBSERVATION
// (计划 §5.1), the caller owns the reclaim-then-assert order.
func speechChHit(entered <-chan struct{}, timeout time.Duration) bool {
	select {
	case <-entered:
		return true
	case <-time.After(timeout):
		return false
	}
}

// speechGateHit is speechChHit for a mounted control point.
func speechGateHit(g *speechOneGate, timeout time.Duration) bool {
	return speechChHit(g.entered, timeout)
}

const speechReclaimBound = 90 * time.Second

// speechConnectionTasksSettled mirrors assertOriginalTasksExited's counters
// without Fatalling — the observation half of §5.1 (single-connection shape:
// exactly one handler/writer, one settled opening announcement).
func (f *speechBrowserFixture) speechConnectionTasksSettled() bool {
	return f.handlerActive.Load() == 0 && f.handlerDone.Load() == 1 &&
		f.writerDone.Load() == 1 && f.providerStarted.Load() > 0 &&
		f.providerStarted.Load() == f.providerDone.Load() &&
		f.announcementRun.Load() > 0 && f.announcementRun.Load() == f.announcementDone.Load()
}

// speechConnectionsSettled is the multi-connection form (the practice journey
// rides two connections): every handler/writer that ran has exited and no
// provider or announcement run is left pending.
func (f *speechBrowserFixture) speechConnectionsSettled() bool {
	return f.handlerActive.Load() == 0 && f.handlerDone.Load() >= 1 &&
		f.writerDone.Load() >= 1 &&
		f.providerStarted.Load() == f.providerDone.Load() &&
		f.announcementRun.Load() == f.announcementDone.Load()
}

// speechServerTasksSettled reports whether every SERVER-side task of the
// fixture has finished: the browser connections (multi-connection form),
// every CompleteCheck latch call still inside the executor (the fixture's
// bottom tracker), and — the S1 whole-task observation — every server task
// registered before its goroutine launched: the accepted submits of all
// kinds, the init-continuation starters and BOTH close-fallback launches
// (their handles are reserved by the close claim itself). A task parked
// BEFORE its executor call counts as live, and a replaced classroom's task
// stays registered on its connection until it truly exits — the old
// activeScope.formalSubmit read missed both (第三轮第 4/5 项). The driver's
// own exit is observed separately by the caller.
func (f *speechBrowserFixture) speechServerTasksSettled() bool {
	if !f.speechConnectionsSettled() {
		return false
	}
	if f.execTrack != nil && f.execTrack.latchInFlight() != 0 {
		return false
	}
	f.mu.Lock()
	mgrs := append([]*speechManagerTracker(nil), f.managers...)
	f.mu.Unlock()
	for _, m := range mgrs {
		if !m.TurnManager.serverTasksSettled() {
			return false
		}
	}
	return true
}

func (f *speechBrowserFixture) recvSpeechDriver(driver <-chan speechDriverResult, timeout time.Duration) (speechDriverResult, bool) {
	f.t.Helper()
	if driver == nil {
		return speechDriverResult{}, false
	}
	select {
	case res, ok := <-driver:
		return res, ok
	case <-time.After(timeout):
		return speechDriverResult{}, false
	}
}

// reclaimAndFail is the failure half of §5.1: release EVERY held control
// point, wait (bounded) for server tasks and the driver goroutine, and only
// then fail carrying both observations. t.Cleanup stays the last resort — it
// must never replace this reclaim-before-assert.
func (f *speechBrowserFixture) reclaimAndFail(driver <-chan speechDriverResult, releases []func(), format string, args ...any) {
	f.t.Helper()
	for _, release := range releases {
		if release != nil {
			release()
		}
	}
	tasksOK := waitFor(f.t, speechReclaimBound, f.speechServerTasksSettled)
	res, driverGot := f.recvSpeechDriver(driver, speechReclaimBound)
	ok, errMsg := false, "no driver result within the reclaim bound"
	if driverGot {
		ok, errMsg = res.OK, res.Err
		if !ok && errMsg == "" {
			errMsg = "driver reported failure"
		}
	} else if driver != nil {
		errMsg = "the driver channel closed without a result"
	}
	f.t.Fatalf(format+" [reclaimed; server_settled=%v driver_ok=%v driver_err=%v]",
		append(args, tasksOK, ok, errMsg)...)
}

// speechSubmitGate wraps the executor's checked CompleteCheck at the two
// formal-submit control points (1.12a's window set, installed for 1.09's
// orderings): before parks AHEAD of the real latch write; committed parks
// AFTER the real write succeeded but BEFORE its result returns to the
// manager. The unchecked close fallback stays transparent.
type speechSubmitGate struct {
	LessonExecutor
	before          *speechOneGate
	committed       *speechOneGate
	uncheckedBefore *speechOneGate
}

func (g *speechSubmitGate) CompleteCheck(ctx context.Context, lessonID string, expectedVersion int, requestID string, unchecked bool) (lesson.CompleteCheckResult, error) {
	if unchecked && g.uncheckedBefore != nil {
		g.uncheckedBefore.hit()
		g.uncheckedBefore.wait()
	}
	if !unchecked && g.before != nil {
		g.before.hit()
		g.before.wait()
	}
	res, err := g.LessonExecutor.CompleteCheck(ctx, lessonID, expectedVersion, requestID, unchecked)
	if !unchecked && err == nil && g.committed != nil {
		g.committed.hit()
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

// gateUncheckedCompleteCheck parks the D-03 fallback's unchecked CompleteCheck
// until release — so a late formal submit probe can run before the fallback
// latch lands (passage classrooms only; poems skip the fallback).
func (f *speechBrowserFixture) gateUncheckedCompleteCheck() *speechOneGate {
	f.mu.Lock()
	defer f.mu.Unlock()
	g, ok := f.exec.(*speechSubmitGate)
	if !ok {
		g = &speechSubmitGate{LessonExecutor: f.exec}
		f.exec = g
	}
	g.uncheckedBefore = newSpeechOneGate()
	return g.uncheckedBefore
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
func (f *speechBrowserFixture) holdPhotoOCR(replies ...string) *speechOneGate {
	f.mu.Lock()
	defer f.mu.Unlock()
	h := &speechHeldRead{inner: &fakeOCRReader{replies: replies}, gate: newSpeechOneGate()}
	f.ocrRead = h.read
	return h.gate
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
	if m := f.tryFirstManager(timeout); m != nil {
		return m
	}
	f.t.Fatal("no /rt connection was created before the deadline")
	return nil
}

// tryFirstManager is waitFirstManager's observation half (§5.1): it returns
// nil on timeout instead of Fataling, so callers that still hold a control
// point or a running driver can reclaim FIRST and fail carrying both
// observations. Every post-runDriverAsync call site uses this shape.
func (f *speechBrowserFixture) tryFirstManager(timeout time.Duration) *speechManagerTracker {
	f.t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		f.mu.Lock()
		var m *speechManagerTracker
		if len(f.managers) > 0 {
			m = f.managers[0]
		}
		f.mu.Unlock()
		if m != nil {
			return m
		}
		time.Sleep(20 * time.Millisecond)
	}
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

// execSpeechDriver runs driver.js and parses SPEECH_RESULT without Fatalling —
// runDriverAsync reports failures on the result channel for §5.1 reclaim.
func (f *speechBrowserFixture) execSpeechDriver(scenario string, timeout time.Duration, extraArgs ...string) (speechDriverResult, string, error) {
	f.t.Helper()
	root := speechRepoRoot(f.t)
	driverPath := filepath.Join(root, "pkg", "rt", "testdata", "speech-browser", "driver.js")

	ctx, cancel := context.WithTimeout(context.Background(), timeout+15*time.Second)
	defer cancel()
	args := append([]string{
		driverPath,
		"--url", f.url() + "tutor/",
		"--scenario", scenario,
		"--timeout-ms", fmt.Sprint(int64(timeout / time.Millisecond)),
	}, extraArgs...)
	if scenario == "fixture" || scenario == "held-fixture" || scenario == "close-after-opening" ||
		scenario == "screen-verdict-close" || scenario == "voice-verdict-close" ||
		scenario == "ocr-prefill-close" || scenario == "check-submit-close" ||
		scenario == "correction-submit-close" || scenario == "passage-submit-close" ||
		scenario == "poem-submit-close" {
		f.mu.Lock()
		sentences := append([]string(nil), f.expectedSentences...)
		pcm := f.expectedPCMPerSentence
		f.mu.Unlock()
		expected, err := json.Marshal(sentences)
		if err != nil {
			return speechDriverResult{}, "", fmt.Errorf("encode expected opening: %w", err)
		}
		args = append(args, "--expected-sentences-json", string(expected))
		args = append(args, "--expected-pcm-per-sentence", fmt.Sprint(pcm))
	}
	cmd := exec.CommandContext(ctx, "node", args...)
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	runErr := cmd.Run()
	stderr := errOut.String()

	var res speechDriverResult
	line := lastSpeechResultLine(out.String())
	if line == "" {
		return res, stderr, fmt.Errorf("driver produced no SPEECH_RESULT line (run err=%v)\nstdout:\n%s\nstderr:\n%s",
			runErr, out.String(), stderr)
	}
	if err := json.Unmarshal([]byte(line), &res); err != nil {
		return res, stderr, fmt.Errorf("driver result decode: %v\nline: %s", err, line)
	}
	if runErr != nil && res.OK {
		return res, stderr, fmt.Errorf("driver exited with error %v but reported ok", runErr)
	}
	if !res.OK {
		return res, stderr, fmt.Errorf("driver scenario %q failed: %s", scenario, res.Err)
	}
	return res, stderr, nil
}

// runDriver spawns the Node browser driver and returns its parsed result.
// extraArgs append verbatim (e.g. "--hold-ms", "3000" for starve-probe).
// A failed scenario comes back as speechDriverResult{OK:false, Err:...}
// with the driver's combined output attached for diagnosis.
func (f *speechBrowserFixture) runDriver(scenario string, timeout time.Duration, extraArgs ...string) speechDriverResult {
	f.t.Helper()
	res, stderr, err := f.execSpeechDriver(scenario, timeout, extraArgs...)
	if err != nil {
		if stderr != "" {
			f.t.Fatalf("%v\nstderr:\n%s", err, stderr)
		}
		f.t.Fatal(err)
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
	if !speechChHit(settled, 10*time.Second) {
		f.reclaimAndFail(driver, nil, "opening did not settle before the close test")
	}
	m := f.tryFirstManager(5 * time.Second)
	if m == nil {
		f.reclaimAndFail(driver, nil, "no /rt connection was created before the deadline")
	}
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
	verdictGate := f.gateScreenVerdict()
	t.Cleanup(verdictGate.release)

	driver := f.runDriverAsync("screen-verdict-close", 45*time.Second)
	if !speechGateHit(verdictGate, 10*time.Second) {
		f.reclaimAndFail(driver, []func(){verdictGate.release}, "screen verdict did not reach the real persistence gate")
	}
	m := f.tryFirstManager(5 * time.Second)
	if m == nil {
		f.reclaimAndFail(driver, []func(){verdictGate.release}, "no /rt connection was created before the deadline")
	}
	if !m.tryTerminalClose(m.currentSender(), ReasonClientEnd) {
		f.reclaimAndFail(driver, []func(){verdictGate.release}, "test close did not obtain session ownership")
	}
	verdictGate.release()
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
	m.TurnManager.mu.Lock()
	pending := len(m.checkWrites)
	m.TurnManager.mu.Unlock()
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
	verdictGate := f.gateScreenVerdict()
	t.Cleanup(verdictGate.release)

	driver := f.runDriverAsync("voice-verdict-close", 45*time.Second)
	if !speechGateHit(verdictGate, 30*time.Second) {
		f.reclaimAndFail(driver, []func(){verdictGate.release}, "spoken verdict did not reach the real persistence gate — the natural voice turn never landed")
	}
	m := f.tryFirstManager(5 * time.Second)
	if m == nil {
		f.reclaimAndFail(driver, []func(){verdictGate.release}, "no /rt connection was created before the deadline")
	}
	if !m.tryTerminalClose(m.currentSender(), ReasonClientEnd) {
		f.reclaimAndFail(driver, []func(){verdictGate.release}, "test close did not obtain session ownership")
	}
	verdictGate.release()
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
	m.TurnManager.mu.Lock()
	pending := len(m.checkWrites)
	m.TurnManager.mu.Unlock()
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
		writesGate := f.gateOcrPrefillWrites()
		t.Cleanup(writesGate.release)

		driver := f.runDriverAsync("ocr-prefill-close", 45*time.Second)
		if !speechGateHit(writesGate, 30*time.Second) {
			f.reclaimAndFail(driver, []func(){writesGate.release}, "OCR prefill batch did not reach the first real persistence gate")
		}
		m := f.tryFirstManager(5 * time.Second)
		if m == nil {
			f.reclaimAndFail(driver, []func(){writesGate.release}, "no /rt connection was created before the deadline")
		}

		// A cross-connection manual verdict lands while the batch's first
		// write parks (another tab's tap — §七 multi-connection rule): the
		// stored truth must keep it and drop the word's prefill.
		lessonID := lessonIDForCategory("speech-browser-fixture", time.Now(), lesson.CategoryChinese)
		l, err := f.tools.GetLessonState(context.Background(), lessonID)
		if err != nil {
			f.reclaimAndFail(driver, []func(){writesGate.release}, "read lesson state: %v", err)
		}
		if _, err := f.tools.ApplyVerdict(context.Background(), lessonID, l.Version, "cross-tab-manual-1",
			lesson.CheckVerdict{WordIndex: 0, Correct: true, Manual: true}); err != nil {
			f.reclaimAndFail(driver, []func(){writesGate.release}, "cross-connection manual verdict: %v", err)
		}
		if !m.tryTerminalClose(m.currentSender(), ReasonClientEnd) {
			f.reclaimAndFail(driver, []func(){writesGate.release}, "test close did not obtain session ownership")
		}
		writesGate.release()
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
		m.TurnManager.mu.Lock()
		pending := len(m.checkWrites)
		m.TurnManager.mu.Unlock()
		if pending != 0 {
			t.Fatalf("completed check-write admissions = %d, want 0", pending)
		}
	})

	t.Run("batch arriving after close writes nothing", func(t *testing.T) {
		f := newSpeechBrowserFixture(t)
		f.seedChecking()
		ocrGate := f.holdPhotoOCR(prefillReplies...)
		t.Cleanup(ocrGate.release)

		driver := f.runDriverAsync("ocr-prefill-close", 45*time.Second)
		if !speechGateHit(ocrGate, 30*time.Second) {
			f.reclaimAndFail(driver, []func(){ocrGate.release}, "the real OCR vision call never started")
		}
		m := f.tryFirstManager(5 * time.Second)
		if m == nil {
			f.reclaimAndFail(driver, []func(){ocrGate.release}, "no /rt connection was created before the deadline")
		}
		if !m.tryTerminalClose(m.currentSender(), ReasonClientEnd) {
			f.reclaimAndFail(driver, []func(){ocrGate.release}, "test close did not obtain session ownership")
		}
		ocrGate.release()
		res := <-driver
		if res.WSCloseCount != 1 || res.LateVerdictEchoes != 0 || res.LateOcrResults != 0 {
			t.Fatalf("close=%d late echoes=%d ocr=%d, want 1/0/0",
				res.WSCloseCount, res.LateVerdictEchoes, res.LateOcrResults)
		}
		f.assertOriginalTasksExited()

		if verdicts := storedVerdicts(f); len(verdicts) != 0 {
			t.Fatalf("stored verdicts = %+v, want zero writes — a batch arriving after close is not admitted", verdicts)
		}
		m.TurnManager.mu.Lock()
		pending := len(m.checkWrites)
		m.TurnManager.mu.Unlock()
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
		m.TurnManager.mu.Lock()
		pending := len(m.checkWrites)
		m.TurnManager.mu.Unlock()
		if pending != 0 {
			t.Fatalf("completed check-write admissions = %d, want 0 (released before the announcement)", pending)
		}
	}

	t.Run("close before the actual commit", func(t *testing.T) {
		f := newSpeechBrowserFixture(t)
		f.seedCheckingWithManualVerdicts()
		before, _ := f.gateCheckSubmit(true, false)
		t.Cleanup(before.release)

		driver := f.runDriverAsync("check-submit-close", 45*time.Second)
		if !speechGateHit(before, 30*time.Second) {
			f.reclaimAndFail(driver, []func(){before.release}, "the formal submit never reached the real latch write")
		}
		m := f.tryFirstManager(5 * time.Second)
		if m == nil {
			f.reclaimAndFail(driver, []func(){before.release}, "no /rt connection was created before the deadline")
		}
		closeOK := m.tryTerminalClose(m.currentSender(), ReasonClientEnd)
		// T6 observation, non-Fatal on purpose (§5.1: record the observation
		// while the gate is still held, assert only after the reclaim): the
		// original connection's read loop must already be free while the
		// accepted save is parked — reverting the submit onto the read loop
		// (go → sync) parks it inside CompleteCheck and this turns red.
		exitedWhileParked := waitFor(t, 15*time.Second, f.speechConnectionTasksSettled)
		before.release()
		if !waitFor(t, speechReclaimBound, f.speechServerTasksSettled) {
			f.reclaimAndFail(driver, nil, "server tasks did not settle after releasing the submit gate")
		}
		res, got := f.recvSpeechDriver(driver, speechReclaimBound)
		if !got {
			f.reclaimAndFail(driver, nil, "driver never returned after reclaim")
		}
		if !closeOK {
			t.Fatal("test close did not obtain session ownership")
		}
		if !exitedWhileParked {
			t.Fatal("the original connection did not exit while the accepted submit was parked — the save must not ride the read loop")
		}
		assertOutcome(t, f, m, res)
	})

	t.Run("close after commit before return", func(t *testing.T) {
		f := newSpeechBrowserFixture(t)
		f.seedCheckingWithManualVerdicts()
		_, committed := f.gateCheckSubmit(false, true)
		t.Cleanup(committed.release)

		driver := f.runDriverAsync("check-submit-close", 45*time.Second)
		if !speechGateHit(committed, 30*time.Second) {
			f.reclaimAndFail(driver, []func(){committed.release}, "the formal submit never committed its latch write")
		}
		m := f.tryFirstManager(5 * time.Second)
		if m == nil {
			f.reclaimAndFail(driver, []func(){committed.release}, "no /rt connection was created before the deadline")
		}
		closeOK := m.tryTerminalClose(m.currentSender(), ReasonClientEnd)
		// Same T6 observation as the first ordering: the read loop must be
		// free while the committed window is parked.
		exitedWhileParked := waitFor(t, 15*time.Second, f.speechConnectionTasksSettled)
		committed.release()
		if !waitFor(t, speechReclaimBound, f.speechServerTasksSettled) {
			f.reclaimAndFail(driver, nil, "server tasks did not settle after releasing the submit gate")
		}
		res, got := f.recvSpeechDriver(driver, speechReclaimBound)
		if !got {
			f.reclaimAndFail(driver, nil, "driver never returned after reclaim")
		}
		if !closeOK {
			t.Fatal("test close did not obtain session ownership")
		}
		if !exitedWhileParked {
			t.Fatal("the original connection did not exit while the committed submit was parked — the save must not ride the read loop")
		}
		assertOutcome(t, f, m, res)
	})

	t.Run("connection exits while persist is still parked", func(t *testing.T) {
		f := newSpeechBrowserFixture(t)
		f.seedCheckingWithManualVerdicts()
		before, _ := f.gateCheckSubmit(true, false)
		t.Cleanup(before.release)

		driver := f.runDriverAsync("check-submit-close", 45*time.Second)
		persistReached := speechGateHit(before, 30*time.Second)
		m := f.tryFirstManager(5 * time.Second)
		if m == nil {
			f.reclaimAndFail(driver, []func(){before.release}, "no /rt connection was created before the deadline")
		}
		closeOK := false
		if persistReached {
			closeOK = m.tryTerminalClose(m.currentSender(), ReasonClientEnd)
		}
		// Record the read-loop-exit observation BEFORE releasing the persist
		// gate (non-Fatal), then release and reclaim — a synchronous latch
		// write in the read loop cannot pass this window while the gate stays
		// closed; the assert happens only after every task was reclaimed.
		exitedWhileParked := waitFor(t, 15*time.Second, f.speechConnectionTasksSettled)
		before.release()
		if !waitFor(t, speechReclaimBound, f.speechServerTasksSettled) {
			f.reclaimAndFail(driver, nil, "server tasks did not settle after releasing the persist gate")
		}
		res, got := f.recvSpeechDriver(driver, speechReclaimBound)
		if !got {
			f.reclaimAndFail(driver, nil, "driver never returned after reclaim")
		}
		if !persistReached {
			t.Fatal("the formal submit never reached the parked latch write")
		}
		if !closeOK {
			t.Fatal("test close did not obtain session ownership")
		}
		if !exitedWhileParked {
			t.Fatal("the original connection did not exit while the persist was parked — the save must not ride the read loop")
		}
		assertOutcome(t, f, m, res)
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
	verdictGate := f.gateScreenVerdict()
	t.Cleanup(verdictGate.release)

	driver := f.runDriverAsync("screen-verdict-close", 45*time.Second)
	if !speechGateHit(verdictGate, 30*time.Second) {
		f.reclaimAndFail(driver, []func(){verdictGate.release}, "the screen verdict did not reach the real persistence gate")
	}
	m := f.tryFirstManager(5 * time.Second)
	if m == nil {
		f.reclaimAndFail(driver, []func(){verdictGate.release}, "no /rt connection was created before the deadline")
	}
	// The goodbye close claims the fallback while the accepted verdict write
	// is parked AHEAD of the real SQLite write.
	m.handleEndSession(m.currentSender())
	verdictGate.release()
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
	m.TurnManager.mu.Lock()
	pending := len(m.checkWrites)
	m.TurnManager.mu.Unlock()
	if pending != 0 {
		t.Fatalf("completed check-write admissions = %d, want 0", pending)
	}
}

// TestCloseFallbackMultipleWrites 是 1.12b 的验收：两笔已接纳但尚未提交的
// 判定（一个 OCR 预填批次内的两笔逐词写入，1.06 纪律下由一次登记覆盖）与
// 关闭竞争——兜底先在锁外等批次完成，再读权威行：两笔写入已把版本连续推
// 进两次，兜底用权威版本竞争一次成功，行 submitted+unchecked、两笔预填保
// 留、FSRS 只有一批降级评分、关闭后没有迟到回显。
//
// 会合顺序（复盘第 5 节）：读门先 hold、再关闭，然后在写入闸仍关着时等读
// 门 entered——只有撤掉 waitCheckWrites 的实现才会在这个窗口内到达权威读
// （读到落库前的旧版本并闩住）；测试随即逼这个未等待的兜底先赢闩、再放行
// 两笔写入，最终以版本/verdict 断言变红。有等待的实现走到另一条路：先放
// 写入、再等 entered、再放读门，同一组断言变绿。「先 release 再等 entered」
// 的旧会合会让 release 自己点亮 entered，既挡不住未等待的读、也会假绿。
func TestCloseFallbackMultipleWrites(t *testing.T) {
	f := newSpeechBrowserFixture(t)
	f.seedCheckingWithOutcomes()
	f.armPhotoOCR(`[{"text":"听写浏览器","confidence":0.95},{"text":"第二词","confidence":0.95}]`)
	verdictGate := f.gateOcrPrefillWrites() // the batch's per-word ApplyVerdict hold
	readGate := f.gateStateRead()
	t.Cleanup(verdictGate.release)

	lessonID := lessonIDForCategory("speech-browser-fixture", time.Now(), lesson.CategoryChinese)
	seeded, err := f.tools.GetLessonState(context.Background(), lessonID)
	if err != nil {
		t.Fatalf("read seeded lesson: %v", err)
	}

	driver := f.runDriverAsync("ocr-prefill-close", 45*time.Second)
	m := f.tryFirstManager(5 * time.Second)
	if m == nil {
		f.reclaimAndFail(driver, []func(){verdictGate.release}, "no /rt connection was created before the deadline")
	}
	if !speechGateHit(verdictGate, 30*time.Second) {
		m.TurnManager.mu.Lock()
		pending := len(m.checkWrites)
		m.TurnManager.mu.Unlock()
		f.reclaimAndFail(driver, []func(){verdictGate.release}, "the prefill batch never hit the gate: admissions=%d", pending)
	}
	// The batch's ONE admission is registered before its first write parks.
	if !waitFor(t, 10*time.Second, func() bool {
		m.TurnManager.mu.Lock()
		defer m.TurnManager.mu.Unlock()
		return len(m.checkWrites) == 1
	}) {
		f.reclaimAndFail(driver, []func(){verdictGate.release}, "the prefill batch was never admitted")
	}
	// Hold the read gate NOW: the fixture/resume reads are long past, the
	// parked batch issues no reads, so the fallback's read is the first one
	// of the held window — holding before the close removes any start-up
	// race a fast (wait-skipping) fallback could win.
	readGate.hold()
	// The goodbye close claims the fallback while the batch's writes are
	// parked AHEAD of their real SQLite commits.
	m.handleEndSession(m.currentSender())
	// 在写入闸仍关着时等读门 entered：只有没有等待的实现会在此窗口到达
	// 权威读（撤销 waitCheckWrites 的变异形态）。等到了就放行读、逼这个
	// 未等待的兜底用旧版本先赢闩，再放行两笔写入——第一笔冲突后走幂等闩
	// 重放（verdict 丢失）、第二笔恰好撞上新版本落库，最终版本断言变红。
	skippedWait := false
	select {
	case <-readGate.gate.entered:
		skippedWait = true
	case <-time.After(3 * time.Second):
	}
	if skippedWait {
		readGate.release()
		if !waitFor(t, 10*time.Second, func() bool {
			l, err := f.tools.GetLessonState(context.Background(), lessonID)
			return err == nil && l.Stage == lesson.StageSubmitted && l.Unchecked
		}) {
			f.reclaimAndFail(driver, []func(){verdictGate.release}, "the un-waited fallback never won the latch on the stale version")
		}
	}
	// 有等待的实现：兜底仍阻塞在登记上，读门无人到达。先放行批次，两笔
	// 预填以 v+1、v+2 落库；登记完成后兜底的权威读才第一次到达读门——
	// 等到 entered 再放行，它读到 v+2 并以新版本赢闩。
	verdictGate.release()
	if !skippedWait && !speechGateHit(readGate.gate, 30*time.Second) {
		f.reclaimAndFail(driver, []func(){readGate.release}, "the waiting fallback's authoritative read never arrived")
	}
	readGate.release()
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
	m.TurnManager.mu.Lock()
	pending := len(m.checkWrites)
	m.TurnManager.mu.Unlock()
	if pending != 0 {
		t.Fatalf("completed check-write admissions = %d, want 0", pending)
	}
}

// TestCloseFallbackVersusSubmit 是 1.12c 的验收：正式提交与关闭的三种顺
// 序，全部经真实保存链路（useRealFSRS：SubmitReview 走真实 pybridge 落进
// 本夹具的临时 SQLite，回执进同一库）核对持久结果——不只数 fake 调用。
//   - 提交先接纳（latch 写前关闭）：读门会合与多笔写入用例同一套——
//     提交闩仍驻留时只有撤掉等待的兜底会到达权威读；有等待的实现先放
//     提交落闩、再读权威行，submitted 后让位——只有提交自己的
//     kind=check 回执与 FSRS 批；
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
		readGate := f.gateStateRead()
		before, _ := f.gateCheckSubmit(true, false)
		t.Cleanup(before.release)
		t.Cleanup(readGate.release)

		driver := f.runDriverAsync("check-submit-close", 45*time.Second)
		if !speechGateHit(before, 30*time.Second) {
			f.reclaimAndFail(driver, []func(){before.release, readGate.release}, "the formal submit never reached its pre-latch window")
		}
		m := f.tryFirstManager(5 * time.Second)
		if m == nil {
			f.reclaimAndFail(driver, []func(){before.release, readGate.release}, "no /rt connection was created before the deadline")
		}
		// The goodbye close claims the fallback while the ACCEPTED submit is
		// parked ahead of its latch write. 读门会合（复盘第 3 节第 2 项）：
		// 提交闩仍驻留时，只有撤掉 waitCheckWrites 的兜底才会在这个窗口
		// 到达权威读——放行读、逼它先赢闩，再放正式提交，行停在
		// submitted+unchecked；有等待的实现放提交后才读，读到 submitted
		// 让位。
		readGate.hold()
		m.handleEndSession(m.currentSender())
		skippedWait := false
		select {
		case <-readGate.gate.entered:
			skippedWait = true
		case <-time.After(3 * time.Second):
		}
		if skippedWait {
			readGate.release()
			if !waitFor(t, 10*time.Second, func() bool {
				l := storedLesson(f)
				return l.Stage == lesson.StageSubmitted && l.Unchecked
			}) {
				f.reclaimAndFail(driver, []func(){before.release}, "the un-waited fallback never won the submit latch")
			}
		} else {
			before.release()
			if !speechGateHit(readGate.gate, 30*time.Second) {
				f.reclaimAndFail(driver, []func(){readGate.release}, "the waiting fallback's authoritative read never arrived")
			}
			readGate.release()
		}
		before.release()
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
		m.TurnManager.mu.Lock()
		pending := len(m.checkWrites)
		m.TurnManager.mu.Unlock()
		if pending != 0 {
			t.Fatalf("completed check-write admissions = %d, want 0", pending)
		}
	})

	t.Run("submit committed, close before return", func(t *testing.T) {
		f := newSpeechBrowserFixture(t)
		probe := f.useRealFSRS(t)
		f.seedCheckingWithManualVerdicts()
		_, committed := f.gateCheckSubmit(false, true)
		t.Cleanup(committed.release)

		driver := f.runDriverAsync("check-submit-close", 45*time.Second)
		if !speechGateHit(committed, 30*time.Second) {
			f.reclaimAndFail(driver, []func(){committed.release}, "the formal submit never committed its latch write")
		}
		m := f.tryFirstManager(5 * time.Second)
		if m == nil {
			f.reclaimAndFail(driver, []func(){committed.release}, "no /rt connection was created before the deadline")
		}
		m.handleEndSession(m.currentSender())
		committed.release()
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
		m.TurnManager.mu.Lock()
		pending := len(m.checkWrites)
		m.TurnManager.mu.Unlock()
		if pending != 0 {
			t.Fatalf("completed check-write admissions = %d, want 0", pending)
		}
	})

	t.Run("close first: the late submit is refused, the fallback owns the save", func(t *testing.T) {
		f := newSpeechBrowserFixture(t)
		probe := f.useRealFSRS(t)
		f.seedCheckingWithOutcomes()
		verdictGate := f.gateScreenVerdict()
		t.Cleanup(verdictGate.release)

		driver := f.runDriverAsync("screen-verdict-close", 45*time.Second)
		if !speechGateHit(verdictGate, 30*time.Second) {
			f.reclaimAndFail(driver, []func(){verdictGate.release}, "the screen verdict did not reach the persistence gate")
		}
		m := f.tryFirstManager(5 * time.Second)
		if m == nil {
			f.reclaimAndFail(driver, []func(){verdictGate.release}, "no /rt connection was created before the deadline")
		}
		m.handleEndSession(m.currentSender())
		verdictGate.release()
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
		m.TurnManager.mu.Lock()
		pending := len(m.checkWrites)
		m.TurnManager.mu.Unlock()
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
		resumeGate, closeGate := newSpeechOneGate(), newSpeechOneGate()
		f.mu.Lock()
		f.resumeHold = func() { resumeGate.hit(); resumeGate.wait() }
		f.closeEntryHold = func() { closeGate.hit(); closeGate.wait() }
		f.mu.Unlock()
		t.Cleanup(resumeGate.release)
		t.Cleanup(closeGate.release)

		driver := f.runDriverAsync("resume-close-window", 45*time.Second)
		if !speechGateHit(resumeGate, 30*time.Second) {
			f.reclaimAndFail(driver, []func(){resumeGate.release, closeGate.release}, "the resume never reached its pre-presentation window")
		}
		m := f.tryFirstManager(5 * time.Second)
		if m == nil {
			f.reclaimAndFail(driver, []func(){resumeGate.release, closeGate.release}, "no /rt connection was created before the deadline")
		}
		// The goodbye parks inside the close entry (init-write lock held,
		// tm.mu not yet) while the presentation is still parked behind its
		// own window — the old two-lock snapshot window, driven through the
		// REAL resume path.
		closeDone := make(chan struct{})
		go func() {
			m.handleEndSession(m.currentSender())
			close(closeDone)
		}()
		if !speechGateHit(closeGate, 10*time.Second) {
			f.reclaimAndFail(driver, []func(){resumeGate.release, closeGate.release}, "the close never entered its claim window")
		}
		// Release the presentation INTO the window: the tutor flips to
		// checking and check_started ships before the claim decides.
		resumeGate.release()
		if !waitFor(t, 10*time.Second, func() bool {
			m.TurnManager.mu.Lock()
			defer m.TurnManager.mu.Unlock()
			return m.tutor != nil && m.tutor.checking
		}) {
			f.reclaimAndFail(driver, []func(){closeGate.release}, "the resume presentation never landed inside the close window")
		}
		closeGate.release()
		if !speechChHit(closeDone, 10*time.Second) {
			f.reclaimAndFail(driver, nil, "the close never finished after the presentation")
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
		m.TurnManager.mu.Lock()
		pending := len(m.checkWrites)
		m.TurnManager.mu.Unlock()
		if pending != 0 {
			t.Fatalf("completed check-write admissions = %d, want 0", pending)
		}
	})

	t.Run("a close winning the window refuses the presentation and keeps checking", func(t *testing.T) {
		f := newSpeechBrowserFixture(t)
		f.seedChecking()
		resumeGate := newSpeechOneGate()
		f.mu.Lock()
		f.resumeHold = func() { resumeGate.hit(); resumeGate.wait() }
		f.mu.Unlock()
		t.Cleanup(resumeGate.release)

		driver := f.runDriverAsync("resume-close-window", 45*time.Second)
		if !speechGateHit(resumeGate, 30*time.Second) {
			f.reclaimAndFail(driver, []func(){resumeGate.release}, "the resume never reached its pre-presentation window")
		}
		m := f.tryFirstManager(5 * time.Second)
		if m == nil {
			f.reclaimAndFail(driver, []func(){resumeGate.release}, "no /rt connection was created before the deadline")
		}
		// The goodbye runs to completion while the presentation is still
		// parked: the claim saw no checking classroom, so no fallback exists
		// and the late presentation is refused outright.
		m.handleEndSession(m.currentSender())
		resumeGate.release()
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
		m.TurnManager.mu.Lock()
		closedFlag, pending := m.TurnManager.closed, len(m.checkWrites)
		m.TurnManager.mu.Unlock()
		if !closedFlag || pending != 0 {
			t.Fatalf("closed=%v admissions=%d, want true/0", closedFlag, pending)
		}
	})
}

// TestDuplicateSubmitBrowser 是 1.13b 的验收：用自然播放夹具替换
// duplicate_submit_terminal 的代 ACK 初始化。另一连接先把检查闩落库，本
// 会话的真实 UI 提交（面板按钮 → check_submit → 生产 CompleteCheck）以
// Idempotent 回放撞上已 submitted 的行——重复提交终端分支经统一关闭入口：
// 终态 session_ended 恰一次、没有第二笔保存、没有 review_result 或迟到
// 播报越过终止消息，连接与任务全部退出。
func TestDuplicateSubmitBrowser(t *testing.T) {
	f := newSpeechBrowserFixture(t)
	f.seedCheckingWithManualVerdicts()
	before, _ := f.gateCheckSubmit(true, false)
	t.Cleanup(before.release)

	driver := f.runDriverAsync("check-submit-close", 45*time.Second)
	if !speechGateHit(before, 30*time.Second) {
		f.reclaimAndFail(driver, []func(){before.release}, "the formal submit never reached its pre-latch window")
	}
	m := f.tryFirstManager(5 * time.Second)
	if m == nil {
		f.reclaimAndFail(driver, []func(){before.release}, "no /rt connection was created before the deadline")
	}
	// While THIS session's submit is parked ahead of its latch write, another
	// connection flips the checking→submitted latch first (the multi-
	// connection rule rides the database, §七).
	lessonID := lessonIDForCategory("speech-browser-fixture", time.Now(), lesson.CategoryChinese)
	l, err := f.tools.GetLessonState(context.Background(), lessonID)
	if err != nil {
		t.Fatalf("read lesson before the foreign latch: %v", err)
	}
	if _, err := f.tools.CompleteCheck(context.Background(), lessonID, l.Version, "other-tab-submit", false); err != nil {
		t.Fatalf("foreign submit latch: %v", err)
	}
	before.release()
	res := <-driver
	if res.WSCloseCount != 1 || res.LateReviewResults != 0 || res.LateTeacherSentences != 0 {
		t.Fatalf("close=%d late review=%d late sentences=%d, want 1/0/0",
			res.WSCloseCount, res.LateReviewResults, res.LateTeacherSentences)
	}
	f.assertOriginalTasksExited()

	stored, err := f.tools.GetLessonState(context.Background(), lessonID)
	if err != nil {
		t.Fatalf("read stored lesson: %v", err)
	}
	if stored.Stage != lesson.StageSubmitted || stored.Unchecked {
		t.Fatalf("stored stage=%q unchecked=%v, want the foreign submit's submitted+checked", stored.Stage, stored.Unchecked)
	}
	if got := len(f.words.submissions()); got != 0 {
		t.Fatalf("review batches = %d, want 0 — the duplicate submit never saves", got)
	}
	m.TurnManager.mu.Lock()
	closedFlag, pending := m.TurnManager.closed, len(m.checkWrites)
	m.TurnManager.mu.Unlock()
	if !closedFlag || pending != 0 {
		t.Fatalf("closed=%v admissions=%d, want true/0", closedFlag, pending)
	}
}

// TestClosedControlBrowser 是 1.13c 的验收：用自然播放夹具替换
// closed_control_silence 的代 ACK 初始化。真实连接经 resume 播报自然 ACK
// 完成初始化后：终态关闭之后到达的控制面（不可解析帧、服务器专用类型、
// 打断、重读、ping）经生产入口零应答、epoch 不动；打断与 goodbye 的竞争
// 无论谁赢，close 之后再无任何下行帧尾随终止消息。
func TestClosedControlBrowser(t *testing.T) {
	settled := func(f *speechBrowserFixture) {
		t.Helper()
		if !waitFor(t, 20*time.Second, func() bool { return f.announcementDone.Load() >= 1 }) {
			t.Fatal("the resume announcement never settled — no natural playback initialization")
		}
	}

	t.Run("frames after the terminal close answer nothing", func(t *testing.T) {
		f := newSpeechBrowserFixture(t)
		f.seedChecking()
		driver := f.runDriverAsync("resume-close-window", 45*time.Second)
		m := f.tryFirstManager(5 * time.Second)
		if m == nil {
			f.reclaimAndFail(driver, nil, "no /rt connection was created before the deadline")
		}
		settled(f)
		m.handleEndSession(m.currentSender())
		res := <-driver
		if res.WSCloseCount != 1 {
			t.Fatalf("close events = %d, want 1", res.WSCloseCount)
		}

		// The post-terminal barrage rides a capture sender through the
		// production entry: the closed session must answer none of it.
		capture := &captureSender{}
		before := ttTotalControls(capture)
		m.HandleControl(capture, []byte("not json"))
		m.HandleControl(capture, mustJSON(t, TeacherSentenceMsg{Type: TypeTeacherSentence}))
		m.HandleControl(capture, mustJSON(t, InterruptMsg{Type: TypeInterrupt, LastSentenceID: "t1-s0"}))
		m.HandleControl(capture, mustJSON(t, ReadWordMsg{Type: TypeReadWord, WordIndex: 0, CurrentIndex: 0}))
		m.HandleControl(capture, mustJSON(t, PingMsg{Type: TypePing, TClientMs: 9}))
		if got := ttTotalControls(capture); got != before {
			t.Fatalf("controls after the close = %d, want unchanged %d", got, before)
		}
		m.TurnManager.mu.Lock()
		closedFlag, epoch := m.TurnManager.closed, m.epoch
		m.TurnManager.mu.Unlock()
		if !closedFlag {
			t.Fatal("the close did not stand")
		}
		if got := m.currentEpoch(); got != epoch {
			t.Fatalf("epoch after the closed interrupt = %d, want unchanged %d", got, epoch)
		}
	})

	t.Run("an interrupt racing the goodbye never trails the terminal message", func(t *testing.T) {
		f := newSpeechBrowserFixture(t)
		f.seedChecking()
		driver := f.runDriverAsync("resume-close-window", 45*time.Second)
		m := f.tryFirstManager(5 * time.Second)
		if m == nil {
			f.reclaimAndFail(driver, nil, "no /rt connection was created before the deadline")
		}
		settled(f)

		start := make(chan struct{})
		var wg sync.WaitGroup
		for _, c := range []func(){
			func() { m.handleInterrupt(m.currentSender(), InterruptMsg{Type: TypeInterrupt}) },
			func() { m.handleEndSession(m.currentSender()) },
		} {
			c := c
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				c()
			}()
		}
		close(start)
		wg.Wait()
		res := <-driver
		if res.WSCloseCount != 1 || res.LateFramesAfterClose != 0 {
			t.Fatalf("close=%d late frames=%d, want 1/0 — nothing may trail the terminal message",
				res.WSCloseCount, res.LateFramesAfterClose)
		}
		m.TurnManager.mu.Lock()
		closedFlag := m.TurnManager.closed
		m.TurnManager.mu.Unlock()
		if !closedFlag {
			t.Fatal("the raced close never landed")
		}
	})
}

// TestCorrectionSubmitBrowser 是 1.10a 的复验（review P1）：更正提交的接
// 纳与保存责任经真实浏览器旅程。两个顺序各自一条业务断言（复盘第 3 节
// 第 3 项——种子已是 submitted，兜底让位不能再当证明）：
//   - 提交先接纳：提交闩（diff 写）驻留时关闭，已接纳的 diff 保存与回
//     执仍然完成，行落在 submitted+checked、版本 +3；
//   - 密封先于提交：改判落库后、提交点按前的延迟窗口内关闭（真实告
//     别），迟到的提交经生产入口 submitCorrections 探针（消息路径由更
//     早的 alive 门槛持有）——查封的登记不接纳：零 diff 批、unchecked
//     标记保留、版本停在 +2。
//
// 撤销验证：撤掉 submitCorrections 的生产接纳后，「密封先于提交」子测试
// 红（diff 落库、标记被清）。
func TestCorrectionSubmitBrowser(t *testing.T) {
	t.Run("submit admitted before the close", func(t *testing.T) {
		f := newSpeechBrowserFixture(t)
		f.seedCorrection()
		reviewGate := f.gateReviewWrite()

		lessonID := lessonIDForCategory("speech-browser-fixture", time.Now(), lesson.CategoryChinese)
		seeded, err := f.tools.GetLessonState(context.Background(), lessonID)
		if err != nil {
			t.Fatalf("read seeded lesson: %v", err)
		}

		driver := f.runDriverAsync("correction-submit-close", 45*time.Second)
		if !speechGateHit(reviewGate, 30*time.Second) {
			f.reclaimAndFail(driver, []func(){reviewGate.release}, "the correction diff write never reached its review gate")
		}
		m := f.tryFirstManager(5 * time.Second)
		if m == nil {
			f.reclaimAndFail(driver, []func(){reviewGate.release}, "no /rt connection was created before the deadline")
		}
		// The goodbye close claims the accepted correction (its admission covers
		// the diff write, the receipt and the marker reset) while the diff write
		// is parked AHEAD of the real save.
		m.handleEndSession(m.currentSender())
		reviewGate.release()
		res := <-driver
		if res.WSCloseCount != 1 || res.LateReviewResults != 0 || res.LateTeacherSentences != 0 {
			t.Fatalf("close=%d late review=%d late sentences=%d, want 1/0/0 late=%q",
				res.WSCloseCount, res.LateReviewResults, res.LateTeacherSentences, res.LateSentenceTexts)
		}
		f.assertOriginalTasksExited()

		if got := len(f.words.submissions()); got != 1 || len(f.words.submissions()[0]) != 1 {
			t.Fatalf("correction diff batches = %v, want exactly the one-word diff", f.words.submissions())
		}
		stored, err := f.tools.GetLessonState(context.Background(), lessonID)
		if err != nil {
			t.Fatalf("read stored lesson: %v", err)
		}
		if stored.Stage != lesson.StageSubmitted || stored.Unchecked {
			t.Fatalf("stored stage=%q unchecked=%v, want the marker cleared by the accepted correction", stored.Stage, stored.Unchecked)
		}
		// Two manual verdicts (v+2) then the marker reset (+1): the diff's own
		// review write lands in the vocabulary store, not the lesson row.
		if stored.Version != seeded.Version+3 {
			t.Fatalf("stored version = %d, want seeded %d + 2 verdicts + 1 marker reset", stored.Version, seeded.Version)
		}
		m.TurnManager.mu.Lock()
		pending := len(m.checkWrites)
		m.TurnManager.mu.Unlock()
		if pending != 0 {
			t.Fatalf("completed check-write admissions = %d, want 0", pending)
		}
	})

	t.Run("close seals before the submit", func(t *testing.T) {
		f := newSpeechBrowserFixture(t)
		f.seedCorrection()

		lessonID := lessonIDForCategory("speech-browser-fixture", time.Now(), lesson.CategoryChinese)
		seeded, err := f.tools.GetLessonState(context.Background(), lessonID)
		if err != nil {
			t.Fatalf("read seeded lesson: %v", err)
		}

		// --submit-delay-ms leaves the window between the two taps and the
		// submit tap; the close lands there, after both tap writes committed
		// (tm.tutor.version = seeded+2). The page's own submit click rides a
		// dead connection — the late submit below probes the production entry
		// in-process instead.
		driver := f.runDriverAsync("correction-submit-close", 45*time.Second, "--submit-delay-ms", "3000")
		m := f.tryFirstManager(5 * time.Second)
		if m == nil {
			f.reclaimAndFail(driver, nil, "no /rt connection was created before the deadline")
		}
		if !waitFor(t, 30*time.Second, func() bool {
			m.TurnManager.mu.Lock()
			defer m.TurnManager.mu.Unlock()
			return m.tutor != nil && m.tutor.version >= seeded.Version+2
		}) {
			t.Fatal("the correction taps never landed their verdict writes")
		}
		m.handleEndSession(m.currentSender())
		// No gate held; the asserts still wait for the server tasks first
		// (§5.1 order — the late-submit window below is observation only).
		res := <-driver

		// A submit arriving past the sealed close cannot ride the message
		// path — handleCheckSubmit's alive gate (a strictly earlier refusal)
		// owns that path, and a read-gate rendezvous inside submitCorrections
		// dies to the close's turnCancel. So the late submit probes the
		// production ENTRY submitCorrections itself (the read loop's
		// last-message equivalent for this gate): 1.10a's admission is the
		// independent second gate — a sealed registry admits nothing.
		m.submitCorrections(m.currentSender(), CheckSubmitMsg{
			Type: TypeCheckSubmit, RequestID: "late-correction-after-close",
		})
		// 拿掉接纳的变异会让 finishCorrections 走异步 goroutine：给这个未被
		// 拒绝的提交一个有界窗口把 diff 真正落库，「没有发生」才是业务断言。
		waitFor(t, 3*time.Second, func() bool { return len(f.words.submissions()) > 0 })
		if !waitFor(t, speechReclaimBound, f.speechServerTasksSettled) {
			t.Fatalf("server tasks did not settle: handler=%d/%d writer=%d provider=%d/%d announcement=%d/%d",
				f.handlerActive.Load(), f.handlerDone.Load(), f.writerDone.Load(),
				f.providerStarted.Load(), f.providerDone.Load(),
				f.announcementRun.Load(), f.announcementDone.Load())
		}

		if res.WSCloseCount != 1 || res.LateReviewResults != 0 || res.LateTeacherSentences != 0 {
			t.Fatalf("close=%d late review=%d late sentences=%d, want 1/0/0",
				res.WSCloseCount, res.LateReviewResults, res.LateTeacherSentences)
		}
		if got := len(f.words.submissions()); got != 0 {
			t.Fatalf("correction diff batches = %v, want none — a sealed registry admits no submit", f.words.submissions())
		}
		stored, err := f.tools.GetLessonState(context.Background(), lessonID)
		if err != nil {
			t.Fatalf("read stored lesson: %v", err)
		}
		if stored.Stage != lesson.StageSubmitted || !stored.Unchecked {
			t.Fatalf("stored stage=%q unchecked=%v, want the marker kept for the next correction session", stored.Stage, stored.Unchecked)
		}
		if stored.Version != seeded.Version+2 {
			t.Fatalf("stored version = %d, want seeded %d + the two taps only (no marker reset)", stored.Version, seeded.Version)
		}
		m.TurnManager.mu.Lock()
		pending := len(m.checkWrites)
		m.TurnManager.mu.Unlock()
		if pending != 0 {
			t.Fatalf("completed check-write admissions = %d, want 0", pending)
		}
	})
}

// TestPassageSubmitBrowser 是 1.10d 的复验（review P1）：短文提交的接纳与
// 保存责任经真实浏览器旅程。两个顺序各自一条业务断言：
//   - 提交先接纳：提交闩驻留时关闭，行落在 submitted+checked；
//   - 密封先于提交：改判落库后、提交点按前的延迟窗口内关闭，迟到提交经
//     生产入口 submitPassage 探针——仅 unchecked 兜底落库，无正式提交。
//
// 撤销验证：撤掉 submitPassage 的 admitCheckWriteLocked 登记后
// 「密封先于提交」子测试红（迟到正式提交把行推到 submitted+checked）。
func TestPassageSubmitBrowser(t *testing.T) {
	t.Run("submit admitted before the close", func(t *testing.T) {
		f := newSpeechBrowserFixture(t)
		f.armPoemPassageSaveChain()
		f.seedPassage()
		f.scriptSpokenConfirm()
		before, _ := f.gateCheckSubmit(true, false)
		t.Cleanup(before.release)

		driver := f.runDriverAsync("passage-submit-close", 45*time.Second)
		if !speechGateHit(before, 30*time.Second) {
			f.reclaimAndFail(driver, []func(){before.release}, "the passage submit never reached its pre-latch window")
		}
		m := f.tryFirstManager(5 * time.Second)
		if m == nil {
			f.reclaimAndFail(driver, []func(){before.release}, "no /rt connection was created before the deadline")
		}
		m.handleEndSession(m.currentSender())
		before.release()
		res := <-driver
		if res.WSCloseCount != 1 || res.LateReviewResults != 0 || res.LateTeacherSentences != 0 {
			t.Fatalf("close=%d late review=%d late sentences=%d, want 1/0/0",
				res.WSCloseCount, res.LateReviewResults, res.LateTeacherSentences)
		}
		// Server tasks, not just the connection: the accepted submit's own
		// tail AND the unchecked fallback must both be done before the row is
		// read (review R0-P2: the driver's return proves neither — the
		// fallback can still be parked on a gate when it fires).
		if !waitFor(t, speechReclaimBound, f.speechServerTasksSettled) {
			t.Fatalf("server tasks did not settle: handler=%d/%d writer=%d provider=%d/%d announcement=%d/%d",
				f.handlerActive.Load(), f.handlerDone.Load(), f.writerDone.Load(),
				f.providerStarted.Load(), f.providerDone.Load(),
				f.announcementRun.Load(), f.announcementDone.Load())
		}

		m.TurnManager.mu.Lock()
		lessonID := m.tutor.lessonID
		pending := len(m.checkWrites)
		m.TurnManager.mu.Unlock()
		stored, err := f.tools.GetLessonState(context.Background(), lessonID)
		if err != nil {
			t.Fatalf("read stored lesson: %v", err)
		}
		if stored.Stage != lesson.StageSubmitted || stored.Unchecked {
			t.Fatalf("stored stage=%q unchecked=%v, want the accepted passage submit's submitted+checked",
				stored.Stage, stored.Unchecked)
		}
		if pending != 0 {
			t.Fatalf("completed check-write admissions = %d, want 0", pending)
		}
		recs, err := f.receipts.ListByLesson(context.Background(), lessonID)
		if err != nil {
			t.Fatalf("list receipts: %v", err)
		}
		if len(recs) != 1 || recs[0].Kind != lesson.ReceiptKindPassage || recs[0].Status != lesson.ReceiptStatusSaved {
			t.Fatalf("receipts = %+v, want one saved kind=passage receipt", recs)
		}
		rows, err := f.passagePracticeStore.ListByRange(context.Background(), 77, false, []int{2})
		if err != nil {
			t.Fatalf("list passage practice: %v", err)
		}
		if len(rows) != 1 || rows[0].LessonID != lessonID {
			t.Fatalf("passage practice rows = %+v, want one row for this lesson", rows)
		}
	})

	t.Run("close seals before the submit", func(t *testing.T) {
		f := newSpeechBrowserFixture(t)
		f.armPoemPassageSaveChain()
		f.seedPassage()
		f.scriptSpokenConfirm()
		fallbackHold := f.gateUncheckedCompleteCheck()
		t.Cleanup(fallbackHold.release)

		driver := f.runDriverAsync("passage-submit-close", 45*time.Second, "--submit-delay-ms", "15000")
		m := f.tryFirstManager(5 * time.Second)
		if m == nil {
			f.reclaimAndFail(driver, []func(){fallbackHold.release}, "no /rt connection was created before the deadline")
		}
		var lessonID string
		var versionAfterVerdict int
		if !waitFor(t, 30*time.Second, func() bool {
			m.TurnManager.mu.Lock()
			defer m.TurnManager.mu.Unlock()
			if m.tutor == nil || len(m.tutor.checkVerdicts) == 0 {
				return false
			}
			if !m.tutor.checkVerdicts[0].Manual {
				return false
			}
			lessonID = m.tutor.lessonID
			versionAfterVerdict = m.tutor.version
			return true
		}) {
			t.Fatal("the passage verdict never landed")
		}
		m.handleEndSession(m.currentSender())
		// The driver's own exit rides the transport teardown, not the held
		// gate — receive it now, assert it AFTER the reclaim (§5.1: no Fatal
		// while a control point is still held).
		res := <-driver

		if !speechGateHit(fallbackHold, 30*time.Second) {
			f.reclaimAndFail(driver, []func(){fallbackHold.release}, "the unchecked fallback never reached CompleteCheck before the probe")
		}

		// S1 acceptance (第三轮第 4 项): the fallback is parked BEFORE its
		// executor call — outside the bottom tracker's sight — and the
		// whole-task observation must still refuse to report it done. The
		// observation is RECORDED here and asserted after the reclaim.
		fallbackParkedSettled := f.speechServerTasksSettled()

		// Probe the production entry while the registry is sealed and the
		// fallback latch is still parked — no unchecked row yet. The stored
		// row is recorded, not asserted: the assert comes after the reclaim.
		m.submitPassage(m.currentSender(), CheckSubmitMsg{
			Type: TypeCheckSubmit, RequestID: "late-passage-after-close",
		})
		parked, parkedErr := f.tools.GetLessonState(context.Background(), lessonID)

		// 释放控制点 → 等待原任务（兜底整任务，含其事件尾部）→ 最后断言。
		fallbackHold.release()
		if !waitFor(t, speechReclaimBound, f.speechServerTasksSettled) {
			t.Fatalf("server tasks did not settle after the reclaim: handler=%d/%d writer=%d provider=%d/%d announcement=%d/%d",
				f.handlerActive.Load(), f.handlerDone.Load(), f.writerDone.Load(),
				f.providerStarted.Load(), f.providerDone.Load(),
				f.announcementRun.Load(), f.announcementDone.Load())
		}

		if res.WSCloseCount != 1 || res.LateReviewResults != 0 || res.LateTeacherSentences != 0 {
			t.Fatalf("close=%d late review=%d late sentences=%d, want 1/0/0",
				res.WSCloseCount, res.LateReviewResults, res.LateTeacherSentences)
		}
		if fallbackParkedSettled {
			t.Fatal("settled reported every server task done while the fallback was still parked before its executor call")
		}
		if parkedErr != nil {
			t.Fatalf("read stored lesson while parked: %v", parkedErr)
		}
		if parked.Stage != lesson.StageChecking {
			t.Fatalf("stored stage=%q, want checking — a sealed registry admits no submit", parked.Stage)
		}
		if parked.Version != versionAfterVerdict {
			t.Fatalf("stored version = %d, want %d (verdicts only, no latch)", parked.Version, versionAfterVerdict)
		}

		// The settled wait above already observed the fallback's whole task
		// exit (its handle is released only after the event tail), so the
		// stored row is final here — no extra poll.
		stored, err := f.tools.GetLessonState(context.Background(), lessonID)
		if err != nil {
			t.Fatalf("read stored lesson after fallback: %v", err)
		}
		if stored.Stage != lesson.StageSubmitted || !stored.Unchecked {
			t.Fatalf("stored stage=%q unchecked=%v, want the fallback's submitted+unchecked",
				stored.Stage, stored.Unchecked)
		}
		if stored.Version != versionAfterVerdict+1 {
			t.Fatalf("stored version = %d, want %d (one unchecked fallback latch only)",
				stored.Version, versionAfterVerdict+1)
		}
		m.TurnManager.mu.Lock()
		pending := len(m.checkWrites)
		m.TurnManager.mu.Unlock()
		if pending != 0 {
			t.Fatalf("completed check-write admissions = %d, want 0", pending)
		}
	})
}

// TestPoemSubmitBrowser 是 1.10c 的复验（review P1）：古诗提交的接纳与保
// 存责任经真实浏览器旅程。两个顺序各自一条业务断言：
//   - 提交先接纳：提交闩驻留时关闭，行落在 submitted+checked；
//   - 密封先于提交：两句改判落库后、提交点按前的延迟窗口内关闭，迟到提
//     交经生产入口 submitPoem 探针——行仍停在 checking、版本不再前进。
//
// 撤销验证：撤掉 submitPoem 的 admitCheckWriteLocked 登记后「密封先于提
// 交」子测试红（行应停在 checking，变异后变为 submitted）。
func TestPoemSubmitBrowser(t *testing.T) {
	t.Run("submit admitted before the close", func(t *testing.T) {
		f := newSpeechBrowserFixture(t)
		f.armPoemPassageSaveChain()
		f.seedPoem()
		f.scriptSpokenConfirm()
		before, _ := f.gateCheckSubmit(true, false)
		t.Cleanup(before.release)

		driver := f.runDriverAsync("poem-submit-close", 45*time.Second)
		if !speechGateHit(before, 30*time.Second) {
			f.reclaimAndFail(driver, []func(){before.release}, "the poem submit never reached its pre-latch window")
		}
		m := f.tryFirstManager(5 * time.Second)
		if m == nil {
			f.reclaimAndFail(driver, []func(){before.release}, "no /rt connection was created before the deadline")
		}
		m.handleEndSession(m.currentSender())
		before.release()
		res := <-driver
		if res.WSCloseCount != 1 || res.LateReviewResults != 0 || res.LateTeacherSentences != 0 {
			t.Fatalf("close=%d late review=%d late sentences=%d, want 1/0/0",
				res.WSCloseCount, res.LateReviewResults, res.LateTeacherSentences)
		}
		// Server tasks, not just the connection: the accepted submit's own
		// tail AND the unchecked fallback must both be done before the row is
		// read (review R0-P2: the driver's return proves neither — the
		// fallback can still be parked on a gate when it fires).
		if !waitFor(t, speechReclaimBound, f.speechServerTasksSettled) {
			t.Fatalf("server tasks did not settle: handler=%d/%d writer=%d provider=%d/%d announcement=%d/%d",
				f.handlerActive.Load(), f.handlerDone.Load(), f.writerDone.Load(),
				f.providerStarted.Load(), f.providerDone.Load(),
				f.announcementRun.Load(), f.announcementDone.Load())
		}

		m.TurnManager.mu.Lock()
		lessonID := m.tutor.lessonID
		pending := len(m.checkWrites)
		m.TurnManager.mu.Unlock()
		stored, err := f.tools.GetLessonState(context.Background(), lessonID)
		if err != nil {
			t.Fatalf("read stored lesson: %v", err)
		}
		if stored.Stage != lesson.StageSubmitted || stored.Unchecked {
			t.Fatalf("stored stage=%q unchecked=%v, want the accepted poem submit's submitted+checked",
				stored.Stage, stored.Unchecked)
		}
		if pending != 0 {
			t.Fatalf("completed check-write admissions = %d, want 0", pending)
		}
		recs, err := f.receipts.ListByLesson(context.Background(), lessonID)
		if err != nil {
			t.Fatalf("list receipts: %v", err)
		}
		if len(recs) != 1 || recs[0].Kind != lesson.ReceiptKindPoem || recs[0].Status != lesson.ReceiptStatusSaved {
			t.Fatalf("receipts = %+v, want one saved kind=poem receipt", recs)
		}
		rows, err := f.poemPracticeStore.ListByPoem(context.Background(), 77)
		if err != nil {
			t.Fatalf("read poem practice: %v", err)
		}
		if len(rows) != 1 || rows[0].LessonID != lessonID {
			t.Fatalf("poem practice rows = %+v, want one row for this lesson", rows)
		}
	})

	t.Run("close seals before the submit", func(t *testing.T) {
		f := newSpeechBrowserFixture(t)
		f.armPoemPassageSaveChain()
		f.seedPoem()
		f.scriptSpokenConfirm()

		driver := f.runDriverAsync("poem-submit-close", 45*time.Second, "--submit-delay-ms", "15000")
		m := f.tryFirstManager(5 * time.Second)
		if m == nil {
			f.reclaimAndFail(driver, nil, "no /rt connection was created before the deadline")
		}
		var lessonID string
		var versionAfterVerdicts int
		if !waitFor(t, 30*time.Second, func() bool {
			m.TurnManager.mu.Lock()
			defer m.TurnManager.mu.Unlock()
			if m.tutor == nil || len(m.tutor.checkVerdicts) < 2 {
				return false
			}
			for _, v := range m.tutor.checkVerdicts {
				if !v.Manual {
					return false
				}
			}
			lessonID = m.tutor.lessonID
			versionAfterVerdicts = m.tutor.version
			return true
		}) {
			t.Fatal("the poem manual verdicts never landed")
		}
		m.handleEndSession(m.currentSender())
		// No gate is held here — still, the driver result is only ASSERTED
		// after the server tasks (the poem fallback's own task) settled.
		res := <-driver

		m.submitPoem(m.currentSender(), CheckSubmitMsg{
			Type: TypeCheckSubmit, RequestID: "late-poem-after-close",
		})
		waitFor(t, 3*time.Second, func() bool {
			stored, err := f.tools.GetLessonState(context.Background(), lessonID)
			return err == nil && stored.Stage == lesson.StageSubmitted
		})
		if !waitFor(t, speechReclaimBound, f.speechServerTasksSettled) {
			t.Fatalf("server tasks did not settle: handler=%d/%d writer=%d provider=%d/%d announcement=%d/%d",
				f.handlerActive.Load(), f.handlerDone.Load(), f.writerDone.Load(),
				f.providerStarted.Load(), f.providerDone.Load(),
				f.announcementRun.Load(), f.announcementDone.Load())
		}

		if res.WSCloseCount != 1 || res.LateReviewResults != 0 || res.LateTeacherSentences != 0 {
			t.Fatalf("close=%d late review=%d late sentences=%d, want 1/0/0",
				res.WSCloseCount, res.LateReviewResults, res.LateTeacherSentences)
		}
		stored, err := f.tools.GetLessonState(context.Background(), lessonID)
		if err != nil {
			t.Fatalf("read stored lesson: %v", err)
		}
		if stored.Stage != lesson.StageChecking {
			t.Fatalf("stored stage=%q, want checking — a sealed registry admits no submit", stored.Stage)
		}
		if stored.Version != versionAfterVerdicts {
			t.Fatalf("stored version = %d, want %d (verdicts only, no latch)", stored.Version, versionAfterVerdicts)
		}
		m.TurnManager.mu.Lock()
		pending := len(m.checkWrites)
		m.TurnManager.mu.Unlock()
		if pending != 0 {
			t.Fatalf("completed check-write admissions = %d, want 0", pending)
		}
	})
}

// TestPlaybackSettleBrowser 是 1.14a 的验收：用自然播放夹具为
// playback_settle 建立对应证明。真实连接经 resume 播报逐句自然 ACK 后，
// 播报的结算（listening 复兴、state 帧、回声尾及其许可的计时器）park 在
// 生产 settle 探针上——goodbye 关闭在该窗口落地并 release 后，整组结算
// 全部被抑制：无 listening 复兴、零计时器、close 之后无任何下行帧。
func TestPlaybackSettleBrowser(t *testing.T) {
	f := newSpeechBrowserFixture(t)
	f.seedChecking()
	probe := f.useTimerProbe()
	settleGate := newSpeechOneGate()
	f.mu.Lock()
	f.settleHold = func() { settleGate.hit(); settleGate.wait() }
	f.mu.Unlock()
	t.Cleanup(settleGate.release)

	driver := f.runDriverAsync("resume-close-window", 45*time.Second)
	m := f.tryFirstManager(5 * time.Second)
	if m == nil {
		f.reclaimAndFail(driver, []func(){settleGate.release}, "no /rt connection was created before the deadline")
	}
	if !speechGateHit(settleGate, 30*time.Second) {
		f.reclaimAndFail(driver, []func(){settleGate.release}, "the naturally-acked announcement never reached its settle window")
	}
	m.handleEndSession(m.currentSender())
	settleGate.release()
	res := <-driver
	if res.WSCloseCount != 1 || res.LateFramesAfterClose != 0 {
		t.Fatalf("close=%d late frames=%d, want 1/0", res.WSCloseCount, res.LateFramesAfterClose)
	}
	m.TurnManager.mu.Lock()
	state := m.state
	m.TurnManager.mu.Unlock()
	closed := m.wasClosed()
	if !closed || state == StateListening {
		t.Fatalf("state=%q closed=%v, want the closed session never revived to listening", state, closed)
	}
	if arms := probe.count(); arms != 0 {
		t.Fatalf("word-timer arms after the closed settle = %d, want 0", arms)
	}
}

// TestStreamResultBrowser 是 1.14b 的验收：用自然播放夹具为 stream_result
// 建立对应证明。真实连接的 resume 播报在供应商块间驻留（首块已入真实传
// 输）时 goodbye 关闭——release 后旧任务不得再向已关闭的连接送出任何后
// 续块或句子，close 之后无任何下行帧。
func TestStreamResultBrowser(t *testing.T) {
	f := newSpeechBrowserFixture(t)
	f.seedChecking()
	// The FIRST provider call parks BETWEEN its two chunks: the first chunk
	// already rode the real transport, the second waits for the test.
	g := newGatedTTS(1)
	f.mu.Lock()
	f.tts = g
	f.mu.Unlock()

	driver := f.runDriverAsync("resume-close-window", 45*time.Second)
	m := f.tryFirstManager(5 * time.Second)
	if m == nil {
		f.reclaimAndFail(driver, []func(){func() { close(g.release) }}, "no /rt connection was created before the deadline")
	}
	if !speechChHit(g.entered, 30*time.Second) {
		f.reclaimAndFail(driver, []func(){func() { close(g.release) }}, "the resume stream never parked between its chunks")
	}
	m.handleEndSession(m.currentSender())
	close(g.release)
	res := <-driver
	if res.WSCloseCount != 1 || res.LateFramesAfterClose != 0 {
		t.Fatalf("close=%d late frames=%d, want 1/0 — the stale stream must ship nothing past the close",
			res.WSCloseCount, res.LateFramesAfterClose)
	}
	f.assertOriginalTasksExited()
}

// TestPracticeSubmitBrowser 是 1.10b 的复验（review P1）：加练提交的接纳
// 与保存责任经真实浏览器旅程——已提交词课堂短路结束后页面「再练一组」开
// 新连接进加练课堂（cfg.Practice 假词源 + 真实 practice store，同临时
// SQLite），加练开场与逐词播报全部自然 ACK，两次语音「写完了」推进，检
// 查面板两行人工判定、真实 UI 提交——提交闩驻留时 goodbye 关闭，已接纳
// 的保存（行翻转与 practice_logs 记录）完成，关闭后无迟到 review_result
// 或总结。撤销验证：撤掉 submitPractice 的生产接纳后本测试红。
func TestPracticeSubmitBrowser(t *testing.T) {
	f := newSpeechBrowserFixture(t)
	f.seedCompletedLesson()
	f.usePractice()
	f.scriptSpokenConfirm()
	f.scriptSpokenConfirm()
	readGate := f.gateStateRead()
	before, _ := f.gateCheckSubmit(true, false)
	t.Cleanup(before.release)
	t.Cleanup(readGate.release)

	driver := f.runDriverAsync("practice-submit-close", 60*time.Second)
	if !speechGateHit(before, 45*time.Second) {
		f.reclaimAndFail(driver, []func(){before.release}, "the practice submit never reached its pre-latch window")
	}
	readGate.hold()
	// The PRACTICE connection is the SECOND manager (the first belongs to
	// the completed short-circuit); its submit tripped the gate.
	f.mu.Lock()
	if len(f.managers) < 2 {
		f.mu.Unlock()
		f.reclaimAndFail(driver, []func(){before.release, readGate.release}, "the practice connection's manager never appeared")
	}
	m := f.managers[1]
	f.mu.Unlock()
	m.handleEndSession(m.currentSender())
	// With the production admission present, the fallback is waiting for the
	// parked submit and has not reached its authoritative read. If admission
	// is removed for the mutation check, wait until that fallback is parked at
	// the read, then let it win the latch before releasing the formal submit.
	m.TurnManager.mu.Lock()
	admitted := len(m.checkWrites) == 1
	practiceLessonID := m.tutor.lessonID
	m.TurnManager.mu.Unlock()
	if !admitted && !speechGateHit(readGate.gate, 10*time.Second) {
		f.reclaimAndFail(driver, []func(){before.release, readGate.release}, "uncoordinated fallback never reached its authoritative read")
	}
	readGate.release()
	if !admitted && !waitFor(t, 10*time.Second, func() bool {
		stored, err := f.tools.GetLessonState(context.Background(), practiceLessonID)
		return err == nil && stored.Stage == lesson.StageSubmitted && stored.Unchecked
	}) {
		f.reclaimAndFail(driver, []func(){before.release}, "uncoordinated fallback did not win the submit latch")
	}
	before.release()
	res := <-driver
	// Two closes: the completed short-circuit's own connection and the
	// practice connection's goodbye — nothing may trail the LAST one.
	if res.WSCloseCount != 2 || res.LateReviewResults != 0 || res.LateTeacherSentences != 0 {
		t.Fatalf("close=%d late review=%d late sentences=%d (texts %q), want 2/0/0",
			res.WSCloseCount, res.LateReviewResults, res.LateTeacherSentences, res.LateSentenceTexts)
	}
	// Two connections live in this journey (completed short-circuit + the
	// practice classroom) — every handler must be gone, none replaced, and
	// the accepted practice submit's own tail (plus any fallback latch) must
	// be done before the rows are read (review R0-P2: the driver's return
	// alone proves neither).
	if !waitFor(t, speechReclaimBound, f.speechServerTasksSettled) {
		t.Fatalf("server tasks did not settle: handler=%d/%d writer=%d provider=%d/%d announcement=%d/%d",
			f.handlerActive.Load(), f.handlerDone.Load(), f.writerDone.Load(),
			f.providerStarted.Load(), f.providerDone.Load(),
			f.announcementRun.Load(), f.announcementDone.Load())
	}

	m.TurnManager.mu.Lock()
	pending := len(m.checkWrites)
	m.TurnManager.mu.Unlock()
	stored, err := f.tools.GetLessonState(context.Background(), practiceLessonID)
	if err != nil {
		t.Fatalf("read stored lesson: %v", err)
	}
	if stored.Stage != lesson.StageSubmitted || stored.Unchecked {
		t.Fatalf("stored stage=%q unchecked=%v, want the accepted practice submit's submitted+checked",
			stored.Stage, stored.Unchecked)
	}
	records, err := f.practiceStore.ListPracticeByLesson(context.Background(), practiceLessonID)
	if err != nil {
		t.Fatalf("read stored practice records: %v", err)
	}
	if len(records) != 2 {
		t.Fatalf("stored practice records = %d, want both accepted words", len(records))
	}
	words := map[string]bool{}
	for _, record := range records {
		words[record.Word] = true
	}
	if !words["练习甲"] || !words["练习乙"] {
		t.Fatalf("stored practice words = %v, want 练习甲 and 练习乙", words)
	}
	if pending != 0 {
		t.Fatalf("completed check-write admissions = %d, want 0", pending)
	}
}

func (f *speechBrowserFixture) runDriverAsync(scenario string, timeout time.Duration, extraArgs ...string) <-chan speechDriverResult {
	f.t.Helper()
	done := make(chan speechDriverResult, 1)
	go func() {
		defer close(done)
		res, stderr, err := f.execSpeechDriver(scenario, timeout, extraArgs...)
		if err != nil {
			res.OK = false
			if res.Err == "" {
				res.Err = err.Error()
			}
			if stderr != "" && !strings.Contains(res.Err, stderr) {
				res.Err += "\nstderr:\n" + stderr
			}
		}
		done <- res
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
	if !waitFor(f.t, 15*time.Second, f.speechConnectionTasksSettled) {
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
				Valid                   bool `json:"valid"`
				MissingRejected         bool `json:"missingRejected"`
				EarlyListeningRejected  bool `json:"earlyListeningRejected"`
				ChunkMissingRejected    bool `json:"chunkMissingRejected"`
				ChunkMisorderedRejected bool `json:"chunkMisorderedRejected"`
			} `json:"observationAssertions"`
		} `json:"selfCheck"`
	}
	if err := json.Unmarshal([]byte(line), &res); err != nil {
		f.t.Fatalf("driver --check decode: %v\nline: %s", err, line)
	}
	if !res.OK {
		f.t.Fatalf("driver self-check violations: %v", res.Check.Violations)
	}
	a := res.Check.Assertions
	if !a.Valid || !a.MissingRejected || !a.EarlyListeningRejected || !a.ChunkMissingRejected || !a.ChunkMisorderedRejected {
		f.t.Fatalf("driver observation self-check incomplete: %+v", a)
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
			f.reclaimAndFail(driver, []func(){release, restore}, "send-site control point never hit")
		}
		release()
		restore()
		f.waitOneManager(5 * time.Second)
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
			f.reclaimAndFail(driver, []func(){release, restore}, "pump consumption control point never hit")
		}
		release()
		restore()
		f.waitOneManager(5 * time.Second)
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
			f.reclaimAndFail(driver, []func(){release}, "provider-return control point never hit")
		}
		release()
		f.waitOneManager(5 * time.Second)
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
