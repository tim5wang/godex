package relay

import (
	"context"
	"fmt"
	"net"
	"sync"
	"testing"
	"time"
)

// connectNodeWithAllowlist is connectNode with a specific egress allowlist, so the
// node-side refusal path can be exercised (connectNode allows "*:*").
func connectNodeWithAllowlist(t *testing.T, hub *Hub, wsURL, nodeID string, allow []string) *Agent {
	t.Helper()
	agent := NewAgent(AgentConfig{
		CenterURL:    wsURL,
		NodeID:       nodeID,
		Credential:   "ck_secret",
		Version:      "v1.2.0",
		ForwardAllow: allow,
	})
	if err := agent.Start(context.Background()); err != nil {
		t.Fatalf("agent start: %v", err)
	}
	t.Cleanup(func() { _ = agent.Stop(context.Background()) })
	waitOnline(t, hub, nodeID)
	return agent
}

// TestManagedTunnelRefusedTargetIsAudited is the managed-path half of the audit
// work.
//
// These tunnels dial IN-PROCESS (ForwardServer.dialStream), so they never pass
// through ForwardHandler: without an audit hook of their own, a target the node's
// egress allowlist refused left no trace on this path while the CLI path recorded
// one. bridge() simply closed the local connection and returned.
func TestManagedTunnelRefusedTargetIsAudited(t *testing.T) {
	hub, wsURL := serveRelayHub(t, func(nodeID, credential string) bool {
		return nodeID == "node-audit" && credential == "ck_secret"
	})
	// The node allows one target only, so the tunnel below is refused by the
	// node's own egress allowlist rather than by anything on the center.
	connectNodeWithAllowlist(t, hub, wsURL, "node-audit", []string{"allowed.internal:443"})

	server := NewForwardServer(hub)
	defer server.Shutdown()

	var mu sync.Mutex
	var attempts []ForwardAudit
	server.SetAuditRecorder(func(attempt ForwardAudit) {
		mu.Lock()
		defer mu.Unlock()
		attempts = append(attempts, attempt)
	})

	localPort := freeLocalPort(t)
	if _, err := server.Add(ForwardSpec{
		NodeID: "node-audit", LocalPort: localPort, Target: "169.254.169.254:80",
	}); err != nil {
		t.Fatalf("add: %v", err)
	}

	// One local connection is enough to drive one dial attempt; the failure is
	// what produces the record.
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", localPort), 5*time.Second)
	if err != nil {
		t.Fatalf("dial forward: %v", err)
	}
	defer conn.Close()
	// The local side is closed immediately on a failed dial, so this read ends.
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	_, _ = conn.Read(make([]byte, 1))

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		n := len(attempts)
		mu.Unlock()
		if n > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(attempts) == 0 {
		t.Fatal("a refused managed-tunnel dial left no audit record")
	}
	got := attempts[0]
	if got.Allowed {
		t.Errorf("refused dial was audited as allowed: %#v", got)
	}
	if got.Target != "169.254.169.254:80" {
		t.Errorf("target = %q, want the tunnel's configured target", got.Target)
	}
	if got.NodeID != "node-audit" {
		t.Errorf("node = %q, want node-audit", got.NodeID)
	}
	// The node's refusal reason must survive into the record, otherwise the audit
	// says "something failed" without saying it was the egress allowlist.
	if got.Reason == "" {
		t.Error("refusal carries no reason")
	}
	// No tenant: these tunnels are the operator's and their connections are
	// anonymous local sockets, so attributing one to a tenant would be a guess.
	if got.TenantID != "" || got.UserID != "" {
		t.Errorf("managed-tunnel record names an identity (%q,%q), want none", got.TenantID, got.UserID)
	}
}

// TestManagedTunnelAuditRecorderAbsentIsUnchanged pins the compatibility
// contract on this path too: no recorder wired means the tunnel behaves exactly
// as before.
func TestManagedTunnelAuditRecorderAbsentIsUnchanged(t *testing.T) {
	hub, wsURL := serveRelayHub(t, func(nodeID, credential string) bool {
		return nodeID == "node-noaudit" && credential == "ck_secret"
	})
	connectNodeWithAllowlist(t, hub, wsURL, "node-noaudit", []string{"allowed.internal:443"})

	server := NewForwardServer(hub)
	defer server.Shutdown()

	localPort := freeLocalPort(t)
	if _, err := server.Add(ForwardSpec{
		NodeID: "node-noaudit", LocalPort: localPort, Target: "169.254.169.254:80",
	}); err != nil {
		t.Fatalf("add: %v", err)
	}
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", localPort), 5*time.Second)
	if err != nil {
		t.Fatalf("dial forward: %v", err)
	}
	defer conn.Close()
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, _ = conn.Read(make([]byte, 1))

	// The tunnel must still be running (a failed dial is per connection, not a
	// tunnel-level error) and the absence of a recorder must not have changed it.
	statuses := server.List()
	if len(statuses) != 1 {
		t.Fatalf("tunnels = %d, want 1", len(statuses))
	}
	if statuses[0].State != ForwardStateRunning {
		t.Errorf("tunnel state = %q, want running: per-connection dial failure must not stop the listener", statuses[0].State)
	}
}
