package rt

import (
	"sync"
	"testing"

	"github.com/xiaotian/home-tutor-agent/pkg/infra"
)

// captureLogger is an inline fake infra.Logger that records every entry's
// message and fields so tests can assert the exact rt_event field set.
type captureLogger struct {
	mu      sync.Mutex
	entries []captureEntry
}

type captureEntry struct {
	msg    string
	fields []infra.Field
}

func (c *captureLogger) record(msg string, fields []infra.Field) {
	c.mu.Lock()
	defer c.mu.Unlock()
	cp := make([]infra.Field, len(fields))
	copy(cp, fields)
	c.entries = append(c.entries, captureEntry{msg: msg, fields: cp})
}

func (c *captureLogger) Debug(msg string, fields ...infra.Field) { c.record(msg, fields) }
func (c *captureLogger) Info(msg string, fields ...infra.Field)  { c.record(msg, fields) }
func (c *captureLogger) Warn(msg string, fields ...infra.Field)  { c.record(msg, fields) }
func (c *captureLogger) Error(msg string, fields ...infra.Field) { c.record(msg, fields) }

// With returns the receiver; the recorder never calls With, and returning
// the same capture keeps any future child entries visible to the test.
func (c *captureLogger) With(fields ...infra.Field) infra.Logger { return c }

func (c *captureLogger) Sync() error { return nil }

func (c *captureLogger) snapshot() []captureEntry {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]captureEntry, len(c.entries))
	copy(out, c.entries)
	return out
}

func fieldValue(t *testing.T, e captureEntry, key string) any {
	t.Helper()
	for _, f := range e.fields {
		if f.Key == key {
			return f.Value
		}
	}
	t.Fatalf("entry %q has no field %q (fields: %v)", e.msg, key, e.fields)
	return nil
}

// TestTimingEmitFields asserts the rt_event entry always carries exactly the
// base field names — event, session_id, t_mono_ns — plus the caller's custom
// fields (T-13-06b: the test asserts field NAMES, not payloads; the
// redaction boundary itself is the godoc contract on Emit).
func TestTimingEmitFields(t *testing.T) {
	log := &captureLogger{}
	rec := NewEventRecorder(log, "sess-1")

	rec.Emit(EventSpeechStarted, infra.F("turn_id", 3), infra.F("epoch", 9))

	entries := log.snapshot()
	if len(entries) != 1 {
		t.Fatalf("captured %d entries, want 1", len(entries))
	}
	e := entries[0]
	if e.msg != "rt_event" {
		t.Errorf("msg = %q, want rt_event", e.msg)
	}
	if got := fieldValue(t, e, "event"); got != EventSpeechStarted {
		t.Errorf("event = %v, want %q", got, EventSpeechStarted)
	}
	if got := fieldValue(t, e, "session_id"); got != "sess-1" {
		t.Errorf("session_id = %v, want sess-1", got)
	}
	ns, ok := fieldValue(t, e, "t_mono_ns").(int64)
	if !ok {
		t.Fatalf("t_mono_ns = %T, want int64", fieldValue(t, e, "t_mono_ns"))
	}
	if ns < 0 {
		t.Errorf("t_mono_ns = %d, want >= 0", ns)
	}
	if got := fieldValue(t, e, "turn_id"); got != 3 {
		t.Errorf("turn_id = %v, want 3", got)
	}
	if got := fieldValue(t, e, "epoch"); got != 9 {
		t.Errorf("epoch = %v, want 9", got)
	}
}

// TestTimingMonoNsMonotonicAndSameSourceAsEmit: two MonoNs calls are
// non-decreasing, and a subsequent Emit's t_mono_ns is drawn from the same
// anchor (it lands between the surrounding MonoNs readings).
func TestTimingMonoNsMonotonicAndSameSourceAsEmit(t *testing.T) {
	log := &captureLogger{}
	rec := NewEventRecorder(log, "sess-2")

	first := rec.MonoNs()
	before := rec.MonoNs()
	rec.Emit(EventConnected)
	after := rec.MonoNs()

	if before < first {
		t.Errorf("MonoNs went backwards: %d then %d", first, before)
	}
	entries := log.snapshot()
	if len(entries) != 1 {
		t.Fatalf("captured %d entries, want 1", len(entries))
	}
	ns, ok := fieldValue(t, entries[0], "t_mono_ns").(int64)
	if !ok {
		t.Fatalf("t_mono_ns = %T, want int64", fieldValue(t, entries[0], "t_mono_ns"))
	}
	if ns < before {
		t.Errorf("emit t_mono_ns %d < surrounding MonoNs %d (different anchors?)", ns, before)
	}
	if ns > after {
		t.Errorf("emit t_mono_ns %d > surrounding MonoNs %d (different anchors?)", ns, after)
	}
}

// TestTimingNilLoggerDefaultsToNoOp: a nil logger must not panic — the
// recorder substitutes the no-op logger (D-42 zero value).
func TestTimingNilLoggerDefaultsToNoOp(t *testing.T) {
	rec := NewEventRecorder(nil, "sess-3")
	if rec == nil {
		t.Fatal("NewEventRecorder(nil, ...) returned nil")
	}
	if rec.MonoNs() < 0 {
		t.Errorf("MonoNs = %d, want >= 0", rec.MonoNs())
	}
	rec.Emit(EventSessionEnd) // must not panic
	rec.Emit(EventError, infra.F("code", ErrCodeProviderFailed))
}

// TestTimingEventVocabulary anchors the server-side event VALUES to the
// REQUIREMENTS 必须记录 vocabulary — these strings land in evidence JSONL,
// so a rename here is a wire change.
func TestTimingEventVocabulary(t *testing.T) {
	want := map[string]string{
		"EventConnected":           "connected",
		"EventSpeechStarted":       "speech_started",
		"EventSpeechEnded":         "speech_ended",
		"EventFirstAudioReceived":  "first_audio_received",
		"EventInterruptHandled":    "interrupt",
		"EventWordAnnounced":       "word_announced",
		"EventWordAdvanced":        "word_advanced",
		"EventTimeoutFired":        "timeout_fired",
		"EventIntentClassified":    "intent_classified",
		"EventLessonComplete":      "lesson_complete",
		"EventReviewWritten":       "review_written",
		"EventPlaybackAckTimeout":  "playback_ack_timeout",
		"EventSentenceAckRejected": "sentence_ack_rejected",
		"EventSessionEnd":          "session_end",
		"EventError":               "error",
	}
	got := map[string]string{
		"EventConnected":           EventConnected,
		"EventSpeechStarted":       EventSpeechStarted,
		"EventSpeechEnded":         EventSpeechEnded,
		"EventFirstAudioReceived":  EventFirstAudioReceived,
		"EventInterruptHandled":    EventInterruptHandled,
		"EventWordAnnounced":       EventWordAnnounced,
		"EventWordAdvanced":        EventWordAdvanced,
		"EventTimeoutFired":        EventTimeoutFired,
		"EventIntentClassified":    EventIntentClassified,
		"EventLessonComplete":      EventLessonComplete,
		"EventReviewWritten":       EventReviewWritten,
		"EventPlaybackAckTimeout":  EventPlaybackAckTimeout,
		"EventSentenceAckRejected": EventSentenceAckRejected,
		"EventSessionEnd":          EventSessionEnd,
		"EventError":               EventError,
	}
	for name, w := range want {
		if got[name] != w {
			t.Errorf("%s = %q, want %q", name, got[name], w)
		}
	}
}
