package usage

import (
	"strings"
	"testing"
)

func TestCreateBizKeyReturnsSecret(t *testing.T) {
	dir := t.TempDir()
	store, err := NewSQLiteStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	svc := NewService(store)

	resp, err := svc.CreateBizKey(BizKeyCreateRequest{
		Name:         "sales",
		Pin:          "123456",
		MCPServers:   []string{"crm", "order"},
		Providers:    []ProviderRef{{Name: "kb", URL: "https://kb.internal/retrieve"}},
		SandboxTools: []string{"read_file"},
		Models:       []string{"fast"},
	})
	if err != nil {
		t.Fatal(err)
	}

	if resp.Secret == "" {
		t.Fatal("expected non-empty secret")
	}
	if !strings.HasPrefix(resp.Secret, "biz_") {
		t.Fatalf("expected secret to start with 'biz_', got %q", resp.Secret[:8])
	}
	if resp.Key.KeyHash == "" {
		t.Fatal("expected stored key to have hash")
	}
	if strings.Contains(resp.Key.KeyPrefix, resp.Secret[8:]) {
		t.Fatal("key prefix should not contain full secret tail")
	}
	if len(resp.Key.MCPServers) != 2 {
		t.Fatalf("expected 2 mcp servers, got %d", len(resp.Key.MCPServers))
	}
	if resp.Key.Providers[0].URL != "https://kb.internal/retrieve" {
		t.Fatalf("provider url round-trip failed: %q", resp.Key.Providers[0].URL)
	}

	// Listed keys must not expose secret/hash.
	keys, err := svc.ListBizKeys()
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 1 {
		t.Fatalf("expected 1 biz key, got %d", len(keys))
	}
	if keys[0].KeyHash != "" {
		t.Fatal("listed biz key should not expose hash")
	}
}

func TestAuthenticateBizKey(t *testing.T) {
	dir := t.TempDir()
	store, err := NewSQLiteStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	svc := NewService(store)

	resp, err := svc.CreateBizKey(BizKeyCreateRequest{Name: "crm", Pin: "123456"})
	if err != nil {
		t.Fatal(err)
	}

	key, err := svc.AuthenticateBizKey(resp.Secret)
	if err != nil {
		t.Fatal(err)
	}
	if key.ID != resp.Key.ID {
		t.Fatalf("expected key ID %s, got %s", resp.Key.ID, key.ID)
	}

	// Wrong prefix must be rejected.
	if _, err := svc.AuthenticateBizKey("gdx_whatever"); err == nil {
		t.Fatal("expected error for non-biz secret")
	}
}

func TestBizKeyUpdateAndDelete(t *testing.T) {
	dir := t.TempDir()
	store, err := NewSQLiteStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	svc := NewService(store)

	resp, err := svc.CreateBizKey(BizKeyCreateRequest{Name: "ops", Pin: "123456", MCPServers: []string{"old"}})
	if err != nil {
		t.Fatal(err)
	}

	// Update: extend MCP servers and disable.
	newServers := []string{"old", "new"}
	disabled := false
	updated, err := svc.UpdateBizKey(resp.Key.ID, BizKeyUpdateRequest{
		MCPServers: &newServers,
		Enabled:    &disabled,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(updated.MCPServers) != 2 {
		t.Fatalf("expected 2 mcp servers after update, got %d", len(updated.MCPServers))
	}
	if updated.Enabled {
		t.Fatal("expected key to be disabled")
	}

	// Disabled key must not authenticate.
	if _, err := svc.AuthenticateBizKey(resp.Secret); err == nil {
		t.Fatal("expected error for disabled biz key")
	}

	// Delete.
	if err := svc.DeleteBizKey(resp.Key.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.GetBizKey(resp.Key.ID); err == nil {
		t.Fatal("expected not-found error after delete")
	}
}
