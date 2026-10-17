// turn_integration_test.go — in-process whole-chain integration (13-08
// Task 3): a real httptest /rt server + gorilla dialer + fakes, driving the
// dictation flow — session_init → greeting → scripted-ASR confirm →
// lesson_update → next-word announcement → completion → session_ended —
// over the actual websocket transport (13-04 harness, review Suggestion 8).
// Assembly-class defects (sender concurrency, ack gating, natural-end
// sequencing) regress here instead of relying on unit fixtures.
package rt

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/xiaotian/home-tutor-agent/pkg/database"
	"github.com/xiaotian/home-tutor-agent/pkg/infra"
	"github.com/xiaotian/home-tutor-agent/pkg/lesson"
)

// itOrigin is the whitelisted Origin these tests dial with.
const itOrigin = "http://origin.test"

// itServer mounts one /rt handler backed by the given fakes, executor, and
// word source.
func itServer(t *testing.T, asr ASRProvider, llm LLMProvider, tts TTSSynthesizer, exec LessonExecutor, words WordSource) *httptest.Server {
	t.Helper()
	return itServerCfg(t, asr, llm, tts, exec, words, nil)
}

// itServerCfg is itServer with a config mutation hook (13.1-05: the Practice
// seam + practice store ride in through TurnManagerConfig).
func itServerCfg(t *testing.T, asr ASRProvider, llm LLMProvider, tts TTSSynthesizer, exec LessonExecutor, words WordSource, mutate func(*TurnManagerConfig)) *httptest.Server {
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
			cfg := TurnManagerConfig{
				LessonID: "it-lesson",
				Words:    words,
			}
			if mutate != nil {
				mutate(&cfg)
			}
			return NewTurnManager(asr, llm, tts, exec, cfg,
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
	typ string
	raw []byte
	mt  int
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

// itInitPractice is itInit with practice=true on session_init (13.1-05).
func itInitPractice(t *testing.T, conn *websocket.Conn) SessionReadyMsg {
	t.Helper()
	itSendText(t, conn, SessionInitMsg{
		Type:     TypeSessionInit,
		Audio:    AudioSpec{SampleRate: 16000, Format: "s16le", Channels: 1},
		Practice: true,
	})
	ready := itRead(t, conn, 5*time.Second, func(m itMsg) bool { return m.typ == TypeSessionReady })
	var readyMsg SessionReadyMsg
	if err := json.Unmarshal(ready.raw, &readyMsg); err != nil {
		t.Fatalf("session_ready decode: %v", err)
	}
	return readyMsg
}

// itInitCategory is itInit with an explicit classroom subject (Issue #10).
func itInitCategory(t *testing.T, conn *websocket.Conn, category string) SessionReadyMsg {
	t.Helper()
	itSendText(t, conn, SessionInitMsg{
		Type:     TypeSessionInit,
		Audio:    AudioSpec{SampleRate: 16000, Format: "s16le", Channels: 1},
		Category: category,
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
	if len(summaryTexts) < 1 || !strings.Contains(strings.Join(summaryTexts, ""), "对了 3 个") {
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

// ---------------------------------------------------------------------------
// 13.1-04 reconnect paths: check resume (row still checking — crash before
// the close fallback, or a pre-fallback row), the unchecked-correction flow
// (row submitted+unchecked after the D-03 fallback), and the legacy
// completed short-circuit that must keep working under the new branches.
// ---------------------------------------------------------------------------

// itAckEnding reads through a natural-ending announcement, acks every
// sentence as its audio_end arrives, and returns the session_ended message
// (asserting its reason). session_ended before ANY ack fails the test (the
// ack gate).
func itAckEnding(t *testing.T, conn *websocket.Conn, wantReason string) SessionEndedMsg {
	t.Helper()
	acks := 0
	deadline := time.Now().Add(15 * time.Second)
	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			t.Fatal("natural ending never produced session_ended")
		}
		_ = conn.SetReadDeadline(time.Now().Add(remaining))
		mt, data, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("itAckEnding read: %v", err)
		}
		m := itProbe(mt, data)
		switch m.typ {
		case TypeAudioEnd:
			var ae AudioEndMsg
			_ = json.Unmarshal(data, &ae)
			itSendText(t, conn, SentenceAckMsg{Type: TypeSentenceAck, SentenceID: ae.SentenceID, Epoch: ae.Epoch})
			acks++
		case TypeSessionEnded:
			if acks == 0 {
				t.Fatal("session_ended arrived before any sentence_ack (ack gate broken)")
			}
			var ended SessionEndedMsg
			if err := json.Unmarshal(data, &ended); err != nil {
				t.Fatalf("session_ended decode: %v", err)
			}
			if ended.Reason != wantReason {
				t.Fatalf("session_ended.reason = %q, want %s", ended.Reason, wantReason)
			}
			return ended
		}
	}
}

// itReadCheckStarted reads the check_started frame and decodes it.
func itReadCheckStarted(t *testing.T, conn *websocket.Conn) CheckStartedMsg {
	t.Helper()
	m := itRead(t, conn, 10*time.Second, func(m itMsg) bool { return m.typ == TypeCheckStarted })
	var started CheckStartedMsg
	if err := json.Unmarshal(m.raw, &started); err != nil {
		t.Fatalf("check_started decode: %v", err)
	}
	return started
}

// itReadCheckVerdicts reads the authoritative verdict echo and decodes it.
func itReadCheckVerdicts(t *testing.T, conn *websocket.Conn) CheckVerdictsMsg {
	t.Helper()
	m := itRead(t, conn, 10*time.Second, func(m itMsg) bool { return m.typ == TypeCheckVerdicts })
	var echo CheckVerdictsMsg
	if err := json.Unmarshal(m.raw, &echo); err != nil {
		t.Fatalf("check_verdicts decode: %v", err)
	}
	return echo
}

// itSeedCheckRow persists a stage='checking' row (with outcomes and optional
// verdicts) through the executor's own idempotent writes — the state a
// session leaves behind when it dies before any close path ran. The cursor
// advances to the end first, mirroring the only real path into the check
// stage (the last advance commit's finished branch).
func itSeedCheckRow(t *testing.T, exec *fakeExecutor, outcomes map[string]lesson.WordOutcome, verdicts ...lesson.CheckVerdict) lesson.Lesson {
	t.Helper()
	ctx := context.Background()
	l, err := exec.EnsureLessonCards(ctx, "it-lesson", itCards, lesson.CategoryEnglish)
	if err != nil {
		t.Fatalf("EnsureLessonCards: %v", err)
	}
	version := l.Version
	for i := range itCards {
		res, advErr := exec.AdvanceWord(ctx, "it-lesson", version, fmt.Sprintf("it-seed-adv-%d", i))
		if advErr != nil {
			t.Fatalf("AdvanceWord seed %d: %v", i, advErr)
		}
		version = res.NewVersion
	}
	ec, err := exec.EnterCheck(ctx, "it-lesson", version, "it-lesson-enter-check", outcomes)
	if err != nil {
		t.Fatalf("EnterCheck: %v", err)
	}
	version = ec.NewVersion
	for i, v := range verdicts {
		res, err := exec.ApplyVerdict(ctx, "it-lesson", version, fmt.Sprintf("it-seed-v-%d", i), v)
		if err != nil {
			t.Fatalf("ApplyVerdict seed %d: %v", i, err)
		}
		version = res.NewVersion
	}
	return l
}

// itCards is the shared integration card list (2 words: the second carries a
// hint so the rating tables are distinguishable).
var itCards = []lesson.Word{
	{Word: "苹果", Meaning: "cat", Example: "x"},
	{Word: "香蕉", Meaning: "dog", Example: "y"},
}

// TestCheckResumeMidCheck (13.1-04, Pitfall 2): a row still sitting in
// stage='checking' reopens INTO the check panel — session_ready, then
// check_started(reason=resume) with the full plaintext list, the STORED
// verdicts echoed verbatim — and the student finishes the check normally:
// the remaining verdict, the unified submit, the battle summary, the acked
// ending. Ratings derive from checkRating over the STORED behavior.
func TestCheckResumeMidCheck(t *testing.T) {
	words := newFakeWords(itCards)
	words.reviewResult = ReviewWriteResult{
		Requested: []string{"苹果", "香蕉"},
		Updated:   []string{"苹果", "香蕉"},
	}
	exec := newFakeExecutorNoRow("it-lesson")
	itSeedCheckRow(t, exec,
		map[string]lesson.WordOutcome{
			"0": {Outcome: "confirmed", Hints: 0},
			"1": {Outcome: "confirmed", Hints: 1},
		},
		lesson.CheckVerdict{WordIndex: 0, Correct: true, Manual: true},
	)
	tts := newFakeTTS(3, 320)
	tts.delay = 10 * time.Millisecond
	srv := itServer(t, newFakeASR(), newFakeLLM(), tts, exec, words)
	conn := itDial(t, srv)

	ready := itInit(t, conn)
	// Dictation is done (cursor at the end): the student view reveals ALL
	// words even before check_started (D-09 revealed=all on resume).
	if len(ready.Lesson.Words) != 2 || ready.Lesson.Words[0] != "苹果" || ready.Lesson.Words[1] != "香蕉" {
		t.Fatalf("session_ready.lesson.words = %v, want fully revealed (dictation done)", ready.Lesson.Words)
	}

	started := itReadCheckStarted(t, conn)
	if started.Reason != CheckStartedReasonResume {
		t.Errorf("check_started.reason = %q, want %s", started.Reason, CheckStartedReasonResume)
	}
	if len(started.Words) != 2 || started.Words[0] != "苹果" || started.Words[1] != "香蕉" {
		t.Errorf("check_started.words = %v, want full plaintext", started.Words)
	}
	echo := itReadCheckVerdicts(t, conn)
	if len(echo.Verdicts) != 1 || echo.Verdicts[0].WordIndex != 0 || !echo.Verdicts[0].Manual || !echo.Verdicts[0].Correct {
		t.Fatalf("check_verdicts = %+v, want the STORED verdict for word 0 (manual correct)", echo.Verdicts)
	}

	// The resume bridge settles through the ACK gate before the panel is
	// usable (every announcement does; the ack order is what the gate owns).
	itAnnounceAck(t, conn)

	// Finish the check: word 1 correct, then the unified submit.
	itSendText(t, conn, CheckVerdictMsg{Type: TypeCheckVerdict, RequestID: "it-resume-v-1", WordIndex: 1, Correct: true})
	echo = itReadCheckVerdicts(t, conn)
	if len(echo.Verdicts) != 2 {
		t.Fatalf("check_verdicts after resume verdict = %+v, want 2 entries", echo.Verdicts)
	}
	itSendText(t, conn, CheckSubmitMsg{Type: TypeCheckSubmit, RequestID: "it-resume-submit"})
	m := itRead(t, conn, 15*time.Second, func(m itMsg) bool { return m.typ == TypeReviewResult })
	var result ReviewResultMsg
	if err := json.Unmarshal(m.raw, &result); err != nil {
		t.Fatalf("review_result decode: %v", err)
	}
	if result.Requested != 2 || result.Updated != 2 || result.Unchecked {
		t.Errorf("review_result = %+v, want 2/2 checked submit", result)
	}

	itAckEnding(t, conn, ReasonLessonComplete)

	// Ratings = the D-02 checkRating table over stored behavior: 苹果
	// correct+0 hints → good; 香蕉 correct+1 hint → hard. Exactly ONE
	// SubmitReview (no fallback ran — the row never closed mid-check).
	subs := words.submissions()
	if len(subs) != 1 {
		t.Fatalf("SubmitReview calls = %d, want exactly 1", len(subs))
	}
	if subs[0]["苹果"] != "good" || subs[0]["香蕉"] != "hard" {
		t.Errorf("ratings = %v, want 苹果=good (correct,0) 香蕉=hard (correct,1)", subs[0])
	}
	exec.mu.Lock()
	stage, unchecked := exec.lesson.Stage, exec.lesson.Unchecked
	exec.mu.Unlock()
	if stage != lesson.StageSubmitted || unchecked {
		t.Errorf("row = stage %q unchecked %v, want submitted/false", stage, unchecked)
	}
}

// TestUncheckedCorrection (13.1-04, D-03 post-hoc correction): a
// submitted+unchecked row (what the close fallback persists) reopens into
// the CORRECTION panel — check_started(reason=correction) with the derived
// downgraded verdicts visible as pending (manual=false) hints; check_submit
// is REFUSED until every word is human-confirmed (no second write); after
// per-word confirmation with ONE override only the overridden word rides the
// second SubmitReview batch, the correction event carries the changed count,
// ClearUnchecked resets the marker, and the honest wrap ends the session.
func TestUncheckedCorrection(t *testing.T) {
	words := newFakeWords(itCards)
	words.reviewResult = ReviewWriteResult{Requested: []string{"香蕉"}, Updated: []string{"香蕉"}}
	exec := newFakeExecutorNoRow("it-lesson")
	// Seed exactly what uncheckedCloseFallback persists: outcomes stored,
	// stage submitted, unchecked=1, verdicts empty. 苹果: confirmed+0 →
	// fallback hard (derived correct); 香蕉: confirmed+1 → fallback again
	// (derived wrong).
	ctx := context.Background()
	l, err := exec.EnsureLessonCards(ctx, "it-lesson", itCards, lesson.CategoryEnglish)
	if err != nil {
		t.Fatalf("EnsureLessonCards: %v", err)
	}
	version := l.Version
	for i := range itCards {
		res, aerr := exec.AdvanceWord(ctx, "it-lesson", version, fmt.Sprintf("it-seed-adv-%d", i))
		if aerr != nil {
			t.Fatalf("AdvanceWord seed %d: %v", i, aerr)
		}
		version = res.NewVersion
	}
	ec, err := exec.EnterCheck(ctx, "it-lesson", version, "it-lesson-enter-check", map[string]lesson.WordOutcome{
		"0": {Outcome: "confirmed", Hints: 0},
		"1": {Outcome: "confirmed", Hints: 1},
	})
	if err != nil {
		t.Fatalf("EnterCheck: %v", err)
	}
	if _, err := exec.CompleteCheck(ctx, "it-lesson", ec.NewVersion, "it-lesson-complete-check", true); err != nil {
		t.Fatalf("CompleteCheck seed: %v", err)
	}
	tts := newFakeTTS(3, 320)
	tts.delay = 10 * time.Millisecond
	srv := itServer(t, newFakeASR(), newFakeLLM(), tts, exec, words)
	conn := itDial(t, srv)

	itInit(t, conn)
	started := itReadCheckStarted(t, conn)
	if started.Reason != CheckStartedReasonCorrection {
		t.Errorf("check_started.reason = %q, want %s", started.Reason, CheckStartedReasonCorrection)
	}
	if len(started.Words) != 2 {
		t.Errorf("check_started.words = %v, want full plaintext", started.Words)
	}
	echo := itReadCheckVerdicts(t, conn)
	if len(echo.Verdicts) != 2 {
		t.Fatalf("check_verdicts = %+v, want 2 derived entries", echo.Verdicts)
	}
	for _, v := range echo.Verdicts {
		if v.Manual {
			t.Errorf("derived verdict %+v must stay manual=false (display hint, D-05)", v)
		}
	}
	if !echo.Verdicts[0].Correct || echo.Verdicts[1].Correct {
		t.Errorf("derived verdicts = %+v, want [correct (苹果 good→hard), wrong (香蕉 hard→again)]", echo.Verdicts)
	}
	itAnnounceAck(t, conn) // the correction bridge

	// Submit BEFORE confirming every word: refused, zero writes.
	itSendText(t, conn, CheckSubmitMsg{Type: TypeCheckSubmit, RequestID: "it-cor-early"})
	errRaw := itRead(t, conn, 10*time.Second, func(m itMsg) bool { return m.typ == TypeError })
	var errMsg ErrorMsg
	if err := json.Unmarshal(errRaw.raw, &errMsg); err != nil {
		t.Fatalf("error decode: %v", err)
	}
	if errMsg.Message != checkSubmitIncompleteNotice {
		t.Errorf("early submit error = %q, want %q", errMsg.Message, checkSubmitIncompleteNotice)
	}
	if got := len(words.submissions()); got != 0 {
		t.Fatalf("SubmitReview calls after early submit = %d, want 0", got)
	}

	// Confirm every word; 香蕉 is OVERRIDDEN to correct (the photo showed it
	// right — the human call wins, D-06①).
	itSendText(t, conn, CheckVerdictMsg{Type: TypeCheckVerdict, RequestID: "it-cor-v-0", WordIndex: 0, Correct: true})
	itReadCheckVerdicts(t, conn) // echo 1
	itSendText(t, conn, CheckVerdictMsg{Type: TypeCheckVerdict, RequestID: "it-cor-v-1", WordIndex: 1, Correct: true})
	echo = itReadCheckVerdicts(t, conn)
	if len(echo.Verdicts) != 2 {
		t.Fatalf("check_verdicts after confirmations = %+v, want 2 manual entries", echo.Verdicts)
	}
	for _, v := range echo.Verdicts {
		if !v.Manual || !v.Correct {
			t.Fatalf("confirmed verdicts = %+v, want all manual correct", echo.Verdicts)
		}
	}

	// The correction submit: only the OVERRIDDEN word rides the second
	// batch (Pitfall 7 — smallest possible second write).
	itSendText(t, conn, CheckSubmitMsg{Type: TypeCheckSubmit, RequestID: "it-cor-submit"})
	m := itRead(t, conn, 15*time.Second, func(m itMsg) bool { return m.typ == TypeReviewResult })
	var result ReviewResultMsg
	if err := json.Unmarshal(m.raw, &result); err != nil {
		t.Fatalf("review_result decode: %v", err)
	}
	if result.Requested != 1 || result.Updated != 1 || result.Unchecked {
		t.Errorf("review_result = %+v, want the 1-word correction batch (香蕉), unchecked=false", result)
	}

	itAckEnding(t, conn, ReasonLessonComplete)

	// Two SubmitReview calls total would mean the fallback reran — the
	// correction session itself writes exactly the diff batch. The seeded
	// fallback write never happened in-process, so exactly 1 call exists.
	subs := words.submissions()
	if len(subs) != 1 {
		t.Fatalf("SubmitReview calls = %d, want exactly 1 (the correction diff)", len(subs))
	}
	if len(subs[0]) != 1 || subs[0]["香蕉"] != "hard" {
		t.Errorf("correction batch = %v, want only 香蕉=hard (correct+1 hint)", subs[0])
	}
	exec.mu.Lock()
	stage, unchecked := exec.lesson.Stage, exec.lesson.Unchecked
	exec.mu.Unlock()
	if stage != lesson.StageSubmitted || unchecked {
		t.Errorf("row after correction = stage %q unchecked %v, want submitted/false (marker cleared)", stage, unchecked)
	}
}

// TestLegacyCompletedStillShortCircuits (13.1-04 regression): a
// stage='submitted' row WITHOUT the unchecked marker (the normal checked
// completion) still short-circuits on reconnect — session_ready +
// session_ended(lesson_complete), no check_started, no narration, no
// review write. The new stage-aware branches must not swallow it.
func TestLegacyCompletedStillShortCircuits(t *testing.T) {
	words := newFakeWords(itCards)
	exec := newFakeExecutor("it-lesson", itCards)
	exec.setRow(2, 4)
	tts := newFakeTTS(1, 320)
	srv := itServer(t, newFakeASR(), newFakeLLM(), tts, exec, words)
	conn := itDial(t, srv)

	ready := itInit(t, conn)
	if len(ready.Lesson.Words) == 0 {
		t.Fatalf("session_ready.lesson.words = %v, want the completed lesson view", ready.Lesson.Words)
	}
	// The short-circuit announces nothing: session_ended follows
	// session_ready directly (no ack gate involved).
	endedRaw := itRead(t, conn, 10*time.Second, func(m itMsg) bool { return m.typ == TypeSessionEnded })
	var ended SessionEndedMsg
	if err := json.Unmarshal(endedRaw.raw, &ended); err != nil {
		t.Fatalf("session_ended decode: %v", err)
	}
	if ended.Reason != ReasonLessonComplete {
		t.Errorf("reason = %q, want %s", ended.Reason, ReasonLessonComplete)
	}
	for _, m := range itDrainUntilTimeout(t, conn, 800*time.Millisecond) {
		if m.typ == TypeCheckStarted || m.typ == TypeTeacherSentence {
			t.Fatalf("unexpected %s frame on a completed reconnect (short-circuit broken)", m.typ)
		}
	}
	if got := len(words.submissions()); got != 0 {
		t.Errorf("SubmitReview calls = %d, want 0", got)
	}
}

// ---------------------------------------------------------------------------
// 13.1-05 practice session over the real transport (D-11/D-12/T-13.1-11):
// session_init(practice=true) never touches the due fetch nor the daily
// lesson namespace, the three-stage flow runs word by word, the submit
// writes practice_logs and NEVER SubmitReview, and the ending carries
// practice_complete.
// ---------------------------------------------------------------------------

func TestPracticeSessionEndToEnd(t *testing.T) {
	cst := time.FixedZone("CST", 8*3600)
	ctx := context.Background()

	// The vocabulary holds FOUR words; today's main lesson already used the
	// first two — practice must pick only from the remaining two.
	pool := []database.Card{
		{ID: 1, Word: "苹果", Meaning: "cat", Example: "x"},
		{ID: 2, Word: "香蕉", Meaning: "dog", Example: "y"},
		{ID: 3, Word: "橘子", Meaning: "orange", Example: "z"},
		{ID: 4, Word: "西瓜", Meaning: "melon", Example: "w"},
	}
	practiceSrc := &fakePracticeSource{cards: pool}
	ps := newTestPracticeStore(t)

	words := newFakeWords() // a due fetch here would mean the practice path leaked into FSRS
	exec := newFakeExecutorNoRow("it-lesson")
	// Seed today's main lesson row (the exclusion set's source).
	if _, err := exec.EnsureLessonCards(ctx, "it-lesson", itCards, lesson.CategoryEnglish); err != nil {
		t.Fatalf("seed daily row: %v", err)
	}

	asr := newFakeASR(asrScript{text: "好了"}, asrScript{text: "好了"})
	llm := newFakeLLM(intentScript(intentConfirm, ""), intentScript(intentConfirm, ""))
	tts := newFakeTTS(2, 320)
	tts.delay = 20 * time.Millisecond
	srv := itServerCfg(t, asr, llm, tts, exec, words, func(cfg *TurnManagerConfig) {
		cfg.Practice = practiceSrc
		cfg.PracticeStore = ps
	})
	conn := itDial(t, srv)

	ready := itInitPractice(t, conn)
	if !strings.HasPrefix(ready.Lesson.LessonID, "r0x-") {
		t.Fatalf("practice lesson id = %q, want the r0x- namespace (Pitfall 10)", ready.Lesson.LessonID)
	}
	if len(ready.Lesson.Words) != 2 {
		t.Fatalf("practice words = %v, want the 2 non-excluded words", ready.Lesson.Words)
	}
	for _, w := range ready.Lesson.Words {
		if w == "苹果" || w == "香蕉" {
			t.Errorf("practice word %q was already dictated today (避开当日已默 broken)", w)
		}
	}
	if got := words.fetchCount(); got != 0 {
		t.Fatalf("FetchDueWords calls = %d, want 0 (practice never runs the due fetch)", got)
	}
	exec.mu.Lock()
	ensureIDs := append([]string(nil), exec.ensureIDs...)
	exec.mu.Unlock()
	for _, id := range ensureIDs {
		if id != "it-lesson" && !strings.HasPrefix(id, "r0x-") {
			t.Errorf("EnsureLessonCards id = %q, want only the daily row or r0x- practice rows", id)
		}
	}

	// The practice greeting settles through the ACK gate before any
	// utterance (the uplink gate would otherwise drop it).
	itAnnounceAck(t, conn)

	// Three-stage flow, word by word (逐词 manual 确认过 D-05 提交闸).
	lessonID := ready.Lesson.LessonID
	for i := 0; i < 2; i++ {
		itSendUtterance(t, conn)
		upd := itRead(t, conn, 15*time.Second, func(m itMsg) bool { return m.typ == TypeLessonUpdate })
		var lu LessonUpdateMsg
		if err := json.Unmarshal(upd.raw, &lu); err != nil {
			t.Fatalf("lesson_update decode: %v", err)
		}
		if lu.LessonID != lessonID {
			t.Errorf("lesson_update %d lesson_id = %q, want %q", i, lu.LessonID, lessonID)
		}
		if i == 0 {
			itAnnounceAck(t, conn) // next-word announcement
		}
	}

	started := itReadCheckStarted(t, conn)
	if len(started.Words) != 2 {
		t.Fatalf("check_started.words = %v, want the 2 practice words in plaintext", started.Words)
	}
	itAnnounceAck(t, conn) // the check bridge
	for i := 0; i < 2; i++ {
		itSendText(t, conn, CheckVerdictMsg{
			Type: TypeCheckVerdict, RequestID: fmt.Sprintf("it-practice-v-%d", i), WordIndex: i, Correct: i == 0,
		})
		echo := itReadCheckVerdicts(t, conn)
		if len(echo.Verdicts) != i+1 {
			t.Fatalf("check_verdicts %d = %+v, want %d entries", i, echo.Verdicts, i+1)
		}
		for _, v := range echo.Verdicts {
			if !v.Manual {
				t.Errorf("verdict %+v must be manual (D-05 gate applies to practice too)", v)
			}
		}
	}

	// Submit: the practice write replaces the FSRS call — review_result
	// reports the record counts, and SubmitReview is NEVER called.
	itSendText(t, conn, CheckSubmitMsg{Type: TypeCheckSubmit, RequestID: "it-practice-submit"})
	rr := itRead(t, conn, 15*time.Second, func(m itMsg) bool { return m.typ == TypeReviewResult })
	var result ReviewResultMsg
	if err := json.Unmarshal(rr.raw, &result); err != nil {
		t.Fatalf("review_result decode: %v", err)
	}
	if result.Requested != 2 || result.Updated != 2 || result.Unchecked {
		t.Errorf("review_result = %+v, want requested 2 / updated 2 / unchecked false", result)
	}

	// Practice wrap summary speaks, then the ack-gated ending carries
	// practice_complete — the third ending copy (D-13).
	ended := itAckEnding(t, conn, ReasonPracticeComplete)
	_ = ended

	if subs := words.submissions(); len(subs) != 0 {
		t.Fatalf("SubmitReview calls = %d, want 0 (T-13.1-11: practice never enters the FSRS schedule)", len(subs))
	}

	// practice_logs holds every practiced word for the Beijing day — and
	// review-side storage (Python-owned) is out of reach by construction.
	day := time.Now().In(cst).Format("2006-01-02")
	practiced, err := ps.ListPracticeWordsOn(ctx, day)
	if err != nil {
		t.Fatalf("ListPracticeWordsOn: %v", err)
	}
	if len(practiced) != 2 {
		t.Fatalf("practice_logs rows for %s = %v, want both practice words", day, practiced)
	}
	for _, w := range practiced {
		if w == "苹果" || w == "香蕉" {
			t.Errorf("practice_logs holds today's dictated word %q (避开当日已默 broken)", w)
		}
	}
}

// ---------------------------------------------------------------------------
// 13.1-06: check-stage voice verdicts (D-06②) — the spoken per-word
// correction path converging on the same ApplyVerdict write as the screen
// buttons (RT-07), with the model's index/text payload validated server-side
// (T-13.1-13).
// ---------------------------------------------------------------------------

// checkIntentScript builds one classification response carrying the
// check-stage payload: index is the 1-based word position the student named,
// text the actual written form they spoke. omitempty drops zero values, so a
// scripted "missing index / missing text" case really reaches the server
// without those fields.
func checkIntentScript(intent string, index int, text string) llmScript {
	payload, _ := json.Marshal(struct {
		Intent string `json:"intent"`
		Reply  string `json:"reply"`
		Text   string `json:"text,omitempty"`
		Index  int    `json:"index,omitempty"`
	}{Intent: intent, Index: index, Text: text})
	wire := `{"choices":[{"index":0,"message":{"role":"assistant","content":` + strconv.Quote(string(payload)) + `}}]}`
	return llmScript{wire: wire}
}

// itVoiceCheckSession seeds a stage='checking' row (2 words, no verdicts
// beyond the optional seed list), opens the resume check panel over the real
// transport, and settles the bridge announcement — the harness state every
// voice-verdict test starts from.
func itVoiceCheckSession(t *testing.T, asr *fakeASR, llm *fakeLLM, exec *fakeExecutor, seedVerdicts ...lesson.CheckVerdict) (*websocket.Conn, *fakeWordSource) {
	t.Helper()
	words := newFakeWords(itCards)
	words.reviewResult = ReviewWriteResult{
		Requested: []string{"苹果", "香蕉"},
		Updated:   []string{"苹果", "香蕉"},
	}
	itSeedCheckRow(t, exec,
		map[string]lesson.WordOutcome{
			"0": {Outcome: "confirmed", Hints: 0},
			"1": {Outcome: "confirmed", Hints: 0},
		},
		seedVerdicts...,
	)
	tts := newFakeTTS(3, 320)
	tts.delay = 10 * time.Millisecond
	srv := itServer(t, asr, llm, tts, exec, words)
	conn := itDial(t, srv)
	itInit(t, conn)
	itReadCheckStarted(t, conn)
	itReadCheckVerdicts(t, conn)
	itAnnounceAck(t, conn) // the resume bridge
	return conn, words
}

// itCountApplyVerdicts counts the executor's recorded apply_verdict calls.
func itCountApplyVerdicts(exec *fakeExecutor) int {
	exec.mu.Lock()
	defer exec.mu.Unlock()
	n := 0
	for _, c := range exec.checkCalls {
		if c.tool == "apply_verdict" {
			n++
		}
	}
	return n
}

// TestCheckVoiceVerdict: 「第一个词对了」 marks word 1 through the SAME
// executor path as the screen button (RT-07), echoes the authoritative
// verdicts, and speaks the ack line through the ACK gate; 「第二个词写的是
// 香焦」 carries the actual written form — the verdict grades WRONG via
// GradeWord (one rune off → near miss), the recognized text rides the
// ocr_result display channel, and the teacher line delivers the near-miss
// encouragement (差一个字母很接近了), never a shaming form.
func TestCheckVoiceVerdict(t *testing.T) {
	exec := newFakeExecutorNoRow("it-lesson")
	asr := newFakeASR(
		asrScript{text: "第一个词对了"},
		asrScript{text: "第二个词写的是香焦"},
	)
	llm := newFakeLLM(
		checkIntentScript(intentCheckRight, 1, ""),
		checkIntentScript(intentCheckSpelling, 2, "香焦"),
	)
	conn, _ := itVoiceCheckSession(t, asr, llm, exec)

	// Voice right-verdict: write → echo → ack-gated announcement.
	itSendUtterance(t, conn)
	echo := itReadCheckVerdicts(t, conn)
	if len(echo.Verdicts) != 1 || echo.Verdicts[0].WordIndex != 0 || !echo.Verdicts[0].Correct || !echo.Verdicts[0].Manual {
		t.Fatalf("check_verdicts after voice right = %+v, want word 1 manual correct", echo.Verdicts)
	}
	ack := itAnnounceAck(t, conn)
	if joined := strings.Join(ack, ""); !strings.Contains(joined, "第 1 个词对了") {
		t.Errorf("verdict ack = %v, want the 第 1 个词对了 wording", ack)
	}

	// Voice spelling correction with the actual written form.
	itSendUtterance(t, conn)
	echo = itReadCheckVerdicts(t, conn)
	if len(echo.Verdicts) != 2 {
		t.Fatalf("check_verdicts after spelling = %+v, want 2 entries", echo.Verdicts)
	}
	if v := echo.Verdicts[1]; v.WordIndex != 1 || v.Correct || !v.Manual {
		t.Fatalf("spelling verdict = %+v, want word 2 manual WRONG", v)
	}
	ocrRaw := itRead(t, conn, 10*time.Second, func(m itMsg) bool { return m.typ == TypeOcrResult })
	var ocr OcrResultMsg
	if err := json.Unmarshal(ocrRaw.raw, &ocr); err != nil {
		t.Fatalf("ocr_result decode: %v", err)
	}
	if len(ocr.Entries) != 1 || ocr.Entries[0].WordIndex != 1 || ocr.Entries[0].OcrText != "香焦" || !ocr.Entries[0].NearMiss {
		t.Fatalf("ocr_result = %+v, want word 2 香焦 near-miss entry", ocr.Entries)
	}
	ack = itAnnounceAck(t, conn)
	joined := strings.Join(ack, "")
	if !strings.Contains(joined, "很接近") || !strings.Contains(joined, "香蕉") || strings.Contains(joined, "笨") {
		t.Errorf("spelling feedback = %v, want gentle near-miss encouragement naming the word", ack)
	}

	// The executor row holds both human verdicts; the spelling verdict
	// carries the spoken written form.
	exec.mu.Lock()
	stored := append([]lesson.CheckVerdict(nil), exec.lesson.Verdicts...)
	exec.mu.Unlock()
	if len(stored) != 2 {
		t.Fatalf("stored verdicts = %+v, want 2", stored)
	}
	if !stored[0].Manual || !stored[0].Correct {
		t.Errorf("stored word 1 = %+v, want manual correct", stored[0])
	}
	if !stored[1].Manual || stored[1].Correct || stored[1].OcrText != "香焦" {
		t.Errorf("stored word 2 = %+v, want manual wrong with ocr_text 香焦", stored[1])
	}
}

// TestCheckVoiceSamePathAsButton: the same word flipped by the screen button
// and then by voice — both human inputs ride the executor's ONE ApplyVerdict
// method (RT-07), the button with its client request_id, the voice with the
// server-generated lessonID-verdict-<index>-v<version>-voice key, and the
// final state is the LAST human call.
func TestCheckVoiceSamePathAsButton(t *testing.T) {
	exec := newFakeExecutorNoRow("it-lesson")
	asr := newFakeASR(asrScript{text: "第一个词对了"})
	llm := newFakeLLM(checkIntentScript(intentCheckRight, 1, ""))
	conn, _ := itVoiceCheckSession(t, asr, llm, exec)

	// Button first: the screen flips word 1 to WRONG.
	itSendText(t, conn, CheckVerdictMsg{Type: TypeCheckVerdict, RequestID: "it-btn-0", WordIndex: 0, Correct: false})
	echo := itReadCheckVerdicts(t, conn)
	if len(echo.Verdicts) != 1 || echo.Verdicts[0].Correct {
		t.Fatalf("button echo = %+v, want word 1 wrong", echo.Verdicts)
	}

	// Then voice: the SAME word flipped back to RIGHT — the spoken
	// correction converges on the same write and the echo shows the new
	// authoritative state.
	itSendUtterance(t, conn)
	echo = itReadCheckVerdicts(t, conn)
	if len(echo.Verdicts) != 1 || !echo.Verdicts[0].Correct || !echo.Verdicts[0].Manual {
		t.Fatalf("final echo = %+v, want word 1 manual correct (voice overrode the button)", echo.Verdicts)
	}
	itAnnounceAck(t, conn)

	// Both human inputs rode apply_verdict on the executor — exactly one
	// call each, with the two request-id families.
	exec.mu.Lock()
	buttons, voices := 0, 0
	for _, c := range exec.checkCalls {
		if c.tool != "apply_verdict" {
			continue
		}
		switch {
		case c.requestID == "it-btn-0":
			buttons++
		case strings.Contains(c.requestID, "-verdict-0-v") && strings.HasSuffix(c.requestID, "-voice"):
			voices++
		}
	}
	exec.mu.Unlock()
	if buttons != 1 || voices != 1 {
		t.Fatalf("apply_verdict calls button=%d voice=%d, want 1/1 (same executor path, RT-07)", buttons, voices)
	}
}

// ---------------------------------------------------------------------------
// 13.1-08 cross-connection regression (gap[1]/CR-01 + the same-family
// correction path): two websocket connections share ONE lesson row through
// the closure-captured fakes (conn.go builds one TurnManager per connection
// via h.factory(), while exec/words ride the itServer closure) — exactly the
// multi-tab / reconnect shape 13.1-04 declares supported. The loser of a
// submit race gets the honest terminal signal (session_ended(lesson_complete)
// + one duplicate_submit diagnostic) and NEVER a fabricated review_result or
// a second FSRS write.
// ---------------------------------------------------------------------------

// itServerLogged is the 13.1-08 harness variant: identical to itServer, but
// the per-connection event recorders share one capturing logger so a test
// can assert the server-side rt_event stream (the duplicate-submit
// diagnostics) beside the wire behavior. Two TurnManagers appending to one
// capture is safe — every Emit appends under the captureLogger's mutex.
func itServerLogged(t *testing.T, asr ASRProvider, llm LLMProvider, tts TTSSynthesizer, exec LessonExecutor, words WordSource, log *captureLogger) *httptest.Server {
	t.Helper()
	h, err := NewHandler(
		ServerConfig{
			AllowedOrigins:    []string{itOrigin},
			HeartbeatInterval: 30 * time.Second,
		},
		func() SessionManager {
			return NewTurnManager(asr, llm, tts, exec,
				TurnManagerConfig{LessonID: "it-lesson", Words: words},
				NewEventRecorder(log, "it"), infra.NewNoOpLogger())
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

// itDuplicateMarkers counts the rt_event error entries that carry the
// duplicate_submit code with exactly the given duplicate marker. The code is
// pinned as the literal (not the protocol constant): it is a frozen wire
// identifier whose value 13-13 acceptance greps by — same discipline as
// TestTimingEventVocabulary.
func itDuplicateMarkers(t *testing.T, log *captureLogger, marker string) int {
	t.Helper()
	n := 0
	for _, e := range log.snapshot() {
		if e.msg != "rt_event" {
			continue
		}
		var event, code, dup string
		for _, f := range e.fields {
			switch f.Key {
			case "event":
				event, _ = f.Value.(string)
			case "code":
				code, _ = f.Value.(string)
			case "duplicate":
				dup, _ = f.Value.(string)
			}
		}
		if event == EventError && code == "duplicate_submit" && dup == marker {
			n++
		}
	}
	return n
}

// itCollectUntilSessionEnded reads the connection's remaining stream and
// collects every control type in arrival order until session_ended arrives
// (the ended message is returned, excluded from the collected types). The
// loser-path tests use it to pin down exactly what the losing connection was
// shown BEFORE the terminal signal.
func itCollectUntilSessionEnded(t *testing.T, conn *websocket.Conn, timeout time.Duration) ([]string, SessionEndedMsg) {
	t.Helper()
	var types []string
	deadline := time.Now().Add(timeout)
	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			t.Fatal("stream never reached session_ended")
		}
		_ = conn.SetReadDeadline(time.Now().Add(remaining))
		mt, data, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("itCollectUntilSessionEnded read: %v", err)
		}
		m := itProbe(mt, data)
		if m.typ == TypeSessionEnded {
			var ended SessionEndedMsg
			if err := json.Unmarshal(data, &ended); err != nil {
				t.Fatalf("session_ended decode: %v", err)
			}
			return types, ended
		}
		if m.typ != "" {
			types = append(types, m.typ)
		}
	}
}

// TestCrossConnectionDoubleSubmitSingleReview (13.1-08, gap[1]/CR-01):
// connection A walks the full 3-word dictation into the check stage; while A
// sits in the check panel, connection B dials in and resumes the SAME
// checking row (two TurnManagers, one lesson row). A checks all three words
// and submits first (the winner: review_result → acked summary →
// session_ended). B then submits with its own request id — the executor
// answers a latch replay (Idempotent=true, EMPTY verdict readback) and B
// must get the honest terminal signal: exactly ONE SubmitReview total, zero
// review_result on B, exactly one session_ended(lesson_complete), and one
// duplicate_submit/check_submit_latch diagnostic on the event stream.
func TestCrossConnectionDoubleSubmitSingleReview(t *testing.T) {
	cards := ttCards("苹果", "香蕉", "橘子")
	words := newFakeWords(cards)
	words.reviewResult = ReviewWriteResult{
		Requested: []string{"苹果", "香蕉", "橘子"},
		Updated:   []string{"苹果", "香蕉", "橘子"},
	}
	exec := newFakeExecutorNoRow("it-lesson")
	log := &captureLogger{}
	asr := newFakeASR(asrScript{text: "好了"}, asrScript{text: "好了"}, asrScript{text: "好了"})
	llm := newFakeLLM(
		intentScript(intentConfirm, ""),
		intentScript(intentConfirm, ""),
		intentScript(intentConfirm, ""),
	)
	tts := newFakeTTS(2, 320)
	tts.delay = 20 * time.Millisecond
	srv := itServerLogged(t, asr, llm, tts, exec, words, log)

	// Connection A: the full dictation into the check stage.
	connA := itDial(t, srv)
	itInit(t, connA)
	itAnnounceAck(t, connA) // the opening greeting
	for i := 0; i < 3; i++ {
		itSendUtterance(t, connA)
		itRead(t, connA, 15*time.Second, func(m itMsg) bool { return m.typ == TypeLessonUpdate })
		if i < 2 {
			itAnnounceAck(t, connA) // next-word announcement
		}
	}
	started := itReadCheckStarted(t, connA)
	if started.Reason != CheckStartedReasonDictationDone {
		t.Errorf("A check_started.reason = %q, want %s", started.Reason, CheckStartedReasonDictationDone)
	}
	itAnnounceAck(t, connA) // the check bridge
	for i := 0; i < 3; i++ {
		itSendText(t, connA, CheckVerdictMsg{
			Type: TypeCheckVerdict, RequestID: fmt.Sprintf("it-v-%d", i), WordIndex: i, Correct: true,
		})
		itReadCheckVerdicts(t, connA)
	}

	// Connection B: dials into the SAME checking row — the resume path
	// restores the check panel from the row (3 stored manual verdicts).
	connB := itDial(t, srv)
	itInit(t, connB)
	startedB := itReadCheckStarted(t, connB)
	if startedB.Reason != CheckStartedReasonResume {
		t.Errorf("B check_started.reason = %q, want %s", startedB.Reason, CheckStartedReasonResume)
	}
	echoB := itReadCheckVerdicts(t, connB)
	if len(echoB.Verdicts) != 3 {
		t.Fatalf("B resync verdicts = %+v, want A's 3 stored manual verdicts", echoB.Verdicts)
	}
	itAnnounceAck(t, connB) // B's resume bridge

	// A submits first and wins the latch: honest result, acked ending.
	itSendText(t, connA, CheckSubmitMsg{Type: TypeCheckSubmit, RequestID: "it-submit-a"})
	m := itRead(t, connA, 15*time.Second, func(m itMsg) bool { return m.typ == TypeReviewResult })
	var result ReviewResultMsg
	if err := json.Unmarshal(m.raw, &result); err != nil {
		t.Fatalf("A review_result decode: %v", err)
	}
	if result.Requested != 3 || result.Updated != 3 || result.Unchecked {
		t.Errorf("A review_result = %+v, want 3/3 checked submit", result)
	}
	itAckEnding(t, connA, ReasonLessonComplete)
	if got := len(words.submissions()); got != 1 {
		t.Fatalf("SubmitReview calls after A = %d, want exactly 1", got)
	}

	// B submits second — the cross-connection loser.
	itSendText(t, connB, CheckSubmitMsg{Type: TypeCheckSubmit, RequestID: "it-submit-b"})
	loserTypes, ended := itCollectUntilSessionEnded(t, connB, 15*time.Second)
	for _, typ := range loserTypes {
		if typ == TypeReviewResult || typ == TypeCheckStarted {
			t.Fatalf("loser B saw %s before session_ended (collected %v) — a fabricated result/stage broadcast", typ, loserTypes)
		}
	}
	if ended.Reason != ReasonLessonComplete {
		t.Errorf("loser B session_ended.reason = %q, want %s", ended.Reason, ReasonLessonComplete)
	}
	// Nothing trails the terminal signal: no late review_result, no second
	// session_ended.
	for _, m := range itDrainUntilTimeout(t, connB, 800*time.Millisecond) {
		if m.typ == TypeReviewResult {
			t.Fatalf("loser B received a post-terminal review_result (fabricated)")
		}
		if m.typ == TypeSessionEnded {
			t.Fatalf("loser B received a second session_ended")
		}
	}

	// Exactly one FSRS write for the whole cross-connection race.
	subs := words.submissions()
	if len(subs) != 1 {
		t.Fatalf("SubmitReview calls = %d, want exactly 1 (the winner's)", len(subs))
	}
	if subs[0]["苹果"] != "good" || subs[0]["橘子"] != "good" {
		t.Errorf("ratings = %v, want the winner's D-02 batch", subs[0])
	}
	// The audit trail: exactly one duplicate_submit diagnostic with the
	// check_submit_latch marker, exactly one review_submitted (the winner).
	if got := itDuplicateMarkers(t, log, "check_submit_latch"); got != 1 {
		t.Errorf("duplicate_submit/check_submit_latch events = %d, want exactly 1", got)
	}
	if got := ttEventCount(t, log, EventReviewSubmitted); got != 1 {
		t.Errorf("review_submitted events = %d, want exactly 1", got)
	}
	exec.mu.Lock()
	stage := exec.lesson.Stage
	exec.mu.Unlock()
	if stage != lesson.StageSubmitted {
		t.Errorf("row stage = %q, want submitted", stage)
	}
}

// TestCrossConnectionCorrectionStraggler (13.1-08, gap[1] same family — the
// full build-out the review asked for): a submitted+unchecked row (the D-03
// fallback state) reopened for correction by TWO connections in sequence.
// A and B each dial in, enter the correction panel, and hand-confirm both
// words. A submits first: the one-word diff batch (香蕉 overridden to
// correct) runs SubmitReview and its Clean run clears the unchecked marker
// (the review_result only ships after ClearUnchecked, check.go write order).
// B — the straggler — then submits: the row reads unchecked=false, so the
// read-side guard fires and B gets the terminal signal with ZERO second
// diff write, zero review_result, one correction_submit_latch diagnostic.
func TestCrossConnectionCorrectionStraggler(t *testing.T) {
	words := newFakeWords(itCards)
	words.reviewResult = ReviewWriteResult{Requested: []string{"香蕉"}, Updated: []string{"香蕉"}}
	exec := newFakeExecutorNoRow("it-lesson")
	// Seed exactly what uncheckedCloseFallback persists: outcomes stored,
	// stage submitted, unchecked=1, verdicts empty.
	ctx := context.Background()
	l, err := exec.EnsureLessonCards(ctx, "it-lesson", itCards, lesson.CategoryEnglish)
	if err != nil {
		t.Fatalf("EnsureLessonCards: %v", err)
	}
	version := l.Version
	for i := range itCards {
		res, aerr := exec.AdvanceWord(ctx, "it-lesson", version, fmt.Sprintf("it-seed-adv-%d", i))
		if aerr != nil {
			t.Fatalf("AdvanceWord seed %d: %v", i, aerr)
		}
		version = res.NewVersion
	}
	ec, err := exec.EnterCheck(ctx, "it-lesson", version, "it-lesson-enter-check", map[string]lesson.WordOutcome{
		"0": {Outcome: "confirmed", Hints: 0},
		"1": {Outcome: "confirmed", Hints: 1},
	})
	if err != nil {
		t.Fatalf("EnterCheck: %v", err)
	}
	if _, err := exec.CompleteCheck(ctx, "it-lesson", ec.NewVersion, "it-lesson-complete-check", true); err != nil {
		t.Fatalf("CompleteCheck seed: %v", err)
	}
	log := &captureLogger{}
	tts := newFakeTTS(3, 320)
	tts.delay = 10 * time.Millisecond
	srv := itServerLogged(t, newFakeASR(), newFakeLLM(), tts, exec, words, log)

	// Both connections reopen the correction panel over the shared row.
	connA := itDial(t, srv)
	itInit(t, connA)
	startedA := itReadCheckStarted(t, connA)
	if startedA.Reason != CheckStartedReasonCorrection {
		t.Fatalf("A check_started.reason = %q, want %s", startedA.Reason, CheckStartedReasonCorrection)
	}
	itAnnounceAck(t, connA) // the correction bridge
	// A confirms both words; 香蕉 is OVERRIDDEN to correct — the one-word diff.
	itSendText(t, connA, CheckVerdictMsg{Type: TypeCheckVerdict, RequestID: "it-cor-a-v0", WordIndex: 0, Correct: true})
	itReadCheckVerdicts(t, connA)
	itSendText(t, connA, CheckVerdictMsg{Type: TypeCheckVerdict, RequestID: "it-cor-a-v1", WordIndex: 1, Correct: true})
	itReadCheckVerdicts(t, connA)

	connB := itDial(t, srv)
	itInit(t, connB)
	startedB := itReadCheckStarted(t, connB)
	if startedB.Reason != CheckStartedReasonCorrection {
		t.Fatalf("B check_started.reason = %q, want %s (the row is still unchecked)", startedB.Reason, CheckStartedReasonCorrection)
	}
	itAnnounceAck(t, connB) // B's own correction bridge
	itSendText(t, connB, CheckVerdictMsg{Type: TypeCheckVerdict, RequestID: "it-cor-b-v0", WordIndex: 0, Correct: true})
	itReadCheckVerdicts(t, connB)
	itSendText(t, connB, CheckVerdictMsg{Type: TypeCheckVerdict, RequestID: "it-cor-b-v1", WordIndex: 1, Correct: true})
	itReadCheckVerdicts(t, connB)

	// A submits first: the one-word diff batch runs and the marker clears
	// (observing review_result proves ClearUnchecked already ran — the
	// message only ships after it, check.go write order).
	itSendText(t, connA, CheckSubmitMsg{Type: TypeCheckSubmit, RequestID: "it-cor-a"})
	m := itRead(t, connA, 15*time.Second, func(m itMsg) bool { return m.typ == TypeReviewResult })
	var result ReviewResultMsg
	if err := json.Unmarshal(m.raw, &result); err != nil {
		t.Fatalf("A review_result decode: %v", err)
	}
	if result.Requested != 1 || result.Updated != 1 || result.Unchecked {
		t.Errorf("A review_result = %+v, want the 1-word correction batch", result)
	}
	itAckEnding(t, connA, ReasonLessonComplete)
	if got := len(words.submissions()); got != 1 {
		t.Fatalf("SubmitReview calls after A = %d, want exactly 1 (the diff batch)", got)
	}

	// B — the straggler — submits into a now-cleared row.
	itSendText(t, connB, CheckSubmitMsg{Type: TypeCheckSubmit, RequestID: "it-cor-b"})
	loserTypes, ended := itCollectUntilSessionEnded(t, connB, 15*time.Second)
	for _, typ := range loserTypes {
		if typ == TypeReviewResult || typ == TypeCheckStarted {
			t.Fatalf("straggler B saw %s before session_ended (collected %v)", typ, loserTypes)
		}
	}
	if ended.Reason != ReasonLessonComplete {
		t.Errorf("straggler B session_ended.reason = %q, want %s", ended.Reason, ReasonLessonComplete)
	}
	for _, m := range itDrainUntilTimeout(t, connB, 800*time.Millisecond) {
		if m.typ == TypeReviewResult || m.typ == TypeSessionEnded {
			t.Fatalf("straggler B received a post-terminal %s", m.typ)
		}
	}

	// Zero second diff write: the straggler never reaches SubmitReview.
	subs := words.submissions()
	if len(subs) != 1 {
		t.Fatalf("SubmitReview calls = %d, want exactly 1 (A's diff)", len(subs))
	}
	if len(subs[0]) != 1 || subs[0]["香蕉"] != "hard" {
		t.Errorf("correction batch = %v, want only 香蕉=hard", subs[0])
	}
	if got := itDuplicateMarkers(t, log, "correction_submit_latch"); got != 1 {
		t.Errorf("duplicate_submit/correction_submit_latch events = %d, want exactly 1", got)
	}
	if got := ttEventCount(t, log, EventReviewCorrection); got != 1 {
		t.Errorf("review_correction events = %d, want exactly 1", got)
	}
	exec.mu.Lock()
	stage, unchecked := exec.lesson.Stage, exec.lesson.Unchecked
	exec.mu.Unlock()
	if stage != lesson.StageSubmitted || unchecked {
		t.Errorf("row = stage %q unchecked %v, want submitted/false", stage, unchecked)
	}
}

// TestCheckVoiceInvalidIndex: the model's index/text payload is untrusted —
// a missing index, an out-of-range index, and a spelling line without the
// actual text ALL fall back to the clarification question with ZERO verdict
// writes and no crash; the classroom stays live afterwards.
func TestCheckVoiceInvalidIndex(t *testing.T) {
	exec := newFakeExecutorNoRow("it-lesson")
	asr := newFakeASR(
		asrScript{text: "那个词对了"},   // check_right with NO index
		asrScript{text: "第五个词错了"},  // check_wrong index 5 (out of range)
		asrScript{text: "第一个词写的是"}, // check_spelling with NO text
	)
	llm := newFakeLLM(
		checkIntentScript(intentCheckRight, 0, ""),
		checkIntentScript(intentCheckWrong, 5, ""),
		checkIntentScript(intentCheckSpelling, 1, ""),
	)
	conn, _ := itVoiceCheckSession(t, asr, llm, exec)

	for i := 0; i < 3; i++ {
		itSendUtterance(t, conn)
		ack := itAnnounceAck(t, conn)
		if joined := strings.Join(ack, ""); !strings.Contains(joined, "你说的是第几个词") {
			t.Fatalf("invalid payload %d announcement = %v, want the clarification fallback", i+1, ack)
		}
		if got := itCountApplyVerdicts(exec); got != 0 {
			t.Fatalf("invalid payload %d produced %d apply_verdict writes, want 0 (no guessing)", i+1, got)
		}
	}

	// The classroom survived: the screen path still applies verdicts after
	// the clarification rounds (the FSM settled back to listening).
	itSendText(t, conn, CheckVerdictMsg{Type: TypeCheckVerdict, RequestID: "it-after-invalid", WordIndex: 0, Correct: true})
	echo := itReadCheckVerdicts(t, conn)
	if len(echo.Verdicts) != 1 || !echo.Verdicts[0].Correct {
		t.Fatalf("post-clarification echo = %+v, want the button verdict applied", echo.Verdicts)
	}
}
