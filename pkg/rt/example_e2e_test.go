//go:build e2e

// example_e2e_test.go — Issue #9's classroom acceptance link over the REAL
// /rt WebSocket transport: a help request mid-dictation generates ONE
// example sentence grounded in the real textbook context, practice position
// survives the exchange, and an honest fallback speaks when the generation
// call fails — after which the classroom keeps dictating.
//
// What this proves over the in-process tests (example_test.go): the
// production wiring — pybridge-seeded cards/textbook/progress, the real
// due selection, the ExampleResolver over the real SQLite repos — serves
// the generated sentence to a real WebSocket client, and the very next
// confirm still advances the SAME lesson row (no drift from the help turn).
//
// Only the external voice providers are faked (ASR/LLM/TTS — the ticket's
// fixed-response allowance), assembled exactly like textbook_e2e_test.go.
package rt

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xiaotian/home-tutor-agent/pkg/database"
	"github.com/xiaotian/home-tutor-agent/pkg/infra"
	"github.com/xiaotian/home-tutor-agent/pkg/pybridge"
)

// TestE2E_ClassroomExampleOnDemand is the Issue #9 classroom acceptance:
// the real classroom generates the sentence from the real textbook context
// (edition + learned progress + learned-word sample), keeps the dictation
// position across the help turn, degrades honestly when the generation
// call fails, and continues the practice (AC「随后继续原来的练习」).
func TestE2E_ClassroomExampleOnDemand(t *testing.T) {
	ctx := context.Background()
	b, tools, practiceStore, cardRepo, db, _ := pe2eStack(t)
	textbookRepo, err := database.NewTextbookRepository(db, infra.NewNoOpLogger())
	if err != nil {
		t.Fatalf("NewTextbookRepository: %v", err)
	}

	// Two eligible words with confirmed meanings and NO stored examples —
	// the on-demand shape. Both live in the textbook's first (learned)
	// lesson so the real due fetch selects them.
	entries := []pybridge.AddEntry{
		{Word: "exA1", Meaning: "苹果释义"},
		{Word: "exB1", Meaning: "香蕉释义"},
	}
	add, err := b.RunAdd(ctx, pybridge.AddOpts{Category: "english", Unit: "ex-e2e", Entries: entries})
	if err != nil || add.Added != len(entries) {
		t.Fatalf("RunAdd = %+v, %v; want %d added", add, err, len(entries))
	}
	created, err := b.RunTextbookCreate(ctx, pybridge.TextbookCreateOpts{
		Name: "例句教材", Category: "english", Edition: "测试版",
		Grade: "三年级", Volume: "上册", Lessons: []string{"第1课", "第2课"},
	})
	if err != nil {
		t.Fatalf("create textbook: %v", err)
	}
	if _, err := b.RunAssignWords(ctx, pybridge.AssignWordsOpts{
		TextbookID: created.ID, IDs: []int64{1, 2}, Lesson: "第1课",
	}); err != nil {
		t.Fatalf("assign words: %v", err)
	}
	learned := 1
	if _, err := b.RunTextbookUpdate(ctx, pybridge.TextbookUpdateOpts{
		ID: created.ID, LearnedLessons: &learned,
	}); err != nil {
		t.Fatalf("set learned 1: %v", err)
	}

	pyWords, err := NewPyFSRS(b, infra.NewNoOpLogger())
	if err != nil {
		t.Fatalf("NewPyFSRS: %v", err)
	}
	var connSeq int32
	asr := newFakeASR(asrScript{text: "exA1怎么用"}, asrScript{text: "好了"}, asrScript{text: "exB1怎么用"}, asrScript{text: "好了"})
	fallback := "老师这边还没有这个例句，你先凭印象写吧"
	llm := newFakeLLM(
		intentScript(intentExample, fallback),
		generationScript("这是苹果的意思。比如：I eat an exA1."),
		intentScript(intentConfirm, ""),
		intentScript(intentExample, fallback),
		llmScript{err: fmt.Errorf("generation provider down")}, // the honest-failure turn
		intentScript(intentConfirm, ""),
	)
	h, err := NewHandler(
		ServerConfig{AllowedOrigins: []string{itOrigin}, HeartbeatInterval: 30 * time.Second},
		func() SessionManager {
			n := atomic.AddInt32(&connSeq, 1)
			return NewTurnManager(asr, llm, newFakeTTS(2, 320), tools, TurnManagerConfig{
				LessonID: fmt.Sprintf("e2e-ex-lesson-%d", n),
				Words:    pyWords,
				Practice: cardRepo, PracticeStore: practiceStore,
				Examples: &ExampleResolver{Cards: cardRepo, Textbooks: textbookRepo},
			}, NewEventRecorder(infra.NewNoOpLogger(), "e2e-ex"),
				infra.NewNoOpLogger())
		},
		infra.NewNoOpLogger(),
	)
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}
	srv := httptest.NewServer(http.HandlerFunc(h.HandleWebSocket))
	t.Cleanup(srv.Close)

	conn := itDial(t, srv)
	defer conn.Close()
	ready := itInit(t, conn)
	if len(ready.Lesson.Words) != 2 {
		t.Fatalf("session_ready words = %v, want the 2 seeded words", ready.Lesson.Words)
	}
	itAnnounceAck(t, conn) // greeting + first word

	// --- Help turn 1: the on-demand sentence over the real wire. The
	// student-mode subtitle masks the dictation word; the spoken PCM rides
	// full (D-10) — here the observable is the masked display copy.
	itSendUtterance(t, conn)
	helpTexts := itAnnounceAck(t, conn)
	joined := strings.Join(helpTexts, "")
	if !strings.Contains(joined, "这是苹果的意思。比如：I eat an") || !strings.Contains(joined, "•••") || strings.Contains(joined, "exA1") {
		t.Fatalf("help reply = %q, want the generated sentence with the word masked on screen", joined)
	}

	// The generation request rode the REAL textbook context: edition,
	// learned scale and the learned-word sample (exB1 eligible; the current
	// word itself excluded) — resolved from the pybridge-seeded SQLite.
	gen := llm.calls[1].messages[1].Content
	for _, want := range []string{"已确认释义：苹果释义", "版本 测试版", "三年级", "上册", "已学完第 1 课", "exB1"} {
		if !strings.Contains(gen, want) {
			t.Errorf("generation prompt missing %q:\n%s", want, gen)
		}
	}
	if strings.Contains(gen, "exA1, ") || strings.HasSuffix(gen, "exA1\n") {
		t.Error("generation prompt's learned sample contains the current word")
	}

	// --- The practice CONTINUES from the same position: the next confirm
	// advances word 1 → 2 in the SAME lesson row.
	itSendUtterance(t, conn)
	upd := itRead(t, conn, 15*time.Second, func(m itMsg) bool { return m.typ == TypeLessonUpdate })
	var lu LessonUpdateMsg
	if err := json.Unmarshal(upd.raw, &lu); err != nil {
		t.Fatalf("lesson_update decode: %v", err)
	}
	if lu.CurrentIndex != 1 || lu.Version != 2 {
		t.Fatalf("post-help lesson_update = (idx %d, v %d), want (1, 2) — the help turn must not move the cursor", lu.CurrentIndex, lu.Version)
	}
	itAnnounceAck(t, conn) // word 2 announcement

	// --- Help turn 2: the generation call fails — the honest fallback
	// speaks, no fabricated sentence.
	itSendUtterance(t, conn)
	fallbackTexts := itAnnounceAck(t, conn)
	if got := strings.Join(fallbackTexts, ""); !strings.Contains(got, fallback) {
		t.Fatalf("failed-generation reply = %q, want the honest fallback %q", got, fallback)
	}

	// --- The classroom keeps dictating: the second confirm advances word
	// 2 → 3 and opens the check stage (2-word lesson).
	itSendUtterance(t, conn)
	upd = itRead(t, conn, 15*time.Second, func(m itMsg) bool { return m.typ == TypeLessonUpdate })
	if err := json.Unmarshal(upd.raw, &lu); err != nil {
		t.Fatalf("lesson_update decode: %v", err)
	}
	if lu.CurrentIndex != 2 || lu.Version != 3 {
		t.Fatalf("post-fallback lesson_update = (idx %d, v %d), want (2, 3)", lu.CurrentIndex, lu.Version)
	}
	if llm.callCount() != 6 {
		t.Errorf("llm calls = %d, want 6 (2 classify-help, 2 generation attempts, 2 classify-confirm)", llm.callCount())
	}
}
