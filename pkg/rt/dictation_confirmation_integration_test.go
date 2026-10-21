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
	"strings"
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

// TestDictationConfirmationRestart: after a confirmation-driven lesson ends
// the honest way, the existing entry points open the next rounds correctly:
// re-initializing the SAME daily classroom short-circuits on its submitted
// row (never an implicit reset), a same-subject practice round opens its own
// r0x- row with its own list, and a different subject opens its own row —
// neither inherits the finished classroom's target, history, timer, or
// cancellation state, and the submitted row is never rewritten.
func TestDictationConfirmationRestart(t *testing.T) {
	words := mixedVocabulary(ttCards("苹果", "香蕉"), ttCards("美丽", "世界"))
	words.reviewResult = ReviewWriteResult{
		Requested: []string{"苹果", "香蕉"},
		Updated:   []string{"苹果", "香蕉"},
	}
	practiceSrc := &fakePracticeSource{cards: []database.Card{
		{ID: 1, Word: "西瓜", Meaning: "melon", Example: "w"},
		{ID: 2, Word: "葡萄", Meaning: "grape", Example: "g"},
	}}
	asr := newFakeASR(asrScript{text: "好了"}, asrScript{text: "下一个"})
	llm := newFakeLLM(intentScript(intentChat, "嗯嗯"), intentScript(intentRepeat, "再读？"))
	tts := newFakeTTS(2, 320)
	tts.delay = 20 * time.Millisecond
	srv, manager, exec := dcConfirmStack(t, asr, llm, tts, words, func(cfg *TurnManagerConfig) {
		cfg.Practice = practiceSrc
		cfg.PracticeStore = newTestPracticeStore(t)
	})

	// Round 1: the confirmation-driven lesson runs to its submitted end.
	conn1 := itDial(t, srv)
	ready1 := itInit(t, conn1)
	if len(ready1.Lesson.Words) != 2 || ready1.Lesson.LessonID != dcConfirmLessonID {
		t.Fatalf("round 1 lesson = %+v, want the 2-word daily row", ready1.Lesson)
	}
	itAnnounceAck(t, conn1)
	for i := 0; i < 2; i++ {
		itSendUtterance(t, conn1)
		itRead(t, conn1, 15*time.Second, func(m itMsg) bool { return m.typ == TypeLessonUpdate })
		if i == 0 {
			itAnnounceAck(t, conn1)
		}
	}
	itReadCheckStarted(t, conn1)
	itAnnounceAck(t, conn1) // the check bridge
	for i := 0; i < 2; i++ {
		itSendText(t, conn1, CheckVerdictMsg{
			Type: TypeCheckVerdict, RequestID: fmt.Sprintf("dc-r-v-%d", i), WordIndex: i, Correct: true,
		})
		itReadCheckVerdicts(t, conn1)
	}
	itSendText(t, conn1, CheckSubmitMsg{Type: TypeCheckSubmit, RequestID: "dc-r-submit"})
	if ended := itAckEnding(t, conn1, ReasonLessonComplete); ended.Reason != ReasonLessonComplete {
		t.Fatalf("round 1 ended %q, want lesson_complete", ended.Reason)
	}
	if got := len(words.submissions()); got != 1 {
		t.Fatalf("round 1 SubmitReview calls = %d, want 1", got)
	}

	// Round 2: re-initializing the SAME daily classroom — the submitted row
	// short-circuits (ready + lesson_complete end), never an implicit reset:
	// no re-fetch, no new review, and the row keeps its terminal state.
	fetchesBefore := words.fetchCount()
	conn2 := itDial(t, srv)
	ready2 := itInit(t, conn2)
	if ready2.Lesson.LessonID != dcConfirmLessonID {
		t.Fatalf("round 2 lesson id = %q, want the same daily row", ready2.Lesson.LessonID)
	}
	ended2 := itRead(t, conn2, 10*time.Second, func(m itMsg) bool { return m.typ == TypeSessionEnded })
	var endMsg2 SessionEndedMsg
	if err := json.Unmarshal(ended2.raw, &endMsg2); err != nil {
		t.Fatalf("round 2 session_ended decode: %v", err)
	}
	if endMsg2.Reason != ReasonLessonComplete {
		t.Errorf("round 2 reason = %q, want %s (the completed row short-circuit)", endMsg2.Reason, ReasonLessonComplete)
	}
	if got := words.fetchCount(); got != fetchesBefore {
		t.Errorf("FetchDueWords after re-init = %d, want %d (a submitted row never re-fetches)", got, fetchesBefore)
	}
	if got := len(words.submissions()); got != 1 {
		t.Errorf("SubmitReview after re-init = %d, want still 1", got)
	}

	// Round 3: a same-subject practice round — its own r0x- row, its own
	// list, its own progress; the fresh manager carries nothing over.
	conn3 := itDial(t, srv)
	ready3 := itInitPractice(t, conn3)
	if !strings.HasPrefix(ready3.Lesson.LessonID, "r0x-") || ready3.Lesson.LessonID == dcConfirmLessonID {
		t.Fatalf("practice lesson id = %q, want the r0x- namespace", ready3.Lesson.LessonID)
	}
	// Student mode masks the practice list on the wire (D-08/D-10); the
	// length plus the exclusion of the dictated words pins the pool pick.
	if len(ready3.Lesson.Words) != 2 {
		t.Fatalf("practice words = %v, want the 2-word pool", ready3.Lesson.Words)
	}
	for _, w := range ready3.Lesson.Words {
		if w == "苹果" || w == "香蕉" {
			t.Errorf("practice picked today's dictated word %q (避开当日已默 broken)", w)
		}
	}
	if !practiceSrc.allCallsCarryCategory(lesson.CategoryEnglish) {
		t.Errorf("practice List filters = %+v, want english-scoped picks", practiceSrc.filtersSnapshot())
	}
	dcAssertFreshClassroom(t, dcWaitManager(t, manager))
	_ = conn3 // the practice greeting stays un-acked; the round is done asserting

	// Round 4: the OTHER subject opens its own row and list; the english
	// row's submitted state survives untouched. (mode=test reveals the list
	// on the wire — same debug affordance the isolation tests assert with.)
	conn4 := itDial(t, srv)
	itSendText(t, conn4, SessionInitMsg{
		Type:     TypeSessionInit,
		Mode:     ModeTest,
		Audio:    AudioSpec{SampleRate: 16000, Format: "s16le", Channels: 1},
		Category: lesson.CategoryChinese,
	})
	ready4Raw := itRead(t, conn4, 10*time.Second, func(m itMsg) bool { return m.typ == TypeSessionReady })
	var ready4 SessionReadyMsg
	if err := json.Unmarshal(ready4Raw.raw, &ready4); err != nil {
		t.Fatalf("round 4 session_ready decode: %v", err)
	}
	if ready4.Lesson.LessonID != dcConfirmLessonID+"-chinese" {
		t.Fatalf("chinese lesson id = %q, want the -chinese row", ready4.Lesson.LessonID)
	}
	if len(ready4.Lesson.Words) != 2 || ready4.Lesson.Words[0] != "美丽" || ready4.Lesson.Words[1] != "世界" {
		t.Fatalf("chinese words = %v, want the chinese list (never inherited)", ready4.Lesson.Words)
	}
	if ready4.Lesson.CurrentIndex != 0 {
		t.Errorf("chinese current_index = %d, want 0", ready4.Lesson.CurrentIndex)
	}
	dcAssertFreshClassroom(t, dcWaitManager(t, manager))

	ctx := context.Background()
	en, err := exec.GetLessonState(ctx, dcConfirmLessonID)
	if err != nil {
		t.Fatalf("english row read: %v", err)
	}
	if en.Stage != lesson.StageSubmitted || en.CurrentIndex != 2 || en.Category != lesson.CategoryEnglish {
		t.Errorf("english row after all rounds = %q idx %d cat %q, want submitted/2/english untouched",
			en.Stage, en.CurrentIndex, en.Category)
	}
	zh, err := exec.GetLessonState(ctx, dcConfirmLessonID+"-chinese")
	if err != nil {
		t.Fatalf("chinese row read: %v", err)
	}
	if zh.CurrentIndex != 0 || zh.Category != lesson.CategoryChinese {
		t.Errorf("chinese row = idx %d cat %q, want 0/chinese", zh.CurrentIndex, zh.Category)
	}
	if cats := words.fetchedCategories(); len(cats) != 2 || cats[0] != lesson.CategoryEnglish || cats[1] != lesson.CategoryChinese {
		t.Errorf("FetchDueWords categories = %v, want exactly [english chinese]", cats)
	}
}

// dcAssertFreshClassroom pins the not-inherited invariants of a just-opened
// classroom: cursor at word 1, no recorded outcomes, no parked subject ask,
// no live per-word countdown (nothing was acked yet).
func dcAssertFreshClassroom(t *testing.T, tm *TurnManager) {
	t.Helper()
	tm.mu.Lock()
	defer tm.mu.Unlock()
	if tm.tutor == nil {
		t.Fatal("fresh classroom has no tutor")
	}
	if tm.tutor.currentIndex != 0 || len(tm.tutor.perWordOutcome) != 0 || tm.tutor.completed {
		t.Errorf("fresh classroom state = idx %d outcomes %v completed %v, want word 1 / none / false",
			tm.tutor.currentIndex, tm.tutor.perWordOutcome, tm.tutor.completed)
	}
	if tm.pendingAsk != nil {
		t.Error("fresh classroom inherited a parked subject ask")
	}
	if tm.wordTimer != nil {
		t.Error("fresh classroom inherited a live per-word countdown")
	}
}
