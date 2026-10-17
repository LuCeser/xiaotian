// fsrs_config_test.go exercises the GET/PUT /settings/fsrs endpoints and
// the atomic fsrs_config.json write (Plan 09-05, D-13/D-14, T-09-06) with
// manual fakes and t.TempDir() DataDirs (D-48 convention).
//
// Coverage (Plan 09-05 Task 3 behavior block):
//   - TestGetFSRSDefaults:   missing file → 200 defaults 0.85/180.
//   - TestGetFSRSFromFile:   file values echoed verbatim.
//   - TestGetFSRSCorruptFile: garbage file → 200 defaults (read side never
//     errors to the client; Python's Warning behavior is Plan 09-02's).
//   - TestGetFSRSOutOfBoundsFile: valid JSON with out-of-range values →
//     200 defaults (WR-02: mirrors Python load_scheduler's whole-file
//     fallback so GET never reports values the scheduler will not use).
//   - TestPutFSRSValidation: out-of-range retention/interval, non-integer
//     interval, and extra scheduler fields all → 400 invalid_request;
//     boundary values are accepted.
//   - TestPutFSRSWritesAtomically: exact on-disk key order, no .tmp litter,
//     byte-stable idempotent re-save, and mid-write failure preserving the
//     original file with a 500.
package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/xiaotian/home-tutor-agent/pkg/infra"
	"github.com/xiaotian/home-tutor-agent/pkg/pybridge"
)

// fsrsTestConfigFile is the on-disk filename the handlers must use inside
// DataDir (kept as a literal so RED stays behavioral about the contract).
const fsrsTestConfigFile = "fsrs_config.json"

// newFSRSTestHandler builds the handler with a temp DataDir config.
func newFSRSTestHandler(t *testing.T, exec pybridge.Executor, dataDir string) http.Handler {
	t.Helper()
	_, lessons, practice, receipts := newLessonTestStores(t)
	return NewHandler(exec, &fakeCardRepo{}, nil, lessons, practice, receipts, nil, nil, nil, infra.PythonConfig{DataDir: dataDir}, infra.NewNoOpLogger())
}

func TestGetFSRSDefaults(t *testing.T) {
	dir := t.TempDir()
	h := newFSRSTestHandler(t, &fakeExecutor{}, dir)

	req := httptest.NewRequest(http.MethodGet, "/settings/fsrs", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	var resp struct {
		DesiredRetention float64 `json:"desired_retention"`
		MaximumInterval  int     `json:"maximum_interval"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response %q: %v", rec.Body.String(), err)
	}
	if resp.DesiredRetention != 0.85 || resp.MaximumInterval != 180 {
		t.Errorf("response = {%v, %d}, want defaults {0.85, 180}",
			resp.DesiredRetention, resp.MaximumInterval)
	}
}

func TestGetFSRSFromFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, fsrsTestConfigFile)
	if err := os.WriteFile(path, []byte(`{"desired_retention":0.9,"maximum_interval":120}`), 0o600); err != nil {
		t.Fatal(err)
	}
	h := newFSRSTestHandler(t, &fakeExecutor{}, dir)

	req := httptest.NewRequest(http.MethodGet, "/settings/fsrs", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	var resp struct {
		DesiredRetention float64 `json:"desired_retention"`
		MaximumInterval  int     `json:"maximum_interval"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response %q: %v", rec.Body.String(), err)
	}
	if resp.DesiredRetention != 0.9 || resp.MaximumInterval != 120 {
		t.Errorf("response = {%v, %d}, want the file values {0.9, 120}",
			resp.DesiredRetention, resp.MaximumInterval)
	}
}

func TestGetFSRSCorruptFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, fsrsTestConfigFile)
	if err := os.WriteFile(path, []byte(`{not json at all`), 0o600); err != nil {
		t.Fatal(err)
	}
	h := newFSRSTestHandler(t, &fakeExecutor{}, dir)

	req := httptest.NewRequest(http.MethodGet, "/settings/fsrs", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 even for a corrupt file (body=%s)",
			rec.Code, rec.Body.String())
	}
	var resp struct {
		DesiredRetention float64 `json:"desired_retention"`
		MaximumInterval  int     `json:"maximum_interval"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response %q: %v", rec.Body.String(), err)
	}
	if resp.DesiredRetention != 0.85 || resp.MaximumInterval != 180 {
		t.Errorf("response = {%v, %d}, want defaults {0.85, 180} for a corrupt file",
			resp.DesiredRetention, resp.MaximumInterval)
	}
}

func TestGetFSRSOutOfBoundsFile(t *testing.T) {
	// WR-02: the Python loader (load_scheduler) falls back to 0.85/180 as a
	// WHOLE FILE when any value is out of range; the Go read side must
	// report the same thing or the SettingsCard sliders show values the
	// scheduler does not use. A boundary-valid file stays verbatim.
	cases := []struct {
		name string
		raw  string
	}{
		{"retention above 1", `{"desired_retention":1.7,"maximum_interval":180}`},
		{"retention below 0.7", `{"desired_retention":0.5,"maximum_interval":180}`},
		{"missing maximum_interval decodes to 0", `{"desired_retention":0.85}`},
		{"interval above cap", `{"desired_retention":0.85,"maximum_interval":99999}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, fsrsTestConfigFile)
			if err := os.WriteFile(path, []byte(tc.raw), 0o600); err != nil {
				t.Fatal(err)
			}
			h := newFSRSTestHandler(t, &fakeExecutor{}, dir)

			req := httptest.NewRequest(http.MethodGet, "/settings/fsrs", nil)
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("%s: status = %d, want 200 (body=%s)", tc.name, rec.Code, rec.Body.String())
			}
			var resp struct {
				DesiredRetention float64 `json:"desired_retention"`
				MaximumInterval  int     `json:"maximum_interval"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
				t.Fatalf("decode response %q: %v", rec.Body.String(), err)
			}
			if resp.DesiredRetention != 0.85 || resp.MaximumInterval != 180 {
				t.Errorf("%s: response = {%v, %d}, want defaults {0.85, 180}",
					tc.name, resp.DesiredRetention, resp.MaximumInterval)
			}
		})
	}

	t.Run("boundary-valid file still returns verbatim", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, fsrsTestConfigFile)
		if err := os.WriteFile(path, []byte(`{"desired_retention":0.95,"maximum_interval":36500}`), 0o600); err != nil {
			t.Fatal(err)
		}
		h := newFSRSTestHandler(t, &fakeExecutor{}, dir)

		req := httptest.NewRequest(http.MethodGet, "/settings/fsrs", nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
		}
		var resp struct {
			DesiredRetention float64 `json:"desired_retention"`
			MaximumInterval  int     `json:"maximum_interval"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("decode response %q: %v", rec.Body.String(), err)
		}
		if resp.DesiredRetention != 0.95 || resp.MaximumInterval != 36500 {
			t.Errorf("response = {%v, %d}, want the in-range file values {0.95, 36500}",
				resp.DesiredRetention, resp.MaximumInterval)
		}
	})
}

func TestPutFSRSValidation(t *testing.T) {
	cases := map[string]string{
		"retention below 0.7":   `{"desired_retention":0.69,"maximum_interval":180}`,
		"retention above 0.95":  `{"desired_retention":0.96,"maximum_interval":180}`,
		"retention way above":   `{"desired_retention":1.5,"maximum_interval":180}`,
		"interval zero":         `{"desired_retention":0.85,"maximum_interval":0}`,
		"interval negative":     `{"desired_retention":0.85,"maximum_interval":-5}`,
		"interval non-integer":  `{"desired_retention":0.85,"maximum_interval":2.5}`,
		"interval above 36500":  `{"desired_retention":0.85,"maximum_interval":36501}`,
		"extra scheduler field": `{"desired_retention":0.85,"maximum_interval":180,"learning_steps":[60,600]}`,
		"extra fuzz flag":       `{"desired_retention":0.85,"maximum_interval":180,"enable_fuzzing":false}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			h := newFSRSTestHandler(t, &fakeExecutor{}, dir)
			req := httptest.NewRequest(http.MethodPut, "/settings/fsrs",
				strings.NewReader(body))
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)

			if rec.Code != http.StatusBadRequest {
				t.Fatalf("%s: status = %d, want 400 (body=%s)", name, rec.Code, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), `"code":"invalid_request"`) {
				t.Errorf("%s: body = %s, want invalid_request envelope", name, rec.Body.String())
			}
			// Nothing may be written when validation fails.
			if _, err := os.Stat(filepath.Join(dir, fsrsTestConfigFile)); !os.IsNotExist(err) {
				t.Errorf("%s: config file exists after rejected PUT (err=%v)", name, err)
			}
		})
	}

	t.Run("boundary values are accepted", func(t *testing.T) {
		dir := t.TempDir()
		h := newFSRSTestHandler(t, &fakeExecutor{}, dir)
		req := httptest.NewRequest(http.MethodPut, "/settings/fsrs",
			strings.NewReader(`{"desired_retention":0.95,"maximum_interval":36500}`))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 at the inclusive bounds (body=%s)",
				rec.Code, rec.Body.String())
		}
	})
}

func TestPutFSRSWritesAtomically(t *testing.T) {
	t.Run("valid write echoes and lands on disk in fixed key order", func(t *testing.T) {
		dir := t.TempDir()
		h := newFSRSTestHandler(t, &fakeExecutor{}, dir)

		req := httptest.NewRequest(http.MethodPut, "/settings/fsrs",
			strings.NewReader(`{"desired_retention":0.88,"maximum_interval":200}`))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
		}
		var resp struct {
			DesiredRetention float64 `json:"desired_retention"`
			MaximumInterval  int     `json:"maximum_interval"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("decode response %q: %v", rec.Body.String(), err)
		}
		if resp.DesiredRetention != 0.88 || resp.MaximumInterval != 200 {
			t.Errorf("echo = {%v, %d}, want {0.88, 200}", resp.DesiredRetention, resp.MaximumInterval)
		}

		// VOC-03 ordering edge: fixed key order desired_retention THEN
		// maximum_interval — assert on the RAW bytes, not a re-decode.
		raw, err := os.ReadFile(filepath.Join(dir, fsrsTestConfigFile))
		if err != nil {
			t.Fatalf("read config file: %v", err)
		}
		if want := `{"desired_retention":0.88,"maximum_interval":200}`; string(raw) != want {
			t.Errorf("file bytes = %s, want exactly %s", raw, want)
		}

		// No .tmp litter may remain after the call.
		assertNoTempFiles(t, dir)
	})

	t.Run("re-PUT identical values is byte-stable (idempotent)", func(t *testing.T) {
		dir := t.TempDir()
		h := newFSRSTestHandler(t, &fakeExecutor{}, dir)
		body := `{"desired_retention":0.88,"maximum_interval":200}`

		for i := 0; i < 2; i++ {
			req := httptest.NewRequest(http.MethodPut, "/settings/fsrs",
				strings.NewReader(body))
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("PUT #%d: status = %d, want 200 (body=%s)", i+1, rec.Code, rec.Body.String())
			}
		}

		raw, err := os.ReadFile(filepath.Join(dir, fsrsTestConfigFile))
		if err != nil {
			t.Fatal(err)
		}
		if string(raw) != body {
			t.Errorf("file bytes after double save = %s, want stable %s", raw, body)
		}
		assertNoTempFiles(t, dir)
	})

	t.Run("mid-write failure leaves the original file intact and returns 500", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, fsrsTestConfigFile)
		original := `{"desired_retention":0.9,"maximum_interval":120}`
		if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
			t.Fatal(err)
		}

		// Force the atomic replace to fail. The plan's literal mechanism is
		// a read-only subdirectory; on Windows a directory's read-only
		// attribute does NOT block child file creation (NTFS semantics), so
		// the per-GOOS equivalent forces the failure at the same stage:
		//   - unix:    read-only DIRECTORY → os.CreateTemp fails.
		//   - windows: READONLY target FILE → os.Rename (MoveFileEx with
		//     MOVEFILE_REPLACE_EXISTING) fails with ACCESS_DENIED.
		// Either way the handler must return 500 with the ORIGINAL file
		// byte-identical — never a truncated config (T-09-06).
		var restore func()
		if runtime.GOOS == "windows" {
			if err := os.Chmod(path, 0o444); err != nil {
				t.Fatal(err)
			}
			restore = func() { _ = os.Chmod(path, 0o600) }
		} else {
			if err := os.Chmod(dir, 0o555); err != nil {
				t.Fatal(err)
			}
			restore = func() { _ = os.Chmod(dir, 0o755) }
		}
		defer restore()

		h := newFSRSTestHandler(t, &fakeExecutor{}, dir)
		req := httptest.NewRequest(http.MethodPut, "/settings/fsrs",
			strings.NewReader(`{"desired_retention":0.88,"maximum_interval":200}`))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)

		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("status = %d, want 500 when the write fails (body=%s)",
				rec.Code, rec.Body.String())
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read original after failed write: %v", err)
		}
		if string(raw) != original {
			t.Errorf("original file mutated: %q, want intact %q", raw, original)
		}
		assertNoTempFiles(t, dir)
	})
}

// assertNoTempFiles fails the test when any .tmp file remains in dir.
func assertNoTempFiles(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Errorf("temp file left behind: %s", e.Name())
		}
	}
}
