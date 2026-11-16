package rt

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/xiaotian/home-tutor-agent/pkg/lesson"
)

func TestOrdinaryPhraseUsesSnapshotExample(t *testing.T) {
	tts, llm := newFakeTTS(1, 320), newFakeLLM()
	cards := []lesson.Word{{Word: "on the ground floor", Example: "The library is on the ground floor."}}
	srv := itServer(t, newFakeASR(), llm, tts, newFakeExecutorNoRow("it-lesson"), newFakeWords(cards))
	conn := itDial(t, srv)
	defer conn.Close()
	itInit(t, conn)
	assertPhraseSubtitles(t, itAnnounceAckSettled(t, conn))
	want := "第 1 个词组，on the ground floor。例句：The library is on the ground floor.请写词组。"
	if got := strings.Join(tts.recordedTexts(), ""); got != want {
		t.Fatalf("TTS = %q, want %q", got, want)
	}
	if llm.callCount() != 0 {
		t.Fatal("automatic reading generated an example")
	}
}

func TestEnglishReadingKindsAcrossClassroomActions(t *testing.T) {
	for _, tc := range []struct{ word, example, normal, slow string }{
		{"on the ground floor", "The library is on the ground floor.", "第 1 个词组，on the ground floor。例句：The library is on the ground floor.请写词组。", "第 1 个词组，on the ground floor。on the ground floor。例句：The library is on the ground floor.请写词组。"},
		{"look after", "I look after my cat.", "第 1 个词组，look after。例句：I look after my cat.请写词组。", "第 1 个词组，look after。look after。例句：I look after my cat.请写词组。"},
		{"do homework", "I do homework after school.", "第 1 个词组，do homework。例句：I do homework after school.请写词组。", "第 1 个词组，do homework。do homework。例句：I do homework after school.请写词组。"},
		{"look after you", "I look after you.", "第 1 个词组，look after you。例句：I look after you.请写词组。", "第 1 个词组，look after you。look after you。例句：I look after you.请写词组。"},
		{"Where is the library?", "Ignored example.", "第 1 句，Where is the library?请写整句。", "第 1 句，Where is the library?Where is the library?请写整句。"},
		{"I like cats", "Ignored example.", "第 1 句，I like cats。请写整句。", "第 1 句，I like cats。I like cats。请写整句。"},
		{"Where is the library", "", "第 1 句，Where is the library。请写整句。", "第 1 句，Where is the library。Where is the library。请写整句。"},
		{"Tom likes cats", "", "第 1 项，Tom likes cats。请按原文写完整内容。", "第 1 项，Tom likes cats。Tom likes cats。请按原文写完整内容。"},
		{"Open the door", "", "第 1 项，Open the door。请按原文写完整内容。", "第 1 项，Open the door。Open the door。请按原文写完整内容。"},
		{"on Monday we play", "", "第 1 句，on Monday we play。请写整句。", "第 1 句，on Monday we play。on Monday we play。请写整句。"},
		{"sb.", "Ignored example.", "好，我再读一遍。sb.。", "sb.。sb.。慢慢写，不着急。"},
		{"sth.", "", "好，我再读一遍。sth.。", "sth.。sth.。慢慢写，不着急。"},
		{"apple", "", "好，我再读一遍。apple。", "apple。apple。慢慢写，不着急。"},
		{"be kind to sb.", "Be kind to Tom.", "第 1 个词组，be kind to sb.例句：Be kind to Tom.请写词组。", "第 1 个词组，be kind to sb.be kind to sb.例句：Be kind to Tom.请写词组。"},
	} {
		t.Run(tc.word, func(t *testing.T) {
			cards := []lesson.Word{{Word: tc.word, Example: tc.example}, {Word: "next"}}
			asr := newFakeASR(asrScript{text: "再读一遍"}, asrScript{text: "慢一点读"}, asrScript{text: "好了"})
			llm := newFakeLLM(intentScript(intentRepeat, ""), intentScript(intentSlow, ""), intentScript(intentConfirm, ""))
			tts := newFakeTTS(1, 320)
			srv := itServer(t, asr, llm, tts, newFakeExecutorNoRow("it-lesson"), newFakeWords(cards))
			conn := itDial(t, srv)
			defer conn.Close()
			itInit(t, conn)
			assertPhraseSubtitles(t, itAnnounceAckSettled(t, conn))
			if strings.HasPrefix(tc.normal, "第") && strings.Join(tts.recordedTexts(), "") != tc.normal {
				t.Fatalf("initial TTS = %q, want %q", strings.Join(tts.recordedTexts(), ""), tc.normal)
			}
			for _, action := range []string{"repeat", "slow", "button"} {
				before := len(tts.recordedTexts())
				if action == "button" {
					itSendText(t, conn, ReadWordMsg{Type: TypeReadWord, WordIndex: 0, CurrentIndex: 0})
				} else {
					itSendUtterance(t, conn)
				}
				assertPhraseSubtitles(t, itAnnounceAckSettled(t, conn))
				want := tc.normal
				if action == "slow" {
					want = tc.slow
				}
				if got := strings.Join(tts.recordedTexts()[before:], ""); got != want {
					t.Fatalf("%s TTS = %q, want %q", action, got, want)
				}
			}
			itSendUtterance(t, conn)
			update := itRead(t, conn, 5*time.Second, func(m itMsg) bool { return m.typ == TypeLessonUpdate })
			var msg LessonUpdateMsg
			if err := json.Unmarshal(update.raw, &msg); err != nil || msg.CurrentIndex != 1 {
				t.Fatalf("advance: %s", update.raw)
			}
			itAnnounceAckSettled(t, conn)
			itSendText(t, conn, EndSessionMsg{Type: TypeEndSession})
			itRead(t, conn, 5*time.Second, func(m itMsg) bool { return m.typ == TypeSessionEnded })
			if llm.callCount() != 3 {
				t.Fatalf("reading generated examples: %d LLM calls", llm.callCount())
			}
		})
	}
}

func TestOrdinaryPhraseAndSentenceContinuousClassroom(t *testing.T) {
	cards := []lesson.Word{{Word: "on the ground floor"}, {Word: "Where is the library?"}, {Word: "look after", Example: "I look after my cat."}}
	asr := newFakeASR(asrScript{text: "好了"}, asrScript{text: "好了"}, asrScript{text: "好了"})
	llm := newFakeLLM(intentScript(intentConfirm, ""), intentScript(intentConfirm, ""), intentScript(intentConfirm, ""))
	tts := newFakeTTS(1, 320)
	srv := itServer(t, asr, llm, tts, newFakeExecutorNoRow("it-lesson"), newFakeWords(cards))
	conn := itDial(t, srv)
	defer conn.Close()
	itInit(t, conn)
	assertPhraseSubtitles(t, itAnnounceAckSettled(t, conn))
	if got := strings.Join(tts.recordedTexts(), ""); got != "第 1 个词组，on the ground floor。请写词组。" {
		t.Fatalf("missing example fallback: %q", got)
	}
	for _, want := range []string{"第 2 句，Where is the library?请写整句。", "第 3 个词组，look after。例句：I look after my cat.请写词组。"} {
		before := len(tts.recordedTexts())
		itSendUtterance(t, conn)
		itRead(t, conn, 5*time.Second, func(m itMsg) bool { return m.typ == TypeLessonUpdate })
		assertPhraseSubtitles(t, itAnnounceAckSettled(t, conn))
		if got := strings.Join(tts.recordedTexts()[before:], ""); got != want {
			t.Fatalf("next reading = %q, want %q", got, want)
		}
	}
	itSendUtterance(t, conn)
	check := itRead(t, conn, 5*time.Second, func(m itMsg) bool { return m.typ == TypeCheckStarted })
	for _, card := range cards {
		if !strings.Contains(string(check.raw), card.Word) {
			t.Fatalf("changed original target: %s", check.raw)
		}
	}
	itAnnounceAckSettled(t, conn, StateGrading)
	itSendText(t, conn, EndSessionMsg{Type: TypeEndSession})
	itRead(t, conn, 5*time.Second, func(m itMsg) bool { return m.typ == TypeSessionEnded })
	if llm.callCount() != 3 {
		t.Fatal("reading triggered generation")
	}
}

func TestOrdinaryReadingCanBeInterruptedAndEnded(t *testing.T) {
	for _, word := range []string{"on the ground floor", "Where is the library?"} {
		t.Run(word, func(t *testing.T) {
			tts := newFakeTTS(3, 320)
			tts.delay = 50 * time.Millisecond
			srv := itServer(t, newFakeASR(), newFakeLLM(), tts, newFakeExecutorNoRow("it-lesson"), newFakeWords([]lesson.Word{{Word: word, Example: "The library is on the ground floor."}}))
			conn := itDial(t, srv)
			defer conn.Close()
			itInit(t, conn)
			itRead(t, conn, 5*time.Second, func(m itMsg) bool { return m.typ == TypeAudioStart })
			itSendText(t, conn, InterruptMsg{Type: TypeInterrupt, Epoch: 0})
			itRead(t, conn, 5*time.Second, func(m itMsg) bool { return m.typ == TypeInterruptAck })
			itSendText(t, conn, EndSessionMsg{Type: TypeEndSession})
			itRead(t, conn, 5*time.Second, func(m itMsg) bool {
				if m.typ == TypeTeacherSentence || m.typ == TypeLessonUpdate || m.typ == "" {
					t.Fatalf("old reading continued after interrupt: %s", m.raw)
				}
				return m.typ == TypeSessionEnded
			})
		})
	}
}
