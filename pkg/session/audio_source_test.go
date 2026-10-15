package session_test

import (
	"context"
	"errors"
	"testing"

	"github.com/xiaotian/home-tutor-agent/pkg/fsrs"
	"github.com/xiaotian/home-tutor-agent/pkg/session"
	"github.com/xiaotian/home-tutor-agent/pkg/tts"
)

// TestNewSession_AudioSourceRemembered: the session stores the DICT-03
// source annotation its generator reported at init — SourceMimo when the
// Mimo per-word path produced the audio, SourceVolcengine for the batch
// path. The source is decided ONCE at init (it never changes mid-session).
func TestNewSession_AudioSourceRemembered(t *testing.T) {
	cases := []struct {
		name   string
		source tts.AudioSource
	}{
		{"mimo source", tts.SourceMimo},
		{"volcengine source", tts.SourceVolcengine},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fsrsSvc := &fakeFSRSService{dueCards: threeCards()}
			gen := &fakeTTSGenerator{source: tc.source}

			s, err := session.NewSession(
				context.Background(),
				nil, // client — not exercised
				"conn-src-"+string(tc.source),
				fsrsSvc,
				gen,
				nil, nil, nil, nil, nil, testCfg(),
				fsrs.SelectOpts{Limit: 3},
			)
			if err != nil {
				t.Fatalf("NewSession failed: %v", err)
			}
			if got := s.AudioSource(); got != tc.source {
				t.Errorf("AudioSource() = %q, want %q", got, tc.source)
			}
			// Sanity: the generator really reported that source to the session.
			if got := gen.LastSource(); got != tc.source {
				t.Errorf("generator last reported source = %q, want %q", got, tc.source)
			}
		})
	}
}

// TestNewSession_TTSFailureWithSource_Unlocks: the WithSource init path
// keeps the D-10 unlock contract — a generation failure still releases the
// FSRS lock synchronously.
func TestNewSession_TTSFailureWithSource_Unlocks(t *testing.T) {
	fsrsSvc := &fakeFSRSService{dueCards: threeCards()}
	gen := &fakeTTSGenerator{genErr: errors.New("simulated TTS failure"), source: tts.SourceMimo}

	if _, err := session.NewSession(
		context.Background(), nil, "conn-fail",
		fsrsSvc, gen, nil, nil, nil, nil, nil, testCfg(),
		fsrs.SelectOpts{Limit: 3},
	); err == nil {
		t.Fatal("NewSession() succeeded, want TTS failure")
	}
	if count := fsrsSvc.UnlockCount(); count != 1 {
		t.Errorf("UnlockCount() = %d, want 1 (D-10 init-path unlock)", count)
	}
}

// TestWordCompleteAudioPlays: the DICT-03 audio_plays hint — mimo audio is
// a SINGLE pronunciation so the frontend double-plays (2); volcengine tracks
// are pre-paired "word. word." so it plays once (1); anything unknown maps
// to 1 (conservative: never double-play a possibly-paired track).
func TestWordCompleteAudioPlays(t *testing.T) {
	cases := []struct {
		source tts.AudioSource
		want   int
	}{
		{tts.SourceMimo, 2},
		{tts.SourceVolcengine, 1},
		{"", 1},      // zero value (sessions built by struct literals in old tests)
		{"weird", 1}, // unknown backend
	}
	for _, tc := range cases {
		if got := session.WordCompleteAudioPlays(tc.source); got != tc.want {
			t.Errorf("WordCompleteAudioPlays(%q) = %d, want %d", tc.source, got, tc.want)
		}
	}
}
