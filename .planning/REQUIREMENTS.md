# Requirements: Home Tutor Agent

**Defined:** 2025-07-18
**Core Value:** Students receive personalized, real-time dictation practice with immediate feedback while parents get detailed progress tracking and the ability to influence the learning algorithm through their reviews.

## v1 Requirements

Requirements for Phase 1 implementation. Each maps to roadmap phases.

### Vocabulary Management

- [ ] **VOC-01**: Parent can add vocabulary words with Chinese translations, pronunciations, and example sentences
- [ ] **VOC-02**: Parent can organize words into units and lessons
- [ ] **VOC-03**: Parent can configure FSRS parameters (difficulty, retention interval)
- [x] **VOC-04**: System stores vocabulary data in SQLite database with proper schema

### FSRS Integration

- [x] **FSRS-01**: System automatically selects due words based on spaced repetition algorithm
- [x] **FSRS-02**: System supports configurable word count limits (20-30 words default)
- [x] **FSRS-03**: System implements fetch-lock pattern for session isolation
- [x] **FSRS-04**: System updates FSRS parameters based on parent feedback ratings

### Real-time Dictation

- [x] **DICT-01**: Student can request dictation session via button ("I want to dictate 30 words")
- [x] **DICT-02**: System initializes session with selected due words
- [x] **DICT-03**: System delivers individual word prompts via TTS audio streaming
- [x] **DICT-04**: System maintains session state (IDLE → LISTENING → DICTATING → WAITING_PARENT → FEEDBACK)
- [x] **DICT-05**: System achieves <1.5 second latency for audio responses
- [ ] **DICT-06**: System provides adaptive writing time (8-15 seconds) based on word length and complexity
- [ ] **DICT-07**: Student can control dictation pace with Next Word / Repeat Word / Pause/Resume buttons

> **2026-08-23 校准**（speech-recognition-redesign ticket 集为唯一真相源）：DICT-03 的音频源由 Python-bridge TTS 迁移至小米 Mimo 直连（`pkg/mimo`，ticket 01 已交付验证）；听写循环采用**学生控速模型**，取代 Phase 8.1 原后端自动推进设计。

### Parent Feedback

- [ ] **FEED-01**: Parent can review completed dictation session results with auto-grading analysis attached (error patterns, suggestions, confidence)
- [ ] **FEED-02**: Parent can verify or override each word's auto-assessed performance (good/hard/again/easy, pre-filled from Phase 8.5 auto-grading)
- [ ] **FEED-03**: Confirmed feedback ratings update FSRS algorithm parameters (RunReview path unchanged)
- [ ] **FEED-04**: System displays progress statistics and vocabulary retention metrics

### Voice Commands

- [ ] **VOICE-01**: System captures student audio via Web Audio API on iPad Safari for command recognition
- [ ] **VOICE-02**: Xiaomi Mimo ASR transcribes student voice commands accurately, including children's voices (ages 6-12)
- [ ] **VOICE-03**: LLM intent analysis maps varied natural expressions to the 4 core commands ("好了", "重读", "暂停", "不会写") and triggers correct session state transitions
- [ ] **VOICE-04**: Failed recognition falls back to button controls

### Answer Submission

- [ ] **SUBMIT-01**: Students can capture photos of written answers via iPad camera on a Safari-optimized interface
- [ ] **SUBMIT-02**: OCR extracts text with ≥85% accuracy for typical elementary handwriting, with confidence scores
- [ ] **SUBMIT-03**: Manual input available when OCR fails or confidence is low; system validates answer format and completeness
- [ ] **SUBMIT-04**: Student submits all answers together after dictation (write all → submit all → grade all); session FSM handles DICTATING → SUBMISSION → FEEDBACK
- [ ] **SUBMIT-05**: Batch submission processes multiple words efficiently with progress indicators, supporting both OCR and manual input

### Automatic Grading

- [ ] **GRADE-01**: System performs character-level comparison between submitted and correct answers, tolerant of minor variations and common mistakes
- [ ] **GRADE-02**: Detailed analysis reports show error patterns, character-level error highlighting, and learning suggestions
- [ ] **GRADE-03**: Final scores and encouraging feedback are read aloud via TTS
- [ ] **GRADE-04**: Results stored for FSRS updates and parent review; performance trends tracked over multiple sessions

### WebSocket Communication

- [x] **WS-01**: System establishes persistent WebSocket connection with iPad browser
- [x] **WS-02**: System handles binary audio frames (640B for 20ms@16kHz PCM)
- [x] **WS-03**: System implements proper session registration and cleanup
- [x] **WS-04**: System manages concurrent connections with backpressure handling

### Resource Management

- [x] **RES-01**: System operates within 150MB memory budget for Go service
- [ ] **RES-02**: System implements audio buffer pool with sync.Pool for frame reuse
- [x] **RES-03**: System enforces connection limits (100 concurrent sessions max)
- [x] **RES-04**: System uses drop-oldest strategy for overflow situations

### Python Integration

- [x] **PY-01**: System integrates with existing vocab_manager.py CLI
- [x] **PY-02**: System executes subprocess commands for word selection (due command)
- [x] **PY-03**: System executes subprocess commands for FSRS updates (review command)
- [x] **PY-04**: System integrates with existing pregenerate_dictation.py for TTS generation

### Web Interface

- [ ] **WEB-01**: Parent interface for vocabulary management (add/edit/organize)
- [x] **WEB-02**: Student interface for dictation session requests and progress display
  - **Completed Phase 08-06** (2026-08-23): Backend now emits progress_update, word_complete, and session_complete control frames. Frontend already implements play_word/next_word triggers. Real-time progress bar updates and gapless audio playback are now functional.
- [ ] **WEB-03**: Parent review interface for post-dictation feedback
- [x] **WEB-04**: Responsive design optimized for iPad Safari browser

### Error Handling

- [ ] **ERR-01**: System gracefully handles WebSocket disconnections
- [ ] **ERR-02**: System provides meaningful error messages to parents and students
- [x] **ERR-03**: System implements timeout-based session cleanup
- [x] **ERR-04**: System logs errors with structured logging for debugging

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
| VOC-04 | Phase 4 | Complete |
| FSRS-01 | Phase 5 | Complete |
| FSRS-02 | Phase 5 | Complete |
| FSRS-03 | Phase 5 | Complete |
| FSRS-04 | Phase 5 | Complete |
| DICT-01 | Phase 8 | Pending |
| DICT-02 | Phase 7 | Complete |
| DICT-03 | Phase 6 | Complete |
| DICT-04 | Phase 7 | Complete |
| DICT-05 | Phase 6 | Complete |
| DICT-06 | Phase 8.1 | Pending |
| DICT-07 | Phase 8.1 | Pending |
| VOICE-01 | Phase 8.2 | Pending |
| VOICE-02 | Phase 8.2 | Pending |
| VOICE-03 | Phase 8.2 | Pending |
| VOICE-04 | Phase 8.2 | Pending |
| SUBMIT-01 | Phase 8.3 | Pending |
| SUBMIT-02 | Phase 8.3 | Pending |
| SUBMIT-03 | Phase 8.3 | Pending |
| SUBMIT-04 | Phase 8.4 | Pending |
| SUBMIT-05 | Phase 8.4 | Pending |
| GRADE-01 | Phase 8.5 | Pending |
| GRADE-02 | Phase 8.5 | Pending |
| GRADE-03 | Phase 8.5 | Pending |
| GRADE-04 | Phase 8.5 / Phase 10 | Pending |
| FEED-01 | Phase 10 | Pending |
| FEED-02 | Phase 10 | Pending |
| FEED-03 | Phase 10 | Pending |
| FEED-04 | Phase 10 | Pending |
| WS-01 | Phase 2 | Complete |
| WS-02 | Phase 2 | Complete |
| WS-03 | Phase 2 | Complete |
| WS-04 | Phase 2 | Complete |
| RES-01 | Phase 1 | Complete |
| RES-02 | Phase 11 | Pending |
| RES-03 | Phase 1 | Complete |
| RES-04 | Phase 1 | Complete |
| PY-01 | Phase 3 | Complete |
| PY-02 | Phase 3 | Complete |
| PY-03 | Phase 3 | Complete |
| PY-04 | Phase 3 | Complete |
| WEB-01 | Phase 9 | Pending |
| WEB-02 | Phase 8 | Complete |
| WEB-03 | Phase 10 | Pending |
| WEB-04 | Phase 8 | Complete |
| ERR-01 | Phase 11 | Pending |
| ERR-02 | Phase 11 | Pending |
| ERR-03 | Phase 7 | Complete |
| ERR-04 | Phase 1 | Complete |

**Coverage:**

- v1 requirements: 47 total (32 original + 15 added 2026-08-23 from speech-recognition-redesign calibration)
- Mapped to phases: 47 (100% coverage ✓)
- Unmapped: 0

---
*Requirements defined: 2025-07-18*
*Last updated: 2026-08-23 — calibrated to speech-recognition-redesign ticket set (single source of truth): added DICT-06/07, VOICE-01..04, SUBMIT-01..05, GRADE-01..04; re-sourced FEED-01..03 to auto-grading + parent confirm/override*
