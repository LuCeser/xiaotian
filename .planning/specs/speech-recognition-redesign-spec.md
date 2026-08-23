# Speech Recognition Redesign Specification

**Spec Version:** 1.0  
**Date Created:** 2025-08-23  
**Status:** Ready for Agent Implementation  
**Phase:** Speech Recognition System Redesign

---

## Problem Statement

The current speech recognition implementation using Web Speech API (`webkitSpeechRecognition`/`SpeechRecognition`) is completely broken across all tested browsers (macOS Safari, Chrome, Edge) and likely to fail on the target iPad Safari platform. The system shows console errors like `speech not supported — auto-advancing startDictationForWord` and `SpeechController supported: false`, rendering the dictation core loop non-functional.

**User's Core Concern:** "我怀疑之前的实现并未使用业界成熟的方案" (I suspect the previous implementation did not use industry-standard solutions)

This represents a fundamental architectural misalignment - successful education apps like Duolingo, Khan Academy, and Chinese platforms (作业帮, 猿辅导) do NOT rely on browser-based Web Speech API due to its unreliability, especially on iOS Safari. The current approach prevents students from completing the minimum viable dictation flow:

> 学生打开网页 → 系统朗读单词 → 学生语音回答 → 系统判断对错 → 显示反馈

---

## Solution

Replace the unreliable Web Speech API with a production-proven, server-side speech recognition architecture using **Xiaomi Mimo ASR and TTS services**. This hybrid approach mirrors industry standards for education apps and provides reliable, consistent speech recognition optimized for children's voices.

**New Architecture Flow:**
```
iPad Safari (Web Audio API capture) → WebSocket (Go Backend) → Xiaomi Mimo ASR → Intent Analysis → Control Logic
                                    ↓
                          Xiaomi Mimo TTS → Audio Streaming → Student Listens & Writes
```

This solution addresses the core requirement of **traditional teacher-student dictation** where:
1. System reads word aloud (TTS)
2. Student writes on paper (not speaks)
3. System waits for student to finish writing
4. Voice commands control flow ("好了", "重读", "暂停", "不会写")
5. Student submits answers via photo OCR or manual input after completion
6. System provides detailed automatic grading and analysis

---

## User Stories

### Core Dictation Flow (User Stories 1-15)

1. **As a student**, I want to hear words read aloud one by one with natural pauses, so that I can write each word on paper at my own pace.
2. **As a student**, I want 8-15 seconds of adaptive writing time per word based on difficulty, so that simple words don't take too long but complex words give enough time.
3. **As a student**, I want to say "好了" (finished) or "下一个" (next) when done writing each word, so that I can control the pace naturally.
4. **As a student**, I want to say "没听清" (didn't hear clearly) or "再说一遍" (say it again) to replay the current word, so that I never miss content due to audio issues.
5. **As a student**, I want to say "暂停" (pause) or "等等一下" (wait a moment) when I need a break, so that I can handle interruptions without losing progress.
6. **As a student**, I want to say "不会写" (don't know how to write) when stuck on a word, so that I can get help or skip to the next word.
7. **As a student**, I want to use either voice commands OR on-screen buttons for control, so that I have reliable backup options if voice recognition fails.
8. **As a student**, I want to write all words first and submit them together at the end, so that I can focus on writing without interruption.
9. **As a student**, I want to take a photo of my written answers for automatic recognition, so that I don't have to manually type each word.
10. **As a student**, I want a manual input option as backup when photo OCR fails, so that I can always complete the submission.
11. **As a student**, I want to receive a detailed analysis report after submission, so that I can understand which words I got wrong and why.
12. **As a student**, I want the system to automatically grade my answers without waiting for parent review, so that I get immediate feedback.
13. **As a student**, I want to hear encouraging feedback and my final score read aloud, so that I feel motivated and know my performance immediately.
14. **As a student**, I want the dictation process to work reliably on iPad Safari, so that I can use my school device for learning.
15. **As a student**, I want the system to understand my natural way of speaking (not just fixed commands), so that voice control feels natural and frustration-free.

### Technical Implementation Stories (User Stories 16-30)

16. **As a developer**, I want to integrate Xiaomi Mimo TTS service for word playback, so that we get high-quality, reliable audio generation optimized for education.
17. **As a developer**, I want to integrate Xiaomi Mimo ASR service for voice command recognition, so that we get accurate intent analysis with children's voice optimization.
18. **As a developer**, I want to use OpenAI-compatible API patterns for Xiaomi Mimo integration, so that we minimize integration complexity and leverage existing libraries.
19. **As a developer**, I want to implement a lightweight LLM-based intent analysis system, so that we can understand natural student commands beyond keyword matching.
20. **As a developer**, I want to support the four core voice commands (continue, repeat, pause, hint), so that students have complete flow control through voice.
21. **As a developer**, I want to implement adaptive timeout logic (8-15 seconds based on word length/difficulty), so that writing time matches educational needs.
22. **As a developer**, I want to capture audio via Web Audio API on iPad Safari and stream via WebSocket, so that we maintain control over audio quality and transmission.
23. **As a developer**, I want to integrate OCR services for photo-based answer submission, so that students can use the most natural input method.
24. **As a developer**, I want to implement hybrid answer submission (photo OCR primary, manual input backup), so that we maximize success rate.
25. **As a developer**, I want to implement automatic grading with character-level comparison, so that we provide immediate, objective feedback.
26. **As a developer**, I want to generate detailed analysis reports including error patterns and learning suggestions, so that students get actionable feedback.
27. **As a developer**, I want to maintain the existing WebSocket protocol and session management, so that we preserve architectural investments while upgrading speech services.
28. **As a developer**, I want to operate within 1C1G VPS memory constraints (150MB Go service), so that we keep infrastructure costs minimal.
29. **As a developer**, I want to use streaming TTS with <1.5s latency targets, so that the dictation flow maintains natural pacing.
30. **As a developer**, I want to design for single-student scale initially with multi-user expansion capability, so that we can start simple while planning for growth.

### Integration and Data Flow Stories (User Stories 31-40)

31. **As a system**, I want to fetch due words from the existing FSRS algorithm, so that students practice the right vocabulary at the right time.
32. **As a system**, I want to maintain the existing session state machine (IDLE → LISTENING → DICTATING → WAITING_PARENT → FEEDBACK), so that architectural consistency is preserved.
33. **As a system**, I want to use the existing WebSocket binary frame protocol for communication, so that we maintain compatibility with the current infrastructure.
34. **As a system**, I want to stream TTS audio using the existing audio buffer patterns, so that we reuse proven audio delivery mechanisms.
35. **As a system**, I want to record dictation results back to the database for FSRS updates, so that the learning algorithm continues to improve.
36. **As a system**, I want to maintain the existing timeout and cleanup patterns, so that resource management remains robust.
37. **As a system**, I want to integrate with the existing Python bridge for vocabulary management, so that we don't duplicate business logic.
38. **As a system**, I want to use the existing SQLite database schema for session storage, so that data persistence patterns remain consistent.
39. **As a system**, I want to maintain compatibility with the current error handling and logging infrastructure, so that debugging and monitoring remain effective.
40. **As a system**, I want to preserve the existing progressive enhancement philosophy, so that each feature addition can be validated independently.

---

## Implementation Decisions

### Technical Architecture Decisions

**Decision 1: Replace Web Speech API with Xiaomi Mimo Services**
- **Rationale:** Industry research shows education apps don't use browser-based speech APIs due to reliability issues, especially on iOS Safari. Xiaomi Mimo provides production-proven ASR/TTS with children's voice optimization.
- **Implementation:** Integrate Xiaomi Mimo ASR for voice commands and TTS for word playback. Use OpenAI-compatible API patterns for minimal integration complexity.
- **Impact:** Requires new service integration layer but eliminates browser compatibility issues and improves reliability significantly.

**Decision 2: Implement Traditional Teacher-Student Dictation Model**
- **Rationale:** User confirmed this is the real classroom pattern - teacher reads word, student writes on paper (not speaks aloud). Voice commands only control flow, not answer content.
- **Implementation:** System reads words via TTS, students write on paper, submit answers via photo OCR or manual input after completion.
- **Impact:** Major UX change from voice answer capture to written answer capture, but aligns with actual educational practice.

**Decision 3: Hybrid Voice + Button Control Interface**
- **Rationale:** Pure voice control can be unreliable; pure buttons lose voice interaction benefits. Hybrid approach provides reliability while maintaining natural interaction.
- **Implementation:** Primary voice commands (好了, 重读, 暂停, 不会写) with on-screen buttons as backup and shortcuts.
- **Impact:** Requires dual control path implementation but maximizes usability and reliability.

**Decision 4: ASR + LLM Intent Analysis for Voice Commands**
- **Rationale:** Children express commands naturally, not using fixed phrases. LLM intent analysis provides understanding of varied expressions.
- **Implementation:** Xiaomi Mimo ASR captures speech → Lightweight LLM analyzes intent → System executes appropriate control action.
- **Impact:** Adds LLM service dependency but enables natural voice interaction and better error handling.

**Decision 5: Adaptive Writing Time (8-15 seconds based on word characteristics)**
- **Rationale:** Fixed timing doesn't match educational reality. Simple words need less time, complex words need more.
- **Implementation:** Calculate adaptive timeout based on word length, character complexity, and historical difficulty data.
- **Impact:** Requires timeout calculation logic but provides more natural pacing and better learning experience.

**Decision 6: Photo OCR Primary + Manual Input Backup for Answer Submission**
- **Rationale:** Writing answers on paper is natural to students. OCR provides automation while manual backup ensures reliability.
- **Implementation:** Students submit photos of written work → OCR processes text → Manual input option available if OCR fails or confidence low.
- **Impact:** Requires OCR service integration but preserves traditional writing workflow while providing automation.

**Decision 7: All-at-Once Answer Submission (Not Per-Word)**
- **Rationale:** Traditional dictation involves writing all words first, then checking answers. Per-word submission interrupts flow.
- **Implementation:** Students write all words, submit together, system provides comprehensive grading and analysis.
- **Impact:** Requires batch submission logic but maintains traditional dictation rhythm and reduces interruption frequency.

**Decision 8: Automatic System Grading with Detailed Analysis Reports**
- **Rationale:** Immediate feedback is crucial for learning. Parent-only grading creates delays and reduces motivation.
- **Implementation:** Character-level comparison algorithm with detailed error analysis, learning suggestions, and performance trends.
- **Impact:** Requires grading algorithm development but provides immediate, objective feedback without parent bottleneck.

**Decision 9: Progressive 4-Phase Implementation Strategy**
- **Rationale:** Complex system needs staged rollout for risk management and learning.
- **Implementation:** Phase 1: Core dictation flow (TTS + basic controls) → Phase 2: Voice commands + intent analysis → Phase 3: Photo OCR submission → Phase 4: Advanced grading and analytics.
- **Impact:** Extends timeline but reduces risk and allows validation at each stage.

### Integration and Architecture Decisions

**Decision 10: Preserve Existing WebSocket Protocol and Session Management**
- **Rationale:** Current infrastructure (binary frames, session FSM, database schema) is well-designed and tested. Complete rewrite would waste proven investment.
- **Implementation:** Extend current WebSocket protocol for new message types while maintaining backward compatibility. Keep existing session state machine and database schema.
- **Impact:** Requires careful API extension but preserves architectural consistency and reduces regression risk.

**Decision 11: Single Student Scale with Multi-User Expansion Design**
- **Rationale:** Initial deployment is for personal use (1 student). Enterprise scale adds unnecessary complexity.
- **Implementation:** Design for single student initially but architect for multi-user expansion through session isolation and resource pooling.
- **Impact:** Reduces initial complexity while enabling future growth without major refactoring.

**Decision 12: Maintain 1C1G VPS Memory Constraints (150MB Go Service)**
- **Rationale:** Current infrastructure is cost-optimized. Scaling up would increase operational costs unnecessarily.
- **Implementation:** Use streaming services (not local models), implement efficient buffer pooling, and optimize memory for target scale.
- **Impact:** Requires careful memory management but keeps operational costs minimal.

**Decision 13: Continue Using Existing Python Bridge for Vocabulary Management**
- **Rationale:** Current vocab_manager.py integration is functional and tested. Rewriting in Go would duplicate business logic.
- **Implementation:** Maintain existing subprocess bridge pattern for vocabulary operations, adding new operations only when needed.
- **Impact:** Preserves investment but keeps hybrid Go/Python architecture.

### Service Integration Decisions

**Decision 14: Xiaomi Mimo Service Selection**
- **Rationale:** Research showed Xiaomi Mimo offers optimal combination of cost (¥0.5/hour ASR, free TTS), children's voice optimization, and OpenAI-compatible APIs.
- **Implementation:** Primary provider for both ASR and TTS services. Integration using standard HTTP/WebSocket patterns with authentication via API keys.
- **Impact:** Monthly cost ~¥10 for single student usage, expanding to ~¥400-500 for 50 students.

**Decision 15: Cost-Optimized Service Usage Strategy**
- **Rationale:** Education applications need predictable, manageable costs. 
- **Implementation:** Daily usage 10-15 minutes per student. Streaming services to minimize per-request overhead. Monitor usage and implement caching where appropriate.
- **Impact:** Predictable monthly costs with linear scaling characteristics.

---

## Testing Decisions

### Testing Philosophy

**Good tests focus on external behavior and educational outcomes, not implementation details.** Tests should validate that students can successfully complete dictation sessions and receive appropriate feedback, regardless of how individual services are implemented.

### Module Testing Strategy

**Core Speech Recognition Layer:**
- Test intent recognition with varied student expressions (not just fixed phrases)
- Validate timeout adaptation with different word characteristics  
- Test error handling for service failures and network issues
- Mock Xiaomi Mimo services for unit testing, integration testing with real services

**Session Management Layer:**
- Test state transitions through the existing FSM with new voice command events
- Validate session cleanup and timeout handling
- Test concurrent session isolation for multi-user design
- Verify database persistence and retrieval accuracy

**Answer Submission and Grading:**
- Test OCR accuracy with various handwriting samples
- Validate manual input fallback behavior
- Test grading algorithm with known answer sets
- Validate analysis report generation and accuracy

**Integration Testing:**
- End-to-end dictation flow with simulated student interactions
- WebSocket communication integrity testing
- Cross-platform testing (especially iPad Safari)
- Performance testing under expected load patterns

### Prior Art for Testing

**Existing Test Patterns:**
- Use existing `pkg/session/session_test.go` patterns for session state testing
- Follow `pkg/ws/integration_test.go` patterns for WebSocket testing
- Apply `pkg/tts/generator_test.go` service testing patterns for new service integrations
- Use existing fakes and mocks from `pkg/session/fakes_test.go` for dependency isolation

**New Test Requirements:**
- Intent recognition test fixtures with varied student expressions
- OCR accuracy testing with real handwriting samples
- Voice command integration testing with audio fixtures
- Multi-user session isolation testing patterns

### Quality Gates

**Acceptance Criteria:**
- Student can complete full dictation session with ≥90% voice command success rate
- OCR accuracy ≥85% for typical elementary student handwriting
- System maintains <1.5s response latency for TTS playback
- Memory usage stays within 150MB Go service budget
- Session cleanup works reliably under all error conditions

---

## Out of Scope

### Explicitly Excluded Features

**Advanced Speech Features:**
- Real-time pronunciation scoring and feedback (deferred to future enhancement)
- Voice answer capture for speaking practice (outside traditional dictation scope)
- Emotion detection and sentiment analysis (not required for MVP dictation)

**Scale and Performance:**
- Multi-user classroom management features (deferred to future phases)
- Real-time collaboration or competitive features (individual learning focus)
- Load balancing and high-availability deployment (single-tenant architecture sufficient)

**Content and Curriculum:**
- Vocabulary photo OCR for textbook entry (separate feature, existing in v2 requirements)
- Advanced lesson planning and curriculum management (basic session management sufficient)
- Multi-language support beyond Chinese (English and Chinese focus for MVP)

**Analytics and Insights:**
- Advanced learning analytics dashboards (basic grading and feedback sufficient)
- Long-term progress tracking and trend analysis (session-level feedback adequate)
- Parental advanced controls and monitoring (basic session review sufficient)

**Infrastructure and DevOps:**
- Automated deployment pipelines (manual deployment acceptable for single-user scale)
- Advanced monitoring and alerting systems (basic logging and health checks sufficient)
- Multi-region deployment and disaster recovery (single-region deployment acceptable)

### Technical Implementation Out of Scope

**No Rewrite of Core Infrastructure:**
- WebSocket protocol redesign (extend existing protocol only)
- Session state machine replacement (enhance existing FSM only)
- Database schema changes (add fields only, no restructuring)
- Python bridge replacement (maintain existing integration)

**No New Architectural Patterns:**
- No introduction of new communication protocols (stick to WebSocket)
- No additional database technologies (SQLite only)
- No major framework changes (maintain existing Go ecosystem)

---

## Further Notes

### Cost Analysis Summary

**Single Student Usage (Initial Deployment):**
- Xiaomi Mimo ASR: ¥0.5/hour × 7.5 hours/month = ¥3.75/month
- Xiaomi Mimo TTS: Free (promotional period)
- LLM Intent Analysis: ~¥5/month
- **Total Monthly Cost: ~¥10**

**50 Student Scale (Future Growth):**
- Xiaomi Mimo ASR: ¥0.5/hour × 375 hours/month = ¥187.5/month  
- LLM Intent Analysis: ~¥50/month
- Infrastructure overhead: ~¥100-200/month
- **Total Monthly Cost: ~¥400-500/month**

### Technical Validation Requirements

**Before Production Deployment:**
1. Validate Xiaomi Mimo services with actual children's voices (ages 6-12)
2. Test OCR accuracy with real elementary student handwriting samples
3. Conduct iPad Safari compatibility testing on actual devices
4. Validate memory usage under expected load patterns
5. Test network resilience and error handling scenarios

### Migration Strategy

**Progressive Replacement Approach:**
1. Implement new service layer alongside existing Web Speech API code
2. Add feature flags for gradual rollout
3. Monitor service quality and user acceptance
4. Remove legacy code only after validation period

### Future Enhancement Pathways

**Natural Evolution Points:**
- Add pronunciation practice mode with real-time feedback
- Integrate advanced analytics and learning insights
- Expand to multi-user classroom management
- Add collaborative learning features
- Implement advanced curriculum management

---

## Appendix: Key Technical Requirements

### Xiaomi Mimo Service Integration
- **ASR Service:** Real-time streaming speech recognition via WebSocket
- **TTS Service:** High-quality text-to-speech with Chinese children's voice optimization
- **Authentication:** API key-based authentication
- **Compatibility:** OpenAI-compatible API patterns for simplified integration

### Performance Requirements
- **Latency:** <1.5s for TTS playback initiation
- **Response Time:** <400ms for ASR intent analysis
- **Memory:** 150MB Go service budget maintained
- **Reliability:** >95% uptime for educational use

### Compatibility Requirements
- **Primary Platform:** iPad Safari (educational standard)
- **Backup Platforms:** macOS Safari, Chrome, Edge
- **Audio Format:** Web Audio API capture with WebSocket streaming
- **Network:** Standard HTTPS/WSS protocols, no special firewall requirements

---

*This specification synthesizes extensive research into industry-standard speech recognition solutions for education applications, confirmed design decisions through structured inquiry, and detailed implementation planning for production deployment.*