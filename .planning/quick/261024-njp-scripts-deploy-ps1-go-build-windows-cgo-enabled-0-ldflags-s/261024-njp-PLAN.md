---
phase: 261024-njp
plan: 01
type: execute
wave: 2
depends_on: ['261024-njn', '261024-njo']
files_modified:
  - scripts/deploy.ps1
  - tests/test-deploy-ps1.js
  - package.json
autonomous: true
requirements: []
estimate:
  tokens: 55000
  raw_tokens: 26000
  tasks: 2
  confidence: med
must_haves:
  truths:
    - 'Windows 上 `powershell -NoProfile -ExecutionPolicy Bypass -File scripts/deploy.ps1 -InstallDir <dir> -Binary <exe>` 一次跑完：旧 bin/web/python 三目录完整落入 <InstallDir>/versions/prev，新二进制、新 static、新 *.py 就位，安装目录 hta.ps1 依序被调 stop 再 start，hta start 退出码 0 时 deploy 退出码 0'
    - 'hta start 退出码非零（启动/健康失败）时 deploy 退出码 1，输出含 rollback 字样、一条可整行复制的回滚命令（deploy.ps1 rollback -InstallDir <dir>）与日志位置提示（InstallDir 下 logs 目录）'
    - 'deploy.ps1 rollback -InstallDir <dir> 把 bin/web/python 换回 versions/prev 内容并重启；versions/prev 不存在时非零退出并明示无可回滚'
    - 'config、secrets、certs、数据目录（config.yaml 里 python 块 vocabDBPath/dataDir/cacheDir/manifestDir/envFile 键所指处）、venv 五处全程字节不变——冒烟测试以哨兵文件钉死'
    - '默认构建路径与 Makefile build 目标逐参一致：CGO_ENABLED=0、ldflags 参数 -s -w、包路径 ./cmd/service；构建产物暂存 TEMP（不覆盖开发机 run.ps1 用的 bin/service.exe）；构建失败时旧实例未停、安装目录零改动（构建先于停机）'
    - 'node 冒烟套件在 Windows 本机全场景通过并接入 npm test 链；非 Windows 或无 PowerShell 环境打印一行跳过说明后 exit 0（HTA_TEST_FORCE_SKIP=1 可在 Windows 上证明该跳过路径）'
  artifacts:
    - 'scripts/deploy.ps1（新文件：deploy 默认动作 + rollback 子命令，PS 5.1 兼容语法）'
    - 'tests/test-deploy-ps1.js（新文件：照 tests/test-start-detached.js 的 temp 目录夹具 + spawnSync 范式）'
    - 'package.json（test 链恰插入 node tests/test-deploy-ps1.js 一项，其余键零改动）'
  key_links:
    - 'deploy.ps1 与安装目录 hta.ps1 的接口：deploy 只以子进程方式调 hta.ps1 stop/start 并把退出码当健康门——契约来自批次目录对 261024-njo 的定义（start 含有界健康端口就绪等待，照 run.ps1 第 149-171 行「本进程必须拥有监听」模式；stop 照 stop.ps1「没在跑也退出 0」语义）；deploy 不另造第二套健康探测'
    - 'deploy.ps1 构建步与 Makefile build 目标（第 17-19 行）同参：modernc.org/sqlite v1.55.0 纯 Go 驱动无 CGO（Phase 04 决策在 Makefile 与 CI 双锁 CGO_ENABLED=0）'
    - '数据安全论证链：所有运行时数据路径由 config.yaml python 块键驱动、全在 bin/web/python 交换面之外——所以只换三目录即可升级，零 Go 代码改动（本任务不改任何 .go 文件）'
---

# 快速任务：scripts/deploy.ps1 家庭部署升级/回滚脚本 + node 冒烟套件

<objective>
做家庭部署的「一条命令升级、一条命令回滚」工具 scripts/deploy.ps1：本机 Windows 直编新 service.exe（CGO_ENABLED=0、-ldflags '-s -w'，照 Makefile build 目标；SQLite 是纯 Go 驱动 modernc.org/sqlite，无需 CGO）→ 经安装目录 hta.ps1 停旧实例（若在跑）→ 当前 bin/web/python 备份到 versions/prev → 拷新版 → 经 hta.ps1 start 重启 → 健康检查（以 hta start 退出码为门），失败打印回滚命令；rollback 子命令把 versions/prev 换回并重启。绝不碰 config、secrets、certs、数据目录、venv。配 node 冒烟测试（照 tests/test-start-detached.js 对 start-detached.mjs 的测法），pwsh 套件在 Linux CI 打印说明后跳过。

Purpose: 批次三件套的收口——261024-njn 装好一次、261024-njo 管日常启停之后，版本升级仍靠手工拷文件，易错且无退路；deploy.ps1 把升级变成可回滚的单命令。数据路径全由 config.yaml 的 vocabDBPath/dataDir/cacheDir/manifestDir/envFile 键驱动（均在交换面之外），因此零 Go 代码改动。
Output: scripts/deploy.ps1、tests/test-deploy-ps1.js 两个新文件 + package.json test 链一项插入；一次原子提交。
</objective>

<execution_context>
@D:/workspace/ai/xiaotian/.claude/gsd-core/workflows/execute-plan.md
@D:/workspace/ai/xiaotian/.claude/gsd-core/templates/summary.md
</execution_context>

<context>
@.planning/STATE.md

执行器开工前重读（勿凭记忆）：
- `Makefile` 第 17-19 行——build 目标原文（构建参数的唯一权威）。
- `run.ps1` 第 98-110 行（按端口停进程）、第 112-147 行（TEMP 外的构建+detached 启动+日志落点风格）、第 149-171 行（有界就绪等待「本进程必须拥有监听」——hta start 健康门契约的来源）、第 30 行（$ErrorActionPreference='Stop' 风格）。
- `stop.ps1` 全文——按端口杀进程 + 「nothing to stop 也退出 0」语义（deploy 容忍没在跑的依据）。
- `tests/test-start-detached.js` 全文——冒烟测试骨架范式（mkdtemp 夹具、spawnSync、finally 清理、Atomics.wait 200ms 等 Windows 释放句柄）。
- `scripts/start-detached.mjs`——被上面测试的脚本本体，了解调用形态即可（本任务不复制它）。
- `config.example.yaml` 第 96-150 行——python 块 interpreter/scriptsDir/vocabScript/pregenScript/vocabDBPath/dataDir/cacheDir/manifestDir/envFile：数据路径全由这些键指向安装交换面之外，「只换 bin/web/python 即升级」的论证依据。
- `package.json` scripts.test 链——插入点在 node tests/test-start-detached.js 之后。
- 兄弟项目录 `.planning/quick/261024-njn-*`、`.planning/quick/261024-njo-*`：若 hta.ps1 已落盘，读其参数注释核对子命令形式（批次目录文本定义为 start/stop/status）；本计划测试用桩 hta.ps1，不依赖兄弟实现细节。

## 规划期实证（planner 2026-10-24 已逐项做过，执行器不必重考，直接采信）
- Makefile build 目标 = `CGO_ENABLED=0 go build -ldflags '-s -w' -o bin/service ./cmd/service`；go.mod 第 11 行 modernc.org/sqlite v1.55.0（纯 Go）；STATE 决策「CGO_ENABLED=0 在 Makefile build target 和 CI workflow 中锁定」。
- pkg/web/static 顶层实测：debug-multiword.html、home.html、index.html、js、parent.html、styles、tutor、vendor；third_party/python 顶层 *.py 恰两个：vocab_manager.py、pregenerate_dictation.py（同目录还有 requirements.txt、__pycache__、.venv——deploy 一律不碰，只拷 *.py）。
- CI（.github/workflows/ci.yml）ubuntu-only 且只跑 Go 步骤，npm test 目前不在 CI 内；跳过门仍必须做（未来接入或本地 WSL/Linux 跑 npm test 时不假失败）。
- PowerShell 调子脚本的坑：`& <script>.ps1` 与调用方同进程，子脚本里的 `exit 1` 会连带杀掉调用方——deploy 调 hta 必须用子进程（`powershell -NoProfile -ExecutionPolicy Bypass -File <hta> <sub>` 取 $LASTEXITCODE）。
- 工作区归属警告：scripts/start-detached.mjs、tests/test-start-detached.js 目前是未跟踪文件，package.json 有一处未落账改动（test 链头部加 test-start-detached.js）——属并行会话的工作。本任务动 package.json 同一条 test 链，提交纪律见 Task 2 第 6 步（外来改动不吸收、混合 hunk 上报不定夺）。

## 范围外（明确不动）
- 任何 .go 文件（背景明说零 Go 代码改动）、config.example.yaml、run.ps1/stop.ps1/start-detached.mjs 本体。
- 兄弟项的 install.ps1 / hta.ps1 实现（批次目录文本即接口契约）。
- .github/workflows/ci.yml（跳过逻辑在测试文件内实现，不动 CI 定义）。
- 真实家庭安装目录的强制端到端升级演练（自动化门全为夹具冒烟；真机演练留给用户按需执行，SUMMARY 里给出操作提示即可）。
</context>

<tasks>

<task type="auto">
  <name>Task 1: 写 scripts/deploy.ps1（deploy/rollback 双子命令，写面白名单钉死）</name>
  <files>scripts/deploy.ps1</files>
  <reversibility rating="reversible">纯新增文件；git revert 一次提交即整体移除，不触碰既有任何文件。</reversibility>
  <action>
新建 scripts/deploy.ps1，PS 5.1 兼容语法（照 run.ps1/stop.ps1，不用 PS7 专属特性，能在 powershell.exe 与 pwsh 双引擎下跑）。`$ErrorActionPreference = 'Stop'`；失败一律 Write-Host Red 说明 + exit 1（照 run.ps1 风格，不裸 throw）。

1. 头部注释块：用途、两行用法示例（升级：`.\scripts\deploy.ps1 -InstallDir D:\hta`；回滚：`.\scripts\deploy.ps1 rollback -InstallDir D:\hta`）、安全不变量——写面白名单恰为 InstallDir 下的 bin、web、python、versions 四棵子树加 TEMP 暂存目录；绝不读写删除 config、secrets、certs、数据目录、venv；数据路径全在 config.yaml python 块键下（vocabDBPath/dataDir/cacheDir/manifestDir/envFile），故换三目录即升级、零 Go 改动。
2. 参数：`[Parameter(Position=0)][ValidateSet('deploy','rollback')][string]$Action = 'deploy'`、`[Parameter(Mandatory=$true)][string]$InstallDir`、`[string]$Binary`（可选：直接给定预构建 service.exe，跳过本机构建——冒烟测试与「部署 CI 产物」两种场景共用该入口）。`$RepoRoot = Split-Path -Parent $PSScriptRoot`。
3. 公共辅助：调 hta 的封装函数——以子进程 `powershell -NoProfile -ExecutionPolicy Bypass -File <hta路径> <子命令>` 执行并返回 $LASTEXITCODE；代码注释写明「不得用 `& <script>` 点调：子脚本内 exit 会连带杀掉 deploy 进程」。hta 路径 = Join-Path $InstallDir 'hta.ps1'。
4. deploy 分支，顺序钉死（任何一步失败 exit 1，且输出说明安装目录此刻处于什么状态）：
   a. 预检：InstallDir 存在；hta.ps1 存在；InstallDir\bin\service.exe 存在（像个真部署）；源侧 RepoRoot\pkg\web\static 与 RepoRoot\third_party\python\*.py 存在。缺任一 → 明确报错退出，安装目录零改动。
   b. 取新二进制：-Binary 给定且 Test-Path 通过 → 用它；否则建 TEMP\hta-deploy-<时间戳> 暂存目录，Push-Location $RepoRoot 后 `$env:CGO_ENABLED='0'; go build -ldflags '-s -w' -o <暂存>\service.exe ./cmd/service`，Pop-Location。构建失败 → 红字退出并说明「尚未停机、安装目录未动」（构建先于停机是有意的：构建失败不该把在用服务搞停）。暂存不落 repo bin\——不覆盖开发机 run.ps1 用的 bin\service.exe。
   c. 停实例：调 hta stop。退出码非零 → 中止（stop.ps1 模式对「没在跑」退出 0，非零即真失败；此刻安装目录未动、旧服务仍在跑或已按 stop 语义处理）。
   d. 备份：InstallDir\versions\prev 已存在则 Remove-Item -Recurse -Force（单槽，旧备份丢弃，幂等重跑支持）；建 versions 目录；把 bin、web、python 三目录整体 Copy-Item -Recurse 到 versions\prev 下（备份先于拷贝，保证 prev 恒为旧版全量）。
   e. 拷新版：暂存 service.exe Copy-Item -Force 到 InstallDir\bin\service.exe；InstallDir\web\static 存在则 Remove-Item -Recurse -Force 后 Copy-Item RepoRoot\pkg\web\static 整目录到 web\static（整换而非合并，防已删文件残留）；删除 InstallDir\python 顶层 *.py（仅文件、不递归子目录——防误伤任何可能放进来的子树）后 Copy-Item RepoRoot\third_party\python\*.py 到 python\。
   f. 启动+健康门：调 hta start（hta start 自带有界健康端口就绪等待——批次目录对 261024-njo 的定义；deploy 不做第二套健康探测）。退出码 0 → 绿字成功（提示入口由 hta start 打印）；非零 → 红字提示「启动/健康检查失败」，给出可整行复制的回滚命令 `.\scripts\deploy.ps1 rollback -InstallDir <dir>` 与日志位置 InstallDir\logs\，exit 1。
5. rollback 分支：预检 InstallDir、hta.ps1、versions\prev 下 bin/web/python 三目录齐备（缺 prev → 「无可回滚」非零退出）；调 hta stop（非零中止）；Remove-Item 当前 bin、web、python 三目录后从 versions\prev 整体拷回（prev 保留不清空——rollback 可重复执行）；调 hta start 报结果，失败提示查 logs\（prev 仍在，可重试）。
6. 自查写面：全脚本只允许对第 1 步白名单路径做 Remove-Item/Copy-Item；不得出现对 InstallDir 根的任何通配删除；config/secrets/certs/venv/数据目录名不出现在任何写操作里。
  </action>
  <verify>
    <automated>powershell -NoProfile -Command '[System.Management.Automation.Language.Parser]::ParseFile("D:/workspace/ai/xiaotian/scripts/deploy.ps1", [ref]$null, [ref]$e) | Out-Null; if ($e.Count -gt 0) { $e | ForEach-Object { $_.Message }; exit 1 }; "PARSE-OK"' || exit 1
S=scripts/deploy.ps1
F=$(grep -v '^[[:space:]]*#' "$S")
grep -q 'CGO_ENABLED' <<< "$F" || exit 1
grep -qF -- "-ldflags '-s -w'" <<< "$F" || exit 1
grep -q 'cmd/service' <<< "$F" || exit 1
grep -qF "ValidateSet('deploy','rollback')" <<< "$F" || exit 1
grep -q 'hta.ps1' <<< "$F" || exit 1
grep -q 'versions' <<< "$F" || exit 1
echo DEPLOY-PS1-SKELETON-OK</automated>
  </verify>
  <done>门输出 DEPLOY-PS1-SKELETON-OK：脚本可被 PowerShell 解析器零错误解析；动作面覆盖 deploy 全序（预检/构建/停/备份/拷/启动健康门）与 rollback（预检/停/换回/重启）；构建参数与 Makefile build 目标逐参一致；-Binary 与 rollback 子命令在位。本任务不提交，与 Task 2 合为一枚原子提交。</done>
</task>

<task type="auto" tdd="true">
  <name>Task 2: 写 tests/test-deploy-ps1.js 冒烟套件（含 Linux CI 跳过门）并接入 npm test、原子提交</name>
  <files>tests/test-deploy-ps1.js, package.json</files>
  <behavior>
    - 场景 A deploy 成功：退出码 0；versions/prev 里 bin/web/python 均为旧内容（OLD-BIN/OLD-STATIC/OLD-PY）；安装位 bin\service.exe 内容为 NEW-BIN；web\static 顶层条目名集合与 repo pkg\web\static 完全一致且旧标记文件消失；python 顶层 *.py 集合恰为 repo third_party\python 顶层 *.py（vocab_manager.py、pregenerate_dictation.py）且旧脚本消失；五处哨兵（config/secrets/certs/data/venv）字节不变；versions/prev 顶层恰为 bin、python、web 三项；桩 hta 调用日志依序为 stop、start。
    - 场景 B 健康失败提示回滚：桩 start 退出码 1（经环境变量注入）→ deploy 退出码 1 且输出含 rollback 字样；此时代码已拷完（bin 为 NEW-BIN）。
    - 场景 C rollback：接 A 之后的夹具 → 退出码 0；bin/web/python 内容回到 OLD-*；哨兵不变；桩调用累计 stop,start,stop,start。
    - 场景 D 无 prev 可回滚：全新夹具跑 rollback → 非零退出且输出含「无可回滚」类信息。
    - 场景 E 预检拦截：无 hta.ps1 的夹具跑 deploy → 非零、输出提及 hta、bin\service.exe 仍为 OLD-BIN（零改动）。
    - 场景 F 真构建（条件）：go 可用时不带 -Binary 跑 deploy，断言安装位 service.exe 为真 PE 文件（前两字节 MZ 且大于 1MB）；go 不可用则打印说明仅跳过该场景。
    - 跳过门：非 win32、找不到 PowerShell 引擎、或 HTA_TEST_FORCE_SKIP=1 → 打印一行 SKIP 说明（写明「Windows 家庭部署工具需 PowerShell，本环境跳过属预期非失败」）后 exit 0。
  </behavior>
  <action>
新建 tests/test-deploy-ps1.js，骨架照 tests/test-start-detached.js：node 内置 assert/fs/os/path/child_process.spawnSync、os.tmpdir() 下 mkdtemp 一次性夹具、失败也让 finally 清理、清理前 Atomics.wait 200ms 给 Windows 释放日志句柄。

1. 跳过门放最前：process.platform 不是 win32、或 pwsh 与 powershell 引擎都探测不到、或 HTA_TEST_FORCE_SKIP=1 → console.log 单行 SKIP 说明（含原因：scripts/deploy.ps1 是 Windows 家庭部署工具、需要 PowerShell；Linux CI/无 PowerShell 环境跳过属预期）→ process.exit(0)。引擎探测：优先 pwsh（spawnSync 'pwsh' ['-NoProfile','-Command','$PSVersionTable.PSVersion.Major']），回退 powershell——这就是「pwsh 套件在 Linux CI 跳过并说明」的落点。
2. 夹具构造函数 makeFixture(root, opts)：建「假安装目录」——bin\service.exe 内容 OLD-BIN；web\static\old-app.txt 内容 OLD-STATIC；python\old_script.py 内容 OLD-PY；哨兵 config\config.yaml=KEEP-CONFIG、secrets\.env=KEEP-SECRETS、certs\hta-lan.pem=KEEP-CERTS、data\vocab.db=KEEP-DATA、venv\Scripts\python.exe=KEEP-VENV；桩 hta.ps1——把收到的子命令逐行追加到夹具根的 hta-calls.log，start 退出码读环境变量 HTA_STUB_START_EXIT（缺省 0），stop 恒 0。opts.noHta 可省略 hta.ps1（场景 E 用）。
3. 调 deploy 统一走 spawnSync(引擎, ['-NoProfile','-ExecutionPolicy','Bypass','-File', deployPath, ...args], { encoding:'utf8', timeout: 120000, env: {...process.env, ...extra} })。
4. 按 Behavior 逐场景实现（A 用 -Binary 指向内容 NEW-BIN 的暂存假二进制；目录集合断言用 fs.readdirSync 排序后 deepEqual；哨兵断言逐字节比对；场景 F 先探测 go）。
5. package.json：用 Edit 定点在 test 链 `node tests/test-start-detached.js && ` 之后插入 `node tests/test-deploy-ps1.js && `，只动这一处、不碰链上其他项与其他键。
6. 提交纪律（工作区有并行会话未落账改动，先分归属再提交）：先跑 <verify> 门；然后 `git status --porcelain package.json scripts/start-detached.mjs tests/test-start-detached.js` 判断 start-detached 三件套是否已被其属主会话落账——若已提交（package.json 干净基线上只有本任务的链插入），一枚原子提交 `git add scripts/deploy.ps1 tests/test-deploy-ps1.js package.json`，message：

   feat(quick-261024-njp): scripts/deploy.ps1 升级/回滚脚本 + node 冒烟套件

   Co-Authored-By: Claude Code <noreply@anthropic.com>

   若三件套仍未落账（package.json 的 test 链 hunk 混有属主改动）：只提交两个新文件（git add scripts/deploy.ps1 tests/test-deploy-ps1.js），package.json 的接入改动保持工作区、在 SUMMARY 与上报信息中说明「待 start-detached 属主落账后补一枚单行链插入提交」——绝不用 git add package.json 把外来改动一并吸收。门若 MISS：修的是实现/断言精度，不是门本身；重跑直到通过。
  </action>
  <verify>
    <automated>node tests/test-deploy-ps1.js || exit 1
HTA_TEST_FORCE_SKIP=1 node tests/test-deploy-ps1.js > /tmp/skip.out 2>&1 || exit 1
grep -q 'SKIP' /tmp/skip.out || exit 1
node -e "const t=require('./package.json').scripts.test; if(!t.includes('test-deploy-ps1')) process.exit(1); if(!t.includes('test-start-detached')) process.exit(1); console.log('WIRED')" || exit 1
node tests/test-start-detached.js || exit 1
echo DEPLOY-SUITE-WIRED-OK</automated>
  </verify>
  <done>门输出 DEPLOY-SUITE-WIRED-OK：套件在 Windows 全场景通过（场景 F 随 go 可用性执行或说明跳过）；HTA_TEST_FORCE_SKIP=1 证明非 Windows 路径打印 SKIP 说明且退出 0；package.json test 链含新套件且 test-start-detached 邻居不受损；提交纪律按第 6 步执行（默认路径一枚原子提交恰含三文件，混合 hunk 路径只提交两新文件并上报）。</done>
</task>

</tasks>

<threat_model>
## Trust Boundaries

| Boundary | Description |
|----------|-------------|
| deploy.ps1 → 安装目录文件系统 | 脚本持删改权的目标区：删换 bin/web/python/versions；config/secrets/certs/数据/venv 是必须隔离的禁区 |
| deploy.ps1 → 本机进程 | 停/启部署实例（经 hta.ps1）；go build 拉起本机工具链 |
| -Binary 参数 → 部署位 | 显式入口可把任意给定 exe 放进部署位（信任由运维自带） |

## STRIDE Threat Register

| Threat ID | Category | Component | Severity | Disposition | Mitigation Plan |
|-----------|----------|-----------|----------|-------------|-----------------|
| T-261024-njp-01 | Tampering | deploy.ps1 删除/覆盖过宽误伤禁区 | high | mitigate | 写面白名单钉死（仅 bin/web/python/versions + TEMP 暂存）；删除只允许叶子路径（web\static、versions\prev、python 顶层 *.py 文件）；冒烟测试五处哨兵字节级断言；对 InstallDir 根禁通配删除 |
| T-261024-njp-02 | DoS | 升级中途失败留下半新半旧或服务停摆 | medium | mitigate | 顺序钉死：构建/暂存先于停机（构建失败不停服务）；备份先于拷贝（prev 恒为旧版全量）；健康失败打印显式回滚命令；rollback 可重复执行恢复 |
| T-261024-njp-03 | Tampering | -Binary 参数植入任意可执行文件 | medium | accept | 本地单用户家庭部署、与手工 ssh 拷贝同级信任面；默认路径仍是本机构建；头部注释写明仅用于可信二进制 |
| T-261024-njp-04 | Info Disclosure | 升级过程读出/拷贝 secrets | low | accept | 备份面只含 bin/web/python（代码与静态资源，无密钥）；secrets\.env 只被 hta start 加载，deploy 不读取不拷贝不打印 |
| T-261024-njp-SC | Tampering | npm/pip/cargo installs | high | mitigate | 本计划零包安装（纯脚本+内置模块测试，无 npm/pip 新依赖）；保留位威胁不适用，冒烟套件不执行任何安装 |
</threat_model>

<verification>
- Task 1 门 DEPLOY-PS1-SKELETON-OK：PowerShell 解析器零错误 + 关键契约正门（构建参数、ValidateSet 子命令、hta 调用、versions 备份）。
- Task 2 门 DEPLOY-SUITE-WIRED-OK：行为级全场景（成功/健康失败提示回滚/回滚/无 prev/预检拦截/条件真构建）+ 跳过路径证明 + test 链接入与邻居无损。
- 提交后核验：默认路径 `git show --stat --oneline HEAD` 恰含 scripts/deploy.ps1、tests/test-deploy-ps1.js、package.json 三文件（混合 hunk 路径则恰两新文件且 SUMMARY 说明 package.json 待补提交）。
- 本机手跑建议（非门）：`npm test` 全链在 Windows 绿。
</verification>

<success_criteria>
- scripts/deploy.ps1 在夹具安装目录上完整走通 deploy（备份→换新→停启→健康门）与 rollback（换回→重启），五处禁区哨兵字节不变。
- 构建步与 Makefile build 目标逐参一致且产物暂存 TEMP；构建失败不停旧实例、不动安装目录。
- tests/test-deploy-ps1.js 全场景绿并接入 npm test 链；非 Windows/无 PowerShell 打印 SKIP 说明退出 0（HTA_TEST_FORCE_SKIP 可证）。
- 提交纪律执行：不吸收并行会话未落账改动；SUMMARY 写盘到本目录。
</success_criteria>

<output>
Create .planning/quick/261024-njp-scripts-deploy-ps1-go-build-windows-cgo-enabled-0-ldflags-s/261024-njp-SUMMARY.md when done
</output>
