---
phase: quick
plan: 261024-njo
type: execute
wave: 1
depends_on: []
files_modified:
  - scripts/hta.ps1
autonomous: true
requirements:
  - QUICK-261024-njo
must_haves:
  truths:
    - "hta.ps1 start 在安装目录布局下后台拉起 bin\\service.exe -config config\\config.yaml：健康端口 10 秒内由该次拉起的 PID 拥有监听后 exit 0，并打印 iPad 入口 URL（https://<LAN-IP>:<部署端口>/tutor/）"
    - "hta.ps1 stop 幂等：只杀 config 解析出的部署端口上的监听进程与本安装目录 bin\\ 前缀下的 service.exe 残留；对非 service.exe 的端口占用者拒绝误杀、红字告警并 exit 1"
    - "hta.ps1 status 做 HTTP /health 探活＋部署端口/进程查询：健康 exit 0、不健康 exit 1（exit-code 契约供 261024-njp 的 deploy.ps1 复用）"
    - "start 前把安装目录 secrets\\.env（不存在时回退 .env）注入本进程环境，供 service.exe 及其 Python 子进程经 os.Getenv 继承（D-05：密钥不进 config、不落日志）"
    - "全部日志写安装目录 logs\\（server.out.log / server.err.log 两个不同文件，start 时清空重建）"
    - "脚本对 Windows PowerShell 5.1 兼容：无 pwsh 专属语法、无 YAML 模块依赖、全文纯英文输出（规避 5.1 编码坑）"
  artifacts:
    - "scripts/hta.ps1（start/stop/status 三子命令的单文件日常启停脚本；本仓源文件，由 261024-njn 的 install.ps1 原样拷入安装目录）"
  key_links:
    - "后台拉起复刻 scripts/start-detached.mjs 的模式：显式文件 stdio（-RedirectStandardOutput 与 -RedirectStandardError 指向两个不同文件）＋ 隐藏窗口 ＋ -PassThru 取 PID；安装目录零 node 依赖"
    - "端口零硬编码：从安装目录 config\\config.yaml 的 server.addr / server.healthAddr 行解析（部署端口由 install.ps1 生成并避开开发占用的 8080/8443/8081）"
    - "就绪等待照抄 run.ps1 第 152-171 行『本进程必须拥有监听』逻辑（20×500ms 轮询、HasExited 短路），但不含 run.ps1 的 build 步骤"
    - "status 的 0/1 exit 契约是 261024-njp deploy.ps1 启动后健康检查的复用点"
---

<objective>
创建 `scripts/hta.ps1`：Windows 部署安装目录（如 D:\hta，由 261024-njn 的 install.ps1 建立的布局）里的日常启停脚本，提供 start / stop / status 三个子命令。

Purpose: 家庭部署机脱离开发仓库日常起停服务。start 复刻 run.ps1 + scripts/start-detached.mjs 已验证的「.env 注入 → 后台拉起 → 本进程必须拥有监听」链路（去掉 build 步骤——安装目录里是现成的 bin\service.exe）；stop 复刻 stop.ps1 的按端口杀进程模式；status 提供健康探活＋进程查询，其 exit code 是 njp（deploy.ps1）健康检查的复用契约。

Output: 仓库新增 `scripts/hta.ps1` 一个文件（本项唯一 git 改动），外加临时安装目录下的真实 service.exe 冒烟梯子证据（不进 git）。

跨项边界（批次契约，非本项依赖）：
- njn（install.ps1）负责把本文件拷入安装目录并在部署 config 里生成避开 8080/8443/8081 的端口——本计划 **不依赖** njn，冒烟自建临时安装目录布局；
- njp（deploy.ps1）升级后调用安装目录 hta.ps1 的 start 并依赖 status exit 契约——本计划交付这两个稳定入口；
- 本项不含 go build（那是 njp deploy 的职责）、不含安装/防火墙/证书（njn 职责）。
</objective>

<execution_context>
@D:/workspace/ai/xiaotian/.claude/gsd-core/workflows/execute-plan.md
@D:/workspace/ai/xiaotian/.claude/gsd-core/templates/summary.md
</execution_context>

<context>
@.planning/STATE.md
@run.ps1
@stop.ps1
@scripts/start-detached.mjs
@config.example.yaml

# 关键事实（已核实，勿重复调研）
- **run.ps1 可照抄的三段逻辑**（行号为当前文件实测）：
  - .env 加载器第 76-96 行：逐行 trim、跳过空行与 `#` 注释、容忍 `export ` 前缀、首个 `=` 切分、剥成对引号、`[Environment]::SetEnvironmentVariable($key, $val, 'Process')`、计数打印。
  - 清场杀监听第 98-110 行：`Get-NetTCPConnection -LocalPort <p> -State Listen` → 唯一 OwningProcess → `Stop-Process -Force`，杀过则 `Start-Sleep 2`。
  - 「本进程必须拥有监听」等待第 152-171 行：20 次 × 500ms，`$p.Refresh()`、HasExited 即断，`Get-NetTCPConnection -LocalPort <p> -State Listen | Where-Object { $_.OwningProcess -eq $servicePid }` 命中才算就绪；超时则 tail 5 行 err 日志、进程未死补杀、exit 1。
- **stop.ps1 两段模式**：按端口杀监听（第 12-23 行）＋ `Get-CimInstance Win32_Process -Filter "Name='service.exe'"` 按 `ExecutablePath` 以本目录 `bin` 为前缀（OrdinalIgnoreCase）清扫残留（第 26-35 行）。
- **start-detached.mjs 的契约**（hta.ps1 要在纯 PowerShell 里复刻，安装目录无 node）：detached 进程组 + 显式文件 fd（stdio ['ignore', fd, fd]）+ windowsHide + 返回子进程 PID。PowerShell 等价物 = `Start-Process -WindowStyle Hidden -RedirectStandardOutput <a> -RedirectStandardError <b> -PassThru`（5.1 下两个重定向文件路径必须不同，否则报错）；子进程 stdio 指向日志文件，绝不继承调用方管道——这正是 run.ps1 注释里 OpenCode 管道 EOF 挂起问题的解法。
- **service.exe 事实**：CLI 为 `-config <path>`（相对 CWD 解析，start 时 WorkingDirectory 必须是安装目录）＋可选 `-health-addr` 覆盖（hta.ps1 不用它，端口从 config 读）；`/health` 路由挂在 healthAddr 监听（cmd/service/main.go:930）；config `log.output: stderr` → JSON 日志进 server.err.log。
- **config.example.yaml 事实**：`addr:` / `healthAddr:` 只在 server 块出现、值带双引号（如 `addr: "0.0.0.0:8080"`）；`python.interpreter` 为空会被 config Validate 拒绝启动（fh3 已证）；本仓 `third_party/python/.venv/Scripts/python.exe` 已存在（冒烟 config 的 interpreter 指它，已确认在盘）。
- **PowerShell 5.1 可用性**（本机 `powershell` 即 5.1）：`Get-NetTCPConnection` / `Get-NetIPAddress` / `Get-CimInstance` / `Invoke-WebRequest -UseBasicParsing -TimeoutSec` 均可用；禁用 `??`、三元、管道链等 pwsh 语法；禁用 ConvertFrom-Yaml（无模块依赖）。
- **exit 传播**：`powershell -NoProfile -File <script>` 下脚本内 `exit N` 即进程退出码 N——冒烟梯子的断言依赖这一点。
- **开发端口**：8080/8443/8081 被 run.ps1/stop.ps1 体系占用；部署端口由 install.ps1 生成并避开，所以 hta.ps1 只认 config 解析值、全文无硬编码端口。
</context>

<tasks>

<task type="auto">
  <name>Task 1: 写 scripts/hta.ps1（start/stop/status，端口解析自安装目录 config）</name>
  <files>scripts/hta.ps1</files>
  <action>
单文件 PowerShell 脚本，纯英文输出消息（Write-Host 配色风格照 run.ps1：绿=成功、黄=提示、红=错误）。结构自上而下：

**1) 头部注释 + 参数**
- 英文头注释：用途、三个用法示例（`.\hta.ps1 start|stop|status`）、端口来自 `config\config.yaml`、本文件由 scripts/install.ps1 拷入安装目录、应在安装目录内运行。
- `param([Parameter(Position=0)][ValidateSet('start','stop','status')][string]$Command = 'status')`（缺省 status，只读安全）；`$ErrorActionPreference = 'Stop'`；`$root = Split-Path -Parent $MyInvocation.MyCommand.Definition`（脚本自身所在目录 = 安装目录，与 run.ps1/stop.ps1 同款锚定）。

**2) config 端口解析（内置函数，如 Read-HtaServerConfig）**
- 读 `$root\config\config.yaml`；缺失 → 红字提示先跑 scripts/install.ps1，exit 1。
- 行式解析 + 顶层块跟踪：以列 0 的 `^([A-Za-z_]\w*)\s*:\s*$` 识别当前顶层块（server:、python:、ws:、rt: …）；跳过空行与 `#` 注释行；仅在当前块为 server 时匹配 `^\s*addr\s*:\s*(.+?)\s*$` 与 `^\s*healthAddr\s*:\s*(.+?)\s*$`。
- 值剥成对引号后：host:port 按最后一个 `:` 切分，端口 `[int]` 强转并校验 1..65535（失败红字 exit 1）；healthAddr 的 host 缺省 `127.0.0.1`。产出 PublicPort / HealthHost / HealthPort 三值。全文禁止 Invoke-Expression / iex。

**3) .env 加载（内置函数，如 Import-HtaEnv）**
- 候选顺序：`$root\secrets\.env` → `$root\.env`，取第一个存在者（install.ps1 的密钥落点是 secrets\.env，回退兼容平铺 .env）。
- 解析逻辑逐行照抄 run.ps1 第 76-96 行（export 容忍、引号剥离、'Process' 作用域）；成功打印 `loaded N var(s) from <path>`（N、路径可见；变量值一律不回显）。两个候选都不存在 → 黄字警告 TTS 音频将失败（照 run.ps1），继续执行。

**4) start 子命令**
- 前置校验：`$root\bin\service.exe` 缺失 → 红字提示先跑 scripts/deploy.ps1，exit 1；`New-Item -ItemType Directory -Force` 建 `$root\logs`。
- Import-HtaEnv。
- 清场（照 run.ps1 第 98-110 行，加杀进程守卫）：对 PublicPort 与 HealthPort 两个端口取 Listen 属主去重，逐个守卫后 `Stop-Process -Force`；杀过则 `Start-Sleep 2`。守卫规则见第 6) 条 G1。守卫拒绝外来占用者 → 红字 exit 1。
- 清空两个日志文件：`logs\server.out.log`、`logs\server.err.log`（Set-Content 空串，照 run.ps1 第 121-122 行）。
- 后台拉起（start-detached.mjs 同款模式的 PowerShell 复刻）：`$p = Start-Process -FilePath (Join-Path $root 'bin\service.exe') -ArgumentList @('-config','config\config.yaml') -WorkingDirectory $root -WindowStyle Hidden -RedirectStandardOutput <logs\server.out.log> -RedirectStandardError <logs\server.err.log> -PassThru`；`$servicePid = $p.Id`。
- 有限就绪等待（照抄 run.ps1 第 152-171 行骨架，目标端口换成 HealthPort）：循环 20 × 500ms；每轮 `$p.Refresh()`，HasExited 即 break 失败；`Get-NetTCPConnection -LocalPort <HealthPort> -State Listen` 存在 `OwningProcess -eq $servicePid` 的行才算就绪。**不含任何 build 步骤。**
- 就绪：绿字报 PID + 日志路径；打印健康探针 URL `http://<HealthHost>:<HealthPort>/health`；取本机 LAN IPv4（`Get-NetIPAddress -AddressFamily IPv4`，排除 `127.*` 与 `169.254.*`，取第一条）打印 iPad 入口 `https://<LAN-IP>:<PublicPort>/tutor/`（可另起一行附 `https://<LAN-IP>:<PublicPort>/parent.html` 家长入口）；exit 0。
- 超时/早退：红字报 PID 未在 10 秒内拥有健康端口（含 HasExited 状态）；tail 5 行 `logs\server.err.log`；进程未死补 `Stop-Process -Force`；exit 1。

**5) stop 子命令（stop.ps1 模式）**
- 对 PublicPort 与 HealthPort 两个端口：Listen 属主去重 → 逐个按守卫 G1 判定后 `Stop-Process -Force`，黄字报每笔；外来占用者（守卫拒绝）记红字并视作失败。
- bin 前缀清扫（照 stop.ps1 第 26-35 行）：`Get-CimInstance Win32_Process -Filter "Name='service.exe'"` 过滤 `ExecutablePath` 以 `$root\bin` 开头（OrdinalIgnoreCase）→ Stop-Process；路径读不到的行 try/catch 跳过。
- 收尾判定：重查两端口 Listen 属主为空 → 绿字（含「nothing to stop」分支）exit 0；仍有属主（典型=被拒杀的外来占用者）→ 红字 exit 1。杀过进程则 `Start-Sleep 1` 再判定。

**6) status 子命令**
- 解析 config → 报部署端口与入口 URL。
- 健康探活：`Invoke-WebRequest -Uri http://<HealthHost>:<HealthPort>/health -UseBasicParsing -TimeoutSec 3` try/catch；StatusCode 200 → 绿字，否则/异常 → 红字。
- 进程查询：两端口各自打印 Listen 属主 PID + 进程名（Get-Process）；再列 bin 前缀清扫同款查询命中的 service.exe（PID + 路径）。
- 末行打印 `STATUS: healthy` 或 `STATUS: unhealthy`；exit 契约：健康探活 200 → exit 0，否则 exit 1（供 deploy.ps1 程序化判定）。

**守卫 G1（T-quick-njo-01 缓解，start 清场与 stop 共用）**：对每个待杀 PID，满足其一才杀——进程名为 `service.exe`（Get-Process -Id 的 Name），或进程可执行路径（.Path，读取包 try/catch）以 `$root\bin` 为前缀；否则判定为外来占用者：黄字报 PID/进程名、拒绝 Stop-Process、置失败标记。
  </action>
  <verify>
    <automated>cd "D:/workspace/ai/xiaotian" && powershell -NoProfile -Command '$errs=$null; [void][System.Management.Automation.Language.Parser]::ParseFile("D:/workspace/ai/xiaotian/scripts/hta.ps1",[ref]$null,[ref]$errs); if($errs -and $errs.Count -gt 0){ $errs | ForEach-Object { $_.Message }; exit 1 }; "PARSE_OK"' && grep -c "RedirectStandardError" scripts/hta.ps1 && grep -c "healthAddr" scripts/hta.ps1 && grep -c "OwningProcess" scripts/hta.ps1 && grep -c "ValidateSet" scripts/hta.ps1</automated>
  </verify>
  <done>
- scripts/hta.ps1 存在，Windows PowerShell 5.1 Parser 零错误（PARSE_OK）。
- start/stop/status 三子命令齐备（ValidateSet 三值，缺省 status），全部端口来自 config\config.yaml 解析，脚本内无硬编码端口号。
- start 链路齐备：secrets\.env→.env 回退加载、logs 目录与双日志文件清空、Start-Process 后台拉起（隐藏窗口 + 双文件重定向 + PassThru PID）、20×500ms 健康端口 own-the-listener 就绪门、超时 tail 日志补杀 exit 1、就绪打印 /health 探针 URL 与 iPad 入口 https://<LAN-IP>:<PublicPort>/tutor/。
- stop 带 G1 守卫与 bin 前缀清扫；status 探活 + 查询 + 0/1 exit 契约。
- 全文纯英文消息、无 pwsh 专属语法、无 Invoke-Expression。
  </done>
</task>

<task type="auto">
  <name>Task 2: 临时安装目录真实 service.exe 冒烟梯子（status↓1 → start→0 → status↑0 → stop→0 → status↓1）</name>
  <files>（无仓库文件——一次性驱动脚本与安装目录布局均在 %TEMP% 下，跑完删除）</files>
  <precondition>go 在 PATH 且本仓 third_party/python/.venv/Scripts/python.exe 存在（冒烟 config 的 python.interpreter 指它；两项 2026-10-24 已确认在盘）。</precondition>
  <action>
用 Write 工具把冒烟驱动脚本写到 `%TEMP%\hta-smoke-261024-njo.ps1`（绝对路径，%TEMP% 取本机实际值），驱动脚本实现以下步骤并在全部断言通过时打印 `SMOKE_PASS` 后 exit 0、任一失败打印失败步骤名后 exit 1；末尾 finally 清理。逐段：

**S1 脚手架**：`$dir = Join-Path $env:TEMP ("hta-smoke-" + [guid]::NewGuid().ToString("N").Substring(0,8))`；建子目录 `bin`、`config`、`logs`、`data\cache\audio`、`secrets`。

**S2 真实构建**：`Push-Location D:\workspace\ai\xiaotian` 后 `go build -o (Join-Path $dir "bin\service.exe") ./cmd/service`，断言 $LASTEXITCODE 为 0 且产物存在，Pop-Location。

**S3 模拟 install.ps1 落位**：Copy-Item 本仓 `scripts\hta.ps1` → `$dir\hta.ps1`（脚本必须以其所在目录为安装目录工作——与生产形态一致）。

**S4 生成部署形 config**：读 config.example.yaml 全文做正则替换后写 `$dir\config\config.yaml`——`interpreter:` 行 → `interpreter: D:/workspace/ai/xiaotian/third_party/python/.venv/Scripts/python.exe`；python 块五个路径键 `vocabDBPath:`、`dataDir:`、`cacheDir:`、`manifestDir:`、`envFile:` 各自整值行 → `$dir` 下的 data 布局绝对路径（正斜杠）；`addr:` → `"127.0.0.1:$pub"`；`healthAddr:` → `"127.0.0.1:$hp"`；`staticDir:` 行 → 本仓 `pkg/web/static` 绝对路径。端口变量：`$pub=18080; $hp=18081`，若驱动启动时任一端口已有 Listen 行则两个同时 +2 重选直到空出。断言写成的 config 含选定端口号。

**S5 密钥落位**：写 `$dir\secrets\.env`，内容一行 `HTA_SMOKE_MARKER=1`。

**S6 梯子断言**（每步 `& powershell -NoProfile -ExecutionPolicy Bypass -File "$dir\hta.ps1" <cmd>`，捕获合并输出与 $LASTEXITCODE）：
- a. `status`（未启动）→ exit 1，且输出含 unhealthy 字样（同时验证只读缺省与 config 解析在 -File 形态可用）。
- b. `start` → exit 0；输出含 `loaded 1 var(s)`（secrets\.env 回退链生效）与 `/tutor/`（入口 URL 已打印）。
- c. 端口归属断言：`Get-NetTCPConnection -LocalPort $hp -State Listen` 的属主进程（Get-Process -Id 取 .Path）路径以 `$dir\bin` 开头。
- d. `status`（已启动）→ exit 0；输出含 healthy。
- e. 日志断言：`$dir\logs\server.out.log` 与 `server.err.log` 均存在（config log.output=stderr，err 侧应有内容）。
- f. `stop` → exit 0；随后最多轮询 3 秒断言 $pub 与 $hp 均无 Listen 行（端口已释放）。
- g. `status`（再查）→ exit 1。
- h. 开发端口隔离说明：梯子全程只触碰 $pub/$hp（与开发 8080/8443/8081 天然错开），不需额外断言——在 SUMMARY 如实记录所选端口即可。

**S7 清理**：finally 中先按 `$dir\bin` 前缀清扫残余 service.exe，再 `Remove-Item -Recurse -Force $dir`（容错）；驱动脚本自身留在 %TEMP%（不进仓库）。

随后从 Bash 运行驱动并核对输出。
  </action>
  <verify>
    <automated>cd "D:/workspace/ai/xiaotian" && powershell -NoProfile -ExecutionPolicy Bypass -Command '& $env:TEMP\hta-smoke-261024-njo.ps1; if ($LASTEXITCODE -ne 0) { exit 1 }; if (-not (git status --porcelain scripts/hta.ps1)) { Write-Host "NO_HTA_CHANGE"; exit 1 }; Write-Host "VERIFY_OK"'</automated>
  </verify>
  <done>
- 驱动一次跑通：S6 七步断言全过（status↓1 / start→0 且打印 loaded 1 var(s) 与 /tutor/ / 健康端口属主在 $dir\bin 下 / status↑0 / 双日志文件在位 / stop→0 且两端口释放 / status↓1），末行 SMOKE_PASS、退出码 0。
- 真实 service.exe 参与起停（go build 产物，非 stub），安装目录布局 bin\ + config\config.yaml + secrets\.env + logs\ 与生产一致，hta.ps1 以拷贝落位形态运行。
- %TEMP% 脚手架目录已删除；git 侧本项仅新增 scripts/hta.ps1（git status 无其他本项改动）。
- 冒烟所选端口（默认 18080/18081 或重选值）与 status/start 输出摘录记入 SUMMARY。
  </done>
</task>

</tasks>

<threat_model>
## Trust Boundaries

| Boundary | Description |
|----------|-------------|
| 本地用户 → hta.ps1 → service.exe | 每日运维入口；secrets\.env 注入进程环境后由 service.exe 及其 Python 子进程经 os.Getenv 继承（复刻 run.ps1 既有通道，无新增暴露面） |
| config\config.yaml → 端口解析 | 部署 config 是脚本解析输入（install.ps1 生成，属本机可信文件，但解析器仍按不可信文本处理） |
| localhost 健康探针 | status 仅向 HealthHost:HealthPort 发 3 秒超时的 GET /health |

## STRIDE Threat Register

| Threat ID | Category | Component | Severity | Disposition | Mitigation Plan |
|-----------|----------|-----------|----------|-------------|-----------------|
| T-quick-njo-01 | Tampering | start 清场 / stop 的按端口杀进程 | medium | mitigate | 守卫 G1：待杀 PID 须满足「进程名 service.exe」或「可执行路径以本安装目录 bin\ 为前缀（OrdinalIgnoreCase）」才 Stop-Process；外来占用者拒绝误杀、红字告警并 exit 1（Task 1 第 4)/5)/6) 条） |
| T-quick-njo-02 | Information disclosure | secrets\.env 密钥经进程环境传递 | medium | mitigate | 复用 run.ps1 同一通道（同用户进程可见性，不新增落盘点）；输出只报变量计数不回显值；D-05 不变量保持密钥不进 config、不进日志（Task 1 第 3) 条） |
| T-quick-njo-03 | Tampering | config 行式解析器 | low | mitigate | 纯正则行解析 + 顶层块跟踪 + 端口值 1..65535 强校验；脚本全文无 Invoke-Expression（Task 1 第 2) 条） |
| T-quick-njo-SC | Tampering | npm/pip/cargo 包安装 | low | accept | 本项零包管理器安装（纯 PowerShell 新增文件，无 RESEARCH 审计表需求），不适用包合法性 checkpoint |
</threat_model>

<verification>
- Task 1 parse 门：Windows PowerShell 5.1 Parser 对 scripts/hta.ps1 零错误，且 RedirectStandardError / healthAddr / OwningProcess / ValidateSet 四个正向结构 token 全在。
- Task 2 冒烟梯子：真实 go build 出的 service.exe 在临时安装目录布局下完成 status↓1 → start→0（含 .env 注入与入口 URL 打印、健康端口属主校验）→ status↑0 → stop→0（端口释放）→ status↓1 全链，SMOKE_PASS / exit 0。
- 隔离性：冒烟全程仅使用 18080/18081（或重选）端口；开发用 8080/8443/8081 与仓内 venv、config.yaml 零接触。
- `git status --porcelain` 本项仅 scripts/hta.ps1 一条新增；%TEMP% 脚手架已删。
</verification>

<success_criteria>
- 安装目录日常运维三命令可用：`hta.ps1 start` 一条命令完成 .env 注入＋后台拉起＋就绪确认＋打印 iPad 入口 URL；`hta.ps1 stop` 幂等停服；`hta.ps1 status` 人读状态 + 机器读 0/1 exit code。
- 全部行为有真实 service.exe 冒烟证据（七步梯子），非脚本静态检查了事。
- scripts/hta.ps1 是本项唯一 git 改动；njn（拷贝入安装目录）与 njp（复用 start/status 契约）的对接面在文件头注释与本计划 objective 中明确。
</success_criteria>

<output>
完成后创建 `.planning/quick/261024-njo-hta-ps1-1-start-stop-status-start-env-scripts-start-detached/261024-njo-SUMMARY.md`，记录：
- scripts/hta.ps1 行数与三子命令结构摘要（含 G1 守卫位置）；
- 冒烟所选端口对、S6 七步各自输出摘录（尤其 start 打印的入口 URL 与 loaded N var(s)、status 两态输出）；
- go build 产物路径与构建耗时；
- 对 njn/njp 的契约重申（install.ps1 拷贝路径、deploy.ps1 复用的 status exit 契约）；
- 最终 git status 摘录（证明本项仅 scripts/hta.ps1 一个文件）。
</output>
