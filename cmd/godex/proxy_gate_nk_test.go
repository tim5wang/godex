package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/tim5wang/godex/internal/core/config"
	"github.com/tim5wang/godex/internal/runtime/httpapi"
	"github.com/tim5wang/godex/internal/services/noderegistry"
	"github.com/tim5wang/godex/internal/services/relay"
)

// The proxy / forward gate lives HERE (cmd/godex), not in httpapi: the read
// surface is protected by httpapi's nodeProxyRead, while /control/nodes/{id}/proxy
// and /forward go through nodeProxyAuthorize. Wiring only one of the two is how the
// per-node migration shipped half-connected — a rotated node got 200 on
// /control/nodes (so it looked healthy) and 401 on its own bridge proxy path (so
// it was in fact cut off). Observed on the live center:
//
//	center web token -> /proxy/meta 200
//	center-wide nk_  -> /proxy/meta 200
//	per-node nk_     -> /proxy/meta 401   <- the rotated node is refused
//
// These tests pin both halves, plus the agreement between them.

const (
	gateWebToken  = "web-secret"
	gateLegacyNK  = "nk_legacy_center_wide_value"
	gatePerNodeNK = "nk_per_node_rotated_value"
	gateForgedNK  = "nk_forged_value"
)

// newProxyGate builds the real gate against a real registry holding one rotated
// node, so the per-node path is exercised rather than a stub boolean.
func newProxyGate(t *testing.T) func(*http.Request) bool {
	t.Helper()
	ctx := context.Background()

	registry, err := noderegistry.New(filepath.Join(t.TempDir(), "nodes.json"), 0)
	if err != nil {
		t.Fatalf("noderegistry.New: %v", err)
	}
	if _, err := registry.Register(ctx, noderegistry.NodeInput{ID: "node-a", Name: "node-a"}); err != nil {
		t.Fatalf("register node: %v", err)
	}
	if err := registry.SetProxyTokenHash(ctx, "node-a", relay.HashCredential(gatePerNodeNK)); err != nil {
		t.Fatalf("set per-node nk_: %v", err)
	}

	manager, err := config.NewManager(config.Options{
		WorkspaceDir: t.TempDir(),
		HomeDir:      t.TempDir(),
	})
	if err != nil {
		t.Fatalf("config.NewManager: %v", err)
	}
	if _, err := manager.Update(ctx, config.UpdateRequest{Values: map[string]any{
		"web.token":                gateWebToken,
		"control.node_proxy_token": gateLegacyNK,
	}}); err != nil {
		t.Fatalf("seed manager: %v", err)
	}

	checker := httpapi.NewNodeProxyTokenChecker(manager, httpapi.NodeProxyTokenLookupFrom(registry))
	return nodeProxyAuthorize(manager, checker, nil)
}

func gateRequest(token string) *http.Request {
	req := httptest.NewRequest(http.MethodGet, "/control/nodes/node-a/proxy/meta", nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	return req
}

// TestProxyGateAcceptsPerNodeNKCredential is the property the migration depends on:
// a node that has rotated must still be able to reach the proxy surface, because
// that bridge is how it talks to every other node.
func TestProxyGateAcceptsPerNodeNKCredential(t *testing.T) {
	gate := newProxyGate(t)
	for _, tc := range []struct {
		name  string
		token string
		want  bool
	}{
		{"per-node nk_ (a rotated node)", gatePerNodeNK, true},
		{"center-wide nk_ (a node that has not rotated)", gateLegacyNK, true},
		{"center web token", gateWebToken, true},
		{"forged nk_", gateForgedNK, false},
		{"no credential", "", false},
	} {
		if got := gate(gateRequest(tc.token)); got != tc.want {
			t.Errorf("%s: accepted=%v, want %v", tc.name, got, tc.want)
		}
	}
}

// TestProxyGateAgreesWithReadSurfaceOnNodeCredentials guards the drift that caused
// the bug: two authenticators answering the same "is this a node credential?"
// question must not disagree, or a node's health depends on which URL it used.
func TestProxyGateAgreesWithReadSurfaceOnNodeCredentials(t *testing.T) {
	ctx := context.Background()
	registry, err := noderegistry.New(filepath.Join(t.TempDir(), "nodes.json"), 0)
	if err != nil {
		t.Fatalf("noderegistry.New: %v", err)
	}
	if _, err := registry.Register(ctx, noderegistry.NodeInput{ID: "node-a", Name: "node-a"}); err != nil {
		t.Fatalf("register node: %v", err)
	}
	if err := registry.SetProxyTokenHash(ctx, "node-a", relay.HashCredential(gatePerNodeNK)); err != nil {
		t.Fatalf("set per-node nk_: %v", err)
	}
	manager, err := config.NewManager(config.Options{WorkspaceDir: t.TempDir(), HomeDir: t.TempDir()})
	if err != nil {
		t.Fatalf("config.NewManager: %v", err)
	}
	if _, err := manager.Update(ctx, config.UpdateRequest{Values: map[string]any{
		"web.token":                gateWebToken,
		"control.node_proxy_token": gateLegacyNK,
	}}); err != nil {
		t.Fatalf("seed manager: %v", err)
	}

	// The read surface's own checker, and the gate wired with that same checker.
	readChecker := httpapi.NewNodeProxyTokenChecker(manager, httpapi.NodeProxyTokenLookupFrom(registry))
	gate := nodeProxyAuthorize(manager, readChecker, nil)

	for _, token := range []string{gatePerNodeNK, gateLegacyNK, gateForgedNK} {
		read, proxy := readChecker(gateRequest(token)), gate(gateRequest(token))
		if read != proxy {
			t.Errorf("token %q: read surface=%v but proxy gate=%v (surfaces must not disagree)", token, read, proxy)
		}
	}
}

// TestProxyGateAcceptsLegacyEvenWithNoNodesRegistered pins the fact that retiring
// nodes does NOT retire the credential.
//
// This is the property that decides whether the migration can be finished by
// deleting stale nodes: it cannot. The legacy branch is a string comparison against
// the configured value — it never consults the registry — so an empty registry is
// indistinguishable from a full one as far as the center-wide credential is
// concerned. A retired node that still holds the value keeps authenticating, and
// deleting it also destroys the record that would have shown it did.
func TestProxyGateAcceptsLegacyEvenWithNoNodesRegistered(t *testing.T) {
	ctx := context.Background()
	empty, err := noderegistry.New(filepath.Join(t.TempDir(), "nodes.json"), 0)
	if err != nil {
		t.Fatalf("noderegistry.New: %v", err)
	}
	// Deliberately no Register calls: this is the "all stale nodes retired" state.
	if nodes, err := empty.List(ctx); err != nil || len(nodes) != 0 {
		t.Fatalf("expected an empty registry, got %d nodes (err=%v)", len(nodes), err)
	}

	manager, err := config.NewManager(config.Options{WorkspaceDir: t.TempDir(), HomeDir: t.TempDir()})
	if err != nil {
		t.Fatalf("config.NewManager: %v", err)
	}
	if _, err := manager.Update(ctx, config.UpdateRequest{Values: map[string]any{
		"web.token":                gateWebToken,
		"control.node_proxy_token": gateLegacyNK,
	}}); err != nil {
		t.Fatalf("seed manager: %v", err)
	}

	checker := httpapi.NewNodeProxyTokenChecker(manager, httpapi.NodeProxyTokenLookupFrom(empty))
	gate := nodeProxyAuthorize(manager, checker, nil)
	if !gate(gateRequest(gateLegacyNK)) {
		t.Fatal("the center-wide credential must still be checked against the value alone, not the registry")
	}
}

// TestProxyGateReadsLiveConfigNotABootSnapshot pins the other half: P4a clears the
// center-wide value, and that must take effect without a restart. A gate holding a
// boot-time *config.Config would keep accepting the retired credential.
func TestProxyGateReadsLiveConfigNotABootSnapshot(t *testing.T) {
	ctx := context.Background()
	manager, err := config.NewManager(config.Options{WorkspaceDir: t.TempDir(), HomeDir: t.TempDir()})
	if err != nil {
		t.Fatalf("config.NewManager: %v", err)
	}
	if _, err := manager.Update(ctx, config.UpdateRequest{Values: map[string]any{
		"web.token":                gateWebToken,
		"control.node_proxy_token": gateLegacyNK,
	}}); err != nil {
		t.Fatalf("seed manager: %v", err)
	}

	gate := nodeProxyAuthorize(manager, nil, nil)
	if !gate(gateRequest(gateLegacyNK)) {
		t.Fatal("the configured center-wide credential must be accepted before it is cleared")
	}

	// Retire it, the way P4a does.
	if _, err := manager.Update(ctx, config.UpdateRequest{ClearSecrets: []string{"control.node_proxy_token"}}); err != nil {
		t.Fatalf("clear center-wide nk_: %v", err)
	}
	if gate(gateRequest(gateLegacyNK)) {
		t.Error("the retired center-wide credential is still accepted: the gate is holding a stale snapshot")
	}
}
