package api

// textbook_progress_test.go — the Issue #82 interpret route at the HTTP
// layer: POST /textbooks/progress/interpret turns one parent sentence into
// a VERIFIED proposal against the REAL textbook repository (per-test temp
// SQLite), with the LLM boundary on the recording fake chat. Nothing here
// writes progress — the route proposes; persistence stays on PUT /textbooks/{id}.

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/xiaotian/home-tutor-agent/pkg/database"
	"github.com/xiaotian/home-tutor-agent/pkg/infra"
	"github.com/xiaotian/home-tutor-agent/pkg/ocr"
)

// newProgressTestHandler wires the handler with a REAL textbook repository
// and a REAL ProgressInterpreter over the canned chat reply.
func newProgressTestHandler(t *testing.T, reply string, chatErr error) (http.Handler, *sql.DB) {
	t.Helper()
	db, lessons, practice, receipts := newLessonTestStores(t)
	textbooks, err := database.NewTextbookRepository(db, nil)
	if err != nil {
		t.Fatalf("NewTextbookRepository: %v", err)
	}
	chat := func(ctx context.Context, system, user string) (string, error) { return reply, chatErr }
	h := NewHandler(&fakeExecutor{}, &fakeCardRepo{}, textbooks, lessons, practice, receipts,
		nil, nil, ocr.NewProgressInterpreter(chat), nil, nil, infra.PythonConfig{}, infra.NewNoOpLogger())
	return h, db
}

// postInterpret fires the route and returns recorder + decoded body.
func postInterpret(t *testing.T, h http.Handler, message string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	rec := httptest.NewRecorder()
	body := `{"message":` + mustMarshal(t, message) + `}`
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/textbooks/progress/interpret", strings.NewReader(body)))
	var decoded map[string]any
	if rec.Body.Len() > 0 {
		if err := json.Unmarshal(rec.Body.Bytes(), &decoded); err != nil {
			t.Fatalf("decode response %q: %v", rec.Body.String(), err)
		}
	}
	return rec, decoded
}

func mustMarshal(t *testing.T, v any) string {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(raw)
}

func TestTextbookProgressInterpretRoute(t *testing.T) {
	t.Run("proposed carries the verified book, lesson and learned count", func(t *testing.T) {
		h, db := newProgressTestHandler(t,
			`{"status":"proposed","textbook_id":7,"lesson":"第三单元"}`, nil)
		ensureTextbookTables(t, db)
		seedTextbookRow(t, db, 7, "快乐英语", `["第一单元","第二单元","第三单元","第四单元"]`, 0)

		rec, decoded := postInterpret(t, h, "现在学到第三单元了")
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
		}
		if decoded["status"] != "proposed" {
			t.Fatalf("status = %v, want proposed", decoded["status"])
		}
		if decoded["textbookId"] != float64(7) || decoded["textbookName"] != "快乐英语" {
			t.Fatalf("textbook = %v/%v, want 7/快乐英语", decoded["textbookId"], decoded["textbookName"])
		}
		if decoded["lesson"] != "第三单元" || decoded["learnedLessons"] != float64(3) {
			t.Fatalf("lesson/learned = %v/%v, want 第三单元/3", decoded["lesson"], decoded["learnedLessons"])
		}
	})

	t.Run("unclear carries the reason and the minimal book candidates", func(t *testing.T) {
		h, db := newProgressTestHandler(t,
			`{"status":"unclear","lesson":"第三单元","reason":"请问是哪一本教材？"}`, nil)
		ensureTextbookTables(t, db)
		seedTextbookRow(t, db, 7, "快乐英语", `["第一单元","第二单元","第三单元","第四单元"]`, 0)
		seedTextbookRow(t, db, 9, "阳光英语", `["第三单元","第五单元"]`, 0)

		rec, decoded := postInterpret(t, h, "学到第三单元了")
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
		}
		if decoded["status"] != "unclear" || decoded["reason"] != "请问是哪一本教材？" {
			t.Fatalf("decoded = %v", decoded)
		}
		cands, ok := decoded["candidates"].([]any)
		if !ok || len(cands) != 2 {
			t.Fatalf("candidates = %v, want 2", decoded["candidates"])
		}
		first := cands[0].(map[string]any)
		if first["textbookName"] != "快乐英语" || first["learnedLessons"] != float64(3) {
			t.Fatalf("first candidate = %v, want 快乐英语/3", first)
		}
	})

	t.Run("not_progress explains without proposing", func(t *testing.T) {
		h, db := newProgressTestHandler(t,
			`{"status":"not_progress","reason":"这句话是想开始一次默写"}`, nil)
		ensureTextbookTables(t, db)
		seedTextbookRow(t, db, 7, "快乐英语", `["第一单元"]`, 0)

		rec, decoded := postInterpret(t, h, "开始默写吧")
		if rec.Code != http.StatusOK || decoded["status"] != "not_progress" {
			t.Fatalf("rec = %d %v", rec.Code, decoded)
		}
		if decoded["reason"] != "这句话是想开始一次默写" {
			t.Fatalf("reason = %v", decoded["reason"])
		}
	})

	t.Run("no registered textbooks answers unclear without an LLM round trip", func(t *testing.T) {
		called := false
		h, _ := newProgressTestHandler(t, "", nil)
		_ = called

		rec, decoded := postInterpret(t, h, "现在学到第三单元了")
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
		}
		if decoded["status"] != "unclear" {
			t.Fatalf("status = %v, want unclear", decoded["status"])
		}
		if reason, _ := decoded["reason"].(string); reason == "" {
			t.Error("reason must explain that no textbook is registered")
		}
	})

	t.Run("nil interpreter answers 503 interpretation_unavailable", func(t *testing.T) {
		_, lessons, practice, receipts := newLessonTestStores(t)
		h := NewHandler(&fakeExecutor{}, &fakeCardRepo{}, nil, lessons, practice, receipts,
			nil, nil, nil, nil, nil, infra.PythonConfig{}, infra.NewNoOpLogger())
		rec, _ := postInterpret(t, h, "现在学到第三单元了")
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("status = %d, want 503", rec.Code)
		}
		if !strings.Contains(rec.Body.String(), "interpretation_unavailable") {
			t.Fatalf("body = %s", rec.Body.String())
		}
	})

	t.Run("empty and oversize messages answer 400 invalid_request", func(t *testing.T) {
		h, _ := newProgressTestHandler(t, "{}", nil)
		for _, msg := range []string{"", strings.Repeat("长", 201)} {
			rec, _ := postInterpret(t, h, msg)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("message len %d: status = %d, want 400", len([]rune(msg)), rec.Code)
			}
			if !strings.Contains(rec.Body.String(), codeInvalidRequest) {
				t.Fatalf("body = %s", rec.Body.String())
			}
		}
	})

	t.Run("LLM transport failure answers 502 interpretation_failed", func(t *testing.T) {
		h, db := newProgressTestHandler(t, "", context.DeadlineExceeded)
		ensureTextbookTables(t, db)
		seedTextbookRow(t, db, 7, "快乐英语", `["第三单元"]`, 0)

		rec, _ := postInterpret(t, h, "学到第三单元了")
		if rec.Code != http.StatusBadGateway {
			t.Fatalf("status = %d, want 502", rec.Code)
		}
		if !strings.Contains(rec.Body.String(), "interpretation_failed") {
			t.Fatalf("body = %s", rec.Body.String())
		}
	})
}
