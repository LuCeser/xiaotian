//go:build linux

package pybridge

import (
	"os/exec"
	"syscall"
	"time"
)

// setProcessGroup configures cmd to start a new Linux process group and
// installs a Cancel hook that SIGKILLs the entire group on context expiry.
// This file is the Linux-only implementation; bridge_other.go provides a
// no-op stub for Windows/macOS dev hosts.
//
// Why this exists (RESEARCH Pattern 1 / Pitfall 1):
//
// exec.CommandContext's default Cancel only kills the direct child process.
// pregenerate_dictation.py spawns ffmpeg as a grandchild via
// subprocess.run(["ffmpeg", ...]). If the per-call ctx times out, the
// default Cancel leaves the orphaned ffmpeg running -- it keeps eating CPU
// and disk on the 1C/1G VPS AND keeps its inherited stdout/stderr pipes
// open, which makes cmd.Wait block (the Go exec doc verbatim:
// "might not occur until orphaned subprocesses of the command have also
// closed their descriptors").
//
// The fix has three coordinated parts:
//
//  1. cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true} -- the child
//     becomes the leader of a new process group whose PGID == child PID.
//     (GOROOT/src/syscall/exec_linux.go lines 75-91.)
//
//  2. cmd.Cancel = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) -- the
//     negative PID addresses the whole group; SIGKILL is uncatchable so
//     neither the Python child nor the ffmpeg grandchild can swallow it.
//     (GOROOT/src/os/exec/exec.go lines 251-303 document Cancel/WaitDelay.)
//
//  3. cmd.WaitDelay = 3 * time.Second -- belt-and-braces backstop: even if
//     Cancel somehow fails to make cmd.Wait return (e.g. a kernel bug or a
//     third-party SIGKILL handler that manages to defer pipe close), the
//     exec package will forcibly close the stdout/stderr pipes after this
//     delay and unblock Wait.
//
// Production deployment is on a Linux VPS, so this implementation is the
// one that actually runs in anger. The non-Linux stub exists purely so
// Windows/macOS dev hosts can `go test ./pkg/pybridge/` without a compile
// error -- and matches the same platform-split convention already used by
// cmd/service/main.go (lines 12-13: "Windows dev hosts do not support
// syscall.Kill with SIGTERM").
func setProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		// -pid addresses the process group; SIGKILL is uncatchable.
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	cmd.WaitDelay = 3 * time.Second
}
