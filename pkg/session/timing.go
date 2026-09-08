package session

import "time"

// AdaptiveTimeout returns a per-word writing timeout between 8 and 15 seconds,
// scaled by word length. Short words (≤4 chars) get the base 8s; each additional
// character adds ~1s, capped at 15s. This models the traditional teacher-student
// dictation rhythm: longer/harder words get more writing time.
//
// The formula is deterministic and pure — no randomness, no external state — so
// tests can assert exact values.
func AdaptiveTimeout(word string) time.Duration {
	const (
		baseSeconds = 8
		perCharSec  = 1
		minChars    = 4 // words ≤ this get base timeout
		maxSeconds  = 15
	)

	n := len(word)
	extra := 0
	if n > minChars {
		extra = n - minChars
	}
	secs := baseSeconds + extra*perCharSec
	if secs > maxSeconds {
		secs = maxSeconds
	}
	return time.Duration(secs) * time.Second
}
