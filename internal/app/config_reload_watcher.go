package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/tim5wang/godex/internal/core/config"
	"github.com/tim5wang/godex/internal/platform/logger"
)

var configReloadLog = logger.New("config_reload")

type configReloader interface {
	Meta() config.Meta
	ReloadFromDisk(context.Context) (config.View, error)
}

type watchedFileState struct {
	Exists  bool
	ModTime time.Time
	Size    int64
}

// ConfigReloadWatcher polls the known config and env layers and asks the
// config manager to apply changes. It is intentionally lightweight so service
// deployments pick up direct file edits without platform-specific watchers.
type ConfigReloadWatcher struct {
	Interval time.Duration

	reloader configReloader

	mu     sync.Mutex
	cancel context.CancelFunc
	done   chan struct{}
}

func NewConfigReloadWatcher(reloader configReloader) *ConfigReloadWatcher {
	return &ConfigReloadWatcher{
		Interval: 2 * time.Second,
		reloader: reloader,
	}
}

func (w *ConfigReloadWatcher) Start(ctx context.Context) error {
	if w == nil || w.reloader == nil {
		return nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.cancel != nil {
		return nil
	}
	runCtx, cancel := context.WithCancel(ctx)
	w.cancel = cancel
	w.done = make(chan struct{})
	previous := snapshotWatchedFiles(w.watchPaths())
	go w.run(runCtx, w.done, previous)
	return nil
}

func (w *ConfigReloadWatcher) Stop(ctx context.Context) error {
	if w == nil {
		return nil
	}
	w.mu.Lock()
	cancel := w.cancel
	done := w.done
	w.cancel = nil
	w.done = nil
	w.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	if done == nil {
		return nil
	}
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (w *ConfigReloadWatcher) run(ctx context.Context, done chan<- struct{}, previous map[string]watchedFileState) {
	defer close(done)
	interval := w.Interval
	if interval <= 0 {
		interval = 2 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			paths := w.watchPaths()
			current := snapshotWatchedFiles(paths)
			if watchedFileStatesEqual(previous, current) {
				continue
			}
			if _, err := w.reloader.ReloadFromDisk(ctx); err != nil {
				configReloadLog.Warnf("reload config after file change failed: %v", err)
			} else {
				configReloadLog.Infof("reloaded config after file change")
			}
			previous = snapshotWatchedFiles(w.watchPaths())
		}
	}
}

func (w *ConfigReloadWatcher) watchPaths() []string {
	meta := w.reloader.Meta()
	raw := []string{
		meta.HomeConfigFile,
		meta.ProjectConfigFile,
		meta.HomeEnvFile,
		meta.ProjectEnvFile,
	}
	seen := make(map[string]struct{}, len(raw))
	out := make([]string, 0, len(raw))
	for _, path := range raw {
		path = filepath.Clean(path)
		if path == "." || path == "" {
			continue
		}
		if _, ok := seen[path]; ok {
			continue
		}
		seen[path] = struct{}{}
		out = append(out, path)
	}
	return out
}

func snapshotWatchedFiles(paths []string) map[string]watchedFileState {
	out := make(map[string]watchedFileState, len(paths))
	for _, path := range paths {
		stat, err := os.Stat(path)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				out[path] = watchedFileState{}
				continue
			}
			out[path] = watchedFileState{Exists: true}
			continue
		}
		out[path] = watchedFileState{
			Exists:  true,
			ModTime: stat.ModTime(),
			Size:    stat.Size(),
		}
	}
	return out
}

func watchedFileStatesEqual(a, b map[string]watchedFileState) bool {
	if len(a) != len(b) {
		return false
	}
	for path, left := range a {
		right, ok := b[path]
		if !ok {
			return false
		}
		if left.Exists != right.Exists || left.Size != right.Size || !left.ModTime.Equal(right.ModTime) {
			return false
		}
	}
	return true
}
