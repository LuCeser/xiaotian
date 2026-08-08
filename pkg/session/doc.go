// Package session is the orchestration seam binding the ws, fsrs, tts, database,
// and infra packages into a single dictation-session lifecycle.
//
// A Session owns the per-connection event loop (the single writer to session
// state), the FSRS fetch-lock acquired at start (DICT-02), and the TTS
// generation/streaming kickoff (D-13). A Manager tracks active sessions and
// runs a sweeper backstop that cancels leaked ones (ERR-03). Every exit path
// runs a deferred cleanup chain that releases the FSRS lock, clears the TTS
// cache, and unregisters from the Hub (D-10).
package session
