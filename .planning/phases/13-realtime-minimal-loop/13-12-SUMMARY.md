---
phase: 13-realtime-minimal-loop
plan: 12
subsystem: qa
tags: [pre-acceptance, local-https, local-e2e, ws-smoke, playwright-real-channel, rt-timing, evidence-hygiene, user-deferred-uat]

# Dependency graph
requires:
  - phase: 13-realtime-minimal-loop/13-10
    provides: FSRS 词源生产链路（PyFSRS 适配器 + main.go 注线 + rt.wordTimeoutMs 标定入口）——冒烟/E2E 词源前提
  - phase: 13-realtime-minimal-loop/13-11
    provides: tutor 页默写课堂呈现 + mock e2e——E2E 的功能面与 UI 断言基础
  - phase: 13-realtime-minimal-loop/13-08
    provides: 默写状态机 + 完结路径 + 三伴随缺陷（VAD 0s/ASR 空转写/invalid response）修复——零复发复核对象
  - phase: 13-realtime-minimal-loop/13-07
    provides: 本机 8443 预验收通道（mkcert 证书 + config.local.yaml）+ rt 协议扩展
provides:
  - "13-CHECKPOINTS 本机预验收段：全量预检记录、8443 通道三探针（words=10）、真实 RunDue 取词证据（Task 1）"
  - "13-CHECKPOINTS 本机 E2E 段（Task 2 改判代理执行）：Track 1 node WSS 完整默写课（rt-7，17/18 断言，三伴随缺陷零复发，review requested/updated/missing=3/3/0 落库）+ Track 2 Playwright 真实通道 UI/错误映射验证（2 用例+17 断言）+ completed-resume 路径设计行为记录"
  - "rt-timing-report 报告管线验证：--self-test 全断言 + rt-7 样本报告（配对数=0 根因与补采路径已记录）"
  - "待用户后续执行项清单（本机能力认证 / iPad 真机认证 / 评级映射追认 Pending / 配对样本补采）"
affects: [13-realtime-minimal-loop/13-13 (VPS 正式验收——本机防火墙职能完成，先暴露先修目标达成), 13-CHECKPOINTS 首周检查行 (2026-10-07)]

# Actuals (#2632)
actuals:
  tokens: 11000       # chars/4 over realized diff（Task 1 两段 + 本轮 E2E 段 + SUMMARY 两次 + STATE）
  tasks: 3            # Task 1 预检冒烟；Task 2 按用户 2026-10-06 改判为本机 E2E（代理执行）；Task 3 报告+落档
  commits: 5          # git rev-list --count 525dc4c..HEAD（622aba3/b6a5541/66c1c7d + 本轮 2 提交）
commits: 5
plan_head_before: 525dc4c
plan_head_after: 66c1c7d   # 收口前；本轮证据/完结提交在其后（见 Task Commits）

# Tech tracking
tech-stack:
  added: []           # 零新增依赖（E2E 用既有 node ws / playwright / ffmpeg 本机件；T-13-SC）
  patterns:
    - "Pattern: 重试继承——服务实例由前次会话启动且构建与树一致时，预检复验通过即复用，不盲目重启"
    - "Pattern: localhost 探针必须 --noproxy（本机 Privoxy 会劫持并伪造失败响应）"
    - "Pattern: node WSS 驱动必须常开麦克风泵（含纯静音帧，20ms 分帧）模拟浏览器实时上行——句间停发帧会让 VAD hangover 无法扣减、段锁死（本 E2E 实证）"
    - "Pattern: TTS 生成测试语音走 Volcengine fragments 契约（seed-tts-1.0 资源配 beijingxiaoye 音色；seed-tts-2.0 资源只配 contextual 音色——资源/音色错配报 55000000）"
    - "Pattern: 课行按日取键且完结后同日短路（completed-resume）——E2E 场景设计须先查当日课状态再排场景"

key-files:
  created:
    - .planning/phases/13-realtime-minimal-loop/13-12-SUMMARY.md
  modified:
    - .planning/phases/13-realtime-minimal-loop/13-CHECKPOINTS.md

key-decisions:
  - "Task 2 改判：原真 iPad blocking-human 检查点按用户 2026-10-06 指令改为三级顺序（代理本机 E2E → 用户本机能力认证 → iPad 真机认证）；本计划完成第一级，后两级与评级追认、配对样本补采登记为待用户项，不阻塞本计划收口"
  - "E2E 语音素材走真实 TTS（Volcengine，与服务同契约）生成学生口令 WAV 直发 WSS——服务端无法区分来源，替代不可自动化的儿童语音；真实扬声器/麦克风回声观察明确留用户"
  - "b2 噪声探针未过判定为标定类观察项非功能缺陷（StartRMS=0.02 与探针幅度问题，无用户可见副作用），未解决留用户/13-13 决策——不静默改 VAD 阈值"
  - "配对时序样本=0 如实落档：当日课行被 rt-7 完结后同日无活跃课可采；报告管线验证口径（self-test+可读报告）满足计划 backstop truth，配对>0 样本留下一活跃课/VPS 段"
  - "评级映射在用户答复前保持 Pending（hint→confirm 与 hint→timeout 次序），证据中标注，不预写已确认"

patterns-established:
  - "Pattern: 本机预验收记录强制标注『本机预验收，不记为正式验收』（T-13-25），正式证据仍归 13-13 VPS 段"
  - "Pattern: E2E 原始证据（JSONL/时间线/结果矩阵/驱动脚本）留 .scratch/ 不入库，入库内容为脱敏摘要（T-13-30）"

requirements-completed: []  # RT-01/02/03/04/05/07/10 正式判定仍归 13-13 与第六周检查（本计划为本机半场：先暴露先修）

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
    description: "默写课实测 + 评级映射追认 + 三伴随缺陷零复发复核（按用户 2026-10-06 改判：自动化部分由代理本机 E2E 执行）"
    verification:
      - kind: integration
        ref: "Track 1（node WSS 完整课，会话 rt-7）：断言 17/18——首问候/确认推进/四口令不推进/暂停恢复/打断即停/超时推进/完结顺序/review requested-updated-missing=3-3-0 与 review_logs 一致；三缺陷零复现（error 事件=0、静音探针 0 段、问候为首事件）。唯一未过 b2 噪声探针=标定类观察项（无用户可见副作用），留用户/13-13"
        status: pass
      - kind: integration
        ref: "Track 2（Playwright 真实通道）：完结课 resume UI 全链 2 用例+17 断言；lesson_unavailable/playback_ack_timeout 人话映射 served JS/HTML 静态断言+接线代码级核验（两态今日不可达如实标注）"
        status: pass
      - kind: human
        ref: "待用户：本机能力认证（真实扬声器/麦克风/回声两档静默）、iPad 真机认证（十步清单在档）、评级映射追认（Pending）"
        status: pending
    human_judgment: true
    rationale: "麦克风/扬声器/儿童语音/回声不可自动化——代理 E2E 覆盖协议与状态机全流程，物理声道与主观确认留用户三级顺序后两级"
  - id: D3
    description: "rt-timing-report 报告管线可用（--self-test 全断言通过，含 clock_offset/无 offset/失败样本分支）"
    verification:
      - kind: command
        ref: "node scripts/rt-timing-report.js --self-test（exit 0，全部断言通过——收口会话复跑确认）"
        status: pass
    human_judgment: false
  - id: D4
    description: "对会话 rt-timing JSONL 跑报告并摘要落档（标注：本机样本仅验证报告管线，不作第六周判定证据）"
    verification:
      - kind: command
        ref: "node scripts/rt-timing-report.js .scratch/e2e-1312/rt-timing-e2e-1312.jsonl --server-log server-8443.err.log（exit 0 可读报告）+ track2 同（exit 0）"
        status: pass
      - kind: caveat
        ref: "配对数=0 如实落档：当日课行已被 rt-7 完结、同日 completed-resume 短路无活跃课；driver JSONL 发射机制已修正，配对>0 留下一活跃课（如次日新课）或 13-13 VPS 段补采"
        status: documented-limitation
    human_judgment: false

# Metrics
duration: 12min+95min
completed: 2026-10-06
status: completed   # Task 2 按用户 2026-10-06 改判为本机 E2E（代理执行）后三任务收口；用户能力认证/iPad/评级追认登记为待用户项
---

# Phase 13 Plan 12: 本机预验收（转向后交互模型真机半场）Summary

**全量预检绿 + 8443 通道三探针绿 + 真实 RunDue 取词冒烟（Task 1）→ 本机 E2E 代理执行（Task 2 改判）：node WSS 完整默写课 rt-7 17/18 断言、三伴随缺陷零复发、学习记录落库核对；Playwright 真实通道 UI/错误映射全过（Track 2）→ 时序报告管线验证 + 证据脱敏落档（Task 3）。用户三级顺序后两级（本机能力认证、iPad 真机认证）与评级追认、配对样本补采登记为待用户项。**

## Performance

- **Duration:** 12 min（Task 1，2026-10-06 上午）+ 95 min（Task 2/3 本机 E2E 与收口，2026-10-06 下午）
- **Tasks:** 3 / 3 完成（Task 2 按用户改判口径）
- **Files modified:** 2（13-CHECKPOINTS.md 三段追加 + 本 SUMMARY 两版）

## Accomplishments

- **Task 1（前会话完成）**：全量预检全绿；8443 通道复用重试继承实例（PID 50472）；三探针 + WSS 冒烟 session_ready words=10（lesson r0-20261006）；RunDue 真实性以 DB/WAL/事件链三点吻合落档。
- **Task 2 Track 1（本机 E2E 主通道）**：node WSS 客户端按浏览器同款上行时序（常开麦克风 20ms 分帧）驱动运行中真实服务完成整节默写课（会话 rt-7，265.9s）：首问候→确认推进→四类口令（repeat/slow/hint/meaning 均不推进）→暂停/继续（paused 期间语音 0 推进）→打断即停（旧 epoch 零复活）→59.1s 静默超时推进→完结总结播完 ack 后 lesson_complete。**三伴随缺陷零复现**（invalid API response=0 / 静音 VAD 0 段、轮次严格一致 / 首问候为首事件）；学习记录：lessons 行 10/10（v11）、review requested=3/updated=3/missing=0、review_logs 增量=3。唯一未过 b2 噪声探针（标定类观察项，无用户可见副作用，留用户/13-13）。
- **Task 2 Track 2（辅通道）**：Playwright 独立 config（真实通道、ignoreHTTPSErrors、fake-ui）2 用例 + 17 断言全过——完结课 resume UI 全链（进度「已完成」100%、词片 0 越界、ended 态、完结横幅、上行自然释放 T-13-24b）+ lesson_unavailable/playback_ack_timeout/no_due_words 人话映射验证（可达态 DOM 断言 + 不可达态 served JS/HTML 静态断言与代码级核验）。设计行为记录：同日完结课后 completed-resume 短路（无问候无轮次、不耗词、幂等关闭不发 session_end）。
- **Task 3**：`rt-timing-report --self-test` exit 0（收口会话复跑确认）；rt-7/track2 JSONL 报告 exit 0；配对数=0 根因（当日无活跃课）与补采路径落档；证据脱敏落档 13-CHECKPOINTS「本机 E2E」段。
- **语音素材方案 a 成功**：Volcengine TTS（与服务同契约，seed-tts-1.0/beijingxiaoye）生成 11 条学生口令 WAV + 本地静音/噪声探针——素材与全部 E2E 脚本留 `.scratch/`（T-13-30）。

## Task Commits

Each task was committed atomically:

1. **Task 1: 全量预检 + 通道确认 + 取词冒烟证据落档** - `622aba3` (docs)
2. **Task 2 blocking-human 检查点请求落档（后被改判）** - `b6a5541` (docs)
3. **Task 1 收口 SUMMARY（designed stop 版）** - `66c1c7d` (docs)
4. **Task 2/3: 本机 E2E 证据落档** - 见 git log（docs(13-12)）
5. **Task 2/3: SUMMARY 完结 + STATE 更新** - 见 git log（docs(13-12)）

## Files Created/Modified

- `.planning/phases/13-realtime-minimal-loop/13-CHECKPOINTS.md` - 「本机预验收（Task 1）」+「Task 2 执行改判：本机 E2E（代理执行）」段：Track 1/2 证据、Task 3 报告口径、遗留观察、待用户项（脱敏）
- `.planning/phases/13-realtime-minimal-loop/13-12-SUMMARY.md` - 本文件（status: completed）

（E2E 驱动/探针/素材/原始证据留 `.scratch/`（e2e-rt-1312.mjs、e2e-rt-1312-short.mjs、pw-real-1312.config.js、fixtures-1312/、e2e-1312/），不入库——T-13-30 纪律。）

## Decisions Made

见 frontmatter key-decisions（Task 2 改判三级顺序、TTS 素材同契约、b2 判标定类不静默改码、配对=0 如实落档、评级保持 Pending）。

## Deviations from Plan

- **Task 2 由「真 iPad blocking-human」改判为「本机 E2E 代理执行」**（2026-10-06 用户指令，三级顺序：代理本机 E2E → 用户本机能力认证 → iPad 真机认证）。原十步 iPad 清单保留在 CHECKPOINTS 供用户后两级使用；本计划按改判口径收口，用户项登记为待办不阻塞。
- Task 1 重启语义在重试继承条件下为空操作（前会话已落档理由）。
- 其余按计划执行；发现项均只取证未改码（b2 标定类、0 字节帧、zap 冗余字段、completed-resume 设计行为）。

## Issues Encountered

- 本机 Privoxy 劫持 localhost：探针一律 `--noproxy "*"`（已固化）。
- node 驱动首跑轮次锁死：句间停发帧致 VAD hangover 无法扣减——改常开麦克风泵（含静音帧）后通过；已固化为 pattern。
- TTS 资源/音色错配报 55000000：seed-tts-2.0 资源只配 contextual 音色，改用 seed-tts-1.0+beijingxiaoye（生产 fragments 同契约）后通过。
- 裸 `py` 跑 vocab_manager 静默退出（WindowsApps 存根）：须用服务同款 venv 解释器。
- 服务 zap 写入滞后于 wire：驱动改 10s 轮询日志尾。
- 当日课完结后同日无法再采活跃课样本（completed-resume 短路）——配对样本留待下一活跃课。

## User Setup Required（待用户项，不阻塞本计划）

1. **本机能力认证**（三级顺序第二级）：真实扬声器/麦克风下复测关键体验 + 回声两档静默观察（0.5～1.5s 与更长，S-01/S-06 口径——不可自动化部分）。
2. **iPad 真机认证**（第三级）：CHECKPOINTS 十步清单；导出 rt-timing JSONL。
3. **评级映射追认**（Pending）：confirm→good、timeout→hard、hint→again 仅无终态时；特别确认 hint→confirm 与 hint→timeout 次序；若修正则改 pkg/rt/dictation.go 常量表重跑单测复测。
4. **配对时序样本补采**：下一活跃课（如次日新课）用修正后 driver 复跑短会话，或 13-13 VPS 段补。

## Next Phase Readiness

- 13-13（VPS 部署 + 正式验收）前置门就绪：本机防火墙职能完成（先暴露先修——三伴随缺陷零复发、协议/状态机/落库/中文文案全链实证）；遗留仅 b2 标定决策与用户三项，均不阻塞 VPS 部署准备。
- 首周检查行（2026-10-07）证据仍归 VPS 正式通道；本机段只承担预验收职能（T-13-25）。
- 环境备忘：8443 实例（PID 50472）与 8765 根证书分发在位；重启用 `.scratch/run-local-8443.ps1`（detached 模式），禁前台启动。

---
*Phase: 13-realtime-minimal-loop*
*Completed: 2026-10-06（Task 2 改判本机 E2E 后三任务收口；待用户项另列）*
