# Deployment Guide

Operational reference for running the Home Tutor Agent service in local
development and VPS production environments.

## Working Directory Requirement (Critical)

The service loads its configuration via a **relative path** (`-config config.yaml`).
The Go process **MUST run from the repository root** so that:

1. `config.yaml` is found (otherwise `LoadConfig` silently falls back to
   `DefaultConfig`, which has `staticDir: ""` and **disables the student
   interface**).
2. `server.staticDir: ./pkg/web/static` (relative) resolves to the correct
   directory.

### How to start correctly

| Environment | Command | Why it works |
|-------------|---------|--------------|
| Linux/macOS dev | `make run` | `make` invokes `go run` from the repo root |
| Windows dev | `./run.ps1` | Script sets `Set-Location` to repo root first |
| VPS (systemd) | systemd unit with `WorkingDirectory=` (see below) | systemd sets CWD before exec |

### How NOT to start

- `cd /tmp && /opt/hta/service` — CWD is `/tmp`, config.yaml not found,
  student interface silently disabled.
- Running from an IDE that sets the working directory to a subfolder.
- Building with `go build` then running the binary from a different directory
  without passing an absolute `-config` path.

## Configuration

### config.yaml

`config.yaml` is **gitignored** (machine-specific paths). Bootstrap it from
the tracked template:

```sh
make config-init          # copies config.example.yaml to config.yaml
# OR
cp config.example.yaml config.yaml
```

Then edit `python.interpreter` to point at your venv.

### Key settings for the student interface

```yaml
server:
  staticDir: ./pkg/web/static    # MUST be set or interface is disabled
  staticRoute: /                  # URL prefix for static files
```

For production, prefer an absolute path:

```yaml
server:
  staticDir: /opt/hta/pkg/web/static
```

### Verifying correct startup

Check the service logs for these two lines:

```
{"msg":"static interface directory verified","dir":"./pkg/web/static"}
{"msg":"public server static files mounted","route":"/","dir":"./pkg/web/static"}
```

If you do NOT see these lines, `staticDir` is empty — the service is serving
debug test pages instead of the student interface.

Quick HTTP check:

```sh
curl -s -o /dev/null -w "%{http_code}" http://localhost:8080/styles/main.css
# Expected: 200
```

## VPS Deployment (systemd)

```ini
[Unit]
Description=Home Tutor Agent
After=network.target

[Service]
Type=simple
User=hta
# CRITICAL: WorkingDirectory must be the repo root so config.yaml resolves.
WorkingDirectory=/opt/hta
ExecStart=/opt/hta/bin/service -config config.yaml
Restart=on-failure
RestartSec=5

# Environment variables for secrets (never in config.yaml — D-05)
EnvironmentFile=/opt/hta/secrets.env

# Resource limits (1C/1G VPS)
MemoryMax=200M
LimitNOFILE=4096

[Install]
WantedBy=multi-user.target
```

**Alternative:** if you cannot guarantee `WorkingDirectory`, pass an absolute
config path instead:

```ini
ExecStart=/opt/hta/bin/service -config /opt/hta/config.yaml
```

And set `server.staticDir` to an absolute path in `config.yaml`.

## HTTPS / TLS 与 /tutor/ 入口 (Phase 13, RT-01)

The public listener terminates TLS in-process (Go `ListenAndServeTLS` — no
nginx/caddy sidecar on the 1C1G VPS). The certificate is the owner's
existing asset; the service only needs the two file paths.

### server.tls configuration

```yaml
server:
  addr: 0.0.0.0:443
  staticDir: /opt/hta/pkg/web/static
  tls:
    certFile: /etc/letsencrypt/live/tutor.example.com/fullchain.pem
    keyFile: /etc/letsencrypt/live/tutor.example.com/privkey.pem
```

Rules (enforced at boot, no silent downgrade):

- No `tls:` block → plain HTTP (localhost dev; `getUserMedia` works on
  localhost as a secure-context exception).
- `tls:` present with BOTH paths → HTTPS. A missing/inaccessible cert file
  aborts startup with a clear log line (`TLS certificate file not
  accessible`) and exit code 1 — the trusted entrance NEVER silently falls
  back to HTTP.
- An empty `tls: {}` block or a half pair (cert without key) is a config
  validation error.

### rt.allowedOrigins — production MUST list the certificate domain

```yaml
rt:
  allowedOrigins:
    - tutor.example.com   # the domain on the certificate (host, optionally host:port)
```

The default `["localhost", "127.0.0.1"]` is dev-only; the /rt WebSocket
rejects every other Origin. The wildcard `"*"` is forbidden at both the
config and transport layers.

### FSRS 词源前提 (Phase 13, 13-10)

The /rt dictation tutor's words are NOT configured in the service — every
session fetches due words from the FSRS vocabulary store through the python
bridge, and learning records are written back the same way. The VPS
therefore needs THREE prerequisites beyond the Go binary:

1. **Python 词库环境** — the venv interpreter + `third_party/python/`
   scripts, configured via the `python:` block (`interpreter`,
   `scriptsDir`, `dataDir`, and `vocabDBPath` pointing at the vocab SQLite
   database).
2. **vocab DB with due words** — an empty/due-less store is legal: the
   session ends with the honest `no_due_words` announcement, not an error.
3. **MIMO_API_KEY** (via `/opt/hta/secrets.env`) — without it the tutor
   degrades to `voice_unavailable` (see Troubleshooting below).

#### rt 配置键（13-10 转向后）

- `rt.lessonWords` **已移除** — 词永远来自 FSRS 到期获取。旧 config.yaml
  若仍携带该键会被静默忽略（viper 对未知键不报错），无需手工清理。
- `rt.lessonId` 默认**留空** = 服务端按北京时间每日派生 `r0-YYYYMMDD`，
  同日重连自动续同一课次；非空则原样使用（专用课堂/测试）。设置时禁止
  空白字符与路径分隔符，长度 ≤64。
- `rt.wordTimeoutMs` 每词书写计时（毫秒），到点自动推进下一词。默认
  60000（60 秒）；真机标定可缩短验证。负数或超过 3600000 拒绝启动。

#### 脚本核验（部署时执行一次并记录）

```sh
# 1) 实际部署脚本必须支持显式取词锁窗口：
/opt/hta/venv/bin/python /opt/hta/third_party/python/vocab_manager.py due --help | grep -- --fetch-lock-hours
#    期待输出包含 --fetch-lock-hours；缺失 = 部署脚本过旧，必须先升级脚本再上线。

# 2) 记录实际脚本指纹（与 vendored 副本核对）：
sha256sum /opt/hta/third_party/python/vocab_manager.py
```

仓库内 vendored 脚本的 24h 默认锁窗口只是**源码事实**——R0 生产调用由
服务端显式传 `--fetch-lock-hours 1`（Go 侧 `DueOpts.FetchLockHours=1`），
不依赖脚本默认值。

**取词锁风险边界**（为何是 1h）：锁的残余影响集中在两处——RunDue 成功但
建课前失败留下的已锁批次，以及跨进程并发创建产生的未使用批次。普通场景
（同 lessonID 重连）复用已存的 Cards，不会再次 RunDue。进程崩溃的残余锁
在 1h 后自动回队；按卡精确解锁/持久恢复留待 Phase 15。

#### 排障：session_init 返回 lesson_unavailable

- **先查 python 词库环境**：`vocabDBPath` 指向的数据库是否存在且可读、
  venv 解释器是否可用（服务日志中 pybridge 调用错误会带结构化字段）。
- **legacy 空 Cards**：13-06 期间创建的旧课堂行没有词卡且**不会**被
  伪填充——使用每日派生 `r0-YYYYMMDD` 或换一个 `rt.lessonId` 即可。
- python 侧失败（超时/非零退出/JSON 解析失败）不会崩溃服务：会话如实
  收到 `lesson_unavailable`，细节只在服务端结构化日志。

### Certificate handling (hard rule)

The certificate and private key NEVER enter git, `.planning/`, or any
documentation — they live only in the VPS deployment config above
(user convention / threat T-13-06). Tests use throwaway self-signed
certificates generated in temp directories.

### systemd — no changes needed

Same binary, same unit file as above. Only `config.yaml` changes (add the
`server.tls` block; point `addr` at 443). Restart picks it up:

```sh
sudo systemctl restart home-tutor-agent
```

### Post-deploy verification

```sh
# On the VPS itself:
curl -I https://tutor.example.com/tutor/        # expect HTTP 200
curl -s http://127.0.0.1:8081/health            # expect {"status": ...}

# iPad Safari: open https://tutor.example.com/tutor/
#   - padlock icon visible (real cert, not a warning page)
#   - one tap starts the session (mic permission prompt once)
```

### Troubleshooting

- **Page loads over HTTPS but the wss connection fails immediately:**
  first suspect is h2 (Pitfall 8). Disable h2 by setting
  `http.Server.TLSNextProto` to an empty non-nil map (one-line fix in
  `cmd/service/main.go` at the ListenAndServeTLS site) and retry.
- **Tutor page loads but shows "语音不可用" (voice unavailable):** that is
  the DESIGNED degraded state, not a fault — `MIMO_API_KEY` is not set in
  `/opt/hta/secrets.env`. The `/rt` endpoint stays mounted and
  `session_init` answers `voice_unavailable` so the page can explain why
  (RT-10 transparency). Add the key and restart to enable voice.

## Troubleshooting

### Static files return 404 (student interface doesn't load)

**Symptom:** The HTML page loads but CSS/JS files return 404. The page looks
unstyled.

**Root cause:** `server.staticDir` is empty in the loaded config. The service
is falling back to the debug root handler (`serveDebugRoot`) which serves
`test-interface.html` — a debug page whose asset references do not exist.

**Fix:**
1. Verify you are running from the repo root: `pwd` should show the repo path.
2. Verify `config.yaml` exists and has `server.staticDir: ./pkg/web/static`.
3. Check service logs for "static interface directory verified".
4. If missing, run `make config-init` and restart.

### Service serves test-interface.html instead of student interface

Same root cause as above. The student interface (`pkg/web/static/index.html`)
is only served when `staticDir` is configured. Without it, the debug fallback
serves test-interface.html.

### Config changes not taking effect

The service loads config once at startup (D-19). There is no live reload —
**restart the process** to pick up new values:

```sh
sudo systemctl restart home-tutor-agent
```
