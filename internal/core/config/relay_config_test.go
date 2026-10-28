package config

import (
	"strings"
	"testing"
)

// Design §6/§7: the center must not store user data, and a session title is user
// content. These pin the three ways the setting could silently stop being
// effective: the wrong default, a broken write path, or a template that renders
// the section back out empty (the config file is regenerated from the template, so
// a missing line would drop the operator's choice on the next save).

func TestRelayObservationDefaultsToExcludingTitles(t *testing.T) {
	// The default is the whole privacy guarantee: a fresh install must not retain
	// titles without anyone choosing to.
	if got := defaultConfigFile().Relay.Observation.IncludeTitles; got {
		t.Errorf("include_titles default = %v, want false (center stores no user data)", got)
	}
	cfg := resolveConfigFile(ConfigFile{}, "", "", "", "", "", "", "", "")
	if cfg.Relay.Observation.IncludeTitles {
		t.Errorf("resolved default include_titles = %v, want false", cfg.Relay.Observation.IncludeTitles)
	}
}

func TestResolveConfigFileMapsRelayObservationSection(t *testing.T) {
	cfg := resolveConfigFile(
		ConfigFile{Relay: RelaySection{Observation: RelayObservationSection{IncludeTitles: true}}},
		"", "", "", "", "", "", "", "",
	)
	if !cfg.Relay.Observation.IncludeTitles {
		t.Error("expected resolved include_titles true when set in YAML")
	}
}

func TestRelayObservationIncludeTitlesStoredValueRoundTrip(t *testing.T) {
	// The settings API writes through setStoredValue; without a case there the
	// field would be reported as an unknown config field and the UI could never
	// turn it on.
	file := defaultConfigFile()
	if err := applyStoredValues(&file, UpdateRequest{
		Values: map[string]any{"relay.observation.include_titles": true},
	}); err != nil {
		t.Fatalf("apply stored values: %v", err)
	}
	if !file.Relay.Observation.IncludeTitles {
		t.Error("stored value did not set include_titles")
	}
	if got := storedValues(file)["relay.observation.include_titles"]; got != true {
		t.Errorf("stored value = %#v, want true", got)
	}
	resolved := resolveConfigFile(file, "", "", "", "", "", "", "", "")
	if got := effectiveValues(resolved)["relay.observation.include_titles"]; got != true {
		t.Errorf("effective value = %#v, want true", got)
	}
}

func TestConfigTemplateRendersRelayObservationSection(t *testing.T) {
	rendered, err := renderConfigTemplate(defaultConfigFile())
	if err != nil {
		t.Fatalf("render config template: %v", err)
	}
	tpl := string(rendered)
	for _, want := range []string{"relay:", "observation:", "include_titles: false"} {
		if !strings.Contains(tpl, want) {
			t.Errorf("expected template to contain %q", want)
		}
	}
}

func TestRelayObservationIncludeTitlesRendersTrueWhenEnabled(t *testing.T) {
	// The template interpolates the field, so an enabled center must write true
	// back rather than pinning the default.
	file := defaultConfigFile()
	file.Relay.Observation.IncludeTitles = true
	rendered, err := renderConfigTemplate(file)
	if err != nil {
		t.Fatalf("render config template: %v", err)
	}
	if !strings.Contains(string(rendered), "include_titles: true") {
		t.Error("expected template to render include_titles: true when enabled")
	}
}
