# Requirements: Home Tutor Agent

> **2026-09-30 产品验收重定基：** RT-01~RT-10 已忠实转录为本文件下方「**产品验收（最高优先级）：RT-01~RT-10**」组（逐字对齐 [项目复盘第 10 节](../docs/PROJECT-REVIEW-2026-09-30.md#10-新验收标准)，数字与措辞不改），是当前产品验收的唯一标准。下列 v1 历史勾选 = 组件证据，不构成产品验收。DICT-05 已重开（待 RT-04 真机实测「首个实际可听响应」）。主线路线与三条硬护栏见 [ADR-0002](../docs/adr/0002-browser-realtime-tutor.md)（Accepted）。

**Defined:** 2025-07-18
**Core Value:** Students receive personalized, real-time dictation practice with immediate feedback while parents get detailed progress tracking and the ability to influence the learning algorithm through their reviews.

## 产品验收（最高优先级）：RT-01~RT-10

逐条忠实转录自 [项目复盘第 10 节](../docs/PROJECT-REVIEW-2026-09-30.md#10-新验收标准)，数字与措辞照抄不改：

- [ ] **RT-01**: 网页可访问 — 目标 iPad Safari 经可信 HTTPS 打开正式入口，资源与状态可见；HTTP 下明确提示语音不可用原因
- [ ] **RT-02**: 进入会话 — 授权完成后，一次点击开始；后续至少 10 轮无需反复按住录音
- [ ] **RT-03**: 自然交流 — 发起 3 词课程、指代上一词、问含义、求慢读、表示没写完均正确响应；至少覆盖 20 条含上下文场景
- [ ] **RT-04**: 首次与轮次延迟 — 授权完成至首次可听问候目标 ≤3 秒；句末至首个实际可听响应目标 P50≤1.5 秒、P95≤2.5 秒，至少 30 次测量
- [ ] **RT-05**: 打断 — 学生开口至旧响应停止目标 P95≤500ms；旧生成/排队音频不复活，下一轮理解实际已播放内容
- [ ] **RT-06**: 教学节奏 — 保持等待书写，不因普通沉默自动跳词；求助可以提供提示、询问是否跳过，不直接等同“不会写→下一词”
- [ ] **RT-07**: 数据一致性 — 对话工具回调与按钮都经过同一业务服务；重试不重复推进，进度、字幕、口头反馈一致
- [ ] **RT-08**: 稳定性与恢复 — 15 分钟真实练习；切后台和断网恢复有明确状态；恢复课堂进度，禁止偷偷新建课程造成跳词
- [ ] **RT-09**: 学习闭环 — 完成听写、提交、批改及确认后，正确记录结果并影响后续 FSRS 选词
- [ ] **RT-10**: 故障可见 — 麦克风、提供商或网络故障可见；备用按钮可用，但界面明确处于降级模式，不能标记语音验收通过

**必须记录**（复盘第 10 节末段）：页面就绪、授权完成、连接就绪、speech_started/ended、工具开始/结束、首个收到的音频、首个实际播放的音频、打断开始/停播及课堂版本。以真实可听音频衡量响应，不以 HTTP 成功、收到帧或模型输出结束替代。

> 注：以上数值是验收目标，不是当前实现或提供商保证；主线路线（自研 RTC + MiMo 可替换接口）与三条硬护栏见 [ADR-0002](../docs/adr/0002-browser-realtime-tutor.md)（Accepted），阶段映射见 ROADMAP「R0–R3 当前主线」章节。

## v1 Requirements

Requirements for Phase 1 implementation. Each maps to roadmap phases.

### Vocabulary Management

- [x] **VOC-01**: Parent can add vocabulary words with Chinese translations, pronunciations, and example sentences
- [x] **VOC-02**: Parent can organize words into units and lessons
- [x] **VOC-03**: Parent can configure FSRS parameters (difficulty, retention interval)
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
- [ ] **DICT-05**: System achieves <1.5 second latency for audio responses
  - 重开（2026-09-30）：待 RT-04 真机实测「首个实际可听响应」；原勾选仅为组件证据。
- [x] **DICT-06**: System provides adaptive writing time (8-15 seconds) based on word length and complexity
- [x] **DICT-07**: Student can control dictation pace with Next Word / Repeat Word / Pause/Resume buttons

> **2026-08-23 校准**（speech-recognition-redesign ticket 集为唯一真相源）：DICT-03 的音频源由 Python-bridge TTS 迁移至小米 Mimo 直连（`pkg/mimo`，ticket 01 已交付验证）；听写循环采用**学生控速模型**，取代 Phase 8.1 原后端自动推进设计。

### Parent Feedback

- [ ] **FEED-01**: Parent can review completed dictation session results with auto-grading analysis attached (error patterns, suggestions, confidence)
- [ ] **FEED-02**: Parent can verify or override each word's auto-assessed performance (good/hard/again/easy, pre-filled from Phase 8.5 auto-grading)
- [ ] **FEED-03**: Confirmed feedback ratings update FSRS algorithm parameters (RunReview path unchanged)
- [ ] **FEED-04**: System displays progress statistics and vocabulary retention metrics

### Voice Commands（降级模式能力，RT-10 备用）

> 已按 ticket 03 交付并 live 验证（2026-09-08）作为组件证据，服务 RT-10 备用按钮路径；旧按钮听写页功能冻结（只修阻塞 bug），不再作为产品主交互演进。

- [x] **VOICE-01**: System captures student audio via Web Audio API on iPad Safari for command recognition
- [x] **VOICE-02**: Xiaomi Mimo ASR transcribes student voice commands accurately, including children's voices (ages 6-12)
- [x] **VOICE-03**: LLM intent analysis maps varied natural expressions to the 4 core commands ("好了", "重读", "暂停", "不会写") and triggers correct session state transitions
- [x] **VOICE-04**: Failed recognition falls back to button controls

### Answer Submission

- [x] **SUBMIT-01**: Students can capture photos of written answers via iPad camera on a Safari-optimized interface
- [x] **SUBMIT-02**: OCR extracts text with ≥85% accuracy for typical elementary handwriting, with confidence scores
- [x] **SUBMIT-03**: Manual input available when OCR fails or confidence is low; system validates answer format and completeness
- [x] **SUBMIT-04**: Student submits all answers together after dictation (write all → submit all → grade all); session FSM handles DICTATING → SUBMISSION → FEEDBACK
- [x] **SUBMIT-05**: Batch submission processes multiple words efficiently with progress indicators, supporting both OCR and manual input

### Automatic Grading

- [x] **GRADE-01**: System performs character-level comparison between submitted and correct answers, tolerant of minor variations and common mistakes
- [x] **GRADE-02**: Detailed analysis reports show error patterns, character-level error highlighting, and learning suggestions
- [x] **GRADE-03**: Final scores and encouraging feedback are read aloud via TTS
- [x] **GRADE-04**: Results stored for FSRS updates and parent review; performance trends tracked over multiple sessions

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

- [x] **WEB-01**: Parent interface for vocabulary management (add/edit/organize)
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
| VOC-01 | Phase 9 | Complete |
| VOC-02 | Phase 9 | Complete |
| VOC-03 | Phase 9 | Complete |
| VOC-04 | Phase 4 | Complete |
| FSRS-01 | Phase 5 | Complete |
| FSRS-02 | Phase 5 | Complete |
| FSRS-03 | Phase 5 | Complete |
| FSRS-04 | Phase 5 | Complete |
| DICT-01 | Phase 8 | Complete |
| DICT-02 | Phase 7 | Complete |
| DICT-03 | Phase 6 | Complete |
| DICT-04 | Phase 7 | Complete |
| DICT-05 | Phase 6 → R0 | Reopened（RT-04） |
| DICT-06 | Phase 8.1 | Delivered（component evidence） |
| DICT-07 | Phase 8.1 | Delivered（component evidence） |
| VOICE-01 | 8.2（RT-10 备用） | Delivered |
| VOICE-02 | 8.2（RT-10 备用） | Delivered |
| VOICE-03 | 8.2（RT-10 备用） | Delivered |
| VOICE-04 | 8.2（RT-10 备用） | Delivered |
| SUBMIT-01 | Phase 8.3 | Delivered（component evidence） |
| SUBMIT-02 | Phase 8.3 | Delivered（component evidence） |
| SUBMIT-03 | Phase 8.3 | Delivered（component evidence） |
| SUBMIT-04 | Phase 8.4 | Delivered（component evidence） |
| SUBMIT-05 | Phase 8.4 | Delivered（component evidence） |
| GRADE-01 | Phase 8.5 | Delivered（component evidence） |
| GRADE-02 | Phase 8.5 | Delivered（component evidence） |
| GRADE-03 | Phase 8.5 | Delivered（component evidence） |
| GRADE-04 | Phase 8.5 / Phase 10 | Delivered（component evidence） |
| FEED-01 | R2 | Pending |
| FEED-02 | R2 | Pending |
| FEED-03 | R2 | Pending |
| FEED-04 | R2 | Pending |
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
| WEB-01 | Phase 9 | Complete |
| WEB-02 | Phase 8 | Complete |
| WEB-03 | Phase 10 | Pending |
| WEB-04 | Phase 8 | Complete |
| ERR-01 | Phase 11 | Pending |
| ERR-02 | Phase 11 | Pending |
| ERR-03 | Phase 7 | Complete |
| ERR-04 | Phase 1 | Complete |
| RT-01 | R0 | Pending |
| RT-02 | R0 | Pending |
| RT-03 | R0 → R1 | Pending |
| RT-04 | R0（kill criteria 挂钩） | Pending |
| RT-05 | R0（kill criteria 挂钩） | Pending |
| RT-06 | R1 | Pending |
| RT-07 | R1 | Pending |
| RT-08 | R2 | Pending |
| RT-09 | R2 | Pending |
| RT-10 | R0 → R1 | Pending |

**Coverage:**

- v1 requirements: 47 total (32 original + 15 added 2026-08-23 from speech-recognition-redesign calibration)
- 产品验收：RT-01~RT-10 共 10 项（最高优先级，挂 R0–R3 主线，见 ROADMAP「R0–R3 当前主线」章节）
- Mapped to phases: 47 (100% coverage ✓)
- Unmapped: 0

---
*Requirements defined: 2025-07-18*
*Last updated: 2026-09-30 — 产品验收重定基：新增「产品验收（最高优先级）：RT-01~RT-10」组（忠实转录自项目复盘第 10 节）；DICT-05 重开待 RT-04 实测；VOICE-01~04 重标为降级模式能力（RT-10 备用）并勾选；DICT-06/07、SUBMIT、GRADE 按组件证据勾选；FEED 重映射 R2；Traceability 新增 RT 行*
*2026-08-23 — calibrated to speech-recognition-redesign ticket set (single source of truth): added DICT-06/07, VOICE-01..04, SUBMIT-01..05, GRADE-01..04; re-sourced FEED-01..03 to auto-grading + parent confirm/override*
