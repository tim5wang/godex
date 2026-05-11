package servicecontrol

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
)

func TestNormalizeOptionsDefaultsToUserService(t *testing.T) {
	opts, err := NormalizeOptions(InstallOptions{
		Name:       "GoDex Web!",
		BinaryPath: filepath.Join(t.TempDir(), "godex"),
		WorkingDir: t.TempDir(),
		HomeDir:    filepath.Join(t.TempDir(), "home"),
	})
	if err != nil {
		t.Fatalf("normalize options: %v", err)
	}
	if opts.Name != "godexweb" {
		t.Fatalf("expected sanitized service name, got %q", opts.Name)
	}
	if opts.Scope != ScopeUser {
		t.Fatalf("expected user scope, got %q", opts.Scope)
	}
	if opts.Addr != "127.0.0.1:8088" {
		t.Fatalf("expected default addr, got %q", opts.Addr)
	}
	if !strings.Contains(opts.LogPath, "godexweb.service.log") {
		t.Fatalf("expected default service log path, got %q", opts.LogPath)
	}
}

func TestRenderSystemdUnitCarriesRuntimeEnvironment(t *testing.T) {
	opts := testInstallOptions(t)
	unit, err := RenderSystemdUnit(opts)
	if err != nil {
		t.Fatalf("render systemd unit: %v", err)
	}
	text := string(unit)
	for _, want := range []string{
		"WorkingDirectory=" + opts.WorkingDir,
		"Environment=GODEX_HOME=" + opts.HomeDir,
		"Environment=GODEX_PROJECT_DIR=" + opts.ProjectDir,
		"Environment=GODEX_SERVICE_NAME=" + opts.Name,
		"Environment=GODEX_SERVICE_SCOPE=user",
		"ExecStart=" + opts.BinaryPath + " serve --addr " + opts.Addr,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("expected systemd unit to contain %q:\n%s", want, text)
		}
	}
}

func TestRenderSystemdUnitCarriesGCAndWatchdogPolicy(t *testing.T) {
	opts := testInstallOptions(t)
	unit, err := RenderSystemdUnit(opts)
	if err != nil {
		t.Fatalf("render systemd unit: %v", err)
	}
	text := string(unit)
	for _, want := range []string{
		"Type=notify",
		"NotifyAccess=all",
		"Environment=GOMEMLIMIT=220MiB",
		"Environment=GOGC=50",
		"Environment=GOMAXPROCS=1",
		"Environment=GODEBUG=madvdontneed=1",
		"Restart=always",
		"RestartSec=3",
		"WatchdogSec=30",
		"MemoryAccounting=yes",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("expected systemd unit to contain %q:\n%s", want, text)
		}
	}
}

func TestRenderLaunchdPlistCarriesRuntimeEnvironment(t *testing.T) {
	opts := testInstallOptions(t)
	plist, err := RenderLaunchdPlist(opts)
	if err != nil {
		t.Fatalf("render launchd plist: %v", err)
	}
	for _, want := range []string{
		"<string>" + opts.BinaryPath + "</string>",
		"<string>serve</string>",
		"<string>--addr</string>",
		"<string>" + opts.Addr + "</string>",
		"<key>GODEX_HOME</key>",
		"<string>" + opts.HomeDir + "</string>",
		"<key>GODEX_SERVICE_SCOPE</key>",
		"<string>user</string>",
	} {
		if !bytes.Contains(plist, []byte(want)) {
			t.Fatalf("expected launchd plist to contain %q:\n%s", want, string(plist))
		}
	}
}

func testInstallOptions(t *testing.T) InstallOptions {
	t.Helper()
	root := t.TempDir()
	return InstallOptions{
		Name:       "godex",
		Scope:      ScopeUser,
		Addr:       "127.0.0.1:8088",
		BinaryPath: filepath.Join(root, "bin", "godex"),
		WorkingDir: filepath.Join(root, "workspace"),
		HomeDir:    filepath.Join(root, "home"),
		ProjectDir: filepath.Join(root, "workspace"),
		LogPath:    filepath.Join(root, "home", "log", "godex.service.log"),
	}
}
