---
gsd_state_version: "1.0"
milestone: v2.0
milestone_name: realtime-tutor — ACTIVE
current_phase: 13
current_phase_name: R0 本地最小闭环
status: planning
stopped_at: Phase 13.3 complete, ready to plan Phase 13
last_updated: "2026-10-11T00:22:28.240Z"
last_activity: 2026-10-11
last_activity_desc: Phase 13.3 complete, transitioned to Phase 13
state_head: d6870fe6ef69b40ab9dd157a3b7eb28b6ea851b5
progress:
  total_phases: 7
  completed_phases: 1
  total_plans: 26
  completed_plans: 26
  percent: 14
---

# Project State

## Product Acceptance Rebaseline — 2026-09-30

产品验收未通过。v1.0 superseded / override_closeout，旧 Phase 10～12 不再执行；新里程碑 v2.0 realtime-tutor。Phase 13～16 对应 R0～R3。MiMo 可替换接口、自研 WebSocket→pion WebRTC、服务端状态机幂等推进、真实 FSRS 读写和真机 RT 验收为当前范围。

## Project Reference

See: .planning/PROJECT.md (updated 2026-10-11)

**Core value:** 学生在浏览器与家教自然实时交流，理解上下文、允许打断、等待纸笔书写，学习结果进入真实记录。
**Current focus:** Phase 13 — R0 本地最小闭环（本地人工验收收口）

## Current Position

Phase: 13 — R0 本地最小闭环
Plan: Not started
Status: Ready to plan
Last activity: 2026-10-11 — Completed quick task 261011-b8u: 13.3 UI 审计三项修复（chip AA 对比度、统一强调蓝、tab 60px 触控）
Next: Phase 13/13.1 本地人工验收收口（13.3 已闭环）；Phase 13.2（VPS）已搁置（2026-10-09）；Phase 14 为下一实施阶段

本地验收未通过项（2026-10-09 14:33 北京时间）：用户报告“再练一组功能测试不通过”。已登记于 [Phase 13.1 UAT](phases/13.1-three-stage-dictation-lesson/13.1-UAT.md) 测试 6 / G-13.1-local-4。**2026-10-10 更新：13.3 真机 UAT-5「再练一组」全链路通过**（完结课总结页→再练一组→家长端「加练」记录＋「不调整复习计划」断言），提示该缺陷可能已被后续修复（13.1-08/13.3 系列改动）——建议重测后按 13.1-UAT 流程关闭 G-13.1-local-4。

13-01～13-12 实施已完成；Phase 13 和 13.1 本地人工验收尚未通过。原未执行 13-13 已移出 Phase 13，独立 Phase 13.2 等待本地通过后再规划部署；正式 RT 产品验收按各项证据判定，不因实施完成自动通过。起算日 2026-09-30（规划计时），第一周/第三周/第六周截止为 2026-10-07/10-21/11-11 北京时间 23:59；不因迁移阶段或供应商重置。

本轮规划检查：7/7 待执行计划结构有效，7/7 阶段需求覆盖；CONTEXT 决策覆盖工具因无可跟踪 ID 跳过，独立检查已核对现行能力与计划的一致性。S-01～S-06、HIGH-1 及评审修正意见已落实。13-07 历史波次的无待执行依赖仅为 INFO；7 条未分类边界探测保留 unresolved，评级优先级及真机证据仍待人工检查。本轮仅修改规划文档，未运行实现测试、部署或真机验收。

## Performance Metrics

v2.0：6 个活跃阶段；Phase 13 实施 12/12、Phase 13.1 实施 8/8，均待本地人工验收。Phase 13.2 独立负责 VPS 部署与真机正式验收，0 个可执行计划、未部署，本地未通过不得开始。历史统计保留于 milestones/v1.0-STATE.md，不用于新里程碑验收结论。
**Per-Plan Metrics:**

| Plan | Duration | Tasks | Files |
|------|----------|-------|-------|
| Phase 13.1 P01 | 309 min | 3 tasks | 17 files |
| Phase 13.1 P02 | 244 min | 3 tasks | 8 files |
| Phase 13.1 P03 | 31 min | 3 tasks | 14 files |
| Phase 13.1 P04 | 245 min（含配额中断恢复等待） | 3 tasks | 10 files |
| Phase 13.1 P05 | 47 min | 3 tasks | 17 files |
| Phase 13.1 P06 | 25 min | 2 tasks | 7 files |
| Phase 13.1 P07 | 41 min | 2 tasks | 5 files |
| Phase 13.1 P08 | 140 min (2 executor runs, rate-limit pause) | 3 tasks | 8 files |

## Accumulated Context

以下保留旧里程碑的决策和待办登记用于追溯；旧阶段命令、历史缺陷判断及快速任务记录不构成当前任务建议。当前执行只以 Phase 13～16、RT-01～RT-10 和日期检查为准。旧登记的未解决状态见 milestones/v1.0-CLOSEOUT.md，不能自动恢复旧阶段。

### Decisions

Decisions are logged in PROJECT.md Key Decisions table.
Recent decisions affecting current work:

- [Phase ?]: PythonConfig 采用 13 个操作性字段（路径/超时/并发），照抄 MemoryConfig 范式；密钥经 os.Getenv 注入子进程而非入 Config（D-05）
- [Phase ?]: vendor 脚本逐字节拷入 third_party/python/（D-03）；fsrs 包名经上游 requirements.txt 确认正确，保持 fsrs>=0.0.1 不变
- [Phase 03]: pybridge.DueCard/UpdatedCard 字段照 vocab_manager.py 真实 JSON 契约（id/word/meaning/example/unit/state/due/stability/difficulty；word/rating/next_due/state/stability），非 plan 草案的 translation/pronunciation/[]string——Rule 1 修正
- [Phase 03]: pybridge semaphore.acquire 加 fast-path ctx.Done() 检查避免 Go select 非确定性（已取消 ctx + 空位时随机选分支）——Rule 1 修正
- [Phase 03]: pybridge 公共契约层 logger-free：接口/类型/纯函数/信号量不注入 infra.Logger；日志注入点在 Plan 03-03 Bridge 实现（D-25 canonical）
- [Phase ?]: Plan 03-03 Bridge: NewBridge 返回 (*Bridge, error) 照抄 NewConnectionRegistry DI 范式（cfg 逐字段校验 + nil-logger 替换）；RunDue/Review/Pregenerate 加 stub 返回 ErrBridgeNotImplemented 让 var _ Executor 编译期检查就位，Plan 03-04/05 替换时同 commit 删除 sentinel
- [Phase ?]: Plan 03-03 childEnv 加 PYTHONUNBUFFERED=1 强制 stdout/stderr 在 Wait 返回前 flush（Rule 2 correctness，避免 SIGKILL 时漏捕上游诊断）；setProcessGroup 双 build-tag 隔离照 main.go 第 12-13 行 SIGTERM 约定同类
- [Phase ?]: Plan 03-04: argv 构造用 cfg.VocabScript 直接作 args[0]（照 RESEARCH+plan），不主动 filepath.Join(ScriptsDir, VocabScript)——调用方对路径形式负全责
- [Phase ?]: Plan 03-04: ErrPythonEmpty 在 RunDue/RunReview 显式前置 len(stdout)==0 检查（Rule 1），让 4 类 sentinel 真正互斥可达，对齐 executor.go 契约
- [Phase ?]: Plan 03-04: reviewArgs 自动 size-based 选 --results vs --file（plan 明确路径），不读 opts.UseFile/ResultsFile；nil Results 兜底为空 map 防 'null' 被脚本拒（Rule 2）
- [Phase ?]: Plan 03-05: pregenArgs 对空 Words/Language 在 argv 层返回 error（脚本 required=True 字段提前拦下），避免无谓 subprocess spawn + 错误消息更精确
- [Phase ?]: Plan 03-05: Bridge.pregenSem cap=1 专用串行化（独立于 D-02 全局 sem），防 fcntl.flock 全局锁竞争致 exit 0 + 空 stdout 静默失败；acquire 在 b.run 前让排队 caller 不饥饿 due/review 路径
- [Phase ?]: Plan 03-05: classifyPregenResult 提取为纯函数让 4 类 sentinel 分支无 subprocess 表驱动可测（plan 明确允许）；Pitfall 6 空 stdout exit 0 严格返回 ErrPythonEmpty 绝非性零路径成功
- [Phase ?]: Plan 03-05: ErrBridgeNotImplemented + RunPregenerate stub 全删除（Phase 3 完结），var _ Executor = (*Bridge)(nil) 现完全靠 RunDue/RunReview/RunPregenerate 三个真方法通过编译期检查
- [Phase 04]: modernc.org/sqlite v1.55.0 作为纯 Go SQLite 驱动（CGO_ENABLED=0 跨平台契约）
- [Phase 04]: DatabaseConfig 仅含 Path 字段，pragmas（WAL/busy_timeout/foreign_keys）由 database.Open 拥有，不在 Config 中暴露
- [Phase 04]: CGO_ENABLED=0 在 Makefile build target 和 CI workflow 中锁定，防止隐式 CGO 依赖
- [Phase 09]: Plan 09-01: Go 默认 DB 路径优先用 python.vocabDBPath（database.path 为空时）——CardRepository 必须与 Python 写同一 vocab DB（多进程 WAL），否则读路径 "no such table: cards" — D-12 读路径（Go 经 CardRepository 读 SQLite）要求 Go 打开的库文件与 python.vocabDBPath 相同；旧的无条件 $HOME/.home-tutor-agent/vocab.db 默认值让读路径 500。同 commit 记录：addArgs 恒用 list-mode JSON 保住 example 字段；MgmtTimeout 为 Phase 9 管理命令共享超时
- [Phase 09]: [Phase 09]: Plan 09-02: fsrs_config 加载器对缺失/非法 KEY 做整文件失效处理（两个旋钮一起回退默认），绝不做部分配置——与 plan 的 any-failure 语义一致 — 部分配置会让回退语义变得难推理；计划文本明确要求 any failure 时用完整默认值构造 Scheduler
- [Phase 09]: [Phase 09]: Plan 09-02: loader 的 Warning 在模块导入时打到 stdout（先于命令 JSON），bridge 的 extractTrailingJSON 天然容忍——已由完整 py-e2e 套件验证而非假设 — Pitfall 9：损坏的 fsrs_config.json 不得弄垮学生听写路径；Warning+回退是唯一安全出口
- [Phase 09]: [Phase 09]: Plan 09-02: cmd_edit 在 commit 后重新 SELECT 打印更新后的 word，JSON 契约始终反映已提交状态 — 09-03 的 RunEdit 直接依赖该 JSON 的真实性；打印旧值会造成契约谎言
- [Phase 09]: unitsResponse reuses database.UnitCount directly so the GET /units wire shape has one definition and cannot drift — Handler-local duplicate structs drift silently when the repo type changes; embedding the repo type keeps endpoint and repository in lockstep (Plan 09-04)
- [Phase 09]: Plan 09-04 resume: interrupted Task 2 work adopted after diff review instead of rewritten; only its seed INSERT arg-count bug fixed — The partial work already mirrored listWords conventions and the plan test spec; rewriting would discard sound design and add churn (Plan 09-04)
- [Phase 09]: [Phase 09]: Plan 09-05 mutation handlers share one mapBridgeError — vocab_manager.py stdout diagnostics ("Card N not found"/"No cards found") map to 404 not_found truncated to 300 runes; the four pybridge sentinels map to 502 python_failed — One mapping cannot drift per-handler; Pitfall 2 closes end-to-end at the API layer and T-09-07 keeps subprocess output bounded in the error envelope
- [Phase 09]: [Phase 09]: Plan 09-05 fsrs_config.json written atomically (same-directory temp + Sync + os.Rename, failure removes temp and preserves original); FSRSSettings struct declaration order fixes on-disk key order — A half-written config would poison every future vocab_manager.py spawn (loader falls back wholesale); atomic replace + fixed key order keeps the Go writer key-for-key with the Python reader (D-14)
- [Phase 09]: Plan 09-06 resume: interrupted Task 2 work (781+99+25+189 lines across parent-modes.js/parent-app.js/parent.html/parent.css) adopted after per-file diff review against the plan spec instead of rewritten — every spec item (tabs, D-06 radio validation, D-08 optional fields, shared datalist, preview table, transparency banner, createElement/textContent) was already present and verified green; zero fragments needed rewriting (Plan 09-04 precedent) — Plan 09-06 resume: interrupted Task 2 work (781+99+25+189 lines across parent-modes.js/parent-app.js/parent.html/parent.css) adopted after per-file diff review against the plan spec instead of rewritten — every spec item (tabs, D-06 radio validation, D-08 optional fields, shared datalist, preview table, transparency banner, createElement/textContent) was already present and verified green; zero fragments needed rewriting (Plan 09-04 precedent)
- [Phase 09]: Batch result banner names the skipped words via a pre-commit GET /words snapshot: the API returns only {added,skipped} counts, but skips are UNIQUE(word,category), so skipped == pasted ∩ existing — the snapshot derives the readable list; on snapshot failure the banner still renders the honest counts (transparency never depends on the extra request) — Batch result banner names the skipped words via a pre-commit GET /words snapshot: the API returns only {added,skipped} counts, but skips are UNIQUE(word,category), so skipped == pasted ∩ existing — the snapshot derives the readable list; on snapshot failure the banner still renders the honest counts (transparency never depends on the extra request)
- [Phase 09]: The batch commit is refused client-side above 100 entries (MAX_BATCH_ENTRIES mirrors pkg/web/api maxEntries) with a split-paste hint, and at zero valid rows the commit button stays disabled — bounded request bodies enforced before the request is ever sent (threat-model trust boundary, Plan 09-01 caps) — The batch commit is refused client-side above 100 entries (MAX_BATCH_ENTRIES mirrors pkg/web/api maxEntries) with a split-paste hint, and at zero valid rows the commit button stays disabled — bounded request bodies enforced before the request is ever sent (threat-model trust boundary, Plan 09-01 caps)
- [Phase ?]: [Phase 09]: Plan 09-07 browse unit-level 重命名/删除 act per-category across the categories present in the group's rows, and header actions are WITHHELD in the 全部+search combination — destructive confirm copy must always match the DELETE {category, unit} server scope (T-09-05) — The mutation API requires category; a merged 全部 group can span both categories — iterating the rows' categories is exact without an extra fetch, and in the one ambiguous combination the group shows no unit-level buttons instead of a count the API would not honor
- [Phase ?]: [Phase 09]: Plan 09-07 unit-group headers build the 48px aria-expanded toggle as a SIBLING of the 重命名/删除 action buttons — buttons are never nested (invalid HTML breaks the accessibility tree); localStorage parent.units.collapsed stores a JSON array of unit names, re-read defensively on every render — UI-SPEC button-headers-with-aria-expanded is preserved per-toggle while keeping valid HTML; the per-render re-read makes collapse state self-healing against manual storage edits
- [Phase ?]: [Phase 09]: Plan 09-07 SettingsCard interval slider caps at 365 (plan spec) though the API allows 36500, and the edit modal sends ALL four fields (provided-empty clears the column) because the modal renders the word's full current state — Values above 365 were never settable from this UI; a full-state editor matching visible fields makes PUT semantics (empty = clear, D-08 partial-form valid) obvious to the parent
- [Phase 09]: E2E net-zero pattern formalized (09-08): dedicated e2e-* units only, finally-shaped API cleanup backstops the UI deletion path, suite idempotent across consecutive runs; the duplicate-skip banner is made deterministic by API-seeding the duplicate target before the paste
- [Phase 09]: Backstop styles shipped under Rule 2 in the e2e plan (09-08): the 2-line meaning clamp (-webkit-line-clamp on .word-meaning) and unit-name title attribute asserted by the must-haves were absent from the Plan 09-07 styles — fixed in the Task 2 commit and asserted behaviorally (rendered height within 2 line-heights; Chromium computes display:-webkit-box as flow-root while the clamp applies)
- [Phase 13]: R0 交互模型转向「状态机默写助手」（2026-10-03 用户决策；本机预验收 rt-6 证据：12 轮对话 0 次课堂工具调用）。服务端状态机控制节奏：FSRS 取到期词（上限 10，不足取全部）→ 老师开场报词 → 等「好了/可以了/下一个」确认或 60 秒超时自动推进 → 支持重读/慢读、不会写提示、词义解释、暂停继续口令 → 完结总结写学习记录。大模型仅做意图识别与话术生成，不再拥有课堂节奏裁量权。 — 用户原始能力模型是默写助手；R0 按 13-CONTEXT 以「自由对话+可选工具」验证实时链路，体验与产品定义错位。属规划阶段翻译损耗而非执行偏移——执行与计划一致（证据：13-CONTEXT L16/L17/L20、RT-06 原文、PROJECT.md「FSRS R2 贯通」）。本决策推翻：固定 3 词不选词、沉默不跳词（RT-06 原文）、FSRS 排 R2。伴随缺陷一并修：MiMo invalid API response（实测×4）、VAD 0 秒误触发、首问候缺失（13-03/04/05 实现缝隙）。
- [Phase 13.1]: 13.1-01 三段式tracer落码：完结写评级改为检查段(stage=checking持久化)→逐词manual改判→check_submit一次统一提交FSRS(D-02/D-07)→含对错战果总结过ACK门(D-04)；checkRating与wordRating同族共用rating常量禁分叉 — D-01/D-02/D-04/D-07模型改造(13.1-CONTEXT显式取代13-CONTEXT完结即写条款)；幂等闸=lessons.stage checking→submitted迁移+request_id去重+expected_version乐观锁，二次提交回放零副作用
- [Phase 13.1]: 打码权威在服务端发送侧（spoken+display 双轨）：TTS/LLM 语境/trim 恒原文，字幕与词表按学生模式脱敏，词明文不再上线；前端只画收到的内容
- [Phase 13.1]: maskTranscript 以 grading.Normalize 为唯一比对口径（按词 script 选 en/zh），替换落在原始 span，窗口=归一化词长+8，不写第二套归一化
- [Phase 13.1]: word_revealed 每个首提交 advance 无差别下发（含末词），幂等回放/冲突不发，测试模式完全不发；repeat/slow/resume 用显式打码模板变体避免叠词
- [Phase 13.1]: 13.1-03 拍照检查走 base64 JSON 控制消息分块（32KiB 原始块 base64 后 43.7KB<65551 读上限；音频二进制通道零改动）；装配纪律＝1-based 连续 seq＋seq1 重置＋1MiB 上限拒绝即清＋cancel/close 清扫（Pitfall 5 不复发、privacy P2 图片仅内存不落盘不入库不打日志）
- [Phase 13.1]: 13.1-03 OCR 判定只作预填：ProcessWords 任一项低于置信度阈值即整张照片不预填（读数按位保留仅显示，门的所有权独占在管线，失败方向是多人工确认而非错误预填）；预填经 ApplyVerdict(manual=false) 且 request_id 带 OCR 轮次（重拍的新读数不被幂等回放钉死）；乐观锁冲突时该词已有 manual 判定则丢弃预填（D-06① 人工优先）
- [Phase 13.1]: 13.1-03 统一提交闸维持 13.1-01 逐词 manual=true 口径——零人工动作的 OCR 预填提交在服务端不可能（TestOcrSubmitRequiresManualConfirmation 钉死）；前端待确认高亮＋提交按钮禁用与服务端闸双镜像；判对错只用 GradeWord 的 Correct/NearMiss 布尔，绝不读 j.Rating 旧语义
- [Phase 13.1]: 13.1-04 D-03 兜底触发点＝会话关闭：Close/handleEndSession 在 internalClose 前同步走 uncheckedCloseFallback，internalClose 本身不自动兜底（自然完结课按构造 no-op）；CompleteCheck 首提交分支是唯一写点，重复关闭/迟到 end_session 绝不双写 — T-13.1-09 幂等闸在 DB 层；兜底写失败如实记 error 事件不盲重试（S-05），行保持 checking 由恢复路径接手
- [Phase 13.1]: 13.1-04 修正提交用判定级 diff：人工确认与派生提示逐词比对，只有改判词进第二次 SubmitReview 批次（EventReviewCorrection 带改判数），空 diff 也走 ClearUnchecked——计划文本的 diff 基准按其自身的空 diff 分支与「只有改判词进批次」测试规范裁定为判定级而非评级级 — 评级级 diff 会让最常见的无提示课（检查对+0提示=good vs 兜底 hard）永远非空，与计划明示的空 diff 分支矛盾
- [Phase 13.1]: 13.1-04 ClearUnchecked 携 AND unchecked=1 门＋request_id 去重＋乐观锁：已清行的新请求是幂等 no-op 绝不二次 bump 版本；startCheckResume/startCheckCorrection 按 stage 优先排序挂在 startCompletedSession 短路之前，历史行行为不变 — 跨会话修正路径的幂等与历史兼容（Pitfall 2 恢复路径＋13.1-01 回归）
- [Phase 13.1]: 13.1-05 练习选词与「当日」口径：CardRepository.List 只读直连＋Go 侧随机采样（禁 RunDue——取锁且只回到期词）；「当日已默」排除集＝当日 r0-YYYYMMDD 课行词 ∪ practice_logs 当日记录，practice_logs 用显式 reviewed_day 列（北京日历日，与 r0-YYYYMMDD 同口径——SQLite datetime('now') 是 UTC 不可依赖）
- [Phase 13.1]: 13.1-05 练习提交零 FSRS 污染（T-13.1-11）：同款 D-05 逐词手动闸＋CompleteCheck stage 闸＋checkRating 单一矩阵，唯一写点 PracticeStore.RecordPractice（Go 自有 practice_logs）；SubmitReview 零调用有负向断言（集成＋单元双测试钉死），r0x-YYYYMMDD-HHMMSS 独立命名空间防次日恢复逻辑误捡
- [Phase 13.1]: 13.1-05 计划文本冲突裁定：Task 3 spec 的「练习会话线上无 review_result」与 Task 2 明示的「复用 ReviewResultMsg 报记录数」矛盾——按 Task 2 协议决定执行（review_result requested/updated=记录数、unchecked=false），e2e 断言改为「无 FSRS 写语义」（mock state.reviewResults==0＋恰一条结果消息）；mock 与 protocol 逐字段一致纪律支撑
- [Phase 13.1]: 13.1-05 no-due 天数文案 nextDueDays 解析 fsrs_card JSON 的 due 字段（计划明示的 Card「opaque JSON」唯一豁免；解析失败容错跳过；当天到期不算→文案降级无天数版本）；完结/无到期/练习三种收尾文案三套模板不共用（D-13），前端再练一组按钮点击＝一次 practice 会话（practice_complete 横幅无按钮，环有界）
- [Phase 13.1]: 13.1-06 语音改判与点按同一条 ApplyVerdict 幂等路径（RT-07）：request_id 服务端生成 lessonID-verdict-<index>-v<version>-voice，version 烘进键、每次成功写 bump version——同一词的后续语音改判绝不回放首次写（OCR retake 教训）
- [Phase 13.1]: 13.1-06 check_spelling 的判对错来自 GradeWord 而非 intent 字面：口述写法与词一致→correct、近错→wrong+「差一个字母很接近了」鼓励话术（判对错权威=人的口令＋GradeWord，服务端确定性逻辑，模型无裁量权）；口述写法经 ocr_result 通道显示在行侧，零前端改动
- [Phase 13.1]: 13.1-06 检查阶段语音分类走单独构建的 buildCheckClassifySystemPrompt（全词表＋逐词判定态；提示词 9 值＝3 检查 intent＋6 个仍合法基础 intent，hint 按 chat）；白名单恒 11 值 parseIntentJSON 不变（Pitfall 8/T-13-13）
- [Phase 13.1]: 13.1-07 收口：13.1-03 延期拍照 mock e2e 四族落地（D-05 双端提交闸、D-06① 人工改判、重拍清缓冲、纯人工降级）；全量回归零回归零弱化——go test 15 包＋npm test＋e2e:rt 21/21，pkg/rt 生产码零改动（修复权未触发）
- [Phase 13.1]: L3 真通道驱动扩展到三段式终态：driver checkStageAct（手写体夹具拍照→真 MiMo OCR→逐词 manual→一次统一提交）25/25 hard-pass；生产实例零接触两次运行证明（lessons 2→2、review_logs 25→25、e2e-% 恒 0）
- [Phase 13.1]: 13.1-07 flake 分类：首跑 g_interrupt 失败（old_epoch 3 帧）同代码重跑即 clean——环境性 flake（TTS 流速竞态），如实记录不掩码不放宽；i_vocab 版本断言按三段式写路径改为单调下限（review 集合算术保持精确）
- [Phase 13.1]: 13.1-08 败方诚实形态＝终态信号(session_ended lesson_complete)＋一条 duplicate_submit 诊断，绝不伪造 review_result/保存数量（透明性禁令跨连接面，三家评审共识修法）；守卫先于 checkRatings 组装（latch replay 空回读会提交空批次）
- [Phase 13.1]: 13.1-08 修正路径 TOCTOU 残窗接受不闭合：前置 ClearUnchecked 闩锁破坏 S-05「review 写失败保留标记供下次重试」；升格 13-13 具名交接项（触发签名：review_logs 同词同日、毫秒级 correction 后缀重复行）＋untested race 登记
- [Phase 13.1]: 13.1-08 Rule 1：terminalDraining 页面级标志从无复位——submit.disabled 消费它即成跨课锁死；在会话起点（startBtn click，上一场排空已完成）复位，practice full loop e2e 首跑抓到后修复
- [Phase 13.1]: 13.1-08 check_spelling 复核确认：applyVoiceVerdict 走 grading.GradeWord 且 correct=j.Correct（口述写法全对→correct=true），codex 13.1-06 疑虑不复现（TestCheckVoice 族 PASS，供 13-13 追认）
- [Phase 13.3]: 家庭入口无门禁两扇门（fusion-consensus-2026-09 决策 2：防不方便≠防孩子）；parent.html 三 tab（学习记录默认｜词库｜设置）
- [Phase 13.3]: 诚实保存回执四态（pending/saved/failed＋无行=unknown），加练回执=练习存储成功非 FSRS；WR-01 后回执元数据失败降级 unknown 绝不 5xx 阻塞可读性
- [Phase 13.3]: rt.forceStudentMode 缺省 false＝放行（本机 config.yaml 不加键）；家庭正式入口 config.local.yaml 显式 true（该键 2026-10-11 时点盘上缺失——下次家庭会话前需补，验证器已登记为操作提醒非缺口）
- [Phase 13.3]: 用户方向（2026-10-10）：UAT 真机验收项应尽量端到端自动化（并入四层 E2E 的 L3 主战场）——ROADMAP Backlog 999.2

### Pending Todos

源自 2026-07-25 架构评审（/architecture）：

- [x] **统一连接注册表** — `infra.ConnectionRegistry` vs `ws.Hub` 双轨制，需 ADR 拍单一真相源（评审 P0-2）。`todos/pending/2026-07-25-unify-connection-registry-adr.md` → Resolved: Phase 07-03 D-04 — Hub is single source of truth; ConnectionRegistry soft-deprecated from metrics path; Hub.Stats() feeds ServerSnapshot via SetConnectionStatsProvider
- [x] **解决 WorkerPool 建而不用** — 无任何 Submit 调用，接线 or 登记 deferred（评审 P1-3）。`todos/pending/2026-07-25-resolve-workerpool-unused.md` → Resolved: Phase 07-03 D-06 — session.audit.go calls pool.Submit for Create/UpdateState/AppendEvent (first real caller)
- [x] **Phase 4 前定 FSRS+SQLite 所有权** — Python(vocab_manager) 已实现 FSRS+SQLite，Phase 4/5 重新定义范围，需 ADR（评审 P1-4）。`todos/pending/2026-07-25-fsrs-sqlite-ownership-adr.md` → Resolved: ADR created at `.planning/adr/2026-08-01-fsrs-sqlite-ownership.md`
- [ ] **仓库卫生批处理** — README 漂移 / gitignore 根级二进制 / 残留 .gitkeep 清理（评审 P2-5/P2-6/P3-7）；**2026-09-09 扩容为总纲**：根目录未跟踪调试产物（test-\*.html/js/py、check_\*.py、nul、\_\_pycache\_\_ 等）+ 一次性分析文档（BUG-FIX-\*.md、SESSION-\*.md 等）盘点分类后合并或删除。`todos/pending/2026-07-25-repo-hygiene-readme-gitignore-cleanup.md`
- [ ] **iOS WebSocket 音频播放问题研究** — iPad Safari 上 WebSocket 流式 TTS 播放问题，需调研当前方案能否达到预期效果。`todos/pending/2026-08-08-research-and-fix-ios-websocket-audio-playback-issues-for-ipa.md`

> 注：评审 P0-1（WS 层未接入运行时，服务监听不到 :8080）未建 todo。
> **2026-07-25 更新**：Phase 02 手动收尾已完成（02-02/02-03 SUMMARY 补写、flaky test 修复、ROADMAP 3/3）——但这两个计划交付的是 WS *库*（Hub/Client/Server/Router），**并非** main.go 运行时接线。P0-1（main.go 监听 :8080 + mux.HandleFunc("/ws", server.HandleWebSocket)）仍属 **Phase 1 Plan 08** 范围，当前未交付、未建 todo —— 建议后续为 P0-1 单独建 todo。

### Blockers/Concerns

None yet.

### Quick Tasks Completed

| # | Description | Date | Commit | Status | Directory |
|---|-------------|------|--------|--------|-----------|
| 260801-fh3 | 让 Home Tutor Agent 在 Windows 本机能跑起来调试 | 2026-08-01 | 077e77d | | [260801-fh3-home-tutor-agent-windows](./quick/260801-fh3-home-tutor-agent-windows/) |
| 260801-ge1 | 接入 WebSocket 运行时(P0-1) | 2026-08-01 | bfb24d3 | Verified | [260801-ge1-websocket-p0-1](./quick/260801-ge1-websocket-p0-1/) |
| 260801-kdq | 根治 ws TestHubStopsOnContextCancel flake | 2026-08-01 | b65ad5e | | [260801-kdq-ws-testhubstopsoncontextcancel-flake](./quick/260801-kdq-ws-testhubstopsoncontextcancel-flake/) |
| 260802-ldq | 创建最小化前端验证界面，用于测试WebSocket连接、FSRS单词选择和TTS音频流播放 | 2026-08-02 | ad599a5 | Verified | [260802-ldq-websocket-fsrs-tts](./quick/260802-ldq-websocket-fsrs-tts/) |
| 260930-t8z | 重定基规划文档：产品验收矫正（R0-R3主线，ADR-0002转正） | 2026-09-30 | a7fcfb4 | | [260930-t8z-r0-r3](./quick/260930-t8z-r0-r3/) |
| 260823-cff | Fix category field in session_ready response for correct speech recognition language | 2026-08-23 | e8556fa | complete | [20260823-category-field-fix](./quick/20260823-category-field-fix/) |
| 260823-fvx | 重构小米ASR实现以符合官方API规范 | 2026-08-23 | 9a8a7b3,3edb20a,cb72cab | complete | [260823-fvx-asr](./quick/260823-fvx-asr/) |
| 260823-j2n | 收尾 speech-recognition-redesign Ticket 01 状态文档（issue 勾选/总结更新/STATE 补 commit） | 2026-08-23 | 371010c,eb58b7e | complete | [260823-j2n-speech-recognition-redesign-ticket-01-is](./quick/260823-j2n-speech-recognition-redesign-ticket-01-is/) |
| 260823-sdx | 修复 session.go 过期 WordComplete 事件竞态：stream generation 计数器 | 2026-08-23 | 3a1fc59 | complete | [260823-sdx-session-go-wordcomplete-stream-generatio](./quick/260823-sdx-session-go-wordcomplete-stream-generatio/) |
| 260823-t13 | 提取 PAUSED 5 分钟超时到 Config.SessionTimeout.PausedStateTimeout | 2026-08-23 | 799de7c | complete | [260823-t13-paused-5-config-sessiontimeout-pausedsta](./quick/260823-t13-paused-5-config-sessiontimeout-pausedsta/) |
| 260823-ti8 | 文档化 onTransition 无锁读 repeatPending/prevState 的单写者不变量 | 2026-08-23 | f8e5e31 | complete | [260823-ti8-ontransition-repeatpending-prevstate](./quick/260823-ti8-ontransition-repeatpending-prevstate/) |
| 260823-tyq | 补全 DictationControls 六个方法的 JSDoc 文档注释 | 2026-08-23 | d6c046b | complete | [260823-tyq-dictationcontrols-jsdoc](./quick/260823-tyq-dictationcontrols-jsdoc/) |
| 260823-u8t | ticket 02 评审小清理集四子项（60px 触控高度/测试错误检查/_applyPaused 防护/godoc TODO） | 2026-08-23 | 45435bb | complete | [260823-u8t-ticket-02](./quick/260823-u8t-ticket-02/) |
| 260823-urr | 收尾 ticket 02 状态文档（issue 置 done 6/7 勾选 + Completion note；STATE 转向 Phase 8.2/iPad UAT） | 2026-08-23 | 3d2cb6f | complete | [260823-urr-ticket-02](./quick/260823-urr-ticket-02/) |
| 260823-vn9 | ticket 02 评审终局清理集（update() JSDoc 死分支如实描述/_applyPaused JSDoc 收紧为类型校验/StaleWordCompleteDropped 测试去时序依赖/ROADMAP 8.1 对齐实况） | 2026-08-23 | 4f5c644,0ff7cca | complete | [260823-vn9-ticket-02-code-review-1-ui-components-js](./quick/260823-vn9-ticket-02-code-review-1-ui-components-js/) |
| 260823-x38 | 补齐 ticket 02 生命周期文档提交清单（issue 02 Status/Completion note 补终审结论、STATE stopped_at×2、ROADMAP 8.1 ticket 行——均补 4f5c644/0ff7cca） | 2026-08-23 | a1ae3f6,f7b92fd,d3b5fa2 | complete | [260823-x38-ticket-02-4f5c644-0ff7cca-roadmap-8-1-e0](./quick/260823-x38-ticket-02-4f5c644-0ff7cca-roadmap-8-1-e0/) |
| 260909-rih | 验证并提交工作区清理改动：Rating 字段下线、DictationFinished 收敛、测试辅助重构 | 2026-09-09 | 2fa555f | complete | [260909-rih-rating-dictationfinished](./quick/260909-rih-rating-dictationfinished/) |
| 260912-eam | 归账清理：9 文件工作区改动落库（gofmt/e2e helper/gitignore）；ROADMAP 8.2–8.6 标记 tickets 03–07 直达交付；08-VERIFICATION 过时 FAIL 注记 | 2026-09-12 | 9e3057c,8411fa8,82e8aa8 | complete | [260912-eam-gofmt-roadmap-8-2-8-6-ticket-state-md-08](./quick/260912-eam-gofmt-roadmap-8-2-8-6-ticket-state-md-08/) |
| 260928-tsp | 测试提速：live 测试加 -short 跳过 + mimo 重试延迟压缩 + run.ps1 增量编译 + mimo RealAPI skip & run.ps1 hint restore | 2026-09-28 | 33f8c52,87534a4 | complete |  |
| 20 | 新建 13-UAT-ISSUES-20261006 本机验收问题台账（U-1 E2E 残留三处已清/防复发待办、U-2 去重版本倒退缺陷立账未修、U-3 确认误判观察） | 2026-10-06 | 4b39357 | — | — |
| 261006-u2o | U-9：评级映射实现＋逐词提示计数（裁定口径在 CHECKPOINTS；将来降级为 13.1 的未核对兜底路径） | 2026-10-06 | f948b17 | complete | [261006-u2o-u-9-checkpoints-13-1](./quick/261006-u2o-u-9-checkpoints-13-1/) |
| 261006-tnr | reset-vocab.py 补当天课堂行删除：cards FSRS 重置 + 抓取锁清理之外，按上海时区算 r0-YYYYMMDD，先删 tool_requests 再删 lessons 当天行，重连 /rt 走新课堂重新取词（对齐 pkg/rt/dictation.go resolveLessonID） | 2026-10-06 | ed94a38 | — | [261006-tnr-add-today-lesson-row-deletion-to-root-re](./quick/261006-tnr-add-today-lesson-row-deletion-to-root-re/) |
| 261006-u7o | 整理项目根目录：盘点一级文件与 .scratch/，可删的删、该提交的提交、建文件夹分类归档 | 2026-10-06 | 9ac1996 | — | [261006-u7o-scratch](./quick/261006-u7o-scratch/) |
| 261007-0ct | U-2：去重版本倒退防护——重置课行的过期去重记录判定失效＋同事务清除重执行推进（pkg/lesson/executor.go） | 2026-10-07 | 7fdfac7 | complete | [261007-0ct-u-2-pkg-lesson-executor-go](./quick/261007-0ct-u-2-pkg-lesson-executor-go/) |
| 261007-22t | U-1：E2E 隔离规范资产化——四层设计 L3 落地（tests/e2e-rt/real 五件套＋npm run e2e:rt-real＋docs/E2E-ISOLATION-SPEC.md，台账防复发闭环） | 2026-10-07 | 9b11c8f,09dc38d,c1d3c94 | complete | [261007-22t-e2e-memory-u-1](./quick/261007-22t-e2e-memory-u-1/) |
| 261007-coe | 根目录梳理第二轮：test-interface.html 与 reset-vocab.py 归位、工具产物清理、卫生 todo 收尾 | 2026-10-07 | 60c129a | complete | [261007-coe-test-interface-html-reset-vocab-py-codex](./quick/261007-coe-test-interface-html-reset-vocab-py-codex/) |
| 261007-d9t | run.ps1 日志归位 logs/：四日志落点迁移+目录自建+注释同步，路径级引用 playwright/e2e 驱动同提交对齐；实机两模式起停验证，冻结证据件零字节变化 | 2026-10-07 | d09ce65 | complete | [261007-d9t-run-ps1-logs-l61-l117-logs-l23-logs-giti](./quick/261007-d9t-run-ps1-logs-l61-l117-logs-l23-logs-giti/) |
| 261007-drn | playwright 配置归位 tests/：git mv 双配置（rename 84%）+testDir 相对化+webServer.cwd 指回仓库根+调用方 -c 同步；干跑双门 7/4 与 4/1 全绿、加载期 cwd 断言通过，冻结证据件零字节变化 | 2026-10-07 | 4df02dc | complete | [261007-drn-playwright-tests-playwright-config-js-pl](./quick/261007-drn-playwright-tests-playwright-config-js-pl/) |
| 261007-e95 | 路径漂移修正收尾：plan13 双 JSON 六处 -c tests/ 化（全仓旧形态清零）、serve-rootca.ps1 日志归位 logs/；settings.local.json 权限规则替换被权限系统移出范围，留用户手动 | 2026-10-07 | 34932d3 | complete | [261007-e95-plan13-json-6-playwright-c-tests-serve-r](./quick/261007-e95-plan13-json-6-playwright-c-tests-serve-r/) |
| 261011-b8u | 修复 13.3 UI 审计三项优先建议：chip 对比度（#047857/#b45309，实测 5.21:1/4.84:1）、tutor 回首页统一 #2563eb、tab 触控 60px | 2026-10-11 | d6870fe | complete | [261011-b8u-13-3-ui-chip-tab](./quick/261011-b8u-13-3-ui-chip-tab/) |

### Roadmap Evolution

- Phase 07.1 inserted after Phase 7: Research and fix iOS WebSocket audio playback issues for iPad Safari browsers (URGENT)
- Phase 8.1 edited: Re-scoped to ticket 02 (student-paced Mimo TTS dictation); auto-advance plans superseded by user decision 2026-08-23
- Phase 8.6 inserted after Phase 8: Phases 8.2-8.6 inserted from speech-recognition-redesign tickets 03-07 (voice commands, photo OCR, batch submission, auto grading, multi-user); ticket 08 absorbed into Phase 12; FEED re-sourced in Phase 10. Ticket set = single source of truth (user decision 2026-08-23)
- Phase 13.1 inserted after Phase 13: 三段式默写课堂（默写→检查→统一反馈）：R0 验收问题 U-5/U-6/U-7/U-10 讨论收敛；三段式先行于 13-13 部署（用户 2026-10-06）
- Phase 13.2 inserted after Phase 13: 2026-10-09 用户要求：VPS 从 Phase 13 拆出；先完成 Phase 13/13.1 本地人工验收。13.2 未规划、未部署，原 13-13 计划仅保留参考。
- Phase 13 edited: R0 范围调整为本地开发机真实语音验收，实施 12/12；部署与公网/iPad 正式验收迁至 13.2，本地验收未通过不得开始部署。
- Phase 13.3 inserted after Phase 13.2: 家长端融合：统一首页+学习成果可读，无门禁（D-04 修订）；纯本地与 VPS 零耦合，13.2 同日搁置（家庭试用期本地即真机） (URGENT)

## Deferred Items

旧里程碑以 superseded / override_closeout 关闭：审计有 17 项开放登记及未验收阶段，详见 [关闭记录](milestones/v1.0-CLOSEOUT.md) 和 [原始审计](milestones/v1.0-ARTIFACT-AUDIT.json)。它们未被判为通过。

| Category | Item | Status | 接收范围 |
|---|---|---|---|
| human_uat | Phase 9 长文本/省略号/预览视觉确认 | 未执行 | Phase 16 人工验收 |
| human_uat | Phase 9 真 iPad 家长页视觉/触控/缩放 | 未执行 | Phase 16 真机验收 |
| roadmap | 旧 Phase 10～12 | superseded | Phase 13～16 / RT-07～RT-10 及全量复验 |
| historical_records | 历史调试/快速任务/待办/验证登记 | 原状态保留，不阻塞 R0 | 相关问题仅在影响新体验时处理 |

## Session Continuity

Last session: 2026-10-11
Stopped at: Phase 13.3 complete（真机 UAT 10/10、VERIFICATION 7/7、SECURITY 0 open、UI 审计 20/24、VALIDATION 0 gaps）；下一步 Phase 13/13.1 本地验收收口
Resume file: None
