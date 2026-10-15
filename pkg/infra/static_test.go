package infra

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestDefaultStaticConfigDisabled asserts the Phase 8 static-serving default:
// StaticDir empty (disabled) so the service boots without an interface
// directory, and StaticRoute "/" so an operator who only sets server.staticDir
// immediately gets the student interface at the service root.
func TestDefaultStaticConfigDisabled(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.Server.StaticDir != "" {
		t.Errorf("Server.StaticDir = %q, want empty (disabled by default)",
			cfg.Server.StaticDir)
	}
	if cfg.Server.StaticRoute != "/" {
		t.Errorf("Server.StaticRoute = %q, want \"/\"", cfg.Server.StaticRoute)
	}
}

// TestStaticDirEmptyAccepted verifies that an empty StaticDir is accepted by
// Config.Validate (disabled state, Database.Path precedent). The service must
// boot without an interface directory.
func TestStaticDirEmptyAccepted(t *testing.T) {
	setRequiredPythonEnv(t)
	if _, err := LoadConfig(""); err != nil {
		t.Fatalf("LoadConfig returned error %v, want nil (empty StaticDir must be accepted)", err)
	}
}

// TestStaticRouteValidationWhenEnabled covers the route-format rules that
// activate ONLY when an operator opts into static serving (StaticDir != "").
func TestStaticRouteValidationWhenEnabled(t *testing.T) {
	cases := []struct {
		name      string
		staticDir string
		route     string
		wantErr   bool
	}{
		{name: "valid root route", staticDir: "./pkg/web/static", route: "/", wantErr: false},
		{name: "valid prefix route", staticDir: "./pkg/web/static", route: "/static/", wantErr: false},
		{name: "empty route rejected", staticDir: "./pkg/web/static", route: "", wantErr: true},
		{name: "non-slash prefix rejected", staticDir: "./pkg/web/static", route: "static", wantErr: true},
		{name: "exact ws collision rejected", staticDir: "./pkg/web/static", route: "/ws", wantErr: true},
		{name: "ws prefix collision rejected", staticDir: "./pkg/web/static", route: "/ws/x", wantErr: true},
		{name: "disabled ignores route", staticDir: "", route: "", wantErr: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := DefaultConfig()
			// DefaultConfig intentionally leaves the required Python fields
			// empty (they come from env at boot), but we are exercising
			// cfg.Validate directly. Populate them so the ONLY variable under
			// test is the static config.
			cfg.Python.Interpreter = "/opt/hta/venv/bin/python"
			cfg.Python.ScriptsDir = "third_party/python"
			cfg.Python.DataDir = t.TempDir()
			cfg.Server.StaticDir = tc.staticDir
			cfg.Server.StaticRoute = tc.route
			err := cfg.Validate()
			if tc.wantErr && err == nil {
				t.Fatalf("Validate returned nil, want error for %s", tc.name)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("Validate returned error %v, want nil for %s", err, tc.name)
			}
			if tc.wantErr && !errors.Is(err, ErrConfigInvalid) {
				t.Errorf("Validate error is not ErrConfigInvalid: %v", err)
			}
		})
	}
}

// TestStaticDirEnvOverride verifies the HTA_SERVER_STATICDIR env binding
// (Pitfall 6: every leaf MUST be bound explicitly or the env var is inert).
func TestStaticDirEnvOverride(t *testing.T) {
	setRequiredPythonEnv(t)
	t.Setenv("HTA_SERVER_STATICDIR", "/opt/hta/web")
	cfg, err := LoadConfig("")
	if err != nil {
		t.Fatalf("LoadConfig returned error %v, want nil", err)
	}
	if cfg.Server.StaticDir != "/opt/hta/web" {
		t.Errorf("Server.StaticDir = %q, want /opt/hta/web (env override)",
			cfg.Server.StaticDir)
	}
}

// TestStaticFileServerEmptyDirReturns404 asserts that a disabled (empty)
// static directory yields a handler that responds 404 rather than panicking.
// This is the safe degenerate path used by tests and disabled configs.
func TestStaticFileServerEmptyDirReturns404(t *testing.T) {
	h := StaticFileServer("", NewNoOpLogger())
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want %d (empty dir must 404)", rec.Code, http.StatusNotFound)
	}
}

// TestStaticFileServerServesFilesWithMimeTypes asserts the handler serves
// index.html, .css, and .js from the configured directory with the correct
// Content-Type. iOS Safari refuses to execute scripts served as text/plain,
// so the .js MIME type is the load-bearing assertion.
func TestStaticFileServerServesFilesWithMimeTypes(t *testing.T) {
	dir := t.TempDir()
	mustWriteFile(t, filepath.Join(dir, "index.html"),
		"<!doctype html><html><body>student</body></html>")
	mustWriteFile(t, filepath.Join(dir, "app.js"), "console.log('hi');")
	mustWriteFile(t, filepath.Join(dir, "style.css"), "body{color:red}")

	h := StaticFileServer(dir, NewNoOpLogger())

	cases := []struct {
		path        string
		wantInBody  string
		wantCT      string
		wantCTExact bool // .css/.js must match the registered type exactly
	}{
		{path: "/", wantInBody: "student", wantCT: "text/html", wantCTExact: false},
		{path: "/app.js", wantInBody: "console.log", wantCT: "text/javascript", wantCTExact: true},
		{path: "/style.css", wantInBody: "color:red", wantCT: "text/css", wantCTExact: true},
	}
	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tc.path, nil)
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200", rec.Code)
			}
			body, _ := io.ReadAll(rec.Body)
			if !strings.Contains(string(body), tc.wantInBody) {
				t.Errorf("body %q does not contain %q", string(body), tc.wantInBody)
			}
			ct := rec.Header().Get("Content-Type")
			if tc.wantCTExact {
				if !strings.HasPrefix(ct, tc.wantCT) {
					t.Errorf("Content-Type = %q, want prefix %q", ct, tc.wantCT)
				}
			} else {
				if !strings.Contains(ct, tc.wantCT) {
					t.Errorf("Content-Type = %q, want to contain %q", ct, tc.wantCT)
				}
			}
		})
	}
}

// TestStaticFileServerHTMLNoCacheHeader asserts HTML entry documents get a
// no-cache header (so deploys reach students on shared iPads) while .js/.css
// assets do not (so they keep benefiting from ETag-based 304s).
func TestStaticFileServerHTMLNoCacheHeader(t *testing.T) {
	dir := t.TempDir()
	mustWriteFile(t, filepath.Join(dir, "index.html"), "<!doctype html><p>x</p>")
	mustWriteFile(t, filepath.Join(dir, "app.js"), "var x=1;")

	h := StaticFileServer(dir, NewNoOpLogger())

	// HTML → no-cache
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if got := rec.Header().Get("Cache-Control"); got != "no-cache, must-revalidate" {
		t.Errorf("HTML Cache-Control = %q, want %q", got, "no-cache, must-revalidate")
	}

	// JS → no forced no-cache (asset caching preserved)
	req = httptest.NewRequest(http.MethodGet, "/app.js", nil)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if got := rec.Header().Get("Cache-Control"); got == "no-cache, must-revalidate" {
		t.Errorf("JS Cache-Control = %q, want asset caching preserved (not no-cache)", got)
	}
}

// TestStaticFileServerRejectsTraversal asserts the path-traversal containment
// (threat T-08-03): a request for ../../etc/passwd must NOT escape the static
// directory. http.FileServer + http.Dir clean the path and refuse escape.
func TestStaticFileServerRejectsTraversal(t *testing.T) {
	dir := t.TempDir()
	mustWriteFile(t, filepath.Join(dir, "index.html"), "<p>ok</p>")

	h := StaticFileServer(dir, NewNoOpLogger())

	req := httptest.NewRequest(http.MethodGet, "/../../../../etc/passwd", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code == http.StatusOK {
		body, _ := io.ReadAll(rec.Body)
		if strings.Contains(string(body), "root:") {
			t.Fatalf("traversal escaped the static dir; got /etc/passwd content")
		}
	}
	// Acceptable outcomes: 404 (cleaned path not found) or 400 (bad request).
	// The load-bearing assertion is that /etc/passwd content is NOT returned.
}

func mustWriteFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
