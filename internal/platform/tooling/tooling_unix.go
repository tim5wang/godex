//go:build !windows

package tooling

import (
	"os/exec"
	"runtime"
	"syscall"
)

// configureCommandProcessGroup places the child in its own process group so
// the whole tree can be killed together (POSIX process groups).
//
// Android seccomp (notably MIUI/HyperOS) blocks setpgid(2): cmd.Start() then
// fails with SIGSYS ("bad system call") and every shell command reports
// ExitCode=-1. Skip process-group setup on Android; killCommandProcessGroup
// still falls back to killing the direct child on cancellation.
func configureCommandProcessGroup(cmd *exec.Cmd) error {
	if cmd == nil {
		return nil
	}
	if runtime.GOOS == "android" {
		return nil
	}
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
	return nil
}

// killCommandProcessGroup terminates the child and every descendant. The
// negative pid addresses the process group created by Setpgid.
func killCommandProcessGroup(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil {
		// Fall back to the direct child so cancellation still makes progress.
		_ = cmd.Process.Kill()
	}
}
