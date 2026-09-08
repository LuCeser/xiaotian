# 04 — Photo OCR Answer Submission System

**What to build:** Students can submit written answers by taking photos of their work, with automatic OCR text extraction and manual input fallback. This preserves the traditional handwriting practice while providing modern automation.

**Blocked by:** 01 — Xiaomi Mimo Service Integration Layer

**Status:** done (2026-09-08, commits: 1ceba56, a7b26f7, d588386, 47a668b, 4536cff, dec6cab)

- [x] Students can capture photos of written answers via iPad camera — 取舍备注：`<input type=file capture=environment>`（iPad Safari 标准相机路径，无需 getUserMedia、无前置权限弹窗、http 局域网可测——与语音端 secure-context 限制相反）；canvas 降采样阶梯（1600px@q0.8→1024px@q0.5，≤768KiB）后 32KiB IMAGE 帧（MsgType 0x07）分块上行 + photo_submit 触发。真实 iPad 相机采集待 UAT
- [x] OCR service extracts text from images with ≥85% accuracy for typical elementary handwriting — 取舍备注：Mimo 官方 image-understanding 路径（mimo-v2.5 视觉，base64 data URI，pkg/mimo ChatVision）；活体验证（TestPhotoOCR_LiveRealVision，gated on MIMO_API_KEY）：英文 apple 0.99 / 中文 漂亮 0.95，经真实 router+socket+分块重组。合成字迹样本 ≠ 真实 6-12 岁手写，真实准确率留 UAT（验收含 ≥85% 量化项，需真机真迹统计）
- [x] Manual input option available when OCR confidence is low or fails — 置信度 < photo.confidenceThreshold（默认 0.6）→ photo_result.low_confidence=true + 最佳猜测文本，前端 ManualAnswerForm 预填待改；识别失败 → 同表单空值 + 原因提示；手动提交走既有 submit_answer 线路（answer_result 反馈复用）
- [x] System validates submitted answer format and completeness — 服务端 JPEG SOI 魔数校验（非 JPEG 拒绝、零调用 API）、空图/空文本 fail-fast、photo.maxImageBytes=1MiB 超限整包废弃并回错误帧；答案判定复用 compareAnswer 归一化（中文去标点、英文去非字母数字——活体发现的字母间距 "a p p l e" 也被吸收）
- [x] Error handling for camera permissions and image processing failures — 采集/编码五类错误（not-supported/not-an-image/too-large/read-failed/encode-failed）本地提示 + 手动输入兜底；无 MIMO_API_KEY 时照片禁用（photo_submit 错误帧、IMAGE 帧静默丢弃），语音/按钮/手动输入不受影响；photo_cancel ack 防取消照片污染下一张（同 voice_cancel 回归测试钉死）
- [x] Photo capture interface optimized for iPad Safari browser — 一键拍照即提交（低置信自动落手动输入，避免儿童多步操作）；60px 触控目标对齐 iOS HIG；16px 输入框字号防 focus 缩放；拍照点击先停语音识别（避免迟到语音结果判到下一词）
- [x] OCR results include confidence scores for validation — 模型按严格 JSON 契约自报 confidence（0-1，clamp），photo_result 帧透传；活体证实非装饰值（0.5 恰为解析兜底默认→倒逼出 decoder-scan 修复）

## Completion note

- 验证方式：`go test ./pkg/ocr/ ./pkg/mimo/ ./pkg/ws/ ./cmd/service/ -count=1` + `npm test`（photo 分块/序号/上限/取消契约）
- 2026-09-08 实测：全绿；活体 TestPhotoOCR_LiveRealVision 两用例通过（真实 ChatVision 视觉调用，单次 ~3-12s，20s requestTimeout 内）
- 架构备注：pkg/ocr 零 mimo 依赖（Reader func seam，cmd/service 适配 ChatVision）；分块重组复用 voice.Buffers（第二实例、独立 1MiB 上限）；settleAssembled 从 settlePCM 提取共用（图/控路由分离的触发先到竞态）；judgeAnswer 从 handleSubmitAnswer 提取（语音/照片共用会话守卫+比对）；共享 mimo client HTTP 超时取 max(voice 15s, photo 20s)
- 活体抓到并修复的真缺陷：①mimoReader 的 MaxTokens=256 截断思考模型（只出 reasoning 无最终 JSON）→去掉上限；②提示词只说"手写"→模型纠结工整字迹不算手写几乎回空 →"手写或印刷都算"；③parseOCRJSON 增加 decoder-scan 层（思考文本中埋 JSON/尾随垃圾大括号也能带自身 confidence 解出）
- 遗留 UAT：真实 iPad Safari 相机采集与触控交互、真实儿童手写 ≥85% 准确率统计、低置信→手动修改的完整人工流
