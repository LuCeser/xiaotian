package rt

import (
	"testing"

	"github.com/xiaotian/home-tutor-agent/pkg/lesson"
)

func TestSubjectAskLifetime(t *testing.T) {
	words := newFakeWords()
	words.setPeek(lesson.CategoryEnglish, ttCards("apple"))
	words.setPeek(lesson.CategoryChinese, ttCards("苹果"))
	captured := &captureSender{}
	tm := NewTurnManager(newFakeASR(), newFakeLLM(), newFakeTTS(1, 320),
		newFakeExecutorNoRow(ttLessonID), ttConfig(words), nil, nil)
	wrapped := ttCloseAtInit(t, tm, captured, TypeSubjectAsk)

	tm.HandleControl(wrapped, mustJSON(t, SessionInitMsg{
		Type: TypeSessionInit, Category: CategoryAuto,
		Audio: AudioSpec{SampleRate: 16000, Format: "s16le", Channels: 1},
	}))
	ttAwaitGate(t, wrapped.closeDone, "close at subject_ask did not finish")

	if got := captured.countOf(TypeSubjectAsk); got != 1 {
		t.Fatalf("subject_ask count = %d, want 1", got)
	}
	ttReadyBeforeEnd(t, captured, TypeSubjectAsk)
	ttAssertNothingAfterEnd(t, captured)
}
