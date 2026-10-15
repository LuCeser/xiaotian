//go:build e2e

// textbook_e2e_test.go — Issue #4's classroom acceptance link over the REAL
// /rt WebSocket transport: the textbook scope must shape BOTH selection
// entries (due lesson + practice) with no caching seam to hide behind.
//
// What this proves over the pybridge-level E2E
// (pkg/pybridge/textbook_e2e_test.go): the production rt wiring —
// session_init → FetchDueWords (real Python) or the practice List
// (real Go repo, ScopeEligibleOnly) — serves the scoped selection to a real
// WebSocket client, and a SECOND classroom connection opened after a
// progress write IMMEDIATELY honors the new progress (the factory hands
// every connection a FRESH lesson id, because lesson rows are
// first-content-wins — the fixed-id trap the PLAN's test split names).
//
// Only the external voice providers are faked (ASR/LLM/TTS — the ticket's
// fixed-response allowance); word selection, both DBs and Python are the
// production stack, assembled exactly like participation_e2e_test.go.
package rt

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xiaotian/home-tutor-agent/pkg/infra"
	"github.com/xiaotian/home-tutor-agent/pkg/pybridge"
)

// tbReadyWords dials one real /rt connection, sends session_init (practice
// per the flag), and resolves with the SELECTED words from session_ready —
// the classroom-selection observable, delivered before any turn traffic.
func tbReadyWords(t *testing.T, srv *httptest.Server, practice bool) []string {
	t.Helper()
	conn := itDial(t, srv)
	defer conn.Close()
	itSendText(t, conn, SessionInitMsg{
		Type: TypeSessionInit, Mode: ModeTest, Practice: practice,
		Audio: AudioSpec{SampleRate: 16000, Format: "s16le", Channels: 1},
	})
	raw := itRead(t, conn, 30*time.Second, func(m itMsg) bool { return m.typ == TypeSessionReady })
	var ready SessionReadyMsg
	if err := json.Unmarshal(raw.raw, &ready); err != nil {
		t.Fatalf("session_ready decode: %v", err)
	}
	words := append([]string(nil), ready.Lesson.Words...)
	sort.Strings(words)
	return words
}

// tbExpectWords fails unless got equals want (both sorted).
func tbExpectWords(t *testing.T, got, want []string) {
	t.Helper()
	sort.Strings(want)
	if len(got) != len(want) || fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("selected words = %v, want exactly %v", got, want)
	}
}

// TestE2E_TextbookScopeShapesClassroomSelection is the Issue #4 classroom
// acceptance: the real due lesson and the real practice entry both exclude
// unlearned-lesson and 待确认 words, practice cannot bypass progress
// (AC「不能借加练绕过」), and a progress write between two connections is
// honored by the very next classroom — same process, no cache.
func TestE2E_TextbookScopeShapesClassroomSelection(t *testing.T) {
	ctx := context.Background()
	b, tools, practiceStore, cardRepo, _, _ := pe2eStack(t)

	// 6 words, insertion order ⇒ ids 1..6: two in the list's FIRST lesson
	// (第3课 — dictionary order would rank 第1课 first, so the selection
	// proves explicit order), one in an unlearned lesson, one with the
	// parent's include override, one 待确认, one supplementary.
	entries := []pybridge.AddEntry{
		{Word: "tbC1", Meaning: "已学课"},
		{Word: "tbC2", Meaning: "已学课"},
		{Word: "tbA1", Meaning: "未学课"},
		{Word: "tbInc1", Meaning: "加入范围"},
		{Word: "tbPend1", Meaning: "待确认"},
		{Word: "tbS1", Meaning: "补充词"},
	}
	add, err := b.RunAdd(ctx, pybridge.AddOpts{Category: "english", Unit: "tb-scope-e2e", Entries: entries})
	if err != nil || add.Added != len(entries) {
		t.Fatalf("RunAdd = %+v, %v; want %d added", add, err, len(entries))
	}
	created, err := b.RunTextbookCreate(ctx, pybridge.TextbookCreateOpts{
		Name: "课堂教材", Category: "english", Lessons: []string{"第3课", "第1课", "第2课"},
	})
	if err != nil {
		t.Fatalf("create textbook: %v", err)
	}
	assign := func(ids []int64, lesson string, include bool) {
		t.Helper()
		res, err := b.RunAssignWords(ctx, pybridge.AssignWordsOpts{
			TextbookID: created.ID, IDs: ids, Lesson: lesson, IncludeInScope: include,
		})
		if err != nil || res.Updated != len(ids) {
			t.Fatalf("assign %v → %q = %+v, %v", ids, lesson, res, err)
		}
	}
	assign([]int64{1, 2}, "第3课", false)
	assign([]int64{3}, "第1课", false)
	assign([]int64{4}, "第2课", true)
	assign([]int64{5}, "", false)
	learned := 1
	if _, err := b.RunTextbookUpdate(ctx, pybridge.TextbookUpdateOpts{
		ID: created.ID, LearnedLessons: &learned,
	}); err != nil {
		t.Fatalf("set learned 1: %v", err)
	}

	// The production wiring with a PER-CONNECTION lesson id: lesson rows
	// are first-content-wins, so a fixed id would replay connection 1's
	// frozen words instead of re-selecting. The counter gives every
	// classroom a fresh row — the "second classroom" of the acceptance.
	pyWords, err := NewPyFSRS(b, infra.NewNoOpLogger())
	if err != nil {
		t.Fatalf("NewPyFSRS: %v", err)
	}
	var connSeq int32
	asr := newFakeASR(asrScript{text: "好了"})
	llm := newFakeLLM(intentScript(intentConfirm, ""))
	tts := newFakeTTS(2, 320)
	h, err := NewHandler(
		ServerConfig{AllowedOrigins: []string{itOrigin}, HeartbeatInterval: 30 * time.Second},
		func() SessionManager {
			n := atomic.AddInt32(&connSeq, 1)
			return NewTurnManager(asr, llm, tts, tools, TurnManagerConfig{
				LessonID: fmt.Sprintf("e2e-tb-lesson-%d", n),
				Words:    pyWords,
				Practice: cardRepo, PracticeStore: practiceStore,
			}, NewEventRecorder(infra.NewNoOpLogger(), "e2e-tb"),
				infra.NewNoOpLogger())
		},
		infra.NewNoOpLogger(),
	)
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}
	srv := httptest.NewServer(http.HandlerFunc(h.HandleWebSocket))
	t.Cleanup(srv.Close)

	// Classroom 1 (due): 第3课 is the LIST's first lesson, so progress 1
	// unlocks tbC1/tbC2; the include override rides along; the supplementary
	// word passes. The unlearned lesson and 待确认 stay out.
	got := tbReadyWords(t, srv, false)
	tbExpectWords(t, got, []string{"tbC1", "tbC2", "tbInc1", "tbS1"})

	// Classroom 2 (practice, same progress): 加练 must hit the SAME scope —
	// tbA1 (unlearned lesson) and tbPend1 cannot be snuck in via practice.
	got = tbReadyWords(t, srv, true)
	tbExpectWords(t, got, []string{"tbC1", "tbC2", "tbInc1", "tbS1"})

	// The parent advances progress to 3 (ALL lessons learned) through a
	// real Python write — then the very next classroom connection, in the
	// SAME server process, selects tbA1: the word classroom 1 REFUSED is
	// now immediately eligible, no cache to invalidate. The four words
	// classroom 1 already fetched stay fetch-locked (same-day no-re-dictation,
	// the 13-10 lock) — so the due fetch returns exactly the newly unlocked
	// word, never an out-of-scope one.
	learned = 3
	if _, err := b.RunTextbookUpdate(ctx, pybridge.TextbookUpdateOpts{
		ID: created.ID, LearnedLessons: &learned,
	}); err != nil {
		t.Fatalf("set learned 3: %v", err)
	}
	got = tbReadyWords(t, srv, false)
	tbExpectWords(t, got, []string{"tbA1"})

	// Practice rides the raw repo List (no fetch lock — D-11), so at full
	// progress its pool is every scope-eligible word: 待确认 stays out even
	// now — only a real lesson (or the explicit include flag) admits a
	// word, never progress alone. The sleep crosses a second boundary:
	// practice lesson ids are r0x-<HHMMSS>, so two practice inits inside
	// one second would collide on the SAME id and replay the first's
	// frozen row (first-content-wins) — the PLAN's timestamp-freshness
	// caveat, honored explicitly here.
	time.Sleep(1100 * time.Millisecond)
	got = tbReadyWords(t, srv, true)
	tbExpectWords(t, got, []string{"tbA1", "tbC1", "tbC2", "tbInc1", "tbS1"})
}
