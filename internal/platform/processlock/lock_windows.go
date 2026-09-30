//go:build windows

package processlock

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

type platformState struct {
	overlapped windows.Overlapped
}

func lockFile(file *os.File, state *platformState) error {
	err := windows.LockFileEx(
		windows.Handle(file.Fd()),
		windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY,
		0,
		1,
		0,
		&state.overlapped,
	)
	if errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
		return ErrLocked
	}
	return err
}

func unlockFile(file *os.File, state *platformState) error {
	return windows.UnlockFileEx(windows.Handle(file.Fd()), 0, 1, 0, &state.overlapped)
}
