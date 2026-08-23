package servicecontrol

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestParseNullEnvironmentIgnoresShellStartupNoise(t *testing.T) {
	output := []byte("startup banner\nPATH=/bad\x00" + shellEnvironmentMarker + "PATH=/usr/local/bin:/usr/bin\x00GOPATH=/home/me/go\x00MULTILINE=one\ntwo\x00")
	env := parseNullEnvironment(output)
	if env["PATH"] != "/usr/local/bin:/usr/bin" {
		t.Fatalf("unexpected PATH: %q", env["PATH"])
	}
	if env["GOPATH"] != "/home/me/go" || env["MULTILINE"] != "one\ntwo" {
		t.Fatalf("unexpected parsed environment: %#v", env)
	}
}

func TestImportUserShellEnvironmentLoadsLoginInteractiveExports(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses a POSIX shell script")
	}
	dir := t.TempDir()
	shell := filepath.Join(dir, "test-shell")
	script := "#!/bin/sh\nexport GODEX_LOGIN_SHELL_TEST=loaded\nexport PATH=/custom/go/bin:/usr/bin:/bin\nexec /bin/sh -c \"$2\"\n"
	if err := os.WriteFile(shell, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GODEX_SERVICE_SCOPE", "user")
	t.Setenv("SHELL", shell)
	t.Setenv("GODEX_LOGIN_SHELL_TEST", "")

	if err := ImportUserShellEnvironment(context.Background()); err != nil {
		t.Fatalf("import user shell environment: %v", err)
	}
	if got := os.Getenv("GODEX_LOGIN_SHELL_TEST"); got != "loaded" {
		t.Fatalf("expected login shell export, got %q", got)
	}
	if got := os.Getenv("PATH"); got != "/custom/go/bin:/usr/bin:/bin" {
		t.Fatalf("expected login shell PATH, got %q", got)
	}
}

func TestImportUserShellEnvironmentSkipsNonUserService(t *testing.T) {
	t.Setenv("GODEX_SERVICE_SCOPE", "system")
	t.Setenv("SHELL", "/does/not/exist")
	if err := ImportUserShellEnvironment(context.Background()); err != nil {
		t.Fatalf("system service should not load a user shell: %v", err)
	}
}

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

func TestWindowsServeScriptWritesShortLauncherPath(t *testing.T) {
	opts, err := NormalizeOptions(testInstallOptions(t))
	if err != nil {
		t.Fatalf("normalize options: %v", err)
	}
	scriptPath, err := writeWindowsServeScript(opts)
	if err != nil {
		t.Fatalf("write windows serve script: %v", err)
	}
	// The /TR value passed to schtasks must be <= 261 characters (the script
	// path alone, quoted) — the long inline command would blow past it.
	if tr := len(windowsQuote(scriptPath)); tr > 261 {
		t.Fatalf("/TR value is %d chars, exceeds schtasks 261-char limit: %q", tr, scriptPath)
	}
	raw, err := os.ReadFile(scriptPath)
	if err != nil {
		t.Fatalf("read script: %v", err)
	}
	text := string(raw)
	for _, want := range []string{
		"@echo off",
		"cd /d " + windowsQuote(opts.WorkingDir),
		`set "GODEX_HOME=` + opts.HomeDir + `"`,
		`set "GODEX_PROJECT_DIR=` + opts.ProjectDir + `"`,
		`set "GODEX_SERVICE_NAME=` + opts.Name + `"`,
		`set "GODEX_SERVICE_SCOPE=user"`,
		`set "GOMEMLIMIT=220MiB"`,
		`set "GOGC=50"`,
		`set "GOMAXPROCS=1"`,
		`set "GODEBUG=madvdontneed=1"`,
		windowsQuote(opts.BinaryPath) + " serve --addr " + opts.Addr,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("expected script to contain %q:\n%s", want, text)
		}
	}
	if !strings.HasSuffix(text, "\r\n") {
		t.Fatalf("expected CRLF line endings, got %q", text)
	}
}

func TestWindowsServeCommandExceeds261ButScriptPathDoesNot(t *testing.T) {
	opts, err := NormalizeOptions(testInstallOptions(t))
	if err != nil {
		t.Fatalf("normalize options: %v", err)
	}
	// Guard the premise of the fix: the old inline command is genuinely too
	// long for schtasks /TR, and the script path is not.
	if got := len(windowsServeCommand(opts)); got <= 261 {
		t.Fatalf("expected inline serve command to exceed 261 chars for the regression test, got %d", got)
	}
	if got := len(windowsQuote(windowsServeScriptPath(opts))); got > 261 {
		t.Fatalf("expected script /TR value to fit in 261 chars, got %d", got)
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
