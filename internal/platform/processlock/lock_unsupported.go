//go:build !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !solaris && !windows

package processlock

import (
	"fmt"
	"os"
	"runtime"
)

type platformState struct{}

func lockFile(_ *os.File, _ *platformState) error {
	return fmt.Errorf("process locks are unsupported on %s", runtime.GOOS)
}

func unlockFile(_ *os.File, _ *platformState) error {
	return nil
}
