package tools

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestResolvePath_ExplicitConfig(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "lightpanda")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\necho v0.0.1\n"), 0755); err != nil {
		t.Fatal(err)
	}
	b := &LightpandaBinary{}
	got, err := b.ResolvePath(context.Background(), bin, dir)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got != bin {
		t.Fatalf("expected %q, got %q", bin, got)
	}
}

func TestResolvePath_ExplicitConfigNotFound(t *testing.T) {
	b := &LightpandaBinary{}
	_, err := b.ResolvePath(context.Background(), "/nonexistent/lightpanda", t.TempDir())
	if err == nil {
		t.Fatal("expected error for nonexistent path")
	}
}

func TestResolvePath_InPATH(t *testing.T) {
	// Create a fake lightpanda in a temp bin dir and add to PATH.
	dir := t.TempDir()
	bin := filepath.Join(dir, "lightpanda")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\necho v0.0.1\n"), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	b := &LightpandaBinary{}
	got, err := b.ResolvePath(context.Background(), "", dir)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got != bin {
		t.Fatalf("expected %q, got %q", bin, got)
	}
}

func TestResolvePath_CachedPath(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "lightpanda")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\necho v0.0.1\n"), 0755); err != nil {
		t.Fatal(err)
	}
	b := &LightpandaBinary{path: bin}
	got, err := b.ResolvePath(context.Background(), "", dir)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got != bin {
		t.Fatalf("expected cached path %q, got %q", bin, got)
	}
}

func TestResolvePath_NotFound(t *testing.T) {
	// Clear PATH so nothing is found.
	t.Setenv("PATH", "")
	b := &LightpandaBinary{}
	_, err := b.ResolvePath(context.Background(), "", t.TempDir())
	if err == nil {
		t.Fatal("expected error when lightpanda not found")
	}
}

func TestFetchDump_BuildsCorrectArgs(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on windows")
	}
	dir := t.TempDir()
	// Create a mock lightpanda script that prints its args to stdout.
	mock := filepath.Join(dir, "lightpanda")
	script := `#!/bin/sh
echo "$@"
`
	if err := os.WriteFile(mock, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	b := &LightpandaBinary{path: mock}
	content, err := b.FetchDump(context.Background(), "https://example.com", "markdown",
		WithFetchWaitUntil("networkidle"),
		WithFetchWaitMS(1500),
		WithFetchObeyRobots(),
		WithFetchLogLevel("info"),
	)
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	// Verify args were passed correctly.
	if !strings.Contains(content, "fetch") {
		t.Errorf("expected 'fetch' in args, got %q", content)
	}
	if !strings.Contains(content, "--dump") {
		t.Errorf("expected '--dump' in args, got %q", content)
	}
	if !strings.Contains(content, "markdown") {
		t.Errorf("expected 'markdown' in args, got %q", content)
	}
	if !strings.Contains(content, "--wait-until networkidle") {
		t.Errorf("expected '--wait-until networkidle', got %q", content)
	}
	if !strings.Contains(content, "--wait-ms 1500") {
		t.Errorf("expected '--wait-ms 1500', got %q", content)
	}
	if !strings.Contains(content, "--obey-robots") {
		t.Errorf("expected '--obey-robots', got %q", content)
	}
	if !strings.Contains(content, "--log-level info") {
		t.Errorf("expected '--log-level info', got %q", content)
	}
	if !strings.Contains(content, "https://example.com") {
		t.Errorf("expected URL in args, got %q", content)
	}
}

func TestFetchDump_ReturnsStdout(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on windows")
	}
	dir := t.TempDir()
	mock := filepath.Join(dir, "lightpanda")
	if err := os.WriteFile(mock, []byte("#!/bin/sh\nread -r line\necho '# Hello World'\n"), 0755); err != nil {
		t.Fatal(err)
	}
	b := &LightpandaBinary{path: mock}
	content, err := b.FetchDump(context.Background(), "https://example.com", "markdown")
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if !strings.Contains(content, "Hello World") {
		t.Errorf("expected 'Hello World' in output, got %q", content)
	}
}

func TestFetchDump_Timeout(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on windows")
	}
	dir := t.TempDir()
	mock := filepath.Join(dir, "lightpanda")
	// Script that sleeps forever.
	if err := os.WriteFile(mock, []byte("#!/bin/sh\nsleep 3600\n"), 0755); err != nil {
		t.Fatal(err)
	}
	b := &LightpandaBinary{path: mock}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, err := b.FetchDump(ctx, "https://example.com", "markdown")
	if err == nil {
		t.Fatal("expected timeout error")
	}
}

func TestFetchDump_ExitCode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on windows")
	}
	dir := t.TempDir()
	mock := filepath.Join(dir, "lightpanda")
	if err := os.WriteFile(mock, []byte("#!/bin/sh\nexit 1\n"), 0755); err != nil {
		t.Fatal(err)
	}
	b := &LightpandaBinary{path: mock}
	_, err := b.FetchDump(context.Background(), "https://example.com", "markdown")
	if err == nil {
		t.Fatal("expected error for exit code 1")
	}
}

func TestFetchDump_StderrCaptured(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on windows")
	}
	dir := t.TempDir()
	mock := filepath.Join(dir, "lightpanda")
	script := `#!/bin/sh
echo "some error" >&2
exit 1
`
	if err := os.WriteFile(mock, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	b := &LightpandaBinary{path: mock}
	_, err := b.FetchDump(context.Background(), "https://example.com", "markdown")
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "some error") {
		t.Errorf("expected stderr in error, got %q", err.Error())
	}
}

func TestFetchDump_DefaultFormat(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on windows")
	}
	dir := t.TempDir()
	mock := filepath.Join(dir, "lightpanda")
	if err := os.WriteFile(mock, []byte("#!/bin/sh\necho \"$@\"\n"), 0755); err != nil {
		t.Fatal(err)
	}
	b := &LightpandaBinary{path: mock}
	content, err := b.FetchDump(context.Background(), "https://example.com", "")
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if !strings.Contains(content, "markdown") {
		t.Errorf("expected default format 'markdown', got %q", content)
	}
}

func TestFetchDump_NilBinary(t *testing.T) {
	var b *LightpandaBinary
	_, err := b.FetchDump(context.Background(), "https://example.com", "markdown")
	if err == nil {
		t.Fatal("expected error for nil binary")
	}
}

func TestFetchDump_BinaryNotResolved(t *testing.T) {
	b := &LightpandaBinary{} // no path set, no PATH available
	t.Setenv("PATH", "")
	_, err := b.FetchDump(context.Background(), "https://example.com", "markdown")
	if err == nil {
		t.Fatal("expected error when binary cannot be resolved")
	}
}

func TestLightpandaAssetName(t *testing.T) {
	tests := []struct {
		goos, arch string
		want       string
	}{
		{"darwin", "arm64", "lightpanda-aarch64-macos"},
		{"darwin", "amd64", "lightpanda-x86_64-macos"},
		{"linux", "amd64", "lightpanda-x86_64-linux"},
		{"linux", "arm64", "lightpanda-aarch64-linux"},
		{"windows", "amd64", ""},
	}
	for _, tt := range tests {
		got := LightpandaAssetName(tt.goos, tt.arch)
		if got != tt.want {
			t.Errorf("LightpandaAssetName(%q, %q) = %q, want %q", tt.goos, tt.arch, got, tt.want)
		}
	}
}

func TestLightpandaAutoDownload_WhenBinaryNotFound(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on windows")
	}
	// Create a mock HTTP server that serves a fake lightpanda binary.
	fakeScript := "#!/bin/sh\necho 'lightpanda v0.0.0'\n"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Write([]byte(fakeScript))
	}))
	defer srv.Close()

	// Override the download base URL for testing.
	origBase := lightpandaDownloadBase
	lightpandaDownloadBase = srv.URL
	defer func() { lightpandaDownloadBase = origBase }()

	dir := t.TempDir()
	b := &LightpandaBinary{}
	path, err := b.EnsureBinary(context.Background(), "", dir, true)
	if err != nil {
		t.Fatalf("EnsureBinary with auto_download: %v", err)
	}
	if path == "" {
		t.Fatal("expected non-empty path")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("downloaded binary not found: %v", err)
	}
	// Verify the binary is executable.
	info, _ := os.Stat(path)
	if info.Mode()&0111 == 0 {
		t.Errorf("downloaded binary is not executable: %v", info.Mode())
	}
}

func TestLightpandaAutoDownload_SkipsWhenDisabled(t *testing.T) {
	t.Setenv("PATH", "")
	dir := t.TempDir()
	b := &LightpandaBinary{}
	_, err := b.EnsureBinary(context.Background(), "", dir, false)
	if err == nil {
		t.Fatal("expected error when auto_download is disabled and binary not found")
	}
}

func TestLightpandaAutoDownload_UsesCachedPath(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "lightpanda")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\necho v0.0.1\n"), 0755); err != nil {
		t.Fatal(err)
	}
	b := &LightpandaBinary{path: bin}
	got, err := b.EnsureBinary(context.Background(), "", dir, true)
	if err != nil {
		t.Fatalf("EnsureBinary: %v", err)
	}
	if got != bin {
		t.Fatalf("expected cached path %q, got %q", bin, got)
	}
}

func TestLightpandaAutoDownload_ExplicitPathTriedFirst(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "lightpanda")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\necho v0.0.1\n"), 0755); err != nil {
		t.Fatal(err)
	}
	b := &LightpandaBinary{}
	got, err := b.EnsureBinary(context.Background(), bin, dir, true)
	if err != nil {
		t.Fatalf("EnsureBinary: %v", err)
	}
	if got != bin {
		t.Fatalf("expected explicit path %q, got %q", bin, got)
	}
}

func TestLightpandaAutoDownload_CacheHitOnSecondCall(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on windows")
	}
	fakeScript := "#!/bin/sh\necho 'lightpanda v0.0.0'\n"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Write([]byte(fakeScript))
	}))
	defer srv.Close()

	origBase := lightpandaDownloadBase
	lightpandaDownloadBase = srv.URL
	defer func() { lightpandaDownloadBase = origBase }()

	dir := t.TempDir()

	// First call: downloads the binary.
	b1 := &LightpandaBinary{}
	path1, err := b1.EnsureBinary(context.Background(), "", dir, true)
	if err != nil {
		t.Fatalf("first EnsureBinary: %v", err)
	}

	// Second call with a fresh LightpandaBinary (simulates restart):
	// should find cached file without downloading again.
	b2 := &LightpandaBinary{}
	path2, err := b2.EnsureBinary(context.Background(), "", dir, true)
	if err != nil {
		t.Fatalf("second EnsureBinary: %v", err)
	}
	if path1 != path2 {
		t.Fatalf("expected same path on cache hit: first=%q, second=%q", path1, path2)
	}
}

func TestLightpandaBinaryVersion(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on windows")
	}
	dir := t.TempDir()
	mock := filepath.Join(dir, "lightpanda")
	if err := os.WriteFile(mock, []byte("#!/bin/sh\necho 'lightpanda v0.2.5'\n"), 0755); err != nil {
		t.Fatal(err)
	}
	b := &LightpandaBinary{path: mock}
	ver, err := b.Version(context.Background())
	if err != nil {
		t.Fatalf("version: %v", err)
	}
	if !strings.Contains(ver, "v0.2.5") {
		t.Errorf("expected version to contain 'v0.2.5', got %q", ver)
	}
}
