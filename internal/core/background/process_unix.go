//go:build unix

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
		cmd.SysProcAttr.Setpgid = true
		return nil
	}

	killProcessTree = func(cmd *exec.Cmd) error {
		if cmd == nil || cmd.Process == nil {
			return nil
		}
		if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
			return err
		}
		return nil
	}
}
