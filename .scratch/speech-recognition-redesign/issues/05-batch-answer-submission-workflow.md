# 05 — Batch Answer Submission Workflow

**What to build:** Students can write all words first and submit together at the end, maintaining traditional dictation rhythm with comprehensive automatic grading. This implements the authentic classroom practice where students write everything, then check answers together.

**Blocked by:** 04 — Photo OCR Answer Submission System

**Status:** done (2026-09-09, commits: ae4c1d6, 87629b4, 33bb23c, 028b2c8; review 修复 5848db4)

- [x] Student can submit all answers together after completing dictation — submit_answers 控制（word→text 映射）；入口两个：WAITING_PARENT（全部词读完、逐词模式零作答的自然落点）与最后一个词的 DICTATING；批量模式开关（首页复选框，流中读取）打开时播放结束不再自动起语音识别，靠服务端逐词 DICTATING 超时推进队列
- [x] System processes batch submission with multiple words efficiently — 一次 ws 控制帧往返 + N 次纯内存 compareAnswer（按队列顺序）+ 一次批量 FSRS UpdateRatings（服务端直接提交，省掉客户端 submit_review 往返）；回复先于 FSRS 调用发出，Python bridge 慢也不拖反馈
- [x] Traditional dictation flow preserved: write all → submit all → grade all — 全写→批量表单（只显示题号不显示词，防止看答案）→ 一次提交→逐词批改（✓/✗+你的答案）+ 正确数汇总
- [x] Session state handles DICTATING → SUBMISSION → FEEDBACK transitions — StateSubmission + EventBatchGraded；DICTATING|WAITING_PARENT + submit_answers → SUBMISSION →（batch_result 发出后）batch_graded → FEEDBACK（session_complete 既有路径）；超时沿用 waiting-parent 预算（瞬态过路态）；wordIndex 冻结（批改读全队列）
- [x] Progress indicators show completion status during submission — progress_update 帧新增 status 字段并在 WAITING_PARENT/SUBMISSION 入态发送（顺带激活了此前从未收到信号而失效的逐词模式 WAITING_PARENT 自动评分提交死路径）；批改进度=表单内逐行 ✓/✗ + "批改完成：N / M 正确"汇总 + 提交按钮态流转（提交全部→批改中…→批改完成）
- [x] System validates all answers before processing — 服务端：空 map / 词不在本会话队列 / 空白答案 → 首个失败整体拒绝（错误帧），会话保持 WAITING_PARENT 可改后重交；客户端 buildAnswers 先行校验（trim、全空不发）；取舍：允许部分提交——未答题按"空白=错"课堂语义判 incorrect/again
- [x] Submission interface supports both OCR and manual input methods — 每行手填输入框 + 📷 photo_ocr（Ticket 04 的 OCR 核心抽出共用 ocrPhoto，跳过判分/会话守卫，回 photo_ocr_result 填入该行；低置信也填入并提示核对）

## Completion note

- 验证方式：`go test ./pkg/session/ ./cmd/service/ -count=1`（含真实 Manager+FSM+socket 的 TestSubmitAnswers_* 四用例）+ `npm test`（buildAnswers 纯函数契约）
- 2026-09-09 实测：全绿；批量测试脚手架教训——轮询状态必须钉"状态+当前词"（仅状态无法区分词 N 与词 N+1 的 DICTATING，next_word 会打进出 LISTENING 被 FSM 丢弃）；中文判分需 start_session 带 category:chinese（en 归一化会把 CJK 清成空串）
- 架构备注：FSM 仍是唯一状态权威（handler 的状态守卫只是友好报错）；判分在 handler 层（compareAnswer 复用），FSRS 评分服务端一次批量提交（失败记日志继续——批改结果已发，不能让调度抖动把学生晾在 SUBMISSION）；两个事件推送顺序稳定（单事件通道）；photo_ocr 与 photo_submit 共享 settle→OCR 核心，仅回复形状不同
- 范围取舍：混合模式（部分逐词作答+批量补交）明确不支持——逐词路径在 WAITING_PARENT 有评分时会走既有 parent-review 完结，批量表单只在零逐词评分时打开
- 双轴 code review（2026-09-09，Standards + Spec 并行子代理，6da351a...HEAD）：Spec 4 项 finding——2 修（DICTATING→SUBMISSION 补末词守卫：线上提前提交会把未听写的词全判错，FSM 拒绝+handler 友好报错+测试钉死；批量模式补门禁：逐词📷按钮隐藏，语音"命令"保留因为是流程控制非答案）、2 记录（SUBMISSION 状态前端注册但无消费者=判分同步瞬态过路，表单本地指示器已满足验收；服务端 FSRS 写入=判分的合理落点）；Standards 0 硬伤 6 判断项——3 修（langForCategory 提取去重、两处注释归位/如实化）、3 清理（camBtn 60px HIG、开关样式入 main.css、表单小噪音）；另有 app.js 注释失实修正（实际是客户端 word_complete 自动推进，非服务端超时兜底）。修复见 5848db4，两包+npm 复测全绿
- 遗留 UAT：真实 iPad 上批量模式全流程（开关→整场听写→表单→逐行拍照识别→提交→批改展示）；photo_ocr 在批量表单的真实相机采集
