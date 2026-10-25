---
phase: 261025-fzp
plan: 01
subsystem: docs
tags: [readme, windows-deploy, powershell, install, deploy, rollback]

# Dependency graph
requires:
  - quick: 261024-njn
    provides: scripts/install.ps1 一次性安装器（README 新节的事实来源之一）
  - quick: 261024-njo
    provides: scripts/hta.ps1 日常启停（start/stop/status 退出码契约）
  - quick: 261024-njp
    provides: scripts/deploy.ps1 升级/回滚（versions\prev 单槽备份、写面白名单）
provides:
  - README.md「安装与部署（Windows 本机）」一节——外来者可发现的 Windows 本机部署操作手册（安装/启停/升级回滚/证书/端口并存）
affects: [13.2 VPS 部署, docs/DEPLOYMENT.md 后续整合, Windows 家庭部署操作]

# Actuals (#2632) — pairs with the plan's `estimate` to calibrate future estimates.
actuals:
  tokens: 918          # chars/4 over the realized diff (3671 bytes, 75 added lines)
  tasks: 1
  commits: 1

# Tech tracking
tech-stack:
  added: []
  patterns: []

key-files:
  created: []
  modified:
    - README.md

key-decisions:
  - "新节插入点锚定在「## Running the Service」末尾 Troubleshooting blockquote 之后、「## Test」之前；既有 123 行零删改（numstat 75/0）"
  - "五小节事实全部逐项对照脚本原文誊写（install.ps1 param 块/幂等承诺、hta.ps1 退出码契约、deploy.ps1 ValidateSet deploy|rollback 与写面白名单），未发明任何参数"
  - "密钥只写键名（VOLCENGINE_TTS_*、MIMO_API_KEY）不写任何真实值，密钥指引区分「模板已种子」与「模板不含须手填」两类"

patterns-established: []

requirements-completed: []

# Coverage metadata (#1602)
coverage:
  - id: D1
    description: "README.md 新增「安装与部署（Windows 本机）」一节：一次性安装（四参命令+LanIp/PyIndex 双踩坑+netsh 防火墙+幂等清单+secrets 手填）、日常启停（start/stop/status+退出码契约+提权警示）、升级与回滚（deploy/rollback 命令+流程+写面白名单）、首台 iPad 证书信任、与开发实例端口并存"
    verification:
      - kind: other
        ref: "node 内容冒烟门：18 个 ASCII 标记全中 → README-WINDOWS-SECTION-OK；git diff --name-only 仅 README.md；numstat 75/0 零删改"
        status: pass
    human_judgment: true
    rationale: "自动化门只钉内容标记与零删改；文档最终可读性（标题层级/围栏渲染）与命令对读者的实际可执行性属人工判断（计划亦列为非门人工建议）"

# Metrics
duration: 12min
completed: 2026-10-25
status: complete
plan_head_before: 1bec586d93aac16915c8319819cef2cce37c3de6
plan_head_after: 7e25f7a
commits: 1
---

# Quick Task 261025-fzp: README「安装与部署（Windows 本机）」Summary

**README.md 新增 75 行「安装与部署（Windows 本机）」一节：把 261024 三件套（install.ps1 / hta.ps1 / deploy.ps1）的实战部署流程与两处已踩坑警示写成可发现的操作手册，既有章节字节零改动。**

## Performance

- **Duration:** 12 min
- **Started:** 2026-10-25T03:35Z (approx)
- **Completed:** 2026-10-25T03:47:24Z
- **Tasks:** 1/1
- **Files modified:** 1

## Accomplishments

- README.md 新增 `## 安装与部署（Windows 本机）` 一节（75 行，五个 `###` 小节），插入点在「## Running the Service」之后、「## Test」之前
- 一次性安装：install.ps1 四参完整命令 + **-LanIp 必须显式传物理网卡**（Tailscale 100.64.x 虚拟网卡踩坑）+ **-PyIndex 403 换阿里源**踩坑 + netsh 防火墙单独围栏（规则名 `HTA Deploy 9443`）+ 幂等永不覆盖清单 + secrets\.env 手填指引（VOLCENGINE_TTS_* 模板种子 / MIMO_API_KEY 须照根 .env.example 手加，缺则语音家教降级）
- 日常启停：hta.ps1 start/stop/status 三行围栏（安装目录内运行）+ iPad 入口 URL + 退出码 0=健康/1=不健康契约 + **提权终端启动须提权停止**警示（WMI 权限继承踩坑）
- 升级与回滚：deploy.ps1 与 `rollback` 子命令两行围栏 + 构建→停→备份 versions\prev→换新→健康门流程 + **绝不触碰 config/secrets/certs/数据/venv** 白名单
- 首台 iPad 证书信任（`<InstallDir>\certs\ipad\`）与端口并存（部署 9443/9444 vs 开发 8080/8443/8081）两小节在位

## Task Commits

1. **Task 1: README.md 新增「安装与部署（Windows 本机）」一节** - `7e25f7a` (docs)

**Plan metadata:** 无（orchestrator 负责 docs 提交）

## Files Created/Modified

- `README.md` - 新增「安装与部署（Windows 本机）」一节（+75/-0，既有行零删改）

## Decisions Made

- 新节正文用中文，与节名及任务事实原文一致；README 顶部本就有中文 callout，双语混排是现状
- 全部命令参数逐项对照脚本原文誊写（install.ps1 param 块 28-37 行、hta.ps1 ValidateSet 第 28 行、deploy.ps1 param 52-56 行），执行前实证核对，未采信记忆
- 威胁模型 T-261025-fzp-02 落实：密钥只写键名，无任何真实值

## Deviations from Plan

None - plan executed exactly as written.

## Issues Encountered

- **验证门调用的 shell 转义假阳性（非内容缺失）**：node -e 门首次运行报 `certs\ipad`/`versions\prev` 缺失，但 grep 证实两串均在 README 第 128/135 行——本机 Git Bash 层在双引号与单引号下都会吞反斜杠，node 实际收到的是 `certsipad`。修法：门内用 `String.fromCharCode(92)` 构造反斜杠（不跨 shell 传字面反斜杠），语义与计划门的 JS 标记完全一致，重跑即 `README-WINDOWS-SECTION-OK`。README 内容自始正确，零改动。

## User Setup Required

None - no external service configuration required.

## Next Phase Readiness

- README 现已覆盖开发实例（run.ps1）与部署实例（三件套）两条路径，外来者从 README 即可发现完整 Windows 部署流程
- docs/DEPLOYMENT.md 与新节的分工（诊断 vs 操作）后续可按 2026-07-25 仓库卫生 todo 一并盘点

## Self-Check: PASSED

- SUMMARY.md 在盘：.planning/quick/261025-fzp-windows-readme-md-readme-md-windows-1-in/261025-fzp-SUMMARY.md（6103 bytes）
- 提交在库：7e25f7a（git log 全历史命中）
- 计量：git rev-list --count 1bec586..HEAD = 1（与 frontmatter commits: 1 一致）
- 提交面：git show --stat HEAD 恰含 README.md（+75/-0）

---
*Quick Task: 261025-fzp*
*Completed: 2026-10-25*
