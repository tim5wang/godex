package tools

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
)

// LightpandaBinary manages the lightpanda CLI binary: path resolution, version cache, and command execution.
type LightpandaBinary struct {
	mu      sync.Mutex
	path    string // cached resolved path
	version string // cached version string
}

// NewLightpandaBinary creates a new binary manager.
func NewLightpandaBinary() *LightpandaBinary {
	return &LightpandaBinary{}
}

// ResolvePath returns the lightpanda executable path.
// Priority: cached path > explicit configured path > $PATH lookup.
// Returns error if not found.
func (b *LightpandaBinary) ResolvePath(ctx context.Context, configured, tempDir string) (string, error) {
	if b == nil {
		return "", fmt.Errorf("lightpanda binary manager is nil")
	}
	b.mu.Lock()
	defer b.mu.Unlock()

	// Return cached path if set and still exists.
	if b.path != "" {
		if _, err := os.Stat(b.path); err == nil {
			return b.path, nil
		}
		b.path = ""
	}

	// Try explicit configured path.
	configured = strings.TrimSpace(configured)
	if configured != "" {
		if resolved, err := exec.LookPath(configured); err == nil {
			b.path = resolved
			return resolved, nil
		}
		// Try as absolute path.
		if filepath.IsAbs(configured) {
			if _, err := os.Stat(configured); err == nil {
				b.path = configured
				return configured, nil
			}
		}
		return "", fmt.Errorf("lightpanda binary not found at configured path %q", configured)
	}

	// Try $PATH lookup.
	if path, err := exec.LookPath("lightpanda"); err == nil {
		b.path = path
		return path, nil
	}

	return "", fmt.Errorf("lightpanda binary not found in PATH; install it or set tools.lightpanda.binary_path")
}

// EnsureBinary resolves or auto-downloads the lightpanda binary.
// Resolution order: cached path > explicit configured path > $PATH lookup > cached download > auto-download.
// If autoDownload is true and the binary is not found locally, it downloads the nightly
// release from GitHub into a stable cache directory under tempDir.
func (b *LightpandaBinary) EnsureBinary(ctx context.Context, configured, tempDir string, autoDownload bool) (string, error) {
	if b == nil {
		return "", fmt.Errorf("lightpanda binary manager is nil")
	}
	// Try normal resolution first.
	path, err := b.ResolvePath(ctx, configured, tempDir)
	if err == nil {
		return path, nil
	}
	// If auto-download is disabled, return the original lookup error.
	if !autoDownload {
		return "", err
	}
	// Check if previously downloaded binary still exists in cache.
	cacheDir := filepath.Join(tempDir, "lightpanda")
	cachedPath := filepath.Join(cacheDir, lightpandaAssetName())
	if cachedPath != "" {
		if _, statErr := os.Stat(cachedPath); statErr == nil {
			b.mu.Lock()
			b.path = cachedPath
			b.mu.Unlock()
			return cachedPath, nil
		}
	}
	dlURL := LightpandaDownloadURL()
	if dlURL == "" {
		return "", fmt.Errorf("lightpanda auto-download not supported on %s/%s", runtime.GOOS, runtime.GOARCH)
	}
	if mkErr := os.MkdirAll(cacheDir, 0755); mkErr != nil {
		return "", fmt.Errorf("create lightpanda cache dir: %w", mkErr)
	}
	dlPath, _, dlErr := downloadFileViaHTTP(ctx, cacheDir, dlURL, lightpandaAssetName())
	if dlErr != nil {
		return "", fmt.Errorf("download lightpanda binary: %w", dlErr)
	}
	if chErr := os.Chmod(dlPath, 0755); chErr != nil {
		return "", fmt.Errorf("chmod lightpanda binary: %w", chErr)
	}
	b.mu.Lock()
	b.path = dlPath
	b.mu.Unlock()
	return dlPath, nil
}

// Version returns the lightpanda version string.
func (b *LightpandaBinary) Version(ctx context.Context) (string, error) {
	if b == nil {
		return "", fmt.Errorf("lightpanda binary manager is nil")
	}
	b.mu.Lock()
	if b.version != "" {
		ver := b.version
		b.mu.Unlock()
		return ver, nil
	}
	b.mu.Unlock()

	path, err := b.ResolvePath(ctx, "", "")
	if err != nil {
		return "", err
	}
	cmd := exec.CommandContext(ctx, path, "version")
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("lightpanda version: %w", err)
	}
	ver := strings.TrimSpace(string(out))
	b.mu.Lock()
	b.version = ver
	b.mu.Unlock()
	return ver, nil
}

// fetchConfig holds options for a lightpanda fetch call.
type fetchConfig struct {
	WaitUntil   string
	WaitMS      int
	WaitSelector string
	ObeyRobots  bool
	LogLevel    string
}

func defaultFetchConfig() fetchConfig {
	return fetchConfig{
		WaitUntil: "networkidle",
		LogLevel:  "warn",
	}
}

// FetchOption configures a lightpanda fetch invocation.
type FetchOption func(*fetchConfig)

// WithFetchWaitUntil sets the --wait-until flag.
func WithFetchWaitUntil(s string) FetchOption {
	return func(c *fetchConfig) { c.WaitUntil = s }
}

// WithFetchWaitMS sets the --wait-ms flag.
func WithFetchWaitMS(ms int) FetchOption {
	return func(c *fetchConfig) { c.WaitMS = ms }
}

// WithFetchWaitSelector sets the --wait-selector flag.
func WithFetchWaitSelector(sel string) FetchOption {
	return func(c *fetchConfig) { c.WaitSelector = sel }
}

// WithFetchObeyRobots enables the --obey-robots flag.
func WithFetchObeyRobots() FetchOption {
	return func(c *fetchConfig) { c.ObeyRobots = true }
}

// WithFetchLogLevel sets the --log-level flag.
func WithFetchLogLevel(level string) FetchOption {
	return func(c *fetchConfig) { c.LogLevel = level }
}

// FetchDump runs `lightpanda fetch --dump <format> [options] <url>` and returns stdout.
// The ctx controls timeout/cancellation. Stderr is captured on error.
func (b *LightpandaBinary) FetchDump(ctx context.Context, targetURL, format string, opts ...FetchOption) (string, error) {
	if b == nil {
		return "", fmt.Errorf("lightpanda binary manager is nil")
	}
	path, err := b.ResolvePath(ctx, "", "")
	if err != nil {
		return "", err
	}

	format = strings.TrimSpace(format)
	if format == "" {
		format = "markdown"
	}

	cfg := defaultFetchConfig()
	for _, opt := range opts {
		opt(&cfg)
	}

	args := []string{"fetch", "--dump", format}
	if cfg.WaitUntil != "" {
		args = append(args, "--wait-until", cfg.WaitUntil)
	}
	if cfg.WaitMS > 0 {
		args = append(args, "--wait-ms", strconv.Itoa(cfg.WaitMS))
	}
	if cfg.WaitSelector != "" {
		args = append(args, "--wait-selector", cfg.WaitSelector)
	}
	if cfg.ObeyRobots {
		args = append(args, "--obey-robots")
	}
	if cfg.LogLevel != "" {
		args = append(args, "--log-level", cfg.LogLevel)
	}
	args = append(args, targetURL)

	cmd := exec.CommandContext(ctx, path, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		stderrStr := strings.TrimSpace(stderr.String())
		if stderrStr != "" {
			return "", fmt.Errorf("lightpanda fetch failed: %w: %s", err, stderrStr)
		}
		return "", fmt.Errorf("lightpanda fetch failed: %w", err)
	}
	return stdout.String(), nil
}

// LightpandaAssetName returns the nightly release asset filename for the given OS/arch.
// Returns empty string for unsupported platforms.
func LightpandaAssetName(goos, arch string) string {
	switch goos {
	case "darwin":
		if arch == "arm64" {
			return "lightpanda-aarch64-macos"
		}
		return "lightpanda-x86_64-macos"
	case "linux":
		if arch == "arm64" {
			return "lightpanda-aarch64-linux"
		}
		return "lightpanda-x86_64-linux"
	default:
		return ""
	}
}

// lightpandaAssetName is an unexported convenience using runtime values.
func lightpandaAssetName() string {
	return LightpandaAssetName(runtime.GOOS, runtime.GOARCH)
}

var lightpandaDownloadBase = "https://github.com/lightpanda-io/browser/releases/download/nightly"

// DownloadURL returns the download URL for the current platform's nightly binary.
func LightpandaDownloadURL() string {
	asset := lightpandaAssetName()
	if asset == "" {
		return ""
	}
	return lightpandaDownloadBase + "/" + asset
}
