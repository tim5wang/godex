package codexnative

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
)

// Desktop launchers and service managers need not inherit an interactive
// shell's PATH. Only try fixed installation paths, never execute a login shell
// or silently replace an explicitly configured executable.
func (s *Service) executable() (string, error) {
	home, _ := os.UserHomeDir()
	return resolveCommand(s.opts.Command, commandCandidates(runtime.GOOS, home))
}

func commandCandidates(platform, home string) []string {
	if platform != "darwin" {
		return nil
	}
	var paths []string
	for _, app := range []string{
		"ChatGPT.app/Contents/Resources/codex-cli/CodexCLI.app/Contents/MacOS/codex",
		"Codex.app/Contents/Resources/codex",
	} {
		if home != "" {
			paths = append(paths, filepath.Join(home, "Applications", app))
		}
		paths = append(paths, filepath.Join("/Applications", app))
	}
	return append(paths, "/opt/homebrew/bin/codex", "/usr/local/bin/codex")
}

func resolveCommand(configured string, fallbacks []string) (string, error) {
	if configured != "" {
		path, err := exec.LookPath(configured)
		if err != nil {
			return "", fmt.Errorf("%w: configured GODEX_CODEX_BIN %q is not executable: %v", ErrUnavailable, configured, err)
		}
		return path, nil
	}
	for _, candidate := range append([]string{"codex"}, fallbacks...) {
		if path, err := exec.LookPath(candidate); err == nil {
			return path, nil
		}
	}
	return "", fmt.Errorf("%w: Codex CLI was not found in PATH or supported desktop locations; set GODEX_CODEX_BIN to its full path on this node", ErrUnavailable)
}
