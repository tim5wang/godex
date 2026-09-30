//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package processlock

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

type platformState struct{}

func lockFile(file *os.File, _ *platformState) error {
	err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB)
	if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
		return ErrLocked
	}
	return err
}

func unlockFile(file *os.File, _ *platformState) error {
	return unix.Flock(int(file.Fd()), unix.LOCK_UN)
}
