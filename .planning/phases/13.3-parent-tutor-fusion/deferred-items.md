# Phase 13.3 Deferred Items

跨计划登记本阶段执行中发现但**不在当前计划范围内**的事项（scope boundary 规则：发现者只登记不修复）。

| Date | Found during | Item | Status | Owner / suggests |
|------|-------------|------|--------|------------------|
| 2026-10-10 | 13.3-05 计划级验证 | `cmd/service` 全量 `go test -count=1` 中三个 live MiMo API 用例失败（TestPhotoOCR_LiveRealVision / TestVoiceCommand_LiveRulePath / TestVoiceCommand_LiveLLMPath——真 ASR 返回空转写、真 OCR 空回复）。`-short` 跳过守卫下全绿（2.7s）。与 13.3-05 改动（bool 配置键＋裁决短路，默认 false 零行为差）无交互面，属真实外部 API 的环境性波动（260928-tsp 已为其建立 -short 跳过惯例）。 | open | Wave merge 门按标准命令跑全量时如实记录；真机 UAT（13.3-06）本就要真 API 全链路验证。**2026-10-10 13.3-06 跟进**：四件套照标准命令跑——live 用例因进程环境带 MIMO_API_KEY 真实执行（worktree 无 .env，env-wins），套件内全 PASS；单独复跑 photo live 出现一次空回复 FAIL，同代码重跑 PASS——「环境性 flake（重跑 clean）」已登记于 VALIDATION 回归记录；本项保持 open（外部 API 波动无法本地关闭，真机 UAT 继续兜底） |
| 2026-10-10 | 13.3-06 真机 UAT（/gsd-verify-work 会话） | **用户方向性意见：13.3-UAT 十项真机验收用例应做成端到端自动化验证**（家庭旅程主链、读零写核查、泄题无词面、未核对兜底、加练隔离、待核实崩溃窗口、强制模式、回首页时机、bfcache/触控、负面清单）。可自动化主体应并入四层 E2E 体系（docs/E2E-ISOLATION-SPEC.md）——L3「隔离实例＋合成客户端」是主战场（13.3-03 mock e2e 已覆盖部分形态，可沿 tests/e2e-rt/real/ 范式资产化）；iPad Safari bfcache/触控命中/真音频体感等物理项保留 L4 人工。 | open | 13.3 UAT 收尾时随 Deferred Follow-Ups 提升到 ROADMAP backlog（/gsd-verify-work 完成时询问）；宜与 13-13 前的 E2E 资产化工作合并规划 |
