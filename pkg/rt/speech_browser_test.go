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
	"testing"
	"time"

	"github.com/xiaotian/home-tutor-agent/pkg/database"
	"github.com/xiaotian/home-tutor-agent/pkg/infra"
	"github.com/xiaotian/home-tutor-agent/pkg/lesson"
)

// speechPageAck is one sentence_ack the real page sent (observed read-only).
type speechPageAck struct {
	Epoch      uint64 `json:"epoch"`
	SentenceID string `json:"sentenceId"`
}

// speechPlaybackObservation mirrors rt-observe.js playbackState (subset).
type speechPlaybackObservation struct {
	Trace         []string `json:"trace"`
	DroppedChunks int      `json:"droppedChunks"`
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
	closed bool
}

func (m *speechManagerTracker) HandleControl(s Sender, data []byte) {
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

func (m *speechManagerTracker) Close(s Sender) {
	m.TurnManager.Close(s)
	m.mu.Lock()
	m.closed = true
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

// speechBrowserFixture is one test's isolated production-shaped server.
type speechBrowserFixture struct {
	t *testing.T

	ln  net.Listener
	srv *http.Server

	asr   *fakeASR
	llm   *fakeLLM
	tts   *fakeTTS
	words *fakeWordSource
	tools *LessonTools
	db    *sql.DB

	mu       sync.Mutex
	managers []*speechManagerTracker
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
		t:     t,
		db:    db,
		tools: tools,
		asr:   newFakeASR(),
		llm:   newFakeLLM(),
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
	mux.HandleFunc("/rt", handler.HandleWebSocket)
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
	tm := NewTurnManager(f.asr, f.llm, f.tts, f.tools, TurnManagerConfig{
		LessonID: "speech-browser-fixture",
		Words:    f.words,
	}, NewEventRecorder(infra.NewNoOpLogger(), "speech-browser"), infra.NewNoOpLogger())
	m := &speechManagerTracker{TurnManager: tm, fixture: f}
	f.mu.Lock()
	f.managers = append(f.managers, m)
	f.mu.Unlock()
	return m
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
// The returned error is non-nil only for process-level failures; a failed
// scenario comes back as speechDriverResult{OK:false, Err:...} with the
// driver's combined output attached for diagnosis.
func (f *speechBrowserFixture) runDriver(scenario string, timeout time.Duration) speechDriverResult {
	f.t.Helper()
	root := speechRepoRoot(f.t)
	driver := filepath.Join(root, "pkg", "rt", "testdata", "speech-browser", "driver.js")

	ctx, cancel := context.WithTimeout(context.Background(), timeout+15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "node", driver,
		"--url", f.url()+"tutor/",
		"--scenario", scenario,
		"--timeout-ms", fmt.Sprint(int64(timeout/time.Millisecond)),
	)
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

// TestSpeechBrowserFixture is 步骤 2 的验收：真实生产页面能连上进程内真实
// /rt 服务端，自然完成一段非零 PCM 的教师播报（原生 onended → 真实
// sentence_ack，服务器侧门控解除、计时启动），销毁后无遗留连接与进程。
func TestSpeechBrowserFixture(t *testing.T) {
	f := newSpeechBrowserFixture(t)

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
	onended := false
	for _, tr := range res.Playback.Trace {
		if strings.HasPrefix(tr, "onended:") {
			t.Logf("native playback completion observed: %s", tr)
			onended = true
			break
		}
	}
	if !onended {
		t.Fatalf("no native onended in playback trace %v (droppedChunks=%d)", res.Playback.Trace, res.Playback.DroppedChunks)
	}
	if res.Playback.DroppedChunks != 0 {
		t.Fatalf("playback dropped %d chunks — the fixture PCM must play whole", res.Playback.DroppedChunks)
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
