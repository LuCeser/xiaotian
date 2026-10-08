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
