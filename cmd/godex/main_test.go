package main

import (
	"strings"
	"testing"
)

// TestExtractGlobalConfigArgsReturnsConfigPathOnly verifies that
// --config is stripped from the remainder and surfaced in the
// returned Options struct.
func TestExtractGlobalConfigArgsReturnsConfigPathOnly(t *testing.T) {
	options, rest, err := extractGlobalConfigArgs([]string{"--config", "/tmp/cfg.yaml", "tui"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if options.ConfigPath != "/tmp/cfg.yaml" {
		t.Fatalf("expected ConfigPath /tmp/cfg.yaml, got %q", options.ConfigPath)
	}
	if len(rest) != 1 || rest[0] != "tui" {
		t.Fatalf("expected remainder [tui], got %v", rest)
	}
}

// TestExtractGlobalConfigArgsInlineConfigForm verifies --config=...
// is also recognized.
func TestExtractGlobalConfigArgsInlineConfigForm(t *testing.T) {
	options, rest, err := extractGlobalConfigArgs([]string{"--config=/tmp/cfg.yaml"})
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

// TestExtractGlobalConfigArgsMissingConfigValue verifies that
// --config without a following argument produces a clear error
// rather than silently dropping the flag.
func TestExtractGlobalConfigArgsMissingConfigValue(t *testing.T) {
	_, _, err := extractGlobalConfigArgs([]string{"--config"})
	if err == nil {
		t.Fatal("expected error for missing --config value")
	}
}

// TestExtractGlobalConfigArgsMissingConfigValueInline verifies that
// --config= without a value produces a clear error.
func TestExtractGlobalConfigArgsMissingConfigValueInline(t *testing.T) {
	_, _, err := extractGlobalConfigArgs([]string{"--config="})
	if err == nil {
		t.Fatal("expected error for missing --config value")
	}
	if !strings.Contains(err.Error(), "missing value for --config") {
		t.Fatalf("expected error to mention --config, got %q", err.Error())
	}
}

// TestExtractGlobalConfigArgsStopsAtSubcommand verifies that global flags
// are only recognized before the subcommand: a per-command --session flag
// after the subcommand must be left in the remainder for the subcommand's
// own flag set to parse (it must not be shadowed by the global extractor).
func TestExtractGlobalConfigArgsStopsAtSubcommand(t *testing.T) {
	options, rest, err := extractGlobalConfigArgs([]string{"ask", "--session", "cli:foo", "hello"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if options.SessionSpec != "" {
		t.Fatalf("expected no global SessionSpec, got %q", options.SessionSpec)
	}
	want := []string{"ask", "--session", "cli:foo", "hello"}
	if len(rest) != len(want) {
		t.Fatalf("expected remainder %v, got %v", want, rest)
	}
	for i := range want {
		if rest[i] != want[i] {
			t.Fatalf("expected remainder %v, got %v", want, rest)
		}
	}
}

// TestExtractGlobalConfigArgsSessionBeforeSubcommand verifies the global
// --session form still works when placed before the subcommand (bare TUI).
func TestExtractGlobalConfigArgsSessionBeforeSubcommand(t *testing.T) {
	options, rest, err := extractGlobalConfigArgs([]string{"--session", "local:my-tui"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if options.SessionSpec != "local:my-tui" {
		t.Fatalf("expected SessionSpec local:my-tui, got %q", options.SessionSpec)
	}
	if len(rest) != 0 {
		t.Fatalf("expected no remainder, got %v", rest)
	}
}

// TestExtractGlobalConfigArgsConfigThenSubcommand verifies mixed placement:
// --config before the subcommand is extracted, everything after is kept.
func TestExtractGlobalConfigArgsConfigThenSubcommand(t *testing.T) {
	options, rest, err := extractGlobalConfigArgs([]string{"--config", "/tmp/cfg.yaml", "serve", "--addr", "127.0.0.1:9090"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if options.ConfigPath != "/tmp/cfg.yaml" {
		t.Fatalf("expected ConfigPath /tmp/cfg.yaml, got %q", options.ConfigPath)
	}
	want := []string{"serve", "--addr", "127.0.0.1:9090"}
	if len(rest) != len(want) {
		t.Fatalf("expected remainder %v, got %v", want, rest)
	}
	for i := range want {
		if rest[i] != want[i] {
			t.Fatalf("expected remainder %v, got %v", want, rest)
		}
	}
}
