//go:build windows

package tooling

import (
	"os/exec"
	"syscall"
)

// configureCommandProcessGroup gives the child a distinct Windows process
// group. Process.Kill still terminates the direct child; the group prevents
// console-control signals from leaking to the GoDex process.
func configureCommandProcessGroup(cmd *exec.Cmd) error {
	if cmd == nil {
		return nil
	}
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.CreationFlags |= syscall.CREATE_NEW_PROCESS_GROUP
	return nil
}

// killCommandProcessGroup terminates the direct child. Windows has no
// process groups; cmd.Process.Kill() terminates the single process.
func killCommandProcessGroup(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	_ = cmd.Process.Kill()
}
