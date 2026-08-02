package tts

import (
	"os"
	"path/filepath"
	"time"

	"github.com/pkg/errors"
	"github.com/xiaotian/home-tutor-agent/pkg/infra"
)

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

// Store writes pcm bytes to filepath.Join(baseDir, sessionID, wordID+".pcm").
// Creates parent directories via os.MkdirAll with 0755 permissions. Returns an
// error wrapping os.WriteFile failures.
func (c *SessionCache) Store(sessionID string, wordID string, pcm []byte) error {
	if sessionID == "" {
		return errors.New("sessionID cannot be empty")
	}
	if wordID == "" {
		return errors.New("wordID cannot be empty")
	}

	sessionDir := c.SessionDir(sessionID)
	filePath := filepath.Join(sessionDir, wordID+".pcm")

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

// Load reads filepath.Join(baseDir, sessionID, wordID+".pcm"). If the file
// does not exist (os.IsNotExist), returns ErrWordNotCached. Otherwise returns
// the file bytes.
func (c *SessionCache) Load(sessionID string, wordID string) ([]byte, error) {
	if sessionID == "" {
		return nil, errors.New("sessionID cannot be empty")
	}
	if wordID == "" {
		return nil, errors.New("wordID cannot be empty")
	}

	filePath := filepath.Join(c.baseDir, sessionID, wordID+".pcm")

	data, err := os.ReadFile(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrWordNotCached
		}
		return nil, errors.Wrap(err, "failed to read PCM file")
	}

	return data, nil
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
