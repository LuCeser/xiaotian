package rt

import (
	"encoding/json"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/xiaotian/home-tutor-agent/pkg/lesson"
)

// Observe the text handed to the speech provider, not just the subtitle.
func TestPlaceholderPhraseFirstReadingTTS(t *testing.T) {
	cards := []lesson.Word{{Word: "live near...", Example: "I live near the school."}}
	tts := newFakeTTS(1, 320)
	srv := itServer(t, newFakeASR(), newFakeLLM(), tts, newFakeExecutorNoRow("it-lesson"), newFakeWords(cards))
	conn := itDial(t, srv)
	defer conn.Close()
	itInit(t, conn)
	assertPhraseSubtitles(t, itAnnounceAckSettled(t, conn))
	spoken := strings.Join(tts.recordedTexts(), "")
	if strings.Contains(spoken, "...") {
		t.Fatalf("placeholder reached actual TTS: %q", spoken)
	}
	if !strings.Contains(spoken, "第 1 个词组，live near。例句：I live near the school.请写词组。") {
		t.Fatalf("phrase reading = %q", spoken)
	}
}

func assertPhraseSubtitles(t *testing.T, texts []string) {
	t.Helper()
	for _, text := range texts {
		if regexp.MustCompile(`[A-Za-z]`).MatchString(text) {
			t.Fatalf("subtitle revealed English phrase/example: %q", text)
		}
	}
}

func TestPlaceholderPhraseVariantsTTS(t *testing.T) {
	for _, tc := range []struct {
		word, example, want string
	}{
		{"live near...", "I live near the school.", "第 1 个词组，live near。例句：I live near the school.请写词组。"},
		{"be kind to ...", "Be kind to your friends.", "第 1 个词组，be kind to。例句：Be kind to your friends.请写词组。"},
		{"live near…", "", "第 1 个词组，live near。请写词组。"},
		{"live near……", " \t ", "第 1 个词组，live near。请写词组。"},
		{"live near  . . . ", "I live near...", "第 1 个词组，live near。请写词组。"},
		{"live near . ..", ".", "第 1 个词组，live near。请写词组。"},
		{"live near...", "I live near " + strings.Repeat("a very distant ", 60) + "school.", "第 1 个词组，live near。请写词组。"},
		{"live near\u00a0.\u3000.\t.", "…", "第 1 个词组，live near。请写词组。"},
		{"from ... to ...", "", "第 1 个词组，from，to。请写词组。"},
		{"from … to ……", "", "第 1 个词组，from，to。请写词组。"},
		{"... as ... as ...", "", "第 1 个词组，as，as。请写词组。"},
		{"ask Dr. Smith ...", "", "第 1 个词组，ask Dr.Smith。请写词组。"},
		{"pay 3.50 ...", "", "第 1 个词组，pay 3.50。请写词组。"},
		{"visit the U.S. ...", "", "第 1 个词组，visit the U.S.。请写词组。"},
	} {
		t.Run(tc.word, func(t *testing.T) {
			cards := []lesson.Word{{Word: tc.word, Example: tc.example}}
			tts, llm := newFakeTTS(1, 320), newFakeLLM()
			srv := itServer(t, newFakeASR(), llm, tts, newFakeExecutorNoRow("it-lesson"), newFakeWords(cards))
			conn := itDial(t, srv)
			defer conn.Close()
			itInit(t, conn)
			assertPhraseSubtitles(t, itAnnounceAckSettled(t, conn))
			if got := strings.Join(tts.recordedTexts(), ""); got != tc.want {
				t.Fatalf("actual TTS = %q, want %q", got, tc.want)
			}
			if llm.callCount() != 0 {
				t.Fatal("automatic reading must not generate examples")
			}
		})
	}
}

// Entire classroom over an isolated loopback WebSocket: speech providers and
// vocabulary are test doubles, and no production service or vocabulary is used.
func TestPlaceholderPhraseClassroom(t *testing.T) {
	cards := []lesson.Word{
		{Word: "live near...", Example: "I live near the school."},
		{Word: "be kind to ...", Example: "Be kind to your friends."},
		{Word: "from ... to ..."},
	}
	asr := newFakeASR(asrScript{text: "再读一遍"}, asrScript{text: "慢一点读"}, asrScript{text: "休息一下"}, asrScript{text: "继续"}, asrScript{text: "好了"}, asrScript{text: "好了"}, asrScript{text: "好了"})
	llm := newFakeLLM(intentScript(intentRepeat, ""), intentScript(intentSlow, ""), intentScript(intentPause, ""), intentScript(intentResume, ""), intentScript(intentConfirm, ""), intentScript(intentConfirm, ""), intentScript(intentConfirm, ""))
	tts := newFakeTTS(1, 320)
	exec := newFakeExecutorNoRow("it-lesson")
	srv := itServer(t, asr, llm, tts, exec, newFakeWords(cards))
	conn := itDial(t, srv)
	defer conn.Close()
	ready := itInit(t, conn)
	for _, word := range ready.Lesson.Words {
		if word != "" {
			t.Fatalf("session_ready leaked answer: %+v", ready)
		}
	}
	assertPhraseSubtitles(t, itAnnounceAckSettled(t, conn))
	first := "第 1 个词组，live near。例句：I live near the school.请写词组。"
	for _, action := range []string{"voice repeat", "slow", "pause", "resume", "button"} {
		before := len(tts.recordedTexts())
		if action == "button" {
			itSendText(t, conn, ReadWordMsg{Type: TypeReadWord, WordIndex: 0, CurrentIndex: 0})
		} else {
			itSendUtterance(t, conn)
		}
		if action == "pause" {
			assertPhraseSubtitles(t, itAnnounceAckSettled(t, conn, StatePaused))
		} else {
			assertPhraseSubtitles(t, itAnnounceAckSettled(t, conn))
		}
		if action != "pause" {
			want := first
			if action == "slow" {
				want = "第 1 个词组，live near。live near。例句：I live near the school.请写词组。"
			}
			if got := strings.Join(tts.recordedTexts()[before:], ""); got != want {
				t.Fatalf("%s TTS = %q, want %q", action, got, want)
			}
		}
	}
	for i, want := range []string{
		"第 2 个词组，be kind to。例句：Be kind to your friends.请写词组。",
		"第 3 个词组，from，to。请写词组。",
	} {
		before := len(tts.recordedTexts())
		itSendUtterance(t, conn)
		update := itRead(t, conn, 5*time.Second, func(m itMsg) bool { return m.typ == TypeLessonUpdate })
		var msg LessonUpdateMsg
		if err := json.Unmarshal(update.raw, &msg); err != nil || msg.CurrentIndex != i+1 {
			t.Fatalf("confirm failed to advance: %s (%v)", update.raw, err)
		}
		assertPhraseSubtitles(t, itAnnounceAckSettled(t, conn))
		if got := strings.Join(tts.recordedTexts()[before:], ""); got != want {
			t.Fatalf("next word TTS = %q, want %q", got, want)
		}
	}
	// Reconnection uses the stored cursor and original card, without re-fetch.
	conn.Close()
	conn = itDial(t, srv)
	defer conn.Close()
	before := len(tts.recordedTexts())
	itInit(t, conn)
	assertPhraseSubtitles(t, itAnnounceAckSettled(t, conn))
	if got := strings.Join(tts.recordedTexts()[before:], ""); got != "第 3 个词组，from，to。请写词组。" {
		t.Fatalf("reconnect TTS = %q", got)
	}
	itSendUtterance(t, conn)
	check := itRead(t, conn, 5*time.Second, func(m itMsg) bool { return m.typ == TypeCheckStarted })
	for _, card := range cards {
		if !strings.Contains(string(check.raw), card.Word) {
			t.Fatalf("check changed original target %q: %s", card.Word, check.raw)
		}
	}
	itAnnounceAckSettled(t, conn, StateGrading)
	itSendText(t, conn, EndSessionMsg{Type: TypeEndSession})
	itRead(t, conn, 5*time.Second, func(m itMsg) bool { return m.typ == TypeSessionEnded })
	if llm.callCount() != 7 {
		t.Fatalf("unexpected generation calls: total LLM calls = %d", llm.callCount())
	}
}

func TestPlaceholderPhraseButtonSubtitle(t *testing.T) {
	cards := []lesson.Word{{Word: "live near...", Example: "I live near the school."}}
	tts := newFakeTTS(1, 320)
	srv := itServer(t, newFakeASR(), newFakeLLM(), tts, newFakeExecutorNoRow("it-lesson"), newFakeWords(cards))
	conn := itDial(t, srv)
	defer conn.Close()
	itInit(t, conn)
	itAnnounceAck(t, conn)
	itSendText(t, conn, ReadWordMsg{Type: TypeReadWord, WordIndex: 0, CurrentIndex: 0})
	texts := itAnnounceAckSettled(t, conn)
	if got := strings.Join(texts, ""); strings.Contains(got, "live near") || strings.Contains(got, "school") {
		t.Fatalf("button subtitle revealed phrase/example: %q", got)
	}
}

func TestPlaceholderPhrasePlaybackTimer(t *testing.T) {
	cards := []lesson.Word{{Word: "live near...", Example: "I live near the school."}, {Word: "apple"}}
	tts := newFakeTTS(1, 320)
	tm := NewTurnManager(newFakeASR(), newFakeLLM(), tts, newFakeExecutorNoRow(ttLessonID), ttConfigWith(newFakeWords(cards), func(c *TurnManagerConfig) { c.WordTimeout = 50 * time.Millisecond }), nil, nil)
	t.Cleanup(tm.internalClose)
	s := &captureSender{}
	ttInit(t, tm, s)
	if !waitFor(t, 5*time.Second, func() bool {
		return strings.HasSuffix(strings.Join(tts.recordedTexts(), ""), "请写词组。") && s.countOf(TypeAudioEnd) == s.countOf(TypeAudioStart)
	}) {
		t.Fatal("phrase synthesis did not finish")
	}
	time.Sleep(100 * time.Millisecond)
	if s.countOf(TypeLessonUpdate) != 0 {
		t.Fatal("timer advanced before playback completion")
	}
	ttAckLast(t, tm, s, 0)
	if !waitFor(t, time.Second, func() bool { return s.countOf(TypeLessonUpdate) == 1 }) {
		t.Fatal("timer did not advance after playback completion")
	}
}

func TestPlaceholderPhraseInterruptedReading(t *testing.T) {
	cards := []lesson.Word{{Word: "live near...", Example: "I live near the school."}}
	tts := newFakeTTS(3, 320)
	tts.delay = 50 * time.Millisecond
	tm := NewTurnManager(newFakeASR(), newFakeLLM(), tts, newFakeExecutorNoRow(ttLessonID), ttConfig(newFakeWords(cards)), nil, nil)
	t.Cleanup(tm.internalClose)
	s := &captureSender{}
	ttInit(t, tm, s)
	if !waitFor(t, time.Second, func() bool { return s.countOf(TypeAudioStart) > 0 }) {
		t.Fatal("reading never started")
	}
	tm.HandleControl(s, mustJSON(t, InterruptMsg{Type: TypeInterrupt, Epoch: 0}))
	before := s.countOf(TypeTeacherSentence)
	time.Sleep(100 * time.Millisecond)
	if s.countOf(TypeInterruptAck) != 1 || s.countOf(TypeTeacherSentence) != before || s.countOf(TypeLessonUpdate) != 0 {
		t.Fatal("interrupted reading continued or advanced")
	}
	tm.HandleControl(s, mustJSON(t, EndSessionMsg{Type: TypeEndSession}))
	if s.countOf(TypeSessionEnded) != 1 {
		t.Fatal("interrupted classroom could not end")
	}
}

func TestPhraseReadingLeavesOtherVocabularyUnchanged(t *testing.T) {
	for _, tc := range []struct{ category, word, want string }{
		{lesson.CategoryEnglish, "apple", "你好呀！今天我们只有一个词要写。听好哦：第 1 个词，apple。"},
		{lesson.CategoryChinese, "想象……", "你好呀！今天我们只有一个词要写。听好哦：第 1 个词，想象……。"},
	} {
		t.Run(tc.category, func(t *testing.T) {
			tts := newFakeTTS(1, 320)
			cards := []lesson.Word{{Word: tc.word, Example: "Should not be read."}}
			srv := itServer(t, newFakeASR(), newFakeLLM(), tts, newFakeExecutorNoRow("it-lesson"), newFakeWords(cards))
			conn := itDial(t, srv)
			defer conn.Close()
			itInitCategory(t, conn, tc.category)
			itAnnounceAckSettled(t, conn)
			if got := strings.Join(tts.recordedTexts(), ""); got != tc.want {
				t.Fatalf("ordinary reading = %q, want %q", got, tc.want)
			}
		})
	}
}
