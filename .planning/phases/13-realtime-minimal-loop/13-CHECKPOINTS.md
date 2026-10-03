# R0～R3 时间检查与真机证据

**T0:** 2026-09-30（本轮规划启动，规划计时）
**Timezone:** Asia/Shanghai，截止当日 23:59
**Current status:** 尚未实施新的实时语音，三次检查全部待执行；本文件不是通过报告。

| 检查 | 截止 | 状态 | 证据 | 下一步 |
|---|---|---|---|---|
| 第一周真 iPad 3 词闭环 | 2026-10-07 | Pending | 待提供 | 未成环则仅聚焦闭环修复，不推迟第三周 |
| 第三周闭环切换检查 | 2026-10-21 | Pending | 待提供 | 未成环立即实施 Plan B |
| 第六周延迟切换检查 | 2026-11-11 | Pending | 待提供 | RT-04/05 任一未达标立即实施 Plan B |

## 每次检查记录模板

- 检查日期、执行者、Git 版本、iPad 型号/iPadOS/Safari、访问地址（不含密钥）、网络及 VPS 环境。
- 一次授权后至少 10 轮连续对话；语音发起 3 词、追问上一词、慢读、等候书写、求助、打断。
- LLM 工具请求、get_lesson_state 结果、advance_word 的前后状态、lesson_id/request_id/expected_version；重复回调后版本和词序只变一次。日志脱敏。
- 真实扬声器播放、回声误触发、学生插话；停播后旧音频不复活；下一轮承认学生实际听到的位置。
- 每轮记录页面就绪、授权完成、连接就绪、speech_started/ended、工具开始/结束、首个收到音频、首个实际播放、打断开始/停播、课堂版本。浏览器播放时刻与服务端耗时分开，不直接相减未校准的跨机器时钟。
- RT-04 至少 30 次响应样本，报告首次问候耗时、轮次 P50/P95；RT-05 至少 30 次打断样本，报告 P95，并提供迟到音频和上下文检查结果。保留失败与超时，不只计算成功轮次。
- 用户可见结果、失败原因、证据路径、通过/未通过；若触发切换，列出 Plan B 每步的结果和阻塞。

## 首次启动实施时

补充证书部署、正式入口、权限与配置核验结果。凭据留部署配置，不写入规划文档。若资源未提供，记录具体阻塞并推进能独立完成的本地闭环工作；不能把本地模拟判为真机通过，也不能自动延后日期。

### 部署准备清单（2026-10-02，13-07 Task 1）

| 项 | 状态 | 记录 |
|---|---|---|
| Linux 部署二进制 | 就绪 | `bin/home-tutor-agent-linux`（ELF 64-bit x86-64 静态链接 stripped，14,549,152 字节；sha256 `4eeb0595ff6e79a3e70e921fb0efbb08dcd9ce6e21f7795fa04438d91e05b5d4`）。构建命令 `CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags '-s -w'`，与 Makefile build 的 CGO_ENABLED=0 契约一致；`bin/` 已被 .gitignore 排除，产物不入库 |
| 证书 certFile/keyFile 路径 | **待提供**（Task 2 VPS 段） | 用户现有证书资产：VPS 上 fullchain.pem 与 key.pem 的绝对路径、到期日、覆盖域名（SAN） |
| 访问域名（rt.allowedOrigins 值） | **待提供**（Task 2 VPS 段） | 必须与证书一致；`rt.allowedOrigins` 配置该域名（host，可含端口），禁 `*` |
| VPS 目标 | **待提供**（Task 2 VPS 段） | 用户 1C1G VPS（systemd 部署，按 docs/DEPLOYMENT.md 流程；下文命令清单假定 /opt/hta 布局，实际布局不同时等价替换） |
| 本地预检 | 全绿 | `go test ./...` 15 包 ok（本机无 gcc，`-race` 不可用——Makefile test 的 race 档位在 VPS/CI 侧执行）；`npm test` ok；`npx playwright test -c playwright.rt.config.js` 3 passed；`npx playwright test --list` 7 项不含 rt-loop（存量套件隔离复检 H-1） |
| 时序分析工具 | 就绪 | `scripts/rt-timing-report.js`（`--self-test`/`--json`，第六周判定口径：clock_offset 中位数校准 + ±RTT/2 误差界 + 无 offset 样本单独计数）；package.json 新增 `rt-report` script |

### VPS 部署命令清单（Task 2 执行；`<占位符>` 由用户替换；凭据不进对话与 git）

约定：`<VPS>` = `用户名@主机`；`<DOMAIN>` = 证书覆盖的访问域名；`<CERT_DIR>` = 证书目录（如 `/etc/letsencrypt/live/<DOMAIN>`）。服务布局沿用 docs/DEPLOYMENT.md（/opt/hta + systemd `home-tutor-agent`）。

1. 本机（仓库根）上传二进制与静态资源：
   ```sh
   scp bin/home-tutor-agent-linux <VPS>:/tmp/service.new
   scp -r pkg/web/static <VPS>:/tmp/static.new
   ```
2. VPS 上放置证书（已在 VPS 的 Let's Encrypt 等证书可跳过；自带证书先上传到 <CERT_DIR>）：
   ```sh
   ls -l <CERT_DIR>/fullchain.pem <CERT_DIR>/privkey.pem   # 两文件必须存在且可读
   sudo chmod 600 <CERT_DIR>/privkey.pem
   ```
3. VPS 上编辑 `/opt/hta/config.yaml`：`server.addr` 改为 `"0.0.0.0:443"`、`server.staticDir` 确认为 `/opt/hta/pkg/web/static`，并加入两个块（其余段落不动；`MIMO_API_KEY` 继续留在 `/opt/hta/secrets.env`，绝不写入 config）：
   ```yaml
   server:
     addr: "0.0.0.0:443"
     staticDir: /opt/hta/pkg/web/static
     tls:
       certFile: <CERT_DIR>/fullchain.pem
       keyFile: <CERT_DIR>/privkey.pem
   rt:
     allowedOrigins:
       - <DOMAIN>
   ```
   （`rt.lessonId`/`rt.lessonWords` 不填即取默认 r0-test 三词「美丽/勇敢/聪明」。）
4. VPS 上切换文件并重启：
   ```sh
   sudo systemctl stop home-tutor-agent
   sudo install -m 0755 /tmp/service.new /opt/hta/bin/service
   sudo rm -rf /opt/hta/pkg/web/static && sudo cp -r /tmp/static.new /opt/hta/pkg/web/static
   sudo systemctl start home-tutor-agent
   systemctl status home-tutor-agent --no-pager | head -5
   sudo journalctl -u home-tutor-agent -n 20 --no-pager
   ```
   日志应出现 `"tls":true` 与 `static interface directory verified`；若报 `TLS certificate file not accessible`，回查第 2 步路径。
5. VPS 本机核验：
   ```sh
   curl -sI https://<DOMAIN>/tutor/        # 期望 HTTP 200（2xx）
   curl -s http://127.0.0.1:8081/health    # 期望健康 JSON
   ```
   云安全组/防火墙需放行 443 入站。
6. 真 iPad Safari 打开 `https://<DOMAIN>/tutor/`：地址栏锁标可信（无告警页）、页面显示状态与开始按钮；点「开始」一次完成麦克风授权 → 听到老师首问候；用页面导出功能存一份 `rt-timing-*.jsonl` 备用。
7. 若页面正常但 wss 连不上：按 docs/DEPLOYMENT.md 排查项先试禁 h2（`cmd/service/main.go` 的 ListenAndServeTLS 处把 `TLSNextProto` 置空 map）再重部署。
8. 回报内容：访问域名、证书到期日、iPad 型号/iPadOS/Safari 版本、每步结果（或失败第几步+报错原文）、导出的 JSONL 文件名——记入下方检查记录（不含任何密钥/私钥内容）。

### 本机预验收通道与执行顺序决策（2026-10-03，13-07 Task 2 重定范围）

用户决定（2026-10-03 checkpoint 回复：先在本机执行验收完成后再部署到 VPS 主机）：Task 2 拆为「本机预验收 → VPS 正式部署」两段。VPS 命令清单（上节）不变，推迟到本机预验收通过后执行。

**1. 本机预验收通道（已就绪并核验，2026-10-03）**

- 入口：`https://<本机 WLAN IPv4>:8443/tutor/`（WLAN 物理适配器地址，记录于本机 `config.local.yaml`，按 T-13-06d 内网地址不落档；WSL/Hyper-V 虚拟适配器地址不适用）。
- 证书：mkcert 本地 CA + 服务器证书（SAN：本机 WLAN IPv4 / 127.0.0.1 / localhost；有效期至 2029-01-03）。证书与私钥在 `.local-certs/`（已加 .gitignore，私钥与 CA 私钥永不入库）；iPad 侧需安装并完全信任 mkcert 根证书（rootCA.pem 为公开证书，经本机 `http://<本机 WLAN IPv4>:8765/` 下发，该目录仅含公开根证书副本）。
- 配置：`config.local.yaml`（gitignored）——`server.addr 0.0.0.0:8443` + `server.tls`（mkcert 证书两路径）+ `rt.allowedOrigins` 增加本机 LAN 主机条目（bare host；/rt 端自动扩展 scheme 并端口宽容匹配）；MiMo 等密钥仍走 `.env` 活进程环境注入（D-05），未落入任何文件。
- 启动与防火墙：Windows 构建 `bin/service.exe -config config.local.yaml`（与 8080 明文开发实例可并行）；防火墙无需新增规则——既有 `bin\service.exe` 与系统 python312 的 Public 配置文件放行规则（program 级、任意端口）已分别覆盖 8443 与 8765 入站。
- 本机核验（2026-10-03，node fetch / ws 客户端）：`/tutor/` HTTPS 200；`/health` 健康 JSON；`/rt` WSS 握手（TLS + 升级 + Origin 白名单校验）通过；服务日志含 `"tls":true` 与 `rt realtime tutor providers enabled`（MIMO 密钥经 .env 生效）。

**2. 证据口径（本机预验收 ≠ 正式验收）**

- 功能预验收（首问候、打断、3 词场景、连续 10 轮、故障可见性）可在本机通道用真 iPad + 真麦克风先行验证，问题先暴露先修；结果如实标注「本机预验收」。
- 正式 RT-01 可信入口证据、≥30 时序样本与检查记录最终落档仍以 VPS 部署为准（清单见上节）；本机 LAN 时序样本仅用于验证 rt-timing-report 报告管线，不作为第六周判定证据。

## Plan B 执行状态

未触发、未实现。触发后依 [书面方案](../../../docs/REALTIME-TUTOR-PLAN-B.md) 顺序执行。没有调度自动提醒；每个后续执行会话必须检查当前日期和证据。
