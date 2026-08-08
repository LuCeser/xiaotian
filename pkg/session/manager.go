package session

import (
	"errors"

	"github.com/xiaotian/home-tutor-agent/pkg/database"
	"github.com/xiaotian/home-tutor-agent/pkg/fsrs"
	"github.com/xiaotian/home-tutor-agent/pkg/infra"
	"github.com/xiaotian/home-tutor-agent/pkg/tts"
	"github.com/xiaotian/home-tutor-agent/pkg/ws"
)

// Manager tracks active Sessions and runs the global sweeper backstop. This is
// a compilable stub for Plan 01 — Plan 02 fills in the sessions map, Start,
// sweep, cleanupLeaked, NewSession, Lookup, and Push.
type Manager struct {
	hub      *ws.Hub
	fsrsSvc  fsrs.FSRSService
	gen      *tts.Generator
	streamer *tts.Streamer
	repo     database.SessionRepository
	pool     *infra.WorkerPool
	logger   infra.Logger
	cfg      infra.Config
}

// NewManager constructs a Manager with nil-guarded dependencies (D-42 DI shape,
// mirroring NewFSRSService). Plan 02 adds the sessions map, running atomic,
// and the Start/sweep/cleanupLeaked methods.
func NewManager(
	hub *ws.Hub,
	fsrsSvc fsrs.FSRSService,
	gen *tts.Generator,
	streamer *tts.Streamer,
	repo database.SessionRepository,
	pool *infra.WorkerPool,
	logger infra.Logger,
	cfg infra.Config,
) (*Manager, error) {
	if hub == nil {
		return nil, errors.New("session: hub must not be nil")
	}
	if fsrsSvc == nil {
		return nil, errors.New("session: fsrsSvc must not be nil")
	}
	if gen == nil {
		return nil, errors.New("session: gen must not be nil")
	}
	if streamer == nil {
		return nil, errors.New("session: streamer must not be nil")
	}
	if repo == nil {
		return nil, errors.New("session: repo must not be nil")
	}
	if pool == nil {
		return nil, errors.New("session: pool must not be nil")
	}
	if logger == nil {
		logger = infra.NewNoOpLogger()
	}
	return &Manager{
		hub:      hub,
		fsrsSvc:  fsrsSvc,
		gen:      gen,
		streamer: streamer,
		repo:     repo,
		pool:     pool,
		logger:   logger,
		cfg:      cfg,
	}, nil
}

// Compile-time type existence check (Plan 01 stub).
var _ = (*Manager)(nil)
