//go:build windows

package tooling

import "os/exec"

// configureCommandProcessGroup is a no-op on Windows: there are no POSIX
// process groups, so the child runs in the default job and cancellation
// falls back to killing the direct child only.
func configureCommandProcessGroup(cmd *exec.Cmd) error {
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
