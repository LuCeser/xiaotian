package session

import (
	"testing"
	"time"
)

func TestAdaptiveTimeout(t *testing.T) {
	tests := []struct {
		name string
		word string
		want time.Duration
	}{
		{"empty word", "", 8 * time.Second},
		{"1 char", "a", 8 * time.Second},
		{"3 chars", "cat", 8 * time.Second},
		{"4 chars (boundary)", "days", 8 * time.Second},
		{"5 chars", "apple", 9 * time.Second},
		{"6 chars", "banana", 10 * time.Second},
		{"7 chars", "because", 11 * time.Second},
		{"8 chars", "elephant", 12 * time.Second},
		{"10 chars", "dictionary", 14 * time.Second},
		{"11 chars", "playgrounds", 15 * time.Second},
		{"12 chars (cap)", "extraordinary", 15 * time.Second},
		{"20 chars (far cap)", "supercalifragilistic", 15 * time.Second},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := AdaptiveTimeout(tt.word)
			if got != tt.want {
				t.Errorf("AdaptiveTimeout(%q) = %v, want %v", tt.word, got, tt.want)
			}
		})
	}
}

func TestAdaptiveTimeout_Range(t *testing.T) {
	// Verify all timeouts are within the 8-15 second range
	words := []string{"", "a", "cat", "hello", "beautiful", "extraordinary", "a-much-longer-word-than-expected"}
	for _, w := range words {
		d := AdaptiveTimeout(w)
		if d < 8*time.Second || d > 15*time.Second {
			t.Errorf("AdaptiveTimeout(%q) = %v, want between 8s and 15s", w, d)
		}
	}
}
