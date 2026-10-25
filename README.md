# Home Tutor Agent

> **2026-09-30：当前产品验收未通过。** 最终目标是浏览器中的自然实时家教交流；当前已有网页听写、家长词库和学习业务，语音主路径仍为按住录音后的有限指令识别。持续对话、上下文追问和自然打断尚未实现。最新事实、启动测量、选型建议及验收标准见 [项目复盘](docs/PROJECT-REVIEW-2026-09-30.md) 与 [已接受的实时家教架构决策](docs/adr/0002-browser-realtime-tutor.md)。

当前规划里程碑为 **v2.0 realtime-tutor**，R0～R3 对应 Phase 13～16，下一项 `/gsd-plan-phase 13`。默认自研实时交互＋MiMo，先 WebSocket、后 pion WebRTC；旧 v1.0 已被取代。日期检查和未达标切换条件见 [路线图](.planning/ROADMAP.md)，备用实现见 [书面 Plan B](docs/REALTIME-TUTOR-PLAN-B.md)。

A web-based real-time dictation tutor agent that provides natural, interactive learning experiences for elementary school students. The system uses conversational AI to deliver personalized vocabulary practice through voice dictation sessions, with parent feedback integration that drives FSRS (Free Spaced Repetition Scheduler) algorithm updates.

## Prerequisites

- Go 1.26+
- Make
- Git

## Build

```sh
make build
```

Produces a static binary at `bin/service`.

## Running the Service

The service loads `config.yaml` from the **current working directory**. You
MUST run it from the repository root so the config file and the relative
`staticDir` path resolve correctly.

### First-time setup

```sh
make config-init   # copies config.example.yaml to config.yaml (gitignored)
```

Edit `config.yaml` to set `python.interpreter` to your venv path.

For Windows, also set the Python script and data paths to actual local paths;
the `/opt/hta/...` paths in the example are deployment placeholders. Ensure
the Go database path and Python vocabulary database refer to the same file.
Configure the provider credentials from `.env.example` in a local `.env`.
`run.ps1` loads that file into the process environment; directly launching
the binary does not automatically load it.

### Start the service

```sh
make run           # Linux/macOS — runs from repo root
./run.ps1          # Windows — sets CWD to repo root automatically
```

### Verify the student interface loaded

Check the logs for `"static interface directory verified"`. Then open
`http://localhost:8080/` in a browser — the student dictation interface
should render with styling.

The parent interface is at `http://localhost:8080/parent.html`.
`test-interface.html` and related pages are developer diagnostics. On iPad,
microphone access requires a trusted HTTPS entry point; ordinary LAN HTTP
does not inherit the localhost exception. A loaded page and a connected
WebSocket do not establish that voice interaction is available.

> **Troubleshooting:** If the page loads without CSS/JS (404 errors), the
> service is not finding `config.yaml` or `server.staticDir` is empty. See
> [docs/DEPLOYMENT.md](docs/DEPLOYMENT.md) for detailed diagnosis.

## 安装与部署（Windows 本机）

上文 `run.ps1` 是仓库内的开发实例。本节面向把服务安装为本机常驻部署（独立安装目录），
使用 261024 批次三件套：[scripts/install.ps1](scripts/install.ps1) 一次性安装、
[scripts/hta.ps1](scripts/hta.ps1) 日常启停、[scripts/deploy.ps1](scripts/deploy.ps1) 升级/回滚。

### 一次性安装

在仓库根目录执行（四个参数都显式给出）：

```powershell
powershell -NoProfile -ExecutionPolicy Bypass -File ./scripts/install.ps1 -InstallDir D:/Apps/hta -DataDir <自定义数据目录> -LanIp <WLAN IP> -PyIndex <镜像>
```

- **`-LanIp` 必须显式传物理网卡地址（WLAN/以太网）**：自动探测可能选中 Tailscale
  （100.64.x 段）等虚拟网卡，iPad 将连不上（已实际踩坑）。
- `-PyIndex` 默认清华源 `https://pypi.tuna.tsinghua.edu.cn/simple`；被 403 拒绝时换
  阿里源 `https://mirrors.aliyun.com/pypi/simple/`（已实际踩坑）。
- 防火墙放行 9443 需要管理员权限执行一次 netsh；安装器没有管理员权限时会把命令打印出来
  让你自己跑：

```powershell
netsh advfirewall firewall add rule name="HTA Deploy 9443" dir=in action=allow protocol=TCP localport=9443
```

- **安装幂等且永不覆盖**：`secrets\`、`config\`、`certs\`、venv、数据目录已存在即保留，
  重跑安全。
- 装完手动补全 `<InstallDir>\secrets\.env`：`VOLCENGINE_TTS_*`（安装器已从
  `third_party/python/.env.example` 模板种子生成，填真实值即可）与 **`MIMO_API_KEY`**
  （模板不含此键，照仓库根 `.env.example` 手动添加——缺它语音家教降级）。

### 日常启停

在安装目录内运行（`hta.ps1` 由安装器拷到安装目录根部）：

```powershell
.\hta.ps1 start
.\hta.ps1 stop
.\hta.ps1 status
```

- `start` 启动后打印 iPad 入口 `https://<LanIp>:9443/tutor/`。
- `status` 探测 `/health`，退出码 0=健康 / 1=不健康（机器可读契约，
  `deploy.ps1` 的健康门复用同一契约）。
- **提权（管理员）终端启动的实例必须提权停止**：`stop` 经 WMI 拉起、继承调用方的权限
  级别（已实际踩坑）——建议统一用普通终端启停。

### 升级与回滚

在仓库根目录执行升级：

```powershell
powershell -NoProfile -ExecutionPolicy Bypass -File ./scripts/deploy.ps1 -InstallDir D:/Apps/hta
```

一键回退到上一版本：

```powershell
powershell -NoProfile -ExecutionPolicy Bypass -File ./scripts/deploy.ps1 rollback -InstallDir D:/Apps/hta
```

- 升级流程：本机构建 → 停旧 → 备份 `bin/web/python` 到 `versions\prev` → 换新 →
  启动并以退出码为健康门；健康门失败时打印一条可整行复制的回滚命令。
- **绝不触碰 config/secrets/certs/数据目录/venv**——写面仅限
  `bin\`、`web\`、`python\`、`versions\`。

### 首台 iPad 证书信任

如 iPad 提示证书不受信：先安装 `<InstallDir>\certs\ipad\` 下的根证书并信任，再访问。

### 与开发实例并存

部署实例占 9443（HTTPS 入口）/ 9444（健康）；开发实例 `run.ps1` 占
8080（HTTP）/ 8443（HTTPS）/ 8081（健康）。端口错开，互不干扰。

## Test

```sh
make test
```

Runs the full test suite with the race detector enabled. For a quick run:

```sh
make test-short
```

## Project Layout

- `cmd/service/` — Application entry point and classroom, voice, and photo handlers
- `pkg/infra/` — Shared infrastructure package (logging, config, resource management)
- `internal/` — Private test helpers (pytest shell fakes, wstest WebSocket test support)
- `pkg/web/` — Student/parent browser interfaces and REST API
- `pkg/session/`, `pkg/voice/`, `pkg/tts/`, `pkg/mimo/` — Current dictation and speech pipelines
- `pkg/database/`, `pkg/fsrs/`, `pkg/pybridge/` — Learning data and Python integration
- `pkg/ws/`, `pkg/crc16/` — WebSocket hub library and streaming audio frame checksums
- `pkg/rt/` — v2.0 realtime tutor session state machine
- `pkg/grading/`, `pkg/lesson/`, `pkg/ocr/` — Answer grading, classroom session records, photo OCR
- `third_party/python/` — Reused vocabulary, scheduling, and batch speech assets

## Dependencies

Current Go dependencies include Viper, Zap, gorilla/websocket, and modernc.org/sqlite;
see `go.mod` for exact versions. Node.js is used for frontend logic and Playwright
tests. Python supports the reused learning business logic, and the legacy MP3
conversion path requires ffmpeg.

## Versioning

Semantic versioning (D-35). Phase 1 ships as v0.1.0 (pre-1.0 since downstream WebSocket/audio phases will evolve APIs).

## Documentation

Run `go doc github.com/xiaotian/home-tutor-agent/pkg/infra` for API docs. README files complement godoc per the combined-docs approach (D-34).

## Build Tags

`//go:build` tags reserved for future conditional compilation (D-41). None needed in Phase 1.

## Phase Status

Historical work has progressed through the student interface and parent
vocabulary management. Completion of those components does not establish
the original natural realtime tutor experience. The next recommended
product milestone is R0: a complete browser voice conversation with context
and interruption, followed by integration with existing learning tools.
See [the product review](docs/PROJECT-REVIEW-2026-09-30.md#9-重新安排交付顺序).

The existing browser suite allows long session-preparation timeouts and
soft-skips some audible-frame checks. Passing it cannot substitute for
real iPad voice, latency, and interruption acceptance. Full browser flows
use configured learning data and providers; use isolated data for testing.
