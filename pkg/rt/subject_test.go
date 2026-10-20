// subject_test.go — Issue #55 subject-flow regressions (reworked): the
// unspecified-subject ("auto") classroom is settled by the CHILD, never a
// model — the one subject holding due words opens directly, both subjects
// holding due words asks the child (subject_ask question, answered by
// voice or the screen buttons), and the answered subject then binds the
// round through every #10 seam (scoped fetch, per-subject row,
// session_ready echo). AC2: an explicit declaration never reaches the
// clarify — the user's word outranks any ask, with zero asks and zero
// content peeks.
package rt

import (
	"testing"

	"github.com/xiaotian/home-tutor-agent/pkg/lesson"
)

// TestSessionInitExplicitCategorySkipsClarify — the AC2 spine: a declared
// english/chinese NEVER reaches the clarify — zero asks, zero peeks — and
// the declared subject binds the round even when the due content would
// point the other way (no ask ever gets the chance to disagree with the
// user, so the user's word can never be silently overruled).
func TestSessionInitExplicitCategorySkipsClarify(t *testing.T) {
	for _, tc := range []struct {
		name      string
		declared  string
		peekCat   string // the subject whose content WOULD point the other way
		wantRowID string
	}{
		{"declared english over chinese-only content", lesson.CategoryEnglish, lesson.CategoryChinese, ttLessonID},
		{"declared chinese over english-only content", lesson.CategoryChinese, lesson.CategoryEnglish, ttLessonID + "-chinese"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			words := newFakeWords(ttCards("美丽", "世界"))
			words.setPeek(tc.peekCat, ttCards("美丽", "世界"))
			exec := newFakeExecutorNoRow(ttLessonID)
			// No scripted replies: ANY Chat call errors ("script exhausted"),
			// so a spurious model call would fail loudly — the zero-call
			// assertion below is the precise pin.
			llm := newFakeLLM()
			tm := NewTurnManager(newFakeASR(), llm, newFakeTTS(1, 320), exec, ttConfig(words), nil, nil)
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
			if got := ready[0]["category"]; got != tc.declared {
				t.Errorf("session_ready.category = %v, want the declared %q (never re-asked)", got, tc.declared)
			}
			if asks := s.controlsOf(t, TypeSubjectAsk); len(asks) != 0 {
				t.Errorf("subject_ask count = %d, want 0 (a declaration is final)", len(asks))
			}
			if llm.callCount() != 0 {
				t.Errorf("model Chat calls = %d, want 0 (the subject flow never asks a model)", llm.callCount())
			}
			if peeks := words.peekedCategories(); len(peeks) != 0 {
				t.Errorf("PeekDueWords categories = %v, want none (no clarify, no content read)", peeks)
			}
			if cats := words.fetchedCategories(); len(cats) != 1 || cats[0] != tc.declared {
				t.Errorf("FetchDueWords categories = %v, want [%s]", cats, tc.declared)
			}
			if ids := exec.ensureIDCalls(); len(ids) != 1 || ids[0] != tc.wantRowID {
				t.Errorf("EnsureLessonCards ids = %v, want [%s]", ids, tc.wantRowID)
			}
		})
	}
}
