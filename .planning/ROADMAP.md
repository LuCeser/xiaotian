# Roadmap: Home Tutor Agent

## Overview

Journey from greenfield to production real-time dictation tutor system. Start with infrastructure foundation and resource management, build WebSocket communication layer, integrate existing Python assets (vocab_manager.py, pregenerate_dictation.py), implement FSRS algorithm and database layer, create TTS audio streaming with <1.5s latency, deliver complete session management workflow, build responsive web interfaces optimized for iPad, implement parent feedback system, polish user experience with error handling and progress tracking, and finally validate complete end-to-end functionality with performance metrics.

## Phases

**Phase Numbering:**

- Integer phases (1, 2, 3): Planned milestone work
- Decimal phases (2.1, 2.2): Urgent insertions (marked with INSERTED)

Decimal phases appear between their surrounding integers in numeric order.

- [x] **Phase 1: Resource-Constrained Infrastructure** - Foundation setup with 150MB memory budget and monitoring (completed 2026-07-23)
- [x] **Phase 2: WebSocket Communication Layer** - Persistent connections with binary audio frame support (completed 2026-07-19)
- [x] **Phase 3: Python CLI Integration** - Subprocess bridge to vocab_manager and TTS generation (completed 2026-08-01)
- [x] **Phase 4: Database Schema Design** - SQLite schema for vocabulary and FSRS metadata (completed 2026-08-01)
- [x] **Phase 5: FSRS Core Implementation** - Automatic due word selection and parameter updates (completed 2026-08-02)
- [x] **Phase 6: TTS Audio Streaming** - Real-time audio delivery with <1.5s latency (completed 2026-08-08)
- [x] **Phase 7: Session State Management** - Complete session lifecycle and state transitions (completed 2026-08-08)
- [x] **Phase 8: Student Web Interface** - Responsive iPad-optimized student experience (completed 2026-09-27)
- [ ] **Phase 9: Parent Vocabulary Management** - Add/edit/organize vocabulary with FSRS configuration
- [ ] **Phase 10: Parent Feedback System** - Post-dictation review and performance ratings
- [ ] **Phase 11: Error Handling & Resilience** - Graceful disconnections and structured logging
- [ ] **Phase 12: End-to-End Validation** - Complete user flow verification and performance testing

## Phase Details

### Phase 1: Resource-Constrained Infrastructure

**Goal**: Production-ready infrastructure foundation with 1C 1G VPS resource constraints
**Depends on**: Nothing (first phase)
**Requirements**: RES-01, RES-03, RES-04, ERR-04
**Success Criteria** (what must be TRUE):

  1. Go service starts and stays within 150MB memory budget under load
  2. System enforces 100 concurrent session limit with backpressure handling
  3. System uses drop-oldest strategy for buffer overflow without crashes
  4. Structured logging captures all system events with proper error levels

**Plans**: 8/8 plans executed

Plans:
**Wave 1**

- [x] 01-01-PLAN.md — Project skeleton + Make build + CI workflow

**Wave 2** *(blocked on Wave 1 completion)*

- [x] 01-02-PLAN.md — Typed Config struct + layered Viper loader + startup validation
- [x] 01-03-PLAN.md — Structured logging (Zap) + panic recovery middleware (ERR-04)

**Wave 3** *(blocked on Wave 2 completion)*

- [x] 01-04-PLAN.md — Memory monitor + load shedding at 150MB budget (RES-01)
- [x] 01-05-PLAN.md — Buffer pool with drop-oldest overflow strategy (RES-04)
- [x] 01-06-PLAN.md — Connection registry + 100-session limit + token bucket (RES-03)
- [x] 01-07-PLAN.md — Fixed-size worker pool with backpressure signals

**Wave 4** *(blocked on Wave 3 completion)*

- [x] 01-08-PLAN.md — Server coordinator + health/metrics + main.go entry point

**PROJECT_MODE**: mvp

### Phase 2: WebSocket Communication Layer

**Goal**: Persistent WebSocket connections supporting binary audio frames for iPad browsers
**Depends on**: Phase 1
**Requirements**: WS-01, WS-02, WS-03, WS-04
**Success Criteria** (what must be TRUE):

  1. iPad Safari browser establishes persistent WebSocket connection
  2. System handles 640B binary audio frames (20ms@16kHz PCM) without data loss
  3. System properly registers sessions and cleans up connections on disconnect
  4. System manages concurrent connections with proper backpressure handling

**Plans**: 3/3 plans executed

Plans:
**Wave 1**

- [x] 02-01-PLAN.md — Binary frame protocol + CRC-16 codec (WS-02, D-01, D-05~D-11)

**Wave 2** *(blocked on Wave 1 completion)*

- [x] 02-02-PLAN.md — Hub + Client lifecycle + Heartbeat + test helpers (WS-03, D-04, D-13~D-17)

**Wave 3** *(blocked on Wave 2 completion)*

- [x] 02-03-PLAN.md — HTTP Server + Router + end-to-end integration tests (WS-01, WS-04, D-02, D-19)

**PROJECT_MODE**: mvp

### Phase 3: Python CLI Integration

**Goal**: Subprocess bridge to existing vocab_manager.py and pregenerate_dictation.py
**Depends on**: Phase 2
**Requirements**: PY-01, PY-02, PY-03, PY-04
**Success Criteria** (what must be TRUE):

  1. System executes vocab_manager due command and returns word selection results
  2. System executes vocab_manager review command with feedback parameters
  3. System integrates with pregenerate_dictation.py for TTS audio generation
  4. Subprocess errors are captured and surfaced to logging system

**Plans**: 5/5 plans executed

Plans:
**Wave 1**

- [x] 03-01-PLAN.md — PythonConfig 扩展 + vendor 上游脚本 + Makefile py-* target (PY-01, D-03/D-04/D-05)
- [x] 03-02-PLAN.md — Executor 接口 + sentinel 错误 + 解析器 + 信号量 + 黄金 fixture (PY-01)

**Wave 2** *(blocked on Wave 1 completion)*

- [x] 03-03-PLAN.md — Bridge exec 引擎 + 进程组杀死 + 密钥 env 注入 + 结构化日志 + main.go 接线 (PY-01)

**Wave 3** *(blocked on Wave 2 completion)*

- [x] 03-04-PLAN.md — RunDue + RunReview + argv 构造 + 末尾 JSON 提取 (PY-02, PY-03)
- [x] 03-05-PLAN.md — RunPregenerate + 按行路径解析 + flock 串行化 + shell fake (PY-04)

**PROJECT_MODE**: mvp

### Phase 4: Database Schema Design

**Goal**: SQLite database schema for vocabulary, FSRS parameters, and session data
**Depends on**: Phase 1
**Requirements**: VOC-04
**Success Criteria** (what must be TRUE):

  1. SQLite database with proper schema for vocabulary words (Chinese, translations, pronunciations, examples)
  2. Database supports unit and lesson organization structures
  3. Database schema supports FSRS parameter storage (difficulty, retention intervals)
  4. Database operations maintain ACID properties for concurrent access

**Plans**:

- 04-01: Foundation — modernc.org/sqlite driver + CGO_ENABLED=0 lock + DatabaseConfig
- 04-02: FSRS+SQLite ownership ADR (resolves P1-4 architecture review todo)
- 04-03: pkg/database — schema + Open (WAL pragmas) + Migrate + README
- 04-04: SessionRepository + read-only CardRepository + tests
- 04-05: main.go wiring (Open + Migrate + repos + db.Close) — integration vertical slice

Total: 5 plans across 4 waves (Wave 1 has 2 parallel plans: 04-01 + 04-02)

**PROJECT_MODE**: mvp

### Phase 5: FSRS Core Implementation

**Goal**: Automatic due word selection with fetch-lock pattern and feedback-driven parameter updates
**Depends on**: Phase 3, Phase 4
**Requirements**: FSRS-01, FSRS-02, FSRS-03, FSRS-04
**Success Criteria** (what must be TRUE):

  1. System automatically selects due words based on FSRS algorithm
  2. System supports configurable word count limits (20-30 words default)
  3. System implements fetch-lock pattern preventing concurrent session conflicts
  4. Parent feedback ratings (good/hard/again/easy) trigger FSRS parameter updates

**Plans**: 1/2 plans executed

Plans:
**Wave 1**

- [x] 05-01-PLAN.md — FSRSConfig extension (config.go + config_test.go, 5 sync points, per D-03) (FSRS-02, FSRS-03)

**Wave 2** *(blocked on Wave 1 completion)*

- [x] 05-02-PLAN.md — FSRSService orchestration layer (pkg/fsrs) + main.go wiring (FSRS-01, FSRS-02, FSRS-03, FSRS-04, D-01/D-02/D-02-a/D-04/D-05)

**PROJECT_MODE**: mvp

### Phase 6: TTS Audio Streaming

**Goal**: Real-time TTS audio delivery with <1.5 second latency target
**Depends on**: Phase 3
**Requirements**: DICT-03, DICT-05, PY-04
**Success Criteria** (what must be TRUE):

  1. System delivers individual word prompts via streaming TTS audio
  2. System achieves <1.5 second latency from request to audio playback
  3. Audio streaming integrates with WebSocket binary frame protocol
  4. TTS generation errors are caught and reported without session crashes

**Plans**: 4/4 plans executed

Plans:
**Wave 1**

- [x] 06-01-PLAN.md — TTSConfig + pkg/tts types + MP3-to-PCM codec + SessionCache (D-07, D-09, D-10, DICT-03)

**Wave 2** *(blocked on Wave 1 completion)*

- [x] 06-02-PLAN.md — TTS Generator + batch generation via RunPregenerate + 3-retry-skip strategy (PY-04, D-01, D-05, DICT-03)
- [x] 06-03-PLAN.md — Audio Frame Streamer + WS binary frame delivery + prefetch + breakpoint resume (DICT-03, DICT-05, D-03, D-04, D-06)

**Wave 3** *(blocked on Wave 2 completion)*

- [x] 06-04-PLAN.md — End-to-end main.go wiring + control frame handlers + latency validation (DICT-03, DICT-05)

**PROJECT_MODE**: mvp

### Phase 7: Session State Management

**Goal**: Complete session lifecycle with proper state transitions and timeout handling
**Depends on**: Phase 2, Phase 5
**Requirements**: DICT-02, DICT-04, ERR-03
**Success Criteria** (what must be TRUE):

  1. Session initializes with selected due words from FSRS system
  2. System maintains session states: IDLE → LISTENING → DICTATING → WAITING_PARENT → FEEDBACK
  3. Sessions properly transition between states based on user actions and timeouts
  4. Timeout-based cleanup removes stale sessions without resource leaks

**Plans**: 3/3 plans executed

Plans:
**Wave 1**

- [x] 07-01-PLAN.md — pkg/session skeleton + FSM transition table + NewSession init (DICT-02, DICT-04, D-01/D-03/D-05/D-09/D-11)

**Wave 2** *(blocked on Wave 1 completion)*

- [x] 07-02-PLAN.md — Session event loop + D-10 exit defer + per-state timeouts + Manager sweeper (DICT-04, ERR-03, D-02/D-08/D-10/D-13)

**Wave 3** *(blocked on Wave 2 completion)*

- [x] 07-03-PLAN.md — D-04 metrics rewiring + D-06 WorkerPool audit + composition root wiring (ERR-03, D-04/D-06, resolves P0-2 + P1-3)

**PROJECT_MODE**: mvp

### Phase 07.1: Research and fix iOS WebSocket audio playback issues for iPad Safari browsers (INSERTED)

**Goal:** Deliver a working end-to-end iPad Safari dictation flow on real hardware — student opens page → one-tap unlock → system reads words → gapless continuous playback of 20-30 words → student responds → feedback. Backend WS audio pipeline stays; fix is frontend (Web Audio API replacing HTMLAudioElement) + protocol-correctness bug fixes (sequence monotonicity). Not research-only — delivers a tested-on-real-iPad fix.
**Requirements**: TBD (acceptance baseline = CONTEXT.md D-13 real-iPad conditions + D-10/D-11/D-12/D-09 protocol correctness)
**Depends on:** Phase 7
**Plans:** 4/4 plans executed (completed 2026-08-09)
**Verification:** Real-iPad UAT on iPadOS 26.5.2 — core audio playback PASS (D-13 cond. 1 one-tap unlock, cond. 3 per-word); gapless-continuous (Play All) + app-switch recovery not explicitly tested (residual hardening). See 07.1-04-SUMMARY.md.

Plans:
**Wave 1** (parallel — backend protocol foundation)

- [x] 07.1-01-PLAN.md — Streamer D-10/D-11 sequence fixes + ClearSeqCounter + session teardown hook (D-10, D-11, Pitfall 5) [tracer]
- [x] 07.1-02-PLAN.md — Control-frame D-12 monotonic atomic counter + D-09 compression-lock documentation (D-12, D-09)

**Wave 2** *(blocked on Wave 1)*

- [x] 07.1-03-PLAN.md — Frontend Web Audio API rewrite: 3-condition unlock, gapless scheduling, PCM→AudioBuffer, app-switch recovery (D-01, D-02, D-03, D-04, D-05, D-06)

**Wave 3** *(blocked on Wave 2 — real hardware gate)*

- [x] 07.1-04-PLAN.md — Real-iPad UAT against D-13 four conditions + iOS version capture (D-13, D-14)

### Phase 8: Student Web Interface

**Goal**: Responsive iPad-optimized student interface for session requests and progress display
**Depends on**: Phase 7
**Requirements**: DICT-01, WEB-02, WEB-04
**Success Criteria** (what must be TRUE):

  1. Student can request dictation session via button ("I want to dictate 30 words")
  2. Interface displays real-time session progress (current word, completed count)
  3. Responsive design works optimally on iPad Safari browser
  4. Interface handles WebSocket reconnections gracefully

**Plans**: 7/7 plans executed

**Wave 1**

- [x] 08-01-PLAN.md — Static file serving + responsive HTML shell + WebSocket client foundation
- [x] 08-02-PLAN.md — Session request button + state management + WebSocket message handling
- [x] 08-03-PLAN.md — Progress display component + real-time updates + session lifecycle integration
- [x] 08-04-PLAN.md — Web Audio API integration + iPad-responsive polish + production error handling

**Gap Closure Wave**

- [x] 08-05-PLAN.md — Fix static file serving 404 errors (gap G-08-2)
- [x] 08-06-PLAN.md — Backend progress frames (progress_update/word_complete/session_complete) + currentWordId tracking + frontend progress display & audio controller fixes (WEB-02)

**Manual Verification Wave** (requires physical iPad)

- [x] 08-07-PLAN.md — iPad Safari manual UAT: 9 previously blocked tests, portrait/landscape screenshots, real-device audio playback & touch verification (WEB-04)

**PROJECT_MODE**: mvp
**UI hint**: yes

### Phase 8.1: Core Dictation Flow with Mimo TTS (INSERTED)

**Goal:** Student-paced core dictation flow — students hear words one by one via Xiaomi Mimo TTS with adaptive writing time (8-15s by word difficulty) and student controls (Next Word / Repeat Word / Pause/Resume), preserving the traditional teacher-student dictation rhythm. Wires pkg/mimo (ticket 01, verified 2026-08-23) into the session pipeline, building on Phase 08-06's client-driven triggers (play_word/next_word) and progress frames.
**Source of truth:** `.scratch/speech-recognition-redesign/issues/02-core-dictation-flow-tts.md` (user decision 2026-08-23: ticket set is the single source of truth)
**Depends on**: Phase 8
**Requirements**: DICT-01, DICT-03, DICT-05, DICT-06, DICT-07, WEB-02
**Success Criteria** (what must be TRUE):

  1. Student can request dictation session and hear words read via Xiaomi Mimo TTS (pkg/mimo becomes the dictation audio source)
  2. Adaptive writing time (8-15s) computed from word length and complexity
  3. Next Word / Repeat Word / Pause/Resume controls work over the existing WebSocket protocol and session management patterns
  4. Session FSM handles LISTENING → DICTATING → PAUSED transitions
  5. Audio latency <1.5s; real-time progress display advances word-by-word (retains 08-06 progress frames)

**Superseded** (2026-08-23 user decision — ticket set supersedes prior planning):

- Backend auto-advance design (D-01/D-02/D-03 Option A, 08.1-CONTEXT.md) → replaced by the student-paced model
- 08.1-01-PLAN.md / 08.1-02-PLAN.md → not executed, obsolete; superseded by the direct ticket-02 delivery below (no re-plan)
- Retained from 08-06 delivery: progress_update/word_complete/session_complete emission + frontend play_word/next_word triggers (client-driven foundation this phase builds on)

**Note**: Phase 8 UAT test 7 (WebSocket reconnection / session resume) remains blocked and is owned by the existing **Phase 11** (Error Handling & Resilience, success criterion #1: "gracefully handles WebSocket disconnections").

**Plans**: delivered directly as ticket 02 (2026-08-23) — no GSD plan files

Plans:

- [x] Ticket 02 — core dictation flow (Mimo TTS) delivered directly: be653a4 (student-paced flow: adaptive 8-15s writing time, Next/Repeat/Pause-Resume controls) + dual-axis review fixes 3a1fc59 (stale WordComplete stream-generation race), 799de7c (PausedStateTimeout config), f8e5e31 (single-writer invariant docs), d6c046b (DictationControls JSDoc), 45435bb (review cleanup set #5/5), 4f5c644 (final-review cleanup: JSDoc truthfulness x2 + deterministic test wait), 0ff7cca (docs: Phase 8.1 section aligned to delivery reality); issue `.scratch/speech-recognition-redesign/issues/02-core-dictation-flow-tts.md` closed done 2026-08-23, 6/7 acceptance items
- [ ] iPad real-device UAT — the only unchecked acceptance item (<1.5s audio latency end-to-end + real Safari audio/touch); run before or alongside Phase 8.2

**UI hint**: yes

### Phase 8.2: Voice Command Recognition via Mimo ASR (INSERTED)

**Goal:** Students control dictation with natural voice commands ("好了", "重读", "暂停", "不会写") — Xiaomi Mimo ASR transcription + LLM intent analysis replaces the unreliable Web Speech API, with button-control fallback.
**Source of truth:** `.scratch/speech-recognition-redesign/issues/03-voice-command-recognition-intent.md`
**Depends on**: Phase 8.1
**Requirements**: VOICE-01, VOICE-02, VOICE-03, VOICE-04
**Success Criteria** (what must be TRUE):

  1. System captures audio via Web Audio API on iPad Safari and Mimo ASR transcribes student commands accurately
  2. LLM intent analysis maps varied natural expressions (beyond fixed phrases) to the 4 core commands and triggers correct session state transitions
  3. Failed recognition falls back to button controls
  4. Voice control works reliably for children's voices (ages 6-12)

**Superseded** (2026-09-12 — delivered directly via ticket set):

- ticket 03 → done 2026-08-31 + live verified 2026-09-08 (见 .scratch/speech-recognition-redesign/issues/03-voice-command-recognition-intent.md Status 行)

**Plans**: delivered directly as ticket 03 (2026-08-31) — no GSD plan files

Plans:

- [x] Ticket 03 — voice command recognition (Mimo ASR + LLM intent, button fallback) delivered directly: b178d99, 825d40d, 4594e59, c554f8a, 76ef58f, e598c60, 22a9ddc + live 验证 e3e7736, ab9f762 (2026-09-08); issue closed done 2026-08-31, live verified 2026-09-08

### Phase 8.3: Photo OCR Answer Submission (INSERTED)

**Goal:** Students submit written answers by photographing their work with the iPad camera; OCR extracts the text with confidence scores, manual input remains the fallback. Preserves traditional handwriting practice with modern automation.
**Source of truth:** `.scratch/speech-recognition-redesign/issues/04-photo-ocr-answer-submission.md`
**Depends on**: Phase 8.1
**Requirements**: SUBMIT-01, SUBMIT-02, SUBMIT-03
**Success Criteria** (what must be TRUE):

  1. Students can capture photos of written answers via iPad camera on a Safari-optimized interface
  2. OCR extracts text with ≥85% accuracy for typical elementary handwriting, results include confidence scores
  3. Manual input option available when OCR confidence is low or fails; system validates submitted answer format and completeness
  4. Camera permission and image processing failures handled gracefully

**Superseded** (2026-09-12 — delivered directly via ticket set):

- ticket 04 → done 2026-09-08 (见 .scratch/speech-recognition-redesign/issues/04-photo-ocr-answer-submission.md Status 行)

**Plans**: delivered directly as ticket 04 (2026-09-08) — no GSD plan files

Plans:

- [x] Ticket 04 — photo OCR answer submission (iPad camera capture + Mimo vision OCR, manual-input fallback) delivered directly: 1ceba56, a7b26f7, d588386, 47a668b, 4536cff, dec6cab + review 修复 2d15bef; issue closed done 2026-09-08

### Phase 8.4: Batch Answer Submission Workflow (INSERTED)

**Goal:** Students write all words first and submit together at the end — the authentic classroom dictation rhythm (write all → submit all → grade all). Session FSM gains DICTATING → SUBMISSION → FEEDBACK transitions.
**Source of truth:** `.scratch/speech-recognition-redesign/issues/05-batch-answer-submission-workflow.md`
**Depends on**: Phase 8.3
**Requirements**: SUBMIT-04, SUBMIT-05
**Success Criteria** (what must be TRUE):

  1. Student can submit all answers together after completing dictation
  2. Batch submission processes multiple words efficiently with progress indicators
  3. Session FSM handles DICTATING → SUBMISSION → FEEDBACK transitions
  4. Submission interface supports both OCR and manual input; all answers validated before processing

**Superseded** (2026-09-12 — delivered directly via ticket set):

- ticket 05 → done 2026-09-09 (见 .scratch/speech-recognition-redesign/issues/05-batch-answer-submission-workflow.md Status 行)

**Plans**: delivered directly as ticket 05 (2026-09-09) — no GSD plan files

Plans:

- [x] Ticket 05 — batch answer submission workflow (write all → submit all → grade all) delivered directly: ae4c1d6, 87629b4, 33bb23c, 028b2c8 + review 修复 5848db4; issue closed done 2026-09-09

### Phase 8.5: Automatic Grading and Analysis Reports (INSERTED)

**Goal:** System automatically grades submitted answers (character-level comparison tolerant of common mistakes) and delivers detailed analysis reports — error patterns, learning suggestions, performance trends — with scores and encouraging feedback read aloud via TTS. Removes the parent bottleneck for immediate feedback; results feed FSRS updates and parent review (Phase 10).
**Source of truth:** `.scratch/speech-recognition-redesign/issues/06-automatic-grading-detailed-analysis.md`
**Depends on**: Phase 8.4
**Requirements**: GRADE-01, GRADE-02, GRADE-03, GRADE-04
**Success Criteria** (what must be TRUE):

  1. Character-level comparison between submitted and correct answers, tolerant of minor variations and common mistakes
  2. Detailed analysis reports show error patterns, character-level error highlighting, and learning suggestions
  3. Final scores and encouraging feedback read aloud via TTS
  4. Results stored for FSRS updates and parent review; performance trends tracked over multiple sessions

**Superseded** (2026-09-12 — delivered directly via ticket set):

- ticket 06 → done 2026-09-10 (见 .scratch/speech-recognition-redesign/issues/06-automatic-grading-detailed-analysis.md Status 行)

**Plans**: delivered directly as ticket 06 (2026-09-10) — no GSD plan files

Plans:

- [x] Ticket 06 — automatic grading and detailed analysis (char-level diff, error-pattern reports, TTS-read feedback) delivered directly: 4f35ede, 1d63a17, bae4c03, b87bfe2, a8c75c6, c7bce18, 6ce1fb6 (review 修复见 issue); issue closed done 2026-09-10

### Phase 8.6: Multi-User Session Isolation and Scaling (INSERTED)

**Goal:** Multiple students run concurrent dictation sessions with proper isolation — data boundaries across database, WebSocket connections, and service integrations — preparing for classroom deployment beyond single-user.
**Source of truth:** `.scratch/speech-recognition-redesign/issues/07-multi-user-session-isolation-scaling.md`
**Depends on**: Phase 8.5
**Requirements**: RES-01 (multi-user), RES-03 (50 concurrent)
**Success Criteria** (what must be TRUE):

  1. Multiple students can have active dictation sessions simultaneously with properly isolated session data
  2. Memory stays within the 150MB Go service budget under expected load; resource pooling prevents leaks
  3. Performance testing validates 50 concurrent student capacity
  4. Graceful degradation when approaching capacity limits

**Superseded** (2026-09-12 — delivered directly via ticket set):

- ticket 07 → done 2026-09-11 (见 .scratch/speech-recognition-redesign/issues/07-multi-user-session-isolation-scaling.md Status 行)

**Plans**: delivered directly as ticket 07 (2026-09-11) — no GSD plan files

Plans:

- [x] Ticket 07 — multi-user session isolation and scaling (per-connID session isolation, 50-concurrent load validated) delivered directly: 4cb7e7b, ad4a09c, 5f8146b + review 修复 (见 git log 'ticket 07'); issue closed done 2026-09-11

### Phase 9: Parent Vocabulary Management

**Goal**: Parent interface for adding, editing, and organizing vocabulary with FSRS configuration
**Depends on**: Phase 4, Phase 8
**Requirements**: VOC-01, VOC-02, VOC-03, WEB-01
**Success Criteria** (what must be TRUE):

  1. Parent can add vocabulary words with Chinese translations, pronunciations, and example sentences
  2. Parent can organize words into units and lessons
  3. Parent can configure FSRS parameters (difficulty, retention interval)
  4. Interface persists all changes to SQLite database

**Plans**: 6/8 plans executed

Plans:
**Wave 1**

- [x] 09-01-PLAN.md — [tracer] Single-word add end-to-end slice: parent.html → /api/v1 → pybridge.RunAdd → vocab_manager.py → SQLite → CardRepository → browse list (VOC-01, WEB-01, D-01/D-02/D-06/D-08/D-12)
- [x] 09-02-PLAN.md — Python engine: `edit` subcommand (D-07) + config-driven SCHEDULER from fsrs_config.json (D-13/D-14) (VOC-01, VOC-03)

**Wave 2** *(blocked on Wave 1)*

- [x] 09-03-PLAN.md — Bridge expansion: RunEdit/RunRenameUnit/RunDeleteWords + --confirm guarantee + ExitError.Stdout diagnostics (VOC-01, VOC-02, Pitfalls 1/2)
- [x] 09-04-PLAN.md — Unit reads: CardRepository.CountByUnit + GET /api/v1/units (VOC-02, D-12)

**Wave 3** *(blocked on Wave 2)*

- [x] 09-05-PLAN.md — Complete REST surface: edit/rename/delete endpoints + GET/PUT /api/v1/settings/fsrs with atomic write (VOC-01, VOC-02, VOC-03, D-11/D-13/D-14)
- [x] 09-06-PLAN.md — Add-words UI: batch paste parser (node-tested) + dual-mode 添加单词 card (VOC-01, WEB-01, D-10)

**Wave 4** *(blocked on Wave 3)*

- [ ] 09-07-PLAN.md — Browse + organize UI (unit groups, search, edit/rename/delete modals) + 学习设置 card + responsive two-column layout (VOC-02, VOC-03, WEB-01, D-03/D-11/D-12/D-15)

**Wave 5** *(blocked on Wave 4)*

- [ ] 09-08-PLAN.md — E2E capstone: full parent flow + 3-viewport responsive matrix + backstop visual checks + phase full-suite gate (WEB-01 + VOC-01/02/03)

**PROJECT_MODE**: mvp
**UI hint**: yes

### Phase 10: Parent Feedback System

**Goal**: Parent review interface over auto-graded dictation results (Phase 8.5) — view detailed analysis reports, verify or override per-word grades, and track progress statistics. Parent confirmation remains the FSRS authority; auto-grading pre-fills the ratings (removes the parent bottleneck without removing parent control).
**Depends on**: Phase 7, Phase 8, Phase 8.5
**Requirements**: FEED-01, FEED-02, FEED-03, FEED-04, GRADE-04, WEB-03
**Success Criteria** (what must be TRUE):

  1. Parent can review completed dictation session results with auto-grading analysis attached (error patterns, suggestions, confidence)
  2. Parent can verify or override each word's auto-assessed performance (good/hard/again/easy, pre-filled from grading)
  3. Confirmed feedback ratings update FSRS algorithm parameters (RunReview path unchanged)
  4. System displays progress statistics and vocabulary retention metrics

**Plans**: TBD

**PROJECT_MODE**: mvp
**UI hint**: yes

### Phase 11: Error Handling & Resilience

**Goal**: Graceful error handling with meaningful messages and structured logging
**Depends on**: Phase 10
**Requirements**: ERR-01, ERR-02, RES-02
**Success Criteria** (what must be TRUE):

  1. System gracefully handles WebSocket disconnections without data corruption
  2. System provides meaningful error messages to parents and students
  3. Audio buffer pool uses sync.Pool for frame reuse and memory efficiency
  4. All errors are logged with structured format for debugging

**Plans**: TBD

**PROJECT_MODE**: mvp

### Phase 12: End-to-End Validation

**Goal**: Complete user flow verification with performance testing and resource validation on target hardware — absorbs speech-recognition-redesign ticket 08 scope (children's voices, real handwriting samples, real iPad Safari workflows).
**Source of truth (extension):** `.scratch/speech-recognition-redesign/issues/08-end-to-end-integration-testing.md`
**Depends on**: Phase 11, Phase 8.6
**Requirements**: All v1 requirements
**Success Criteria** (what must be TRUE):

  1. Complete student flow works on actual iPad Safari devices: session request → dictation → voice commands → photo/batch submission → auto-grading → feedback
  2. Complete parent flow works: vocabulary management → session monitoring → review/override auto-grades → feedback
  3. Voice command recognition tested with children's voices (ages 6-12); OCR accuracy validated with real elementary handwriting samples
  4. Performance benchmarks meet latency (<1.5s) and memory (150MB) requirements; error scenarios degrade gracefully with fallbacks
  5. All v1 requirements validated with observable user behaviors; system ready for single-student production deployment

**Plans**: TBD

**PROJECT_MODE**: mvp

## Progress

**Execution Order:**
Phases execute in numeric order: 1 → 2 → 3 → 4 → 5 → 6 → 7 → 8 → 8.1 → 8.2 → 8.3 → 8.4 → 8.5 → 8.6 → 9 → 10 → 11 → 12

| Phase | Plans Complete | Status | Completed |
|-------|----------------|--------|-----------|
| 1. Resource-Constrained Infrastructure | 8/8 | Complete    | 2026-07-23 |
| 2. WebSocket Communication Layer | 1/3 | Complete    | 2026-07-19 |
| 3. Python CLI Integration | 5/5 | Complete    | 2026-08-01 |
| 4. Database Schema Design | 5/5 | Complete    | 2026-08-01 |
| 5. FSRS Core Implementation | 2/2 | Complete    | 2026-08-02 |
| 6. TTS Audio Streaming | 4/4 | Complete    | 2026-08-08 |
| 7. Session State Management | 3/3 | Complete    | 2026-08-08 |
| 07.1. iOS WebSocket Audio Playback Fix (INSERTED) | 4/4 | Complete    | 2026-08-09 |
| 8. Student Web Interface | 7/7 | Complete    | 2026-09-27 |
| 8.1. Core Dictation Flow with Mimo TTS (INSERTED) | 0/0 | Not started (re-planning after re-scope) | - |
| 8.2. Voice Command Recognition via Mimo ASR (INSERTED) | 0/0 | Not started | - |
| 8.3. Photo OCR Answer Submission (INSERTED) | 0/0 | Not started | - |
| 8.4. Batch Answer Submission Workflow (INSERTED) | 0/0 | Not started | - |
| 8.5. Automatic Grading and Analysis Reports (INSERTED) | 0/0 | Not started | - |
| 8.6. Multi-User Session Isolation and Scaling (INSERTED) | 0/0 | Not started | - |
| 9. Parent Vocabulary Management | 6/8 | In Progress|  |
| 10. Parent Feedback System | 0/0 | Not started | - |
| 11. Error Handling & Resilience | 0/0 | Not started | - |
| 12. End-to-End Validation | 0/0 | Not started | - |

## Backlog

### Phase 999.1: go build/test 性能优化 — 超时测试导致构建慢 (BACKLOG)

**Goal:** 排查并修复导致 `go build` / `go test` 执行缓慢的根因
**Requirements:** TBD
**Plans:** 0 plans

Plans:

- [ ] TBD (promote with /gsd-review-backlog when ready)

**问题描述：** `go build` 和 `go test` 执行时间异常长，怀疑代码中存在设置了长时间超时条件的测试用例（如 `time.Sleep`、`time.After`、自定义超时），导致每次构建/测试都需要等待超时才能完成。需要专题排查所有测试文件中的 time-related 阻塞模式，将其改为可控的 mock 或缩短超时。
