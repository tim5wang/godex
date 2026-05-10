package browserutil

import (
	"errors"
	"testing"
)

func TestResolvePathPrefersConfiguredPath(t *testing.T) {
	t.Parallel()

	lookups := 0
	got := resolvePath(
		"linux",
		"google-chrome",
		func(name string) (string, error) {
			lookups++
			if name == "google-chrome" {
				return "/usr/bin/google-chrome", nil
			}
			return "", errors.New("not found")
		},
		func(string) bool { return false },
		func() (string, bool) { return "", false },
	)

	if got != "/usr/bin/google-chrome" {
		t.Fatalf("expected configured browser path to resolve from PATH, got %q", got)
	}
	if lookups != 1 {
		t.Fatalf("expected 1 lookup, got %d", lookups)
	}
}

func TestResolvePathPreservesInvalidConfiguredAbsolutePath(t *testing.T) {
	t.Parallel()

	const missing = "/missing/chrome"
	got := resolvePath(
		"darwin",
		missing,
		func(string) (string, error) { return "", errors.New("not found") },
		func(string) bool { return false },
		func() (string, bool) { return "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome", true },
	)

	if got != missing {
		t.Fatalf("expected invalid configured path to be preserved, got %q", got)
	}
}

func TestResolvePathUsesMacAppBundleFallback(t *testing.T) {
	t.Parallel()

	const chromeApp = "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome"
	got := resolvePath(
		"darwin",
		"",
		func(string) (string, error) { return "", errors.New("not found") },
		func(path string) bool { return path == chromeApp },
		func() (string, bool) { return "", false },
	)

	if got != chromeApp {
		t.Fatalf("expected macOS app bundle path, got %q", got)
	}
}

func TestResolvePathFallsBackToLauncherLookup(t *testing.T) {
	t.Parallel()

	got := resolvePath(
		"linux",
		"",
		func(string) (string, error) { return "", errors.New("not found") },
		func(string) bool { return false },
		func() (string, bool) { return "/cache/rod/chromium/chrome", true },
	)

	if got != "/cache/rod/chromium/chrome" {
		t.Fatalf("expected launcher fallback path, got %q", got)
	}
}
