// category_test.go — Issue #10 学科贯穿回归: the classroom's subject
// ("english"/"chinese") rides EVERY subject-scoped seam from session_init
// to the review write. AC4 (学科信息从内容查询传到实际课堂、检查和记录):
// the due fetch, the lesson-row id/column, the practice pick, the no-due
// read and the unified check submit must all scope to the subject the
// session was opened under — and on every reconnect the ROW's subject is
// the authority, so a post-hoc correction never writes the other subject's
// history. The mixed-page extraction contract itself (同页中英混排, the
// 语文 prompt ignoring English content) is pinned in pkg/ocr
// (TestWordlistChinesePromptContract) — this file pins the classroom side.
package rt

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/xiaotian/home-tutor-agent/pkg/lesson"
)

// TestSessionInitCategoryScopesFreshClassroom — a FRESH classroom (no row
// yet) threads the declared subject through every seam: the due fetch, the
// lesson-row id (chinese qualifies it, english keeps the legacy id
// byte-for-byte), the row's category column and the session_ready echo.
// Absent and off-enum values normalize to english — the deny-by-default
// shape of normalizeSessionMode, so a typo'd subject can never open the
// wrong subject's classroom.
func TestSessionInitCategoryScopesFreshClassroom(t *testing.T) {
	for _, tc := range []struct {
		name       string
		declared   string
		wantFetch  string
		wantRowID  string
		wantEcho   string
		wantRowCat string
	}{
		{"chinese classroom", lesson.CategoryChinese, "chinese", ttLessonID + "-chinese", "chinese", lesson.CategoryChinese},
		{"absent category defaults to english", "", "english", ttLessonID, "english", lesson.CategoryEnglish},
		{"english stays english", lesson.CategoryEnglish, "english", ttLessonID, "english", lesson.CategoryEnglish},
		{"off-enum falls back to english", "france", "english", ttLessonID, "english", lesson.CategoryEnglish},
	} {
		t.Run(tc.name, func(t *testing.T) {
			words := newFakeWords(ttCards("美丽", "世界"))
			exec := newFakeExecutorNoRow(ttLessonID)
			tm := NewTurnManager(newFakeASR(), newFakeLLM(), newFakeTTS(1, 320), exec, ttConfig(words), nil, nil)
			s := &captureSender{}
			tm.HandleControl(s, mustJSON(t, SessionInitMsg{
				Type:     TypeSessionInit,
				Audio:    AudioSpec{SampleRate: 16000, Format: "s16le", Channels: 1},
				Category: tc.declared,
			}))

			ready := s.controlsOf(t, TypeSessionReady)
			if len(ready) != 1 {
				t.Fatalf("session_ready count = %d, want 1", len(ready))
			}
			if got := ready[0]["category"]; got != tc.wantEcho {
				t.Errorf("session_ready.category = %v, want %q", got, tc.wantEcho)
			}
			if ids := exec.ensureIDCalls(); len(ids) != 1 || ids[0] != tc.wantRowID {
				t.Errorf("EnsureLessonCards ids = %v, want [%s]", ids, tc.wantRowID)
			}
			if cats := words.fetchedCategories(); len(cats) != 1 || cats[0] != tc.wantFetch {
				t.Errorf("FetchDueWords categories = %v, want [%s]", cats, tc.wantFetch)
			}
			if got := exec.rowCategory(); got != tc.wantRowCat {
				t.Errorf("row category = %q, want %q", got, tc.wantRowCat)
			}
		})
	}
}

// TestSessionInitCategoryRowAuthority — an EXISTING row is the subject
// authority: a reconnect that omits the category (or re-declares the other
// subject) still echoes the row's subject and never re-fetches due words —
// the row was created under one subject and every later write rides it.
func TestSessionInitCategoryRowAuthority(t *testing.T) {
	for _, tc := range []struct {
		name     string
		declared string
	}{
		{"absent category on a chinese row", ""},
		{"english re-declaration on a chinese row", lesson.CategoryEnglish},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cards := ttCards("美丽", "世界")
			exec := newFakeExecutor(ttLessonID, cards)
			exec.lesson.Category = lesson.CategoryChinese
			words := newFakeWords(ttCards("别的")) // a fetch here would mean the row was not the authority
			tm := NewTurnManager(newFakeASR(), newFakeLLM(), newFakeTTS(1, 320), exec, ttConfig(words), nil, nil)
			s := &captureSender{}
			tm.HandleControl(s, mustJSON(t, SessionInitMsg{
				Type:     TypeSessionInit,
				Audio:    AudioSpec{SampleRate: 16000, Format: "s16le", Channels: 1},
				Category: tc.declared,
			}))

			ready := s.controlsOf(t, TypeSessionReady)
			if len(ready) != 1 {
				t.Fatalf("session_ready count = %d, want 1", len(ready))
			}
			if got := ready[0]["category"]; got != "chinese" {
				t.Errorf("session_ready.category = %v, want chinese (the row's subject)", got)
			}
			if words.fetchCount() != 0 {
				t.Errorf("FetchDueWords calls = %d, want 0 (existing row is never re-fetched)", words.fetchCount())
			}
			if exec.ensureCardsCalls != 0 {
				t.Errorf("EnsureLessonCards calls = %d, want 0 (first stored content wins)", exec.ensureCardsCalls)
			}
		})
	}
}

// TestLessonIDForCategory — the id qualifier: chinese appends the suffix to
// a configured AND a daily id alike (one row per subject per day);
// english/absent/off-enum keep the legacy id byte-for-byte, so every
// existing row, bookmark and resume path is untouched.
func TestLessonIDForCategory(t *testing.T) {
	now := time.Date(2026, 10, 16, 20, 0, 0, 0, time.UTC)
	daily := resolveLessonID("", now)
	if !strings.HasPrefix(daily, "r0-") {
		t.Fatalf("daily id = %q, want the r0- family", daily)
	}
	for _, tc := range []struct{ configured, category, want string }{
		{"L1", lesson.CategoryChinese, "L1-chinese"},
		{"L1", lesson.CategoryEnglish, "L1"},
		{"L1", "", "L1"},
		{"L1", "france", "L1"},
		{"", lesson.CategoryChinese, daily + "-chinese"},
		{"", lesson.CategoryEnglish, daily},
		{"", "", daily},
	} {
		if got := lessonIDForCategory(tc.configured, now, tc.category); got != tc.want {
			t.Errorf("lessonIDForCategory(%q, %q) = %q, want %q", tc.configured, tc.category, got, tc.want)
		}
	}
}

// TestPracticeInitCategoryScoping — the practice pick (再练一组) is
// subject-isolated at the same seam: the vocabulary List carries the
// session's category together with the Issue #4 participation+scope
// switches, and the exclusion read targets THIS subject's daily row
// (r0-TODAY-chinese), never the other subject's words.
func TestPracticeInitCategoryScoping(t *testing.T) {
	practice := &fakePracticeSource{cards: ptPracticeCards("美丽", "世界")}
	words := newFakeWords() // a due fetch here would mean the practice path leaked into FSRS
	exec := newFakeExecutorNoRow(ttLessonID)
	cfg := TurnManagerConfig{LessonID: ttLessonID, Words: words, Practice: practice, PracticeStore: newTestPracticeStore(t)}
	tm := NewTurnManager(newFakeASR(), newFakeLLM(), newFakeTTS(1, 320), exec, cfg, nil, nil)
	s := &captureSender{}
	tm.HandleControl(s, mustJSON(t, SessionInitMsg{
		Type:     TypeSessionInit,
		Audio:    AudioSpec{SampleRate: 16000, Format: "s16le", Channels: 1},
		Practice: true,
		Category: lesson.CategoryChinese,
	}))

	ready := s.controlsOf(t, TypeSessionReady)
	if len(ready) != 1 {
		t.Fatalf("session_ready count = %d, want 1", len(ready))
	}
	if got := ready[0]["category"]; got != "chinese" {
		t.Errorf("session_ready.category = %v, want chinese", got)
	}
	if !practice.allCallsCarryCategory(lesson.CategoryChinese) {
		t.Errorf("practice List filters = %+v, want every call scoped to chinese + participating + scope-eligible", practice.filtersSnapshot())
	}
	// The exclusion set reads the CHINESE daily row (the configured id is
	// qualified the same way as the dictation classroom's).
	if ids := exec.stateIDCalls(); len(ids) == 0 || !strings.HasSuffix(ids[0], "-chinese") {
		t.Errorf("GetLessonState ids = %v, want the chinese daily id first", ids)
	}
	if words.fetchCount() != 0 {
		t.Errorf("FetchDueWords calls = %d, want 0 (practice never touches FSRS)", words.fetchCount())
	}
}

// TestDictationChineseClassroomEndToEnd — the Issue #10 acceptance spine
// over the real /rt transport: a chinese session_init creates the
// -chinese row from a chinese-scoped due fetch, echoes the subject, runs
// the same three-stage classroom, and the unified check submit writes the
// review batch under the chinese category — the exact write that would
// otherwise land on the english column (the AC4 defect).
func TestDictationChineseClassroomEndToEnd(t *testing.T) {
	cards := ttCards("美丽", "世界")
	words := newFakeWords(cards)
	words.reviewResult = ReviewWriteResult{
		Requested: []string{"美丽", "世界"},
		Updated:   []string{"美丽", "世界"},
	}
	exec := newFakeExecutorNoRow("it-lesson")
	asr := newFakeASR(asrScript{text: "好了"}, asrScript{text: "好了"})
	llm := newFakeLLM(intentScript(intentConfirm, ""), intentScript(intentConfirm, ""))
	tts := newFakeTTS(2, 320)
	tts.delay = 20 * time.Millisecond
	srv := itServer(t, asr, llm, tts, exec, words)
	conn := itDial(t, srv)

	ready := itInitCategory(t, conn, lesson.CategoryChinese)
	if ready.Category != lesson.CategoryChinese {
		t.Fatalf("session_ready.category = %q, want chinese", ready.Category)
	}
	if ids := exec.ensureIDCalls(); len(ids) != 1 || ids[0] != "it-lesson-chinese" {
		t.Fatalf("EnsureLessonCards ids = %v, want [it-lesson-chinese]", ids)
	}
	if cats := words.fetchedCategories(); len(cats) != 1 || cats[0] != lesson.CategoryChinese {
		t.Fatalf("FetchDueWords categories = %v, want [chinese]", cats)
	}

	itAnnounceAck(t, conn) // greeting
	for i := 0; i < 2; i++ {
		itSendUtterance(t, conn)
		upd := itRead(t, conn, 15*time.Second, func(m itMsg) bool { return m.typ == TypeLessonUpdate })
		var lu LessonUpdateMsg
		if err := json.Unmarshal(upd.raw, &lu); err != nil {
			t.Fatalf("lesson_update decode: %v", err)
		}
		if lu.RequestID != lessonRequestID("it-lesson-chinese", i) {
			t.Errorf("lesson_update %d request_id = %q, want the chinese row's key", i, lu.RequestID)
		}
		if i < 1 {
			itAnnounceAck(t, conn) // next-word announcement
		}
	}

	startedRaw := itRead(t, conn, 15*time.Second, func(m itMsg) bool { return m.typ == TypeCheckStarted })
	var started CheckStartedMsg
	if err := json.Unmarshal(startedRaw.raw, &started); err != nil {
		t.Fatalf("check_started decode: %v", err)
	}
	if len(started.Words) != 2 || started.Words[0] != "美丽" || started.Words[1] != "世界" {
		t.Fatalf("check_started.words = %v, want the full 词语 list", started.Words)
	}

	itAnnounceAckSettled(t, conn, StateGrading) // check bridge settles to grading (#96 U2)
	for i := 0; i < 2; i++ {
		itSendText(t, conn, CheckVerdictMsg{
			Type: TypeCheckVerdict, RequestID: fmt.Sprintf("it-zh-v-%d", i), WordIndex: i, Correct: true,
		})
		itRead(t, conn, 10*time.Second, func(m itMsg) bool { return m.typ == TypeCheckVerdicts })
	}

	itSendText(t, conn, CheckSubmitMsg{Type: TypeCheckSubmit, RequestID: "it-zh-submit"})
	rr := itRead(t, conn, 15*time.Second, func(m itMsg) bool { return m.typ == TypeReviewResult })
	var result ReviewResultMsg
	if err := json.Unmarshal(rr.raw, &result); err != nil {
		t.Fatalf("review_result decode: %v", err)
	}
	if result.Requested != 2 || result.Updated != 2 || result.Missing != 0 {
		t.Errorf("review_result = %+v, want requested 2 / updated 2 / missing 0", result)
	}

	// The battle summary (1 sentence for a 2/2 batch), then the held ack,
	// then session_ended(lesson_complete).
	var (
		lastAckID    string
		lastAckEpoch uint64
		audioEnds    int
	)
	deadline := time.Now().Add(15 * time.Second)
	for audioEnds < 1 {
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
		case TypeAudioEnd:
			var ae AudioEndMsg
			_ = json.Unmarshal(data, &ae)
			lastAckID, lastAckEpoch = ae.SentenceID, ae.Epoch
			audioEnds++
		case TypeSessionEnded:
			t.Fatal("session_ended arrived before the summary ack (ack gate broken)")
		}
	}
	ackSent := itHoldThenAck(t, conn, 600*time.Millisecond, lastAckID, lastAckEpoch)
	ended := itReadUntilSessionEnded(t, conn, ackSent)
	if ended.Reason != ReasonLessonComplete {
		t.Errorf("reason = %q, want %s", ended.Reason, ReasonLessonComplete)
	}

	// The AC4 write: one review batch, submitted under the chinese
	// category, ratings good for the checked-correct no-hint 词语.
	subs := words.submissions()
	if len(subs) != 1 {
		t.Fatalf("SubmitReview calls = %d, want exactly 1", len(subs))
	}
	for _, w := range []string{"美丽", "世界"} {
		if subs[0][w] != "good" {
			t.Errorf("submitted rating for %s = %q, want good", w, subs[0][w])
		}
	}
	if cats := words.submittedCategories(); len(cats) != 1 || cats[0] != lesson.CategoryChinese {
		t.Errorf("SubmitReview categories = %v, want [chinese] (the write lands on the 语文 column)", cats)
	}
}
