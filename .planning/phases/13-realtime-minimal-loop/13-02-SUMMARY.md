---
phase: 13-realtime-minimal-loop
plan: 02
subsystem: api
tags: [mimo, function-calling, openai-compatible, llm, wire-format, go, thinking-mode]

# Dependency graph
requires:
  - phase: 08（组件证据：pkg/mimo Chat/ChatVision 客户端与 ChatOption 机制）
    provides: ChatOption 直通的 buildChatRequest/executeChatRequest 调用链（本计划零改动复用）
provides:
  - mimo wire 类型 ThinkingConfig/ToolDef/ToolFunctionDef/ToolCall/ToolCallFunction
  - ChatRequest.Tools/ToolChoice/Thinking 与 Message.ToolCalls/ToolCallID（role:"tool" 回填）
  - ChatMessageContent.ToolCalls + ChatResponse Choice.FinishReason 解析
  - ChatResponse.ToolCallsRequested()/FirstToolCalls() 辅助方法
  - 选项 WithTools/WithToolChoiceAuto/WithThinkingDisabled
  - httptest wire 双向锚定测试（5 个）+ 真 key A4 烟测通道（env 门控）
affects: [13-04 (pkg/rt/turn.go 工具环), 13-07 (真机验收), RT-03, RT-04]

# Actuals (#2632) — pairs with the plan's `estimate` to calibrate future estimates.
actuals:
  tokens: 7570      # chars/4 over the realized diff (30271 diff chars)
  tasks: 3
  commits: 3        # MEASURED: git rev-list --count bd0a4b4..c055dc4 (docs commit follows)
plan_head_before: bd0a4b4aa3faa29e45fa74b221810d19dabe1adf
plan_head_after: c055dc457a544c6625cc543d6d969882833ac4d5

# Tech tracking
tech-stack:
  added: []           # 零新增依赖（13-RESEARCH Package Legitimacy Audit 全部存量 Approved）
  patterns:
    - "工具参数保持原始 JSON 字符串传输，校验延迟到 13-04 执行端（T-13-05 契约由 TestChatToolCallsResponseParse 锚定）"
    - "omitempty 全字段防漂移：无选项请求体与零值 Message 序列化结果不含任何工具键（wire 零漂移测试锚定）"

key-files:
  created:
    - pkg/mimo/chat_tools_test.go
    - pkg/mimo/chat_tools_live_test.go
  modified:
    - pkg/mimo/types.go
    - pkg/mimo/chat_test.go
    - pkg/mimo/chatvision_test.go

key-decisions:
  - "FinishReason 追加进 ChatResponse 匿名 Choice 结构体（照 plan），chat_test.go 两处与 chatvision_test.go 一处的存量匿名结构体字面量同步补字段保住类型同一性——Rule 3 编译阻塞修复，wire 语义零变化（补的值是 finish_reason:\"stop\"）"
  - "ToolCall.Function.Arguments 保持原始字符串不 eager Unmarshal——官方文档警告参数可能非法 JSON，校验归 13-04 工具执行端（威胁 T-13-05 mitigate 落地）"
  - "live 烟测复用 integration_test.go 的 getTestAPIKey 门控（MIMO_API_KEY 缺失或 -short 即 Skip）；有 key 的 A4 证据采集延后到 key 可用环境，已登记 WINDOWS.md（unrun-verify）"

patterns-established:
  - "Wire 锚定测试模式：httptest 捕获原始请求体 + map 解码断言键级形状（非仅 Go 结构体回读），响应用官方文档样例原文常量"
  - "零漂移防回归模式：新旧选项两条路径（TestChatWithoutToolsUnchanged / TestToolMessageFieldsOmittedOnEmpty）双向锁 wire"

requirements-completed: [RT-03, RT-04]

# Coverage metadata (#1602)
coverage:
  - id: D1
    description: "MiMo function-calling 与 thinking 关闭的 wire 双向支持：请求 tools/tool_choice/thinking、响应 tool_calls/finish_reason、role:tool 回填、三个 ChatOption 与两个 ChatResponse 辅助方法，存量行为零漂移"
    requirement: RT-03
    verification:
      - kind: unit
        ref: "pkg/mimo/chat_tools_test.go#TestChatToolsRequestBody"
        status: pass
      - kind: unit
        ref: "pkg/mimo/chat_tools_test.go#TestChatToolCallsResponseParse"
        status: pass
      - kind: unit
        ref: "pkg/mimo/chat_tools_test.go#TestToolMessageRoundTrip"
        status: pass
      - kind: unit
        ref: "pkg/mimo/chat_tools_test.go#TestChatWithoutToolsUnchanged"
        status: pass
      - kind: unit
        ref: "pkg/mimo/chat_tools_test.go#TestToolMessageFieldsOmittedOnEmpty"
        status: pass
      - kind: integration
        ref: "go build ./... && go vet ./pkg/mimo/ && go test ./pkg/mimo/ -short -count=1"
        status: pass
    human_judgment: false
  - id: D2
    description: "真 key 烟测通道（A4 证据）：thinking 关闭后无 reasoning_content + 工具调用质量摸底 + 默认开启延迟对比 t.Logf 证据"
    requirement: RT-04
    verification:
      - kind: integration
        ref: "go test ./pkg/mimo/ -run TestLiveChatTools -count=1 -v（无 key 环境：SKIP、退出码 0，门控行为已验证）"
        status: pass
    human_judgment: true
    rationale: "有 MIMO_API_KEY 的真机运行（含耗时毫秒数日志）需 key 可用环境；执行环境无 key，SKIP 为设计行为。A4 判定证据延后采集，已登记 .planning/WINDOWS.md unrun-verify"

# Metrics
duration: 13 min
completed: 2026-10-01
status: complete
---

# Phase 13 Plan 02: MiMo tools/thinking wire 扩展 Summary

**pkg/mimo 增加 OpenAI 风格 function calling 与 thinking 关闭的完整 wire 双向支持（tools/tool_choice/thinking 请求字段、tool_calls/finish_reason 响应解析、role:"tool" 回填），全部经既有 ChatOption 机制注入，Client 接口签名零改动，存量请求体零漂移**

## Performance

- **Duration:** 13 min
- **Started:** 2026-10-01T12:47:39Z
- **Completed:** 2026-10-01T13:00:20Z
- **Tasks:** 3
- **Files modified:** 5（新建 2 / 修改 3）

## Accomplishments
- ChatRequest 新增 Tools/ToolChoice/Thinking 字段 + Message 新增 ToolCalls/ToolCallID 回填字段 + ChatMessageContent.ToolCalls + Choice.FinishReason，新字段全部 omitempty 且带 Pitfall 引用 godoc
- 新选项 WithTools/WithToolChoiceAuto/WithThinkingDisabled 与辅助方法 ToolCallsRequested/FirstToolCalls 就位，13-04 轮次管理器可直接消费
- 5 个 httptest 测试锚定官方 Pattern 4 wire 格式双向往返，并锁定存量语音命令路径请求体零漂移（含 review L-6 的 message 级 TTS wire 防污染断言）
- 真 key 烟测（A4）通道建立：无 key / -short 环境 Skip 不失败，有 key 环境输出耗时毫秒数与 reasoning_content 有无

## Task Commits

Each task was committed atomically:

1. **Task 1: types.go — tools/thinking/tool_calls wire 类型与选项** - `7ebba3c` (feat)
2. **Task 2: chat_tools_test.go — httptest 断言 wire 请求与 tool_calls 解析** - `ba56ad4` (test)
3. **Task 3: chat_tools_live_test.go — 真 key 烟测（thinking 关闭 + 工具调用）** - `c055dc4` (test)

**Plan metadata:** 见本 SUMMARY 的 docs commit

## Files Created/Modified
- `pkg/mimo/types.go` - ThinkingConfig/ToolDef/ToolFunctionDef/ToolCall/ToolCallFunction wire 类型；ChatRequest/Message/ChatMessageContent/Choice 扩展；3 个 ChatOption；2 个辅助方法
- `pkg/mimo/chat_tools_test.go` - 5 个 wire 锚定测试（请求体捕获、Pattern 4 响应样例解析、role:tool 往返、无选项零漂移、message 级 omitempty）
- `pkg/mimo/chat_tools_live_test.go` - 真 key 烟测：thinking 关闭断言 + 默认开启延迟对比（env 门控）
- `pkg/mimo/chat_test.go` - [Rule 3] 匿名 Choice 字面量补 FinishReason 字段
- `pkg/mimo/chatvision_test.go` - [Rule 3] 同上（一处）

## Decisions Made
- FinishReason 落在匿名 Choice 结构体（照 plan 原文）；3 处存量匿名结构体字面量必须同步补字段否则编译失败——按 Rule 3 修复，测试语义不变（响应补 finish_reason:"stop"）
- TestChatToolsRequestBody 的 mock 响应取「单 choice + 空 content + finish_reason:stop」的正常形状（客户端可接受的最小响应；plan 原文"空 Choices 正常形状"按此解读——返回真正空 Choices 数组会被客户端 ErrInvalidResponse 拒绝）
- live 烟测不复制门控逻辑，直接复用 getTestAPIKey/getTestBaseURL（integration_test.go 既有范式）

## Deviations from Plan

### Auto-fixed Issues

**1. [Rule 3 - Blocking] 存量匿名 Choice 结构体字面量编译修复**
- **Found during:** Task 1（types.go 扩展后）
- **Issue:** ChatResponse.Choices 是匿名结构体切片；给该匿名结构体加 FinishReason 字段后，chat_test.go（2 处）与 chatvision_test.go（1 处）的存量字面量类型不再同一，编译失败
- **Fix:** 三处字面量类型声明同步补 `FinishReason string \`json:"finish_reason"\`` 并填 "stop"
- **Files modified:** pkg/mimo/chat_test.go, pkg/mimo/chatvision_test.go
- **Verification:** `go build ./...` 全绿；存量测试套件（-short）全绿
- **Committed in:** 7ebba3c（Task 1 commit 内）

---

**Total deviations:** 1 auto-fixed (Rule 3 blocking)
**Impact on plan:** 编译必需的最小机械修复，无语义变化、无范围蔓延。client.go 按计划零改动（buildChatRequest/executeChatRequest 直通新字段）。

## Issues Encountered
- 执行环境无 MIMO_API_KEY，live 烟测按设计 SKIP（不算失败）；有 key 运行证据（A4）延后——已登记 `.planning/WINDOWS.md`（kind: unrun-verify）
- gofmt -l 对仓库全部 .go 文件报格式差异（CRLF 检出痕迹，含本计划未触碰的 client.go/errors.go）——存量现象、超出本任务范围，未处理；`go vet ./pkg/mimo/` 通过

## User Setup Required

None - no external service configuration required.（有 key 环境跑 live 烟测：`MIMO_API_KEY=... go test ./pkg/mimo/ -run "TestLiveChat" -count=1 -v`）

## Next Phase Readiness
- wire 层就绪：13-04（pkg/rt/turn.go 工具环）可直接以 WithTools/WithThinkingDisabled 注入、经 ChatResponse.ToolCallsRequested/FirstToolCalls 驱动工具循环
- RT-03 的 3 词场景验收与 RT-04 的延迟达标判定按计划归 13-07 真机与第六周检查

## Self-Check: PASSED

- [x] pkg/mimo/chat_tools_test.go 存在（FOUND）
- [x] pkg/mimo/chat_tools_live_test.go 存在（FOUND）
- [x] pkg/mimo/types.go 含 Thinking/Tools 字段与 3 个新选项（FOUND）
- [x] commit 7ebba3c 存在（FOUND）
- [x] commit ba56ad4 存在（FOUND）
- [x] commit c055dc4 存在（FOUND）
- [x] `go test ./pkg/mimo/ -run "TestChat|TestToolMessage" -count=1` 退出码 0
- [x] 无 MIMO_API_KEY 时 `-run TestLiveChatTools -v` 输出 SKIP 且退出码 0

---
*Phase: 13-realtime-minimal-loop*
*Completed: 2026-10-01*
