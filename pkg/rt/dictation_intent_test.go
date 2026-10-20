// dictation_intent_test.go — Issue #56: the dictation turn's task-context
// snapshot and the intent_classified evidence correlation. The full-phrase
// confirmation correction's own table tests land with the resolver (step 3);
// this file first pins WHAT the classifier may assume about the classroom
// (captured before the processing flip, retained through the turn) and WHAT
// the event stream may say about one turn (labels + count-class fields,
// never transcript or word text).
package rt

import (
	"testing"
	"time"
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
		"paused":    paused,
		"checking":  checking,
		"recitation": recite,
		"completed": completed,
		"past-end":  pastEnd,
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
