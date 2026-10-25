---
phase: 261025-fzp
plan: 01
type: execute
wave: 1
depends_on: []
files_modified:
  - README.md
autonomous: true
requirements: []
estimate:
  tokens: 24000
  raw_tokens: 12000
  tasks: 1
  confidence: med
must_haves:
  truths:
    - 'README.md 出现 `## 安装与部署（Windows 本机）` 一节，位置在「## Running the Service」整节之后、「## Test」之前；其余既有章节字节不变'
    - '一次性安装小节给出完整 install.ps1 命令（-InstallDir/-DataDir/-LanIp/-PyIndex 四参齐备）与五个要点：-LanIp 必须显式传物理网卡地址（自动探测可能选中 Tailscale 100.64.x 虚拟网卡，已踩坑）；-PyIndex 默认清华源、403 时换阿里源 mirrors.aliyun.com/pypi/simple/（已踩坑）；防火墙放行 9443 需管理员一次的 netsh 命令；安装幂等且 secrets/、config、certs、venv、数据目录永不覆盖；装完手填 secrets\.env（VOLCENGINE_TTS_*、MIMO_API_KEY）'
    - '日常启停小节写明在安装目录内 .\hta.ps1 start / stop / status；start 打印 iPad 入口 https://<LanIp>:9443/tutor/；status 退出码 0=健康 / 1=不健康；警示提权终端启动的实例必须提权停止（WMI 拉起继承调用方权限级别，已踩坑）、建议统一普通终端'
    - '升级/回滚小节写明 deploy.ps1 升级命令（构建→停→备份到 versions\prev→换新→启→健康门，失败提示回滚命令）与 rollback 子命令一键回退；绝不触碰 config/secrets/certs/数据/venv'
    - '首台 iPad 证书小节（安装 <InstallDir>\certs\ipad\ 下根证书）与端口并存小节（部署 9443/9444 与开发 run.ps1 8080/8443/8081 错开互不干扰）均在位'
    - '除 README.md 外无任何文件改动；恰一枚原子提交'
  artifacts:
    - 'README.md（新增「安装与部署（Windows 本机）」一节，其余行零改动）'
  key_links:
    - 'README 所写命令 ↔ 三件套脚本真实参数面逐项一致：scripts/install.ps1 第 28-35 行 param 块（InstallDir/DataDir/LanIp/PyIndex，默认 D:\hta、清华源、端口 9443/9444）、scripts/deploy.ps1 第 52-54 行（Action position 0 ValidateSet deploy|rollback、InstallDir Mandatory）、scripts/hta.ps1 第 28 行（ValidateSet start|stop|status）——规划期已实证，执行器照 context 清单誊写，不得发明参数'
    - 'README 密钥指引 ↔ 真实消费方：安装器把 third_party/python/.env.example（仅 VOLCENGINE_TTS_API_KEY/APP_ID/ACCESS_KEY/RESOURCE_ID）种子成 secrets\.env；MIMO_API_KEY 不在该模板内、须照仓库根 .env.example 第 6 行手填，cmd/service/voice.go 第 41 行注明缺该键语音家教降级'
    - 'README 端口声明 ↔ 实测：run.ps1 第 13-23 行开发实例 8080 HTTP / 8443 HTTPS / 8081 健康；install.ps1 第 20 行部署默认 9443 公共 / 9444 健康'
---

# 快速任务：README.md 增补「安装与部署（Windows 本机）」一节

<objective>
把 261024 批次三件套（install.ps1 一次性安装 / hta.ps1 日常启停 / deploy.ps1 升级回滚）的实战验证流程写进仓库根 README.md，新增一节 `## 安装与部署（Windows 本机）`，内容以任务描述给定的五组「已实际踩坑」事实为唯一来源，参数与行为细节照下文规划期实证清单锚定到脚本原文。只改 README.md，一枚原子提交。

Purpose: 三件套已落地但 README 只有开发实例流程（run.ps1，8080）；家庭部署操作知识目前只存在于批次目录里，README 是外来者第一个翻的文件，缺这节等于部署流程不可发现。
Output: README.md 新增一节（约 60-90 行）；261025-fzp-SUMMARY.md。
</objective>

<execution_context>
@D:/workspace/ai/xiaotian/.claude/gsd-core/workflows/execute-plan.md
@D:/workspace/ai/xiaotian/.claude/gsd-core/templates/summary.md
</execution_context>

<context>
@.planning/STATE.md

执行器开工前重读（勿凭记忆）：
- `README.md` 全文——插入点按标题锚定：新节插在「## Running the Service」最后一个 blockquote（Troubleshooting，指向 docs/DEPLOYMENT.md 那段）之后、「## Test」之前；同时吸收既有行文手法（`###` 子标题、围栏代码、**加粗**警示、blockquote 提示）。
- `scripts/install.ps1` 第 1-60 行——头注释的用法示例、幂等承诺（"IDEMPOTENT AND DELETION-FREE: never overwrites secrets\.env..."）、param 块、防火墙命令模板。
- `scripts/hta.ps1` 第 1-28 行——start/stop/status 三子命令语义、退出码契约、secrets\.env 注入说明。
- `scripts/deploy.ps1` 第 1-52 行——deploy/rollback 双动作、写面白名单（只动 bin/web/python/versions）、versions\prev 单槽备份。
- `third_party/python/.env.example` 与仓库根 `.env.example`——前者的 VOLCENGINE_TTS_* 键（安装器种子模板）与后者的 MIMO_API_KEY（第 6 行）。

## 规划期实证（planner 2026-10-25 已逐项核对脚本原文，执行器不必重考，直接采信）
- install.ps1：`[string]$InstallDir = 'D:\hta'`、`[string]$DataDir = ''`（缺省落 <InstallDir>\data）、`[string]$LanIp = ''`、`[string]$PyIndex = 'https://pypi.tuna.tsinghua.edu.cn/simple'`、`[int]$PublicPort = 9443`、`[int]$HealthPort = 9444`。防火墙规则名格式 `"HTA Deploy {0}"` → 端口 9443 时即 `HTA Deploy 9443`；无管理员权限时退化为打印该命令让用户自己跑。
- 幂等面：secrets\.env 存在即保留（"never overwritten"）；config、certs、venv、数据目录同属永不覆盖；可刷新面（-Force）为二进制、静态前端、vendored python、hta.ps1。
- hta.ps1：ValidateSet('start','stop','status')；start 注入 secrets\.env（缺失则 .env 回退，都没有时黄字警告 TTS 会失败）、有界就绪等待后打印 iPad 入口 URL，exit 0=ready；status 为 GET /health 探测，exit 0=健康 / 1=不健康；安装器把它拷进安装目录根部，从安装目录内运行。
- deploy.ps1：默认动作 deploy（本机构建→经 hta stop 停旧→bin/web/python 备份到 versions\prev→换新→经 hta start 启动并以 start 退出码为健康门→失败打印可整行复制的回滚命令）；`rollback` 为位置参数子命令；绝不读写 config/secrets/certs/数据目录/venv。
- 密钥事实：安装器从 third_party/python/.env.example 种子生成 secrets\.env，该模板仅含 VOLCENGINE_TTS_API_KEY/APP_ID/ACCESS_KEY/RESOURCE_ID；MIMO_API_KEY 需照仓库根 .env.example 手填，cmd/service/voice.go 注释明确无该键语音家教降级。
- 证书事实：安装器从 .local-certs\ipad\ 拷根证书到 <InstallDir>\certs\ipad\（rootCA.pem / rootCA.crt），新 iPad 先装它再信任。
- 端口事实：开发实例 run.ps1 = 8080 HTTP（config.yaml）/ 8443 HTTPS（config.local.yaml）/ 8081 健康；部署实例 = 9443 公共 HTTPS / 9444 健康，天然错开。

## 范围外（明确不动）
- 除 README.md 外的一切文件：三件套脚本本体、docs/DEPLOYMENT.md、config.example.yaml、.env.example 均不改。
- 不新增测试文件（docs-only；门为 node 内容冒烟 + diff 检查，见 verify）。
- 不执行任何 PowerShell 脚本、不碰真实安装目录。
</context>

<tasks>

<task type="auto">
  <name>Task 1: README.md 新增「安装与部署（Windows 本机）」一节，验证后一枚原子提交</name>
  <files>README.md</files>
  <precondition>三件套脚本已在库：git ls-files scripts/install.ps1 scripts/hta.ps1 scripts/deploy.ps1 输出恰 3 行（261024 批次提交 8961739 / 7d625a6 / 32f4c74）。</precondition>
  <reversibility rating="reversible">纯文档增补；git revert 一枚提交即整体移除，不触碰任何代码。</reversibility>
  <action>
先全文重读 README.md，然后按标题锚定插入新节 `## 安装与部署（Windows 本机）`：位置在「## Running the Service」整节（含最后的 Troubleshooting blockquote）之后、「## Test」之前。其余既有行一个字符都不动。

正文用中文（与节名及任务事实原文一致；README 顶部本就有中文 callout，双语混排是现状），命令放 powershell 围栏、可整行复制，踩坑点用 **加粗** 或 blockquote 标注，无 emoji，子小节用 `###`。内容恰为以下五小节，事实逐字取自任务描述与上文规划期实证清单，不得发明参数或行为：

1. `### 一次性安装`——完整命令围栏：
   powershell -NoProfile -ExecutionPolicy Bypass -File ./scripts/install.ps1 -InstallDir D:/Apps/hta -DataDir <自定义数据目录> -LanIp <WLAN IP> -PyIndex <镜像>
   之后要点列表：**-LanIp 必须显式传物理网卡地址（WLAN/以太网）**——自动探测可能选中 Tailscale（100.64.x 段）等虚拟网卡（已实际踩坑）；-PyIndex 默认清华源，被 403 时换 https://mirrors.aliyun.com/pypi/simple/（已实际踩坑）；防火墙放行 9443 需管理员一次（安装器无管理员权限时会把命令打印出来让你自己跑），netsh 命令单独围栏：netsh advfirewall firewall add rule name="HTA Deploy 9443" dir=in action=allow protocol=TCP localport=9443；**安装幂等**——secrets\、config、certs、venv、数据目录永不覆盖，重跑安全；装完要手填 <InstallDir>\secrets\.env：VOLCENGINE_TTS_*（安装器从模板种子生成）与 **MIMO_API_KEY**（模板不含、需照仓库根 .env.example 手动加，缺它语音家教降级）。
2. `### 日常启停`——说明在安装目录内运行，围栏给出 .\hta.ps1 start / stop / status 三行；要点：start 打印 iPad 入口 https://<LanIp>:9443/tutor/；status 退出码 0=健康 / 1=不健康（机器可读，deploy.ps1 健康门复用同一契约）；**提权终端启动的实例必须提权停止**（WMI 拉起继承调用方权限级别，已实际踩坑）——建议统一用普通终端启停。
3. `### 升级与回滚`——围栏两行：powershell -NoProfile -ExecutionPolicy Bypass -File ./scripts/deploy.ps1 -InstallDir D:/Apps/hta 与同前缀加 rollback 子命令的一行；要点：流程为本机构建→停→备份 bin/web/python 到 versions\prev→换新→启→健康门，失败时打印可整行复制的回滚命令；rollback 一键回退；**绝不触碰 config/secrets/certs/数据/venv**。
4. `### 首台 iPad 证书信任`——如 iPad 提示证书不受信：安装 <InstallDir>\certs\ipad\ 下的根证书后再信任。
5. `### 与开发实例并存`——部署实例 9443（HTTPS 入口）/ 9444（健康）与开发实例 run.ps1 的 8080/8443/8081 端口错开，互不干扰。

写完先跑 <verify> 门（内容标记 + 仅 README 改动两项都要过），然后恰一枚原子提交，git add 只加 README.md，message：

   docs(quick-261025-fzp): README 增补「安装与部署（Windows 本机）」一节（install/hta/deploy 三件套实战要点）

   Co-Authored-By: Claude Code <noreply@anthropic.com>

门 MISS 时修的是正文内容，不是门本身；重跑直到通过。工作区若有外来未落账改动（非本任务的 README 之外文件），不吸收、原样留在工作区并在 SUMMARY 上报。
  </action>
  <verify>
    <automated>node -e "const s=require('fs').readFileSync('README.md','utf8');const need=['## \u5b89\u88c5\u4e0e\u90e8\u7f72','scripts/install.ps1','scripts/deploy.ps1','hta.ps1 start','hta.ps1 stop','hta.ps1 status','-LanIp','-PyIndex','pypi.tuna.tsinghua.edu.cn','mirrors.aliyun.com','HTA Deploy 9443','certs\\\\ipad','versions\\\\prev','VOLCENGINE_TTS_','MIMO_API_KEY','9444','8081','rollback'];const miss=need.filter(t=>!s.includes(t));if(miss.length){console.error('missing markers:',miss.join(' | '));process.exit(1)}console.log('README-WINDOWS-SECTION-OK')"
RAW=$(git diff --name-only) || { echo 'git diff failed'; exit 1; }
CHANGED=$(printf '%s\n' "$RAW" | grep -v '^README.md$' || true)
[ -z "$CHANGED" ] || { echo "unexpected files changed: $CHANGED"; exit 1; }
echo ONLY-README-CHANGED</automated>
  </verify>
  <done>门输出 README-WINDOWS-SECTION-OK 且 ONLY-README-CHANGED：新节在位（标题锚定于 Running the Service 之后、Test 之前）、五小节事实齐备（命令参数与脚本逐项一致、双踩坑警示、幂等永不覆盖清单、MIMO_API_KEY 手填指引、退出码契约、回滚命令、证书路径、端口错开）；README.md 之外零改动；恰一枚原子提交（git show --stat HEAD 恰含 README.md）。</done>
</task>

</tasks>

<threat_model>
## Trust Boundaries

| Boundary | Description |
|----------|-------------|
| README 内容 → 读者操作 | 文档命令会被运维者逐字复制执行，写错即真事故 |
| 脚本真实接口 ↔ 文档声明 | 文档与脚本参数面的漂移边界 |

## STRIDE Threat Register

| Threat ID | Category | Component | Severity | Disposition | Mitigation Plan |
|-----------|----------|-----------|----------|-------------|-----------------|
| T-261025-fzp-01 | Tampering | README 命令与脚本参数不一致导致复制即失败/误导 | high | mitigate | 全部事实锚定规划期实证清单（脚本 param 块与头注释逐行核对）；门以 ASCII 标记钉住关键命令片段与两镜像 URL、netsh 规则名、退出码端口 |
| T-261025-fzp-02 | Info Disclosure | 文档泄露真实密钥值 | medium | mitigate | 只写键名（VOLCENGINE_TTS_*、MIMO_API_KEY），绝不写任何真实值；来源模板本身即占位符 |
| T-261025-fzp-SC | Tampering | npm/pip/cargo installs | high | mitigate | 零包安装、零脚本执行的纯文档改动；保留位威胁不适用 |
</threat_model>

<verification>
- Task 1 门双输出 README-WINDOWS-SECTION-OK + ONLY-README-CHANGED。
- 提交后核验：git show --stat --oneline HEAD 恰含 README.md 一文件；git show HEAD -- README.md 抽查 diff 只新增一节、既有行零删改。
- 人工建议（非门）：浏览器渲染 README 确认围栏语言标记与标题层级显示正常。
</verification>

<success_criteria>
- README.md 新增「安装与部署（Windows 本机）」一节，五组实战事实全部落笔且与三件套脚本参数面逐项一致。
- 既有章节字节不变；除 README.md 外无文件改动；恰一枚原子提交。
- SUMMARY 写盘到本目录。
</success_criteria>

<output>
Create .planning/quick/261025-fzp-windows-readme-md-readme-md-windows-1-in/261025-fzp-SUMMARY.md when done
</output>
