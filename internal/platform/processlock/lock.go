package processlock

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

var ErrLocked = errors.New("process lock is already held")

type Lock struct {
	mu    sync.Mutex
	file  *os.File
	state platformState
}

func Acquire(path string) (*Lock, error) {
	if strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("missing lock path")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	lock := &Lock{file: file}
	if err := lockFile(file, &lock.state); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("lock %q: %w", path, err)
	}
	return lock, nil
}

func (l *Lock) Close() error {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.file == nil {
		return nil
	}
	unlockErr := unlockFile(l.file, &l.state)
	closeErr := l.file.Close()
	l.file = nil
	return errors.Join(unlockErr, closeErr)
}
