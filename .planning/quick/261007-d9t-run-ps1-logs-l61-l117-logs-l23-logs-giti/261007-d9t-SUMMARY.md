---
phase: 261007-d9t
plan: 01
status: complete
subsystem: repo-hygiene
tags: [logs, run-ps1, path-migration, playwright, e2e-drivers, evidence-freeze]
requires: []
provides:
  - "run.ps1 双模式四日志（server.log / server.out.log / server-8443.err.log / server-8443.out.log）统一落 logs/，目录不存在自动创建；仓库根从此不再新增 .log"
  - "仓库根冻结证据件与活日志物理隔离——run.ps1 L118 启动截断随落点迁离根路径，根上 server-8443.err.log（12099 字节）不可能再被启动流程触碰（T-d9t-01 关闭）"
  - ".scratch/run-local-8443.ps1 不再以 Start-Process 重定向覆写根上冻结证据件（既有隐患顺带消除）"
  - ".scratch/e2e-rt-1312*.mjs SERVER_LOG 尾读活日志 logs/server-8443.err.log——未来重跑不再静默尾读冻结件产出错误时序结论（T-d9t-04 关闭）"
affects: []
key-files:
  created:
    - logs/（运行时目录，不入库；由 run.ps1 / run-local-8443.ps1 按需 New-Item -Force 创建）
  modified:
    - run.ps1（$logDir 单点定义 + 目录自建；L116/L117 落点迁移；L23 注释与 L127/L142 提示语同步）
    - playwright.config.js（L7/L35 排查指引注释改指 logs/server.log，纯注释零代码改动）
    - .scratch/run-local-8443.ps1（$logs 目录自建 + 两处 -RedirectStandard* 迁 logs/ + L40 提示语）
    - .scratch/e2e-rt-1312.mjs（L44 SERVER_LOG → path.join(ROOT, 'logs', ...)）
    - .scratch/e2e-rt-1312-short.mjs（L35 SERVER_LOG → path.join(ROOT, 'logs', ...)）
decisions:
  - "落点集中为单变量 $logDir（run.ps1）/ $logs（run-local-8443.ps1）——单点错即全错、单点对即全对，截断与重定向天然随落点迁移，根路径从代码里消失"
  - "stop.ps1 与 .gitignore 零改动：stop 只按端口/进程路径清理（实机两轮验证引用对完好）；logs/*.log 四名全命中既有任意深度 *.log 规则，不新增规则、不加 .gitkeep"
  - "计划 verify 的 grep -c 断言按行计数（2 行）而实现按计划 action 就是 3 处 occurrence（L117 单行双分支），属验证命令自身缺陷——按 Rule 1 修正为 grep -o | wc -l 后全绿，实现与计划 action 逐字一致"
metrics:
  duration: ~5m（01:45–01:50 UTC）
  completed: 2026-10-07
  tasks: 2
  commits: 1
actuals:
  tokens: 1543（diff 6171 chars ÷ 4，1 提交 5 文件 +16/-12）
  tasks: 2
  commits: 1
commits:
  - "d09ce65 chore(261007-d9t): run.ps1 日志归位 logs/（落点迁移+目录自建+注释同步），路径级引用 playwright/e2e 驱动同提交对齐"
plan_head_before: 49ebeaa
plan_head_after: d09ce65
---

# Quick Task 261007-d9t Summary

run.ps1 四日志落点从仓库根迁入 logs/（目录自建），单原子提交同步全部 4 个路径级引用（playwright 注释、8443 独立启动器、两个 E2E 驱动）；实机 HTTP/HTTPS 各完成一轮 启动→就绪→停止，日志全部落 logs/、仓库根零新增 .log，冻结证据件 server-8443.err.log（12099 字节）与 rt-timing-20261006-202012.jsonl（7538 字节）前后逐字节一致。

## What Was Done

### Task 1 — 落点迁移 + 路径级引用同步（静态验证）
- **run.ps1**（+7/-5）：第 3 节注释后插入 `$logDir = Join-Path $root 'logs'` + `New-Item -ItemType Directory -Force`；L116 `$logErr` 与 L117 `$logOut` 双分支改 `Join-Path $logDir`；L23 头注释加 `logs/` 前缀；L127/L142 提示语改 `logs\$logName`。logName 文件名、L118-119 截断行为（随落点迁移）、Start-Process 参数、就绪轮询均未动。
- **playwright.config.js**（2/2）：仅 L7/L35 注释 server.log → logs/server.log。
- **.scratch/run-local-8443.ps1**（+5/-3）：Start-Process 前 `$logs` 目录自建，两处 `-RedirectStandard*` 改 `Join-Path $logs`，L40 提示语同步——根上冻结证据件从此不会被该脚本覆写。
- **.scratch/e2e-rt-1312.mjs / -short.mjs**（各 1/1）：SERVER_LOG → `path.join(ROOT, 'logs', 'server-8443.err.log')`。
- 不动清单零触碰：stop.ps1、.gitignore、README.md、docs/DEPLOYMENT.md、scripts/rt-timing-report.js、.scratch/serve-rootca.ps1、.planning 历史记录与冻结产物。
- 静态门全绿：两 .ps1 PowerShell Parser 零错误；grep 断言（$logDir 创建、Join-Path $logDir ×3、旧 `Join-Path $root` 直连 server* 日志清零）；git check-ignore ×4 通过；两证据件在位未跟踪；`git diff --numstat` 恰 5 文件。

### Task 2 — 实机起停验证 + 原子提交
- **前置复查**：8080/8443/8081 无 LISTENING、无本仓库 service.exe（live 验证不跳过）；config.local.yaml、.local-certs/（hta-lan.pem/key）、.env 均在（HTTPS 分支可跑）；基线 12099/7538 字节。
- **HTTP 轮**：`run.ps1` → :8080 LISTENING（PID 51256）→ logs/server.log（JSON 启动行非空）与 logs/server.out.log 在位 → 仓库根无 server.log/server.out.log → `stop.ps1` → 三端口净空。logs/ 由本次启动自动创建（规划期不存在），验证了目录自建路径。
- **HTTPS 轮**（关键路径）：`run.ps1 -Https` → :8443 LISTENING（PID 18168）→ logs/server-8443.err.log **非空**（1955 字节 JSON 启动行，无需降级为存在性断言）与 logs/server-8443.out.log 在位 → 根上 server-8443.err.log 仍 12099 字节（截断已随落点迁走，根上冻结件与新 logs 副本共存即预期）→ `stop.ps1` → 三端口净空、无残留 service.exe。
- **提交**：staged 恰 5 文件；密钥模式扫描（sk- / api_key / Bearer / secret）零命中；提交 `d09ce65`（pre-commit 钩子正常执行，未用 --no-verify）；提交后 `git status --porcelain` 空（logs/ 因 *.log 全忽略不出现）；未 push。全程未执行 reset-vocab.py。

## Verification

| 项 | 结果 |
|---|---|
| 语法解析 | run.ps1 与 run-local-8443.ps1 Parser 零错误（PARSE1-OK / PARSE2-OK） |
| HTTP live 轮 | :8080 LISTENING → logs/server.log + logs/server.out.log 在位、根上无同名 → stop 后三端口净空 |
| HTTPS live 轮 | :8443 LISTENING → logs/server-8443.err.log（1955B 非空）+ logs/server-8443.out.log 在位 → stop 后三端口净空 |
| 证据件不变性 | server-8443.err.log 起停前后均 12099 字节；rt-timing-20261006-202012.jsonl 均 7538 字节；`git ls-files` 不含两者 |
| 日志落点 | 四日志全在 logs/；仓库根仅剩冻结证据件 server-8443.err.log，零新增 .log |
| gitignore | 四日志名 check-ignore 全命中既有 `*.log`；git status 不出现 logs/；零新增规则、无 .gitkeep |
| 提交门 | 单原子提交 d09ce65（5 文件 +16/-12）、密钥扫描零命中、钩子正常、提交后 status 空、未 push、零文件删除 |

## Deviations from Plan

### 1. [Rule 1 - Bug] 计划 verify 的 `grep -c` 断言按行计数与计划自身 action 矛盾（验证命令修正，实现零改动）
- **发现于：** Task 1 静态验证
- **问题：** 计划 verify 要求 `grep -c 'Join-Path \$logDir' run.ps1 -ge 3`，但 `-c` 统计**行数**；计划 action 明确 L117 是单行双分支（两处 Join-Path $logDir），加上 L116 共 3 处 occurrence 落在 2 行，`grep -c` 恒得 2——按计划 action 逐字实现也过不了计划自己的 verify。
- **修正：** 验证命令改 `grep -o 'Join-Path \$logDir' run.ps1 | wc -l -ge 3`（计 occurrence），3 处全数确认。run.ps1 实现与计划 action 逐字一致，零代码改动。
- **文件：** 仅验证命令（不入库）。

其余执行与计划完全一致（live 验证未触发跳过分支；HTTPS err 日志非空未触发降级断言）。

## Self-Check: PASSED

- run.ps1 / playwright.config.js / .scratch/run-local-8443.ps1 / .scratch/e2e-rt-1312.mjs / .scratch/e2e-rt-1312-short.mjs 修改后入库：提交 d09ce65 存在（git log 已验证）。
- 四日志文件在 logs/：已验证（FOUR-LOGS-IN-logs/ OK）。
- 两证据件原位、字节数不变、未跟踪：已验证（12099 / 7538，git ls-files 空）。
- 提交后 git status --porcelain 空：已验证。
- stop.ps1 与 .gitignore 零改动：`git show --stat d09ce65` 仅 5 个计划文件。
