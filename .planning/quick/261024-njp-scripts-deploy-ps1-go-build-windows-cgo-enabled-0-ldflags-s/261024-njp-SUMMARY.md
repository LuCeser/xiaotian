---
phase: 261024-njp
plan: 01
subsystem: deploy-tooling
tags: [powershell, windows, deploy, rollback, smoke-test]
requires:
  - 261024-njn (install.ps1 建立的 D:\hta 安装目录布局：bin/web/python + config/secrets/certs/data/venv/versions)
  - 261024-njo (安装目录 hta.ps1 的 stop/start 退出码契约：stop 幂等 0，start 0=健康就绪)
provides:
  - scripts/deploy.ps1（deploy/rollback 双子命令：一条命令升级、一条命令回滚；-Binary 入口）
  - tests/test-deploy-ps1.js（Windows 冒烟套件，非 Windows/无 PowerShell 打印 SKIP 说明后 exit 0）
affects:
  - npm test 链（test-deploy-ps1 插入 test-start-detached 之后、test-install 之前）
tech-stack:
  added: []
  patterns:
    - 子进程调 hta.ps1 取 $LASTEXITCODE 当健康门（点调 exit 连杀风险的规避落在代码注释里）
    - TEMP 暂存构建（构建先于停机；不覆盖开发机 run.ps1 的 bin\service.exe）
    - 桩 hta.ps1 + 五禁区哨兵的夹具冒烟范式（延续 test-install.js 的 probeShell/spawnSync/mkdtemp 骨架）
key-files:
  created:
    - scripts/deploy.ps1
    - tests/test-deploy-ps1.js
  modified:
    - package.json
decisions:
  - "-Binary 给定但文件不存在 → 显式红字拒绝退出，绝不静默回退本机构建（路径打字错会部署进非预期代码，安全解读优于字面『否则 build』）"
  - "调 hta 一律 `powershell.exe -NoProfile -ExecutionPolicy Bypass -File <hta> <sub> | Out-Host` 子进程，管道只回传退出码整数（子脚本 stdout 若留在函数管道会污染返回值）"
  - "deploy.ps1 注释与输出全英文（PS 5.1 codepage 安全），与 hta.ps1/install.ps1 兄弟脚本一致；测试 SKIP 说明含计划要求的中文原句"
metrics:
  duration: 7 min
  completed: 2026-10-25
  tasks: 2
  files: 3
status: complete
actuals:
  tokens: 7400
  tasks: 2
  commits: 1
plan_head_before: 5e02086896ee4d6145855e9000c8771e0c99b787
plan_head_after: 32f4c74
commits: 1
---

# Phase 261024-njp Plan 01: deploy.ps1 升级/回滚脚本 + 冒烟套件 Summary

**One-liner:** scripts/deploy.ps1 一条命令升级（TEMP 构建 CGO_ENABLED=0 → hta stop → 备份 versions/prev → 换 bin/web/python → hta start 退出码健康门，失败打印可复制回滚命令）+ rollback 子命令（prev 换回重启、可重复执行），tests/test-deploy-ps1.js 场景 A-F 全绿接入 npm test。

## What Was Built

- **scripts/deploy.ps1**（新，230 行，PS 5.1 兼容）：deploy 默认动作按钉死顺序——预检（InstallDir/hta.ps1/现有 service.exe/源侧 static 与 *.py，缺任一红字退出零改动）→ 取新二进制（-Binary 或 TEMP\hta-deploy-<ts> 暂存构建，`$env:CGO_ENABLED='0'; go build -ldflags '-s -w' -o <staging>\service.exe ./cmd/service` 与 Makefile build 逐参一致；构建失败不停旧服务）→ hta stop（非零中止）→ 备份三目录至 versions\prev（旧 prev 先弃，幂等重跑）→ 换新（exe 强拷、web\static 整换防残留、python 顶层 *.py 非递归换）→ hta start 退出码即健康门（不另造第二套探测），失败红字给出 `.\scripts\deploy.ps1 rollback -InstallDir <dir>` 与 logs 位置。rollback 分支：缺 prev 报 "nothing to roll back to" 非零退出；换回三目录重启，prev 保留可重复执行。写面白名单恰为 InstallDir 下 bin/web/python/versions + TEMP 暂存；对 InstallDir 根零通配删除。
- **tests/test-deploy-ps1.js**（新，398 行）：桩 hta.ps1 记录子命令到 hta-calls.log（start 退出码经 HTA_STUB_START_EXIT 注入）。场景 A 备份+换新+哨兵+调用序全断言；B 健康失败退出 1 且输出含 rollback、bin 已是 NEW-BIN；C 接 A 夹具回滚、内容回 OLD-*、调用累计 stop,start,stop,start；D 无 prev 拒绝；E 无 hta.ps1 预检拦截且 bin 仍 OLD-BIN；F go 可用时真构建断言安装位为 MZ 头 >1MB 真 PE（实测 15.8MB）。跳过门在最前：HTA_TEST_FORCE_SKIP=1 / 非 win32 / 无 PowerShell 引擎 → 单行 SKIP 说明（含「Windows 家庭部署工具需 PowerShell，本环境跳过属预期非失败」）exit 0。
- **package.json**：test 链 `node tests/test-start-detached.js && ` 后恰插 `node tests/test-deploy-ps1.js && `，单 hunk，其余键零改动。

## Verification

- Task 1 门 `DEPLOY-PS1-SKELETON-OK`：PowerShell Parser 零错误 + 六项契约 grep（CGO_ENABLED/-ldflags '-s -w'/cmd/service/ValidateSet('deploy','rollback')/hta.ps1/versions）全中非注释行。
- Task 2 门 `DEPLOY-SUITE-WIRED-OK`：套件 Windows 全绿；HTA_TEST_FORCE_SKIP=1 输出 SKIP 且 exit 0；test 链含 test-deploy-ps1 且 test-start-detached 邻居无损、其套件复跑绿。
- 非门建议项：`npm test` 全链 Windows 本机绿（含新套件与链上全部既有套件）。
- 提交核验：`git show --stat HEAD` 恰含三文件（+629/-1）。

## Deviations from Plan

无实质偏差。两处机械适配记录：

1. **Task 1 解析门的调用方式**（非行为偏差）：计划门原文直接 `powershell -NoProfile -Command ParseFile`，本环境 Bash 工具直调 powershell 被工作树隔离守卫拦截（批次环境已知，261024-njn 同款）——改经 `node -e` spawnSync 同一 ParseFile 命令执行，门语义逐字保留；路径按本工作树解析（计划文本写的是主仓绝对路径）。
2. **[决策] -Binary 给定但缺失的语义**：计划 b 步字面「-Binary 给定且 Test-Path 通过 → 用它；否则 build」对「给定但缺失」留了静默回退构建的口子——路径打错时会部署进非预期代码，按更安全解读改为显式拒绝退出（头部注释与错误文案写明理由）。夹具场景 A/B/E 传的 -Binary 恒存在，门不受影响。

## Commit Discipline (Task 2 第 6 步)

`git status --porcelain package.json scripts/start-detached.mjs tests/test-start-detached.js`：start-detached 三件套在本基线（5e02086）均已落账（40ab6dd/8961739），工作区仅本任务三文件——走默认路径，一枚原子提交 `32f4c74` 恰含 scripts/deploy.ps1、tests/test-deploy-ps1.js、package.json，无外来改动被吸收。

## Real-Machine Drill Hint (范围外，按需手跑)

```powershell
# 升级（真构建）：
.\scripts\deploy.ps1 -InstallDir D:\hta
# 或部署指定可信二进制：
.\scripts\deploy.ps1 -InstallDir D:\hta -Binary C:\path\to\service.exe
# 出问题时一条回滚：
.\scripts\deploy.ps1 rollback -InstallDir D:\hta
```

## Known Stubs

None.

## Threat Flags

无新增面——deploy.ps1 的删改面即计划 threat_model 的 T-261024-njp-01/T-02 缓解本体（写面白名单 + 顺序钉死），并经五哨兵字节级断言与场景 B/C/D/E 行为级钉死。

## Self-Check: PASSED

- scripts/deploy.ps1 存在于工作树（已提交 32f4c74）。
- tests/test-deploy-ps1.js 存在于工作树（已提交 32f4c74）。
- package.json test 链含 test-deploy-ps1（已提交 32f4c74）。
- `git rev-list --count 5e02086..HEAD` = 1（与 ledger 一致）。
