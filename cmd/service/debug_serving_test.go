package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
)

// The debug interface page moved to scripts/archive/ (261007-coe) while its
// URL contract /test-interface.html stayed fixed. These tests pin the new
// file path behind both serving mechanisms: the explicit registration used
// when static serving is enabled (serveRepoRootFile) and the fallback root
// used when static serving is disabled (serveDebugRoot). t.Chdir resolves
// the handlers' repository-relative paths against the repository root;
// t.Chdir must not be combined with t.Parallel, so these tests run serially.

func TestServeRepoRootFile(t *testing.T) {
	t.Chdir(filepath.Join("..", ".."))

	t.Run("archived debug page returns 200 with content", func(t *testing.T) {
		handler := serveRepoRootFile("scripts/archive/test-interface.html")
		rec := httptest.NewRecorder()
		handler(rec, httptest.NewRequest(http.MethodGet, "/test-interface.html", nil))

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", rec.Code)
		}
		body, err := io.ReadAll(rec.Body)
		if err != nil {
			t.Fatalf("read body: %v", err)
		}
		if len(body) <= 1000 {
			t.Fatalf("body size = %d bytes, want > 1000", len(body))
		}
	})

	t.Run("missing file returns 404", func(t *testing.T) {
		handler := serveRepoRootFile("scripts/archive/does-not-exist.html")
		rec := httptest.NewRecorder()
		handler(rec, httptest.NewRequest(http.MethodGet, "/does-not-exist.html", nil))

		if rec.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want 404", rec.Code)
		}
	})
}

func TestServeDebugRoot(t *testing.T) {
	t.Chdir(filepath.Join("..", ".."))

	t.Run("root serves archived debug page", func(t *testing.T) {
		rec := httptest.NewRecorder()
		serveDebugRoot(rec, httptest.NewRequest(http.MethodGet, "/", nil))

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", rec.Code)
		}
		body, err := io.ReadAll(rec.Body)
		if err != nil {
			t.Fatalf("read body: %v", err)
		}
		if len(body) <= 1000 {
			t.Fatalf("body size = %d bytes, want > 1000", len(body))
		}
	})

	t.Run("removed dead debug page path returns 404", func(t *testing.T) {
		rec := httptest.NewRecorder()
		serveDebugRoot(rec, httptest.NewRequest(http.MethodGet, "/test-interface-debug.html", nil))

		if rec.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want 404", rec.Code)
		}
	})
}
