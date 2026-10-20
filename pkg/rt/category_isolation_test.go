// category_isolation_test.go — Issue #55 isolation regressions: with BOTH
// subjects holding vocabulary, one round draws its whole list from ONE
// subject (AC3/AC4), a second subject's session never inherits the first
// one's list or progress (AC5/AC8, over the REAL lesson store so row-key
// isolation — not a fake's courtesy — is what's pinned), a short subject
// starts with its actual count (AC6), and an empty subject ends
// no_due_words without borrowing the other subject's words (AC7). These
// tests pin the #10/#47 deliveries the issue asked to re-verify: the
// mixed-subject phenomenon cannot reproduce while they hold.
package rt

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/xiaotian/home-tutor-agent/pkg/database"
	"github.com/xiaotian/home-tutor-agent/pkg/lesson"
)

// mixedVocabulary builds the Issue #55 scenario's word source: BOTH
// subjects hold due content, scripted per category so a fetch under one
// category can never answer the other subject's words.
func mixedVocabulary(english, chinese []lesson.Word) *fakeWordSource {
	words := newFakeWords()
	words.setDue(lesson.CategoryEnglish, english)
	words.setDue(lesson.CategoryChinese, chinese)
	return words
}

// sessionInitFor builds one session_init payload under the given subject
// (mode=test keeps the full word-table reveal — the isolation assertions
// read the WHOLE round's list from session_ready).
func sessionInitFor(t *testing.T, category string) []byte {
	t.Helper()
	return mustJSON(t, SessionInitMsg{
		Type:     TypeSessionInit,
		Mode:     ModeTest,
		Audio:    AudioSpec{SampleRate: 16000, Format: "s16le", Channels: 1},
		Category: category,
	})
}

// readyMessages decodes every captured session_ready (typed, so the nested
// LessonView rides along).
func readyMessages(t *testing.T, s *captureSender) []SessionReadyMsg {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []SessionReadyMsg
	for _, b := range s.controls {
		var probe struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(b, &probe) != nil || probe.Type != TypeSessionReady {
			continue
		}
		var m SessionReadyMsg
		if err := json.Unmarshal(b, &m); err != nil {
			t.Fatalf("decode session_ready: %v", err)
		}
		out = append(out, m)
	}
	return out
}

// TestMixedVocabularySingleSubjectRound — AC3/AC4: the vocabulary holds
// BOTH subjects' words, yet each subject's round lists only that
// subject's words, fetches exactly once under its own category, and lands
// its own lesson row. One round = one subject, structurally.
func TestMixedVocabularySingleSubjectRound(t *testing.T) {
	englishCards := ttCards("apple", "book", "pencil")
	chineseCards := ttCards("美丽", "世界")
	for _, tc := range []struct {
		name     string
		declared string
		wantRow  string
		wantList []string
	}{
		{"english round lists only english words", lesson.CategoryEnglish, ttLessonID, []string{"apple", "book", "pencil"}},
		{"chinese round lists only chinese words", lesson.CategoryChinese, ttLessonID + "-chinese", []string{"美丽", "世界"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			words := mixedVocabulary(englishCards, chineseCards)
			exec := newFakeExecutorNoRow(ttLessonID)
			tm := NewTurnManager(newFakeASR(), newFakeLLM(), newFakeTTS(1, 320), exec, ttConfig(words), nil, nil)
			s := &captureSender{}
			tm.HandleControl(s, sessionInitFor(t, tc.declared))

			ready := readyMessages(t, s)
			if len(ready) != 1 {
				t.Fatalf("session_ready count = %d, want 1", len(ready))
			}
			if ready[0].Category != tc.declared {
				t.Errorf("session_ready.category = %q, want %q", ready[0].Category, tc.declared)
			}
			got := ready[0].Lesson.Words
			if len(got) != len(tc.wantList) {
				t.Fatalf("round list = %v, want exactly %v (the OTHER subject must never join)", got, tc.wantList)
			}
			for i, w := range tc.wantList {
				if got[i] != w {
					t.Errorf("round list[%d] = %q, want %q", i, got[i], w)
				}
			}
			if ids := exec.ensureIDCalls(); len(ids) != 1 || ids[0] != tc.wantRow {
				t.Errorf("EnsureLessonCards ids = %v, want [%s]", ids, tc.wantRow)
			}
			if cats := words.fetchedCategories(); len(cats) != 1 || cats[0] != tc.declared {
				t.Errorf("FetchDueWords categories = %v, want exactly [%s]", cats, tc.declared)
			}
		})
	}
}

// TestSequentialSubjectSessionsIsolated — AC5/AC8 over the REAL lesson
// store (SQLite): the english round completes, then a chinese round opens
// its OWN row at index 0 with the chinese list — never the english row's
// id, list, or progress — and the english row's completed state survives
// the second round untouched.
func TestSequentialSubjectSessionsIsolated(t *testing.T) {
	db, err := database.Open(filepath.Join(t.TempDir(), "issue55.db"))
	if err != nil {
		t.Fatalf("open temp db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()
	if err := lesson.Migrate(ctx, db); err != nil {
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
	words := mixedVocabulary(ttCards("apple", "book"), ttCards("美丽", "世界"))

	// Round 1: the english classroom opens and runs to completion (the
	// two advances drive the row to current_index = len(cards) directly —
	// the completion state a reconnect would see).
	tm1 := NewTurnManager(newFakeASR(), newFakeLLM(), newFakeTTS(1, 320), tools, ttConfig(words), nil, nil)
	s1 := &captureSender{}
	tm1.HandleControl(s1, sessionInitFor(t, lesson.CategoryEnglish))
	if ready := readyMessages(t, s1); len(ready) != 1 || ready[0].Lesson.LessonID != ttLessonID {
		t.Fatalf("english session_ready = %+v, want the english row %s", ready, ttLessonID)
	}
	for v := 1; v <= 2; v++ {
		if _, err := exec.AdvanceWord(ctx, ttLessonID, v, fmt.Sprintf("seed-advance-%d", v)); err != nil {
			t.Fatalf("seed advance %d: %v", v, err)
		}
	}

	// Round 2: a chinese round opens on its own row.
	tm2 := NewTurnManager(newFakeASR(), newFakeLLM(), newFakeTTS(1, 320), tools, ttConfig(words), nil, nil)
	s2 := &captureSender{}
	tm2.HandleControl(s2, sessionInitFor(t, lesson.CategoryChinese))

	ready := readyMessages(t, s2)
	if len(ready) != 1 {
		t.Fatalf("chinese session_ready count = %d, want 1", len(ready))
	}
	if ready[0].Lesson.LessonID != ttLessonID+"-chinese" {
		t.Errorf("chinese round row = %q, want %q (never the english row)", ready[0].Lesson.LessonID, ttLessonID+"-chinese")
	}
	if got := ready[0].Lesson.Words; len(got) != 2 || got[0] != "美丽" || got[1] != "世界" {
		t.Errorf("chinese round list = %v, want [美丽 世界] (not inherited from round 1)", got)
	}
	if ready[0].Lesson.CurrentIndex != 0 {
		t.Errorf("chinese round current_index = %d, want 0 (progress never carries over)", ready[0].Lesson.CurrentIndex)
	}
	if n := s2.countOf(TypeSessionEnded); n != 0 {
		t.Errorf("chinese round session_ended count = %d, want 0 (a fresh classroom, not the english row's completion)", n)
	}

	// The english row keeps its completed state after the chinese round.
	en, err := exec.GetLessonState(ctx, ttLessonID)
	if err != nil {
		t.Fatalf("english row read: %v", err)
	}
	if en.CurrentIndex != 2 || en.Category != lesson.CategoryEnglish {
		t.Errorf("english row after round 2 = index %d category %q, want index 2 english (untouched)", en.CurrentIndex, en.Category)
	}
	zh, err := exec.GetLessonState(ctx, ttLessonID+"-chinese")
	if err != nil {
		t.Fatalf("chinese row read: %v", err)
	}
	if zh.CurrentIndex != 0 || zh.Category != lesson.CategoryChinese {
		t.Errorf("chinese row = index %d category %q, want index 0 chinese", zh.CurrentIndex, zh.Category)
	}
	if cats := words.fetchedCategories(); len(cats) != 2 || cats[0] != lesson.CategoryEnglish || cats[1] != lesson.CategoryChinese {
		t.Errorf("FetchDueWords categories = %v, want [english chinese] — one scoped fetch per round", cats)
	}
}

// TestShortVocabularyStartsWithActualCount — AC6: the classroom caps at 10
// words, but a subject holding only 3 starts with exactly those 3 — the
// list is never topped up, and the other subject's words (present in the
// vocabulary) never join.
func TestShortVocabularyStartsWithActualCount(t *testing.T) {
	words := mixedVocabulary(ttCards("apple", "book"), ttCards("美丽", "世界", "好看"))
	exec := newFakeExecutorNoRow(ttLessonID)
	tm := NewTurnManager(newFakeASR(), newFakeLLM(), newFakeTTS(1, 320), exec, ttConfig(words), nil, nil)
	s := &captureSender{}
	tm.HandleControl(s, sessionInitFor(t, lesson.CategoryChinese))

	ready := readyMessages(t, s)
	if len(ready) != 1 {
		t.Fatalf("session_ready count = %d, want 1 (3 available words is a real classroom)", len(ready))
	}
	got := ready[0].Lesson.Words
	if len(got) != 3 || got[0] != "美丽" || got[1] != "世界" || got[2] != "好看" {
		t.Errorf("round list = %v, want the 3 actual chinese words (no top-up)", got)
	}
	if cats := words.fetchedCategories(); len(cats) != 1 || cats[0] != lesson.CategoryChinese {
		t.Errorf("FetchDueWords categories = %v, want exactly [chinese] (never topped up from english)", cats)
	}
	if n := s.countOf(TypeSessionEnded); n != 0 {
		t.Errorf("session_ended count = %d, want 0 (the short classroom starts)", n)
	}
}

// TestEmptySubjectNoCrossBorrow — AC7: the declared subject holds nothing
// while the OTHER subject has words — the session still ends
// no_due_words with the empty-list session_ready and the spoken
// explanation, no lesson row is created, and the english vocabulary is
// never fetched (no auto-switch, no borrowed words).
func TestEmptySubjectNoCrossBorrow(t *testing.T) {
	words := mixedVocabulary(ttCards("apple", "book"), nil)
	exec := newFakeExecutorNoRow(ttLessonID)
	tm := NewTurnManager(newFakeASR(), newFakeLLM(), newFakeTTS(1, 320), exec, ttConfig(words), nil, nil)
	s := &captureSender{}
	tm.HandleControl(s, sessionInitFor(t, lesson.CategoryChinese))

	ready := readyMessages(t, s)
	if len(ready) != 1 {
		t.Fatalf("session_ready count = %d, want 1", len(ready))
	}
	if len(ready[0].Lesson.Words) != 0 || ready[0].Category != lesson.CategoryChinese {
		t.Errorf("session_ready = %d words under %q, want 0 words under chinese", len(ready[0].Lesson.Words), ready[0].Category)
	}
	if exec.ensureCardsCalls != 0 {
		t.Errorf("EnsureLessonCards calls = %d, want 0 (no empty task, no row)", exec.ensureCardsCalls)
	}
	if cats := words.fetchedCategories(); len(cats) != 1 || cats[0] != lesson.CategoryChinese {
		t.Errorf("FetchDueWords categories = %v, want exactly [chinese] (english never borrowed)", cats)
	}

	// The spoken explanation and the no_due_words ending both arrive (the
	// ending rides the explanation's playback wait, so poll briefly).
	deadline := time.Now().Add(5 * time.Second)
	for s.countOf(TypeSessionEnded) == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if n := s.countOf(TypeSessionEnded); n != 1 {
		t.Fatalf("session_ended count = %d, want 1 (no_due_words)", n)
	}
	ended := s.controlsOf(t, TypeSessionEnded)[0]
	if got := ended["reason"]; got != ReasonNoDueWords {
		t.Errorf("session_ended reason = %v, want %q", got, ReasonNoDueWords)
	}
	if n := s.countOf(TypeTeacherSentence); n == 0 {
		t.Error("teacher_sentence count = 0, want the spoken no-due explanation (明确的提示)")
	}
}
