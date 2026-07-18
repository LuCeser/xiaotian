# Requirements: Home Tutor Agent

**Defined:** 2025-07-18
**Core Value:** Students receive personalized, real-time dictation practice with immediate feedback while parents get detailed progress tracking and the ability to influence the learning algorithm through their reviews.

## v1 Requirements

Requirements for Phase 1 implementation. Each maps to roadmap phases.

### Vocabulary Management

- [ ] **VOC-01**: Parent can add vocabulary words with Chinese translations, pronunciations, and example sentences
- [ ] **VOC-02**: Parent can organize words into units and lessons
- [ ] **VOC-03**: Parent can configure FSRS parameters (difficulty, retention interval)
- [ ] **VOC-04**: System stores vocabulary data in SQLite database with proper schema

### FSRS Integration

- [ ] **FSRS-01**: System automatically selects due words based on spaced repetition algorithm
- [ ] **FSRS-02**: System supports configurable word count limits (20-30 words default)
- [ ] **FSRS-03**: System implements fetch-lock pattern for session isolation
- [ ] **FSRS-04**: System updates FSRS parameters based on parent feedback ratings

### Real-time Dictation

- [ ] **DICT-01**: Student can request dictation session via button ("I want to dictate 30 words")
- [ ] **DICT-02**: System initializes session with selected due words
- [ ] **DICT-03**: System delivers individual word prompts via TTS audio streaming
- [ ] **DICT-04**: System maintains session state (IDLE → LISTENING → DICTATING → WAITING_PARENT → FEEDBACK)
- [ ] **DICT-05**: System achieves <1.5 second latency for audio responses

### Parent Feedback

- [ ] **FEED-01**: Parent can review completed dictation session results
- [ ] **FEED-02**: Parent can rate each word performance (good/hard/again/easy)
- [ ] **FEED-03**: System submits feedback ratings to FSRS algorithm for parameter updates
- [ ] **FEED-04**: System displays progress statistics and vocabulary retention metrics

### WebSocket Communication

- [ ] **WS-01**: System establishes persistent WebSocket connection with iPad browser
- [ ] **WS-02**: System handles binary audio frames (640B for 20ms@16kHz PCM)
- [ ] **WS-03**: System implements proper session registration and cleanup
- [ ] **WS-04**: System manages concurrent connections with backpressure handling

### Resource Management

- [ ] **RES-01**: System operates within 150MB memory budget for Go service
- [ ] **RES-02**: System implements audio buffer pool with sync.Pool for frame reuse
- [ ] **RES-03**: System enforces connection limits (100 concurrent sessions max)
- [ ] **RES-04**: System uses drop-oldest strategy for overflow situations

### Python Integration

- [ ] **PY-01**: System integrates with existing vocab_manager.py CLI
- [ ] **PY-02**: System executes subprocess commands for word selection (due command)
- [ ] **PY-03**: System executes subprocess commands for FSRS updates (review command)
- [ ] **PY-04**: System integrates with existing pregenerate_dictation.py for TTS generation

### Web Interface

- [ ] **WEB-01**: Parent interface for vocabulary management (add/edit/organize)
- [ ] **WEB-02**: Student interface for dictation session requests and progress display
- [ ] **WEB-03**: Parent review interface for post-dictation feedback
- [ ] **WEB-04**: Responsive design optimized for iPad Safari browser

### Error Handling

- [ ] **ERR-01**: System gracefully handles WebSocket disconnections
- [ ] **ERR-02**: System provides meaningful error messages to parents and students
- [ ] **ERR-03**: System implements timeout-based session cleanup
- [ ] **ERR-04**: System logs errors with structured logging for debugging

## v2 Requirements

Deferred to future release. Not included in Phase 1 roadmap.

### Photo OCR

- **OCR-01**: System can extract vocabulary words from photos of textbook pages
- **OCR-02**: Parent can upload photos for automatic vocabulary entry
- **OCR-03**: System displays OCR confidence scores and editing interface

### Digital Avatar

- **AVATAR-01**: System integrates digital avatar for visual teaching presence
- **AVATAR-02**: Avatar can lip-sync with TTS audio output
- **AVATAR-03**: Avatar can display emotions and gestures based on context

## Out of Scope

Explicitly excluded. Documented to prevent scope creep.

| Feature | Reason |
|---------|--------|
| Mobile native apps | iPad web browser approach reduces development complexity; mobile can be added later |
| Multi-language support | English-only for Phase 1; framework should support future language additions |
| Advanced analytics dashboard | Basic statistics sufficient for Phase 1; detailed analytics can be v2 feature |
| Social features | Individual learning focus; social comparison/features not needed for MVP |
| Content marketplace | Private family/educational use; no content sharing or marketplace planned |

## Traceability

Which phases cover which requirements. Updated during roadmap creation.

| Requirement | Phase | Status |
|-------------|-------|--------|
| VOC-01 | Phase 9 | Pending |
| VOC-02 | Phase 9 | Pending |
| VOC-03 | Phase 9 | Pending |
| VOC-04 | Phase 4 | Pending |
| FSRS-01 | Phase 5 | Pending |
| FSRS-02 | Phase 5 | Pending |
| FSRS-03 | Phase 5 | Pending |
| FSRS-04 | Phase 5 | Pending |
| DICT-01 | Phase 8 | Pending |
| DICT-02 | Phase 7 | Pending |
| DICT-03 | Phase 6 | Pending |
| DICT-04 | Phase 7 | Pending |
| DICT-05 | Phase 6 | Pending |
| FEED-01 | Phase 10 | Pending |
| FEED-02 | Phase 10 | Pending |
| FEED-03 | Phase 10 | Pending |
| FEED-04 | Phase 10 | Pending |
| WS-01 | Phase 2 | Pending |
| WS-02 | Phase 2 | Pending |
| WS-03 | Phase 2 | Pending |
| WS-04 | Phase 2 | Pending |
| RES-01 | Phase 1 | Pending |
| RES-02 | Phase 11 | Pending |
| RES-03 | Phase 1 | Pending |
| RES-04 | Phase 1 | Pending |
| PY-01 | Phase 3 | Pending |
| PY-02 | Phase 3 | Pending |
| PY-03 | Phase 3 | Pending |
| PY-04 | Phase 3 | Pending |
| WEB-01 | Phase 9 | Pending |
| WEB-02 | Phase 8 | Pending |
| WEB-03 | Phase 10 | Pending |
| WEB-04 | Phase 8 | Pending |
| ERR-01 | Phase 11 | Pending |
| ERR-02 | Phase 11 | Pending |
| ERR-03 | Phase 7 | Pending |
| ERR-04 | Phase 1 | Pending |

**Coverage:**
- v1 requirements: 32 total
- Mapped to phases: 32 (100% coverage ✓)
- Unmapped: 0

---
*Requirements defined: 2025-07-18*
*Last updated: 2025-07-18 after roadmap creation*
