package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Round-trip: settings-page save of voice_enabled / voice_engine_addr must
// survive write-to-disk, View 回读（storedValues/effectiveValues）和 reload
// （regression for storedValues/effectiveValues dropping the fields）.
func TestManagerPersistsVoiceAudioFields(t *testing.T) {
	workspace := t.TempDir()
	manager := newTestManager(t, workspace)

	view, err := manager.Update(t.Context(), UpdateRequest{
		Values: map[string]any{
			"media.audio.voice_enabled":     true,
			"media.audio.voice_engine_addr": "127.0.0.1:17021",
		},
	})
	if err != nil {
		t.Fatalf("update voice fields: %v", err)
	}
	// View 回读必须包含新字段（这是前端刷新后表单回填的来源）。
	if got, _ := view.StoredValues["media.audio.voice_enabled"].(bool); !got {
		t.Fatalf("expected stored voice_enabled true, got %#v", view.StoredValues["media.audio.voice_enabled"])
	}
	if got, _ := view.StoredValues["media.audio.voice_engine_addr"].(string); got != "127.0.0.1:17021" {
		t.Fatalf("expected stored voice_engine_addr 127.0.0.1:17021, got %#v", view.StoredValues["media.audio.voice_engine_addr"])
	}
	if got, _ := view.EffectiveValues["media.audio.voice_enabled"].(bool); !got {
		t.Fatalf("expected effective voice_enabled true, got %#v", view.EffectiveValues["media.audio.voice_enabled"])
	}

	// 运行时配置同步。
	if !manager.Current().Media.Audio.VoiceEnabled {
		t.Fatal("expected effective VoiceEnabled true")
	}
	if manager.Current().Media.Audio.VoiceEngineAddr != "127.0.0.1:17021" {
		t.Fatalf("expected VoiceEngineAddr 127.0.0.1:17021, got %q", manager.Current().Media.Audio.VoiceEngineAddr)
	}

	// 写盘 yaml 必须包含字段。
	home := testHomeForWorkspace(workspace)
	raw, err := os.ReadFile(filepath.Join(home, "godex.yaml"))
	if err != nil {
		t.Fatalf("read written config: %v", err)
	}
	if !strings.Contains(string(raw), "voice_enabled: true") {
		t.Fatalf("written yaml missing voice_enabled, got:\n%s", raw)
	}
	if !strings.Contains(string(raw), "voice_engine_addr: 127.0.0.1:17021") {
		t.Fatalf("written yaml missing voice_engine_addr, got:\n%s", raw)
	}

	// Reload from disk must preserve the values.
	reloaded, err := NewManager(Options{HomeDir: home, WorkspaceDir: workspace})
	if err != nil {
		t.Fatalf("reload manager: %v", err)
	}
	if !reloaded.Current().Media.Audio.VoiceEnabled {
		t.Fatal("expected reloaded VoiceEnabled true")
	}
	if got := reloaded.Current().Media.Audio.VoiceEngineAddr; got != "127.0.0.1:17021" {
		t.Fatalf("expected reloaded VoiceEngineAddr 127.0.0.1:17021, got %q", got)
	}
}
