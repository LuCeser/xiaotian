package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/xiaotian/home-tutor-agent/pkg/infra"
	"github.com/xiaotian/home-tutor-agent/pkg/ocr"
)

// newMeaningHandler wires the supplement route around a real
// MeaningGenerator whose ChatReader seam is a closure (the photo-recognize
// harness pattern: a real pipeline, a fake external boundary).
func newMeaningHandler(t *testing.T, reply string, chatErr error) (http.Handler, *[]string) {
	t.Helper()
	var prompts []string
	gen := ocr.NewMeaningGenerator(func(ctx context.Context, systemPrompt, userPrompt string) (string, error) {
		prompts = append(prompts, systemPrompt+"\n---\n"+userPrompt)
		if chatErr != nil {
			return "", chatErr
		}
		return reply, nil
	})
	_, lessons, practice, receipts := newLessonTestStores(t)
	return NewHandler(&fakeExecutor{}, &fakeCardRepo{}, nil, lessons, practice, receipts,
		nil, gen, nil, infra.PythonConfig{}, infra.NewNoOpLogger()), &prompts
}

func postMeanings(t *testing.T, h http.Handler, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/words/meaning/generate", strings.NewReader(body))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestGenerateWordMeanings(t *testing.T) {
	t.Run("nil generator returns 503 generation_unavailable", func(t *testing.T) {
		_, lessons, practice, receipts := newLessonTestStores(t)
		h := NewHandler(&fakeExecutor{}, &fakeCardRepo{}, nil, lessons, practice, receipts,
			nil, nil, nil, infra.PythonConfig{}, infra.NewNoOpLogger())
		rec := postMeanings(t, h, `{"words":["apple"],"textbook":{}}`)
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("status = %d, want 503 (body=%s)", rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), codeGenerationUnavailable) {
			t.Errorf("body = %s, want %s envelope", rec.Body.String(), codeGenerationUnavailable)
		}
	})

	t.Run("happy path answers meanings plus missing, prompt carries the confirmed context", func(t *testing.T) {
		h, prompts := newMeaningHandler(t,
			`[{"word":"apple","meaning":"n. 苹果"},{"word":"banana","meaning":""}]`, nil)
		rec := postMeanings(t, h, `{
			"words": ["apple", "banana", " apple "],
			"textbook": {"edition":"人教版PEP","grade":"三年级","volume":"上册"},
			"learnedLessons": 12
		}`)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
		}
		// Wire shape: meanings map + missing array (per-word honesty).
		body := rec.Body.String()
		for _, want := range []string{`"apple":"n. 苹果"`, `"missing":["banana"]`} {
			if !strings.Contains(body, want) {
				t.Errorf("body = %s, want %s", body, want)
			}
		}
		// AC 自动测试验证提示所用背景: the seam saw the confirmed context
		// (deduplicated words included) — the request that reached the
		// external service is part of the verified surface.
		if len(*prompts) != 1 {
			t.Fatalf("seam calls = %d, want 1", len(*prompts))
		}
		saw := (*prompts)[0]
		for _, want := range []string{"人教版PEP", "三年级", "上册", "已学完第 12 课", "1. apple", "2. banana"} {
			if !strings.Contains(saw, want) {
				t.Errorf("seam prompt missing %q", want)
			}
		}
		if strings.Contains(saw, " apple ") {
			t.Errorf("seam prompt saw un-deduplicated word")
		}
	})

	t.Run("null learnedLessons passes through as unregistered progress", func(t *testing.T) {
		h, prompts := newMeaningHandler(t, `[{"word":"apple","meaning":"n. 苹果"}]`, nil)
		rec := postMeanings(t, h, `{"words":["apple"],"textbook":{"edition":"外研版"},"learnedLessons":null}`)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
		}
		if !strings.Contains((*prompts)[0], "尚未登记学习进度") {
			t.Errorf("prompt must carry 尚未登记学习进度 for a null gauge:\n%s", (*prompts)[0])
		}
	})

	t.Run("empty or oversized word lists return 400 invalid_request", func(t *testing.T) {
		h, _ := newMeaningHandler(t, `[]`, nil)
		var oversized strings.Builder
		for i := 0; i < 101; i++ {
			if i > 0 {
				oversized.WriteByte(',')
			}
			oversized.WriteString(`"w`)
			oversized.WriteString(string(rune('a' + i%26)))
			oversized.WriteString(string(rune('a' + i/26)))
			oversized.WriteString(`"`)
		}
		for name, body := range map[string]string{
			"empty":     `{"words":[],"textbook":{}}`,
			"blank":     `{"words":["  ",""],"textbook":{}}`,
			"oversized": `{"words":[` + oversized.String() + `],"textbook":{}}`,
		} {
			rec := postMeanings(t, h, body)
			if rec.Code != http.StatusBadRequest {
				t.Errorf("%s: status = %d, want 400 (body=%s)", name, rec.Code, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), codeInvalidRequest) {
				t.Errorf("%s: body = %s, want invalid_request envelope", name, rec.Body.String())
			}
		}
	})

	t.Run("negative learnedLessons returns 400", func(t *testing.T) {
		h, _ := newMeaningHandler(t, `[]`, nil)
		rec := postMeanings(t, h, `{"words":["apple"],"textbook":{},"learnedLessons":-1}`)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400 (body=%s)", rec.Code, rec.Body.String())
		}
	})

	t.Run("seam failure maps to 502 generation_failed", func(t *testing.T) {
		h, _ := newMeaningHandler(t, "", context.DeadlineExceeded)
		rec := postMeanings(t, h, `{"words":["apple"],"textbook":{}}`)
		if rec.Code != http.StatusBadGateway {
			t.Fatalf("status = %d, want 502 (body=%s)", rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), codeGenerationFailed) {
			t.Errorf("body = %s, want %s envelope", rec.Body.String(), codeGenerationFailed)
		}
	})

	t.Run("unparseable reply maps to 502 generation_failed", func(t *testing.T) {
		h, _ := newMeaningHandler(t, "抱歉，我无法完成这个任务。", nil)
		rec := postMeanings(t, h, `{"words":["apple"],"textbook":{}}`)
		if rec.Code != http.StatusBadGateway {
			t.Fatalf("status = %d, want 502 (body=%s)", rec.Code, rec.Body.String())
		}
	})

	t.Run("unknown body field returns 400 (strict decode)", func(t *testing.T) {
		h, _ := newMeaningHandler(t, `[]`, nil)
		rec := postMeanings(t, h, `{"words":["apple"],"region":"北京"}`)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400 (body=%s)", rec.Code, rec.Body.String())
		}
	})
}
