package session_test

import (
	"context"
	"sync"

	"github.com/xiaotian/home-tutor-agent/pkg/database"
	"github.com/xiaotian/home-tutor-agent/pkg/fsrs"
	"github.com/xiaotian/home-tutor-agent/pkg/pybridge"
	"github.com/xiaotian/home-tutor-agent/pkg/session"
	"github.com/xiaotian/home-tutor-agent/pkg/tts"
	"github.com/xiaotian/home-tutor-agent/pkg/ws"
)

// fakeFSRSService implements fsrs.FSRSService for tests. Shared across all
// Phase 7 plans: configurable canned results + thread-safe call counters
// (Plan 02 exercises it from the event-loop goroutine).
type fakeFSRSService struct {
	mu sync.Mutex

	dueCards  []pybridge.DueCard
	selectErr error
	selectCnt int

	unlockCnt    int
	unlockOpts   fsrs.UnlockOpts
	unlockCtxErr error // records ctx.Err() of the UnlockCards ctx (Pitfall 1 test)

	updateRatingsCnt int
}

func (f *fakeFSRSService) SelectDueCards(ctx context.Context, opts fsrs.SelectOpts) ([]pybridge.DueCard, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.selectCnt++
	return f.dueCards, f.selectErr
}

func (f *fakeFSRSService) UpdateRatings(ctx context.Context, ratings map[string]string, category string) (pybridge.ReviewResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.updateRatingsCnt++
	return pybridge.ReviewResult{}, nil
}

func (f *fakeFSRSService) UnlockCards(ctx context.Context, opts fsrs.UnlockOpts) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.unlockCnt++
	f.unlockOpts = opts
	f.unlockCtxErr = ctx.Err()
	return 1, nil
}

// thread-safe counter accessors (tests read from outside the lock-holding path).
func (f *fakeFSRSService) SelectCount() int { f.mu.Lock(); defer f.mu.Unlock(); return f.selectCnt }
func (f *fakeFSRSService) UnlockCount() int { f.mu.Lock(); defer f.mu.Unlock(); return f.unlockCnt }
func (f *fakeFSRSService) UnlockOpts() fsrs.UnlockOpts {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.unlockOpts
}
func (f *fakeFSRSService) UnlockCtxErr() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.unlockCtxErr
}
func (f *fakeFSRSService) UpdateRatingsCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.updateRatingsCnt
}

// Compile-time interface satisfaction (D-43).
var _ fsrs.FSRSService = (*fakeFSRSService)(nil)

// fakeSessionRepository implements database.SessionRepository for tests with an
// in-memory map + thread-safe counters.
type fakeSessionRepository struct {
	mu sync.Mutex

	sessions map[int64]database.Session
	nextID   int64

	createCnt      int
	updateStateCnt int
	appendEventCnt int
	createErr      error
}

func newFakeSessionRepository() *fakeSessionRepository {
	return &fakeSessionRepository{sessions: make(map[int64]database.Session), nextID: 1}
}

func (r *fakeSessionRepository) Create(ctx context.Context, s database.Session) (int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.createCnt++
	if r.createErr != nil {
		return 0, r.createErr
	}
	id := r.nextID
	r.nextID++
	r.sessions[id] = s
	return id, nil
}

func (r *fakeSessionRepository) UpdateState(ctx context.Context, id int64, state database.SessionState) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.updateStateCnt++
	if s, ok := r.sessions[id]; ok {
		s.State = state
		r.sessions[id] = s
	}
	return nil
}

func (r *fakeSessionRepository) AppendEvent(ctx context.Context, event database.SessionEvent) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.appendEventCnt++
	return nil
}

func (r *fakeSessionRepository) GetByID(ctx context.Context, id int64) (database.Session, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if s, ok := r.sessions[id]; ok {
		return s, nil
	}
	return database.Session{}, database.ErrSessionNotFound
}

func (r *fakeSessionRepository) CreateCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.createCnt
}
func (r *fakeSessionRepository) UpdateStateCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.updateStateCnt
}
func (r *fakeSessionRepository) AppendEventCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.appendEventCnt
}

var _ database.SessionRepository = (*fakeSessionRepository)(nil)

// fakeFrameSender implements session.FrameSender for tests: captures sent
// messages + a configurable error.
type fakeFrameSender struct {
	mu      sync.Mutex
	sent    [][]byte
	sendErr error
}

func (s *fakeFrameSender) Send(msg []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sent = append(s.sent, msg)
	return s.sendErr
}

func (s *fakeFrameSender) Sent() [][]byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([][]byte, len(s.sent))
	copy(out, s.sent)
	return out
}

var _ session.FrameSender = (*fakeFrameSender)(nil)

// fakeTTSGenerator implements session.TTSSessionGenerator for tests: configurable
// error from GenerateForSession (for the D-10 init-path unlock test) + call
// counter. *tts.Generator satisfies the interface structurally in production.
type fakeTTSGenerator struct {
	mu           sync.Mutex
	genErr       error
	genCalls     int
	cleanupCalls int
}

func (g *fakeTTSGenerator) GenerateForSession(ctx context.Context, sessionID string, words []string) (map[string]string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.genCalls++
	if g.genErr != nil {
		return nil, g.genErr
	}
	result := make(map[string]string, len(words))
	for _, w := range words {
		result[w] = "/fake/" + w + ".pcm"
	}
	return result, nil
}

func (g *fakeTTSGenerator) CleanupSession(ctx context.Context, sessionID string) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.cleanupCalls++
	return nil
}

func (g *fakeTTSGenerator) GenCalls() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.genCalls
}

func (g *fakeTTSGenerator) CleanupCalls() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.cleanupCalls
}

var _ session.TTSSessionGenerator = (*fakeTTSGenerator)(nil)

// fakeStreamer implements session.SessionStreamer for tests. Records call count
// + configurable error. *tts.Streamer satisfies the interface structurally in
// production.
type fakeStreamer struct {
	mu          sync.Mutex
	streamCalls int
	streamErr   error
}

func (f *fakeStreamer) StreamWord(ctx context.Context, client tts.Sender, sessionID string, wordID string, startSeq uint32) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.streamCalls++
	return f.streamErr
}

// ClearSeqCounter satisfies the widened SessionStreamer interface (D-11 teardown
// hook). No-op for tests — the production *tts.Streamer clears its per-session
// counter here.
func (f *fakeStreamer) ClearSeqCounter(sessionID string) {
	// no-op
}

func (f *fakeStreamer) StreamCalls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.streamCalls
}

var _ session.SessionStreamer = (*fakeStreamer)(nil)

// fakeHub implements session.SessionHub for tests. Records Unregister calls.
// *ws.Hub satisfies the interface structurally in production.
type fakeHub struct {
	mu              sync.Mutex
	unregisterCalls int
}

func (f *fakeHub) Unregister(c *ws.Client) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.unregisterCalls++
}

func (f *fakeHub) UnregisterCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.unregisterCalls
}

var _ session.SessionHub = (*fakeHub)(nil)
