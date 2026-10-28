package config

import (
	"context"
	"testing"
)

// The per-node migration retires the center-wide nk_ by clearing this setting
// (migration plan §4/§6 P4a). That step must actually remove the credential:
// a clear that silently does nothing would leave the retired master credential
// live while the operator believes the migration is finished.
//
// Mechanically it is easy to get wrong: applyStoredValues skips any Secret whose
// value is empty (so the generic loop never reaches setStoredValue), and only the
// explicit per-path switch in that function touches the struct. A Secret added to
// the schema but not to that switch therefore accepts a "clear" request and keeps
// its value.
func TestClearSecretsRetiresCenterWideNodeProxyToken(t *testing.T) {
	home, project := t.TempDir(), t.TempDir()
	manager, err := NewManager(Options{HomeDir: home, WorkspaceDir: project})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	ctx := context.Background()
	const legacy = "nk_legacy_center_wide_value"

	if _, err := manager.Update(ctx, UpdateRequest{Values: map[string]any{
		"control.node_proxy_token": legacy,
	}}); err != nil {
		t.Fatalf("seed node_proxy_token: %v", err)
	}
	if got := manager.Current().Control.NodeProxyToken; got != legacy {
		t.Fatalf("seeded value = %q, want %q", got, legacy)
	}

	if _, err := manager.Update(ctx, UpdateRequest{ClearSecrets: []string{"control.node_proxy_token"}}); err != nil {
		t.Fatalf("clear node_proxy_token: %v", err)
	}
	if got := manager.Current().Control.NodeProxyToken; got != "" {
		t.Errorf("after clear, node_proxy_token = %q, want empty", got)
	}

	// Retirement must survive a restart: the operator clears it once, and the node
	// must not be able to keep authenticating after the process is restarted.
	reloaded, err := NewManager(Options{HomeDir: home, WorkspaceDir: project})
	if err != nil {
		t.Fatalf("reload NewManager: %v", err)
	}
	if got := reloaded.Current().Control.NodeProxyToken; got != "" {
		t.Errorf("after reload, node_proxy_token = %q, want empty (retirement did not persist)", got)
	}
}

// The same mechanism must keep working for secrets that were already clearable,
// so the fix for the case above cannot be a rewrite that drops them.
func TestClearSecretsStillRetiresWebToken(t *testing.T) {
	head, project := t.TempDir(), t.TempDir()
	manager, err := NewManager(Options{HomeDir: head, WorkspaceDir: project})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	ctx := context.Background()

	if _, err := manager.Update(ctx, UpdateRequest{Values: map[string]any{"web.token": "web-secret"}}); err != nil {
		t.Fatalf("seed web token: %v", err)
	}
	if _, err := manager.Update(ctx, UpdateRequest{ClearSecrets: []string{"web.token"}}); err != nil {
		t.Fatalf("clear web token: %v", err)
	}
	if got := manager.Current().WebToken; got != "" {
		t.Errorf("after clear, web.token = %q, want empty", got)
	}
}
