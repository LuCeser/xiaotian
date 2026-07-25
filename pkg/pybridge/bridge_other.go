//go:build !linux

package pybridge

import "os/exec"

// setProcessGroup is the non-Linux stub. It intentionally does nothing.
//
// syscall.SysProcAttr.Setpgid is a Linux-only field -- on Windows/macOS the
// field does not exist, so any reference to it is a compile error on those
// platforms. This stub lets Windows/macOS dev hosts build the package and
// run the (fake-driven) unit tests so the platform split does not break the
// dev inner loop.
//
// Production deploys on a Linux VPS pick up the real implementation in
// bridge_linux.go. The non-Linux path relies on exec.CommandContext's
// DEFAULT Cancel (which calls Process.Kill on the direct child) plus the
// ctx-cancellation path -- fine for the fake-driven tests, which never
// spawn an ffmpeg grandchild. This is the same platform-split convention
// already documented at cmd/service/main.go lines 12-13 (Windows dev hosts
// do not support syscall.Kill with SIGTERM).
//
// bridge.go's run() also never installs its own Cancel when this stub is
// selected, so the default exec behavior applies unchanged.
func setProcessGroup(cmd *exec.Cmd) {}
