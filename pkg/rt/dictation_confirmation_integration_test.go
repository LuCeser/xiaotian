// dictation_confirmation_integration_test.go — Issue #56 end-to-end: the
// natural confirmation phrases drive the REAL voice loop (websocket frames →
// VAD → ASR → classification) over a REAL lesson store (temp SQLite through
// LessonTools), with the model deliberately misjudging every dictation-turn
// phrase so the chain proves the correction — not the fake model's
// cooperation. The last word opens the check stage exactly once, the confirm
// itself writes no FSRS, the existing manual-verdict/submit messages finish
// the lesson, and duplicates (a repeated 「好了」 in the check stage, a
// repeated check_submit) write nothing extra.
package rt

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xiaotian/home-tutor-agent/pkg/database"
	"github.com/xiaotian/home-tutor-agent/pkg/infra"
	"github.com/xiaotian/home-tutor-agent/pkg/lesson"
)

// dcConfirmLessonID is the daily row id this file's classrooms bind to.
const dcConfirmLessonID = "dc-confirm"

// dcConfirmStack mounts one /rt server over the REAL lesson store (temp
// SQLite, migrated, through LessonTools) and hands back the manager slot
// (filled when the first connection upgrades) for white-box timer
// assertions.
func dcConfirmStack(t *testing.T, asr ASRProvider, llm LLMProvider, tts TTSSynthesizer, words WordSource, mutate func(*TurnManagerConfig)) (*httptest.Server, *atomic.Pointer[TurnManager], *lesson.Executor) {
	t.Helper()
	db, err := database.Open(filepath.Join(t.TempDir(), "dc-confirm.db"))
	if err != nil {
		t.Fatalf("open temp db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()
	if err = lesson.Migrate(ctx, db); err != nil {
		t.Fatalf("lesson migrate: %v", err)
	}
	store, err := lesson.NewStore(db, nil)
	if err != nil {
		t.Fatalf("lesson store: %v", err)
	}
	exec, err := lesson.NewExecutor(store, nil)
	if err != nil {
		t.Fatalf("lesson executor: %v", err)
	}
	tools, err := NewLessonTools(store, exec)
	if err != nil {
		t.Fatalf("lesson tools: %v", err)
	}

	var captured atomic.Pointer[TurnManager]
	h, err := NewHandler(
		ServerConfig{AllowedOrigins: []string{itOrigin}, HeartbeatInterval: 30 * time.Second},
		func() SessionManager {
			cfg := TurnManagerConfig{LessonID: dcConfirmLessonID, Words: words}
			if mutate != nil {
				mutate(&cfg)
			}
			tm := NewTurnManager(asr, llm, tts, tools, cfg,
				NewEventRecorder(infra.NewNoOpLogger(), "dc"), infra.NewNoOpLogger())
			captured.Store(tm)
			return tm
		},
		infra.NewNoOpLogger(),
	)
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}
	srv := httptest.NewServer(http.HandlerFunc(h.HandleWebSocket))
	t.Cleanup(srv.Close)
	return srv, &captured, exec
}

// TestDictationConfirmationOverConn: 「好了」「好嘞」「下一个」 — each one
// misjudged by the model (repeat / chat / hint) — advance the classroom one
// word at a time over the real transport and store: indexes arrive in order
// with no gaps or repeats, the last word opens check_started(
// reason=dictation_done) exactly once with the per-word countdown stopped,
// the confirmation itself writes no FSRS, the manual verdicts and the
// unified submit finish the lesson with the summary held for its ack, and
// neither a repeated 「好了」 in the check stage nor a repeated check_submit
// writes anything extra.
func TestDictationConfirmationOverConn(t *testing.T) {
	cards := ttCards("苹果", "香蕉", "橘子")
	words := newFakeWords(cards)
	words.reviewResult = ReviewWriteResult{
		Requested: []string{"苹果", "香蕉", "橘子"},
		Updated:   []string{"苹果", "香蕉", "橘子"},
	}
	// Four voice turns: the three confirmations (each MISJUDGED by the
	// model — the correction must carry them) plus a repeated 「好了」 that
	// must do nothing in the check stage.
	asr := newFakeASR(
		asrScript{text: "好了"},
		asrScript{text: "好嘞"},
		asrScript{text: "下一个"},
		asrScript{text: "好了"},
	)
	llm := newFakeLLM(
		intentScript(intentRepeat, "哎呀老师再读一遍吗"),
		intentScript(intentChat, "你说什么呀"),
		intentScript(intentHint, "上面是草字头哦"),
		intentScript(intentConfirm, ""),
	)
	tts := newFakeTTS(2, 320)
	tts.delay = 20 * time.Millisecond
	srv, manager, exec := dcConfirmStack(t, asr, llm, tts, words, nil)
	conn := itDial(t, srv)

	ready := itInit(t, conn)
	if len(ready.Lesson.Words) != 3 || ready.Lesson.LessonID != dcConfirmLessonID {
		t.Fatalf("session_ready.lesson = %+v, want the 3-word daily row", ready.Lesson)
	}
	itAnnounceAck(t, conn) // the opening greeting

	// The three confirmations: word 1 → 2 → 3, each followed by the next
	// word's announcement (masked position subtitle, real audio frames).
	var versions, indexes []int
	for i := 0; i < 3; i++ {
		itSendUtterance(t, conn)
		upd := itRead(t, conn, 15*time.Second, func(m itMsg) bool { return m.typ == TypeLessonUpdate })
		var lu LessonUpdateMsg
		if err := json.Unmarshal(upd.raw, &lu); err != nil {
			t.Fatalf("lesson_update decode: %v", err)
		}
		versions, indexes = append(versions, lu.Version), append(indexes, lu.CurrentIndex)
		if lu.RequestID != lessonRequestID(dcConfirmLessonID, i) {
			t.Errorf("lesson_update %d request_id = %q, want %q", i, lu.RequestID, lessonRequestID(dcConfirmLessonID, i))
		}
		if i < 2 {
			texts, frames := itAnnounceAckCounted(t, conn)
			if frames == 0 {
				t.Errorf("word %d announcement shipped no audio frames", i+2)
			}
			joined := joinAny(texts)
			if !contains(joined, fmt.Sprintf("第 %d 个词", i+2)) || contains(joined, cards[i+1].Word) {
				t.Errorf("word %d announcement = %q, want the masked position variant", i+2, joined)
			}
		}
	}
	if len(indexes) != 3 || indexes[0] != 1 || indexes[1] != 2 || indexes[2] != 3 {
		t.Errorf("lesson_update indexes = %v, want [1 2 3] in order, no gaps or repeats", indexes)
	}
	if len(versions) != 3 || versions[0] != 2 || versions[1] != 3 || versions[2] != 4 {
		t.Errorf("lesson_update versions = %v, want [2 3 4]", versions)
	}
	if got := len(words.submissions()); got != 0 {
		t.Fatalf("SubmitReview calls before check_submit = %d, want 0 (the confirm itself never writes FSRS)", got)
	}

	// The last confirm opens the check stage exactly once; the per-word
	// countdown is stopped.
	startedRaw := itRead(t, conn, 15*time.Second, func(m itMsg) bool { return m.typ == TypeCheckStarted })
	var started CheckStartedMsg
	if err := json.Unmarshal(startedRaw.raw, &started); err != nil {
		t.Fatalf("check_started decode: %v", err)
	}
	if started.Reason != CheckStartedReasonDictationDone {
		t.Errorf("check_started.reason = %q, want %s", started.Reason, CheckStartedReasonDictationDone)
	}
	if len(started.Words) != 3 || started.Words[0] != "苹果" || started.Words[2] != "橘子" {
		t.Errorf("check_started.words = %v, want the full plaintext list", started.Words)
	}
	itAnnounceAck(t, conn) // the check bridge
	if _, handle := ttTimerState(dcWaitManager(t, manager)); handle != nil {
		t.Error("per-word countdown still armed in the check stage")
	}

	// A repeated 「好了」 in the check stage: classified through the check
	// prompt, never an advance — the row keeps current_index 3, no second
	// check_started, just the degraded spoken line.
	itSendUtterance(t, conn)
	degraded := itAnnounceAck(t, conn)
	if joined := joinAny(degraded); joined == "" {
		t.Error("repeated confirm in the check stage produced no spoken line")
	}
	row, err := exec.GetLessonState(context.Background(), dcConfirmLessonID)
	if err != nil {
		t.Fatalf("row read: %v", err)
	}
	if row.CurrentIndex != 3 {
		t.Errorf("row current_index = %d, want 3 (the repeated confirm advanced nothing)", row.CurrentIndex)
	}

	// Manual verdicts + the unified submit (sent twice back-to-back — the
	// latch may only let one review through), then the ack-gated ending.
	for i := 0; i < 3; i++ {
		itSendText(t, conn, CheckVerdictMsg{
			Type: TypeCheckVerdict, RequestID: fmt.Sprintf("dc-v-%d", i), WordIndex: i, Correct: true,
		})
		itReadCheckVerdicts(t, conn)
	}
	itSendText(t, conn, CheckSubmitMsg{Type: TypeCheckSubmit, RequestID: "dc-submit"})
	itSendText(t, conn, CheckSubmitMsg{Type: TypeCheckSubmit, RequestID: "dc-submit"})

	var (
		resultCount     int
		checkStartedN   int
		lastAckID       string
		lastAckEpoch    uint64
		summaryAudioEnd int
		summaryTexts    []string
	)
	deadline := time.Now().Add(15 * time.Second)
	for summaryAudioEnd < 2 {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			t.Fatal("battle summary never fully streamed")
		}
		_ = conn.SetReadDeadline(time.Now().Add(remaining))
		mt, data, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("summary read: %v", err)
		}
		m := itProbe(mt, data)
		switch m.typ {
		case TypeReviewResult:
			resultCount++
		case TypeCheckStarted:
			checkStartedN++
		case TypeTeacherSentence:
			var ts TeacherSentenceMsg
			_ = json.Unmarshal(data, &ts)
			summaryTexts = append(summaryTexts, ts.Text)
		case TypeAudioEnd:
			var ae AudioEndMsg
			_ = json.Unmarshal(data, &ae)
			lastAckID, lastAckEpoch = ae.SentenceID, ae.Epoch
			summaryAudioEnd++
		case TypeSessionEnded:
			t.Fatal("session_ended arrived before the summary ack (ack gate broken)")
		}
	}
	if resultCount != 1 {
		t.Errorf("review_result count = %d, want exactly 1 (the duplicate submit writes nothing)", resultCount)
	}
	if checkStartedN != 0 {
		t.Errorf("second check_started after the panel opened = %d, want 0", checkStartedN)
	}
	if !contains(joinAny(summaryTexts), "对了 3 个") {
		t.Errorf("summary = %v, want the 3/3 battle wrap-up", summaryTexts)
	}

	ackSent := itHoldThenAck(t, conn, 600*time.Millisecond, lastAckID, lastAckEpoch)
	ended := itReadUntilSessionEnded(t, conn, ackSent)
	if ended.Reason != ReasonLessonComplete {
		t.Errorf("session_ended.reason = %q, want %s", ended.Reason, ReasonLessonComplete)
	}

	// Exactly one full-batch FSRS write across the whole flow (duplicate
	// confirm + duplicate submit included); the store row is the terminal
	// state a reconnect would see.
	subs := words.submissions()
	if len(subs) != 1 {
		t.Fatalf("SubmitReview calls = %d, want exactly 1", len(subs))
	}
	for _, w := range []string{"苹果", "香蕉", "橘子"} {
		if subs[0][w] != "good" {
			t.Errorf("rating for %s = %q, want good", w, subs[0][w])
		}
	}
	row, err = exec.GetLessonState(context.Background(), dcConfirmLessonID)
	if err != nil {
		t.Fatalf("final row read: %v", err)
	}
	if row.Stage != lesson.StageSubmitted || row.CurrentIndex != 3 {
		t.Errorf("final row = stage %q index %d, want submitted at 3", row.Stage, row.CurrentIndex)
	}
}

// dcWaitManager waits for the stack's captured manager (the factory runs
// when the first connection upgrades).
func dcWaitManager(t *testing.T, slot *atomic.Pointer[TurnManager]) *TurnManager {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if tm := slot.Load(); tm != nil {
			return tm
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("manager never constructed")
	return nil
}

// joinAny concatenates string slices (announcement texts).
func joinAny(ss []string) string {
	out := ""
	for _, s := range ss {
		out += s
	}
	return out
}
