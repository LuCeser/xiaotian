# Home Tutor Agent

## What This Is

A web-based real-time dictation tutor agent that provides natural, interactive learning experiences for elementary school students. The system uses conversational AI to deliver personalized vocabulary practice through voice dictation sessions, with parent feedback integration that drives FSRS (Free Spaced Repetition Scheduler) algorithm updates.

## Core Value

Students receive personalized, real-time dictation practice with immediate feedback while parents get detailed progress tracking and the ability to influence the learning algorithm through their reviews.

## Business Context

- **Customer**: Elementary school students and their parents
- **Revenue model**: Not yet defined (educational research phase)
- **Success metric**: Student engagement and vocabulary retention improvement
- **Strategy notes**: Phase 1 focuses on technical MVP; broader market strategy to be determined

## Requirements

### Validated

(None yet — ship to validate)

### Active

- [ ] **Word/vocabulary entry management** - Parents can add and organize vocabulary words by unit/lesson
- [ ] **FSRS-based automatic word selection** - System selects due words based on spaced repetition algorithm
- [ ] **Real-time voice dictation interaction** - Students request dictation sessions ("I want to dictate 30 words") and receive TTS prompts
- [ ] **Parent feedback integration** - Post-dictation review interface for parents to rate performance (good/hard/again/easy)
- [ ] **FSRS algorithm updates** - Parent feedback automatically updates the spaced repetition scheduler
- [ ] **iPad web browser support** - Responsive interface optimized for iPad tablets
- [ ] **Resource-constrained deployment** - Efficient operation on 1C 1G VPS infrastructure
- [ ] **Session state management** - Robust WebSocket session handling (IDLE → LISTENING → DICTATING → WAITING_PARENT → FEEDBACK)
- [ ] **Audio streaming** - Real-time TTS audio delivery with <1.5s latency target
- [ ] **Integration with existing Python assets** - Reuse vocab_manager.py and pregenerate_dictation.py from xiaotian-skills

### Out of Scope

- **Photo OCR** — Deferred to Phase 2 to maintain Phase 1 focus on core dictation flow
- **Digital avatar integration** — Planned for Phase 3, requires Phase 1 real-time foundation
- **Mobile native apps** — iPad web browser approach reduces development complexity
- **Multi-language support** — English-only for Phase 1, expansion possible later
- **Advanced analytics dashboard** — Basic statistics for Phase 1, detailed analytics later

## Context

**Technical Environment:**
- Greenfield project located at `D:\workspace\ai\xiaotian`
- Existing reusable assets at `D:\workspace\ai\xiaotian-skills\shared\dictation\scripts\`
- vocab_manager.py (822 lines): FSRS algorithm + SQLite CLI
- pregenerate_dictation.py (1348 lines): Sophisticated TTS pipeline
- Target deployment: 1C 1G VPS (resource-constrained environment)

**Prior Work:**
- Extensive planning completed including architecture design, implementation strategy, and verification approach
- Technical stack research completed: Go WebSocket + Python microservices architecture
- WebSocket protocol designed with binary audio frames (640B for 20ms@16kHz PCM)
- Resource management strategy defined: 150MB memory budget for Go service, 100 concurrent sessions max

**User Research:**
- Current Hermes + WeChat gateway approach has 1-2 minute delays and poor interaction experience
- Target users: elementary school students requiring simple, engaging interface
- Parents need: vocabulary management, progress tracking, and feedback mechanisms

**Known Issues:**
- Current solution lacks real-time interaction capabilities
- WeChat restrictions prevent rich interactive experiences
- No foundation for future digital avatar integration
- Resource constraints require careful memory and connection management

## Constraints

- **Infrastructure**: 1C 1G VPS with strict memory limits (150MB for Go service, 50MB audio buffer pool)
- **Target Platform**: iPad web browser (Safari compatibility required)
- **Audio Technology**: Full streaming TTS approach (consistency over caching), Doubao/XiaoMimiMo APIs
- **Language Stack**: Go for WebSocket service, Python for existing business logic integration
- **Storage**: SQLite database (sufficient for current needs, avoids PostgreSQL overhead)
- **Latency Target**: <1.5 seconds acceptable for dictation responses
- **Architecture**: Must support future digital avatar integration (Phase 3)
- **Concurrency**: Support 50-100 concurrent sessions within resource constraints

## Key Decisions

| Decision | Rationale | Outcome |
|----------|-----------|---------|
| Go WebSocket + Python microservices | Go provides efficient WebSocket handling with lower memory; Python reuse existing FSRS and TTS logic | — Pending implementation |
| Full streaming TTS vs caching | Consistency and simplicity over complex caching strategies for Phase 1 | — Pending implementation |
| SQLite vs PostgreSQL | SQLite sufficient for current scale, avoids resource overhead on 1C 1G VPS | — Pending implementation |
| Binary WebSocket protocol (640B frames) | Optimized for 20ms@16kHz PCM audio with proper sequence management | — Pending implementation |
| Resource budgeting (150MB Go + 50MB audio) | Prevents memory overflow on 1C 1G VPS while supporting 50-100 concurrent sessions | — Pending implementation |

## Evolution

This document evolves at phase transitions and milestone boundaries.

**2026-09-27 — Phase 08 (Student Web Interface) complete.** DICT-01 / WEB-02 / WEB-04 validated (goal-backward verification passed: 28/31 must-have truths verified, 2 device-audition deferrals, VPS rehearsal → Phase 12). New browser-E2E layer: Playwright (`make e2e` / `npm run e2e`, commit 599c2a4). Phases 8.1–8.6 were delivered directly as speech-recognition-redesign tickets 01–07; next actionable phase with pending work: **9 (Parent Vocabulary Management)**. Carried residuals: UAT-CHECKLIST A2/A4-sub/A5 (next iPad session), session-resume-across-reconnect → Phase 11, VPS deployment → Phase 12.

**After each phase transition** (via `/gsd-transition`):
1. Requirements invalidated? → Move to Out of Scope with reason
2. Requirements validated? → Move to Validated with phase reference
3. New requirements emerged? → Add to Active
4. Decisions to log? → Add to Key Decisions
5. "What This Is" still accurate? → Update if drifted

**After each milestone** (via `/gsd-complete-milestone`):
1. Full review of all sections
2. Core Value check — still the right priority?
3. Audit Out of Scope — reasons still valid?
4. Update Context with current state

---
*Last updated: 2026-09-27 after Phase 08 completion*