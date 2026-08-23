package tools

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/tim5wang/godex/internal/core/config"
)

// TestSnapshotScriptIncludesA11yFields verifies the snapshot collector emits
// accessibility fields (role / aria-label / aria-checked) and canvas hints.
func TestSnapshotScriptIncludesA11yFields(t *testing.T) {
	script := snapshotScript(4000)
	for _, want := range []string{
		`aria-label`,
		`aria-checked`,
		`role`,
		`has_canvas`,
		`needs_screenshot`,
		`shadowRoot`,
		`contentDocument`,
		`[role="checkbox"]`,
	} {
		if !strings.Contains(script, want) {
			t.Errorf("snapshotScript missing %q", want)
		}
	}
}

// TestFindElementsScriptPiercesShadowAndIframes verifies the find script
// collects elements from open shadow roots and same-origin iframes.
func TestFindElementsScriptPiercesShadowAndIframes(t *testing.T) {
	script := findElementsScript(BrowserLocator{Text: "submit"}, 10)
	for _, want := range []string{
		"shadowRoot",
		"contentDocument",
		"iframe",
		"aria-label",
		"aria-checked",
	} {
		if !strings.Contains(script, want) {
			t.Errorf("findElementsScript missing %q", want)
		}
	}
}

// TestPierceScriptsCoverShadowAndIframes verifies the click/type fallback
// scripts locate elements across shadow roots and iframes.
func TestPierceScriptsCoverShadowAndIframes(t *testing.T) {
	for _, script := range []string{
		pierceFindScript("#btn"),
		pierceTypeScript("#input", "hi"),
	} {
		for _, want := range []string{"shadowRoot", "contentDocument", "iframe", "scrollIntoView"} {
			if !strings.Contains(script, want) {
				t.Errorf("pierce script missing %q:\n%s", want, script)
			}
		}
	}
}

// TestBrowserServicePersistentProfileDir verifies the user-data dir moves to
// the state dir when persistent_profile is on, and stays in temp otherwise.
func TestBrowserServicePersistentProfileDir(t *testing.T) {
	temp := t.TempDir()
	state := t.TempDir()

	// Persistent: user-data under state dir; work/cache stay in temp.
	persistent := NewBrowserService(config.BrowserConfig{PersistentProfile: true}, temp)
	persistent.SetStateDir(state)
	if dir, err := persistent.ensureBrowserDir("user-data"); err != nil {
		t.Fatalf("ensure user-data: %v", err)
	} else if !strings.HasPrefix(dir, filepath.Join(state, "browser")) {
		t.Fatalf("expected user-data under state dir, got %q", dir)
	}
	if dir, err := persistent.ensureBrowserDir("work"); err != nil {
		t.Fatalf("ensure work: %v", err)
	} else if !strings.HasPrefix(dir, filepath.Join(temp, "browser")) {
		t.Fatalf("expected work under temp dir, got %q", dir)
	}

	// Non-persistent: everything under temp.
	ephemeral := NewBrowserService(config.BrowserConfig{}, temp)
	ephemeral.SetStateDir(state)
	if dir, err := ephemeral.ensureBrowserDir("user-data"); err != nil {
		t.Fatalf("ensure user-data: %v", err)
	} else if !strings.HasPrefix(dir, filepath.Join(temp, "browser")) {
		t.Fatalf("expected user-data under temp dir without persistent_profile, got %q", dir)
	}
}

// TestBrowserToolTabActionsRequirePageID verifies switch_tab / close_tab
// reject missing page ids at the tool boundary.
func TestBrowserToolTabActionsRequirePageID(t *testing.T) {
	service := NewBrowserService(config.BrowserConfig{Enabled: true}, t.TempDir())
	tool := NewBrowserTool(service, t.TempDir())
	handler := NewToolHandler()
	handler.Register(tool)

	ctx := WithSessionID(t.Context(), "session-tab")
	for _, action := range []string{"switch_tab", "close_tab"} {
		_, err := handler.HandleResult(ctx, "browser", map[string]interface{}{"action": action})
		if err == nil {
			t.Fatalf("expected %s to require page_id, got nil error", action)
		}
		if !strings.Contains(err.Error(), "page_id") {
			t.Fatalf("expected error mentioning page_id for %s, got %v", action, err)
		}
	}
}
