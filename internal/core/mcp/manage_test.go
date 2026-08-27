package mcp

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newTestManager(t *testing.T) (*Manager, string) {
	t.Helper()
	workspace := t.TempDir()
	configPath := filepath.Join(workspace, ".godex", "mcp.json")
	if err := os.MkdirAll(filepath.Dir(configPath), 0755); err != nil {
		t.Fatal(err)
	}
	tempDir := filepath.Join(workspace, ".godex", ".tmp")
	_ = os.MkdirAll(tempDir, 0755)
	return NewManager(configPath, workspace, tempDir), configPath
}

func TestUpsertServerAddsAndPersists(t *testing.T) {
	mgr, configPath := newTestManager(t)
	srv := ServerConfig{Name: "fs", Type: ServerTypeFilesystem, Root: "docs"}
	if err := mgr.UpsertServer(srv); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if _, err := os.Stat(configPath); err != nil {
		t.Fatalf("config not persisted: %v", err)
	}
	got, err := mgr.GetServer("fs")
	if err != nil {
		t.Fatalf("get server: %v", err)
	}
	if got.Name != "fs" || got.Type != ServerTypeFilesystem || got.Root != "docs" {
		t.Fatalf("unexpected server %+v", got)
	}
	// Reload from disk to confirm persistence.
	reloaded := NewManager(configPath, "", "")
	if _, err := reloaded.GetServer("fs"); err != nil {
		t.Fatalf("reloaded server missing: %v", err)
	}
}

func TestUpsertServerReplacesByIdentity(t *testing.T) {
	mgr, _ := newTestManager(t)
	if err := mgr.UpsertServer(ServerConfig{Name: "s", Type: ServerTypeFilesystem, Root: "a"}); err != nil {
		t.Fatal(err)
	}
	if err := mgr.UpsertServer(ServerConfig{Name: "s", Type: ServerTypeFilesystem, Root: "b"}); err != nil {
		t.Fatal(err)
	}
	got, _ := mgr.GetServer("s")
	if got.Root != "b" {
		t.Fatalf("expected root b, got %+v", got)
	}
	list, _ := mgr.ListServers()
	if len(list) != 1 {
		t.Fatalf("expected 1 server after replace, got %d", len(list))
	}
}

func TestUpsertServerValidates(t *testing.T) {
	mgr, _ := newTestManager(t)
	cases := []ServerConfig{
		{Name: "", Type: ServerTypeFilesystem, Root: "docs"},
		{Name: "no-root", Type: ServerTypeFilesystem},
		{Name: "no-cmd", Type: ServerTypeStdio},
		{Name: "no-url", Type: ServerTypeHTTP},
	}
	for _, c := range cases {
		if err := mgr.UpsertServer(c); err == nil {
			t.Fatalf("expected validation error for %+v", c)
		}
	}
}

func TestDeleteServerIsIdempotent(t *testing.T) {
	mgr, _ := newTestManager(t)
	_ = mgr.UpsertServer(ServerConfig{Name: "fs", Type: ServerTypeFilesystem, Root: "docs"})
	if err := mgr.DeleteServer("fs"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := mgr.GetServer("fs"); err == nil {
		t.Fatalf("expected server removed")
	}
	// Deleting a missing server is not an error.
	if err := mgr.DeleteServer("missing"); err != nil {
		t.Fatalf("idempotent delete: %v", err)
	}
}

func TestValidateServerTransportSpecific(t *testing.T) {
	if err := validateServer(ServerConfig{Name: "x", Type: ServerTypeFilesystem, Root: "r"}); err != nil {
		t.Fatalf("valid filesystem: %v", err)
	}
	if err := validateServer(ServerConfig{Name: "x", Type: ServerTypeStdio, Command: "echo"}); err != nil {
		t.Fatalf("valid stdio: %v", err)
	}
	if err := validateServer(ServerConfig{Name: "x", Type: ServerTypeHTTP, URL: "https://e"}); err != nil {
		t.Fatalf("valid http: %v", err)
	}
	if err := validateServer(ServerConfig{Name: "x", Type: "bogus"}); err == nil {
		t.Fatalf("expected unsupported type error")
	}
}

func TestTestConnectionFilesystem(t *testing.T) {
	mgr, _ := newTestManager(t)
	root := filepath.Join(mgr.workspaceDir, "docs")
	if err := os.MkdirAll(root, 0755); err != nil {
		t.Fatal(err)
	}
	_ = mgr.UpsertServer(ServerConfig{Name: "fs", Type: ServerTypeFilesystem, Root: root})
	st, err := mgr.TestConnection(context.Background(), "fs")
	if err != nil {
		t.Fatalf("test connection: %v", err)
	}
	if !st.Online || st.Name != "fs" || st.Type != ServerTypeFilesystem {
		t.Fatalf("unexpected status %+v", st)
	}
}

func TestStatusesReportsOfflineForBadServer(t *testing.T) {
	mgr, _ := newTestManager(t)
	_ = mgr.UpsertServer(ServerConfig{Name: "bad", Type: ServerTypeFilesystem, Root: "nope"})
	_ = mgr.UpsertServer(ServerConfig{Name: "missing-stdio", Type: ServerTypeStdio, Command: "/does/not/exist"})
	sts, err := mgr.Statuses(context.Background())
	if err != nil {
		t.Fatalf("statuses: %v", err)
	}
	if len(sts) != 2 {
		t.Fatalf("expected 2 statuses, got %d", len(sts))
	}
	byName := map[string]ServerStatus{}
	for _, s := range sts {
		byName[s.Name] = s
	}
	if byName["bad"].Online {
		t.Fatalf("expected offline for bad server")
	}
	if strings.TrimSpace(byName["bad"].Error) == "" {
		t.Fatalf("expected error for bad server")
	}
}
