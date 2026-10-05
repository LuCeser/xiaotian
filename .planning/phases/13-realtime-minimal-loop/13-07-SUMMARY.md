---
phase: 13-realtime-minimal-loop
plan: 07
subsystem: api
tags: [go, sentinel-errors, sqlite-migration, json-tags, websocket-protocol, contract-first]

# Dependency graph
requires:
  - phase: 13-realtime-minimal-loop/13-01
    provides: pkg/lesson Store（EnsureLesson INSERT OR IGNORE 首内容优先范式、Migrate 幂等、ErrInvalidLesson 哨兵）
  - phase: 13-realtime-minimal-loop/03
    provides: pkg/rt protocol 17 消息词表、StateMsg/ErrorMsg/SessionEndedMsg/SentenceAckMsg 结构与 ErrorCode 映射范式
  - phase: 13-realtime-minimal-loop/06 (wave 2 合并后为现存源码事实)
    provides: pkg/mimo ASR executeASRRequest 双分支现状、legacy lessons 表（VPS 存量）
provides:
  - "pkg/mimo：ErrEmptyTranscription 哨兵——ASR 空 choices（合法空结果）与 ErrInvalidResponse（损坏响应）类型化分离"
  - "pkg/lesson：Word{word/meaning/example} 类型（json 标签与 pybridge.DueCard 逐字对齐）、lessons.cards 列、Migrate PRAGMA 探测幂等补列、Store.EnsureLessonCards（首内容优先）、GetLesson 返回 Cards"
  - "pkg/rt：StatePaused、ErrCodeLessonUnavailable/ErrLessonUnavailable、ErrCodePlaybackAckTimeout/ErrPlaybackAckTimeout、ReasonLessonComplete/ReasonNoDueWords；SentenceAckMsg 冻结为实际播放完成语义"
affects: [13-08 (默写 FSM tracer), 13-09 (空转写容忍), 13-10 (PyFSRS 适配器), 13-11 (前端暂停/超时处理)]

# Actuals (#2632)
actuals:
  tokens: 7558        # chars/4 over the realized diff (30234 chars across 7 files)
  tasks: 3
  commits: 3          # MEASURED: git rev-list --count 06d33f0..6f12ab2
commits: 3
plan_head_before: 06d33f02dbd721dfd62cf84da1b3a0446ed01f1a
plan_head_after: 6f12ab2affffa291d8c8a2384e62db1c30aea3bc

# Tech tracking
tech-stack:
  added: []           # 零新增依赖（T-13-SC：包安装面零变化）
  patterns:
    - "哨兵分类分离：合法空结果（ErrEmptyTranscription）与损坏响应（ErrInvalidResponse）互斥双测锚定，调用方 errors.Is 判定"
    - "加性 SQLite 迁移：PRAGMA table_info 探测缺列才 ALTER TABLE，幂等可重跑；旧行读回不损有测试证明"
    - "协议词表冻结：新状态/错误码/结束原因只加常量不加消息类型，wire 形状零变化"

key-files:
  created: []
  modified:
    - pkg/mimo/errors.go
    - pkg/mimo/client.go
    - pkg/mimo/client_test.go
    - pkg/lesson/store.go
    - pkg/lesson/store_test.go
    - pkg/rt/protocol.go
    - pkg/rt/protocol_test.go

key-decisions:
  - "ErrEmptyTranscription 独立哨兵：空 choices 是供应商对无声/噪声段的合法回应而非故障；grep 核查确认无任何调用方对 ErrInvalidResponse 有 errors.Is 特判，拆分零行为回归"
  - "lessons.cards 迁移用「PRAGMA table_info(lessons) 探测 + ALTER TABLE ADD COLUMN」而非重建表：13-06 期间 VPS 创建的 legacy 表无手工干预获得新列，旧行 cards 读回空切片"
  - "EnsureLessonCards 明确边界（godoc + 双测试锚定）：INSERT OR IGNORE 不是替换机制，legacy 空 cards 行不被二次调用伪填充——13-08 必须识别该状态并返回 lesson_unavailable"
  - "playback_ack_timeout 冻结为仅 ErrorMsg.Code：不是 SessionEndedMsg.Reason、不是实际播放完成证据；超时降级顺序为「先 ErrorMsg 后仍携带原 lesson_complete/no_due_words reason 的结束消息」，有字面值守卫测试防泄漏进 Reason 词表"
  - "SentenceAckMsg godoc 从 started/finished 收紧为实际播放完成（匹配 audio_end 已见 + 最后一个 AudioBufferSourceNode onended 生效后才发送）；字段形状不变，运行时身份校验归 13-08，浏览器逐句排程归 13-11（T-13-12b）"

patterns-established:
  - "Pattern: 空结果≠故障——供应商合法空回应走独立哨兵，双向互斥断言（A≠B 且 B≠A）锁死分类"
  - "Pattern: 协议扩展只加常量词表、零消息类型增减，17 消息往返测试全绿即 wire 零漂移证明"

requirements-completed: [RT-03, RT-07]  # 共享 ID：RT-03/RT-07 亦被 13-08/13-09/13-12 等声明，mark-complete 由 orchestrator 按 shared-ID 门槛收口

# Coverage metadata (#1602)
coverage:
  - id: D1
    description: "pkg/mimo ASR 空转写类型化：HTTP 200 + JSON 解码成功 + choices 为空 → ErrEmptyTranscription（errors.Is 可判定），解码失败仍 → ErrInvalidResponse，两哨兵互斥"
    requirement: RT-03
    verification:
      - kind: unit
        ref: "pkg/mimo/client_test.go#TestClient_TranscribeEmptyChoices（空 choices → ErrEmptyTranscription 且 ≠ ErrInvalidResponse）"
        status: pass
      - kind: unit
        ref: "pkg/mimo/client_test.go#TestClient_TranscribeInvalidJSON（非法 JSON → ErrInvalidResponse 且 ≠ ErrEmptyTranscription）"
        status: pass
      - kind: unit
        ref: "command: go test ./pkg/mimo/ -short -count=1 全绿"
        status: pass
    human_judgment: false
  - id: D2
    description: "pkg/lesson 词卡存储：Word 类型（json 标签 word/meaning/example 与 pybridge.DueCard 逐字对齐）、lessons.cards 列、PRAGMA 探测幂等补列迁移、EnsureLessonCards 首内容优先、GetLesson 返回 Cards"
    requirement: RT-07
    verification:
      - kind: unit
        ref: "pkg/lesson/store_test.go#TestEnsureLessonCards（往返 + 首内容优先 + 非法参数拒写）"
        status: pass
      - kind: unit
        ref: "pkg/lesson/store_test.go#TestMigrateAddsCardsColumnToLegacyTable（legacy 表补列幂等、旧行不损、Cards 空切片）"
        status: pass
      - kind: unit
        ref: "pkg/lesson/store_test.go#TestEnsureLessonCardsDoesNotReplaceLegacyEmptyCards + TestGetLessonCorruptedCardsJSON"
        status: pass
      - kind: unit
        ref: "command: go test ./pkg/lesson/ ./pkg/database/ -count=1 全绿"
        status: pass
    human_judgment: false
  - id: D3
    description: "pkg/rt 协议词表扩展：StatePaused、lesson_unavailable/playback_ack_timeout 错误码+哨兵+ErrorCode 映射、lesson_complete/no_due_words 结束原因、SentenceAckMsg 播放完成语义、playback_ack_timeout 仅 ErrorMsg.Code 冻结"
    requirement: RT-03
    verification:
      - kind: unit
        ref: "pkg/rt/protocol_test.go#TestProtocolStateRoundTrip（含 StatePaused 往返）"
        status: pass
      - kind: unit
        ref: "pkg/rt/protocol_test.go#TestErrorCode（两新哨兵裸/Wrap 映射 + 默认回退）"
        status: pass
      - kind: unit
        ref: "pkg/rt/protocol_test.go#TestProtocolPausedAndReasonLiterals + TestProtocolSentenceAckCompletionRoundTrip"
        status: pass
      - kind: unit
        ref: "command: go test ./pkg/rt/ -run \"TestProtocol|TestErrorCode\" -count=1（17 消息往返零漂移全绿）"
        status: pass
    human_judgment: false
  - id: D4
    description: "存量调用方零回归 backstop：pkg/voice Transcribe 消费者、EnsureLesson 既有调用点（pkg/rt/turn.go、pkg/rt/mimo_adapters.go）、pkg/lesson Executor.AdvanceWord 均行为不变"
    verification:
      - kind: unit
        ref: "command: go build ./... && go vet ./pkg/mimo/ ./pkg/lesson/ ./pkg/rt/ && go test ./pkg/mimo/ ./pkg/lesson/ ./pkg/rt/ ./pkg/voice/ -short -count=1 全绿"
        status: pass
      - kind: unit
        ref: "grep 断言：无生产代码 errors.Is(ErrInvalidResponse) 特判；EnsureLesson 调用点签名未改"
        status: pass
    human_judgment: false

# Metrics
duration: 11min
completed: 2026-10-05
status: complete
---

# Phase 13 Plan 07: 三包契约扩展（mimo 空转写 / lesson 词卡 / rt 协议词表）Summary

**状态机默写助手转向的第一块地基：MiMo ASR 空转写哨兵 ErrEmptyTranscription 与真故障分离、lesson.Word 词卡（word/meaning/example）随课堂行持久化含 legacy 幂等补列迁移、rt 协议新增 paused/lesson_unavailable/playback_ack_timeout 与 lesson_complete/no_due_words 自然结束词表——三契约全部测试锚定，存量调用零回归**

## Performance

- **Duration:** 11 min（2026-10-05T01:05:08Z → 2026-10-05T01:15:32Z）
- **Started:** 2026-10-05T01:05:08Z
- **Completed:** 2026-10-05T01:15:32Z
- **Tasks:** 3 / 3
- **Files modified:** 7（全部为计划声明的既有文件扩展，零新建文件、零删除）

## Accomplishments

- `pkg/mimo`：ErrEmptyTranscription 哨兵落地——HTTP 200 + JSON 解码成功 + choices 为空是供应商合法空回应；与 ErrInvalidResponse（损坏/不可解析）严格分离，双向互斥断言锁死分类（T-13-11 缓解），13-09 可据 errors.Is 做静默容忍
- `pkg/lesson`：Word 类型 json 标签与 pybridge.DueCard 逐字对齐（13-10 零损映射）；lessons.cards 列经 PRAGMA table_info 探测幂等补列，VPS 上 13-06 期间的 legacy 表无手工干预升级（T-13-12 缓解）；EnsureLessonCards 首内容优先，legacy 空 cards 不被 INSERT OR IGNORE 伪替换——该边界以双测试锚定并在 godoc 冻结为 13-08 的识别义务
- `pkg/rt`：StatePaused、ErrCodeLessonUnavailable/ErrLessonUnavailable、ErrCodePlaybackAckTimeout/ErrPlaybackAckTimeout、ReasonLessonComplete/ReasonNoDueWords 全部就绪并经 ErrorCode errors.Is 映射、往返与字面值测试锚定；SentenceAckMsg godoc 收紧为实际播放完成确认（T-13-12b 缓解）；零新增消息类型、wire 字段形状零变化（T-13-SC：零新增依赖）

## Task Commits

Each task was committed atomically:

1. **Task 1: pkg/mimo — ASR 空转写哨兵 ErrEmptyTranscription** - `9cdb8ed` (feat)
2. **Task 2: pkg/lesson — Word 类型与 lessons.cards 列（幂等迁移）** - `14f2355` (feat)
3. **Task 3: pkg/rt/protocol — paused / 错误码 / 结束原因词表** - `6f12ab2` (feat)

**Plan metadata:** 本 SUMMARY 提交（docs(13-07)）

## Files Created/Modified

- `pkg/mimo/errors.go` - ErrEmptyTranscription 哨兵（godoc 写明与 ErrInvalidResponse 的语义边界）
- `pkg/mimo/client.go` - executeASRRequest 空 choices 分支改包 ErrEmptyTranscription；解码失败分支不动
- `pkg/mimo/client_test.go` - TestClient_TranscribeEmptyChoices / TestClient_TranscribeInvalidJSON 双向互斥断言
- `pkg/lesson/store.go` - Word 类型、cards 列 DDL、lessonsHasCardsColumn 探测、Migrate 幂等补列、Lesson.Cards、EnsureLessonCards、GetLesson cards 读取与损坏 JSON 包装错误
- `pkg/lesson/store_test.go` - TestEnsureLessonCards / TestMigrateAddsCardsColumnToLegacyTable / TestEnsureLessonCardsDoesNotReplaceLegacyEmptyCards / TestGetLessonCorruptedCardsJSON
- `pkg/rt/protocol.go` - StatePaused、两错误码+两哨兵+ErrorCode 映射、两结束原因常量、SessionEndedMsg 降级顺序契约、SentenceAckMsg 播放完成语义
- `pkg/rt/protocol_test.go` - StatePaused 往返、TestErrorCode 映射矩阵、TestProtocolPausedAndReasonLiterals 字面值守卫、sentence_ack completion 往返

## Must-Have Truths Verification

| Truth | Evidence | Status |
|-------|----------|--------|
| 空 choices → ErrEmptyTranscription（errors.Is 可判定），解码失败仍 ErrInvalidResponse，两哨兵分离 | TestClient_TranscribeEmptyChoices + TestClient_TranscribeInvalidJSON 互斥断言 | PASS |
| Word 三字段 json 标签与 pybridge.DueCard 逐字对齐；新库建表即含 cards；legacy 表幂等补列；EnsureLessonCards 首内容优先；GetLesson 返回 Cards | TestEnsureLessonCards + TestMigrateAddsCardsColumnToLegacyTable；store.go 标签逐字比对 executor.go DueCard | PASS |
| paused/lesson_unavailable/playback_ack_timeout 词表就绪；playback_ack_timeout 仅 ErrorMsg.Code、非 Reason、非播放完成证据；sentence_ack = 实际播放完成确认；零消息类型增减 | TestProtocolPausedAndReasonLiterals（含 Reason 泄漏守卫）+ TestErrorCode + 17 消息往返全绿 + protocol.go godoc | PASS |
| 存量调用方零回归（pkg/voice、EnsureLesson 调用点、17 往返测试） | go test ./pkg/mimo/ ./pkg/lesson/ ./pkg/rt/ ./pkg/voice/ -short -count=1 全绿；grep 核查调用点未改 | PASS |

## Decisions Made

见 frontmatter key-decisions（哨兵分类、探测式迁移、空 cards 边界冻结、playback_ack_timeout 词表归属、sentence_ack 语义收紧）。

## Deviations from Plan

### Auto-fixed Issues

**1. [Rule 3 - Blocking] Task 1 验证命令的 -run 过滤器修正**
- **Found during:** Task 1（verify 步骤）
- **Issue:** 计划的验证命令 `-run "TestASR|TestTranscribe"` 按子串匹配命不中 `TestClient_Transcribe*` 系测试名（不含 `TestTranscribe` 子串），首跑仅执行 TestASROptions，新增两用例未运行——违反 fails_when「输出缺少新增的两个用例名」
- **Fix:** 过滤器改为 `-run "TestASR|Transcribe"`（仅验证命令层面的修正，零生产代码改动），新增用例与全部既有 Transcribe 用例均实际执行并通过
- **Files modified:** 无（验证命令参数）
- **Verification:** TestClient_TranscribeEmptyChoices / TestClient_TranscribeInvalidJSON 出现在输出且 PASS
- **Committed in:** 无独立提交（Task 1 提交 9cdb8ed 内含的测试本体未变）

---

**Total deviations:** 1 auto-fixed（1 blocking，验证命令层面）
**Impact on plan:** 不改变任何实现与测试内容，仅让计划的验证意图（新用例必须实际运行）真正成立。无范围蔓延。

## Issues Encountered

- 本计划由 orchestrator 指定为顺序执行器，在主工作树（main 分支）直接提交（config `branching_strategy: none`，与 13-01～13-06 合并后的主线一致）；每任务仅暂存计划声明的文件，工作区内无关的未提交改动（.gitignore、CONTEXT.md、pkg/* EOL 噪声、根目录未跟踪 scratch 文件）一律未触碰

## User Setup Required

None - no external service configuration required.

## Threat Mitigations Delivered

| Threat | Disposition | Where |
|--------|-------------|-------|
| T-13-11 (Tampering, 空响应分类, medium) | mitigate | 哨兵分离 + 双向互斥断言（client_test.go），杜绝损坏响应当空结果静默吞掉或空结果当故障误报 |
| T-13-12 (Tampering, legacy ALTER, medium) | mitigate | PRAGMA table_info 探测后 ALTER，幂等可重跑；legacy 迁移测试证明旧行不损、空 cards 不被伪替换；DDL 仅加性 |
| T-13-12b (Spoofing, sentence_ack 语义含混, high) | mitigate | 协议 godoc 冻结为 actual playback completion（audio_end 已见 + onended 生效）；epoch+sentence_id 双键不变；13-08/13-11 归属写明 |
| T-13-SC (Tampering, 包安装, high) | mitigate | 零新增依赖，包安装面零变化 |

## Next Phase Readiness

- 13-08（默写 FSM 主环 tracer）所需全部契约就绪：ErrLessonUnavailable/ErrCodeLessonUnavailable（session_init 取词失败路径）、StatePaused（暂停口令）、ReasonLessonComplete/ReasonNoDueWords（自然结束）、SentenceAckMsg 播放完成语义与 ErrPlaybackAckTimeout（有界等待降级顺序）；legacy 空 cards 的识别义务已在 EnsureLessonCards godoc 冻结
- 13-09（空转写容忍）：errors.Is(err, ErrEmptyTranscription) 即静默容忍判据
- 13-10（PyFSRS 适配器）：Word 标签逐字对齐 DueCard，零损映射
- 13-11（前端）：paused 状态值、playback_ack_timeout 前端义务（诚实提示 + 保留已排程音频）均已在协议 godoc 冻结
- RT-03/RT-07 为共享 ID（13-08/13-09/13-12 等亦声明），REQUIREMENTS 勾选由 orchestrator 按 shared-ID 门槛收口

## Self-Check: PASSED

- [x] 7 个修改文件全部存在于磁盘（Test-Path 验证）
- [x] 提交 9cdb8ed / 14f2355 / 6f12ab2 均在 git log（--all 验证）
- [x] commits: 3 为实测值（git rev-list --count 06d33f0..HEAD），与 narrated 一致
- [x] 计划级验证命令全部通过：go build ./... / go vet ./pkg/mimo/ ./pkg/lesson/ ./pkg/rt/ / go test ./pkg/mimo/ ./pkg/lesson/ ./pkg/rt/ ./pkg/voice/ -short -count=1 全绿；17 消息往返测试零漂移
- [x] 无 stub 模式（无 TODO/placeholder/空数据源）；无计划外威胁面新增

---
*Phase: 13-realtime-minimal-loop*
*Completed: 2026-10-05*
