# 06 — Automatic Grading and Detailed Analysis Reports

**What to build:** System automatically grades student answers and provides detailed analysis reports with error patterns, learning suggestions, and performance trends. This delivers immediate, objective feedback without parent bottleneck.

**Blocked by:** 05 — Batch Answer Submission Workflow

**Status:** implemented (2026-09-10, commits 4f35ede, 1d63a17, bae4c03, b87bfe2, a8c75c6, c7bce18, 6ce1fb6; review pending)

- [x] System performs character-level comparison between submitted and correct answers — pkg/grading.GradeWord：归一化（en/zh 两套，自 handlers.go 迁入）后 Damerau-Levenshtein（含相邻换位）对齐，输出完整 diff（match/sub/del/ins/swap 逐字符操作）+ 距离；blank 不比对
- [x] Grading algorithm handles minor spelling variations and common mistakes — 归一化吃掉大小写/标点/空格；near-miss（距离≤1 且词长≥2：单错字/漏字/多字/换位）仍判错不涨分，但 FSRS 评 "hard" 而非 "again"，报告标「只差一点」；单字词不适用（错=全错）
- [x] Detailed analysis reports show error patterns and learning suggestions — analysis_result 帧：逐错词卡（完整 diff ops→字符高亮 + 中文提示语「X写成了Y/漏写/多写/顺序颠倒/没作答」）、会话级规律（≥2 条证据才报：没作答/只差一点/漏词尾/错 N 个）、练习建议、重复错词（历史 miss 计数）
- [x] Final scores and encouraging feedback read aloud via TTS — Session.SpeakFeedback：GenerateText（bare 模式、chinese 声音）→ StreamWord，独立 goroutine + runCtx 生命周期 + 30s 上限，尽力而为不阻塞批改；批量路径（submit_answers 后）与逐词路径（submit_review，good/easy=对）都读分档鼓励语；客户端 FEEDBACK 态 playLiveFrame 边收边播（无 word_complete 触发）
- [x] Results stored for FSRS algorithm updates and parent review — FSRS：批量路径服务端一次 UpdateRatings（near-miss→hard）；存储：batch_graded session_event（category/counts/逐词 word+heard+correct+near_miss；diff 不存——可由 word+heard 纯函数重导出）
- [x] Analysis includes character-level error highlighting and suggestions — FeedbackReport 组件：正确写法/你写的两行逐字符高亮（ok/dim/bad/miss/extra 五态，颜色+粗细+下划线非仅颜色），buildCharSpans 纯函数 Node 测试钉死契约；批量表单 🟡 near-miss 档
- [x] Performance trends tracked over multiple sessions — ListRecentResults（sessions⋈batch_graded，最新在前）→ applyHistory：趋势列表（历史旧→新 + 本次收尾，带 ↑↓—）+ 重复错词；历史是增强不是门槛（查询失败/坏 payload 跳过，报告照发）

## Completion note

- 验证方式：`go test ./... -count=1`（全 12 包 ok，含真实 Manager+FSM+socket 的 TestSubmitAnswers_AnalysisResult/PersistsGradedResult/TrendsFromHistory/TestSubmitReview_SpeaksFeedback）+ `npm test`（5 个套件，新增 test-feedback-report.js）
- 顺带修复：SessionCache wordID 由白名单校验改为「标识符原名 / 其他文本哈希 w_hex」文件名映射——旧白名单会把 CJK 词全部拒之门外（zh 会话在 TTS 生成即失败，属潜伏 bug），中文反馈语句同理；sessionID 仍严格白名单，穿越攻击面不变（更强：任意 wordID 均被中性化）
- 架构备注：pkg/grading 为纯函数判分权威（compareAnswer/normalizeForCompare 已删除），handler 层只做编排；answer_result（语音/拍照单词路径）也带上 near_miss+diff；analysis_result 在 batch_result 之后、session_complete 之前发出；持久化与会话事件走既有 sessions/session_events 表（无新表）；趋势上限 10（常量，不设配置项）
- 范围取舍：逐词模式的历史不持久化（该路径 FSRS 即时调度、家长现场看）；trends 为文字列表非图表（儿童 UI 从简）；SUBMISSION 瞬态不变
- 遗留 UAT：真实 iPad 全流程（含语音鼓励外放、报告滚动）；真实中文会话的 TTS 反馈音色（chinese 声音档）；zh 词表 TTS 路径此前未被真实执行过——本次修复后需真机确认
