---
status: incomplete
quick_id: 261015-ecc
slug: ci-golangci-lint-v2-14-0-ci-yml-golangci
date: 2026-10-15
---

# Quick Task 261015-ecc — 修复 CI：golangci-lint v2.14.0（让位收尾，未执行）

## 结论

**规划完成、执行让位。** 规划器独立推导的方案（golangci-lint-action@v9 + `version: v2.14.0` + `.golangci.yml` migrate 到 v2 + 清 531 条积压告警，约 95 文件 / 预估 12 万 token）与并行 cursor 会话已开的 PR #23（工具链修复）/ PR #24（清告警，叠在 #23 上）完全重复，连做法都一致。用户 2026-10-15 拍板：让位给 PR #23/#24，避免两套重复实现。

## 产物

- 计划：`261015-ecc-PLAN.md`（commit 5208295，3 任务，未执行）

## 规划期实证结论（留给 PR #23/#24 审查时参考）

- v2.14.0 官方二进制 built with go1.27.0 ≥ go.mod 的 `go 1.26`，原始失败类别（构建用 Go < 目标 Go）在 CI 与本地均消除
- action v7+ 仅支持 golangci-lint v2，v8+ 要求 ≥ v2.1.0；`version:` 输入接受 v 前缀 patch 钉法（`v2.14.0`）
- 用真实 v2.14.0 二进制 + 迁移后配置对主树实测：**531 条积压**（govet 339 = fieldalignment 239 + shadow ~45 + unusedwrite 7；gosec 63 = G115 38、G306 7、G404 6、G118 4；gofmt 44 + goimports 65 可 `fmt` 自动修；errcheck 27、staticcheck 15、unused 12、misspell 9、ineffassign 1）——属 lint 失明窗口积债，非 v2 姿态扩张；不应靠关 analyzer / 封顶 issue 压下
- `golangci-lint migrate` 会产出 `.golangci.bck.yml` 备份文件，未被 gitignore，需手动删除
- 本地无 `make`（不在 PATH）；验证命令用等价裸 go 命令；worktree 无 .env 时活体测试自跳过，与 CI 行为一致

## 后续

- PR #24 叠在 #23 上：#23 合入后需将 #24 的 base 改回 main 才会触发 CI
- fieldalignment 结构体重排（239 条）重点抽查二进制/序列化布局影响（SQLite 行、JSON 编解码、unsafe）
- 本会话转入：核查 PR #23/#24 → 推进合入 → PR #25 重跑 CI
