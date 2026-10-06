---
phase: 13-realtime-minimal-loop
plan: 12
subsystem: qa
tags: [pre-acceptance, local-https, ipad-uat, fsrs-word-source, ws-smoke, rt-timing, checkpoint-human, evidence-hygiene]

# Dependency graph
requires:
  - phase: 13-realtime-minimal-loop/13-10
    provides: FSRS 词源生产链路（PyFSRS 适配器 + main.go 注线 + rt.wordTimeoutMs 标定入口）——冒烟验证对象
  - phase: 13-realtime-minimal-loop/13-11
    provides: tutor 页默写课堂呈现 + mock e2e——真机会话的功能面
  - phase: 13-realtime-minimal-loop/13-08
    provides: 默写状态机 + 完结路径 + 三伴随缺陷（VAD 0s/ASR 空转写/invalid response）修复——零复发复核对象
  - phase: 13-realtime-minimal-loop/13-07
    provides: 本机 8443 预验收通道（mkcert 证书 + config.local.yaml）+ rt 协议扩展
provides:
  - "13-CHECKPOINTS 本机预验收段：全量预检记录（build/vet/go test 16 包/npm test/playwright rt 4 用例/存量隔离复检全绿）、8443 通道三探针（页面 200 + 健康 + WSS session_ready words=10）、真实 RunDue 取词证据（lessons 表 r0-20261006 行 10 词卡与 vocab.db WAL 写入时刻吻合）、三伴随缺陷自动化侧初判"
  - "Task 2 blocking-human 检查点请求：真 iPad 默写课十步操作、评级映射追认项（Pending，未定案）、三缺陷零复发观察点"
  - "rt-timing-report 报告管线自检通过（真实会话 JSONL 报告待用户会话后补）"
affects: [13-realtime-minimal-loop/13-13 (VPS 正式验收——前置门为本计划 blocking-human 收口), 13-CHECKPOINTS 首周检查行 (2026-10-07)]

# Actuals (#2632)
actuals:
  tokens: 5200        # chars/4 over realized diff (两段 CHECKPOINTS + SUMMARY)
  tasks: 1            # Task 1 完成；Task 2 停在 blocking-human 门（designed stop）；Task 3 仅自检半
  commits: 3          # MEASURED: git rev-list --count 525dc4c..HEAD（含本 SUMMARY 提交）
commits: 3
plan_head_before: 525dc4c
plan_head_after: b6a5541

# Tech tracking
tech-stack:
  added: []           # 零新增依赖（T-13-SC：探针仅用既有 node ws 依赖）
  patterns:
    - "Pattern: 重试继承——服务实例由前次会话启动且构建与树一致时，预检复验（go build + 探针）通过即复用，不盲目重启；跳过计划重启步须在检查记录写明理由"
    - "Pattern: Debug 级成功路径的替代证据链——pybridge 成功调用不入 info 日志时，用 DB 行时间戳 + WAL 写入时刻 + 会话事件链三点吻合作 RunDue 真实性证据"
    - "Pattern: localhost 探针必须 --noproxy（本机 Privoxy 会劫持并伪造失败响应）"

key-files:
  created:
    - .planning/phases/13-realtime-minimal-loop/13-12-SUMMARY.md
  modified:
    - .planning/phases/13-realtime-minimal-loop/13-CHECKPOINTS.md

key-decisions:
  - "未重启 8443 实例：重试继承的实例（PID 50472，01:46:05 启动）即 post-13-10 新构建（01:45:31 构建，HEAD 后无代码提交，go build 复验通过），符合 Orchestrator『预检不要求重建则不重启』的重试指令；计划 Task 1 第 2 步的重启语义在该条件下为空操作"
  - "RunDue 证据落档口径：服务日志无 pybridge 成功行属预期（成功路径仅 Debug 级），以 lessons 表 r0-20261006 行（10 词卡）+ vocab.db WAL 写入时刻 + session_ready words=10 作替代证据链"
  - "冒烟脚本以 session_ended 即收口（服务端保持 ws 开启属 idle 超时语义，非缺陷）；词表正文不入检查记录，仅记词数"
  - "评级映射在用户答复前保持 Pending 并入档十步操作与两个待确认次序（hint→confirm、hint→timeout），不预写任何『已确认』"

patterns-established:
  - "Pattern: 本机预验收记录强制标注『本机预验收，不记为正式验收』（T-13-25），正式证据仍归 13-13 VPS 段"

requirements-completed: []  # 计划声明的 RT-01/02/03/04/05/07/10 全部待真机会话与 13-13 收口（flagged assumptions：probe 边缘谓词 unresolved，本计划是本机半场）

# Coverage metadata (#1602)
coverage:
  - id: D1
    description: "全量预检 + 8443 通道三探针 + 真实 RunDue 取词冒烟（session_ready words=10，词源链路在转向接线下真实工作）落档 13-CHECKPOINTS 本机预验收段"
    requirement: RT-10
    verification:
      - kind: command
        ref: "go test ./pkg/... ./cmd/... -short -count=1 && npm test && npx playwright test -c playwright.rt.config.js（全绿）+ npx playwright test --list（7 用例无 rt-loop）"
        status: pass
      - kind: integration
        ref: "node .scratch/smoke-rt-8443-1312.mjs（wss session_init→session_ready lesson_id=r0-20261006 words=10→sentence_ack×3→end_session→session_ended client_end，exit 0）+ lessons 表 r0-20261006 行与 vocab.db WAL 时刻吻合"
        status: pass
    human_judgment: false
  - id: D2
    description: "真 iPad 本机默写课实测（开场/确认/超时/四类口令/暂停继续/打断/完结记录）+ 评级映射用户追认 + 三伴随缺陷零复发复核"
    verification: []
    human_judgment: true
    rationale: "麦克风/扬声器/儿童语音不可自动化；需用户持真 iPad 完成一节完整默写课并对评级映射追认（检查点请求已落档 13-CHECKPOINTS Task 2 段）"
  - id: D3
    description: "rt-timing-report 报告管线可用（--self-test 全断言通过，含 clock_offset/无 offset/失败样本分支）"
    verification:
      - kind: command
        ref: "node scripts/rt-timing-report.js --self-test（exit 0，全部断言通过）"
        status: pass
    human_judgment: false
  - id: D4
    description: "对本机会话导出的 rt-timing JSONL 跑报告并把首问候耗时/逐词推进时序摘要落档（标注：本机 LAN 样本仅验证报告管线，不作第六周判定证据）"
    verification: []
    human_judgment: true
    rationale: "依赖 Task 2 用户会话导出的真实 JSONL；会话未发生，报告样本不存在"

# Metrics
duration: 12min
completed: 2026-10-06
status: halted   # designed stop：Task 2 为 blocking-human 检查点（真 iPad 会话须用户亲自执行）；恢复后按 Task 2 完成信号续跑
---

# Phase 13 Plan 12: 本机预验收（转向后交互模型真机半场）Summary

**全量预检绿 + 8443 通道三探针绿 + 真实 RunDue 取词冒烟 words=10（lesson r0-20261006，词源链路实测工作）落档本机预验收段；Task 2 真 iPad 默写课为 blocking-human 检查点已请求（评级映射追认 Pending），计划按设计停在该门**

## Performance

- **Duration:** 12 min（2026-10-06T09:22+08:00 → 2026-10-06T09:34+08:00）
- **Started:** 2026-10-06T09:22:37+08:00
- **Completed:** 2026-10-06T09:34+08:00（designed stop 落档时刻）
- **Tasks:** 1 / 3 完成（Task 3 自检半完成；Task 2 blocking-human 待用户）
- **Files modified:** 2（13-CHECKPOINTS.md 两段追加 + 本 SUMMARY）

## Accomplishments

- **全量预检全绿**：go build / go vet / go test 16 包（-short -count=1）/ npm test 9 套件 / playwright rt 4 用例全过；存量隔离复检 `--list` 7 用例不含 rt-loop
- **8443 通道确认（未重启）**：复用重试继承实例（PID 50472，01:46:05 启动，01:45:31 post-13-10 构建，树一致）；启动日志 `"tls":true` + `rt endpoint mounted lesson_id=""`（每日派生语义生效）；三探针——`/tutor/` 200（18461B 含 data-page="tutor"）、`/health` status ok、WSS 握手 5–7ms。探针纪律：全部 `--noproxy "*"`（本机代理劫持 localhost 会伪造失败）
- **真实 RunDue 取词冒烟**：session_init → session_ready（lesson_id=r0-20261006 每日派生、version=1、words=10、tts 24000Hz、audio 16000/s16le/1）；首问候链完整（word_announced 为首实质事件）；3×audio_end 全 ack → listening → end_session → session_ended(client_end)；两会话零 error 事件。词源真实性证据：lessons 表 r0-20261006 行（10 词卡）+ vocab.db WAL 写入时刻 + 会话时刻三点吻合（pybridge 成功路径仅 Debug 级，info 日志无行属预期）
- **三伴随缺陷自动化侧初判为零**：无 0s VAD 空转轮、无 ASR 空转写事件、无 invalid API response 报错（最终结论待 Task 2 真机会话复核）
- **Task 2 blocking-human 检查点请求落档**：十步用户操作、评级映射追认项（Pending + 两个待确认次序）、三缺陷零复发观察点、完成信号（"approved" + JSONL 文件名）
- **Task 3 自检半**：`node scripts/rt-timing-report.js --self-test` exit 0 全断言通过；真实会话报告待用户 JSONL

## Task Commits

Each task was committed atomically:

1. **Task 1: 全量预检 + 通道确认 + 取词冒烟证据落档** - `622aba3` (docs)
2. **Task 2: blocking-human 检查点请求落档** - `b6a5541` (docs)

**Plan metadata:** 本 SUMMARY 提交（docs(13-12)）

## Files Created/Modified

- `.planning/phases/13-realtime-minimal-loop/13-CHECKPOINTS.md` - 新增「本机预验收（2026-10-06，13-12）」段：Task 1 证据（预检表/通道确认/三探针/RunDue 证据链/缺陷初判）+ Task 2 检查点请求（用户步骤/评级追认 Pending/观察点）
- `.planning/phases/13-realtime-minimal-loop/13-12-SUMMARY.md` - 本文件（status: halted，designed stop）

（探针与校验脚本留在 `.scratch/`（smoke-rt-8443-1312.mjs / check-lesson-row-1312.py），不入库——T-13-30 纪律，原始证据本地留存。）

## Decisions Made

见 frontmatter key-decisions（复用实例不重启、RunDue 替代证据链、session_ended 即收口、评级映射保持 Pending）。

## Deviations from Plan

None - plan executed exactly as written.（计划 Task 1 第 2 步「停旧实例重启」的重启语义在重试继承条件下为空操作：旧 PID 已死、当前实例即新构建且与树一致——按 Orchestrator 重试指令跳过，理由已落档 CHECKPOINTS。）

## Issues Encountered

- 本机 Privoxy 代理劫持 localhost：所有探针必须 `--noproxy "*"`，已在 CHECKPOINTS 探针纪律行固化
- pybridge 成功路径仅 Debug 级日志（bridge.go:346），info 级服务日志无 RunDue 成功行——以 DB/WAL/事件链替代证据（已落档）
- 冒烟脚本首跑因等待服务端 close 触发自身 watchdog（服务端 session_ended 后保持 ws 开启属 idle 超时语义）；改为 session_ended 即收口后 exit 0，属探针脚本修正非服务缺陷

## User Setup Required

Task 2 需用户执行（完整十步见 13-CHECKPOINTS「Task 2：真 iPad 本机默写课实测」段）：

- iPad 与本机同 WLAN，Safari 打开本机 8443 通道入口（mkcert 根证书已装并信任为前提；根证书分发页在本机 8765 端口，进程在位已核验）
- 完成一节完整默写课（开场/确认/超时/四类口令/暂停继续/打断/完结/回声观察），导出 rt-timing JSONL
- 追认评级映射（confirm→good、timeout→hard、hint→again 仅无终态时；特别确认 hint→confirm 与 hint→timeout 次序）
- 回复 "approved"（附 JSONL 文件名与异常现象）或指出未过项

## Pending Items（恢复续跑清单）

1. **Task 2 收口**：用户会话期间执行者同步看日志（`Get-Content server-8443.err.log -Tail`，禁 tail -f）；结束后核对 word_announced/word_advanced/lesson_complete/review_written 事件链与 lesson_version、三缺陷零复发、summary/no_due 的「实际听完→sentence_ack→session_ended」顺序、review requested/updated/missing 与词库实际变化核对、评级追认落档（或改 pkg/rt/dictation.go 常量表重测）
2. **Task 3 收口**：对导出 JSONL 跑 `node scripts/rt-timing-report.js`，首问候耗时/逐词推进摘要落档（标注本机 LAN 样本仅验证报告管线）；汇总三任务结论入检查记录模板口径；脱敏复查（grep 暂存 diff 无密钥/私钥/192.168 与 10. 局段/转写正文）后提交
3. **收尾核验**：13-CHECKPOINTS 首周检查行（2026-10-07）按结果更新由 Orchestrator 决定；本机结果不顶替 13-13 VPS 正式验收口径（T-13-25）
4. 环境备忘：8443 实例（PID 50472）与 8765 根证书分发（PID 45992）在位运行中；如需重启用 `.scratch/run-local-8443.ps1`（含输出重定向的 detached 模式），禁前台/裸 `&` 启动

## Next Phase Readiness

- 13-13（VPS 正式验收）前置门：本计划 blocking-human 收口后才推进；FSRS 词源前提、时序报告管线、评级口径追认三者在本机半场就绪度分别为：已冒烟实证 / 自检通过 / 待用户追认
- 首周检查行（2026-10-07）证据仍归 VPS 正式通道；本机段只承担「先暴露先修」防火墙职能

---
*Phase: 13-realtime-minimal-loop*
*Completed: 2026-10-06（designed stop at blocking-human gate）*
