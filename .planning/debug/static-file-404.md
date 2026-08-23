---
status: fixing
trigger: "Phase 08 UAT - Static File Serving 404 errors for CSS/JS files. HTML loads correctly from :8080 but subresources (js/app.js, styles/main.css) return 404. Config shows staticDir: "./pkg/web/static", staticRoute: "/". Files exist on disk."
created: 2026-08-10T00:00:00Z
updated: 2026-08-10T00:19:00Z
---

## Current Focus

hypothesis: ROOT CAUSE CONFIRMED - Service running from wrong working directory (temp Go build dir), config not found, using DefaultConfig() with StaticDir="" which disables static serving
test: Root cause confirmed via evidence
expecting: Fix is to restart service from correct working directory
next_action: Provide diagnosis and fix recommendation to user

## Symptoms

expected: Navigation to server URL loads student interface correctly, CSS/JS files served with correct MIME types
actual: HTML page loads correctly (Content-Type: text/html; charset=utf-8), but CSS and JS files return 404 errors
errors: 404 errors for js/app.js and styles/main.css
reproduction: Navigate to http://server:8080/, observe browser dev tools network tab showing 404s for CSS/JS resources
started: Phase 08 UAT testing (major issue)

## Eliminated

## Evidence

- timestamp: 2026-08-10T00:10:00Z
  checked: Static file server implementation (pkg/infra/static.go)
  found: StaticFileServer wraps http.FileServer with MIME type registration and cache headers. Uses http.FileServer(http.Dir(dir)) where dir is the configured static directory.
  implication: Implementation looks correct for serving files from a directory.

- timestamp: 2026-08-10T00:10:30Z
  checked: Static file mounting in cmd/service/main.go line 382
  found: publicMux.Handle(cfg.Server.StaticRoute, infra.StaticFileServer(cfg.Server.StaticDir, logger)) where StaticRoute="/" and StaticDir="./pkg/web/static"
  implication: Mount point is "/", directory is "./pkg/web/static"

- timestamp: 2026-08-10T00:11:00Z
  checked: File system structure
  found: Files exist at pkg/web/static/index.html, pkg/web/static/js/app.js, pkg/web/static/styles/main.css with correct permissions (644)
  implication: Not a file system permission issue

- timestamp: 2026-08-10T00:11:50Z
  checked: Actual HTTP responses from server
  found: "/" returns 200 OK but serves test-interface.html (57KB debug interface), not pkg/web/static/index.html (5.2KB student interface). "/styles/main.css" and "/js/app.js" return 404.
  implication: Wrong handler is mounted - serving debug interface instead of student interface

- timestamp: 2026-08-10T00:12:30Z
  checked: Running process details (PID 10044)
  found: Process is C:\Users\fortu\AppData\Local\Temp\go-build2890987147\b001\exe\service.exe (temp Go build directory) with command line `-config config.yaml`
  implication: Service is running from temp directory, not repo root

- timestamp: 2026-08-10T00:15:00Z
  checked: LoadConfig behavior in pkg/infra/config.go lines 714-729
  found: LoadConfig checks os.Stat(configPath) and silently skips config file if not found, falling back to DefaultConfig()
  implication: If config.yaml doesn't exist in working directory, default config is used

- timestamp: 2026-08-10T00:16:00Z
  checked: DefaultConfig() in pkg/infra/config.go lines 585-684
  found: DefaultConfig() has StaticDir: "" (line 619), which means static file serving is DISABLED by default
  implication: Service is using default config with StaticDir="", causing fallback to debug interface

- timestamp: 2026-08-10T00:17:00Z
  checked: main.go fallback logic lines 394-396
  found: When cfg.Server.StaticDir == "", main.go uses publicMux.HandleFunc("/", serveDebugRoot) instead of mounting StaticFileServer
  implication: This explains why test-interface.html is served instead of student interface

## Resolution

root_cause: Service started from wrong working directory (temp Go build dir instead of repo root). Config file not found at relative path, so DefaultConfig() used with StaticDir="" (static serving disabled). Debug interface serves at "/" but doesn't have CSS/JS files, causing 404s for subresources.

fix: Start service from repo root (D:\workspace\ai\xiaotian) or use absolute path to config file. Ensure working directory is correct when running service.

verification: Restart service with correct working directory and verify / serves pkg/web/static/index.html with correct CSS/JS subresources.

files_changed: []
