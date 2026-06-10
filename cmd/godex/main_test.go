package main

import (
	"strings"
	"testing"
)

// TestExtractGlobalConfigArgsReturnsConfigPathOnly verifies that
// --config is stripped from the remainder and surfaced in the
// returned Options struct.
func TestExtractGlobalConfigArgsReturnsConfigPathOnly(t *testing.T) {
	options, mode, rest, err := extractGlobalConfigArgs([]string{"--config", "/tmp/cfg.yaml", "tui"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if options.ConfigPath != "/tmp/cfg.yaml" {
		t.Fatalf("expected ConfigPath /tmp/cfg.yaml, got %q", options.ConfigPath)
	}
	if mode != "" {
		t.Fatalf("expected default tui mode, got %q", mode)
	}
	if len(rest) != 1 || rest[0] != "tui" {
		t.Fatalf("expected remainder [tui], got %v", rest)
	}
}

// TestExtractGlobalConfigArgsInlineConfigForm verifies --config=...
// is also recognized.
func TestExtractGlobalConfigArgsInlineConfigForm(t *testing.T) {
	options, _, rest, err := extractGlobalConfigArgs([]string{"--config=/tmp/cfg.yaml"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if options.ConfigPath != "/tmp/cfg.yaml" {
		t.Fatalf("expected /tmp/cfg.yaml, got %q", options.ConfigPath)
	}
	if len(rest) != 0 {
		t.Fatalf("expected no remainder, got %v", rest)
	}
}

// TestExtractGlobalConfigArgsScrollbackMode verifies that
// --tui-mode=scrollback sets the streaming mode and leaves the
// remaining args intact.
func TestExtractGlobalConfigArgsScrollbackMode(t *testing.T) {
	_, mode, rest, err := extractGlobalConfigArgs([]string{"--tui-mode=scrollback", "tui"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if mode != "scrollback" {
		t.Fatalf("expected mode=scrollback, got %q", mode)
	}
	if len(rest) != 1 || rest[0] != "tui" {
		t.Fatalf("expected remainder [tui], got %v", rest)
	}
}

// TestExtractGlobalConfigArgsScrollbackModeSpaceForm verifies that
// --tui-mode scrollback (separate args) is also recognized.
func TestExtractGlobalConfigArgsScrollbackModeSpaceForm(t *testing.T) {
	_, mode, rest, err := extractGlobalConfigArgs([]string{"--tui-mode", "scrollback", "tui"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if mode != "scrollback" {
		t.Fatalf("expected mode=scrollback, got %q", mode)
	}
	if len(rest) != 1 || rest[0] != "tui" {
		t.Fatalf("expected remainder [tui], got %v", rest)
	}
}

// TestExtractGlobalConfigArgsFullModeIsDefault verifies that
// --tui-mode=full and missing values fall back to the legacy
// bubbletea TUI.
func TestExtractGlobalConfigArgsFullModeIsDefault(t *testing.T) {
	cases := []string{"--tui-mode=full", "--tui-mode=bubbletea", "--tui-mode="}
	for _, raw := range cases {
		_, mode, _, err := extractGlobalConfigArgs([]string{raw})
		if err != nil {
			t.Fatalf("%s: unexpected error: %v", raw, err)
		}
		if mode != "" {
			t.Fatalf("%s: expected default mode, got %q", raw, mode)
		}
	}
}

// TestExtractGlobalConfigArgsStreamingAlias verifies --tui-mode=streaming
// is treated as scrollback.
func TestExtractGlobalConfigArgsStreamingAlias(t *testing.T) {
	_, mode, _, err := extractGlobalConfigArgs([]string{"--tui-mode=streaming"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if mode != "scrollback" {
		t.Fatalf("expected scrollback, got %q", mode)
	}
}

// TestExtractGlobalConfigArgsInvalidModeRejects verifies that an
// unknown --tui-mode value is rejected with a precise error so the
// operator knows the right values to use.
func TestExtractGlobalConfigArgsInvalidModeRejects(t *testing.T) {
	_, _, _, err := extractGlobalConfigArgs([]string{"--tui-mode=bogus"})
	if err == nil {
		t.Fatal("expected error for bogus mode")
	}
	if !strings.Contains(err.Error(), "invalid --tui-mode value") {
		t.Fatalf("expected error to mention --tui-mode, got %q", err.Error())
	}
}

// TestExtractGlobalConfigArgsMissingConfigValue verifies that
// --config without a following argument produces a clear error
// rather than silently dropping the flag.
func TestExtractGlobalConfigArgsMissingConfigValue(t *testing.T) {
	_, _, _, err := extractGlobalConfigArgs([]string{"--config"})
	if err == nil {
		t.Fatal("expected error for missing --config value")
	}
}
