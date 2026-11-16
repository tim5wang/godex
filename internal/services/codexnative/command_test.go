package codexnative

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveCommandPrefersPATHAndHonorsExplicitOverride(t *testing.T) {
	dir := t.TempDir()
	fromPath := filepath.Join(dir, "codex")
	bundled := filepath.Join(t.TempDir(), "bundled codex")
	for _, path := range []string{fromPath, bundled} {
		if err := os.WriteFile(path, []byte("not executed by discovery"), 0700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir)
	if got, err := resolveCommand("", []string{bundled}); err != nil || got != fromPath {
		t.Fatalf("PATH precedence: %q %v", got, err)
	}
	if got, err := resolveCommand(bundled, []string{fromPath}); err != nil || got != bundled {
		t.Fatalf("explicit override: %q %v", got, err)
	}
	if _, err := resolveCommand(filepath.Join(dir, "missing"), []string{bundled}); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("invalid explicit override silently fell back: %v", err)
	}
}

func TestResolveCommandFindsBundledExecutableWithoutPATH(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PATH", dir)
	notExecutable := filepath.Join(dir, "not-executable")
	bundled := filepath.Join(dir, "desktop codex")
	if err := os.WriteFile(notExecutable, []byte("no execution permission"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bundled, []byte("not executed by discovery"), 0700); err != nil {
		t.Fatal(err)
	}
	if got, err := resolveCommand("", []string{notExecutable, bundled}); err != nil || got != bundled {
		t.Fatalf("desktop fallback: %q %v", got, err)
	}
	if _, err := resolveCommand("", []string{notExecutable}); !errors.Is(err, ErrUnavailable) || !strings.Contains(err.Error(), "GODEX_CODEX_BIN") {
		t.Fatalf("missing CLI must give actionable diagnostic: %v", err)
	}
}

func TestCommandCandidatesUsePortableDesktopLocations(t *testing.T) {
	home := t.TempDir()
	want := filepath.Join(home, "Applications", "ChatGPT.app", "Contents", "Resources", "codex-cli", "CodexCLI.app", "Contents", "MacOS", "codex")
	paths := commandCandidates("darwin", home)
	if len(paths) == 0 || paths[0] != want {
		t.Fatalf("missing user desktop location: %v", paths)
	}
	if len(commandCandidates("linux", home)) != 0 {
		t.Fatal("macOS paths leaked to other platforms")
	}
}

func TestStatusReportsExecutableWithoutStartingDaemon(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "desktop codex")
	if err := os.WriteFile(path, []byte("not executed; invalid binary"), 0700); err != nil {
		t.Fatal(err)
	}
	s := New(Options{Command: path, Socket: filepath.Join(dir, "missing.sock")})
	defer s.Close()
	status := s.Status(testContext(t))
	if status.Ready || status.Executable != path || !strings.Contains(status.Reason, "missing.sock") {
		t.Fatalf("unexpected readiness: %+v", status)
	}
}
