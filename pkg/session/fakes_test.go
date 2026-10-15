package session_test

import (
	"context"
	"strings"
	"sync"

	"github.com/xiaotian/home-tutor-agent/pkg/database"
	"github.com/xiaotian/home-tutor-agent/pkg/fsrs"
	"github.com/xiaotian/home-tutor-agent/pkg/infra"
	"github.com/xiaotian/home-tutor-agent/pkg/pybridge"
	"github.com/xiaotian/home-tutor-agent/pkg/session"
	"github.com/xiaotian/home-tutor-agent/pkg/tts"
	"github.com/xiaotian/home-tutor-agent/pkg/ws"
)

// fakeFSRSService implements fsrs.FSRSService for tests. Shared across all
// Phase 7 plans: configurable canned results + thread-safe call counters
// (Plan 02 exercises it from the event-loop goroutine).
type fakeFSRSService struct {
	selectErr    error
	unlockCtxErr error // records ctx.Err() of the UnlockCards ctx (Pitfall 1 test)
	unlockOpts   fsrs.UnlockOpts

	dueCards  []pybridge.DueCard
	selectCnt int

	unlockCnt int

	updateRatingsCnt int
	mu               sync.Mutex
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
	createErr error

	sessions map[int64]database.Session
	results  []database.SessionResult
	nextID   int64

	createCnt      int
	updateStateCnt int
	appendEventCnt int
	mu             sync.Mutex
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

// results feeds ListRecentResults (Ticket 06 trends); tests that don't care
// leave it empty.
func (r *fakeSessionRepository) ListRecentResults(ctx context.Context, limit int) ([]database.SessionResult, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if limit <= 0 || len(r.results) == 0 {
		return nil, nil
	}
	return r.results, nil
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

// fakeTTSGenerator implements session.TTSSessionGenerator for tests: configurable
// error from GenerateForSession (for the D-10 init-path unlock test) + call
// counter. GenerateText (Ticket 06 spoken feedback) captures its arguments;
// textErr simulates a synthesis failure without touching the word pipeline.
// source feeds the DICT-03 source annotation (default volcengine when unset);
// *tts.Generator satisfies the interface structurally in production.
type fakeTTSGenerator struct {
	genErr error

	textErr      error
	source       tts.AudioSource
	lastSource   tts.AudioSource
	lastText     string
	lastLang     string
	genCalls     int
	cleanupCalls int
	textCalls    int
	mu           sync.Mutex
}

func (g *fakeTTSGenerator) GenerateText(ctx context.Context, sessionID, text, language string) (string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.textCalls++
	g.lastText = text
	g.lastLang = language
	if g.textErr != nil {
		return "", g.textErr
	}
	return "/fake/feedback.pcm", nil
}

func (g *fakeTTSGenerator) TextCalls() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.textCalls
}

func (g *fakeTTSGenerator) LastText() (string, string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.lastText, g.lastLang
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

// GenerateForSessionWithSource satisfies the widened DICT-03 seam by
// delegating to GenerateForSession (so GenCalls counts stay meaningful) and
// reporting the configured source.
func (g *fakeTTSGenerator) GenerateForSessionWithSource(ctx context.Context, sessionID string, words []string) (map[string]string, tts.AudioSource, error) {
	paths, err := g.GenerateForSession(ctx, sessionID, words)
	g.mu.Lock()
	defer g.mu.Unlock()
	g.lastSource = g.source
	return paths, g.source, err
}

// LastSource returns the source reported by the most recent WithSource call.
func (g *fakeTTSGenerator) LastSource() tts.AudioSource {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.lastSource
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
// + configurable error + the last (sessionID, wordID) pair (Ticket 06 feedback
// tests assert the streamed key). *tts.Streamer satisfies the interface
// structurally in production.
type fakeStreamer struct {
	streamErr     error
	lastSessionID string
	lastWordID    string
	streamCalls   int
	mu            sync.Mutex
}

func (f *fakeStreamer) StreamWord(ctx context.Context, client tts.Sender, sessionID string, wordID string, startSeq uint32) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.streamCalls++
	f.lastSessionID = sessionID
	f.lastWordID = wordID
	return f.streamErr
}

func (f *fakeStreamer) LastWordID() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.lastWordID
}

func (f *fakeStreamer) LastSessionID() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.lastSessionID
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

// recordingLogger implements infra.Logger for tests: appends every message
// text to an in-memory slice so tests can poll for a specific entry (used to
// observe the event loop's stale-event drop path, whose debug log is the only
// synchronous signal that an event was consumed).
type recordingLogger struct {
	msgs []string
	mu   sync.Mutex
}

func (l *recordingLogger) Debug(msg string, fields ...infra.Field) { l.record(msg) }
func (l *recordingLogger) Info(msg string, fields ...infra.Field)  { l.record(msg) }
func (l *recordingLogger) Warn(msg string, fields ...infra.Field)  { l.record(msg) }
func (l *recordingLogger) Error(msg string, fields ...infra.Field) { l.record(msg) }

// With returns the receiver itself, dropping the context fields — tests only
// assert on message text, and returning the receiver guarantees every entry
// lands in the same slice.
func (l *recordingLogger) With(fields ...infra.Field) infra.Logger { return l }

// Sync is a no-op for an in-memory logger.
func (l *recordingLogger) Sync() error { return nil }

// record appends a message text under the lock.
func (l *recordingLogger) record(msg string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.msgs = append(l.msgs, msg)
}

// hasMessage reports whether any recorded message contains sub.
func (l *recordingLogger) hasMessage(sub string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, m := range l.msgs {
		if strings.Contains(m, sub) {
			return true
		}
	}
	return false
}

var _ infra.Logger = (*recordingLogger)(nil)
