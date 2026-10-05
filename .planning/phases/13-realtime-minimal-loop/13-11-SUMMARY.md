---
phase: 13-realtime-minimal-loop
plan: 11
subsystem: ui
tags: [vanilla-js, websocket-protocol, webaudio, playwright-e2e, mock-server, terminal-draining, sentence-ack, xss-discipline]

# Dependency graph
requires:
  - phase: 13-realtime-minimal-loop/13-07
    provides: pkg/rt 词表扩展（StatePaused/ErrCodeLessonUnavailable/ErrCodePlaybackAckTimeout/ReasonLessonComplete/ReasonNoDueWords）与 SentenceAckMsg 实际播放完成语义、超时降级顺序契约
  - phase: 13-realtime-minimal-loop/13-08
    provides: 服务端默写 FSM 行为（开场报词→确认/超时推进→完结；intent 词表；session_ended 自然结束顺序）——前端与 mock 必须镜像
  - phase: 13-realtime-minimal-loop/13-05
    provides: tutor 页骨架（状态条 chip、RTCClient/RTCapture/RTCPlayback 三件套）、mock e2e 目录隔离（playwright.rt.config.js + H-1 门）
provides:
  - "tutor 前端：paused chip 五态分派、lesson_complete 完结横幅（字幕保留总结）、no_due_words 提示、lesson_unavailable/playback_ack_timeout 人话映射、动态词数进度（词数不假设，current_index==len → 100%+已完成）、terminalDraining 排空路径（停上行、禁交互、不 stopNow、最后 onended 后才释放共享播放资源）"
  - "pkg/web/static/tutor/js/rt-playback.js：按 (epoch,sentence_id) 独立账本（audio_end + 该句全部 source.onended 同时满足才发一次 sentence_ack）、drainNaturally()（不停止任何 source，watchdog 按剩余排程+3s 夹 3..45s）、releaseSharedResources()（不经过 stopNow 的释放通道）、有序 trace 观测"
  - "tests/e2e-rt/mock-rt-server.js：默写流参考实现（mock_step_ms/mock_pause/mock_empty/mock_ack_timeout 参数、完结与 no-due 的 ACK 门、未知/旧 epoch/重复 ACK 拒绝观测、timeout 降级顺序、/__test/state 终态顺序观测）"
  - "tests/e2e-rt/rt-loop.spec.js：默写课堂四用例（完整课堂单授权/报词中打断/暂停+无到期词/确认超时排空）+ 存量套件隔离复检维持"
affects: [13-12 (本机真服务预验收——同一顺序的真实后端复核), 13-13 (真机呈现)]

# Actuals (#2632)
actuals:
  tokens: 22177       # chars/4 over the realized diff (88707 chars across 8 files, +1194/-189)
  tasks: 3
  commits: 5          # MEASURED: git rev-list --count db9a9d3..344ab9a
commits: 5
plan_head_before: db9a9d3
plan_head_after: 344ab9a

# Tech tracking
tech-stack:
  added: []           # 零新增依赖（T-13-SC：包安装面零变化）
  patterns:
    - "Pattern: 按句独立账本——播放完成确认以 (epoch,sentence_id) 为键，audio_end 与全部 source.onended 同时满足才一次性 ACK；相邻句交叠/重复 audio_end/旧 epoch onended 互不误伤；迟到完成的前句不关闭当前句的 chunk 门"
    - "Pattern: 有界自然排空——drainNaturally 不停止任何 source，真实最后 onended 才 resolve；watchdog 按 AudioContext 剩余排程时长+3s 推导夹在 3..45s，仅 watchdog 路径 stopNow 且清账本先于一切 ACK（不伪造完成证据）；显式取消 resolve('stopped')"
    - "Pattern: 因果顺序单数组观测——客户端 trace 有序记录 onended/ack/stopNow/drain/release + 页面 note（error/session_ended/close），e2e 用数组下标断言协议顺序"
    - "Pattern: mock 只证明前端协议行为——完结/no-due 严格等匹配 ACK、拒绝只记观测、timeout 依次 ErrorMsg→原 reason→正常 close；13-12 以真实后端复核同一顺序"

key-files:
  created: []
  modified:
    - pkg/web/static/tutor/index.html
    - pkg/web/static/tutor/js/rt-client.js
    - pkg/web/static/tutor/js/rt-playback.js
    - pkg/web/static/tutor/styles/tutor.css
    - tests/e2e-rt/mock-rt-server.js
    - tests/e2e-rt/rt-loop.spec.js
    - tests/test-rt-timing.js
    - tests/test-rt-playback.js

key-decisions:
  - "页面查询参数透传到 /rt 升级请求（client.connect(deriveRtUrl(location)+location.search)）：Go mux 按路径匹配忽略 query，真实后端零影响；mock 据此选择会话脚本（mock_pause/mock_empty/mock_ack_timeout/mock_step_ms），避免 Referer/共享全局等脆弱通道"
  - "terminalDraining 是页面级状态而非播放器内部态：进入时立即 capture.stop()（儿童语音隐私边界：停上行/MediaStreamTrack）并禁用继续交互，但 playback 不 stopNow、共享 AudioContext 不关——session_ended 与正常 close 都不得撤销已排程音频；timeout 不作为 reason 分支"
  - "releaseSharedResources 与 stopNow 分离：自然排空后的资源释放不经过 stopNow，stopNowCalls 计数在自然/超时排空路径保持 0——stopNow 计数因此成为「截断」的唯一证据面（T-13-24/24b 的 e2e 断言基元）"
  - "客户端 trace 单数组承载因果顺序（同一 JS 线程 push 序=真实顺序），页面事件经 playback.note 并入同一数组；e2e 断言 onended→ack→session_ended→close、error→session_ended→close 全部基于下标比较"
  - "playback_ack_timeout 的客户端诚实语义：timeout 错误后已排程音频播完时账本仍会正常 ACK（对已关闭 socket 的 send 静默失败，属真实完成记录而非伪造）——e2e 断言收窄为「降级序列运行期间无 summary ACK」（ackIdx===-1 || ackIdx>closeIdx），不要求抑制真实完成记录"
  - "mock 终态 close 用独立 closeSoon 通道（绕过 ended 守卫）：结束后的 classroom 静默守卫不得吞掉告别帧；/__test/state 的 terminalOrder 由此完整记录 ack→ended→close"

patterns-established:
  - "Pattern: 注入负门即验证命令——rg 逐字匹配四个前端 JS + index.html，注释/文档同样不得命中字面量（本计划曾因注释提及该词被门拦下，措辞修正）"
  - "Pattern: e2e 终态断言轮询到证据齐全——服务端延迟记录（close ~50ms）与客户端状态渲染都可能滞后，直读存在竞态；poll 到 terminalOrder/close 出现再断言"

requirements-completed: [RT-01, RT-02, RT-10]  # 共享 ID：RT-01/RT-02/RT-10 亦被 13-12/13-13 声明，mark-complete 由 orchestrator 按 shared-ID 门槛收口

# Coverage metadata (#1602)
coverage:
  - id: D1
    description: "tutor 页入口状态可见（RT-01 客户端半边）：paused chip 五态分派、lesson_complete 完结横幅+字幕保留总结、no_due_words「今天没有到期单词」提示、动态词数进度（初始占位「第 - / - 个词」、current_index==len → 100%+已完成）"
    requirement: RT-01
    verification:
      - kind: e2e
        ref: "tests/e2e-rt/rt-loop.spec.js#complete classroom runs on a single getUserMedia authorization（已完成态 + 完结横幅 + 字幕保留总结 + mock lessonUpdates=3/currentIndex=3）"
        status: pass
      - kind: e2e
        ref: "tests/e2e-rt/rt-loop.spec.js#pause stays paused after the confirmation speech; no-due path ends honestly（paused chip、确认话术后保持 paused、无到期词提示、无完结横幅）"
        status: pass
    human_judgment: false
  - id: D2
    description: "单次授权持续听说（RT-02 客户端半边）：__gumCallCount===1 贯穿完整默写课堂；报词中 interruptNow 打断——interrupt 三字段、epoch 递增、旧句无 ACK 且 stopNow 后不复活、课堂推进至完结不跳词"
    requirement: RT-02
    verification:
      - kind: e2e
        ref: "tests/e2e-rt/rt-loop.spec.js#complete classroom runs on a single getUserMedia authorization（gumCallCount===1 断言）"
        status: pass
      - kind: e2e
        ref: "tests/e2e-rt/rt-loop.spec.js#interrupt during the second announcement bumps the epoch and the lesson still completes（mock interrupts/epoch/acks + 客户端 trace 无旧键 ACK/onended 复活）"
        status: pass
    human_judgment: false
  - id: D3
    description: "播放完成协调客户端半边（T-13-24/24b）：按 (epoch,sentence_id) 账本一次性 ACK（audio_end+全部 onended 同时满足）；drainNaturally 正常排空/watchdog 3..45s 双夹取/timed_out 不伪造 ACK/mid-drain stopped；e2e 顺序断言 onended→ack→session_ended(lesson_complete)→close 与 timeout 排空（排队时不释放不 stopNow、最后 onended 后才释放）"
    requirement: RT-02
    verification:
      - kind: unit
        ref: "tests/test-rt-playback.js（多句交叠独立 ACK、旧 epoch 隔离、重复事件、正常排空、watchdog 下限 3000ms/上限 45000ms 假时钟、timed_out 零 ACK、stopped）"
        status: pass
      - kind: e2e
        ref: "tests/e2e-rt/rt-loop.spec.js#complete classroom…（trace 顺序 + stopNowCalls===0 + drain:drained→release）"
        status: pass
      - kind: e2e
        ref: "tests/e2e-rt/rt-loop.spec.js#playback-ack timeout drains with sources queued…（obs 排队/未释放/stopNow 不变 + 顺序断言 + summaryAcked=false）"
        status: pass
    human_judgment: false
  - id: D4
    description: "故障人话呈现（RT-10 客户端半边）：humanizeError 的 lesson_unavailable（可重试引导）与 playback_ack_timeout（不声称已听完）映射、未知码回退不回归；注入纪律负门（rg 逐字匹配四 JS + index.html 零命中）"
    requirement: RT-10
    verification:
      - kind: unit
        ref: "tests/test-rt-timing.js（两新码映射 + timeout 文案无「已听完/播放完成」 + 未知码回退）"
        status: pass
      - kind: unit
        ref: "command: rg -q innerHTML 于四个前端 JS + index.html（退出码 1 = clean）"
        status: pass
    human_judgment: false
  - id: D5
    description: "mock 默写流参考实现与四用例回归网：完结总结/no-due 提示严格等匹配 sentence_ack 才 session_ended；未知/旧 epoch/重复 ACK 只记拒绝观测；mock_ack_timeout 依次 ErrorMsg(playback_ack_timeout)→原 lesson_complete reason→正常 close（timeout 永不入 reason）；存量套件零拾取（--list 不含 rt-loop，H-1 门）"
    requirement: RT-02
    verification:
      - kind: e2e
        ref: "command: npx playwright test -c playwright.rt.config.js（4 passed，8+ 次复跑稳定）"
        status: pass
      - kind: e2e
        ref: "command: npx playwright test --list（7 tests in 4 files，零 rt-loop 拾取）"
        status: pass
    human_judgment: false
  - id: D6
    description: "真机呈现与听感：paused chip 在真 iPad Safari 的可读性、完结横幅/无到期词提示的实际观感、暂停期间语音 resume 的真实链路——mock 只证明前端协议行为，真实后端端到端复核归 13-12，真机呈现归 13-13"
    verification: []
    human_judgment: true
    rationale: "mock e2e 用测试通道 __test_resume 模拟暂停恢复、假音频设备下无真实 TTS 听感；RT-01 入口状态的真实 HTTPS/真机呈现与 13-08 coverage D7 的话术语气温感需真人评判"

# Metrics
duration: 187min
completed: 2026-10-05
status: complete
---

# Phase 13 Plan 11: tutor 页默写课堂适配 + mock e2e 四用例重写 Summary

**前端从「自由对话十轮」适配为「状态机默写课堂」：paused chip/完结横幅/无到期词提示/两新错误码人话映射/动态词数进度与 terminalDraining 排空语义（stopNow 计数成为截断唯一证据面），rt-playback 改按 (epoch,sentence_id) 独立账本一次性 ACK 并新增 3..45s 有界 watchdog 的 drainNaturally，mock 重写为默写流参考实现（ACK 门/拒绝观测/超时降级顺序），四用例全绿且存量套件隔离复检维持 H-1 门**

## Performance

- **Duration:** 187 min（2026-10-05T10:15Z+8 前后 → 2026-10-05T23:22Z+8；含 e2e 竞态定位与 8+ 次稳定性复跑）
- **Tasks:** 3 / 3
- **Files modified:** 8（全部为计划声明的文件，零新建、零删除、零越界）

## Accomplishments

- **tutor 页新协议值全呈现（RT-01/RT-10 客户端半边）**：paused chip（⏸ 休息中，data-state 五态分派）；session_ended 只按 lesson_complete（完结横幅 + 字幕保留总结话术）/no_due_words（「今天没有到期单词」）分支呈现，timeout 永不作为 reason 分支；humanizeError 新增 lesson_unavailable（附「开始上课」重试入口）与 playback_ack_timeout 人话映射——timeout 文案只承诺「先把已收到的语音播完」，绝不声称已经听完
- **terminalDraining 排空语义（T-13-24/24b）**：timeout ErrorMsg、自然 session_ended、正常 WebSocket close 共享同一条排空路径——立即停上行采集/MediaStreamTrack、禁用继续交互，但不 stopNow、不关共享 AudioContext；已排程音频等真实最后 onended（或播放侧 3..45s watchdog）后才释放共享播放资源；显式用户结束/打断/非 timeout 错误/异常断线仍立即 stopNow。stopNowCalls 只读观测使「排空路径零 stopNow」成为 e2e 可断言事实
- **按句独立播放账本**：rt-playback.js 全局句子计数改为 (epoch,sentence_id) 账本——audio_end 与该句全部 source.onended 同时满足才发一次 sentence_ack；相邻句交叠、重复 audio_end、旧 epoch 迟到 onended 均不误确认新句；迟到完成的前句不关闭当前句的 chunk 门；drainNaturally() 不停止任何 source，watchdog 按剩余排程+3s 夹 3..45s 且 timed_out 路径清账本先于一切 ACK（不伪造完成证据）
- **mock 默写流参考实现**：3 词全自动课堂（mock_step_ms 默认 1500）、完结总结与 no-due 提示严格等匹配 ACK、未知/旧 epoch/重复 ACK 只记拒绝观测（T-13-24：伪造/迟到 ACK 不成完成证据）、mock_pause 保持 paused 至测试 resume、mock_ack_timeout 依次 ErrorMsg→原 reason→正常 close 且总结音频保留 6s 排程使 close 到达时仍有排队源；/__test/state 暴露 lessonUpdates/currentIndex/terminalOrder/acks/acksRejected
- **e2e 四用例 + 双重稳定性**：完整课堂单授权（gumCallCount===1、onended→ack→session_ended→close 顺序、stopNowCalls=0）、报词中打断（pendingSources>0 原子门消除 ACK 竞态）、暂停+无到期词、确认超时排空；终态断言轮询到 close 记录齐全（修掉 50ms 终态记录竞态）；5 次单测 + 3 次全套件复跑 8/8 稳定；存量套件 --list 零拾取维持 H-1 门

## Task Commits

Each task was committed atomically:

1. **Task 1: tutor 页 — paused/完结/无到期词/错误映射/动态进度** - `303a3e9` (feat)
2. **Task 2: mock 服务重写 — 默写流脚本（报词/推进/暂停/完结）** - `a2912b9` (feat)
3. **Task 3: 播放排空控制 + e2e 四用例重写 + 隔离复检** - `d51b1ad` (feat)
   - 稳定化跟进 `d11bccd` (test)、负门字面量修正 `344ab9a` (fix)

**Plan metadata:** 本 SUMMARY 提交（docs(13-11)）

## Files Created/Modified

- `pkg/web/static/tutor/index.html` - paused chip、完结横幅区、动态进度占位、terminalDraining 页面状态机（错误/结束/断线三条接线）、clearChildren 注入纪律、observe() 只读观测、页面查询参数透传 WS 升级请求
- `pkg/web/static/tutor/js/rt-client.js` - ERROR_MESSAGES 新增 lesson_unavailable/playback_ack_timeout 两键（人话文案，timeout 不宣称完成）
- `pkg/web/static/tutor/js/rt-playback.js` - (epoch,sentence_id) 账本、_maybeAckEntry 一次性 ACK、drainNaturally + 可注入定时器 watchdog（3..45s）、releaseSharedResources、stopNowCalls/trace 有序观测、note()
- `pkg/web/static/tutor/styles/tutor.css` - paused chip 琥珀色、完结横幅、lesson_unavailable 重试引导样式（触控 ≥60px 不变）
- `tests/e2e-rt/mock-rt-server.js` - 默写课堂脚本重写（四参数选择器、ACK 门、拒绝观测、超时降级顺序、closeSoon 独立通道、/__test/state 扩展）
- `tests/e2e-rt/rt-loop.spec.js` - 四用例重写 + getFinalSession 轮询助手 + trace/observe 断言
- `tests/test-rt-playback.js` - 交叠/旧 epoch/重复事件账本用例 + drainNaturally/watchdog 假时钟用例（FakeTimers）
- `tests/test-rt-timing.js` - 两新错误码映射用例（timeout 不宣称听完 + 回退不回归）

## Must-Have Truths Verification

| Truth | Evidence | Status |
|-------|----------|--------|
| paused chip 五态分派；进度由 session_ready/lesson_update 驱动、词数动态、current_index==len 完成态 | e2e 用例一（已完成 + lessonUpdates=3）+ 用例三（paused chip）；index.html/CSS | PASS |
| lesson_complete/no_due_words 两 reason 分支呈现；lesson_unavailable/playback_ack_timeout 人话映射；timeout 非 reason 非完成证据 | e2e 用例一/三/四 + test-rt-timing 两码用例；mock terminalOrder 断言 | PASS |
| 四用例默写流全绿 + 存量套件零拾取 | npx playwright test -c playwright.rt.config.js → 4 passed（8+ 次复跑）；--list 7 tests in 4 files 零 rt-loop | PASS |
| 每 (epoch,sentence_id) 的 sentence_ack 只在 audio_end+全部 onended 后发一次；timeout→自然 ended→正常 close 进入 terminalDraining 不 stopNow；watchdog 有界且不伪造 ACK | test-rt-playback（交叠/旧 epoch/重复/watchdog 双夹取/零伪造 ACK）+ e2e 用例一/四（顺序断言 + stopNowCalls=0 + 最后 onended 后才释放） | PASS |
| 暂停确认话术播完后保持 paused，只有真实 StateMsg(listening) 才恢复 | e2e 用例三：ack:1:p1 后仍 paused，__test_resume 后才 listening | PASS |
| 前端文本注入纪律：全链 createElement/textContent，无 innerHTML | rg 逐字负门四 JS + index.html 零命中（含注释） | PASS |
| 一次 getUserMedia 授权贯穿完整默写课堂（backstop） | e2e 用例一 __gumCallCount===1 | PASS |

## Decisions Made

见 frontmatter key-decisions（query 透传选脚本、terminalDraining 页面级状态、release/stopNow 分离、trace 因果观测、timeout 后真实完成 ACK 的诚实口径、mock closeSoon 独立通道）。

## Deviations from Plan

### Auto-fixed Issues

**1. [Rule 3 - Blocking] index.html 内联脚本误用 IIFE 别名 `global`（浏览器 ReferenceError，connect 不执行）**
- **Found during:** Task 3（首次 playwright rt 全量，session_ready 20s 超时；浏览器 console 捕获 `ReferenceError: global is not defined`）
- **Issue:** `client.connect(deriveRtUrl(global.location)+…)` 中的 `global` 是各 JS 模块 IIFE 的内部别名，页面内联作用域不存在该标识符；点击开始后处理函数抛错，connect 从未执行
- **Fix:** 改为 `window.location`（页面级标准全局）；本地无头复现确认 session_ready/开场报词/lesson_update 全链恢复
- **Files modified:** pkg/web/static/tutor/index.html
- **Verification:** e2e 四用例全绿
- **Committed in:** d51b1ad（Task 3 提交内含）

**2. [Rule 3 - Blocking] mock 终态 close 定时器被 ended 守卫吞掉（terminalOrder 缺 close、socket 悬挂）**
- **Found during:** Task 2（node --check 后的静态自审：`later()` 的 `if (!state.ended)` 守卫会拦下 fireNaturalEnd/超时路径在置 ended 之后武装的 close 定时器）
- **Issue:** session_ended 后 50ms 的 close 回调永不执行——close 记录缺失、连接靠对端超时回收
- **Fix:** 新增 `closeSoon()` 独立通道（原始 setTimeout，绕过 classroom 静默守卫），三条终态路径统一使用
- **Files modified:** tests/e2e-rt/mock-rt-server.js
- **Verification:** e2e terminalOrder 三元素断言全绿
- **Committed in:** a2912b9（Task 2 提交内含）

**3. [Rule 3 - Blocking] e2e 两处竞态：终态直读与打断窗口判定（复现率约 1/8）**
- **Found during:** Task 3（计划级复跑出现一次失败；定位为 mock 的 close 记录滞后 session_ended ~50ms 被直读抢跑，以及 DOM speaking 显示滞后音频时钟导致 ACK 竞态）
- **Issue:** terminalOrder 直读缺 close 元素；interruptNow 可能在该句合法播完并 ACK 之后落地，「旧句无 ACK」断言偶发假红
- **Fix:** getFinalSession 轮询 terminalOrder 齐全再断言；打断原子等待加入 `pendingSources>0`（客户端仍有排队源时该句必然尚未 ACK，打断必然真实取消在飞音频）
- **Files modified:** tests/e2e-rt/rt-loop.spec.js
- **Verification:** 5 次单用例 + 3 次全套件复跑 8/8 稳定
- **Committed in:** d11bccd

**4. [Rule 3 - Minor] 页面注释提及负门字面量本身，逐字 grep 门误命中**
- **Found during:** 计划级验证（innerHTML 负门 FAIL——唯一命中是 clearChildren 的注释文本）
- **Issue:** 负门按计划为逐字 `rg -q innerHTML`，注释提及该词同样命中
- **Fix:** 注释措辞改为「negative grep gate」；门命令保持计划原文未动
- **Files modified:** pkg/web/static/tutor/index.html
- **Verification:** 负门 clean；内联脚本语法复查通过
- **Committed in:** 344ab9a

---

**Total deviations:** 4 auto-fixed（3 blocking，1 minor；全部为计划意图的机械落地或其直接必然后果）
**Impact on plan:** 零范围蔓延——修正均发生在计划声明文件内，验证命令全部按计划原样命中（npm test / playwright rt / --list / 负门逐字执行），无过滤漂移。

## Issues Encountered

- 本计划由 orchestrator 指定为顺序执行器，在主工作树（main 分支）直接提交（config `branching_strategy: none`，与 13-01～13-08 一致）；每任务仅暂存计划声明文件（+ Rule 3 修正文件），工作区内无关改动（.gitignore、STATE.md、CONTEXT.md、pkg/* EOL 噪声、根目录未跟踪 scratch 文件）一律未触碰
- e2e 初版两处断言在真实浏览器时序下过严（打断句可能存在打断前已合法结束的 chunk onended；句尾 ACK 与打断存在理论竞态）——按计划语义（「不复活」「旧 epoch 无 ACK」）收窄为 stopNow 标记后无复活 + pendingSources>0 确定性窗口，语义与计划一致且可复现稳定
- Playwright webServer 复用端口 8099：调试期间的后台 mock 进程残留会毒化断言（旧脚本仍在服务），定位时用 `Get-NetTCPConnection` 清点并全部回收后复跑

## User Setup Required

None - no external service configuration required.

## Threat Mitigations Delivered

| Threat | Disposition | Where |
|--------|-------------|-------|
| T-13-22 (Tampering, 服务端文本经 HTML sink 注入, medium) | mitigate | 全链 createElement/textContent + clearChildren；rg 逐字负门（四 JS + index.html，含注释）进计划级验证；字幕/转写/错误/话术三通道全覆盖 |
| T-13-23 (DoS, mock 与真实服务语义漂移假绿, low) | mitigate | mock 逐字段镜像 protocol.go（LessonView/StateMsg/ErrorMsg/SessionEndedMsg/InterruptAckMsg）；ACK 门与超时降级顺序按 13-07/13-08 契约；13-12 本机真服务预验收为最终对账点 |
| T-13-24 (Spoofing, 错误 epoch/重复 ACK/timeout 被误记播放完成, high) | mitigate | 客户端按句一次性 ACK（audio_end+全部 onended 双门）；mock 拒绝未知/旧 epoch/重复只记观测；timeout 只进 terminalDraining、不产生完成证据（e2e 断言降级序列期间无 summary ACK）；watchdog 路径清账本先于 ACK |
| T-13-24b (DoS, 正常结束等待 onended 永不释放, medium) | mitigate | drainNaturally 按剩余排程+3s 推导夹 3..45s watchdog；正常路径最后 onended 后 releaseSharedResources（不经过 stopNow）；watchdog 路径有界 stopNow；node 假时钟双夹取用例 + e2e 超时排空用例分别覆盖 |
| T-13-SC (Tampering, 包安装, high) | mitigate | 零新增依赖，包安装面零变化 |

## Next Phase Readiness

- **13-12（本机真服务预验收）**：前端与 mock 已对新协议词表（paused/lesson_unavailable/playback_ack_timeout/lesson_complete/no_due_words）形成完整客户端回归网；必须以真实后端复核同一顺序（ACK→session_ended→close、超时降级序、语音 resume——mock 的 __test_resume 只是占位）；评分映射追认与话术听感 UAT 仍待检查点收口
- **13-13（真机呈现）**：iPad Safari 真机上的 paused/完结/无到期词呈现与话术听感待真人评判（coverage D6 human_judgment）；?interrupt_rms/?interrupt_sustain_ms 校准机制延续
- RT-01/RT-02/RT-10 为共享 ID（13-12/13-13 亦声明），REQUIREMENTS 勾选由 orchestrator 按 shared-ID 门槛收口

## Self-Check: PASSED

- [x] 8 个修改文件全部存在于磁盘且全部在计划 files_modified 清单内（Test-Path/清单比对）
- [x] 提交 303a3e9 / a2912b9 / d51b1ad / d11bccd / 344ab9a 均在 git log（git log 303a3e9^..HEAD 验证）
- [x] commits: 5 为实测值（git rev-list --count db9a9d3..344ab9a），与 narrated 一致
- [x] 计划级验证命令全部通过且按原文执行：npm test 全绿（9 套件）；npx playwright test -c playwright.rt.config.js → 4 passed；npx playwright test --list → 7 tests in 4 files 零 rt-loop；innerHTML 负门 clean；node --check mock 通过
- [x] e2e 稳定性：修复后 5 次单用例 + 3 次全套件复跑 8/8 通过
- [x] 无 stub 模式（无 TODO/placeholder/空数据源）；无计划外威胁面新增（零新增依赖、零新端点、零新协议类型——__test_resume 为 mock 测试通道并在代码注释与本表登记）

---
*Phase: 13-realtime-minimal-loop*
*Completed: 2026-10-05*
