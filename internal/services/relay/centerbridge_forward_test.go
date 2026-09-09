package relay

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestForwardServerCenterBridge verifies the center-bridge fallback: a tunnel
// targeting a node that is NOT connected to the local hub (it joined the
// center instead) is dialed through the center's forward endpoint, and its
// status is flagged via_center.
func TestForwardServerCenterBridge(t *testing.T) {
	// Center: hub + node-b's agent + forward endpoint (/api prefix stripped,
	// mirroring the webui delegation in cmd/godex/main.go).
	centerHub, centerWS := serveRelayHub(t, func(nodeID, credential string) bool {
		return nodeID == "node-b" && credential == "ck_secret"
	})
	connectNode(t, centerHub, centerWS, "node-b")
	centerMux := http.NewServeMux()
	centerMux.Handle("/control/nodes/{id}/forward", NewForwardHandler(centerHub, nil))
	centerSrv := httptest.NewServer(http.StripPrefix("/api", centerMux))
	t.Cleanup(centerSrv.Close)

	// Local instance A: its hub has NO node connections (node-b never dials it),
	// so the tunnel must fall back to the center bridge.
	localHub, _ := serveRelayHub(t, func(nodeID, credential string) bool { return false })
	server := NewForwardServer(localHub)
	server.SetCenterBridge(centerSrv.URL, "tok_center")
	defer server.Shutdown()

	echoAddr := startEchoServer(t)
	localPort := freeLocalPort(t)
	if _, err := server.Add(ForwardSpec{
		NodeID: "node-b", LocalPort: localPort, Target: echoAddr,
	}); err != nil {
		t.Fatalf("add: %v", err)
	}

	st, ok := server.Get(specIDOf(t, server))
	if !ok {
		t.Fatal("expected status")
	}
	if !st.ViaCenter {
		t.Fatalf("expected via_center=true for node not on local hub, got %+v", st)
	}

	// End-to-end: local connect → center bridge → node-b agent → echo server.
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", localPort), 5*time.Second)
	if err != nil {
		t.Fatalf("dial forward: %v", err)
	}
	defer conn.Close()
	payload := "hello-through-center-bridge"
	if _, err := conn.Write([]byte(payload)); err != nil {
		t.Fatalf("write: %v", err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	buf := make([]byte, 64)
	n, err := conn.Read(buf)
	if err != nil {
		t.Fatalf("read echo: %v", err)
	}
	if got := string(buf[:n]); got != payload {
		t.Fatalf("expected echo %q, got %q", payload, got)
	}
}

// TestForwardServerCheckViaCenter verifies Check reports a via-center tunnel as
// reachable when the node is on the center, and marks it offline when no center
// bridge is configured.
func TestForwardServerCheckViaCenter(t *testing.T) {
	centerHub, centerWS := serveRelayHub(t, func(nodeID, credential string) bool {
		return nodeID == "node-b" && credential == "ck_secret"
	})
	connectNode(t, centerHub, centerWS, "node-b")
	centerMux := http.NewServeMux()
	centerMux.Handle("/control/nodes/{id}/forward", NewForwardHandler(centerHub, nil))
	centerSrv := httptest.NewServer(http.StripPrefix("/api", centerMux))
	t.Cleanup(centerSrv.Close)

	localHub, _ := serveRelayHub(t, func(nodeID, credential string) bool { return false })
	server := NewForwardServer(localHub)
	defer server.Shutdown()

	echoAddr := startEchoServer(t)
	localPort := freeLocalPort(t)
	if _, err := server.Add(ForwardSpec{NodeID: "node-b", LocalPort: localPort, Target: echoAddr}); err != nil {
		t.Fatalf("add: %v", err)
	}

	// Without a bridge the check must fail on the node leg.
	result, err := server.Check(specIDOf(t, server))
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	if result.OK {
		t.Fatalf("expected check to fail without center bridge, got %+v", result)
	}

	// With the bridge configured the check must pass end to end.
	server.SetCenterBridge(centerSrv.URL, "tok_center")
	result, err = server.Check(specIDOf(t, server))
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	if !result.OK {
		t.Fatalf("expected check to pass via center bridge, got %+v", result)
	}
	for _, step := range result.Steps {
		if !step.OK {
			t.Fatalf("step %q not ok: %s", step.Name, step.Detail)
		}
	}
	// The target leg must have gone through the bridge (node leg says via center).
	foundNode := false
	for _, step := range result.Steps {
		if step.Name == "node" {
			foundNode = true
			if !contains(step.Detail, "center bridge") {
				t.Fatalf("node leg detail = %q, want mention of center bridge", step.Detail)
			}
		}
	}
	if !foundNode {
		t.Fatal("missing node leg in check result")
	}
}

func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && (haystack == needle || len(needle) == 0 || indexOf(haystack, needle) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

var _ = context.Background
