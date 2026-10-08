---
phase: 261007-drn
plan: 01
subsystem: e2e-testing
tags: [playwright, repo-hygiene, config-relocation, dry-run]
requires: []
provides:
  - "tests/playwright.config.js（自仓库根 git mv，历史保留）"
  - "tests/playwright.rt.config.js（自仓库根 git mv，历史保留）"
affects:
  - "Makefile e2e 目标（-c tests/ 化）"
  - "package.json e2e/e2e:rt 脚本串（-c tests/ 化）"
  - "tests/e2e-rt/rt-loop.spec.js L7 注释（-c 同步）"
tech-stack:
  added: []
  patterns:
    - "配置迁址范式：testDir 相对配置文件目录 + webServer.cwd 用 resolve(__dirname,'..') 显式指回仓库根"
key-files:
  created:
    - "tests/playwright.config.js（git mv + 编辑，rename 相似度 84%）"
    - "tests/playwright.rt.config.js（git mv + 编辑，rename 相似度 84%）"
  modified:
    - "Makefile"
    - "package.json"
    - "tests/e2e-rt/rt-loop.spec.js"
decisions:
  - "webServer.cwd 用 require('node:path').resolve(__dirname, '..') 绝对解析指回仓库根，不依赖调用方 cwd（--list 测不到的盲区由加载期 node -e 断言背书）"
  - "全部活调用方（Makefile/package.json×2/三处注释示例）同提交 -c tests/ 化，防裸调用回落默认配置把两套 spec 混跑（T-drn-02）"
metrics:
  duration: "4 min"
  completed: 2026-10-07
status: complete
commits: 1
plan_head_before: 58df9c4
plan_head_after: 4df02dc
actuals:
  tokens: 1308
  tasks: 2
  commits: 1
---

# Quick Task 261007-drn: playwright 配置归位 tests/ Summary

**一句话：** 两个 playwright 配置以 git mv（rename 84%）迁入 tests/，testDir 相对化 + 双 webServer.cwd 指回仓库根 + 全部活调用方 -c 同步，干跑双门（7/4 与 4/1）与加载期 cwd 断言全绿。

## What Was Done

### Task 1: git mv 双配置入 tests/ 并同提交改齐配置路径与全部活调用方

- `git mv playwright.config.js tests/playwright.config.js`、`git mv playwright.rt.config.js tests/playwright.rt.config.js`（历史保留，暂存即呈现 R）。
- `tests/playwright.config.js`：testDir `'tests/e2e'` → `'./e2e'`；webServer 新增 `cwd: require('node:path').resolve(__dirname, '..')`（command `pwsh -NoProfile -File run.ps1` 零改动）；头注释 L9 与 webServer 注释块同步 cwd 钉根说明。
- `tests/playwright.rt.config.js`：testDir `'./e2e-rt'`；webServer 同一 cwd 行（command `node tests/e2e-rt/mock-rt-server.js --port 8099` 零改动）；L9 隔离断言与 L12 示例改 `-c tests/` 形态；NO_PROXY 守卫块零改动。
- 调用方：Makefile L49、package.json `e2e`/`e2e:rt` 两行脚本串、rt-loop.spec.js L7 注释，全部 `-c tests/` 化。
- 三个文件 `node --check` 通过；`git diff HEAD --numstat` 恰好 5 条计划内路径，无 CRLF 幻影。

### Task 2: 干跑双门 + cwd 断言 + 调用方门，全绿后原子提交

- **cwd 盲区断言**：node -e require 两配置，testDir 断言 `./e2e`/`./e2e-rt` 通过，`resolve(cwd) === D:\workspace\ai\xiaotian` 双双通过。
- **干跑双门**（全程 DRY-RUN，--list 不启动 webServer）：
  - legacy：`Total: 7 tests in 4 files`（audio-unlock、dictation-flow、ipad-layout、parent-management），`grep -c rt-loop` = 0；
  - rt：`Total: 4 tests in 1 file`（rt-loop.spec.js），零存量 spec——H-1 目录隔离搬家后复验成立。
- **调用方门**：`npm run e2e -- --list` → 7/4、`npm run e2e:rt -- --list` → 4/1，脚本串直证；grep 门（Makefile/package.json 每处 `playwright test` 均带 `-c tests/`）通过；make 不在 PATH，按计划以 grep 为准，未装工具。
- **证据件守卫**：server-8443.err.log = 12099 字节、rt-timing-20261006-202012.jsonl = 7538 字节，前后一致，原位，未入库。
- **原子提交**：暂存区恰好 5 条（2 R + 3 M），`git diff --cached` 密钥扫描零命中，提交后 `git status --porcelain` 为空，未 push。

## Commits

| Commit | Description |
|--------|-------------|
| 4df02dc | chore(261007-drn): playwright 配置归位 tests/（git mv 双配置+testDir 相对化+webServer.cwd 指回仓库根+调用方 -c 同步）— 5 files changed, 17 insertions(+), 10 deletions(-)，两 rename 均以 84% 相似度判定（非 D+A） |

## Verification (plan <verification> 对照)

1. ✅ 根目录 `ls playwright.config.js playwright.rt.config.js` 两件均不存在；`git log --follow tests/playwright.config.js` 追到根时期提交 599c2a4（git mv 生效）。
2. ✅ 干跑双门计数精确：7 tests / 4 files（零 rt-loop）与 4 tests / 1 file（零存量 spec）。
3. ✅ cwd/testDir 加载期断言通过（--list 唯一测不到的盲区已补，T-drn-01 缓解落地）。
4. ✅ `npm run e2e -- --list` 与 `npm run e2e:rt -- --list` 经脚本串枚举成功；Makefile/package.json 无裸 playwright test（T-drn-02 缓解落地）。
5. ✅ 冻结证据件字节数不变（12099/7538）；恰好 5 条记录单原子提交；未 push（origin/main 落后于 HEAD）。

## Deviations from Plan

None - plan executed exactly as written.

**范围外观察（未动手）**：`.claude/worktrees/agent-*` 三个旧 agent 分支的冻结 worktree 检出内仍含搬家前的配置与裸调用形态。它们是 `.claude/` 下的历史 worktree 基础设施，不是仓库根的活调用方，按范围边界不动；若后续清理旧 worktree 时自然消解。

## Known Stubs

None（纯迁址与路径编辑，无数据流代码引入）。

## Self-Check: PASSED

- `tests/playwright.config.js` / `tests/playwright.rt.config.js` 在位（FOUND）。
- 提交 4df02dc 存在于 git log（FOUND），父提交 58df9c4，工作树净，未 push。
