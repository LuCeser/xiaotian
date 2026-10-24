---
phase: 261024-njn
plan: 01
type: execute
wave: 1
depends_on: []
files_modified:
  - scripts/install.ps1
  - tests/test-install.js
  - package.json
autonomous: true
requirements: []
estimate:
  tokens: 63000
  raw_tokens: 45000
  tasks: 2
  confidence: low
must_haves:
  truths:
    - '夹具冒烟：`node tests/test-install.js` 用临时夹具 SourceDir 跑 `install.ps1 -SkipFirewall -LanIp 192.0.2.10 -PublicPort 9443 -HealthPort 9444` 退出 0，产出完整安装布局——bin\service.exe、web\static\index.html、python\vocab_manager.py＋python\requirements.txt、config\config.yaml、certs\hta-lan.pem＋hta-lan-key.pem、secrets\.env、venv\Scripts\python.exe（stub uv 产物）、data\、logs\、hta.ps1、INSTALL-NOTES.md 逐项存在'
    - '生成配置逐键正确：config\config.yaml 含 addr "0.0.0.0:9443" 与 healthAddr "127.0.0.1:9444"（部署端口永不落 8080/8443/8081）、staticDir/tls.certFile/tls.keyFile/python.interpreter/scriptsDir/vocabDBPath/dataDir/cacheDir/manifestDir/envFile 全为安装目录绝对路径（Windows 反斜杠、YAML 单引号标量）、ws 与 rt 的 allowedOrigins 均含 LAN IP 条目、rt.forceStudentMode: true、全文无 /opt/hta 残留'
    - '幂等不覆盖：二跑前在 secrets\.env、config\config.yaml、certs\hta-lan.pem、data\ 下各放标记物，重跑安装器退出 0 且四处标记物原样存活（config 已存在时打印"保留现有"提示而非重生成）'
    - 'uv 调用契约：stub uv 的调用记录含 venv 子命令（--python 3.12、目标 <InstallDir>\venv）与 pip install 子命令（--python <InstallDir>\venv\Scripts\python.exe -r <InstallDir>\python\requirements.txt），环境变量 UV_INDEX_URL 已设为清华源；venv\Scripts\python.exe 已存在时整个 venv 步骤跳过（此时不要求 uv 在 PATH）'
    - 'INSTALL-NOTES.md 含安装证书的到期日（真 PEM 夹具下为 yyyy-MM-dd 日期；DER 解析失败降级为显式"未知"文案＋手工查看命令，安装不因此失败）、iPad 入口 URL https://<LanIp>:<端口>/tutor/、家长页 URL /parent.html、防火墙放行命令原文'
    - '参数防御：-PublicPort/-HealthPort 传 8080/8443/8081 或两端口相同 → 退出非 0 并点名"开发占用端口"；-LanIp 非 IPv4 字面量 → 拒绝；载荷缺失（bin\service.exe、pkg\web\static、third_party\python\*.py、config.example.yaml、.local-certs 叶证书、scripts\hta.ps1）→ 一次性列出全部缺失项后退出非 0'
    - '测试接线：package.json 的 test 链含 tests/test-install.js；无 PowerShell（powershell/pwsh 均不可用）的环境上该测试打印 SKIP 并退出 0'
  artifacts:
    - 'scripts/install.ps1（Windows PowerShell 5.1 兼容的一次性安装器，本计划核心交付）'
    - 'tests/test-install.js（夹具冒烟，照 tests/test-start-detached.js 的 mkdtemp＋spawnSync＋finally 清理范式）'
    - 'package.json（test 链插入 node tests/test-install.js）'
    - '.planning/quick/261024-njn-scripts-install-ps1-d-hta-bin-service-exe-pkg-web-static-web/261024-njn-SUMMARY.md（执行器写盘）'
  key_links:
    - 'config 的 python.interpreter ↔ uv 步骤产物 <InstallDir>\venv\Scripts\python.exe——Windows venv 布局 Scripts\python.exe（Makefile py-bootstrap 的 bin/python 是 Linux 布局，安装器必须用 Windows 形态），两处必须逐字一致'
    - 'ws/rt allowedOrigins 的 LanIp ↔ INSTALL-NOTES.md 的 iPad 入口 URL ↔ netsh 放行端口——同一组值三处出现，任何一处漂移即 iPad 打不开或连不上 /rt'
    - 'secrets\.env 布局契约（跨件共享接口）：本安装器定义 <InstallDir>\secrets\.env 并只在缺失时从 third_party\python\.env.example 播种；261024-njo 的 hta.ps1 start 按该路径加载 .env，261024-njp 的 deploy.ps1 升级时永不触碰 secrets\——三件套对齐的接口以本计划 + INSTALL-NOTES.md 文档为准'
    - 'config 变换 ↔ config.example.yaml 现行行文：整行精确匹配＋每模式恰一次计数闸，example 未来改版时安装器当场报错列出未命中的变换点，而非静默生成错配配置'
---

# 快速任务 261024-njn：scripts/install.ps1 一次性安装器（D:\hta 参数化布局 + 生产 config 生成 + uv venv + netsh + 幂等）

<objective>
把本机 Windows 开发机变成家庭常驻部署机的一次性安装器。`scripts/install.ps1` 从仓库检出（-SourceDir，默认取脚本上两级）把运行所需的一切落进安装目录（-InstallDir，默认 D:\hta）：bin\service.exe、web\static、vendored Python 脚本、按 config.example.yaml 变换出的生产 config\config.yaml（绝对路径、自定义数据目录、TLS 块指向拷入的 mkcert 证书、ws/rt allowedOrigins 含主机 LAN IP、端口避开开发占用的 8080/8443/8081 且健康端口也错开）、certs\ 证书、uv＋清华源建的 venv（Windows 布局，config interpreter 指它）、netsh 防火墙放行（非管理员时降级为提示＋记录命令）、scripts\hta.ps1（261024-njo 产出）拷入安装目录、含 mkcert 证书到期日的 INSTALL-NOTES.md。幂等：绝不覆盖已存在的 secrets\.env、config\config.yaml、certs\*、数据目录内容；安装器全程零删除操作，重跑安全。

Purpose: 家庭 iPad 试用（Phase 13.3 落地形态）需要开发机 8080/8443 之外常驻一套独立端口的服务；后续 261024-njo（hta.ps1 日常启停）与 261024-njp（deploy.ps1 升级/回滚）都建立在本安装器定义的目录布局上。
Output: scripts/install.ps1 ＋ tests/test-install.js（夹具冒烟，stub uv 零网络）＋ package.json test 链接线。
</objective>

<execution_context>
@D:/workspace/ai/xiaotian/.claude/gsd-core/workflows/execute-plan.md
@D:/workspace/ai/xiaotian/.claude/gsd-core/templates/summary.md
</execution_context>

<context>
@.planning/STATE.md

执行器开工前重读一遍即可、勿凭记忆的仓库文件：
- `config.example.yaml`（变换源；context B 表格逐行对应它的现行行文）
- `Makefile` L81-89（py-bootstrap 的 uv 参数与清华源，venv 步骤照抄）
- `run.ps1`（L56-62 端口/配置模式、L98-110 按端口杀监听、L149-171 "本进程必须拥有监听"健康等待——本任务只取端口与风格参照，启动逻辑归 261024-njo）
- `config.local.yaml`（已验证的家庭 HTTPS 配置形态：TLS 块写法、裸 host allowedOrigins 条目、rt 条目注释说明 /rt 自动展开 scheme＋剥端口匹配）
- `tests/test-start-detached.js`（冒烟测试范式：mkdtemp 夹具、spawnSync、超时、finally 清理）
- `third_party/python/.env.example`（secrets\.env 的播种模板，本身在 git 内、无真实密钥）
- `pkg/infra/config.go` L161-176 TLSConfig、L195-199 rt.allowedOrigins、L251-261 rt Validate（空 origins 拒绝、通配符禁止）、L1242-1253 TLS 三态校验——生成配置必须过这些闸

## 规划期实证（planner 2026-10-24 已逐项做过，执行器直接采信）

### A. 安装目录布局契约（跨三件套的共享接口，INSTALL-NOTES.md 必须复述）

```
<InstallDir>\            默认 D:\hta，-InstallDir 参数化
  bin\service.exe        ← <SourceDir>\bin\service.exe（拷贝 -Force 可刷新）
  web\static\...         ← <SourceDir>\pkg\web\static（-Recurse -Force）
  python\*.py + requirements.txt ← <SourceDir>\third_party\python（-Force；requirements.txt 必须随拷，venv 步骤装它）
  config\config.yaml     ← 从 config.example.yaml 变换生成；已存在则保留＋提示（绝不覆盖）
  certs\hta-lan.pem, hta-lan-key.pem  ← <SourceDir>\.local-certs（逐文件只补缺，绝不覆盖）
  certs\ipad\rootCA.pem, rootCA.crt   ← 存在则拷（新 iPad 装根证书用；缺失仅提示不失败）
  secrets\.env           ← 缺失时从 third_party\python\.env.example 播种＋提示填 VOLCENGINE_TTS_*（绝不覆盖）
  venv\Scripts\python.exe ← uv 建（Windows 布局）；已存在跳过整个 venv 步骤
  data\                  ← -DataDir 参数化（默认 <InstallDir>\data）；只建目录绝不碰内容
  logs\                  ← 预建空目录，供 hta.ps1 写日志（261024-njo 约定）
  hta.ps1                ← <SourceDir>\scripts\hta.ps1 拷入（-Force；它由批内 261024-njo 产出）
  INSTALL-NOTES.md       ← 每次运行重新生成（派生信息非用户数据），证书到期日读自 certs\ 落盘后的证书
```

安装器零删除：任何路径都不执行 Remove-Item，重跑永远安全。载荷清单（全部在 SourceDir 下，缺任一即一次性列全后退出非 0）：bin\service.exe（提示 go build / 261024-njp）、pkg\web\static、third_party\python 的 vocab_manager.py＋pregenerate_dictation.py＋requirements.txt、config.example.yaml、.local-certs\hta-lan.pem＋hta-lan-key.pem、scripts\hta.ps1（提示由批内 261024-njo 产出）；uv 仅在 venv 待建时要求在 PATH（Get-Command uv），venv 已存在则不要求。

### B. config 生成：整行精确匹配变换表（防漂移的关键设计）

从 config.example.yaml 逐行读取（Get-Content），按下表**整行字符串精确匹配**（非子串、非正则）替换或插入；每个模式必须恰好命中 1 行，未命中或多次命中 → 列出全部未命中变换点退出非 0（example 未来改版时当场报错，不静默错配）。Windows 绝对路径一律写成 YAML 单引号标量（反斜杠在单引号内是字面量，go-yaml 正确解析；config.local.yaml 是相对路径故无先例，单引号是安全形态）：

| config.example.yaml 现行整行 | 生成结果 |
|---|---|
| `  addr: "0.0.0.0:8080"` | `  addr: "0.0.0.0:<PublicPort>"` |
| `  healthAddr: "127.0.0.1:8081"` | `  healthAddr: "127.0.0.1:<HealthPort>"` |
| `  staticDir: ./pkg/web/static` | `  staticDir: '<InstallDir>\web\static'` |
| `  staticRoute: /`（此行之后插入 3 行） | `  tls:` ／ `    certFile: '<InstallDir>\certs\hta-lan.pem'` ／ `    keyFile: '<InstallDir>\certs\hta-lan-key.pem'` |
| `  interpreter: /opt/hta/venv/bin/python` | `  interpreter: '<InstallDir>\venv\Scripts\python.exe'` |
| `  scriptsDir: third_party/python` | `  scriptsDir: '<InstallDir>\python'` |
| `  vocabDBPath: /opt/hta/data/vocab.db` | `  vocabDBPath: '<DataDir>\vocab.db'` |
| `  dataDir: /opt/hta/data` | `  dataDir: '<DataDir>'`（整行等值匹配天然区分其余 /opt/hta 行） |
| `  cacheDir: /opt/hta/data/cache/audio` | `  cacheDir: '<DataDir>\cache\audio'` |
| `  manifestDir: /opt/hta/data/cache` | `  manifestDir: '<DataDir>\cache'` |
| `  envFile: /opt/hta/data/dictation.env` | `  envFile: '<DataDir>\dictation.env'` |
| `ws:`（此行之后插入 1 行） | `  allowedOrigins: [localhost, 127.0.0.1, <LanIp>]` |
| `rt:`（此行之后插入 2 行） | `  allowedOrigins: [localhost, 127.0.0.1, <LanIp>]` ＋ `  forceStudentMode: true` |

vocabScript/pregenScript 两行不动（脚本已随拷进 python\，scriptsDir 指过去）。allowedOrigins 用**裸 host 条目**：config.local.yaml L82/L99 的已验证形态（/rt 自动展开 scheme＋剥端口匹配，config.go L195-198 同述），通配符 "*" 被 D-24 双层拒绝。forceStudentMode: true 是 config.example.yaml rt 块自述的家庭/生产入口推荐值（STATE Phase 13.3 决策：家庭正式入口显式 true）。
生成后写盘前自检（任一失败退出非 0）：addr/healthAddr 行含新端口；staticDir/tls 两行/interpreter/scriptsDir/五个数据路径行含安装目录前缀；两条 allowedOrigins 行含 LanIp；含 `forceStudentMode: true`；全文不含 `/opt/hta`、不含 `"0.0.0.0:8080"`、不含 `"127.0.0.1:8081"`。config 已存在时跳过生成，打印保留提示＋"删除该文件后重跑可重新生成"。

### C. venv 步骤（照 Makefile py-bootstrap L81-89，Windows 布局）

venv\Scripts\python.exe 已存在 → 跳过＋打印"删除 venv\ 后重跑可重建"。否则：`uv venv --python 3.12 <InstallDir>\venv`，随后设 `$env:UV_INDEX_URL = <PyIndex>`（参数 -PyIndex，默认 https://pypi.tuna.tsinghua.edu.cn/simple，与 Makefile PY_INDEX 同值）再 `uv pip install --python <InstallDir>\venv\Scripts\python.exe -r <InstallDir>\python\requirements.txt`（只装仓库冻结的锁文件，绝不引入新包名，也**不回写 freeze**——安装目录的 requirements.txt 是仓库锁的副本，别碰仓库文件）。装完断言 venv\Scripts\python.exe 存在。uv venv 首次可能自下载 Python 3.12（联网），这一点写进 INSTALL-NOTES。

### D. 证书到期日提取（planner 已在 Windows PowerShell 5.1.26100 上用真 mkcert 证书实测通过，输出 NotAfter 2029-01-03）

PS 5.1 的 .NET 不能直接吃 PEM 文件，但吃 DER 字节数组：Get-Content certs\hta-lan.pem 逐行收集 `-----BEGIN CERTIFICATE-----` 与 `-----END CERTIFICATE-----` 之间的 base64（不含标记行），`[Convert]::FromBase64String` 得 byte[]，`New-Object System.Security.Cryptography.X509Certificates.X509Certificate2 -ArgumentList @(,$der)`（@(,) 包装防数组展平），读 `.NotAfter.ToString("yyyy-MM-dd")`。任何一步抛异常 → 降级文案"到期日未知（手工执行 openssl x509 -enddate -noout -in <路径> 查看）"＋继续安装（警告不失败）。

### E. 端口 / 防火墙 / LAN IP 事实

- 开发占用端口（run.ps1 L58-62）：8080（config.yaml HTTP）、8443（config.local.yaml HTTPS）、8081（健康）。-PublicPort 默认 9443、-HealthPort 默认 9444；校验：两端口均不得为 8080/8443/8081、不得相同、1-65535；Get-NetTCPConnection 查到目标端口已有监听 → 警告＋建议换端口（不失败）。
- 健康口是 127.0.0.1-only（config.go 注释：与公网 addr 分离），**不需要**防火墙规则；只放行 -PublicPort。
- netsh：`netsh advfirewall firewall add rule name="HTA Deploy <PublicPort>" dir=in action=allow protocol=TCP localport=<PublicPort>`。幂等：先 `Get-NetFirewallRule -DisplayName "HTA Deploy <PublicPort>" -ErrorAction SilentlyContinue`，已存在跳过。管理员判定用 WindowsPrincipal/WindowsBuiltInRole Administrator；非管理员时尝试执行并在失败 catch 里降级——打印 + 把命令原文写进 INSTALL-NOTES（提示需管理员跑一次，给出 Start-Process -Verb RunAs 一行式）。-SkipFirewall 开关整体跳过（测试/CI 用）。防火墙失败**不是**安装失败。
- LAN IP：-LanIp 显式给则用（[System.Net.IPAddress]::TryParse 校验＋拒绝回环地址）；未给则 Get-NetIPAddress -AddressFamily IPv4 里排除 127.*、169.254.*、SkipAsUnreachable，取第一条；一条都没有 → 退出非 0 提示用 -LanIp 指定。

### F. 测试与机器事实

- 本机 powershell.exe（5.1.26100）与 pwsh 均在、node v24、uv 0.10.4（scoop shims）、`make` 不在 PATH（261015-ecc 实证）。测试 spawn 优先 pwsh、无则 powershell、都没有 → 打印 SKIP 退出 0（批内 261024-njp 同款"Linux CI 跳过并说明"约定）。
- 仓库 `.local-certs\hta-lan.pem`/`hta-lan-key.pem` 真实存在（未跟踪）：测试夹具优先拷真证书（断言 INSTALL-NOTES 出现 yyyy-MM-dd 日期）；找不到则写假 PEM（base64 无效）走"到期日未知"降级分支（断言安装仍退出 0）。
- stub uv：测试在夹具里落一个 uv.cmd（batch），把 PATH 前置后传给子进程；它把收到的参数行追加进 %UV_STUB_LOG%，venv 子命令时按第 3 个参数建 `<目标>\Scripts\` 并 type nul 出 python.exe。由此零网络验证调用契约，断言记录含 C 节的两组参数与 UV_INDEX_URL 值。
- 接线：package.json scripts.test 链在 `node tests/test-start-detached.js` 之后插 `&& node tests/test-install.js`。注意批内 261024-njo/261024-njp 大概率同样改 package.json 该行——执行器按届时工作区实况做最小插入，冲突时保序合并；此为已知共享文件，编排方应串行执行三件套。

### G. 风格与安全约束

- Windows PowerShell 5.1 兼容：不用三元运算符/?. /??/ForEach -Parallel；$ErrorActionPreference = 'Stop' ＋ 外层 try/catch 出干净错误（run.ps1 同款）；全脚本只用 ASCII 安全的 cmdlet。
- 输出卫生：控制台与 INSTALL-NOTES.md 绝不打印任何 .env 变量值或私钥内容；安装器不提权（不自触 UAC），管理员动作留给用户按说明执行一次。
- INSTALL-NOTES.md 内容清单：安装时间戳、目录布局图（A 节）、端口及"避开 8080/8443/8081"理由、LanIp、学生入口 https://<LanIp>:<端口>/tutor/（尾斜杠）与家长页 https://<LanIp>:<端口>/parent.html、证书到期日（D 节）＋certs\ipad\rootCA 的新设备提示、secrets\.env 待填提示（只提文件名不回显内容）、防火墙命令原文（若未落账）、启动指引 `.\hta.ps1 start`（指向 261024-njo）。
</context>

<tasks>

<task type="auto">
  <name>Task 1: 落地 scripts/install.ps1 一次性安装器（参数→载荷清点→布局拷贝→config 变换生成→uv venv→netsh→INSTALL-NOTES，全幂等零删除）</name>
  <files>scripts/install.ps1</files>
  <action>
  新建 scripts/install.ps1，按 context A-G 实现，头部注释块写明用途、用法示例与幂等承诺（照 run.ps1 注释密度）。结构依次为：
  1. param 块：-InstallDir（默认 D:\hta）、-SourceDir（默认 Split-Path $PSScriptRoot -Parent）、-DataDir（默认 <InstallDir>\data）、-PublicPort 9443、-HealthPort 9444、-LanIp（默认自动探测，E 节算法）、-PyIndex（默认清华源）、-SkipFirewall 开关。
  2. 参数校验（E 节端口规则：8080/8443/8081 黑名单＋两端口不同＋范围＋占用警告；LanIp 解析与回环拒绝；探测失败退出非 0）。
  3. 载荷清点（A 节清单＋uv 条件性要求）：收集全部缺失项一次列全后 exit 1，缺失项各自带一行补救提示（bin 提示 go build 或 261024-njp；hta.ps1 提示批内 261024-njo）。
  4. 建目录（bin/web/python/config/certs/secrets/DataDir/logs，New-Item -Force）。
  5. 拷贝，逐项区分刷新语义：bin\service.exe、pkg\web\static→web\static、third_party 的 *.py＋requirements.txt→python\、scripts\hta.ps1→根，均 -Force；certs 叶证书与 certs\ipad\ 逐文件**只在目标缺失时拷**（已存在打印保留）；secrets\.env 仅缺失时从 third_party\python\.env.example 播种＋打印填写提示（D-05：绝不写任何真实密钥值）。
  6. config 生成（B 节变换表：整行精确匹配、恰一次计数闸、插入点、单引号路径、写盘前自检九项；已存在跳过＋提示）。
  7. venv（C 节：存在即跳过；uv venv --python 3.12；$env:UV_INDEX_URL 后 uv pip install -r 安装目录内的 requirements.txt；产物断言；不回写 freeze、不改仓库文件）。
  8. 防火墙（E 节：-SkipFirewall 直跳；Get-NetFirewallRule 幂等查重；管理员直加；失败降级打印＋记入 notes 变量）。
  9. INSTALL-NOTES.md 生成（G 节内容清单；证书到期日用 D 节 DER 提取，异常走降级文案；防火墙命令按第 8 步结果决定是否收录）。
  10. 收尾摘要：新建/保留/跳过分组打印＋下一步指引（填 secrets\.env、（如需）管理员跑防火墙、.\hta.ps1 start、iPad URL）。
  全程不出现 Remove-Item；exit 0 仅在硬步骤全过（防火墙与到期日降级不算失败）。
  </action>
  <verify>
    <automated>powershell -NoProfile -Command '$t=$null;$e=$null;[System.Management.Automation.Language.Parser]::ParseFile((Join-Path (Get-Location) "scripts\install.ps1"),[ref]$t,[ref]$e)|Out-Null; if($e -and $e.Count -gt 0){$e|ForEach-Object{$_.Message};exit 1}; "PARSE-OK"' &amp;&amp; grep -c "param(" scripts/install.ps1 &gt;/dev/null &amp;&amp; grep -c "INSTALL-NOTES" scripts/install.ps1 &gt;/dev/null &amp;&amp; echo STRUCTURE-OK</automated>
  </verify>
  <done>PARSE-OK 与 STRUCTURE-OK 打印（PS 5.1 解析零错误、param 块与说明文件生成点在位）；脚本含 A 节全部布局路径、B 节全部变换点与恰一次计数闸、C/E 节的 uv 与 netsh 调用；无任何 Remove-Item 调用。</done>
</task>

<task type="auto">
  <name>Task 2: tests/test-install.js 夹具冒烟（stub uv 零网络＋幂等二跑＋负向参数）＋ package.json test 链接线</name>
  <files>tests/test-install.js, package.json</files>
  <action>
  照 tests/test-start-detached.js 范式（node:assert、os.tmpdir 夹具、spawnSync、finally 清理＋Windows 句柄释放等待）新建 tests/test-install.js：
  1. PowerShell 探测：优先 pwsh、退 powershell，均无则 console.log SKIP 理由＋process.exit(0)。
  2. 夹具 SourceDir：从仓库拷真 config.example.yaml；落哑 bin\service.exe、pkg\web\static\{index.html,js\app.js}、third_party\python\{vocab_manager.py,pregenerate_dictation.py,requirements.txt}、scripts\hta.ps1（一行哑脚本）；.local-certs 有真证书则拷真、否则写无效 base64 的假 PEM（两分支断言见 6）。夹具外建 stub 目录放 uv.cmd（F 节行为：记参数行进 %UV_STUB_LOG%、venv 子命令按第 3 参建 Scripts 并 type nul 出 python.exe），子进程 env 的 PATH 前置 stub 目录。
  3. 正向跑：-SkipFirewall -LanIp 192.0.2.10 -PublicPort 9443 -HealthPort 9444 -InstallDir/-SourceDir 指夹具；断言退出 0、must_haves 第 1 条的 11 项布局逐项存在。
  4. config 断言（must_haves 第 2 条逐键：两端口行、staticDir/tls 对/interpreter/scriptsDir/五路径的安装目录前缀（路径分隔符按 node path.join 产物匹配）、两条 allowedOrigins 含 192.0.2.10、forceStudentMode: true、无 /opt/hta）。
  5. stub 断言：UV_STUB_LOG 含 venv 子命令参数（--python 3.12 与 venv 目标）与 pip install 参数（--python …Scripts\python.exe -r …python\requirements.txt）＋ UV_INDEX_URL 为清华源值。
  6. INSTALL-NOTES 断言：真证书分支出现 /\b(19|20)\d\d-\d\d-\d\d\b 日期；假 PEM 分支出现降级文案且退出码仍 0。另断言含 https://192.0.2.10:9443/tutor/ 与防火墙命令文本。
  7. 幂等二跑：向 config\config.yaml 追加 '# MARKER-CFG'、secrets\.env 与 certs\hta-lan.pem 各写标记行、data\ 下放 keep.txt，重跑同一命令；断言退出 0、四处标记物原样、且幂等保护清单内容逐字未变。
  8. 负向三连：-PublicPort 8080 退出非 0 且输出点名开发端口；两端口同值退出非 0；删掉夹具 bin\service.exe 后跑，退出非 0 且输出同时点名缺失的 service.exe（可再删 hta.ps1 验证一次列全两条）。
  9. package.json：scripts.test 链在 test-start-detached.js 之后插入 `&& node tests/test-install.js`（共享文件，按届时工作区实况最小插入，见 context F）。
  </action>
  <verify>
    <automated>node tests/test-install.js &amp;&amp; node -e 'const s=require("./package.json").scripts.test; if(!s.includes("tests/test-install.js")) process.exit(1); console.log("WIRED")'</automated>
  </verify>
  <done>test-install.js 打印各段 PASS 并退出 0（布局/config/stub/notes/幂等/负向全覆盖）；WIRED 打印（test 链已含新测试）；npm 链上其余测试不受影响（仅 package.json 一行插入，无其他文件改动）。</done>
</task>

</tasks>

<threat_model>
## Trust Boundaries

| Boundary | Description |
|----------|-------------|
| 仓库检出 → 安装目录 | 构建产物/脚本/证书从 SourceDir 拷入生产位置，成为常驻家庭服务的运行体 |
| uv/pip 网络（清华镜像） | venv 依赖经镜像安装（可能附带 uv 自下载 Python 3.12） |
| netsh（提权面） | 防火墙规则修改需管理员， installer 自身不提权 |
| 生成的 config | TLS/origins/端口错误会让家庭入口静默不可用（连不上或证书错） |

## STRIDE Threat Register

| Threat ID | Category | Component | Severity | Disposition | Mitigation Plan |
|-----------|----------|-----------|----------|-------------|-----------------|
| T-261024-01 | Tampering | uv/pip 镜像安装（供应链） | high | mitigate | 只装仓库已冻结的 requirements.txt 锁（uv pip freeze 产物，STATE Phase 03 确认过 fsrs 等包名经上游核实），本任务零新增包名；清华镜像为项目既定渠道；不回写、不装临时试探包 |
| T-261024-02 | Elevation of Privilege | netsh 防火墙规则 | medium | mitigate | 规则最小化（具名＋TCP＋单 localport）；Get-NetFirewallRule 幂等查重防重复堆积；安装器绝不自动提权，管理员命令原文交用户执行一次并留档 INSTALL-NOTES |
| T-261024-03 | Tampering（配置错配） | config 变换生成 | medium | mitigate | 整行精确匹配＋每变换点恰一次计数闸＋写盘前九项自检——example 漂移或变换遗漏都变成安装期硬错误；生成的键集合最终仍受服务端 D-04 fail-fast 校验兜底（hta.ps1 首启即验） |
| T-261024-04 | Information Disclosure | secrets\.env / TLS 私钥 | medium | mitigate | 播种只用 git 内模板（无真实值）；控制台与 INSTALL-NOTES 绝不回显 .env 内容；hta-lan-key.pem 只拷贝不打印；幂等闸保证用户已填的 secrets 绝不被模板覆盖 |
| T-261024-SC | Tampering | 包管理器安装 | high | mitigate | 本计划涉及 pip（经 uv）安装：仅从仓库锁文件 third_party\python\requirements.txt 安装已审计依赖，不引入任何新包（无 [ASSUMED]/[SUS] 名单增量），故不设阻塞式人工合法性检查点；若执行中发现必须新增包名，停下转 orchestrator 裁决 |
</threat_model>

<verification>
- Task 1 门禁：PARSE-OK ＋ STRUCTURE-OK（PS 5.1 解析零错误、结构点在位）。
- Task 2 门禁：`node tests/test-install.js` 全段 PASS 退出 0 ＋ package.json 接线 WIRED。
- 人工抽查项（写进 SUMMARY 供 orchestrator/用户复核）：生成的 config 与 config.local.yaml 的已验证形态同构（TLS 块、裸 host origins）；安装器输出不含任何密钥值；重跑全程零删除。
</verification>

<success_criteria>
- scripts/install.ps1 在夹具上一次跑通产出 must_haves 第 1 条的完整布局；二跑幂等不覆盖四类保护对象。
- 生成 config 逐键满足第 2 条（端口避开发发占用、绝对路径、TLS 指 certs\、双 origins 含 LAN IP、forceStudentMode true、零 /opt/hta 残留）。
- uv 调用与 Makefile py-bootstrap 同构（清华源、Windows venv 布局、只装锁文件）；netsh 幂等且非管理员可完整走完安装。
- INSTALL-NOTES.md 含证书到期日（或显式降级文案）与全部入口信息；测试绿且接入 npm test 链、无 PowerShell 环境优雅跳过。
</success_criteria>

<output>
Create `.planning/quick/261024-njn-scripts-install-ps1-d-hta-bin-service-exe-pkg-web-static-web/261024-njn-SUMMARY.md` when done
</output>
