package api

// arrangement_test.go — the POST /designations route acceptance at the
// HTTP layer: shape validation, the 404/400 pre-subprocess gates, the
// typed occupied/too_many/empty outcomes, and the pending snapshot the ok
// branch stores. The bridge is the recording fakeExecutor; the textbook
// repository and lesson stores are REAL over the per-test temp SQLite
// (the textbooks_test.go harness).

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/xiaotian/home-tutor-agent/pkg/pybridge"
)

// postDesignation fires one POST /designations with body against h.
func postDesignation(t *testing.T, h http.Handler, body string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/designations", strings.NewReader(body)))
	return rec
}

// designationRows counts the designations table straight from SQLite —
// the typed-outcome branches must all leave it at zero.
func designationRows(t *testing.T, db *sql.DB) int {
	t.Helper()
	var n int
	if err := db.QueryRow("SELECT COUNT(*) FROM designations").Scan(&n); err != nil {
		t.Fatalf("count designations: %v", err)
	}
	return n
}

func TestCreateDesignationRoute(t *testing.T) {
	t.Run("validates shape before any read or subprocess", func(t *testing.T) {
		exec := &fakeExecutor{}
		h, db := newTextbookTestHandler(t, exec, &fakeCardRepo{})
		for name, body := range map[string]string{
			"missing textbook": `{"lesson":"第4单元"}`,
			"zero textbook":    `{"textbookId":0,"lesson":"第4单元"}`,
			"empty lesson":     `{"textbookId":7,"lesson":""}`,
			"blank lesson":     `{"textbookId":7,"lesson":"  "}`,
		} {
			rec := postDesignation(t, h, body)
			if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), codeInvalidRequest) {
				t.Fatalf("%s: status %d (%s), want 400 invalid_request", name, rec.Code, rec.Body.String())
			}
		}
		if len(exec.designateCalls) != 0 {
			t.Fatalf("subprocess calls = %d, want 0 on validation failures", len(exec.designateCalls))
		}
		if rows := designationRows(t, db); rows != 0 {
			t.Fatalf("rows = %d, want 0", rows)
		}
	})

	t.Run("answers 404/400 before the subprocess", func(t *testing.T) {
		exec := &fakeExecutor{}
		h, db := newTextbookTestHandler(t, exec, &fakeCardRepo{})
		ensureTextbookTables(t, db)
		seedTextbookRow(t, db, 7, "指定教材", `["第1课","第4单元"]`, 1)

		rec := postDesignation(t, h, `{"textbookId":99,"lesson":"第4单元"}`)
		if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), codeNotFound) {
			t.Fatalf("unknown textbook: status %d (%s), want 404", rec.Code, rec.Body.String())
		}
		rec = postDesignation(t, h, `{"textbookId":7,"lesson":"不存在课"}`)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), codeInvalidRequest) {
			t.Fatalf("lesson outside list: status %d (%s), want 400", rec.Code, rec.Body.String())
		}
		if len(exec.designateCalls) != 0 {
			t.Fatalf("subprocess calls = %d, want 0 on the pre-subprocess gates", len(exec.designateCalls))
		}
	})

	t.Run("ok stores a pending snapshot and answers identity only", func(t *testing.T) {
		exec := &fakeExecutor{designateResult: pybridge.DesignateResult{
			Status: pybridge.DesignateStatusOK, Total: 2,
			Cards: []pybridge.DesignateCard{
				{ID: 1, Word: "apple", Meaning: "苹果"},
				{ID: 2, Word: "banana", Meaning: "香蕉"},
			},
		}}
		h, db := newTextbookTestHandler(t, exec, &fakeCardRepo{})
		ensureTextbookTables(t, db)
		seedTextbookRow(t, db, 7, "指定教材", `["第1课","第4单元"]`, 1)

		rec := postDesignation(t, h, `{"textbookId":7,"lesson":"第4单元"}`)
		if rec.Code != http.StatusOK {
			t.Fatalf("status %d (%s), want 200", rec.Code, rec.Body.String())
		}
		body := rec.Body.String()
		for _, want := range []string{`"id":1`, `"textbookId":7`, `"lesson":"第4单元"`,
			`"status":"pending"`, `"wordCount":2`} {
			if !strings.Contains(body, want) {
				t.Fatalf("body %s missing %s", body, want)
			}
		}
		// Leak guard: the response never carries word surfaces.
		for _, banned := range []string{"apple", "banana", "苹果", "香蕉"} {
			if strings.Contains(body, banned) {
				t.Fatalf("body %s leaks the word surface %q", body, banned)
			}
		}
		if len(exec.designateCalls) != 1 ||
			exec.designateCalls[0].TextbookID != 7 || exec.designateCalls[0].Lesson != "第4单元" {
			t.Fatalf("designate calls = %+v, want one {7 第4单元}", exec.designateCalls)
		}

		var status, cardsJSON string
		if err := db.QueryRow("SELECT status, cards FROM designations WHERE id = 1").
			Scan(&status, &cardsJSON); err != nil {
			t.Fatalf("read designation row: %v", err)
		}
		if status != "pending" || !strings.Contains(cardsJSON, "apple") {
			t.Fatalf("row = %q / %s, want pending with the word snapshot", status, cardsJSON)
		}
	})

	t.Run("typed outcomes answer their own codes and store nothing", func(t *testing.T) {
		cases := []struct {
			name       string
			wantCode   string
			wantBody   string
			result     pybridge.DesignateResult
			wantStatus int
		}{
			{
				name:       "occupied",
				result:     pybridge.DesignateResult{Status: pybridge.DesignateStatusOccupied, Total: 20},
				wantStatus: http.StatusConflict,
				wantCode:   codeDesignationOccupied,
			},
			{
				name:       "too_many carries the real total",
				result:     pybridge.DesignateResult{Status: pybridge.DesignateStatusTooMany, Total: 120},
				wantStatus: http.StatusUnprocessableEntity,
				wantCode:   codeDesignationTooMany,
				wantBody:   "120",
			},
			{
				name:       "empty target",
				result:     pybridge.DesignateResult{Status: pybridge.DesignateStatusOK, Total: 0},
				wantStatus: http.StatusConflict,
				wantCode:   codeDesignationEmpty,
			},
		}
		for _, tc := range cases {
			exec := &fakeExecutor{designateResult: tc.result}
			h, db := newTextbookTestHandler(t, exec, &fakeCardRepo{})
			ensureTextbookTables(t, db)
			seedTextbookRow(t, db, 7, "指定教材", `["第1课","第4单元"]`, 1)

			rec := postDesignation(t, h, `{"textbookId":7,"lesson":"第4单元"}`)
			if rec.Code != tc.wantStatus || !strings.Contains(rec.Body.String(), tc.wantCode) {
				t.Fatalf("%s: status %d (%s), want %d %s",
					tc.name, rec.Code, rec.Body.String(), tc.wantStatus, tc.wantCode)
			}
			if tc.wantBody != "" && !strings.Contains(rec.Body.String(), tc.wantBody) {
				t.Fatalf("%s: body %s missing %q", tc.name, rec.Body.String(), tc.wantBody)
			}
			if rows := designationRows(t, db); rows != 0 {
				t.Fatalf("%s: stored %d rows, want 0", tc.name, rows)
			}
		}
	})

	t.Run("bridge failures map through the sentinel envelope", func(t *testing.T) {
		exec := &fakeExecutor{designateErr: pybridge.ErrPythonTimeout}
		h, db := newTextbookTestHandler(t, exec, &fakeCardRepo{})
		ensureTextbookTables(t, db)
		seedTextbookRow(t, db, 7, "指定教材", `["第1课","第4单元"]`, 1)

		rec := postDesignation(t, h, `{"textbookId":7,"lesson":"第4单元"}`)
		if rec.Code != http.StatusBadGateway || !strings.Contains(rec.Body.String(), codePythonFailed) {
			t.Fatalf("status %d (%s), want 502 python_failed", rec.Code, rec.Body.String())
		}
		if rows := designationRows(t, db); rows != 0 {
			t.Fatalf("rows = %d, want 0 on a failed bridge call", rows)
		}
	})
}
