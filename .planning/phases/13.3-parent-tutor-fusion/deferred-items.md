# Phase 13.3 Deferred Items

跨计划登记本阶段执行中发现但**不在当前计划范围内**的事项（scope boundary 规则：发现者只登记不修复）。

| Date | Found during | Item | Status | Owner / suggests |
|------|-------------|------|--------|------------------|
| 2026-10-10 | 13.3-05 计划级验证 | `cmd/service` 全量 `go test -count=1` 中三个 live MiMo API 用例失败（TestPhotoOCR_LiveRealVision / TestVoiceCommand_LiveRulePath / TestVoiceCommand_LiveLLMPath——真 ASR 返回空转写、真 OCR 空回复）。`-short` 跳过守卫下全绿（2.7s）。与 13.3-05 改动（bool 配置键＋裁决短路，默认 false 零行为差）无交互面，属真实外部 API 的环境性波动（260928-tsp 已为其建立 -short 跳过惯例）。 | open | Wave merge 门按标准命令跑全量时如实记录；真机 UAT（13.3-06）本就要真 API 全链路验证。**2026-10-10 13.3-06 跟进**：四件套照标准命令跑——live 用例因进程环境带 MIMO_API_KEY 真实执行（worktree 无 .env，env-wins），套件内全 PASS；单独复跑 photo live 出现一次空回复 FAIL，同代码重跑 PASS——「环境性 flake（重跑 clean）」已登记于 VALIDATION 回归记录；本项保持 open（外部 API 波动无法本地关闭，真机 UAT 继续兜底） |
