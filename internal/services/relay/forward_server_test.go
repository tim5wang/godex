package relay

import (
	"context"
	"fmt"
	"io"
	"net"
	"testing"
	"time"
)

// freeLocalPort grabs an ephemeral port for the forward listener.
func freeLocalPort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve port: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()
	return port
}

// startEchoServer starts a TCP echo server in-process and returns its address.
func startEchoServer(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("echo listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				_, _ = io.Copy(c, c)
			}(conn)
		}
	}()
	return ln.Addr().String()
}

// connectNode dials a node agent to the hub so the hub can reach node-side TCP.
func connectNode(t *testing.T, hub *Hub, wsURL, nodeID string) *Agent {
	t.Helper()
	agent := NewAgent(AgentConfig{
		CenterURL:    wsURL,
		NodeID:       nodeID,
		Credential:   "ck_secret",
		Version:      "v1.2.0",
		Caps:         []string{"chat"},
		ForwardAllow: []string{"*:*"}, // allow all targets for the test
	})
	if err := agent.Start(context.Background()); err != nil {
		t.Fatalf("agent start: %v", err)
	}
	t.Cleanup(func() { _ = agent.Stop(context.Background()) })
	waitOnline(t, hub, nodeID)
	return agent
}

func TestForwardServerAddListRemove(t *testing.T) {
	hub, _ := serveRelayHub(t, func(nodeID, credential string) bool {
		return nodeID == "node-a" && credential == "ck_secret"
	})
	server := NewForwardServer(hub)
	defer server.Shutdown()

	spec, err := server.Add(ForwardSpec{NodeID: "node-a", LocalPort: freeLocalPort(t), Target: "127.0.0.1:9999"})
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	if spec.ID == "" {
		t.Fatal("expected generated id")
	}
	statuses := server.List()
	if len(statuses) != 1 {
		t.Fatalf("expected 1 forward, got %d", len(statuses))
	}
	if statuses[0].State != ForwardStateRunning {
		t.Fatalf("expected running, got %q", statuses[0].State)
	}
	if !server.Remove(spec.ID) {
		t.Fatal("expected remove to succeed")
	}
	if len(server.List()) != 0 {
		t.Fatalf("expected empty list after remove, got %d", len(server.List()))
	}
	if server.Remove(spec.ID) {
		t.Fatal("expected second remove to fail")
	}
}

func TestForwardServerValidatesSpec(t *testing.T) {
	hub, _ := serveRelayHub(t, func(nodeID, credential string) bool { return true })
	server := NewForwardServer(hub)
	defer server.Shutdown()
	cases := []ForwardSpec{
		{NodeID: "", LocalPort: 3000, Target: "x:1"},
		{NodeID: "n", LocalPort: 0, Target: "x:1"},
		{NodeID: "n", LocalPort: 70000, Target: "x:1"},
		{NodeID: "n", LocalPort: 3000, Target: ""},
	}
	for i, spec := range cases {
		if _, err := server.Add(spec); err == nil {
			t.Fatalf("case %d: expected error for %+v", i, spec)
		}
	}
}

func TestForwardServerBridgesTCPConnection(t *testing.T) {
	hub, wsURL := serveRelayHub(t, func(nodeID, credential string) bool {
		return nodeID == "node-b" && credential == "ck_secret"
	})
	connectNode(t, hub, wsURL, "node-b")
	server := NewForwardServer(hub)
	defer server.Shutdown()

	echoAddr := startEchoServer(t)
	localPort := freeLocalPort(t)
	if _, err := server.Add(ForwardSpec{
		NodeID: "node-b", LocalPort: localPort, Target: echoAddr,
	}); err != nil {
		t.Fatalf("add: %v", err)
	}

	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", localPort), 5*time.Second)
	if err != nil {
		t.Fatalf("dial forward: %v", err)
	}
	defer conn.Close()
	payload := "hello-through-relay"
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

	// The tunnel should report one active connection while it is open.
	st, ok := server.Get(specIDOf(t, server))
	if !ok {
		t.Fatal("expected status")
	}
	if st.ActiveConns < 1 {
		t.Fatalf("expected >=1 active conn, got %d", st.ActiveConns)
	}
}

func TestForwardServerCheckTargetUnreachable(t *testing.T) {
	hub, wsURL := serveRelayHub(t, func(nodeID, credential string) bool {
		return nodeID == "node-c" && credential == "ck_secret"
	})
	connectNode(t, hub, wsURL, "node-c")
	server := NewForwardServer(hub)
	defer server.Shutdown()

	// Eavesdrop on an unused port so the dial fails on the node side.
	unused := startEchoServer(t)
	_ = unused
	if _, err := server.Add(ForwardSpec{
		NodeID: "node-c", LocalPort: freeLocalPort(t), Target: "127.0.0.1:1",
	}); err != nil {
		t.Fatalf("add: %v", err)
	}
	result, err := server.Check(specIDOf(t, server))
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	if result.OK {
		t.Fatal("expected check to fail (target unreachable)")
	}
	if len(result.Steps) != 3 {
		t.Fatalf("expected 3 steps, got %d: %+v", len(result.Steps), result.Steps)
	}
	if !result.Steps[0].OK || !result.Steps[1].OK {
		t.Fatalf("expected listener+node legs ok, got %+v", result.Steps)
	}
	if result.Steps[2].OK {
		t.Fatalf("expected target leg to fail, got %+v", result.Steps[2])
	}
}

func TestForwardServerCheckNodeOffline(t *testing.T) {
	hub, _ := serveRelayHub(t, func(nodeID, credential string) bool {
		return nodeID == "ghost" && credential == "ck_secret"
	})
	server := NewForwardServer(hub)
	defer server.Shutdown()
	if _, err := server.Add(ForwardSpec{
		NodeID: "ghost", LocalPort: freeLocalPort(t), Target: "127.0.0.1:1",
	}); err != nil {
		t.Fatalf("add: %v", err)
	}
	result, err := server.Check(specIDOf(t, server))
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	if result.OK {
		t.Fatal("expected check to fail (node offline)")
	}
	if len(result.Steps) != 2 {
		t.Fatalf("expected 2 steps (listener ok, node offline), got %+v", result.Steps)
	}
	if !result.Steps[0].OK || result.Steps[1].OK {
		t.Fatalf("expected listener ok / node offline, got %+v", result.Steps)
	}
}

func TestForwardServerCheckAllLegsOK(t *testing.T) {
	hub, wsURL := serveRelayHub(t, func(nodeID, credential string) bool {
		return nodeID == "node-d" && credential == "ck_secret"
	})
	connectNode(t, hub, wsURL, "node-d")
	server := NewForwardServer(hub)
	defer server.Shutdown()

	echoAddr := startEchoServer(t)
	if _, err := server.Add(ForwardSpec{
		NodeID: "node-d", LocalPort: freeLocalPort(t), Target: echoAddr,
	}); err != nil {
		t.Fatalf("add: %v", err)
	}
	result, err := server.Check(specIDOf(t, server))
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	if !result.OK {
		t.Fatalf("expected check ok, got %+v", result.Steps)
	}
	if len(result.Steps) != 3 {
		t.Fatalf("expected 3 steps, got %+v", result.Steps)
	}
	for i, step := range result.Steps {
		if !step.OK {
			t.Fatalf("step %d should be ok: %+v", i, step)
		}
	}
	if result.Steps[2].LatencyMs < 0 {
		t.Fatalf("expected non-negative latency, got %d", result.Steps[2].LatencyMs)
	}
	// The tunnel must record the check timestamp + latency.
	st, ok := server.Get(specIDOf(t, server))
	if !ok || st.LastCheckedAt.IsZero() {
		t.Fatalf("expected LastCheckedAt recorded, got %+v", st)
	}
}

// specIDOf returns the id of the first (only) managed tunnel.
func specIDOf(t *testing.T, server *ForwardServer) string {
	t.Helper()
	statuses := server.List()
	if len(statuses) != 1 {
		t.Fatalf("expected exactly 1 forward, got %d", len(statuses))
	}
	return statuses[0].ID
}
