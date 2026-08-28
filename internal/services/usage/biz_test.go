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

func TestBizKeyTemplateIDRoundTrip(t *testing.T) {
	dir := t.TempDir()
	store, err := NewSQLiteStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	svc := NewService(store)

	// Create with a template pinned.
	resp, err := svc.CreateBizKey(BizKeyCreateRequest{
		Name:       "sales",
		Pin:        "123456",
		TemplateID: "geek",
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Key.TemplateID != "geek" {
		t.Fatalf("expected template_id geek on create, got %q", resp.Key.TemplateID)
	}

	// Listed keys carry it too.
	keys, err := svc.ListBizKeys()
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 1 || keys[0].TemplateID != "geek" {
		t.Fatalf("expected listed key template_id geek, got %+v", keys)
	}

	// Update replaces / clears it.
	newTpl := "reviewer"
	updated, err := svc.UpdateBizKey(resp.Key.ID, BizKeyUpdateRequest{TemplateID: &newTpl})
	if err != nil {
		t.Fatal(err)
	}
	if updated.TemplateID != "reviewer" {
		t.Fatalf("expected template_id reviewer after update, got %q", updated.TemplateID)
	}
	clear := ""
	updated, err = svc.UpdateBizKey(resp.Key.ID, BizKeyUpdateRequest{TemplateID: &clear})
	if err != nil {
		t.Fatal(err)
	}
	if updated.TemplateID != "" {
		t.Fatalf("expected empty template_id after clear, got %q", updated.TemplateID)
	}
}

func TestTemplateFromBizKeyMapping(t *testing.T) {
	key := &BizAPIKey{
		ID:            "biz_1",
		Name:          "sales-crm",
		Description:   "sales assistant",
		DefaultPrompt: "you are the sales agent",
		MCPServers:    []string{"crm", "kb"},
		SandboxTools:  []string{"read_file", "bash"},
		Skills:        []string{"sales-skill"},
		Packages:      []string{"sales-pkg"},
		ProjectDir:    "/work/sales",
	}
	tpl := TemplateFromBizKey(key)

	if tpl.ID != "biz-sales-crm" {
		t.Fatalf("expected template id biz-sales-crm, got %q", tpl.ID)
	}
	if tpl.BasePrompt != key.DefaultPrompt {
		t.Fatalf("expected BasePrompt from DefaultPrompt, got %q", tpl.BasePrompt)
	}
	if len(tpl.Tools) != 2 || tpl.Tools[0] != "read_file" {
		t.Fatalf("expected sandbox tools mapped to template Tools, got %v", tpl.Tools)
	}
	if len(tpl.MCPServers) != 2 || tpl.MCPServers[0] != "crm" {
		t.Fatalf("expected MCP servers mapped, got %v", tpl.MCPServers)
	}
	if len(tpl.Skills) != 1 || tpl.Skills[0] != "sales-skill" {
		t.Fatalf("expected skills mapped, got %v", tpl.Skills)
	}
	if len(tpl.Packages) != 1 || tpl.Packages[0] != "sales-pkg" {
		t.Fatalf("expected packages mapped, got %v", tpl.Packages)
	}
	if tpl.ProjectDir != "/work/sales" {
		t.Fatalf("expected project_dir mapped, got %q", tpl.ProjectDir)
	}
}
