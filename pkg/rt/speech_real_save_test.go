//go:build rtbrowser

// speech_real_save_test.go — 1.12a 的夹具扩展：为浏览器关闭/保存竞争用
// 例补齐两类设施。
//
//   - 权威读回控制点（gateStateRead）：单发门挂在本夹具真实执行器的
//     GetLessonState 上，编排好的用例可以把「关闭后读权威行」的时机握在
//     测试手里（1.11 落地、1.12b/c 依赖的第三个控制点，与 1.09 建立的
//     CompleteCheck before/committed 门并列）。
//   - 隔离的真实 FSRS 保存链路（useRealFSRS）：到期词仍由 fake 提供以保
//     持课堂确定性，SubmitReview 换成真实 pybridge → vocab_manager.py，
//     Python 写入本夹具自己的临时 SQLite（与 Go 端同一个 vocab.db），Go
//     侧读回 review_logs 核对——需要验证 FSRS 写入次数的用例据此不再依
//     赖 fake 调用数。解释器自举复用 pybridge e2e 的丢弃型 venv 模式
//     （HTA_PYTHON_INTERPRETER > HTA_E2E_VENV > uv 临时 venv），真实环
//     境不可用时相关用例如实 Skip 并写明原因。
package rt

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xiaotian/home-tutor-agent/pkg/infra"
	"github.com/xiaotian/home-tutor-agent/pkg/lesson"
	"github.com/xiaotian/home-tutor-agent/pkg/pybridge"
)

// ---------------------------------------------------------------------------
// Interpreter bootstrap (pybridge e2e's throwaway-venv shape, package-local).
// ---------------------------------------------------------------------------

var (
	speechPyOnce   sync.Once
	speechPyInterp string
	speechPySkip   string
)

// speechPythonInterpreter resolves the real interpreter ONCE per test binary:
// an explicit HTA_PYTHON_INTERPRETER, an HTA_E2E_VENV, or a uv-built venv
// under the OS temp dir (never the deploy venv; the OS temp scavenger owns
// its cleanup, same trade pybridge's TestMain documents). The second return
// is the honest skip reason when no real Python can be built.
func speechPythonInterpreter() (interp, skipReason string) {
	speechPyOnce.Do(func() {
		switch {
		case os.Getenv("HTA_PYTHON_INTERPRETER") != "":
			speechPyInterp = os.Getenv("HTA_PYTHON_INTERPRETER")
		case os.Getenv("HTA_E2E_VENV") != "":
			speechPyInterp = filepath.Join(os.Getenv("HTA_E2E_VENV"), "bin", "python")
		default:
			interp, dir, reason, err := speechUVBootstrap()
			if err != nil {
				speechPySkip = reason
				return
			}
			speechPyInterp, _ = interp, dir
		}
		if speechPyInterp != "" {
			if _, err := os.Stat(speechPyInterp); err != nil {
				speechPySkip = fmt.Sprintf("interpreter %s unusable: %v", speechPyInterp, err)
				speechPyInterp = ""
			}
		}
	})
	return speechPyInterp, speechPySkip
}

// speechUVBootstrap creates the throwaway venv via uv and installs the
// vendored requirements into it (mirror of pybridge's e2e bootstrap).
func speechUVBootstrap() (interp, venvDir, reason string, err error) {
	if _, lookErr := exec.LookPath("uv"); lookErr != nil {
		return "", "", "uv not on PATH — set HTA_PYTHON_INTERPRETER or HTA_E2E_VENV", lookErr
	}
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		return "", "", "cannot locate third_party/python from the test file", fmt.Errorf("runtime.Caller failed")
	}
	repoRoot := filepath.Dir(filepath.Dir(filepath.Dir(file))) // pkg/rt/x.go → root
	requirements := filepath.Join(repoRoot, "third_party", "python", "requirements.txt")
	if _, statErr := os.Stat(requirements); statErr != nil {
		return "", "", "vendored requirements.txt missing: " + statErr.Error(), statErr
	}
	if venvDir, err = os.MkdirTemp("", "hta-speech-venv-*"); err != nil {
		return "", "", "mktemp failed: " + err.Error(), err
	}
	venvCmd := exec.Command("uv", "venv", "--python", "3.12", venvDir)
	if out, err := venvCmd.CombinedOutput(); err != nil {
		return "", "", fmt.Sprintf("uv venv failed: %v (%s)", err, out), err
	}
	install := exec.Command("uv", "pip", "install", "--python",
		filepath.Join(venvDir, "bin", "python"), "-r", requirements)
	install.Env = append(os.Environ(), "UV_INDEX_URL=https://pypi.tuna.tsinghua.edu.cn/simple")
	if out, err := install.CombinedOutput(); err != nil {
		return "", "", fmt.Sprintf("uv pip install failed: %v (%s)", err, out), err
	}
	return filepath.Join(venvDir, "bin", "python"), venvDir, "", nil
}

// ---------------------------------------------------------------------------
// Authoritative read-back control point.
// ---------------------------------------------------------------------------

// speechReadGate parks the FIRST GetLessonState after the test HOLDS it —
// the authoritative read the 1.11 fallback performs once its waits settle.
// Until hold() the gate is transparent (the fixture's own resume reads go
// through); after it, exactly one read parks and every later one passes.
// Production reads the state all over the place, so the parked window must
// be a test-chosen instant.
type speechReadGate struct {
	LessonExecutor
	gate  *speechOneGate
	armed atomic.Bool
	once  sync.Once
}

func (g *speechReadGate) GetLessonState(ctx context.Context, lessonID string) (lesson.Lesson, error) {
	if g.armed.Load() {
		g.once.Do(func() {
			g.gate.hit()
			g.gate.wait()
		})
	}
	return g.LessonExecutor.GetLessonState(ctx, lessonID)
}

// hold arms the one-shot park; release (idempotent) releases the parked read.
func (g *speechReadGate) hold()    { g.armed.Store(true) }
func (g *speechReadGate) release() { g.gate.release() }

// gateStateRead mounts the one-shot authoritative-read hold on every future
// manager's executor chain (1.12a). The gate is passive until hold().
func (f *speechBrowserFixture) gateStateRead() *speechReadGate {
	f.mu.Lock()
	defer f.mu.Unlock()
	g := &speechReadGate{LessonExecutor: f.exec, gate: newSpeechOneGate()}
	f.exec = g
	return g
}

// ---------------------------------------------------------------------------
// Isolated REAL save chain (fake due words + real pybridge SubmitReview).
// ---------------------------------------------------------------------------

// speechFSRSSource keeps the fixture's deterministic fake due words but sends
// SubmitReview through the REAL pybridge chain, counting the calls and
// recording the reported sets so assertions can pair the call count with the
// rows Python actually wrote.
type speechFSRSSource struct {
	due   *fakeWordSource
	real  WordSource
	calls atomic.Int32

	mu      sync.Mutex
	results []ReviewWriteResult
}

func (s *speechFSRSSource) FetchDueWords(ctx context.Context, limit int, category string) ([]lesson.Word, error) {
	return s.due.FetchDueWords(ctx, limit, category)
}

func (s *speechFSRSSource) PeekDueWords(ctx context.Context, limit int, category string) ([]lesson.Word, error) {
	return s.due.PeekDueWords(ctx, limit, category)
}

func (s *speechFSRSSource) SubmitReview(ctx context.Context, results map[string]string, category string) (ReviewWriteResult, error) {
	s.calls.Add(1)
	res, err := s.real.SubmitReview(ctx, results, category)
	if err == nil {
		s.mu.Lock()
		s.results = append(s.results, res)
		s.mu.Unlock()
	}
	return res, err
}

// submitCalls reports how many real SubmitReview calls left the manager.
func (s *speechFSRSSource) submitCalls() int32 { return s.calls.Load() }

// useRealFSRS mounts the real FSRS save chain on this fixture (1.12a): the
// bridge's Python owns the SAME temp vocab.db the Go lesson store uses, the
// fixture's two classroom cards are seeded through RunAdd so the Python store
// knows them, and a real receipt store is wired so submit/fallback receipts
// land in the same temp SQLite. Skips (with the reason) when no real Python
// interpreter can be built.
func (f *speechBrowserFixture) useRealFSRS(t *testing.T) *speechFSRSSource {
	t.Helper()
	interp, skip := speechPythonInterpreter()
	if skip != "" {
		t.Skipf("real FSRS chain unavailable: %s", skip)
	}
	base := filepath.Dir(f.dbPath)
	scripts := filepath.Join(speechRepoRoot(t), "third_party", "python")
	cfg := infra.PythonConfig{
		Interpreter: interp,
		ScriptsDir:  scripts,
		// pybridge passes the script paths as argv[0] — absolute, not a
		// bare filename (pe2eStack's shape).
		VocabScript:   filepath.Join(scripts, "vocab_manager.py"),
		PregenScript:  filepath.Join(scripts, "pregenerate_dictation.py"),
		VocabDBPath:   f.dbPath,
		DataDir:       filepath.Join(base, "data"),
		CacheDir:      filepath.Join(base, "cache", "audio"),
		ManifestDir:   filepath.Join(base, "cache"),
		DueTimeout:    30 * time.Second,
		ReviewTimeout: 30 * time.Second,
		PregenTimeout: 30 * time.Second,
		MgmtTimeout:   30 * time.Second,
		MaxConcurrent: 2,
	}
	for _, d := range []string{cfg.DataDir, cfg.CacheDir, cfg.ManifestDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}
	b, err := pybridge.NewBridge(cfg, infra.NewNoOpLogger())
	if err != nil {
		t.Fatalf("pybridge: %v", err)
	}
	// Seed the classroom's two cards through the REAL add path so the
	// Python store knows the words the fixture's classrooms rate.
	add, err := b.RunAdd(context.Background(), pybridge.AddOpts{
		Category: "chinese", Unit: "speech-fixture",
		Entries: []pybridge.AddEntry{
			{Word: "听写浏览器", Meaning: "fixture 第一词"},
			{Word: "第二词", Meaning: "fixture 第二词"},
		},
	})
	if err != nil || add.Added != 2 {
		t.Fatalf("RunAdd = %+v, %v; want 2 seeded cards", add, err)
	}
	py, err := NewPyFSRS(b, infra.NewNoOpLogger())
	if err != nil {
		t.Fatalf("NewPyFSRS: %v", err)
	}
	receipts, err := lesson.NewReceiptStore(f.db, infra.NewNoOpLogger())
	if err != nil {
		t.Fatalf("receipt store: %v", err)
	}
	f.mu.Lock()
	f.wordsOverride = &speechFSRSSource{due: f.words, real: py}
	f.receipts = receipts
	f.mu.Unlock()
	return f.wordsOverride.(*speechFSRSSource)
}

// ---------------------------------------------------------------------------
// 1.12a fixture self-checks.
// ---------------------------------------------------------------------------

// TestSpeechFixtureControlPoints 自检 1.12a 的控制设施：权威读回门在第
// 一次 GetLessonState 命中并驻留、后续读直通；1.09 建立的提交两窗门在
// before 与 committed 位置都能命中。全部控制点经 release 释放后没有滞留
// 的等待方。
func TestSpeechFixtureControlPoints(t *testing.T) {
	t.Run("one gate separates hit from release", func(t *testing.T) {
		g := newSpeechOneGate()

		// release() must NEVER close entered (计划 §5.1): releasing a
		// control point that was never hit is not — and must not
		// fabricate — a hit observation.
		g.release()
		select {
		case <-g.entered:
			t.Fatal("release closed entered — 解除控制点伪造了命中")
		default:
		}
		if got := g.hitCount(); got != 0 {
			t.Fatalf("hit count after release-only = %d, want 0", got)
		}

		// A late arrival still passes straight through the spent gate and is
		// honestly counted as a hit.
		g.hit()
		g.wait()
		select {
		case <-g.entered:
		default:
			t.Fatal("hit after release did not signal entered")
		}
		if got := g.hitCount(); got != 1 {
			t.Fatalf("hit count = %d, want 1", got)
		}

		// Repeated hits keep counting (the multi-write batch observation)
		// while entered stays single-shot and release stays idempotent.
		g.hit()
		g.release()
		if got := g.hitCount(); got != 2 {
			t.Fatalf("hit count = %d, want 2 (every arrival counts)", got)
		}
	})

	t.Run("state read gate parks the first held read only", func(t *testing.T) {
		exec := newFakeExecutor(ttLessonID, ttCards("苹果"))
		g := &speechReadGate{LessonExecutor: exec, gate: newSpeechOneGate()}

		// Before hold() the gate is transparent — the fixture/resume reads
		// pass straight through.
		early := make(chan lesson.Lesson, 1)
		go func() {
			l, err := g.GetLessonState(context.Background(), ttLessonID)
			if err != nil {
				t.Errorf("early read: %v", err)
			}
			early <- l
		}()
		select {
		case <-early:
		case <-time.After(5 * time.Second):
			// Nothing is parked (the gate is not held) — drain the in-flight
			// read before failing so no goroutine outlives the assertion.
			select {
			case <-early:
			case <-time.After(5 * time.Second):
			}
			t.Fatal("a passive read never finished on an unheld gate")
		}

		g.hold()
		first := make(chan lesson.Lesson, 1)
		go func() {
			l, err := g.GetLessonState(context.Background(), ttLessonID)
			if err != nil {
				t.Errorf("first read: %v", err)
			}
			first <- l
		}()
		select {
		case <-g.gate.entered:
		case <-time.After(5 * time.Second):
			// The gate is armed: the probing read may still be about to park.
			// Release and drain it BEFORE failing (§5.1) — a bare Fatal would
			// strand the goroutine on the channel with no reclaim.
			g.release()
			select {
			case <-first:
			case <-time.After(5 * time.Second):
			}
			t.Fatal("the first held read never hit the gate")
		}
		select {
		case l := <-first:
			t.Fatalf("held read returned while parked: %+v", l)
		case <-time.After(100 * time.Millisecond):
		}
		g.release()
		select {
		case <-first:
		case <-time.After(5 * time.Second):
			t.Fatal("the parked read never resumed after release")
		}
		// The second read passes straight through — the hold is one-shot.
		done := make(chan lesson.Lesson, 1)
		go func() {
			l, err := g.GetLessonState(context.Background(), ttLessonID)
			if err != nil {
				t.Errorf("second read: %v", err)
			}
			done <- l
		}()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("the second read parked on a spent one-shot gate")
		}
	})

	t.Run("submit gates hold their two windows", func(t *testing.T) {
		exec := newFakeExecutor(ttLessonID, ttCards("苹果"))
		g := &speechSubmitGate{LessonExecutor: exec, before: newSpeechOneGate(), committed: newSpeechOneGate()}
		ctx := context.Background()

		res := make(chan lesson.CompleteCheckResult, 1)
		go func() {
			r, err := g.CompleteCheck(ctx, ttLessonID, 1, "cp-submit", false)
			if err != nil {
				t.Errorf("complete check: %v", err)
			}
			res <- r
		}()
		select {
		case <-g.before.entered:
		case <-time.After(5 * time.Second):
			g.before.release()
			g.committed.release()
			select {
			case <-res:
			case <-time.After(5 * time.Second):
			}
			t.Fatal("the submit never reached its before window")
		}
		g.before.release()
		select {
		case <-g.committed.entered:
		case <-time.After(5 * time.Second):
			g.committed.release()
			select {
			case <-res:
			case <-time.After(5 * time.Second):
			}
			t.Fatal("the submit never reached its committed window")
		}
		select {
		case r := <-res:
			t.Fatalf("submit returned inside the committed window: %+v", r)
		case <-time.After(100 * time.Millisecond):
		}
		g.committed.release()
		select {
		case <-res:
		case <-time.After(5 * time.Second):
			t.Fatal("the submit never resumed after release")
		}
	})
}

// TestSpeechRealSaveSharedDB 自检真实保存链路的隔离性：RunAdd 的卡片由
// Python 写进本夹具的临时库、Go 能从同一文件读回；一次真实 SubmitReview
// 恰好落两行 review_logs，调用计数与持久行数一致。解释器不可用时如实
// Skip。
func TestSpeechRealSaveSharedDB(t *testing.T) {
	f := newSpeechBrowserFixture(t)
	probe := f.useRealFSRS(t)

	// Python wrote the seeded cards; Go reads the same SQLite file.
	var cards int
	if err := f.db.QueryRow(`SELECT COUNT(*) FROM cards WHERE word IN ('听写浏览器', '第二词')`).Scan(&cards); err != nil {
		t.Fatalf("read python-seeded cards: %v", err)
	}
	if cards != 2 {
		t.Fatalf("python-seeded cards = %d, want 2 — Go/Python are not on the same temp DB", cards)
	}

	res, err := probe.SubmitReview(context.Background(), map[string]string{
		"听写浏览器": ratingGood,
		"第二词":   ratingHard,
	}, "chinese")
	if err != nil {
		t.Fatalf("real submit review: %v", err)
	}
	if len(res.Updated) != 2 {
		t.Fatalf("real review updated = %v (requested %v), want both words", res.Updated, res.Requested)
	}
	if calls := probe.submitCalls(); calls != 1 {
		t.Fatalf("real submit calls = %d, want 1", calls)
	}
	var logs int
	if err := f.db.QueryRow(`SELECT COUNT(*) FROM review_logs`).Scan(&logs); err != nil {
		t.Fatalf("read review_logs: %v", err)
	}
	if logs != 2 {
		t.Fatalf("review_logs rows = %d, want 2 — the real save must land in the shared DB", logs)
	}
}
