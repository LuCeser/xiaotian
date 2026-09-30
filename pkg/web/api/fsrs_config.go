// fsrs_config.go implements the GET/PUT /settings/fsrs endpoints (Plan
// 09-05, VOC-03, D-13/D-14) backed by an atomically-written
// DataDir/fsrs_config.json.
//
// File contract (must stay key-for-key with the Python reader): the file
// lives at PythonConfig.DataDir/fsrs_config.json with EXACTLY two keys —
// {"desired_retention": <float>, "maximum_interval": <int>} — the same
// path+keys vocab_manager.py's load_scheduler() reads via the
// DICTATION_DATA_DIR env that Bridge.childEnv forwards to every spawn
// (Pitfall 8 single-resolution rule: Go writer and Python reader resolve
// THE SAME directory through the SAME env; Plan 09-02 pinned the reader
// side, this file owns the writer side).
//
// Atomicity (D-14, T-09-06): writes go through temp-file-in-the-same-
// directory → write → Sync → Close → os.Rename. A failure at ANY stage
// removes the temp file and leaves the original untouched, so a
// half-written config can never exist at the target path and re-saving
// over an existing file replaces it whole (VOC-03 adjacency edge).
package api

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"

	"github.com/pkg/errors"

	"github.com/xiaotian/home-tutor-agent/pkg/infra"
)

const (
	// fsrsConfigFile is the fixed filename inside PythonConfig.DataDir.
	// Config-derived path only — no user input ever reaches it (T-09-10).
	fsrsConfigFile = "fsrs_config.json"

	// Defaults mirror the SCHEDULER constructor constants
	// (vocab_manager.py:78-84): a missing file means "use these", NOT an
	// error (Plan 09-02: absent file = silent defaults).
	fsrsRetentionDefault = 0.85
	fsrsIntervalDefault  = 180

	// Locked D-13 ranges. The Python loader independently range-checks and
	// falls back on out-of-range values (Plan 09-02), so the Go writer MUST
	// enforce the same window — otherwise a saved setting would be silently
	// ignored by the scheduler while the UI reports success.
	fsrsRetentionMin = 0.7
	fsrsRetentionMax = 0.95
	fsrsIntervalMin  = 1
	fsrsIntervalMax  = 36500
)

// FSRSSettings is the two-knob scheduler config (D-13 two-knobs: no route
// accepts any other scheduler field — no weights, no learning steps, no
// fuzz flags; decodeJSON's DisallowUnknownFields turns an extra field into
// a 400, not a silent ignore). The struct declaration order ALSO defines
// the on-disk key order: json.Marshal emits struct fields in declaration
// order, so the file always serializes as
// {"desired_retention":...,"maximum_interval":...} (VOC-03 ordering edge).
type FSRSSettings struct {
	DesiredRetention float64 `json:"desired_retention"`
	MaximumInterval  int     `json:"maximum_interval"`
}

// fsrsSettingsDefaults returns the D-13 default configuration.
func fsrsSettingsDefaults() FSRSSettings {
	return FSRSSettings{
		DesiredRetention: fsrsRetentionDefault,
		MaximumInterval:  fsrsIntervalDefault,
	}
}

// readFSRSConfig reads and decodes the config file at path. A missing file
// or ANY json/decode problem yields the defaults — the read side never
// errors to the client. WR-02: a file with VALID JSON but OUT-OF-BOUNDS
// values (e.g. desired_retention 1.7 after a manual edit) also falls back
// to the defaults, mirroring the Python loader's whole-file fallback in
// load_scheduler (vocab_manager.py) — otherwise GET would report "current"
// values the scheduler does not actually use, and the SettingsCard sliders
// would mislead the parent.
func readFSRSConfig(path string) FSRSSettings {
	raw, err := os.ReadFile(path)
	if err != nil {
		return fsrsSettingsDefaults()
	}
	var s FSRSSettings
	if err := json.Unmarshal(raw, &s); err != nil {
		return fsrsSettingsDefaults()
	}
	if err := ValidateFSRSSettings(s); err != nil {
		return fsrsSettingsDefaults()
	}
	return s
}

// writeFSRSConfig atomically replaces the config file at path (D-14):
// os.CreateTemp in the SAME directory (so the rename is a same-filesystem
// atomic replace) → write → Sync → Close → os.Rename onto the target. On
// ANY error the temp file is removed and the ORIGINAL file is left
// untouched — never a truncated or partial config.
func writeFSRSConfig(path string, s FSRSSettings) error {
	// Struct marshal → fixed key order (declaration order).
	data, err := json.Marshal(s)
	if err != nil {
		return errors.Wrap(err, "marshal fsrs config")
	}

	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".fsrs_config-*.tmp")
	if err != nil {
		return errors.Wrap(err, "create fsrs config temp file")
	}
	tmpName := tmp.Name()
	// The temp file must not survive ANY exit path once it exists. After a
	// successful rename tmpName no longer exists; the spurious
	// os.Remove error is deliberately ignored.
	defer func() { _ = os.Remove(tmpName) }()

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return errors.Wrap(err, "write fsrs config temp file")
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return errors.Wrap(err, "sync fsrs config temp file")
	}
	if err := tmp.Close(); err != nil {
		return errors.Wrap(err, "close fsrs config temp file")
	}
	if err := os.Rename(tmpName, path); err != nil {
		return errors.Wrap(err, "replace fsrs config")
	}
	return nil
}

// fsrsConfigPath resolves the config file path from the injected
// PythonConfig (config-derived, T-09-10).
func (h *handler) fsrsConfigPath() string {
	return filepath.Join(h.cfg.DataDir, fsrsConfigFile)
}

// getFSRS handles GET /settings/fsrs (Plan 09-05): the file values when
// DataDir/fsrs_config.json exists and parses, defaults 0.85/180 otherwise.
// PUT then GET returns exactly the saved values (VOC-03 ordering edge —
// the struct marshal round-trips the same two keys).
func (h *handler) getFSRS(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, readFSRSConfig(h.fsrsConfigPath()))
}

// putFSRS handles PUT /settings/fsrs (Plan 09-05): strict decode (an extra
// scheduler field is a 400, not a silent ignore), D-13 bounds validation,
// atomic file write, then a 200 echo of the saved values. A write failure
// is a 500 internal — the previous config file remains intact.
func (h *handler) putFSRS(w http.ResponseWriter, r *http.Request) {
	var req FSRSSettings
	if !decodeJSON(w, r, &req) {
		return
	}
	if err := ValidateFSRSSettings(req); err != nil {
		writeError(w, http.StatusBadRequest, codeInvalidRequest, err.Error())
		return
	}
	if err := writeFSRSConfig(h.fsrsConfigPath(), req); err != nil {
		h.logger.Error("write fsrs config failed",
			infra.F("component", "web/api"),
			infra.ErrField(err),
		)
		writeError(w, http.StatusInternalServerError, codeInternal,
			"failed to save settings")
		return
	}
	writeJSON(w, req)
}
