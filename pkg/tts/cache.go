package tts

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"regexp"
	"time"

	"github.com/pkg/errors"
	"github.com/xiaotian/home-tutor-agent/pkg/infra"
)

// identPattern is the allow-list for sessionID identifiers used to form
// filesystem paths. Restricted to alphanumeric, hyphen, and underscore so
// that path separators ("/", "\") and traversal segments ("..") cannot reach
// filepath.Join (T-06-02 path-traversal mitigation).
var identPattern = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// validateIdent rejects empty sessionIDs or any sessionID containing
// characters outside the allow-list. This prevents sessionID values supplied
// via control frames from escaping baseDir through path traversal.
func validateIdent(name, value string) error {
	if value == "" {
		return errors.Errorf("%s cannot be empty", name)
	}
	if !identPattern.MatchString(value) {
		return errors.Errorf("%s contains invalid characters; only alphanumeric, hyphen, and underscore are allowed", name)
	}
	return nil
}

// cacheKey sanitizes a wordID into a filename-safe cache key. WordIDs are
// CACHE KEYS, not filenames: Chinese dictation words and the Ticket-06
// feedback sentences are arbitrary text, which the old allow-list rejected
// (breaking zh sessions at TTS generation). Identifier-shaped keys keep their
// readable name; everything else is hashed to a fixed-length hex name that no
// separator or traversal sequence can survive. The "w_" prefix keeps hashed
// names out of the identifier namespace entirely.
func cacheKey(wordID string) (string, error) {
	if wordID == "" {
		return "", errors.New("wordID cannot be empty")
	}
	if identPattern.MatchString(wordID) {
		return wordID, nil
	}
	sum := sha256.Sum256([]byte(wordID))
	return "w_" + hex.EncodeToString(sum[:16]), nil
}

// SessionCache manages PCM file storage per D-02/D-08. Each session gets a
// subdirectory under baseDir, and words are stored as {wordID}.pcm files.
type SessionCache struct {
	baseDir string
	logger  infra.Logger
}

// NewSessionCache creates a new SessionCache with the given base directory.
// Validates baseDir is non-empty and substitutes NewNoOpLogger for nil logger.
func NewSessionCache(baseDir string, logger infra.Logger) (*SessionCache, error) {
	if baseDir == "" {
		return nil, errors.New("baseDir cannot be empty")
	}

	// Substitute NewNoOpLogger for nil (same pattern as infra/connection.go)
	if logger == nil {
		logger = infra.NewNoOpLogger()
	}

	return &SessionCache{
		baseDir: baseDir,
		logger:  logger,
	}, nil
}

// Store writes pcm bytes to the cache path for (sessionID, wordID) — see
// cacheKey for how arbitrary-text wordIDs map onto safe filenames. Creates
// parent directories via os.MkdirAll with 0755 permissions. Returns an error
// wrapping os.WriteFile failures.
func (c *SessionCache) Store(sessionID string, wordID string, pcm []byte) error {
	if err := validateIdent("sessionID", sessionID); err != nil {
		return err
	}
	key, err := cacheKey(wordID)
	if err != nil {
		return err
	}

	sessionDir := c.SessionDir(sessionID)
	filePath := filepath.Join(sessionDir, key+".pcm")

	// Create parent directories if needed
	if err := os.MkdirAll(sessionDir, 0755); err != nil {
		return errors.Wrap(err, "failed to create session directory")
	}

	// Write the PCM data
	if err := os.WriteFile(filePath, pcm, 0644); err != nil {
		return errors.Wrap(err, "failed to write PCM file")
	}

	return nil
}

// Load reads the cache path for (sessionID, wordID). If the file does not
// exist (os.IsNotExist), returns ErrWordNotCached. Otherwise returns the file
// bytes.
func (c *SessionCache) Load(sessionID string, wordID string) ([]byte, error) {
	if err := validateIdent("sessionID", sessionID); err != nil {
		return nil, err
	}
	key, err := cacheKey(wordID)
	if err != nil {
		return nil, err
	}

	filePath := filepath.Join(c.baseDir, sessionID, key+".pcm")

	data, err := os.ReadFile(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrWordNotCached
		}
		return nil, errors.Wrap(err, "failed to read PCM file")
	}

	return data, nil
}

// Path returns the on-disk cache path for (sessionID, wordID) — the same path
// Store writes and Load reads. The generator returns it as its result-map
// value so callers never rebuild the filename mapping (which would drift from
// cacheKey).
func (c *SessionCache) Path(sessionID string, wordID string) string {
	key, err := cacheKey(wordID)
	if err != nil {
		return "" // callers of Path pass through Store-validated wordIDs
	}
	return filepath.Join(c.baseDir, sessionID, key+".pcm")
}

// SessionDir returns filepath.Join(baseDir, sessionID). Used by the generator
// to pass the output directory to pregenerate.
func (c *SessionCache) SessionDir(sessionID string) string {
	return filepath.Join(c.baseDir, sessionID)
}

// Cleanup removes filepath.Join(baseDir, sessionID) via os.RemoveAll.
// Idempotent: returns nil if the directory does not exist.
func (c *SessionCache) Cleanup(sessionID string) error {
	sessionDir := c.SessionDir(sessionID)

	// RemoveAll returns nil if the directory does not exist (idempotent)
	if err := os.RemoveAll(sessionDir); err != nil {
		return errors.Wrap(err, "failed to cleanup session directory")
	}

	return nil
}

// CleanupStale walks baseDir and removes session subdirectories whose
// modification time is older than maxAge. Uses filepath.WalkDir for efficiency.
// Errors removing individual directories are logged but do not stop the walk
// (best-effort cleanup per D discretion: "session directory cleanup 30 minutes
// after session ends").
func (c *SessionCache) CleanupStale(maxAge time.Duration) error {
	cutoffTime := time.Now().Add(-maxAge)

	err := filepath.WalkDir(c.baseDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			// Log error but continue walking
			c.logger.Warn("failed to walk path during cleanup", infra.F("path", path), infra.F("error", err))
			return nil
		}

		// Skip the base directory itself
		if path == c.baseDir {
			return nil
		}

		// Only process directories (session directories)
		if !d.IsDir() {
			return nil
		}

		// Check modification time
		info, err := d.Info()
		if err != nil {
			c.logger.Warn("failed to get directory info", infra.F("path", path), infra.F("error", err))
			return nil
		}

		if info.ModTime().Before(cutoffTime) {
			// Directory is stale, remove it
			if err := os.RemoveAll(path); err != nil {
				c.logger.Warn("failed to remove stale session directory", infra.F("path", path), infra.F("error", err))
			} else {
				c.logger.Info("removed stale session directory", infra.F("path", path), infra.F("age", time.Since(info.ModTime())))
			}
		}

		return filepath.SkipDir // Don't recurse into session directories
	})

	return err
}
