package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tim5wang/godex/internal/wasmrt"
)

func loadWasmTestPlugin(t *testing.T) *wasmrt.Plugin {
	t.Helper()
	binary, err := os.ReadFile(filepath.Join("..", "wasmrt", "testdata", "plugin.wasm"))
	if err != nil {
		t.Fatalf("read wasm test plugin: %v", err)
	}
	plugin, err := wasmrt.NewPlugin(context.Background(), wasmrt.Config{Binary: binary})
	if err != nil {
		t.Fatalf("new wasm plugin: %v", err)
	}
	t.Cleanup(func() { _ = plugin.Close(context.Background()) })
	return plugin
}

func TestWasmCallToolRoundTrip(t *testing.T) {
	plugin := loadWasmTestPlugin(t)
	tools, err := plugin.ToolsList(context.Background())
	if err != nil {
		t.Fatalf("tools list: %v", err)
	}
	if len(tools) == 0 || tools[0].Name != "wasm_echo" {
		t.Fatalf("unexpected tools: %+v", tools)
	}

	tool, err := NewWasmCallTool(NewWasmToolRunner(plugin), tools[0], WasmToolOptions{PluginID: "test"})
	if err != nil {
		t.Fatalf("new wasm call tool: %v", err)
	}
	if tool.Name() != "wasm_echo" {
		t.Fatalf("unexpected tool name: %s", tool.Name())
	}
	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"message": "hi from test",
	})
	if err != nil {
		t.Fatalf("execute wasm tool: %v", err)
	}
	if !strings.Contains(result, "wasm echo: hi from test") {
		t.Fatalf("unexpected result: %q", result)
	}
}

func TestWasmCallToolMissingPlugin(t *testing.T) {
	plugin := loadWasmTestPlugin(t)
	tools, err := plugin.ToolsList(context.Background())
	if err != nil {
		t.Fatalf("tools list: %v", err)
	}
	// Runner that reports no loaded plugin.
	runner := &emptyWasmRunner{}
	tool, err := NewWasmCallTool(runner, tools[0], WasmToolOptions{PluginID: "ghost"})
	if err != nil {
		t.Fatalf("new wasm call tool: %v", err)
	}
	if _, err := tool.Execute(context.Background(), map[string]interface{}{"message": "x"}); err == nil {
		t.Fatal("expected error for unloaded plugin")
	} else if !strings.Contains(err.Error(), "not loaded") {
		t.Fatalf("unexpected error: %v", err)
	}
}

type emptyWasmRunner struct{}

func (r *emptyWasmRunner) WasmPlugin(id string) *wasmrt.Plugin { return nil }

func TestSanitizeWasmToolName(t *testing.T) {
	tests := map[string]string{
		"wasm_echo":   "wasm_echo",
		"Echo-Tool":   "echo_tool",
		"  spaced  ":  "spaced",
		"!!!":         "",
		"tôöl":        "tl",
	}
	for in, want := range tests {
		if got := sanitizeWasmToolName(in); got != want {
			t.Errorf("sanitize(%q) = %q, want %q", in, got, want)
		}
	}
}
