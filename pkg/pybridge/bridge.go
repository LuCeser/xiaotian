// Bridge is the concrete Executor implementation that runs vendored Python
// scripts (vocab_manager.py / pregenerate_dictation.py) as short-lived
// subprocesses (D-01 spawn-per-call). It owns the bridge's only mutable state:
// the operational config, the structured logger, and the global bounded
// semaphore (D-02). The command methods (RunDue/RunReview/RunPregenerate)
// live in vocab.go and pregenerate.go (Plan 03-04 / 03-05) and reuse this
// file's run/logResult/childEnv helpers.
//
// This file's scope (Plan 03-03):
//   - the Bridge struct + compile-time Executor satisfaction check
//   - NewBridge: pure constructor (cfg validation + nil-logger substitution +
//     internal semaphore construction). NO subprocess spawn, NO file IO at
//     construction time -- cmd/service/main.go builds the Bridge at boot and
//     the boot tests construct it with interpreter="python3" without a real
//     Python present.
//   - childEnv: child process env construction, including the D-05 secret
//     forwarding from os.Getenv.
//   - run: exec.CommandContext (argv slice, never shell) + setProcessGroup
//     (build-tag dispatched) + WaitDelay (Linux only) + semaphore gating.
//   - logResult: 5-class structured-log classification via infra.Logger.
//
// Platform note: setProcessGroup is implemented twice, behind //go:build linux
// and //go:build !linux tags (see bridge_linux.go / bridge_other.go). The
// bridge.go file itself imports no syscall package and compiles on every
// platform Go supports.
package pybridge

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"time"

	"github.com/pkg/errors"

	"github.com/xiaotian/home-tutor-agent/pkg/infra"
)

// Compile-time interface satisfaction check (D-43). If Bridge ever drifts
// from the Executor contract this fails the build at compile time rather than
// surfacing as a runtime panic. The full RunDue/RunReview/RunPregenerate
// implementations live in vocab.go and pregenerate.go (Plan 03-04 / 03-05);
// until those land, the stub implementations at the bottom of this file
// keep the build green and the interface contract enforced.
var _ Executor = (*Bridge)(nil)

// ErrBridgeNotImplemented is the sentinel returned by the remaining Bridge
// command method stub. Plan 03-04 replaced RunDue/RunReview with real
// implementations in vocab.go; only RunPregenerate remains as a stub until
// Plan 03-05 lands (pregenerate.go). The stub exists solely so the
// `var _ Executor = (*Bridge)(nil)` compile-time check above passes -- callers
// MUST NOT depend on this error in production code. Plan 03-05 will delete
// this sentinel together with the RunPregenerate stub.
//
// Detection via errors.Is(err, ErrBridgeNotImplemented) is supported but
// should never be needed outside this package's own tests.
var ErrBridgeNotImplemented = errors.New("pybridge: bridge command method not yet implemented (Plan 03-05).")

// pythonSecretEnvKeys is the fixed allowlist of VOLCENGINE_TTS_* secret env
// vars the bridge forwards from the parent process to the Python subprocess
// (D-05). Listed here so the security review surface is one place. The values
// are NEVER placed in PythonConfig, NEVER written to logs, and NEVER included
// in ExitError fields -- they only ride in cmd.Env.
//
// RESOURCE_ID is included alongside the three documented in CONTEXT D-05
// because pregenerate_dictation.py reads it from the live environment when
// constructing Volcengine TTS requests; missing it causes the script to emit
// a non-zero-exit error during TTS synthesis, so forwarding is the
// correctness-affirming default.
var pythonSecretEnvKeys = []string{
	"VOLCENGINE_TTS_API_KEY",
	"VOLCENGINE_TTS_APP_ID",
	"VOLCENGINE_TTS_ACCESS_KEY",
	"VOLCENGINE_TTS_RESOURCE_ID",
}

// Bridge is the concrete Executor that spawns Python subprocesses. It is safe
// for concurrent use: every run() call goes through the D-02 semaphore so
// cfg.MaxConcurrent sets the hard cap on simultaneously running Python
// processes, defending the 1C/1G VPS from fork-bombing under 50-100
// concurrent sessions.
//
// Field invariants:
//   - cfg holds the operational config (interpreter path, vendored script
//     paths, runtime data/cache dirs, per-call timeouts, MaxConcurrent).
//     Treated as immutable after construction; never mutated by run().
//   - logger is always non-nil after NewBridge (nil callers get
//     infra.NewNoOpLogger() substituted in). All log entries route through
//     it via infra.F() / infra.ErrField() so structured fields are consistent
//     across the pybridge subsystem (D-25 canonical, not raw *zap.Logger).
//   - sem is the internal D-02 semaphore constructed from cfg.MaxConcurrent.
//     Acquired before every subprocess Start and released after Wait.
type Bridge struct {
	// cfg is the operational PythonConfig captured at construction. Read-only
	// after NewBridge returns.
	cfg infra.PythonConfig

	// logger is the structured event sink (D-25, D-47). Substituted with
	// NewNoOpLogger() when the caller passes nil so the bridge is always
	// observable.
	logger infra.Logger

	// sem caps simultaneous Python subprocesses (D-02). Built from
	// cfg.MaxConcurrent at construction; not exposed outside the package.
	sem semaphore
}

// NewBridge is the Bridge constructor (D-42 constructor DI). It validates
// every operational cfg field (defense-in-depth on top of
// PythonConfig.Validate -- PythonConfig.Validate already enforces these
// rules at LoadConfig time, but a caller that hand-constructs a PythonConfig
// literal must hit the same wall here, not at the first run() call),
// substitutes infra.NewNoOpLogger() for a nil logger (D-47 always non-nil
// observable), and builds the internal D-02 semaphore from
// cfg.MaxConcurrent.
//
// NewBridge is a PURE CONSTRUCTOR: it spawns no subprocess and performs no
// file IO. cmd/service/main.go calls it at boot; the cmd/service boot tests
// construct the Bridge with interpreter="python3" and no real Python
// interpreter present and expect the construction to succeed.
//
// The shape mirrors pkg/infra/connection.go's NewConnectionRegistry
// (lines 251-272): field-by-field cfg validation returning
// errors.Wrap(infra.ErrConfigInvalid, ...); nil-logger substitution; pure
// state init.
func NewBridge(cfg infra.PythonConfig, logger infra.Logger) (*Bridge, error) {
	if cfg.Interpreter == "" {
		return nil, errors.Wrap(infra.ErrConfigInvalid,
			"python.interpreter is required for Bridge")
	}
	if cfg.ScriptsDir == "" {
		return nil, errors.Wrap(infra.ErrConfigInvalid,
			"python.scriptsDir is required for Bridge")
	}
	if cfg.VocabScript == "" {
		return nil, errors.Wrap(infra.ErrConfigInvalid,
			"python.vocabScript is required for Bridge")
	}
	if cfg.PregenScript == "" {
		return nil, errors.Wrap(infra.ErrConfigInvalid,
			"python.pregenScript is required for Bridge")
	}
	if cfg.MaxConcurrent < 1 || cfg.MaxConcurrent > 4 {
		return nil, errors.Wrapf(infra.ErrConfigInvalid,
			"python.maxConcurrent must be in [1, 4] for Bridge, got %d",
			cfg.MaxConcurrent)
	}
	if cfg.DueTimeout <= 0 {
		return nil, errors.Wrap(infra.ErrConfigInvalid,
			"python.dueTimeout must be > 0 for Bridge")
	}
	if cfg.ReviewTimeout <= 0 {
		return nil, errors.Wrap(infra.ErrConfigInvalid,
			"python.reviewTimeout must be > 0 for Bridge")
	}
	if cfg.PregenTimeout <= 0 {
		return nil, errors.Wrap(infra.ErrConfigInvalid,
			"python.pregenTimeout must be > 0 for Bridge")
	}
	if logger == nil {
		logger = infra.NewNoOpLogger()
	}
	return &Bridge{
		cfg:    cfg,
		logger: logger,
		sem:    newSemaphore(cfg.MaxConcurrent),
	}, nil
}

// childEnv builds the child process env slice (the "KEY=VALUE" string list
// exec.Cmd expects) for a Python subprocess invocation.
//
// Composition (D-05 secrets-via-os.Getenv-not-in-Config):
//  1. os.Environ() -- inherit the parent's env so the operator-supplied
//     VOLCENGINE_TTS_* secrets and any system PATH reach the subprocess.
//  2. PYTHONUTF8=1 + PYTHONIOENCODING=utf-8 + PYTHONUNBUFFERED=1 -- force
//     UTF-8 on stdin/stdout/stderr so Chinese vocabulary never mojibakes
//     through a non-UTF-8 locale (RESEARCH Pitfall: Windows GBK; also
//     defence for any posix_C_cc locale on the VPS). PYTHONUNBUFFERED=1
//     ensures stdout/stderr flush promptly so the bridge's bytes.Buffer
//     captures everything before Wait returns (Rule 2: correctness).
//  3. DICTATION_* operational paths from cfg -- the upstream Python scripts
//     read these to locate vocab.db, the data dir, the cache dir, the
//     manifest dir, and the optional non-secret .env file.
//  4. VOLCENGINE_TTS_* secrets -- forwarded from os.Getenv (NEVER from cfg).
//     Only non-empty values are appended so we do not pollute the child env
//     with empty placeholders. These names live in pythonSecretEnvKeys so
//     the security-review surface is one place.
//  5. extra -- caller-supplied additional entries (currently unused; kept
//     for Plan 03-04/03-05 callers that may need to inject DICTATION_*
//     overrides per-call).
//
// SECURITY: the returned slice carries secret values; it MUST be passed
// directly to cmd.Env and never logged (T-03-08). The logResult helper
// below hard-codes a field whitelist that excludes env entirely.
func (b *Bridge) childEnv(extra ...string) []string {
	env := os.Environ()
	env = append(env,
		"PYTHONUTF8=1",
		"PYTHONIOENCODING=utf-8",
		"PYTHONUNBUFFERED=1",
	)
	if b.cfg.VocabDBPath != "" {
		env = append(env, "DICTATION_DB_PATH="+b.cfg.VocabDBPath)
	}
	if b.cfg.DataDir != "" {
		env = append(env, "DICTATION_DATA_DIR="+b.cfg.DataDir)
	}
	if b.cfg.CacheDir != "" {
		env = append(env, "DICTATION_CACHE_DIR="+b.cfg.CacheDir)
	}
	if b.cfg.ManifestDir != "" {
		env = append(env, "DICTATION_MANIFEST_DIR="+b.cfg.ManifestDir)
	}
	if b.cfg.EnvFile != "" {
		env = append(env, "DICTATION_ENV_FILE="+b.cfg.EnvFile)
	}
	// D-05: secret forwarding from parent env. See pythonSecretEnvKeys doc.
	for _, k := range pythonSecretEnvKeys {
		if v := os.Getenv(k); v != "" {
			env = append(env, k+"="+v)
		}
	}
	env = append(env, extra...)
	return env
}

// run spawns one Python subprocess via exec.CommandContext with argv slice
// semantics (NEVER shell-string -- T-03-07 tampering mitigation), gates it
// behind the D-02 semaphore, applies the per-call timeout, and returns the
// captured stdout / stderr / exit code / err.
//
// Lifecycle (per call, D-01 spawn-per-call):
//  1. sem.acquire(ctx) -- block until a slot is free or ctx expires. On
//     ctx expiry returns ctx.Err() (no slot consumed, no subprocess spawned);
//     the caller can translate this into a backpressure signal.
//  2. defer sem.release() -- guaranteed slot return.
//  3. cctx, cancel := context.WithTimeout(ctx, timeout) -- a per-call
//     timeout layered on top of the parent ctx. Either trips the Cancel
//     hook below.
//  4. exec.CommandContext(cctx, name, args...) -- argv slice, no shell
//     interpolation. Args from DueOpts/ReviewOpts/PregenOpts pass as
//     standalone argv elements; argparse sees them as values, never as
//     shell commands.
//  5. setProcessGroup(cmd) -- build-tag dispatched (Linux sets Setpgid +
//     Cancel + WaitDelay; non-Linux is a no-op and relies on exec's default
//     Cancel + the standard ctx-kill path).
//  6. cmd.Env = childEnv() -- UTF-8 + DICTATION_* + VOLCENGINE_TTS_*.
//  7. cmd.Stdout/stderr = bytes.Buffer -- capture for logResult.
//  8. cmd.Run() + capture start time for the duration_ms log field.
//  9. logResult(...) -- classify the outcome into one structured log entry.
//
// Return values:
//   - stdout: the subprocess's stdout bytes (may be empty on error)
//   - stderr: the subprocess's stderr bytes (may carry the script's
//     diagnostics)
//   - exitCode: ProcessState.ExitCode(), or 0 if ProcessState is nil (e.g.
//     cancelled before Start)
//   - err: nil on success; context.DeadlineExceeded on timeout; *exec.ExitError
//     on non-zero exit; exec.ErrWaitDelay if WaitDelay closes the pipes
//
// The caller (RunDue/RunReview/RunPregenerate in vocab.go / pregenerate.go)
// is responsible for wrapping the returned err into the appropriate
// pybridge sentinel (ErrPythonTimeout / ErrPythonExit) and parsing stdout
// via extractTrailingJSON or splitNonEmptyLines.
func (b *Bridge) run(ctx context.Context, timeout time.Duration, name string, args ...string) ([]byte, []byte, int, error) {
	// Gate the subprocess through the D-02 semaphore BEFORE setting up the
	// per-call timeout. An already-cancelled parent ctx fails fast here
	// without consuming a slot.
	if err := b.sem.acquire(ctx); err != nil {
		return nil, nil, -1, err
	}
	defer b.sem.release()

	// Layer the per-call timeout on top of the parent ctx. Either the parent
	// ctx or this timeout will fire the Cancel hook set by setProcessGroup.
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// exec.CommandContext takes argv as variadic args -- there is no shell
	// interpreting the values, so a malicious word containing shell
	// metacharacters cannot escape its argv slot (T-03-07).
	cmd := exec.CommandContext(cctx, name, args...)

	// Build-tag dispatched: Linux sets SysProcAttr + Cancel + WaitDelay,
	// non-Linux is a no-op (process-group kill is a Linux-only concept
	// because non-Linux lacks syscall.SysProcAttr.Setpgid).
	setProcessGroup(cmd)

	cmd.Env = b.childEnv()

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	start := time.Now()
	err := cmd.Run()

	exitCode := 0
	if cmd.ProcessState != nil {
		exitCode = cmd.ProcessState.ExitCode()
	}

	b.logResult(name, start, stdout.Bytes(), stderr.Bytes(), exitCode, err)
	return stdout.Bytes(), stderr.Bytes(), exitCode, err
}

// logResult classifies a run() outcome into exactly one structured log entry
// (success criterion 4). The classification is exhaustive over the (err, exit)
// space:
//
//   - err == nil && exit == 0            -> Debug "python subprocess ok"
//   - errors.Is(err, DeadlineExceeded)   -> Error "python subprocess timed out"
//     (error_type=timeout, +stderr)
//   - exit != 0                          -> Error "python subprocess failed"
//     (error_type=nonzero_exit, +stderr, +stdout)
//   - default (e.g. ErrWaitDelay, ctx
//     cancelled before Start)            -> Error "python subprocess error"
//     (error_type=other, +infra.ErrField(err))
//
// SECURITY (T-03-08): the field set is a fixed whitelist -- component,
// command, exit_code, duration_ms, error_type, stderr, stdout. The Python
// subprocess may print arbitrary text to stderr/stdout (including upstream
// Volcengine API diagnostics), but the structured env (carrying
// VOLCENGINE_TTS_* secrets) is NEVER included. The pythonSecretEnvKeys doc
// above and the explicit field list below are the two review surfaces for
// this mitigation.
//
// All entries carry component="pybridge" so operators can filter pybridge
// logs from websocket / TTS / infra logs (D-25 component convention).
func (b *Bridge) logResult(method string, start time.Time, stdout, stderr []byte, exit int, err error) {
	fields := []infra.Field{
		infra.F("component", "pybridge"),
		infra.F("command", method),
		infra.F("exit_code", exit),
		infra.F("duration_ms", time.Since(start).Milliseconds()),
	}
	switch {
	case err == nil && exit == 0:
		b.logger.Debug("python subprocess ok", fields...)
	case errors.Is(err, context.DeadlineExceeded):
		fields = append(fields,
			infra.F("error_type", "timeout"),
			infra.F("stderr", string(stderr)),
		)
		b.logger.Error("python subprocess timed out", fields...)
	case exit != 0:
		fields = append(fields,
			infra.F("error_type", "nonzero_exit"),
			infra.F("stderr", string(stderr)),
			infra.F("stdout", string(stdout)),
		)
		b.logger.Error("python subprocess failed", fields...)
	default:
		fields = append(fields,
			infra.F("error_type", "other"),
			infra.ErrField(err),
		)
		b.logger.Error("python subprocess error", fields...)
	}
}

// RunPregenerate is a STUB. The real implementation (executes
// `pregenerate_dictation.py generate ...` and parses the line-based mp3
// path output via splitNonEmptyLines) lands in Plan 03-05 (pregenerate.go).
// See RunDue for the rationale.
func (*Bridge) RunPregenerate(ctx context.Context, _ PregenOpts) (PregenResult, error) {
	return PregenResult{}, errors.Wrap(ErrBridgeNotImplemented, "RunPregenerate")
}
