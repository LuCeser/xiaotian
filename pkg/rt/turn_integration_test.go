// turn_integration_test.go — in-process whole-chain integration (13-08
// Task 3): a real httptest /rt server + gorilla dialer + fakes, driving the
// dictation flow — session_init → greeting → scripted-ASR confirm →
// lesson_update → next-word announcement → completion → session_ended —
// over the actual websocket transport (13-04 harness, review Suggestion 8).
// Assembly-class defects (sender concurrency, ack gating, natural-end
// sequencing) regress here instead of relying on unit fixtures.
package rt

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/xiaotian/home-tutor-agent/pkg/infra"
)

// itOrigin is the whitelisted Origin these tests dial with.
const itOrigin = "http://origin.test"

// itServer mounts one /rt handler backed by the given fakes, executor, and
// word source.
func itServer(t *testing.T, asr ASRProvider, llm LLMProvider, tts TTSSynthesizer, exec LessonExecutor, words WordSource) *httptest.Server {
	t.Helper()
	h, err := NewHandler(
		ServerConfig{
			AllowedOrigins: []string{itOrigin},
			// Default 30s cadence: the harness sleeps through the 13-09
			// gating lead-ins without reading, and an aggressive test
			// heartbeat would kill the connection for missing pongs.
			HeartbeatInterval: 30 * time.Second,
		},
		func() SessionManager {
			return NewTurnManager(asr, llm, tts, exec,
				TurnManagerConfig{
					LessonID: "it-lesson",
					Words:    words,
				},
				NewEventRecorder(infra.NewNoOpLogger(), "it"),
				infra.NewNoOpLogger())
		},
		infra.NewNoOpLogger(),
	)
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}
	srv := httptest.NewServer(http.HandlerFunc(h.HandleWebSocket))
	t.Cleanup(srv.Close)
	return srv
}

// itDial dials the /rt endpoint with the whitelisted Origin.
func itDial(t *testing.T, srv *httptest.Server) *websocket.Conn {
	t.Helper()
	d := websocket.Dialer{HandshakeTimeout: 2 * time.Second}
	header := http.Header{}
	header.Set("Origin", itOrigin)
	u := "ws" + strings.TrimPrefix(srv.URL, "http")
	conn, _, err := d.Dial(u, header)
	if err != nil {
		t.Fatalf("dial /rt: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

// itMsg is one received websocket message; typ is the control type for text
// frames ("" for binary).
type itMsg struct {
	mt  int
	typ string
	raw []byte
}

// itProbe types a received message (typ is "" for binary frames).
func itProbe(mt int, data []byte) itMsg {
	m := itMsg{mt: mt, raw: data}
	if mt == websocket.TextMessage {
		var probe struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(data, &probe) == nil {
			m.typ = probe.Type
		}
	}
	return m
}

// itRead reads messages until match returns true. One overall deadline
// governs the wait; gorilla forbids reads after ANY read error (even a
// deadline timeout), so an error is terminal here — no retry loop.
func itRead(t *testing.T, conn *websocket.Conn, timeout time.Duration, match func(itMsg) bool) itMsg {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(timeout))
	for {
		mt, data, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("itRead: no matching message within %v: %v", timeout, err)
		}
		m := itProbe(mt, data)
		if match(m) {
			return m
		}
	}
}

// itDrainUntilTimeout collects every message arriving inside the window and
// then returns. NOTE: the connection is poisoned after the closing timeout
// read (gorilla sticky read error) — use only as the LAST read phase of a
// test.
func itDrainUntilTimeout(t *testing.T, conn *websocket.Conn, window time.Duration) []itMsg {
	t.Helper()
	var seen []itMsg
	deadline := time.Now().Add(window)
	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return seen
		}
		_ = conn.SetReadDeadline(time.Now().Add(remaining))
		mt, data, err := conn.ReadMessage()
		if err != nil {
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				return seen // window elapsed with nothing in flight
			}
			t.Fatalf("itDrainUntilTimeout: connection failed inside the quiet window: %v", err)
		}
		seen = append(seen, itProbe(mt, data))
	}
}

// itSendText writes one JSON control frame.
func itSendText(t *testing.T, conn *websocket.Conn, v any) {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal control: %v", err)
	}
	if err := conn.WriteMessage(websocket.TextMessage, b); err != nil {
		t.Fatalf("write control: %v", err)
	}
}

// itUtteranceLeadIn is the pause before an utterance burst: a real child
// starts speaking only after the teacher finished (the echo tail has run)
// and outside the mic-open start grace. The harness bursts all frames at
// wire speed, so the lead-in must cover the 13-09 gating windows (start
// grace 1000ms after session_ready + greeting, echo tail 500ms after the
// last ack) — otherwise the WHOLE utterance lands inside a window and is
// dropped by the very gate under test.
const itUtteranceLeadIn = 1400 * time.Millisecond

// itSendUtterance writes one loud-speech + silence sequence that closes a
// VAD segment (one turn).
func itSendUtterance(t *testing.T, conn *websocket.Conn) {
	t.Helper()
	time.Sleep(itUtteranceLeadIn)
	loud := vadSine(12000, 30)
	quiet := vadSilence(30)
	for i := 0; i < 50; i++ { // 1.5 s of speech
		if err := conn.WriteMessage(websocket.BinaryMessage, loud); err != nil {
			t.Fatalf("write speech frame: %v", err)
		}
	}
	for i := 0; i < 30; i++ { // 0.9 s of silence closes the segment
		if err := conn.WriteMessage(websocket.BinaryMessage, quiet); err != nil {
			t.Fatalf("write silence frame: %v", err)
		}
	}
}

// itInit sends session_init and returns the session_ready message.
func itInit(t *testing.T, conn *websocket.Conn) SessionReadyMsg {
	t.Helper()
	itSendText(t, conn, SessionInitMsg{
		Type:  TypeSessionInit,
		Audio: AudioSpec{SampleRate: 16000, Format: "s16le", Channels: 1},
	})
	ready := itRead(t, conn, 5*time.Second, func(m itMsg) bool { return m.typ == TypeSessionReady })
	var readyMsg SessionReadyMsg
	if err := json.Unmarshal(ready.raw, &readyMsg); err != nil {
		t.Fatalf("session_ready decode: %v", err)
	}
	return readyMsg
}

// itAnnounceAck reads messages through one announcement: acks every
// audio_end (completion-ack semantics) and returns the announcement's
// teacher sentence texts once the FSM hands control back (listening).
func itAnnounceAck(t *testing.T, conn *websocket.Conn) []string {
	texts, _ := itAnnounceAckCounted(t, conn)
	return texts
}

// itAnnounceAckCounted is itAnnounceAck, also returning the number of
// binary PCM frames delivered for the announcement (报词音频字节 evidence).
func itAnnounceAckCounted(t *testing.T, conn *websocket.Conn) ([]string, int) {
	t.Helper()
	var texts []string
	binaryFrames := 0
	deadline := time.Now().Add(15 * time.Second)
	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			t.Fatal("announcement never settled to listening")
		}
		_ = conn.SetReadDeadline(time.Now().Add(remaining))
		mt, data, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("itAnnounceAck read: %v", err)
		}
		if mt == websocket.BinaryMessage {
			binaryFrames++
			continue
		}
		m := itProbe(mt, data)
		switch m.typ {
		case TypeTeacherSentence:
			var ts TeacherSentenceMsg
			_ = json.Unmarshal(data, &ts)
			texts = append(texts, ts.Text)
		case TypeAudioEnd:
			var ae AudioEndMsg
			_ = json.Unmarshal(data, &ae)
			itSendText(t, conn, SentenceAckMsg{Type: TypeSentenceAck, SentenceID: ae.SentenceID, Epoch: ae.Epoch})
		case TypeState:
			var sm StateMsg
			_ = json.Unmarshal(data, &sm)
			if sm.Value == StateListening {
				return texts, binaryFrames
			}
		}
	}
}

// itHoldThenAck schedules the sentence ack after holdDelay while the caller
// keeps reading: if the server ends the session BEFORE the ack was sent,
// the test fails (the natural-end ack gate, observed over the real wire).
func itHoldThenAck(t *testing.T, conn *websocket.Conn, holdDelay time.Duration, sentenceID string, epoch uint64) *atomic.Bool {
	t.Helper()
	var ackSent atomic.Bool
	go func() {
		time.Sleep(holdDelay)
		itSendText(t, conn, SentenceAckMsg{Type: TypeSentenceAck, SentenceID: sentenceID, Epoch: epoch})
		ackSent.Store(true)
	}()
	return &ackSent
}

// itReadUntilSessionEnded reads until session_ended, failing if it arrives
// before ackSent flipped true (any earlier arrival = the FSM ended without
// the matching playback ack).
func itReadUntilSessionEnded(t *testing.T, conn *websocket.Conn, ackSent *atomic.Bool) SessionEndedMsg {
	t.Helper()
	m := itRead(t, conn, 15*time.Second, func(m itMsg) bool { return m.typ == TypeSessionEnded })
	if !ackSent.Load() {
		t.Fatal("session_ended arrived before the matching sentence_ack was sent")
	}
	var ended SessionEndedMsg
	if err := json.Unmarshal(m.raw, &ended); err != nil {
		t.Fatalf("session_ended decode: %v", err)
	}
	return ended
}

// ---------------------------------------------------------------------------
// TestDictationOverConnHappyPath: the whole dictation loop over the real
// transport — 3 words advanced through scripted confirms, completion
// summary held for its ack, lesson_complete only after it, one full-batch
// SubmitReview.
// ---------------------------------------------------------------------------

func TestDictationOverConnHappyPath(t *testing.T) {
	cards := ttCards("苹果", "香蕉", "橘子")
	words := newFakeWords(cards)
	words.reviewResult = ReviewWriteResult{
		Requested: []string{"苹果", "香蕉", "橘子"},
		Updated:   []string{"苹果", "香蕉", "橘子"},
	}
	exec := newFakeExecutorNoRow("it-lesson")
	asr := newFakeASR(asrScript{text: "好了"}, asrScript{text: "好了"}, asrScript{text: "好了"})
	llm := newFakeLLM(
		intentScript(intentConfirm, ""),
		intentScript(intentConfirm, ""),
		intentScript(intentConfirm, ""),
	)
	tts := newFakeTTS(2, 320)
	tts.delay = 20 * time.Millisecond
	srv := itServer(t, asr, llm, tts, exec, words)
	conn := itDial(t, srv)

	ready := itInit(t, conn)
	// 13.1-02 student semantics (D-08 default): undictated words leave the
	// wire as "" — the full reveal rides ?mode=test only.
	if len(ready.Lesson.Words) != 3 || ready.Lesson.Words[0] != "" || ready.Lesson.Words[2] != "" {
		t.Fatalf("session_ready.lesson.words = %v, want 3 masked student entries", ready.Lesson.Words)
	}

	// Opening greeting (teacher speaks immediately after session_ready);
	// the announcement's PCM bytes must reach the client side. The subtitle
	// is the masked position variant (D-10) — the word rides the audio only.
	greeting, greetingFrames := itAnnounceAckCounted(t, conn)
	joinedGreeting := strings.Join(greeting, "")
	if len(greeting) < 2 || !strings.Contains(joinedGreeting, "第 1 个词") || strings.Contains(joinedGreeting, "苹果") {
		t.Fatalf("greeting = %v, want the masked 第 1 个词 opening", greeting)
	}
	if greetingFrames < 2 {
		t.Errorf("greeting binary frames = %d, want >0 (word-announcement audio bytes must reach the client)", greetingFrames)
	}

	// Three confirms: word 1 → 2 → 3; the first two announce the next word.
	var versions, indexes []int
	for i := 0; i < 3; i++ {
		itSendUtterance(t, conn)
		upd := itRead(t, conn, 15*time.Second, func(m itMsg) bool { return m.typ == TypeLessonUpdate })
		var lu LessonUpdateMsg
		if err := json.Unmarshal(upd.raw, &lu); err != nil {
			t.Fatalf("lesson_update decode: %v", err)
		}
		versions = append(versions, lu.Version)
		indexes = append(indexes, lu.CurrentIndex)
		if lu.RequestID != lessonRequestID("it-lesson", i) {
			t.Errorf("lesson_update %d request_id = %q, want %q (FSM-derived target key)", i, lu.RequestID, lessonRequestID("it-lesson", i))
		}
		if i < 2 {
			itAnnounceAck(t, conn) // next-word announcement
		}
	}
	if len(versions) != 3 || versions[0] != 2 || versions[1] != 3 || versions[2] != 4 {
		t.Errorf("lesson_update versions = %v, want [2 3 4] (strictly increasing)", versions)
	}
	if len(indexes) != 3 || indexes[0] != 1 || indexes[1] != 2 || indexes[2] != 3 {
		t.Errorf("lesson_update indexes = %v, want [1 2 3]", indexes)
	}

	// 13.1-01: the last commit opens the CHECK stage — no immediate summary,
	// no SubmitReview yet. check_started carries the FULL plaintext list
	// (the D-10 unmask point over the real wire).
	startedRaw := itRead(t, conn, 15*time.Second, func(m itMsg) bool { return m.typ == TypeCheckStarted })
	var started CheckStartedMsg
	if err := json.Unmarshal(startedRaw.raw, &started); err != nil {
		t.Fatalf("check_started decode: %v", err)
	}
	if len(started.Words) != 3 || started.Words[0] != "苹果" || started.Words[2] != "橘子" {
		t.Errorf("check_started.words = %v, want the full plaintext [苹果 香蕉 橘子]", started.Words)
	}
	if started.Reason != CheckStartedReasonDictationDone {
		t.Errorf("check_started.reason = %q, want %s", started.Reason, CheckStartedReasonDictationDone)
	}
	if got := len(words.submissions()); got != 0 {
		t.Fatalf("SubmitReview calls before check_submit = %d, want 0 (D-01)", got)
	}

	// The check bridge announcement settles through the ACK gate; then the
	// harness marks every word correct via check_verdict frames and reads
	// the authoritative echoes.
	itAnnounceAck(t, conn)
	for i := 0; i < 3; i++ {
		itSendText(t, conn, CheckVerdictMsg{
			Type: TypeCheckVerdict, RequestID: fmt.Sprintf("it-v-%d", i), WordIndex: i, Correct: true,
		})
		verdictsRaw := itRead(t, conn, 10*time.Second, func(m itMsg) bool { return m.typ == TypeCheckVerdicts })
		var echo CheckVerdictsMsg
		if err := json.Unmarshal(verdictsRaw.raw, &echo); err != nil {
			t.Fatalf("check_verdicts decode: %v", err)
		}
		if len(echo.Verdicts) != i+1 {
			t.Fatalf("check_verdicts %d = %+v, want %d entries", i, echo.Verdicts, i+1)
		}
		for _, v := range echo.Verdicts {
			if !v.Manual || !v.Correct {
				t.Errorf("verdict %+v, want manual correct (screen echo is the render source)", v)
			}
		}
	}

	// Unified submit: one honest review_result, THEN the battle summary
	// held for its ack, THEN session_ended(lesson_complete).
	itSendText(t, conn, CheckSubmitMsg{Type: TypeCheckSubmit, RequestID: "it-submit"})
	rr := itRead(t, conn, 15*time.Second, func(m itMsg) bool { return m.typ == TypeReviewResult })
	var result ReviewResultMsg
	if err := json.Unmarshal(rr.raw, &result); err != nil {
		t.Fatalf("review_result decode: %v", err)
	}
	if result.Requested != 3 || result.Updated != 3 || result.Missing != 0 {
		t.Errorf("review_result = %+v, want requested 3 / updated 3 / missing 0", result)
	}

	var (
		summaryTexts    []string
		lastAckID       string
		lastAckEpoch    uint64
		summaryAudioEnd int
	)
	deadline := time.Now().Add(15 * time.Second)
	for summaryAudioEnd < 2 { // summary = 2 sentences for a full 3/3 batch
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
	if len(summaryTexts) < 1 || !strings.Contains(strings.Join(summaryTexts, ""), "3 个词对了 3 个") {
		t.Errorf("summary = %v, want the 3/3 battle wrap-up (D-04)", summaryTexts)
	}

	ackSent := itHoldThenAck(t, conn, 600*time.Millisecond, lastAckID, lastAckEpoch)
	ended := itReadUntilSessionEnded(t, conn, ackSent)
	if ended.Reason != ReasonLessonComplete {
		t.Errorf("reason = %q, want %s", ended.Reason, ReasonLessonComplete)
	}

	// One full-batch review write through the stage latch, executed before
	// the summary aired; the checked-correct-no-hint words rate good.
	subs := words.submissions()
	if len(subs) != 1 {
		t.Fatalf("SubmitReview calls = %d, want exactly 1", len(subs))
	}
	for _, w := range []string{"苹果", "香蕉", "橘子"} {
		if subs[0][w] != "good" {
			t.Errorf("submitted rating for %s = %q, want good", w, subs[0][w])
		}
	}
}

// lessonRequestID mirrors the FSM's advance request key (dictation.go).
func lessonRequestID(lessonID string, wordIndex int) string {
	return fmt.Sprintf("%s-advance-%d", lessonID, wordIndex)
}

// TestDictationOverConnInterruptMidAnnouncement: interrupting mid-announcement
// invalidates the old epoch's pending acks and NO late binary frame arrives;
// the classroom still advances on the next confirmed turn.
func TestDictationOverConnInterruptMidAnnouncement(t *testing.T) {
	cards := ttCards("苹果", "香蕉")
	words := newFakeWords(cards)
	exec := newFakeExecutorNoRow("it-lesson")
	asr := newFakeASR(asrScript{text: "好了"})
	llm := newFakeLLM(intentScript(intentConfirm, ""))
	tts := newFakeTTS(8, 320) // 8 chunks × 40 ms: a stream to interrupt
	tts.delay = 40 * time.Millisecond
	srv := itServer(t, asr, llm, tts, exec, words)
	conn := itDial(t, srv)

	itInit(t, conn)

	// Wait for the first greeting chunk, then interrupt immediately.
	itRead(t, conn, 10*time.Second, func(m itMsg) bool { return m.mt == websocket.BinaryMessage }) // BinaryMessage
	itSendText(t, conn, InterruptMsg{Type: TypeInterrupt, Epoch: 0, LastSentenceID: "t1-s0", PlayedUntilMs: 250})

	ack := itRead(t, conn, 5*time.Second, func(m itMsg) bool { return m.typ == TypeInterruptAck })
	var ackMsg InterruptAckMsg
	if err := json.Unmarshal(ack.raw, &ackMsg); err != nil {
		t.Fatalf("interrupt_ack decode: %v", err)
	}
	if ackMsg.NewEpoch <= ackMsg.OldEpoch {
		t.Errorf("epoch = old %d -> new %d, want new > old", ackMsg.OldEpoch, ackMsg.NewEpoch)
	}

	// From the ack to the FSM's listening state: NO binary frame may arrive
	// (no late audio after the interrupt).
	for {
		m := itRead(t, conn, 5*time.Second, func(m itMsg) bool {
			return m.mt == websocket.BinaryMessage || m.typ == TypeState
		})
		if m.mt == websocket.BinaryMessage {
			t.Fatal("binary frame arrived after the interrupt ack (late audio resurrected)")
		}
		var sm StateMsg
		if err := json.Unmarshal(m.raw, &sm); err == nil && sm.Value == StateListening {
			break
		}
	}

	// The classroom still advances: confirm word 1 over the same session.
	itSendUtterance(t, conn)
	upd := itRead(t, conn, 15*time.Second, func(m itMsg) bool { return m.typ == TypeLessonUpdate })
	var lu LessonUpdateMsg
	if err := json.Unmarshal(upd.raw, &lu); err != nil {
		t.Fatalf("lesson_update decode: %v", err)
	}
	if lu.Version != 2 || lu.CurrentIndex != 1 {
		t.Errorf("lesson_update = %+v, want version 2 index 1 (lesson advanced after the interrupt)", lu)
	}
	itAnnounceAck(t, conn) // settle the next-word announcement
}

// TestDictationOverConnEarlyNoiseDoesNotStartTurn: a burst of high-energy
// PCM fired immediately after session_ready lands inside the 13-09 start
// grace (and the greeting's playback gate) — the server cuts NO phantom
// turn (no turn_start, no extra teacher sentence), and the normal confirm
// flow still works afterwards.
func TestDictationOverConnEarlyNoiseDoesNotStartTurn(t *testing.T) {
	cards := ttCards("苹果", "香蕉")
	words := newFakeWords(cards)
	exec := newFakeExecutorNoRow("it-lesson")
	asr := newFakeASR(asrScript{text: "好了"})
	llm := newFakeLLM(intentScript(intentConfirm, ""))
	tts := newFakeTTS(2, 320)
	tts.delay = 20 * time.Millisecond
	srv := itServer(t, asr, llm, tts, exec, words)
	conn := itDial(t, srv)

	itInit(t, conn)

	// ~500ms of loud synthetic PCM, sent at burst speed right after ready.
	noise := vadSine(12000, 30)
	for i := 0; i < 17; i++ {
		if err := conn.WriteMessage(websocket.BinaryMessage, noise); err != nil {
			t.Fatalf("write noise: %v", err)
		}
	}

	// Read through the greeting while tallying: only the greeting's three
	// sentences may appear, and no turn_start may exist.
	sentences, turnStarts := 0, 0
	settled := false
	deadline := time.Now().Add(15 * time.Second)
	for !settled {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			t.Fatal("greeting never settled to listening")
		}
		_ = conn.SetReadDeadline(time.Now().Add(remaining))
		mt, data, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("greeting read: %v", err)
		}
		m := itProbe(mt, data)
		switch m.typ {
		case TypeTeacherSentence:
			sentences++
		case TypeTurnStart:
			turnStarts++
		case TypeAudioEnd:
			var ae AudioEndMsg
			_ = json.Unmarshal(data, &ae)
			itSendText(t, conn, SentenceAckMsg{Type: TypeSentenceAck, SentenceID: ae.SentenceID, Epoch: ae.Epoch})
		case TypeState:
			var sm StateMsg
			_ = json.Unmarshal(data, &sm)
			if sm.Value == StateListening {
				settled = true
			}
		}
	}
	if turnStarts != 0 {
		t.Errorf("turn_start during the noise window = %d, want 0 (the grace suppressed the phantom turn)", turnStarts)
	}
	if sentences != 3 {
		t.Errorf("teacher sentences = %d, want exactly the greeting's 3", sentences)
	}

	// The classroom still works: a normal utterance confirms word 1.
	itSendUtterance(t, conn)
	upd := itRead(t, conn, 15*time.Second, func(m itMsg) bool { return m.typ == TypeLessonUpdate })
	var lu LessonUpdateMsg
	if err := json.Unmarshal(upd.raw, &lu); err != nil {
		t.Fatalf("lesson_update decode: %v", err)
	}
	if lu.Version != 2 || lu.CurrentIndex != 1 {
		t.Errorf("lesson_update = %+v, want version 2 index 1 (normal flow after the noise)", lu)
	}
	itAnnounceAck(t, conn) // settle the next-word announcement
}

// TestDictationOverConnNoDueWaitsForAck: with zero due words the teacher's
// explanation is fully delivered and session_ended(no_due_words) arrives
// only after the explanation's actual-playback ack — the ending never
// truncates the explanation.
func TestDictationOverConnNoDueWaitsForAck(t *testing.T) {
	words := newFakeWords() // zero due
	exec := newFakeExecutorNoRow("it-lesson")
	tts := newFakeTTS(2, 320)
	tts.delay = 20 * time.Millisecond
	srv := itServer(t, newFakeASR(), newFakeLLM(), tts, exec, words)
	conn := itDial(t, srv)

	ready := itInit(t, conn)
	if len(ready.Lesson.Words) != 0 {
		t.Fatalf("session_ready.lesson.words = %v, want empty", ready.Lesson.Words)
	}

	// Read the explanation through its audio_end without acking.
	var lastAckID string
	var lastAckEpoch uint64
	m := itRead(t, conn, 15*time.Second, func(m itMsg) bool { return m.typ == TypeAudioEnd })
	var ae AudioEndMsg
	if err := json.Unmarshal(m.raw, &ae); err != nil {
		t.Fatalf("audio_end decode: %v", err)
	}
	lastAckID, lastAckEpoch = ae.SentenceID, ae.Epoch

	ackSent := itHoldThenAck(t, conn, 600*time.Millisecond, lastAckID, lastAckEpoch)
	ended := itReadUntilSessionEnded(t, conn, ackSent)
	if ended.Reason != ReasonNoDueWords {
		t.Errorf("reason = %q, want %s", ended.Reason, ReasonNoDueWords)
	}
	if got := len(words.submissions()); got != 0 {
		t.Errorf("SubmitReview calls = %d, want 0 (no due words, no review)", got)
	}
}
