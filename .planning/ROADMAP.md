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
- [ ] **Phase 3: Python CLI Integration** - Subprocess bridge to vocab_manager and TTS generation
- [ ] **Phase 4: Database Schema Design** - SQLite schema for vocabulary and FSRS metadata
- [ ] **Phase 5: FSRS Core Implementation** - Automatic due word selection and parameter updates
- [ ] **Phase 6: TTS Audio Streaming** - Real-time audio delivery with <1.5s latency
- [ ] **Phase 7: Session State Management** - Complete session lifecycle and state transitions
- [ ] **Phase 8: Student Web Interface** - Responsive iPad-optimized student experience
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

**Plans**: 1/3 plans executed

Plans:
**Wave 1**

- [x] 02-01-PLAN.md — Binary frame protocol + CRC-16 codec (WS-02, D-01, D-05~D-11)

**Wave 2** *(blocked on Wave 1 completion)*

- [ ] 02-02-PLAN.md — Hub + Client lifecycle + Heartbeat + test helpers (WS-03, D-04, D-13~D-17)

**Wave 3** *(blocked on Wave 2 completion)*

- [ ] 02-03-PLAN.md — HTTP Server + Router + end-to-end integration tests (WS-01, WS-04, D-02, D-19)

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

**Plans**: 3/5 plans executed

Plans:
**Wave 1**

- [x] 03-01-PLAN.md — PythonConfig 扩展 + vendor 上游脚本 + Makefile py-* target (PY-01, D-03/D-04/D-05)
- [x] 03-02-PLAN.md — Executor 接口 + sentinel 错误 + 解析器 + 信号量 + 黄金 fixture (PY-01)

**Wave 2** *(blocked on Wave 1 completion)*

- [x] 03-03-PLAN.md — Bridge exec 引擎 + 进程组杀死 + 密钥 env 注入 + 结构化日志 + main.go 接线 (PY-01)

**Wave 3** *(blocked on Wave 2 completion)*

- [ ] 03-04-PLAN.md — RunDue + RunReview + argv 构造 + 末尾 JSON 提取 (PY-02, PY-03)
- [ ] 03-05-PLAN.md — RunPregenerate + 按行路径解析 + flock 串行化 + shell fake (PY-04)

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

**Plans**: TBD

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

**Plans**: TBD

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

**Plans**: TBD

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

**Plans**: TBD

**PROJECT_MODE**: mvp

### Phase 8: Student Web Interface

**Goal**: Responsive iPad-optimized student interface for session requests and progress display
**Depends on**: Phase 7
**Requirements**: DICT-01, WEB-02, WEB-04
**Success Criteria** (what must be TRUE):

  1. Student can request dictation session via button ("I want to dictate 30 words")
  2. Interface displays real-time session progress (current word, completed count)
  3. Responsive design works optimally on iPad Safari browser
  4. Interface handles WebSocket reconnections gracefully

**Plans**: TBD

**PROJECT_MODE**: mvp
**UI hint**: yes

### Phase 9: Parent Vocabulary Management

**Goal**: Parent interface for adding, editing, and organizing vocabulary with FSRS configuration
**Depends on**: Phase 4, Phase 8
**Requirements**: VOC-01, VOC-02, VOC-03, WEB-01
**Success Criteria** (what must be TRUE):

  1. Parent can add vocabulary words with Chinese translations, pronunciations, and example sentences
  2. Parent can organize words into units and lessons
  3. Parent can configure FSRS parameters (difficulty, retention interval)
  4. Interface persists all changes to SQLite database

**Plans**: TBD

**PROJECT_MODE**: mvp
**UI hint**: yes

### Phase 10: Parent Feedback System

**Goal**: Post-dictation review interface for performance ratings and progress statistics
**Depends on**: Phase 7, Phase 8
**Requirements**: FEED-01, FEED-02, FEED-03, FEED-04, WEB-03
**Success Criteria** (what must be TRUE):

  1. Parent can review completed dictation session results
  2. Parent can rate each word performance (good/hard/again/easy)
  3. Feedback ratings automatically update FSRS algorithm parameters
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

**Goal**: Complete user flow verification with performance testing and resource validation
**Depends on**: Phase 11
**Requirements**: All v1 requirements
**Success Criteria** (what must be TRUE):

  1. Complete student flow works: session request → dictation → parent review → feedback
  2. Complete parent flow works: vocabulary management → session monitoring → feedback
  3. System operates within 150MB memory budget under 100 concurrent sessions
  4. All 32 v1 requirements validated with observable user behaviors

**Plans**: TBD

**PROJECT_MODE**: mvp

## Progress

**Execution Order:**
Phases execute in numeric order: 1 → 2 → 3 → 4 → 5 → 6 → 7 → 8 → 9 → 10 → 11 → 12

| Phase | Plans Complete | Status | Completed |
|-------|----------------|--------|-----------|
| 1. Resource-Constrained Infrastructure | 8/8 | Complete    | 2026-07-23 |
| 2. WebSocket Communication Layer | 1/3 | Complete    | 2026-07-19 |
| 3. Python CLI Integration | 3/5 | In Progress|  |
| 4. Database Schema Design | 0/0 | Not started | - |
| 5. FSRS Core Implementation | 0/0 | Not started | - |
| 6. TTS Audio Streaming | 0/0 | Not started | - |
| 7. Session State Management | 0/0 | Not started | - |
| 8. Student Web Interface | 0/0 | Not started | - |
| 9. Parent Vocabulary Management | 0/0 | Not started | - |
| 10. Parent Feedback System | 0/0 | Not started | - |
| 11. Error Handling & Resilience | 0/0 | Not started | - |
| 12. End-to-End Validation | 0/0 | Not started | - |
