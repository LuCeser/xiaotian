# Phase 13: R0 真机最小闭环 - Pattern Map

**Mapped:** 2026-10-01
**Files analyzed:** 15（新建 12 / 修改 3）
**Analogs found:** 12 / 15（3 项无对应存量 → 见 No Analog Found）

> 路径勘误：13-RESEARCH.md 结构图写的是 `web/static/`，实际前端目录是 **`pkg/web/static/`**（已逐文件核实）。计划一律按 `pkg/web/static/` 落地。

## File Classification

| New/Modified File | Role | Data Flow | Closest Analog | Match Quality |
|-------------------|------|-----------|----------------|---------------|
| `pkg/rt/conn.go` | transport (WS endpoint) | streaming (bidirectional) | `pkg/ws/server.go` | exact（升级/读限/心跳/origin；不复制其帧协议） |
| `pkg/rt/protocol.go` | protocol types | request-response (JSON ctrl) | `pkg/ws/protocol.go`（反例参照）+ mimo wire types | partial |
| `pkg/rt/turn.go` | service (turn FSM) | event-driven | 无（绿地） | none |
| `pkg/rt/vad.go` | utility (DSP) | streaming | 无 | none |
| `pkg/rt/providers.go` | provider interfaces | request-response | `pkg/mimo/types.go` Client interface | exact |
| `pkg/rt/timing.go` | utility (event logging) | event-driven | `pkg/infra/logging.go` Logger 用法 | role-match |
| `pkg/lesson/store.go` | model/repository | CRUD | `pkg/database/session_repo.go` + `schema.go` | exact |
| `pkg/lesson/executor.go` | service (tool exec, 幂等) | CRUD (transactional) | `pkg/database/session_repo.go`（RowsAffected=0 判定模式） | role-match |
| `pkg/mimo/client.go`+`types.go`（扩展 ChatWithTools） | provider client | request-response | 自身 Chat/ChatVision | exact |
| `pkg/web/static/tutor.html` + `js/rt-*.js`（4 个） | component (frontend) | streaming | `pkg/web/static/js/audio-controller.js` + `voice-command-controller.js` | exact |
| `cmd/service/main.go`（挂 /rt + TLS 分支） | config/wiring | request-response | 自身 /ws 挂载段 | exact |
| `docs/DEPLOYMENT.md`（补 TLS） | docs | — | 自身 | n/a |
| `tests/test-rt-*.js` / `tests/e2e/rt-loop.spec.js` | test | — | `pkg/mimo/chat_test.go` + 存量 playwright | role-match |

**冻结不扩展**：`pkg/ws` 帧协议、`pkg/voice`、`pkg/session`（ADR-0002）。只可"抄参数/抄模式"，不可 import 其 Hub/Router/FSM。

## Pattern Assignments

### `pkg/rt/conn.go`（transport, streaming）

**Analog:** `pkg/ws/server.go`（升级与连接治理段落；**不抄** Hub/Router/帧协议）

**Origin 白名单 + Safari 兼容**（`pkg/ws/server.go` lines 116-163）——注意 line 135 关键注释：`EnableCompression: false` 是 Safari TEXT→BINARY 帧切换协议错误的规避，/rt 同样 JSON+二进制混发，**必须照抄 false**；CheckOrigin 两段式匹配（精确 → 去端口回退）与空 Origin 拒绝也整体照抄：

```go
upgrader: websocket.Upgrader{
    ReadBufferSize:  cfg.ReadBufferSize,
    WriteBufferSize: cfg.WriteBufferSize,
    EnableCompression: false, // Safari: TEXT 控制帧后接 BINARY 音频帧时 permessage-deflate 触发 "Protocol error"
    CheckOrigin: func(r *http.Request) bool {
        origin := r.Header.Get("Origin")
        if origin == "" { return false } // 非浏览器客户端拒绝
        if allowedOrigins[origin] { return true }
        if stripped := stripOriginPort(origin); stripped != origin && allowedOrigins[stripped] { return true }
        return false
    },
    HandshakeTimeout: cfg.HandshakeTimeout,
}
```

**读限 + 滚动 read deadline + 心跳**（lines 199-215, 262-264）：

```go
conn.SetReadLimit(s.cfg.MaxMessageBytes)
readTimeout := s.cfg.HeartbeatInterval * 2
conn.SetReadDeadline(time.Now().Add(readTimeout))
conn.SetPongHandler(func(string) error {
    conn.SetReadDeadline(time.Now().Add(readTimeout))
    return nil
})
// go Heartbeat(ctx, conn, s.cfg.HeartbeatInterval, s.logger)
```

**读写循环骨架**（RESEARCH Pattern 1，gorilla Text/Binary 天然分流）：

```go
for {
    mt, data, err := conn.ReadMessage()
    if err != nil { /* 释放会话 */ break }
    switch mt {
    case websocket.BinaryMessage: tm.IngestAudio(data)
    case websocket.TextMessage:   tm.HandleControl(data)
    }
}
```

**Origin 配置来源**：`cmd/service/main.go` line 346 `AllowedOrigins: cfg.WS.AllowedOrigins` —— /rt 复用同一 config 字段语义（禁 "*"）。

---

### `pkg/mimo` 扩展：ChatWithTools + thinking（provider client, request-response）

**Analog:** 自身 `Chat`/`ChatVision`（`pkg/mimo/client.go`）——四段式结构逐字照抄：retry 循环（lines 252-294，指数退避 + `IsRetryable` 短路 + `ctx.Done()` 中断）→ `buildXxxRequest`（options 模式）→ `executeXxxRequest`（lines 312-349：marshal → `http.NewRequestWithContext` → Bearer 头 → `handleAPIError` → decode → `len(Choices)==0` 校验）→ types.go 增加请求/响应 struct。

**types.go 扩展点**（现 `ChatRequest` lines 182-192 只有 Model/Messages/MaxTokens/Temperature）：

```go
// 新增字段（wire 格式见 13-RESEARCH.md Pattern 4）：
type ChatRequest struct {
    ...
    Tools    []ToolDef  `json:"tools,omitempty"`
    ToolChoice any      `json:"tool_choice,omitempty"` // 仅 auto 有效，Pitfall 7
    Thinking *Thinking  `json:"thinking,omitempty"`    // {"type":"disabled"} 关思考模式
}
// ChatMessageContent 需补 ToolCalls []*ToolCall `json:"tool_calls,omitempty"`
// 及 FinishReason；新增 role:"tool" 消息类型（ToolCallID + Content）
```

**Option 模式**照抄 `WithChatModel`/`WithMaxTokens`（types.go lines 356-367）：`WithTools([]ToolDef)`、`WithThinkingDisabled()`。

**错误处理**：直接复用 `handleAPIError`（client.go lines 809-852，含 429 Retry-After 映射）与 `pkg/mimo/errors.go` 的 `IsRetryable`，不新建。

**TTS 流式复用**：`SynthesizeStream`（client.go lines 128-175）已实现 pcm16 SSE（`parseSSEStream` lines 855-906 + `StreamChunk.UnmarshalJSON` types.go lines 427-449）——逐句合成直接调它，不重写。

---

### `pkg/lesson/store.go` + `executor.go`（model + service, CRUD）

**Analog:** `pkg/database/session_repo.go` + `schema.go` + `migrate.go`

**DDL 注册**（照 `schema.go` lines 71-93 的 `CREATE TABLE IF NOT EXISTS` 常量 + `migrate.go` lines 18-33 的幂等语句列表）：

```go
lessonsDDL = `CREATE TABLE IF NOT EXISTS lessons (
    lesson_id TEXT PRIMARY KEY,
    words TEXT NOT NULL,            -- JSON array
    current_index INTEGER NOT NULL DEFAULT 0,
    version INTEGER NOT NULL DEFAULT 1,
    created_at TEXT NOT NULL DEFAULT (datetime('now')),
    updated_at TEXT NOT NULL DEFAULT (datetime('now'))
)`
toolRequestsDDL = `CREATE TABLE IF NOT EXISTS tool_requests (
    request_id TEXT PRIMARY KEY,    -- MiMo tool_call id
    lesson_id TEXT NOT NULL,
    tool TEXT NOT NULL,
    result TEXT NOT NULL,
    created_at TEXT NOT NULL DEFAULT (datetime('now'))
)`
```

**Repository 结构**照抄 `session_repo.go`：sentinel errors + `errors.Is` 分支（lines 14-22）、interface 定义 + 编译期检查 `var _ SessionRepository = (*sqliteSessionRepository)(nil)`（line 64）、`NewXxx(db, logger)` nil-guard + NoOpLogger 兜底（lines 76-84）、`infra.F("component", "database/session_repo")` 结构化日志字段。

**乐观并发判定**照抄 `UpdateState` 的 RowsAffected 模式（lines 155-179）：

```go
result, err := r.db.ExecContext(ctx,
    "UPDATE lessons SET current_index=current_index+1, version=version+1, updated_at=datetime('now') WHERE lesson_id=? AND version=?",
    lessonID, expectedVersion)
rows, _ := result.RowsAffected()
if rows == 0 { return ErrVersionConflict /* + 回读 current_version */ }
```

**幂等事务**（RESEARCH Pattern 5 SQL）：单事务内先 INSERT tool_requests（唯一键冲突 ⇒ 回滚并 SELECT 原 result 返回），再 UPDATE 带 version 条件。用 `db.BeginTx` 包裹两条语句。

---

### `pkg/web/static/js/rt-capture.js` / `rt-playback.js` / `rt-client.js` / `rt-pcm-worklet.js`（component, streaming）

**Analog 1:** `pkg/web/static/js/audio-controller.js` —— iOS 手势解锁与 generation 令牌。

解锁三条件（lines 121-169，**必须同步在手势内执行、无 await**）：

```js
this.audioCtx = new AudioContextClass({ sampleRate: 16000 });
if (this.audioCtx.sampleRate !== 16000) { /* D-02: iOS 可能重协商，记录告警 */ }
this.audioCtx.resume();                      // 条件2：同步，不 await
var silentBuf = this.audioCtx.createBuffer(1, 1, this.audioCtx.sampleRate);
silentBuf.getChannelData(0)[0] = 0;          // 条件3：真实 buffer start
silentSource.start(0);
```

Generation 令牌（lines 47-52, 387-398, 467-468）——打断"旧音频不复活"的客户端半边：

```js
var generation = this._playbackGeneration;   // 捕获当下令牌
// ... 所有异步回调开头：
if (self._playbackGeneration !== generation) { return; } // 旧 onended 作废
// stop()/打断： this._playbackGeneration += 1;
```

visibilitychange → `resume()` 不重建（lines 499-568，D-06）也照抄。

**Analog 2:** `pkg/web/static/js/voice-command-controller.js` —— 采集与降采样。

getUserMedia（lines 139-153）：AudioContext 在 await **之前**构造（手势栈内出生）；约束三元组 `echoCancellation/noiseSuppression/autoGainControl: true`；`NotAllowedError` 与其他错误分流为 `mic-denied`/`mic-unavailable`。

ScriptProcessor 消音接线（lines 159-173）：`source → processor → gain(0) → destination`（processor 必须连 destination 才拉数据）。R0 持续采集首选 AudioWorklet（Safari 14.1+，独立 worklet 模块文件 `rt-pcm-worklet.js`），运行时特性检测回退到此 ScriptProcessor 模式。

降采样（lines 281-309）：`ratio = audioCtx.sampleRate / 16000` 线性插值 + `_tail` 残余缓冲，**整段照抄**（session_init 协商的客户端依据即 `audioCtx.sampleRate` 实测值）。Float32→Int16 钳位转换（lines 262-267）照抄。

**RT-01 检测**：`supported = typeof navigator.mediaDevices?.getUserMedia === 'function'`（lines 77-80 注释模式）——HTTP 下显示"语音不可用原因"。

---

### `pkg/rt/providers.go`（provider interfaces）

**Analog:** `pkg/mimo/types.go` `Client` interface（lines 314-339）——Go interface + 具体实现 + 编译期检查的组合方式；方法签名风格带 `ctx` 首参 + options 尾参。三个接口（ASRProvider/LLMProvider/TTSSynthesizer）签名直接对齐 mimo 现有方法（`Transcribe`/`Chat`/`SynthesizeStream`），使 `pkg/mimo.Client` 天然满足接口（`var _ rt.LLMProvider = mimoClient`），mock providers 由此注入。

---

### `cmd/service/main.go` 修改（挂 /rt + TLS）

**Analog:** 自身 /ws 挂载段（lines 500-559）。

- /rt 路由：`publicMux.HandleFunc("/rt", rtServer.HandleWebSocket)` —— 与 `/ws`（line 505）并列；ServeMux 前缀匹配天然与静态路由共存（line 521-524 注释即此保证）。
- TLS 分支：`publicSrv.ListenAndServe()`（line 553）改为 `cfg.Server.TLS.CertFile != "" 时 ListenAndServeTLS(cert, key)`；h2 排查项（RESEARCH Pitfall 8：`TLSNextProto: map[string]func(...){}`）写入 TLS 失败排查注释。
- mimo client 构造：lines 400-409 的 `os.Getenv("MIMO_API_KEY")` 无 key 可启动模式照抄（RT-10 降级语义）。

### `pkg/rt/turn.go` / `vad.go` / `timing.go`

无存量 Go analog（见下表），实现以 13-RESEARCH.md Pattern 2/3（epoch 取消、半双工门控）、Code Examples 三段为准；timing 事件发射用 `infra.Logger` 的 `infra.F(k,v)` 字段模式（全库统一）。

## Shared Patterns

### Origin 白名单 / 资源保护（Apply to: pkg/rt/conn.go, main.go）
Source: `pkg/ws/server.go` lines 41-67（Validate 禁 "*"）+ 116-163 + 199-215。整段模式（含 EnableCompression:false、SetReadLimit、pong 滚动 deadline）照抄参数，不引入其帧协议。

### 错误处理（Apply to: 全部新 Go 文件）
- 供应商错误：`pkg/mimo/errors.go` sentinel + `IsRetryable` + `handleAPIError`（client.go 809-852），禁止字符串匹配。
- 仓储错误：sentinel + `errors.Is`（session_repo.go lines 14-22）；`errors.Wrap(err, "动词短语")` 包装。
- 结构化日志：`infra.F("component", "pkg/rt.turn")` + `infra.ErrField(err)`（全库统一，见 client.go 各 catch 段）。

### 重试契约（Apply to: 所有 MiMo HTTP 调用）
`pkg/mimo/client.go` lines 82-124：MaxRetries 循环 + 指数退避 + `ctx.Done()` 提前退出 + 非可重试错误立即失败。扩展方法不另起炉灶。

### 前端 generation 令牌 + iOS 音频生命周期（Apply to: rt-playback.js, rt-capture.js, tutor.html）
`audio-controller.js` D-03 解锁 / P1-1 令牌 / D-06 visibilitychange 三件套整体沿用；打断 = 令牌自增 + 立即 stopNow + 清队 + interrupt 消息（RESEARCH Code Example 2）。

### 幂等与版本冲突（Apply to: pkg/lesson/executor.go）
INSERT 唯一键 + UPDATE...WHERE version=? 各司其职（RESEARCH Pattern 5 SQL 原文执行）；冲突返回结构化 JSON 给 LLM，不 panic 不重试写。

## No Analog Found

| File | Role | Data Flow | Reason |
|------|------|-----------|--------|
| `pkg/rt/turn.go` | service (turn FSM + epoch 取消) | event-driven | 旧 pkg/session FSM 属冻结栈且为按钮驱动，架构不兼容；以 RESEARCH Pattern 2/3 + Code Examples 为准 |
| `pkg/rt/vad.go` | utility (能量 VAD) | streaming | 仓库无任何 DSP；RESEARCH Alternatives 已锁定自研 ~100 行能量法（hysteresis/hangover/pre-roll） |
| `pkg/rt/protocol.go` | protocol types | request-response | 旧 16 字节二进制帧协议被 ADR-0002 否决，仅作反例；JSON 消息集以 RESEARCH Pattern 6 消息示例为雏形 |

## Metadata

**Analog search scope:** pkg/mimo, pkg/ws, pkg/database, pkg/infra, pkg/web/static/js, cmd/service（RESEARCH 已逐文件读过库，本次抽样复核 7 个关键文件）
**Files scanned:** 9 深读 + grep 扫描 2 个前端大文件
**Pattern extraction date:** 2026-10-01
