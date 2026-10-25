---
phase: quick
plan: 261024-njo
subsystem: scripts
tags: [powershell, windows-deployment, service-control, ps51]
requires: []
provides:
  - "scripts/hta.ps1（安装目录日常启停单文件：start/stop/status；status 0/1 exit 机器契约）"
affects:
  - "261024-njn（install.ps1：把本文件原样拷入安装目录）"
  - "261024-njp（deploy.ps1：调用 start 并以 status exit 契约做启动后健康门）"
key-files:
  created:
    - scripts/hta.ps1
  modified: []
tech-stack:
  added: []
  patterns:
    - "WMI Win32_Process.Create 斩断调用链句柄继承的后台拉起（PS 5.1 下替代 start-detached.mjs 的等价物）"
    - "cmd /d /c 相对路径双文件重定向（CurrentDirectory=安装目录，零引号）"
    - "TCP 表属主 G1 身份判定（service.exe 名或 bin\\ 前缀路径）"
decisions:
  - "start 拉起弃计划原文的 Start-Process，改 Win32_Process.Create + cmd /c 重定向——冒烟实证 Start-Process 重定向必开句柄继承、把调用链 stdio 管道泄给孙进程，捕获输出的调用方永久挂死（run.ps1 引入 start-detached.mjs 的同一坑）；就绪门相应从 PID 相等改为 G1 身份判定（清场后首个过守卫的健康端口属主即本次拉起）"
  - "Get-HtaPortOwnerPids 平数组返回（弃 ,@() 包装）、start/stop 跨端口一次性快照去重、死 PID=幂等成功、stop 终判只统计活属主——冒烟 S6f 实证 ,@() 在 @(fn) 收集语境不展开（.Count 恒 1 + Object[] 漏进 -Id）与按端口循环撞陈旧监听行两缺陷"
  - "LAN IPv4 缺失时入口 URL 退化为 https://<LAN-IP>:<port>/tutor/ 占位并黄字提示，保证 /tutor/ 入口行恒打印"
metrics:
  duration: 2026-10-24 17:32 → 2026-10-25 09:55（北京时间；含配额中断与两轮外部门禁往返等待）
  completed: 2026-10-25
  tasks: 2
  files: 1
status: complete
commits: 1
plan_head_before: 40ab6dd7ad4b11289d8807edd3f03fc8eaaef46e
plan_head_after: 7d625a605c66a191e59fdd27b19b707dfee64ffa
---

# Quick 261024-njo: 部署安装目录日常启停脚本 hta.ps1 Summary

Windows 安装目录单文件启停脚本 scripts/hta.ps1（447 行）：start 一条命令完成 .env 注入＋WMI 斩链后台拉起＋G1 就绪门＋打印 iPad 入口 URL；stop 幂等停服（守卫拒杀外来占用者）；status 健康探活＋进程查询＋0/1 exit 机器契约。真实 go build service.exe 七步冒烟全绿。

## 交付物与结构

**scripts/hta.ps1（447 行，本项唯一 git 改动，commit 7d625a6）**，自上而下：

| 区段 | 内容 |
|------|------|
| 头注释 | 用途、三用法示例、端口来自 config\config.yaml、由 scripts\install.ps1 拷入安装目录、5.1 兼容声明 |
| param | `[ValidateSet('start','stop','status')]`，缺省只读 status |
| Convert-HtaHostPort / Read-HtaServerConfig | 行式解析＋顶层块跟踪（`^([A-Za-z_]\w*)\s*:\s*$`），仅 server 块取 addr/healthAddr，剥引号、按最后冒号切分、端口 1..65535 强校验；产出 PublicPort/HealthHost/HealthPort；全文无 Invoke-Expression、无硬编码端口 |
| Import-HtaEnv | secrets\.env → .env 回退；run.ps1 同款逐行解析（export 容忍/引号剥离/'Process' 作用域）；打印 `loaded N var(s) from <path>`，值不回显；两者皆缺黄字警告继续 |
| Get-HtaPortOwnerPids / Get-HtaProcessLabel / **Test-HtaOursToKill（G1 守卫，L163）** / Get-HtaGuardedPortOwner / Get-HtaLanIPv4 | 端口属主去重快照；**G1：待杀 PID 须为 service.exe 名或本安装 bin\ 前缀路径（OrdinalIgnoreCase），否则外来占用者拒杀** |
| Invoke-HtaStart | bin\service.exe 前检（缺→红字提示先跑 deploy.ps1）→ logs 目录自建 → Import-HtaEnv → 跨端口快照守卫清场（死 PID 跳过）→ 双日志截断 → WMI Win32_Process.Create 后台拉起（cmd /d /c 相对路径双文件重定向、ShowWindow=0 隐藏）→ 20×500ms G1 就绪门（健康端口属主过守卫；包装 cmd 死亡且无监听=早败信号）→ 绿字报 service PID＋/health 探针 URL＋iPad/parent 入口（LAN IPv4 排除 127.*/169.254.* 取首条）；超时 tail 5 行 err 日志＋补杀 bin 前缀残余＋补杀残留包装 cmd→exit 1 |
| Invoke-HtaStop | 跨端口快照去重逐 PID：死 PID=幂等跳过；活属主过 G1 守卫→黄字杀；活外来者→黄字拒杀；bin 前缀 Win32_Process 清扫；终判只统计活属主，空→绿字（含 nothing to stop 分支）exit 0，非空→红字 exit 1 |
| Invoke-HtaStatus | 报端口与入口 URL；/health 探活（`-UseBasicParsing -TimeoutSec 3`）200 绿/否则红；双端口监听属主 PID＋进程名；bin 前缀 service.exe 清单（PID＋路径）；末行 `STATUS: healthy`/`STATUS: unhealthy`；**200→exit 0，否则 exit 1（njp 复用契约）** |

## 冒烟证据（用户亲跑，七步全绿，SMOKE_PASS）

- 驱动：%TEMP%\hta-smoke-261024-njo.ps1；S1-S5 脚手架＋真实 `go build -o <staged>\bin\service.exe ./cmd/service`（**1893 ms**）＋hta.ps1 拷贝落位＋部署形 config＋secrets\.env 单变量。
- 端口对：**18080（公网）/ 18081（健康）**，与开发 8080/8443/8081 天然错开（S6h 隔离项）。
- S6a status(down) → exit 1，含 unhealthy
- S6b start → exit 0，输出含 `loaded 1 var(s)`、健康探针 URL、**iPad 入口 `https://172.30.240.1:18080/tutor/`**、parent 入口（服务约 1 秒就绪）
- S6c 健康端口属主：PID 23204 → `...\hta-smoke-2236f48b\bin\service.exe`（本安装 bin 前缀）
- S6d status(up) → exit 0，双端口 listener 报告＋`STATUS: healthy`
- S6e 双日志在位（server.err.log 1788 bytes，config log.output=stderr）
- S6f stop → exit 0，**18080/18081 双端口释放**
- S6g status(down again) → exit 1
- S7 清理完成（staged 树删除；驱动留 %TEMP% 不进仓库）
- go build 产物路径：<staged>\bin\service.exe（临时，已随 S7 删除）；构建耗时 1893 ms

## 门禁迭代记录（三轮，均如实记录于各版 commit message）

| 版本 | 门禁结果 | 缺陷与修复 |
|------|----------|------------|
| 76d6268 | parse FAIL（4 条） | L369 StartsWith 缺自身右括号（误写 `}))`）→ 补 `) })`，复核全部 10 处方法调用点 → c195c67 |
| c195c67 | parse PASS；冒烟 S6b 挂死 | Start-Process 重定向句柄继承泄调用链管道，捕获型调用方永久阻塞 → 改 WMI Win32_Process.Create＋cmd /d /c 重定向，就绪门 PID→G1 身份 → 5620b63 |
| 5620b63 | 冒烟 S6a-S6e 过，S6f FAIL＋崩溃 | `,@()` 包装在 @(fn) 收集语境不展开（.Count 恒 1＋Object[] 漏进 -Id）；按端口循环撞死 PID 陈旧监听行误判拒杀 → 平数组返回＋跨端口快照＋死 PID 幂等成功＋终判只算活属主 → **7d625a6（最终）** |
| 7d625a6 | **PARSE_OK＋SMOKE_PASS 全七步** | — |

## njn / njp 契约重申

- **njn（install.ps1）**：把本仓 `scripts/hta.ps1` **原样拷贝**到安装目录根（与 bin\、config\、secrets\、logs\ 同级）；脚本以其自身所在目录为安装目录锚定，无需改写任何路径；部署端口由 install.ps1 生成并避开 8080/8443/8081，hta.ps1 只认 config 解析值。
- **njp（deploy.ps1）**：升级后调用安装目录 `hta.ps1 start`（可捕获输出——WMI 拉起已保证调用方管道不被孙进程持有）；启动后健康门直接跑 `hta.ps1 status` 并以 **exit 0/1** 为准（200→0，否则 1），勿解析文本。stop 幂等可安全重复调用。
- 运行前提：安装目录内 `bin\service.exe`（njp 构建落位）、`config\config.yaml`（njn 生成）、`secrets\.env`（njn 落密钥，缺则黄字降级）。

## Deviations from Plan

**[Rule 1 - Bug] start 拉起方式偏离计划原文（Start-Process → WMI Win32_Process.Create）**
- **发现于**：Task 2 冒烟 S6b（用户亲跑，驱动挂死、service.exe 实际就绪）
- **问题**：计划规定的 `Start-Process -RedirectStandardOutput/-RedirectStandardError` 在 PS 5.1 下必须开启句柄继承才能传文件句柄，连带把调用链 stdio 管道泄给 service.exe——正是 run.ps1 注释与 start-detached.mjs 存在所要避免的坑；与计划自身的 must-have「捕获输出的调用方（njp deploy.ps1）能拿到返回」直接冲突
- **修复**：Win32_Process.Create（WmiPrvSE 代生，句柄链零传递）＋cmd /d /c 相对路径双文件重定向（日志契约不变）；就绪门由 PID 相等改为 G1 身份判定（等价：清场后首个过守卫属主即本次拉起）
- **文件**：scripts/hta.ps1；**Commit**：5620b63（现收敛于 7d625a6）

**[Rule 1 - Bug] stop 幂等语义与 ,@() 包装缺陷**
- **发现于**：Task 2 冒烟 S6f（stop exit 1＋ParameterBindingArgumentTransformationException）
- **修复**：平数组返回＋跨端口一次性快照＋死 PID 幂等成功＋终判只统计活属主
- **文件**：scripts/hta.ps1；**Commit**：7d625a6

其余执行形态适配（非计划偏差）：冒烟驱动与 parse 门按批次分工由主会话/用户在隔离外代跑（本执行会话的 worktree 隔离守卫拒绝 powershell 执行）；Task 2 verify 的 `git status --porcelain scripts/hta.ps1` 非空断言因 Task 1 已先行提交改为等价的"该 item 范围树干净"断言。

## 最终 git 状态

```
$ git log --oneline -2
7d625a6 feat(quick-261024-njo): 新增部署安装目录日常启停脚本 scripts/hta.ps1（start/stop/status）
40ab6dd WIP(checkpoint): quick-batch 261024-njm 规划完成（...）
$ git status --porcelain
（空——本项仅 scripts/hta.ps1 一个文件，已提交）
$ git show --stat HEAD
 scripts/hta.ps1 | 447 ++++++++++++++++++++++++++++++++++++++++++++++++++++++++
 1 file changed, 447 insertions(+)
```

## Self-Check: PASSED

- scripts/hta.ps1 存在（447 行）：FOUND
- 提交 7d625a605c66a191e59fdd27b19b707dfee64ffa 存在于分支 worktree-agent-a5ec6bcb77b08c595：FOUND
- Parse gate PARSE_OK（主会话，对 7d625a6）：外部证据
- SMOKE_PASS ports=18080/18081 build_ms=1893（用户亲跑，七步全绿）：外部证据
- 无 Known Stubs（脚本全量接线并经真实 service.exe 行为验证）；无新增计划外安全面（威胁登记 T-quick-njo-01/02/03 缓解均已落地）
