package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tim5wang/godex/internal/wasmrt"
)

func loadTodoPlugin(t *testing.T, host wasmrt.HostCallbacks) *wasmrt.Plugin {
	t.Helper()
	binary, err := os.ReadFile("plugin.wasm")
	if err != nil {
		t.Skip("plugin.wasm not built; run: GOOS=wasip1 GOARCH=wasm go build -buildmode=c-shared -o plugin.wasm .")
	}
	plugin, err := wasmrt.NewPlugin(context.Background(), wasmrt.Config{
		Binary:   binary,
		PluginID: "todo-tracker",
		Host:     host,
	})
	if err != nil {
		t.Fatalf("load plugin: %v", err)
	}
	t.Cleanup(func() { _ = plugin.Close(context.Background()) })
	return plugin
}

// TestTodoPluginEndToEnd loads the compiled module through internal/wasmrt
// with stub host callbacks and verifies all four plugin faces: tools list,
// prompt sections, workspace read, and the KV-backed new/resolved diff.
func TestTodoPluginEndToEnd(t *testing.T) {
	kv := map[string]string{}
	dir := t.TempDir()
	host := wasmrt.HostCallbacks{
		KVGet: func(key string) string { return kv[key] },
		KVSet: func(key, value string) { kv[key] = value },
		WorkspaceRead: func(relPath string) (string, error) {
			if filepath.IsAbs(relPath) || strings.HasPrefix(relPath, "..") {
				return "", fmt.Errorf("path escapes workspace: %s", relPath)
			}
			data, err := os.ReadFile(filepath.Join(dir, relPath))
			if err != nil {
				return "", err
			}
			return string(data), nil
		},
	}
	plugin := loadTodoPlugin(t, host)

	tools, err := plugin.ToolsList(context.Background())
	if err != nil || len(tools) != 1 || tools[0].Name != "todo_scan" {
		t.Fatalf("tools list: %+v err=%v", tools, err)
	}

	sections, err := plugin.PromptSections(context.Background())
	if err != nil || len(sections) != 1 || sections[0].Key != "todo_tracker" || sections[0].Kind != "background" {
		t.Fatalf("prompt sections: %+v err=%v", sections, err)
	}

	// First scan: two TODOs.
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte(
		"package main\n\n// TODO: fix this\nfunc f() {}\n\n// FIXME: broken\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := plugin.CallTool(context.Background(), "todo_scan", map[string]any{"path": "a.go"})
	if err != nil {
		t.Fatalf("first todo_scan: %v", err)
	}
	m, ok := res.(map[string]any)
	if !ok || m["file"] != "a.go" || m["total"].(float64) != 2 {
		t.Fatalf("first scan result: %#v", res)
	}
	if todos := m["todos"].([]any); todos[0].(map[string]any)["line"].(float64) != 3 {
		t.Fatalf("line numbers wrong: %#v", todos)
	}

	// Second scan: one new TODO, one resolved, still two total.
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte(
		"package main\n\n// TODO: fix this\n// TODO: add tests\nfunc f() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err = plugin.CallTool(context.Background(), "todo_scan", map[string]any{"path": "a.go"})
	if err != nil {
		t.Fatalf("second todo_scan: %v", err)
	}
	m = res.(map[string]any)
	if len(m["new"].([]any)) != 1 || len(m["resolved"].([]any)) != 1 || m["total"].(float64) != 2 {
		t.Fatalf("diff result: %#v", res)
	}
	if _, ok := kv["todos:a.go"]; !ok {
		t.Fatal("scan not persisted to plugin KV")
	}

	// Missing file surfaces a tool error.
	if _, err := plugin.CallTool(context.Background(), "todo_scan", map[string]any{"path": "missing.go"}); err == nil {
		t.Fatal("expected error for unreadable file")
	}
}
