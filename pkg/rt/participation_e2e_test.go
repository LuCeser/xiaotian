//go:build e2e

// participation_e2e_test.go — Issue #2's classroom acceptance link: the
// parent's participation switch must reach the ACTUAL lesson selection and
// learning records, not just the management views.
//
// What this proves over the pybridge-level E2E (TestE2E_SetParticipation*):
// the real /rt WebSocket transport, the real lesson/practice stores (Go
// SQLite tables in the same file Python owns) and the real TurnManager FSM
// all honor participates=0 —
//
//   - due lesson: session_init selects 10 of the 11 participating cards
//     and NEVER a disabled one (the disabled cards are seeded FIRST, so a
//     broken filter would surface them in the first-10 window);
//   - a full classroom run writes review_logs rows for exactly the
//     dictated cards and leaves every disabled card's fsrs_card
//     byte-identical;
//   - practice (加练) selects from participating minus today's dictated
//     words — disabled cards stay out — and records to practice_logs only
//     (review_logs unchanged: practice never touches the FSRS schedule,
//     D-12).
//
// Only the external voice providers are faked (ASR/LLM/TTS — the ticket's
// test boundary allows fixed responses for external recognition/generation
// services); word selection, the DB and Python are the real production
// stack, assembled exactly like cmd/service/main.go wires them.
//
// Build tag: only under `go test -tags=e2e ./pkg/rt`. Interpreter
// bootstrap mirrors pkg/pybridge/e2e_test.go's TestMain precedence
// (HTA_PYTHON_INTERPRETER > HTA_E2E_VENV > uv venv + Tsinghua index);
// tests skip cleanly when no interpreter is available.
package rt

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/xiaotian/home-tutor-agent/pkg/database"
	"github.com/xiaotian/home-tutor-agent/pkg/infra"
	"github.com/xiaotian/home-tutor-agent/pkg/lesson"
	"github.com/xiaotian/home-tutor-agent/pkg/pybridge"

	_ "modernc.org/sqlite" // same pure-Go driver pkg/database uses
)

// Shared bootstrap state, populated once by TestMain (mirrors
// pkg/pybridge/e2e_test.go; "" interpreter ⇒ every test skips).
var (
	pe2eInterpreter string
	pe2eScriptsDir  string
	pe2eVenvDir     string
	pe2eSkipReason  string
)

func TestMain(m *testing.M) {
	if _, file, _, ok := runtime.Caller(0); ok {
		pkgDir := filepath.Dir(file)
		pe2eScriptsDir = filepath.Join(filepath.Dir(filepath.Dir(pkgDir)), "third_party", "python")
	}
	switch {
	case os.Getenv("HTA_PYTHON_INTERPRETER") != "":
		pe2eInterpreter = os.Getenv("HTA_PYTHON_INTERPRETER")
	case os.Getenv("HTA_E2E_VENV") != "":
		pe2eInterpreter = pe2eVenvInterpreter(os.Getenv("HTA_E2E_VENV"))
	default:
		interp, venvDir, reason, err := pe2eUvBootstrap()
		if err != nil {
			pe2eSkipReason = reason
		} else {
			pe2eInterpreter, pe2eVenvDir = interp, venvDir
		}
	}
	code := m.Run()
	if pe2eVenvDir != "" {
		_ = os.RemoveAll(pe2eVenvDir)
	}
	os.Exit(code)
}

// pe2eVenvInterpreter resolves the python binary inside a venv for either
// platform layout (pybridge venvInterpreter mirror).
func pe2eVenvInterpreter(venvDir string) string {
	if runtime.GOOS == "windows" {
		return filepath.Join(venvDir, "Scripts", "python.exe")
	}
	return filepath.Join(venvDir, "bin", "python")
}

// pe2eUvBootstrap builds a throwaway uv venv with the vendored
// requirements (China Tsinghua index by default — memory:
// china-mirror-installs; UV_INDEX_URL overrides).
func pe2eUvBootstrap() (interp, venvDir, reason string, err error) {
	if _, lookErr := exec.LookPath("uv"); lookErr != nil {
		return "", "", "uv not on PATH — set HTA_PYTHON_INTERPRETER or HTA_E2E_VENV", lookErr
	}
	venvDir, err = os.MkdirTemp("", "hta-rt-e2e-venv-*")
	if err != nil {
		return "", "", "mktemp failed: " + err.Error(), err
	}
	if err = exec.Command("uv", "venv", "--python", "3.12", venvDir).Run(); err != nil {
		return "", "", "uv venv failed: " + err.Error(), err
	}
	interp = pe2eVenvInterpreter(venvDir)
	index := os.Getenv("UV_INDEX_URL")
	if index == "" {
		index = "https://pypi.tuna.tsinghua.edu.cn/simple"
	}
	install := exec.Command("uv", "pip", "install", "--python", interp,
		"-r", filepath.Join(pe2eScriptsDir, "requirements.txt"))
	install.Env = append(os.Environ(), "UV_INDEX_URL="+index)
	if err = install.Run(); err != nil {
		return "", "", "uv pip install failed: " + err.Error(), err
	}
	return interp, venvDir, "", nil
}

// pe2eBridge assembles the production-shaped stack over one temp SQLite
// file: the real pybridge.Bridge (real interpreter + vendored scripts) and
// the real Go-owned stores in the same file, exactly like
// cmd/service/main.go's wiring. Returns the bridge, the lesson tools
// adapter, the practice store and the shared card repository.
func pe2eStack(t *testing.T) (*pybridge.Bridge, *LessonTools, *lesson.PracticeStore, database.CardRepository, *sql.DB, infra.PythonConfig) {
	t.Helper()
	if pe2eInterpreter == "" {
		t.Skipf("real-Python E2E unavailable: %s", pe2eSkipReason)
	}

	base := t.TempDir()
	cfg := infra.PythonConfig{
		Interpreter:   pe2eInterpreter,
		ScriptsDir:    pe2eScriptsDir,
		VocabScript:   filepath.Join(pe2eScriptsDir, "vocab_manager.py"),
		PregenScript:  filepath.Join(pe2eScriptsDir, "pregenerate_dictation.py"),
		VocabDBPath:   filepath.Join(base, "vocab.db"),
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
		t.Fatalf("NewBridge: %v", err)
	}

	db, err := database.Open(cfg.VocabDBPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
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
	practiceStore, err := lesson.NewPracticeStore(db, infra.NewNoOpLogger())
	if err != nil {
		t.Fatalf("practice store: %v", err)
	}
	cardRepo, err := database.NewCardRepository(db, infra.NewNoOpLogger())
	if err != nil {
		t.Fatalf("card repository: %v", err)
	}
	return b, tools, practiceStore, cardRepo, db, cfg
}

// pe2eParticipates reads one card's flag straight from SQLite.
func pe2eParticipates(t *testing.T, db *sql.DB, id int64) bool {
	t.Helper()
	var flag int
	if err := db.QueryRow("SELECT participates FROM cards WHERE id = ?", id).Scan(&flag); err != nil {
		t.Fatalf("read participates id=%d: %v", id, err)
	}
	return flag == 1
}

// pe2eFsrs reads one card's raw fsrs_card column — the byte-level proof
// that disabled cards keep their learning state.
func pe2eFsrs(t *testing.T, db *sql.DB, id int64) string {
	t.Helper()
	var fsrs string
	if err := db.QueryRow("SELECT fsrs_card FROM cards WHERE id = ?", id).Scan(&fsrs); err != nil {
		t.Fatalf("read fsrs_card id=%d: %v", id, err)
	}
	return fsrs
}

// pe2eReviewCardIDs lists the card ids the Python-owned review_logs table
// recorded so far — the real learning-record evidence.
func pe2eReviewCardIDs(t *testing.T, db *sql.DB) []int64 {
	t.Helper()
	rows, err := db.Query("SELECT card_id FROM review_logs ORDER BY card_id")
	if err != nil {
		t.Fatalf("query review_logs: %v", err)
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			t.Fatalf("scan review_logs: %v", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("review_logs rows: %v", err)
	}
	return ids
}

// TestE2E_ParticipationShapesClassroomSelection is the Issue #2 classroom
// acceptance: parent-disabled cards stay out of BOTH selection entries
// (due lesson + practice), the dictated cards' learning records land in
// review_logs, and disabled cards keep their FSRS bytes untouched.
func TestE2E_ParticipationShapesClassroomSelection(t *testing.T) {
	ctx := context.Background()
	b, tools, practiceStore, cardRepo, db, _ := pe2eStack(t)

	// 14 cards in ONE real add: the three disabled candidates FIRST (a
	// broken filter would surface them inside the first-10 due window),
	// then eleven participating cards (10 get dictated, 1 stays for
	// practice — practice must exclude disabled AND already-dictated).
	disabledWords := []string{"partX一", "partX二", "partX三"}
	enabledWords := make([]string, 11)
	for i := range enabledWords {
		enabledWords[i] = fmt.Sprintf("partA%02d词", i+1)
	}
	entries := make([]pybridge.AddEntry, 0, len(disabledWords)+len(enabledWords))
	for _, w := range disabledWords {
		entries = append(entries, pybridge.AddEntry{Word: w, Meaning: "已停用"})
	}
	for _, w := range enabledWords {
		entries = append(entries, pybridge.AddEntry{Word: w, Meaning: "参与中"})
	}
	add, err := b.RunAdd(ctx, pybridge.AddOpts{
		Category: "english", Unit: "part-e2e", Entries: entries,
	})
	if err != nil || add.Added != len(entries) {
		t.Fatalf("RunAdd = %+v, %v; want %d added", add, err, len(entries))
	}
	// Fresh DB ⇒ autoincrement ids are 1..14 in insertion order: the three
	// disabled cards are ids 1..3 (this convention is pinned by
	// pkg/pybridge's E2E suite).
	disabledIDs := []int64{1, 2, 3}
	res, err := b.RunSetParticipation(ctx, pybridge.SetParticipationOpts{
		IDs: disabledIDs, Participates: false,
	})
	if err != nil || res.Updated != len(disabledIDs) {
		t.Fatalf("disable = %+v, %v; want all %d updated", res, err, len(disabledIDs))
	}
	for _, id := range disabledIDs {
		if pe2eParticipates(t, db, id) {
			t.Fatalf("card %d still participating after disable", id)
		}
	}
	disabledFsrs := make(map[int64]string, len(disabledIDs))
	for _, id := range disabledIDs {
		disabledFsrs[id] = pe2eFsrs(t, db, id)
	}

	// The production wiring: real Python word source, real stores, faked
	// voice providers (the ticket's fixed-response allowance).
	pyWords, err := NewPyFSRS(b, infra.NewNoOpLogger())
	if err != nil {
		t.Fatalf("NewPyFSRS: %v", err)
	}
	turns := len(enabledWords) // 10 due confirms + 1 practice confirm
	asrScripts := make([]asrScript, turns)
	llmScripts := make([]llmScript, turns)
	for i := range asrScripts {
		asrScripts[i] = asrScript{text: "好了"}
		llmScripts[i] = intentScript(intentConfirm, "")
	}
	asr := newFakeASR(asrScripts...)
	llm := newFakeLLM(llmScripts...)
	tts := newFakeTTS(2, 320)
	tts.delay = 20 * time.Millisecond
	h, err := NewHandler(
		ServerConfig{AllowedOrigins: []string{itOrigin}, HeartbeatInterval: 30 * time.Second},
		func() SessionManager {
			return NewTurnManager(asr, llm, tts, tools, TurnManagerConfig{
				LessonID: "e2e-part-lesson",
				Words:    pyWords,
				Practice: cardRepo, PracticeStore: practiceStore,
			}, NewEventRecorder(infra.NewNoOpLogger(), "e2e-part"),
				infra.NewNoOpLogger())
		},
		infra.NewNoOpLogger(),
	)
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}
	srv := httptest.NewServer(http.HandlerFunc(h.HandleWebSocket))
	t.Cleanup(srv.Close)

	inSet := func(list []string, set map[string]bool) bool {
		for _, w := range list {
			if !set[w] {
				return false
			}
		}
		return true
	}
	toSet := func(list []string) map[string]bool {
		set := make(map[string]bool, len(list))
		for _, w := range list {
			set[w] = true
		}
		return set
	}
	disabledSet := toSet(disabledWords)

	// --- due classroom: selection excludes the disabled cards -------------
	conn := itDial(t, srv)
	itSendText(t, conn, SessionInitMsg{
		Type: TypeSessionInit, Mode: ModeTest,
		Audio: AudioSpec{SampleRate: 16000, Format: "s16le", Channels: 1},
	})
	readyRaw := itRead(t, conn, 30*time.Second, func(m itMsg) bool { return m.typ == TypeSessionReady })
	var ready SessionReadyMsg
	if err := json.Unmarshal(readyRaw.raw, &ready); err != nil {
		t.Fatalf("session_ready decode: %v", err)
	}
	dueWords := ready.Lesson.Words
	if len(dueWords) != 10 {
		t.Fatalf("due lesson words = %v, want exactly 10 (11 participants, limit 10)", dueWords)
	}
	for _, w := range dueWords {
		if disabledSet[w] {
			t.Fatalf("disabled word %q selected for the due lesson", w)
		}
	}
	if !inSet(dueWords, toSet(enabledWords)) {
		t.Fatalf("due words %v contain non-participants", dueWords)
	}
	dueSet := toSet(dueWords)

	// Drive the full three-stage classroom over the real wire.
	itAnnounceAck(t, conn)
	for i := 0; i < len(dueWords); i++ {
		itSendUtterance(t, conn)
		upd := itRead(t, conn, 15*time.Second, func(m itMsg) bool { return m.typ == TypeLessonUpdate })
		var lu LessonUpdateMsg
		if err := json.Unmarshal(upd.raw, &lu); err != nil {
			t.Fatalf("lesson_update decode: %v", err)
		}
		if i < len(dueWords)-1 {
			itAnnounceAck(t, conn) // next-word announcement
		}
	}
	started := itReadCheckStarted(t, conn)
	if len(started.Words) != 10 || !inSet(started.Words, dueSet) {
		t.Fatalf("check_started words = %v, want the dictated 10", started.Words)
	}
	itAnnounceAck(t, conn) // the check bridge
	for i := 0; i < len(dueWords); i++ {
		itSendText(t, conn, CheckVerdictMsg{
			Type: TypeCheckVerdict, RequestID: fmt.Sprintf("e2e-part-v-%d", i),
			WordIndex: i, Correct: true,
		})
		echo := itReadCheckVerdicts(t, conn)
		if len(echo.Verdicts) != i+1 {
			t.Fatalf("check_verdicts %d = %+v, want %d entries", i, echo.Verdicts, i+1)
		}
	}
	itSendText(t, conn, CheckSubmitMsg{Type: TypeCheckSubmit, RequestID: "e2e-part-submit"})
	rr := itRead(t, conn, 30*time.Second, func(m itMsg) bool { return m.typ == TypeReviewResult })
	var result ReviewResultMsg
	if err := json.Unmarshal(rr.raw, &result); err != nil {
		t.Fatalf("review_result decode: %v", err)
	}
	if result.Requested != 10 || result.Updated != 10 || result.Missing != 0 {
		t.Fatalf("review_result = %+v, want requested/updated 10, missing 0", result)
	}
	itAckEnding(t, conn, ReasonLessonComplete)

	// Learning records: review_logs holds exactly the dictated cards; the
	// disabled cards' FSRS bytes are untouched.
	reviewed := pe2eReviewCardIDs(t, db)
	if len(reviewed) != 10 {
		t.Fatalf("review_logs rows = %v, want exactly the 10 dictated cards", reviewed)
	}
	for _, id := range reviewed {
		if id <= 3 {
			t.Fatalf("disabled card %d received a review row", id)
		}
	}
	for id, before := range disabledFsrs {
		if after := pe2eFsrs(t, db, id); after != before {
			t.Fatalf("disabled card %d fsrs_card changed by the classroom:\nbefore %s\nafter  %s", id, before, after)
		}
	}

	// --- practice: participating minus already-dictated, never disabled ---
	conn2 := itDial(t, srv)
	itSendText(t, conn2, SessionInitMsg{
		Type: TypeSessionInit, Mode: ModeTest, Practice: true,
		Audio: AudioSpec{SampleRate: 16000, Format: "s16le", Channels: 1},
	})
	ready2Raw := itRead(t, conn2, 30*time.Second, func(m itMsg) bool { return m.typ == TypeSessionReady })
	var ready2 SessionReadyMsg
	if err := json.Unmarshal(ready2Raw.raw, &ready2); err != nil {
		t.Fatalf("practice session_ready decode: %v", err)
	}
	practiceWords := ready2.Lesson.Words
	if len(practiceWords) != 1 {
		t.Fatalf("practice words = %v, want the single leftover participant", practiceWords)
	}
	for _, w := range practiceWords {
		if disabledSet[w] || dueSet[w] {
			t.Fatalf("practice word %q is disabled or already dictated today", w)
		}
	}
	if !inSet(practiceWords, toSet(enabledWords)) {
		t.Fatalf("practice words %v outside the participating pool", practiceWords)
	}

	itAnnounceAck(t, conn2)
	itSendUtterance(t, conn2)
	itRead(t, conn2, 15*time.Second, func(m itMsg) bool { return m.typ == TypeLessonUpdate })
	started2 := itReadCheckStarted(t, conn2)
	if len(started2.Words) != 1 {
		t.Fatalf("practice check_started words = %v, want 1", started2.Words)
	}
	itAnnounceAck(t, conn2)
	itSendText(t, conn2, CheckVerdictMsg{
		Type: TypeCheckVerdict, RequestID: "e2e-part-practice-v", WordIndex: 0, Correct: true,
	})
	if echo := itReadCheckVerdicts(t, conn2); len(echo.Verdicts) != 1 {
		t.Fatalf("practice verdict echo = %+v, want 1 entry", echo.Verdicts)
	}
	itSendText(t, conn2, CheckSubmitMsg{Type: TypeCheckSubmit, RequestID: "e2e-part-practice-submit"})
	rr2 := itRead(t, conn2, 30*time.Second, func(m itMsg) bool { return m.typ == TypeReviewResult })
	var result2 ReviewResultMsg
	if err := json.Unmarshal(rr2.raw, &result2); err != nil {
		t.Fatalf("practice review_result decode: %v", err)
	}
	if result2.Requested != 1 || result2.Updated != 1 {
		t.Fatalf("practice review_result = %+v, want requested/updated 1", result2)
	}
	itAckEnding(t, conn2, ReasonPracticeComplete)

	// D-12: practice records to practice_logs ONLY — review_logs still
	// holds exactly the 10 classroom rows, FSRS bytes everywhere intact.
	if reviewedAfter := pe2eReviewCardIDs(t, db); len(reviewedAfter) != 10 {
		t.Fatalf("review_logs rows after practice = %v, want still 10 (practice never writes FSRS)", reviewedAfter)
	}
	day := time.Now().In(time.FixedZone("CST", 8*3600)).Format("2006-01-02")
	practiced, err := practiceStore.ListPracticeWordsOn(ctx, day)
	if err != nil {
		t.Fatalf("ListPracticeWordsOn: %v", err)
	}
	if len(practiced) != 1 || practiced[0] != practiceWords[0] {
		t.Fatalf("practice_logs for %s = %v, want [%s]", day, practiced, practiceWords[0])
	}
	for id, before := range disabledFsrs {
		if after := pe2eFsrs(t, db, id); after != before {
			t.Fatalf("disabled card %d fsrs_card changed by practice", id)
		}
	}
}
