# Phase 08 Validation Plan

**Phase:** 08 - Student Web Interface
**Last Updated:** 2026-08-09
**Confidence:** HIGH

## Quick Validation Commands

```bash
# 后端单元测试（静态文件服务 + WebSocket集成）
go test ./pkg/infra/... ./pkg/ws/... ./cmd/service/... -count=1

# 后端集成测试
go test ./... -race -count=1

# 前端手动测试（启动服务后）
open http://localhost:8080
```

## Success Criteria Validation

### SC-1: 学生可以通过按钮请求听写会话（"我想听写30个单词"）
- **Type:** Manual Interface Test
- **Command:** Open `http://localhost:8080` in iPad Safari, click session button
- **Verification:** Button click triggers WebSocket `start_session` message, UI updates to show session starting state
- **Automated:** No - requires browser interaction
- **Owner:** Wave 2 (08-02-PLAN.md)

### SC-2: 界面显示实时会话进度（当前单词，已完成数量）
- **Type:** Manual Interface Test
- **Command:** Simulate WebSocket progress messages via DevTools or backend test
- **Verification:** Progress bar fills as session advances, word counter updates in real-time
- **Automated:** Partial - can test state manager logic, requires browser for UI verification
- **Owner:** Wave 3 (08-03-PLAN.md)

### SC-3: 响应式设计在iPad Safari浏览器上最佳运行
- **Type:** Manual Responsive Test
- **Command:** Test in Desktop Safari Responsive Design Mode + real iPad Safari
- **Verification:** Layout adapts to iPad dimensions (768px-1024px), touch targets meet 44x44px minimum
- **Automated:** No - requires real device or simulator testing
- **Owner:** Wave 1 (08-01-PLAN.md) + Wave 4 (08-04-PLAN.md)

### SC-4: 界面优雅处理WebSocket重连
- **Type:** Manual Reconnection Test
- **Command:** Simulate network disconnect in Safari DevTools, observe reconnection behavior
- **Verification:** Client attempts reconnection with exponential backoff, queues messages, displays connection status
- **Automated:** Partial - can unit test reconnection logic, full flow requires browser
- **Owner:** Wave 1 (08-01-PLAN.md)

## Requirements Traceability

| Req ID | Description | Test Type | Command | Status |
|--------|-------------|-----------|---------|--------|
| WEB-02 | 学生可以通过按钮请求听写会话 | Manual | Open index.html, click session button | ❌ Wave 0 |
| WEB-04 | 界面显示实时会话进度 | Manual | Simulate progress updates | ❌ Wave 0 |
| WEB-06 | 响应式设计在iPad Safari上最佳运行 | Manual | Responsive Design Mode + iPad | ❌ Wave 0 |
| WEB-08 | 界面优雅处理WebSocket重连 | Manual | Network disconnect test | ❌ Wave 0 |
| DICT-01 | 会话请求功能实现 | Integration | WebSocket message flow test | ❌ Wave 0 |
| STATIC-01 | 静态文件正确服务 | Unit | `go test ./pkg/infra -run TestFileServer` | ❌ Wave 0 |
| STATIC-02 | 静态文件路由不与WebSocket冲突 | Unit | `go test ./pkg/infra -run TestStaticRouteConflict` | ❌ Wave 0 |

## Per-Wave Validation Gates

### Wave 0 (Pre-Implementation)
- [ ] Create `pkg/infra/server_test.go` for static file serving tests
- [ ] Create `tests/test-state-manager.html` for state manager unit tests
- [ ] Create `tests/phase08-manual-test.html` for manual testing checklist
- [ ] Verify Go test framework: `go test ./pkg/infra/... -count=1`

### Wave 1 (Foundation)
- [ ] Static file serving: `go test ./pkg/infra -run TestFileServer -count=1`
- [ ] Static route conflict: `go test ./pkg/infra -run TestStaticRouteConflict -count=1`
- [ ] Open `http://localhost:8080` - page loads without errors
- [ ] Responsive layout: Test in Safari Responsive Design Mode

### Wave 2 (Session Request)
- [ ] WebSocket client connection: DevTools shows WS connection established
- [ ] Session button click: Triggers `start_session` message
- [ ] State manager updates: UI reflects session starting state
- [ ] Manual: Click button, observe WebSocket message

### Wave 3 (Progress Display)
- [ ] Progress bar updates: Simulate progress messages
- [ ] Word counter: Updates correctly with progress data
- [ ] State synchronization: Multiple UI components stay in sync
- [ ] Manual: Send test WebSocket messages, verify UI updates

### Wave 4 (Audio & Polish)
- [ ] Web Audio API integration: Audio plays without gaps
- [ ] iOS gesture unlock: Three-phase unlock works on iPad
- [ ] Touch target verification: All buttons ≥44x44px
- [ ] Reconnection stress test: Multiple disconnect/reconnect cycles
- [ ] Manual: Full session on iPad Safari from start to finish

## Nyquist Compliance

**Automated Test Coverage:**
- Static file serving: ✅ Unit tests (pkg/infra/server_test.go)
- WebSocket integration: ✅ Integration tests (pkg/ws/ - existing from Phase 2)
- State manager logic: ✅ Browser-based unit tests (tests/test-state-manager.html)

**Manual Test Coverage:**
- UI interaction flows: ✅ Manual checklist (tests/phase08-manual-test.html)
- Responsive design: ✅ iPad Safari testing
- Real-time behavior: ✅ Live WebSocket session testing

**Fast Feedback Loops:**
- Per-commit: `go test ./pkg/infra/... -count=1` (<3 seconds)
- Per-wave: `go test ./... -count=1` + manual interface check (<15 seconds)
- Phase gate: Full automated suite + iPad Safari manual verification

## Security Validation

### Static File Security
- [ ] Path traversal prevention: `http.FileServer` properly restricted
- [ ] Directory validation: Static path must be absolute and within expected bounds
- [ ] Test: Attempt `../../etc/passwd` access, verify blocked

### Frontend Security
- [ ] XSS prevention: No `innerHTML` usage with user data
- [ ] WebSocket message validation: Message format and size limits enforced
- [ ] Origin validation: WebSocket server checks request origin

### Test Commands
```bash
# Static file security test
go test ./pkg/infra -run TestStaticFileSecurity -count=1

# WebSocket message validation
go test ./pkg/ws -run TestMessageValidation -count=1
```

## Performance Validation

### Memory Constraints
- [ ] Frontend bundle size: <100KB (vanilla JS + CSS)
- [ ] WebSocket message queue: Max 100 messages (~6KB)
- [ ] State manager memory: Single state object (<1KB)

### Latency Targets
- [ ] Button click to WebSocket send: <50ms
- [ ] WebSocket message to UI update: <100ms
- [ ] Page load to interactive: <2s on typical WiFi

### Test Commands
```bash
# Bundle size check
ls -lh pkg/web/static/js/*.js pkg/web/static/styles/*.css

# Memory profiling (if needed)
go test ./pkg/infra -memprofile=mem.prof
```

## Device-Specific Validation

### iPad Safari Requirements
- [ ] iOS Version: Test on iOS 14.5+ (Phase 7.1 verified on iPadOS 26.5.2)
- [ ] Screen Sizes: 768px-1024px (iPad), 1024px-1366px (iPad Pro)
- [ ] Orientation: Portrait and landscape both functional
- [ ] Touch Interaction: All buttons meet 44x44px minimum

### Test Devices
- **Primary:** Real iPad device (preferred) or iOS Simulator
- **Secondary:** Desktop Safari Responsive Design Mode
- **Fallback:** Chrome DevTools for CSS debugging only

## Regression Prevention

### Integration with Existing Phases
- [ ] Phase 2 WebSocket: No breaking changes to binary protocol
- [ ] Phase 6 TTS Streaming: Audio frame format unchanged
- [ ] Phase 7 Session State: State transitions compatible with frontend
- [ ] Phase 7.1 iOS Fixes: Audio unlock patterns preserved

### Backwards Compatibility Tests
```bash
# Phase 2 WebSocket compatibility
go test ./pkg/ws -run TestBinaryProtocol -count=1

# Phase 6 audio streaming compatibility
go test ./pkg/tts -run TestPCMFrameFormat -count=1

# Phase 7 session state compatibility
go test ./pkg/session -run TestStateTransitions -count=1
```

## Sign-off Criteria

Phase 8 is complete when:

1. **All automated tests pass:** `go test ./... -race -count=1`
2. **Manual checklist complete:** All items in `tests/phase08-manual-test.html` verified
3. **iPad Safari validated:** Full session workflow tested on real device or simulator
4. **Security controls verified:** Static file and WebSocket security tests pass
5. **No regressions:** Existing Phase 2/6/7 tests still pass
6. **Performance targets met:** Bundle size, latency, memory constraints satisfied

---

**Next Steps:**
1. Run `/gsd-execute-phase 08-student-web-interface` to begin implementation
2. Execute waves in sequence: Wave 0 → Wave 1 → Wave 2 → Wave 3 → Wave 4
3. Run validation commands after each wave completion
4. Complete sign-off criteria before phase transition