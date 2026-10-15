// Package infra: static.go provides the student web interface static file
// handler (Phase 8, WEB-02/WEB-04).
//
// This file deliberately follows the established infra handler-function
// pattern (see HealthHandler, MetricsHandler in this package): infra exposes
// pure constructors returning http.Handler, and cmd/service/main.go owns the
// single http.ServeMux that wires them. The infra.Server is the lifecycle
// coordinator for memory / pool / registry / workers — it intentionally does
// NOT own an HTTP mux, so static serving lives here as a standalone handler
// rather than a method on Server.
package infra

import (
	"mime"
	"net/http"
)

// staticMimeTypesRegistered ensures .js and .css responses carry the correct
// Content-Type even on minimal VPS images whose mime.types database is
// incomplete (common on stripped Alpine/Debian). iOS Safari refuses to
// execute a <script> served as anything other than a JavaScript MIME type,
// so a missing or wrong type silently breaks the student interface. Go's
// net/http serves files via mime.TypeByExtension; AddExtensionType patches
// the global registry so http.FileServer picks the right value up. The
// registration is idempotent and process-global, which is the desired scope
// for a single-binary service.
var staticMimeTypesRegistered = registerStaticMimeTypes()

func registerStaticMimeTypes() struct{} {
	// RFC 9239 standardized the JavaScript MIME type as text/javascript;
	// both text/javascript and application/javascript are in the HTML spec's
	// "JavaScript MIME type" list, so browsers (including Safari) accept
	// either. charset=utf-8 matches what Go's own server defaults to.
	// The extensions and types are fixed constants; a failure here only means
	// the process mime map rejected a known type, which does not change serving.
	_ = mime.AddExtensionType(".js", "text/javascript; charset=utf-8")
	_ = mime.AddExtensionType(".mjs", "text/javascript; charset=utf-8")
	_ = mime.AddExtensionType(".css", "text/css; charset=utf-8")
	return struct{}{}
}

// StaticFileServer returns an http.Handler that serves static files from dir
// for the student web interface (Phase 8, WEB-02/WEB-04). It wraps
// http.FileServer with:
//
//   - Correct MIME types for .js/.css/.mjs (see staticMimeTypesRegistered),
//     so iOS Safari executes scripts and applies stylesheets.
//   - iOS Safari no-caching headers for HTML entry documents so interface
//     updates are picked up without a manual cache clear; static assets keep
//     their ETag-based caching for performance (RESEARCH.md "prevent caching
//     issues" intent).
//   - Path-traversal containment inherited from http.Dir (threat T-08-03):
//     http.FileServer resolves the request URL against dir and rejects any
//     path that escapes the root (e.g. ../../etc/passwd) with HTTP 404.
//
// dir MUST be non-empty and SHOULD have been validated to exist by the caller
// (main.go Stat-checks it at boot). An empty dir returns a handler that
// always responds 404 — this lets tests and disabled-config paths construct
// the handler without a real directory.
//
// logger receives a debug line per served HTML document when non-nil; nil is
// accepted so the constructor is cheap to call from tests that do not wire a
// logger.
func StaticFileServer(dir string, logger Logger) http.Handler {
	// Touch the sentinel so the mime registration runs even if a caller never
	// imports the package init path. Cheap (struct{} assignment).
	_ = staticMimeTypesRegistered

	if dir == "" {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.NotFound(w, r)
		})
	}

	fileServer := http.FileServer(http.Dir(dir))

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// HTML entry documents must not be cached aggressively, otherwise
		// students on shared iPads see a stale interface after a deploy. The
		// referenced /styles/* and /js/* assets keep http.FileServer's default
		// ETag + Last-Modified handling, so they still benefit from 304s. We
		// set this header BEFORE delegating to FileServer; http.FileServer
		// does not overwrite Cache-Control for existing files.
		if isHTMLDocument(r.URL.Path) {
			w.Header().Set("Cache-Control", "no-cache, must-revalidate")
		}
		fileServer.ServeHTTP(w, r)
	})
}

// isHTMLDocument reports whether path names an HTML entry document (the
// student interface's index.html or any *.htm/*.html file). Used to scope the
// no-cache header so asset caching is preserved.
func isHTMLDocument(path string) bool {
	if path == "/" || path == "/index.html" {
		return true
	}
	if hasSuffix(path, ".html") || hasSuffix(path, ".htm") {
		return true
	}
	return false
}

// hasSuffix is a tiny allocation-free suffix check so this file does not need
// to import strings just for two comparisons.
func hasSuffix(s, suffix string) bool {
	return len(s) >= len(suffix) && s[len(s)-len(suffix):] == suffix
}
