// dictation_intent_test.go — Issue #56: the dictation turn's task-context
// snapshot and the intent_classified evidence correlation. The full-phrase
// confirmation correction's own table tests land with the resolver (step 3);
// this file first pins WHAT the classifier may assume about the classroom
// (captured before the processing flip, retained through the turn) and WHAT
// the event stream may say about one turn (labels + count-class fields,
// never transcript or word text).
package rt

import (
	"context"
	"testing"
	"time"

	"github.com/xiaotian/home-tutor-agent/pkg/infra"
	"github.com/xiaotian/home-tutor-agent/pkg/lesson"
)

// TestDictationIntentContext: the turn context is captured under the lock at
// turn START — an ordinary word classroom waiting on the current word
// snapshots awaitingWord with the ORIGINAL target (index/version/word), the
// pause bit and the stage flags flip it off, and the snapshot is still
// intact after the turn has entered processing (the waiting context does not
// evaporate mid-turn). A cursor past the end (an EnterCheck write failure
// leaves index=len without the checking bit) must yield the no-target marker
// and never panic.
func TestDictationIntentContext(t *testing.T) {
	cards := ttCards("苹果", "香蕉", "橘子")
	asr := newFakeASR(asrScript{text: "老师我有个问题", delay: 300 * time.Millisecond})
	llm := newFakeLLM(intentScript(intentChat, "你说呀"))
	tm, s, _, _ := ttIntentFixture(t, cards, asr, llm)

	capture := func() dictationTurnContext {
		t.Helper()
		tm.mu.Lock()
		defer tm.mu.Unlock()
		return tm.captureDictationTurnContextLocked(1, 0, false, tm.tutor.turnTarget())
	}

	// Pre-turn: waiting on word 1 of 3 at version 1.
	before := capture()
	if !before.awaitingWord {
		t.Error("awaitingWord = false for a live word classroom waiting on the current word")
	}
	if before.paused || before.checking {
		t.Errorf("paused/checking = %v/%v, want false/false", before.paused, before.checking)
	}
	if before.target.WordIndex != 0 || before.target.ExpectedVersion != 1 || before.target.Word != "苹果" {
		t.Errorf("target = %+v, want word 0 苹果 at v1", before.target)
	}

	// The snapshot survives into processing: a mid-turn capture (the same
	// hold processTurn used) still sees the waiting context and the same
	// target. The ASR delay holds the turn in processing long enough to
	// observe; the reply announcement then settles through its ack.
	prevSent := s.countOf(TypeTeacherSentence)
	done := make(chan struct{})
	go func() {
		tm.processTurn(s, []byte("pcm"))
		close(done)
	}()
	if !waitFor(t, 5*time.Second, func() bool {
		tm.mu.Lock()
		defer tm.mu.Unlock()
		return tm.state == StateProcessing
	}) {
		t.Fatal("turn never entered processing")
	}
	mid := capture()
	if !mid.awaitingWord || mid.target != before.target {
		t.Errorf("mid-processing context = %+v, want the retained waiting context %+v", mid, before)
	}
	if !waitFor(t, 5*time.Second, func() bool { return s.countOf(TypeTeacherSentence) > prevSent }) {
		t.Fatal("chat reply announcement never spoke")
	}
	ttAckLast(t, tm, s, 0)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("chat turn never settled")
	}

	// Stage flags flip awaitingWord off: paused, checking, a 背默 task, a
	// completed lesson, and an out-of-range target all disqualify.
	tm.mu.Lock()
	tm.tutor.paused = true
	paused := tm.captureDictationTurnContextLocked(2, 0, false, tm.tutor.turnTarget())
	tm.tutor.paused = false
	tm.tutor.checking = true
	checking := tm.captureDictationTurnContextLocked(3, 0, true, TurnTarget{WordIndex: -1})
	tm.tutor.checking = false
	tm.tutor.poem = &poemRef{ID: 1}
	recite := tm.captureDictationTurnContextLocked(4, 0, false, tm.tutor.turnTarget())
	tm.tutor.poem = nil
	tm.tutor.completed = true
	completed := tm.captureDictationTurnContextLocked(5, 0, false, TurnTarget{WordIndex: -1})
	tm.tutor.completed = false
	// Cursor parked at len(words) without the checking bit: the caller-side
	// target capture bounds-checks, so the snapshot only ever sees the
	// no-target marker — and awaitingWord is off.
	tm.tutor.align(len(cards), 4)
	pastEnd := tm.captureDictationTurnContextLocked(6, 0, false, TurnTarget{WordIndex: -1})
	tm.mu.Unlock()

	for name, snap := range map[string]dictationTurnContext{
		"paused":     paused,
		"checking":   checking,
		"recitation": recite,
		"completed":  completed,
		"past-end":   pastEnd,
	} {
		if snap.awaitingWord {
			t.Errorf("awaitingWord = true for the %s classroom — the correction gate must stay shut", name)
		}
	}
	if !paused.paused || !checking.checking {
		t.Errorf("snapshot flags: paused=%v checking=%v, want both true", paused.paused, checking.checking)
	}
}

// TestDictationIntentEvidence: the intent_classified event correlates ONE
// turn — turn_id equals the turn_start the client saw, word_index/version
// equal the captured target — while its fields stay inside the T-13-06b
// desensitization boundary: no transcript text, no word text, no audio.
func TestDictationIntentEvidence(t *testing.T) {
	cards := ttCards("苹果", "香蕉")
	asr := newFakeASR(asrScript{text: "老师我有个问题"})
	llm := newFakeLLM(intentScript(intentChat, "你说呀"))
	tm, s, log, _ := ttIntentFixture(t, cards, asr, llm)

	prev := s.countOf(TypeTeacherSentence)
	ttRunIntentTurn(t, tm, s, prev)

	// The turn identity: the last turn_start this connection saw.
	starts := s.controlsOf(t, TypeTurnStart)
	if len(starts) == 0 {
		t.Fatal("no turn_start captured — evidence cannot correlate the turn")
	}
	wantTurn, _ := starts[len(starts)-1]["turn_id"].(float64)

	var event *captureEntry
	for i, e := range log.snapshot() {
		if e.msg != "rt_event" {
			continue
		}
		for _, f := range e.fields {
			if f.Key == "event" && f.Value == EventIntentClassified {
				event = &log.snapshot()[i]
				break
			}
		}
	}
	if event == nil {
		t.Fatal("no intent_classified event captured")
	}
	field := func(key string) (any, bool) {
		for _, f := range event.fields {
			if f.Key == key {
				return f.Value, true
			}
		}
		return nil, false
	}
	if got, ok := field("intent"); !ok || got != intentChat {
		t.Errorf("intent field = %v (present %v), want chat", got, ok)
	}
	if got, ok := field("model_intent"); !ok || got != intentChat {
		t.Errorf("model_intent field = %v (present %v), want chat (parsed verdict)", got, ok)
	}
	if got, ok := field("turn_id"); !ok || got != uint64(wantTurn) {
		t.Errorf("turn_id field = %v (present %v), want the turn_start turn_id %.0f", got, ok, wantTurn)
	}
	if got, ok := field("word_index"); !ok || got != 0 {
		t.Errorf("word_index field = %v (present %v), want 0 (the captured target)", got, ok)
	}
	if got, ok := field("version"); !ok || got != 1 {
		t.Errorf("version field = %v (present %v), want 1 (the captured target's version)", got, ok)
	}

	// Desensitization: no field value carries the transcript, the word
	// text, or anything audio-shaped.
	for _, f := range event.fields {
		v, isString := f.Value.(string)
		if !isString {
			continue
		}
		if contains(v, "老师我有个问题") || contains(v, "苹果") || contains(v, "香蕉") {
			t.Errorf("field %q leaked text %q — T-13-06b boundary violated", f.Key, v)
		}
	}
}

// ---------------------------------------------------------------------------
// Issue #56 step 3: the state-gated full-phrase correction.
// ---------------------------------------------------------------------------

// TestResolveDictationIntent: the pure resolver's whole-phrase table —
// completion phrases correct to confirm with the reply cleared; explicit
// not-done / waiting / re-read phrases outrank a model confirm; edge
// normalization strips statement punctuation and space only (questions,
// negations, and compound utterances never hit); everything else returns
// the model's verdict untouched.
func TestResolveDictationIntent(t *testing.T) {
	const junk = "模型生成的回复"
	cases := []struct {
		name        string
		transcript  string
		modelIntent string
		wantIntent  string
		wantReply   string
	}{
		// The three acceptance phrases and the wider completion family —
		// whatever the model made of them.
		{"好了 misjudged repeat", "好了", intentRepeat, intentConfirm, ""},
		{"好嘞 misjudged chat", "好嘞", intentChat, intentConfirm, ""},
		{"下一个 misjudged repeat", "下一个", intentRepeat, intentConfirm, ""},
		{"下一个词 misjudged chat", "下一个词", intentChat, intentConfirm, ""},
		{"写好了 misjudged hint", "写好了", intentHint, intentConfirm, ""},
		{"写完了 correct already", "写完了", intentConfirm, intentConfirm, ""},
		{"可以了 with junk reply", "可以了", intentConfirm, intentConfirm, ""},
		// Statement punctuation and surrounding space are edge-normalized.
		{"好了。", "好了。", intentChat, intentConfirm, ""},
		{" 好嘞，", " 好嘞，", intentChat, intentConfirm, ""},
		{"下一个！", "下一个！", intentChat, intentConfirm, ""},
		// Explicit not-done: a model confirm must not advance.
		{"还没好 misjudged confirm", "还没好", intentConfirm, intentChat, notDoneAckText()},
		{"还没写好 misjudged confirm", "还没写好", intentConfirm, intentChat, notDoneAckText()},
		{"没写完。 misjudged confirm", "没写完。", intentConfirm, intentChat, notDoneAckText()},
		{"不要下一个 misjudged confirm", "不要下一个", intentConfirm, intentChat, notDoneAckText()},
		// Explicit waiting → the existing pause handling.
		{"等一下 misjudged confirm", "等一下", intentConfirm, intentPause, ""},
		// Explicit re-read outranks an affirmative reading.
		{"再读一遍 misjudged confirm", "再读一遍", intentConfirm, intentRepeat, ""},
		// Questions keep their mark and never hit the table.
		{"好了？ stays model", "好了？", intentChat, intentChat, junk},
		{"好了? stays model", "好了?", intentChat, intentChat, junk},
		// Compound utterances keep interior structure — never substring
		// matching, never an affirmative-word sweep.
		{"好了，再读一遍 stays model", "好了，再读一遍", intentRepeat, intentRepeat, junk},
		{"好了再叫你 stays model", "好了再叫你", intentChat, intentChat, junk},
		{"我还没好呢 stays model", "我还没好呢", intentChat, intentChat, junk},
		{"老师再读一遍这个词 stays model", "老师再读一遍这个词", intentRepeat, intentRepeat, junk},
		{"下一个词是什么 stays model", "下一个词是什么", intentMeaning, intentMeaning, junk},
		// Non-hit expressions keep the model verdict (and its reply).
		{"plain chat", "老师你好", intentChat, intentChat, junk},
		{"empty-ish", " ", intentChat, intentChat, junk},
	}
	for _, tc := range cases {
		gotIntent, gotReply := resolveDictationIntent(tc.transcript, tc.modelIntent, junk)
		if gotIntent != tc.wantIntent || gotReply != tc.wantReply {
			t.Errorf("%s: resolveDictationIntent(%q, %s) = (%q, %q), want (%q, %q)",
				tc.name, tc.transcript, tc.modelIntent, gotIntent, gotReply, tc.wantIntent, tc.wantReply)
		}
	}
}

// TestDictationConfirmationUtterances: the correction through the REAL
// processTurn in a live word classroom — the model's misjudgement of the
// acceptance phrases cannot swallow the advance (and the stale reply is
// cleared), while a model confirm on explicit not-done / waiting / re-read
// phrases never advances. A collapsed classification keeps the spoken
// fallback EXCEPT an exact table phrase, which advances on the transcript
// alone (PR #61 review round 2).
func TestDictationConfirmationUtterances(t *testing.T) {
	confirmDrive := func(t *testing.T, phrase, modelIntent, modelReply string) (*fakeExecutor, *TurnManager, *captureSender, *captureLogger, int) {
		t.Helper()
		cards := ttCards("苹果", "香蕉", "橘子")
		asr := newFakeASR(asrScript{text: phrase})
		llm := newFakeLLM(intentScript(modelIntent, modelReply))
		tm, s, log, exec := ttIntentFixture(t, cards, asr, llm)
		prev := s.countOf(TypeTeacherSentence)
		ttRunIntentTurn(t, tm, s, prev)
		return exec, tm, s, log, prev
	}

	t.Run("completion phrases advance one word whatever the model said", func(t *testing.T) {
		for _, tc := range []struct{ phrase, modelIntent string }{
			{"好了", intentRepeat}, {"好嘞", intentChat}, {"下一个", intentRepeat},
		} {
			exec, tm, s, log, prev := confirmDrive(t, tc.phrase, tc.modelIntent, "哎呀我再给你读一遍苹果")
			if got := exec.advanceCount(); got != 1 {
				t.Errorf("%q: advance calls = %d, want 1 (exactly one word)", tc.phrase, got)
			}
			if calls := exec.advanceCallsSnapshot(); len(calls) == 1 && calls[0].requestID != "L1-advance-0" {
				t.Errorf("%q: request id = %q, want L1-advance-0 (the captured target)", tc.phrase, calls[0].requestID)
			}
			joined := ttSentencesSince(t, s, prev)
			if !contains(joined, "第 2 个词") {
				t.Errorf("%q: announcement = %q, want the next-word position", tc.phrase, joined)
			}
			if contains(joined, "哎呀我再给你读一遍") {
				t.Errorf("%q: corrected confirm must clear the stale model reply: %q", tc.phrase, joined)
			}
			if spoken := tm.tts.(*fakeTTS).recordedTexts(); !contains(spoken[len(spoken)-1], "香蕉") {
				t.Errorf("%q: tts must name the next word, got %v", tc.phrase, spoken)
			}
			// The evidence stream shows the correction: model verdict beside
			// the final intent, on the same turn.
			intent, model, _ := lastIntentClassified(t, log)
			if intent != intentConfirm || model != tc.modelIntent {
				t.Errorf("%q: intent_classified = (%q, %q), want final confirm over model %q",
					tc.phrase, intent, model, tc.modelIntent)
			}
		}
	})

	t.Run("explicit not-done never advances despite model confirm", func(t *testing.T) {
		for _, phrase := range []string{"还没好", "还没写好", "没写完", "不要下一个"} {
			exec, _, s, log, prev := confirmDrive(t, phrase, intentConfirm, "")
			if got := exec.advanceCount(); got != 0 {
				t.Errorf("%q: advance calls = %d, want 0", phrase, got)
			}
			if joined := ttSentencesSince(t, s, prev); !contains(joined, "不着急，慢慢写") {
				t.Errorf("%q: reply = %q, want the gentle not-done ack", phrase, joined)
			}
			intent, model, _ := lastIntentClassified(t, log)
			if intent != intentChat || model != intentConfirm {
				t.Errorf("%q: intent_classified = (%q, %q), want final chat over model confirm", phrase, intent, model)
			}
		}
	})

	t.Run("等一下 pauses instead of advancing despite model confirm", func(t *testing.T) {
		exec, _, s, log, prev := confirmDrive(t, "等一下", intentConfirm, "")
		if got := exec.advanceCount(); got != 0 {
			t.Errorf("advance calls = %d, want 0", got)
		}
		if joined := ttSentencesSince(t, s, prev); !contains(joined, "先休息") {
			t.Errorf("reply = %q, want the pause ack", joined)
		}
		intent, model, _ := lastIntentClassified(t, log)
		if intent != intentPause || model != intentConfirm {
			t.Errorf("intent_classified = (%q, %q), want final pause over model confirm", intent, model)
		}
	})

	t.Run("再读一遍 re-reads the current word despite model confirm", func(t *testing.T) {
		exec, tm, _, log, _ := confirmDrive(t, "再读一遍", intentConfirm, "")
		if got := exec.advanceCount(); got != 0 {
			t.Errorf("advance calls = %d, want 0", got)
		}
		if spoken := tm.tts.(*fakeTTS).recordedTexts(); !contains(spoken[len(spoken)-1], "苹果") {
			t.Errorf("tts = %v, want the CURRENT word re-read", spoken)
		}
		intent, model, _ := lastIntentClassified(t, log)
		if intent != intentRepeat || model != intentConfirm {
			t.Errorf("intent_classified = (%q, %q), want final repeat over model confirm", intent, model)
		}
	})

	// PR #61 review round 2: the correction keys on the transcript, so two
	// garbage model hops cannot swallow an exact completion phrase — issue
	// #56's "明确完成表达能推进" holds with NO model verdict at all.
	t.Run("collapsed classification still advances an exact completion phrase", func(t *testing.T) {
		cards := ttCards("苹果", "香蕉")
		garbage := llmScript{resp: wireChatResponse(t, `{"choices":[{"message":{"content":"不是JSON"}}]}`)}
		asr := newFakeASR(asrScript{text: "好了"})
		llm := newFakeLLM(garbage, garbage)
		tm, s, log, exec := ttIntentFixture(t, cards, asr, llm)
		prev := s.countOf(TypeTeacherSentence)
		ttRunIntentTurn(t, tm, s, prev)
		if got := exec.advanceCount(); got != 1 {
			t.Errorf("advance calls = %d, want 1 (an exact completion phrase advances on the transcript alone)", got)
		}
		if spoken := tm.tts.(*fakeTTS).recordedTexts(); !contains(spoken[len(spoken)-1], "香蕉") {
			t.Errorf("tts = %v, want the next word narrated", spoken)
		}
		intent, model, _ := lastIntentClassified(t, log)
		if intent != intentConfirm || model != "" {
			t.Errorf("intent_classified = (%q, %q), want confirm with no model verdict", intent, model)
		}
	})

	t.Run("collapsed classification without a table phrase keeps the spoken fallback", func(t *testing.T) {
		cards := ttCards("苹果", "香蕉")
		garbage := llmScript{resp: wireChatResponse(t, `{"choices":[{"message":{"content":"不是JSON"}}]}`)}
		asr := newFakeASR(asrScript{text: "我想吃苹果"})
		llm := newFakeLLM(garbage, garbage)
		tm, s, log, exec := ttIntentFixture(t, cards, asr, llm)
		prev := s.countOf(TypeTeacherSentence)
		ttRunIntentTurn(t, tm, s, prev)
		if got := exec.advanceCount(); got != 0 {
			t.Errorf("advance calls = %d, want 0 (no table phrase, no advance)", got)
		}
		if joined := ttSentencesSince(t, s, prev); !contains(joined, "老师没听清") {
			t.Errorf("reply = %q, want the fixed fallback", joined)
		}
		intent, model, _ := lastIntentClassified(t, log)
		if intent != intentChat || model != "" {
			t.Errorf("intent_classified = (%q, %q), want the chat fallback with no model verdict", intent, model)
		}
	})
}

// lastIntentClassified extracts the newest intent_classified event's final
// intent, model intent, and full field list.
func lastIntentClassified(t *testing.T, log *captureLogger) (intent, model string, fields []infra.Field) {
	t.Helper()
	var found *captureEntry
	entries := log.snapshot()
	for i := range entries {
		if entries[i].msg != "rt_event" {
			continue
		}
		for _, f := range entries[i].fields {
			if f.Key == "event" && f.Value == EventIntentClassified {
				found = &entries[i]
			}
		}
	}
	if found == nil {
		t.Fatal("no intent_classified event captured")
	}
	for _, f := range found.fields {
		switch f.Key {
		case "intent":
			intent, _ = f.Value.(string)
		case "model_intent":
			model, _ = f.Value.(string)
		}
	}
	return intent, model, found.fields
}

// ---------------------------------------------------------------------------
// Issue #56 step 4: state boundaries and the stale-turn races around the
// corrected confirmation.
// ---------------------------------------------------------------------------

// TestDictationConfirmationStateGuard: the completion-phrase correction (and
// confirm dispatch generally) only ever fires inside a live word-dictation
// writing stage — a paused classroom, a parked subject clarify, the check
// stage, and a closed session all leave a spoken 「好了」 with zero advances.
func TestDictationConfirmationStateGuard(t *testing.T) {
	t.Run("paused classroom rejects the phrase without advancing", func(t *testing.T) {
		cards := ttCards("苹果", "香蕉")
		asr := newFakeASR(asrScript{text: "休息一下"}, asrScript{text: "好了"})
		llm := newFakeLLM(intentScript(intentPause, ""), intentScript(intentConfirm, ""))
		tm, s, _, exec := ttIntentFixture(t, cards, asr, llm)
		ttRunIntentTurn(t, tm, s, 3) // pause
		ttRunIntentTurn(t, tm, s, 4) // 「好了」 while paused
		if got := exec.advanceCount(); got != 0 {
			t.Errorf("advance calls = %d, want 0 (paused confirm never advances)", got)
		}
		if got := ttSentencesSince(t, s, 4); got != confirmPausedRejectText() {
			t.Errorf("paused reply = %q, want the gentle rejection", got)
		}
	})

	t.Run("subject clarify answers never touch the lesson classifier", func(t *testing.T) {
		cards := ttCards("苹果", "香蕉")
		asr := newFakeASR(asrScript{text: "好了"})
		llm := newFakeLLM(intentScript(intentConfirm, ""))
		tm, s, _, exec := ttIntentFixture(t, cards, asr, llm)
		// Park the clarify exactly like startSubjectAsk: no tutor, ask live.
		tm.mu.Lock()
		initMsg := SessionInitMsg{Type: TypeSessionInit, Audio: AudioSpec{SampleRate: 16000, Format: "s16le", Channels: 1}}
		tm.pendingAsk = &initMsg
		tm.tutor = nil
		tm.mu.Unlock()

		prev := s.countOf(TypeTeacherSentence)
		done := make(chan struct{})
		go func() {
			tm.processTurn(s, []byte("pcm"))
			close(done)
		}()
		if !waitFor(t, 5*time.Second, func() bool { return s.countOf(TypeTeacherSentence) > prev }) {
			t.Fatal("the clarify retry never spoke")
		}
		ttAckLast(t, tm, s, 0)
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("clarify answer turn never settled")
		}
		if got := llm.callCount(); got != 0 {
			t.Errorf("llm calls = %d, want 0 (the clarify binds by keyword, never a classification)", got)
		}
		if got := exec.advanceCount(); got != 0 {
			t.Errorf("advance calls = %d, want 0", got)
		}
		if got := ttSentencesSince(t, s, prev); got != subjectAskRetryText {
			t.Errorf("clarify retry = %q, want %q", got, subjectAskRetryText)
		}
		tm.mu.Lock()
		stillAsking := tm.pendingAsk != nil
		tm.mu.Unlock()
		if !stillAsking {
			t.Error("「好了」 consumed the pending clarify — it binds nothing")
		}
	})

	t.Run("check stage classifies through its own prompt and never advances", func(t *testing.T) {
		cards := ttCards("苹果", "香蕉")
		asr := newFakeASR(asrScript{text: "好了"})
		llm := newFakeLLM(intentScript(intentConfirm, ""))
		tm, s, _, exec := ttIntentFixture(t, cards, asr, llm)
		tm.mu.Lock()
		tm.tutor.checking = true
		tm.mu.Unlock()

		prev := s.countOf(TypeTeacherSentence)
		ttRunIntentTurn(t, tm, s, prev)
		if got := llm.callCount(); got != 1 {
			t.Fatalf("llm calls = %d, want 1", got)
		}
		if sys := llm.calls[0].messages[0].Content; !contains(sys, "检查阶段") {
			t.Errorf("check-stage system prompt = %q, want the check prompt (Pitfall 8)", sys)
		}
		if got := exec.advanceCount(); got != 0 {
			t.Errorf("advance calls = %d, want 0 (the check stage has no target)", got)
		}
	})

	t.Run("closed session ignores the utterance entirely", func(t *testing.T) {
		cards := ttCards("苹果", "香蕉")
		asr := newFakeASR(asrScript{text: "好了"})
		llm := newFakeLLM(intentScript(intentConfirm, ""))
		tm, s, _, exec := ttIntentFixture(t, cards, asr, llm)
		ended := s.countOf(TypeSessionEnded)
		tm.HandleControl(s, mustJSON(t, EndSessionMsg{Type: TypeEndSession}))
		if got := s.countOf(TypeSessionEnded); got != ended+1 {
			t.Fatalf("session_ended count = %d, want %d", got, ended+1)
		}

		done := make(chan struct{})
		go func() {
			tm.processTurn(s, []byte("pcm"))
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("closed turn never returned")
		}
		if got := asr.fedCount(); got != 0 {
			t.Errorf("asr calls = %d, want 0 (a closed session processes nothing)", got)
		}
		if got := exec.advanceCount(); got != 0 {
			t.Errorf("advance calls = %d, want 0", got)
		}
	})
}

// TestDictationConfirmStaleTurn: a confirmation whose classification is IN
// FLIGHT when the session moves under it must never execute. The hold sits
// in the LLM, not the ASR (PR #61 review): the classification request has
// been SENT — gateNextChat fires only once the model call is recorded — and
// the interrupt/close lands before the model answers; release then delivers
// the LATE 「confirm」 verdict. Waiting for StateProcessing (the old proof)
// only showed the turn had started, and an ASR-side delay never reached the
// classification phase at all. The interrupt aborts via the epoch bump; the
// close does not bump anything — epochCurrent's folded closed flag is what
// kills it.
func TestDictationConfirmStaleTurn(t *testing.T) {
	staleDrive := func(t *testing.T, interrupt bool) (*fakeExecutor, *captureSender, int) {
		t.Helper()
		cards := ttCards("苹果", "香蕉")
		asr := newFakeASR(asrScript{text: "好了"}) // prompt: the race is not in the ASR
		llm := newFakeLLM(intentScript(intentConfirm, ""))
		entered, release := llm.gateNextChat()
		tm, s, _, exec := ttIntentFixture(t, cards, asr, llm)
		sentBefore := s.countOf(TypeTeacherSentence)
		endedBefore := s.countOf(TypeSessionEnded)

		done := make(chan struct{})
		go func() {
			tm.processTurn(s, []byte("pcm"))
			close(done)
		}()
		select {
		case <-entered: // the classification request is out; the model is silent
		case <-time.After(5 * time.Second):
			t.Fatal("classification never started")
		}
		if interrupt {
			tm.HandleControl(s, mustJSON(t, InterruptMsg{Type: TypeInterrupt}))
		} else {
			tm.HandleControl(s, mustJSON(t, EndSessionMsg{Type: TypeEndSession}))
		}
		close(release) // the model's LATE confirm verdict
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Fatal("stale turn never returned")
		}
		if !interrupt && s.countOf(TypeSessionEnded) != endedBefore+1 {
			t.Fatalf("session_ended count = %d, want exactly one", s.countOf(TypeSessionEnded))
		}
		return exec, s, sentBefore
	}

	t.Run("interrupt mid-classification aborts the turn", func(t *testing.T) {
		exec, s, sentBefore := staleDrive(t, true)
		if got := exec.advanceCount(); got != 0 {
			t.Errorf("advance calls = %d, want 0 (interrupted confirm never executes)", got)
		}
		if got := s.countOf(TypeLessonUpdate); got != 0 {
			t.Errorf("lesson_update count = %d, want 0", got)
		}
		if got := s.countOf(TypeTeacherSentence); got != sentBefore {
			t.Errorf("teacher sentences = %d, want %d (no announcement after the interrupt)", got, sentBefore)
		}
	})

	t.Run("close mid-classification never commits the advance", func(t *testing.T) {
		exec, s, sentBefore := staleDrive(t, false)
		if got := exec.advanceCount(); got != 0 {
			t.Errorf("advance calls = %d, want 0 (a closed classroom never advances)", got)
		}
		if got := s.countOf(TypeLessonUpdate); got != 0 {
			t.Errorf("lesson_update count = %d, want 0 (nothing follows session_ended)", got)
		}
		if got := s.countOf(TypeTeacherSentence); got != sentBefore {
			t.Errorf("teacher sentences = %d, want %d (nothing speaks after the close)", got, sentBefore)
		}
		if order := s.typeOrder(); len(order) == 0 || order[len(order)-1] != TypeSessionEnded {
			t.Errorf("control order = %v, want session_ended last", order)
		}
	})
}

// TestDictationConfirmAdvanceCommitReturnRace: the executor's commit-vs-return
// window — the row is ALREADY committed when the session moves, and the
// executor returns into the moved session (PR #61 review asked for this race
// separately from the classification one: neither the upstream epoch check
// nor the cancelled context can undo a committed write, and a close does not
// bump the epoch). The store keeps the commit — the durable truth a
// reconnect reads — but the dead turn must not align the tutor, ship
// lesson_update, or open the check stage; session_ended stays terminal.
func TestDictationConfirmAdvanceCommitReturnRace(t *testing.T) {
	raceDrive := func(t *testing.T, interrupt bool) (*fakeExecutor, *TurnManager, *captureSender, int) {
		t.Helper()
		cards := ttCards("苹果", "香蕉")
		asr := newFakeASR(asrScript{text: "好了"})
		llm := newFakeLLM(intentScript(intentConfirm, ""))
		tm, s, _, exec := ttIntentFixture(t, cards, asr, llm)
		entered, release := exec.gateAdvance()
		sentBefore := s.countOf(TypeTeacherSentence)

		done := make(chan struct{})
		go func() {
			tm.processTurn(s, []byte("pcm"))
			close(done)
		}()
		select {
		case <-entered: // AdvanceWord committed the row; it has not returned
		case <-time.After(5 * time.Second):
			t.Fatal("advance never committed")
		}
		if interrupt {
			tm.HandleControl(s, mustJSON(t, InterruptMsg{Type: TypeInterrupt}))
		} else {
			tm.HandleControl(s, mustJSON(t, EndSessionMsg{Type: TypeEndSession}))
		}
		close(release) // the executor returns into the moved session
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Fatal("raced turn never returned")
		}
		return exec, tm, s, sentBefore
	}

	assertSuppressed := func(t *testing.T, exec *fakeExecutor, tm *TurnManager, s *captureSender, sentBefore int) {
		t.Helper()
		if got := exec.advanceCount(); got != 1 {
			t.Fatalf("advance calls = %d, want 1 (the commit itself stands in the store)", got)
		}
		row, err := exec.GetLessonState(context.Background(), ttLessonID)
		if err != nil {
			t.Fatalf("row read: %v", err)
		}
		if row.CurrentIndex != 1 || row.Version != 2 {
			t.Errorf("stored row = index %d version %d, want 1/2 (the commit survives the move)", row.CurrentIndex, row.Version)
		}
		if got := s.countOf(TypeLessonUpdate); got != 0 {
			t.Errorf("lesson_update count = %d, want 0 (a dead turn ships no progress)", got)
		}
		if got := s.countOf(TypeTeacherSentence); got != sentBefore {
			t.Errorf("teacher sentences = %d, want %d (nothing narrates after the move)", got, sentBefore)
		}
		tm.mu.Lock()
		idx, checking := tm.tutor.currentIndex, tm.tutor.checking
		tm.mu.Unlock()
		if idx != 0 || checking {
			t.Errorf("tutor = index %d checking %v, want 0/false (a dead session aligns nothing)", idx, checking)
		}
	}

	t.Run("close under a mid-lesson advance", func(t *testing.T) {
		exec, tm, s, sentBefore := raceDrive(t, false)
		assertSuppressed(t, exec, tm, s, sentBefore)
		if order := s.typeOrder(); len(order) == 0 || order[len(order)-1] != TypeSessionEnded {
			t.Errorf("control order = %v, want session_ended last", order)
		}
	})

	t.Run("interrupt under a mid-lesson advance", func(t *testing.T) {
		exec, tm, s, sentBefore := raceDrive(t, true)
		assertSuppressed(t, exec, tm, s, sentBefore)
	})
}

// TestDictationConfirmAdvanceEntryEpochGate: PR #61 review round 2 — the one
// ordering the executor-gated race above cannot reach: processTurn's
// post-classification epoch check PASSES and an interrupt lands in the
// unlocked gap before advanceTarget's entry. The gap holds no provider call a
// test gate could hold, so the test reproduces the gap's exit state directly:
// the classification returned its confirm verdict, the FSM epoch has since
// been bumped (interrupt). The entry gate must refuse BEFORE AdvanceWord —
// unlike the commit-vs-return race, the store keeps NO commit at all — and
// the dead turn must not speak its degraded chat reply either.
func TestDictationConfirmAdvanceEntryEpochGate(t *testing.T) {
	newTurn := func(t *testing.T) (*fakeExecutor, *TurnManager, *captureSender, int) {
		cards := ttCards("苹果", "香蕉")
		asr := newFakeASR(asrScript{text: "好了"})
		llm := newFakeLLM(intentScript(intentConfirm, "这就给你下一个词。"))
		tm, s, _, exec := ttIntentFixture(t, cards, asr, llm)
		return exec, tm, s, s.countOf(TypeTeacherSentence)
	}

	t.Run("interrupt before the executor never commits", func(t *testing.T) {
		exec, tm, s, sentBefore := newTurn(t)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		tm.mu.Lock()
		tm.epoch++ // the interrupt's bump: the turn's captured epoch is stale now
		tm.mu.Unlock()
		tm.confirmTarget(s, ctx, cancel, 0, 0, "这就给你下一个词。", TurnTarget{WordIndex: 0, ExpectedVersion: 1})

		if got := exec.advanceCount(); got != 0 {
			t.Errorf("advance calls = %d, want 0 (a stale-epoch dispatch never reaches the executor)", got)
		}
		row, err := exec.GetLessonState(context.Background(), ttLessonID)
		if err != nil {
			t.Fatalf("row read: %v", err)
		}
		if row.CurrentIndex != 0 || row.Version != 1 {
			t.Errorf("stored row = index %d version %d, want 0/1 (no commit at all)", row.CurrentIndex, row.Version)
		}
		if got := s.countOf(TypeLessonUpdate); got != 0 {
			t.Errorf("lesson_update count = %d, want 0", got)
		}
		if got := s.countOf(TypeTeacherSentence); got != sentBefore {
			t.Errorf("teacher sentences = %d, want %d (the degraded reply stays silent on a stale turn)", got, sentBefore)
		}
	})

	t.Run("closed before the executor stays silent", func(t *testing.T) {
		_, tm, s, sentBefore := newTurn(t)
		tm.handleEndSession(s)
		ended := s.countOf(TypeSessionEnded)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		tm.confirmTarget(s, ctx, cancel, 0, tm.currentEpoch(), "这就给你下一个词。", TurnTarget{WordIndex: 0, ExpectedVersion: 1})

		if got := s.countOf(TypeTeacherSentence); got != sentBefore {
			t.Errorf("teacher sentences = %d, want %d (nothing speaks after the close)", got, sentBefore)
		}
		if got := s.countOf(TypeSessionEnded); got != ended {
			t.Errorf("session_ended count = %d, want %d", got, ended)
		}
		if order := s.typeOrder(); len(order) == 0 || order[len(order)-1] != TypeSessionEnded {
			t.Errorf("control order = %v, want session_ended last", order)
		}
	})
}

// TestDictationConfirmCloseUnderEnterCheck: the LAST word's stage write — the
// advance committed and returned, EnterCheck's row write is IN FLIGHT when
// the session closes. The row keeps stage='checking' (a reconnect resumes
// the panel from it), but the dead classroom opens no panel: no
// check_started, no bridge, tutor never flips to checking, session_ended
// stays last.
func TestDictationConfirmCloseUnderEnterCheck(t *testing.T) {
	cards := ttCards("苹果") // one word: the first advance is the last
	asr := newFakeASR(asrScript{text: "好了"})
	llm := newFakeLLM(intentScript(intentConfirm, ""))
	tm, s, _, exec := ttIntentFixture(t, cards, asr, llm)
	entered, release := exec.gateEnterCheck()
	sentBefore := s.countOf(TypeTeacherSentence)
	endedBefore := s.countOf(TypeSessionEnded)

	done := make(chan struct{})
	go func() {
		tm.processTurn(s, []byte("pcm"))
		close(done)
	}()
	select {
	case <-entered: // EnterCheck committed stage='checking'; not returned
	case <-time.After(5 * time.Second):
		t.Fatal("enter_check never committed")
	}
	tm.HandleControl(s, mustJSON(t, EndSessionMsg{Type: TypeEndSession}))
	close(release) // the stage write returns into the closed session
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("enter-check turn never returned")
	}

	if got := s.countOf(TypeSessionEnded); got != endedBefore+1 {
		t.Fatalf("session_ended count = %d, want exactly one", got)
	}
	if got := s.countOf(TypeCheckStarted); got != 0 {
		t.Errorf("check_started count = %d, want 0 (a closed classroom opens no panel)", got)
	}
	if got := s.countOf(TypeTeacherSentence); got != sentBefore {
		t.Errorf("teacher sentences = %d, want %d (no bridge after the close)", got, sentBefore)
	}
	if order := s.typeOrder(); len(order) == 0 || order[len(order)-1] != TypeSessionEnded {
		t.Errorf("control order = %v, want session_ended last", order)
	}
	row, err := exec.GetLessonState(context.Background(), ttLessonID)
	if err != nil {
		t.Fatalf("row read: %v", err)
	}
	if row.Stage != lesson.StageChecking {
		t.Errorf("stored stage = %q, want checking (the committed write survives the close)", row.Stage)
	}
	tm.mu.Lock()
	checking := tm.tutor.checking
	tm.mu.Unlock()
	if checking {
		t.Error("the dead tutor entered the check stage")
	}
}

// TestDictationConfirmMoveAfterEnterCheckPassed: PR #61 review round 2 — the
// ordering TestDictationConfirmCloseUnderEnterCheck cannot reach: EnterCheck's
// liveness check has already PASSED and the move lands in the unlocked gap
// before the tutor mutation and the check_started send. The conflict
// read-back (GetLessonState) is the one hookable provider call inside that
// window, so the one-shot gate pins it in flight: EnterCheck conflicts, its
// liveness check passes, the read-back is held, the session moves, the read
// returns — and the fixed enterCheck refuses at the mutation lock: no panel,
// no bridge, the tutor never flips to checking, session_ended stays last.
func TestDictationConfirmMoveAfterEnterCheckPassed(t *testing.T) {
	drive := func(t *testing.T, interrupt bool) (*fakeExecutor, *TurnManager, *captureSender, int) {
		t.Helper()
		cards := ttCards("苹果") // one word: the first advance is the last
		asr := newFakeASR(asrScript{text: "好了"})
		llm := newFakeLLM(intentScript(intentConfirm, ""))
		tm, s, _, exec := ttIntentFixture(t, cards, asr, llm)
		advEntered, advRelease := exec.gateAdvance()
		stateEntered, stateRelease := exec.gateGetState()
		sentBefore := s.countOf(TypeTeacherSentence)

		done := make(chan struct{})
		go func() {
			tm.processTurn(s, []byte("pcm"))
			close(done)
		}()
		select {
		case <-advEntered: // the advance committed; hold its return
		case <-time.After(5 * time.Second):
			t.Fatal("advance never committed")
		}
		// Force the stage write to CONFLICT so enterCheck takes the read-back
		// branch — the hookable window under test.
		exec.mu.Lock()
		exec.conflictVersion = 2
		exec.mu.Unlock()
		close(advRelease)
		select {
		case <-stateEntered: // liveness check passed; the read-back is in flight
		case <-time.After(5 * time.Second):
			t.Fatal("conflict read-back never started")
		}
		// The stored row reads as mid-check, so the OLD code's resyncCheck
		// would flip the dead tutor into the check state (the fixed code
		// never reaches it).
		exec.mu.Lock()
		exec.lesson.Stage = lesson.StageChecking
		exec.mu.Unlock()
		if interrupt {
			tm.HandleControl(s, mustJSON(t, InterruptMsg{Type: TypeInterrupt}))
		} else {
			tm.HandleControl(s, mustJSON(t, EndSessionMsg{Type: TypeEndSession}))
		}
		close(stateRelease) // the read returns into the moved session
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Fatal("enter-check turn never returned")
		}
		return exec, tm, s, sentBefore
	}

	t.Run("close after the check passed opens no panel", func(t *testing.T) {
		_, tm, s, sentBefore := drive(t, false)
		if got := s.countOf(TypeSessionEnded); got != 1 {
			t.Fatalf("session_ended count = %d, want exactly one", got)
		}
		if got := s.countOf(TypeCheckStarted); got != 0 {
			t.Errorf("check_started count = %d, want 0 (nothing follows session_ended)", got)
		}
		if got := s.countOf(TypeTeacherSentence); got != sentBefore {
			t.Errorf("teacher sentences = %d, want %d (no bridge after the close)", got, sentBefore)
		}
		if order := s.typeOrder(); len(order) == 0 || order[len(order)-1] != TypeSessionEnded {
			t.Errorf("control order = %v, want session_ended last", order)
		}
		tm.mu.Lock()
		checking := tm.tutor.checking
		tm.mu.Unlock()
		if checking {
			t.Error("the dead tutor entered the check stage")
		}
	})

	t.Run("interrupt after the check passed never plays the bridge", func(t *testing.T) {
		_, tm, s, sentBefore := drive(t, true)
		if got := s.countOf(TypeCheckStarted); got != 0 {
			t.Errorf("check_started count = %d, want 0 (a stale turn opens no panel)", got)
		}
		if got := s.countOf(TypeTeacherSentence); got != sentBefore {
			t.Errorf("teacher sentences = %d, want %d (the bridge never plays over the next classroom)", got, sentBefore)
		}
		tm.mu.Lock()
		checking := tm.tutor.checking
		tm.mu.Unlock()
		if checking {
			t.Error("a stale turn flipped the tutor into the check stage")
		}
	})
}

// TestDictationDoubleConfirmNewTargetAdvances: after the NEXT word's
// announcement settled, a NEW spoken 「好了」 belongs to the NEW target — the
// per-target idempotency key must not swallow it as a duplicate of the first
// confirm, and each confirm advances exactly one word.
func TestDictationDoubleConfirmNewTargetAdvances(t *testing.T) {
	cards := ttCards("苹果", "香蕉", "橘子")
	asr := newFakeASR(asrScript{text: "好了"}, asrScript{text: "好了"})
	llm := newFakeLLM(intentScript(intentConfirm, ""), intentScript(intentConfirm, ""))
	tm, s, _, exec := ttIntentFixture(t, cards, asr, llm)

	prev := s.countOf(TypeTeacherSentence)
	ttRunIntentTurn(t, tm, s, prev) // confirm word 1 → announce word 2
	mid := s.countOf(TypeTeacherSentence)
	ttRunIntentTurn(t, tm, s, mid) // confirm word 2 → announce word 3

	if got := exec.advanceCount(); got != 2 {
		t.Fatalf("advance calls = %d, want 2 (each confirm drives its own target)", got)
	}
	calls := exec.advanceCallsSnapshot()
	if calls[0].requestID != "L1-advance-0" || calls[1].requestID != "L1-advance-1" {
		t.Errorf("request ids = [%s %s], want per-target keys [L1-advance-0 L1-advance-1]",
			calls[0].requestID, calls[1].requestID)
	}
	if got := s.countOf(TypeLessonUpdate); got != 2 {
		t.Errorf("lesson_update count = %d, want 2", got)
	}
	tm.mu.Lock()
	idx := tm.tutor.currentIndex
	tm.mu.Unlock()
	if idx != 2 {
		t.Errorf("currentIndex = %d, want 2", idx)
	}
}
