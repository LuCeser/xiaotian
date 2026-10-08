# Session ID "0" Bug - Root Cause Analysis

## Bug Summary
**Error:** "update state for session 0: session not found"  
**Impact:** Multi-word dictation fails after first word completes  
**Root Cause:** Session.dbID field is never set during initialization

## Code Analysis - Step by Step

### Step 1: Session Initialization (pkg/session/session.go:192-213)
```go
s := &Session{
    id:               sessionID,
    connID:           connID,
    state:            database.StateIdle,
    wordQueue:        cards,
    wordIndex:        0,
    deadline:         time.Now().Add(cfg.SessionTimeout.IdleStateTimeout),
    lockCategory:     resolvedCategory,
    lockUnit:         resolvedUnit,
    client:           client,
    fsrsSvc:          fsrsSvc,
    gen:              gen,
    streamer:         streamer,
    repo:             repo,
    pool:             pool,
    hub:              hub,
    logger:           logger,
    cfg:              cfg,
    events:           make(chan SessionEvent, eventBufferSize),
    currentWordID:    "",         // Initialize current word ID tracker (08-06)
    sessionStartTime: time.Now(), // Track session start for progress frames (08-06)
}
```

**❌ MISSING:** `dbID` field is NOT initialized (defaults to 0 in Go)

### Step 2: Asynchronous Database Creation (pkg/session/session.go:216)
```go
s.auditCreate()
```

This calls `pkg/session/audit.go:25-38`:
```go
func (s *Session) auditCreate() {
    if s.pool == nil {
        return
    }
    if err := s.pool.Submit(func(ctx context.Context) error {
        _, err := s.repo.Create(ctx, database.Session{
            ConnectionID: s.connID,
            State:        database.StateIdle,
            WordCount:    len(s.wordQueue),
        })
        return err
    }); err != nil {
        s.logger.Warn("session: audit create dropped (pool full)", ...)
    }
}
```

**❌ PROBLEM:** The database ID returned by `repo.Create()` is lost because:
1. Task runs asynchronously on worker goroutine
2. Return value is not captured back into `s.dbID`
3. Code comment at audit.go:16-20 explicitly states this is by design for Phase 7

### Step 3: Subsequent Database Operations (pkg/session/audit.go:43-57)
```go
func (s *Session) auditState(newState database.SessionState) {
    if s.pool == nil {
        return
    }
    if err := s.pool.Submit(func(ctx context.Context) error {
        return s.repo.UpdateState(ctx, s.dbID, newState)  // ❌ Uses dbID=0
    }); err != nil {
        s.logger.Warn("session: audit state dropped (pool full)", ...)
    }
}
```

**❌ PROBLEM:** `s.dbID` is still 0, causing database update to fail

### Step 4: Database Update Failure (pkg/database/session_repo.go:138-163)
```go
func (r *sqliteSessionRepository) UpdateState(ctx context.Context, id int64, state SessionState) error {
    result, err := r.db.ExecContext(ctx,
        "UPDATE sessions SET state = ?, updated_at = datetime('now') WHERE id = ?",
        string(state),
        id,  // ❌ id=0
    )
    // ... error handling ...
    if rows == 0 {
        return errors.Wrap(ErrSessionNotFound, fmt.Sprintf("update state for session %d", id))  
        // ❌ Returns: "update state for session 0: session not found"
    }
    return nil
}
```

## Evidence from Code Comments

**pkg/session/audit.go:16-20** explicitly documents this design decision:
```go
// The returned dbID from repo.Create is not captured back into s.dbID because
// the Task runs asynchronously on a worker goroutine; writing to a field read
// by the event loop would violate the single-writer discipline (D-02). The
// dbID stays 0 for Phase 7; a future phase may add a synchronous init-time
// Create or a channel-based write-back if accurate dbID becomes necessary.
```

## Fix Required

The design assumption that "accurate dbID becomes necessary" in a future phase was incorrect. The system needs `s.dbID` immediately for:

1. **auditState** - State transitions need correct database ID
2. **auditEvent** - Event logging needs correct session reference  
3. **Session management** - Any database operation on the session

**Solution Options:**

1. **Synchronous Create:** Call `repo.Create` synchronously during `NewSession` and capture the returned ID
2. **Channel Write-back:** Implement a channel to pass the database ID back from worker to Session struct
3. **Query by ConnectionID:** Change database operations to query by `connID` instead of `dbID`

## Test Scenario

**Reproduction Steps:**
1. Create session: `NewSession(...)` → generates UUID, creates Session struct with `dbID=0`
2. Async `auditCreate` → creates database record with actual ID (e.g., ID=42)
3. State transition: `auditState(StateListening)` → calls `UpdateState(0, StateListening)`
4. Database query: `UPDATE sessions SET state = 'listening' WHERE id = 0` → 0 rows affected
5. Error returned: `"update state for session 0: session not found"`

**Expected Fix Behavior:**
1. Create session: `NewSession(...)` → generates UUID, creates Session struct
2. **Synchronous** `repo.Create` → creates database record with actual ID (e.g., ID=42)
3. **Capture ID:** `s.dbID = 42` ← This step is missing!
4. State transition: `auditState(StateListening)` → calls `UpdateState(42, StateListening)`
5. Database query: `UPDATE sessions SET state = 'listening' WHERE id = 42` → 1 row affected
6. Success ✅

## Conclusion

The bug is confirmed through code analysis. The architectural design of asynchronous audit operations conflicts with the immediate need for the database session ID in subsequent operations. The fix requires making session creation synchronous or implementing a write-back mechanism.