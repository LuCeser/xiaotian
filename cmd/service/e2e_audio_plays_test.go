package main

// DICT-03 e2e: the word_complete control frame carries the additive
// audio_plays hint derived from the session's word audio source — 2 when the
// Mimo per-word path produced single-pronunciation tracks (the frontend
// schedules the dictation-standard double play), 1 for the Volcengine
// pregenerate path's pre-paired "word. word." tracks. These tests drive the
// REAL handler stack + session event loop (same harness as the Ticket 08
// stories) and assert the actual wire payload per source.

import (
	"encoding/json"
	"testing"

	"github.com/xiaotian/home-tutor-agent/pkg/session"
)

// wordCompletePayloadOf boots a session with the given generator, plays the
// first word, and returns the decoded word_complete payload — the shared
// ramp for both source assertions.
func wordCompletePayloadOf(t *testing.T, gen session.TTSSessionGenerator) map[string]interface{} {
	t.Helper()
	fakeFSRS := newStoriesFSRS()
	conns, _, teardown := bootStoriesWithGen(t, fakeFSRS, gen, 1)
	defer teardown()
	conn := conns[0]

	ready := startStoriesSession(t, conn)
	sendControlJSON(t, conn, `{"type":"play_word","params":{"word_id":"`+ready.Words[0]+`"}}`)
	payload := readControlUntil(t, conn, `"type":"word_complete"`)

	var msg map[string]interface{}
	if err := json.Unmarshal([]byte(payload), &msg); err != nil {
		t.Fatalf("word_complete decode: %v (payload: %s)", err, payload)
	}
	return msg
}

// TestE2E_WordCompleteAudioPlays_MimoSource: mimo word source → audio_plays
// 2 (single pronunciation, frontend double-plays).
func TestE2E_WordCompleteAudioPlays_MimoSource(t *testing.T) {
	msg := wordCompletePayloadOf(t, &mimoGen{})
	if got, ok := msg["audio_plays"].(float64); !ok || int(got) != 2 {
		t.Errorf("audio_plays = %v (%T), want 2", msg["audio_plays"], msg["audio_plays"])
	}
}

// TestE2E_WordCompleteAudioPlays_VolcengineSource: volcengine word source →
// audio_plays 1 (the track is already paired "word. word.").
func TestE2E_WordCompleteAudioPlays_VolcengineSource(t *testing.T) {
	msg := wordCompletePayloadOf(t, &fsmGen{})
	if got, ok := msg["audio_plays"].(float64); !ok || int(got) != 1 {
		t.Errorf("audio_plays = %v (%T), want 1", msg["audio_plays"], msg["audio_plays"])
	}
	// The pre-existing fields stay intact (additive-field compatibility).
	if msg["type"] != "word_complete" {
		t.Errorf("type = %v, want word_complete", msg["type"])
	}
	if msg["word_id"] == "" || msg["word_id"] == nil {
		t.Errorf("word_id missing: %v", msg)
	}
}
