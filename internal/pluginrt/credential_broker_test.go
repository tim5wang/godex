package pluginrt

import (
	"strings"
	"testing"
)

func TestCredentialBrokerAllowlist(t *testing.T) {
	env := map[string]string{
		"OPENAI_API_KEY": "sk-secret",
		"OTHER_KEY":      "other",
	}
	lookup := func(name string) (string, bool) {
		value, ok := env[name]
		return value, ok
	}
	broker := NewCredentialBroker(lookup, map[string][]string{
		"plugin-a": {"OPENAI_API_KEY", "MISSING_KEY"},
	})

	value, err := broker.Get("plugin-a", "OPENAI_API_KEY")
	if err != nil {
		t.Fatalf("get allowed secret: %v", err)
	}
	if value != "sk-secret" {
		t.Fatalf("unexpected value: %q", value)
	}
	// Not allowed.
	if _, err := broker.Get("plugin-a", "OTHER_KEY"); err == nil {
		t.Fatal("expected error for unauthorized secret")
	} else if !strings.Contains(err.Error(), "not allowed") {
		t.Fatalf("unexpected error: %v", err)
	}
	// Plugin without permissions.
	if _, err := broker.Get("plugin-unknown", "OPENAI_API_KEY"); err == nil {
		t.Fatal("expected error for plugin without permissions")
	}
	// Authorized but unset.
	if _, err := broker.Get("plugin-a", "MISSING_KEY"); err == nil {
		t.Fatal("expected error for unset secret")
	} else if !strings.Contains(err.Error(), "not set") {
		t.Fatalf("unexpected error: %v", err)
	}
	// Empty name.
	if _, err := broker.Get("plugin-a", "  "); err == nil {
		t.Fatal("expected error for empty secret name")
	}
}

func TestCredentialBrokerAllowAtRuntime(t *testing.T) {
	lookup := func(name string) (string, bool) {
		if name == "TOKEN" {
			return "abc", true
		}
		return "", false
	}
	broker := NewCredentialBroker(lookup, nil)
	if _, err := broker.Get("plugin-b", "TOKEN"); err == nil {
		t.Fatal("expected denial before Allow")
	}
	broker.Allow("plugin-b", "TOKEN")
	value, err := broker.Get("plugin-b", "TOKEN")
	if err != nil {
		t.Fatalf("get after allow: %v", err)
	}
	if value != "abc" {
		t.Fatalf("unexpected value: %q", value)
	}
}

func TestCredentialBrokerAdapter(t *testing.T) {
	lookup := func(name string) (string, bool) {
		if name == "KEY" {
			return "v", true
		}
		return "", false
	}
	broker := NewCredentialBroker(lookup, map[string][]string{"plugin-x": {"KEY"}})
	get := broker.adapterFunc("plugin-x")
	value, err := get("KEY")
	if err != nil || value != "v" {
		t.Fatalf("adapter get = %q, %v", value, err)
	}
	if _, err := get("OTHER"); err == nil {
		t.Fatal("expected adapter denial for unauthorized secret")
	}
}
