package app

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/tim5wang/godex/internal/core/config"
)

type fakeConfigReloader struct {
	meta config.Meta

	mu      sync.Mutex
	reloads int
}

func (f *fakeConfigReloader) Meta() config.Meta {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.meta
}

func (f *fakeConfigReloader) ReloadFromDisk(context.Context) (config.View, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reloads++
	return config.View{}, nil
}

func (f *fakeConfigReloader) ReloadCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.reloads
}

func TestConfigReloadWatcherReloadsWhenConfigFileChanges(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "godex.yaml")
	if err := os.WriteFile(configPath, []byte("lead_name: old\n"), 0600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	reloader := &fakeConfigReloader{meta: config.Meta{HomeConfigFile: configPath}}
	watcher := NewConfigReloadWatcher(reloader)
	watcher.Interval = 10 * time.Millisecond

	if err := watcher.Start(t.Context()); err != nil {
		t.Fatalf("start watcher: %v", err)
	}
	defer watcher.Stop(context.Background())

	time.Sleep(30 * time.Millisecond)
	if got := reloader.ReloadCount(); got != 0 {
		t.Fatalf("expected no initial reload, got %d", got)
	}

	if err := os.WriteFile(configPath, []byte("lead_name: new\n"), 0600); err != nil {
		t.Fatalf("rewrite config: %v", err)
	}
	waitForReloadCount(t, reloader, 1)
}

func TestConfigReloadWatcherReloadsWhenEnvFileIsCreated(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "godex.yaml")
	envPath := filepath.Join(dir, ".env")
	if err := os.WriteFile(configPath, []byte("lead_name: old\n"), 0600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	reloader := &fakeConfigReloader{meta: config.Meta{
		HomeConfigFile: configPath,
		HomeEnvFile:    envPath,
	}}
	watcher := NewConfigReloadWatcher(reloader)
	watcher.Interval = 10 * time.Millisecond

	if err := watcher.Start(t.Context()); err != nil {
		t.Fatalf("start watcher: %v", err)
	}
	defer watcher.Stop(context.Background())

	if err := os.WriteFile(envPath, []byte("WEIXIN_ENABLED=true\n"), 0600); err != nil {
		t.Fatalf("write env: %v", err)
	}
	waitForReloadCount(t, reloader, 1)
}

func waitForReloadCount(t *testing.T, reloader *fakeConfigReloader, want int) {
	t.Helper()
	deadline := time.After(time.Second)
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-deadline:
			t.Fatalf("timed out waiting for reload count %d, got %d", want, reloader.ReloadCount())
		case <-ticker.C:
			if got := reloader.ReloadCount(); got >= want {
				return
			}
		}
	}
}
