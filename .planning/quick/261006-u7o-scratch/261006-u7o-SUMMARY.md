---
phase: 261006-u7o
plan: 01
status: complete
subsystem: repo-hygiene
tags: [cleanup, gitignore, archive, root-directory]
requires: []
provides:
  - "干净的仓库根（git status 零条目，除快任务目录自身）"
  - "scripts/ 分类目录：4 个活 DB 检查脚本（路径锚定仓库根）+ scripts/archive/ 11 个死脚本"
  - "docs/archive/ 5 个一次性分析文档"
  - ".gitignore GSD 运行态规则块（.gsd/、state.json、milestone.lock、research/.cache/、tmp-*、rt-timing jsonl、scratch/debug）"
affects: []
key-files:
  created:
    - stop.ps1
    - .scratch/run-local-8443.ps1
    - .scratch/serve-rootca.ps1
    - .scratch/uat_t234.go（自 .scratch/debug/ 迁出）
    - docs/archive/（5 个 md）
    - scripts/archive/（11 个死脚本）
    - scripts/check_db.py、scripts/check_due.py、scripts/check_schema.py、scripts/unlock_cards.py
    - AGENTS.md
  modified:
    - run.ps1（-Https 8443 模式，规划期已有改动，本次入库）
    - CONTEXT.md（Language 术语两条，规划期已有改动，本次入库）
    - .gitignore（+1 .pi 行 + 11 行 GSD 运行态规则块）
decisions:
  - "25 个 CRLF 幻影用 git add 索引刷新清零，git checkout -- 根本未执行（numstat 守卫证实内容零差异后索引刷新即清除）——比计划设想的更强：没有任何工作区字节被丢弃"
  - "server-8443.err.log 与 rt-timing-20261006-202012.jsonl 留在仓库根不提交，由 *.log 与 /rt-timing-*.jsonl 规则隐藏（13-UAT-ISSUES L102 证据链约束）"
  - "4 个 DB 脚本统一用 pathlib.Path(__file__).resolve().parent.parent 锚定仓库根 .hta-data/vocab.db，scripts/ 目录直接运行已验证"
metrics:
  duration: ~2h6m（14:02–16:08 UTC，含配额窗口中断）
  completed: 2026-10-07（本机时间；跨零点）
  tasks: 3
  commits: 6
commits:
  - "ef9a0e8 feat(261006-u7o): run.ps1 增 -Https 本机 8443 通道；stop.ps1 与 .scratch 运行助手入库"
  - "37efe69 docs(261006-u7o): CONTEXT.md 补 Language 术语——批量添加结果、整课批改"
  - "9adc146 docs(261006-u7o): 补档 Phase 9 VERIFICATION 验证文档"
  - "3975f1a chore(261006-u7o): gitignore 加固——GSD 运行态/研究缓存/根级证据不入库；research 缓存退库"
  - "4de06a0 chore(261006-u7o): 根目录清理——分析文档归档 docs/archive，调试脚本分类 scripts 与 scripts/archive，AGENTS.md 入库"
  - "9ac1996 chore(261006-u7o): .scratch 13-12 验收证据与 plan13 规划工件入库；uat_t234.go 自 debug 迁出"
---

# Quick Task 261006-u7o Summary

根目录整理落地：6 个原子提交（A–F）清空 90 条杂物——3 个真实改动分型入库、25 个 CRLF 幻影零噪声清零、可再生垃圾删除 + gitignore 防复发、5 md 归档 docs/archive、11 死脚本归档 scripts/archive、4 个活 DB 脚本入 scripts/ 并锚定仓库根路径、13-12 验收证据（46 文件）原地入库。

## What Was Done

### Task 1 — 基线 + 真实改动原子提交 + 幻影清零
- 基线：`go build ./...` exit 0；`go test ./... -short` exit 0（17 包全绿）。
- 提交 A `ef9a0e8`：run.ps1 + stop.ps1 + 两个 .scratch 运行助手（stop 是 run -Https 用法直接引用，四者一组）。
- 提交 B `37efe69`：CONTEXT.md 补 Language 术语（批量添加结果、整课批改）。
- 提交 C `9adc146`：09-VERIFICATION.md 补档（同目录 26 个兄弟文档均已跟踪）。
- **T-u7o-02 守卫**：numstat 对 25 个幻影文件输出为空（内容零差异证实）→ `git add` 索引刷新即全部清除，**git checkout -- 未执行**，工作区零字节丢弃。`git log --stat -3` 确认 pkg/ 与 .planning/config.json 未进任何提交。

### Task 2 — 删垃圾 + gitignore 加固 + research 缓存退库
- 已删：server.log、rootca-server.err.log、rootca-server.out.log、nul（Windows 保留名，rm 一次成功）、.planning/tmp-phase13-coverage.json、.scratch/debug/（uat_t234.go 先行迁出至 .scratch/）。
- 保留：server-8443.err.log、rt-timing-20261006-202012.jsonl（台账证据，gitignore 隐藏，verify 断言存在性）。
- 提交 D `3975f1a`：.gitignore 加 `# GSD/tool runtime state (2026-10-06 root cleanup)` 规则块（.gsd/、/skills-lock.json、.planning/state.json、.planning/milestone.lock、.planning/research/.cache/、.planning/tmp-*、/rt-timing-*.jsonl、.scratch/debug/）+ 3 个已跟踪 research 缓存 `git rm --cached` 退库（文件留盘）。check-ignore 8 目标全命中。

### Task 3 — 分类归档 + .scratch 证据入库 + 终验
- docs/archive/ ← 5 个一次性分析文档；scripts/archive/ ← 11 个死调试脚本（test2-websocket.js/.py、test-e2e-*.js、test-session-complete.js、4 个 debug html、fix_conflicts.py、reset-vocab.sh）。
- scripts/ ← 4 个活 DB 脚本，同一改法：`sqlite3.connect('.hta-data/vocab.db')` → `pathlib.Path(__file__).resolve().parent.parent / '.hta-data' / 'vocab.db'` 锚定。
- 提交 E `4de06a0`：AGENTS.md + scripts/ + docs/archive/（21 文件；staged 集密钥扫描 clean）。
- 提交 F `9ac1996`：.scratch 13-12 证据与 plan13 规划工件 46 文件原地入库（含 14 个 wav fixtures、e2e-1312/、uat_t234.go）；**T-u7o-01 重扫** staged 全部文件 sk-/api_key/Bearer/JWT 模式零命中。

## Verification（终验三件套）

| 项 | 结果 |
|---|---|
| git log | 6 个 261006-u7o 前缀提交（feat×1 / docs×2 / chore×3）全部落库 |
| git status --porcelain | 除 `.planning/quick/261006-u7o-scratch/` 外 **0 条目**（ROOT-CLEAN） |
| go build + test vs 基线 | build exit 0 / test -short exit 0，与 Task 1 基线完全一致，无新增失败 |

证据链：rt-timing jsonl 与 server-8443.err.log 仍在仓库根；`git ls-files .scratch/uat_t234.go` 有输出；`cd scripts && py -3 check_schema.py` 打印 cards 表结构（9 列，exit 0）。

## Deviations from Plan

### Auto-fixed / Notable

**1. [Rule 3-adjacent] server-8443.out.log 无法删除（外部进程锁）**
- **Found during:** Task 2
- **Issue:** `rm` 两次报 Device or resource busy——PID 50744 正监听 :8443（用户的本机 server 仍在运行，持有该日志句柄）
- **处理:** 不杀用户进程（超范围）；文件 0 字节且已被既有 `*.log` 规则忽略，对 git status 零影响，计划真值不受损。服务器停止后可随手删。
- **Commit:** 无（文件不入库）

**2. [计划内更强路径] 幻影清零未动用 checkout**
- numstat 守卫通过后，仅 `git add` 刷新索引即清零全部 25 个幻影——`git checkout --` 完全未执行。守卫本为防 checkout 丢改动，此处连 checkout 的必要都没有，T-u7o-02 风险面为零。

**3. [环境注记] `python` 是 WindowsApps stub**
- PATH 上的 `python` 是 Microsoft Store 别名 stub（exit 49 无输出）；脚本验证用 `py -3`（3.12.0）。与用户 Windows 本地调试约定一致，非阻塞。

## Known Stubs

None — 本任务为仓库卫生整理，不引入任何代码 stub。未跟踪文件删除均不可逆，已按计划逐项存在性确认后执行；两个台账证据文件与 uat_t234.go 走保留/迁出路径。

## Threat Flags

None — 无新增网络端点、认证路径或信任边界面。密钥扫描（提交 E staged 集与提交 F staged 全集）均零命中；server-8443.err.log 与 rt-timing jsonl（含本机会话与 LAN 信息）确认未入库。

## Self-Check: PASSED

- SUMMARY.md 存在；6 个提交哈希（ef9a0e8 / 37efe69 / 9adc146 / 3975f1a / 4de06a0 / 9ac1996）全部在 git log 可见。
- 关键工件在位：docs/archive/BUG-FIX-REPORT.md、scripts/check_schema.py、AGENTS.md、.scratch/uat_t234.go。
- 终态 git status 仅剩本 scratch 目录（`?? .planning/quick/261006-u7o-scratch/261006-u7o-SUMMARY.md`）——由 orchestrator 的收尾 docs 提交处理，executor 不入库。
