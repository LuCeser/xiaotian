package tts

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestSessionCache_StoreLoad_RoundTrip tests that Store followed by Load
// returns identical bytes to what was stored.
func TestSessionCache_StoreLoad_RoundTrip(t *testing.T) {
	baseDir := t.TempDir()
	cache, err := NewSessionCache(baseDir, nil) // No-op logger is fine for this test
	if err != nil {
		t.Fatalf("NewSessionCache() returned error: %v", err)
	}

	sessionID := "test-session"
	wordID := "test-word"
	pcmData := []byte{0x00, 0x01, 0x02, 0x03, 0x04}

	// Store the PCM data
	err = cache.Store(sessionID, wordID, pcmData)
	if err != nil {
		t.Fatalf("Store() returned error: %v", err)
	}

	// Load it back
	loaded, err := cache.Load(sessionID, wordID)
	if err != nil {
		t.Fatalf("Load() returned error: %v", err)
	}

	// Verify the bytes match
	if len(loaded) != len(pcmData) {
		t.Fatalf("Loaded %d bytes, want %d", len(loaded), len(pcmData))
	}
	for i := range pcmData {
		if loaded[i] != pcmData[i] {
			t.Errorf("Byte[%d] = %02x, want %02x", i, loaded[i], pcmData[i])
		}
	}
}

// TestSessionCache_Load_NotFound_ReturnsErrWordNotCached tests that Load for a
// non-existent wordID returns ErrWordNotCached (detectable via errors.Is).
func TestSessionCache_Load_NotFound_ReturnsErrWordNotCached(t *testing.T) {
	baseDir := t.TempDir()
	cache, err := NewSessionCache(baseDir, nil)
	if err != nil {
		t.Fatalf("NewSessionCache() returned error: %v", err)
	}

	sessionID := "test-session"
	wordID := "nonexistent-word"

	_, err = cache.Load(sessionID, wordID)
	if err == nil {
		t.Fatal("Load() for nonexistent word returned nil error, want ErrWordNotCached")
	}
	if !errors.Is(err, ErrWordNotCached) {
		t.Fatalf("Load() error = %v, want ErrWordNotCached (errors.Is)", err)
	}
}

// TestSessionCache_Cleanup_Idempotent tests that Cleanup removes the session
// directory and a second Cleanup call returns nil (idempotent).
func TestSessionCache_Cleanup_Idempotent(t *testing.T) {
	baseDir := t.TempDir()
	cache, err := NewSessionCache(baseDir, nil)
	if err != nil {
		t.Fatalf("NewSessionCache() returned error: %v", err)
	}

	sessionID := "test-session"
	wordID := "test-word"
	pcmData := []byte{0x00, 0x01, 0x02}

	// Store some data
	err = cache.Store(sessionID, wordID, pcmData)
	if err != nil {
		t.Fatalf("Store() returned error: %v", err)
	}

	// First cleanup
	err = cache.Cleanup(sessionID)
	if err != nil {
		t.Fatalf("First Cleanup() returned error: %v", err)
	}

	// Verify directory is gone
	sessionDir := cache.SessionDir(sessionID)
	if _, err := os.Stat(sessionDir); !os.IsNotExist(err) {
		t.Fatalf("Session directory still exists after Cleanup: %v", err)
	}

	// Second cleanup should also succeed (idempotent)
	err = cache.Cleanup(sessionID)
	if err != nil {
		t.Fatalf("Second Cleanup() returned error (not idempotent): %v", err)
	}
}

// TestSessionCache_CleanupStale_OldDirectoriesRemoved tests that CleanupStale
// removes directories older than the given duration but leaves newer ones.
func TestSessionCache_CleanupStale_OldDirectoriesRemoved(t *testing.T) {
	baseDir := t.TempDir()
	cache, err := NewSessionCache(baseDir, nil)
	if err != nil {
		t.Fatalf("NewSessionCache() returned error: %v", err)
	}

	now := time.Now()

	// Create an old session (simulate it was created 2 hours ago)
	oldSession := "old-session"
	oldSessionDir := filepath.Join(baseDir, oldSession)
	if err := os.MkdirAll(oldSessionDir, 0755); err != nil {
		t.Fatalf("Failed to create old session directory: %v", err)
	}
	// Set modification time to 2 hours ago
	oldTime := now.Add(-2 * time.Hour)
	if err := os.Chtimes(oldSessionDir, oldTime, oldTime); err != nil {
		t.Fatalf("Failed to set old session directory time: %v", err)
	}

	// Create a new session (simulate it was created 10 minutes ago)
	newSession := "new-session"
	newSessionDir := filepath.Join(baseDir, newSession)
	if err := os.MkdirAll(newSessionDir, 0755); err != nil {
		t.Fatalf("Failed to create new session directory: %v", err)
	}
	// Set modification time to 10 minutes ago
	newTime := now.Add(-10 * time.Minute)
	if err := os.Chtimes(newSessionDir, newTime, newTime); err != nil {
		t.Fatalf("Failed to set new session directory time: %v", err)
	}

	// Cleanup sessions older than 1 hour
	maxAge := 1 * time.Hour
	err = cache.CleanupStale(maxAge)
	if err != nil {
		t.Fatalf("CleanupStale() returned error: %v", err)
	}

	// Verify old session is gone
	if _, err := os.Stat(oldSessionDir); !os.IsNotExist(err) {
		t.Errorf("Old session directory still exists after CleanupStale: %v", err)
	}

	// Verify new session still exists
	if _, err := os.Stat(newSessionDir); os.IsNotExist(err) {
		t.Error("New session directory was removed by CleanupStale, should still exist")
	}
}

// TestSessionCache_SessionDir_ReturnsCorrectPath tests that SessionDir returns
// the correct path for a session.
func TestSessionCache_SessionDir_ReturnsCorrectPath(t *testing.T) {
	baseDir := t.TempDir()
	cache, err := NewSessionCache(baseDir, nil)
	if err != nil {
		t.Fatalf("NewSessionCache() returned error: %v", err)
	}

	sessionID := "test-session"
	expectedPath := filepath.Join(baseDir, sessionID)

	actualPath := cache.SessionDir(sessionID)
	if actualPath != expectedPath {
		t.Errorf("SessionDir() = %q, want %q", actualPath, expectedPath)
	}
}

// TestSessionCache_FileNaming_WordsStoredAsWordIDpcm tests that Store creates
// files named {wordID}.pcm as per D-08.
func TestSessionCache_FileNaming_WordsStoredAsWordIDpcm(t *testing.T) {
	baseDir := t.TempDir()
	cache, err := NewSessionCache(baseDir, nil)
	if err != nil {
		t.Fatalf("NewSessionCache() returned error: %v", err)
	}

	sessionID := "test-session"
	wordID := "test-word"
	pcmData := []byte{0x00, 0x01}

	err = cache.Store(sessionID, wordID, pcmData)
	if err != nil {
		t.Fatalf("Store() returned error: %v", err)
	}

	// Verify the file exists at the expected path
	expectedPath := filepath.Join(baseDir, sessionID, wordID+".pcm")
	if _, err := os.Stat(expectedPath); os.IsNotExist(err) {
		t.Fatalf("PCM file not found at expected path: %s", expectedPath)
	}
}
