package browserutil

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/go-rod/rod/lib/launcher"
)

// ResolvePath returns the browser executable path to use for a local launch.
// It respects an explicit configured path first and otherwise falls back to
// common operating-system-specific browser locations.
func ResolvePath(configured string) string {
	return resolvePath(runtime.GOOS, configured, exec.LookPath, fileExists, launcher.LookPath)
}

// HasLocalBrowser reports whether a common local browser installation is
// available on the current machine without requiring a download.
func HasLocalBrowser() bool {
	return resolveLocalBrowserPath(runtime.GOOS, exec.LookPath, fileExists, launcher.LookPath) != ""
}

func resolvePath(
	goos string,
	configured string,
	lookPath func(string) (string, error),
	exists func(string) bool,
	fallback func() (string, bool),
) string {
	configured = strings.TrimSpace(configured)
	if configured != "" {
		if resolved := resolveCandidate(configured, lookPath, exists); resolved != "" {
			return resolved
		}
		return configured
	}
	return resolveLocalBrowserPath(goos, lookPath, exists, fallback)
}

func resolveLocalBrowserPath(
	goos string,
	lookPath func(string) (string, error),
	exists func(string) bool,
	fallback func() (string, bool),
) string {
	for _, candidate := range browserCandidates(goos) {
		if resolved := resolveCandidate(candidate, lookPath, exists); resolved != "" {
			return resolved
		}
	}
	if fallback != nil {
		if path, ok := fallback(); ok {
			return path
		}
	}
	return ""
}

func resolveCandidate(candidate string, lookPath func(string) (string, error), exists func(string) bool) string {
	candidate = strings.TrimSpace(candidate)
	if candidate == "" {
		return ""
	}
	if filepath.IsAbs(candidate) {
		if exists(candidate) {
			return candidate
		}
		return ""
	}
	if lookPath == nil {
		return ""
	}
	path, err := lookPath(candidate)
	if err != nil {
		return ""
	}
	return path
}

func browserCandidates(goos string) []string {
	switch goos {
	case "darwin":
		return []string{
			"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
			"/Applications/Google Chrome Canary.app/Contents/MacOS/Google Chrome Canary",
			"/Applications/Chromium.app/Contents/MacOS/Chromium",
			"/Applications/Brave Browser.app/Contents/MacOS/Brave Browser",
			"/Applications/Microsoft Edge.app/Contents/MacOS/Microsoft Edge",
			"google-chrome",
			"google-chrome-stable",
			"chromium",
			"chromium-browser",
			"chrome",
			"brave",
			"brave-browser",
			"msedge",
			"microsoft-edge",
		}
	case "windows":
		return append(commonBrowserCommands(), windowsBrowserPaths()...)
	case "linux":
		return append(commonBrowserCommands(), []string{
			"/snap/bin/chromium",
			"/usr/bin/google-chrome",
			"/usr/bin/google-chrome-stable",
			"/usr/bin/chromium",
			"/usr/bin/chromium-browser",
			"/usr/bin/microsoft-edge",
		}...)
	default:
		return commonBrowserCommands()
	}
}

func commonBrowserCommands() []string {
	return []string{
		"google-chrome",
		"google-chrome-stable",
		"chromium",
		"chromium-browser",
		"chrome",
		"brave",
		"brave-browser",
		"msedge",
		"microsoft-edge",
	}
}

func windowsBrowserPaths() []string {
	baseDirs := []string{
		os.Getenv("ProgramFiles"),
		os.Getenv("ProgramFiles(x86)"),
		os.Getenv("LOCALAPPDATA"),
	}
	relativePaths := []string{
		filepath.Join("Google", "Chrome", "Application", "chrome.exe"),
		filepath.Join("Google", "Chrome Beta", "Application", "chrome.exe"),
		filepath.Join("Chromium", "Application", "chrome.exe"),
		filepath.Join("BraveSoftware", "Brave-Browser", "Application", "brave.exe"),
		filepath.Join("Microsoft", "Edge", "Application", "msedge.exe"),
	}
	candidates := make([]string, 0, len(baseDirs)*len(relativePaths))
	for _, baseDir := range baseDirs {
		baseDir = strings.TrimSpace(baseDir)
		if baseDir == "" {
			continue
		}
		for _, relativePath := range relativePaths {
			candidates = append(candidates, filepath.Join(baseDir, relativePath))
		}
	}
	return candidates
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}
