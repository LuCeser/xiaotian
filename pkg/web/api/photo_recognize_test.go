// photo_recognize_test.go exercises the POST /words/photo/recognize route
// (Issue #3): the parent word-list photo draft endpoint. The vision seam is
// faked through a real *ocr.WordlistPipeline (NewWordlistPipeline + closure
// seam — same construction shape cmd/service uses), so the route's body
// handling, cap enforcement and error mapping are what these tests pin.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/xiaotian/home-tutor-agent/pkg/infra"
	"github.com/xiaotian/home-tutor-agent/pkg/ocr"
)

// jpegPrefix mirrors pkg/ocr's sniff input (SOI + marker start).
var jpegPrefix = []byte{0xFF, 0xD8, 0xFF, 0xE0, 0x00, 0x10, 'J', 'F', 'I', 'F'}

// pngPrefix mirrors pkg/ocr's PNG signature.
var pngPrefix = []byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A}

// newRecognizeHandler wires a handler whose recognizer replies with the
// canned entries (or the canned error) for every image.
func newRecognizeHandler(t *testing.T, reply string, readErr error, maxBytes int) http.Handler {
	t.Helper()
	_, lessons, practice, receipts := newLessonTestStores(t)
	pipeline := ocr.NewWordlistPipeline(
		func(_ context.Context, _, _ string, _ []byte, _ string) (string, error) {
			if readErr != nil {
				return "", readErr
			}
			return reply, nil
		}, maxBytes)
	return NewHandler(&fakeExecutor{}, &fakeCardRepo{}, nil, lessons, practice, receipts,
		pipeline, infra.PythonConfig{}, infra.NewNoOpLogger())
}

func TestRecognizeWordPhoto(t *testing.T) {
	const reply = `[{"word":"ice cream","meaning":"冰激凌","confidence":0.9}]`

	t.Run("valid image returns 200 with the draft entries", func(t *testing.T) {
		h := newRecognizeHandler(t, reply, nil, 1<<20)
		req := httptest.NewRequest(http.MethodPost, "/words/photo/recognize",
			strings.NewReader(string(jpegPrefix)+"rest-of-jpeg"))
		req.Header.Set("Content-Type", "image/jpeg")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
		}
		var resp struct {
			Entries []ocr.WordlistEntry `json:"entries"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("decode response %q: %v", rec.Body.String(), err)
		}
		if len(resp.Entries) != 1 || resp.Entries[0].Word != "ice cream" ||
			resp.Entries[0].Meaning != "冰激凌" || resp.Entries[0].Confidence != 0.9 {
			t.Errorf("entries = %+v, want the parsed draft entry", resp.Entries)
		}
	})

	t.Run("declared content type is ignored in favor of sniffing", func(t *testing.T) {
		h := newRecognizeHandler(t, reply, nil, 1<<20)
		// PNG bytes labeled as JPEG must still be recognized as PNG (the
		// sniff is authoritative; a wrong label must not break recognition).
		req := httptest.NewRequest(http.MethodPost, "/words/photo/recognize",
			strings.NewReader(string(pngPrefix)+"rest-of-png"))
		req.Header.Set("Content-Type", "image/jpeg")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
		}
	})

	t.Run("empty body returns 400 invalid_request", func(t *testing.T) {
		h := newRecognizeHandler(t, reply, nil, 1<<20)
		req := httptest.NewRequest(http.MethodPost, "/words/photo/recognize", strings.NewReader(""))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400", rec.Code)
		}
		if !strings.Contains(rec.Body.String(), codeInvalidRequest) {
			t.Errorf("body = %s, want invalid_request envelope", rec.Body.String())
		}
	})

	t.Run("non-image bytes return 400 invalid_request", func(t *testing.T) {
		h := newRecognizeHandler(t, reply, nil, 1<<20)
		req := httptest.NewRequest(http.MethodPost, "/words/photo/recognize",
			strings.NewReader("GIF89a definitely not a word list"))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400", rec.Code)
		}
	})

	t.Run("oversized body returns 413 image_too_large", func(t *testing.T) {
		h := newRecognizeHandler(t, reply, nil, 64)
		big := append(append([]byte{}, jpegPrefix...), strings.Repeat("x", 512)...)
		req := httptest.NewRequest(http.MethodPost, "/words/photo/recognize",
			strings.NewReader(string(big)))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusRequestEntityTooLarge {
			t.Fatalf("status = %d, want 413", rec.Code)
		}
		if !strings.Contains(rec.Body.String(), "image_too_large") {
			t.Errorf("body = %s, want image_too_large envelope", rec.Body.String())
		}
	})

	t.Run("vision failure returns 502 recognition_failed", func(t *testing.T) {
		h := newRecognizeHandler(t, "", errors.New("upstream 500"), 1<<20)
		req := httptest.NewRequest(http.MethodPost, "/words/photo/recognize",
			strings.NewReader(string(jpegPrefix)+"x"))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadGateway {
			t.Fatalf("status = %d, want 502", rec.Code)
		}
		if !strings.Contains(rec.Body.String(), "recognition_failed") {
			t.Errorf("body = %s, want recognition_failed envelope", rec.Body.String())
		}
	})

	t.Run("unrecognizable photo returns 502 recognition_failed", func(t *testing.T) {
		h := newRecognizeHandler(t, "完全不是 JSON 的回复", nil, 1<<20)
		req := httptest.NewRequest(http.MethodPost, "/words/photo/recognize",
			strings.NewReader(string(jpegPrefix)+"x"))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadGateway {
			t.Fatalf("status = %d, want 502", rec.Code)
		}
	})

	t.Run("no word-list entries found returns 200 with empty array", func(t *testing.T) {
		h := newRecognizeHandler(t, `[]`, nil, 1<<20)
		req := httptest.NewRequest(http.MethodPost, "/words/photo/recognize",
			strings.NewReader(string(jpegPrefix)+"x"))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 (empty is legal)", rec.Code)
		}
		if !strings.Contains(rec.Body.String(), `"entries":[]`) {
			t.Errorf("body = %s, want entries:[]", rec.Body.String())
		}
	})

	t.Run("nil recognizer returns 503 recognition_unavailable", func(t *testing.T) {
		_, lessons, practice, receipts := newLessonTestStores(t)
		h := NewHandler(&fakeExecutor{}, &fakeCardRepo{}, nil, lessons, practice, receipts,
			nil, infra.PythonConfig{}, infra.NewNoOpLogger())
		req := httptest.NewRequest(http.MethodPost, "/words/photo/recognize",
			strings.NewReader(string(jpegPrefix)+"x"))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("status = %d, want 503", rec.Code)
		}
		if !strings.Contains(rec.Body.String(), "recognition_unavailable") {
			t.Errorf("body = %s, want recognition_unavailable envelope", rec.Body.String())
		}
	})
}
