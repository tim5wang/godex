package mcp

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestManagerListsFilesystemResources(t *testing.T) {
	workspace := t.TempDir()
	tempDir := filepath.Join(workspace, ".godex", ".tmp")
	if err := os.MkdirAll(tempDir, 0755); err != nil {
		t.Fatalf("mkdir temp dir: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(workspace, "docs"), 0755); err != nil {
		t.Fatalf("mkdir docs: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "docs", "guide.md"), []byte("# guide"), 0644); err != nil {
		t.Fatalf("write guide: %v", err)
	}

	cfg := Config{
		Servers: []ServerConfig{{
			Name: "docs",
			Type: ServerTypeFilesystem,
			Root: "docs",
		}},
	}
	data, err := MarshalConfig(cfg)
	if err != nil {
		t.Fatalf("marshal config: %v", err)
	}
	configPath := filepath.Join(workspace, ".godex", "mcp.json")
	if err := os.MkdirAll(filepath.Dir(configPath), 0755); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}
	if err := os.WriteFile(configPath, data, 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	manager := NewManager(configPath, workspace, tempDir)
	items, err := manager.ListResources()
	if err != nil {
		t.Fatalf("list resources: %v", err)
	}
	if len(items) != 1 || items[0].Server != "docs" || items[0].URI != "guide.md" {
		t.Fatalf("unexpected resources %+v", items)
	}
	if !manager.HasConfiguredServers() {
		t.Fatal("expected configured MCP servers")
	}
}

func TestManagerRejectsFilesystemResourceEscape(t *testing.T) {
	workspace := t.TempDir()
	root := filepath.Join(workspace, "docs")
	if err := os.MkdirAll(root, 0755); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(workspace, "secret.txt")
	if err := os.WriteFile(outside, []byte("secret"), 0644); err != nil {
		t.Fatal(err)
	}
	manager := &Manager{workspaceDir: workspace}
	server := ServerConfig{Name: "docs", Type: ServerTypeFilesystem, Root: "docs"}
	if _, err := manager.readFilesystemResource(server, "../secret.txt"); err == nil || !strings.Contains(err.Error(), "escapes") {
		t.Fatalf("expected traversal rejection, got %v", err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "linked.txt")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, err := manager.readFilesystemResource(server, "linked.txt"); err == nil || !strings.Contains(err.Error(), "escapes") {
		t.Fatalf("expected symlink escape rejection, got %v", err)
	}
}

func TestManagerReadsTextAndBinaryResources(t *testing.T) {
	workspace := t.TempDir()
	tempDir := filepath.Join(workspace, ".godex", ".tmp")
	if err := os.MkdirAll(tempDir, 0755); err != nil {
		t.Fatalf("mkdir temp dir: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(workspace, "assets"), 0755); err != nil {
		t.Fatalf("mkdir assets: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "assets", "note.txt"), []byte("hello"), 0644); err != nil {
		t.Fatalf("write text file: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "assets", "image.png"), []byte{0x89, 'P', 'N', 'G', 0x00}, 0644); err != nil {
		t.Fatalf("write binary file: %v", err)
	}

	cfg := Config{
		Servers: []ServerConfig{{
			Name: "assets",
			Type: ServerTypeFilesystem,
			Root: "assets",
		}},
	}
	data, err := MarshalConfig(cfg)
	if err != nil {
		t.Fatalf("marshal config: %v", err)
	}
	configPath := filepath.Join(workspace, ".godex", "mcp.json")
	if err := os.MkdirAll(filepath.Dir(configPath), 0755); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}
	if err := os.WriteFile(configPath, data, 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	manager := NewManager(configPath, workspace, tempDir)

	textResult, err := manager.ReadResource("assets", "note.txt")
	if err != nil {
		t.Fatalf("read text resource: %v", err)
	}
	if textResult.Binary || textResult.Text != "hello" {
		t.Fatalf("unexpected text result %+v", textResult)
	}

	binaryResult, err := manager.ReadResource("assets", "image.png")
	if err != nil {
		t.Fatalf("read binary resource: %v", err)
	}
	if !binaryResult.Binary || binaryResult.Path == "" {
		t.Fatalf("unexpected binary result %+v", binaryResult)
	}
	if _, err := os.Stat(binaryResult.Path); err != nil {
		t.Fatalf("expected copied binary file: %v", err)
	}
	if !strings.Contains(binaryResult.Summary, "Binary resource copied") {
		t.Fatalf("expected binary summary, got %+v", binaryResult)
	}
}
