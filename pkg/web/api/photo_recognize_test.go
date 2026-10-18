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

// newCategoryCaptureHandler wires a handler whose recognizer records the
// prompt pair it was called with (the closure seam's system/user strings)
// and answers an empty draft — enough to pin the ?category= routing.
func newCategoryCaptureHandler(t *testing.T) (http.Handler, *[]string) {
	t.Helper()
	_, lessons, practice, receipts := newLessonTestStores(t)
	var prompts []string
	pipeline := ocr.NewWordlistPipeline(
		func(_ context.Context, system, user string, _ []byte, _ string) (string, error) {
			prompts = append(prompts, system, user)
			return `[]`, nil
		}, 1<<20)
	return NewHandler(&fakeExecutor{}, &fakeCardRepo{}, nil, lessons, practice, receipts,
		pipeline, nil, nil, infra.PythonConfig{}, infra.NewNoOpLogger()), &prompts
}

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
		pipeline, nil, nil, infra.PythonConfig{}, infra.NewNoOpLogger())
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

	t.Run("object reply returns 200 with the textbook header and per-entry lessons", func(t *testing.T) {
		h := newRecognizeHandler(t,
			`{"textbook":{"edition":"人教版PEP","grade":"三年级","volume":"上册","lesson":"第5课"},`+
				`"entries":[{"word":"ice cream","meaning":"冰激凌"},{"word":"apple","meaning":"苹果","lesson":"第6课"}]}`,
			nil, 1<<20)
		req := httptest.NewRequest(http.MethodPost, "/words/photo/recognize",
			strings.NewReader(string(jpegPrefix)+"x"))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
		}
		var resp ocr.WordlistResult
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("decode response %q: %v", rec.Body.String(), err)
		}
		if resp.Textbook == nil || resp.Textbook.Edition != "人教版PEP" || resp.Textbook.Lesson != "第5课" {
			t.Errorf("textbook = %+v, want the readable header", resp.Textbook)
		}
		if len(resp.Entries) != 2 {
			t.Fatalf("entries = %+v, want 2", resp.Entries)
		}
		// Page-level lesson inheritance happens in the pipeline; the route
		// passes it through untouched.
		if resp.Entries[0].Lesson != "第5课" || resp.Entries[1].Lesson != "第6课" {
			t.Errorf("lessons = %q/%q, want inherited 第5课 and own 第6课",
				resp.Entries[0].Lesson, resp.Entries[1].Lesson)
		}
	})

	t.Run("legacy array reply returns 200 with a null textbook", func(t *testing.T) {
		h := newRecognizeHandler(t, reply, nil, 1<<20)
		req := httptest.NewRequest(http.MethodPost, "/words/photo/recognize",
			strings.NewReader(string(jpegPrefix)+"x"))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", rec.Code)
		}
		var resp ocr.WordlistResult
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("decode response %q: %v", rec.Body.String(), err)
		}
		if resp.Textbook != nil {
			t.Errorf("textbook = %+v, want nil for a legacy reply", resp.Textbook)
		}
		if len(resp.Entries) != 1 {
			t.Errorf("entries = %+v, want the one draft entry", resp.Entries)
		}
	})

	t.Run("nil recognizer returns 503 recognition_unavailable", func(t *testing.T) {
		_, lessons, practice, receipts := newLessonTestStores(t)
		h := NewHandler(&fakeExecutor{}, &fakeCardRepo{}, nil, lessons, practice, receipts,
			nil, nil, nil, infra.PythonConfig{}, infra.NewNoOpLogger())
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

// TestRecognizeWordPhotoCategory (Issue #10) pins the route's ?category=
// contract: the SAME endpoint reads either subject's list — chinese routes
// the 语文 prompt pair, english (and absent, the pre-#10 wire shape) keeps
// the Issue #3 English pair, and an off-enum value 400s BEFORE any vision
// cost (the D-06 gate every category-bearing route applies).
func TestRecognizeWordPhotoCategory(t *testing.T) {
	post := func(h http.Handler, rawQuery string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/words/photo/recognize?"+rawQuery,
			strings.NewReader(string(jpegPrefix)+"x"))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}

	t.Run("chinese routes the 语文 prompt pair", func(t *testing.T) {
		h, prompts := newCategoryCaptureHandler(t)
		if rec := post(h, "category=chinese"); rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
		}
		if len(*prompts) != 2 {
			t.Fatalf("pipeline calls = %d prompt strings, want the one system+user pair", len(*prompts))
		}
		// Issue #11 widens the chinese pair into the mixed-page contract —
		// the pair is still unmistakably the 语文 one.
		if !strings.Contains((*prompts)[0], "语文学习内容导入助手") || !strings.Contains((*prompts)[1], "完整古诗") {
			t.Errorf("prompts = %q / %q, want the chinese pair", (*prompts)[0], (*prompts)[1])
		}
	})

	t.Run("english and absent keep the english pair", func(t *testing.T) {
		for _, rawQuery := range []string{"category=english", ""} {
			h, prompts := newCategoryCaptureHandler(t)
			if rec := post(h, rawQuery); rec.Code != http.StatusOK {
				t.Fatalf("?%s status = %d, want 200", rawQuery, rec.Code)
			}
			if len(*prompts) != 2 ||
				!strings.Contains((*prompts)[0], "英语词表导入助手") || !strings.Contains((*prompts)[1], "英语词表") {
				t.Errorf("?%s prompts = %q / %q, want the english pair", rawQuery, (*prompts)[0], (*prompts)[1])
			}
		}
	})

	t.Run("off-enum category 400s before any vision call", func(t *testing.T) {
		h, prompts := newCategoryCaptureHandler(t)
		rec := post(h, "category=france")
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400 (body=%s)", rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), codeInvalidRequest) {
			t.Errorf("body = %s, want invalid_request envelope", rec.Body.String())
		}
		if len(*prompts) != 0 {
			t.Errorf("pipeline calls = %d, want 0 (a typo'd subject must never cost a vision round)", len(*prompts))
		}
	})
}
