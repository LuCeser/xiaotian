---
phase: 13-realtime-minimal-loop
plan: 05
subsystem: ui
tags: [websocket, tutor-frontend, audioworklet, pcm-audio, interruption, timing, playwright-e2e]

requires:
  - phase: 13-realtime-minimal-loop/13-03
    provides: pkg/rt 协议契约（17 消息 JSON wire 字段 + 二进制 PCM 约定 + session_init 协商 + ping/pong 时钟探针）
provides:
  - /tutor/ 自然实时家教前端页（一次手势授权、持续听说、收即播、可打断、错误人话化）
  - rt-client.js（RTCClient 协议分派 + TimingLog JSONL 时序证据 + estimateClockOffset 纯函数）
  - rt-capture.js + rt-pcm-worklet.js（AudioWorklet 首选/ScriptProcessor 回退持续采集 + 实测采样率降采样 16k s16le）
  - rt-playback.js（24kHz 收即播调度 + P1-1 generation 令牌 + RMS 打断检测 + sentence_ack + 队列上限）
  - tests/e2e-rt/ mock e2e 套件（10 轮环/打断/结束三用例，独立 testDir 零污染存量 e2e）
  - 旧学生页降级模式横幅（index-banner.js，RT-10 显式标注）
affects: [13-realtime-minimal-loop/13-06 (挂载 /rt 后前端可直接对真实服务), 13-realtime-minimal-loop/13-07 (真机验收消费时序导出与 URL 标定参数)]

actuals:
  tokens: 28800    # chars/4 over the realized diff (115143 chars / 4)
  tasks: 3
  commits: 3       # MEASURED: git rev-list --count c7c4741..HEAD
plan_head_before: c7c47417a30e83c829f18f055a6c5d8f507f5b0e
plan_head_after: 4a9f98fbcf56bdbc275b38b57da4cae5fe3b4334

tech-stack:
  added: []         # 零新增依赖（ws ^8.21.3 与 @playwright/test 为存量）
  patterns:
    - "generation 令牌从 audio-controller.js P1-1 推广到整段播放生命周期：stopNow 自增 + 全异步回调首行比对 + epoch 门双防线（迟到 chunk 丢弃）"
    - "URL 参数标定通道（interrupt_rms / interrupt_sustain_ms / queue_max）：真机阈值调参不动代码，e2e 复用同一通道禁用自动检测"
    - "mock 服务静态根 = pkg/web/static：e2e 页面 URL 形态与生产完全一致（/tutor/ + 相对资产路径）"
    - "Playwright webServer 探针的 NO_PROXY 回环守卫：系统代理（Privoxy）环境下 webServer 探活/页面请求需显式回环豁免"

key-files:
  created:
    - pkg/web/static/tutor/index.html
    - pkg/web/static/tutor/styles/tutor.css
    - pkg/web/static/tutor/js/rt-client.js
    - pkg/web/static/tutor/js/rt-capture.js
    - pkg/web/static/tutor/js/rt-playback.js
    - pkg/web/static/tutor/js/rt-pcm-worklet.js
    - pkg/web/static/js/index-banner.js
    - tests/test-rt-timing.js
    - tests/test-rt-playback.js
    - tests/e2e-rt/mock-rt-server.js
    - tests/e2e-rt/rt-loop.spec.js
    - playwright.rt.config.js
  modified:
    - pkg/web/static/index.html
    - package.json

key-decisions:
  - "rt-pcm-worklet.js 在 index.html 中以注释形式引用而非 <script> 标签——registerProcessor 作用域没有 AudioWorkletProcessor，普通 script 加载会抛 ReferenceError 触发 pageerror；实际加载走 rt-capture.js 的 audioWorklet.addModule"
  - "stopNow 的 interrupt_stop 事件仅在播放确实存活时记录（playing 或在飞 source > 0）——会话结束对静默播放器的 stopNow 不再制造无配对的 interrupt_stop，保持 RT-04 打断对证据干净"
  - "e2e 页面以 ?interrupt_rms=99&interrupt_sustain_ms=999999 禁用 RMS 自动打断——Chromium fake 音频设备输出恒定音调会让 10 轮环非确定性自打断；确定性打断路径是 interruptNow()，且该 URL 覆盖通道正是 13-07 真机标定机制（一次测试同时验证了标定入口）"
  - "resampleTo16k 照抄存量的每次调用 floor() 亚样本损失（voice-command-controller 同款已上线行为）——/rt 无固定 wire 帧长，320 样本 emit 块仅为避免碎片帧并给残余缓冲赋予语义"
  - "RT-01/RT-02/RT-05/RT-10 不在本次 mark-complete——四 ID 均被 13-06/13-07 等共同声明且其 SUMMARY 未产出（共享 ID 门 #2388 阻塞）；计划 Flagged Assumptions 明确真机验收归 13-07"
  - "playwright.rt.config.js 在 config 加载期向 NO_PROXY 追加 127.0.0.1,localhost——本机 HTTP_PROXY 指向 Privoxy 且无回环豁免时，Playwright 自身探活/请求经代理得 500，套件必然超时（Rule 3 修正，见 Deviations）"

patterns-established:
  - "一次手势授权全生涯持有：unlock 三条件同步执行 → getUserMedia 异步 → 流与 AudioContext 全程持有，visibilitychange 只 resume 不重建（D-06）"
  - "会话结束即释放麦克风：capture.stop() 停全部 MediaStreamTrack 并断开图，AudioContext 保留——儿童音频隐私边界有 e2e 断言锚定"
  - "纯函数与浏览器胶水分离：TimingLog/resampleTo16k/floatToInt16/playedUntilMs/estimateClockOffset/applyTuning 全部 node 可测，DOM/WS 下载胶水隔离"

requirements-completed: [RT-01, RT-02, RT-05, RT-10]

coverage:
  - id: D1
    description: "/tutor/ 独立自然家教入口页：状态条三态+连接态、字幕区、课堂进度、错误面板、>=60px 控件、RT-01 非安全上下文提示并禁用开始按钮"
    requirement: RT-01
    verification:
      - kind: automated_ui
        ref: "tests/e2e-rt/rt-loop.spec.js#10-round loop runs on a single getUserMedia authorization（body[data-page=tutor] 可见、开始/结束按钮流转）"
        status: pass
      - kind: unit
        ref: "tests/test-rt-timing.js#isVoiceCaptureAvailable 安全上下文探针真假分支"
        status: pass
    human_judgment: true
    rationale: "非安全上下文的页面级降级呈现（HTTP 下提示文案与禁用按钮的视觉表现）无法在 Chromium 127.0.0.1 安全上下文中自动化复现；真机 HTTPS/HTTP 对比归 13-07 Task 2"
  - id: D2
    description: "rt-client.js 协议客户端 + TimingLog 时序证据 + ping/pong 时钟偏移估计（RT-04 客户端半边，M-6）"
    requirement: RT-02
    verification:
      - kind: unit
        ref: "tests/test-rt-timing.js#TimingLog JSONL 行格式/无服务端字段/estimateClockOffset 已知数值与零 RTT 对称用例"
        status: pass
      - kind: automated_ui
        ref: "tests/e2e-rt/rt-loop.spec.js#clock_offset poll >= 1（mock 回 pong 带 hrtime 单调戳）"
        status: pass
    human_judgment: false
  - id: D3
    description: "一次手势授权的持续采集：iOS 三条件同步解锁、getUserMedia 单次调用贯穿 10 轮、实测采样率降采样 16k s16le 上行"
    requirement: RT-02
    verification:
      - kind: unit
        ref: "tests/test-rt-playback.js#resampleTo16k 48k 正弦 1 秒→16000 样本<2% 误差 + tail 块语义 + floatToInt16 钳位"
        status: pass
      - kind: automated_ui
        ref: "tests/e2e-rt/rt-loop.spec.js#__gumCallCount === 1 贯穿 10 轮 + capture.isCapturing 持真"
        status: pass
    human_judgment: false
  - id: D4
    description: "24kHz 收即播调度 + generation 令牌旧回调不复活 + epoch 门迟到 chunk 丢弃 + RMS 打断闭环 + sentence_ack + 64 chunk 队列上限（T-13-06c）"
    requirement: RT-05
    verification:
      - kind: unit
        ref: "tests/test-rt-playback.js#generation 令牌/迟到 chunk/队列上限/playedUntilMs/RMS 三态窗口"
        status: pass
      - kind: automated_ui
        ref: "tests/e2e-rt/rt-loop.spec.js#interrupt during playback（interrupt 消息三字段、epoch 递增、新一轮渲染）"
        status: pass
    human_judgment: false
  - id: D5
    description: "旧学生页降级模式横幅（不计语音验收、指向 /tutor/）与麦克风错误/服务端错误的人话呈现（RT-10 客户端半边）"
    requirement: RT-10
    verification:
      - kind: unit
        ref: "tests/test-rt-timing.js#humanizeError 全词表中文映射 + 未知码回退"
        status: pass
      - kind: manual_procedural
        ref: "grep 断言：index.html 引入 index-banner.js；banner 文案含「降级」与 /tutor/ 链接（Task 1 验收执行通过）"
        status: pass
    human_judgment: true
    rationale: "横幅视觉呈现与真实麦克风拒绝/服务端故障下学生看到的错误面板表现需人工抽查；真实故障表现归 13-07 Task 3 步骤 7"

duration: 8h 31m
completed: 2026-10-02
status: complete
---

# Phase 13 Plan 05: 前端三件套 + tutor 页 + mock e2e Summary

**/tutor/ 自然实时家教前端：一次手势授权持续听说（getUserMedia 全程单次）、AudioWorklet 采集降采样 16k 上行、24kHz 收即播 + generation 令牌打断不复活、RMS 插话检测 + interrupt 三字段上报、TimingLog/clock_offset 时序证据，mock WSS e2e 三用例（10 轮环/打断/结束）全绿且存量 e2e 零拾取**

## Performance

- **Duration:** 8h 31m（挂钟时间，含一次 API 限流中断等待；实际执行 ~2h）
- **Started:** 2026-10-01T16:01:54Z
- **Completed:** 2026-10-02T00:40:25Z
- **Tasks:** 3 / 3
- **Files:** 14（12 新建 + 2 修改），+2886 行

## Accomplishments

- /tutor/ 入口页落地 RT-01：非安全上下文（navigator.mediaDevices 缺失）显示"语音不可用：需要 HTTPS"人话原因并禁用开始按钮，不白屏不留必败按钮；连接态/三态状态条/字幕区/课堂进度条/错误面板齐备
- rt-client.js 逐字镜像 pkg/rt/protocol.go wire 契约（session_init 声明 16000/s16le/1ch、pong 回显 t_client_ms + 服务端单调 ns），session_ready 后立即 ping + 每 10s 刷新，clock_offset {offset_ms, rtt_ms} 入 TimingLog——RT-04 跨机「句末→首实际可听」测量的客户端基础（review M-6 裁定闭环）
- RT-02 客户端半边：iOS 三条件解锁同步执行于单次点击、getUserMedia 全程一次（e2e 计数器断言贯穿 10 轮）、AudioWorklet 首选/ScriptProcessor 消音回退、audioCtx.sampleRate 实测值降采样（Pitfall 5 不假设 16k）
- RT-05 客户端半边：迟到 chunk 按 epoch 门丢弃、stale onended 因 generation 令牌失效（node 测试以纯状态对象证明 stopNow 后回调无效）、打断上报携带 epoch/last_sentence_id/played_until_ms、句完成发 sentence_ack、下行洪泛被 64 chunk 队列上限截断并计数（T-13-06c 缓解落地）
- mock e2e 三用例全绿：10 轮环无二次授权 + clock_offset 证据、interruptNow 打断闭环（mock 侧 epoch 递增断言 + DOM 新一轮回显）、结束按钮→session_ended→麦克风释放（儿童音频隐私边界有断言）
- 目录隔离成立（review H-1）：spec 与 mock 服务物理位于 tests/e2e-rt/，playwright.rt.config.js 独立 testDir；存量 `npx playwright test --list` 输出零 rt-loop，存量 e2e 配置零改动
- 旧学生页横幅明示降级备用、不计语音验收、链接 /tutor/（RT-10 显式标注，13-CONTEXT 决策）

## Task Commits

Each task was committed atomically:

1. **Task 1: rt-client.js + tutor 页骨架 + 旧页降级横幅** - `45300ea` (feat)
2. **Task 2: rt-capture.js + rt-pcm-worklet.js + rt-playback.js** - `4c831c0` (feat)
3. **Task 3: node 单测 + mock WSS e2e + 独立 rt 配置** - `4a9f98f` (test)

**Plan metadata:** 本 SUMMARY 提交（docs）

## Files Created/Modified

- `pkg/web/static/tutor/index.html` - /tutor/ 入口页：三态状态条、字幕、进度、错误面板、>=60px 控件、内联启动脚本（安全上下文检测 + 全部消息→DOM 接线 + __rtTutor e2e 钩子）
- `pkg/web/static/tutor/styles/tutor.css` - iPad 单列布局、状态色、横幅/错误/通知样式
- `pkg/web/static/tutor/js/rt-client.js` - RTCClient（/rt WSS + JSON 分派 + ping 循环）、TimingLog（JSONL 导出）、estimateClockOffset、错误码人话映射、isVoiceCaptureAvailable、deriveRtUrl
- `pkg/web/static/tutor/js/rt-capture.js` - RTCapture（三条件解锁、gum 约束三元组、worklet/ScriptProcessor 双路径、stop 释放全部 track）、resampleTo16k/floatToInt16 纯函数
- `pkg/web/static/tutor/js/rt-pcm-worklet.js` - AudioWorklet 处理器（1024 样本帧拷贝发送）
- `pkg/web/static/tutor/js/rt-playback.js` - RTCPlayback（收即播调度、generation 令牌、epoch 门、队列上限、RMS 持续窗打断、sentence_ack、playedUntilMs）、applyTuning URL 标定
- `pkg/web/static/js/index-banner.js` - 旧学生页降级横幅（自包含、defer、零 app.js 依赖）
- `pkg/web/static/index.html` - 引入 index-banner.js（脚本清单注释同步）
- `tests/test-rt-timing.js` - TimingLog/estimateClockOffset/工具函数 node 单测
- `tests/test-rt-playback.js` - 降采样/钳位/RMS 窗/playedUntilMs/令牌/队列上限 node 单测
- `tests/e2e-rt/mock-rt-server.js` - node http+ws mock：静态服务 + /rt 协议实现 + 脚本化 10 轮 + 打断/结束处理 + /__test/state
- `tests/e2e-rt/rt-loop.spec.js` - 三用例：10 轮单授权、打断闭环、结束释放麦克风
- `playwright.rt.config.js` - 独立 testDir tests/e2e-rt + fake 媒体 chromium 参数 + NO_PROXY 回环守卫
- `package.json` - test 追加两个新 node 测试；新增 e2e:rt 脚本

## Decisions Made

- **worklet 不进 script 标签**：index.html 对 rt-pcm-worklet.js 的第 4 处引用是文档化注释（加载途径 audioWorklet.addModule）——普通 <script> 加载会在 registerProcessor 作用域抛 ReferenceError，触发 e2e 的 pageerror 断言
- **interrupt_stop 只在播放存活时记录**：会话结束对静默播放器的 stopNow 不制造无配对 interrupt_stop，RT-04 打断对证据保持干净
- **e2e 用 URL 标定通道禁用自动打断**：fake 音频设备恒定音调会让 10 轮环非确定性自打断；interruptNow() 是确定性打断路径，且测试同时验证了 13-07 将使用的标定入口
- **降采样照抄存量亚样本损失**：每次调用 floor() 损失 ≤1 样本与已上线 voice-command-controller 行为一致；320 样本 emit 块沿袭 640 字节 wire 节奏仅为避免碎片帧
- **Requirement 不标完成**：四个 ID 均被 13-06/13-07 共同声明（共享 ID 门 #2388 阻塞）；本计划交付组件证据，真机验收归 13-07

## Deviations from Plan

### Auto-fixed Issues

**1. [Rule 3 - Blocking] Playwright webServer 探活被系统代理劫持，套件在代理环境下必然超时**
- **Found during:** Task 3（verify 阶段首跑 e2e）
- **Issue:** 本机 HTTP_PROXY 指向本地 Privoxy 且无回环 NO_PROXY；Playwright 自身的 webServer 探活与 page.request 走代理，代理无法回连 127.0.0.1 答 500——`npx playwright test -c playwright.rt.config.js` 在 config.webServer 等待 20s 超时，mock 服务实际已正常启动（DEBUG=pw:webserver 确认探针全部收到 500）
- **Fix:** playwright.rt.config.js 在 config 加载期向 NO_PROXY/no_proxy 追加 `127.0.0.1,localhost`（保留既有值）；Chromium 页面/WS 流量本就隐式绕过 localhost，此修正覆盖 Playwright 侧请求。任何代理开发机上计划的验收命令原样可跑
- **Files modified:** playwright.rt.config.js
- **Verification:** 代理环境原样重跑 `npx playwright test -c playwright.rt.config.js` → 3 passed (15-16s)；`npx playwright test --list` 不受影响
- **Committed in:** 4a9f98f（Task 3 commit）

**2. [Rule 1 - 测试断言修正] test-rt-timing.js 脱敏断言误伤合法事件名**
- **Found during:** Task 1（verify 首跑）
- **Issue:** 断言 `!text.includes('audio')` 命中了合法事件名 first_audio_played 中的子串——脱敏检查的真实意图是"无音频/转写载荷"，而非禁止含该词的事件名
- **Fix:** 收窄为载荷键检查（/"pcm"|samples|transcript/），事件词表不受影响
- **Files modified:** tests/test-rt-timing.js
- **Verification:** node tests/test-rt-timing.js 全绿
- **Committed in:** 45300ea（Task 1 commit）

---

**Total deviations:** 2 auto-fixed（1 blocking 环境修正、1 测试断言修正）
**Impact on plan:** 均不改变架构与交付范围。NO_PROXY 守卫让验收命令在代理开发机（本机即是一台）可复现；断言修正让脱敏检查表达真实意图。

## Issues Encountered

- Playwright webServer 首跑超时的根因定位走了两步：先以 node --check + 手动起服务排除 mock 自身问题（服务日志正常、curl 却得 500），再以 DEBUG=pw:webserver 确认 500 来自 Privoxy 代理而非本服务——排障路径已固化为其修复（NO_PROXY 守卫）
- 中途一次 API 限流中断（Task 1 验收后、提交前）：恢复后核对 git log/status 确认无部分提交状态，按原子提交流程继续

## User Setup Required

None - no external service configuration required.

## Next Phase Readiness

- 13-06（适配器 + /rt 挂载 + TLS）：前端已按 protocol.go wire 契约冻结实现，真实服务挂载后 /tutor/ 无需改动即可连接；mock e2e 套件可直接回归协议一致性
- 13-07（真机验收）：TimingLog 导出（rt-timing-*.jsonl）与 clock_offset 事件已就绪；打断阈值经 URL 参数（interrupt_rms/interrupt_sustain_ms/queue_max）真机标定无需改代码；e2e 的 fake 媒体参数仅测试环境
- 遗留标定项：INTERRUPT_RMS_THRESHOLD=0.08 / INTERRUPT_SUSTAIN_MS=200 为初始值，扬声器回声不自我打断的阈值需 13-07 真机实测（计划内安排，非缺陷）

## Self-Check: PASSED

- [x] 12 个新建文件 + 2 个修改文件全部在磁盘并已提交（git status 干净）
- [x] git log 含 45300ea / 4c831c0 / 4a9f98f 三个任务提交（base c7c4741 之上，rev-list 计数 3）
- [x] 计划级验证全通过：npm test（exit 0，9 个 node 测试文件）；npx playwright test -c playwright.rt.config.js（3 passed）；npx playwright test --list（exit 0 且零 rt-loop，H-1 隔离成立）；go build ./...（零 Go 改动）
- [x] 工作树无未跟踪/未提交文件

---
*Phase: 13-realtime-minimal-loop*
*Completed: 2026-10-02*
