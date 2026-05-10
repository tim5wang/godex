//go:build windows

package background

import (
	"errors"
	"os/exec"
	"syscall"
)

func init() {
	configureProcessGroup = func(cmd *exec.Cmd) error {
		if cmd == nil {
			return nil
		}
		if cmd.SysProcAttr == nil {
			cmd.SysProcAttr = &syscall.SysProcAttr{}
		}
		cmd.SysProcAttr.CreationFlags |= syscall.CREATE_NEW_PROCESS_GROUP
		return nil
	}

	killProcessTree = func(cmd *exec.Cmd) error {
		if cmd == nil || cmd.Process == nil {
			return nil
		}
		if err := killWindowsProcessTree(cmd.Process.Pid); err != nil {
			return cmd.Process.Kill()
		}
		return nil
	}
}

// killWindowsProcessTree terminates the process identified by pid on Windows.
func killWindowsProcessTree(pid int) error {
	handle, err := syscall.OpenProcess(syscall.PROCESS_TERMINATE, false, uint32(pid))
	if err != nil {
		if errors.Is(err, syscall.ERROR_ACCESS_DENIED) {
			return nil // process already dying
		}
		return err
	}
	defer syscall.CloseHandle(handle)

	return syscall.TerminateProcess(handle, 1)
}
